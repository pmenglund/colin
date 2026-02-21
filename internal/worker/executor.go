package worker

import (
	"context"

	"github.com/pmenglund/colin/internal/execution"
	"github.com/pmenglund/colin/internal/linear"
)

// InProgressExecutionResult is the codex execution outcome for an in-progress issue.
type InProgressExecutionResult = execution.InProgressExecutionResult

// InProgressExecutor evaluates and executes work for in-progress issues.
type InProgressExecutor interface {
	EvaluateAndExecute(ctx context.Context, issue linear.Issue) (InProgressExecutionResult, error)
}
