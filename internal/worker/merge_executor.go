package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

const (
	defaultMergeBaseBranch = "main"
	defaultMergeRemoteName = "origin"
	defaultMergeGitBinary  = "git"
)

// GitMergeExecutorOptions configures a git-backed MergeExecutor.
type GitMergeExecutorOptions struct {
	RepoRoot      string
	BaseBranch    string
	RemoteName    string
	GitBinary     string
	MergePreparer MergePreparer
}

// GitMergeExecutor executes merge queue steps using git.
type GitMergeExecutor struct {
	repoRoot      string
	baseBranch    string
	remoteName    string
	gitBinary     string
	mergePreparer MergePreparer
}

// NewGitMergeExecutor builds a git-backed merge executor.
func NewGitMergeExecutor(opts GitMergeExecutorOptions) *GitMergeExecutor {
	baseBranch := strings.TrimSpace(opts.BaseBranch)
	if baseBranch == "" {
		baseBranch = defaultMergeBaseBranch
	}
	remoteName := strings.TrimSpace(opts.RemoteName)
	if remoteName == "" {
		remoteName = defaultMergeRemoteName
	}
	gitBinary := strings.TrimSpace(opts.GitBinary)
	if gitBinary == "" {
		gitBinary = defaultMergeGitBinary
	}

	return &GitMergeExecutor{
		repoRoot:      filepath.Clean(strings.TrimSpace(opts.RepoRoot)),
		baseBranch:    baseBranch,
		remoteName:    remoteName,
		gitBinary:     gitBinary,
		mergePreparer: opts.MergePreparer,
	}
}

// ExecuteMerge runs merge, push, branch delete, and worktree delete in order.
func (e *GitMergeExecutor) ExecuteMerge(ctx context.Context, issue linear.Issue) error {
	if e == nil {
		return errors.New("git merge executor is nil")
	}
	if e.repoRoot == "." || e.repoRoot == "" {
		return errors.New("repo root is required")
	}

	branchName, err := e.resolveBranchName(issue)
	if err != nil {
		return err
	}
	exists, err := e.branchExists(ctx, branchName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("source branch %q does not exist in %q", branchName, e.repoRoot)
	}
	worktreePath, err := e.resolveWorktreePath(ctx, issue, branchName)
	if err != nil {
		return err
	}
	if err := e.ensureBaseBranchExists(ctx); err != nil {
		return err
	}
	if err := e.prepareMerge(ctx, issue, branchName, worktreePath); err != nil {
		return err
	}
	if err := e.checkoutBaseBranch(ctx); err != nil {
		return err
	}
	if err := e.mergeTaskBranch(ctx, branchName); err != nil {
		return err
	}
	if err := e.pushBaseBranch(ctx); err != nil {
		return err
	}
	if err := e.deleteTaskBranch(ctx, branchName, worktreePath); err != nil {
		return err
	}
	if err := e.deleteTaskWorktree(ctx, worktreePath); err != nil {
		return err
	}

	return nil
}

// NeedsMergeRecovery reports whether a done issue should be moved back to merge.
func (e *GitMergeExecutor) NeedsMergeRecovery(ctx context.Context, issue linear.Issue) (bool, string, error) {
	if e == nil {
		return false, "", errors.New("git merge executor is nil")
	}
	if e.repoRoot == "." || e.repoRoot == "" {
		return false, "", errors.New("repo root is required")
	}

	branchName, err := e.resolveBranchName(issue)
	if err != nil {
		return false, "", err
	}
	exists, err := e.branchExists(ctx, branchName)
	if err != nil {
		return false, "", err
	}
	if !exists {
		return false, "", nil
	}
	if err := e.ensureBaseBranchExists(ctx); err != nil {
		return false, "", err
	}

	merged, err := e.isAncestor(ctx, branchName, e.baseBranch)
	if err != nil {
		return false, "", err
	}
	if merged {
		return true, fmt.Sprintf(
			"branch %q is already merged into %q but still exists; cleanup is incomplete",
			branchName,
			e.baseBranch,
		), nil
	}
	return true, fmt.Sprintf(
		"branch %q is not merged into %q",
		branchName,
		e.baseBranch,
	), nil
}

func (e *GitMergeExecutor) prepareMerge(
	ctx context.Context,
	issue linear.Issue,
	branchName string,
	worktreePath string,
) error {
	if e.mergePreparer == nil {
		return nil
	}
	if err := e.mergePreparer.PrepareMerge(
		ctx,
		issue,
		branchName,
		worktreePath,
		e.baseBranch,
		e.remoteName,
	); err != nil {
		return fmt.Errorf("prepare merge for branch %q: %w", branchName, err)
	}
	return nil
}

func (e *GitMergeExecutor) ensureBaseBranchExists(ctx context.Context) error {
	if err := gitRun(ctx, e.gitBinary, "-C", e.repoRoot, "rev-parse", "--verify", e.baseBranch+"^{commit}"); err != nil {
		return fmt.Errorf("verify base branch %q in %q: %w", e.baseBranch, e.repoRoot, err)
	}
	return nil
}

func (e *GitMergeExecutor) checkoutBaseBranch(ctx context.Context) error {
	if err := gitRun(ctx, e.gitBinary, "-C", e.repoRoot, "checkout", e.baseBranch); err != nil {
		return fmt.Errorf("checkout base branch %q in %q: %w", e.baseBranch, e.repoRoot, err)
	}
	return nil
}

