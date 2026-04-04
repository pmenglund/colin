package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
)

func (o *Orchestrator) handleCommentEvent(ctx context.Context, entry *runningEntry, event codex.Event) {
	if entry == nil {
		return
	}

	runType := event.RunType
	if runType == "" {
		runType = runTypeForState(o, entry.issue.State)
	}
	entry.comment = commentState(entry.issue, entry.comment, runType)

	if shouldCreateRootComment(event.Event) && entry.comment.RootCommentID == "" {
		o.createRootComment(ctx, entry, event)
	}
	if body, ok := o.replyBodyForEvent(entry, event); ok {
		o.postReply(ctx, entry, body)
	}
}

func (o *Orchestrator) createRootComment(ctx context.Context, entry *runningEntry, event codex.Event) {
	if o.runtime.Tracker == nil {
		return
	}
	body := rootCommentBody(entry, event)
	if strings.TrimSpace(body) == "" {
		return
	}
	body = colinCommentBody(body)

	commentID, err := o.withCommentTimeout(ctx, func(ctx context.Context) (string, error) {
		return o.runtime.Tracker.CreateIssueComment(ctx, entry.issue.ID, body)
	})
	if err != nil {
		o.logger.Warn(
			"failed to create Linear progress comment",
			"issue_id", entry.issue.ID,
			"issue_identifier", entry.identifier,
			"run_type", entry.comment.RunType,
			"error", err,
		)
		return
	}
	entry.comment.RootCommentID = commentID
	entry.issue = o.persistIssueCommentMetadata(ctx, entry.issue, "", commentID, commentID)
	o.logger.Info(
		"created Linear progress comment",
		"issue_id", entry.issue.ID,
		"issue_identifier", entry.identifier,
		"run_type", entry.comment.RunType,
		"comment_id", commentID,
	)
}

func (o *Orchestrator) postReply(ctx context.Context, entry *runningEntry, body string) string {
	if o.runtime.Tracker == nil || entry == nil || entry.comment == nil || entry.comment.RootCommentID == "" || strings.TrimSpace(body) == "" {
		return ""
	}
	body = colinCommentBody(body)

	commentID, err := o.withCommentTimeout(ctx, func(ctx context.Context) (string, error) {
		return o.runtime.Tracker.CreateCommentReply(ctx, entry.issue.ID, entry.comment.RootCommentID, body)
	})
	if err != nil {
		o.logger.Warn(
			"failed to create Linear progress reply",
			"issue_id", entry.issue.ID,
			"issue_identifier", entry.identifier,
			"comment_id", entry.comment.RootCommentID,
			"error", err,
		)
		return ""
	}
	entry.issue = o.persistIssueCommentMetadata(ctx, entry.issue, "", "", commentID)
	return commentID
}

func (o *Orchestrator) postIssueStatus(ctx context.Context, issue domain.Issue, identifier string, comment *commentThreadState, body string) (domain.Issue, *commentThreadState) {
	issue, comment, _ = o.postIssueStatusDetailed(ctx, issue, identifier, comment, body)
	return issue, comment
}

func (o *Orchestrator) postIssueStatusDetailed(ctx context.Context, issue domain.Issue, identifier string, comment *commentThreadState, body string) (domain.Issue, *commentThreadState, string) {
	if o.runtime.Tracker == nil || strings.TrimSpace(body) == "" {
		return issue, comment, ""
	}
	comment = commentState(issue, comment, runTypeForState(o, issue.State))
	body = colinCommentBody(body)

	if comment.RootCommentID == "" {
		commentID, err := o.withCommentTimeout(ctx, func(ctx context.Context) (string, error) {
			return o.runtime.Tracker.CreateIssueComment(ctx, issue.ID, body)
		})
		if err != nil {
			o.logger.Warn(
				"failed to create Linear status comment",
				"issue_id", issue.ID,
				"issue_identifier", identifier,
				"error", err,
			)
			return issue, comment, ""
		}
		comment.RootCommentID = commentID
		issue = o.persistIssueCommentMetadata(ctx, issue, "", commentID, commentID)
		return issue, comment, commentID
	}

	commentID, err := o.withCommentTimeout(ctx, func(ctx context.Context) (string, error) {
		return o.runtime.Tracker.CreateCommentReply(ctx, issue.ID, comment.RootCommentID, body)
	})
	if err != nil {
		o.logger.Warn(
			"failed to create Linear status reply",
			"issue_id", issue.ID,
			"issue_identifier", identifier,
			"comment_id", comment.RootCommentID,
			"error", err,
		)
		return issue, comment, ""
	}
	issue = o.persistIssueCommentMetadata(ctx, issue, "", "", commentID)
	return issue, comment, commentID
}

