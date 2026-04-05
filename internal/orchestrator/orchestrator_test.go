package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/notify"
	"github.com/pmenglund/colin/internal/repohost/builtin"
	"github.com/pmenglund/colin/internal/userworkflow"
)

func init() {
	builtin.Register()
}

type trackerStub struct {
	candidateIssues         []domain.Issue
	candidateCalls          int
	candidateInvoked        chan struct{}
	candidateCallCh         chan int
	candidateHook           func(*trackerStub)
	issuesByState           []domain.Issue
	issuesByStateCalls      int
	issuesByStateHook       func(*trackerStub)
	schedulingMetadata      map[string]domain.ColinMetadata
	schedulingMetadataCalls int
	schedulingMetadataHook  func(*trackerStub)
	issuesByID              []domain.Issue
	issuesByIDCalls         int
	updatedStates           []string
	fetchIssueByIDCalls     int
	rateLimits              domain.RateLimitSnapshot
	issueComments           []string
	commentReplies          []string
	metadata                domain.ColinMetadata
	ensuredLabels           []string
	addedLabels             []string
	removedLabels           []string
	ensureLabelErr          error
	addIssueLabelErr        error
	removeIssueLabelErr     error
}

func (s *trackerStub) FetchCandidateIssueSnapshots(context.Context) ([]domain.Issue, error) {
	s.candidateCalls++
	if s.candidateHook != nil {
		s.candidateHook(s)
	}
	if s.candidateInvoked != nil {
		select {
		case <-s.candidateInvoked:
		default:
			close(s.candidateInvoked)
		}
	}
	if s.candidateCallCh != nil {
		select {
		case s.candidateCallCh <- s.candidateCalls:
		default:
		}
	}
	return s.candidateIssues, nil
}

func (s *trackerStub) FetchIssueSnapshotsByStates(context.Context, []string) ([]domain.Issue, error) {
	s.issuesByStateCalls++
	if s.issuesByStateHook != nil {
		s.issuesByStateHook(s)
	}
	return s.issuesByState, nil
}

func (s *trackerStub) FetchIssueSchedulingMetadataByIDs(context.Context, []string) (map[string]domain.ColinMetadata, error) {
	s.schedulingMetadataCalls++
	if s.schedulingMetadataHook != nil {
		s.schedulingMetadataHook(s)
	}
	if s.schedulingMetadata == nil {
		return map[string]domain.ColinMetadata{}, nil
	}
	return maps.Clone(s.schedulingMetadata), nil
}

func (s *trackerStub) FetchIssueStatesByIDs(context.Context, []string) ([]domain.Issue, error) {
	s.issuesByIDCalls++
	return s.issuesByID, nil
}

func (s *trackerStub) FetchIssueByID(_ context.Context, issueID string) (domain.Issue, error) {
	s.fetchIssueByIDCalls++
	for _, issue := range s.issuesByID {
		if issue.ID == issueID {
			return issue, nil
		}
	}
	for _, issue := range s.candidateIssues {
		if issue.ID == issueID {
			return issue, nil
		}
	}
	for _, issue := range s.issuesByState {
		if issue.ID == issueID {
			return issue, nil
		}
	}
	return domain.Issue{}, nil
}

func (s *trackerStub) UpdateIssueState(_ context.Context, issueID string, state string) error {
	s.updatedStates = append(s.updatedStates, issueID+":"+state)
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
	if metadataClearsLoopState(metadata) && metadataTouchesCommentState(metadata) {
		metadata.LoopFailureFingerprint = s.metadata.LoopFailureFingerprint
		metadata.LoopFailureCount = s.metadata.LoopFailureCount
		metadata.PausedAt = s.metadata.PausedAt
		metadata.PausedRunType = s.metadata.PausedRunType
		metadata.PausedState = s.metadata.PausedState
		metadata.PausedReason = s.metadata.PausedReason
	}
	s.metadata = metadata
	return metadata, nil
}

func (s *trackerStub) WatchedProjectIDs() []string {
	return nil
}

func (s *trackerStub) SetUIBaseURLResolver(func(context.Context) string) {}

func metadataClearsLoopState(metadata domain.ColinMetadata) bool {
	return strings.TrimSpace(metadata.LoopFailureFingerprint) == "" &&
		metadata.LoopFailureCount == 0 &&
		metadata.PausedAt == nil &&
		strings.TrimSpace(metadata.PausedRunType) == "" &&
		strings.TrimSpace(metadata.PausedState) == "" &&
		strings.TrimSpace(metadata.PausedReason) == ""
}

func metadataTouchesCommentState(metadata domain.ColinMetadata) bool {
	return strings.TrimSpace(metadata.LastSummaryCommentID) != "" ||
		strings.TrimSpace(metadata.ProgressRootCommentID) != "" ||
		len(metadata.ColinCommentIDs) > 0 ||
		len(metadata.CodexOutput) > 0
}

func (s *trackerStub) UpsertIssueExecPlan(_ context.Context, _ string, plan domain.ExecPlan) (domain.ExecPlan, error) {
	return plan, nil
}

func TestEnterShutdownDrainClearsRetriesAndBlocksDispatch(t *testing.T) {
	t.Parallel()

	retryTimer := time.NewTimer(time.Hour)
	defer retryTimer.Stop()

	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		running: map[string]*runningEntry{},
		claimed: map[string]struct{}{
			"retry-issue": {},
		},
		retrying: map[string]*retryState{
			"retry-issue": {
				entry: domain.RetryEntry{Identifier: "COLIN-150"},
				timer: retryTimer,
			},
		},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.enterShutdownDrain()

	if !orch.draining {
		t.Fatal("draining = false, want true")
	}
	if !orch.shutdownRequested.Load() {
		t.Fatal("shutdownRequested = false, want true")
	}
	if got := len(orch.retrying); got != 0 {
		t.Fatalf("retrying count = %d, want 0", got)
	}
	if _, ok := orch.claimed["retry-issue"]; ok {
		t.Fatal("claimed retry issue should be released during shutdown drain")
	}
	if orch.shouldDispatch(domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-151",
		Title:      "blocked by shutdown drain",
		State:      "Todo",
	}) {
		t.Fatal("shouldDispatch() = true, want false while draining")
	}
}

func TestRequestShutdownDrainLatchesImmediately(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		eventCh: make(chan any, 1),
		running: map[string]*runningEntry{},
		claimed: map[string]struct{}{},
	}
	orch.loopStarted.Store(true)

	if ok := orch.RequestShutdownDrain(); !ok {
		t.Fatal("RequestShutdownDrain() = false, want true")
	}
	if !orch.shutdownRequested.Load() {
		t.Fatal("shutdownRequested = false, want true")
	}
	if orch.shouldDispatch(domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-151",
		Title:      "blocked by shutdown request",
		State:      "Todo",
	}) {
		t.Fatal("shouldDispatch() = true, want false after shutdown request")
	}
	select {
	case raw := <-orch.eventCh:
		if _, ok := raw.(shutdownDrainRequestedEvent); !ok {
			t.Fatalf("queued event = %T, want shutdownDrainRequestedEvent", raw)
		}
	default:
		t.Fatal("shutdown drain event was not queued")
	}
}

func (s *trackerStub) CurrentRateLimits() domain.RateLimitSnapshot {
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

func TestSyncIssueCodexReviewLabelKeepsCurrentLabelWhenReplacementAddFails(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{addIssueLabelErr: os.ErrPermission}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Tracker: tracker},
	}

	orch.syncIssueCodexReviewLabel(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-128",
		Labels: []string{
			domain.CodexReviewApprovedLabel,
		},
	}, domain.CodexReviewPendingLabel)

	if got, want := tracker.addedLabels, []string{"issue-1:" + domain.CodexReviewPendingLabel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("addedLabels = %v, want %v", got, want)
	}
	if len(tracker.removedLabels) != 0 {
		t.Fatalf("removedLabels = %v, want none", tracker.removedLabels)
	}
}

