//go:build livee2e

package e2e_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const liveWorktreesDir = "worktrees"

type liveGitSandbox struct {
	RepoRoot  string
	RemoteURL string
}

type liveMergeFixture struct {
	IssueIdentifier string
	BranchName      string
	WorktreePath    string
	FileName        string
	FilePath        string
	CommitSHA       string
}

func prepareLiveGitSandbox(t *testing.T) *liveGitSandbox {
	t.Helper()

	originPath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "", "init", "--bare", originPath)

	repoRoot := filepath.Join(t.TempDir(), "sandbox-repo")
	runGit(t, "", "clone", originPath, repoRoot)
	runGit(t, repoRoot, "config", "user.email", "colin-live-e2e@example.com")
	runGit(t, repoRoot, "config", "user.name", "Colin Live E2E")

	seedFile := filepath.Join(repoRoot, "README.md")
	if err := os.WriteFile(seedFile, []byte("live e2e seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", seedFile, err)
	}
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "seed")
	runGit(t, repoRoot, "branch", "-M", "main")
	runGit(t, repoRoot, "push", "-u", "origin", "main")

	status := runGit(t, repoRoot, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("sandbox clone is dirty before test run:\n%s", status)
	}

	return &liveGitSandbox{RepoRoot: repoRoot, RemoteURL: originPath}
}

func (s *liveGitSandbox) createMergeFixture(t *testing.T, issueIdentifier string, colinHome string) liveMergeFixture {
	t.Helper()

	identifier := strings.TrimSpace(issueIdentifier)
	if identifier == "" {
		t.Fatal("issue identifier is required for merge fixture")
	}

	branchName := "colin/" + identifier
	worktreePath := filepath.Join(strings.TrimSpace(colinHome), liveWorktreesDir, identifier)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(worktreePath), err)
	}

	runGit(t, s.RepoRoot, "worktree", "add", "--detach", worktreePath, "main")
	runGit(t, worktreePath, "checkout", "-b", branchName, "main")

	fileName := fmt.Sprintf("live-merge-%s.txt", strings.ToLower(strings.ReplaceAll(identifier, "/", "-")))
	filePath := filepath.Join(worktreePath, fileName)
	content := fmt.Sprintf("live merge fixture for %s\n", identifier)
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", filePath, err)
	}

	runGit(t, worktreePath, "add", fileName)
	runGit(t, worktreePath, "commit", "-m", fmt.Sprintf("live merge fixture %s", identifier))
	commitSHA := runGit(t, worktreePath, "rev-parse", "HEAD")

	return liveMergeFixture{
		IssueIdentifier: identifier,
		BranchName:      branchName,
		WorktreePath:    worktreePath,
		FileName:        fileName,
		FilePath:        filepath.Join(s.RepoRoot, fileName),
		CommitSHA:       commitSHA,
	}
}

func (s *liveGitSandbox) branchExists(t *testing.T, branchName string) bool {
	t.Helper()

	cmd := exec.Command("git", "-C", s.RepoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+strings.TrimSpace(branchName))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false
		}
		t.Fatalf("git show-ref failed: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return true
}

func (s *liveGitSandbox) localMainRevision(t *testing.T) string {
	t.Helper()
	return runGit(t, s.RepoRoot, "rev-parse", "main")
}

func (s *liveGitSandbox) remoteMainRevision(t *testing.T) string {
	t.Helper()
	line := runGit(t, s.RepoRoot, "ls-remote", "--heads", "origin", "main")
	parts := strings.Fields(line)
	if len(parts) == 0 {
		t.Fatalf("ls-remote returned no output for origin main")
	}
	return parts[0]
}

func runGit(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()

	gitArgs := make([]string, 0, len(args)+2)
	if strings.TrimSpace(repoRoot) != "" {
		gitArgs = append(gitArgs, "-C", repoRoot)
	}
	gitArgs = append(gitArgs, args...)

	cmd := exec.Command("git", gitArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(gitArgs, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}
