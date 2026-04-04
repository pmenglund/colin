package slack

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	slackapi "github.com/slack-go/slack"

	"github.com/pmenglund/colin/internal/userworkflow"
)

const (
	maxHomeBlocks      = 100
	maxHomeSectionText = 2800
)

type homeChunk struct {
	text       string
	issueCount int
}

// PublishHome publishes the current watched-issue view to a Slack user's App Home tab.
func (n *Notifier) PublishHome(ctx context.Context, userID string, view userworkflow.SlackHomeView) error {
	if n == nil || n.client == nil || strings.TrimSpace(userID) == "" {
		return nil
	}
	request := slackapi.HomeTabViewRequest{
		Type:   slackapi.VTHomeTab,
		Blocks: slackapi.Blocks{BlockSet: homeBlocks(view)},
	}
	_, err := n.client.PublishViewContext(ctx, slackapi.PublishViewContextRequest{
		UserID: strings.TrimSpace(userID),
		View:   request,
	})
	return err
}

func homeBlocks(view userworkflow.SlackHomeView) []slackapi.Block {
	blocks := []slackapi.Block{
		slackapi.NewHeaderBlock(slackapi.NewTextBlockObject(slackapi.PlainTextType, "Colin", false, false)),
		slackapi.NewSectionBlock(slackapi.NewTextBlockObject(slackapi.MarkdownType, homeSummaryText(view.TotalIssues), false, false), nil, nil),
	}

	if view.TotalIssues == 0 || len(view.Groups) == 0 {
		return append(blocks, slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject(slackapi.MarkdownType, "No watched issues are currently outside `Backlog` and terminal states.", false, false),
			nil,
			nil,
		))
	}

	renderedIssues := 0
	overflowed := false
	for _, group := range view.Groups {
		chunks := homeGroupChunks(group)
		for _, chunk := range chunks {
			if len(blocks) >= maxHomeBlocks-1 {
				overflowed = true
				break
			}
			blocks = append(blocks, slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject(slackapi.MarkdownType, chunk.text, false, false),
				nil,
				nil,
			))
			renderedIssues += chunk.issueCount
		}
		if overflowed {
			break
		}
	}

	if omitted := view.TotalIssues - renderedIssues; omitted > 0 {
		note := fmt.Sprintf("Showing %d of %d eligible issues because Slack Home payload limits were reached. Use Linear for the complete board.", renderedIssues, view.TotalIssues)
		if len(blocks) >= maxHomeBlocks {
			blocks = blocks[:maxHomeBlocks-1]
		}
		blocks = append(blocks, slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject(slackapi.MarkdownType, note, false, false),
			nil,
			nil,
		))
	}

	return blocks
}

func homeSummaryText(total int) string {
	switch total {
	case 1:
		return "1 watched issue outside `Backlog` and terminal states."
	default:
		return fmt.Sprintf("%d watched issues outside `Backlog` and terminal states.", total)
	}
}

func homeGroupChunks(group userworkflow.SlackHomeStateGroup) []homeChunk {
	if len(group.Issues) == 0 {
		return nil
	}

	prefix := "*" + strings.TrimSpace(group.State) + "*\n"
	current := prefix
	currentCount := 0
	var chunks []homeChunk
	for _, issue := range group.Issues {
		line := "• " + truncateHomeIssueLine(homeIssueLine(issue), maxHomeSectionText-runeCount(prefix)-runeCount("• "))
		candidate := current + line + "\n"
		if current != prefix && runeCount(candidate) > maxHomeSectionText {
			chunks = append(chunks, homeChunk{text: strings.TrimSpace(current), issueCount: currentCount})
			current = prefix + line + "\n"
			currentCount = 1
		} else {
			current = candidate
			currentCount++
		}
	}
	if strings.TrimSpace(current) != strings.TrimSpace(prefix) {
		chunks = append(chunks, homeChunk{text: strings.TrimSpace(current), issueCount: currentCount})
	}
	return chunks
}

func homeIssueLine(issue userworkflow.SlackHomeIssue) string {
	identifier := strings.TrimSpace(issue.Identifier)
	title := strings.TrimSpace(issue.Title)
	if url := strings.TrimSpace(issue.URL); url != "" && identifier != "" {
		identifier = "<" + url + "|" + identifier + ">"
	}
	switch {
	case identifier != "" && title != "":
		return identifier + " " + title
	case identifier != "":
		return identifier
	case title != "":
		return title
	default:
		return strings.TrimSpace(issue.ID)
	}
}

func truncateHomeIssueLine(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	if runeCount(value) <= limit {
		return value
	}
	if limit <= 3 {
		return string([]rune(value)[:limit])
	}
	return string([]rune(value)[:limit-3]) + "..."
}

func runeCount(value string) int {
	return utf8.RuneCountInString(value)
}
