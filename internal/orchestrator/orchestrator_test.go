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

type trackerStub struct {
	candidateIssues    []domain.Issue
	candidateCalls     int
	issuesByState      []domain.Issue
	issuesByStateCalls int
	rateLimits         map[string]any
}

func (s *trackerStub) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	s.candidateCalls++
	return s.candidateIssues, nil
}

func (s *trackerStub) FetchIssuesByStates(context.Context, []string) ([]domain.Issue, error) {
	s.issuesByStateCalls++
	return s.issuesByState, nil
}

func (s *trackerStub) FetchIssueStatesByIDs(context.Context, []string) ([]domain.Issue, error) {
	return nil, nil
}

func (s *trackerStub) UpdateIssueState(context.Context, string, string) error {
	return nil
}

func (s *trackerStub) CreateIssueComment(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *trackerStub) CreateCommentReply(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (s *trackerStub) CurrentRateLimits() map[string]any {
	return s.rateLimits
}

func TestShouldDispatchRejectsTodoBlockedByNonTerminal(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger:    slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime:   Runtime{Config: domain.ServiceConfig{Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo"}, TerminalStates: []string{"Done"}}}},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
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
		completed:   map[string]string{},
		totalTokens: domain.Totals{},
		eventCh:     make(chan any, 4),
	}

	orch.handleWorkerExit(context.Background(), workerExitedEvent{
		issueID: "1",
		result:  codex.Result{Issue: issue, RunType: codex.RunTypeCoding, Status: "succeeded"},
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

func TestHandleWorkerExitMarksReviewStateCompletedWithoutRetry(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", State: "Review"}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Repo: domain.RepoConfig{PublishStates: []string{"Review"}},
		}},
		running:   map[string]*runningEntry{"1": {issue: issue, identifier: issue.Identifier, startedAt: time.Now().Add(-2 * time.Second)}},
		claimed:   map[string]struct{}{"1": {}},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 4),
	}

	orch.handleWorkerExit(context.Background(), workerExitedEvent{
		issueID: "1",
		result:  codex.Result{Issue: issue, RunType: codex.RunTypeReviewPublish, Status: "succeeded"},
	})

	if _, ok := orch.retrying["1"]; ok {
		t.Fatal("unexpected retry entry for review handoff state")
	}
	if got := orch.completed["1"]; got != "Review" {
		t.Fatalf("completed state = %q, want %q", got, "Review")
	}
	if _, ok := orch.claimed["1"]; ok {
		t.Fatal("expected claim to be released after review handoff")
	}
}

func TestHandleWorkerExitCodingRunToReviewDoesNotMarkReviewCompleted(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", State: "Review"}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Repo:  domain.RepoConfig{PublishStates: []string{"Review"}},
			Agent: domain.AgentConfig{MaxRetryBackoff: 5 * time.Minute},
		}},
		running:   map[string]*runningEntry{"1": {issue: issue, identifier: issue.Identifier, startedAt: time.Now().Add(-2 * time.Second), comment: &commentThreadState{RunType: codex.RunTypeCoding}}},
		claimed:   map[string]struct{}{"1": {}},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 4),
	}

	orch.handleWorkerExit(context.Background(), workerExitedEvent{
		issueID: "1",
		result:  codex.Result{Issue: issue, RunType: codex.RunTypeCoding, Status: "succeeded"},
	})

	if got := orch.completed["1"]; got != "" {
		t.Fatalf("completed state = %q, want empty", got)
	}
	if _, ok := orch.retrying["1"]; !ok {
		t.Fatal("expected retry entry so review automation can dispatch next")
	}
}

func TestHandleTickDefersTrackerPollingWhenLinearBudgetIsExhausted(t *testing.T) {
	t.Parallel()

	nextAllowedAt := time.Now().UTC().Add(2 * time.Minute).Unix()
	tracker := &trackerStub{
		rateLimits: map[string]any{
			"linear_requests": map[string]any{
				"nextAllowedAt": nextAllowedAt,
			},
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: 30 * time.Second},
			Agent:   domain.AgentConfig{MaxConcurrentAgents: 1},
			Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo"}},
		}, Tracker: tracker},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 4),
	}

	orch.handleTick(context.Background())

	if tracker.issuesByStateCalls != 0 {
		t.Fatalf("FetchIssuesByStates() calls = %d, want 0", tracker.issuesByStateCalls)
	}
	if tracker.candidateCalls != 0 {
		t.Fatalf("FetchCandidateIssues() calls = %d, want 0", tracker.candidateCalls)
	}
}

func TestAppendOutputSkipsAdjacentTerminalDuplicateMessage(t *testing.T) {
	t.Parallel()

	entry := &runningEntry{}
	orch := &Orchestrator{}

	orch.appendOutput(entry, codex.Event{
		Event:     codex.EventOtherMessage,
		Timestamp: time.Date(2026, 3, 28, 12, 0, 1, 0, time.UTC),
		Message:   "Implemented the fix.",
	})
	orch.appendOutput(entry, codex.Event{
		Event:     codex.EventTurnCompleted,
		Timestamp: time.Date(2026, 3, 28, 12, 0, 2, 0, time.UTC),
		Message:   "Implemented the fix.",
	})

	if got := len(entry.outputLog); got != 1 {
		t.Fatalf("outputLog length = %d, want 1", got)
	}
	if got := entry.outputLog[0].Event; got != codex.EventOtherMessage {
		t.Fatalf("first event = %q, want %q", got, codex.EventOtherMessage)
	}
}
