package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/domain"
)

func (o *Orchestrator) handleCommentEvent(ctx context.Context, entry *runningEntry, event codex.Event) {
	if entry == nil {
		return
	}

	runType := event.RunType
	if runType == "" {
		runType = runTypeForState(o, entry.issue.State)
	}
	if entry.comment == nil {
		entry.comment = &commentThreadState{RunType: runType}
	} else if entry.comment.RunType != runType {
		entry.comment = &commentThreadState{RunType: runType}
	}

	if shouldCreateRootComment(event.Event) && entry.comment.RootCommentID == "" {
		o.createRootComment(ctx, entry, event)
	}
	if body, ok := o.replyBodyForEvent(entry, event); ok {
		o.postReply(ctx, entry, body)
	}
}

func (o *Orchestrator) createRootComment(ctx context.Context, entry *runningEntry, event codex.Event) {
	if o.runtime.Tracker == nil {
		return
	}
	body := rootCommentBody(entry, event)
	if strings.TrimSpace(body) == "" {
		return
	}

	commentID, err := o.withCommentTimeout(ctx, func(ctx context.Context) (string, error) {
		return o.runtime.Tracker.CreateIssueComment(ctx, entry.issue.ID, body)
	})
	if err != nil {
		o.logger.Warn(
			"failed to create Linear progress comment",
			"issue_id", entry.issue.ID,
			"issue_identifier", entry.identifier,
			"run_type", entry.comment.RunType,
			"error", err,
		)
		return
	}
	entry.comment.RootCommentID = commentID
	o.logger.Info(
		"created Linear progress comment",
		"issue_id", entry.issue.ID,
		"issue_identifier", entry.identifier,
		"run_type", entry.comment.RunType,
		"comment_id", commentID,
	)
}

func (o *Orchestrator) postReply(ctx context.Context, entry *runningEntry, body string) {
	if o.runtime.Tracker == nil || entry == nil || entry.comment == nil || entry.comment.RootCommentID == "" || strings.TrimSpace(body) == "" {
		return
	}

	_, err := o.withCommentTimeout(ctx, func(ctx context.Context) (string, error) {
		return o.runtime.Tracker.CreateCommentReply(ctx, entry.issue.ID, entry.comment.RootCommentID, body)
	})
	if err != nil {
		o.logger.Warn(
			"failed to create Linear progress reply",
			"issue_id", entry.issue.ID,
			"issue_identifier", entry.identifier,
			"comment_id", entry.comment.RootCommentID,
			"error", err,
		)
	}
}

func (o *Orchestrator) withCommentTimeout(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
	commentCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return fn(commentCtx)
}

func shouldCreateRootComment(eventName string) bool {
	switch eventName {
	case codex.EventSessionStarted, codex.EventReviewPublishStarted, codex.EventMergeStarted, codex.EventRunFailed:
		return true
	default:
		return false
	}
}

func rootCommentBody(entry *runningEntry, event codex.Event) string {
	lines := []string{
		"Colin started work on this issue.",
		"",
		fmt.Sprintf("- Issue: `%s`", entry.identifier),
		fmt.Sprintf("- Run type: `%s`", event.RunType),
		fmt.Sprintf("- Attempt: `%d`", event.Attempt),
		fmt.Sprintf("- State: `%s`", event.State),
	}
	if event.Workspace != "" {
		lines = append(lines, fmt.Sprintf("- Workspace: `%s`", event.Workspace))
	}
	if event.SessionID != "" {
		lines = append(lines, fmt.Sprintf("- Session ID: `%s`", event.SessionID))
	}
	if event.ThreadID != "" {
		lines = append(lines, fmt.Sprintf("- Thread ID: `%s`", event.ThreadID))
	}
	if event.Branch != "" {
		lines = append(lines, fmt.Sprintf("- Branch: `%s`", event.Branch))
	}
	if event.BaseRef != "" {
		lines = append(lines, fmt.Sprintf("- Base ref: `%s`", event.BaseRef))
	}
	return strings.Join(lines, "\n")
}

