package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultTaskBaseBranch = "main"
	defaultGitBinary      = "git"
	worktreesDirName      = "worktrees"
	issueBranchPrefix     = "colin/"
)

// TaskBootstrapResult captures the workspace coordinates for an issue.
type TaskBootstrapResult struct {
	WorktreePath string
	BranchName   string
}

// TaskBootstrapper prepares per-issue git workspaces before execution starts.
type TaskBootstrapper interface {
	EnsureTaskWorkspace(ctx context.Context, issueIdentifier string) (TaskBootstrapResult, error)
}

// GitTaskBootstrapperOptions configures a git-backed TaskBootstrapper.
type GitTaskBootstrapperOptions struct {
	RepoRoot   string
	ColinHome  string
	BaseBranch string
	GitBinary  string
}

// GitTaskBootstrapper creates and reuses per-issue worktrees and branches.
type GitTaskBootstrapper struct {
	repoRoot   string
	colinHome  string
	baseBranch string
	gitBinary  string
}

// NewGitTaskBootstrapper builds a git-backed bootstrapper.
func NewGitTaskBootstrapper(opts GitTaskBootstrapperOptions) *GitTaskBootstrapper {
	baseBranch := strings.TrimSpace(opts.BaseBranch)
	if baseBranch == "" {
		baseBranch = defaultTaskBaseBranch
	}
	gitBinary := strings.TrimSpace(opts.GitBinary)
	if gitBinary == "" {
		gitBinary = defaultGitBinary
	}
	return &GitTaskBootstrapper{
		repoRoot:   filepath.Clean(strings.TrimSpace(opts.RepoRoot)),
		colinHome:  filepath.Clean(strings.TrimSpace(opts.ColinHome)),
		baseBranch: baseBranch,
		gitBinary:  gitBinary,
	}
}

// EnsureTaskWorkspace creates or reuses the issue worktree and branch.
func (b *GitTaskBootstrapper) EnsureTaskWorkspace(ctx context.Context, issueIdentifier string) (TaskBootstrapResult, error) {
	if b == nil {
		return TaskBootstrapResult{}, errors.New("git task bootstrapper is nil")
	}

	identifier := strings.TrimSpace(issueIdentifier)
	if identifier == "" {
		return TaskBootstrapResult{}, errors.New("issue identifier is required")
	}
	if b.repoRoot == "." || b.repoRoot == "" {
		return TaskBootstrapResult{}, errors.New("repo root is required")
	}
	if b.colinHome == "." || b.colinHome == "" {
		return TaskBootstrapResult{}, errors.New("colin home is required")
	}

	worktreePath := filepath.Join(b.colinHome, worktreesDirName, identifier)
	branchName := issueBranchPrefix + identifier
	result := TaskBootstrapResult{
		WorktreePath: worktreePath,
		BranchName:   branchName,
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return TaskBootstrapResult{}, fmt.Errorf("ensure worktree parent %q: %w", filepath.Dir(worktreePath), err)
	}
	if err := b.ensureBaseBranchExists(ctx); err != nil {
		return TaskBootstrapResult{}, err
	}
	if err := b.ensureWorktreeExists(ctx, worktreePath); err != nil {
		return TaskBootstrapResult{}, err
	}
	if err := b.ensureBranchCheckedOut(ctx, worktreePath, branchName); err != nil {
		return TaskBootstrapResult{}, err
	}

	return result, nil
}

func (b *GitTaskBootstrapper) ensureBaseBranchExists(ctx context.Context) error {
	if err := b.gitRun(ctx, "-C", b.repoRoot, "rev-parse", "--verify", b.baseBranch+"^{commit}"); err != nil {
		return fmt.Errorf("verify base branch %q in %q: %w", b.baseBranch, b.repoRoot, err)
	}
	return nil
}

func (b *GitTaskBootstrapper) ensureWorktreeExists(ctx context.Context, worktreePath string) error {
	if _, err := os.Stat(worktreePath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat worktree path %q: %w", worktreePath, err)
	}

	if err := b.gitRun(ctx, "-C", b.repoRoot, "worktree", "add", "--detach", worktreePath, b.baseBranch); err != nil {
		return fmt.Errorf("create worktree %q from %q: %w", worktreePath, b.baseBranch, err)
	}
	return nil
}

func (b *GitTaskBootstrapper) ensureBranchCheckedOut(ctx context.Context, worktreePath string, branchName string) error {
	currentBranch, err := b.gitOutput(ctx, "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("inspect current branch in %q: %w", worktreePath, err)
	}
	if currentBranch == branchName {
		return nil
	}

	exists, err := b.branchExists(ctx, branchName)
	if err != nil {
		return err
	}
	if exists {
		if err := b.gitRun(ctx, "-C", worktreePath, "checkout", branchName); err != nil {
			return fmt.Errorf("checkout branch %q in %q: %w", branchName, worktreePath, err)
		}
		return nil
	}

	if err := b.gitRun(ctx, "-C", worktreePath, "checkout", "-b", branchName, b.baseBranch); err != nil {
		return fmt.Errorf("create branch %q from %q in %q: %w", branchName, b.baseBranch, worktreePath, err)
	}
	return nil
}

func (b *GitTaskBootstrapper) branchExists(ctx context.Context, branchName string) (bool, error) {
	cmd := exec.CommandContext(ctx, b.gitBinary, "-C", b.repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check branch %q in %q: %w (%s)", branchName, b.repoRoot, err, strings.TrimSpace(string(output)))
}

func (b *GitTaskBootstrapper) gitRun(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, b.gitBinary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (b *GitTaskBootstrapper) gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, b.gitBinary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}
