package slack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	slackapi "github.com/slack-go/slack"

	"github.com/pmenglund/colin/internal/notify"
)

const (
	actionIDLinearIssue = "linear_issue"
	actionIDPullRequest = "pull_request"
	actionIDMetadata    = "metadata"
	actionIDExecPlan    = "exec_plan"
)

type client interface {
	PostMessageContext(ctx context.Context, channelID string, options ...slackapi.MsgOption) (string, string, error)
	UpdateMessageContext(ctx context.Context, channelID, timestamp string, options ...slackapi.MsgOption) (string, string, string, error)
	GetPermalinkContext(ctx context.Context, params *slackapi.PermalinkParameters) (string, error)
	PublishViewContext(ctx context.Context, req slackapi.PublishViewContextRequest) (*slackapi.ViewResponse, error)
}

// Notifier keeps one Slack message per issue summary up to date.
type Notifier struct {
	channelID string
	client    client
	logger    *slog.Logger
}

// New constructs a Slack-backed issue notifier.
func New(botToken string, channelID string, logger *slog.Logger) *Notifier {
	return &Notifier{
		channelID: strings.TrimSpace(channelID),
		client:    slackapi.New(strings.TrimSpace(botToken)),
		logger:    slackLogger(logger),
	}
}

func newWithClient(channelID string, client client, logger *slog.Logger) *Notifier {
	return &Notifier{
		channelID: strings.TrimSpace(channelID),
		client:    client,
		logger:    slackLogger(logger),
	}
}

// SyncIssue creates or updates the Slack message for one issue summary.
func (n *Notifier) SyncIssue(ctx context.Context, summary notify.IssueSummary, existing notify.IssueNotificationState) (notify.IssueNotificationState, error) {
	if n == nil || n.client == nil || strings.TrimSpace(n.channelID) == "" {
		return notify.IssueNotificationState{}, nil
	}
	if strings.TrimSpace(summary.Fingerprint) != "" && strings.TrimSpace(existing.Fingerprint) == strings.TrimSpace(summary.Fingerprint) {
		n.logger.Info(
			"slack sync skipped unchanged fingerprint",
			"issue_identifier", strings.TrimSpace(summary.Identifier),
			"channel_id", strings.TrimSpace(existing.ChannelID),
			"message_ts", strings.TrimSpace(existing.MessageTS),
			"fingerprint", strings.TrimSpace(existing.Fingerprint),
		)
		return existing, nil
	}

	options := n.messageOptions(summary)
	channelID := n.channelID
	if messageTS := strings.TrimSpace(existing.MessageTS); messageTS != "" {
		n.logger.Info(
			"slack sync attempting message update",
			"issue_identifier", strings.TrimSpace(summary.Identifier),
			"configured_channel_id", channelID,
			"existing_channel_id", strings.TrimSpace(existing.ChannelID),
			"message_ts", messageTS,
			"previous_fingerprint", strings.TrimSpace(existing.Fingerprint),
			"next_fingerprint", strings.TrimSpace(summary.Fingerprint),
		)
		channel, timestamp, _, err := n.client.UpdateMessageContext(ctx, channelID, messageTS, options...)
		if err == nil {
			n.logger.Info(
				"slack sync updated existing message",
				"issue_identifier", strings.TrimSpace(summary.Identifier),
				"channel_id", strings.TrimSpace(channel),
				"message_ts", strings.TrimSpace(timestamp),
			)
			return n.notificationState(ctx, summary, existing, channel, timestamp), nil
		}
		if !isMessageNotFound(err) {
			return notify.IssueNotificationState{}, err
		}
		n.logger.Info(
			"slack sync update target missing; posting replacement message",
			"issue_identifier", strings.TrimSpace(summary.Identifier),
			"configured_channel_id", channelID,
			"existing_channel_id", strings.TrimSpace(existing.ChannelID),
			"message_ts", messageTS,
		)
	}

	channel, timestamp, err := n.client.PostMessageContext(ctx, n.channelID, options...)
	if err != nil {
		return notify.IssueNotificationState{}, err
	}
	n.logger.Info(
		"slack sync posted message",
		"issue_identifier", strings.TrimSpace(summary.Identifier),
		"channel_id", strings.TrimSpace(channel),
		"message_ts", strings.TrimSpace(timestamp),
		"previous_message_ts", strings.TrimSpace(existing.MessageTS),
	)
	return n.notificationState(ctx, summary, notify.IssueNotificationState{}, channel, timestamp), nil
}

