package orchestrator

import (
	"context"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
)

func (o *Orchestrator) reconcileRunning(ctx context.Context) {
	if len(o.running) == 0 {
		return
	}
	o.reconcileStalled()
	if delay := o.trackerThrottleDelay(time.Now().UTC()); delay > 0 {
		o.logger.Info("running-state refresh deferred by Linear request budget", append([]any{"delay", delay.String()}, o.linearRateLimitLogArgs()...)...)
		return
	}

	ids := make([]string, 0, len(o.running))
	for issueID := range o.running {
		ids = append(ids, issueID)
	}
	issues, err := o.runtime.Tracker.FetchIssueStatesByIDs(ctx, ids)
	if err != nil {
		o.logger.Warn("running-state refresh failed", "error", err)
		return
	}
	byID := make(map[string]domain.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}
	for issueID, entry := range o.running {
		issue, ok := byID[issueID]
		if !ok {
			continue
		}
		entry.issue = issue
		if o.isTerminal(issue.State) {
			o.logger.Info("stopping terminal issue", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", issue.State)
			o.requestStop(issueID, "terminal", true)
			continue
		}
		if !o.isActive(issue.State) {
			o.logger.Info("stopping non-active issue", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", issue.State)
			o.requestStop(issueID, "non_active", false)
		}
	}
}

func (o *Orchestrator) reconcileStalled() {
	stallTimeout := o.runtime.Config.Codex.StallTimeout
	if stallTimeout <= 0 {
		return
	}
	now := time.Now().UTC()
	for issueID, entry := range o.running {
		if entry.stopReason != "" {
			continue
		}
		last := entry.startedAt
		if entry.session.LastCodexTimestamp != nil {
			last = *entry.session.LastCodexTimestamp
		}
		if now.Sub(last) > stallTimeout {
			o.logger.Warn("stalled issue detected", "issue_id", issueID, "issue_identifier", entry.identifier, "stall_timeout", stallTimeout.String())
			o.requestStop(issueID, "stalled", false)
		}
	}
}

func (o *Orchestrator) requestStop(issueID, reason string, cleanup bool) {
	entry, ok := o.running[issueID]
	if !ok || entry.stopReason != "" {
		return
	}
	entry.stopReason = reason
	entry.cleanupOnStop = cleanup
	entry.cancel()
}

func (o *Orchestrator) stopAll(reason string) {
	for issueID := range o.running {
		o.requestStop(issueID, reason, false)
	}
}

func (o *Orchestrator) handleWorkerExit(ctx context.Context, event workerExitedEvent) {
	entry, ok := o.running[event.issueID]
	if !ok {
		return
	}
	delete(o.running, event.issueID)
	o.totalTokens.SecondsRunning += time.Since(entry.startedAt).Seconds()

	switch entry.stopReason {
	case "terminal":
		if event.result.WorkspacePath != "" {
			if err := o.runtime.Workspace.Remove(ctx, event.result.WorkspacePath); err != nil {
				o.logger.Warn("workspace cleanup failed", "issue_id", event.issueID, "error", err)
			}
		}
		delete(o.claimed, event.issueID)
		o.logger.Info("worker stopped for terminal issue", "issue_id", event.issueID, "issue_identifier", entry.identifier)
		return
	case "non_active", "shutdown":
		delete(o.claimed, event.issueID)
		o.logger.Info("worker stopped", "issue_id", event.issueID, "issue_identifier", entry.identifier, "reason", entry.stopReason)
		return
	case "stalled":
		o.scheduleRetry(event.issueID, entry.identifier, nextAttempt(entry.retryAttempt), "worker stalled", 10*time.Second, entry.comment)
		return
	}

	if event.result.Status == "succeeded" {
		if event.result.RunType == codex.RunTypeReviewPublish || event.result.RunType == codex.RunTypeMerge {
			o.completed[event.issueID] = event.result.Issue.State
			delete(o.claimed, event.issueID)
			o.logger.Info(
				"handoff automation completed; waiting for next tracker state change",
				"issue_id", event.issueID,
				"issue_identifier", entry.identifier,
				"status", event.result.Status,
				"run_type", event.result.RunType,
				"current_state", event.result.Issue.State,
			)
			return
		}
		if o.isActive(event.result.Issue.State) {
			o.logger.Info(
				"worker run completed but issue is still active; scheduling continuation retry",
				"issue_id", event.issueID,
				"issue_identifier", entry.identifier,
				"status", event.result.Status,
				"current_state", event.result.Issue.State,
				"turn_count", entry.session.TurnCount,
				"last_event", entry.session.LastCodexEvent,
			)
		} else {
			o.logger.Info(
				"worker completed and issue is no longer active; scheduling verification retry",
				"issue_id", event.issueID,
				"issue_identifier", entry.identifier,
				"status", event.result.Status,
				"current_state", event.result.Issue.State,
			)
		}
		o.scheduleRetry(event.issueID, entry.identifier, 1, "", time.Second, entry.comment)
		return
	}
	o.logger.Warn(
		"worker failed",
		"issue_id", event.issueID,
		"issue_identifier", entry.identifier,
		"status", event.result.Status,
		"error", errorString(event.result.Err),
	)
	o.handleCommentEvent(ctx, entry, codex.Event{
		Event:      codex.EventRunFailed,
		RunType:    event.result.RunType,
		Timestamp:  time.Now().UTC(),
		IssueID:    event.result.Issue.ID,
		Identifier: event.result.Issue.Identifier,
		Workspace:  event.result.WorkspacePath,
		State:      event.result.Issue.State,
		Message:    errorString(event.result.Err),
	})
	o.scheduleRetry(
		event.issueID,
		entry.identifier,
		nextAttempt(entry.retryAttempt),
		errorString(event.result.Err),
		backoff(o.runtime.Config.Agent.MaxRetryBackoff, nextAttempt(entry.retryAttempt)),
		entry.comment,
	)
}

func (o *Orchestrator) scheduleRetry(issueID, identifier string, attempt int, errText string, delay time.Duration, comment *commentThreadState) {
	if current, ok := o.retrying[issueID]; ok {
		current.timer.Stop()
	}
	dueAt := time.Now().UTC().Add(delay)
	state := &retryState{
		entry: domain.RetryEntry{
			IssueID:    issueID,
			Identifier: identifier,
			Attempt:    attempt,
			DueAt:      dueAt,
			Error:      errText,
		},
		comment: comment,
	}
	state.timer = time.AfterFunc(delay, func() {
		o.eventCh <- retryFiredEvent{issueID: issueID}
	})
	o.retrying[issueID] = state
	o.claimed[issueID] = struct{}{}
	o.logger.Info(
		"retry scheduled",
		"issue_id", issueID,
		"issue_identifier", identifier,
		"attempt", attempt,
		"delay", delay.String(),
		"error", errText,
	)
	o.postRetryScheduledReply(context.Background(), comment, issueID, identifier, attempt, delay, errText)
}

func (o *Orchestrator) handleRetry(ctx context.Context, issueID string) {
	state, ok := o.retrying[issueID]
	if !ok {
		return
	}
	delete(o.retrying, issueID)
	if delay := o.trackerThrottleDelay(time.Now().UTC()); delay > 0 {
		args := []any{
			"issue_id", issueID,
			"issue_identifier", state.entry.Identifier,
			"delay", delay.String(),
		}
		args = append(args, o.linearRateLimitLogArgs()...)
		o.logger.Info("retry deferred by Linear request budget", args...)
		o.scheduleRetry(issueID, state.entry.Identifier, state.entry.Attempt, "tracker request throttled by Linear budget", delay, state.comment)
		return
	}
	o.logger.Info(
		"retry fired",
		"issue_id", issueID,
		"issue_identifier", state.entry.Identifier,
		"attempt", state.entry.Attempt,
	)
	o.postRetryFiredReply(ctx, issueID, state)
	issues, err := o.runtime.Tracker.FetchCandidateIssues(ctx)
	if err != nil {
		o.scheduleRetry(issueID, state.entry.Identifier, state.entry.Attempt+1, "retry poll failed", backoff(o.runtime.Config.Agent.MaxRetryBackoff, state.entry.Attempt+1), state.comment)
		return
	}
	var issue *domain.Issue
	for i := range issues {
		if issues[i].ID == issueID {
			issue = &issues[i]
			break
		}
	}
	if issue == nil {
		delete(o.claimed, issueID)
		o.logger.Info("retry released missing issue", "issue_id", issueID, "issue_identifier", state.entry.Identifier)
		return
	}
	if !o.hasGlobalSlots() || !o.hasStateSlots(issue.State) {
		o.scheduleRetry(issueID, issue.Identifier, state.entry.Attempt+1, "no available orchestrator slots", backoff(o.runtime.Config.Agent.MaxRetryBackoff, state.entry.Attempt+1), state.comment)
		return
	}
	o.logger.Info("retry dispatching issue", "issue_id", issueID, "issue_identifier", issue.Identifier, "attempt", state.entry.Attempt)
	o.dispatch(ctx, *issue, intPtr(state.entry.Attempt), state.comment)
}

func backoff(max time.Duration, attempt int) time.Duration {
	delay := 10 * time.Second
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= max {
			return max
		}
	}
	if delay > max {
		return max
	}
	return delay
}

func attemptValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func nextAttempt(current int) int {
	if current <= 0 {
		return 1
	}
	return current + 1
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func intPtr(value int) *int { return &value }
