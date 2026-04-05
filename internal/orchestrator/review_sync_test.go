package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/repoops/fakes"
	"github.com/pmenglund/colin/internal/workspace"
)

func TestNeedsReviewSyncRequiresConcretePullRequestSignal(t *testing.T) {
	reviewCycle := &domain.ReviewCycle{
		EnteredReviewAt:  time.Date(2026, time.March, 29, 18, 0, 0, 0, time.UTC),
		ReturnedToTodoAt: time.Date(2026, time.March, 30, 18, 0, 0, 0, time.UTC),
	}
	branch := "colin-123"

	tests := []struct {
		name  string
		issue domain.Issue
		want  bool
	}{
		{
			name: "no pr signal",
			issue: domain.Issue{
				State:       "Todo",
				ReviewCycle: reviewCycle,
				BranchName:  &branch,
			},
			want: false,
		},
		{
			name: "tracked metadata pr",
			issue: domain.Issue{
				State:       "Todo",
				ReviewCycle: reviewCycle,
				BranchName:  &branch,
				ColinMetadata: &domain.ColinMetadata{
					PullRequestNumber: 11,
				},
			},
			want: true,
		},
		{
			name: "single attached pr",
			issue: domain.Issue{
				State:       "Todo",
				ReviewCycle: reviewCycle,
				BranchName:  &branch,
				AttachedPullRequests: []domain.PullRequestRef{
					{Number: 11, URL: "https://github.com/pmenglund/colin/pull/11"},
				},
			},
			want: true,
		},
		{
			name: "cross-repo attached prs remain concrete",
			issue: domain.Issue{
				State:       "Todo",
				ReviewCycle: reviewCycle,
				BranchName:  &branch,
				AttachedPullRequests: []domain.PullRequestRef{
					{
						Backend:    "github",
						Owner:      "pmenglund",
						Repository: "colin",
						Number:     11,
						URL:        "https://github.com/pmenglund/colin/pull/11",
					},
					{
						Backend:    "github",
						Owner:      "pmenglund",
						Repository: "sibling",
						Number:     11,
						URL:        "https://github.com/pmenglund/sibling/pull/11",
					},
				},
			},
			want: true,
		},
		{
			name: "multiple attached prs in the same repo are ambiguous",
			issue: domain.Issue{
				State:       "Todo",
				ReviewCycle: reviewCycle,
				BranchName:  &branch,
				AttachedPullRequests: []domain.PullRequestRef{
					{
						Backend:    "github",
						Owner:      "pmenglund",
						Repository: "colin",
						Number:     11,
						URL:        "https://github.com/pmenglund/colin/pull/11",
					},
					{
						Backend:    "github",
						Owner:      "pmenglund",
						Repository: "colin",
						Number:     12,
						URL:        "https://github.com/pmenglund/colin/pull/12",
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		if got := needsReviewSync(tt.issue); got != tt.want {
			t.Fatalf("%s: needsReviewSync() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPrepareReviewIssueAcceptsCrossRepoAttachedPullRequestsWhenWorkspaceRepoDisambiguates(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	fakeGitHub.PullRequestByNumberCalls(func(_ context.Context, owner, repo string, number int) (*repoops.GitHubPullRequest, error) {
		if owner != "pmenglund" || repo != "colin" || number != 11 {
			t.Fatalf("PullRequestByNumber() args = %q %q %d, want pmenglund colin 11", owner, repo, number)
		}
		return &repoops.GitHubPullRequest{
			Number:      11,
			URL:         "https://github.com/pmenglund/colin/pull/11",
			State:       "OPEN",
			HeadRefName: "colin-123",
			BaseRefName: "main",
		}, nil
	})
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, fakeGitHub),
			Workspace: workspace.NewManager(cfg, logger),
		},
		reviewSync: map[string]*reviewSyncState{
			"1": {
				firstObserved: time.Now().UTC().Add(-time.Minute),
				comment:       &commentThreadState{RootCommentID: "root"},
			},
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	branch := "colin-123"
	now := time.Date(2026, time.March, 30, 19, 0, 0, 0, time.UTC)
	issue := domain.Issue{
		ID:         "1",
		Identifier: "COLIN-123",
		Title:      "Resume coding with cross-repo attachments",
		State:      "Todo",
		BranchName: &branch,
		ReviewCycle: &domain.ReviewCycle{
			EnteredReviewAt:  now.Add(-2 * time.Hour),
			ReturnedToTodoAt: now.Add(-time.Hour),
		},
		AttachedPullRequests: []domain.PullRequestRef{
			{
				Backend:    "github",
				Owner:      "pmenglund",
				Repository: "colin",
				Number:     11,
				URL:        "https://github.com/pmenglund/colin/pull/11",
			},
			{
				Backend:    "github",
				Owner:      "pmenglund",
				Repository: "sibling",
				Number:     11,
				URL:        "https://github.com/pmenglund/sibling/pull/11",
			},
		},
	}

	prepared, ready := orch.prepareReviewIssue(context.Background(), issue, now)

	if !ready {
		t.Fatalf("prepareReviewIssue() ready = false, want true")
	}
	if prepared.PullRequest == nil {
		t.Fatal("PullRequest = nil, want resolved attached PR")
	}
	if prepared.PullRequest.Owner != "" || prepared.PullRequest.Repository != "" {
		t.Fatalf("prepared.PullRequest identity = %+v, want review context PR payload", prepared.PullRequest)
	}
	if prepared.PullRequest.Number != 11 {
		t.Fatalf("prepared.PullRequest.Number = %d, want 11", prepared.PullRequest.Number)
	}
	if got := fakeGitHub.PullRequestByNumberCallCount(); got != 1 {
		t.Fatalf("PullRequestByNumberCallCount() = %d, want 1", got)
	}
	if _, ok := orch.reviewSync[issue.ID]; ok {
		t.Fatal("reviewSync state was not cleared after successful PR resolution")
	}
	if got := len(tracker.issueComments); got != 0 {
		t.Fatalf("issueComments length = %d, want 0", got)
	}
}

func TestPrepareReviewIssueDoesNotWaitWhenReviewContextHasNoPullRequest(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	fakeGitHub.PullRequestByHeadReturns(nil, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, fakeGitHub),
			Workspace: workspace.NewManager(cfg, logger),
		},
		reviewSync: map[string]*reviewSyncState{
			"1": {
				firstObserved: time.Now().UTC().Add(-time.Minute),
				comment:       &commentThreadState{RootCommentID: "root"},
			},
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	branch := "colin-123"
	now := time.Date(2026, time.March, 30, 19, 0, 0, 0, time.UTC)
	issue := domain.Issue{
		ID:         "1",
		Identifier: "COLIN-123",
		Title:      "Resume coding without a PR",
		State:      "Todo",
		BranchName: &branch,
		ReviewCycle: &domain.ReviewCycle{
			EnteredReviewAt:  now.Add(-2 * time.Hour),
			ReturnedToTodoAt: now.Add(-time.Hour),
		},
		AttachedPullRequests: []domain.PullRequestRef{
			{Number: 11, URL: "https://github.com/example/other-repo/pull/11"},
		},
	}

	prepared, ready := orch.prepareReviewIssue(context.Background(), issue, now)

	if !ready {
		t.Fatalf(
			"prepareReviewIssue() ready = false, want true (comments=%q replies=%q reviewSync=%#v pullRequest=%#v reviewThreads=%d)",
			tracker.issueComments,
			tracker.commentReplies,
			orch.reviewSync[issue.ID],
			prepared.PullRequest,
			len(prepared.ReviewThreads),
		)
	}
	if len(prepared.ReviewThreads) != 0 {
		t.Fatalf("ReviewThreads length = %d, want 0", len(prepared.ReviewThreads))
	}
	if prepared.PullRequest != nil {
		t.Fatalf("PullRequest = %#v, want nil", prepared.PullRequest)
	}
	if got := len(tracker.issueComments); got != 0 {
		t.Fatalf("issueComments length = %d, want 0", got)
	}
	if got := len(tracker.commentReplies); got != 0 {
		t.Fatalf("commentReplies length = %d, want 0", got)
	}
	if _, ok := orch.reviewSync[issue.ID]; ok {
		t.Fatal("reviewSync state was not cleared after no-PR fallback")
	}
}

func TestPrepareReviewIssueStartsImmediatelyWhenTrackedPullRequestHasNoUnresolvedThreads(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://example.test/pr/11",
		State:       "OPEN",
		HeadRefName: "colin-123",
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, fakeGitHub),
			Workspace: workspace.NewManager(cfg, logger),
		},
		reviewSync: map[string]*reviewSyncState{},
		running:    map[string]*runningEntry{},
		claimed:    map[string]struct{}{},
		retrying:   map[string]*retryState{},
		completed:  map[string]string{},
	}

	branch := "colin-123"
	now := time.Date(2026, time.March, 30, 19, 0, 0, 0, time.UTC)
	issue := domain.Issue{
		ID:         "1",
		Identifier: "COLIN-123",
		Title:      "Wait for PR review sync",
		State:      "Todo",
		BranchName: &branch,
		ReviewCycle: &domain.ReviewCycle{
			EnteredReviewAt:  now.Add(-2 * time.Hour),
			ReturnedToTodoAt: now.Add(-time.Hour),
		},
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:  11,
			PullRequestURL:     "https://example.test/pr/11",
			PullRequestHeadRef: "colin-123",
			PullRequestBaseRef: "symphony",
		},
	}

	prepared, ready := orch.prepareReviewIssue(context.Background(), issue, now)

	if !ready {
		t.Fatal("prepareReviewIssue() ready = false, want true")
	}
	if prepared.PullRequest == nil || prepared.PullRequest.Number != 11 {
		t.Fatalf("PullRequest = %#v, want tracked PR #11", prepared.PullRequest)
	}
	if got := len(prepared.ReviewThreads); got != 0 {
		t.Fatalf("ReviewThreads length = %d, want 0", got)
	}
	if got := len(tracker.issueComments); got != 0 {
		t.Fatalf("issueComments length = %d, want 0", got)
	}
	if got := len(tracker.commentReplies); got != 0 {
		t.Fatalf("commentReplies length = %d, want 0", got)
	}
	if _, ok := orch.reviewSync[issue.ID]; ok {
		t.Fatal("reviewSync state should be cleared after immediate dispatch")
	}
}

