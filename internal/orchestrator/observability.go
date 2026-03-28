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
