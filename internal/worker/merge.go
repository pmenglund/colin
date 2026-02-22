package worker

import (
	"context"

	"github.com/pmenglund/colin/internal/linear"
)

// MergeExecutor executes the merge queue side effects for a merge-ready issue.
type MergeExecutor interface {
	ExecuteMerge(ctx context.Context, issue linear.Issue) error
}

// MergePreparer performs pre-merge preparation work before git-side merge execution.
type MergePreparer interface {
	PrepareMerge(
		ctx context.Context,
		issue linear.Issue,
		branchName string,
		worktreePath string,
		baseBranch string,
		remoteName string,
	) error
}

// NoopMergeExecutor is a merge executor for fake/offline runs.
type NoopMergeExecutor struct{}

// ExecuteMerge is intentionally a no-op for fake/offline execution.
func (NoopMergeExecutor) ExecuteMerge(_ context.Context, _ linear.Issue) error {
	return nil
}
