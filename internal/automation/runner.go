package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/execplan"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/tracker"
	"github.com/pmenglund/colin/internal/userworkflow"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
)

type Event = codex.Event
type Result = codex.Result

const (
	RunTypeCoding        = codex.RunTypeCoding
	RunTypeReviewPublish = codex.RunTypeReviewPublish
	RunTypeMerge         = codex.RunTypeMerge

	EventWorkspacePrepared    = codex.EventWorkspacePrepared
	EventIssueStateRefreshed  = codex.EventIssueStateRefreshed
	EventReviewPublishStarted = codex.EventReviewPublishStarted
	EventReviewPublishDone    = codex.EventReviewPublishDone
	EventMergeStarted         = codex.EventMergeStarted
	EventRunFailed            = codex.EventRunFailed
	EventContinuationNeeded   = codex.EventContinuationNeeded
	EventRunSucceeded         = codex.EventRunSucceeded
	EventNotification         = codex.EventNotification
	EventMergeDone            = codex.EventMergeDone
)

var (
	ErrTurnTimeout     = codex.ErrTurnTimeout
	ErrTurnCancelled   = codex.ErrTurnCancelled
	ErrTurnInputNeeded = codex.ErrTurnInputNeeded
	ErrTurnFailed      = codex.ErrTurnFailed
)

// Runner executes one issue attempt inside a workspace using the Codex app-server protocol.
type Runner struct {
	cfg        domain.ServiceConfig
	workflow   domain.WorkflowDefinition
	repo       *repoops.Manager
	tracker    tracker.Client
	workspaces *workspace.Manager
	logger     *slog.Logger
}

const (
	outcomeReadyForReview        = "COLIN_OUTCOME: READY_FOR_REVIEW"
	outcomeReadyForMergeRetry    = "COLIN_OUTCOME: READY_FOR_MERGE_RETRY"
	outcomeNeedsSpec             = "COLIN_OUTCOME: NEEDS_SPEC"
	execPlanDecisionOneShotLine  = "COLIN_EXECPLAN_DECISION: ONE_SHOT"
	execPlanDecisionExecPlanLine = "COLIN_EXECPLAN_DECISION: EXEC_PLAN"
	refineStateName              = "Refine"
	metadataOutcomeReady         = "ready_for_review"
	metadataOutcomeSpec          = "needs_spec"
	metadataOutcomeMax           = "max_turns"
	metadataOutcomePlan          = "exec_plan_conflict"
	metadataOutcomePlanInvalid   = "exec_plan_invalid"
	metadataOutcomeMerged        = "merged"
	maxAutomaticMergeRetries     = 2
	mergeRetryDelay              = 30 * time.Second
)

// NewRunner constructs a Runner bound to the current workflow, tracker, and workspace manager.
func NewRunner(cfg domain.ServiceConfig, def domain.WorkflowDefinition, trackerClient tracker.Client, manager *workspace.Manager, logger *slog.Logger) *Runner {
	return newRunner(cfg, def, trackerClient, manager, repoops.NewManager(cfg, logger), logger)
}

func newRunner(cfg domain.ServiceConfig, def domain.WorkflowDefinition, trackerClient tracker.Client, manager *workspace.Manager, repoManager *repoops.Manager, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:        cfg,
		workflow:   def,
		repo:       repoManager,
		tracker:    trackerClient,
		workspaces: manager,
		logger:     logger,
	}
}

