package codex

import (
	"errors"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

const (
	EventSessionStarted       = "session_started"
	EventStartupFailed        = "startup_failed"
	EventTurnCompleted        = "turn_completed"
	EventTurnFailed           = "turn_failed"
	EventTurnCancelled        = "turn_cancelled"
	EventTurnEndedWithError   = "turn_ended_with_error"
	EventTurnInputRequired    = "turn_input_required"
	EventApprovalAutoApproved = "approval_auto_approved"
	EventUnsupportedToolCall  = "unsupported_tool_call"
	EventNotification         = "notification"
	EventOtherMessage         = "other_message"
	EventMalformed            = "malformed"
)

var (
	ErrResponseTimeout  = errors.New("response_timeout")
	ErrTurnTimeout      = errors.New("turn_timeout")
	ErrPortExit         = errors.New("port_exit")
	ErrTurnFailed       = errors.New("turn_failed")
	ErrTurnCancelled    = errors.New("turn_cancelled")
	ErrTurnInputNeeded  = errors.New("turn_input_required")
	ErrCodexNotFound    = errors.New("codex_not_found")
	ErrInvalidWorkspace = errors.New("invalid_workspace_cwd")
)

// Event is a normalized runtime event emitted from the Codex app-server session.
type Event struct {
	Event      string
	Timestamp  time.Time
	SessionID  string
	ThreadID   string
	TurnID     string
	PID        *int
	Message    string
	Usage      map[string]int64
	RateLimits map[string]any
	Raw        map[string]any
	IssueID    string
	Identifier string
	Workspace  string
}

// Result is the terminal outcome of one runner invocation for a single issue.
type Result struct {
	Issue         domain.Issue
	WorkspacePath string
	Status        string
	Err           error
}

type protocolLine struct {
	msg map[string]any
	err error
}