func (o *Orchestrator) withCommentTimeout(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
	commentCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return fn(commentCtx)
}

func (o *Orchestrator) persistSummaryCommentMetadata(ctx context.Context, issue domain.Issue, commentID string) domain.Issue {
	return o.persistIssueCommentMetadata(ctx, issue, strings.TrimSpace(commentID), "", "")
}

func (o *Orchestrator) persistProgressRootCommentMetadata(ctx context.Context, issue domain.Issue, commentID string) domain.Issue {
	return o.persistIssueCommentMetadata(ctx, issue, "", strings.TrimSpace(commentID), "")
}

func (o *Orchestrator) persistIssueCommentMetadata(ctx context.Context, issue domain.Issue, summaryCommentID string, progressRootCommentID string, colinCommentID string) domain.Issue {
	return o.persistIssueMetadata(ctx, issue, summaryCommentID, progressRootCommentID, nil, colinCommentID)
}

func (o *Orchestrator) persistIssueOutputMetadata(ctx context.Context, issue domain.Issue, output []domain.OutputLog) domain.Issue {
	return o.persistIssueMetadata(ctx, issue, "", "", output, "")
}

func (o *Orchestrator) persistIssueMetadata(ctx context.Context, issue domain.Issue, summaryCommentID string, progressRootCommentID string, output []domain.OutputLog, colinCommentID string) domain.Issue {
	if o.runtime.Tracker == nil {
		return issue
	}
	if strings.TrimSpace(summaryCommentID) == "" && strings.TrimSpace(progressRootCommentID) == "" && len(output) == 0 && strings.TrimSpace(colinCommentID) == "" {
		return issue
	}
	metadata := domain.ColinMetadata{
		LastSummaryCommentID:  strings.TrimSpace(summaryCommentID),
		ProgressRootCommentID: strings.TrimSpace(progressRootCommentID),
	}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	if strings.TrimSpace(summaryCommentID) != "" {
		metadata.LastSummaryCommentID = strings.TrimSpace(summaryCommentID)
	}
	if strings.TrimSpace(progressRootCommentID) != "" {
		metadata.ProgressRootCommentID = strings.TrimSpace(progressRootCommentID)
	}
	if len(output) > 0 {
		metadata.CodexOutput = append([]domain.OutputLog(nil), output...)
	}
	if trimmed := strings.TrimSpace(colinCommentID); trimmed != "" {
		metadata.ColinCommentIDs = appendUniqueStrings(metadata.ColinCommentIDs, trimmed)
	}
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	persisted, err := o.runtime.Tracker.UpsertIssueMetadata(ctx, issue.ID, metadata)
	if err != nil {
		o.logger.Warn(
			"failed to persist summary comment metadata",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"comment_id", summaryCommentID,
			"error", err,
		)
		issue.ColinMetadata = &metadata
		return issue
	}
	issue.ColinMetadata = &persisted
	return issue
}

func appendUniqueStrings(values []string, items ...string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values)+len(items))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func shouldCreateRootComment(eventName string) bool {
	switch eventName {
	case codex.EventSessionStarted, codex.EventReviewPublishStarted, codex.EventMergeStarted, codex.EventRunFailed:
		return true
	default:
		return false
	}
}

func rootCommentBody(entry *runningEntry, event codex.Event) string {
	lines := []string{
		"Colin started work on this issue.",
		"",
		fmt.Sprintf("- Run type: `%s`", event.RunType),
		fmt.Sprintf("- Attempt: `%d`", event.Attempt),
	}
	if event.Workspace != "" {
		lines = append(lines, fmt.Sprintf("- Workspace: `%s`", event.Workspace))
	}
	if event.SessionID != "" {
		lines = append(lines, fmt.Sprintf("- Session ID: `%s`", event.SessionID))
	}
	if event.ThreadID != "" {
		lines = append(lines, fmt.Sprintf("- Thread ID: `%s`", event.ThreadID))
	}
	if event.Branch != "" {
		lines = append(lines, fmt.Sprintf("- Branch: `%s`", event.Branch))
	}
	if event.BaseRef != "" {
		lines = append(lines, fmt.Sprintf("- Base ref: `%s`", event.BaseRef))
	}
	return strings.Join(lines, "\n")
}

func colinCommentBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(body), "[colin]") {
		return body
	}
	return "[colin] " + body
}

func commentState(issue domain.Issue, comment *commentThreadState, runType string) *commentThreadState {
	if comment == nil {
		comment = &commentThreadState{}
	}
	comment.RunType = runType
	if comment.RootCommentID == "" && issue.ColinMetadata != nil {
		comment.RootCommentID = strings.TrimSpace(issue.ColinMetadata.ProgressRootCommentID)
	}
	return comment
}

