package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
)

type trackerStub struct {
	candidateIssues     []domain.Issue
	candidateCalls      int
	issuesByState       []domain.Issue
	issuesByStateCalls  int
	issuesByID          []domain.Issue
	rateLimits          map[string]any
	issueComments       []string
	commentReplies      []string
	metadata            domain.ColinMetadata
	ensuredLabels       []string
	addedLabels         []string
	removedLabels       []string
	ensureLabelErr      error
	addIssueLabelErr    error
	removeIssueLabelErr error
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

func (s *trackerStub) EnsureIssueLabel(_ context.Context, labelName string) error {
	s.ensuredLabels = append(s.ensuredLabels, labelName)
	return s.ensureLabelErr
}

func (s *trackerStub) AddIssueLabel(_ context.Context, issueID string, labelName string) error {
	s.addedLabels = append(s.addedLabels, issueID+":"+labelName)
	return s.addIssueLabelErr
}

func (s *trackerStub) RemoveIssueLabel(_ context.Context, issueID string, labelName string) error {
	s.removedLabels = append(s.removedLabels, issueID+":"+labelName)
	return s.removeIssueLabelErr
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
	s.metadata = metadata
	return metadata, nil
}

func (s *trackerStub) UpsertIssueExecPlan(_ context.Context, _ string, plan domain.ExecPlan) (domain.ExecPlan, error) {
	return plan, nil
}

func (s *trackerStub) CurrentRateLimits() map[string]any {
	return s.rateLimits
}

func TestSyncIssueCodexReviewLabelAddsDesiredAndRemovesOthers(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Tracker: tracker},
	}

	orch.syncIssueCodexReviewLabel(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-128",
		Labels: []string{
			domain.CodexReviewApprovedLabel,
			domain.CodexReviewUnresolvedLabel,
			"e2e",
		},
	}, domain.CodexReviewPendingLabel)

	if len(tracker.addedLabels) != 1 || tracker.addedLabels[0] != "issue-1:"+domain.CodexReviewPendingLabel {
		t.Fatalf("addedLabels = %v, want pending label", tracker.addedLabels)
	}
	if got, want := tracker.removedLabels, []string{
		"issue-1:" + domain.CodexReviewApprovedLabel,
		"issue-1:" + domain.CodexReviewUnresolvedLabel,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removedLabels = %v, want %v", got, want)
	}
}

func TestSyncIssueCodexReviewLabelClearsManagedLabelsWhenNoStateWanted(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Tracker: tracker},
	}

	orch.syncIssueCodexReviewLabel(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-128",
		Labels: []string{
			domain.CodexReviewPendingLabel,
			domain.CodexReviewApprovedLabel,
		},
	}, "")

	if len(tracker.addedLabels) != 0 {
		t.Fatalf("addedLabels = %v, want none", tracker.addedLabels)
	}
	if got, want := tracker.removedLabels, []string{
		"issue-1:" + domain.CodexReviewPendingLabel,
		"issue-1:" + domain.CodexReviewApprovedLabel,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removedLabels = %v, want %v", got, want)
	}
}

type runnerStub struct {
	invoked chan struct{}
	release chan struct{}
	attempt *int
	issue   domain.Issue
	result  codex.Result
}

func (s *runnerStub) Run(_ context.Context, issue domain.Issue, attempt *int, _ func(codex.Event)) codex.Result {
	s.issue = issue
	if attempt != nil {
		value := *attempt
		s.attempt = &value
	}
	if s.invoked != nil {
		close(s.invoked)
	}
	if s.release != nil {
		<-s.release
	}
	result := s.result
	if strings.TrimSpace(result.Issue.ID) == "" {
		result.Issue = issue
	}
	return result
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

func TestShouldDispatchRejectsPausedLabel(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo"}},
		}},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	if orch.shouldDispatch(domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		Title:      "Paused",
		State:      "Todo",
		Labels:     []string{domain.PausedIssueLabel},
	}) {
		t.Fatal("shouldDispatch() = true, want false")
	}
}