// Run executes one worker lifecycle for an issue, including workspace hooks and continuation turns.
func (r *Runner) Run(ctx context.Context, issue domain.Issue, attempt *int, onEvent func(Event)) Result {
	runType := RunTypeCoding
	switch {
	case isPublishState(r.cfg, issue.State):
		runType = RunTypeReviewPublish
	case isMergeState(r.cfg, issue.State):
		runType = RunTypeMerge
	}

	emit := func(event Event) {
		if onEvent == nil {
			return
		}
		event.RunType = runType
		event.Attempt = attemptNumber(attempt)
		if event.State == "" {
			event.State = issue.State
		}
		onEvent(event)
	}

	ws, err := r.workspaces.Ensure(ctx, issue)
	if err != nil {
		return Result{Issue: issue, RunType: runType, Status: "failed", Err: err}
	}
	r.logger.Info(
		"workspace prepared",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", ws.Path,
		"created_now", ws.CreatedNow,
		"attempt", attemptNumber(attempt),
	)
	emit(Event{
		Event:     EventWorkspacePrepared,
		Timestamp: time.Now().UTC(),
		Workspace: ws.Path,
		State:     issue.State,
		Message:   "Workspace prepared",
	})
	issue = r.persistActualBranchNameBestEffort(ctx, issue, ws.Path)
	if err := r.workspaces.RunBeforeRun(ctx, ws.Path); err != nil {
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	defer func() {
		if err := r.workspaces.RunAfterRun(context.Background(), ws.Path); err != nil {
			r.logger.Warn("after_run hook failed", "workspace_path", ws.Path, "error", err)
		}
	}()

	current, err := r.moveActiveIssueToWorkingState(ctx, issue)
	if err != nil {
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	if previousState := strings.TrimSpace(issue.State); previousState != "" && !strings.EqualFold(previousState, strings.TrimSpace(current.State)) {
		emit(Event{
			Event:     EventIssueStateRefreshed,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     current.State,
			PrevState: previousState,
			Message:   fmt.Sprintf("Issue moved from %s to %s before coding started", previousState, current.State),
		})
	}

	client := codex.NewClient(r.cfg, r.logger, emit, current, ws.Path, runType)
	if runType == RunTypeCoding && hasDuplicateExecPlans(current) {
		return r.handleDuplicateExecPlans(ctx, current, ws.Path)
	}
	if isPublishState(r.cfg, issue.State) {
		emit(Event{
			Event:     EventReviewPublishStarted,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Starting publish automation",
		})
		result, err := r.repo.Publish(ctx, issue, ws.Path)
		if err != nil {
			if errors.Is(err, repoops.ErrNoReviewableChanges) {
				return r.handlePublishWithoutReviewableChanges(ctx, issue, ws.Path)
			}
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		r.logger.Info(
			"review automation completed",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"workspace_path", ws.Path,
			"branch", result.Branch,
			"base_ref", result.BaseRef,
			"pr_number", result.PRNumber,
			"pr_url", result.PRURL,
			"action", result.Action,
		)
		emit(Event{
			Event:     EventReviewPublishDone,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Publish automation completed",
			Branch:    result.Branch,
			BaseRef:   result.BaseRef,
			PRNumber:  result.PRNumber,
			PRURL:     result.PRURL,
			PRState:   result.PRState,
			Action:    result.Action,
		})
		issue = r.persistActualBranchNameValueBestEffort(ctx, issue, result.Branch)
		issue.PullRequest = &domain.PullRequestRef{
			Number:          result.PRNumber,
			URL:             result.PRURL,
			State:           result.PRState,
			HeadRef:         result.PRHeadRef,
			BaseRef:         result.PRBaseRef,
			Backend:         result.PRBackend,
			RepositoryOwner: result.PROwner,
			RepositoryName:  result.PRRepoName,
		}
		issue = r.persistIssueMetadataBestEffort(ctx, issue, codexMetadataWithResult(issue, runType, metadataOutcomeReady, "", result))
		return Result{
			Issue:         issue,
			RunType:       runType,
			WorkspacePath: ws.Path,
			Status:        "succeeded",
			PR: &domain.PullRequestRef{
				Number:          result.PRNumber,
				URL:             result.PRURL,
				State:           result.PRState,
				HeadRef:         result.PRHeadRef,
				BaseRef:         result.PRBaseRef,
				Backend:         result.PRBackend,
				RepositoryOwner: result.PROwner,
				RepositoryName:  result.PRRepoName,
			},
		}
	}
	if isMergeState(r.cfg, issue.State) {
		emit(Event{
			Event:     EventMergeStarted,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Starting merge automation",
		})
		reviewContext, err := r.repo.ReviewContext(ctx, issue, ws.Path)
		if err != nil {
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		current, summary, blocked, err := r.blockMergeForCodexReview(ctx, issue, reviewContext)
		if err != nil {
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		if blocked {
			return Result{
				Issue:         current,
				RunType:       runType,
				WorkspacePath: ws.Path,
				Status:        mergeReviewBlockedStatus(issue.State, current.State),
				Summary:       summary,
				PR:            pullRequestRef(reviewContext.PullRequest),
			}
		}
		result, err := r.repo.Merge(ctx, issue, ws.Path)
		if err != nil {
			if repoops.IsMergeFailureKind(err, repoops.MergeFailureKindTransient) {
				if attemptNumber(attempt) < maxAutomaticMergeRetries {
					return r.handleBlockedMergeRetry(ctx, issue, ws.Path, result, "", err)
				}
				return r.handleMergeFailure(ctx, issue, ws.Path, result, err)
			}
			if isHumanMergeFailure(err) {
				return r.handleRecoverableMergeFailure(ctx, issue, ws.Path, emit, result, err, attemptNumber(attempt))
			}
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		return r.buildMergedResult(ctx, issue, ws.Path, result, emit)
	}

	if err := client.Start(ctx, ws.Path); err != nil {
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	current = r.persistCodexThreadIDBestEffort(ctx, current, client.ThreadID())
	r.logger.Info(
		"codex session ready",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", ws.Path,
		"thread_id", client.ThreadID(),
	)
	defer client.Stop()

	current, err = r.ensureExecPlanDecision(ctx, client, ws.Path, current)
	if err != nil {
		return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: client.FinalSummary(), Err: err}
	}

	current, err = r.ensureExecPlan(ctx, client, ws.Path, current)
	if err != nil {
		if errors.Is(err, tracker.ErrDuplicateExecPlans) {
			return r.handleDuplicateExecPlans(ctx, current, ws.Path)
		}
		return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: client.FinalSummary(), Err: err}
	}

	var (
		execPlanCopy     *execplan.WorkingCopy
		execPlanProgress execplan.Progress
	)
	if runType == RunTypeCoding && r.execPlanTrackingEnabled(current) {
		execPlanCopy, execPlanProgress, err = r.prepareExecPlanWorkingCopy(current)
		if err != nil {
			return r.handleInvalidExecPlan(ctx, current, ws.Path, fmt.Sprintf("the stored ExecPlan is not usable: %v", err), nil)
		}
		defer func() {
			if err := execPlanCopy.Close(); err != nil {
				r.logger.Warn("failed to remove exec plan working copy", "path", execPlanCopy.Path(), "error", err)
			}
		}()
	}

	maxTurnsReached := false
	for turn := 1; turn <= r.cfg.Agent.MaxTurns; turn++ {
		prompt, err := workflow.RenderPrompt(r.workflow, current, attempt)
		if err != nil {
			return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: client.FinalSummary(), Err: err}
		}
		if turn == 1 && r.cfg.Agent.CreateExecPlan {
			prompt = injectExecPlanPrompt(prompt, current.ExecPlan, execPlanCopy, execPlanProgress)
		}
		if turn > 1 {
			prompt = buildCodingContinuationPrompt(current.Identifier, execPlanCopy, execPlanProgress)
		}
		r.logger.Info(
			"runner turn starting",
			"issue_id", current.ID,
			"issue_identifier", current.Identifier,
			"workspace_path", ws.Path,
			"state", current.State,
			"turn", turn,
			"max_turns", r.cfg.Agent.MaxTurns,
			"continuation", turn > 1,
		)
		startedAt := time.Now()
		if err := client.RunTurn(ctx, ws.Path, current, prompt); err != nil {
			duration := time.Since(startedAt).Round(time.Millisecond)
			r.logger.Warn(
				"runner turn failed",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"workspace_path", ws.Path,
				"state", current.State,
				"turn", turn,
				"duration", duration.String(),
				"error", err,
			)
			emit(Event{
				Event:     EventRunFailed,
				Timestamp: time.Now().UTC(),
				Workspace: ws.Path,
				State:     current.State,
				Duration:  duration,
				Message:   err.Error(),
			})
			status := "failed"
			switch {
			case errors.Is(err, ErrTurnTimeout):
				status = "timed_out"
			case errors.Is(err, ErrTurnCancelled):
				status = "failed"
			case errors.Is(err, ErrTurnInputNeeded):
				status = "failed"
			}
			return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: status, Err: err}
		}
		if execPlanCopy != nil {
			current, execPlanProgress, err = r.syncIssueExecPlanFromWorkingCopy(ctx, current, execPlanCopy)
			if err != nil {
				if errors.Is(err, tracker.ErrDuplicateExecPlans) {
					return r.handleDuplicateExecPlans(ctx, current, ws.Path)
				}
				return r.handleInvalidExecPlan(ctx, current, ws.Path, err.Error(), execPlanProgress.Remaining())
			}
		}

		duration := time.Since(startedAt).Round(time.Millisecond)
		previousState := current.State
		issues, err := r.tracker.FetchIssueStatesByIDs(ctx, []string{current.ID})
		if err != nil {
			return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		refreshed := false
		if len(issues) > 0 {
			current = mergeIssueContext(current, issues[0])
			refreshed = true
		} else {
			r.logger.Warn(
				"issue state refresh returned no rows",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"turn", turn,
			)
		}
		active := isActive(r.cfg, current.State)
		r.logger.Info(
			"runner turn completed",
			"issue_id", current.ID,
			"issue_identifier", current.Identifier,
			"workspace_path", ws.Path,
			"turn", turn,
			"duration", duration.String(),
			"previous_state", previousState,
			"current_state", current.State,
			"state_refreshed", refreshed,
			"still_active", active,
		)
		emit(Event{
			Event:     EventIssueStateRefreshed,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     current.State,
			PrevState: previousState,
			Duration:  duration,
			Message:   fmt.Sprintf("Turn %d completed; issue state is now %s", turn, current.State),
		})
		if !active {
			r.logger.Info(
				"issue left active states; stopping runner",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"turn", turn,
				"current_state", current.State,
			)
			break
		}
		if summary := strings.TrimSpace(client.FinalSummary()); summary != "" {
			handoffOutcome, _ := parseCodingSummaryOutcome(summary)
			if handoffOutcome == outcomeNeedsSpec {
				r.logger.Info(
					"runner turn produced an explicit needs-spec outcome; finishing coding run",
					"issue_id", current.ID,
					"issue_identifier", current.Identifier,
					"current_state", current.State,
					"turn", turn,
				)
				break
			}
			reviewable, err := r.reviewableCodingArtifact(ctx, ws.Path, current)
			if err != nil {
				return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: client.FinalSummary(), Err: err}
			}
			if reviewable {
				if execPlanCopy != nil && !execPlanProgress.AllCompleted() {
					client.ClearFinalSummary()
					r.logger.Info(
						"runner turn produced ready-for-review before exec plan progress was complete; continuing",
						"issue_id", current.ID,
						"issue_identifier", current.Identifier,
						"current_state", current.State,
						"turn", turn,
						"remaining_progress_items", len(execPlanProgress.Remaining()),
					)
					if turn == r.cfg.Agent.MaxTurns {
						maxTurnsReached = true
						break
					}
					emit(Event{
						Event:     EventContinuationNeeded,
						Timestamp: time.Now().UTC(),
						Workspace: ws.Path,
						State:     current.State,
						Message:   fmt.Sprintf("ExecPlan progress still has %d remaining tasks; continuing in the same run", len(execPlanProgress.Remaining())),
					})
					continue
				}
				r.logger.Info(
					"runner turn produced an explicit ready-for-review outcome with reviewable repository changes; finishing coding run",
					"issue_id", current.ID,
					"issue_identifier", current.Identifier,
					"current_state", current.State,
					"turn", turn,
				)
				break
			}
			client.ClearFinalSummary()
			r.logger.Info(
				"runner turn produced a ready-for-review outcome without reviewable repository changes; continuing",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"current_state", current.State,
				"turn", turn,
			)
		}
		if turn < r.cfg.Agent.MaxTurns {
			r.logger.Info(
				"issue still active after turn and no final summary was captured; continuing",
				"issue_id", current.ID,
				"issue_identifier", current.Identifier,
				"current_state", current.State,
				"next_turn", turn+1,
				"max_turns", r.cfg.Agent.MaxTurns,
			)
			emit(Event{
				Event:     EventContinuationNeeded,
				Timestamp: time.Now().UTC(),
				Workspace: ws.Path,
				State:     current.State,
				Message:   fmt.Sprintf("Issue is still active in %s; continuing in the same run", current.State),
			})
			continue
		}
		r.logger.Warn(
			"max turns reached while issue is still active",
			"issue_id", current.ID,
			"issue_identifier", current.Identifier,
			"current_state", current.State,
			"max_turns", r.cfg.Agent.MaxTurns,
		)
		maxTurnsReached = true
	}

	summary := client.FinalSummary()
	prRef := (*domain.PullRequestRef)(nil)
	threadsHandled := 0
	threadsRemaining := 0
	if len(current.ReviewThreads) > 0 {
		var blocked bool
		current, prRef, threadsHandled, threadsRemaining, summary, blocked = r.finalizeReviewThreads(ctx, current, ws.Path, summary)
		if blocked {
			return Result{
				Issue:            current,
				RunType:          runType,
				WorkspacePath:    ws.Path,
				Status:           "blocked",
				Summary:          summary,
				PR:               prRef,
				ThreadsHandled:   threadsHandled,
				ThreadsRemaining: threadsRemaining,
			}
		}
	}

	handoffOutcome, summary := parseCodingSummaryOutcome(summary)
	if maxTurnsReached {
		summary = appendMaxTurnsSummary(summary, current.State, r.cfg.Agent.MaxTurns)
		if execPlanCopy != nil {
			summary = appendRemainingExecPlanTasks(summary, execPlanProgress.Remaining())
		}
	}
	current, err = r.persistIssueMetadata(ctx, current, codexMetadataWithDirective(current, runType, codingOutcome(handoffOutcome, maxTurnsReached), "", ""))
	if err != nil {
		return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: summary, PR: prRef, ThreadsHandled: threadsHandled, ThreadsRemaining: threadsRemaining, Err: err}
	}
	current, err = r.moveSuccessfulCodingRunToHandoffState(ctx, current, codingHandoffState(handoffOutcome, maxTurnsReached, r.cfg.Repo.PublishStates))
	if err != nil {
		return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Summary: summary, PR: prRef, ThreadsHandled: threadsHandled, ThreadsRemaining: threadsRemaining, Err: err}
	}
	if r.shouldResetPersistentThreads(current.State) {
		current = r.clearPersistentThreadMetadataBestEffort(ctx, current)
	}

	emit(Event{
		Event:     EventRunSucceeded,
		Timestamp: time.Now().UTC(),
		Workspace: ws.Path,
		State:     current.State,
		Message:   "Run completed successfully",
	})
	return Result{
		Issue:            current,
		RunType:          runType,
		WorkspacePath:    ws.Path,
		Status:           "succeeded",
		Summary:          summary,
		PR:               prRef,
		ThreadsHandled:   threadsHandled,
		ThreadsRemaining: threadsRemaining,
	}
}

func (r *Runner) blockMergeForCodexReview(ctx context.Context, issue domain.Issue, reviewContext repoops.ReviewContext) (domain.Issue, string, bool, error) {
	summary, blocked, moveToReview := buildMergeBlockedSummary(r.cfg, reviewContext)
	if !blocked {
		return issue, "", false, nil
	}
	if pr := pullRequestRef(reviewContext.PullRequest); pr != nil {
		issue.PullRequest = pr
	}
	issue.ReviewThreads = append([]domain.ReviewThread(nil), reviewContext.CodexReviewThreads...)
	if moveToReview {
		if r.tracker == nil {
			return issue, "", false, errors.New("missing tracker client")
		}
		targetState, ok := firstConfiguredState(r.cfg.Repo.PublishStates)
		if !ok {
			targetState = "Review"
		}
		if !strings.EqualFold(issue.State, targetState) {
			if err := r.tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
				return issue, "", false, fmt.Errorf("update issue state to %s: %w", targetState, err)
			}
			issue.State = targetState
			now := time.Now().UTC()
			issue.UpdatedAt = &now
		}
	}
	return issue, summary, true, nil
}

func mergeReviewBlockedStatus(previousState string, currentState string) string {
	if config.StateKey(previousState) == config.StateKey(currentState) {
		return "blocked"
	}
	return "succeeded"
}

func isAutomaticMergeRetryFailure(err error) bool {
	return repoops.IsMergeFailureKind(err, repoops.MergeFailureKindTransient) ||
		repoops.IsMergeFailureKind(err, repoops.MergeFailureKindBaseAdvanced)
}

func (r *Runner) handleMergeFailure(ctx context.Context, issue domain.Issue, workspacePath string, result repoops.Result, err error) Result {
	reviewState := mergeReviewState(r.cfg)
	updated, updateErr := r.moveIssueToState(ctx, issue, reviewState)
	if updateErr != nil {
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: updateErr}
	}
	updated = r.persistActualBranchNameValueBestEffort(ctx, updated, result.Branch)
	updated = r.persistIssueMetadataBestEffort(ctx, updated, codexMetadata(updated, RunTypeMerge, metadataOutcomeReady, ""))

	return Result{
		Issue:         updated,
		RunType:       RunTypeMerge,
		WorkspacePath: workspacePath,
		Status:        "succeeded",
		Summary:       buildMergeFailureSummary(result, reviewState, err),
		PR: &domain.PullRequestRef{
			Number: result.PRNumber,
			URL:    result.PRURL,
			State:  result.PRState,
		},
	}
}

