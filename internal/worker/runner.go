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
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

// Runner executes deterministic state transitions for Linear issues.
type Runner struct {
	Linear         linear.Client
	Executor       InProgressExecutor
	MergeExecutor  MergeExecutor
	Bootstrapper   TaskBootstrapper
	BranchMetadata BranchMetadataStore
	TeamID         string
	WorkerID       string
	PollEvery      time.Duration
	LeaseTTL       time.Duration
	MaxConcurrency int
	DryRun         bool
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

	nonMergeIssueIDs := make([]string, 0, len(issues))
	mergeIssues := make([]linear.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.StateName == workflow.StateMerge && r.MergeExecutor != nil {
			mergeIssues = append(mergeIssues, issue)
			continue
		}
		nonMergeIssueIDs = append(nonMergeIssueIDs, issue.ID)
	}

	selectedMergeIssueID := ""
	if len(mergeIssues) > 0 {
		selectedMergeIssueID = mergeIssues[0].ID
		deferredMergeIdentifiers := make([]string, 0, len(mergeIssues)-1)
		for _, mergeIssue := range mergeIssues[1:] {
			deferredMergeIdentifiers = append(deferredMergeIdentifiers, mergeIssue.Identifier)
		}
		r.Logger.Info("worker merge queue",
			"worker", r.WorkerID,
			"action", "merge_queue_select",
			"execution_id", executionID,
			"queue_length", len(mergeIssues),
			"selected_issue", mergeIssues[0].Identifier,
			"selected_issue_id", selectedMergeIssueID,
			"deferred_issues", strings.Join(deferredMergeIdentifiers, ","),
		)
	}

	results := make([]issueRunResult, 0, len(nonMergeIssueIDs)+1)
	results = append(results, r.processIssueIDsConcurrently(ctx, nonMergeIssueIDs, executionID, now)...)

	if selectedMergeIssueID != "" {
		err := r.processIssue(ctx, selectedMergeIssueID, executionID, now)
		results = append(results, issueRunResult{
			issueID: selectedMergeIssueID,
			err:     err,
		})
	}

	conflicts := 0
	var firstErr error
	firstErrIssue := ""
	for _, result := range results {
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
	r.Logger.Info("worker cycle",
		"worker", r.WorkerID,
		"action", "cycle_complete",
		"execution_id", executionID,
		"processed", len(results),
		"conflicts", conflicts,
		"duration_ms", time.Since(cycleStartedAt).Milliseconds(),
	)

	return nil
}

type issueRunResult struct {
	issueID string
	err     error
}

func (r *Runner) processIssueIDsConcurrently(
	ctx context.Context,
	issueIDs []string,
	executionID string,
	now time.Time,
) []issueRunResult {
	maxConcurrency := r.MaxConcurrency
	if maxConcurrency <= 0 {
		if len(issueIDs) == 0 {
			maxConcurrency = 1
		} else {
			maxConcurrency = len(issueIDs)
		}
	}

	issueCtx, cancelIssues := context.WithCancel(ctx)
	defer cancelIssues()

	results := make(chan issueRunResult, len(issueIDs))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for _, issueID := range issueIDs {
		issueID := issueID
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

	out := make([]issueRunResult, 0, len(issueIDs))
	for result := range results {
		out = append(out, result)
	}
	return out
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
	if issue.StateName == workflow.StateMerge && r.MergeExecutor != nil {
		return r.processMergeIssue(ctx, issue, executionID, now)
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

	var workspace TaskBootstrapResult
	if shouldBootstrapWorkspace(issue.StateName, decision) {
		var workspace TaskBootstrapResult
		if r.Bootstrapper != nil {
			workspace, err = r.Bootstrapper.EnsureTaskWorkspace(ctx, issue.Identifier)
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

	patch := toMetadataPatch(decision, now)
	if strings.TrimSpace(workspace.WorktreePath) != "" {
		patch.Set[workflow.MetaTaskWorktreePath] = strings.TrimSpace(workspace.WorktreePath)
	}
	if strings.TrimSpace(workspace.BranchName) != "" {
		patch.Set[workflow.MetaTaskBranchName] = strings.TrimSpace(workspace.BranchName)
	}
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

func (r *Runner) processMergeIssue(ctx context.Context, issue linear.Issue, executionID string, now time.Time) error {
	r.Logger.Info("worker decision",
		"execution_id", executionID,
		"issue", issue.Identifier,
		"state", issue.StateName,
		"action", "execute_merge",
		"to", workflow.StateDone,
		"reason", "issue in merge queue",
	)

	if r.DryRun {
		return nil
	}

	if err := r.MergeExecutor.ExecuteMerge(ctx, issue); err != nil {
		return fmt.Errorf("execute merge for issue %s: %w", issue.Identifier, err)
	}

	patch := linear.MetadataPatch{
		Set: map[string]string{
			workflow.MetaLastHeartbeatUTC: now.UTC().Format(time.RFC3339),
			workflow.MetaMergeReady:       "false",
			workflow.MetaReason:           "",
		},
	}
	if patch.HasChanges() {
		if err := r.Linear.UpdateIssueMetadata(ctx, issue.ID, patch); err != nil {
			return err
		}
	}

	if !workflow.CanTransition(issue.StateName, workflow.StateDone) {
		return fmt.Errorf("invalid transition %q -> %q", issue.StateName, workflow.StateDone)
	}
	if issue.StateName == workflow.StateDone {
		return nil
	}
	return r.Linear.UpdateIssueState(ctx, issue.ID, workflow.StateDone)
}

func (r *Runner) processInProgressIssue(ctx context.Context, issue linear.Issue, executionID string, now time.Time) error {
	if err := r.ensureInProgressWorkspaceMetadata(ctx, &issue); err != nil {
		return err
	}

	result, err := r.Executor.EvaluateAndExecute(ctx, issue)
	if err != nil {
		return fmt.Errorf("evaluate and execute in-progress issue %s: %w", issue.Identifier, err)
	}
	threadID := strings.TrimSpace(result.ThreadID)
	sessionID := firstNonEmpty(result.SessionID, threadID)
	branchName := strings.TrimSpace(issue.Metadata[workflow.MetaBranchName])
	if r.BranchMetadata != nil && branchName != "" && sessionID != "" {
		if err := r.BranchMetadata.SetBranchSessionID(ctx, branchName, sessionID); err != nil {
			return fmt.Errorf("persist git branch metadata for issue %s: %w", issue.Identifier, err)
		}
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
		}, "refine", result); err != nil {
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

	threadID := strings.TrimSpace(result.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(issue.Metadata[workflow.MetaThreadID])
	}
	comment := buildReviewComment(reviewCommentInput{
		ExecutionSummary: result.ExecutionSummary,
		ThreadID:         threadID,
		BranchName:       issue.Metadata[workflow.MetaBranchName],
		WorktreePath:     issue.Metadata[workflow.MetaWorktreePath],
		TranscriptRef:    result.TranscriptRef,
		ScreenshotRef:    result.ScreenshotRef,
	})
	if err := r.applyInProgressOutcome(ctx, issue, workflow.StateReview, comment, now, map[string]string{
		workflow.MetaNeedsRefine:         "false",
		workflow.MetaReadyForHumanReview: "true",
		workflow.MetaReason:              "",
	}, "review", result); err != nil {
		return err
	}
	r.Logger.Info("worker decision",
		"execution_id", executionID,
		"issue", issue.Identifier,
		"action", "transition",
		"to", workflow.StateReview,
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
	result InProgressExecutionResult,
) error {
	comment = strings.TrimSpace(comment)
	commentID := commentFingerprint(comment)
	threadID := strings.TrimSpace(result.ThreadID)
	sessionID := strings.TrimSpace(result.SessionID)
	if sessionID == "" {
		sessionID = threadID
	}

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
	if threadID != "" {
		patch.Set[workflow.MetaCodexThreadID] = threadID
	}
	if sessionID != "" {
		patch.Set[workflow.MetaCodexSessionID] = sessionID
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

func (r *Runner) ensureInProgressWorkspaceMetadata(ctx context.Context, issue *linear.Issue) error {
	if r.Bootstrapper == nil {
		return nil
	}
	if issue == nil {
		return errors.New("issue is required")
	}

	workspace, err := r.Bootstrapper.EnsureTaskWorkspace(ctx, issue.Identifier)
	if err != nil {
		return fmt.Errorf("ensure task workspace for issue %s: %w", issue.Identifier, err)
	}

	expectedWorktree := strings.TrimSpace(workspace.WorktreePath)
	expectedBranch := strings.TrimSpace(workspace.BranchName)
	gotWorktree := strings.TrimSpace(issue.Metadata[workflow.MetaTaskWorktreePath])
	gotBranch := strings.TrimSpace(issue.Metadata[workflow.MetaTaskBranchName])

	if gotWorktree == "" && gotBranch == "" {
		patch := linear.MetadataPatch{
			Set: map[string]string{
				workflow.MetaTaskWorktreePath: expectedWorktree,
				workflow.MetaTaskBranchName:   expectedBranch,
			},
		}
		if patch.HasChanges() {
			if err := r.Linear.UpdateIssueMetadata(ctx, issue.ID, patch); err != nil {
				return err
			}
		}
		if issue.Metadata == nil {
			issue.Metadata = map[string]string{}
		}
		issue.Metadata[workflow.MetaTaskWorktreePath] = expectedWorktree
		issue.Metadata[workflow.MetaTaskBranchName] = expectedBranch
		return nil
	}

	if gotWorktree == "" || gotBranch == "" {
		return fmt.Errorf(
			"inconsistent workspace metadata for issue %s (%s=%q, %s=%q): set both keys to expected values (%q, %q) or clear both keys and retry",
			issue.Identifier,
			workflow.MetaTaskWorktreePath, gotWorktree,
			workflow.MetaTaskBranchName, gotBranch,
			expectedWorktree, expectedBranch,
		)
	}

	if gotWorktree != expectedWorktree || gotBranch != expectedBranch {
		return fmt.Errorf(
			"workspace metadata mismatch for issue %s: expected %s=%q and %s=%q, got %s=%q and %s=%q; update metadata to match local workspace or fix local workspace and retry",
			issue.Identifier,
			workflow.MetaTaskWorktreePath, expectedWorktree,
			workflow.MetaTaskBranchName, expectedBranch,
			workflow.MetaTaskWorktreePath, gotWorktree,
			workflow.MetaTaskBranchName, gotBranch,
		)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func shouldBootstrapWorkspace(fromState string, decision workflow.Decision) bool {
	return fromState == workflow.StateTodo &&
		decision.Action != workflow.ActionNoop &&
		decision.ToState == workflow.StateInProgress
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
