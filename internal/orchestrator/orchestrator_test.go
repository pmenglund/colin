package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/execution"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
)

type fakeConfigProvider struct{ cfg config.Config }

func (f *fakeConfigProvider) Current() config.Config { return f.cfg }
func (f *fakeConfigProvider) Reload() error          { return nil }

type fakeTracker struct {
	mu          sync.Mutex
	candidates  []linear.Issue
	terminal    []linear.Issue
	refreshByID map[string]linear.Issue
}

func (f *fakeTracker) ListCandidateIssues(context.Context, string) ([]linear.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]linear.Issue, len(f.candidates))
	copy(out, f.candidates)
	return out, nil
}

func (f *fakeTracker) FetchIssueStatesByIDs(context.Context, []string) (map[string]linear.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]linear.Issue, len(f.refreshByID))
	for key, value := range f.refreshByID {
		out[key] = value
	}
	return out, nil
}

func (f *fakeTracker) FetchIssuesByStates(context.Context, string, []string) ([]linear.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]linear.Issue, len(f.terminal))
	copy(out, f.terminal)
	return out, nil
}

type fakeWorkspaceManager struct {
	cleanupIDs []string
	removed    []string
}

func (f *fakeWorkspaceManager) Ensure(context.Context, string) (workspace.Workspace, error) {
	return workspace.Workspace{Path: "/tmp/ws", Metadata: map[string]string{workflow.MetaBranchName: "colin/COL-1"}}, nil
}
func (f *fakeWorkspaceManager) BeforeRun(context.Context, workspace.Workspace) error { return nil }
func (f *fakeWorkspaceManager) AfterRun(context.Context, workspace.Workspace, error) {}
func (f *fakeWorkspaceManager) Remove(_ context.Context, ws workspace.Workspace) error {
	f.removed = append(f.removed, ws.Path)
	return nil
}
func (f *fakeWorkspaceManager) CleanupTerminal(_ context.Context, issueIdentifiers []string) error {
	f.cleanupIDs = append(f.cleanupIDs, issueIdentifiers...)
	return nil
}

type fakeRunner struct {
	mu      sync.Mutex
	calls   []execution.AttemptRequest
	result  execution.AttemptResult
	err     error
	blockCh chan struct{}
}

func (f *fakeRunner) RunAttempt(_ context.Context, req execution.AttemptRequest, _ func(execution.SessionUpdate)) (execution.AttemptResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.blockCh != nil {
		<-f.blockCh
	}
	if f.err != nil {
		return execution.AttemptResult{}, f.err
	}
	return f.result, nil
}

func testConfig(now time.Time) config.Config {
	return config.Config{
		LinearBackend:          config.LinearBackendFake,
		LinearTeamID:           "team",
		LinearBaseURL:          "http://example.invalid",
		GitHubAPIURL:           "http://example.invalid",
		BaseBranch:             "main",
		PushAfterMerge:         true,
		ColinHome:              "/tmp/colin",
		WorkerID:               "worker",
		PollEvery:              time.Second,
		LeaseTTL:               time.Minute,
		MaxConcurrency:         1,
		MaxTurns:               3,
		MaxRetryBackoff:        time.Minute,
		MaxConcurrencyByState:  map[string]int{},
		WorkflowStates:         config.DefaultWorkflowStates(),
		ActiveStates:           []string{workflow.StateTodo, workflow.StateInProgress},
		TerminalStates:         []string{workflow.StateDone},
		Hooks:                  config.HookConfig{Timeout: time.Second},
		Codex:                  config.CodexConfig{Command: "codex app-server", ReadTimeout: time.Second, TurnTimeout: time.Minute, StallTimeout: time.Minute},
		WorkspaceRoot:          "/tmp/workspaces",
		WorkflowPath:           "WORKFLOW.md",
		WorkflowPromptTemplate: "prompt",
	}
}