func (r *Runner) handleBlockedMergeRetry(ctx context.Context, issue domain.Issue, workspacePath string, result repoops.Result, recoverySummary string, mergeErr error) Result {
	issue = r.persistActualBranchNameValueBestEffort(ctx, issue, result.Branch)
	issue.PullRequest = &domain.PullRequestRef{
		Number:          result.PRNumber,
		URL:             result.PRURL,
		State:           result.PRState,
		HeadRef:         result.PRHeadRef,
		BaseRef:         result.PRBaseRef,
		Backend:         result.PRBackend,
		RepositoryOwner: result.PROwner,
		RepositoryName:  result.PRRepoName,
	}
	return Result{
		Issue:         issue,
		RunType:       RunTypeMerge,
		WorkspacePath: workspacePath,
		Status:        "blocked",
		Summary:       buildMergeRetrySummary(result, mergeErr, recoverySummary),
		PR:            pullRequestRef(*issue.PullRequest),
		RetryDelay:    mergeRetryDelay,
	}
}

func (r *Runner) handleInvalidMergeRecovery(ctx context.Context, issue domain.Issue, workspacePath string, result repoops.Result, mergeErr error, validation repoops.MergeRecoveryValidation, recoverySummary string) Result {
	reviewState := mergeReviewState(r.cfg)
	updated, updateErr := r.moveIssueToState(ctx, issue, reviewState)
	if updateErr != nil {
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: updateErr}
	}
	updated = r.persistActualBranchNameValueBestEffort(ctx, updated, result.Branch)
	updated = r.persistIssueMetadataBestEffort(ctx, updated, codexMetadata(updated, RunTypeMerge, metadataOutcomeReady, ""))

	return Result{
		Issue:         updated,
		RunType:       RunTypeMerge,
		WorkspacePath: workspacePath,
		Status:        "succeeded",
		Summary:       buildMergeRecoveryValidationFailureSummary(result, reviewState, mergeErr, validation, recoverySummary),
		PR: &domain.PullRequestRef{
			Number:          result.PRNumber,
			URL:             result.PRURL,
			State:           result.PRState,
			HeadRef:         result.PRHeadRef,
			BaseRef:         result.PRBaseRef,
			Backend:         result.PRBackend,
			RepositoryOwner: result.PROwner,
			RepositoryName:  result.PRRepoName,
		},
	}
}

func (r *Runner) handleRecoverableMergeFailure(ctx context.Context, issue domain.Issue, workspacePath string, emit func(Event), result repoops.Result, mergeErr error, attempt int) Result {
	r.logger.Info(
		"merge conflict detected; starting codex merge recovery",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", workspacePath,
		"branch", result.Branch,
		"base_ref", result.BaseRef,
		"pr_number", result.PRNumber,
		"error", mergeErr,
	)
	emit(Event{
		Event:     EventNotification,
		Timestamp: time.Now().UTC(),
		Workspace: workspacePath,
		State:     issue.State,
		Message:   "Merge conflict detected; asking Codex to repair the branch before retrying merge.",
	})

	client := codex.NewClient(r.cfg, r.logger, emit, issue, workspacePath, RunTypeMerge)
	if err := client.Start(ctx, workspacePath); err != nil {
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: err}
	}
	issue = r.persistCodexThreadIDBestEffort(ctx, issue, client.ThreadID())
	r.logger.Info(
		"codex session ready for merge recovery",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", workspacePath,
		"thread_id", client.ThreadID(),
	)
	defer client.Stop()

	recoverySummary, ready, err := r.runMergeRecoveryTurns(ctx, client, issue, workspacePath, result, mergeErr, emit)
	if err != nil {
		if shouldHandoffMergeRecoveryError(err) {
			return r.handleMergeRecoveryFailure(ctx, issue, workspacePath, result, mergeErr, err.Error(), client.LastOutput())
		}
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: err}
	}
	if !ready {
		return r.handleMergeRecoveryFailure(ctx, issue, workspacePath, result, mergeErr, "Codex did not finish the merge-conflict repair within the configured turn limit.", client.LastOutput())
	}

	published, err := r.repo.Publish(ctx, issue, workspacePath)
	if err != nil {
		if isHumanMergeFailure(err) {
			return r.handleMergeFailure(ctx, issue, workspacePath, published, err)
		}
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: err}
	}
	issue = r.persistActualBranchNameValueBestEffort(ctx, issue, published.Branch)
	issue.PullRequest = &domain.PullRequestRef{
		Number:          published.PRNumber,
		URL:             published.PRURL,
		State:           published.PRState,
		HeadRef:         published.PRHeadRef,
		BaseRef:         published.PRBaseRef,
		Backend:         published.PRBackend,
		RepositoryOwner: published.PROwner,
		RepositoryName:  published.PRRepoName,
	}
	validation, err := r.repo.ValidateMergeRecovery(ctx, workspacePath, result, published)
	if err != nil {
		return r.handleMergeRecoveryFailure(ctx, issue, workspacePath, published, mergeErr, fmt.Sprintf("failed to validate the repaired branch before retrying merge: %v", err), recoverySummary)
	}
	if !validation.Valid() {
		return r.handleInvalidMergeRecovery(ctx, issue, workspacePath, published, mergeErr, validation, recoverySummary)
	}

	reviewContext, err := r.repo.ReviewContext(ctx, issue, workspacePath)
	if err != nil {
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: err}
	}
	current, _, blocked, err := r.blockMergeForCodexReview(ctx, issue, reviewContext)
	if err != nil {
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: err}
	}
	if blocked {
		return Result{
			Issue:         current,
			RunType:       RunTypeMerge,
			WorkspacePath: workspacePath,
			Status:        mergeReviewBlockedStatus(issue.State, current.State),
			Summary:       buildMergeRecoveryReviewBlockedSummary(r.cfg, recoverySummary, reviewContext),
			PR:            pullRequestRef(reviewContext.PullRequest),
		}
	}

	r.logger.Info(
		"merge conflict repaired; retrying merge",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", workspacePath,
		"branch", published.Branch,
		"base_ref", published.BaseRef,
		"pr_number", published.PRNumber,
	)
	emit(Event{
		Event:     EventNotification,
		Timestamp: time.Now().UTC(),
		Workspace: workspacePath,
		State:     issue.State,
		Message:   "Merge conflict repaired; retrying merge.",
	})

	merged, err := r.repo.MergePullRequest(ctx, workspacePath, published)
	if err != nil {
		if isAutomaticMergeRetryFailure(err) && attempt < maxAutomaticMergeRetries {
			return r.handleBlockedMergeRetry(ctx, issue, workspacePath, merged, recoverySummary, err)
		}
		if isHumanMergeFailure(err) {
			return r.handleMergeFailure(ctx, issue, workspacePath, merged, err)
		}
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: err}
	}
	return r.buildMergedResult(ctx, issue, workspacePath, merged, emit)
}

func (r *Runner) runMergeRecoveryTurns(ctx context.Context, client *codex.Client, issue domain.Issue, workspacePath string, result repoops.Result, mergeErr error, emit func(Event)) (string, bool, error) {
	for turn := 1; turn <= r.cfg.Agent.MaxTurns; turn++ {
		prompt := buildMergeRecoveryPrompt(issue, result, mergeErr)
		if turn > 1 {
			prompt = buildMergeRecoveryContinuationPrompt(issue, result)
		}
		r.logger.Info(
			"merge recovery turn starting",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"workspace_path", workspacePath,
			"turn", turn,
			"max_turns", r.cfg.Agent.MaxTurns,
			"continuation", turn > 1,
		)
		startedAt := time.Now()
		if err := client.RunTurn(ctx, workspacePath, issue, prompt); err != nil {
			duration := time.Since(startedAt).Round(time.Millisecond)
			r.logger.Warn(
				"merge recovery turn failed",
				"issue_id", issue.ID,
				"issue_identifier", issue.Identifier,
				"workspace_path", workspacePath,
				"turn", turn,
				"duration", duration.String(),
				"error", err,
			)
			emit(Event{
				Event:     EventRunFailed,
				Timestamp: time.Now().UTC(),
				Workspace: workspacePath,
				State:     issue.State,
				Duration:  duration,
				Message:   err.Error(),
			})
			return "", false, err
		}

		duration := time.Since(startedAt).Round(time.Millisecond)
		r.logger.Info(
			"merge recovery turn completed",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"workspace_path", workspacePath,
			"turn", turn,
			"duration", duration.String(),
		)
		emit(Event{
			Event:     EventIssueStateRefreshed,
			Timestamp: time.Now().UTC(),
			Workspace: workspacePath,
			State:     issue.State,
			PrevState: issue.State,
			Duration:  duration,
			Message:   fmt.Sprintf("Merge recovery turn %d completed; issue state is still %s", turn, issue.State),
		})

		if outcome, summary := parseMergeRecoverySummaryOutcome(client.FinalSummary()); outcome == outcomeReadyForMergeRetry {
			return summary, true, nil
		}
		if turn < r.cfg.Agent.MaxTurns {
			emit(Event{
				Event:     EventContinuationNeeded,
				Timestamp: time.Now().UTC(),
				Workspace: workspacePath,
				State:     issue.State,
				Message:   fmt.Sprintf("Merge recovery is still in progress in %s; continuing in the same run", issue.State),
			})
		}
	}
	return strings.TrimSpace(client.LastOutput()), false, nil
}

func (r *Runner) handleMergeRecoveryFailure(ctx context.Context, issue domain.Issue, workspacePath string, result repoops.Result, mergeErr error, reason string, recoveryOutput string) Result {
	reviewState := mergeReviewState(r.cfg)
	updated, updateErr := r.moveIssueToState(ctx, issue, reviewState)
	if updateErr != nil {
		return Result{Issue: issue, RunType: RunTypeMerge, WorkspacePath: workspacePath, Status: "failed", Err: updateErr}
	}
	updated = r.persistActualBranchNameValueBestEffort(ctx, updated, result.Branch)
	updated = r.persistIssueMetadataBestEffort(ctx, updated, codexMetadata(updated, RunTypeMerge, metadataOutcomeReady, ""))

	return Result{
		Issue:         updated,
		RunType:       RunTypeMerge,
		WorkspacePath: workspacePath,
		Status:        "succeeded",
		Summary:       buildMergeRecoveryFailureSummary(result, reviewState, mergeErr, reason, recoveryOutput),
		PR: &domain.PullRequestRef{
			Number: result.PRNumber,
			URL:    result.PRURL,
			State:  result.PRState,
		},
	}
}

