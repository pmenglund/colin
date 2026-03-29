package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/tracker"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
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

// NewRunner constructs a Runner bound to the current workflow, tracker, and workspace manager.
func NewRunner(cfg domain.ServiceConfig, def domain.WorkflowDefinition, trackerClient tracker.Client, manager *workspace.Manager, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:        cfg,
		workflow:   def,
		repo:       repoops.NewManager(cfg, logger),
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
	if err := r.workspaces.RunBeforeRun(ctx, ws.Path); err != nil {
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	defer func() {
		if err := r.workspaces.RunAfterRun(context.Background(), ws.Path); err != nil {
			r.logger.Warn("after_run hook failed", "workspace_path", ws.Path, "error", err)
		}
	}()

	client := &appServerClient{
		cfg:       r.cfg,
		logger:    r.logger,
		onEvent:   emit,
		issue:     issue,
		workspace: ws.Path,
		runType:   runType,
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
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "succeeded"}
	}
	if isMergeState(r.cfg, issue.State) {
		emit(Event{
			Event:     EventMergeStarted,
			Timestamp: time.Now().UTC(),
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Starting merge automation",
		})
		result, err := r.repo.Merge(ctx, issue, ws.Path)
		if err != nil {
			return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		r.logger.Info(
			"merge automation completed",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
			"workspace_path", ws.Path,
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
			Workspace: ws.Path,
			State:     issue.State,
			Message:   "Merge automation completed",
			Branch:    result.Branch,
			BaseRef:   result.BaseRef,
			PRNumber:  result.PRNumber,
			PRURL:     result.PRURL,
			PRState:   result.PRState,
			Action:    result.Action,
		})
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "succeeded"}
	}

	if err := client.start(ctx, ws.Path); err != nil {
		return Result{Issue: issue, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	r.logger.Info(
		"codex session ready",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"workspace_path", ws.Path,
		"thread_id", client.threadID,
	)
	defer client.stop()

	current := issue
	for turn := 1; turn <= r.cfg.Agent.MaxTurns; turn++ {
		prompt, err := workflow.RenderPrompt(r.workflow, current, attempt)
		if err != nil {
			return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		if turn > 1 {
			prompt = fmt.Sprintf(
				"Continue working on %s without restating the original plan. Pick up from the current thread history and leave the issue in a handoff-ready state if appropriate.",
				current.Identifier,
			)
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
		if err := client.runTurn(ctx, ws.Path, current, prompt); err != nil {
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

		duration := time.Since(startedAt).Round(time.Millisecond)
		previousState := current.State
		issues, err := r.tracker.FetchIssueStatesByIDs(ctx, []string{current.ID})
		if err != nil {
			return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		refreshed := false
		if len(issues) > 0 {
			current = issues[0]
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
		if turn < r.cfg.Agent.MaxTurns {
			r.logger.Info(
				"issue still active after turn; continuing",
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
	}

	current, err = r.moveSuccessfulCodingRunToPublishState(ctx, current)
	if err != nil {
		return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}

	emit(Event{
		Event:     EventRunSucceeded,
		Timestamp: time.Now().UTC(),
		Workspace: ws.Path,
		State:     current.State,
		Message:   "Run completed successfully",
	})
	return Result{Issue: current, RunType: runType, WorkspacePath: ws.Path, Status: "succeeded"}
}

func (r *Runner) moveSuccessfulCodingRunToPublishState(ctx context.Context, issue domain.Issue) (domain.Issue, error) {
	if !isActive(r.cfg, issue.State) {
		return issue, nil
	}

	targetState, ok := firstConfiguredState(r.cfg.Repo.PublishStates)
	if !ok || config.ContainsState([]string{issue.State}, targetState) {
		return issue, nil
	}

	if err := r.tracker.UpdateIssueState(ctx, issue.ID, targetState); err != nil {
		return issue, fmt.Errorf("update issue state to %s: %w", targetState, err)
	}
	r.logger.Info(
		"issue moved to publish state after successful coding run",
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
