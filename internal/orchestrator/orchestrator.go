package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
)

// New constructs an Orchestrator for the supplied runtime dependencies.
func New(runtime Runtime, logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		logger:      logger,
		eventCh:     make(chan any, 256),
		runtime:     runtime,
		running:     map[string]*runningEntry{},
		claimed:     map[string]struct{}{},
		retrying:    map[string]*retryState{},
		reviewSync:  map[string]*reviewSyncState{},
		completed:   map[string]string{},
		issueStates: map[string]int{},
	}
}

// UpdateRuntime swaps in a reloaded runtime configuration for future scheduling decisions.
func (o *Orchestrator) UpdateRuntime(runtime Runtime) {
	o.eventCh <- configUpdatedEvent{runtime: runtime}
}

// Run starts the main event loop and exits when the provided context is canceled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.loopStarted.Store(true)
	defer o.loopStarted.Store(false)

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
			case snapshotRequestEvent:
				event.response <- o.snapshotAt(time.Now().UTC())
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
	o.refreshIssueStateCounts(ctx)
	if err := config.ValidateDispatch(o.runtime.Config); err != nil {
		o.logger.Error("dispatch validation failed", "error", err)
		return
	}
	if delay := o.trackerThrottleDelay(time.Now().UTC()); delay > 0 {
		o.logger.Info("candidate fetch deferred by Linear request budget", append([]any{"delay", delay.String()}, o.linearRateLimitLogArgs()...)...)
		return
	}
	issues, err := o.runtime.Tracker.FetchCandidateIssues(ctx)
	if err != nil {
		o.logger.Error("candidate fetch failed", "error", err)
		return
	}
	o.cleanupReviewSync(issues)
	sortIssues(issues)
	dispatched := 0
	eligible := 0
	now := time.Now().UTC()
	for _, issue := range issues {
		issue, ready := o.prepareReviewIssue(ctx, issue, now)
		if !ready {
			continue
		}
		issue = o.clearPausedLoopMetadataIfUnpaused(ctx, issue)
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

func (o *Orchestrator) refreshIssueStateCounts(ctx context.Context) {
	stateNames := trackedStateNames(o.runtime.Config)
	if len(stateNames) == 0 {
		o.issueStates = map[string]int{}
		return
	}
	if delay := o.trackerThrottleDelay(time.Now().UTC()); delay > 0 {
		o.logger.Info("issue state count refresh deferred by Linear request budget", append([]any{"delay", delay.String()}, o.linearRateLimitLogArgs()...)...)
		return
	}

	issues, err := o.runtime.Tracker.FetchIssuesByStates(ctx, stateNames)
	if err != nil {
		o.logger.Warn("issue state count refresh failed", "error", err)
		return
	}

	counts := make(map[string]int, len(stateNames))
	for _, issue := range issues {
		counts[issue.State]++
	}
	o.issueStates = counts
}

func (o *Orchestrator) trackerThrottleDelay(now time.Time) time.Duration {
	linearRequests, ok := o.currentLinearRequests()
	if !ok {
		return 0
	}
	nextAllowedAt, ok := unixTimeValue(linearRequests["nextAllowedAt"])
	if !ok || !nextAllowedAt.After(now) {
		return 0
	}
	return nextAllowedAt.Sub(now)
}

func (o *Orchestrator) linearRateLimitLogArgs() []any {
	linearRequests, ok := o.currentLinearRequests()
	if !ok {
		return nil
	}
	args := make([]any, 0, 8)
	if remaining, ok := int64Value(linearRequests["remaining"]); ok {
		args = append(args, "linear_requests_remaining", remaining)
	}
	if limit, ok := int64Value(linearRequests["limit"]); ok {
		args = append(args, "linear_requests_limit", limit)
	}
	if resetsAt, ok := unixTimeValue(linearRequests["resetsAt"]); ok {
		args = append(args, "linear_requests_reset_at", resetsAt.Format(time.RFC3339))
	}
	if nextAllowedAt, ok := unixTimeValue(linearRequests["nextAllowedAt"]); ok {
		args = append(args, "linear_requests_next_allowed_at", nextAllowedAt.Format(time.RFC3339))
	}
	return args
}

func (o *Orchestrator) currentLinearRequests() (map[string]any, bool) {
	limits := o.runtime.Tracker.CurrentRateLimits()
	if len(limits) == 0 {
		return nil, false
	}
	linearRequests, ok := limits["linear_requests"].(map[string]any)
	if !ok {
		return nil, false
	}
	return linearRequests, true
}

func unixTimeValue(value any) (time.Time, bool) {
	switch v := value.(type) {
	case int64:
		return time.Unix(v, 0).UTC(), true
	case int:
		return time.Unix(int64(v), 0).UTC(), true
	case float64:
		return time.Unix(int64(v), 0).UTC(), true
	default:
		return time.Time{}, false
	}
}

func int64Value(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

func trackedStateNames(cfg domain.ServiceConfig) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, state := range append(config.CandidateStates(cfg), cfg.Tracker.TerminalStates...) {
		key := config.StateKey(state)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, state)
	}
	return out
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
	o.appendOutput(entry, event)
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

func (o *Orchestrator) appendOutput(entry *runningEntry, event codex.Event) {
	if entry == nil {
		return
	}

	message := strings.TrimSpace(event.Message)
	if skipOutputEvent(event.Event, message) {
		return
	}
	if message == "" {
		switch event.Event {
		case codex.EventSessionStarted:
			message = "session started"
		case codex.EventApprovalAutoApproved:
			message = "approval auto-approved"
		default:
			message = event.Event
		}
	}
	if isDuplicateOutput(entry.outputLog, event.Event, message) {
		return
	}

	entry.outputLog = append(entry.outputLog, domain.OutputLog{
		Timestamp: event.Timestamp,
		Event:     event.Event,
		Message:   message,
	})
	if len(entry.outputLog) > 200 {
		entry.outputLog = append([]domain.OutputLog(nil), entry.outputLog[len(entry.outputLog)-200:]...)
	}
}

func skipOutputEvent(eventName, message string) bool {
	switch eventName {
	case codex.EventOtherMessage, codex.EventNotification:
		if message == "" || message == eventName {
			return true
		}
	}
	return false
}

func isDuplicateOutput(log []domain.OutputLog, eventName, message string) bool {
	if len(log) == 0 {
		return false
	}
	last := log[len(log)-1]
	lastMessage := strings.TrimSpace(last.Message)
	currentMessage := strings.TrimSpace(message)
	if lastMessage == "" || currentMessage == "" || lastMessage != currentMessage {
		return false
	}
	if last.Event == eventName {
		return true
	}
	if isTurnTerminalEvent(eventName) && isContentEvent(last.Event) {
		return true
	}
	return false
}

func isTurnTerminalEvent(eventName string) bool {
	switch eventName {
	case codex.EventTurnCompleted, codex.EventTurnFailed, codex.EventTurnCancelled, codex.EventTurnInputRequired:
		return true
	default:
		return false
	}
}

func isContentEvent(eventName string) bool {
	switch eventName {
	case codex.EventOtherMessage, codex.EventNotification:
		return true
	default:
		return false
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