func (r *Runner) buildMergedResult(ctx context.Context, issue domain.Issue, workspacePath string, result repoops.Result, emit func(Event)) Result {
	r.logger.Info(
		"merge automation completed",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", workspacePath,
		"branch", result.Branch,
		"base_ref", result.BaseRef,
		"pr_number", result.PRNumber,
		"pr_url", result.PRURL,
		"pr_state", result.PRState,
		"action", result.Action,
	)
	emit(Event{
		Event:     EventMergeDone,
		Timestamp: time.Now().UTC(),
		Workspace: workspacePath,
		State:     issue.State,
		Message:   "Merge automation completed",
		Branch:    result.Branch,
		BaseRef:   result.BaseRef,
		PRNumber:  result.PRNumber,
		PRURL:     result.PRURL,
		PRState:   result.PRState,
		Action:    result.Action,
	})
	issue = r.applyPostMergeState(ctx, issue, result.BaseRef)
	if r.shouldResetPersistentThreads(issue.State) {
		issue = r.clearPersistentThreadMetadataBestEffort(ctx, issue)
	}
	issue = r.clearManagedCodexReviewLabelsBestEffort(ctx, issue)
	issue = r.persistActualBranchNameValueBestEffort(ctx, issue, result.Branch)
	issue.PullRequest = &domain.PullRequestRef{
		Number:          result.PRNumber,
		URL:             result.PRURL,
		State:           result.PRState,
		HeadRef:         result.PRHeadRef,
		BaseRef:         result.PRBaseRef,
		Backend:         result.PRBackend,
		RepositoryOwner: result.PROwner,
		RepositoryName:  result.PRRepoName,
	}
	issue = r.persistIssueMetadataBestEffort(ctx, issue, codexMetadataWithResult(issue, RunTypeMerge, metadataOutcomeMerged, "", result))
	return Result{
		Issue:         issue,
		RunType:       RunTypeMerge,
		WorkspacePath: workspacePath,
		Status:        "succeeded",
		PR: &domain.PullRequestRef{
			Number:          result.PRNumber,
			URL:             result.PRURL,
			State:           result.PRState,
			HeadRef:         result.PRHeadRef,
			BaseRef:         result.PRBaseRef,
			Backend:         result.PRBackend,
			RepositoryOwner: result.PROwner,
			RepositoryName:  result.PRRepoName,
		},
	}
}

func (r *Runner) clearManagedCodexReviewLabelsBestEffort(ctx context.Context, issue domain.Issue) domain.Issue {
	if r.tracker == nil {
		return issue
	}
	kept := issue.Labels[:0]
	for _, labelName := range issue.Labels {
		managed := false
		for _, managedLabel := range domain.ManagedCodexReviewLabels() {
			if strings.EqualFold(strings.TrimSpace(labelName), managedLabel) {
				managed = true
				if err := r.tracker.RemoveIssueLabel(ctx, issue.ID, managedLabel); err != nil {
					r.logger.Warn("failed to remove managed codex review label after merge", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "label", managedLabel, "error", err)
					kept = append(kept, labelName)
				}
				break
			}
		}
		if !managed {
			kept = append(kept, labelName)
		}
	}
	issue.Labels = kept
	return issue
}

func (r *Runner) handlePublishWithoutReviewableChanges(ctx context.Context, issue domain.Issue, workspacePath string) Result {
	targetState := workingActiveState(r.cfg)
	updated, updateErr := r.moveIssueToState(ctx, issue, targetState)
	if updateErr != nil {
		return Result{Issue: issue, RunType: RunTypeReviewPublish, WorkspacePath: workspacePath, Status: "failed", Err: updateErr}
	}

	return Result{
		Issue:         updated,
		RunType:       RunTypeReviewPublish,
		WorkspacePath: workspacePath,
		Status:        "succeeded",
		Summary:       buildNoReviewableChangesSummary(targetState),
	}
}

func (r *Runner) handleDuplicateExecPlans(ctx context.Context, issue domain.Issue, workspacePath string) Result {
	updated, updateErr := r.moveIssueToState(ctx, issue, refineStateName)
	if updateErr != nil {
		return Result{Issue: issue, RunType: RunTypeCoding, WorkspacePath: workspacePath, Status: "failed", Err: updateErr}
	}
	updated = r.clearPersistentThreadMetadataBestEffort(ctx, updated)
	updated = r.persistIssueMetadataBestEffort(ctx, updated, codexMetadata(updated, RunTypeCoding, metadataOutcomePlan, ""))
	return Result{
		Issue:         updated,
		RunType:       RunTypeCoding,
		WorkspacePath: workspacePath,
		Status:        "succeeded",
		Summary:       buildDuplicateExecPlanSummary(updated),
	}
}

func (r *Runner) moveActiveIssueToWorkingState(ctx context.Context, issue domain.Issue) (domain.Issue, error) {
	if !isActive(r.cfg, issue.State) {
		return issue, nil
	}

	targetState, ok := nextConfiguredState(r.cfg.Tracker.ActiveStates, issue.State)
	if !ok {
		return issue, nil
	}
	if err := r.tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
		return issue, fmt.Errorf("update issue state to %s: %w", targetState, err)
	}
	r.logger.Info(
		"issue moved to working state before coding run",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"previous_state", issue.State,
		"current_state", targetState,
	)
	issue.State = targetState
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	return issue, nil
}

func (r *Runner) moveSuccessfulCodingRunToHandoffState(ctx context.Context, issue domain.Issue, targetState string) (domain.Issue, error) {
	if !isActive(r.cfg, issue.State) {
		return issue, nil
	}

	targetState = strings.TrimSpace(targetState)
	if targetState == "" || config.ContainsState([]string{issue.State}, targetState) {
		return issue, nil
	}

	if err := r.tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
		return issue, fmt.Errorf("update issue state to %s: %w", targetState, err)
	}
	r.logger.Info(
		"issue moved to handoff state after coding run",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"previous_state", issue.State,
		"current_state", targetState,
	)
	issue.State = targetState
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	return issue, nil
}

func (r *Runner) applyPostMergeState(ctx context.Context, issue domain.Issue, targetBranch string) domain.Issue {
	if r.tracker == nil {
		return issue
	}

	stateName, ok, err := r.tracker.ResolveGitAutomationState(ctx, issue.ID, "merge", targetBranch)
	if err != nil {
		r.logger.Warn(
			"failed to resolve post-merge Linear automation state",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"target_branch", targetBranch,
			"error", err,
		)
		return issue
	}
	if !ok || strings.TrimSpace(stateName) == "" || config.ContainsState([]string{issue.State}, stateName) {
		return issue
	}
	if err := r.tracker.UpdateIssueState(ctx, issue.ID, stateName); err != nil {
		r.logger.Warn(
			"failed to update issue to post-merge Linear automation state",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"target_branch", targetBranch,
			"state", stateName,
			"error", err,
		)
		return issue
	}

	r.logger.Info(
		"issue moved to configured post-merge Linear automation state",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"previous_state", issue.State,
		"current_state", stateName,
		"target_branch", targetBranch,
	)
	issue.State = stateName
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	return issue
}

func mergeIssueContext(previous, refreshed domain.Issue) domain.Issue {
	refreshed.Description = firstStringPtr(refreshed.Description, previous.Description)
	refreshed.Priority = firstIntPtr(refreshed.Priority, previous.Priority)
	refreshed.BranchName = firstStringPtr(refreshed.BranchName, previous.BranchName)
	refreshed.URL = firstStringPtr(refreshed.URL, previous.URL)
	if len(refreshed.Labels) == 0 {
		refreshed.Labels = append([]string(nil), previous.Labels...)
	}
	if len(refreshed.BlockedBy) == 0 {
		refreshed.BlockedBy = append([]domain.BlockerRef(nil), previous.BlockedBy...)
	}
	if refreshed.ReviewCycle == nil {
		refreshed.ReviewCycle = previous.ReviewCycle
	}
	if len(refreshed.ReviewFeedback) == 0 {
		refreshed.ReviewFeedback = append([]domain.ReviewFeedback(nil), previous.ReviewFeedback...)
	}
	if len(refreshed.ReviewThreads) == 0 {
		refreshed.ReviewThreads = append([]domain.ReviewThread(nil), previous.ReviewThreads...)
	}
	if refreshed.ColinMetadata == nil {
		refreshed.ColinMetadata = previous.ColinMetadata
	}
	if refreshed.ExecPlan == nil {
		refreshed.ExecPlan = previous.ExecPlan
	}
	if refreshed.ExecPlanCount == 0 && previous.ExecPlanCount > 0 {
		refreshed.ExecPlanCount = previous.ExecPlanCount
	}
	if refreshed.PullRequest == nil {
		refreshed.PullRequest = previous.PullRequest
	}
	if refreshed.CreatedAt == nil {
		refreshed.CreatedAt = previous.CreatedAt
	}
	return refreshed
}

func (r *Runner) finalizeReviewThreads(ctx context.Context, issue domain.Issue, workspacePath string, summary string) (domain.Issue, *domain.PullRequestRef, int, int, string, bool) {
	reviewContext, err := r.repo.ReviewContext(ctx, issue, workspacePath)
	if err != nil {
		return issue, nil, 0, len(issue.ReviewThreads), buildReviewBlockedSummary(summary, nil, 0, len(issue.ReviewThreads), fmt.Sprintf("failed to fetch GitHub review threads: %v", err)), true
	}
	if reviewContext.PullRequest.Number == 0 && strings.TrimSpace(reviewContext.PullRequest.URL) == "" {
		return issue, nil, 0, len(issue.ReviewThreads), buildReviewBlockedSummary(summary, nil, 0, len(issue.ReviewThreads), "no pull request found for the issue branch"), true
	}
	pr := &reviewContext.PullRequest
	targetThreadID := pendingReviewThreadID(issue)
	if len(reviewContext.Threads) == 0 {
		if targetThreadID != "" {
			issue = r.clearPendingReviewFollowUpBestEffort(ctx, issue)
		}
		return issue, pr, 0, 0, buildReviewReadySummary(summary, pr, 0, 0), false
	}
	if targetThreadID != "" {
		targetThread, ok := reviewThreadByID(reviewContext.Threads, targetThreadID)
		if !ok {
			issue = r.clearPendingReviewFollowUpBestEffort(ctx, issue)
			return issue, pr, 0, 0, buildTargetedReviewReadySummary(summary, pr, 0, len(reviewContext.Threads), true), false
		}
		replyBody := buildReviewThreadReplyBody(summary)
		if err := r.repo.ReplyAndResolveReviewThread(ctx, workspacePath, targetThread, replyBody); err != nil {
			r.logger.Warn(
				"failed to reply to or resolve targeted GitHub review thread",
				"issue_id", issue.ID,
				"issue_identifier", issue.Identifier,
				"thread_id", targetThread.ID,
				"path", targetThread.Path,
				"error", err,
			)
			return issue, pr, 0, 1, buildReviewBlockedSummary(summary, pr, 0, 1, ""), true
		}
		postContext, err := r.repo.ReviewContext(ctx, issue, workspacePath)
		if err != nil {
			return issue, pr, 1, 1, buildReviewBlockedSummary(summary, pr, 1, 1, fmt.Sprintf("failed to verify GitHub review threads after update: %v", err)), true
		}
		if postContext.PullRequest.Number != 0 || strings.TrimSpace(postContext.PullRequest.URL) != "" {
			pr = &postContext.PullRequest
		}
		if _, ok := reviewThreadByID(postContext.Threads, targetThreadID); ok {
			return issue, pr, 1, 1, buildReviewBlockedSummary(summary, pr, 1, 1, ""), true
		}
		issue = r.clearPendingReviewFollowUpBestEffort(ctx, issue)
		return issue, pr, 1, 0, buildTargetedReviewReadySummary(summary, pr, 1, len(postContext.Threads), false), false
	}

	replyBody := buildReviewThreadReplyBody(summary)
	handled := 0
	failures := 0
	for _, thread := range reviewContext.Threads {
		if err := r.repo.ReplyAndResolveReviewThread(ctx, workspacePath, thread, replyBody); err != nil {
			failures++
			r.logger.Warn(
				"failed to reply to or resolve GitHub review thread",
				"issue_id", issue.ID,
				"issue_identifier", issue.Identifier,
				"thread_id", thread.ID,
				"path", thread.Path,
				"error", err,
			)
			continue
		}
		handled++
	}

	postContext, err := r.repo.ReviewContext(ctx, issue, workspacePath)
	if err != nil {
		return issue, pr, handled, len(reviewContext.Threads), buildReviewBlockedSummary(summary, pr, handled, len(reviewContext.Threads), fmt.Sprintf("failed to verify GitHub review threads after update: %v", err)), true
	}
	remaining := len(postContext.Threads)
	if postContext.PullRequest.Number != 0 || strings.TrimSpace(postContext.PullRequest.URL) != "" {
		pr = &postContext.PullRequest
	}
	if failures > 0 || remaining > 0 {
		return issue, pr, handled, remaining, buildReviewBlockedSummary(summary, pr, handled, remaining, ""), true
	}
	return issue, pr, handled, 0, buildReviewReadySummary(summary, pr, handled, 0), false
}

