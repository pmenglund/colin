package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitTaskBootstrapperEnsureTaskWorkspaceCreatesAndReusesWorkspace(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	colinHome := filepath.Join(t.TempDir(), "colin-home")
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: colinHome,
	})

	result1, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-3")
	if err != nil {
		t.Fatalf("first EnsureTaskWorkspace() error = %v", err)
	}

	wantWorktreePath := filepath.Join(colinHome, worktreesDirName, "COLIN-3")
	if result1.WorktreePath != wantWorktreePath {
		t.Fatalf("WorktreePath = %q, want %q", result1.WorktreePath, wantWorktreePath)
	}
	if result1.BranchName != "colin/COLIN-3" {
		t.Fatalf("BranchName = %q, want %q", result1.BranchName, "colin/COLIN-3")
	}

	currentBranch := runGit(t, "-C", result1.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if currentBranch != result1.BranchName {
		t.Fatalf("worktree current branch = %q, want %q", currentBranch, result1.BranchName)
	}

	result2, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-3")
	if err != nil {
		t.Fatalf("second EnsureTaskWorkspace() error = %v", err)
	}
	if result2 != result1 {
		t.Fatalf("result2 = %#v, want %#v", result2, result1)
	}

	worktreeList := runGit(t, "-C", repoRoot, "worktree", "list", "--porcelain")
	wantWorktreePathCanonical := mustEvalPath(t, wantWorktreePath)
	got := strings.Count(worktreeList, "worktree "+wantWorktreePath)
	if wantWorktreePathCanonical != wantWorktreePath {
		got += strings.Count(worktreeList, "worktree "+wantWorktreePathCanonical)
	}
	if got != 1 {
		t.Fatalf("worktree occurrences = %d, want 1; list:\n%s", got, worktreeList)
	}
}

func TestGitTaskBootstrapperEnsureTaskWorkspaceUsesConfiguredBaseBranch(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	runGit(t, "-C", repoRoot, "checkout", "-b", "master")
	runGit(t, "-C", repoRoot, "branch", "-D", "main")

	colinHome := filepath.Join(t.TempDir(), "colin-home")
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:   repoRoot,
		ColinHome:  colinHome,
		BaseBranch: "master",
	})

	result, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-4")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	masterHead := runGit(t, "-C", repoRoot, "rev-parse", "master")
	branchHead := runGit(t, "-C", result.WorktreePath, "rev-parse", result.BranchName)
	if branchHead != masterHead {
		t.Fatalf("issue branch head = %q, want %q from base branch master", branchHead, masterHead)
	}
}

func TestGitTaskBootstrapperEnsureTaskWorkspaceReturnsActionableError(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	colinHome := filepath.Join(t.TempDir(), "colin-home")
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:   repoRoot,
		ColinHome:  colinHome,
		BaseBranch: "missing-branch",
	})

	_, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-3")
	if err == nil {
		t.Fatal("EnsureTaskWorkspace() error = nil, want error")
	}
	if !strings.Contains(err.Error(), `verify base branch "missing-branch"`) {
		t.Fatalf("error = %q, want base-branch context", err.Error())
	}
}

func TestGitTaskBootstrapperRecordBranchSessionPersistsMetadata(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	colinHome := filepath.Join(t.TempDir(), "colin-home")
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: colinHome,
	})

	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-3")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	if err := bootstrapper.RecordBranchSession(context.Background(), workspace.WorktreePath, workspace.BranchName, "thr_123"); err != nil {
		t.Fatalf("RecordBranchSession() error = %v", err)
	}

	got := runGit(t, "-C", repoRoot, "config", "--get", "branch."+workspace.BranchName+".colinSessionId")
	if got != "thr_123" {
		t.Fatalf("branch session metadata = %q, want %q", got, "thr_123")
	}
}

func initTestGitRepo(t *testing.T) string {
	t.Helper()

	repoRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	runGit(t, "init", repoRoot)
	runGit(t, "-C", repoRoot, "config", "user.email", "colin-tests@example.com")
	runGit(t, "-C", repoRoot, "config", "user.name", "Colin Tests")

	readmePath := filepath.Join(repoRoot, "README.md")
	if err := os.WriteFile(readmePath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	runGit(t, "-C", repoRoot, "add", "README.md")
	runGit(t, "-C", repoRoot, "commit", "-m", "seed")
	runGit(t, "-C", repoRoot, "branch", "-M", "main")

	return repoRoot
}

func runGit(t *testing.T, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func mustEvalPath(t *testing.T, p string) string {
	t.Helper()

	evaluated, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return evaluated
}
