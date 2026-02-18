package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/linear/linearfakes"
	"github.com/pmenglund/colin/internal/workflow"
)

type fakeClientState struct {
	mu                       sync.Mutex
	issues                   map[string]linear.Issue
	stateUpdates             int
	metadataUpdates          int
	comments                 map[string][]string
	conflictOnNextStateWrite bool
}

func newFakeLinearClient(state *fakeClientState) *linearfakes.FakeClient {
	fake := &linearfakes.FakeClient{}

	fake.ListCandidateIssuesCalls(func(_ context.Context, _ string) ([]linear.Issue, error) {
		state.mu.Lock()
		defer state.mu.Unlock()

		out := make([]linear.Issue, 0, len(state.issues))
		for _, issue := range state.issues {
			if issue.StateName == workflow.StateTodo || issue.StateName == workflow.StateInProgress || issue.StateName == workflow.StateMerge {
				out = append(out, cloneIssue(issue))
			}
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].ID < out[j].ID
		})
		return out, nil
	})

	fake.GetIssueCalls(func(_ context.Context, issueID string) (linear.Issue, error) {
		state.mu.Lock()
		defer state.mu.Unlock()

		issue, ok := state.issues[issueID]
		if !ok {
			return linear.Issue{}, fmt.Errorf("issue %s not found", issueID)
		}
		return cloneIssue(issue), nil
	})

	fake.UpdateIssueStateCalls(func(_ context.Context, issueID string, toState string) error {
		state.mu.Lock()
		defer state.mu.Unlock()

		if state.conflictOnNextStateWrite {
			state.conflictOnNextStateWrite = false
			return linear.ErrConflict
		}
		issue, ok := state.issues[issueID]
		if !ok {
			return fmt.Errorf("issue %s not found", issueID)
		}
		issue.StateName = toState
		state.issues[issueID] = issue
		state.stateUpdates++
		return nil
	})

	fake.UpdateIssueMetadataCalls(func(_ context.Context, issueID string, patch linear.MetadataPatch) error {
		state.mu.Lock()
		defer state.mu.Unlock()

		issue, ok := state.issues[issueID]
		if !ok {
			return fmt.Errorf("issue %s not found", issueID)
		}
		if issue.Metadata == nil {
			issue.Metadata = map[string]string{}
		}
		for k, v := range patch.Set {
			issue.Metadata[k] = v
		}
		for _, k := range patch.Delete {
			delete(issue.Metadata, k)
		}
		state.issues[issueID] = issue
		state.metadataUpdates++
		return nil
	})

	fake.CreateIssueCommentCalls(func(_ context.Context, issueID string, body string) error {
		state.mu.Lock()
		defer state.mu.Unlock()

		if state.comments == nil {
			state.comments = map[string][]string{}
		}
		state.comments[issueID] = append(state.comments[issueID], body)
		return nil
	})

	return fake
}

func cloneIssue(issue linear.Issue) linear.Issue {
	out := issue
	out.Metadata = map[string]string{}
	for k, v := range issue.Metadata {
		out.Metadata[k] = v
	}
	return out
}

type fakeInProgressExecutor struct {
	result    InProgressExecutionResult
	err       error
	callCnt   int
	lastIssue linear.Issue
}

func (f *fakeInProgressExecutor) EvaluateAndExecute(_ context.Context, issue linear.Issue) (InProgressExecutionResult, error) {
	f.callCnt++
	f.lastIssue = issue
	if f.err != nil {
		return InProgressExecutionResult{}, f.err
	}
	return f.result, nil
}

type blockingInProgressExecutor struct {
	entered chan string
	release chan struct{}
	result  InProgressExecutionResult
}

func (b *blockingInProgressExecutor) EvaluateAndExecute(_ context.Context, issue linear.Issue) (InProgressExecutionResult, error) {
	b.entered <- issue.ID
	<-b.release
	return b.result, nil
}