func buildReviewThreadReplyBody(_ string) string {
	return "[colin] Addressed in the latest update. See the Linear issue comment for the Why/Before/After/Evidence summary."
}

func buildReviewReadySummary(summary string, pr *domain.PullRequestRef, handled int, remaining int) string {
	return userworkflow.ReviewReady(pr, handled, remaining, summary)
}

func buildReviewBlockedSummary(summary string, pr *domain.PullRequestRef, handled int, remaining int, reason string) string {
	return userworkflow.ReviewBlocked(pr, handled, remaining, reason, summary)
}

func buildTargetedReviewReadySummary(summary string, pr *domain.PullRequestRef, handled int, untouched int, stale bool) string {
	result := buildReviewReadySummary(summary, pr, handled, 0)
	switch {
	case stale:
		return result + "\n\n- Note: The reacted GitHub review thread was already resolved before Colin finalized the follow-up."
	case untouched > 0:
		return result + fmt.Sprintf("\n\n- Note: Left `%d` other unresolved GitHub review threads untouched because this follow-up was scoped to the reacted thread.", untouched)
	default:
		return result
	}
}

func reviewThreadByID(threads []domain.ReviewThread, threadID string) (domain.ReviewThread, bool) {
	threadID = strings.TrimSpace(threadID)
	for _, thread := range threads {
		if strings.EqualFold(strings.TrimSpace(thread.ID), threadID) {
			return thread, true
		}
	}
	return domain.ReviewThread{}, false
}

func pendingReviewThreadID(issue domain.Issue) string {
	if issue.ColinMetadata == nil {
		return ""
	}
	return strings.TrimSpace(issue.ColinMetadata.PendingReviewThreadID)
}

func buildMergeBlockedSummary(cfg domain.ServiceConfig, reviewContext repoops.ReviewContext) (string, bool, bool) {
	block := mergeReviewBlockDisposition(cfg, reviewContext)
	if !block.Blocked {
		return "", false, false
	}

	if block.MoveToReview {
		return userworkflow.MergeReturnedToReview(reviewContext.PullRequest, block.ThreadCount), true, true
	}
	return userworkflow.MergeWaitingForReview(reviewContext.PullRequest, block.WaitingForPickup, block.PendingApproval), true, false
}

func codexReviewApprovalPending(reviewContext repoops.ReviewContext) bool {
	if reviewContext.CodexReviewRequestedAt == nil && reviewContext.CodexReviewApprovedAt != nil {
		return false
	}
	if reviewContext.CodexReviewRequestedAt == nil {
		return false
	}
	if reviewContext.CodexReviewApprovedAt == nil {
		return true
	}
	return !reviewContext.CodexReviewApprovedAt.After(*reviewContext.CodexReviewRequestedAt)
}

type mergeReviewBlock struct {
	Blocked          bool
	MoveToReview     bool
	PendingApproval  bool
	WaitingForPickup bool
	ThreadCount      int
}

func mergeReviewBlockDisposition(cfg domain.ServiceConfig, reviewContext repoops.ReviewContext) mergeReviewBlock {
	if strings.EqualFold(strings.TrimSpace(reviewContext.PullRequest.State), "MERGED") {
		return mergeReviewBlock{}
	}

	block := mergeReviewBlock{
		ThreadCount: len(reviewContext.CodexReviewThreads),
	}
	if !cfg.Repo.CodexPRReviewsEnabled {
		return block
	}
	if block.ThreadCount > 0 {
		block.Blocked = true
		block.MoveToReview = true
		return block
	}
	if reviewContext.CodexReviewObserved {
		return block
	}
	if reviewContext.CodexReviewRequestedAt == nil {
		if reviewContext.CodexReviewApprovedAt != nil {
			return block
		}
		block.Blocked = true
		block.WaitingForPickup = true
		return block
	}
	block.PendingApproval = codexReviewApprovalPending(reviewContext)
	if block.PendingApproval {
		block.Blocked = true
	}
	return block
}

func pullRequestRef(pr domain.PullRequestRef) *domain.PullRequestRef {
	if pr.Number == 0 && strings.TrimSpace(pr.URL) == "" && strings.TrimSpace(pr.State) == "" {
		return nil
	}
	value := pr
	return &value
}

func firstStringPtr(value, fallback *string) *string {
	if value != nil {
		return value
	}
	return fallback
}

func firstIntPtr(value, fallback *int) *int {
	if value != nil {
		return value
	}
	return fallback
}

func attemptNumber(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func isActive(cfg domain.ServiceConfig, state string) bool {
	stateKey := strings.ToLower(strings.TrimSpace(state))
	for _, active := range cfg.Tracker.ActiveStates {
		if strings.ToLower(strings.TrimSpace(active)) == stateKey {
			return true
		}
	}
	return false
}

func isPublishState(cfg domain.ServiceConfig, state string) bool {
	return config.ContainsState(cfg.Repo.PublishStates, state)
}

func isMergeState(cfg domain.ServiceConfig, state string) bool {
	return config.ContainsState(cfg.Repo.MergeStates, state)
}

func firstConfiguredState(states []string) (string, bool) {
	for _, state := range states {
		state = strings.TrimSpace(state)
		if state != "" {
			return state, true
		}
	}
	return "", false
}

func lastConfiguredState(states []string) (string, bool) {
	for i := len(states) - 1; i >= 0; i-- {
		state := strings.TrimSpace(states[i])
		if state != "" {
			return state, true
		}
	}
	return "", false
}

func mergeReviewState(cfg domain.ServiceConfig) string {
	if state, ok := firstConfiguredState(cfg.Repo.PublishStates); ok {
		return state
	}
	return "Review"
}

func nextConfiguredState(states []string, current string) (string, bool) {
	currentKey := config.StateKey(current)
	for i, state := range states {
		if config.StateKey(state) != currentKey {
			continue
		}
		for _, candidate := range states[i+1:] {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				return candidate, true
			}
		}
		return "", false
	}
	return "", false
}

func parseCodingSummaryOutcome(summary string) (string, string) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", ""
	}

	lines := strings.Split(summary, "\n")
	first := strings.TrimSpace(lines[0])
	switch first {
	case outcomeNeedsSpec:
		return outcomeNeedsSpec, strings.TrimSpace(strings.Join(lines[1:], "\n"))
	case outcomeReadyForReview:
		return outcomeReadyForReview, strings.TrimSpace(strings.Join(lines[1:], "\n"))
	default:
		return "", summary
	}
}

func parseMergeRecoverySummaryOutcome(summary string) (string, string) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", ""
	}

	lines := strings.Split(summary, "\n")
	first := strings.TrimSpace(lines[0])
	switch first {
	case outcomeReadyForMergeRetry:
		return outcomeReadyForMergeRetry, strings.TrimSpace(strings.Join(lines[1:], "\n"))
	default:
		return "", summary
	}
}

func codingOutcome(handoffOutcome string, maxTurnsReached bool) string {
	if maxTurnsReached {
		return metadataOutcomeMax
	}
	if handoffOutcome == outcomeNeedsSpec {
		return metadataOutcomeSpec
	}
	return metadataOutcomeReady
}

func codingHandoffState(handoffOutcome string, maxTurnsReached bool, publishStates []string) string {
	if maxTurnsReached || handoffOutcome == outcomeNeedsSpec {
		return refineStateName
	}
	targetState, _ := firstConfiguredState(publishStates)
	return targetState
}

func workingActiveState(cfg domain.ServiceConfig) string {
	if state, ok := lastConfiguredState(cfg.Tracker.ActiveStates); ok {
		return state
	}
	return "In Progress"
}

func appendMaxTurnsSummary(summary string, state string, maxTurns int) string {
	note := fmt.Sprintf("Colin reached the maximum of `%d` turns while the issue remained in `%s`, so it is handing off for human refinement before more implementation work.", maxTurns, state)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return note
	}
	return summary + "\n\n" + note
}

func shouldHandoffMergeRecoveryError(err error) bool {
	return errors.Is(err, ErrTurnTimeout) || errors.Is(err, ErrTurnFailed) || errors.Is(err, ErrTurnInputNeeded)
}

func isHumanMergeFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "not mergeable") ||
		strings.Contains(message, "merge commit cannot be cleanly created") ||
		strings.Contains(message, "base branch was modified") ||
		strings.Contains(message, "resolve the merge conflicts locally") ||
		strings.Contains(message, "review and try the merge again")
}

func buildMergeFailureSummary(result repoops.Result, reviewState string, err error) string {
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	return userworkflow.MergeFailure(domain.PullRequestRef{
		Number: result.PRNumber,
		URL:    result.PRURL,
		State:  result.PRState,
	}, result.Branch, result.BaseRef, reason, reviewState)
}

func buildMergeRetrySummary(result repoops.Result, err error, recoverySummary string) string {
	reason := "GitHub is still recalculating the pull request mergeability."
	if repoops.IsMergeFailureKind(err, repoops.MergeFailureKindBaseAdvanced) {
		reason = fmt.Sprintf("the base branch `%s` moved again after Colin prepared the branch for merge", result.BaseRef)
	}
	return userworkflow.MergeRetrying(domain.PullRequestRef{
		Number:          result.PRNumber,
		URL:             result.PRURL,
		State:           result.PRState,
		HeadRef:         result.PRHeadRef,
		BaseRef:         result.PRBaseRef,
		Backend:         result.PRBackend,
		RepositoryOwner: result.PROwner,
		RepositoryName:  result.PRRepoName,
	}, result.Branch, result.BaseRef, reason, recoverySummary)
}

