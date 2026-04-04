package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/notify"
	"github.com/pmenglund/colin/internal/userworkflow"
)

func (o *Orchestrator) syncSlackIssues(ctx context.Context, issues []domain.Issue) {
	for _, issue := range issues {
		if ctx.Err() != nil {
			return
		}
		issue = o.mergeKnownSlackIssueContext(issue)
		if !o.shouldSyncSlackIssue(issue) {
			continue
		}
		issue = o.syncSlackIssue(ctx, issue)
		o.rememberSlackIssueContext(issue)
	}
}

func (o *Orchestrator) mergeKnownSlackIssueContext(issue domain.Issue) domain.Issue {
	if o == nil {
		return issue
	}
	entry, ok := o.running[issue.ID]
	if !ok || entry == nil {
		return issue
	}
	return mergeRunningIssueContext(entry.issue, issue)
}

func (o *Orchestrator) rememberSlackIssueContext(issue domain.Issue) {
	if o == nil {
		return
	}
	entry, ok := o.running[issue.ID]
	if !ok || entry == nil {
		return
	}
	entry.issue = mergeRunningIssueContext(entry.issue, issue)
}

func (o *Orchestrator) shouldSyncSlackIssue(issue domain.Issue) bool {
	if o == nil {
		return false
	}
	if o.isActive(issue.State) || o.isPublishState(issue.State) || o.isMergeState(issue.State) {
		return true
	}
	if !o.isTerminal(issue.State) {
		return false
	}

	summary := userworkflow.SlackIssueSummary(o.runtime.Config, issue)
	existing := currentIssueNotificationState(issue)
	return strings.TrimSpace(summary.Fingerprint) != strings.TrimSpace(existing.Fingerprint)
}

func (o *Orchestrator) syncSlackIssue(ctx context.Context, issue domain.Issue) domain.Issue {
	if o == nil || o.runtime.Notifier == nil {
		return issue
	}
	if strings.TrimSpace(o.runtime.Config.Slack.BotToken) == "" || strings.TrimSpace(o.runtime.Config.Slack.ChannelID) == "" {
		return issue
	}

	summary := userworkflow.SlackIssueSummary(o.runtime.Config, issue)
	existing := currentIssueNotificationState(issue)
	o.logger.Info(
		"starting slack issue sync",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"state", issue.State,
		"existing_channel_id", strings.TrimSpace(existing.ChannelID),
		"existing_message_ts", strings.TrimSpace(existing.MessageTS),
		"existing_permalink", strings.TrimSpace(existing.Permalink),
		"existing_fingerprint", strings.TrimSpace(existing.Fingerprint),
		"next_fingerprint", strings.TrimSpace(summary.Fingerprint),
	)
	state, err := o.runtime.Notifier.SyncIssue(ctx, summary, existing)
	if err != nil {
		o.logger.Warn("failed to sync Slack issue summary", slackLogArgs(issue, err)...)
		return issue
	}
	o.logger.Info(
		"completed slack issue sync",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"result_channel_id", strings.TrimSpace(state.ChannelID),
		"result_message_ts", strings.TrimSpace(state.MessageTS),
		"result_permalink", strings.TrimSpace(state.Permalink),
		"result_fingerprint", strings.TrimSpace(state.Fingerprint),
	)
	if sameIssueNotificationState(state, existing) {
		o.logger.Info(
			"slack issue sync produced no metadata change",
			"issue_id", issue.ID,
			"issue_identifier", issue.Identifier,
		)
		return issue
	}

	return o.persistSlackNotificationState(ctx, issue, state)
}

func (o *Orchestrator) persistSlackNotificationState(ctx context.Context, issue domain.Issue, state notify.IssueNotificationState) domain.Issue {
	if o.runtime.Tracker == nil {
		return issue
	}

	metadata := domain.ColinMetadata{}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
	}
	metadata.SlackChannelID = strings.TrimSpace(state.ChannelID)
	metadata.SlackMessageTS = strings.TrimSpace(state.MessageTS)
	metadata.SlackPermalink = strings.TrimSpace(state.Permalink)
	metadata.SlackSummaryFingerprint = strings.TrimSpace(state.Fingerprint)
	now := time.Now().UTC()
	metadata.UpdatedAt = &now

	persisted, err := o.runtime.Tracker.UpsertIssueMetadata(ctx, issue.ID, metadata)
	if err != nil {
		o.logger.Warn("failed to persist Slack issue summary metadata", slackLogArgs(issue, err)...)
		issue.ColinMetadata = &metadata
		return issue
	}
	o.logger.Info(
		"persisted slack issue summary metadata",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"attachment_id", strings.TrimSpace(persisted.AttachmentID),
		"channel_id", strings.TrimSpace(persisted.SlackChannelID),
		"message_ts", strings.TrimSpace(persisted.SlackMessageTS),
		"permalink", strings.TrimSpace(persisted.SlackPermalink),
		"fingerprint", strings.TrimSpace(persisted.SlackSummaryFingerprint),
	)
	issue.ColinMetadata = &persisted
	return issue
}

func currentIssueNotificationState(issue domain.Issue) notify.IssueNotificationState {
	if issue.ColinMetadata == nil {
		return notify.IssueNotificationState{}
	}
	return notify.IssueNotificationState{
		ChannelID:   strings.TrimSpace(issue.ColinMetadata.SlackChannelID),
		MessageTS:   strings.TrimSpace(issue.ColinMetadata.SlackMessageTS),
		Permalink:   strings.TrimSpace(issue.ColinMetadata.SlackPermalink),
		Fingerprint: strings.TrimSpace(issue.ColinMetadata.SlackSummaryFingerprint),
	}
}

func sameIssueNotificationState(left notify.IssueNotificationState, right notify.IssueNotificationState) bool {
	return strings.TrimSpace(left.ChannelID) == strings.TrimSpace(right.ChannelID) &&
		strings.TrimSpace(left.MessageTS) == strings.TrimSpace(right.MessageTS) &&
		strings.TrimSpace(left.Permalink) == strings.TrimSpace(right.Permalink) &&
		strings.TrimSpace(left.Fingerprint) == strings.TrimSpace(right.Fingerprint)
}

func slackLogArgs(issue domain.Issue, err error) []any {
	args := []any{
		slog.String("issue_id", issue.ID),
		slog.String("issue_identifier", issue.Identifier),
	}
	if err != nil {
		args = append(args, slog.Any("error", err))
	}
	return args
}
