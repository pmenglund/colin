package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
)

func TestShouldDispatchRejectsTodoBlockedByNonTerminal(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime:  Runtime{Config: domain.ServiceConfig{Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo"}, TerminalStates: []string{"Done"}}}},
		running:  map[string]*runningEntry{},
		claimed:  map[string]struct{}{},
		retrying: map[string]*retryState{},
	}
	state := "In Progress"
	if orch.shouldDispatch(domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		Title:      "Test",
		State:      "Todo",
		BlockedBy:  []domain.BlockerRef{{State: &state}},
	}) {
		t.Fatal("shouldDispatch() = true, want false")
	}
}

func TestHandleWorkerExitSchedulesContinuationRetry(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", State: "In Progress"}
	orch := &Orchestrator{
		logger:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime:     Runtime{Config: domain.ServiceConfig{Agent: domain.AgentConfig{MaxRetryBackoff: 5 * time.Minute}}},
		running:     map[string]*runningEntry{"1": {issue: issue, identifier: issue.Identifier, startedAt: time.Now().Add(-2 * time.Second)}},
		claimed:     map[string]struct{}{"1": {}},
		retrying:    map[string]*retryState{},
		completed:   map[string]struct{}{},
		totalTokens: domain.Totals{},
		eventCh:     make(chan any, 4),
	}

	orch.handleWorkerExit(context.Background(), workerExitedEvent{
		issueID: "1",
		result:  codex.Result{Issue: issue, Status: "succeeded"},
	})

	retry, ok := orch.retrying["1"]
	if !ok {
		t.Fatal("retry entry missing")
	}
	if retry.entry.Attempt != 1 {
		t.Fatalf("retry attempt = %d", retry.entry.Attempt)
	}
	if retry.entry.Error != "" {
		t.Fatalf("retry error = %q", retry.entry.Error)
	}
}

func TestBackoffCapsAtConfiguredMax(t *testing.T) {
	t.Parallel()

	if got := backoff(30*time.Second, 5); got != 30*time.Second {
		t.Fatalf("backoff() = %v", got)
	}
}
