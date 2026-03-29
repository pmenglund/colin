package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/workspace"
)

type fakeTrackerClient struct {
	fetchIssuesByStates func(context.Context, []string) ([]domain.Issue, error)
}

func (fakeTrackerClient) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	return nil, nil
}

func (f fakeTrackerClient) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	if f.fetchIssuesByStates != nil {
		return f.fetchIssuesByStates(ctx, states)
	}
	return nil, nil
}

func (fakeTrackerClient) FetchIssueStatesByIDs(context.Context, []string) ([]domain.Issue, error) {
	return nil, nil
}

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

func TestStartupTerminalCleanupSkipsEmptyTerminalStates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspacePath := filepath.Join(root, workspace.SanitizeWorkspaceKey("ABC-1"))
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}

	var fetchCalled bool
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{Root: root},
		Tracker: domain.TrackerConfig{
			TerminalStates: nil,
		},
	}
	orch := New(
		Runtime{
			Config:    cfg,
			Tracker:   fakeTrackerClient{fetchIssuesByStates: func(context.Context, []string) ([]domain.Issue, error) { fetchCalled = true; return nil, nil }},
			Workspace: workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	if err := orch.StartupTerminalCleanup(context.Background()); err != nil {
		t.Fatalf("StartupTerminalCleanup() error = %v", err)
	}
	if fetchCalled {
		t.Fatal("FetchIssuesByStates() was called for empty terminal states")
	}
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("workspace path stat error = %v, want existing workspace", err)
	}
}

func TestSnapshotContextReturnsRuntimeSnapshot(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC().Add(-2 * time.Second)
	dueAt := time.Now().UTC().Add(30 * time.Second)
	orch := New(
		Runtime{
			Config: domain.ServiceConfig{
				Polling: domain.PollingConfig{Interval: time.Hour},
			},
			Tracker: fakeTrackerClient{},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	orch.running["1"] = &runningEntry{
		issue:      domain.Issue{ID: "1", Identifier: "ABC-1", State: "In Progress"},
		identifier: "ABC-1",
		startedAt:  startedAt,
		cancel:     func() {},
		session: domain.LiveSession{
			SessionID:         "session-1",
			TurnCount:         2,
			LastCodexEvent:    codex.EventTurnCompleted,
			CodexInputTokens:  11,
			CodexOutputTokens: 7,
			CodexTotalTokens:  18,
		},
	}
	orch.retrying["2"] = &retryState{
		entry: domain.RetryEntry{
			IssueID:    "2",
			Identifier: "ABC-2",
			Attempt:    3,
			DueAt:      dueAt,
		},
	}
	orch.rateLimits = map[string]any{"requests_remaining": 12}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	requestCtx, requestCancel := context.WithTimeout(context.Background(), time.Second)
	defer requestCancel()

	snapshot, err := orch.SnapshotContext(requestCtx)
	if err != nil {
		t.Fatalf("SnapshotContext() error = %v", err)
	}
	if got := snapshot.Counts["running"]; got != 1 {
		t.Fatalf("snapshot.Counts[running] = %d, want 1", got)
	}
	if got := snapshot.Counts["retrying"]; got != 1 {
		t.Fatalf("snapshot.Counts[retrying] = %d, want 1", got)
	}
	if len(snapshot.Running) != 1 {
		t.Fatalf("len(snapshot.Running) = %d, want 1", len(snapshot.Running))
	}
	if len(snapshot.Retrying) != 1 {
		t.Fatalf("len(snapshot.Retrying) = %d, want 1", len(snapshot.Retrying))
	}
	if snapshot.Running[0].Identifier != "ABC-1" {
		t.Fatalf("snapshot.Running[0].Identifier = %q, want ABC-1", snapshot.Running[0].Identifier)
	}
	if snapshot.Retrying[0].Identifier != "ABC-2" {
		t.Fatalf("snapshot.Retrying[0].Identifier = %q, want ABC-2", snapshot.Retrying[0].Identifier)
	}
	if snapshot.Running[0].SessionID != "session-1" {
		t.Fatalf("snapshot.Running[0].SessionID = %q, want session-1", snapshot.Running[0].SessionID)
	}
	if snapshot.Running[0].TotalTokens != 18 {
		t.Fatalf("snapshot.Running[0].TotalTokens = %d, want 18", snapshot.Running[0].TotalTokens)
	}
	if got := snapshot.RateLimits["requests_remaining"]; got != 12 {
		t.Fatalf("snapshot.RateLimits[requests_remaining] = %v, want 12", got)
	}
	snapshot.RateLimits["requests_remaining"] = 0
	if got := orch.rateLimits["requests_remaining"]; got != 12 {
		t.Fatalf("orch.rateLimits[requests_remaining] = %v, want 12", got)
	}
}

func TestSnapshotContextHonorsCanceledContextWhenLoopIsNotRunning(t *testing.T) {
	t.Parallel()

	orch := New(Runtime{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := orch.SnapshotContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SnapshotContext() error = %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestSnapshotContextHandlesConcurrentRequests(t *testing.T) {
	t.Parallel()

	orch := New(
		Runtime{
			Config: domain.ServiceConfig{
				Polling: domain.PollingConfig{Interval: time.Hour},
			},
			Tracker: fakeTrackerClient{},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	orch.running["1"] = &runningEntry{
		issue:      domain.Issue{ID: "1", Identifier: "ABC-1", State: "In Progress"},
		identifier: "ABC-1",
		startedAt:  time.Now().UTC().Add(-time.Second),
		cancel:     func() {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestCtx, requestCancel := context.WithTimeout(context.Background(), time.Second)
			defer requestCancel()

			snapshot, err := orch.SnapshotContext(requestCtx)
			if err != nil {
				errCh <- err
				return
			}
			if got := snapshot.Counts["running"]; got != 1 {
				errCh <- errors.New("unexpected running count")
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("SnapshotContext() concurrent error = %v", err)
	}
}
