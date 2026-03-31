package app

import (
	"context"
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

	handler, err := NewObservabilityServer(func(context.Context) (domain.Snapshot, error) {
		now := time.Date(2026, 3, 28, 12, 34, 56, 0, time.UTC)
		return domain.Snapshot{
			GeneratedAt: now,
			Counts:      map[string]int{"running": 1, "retrying": 0},
			IssueStates: map[string]int{"Todo": 5, "In Progress": 1, "Review": 2},
			PausedIssueStates: map[string]domain.PausedStateSummary{
				"Review": {
					Count: 1,
					URL:   "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22",
				},
			},
			Running: []domain.SnapshotRunning{{
				IssueID:      "issue-1",
				Identifier:   "COLIN-93",
				Title:        "Add dashboard",
				State:        "In Progress",
				SessionID:    "session-1",
				TurnCount:    4,
				LastEvent:    "turn_completed",
				LastMessage:  "refresh complete",
				StartedAt:    now.Add(-time.Minute),
				LastEventAt:  ptr(now.Add(-2 * time.Second)),
				InputTokens:  11,
				OutputTokens: 12,
				TotalTokens:  23,
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
			GeneratedAt:      now,
			Ready:            true,
			LocalBaseURL:     "http://127.0.0.1:8888",
			PublicBaseURL:    "https://colin.tail.example.ts.net",
			SuggestedCommand: "tailscale funnel --bg --https=443 --set-path=/webhooks 8888",
			LinearWebhookURL: "https://colin.tail.example.ts.net/webhooks/linear",
			GitHubWebhookURL: "https://colin.tail.example.ts.net/webhooks/github",
			Checks: []domain.SetupCheck{
				{
					ID:        "tailscale_local_api",
					Label:     "Colin can reach the local Tailscale daemon",
					Status:    "ok",
					Detail:    "Connected to the local Tailscale daemon.",
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
	}, nil, nil, nil)
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
		if !strings.Contains(text, `data-testid="shell-instance"`) {
			t.Fatalf("missing shell marker: %s", text)
		}
		if !strings.Contains(text, `data-testid="worker-card-COLIN-93"`) {
			t.Fatalf("missing worker card: %s", text)
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
		if got := snapshot.IssueStates["Review"]; got != 2 {
			t.Fatalf("review count = %d, want 2", got)
		}
		if got := snapshot.PausedIssueStates["Review"].Count; got != 1 {
			t.Fatalf("review paused count = %d, want 1", got)
		}
		if got := snapshot.PausedIssueStates["Review"].URL; got != "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22" {
			t.Fatalf("review paused url = %q", got)
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
		if status.GitHubWebhookURL != "https://colin.tail.example.ts.net/webhooks/github" {
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
			`Ready for webhooks`,
			`https://colin.tail.example.ts.net/webhooks/github`,
			`tailscale funnel --bg --https=443 --set-path=/webhooks 8888`,
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

	t.Run("reserved github webhook endpoints", func(t *testing.T) {
		for _, path := range []string{"/webhooks/github", "/github"} {
			resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("POST %s error = %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotImplemented {
				t.Fatalf("%s status = %d, want 501", path, resp.StatusCode)
			}
		}
	})
}

func TestObservabilityServerLogRouteDefaultsToEmptyWhenProviderNil(t *testing.T) {
	t.Parallel()

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, nil)
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

	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, func(context.Context) string {
		return "secret"
	}, nil)
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
			handler, err := NewObservabilityServer(nil, nil, nil, nil, func(_ context.Context, event LinearWebhookEvent) LinearWebhookTriggerResult {
				events = append(events, event)
				return LinearWebhookTriggerResult{Relevant: true, Queued: true}
			}, nil, nil)
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
	handler, err := NewObservabilityServer(nil, nil, nil, nil, func(_ context.Context, event LinearWebhookEvent) LinearWebhookTriggerResult {
		triggerCalls++
		return LinearWebhookTriggerResult{Relevant: true, Queued: true}
	}, nil, nil)
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

	handler, err := NewObservabilityServer(nil, nil, nil, nil, func(_ context.Context, event LinearWebhookEvent) LinearWebhookTriggerResult {
		return LinearWebhookTriggerResult{Relevant: true, Queued: true, Coalesced: true}
	}, nil, nil)
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
	handler, err := NewObservabilityServer(nil, nil, nil, nil, nil, nil, logger)
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