type fakeTaskBootstrapper struct {
	callCnt   int
	lastIssue string
	result    TaskBootstrapResult
	err       error
}

func (f *fakeTaskBootstrapper) EnsureTaskWorkspace(_ context.Context, issueIdentifier string) (TaskBootstrapResult, error) {
	f.callCnt++
	f.lastIssue = issueIdentifier
	if f.err != nil {
		return TaskBootstrapResult{}, f.err
	}
	return f.result, nil
}

type fakeBranchMetadataStore struct {
	getSessionID string
	getErr       error
	setErr       error
	setCalls     int
	lastBranch   string
	lastSession  string
}

func (f *fakeBranchMetadataStore) GetBranchSessionID(_ context.Context, _ string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	return strings.TrimSpace(f.getSessionID), nil
}

func (f *fakeBranchMetadataStore) SetBranchSessionID(_ context.Context, branchName string, sessionID string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.setCalls++
	f.lastBranch = strings.TrimSpace(branchName)
	f.lastSession = strings.TrimSpace(sessionID)
	return nil
}

func TestRunnerValidate(t *testing.T) {
	t.Parallel()

	validRunner := func() Runner {
		return Runner{
			Linear:   newFakeLinearClient(&fakeClientState{}),
			WorkerID: "worker-1",
			LeaseTTL: time.Minute,
		}
	}

	tests := []struct {
		name   string
		mutate func(*Runner)
		want   string
	}{
		{
			name: "missing linear client",
			mutate: func(r *Runner) {
				r.Linear = nil
			},
			want: "runner linear client is required",
		},
		{
			name: "missing worker id",
			mutate: func(r *Runner) {
				r.WorkerID = ""
			},
			want: "runner worker id is required",
		},
		{
			name: "non-positive lease ttl",
			mutate: func(r *Runner) {
				r.LeaseTTL = 0
			},
			want: "runner lease ttl must be positive",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := validRunner()
			tc.mutate(&r)

			err := r.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("Validate() error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func TestRunnerRunOnceIsIdempotentForTodoClaim(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateTodo,
				Description: "spec present",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)

	r := Runner{
		Linear:    client,
		TeamID:    "team-1",
		WorkerID:  "worker-1",
		LeaseTTL:  5 * time.Minute,
		Clock:     func() time.Time { return now },
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollEvery: time.Second,
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}

	issue := state.issues["1"]
	if issue.StateName != workflow.StateInProgress {
		t.Fatalf("StateName = %q", issue.StateName)
	}
	if state.stateUpdates != 1 {
		t.Fatalf("stateUpdates = %d, want 1", state.stateUpdates)
	}
}

func TestRunnerRunOnceBootstrapsTodoTransition(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateTodo,
				Description: "spec present",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	bootstrapper := &fakeTaskBootstrapper{
		result: TaskBootstrapResult{
			WorktreePath: "/tmp/colin/worktrees/COL-1",
			BranchName:   "colin/COL-1",
		},
	}

	r := Runner{
		Linear:       client,
		Bootstrapper: bootstrapper,
		TeamID:       "team-1",
		WorkerID:     "worker-1",
		LeaseTTL:     5 * time.Minute,
		Clock:        func() time.Time { return now },
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollEvery:    time.Second,
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if bootstrapper.callCnt != 1 {
		t.Fatalf("bootstrapper call count = %d, want 1", bootstrapper.callCnt)
	}
	if bootstrapper.lastIssue != "COL-1" {
		t.Fatalf("bootstrapper last issue = %q, want %q", bootstrapper.lastIssue, "COL-1")
	}
	issue := state.issues["1"]
	if got := issue.Metadata[workflow.MetaWorktreePath]; got != "/tmp/colin/worktrees/COL-1" {
		t.Fatalf("Metadata[%s] = %q", workflow.MetaWorktreePath, got)
	}
	if got := issue.Metadata[workflow.MetaBranchName]; got != "colin/COL-1" {
		t.Fatalf("Metadata[%s] = %q", workflow.MetaBranchName, got)
	}
}

func TestRunnerRunOnceBootstrapFailureIsActionableAndRecoverable(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateTodo,
				Description: "spec present",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	bootstrapper := &fakeTaskBootstrapper{
		err: fmt.Errorf("create worktree %q from %q: %w", "/tmp/colin/worktrees/COL-1", "main", errors.New("fatal git error")),
	}

	r := Runner{
		Linear:       client,
		Bootstrapper: bootstrapper,
		TeamID:       "team-1",
		WorkerID:     "worker-1",
		LeaseTTL:     5 * time.Minute,
		Clock:        func() time.Time { return now },
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollEvery:    time.Second,
	}

	err := r.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want bootstrap error")
	}
	if !strings.Contains(err.Error(), "bootstrap workspace for issue COL-1") {
		t.Fatalf("error = %q, want bootstrap context", err.Error())
	}
	if got := state.issues["1"].StateName; got != workflow.StateTodo {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateTodo)
	}
	if state.metadataUpdates != 0 {
		t.Fatalf("metadataUpdates = %d, want 0", state.metadataUpdates)
	}
	if state.stateUpdates != 0 {
		t.Fatalf("stateUpdates = %d, want 0", state.stateUpdates)
	}
}

func TestRunnerRunOnceBootstrapsTodoTransitionBackfillsSessionFromBranchMetadata(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateTodo,
				Description: "spec present",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	bootstrapper := &fakeTaskBootstrapper{
		result: TaskBootstrapResult{
			WorktreePath: "/tmp/colin/worktrees/COL-1",
			BranchName:   "colin/COL-1",
		},
	}
	branchMetadata := &fakeBranchMetadataStore{
		getSessionID: "sess-from-git",
	}

	r := Runner{
		Linear:         client,
		Bootstrapper:   bootstrapper,
		BranchMetadata: branchMetadata,
		TeamID:         "team-1",
		WorkerID:       "worker-1",
		LeaseTTL:       5 * time.Minute,
		Clock:          func() time.Time { return now },
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollEvery:      time.Second,
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	issue := state.issues["1"]
	if got := issue.Metadata[workflow.MetaCodexSessionID]; got != "sess-from-git" {
		t.Fatalf("Metadata[%s] = %q", workflow.MetaCodexSessionID, got)
	}
	if branchMetadata.setCalls != 1 {
		t.Fatalf("branch metadata set calls = %d, want 1", branchMetadata.setCalls)
	}
	if branchMetadata.lastBranch != "colin/COL-1" {
		t.Fatalf("branch metadata branch = %q, want %q", branchMetadata.lastBranch, "colin/COL-1")
	}
	if branchMetadata.lastSession != "sess-from-git" {
		t.Fatalf("branch metadata session = %q, want %q", branchMetadata.lastSession, "sess-from-git")
	}
}

func TestRunnerTodoWithoutSpecMovesToRefine(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:         "1",
				Identifier: "COL-1",
				StateName:  workflow.StateTodo,
				Metadata:   map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)

	r := Runner{
		Linear:   client,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    time.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if got := state.issues["1"].StateName; got != workflow.StateRefine {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateRefine)
	}
}

func TestRunnerRespectsActiveLeaseFromOtherWorker(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateTodo,
				Description: "spec present",
				Metadata: map[string]string{
					workflow.MetaLeaseOwner:        "worker-2",
					workflow.MetaLeaseExpiresAtUTC: now.Add(time.Minute).Format(time.RFC3339),
				},
			},
		},
	}
	client := newFakeLinearClient(state)

	r := Runner{
		Linear:   client,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    func() time.Time { return now },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if state.stateUpdates != 0 {
		t.Fatalf("stateUpdates = %d, want 0", state.stateUpdates)
	}
}

