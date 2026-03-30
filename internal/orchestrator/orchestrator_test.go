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
	issuesByID         []domain.Issue
	rateLimits         map[string]any
	issueComments      []string
	commentReplies     []string
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
	return s.issuesByID, nil
}

func (s *trackerStub) FetchIssueByID(_ context.Context, issueID string) (domain.Issue, error) {
	for _, issue := range s.issuesByID {
		if issue.ID == issueID {
			return issue, nil
		}
	}
	return domain.Issue{}, nil
}

func (s *trackerStub) UpdateIssueState(context.Context, string, string) error {
	return nil
}

func (s *trackerStub) ResolveGitAutomationState(context.Context, string, string, string) (string, bool, error) {
	return "", false, nil
}

func (s *trackerStub) CreateIssueComment(_ context.Context, _ string, body string) (string, error) {
	s.issueComments = append(s.issueComments, body)
	return "root", nil
}

func (s *trackerStub) CreateCommentReply(_ context.Context, _ string, _ string, body string) (string, error) {
	s.commentReplies = append(s.commentReplies, body)
	return "reply", nil
}

func (s *trackerStub) UpsertIssueMetadata(_ context.Context, _ string, metadata domain.ColinMetadata) (domain.ColinMetadata, error) {
	return metadata, nil
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

func TestShouldDispatchRejectsRefine(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Repo: domain.RepoConfig{PublishStates: []string{"Review"}},
		}},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	if orch.shouldDispatch(domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		Title:      "Needs more detail",
		State:      "Refine",
	}) {
		t.Fatal("shouldDispatch() = true, want false")
	}
}

func TestHandleWorkerExitSchedulesContinuationRetry(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", State: "In Progress"}
	orch := &Orchestrator{
		logger:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime:     Runtime{Config: domain.ServiceConfig{Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo", "In Progress"}}, Agent: domain.AgentConfig{MaxRetryBackoff: 5 * time.Minute}}},
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

func TestHandleWorkerExitMergeBlockedBackToReviewPostsSummary(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", State: "Review"}
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Repo: domain.RepoConfig{PublishStates: []string{"Review"}, MergeStates: []string{"Merge"}},
			},
			Tracker: tracker,
		},
		running: map[string]*runningEntry{
			"1": {
				issue:      domain.Issue{ID: "1", Identifier: "ABC-1", State: "Merge"},
				identifier: issue.Identifier,
				startedAt:  time.Now().Add(-2 * time.Second),
				comment:    &commentThreadState{RunType: codex.RunTypeMerge, RootCommentID: "root"},
			},
		},
		claimed:   map[string]struct{}{"1": {}},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 4),
	}

	orch.handleWorkerExit(context.Background(), workerExitedEvent{
		issueID: "1",
		result: codex.Result{
			Issue:   issue,
			RunType: codex.RunTypeMerge,
			Status:  "succeeded",
			Summary: "Returning issue to `Review` because Codex PR feedback still needs to be resolved.",
		},
	})

	if len(tracker.commentReplies) != 2 {
		t.Fatalf("commentReplies length = %d, want 2", len(tracker.commentReplies))
	}
	if tracker.commentReplies[0] != "[colin] Returning issue to `Review` because Codex PR feedback still needs to be resolved." {
		t.Fatalf("first comment reply = %q", tracker.commentReplies[0])
	}
	if got := orch.completed["1"]; got != "Review" {
		t.Fatalf("completed state = %q, want %q", got, "Review")
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

func TestHandleWorkerExitCodingRunToReviewHidesVerificationRetryComments(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", State: "Review"}
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Repo:  domain.RepoConfig{PublishStates: []string{"Review"}},
				Agent: domain.AgentConfig{MaxRetryBackoff: 5 * time.Minute},
			},
			Tracker: tracker,
		},
		running: map[string]*runningEntry{"1": {
			issue:      issue,
			identifier: issue.Identifier,
			startedAt:  time.Now().Add(-2 * time.Second),
			comment:    &commentThreadState{RunType: codex.RunTypeCoding, RootCommentID: "root"},
		}},
		claimed:   map[string]struct{}{"1": {}},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 4),
	}

	orch.handleWorkerExit(context.Background(), workerExitedEvent{
		issueID: "1",
		result:  codex.Result{Issue: issue, RunType: codex.RunTypeCoding, Status: "succeeded"},
	})

	retry := orch.retrying["1"]
	if retry == nil {
		t.Fatal("expected retry entry so review automation can dispatch next")
	}
	if retry.notifyLinear {
		t.Fatal("verification retry should be hidden from Linear comments")
	}
	if got := len(tracker.commentReplies); got != 0 {
		t.Fatalf("commentReplies length = %d, want 0", got)
	}
}

