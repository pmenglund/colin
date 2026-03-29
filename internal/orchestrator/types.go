package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/tracker"
	"github.com/pmenglund/colin/internal/workspace"
)

// Runtime groups the active config and service dependencies used by the orchestrator.
type Runtime struct {
	Workflow  domain.WorkflowDefinition
	Config    domain.ServiceConfig
	Tracker   tracker.Client
	Workspace *workspace.Manager
	Runner    *codex.Runner
}

// Orchestrator owns all mutable scheduling state for issue dispatch, reconciliation, and retries.
type Orchestrator struct {
	logger      *slog.Logger
	eventCh     chan any
	runtime     Runtime
	running     map[string]*runningEntry
	claimed     map[string]struct{}
	retrying    map[string]*retryState
	completed   map[string]struct{}
	totalTokens domain.Totals
	rateLimits  map[string]any
}

type runningEntry struct {
	issue         domain.Issue
	identifier    string
	startedAt     time.Time
	session       domain.LiveSession
	retryAttempt  int
	cancel        context.CancelFunc
	stopReason    string
	cleanupOnStop bool
}

type retryState struct {
	entry domain.RetryEntry
	timer *time.Timer
}

type configUpdatedEvent struct{ runtime Runtime }
type snapshotRequestEvent struct {
	response chan domain.Snapshot
}
type codexEvent struct{ event codex.Event }
type workerExitedEvent struct {
	issueID string
	result  codex.Result
}
type retryFiredEvent struct{ issueID string }
