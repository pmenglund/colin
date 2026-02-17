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
	TranscriptRef     string
	ScreenshotRef     string
}

// InProgressExecutor evaluates and executes work for in-progress issues.
type InProgressExecutor interface {
	EvaluateAndExecute(ctx context.Context, issue linear.Issue) (InProgressExecutionResult, error)
}