func TestShouldFetchCodexReviewLabelState(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		runtime: Runtime{Config: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{
				ActiveStates:   []string{"Todo", "In Progress"},
				TerminalStates: []string{"Done"},
			},
			Repo: domain.RepoConfig{
				PublishStates: []string{"Review"},
				MergeStates:   []string{"Merge"},
			},
		}},
	}

	cases := []struct {
		state string
		want  bool
	}{
		{state: "Todo", want: true},
		{state: "Review", want: true},
		{state: "Merge", want: true},
		{state: "Done", want: false},
	}

	for _, tc := range cases {
		if got := orch.shouldFetchCodexReviewLabelState(tc.state); got != tc.want {
			t.Fatalf("shouldFetchCodexReviewLabelState(%q) = %t, want %t", tc.state, got, tc.want)
		}
	}
}

func TestSyncCodexReviewLabelsClearsTerminalIssueWithoutRepoFetch(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					ActiveStates:   []string{"Todo"},
					TerminalStates: []string{"Done"},
				},
				Repo: domain.RepoConfig{
					PublishStates: []string{"Review"},
					MergeStates:   []string{"Merge"},
				},
			},
			Tracker: tracker,
		},
	}

	orch.syncCodexReviewLabels(context.Background(), []domain.Issue{
		{
			ID:         "issue-1",
			Identifier: "COLIN-128",
			State:      "Done",
			Labels: []string{
				domain.CodexReviewApprovedLabel,
				"other",
			},
			AttachedPullRequests: []domain.PullRequestRef{{Number: 42}},
		},
	})

	if len(tracker.addedLabels) != 0 {
		t.Fatalf("addedLabels = %v, want none", tracker.addedLabels)
	}
	if got, want := tracker.removedLabels, []string{"issue-1:" + domain.CodexReviewApprovedLabel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removedLabels = %v, want %v", got, want)
	}
}

func TestSyncCodexReviewLabelsStopsQuietlyWhenContextCanceled(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					ActiveStates:   []string{"Todo"},
					TerminalStates: []string{"Done"},
				},
				Repo: domain.RepoConfig{
					PublishStates: []string{"Review"},
					MergeStates:   []string{"Merge"},
				},
			},
			Tracker: tracker,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	orch.syncCodexReviewLabels(ctx, []domain.Issue{
		{
			ID:         "issue-1",
			Identifier: "COLIN-128",
			State:      "Done",
			Labels:     []string{domain.CodexReviewApprovedLabel},
		},
		{
			ID:         "issue-2",
			Identifier: "COLIN-129",
			State:      "Done",
			Labels:     []string{domain.CodexReviewPendingLabel},
		},
	})

	if len(tracker.addedLabels) != 0 {
		t.Fatalf("addedLabels = %v, want none", tracker.addedLabels)
	}
	if len(tracker.removedLabels) != 0 {
		t.Fatalf("removedLabels = %v, want none", tracker.removedLabels)
	}
}

type runnerStub struct {
	invoked chan struct{}
	release chan struct{}
	attempt *int
	issue   domain.Issue
	result  codex.Result
	runs    int
}

func (s *runnerStub) Run(_ context.Context, issue domain.Issue, attempt *int, _ func(codex.Event)) codex.Result {
	s.runs++
	s.issue = issue
	if attempt != nil {
		value := *attempt
		s.attempt = &value
	}
	if s.invoked != nil {
		select {
		case <-s.invoked:
		default:
			close(s.invoked)
		}
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

type notifierStub struct {
	err      error
	state    notify.IssueNotificationState
	calls    []notify.IssueSummary
	existing []notify.IssueNotificationState
}

func (s *notifierStub) SyncIssue(_ context.Context, summary notify.IssueSummary, existing notify.IssueNotificationState) (notify.IssueNotificationState, error) {
	s.calls = append(s.calls, summary)
	s.existing = append(s.existing, existing)
	if s.err != nil {
		return notify.IssueNotificationState{}, s.err
	}
	if strings.TrimSpace(s.state.ChannelID) == "" && strings.TrimSpace(s.state.MessageTS) == "" && strings.TrimSpace(s.state.Permalink) == "" && strings.TrimSpace(s.state.Fingerprint) == "" {
		return existing, nil
	}
	return s.state, nil
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

func TestHandleTickSyncsSlackSummaryForTrackedIssues(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.example.test/COLIN-153"
	tracker := &trackerStub{
		candidateIssues: nil,
		issuesByState: []domain.Issue{{
			ID:         "issue-1",
			Identifier: "COLIN-153",
			Title:      "Slack support",
			State:      "Review",
			URL:        &issueURL,
			ColinMetadata: &domain.ColinMetadata{
				URL: "https://colin.example.test/linear/issues/issue-1/metadata",
			},
		}},
	}
	notifier := &notifierStub{
		state: notify.IssueNotificationState{
			ChannelID:   "C12345678",
			MessageTS:   "1743630000.123456",
			Permalink:   "https://example.slack.com/archives/C12345678/p1743630000123456",
			Fingerprint: "fp-1",
		},
	}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventCh: make(chan any, 1),
		runtime: Runtime{
			Tracker:  tracker,
			Notifier: notifier,
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					Kind:           "linear",
					APIKey:         "token",
					ProjectSlug:    "project-1",
					ActiveStates:   []string{"Todo", "In Progress"},
					TerminalStates: []string{"Done"},
				},
				Repo: domain.RepoConfig{
					PublishStates: []string{"Review"},
					MergeStates:   []string{"Merge"},
				},
				Agent: domain.AgentConfig{MaxConcurrentAgents: 1},
				Codex: domain.CodexConfig{Command: "codex app-server"},
				Slack: domain.SlackConfig{
					BotToken:  "xoxb-test",
					ChannelID: "C12345678",
				},
			},
		},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.handleTick(context.Background())

	if len(notifier.calls) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(notifier.calls))
	}
	if got := tracker.metadata.SlackPermalink; got != "https://example.slack.com/archives/C12345678/p1743630000123456" {
		t.Fatalf("metadata.SlackPermalink = %q, want persisted permalink", got)
	}
}

func TestHandleTickSkipsSlackSummaryForStableTerminalIssue(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.example.test/COLIN-169"
	issue := domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-169",
		Title:      "Done issue",
		State:      "Done",
		URL:        &issueURL,
		ColinMetadata: &domain.ColinMetadata{
			SlackChannelID: "C12345678",
			SlackMessageTS: "1743630000.123456",
			SlackPermalink: "https://example.slack.com/archives/C12345678/p1743630000123456",
			SlackSummaryFingerprint: userworkflow.SlackIssueSummary(domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					ActiveStates:   []string{"Todo", "In Progress"},
					TerminalStates: []string{"Done"},
				},
				Repo: domain.RepoConfig{
					PublishStates: []string{"Review"},
					MergeStates:   []string{"Merge"},
				},
			}, domain.Issue{
				Identifier: "COLIN-169",
				Title:      "Done issue",
				State:      "Done",
				URL:        &issueURL,
			}).Fingerprint,
		},
	}
	tracker := &trackerStub{
		issuesByState: []domain.Issue{issue},
	}
	notifier := &notifierStub{
		state: notify.IssueNotificationState{
			ChannelID:   "C12345678",
			MessageTS:   "1743630000.123456",
			Permalink:   "https://example.slack.com/archives/C12345678/p1743630000123456",
			Fingerprint: issue.ColinMetadata.SlackSummaryFingerprint,
		},
	}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventCh: make(chan any, 1),
		runtime: Runtime{
			Tracker:  tracker,
			Notifier: notifier,
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					Kind:           "linear",
					APIKey:         "token",
					ProjectSlug:    "project-1",
					ActiveStates:   []string{"Todo", "In Progress"},
					TerminalStates: []string{"Done"},
				},
				Repo: domain.RepoConfig{
					PublishStates: []string{"Review"},
					MergeStates:   []string{"Merge"},
				},
				Agent: domain.AgentConfig{MaxConcurrentAgents: 1},
				Codex: domain.CodexConfig{Command: "codex app-server"},
				Slack: domain.SlackConfig{
					BotToken:  "xoxb-test",
					ChannelID: "C12345678",
				},
			},
		},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.handleTick(context.Background())

	if len(notifier.calls) != 0 {
		t.Fatalf("notifier calls = %d, want 0", len(notifier.calls))
	}
	if tracker.metadata.SlackPermalink != "" {
		t.Fatalf("tracker metadata = %#v, want no metadata upsert", tracker.metadata)
	}
}