func TestPrepareReviewIssueInjectsUnresolvedThreadsWhenTrackedPullRequestHasThem(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://example.test/pr/11",
		State:       "OPEN",
		HeadRefName: "colin-123",
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			reviewSyncThreadNode("thread-1", "reviewer", "Please fix this."),
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}

	branch := "colin-123"
	now := time.Date(2026, time.March, 30, 19, 0, 0, 0, time.UTC)
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, fakeGitHub),
			Workspace: workspace.NewManager(cfg, logger),
		},
		reviewSync: map[string]*reviewSyncState{
			"1": {
				firstObserved: now.Add(-time.Minute),
				comment:       &commentThreadState{RootCommentID: "root"},
			},
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	issue := domain.Issue{
		ID:         "1",
		Identifier: "COLIN-123",
		Title:      "Resume coding with unresolved review threads",
		State:      "Todo",
		BranchName: &branch,
		ReviewCycle: &domain.ReviewCycle{
			EnteredReviewAt:  now.Add(-2 * time.Hour),
			ReturnedToTodoAt: now.Add(-time.Hour),
		},
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:  11,
			PullRequestURL:     "https://example.test/pr/11",
			PullRequestHeadRef: "colin-123",
			PullRequestBaseRef: "symphony",
		},
	}

	prepared, ready := orch.prepareReviewIssue(context.Background(), issue, now)

	if !ready {
		t.Fatal("prepareReviewIssue() ready = false, want true")
	}
	if prepared.PullRequest == nil || prepared.PullRequest.Number != 11 {
		t.Fatalf("PullRequest = %#v, want tracked PR #11", prepared.PullRequest)
	}
	if got := len(prepared.ReviewThreads); got != 1 {
		t.Fatalf("ReviewThreads length = %d, want 1", got)
	}
	if prepared.ReviewThreads[0].Body != "Please fix this." {
		t.Fatalf("ReviewThreads[0].Body = %q, want %q", prepared.ReviewThreads[0].Body, "Please fix this.")
	}
	if got := len(tracker.issueComments); got != 0 {
		t.Fatalf("issueComments length = %d, want 0", got)
	}
	if got := len(tracker.commentReplies); got != 1 {
		t.Fatalf("commentReplies length = %d, want 1", got)
	}
	if !strings.Contains(tracker.commentReplies[0], "GitHub review feedback synced, so Colin is starting work now.") {
		t.Fatalf("comment reply = %q, want sync confirmation", tracker.commentReplies[0])
	}
	if !strings.Contains(tracker.commentReplies[0], "Unresolved review threads: `1`") {
		t.Fatalf("comment reply = %q, want thread count", tracker.commentReplies[0])
	}
	if _, ok := orch.reviewSync[issue.ID]; ok {
		t.Fatal("reviewSync state should be cleared after syncing review threads")
	}
}

