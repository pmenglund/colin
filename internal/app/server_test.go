package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func TestObservabilityServerRoutes(t *testing.T) {
	t.Parallel()

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
		return domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-93",
			Title:      "Add dashboard",
			State:      "In Progress",
			ColinMetadata: &domain.ColinMetadata{
				LastRunType: "coding",
				LastOutcome: "ready_for_review",
				CodexOutput: nil,
				UpdatedAt:   ptr(time.Date(2026, 3, 28, 12, 34, 55, 0, time.UTC)),
			},
		}, nil
	})
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
			`Last outcome`,
			`ready_for_review`,
			`refresh complete`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("metadata page missing %q: %s", want, text)
			}
		}
	})
}

func ptr(value time.Time) *time.Time {
	return &value
}