func TestHandleTickSyncsSlackSummaryForTerminalTransition(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.example.test/COLIN-170"
	issue := domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-170",
		Title:      "Freshly done issue",
		State:      "Done",
		URL:        &issueURL,
		ColinMetadata: &domain.ColinMetadata{
			URL:                     "https://colin.example.test/linear/issues/issue-1/metadata",
			SlackChannelID:          "C12345678",
			SlackMessageTS:          "1743630000.123456",
			SlackPermalink:          "https://example.slack.com/archives/C12345678/p1743630000123456",
			SlackSummaryFingerprint: "previous-non-terminal-fingerprint",
		},
	}
	tracker := &trackerStub{
		issuesByState: []domain.Issue{issue},
	}
	notifier := &notifierStub{
		state: notify.IssueNotificationState{
			ChannelID:   "C12345678",
			MessageTS:   "1743630000.123456",
			Permalink:   "https://example.slack.com/archives/C12345678/p1743630000123456",
			Fingerprint: "terminal-fingerprint",
		},
	}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventCh: make(chan any, 1),
		runtime: Runtime{
			Tracker:  tracker,
			Notifier: notifier,
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					Kind:           "linear",
					APIKey:         "token",
					ProjectSlug:    "project-1",
					ActiveStates:   []string{"Todo", "In Progress"},
					TerminalStates: []string{"Done"},
				},
				Repo: domain.RepoConfig{
					PublishStates: []string{"Review"},
					MergeStates:   []string{"Merge"},
				},
				Agent: domain.AgentConfig{MaxConcurrentAgents: 1},
				Codex: domain.CodexConfig{Command: "codex app-server"},
				Slack: domain.SlackConfig{
					BotToken:  "xoxb-test",
					ChannelID: "C12345678",
				},
			},
		},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.handleTick(context.Background())

	if len(notifier.calls) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(notifier.calls))
	}
	if got := tracker.metadata.SlackPermalink; got != "https://example.slack.com/archives/C12345678/p1743630000123456" {
		t.Fatalf("metadata.SlackPermalink = %q, want persisted permalink", got)
	}
}

func TestSyncSlackIssueSkipsMetadataPersistWhenStateUnchanged(t *testing.T) {
	t.Parallel()

	notifier := &notifierStub{}
	issue := domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-153",
		Title:      "Slack support",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			SlackChannelID:          "C12345678",
			SlackMessageTS:          "1743630000.123456",
			SlackPermalink:          "https://example.slack.com/archives/C12345678/p1743630000123456",
			SlackSummaryFingerprint: "fp-1",
		},
	}
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{
			Tracker:  tracker,
			Notifier: notifier,
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					Kind:           "linear",
					APIKey:         "token",
					ProjectSlug:    "project-1",
					ActiveStates:   []string{"Todo"},
					TerminalStates: []string{"Done"},
				},
				Repo:  domain.RepoConfig{PublishStates: []string{"Review"}, MergeStates: []string{"Merge"}},
				Agent: domain.AgentConfig{MaxConcurrentAgents: 1},
				Codex: domain.CodexConfig{Command: "codex app-server"},
				Slack: domain.SlackConfig{BotToken: "xoxb-test", ChannelID: "C12345678"},
			},
		},
	}

	issue = orch.syncSlackIssue(context.Background(), issue)

	if len(notifier.calls) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(notifier.calls))
	}
	if tracker.metadata.SlackPermalink != "" {
		t.Fatalf("tracker metadata = %#v, want no metadata upsert", tracker.metadata)
	}
	if issue.ColinMetadata == nil || issue.ColinMetadata.SlackPermalink != "https://example.slack.com/archives/C12345678/p1743630000123456" {
		t.Fatalf("issue.ColinMetadata = %#v, want existing Slack ref preserved", issue.ColinMetadata)
	}
}

func TestMergeRunningIssueContextPreservesSlackMetadataFromPreviousIssue(t *testing.T) {
	t.Parallel()

	previous := domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-174",
		State:      "In Progress",
		ColinMetadata: &domain.ColinMetadata{
			SlackChannelID:          "C12345678",
			SlackMessageTS:          "1743630000.123456",
			SlackPermalink:          "https://example.slack.com/archives/C12345678/p1743630000123456",
			SlackSummaryFingerprint: "fp-1",
			CodexThreadID:           "thread-1",
		},
	}
	current := domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-174",
		State:      "In Progress",
		ColinMetadata: &domain.ColinMetadata{
			ProgressRootCommentID: "comment-1",
		},
	}

	merged := mergeRunningIssueContext(previous, current)

	if merged.ColinMetadata == nil {
		t.Fatal("merged.ColinMetadata = nil, want merged metadata")
	}
	if got := merged.ColinMetadata.SlackChannelID; got != "C12345678" {
		t.Fatalf("SlackChannelID = %q, want preserved value", got)
	}
	if got := merged.ColinMetadata.SlackMessageTS; got != "1743630000.123456" {
		t.Fatalf("SlackMessageTS = %q, want preserved value", got)
	}
	if got := merged.ColinMetadata.SlackPermalink; got != "https://example.slack.com/archives/C12345678/p1743630000123456" {
		t.Fatalf("SlackPermalink = %q, want preserved value", got)
	}
	if got := merged.ColinMetadata.SlackSummaryFingerprint; got != "fp-1" {
		t.Fatalf("SlackSummaryFingerprint = %q, want preserved value", got)
	}
	if got := merged.ColinMetadata.CodexThreadID; got != "thread-1" {
		t.Fatalf("CodexThreadID = %q, want preserved value", got)
	}
	if got := merged.ColinMetadata.ProgressRootCommentID; got != "comment-1" {
		t.Fatalf("ProgressRootCommentID = %q, want current value", got)
	}
}

func TestHandleTickMergesRunningSlackMetadataIntoTrackedIssueSync(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.example.test/COLIN-175"
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:           "linear",
			APIKey:         "token",
			ProjectSlug:    "project-1",
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Done"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
		Agent: domain.AgentConfig{MaxConcurrentAgents: 1},
		Codex: domain.CodexConfig{Command: "codex app-server"},
		Slack: domain.SlackConfig{
			BotToken:  "xoxb-test",
			ChannelID: "C12345678",
		},
	}
	fingerprint := userworkflow.SlackIssueSummary(cfg, domain.Issue{
		Identifier: "COLIN-175",
		Title:      "Running issue",
		State:      "In Progress",
		URL:        &issueURL,
	}).Fingerprint
	runningIssue := domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-175",
		Title:      "Running issue",
		State:      "Todo",
		URL:        &issueURL,
		ColinMetadata: &domain.ColinMetadata{
			SlackChannelID:          "C12345678",
			SlackMessageTS:          "1743630000.123456",
			SlackPermalink:          "https://example.slack.com/archives/C12345678/p1743630000123456",
			SlackSummaryFingerprint: fingerprint,
		},
	}
	tracker := &trackerStub{
		issuesByState: []domain.Issue{{
			ID:         "issue-1",
			Identifier: "COLIN-175",
			Title:      "Running issue",
			State:      "In Progress",
			URL:        &issueURL,
		}},
		issuesByID: []domain.Issue{{
			ID:         "issue-1",
			Identifier: "COLIN-175",
			Title:      "Running issue",
			State:      "In Progress",
			URL:        &issueURL,
		}},
	}
	notifier := &notifierStub{}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventCh: make(chan any, 1),
		runtime: Runtime{
			Tracker:  tracker,
			Notifier: notifier,
			Config:   cfg,
		},
		running: map[string]*runningEntry{
			"issue-1": {
				issue:      runningIssue,
				identifier: "COLIN-175",
				startedAt:  time.Now().UTC(),
				session:    domain.LiveSession{},
				cancel:     func() {},
			},
		},
		claimed:           map[string]struct{}{"issue-1": {}},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.handleTick(context.Background())

	if len(notifier.calls) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(notifier.calls))
	}
	if len(notifier.existing) != 1 {
		t.Fatalf("notifier existing calls = %d, want 1", len(notifier.existing))
	}
	if got := notifier.existing[0].MessageTS; got != "1743630000.123456" {
		t.Fatalf("existing message ts = %q, want preserved running slack reference", got)
	}
	if tracker.metadata.SlackPermalink != "" {
		t.Fatalf("tracker metadata = %#v, want no metadata upsert for unchanged running issue", tracker.metadata)
	}
}