func buildMergeRecoveryPrompt(issue domain.Issue, result repoops.Result, mergeErr error) string {
	var b strings.Builder
	b.WriteString("Repair the merge conflict for the Linear issue below so Colin can retry the GitHub merge.\n\n")
	b.WriteString("You are working in the issue branch workspace that GitHub reported as not mergeable.\n")
	b.WriteString("Fetch the base branch, merge it into the current branch, resolve any conflicts without dropping valid changes from either side, run focused verification, and leave the branch ready for Colin to publish and retry the merge.\n\n")
	b.WriteString("Return a short answer.\n")
	b.WriteString("The first line must be exactly:\n")
	b.WriteString(outcomeReadyForMergeRetry + "\n\n")
	b.WriteString("Only return that line when the branch is ready for Colin to retry the merge.\n")
	b.WriteString("After the first line, include a brief 1-3 sentence summary of what you resolved and any verification you ran.\n\n")
	b.WriteString("Issue context:\n")
	b.WriteString(fmt.Sprintf("- Identifier: %s\n", issue.Identifier))
	b.WriteString(fmt.Sprintf("- Title: %s\n", issue.Title))
	b.WriteString(fmt.Sprintf("- State: %s\n", issue.State))
	if result.PRNumber > 0 {
		b.WriteString(fmt.Sprintf("- PR: #%d\n", result.PRNumber))
	}
	if strings.TrimSpace(result.PRURL) != "" {
		b.WriteString(fmt.Sprintf("- PR URL: %s\n", result.PRURL))
	}
	if strings.TrimSpace(result.Branch) != "" {
		b.WriteString(fmt.Sprintf("- Branch: %s\n", result.Branch))
	}
	if strings.TrimSpace(result.BaseRef) != "" {
		b.WriteString(fmt.Sprintf("- Base ref: %s\n", result.BaseRef))
	}
	if mergeErr != nil {
		b.WriteString("\nOriginal merge error:\n\n")
		b.WriteString(strings.TrimSpace(mergeErr.Error()))
		b.WriteString("\n")
	}
	b.WriteString("\nRequirements:\n")
	b.WriteString("- Merge the latest base branch into the current branch inside this workspace.\n")
	b.WriteString("- Resolve conflicts fully; do not leave conflict markers or an unfinished merge.\n")
	b.WriteString("- Preserve both the branch changes and the relevant base-branch updates.\n")
	b.WriteString("- Run the most relevant focused checks for the conflicted files.\n")
	b.WriteString("- Do not move the Linear issue or open/close PRs yourself; Colin will handle publish and merge retry after your turn.\n")
	return strings.TrimSpace(b.String())
}

func buildMergeRecoveryContinuationPrompt(issue domain.Issue, result repoops.Result) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Continue resolving the merge conflict for %s.", issue.Identifier))
	if strings.TrimSpace(result.BaseRef) != "" {
		b.WriteString(fmt.Sprintf(" The goal is to leave the current branch ready to merge %s.", result.BaseRef))
	}
	b.WriteString("\n\nReturn `")
	b.WriteString(outcomeReadyForMergeRetry)
	b.WriteString("` only when the branch is fully ready for Colin to retry the merge.")
	return strings.TrimSpace(b.String())
}

func buildMergeRecoveryFailureSummary(result repoops.Result, reviewState string, mergeErr error, reason string, recoveryOutput string) string {
	return userworkflow.MergeRecoveryFailure(domain.PullRequestRef{
		Number: result.PRNumber,
		URL:    result.PRURL,
		State:  result.PRState,
	}, result.Branch, result.BaseRef, mergeErr, reason, reviewState, recoveryOutput)
}

func buildMergeRecoveryValidationFailureSummary(result repoops.Result, reviewState string, mergeErr error, validation repoops.MergeRecoveryValidation, recoverySummary string) string {
	reason := "Codex reported the merge recovery as ready, but Colin could not verify that the branch was actually updated for merge retry."
	if !validation.HeadChanged && validation.PreviousHeadSHA != "" && validation.CurrentHeadSHA != "" {
		reason = fmt.Sprintf("Codex reported the merge recovery as ready, but the branch head did not change (%s -> %s).", validation.PreviousHeadSHA, validation.CurrentHeadSHA)
	} else if !validation.RemoteHeadMatches && validation.CurrentHeadSHA != "" && validation.RemoteHeadSHA != "" {
		reason = fmt.Sprintf("Codex reported the merge recovery as ready, but the pushed branch head (%s) does not match origin (%s).", validation.CurrentHeadSHA, validation.RemoteHeadSHA)
	} else if !validation.ContainsExpectedBase && validation.ExpectedBaseSHA != "" {
		reason = fmt.Sprintf("Codex reported the merge recovery as ready, but the branch still does not contain the expected base commit %s.", validation.ExpectedBaseSHA)
	}
	evidence := mergeRecoveryValidationEvidence(validation)
	return userworkflow.MergeRecoveryValidationFailure(domain.PullRequestRef{
		Number:          result.PRNumber,
		URL:             result.PRURL,
		State:           result.PRState,
		HeadRef:         result.PRHeadRef,
		BaseRef:         result.PRBaseRef,
		Backend:         result.PRBackend,
		RepositoryOwner: result.PROwner,
		RepositoryName:  result.PRRepoName,
	}, result.Branch, result.BaseRef, reviewState, reason, recoverySummary, evidence, mergeErr)
}

func buildMergeRecoveryReviewBlockedSummary(cfg domain.ServiceConfig, recoverySummary string, reviewContext repoops.ReviewContext) string {
	block := mergeReviewBlockDisposition(cfg, reviewContext)
	return userworkflow.MergeRecoveryReviewBlocked(reviewContext.PullRequest, recoverySummary, block.WaitingForPickup, block.PendingApproval, block.ThreadCount)
}

func mergeRecoveryValidationEvidence(validation repoops.MergeRecoveryValidation) []string {
	var evidence []string
	if validation.PreviousHeadSHA != "" {
		evidence = append(evidence, fmt.Sprintf("- Branch head before recovery: `%s`", validation.PreviousHeadSHA))
	}
	if validation.CurrentHeadSHA != "" {
		evidence = append(evidence, fmt.Sprintf("- Branch head after recovery: `%s`", validation.CurrentHeadSHA))
	}
	if validation.RemoteHeadSHA != "" {
		evidence = append(evidence, fmt.Sprintf("- Branch head on origin: `%s`", validation.RemoteHeadSHA))
	}
	if validation.ExpectedBaseSHA != "" {
		evidence = append(evidence, fmt.Sprintf("- Expected base commit before retry: `%s`", validation.ExpectedBaseSHA))
	}
	if validation.CurrentBaseSHA != "" {
		evidence = append(evidence, fmt.Sprintf("- Current base commit: `%s`", validation.CurrentBaseSHA))
	}
	if validation.MergeBaseSHA != "" {
		evidence = append(evidence, fmt.Sprintf("- Current merge-base: `%s`", validation.MergeBaseSHA))
	}
	return evidence
}

func buildNoReviewableChangesSummary(targetState string) string {
	return userworkflow.NoReviewableChanges(targetState)
}

func buildDuplicateExecPlanSummary(issue domain.Issue) string {
	lines := []string{
		fmt.Sprintf("Colin found multiple `Colin ExecPlan` attachments, so it moved the issue to `%s` for human cleanup before continuing.", refineStateName),
		"- What to fix: remove the duplicate ExecPlan attachments and leave exactly one canonical `Colin ExecPlan` on the issue.",
		"- Next step: once the issue has exactly one ExecPlan attachment, move it back to an active coding state to continue from that plan.",
	}
	if issue.ExecPlanCount > 1 {
		lines = append(lines, fmt.Sprintf("- Duplicate count: `%d`", issue.ExecPlanCount))
	}
	return strings.Join(lines, "\n")
}

func buildInvalidExecPlanSummary(reason string, remaining []string) string {
	lines := []string{
		fmt.Sprintf("Colin moved this issue to `%s` because the ExecPlan-backed coding run cannot continue safely.", refineStateName),
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		lines = append(lines, "- Blocker: "+reason)
	}
	if len(remaining) > 0 {
		lines = append(lines, "- Remaining ExecPlan tasks:")
		for _, item := range remaining {
			lines = append(lines, "  - "+strings.TrimSpace(item))
		}
	}
	lines = append(lines, "- What Colin is doing next: stopping implementation until the ExecPlan or issue state is fixed.")
	lines = append(lines, "- What you should do: update the ExecPlan or issue details, then move the issue back to active work.")
	return strings.Join(lines, "\n")
}

func appendRemainingExecPlanTasks(summary string, remaining []string) string {
	if len(remaining) == 0 {
		return strings.TrimSpace(summary)
	}
	lines := []string{"Remaining ExecPlan tasks:"}
	for _, item := range remaining {
		lines = append(lines, "- "+strings.TrimSpace(item))
	}
	note := strings.Join(lines, "\n")
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return note
	}
	return summary + "\n\n" + note
}

func codexMetadata(issue domain.Issue, runType string, outcome string, summaryCommentID string) domain.ColinMetadata {
	return codexMetadataWithDirective(issue, runType, outcome, summaryCommentID, reviewPublishDirective(issue))
}

func codexMetadataWithResult(issue domain.Issue, runType string, outcome string, summaryCommentID string, result repoops.Result) domain.ColinMetadata {
	metadata := codexMetadata(issue, runType, outcome, summaryCommentID)
	metadata.PullRequestNumber = result.PRNumber
	metadata.PullRequestURL = strings.TrimSpace(result.PRURL)
	metadata.PullRequestState = strings.TrimSpace(result.PRState)
	metadata.PullRequestHeadRef = strings.TrimSpace(result.PRHeadRef)
	metadata.PullRequestBaseRef = strings.TrimSpace(result.PRBaseRef)
	metadata.PullRequestBackend = strings.TrimSpace(result.PRBackend)
	metadata.PullRequestRepoOwner = strings.TrimSpace(result.PROwner)
	metadata.PullRequestRepoName = strings.TrimSpace(result.PRRepoName)
	return metadata
}

func codexMetadataWithDirective(issue domain.Issue, runType string, outcome string, summaryCommentID string, directive string) domain.ColinMetadata {
	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	metadata.ReviewPublishDirective = domain.ReviewPublishDirective(strings.TrimSpace(directive))
	metadata.LastRunType = domain.RunType(strings.TrimSpace(runType))
	metadata.LastOutcome = domain.RunOutcome(strings.TrimSpace(outcome))
	metadata.LastSummaryCommentID = strings.TrimSpace(summaryCommentID)
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return metadata
}

func actualBranchMetadata(issue domain.Issue, branch string) (domain.ColinMetadata, bool) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return domain.ColinMetadata{}, false
	}

	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	if strings.TrimSpace(metadata.ActualBranchName) == branch {
		return metadata, false
	}

	metadata.ActualBranchName = branch
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return metadata, true
}

