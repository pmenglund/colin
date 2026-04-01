package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/workspace"
)

// Snapshot returns a read-only summary of current runtime state for logs and future status surfaces.
func (o *Orchestrator) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	_ = ctx
	if !o.loopStarted.Load() {
		return o.snapshotAt(time.Now().UTC()), nil
	}
	if cached, ok := o.snapshot.Load().(domain.Snapshot); ok {
		return cloneSnapshot(cached), nil
	}
	return domain.Snapshot{}, nil
}

func (o *Orchestrator) snapshotAt(now time.Time) domain.Snapshot {
	running := make([]domain.SnapshotRunning, 0, len(o.running))
	for _, entry := range o.running {
		running = append(running, domain.SnapshotRunning{
			IssueID:      entry.issue.ID,
			Identifier:   entry.issue.Identifier,
			Title:        entry.issue.Title,
			URL:          entry.issue.URL,
			State:        entry.issue.State,
			SessionID:    entry.session.SessionID,
			TurnCount:    entry.session.TurnCount,
			LastEvent:    entry.session.LastCodexEvent,
			LastMessage:  entry.session.LastCodexMessage,
			StartedAt:    entry.startedAt,
			LastEventAt:  entry.session.LastCodexTimestamp,
			InputTokens:  entry.session.CodexInputTokens,
			OutputTokens: entry.session.CodexOutputTokens,
			TotalTokens:  entry.session.CodexTotalTokens,
			OutputLog:    append([]domain.OutputLog(nil), entry.outputLog...),
		})
	}
	sort.Slice(running, func(i, j int) bool { return running[i].Identifier < running[j].Identifier })

	retrying := make([]domain.RetryEntry, 0, len(o.retrying))
	for _, entry := range o.retrying {
		retrying = append(retrying, entry.entry)
	}
	sort.Slice(retrying, func(i, j int) bool { return retrying[i].Identifier < retrying[j].Identifier })

	totals := o.totalTokens
	for _, entry := range o.running {
		totals.SecondsRunning += now.Sub(entry.startedAt).Seconds()
	}
	return domain.Snapshot{
		GeneratedAt: now,
		Running:     running,
		Retrying:    retrying,
		CodexTotals: totals,
		RateLimits:  mergeRateLimits(o.rateLimits, o.runtime.Tracker.CurrentRateLimits()),
		Counts: map[string]int{
			"running":  len(running),
			"retrying": len(retrying),
		},
		IssueStates:       cloneCounts(o.issueStates),
		PausedIssueStates: clonePausedStateSummaries(o.pausedIssueStates),
	}
}

func (o *Orchestrator) publishSnapshot(now time.Time) {
	if o == nil {
		return
	}
	o.snapshot.Store(o.snapshotAt(now))
}

func cloneSnapshot(input domain.Snapshot) domain.Snapshot {
	out := input
	out.Running = cloneSnapshotRunning(input.Running)
	out.Retrying = append([]domain.RetryEntry(nil), input.Retrying...)
	out.RateLimits = cloneRateLimitSnapshot(input.RateLimits)
	out.Counts = cloneCounts(input.Counts)
	out.IssueStates = cloneCounts(input.IssueStates)
	out.PausedIssueStates = clonePausedStateSummaries(input.PausedIssueStates)
	if len(input.Tracked) > 0 {
		out.Tracked = make(map[string]struct{}, len(input.Tracked))
		for key, value := range input.Tracked {
			out.Tracked[key] = value
		}
	} else {
		out.Tracked = nil
	}
	return out
}

func cloneSnapshotRunning(input []domain.SnapshotRunning) []domain.SnapshotRunning {
	if len(input) == 0 {
		return nil
	}
	out := make([]domain.SnapshotRunning, 0, len(input))
	for _, entry := range input {
		cloned := entry
		if entry.URL != nil {
			value := *entry.URL
			cloned.URL = &value
		}
		if entry.LastEventAt != nil {
			value := *entry.LastEventAt
			cloned.LastEventAt = &value
		}
		cloned.OutputLog = append([]domain.OutputLog(nil), entry.OutputLog...)
		out = append(out, cloned)
	}
	return out
}