func TestHandleTickDispatchesWhenSlackSyncFails(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-153",
		Title:      "Slack support",
		State:      "Todo",
	}
	tracker := &trackerStub{
		candidateIssues: []domain.Issue{issue},
		issuesByState:   []domain.Issue{issue},
	}
	runner := &runnerStub{
		invoked: make(chan struct{}),
		result:  codex.Result{Status: "succeeded", Issue: issue},
	}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventCh: make(chan any, 4),
		runtime: Runtime{
			Tracker:  tracker,
			Runner:   runner,
			Notifier: &notifierStub{err: errors.New("slack unavailable")},
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					Kind:           "linear",
					APIKey:         "token",
					ProjectSlug:    "project-1",
					ActiveStates:   []string{"Todo"},
					TerminalStates: []string{"Done"},
				},
				Agent: domain.AgentConfig{MaxConcurrentAgents: 1},
				Codex: domain.CodexConfig{Command: "codex app-server"},
				Slack: domain.SlackConfig{
					BotToken:  "xoxb-test",
					ChannelID: "C12345678",
				},
			},
		},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.handleTick(context.Background())

	select {
	case <-runner.invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("runner was not invoked after Slack sync failure")
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

func TestShouldDispatchRejectsSecondMergeByDefault(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Repo:  domain.RepoConfig{MergeStates: []string{"Merge"}},
			Agent: domain.AgentConfig{MaxConcurrentAgents: 4, MaxConcurrentAgentsByState: map[string]int{"merge": 1}},
		}},
		running: map[string]*runningEntry{
			"1": {
				issue:      domain.Issue{ID: "1", Identifier: "ABC-1", Title: "Already merging", State: "Merge"},
				identifier: "ABC-1",
			},
		},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	if orch.shouldDispatch(domain.Issue{
		ID:         "2",
		Identifier: "ABC-2",
		Title:      "Merge candidate",
		State:      "Merge",
	}) {
		t.Fatal("shouldDispatch() = true, want false when one merge is already running")
	}
}

func TestShouldDispatchAllowsSecondMergeWhenWorkflowOverridesLimit(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Repo:  domain.RepoConfig{MergeStates: []string{"Merge"}},
			Agent: domain.AgentConfig{MaxConcurrentAgents: 4, MaxConcurrentAgentsByState: map[string]int{"merge": 2}},
		}},
		running: map[string]*runningEntry{
			"1": {
				issue:      domain.Issue{ID: "1", Identifier: "ABC-1", Title: "Already merging", State: "Merge"},
				identifier: "ABC-1",
			},
		},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	if !orch.shouldDispatch(domain.Issue{
		ID:         "2",
		Identifier: "ABC-2",
		Title:      "Merge candidate",
		State:      "Merge",
	}) {
		t.Fatal("shouldDispatch() = false, want true when merge limit is explicitly raised")
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
		stateIssues:       map[string][]domain.StateIssueSummary{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.refreshIssueStateCounts(context.Background())

	if got := orch.issueStates["Review"]; got != 2 {
		t.Fatalf("Review count = %d, want 2", got)
	}
	if got := orch.issueStates["Todo"]; got != 1 {
		t.Fatalf("Todo count = %d, want 1", got)
	}
	if got := len(orch.stateIssues["Review"]); got != 2 {
		t.Fatalf("Review issue list length = %d, want 2", got)
	}
	if got := orch.stateIssues["Review"][0].Identifier; got != "COLIN-2" {
		t.Fatalf("first review issue = %q, want COLIN-2", got)
	}
	if got := orch.stateIssues["Review"][1].Identifier; got != "COLIN-3" {
		t.Fatalf("second review issue = %q, want COLIN-3", got)
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

func TestRefreshIssueStateCountsOmitsHiddenStateIssueLists(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{
		issuesByState: []domain.Issue{
			{ID: "1", Identifier: "COLIN-1", Title: "Ready", State: "Todo"},
			{ID: "2", Identifier: "COLIN-2", Title: "Shipped", State: "Done"},
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Tracker: tracker, Config: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{
				ActiveStates:   []string{"Todo"},
				TerminalStates: []string{"Done"},
			},
		}},
		issueStates:       map[string]int{},
		stateIssues:       map[string][]domain.StateIssueSummary{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}

	orch.refreshIssueStateCounts(context.Background())

	if got := orch.issueStates["Done"]; got != 1 {
		t.Fatalf("Done count = %d, want 1", got)
	}
	if got := len(orch.stateIssues["Todo"]); got != 1 {
		t.Fatalf("Todo issue list length = %d, want 1", got)
	}
	if _, ok := orch.stateIssues["Done"]; ok {
		t.Fatalf("unexpected Done issue list: %+v", orch.stateIssues["Done"])
	}
}

func TestHandleCodexEventUpdatesIssueCountsForObservedStateTransition(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{
				ActiveStates:   []string{"Todo", "In Progress"},
				TerminalStates: []string{"Done"},
			},
			Codex: domain.CodexConfig{Command: "codex"},
		}},
		running: map[string]*runningEntry{
			"issue-1": {
				issue: domain.Issue{
					ID:         "issue-1",
					Identifier: "COLIN-1",
					Title:      "Wake fast",
					State:      "Todo",
				},
			},
		},
		issueStates: map[string]int{
			"Todo":        1,
			"In Progress": 0,
			"Done":        0,
		},
		stateIssues: map[string][]domain.StateIssueSummary{
			"Todo": {
				{ID: "issue-1", Identifier: "COLIN-1", Title: "Wake fast"},
			},
		},
	}

	orch.handleCodexEvent(context.Background(), codex.Event{
		Event:      codex.EventIssueStateRefreshed,
		IssueID:    "issue-1",
		Identifier: "COLIN-1",
		Timestamp:  time.Now().UTC(),
		PrevState:  "Todo",
		State:      "In Progress",
	})

	if got := orch.issueStates["Todo"]; got != 0 {
		t.Fatalf("Todo count = %d, want 0", got)
	}
	if got := orch.issueStates["In Progress"]; got != 1 {
		t.Fatalf("In Progress count = %d, want 1", got)
	}
	if got := len(orch.stateIssues["Todo"]); got != 0 {
		t.Fatalf("Todo issue list length = %d, want 0", got)
	}
	if got := len(orch.stateIssues["In Progress"]); got != 1 {
		t.Fatalf("In Progress issue list length = %d, want 1", got)
	}
	if got := orch.stateIssues["In Progress"][0].Identifier; got != "COLIN-1" {
		t.Fatalf("In Progress issue identifier = %q, want COLIN-1", got)
	}
	if got := orch.running["issue-1"].issue.State; got != "In Progress" {
		t.Fatalf("running issue state = %q, want %q", got, "In Progress")
	}
}