func codexThreadMetadata(issue domain.Issue, threadID string) (domain.ColinMetadata, bool) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return domain.ColinMetadata{}, false
	}

	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	if strings.TrimSpace(metadata.CodexThreadID) == threadID {
		return metadata, false
	}

	metadata.CodexThreadID = threadID
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return metadata, true
}

func clearedPersistentThreadMetadata(issue domain.Issue) (domain.ColinMetadata, bool) {
	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	if strings.TrimSpace(metadata.CodexThreadID) == "" && strings.TrimSpace(metadata.ProgressRootCommentID) == "" {
		return metadata, false
	}

	metadata.CodexThreadID = ""
	metadata.ProgressRootCommentID = ""
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return metadata, true
}

func clearedPendingReviewFollowUpMetadata(issue domain.Issue) (domain.ColinMetadata, bool) {
	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	if strings.TrimSpace(metadata.PendingReviewThreadID) == "" &&
		strings.TrimSpace(metadata.PendingReviewCommentID) == "" &&
		strings.TrimSpace(metadata.PendingReviewReactionID) == "" &&
		strings.TrimSpace(metadata.PendingReviewReactor) == "" &&
		metadata.PendingReviewRequestedAt == nil {
		return metadata, false
	}

	metadata.PendingReviewThreadID = ""
	metadata.PendingReviewCommentID = ""
	metadata.PendingReviewReactionID = ""
	metadata.PendingReviewReactor = ""
	metadata.PendingReviewRequestedAt = nil
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return metadata, true
}

func reviewPublishDirective(issue domain.Issue) string {
	if issue.ColinMetadata == nil {
		return ""
	}
	return strings.TrimSpace(string(issue.ColinMetadata.ReviewPublishDirective))
}

func (r *Runner) reviewableCodingArtifact(ctx context.Context, workspacePath string, issue domain.Issue) (bool, error) {
	if r.repo == nil {
		return true, nil
	}
	target, err := domain.ResolveTargetForIssue(r.cfg, issue)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(target.RepoURL) == "" {
		return true, nil
	}
	return r.repo.ReviewableArtifact(ctx, workspacePath, issue)
}

func (r *Runner) execPlanTrackingEnabled(issue domain.Issue) bool {
	return r.cfg.Agent.CreateExecPlan &&
		execPlanDecision(issue) == domain.ExecPlanDecisionExecPlan &&
		hasCanonicalExecPlan(issue)
}

func (r *Runner) prepareExecPlanWorkingCopy(issue domain.Issue) (*execplan.WorkingCopy, execplan.Progress, error) {
	body := execPlanBody(issue.ExecPlan)
	progress, err := execplan.ParseProgress(body)
	if err != nil {
		return nil, execplan.Progress{}, err
	}
	copy, err := execplan.NewWorkingCopy(body)
	if err != nil {
		return nil, execplan.Progress{}, err
	}
	return copy, progress, nil
}

func (r *Runner) syncIssueExecPlanFromWorkingCopy(ctx context.Context, issue domain.Issue, workingCopy *execplan.WorkingCopy) (domain.Issue, execplan.Progress, error) {
	body, err := workingCopy.ReadBody()
	if err != nil {
		return issue, execplan.Progress{}, fmt.Errorf("failed to read the ExecPlan working copy at %s: %w", workingCopy.Path(), err)
	}
	progress, err := execplan.ParseProgress(body)
	if err != nil {
		return issue, execplan.Progress{}, fmt.Errorf("failed to parse the ExecPlan working copy at %s: %w", workingCopy.Path(), err)
	}
	body = strings.TrimSpace(body)
	if strings.TrimSpace(execPlanBody(issue.ExecPlan)) == body {
		return issue, progress, nil
	}
	plan := domain.ExecPlan{
		Body: body,
	}
	if issue.ExecPlan != nil {
		plan.AttachmentID = issue.ExecPlan.AttachmentID
	}
	now := time.Now().UTC()
	plan.UpdatedAt = &now
	issue, err = r.persistIssueExecPlan(ctx, issue, plan)
	if err != nil {
		return issue, progress, err
	}
	return issue, progress, nil
}

func (r *Runner) ensureExecPlanDecision(ctx context.Context, client *codex.Client, workspacePath string, issue domain.Issue) (domain.Issue, error) {
	if hasDuplicateExecPlans(issue) || !r.cfg.Agent.CreateExecPlan {
		return issue, nil
	}
	if execPlanDecision(issue) != "" {
		return issue, nil
	}
	if hasCanonicalExecPlan(issue) {
		return r.persistExecPlanDecision(ctx, issue, domain.ExecPlanDecisionExecPlan)
	}

	prompt := buildExecPlanDecisionPrompt(issue)
	r.logger.Info(
		"deciding issue exec plan strategy",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", workspacePath,
	)
	if err := client.RunTurn(ctx, workspacePath, issue, prompt); err != nil {
		return issue, fmt.Errorf("decide exec plan strategy: %w", err)
	}

	output := client.LastOutput()
	decision, err := parseExecPlanDecision(output)
	if err == nil {
		return r.persistExecPlanDecision(ctx, issue, decision)
	}

	r.logger.Warn(
		"exec plan decision output was malformed; retrying once",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", workspacePath,
		"invalid_first_line", firstLine(output),
		"captured_from_completed_item", client.LastOutputCapturedFromCompletedItem(),
	)
	if err := client.RunTurn(ctx, workspacePath, issue, buildExecPlanDecisionRetryPrompt(output)); err != nil {
		return issue, fmt.Errorf("decide exec plan strategy: %w", err)
	}

	output = client.LastOutput()
	decision, err = parseExecPlanDecision(output)
	if err != nil {
		r.logger.Warn(
			"exec plan decision output remained malformed after retry",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"workspace_path", workspacePath,
			"invalid_first_line", firstLine(output),
			"captured_from_completed_item", client.LastOutputCapturedFromCompletedItem(),
		)
		return issue, fmt.Errorf("decide exec plan strategy: %w", err)
	}
	return r.persistExecPlanDecision(ctx, issue, decision)
}

func (r *Runner) ensureExecPlan(ctx context.Context, client *codex.Client, workspacePath string, issue domain.Issue) (domain.Issue, error) {
	if hasDuplicateExecPlans(issue) {
		return issue, tracker.ErrDuplicateExecPlans
	}
	decision := execPlanDecision(issue)
	if !r.cfg.Agent.CreateExecPlan || decision == domain.ExecPlanDecisionOneShot || hasCanonicalExecPlan(issue) {
		return issue, nil
	}
	if decision != domain.ExecPlanDecisionExecPlan {
		return issue, nil
	}

	prompt := buildExecPlanPrompt(issue)
	r.logger.Info(
		"generating issue exec plan",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", workspacePath,
	)
	if err := client.RunTurn(ctx, workspacePath, issue, prompt); err != nil {
		return issue, fmt.Errorf("generate exec plan: %w", err)
	}

	body := normalizeExecPlanBody(client.LastOutput())
	if body == "" {
		return issue, errors.New("generate exec plan: empty response")
	}

	now := time.Now().UTC()
	return r.persistIssueExecPlan(ctx, issue, domain.ExecPlan{
		Body:      body,
		UpdatedAt: &now,
	})
}

func buildExecPlanDecisionPrompt(issue domain.Issue) string {
	var b strings.Builder
	b.WriteString("Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.\n\n")
	b.WriteString("Return a short answer.\n")
	b.WriteString("The first line must be exactly one of:\n")
	b.WriteString(execPlanDecisionOneShotLine + "\n")
	b.WriteString(execPlanDecisionExecPlanLine + "\n\n")
	b.WriteString("After the first line, include a brief rationale in 1-3 sentences.\n")
	b.WriteString("Choose `ONE_SHOT` only when the change is small and safe enough to implement directly without a stored plan.\n")
	b.WriteString("Choose `EXEC_PLAN` when the issue is large, risky, multi-step, or would benefit from a persistent implementation plan.\n\n")
	b.WriteString("Issue context:\n")
	b.WriteString(fmt.Sprintf("- Identifier: %s\n", issue.Identifier))
	b.WriteString(fmt.Sprintf("- Title: %s\n", issue.Title))
	b.WriteString(fmt.Sprintf("- State: %s\n", issue.State))
	if issue.URL != nil && strings.TrimSpace(*issue.URL) != "" {
		b.WriteString(fmt.Sprintf("- URL: %s\n", strings.TrimSpace(*issue.URL)))
	}
	if len(issue.Labels) > 0 {
		b.WriteString("- Labels:\n")
		for _, label := range issue.Labels {
			b.WriteString(fmt.Sprintf("  - %s\n", label))
		}
	}
	if issue.Description != nil && strings.TrimSpace(*issue.Description) != "" {
		b.WriteString("\nIssue description:\n\n")
		b.WriteString(strings.TrimSpace(*issue.Description))
		b.WriteString("\n")
	}
	if len(issue.ReviewFeedback) > 0 {
		b.WriteString("\nReview feedback:\n")
		for _, item := range issue.ReviewFeedback {
			b.WriteString(fmt.Sprintf("- %s\n", strings.TrimSpace(item.Body)))
		}
	}
	if len(issue.ReviewThreads) > 0 {
		b.WriteString("\nGitHub review threads:\n")
		for _, thread := range issue.ReviewThreads {
			lineText := ""
			if thread.Line != nil {
				lineText = fmt.Sprintf(":%d", *thread.Line)
			}
			b.WriteString(fmt.Sprintf("- %s%s by %s: %s\n", thread.Path, lineText, thread.Author, strings.TrimSpace(thread.Body)))
		}
	}
	return strings.TrimSpace(b.String())
}

func buildExecPlanDecisionRetryPrompt(previousOutput string) string {
	var b strings.Builder
	b.WriteString("Your previous ExecPlan strategy response could not be parsed.\n\n")
	b.WriteString("Return a short answer.\n")
	b.WriteString("The first line must be exactly one of:\n")
	b.WriteString(execPlanDecisionOneShotLine + "\n")
	b.WriteString(execPlanDecisionExecPlanLine + "\n\n")
	b.WriteString("After the first line, include a brief rationale in 1-3 sentences.\n")
	b.WriteString("Do not repeat the original question or issue description.\n")
	if invalid := firstLine(previousOutput); invalid != "" {
		b.WriteString(fmt.Sprintf("Your previous first line was: %q\n", invalid))
	}
	return strings.TrimSpace(b.String())
}