func TestRefreshIssueStateCountsTracksPausedIssuesByState(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{
		issuesByState: []domain.Issue{
			{ID: "1", Identifier: "COLIN-1", Title: "Ready", State: "Todo"},
			{
				ID:         "2",
				Identifier: "COLIN-2",
				Title:      "Waiting on review",
				State:      "Review",
				Labels:     []string{domain.PausedIssueLabel},
				URL:        stringPtr("https://linear.app/example/issue/COLIN-2/waiting-on-review"),
			},
			{
				ID:         "3",
				Identifier: "COLIN-3",
				Title:      "Needs human follow-up",
				State:      "Review",
				Labels:     []string{domain.PausedIssueLabel},
				URL:        stringPtr("https://linear.app/example/issue/COLIN-3/needs-human-follow-up"),
			},
			{ID: "4", Identifier: "COLIN-4", Title: "Shipping", State: "Merge"},
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Tracker: tracker, Config: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{
				ActiveStates:   []string{"Todo", "Review", "Merge"},
				TerminalStates: []string{"Done"},
			},
		}},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.refreshIssueStateCounts(context.Background())

	if got := orch.issueStates["Review"]; got != 2 {
		t.Fatalf("Review count = %d, want 2", got)
	}
	if got := orch.issueStates["Todo"]; got != 1 {
		t.Fatalf("Todo count = %d, want 1", got)
	}
	summary, ok := orch.pausedIssueStates["Review"]
	if !ok {
		t.Fatal("paused review summary missing")
	}
	if summary.Count != 2 {
		t.Fatalf("Review paused count = %d, want 2", summary.Count)
	}
	if summary.URL != "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22" {
		t.Fatalf("Review paused url = %q", summary.URL)
	}
	if _, ok := orch.pausedIssueStates["Todo"]; ok {
		t.Fatalf("unexpected paused summary for Todo: %+v", orch.pausedIssueStates["Todo"])
	}
}

func TestSnapshotClonesPausedIssueStates(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		runtime: Runtime{Tracker: &trackerStub{}},
		pausedIssueStates: map[string]domain.PausedStateSummary{
			"Review": {
				Count: 1,
				URL:   "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22",
			},
		},
		issueStates: map[string]int{"Review": 2},
	}

	snapshot := orch.snapshotAt(time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC))
	snapshot.PausedIssueStates["Review"] = domain.PausedStateSummary{Count: 99, URL: "https://example.invalid"}

	if got := orch.pausedIssueStates["Review"].Count; got != 1 {
		t.Fatalf("orchestrator paused count = %d, want 1", got)
	}
	if got := orch.pausedIssueStates["Review"].URL; got != "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22" {
		t.Fatalf("orchestrator paused url = %q", got)
	}
}

