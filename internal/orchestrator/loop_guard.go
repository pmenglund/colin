package orchestrator

import (
	"context"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
)

const loopFailureThreshold = 3

type loopFailure struct {
	runType     string
	state       string
	reason      string
	fingerprint string
}

func buildLoopFailure(entry *runningEntry, result codex.Result) (domain.Issue, loopFailure) {
	issue := result.Issue
	if strings.TrimSpace(issue.ID) == "" {
		issue = entry.issue
	}
	if strings.TrimSpace(issue.Identifier) == "" {
		issue.Identifier = entry.identifier
	}
	if strings.TrimSpace(issue.State) == "" {
		issue.State = entry.issue.State
	}
	if issue.ColinMetadata == nil {
		issue.ColinMetadata = entry.issue.ColinMetadata
	}
	if len(issue.Labels) == 0 && len(entry.issue.Labels) > 0 {
		issue.Labels = append([]string(nil), entry.issue.Labels...)
	}

	runType := strings.TrimSpace(result.RunType)
	if runType == "" {
		runType = strings.TrimSpace(entry.runType)
	}
	state := strings.TrimSpace(issue.State)
	reason := normalizeLoopFailureReason(errorString(result.Err))
	if reason == "" {
		reason = normalizeLoopFailureReason(result.Status)
	}
	return issue, loopFailure{
		runType:     runType,
		state:       state,
		reason:      reason,
		fingerprint: buildLoopFailureFingerprint(runType, state, reason),
	}
}

func normalizeLoopFailureReason(reason string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(reason)), " ")
}

func buildLoopFailureFingerprint(runType string, state string, reason string) string {
	return strings.Join([]string{
		strings.TrimSpace(runType),
		strings.TrimSpace(state),
		normalizeLoopFailureReason(reason),
	}, "\n")
}

func hasIssueLabel(issue domain.Issue, labelName string) bool {
	want := strings.ToLower(strings.TrimSpace(labelName))
	if want == "" {
		return false
	}
	for _, label := range issue.Labels {
		if strings.EqualFold(strings.TrimSpace(label), want) {
			return true
		}
	}
	return false
}

func hasPausedLoopMetadata(issue domain.Issue) bool {
	if issue.ColinMetadata == nil {
		return false
	}
	metadata := issue.ColinMetadata
	return metadata.PausedAt != nil ||
		strings.TrimSpace(metadata.PausedRunType) != "" ||
		strings.TrimSpace(metadata.PausedState) != "" ||
		strings.TrimSpace(metadata.PausedReason) != ""
}

func clearLoopMetadata(metadata *domain.ColinMetadata) {
	if metadata == nil {
		return
	}
	metadata.LoopFailureFingerprint = ""
	metadata.LoopFailureCount = 0
	metadata.PausedAt = nil
	metadata.PausedRunType = ""
	metadata.PausedState = ""
	metadata.PausedReason = ""
}

func (o *Orchestrator) clearLoopState(ctx context.Context, issue domain.Issue) domain.Issue {
	metadata := loopMetadata(issue)
	if strings.TrimSpace(metadata.LoopFailureFingerprint) == "" &&
		metadata.LoopFailureCount == 0 &&
		metadata.PausedAt == nil &&
		strings.TrimSpace(metadata.PausedRunType) == "" &&
		strings.TrimSpace(metadata.PausedState) == "" &&
		strings.TrimSpace(metadata.PausedReason) == "" {
		return issue
	}
	clearLoopMetadata(&metadata)
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return o.persistLoopMetadata(ctx, issue, metadata)
}

func (o *Orchestrator) clearPausedLoopMetadataIfUnpaused(ctx context.Context, issue domain.Issue) domain.Issue {
	if hasIssueLabel(issue, domain.PausedIssueLabel) || !hasPausedLoopMetadata(issue) {
		return issue
	}
	return o.clearLoopState(ctx, issue)
}

func (o *Orchestrator) recordLoopFailure(ctx context.Context, issue domain.Issue, failure loopFailure) domain.Issue {
	metadata := loopMetadata(issue)
	if metadata.LoopFailureFingerprint == failure.fingerprint {
		metadata.LoopFailureCount++
	} else {
		metadata.LoopFailureFingerprint = failure.fingerprint
		metadata.LoopFailureCount = 1
	}
	metadata.PausedAt = nil
	metadata.PausedRunType = ""
	metadata.PausedState = ""
	metadata.PausedReason = ""
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return o.persistLoopMetadata(ctx, issue, metadata)
}

func (o *Orchestrator) pauseIssueForLoop(ctx context.Context, issue domain.Issue, failure loopFailure) (domain.Issue, bool) {
	if o.runtime.Tracker == nil {
		return issue, false
	}
	if err := o.runtime.Tracker.EnsureIssueLabel(ctx, domain.PausedIssueLabel); err != nil {
		o.logger.Warn("failed to ensure paused label", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return issue, false
	}
	if err := o.runtime.Tracker.AddIssueLabel(ctx, issue.ID, domain.PausedIssueLabel); err != nil {
		o.logger.Warn("failed to add paused label", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return issue, false
	}

	if !hasIssueLabel(issue, domain.PausedIssueLabel) {
		issue.Labels = append(issue.Labels, domain.PausedIssueLabel)
	}
	metadata := loopMetadata(issue)
	now := time.Now().UTC()
	metadata.PausedAt = &now
	metadata.PausedRunType = failure.runType
	metadata.PausedState = failure.state
	metadata.PausedReason = failure.reason
	metadata.UpdatedAt = &now
	issue = o.persistLoopMetadata(ctx, issue, metadata)
	return issue, true
}

func (o *Orchestrator) persistLoopMetadata(ctx context.Context, issue domain.Issue, metadata domain.ColinMetadata) domain.Issue {
	if o.runtime.Tracker == nil {
		issue.ColinMetadata = &metadata
		return issue
	}
	persisted, err := o.runtime.Tracker.UpsertIssueMetadata(ctx, issue.ID, metadata)
	if err != nil {
		o.logger.Warn("failed to persist loop metadata", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		issue.ColinMetadata = &metadata
		return issue
	}
	issue.ColinMetadata = &persisted
	return issue
}

func loopMetadata(issue domain.Issue) domain.ColinMetadata {
	if issue.ColinMetadata != nil {
		return *issue.ColinMetadata
	}
	return domain.ColinMetadata{}
}

func buildLoopPausedSummary(failure loopFailure) string {
	lines := []string{
		"Colin paused automation after 3 identical failures and added the `paused` label.",
		"",
		"- Run type: `" + failure.runType + "`",
		"- State: `" + failure.state + "`",
		"- Failure threshold: `3`",
		"- Reason: " + failure.reason,
		"- Resume: remove the `paused` label after fixing the underlying problem.",
	}
	return strings.Join(lines, "\n")
}
