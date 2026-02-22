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

type fakeMergePreparer struct {
	callCnt      int
	lastIssueID  string
	lastBranch   string
	lastWorktree string
	lastBase     string
	lastRemote   string
	err          error
}

func (f *fakeMergePreparer) PrepareMerge(
	_ context.Context,
	issue linear.Issue,
	branchName string,
	worktreePath string,
	baseBranch string,
	remoteName string,
) error {
	f.callCnt++
	f.lastIssueID = issue.Identifier
	f.lastBranch = branchName
	f.lastWorktree = worktreePath
	f.lastBase = baseBranch
	f.lastRemote = remoteName
	return f.err
}

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

func TestGitMergeExecutorExecuteMergeInvokesMergePreparer(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", remotePath)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	runGit(t, "-C", repoRoot, "push", "-u", "origin", "main")

	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-7")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "merge-prep-target.txt")
	if err := os.WriteFile(changePath, []byte("merged content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "merge-prep-target.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "task change")

	preparer := &fakeMergePreparer{}
	executor := NewGitMergeExecutor(GitMergeExecutorOptions{
		RepoRoot:      repoRoot,
		MergePreparer: preparer,
	})
	issue := linear.Issue{
		Identifier: "COLIN-7",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	}

	if err := executor.ExecuteMerge(context.Background(), issue); err != nil {
		t.Fatalf("ExecuteMerge() error = %v", err)
	}

	if preparer.callCnt != 1 {
		t.Fatalf("PrepareMerge() call count = %d, want 1", preparer.callCnt)
	}
	if preparer.lastIssueID != "COLIN-7" {
		t.Fatalf("PrepareMerge() issue identifier = %q, want %q", preparer.lastIssueID, "COLIN-7")
	}
	if preparer.lastBranch != workspace.BranchName {
		t.Fatalf("PrepareMerge() branch = %q, want %q", preparer.lastBranch, workspace.BranchName)
	}
	if preparer.lastWorktree != workspace.WorktreePath {
		t.Fatalf("PrepareMerge() worktree = %q, want %q", preparer.lastWorktree, workspace.WorktreePath)
	}
	if preparer.lastBase != "main" {
		t.Fatalf("PrepareMerge() base branch = %q, want %q", preparer.lastBase, "main")
	}
	if preparer.lastRemote != "origin" {
		t.Fatalf("PrepareMerge() remote = %q, want %q", preparer.lastRemote, "origin")
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

func TestGitMergeExecutorExecuteMergePreparationFailureStopsMerge(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", remotePath)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	runGit(t, "-C", repoRoot, "push", "-u", "origin", "main")

	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-8")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "merge-prep-fail-target.txt")
	if err := os.WriteFile(changePath, []byte("not merged\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "merge-prep-fail-target.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "task change")

	preparer := &fakeMergePreparer{err: errors.New("prep failed")}
	executor := NewGitMergeExecutor(GitMergeExecutorOptions{
		RepoRoot:      repoRoot,
		MergePreparer: preparer,
	})
	issue := linear.Issue{
		Identifier: "COLIN-8",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	}

	err = executor.ExecuteMerge(context.Background(), issue)
	if err == nil {
		t.Fatal("ExecuteMerge() error = nil, want preparation failure")
	}
	if !strings.Contains(err.Error(), "prepare merge") {
		t.Fatalf("error = %q, want preparation context", err.Error())
	}

	if _, statErr := os.Stat(filepath.Join(repoRoot, "merge-prep-fail-target.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected merge target to remain unmerged in base repo, stat error = %v", statErr)
	}
	if !branchExistsInRepo(t, repoRoot, workspace.BranchName) {
		t.Fatalf("branch %q should still exist after preparation failure", workspace.BranchName)
	}
	if _, statErr := os.Stat(workspace.WorktreePath); statErr != nil {
		t.Fatalf("worktree path should still exist after preparation failure: %v", statErr)
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
