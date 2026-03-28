package execution

import "github.com/pmenglund/colin/internal/linear"

// AttemptRequest describes one agent attempt for an issue.
type AttemptRequest struct {
	Issue         linear.Issue
	Attempt       *int
	WorkspacePath string
	Continuation  bool
}

// AttemptStatus classifies the outcome of one attempt.
type AttemptStatus string

const (
	AttemptStatusSucceeded AttemptStatus = "succeeded"
	AttemptStatusFailed    AttemptStatus = "failed"
	AttemptStatusTimedOut  AttemptStatus = "timed_out"
	AttemptStatusCanceled  AttemptStatus = "canceled"
	AttemptStatusStalled   AttemptStatus = "stalled"
)

// AttemptResult captures one attempt outcome and session identifiers.
type AttemptResult struct {
	Status AttemptStatus

	IsWellSpecified      bool
	NeedsInputSummary    string
	ExecutionSummary     string
	ExecutionContext     string
	ThreadID             string
	TurnID               string
	ResumedFromThreadID  string
	ResumeFallbackReason string
	BeforeEvidenceRef    string
	AfterEvidenceRef     string
	ShouldContinue       bool
}
