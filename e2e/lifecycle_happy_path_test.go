package e2e_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/worker"
	"github.com/pmenglund/colin/internal/workflow"
)

type e2eFakeBootstrapper struct {
	callCount int
}

func (b *e2eFakeBootstrapper) EnsureTaskWorkspace(_ context.Context, _ string) (worker.TaskBootstrapResult, error) {
	b.callCount++
	return worker.TaskBootstrapResult{
		WorktreePath: "/tmp/colin/worktrees/COLIN-100",
		BranchName:   "colin/COLIN-100",
	}, nil
}

type e2eFakeExecutor struct {
	callCount int
}

func (e *e2eFakeExecutor) EvaluateAndExecute(_ context.Context, _ linear.Issue) (worker.InProgressExecutionResult, error) {
	e.callCount++
	return worker.InProgressExecutionResult{
		IsWellSpecified:  true,
		ExecutionSummary: "implemented change set",
		ThreadID:         "thread-100",
	}, nil
}

func TestLifecycleHappyPathTodoToDone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 2, 17, 21, 0, 0, 0, time.UTC)

	client := linear.NewInMemoryClient([]linear.Issue{
		{
			ID:          "issue-100",
			Identifier:  "COLIN-100",
			Title:       "happy path",
			Description: "specification present",
			StateName:   workflow.StateTodo,
		},
	})
	bootstrapper := &e2eFakeBootstrapper{}
	executor := &e2eFakeExecutor{}

	runner := &worker.Runner{
		Linear:        client,
		Executor:      executor,
		MergeExecutor: worker.NoopMergeExecutor{},
		Bootstrapper:  bootstrapper,
		TeamID:        "team-1",
		WorkerID:      "worker-e2e",
		LeaseTTL:      5 * time.Minute,
		Clock:         func() time.Time { return now },
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 1 RunOnce() error = %v", err)
	}
	issue, err := client.GetIssue(ctx, "issue-100")
	if err != nil {
		t.Fatalf("GetIssue() after cycle 1 error = %v", err)
	}
	if issue.StateName != workflow.StateInProgress {
		t.Fatalf("cycle 1 state = %q, want %q", issue.StateName, workflow.StateInProgress)
	}
	if bootstrapper.callCount != 1 {
		t.Fatalf("bootstrapper callCount = %d, want 1", bootstrapper.callCount)
	}
	if got := issue.Metadata[workflow.MetaWorktreePath]; got != "/tmp/colin/worktrees/COLIN-100" {
		t.Fatalf("cycle 1 Metadata[%s] = %q, want %q", workflow.MetaWorktreePath, got, "/tmp/colin/worktrees/COLIN-100")
	}
	if got := issue.Metadata[workflow.MetaBranchName]; got != "colin/COLIN-100" {
		t.Fatalf("cycle 1 Metadata[%s] = %q, want %q", workflow.MetaBranchName, got, "colin/COLIN-100")
	}

	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 2 RunOnce() error = %v", err)
	}
	issue, err = client.GetIssue(ctx, "issue-100")
	if err != nil {
		t.Fatalf("GetIssue() after cycle 2 error = %v", err)
	}
	if issue.StateName != workflow.StateReview {
		t.Fatalf("cycle 2 state = %q, want %q", issue.StateName, workflow.StateReview)
	}
	if executor.callCount != 1 {
		t.Fatalf("executor callCount = %d, want 1", executor.callCount)
	}
	if got := issue.Metadata[workflow.MetaThreadID]; got != "thread-100" {
		t.Fatalf("cycle 2 Metadata[%s] = %q, want %q", workflow.MetaThreadID, got, "thread-100")
	}

	if err := client.UpdateIssueState(ctx, "issue-100", workflow.StateMerge); err != nil {
		t.Fatalf("UpdateIssueState() to Merge error = %v", err)
	}
	if err := client.UpdateIssueMetadata(ctx, "issue-100", linear.MetadataPatch{
		Set: map[string]string{workflow.MetaMergeReady: "true"},
	}); err != nil {
		t.Fatalf("UpdateIssueMetadata() set merge_ready error = %v", err)
	}

	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 3 RunOnce() error = %v", err)
	}
	issue, err = client.GetIssue(ctx, "issue-100")
	if err != nil {
		t.Fatalf("GetIssue() after cycle 3 error = %v", err)
	}
	if issue.StateName != workflow.StateDone {
		t.Fatalf("cycle 3 state = %q, want %q", issue.StateName, workflow.StateDone)
	}

	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 4 RunOnce() error = %v", err)
	}
	if bootstrapper.callCount != 1 {
		t.Fatalf("bootstrapper callCount after rerun = %d, want 1", bootstrapper.callCount)
	}
	if executor.callCount != 1 {
		t.Fatalf("executor callCount after rerun = %d, want 1", executor.callCount)
	}
}

