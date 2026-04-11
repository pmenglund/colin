package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/repoops/fakes"
	"github.com/pmenglund/colin/internal/workspace"
)

func TestSyncGitHubPullRequestCheckMovesActualFailureToActiveWork(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Repo.PublishStates = []string{"Review"}
	fakeGitHub.PullRequestByNumberReturns(checkTestPullRequest(), nil)
	fakeGitHub.PullRequestChecksReturns(repohost.PullRequestCheckRollup{
		PullRequest: *checkTestPullRequest(),
		HeadSHA:     "abc123",
		State:       repohost.PullRequestCheckStateFailed,
		Failed: []repohost.PullRequestCheck{{
			Name:        "go test",
			Status:      "completed",
			Conclusion:  "failure",
			State:       repohost.PullRequestCheckStateFailed,
			FailureKind: repohost.PullRequestCheckFailureKindActual,
			DetailsURL:  "https://github.com/pmenglund/colin/actions/runs/1",
			Summary:     "unit tests failed",
		}},
	}, nil)

	tracker, orch := checkTestOrchestrator(cfg, fakeGitHub)
	issue := checkTestReviewIssue()
	updated, queued := orch.syncGitHubPullRequestCheck(context.Background(), issue, time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC))

	if !queued {
		t.Fatal("syncGitHubPullRequestCheck() queued = false, want true")
	}
	if updated.State != "Todo" {
		t.Fatalf("updated.State = %q, want Todo", updated.State)
	}
	if got := tracker.updatedStates; len(got) != 1 || got[0] != "issue-1:Todo" {
		t.Fatalf("updated states = %#v, want move to Todo", got)
	}
	if tracker.metadata.PendingCheckFailure == nil {
		t.Fatalf("PendingCheckFailure = nil, want stored failure")
	}
	if tracker.metadata.PendingCheckFailure.Name != "go test" || tracker.metadata.PendingCheckFailure.FailureKind != "actual" {
		t.Fatalf("PendingCheckFailure = %#v, want actual go test failure", tracker.metadata.PendingCheckFailure)
	}
	if len(tracker.issueComments) != 1 || !strings.Contains(tracker.issueComments[0], "GitHub check `go test` failed") {
		t.Fatalf("issue comments = %#v, want check repair comment", tracker.issueComments)
	}
}

func TestSyncGitHubPullRequestCheckLeavesTimeoutFailureInReview(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Repo.PublishStates = []string{"Review"}
	fakeGitHub.PullRequestByNumberReturns(checkTestPullRequest(), nil)
	fakeGitHub.PullRequestChecksReturns(repohost.PullRequestCheckRollup{
		PullRequest: *checkTestPullRequest(),
		HeadSHA:     "abc123",
		State:       repohost.PullRequestCheckStateFailed,
		Failed: []repohost.PullRequestCheck{{
			Name:        "slow integration test",
			Status:      "completed",
			Conclusion:  "timed_out",
			State:       repohost.PullRequestCheckStateFailed,
			FailureKind: repohost.PullRequestCheckFailureKindTimeout,
		}},
	}, nil)

	tracker, orch := checkTestOrchestrator(cfg, fakeGitHub)
	issue := checkTestReviewIssue()
	issue.ColinMetadata.PendingCheckFailure = &domain.PendingPullRequestCheckFailure{
		Name:        "go test",
		FailureKind: string(repohost.PullRequestCheckFailureKindActual),
		Conclusion:  "failure",
		HeadSHA:     "oldsha",
		PRNumber:    11,
		PRURL:       "https://github.com/pmenglund/colin/pull/11",
	}
	updated, queued := orch.syncGitHubPullRequestCheck(context.Background(), issue, time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC))

	if queued {
		t.Fatal("syncGitHubPullRequestCheck() queued = true, want false for timeout")
	}
	if updated.State != "Review" {
		t.Fatalf("updated.State = %q, want Review", updated.State)
	}
	if len(tracker.updatedStates) != 0 {
		t.Fatalf("updated states = %#v, want no state move", tracker.updatedStates)
	}
	if tracker.metadata.PendingCheckFailure != nil {
		t.Fatalf("PendingCheckFailure = %#v, want nil", tracker.metadata.PendingCheckFailure)
	}
	if len(tracker.issueComments) != 1 || !strings.Contains(tracker.issueComments[0], "Timeout failures: `1`") {
		t.Fatalf("issue comments = %#v, want timeout status comment", tracker.issueComments)
	}
}

func checkTestOrchestrator(cfg domain.ServiceConfig, fakeGitHub *fakes.FakeRepoHostClient) (*trackerStub, *Orchestrator) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	return tracker, &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, fakeGitHub),
			Workspace: workspace.NewManager(cfg, logger),
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{"issue-1": "Review"},
	}
}

func checkTestReviewIssue() domain.Issue {
	return domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-208",
		Title:      "Watch PR checks",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber: 11,
			PullRequestURL:    "https://github.com/pmenglund/colin/pull/11",
		},
	}
}

func checkTestPullRequest() *repohost.PullRequest {
	return &repohost.PullRequest{
		Number:      11,
		URL:         "https://github.com/pmenglund/colin/pull/11",
		State:       "OPEN",
		HeadSHA:     "abc123",
		HeadRefName: "colin/COLIN-208",
		BaseRefName: "symphony",
	}
}
