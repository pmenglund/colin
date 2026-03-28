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
func (o *Orchestrator) Snapshot() domain.Snapshot {
	running := make([]domain.SnapshotRunning, 0, len(o.running))
	for _, entry := range o.running {
		running = append(running, domain.SnapshotRunning{
			IssueID:      entry.issue.ID,
			Identifier:   entry.issue.Identifier,
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
		totals.SecondsRunning += time.Since(entry.startedAt).Seconds()
	}
	return domain.Snapshot{
		GeneratedAt: time.Now().UTC(),
		Running:     running,
		Retrying:    retrying,
		CodexTotals: totals,
		RateLimits:  o.rateLimits,
		Counts: map[string]int{
			"running":  len(running),
			"retrying": len(retrying),
		},
	}
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
