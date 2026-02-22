package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/logging"
	"github.com/pmenglund/colin/internal/workflow"
)

// Runner executes deterministic state transitions for Linear issues.
type Runner struct {
	Linear         linear.Client
	Executor       InProgressExecutor
	MergeExecutor  MergeExecutor
	Bootstrapper   TaskBootstrapper
	TeamID         string
	ProjectFilter  []string
	WorkerID       string
	PollEvery      time.Duration
	LeaseTTL       time.Duration
	MaxConcurrency int
	DryRun         bool
	States         workflow.States
	Clock          func() time.Time
	Logger         *slog.Logger
}

// Validate reports whether the runner has the required dependencies and
// configuration to execute a polling cycle.
func (r *Runner) Validate() error {
	if r.Linear == nil {
		return errors.New("runner linear client is required")
	}
	if r.WorkerID == "" {
		return errors.New("runner worker id is required")
	}
	if r.LeaseTTL <= 0 {
		return errors.New("runner lease ttl must be positive")
	}
	if err := r.runtimeStates().Validate(); err != nil {
		return fmt.Errorf("runner workflow states: %w", err)
	}
	return nil
}

// RunOnce processes one polling cycle.
func (r *Runner) RunOnce(ctx context.Context) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Clock == nil {
		r.Clock = time.Now
	}
	if r.Logger == nil {
		r.Logger = logging.NewSlog(nil, logging.LevelInfo)
	}

	cycleStartedAt := time.Now()
	now := r.Clock().UTC()
	executionID := fmt.Sprintf("%s-%d", r.WorkerID, now.UnixNano())
	r.Logger.Debug("worker cycle",
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
	issues = filterIssuesByProject(issues, r.ProjectFilter)
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Identifier == issues[j].Identifier {
			return issues[i].ID < issues[j].ID
		}
		return issues[i].Identifier < issues[j].Identifier
	})
	if len(issues) == 0 {
		r.Logger.Debug("worker cycle",
			"worker", r.WorkerID,
			"action", "issues_fetched",
			"execution_id", executionID,
			"count", len(issues),
		)
	} else {
		r.Logger.Info("worker cycle",
			"worker", r.WorkerID,
			"action", "issues_fetched",
			"execution_id", executionID,
			"count", len(issues),
		)
	}

	type issueRunResult struct {
		issueID string
		err     error
	}
	states := r.runtimeStates()
	mergeIssueToProcess := ""
	for _, issue := range issues {
		if issue.StateName == states.Merge {
			mergeIssueToProcess = issue.ID
			break
		}
	}
	maxConcurrency := r.MaxConcurrency
	if maxConcurrency <= 0 {
		if len(issues) == 0 {
			maxConcurrency = 1
		} else {
			maxConcurrency = len(issues)
		}
	}

	issueCtx, cancelIssues := context.WithCancel(ctx)
	defer cancelIssues()

	results := make(chan issueRunResult, len(issues))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for _, issue := range issues {
		if issue.StateName == states.Merge && mergeIssueToProcess != "" && issue.ID != mergeIssueToProcess {
			r.Logger.Info("worker decision",
				"execution_id", executionID,
				"issue", issue.Identifier,
				"state", issue.StateName,
				"action", "noop",
				"reason", "merge queue serialized; deferred to next cycle",
			)
			results <- issueRunResult{issueID: issue.ID, err: nil}
			continue
		}
		issueID := issue.ID
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := r.processIssue(issueCtx, issueID, executionID, now)
			if err != nil && !errors.Is(err, linear.ErrConflict) {
				cancelIssues()
			}
			results <- issueRunResult{
				issueID: issueID,
				err:     err,
			}
		}()
	}
	wg.Wait()
	close(results)

	conflicts := 0
	var firstErr error
	firstErrIssue := ""
	for result := range results {
		if result.err == nil {
			continue
		}
		if errors.Is(result.err, linear.ErrConflict) {
			conflicts++
			r.Logger.Info("worker issue conflict", "issue", result.issueID, "action", "conflict", "detail", result.err)
			continue
		}
		if firstErr == nil {
			firstErr = result.err
			firstErrIssue = result.issueID
		}
	}

	if firstErr != nil {
		r.Logger.Error("worker cycle failed",
			"worker", r.WorkerID,
			"action", "cycle_error",
			"execution_id", executionID,
			"stage", "process_issue",
			"issue", firstErrIssue,
			"detail", firstErr,
		)
		return firstErr
	}
	if len(issues) == 0 {
		r.Logger.Debug("worker cycle",
			"worker", r.WorkerID,
			"action", "cycle_complete",
			"execution_id", executionID,
			"processed", len(issues),
			"conflicts", conflicts,
			"duration_ms", time.Since(cycleStartedAt).Milliseconds(),
		)
	} else {
		r.Logger.Info("worker cycle",
			"worker", r.WorkerID,
			"action", "cycle_complete",
			"execution_id", executionID,
			"processed", len(issues),
			"conflicts", conflicts,
			"duration_ms", time.Since(cycleStartedAt).Milliseconds(),
		)
	}

	return nil
}