func TestBuildPausedIssueSearchURL(t *testing.T) {
	t.Parallel()

	got := buildPausedIssueSearchURL("https://linear.app/example/issue/COLIN-2/waiting-on-review", "Review")
	want := "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22"
	if got != want {
		t.Fatalf("buildPausedIssueSearchURL() = %q, want %q", got, want)
	}

	if got := buildPausedIssueSearchURL("not a url", "Review"); got != "" {
		t.Fatalf("buildPausedIssueSearchURL() malformed = %q, want empty", got)
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

func TestHandleWorkerExitReviewPublishToActiveStateDoesNotMarkCompleted(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	issue := domain.Issue{ID: "1", Identifier: "ABC-1", State: "Review"}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo", "In Progress"}},
				Repo:    domain.RepoConfig{PublishStates: []string{"Review"}},
			},
			Tracker: tracker,
		},
		running: map[string]*runningEntry{
			"1": {
				issue:      issue,
				identifier: issue.Identifier,
				startedAt:  time.Now().Add(-2 * time.Second),
				comment:    &commentThreadState{RunType: codex.RunTypeReviewPublish, RootCommentID: "root"},
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
			Issue:   domain.Issue{ID: "1", Identifier: "ABC-1", State: "In Progress"},
			RunType: codex.RunTypeReviewPublish,
			Status:  "succeeded",
			Summary: "Colin did not find reviewable repository changes, so it moved the issue back to `In Progress` instead of opening a PR.",
		},
	})

	if got := orch.completed["1"]; got != "" {
		t.Fatalf("completed state = %q, want empty", got)
	}
	if _, ok := orch.claimed["1"]; ok {
		t.Fatal("expected claim to be released after active hand-back")
	}
	if _, ok := orch.retrying["1"]; ok {
		t.Fatal("unexpected retry entry after active hand-back")
	}
	if len(tracker.commentReplies) == 0 {
		t.Fatal("expected summary comment reply for active hand-back")
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

	if len(tracker.commentReplies) != 1 {
		t.Fatalf("commentReplies length = %d, want 1", len(tracker.commentReplies))
	}
	if tracker.commentReplies[0] != "[colin] Returning issue to `Review` because Codex PR feedback still needs to be resolved." {
		t.Fatalf("first comment reply = %q", tracker.commentReplies[0])
	}
	if got := orch.completed["1"]; got != "Review" {
		t.Fatalf("completed state = %q, want %q", got, "Review")
	}
}

func TestHandleWorkerExitMergeBlockedBackToReviewCreatesIssueCommentWhenRootIsMissing(t *testing.T) {
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
				comment:    &commentThreadState{RunType: codex.RunTypeMerge},
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

	if got := len(tracker.issueComments); got != 1 {
		t.Fatalf("issueComments length = %d, want 1", got)
	}
	if tracker.issueComments[0] != "[colin] Returning issue to `Review` because Codex PR feedback still needs to be resolved." {
		t.Fatalf("issue comment = %q", tracker.issueComments[0])
	}
	if got := len(tracker.commentReplies); got != 0 {
		t.Fatalf("commentReplies length = %d, want 0", got)
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
	runner := &runnerStub{}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Repo:  domain.RepoConfig{PublishStates: []string{"Review"}},
				Agent: domain.AgentConfig{MaxRetryBackoff: 5 * time.Minute, MaxConcurrentAgents: 1},
			},
			Tracker: tracker,
			Runner:  runner,
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 1),
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