func TestHandleWorkerExitCodingRunToRefineMarksCompletedWithoutRetry(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		State:      "Refine",
	}
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

	if _, ok := orch.retrying["1"]; ok {
		t.Fatal("unexpected retry entry for refine handoff state")
	}
	if got := orch.completed["1"]; got != "Refine" {
		t.Fatalf("completed state = %q, want %q", got, "Refine")
	}
	if _, ok := orch.claimed["1"]; ok {
		t.Fatal("expected claim to be released after refine handoff")
	}
}

func TestVisibleRetryPostsScheduledAndFiredComments(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", Title: "Review", State: "Review"}
	tracker := &trackerStub{
		candidateIssues: []domain.Issue{issue},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Repo:  domain.RepoConfig{PublishStates: []string{"Review"}},
				Agent: domain.AgentConfig{MaxRetryBackoff: 5 * time.Minute, MaxConcurrentAgents: 1},
			},
			Tracker: tracker,
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	orch.scheduleRetry("1", issue.Identifier, 1, "worker stalled", time.Second, &commentThreadState{RunType: codex.RunTypeCoding, RootCommentID: "root"}, true)

	retry := orch.retrying["1"]
	if retry == nil {
		t.Fatal("expected retry entry")
	}
	retry.timer.Stop()
	if !retry.notifyLinear {
		t.Fatal("visible retry should notify Linear")
	}
	if got := len(tracker.commentReplies); got != 1 {
		t.Fatalf("commentReplies length after schedule = %d, want 1", got)
	}

	orch.handleRetry(context.Background(), "1")

	if got := len(tracker.commentReplies); got != 2 {
		t.Fatalf("commentReplies length after fire = %d, want 2", got)
	}
	if tracker.commentReplies[0] != "[colin] Colin scheduled retry attempt `1` in `1s`.\n\n- Reason: worker stalled" {
		t.Fatalf("scheduled retry comment = %q", tracker.commentReplies[0])
	}
	if tracker.commentReplies[1] != "[colin] Colin is starting retry attempt `1`." {
		t.Fatalf("fired retry comment = %q", tracker.commentReplies[1])
	}
}

func TestHiddenRetryRemainsHiddenWhenDeferredByLinearBudget(t *testing.T) {
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
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Agent: domain.AgentConfig{MaxRetryBackoff: 5 * time.Minute},
			},
			Tracker: tracker,
		},
		claimed:   map[string]struct{}{"1": {}},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	orch.retrying["1"] = &retryState{
		entry: domain.RetryEntry{
			IssueID:    "1",
			Identifier: "ABC-1",
			Attempt:    1,
			DueAt:      time.Now().UTC(),
		},
		timer:        time.NewTimer(time.Hour),
		comment:      &commentThreadState{RunType: codex.RunTypeCoding, RootCommentID: "root"},
		notifyLinear: false,
	}
	defer orch.retrying["1"].timer.Stop()

	orch.handleRetry(context.Background(), "1")

	retry := orch.retrying["1"]
	if retry == nil {
		t.Fatal("expected retry entry to be rescheduled")
	}
	retry.timer.Stop()
	if retry.notifyLinear {
		t.Fatal("hidden retry should remain hidden after Linear budget deferral")
	}
	if got := len(tracker.commentReplies); got != 0 {
		t.Fatalf("commentReplies length = %d, want 0", got)
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

func TestReconcileRunningKeepsPublishAutomationRunningInReview(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", Title: "Review", State: "Review"}
	tracker := &trackerStub{
		issuesByID: []domain.Issue{issue},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{TerminalStates: []string{"Done"}},
				Repo:    domain.RepoConfig{PublishStates: []string{"Review"}},
			},
			Tracker: tracker,
		},
		running: map[string]*runningEntry{
			"1": {
				issue:      issue,
				identifier: issue.Identifier,
				runType:    codex.RunTypeReviewPublish,
				startedAt:  time.Now().Add(-time.Second),
				cancel:     func() {},
			},
		},
		claimed: map[string]struct{}{"1": {}},
	}

	orch.reconcileRunning(context.Background())

	entry := orch.running["1"]
	if entry == nil {
		t.Fatal("running entry removed unexpectedly")
	}
	if entry.stopReason != "" {
		t.Fatalf("stopReason = %q, want empty", entry.stopReason)
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
