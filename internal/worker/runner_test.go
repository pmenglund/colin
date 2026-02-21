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
	"github.com/pmenglund/colin/internal/linear/fakes"
	"github.com/pmenglund/colin/internal/workflow"
)

type fakeClientState struct {
	mu                       sync.Mutex
	issues                   map[string]linear.Issue
	stateUpdates             int
	metadataUpdates          int
	comments                 map[string][]string
	conflictOnNextStateWrite bool
	conflictOnNextMetaWrite  bool
}

func newFakeLinearClient(state *fakeClientState) *fakes.FakeClient {
	fake := &fakes.FakeClient{}

	fake.ListCandidateIssuesCalls(func(_ context.Context, _ string) ([]linear.Issue, error) {
		state.mu.Lock()
		defer state.mu.Unlock()

		out := make([]linear.Issue, 0, len(state.issues))
		for _, issue := range state.issues {
			out = append(out, cloneIssue(issue))
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
		if state.conflictOnNextMetaWrite {
			state.conflictOnNextMetaWrite = false
			return linear.ErrConflict
		}

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
	out.BlockedBy = append([]string(nil), issue.BlockedBy...)
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
	callCnt          int
	lastIssue        string
	result           TaskBootstrapResult
	err              error
	recordCallCnt    int
	recordWorktree   string
	recordBranch     string
	recordSessionID  string
	recordSessionErr error
}

func (f *fakeTaskBootstrapper) EnsureTaskWorkspace(_ context.Context, issueIdentifier string) (TaskBootstrapResult, error) {
	f.callCnt++
	f.lastIssue = issueIdentifier
	if f.err != nil {
		return TaskBootstrapResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeTaskBootstrapper) RecordBranchSession(_ context.Context, worktreePath string, branchName string, sessionID string) error {
	f.recordCallCnt++
	f.recordWorktree = worktreePath
	f.recordBranch = branchName
	f.recordSessionID = sessionID
	if f.recordSessionErr != nil {
		return f.recordSessionErr
	}
	return nil
}

type fakeMergeExecutor struct {
	callCnt   int
	lastIssue linear.Issue
	err       error
}

func (f *fakeMergeExecutor) ExecuteMerge(_ context.Context, issue linear.Issue) error {
	f.callCnt++
	f.lastIssue = issue
	if f.err != nil {
		return f.err
	}
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
		t.Fatalf("Metadata[%s] = %q, want %q", workflow.MetaWorktreePath, got, "/tmp/colin/worktrees/COL-1")
	}
	if got := issue.Metadata[workflow.MetaBranchName]; got != "colin/COL-1" {
		t.Fatalf("Metadata[%s] = %q, want %q", workflow.MetaBranchName, got, "colin/COL-1")
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	if bootstrapper.callCnt != 1 {
		t.Fatalf("bootstrapper call count after rerun = %d, want 1", bootstrapper.callCnt)
	}
	issue = state.issues["1"]
	if got := issue.Metadata[workflow.MetaWorktreePath]; got != "/tmp/colin/worktrees/COL-1" {
		t.Fatalf("Metadata[%s] after rerun = %q, want %q", workflow.MetaWorktreePath, got, "/tmp/colin/worktrees/COL-1")
	}
	if got := issue.Metadata[workflow.MetaBranchName]; got != "colin/COL-1" {
		t.Fatalf("Metadata[%s] after rerun = %q, want %q", workflow.MetaBranchName, got, "colin/COL-1")
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

func TestRunnerInProgressSkipsExecutionWhenLeaseOwnedByOtherWorker(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "spec present",
				Metadata: map[string]string{
					workflow.MetaLeaseOwner:        "worker-2",
					workflow.MetaLeaseExecutionID:  "exec-2",
					workflow.MetaLeaseExpiresAtUTC: now.Add(time.Minute).Format(time.RFC3339),
				},
			},
		},
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:  true,
			ExecutionSummary: "done",
			ThreadID:         "thr_1",
		},
	}

	r := Runner{
		Linear:   client,
		Executor: executor,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    func() time.Time { return now },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if executor.callCnt != 0 {
		t.Fatalf("executor call count = %d, want 0", executor.callCnt)
	}
	if got := state.issues["1"].StateName; got != workflow.StateInProgress {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateInProgress)
	}
	if got := len(state.comments["1"]); got != 0 {
		t.Fatalf("comment count = %d, want 0", got)
	}
}

func TestRunnerInProgressExecutionErrorClaimsLeaseForRecovery(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "spec present",
				Metadata:    map[string]string{},
			},
		},
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		err: errors.New("codex transient failure"),
	}

	r := Runner{
		Linear:   client,
		Executor: executor,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    func() time.Time { return now },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := r.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want in-progress execution error")
	}
	if !strings.Contains(err.Error(), "evaluate and execute in-progress issue COL-1") {
		t.Fatalf("error = %q", err.Error())
	}

	issue := state.issues["1"]
	if got := issue.Metadata[workflow.MetaLeaseOwner]; got != "worker-1" {
		t.Fatalf("Metadata[%s] = %q, want %q", workflow.MetaLeaseOwner, got, "worker-1")
	}
	if got := issue.Metadata[workflow.MetaLeaseExecutionID]; got == "" {
		t.Fatalf("Metadata[%s] = empty, want execution id", workflow.MetaLeaseExecutionID)
	}
	expiresRaw := issue.Metadata[workflow.MetaLeaseExpiresAtUTC]
	if expiresRaw == "" {
		t.Fatalf("Metadata[%s] = empty", workflow.MetaLeaseExpiresAtUTC)
	}
	expiresAt, parseErr := time.Parse(time.RFC3339, expiresRaw)
	if parseErr != nil {
		t.Fatalf("parse lease expiry: %v", parseErr)
	}
	if !expiresAt.After(now) {
		t.Fatalf("lease expiry = %s, want after %s", expiresAt, now)
	}
}