func TestHandleRetryDispatchesClaimedRetryIssue(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", Title: "Retry me", State: "Review"}
	tracker := &trackerStub{
		candidateIssues: []domain.Issue{issue},
	}
	runner := &runnerStub{
		invoked: make(chan struct{}),
		release: make(chan struct{}),
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Repo:  domain.RepoConfig{PublishStates: []string{"Review"}},
				Agent: domain.AgentConfig{MaxConcurrentAgents: 1},
			},
			Tracker: tracker,
			Runner:  runner,
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{"1": {}},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 1),
	}

	orch.retrying["1"] = &retryState{
		entry: domain.RetryEntry{
			IssueID:    "1",
			Identifier: issue.Identifier,
			Attempt:    1,
			DueAt:      time.Now().UTC(),
		},
		timer:   time.NewTimer(time.Hour),
		comment: &commentThreadState{RunType: codex.RunTypeReviewPublish},
	}
	defer orch.retrying["1"].timer.Stop()

	orch.handleRetry(context.Background(), "1")

	select {
	case <-runner.invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("runner was not invoked")
	}
	close(runner.release)

	if runner.attempt == nil || *runner.attempt != 1 {
		t.Fatalf("retry attempt = %v, want 1", runner.attempt)
	}
	if runner.issue.ID != "1" {
		t.Fatalf("runner issue id = %q, want %q", runner.issue.ID, "1")
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

func TestHandleWorkerExitPausesAfterRepeatedIdenticalFailures(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	issue := domain.Issue{ID: "1", Identifier: "ABC-1", Title: "Review", State: "Review"}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Repo: domain.RepoConfig{PublishStates: []string{"Review"}},
			},
			Tracker: tracker,
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 4),
	}

	for i := 0; i < loopFailureThreshold; i++ {
		current := issue
		if i > 0 {
			metadata := tracker.metadata
			current.ColinMetadata = &metadata
		}
		delete(orch.retrying, "1")
		orch.running["1"] = &runningEntry{
			issue:      current,
			identifier: current.Identifier,
			runType:    codex.RunTypeReviewPublish,
			startedAt:  time.Now().Add(-time.Second),
			comment:    &commentThreadState{RunType: codex.RunTypeReviewPublish, RootCommentID: "root"},
		}
		orch.claimed["1"] = struct{}{}

		orch.handleWorkerExit(context.Background(), workerExitedEvent{
			issueID: "1",
			result: codex.Result{
				Issue:   current,
				RunType: codex.RunTypeReviewPublish,
				Status:  "failed",
				Err:     context.DeadlineExceeded,
			},
		})
	}

	if got := tracker.metadata.LoopFailureCount; got != loopFailureThreshold {
		t.Fatalf("LoopFailureCount = %d, want %d", got, loopFailureThreshold)
	}
	if tracker.metadata.PausedAt == nil {
		t.Fatal("PausedAt = nil, want timestamp")
	}
	if tracker.metadata.PausedRunType != codex.RunTypeReviewPublish {
		t.Fatalf("PausedRunType = %q, want %q", tracker.metadata.PausedRunType, codex.RunTypeReviewPublish)
	}
	if len(tracker.ensuredLabels) == 0 || tracker.ensuredLabels[len(tracker.ensuredLabels)-1] != domain.PausedIssueLabel {
		t.Fatalf("ensuredLabels = %v, want paused", tracker.ensuredLabels)
	}
	if len(tracker.addedLabels) == 0 || tracker.addedLabels[len(tracker.addedLabels)-1] != "1:"+domain.PausedIssueLabel {
		t.Fatalf("addedLabels = %v, want issue paused label", tracker.addedLabels)
	}
	if _, ok := orch.retrying["1"]; ok {
		t.Fatal("unexpected retry after pause")
	}
	if _, ok := orch.claimed["1"]; ok {
		t.Fatal("expected claim to be released after pause")
	}
	if got := tracker.commentReplies[len(tracker.commentReplies)-1]; !strings.Contains(got, "added the `paused` label") {
		t.Fatalf("last comment = %q, want pause summary", got)
	}
}

func TestHandleWorkerExitResetsFailureStreakOnDifferentError(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	issue := domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		Title:      "Retry me",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			LoopFailureFingerprint: buildLoopFailureFingerprint(codex.RunTypeReviewPublish, "Review", "first failure"),
			LoopFailureCount:       2,
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config:  domain.ServiceConfig{Repo: domain.RepoConfig{PublishStates: []string{"Review"}}, Agent: domain.AgentConfig{MaxRetryBackoff: time.Minute}},
			Tracker: tracker,
			Runner:  nil,
		},
		running: map[string]*runningEntry{
			"1": {
				issue:      issue,
				identifier: issue.Identifier,
				runType:    codex.RunTypeReviewPublish,
				startedAt:  time.Now().Add(-time.Second),
				comment:    &commentThreadState{RunType: codex.RunTypeReviewPublish, RootCommentID: "root"},
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
			RunType: codex.RunTypeReviewPublish,
			Status:  "failed",
			Err:     os.ErrPermission,
		},
	})

	if got := tracker.metadata.LoopFailureCount; got != 1 {
		t.Fatalf("LoopFailureCount = %d, want 1", got)
	}
	if tracker.metadata.PausedAt != nil {
		t.Fatal("PausedAt != nil, want nil")
	}
}