func buildExecPlanPrompt(issue domain.Issue) string {
	var b strings.Builder
	b.WriteString("Create an ExecPlan for the Linear issue below.\n\n")
	b.WriteString("Do not modify repository files or implement the change yet.\n")
	b.WriteString("Return only the final ExecPlan markdown document as file contents, without surrounding commentary and without wrapping it in an outer triple-backtick fence.\n\n")
	b.WriteString("Issue context:\n")
	b.WriteString(fmt.Sprintf("- Identifier: %s\n", issue.Identifier))
	b.WriteString(fmt.Sprintf("- Title: %s\n", issue.Title))
	b.WriteString(fmt.Sprintf("- State: %s\n", issue.State))
	if issue.URL != nil && strings.TrimSpace(*issue.URL) != "" {
		b.WriteString(fmt.Sprintf("- URL: %s\n", strings.TrimSpace(*issue.URL)))
	}
	if len(issue.Labels) > 0 {
		b.WriteString("- Labels:\n")
		for _, label := range issue.Labels {
			b.WriteString(fmt.Sprintf("  - %s\n", label))
		}
	}
	if issue.Description != nil && strings.TrimSpace(*issue.Description) != "" {
		b.WriteString("\nIssue description:\n\n")
		b.WriteString(strings.TrimSpace(*issue.Description))
		b.WriteString("\n")
	}
	if len(issue.ReviewFeedback) > 0 {
		b.WriteString("\nReview feedback:\n")
		for _, item := range issue.ReviewFeedback {
			b.WriteString(fmt.Sprintf("- %s\n", strings.TrimSpace(item.Body)))
		}
	}
	if len(issue.ReviewThreads) > 0 {
		b.WriteString("\nGitHub review threads:\n")
		for _, thread := range issue.ReviewThreads {
			lineText := ""
			if thread.Line != nil {
				lineText = fmt.Sprintf(":%d", *thread.Line)
			}
			b.WriteString(fmt.Sprintf("- %s%s by %s: %s\n", thread.Path, lineText, thread.Author, strings.TrimSpace(thread.Body)))
		}
	}
	b.WriteString("\nExecPlan authoring guide:\n\n")
	b.WriteString(execplan.Template())
	return strings.TrimSpace(b.String())
}

func normalizeExecPlanBody(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	lines := strings.Split(value, "\n")
	if len(lines) >= 2 {
		first := strings.TrimSpace(lines[0])
		last := strings.TrimSpace(lines[len(lines)-1])
		if strings.HasPrefix(first, "```") && last == "```" {
			value = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	return strings.TrimSpace(value)
}

func parseExecPlanDecision(value string) (domain.ExecPlanDecision, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty response")
	}

	firstLine := strings.TrimSpace(strings.Split(value, "\n")[0])
	switch strings.ToUpper(firstLine) {
	case execPlanDecisionOneShotLine:
		return domain.ExecPlanDecisionOneShot, nil
	case execPlanDecisionExecPlanLine:
		return domain.ExecPlanDecisionExecPlan, nil
	default:
		return "", fmt.Errorf("unexpected decision %q", firstLine)
	}
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(value, "\n")[0])
}

func execPlanDecision(issue domain.Issue) domain.ExecPlanDecision {
	if issue.ColinMetadata == nil {
		return ""
	}
	return normalizeExecPlanDecision(issue.ColinMetadata.ExecPlanDecision)
}

func normalizeExecPlanDecision(value domain.ExecPlanDecision) domain.ExecPlanDecision {
	switch strings.ToLower(strings.TrimSpace(string(value))) {
	case string(domain.ExecPlanDecisionOneShot):
		return domain.ExecPlanDecisionOneShot
	case string(domain.ExecPlanDecisionExecPlan):
		return domain.ExecPlanDecisionExecPlan
	default:
		return ""
	}
}

func execPlanBody(plan *domain.ExecPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.Body)
}

func hasCanonicalExecPlan(issue domain.Issue) bool {
	if issue.ExecPlanCount == 1 {
		return true
	}
	return strings.TrimSpace(execPlanBody(issue.ExecPlan)) != ""
}

func hasDuplicateExecPlans(issue domain.Issue) bool {
	return issue.ExecPlanCount > 1
}

func injectExecPlanPrompt(prompt string, plan *domain.ExecPlan, workingCopy *execplan.WorkingCopy, progress execplan.Progress) string {
	body := execPlanBody(plan)
	parts := make([]string, 0, 3)
	if trimmed := strings.TrimSpace(prompt); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if body != "" {
		parts = append(parts, "ExecPlan:\n\n"+body)
	}
	if workingCopy != nil {
		parts = append(parts, buildExecPlanTrackingInstructions(workingCopy.Path(), progress.Remaining()))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func buildCodingContinuationPrompt(identifier string, workingCopy *execplan.WorkingCopy, progress execplan.Progress) string {
	prompt := fmt.Sprintf(
		"Continue working on %s without restating the original plan. Pick up from the current thread history and leave the issue in a handoff-ready state if appropriate.",
		identifier,
	)
	if workingCopy == nil {
		return prompt
	}
	return prompt + "\n\n" + buildExecPlanTrackingInstructions(workingCopy.Path(), progress.Remaining())
}

func buildExecPlanTrackingInstructions(path string, remaining []string) string {
	lines := []string{
		fmt.Sprintf("ExecPlan working copy: %s", path),
		"Keep that file updated as you work. It is the live copy Colin will sync back to the Linear issue after each turn.",
		"For ExecPlan-backed issues, do not return `COLIN_OUTCOME: READY_FOR_REVIEW` until every checkbox under `## Progress` is complete.",
		"If you cannot safely complete the remaining `## Progress` tasks, return `COLIN_OUTCOME: NEEDS_SPEC` and explain the blocker.",
	}
	if len(remaining) > 0 {
		lines = append(lines, "Remaining `## Progress` tasks:")
		for _, item := range remaining {
			lines = append(lines, "- "+strings.TrimSpace(item))
		}
	}
	return strings.Join(lines, "\n")
}

func (r *Runner) persistIssueMetadata(ctx context.Context, issue domain.Issue, metadata domain.ColinMetadata) (domain.Issue, error) {
	if r.tracker == nil {
		issue.ColinMetadata = &metadata
		return issue, nil
	}
	persisted, err := r.tracker.UpsertIssueMetadata(ctx, issue.ID, metadata)
	if err != nil {
		return issue, fmt.Errorf("upsert issue metadata: %w", err)
	}
	issue.ColinMetadata = &persisted
	return issue, nil
}

func (r *Runner) persistExecPlanDecision(ctx context.Context, issue domain.Issue, decision domain.ExecPlanDecision) (domain.Issue, error) {
	decision = normalizeExecPlanDecision(decision)
	if decision == "" {
		return issue, errors.New("missing exec plan decision")
	}

	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	if normalizeExecPlanDecision(metadata.ExecPlanDecision) == decision {
		return issue, nil
	}
	metadata.ExecPlanDecision = decision
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	return r.persistIssueMetadata(ctx, issue, metadata)
}

func (r *Runner) persistIssueExecPlan(ctx context.Context, issue domain.Issue, plan domain.ExecPlan) (domain.Issue, error) {
	if r.tracker == nil {
		issue.ExecPlan = &plan
		issue.ExecPlanCount = 1
		return issue, nil
	}
	persisted, err := r.tracker.UpsertIssueExecPlan(ctx, issue.ID, plan)
	if err != nil {
		return issue, fmt.Errorf("upsert issue exec plan: %w", err)
	}
	issue.ExecPlan = &persisted
	issue.ExecPlanCount = 1
	return issue, nil
}

func (r *Runner) handleInvalidExecPlan(ctx context.Context, issue domain.Issue, workspacePath string, reason string, remaining []string) Result {
	updated, updateErr := r.moveIssueToState(ctx, issue, refineStateName)
	if updateErr != nil {
		return Result{Issue: issue, RunType: RunTypeCoding, WorkspacePath: workspacePath, Status: "failed", Err: updateErr}
	}
	updated = r.clearPersistentThreadMetadataBestEffort(ctx, updated)
	updated = r.persistIssueMetadataBestEffort(ctx, updated, codexMetadata(updated, RunTypeCoding, metadataOutcomePlanInvalid, ""))
	return Result{
		Issue:         updated,
		RunType:       RunTypeCoding,
		WorkspacePath: workspacePath,
		Status:        "succeeded",
		Summary:       buildInvalidExecPlanSummary(reason, remaining),
	}
}

func (r *Runner) persistIssueMetadataBestEffort(ctx context.Context, issue domain.Issue, metadata domain.ColinMetadata) domain.Issue {
	updated, err := r.persistIssueMetadata(ctx, issue, metadata)
	if err != nil {
		r.logger.Warn(
			"failed to persist issue metadata",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"run_type", metadata.LastRunType,
			"outcome", metadata.LastOutcome,
			"error", err,
		)
		issue.ColinMetadata = &metadata
		return issue
	}
	return updated
}

func (r *Runner) persistActualBranchNameBestEffort(ctx context.Context, issue domain.Issue, workspacePath string) domain.Issue {
	if r.repo == nil {
		return issue
	}
	branch, err := r.repo.CurrentBranch(ctx, workspacePath)
	if err != nil {
		return issue
	}
	return r.persistActualBranchNameValueBestEffort(ctx, issue, branch)
}

func (r *Runner) persistActualBranchNameValueBestEffort(ctx context.Context, issue domain.Issue, branch string) domain.Issue {
	metadata, changed := actualBranchMetadata(issue, branch)
	if !changed {
		return issue
	}
	return r.persistIssueMetadataBestEffort(ctx, issue, metadata)
}

func (r *Runner) shouldResetPersistentThreads(state string) bool {
	if strings.EqualFold(strings.TrimSpace(state), refineStateName) || config.ContainsState(r.cfg.Tracker.TerminalStates, state) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "done", "merged", "closed", "cancelled", "canceled", "duplicate":
		return true
	default:
		return false
	}
}

func (r *Runner) persistCodexThreadIDBestEffort(ctx context.Context, issue domain.Issue, threadID string) domain.Issue {
	metadata, changed := codexThreadMetadata(issue, threadID)
	if !changed {
		return issue
	}
	return r.persistIssueMetadataBestEffort(ctx, issue, metadata)
}

func (r *Runner) clearPersistentThreadMetadataBestEffort(ctx context.Context, issue domain.Issue) domain.Issue {
	metadata, changed := clearedPersistentThreadMetadata(issue)
	if !changed {
		return issue
	}
	return r.persistIssueMetadataBestEffort(ctx, issue, metadata)
}

func (r *Runner) clearPendingReviewFollowUpBestEffort(ctx context.Context, issue domain.Issue) domain.Issue {
	metadata, changed := clearedPendingReviewFollowUpMetadata(issue)
	if !changed {
		return issue
	}
	return r.persistIssueMetadataBestEffort(ctx, issue, metadata)
}

func (r *Runner) moveIssueToState(ctx context.Context, issue domain.Issue, targetState string) (domain.Issue, error) {
	targetState = strings.TrimSpace(targetState)
	if targetState == "" || config.ContainsState([]string{issue.State}, targetState) {
		return issue, nil
	}
	if r.tracker != nil {
		if err := r.tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
			return issue, fmt.Errorf("update issue state to %s: %w", targetState, err)
		}
	}
	issue.State = targetState
	now := time.Now().UTC()
	issue.UpdatedAt = &now
	return issue, nil
}
