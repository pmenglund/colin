package slack

import (
	"context"
	"errors"
	"reflect"
	"testing"

	slackapi "github.com/slack-go/slack"

	"github.com/pmenglund/colin/internal/notify"
)

type fakeClient struct {
	postCalls      int
	updateCalls    int
	permalinkCalls int
	publishCalls   int
	postChannel    string
	updateChannel  string
	updateTS       string
	postOptions    []slackapi.MsgOption
	updateOptions  []slackapi.MsgOption
	publishReq     slackapi.PublishViewContextRequest
	postErr        error
	updateErr      error
	permalinkErr   error
	publishErr     error
	permalink      string
}

func (f *fakeClient) PostMessageContext(_ context.Context, channelID string, options ...slackapi.MsgOption) (string, string, error) {
	f.postCalls++
	f.postChannel = channelID
	f.postOptions = options
	if f.postErr != nil {
		return "", "", f.postErr
	}
	return channelID, "1743630000.123456", nil
}

func (f *fakeClient) UpdateMessageContext(_ context.Context, channelID, timestamp string, options ...slackapi.MsgOption) (string, string, string, error) {
	f.updateCalls++
	f.updateChannel = channelID
	f.updateTS = timestamp
	f.updateOptions = options
	if f.updateErr != nil {
		return "", "", "", f.updateErr
	}
	return channelID, timestamp, "", nil
}

func (f *fakeClient) GetPermalinkContext(_ context.Context, params *slackapi.PermalinkParameters) (string, error) {
	f.permalinkCalls++
	if f.permalinkErr != nil {
		return "", f.permalinkErr
	}
	if f.permalink != "" {
		return f.permalink, nil
	}
	return "https://example.slack.com/archives/" + params.Channel + "/p1743630000123456", nil
}

func (f *fakeClient) PublishViewContext(_ context.Context, req slackapi.PublishViewContextRequest) (*slackapi.ViewResponse, error) {
	f.publishCalls++
	f.publishReq = req
	if f.publishErr != nil {
		return nil, f.publishErr
	}
	return &slackapi.ViewResponse{}, nil
}

func TestSyncIssuePostsNewMessage(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	notifier := newWithClient("C12345678", client)

	state, err := notifier.SyncIssue(context.Background(), notify.IssueSummary{
		Identifier:  "COLIN-153",
		Title:       "Slack support",
		State:       "Review",
		NextAction:  "Review the PR.",
		LinearURL:   "https://linear.example.test/COLIN-153",
		Fingerprint: "fp-1",
	}, notify.IssueNotificationState{})
	if err != nil {
		t.Fatalf("SyncIssue() error = %v", err)
	}
	if client.postCalls != 1 {
		t.Fatalf("postCalls = %d, want 1", client.postCalls)
	}
	if client.updateCalls != 0 {
		t.Fatalf("updateCalls = %d, want 0", client.updateCalls)
	}
	if state.ChannelID != "C12345678" || state.MessageTS != "1743630000.123456" || state.Fingerprint != "fp-1" {
		t.Fatalf("state = %#v, want persisted Slack reference", state)
	}
}

func TestSyncIssueSkipsUnchangedFingerprint(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	notifier := newWithClient("C12345678", client)
	existing := notify.IssueNotificationState{
		ChannelID:   "C12345678",
		MessageTS:   "1743630000.123456",
		Permalink:   "https://example.slack.com/archives/C12345678/p1743630000123456",
		Fingerprint: "fp-1",
	}

	state, err := notifier.SyncIssue(context.Background(), notify.IssueSummary{
		Identifier:  "COLIN-153",
		Title:       "Slack support",
		State:       "Review",
		NextAction:  "Review the PR.",
		Fingerprint: "fp-1",
	}, existing)
	if err != nil {
		t.Fatalf("SyncIssue() error = %v", err)
	}
	if !reflect.DeepEqual(state, existing) {
		t.Fatalf("state = %#v, want existing %#v", state, existing)
	}
	if client.postCalls != 0 || client.updateCalls != 0 {
		t.Fatalf("postCalls=%d updateCalls=%d, want 0", client.postCalls, client.updateCalls)
	}
}

func TestSyncIssueUpdatesExistingMessage(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	notifier := newWithClient("C12345678", client)

	state, err := notifier.SyncIssue(context.Background(), notify.IssueSummary{
		Identifier:  "COLIN-153",
		Title:       "Slack support",
		State:       "Merge",
		NextAction:  "Colin is handling merge automation.",
		Fingerprint: "fp-2",
	}, notify.IssueNotificationState{
		ChannelID:   "C12345678",
		MessageTS:   "1743630000.123456",
		Fingerprint: "fp-1",
	})
	if err != nil {
		t.Fatalf("SyncIssue() error = %v", err)
	}
	if client.updateCalls != 1 {
		t.Fatalf("updateCalls = %d, want 1", client.updateCalls)
	}
	if client.postCalls != 0 {
		t.Fatalf("postCalls = %d, want 0", client.postCalls)
	}
	if state.Fingerprint != "fp-2" {
		t.Fatalf("state.Fingerprint = %q, want fp-2", state.Fingerprint)
	}
}

