package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

func TestGitPullRequestManagerEnsurePullRequestCreatesNewPullRequest(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := initTestGitRemote(t)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	createTaskBranchWithCommit(t, repoRoot, "colin/COLIN-90", "feature.txt", "feature change\n")

	manager := NewGitPullRequestManager(GitPullRequestManagerOptions{
		RepoRoot:   repoRoot,
		BaseBranch: "main",
		RemoteName: "origin",
		Binary:     "gh",
	})

	var createCalled bool
	listCalls := 0
	manager.runCommand = func(_ context.Context, _ string, binary string, args []string) (commandOutput, error) {
		if binary != "gh" {
			t.Fatalf("binary = %q, want %q", binary, "gh")
		}
		if len(args) < 2 || args[0] != "pr" {
			t.Fatalf("unexpected gh args: %v", args)
		}
		switch args[1] {
		case "list":
			listCalls++
			if listCalls == 1 {
				return commandOutput{Stdout: "[]"}, nil
			}
			return commandOutput{Stdout: `[{"url":"https://github.com/pmenglund/colin/pull/90"}]`}, nil
		case "create":
			createCalled = true
			return commandOutput{Stdout: "https://github.com/pmenglund/colin/pull/90"}, nil
		default:
			t.Fatalf("unexpected gh command: %v", args)
			return commandOutput{}, nil
		}
	}

	url, err := manager.EnsurePullRequest(context.Background(), linear.Issue{
		Identifier: "COLIN-90",
		Title:      "Auto PR test",
		Metadata: map[string]string{
			workflow.MetaBranchName: "colin/COLIN-90",
		},
	})
	if err != nil {
		t.Fatalf("EnsurePullRequest() error = %v", err)
	}
	if url != "https://github.com/pmenglund/colin/pull/90" {
		t.Fatalf("EnsurePullRequest() url = %q", url)
	}
	if !createCalled {
		t.Fatal("expected gh pr create to be called")
	}
	if listCalls != 2 {
		t.Fatalf("gh pr list call count = %d, want 2", listCalls)
	}
	if got := runGit(t, "--git-dir", remotePath, "rev-parse", "refs/heads/colin/COLIN-90"); strings.TrimSpace(got) == "" {
		t.Fatalf("remote branch ref is empty")
	}
}

func TestGitPullRequestManagerEnsurePullRequestUsesExistingPullRequest(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := initTestGitRemote(t)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	createTaskBranchWithCommit(t, repoRoot, "colin/COLIN-91", "feature.txt", "feature change\n")

	manager := NewGitPullRequestManager(GitPullRequestManagerOptions{
		RepoRoot:   repoRoot,
		BaseBranch: "main",
		RemoteName: "origin",
		Binary:     "gh",
	})

	createCalled := 0
	manager.runCommand = func(_ context.Context, _ string, _ string, args []string) (commandOutput, error) {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			return commandOutput{Stdout: `[{"url":"https://github.com/pmenglund/colin/pull/91"}]`}, nil
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
			createCalled++
			return commandOutput{}, nil
		}
		return commandOutput{}, nil
	}

	url, err := manager.EnsurePullRequest(context.Background(), linear.Issue{
		Identifier: "COLIN-91",
		Title:      "Existing PR test",
		Metadata: map[string]string{
			workflow.MetaBranchName: "colin/COLIN-91",
		},
	})
	if err != nil {
		t.Fatalf("EnsurePullRequest() error = %v", err)
	}
	if url != "https://github.com/pmenglund/colin/pull/91" {
		t.Fatalf("EnsurePullRequest() url = %q", url)
	}
	if createCalled != 0 {
		t.Fatalf("gh pr create call count = %d, want 0", createCalled)
	}
}

func TestGitPullRequestManagerEnsurePullRequestAcceptsStoredPullRequestURL(t *testing.T) {
	manager := NewGitPullRequestManager(GitPullRequestManagerOptions{
		RepoRoot:   "/tmp/repo",
		BaseBranch: "main",
		RemoteName: "origin",
		Binary:     "gh",
	})

	callCnt := 0
	manager.runCommand = func(_ context.Context, _ string, _ string, _ []string) (commandOutput, error) {
		callCnt++
		return commandOutput{}, errors.New("unexpected command call")
	}

	url, err := manager.EnsurePullRequest(context.Background(), linear.Issue{
		Identifier: "COLIN-92",
		Metadata: map[string]string{
			workflow.MetaPRURL: "https://github.com/pmenglund/colin/pull/92",
		},
	})
	if err != nil {
		t.Fatalf("EnsurePullRequest() error = %v", err)
	}
	if url != "https://github.com/pmenglund/colin/pull/92" {
		t.Fatalf("EnsurePullRequest() url = %q", url)
	}
	if callCnt != 0 {
		t.Fatalf("runCommand call count = %d, want 0", callCnt)
	}
}

func initTestGitRemote(t *testing.T) string {
	t.Helper()

	remotePath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", remotePath)
	return remotePath
}

func createTaskBranchWithCommit(t *testing.T, repoRoot string, branchName string, fileName string, content string) {
	t.Helper()

	runGit(t, "-C", repoRoot, "checkout", "-b", branchName)
	runGit(t, "-C", repoRoot, "config", "user.email", "colin-tests@example.com")
	runGit(t, "-C", repoRoot, "config", "user.name", "Colin Tests")
	runGit(t, "-C", repoRoot, "add", ".")
	writeFilePath := filepath.Join(repoRoot, fileName)
	if err := os.WriteFile(writeFilePath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", writeFilePath, err)
	}
	runGit(t, "-C", repoRoot, "add", fileName)
	runGit(t, "-C", repoRoot, "commit", "-m", "task commit")
	runGit(t, "-C", repoRoot, "checkout", "main")
}
