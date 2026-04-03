package slack

import (
	"context"
	"errors"
	"strings"

	slackapi "github.com/slack-go/slack"

	"github.com/pmenglund/colin/internal/notify"
)

type client interface {
	PostMessageContext(ctx context.Context, channelID string, options ...slackapi.MsgOption) (string, string, error)
	UpdateMessageContext(ctx context.Context, channelID, timestamp string, options ...slackapi.MsgOption) (string, string, string, error)
	GetPermalinkContext(ctx context.Context, params *slackapi.PermalinkParameters) (string, error)
}

// Notifier keeps one Slack message per issue summary up to date.
type Notifier struct {
	channelID string
	client    client
}

// New constructs a Slack-backed issue notifier.
func New(botToken string, channelID string) *Notifier {
	return &Notifier{
		channelID: strings.TrimSpace(channelID),
		client:    slackapi.New(strings.TrimSpace(botToken)),
	}
}

func newWithClient(channelID string, client client) *Notifier {
	return &Notifier{
		channelID: strings.TrimSpace(channelID),
		client:    client,
	}
}

// SyncIssue creates or updates the Slack message for one issue summary.
func (n *Notifier) SyncIssue(ctx context.Context, summary notify.IssueSummary, existing notify.IssueNotificationState) (notify.IssueNotificationState, error) {
	if n == nil || n.client == nil || strings.TrimSpace(n.channelID) == "" {
		return notify.IssueNotificationState{}, nil
	}
	if strings.TrimSpace(summary.Fingerprint) != "" && strings.TrimSpace(existing.Fingerprint) == strings.TrimSpace(summary.Fingerprint) {
		return existing, nil
	}

	options := n.messageOptions(summary)
	channelID := n.channelID
	if messageTS := strings.TrimSpace(existing.MessageTS); messageTS != "" {
		channel, timestamp, _, err := n.client.UpdateMessageContext(ctx, channelID, messageTS, options...)
		if err == nil {
			return n.notificationState(ctx, summary, existing, channel, timestamp), nil
		}
		if !isMessageNotFound(err) {
			return notify.IssueNotificationState{}, err
		}
	}

	channel, timestamp, err := n.client.PostMessageContext(ctx, n.channelID, options...)
	if err != nil {
		return notify.IssueNotificationState{}, err
	}
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
		if sameMessage(channelID, messageTS, existing) {
			state.Permalink = strings.TrimSpace(existing.Permalink)
		}
		return state
	}
	state.Permalink = strings.TrimSpace(permalink)
	return state
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
		elements = append(elements, linkButton("linear_issue", "linear_issue", "Linear issue", url))
	}
	if url := strings.TrimSpace(summary.PullRequestURL); url != "" {
		elements = append(elements, linkButton("pull_request", "pull_request", "PR", url))
	}
	if url := strings.TrimSpace(summary.MetadataURL); url != "" {
		elements = append(elements, linkButton("metadata", "metadata", "Colin metadata", url))
	}
	if url := strings.TrimSpace(summary.ExecPlanURL); url != "" {
		elements = append(elements, linkButton("exec_plan", "exec_plan", "ExecPlan", url))
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

func isMessageNotFound(err error) bool {
	var response slackapi.SlackErrorResponse
	return errors.As(err, &response) && strings.EqualFold(strings.TrimSpace(response.Err), "message_not_found")
}

func sameMessage(channelID string, messageTS string, existing notify.IssueNotificationState) bool {
	return channelID == strings.TrimSpace(existing.ChannelID) &&
		messageTS == strings.TrimSpace(existing.MessageTS)
}
