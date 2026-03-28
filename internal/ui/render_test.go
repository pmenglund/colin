package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	g "maragu.dev/gomponents"
)

func TestPageRendersDashboardShell(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.app/example/issue/COLIN-93"
	snapshot := domain.Snapshot{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Counts:      map[string]int{"running": 1, "retrying": 1},
		IssueStates: map[string]int{"Todo": 4, "In Progress": 1, "Done": 2},
		Running: []domain.SnapshotRunning{{
			Identifier:   "COLIN-93",
			Title:        "Add live dashboard",
			URL:          &issueURL,
			State:        "In Progress",
			SessionID:    "session-1",
			TurnCount:    3,
			LastEvent:    "turn_completed",
			LastMessage:  "Still working",
			StartedAt:    time.Date(2026, 3, 28, 11, 50, 0, 0, time.UTC),
			LastEventAt:  ptr(time.Date(2026, 3, 28, 11, 59, 0, 0, time.UTC)),
			InputTokens:  11,
			OutputTokens: 22,
			TotalTokens:  33,
		}},
		Retrying: []domain.RetryEntry{{
			Identifier: "COLIN-91",
			Attempt:    2,
			DueAt:      time.Date(2026, 3, 28, 12, 0, 45, 0, time.UTC),
			Error:      "workspace busy",
		}},
	}

	html := renderNode(t, Page(snapshot, snapshot.GeneratedAt))
	for _, want := range []string{
		`data-testid="dashboard-root"`,
		`data-testid="refresh-button"`,
		`data-testid="linear-state-counts"`,
		`data-testid="worker-card-COLIN-93"`,
		`data-testid="worker-output-COLIN-93"`,
		`hx-get="/"`,
		`/api/v1/state`,
		`COLIN-93`,
		`Add live dashboard`,
		`Tracked Issues`,
		`In Progress`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("render missing %q\n%s", want, html)
		}
	}
}

func TestDashboardFragmentOmitsDocumentShell(t *testing.T) {
	t.Parallel()

	html := renderNode(t, Dashboard(domain.Snapshot{
		GeneratedAt: time.Now().UTC(),
		Counts:      map[string]int{},
	}))
	if strings.Contains(html, "<html") {
		t.Fatalf("fragment should not render document shell:\n%s", html)
	}
	if !strings.Contains(html, `id="dashboard-root"`) {
		t.Fatalf("fragment missing dashboard root:\n%s", html)
	}
}

func renderNode(t *testing.T, node g.Node) string {
	t.Helper()

	var builder strings.Builder
	if err := node.Render(&builder); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return builder.String()
}

func ptr(value time.Time) *time.Time {
	return &value
}
