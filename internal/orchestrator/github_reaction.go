package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
)

func (o *Orchestrator) syncGitHubReviewFollowUps(ctx context.Context, issues []domain.Issue, now time.Time) ([]domain.Issue, []domain.Issue) {
	if len(issues) == 0 || o.runtime.Tracker == nil || o.runtime.Repo == nil || o.runtime.Workspace == nil {
		return issues, nil
	}
	updated := append([]domain.Issue(nil), issues...)
	started := make([]domain.Issue, 0, 1)
	for i, issue := range updated {
		next, queued := o.syncGitHubReviewFollowUp(ctx, issue, now)
		updated[i] = next
		if queued {
			started = append(started, next)
		}
	}
	return updated, started
}

func (o *Orchestrator) syncGitHubReviewFollowUp(ctx context.Context, issue domain.Issue, now time.Time) (domain.Issue, bool) {
	if !strings.EqualFold(strings.TrimSpace(issue.State), "Review") || !o.isPublishState(issue.State) {
		return issue, false
	}
	if hasIssueLabel(issue, domain.PausedIssueLabel) {
		return issue, false
	}
	if _, ok := o.running[issue.ID]; ok {
		return issue, false
	}
	if _, ok := o.claimed[issue.ID]; ok {
		return issue, false
	}
	if _, ok := o.retrying[issue.ID]; ok {
		return issue, false
	}
	if !hasIssueReviewSyncPullRequestSignal(issue) && !hasQueuedReviewFollowUps(issue) && !hasPendingReviewFollowUp(issue) {
		return issue, false
	}

	workspace, err := o.runtime.Workspace.Ensure(ctx, issue)
	if err != nil {
		o.logger.Warn("failed to prepare workspace for github review follow-up scan", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return issue, false
	}
	scan, err := o.runtime.Repo.ReviewFollowUpScan(ctx, issue, workspace.Path)
	if err != nil {
		o.logger.Warn("failed to scan github review follow-up approvals", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return issue, false
	}
	if scan.PullRequest.Number > 0 || strings.TrimSpace(scan.PullRequest.URL) != "" {
		issue.PullRequest = &scan.PullRequest
	}

	threadsByID := make(map[string]domain.ReviewThread, len(scan.Threads))
	for _, thread := range scan.Threads {
		threadsByID[strings.TrimSpace(thread.ID)] = thread
	}

	metadata := reviewFollowUpMetadata(issue)
	changed := false
	if clearStalePendingReviewFollowUp(&metadata, threadsByID) {
		changed = true
	}
	if pruneQueuedReviewFollowUps(&metadata, threadsByID) {
		changed = true
	}

	approvals := append([]repoops.ReviewCommentApproval(nil), scan.Approvals...)
	sort.Slice(approvals, func(i, j int) bool {
		return compareReviewReactionIDs(approvals[i].ReactionID, approvals[j].ReactionID) < 0
	})
	for _, approval := range approvals {
		commentID := strings.TrimSpace(approval.CommentID)
		if commentID == "" || strings.TrimSpace(approval.Thread.ID) == "" {
			continue
		}
		if !reviewReactionIDGreater(approval.ReactionID, metadata.ReviewReactionWatermarks[commentID]) {
			continue
		}
		if metadata.ReviewReactionWatermarks == nil {
			metadata.ReviewReactionWatermarks = map[string]string{}
		}
		metadata.ReviewReactionWatermarks[commentID] = strings.TrimSpace(approval.ReactionID)
		changed = true
		if queuedOrPendingReviewComment(metadata, commentID) {
			continue
		}
		requestedAt := now
		metadata.QueuedReviewFollowUps = append(metadata.QueuedReviewFollowUps, domain.PendingReviewFollowUp{
			ThreadID:    strings.TrimSpace(approval.Thread.ID),
			CommentID:   commentID,
			ReactionID:  strings.TrimSpace(approval.ReactionID),
			Reactor:     strings.TrimSpace(approval.Reactor),
			RequestedAt: &requestedAt,
		})
	}

	if hasPendingReviewFollowUpMetadata(metadata) {
		if changed {
			issue = o.persistReviewFollowUpMetadata(ctx, issue, metadata)
		}
		return o.startPendingReviewFollowUp(ctx, issue)
	}

	if len(metadata.QueuedReviewFollowUps) == 0 {
		if changed {
			issue = o.persistReviewFollowUpMetadata(ctx, issue, metadata)
		}
		return issue, false
	}

	next := metadata.QueuedReviewFollowUps[0]
	metadata.QueuedReviewFollowUps = append([]domain.PendingReviewFollowUp(nil), metadata.QueuedReviewFollowUps[1:]...)
	if next.RequestedAt == nil {
		requestedAt := now
		next.RequestedAt = &requestedAt
	}
	metadata.PendingReviewThreadID = strings.TrimSpace(next.ThreadID)
	metadata.PendingReviewCommentID = strings.TrimSpace(next.CommentID)
	metadata.PendingReviewReactionID = strings.TrimSpace(next.ReactionID)
	metadata.PendingReviewReactor = strings.TrimSpace(next.Reactor)
	metadata.PendingReviewRequestedAt = next.RequestedAt
	return o.activatePendingReviewFollowUp(ctx, issue, reviewFollowUpMetadata(issue), metadata)
}

func (o *Orchestrator) startPendingReviewFollowUp(ctx context.Context, issue domain.Issue) (domain.Issue, bool) {
	if !hasPendingReviewFollowUp(issue) || o.runtime.Tracker == nil {
		return issue, false
	}
	previousState := issue.State
	targetState := targetedReviewFollowUpState(o.runtime.Config.Tracker.ActiveStates)
	if err := o.runtime.Tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
		o.logger.Warn("failed to move issue for github review follow-up", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", targetState, "error", err)
		return issue, false
	}
	issue.State = targetState
	o.applyObservedDashboardIssueTransition(issue, previousState, issue.State)
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	delete(o.completed, issue.ID)
	issue, _ = o.postIssueStatus(ctx, issue, issue.Identifier, nil, fmt.Sprintf(
		"GitHub collaborator `%s` approved the invited Codex review suggestion, so Colin moved the issue to `%s` to address that PR thread.",
		pendingReviewReactor(issue),
		targetState,
	))
	return issue, true
}

func (o *Orchestrator) activatePendingReviewFollowUp(ctx context.Context, issue domain.Issue, previous domain.ColinMetadata, next domain.ColinMetadata) (domain.Issue, bool) {
	issue = o.persistReviewFollowUpMetadata(ctx, issue, next)
	updated, started := o.startPendingReviewFollowUp(ctx, issue)
	if started {
		return updated, true
	}
	return o.persistReviewFollowUpMetadata(ctx, issue, previous), false
}

func targetedReviewFollowUpState(states []string) string {
	for _, state := range states {
		if strings.EqualFold(strings.TrimSpace(state), "todo") {
			return state
		}
	}
	if len(states) == 0 {
		return "Todo"
	}
	return strings.TrimSpace(states[0])
}

func (o *Orchestrator) persistReviewFollowUpMetadata(ctx context.Context, issue domain.Issue, metadata domain.ColinMetadata) domain.Issue {
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	if o.runtime.Tracker == nil {
		issue.ColinMetadata = &metadata
		return issue
	}
	persisted, err := o.runtime.Tracker.UpsertIssueMetadata(ctx, issue.ID, metadata)
	if err != nil {
		o.logger.Warn("failed to persist github review follow-up metadata", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		issue.ColinMetadata = &metadata
		return issue
	}
	issue.ColinMetadata = &persisted
	return issue
}

func (o *Orchestrator) clearPendingReviewFollowUp(ctx context.Context, issue domain.Issue) domain.Issue {
	metadata := reviewFollowUpMetadata(issue)
	if !clearActivePendingReviewFollowUp(&metadata) {
		return issue
	}
	return o.persistReviewFollowUpMetadata(ctx, issue, metadata)
}

func reviewFollowUpMetadata(issue domain.Issue) domain.ColinMetadata {
	if issue.ColinMetadata != nil {
		return *issue.ColinMetadata
	}
	return domain.ColinMetadata{}
}

func clearActivePendingReviewFollowUp(metadata *domain.ColinMetadata) bool {
	if metadata == nil {
		return false
	}
	if !hasPendingReviewFollowUpMetadata(*metadata) {
		return false
	}
	metadata.PendingReviewThreadID = ""
	metadata.PendingReviewCommentID = ""
	metadata.PendingReviewReactionID = ""
	metadata.PendingReviewReactor = ""
	metadata.PendingReviewRequestedAt = nil
	return true
}

func clearStalePendingReviewFollowUp(metadata *domain.ColinMetadata, threadsByID map[string]domain.ReviewThread) bool {
	if metadata == nil || !hasPendingReviewFollowUpMetadata(*metadata) {
		return false
	}
	if _, ok := threadsByID[strings.TrimSpace(metadata.PendingReviewThreadID)]; ok {
		return false
	}
	return clearActivePendingReviewFollowUp(metadata)
}

func pruneQueuedReviewFollowUps(metadata *domain.ColinMetadata, threadsByID map[string]domain.ReviewThread) bool {
	if metadata == nil || len(metadata.QueuedReviewFollowUps) == 0 {
		return false
	}
	filtered := make([]domain.PendingReviewFollowUp, 0, len(metadata.QueuedReviewFollowUps))
	seenComments := make(map[string]struct{}, len(metadata.QueuedReviewFollowUps))
	changed := false
	for _, item := range metadata.QueuedReviewFollowUps {
		threadID := strings.TrimSpace(item.ThreadID)
		commentID := strings.TrimSpace(item.CommentID)
		if threadID == "" || commentID == "" {
			changed = true
			continue
		}
		if _, ok := threadsByID[threadID]; !ok {
			changed = true
			continue
		}
		if _, dup := seenComments[commentID]; dup {
			changed = true
			continue
		}
		seenComments[commentID] = struct{}{}
		filtered = append(filtered, item)
	}
	if changed {
		metadata.QueuedReviewFollowUps = filtered
	}
	return changed
}

func hasPendingReviewFollowUpMetadata(metadata domain.ColinMetadata) bool {
	return strings.TrimSpace(metadata.PendingReviewThreadID) != "" || strings.TrimSpace(metadata.PendingReviewCommentID) != ""
}

func queuedOrPendingReviewComment(metadata domain.ColinMetadata, commentID string) bool {
	commentID = strings.TrimSpace(commentID)
	if commentID == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(metadata.PendingReviewCommentID), commentID) {
		return true
	}
	for _, item := range metadata.QueuedReviewFollowUps {
		if strings.EqualFold(strings.TrimSpace(item.CommentID), commentID) {
			return true
		}
	}
	return false
}

func hasQueuedReviewFollowUps(issue domain.Issue) bool {
	return issue.ColinMetadata != nil && len(issue.ColinMetadata.QueuedReviewFollowUps) > 0
}

func compareReviewReactionIDs(left string, right string) int {
	leftID, leftErr := strconv.ParseInt(strings.TrimSpace(left), 10, 64)
	rightID, rightErr := strconv.ParseInt(strings.TrimSpace(right), 10, 64)
	switch {
	case leftErr == nil && rightErr == nil:
		switch {
		case leftID < rightID:
			return -1
		case leftID > rightID:
			return 1
		default:
			return 0
		}
	case leftErr == nil:
		return 1
	case rightErr == nil:
		return -1
	default:
		return strings.Compare(strings.TrimSpace(left), strings.TrimSpace(right))
	}
}

func reviewReactionIDGreater(candidate string, current string) bool {
	return compareReviewReactionIDs(candidate, current) > 0
}

func pendingReviewReactor(issue domain.Issue) string {
	if issue.ColinMetadata == nil {
		return ""
	}
	return strings.TrimSpace(issue.ColinMetadata.PendingReviewReactor)
}
