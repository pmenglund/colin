package execution

import "time"

// SessionUpdate describes live execution metadata emitted during a Codex turn.
type SessionUpdate struct {
	ThreadID             string
	TurnID               string
	LastEvent            string
	LastMessage          string
	LastTimestamp        time.Time
	InputTokens          int
	OutputTokens         int
	TotalTokens          int
	ReportedInputTokens  int
	ReportedOutputTokens int
	ReportedTotalTokens  int
}
