package orchestrator

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/tracker"
	"github.com/pmenglund/colin/internal/workspace"
)

// Runtime groups the active config and service dependencies used by the orchestrator.
type Runtime struct {
	Workflow  domain.WorkflowDefinition
	Config    domain.ServiceConfig
	Tracker   tracker.Client
	Repo      *repoops.Manager
	Workspace *workspace.Manager
	Runner    Runner
}

// Runner describes the automation runner used by the orchestrator.
type Runner interface {
	Run(ctx context.Context, issue domain.Issue, attempt *int, onEvent func(codex.Event)) codex.Result
}

// Orchestrator owns all mutable scheduling state for issue dispatch, reconciliation, and retries.
type Orchestrator struct {
	logger            *slog.Logger
	eventCh           chan any
	runtime           Runtime
	loopStarted       atomic.Bool
	running           map[string]*runningEntry
	claimed           map[string]struct{}
	retrying          map[string]*retryState
	reviewSync        map[string]*reviewSyncState
	completed         map[string]string
	totalTokens       domain.Totals
	rateLimits        map[string]any
	issueStates       map[string]int
	pausedIssueStates map[string]domain.PausedStateSummary
}

type runningEntry struct {
	issue         domain.Issue
	identifier    string
	runType       string
	startedAt     time.Time
	session       domain.LiveSession
	outputLog     []domain.OutputLog
	comment       *commentThreadState
	retryAttempt  int
	cancel        context.CancelFunc
	stopReason    string
	cleanupOnStop bool
}

type retryState struct {
	entry        domain.RetryEntry
	timer        *time.Timer
	comment      *commentThreadState
	notifyLinear bool
}

type reviewSyncState struct {
	firstObserved time.Time
	nextPollAt    time.Time
	timedOut      bool
	comment       *commentThreadState
}

type commentThreadState struct {
	RunType       string
	RootCommentID string
}

type configUpdatedEvent struct{ runtime Runtime }
type codexEvent struct{ event codex.Event }
type workerExitedEvent struct {
	issueID string
	result  codex.Result
}
type retryFiredEvent struct{ issueID string }
type snapshotRequestEvent struct{ response chan domain.Snapshot }
