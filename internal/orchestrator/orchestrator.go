package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/config"
)

// New constructs an Orchestrator for the supplied runtime dependencies.
func New(runtime Runtime, logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		logger:    logger,
		eventCh:   make(chan any, 256),
		runtime:   runtime,
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}
}

// UpdateRuntime swaps in a reloaded runtime configuration for future scheduling decisions.
func (o *Orchestrator) UpdateRuntime(runtime Runtime) {
	o.eventCh <- configUpdatedEvent{runtime: runtime}
}

// Run starts the main event loop and exits when the provided context is canceled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.logger.Info(
		"orchestrator started",
		"poll_interval", o.runtime.Config.Polling.Interval.String(),
		"max_concurrent_agents", o.runtime.Config.Agent.MaxConcurrentAgents,
	)
	tick := time.NewTimer(0)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			o.logger.Info("orchestrator stopping")
			o.stopAll("shutdown")
			return nil
		case <-tick.C:
			o.handleTick(ctx)
			tick.Reset(o.runtime.Config.Polling.Interval)
		case raw := <-o.eventCh:
			switch event := raw.(type) {
			case configUpdatedEvent:
				o.runtime = event.runtime
				o.logger.Info(
					"runtime updated",
					"poll_interval", o.runtime.Config.Polling.Interval.String(),
					"max_concurrent_agents", o.runtime.Config.Agent.MaxConcurrentAgents,
				)
				if !tick.Stop() {
					select {
					case <-tick.C:
					default:
					}
				}
				tick.Reset(o.runtime.Config.Polling.Interval)
			case codexEvent:
				o.handleCodexEvent(ctx, event.event)
			case workerExitedEvent:
				o.handleWorkerExit(ctx, event)
			case retryFiredEvent:
				o.handleRetry(ctx, event.issueID)
			}
		}
	}
}

func (o *Orchestrator) handleTick(ctx context.Context) {
	args := []any{
		"running", len(o.running),
		"retrying", len(o.retrying),
		"claimed", len(o.claimed),
	}
	if summaries := o.runningIssueSummaries(time.Now().UTC()); len(summaries) > 0 {
		args = append(args, "running_issues", summaries)
	}
	if summaries := o.retrySummaries(); len(summaries) > 0 {
		args = append(args, "retry_issues", summaries)
	}
	o.logger.Info("poll tick started", args...)
	o.reconcileRunning(ctx)
	if err := config.ValidateDispatch(o.runtime.Config); err != nil {
		o.logger.Error("dispatch validation failed", "error", err)
		return
	}
	issues, err := o.runtime.Tracker.FetchCandidateIssues(ctx)
	if err != nil {
		o.logger.Error("candidate fetch failed", "error", err)
		return
	}
	sortIssues(issues)
	dispatched := 0
	eligible := 0
	for _, issue := range issues {
		if !o.shouldDispatch(issue) {
			continue
		}
		eligible++
		if !o.hasGlobalSlots() {
			break
		}
		o.dispatch(ctx, issue, nil, nil)
		dispatched++
	}
	args = []any{
		"candidate_count", len(issues),
		"eligible_count", eligible,
		"dispatched_count", dispatched,
		"running", len(o.running),
		"retrying", len(o.retrying),
	}
	if summaries := o.runningIssueSummaries(time.Now().UTC()); len(summaries) > 0 {
		args = append(args, "running_issues", summaries)
	}
	if summaries := o.retrySummaries(); len(summaries) > 0 {
		args = append(args, "retry_issues", summaries)
	}
	o.logger.Info("poll tick completed", args...)
}

func (o *Orchestrator) handleCodexEvent(ctx context.Context, event codex.Event) {
	entry, ok := o.running[event.IssueID]
	if !ok {
		return
	}
	o.handleCommentEvent(ctx, entry, event)
	entry.session.SessionID = event.SessionID
	entry.session.ThreadID = event.ThreadID
	entry.session.TurnID = event.TurnID
	entry.session.CodexAppServerPID = event.PID
	entry.session.LastCodexEvent = event.Event
	entry.session.LastCodexMessage = event.Message
	entry.session.LastCodexTimestamp = &event.Timestamp
	if event.State != "" {
		entry.issue.State = event.State
	}
	if event.Event == codex.EventSessionStarted {
		entry.session.TurnCount++
	}
	o.applyUsage(entry, event.Usage)
	if event.RateLimits != nil {
		o.rateLimits = event.RateLimits
	}
	switch event.Event {
	case codex.EventSessionStarted:
		o.logger.Info(
			"codex session started",
			"issue_id", event.IssueID,
			"issue_identifier", event.Identifier,
			"session_id", event.SessionID,
			"thread_id", event.ThreadID,
			"turn_id", event.TurnID,
			"turn_count", entry.session.TurnCount,
			"state", entry.issue.State,
		)
	case codex.EventApprovalAutoApproved:
		o.logger.Info(
			"codex approval auto-approved",
			"issue_id", event.IssueID,
			"issue_identifier", event.Identifier,
			"session_id", event.SessionID,
			"turn_id", event.TurnID,
		)
	case codex.EventTurnCompleted, codex.EventTurnFailed, codex.EventTurnCancelled, codex.EventTurnInputRequired:
		o.logger.Info(
			"codex turn event",
			"issue_id", event.IssueID,
			"issue_identifier", event.Identifier,
			"session_id", event.SessionID,
			"turn_id", event.TurnID,
			"turn_count", entry.session.TurnCount,
			"state", entry.issue.State,
			"event", event.Event,
			"message", event.Message,
			"input_tokens", entry.session.CodexInputTokens,
			"output_tokens", entry.session.CodexOutputTokens,
			"total_tokens", entry.session.CodexTotalTokens,
		)
	}
}

func (o *Orchestrator) applyUsage(entry *runningEntry, usage map[string]int64) {
	if len(usage) == 0 {
		return
	}
	if total, ok := usage["input_tokens"]; ok {
		delta := total - entry.session.LastReportedInputTokens
		if delta > 0 {
			o.totalTokens.InputTokens += delta
		}
		entry.session.CodexInputTokens = total
		entry.session.LastReportedInputTokens = total
	}
	if total, ok := usage["output_tokens"]; ok {
		delta := total - entry.session.LastReportedOutputTokens
		if delta > 0 {
			o.totalTokens.OutputTokens += delta
		}
		entry.session.CodexOutputTokens = total
		entry.session.LastReportedOutputTokens = total
	}
	if total, ok := usage["total_tokens"]; ok {
		delta := total - entry.session.LastReportedTotalTokens
		if delta > 0 {
			o.totalTokens.TotalTokens += delta
		}
		entry.session.CodexTotalTokens = total
		entry.session.LastReportedTotalTokens = total
	}
}