func (o *Orchestrator) replyBodyForEvent(entry *runningEntry, event codex.Event) (string, bool) {
	switch event.Event {
	case codex.EventIssueStateRefreshed:
		return fmt.Sprintf(
			"Turn finished.\n\n- Duration: `%s`\n- Previous state: `%s`\n- Current state: `%s`",
			event.Duration.Round(time.Millisecond),
			fallbackString(event.PrevState, "unknown"),
			fallbackString(event.State, "unknown"),
		), true
	case codex.EventContinuationNeeded:
		return fmt.Sprintf("Issue is still active in `%s`; Colin is continuing work in the same run.", fallbackString(event.State, "unknown")), true
	case codex.EventReviewPublishDone:
		lines := []string{"Review automation completed."}
		if event.Branch != "" {
			lines = append(lines, "", fmt.Sprintf("- Branch: `%s`", event.Branch))
		}
		if event.BaseRef != "" {
			lines = append(lines, fmt.Sprintf("- Base ref: `%s`", event.BaseRef))
		}
		if event.PRNumber > 0 {
			lines = append(lines, fmt.Sprintf("- PR: `#%d`", event.PRNumber))
		}
		if event.PRURL != "" {
			lines = append(lines, fmt.Sprintf("- PR URL: %s", event.PRURL))
		}
		return strings.Join(lines, "\n"), true
	case codex.EventMergeDone:
		lines := []string{"Merge automation completed."}
		if event.PRNumber > 0 {
			lines = append(lines, "", fmt.Sprintf("- PR: `#%d`", event.PRNumber))
		}
		if event.PRURL != "" {
			lines = append(lines, fmt.Sprintf("- PR URL: %s", event.PRURL))
		}
		if event.Action != "" {
			lines = append(lines, fmt.Sprintf("- Action: `%s`", event.Action))
		}
		return strings.Join(lines, "\n"), true
	case codex.EventRetryScheduled:
		if event.Message == "" {
			return "", false
		}
		return event.Message, true
	case codex.EventRetryFired:
		if event.Message == "" {
			return "", false
		}
		return event.Message, true
	case codex.EventRunFailed:
		if event.Message == "" {
			return "", false
		}
		return fmt.Sprintf("Run failed.\n\n- Error: %s", event.Message), true
	case codex.EventRunSucceeded:
		if event.RunType == codex.RunTypeCoding && !o.issueStateIsHandoffOrTerminal(event.State) {
			return "", false
		}
		return fmt.Sprintf("Run completed successfully.\n\n- Current state: `%s`", fallbackString(event.State, "unknown")), true
	default:
		return "", false
	}
}

func (o *Orchestrator) postRetryScheduledReply(ctx context.Context, comment *commentThreadState, issueID string, identifier string, attempt int, delay time.Duration, errText string, notifyLinear bool) {
	if !notifyLinear || comment == nil || comment.RootCommentID == "" {
		return
	}
	entry := &runningEntry{
		issue:      domainIssue(issueID, identifier),
		comment:    comment,
		identifier: identifier,
	}
	message := fmt.Sprintf("Colin scheduled retry attempt `%d` in `%s`.", attempt, delay.Round(time.Second))
	if strings.TrimSpace(errText) != "" {
		message += fmt.Sprintf("\n\n- Reason: %s", errText)
	}
	o.postReply(ctx, entry, message)
}

func (o *Orchestrator) postRetryFiredReply(ctx context.Context, issueID string, state *retryState) {
	if state == nil || !state.notifyLinear || state.comment == nil || state.comment.RootCommentID == "" {
		return
	}
	entry := &runningEntry{
		issue:      domainIssue(issueID, state.entry.Identifier),
		identifier: state.entry.Identifier,
		comment:    state.comment,
	}
	o.postReply(ctx, entry, fmt.Sprintf("Colin is starting retry attempt `%d`.", state.entry.Attempt))
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func runTypeForState(o *Orchestrator, state string) string {
	switch {
	case o.isPublishState(state):
		return codex.RunTypeReviewPublish
	case o.isMergeState(state):
		return codex.RunTypeMerge
	default:
		return codex.RunTypeCoding
	}
}

func (o *Orchestrator) issueStateIsHandoffOrTerminal(state string) bool {
	return o.isPublishState(state) || o.isMergeState(state) || o.isTerminal(state)
}

func domainIssue(issueID string, identifier string) domain.Issue {
	return domain.Issue{ID: issueID, Identifier: identifier}
}
