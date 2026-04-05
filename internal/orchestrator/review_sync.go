package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/userworkflow"
)

const (
	reviewSyncFastPollInterval = 30 * time.Second
	reviewSyncSlowPollInterval = 5 * time.Minute
	reviewSyncTimeout          = 15 * time.Minute
)

func (o *Orchestrator) prepareReviewIssue(ctx context.Context, issue domain.Issue, now time.Time) (domain.Issue, bool) {
	if (!needsReviewSync(issue) && !hasPendingReviewFollowUp(issue)) || o.runtime.Repo == nil || o.runtime.Workspace == nil {
		delete(o.reviewSync, issue.ID)
		return issue, true
	}

	state := o.reviewSync[issue.ID]
	if state != nil && !state.nextPollAt.IsZero() && now.Before(state.nextPollAt) {
		return issue, false
	}

	workspace, err := o.runtime.Workspace.Ensure(ctx, issue)
	if err != nil {
		state = o.ensureReviewSyncState(issue, state, now)
		issue, state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, fmt.Sprintf(
			"%s\n- Blocker: failed to prepare workspace for GitHub review sync: %v",
			userworkflow.ReviewSyncWaiting(domain.PullRequestRef{}, reviewReturnedToTodoAt(issue), now.Sub(state.firstObserved), state.timedOut),
			err,
		))
		state.nextPollAt = nextReviewSyncPoll(now, state.firstObserved, state.timedOut)
		o.reviewSync[issue.ID] = state
		return issue, false
	}

	if hasPendingReviewFollowUp(issue) {
		reviewContext, err := o.runtime.Repo.ReviewContext(ctx, issue, workspace.Path)
		if err != nil {
			state = o.ensureReviewSyncState(issue, state, now)
			issue, state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, fmt.Sprintf(
				"Waiting for the targeted GitHub review thread before starting work.\n- Blocker: failed to read GitHub review threads: %v",
				err,
			))
			state.nextPollAt = nextReviewSyncPoll(now, state.firstObserved, state.timedOut)
			o.reviewSync[issue.ID] = state
			return issue, false
		}
		if reviewContext.PullRequest.Number > 0 || strings.TrimSpace(reviewContext.PullRequest.URL) != "" {
			issue.PullRequest = &reviewContext.PullRequest
		}
		targetThreadID := strings.TrimSpace(issue.ColinMetadata.PendingReviewThreadID)
		for _, thread := range reviewContext.Threads {
			if strings.EqualFold(strings.TrimSpace(thread.ID), targetThreadID) {
				issue.ReviewThreads = []domain.ReviewThread{thread}
				delete(o.reviewSync, issue.ID)
				return issue, true
			}
		}
		reviewState := firstConfiguredPublishState(o.runtime.Config.Repo.PublishStates)
		if strings.TrimSpace(reviewState) == "" {
			reviewState = "Review"
		}
		if err := o.runtime.Tracker.UpdateIssueState(ctx, issue.ID, reviewState); err != nil {
			state = o.ensureReviewSyncState(issue, state, now)
			issue, state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, fmt.Sprintf(
				"Waiting for the targeted GitHub review thread before starting work.\n- Blocker: failed to restore issue to `%s`: %v",
				reviewState,
				err,
			))
			state.nextPollAt = nextReviewSyncPoll(now, state.firstObserved, state.timedOut)
			o.reviewSync[issue.ID] = state
			return issue, false
		}
		previousState := issue.State
		issue.State = reviewState
		o.applyObservedDashboardIssueTransition(issue, previousState, issue.State)
		issue = o.clearPendingReviewFollowUp(ctx, issue)
		issue, _ = o.postIssueStatus(ctx, issue, issue.Identifier, nil, "The requested GitHub review thread is no longer unresolved, so Colin returned the issue to `Review` without starting a coding round.")
		delete(o.reviewSync, issue.ID)
		return issue, false
	}

	reviewContext, err := o.runtime.Repo.ReviewContext(ctx, issue, workspace.Path)
	if err != nil {
		state = o.ensureReviewSyncState(issue, state, now)
		issue, state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, fmt.Sprintf(
			"%s\n- Blocker: failed to read GitHub review threads: %v",
			userworkflow.ReviewSyncWaiting(domain.PullRequestRef{}, reviewReturnedToTodoAt(issue), now.Sub(state.firstObserved), state.timedOut),
			err,
		))
		state.nextPollAt = nextReviewSyncPoll(now, state.firstObserved, state.timedOut)
		o.reviewSync[issue.ID] = state
		return issue, false
	}

	if reviewContext.PullRequest.Number > 0 || strings.TrimSpace(reviewContext.PullRequest.URL) != "" {
		issue.PullRequest = &reviewContext.PullRequest
	}
	if !hasReviewSyncPullRequest(reviewContext.PullRequest) {
		delete(o.reviewSync, issue.ID)
		return issue, true
	}
	if len(reviewContext.Threads) > 0 {
		issue.ReviewThreads = reviewContext.Threads
		if state != nil && state.comment != nil && state.comment.RootCommentID != "" {
			issue, state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, userworkflow.ReviewSyncReady(reviewContext.PullRequest, len(reviewContext.Threads)))
		}
	}
	delete(o.reviewSync, issue.ID)
	return issue, true
}