func (e *GitMergeExecutor) mergeTaskBranch(ctx context.Context, branchName string) error {
	exists, err := e.branchExists(ctx, branchName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("source branch %q does not exist in %q", branchName, e.repoRoot)
	}

	merged, err := e.isAncestor(ctx, branchName, e.baseBranch)
	if err != nil {
		return err
	}
	if merged {
		return nil
	}

	if err := gitRun(ctx, e.gitBinary, "-C", e.repoRoot, "merge", "--no-ff", "--no-edit", branchName); err != nil {
		return fmt.Errorf("merge branch %q into %q: %w", branchName, e.baseBranch, err)
	}
	return nil
}

func (e *GitMergeExecutor) pushBaseBranch(ctx context.Context) error {
	if err := gitRun(ctx, e.gitBinary, "-C", e.repoRoot, "remote", "get-url", e.remoteName); err != nil {
		return fmt.Errorf("verify remote %q in %q: %w", e.remoteName, e.repoRoot, err)
	}
	if err := gitRun(ctx, e.gitBinary, "-C", e.repoRoot, "push", e.remoteName, e.baseBranch); err != nil {
		return fmt.Errorf("push %q to remote %q: %w", e.baseBranch, e.remoteName, err)
	}
	return nil
}

func (e *GitMergeExecutor) deleteTaskBranch(ctx context.Context, branchName string, worktreePath string) error {
	exists, err := e.branchExists(ctx, branchName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	if strings.TrimSpace(worktreePath) != "" {
		if _, statErr := os.Stat(worktreePath); statErr == nil {
			currentBranch, outErr := gitOutputAllowMissing(ctx, e.gitBinary, "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
			if outErr != nil {
				return fmt.Errorf("inspect branch in worktree %q: %w", worktreePath, outErr)
			}
			if strings.TrimSpace(currentBranch) == branchName {
				if err := gitRun(ctx, e.gitBinary, "-C", worktreePath, "checkout", "--detach"); err != nil {
					return fmt.Errorf("detach HEAD in worktree %q before branch delete: %w", worktreePath, err)
				}
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat worktree path %q: %w", worktreePath, statErr)
		}
	}

	if err := gitRun(ctx, e.gitBinary, "-C", e.repoRoot, "branch", "-d", branchName); err != nil {
		return fmt.Errorf("delete branch %q in %q: %w", branchName, e.repoRoot, err)
	}
	return nil
}

func (e *GitMergeExecutor) deleteTaskWorktree(ctx context.Context, worktreePath string) error {
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" {
		return nil
	}
	if _, err := os.Stat(worktreePath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat worktree path %q: %w", worktreePath, err)
	}

	if err := gitRun(ctx, e.gitBinary, "-C", e.repoRoot, "worktree", "remove", "--force", worktreePath); err != nil {
		return fmt.Errorf("delete worktree %q from %q: %w", worktreePath, e.repoRoot, err)
	}
	return nil
}

func (e *GitMergeExecutor) findWorktreePathByBranch(ctx context.Context, branchName string) (string, error) {
	listing, err := gitOutputAllowMissing(ctx, e.gitBinary, "-C", e.repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("list worktrees in %q: %w", e.repoRoot, err)
	}
	if strings.TrimSpace(listing) == "" {
		return "", nil
	}

	const prefix = "refs/heads/"
	target := prefix + strings.TrimSpace(branchName)

	currentPath := ""
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			currentPath = ""
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			continue
		}
		if strings.HasPrefix(line, "branch ") && currentPath != "" {
			branchRef := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			if branchRef == target {
				return currentPath, nil
			}
		}
	}

	return "", nil
}

func (e *GitMergeExecutor) resolveBranchName(issue linear.Issue) (string, error) {
	branchName := strings.TrimSpace(issue.Metadata[workflow.MetaBranchName])
	if branchName != "" {
		return branchName, nil
	}

	identifier := strings.TrimSpace(issue.Identifier)
	if identifier == "" {
		return "", errors.New("issue identifier is required when metadata branch name is empty")
	}
	return issueBranchPrefix + identifier, nil
}

func (e *GitMergeExecutor) resolveWorktreePath(ctx context.Context, issue linear.Issue, branchName string) (string, error) {
	worktreePath := strings.TrimSpace(issue.Metadata[workflow.MetaWorktreePath])
	if worktreePath == "" {
		discovered, err := e.findWorktreePathByBranch(ctx, branchName)
		if err != nil {
			return "", fmt.Errorf("resolve worktree path for branch %q: %w", branchName, err)
		}
		worktreePath = discovered
	}
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" {
		return "", fmt.Errorf("worktree path for branch %q is required", branchName)
	}

	if _, err := os.Stat(worktreePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("worktree path %q for branch %q does not exist", worktreePath, branchName)
		}
		return "", fmt.Errorf("stat worktree path %q: %w", worktreePath, err)
	}
	return worktreePath, nil
}

func (e *GitMergeExecutor) branchExists(ctx context.Context, branchName string) (bool, error) {
	exists, err := gitCheckExitCodeOneMeansFalse(ctx, e.gitBinary, "-C", e.repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	if err != nil {
		return false, fmt.Errorf("check branch %q in %q: %w", branchName, e.repoRoot, err)
	}
	return exists, nil
}

func (e *GitMergeExecutor) isAncestor(ctx context.Context, ancestor string, descendant string) (bool, error) {
	ok, err := gitCheckExitCodeOneMeansFalse(ctx, e.gitBinary, "-C", e.repoRoot, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		return false, fmt.Errorf("check whether %q is ancestor of %q in %q: %w", ancestor, descendant, e.repoRoot, err)
	}
	return ok, nil
}