func TestRunnerHandlesConflictWithoutFailingCycle(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateTodo,
				Description: "spec present",
				Metadata:    map[string]string{},
			},
		},
		conflictOnNextStateWrite: true,
	}
	client := newFakeLinearClient(state)

	r := Runner{
		Linear:   client,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    time.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if state.stateUpdates != 0 {
		t.Fatalf("stateUpdates = %d, want 0", state.stateUpdates)
	}
}

func TestRunnerMergeSuccessMovesIssueToDone(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateMerge,
				Description: "ready to merge",
				Metadata: map[string]string{
					workflow.MetaMergeReady: "true",
				},
			},
		},
	}
	client := newFakeLinearClient(state)
	mergeExecutor := &fakeMergeExecutor{}

	r := Runner{
		Linear:        client,
		MergeExecutor: mergeExecutor,
		TeamID:        "team-1",
		WorkerID:      "worker-1",
		LeaseTTL:      5 * time.Minute,
		Clock:         func() time.Time { return now },
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if mergeExecutor.callCnt != 1 {
		t.Fatalf("merge executor call count = %d, want 1", mergeExecutor.callCnt)
	}
	if mergeExecutor.lastIssue.ID != "1" {
		t.Fatalf("merge executor issue id = %q, want %q", mergeExecutor.lastIssue.ID, "1")
	}
	if got := state.issues["1"].StateName; got != workflow.StateDone {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateDone)
	}
	if got := state.issues["1"].Metadata[workflow.MetaMergeReady]; got != "false" {
		t.Fatalf("Metadata[%s] = %q, want %q", workflow.MetaMergeReady, got, "false")
	}
}