func TestHandleWorkerExitClearsFailureStreakOnSuccess(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	metadata := domain.ColinMetadata{
		LoopFailureFingerprint: "fp",
		LoopFailureCount:       2,
		PausedRunType:          codex.RunTypeCoding,
		PausedState:            "In Progress",
		PausedReason:           "boom",
	}
	now := time.Now().UTC()
	metadata.PausedAt = &now
	issue := domain.Issue{
		ID:            "1",
		Identifier:    "ABC-1",
		Title:         "Success",
		State:         "Review",
		ColinMetadata: &metadata,
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config:  domain.ServiceConfig{Repo: domain.RepoConfig{PublishStates: []string{"Review"}}},
			Tracker: tracker,
		},
		running: map[string]*runningEntry{
			"1": {
				issue:      issue,
				identifier: issue.Identifier,
				runType:    codex.RunTypeReviewPublish,
				startedAt:  time.Now().Add(-time.Second),
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
			RunType: codex.RunTypeReviewPublish,
			Status:  "succeeded",
		},
	})

	if tracker.metadata.LoopFailureCount != 0 {
		t.Fatalf("LoopFailureCount = %d, want 0", tracker.metadata.LoopFailureCount)
	}
	if tracker.metadata.LoopFailureFingerprint != "" {
		t.Fatalf("LoopFailureFingerprint = %q, want empty", tracker.metadata.LoopFailureFingerprint)
	}
	if tracker.metadata.PausedAt != nil {
		t.Fatal("PausedAt != nil, want nil")
	}
}

func TestHandleWorkerExitKeepsRetryingWhenPauseLabelFails(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{addIssueLabelErr: os.ErrPermission}
	issue := domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		Title:      "Review",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			LoopFailureFingerprint: buildLoopFailureFingerprint(codex.RunTypeReviewPublish, "Review", "permission denied"),
			LoopFailureCount:       loopFailureThreshold - 1,
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Config:  domain.ServiceConfig{Repo: domain.RepoConfig{PublishStates: []string{"Review"}}, Agent: domain.AgentConfig{MaxRetryBackoff: time.Minute}},
			Tracker: tracker,
		},
		running: map[string]*runningEntry{
			"1": {
				issue:      issue,
				identifier: issue.Identifier,
				runType:    codex.RunTypeReviewPublish,
				startedAt:  time.Now().Add(-time.Second),
				comment:    &commentThreadState{RunType: codex.RunTypeReviewPublish, RootCommentID: "root"},
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
			RunType: codex.RunTypeReviewPublish,
			Status:  "failed",
			Err:     os.ErrPermission,
		},
	})

	if _, ok := orch.retrying["1"]; !ok {
		t.Fatal("expected retry when paused label application fails")
	}
	if tracker.metadata.PausedAt != nil {
		t.Fatal("PausedAt != nil, want nil when label apply fails")
	}
}

func TestClearPausedLoopMetadataIfUnpausedClearsStoredPauseState(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	now := time.Now().UTC()
	issue := domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		Title:      "Unpaused",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			LoopFailureFingerprint: "fp",
			LoopFailureCount:       3,
			PausedAt:               &now,
			PausedRunType:          codex.RunTypeReviewPublish,
			PausedState:            "Review",
			PausedReason:           "failed repeatedly",
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{
			Tracker: tracker,
		},
	}

	updated := orch.clearPausedLoopMetadataIfUnpaused(context.Background(), issue)

	if updated.ColinMetadata == nil {
		t.Fatal("updated.ColinMetadata = nil")
	}
	if updated.ColinMetadata.LoopFailureCount != 0 {
		t.Fatalf("LoopFailureCount = %d, want 0", updated.ColinMetadata.LoopFailureCount)
	}
	if updated.ColinMetadata.PausedAt != nil {
		t.Fatal("PausedAt != nil, want nil")
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

func stringPtr(value string) *string {
	return &value
}
