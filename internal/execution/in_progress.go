package execution

// InProgressExecutionResult is the execution outcome for an in-progress issue.
type InProgressExecutionResult struct {
	IsWellSpecified      bool
	NeedsInputSummary    string
	ExecutionSummary     string
	ExecutionContext     string
	ThreadID             string
	ResumedFromThreadID  string
	ResumeFallbackReason string
	BeforeEvidenceRef    string
	AfterEvidenceRef     string
}