// Run starts a continuous polling loop until context cancellation.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.Validate(); err != nil {
		return err
	}

	pollEvery := r.PollEvery
	if pollEvery <= 0 {
		pollEvery = 30 * time.Second
	}
	if r.Logger == nil {
		r.Logger = logging.NewSlog(nil, logging.LevelInfo)
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
	states := r.runtimeStates()
	if issue.StateName == states.InProgress && r.Executor != nil {
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

	decision := workflow.DecideWithStates(snapshot, now, states)
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
	if shouldExecuteMerge(issue.StateName, decision, states) {
		if r.MergeExecutor == nil {
			return errors.New("runner merge executor is required for merge transitions")
		}
		if err := r.MergeExecutor.ExecuteMerge(ctx, issue); err != nil {
			return fmt.Errorf("execute merge for issue %s: %w", issue.Identifier, err)
		}
	}

	if shouldBootstrapWorkspace(issue.StateName, decision, states) {
		if r.Bootstrapper != nil {
			workspace, err := r.Bootstrapper.EnsureTaskWorkspace(ctx, issue.Identifier)
			if err != nil {
				return fmt.Errorf("bootstrap workspace for issue %s: %w", issue.Identifier, err)
			}
			r.Logger.Info("worker bootstrap complete",
				"execution_id", executionID,
				"issue", issue.Identifier,
				"worktree", workspace.WorktreePath,
				"branch", workspace.BranchName,
			)
			if decision.MetadataPatch == nil {
				decision.MetadataPatch = map[string]string{}
			}
			decision.MetadataPatch[workflow.MetaWorktreePath] = workspace.WorktreePath
			decision.MetadataPatch[workflow.MetaBranchName] = workspace.BranchName
		}
	}

	patch := toMetadataPatch(decision, now)
	if patch.HasChanges() {
		if err := r.Linear.UpdateIssueMetadata(ctx, issue.ID, patch); err != nil {
			return err
		}
	}

	if decision.Action != workflow.ActionNoop && decision.ToState != "" {
		if !states.CanTransition(issue.StateName, decision.ToState) {
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
	lease, err := workflow.LeaseFromMetadata(issue.Metadata)
	if err != nil {
		r.Logger.Info("worker decision",
			"execution_id", executionID,
			"issue", issue.Identifier,
			"state", issue.StateName,
			"action", "recover_invalid_lease",
			"reason", "lease metadata invalid; claiming new lease for execution",
		)
		lease = workflow.Lease{}
	}

	if workflow.IsLeaseActive(lease, now) && lease.Owner != r.WorkerID {
		r.Logger.Info("worker decision",
			"execution_id", executionID,
			"issue", issue.Identifier,
			"state", issue.StateName,
			"action", "noop",
			"reason", "active lease owned by another worker",
		)
		return nil
	}

	if !r.DryRun {
		leasePatch := linear.MetadataPatch{
			Set: map[string]string{
				workflow.MetaLastHeartbeatUTC: now.UTC().Format(time.RFC3339),
			},
		}
		for k, v := range workflow.LeaseMetadataMap(workflow.BuildLease(r.WorkerID, executionID, now, r.LeaseTTL)) {
			leasePatch.Set[k] = v
		}
		if err := r.Linear.UpdateIssueMetadata(ctx, issue.ID, leasePatch); err != nil {
			return err
		}
	}

	if err := r.commentTurnExecutionContext(ctx, issue); err != nil {
		return err
	}

	result, err := r.Executor.EvaluateAndExecute(ctx, issue)
	if err != nil {
		return fmt.Errorf("evaluate and execute in-progress issue %s: %w", issue.Identifier, err)
	}

	states := r.runtimeStates()
	if !result.IsWellSpecified {
		needsInput := strings.TrimSpace(result.NeedsInputSummary)
		if needsInput == "" {
			needsInput = "Provide clear scope, acceptance criteria, and implementation constraints."
		}
		comment := fmt.Sprintf(
			"Moved to **%s** because this task is not specified enough for autonomous execution.\n\nWhat is needed:\n%s",
			states.Refine,
			needsInput,
		)
		if err := r.applyInProgressOutcome(ctx, issue, states.Refine, comment, now, map[string]string{
			workflow.MetaNeedsRefine: "true",
			workflow.MetaReason:      "missing required specification for execution",
		}, "refine", result.ThreadID); err != nil {
			return err
		}
		r.Logger.Info("worker decision",
			"execution_id", executionID,
			"issue", issue.Identifier,
			"action", "transition",
			"to", states.Refine,
			"reason", "specification requires refinement",
		)
		return nil
	}

	threadID := strings.TrimSpace(result.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(issue.Metadata[workflow.MetaThreadID])
	}
	comment := buildReviewComment(reviewCommentInput{
		ExecutionSummary: result.ExecutionSummary,
		ReviewStateName:  states.Review,
		ThreadID:         threadID,
		BranchName:       issue.Metadata[workflow.MetaBranchName],
		WorktreePath:     issue.Metadata[workflow.MetaWorktreePath],
		TranscriptRef:    result.TranscriptRef,
		ScreenshotRef:    result.ScreenshotRef,
	})
	if err := r.applyInProgressOutcome(ctx, issue, states.Review, comment, now, map[string]string{
		workflow.MetaNeedsRefine:         "false",
		workflow.MetaReadyForHumanReview: "true",
		workflow.MetaReason:              "",
	}, "human_review", threadID); err != nil {
		return err
	}
	r.Logger.Info("worker decision",
		"execution_id", executionID,
		"issue", issue.Identifier,
		"action", "transition",
		"to", states.Review,
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
	threadID string,
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
	trimmedThreadID := strings.TrimSpace(threadID)
	if trimmedThreadID != "" {
		patch.Set[workflow.MetaThreadID] = trimmedThreadID
	}

	if patch.HasChanges() {
		if err := r.Linear.UpdateIssueMetadata(ctx, issue.ID, patch); err != nil {
			return err
		}
	}
	if !r.DryRun && trimmedThreadID != "" {
		if err := r.recordBranchSessionMetadata(ctx, issue, trimmedThreadID); err != nil {
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

	if !r.runtimeStates().CanTransition(issue.StateName, toState) {
		return fmt.Errorf("invalid transition %q -> %q", issue.StateName, toState)
	}
	if issue.StateName == toState {
		return nil
	}
	return r.Linear.UpdateIssueState(ctx, issue.ID, toState)
}

func (r *Runner) commentTurnExecutionContext(ctx context.Context, issue linear.Issue) error {
	comment := buildExecutionContextComment(executionContextInput{
		ThreadID:     issue.Metadata[workflow.MetaThreadID],
		BranchName:   issue.Metadata[workflow.MetaBranchName],
		WorktreePath: issue.Metadata[workflow.MetaWorktreePath],
	})
	commentID := commentFingerprint(comment)
	if issue.Metadata[workflow.MetaInProgressContextCommentID] == commentID {
		return nil
	}
	if err := r.Linear.CreateIssueComment(ctx, issue.ID, comment); err != nil {
		return err
	}
	if r.DryRun {
		return nil
	}
	patch := linear.MetadataPatch{
		Set: map[string]string{
			workflow.MetaInProgressContextCommentID: commentID,
		},
	}
	if err := r.Linear.UpdateIssueMetadata(ctx, issue.ID, patch); err != nil {
		return err
	}
	return nil
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

func shouldBootstrapWorkspace(fromState string, decision workflow.Decision, states workflow.States) bool {
	return fromState == states.Todo &&
		decision.Action != workflow.ActionNoop &&
		decision.ToState == states.InProgress
}

func shouldExecuteMerge(fromState string, decision workflow.Decision, states workflow.States) bool {
	return fromState == states.Merge &&
		decision.Action != workflow.ActionNoop &&
		decision.ToState == states.Done
}

func (r *Runner) recordBranchSessionMetadata(ctx context.Context, issue linear.Issue, sessionID string) error {
	sessionWriter, ok := r.Bootstrapper.(TaskSessionMetadataWriter)
	if !ok || sessionWriter == nil {
		return nil
	}

	worktreePath := strings.TrimSpace(issue.Metadata[workflow.MetaWorktreePath])
	branchName := strings.TrimSpace(issue.Metadata[workflow.MetaBranchName])
	if worktreePath == "" || branchName == "" {
		return nil
	}

	if err := sessionWriter.RecordBranchSession(ctx, worktreePath, branchName, sessionID); err != nil {
		return fmt.Errorf("record codex session metadata for issue %s: %w", issue.Identifier, err)
	}
	return nil
}

func (r *Runner) runtimeStates() workflow.States {
	return r.States.WithDefaults()
}

func filterIssuesByProject(issues []linear.Issue, rawFilter []string) []linear.Issue {
	filterSet := normalizeProjectFilter(rawFilter)
	if len(filterSet) == 0 {
		return issues
	}

	out := make([]linear.Issue, 0, len(issues))
	for _, issue := range issues {
		if issueMatchesProjectFilter(issue, filterSet) {
			out = append(out, issue)
		}
	}
	return out
}

func normalizeProjectFilter(rawFilter []string) map[string]struct{} {
	if len(rawFilter) == 0 {
		return nil
	}

	out := make(map[string]struct{}, len(rawFilter))
	for _, raw := range rawFilter {
		normalized := normalizeProjectFilterValue(raw)
		if normalized == "" {
			continue
		}
		out[normalized] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func issueMatchesProjectFilter(issue linear.Issue, filterSet map[string]struct{}) bool {
	if len(filterSet) == 0 {
		return true
	}

	projectID := normalizeProjectFilterValue(issue.ProjectID)
	if projectID != "" {
		if _, ok := filterSet[projectID]; ok {
			return true
		}
	}

	projectName := normalizeProjectFilterValue(issue.ProjectName)
	if projectName != "" {
		if _, ok := filterSet[projectName]; ok {
			return true
		}
	}

	return false
}

func normalizeProjectFilterValue(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
