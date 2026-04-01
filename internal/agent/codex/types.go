package codex

import (
	"errors"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

const (
	RunTypeCoding        = "coding"
	RunTypeReviewPublish = "review_publish"
	RunTypeMerge         = "merge"

	OutcomeReadyForReviewLine     = "COLIN_OUTCOME: READY_FOR_REVIEW"
	OutcomeReadyForMergeRetryLine = "COLIN_OUTCOME: READY_FOR_MERGE_RETRY"
	OutcomeNeedsSpecLine          = "COLIN_OUTCOME: NEEDS_SPEC"

	EventSessionStarted       = "session_started"
	EventStartupFailed        = "startup_failed"
	EventWorkspacePrepared    = "workspace_prepared"
	EventTurnCompleted        = "turn_completed"
	EventTurnFailed           = "turn_failed"
	EventTurnCancelled        = "turn_cancelled"
	EventTurnEndedWithError   = "turn_ended_with_error"
	EventTurnInputRequired    = "turn_input_required"
	EventApprovalAutoApproved = "approval_auto_approved"
	EventIssueStateRefreshed  = "issue_state_refreshed"
	EventContinuationNeeded   = "continuation_needed"
	EventRetryScheduled       = "retry_scheduled"
	EventRetryFired           = "retry_fired"
	EventRunFailed            = "run_failed"
	EventRunSucceeded         = "run_succeeded"
	EventReviewPublishStarted = "review_publish_started"
	EventReviewPublishDone    = "review_publish_completed"
	EventMergeStarted         = "merge_started"
	EventMergeDone            = "merge_completed"
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
	RunType    string
	Timestamp  time.Time
	SessionID  string
	ThreadID   string
	TurnID     string
	PID        *int
	Message    string
	Attempt    int
	State      string
	PrevState  string
	Duration   time.Duration
	Usage      map[string]int64
	RateLimits domain.RateLimitSnapshot
	Raw        map[string]any
	IssueID    string
	Identifier string
	Workspace  string
	Branch     string
	BaseRef    string
	PRNumber   int
	PRURL      string
	PRState    string
	Action     string
}

// Result is the terminal outcome of one runner invocation for a single issue.
type Result struct {
	Issue            domain.Issue
	RunType          string
	WorkspacePath    string
	Status           string
	Summary          string
	PR               *domain.PullRequestRef
	ThreadsHandled   int
	ThreadsRemaining int
	Err              error
}

type protocolLine struct {
	msg map[string]any
	err error
}