func TestRunnerMergeFailureLeavesIssueInMerge(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateMerge,
				Description: "ready to merge",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	mergeExecutor := &fakeMergeExecutor{
		errs: []error{errors.New("push failed")},
	}

	r := Runner{
		Linear:        client,
		MergeExecutor: mergeExecutor,
		TeamID:        "team-1",
		WorkerID:      "worker-1",
		LeaseTTL:      5 * time.Minute,
		Clock:         time.Now,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := r.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want merge execution error")
	}
	if !strings.Contains(err.Error(), "execute merge for issue COL-1") {
		t.Fatalf("error = %q, want merge context", err.Error())
	}
	if got := state.issues["1"].StateName; got != workflow.StateMerge {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateMerge)
	}
	if state.stateUpdates != 0 {
		t.Fatalf("stateUpdates = %d, want 0", state.stateUpdates)
	}
}

func TestRunnerMergeRetryAfterFailureIsRecoverable(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateMerge,
				Description: "ready to merge",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	mergeExecutor := &fakeMergeExecutor{
		errs: []error{errors.New("push failed")},
	}

	r := Runner{
		Linear:        client,
		MergeExecutor: mergeExecutor,
		TeamID:        "team-1",
		WorkerID:      "worker-1",
		LeaseTTL:      5 * time.Minute,
		Clock:         time.Now,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err == nil {
		t.Fatal("first RunOnce() error = nil, want merge error")
	}
	if got := state.issues["1"].StateName; got != workflow.StateMerge {
		t.Fatalf("first run state = %q, want %q", got, workflow.StateMerge)
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	if got := state.issues["1"].StateName; got != workflow.StateDone {
		t.Fatalf("second run state = %q, want %q", got, workflow.StateDone)
	}
	if mergeExecutor.callCnt != 2 {
		t.Fatalf("merge executor call count = %d, want 2", mergeExecutor.callCnt)
	}
}

func TestRunnerMergeQueueProcessesOneIssuePerCycleDeterministically(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-2",
				StateName:   workflow.StateMerge,
				Description: "ready to merge second",
				Metadata:    map[string]string{},
			},
			"2": {
				ID:          "2",
				Identifier:  "COL-1",
				StateName:   workflow.StateMerge,
				Description: "ready to merge first",
				Metadata:    map[string]string{},
			},
			"3": {
				ID:          "3",
				Identifier:  "COL-3",
				StateName:   workflow.StateMerge,
				Description: "ready to merge third",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	mergeExecutor := &fakeMergeExecutor{}

	r := Runner{
		Linear:        client,
		MergeExecutor: mergeExecutor,
		TeamID:        "team-1",
		WorkerID:      "worker-1",
		LeaseTTL:      5 * time.Minute,
		Clock:         time.Now,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	if mergeExecutor.callCnt != 1 {
		t.Fatalf("first run merge executor call count = %d, want 1", mergeExecutor.callCnt)
	}
	if got := mergeExecutor.calledIssueIDs[0]; got != "2" {
		t.Fatalf("first run merged issue id = %q, want %q", got, "2")
	}
	if got := state.issues["2"].StateName; got != workflow.StateDone {
		t.Fatalf("issue 2 state = %q, want %q", got, workflow.StateDone)
	}
	if got := state.issues["1"].StateName; got != workflow.StateMerge {
		t.Fatalf("issue 1 state = %q, want %q", got, workflow.StateMerge)
	}
	if got := state.issues["3"].StateName; got != workflow.StateMerge {
		t.Fatalf("issue 3 state = %q, want %q", got, workflow.StateMerge)
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	if mergeExecutor.callCnt != 2 {
		t.Fatalf("second run merge executor call count = %d, want 2", mergeExecutor.callCnt)
	}
	if got := mergeExecutor.calledIssueIDs[1]; got != "1" {
		t.Fatalf("second run merged issue id = %q, want %q", got, "1")
	}
	if got := state.issues["1"].StateName; got != workflow.StateDone {
		t.Fatalf("issue 1 state = %q, want %q", got, workflow.StateDone)
	}
	if got := state.issues["3"].StateName; got != workflow.StateMerge {
		t.Fatalf("issue 3 state = %q, want %q", got, workflow.StateMerge)
	}
}

func TestRunnerInProgressNotWellSpecifiedMovesToRefineAndComments(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "incomplete issue",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:   false,
			NeedsInputSummary: "- acceptance criteria",
			ThreadID:          "thr_1",
		},
	}

	r := Runner{
		Linear:   client,
		Executor: executor,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    time.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if got := state.issues["1"].StateName; got != workflow.StateRefine {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateRefine)
	}
	if executor.callCnt != 1 {
		t.Fatalf("executor call count = %d, want 1", executor.callCnt)
	}
	comments := state.comments["1"]
	if len(comments) != 1 {
		t.Fatalf("comment count = %d, want 1", len(comments))
	}
	if !strings.Contains(comments[0], "Moved to **Refine**") {
		t.Fatalf("unexpected comment body: %q", comments[0])
	}
	if got := state.issues["1"].Metadata[workflow.MetaCodexThreadID]; got != "thr_1" {
		t.Fatalf("MetaCodexThreadID = %q, want %q", got, "thr_1")
	}
	if got := state.issues["1"].Metadata[workflow.MetaCodexSessionID]; got != "thr_1" {
		t.Fatalf("MetaCodexSessionID = %q, want %q", got, "thr_1")
	}
}

func TestRunnerInProgressWellSpecifiedMovesToReviewAndComments(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "complete issue",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:  true,
			ExecutionSummary: "implemented the requested change",
			ThreadID:         "thr_2",
		},
	}

	r := Runner{
		Linear:   client,
		Executor: executor,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    time.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if got := state.issues["1"].StateName; got != workflow.StateReview {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateReview)
	}
	comments := state.comments["1"]
	if len(comments) != 1 {
		t.Fatalf("comment count = %d, want 1", len(comments))
	}
	if !strings.Contains(comments[0], "Moved to **Review**") {
		t.Fatalf("unexpected comment body: %q", comments[0])
	}
	if got := state.issues["1"].Metadata[workflow.MetaCodexThreadID]; got != "thr_2" {
		t.Fatalf("MetaCodexThreadID = %q, want %q", got, "thr_2")
	}
	if got := state.issues["1"].Metadata[workflow.MetaCodexSessionID]; got != "thr_2" {
		t.Fatalf("MetaCodexSessionID = %q, want %q", got, "thr_2")
	}
}

