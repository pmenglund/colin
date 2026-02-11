package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

// Runner executes deterministic state transitions for Linear issues.
type Runner struct {
	Linear    linear.Client
	TeamID    string
	WorkerID  string
	PollEvery time.Duration
	LeaseTTL  time.Duration
	DryRun    bool
	Clock     func() time.Time
	Logger    *log.Logger
}

// RunOnce processes one polling cycle.
func (r *Runner) RunOnce(ctx context.Context) error {
	if r.Linear == nil {
		return errors.New("runner linear client is required")
	}
	if r.WorkerID == "" {
		return errors.New("runner worker id is required")
	}
	if r.LeaseTTL <= 0 {
		return errors.New("runner lease ttl must be positive")
	}
	if r.Clock == nil {
		r.Clock = time.Now
	}
	if r.Logger == nil {
		r.Logger = log.New(log.Writer(), "", log.LstdFlags)
	}

	cycleStartedAt := time.Now()
	now := r.Clock().UTC()
	executionID := fmt.Sprintf("%s-%d", r.WorkerID, now.UnixNano())
	r.Logger.Printf("worker=%s action=cycle_start execution_id=%s team=%q dry_run=%t", r.WorkerID, executionID, r.TeamID, r.DryRun)

	issues, err := r.Linear.ListCandidateIssues(ctx, r.TeamID)
	if err != nil {
		r.Logger.Printf("worker=%s action=cycle_error execution_id=%s stage=list_candidates detail=%v", r.WorkerID, executionID, err)
		return err
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Identifier == issues[j].Identifier {
			return issues[i].ID < issues[j].ID
		}
		return issues[i].Identifier < issues[j].Identifier
	})
	r.Logger.Printf("worker=%s action=issues_fetched execution_id=%s count=%d", r.WorkerID, executionID, len(issues))

	conflicts := 0
	for _, issue := range issues {
		if err := r.processIssue(ctx, issue.ID, executionID, now); err != nil {
			if errors.Is(err, linear.ErrConflict) {
				conflicts++
				r.Logger.Printf("issue=%s action=conflict detail=%v", issue.ID, err)
				continue
			}
			r.Logger.Printf("worker=%s action=cycle_error execution_id=%s stage=process_issue issue=%s detail=%v", r.WorkerID, executionID, issue.ID, err)
			return err
		}
	}
	r.Logger.Printf(
		"worker=%s action=cycle_complete execution_id=%s processed=%d conflicts=%d duration_ms=%d",
		r.WorkerID,
		executionID,
		len(issues),
		conflicts,
		time.Since(cycleStartedAt).Milliseconds(),
	)

	return nil
}

// Run starts a continuous polling loop until context cancellation.
func (r *Runner) Run(ctx context.Context) error {
	pollEvery := r.PollEvery
	if pollEvery <= 0 {
		pollEvery = 30 * time.Second
	}
	if r.Logger == nil {
		r.Logger = log.New(log.Writer(), "", log.LstdFlags)
	}

	r.Logger.Printf("worker=%s action=run_start poll_every=%s", r.WorkerID, pollEvery)

	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()

	for {
		if err := r.RunOnce(ctx); err != nil {
			r.Logger.Printf("worker=%s action=run_error detail=%v", r.WorkerID, err)
			return err
		}

		select {
		case <-ctx.Done():
			r.Logger.Printf("worker=%s action=run_stop reason=%v", r.WorkerID, ctx.Err())
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runner) processIssue(ctx context.Context, issueID string, executionID string, now time.Time) error {
	issue, err := r.Linear.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}

	snapshot := workflow.IssueSnapshot{
		IssueID:     issue.ID,
		Identifier:  issue.Identifier,
		State:       issue.StateName,
		Description: issue.Description,
		Metadata:    copyMetadata(issue.Metadata),
		WorkerID:    r.WorkerID,
		ExecutionID: executionID,
		LeaseTTL:    r.LeaseTTL,
	}

	decision := workflow.Decide(snapshot, now)
	r.Logger.Printf("execution_id=%s issue=%s state=%q action=%s to=%q reason=%q", executionID, issue.Identifier, issue.StateName, decision.Action, decision.ToState, decision.Reason)

	if r.DryRun {
		return nil
	}

	patch := toMetadataPatch(decision, now)
	if patch.HasChanges() {
		if err := r.Linear.UpdateIssueMetadata(ctx, issue.ID, patch); err != nil {
			return err
		}
	}

	if decision.Action != workflow.ActionNoop && decision.ToState != "" {
		if !workflow.CanTransition(issue.StateName, decision.ToState) {
			return fmt.Errorf("invalid transition %q -> %q", issue.StateName, decision.ToState)
		}
		if issue.StateName != decision.ToState {
			if err := r.Linear.UpdateIssueState(ctx, issue.ID, decision.ToState); err != nil {
				return err
			}
		}
	}

	return nil
}

func toMetadataPatch(decision workflow.Decision, now time.Time) linear.MetadataPatch {
	patch := linear.MetadataPatch{
		Set: map[string]string{
			workflow.MetaLastHeartbeatUTC: now.UTC().Format(time.RFC3339),
		},
	}

	for k, v := range decision.MetadataPatch {
		if v == "" {
			patch.Delete = append(patch.Delete, k)
			continue
		}
		patch.Set[k] = v
	}

	if decision.LeasePatch != nil {
		for k, v := range workflow.LeaseMetadataMap(*decision.LeasePatch) {
			patch.Set[k] = v
		}
	}

	return patch
}

func copyMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
