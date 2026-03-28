package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/tracker"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
)

// Runner executes one issue attempt inside a workspace using the Codex app-server protocol.
type Runner struct {
	cfg        domain.ServiceConfig
	workflow   domain.WorkflowDefinition
	tracker    tracker.Client
	workspaces *workspace.Manager
	logger     *slog.Logger
}

// NewRunner constructs a Runner bound to the current workflow, tracker, and workspace manager.
func NewRunner(cfg domain.ServiceConfig, def domain.WorkflowDefinition, trackerClient tracker.Client, manager *workspace.Manager, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:        cfg,
		workflow:   def,
		tracker:    trackerClient,
		workspaces: manager,
		logger:     logger,
	}
}

// Run executes one worker lifecycle for an issue, including workspace hooks and continuation turns.
func (r *Runner) Run(ctx context.Context, issue domain.Issue, attempt *int, onEvent func(Event)) Result {
	ws, err := r.workspaces.Ensure(ctx, issue)
	if err != nil {
		return Result{Issue: issue, Status: "failed", Err: err}
	}
	if err := r.workspaces.RunBeforeRun(ctx, ws.Path); err != nil {
		return Result{Issue: issue, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	defer func() {
		if err := r.workspaces.RunAfterRun(context.Background(), ws.Path); err != nil {
			r.logger.Warn("after_run hook failed", "workspace_path", ws.Path, "error", err)
		}
	}()

	client := &appServerClient{
		cfg:      r.cfg,
		logger:   r.logger,
		onEvent:  onEvent,
		issue:    issue,
		workflow: r.workflow,
	}
	if err := client.start(ctx, ws.Path); err != nil {
		return Result{Issue: issue, WorkspacePath: ws.Path, Status: "failed", Err: err}
	}
	defer client.stop()

	current := issue
	for turn := 1; turn <= r.cfg.Agent.MaxTurns; turn++ {
		prompt, err := workflow.RenderPrompt(r.workflow, current, attempt)
		if err != nil {
			return Result{Issue: current, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		if turn > 1 {
			prompt = fmt.Sprintf(
				"Continue working on %s without restating the original plan. Pick up from the current thread history and leave the issue in a handoff-ready state if appropriate.",
				current.Identifier,
			)
		}
		if err := client.runTurn(ctx, ws.Path, current, prompt); err != nil {
			status := "failed"
			switch {
			case errors.Is(err, ErrTurnTimeout):
				status = "timed_out"
			case errors.Is(err, ErrTurnCancelled):
				status = "failed"
			case errors.Is(err, ErrTurnInputNeeded):
				status = "failed"
			}
			return Result{Issue: current, WorkspacePath: ws.Path, Status: status, Err: err}
		}

		issues, err := r.tracker.FetchIssueStatesByIDs(ctx, []string{current.ID})
		if err != nil {
			return Result{Issue: current, WorkspacePath: ws.Path, Status: "failed", Err: err}
		}
		if len(issues) > 0 {
			current = issues[0]
		}
		if !isActive(r.cfg, current.State) {
			break
		}
	}

	return Result{Issue: current, WorkspacePath: ws.Path, Status: "succeeded"}
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
