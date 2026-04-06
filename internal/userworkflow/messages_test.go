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

func TestReviewSyncReadyExplainsAutomaticResume(t *testing.T) {
	t.Parallel()

	got := ReviewSyncReady(domain.PullRequestRef{
		Number: 17,
		URL:    "https://example.test/pr/17",
	}, 2)

	for _, want := range []string{
		"GitHub review feedback synced, so Colin is starting work now.",
		"- PR: `#17`",
		"- Unresolved review threads: `2`",
		"- What Colin is doing next: starting the next coding round with the synced GitHub review feedback.",
		"- What you should do: nothing yet unless Colin later reports that more review follow-up is needed.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ReviewSyncReady() = %q, want substring %q", got, want)
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

func TestMergeRetryingExplainsAutomaticMergeRetry(t *testing.T) {
	t.Parallel()

	got := MergeRetrying(
		domain.PullRequestRef{Number: 17, URL: "https://example.test/pr/17"},
		"colin/workflow-improvements",
		"main",
		"the base branch `main` moved again after Colin prepared the branch for merge",
		"COLIN_OUTCOME: READY_FOR_MERGE_RETRY",
	)

	for _, want := range []string{
		"Keeping issue in `Merge` while Colin retries merge automation automatically.",
		"- Branch: `colin/workflow-improvements`",
		"- Retry reason: the base branch `main` moved again after Colin prepared the branch for merge",
		"- What Colin is doing next: retrying merge automation automatically after a short backoff.",
		"Codex repair summary:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MergeRetrying() = %q, want substring %q", got, want)
		}
	}
}

func TestMergeRecoveryValidationFailureExplainsFalsePositiveRepair(t *testing.T) {
	t.Parallel()

	got := MergeRecoveryValidationFailure(
		domain.PullRequestRef{Number: 17, URL: "https://example.test/pr/17"},
		"colin/workflow-improvements",
		"main",
		"Review",
		"Codex reported the merge recovery as ready, but the branch head did not change (abc -> abc).",
		"COLIN_OUTCOME: READY_FOR_MERGE_RETRY",
		[]string{
			"- Branch head before recovery: `abc`",
			"- Branch head after recovery: `abc`",
		},
		errors.New("merge commit cannot be cleanly created"),
	)

	for _, want := range []string{
		"Colin moved the issue back to `Review` because Codex reported the merge recovery as ready, but Colin could not verify that the branch was actually updated for merge retry.",
		"- Validation blocker: Codex reported the merge recovery as ready, but the branch head did not change (abc -> abc).",
		"- Branch head before recovery: `abc`",
		"Codex recovery summary:",
		"- What Colin is doing next: stopping merge automation until the branch is updated for real.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MergeRecoveryValidationFailure() = %q, want substring %q", got, want)
		}
	}
}

func TestCodingNoReviewableChangesRefineExplainsInvestigationPath(t *testing.T) {
	t.Parallel()

	got := CodingNoReviewableChangesRefine(
		"In Progress",
		2,
		true,
		"## Why\n\nCOLIN-157 is already implemented in the repository.\n\n## Evidence\n\n```text\n$ git diff --stat HEAD origin/main\n<no output>\n```",
		&domain.ColinMetadata{
			URL:            "https://example.test/metadata/COLIN-157",
			SlackPermalink: "https://example.test/slack/COLIN-157",
		},
	)

	for _, want := range []string{
		"Colin moved this issue to `Refine` because Codex repeatedly reported it as ready for review, but Colin still found no reviewable repository changes.",
		"- Run type: `coding`",
		"- State: `In Progress`",
		"- Turns used: `2`",
		"- Last meaningful Codex outcome: COLIN-157 is already implemented in the repository.",
		"- What Colin is doing next: stopping early and moving the issue to `Refine` instead of spending the remaining turn budget repeating the same no-diff handoff.",
		"- Colin metadata: https://example.test/metadata/COLIN-157",
		"- Slack thread: https://example.test/slack/COLIN-157",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("CodingNoReviewableChangesRefine() = %q, want substring %q", got, want)
		}
	}
}

func TestCodingNoReviewableChangesRefineOmitsLinksWhenMissing(t *testing.T) {
	t.Parallel()

	got := CodingNoReviewableChangesRefine("In Progress", 1, false, "## Why\n\nAlready shipped.", nil)

	if strings.Contains(got, "Colin metadata:") {
		t.Fatalf("CodingNoReviewableChangesRefine() = %q, want no metadata link", got)
	}
	if strings.Contains(got, "Slack thread:") {
		t.Fatalf("CodingNoReviewableChangesRefine() = %q, want no Slack link", got)
	}
	if !strings.Contains(got, "- Last meaningful Codex outcome: Already shipped.") {
		t.Fatalf("CodingNoReviewableChangesRefine() = %q, want condensed Codex outcome", got)
	}
}