func cloneCounts(input map[string]int) map[string]int {
	if len(input) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func clonePausedStateSummaries(input map[string]domain.PausedStateSummary) map[string]domain.PausedStateSummary {
	if len(input) == 0 {
		return map[string]domain.PausedStateSummary{}
	}
	out := make(map[string]domain.PausedStateSummary, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func mergeRateLimits(base domain.RateLimitSnapshot, overlay domain.RateLimitSnapshot) domain.RateLimitSnapshot {
	switch {
	case len(base) == 0 && len(overlay) == 0:
		return nil
	case len(base) == 0:
		return cloneRateLimitSnapshot(overlay)
	case len(overlay) == 0:
		return cloneRateLimitSnapshot(base)
	}

	out := cloneRateLimitSnapshot(base)
	for key, value := range overlay {
		out[key] = cloneRateLimitWindow(value)
	}
	return out
}

func cloneRateLimitSnapshot(input domain.RateLimitSnapshot) domain.RateLimitSnapshot {
	if len(input) == 0 {
		return nil
	}
	out := make(domain.RateLimitSnapshot, len(input))
	for key, value := range input {
		out[key] = cloneRateLimitWindow(value)
	}
	return out
}

func cloneRateLimitWindow(input domain.RateLimitWindow) domain.RateLimitWindow {
	out := input
	if input.WindowDurationMinutes != nil {
		value := *input.WindowDurationMinutes
		out.WindowDurationMinutes = &value
	}
	if input.Limit != nil {
		value := *input.Limit
		out.Limit = &value
	}
	if input.Remaining != nil {
		value := *input.Remaining
		out.Remaining = &value
	}
	if input.UsedPercent != nil {
		value := *input.UsedPercent
		out.UsedPercent = &value
	}
	if input.ResetsAt != nil {
		value := *input.ResetsAt
		out.ResetsAt = &value
	}
	if input.NextAllowedAt != nil {
		value := *input.NextAllowedAt
		out.NextAllowedAt = &value
	}
	return out
}

// StartupTerminalCleanup removes stale workspaces for issues already in terminal tracker states.
func (o *Orchestrator) StartupTerminalCleanup(ctx context.Context) error {
	issues, err := o.runtime.Tracker.FetchIssuesByStates(ctx, o.runtime.Config.Tracker.TerminalStates)
	if err != nil {
		return fmt.Errorf("startup terminal cleanup: %w", err)
	}
	for _, issue := range issues {
		ws := domain.Workspace{
			Path: fmt.Sprintf("%s/%s", o.runtime.Config.Workspace.Root, workspace.SanitizeWorkspaceKey(issue.Identifier)),
		}
		if err := o.runtime.Workspace.Remove(ctx, ws.Path); err != nil {
			o.logger.Warn("terminal workspace cleanup failed", "issue_identifier", issue.Identifier, "error", err)
		}
	}
	return nil
}

func (o *Orchestrator) runningIssueSummaries(now time.Time) []string {
	if len(o.running) == 0 {
		return nil
	}

	issueIDs := make([]string, 0, len(o.running))
	for issueID := range o.running {
		issueIDs = append(issueIDs, issueID)
	}
	sort.Strings(issueIDs)

	summaries := make([]string, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		entry := o.running[issueID]
		if entry == nil {
			continue
		}
		idle := "unknown"
		if entry.session.LastCodexTimestamp != nil {
			idle = now.Sub(*entry.session.LastCodexTimestamp).Round(time.Second).String()
		}
		lastEvent := entry.session.LastCodexEvent
		if lastEvent == "" {
			lastEvent = "none"
		}
		summaries = append(summaries, fmt.Sprintf(
			"%s(state=%s,turns=%d,last_event=%s,age=%s,idle=%s)",
			entry.identifier,
			entry.issue.State,
			entry.session.TurnCount,
			lastEvent,
			now.Sub(entry.startedAt).Round(time.Second),
			idle,
		))
	}
	return summaries
}

func (o *Orchestrator) retrySummaries() []string {
	if len(o.retrying) == 0 {
		return nil
	}

	issueIDs := make([]string, 0, len(o.retrying))
	for issueID := range o.retrying {
		issueIDs = append(issueIDs, issueID)
	}
	sort.Strings(issueIDs)

	summaries := make([]string, 0, len(issueIDs))
	now := time.Now().UTC()
	for _, issueID := range issueIDs {
		entry := o.retrying[issueID]
		if entry == nil {
			continue
		}
		summaries = append(summaries, fmt.Sprintf(
			"%s(attempt=%d,due_in=%s,error=%s)",
			entry.entry.Identifier,
			entry.entry.Attempt,
			entry.entry.DueAt.Sub(now).Round(time.Second),
			entry.entry.Error,
		))
	}
	return summaries
}