func TestPrepareReviewIssueRepliesWhenReviewThreadsSyncAndWorkCanResume(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://example.test/pr/11",
		State:       "OPEN",
		HeadRefName: "colin-123",
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			{
				ID:               "thread-1",
				IsResolved:       false,
				IsOutdated:       false,
				ViewerCanReply:   true,
				ViewerCanResolve: true,
				Path:             "internal/foo.go",
				Comments: repoops.GitHubReviewCommentConnection{
					Comments: []repoops.GitHubReviewComment{
						{ID: "comment-1", Body: "Please fix this.", AuthorLogin: "reviewer"},
					},
				},
			},
		},
	}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, fakeGitHub),
			Workspace: workspace.NewManager(cfg, logger),
		},
		reviewSync: map[string]*reviewSyncState{
			"1": {
				firstObserved: time.Date(2026, time.March, 30, 18, 55, 0, 0, time.UTC),
				comment:       &commentThreadState{RootCommentID: "root"},
			},
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	branch := "colin-123"
	now := time.Date(2026, time.March, 30, 19, 0, 0, 0, time.UTC)
	issue := domain.Issue{
		ID:         "1",
		Identifier: "COLIN-123",
		Title:      "Resume coding after review threads sync",
		State:      "Todo",
		BranchName: &branch,
		ReviewCycle: &domain.ReviewCycle{
			EnteredReviewAt:  now.Add(-2 * time.Hour),
			ReturnedToTodoAt: now.Add(-time.Hour),
		},
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:  11,
			PullRequestURL:     "https://example.test/pr/11",
			PullRequestHeadRef: "colin-123",
			PullRequestBaseRef: "symphony",
		},
	}

	prepared, ready := orch.prepareReviewIssue(context.Background(), issue, now)

	if !ready {
		t.Fatal("prepareReviewIssue() ready = false, want true")
	}
	if prepared.PullRequest == nil || prepared.PullRequest.Number != 11 {
		t.Fatalf("PullRequest = %#v, want tracked PR #11", prepared.PullRequest)
	}
	if got := len(prepared.ReviewThreads); got != 1 {
		t.Fatalf("ReviewThreads length = %d, want 1", got)
	}
	if got := len(tracker.commentReplies); got != 1 {
		t.Fatalf("commentReplies length = %d, want 1", got)
	}
	if !strings.Contains(tracker.commentReplies[0], "GitHub review feedback synced, so Colin is starting work now.") {
		t.Fatalf("comment reply = %q, want sync-ready message", tracker.commentReplies[0])
	}
	if !strings.Contains(tracker.commentReplies[0], "What Colin is doing next: starting the next coding round with the synced GitHub review feedback.") {
		t.Fatalf("comment reply = %q, want next-step guidance", tracker.commentReplies[0])
	}
	if !strings.Contains(tracker.commentReplies[0], "What you should do: nothing yet unless Colin later reports that more review follow-up is needed.") {
		t.Fatalf("comment reply = %q, want human guidance", tracker.commentReplies[0])
	}
	if _, ok := orch.reviewSync[issue.ID]; ok {
		t.Fatal("reviewSync state was not cleared after review threads synced")
	}
}