func TestRunnerInProgressInitializesMissingWorkspaceMetadata(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "complete issue",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:  true,
			ExecutionSummary: "implemented",
			ThreadID:         "thr_2",
		},
	}
	bootstrapper := &fakeTaskBootstrapper{
		result: TaskBootstrapResult{
			WorktreePath: "/tmp/colin/worktrees/COL-1",
			BranchName:   "colin/COL-1",
		},
	}

	r := Runner{
		Linear:       client,
		Executor:     executor,
		Bootstrapper: bootstrapper,
		TeamID:       "team-1",
		WorkerID:     "worker-1",
		LeaseTTL:     5 * time.Minute,
		Clock:        time.Now,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	issue := state.issues["1"]
	if got := issue.Metadata[workflow.MetaTaskWorktreePath]; got != "/tmp/colin/worktrees/COL-1" {
		t.Fatalf("MetaTaskWorktreePath = %q, want %q", got, "/tmp/colin/worktrees/COL-1")
	}
	if got := issue.Metadata[workflow.MetaTaskBranchName]; got != "colin/COL-1" {
		t.Fatalf("MetaTaskBranchName = %q, want %q", got, "colin/COL-1")
	}
	if executor.callCnt != 1 {
		t.Fatalf("executor call count = %d, want 1", executor.callCnt)
	}
}

