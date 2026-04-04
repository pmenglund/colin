package slack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	slackapi "github.com/slack-go/slack"

	"github.com/pmenglund/colin/internal/userworkflow"
)

func TestPublishHomePublishesHomeTabView(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	notifier := newWithClient("C12345678", client)
	view := userworkflow.SlackHomeView{
		TotalIssues: 2,
		Groups: []userworkflow.SlackHomeStateGroup{
			{
				State: "Todo",
				Issues: []userworkflow.SlackHomeIssue{
					{Identifier: "COLIN-1", Title: "First issue", URL: "https://linear.example.test/COLIN-1"},
					{Identifier: "COLIN-2", Title: "Second issue"},
				},
			},
		},
	}

	if err := notifier.PublishHome(context.Background(), "U12345678", view); err != nil {
		t.Fatalf("PublishHome() error = %v", err)
	}
	if client.publishCalls != 1 {
		t.Fatalf("publishCalls = %d, want 1", client.publishCalls)
	}
	if client.publishReq.UserID != "U12345678" {
		t.Fatalf("publishReq.UserID = %q, want %q", client.publishReq.UserID, "U12345678")
	}
	if client.publishReq.View.Type != slackapi.VTHomeTab {
		t.Fatalf("publishReq.View.Type = %q, want %q", client.publishReq.View.Type, slackapi.VTHomeTab)
	}

	body, err := json.Marshal(client.publishReq.View)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "2 watched issues outside") {
		t.Fatalf("view json = %s, want issue summary", text)
	}
	if !strings.Contains(text, `\u003chttps://linear.example.test/COLIN-1|COLIN-1\u003e First issue`) {
		t.Fatalf("view json = %s, want linked identifier", text)
	}
	if !strings.Contains(text, "*Todo*") {
		t.Fatalf("view json = %s, want state heading", text)
	}
}

func TestPublishHomeRendersEmptyState(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	notifier := newWithClient("C12345678", client)

	if err := notifier.PublishHome(context.Background(), "U12345678", userworkflow.SlackHomeView{}); err != nil {
		t.Fatalf("PublishHome() error = %v", err)
	}

	body, err := json.Marshal(client.publishReq.View)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(body), "No watched issues are currently outside") {
		t.Fatalf("view json = %s, want empty state", string(body))
	}
}

func TestPublishHomeAddsOverflowNotice(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	notifier := newWithClient("C12345678", client)

	groups := make([]userworkflow.SlackHomeStateGroup, 0, 120)
	totalIssues := 0
	for i := 0; i < 120; i++ {
		identifier := "COLIN-" + strings.Repeat("1", 1)
		title := strings.Repeat("x", 40)
		groups = append(groups, userworkflow.SlackHomeStateGroup{
			State: "State " + strings.Repeat("a", 1) + string(rune('A'+(i%26))),
			Issues: []userworkflow.SlackHomeIssue{
				{Identifier: identifier, Title: title},
			},
		})
		totalIssues++
	}

	if err := notifier.PublishHome(context.Background(), "U12345678", userworkflow.SlackHomeView{
		TotalIssues: totalIssues,
		Groups:      groups,
	}); err != nil {
		t.Fatalf("PublishHome() error = %v", err)
	}

	body, err := json.Marshal(client.publishReq.View)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "Slack Home payload limits were reached") {
		t.Fatalf("view json = %s, want overflow note", text)
	}
	if got := len(client.publishReq.View.Blocks.BlockSet); got > maxHomeBlocks {
		t.Fatalf("block count = %d, want <= %d", got, maxHomeBlocks)
	}
}
