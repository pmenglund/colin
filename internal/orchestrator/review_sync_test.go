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
			name: "multiple attached prs are ambiguous",
			issue: domain.Issue{
				State:       "Todo",
				ReviewCycle: reviewCycle,
				BranchName:  &branch,
				AttachedPullRequests: []domain.PullRequestRef{
					{Number: 11, URL: "https://github.com/pmenglund/colin/pull/11"},
					{Number: 12, URL: "https://github.com/pmenglund/colin/pull/12"},
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

func TestPrepareReviewIssueDoesNotWaitWhenReviewContextHasNoPullRequest(t *testing.T) {
	cfg := setupReviewSyncTestRuntime(t, "[]\n", emptyReviewThreadsJSON, emptyReactionsJSON)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManager(cfg, logger),
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

func TestPrepareReviewIssueWaitsWhenTrackedPullRequestExistsButThreadsHaveNotSynced(t *testing.T) {
	cfg := setupReviewSyncTestRuntime(t, `[{"number":11,"url":"https://example.test/pr/11","state":"OPEN","headRefName":"colin-123","baseRefName":"symphony"}]`+"\n", emptyReviewThreadsJSON, emptyReactionsJSON)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManager(cfg, logger),
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

	if ready {
		t.Fatal("prepareReviewIssue() ready = true, want false")
	}
	if prepared.PullRequest == nil || prepared.PullRequest.Number != 11 {
		t.Fatalf("PullRequest = %#v, want tracked PR #11", prepared.PullRequest)
	}
	if got := len(tracker.issueComments); got != 1 {
		t.Fatalf("issueComments length = %d, want 1", got)
	}
	if !strings.Contains(tracker.issueComments[0], "Waiting for GitHub review feedback to sync before starting work.") {
		t.Fatalf("issue comment = %q, want waiting message", tracker.issueComments[0])
	}
	if !strings.Contains(tracker.issueComments[0], "PR: `#11`") {
		t.Fatalf("issue comment = %q, want PR reference", tracker.issueComments[0])
	}
	state, ok := orch.reviewSync[issue.ID]
	if !ok {
		t.Fatal("reviewSync state missing")
	}
	if state.comment == nil || state.comment.RootCommentID == "" {
		t.Fatalf("comment state = %#v, want persisted root comment id", state.comment)
	}
}

func setupReviewSyncTestRuntime(t *testing.T, ghState string, ghReviewThreads string, ghReactions string) domain.ServiceConfig {
	t.Helper()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "origin.git")
	seedPath := filepath.Join(tempDir, "seed")
	binPath := filepath.Join(tempDir, "bin")
	ghStatePath := filepath.Join(tempDir, "gh-state.json")
	ghReviewThreadsPath := filepath.Join(tempDir, "gh-review-threads.json")
	ghReactionsPath := filepath.Join(tempDir, "gh-reactions.json")

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

	if err := os.MkdirAll(binPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	reviewSyncWriteFile(t, ghStatePath, ghState)
	reviewSyncWriteFile(t, ghReviewThreadsPath, ghReviewThreads)
	reviewSyncWriteFile(t, ghReactionsPath, ghReactions)
	reviewSyncWriteFile(t, filepath.Join(binPath, "gh"), fakeReviewSyncGHScript)
	if err := os.Chmod(filepath.Join(binPath, "gh"), 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	t.Setenv("PATH", binPath+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COLIN_REVIEW_SYNC_GH_STATE", ghStatePath)
	t.Setenv("COLIN_REVIEW_SYNC_GH_REVIEW_THREADS", ghReviewThreadsPath)
	t.Setenv("COLIN_REVIEW_SYNC_GH_REACTIONS", ghReactionsPath)

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
	}
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

const emptyReviewThreadsJSON = `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`
const emptyReactionsJSON = `{"data":{"repository":{"pullRequest":{"reactions":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`

const fakeReviewSyncGHScript = `#!/bin/sh
set -eu
case "$1 $2" in
  "pr list")
    head=""
    base=""
    shift 2
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --head)
          head="$2"
          shift 2
          ;;
        --base)
          base="$2"
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    python3 - "$COLIN_REVIEW_SYNC_GH_STATE" "$head" "$base" <<'PY'
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as fh:
    prs = json.load(fh)
head = sys.argv[2]
base = sys.argv[3]
if head:
    prs = [pr for pr in prs if pr.get("headRefName") == head]
if base:
    prs = [pr for pr in prs if pr.get("baseRefName") == base]
json.dump(prs, sys.stdout)
print()
PY
    ;;
  "pr view")
    python3 - "$COLIN_REVIEW_SYNC_GH_STATE" "$3" <<'PY'
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as fh:
    prs = json.load(fh)
target = int(sys.argv[2])
for pr in prs:
    if int(pr.get("number", 0)) == target:
        json.dump(pr, sys.stdout)
        print()
        raise SystemExit(0)
raise SystemExit(1)
PY
    ;;
  "api graphql")
    case "$*" in
      *"ReviewThreads"*)
        cat "$COLIN_REVIEW_SYNC_GH_REVIEW_THREADS"
        ;;
      *"PullRequestReactions"*)
        cat "$COLIN_REVIEW_SYNC_GH_REACTIONS"
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