func TestRunnerInProgressPartialWorkspaceMetadataReturnsActionableError(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "complete issue",
				Metadata: map[string]string{
					workflow.MetaTaskWorktreePath: "/tmp/colin/worktrees/COL-1",
				},
			},
		},
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:  true,
			ExecutionSummary: "implemented",
			ThreadID:         "thr_2",
		},
	}
	bootstrapper := &fakeTaskBootstrapper{
		result: TaskBootstrapResult{
			WorktreePath: "/tmp/colin/worktrees/COL-1",
			BranchName:   "colin/COL-1",
		},
	}

	r := Runner{
		Linear:       client,
		Executor:     executor,
		Bootstrapper: bootstrapper,
		TeamID:       "team-1",
		WorkerID:     "worker-1",
		LeaseTTL:     5 * time.Minute,
		Clock:        time.Now,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := r.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want actionable metadata error")
	}
	if !strings.Contains(err.Error(), "inconsistent workspace metadata") {
		t.Fatalf("error = %q, want inconsistent metadata context", err.Error())
	}
	if executor.callCnt != 0 {
		t.Fatalf("executor call count = %d, want 0", executor.callCnt)
	}
}

func TestRunnerInProgressMismatchedWorkspaceMetadataReturnsActionableError(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "complete issue",
				Metadata: map[string]string{
					workflow.MetaTaskWorktreePath: "/unexpected/worktree",
					workflow.MetaTaskBranchName:   "colin/OTHER",
				},
			},
		},
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:  true,
			ExecutionSummary: "implemented",
			ThreadID:         "thr_2",
		},
	}
	bootstrapper := &fakeTaskBootstrapper{
		result: TaskBootstrapResult{
			WorktreePath: "/tmp/colin/worktrees/COL-1",
			BranchName:   "colin/COL-1",
		},
	}

	r := Runner{
		Linear:       client,
		Executor:     executor,
		Bootstrapper: bootstrapper,
		TeamID:       "team-1",
		WorkerID:     "worker-1",
		LeaseTTL:     5 * time.Minute,
		Clock:        time.Now,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := r.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want workspace mismatch error")
	}
	if !strings.Contains(err.Error(), "workspace metadata mismatch") {
		t.Fatalf("error = %q, want mismatch context", err.Error())
	}
	if executor.callCnt != 0 {
		t.Fatalf("executor call count = %d, want 0", executor.callCnt)
	}
}