func (n *Notifier) notificationState(ctx context.Context, summary notify.IssueSummary, existing notify.IssueNotificationState, channelID string, messageTS string) notify.IssueNotificationState {
	channelID = strings.TrimSpace(channelID)
	messageTS = strings.TrimSpace(messageTS)
	state := notify.IssueNotificationState{
		ChannelID:   channelID,
		MessageTS:   messageTS,
		Fingerprint: strings.TrimSpace(summary.Fingerprint),
	}
	permalink, err := n.client.GetPermalinkContext(ctx, &slackapi.PermalinkParameters{
		Channel: channelID,
		Ts:      messageTS,
	})
	if err != nil {
		n.logger.Info(
			"slack sync permalink lookup failed",
			"issue_identifier", strings.TrimSpace(summary.Identifier),
			"channel_id", channelID,
			"message_ts", messageTS,
			"error", err,
		)
		if sameMessage(channelID, messageTS, existing) {
			state.Permalink = strings.TrimSpace(existing.Permalink)
		}
		return state
	}
	state.Permalink = strings.TrimSpace(permalink)
	return state
}

func slackLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (n *Notifier) messageOptions(summary notify.IssueSummary) []slackapi.MsgOption {
	blocks := []slackapi.Block{
		slackapi.NewHeaderBlock(slackapi.NewTextBlockObject(slackapi.PlainTextType, summaryHeader(summary), false, false)),
		slackapi.NewSectionBlock(nil, []*slackapi.TextBlockObject{
			slackapi.NewTextBlockObject(slackapi.MarkdownType, "*State*\n"+safeBlockText(summary.State), false, false),
			slackapi.NewTextBlockObject(slackapi.MarkdownType, "*Next action*\n"+safeBlockText(summary.NextAction), false, false),
		}, nil),
	}
	if actions := linkActionBlock(summary); actions != nil {
		blocks = append(blocks, actions)
	}
	return []slackapi.MsgOption{
		slackapi.MsgOptionText(fallbackText(summary), false),
		slackapi.MsgOptionBlocks(blocks...),
	}
}

func linkActionBlock(summary notify.IssueSummary) slackapi.Block {
	var elements []slackapi.BlockElement
	if url := strings.TrimSpace(summary.LinearURL); url != "" {
		elements = append(elements, linkButton(actionIDLinearIssue, actionIDLinearIssue, "Linear issue", url))
	}
	if url := strings.TrimSpace(summary.PullRequestURL); url != "" {
		elements = append(elements, linkButton(actionIDPullRequest, actionIDPullRequest, "PR", url))
	}
	if url := strings.TrimSpace(summary.MetadataURL); url != "" {
		elements = append(elements, linkButton(actionIDMetadata, actionIDMetadata, "Colin metadata", url))
	}
	if url := strings.TrimSpace(summary.ExecPlanURL); url != "" {
		elements = append(elements, linkButton(actionIDExecPlan, actionIDExecPlan, "ExecPlan", url))
	}
	if len(elements) == 0 {
		return nil
	}
	return slackapi.NewActionBlock("issue_links", elements...)
}

func linkButton(actionID string, value string, label string, url string) *slackapi.ButtonBlockElement {
	return slackapi.NewButtonBlockElement(actionID, value, slackapi.NewTextBlockObject(slackapi.PlainTextType, label, false, false)).WithURL(url)
}

func summaryHeader(summary notify.IssueSummary) string {
	header := strings.TrimSpace(summary.Identifier)
	if title := strings.TrimSpace(summary.Title); title != "" {
		if header != "" {
			header += ": "
		}
		header += title
	}
	if header == "" {
		return "Colin issue update"
	}
	return header
}

func fallbackText(summary notify.IssueSummary) string {
	lines := []string{summaryHeader(summary)}
	if state := strings.TrimSpace(summary.State); state != "" {
		lines = append(lines, "State: "+state)
	}
	if nextAction := strings.TrimSpace(summary.NextAction); nextAction != "" {
		lines = append(lines, "Next action: "+nextAction)
	}
	var links []string
	if strings.TrimSpace(summary.LinearURL) != "" {
		links = append(links, "Linear issue")
	}
	if strings.TrimSpace(summary.PullRequestURL) != "" {
		links = append(links, "PR")
	}
	if strings.TrimSpace(summary.MetadataURL) != "" {
		links = append(links, "Colin metadata")
	}
	if strings.TrimSpace(summary.ExecPlanURL) != "" {
		links = append(links, "ExecPlan")
	}
	if len(links) > 0 {
		lines = append(lines, "Links: "+strings.Join(links, " | "))
	}
	return strings.Join(lines, "\n")
}

func safeBlockText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Not available"
	}
	return value
}

func isKnownLinkActionID(value string) bool {
	switch strings.TrimSpace(value) {
	case actionIDLinearIssue, actionIDPullRequest, actionIDMetadata, actionIDExecPlan:
		return true
	default:
		return false
	}
}

func isMessageNotFound(err error) bool {
	var response slackapi.SlackErrorResponse
	return errors.As(err, &response) && strings.EqualFold(strings.TrimSpace(response.Err), "message_not_found")
}

func sameMessage(channelID string, messageTS string, existing notify.IssueNotificationState) bool {
	return channelID == strings.TrimSpace(existing.ChannelID) &&
		messageTS == strings.TrimSpace(existing.MessageTS)
}