func TestLifecycleBlockedDependencyUnblocksWhenDependencyDone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 2, 17, 21, 0, 0, 0, time.UTC)

	client := linear.NewInMemoryClient([]linear.Issue{
		{
			ID:          "issue-dep",
			Identifier:  "COLIN-DEP",
			Title:       "dependency",
			Description: "specification present",
			StateName:   workflow.StateTodo,
		},
		{
			ID:          "issue-blocked",
			Identifier:  "COLIN-BLOCKED",
			Title:       "blocked issue",
			Description: "specification present",
			StateName:   workflow.StateTodo,
			BlockedBy:   []string{"issue-dep"},
		},
	})

	runner := &worker.Runner{
		Linear:        client,
		MergeExecutor: worker.NoopMergeExecutor{},
		TeamID:        "team-1",
		WorkerID:      "worker-e2e",
		LeaseTTL:      5 * time.Minute,
		Clock:         func() time.Time { return now },
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 1 RunOnce() error = %v", err)
	}

	blocked, err := client.GetIssue(ctx, "issue-blocked")
	if err != nil {
		t.Fatalf("GetIssue(blocked) after cycle 1 error = %v", err)
	}
	if blocked.StateName != workflow.StateTodo {
		t.Fatalf("blocked issue state after cycle 1 = %q, want %q", blocked.StateName, workflow.StateTodo)
	}

	if err := client.UpdateIssueState(ctx, "issue-dep", workflow.StateDone); err != nil {
		t.Fatalf("UpdateIssueState(dep->Done) error = %v", err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 2 RunOnce() error = %v", err)
	}

	blocked, err = client.GetIssue(ctx, "issue-blocked")
	if err != nil {
		t.Fatalf("GetIssue(blocked) after cycle 2 error = %v", err)
	}
	if blocked.StateName != workflow.StateInProgress {
		t.Fatalf("blocked issue state after cycle 2 = %q, want %q", blocked.StateName, workflow.StateInProgress)
	}
}

func TestLifecycleMergeQueueSerializedAcrossCycles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 2, 17, 21, 0, 0, 0, time.UTC)

	client := linear.NewInMemoryClient([]linear.Issue{
		{
			ID:          "issue-merge-1",
			Identifier:  "COLIN-MERGE-1",
			Title:       "merge one",
			Description: "ready",
			StateName:   workflow.StateMerge,
			Metadata: map[string]string{
				workflow.MetaMergeReady: "true",
			},
		},
		{
			ID:          "issue-merge-2",
			Identifier:  "COLIN-MERGE-2",
			Title:       "merge two",
			Description: "ready",
			StateName:   workflow.StateMerge,
			Metadata: map[string]string{
				workflow.MetaMergeReady: "true",
			},
		},
	})

	runner := &worker.Runner{
		Linear:         client,
		MergeExecutor:  worker.NoopMergeExecutor{},
		TeamID:         "team-1",
		WorkerID:       "worker-e2e",
		LeaseTTL:       5 * time.Minute,
		MaxConcurrency: 8,
		Clock:          func() time.Time { return now },
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 1 RunOnce() error = %v", err)
	}

	issueOne, err := client.GetIssue(ctx, "issue-merge-1")
	if err != nil {
		t.Fatalf("GetIssue(issue-merge-1) after cycle 1 error = %v", err)
	}
	issueTwo, err := client.GetIssue(ctx, "issue-merge-2")
	if err != nil {
		t.Fatalf("GetIssue(issue-merge-2) after cycle 1 error = %v", err)
	}
	if issueOne.StateName != workflow.StateDone {
		t.Fatalf("cycle 1 issue-merge-1 state = %q, want %q", issueOne.StateName, workflow.StateDone)
	}
	if issueTwo.StateName != workflow.StateMerge {
		t.Fatalf("cycle 1 issue-merge-2 state = %q, want %q", issueTwo.StateName, workflow.StateMerge)
	}

	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("cycle 2 RunOnce() error = %v", err)
	}
	issueTwo, err = client.GetIssue(ctx, "issue-merge-2")
	if err != nil {
		t.Fatalf("GetIssue(issue-merge-2) after cycle 2 error = %v", err)
	}
	if issueTwo.StateName != workflow.StateDone {
		t.Fatalf("cycle 2 issue-merge-2 state = %q, want %q", issueTwo.StateName, workflow.StateDone)
	}
}
