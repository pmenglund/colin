package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

const (
	defaultMergeBaseBranch = "main"
	defaultMergeRemoteName = "origin"
)

// GitMergeExecutorOptions configures a git-backed MergeExecutor.
type GitMergeExecutorOptions struct {
	RepoRoot       string
	BaseBranch     string
	RemoteName     string
	GitBinary      string
	PushBaseBranch *bool
	MergePreparer  MergePreparer
}

// GitMergeExecutor executes merge queue steps using git.
type GitMergeExecutor struct {
	repoRoot             string
	baseBranch           string
	remoteName           string
	shouldPushBaseBranch bool
	mergePreparer        MergePreparer
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
	pushBaseBranch := true
	if opts.PushBaseBranch != nil {
		pushBaseBranch = *opts.PushBaseBranch
	}

	return &GitMergeExecutor{
		repoRoot:             filepath.Clean(strings.TrimSpace(opts.RepoRoot)),
		baseBranch:           baseBranch,
		remoteName:           remoteName,
		shouldPushBaseBranch: pushBaseBranch,
		mergePreparer:        opts.MergePreparer,
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
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	repo, err := openRepository(e.repoRoot)
	if err != nil {
		return fmt.Errorf("verify base branch %q in %q: %w", e.baseBranch, e.repoRoot, err)
	}
	if _, err := resolveCommit(repo, e.baseBranch); err != nil {
		return fmt.Errorf("verify base branch %q in %q: %w", e.baseBranch, e.repoRoot, err)
	}
	return nil
}

func (e *GitMergeExecutor) checkoutBaseBranch(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	repo, err := openRepository(e.repoRoot)
	if err != nil {
		return fmt.Errorf("checkout base branch %q in %q: %w", e.baseBranch, e.repoRoot, err)
	}
	if err := checkoutBranch(repo, e.baseBranch); err != nil {
		return fmt.Errorf("checkout base branch %q in %q: %w", e.baseBranch, e.repoRoot, err)
	}
	return nil
}

func (e *GitMergeExecutor) mergeTaskBranch(ctx context.Context, branchName string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	repo, err := openRepository(e.repoRoot)
	if err != nil {
		return fmt.Errorf("merge branch %q into %q: %w", branchName, e.baseBranch, err)
	}

	exists, err := gitBranchExists(repo, branchName)
	if err != nil {
		return fmt.Errorf("check branch %q in %q: %w", branchName, e.repoRoot, err)
	}
	if !exists {
		return fmt.Errorf("source branch %q does not exist in %q", branchName, e.repoRoot)
	}

	merged, err := isAncestorBranch(repo, branchName, e.baseBranch)
	if err != nil {
		return fmt.Errorf("check whether %q is ancestor of %q in %q: %w", branchName, e.baseBranch, e.repoRoot, err)
	}
	if merged {
		return nil
	}

	if err := fastForwardBranch(repo, e.baseBranch, branchName); err != nil {
		return fmt.Errorf("merge branch %q into %q: %w", branchName, e.baseBranch, err)
	}
	return nil
}

func (e *GitMergeExecutor) pushBaseBranch(ctx context.Context) error {
	if !e.shouldPushBaseBranch {
		return nil
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	repo, err := openRepository(e.repoRoot)
	if err != nil {
		return fmt.Errorf("verify remote %q in %q: %w", e.remoteName, e.repoRoot, err)
	}

	if _, err := remoteURL(repo, e.remoteName); err != nil {
		if errors.Is(err, git.ErrRemoteNotFound) {
			return nil
		}
		return fmt.Errorf("verify remote %q in %q: %w", e.remoteName, e.repoRoot, err)
	}
	if err := pushBranch(ctx, repo, e.remoteName, e.baseBranch); err != nil {
		return fmt.Errorf("push %q to remote %q: %w", e.baseBranch, e.remoteName, err)
	}
	return nil
}

func (e *GitMergeExecutor) deleteTaskBranch(ctx context.Context, branchName string, worktreePath string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	repo, err := openRepository(e.repoRoot)
	if err != nil {
		return fmt.Errorf("delete branch %q in %q: %w", branchName, e.repoRoot, err)
	}

	exists, err := gitBranchExists(repo, branchName)
	if err != nil {
		return fmt.Errorf("check branch %q in %q: %w", branchName, e.repoRoot, err)
	}
	if !exists {
		return nil
	}

	if strings.TrimSpace(worktreePath) != "" {
		if _, statErr := os.Stat(worktreePath); statErr == nil {
			worktreeRepo, openErr := openRepository(worktreePath)
			if openErr != nil {
				return fmt.Errorf("inspect branch in worktree %q: %w", worktreePath, openErr)
			}

			currentBranch, outErr := currentBranchName(worktreeRepo)
			if outErr != nil {
				return fmt.Errorf("inspect branch in worktree %q: %w", worktreePath, outErr)
			}
			if strings.TrimSpace(currentBranch) == branchName {
				if err := checkoutDetachedHead(worktreeRepo); err != nil {
					return fmt.Errorf("detach HEAD in worktree %q before branch delete: %w", worktreePath, err)
				}
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat worktree path %q: %w", worktreePath, statErr)
		}
	}

	merged, err := isAncestorBranch(repo, branchName, e.baseBranch)
	if err != nil {
		return fmt.Errorf("delete branch %q in %q: %w", branchName, e.repoRoot, err)
	}
	if !merged {
		return fmt.Errorf("delete branch %q in %q: branch is not merged into %q", branchName, e.repoRoot, e.baseBranch)
	}

	if err := repo.Storer.RemoveReference(branchRef(branchName)); err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
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

	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	if err := removeLinkedWorktree(e.repoRoot, worktreePath); err != nil {
		return fmt.Errorf("delete worktree %q from %q: %w", worktreePath, e.repoRoot, err)
	}
	return nil
}

func (e *GitMergeExecutor) findWorktreePathByBranch(ctx context.Context, branchName string) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}

	worktreePath, err := findLinkedWorktreePathByBranch(e.repoRoot, branchName)
	if err != nil {
		return "", fmt.Errorf("list worktrees in %q: %w", e.repoRoot, err)
	}
	return strings.TrimSpace(worktreePath), nil
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
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}

	repo, err := openRepository(e.repoRoot)
	if err != nil {
		return false, fmt.Errorf("check branch %q in %q: %w", branchName, e.repoRoot, err)
	}

	exists, err := gitBranchExists(repo, branchName)
	if err != nil {
		return false, fmt.Errorf("check branch %q in %q: %w", branchName, e.repoRoot, err)
	}
	return exists, nil
}

func (e *GitMergeExecutor) isAncestor(ctx context.Context, ancestor string, descendant string) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}

	repo, err := openRepository(e.repoRoot)
	if err != nil {
		return false, fmt.Errorf("check whether %q is ancestor of %q in %q: %w", ancestor, descendant, e.repoRoot, err)
	}

	ok, err := isAncestorBranch(repo, ancestor, descendant)
	if err != nil {
		return false, fmt.Errorf("check whether %q is ancestor of %q in %q: %w", ancestor, descendant, e.repoRoot, err)
	}
	return ok, nil
}
