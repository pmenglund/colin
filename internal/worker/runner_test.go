package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/linear/linearfakes"
	"github.com/pmenglund/colin/internal/workflow"
)

type fakeClientState struct {
	issues                   map[string]linear.Issue
	stateUpdates             int
	metadataUpdates          int
	comments                 map[string][]string
	conflictOnNextStateWrite bool
}

func newFakeLinearClient(state *fakeClientState) *linearfakes.FakeClient {
	fake := &linearfakes.FakeClient{}

	fake.ListCandidateIssuesCalls(func(_ context.Context, _ string) ([]linear.Issue, error) {
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
		issue, ok := state.issues[issueID]
		if !ok {
			return linear.Issue{}, fmt.Errorf("issue %s not found", issueID)
		}
		return cloneIssue(issue), nil
	})

	fake.UpdateIssueStateCalls(func(_ context.Context, issueID string, toState string) error {
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
}

func TestRunnerInProgressWellSpecifiedMovesToHumanReviewAndComments(t *testing.T) {
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

	if got := state.issues["1"].StateName; got != workflow.StateHumanReview {
		t.Fatalf("StateName = %q, want %q", got, workflow.StateHumanReview)
	}
	comments := state.comments["1"]
	if len(comments) != 1 {
		t.Fatalf("comment count = %d, want 1", len(comments))
	}
	if !strings.Contains(comments[0], "Moved to **Human Review**") {
		t.Fatalf("unexpected comment body: %q", comments[0])
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
