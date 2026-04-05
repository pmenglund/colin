package userworkflow

import (
	"fmt"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func ReviewSyncWaiting(pr domain.PullRequestRef, returnedToTodoAt *time.Time, waited time.Duration, timedOut bool) string {
	lines := []string{"Waiting for GitHub review feedback to sync before starting work."}
	lines = appendPullRequest(lines, &pr)
	if returnedToTodoAt != nil {
		lines = append(lines, fmt.Sprintf("- Returned to `Todo`: `%s`", returnedToTodoAt.Format(time.RFC3339)))
	}
	lines = append(lines, fmt.Sprintf("- Waited: `%s`", waited.Round(time.Second)))
	if timedOut {
		lines = append(lines, "- What Colin is doing next: polling GitHub in the background for delayed unresolved review threads.")
		lines = append(lines, "- What you should do: nothing yet unless you want to inspect the PR manually while Colin keeps polling.")
	} else {
		lines = append(lines, "- What Colin is doing next: polling GitHub for unresolved review threads before starting the next coding round.")
		lines = append(lines, "- What you should do: nothing yet unless Colin later reports that the sync timed out.")
	}
	return strings.Join(lines, "\n")
}

func ReviewSyncTimedOut(pr domain.PullRequestRef, timeout time.Duration, pollInterval time.Duration) string {
	lines := []string{"GitHub review feedback has not appeared yet, so Colin is keeping the issue in `Todo`."}
	lines = appendPullRequest(lines, &pr)
	lines = append(lines, fmt.Sprintf("- Wait timeout reached: `%s`", timeout))
	lines = append(lines, fmt.Sprintf("- Background poll interval: `%s`", pollInterval))
	lines = append(lines, "- What Colin is doing next: continuing to poll GitHub in the background until unresolved review threads appear.")
	lines = append(lines, "- What you should do: nothing yet unless you want to inspect the PR manually while Colin keeps polling.")
	return strings.Join(lines, "\n")
}

func ReviewSyncReady(pr domain.PullRequestRef, unresolvedThreads int) string {
	lines := []string{"GitHub review feedback synced, so Colin is starting work now."}
	lines = appendPullRequest(lines, &pr)
	lines = append(lines, fmt.Sprintf("- Unresolved review threads: `%d`", unresolvedThreads))
	lines = append(lines, "- What Colin is doing next: starting the next coding round with the synced GitHub review feedback.")
	lines = append(lines, "- What you should do: nothing yet unless Colin later reports that more review follow-up is needed.")
	return strings.Join(lines, "\n")
}

func ReviewBlocked(pr *domain.PullRequestRef, handled int, remaining int, reason string, codexSummary string) string {
	lines := []string{"Staying in `Todo` until GitHub review feedback is fully addressed."}
	lines = appendPullRequest(lines, pr)
	lines = append(lines, fmt.Sprintf("- Review threads handled: `%d`", handled))
	lines = append(lines, fmt.Sprintf("- Review threads remaining: `%d`", remaining))
	if reason = strings.TrimSpace(reason); reason != "" {
		lines = append(lines, fmt.Sprintf("- Blocker: %s", reason))
	}
	if remaining > 0 {
		lines = append(lines, "- What Colin is doing next: retrying after the review follow-up state changes or new review context appears.")
		lines = append(lines, "- What you should do: keep the issue in active work until the remaining feedback is resolved.")
	} else if reason != "" {
		lines = append(lines, "- What Colin is doing next: waiting for the review-context blocker above to clear before the next review handoff.")
		lines = append(lines, "- What you should do: check the blocker above and move the issue forward once Colin can see the review context it needs.")
	} else {
		lines = append(lines, "- What Colin is doing next: keeping the issue in active work until the review follow-up is complete.")
		lines = append(lines, "- What you should do: continue the review follow-up and move the issue forward once the PR is ready again.")
	}
	lines = appendCodexSummary(lines, codexSummary)
	return strings.Join(lines, "\n")
}

func ReviewReady(pr *domain.PullRequestRef, handled int, remaining int, codexSummary string) string {
	lines := []string{"Ready for review."}
	lines = appendPullRequest(lines, pr)
	lines = append(lines, fmt.Sprintf("- Review threads handled: `%d`", handled))
	lines = append(lines, fmt.Sprintf("- Review threads remaining: `%d`", remaining))
	lines = append(lines, "- What Colin is doing next: leaving the issue ready for PR review.")
	lines = append(lines, "- What you should do: review the updated PR and either move it back to active work or forward to `Merge`.")
	lines = appendCodexSummary(lines, codexSummary)
	return strings.Join(lines, "\n")
}

func NoReviewableChanges(targetState string) string {
	lines := []string{
		fmt.Sprintf("Colin did not find reviewable repository changes, so it moved the issue back to `%s` instead of opening a PR.", targetState),
		"- What happened: the workspace has no uncommitted changes and the branch is not ahead of the configured base branch.",
		fmt.Sprintf("- What Colin is doing next: leaving the issue in `%s` for more implementation work.", targetState),
		"- What you should do: keep working until there is reviewable code or explicitly hand the issue to `Refine`.",
	}
	return strings.Join(lines, "\n")
}

func MergeWaitingForReview(pr domain.PullRequestRef, waitingForPickup bool, pendingApproval bool) string {
	lines := []string{"Keeping issue in `Merge` while waiting for Codex PR review feedback."}
	if waitingForPickup {
		lines = []string{"Keeping issue in `Merge` while waiting for Codex PR review to start."}
	}
	lines = appendPullRequest(lines, &pr)
	if pendingApproval {
		lines = append(lines, "- Codex review status: waiting for a `thumbs up` reaction after the latest `eyes` reaction.")
	}
	if waitingForPickup {
		lines = append(lines, "- Codex review status: waiting for Codex to acknowledge the PR with an `eyes` reaction before merge automation continues.")
	}
	lines = append(lines, "- What Colin is doing next: retrying merge automation automatically after the Codex review state changes.")
	lines = append(lines, "- What you should do: leave the issue in `Merge` unless Colin later returns it to `Review`.")
	return strings.Join(lines, "\n")
}

func MergeReturnedToReview(pr domain.PullRequestRef, unresolvedThreads int) string {
	lines := []string{"Returning issue to `Review` because Codex PR feedback still needs to be resolved."}
	lines = appendPullRequest(lines, &pr)
	if unresolvedThreads > 0 {
		lines = append(lines, fmt.Sprintf("- Unresolved Codex review threads: `%d`", unresolvedThreads))
	}
	lines = append(lines, "- What Colin is doing next: stopping merge automation until the PR feedback is resolved.")
	lines = append(lines, "- What you should do: resolve the remaining Codex PR feedback, then move the issue back to `Merge`.")
	return strings.Join(lines, "\n")
}

func MergeFailure(pr domain.PullRequestRef, branch string, baseRef string, reason string, reviewState string) string {
	lines := []string{
		fmt.Sprintf("Colin could not merge this PR automatically, so it moved the issue back to `%s`.", reviewState),
	}
	lines = appendPullRequest(lines, &pr)
	if branch = strings.TrimSpace(branch); branch != "" {
		lines = append(lines, fmt.Sprintf("- Branch: `%s`", branch))
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		lines = append(lines, fmt.Sprintf("- Reason: %s", reason))
	}
	lines = append(lines, "- What Colin is doing next: stopping merge automation until the branch is updated.")
	lines = append(lines, mergeHumanAction(pr.Number, baseRef, "reviewStateUnused"))
	if pr.Number > 0 && strings.TrimSpace(baseRef) != "" {
		lines = append(lines, fmt.Sprintf("- Suggested command: `gh pr checkout %d && git fetch origin %s && git merge origin/%s`", pr.Number, baseRef, baseRef))
	}
	return strings.Join(lines, "\n")
}

func MergeRetrying(pr domain.PullRequestRef, branch string, baseRef string, reason string, recoverySummary string) string {
	lines := []string{"Keeping issue in `Merge` while Colin retries merge automation automatically."}
	lines = appendPullRequest(lines, &pr)
	if branch = strings.TrimSpace(branch); branch != "" {
		lines = append(lines, fmt.Sprintf("- Branch: `%s`", branch))
	}
	if baseRef = strings.TrimSpace(baseRef); baseRef != "" {
		lines = append(lines, fmt.Sprintf("- Base ref: `%s`", baseRef))
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		lines = append(lines, fmt.Sprintf("- Retry reason: %s", reason))
	}
	if recoverySummary = strings.TrimSpace(recoverySummary); recoverySummary != "" {
		lines = append(lines, "", "Codex repair summary:", "", recoverySummary)
	}
	lines = append(lines, "- What Colin is doing next: retrying merge automation automatically after a short backoff.")
	lines = append(lines, "- What you should do: leave the issue in `Merge` unless Colin later returns it to `Review`.")
	return strings.Join(lines, "\n")
}

func MergeRecoveryFailure(pr domain.PullRequestRef, branch string, baseRef string, mergeErr error, reason string, reviewState string, recoveryOutput string) string {
	lines := []string{
		fmt.Sprintf("Colin hit a merge conflict, tried to repair it automatically, and then moved the issue back to `%s`.", reviewState),
	}
	lines = appendPullRequest(lines, &pr)
	if branch = strings.TrimSpace(branch); branch != "" {
		lines = append(lines, fmt.Sprintf("- Branch: `%s`", branch))
	}
	if mergeErr != nil {
		lines = append(lines, fmt.Sprintf("- Original merge error: %s", strings.TrimSpace(mergeErr.Error())))
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		lines = append(lines, fmt.Sprintf("- Recovery blocker: %s", reason))
	}
	lines = append(lines, "- What Colin is doing next: stopping merge automation until the branch is updated.")
	lines = append(lines, mergeHumanAction(pr.Number, baseRef, reviewState))
	if pr.Number > 0 && strings.TrimSpace(baseRef) != "" {
		lines = append(lines, fmt.Sprintf("- Suggested command: `gh pr checkout %d && git fetch origin %s && git merge origin/%s`", pr.Number, baseRef, baseRef))
	}
	if recoveryOutput = strings.TrimSpace(recoveryOutput); recoveryOutput != "" {
		lines = append(lines, "", "Codex recovery output:", "", recoveryOutput)
	}
	return strings.Join(lines, "\n")
}

func MergeRecoveryValidationFailure(pr domain.PullRequestRef, branch string, baseRef string, reviewState string, reason string, recoverySummary string, evidence []string, mergeErr error) string {
	lines := []string{
		fmt.Sprintf("Colin moved the issue back to `%s` because Codex reported the merge recovery as ready, but Colin could not verify that the branch was actually updated for merge retry.", reviewState),
	}
	lines = appendPullRequest(lines, &pr)
	if branch = strings.TrimSpace(branch); branch != "" {
		lines = append(lines, fmt.Sprintf("- Branch: `%s`", branch))
	}
	if mergeErr != nil {
		lines = append(lines, fmt.Sprintf("- Original merge error: %s", strings.TrimSpace(mergeErr.Error())))
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		lines = append(lines, fmt.Sprintf("- Validation blocker: %s", reason))
	}
	lines = append(lines, evidence...)
	if recoverySummary = strings.TrimSpace(recoverySummary); recoverySummary != "" {
		lines = append(lines, "", "Codex recovery summary:", "", recoverySummary)
	}
	lines = append(lines, "- What Colin is doing next: stopping merge automation until the branch is updated for real.")
	lines = append(lines, mergeHumanAction(pr.Number, baseRef, reviewState))
	if pr.Number > 0 && strings.TrimSpace(baseRef) != "" {
		lines = append(lines, fmt.Sprintf("- Suggested command: `gh pr checkout %d && git fetch origin %s && git merge origin/%s`", pr.Number, baseRef, baseRef))
	}
	return strings.Join(lines, "\n")
}

func MergeRecoveryReviewBlocked(pr domain.PullRequestRef, recoverySummary string, waitingForPickup bool, pendingApproval bool, unresolvedThreads int) string {
	lines := []string{"Colin repaired the merge conflict, but the updated PR still needs Codex review before it can be merged."}
	lines = appendPullRequest(lines, &pr)
	if recoverySummary = strings.TrimSpace(recoverySummary); recoverySummary != "" {
		lines = append(lines, "", "Codex repair summary:", "", recoverySummary)
	}
	if waitingForPickup {
		lines = append(lines, "- Codex review status: waiting for Codex to acknowledge the PR with an `eyes` reaction before merge automation continues.")
		lines = append(lines, "- What Colin is doing next: retrying merge automation automatically after the Codex review state changes.")
		lines = append(lines, "- What you should do: leave the issue in `Merge` unless Colin later returns it to `Review`.")
		return strings.Join(lines, "\n")
	}
	if pendingApproval {
		lines = append(lines, "- Codex review status: waiting for a `thumbs up` reaction after the latest `eyes` reaction.")
		lines = append(lines, "- What Colin is doing next: retrying merge automation automatically after the Codex review state changes.")
		lines = append(lines, "- What you should do: leave the issue in `Merge` unless Colin later returns it to `Review`.")
		return strings.Join(lines, "\n")
	}
	if unresolvedThreads > 0 {
		lines = append(lines, fmt.Sprintf("- Unresolved Codex review threads: `%d`", unresolvedThreads))
	}
	lines = append(lines, "- What Colin is doing next: stopping merge automation until the remaining Codex PR feedback is resolved.")
	lines = append(lines, "- What you should do: resolve the remaining Codex PR feedback, then move the issue back to `Merge`.")
	return strings.Join(lines, "\n")
}

func appendPullRequest(lines []string, pr *domain.PullRequestRef) []string {
	if pr == nil {
		return lines
	}
	if pr.Number > 0 {
		lines = append(lines, fmt.Sprintf("- PR: `#%d`", pr.Number))
	}
	if url := strings.TrimSpace(pr.URL); url != "" {
		lines = append(lines, fmt.Sprintf("- PR URL: %s", url))
	}
	return lines
}

func appendCodexSummary(lines []string, summary string) []string {
	if summary = strings.TrimSpace(summary); summary != "" {
		lines = append(lines, "", "Codex summary:", "", summary)
	}
	return lines
}

func mergeHumanAction(prNumber int, baseRef string, _ string) string {
	if prNumber > 0 && strings.TrimSpace(baseRef) != "" {
		return fmt.Sprintf("- What you should do: check out the PR branch, merge `%s`, resolve any conflicts, push the updated branch, then move the issue back to `Merge`.", baseRef)
	}
	return "- What you should do: resolve the merge issue on the PR branch, push the updated branch, then move the issue back to `Merge`."
}
