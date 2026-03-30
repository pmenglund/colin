package repoops

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/domain"
)

func TestPublishCreatesCommitPushesBranchAndOpensPR(t *testing.T) {
	workspacePath, remotePath, ghLogPath := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	manager := NewManager(testConfig(), testLogger())
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

	log := readFile(t, ghLogPath)
	if !strings.Contains(log, "pr create") {
		t.Fatalf("gh log = %q, want pr create", log)
	}
}

func TestPublishUsesConfiguredPRTemplate(t *testing.T) {
	workspacePath, _, ghLogPath := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	cfg := testConfig()
	cfg.Workspace.BaseRef = "symphony"
	cfg.Repo.PRTemplate = "PRBODY issue={{.issue.identifier}} branch={{.branch}} base={{.base_ref}}"

	manager := NewManager(cfg, testLogger())
	if _, err := manager.Publish(context.Background(), domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Use template",
	}, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	log := readFile(t, ghLogPath)
	if !strings.Contains(log, "PRBODY issue=COLIN-93 branch=colin-93 base=symphony") {
		t.Fatalf("gh log = %q, want rendered PR body", log)
	}
}

func TestMergeMergesExistingPR(t *testing.T) {
	workspacePath, _, ghLogPath := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	manager := NewManager(testConfig(), testLogger())
	issue := domain.Issue{Identifier: "COLIN-93", Title: "Add merge automation"}
	if _, err := manager.Publish(context.Background(), issue, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	result, err := manager.Merge(context.Background(), issue, workspacePath)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if result.Action != "merged" {
		t.Fatalf("result.Action = %q, want %q", result.Action, "merged")
	}

	log := readFile(t, ghLogPath)
	if !strings.Contains(log, "pr merge 1 --merge") {
		t.Fatalf("gh log = %q, want merge invocation", log)
	}
}

func TestReviewContextReturnsUnresolvedThreads(t *testing.T) {
	workspacePath, _, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	manager := NewManager(testConfig(), testLogger())
	issue := domain.Issue{Identifier: "COLIN-93", Title: "Address review"}
	if _, err := manager.Publish(context.Background(), issue, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	writeFile(t, os.Getenv("COLIN_FAKE_GH_REVIEW_THREADS"), `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread-1","isResolved":false,"isOutdated":false,"viewerCanReply":true,"viewerCanResolve":true,"path":"internal/foo.go","line":42,"startLine":40,"comments":{"nodes":[{"id":"comment-1","body":"Please fix this.","url":"https://example.test/comment/1","createdAt":"2026-03-28T18:00:00Z","author":{"login":"reviewer"}}]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`)

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
	workspacePath, _, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	manager := NewManager(testConfig(), testLogger())
	issue := domain.Issue{Identifier: "COLIN-93", Title: "Address Codex review"}
	if _, err := manager.Publish(context.Background(), issue, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	writeFile(t, os.Getenv("COLIN_FAKE_GH_REVIEW_THREADS"), `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread-1","isResolved":false,"isOutdated":false,"viewerCanReply":true,"viewerCanResolve":true,"path":"internal/foo.go","line":42,"startLine":40,"comments":{"nodes":[{"id":"comment-1","body":"Please fix this.","url":"https://example.test/comment/1","createdAt":"2026-03-28T18:00:00Z","author":{"login":"chatgpt-codex-connector[bot]"}}]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`)
	writeFile(t, os.Getenv("COLIN_FAKE_GH_REACTIONS"), `{"data":{"repository":{"pullRequest":{"reactions":{"nodes":[{"content":"EYES","createdAt":"2026-03-28T18:01:00Z","user":{"login":"chatgpt-codex-connector[bot]"}},{"content":"THUMBS_UP","createdAt":"2026-03-28T18:02:00Z","user":{"login":"chatgpt-codex-connector[bot]"}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`)

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
	workspacePath, _, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")

	manager := NewManager(testConfig(), testLogger())
	issue := domain.Issue{Identifier: "COLIN-93", Title: "Address paginated Codex review"}
	if _, err := manager.Publish(context.Background(), issue, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	writeFile(t, os.Getenv("COLIN_FAKE_GH_REVIEW_THREADS"), `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread-1","isResolved":false,"isOutdated":false,"viewerCanReply":true,"viewerCanResolve":true,"path":"internal/foo.go","line":42,"startLine":40,"comments":{"nodes":[{"id":"comment-1","body":"Comment 1","url":"https://example.test/comment/1","createdAt":"2026-03-28T18:00:00Z","author":{"login":"reviewer-1"}},{"id":"comment-2","body":"Comment 2","url":"https://example.test/comment/2","createdAt":"2026-03-28T18:01:00Z","author":{"login":"reviewer-2"}},{"id":"comment-3","body":"Comment 3","url":"https://example.test/comment/3","createdAt":"2026-03-28T18:02:00Z","author":{"login":"reviewer-3"}},{"id":"comment-4","body":"Comment 4","url":"https://example.test/comment/4","createdAt":"2026-03-28T18:03:00Z","author":{"login":"reviewer-4"}},{"id":"comment-5","body":"Comment 5","url":"https://example.test/comment/5","createdAt":"2026-03-28T18:04:00Z","author":{"login":"reviewer-5"}},{"id":"comment-6","body":"Comment 6","url":"https://example.test/comment/6","createdAt":"2026-03-28T18:05:00Z","author":{"login":"reviewer-6"}},{"id":"comment-7","body":"Comment 7","url":"https://example.test/comment/7","createdAt":"2026-03-28T18:06:00Z","author":{"login":"reviewer-7"}},{"id":"comment-8","body":"Comment 8","url":"https://example.test/comment/8","createdAt":"2026-03-28T18:07:00Z","author":{"login":"reviewer-8"}},{"id":"comment-9","body":"Comment 9","url":"https://example.test/comment/9","createdAt":"2026-03-28T18:08:00Z","author":{"login":"reviewer-9"}},{"id":"comment-10","body":"Comment 10","url":"https://example.test/comment/10","createdAt":"2026-03-28T18:09:00Z","author":{"login":"reviewer-10"}},{"id":"comment-11","body":"Comment 11","url":"https://example.test/comment/11","createdAt":"2026-03-28T18:10:00Z","author":{"login":"reviewer-11"}},{"id":"comment-12","body":"Comment 12","url":"https://example.test/comment/12","createdAt":"2026-03-28T18:11:00Z","author":{"login":"reviewer-12"}},{"id":"comment-13","body":"Comment 13","url":"https://example.test/comment/13","createdAt":"2026-03-28T18:12:00Z","author":{"login":"reviewer-13"}},{"id":"comment-14","body":"Comment 14","url":"https://example.test/comment/14","createdAt":"2026-03-28T18:13:00Z","author":{"login":"reviewer-14"}},{"id":"comment-15","body":"Comment 15","url":"https://example.test/comment/15","createdAt":"2026-03-28T18:14:00Z","author":{"login":"reviewer-15"}},{"id":"comment-16","body":"Comment 16","url":"https://example.test/comment/16","createdAt":"2026-03-28T18:15:00Z","author":{"login":"reviewer-16"}},{"id":"comment-17","body":"Comment 17","url":"https://example.test/comment/17","createdAt":"2026-03-28T18:16:00Z","author":{"login":"reviewer-17"}},{"id":"comment-18","body":"Comment 18","url":"https://example.test/comment/18","createdAt":"2026-03-28T18:17:00Z","author":{"login":"reviewer-18"}},{"id":"comment-19","body":"Comment 19","url":"https://example.test/comment/19","createdAt":"2026-03-28T18:18:00Z","author":{"login":"reviewer-19"}},{"id":"comment-20","body":"Comment 20","url":"https://example.test/comment/20","createdAt":"2026-03-28T18:19:00Z","author":{"login":"reviewer-20"}}],"pageInfo":{"hasNextPage":true,"endCursor":"comments-page-2"}}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`)
	writeFile(t, os.Getenv("COLIN_FAKE_GH_REVIEW_THREAD_COMMENTS"), `{"data":{"node":{"comments":{"nodes":[{"author":{"login":"chatgpt-codex-connector[bot]"}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`)

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
}

func TestReviewContextPrefersCurrentWorkspaceBranchOverTrackerBranchName(t *testing.T) {
	workspacePath, _, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")
	t.Setenv("COLIN_FAKE_GH_HEAD", "colin-93")

	manager := NewManager(testConfig(), testLogger())
	issue := domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address review",
		BranchName: stringPtr("pmenglund/colin-93"),
	}
	if _, err := manager.Publish(context.Background(), issue, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	writeFile(t, os.Getenv("COLIN_FAKE_GH_REVIEW_THREADS"), `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread-1","isResolved":false,"isOutdated":false,"viewerCanReply":true,"viewerCanResolve":true,"path":"internal/foo.go","line":42,"startLine":40,"comments":{"nodes":[{"id":"comment-1","body":"Please fix this.","url":"https://example.test/comment/1","createdAt":"2026-03-28T18:00:00Z","author":{"login":"reviewer"}}]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`)

	reviewContext, err := manager.ReviewContext(context.Background(), issue, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if reviewContext.PullRequest.Number != 1 {
		t.Fatalf("pull request number = %d, want 1", reviewContext.PullRequest.Number)
	}
	if len(reviewContext.Threads) != 1 {
		t.Fatalf("threads length = %d, want 1", len(reviewContext.Threads))
	}
}

func TestReviewContextFallsBackToMetadataActualBranchNameWhenWorkspaceBranchUnavailable(t *testing.T) {
	workspacePath, _, _ := setupRepoAutomationTest(t)
	writeFile(t, filepath.Join(workspacePath, "feature.txt"), "hello\n")
	t.Setenv("COLIN_FAKE_GH_HEAD", "colin-93")

	manager := NewManager(testConfig(), testLogger())
	issue := domain.Issue{
		Identifier: "COLIN-93",
		Title:      "Address review",
		BranchName: stringPtr("pmenglund/colin-93"),
		ColinMetadata: &domain.ColinMetadata{
			ActualBranchName: "colin-93",
		},
	}
	if _, err := manager.Publish(context.Background(), issue, workspacePath); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	runCmd(t, workspacePath, "git", "checkout", "--detach")

	writeFile(t, os.Getenv("COLIN_FAKE_GH_REVIEW_THREADS"), `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread-1","isResolved":false,"isOutdated":false,"viewerCanReply":true,"viewerCanResolve":true,"path":"internal/foo.go","line":42,"startLine":40,"comments":{"nodes":[{"id":"comment-1","body":"Please fix this.","url":"https://example.test/comment/1","createdAt":"2026-03-28T18:00:00Z","author":{"login":"reviewer"}}]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`)

	reviewContext, err := manager.ReviewContext(context.Background(), issue, workspacePath)
	if err != nil {
		t.Fatalf("ReviewContext() error = %v", err)
	}
	if reviewContext.PullRequest.Number != 1 {
		t.Fatalf("pull request number = %d, want 1", reviewContext.PullRequest.Number)
	}
	if len(reviewContext.Threads) != 1 {
		t.Fatalf("threads length = %d, want 1", len(reviewContext.Threads))
	}
}

func TestReplyAndResolveReviewThreadRunsGraphQLMutations(t *testing.T) {
	workspacePath, _, ghLogPath := setupRepoAutomationTest(t)
	manager := NewManager(testConfig(), testLogger())

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

	log := readFile(t, ghLogPath)
	if !strings.Contains(log, "api graphql") || !strings.Contains(log, "ReplyReviewThread") || !strings.Contains(log, "ResolveReviewThread") {
		t.Fatalf("gh log = %q, want review-thread reply and resolve mutations", log)
	}
}

func setupRepoAutomationTest(t *testing.T) (workspacePath string, remotePath string, ghLogPath string) {
	t.Helper()

	tempDir := t.TempDir()
	remotePath = filepath.Join(tempDir, "remote.git")
	seedPath := filepath.Join(tempDir, "seed")
	workspacePath = filepath.Join(tempDir, "workspace")
	binPath := filepath.Join(tempDir, "bin")
	ghStatePath := filepath.Join(tempDir, "gh-state.json")
	ghReviewThreadsPath := filepath.Join(tempDir, "gh-review-threads.json")
	ghReviewThreadCommentsPath := filepath.Join(tempDir, "gh-review-thread-comments.json")
	ghReactionsPath := filepath.Join(tempDir, "gh-reactions.json")
	ghLogPath = filepath.Join(tempDir, "gh.log")

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

	if err := os.MkdirAll(binPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeFile(t, ghStatePath, "[]\n")
	writeFile(t, ghReviewThreadsPath, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`)
	writeFile(t, ghReviewThreadCommentsPath, `{"data":{"node":{"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`)
	writeFile(t, ghReactionsPath, `{"data":{"repository":{"pullRequest":{"reactions":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`)
	writeFile(t, filepath.Join(binPath, "gh"), fakeGHScript)
	if err := os.Chmod(filepath.Join(binPath, "gh"), 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	t.Setenv("PATH", binPath+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COLIN_FAKE_GH_STATE", ghStatePath)
	t.Setenv("COLIN_FAKE_GH_REVIEW_THREADS", ghReviewThreadsPath)
	t.Setenv("COLIN_FAKE_GH_REVIEW_THREAD_COMMENTS", ghReviewThreadCommentsPath)
	t.Setenv("COLIN_FAKE_GH_REACTIONS", ghReactionsPath)
	t.Setenv("COLIN_FAKE_GH_LOG", ghLogPath)

	return workspacePath, remotePath, ghLogPath
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
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}

func stringPtr(value string) *string {
	return &value
}

const fakeGHScript = `#!/bin/sh
set -eu
echo "$*" >>"$COLIN_FAKE_GH_LOG"
case "$1 $2" in
  "pr list")
    cat "$COLIN_FAKE_GH_STATE"
    ;;
  "pr create")
    printf '[{"number":1,"url":"https://example.test/pr/1","state":"OPEN"}]\n' >"$COLIN_FAKE_GH_STATE"
    printf 'https://example.test/pr/1\n'
    ;;
  "pr merge")
    printf '[{"number":1,"url":"https://example.test/pr/1","state":"MERGED"}]\n' >"$COLIN_FAKE_GH_STATE"
    ;;
  "api graphql")
    case "$*" in
      *"ReviewThreads"*)
        cat "$COLIN_FAKE_GH_REVIEW_THREADS"
        ;;
      *"ReviewThreadComments"*)
        cat "$COLIN_FAKE_GH_REVIEW_THREAD_COMMENTS"
        ;;
      *"PullRequestReactions"*)
        cat "$COLIN_FAKE_GH_REACTIONS"
        ;;
      *"ReplyReviewThread"*)
        printf '{"data":{"addPullRequestReviewThreadReply":{"comment":{"id":"reply-1","url":"https://example.test/comment/reply-1"}}}}\n'
        ;;
      *"ResolveReviewThread"*)
        printf '{"data":{"resolveReviewThread":{"thread":{"id":"thread-1","isResolved":true}}}}\n'
        ;;
      *)
        echo "unexpected graphql invocation: $*" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    echo "unexpected gh invocation: $*" >&2
    exit 1
    ;;
esac
`