func TestHandleCodexEventRemovesStateIssueWhenTransitionLeavesDashboardStates(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{
				ActiveStates:   []string{"Todo"},
				TerminalStates: []string{"Done"},
			},
			Codex: domain.CodexConfig{Command: "codex"},
		}},
		running: map[string]*runningEntry{
			"issue-1": {
				issue: domain.Issue{
					ID:         "issue-1",
					Identifier: "COLIN-1",
					Title:      "Ship it",
					State:      "Todo",
				},
			},
		},
		issueStates: map[string]int{
			"Todo": 1,
			"Done": 0,
		},
		stateIssues: map[string][]domain.StateIssueSummary{
			"Todo": {
				{ID: "issue-1", Identifier: "COLIN-1", Title: "Ship it"},
			},
		},
	}

	orch.handleCodexEvent(context.Background(), codex.Event{
		Event:      codex.EventIssueStateRefreshed,
		IssueID:    "issue-1",
		Identifier: "COLIN-1",
		Timestamp:  time.Now().UTC(),
		PrevState:  "Todo",
		State:      "Done",
	})

	if got := orch.issueStates["Todo"]; got != 0 {
		t.Fatalf("Todo count = %d, want 0", got)
	}
	if got := orch.issueStates["Done"]; got != 1 {
		t.Fatalf("Done count = %d, want 1", got)
	}
	if got := len(orch.stateIssues["Todo"]); got != 0 {
		t.Fatalf("Todo issue list length = %d, want 0", got)
	}
	if _, ok := orch.stateIssues["Done"]; ok {
		t.Fatalf("unexpected Done issue list: %+v", orch.stateIssues["Done"])
	}
	if got := orch.running["issue-1"].issue.State; got != "Done" {
		t.Fatalf("running issue state = %q, want %q", got, "Done")
	}
}

func TestHandleCodexEventPersistsContextWindowUsage(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		running: map[string]*runningEntry{
			"issue-1": {
				issue: domain.Issue{
					ID:         "issue-1",
					Identifier: "COLIN-1",
					Title:      "Watch context",
					State:      "In Progress",
				},
			},
		},
	}

	orch.handleCodexEvent(context.Background(), codex.Event{
		Event:         codex.EventOtherMessage,
		IssueID:       "issue-1",
		Identifier:    "COLIN-1",
		Timestamp:     time.Now().UTC(),
		ContextWindow: &domain.ContextWindowUsage{UsedTokens: 78400, LimitTokens: 258000},
	})

	got := orch.running["issue-1"].session.ContextWindow
	if got == nil {
		t.Fatal("session ContextWindow = nil, want value")
	}
	if got.UsedTokens != 78400 || got.LimitTokens != 258000 {
		t.Fatalf("session ContextWindow = %#v, want used=78400 limit=258000", got)
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

func TestSnapshotReturnsCachedSnapshotWithoutEventLoopRoundTrip(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		runtime: Runtime{Tracker: &trackerStub{}},
		issueStates: map[string]int{
			"Review": 2,
		},
		pausedIssueStates: map[string]domain.PausedStateSummary{
			"Review": {
				Count: 1,
				URL:   "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22",
			},
		},
	}
	orch.publishSnapshot(time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC))
	orch.loopStarted.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	snapshot, err := orch.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot() error = %v, want nil", err)
	}
	if got := snapshot.IssueStates["Review"]; got != 2 {
		t.Fatalf("Snapshot().IssueStates[Review] = %d, want 2", got)
	}
	if got := snapshot.PausedIssueStates["Review"].Count; got != 1 {
		t.Fatalf("Snapshot().PausedIssueStates[Review].Count = %d, want 1", got)
	}
}

func TestSnapshotReflectsShutdownRequest(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		runtime: Runtime{Tracker: &trackerStub{}},
	}
	orch.publishSnapshot(time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC))
	orch.loopStarted.Store(true)
	orch.shutdownRequested.Store(true)

	snapshot, err := orch.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v, want nil", err)
	}
	if !snapshot.ShutdownRequested {
		t.Fatal("Snapshot().ShutdownRequested = false, want true")
	}
}

func TestSubscribeSnapshotUpdatesReceivesPublishedSnapshots(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		runtime:           Runtime{Tracker: &trackerStub{}},
		subscribers:       map[uint64]chan domain.SnapshotUpdate{},
		issueStates:       map[string]int{"Review": 2},
		stateIssues:       map[string][]domain.StateIssueSummary{},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates := orch.SubscribeSnapshotUpdates(ctx)
	firstTime := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	secondTime := firstTime.Add(2 * time.Second)
	orch.publishSnapshot(firstTime)

	first := <-updates
	orch.publishSnapshot(secondTime)
	second := <-updates
	if first.Sequence != 1 || !first.GeneratedAt.Equal(firstTime) {
		t.Fatalf("first update = %#v, want sequence 1 at %s", first, firstTime)
	}
	if second.Sequence != 2 || !second.GeneratedAt.Equal(secondTime) {
		t.Fatalf("second update = %#v, want sequence 2 at %s", second, secondTime)
	}

	first.GeneratedAt = first.GeneratedAt.Add(-time.Hour)
	latest := orch.LatestSnapshotUpdate()
	if latest.Sequence != 2 || !latest.GeneratedAt.Equal(secondTime) {
		t.Fatalf("LatestSnapshotUpdate() = %#v, want latest published value", latest)
	}
}

func TestSubscribeSnapshotUpdatesCoalescesSlowSubscribers(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		runtime:           Runtime{Tracker: &trackerStub{}},
		subscribers:       map[uint64]chan domain.SnapshotUpdate{},
		issueStates:       map[string]int{},
		stateIssues:       map[string][]domain.StateIssueSummary{},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates := orch.SubscribeSnapshotUpdates(ctx)
	firstTime := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	secondTime := firstTime.Add(time.Second)
	orch.publishSnapshot(firstTime)
	orch.publishSnapshot(secondTime)

	update := <-updates
	if update.Sequence != 2 || !update.GeneratedAt.Equal(secondTime) {
		t.Fatalf("coalesced update = %#v, want latest published value", update)
	}
}

