package workflow

import (
	"testing"
	"time"
)

func TestDecideTodoToInProgress(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	s := IssueSnapshot{
		State:       StateTodo,
		Description: "spec is present",
		Metadata:    map[string]string{},
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    2 * time.Minute,
	}

	d := Decide(s, now)
	if d.Action != ActionClaimAndTransition {
		t.Fatalf("Action = %q", d.Action)
	}
	if d.ToState != StateInProgress {
		t.Fatalf("ToState = %q", d.ToState)
	}
	if d.LeasePatch == nil {
		t.Fatal("expected lease patch")
	}
}

func TestDecideTodoToRefineWithoutSpec(t *testing.T) {
	d := Decide(IssueSnapshot{
		State:       StateTodo,
		Metadata:    map[string]string{},
		WorkerID:    "worker-1",
		LeaseTTL:    time.Minute,
		ExecutionID: "exec-1",
	}, time.Now())

	if d.ToState != StateRefine {
		t.Fatalf("ToState = %q", d.ToState)
	}
}

func TestDecideTodoNoopWhenLeaseOwnedByOther(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	d := Decide(IssueSnapshot{
		State:       StateTodo,
		Description: "spec",
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    time.Minute,
		Metadata: map[string]string{
			MetaLeaseOwner:        "worker-2",
			MetaLeaseExpiresAtUTC: now.Add(time.Minute).Format(time.RFC3339),
		},
	}, now)

	if d.Action != ActionNoop {
		t.Fatalf("Action = %q", d.Action)
	}
}

func TestDecideTodoNoopWhenBlocked(t *testing.T) {
	d := Decide(IssueSnapshot{
		State:       StateTodo,
		Description: "spec",
		Blocked:     true,
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    time.Minute,
	}, time.Now())

	if d.Action != ActionNoop {
		t.Fatalf("Action = %q", d.Action)
	}
	if d.Reason != "blocked by dependency" {
		t.Fatalf("Reason = %q", d.Reason)
	}
}

func TestDecideTodoRecoversFromInvalidLeaseMetadata(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	d := Decide(IssueSnapshot{
		State:       StateTodo,
		Description: "spec",
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    time.Minute,
		Metadata: map[string]string{
			MetaLeaseOwner:        "worker-2",
			MetaLeaseExpiresAtUTC: "not-a-timestamp",
		},
	}, now)

	if d.Action != ActionClaimAndTransition {
		t.Fatalf("Action = %q", d.Action)
	}
	if d.ToState != StateInProgress {
		t.Fatalf("ToState = %q", d.ToState)
	}
	if d.LeasePatch == nil {
		t.Fatal("expected lease patch")
	}
	if d.Reason != "claimed todo issue after invalid lease metadata recovery" {
		t.Fatalf("Reason = %q", d.Reason)
	}
}

func TestDecideInProgressToReview(t *testing.T) {
	now := time.Now().UTC()
	d := Decide(IssueSnapshot{
		State:       StateInProgress,
		Description: "spec",
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    time.Minute,
		Metadata: map[string]string{
			MetaLeaseOwner:          "worker-1",
			MetaLeaseExpiresAtUTC:   now.Add(time.Minute).Format(time.RFC3339),
			MetaReadyForHumanReview: "true",
		},
	}, now)

	if d.ToState != StateReview {
		t.Fatalf("ToState = %q", d.ToState)
	}
}

func TestDecideInProgressToRefine(t *testing.T) {
	d := Decide(IssueSnapshot{
		State:       StateInProgress,
		Description: "spec",
		Metadata: map[string]string{
			MetaNeedsRefine: "true",
		},
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    time.Minute,
	}, time.Now())

	if d.ToState != StateRefine {
		t.Fatalf("ToState = %q", d.ToState)
	}
}

func TestDecideInProgressNoopWhenBlocked(t *testing.T) {
	d := Decide(IssueSnapshot{
		State:       StateInProgress,
		Description: "spec",
		Blocked:     true,
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    time.Minute,
	}, time.Now())

	if d.Action != ActionNoop {
		t.Fatalf("Action = %q", d.Action)
	}
	if d.Reason != "blocked by dependency" {
		t.Fatalf("Reason = %q", d.Reason)
	}
}

func TestDecideMergeToDoneWithoutMergeReadyMetadata(t *testing.T) {
	d := Decide(IssueSnapshot{
		State:    StateMerge,
		Metadata: map[string]string{},
	}, time.Now())

	if d.ToState != StateDone {
		t.Fatalf("ToState = %q", d.ToState)
	}
}

func TestDecideIsDeterministic(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	s := IssueSnapshot{
		State:       StateTodo,
		Description: "spec",
		Metadata:    map[string]string{},
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    time.Minute,
	}

	d1 := Decide(s, now)
	d2 := Decide(s, now)

	if d1.Action != d2.Action || d1.ToState != d2.ToState || d1.Reason != d2.Reason {
		t.Fatalf("decisions differ: %#v vs %#v", d1, d2)
	}
	if d1.LeasePatch == nil || d2.LeasePatch == nil {
		t.Fatal("expected lease patches")
	}
	if d1.LeasePatch.ExpiresAtUTC != d2.LeasePatch.ExpiresAtUTC {
		t.Fatalf("lease expiration differs: %s vs %s", d1.LeasePatch.ExpiresAtUTC, d2.LeasePatch.ExpiresAtUTC)
	}
}

func TestDecideWithStatesUsesConfiguredNames(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	states := States{
		Todo:       "Backlog",
		InProgress: "Doing",
		Refine:     "Needs Spec",
		Review:     "Human Review",
		Merge:      "Merge Queue",
		Done:       "Closed",
	}

	d := DecideWithStates(IssueSnapshot{
		State:       "Backlog",
		Description: "spec",
		Metadata:    map[string]string{},
		WorkerID:    "worker-1",
		ExecutionID: "exec-1",
		LeaseTTL:    time.Minute,
	}, now, states)

	if d.Action != ActionClaimAndTransition {
		t.Fatalf("Action = %q, want %q", d.Action, ActionClaimAndTransition)
	}
	if d.ToState != "Doing" {
		t.Fatalf("ToState = %q, want %q", d.ToState, "Doing")
	}
}
