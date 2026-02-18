package worker

import (
	"context"

	"github.com/pmenglund/colin/internal/linear"
)

// InProgressExecutionResult is the Codex execution outcome for an in-progress issue.
type InProgressExecutionResult struct {
	IsWellSpecified   bool
	NeedsInputSummary string
	ExecutionSummary  string
	ThreadID          string
	SessionID         string
	ThreadResumed     bool
}

// InProgressExecutor evaluates and executes work for in-progress issues.
type InProgressExecutor interface {
	EvaluateAndExecute(ctx context.Context, issue linear.Issue) (InProgressExecutionResult, error)
}

// MergeExecutor performs merge queue execution for issues in the Merge state.
type MergeExecutor interface {
	ExecuteMerge(ctx context.Context, issue linear.Issue) error
}