func TestSnapshotClonesCachedSnapshot(t *testing.T) {
	t.Parallel()

	lastEventAt := time.Date(2026, 3, 30, 12, 1, 0, 0, time.UTC)
	url := "https://linear.app/example/issue/COLIN-2/waiting-on-review"
	orch := &Orchestrator{
		runtime: Runtime{Tracker: &trackerStub{}},
		running: map[string]*runningEntry{
			"issue-1": {
				issue: domain.Issue{
					ID:         "issue-1",
					Identifier: "COLIN-2",
					Title:      "Review issue",
					URL:        &url,
					State:      "Review",
				},
				identifier: "COLIN-2",
				startedAt:  time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC),
				session:    domain.LiveSession{ContextWindow: &domain.ContextWindowUsage{UsedTokens: 78400, LimitTokens: 258000}},
				outputLog: []domain.OutputLog{{
					Timestamp: time.Date(2026, 3, 30, 12, 0, 30, 0, time.UTC),
					Event:     "other_message",
					Message:   "hello",
				}},
			},
		},
		issueStates: map[string]int{"Review": 1},
		stateIssues: map[string][]domain.StateIssueSummary{
			"Review": {
				{ID: "issue-1", Identifier: "COLIN-2", Title: "Review issue", URL: url},
			},
		},
	}
	orch.running["issue-1"].session.LastCodexTimestamp = &lastEventAt
	orch.publishSnapshot(time.Date(2026, 3, 30, 12, 2, 0, 0, time.UTC))
	orch.loopStarted.Store(true)

	first, err := orch.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v, want nil", err)
	}
	first.Running[0].Identifier = "MUTATED"
	*first.Running[0].URL = "https://example.invalid"
	*first.Running[0].LastEventAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	first.Running[0].ContextWindow.UsedTokens = 999
	first.Running[0].ContextWindow.LimitTokens = 1000
	first.Running[0].OutputLog[0].Message = "changed"
	first.IssueStates["Review"] = 99
	first.StateIssues["Review"][0].Identifier = "MUTATED"

	second, err := orch.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() second error = %v, want nil", err)
	}
	if got := second.Running[0].Identifier; got != "COLIN-2" {
		t.Fatalf("cached Identifier = %q, want COLIN-2", got)
	}
	if got := *second.Running[0].URL; got != url {
		t.Fatalf("cached URL = %q, want %q", got, url)
	}
	if got := *second.Running[0].LastEventAt; !got.Equal(lastEventAt) {
		t.Fatalf("cached LastEventAt = %v, want %v", got, lastEventAt)
	}
	if got := second.Running[0].ContextWindow; got == nil {
		t.Fatal("cached ContextWindow = nil, want value")
	} else if got.UsedTokens != 78400 || got.LimitTokens != 258000 {
		t.Fatalf("cached ContextWindow = %#v, want used=78400 limit=258000", got)
	}
	if got := second.Running[0].OutputLog[0].Message; got != "hello" {
		t.Fatalf("cached OutputLog message = %q, want hello", got)
	}
	if got := second.IssueStates["Review"]; got != 1 {
		t.Fatalf("cached IssueStates[Review] = %d, want 1", got)
	}
	if got := second.StateIssues["Review"][0].Identifier; got != "COLIN-2" {
		t.Fatalf("cached StateIssues[Review][0].Identifier = %q, want COLIN-2", got)
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

func TestHandleRetryDropsClaimedIssueAfterShutdownRequest(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		running:  map[string]*runningEntry{},
		retrying: map[string]*retryState{},
		claimed: map[string]struct{}{
			"issue-1": {},
		},
	}
	orch.shutdownRequested.Store(true)
	orch.retrying["issue-1"] = &retryState{
		entry: domain.RetryEntry{
			IssueID:    "issue-1",
			Identifier: "COLIN-151",
			Attempt:    2,
		},
	}

	orch.handleRetry(context.Background(), "issue-1")

	if _, ok := orch.retrying["issue-1"]; ok {
		t.Fatal("retrying entry still present after shutdown request")
	}
	if _, ok := orch.claimed["issue-1"]; ok {
		t.Fatal("claimed issue should be released after shutdown request")
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
			Summary: userworkflow.MergeReturnedToReview(domain.PullRequestRef{}, 0),
		},
	})

	if len(tracker.commentReplies) != 1 {
		t.Fatalf("commentReplies length = %d, want 1", len(tracker.commentReplies))
	}
	if !strings.Contains(tracker.commentReplies[0], "[colin] Returning issue to `Review` because Codex PR feedback still needs to be resolved.") {
		t.Fatalf("first comment reply = %q", tracker.commentReplies[0])
	}
	if !strings.Contains(tracker.commentReplies[0], "What you should do: resolve the remaining Codex PR feedback, then move the issue back to `Merge`.") {
		t.Fatalf("first comment reply = %q, want human guidance", tracker.commentReplies[0])
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
			Summary: userworkflow.MergeReturnedToReview(domain.PullRequestRef{}, 0),
		},
	})

	if got := len(tracker.issueComments); got != 1 {
		t.Fatalf("issueComments length = %d, want 1", got)
	}
	if !strings.Contains(tracker.issueComments[0], "[colin] Returning issue to `Review` because Codex PR feedback still needs to be resolved.") {
		t.Fatalf("issue comment = %q", tracker.issueComments[0])
	}
	if !strings.Contains(tracker.issueComments[0], "What you should do: resolve the remaining Codex PR feedback, then move the issue back to `Merge`.") {
		t.Fatalf("issue comment = %q, want human guidance", tracker.issueComments[0])
	}
	if got := len(tracker.commentReplies); got != 0 {
		t.Fatalf("commentReplies length = %d, want 0", got)
	}
	if got := orch.completed["1"]; got != "Review" {
		t.Fatalf("completed state = %q, want %q", got, "Review")
	}
}

func TestHandleWorkerExitMergeBlockedInMergeSchedulesHiddenRetry(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{ID: "1", Identifier: "ABC-1", State: "Merge"}
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
				issue:      issue,
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
			Status:  "blocked",
			Summary: userworkflow.MergeWaitingForReview(domain.PullRequestRef{}, false, false),
		},
	})

	retry := orch.retrying["1"]
	if retry == nil {
		t.Fatal("expected retry entry for blocked merge handoff")
	}
	retry.timer.Stop()
	if retry.notifyLinear {
		t.Fatal("same-state handoff retry should be hidden from Linear comments")
	}
	if got := orch.completed["1"]; got != "" {
		t.Fatalf("completed state = %q, want empty", got)
	}
	if got := len(tracker.commentReplies); got != 1 {
		t.Fatalf("commentReplies length = %d, want 1", got)
	}
	if !strings.Contains(tracker.commentReplies[0], "[colin] Keeping issue in `Merge` while waiting for Codex PR review feedback.") {
		t.Fatalf("summary comment = %q", tracker.commentReplies[0])
	}
	if !strings.Contains(tracker.commentReplies[0], "What Colin is doing next: retrying merge automation automatically after the Codex review state changes.") {
		t.Fatalf("summary comment = %q, want automatic-retry guidance", tracker.commentReplies[0])
	}
}

