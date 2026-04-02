package userworkflow

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func TestReviewSyncWaitingExplainsAutomaticPolling(t *testing.T) {
	t.Parallel()

	returnedAt := time.Date(2026, time.April, 1, 18, 0, 0, 0, time.UTC)
	got := ReviewSyncWaiting(domain.PullRequestRef{
		Number: 17,
		URL:    "https://example.test/pr/17",
	}, &returnedAt, 90*time.Second, false)

	for _, want := range []string{
		"Waiting for GitHub review feedback to sync before starting work.",
		"- PR: `#17`",
		"- What Colin is doing next: polling GitHub for unresolved review threads before starting the next coding round.",
		"- What you should do: nothing yet unless Colin later reports that the sync timed out.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ReviewSyncWaiting() = %q, want substring %q", got, want)
		}
	}
}

func TestReviewBlockedExplainsHumanFollowUp(t *testing.T) {
	t.Parallel()

	got := ReviewBlocked(&domain.PullRequestRef{
		Number: 17,
		URL:    "https://example.test/pr/17",
	}, 2, 1, "", "Updated the API and added tests.")

	for _, want := range []string{
		"Staying in `Todo` until GitHub review feedback is fully addressed.",
		"- Review threads handled: `2`",
		"- Review threads remaining: `1`",
		"- What Colin is doing next: retrying after the review follow-up state changes or new review context appears.",
		"- What you should do: keep the issue in active work until the remaining feedback is resolved.",
		"Codex summary:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ReviewBlocked() = %q, want substring %q", got, want)
		}
	}
}

func TestMergeWaitingForReviewExplainsAutomaticRetry(t *testing.T) {
	t.Parallel()

	got := MergeWaitingForReview(domain.PullRequestRef{
		Number: 17,
		URL:    "https://example.test/pr/17",
	}, true, false)

	for _, want := range []string{
		"Keeping issue in `Merge` while waiting for Codex PR review to start.",
		"- What Colin is doing next: retrying merge automation automatically after the Codex review state changes.",
		"- What you should do: leave the issue in `Merge` unless Colin later returns it to `Review`.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MergeWaitingForReview() = %q, want substring %q", got, want)
		}
	}
}

func TestMergeRecoveryFailureExplainsHumanRepairStep(t *testing.T) {
	t.Parallel()

	got := MergeRecoveryFailure(
		domain.PullRequestRef{Number: 17, URL: "https://example.test/pr/17"},
		"colin/workflow-improvements",
		"main",
		errors.New("merge commit cannot be cleanly created"),
		"Codex did not finish the repair.",
		"Review",
		"Conflicts remained in cmd/root.go.",
	)

	for _, want := range []string{
		"Colin hit a merge conflict, tried to repair it automatically, and then moved the issue back to `Review`.",
		"- Branch: `colin/workflow-improvements`",
		"- What Colin is doing next: stopping merge automation until the branch is updated.",
		"- What you should do: check out the PR branch, merge `main`, resolve any conflicts, push the updated branch, then move the issue back to `Merge`.",
		"Codex recovery output:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MergeRecoveryFailure() = %q, want substring %q", got, want)
		}
	}
}
