package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
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
		state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, fmt.Sprintf(
			"Waiting for GitHub review feedback to sync before starting work.\n\n- Blocker: failed to prepare workspace for GitHub review sync: %v",
			err,
		))
		state.nextPollAt = nextReviewSyncPoll(now, state.firstObserved, state.timedOut)
		o.reviewSync[issue.ID] = state
		return issue, false
	}

	reviewContext, err := o.runtime.Repo.ReviewContext(ctx, issue, workspace.Path)
	if err != nil {
		state = o.ensureReviewSyncState(issue, state, now)
		state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, fmt.Sprintf(
			"Waiting for GitHub review feedback to sync before starting work.\n\n- Blocker: failed to read GitHub review threads: %v",
			err,
		))
		state.nextPollAt = nextReviewSyncPoll(now, state.firstObserved, state.timedOut)
		o.reviewSync[issue.ID] = state
		return issue, false
	}

	if reviewContext.PullRequest.Number > 0 || strings.TrimSpace(reviewContext.PullRequest.URL) != "" {
		issue.PullRequest = &reviewContext.PullRequest
	}
	if len(reviewContext.Threads) > 0 {
		issue.ReviewThreads = reviewContext.Threads
		if state != nil && state.comment != nil && state.comment.RootCommentID != "" {
			state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, fmt.Sprintf(
				"GitHub review feedback synced.\n\n- Pull request: `#%d`\n- Unresolved review threads: `%d`\n- Colin is starting work now.",
				reviewContext.PullRequest.Number,
				len(reviewContext.Threads),
			))
		}
		delete(o.reviewSync, issue.ID)
		return issue, true
	}

	state = o.ensureReviewSyncState(issue, state, now)
	body := buildReviewSyncWaitingBody(issue, reviewContext.PullRequest, now.Sub(state.firstObserved), state.timedOut)
	state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, body)
	if !state.timedOut && now.Sub(state.firstObserved) >= reviewSyncTimeout {
		state.timedOut = true
		state.comment = o.postIssueStatus(ctx, issue, issue.Identifier, state.comment, buildReviewSyncTimedOutBody(issue, reviewContext.PullRequest))
	}
	state.nextPollAt = nextReviewSyncPoll(now, state.firstObserved, state.timedOut)
	o.reviewSync[issue.ID] = state
	return issue, false
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
	return issue.BranchName != nil && strings.TrimSpace(*issue.BranchName) != ""
}

func nextReviewSyncPoll(now, firstObserved time.Time, timedOut bool) time.Time {
	if timedOut || now.Sub(firstObserved) >= reviewSyncTimeout {
		return now.Add(reviewSyncSlowPollInterval)
	}
	return now.Add(reviewSyncFastPollInterval)
}

func buildReviewSyncWaitingBody(issue domain.Issue, pr domain.PullRequestRef, elapsed time.Duration, timedOut bool) string {
	lines := []string{"Waiting for GitHub review feedback to sync before starting work."}
	if pr.Number > 0 {
		lines = append(lines, fmt.Sprintf("- PR: `#%d`", pr.Number))
	}
	if strings.TrimSpace(pr.URL) != "" {
		lines = append(lines, fmt.Sprintf("- PR URL: %s", pr.URL))
	}
	if issue.ReviewCycle != nil {
		lines = append(lines, fmt.Sprintf("- Returned to `Todo`: `%s`", issue.ReviewCycle.ReturnedToTodoAt.Format(time.RFC3339)))
	}
	lines = append(lines, fmt.Sprintf("- Waited: `%s`", elapsed.Round(time.Second)))
	if timedOut {
		lines = append(lines, "- Polling in the background until unresolved GitHub review threads appear.")
	}
	return strings.Join(lines, "\n")
}

func buildReviewSyncTimedOutBody(issue domain.Issue, pr domain.PullRequestRef) string {
	lines := []string{"GitHub review feedback has not appeared yet, so Colin is keeping the issue in `Todo`."}
	if pr.Number > 0 {
		lines = append(lines, fmt.Sprintf("- PR: `#%d`", pr.Number))
	}
	if strings.TrimSpace(pr.URL) != "" {
		lines = append(lines, fmt.Sprintf("- PR URL: %s", pr.URL))
	}
	lines = append(lines, fmt.Sprintf("- Wait timeout reached: `%s`", reviewSyncTimeout))
	lines = append(lines, fmt.Sprintf("- Background poll interval: `%s`", reviewSyncSlowPollInterval))
	return strings.Join(lines, "\n")
}