func TestRunnerInProgressMetadataConflictSkipsExecution(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "spec present",
				Metadata:    map[string]string{},
			},
		},
		conflictOnNextMetaWrite: true,
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:  true,
			ExecutionSummary: "done",
			ThreadID:         "thr_1",
		},
	}

	r := Runner{
		Linear:   client,
		Executor: executor,
		TeamID:   "team-1",
		WorkerID: "worker-1",
		LeaseTTL: 5 * time.Minute,
		Clock:    func() time.Time { return now },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if executor.callCnt != 0 {
		t.Fatalf("executor call count = %d, want 0", executor.callCnt)
	}
	if got := state.issues["1"].StateName; got != workflow.StateInProgress {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateInProgress)
	}
	if got := len(state.comments["1"]); got != 0 {
		t.Fatalf("comment count = %d, want 0", got)
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
	if got := state.issues["1"].Metadata[workflow.MetaThreadID]; got != "thr_1" {
		t.Fatalf("Metadata[%s] = %q, want %q", workflow.MetaThreadID, got, "thr_1")
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
				Metadata: map[string]string{
					workflow.MetaWorktreePath: "/tmp/colin/worktrees/COL-1",
					workflow.MetaBranchName:   "colin/COL-1",
				},
			},
		},
	}
	client := newFakeLinearClient(state)
	bootstrapper := &fakeTaskBootstrapper{}
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:  true,
			ExecutionSummary: "implemented the requested change",
			ThreadID:         "thr_2",
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

	if got := state.issues["1"].StateName; got != workflow.StateReview {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateReview)
	}
	comments := state.comments["1"]
	if len(comments) != 1 {
		t.Fatalf("comment count = %d, want 1", len(comments))
	}
	wantComment := "Moved to **Review** after Codex execution.\n\n## Execution Summary\nimplemented the requested change\n\n## Execution Context\n- Thread: `thr_2`\n- Branch: `colin/COL-1`\n- Worktree: `/tmp/colin/worktrees/COL-1`"
	if comments[0] != wantComment {
		t.Fatalf("comment body = %q, want %q", comments[0], wantComment)
	}
	if got := state.issues["1"].Metadata[workflow.MetaThreadID]; got != "thr_2" {
		t.Fatalf("Metadata[%s] = %q, want %q", workflow.MetaThreadID, got, "thr_2")
	}
	if bootstrapper.recordCallCnt != 1 {
		t.Fatalf("recordCallCnt = %d, want 1", bootstrapper.recordCallCnt)
	}
	if bootstrapper.recordWorktree != "/tmp/colin/worktrees/COL-1" {
		t.Fatalf("recordWorktree = %q, want %q", bootstrapper.recordWorktree, "/tmp/colin/worktrees/COL-1")
	}
	if bootstrapper.recordBranch != "colin/COL-1" {
		t.Fatalf("recordBranch = %q, want %q", bootstrapper.recordBranch, "colin/COL-1")
	}
	if bootstrapper.recordSessionID != "thr_2" {
		t.Fatalf("recordSessionID = %q, want %q", bootstrapper.recordSessionID, "thr_2")
	}
}

