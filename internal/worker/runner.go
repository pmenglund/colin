package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

// Runner executes deterministic state transitions for Linear issues.
type Runner struct {
	Linear    linear.Client
	Executor  InProgressExecutor
	TeamID    string
	WorkerID  string
	PollEvery time.Duration
	LeaseTTL  time.Duration
	DryRun    bool
	Clock     func() time.Time
	Logger    *slog.Logger
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
		r.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	cycleStartedAt := time.Now()
	now := r.Clock().UTC()
	executionID := fmt.Sprintf("%s-%d", r.WorkerID, now.UnixNano())
	r.Logger.Info("worker cycle",
		"worker", r.WorkerID,
		"action", "cycle_start",
		"execution_id", executionID,
		"team", r.TeamID,
		"dry_run", r.DryRun,
	)

	issues, err := r.Linear.ListCandidateIssues(ctx, r.TeamID)
	if err != nil {
		r.Logger.Error("worker cycle failed",
			"worker", r.WorkerID,
			"action", "cycle_error",
			"execution_id", executionID,
			"stage", "list_candidates",
			"detail", err,
		)
		return err
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Identifier == issues[j].Identifier {
			return issues[i].ID < issues[j].ID
		}
		return issues[i].Identifier < issues[j].Identifier
	})
	r.Logger.Info("worker cycle",
		"worker", r.WorkerID,
		"action", "issues_fetched",
		"execution_id", executionID,
		"count", len(issues),
	)

	conflicts := 0
	for _, issue := range issues {
		if err := r.processIssue(ctx, issue.ID, executionID, now); err != nil {
			if errors.Is(err, linear.ErrConflict) {
				conflicts++
				r.Logger.Info("worker issue conflict", "issue", issue.ID, "action", "conflict", "detail", err)
				continue
			}
			r.Logger.Error("worker cycle failed",
				"worker", r.WorkerID,
				"action", "cycle_error",
				"execution_id", executionID,
				"stage", "process_issue",
				"issue", issue.ID,
				"detail", err,
			)
			return err
		}
	}
	r.Logger.Info("worker cycle",
		"worker", r.WorkerID,
		"action", "cycle_complete",
		"execution_id", executionID,
		"processed", len(issues),
		"conflicts", conflicts,
		"duration_ms", time.Since(cycleStartedAt).Milliseconds(),
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
		r.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	r.Logger.Info("worker run", "worker", r.WorkerID, "action", "run_start", "poll_every", pollEvery.String())

	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()

	for {
		if err := r.RunOnce(ctx); err != nil {
			r.Logger.Error("worker run failed", "worker", r.WorkerID, "action", "run_error", "detail", err)
			return err
		}

		select {
		case <-ctx.Done():
			r.Logger.Info("worker run", "worker", r.WorkerID, "action", "run_stop", "reason", ctx.Err())
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
	if issue.StateName == workflow.StateInProgress && r.Executor != nil {
		return r.processInProgressIssue(ctx, issue, executionID, now)
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
	r.Logger.Info("worker decision",
		"execution_id", executionID,
		"issue", issue.Identifier,
		"state", issue.StateName,
		"action", string(decision.Action),
		"to", decision.ToState,
		"reason", decision.Reason,
	)

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

func (r *Runner) processInProgressIssue(ctx context.Context, issue linear.Issue, executionID string, now time.Time) error {
	result, err := r.Executor.EvaluateAndExecute(ctx, issue)
	if err != nil {
		return fmt.Errorf("evaluate and execute in-progress issue %s: %w", issue.Identifier, err)
	}

	if !result.IsWellSpecified {
		needsInput := strings.TrimSpace(result.NeedsInputSummary)
		if needsInput == "" {
			needsInput = "Provide clear scope, acceptance criteria, and implementation constraints."
		}
		comment := fmt.Sprintf(
			"Moved to **Refine** because this task is not specified enough for autonomous execution.\n\nWhat is needed:\n%s",
			needsInput,
		)
		if err := r.applyInProgressOutcome(ctx, issue, workflow.StateRefine, comment, now, map[string]string{
			workflow.MetaNeedsRefine: "true",
			workflow.MetaReason:      "missing required specification for execution",
		}, "refine"); err != nil {
			return err
		}
		r.Logger.Info("worker decision",
			"execution_id", executionID,
			"issue", issue.Identifier,
			"action", "transition",
			"to", workflow.StateRefine,
			"reason", "specification requires refinement",
		)
		return nil
	}

	summary := strings.TrimSpace(result.ExecutionSummary)
	if summary == "" {
		summary = "Codex execution completed; no additional details were provided."
	}
	comment := fmt.Sprintf("Moved to **Human Review** after Codex execution.\n\nThread: `%s`\n\nSummary:\n%s", strings.TrimSpace(result.ThreadID), summary)
	if err := r.applyInProgressOutcome(ctx, issue, workflow.StateHumanReview, comment, now, map[string]string{
		workflow.MetaNeedsRefine:         "false",
		workflow.MetaReadyForHumanReview: "true",
		workflow.MetaReason:              "",
	}, "human_review"); err != nil {
		return err
	}
	r.Logger.Info("worker decision",
		"execution_id", executionID,
		"issue", issue.Identifier,
		"action", "transition",
		"to", workflow.StateHumanReview,
		"reason", "issue processed by codex",
	)
	return nil
}

func (r *Runner) applyInProgressOutcome(
	ctx context.Context,
	issue linear.Issue,
	toState string,
	comment string,
	now time.Time,
	set map[string]string,
	outcome string,
) error {
	comment = strings.TrimSpace(comment)
	commentID := commentFingerprint(comment)

	patch := linear.MetadataPatch{
		Set: map[string]string{
			workflow.MetaLastHeartbeatUTC:    now.UTC().Format(time.RFC3339),
			workflow.MetaInProgressOutcome:   outcome,
			workflow.MetaInProgressCommentID: commentID,
		},
		Delete: []string{
			workflow.MetaLeaseOwner,
			workflow.MetaLeaseExecutionID,
			workflow.MetaLeaseExpiresAtUTC,
		},
	}
	for k, v := range set {
		if strings.TrimSpace(v) == "" {
			patch.Delete = append(patch.Delete, k)
			continue
		}
		patch.Set[k] = v
	}

	if patch.HasChanges() {
		if err := r.Linear.UpdateIssueMetadata(ctx, issue.ID, patch); err != nil {
			return err
		}
	}

	alreadyCommented := issue.Metadata[workflow.MetaInProgressOutcome] == outcome &&
		issue.Metadata[workflow.MetaInProgressCommentID] == commentID
	if !alreadyCommented {
		if err := r.Linear.CreateIssueComment(ctx, issue.ID, comment); err != nil {
			return err
		}
	}

	if !workflow.CanTransition(issue.StateName, toState) {
		return fmt.Errorf("invalid transition %q -> %q", issue.StateName, toState)
	}
	if issue.StateName == toState {
		return nil
	}
	return r.Linear.UpdateIssueState(ctx, issue.ID, toState)
}

func commentFingerprint(comment string) string {
	sum := sha256.Sum256([]byte(comment))
	return hex.EncodeToString(sum[:8])
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