func TestOrchestratorStartupCleanupUsesTerminalStates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	tracker := &fakeTracker{
		terminal: []linear.Issue{{Identifier: "COLIN-1"}, {Identifier: "COLIN-2"}},
	}
	workspaces := &fakeWorkspaceManager{}
	orchestrator, err := New(Options{
		Tracker:    tracker,
		Configs:    &fakeConfigProvider{cfg: testConfig(now)},
		Workspaces: workspaces,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := orchestrator.start(context.Background(), testConfig(now)); err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if len(workspaces.cleanupIDs) != 2 {
		t.Fatalf("cleanup IDs = %#v", workspaces.cleanupIDs)
	}
}

func TestOrchestratorDispatchesByPriorityThenAge(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
		p1 := 2
		p2 := 1
		runner := &fakeRunner{result: execution.AttemptResult{Status: execution.AttemptStatusSucceeded}}
		tracker := &fakeTracker{
			candidates: []linear.Issue{
				{ID: "1", Identifier: "COLIN-1", Title: "one", StateName: workflow.StateTodo, CreatedAt: now.Add(-time.Hour), Priority: &p1},
				{ID: "2", Identifier: "COLIN-2", Title: "two", StateName: workflow.StateTodo, CreatedAt: now.Add(-2 * time.Hour), Priority: &p2},
			},
		}
		orchestrator, err := New(Options{
			Tracker:    tracker,
			Configs:    &fakeConfigProvider{cfg: testConfig(now)},
			Runner:     runner,
			Workspaces: &fakeWorkspaceManager{},
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			Clock:      func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if err := orchestrator.tick(context.Background()); err != nil {
			t.Fatalf("tick() error = %v", err)
		}
		synctest.Wait()
		orchestrator.drainCompletions(testConfig(now), now)

		if len(runner.calls) != 1 {
			t.Fatalf("runner call count = %d, want 1 due to concurrency limit", len(runner.calls))
		}
		if runner.calls[0].Issue.Identifier != "COLIN-2" {
			t.Fatalf("first dispatched issue = %q, want COLIN-2", runner.calls[0].Issue.Identifier)
		}
	})
}

func TestOrchestratorFailureSchedulesPerIssueRetry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
		runner := &fakeRunner{err: errors.New("boom")}
		issue := linear.Issue{ID: "1", Identifier: "COLIN-1", Title: "one", StateName: workflow.StateInProgress}
		tracker := &fakeTracker{candidates: []linear.Issue{issue}}
		orchestrator, err := New(Options{
			Tracker:    tracker,
			Configs:    &fakeConfigProvider{cfg: testConfig(now)},
			Runner:     runner,
			Workspaces: &fakeWorkspaceManager{},
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			Clock:      func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if !orchestrator.dispatchOne(context.Background(), testConfig(now), now, issue, 0, "", false) {
			t.Fatal("dispatchOne() = false, want true")
		}
		synctest.Wait()
		orchestrator.drainCompletions(testConfig(now), now)

		snap := orchestrator.Snapshot()
		if len(snap.Retrying) != 1 {
			t.Fatalf("retry rows = %#v", snap.Retrying)
		}
		if snap.Retrying[0].Attempt != 1 {
			t.Fatalf("retry attempt = %d, want 1", snap.Retrying[0].Attempt)
		}
		if got := snap.Retrying[0].DueAt.Sub(now); got != 10*time.Second {
			t.Fatalf("retry delay = %s, want 10s", got)
		}
	})
}

func TestOrchestratorReconciliationCancelsTerminalRunAndCleansWorkspace(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
		blockCh := make(chan struct{})
		runner := &fakeRunner{blockCh: blockCh}
		issue := linear.Issue{ID: "1", Identifier: "COLIN-1", Title: "one", StateName: workflow.StateInProgress}
		tracker := &fakeTracker{
			refreshByID: map[string]linear.Issue{
				"1": {ID: "1", Identifier: "COLIN-1", Title: "one", StateName: workflow.StateDone},
			},
		}
		workspaces := &fakeWorkspaceManager{}
		orchestrator, err := New(Options{
			Tracker:    tracker,
			Configs:    &fakeConfigProvider{cfg: testConfig(now)},
			Runner:     runner,
			Workspaces: workspaces,
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			Clock:      func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if !orchestrator.dispatchOne(context.Background(), testConfig(now), now, issue, 0, "", false) {
			t.Fatal("dispatchOne() = false, want true")
		}
		synctest.Wait()
		if err := orchestrator.reconcileRunningStates(context.Background(), testConfig(now), now); err != nil {
			t.Fatalf("reconcileRunningStates() error = %v", err)
		}
		close(blockCh)
		synctest.Wait()
		orchestrator.drainCompletions(testConfig(now), now)

		if len(workspaces.removed) != 1 {
			t.Fatalf("removed workspaces = %#v", workspaces.removed)
		}
		if len(orchestrator.Snapshot().Running) != 0 {
			t.Fatalf("snapshot running = %#v", orchestrator.Snapshot().Running)
		}
	})
}
