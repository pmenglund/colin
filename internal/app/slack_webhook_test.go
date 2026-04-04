package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestObservabilityServerSlackWebhookVerifiesSignatureWhenConfigured(t *testing.T) {
	t.Parallel()

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, func(context.Context) string {
		return "secret"
	}, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL+"/webhooks/slack", "application/json", strings.NewReader(`{"type":"url_verification","challenge":"abc123"}`))
	if err != nil {
		t.Fatalf("POST /webhooks/slack error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestObservabilityServerSlackWebhookIsNotAvailableWithoutSigningSecret(t *testing.T) {
	t.Parallel()

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL+"/webhooks/slack", "application/json", strings.NewReader(`{"type":"url_verification","challenge":"abc123"}`))
	if err != nil {
		t.Fatalf("POST /webhooks/slack error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestObservabilityServerSlackWebhookAcceptsValidURLVerification(t *testing.T) {
	t.Parallel()

	const secret = "secret"
	payload := `{"type":"url_verification","challenge":"abc123"}`
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, func(context.Context) string {
		return secret
	}, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/slack", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackTestSignature(secret, ts, payload))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/slack error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"challenge":"abc123"`) {
		t.Fatalf("body = %s, want challenge response", string(body))
	}
}

func TestObservabilityServerSlackWebhookPublishesHomeViewForAppHomeOpened(t *testing.T) {
	t.Parallel()

	const secret = "secret"
	payload := `{"type":"event_callback","event":{"type":"app_home_opened","user":"U12345678"}}`
	var events []SlackWebhookEvent
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, func(_ context.Context, event SlackWebhookEvent) error {
		events = append(events, event)
		return nil
	}, func(context.Context) string {
		return secret
	}, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/slack", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackTestSignature(secret, ts, payload))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/slack error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(events) != 1 {
		t.Fatalf("publisher calls = %d, want 1", len(events))
	}
	if events[0].UserID != "U12345678" {
		t.Fatalf("UserID = %q, want %q", events[0].UserID, "U12345678")
	}
	if events[0].Event != "app_home_opened" {
		t.Fatalf("Event = %q, want %q", events[0].Event, "app_home_opened")
	}
}

func TestObservabilityServerSlackWebhookIgnoresIrrelevantEvents(t *testing.T) {
	t.Parallel()

	const secret = "secret"
	payload := `{"type":"event_callback","event":{"type":"message","user":"U12345678"}}`
	calls := 0
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, func(_ context.Context, event SlackWebhookEvent) error {
		calls++
		return nil
	}, func(context.Context) string {
		return secret
	}, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/slack", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackTestSignature(secret, ts, payload))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/slack error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if calls != 0 {
		t.Fatalf("publisher calls = %d, want 0", calls)
	}
}

func slackTestSignature(secret string, timestamp string, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
	_, _ = mac.Write([]byte(payload))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}
