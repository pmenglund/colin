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
	repoops "github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/repoops/fakes"
)

func TestPublishCreatesCommitPushesBranchAndOpensPR(t *testing.T) {
	workspacePath, remotePath := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturnsOnCall(0, nil, nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(1, nil, nil)
	fakeGitHub.CreatePullRequestReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(2, testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(3, testPullRequest(1, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

	manager := repoops.NewManagerWithGitHubClient(cfg, testLogger(), fakeGitHub)
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

func TestMergeMergesExistingPR(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturnsOnCall(0, nil, nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(1, nil, nil)
	fakeGitHub.CreatePullRequestReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.PullRequestByHeadReturnsOnCall(2, testPullRequest(1, "OPEN", "colin-93"), nil)

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

func TestReviewContextReturnsUnresolvedThreads(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []map[string]any{
			reviewThreadNode("thread-1", "reviewer", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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
		Threads: []map[string]any{
			reviewThreadNode("thread-1", "chatgpt-codex-connector[bot]", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{
		Reactions: []repoops.GitHubReaction{
			{Content: "EYES", CreatedAt: &requestedAt, UserLogin: "chatgpt-codex-connector[bot]"},
			{Content: "THUMBS_UP", CreatedAt: &approvedAt, UserLogin: "chatgpt-codex-connector[bot]"},
		},
	}, nil)

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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
	if reviewContext.CodexReviewRequestedAt == nil {
		t.Fatal("CodexReviewRequestedAt = nil, want timestamp")
	}
	if reviewContext.CodexReviewApprovedAt == nil {
		t.Fatal("CodexReviewApprovedAt = nil, want timestamp")
	}
}

func TestReviewContextIncludesCodexThreadWhenBotCommentIsOnLaterCommentPage(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	threadNode := reviewThreadNode("thread-1", "reviewer", "Comment 20", false, true)
	threadNode["comments"] = map[string]any{
		"nodes":    reviewComments("reviewer", 20),
		"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "comments-page-2"},
	}

	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(testPullRequest(1, "OPEN", "colin-93"), nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []map[string]any{threadNode},
	}, nil)
	fakeGitHub.ReviewThreadCommentsReturns(repoops.GitHubReviewThreadCommentPage{
		Comments: []map[string]any{
			{"author": map[string]any{"login": "chatgpt-codex-connector[bot]"}},
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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
		Threads: []map[string]any{
			reviewThreadNode("thread-1", "reviewer", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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
		Threads: []map[string]any{
			reviewThreadNode("thread-1", "reviewer", "Please fix this.", false, false),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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

func TestPublishFailsWhenMultipleAttachedPullRequestsExist(t *testing.T) {
	workspacePath, _ := setupRepoAutomationTest(t)

	fakeGitHub := &fakes.FakeGitHubClient{}
	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)
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
	manager := repoops.NewManagerWithGitHubClient(testConfig(), testLogger(), fakeGitHub)

	thread := domain.GitHubReviewThread{
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
	runCmd(t, seedPath, "git", "config", "user.name", "Test User")
	runCmd(t, seedPath, "git", "config", "user.email", "test@example.com")
	writeFile(t, filepath.Join(seedPath, "README.md"), "seed\n")
	runCmd(t, seedPath, "git", "add", "README.md")
	runCmd(t, seedPath, "git", "commit", "-m", "seed")
	runCmd(t, seedPath, "git", "branch", "-M", "symphony")
	runCmd(t, seedPath, "git", "remote", "add", "origin", remotePath)
	runCmd(t, seedPath, "git", "push", "-u", "origin", "symphony")

	runCmd(t, "", "git", "clone", remotePath, workspacePath)
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

func reviewThreadNode(id, author, body string, resolved bool, commentsHasNextPage bool) map[string]any {
	return map[string]any{
		"id":               id,
		"isResolved":       resolved,
		"isOutdated":       false,
		"viewerCanReply":   true,
		"viewerCanResolve": true,
		"path":             "internal/foo.go",
		"line":             float64(42),
		"startLine":        float64(40),
		"comments": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":        "comment-1",
					"body":      body,
					"url":       "https://example.test/comment/1",
					"createdAt": "2026-03-28T18:00:00Z",
					"author":    map[string]any{"login": author},
				},
			},
			"pageInfo": map[string]any{
				"hasNextPage": commentsHasNextPage,
				"endCursor":   "comments-page-2",
			},
		},
	}
}

func reviewComments(author string, count int) []any {
	out := make([]any, 0, count)
	for i := 1; i <= count; i++ {
		out = append(out, map[string]any{
			"id":        fmt.Sprintf("comment-%d", i),
			"body":      fmt.Sprintf("Comment %d", i),
			"url":       fmt.Sprintf("https://example.test/comment/%d", i),
			"createdAt": "2026-03-28T18:00:00Z",
			"author":    map[string]any{"login": author},
		})
	}
	return out
}