func TestPostRunSummaryPreservesMultilineEvidence(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Tracker: tracker},
	}
	entry := &runningEntry{
		identifier: "COLIN-149",
		comment:    &commentThreadState{RunType: codex.RunTypeCoding, RootCommentID: "root"},
	}

	summary := "What changed: tightened the review summary format.\n\nBefore: completion comments were generic.\nAfter: completion comments explain the change in before/after terms.\nVerification: go test ./internal/automation ./internal/orchestrator"
	orch.postRunSummary(context.Background(), entry, domain.Issue{ID: "issue-1", Identifier: "COLIN-149"}, summary)

	if len(tracker.commentReplies) != 1 {
		t.Fatalf("commentReplies length = %d, want 1", len(tracker.commentReplies))
	}
	if got := tracker.commentReplies[0]; got != "[colin] "+summary {
		t.Fatalf("summary comment = %q", got)
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

func TestHandleWorkerExitTerminalStopClearsPersistentThreadMetadata(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{}
	issue := domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		State:      "Merged",
		ColinMetadata: &domain.ColinMetadata{
			CodexThreadID:         "thread-1",
			ProgressRootCommentID: "root-1",
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{
			Config:  domain.ServiceConfig{Tracker: domain.TrackerConfig{TerminalStates: []string{"Merged"}}},
			Tracker: tracker,
		},
		running:   map[string]*runningEntry{"1": {issue: issue, identifier: issue.Identifier, startedAt: time.Now().Add(-2 * time.Second), stopReason: "terminal"}},
		claimed:   map[string]struct{}{"1": {}},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
		eventCh:   make(chan any, 4),
	}

	orch.handleWorkerExit(context.Background(), workerExitedEvent{
		issueID: "1",
		result: codex.Result{
			Issue:   domain.Issue{ID: "1", Identifier: "ABC-1", State: "Merged"},
			RunType: codex.RunTypeMerge,
			Status:  "failed",
		},
	})

	if tracker.metadata.CodexThreadID != "" {
		t.Fatalf("metadata.CodexThreadID = %q, want cleared", tracker.metadata.CodexThreadID)
	}
	if tracker.metadata.ProgressRootCommentID != "" {
		t.Fatalf("metadata.ProgressRootCommentID = %q, want cleared", tracker.metadata.ProgressRootCommentID)
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
		rateLimits: domain.RateLimitSnapshot{
			"linear_requests": {
				NextAllowedAt: unixTimePtr(nextAllowedAt),
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

func TestHandleTickDefersTrackerPollingWhenLinearBudgetHitsHardReserve(t *testing.T) {
	t.Parallel()

	nextAllowedAt := time.Now().UTC().Add(2 * time.Minute).Unix()
	tracker := &trackerStub{
		rateLimits: domain.RateLimitSnapshot{
			"linear_requests": {
				Remaining:     int64Ptr(100),
				Limit:         int64Ptr(5000),
				NextAllowedAt: unixTimePtr(nextAllowedAt),
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

func TestHandleTickAllowsCandidateFetchAboveSoftReserveDespiteNextAllowedAt(t *testing.T) {
	t.Parallel()

	nextAllowedAt := time.Now().UTC().Add(2 * time.Minute).Unix()
	issue := domain.Issue{ID: "1", Identifier: "COLIN-143", Title: "github interface", State: "Todo"}
	tracker := &trackerStub{
		candidateIssues: []domain.Issue{issue},
		rateLimits: domain.RateLimitSnapshot{
			"linear_requests": {
				Remaining:     int64Ptr(4999),
				Limit:         int64Ptr(5000),
				NextAllowedAt: unixTimePtr(nextAllowedAt),
			},
		},
	}
	runner := &runnerStub{invoked: make(chan struct{})}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: 30 * time.Second},
			Agent:   domain.AgentConfig{MaxConcurrentAgents: 1},
			Tracker: domain.TrackerConfig{
				Kind:         "linear",
				APIKey:       "test-key",
				ProjectSlug:  "test-project",
				ActiveStates: []string{"Todo"},
			},
			Codex: domain.CodexConfig{Command: "codex"},
		}, Tracker: tracker, Runner: runner},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
		eventCh:           make(chan any, 4),
	}

	orch.handleTick(context.Background())

	if tracker.candidateCalls != 1 {
		t.Fatalf("FetchCandidateIssueSnapshots() calls = %d, want 1", tracker.candidateCalls)
	}
	if tracker.fetchIssueByIDCalls != 1 {
		t.Fatalf("FetchIssueByID() calls = %d, want 1", tracker.fetchIssueByIDCalls)
	}
	select {
	case <-runner.invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("runner was not invoked for eligible Todo issue")
	}
	if _, ok := orch.running["1"]; !ok {
		t.Fatal("expected issue to be marked running after dispatch")
	}
}

func TestHandleTickRefreshesStateCountsAfterCandidateFetchUsesBudget(t *testing.T) {
	t.Parallel()

	nextAllowedAt := time.Now().UTC().Add(2 * time.Minute).Unix()
	issue := domain.Issue{ID: "1", Identifier: "COLIN-143", Title: "github interface", State: "Todo"}
	tracker := &trackerStub{
		candidateIssues: []domain.Issue{},
		issuesByState:   []domain.Issue{issue},
		candidateHook: func(s *trackerStub) {
			s.rateLimits = domain.RateLimitSnapshot{
				"linear_requests": {
					Remaining:     int64Ptr(4999),
					Limit:         int64Ptr(5000),
					NextAllowedAt: unixTimePtr(nextAllowedAt),
				},
			}
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: 30 * time.Second},
			Agent:   domain.AgentConfig{MaxConcurrentAgents: 1},
			Tracker: domain.TrackerConfig{
				Kind:         "linear",
				APIKey:       "test-key",
				ProjectSlug:  "test-project",
				ActiveStates: []string{"Todo"},
			},
			Codex: domain.CodexConfig{Command: "codex"},
		}, Tracker: tracker},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
		eventCh:           make(chan any, 4),
	}

	orch.handleTick(context.Background())

	if tracker.candidateCalls != 1 {
		t.Fatalf("FetchCandidateIssueSnapshots() calls = %d, want 1", tracker.candidateCalls)
	}
	if tracker.issuesByStateCalls != 1 {
		t.Fatalf("FetchIssueSnapshotsByStates() calls = %d, want 1", tracker.issuesByStateCalls)
	}
	if tracker.schedulingMetadataCalls != 1 {
		t.Fatalf("FetchIssueSchedulingMetadataByIDs() calls = %d, want 1", tracker.schedulingMetadataCalls)
	}
	if tracker.fetchIssueByIDCalls != 0 {
		t.Fatalf("FetchIssueByID() calls = %d, want 0", tracker.fetchIssueByIDCalls)
	}
	if got := orch.issueStates["Todo"]; got != 1 {
		t.Fatalf("issueStates[Todo] = %d, want 1", got)
	}
}

func TestHandleTickSkipsStateCountRefreshWithinSoftReserve(t *testing.T) {
	t.Parallel()

	nextAllowedAt := time.Now().UTC().Add(2 * time.Minute).Unix()
	issue := domain.Issue{ID: "1", Identifier: "COLIN-143", Title: "github interface", State: "Todo"}
	tracker := &trackerStub{
		candidateIssues: []domain.Issue{},
		issuesByState:   []domain.Issue{issue},
		candidateHook: func(s *trackerStub) {
			s.rateLimits = domain.RateLimitSnapshot{
				"linear_requests": {
					Remaining:     int64Ptr(200),
					Limit:         int64Ptr(5000),
					NextAllowedAt: unixTimePtr(nextAllowedAt),
				},
			}
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: 30 * time.Second},
			Agent:   domain.AgentConfig{MaxConcurrentAgents: 1},
			Tracker: domain.TrackerConfig{
				Kind:         "linear",
				APIKey:       "test-key",
				ProjectSlug:  "test-project",
				ActiveStates: []string{"Todo"},
			},
			Codex: domain.CodexConfig{Command: "codex"},
		}, Tracker: tracker},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{"Todo": 7},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
		eventCh:           make(chan any, 4),
	}

	orch.handleTick(context.Background())

	if tracker.candidateCalls != 1 {
		t.Fatalf("FetchCandidateIssues() calls = %d, want 1", tracker.candidateCalls)
	}
	if tracker.issuesByStateCalls != 0 {
		t.Fatalf("FetchIssuesByStates() calls = %d, want 0", tracker.issuesByStateCalls)
	}
	if got := orch.issueStates["Todo"]; got != 7 {
		t.Fatalf("issueStates[Todo] = %d, want preserved value 7", got)
	}
}

func TestReconcileRunningAllowsStateRefreshAboveSoftReserveDespiteNextAllowedAt(t *testing.T) {
	t.Parallel()

	nextAllowedAt := time.Now().UTC().Add(2 * time.Minute).Unix()
	tracker := &trackerStub{
		issuesByID: []domain.Issue{{ID: "1", Identifier: "COLIN-143", Title: "github interface", State: "In Progress"}},
		rateLimits: domain.RateLimitSnapshot{
			"linear_requests": {
				Remaining:     int64Ptr(4999),
				Limit:         int64Ptr(5000),
				NextAllowedAt: unixTimePtr(nextAllowedAt),
			},
		},
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{ActiveStates: []string{"In Progress"}},
		}, Tracker: tracker},
		running: map[string]*runningEntry{
			"1": {
				issue:      domain.Issue{ID: "1", Identifier: "COLIN-143", Title: "github interface", State: "In Progress"},
				identifier: "COLIN-143",
				startedAt:  time.Now().Add(-time.Minute),
			},
		},
		claimed: map[string]struct{}{"1": {}},
	}

	orch.reconcileRunning(context.Background())

	if tracker.issuesByIDCalls != 1 {
		t.Fatalf("FetchIssueStatesByIDs() calls = %d, want 1", tracker.issuesByIDCalls)
	}
}

