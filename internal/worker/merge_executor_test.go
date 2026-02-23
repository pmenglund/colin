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

func TestGitMergeExecutorExecuteMergeHappyPathAndStrictRetryFailure(t *testing.T) {
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

	// Re-running after cleanup should fail fast because branch/worktree no longer exist.
	err = executor.ExecuteMerge(context.Background(), issue)
	if err == nil {
		t.Fatal("ExecuteMerge() second run error = nil, want missing source branch error")
	}
	if !strings.Contains(err.Error(), "source branch") {
		t.Fatalf("second run error = %q, want source branch context", err.Error())
	}
}

func TestGitMergeExecutorExecuteMergeSupportsTwoPhaseLifecycle(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", remotePath)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	runGit(t, "-C", repoRoot, "push", "-u", "origin", "main")

	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-600")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "two-phase-target.txt")
	if err := os.WriteFile(changePath, []byte("merged content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "two-phase-target.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "task change")

	executor := NewGitMergeExecutor(GitMergeExecutorOptions{RepoRoot: repoRoot})
	issue := linear.Issue{
		Identifier: "COLIN-600",
		StateName:  workflow.StateMerge,
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	}

	if err := executor.ExecuteMerge(context.Background(), issue); err != nil {
		t.Fatalf("ExecuteMerge(merge phase) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "two-phase-target.txt")); err != nil {
		t.Fatalf("expected merged file in base repo after merge phase: %v", err)
	}
	if !branchExistsInRepo(t, repoRoot, workspace.BranchName) {
		t.Fatalf("branch %q should still exist after merge phase", workspace.BranchName)
	}
	if _, err := os.Stat(workspace.WorktreePath); err != nil {
		t.Fatalf("worktree should still exist after merge phase: %v", err)
	}

	issue.StateName = workflow.StateMerged
	if err := executor.ExecuteMerge(context.Background(), issue); err != nil {
		t.Fatalf("ExecuteMerge(cleanup phase) error = %v", err)
	}
	if branchExistsInRepo(t, repoRoot, workspace.BranchName) {
		t.Fatalf("branch %q should be deleted after cleanup phase", workspace.BranchName)
	}
	if _, err := os.Stat(workspace.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree should be removed after cleanup phase, stat err=%v", err)
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

func TestGitMergeExecutorExecuteMergeUsesConfiguredBaseBranch(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	runGit(t, "-C", repoRoot, "checkout", "-b", "master")
	runGit(t, "-C", repoRoot, "branch", "-D", "main")

	remotePath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", remotePath)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	runGit(t, "-C", repoRoot, "push", "-u", "origin", "master")

	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:   repoRoot,
		ColinHome:  filepath.Join(t.TempDir(), "colin-home"),
		BaseBranch: "master",
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-76")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "merge-master-target.txt")
	if err := os.WriteFile(changePath, []byte("merged into master\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "merge-master-target.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "task change")

	preparer := &fakeMergePreparer{}
	executor := NewGitMergeExecutor(GitMergeExecutorOptions{
		RepoRoot:      repoRoot,
		BaseBranch:    "master",
		MergePreparer: preparer,
	})
	issue := linear.Issue{
		Identifier: "COLIN-76",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	}

	if err := executor.ExecuteMerge(context.Background(), issue); err != nil {
		t.Fatalf("ExecuteMerge() error = %v", err)
	}
	if preparer.lastBase != "master" {
		t.Fatalf("PrepareMerge() base branch = %q, want %q", preparer.lastBase, "master")
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "merge-master-target.txt")); err != nil {
		t.Fatalf("expected merged file in base repo: %v", err)
	}
	localMaster := runGit(t, "-C", repoRoot, "rev-parse", "master")
	remoteMaster := runGit(t, "--git-dir", remotePath, "rev-parse", "refs/heads/master")
	if localMaster != remoteMaster {
		t.Fatalf("remote master %q != local master %q", remoteMaster, localMaster)
	}
}

func TestGitMergeExecutorExecuteMergeSkipsPushWhenRemoteMissing(t *testing.T) {
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
	if err := os.WriteFile(changePath, []byte("no-remote content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "retry-target.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "task change without remote")

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

	if _, err := os.Stat(filepath.Join(repoRoot, "retry-target.txt")); err != nil {
		t.Fatalf("expected merged file in base repo: %v", err)
	}
	if branchExistsInRepo(t, repoRoot, workspace.BranchName) {
		t.Fatalf("branch %q should be deleted after merge", workspace.BranchName)
	}
	if _, statErr := os.Stat(workspace.WorktreePath); !os.IsNotExist(statErr) {
		t.Fatalf("worktree path should be removed, stat error = %v", statErr)
	}
}

func TestGitMergeExecutorExecuteMergeSkipsPushWhenConfiguredOff(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", remotePath)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	runGit(t, "-C", repoRoot, "push", "-u", "origin", "main")

	beforeRemoteMain := runGit(t, "--git-dir", remotePath, "rev-parse", "refs/heads/main")

	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-61")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "skip-push-target.txt")
	if err := os.WriteFile(changePath, []byte("skip push\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "skip-push-target.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "task change without push")

	pushBaseBranch := false
	executor := NewGitMergeExecutor(GitMergeExecutorOptions{
		RepoRoot:       repoRoot,
		PushBaseBranch: &pushBaseBranch,
	})
	issue := linear.Issue{
		Identifier: "COLIN-61",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	}

	if err := executor.ExecuteMerge(context.Background(), issue); err != nil {
		t.Fatalf("ExecuteMerge() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(repoRoot, "skip-push-target.txt")); err != nil {
		t.Fatalf("expected merged file in base repo: %v", err)
	}
	if branchExistsInRepo(t, repoRoot, workspace.BranchName) {
		t.Fatalf("branch %q should be deleted after merge", workspace.BranchName)
	}
	if _, statErr := os.Stat(workspace.WorktreePath); !os.IsNotExist(statErr) {
		t.Fatalf("worktree path should be removed, stat error = %v", statErr)
	}

	afterRemoteMain := runGit(t, "--git-dir", remotePath, "rev-parse", "refs/heads/main")
	if beforeRemoteMain != afterRemoteMain {
		t.Fatalf("remote main changed from %q to %q despite push being disabled", beforeRemoteMain, afterRemoteMain)
	}
}

func TestGitMergeExecutorExecuteMergePushFailureLeavesRecoverableState(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	missingRemotePath := filepath.Join(t.TempDir(), "origin-missing.git")
	runGit(t, "-C", repoRoot, "remote", "add", "origin", missingRemotePath)

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
		t.Fatal("ExecuteMerge() error = nil, want push failure")
	}
	if !strings.Contains(err.Error(), `push "main" to remote "origin"`) {
		t.Fatalf("error = %q, want push failure context", err.Error())
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

func TestGitMergeExecutorExecuteMergeFailsWhenSourceBranchMissing(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-10")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	runGit(t, "-C", workspace.WorktreePath, "checkout", "--detach")
	runGit(t, "-C", repoRoot, "branch", "-D", workspace.BranchName)

	executor := NewGitMergeExecutor(GitMergeExecutorOptions{RepoRoot: repoRoot})
	issue := linear.Issue{
		Identifier: "COLIN-10",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	}

	err = executor.ExecuteMerge(context.Background(), issue)
	if err == nil {
		t.Fatal("ExecuteMerge() error = nil, want missing source branch error")
	}
	if !strings.Contains(err.Error(), "source branch") {
		t.Fatalf("error = %q, want missing source branch context", err.Error())
	}
}

func TestGitMergeExecutorNeedsMergeRecoveryWhenBranchUnmerged(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-11")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "needs-recovery-unmerged.txt")
	if err := os.WriteFile(changePath, []byte("unmerged branch\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "needs-recovery-unmerged.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "unmerged change")

	executor := NewGitMergeExecutor(GitMergeExecutorOptions{RepoRoot: repoRoot})
	needsRecovery, target, reason, err := executor.NeedsMergeRecovery(context.Background(), linear.Issue{
		Identifier: "COLIN-11",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	})
	if err != nil {
		t.Fatalf("NeedsMergeRecovery() error = %v", err)
	}
	if !needsRecovery {
		t.Fatal("NeedsMergeRecovery() = false, want true")
	}
	if target != MergeRecoveryTargetMerge {
		t.Fatalf("NeedsMergeRecovery() target = %q, want %q", target, MergeRecoveryTargetMerge)
	}
	if !strings.Contains(reason, "not merged") {
		t.Fatalf("NeedsMergeRecovery() reason = %q, want not merged context", reason)
	}
}

func TestGitMergeExecutorNeedsMergeRecoveryWhenBranchMergedButNotCleaned(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-12")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	changePath := filepath.Join(workspace.WorktreePath, "needs-recovery-cleanup.txt")
	if err := os.WriteFile(changePath, []byte("merged but not cleaned\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}
	runGit(t, "-C", workspace.WorktreePath, "add", "needs-recovery-cleanup.txt")
	runGit(t, "-C", workspace.WorktreePath, "commit", "-m", "cleanup pending")
	runGit(t, "-C", repoRoot, "merge", "--no-ff", "--no-edit", workspace.BranchName)

	executor := NewGitMergeExecutor(GitMergeExecutorOptions{RepoRoot: repoRoot})
	needsRecovery, target, reason, err := executor.NeedsMergeRecovery(context.Background(), linear.Issue{
		Identifier: "COLIN-12",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	})
	if err != nil {
		t.Fatalf("NeedsMergeRecovery() error = %v", err)
	}
	if !needsRecovery {
		t.Fatal("NeedsMergeRecovery() = false, want true")
	}
	if target != MergeRecoveryTargetMerged {
		t.Fatalf("NeedsMergeRecovery() target = %q, want %q", target, MergeRecoveryTargetMerged)
	}
	if !strings.Contains(reason, "cleanup is incomplete") {
		t.Fatalf("NeedsMergeRecovery() reason = %q, want cleanup context", reason)
	}
}

func TestGitMergeExecutorNeedsMergeRecoveryFalseWhenBranchMissing(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	bootstrapper := NewGitTaskBootstrapper(GitTaskBootstrapperOptions{
		RepoRoot:  repoRoot,
		ColinHome: filepath.Join(t.TempDir(), "colin-home"),
	})
	workspace, err := bootstrapper.EnsureTaskWorkspace(context.Background(), "COLIN-13")
	if err != nil {
		t.Fatalf("EnsureTaskWorkspace() error = %v", err)
	}

	runGit(t, "-C", workspace.WorktreePath, "checkout", "--detach")
	runGit(t, "-C", repoRoot, "branch", "-D", workspace.BranchName)

	executor := NewGitMergeExecutor(GitMergeExecutorOptions{RepoRoot: repoRoot})
	needsRecovery, target, reason, err := executor.NeedsMergeRecovery(context.Background(), linear.Issue{
		Identifier: "COLIN-13",
		Metadata: map[string]string{
			workflow.MetaBranchName:   workspace.BranchName,
			workflow.MetaWorktreePath: workspace.WorktreePath,
		},
	})
	if err != nil {
		t.Fatalf("NeedsMergeRecovery() error = %v", err)
	}
	if needsRecovery {
		t.Fatal("NeedsMergeRecovery() = true, want false")
	}
	if target != "" {
		t.Fatalf("NeedsMergeRecovery() target = %q, want empty", target)
	}
	if strings.TrimSpace(reason) != "" {
		t.Fatalf("NeedsMergeRecovery() reason = %q, want empty", reason)
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
