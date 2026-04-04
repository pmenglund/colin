package app

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func TestObservabilityServerRoutes(t *testing.T) {
	t.Parallel()

	logEntries := []domain.BufferedLogEntry{
		{
			Timestamp: time.Date(2026, 3, 28, 12, 34, 50, 0, time.UTC),
			Level:     slog.LevelDebug.String(),
			Message:   "poll tick started",
			Fields:    []string{"running=1"},
		},
		{
			Timestamp: time.Date(2026, 3, 28, 12, 34, 51, 0, time.UTC),
			Level:     slog.LevelInfo.String(),
			Message:   "service starting",
			Fields:    []string{"workflow_path=/tmp/WORKFLOW.md"},
		},
		{
			Timestamp: time.Date(2026, 3, 28, 12, 34, 52, 0, time.UTC),
			Level:     slog.LevelError.String(),
			Message:   "candidate fetch failed",
			Fields:    []string{"error=boom"},
		},
	}
	streamUpdates := make(chan domain.SnapshotUpdate, 1)

	handler, err := NewObservabilityServer(func(context.Context) (domain.Snapshot, error) {
		now := time.Date(2026, 3, 28, 12, 34, 56, 0, time.UTC)
		return domain.Snapshot{
			GeneratedAt:       now,
			ShutdownRequested: true,
			Counts:            map[string]int{"running": 1, "retrying": 0},
			IssueStates:       map[string]int{"Todo": 5, "In Progress": 1, "Review": 1},
			StateIssues: map[string][]domain.StateIssueSummary{
				"In Progress": {
					{
						ID:         "issue-1",
						Identifier: "COLIN-93",
						Title:      "Add dashboard",
						URL:        "https://linear.app/example/issue/COLIN-93",
					},
				},
				"Review": {
					{
						ID:         "issue-2",
						Identifier: "COLIN-94",
						Title:      "Sync review labels",
						URL:        "https://linear.app/example/issue/COLIN-94",
					},
				},
			},
			PausedIssueStates: map[string]domain.PausedStateSummary{
				"Review": {
					Count: 1,
					URL:   "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22",
				},
			},
			Running: []domain.SnapshotRunning{{
				IssueID:       "issue-1",
				Identifier:    "COLIN-93",
				Title:         "Add dashboard",
				State:         "In Progress",
				SessionID:     "session-1",
				TurnCount:     4,
				LastEvent:     "turn_completed",
				LastMessage:   "refresh complete",
				StartedAt:     now.Add(-time.Minute),
				LastEventAt:   ptr(now.Add(-2 * time.Second)),
				InputTokens:   11,
				OutputTokens:  12,
				TotalTokens:   23,
				ContextWindow: &domain.ContextWindowUsage{UsedTokens: 78400, LimitTokens: 258000},
				OutputLog: []domain.OutputLog{{
					Timestamp: now.Add(-2 * time.Second),
					Event:     "turn_completed",
					Message:   "refresh complete",
				}},
			}},
		}, nil
	}, func(context.Context, string) (domain.Issue, error) {
		execPlanUpdatedAt := time.Date(2026, 3, 28, 12, 34, 54, 0, time.UTC)
		return domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-93",
			Title:      "Add dashboard",
			State:      "In Progress",
			ColinMetadata: &domain.ColinMetadata{
				ExecPlanDecision: domain.ExecPlanDecisionOneShot,
				LastRunType:      "coding",
				LastOutcome:      "ready_for_review",
				CodexOutput:      nil,
				UpdatedAt:        ptr(time.Date(2026, 3, 28, 12, 34, 55, 0, time.UTC)),
			},
			ExecPlan: &domain.ExecPlan{
				AttachmentID: "attachment-1",
				Body:         "# Fake ExecPlan\n\nPlan details.",
				UpdatedAt:    &execPlanUpdatedAt,
			},
		}, nil
	}, func(context.Context) (domain.FunnelSetupStatus, error) {
		now := time.Date(2026, 3, 28, 12, 34, 56, 0, time.UTC)
		return domain.FunnelSetupStatus{
			GeneratedAt:           now,
			Ready:                 true,
			LocalBaseURL:          "http://127.0.0.1:8888",
			LocalWebhookBaseURL:   "http://127.0.0.1:8998",
			TailnetUIBaseURL:      "https://colin.tail.example.ts.net",
			PublicBaseURL:         "https://colin.tail.example.ts.net:8443",
			SuggestedServeCommand: "tailscale serve --bg 8888",
			SuggestedCommand:      "tailscale funnel --bg --https=8443 --set-path=/webhooks 8998",
			LinearWebhookURL:      "https://colin.tail.example.ts.net:8443/webhooks/linear",
			GitHubWebhookURL:      "https://colin.tail.example.ts.net:8443/webhooks/github",
			Checks: []domain.SetupCheck{
				{
					ID:        "tailscale_local_api",
					Label:     "Colin can reach the local Tailscale daemon",
					Status:    "ok",
					Detail:    "Connected to the local Tailscale daemon.",
					CheckedAt: now,
				},
				{
					ID:        "serve_route",
					Label:     "Serve proxies Colin at `/` on the tailnet",
					Status:    "ok",
					Detail:    "Detected `https://colin.tail.example.ts.net` proxying Colin from `/`.",
					CheckedAt: now,
				},
			},
		}, nil
	}, func(_ context.Context, minLevel *slog.Level) (domain.BufferedLogSnapshot, error) {
		filtered := make([]domain.BufferedLogEntry, 0, len(logEntries))
		for _, entry := range logEntries {
			if minLevel != nil && levelFromString(entry.Level) < *minLevel {
				continue
			}
			filtered = append(filtered, entry)
		}
		return domain.BufferedLogSnapshot{
			Capacity: len(logEntries),
			Count:    len(filtered),
			Entries:  filtered,
		}, nil
	}, func(context.Context) (domain.SnapshotUpdate, <-chan domain.SnapshotUpdate, error) {
		return domain.SnapshotUpdate{
			Sequence:    7,
			GeneratedAt: time.Date(2026, 3, 28, 12, 34, 56, 0, time.UTC),
		}, streamUpdates, nil
	}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	t.Run("full page", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/")
		if err != nil {
			t.Fatalf("GET / error = %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		text := string(body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if !strings.Contains(text, "<html") {
			t.Fatalf("expected full document, got %s", text)
		}
		if strings.Contains(text, `data-testid="shell-instance"`) {
			t.Fatalf("unexpected shell renderer card: %s", text)
		}
		if !strings.Contains(text, `data-testid="worker-card-COLIN-93"`) {
			t.Fatalf("missing worker card: %s", text)
		}
		if !strings.Contains(text, `data-testid="context-window-COLIN-93"`) {
			t.Fatalf("missing context window usage: %s", text)
		}
		if !strings.Contains(text, `Context window: 70% left (78.4K used / 258K)`) {
			t.Fatalf("missing context window copy: %s", text)
		}
		if !strings.Contains(text, `data-testid="state-issues-trigger-review"`) {
			t.Fatalf("missing state issue trigger: %s", text)
		}
		if !strings.Contains(text, `href="https://linear.app/example/issue/COLIN-94"`) {
			t.Fatalf("missing state issue linear link: %s", text)
		}
		if !strings.Contains(text, `href="/linear/issues/issue-2/metadata"`) {
			t.Fatalf("missing state issue detail link: %s", text)
		}
		if !strings.Contains(text, `data-testid="paused-issues-review"`) {
			t.Fatalf("missing paused issue indicator: %s", text)
		}
		if !strings.Contains(text, `data-testid="refresh-status"`) {
			t.Fatalf("missing refresh status indicator: %s", text)
		}
		if !strings.Contains(text, `data-refresh-status="live"`) {
			t.Fatalf("missing live refresh status: %s", text)
		}
		if !strings.Contains(text, `data-testid="shutdown-alert"`) {
			t.Fatalf("missing shutdown alert: %s", text)
		}
	})

	t.Run("fragment", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/", nil)
		if err != nil {
			t.Fatalf("NewRequest() error = %v", err)
		}
		req.Header.Set("HX-Request", "true")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET / fragment error = %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		text := string(body)
		if strings.Contains(text, "<html") {
			t.Fatalf("expected fragment, got %s", text)
		}
		if !strings.Contains(text, `id="dashboard-root"`) {
			t.Fatalf("missing dashboard root: %s", text)
		}
		if !strings.Contains(text, `data-testid="refresh-status"`) {
			t.Fatalf("missing refresh status indicator: %s", text)
		}
		if !strings.Contains(text, `data-testid="shutdown-alert"`) {
			t.Fatalf("missing shutdown alert: %s", text)
		}
	})

	t.Run("api", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/v1/state")
		if err != nil {
			t.Fatalf("GET /api/v1/state error = %v", err)
		}
		defer resp.Body.Close()
		var snapshot domain.Snapshot
		if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if got := snapshot.Counts["running"]; got != 1 {
			t.Fatalf("running count = %d, want 1", got)
		}
		if got := snapshot.IssueStates["Review"]; got != 1 {
			t.Fatalf("review count = %d, want 1", got)
		}
		if got := len(snapshot.StateIssues["Review"]); got != 1 {
			t.Fatalf("review issue list length = %d, want 1", got)
		}
		if !snapshot.ShutdownRequested {
			t.Fatal("shutdown requested = false, want true")
		}
		if got := snapshot.PausedIssueStates["Review"].Count; got != 1 {
			t.Fatalf("review paused count = %d, want 1", got)
		}
		if got := snapshot.PausedIssueStates["Review"].URL; got != "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22" {
			t.Fatalf("review paused url = %q", got)
		}
		if snapshot.Running[0].ContextWindow == nil {
			t.Fatal("context window = nil, want value")
		}
		if got := snapshot.Running[0].ContextWindow.UsedTokens; got != 78400 {
			t.Fatalf("context window used = %d, want 78400", got)
		}
		if got := snapshot.Running[0].ContextWindow.LimitTokens; got != 258000 {
			t.Fatalf("context window limit = %d, want 258000", got)
		}
	})

	t.Run("events api", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/events", nil)
		if err != nil {
			t.Fatalf("NewRequest() error = %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /api/v1/events error = %v", err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
			t.Fatalf("Content-Type = %q, want text/event-stream", got)
		}

		reader := bufio.NewReader(resp.Body)
		readyEvent := readSSEEvent(t, reader)
		if !strings.Contains(readyEvent, "event: ready") {
			t.Fatalf("ready event = %q, want ready event", readyEvent)
		}
		if !strings.Contains(readyEvent, `"sequence":7`) {
			t.Fatalf("ready event = %q, want initial sequence", readyEvent)
		}

		streamUpdates <- domain.SnapshotUpdate{
			Sequence:    8,
			GeneratedAt: time.Date(2026, 3, 28, 12, 34, 57, 0, time.UTC),
		}
		snapshotEvent := readSSEEvent(t, reader)
		if !strings.Contains(snapshotEvent, "event: snapshot") {
			t.Fatalf("snapshot event = %q, want snapshot event", snapshotEvent)
		}
		if !strings.Contains(snapshotEvent, `"sequence":8`) {
			t.Fatalf("snapshot event = %q, want updated sequence", snapshotEvent)
		}
	})

	t.Run("logs api", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/v1/logs?level=info")
		if err != nil {
			t.Fatalf("GET /api/v1/logs error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var snapshot domain.BufferedLogSnapshot
		if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if snapshot.Capacity != len(logEntries) {
			t.Fatalf("Capacity = %d, want %d", snapshot.Capacity, len(logEntries))
		}
		if snapshot.Count != 2 {
			t.Fatalf("Count = %d, want 2", snapshot.Count)
		}
		if snapshot.Entries[0].Message != "service starting" || snapshot.Entries[1].Message != "candidate fetch failed" {
			t.Fatalf("Entries = %#v, want info and error entries", snapshot.Entries)
		}
	})

	t.Run("logs api head", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodHead, server.URL+"/api/v1/logs", nil)
		if err != nil {
			t.Fatalf("NewRequest() error = %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("HEAD /api/v1/logs error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if len(body) != 0 {
			t.Fatalf("body len = %d, want 0", len(body))
		}
	})

	t.Run("logs api invalid level", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/v1/logs?level=trace")
		if err != nil {
			t.Fatalf("GET /api/v1/logs invalid level error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		var payload map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !strings.Contains(payload["error"], `invalid log level "trace"`) {
			t.Fatalf("error = %q, want invalid level error", payload["error"])
		}
	})

	t.Run("setup api", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/v1/setup/funnel")
		if err != nil {
			t.Fatalf("GET /api/v1/setup/funnel error = %v", err)
		}
		defer resp.Body.Close()
		var status domain.FunnelSetupStatus
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !status.Ready {
			t.Fatal("Ready = false, want true")
		}
		if status.GitHubWebhookURL != "https://colin.tail.example.ts.net:8443/webhooks/github" {
			t.Fatalf("GitHubWebhookURL = %q", status.GitHubWebhookURL)
		}
	})

	t.Run("assets", func(t *testing.T) {
		for _, path := range []string{"/assets/app.css", "/assets/htmx.min.js"} {
			resp, err := http.Get(server.URL + path)
			if err != nil {
				t.Fatalf("GET %s error = %v", path, err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
			}
			if len(body) == 0 {
				t.Fatalf("%s returned empty body", path)
			}
		}
	})

	t.Run("issue metadata page", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/linear/issues/issue-1/metadata")
		if err != nil {
			t.Fatalf("GET metadata page error = %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		text := string(body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		for _, want := range []string{
			`data-testid="issue-metadata-panel"`,
			`data-live-refresh-mode="reload"`,
			`COLIN-93 - Add dashboard`,
			`ExecPlan decision`,
			`one_shot`,
			`Last outcome`,
			`ready_for_review`,
			`refresh complete`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("metadata page missing %q: %s", want, text)
			}
		}
	})

	t.Run("issue exec plan page", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/linear/issues/issue-1/exec-plan")
		if err != nil {
			t.Fatalf("GET exec plan page error = %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		text := string(body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		for _, want := range []string{
			`data-testid="issue-exec-plan-panel"`,
			`data-testid="issue-exec-plan-body"`,
			`data-live-refresh-mode="reload"`,
			`COLIN-93 - Add dashboard`,
			`attachment-1`,
			`# Fake ExecPlan`,
			`Plan details.`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("exec plan page missing %q: %s", want, text)
			}
		}
	})

	t.Run("funnel setup page", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/setup/funnel")
		if err != nil {
			t.Fatalf("GET /setup/funnel error = %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		text := string(body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		for _, want := range []string{
			`data-testid="funnel-urls"`,
			`Tailscale ready`,
			`https://colin.tail.example.ts.net:8443/webhooks/github`,
			`tailscale serve --bg 8888`,
			`tailscale funnel --bg --https=8443 --set-path=/webhooks 8998`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("setup page missing %q: %s", want, text)
			}
		}
	})

	t.Run("webhook readyz", func(t *testing.T) {
		for _, path := range []string{"/webhooks/readyz", "/readyz"} {
			resp, err := http.Get(server.URL + path)
			if err != nil {
				t.Fatalf("GET %s error = %v", path, err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
			}
			if !strings.Contains(string(body), `"status":"ok"`) {
				t.Fatalf("%s body = %s", path, string(body))
			}
		}
	})

	t.Run("linear webhook endpoints acknowledge posts", func(t *testing.T) {
		for _, path := range []string{"/webhooks/linear", "/linear"} {
			resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(`{"webhookTimestamp":1735689600000}`))
			if err != nil {
				t.Fatalf("POST %s error = %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
			}
		}
	})

	t.Run("github webhook endpoints acknowledge posts", func(t *testing.T) {
		for _, path := range []string{"/webhooks/github", "/github"} {
			resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("POST %s error = %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
			}
		}
	})
}