func TestHandleRetryAllowsDispatchAboveSoftReserveDespiteNextAllowedAt(t *testing.T) {
	t.Parallel()

	nextAllowedAt := time.Now().UTC().Add(2 * time.Minute).Unix()
	issue := domain.Issue{ID: "1", Identifier: "ABC-1", Title: "Retry me", State: "Review"}
	tracker := &trackerStub{
		candidateIssues: []domain.Issue{issue},
		rateLimits: domain.RateLimitSnapshot{
			"linear_requests": {
				Remaining:     int64Ptr(4999),
				Limit:         int64Ptr(5000),
				NextAllowedAt: unixTimePtr(nextAllowedAt),
			},
		},
	}
	runner := &runnerStub{
		invoked: make(chan struct{}),
		release: make(chan struct{}),
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
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
}

func TestRequestRefreshCoalescesPendingRequests(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventCh: make(chan any, 1),
	}
	orch.refreshReady.Store(true)

	queued, coalesced := orch.RequestRefresh("first")
	if !queued || coalesced {
		t.Fatalf("first RequestRefresh() = (%t, %t), want (true, false)", queued, coalesced)
	}

	queued, coalesced = orch.RequestRefresh("second")
	if !queued || !coalesced {
		t.Fatalf("second RequestRefresh() = (%t, %t), want (true, true)", queued, coalesced)
	}

	select {
	case raw := <-orch.eventCh:
		event, ok := raw.(refreshRequestedEvent)
		if !ok {
			t.Fatalf("event type = %T, want refreshRequestedEvent", raw)
		}
		if event.reason != "first" {
			t.Fatalf("event reason = %q, want %q", event.reason, "first")
		}
	default:
		t.Fatal("expected queued refresh event")
	}

	if !orch.refreshPending.Load() {
		t.Fatal("refreshPending = false, want true until the refresh event is processed")
	}
}

func TestRequestRefreshBeforeLoopReadyDoesNotQueue(t *testing.T) {
	t.Parallel()

	orch := &Orchestrator{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventCh: make(chan any, 1),
	}

	queued, coalesced := orch.RequestRefresh("before-start")
	if queued || coalesced {
		t.Fatalf("RequestRefresh() = (%t, %t), want (false, false)", queued, coalesced)
	}
	if orch.RefreshReady() {
		t.Fatal("RefreshReady() = true, want false")
	}
	select {
	case raw := <-orch.eventCh:
		t.Fatalf("unexpected queued event: %T", raw)
	default:
	}
}

func TestRunFetchesCandidatesImmediatelyBeforeFirstPollInterval(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{
		candidateInvoked: make(chan struct{}),
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: time.Hour},
			Agent:   domain.AgentConfig{MaxConcurrentAgents: 1},
			Tracker: domain.TrackerConfig{
				Kind:        "linear",
				APIKey:      "test-key",
				ProjectSlug: "test-project",
				ActiveStates: []string{
					"Todo",
				},
			},
			Codex: domain.CodexConfig{Command: "codex"},
		}, Tracker: tracker},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
		eventCh:           make(chan any, 4),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- orch.Run(ctx)
	}()

	select {
	case <-tracker.candidateInvoked:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("FetchCandidateIssues was not called immediately at startup")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}

	if tracker.candidateCalls != 1 {
		t.Fatalf("FetchCandidateIssues() calls = %d, want 1", tracker.candidateCalls)
	}
}

func TestRunDoesNotDoubleFetchWhenRefreshWasRequestedBeforeLoopReady(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{
		candidateInvoked: make(chan struct{}),
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: time.Hour},
			Agent:   domain.AgentConfig{MaxConcurrentAgents: 1},
			Tracker: domain.TrackerConfig{
				Kind:         "linear",
				APIKey:       "test-key",
				ProjectSlug:  "test-project",
				ActiveStates: []string{"Todo"},
			},
			Codex: domain.CodexConfig{Command: "codex"},
		}, Tracker: tracker},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
		eventCh:           make(chan any, 4),
	}

	queued, coalesced := orch.RequestRefresh("before-start")
	if queued || coalesced {
		t.Fatalf("RequestRefresh() = (%t, %t), want (false, false)", queued, coalesced)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- orch.Run(ctx)
	}()

	select {
	case <-tracker.candidateInvoked:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("FetchCandidateIssues was not called immediately at startup")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}

	if tracker.candidateCalls != 1 {
		t.Fatalf("FetchCandidateIssues() calls = %d, want 1", tracker.candidateCalls)
	}
}

func TestRunProcessesRefreshRequestsImmediately(t *testing.T) {
	t.Parallel()

	tracker := &trackerStub{
		candidateCallCh: make(chan int, 4),
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: time.Hour},
			Agent:   domain.AgentConfig{MaxConcurrentAgents: 1},
			Tracker: domain.TrackerConfig{
				Kind:         "linear",
				APIKey:       "test-key",
				ProjectSlug:  "test-project",
				ActiveStates: []string{"Todo"},
			},
			Codex: domain.CodexConfig{Command: "codex"},
		}, Tracker: tracker},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
		eventCh:           make(chan any, 4),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- orch.Run(ctx)
	}()

	select {
	case got := <-tracker.candidateCallCh:
		if got != 1 {
			t.Fatalf("first candidate call = %d, want 1", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("startup candidate fetch did not happen")
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for !orch.RefreshReady() {
		if time.Now().After(deadline) {
			t.Fatal("RefreshReady() did not become true after startup tick")
		}
		time.Sleep(5 * time.Millisecond)
	}

	queued, coalesced := orch.RequestRefresh("test refresh")
	if !queued || coalesced {
		t.Fatalf("RequestRefresh() = (%t, %t), want (true, false)", queued, coalesced)
	}

	select {
	case got := <-tracker.candidateCallCh:
		if got != 2 {
			t.Fatalf("second candidate call = %d, want 2", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("refresh-triggered candidate fetch did not happen")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func TestRefreshDoesNotDispatchDuplicateWorkerForClaimedIssue(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{
		ID:         "1",
		Identifier: "ABC-1",
		Title:      "Wake quickly",
		State:      "Todo",
	}
	tracker := &trackerStub{
		candidateIssues: []domain.Issue{issue},
		candidateCallCh: make(chan int, 4),
	}
	runner := &runnerStub{
		invoked: make(chan struct{}),
		release: make(chan struct{}),
	}
	orch := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtime: Runtime{
			Config: domain.ServiceConfig{
				Polling: domain.PollingConfig{Interval: time.Hour},
				Agent:   domain.AgentConfig{MaxConcurrentAgents: 1},
				Tracker: domain.TrackerConfig{
					Kind:         "linear",
					APIKey:       "test-key",
					ProjectSlug:  "test-project",
					ActiveStates: []string{"Todo"},
				},
				Repo:  domain.RepoConfig{Backend: "github"},
				Codex: domain.CodexConfig{Command: "codex"},
			},
			Tracker: tracker,
			Runner:  runner,
		},
		running:           map[string]*runningEntry{},
		claimed:           map[string]struct{}{},
		retrying:          map[string]*retryState{},
		reviewSync:        map[string]*reviewSyncState{},
		completed:         map[string]string{},
		issueStates:       map[string]int{},
		pausedIssueStates: map[string]domain.PausedStateSummary{},
		eventCh:           make(chan any, 8),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- orch.Run(ctx)
	}()

	select {
	case <-runner.invoked:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("initial dispatch did not start")
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for !orch.RefreshReady() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !orch.RefreshReady() {
		t.Fatal("orchestrator did not become refresh-ready")
	}

	queued, coalesced := orch.RequestRefresh("webhook update")
	if !queued || coalesced {
		t.Fatalf("RequestRefresh() = (%t, %t), want (true, false)", queued, coalesced)
	}

	select {
	case got := <-tracker.candidateCallCh:
		if got != 1 {
			t.Fatalf("first candidate call = %d, want 1", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("startup candidate fetch did not happen")
	}

	select {
	case got := <-tracker.candidateCallCh:
		if got != 2 {
			t.Fatalf("second candidate call = %d, want 2", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("refresh-triggered candidate fetch did not happen")
	}

	if runner.runs != 1 {
		t.Fatalf("runner runs = %d, want 1", runner.runs)
	}
	if len(orch.running) != 1 {
		t.Fatalf("running count = %d, want 1", len(orch.running))
	}

	close(runner.release)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
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

func unixTimePtr(value int64) *time.Time {
	parsed := time.Unix(value, 0).UTC()
	return &parsed
}

func int64Ptr(value int64) *int64 {
	return &value
}
