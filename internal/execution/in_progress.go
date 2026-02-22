package execution

// InProgressExecutionResult is the execution outcome for an in-progress issue.
type InProgressExecutionResult struct {
	IsWellSpecified   bool
	NeedsInputSummary string
	ExecutionSummary  string
	ExecutionContext  string
	ThreadID          string
	TranscriptRef     string
	ScreenshotRef     string
}