func TestSeparateUIAndWebhookHandlers(t *testing.T) {
	t.Parallel()

	uiHandler, err := NewUIHandler(func(context.Context) (domain.Snapshot, error) {
		return domain.Snapshot{GeneratedAt: time.Now().UTC(), Counts: map[string]int{}}, nil
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewUIHandler() error = %v", err)
	}
	webhookHandler := NewWebhookHandler(nil, nil, nil, nil, nil)

	uiServer := httptest.NewServer(uiHandler)
	defer uiServer.Close()
	webhookServer := httptest.NewServer(webhookHandler)
	defer webhookServer.Close()

	resp, err := http.Get(uiServer.URL + "/")
	if err != nil {
		t.Fatalf("GET ui / error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui / status = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Get(uiServer.URL + "/webhooks/readyz")
	if err != nil {
		t.Fatalf("GET ui /webhooks/readyz error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ui /webhooks/readyz status = %d, want 404", resp.StatusCode)
	}

	resp, err = http.Post(webhookServer.URL+"/webhooks/linear", "application/json", strings.NewReader(`{"webhookTimestamp":1735689600000}`))
	if err != nil {
		t.Fatalf("POST webhook /webhooks/linear error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook /webhooks/linear status = %d, want 200", resp.StatusCode)
	}

	redirectClient := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err = redirectClient.Get(webhookServer.URL + "/")
	if err != nil {
		t.Fatalf("GET webhook / error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("webhook / status = %d, want 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != projectURL {
		t.Fatalf("webhook / Location = %q, want %q", got, projectURL)
	}
}

func TestObservabilityServerLogRouteDefaultsToEmptyWhenProviderNil(t *testing.T) {
	t.Parallel()

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/logs")
	if err != nil {
		t.Fatalf("GET /api/v1/logs error = %v", err)
	}
	defer resp.Body.Close()

	var snapshot domain.BufferedLogSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if snapshot.Count != 0 {
		t.Fatalf("Count = %d, want 0", snapshot.Count)
	}
}

func TestObservabilityServerLinearWebhookVerifiesSignatureWhenConfigured(t *testing.T) {
	t.Parallel()

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, func(context.Context) string {
		return "secret"
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL+"/webhooks/linear", "application/json", strings.NewReader(`{"webhookTimestamp":1735689600000}`))
	if err != nil {
		t.Fatalf("POST /webhooks/linear error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestObservabilityServerLinearWebhookTriggersRefreshForRelevantIssueEvents(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		body          string
		action        string
		projectID     string
		changedFields []string
	}{
		{
			name:      "create",
			body:      `{"action":"create","type":"Issue","webhookTimestamp":1735689600000,"data":{"id":"issue-1","projectId":"project-1"}}`,
			action:    "create",
			projectID: "project-1",
		},
		{
			name:          "update",
			body:          `{"action":"update","type":"Issue","webhookTimestamp":1735689600000,"data":{"id":"issue-1","projectId":"project-1"},"updatedFrom":{"stateId":"old-state","updatedAt":"2026-03-31T00:00:00.000Z"}}`,
			action:        "update",
			projectID:     "project-1",
			changedFields: []string{"stateid", "updatedat"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []LinearWebhookEvent
			handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, func(_ context.Context, event LinearWebhookEvent) LinearWebhookTriggerResult {
				events = append(events, event)
				return LinearWebhookTriggerResult{Relevant: true, Queued: true}
			}, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("NewObservabilityServer() error = %v", err)
			}

			server := httptest.NewServer(handler)
			defer server.Close()

			req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/linear", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			req.Header.Set("Linear-Delivery", "delivery-1")
			req.Header.Set("Linear-Event", "Issue")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /webhooks/linear error = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			if len(events) != 1 {
				t.Fatalf("trigger calls = %d, want 1", len(events))
			}
			if events[0].DeliveryID != "delivery-1" {
				t.Fatalf("DeliveryID = %q, want %q", events[0].DeliveryID, "delivery-1")
			}
			if events[0].Action != tc.action {
				t.Fatalf("Action = %q, want %q", events[0].Action, tc.action)
			}
			if events[0].ResourceType != "Issue" {
				t.Fatalf("ResourceType = %q, want %q", events[0].ResourceType, "Issue")
			}
			if events[0].IssueID != "issue-1" {
				t.Fatalf("IssueID = %q, want %q", events[0].IssueID, "issue-1")
			}
			if events[0].ProjectID != tc.projectID {
				t.Fatalf("ProjectID = %q, want %q", events[0].ProjectID, tc.projectID)
			}
			if got, want := strings.Join(events[0].ChangedFields, ","), strings.Join(tc.changedFields, ","); got != want {
				t.Fatalf("ChangedFields = %q, want %q", got, want)
			}
		})
	}
}

func TestObservabilityServerLinearWebhookIgnoresIrrelevantEvents(t *testing.T) {
	t.Parallel()

	triggerCalls := 0
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, func(_ context.Context, event LinearWebhookEvent) LinearWebhookTriggerResult {
		triggerCalls++
		return LinearWebhookTriggerResult{Relevant: true, Queued: true}
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL+"/webhooks/linear", "application/json", strings.NewReader(`{"action":"update","type":"Comment","webhookTimestamp":1735689600000}`))
	if err != nil {
		t.Fatalf("POST /webhooks/linear error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if triggerCalls != 0 {
		t.Fatalf("triggerCalls = %d, want 0", triggerCalls)
	}
}

func TestObservabilityServerLinearWebhookAcknowledgesCoalescedRefresh(t *testing.T) {
	t.Parallel()

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, func(_ context.Context, event LinearWebhookEvent) LinearWebhookTriggerResult {
		return LinearWebhookTriggerResult{Relevant: true, Queued: true, Coalesced: true}
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL+"/webhooks/linear", "application/json", strings.NewReader(`{"action":"update","type":"Issue","webhookTimestamp":1735689600000,"data":{"id":"issue-1","projectId":"project-1"}}`))
	if err != nil {
		t.Fatalf("POST /webhooks/linear error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("body = %s, want ok status", string(body))
	}
}

func TestObservabilityServerLinearWebhookLogsRequests(t *testing.T) {
	t.Parallel()

	var output strings.Builder
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, logger)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/linear", strings.NewReader(`{"webhookTimestamp":1735689600000}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Linear-Delivery", "delivery-1")
	req.Header.Set("Linear-Event", "Issue")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/linear error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	logText := output.String()
	if !strings.Contains(logText, "received linear webhook request") {
		t.Fatalf("log output = %q, want received message", logText)
	}
	if !strings.Contains(logText, "delivery-1") {
		t.Fatalf("log output = %q, want delivery id", logText)
	}
	if !strings.Contains(logText, "\"linear_event\":\"Issue\"") {
		t.Fatalf("log output = %q, want linear event", logText)
	}
	if strings.Contains(logText, "accepted linear webhook request") {
		t.Fatalf("log output = %q, want accepted message at debug only", logText)
	}
}

func TestObservabilityServerGitHubWebhookVerifiesSignatureWhenConfigured(t *testing.T) {
	t.Parallel()

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, func(context.Context) string {
		return "secret"
	}, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/github", strings.NewReader(`{"repository":{"full_name":"acme/widgets"},"pull_request":{"number":11}}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/github error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestObservabilityServerGitHubWebhookTriggersRefreshForRelevantEvents(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		eventHeader       string
		body              string
		action            string
		repository        string
		pullRequestNumber int
	}{
		{
			name:              "pull request review",
			eventHeader:       "pull_request_review",
			body:              `{"action":"submitted","repository":{"full_name":"acme/widgets"},"pull_request":{"number":11}}`,
			action:            "submitted",
			repository:        "acme/widgets",
			pullRequestNumber: 11,
		},
		{
			name:              "reaction on pull request comment",
			eventHeader:       "reaction",
			body:              `{"action":"created","repository":{"full_name":"acme/widgets"},"comment":{"pull_request_url":"https://api.github.com/repos/acme/widgets/pulls/11"}}`,
			action:            "created",
			repository:        "acme/widgets",
			pullRequestNumber: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []GitHubWebhookEvent
			handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, func(_ context.Context, event GitHubWebhookEvent) GitHubWebhookTriggerResult {
				events = append(events, event)
				return GitHubWebhookTriggerResult{Relevant: true, Queued: true}
			}, nil, nil)
			if err != nil {
				t.Fatalf("NewObservabilityServer() error = %v", err)
			}

			server := httptest.NewServer(handler)
			defer server.Close()

			req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/github", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			req.Header.Set("X-GitHub-Delivery", "delivery-1")
			req.Header.Set("X-GitHub-Event", tc.eventHeader)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /webhooks/github error = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			if len(events) != 1 {
				t.Fatalf("trigger calls = %d, want 1", len(events))
			}
			if events[0].DeliveryID != "delivery-1" {
				t.Fatalf("DeliveryID = %q, want %q", events[0].DeliveryID, "delivery-1")
			}
			if events[0].Event != tc.eventHeader {
				t.Fatalf("Event = %q, want %q", events[0].Event, tc.eventHeader)
			}
			if events[0].Action != tc.action {
				t.Fatalf("Action = %q, want %q", events[0].Action, tc.action)
			}
			if events[0].RepositoryFullName != tc.repository {
				t.Fatalf("RepositoryFullName = %q, want %q", events[0].RepositoryFullName, tc.repository)
			}
			if events[0].PullRequestNumber != tc.pullRequestNumber {
				t.Fatalf("PullRequestNumber = %d, want %d", events[0].PullRequestNumber, tc.pullRequestNumber)
			}
			if !events[0].HasPullRequest {
				t.Fatal("HasPullRequest = false, want true")
			}
		})
	}
}

func TestObservabilityServerGitHubWebhookIgnoresIrrelevantEvents(t *testing.T) {
	t.Parallel()

	triggerCalls := 0
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, func(_ context.Context, event GitHubWebhookEvent) GitHubWebhookTriggerResult {
		triggerCalls++
		return GitHubWebhookTriggerResult{Relevant: true, Queued: true}
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/github", strings.NewReader(`{"zen":"Keep it logically awesome.","hook_id":1}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-GitHub-Event", "ping")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/github error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if triggerCalls != 0 {
		t.Fatalf("triggerCalls = %d, want 0", triggerCalls)
	}
}

func TestObservabilityServerGitHubWebhookAcknowledgesCoalescedRefresh(t *testing.T) {
	t.Parallel()

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, func(_ context.Context, event GitHubWebhookEvent) GitHubWebhookTriggerResult {
		return GitHubWebhookTriggerResult{Relevant: true, Queued: true, Coalesced: true}
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL+"/webhooks/github", "application/json", strings.NewReader(`{"action":"submitted","repository":{"full_name":"acme/widgets"},"pull_request":{"number":11}}`))
	if err != nil {
		t.Fatalf("POST /webhooks/github error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("body = %s, want ok status", string(body))
	}
}

func TestObservabilityServerGitHubWebhookLogsRequests(t *testing.T) {
	t.Parallel()

	var output strings.Builder
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, logger)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/github", strings.NewReader(`{"action":"submitted","repository":{"full_name":"acme/widgets"},"pull_request":{"number":11}}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-GitHub-Event", "pull_request_review")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/github error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	logText := output.String()
	if !strings.Contains(logText, "received github webhook request") {
		t.Fatalf("log output = %q, want received message", logText)
	}
	if !strings.Contains(logText, "delivery-1") {
		t.Fatalf("log output = %q, want delivery id", logText)
	}
	if !strings.Contains(logText, "\"github_event\":\"pull_request_review\"") {
		t.Fatalf("log output = %q, want github event", logText)
	}
}

func TestObservabilityServerGitHubWebhookAcceptsValidSignature(t *testing.T) {
	t.Parallel()

	const secret = "secret"
	payload := `{"action":"submitted","repository":{"full_name":"acme/widgets"},"pull_request":{"number":11}}`
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil, nil, func(context.Context) string {
		return secret
	}, nil)
	if err != nil {
		t.Fatalf("NewObservabilityServer() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/webhooks/github", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-GitHub-Event", "pull_request_review")
	req.Header.Set("X-Hub-Signature-256", gitHubTestSignature(secret, payload))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/github error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func gitHubTestSignature(secret string, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func readSSEEvent(t *testing.T, reader *bufio.Reader) string {
	t.Helper()

	var builder strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("ReadString() error = %v", err)
		}
		builder.WriteString(line)
		if line == "\n" {
			return builder.String()
		}
	}
}

func ptr(value time.Time) *time.Time {
	return &value
}

func levelFromString(value string) slog.Level {
	switch value {
	case slog.LevelDebug.String():
		return slog.LevelDebug
	case slog.LevelInfo.String():
		return slog.LevelInfo
	case slog.LevelWarn.String():
		return slog.LevelWarn
	case slog.LevelError.String():
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