func TestRunnerInProgressWellSpecifiedReviewCommentIncludesEvidence(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "complete issue",
				Metadata: map[string]string{
					workflow.MetaWorktreePath: "/tmp/colin/worktrees/COL-1",
					workflow.MetaBranchName:   "colin/COL-1",
				},
			},
		},
	}
	client := newFakeLinearClient(state)
	executor := &fakeInProgressExecutor{
		result: InProgressExecutionResult{
			IsWellSpecified:  true,
			ExecutionSummary: "implemented the requested change",
			ThreadID:         "thr_2",
			TranscriptRef:    "terminal://logs/COL-1.txt",
			ScreenshotRef:    "https://example.invalid/screenshot.png",
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

	comments := state.comments["1"]
	if len(comments) != 1 {
		t.Fatalf("comment count = %d, want 1", len(comments))
	}
	wantComment := "Moved to **Review** after Codex execution.\n\n## Execution Summary\nimplemented the requested change\n\n## Execution Context\n- Thread: `thr_2`\n- Branch: `colin/COL-1`\n- Worktree: `/tmp/colin/worktrees/COL-1`\n\n## Evidence\n- Terminal transcript: terminal://logs/COL-1.txt\n- Screenshot: https://example.invalid/screenshot.png"
	if comments[0] != wantComment {
		t.Fatalf("comment body = %q, want %q", comments[0], wantComment)
	}
}

func TestRunnerUsesConfiguredWorkflowStateNames(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   "Doing",
				Description: "complete issue",
				Metadata: map[string]string{
					workflow.MetaWorktreePath: "/tmp/colin/worktrees/COL-1",
					workflow.MetaBranchName:   "colin/COL-1",
				},
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
		States: workflow.States{
			Todo:       "Backlog",
			InProgress: "Doing",
			Refine:     "Needs Spec",
			Review:     "Human Review",
			Merge:      "Merge Queue",
			Done:       "Closed",
		},
		Clock:  time.Now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if got := state.issues["1"].StateName; got != "Human Review" {
		t.Fatalf("StateName = %q, want %q", got, "Human Review")
	}
	comments := state.comments["1"]
	if len(comments) != 1 {
		t.Fatalf("comment count = %d, want 1", len(comments))
	}
	if !strings.Contains(comments[0], "Moved to **Human Review**") {
		t.Fatalf("comment body = %q, want configured review state", comments[0])
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

func TestRunnerInProgressReviewRetryAfterConflictDoesNotDuplicateComment(t *testing.T) {
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateInProgress,
				Description: "complete issue",
				Metadata: map[string]string{
					workflow.MetaWorktreePath: "/tmp/colin/worktrees/COL-1",
					workflow.MetaBranchName:   "colin/COL-1",
				},
			},
		},
		conflictOnNextStateWrite: true,
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
	if got := state.issues["1"].StateName; got != workflow.StateReview {
		t.Fatalf("second run StateName = %q, want %q", got, workflow.StateReview)
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

func TestRunnerRunOnceSerializesMergeQueue(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateMerge,
				Description: "ready",
				Metadata: map[string]string{
					workflow.MetaMergeReady: "true",
				},
			},
			"2": {
				ID:          "2",
				Identifier:  "COL-2",
				StateName:   workflow.StateMerge,
				Description: "ready",
				Metadata: map[string]string{
					workflow.MetaMergeReady: "true",
				},
			},
		},
	}
	client := newFakeLinearClient(state)
	mergeExecutor := &fakeMergeExecutor{}

	r := Runner{
		Linear:         client,
		MergeExecutor:  mergeExecutor,
		TeamID:         "team-1",
		WorkerID:       "worker-1",
		LeaseTTL:       5 * time.Minute,
		MaxConcurrency: 4,
		Clock:          func() time.Time { return now },
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	if got := state.issues["1"].StateName; got != workflow.StateDone {
		t.Fatalf("issue 1 state after first cycle = %q, want %q", got, workflow.StateDone)
	}
	if got := state.issues["2"].StateName; got != workflow.StateMerge {
		t.Fatalf("issue 2 state after first cycle = %q, want %q", got, workflow.StateMerge)
	}

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	if got := state.issues["2"].StateName; got != workflow.StateDone {
		t.Fatalf("issue 2 state after second cycle = %q, want %q", got, workflow.StateDone)
	}
	if mergeExecutor.callCnt != 2 {
		t.Fatalf("merge executor call count = %d, want 2", mergeExecutor.callCnt)
	}
}

func TestRunnerRunOnceMergeExecutionFailureKeepsIssueInMerge(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	state := &fakeClientState{
		issues: map[string]linear.Issue{
			"1": {
				ID:          "1",
				Identifier:  "COL-1",
				StateName:   workflow.StateMerge,
				Description: "ready",
				Metadata: map[string]string{
					workflow.MetaMergeReady: "true",
				},
			},
		},
	}
	client := newFakeLinearClient(state)
	mergeExecutor := &fakeMergeExecutor{err: errors.New("push failed")}

	r := Runner{
		Linear:        client,
		MergeExecutor: mergeExecutor,
		TeamID:        "team-1",
		WorkerID:      "worker-1",
		LeaseTTL:      5 * time.Minute,
		Clock:         func() time.Time { return now },
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := r.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want merge execution failure")
	}
	if !strings.Contains(err.Error(), "execute merge for issue COL-1") {
		t.Fatalf("error = %q, want merge execution context", err.Error())
	}
	if got := state.issues["1"].StateName; got != workflow.StateMerge {
		t.Fatalf("issue state after merge failure = %q, want %q", got, workflow.StateMerge)
	}
	if got := state.issues["1"].Metadata[workflow.MetaMergeReady]; got != "true" {
		t.Fatalf("merge_ready after merge failure = %q, want %q", got, "true")
	}
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
