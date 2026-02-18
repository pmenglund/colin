package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

func TestGitMergeExecutorExecuteMergeHappyPathAndIdempotent(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", remotePath)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	runGit(t, "-C", repoRoot, "push", "-u", "origin", "main")

	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-6")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "merge-target.txt")
	if err := os.WriteFile(changePath, []byte("merged content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "merge-target.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "task change")

	issue := linear.Issue{
		Identifier: "COLIN-6",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	}
	executor := NewGitMergeExecutor(GitMergeExecutorOptions{RepoRoot: repoRoot})

	if err := executor.ExecuteMerge(context.Background(), issue); err != nil {
		t.Fatalf("ExecuteMerge() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(repoRoot, "merge-target.txt")); err != nil {
		t.Fatalf("expected merged file in base repo: %v", err)
	}
	if branchExistsInRepo(t, repoRoot, workspace.BranchName) {
		t.Fatalf("branch %q should be deleted after merge", workspace.BranchName)
	}
	if _, err := os.Stat(workspace.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree path should be removed, stat error = %v", err)
	}

	localMain := runGit(t, "-C", repoRoot, "rev-parse", "main")
	remoteMain := runGit(t, "--git-dir", remotePath, "rev-parse", "refs/heads/main")
	if localMain != remoteMain {
		t.Fatalf("remote main %q != local main %q", remoteMain, localMain)
	}

	// Re-running after cleanup should be safe.
	if err := executor.ExecuteMerge(context.Background(), issue); err != nil {
		t.Fatalf("ExecuteMerge() second run error = %v", err)
	}
}

func TestGitMergeExecutorExecuteMergePushFailureLeavesRecoverableState(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-6")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "retry-target.txt")
	if err := os.WriteFile(changePath, []byte("retry content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "retry-target.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "task change for retry")

	issue := linear.Issue{
		Identifier: "COLIN-6",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	}
	executor := NewGitMergeExecutor(GitMergeExecutorOptions{RepoRoot: repoRoot})

	err = executor.ExecuteMerge(context.Background(), issue)
	if err == nil {
		t.Fatal("ExecuteMerge() error = nil, want push/remote validation failure")
	}
	if !strings.Contains(err.Error(), `verify remote "origin"`) {
		t.Fatalf("error = %q, want remote verification context", err.Error())
	}

	// Merge ran before push failure, so cleanup must remain retryable.
	if !branchExistsInRepo(t, repoRoot, workspace.BranchName) {
		t.Fatalf("branch %q should still exist after push failure for retry", workspace.BranchName)
	}
	if _, statErr := os.Stat(workspace.WorktreePath); statErr != nil {
		t.Fatalf("worktree path should still exist after push failure: %v", statErr)
	}
}

func branchExistsInRepo(t *testing.T, repoRoot string, branchName string) bool {
	t.Helper()

	cmd := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git show-ref failed: %v (%s)", err, strings.TrimSpace(string(output)))
	return false
}
