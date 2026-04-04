package repoops_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost/builtin"
	repoops "github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/repoops/fakes"
)

func init() {
	builtin.Register()
}

func TestPublishCreatesCommitPushesBranchAndOpensPR(t *testing.T) {
	workspacePath, remotePath := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturnsOnCall(0, nil, nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(1, nil, nil)
	fakeGitHub.CreatePullRequestReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(2, testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(3, testPullRequest(1, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	issueURL := "https://linear.example/COLIN-93"
	result, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Add publish automation",
		URL:        &issueURL,
	}, workspacePath)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if result.PRNumber != 1 {
		t.Fatalf("result.PRNumber = %d, want 1", result.PRNumber)
	}
	if result.Branch != "colin-93" {
		t.Fatalf("result.Branch = %q, want %q", result.Branch, "colin-93")
	}

	remoteBranches := runCmd(t, "", "git", "--git-dir", remotePath, "branch", "--list")
	if !strings.Contains(remoteBranches, "colin-93") {
		t.Fatalf("remote branches = %q, want issue branch", remoteBranches)
	}
	if fakeGitHub.CreatePullRequestCallCount() != 1 {
		t.Fatalf("CreatePullRequestCallCount() = %d, want 1", fakeGitHub.CreatePullRequestCallCount())
	}
}

func TestValidateRepoAccessSkipsWhenTokenMissing(t *testing.T) {
	fakeGitHub := &fakes.FakeGitHubClient{}
	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)

	if err := manager.ValidateRepoAccess(context.Background()); err != nil {
		t.Fatalf("ValidateRepoAccess() error = %v", err)
	}
	if fakeGitHub.ValidateAuthCallCount() != 0 {
		t.Fatalf("ValidateAuthCallCount() = %d, want 0", fakeGitHub.ValidateAuthCallCount())
	}
}

func TestValidateRepoAccessChecksConfiguredToken(t *testing.T) {
	cfg := testConfig()
	cfg.Repo.APIToken = "test-token"

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.ValidateAuthReturns(errors.New("unauthorized"))
	manager := repoops.NewManagerWithRepoHostClient(cfg, testLogger(), fakeGitHub)

	err := manager.ValidateRepoAccess(context.Background())
	if err == nil {
		t.Fatal("ValidateRepoAccess() error = nil, want unauthorized")
	}
	if fakeGitHub.ValidateAuthCallCount() != 1 {
		t.Fatalf("ValidateAuthCallCount() = %d, want 1", fakeGitHub.ValidateAuthCallCount())
	}
}

func TestPublishUsesConfiguredPRTemplate(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	cfg := testConfig()
	cfg.Workspace.BaseRef = "symphony"
	cfg.Repo.PRTemplate = "PRBODY issue={{.issue.identifier}} branch={{.branch}} base={{.base_ref}}"

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturnsOnCall(0, nil, nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(1, nil, nil)
	fakeGitHub.CreatePullRequestReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(2, testPullRequest(1, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(cfg, testLogger(), fakeGitHub)
	if _, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Use template",
	}, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	_, owner, repo, input := fakeGitHub.CreatePullRequestArgsForCall(0)
	if owner != "local" || repo != "remote" {
		t.Fatalf("CreatePullRequestArgs owner/repo = %q/%q, want local/remote", owner, repo)
	}
	if !strings.Contains(input.Body, "PRBODY issue=COLIN-93 branch=colin-93 base=symphony") {
		t.Fatalf("CreatePullRequest body = %q, want rendered template", input.Body)
	}
}

func TestPublishUsesTargetBaseRefWhenConfigured(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	cfg := testConfig()
	cfg.Workspace.BaseRef = "main"
	cfg.Targets = []domain.TargetConfig{
		{Key: "project-1-remote", ProjectSlug: "project-1", RepoURL: "git@github.com:acme/remote.git", BaseRef: "release"},
		{Key: "project-2-remote", ProjectSlug: "project-2", RepoURL: "git@github.com:acme/remote.git", BaseRef: "symphony"},
	}
	cfg.Repo.PRTemplate = "base={{.base_ref}}"

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturnsOnCall(0, nil, nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(1, nil, nil)
	fakeGitHub.CreatePullRequestReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(2, testPullRequest(1, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(cfg, testLogger(), fakeGitHub)
	if _, err := manager.Publish(context.Background(), domain.Issue{
		Identifier:  "COLIN-93",
		Title:       "Use target base ref",
		ProjectSlug: "project-2",
	}, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	_, _, _, input := fakeGitHub.CreatePullRequestArgsForCall(0)
	if input.Base != "symphony" {
		t.Fatalf("CreatePullRequest base = %q, want %q", input.Base, "symphony")
	}
	if input.Body != "base=symphony" {
		t.Fatalf("CreatePullRequest body = %q, want %q", input.Body, "base=symphony")
	}
}

func TestMergeMergesExistingPR(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturnsOnCall(0, nil, nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(1, nil, nil)
	fakeGitHub.CreatePullRequestReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(2, testPullRequest(1, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	issue := domain.Issue{Identifier: "COLIN-93", Title: "Add merge automation"}
	result, err := manager.Merge(context.Background(), issue, workspacePath)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if result.Action != "merged" {
		t.Fatalf("result.Action = %q, want %q", result.Action, "merged")
	}
	if fakeGitHub.MergePullRequestCallCount() != 1 {
		t.Fatalf("MergePullRequestCallCount() = %d, want 1", fakeGitHub.MergePullRequestCallCount())
	}
	_, owner, repo, number, method := fakeGitHub.MergePullRequestArgsForCall(0)
	if owner != "local" || repo != "remote" || number != 1 || method != "merge" {
		t.Fatalf("MergePullRequest args = %q/%q #%d %q, want local/remote #1 merge", owner, repo, number, method)
	}
}

func TestMergeReturnsPublishContextWhenGitHubMergeFails(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturnsOnCall(0, nil, nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(1, nil, nil)
	fakeGitHub.CreatePullRequestReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(2, testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.MergePullRequestReturns(errors.New("merge failed"))

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	issue := domain.Issue{Identifier: "COLIN-93", Title: "Add merge automation"}
	result, err := manager.Merge(context.Background(), issue, workspacePath)
	if err == nil {
		t.Fatal("Merge() error = nil, want error")
	}
	if result.PRNumber != 1 {
		t.Fatalf("result.PRNumber = %d, want 1", result.PRNumber)
	}
	if result.Branch != "colin-93" {
		t.Fatalf("result.Branch = %q, want %q", result.Branch, "colin-93")
	}
}

func TestMergePullRequestMergesPublishedPR(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturnsOnCall(0, nil, nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(1, nil, nil)
	fakeGitHub.CreatePullRequestReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(2, testPullRequest(1, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	issue := domain.Issue{Identifier: "COLIN-93", Title: "Add merge automation"}
	result, err := manager.Publish(context.Background(), issue, workspacePath)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	merged, err := manager.MergePullRequest(context.Background(), workspacePath, result)
	if err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}
	if merged.Action != "merged" {
		t.Fatalf("merged.Action = %q, want %q", merged.Action, "merged")
	}
	if fakeGitHub.MergePullRequestCallCount() != 1 {
		t.Fatalf("MergePullRequestCallCount() = %d, want 1", fakeGitHub.MergePullRequestCallCount())
	}
}

func TestMergePullRequestRetriesAfterRefreshWhenPullRequestIsAlreadyMergeable(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.MergePullRequestReturnsOnCall(0, errors.New("PUT https://api.github.com/repos/acme/widgets/pulls/11/merge: 405 Pull Request is not mergeable []"))
	fakeGitHub.PullRequestByNumberReturns(testPullRequestWithMergeable(11, "OPEN", "colin-93", true), nil)
	fakeGitHub.MergePullRequestReturnsOnCall(1, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	merged, err := manager.MergePullRequest(context.Background(), workspacePath, repoops.Result{
		PRNumber: 11,
		PRURL:    "https://github.com/pmenglund/colin/pull/11",
		PRState:  "OPEN",
	})
	if err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}
	if merged.Action != "merged" {
		t.Fatalf("merged.Action = %q, want %q", merged.Action, "merged")
	}
	if fakeGitHub.PullRequestByNumberCallCount() != 1 {
		t.Fatalf("PullRequestByNumberCallCount() = %d, want 1", fakeGitHub.PullRequestByNumberCallCount())
	}
	if fakeGitHub.MergePullRequestCallCount() != 2 {
		t.Fatalf("MergePullRequestCallCount() = %d, want 2", fakeGitHub.MergePullRequestCallCount())
	}
}

func TestMergePullRequestDoesNotRetryWhenRefreshStillReportsNotMergeable(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	mergeErr := errors.New("PUT https://api.github.com/repos/acme/widgets/pulls/11/merge: 405 Pull Request is not mergeable []")
	fakeGitHub.MergePullRequestReturns(mergeErr)
	fakeGitHub.PullRequestByNumberReturns(testPullRequestWithMergeable(11, "OPEN", "colin-93", false), nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	_, err := manager.MergePullRequest(context.Background(), workspacePath, repoops.Result{
		PRNumber: 11,
		PRURL:    "https://github.com/pmenglund/colin/pull/11",
		PRState:  "OPEN",
	})
	if !errors.Is(err, mergeErr) && (err == nil || err.Error() != mergeErr.Error()) {
		t.Fatalf("MergePullRequest() error = %v, want %v", err, mergeErr)
	}
	if fakeGitHub.PullRequestByNumberCallCount() != 1 {
		t.Fatalf("PullRequestByNumberCallCount() = %d, want 1", fakeGitHub.PullRequestByNumberCallCount())
	}
	if fakeGitHub.MergePullRequestCallCount() != 1 {
		t.Fatalf("MergePullRequestCallCount() = %d, want 1", fakeGitHub.MergePullRequestCallCount())
	}
}

func TestReviewContextReturnsUnresolvedThreads(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			reviewThreadNode("thread-1", "reviewer", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	reviewContext, err := manager.ReviewContext(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address review",
		BranchName: stringPtr("colin-93"),
	}, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if reviewContext.PullRequest.Number != 1 {
		t.Fatalf("pull request number = %d, want 1", reviewContext.PullRequest.Number)
	}
	if len(reviewContext.Threads) != 1 {
		t.Fatalf("threads length = %d, want 1", len(reviewContext.Threads))
	}
	if reviewContext.Threads[0].Body != "Please fix this." {
		t.Fatalf("thread body = %q, want %q", reviewContext.Threads[0].Body, "Please fix this.")
	}
}

func TestReviewContextIncludesCodexReviewSignals(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	requestedAt := time.Date(2026, 3, 28, 18, 1, 0, 0, time.UTC)
	approvedAt := time.Date(2026, 3, 28, 18, 2, 0, 0, time.UTC)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			reviewThreadNode("thread-1", "chatgpt-codex-connector", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{
		Reactions: []repoops.GitHubReaction{
			{Content: "EYES", CreatedAt: &requestedAt, UserLogin: "chatgpt-codex-connector"},
			{Content: "THUMBS_UP", CreatedAt: &approvedAt, UserLogin: "chatgpt-codex-connector"},
		},
	}, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	reviewContext, err := manager.ReviewContext(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address Codex review",
		BranchName: stringPtr("colin-93"),
	}, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if len(reviewContext.CodexReviewThreads) != 1 {
		t.Fatalf("codex review threads length = %d, want 1", len(reviewContext.CodexReviewThreads))
	}
	if !reviewContext.CodexReviewObserved {
		t.Fatal("CodexReviewObserved = false, want true")
	}
	if reviewContext.CodexReviewRequestedAt == nil {
		t.Fatal("CodexReviewRequestedAt = nil, want timestamp")
	}
	if reviewContext.CodexReviewApprovedAt == nil {
		t.Fatal("CodexReviewApprovedAt = nil, want timestamp")
	}
}

func TestReviewContextMarksResolvedCodexReviewAsObservedWithoutReactions(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			reviewThreadNode("thread-1", "chatgpt-codex-connector", "Please fix this.", true, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	reviewContext, err := manager.ReviewContext(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address Codex review",
		BranchName: stringPtr("colin-93"),
	}, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if !reviewContext.CodexReviewObserved {
		t.Fatal("CodexReviewObserved = false, want true")
	}
	if len(reviewContext.CodexReviewThreads) != 0 {
		t.Fatalf("codex review threads length = %d, want 0", len(reviewContext.CodexReviewThreads))
	}
	if reviewContext.CodexReviewRequestedAt != nil {
		t.Fatalf("CodexReviewRequestedAt = %v, want nil", reviewContext.CodexReviewRequestedAt)
	}
	if reviewContext.CodexReviewApprovedAt != nil {
		t.Fatalf("CodexReviewApprovedAt = %v, want nil", reviewContext.CodexReviewApprovedAt)
	}
}

func TestReviewContextIncludesCodexThreadWhenBotCommentIsOnLaterCommentPage(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	threadNode := reviewThreadNode("thread-1", "reviewer", "Comment 20", false, true)
	threadNode.Comments = repoops.GitHubReviewCommentConnection{
		Comments:    reviewComments("reviewer", 20),
		HasNextPage: true,
		EndCursor:   "comments-page-2",
	}

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{threadNode},
	}, nil)
	fakeGitHub.ReviewThreadCommentsReturns(repoops.GitHubReviewThreadCommentPage{
		Comments: []repoops.GitHubReviewComment{
			{AuthorLogin: "chatgpt-codex-connector"},
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	reviewContext, err := manager.ReviewContext(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address paginated Codex review",
		BranchName: stringPtr("colin-93"),
	}, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if len(reviewContext.CodexReviewThreads) != 1 {
		t.Fatalf("codex review threads length = %d, want 1", len(reviewContext.CodexReviewThreads))
	}
	if fakeGitHub.ReviewThreadCommentsCallCount() != 1 {
		t.Fatalf("ReviewThreadCommentsCallCount() = %d, want 1", fakeGitHub.ReviewThreadCommentsCallCount())
	}
}

func TestReviewContextAcceptsCodexBotLoginSuffix(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	requestedAt := time.Date(2026, 3, 28, 18, 1, 0, 0, time.UTC)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			reviewThreadNode("thread-1", "chatgpt-codex-connector[bot]", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{
		Reactions: []repoops.GitHubReaction{
			{Content: "EYES", CreatedAt: &requestedAt, UserLogin: "chatgpt-codex-connector[bot]"},
		},
	}, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	reviewContext, err := manager.ReviewContext(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address Codex bot review",
		BranchName: stringPtr("colin-93"),
	}, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if len(reviewContext.CodexReviewThreads) != 1 {
		t.Fatalf("codex review threads length = %d, want 1", len(reviewContext.CodexReviewThreads))
	}
	if reviewContext.CodexReviewRequestedAt == nil {
		t.Fatal("CodexReviewRequestedAt = nil, want timestamp")
	}
}

func TestReviewContextPrefersCurrentWorkspaceBranchOverTrackerBranchName(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadCalls(func(_ context.Context, _, _, head, _ string) (*repoops.GitHubPullRequest, error) {
		if head == "colin-93" {
			return testPullRequest(1, "OPEN", "colin-93"), nil
		}
		return nil, nil
	})
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			reviewThreadNode("thread-1", "reviewer", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	issue := domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address review",
		BranchName: stringPtr("pmenglund/colin-93"),
	}

	reviewContext, err := manager.ReviewContext(context.Background(), issue, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if reviewContext.PullRequest.Number != 1 {
		t.Fatalf("pull request number = %d, want 1", reviewContext.PullRequest.Number)
	}
	if fakeGitHub.PullRequestByHeadCallCount() != 1 {
		t.Fatalf("PullRequestByHeadCallCount() = %d, want 1", fakeGitHub.PullRequestByHeadCallCount())
	}
	_, _, _, head, _ := fakeGitHub.PullRequestByHeadArgsForCall(0)
	if head != "colin-93" {
		t.Fatalf("PullRequestByHead head = %q, want %q", head, "colin-93")
	}
}

func TestReviewContextFallsBackToMetadataActualBranchNameWhenWorkspaceBranchUnavailable(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)
	runCmd(t, workspacePath, "git", "checkout", "--detach")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadCalls(func(_ context.Context, _, _, head, _ string) (*repoops.GitHubPullRequest, error) {
		if head == "colin-93" {
			return testPullRequest(1, "OPEN", "colin-93"), nil
		}
		return nil, nil
	})
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			reviewThreadNode("thread-1", "reviewer", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	issue := domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address review",
		BranchName: stringPtr("pmenglund/colin-93"),
		ColinMetadata: &domain.ColinMetadata{
			ActualBranchName: "colin-93",
		},
	}

	reviewContext, err := manager.ReviewContext(context.Background(), issue, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if reviewContext.PullRequest.Number != 1 {
		t.Fatalf("pull request number = %d, want 1", reviewContext.PullRequest.Number)
	}
	if fakeGitHub.PullRequestByHeadCallCount() != 1 {
		t.Fatalf("PullRequestByHeadCallCount() = %d, want 1", fakeGitHub.PullRequestByHeadCallCount())
	}
	_, _, _, head, _ := fakeGitHub.PullRequestByHeadArgsForCall(0)
	if head != "colin-93" {
		t.Fatalf("PullRequestByHead head = %q, want %q", head, "colin-93")
	}
}

func TestPublishReusesTrackedPullRequestFromMetadata(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByNumberReturns(testPullRequest(11, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	result, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Reuse tracked PR",
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:  11,
			PullRequestURL:     "https://github.com/pmenglund/colin/pull/11",
			PullRequestState:   "OPEN",
			PullRequestHeadRef: "colin-93",
			PullRequestBaseRef: "symphony",
		},
	}, workspacePath)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.PRNumber != 11 {
		t.Fatalf("result.PRNumber = %d, want 11", result.PRNumber)
	}
	if fakeGitHub.CreatePullRequestCallCount() != 0 {
		t.Fatalf("CreatePullRequestCallCount() = %d, want 0", fakeGitHub.CreatePullRequestCallCount())
	}
	if fakeGitHub.PullRequestByNumberCallCount() != 1 {
		t.Fatalf("PullRequestByNumberCallCount() = %d, want 1", fakeGitHub.PullRequestByNumberCallCount())
	}
}

func TestPublishFailsWhenTrackedPullRequestHeadDoesNotMatchCurrentBranch(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://github.com/pmenglund/colin/pull/11",
		State:       "OPEN",
		HeadRefName: "pmenglund/colin-93",
		BaseRefName: "symphony",
	}, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	_, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Reject branch drift",
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:  11,
			PullRequestURL:     "https://github.com/pmenglund/colin/pull/11",
			PullRequestState:   "OPEN",
			PullRequestHeadRef: "pmenglund/colin-93",
			PullRequestBaseRef: "symphony",
		},
	}, workspacePath)
	if err == nil {
		t.Fatal("Publish() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "current branch") {
		t.Fatalf("Publish() error = %v, want current branch mismatch", err)
	}
	if fakeGitHub.CreatePullRequestCallCount() != 0 {
		t.Fatalf("CreatePullRequestCallCount() = %d, want 0", fakeGitHub.CreatePullRequestCallCount())
	}
}

func TestPublishFailsWhenBranchIsNotAheadOfBaseAndWorkspaceIsClean(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(nil, nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	_, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Refuse empty review handoff",
	}, workspacePath)
	if err == nil {
		t.Fatal("Publish() error = nil, want error")
	}
	if !errors.Is(err, repoops.ErrNoReviewableChanges) {
		t.Fatalf("Publish() error = %v, want ErrNoReviewableChanges", err)
	}
	if fakeGitHub.CreatePullRequestCallCount() != 0 {
		t.Fatalf("CreatePullRequestCallCount() = %d, want 0", fakeGitHub.CreatePullRequestCallCount())
	}
}

func TestPublishAdoptsSingleAttachedPullRequest(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByNumberReturns(testPullRequest(11, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	result, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Adopt attached PR",
		AttachedPullRequests: []domain.PullRequestRef{
			{Number: 11, URL: "https://github.com/pmenglund/colin/pull/11"},
		},
	}, workspacePath)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.PRNumber != 11 {
		t.Fatalf("result.PRNumber = %d, want 11", result.PRNumber)
	}
	if fakeGitHub.CreatePullRequestCallCount() != 0 {
		t.Fatalf("CreatePullRequestCallCount() = %d, want 0", fakeGitHub.CreatePullRequestCallCount())
	}
}

func TestPublishRebasesOntoRemoteBranchWhenPushIsRejectedAsNonFastForward(t *testing.T) {
	workspacePath, remotePath := setupRepoAutomationTest(t)

	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "initial\n")
	runCmd(t, workspacePath, "git", "add", "feature.txt")
	runCmd(t, workspacePath, "git", "commit", "-m", "initial feature work")
	runCmd(t, workspacePath, "git", "push", "-u", "origin", "colin-93")

	peerPath := filepath.Join(t.TempDir(), "peer")
	runCmd(t, "", "git", "clone", remotePath, peerPath)
	runCmd(t, peerPath, "git", "checkout", "colin-93")
	runCmd(t, peerPath, "git", "config", "user.name", "Peer User")
	runCmd(t, peerPath, "git", "config", "user.email", "peer@example.com")
	writeFile(t, filepath.Join(peerPath, "remote.txt"), "remote\n")
	runCmd(t, peerPath, "git", "add", "remote.txt")
	runCmd(t, peerPath, "git", "commit", "-m", "remote branch update")
	runCmd(t, peerPath, "git", "push", "origin", "colin-93")

	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local\n")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(11, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	result, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Rebase divergent branch before publish",
	}, workspacePath)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if result.PRNumber != 11 {
		t.Fatalf("result.PRNumber = %d, want 11", result.PRNumber)
	}
	if result.Action != "committed_and_pushed" {
		t.Fatalf("result.Action = %q, want %q", result.Action, "committed_and_pushed")
	}

	remoteLog := runCmd(t, "", "git", "--git-dir", remotePath, "log", "--format=%s", "colin-93", "-n", "3")
	for _, want := range []string{
		"COLIN-93: Rebase divergent branch before publish",
		"remote branch update",
	} {
		if !strings.Contains(remoteLog, want) {
			t.Fatalf("remote log = %q, want %q", remoteLog, want)
		}
	}
}

func TestPublishReturnsErrorWhenAutomaticRebaseConflicts(t *testing.T) {
	workspacePath, remotePath := setupRepoAutomationTest(t)

	writeFile(t, filepath.Join(workspacePath, "shared.txt"), "base\n")
	runCmd(t, workspacePath, "git", "add", "shared.txt")
	runCmd(t, workspacePath, "git", "commit", "-m", "initial feature work")
	runCmd(t, workspacePath, "git", "push", "-u", "origin", "colin-93")

	peerPath := filepath.Join(t.TempDir(), "peer")
	runCmd(t, "", "git", "clone", remotePath, peerPath)
	runCmd(t, peerPath, "git", "checkout", "colin-93")
	runCmd(t, peerPath, "git", "config", "user.name", "Peer User")
	runCmd(t, peerPath, "git", "config", "user.email", "peer@example.com")
	writeFile(t, filepath.Join(peerPath, "shared.txt"), "remote\n")
	runCmd(t, peerPath, "git", "add", "shared.txt")
	runCmd(t, peerPath, "git", "commit", "-m", "remote conflicting update")
	runCmd(t, peerPath, "git", "push", "origin", "colin-93")

	writeFile(t, filepath.Join(workspacePath, "shared.txt"), "local\n")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(11, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	_, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Conflict while rebasing divergent branch",
	}, workspacePath)
	if err == nil {
		t.Fatal("Publish() error = nil, want rebase conflict")
	}
	if !strings.Contains(err.Error(), "rebase onto origin/colin-93 failed") {
		t.Fatalf("Publish() error = %v, want rebase failure", err)
	}

	status := runCmd(t, workspacePath, "git", "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("workspace status = %q, want clean after failed rebase recovery", status)
	}
}

func TestPublishFailsWhenMultipleAttachedPullRequestsExist(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)
	_, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Reject duplicate attached PRs",
		AttachedPullRequests: []domain.PullRequestRef{
			{Number: 11, URL: "https://github.com/pmenglund/colin/pull/11"},
			{Number: 14, URL: "https://github.com/pmenglund/colin/pull/14"},
		},
	}, workspacePath)
	if err == nil {
		t.Fatal("Publish() error = nil, want error")
	}
	if !errors.Is(err, repoops.ErrDuplicatePullRequests) {
		t.Fatalf("Publish() error = %v, want ErrDuplicatePullRequests", err)
	}
	if fakeGitHub.CreatePullRequestCallCount() != 0 {
		t.Fatalf("CreatePullRequestCallCount() = %d, want 0", fakeGitHub.CreatePullRequestCallCount())
	}
}

func TestReplyAndResolveReviewThreadRunsGraphQLMutations(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)
	fakeGitHub := &fakes.FakeGitHubClient{}
	manager := repoops.NewManagerWithRepoHostClient(testConfig(), testLogger(), fakeGitHub)

	thread := domain.ReviewThread{
		ID:         "thread-1",
		Path:       "internal/foo.go",
		CommentID:  "comment-1",
		CanReply:   true,
		CanResolve: true,
	}
	if err := manager.ReplyAndResolveReviewThread(context.Background(), workspacePath, thread, "[colin] Addressed."); err != nil {
		t.Fatalf("ReplyAndResolveReviewThread() error = %v", err)
	}

	if fakeGitHub.ReplyToReviewThreadCallCount() != 1 {
		t.Fatalf("ReplyToReviewThreadCallCount() = %d, want 1", fakeGitHub.ReplyToReviewThreadCallCount())
	}
	if fakeGitHub.ResolveReviewThreadCallCount() != 1 {
		t.Fatalf("ResolveReviewThreadCallCount() = %d, want 1", fakeGitHub.ResolveReviewThreadCallCount())
	}
	_, threadID, body := fakeGitHub.ReplyToReviewThreadArgsForCall(0)
	if threadID != "thread-1" || body != "[colin] Addressed." {
		t.Fatalf("ReplyToReviewThread args = %q %q, want thread-1 [colin] Addressed.", threadID, body)
	}
}

func setupRepoAutomationTest(t *testing.T) (workspacePath string, remotePath string) {
	t.Helper()

	tempDir := t.TempDir()
	remotePath = filepath.Join(tempDir, "remote.git")
	seedPath := filepath.Join(tempDir, "seed")
	workspacePath = filepath.Join(tempDir, "workspace")

	runCmd(t, "", "git", "init", "--bare", remotePath)
	runCmd(t, "", "git", "init", seedPath)
	configureGitIdentity(t, seedPath, "Test User", "test@example.com")
	writeFile(t, filepath.Join(seedPath, "README.md"), "seed\n")
	runCmd(t, seedPath, "git", "add", "README.md")
	runCmd(t, seedPath, "git", "commit", "-m", "seed")
	runCmd(t, seedPath, "git", "branch", "-M", "symphony")
	runCmd(t, seedPath, "git", "remote", "add", "origin", remotePath)
	runCmd(t, seedPath, "git", "push", "-u", "origin", "symphony")

	runCmd(t, "", "git", "clone", remotePath, workspacePath)
	configureGitIdentity(t, workspacePath, "Test User", "test@example.com")
	runCmd(t, workspacePath, "git", "checkout", "-b", "colin-93", "origin/symphony")

	return workspacePath, remotePath
}

func testConfig() domain.ServiceConfig {
	return domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			BaseRef: "symphony",
		},
		Repo: domain.RepoConfig{
			RemoteName:  "origin",
			MergeMethod: "merge",
		},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func configureGitIdentity(t *testing.T, cwd, name, email string) {
	t.Helper()

	runCmd(t, cwd, "git", "config", "user.name", name)
	runCmd(t, cwd, "git", "config", "user.email", email)
}

func runCmd(t *testing.T, cwd string, name string, args ...string) string {
	t.Helper()

	cmd := exec.Command(name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, string(output))
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func stringPtr(value string) *string {
	return &value
}

func testPullRequest(number int, state, head string) *repoops.GitHubPullRequest {
	return &repoops.GitHubPullRequest{
		Number:      number,
		URL:         fmt.Sprintf("https://github.com/pmenglund/colin/pull/%d", number),
		State:       state,
		HeadRefName: head,
		BaseRefName: "symphony",
	}
}

func testPullRequestWithMergeable(number int, state, head string, mergeable bool) *repoops.GitHubPullRequest {
	pr := testPullRequest(number, state, head)
	pr.Mergeable = &mergeable
	return pr
}

func reviewThreadNode(id, author, body string, resolved bool, commentsHasNextPage bool) repoops.GitHubReviewThread {
	createdAt := time.Date(2026, 3, 28, 18, 0, 0, 0, time.UTC)
	line := 42
	startLine := 40
	return repoops.GitHubReviewThread{
		ID:               id,
		IsResolved:       resolved,
		IsOutdated:       false,
		ViewerCanReply:   true,
		ViewerCanResolve: true,
		Path:             "internal/foo.go",
		Line:             &line,
		StartLine:        &startLine,
		Comments: repoops.GitHubReviewCommentConnection{
			Comments: []repoops.GitHubReviewComment{
				{
					ID:          "comment-1",
					Body:        body,
					URL:         "https://example.test/comment/1",
					CreatedAt:   &createdAt,
					AuthorLogin: author,
				},
			},
			HasNextPage: commentsHasNextPage,
			EndCursor:   "comments-page-2",
		},
	}
}

func reviewComments(author string, count int) []repoops.GitHubReviewComment {
	out := make([]repoops.GitHubReviewComment, 0, count)
	createdAt := time.Date(2026, 3, 28, 18, 0, 0, 0, time.UTC)
	for i := 1; i <= count; i++ {
		out = append(out, repoops.GitHubReviewComment{
			ID:          fmt.Sprintf("comment-%d", i),
			Body:        fmt.Sprintf("Comment %d", i),
			URL:         fmt.Sprintf("https://example.test/comment/%d", i),
			CreatedAt:   &createdAt,
			AuthorLogin: author,
		})
	}
	return out
}
