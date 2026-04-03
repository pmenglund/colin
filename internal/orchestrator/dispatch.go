package orchestrator

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
)

func (o *Orchestrator) shouldDispatch(issue domain.Issue) bool {
	if o.draining {
		return false
	}
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return false
	}
	if o.isTerminal(issue.State) || !o.isDispatchable(issue.State) {
		return false
	}
	if hasIssueLabel(issue, domain.PausedIssueLabel) {
		return false
	}
	if _, ok := o.running[issue.ID]; ok {
		return false
	}
	if _, ok := o.claimed[issue.ID]; ok {
		return false
	}
	if completedState, ok := o.completed[issue.ID]; ok {
		if config.StateKey(completedState) == config.StateKey(issue.State) {
			return false
		}
		delete(o.completed, issue.ID)
	}
	if !o.hasStateSlots(issue.State) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(issue.State), "todo") {
		for _, blocker := range issue.BlockedBy {
			if blocker.State == nil {
				return false
			}
			if !o.isTerminal(*blocker.State) {
				return false
			}
		}
	}
	return true
}

func (o *Orchestrator) hasGlobalSlots() bool {
	return len(o.running) < o.runtime.Config.Agent.MaxConcurrentAgents
}

func (o *Orchestrator) hasStateSlots(state string) bool {
	key := config.StateKey(state)
	limit, ok := o.runtime.Config.Agent.MaxConcurrentAgentsByState[key]
	if !ok {
		return o.hasGlobalSlots()
	}
	count := 0
	for _, entry := range o.running {
		if config.StateKey(entry.issue.State) == key {
			count++
		}
	}
	return count < limit
}

func (o *Orchestrator) dispatch(parent context.Context, issue domain.Issue, attempt *int, comment *commentThreadState) {
	ctx, cancel := context.WithCancel(parent)
	runType := runTypeForState(o, issue.State)
	if comment != nil && comment.RunType != runTypeForState(o, issue.State) {
		comment = nil
	}
	if comment == nil {
		comment = &commentThreadState{RunType: runType}
	}
	entry := &runningEntry{
		issue:        issue,
		identifier:   issue.Identifier,
		runType:      runType,
		startedAt:    time.Now().UTC(),
		comment:      comment,
		retryAttempt: attemptValue(attempt),
		cancel:       cancel,
	}
	o.running[issue.ID] = entry
	o.claimed[issue.ID] = struct{}{}
	o.logger.Info(
		"dispatching issue",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"state", issue.State,
		"attempt", attemptValue(attempt),
	)
	if existing, ok := o.retrying[issue.ID]; ok {
		if entry.comment == nil {
			entry.comment = existing.comment
		}
		existing.timer.Stop()
		delete(o.retrying, issue.ID)
	}

	go func(issue domain.Issue, attempt *int) {
		result := o.runtime.Runner.Run(ctx, issue, attempt, func(event codex.Event) {
			o.eventCh <- codexEvent{event: event}
		})
		o.eventCh <- workerExitedEvent{issueID: issue.ID, result: result}
	}(issue, attempt)
}

func sortIssues(issues []domain.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		leftPriority := issuePriority(issues[i])
		rightPriority := issuePriority(issues[j])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		leftCreated := issueCreated(issues[i])
		rightCreated := issueCreated(issues[j])
		if !leftCreated.Equal(rightCreated) {
			return leftCreated.Before(rightCreated)
		}
		return issues[i].Identifier < issues[j].Identifier
	})
}

func issuePriority(issue domain.Issue) int {
	if issue.Priority == nil {
		return 1 << 30
	}
	return *issue.Priority
}

func issueCreated(issue domain.Issue) time.Time {
	if issue.CreatedAt == nil {
		return time.Unix(1<<62, 0)
	}
	return *issue.CreatedAt
}

func (o *Orchestrator) isActive(state string) bool {
	return config.ContainsState(o.runtime.Config.Tracker.ActiveStates, state)
}

func (o *Orchestrator) isPublishState(state string) bool {
	return config.ContainsState(o.runtime.Config.Repo.PublishStates, state)
}

func (o *Orchestrator) isMergeState(state string) bool {
	return config.ContainsState(o.runtime.Config.Repo.MergeStates, state)
}

func (o *Orchestrator) isDispatchable(state string) bool {
	return o.isActive(state) || o.isPublishState(state) || o.isMergeState(state)
}

func (o *Orchestrator) isTerminal(state string) bool {
	return config.ContainsState(o.runtime.Config.Tracker.TerminalStates, state)
}
