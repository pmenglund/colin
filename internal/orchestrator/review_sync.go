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
	if !needsReviewSync(issue) || o.runtime.Repo == nil || o.runtime.Workspace == nil {
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
		if !ok || !needsReviewSync(issue) {
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
	if issue.ReviewCycle == nil {
		return false
	}
	return hasIssueReviewSyncPullRequestSignal(issue)
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
	return len(issue.AttachedPullRequests) == 1
}

func hasReviewSyncPullRequest(pr domain.PullRequestRef) bool {
	return pr.Number > 0 || strings.TrimSpace(pr.URL) != ""
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