func (o *Orchestrator) cleanupReviewSync(active []domain.Issue) {
	activeByID := make(map[string]domain.Issue, len(active))
	for _, issue := range active {
		activeByID[issue.ID] = issue
	}
	for issueID := range o.reviewSync {
		issue, ok := activeByID[issueID]
		if !ok || !strings.EqualFold(strings.TrimSpace(issue.State), "todo") {
			delete(o.reviewSync, issueID)
		}
	}
}

func (o *Orchestrator) ensureReviewSyncState(issue domain.Issue, state *reviewSyncState, now time.Time) *reviewSyncState {
	if state != nil {
		return state
	}
	return &reviewSyncState{
		firstObserved: now,
		comment:       &commentThreadState{},
	}
}

func needsReviewSync(issue domain.Issue) bool {
	if !strings.EqualFold(strings.TrimSpace(issue.State), "todo") {
		return false
	}
	if hasPendingReviewFollowUp(issue) {
		return true
	}
	if issue.ReviewCycle == nil {
		return false
	}
	return hasIssueReviewSyncPullRequestSignal(issue)
}

func hasPendingReviewFollowUp(issue domain.Issue) bool {
	if issue.ColinMetadata == nil {
		return false
	}
	return strings.TrimSpace(issue.ColinMetadata.PendingReviewThreadID) != "" || strings.TrimSpace(issue.ColinMetadata.PendingReviewCommentID) != ""
}

func firstConfiguredPublishState(states []string) string {
	for _, state := range states {
		if strings.TrimSpace(state) != "" {
			return strings.TrimSpace(state)
		}
	}
	return ""
}

func hasIssueReviewSyncPullRequestSignal(issue domain.Issue) bool {
	if issue.ColinMetadata != nil {
		if issue.ColinMetadata.PullRequestNumber > 0 {
			return true
		}
		if strings.TrimSpace(issue.ColinMetadata.PullRequestURL) != "" {
			return true
		}
	}
	if len(issue.AttachedPullRequests) == 0 {
		return false
	}

	seen := make(map[string]struct{}, len(issue.AttachedPullRequests))
	for _, pr := range issue.AttachedPullRequests {
		if !hasReviewSyncPullRequest(pr) {
			continue
		}

		key := reviewSyncPullRequestRepositoryKey(pr)
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
	}

	return len(seen) > 0
}

func hasReviewSyncPullRequest(pr domain.PullRequestRef) bool {
	return pr.Number > 0 || strings.TrimSpace(pr.URL) != ""
}

func reviewSyncPullRequestRepositoryKey(pr domain.PullRequestRef) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(pr.Backend)),
		strings.ToLower(strings.TrimSpace(pr.RepositoryOwner)),
		strings.ToLower(strings.TrimSpace(pr.RepositoryName)),
	}
	return strings.Join(parts, "\x00")
}

func nextReviewSyncPoll(now, firstObserved time.Time, timedOut bool) time.Time {
	if timedOut || now.Sub(firstObserved) >= reviewSyncTimeout {
		return now.Add(reviewSyncSlowPollInterval)
	}
	return now.Add(reviewSyncFastPollInterval)
}

func reviewReturnedToTodoAt(issue domain.Issue) *time.Time {
	if issue.ReviewCycle == nil {
		return nil
	}
	return &issue.ReviewCycle.ReturnedToTodoAt
}