func (o *Orchestrator) replyBodyForEvent(entry *runningEntry, event codex.Event) (string, bool) {
	switch event.Event {
	case codex.EventIssueStateRefreshed:
		return fmt.Sprintf(
			"Turn finished.\n\n- Duration: `%s`\n- Previous state: `%s`\n- Current state: `%s`",
			event.Duration.Round(time.Millisecond),
			fallbackString(event.PrevState, "unknown"),
			fallbackString(event.State, "unknown"),
		), true
	case codex.EventContinuationNeeded:
		return fmt.Sprintf("Issue is still active in `%s`; Colin is continuing work in the same run.", fallbackString(event.State, "unknown")), true
	case codex.EventReviewPublishDone:
		lines := []string{"Review automation completed."}
		if event.Branch != "" {
			lines = append(lines, "", fmt.Sprintf("- Branch: `%s`", event.Branch))
		}
		if event.BaseRef != "" {
			lines = append(lines, fmt.Sprintf("- Base ref: `%s`", event.BaseRef))
		}
		if event.PRNumber > 0 {
			lines = append(lines, fmt.Sprintf("- PR: `#%d`", event.PRNumber))
		}
		if event.PRURL != "" {
			lines = append(lines, fmt.Sprintf("- PR URL: %s", event.PRURL))
		}
		return strings.Join(lines, "\n"), true
	case codex.EventMergeDone:
		lines := []string{"Merge automation completed."}
		if event.PRNumber > 0 {
			lines = append(lines, "", fmt.Sprintf("- PR: `#%d`", event.PRNumber))
		}
		if event.PRURL != "" {
			lines = append(lines, fmt.Sprintf("- PR URL: %s", event.PRURL))
		}
		if event.Action != "" {
			lines = append(lines, fmt.Sprintf("- Action: `%s`", event.Action))
		}
		return strings.Join(lines, "\n"), true
	case codex.EventRetryScheduled:
		if event.Message == "" {
			return "", false
		}
		return event.Message, true
	case codex.EventRetryFired:
		if event.Message == "" {
			return "", false
		}
		return event.Message, true
	case codex.EventRunFailed:
		if event.Message == "" {
			return "", false
		}
		return fmt.Sprintf("Run failed.\n\n- Error: %s", event.Message), true
	case codex.EventRunSucceeded:
		if event.RunType == codex.RunTypeCoding && !o.issueStateIsHandoffOrTerminal(event.State) {
			return "", false
		}
		return fmt.Sprintf("Run completed successfully.\n\n- Current state: `%s`", fallbackString(event.State, "unknown")), true
	default:
		return "", false
	}
}

func (o *Orchestrator) postRetryScheduledReply(ctx context.Context, comment *commentThreadState, issueID string, identifier string, attempt int, delay time.Duration, errText string) {
	if comment == nil || comment.RootCommentID == "" {
		return
	}
	entry := &runningEntry{
		issue:      domainIssue(issueID, identifier),
		comment:    comment,
		identifier: identifier,
	}
	message := fmt.Sprintf("Colin scheduled retry attempt `%d` in `%s`.", attempt, delay.Round(time.Second))
	if strings.TrimSpace(errText) != "" {
		message += fmt.Sprintf("\n\n- Reason: %s", errText)
	}
	o.postReply(ctx, entry, message)
}

func (o *Orchestrator) postRetryFiredReply(ctx context.Context, issueID string, state *retryState) {
	if state == nil || state.comment == nil || state.comment.RootCommentID == "" {
		return
	}
	entry := &runningEntry{
		issue:      domainIssue(issueID, state.entry.Identifier),
		identifier: state.entry.Identifier,
		comment:    state.comment,
	}
	o.postReply(ctx, entry, fmt.Sprintf("Colin is starting retry attempt `%d`.", state.entry.Attempt))
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func runTypeForState(o *Orchestrator, state string) string {
	switch {
	case o.isPublishState(state):
		return codex.RunTypeReviewPublish
	case o.isMergeState(state):
		return codex.RunTypeMerge
	default:
		return codex.RunTypeCoding
	}
}

func (o *Orchestrator) issueStateIsHandoffOrTerminal(state string) bool {
	return o.isPublishState(state) || o.isMergeState(state) || o.isTerminal(state)
}

func domainIssue(issueID string, identifier string) domain.Issue {
	return domain.Issue{ID: issueID, Identifier: identifier}
}