func TestRunnerInProgressRetryAfterConflictDoesNotDuplicateComment(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "incomplete issue",
				Metadata:    map[string]string{},
			},
		},
		conflictOnNextStateWrite: true,
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:   false,
			NeedsInputSummary: "- acceptance criteria",
			ThreadID:          "thr_1",
		},
	}

	r := Runner{
		Linear:   client,
		Executor: executor,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    time.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	if got := state.issues["1"].StateName; got != workflow.StateInProgress {
		t.Fatalf("first run StateName = %q, want %q", got, workflow.StateInProgress)
	}
	if got := len(state.comments["1"]); got != 1 {
		t.Fatalf("first run comment count = %d, want 1", got)
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	if got := state.issues["1"].StateName; got != workflow.StateRefine {
		t.Fatalf("second run StateName = %q, want %q", got, workflow.StateRefine)
	}
	if got := len(state.comments["1"]); got != 1 {
		t.Fatalf("second run comment count = %d, want 1", got)
	}
}

func TestRunnerRunOnceProcessesIssuesConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := &fakeClientState{
			issues: map[string]linear.Issue{
				"1": {
					ID:          "1",
					Identifier:  "COL-1",
					StateName:   workflow.StateInProgress,
					Description: "spec one",
					Metadata:    map[string]string{},
				},
				"2": {
					ID:          "2",
					Identifier:  "COL-2",
					StateName:   workflow.StateInProgress,
					Description: "spec two",
					Metadata:    map[string]string{},
				},
			},
		}
		client := newFakeLinearClient(state)
		executor := &blockingInProgressExecutor{
			entered: make(chan string, 2),
			release: make(chan struct{}),
			result: InProgressExecutionResult{
				IsWellSpecified:  true,
				ExecutionSummary: "done",
				ThreadID:         "thr",
			},
		}

		r := Runner{
			Linear:   client,
			Executor: executor,
			TeamID:   "team-1",
			WorkerID: "worker-1",
			LeaseTTL: 5 * time.Minute,
			Clock:    time.Now,
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		}

		done := make(chan error, 1)
		go func() {
			done <- r.RunOnce(context.Background())
		}()

		// Wait until both issue goroutines reached EvaluateAndExecute and blocked on release.
		synctest.Wait()
		if got := len(executor.entered); got != 2 {
			t.Fatalf("entered count = %d, want 2 (all issues started before release)", got)
		}

		enteredIssues := map[string]struct{}{}
		enteredIssues[<-executor.entered] = struct{}{}
		enteredIssues[<-executor.entered] = struct{}{}
		if _, ok := enteredIssues["1"]; !ok {
			t.Fatalf("missing issue 1 start, got %#v", enteredIssues)
		}
		if _, ok := enteredIssues["2"]; !ok {
			t.Fatalf("missing issue 2 start, got %#v", enteredIssues)
		}

		close(executor.release)
		synctest.Wait()

		if err := <-done; err != nil {
			t.Fatalf("RunOnce() error = %v", err)
		}

		if got := state.issues["1"].StateName; got != workflow.StateReview {
			t.Fatalf("issue 1 state = %q, want %q", got, workflow.StateReview)
		}
		if got := state.issues["2"].StateName; got != workflow.StateReview {
			t.Fatalf("issue 2 state = %q, want %q", got, workflow.StateReview)
		}
	})
}

func TestRunnerRunOnceRespectsMaxConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := &fakeClientState{
			issues: map[string]linear.Issue{
				"1": {
					ID:          "1",
					Identifier:  "COL-1",
					StateName:   workflow.StateInProgress,
					Description: "spec one",
					Metadata:    map[string]string{},
				},
				"2": {
					ID:          "2",
					Identifier:  "COL-2",
					StateName:   workflow.StateInProgress,
					Description: "spec two",
					Metadata:    map[string]string{},
				},
			},
		}
		client := newFakeLinearClient(state)
		executor := &blockingInProgressExecutor{
			entered: make(chan string, 2),
			release: make(chan struct{}),
			result: InProgressExecutionResult{
				IsWellSpecified:  true,
				ExecutionSummary: "done",
				ThreadID:         "thr",
			},
		}

		r := Runner{
			Linear:         client,
			Executor:       executor,
			TeamID:         "team-1",
			WorkerID:       "worker-1",
			LeaseTTL:       5 * time.Minute,
			MaxConcurrency: 1,
			Clock:          time.Now,
			Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		}

		done := make(chan error, 1)
		go func() {
			done <- r.RunOnce(context.Background())
		}()

		// Only one issue can enter execution while MaxConcurrency is 1.
		synctest.Wait()
		if got := len(executor.entered); got != 1 {
			t.Fatalf("entered count = %d, want 1", got)
		}

		close(executor.release)
		synctest.Wait()

		if err := <-done; err != nil {
			t.Fatalf("RunOnce() error = %v", err)
		}
		if got := state.issues["1"].StateName; got != workflow.StateReview {
			t.Fatalf("issue 1 state = %q, want %q", got, workflow.StateReview)
		}
		if got := state.issues["2"].StateName; got != workflow.StateReview {
			t.Fatalf("issue 2 state = %q, want %q", got, workflow.StateReview)
		}
	})
}

func TestRunnerRunOnceLogsCycleEvenWhenNoIssues(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{},
	}
	client := newFakeLinearClient(state)

	var logOutput bytes.Buffer
	r := Runner{
		Linear:   client,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    time.Now,
		Logger:   slog.New(slog.NewTextHandler(&logOutput, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	text := logOutput.String()
	if !strings.Contains(text, "action=cycle_start") {
		t.Fatalf("expected cycle_start log entry, got %q", text)
	}
	if !strings.Contains(text, "action=issues_fetched") || !strings.Contains(text, "count=0") {
		t.Fatalf("expected issues_fetched count=0 log entry, got %q", text)
	}
	if !strings.Contains(text, "action=cycle_complete") || !strings.Contains(text, "processed=0") {
		t.Fatalf("expected cycle_complete processed=0 log entry, got %q", text)
	}
}

func TestRunnerRunOnceLogsMergeQueueSelectionAndDeferredIssues(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-2",
				StateName:   workflow.StateMerge,
				Description: "ready to merge second",
				Metadata:    map[string]string{},
			},
			"2": {
				ID:          "2",
				Identifier:  "COL-1",
				StateName:   workflow.StateMerge,
				Description: "ready to merge first",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)

	var logOutput bytes.Buffer
	r := Runner{
		Linear:        client,
		MergeExecutor: &fakeMergeExecutor{},
		TeamID:        "team-1",
		WorkerID:      "worker-1",
		LeaseTTL:      5 * time.Minute,
		Clock:         time.Now,
		Logger:        slog.New(slog.NewTextHandler(&logOutput, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	text := logOutput.String()
	if !strings.Contains(text, "action=merge_queue_select") {
		t.Fatalf("expected merge_queue_select log entry, got %q", text)
	}
	if !strings.Contains(text, "queue_length=2") {
		t.Fatalf("expected queue_length=2 in logs, got %q", text)
	}
	if !strings.Contains(text, "selected_issue=COL-1") {
		t.Fatalf("expected selected_issue=COL-1 in logs, got %q", text)
	}
	if !strings.Contains(text, "deferred_issues=COL-2") {
		t.Fatalf("expected deferred_issues=COL-2 in logs, got %q", text)
	}
}