func setupReviewSyncTestRuntime(t *testing.T) (domain.ServiceConfig, *fakes.FakeGitHubClient) {
	t.Helper()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "origin.git")
	seedPath := filepath.Join(tempDir, "seed")

	reviewSyncRunCmd(t, "", "git", "init", "--bare", remotePath)
	reviewSyncRunCmd(t, "", "git", "init", seedPath)
	reviewSyncRunCmd(t, seedPath, "git", "config", "user.name", "Test User")
	reviewSyncRunCmd(t, seedPath, "git", "config", "user.email", "test@example.com")
	reviewSyncWriteFile(t, filepath.Join(seedPath, "README.md"), "seed\n")
	reviewSyncRunCmd(t, seedPath, "git", "add", "README.md")
	reviewSyncRunCmd(t, seedPath, "git", "commit", "-m", "seed")
	reviewSyncRunCmd(t, seedPath, "git", "branch", "-M", "symphony")
	reviewSyncRunCmd(t, seedPath, "git", "remote", "add", "origin", remotePath)
	reviewSyncRunCmd(t, seedPath, "git", "push", "-u", "origin", "symphony")

	return domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:    filepath.Join(tempDir, "workspaces"),
			RepoURL: remotePath,
			BaseRef: "symphony",
		},
		Repo: domain.RepoConfig{
			RemoteName:     "origin",
			BranchTemplate: "colin/{{.issue.identifier}}",
		},
		Hooks: domain.HookConfig{
			AfterCreate: "git remote set-url origin https://github.com/pmenglund/colin.git",
		},
	}, &fakes.FakeGitHubClient{}
}

func reviewSyncRunCmd(t *testing.T, cwd string, name string, args ...string) string {
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

func reviewSyncWriteFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func reviewSyncThreadNode(id, author, body string) repoops.GitHubReviewThread {
	createdAt := time.Date(2026, time.March, 28, 18, 0, 0, 0, time.UTC)
	line := 42
	startLine := 40
	return repoops.GitHubReviewThread{
		ID:               id,
		IsResolved:       false,
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
		},
	}
}