func TestSyncIssueUsesConfiguredChannelForExistingMessage(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		updateErr: slackapi.SlackErrorResponse{Err: "message_not_found"},
	}
	notifier := newWithClient("C12345678", client)

	state, err := notifier.SyncIssue(context.Background(), notify.IssueSummary{
		Identifier:  "COLIN-153",
		Title:       "Slack support",
		State:       "Merge",
		NextAction:  "Colin is handling merge automation.",
		Fingerprint: "fp-2",
	}, notify.IssueNotificationState{
		ChannelID:   "C87654321",
		MessageTS:   "1743630000.123456",
		Fingerprint: "fp-1",
	})
	if err != nil {
		t.Fatalf("SyncIssue() error = %v", err)
	}
	if client.updateChannel != "C12345678" {
		t.Fatalf("updateChannel = %q, want configured channel", client.updateChannel)
	}
	if client.postChannel != "C12345678" {
		t.Fatalf("postChannel = %q, want configured channel", client.postChannel)
	}
	if state.ChannelID != "C12345678" {
		t.Fatalf("state.ChannelID = %q, want configured channel", state.ChannelID)
	}
}

func TestSyncIssueRepostsWhenMessageWasDeleted(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		updateErr: slackapi.SlackErrorResponse{Err: "message_not_found"},
	}
	notifier := newWithClient("C12345678", client)

	_, err := notifier.SyncIssue(context.Background(), notify.IssueSummary{
		Identifier:  "COLIN-153",
		Title:       "Slack support",
		State:       "Done",
		NextAction:  "The workflow is complete unless the issue is reopened.",
		Fingerprint: "fp-3",
	}, notify.IssueNotificationState{
		ChannelID:   "C12345678",
		MessageTS:   "1743630000.123456",
		Fingerprint: "fp-2",
	})
	if err != nil {
		t.Fatalf("SyncIssue() error = %v", err)
	}
	if client.updateCalls != 1 {
		t.Fatalf("updateCalls = %d, want 1", client.updateCalls)
	}
	if client.postCalls != 1 {
		t.Fatalf("postCalls = %d, want 1", client.postCalls)
	}
}

func TestSyncIssuePersistsReferenceWhenPermalinkLookupFails(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		permalinkErr: errors.New("missing_scope"),
	}
	notifier := newWithClient("C12345678", client)

	state, err := notifier.SyncIssue(context.Background(), notify.IssueSummary{
		Identifier:  "COLIN-153",
		Title:       "Slack support",
		State:       "Review",
		NextAction:  "Review the PR.",
		Fingerprint: "fp-5",
	}, notify.IssueNotificationState{})
	if err != nil {
		t.Fatalf("SyncIssue() error = %v, want nil when permalink lookup fails", err)
	}
	if state.ChannelID != "C12345678" || state.MessageTS != "1743630000.123456" {
		t.Fatalf("state = %#v, want persisted channel and timestamp", state)
	}
	if state.Permalink != "" {
		t.Fatalf("state.Permalink = %q, want empty permalink on lookup failure", state.Permalink)
	}
}

func TestSyncIssueKeepsExistingPermalinkWhenLookupFailsForSameMessage(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		permalinkErr: errors.New("missing_scope"),
	}
	notifier := newWithClient("C12345678", client)

	state, err := notifier.SyncIssue(context.Background(), notify.IssueSummary{
		Identifier:  "COLIN-153",
		Title:       "Slack support",
		State:       "Merge",
		NextAction:  "Colin is handling merge automation.",
		Fingerprint: "fp-6",
	}, notify.IssueNotificationState{
		ChannelID:   "C12345678",
		MessageTS:   "1743630000.123456",
		Permalink:   "https://example.slack.com/archives/C12345678/p1743630000123456",
		Fingerprint: "fp-5",
	})
	if err != nil {
		t.Fatalf("SyncIssue() error = %v, want nil when permalink lookup fails", err)
	}
	if state.Permalink != "https://example.slack.com/archives/C12345678/p1743630000123456" {
		t.Fatalf("state.Permalink = %q, want existing permalink preserved", state.Permalink)
	}
}

func TestSyncIssueReturnsUpdateError(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		updateErr: errors.New("slack unavailable"),
	}
	notifier := newWithClient("C12345678", client)

	_, err := notifier.SyncIssue(context.Background(), notify.IssueSummary{
		Identifier:  "COLIN-153",
		Title:       "Slack support",
		State:       "Review",
		NextAction:  "Review the PR.",
		Fingerprint: "fp-4",
	}, notify.IssueNotificationState{
		ChannelID:   "C12345678",
		MessageTS:   "1743630000.123456",
		Fingerprint: "fp-3",
	})
	if err == nil {
		t.Fatal("SyncIssue() error = nil, want update error")
	}
	if client.postCalls != 0 {
		t.Fatalf("postCalls = %d, want 0", client.postCalls)
	}
}
