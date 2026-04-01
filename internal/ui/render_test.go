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
		IssueStates: map[string]int{"Backlog": 2, "Todo": 4, "In Progress": 1, "Refine": 0, "Review": 3, "Merge": 1, "Done": 2},
		PausedIssueStates: map[string]domain.PausedStateSummary{
			"Review": {
				Count: 2,
				URL:   "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22",
			},
		},
		RateLimits: domain.RateLimitSnapshot{
			"primary": {
				ResetsAt:              ptr(time.Date(2026, 3, 28, 17, 32, 0, 0, time.UTC)),
				UsedPercent:           int64Ptr(5),
				WindowDurationMinutes: int64Ptr(300),
			},
			"secondary": {
				ResetsAt:              ptr(time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)),
				UsedPercent:           int64Ptr(9),
				WindowDurationMinutes: int64Ptr(10080),
			},
			"linear_requests": {
				Limit:         int64Ptr(100),
				Remaining:     int64Ptr(25),
				ResetsAt:      ptr(time.Date(2026, 3, 28, 12, 5, 0, 0, time.UTC)),
				NextAllowedAt: ptr(time.Date(2026, 3, 28, 12, 0, 3, 0, time.UTC)),
			},
		},
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
			OutputLog: []domain.OutputLog{
				{Timestamp: time.Date(2026, 3, 28, 11, 58, 1, 0, time.UTC), Event: "session_started", Message: "session started"},
				{Timestamp: time.Date(2026, 3, 28, 11, 59, 2, 0, time.UTC), Event: "turn_completed", Message: "Still working"},
			},
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
		`data-testid="refresh-status"`,
		`data-refresh-status="live"`,
		`data-generated-at="2026-03-28T12:00:00Z"`,
		`title="Last successful update at 2026-03-28T12:00:00Z"`,
		`Live data`,
		`data-refresh-toggle="true"`,
		`❚❚`,
		`<h1>Colin</h1>`,
		`Colin is a Go service that watches a Linear project, runs Codex in per-issue workspaces, and hands off review-ready changes.`,
		`href="https://github.com/pmenglund/colin"`,
		`View the GitHub repository`,
		`data-testid="linear-state-counts"`,
		`data-testid="worker-card-COLIN-93"`,
		`data-testid="worker-output-COLIN-93"`,
		`hx-get="/"`,
		`/api/v1/state`,
		`/setup/funnel`,
		`COLIN-93`,
		`Add live dashboard`,
		`Linear issues`,
		`Backlog`,
		`Issue is parked outside the active handoff states.`,
		`Todo`,
		`Issue is ready for Colin to pick up.`,
		`In Progress`,
		`Issue is actively being worked.`,
		`Refine`,
		`Issue needs human clarification before a PR can be reviewed.`,
		`Review`,
		`Issue has a PR and is awaiting human review.`,
		`data-testid="paused-issues-review"`,
		`href="https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22"`,
		`2 paused`,
		`Merge`,
		`Issue is approved and waiting to be merged.`,
		`data-testid="rate-limits-codex"`,
		`data-testid="rate-limits-linear"`,
		`Codex`,
		`Linear`,
		`5% used of 5h window which resets in 5h32m`,
		`9% used of 1w window which resets in 1w`,
		`resets in 5m, 25 of 100 remaining next request in 3s`,
		`data-local-time="true"`,
		`data-timestamp="2026-03-28T11:58:01Z"`,
		`11:58:01 UTC`,
		`session_started`,
		`data-timestamp="2026-03-28T11:59:02Z"`,
		`11:59:02 UTC`,
		`turn_completed`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("render missing %q\n%s", want, html)
		}
	}
	if strings.Contains(html, "Tracked Issues") {
		t.Fatalf("render should not include tracked issues summary card\n%s", html)
	}

	if strings.Index(html, "Running tasks") > strings.Index(html, "API snapshot") {
		t.Fatalf("API snapshot should render after running tasks:\n%s", html)
	}
}

func TestPausedIndicatorRendersWithoutLinkWhenURLMissing(t *testing.T) {
	t.Parallel()

	html := renderNode(t, stateCountCard("Review", 3, domain.PausedStateSummary{Count: 1}))
	if !strings.Contains(html, `data-testid="paused-issues-review"`) {
		t.Fatalf("paused indicator missing test id\n%s", html)
	}
	if !strings.Contains(html, `>1 paused</span>`) {
		t.Fatalf("paused indicator missing label\n%s", html)
	}
	if strings.Contains(html, `<a `) {
		t.Fatalf("paused indicator should not render a link without URL\n%s", html)
	}
}

func TestWorkerOutputRendersOnePrePerMessage(t *testing.T) {
	t.Parallel()

	html := renderNode(t, workerOutput(domain.SnapshotRunning{
		Identifier: "COLIN-93",
		OutputLog: []domain.OutputLog{
			{Timestamp: time.Date(2026, 3, 28, 11, 58, 1, 0, time.UTC), Event: "session_started", Message: "session started"},
			{Timestamp: time.Date(2026, 3, 28, 11, 59, 2, 0, time.UTC), Event: "turn_completed", Message: "Still working"},
		},
	}))

	if got := strings.Count(html, `class="worker-output-entry"`); got != 2 {
		t.Fatalf("worker output entry count = %d, want 2\n%s", got, html)
	}
	if got := strings.Count(html, `<pre class="mockup-code">`); got != 1 {
		t.Fatalf("worker output pre count = %d, want 1\n%s", got, html)
	}
	firstMeta := strings.Index(html, `11:59:02 UTC`)
	firstPre := strings.Index(html, `<pre class="mockup-code">Still working</pre>`)
	if firstMeta == -1 || firstPre == -1 || firstMeta > firstPre {
		t.Fatalf("expected first timestamp before first pre\n%s", html)
	}
	secondMeta := strings.Index(html, `11:58:01 UTC`)
	if secondMeta == -1 {
		t.Fatalf("expected second timestamp to render\n%s", html)
	}
	if firstMeta > secondMeta {
		t.Fatalf("expected newest output entry first\n%s", html)
	}
	if strings.Contains(html, `<pre class="mockup-code">session started</pre>`) {
		t.Fatalf("session_started should not render redundant pre block\n%s", html)
	}
	if !strings.Contains(html, `class="badge badge-session"`) {
		t.Fatalf("session_started should use session badge styling\n%s", html)
	}
}

func TestWorkerOutputSkipsRedundantTurnCompletedMessageBody(t *testing.T) {
	t.Parallel()

	html := renderNode(t, workerOutput(domain.SnapshotRunning{
		Identifier: "COLIN-93",
		OutputLog: []domain.OutputLog{
			{Timestamp: time.Date(2026, 3, 28, 11, 59, 2, 0, time.UTC), Event: "turn_completed", Message: "turn_completed"},
		},
	}))

	if strings.Contains(html, `<pre class="mockup-code">turn_completed</pre>`) {
		t.Fatalf("turn_completed should not render redundant pre block\n%s", html)
	}
	if !strings.Contains(html, `class="badge badge-turn-completed"`) {
		t.Fatalf("turn_completed should use completed badge styling\n%s", html)
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
	if !strings.Contains(html, `data-testid="refresh-status"`) {
		t.Fatalf("fragment missing refresh status badge:\n%s", html)
	}
	if !strings.Contains(html, `data-refresh-status="live"`) {
		t.Fatalf("fragment missing live refresh status:\n%s", html)
	}
}

func TestIssueMetadataPageRendersIssueAndOutput(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.app/example/issue/COLIN-111"
	updatedAt := time.Date(2026, 3, 29, 18, 4, 0, 0, time.UTC)
	html := renderNode(t, IssueMetadataPage(domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-111",
		Title:      "Update metadata link",
		State:      "Review",
		URL:        &issueURL,
		ColinMetadata: &domain.ColinMetadata{
			ExecPlanDecision:     domain.ExecPlanDecisionOneShot,
			LastRunType:          "coding",
			LastOutcome:          "ready_for_review",
			LastSummaryCommentID: "comment-12",
			UpdatedAt:            &updatedAt,
			CodexOutput: []domain.OutputLog{
				{Timestamp: time.Date(2026, 3, 29, 18, 3, 0, 0, time.UTC), Event: "turn_completed", Message: "Updated the metadata link."},
			},
		},
	}, updatedAt))

	for _, want := range []string{
		`data-testid="issue-metadata-panel"`,
		`data-testid="issue-metadata-output"`,
		`COLIN-111 - Update metadata link`,
		`ExecPlan decision`,
		`one_shot`,
		`Last run type`,
		`ready_for_review`,
		`comment-12`,
		`href="/linear/issues/issue-1/metadata"`,
		`href="/linear/issues/issue-1/exec-plan"`,
		`href="https://linear.app/example/issue/COLIN-111"`,
		`Updated the metadata link.`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("render missing %q\n%s", want, html)
		}
	}
}

func TestExecPlanPageRendersStoredPlan(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.app/example/issue/COLIN-135"
	updatedAt := time.Date(2026, 3, 30, 10, 15, 0, 0, time.UTC)
	html := renderNode(t, ExecPlanPage(domain.Issue{
		ID:         "issue-135",
		Identifier: "COLIN-135",
		Title:      "ExecPlan attachment",
		State:      "In Progress",
		URL:        &issueURL,
		ExecPlan: &domain.ExecPlan{
			AttachmentID: "attachment-99",
			Body:         "# Plan\n\nInspect the stored exec plan body.",
			UpdatedAt:    &updatedAt,
		},
	}, updatedAt))

	for _, want := range []string{
		`data-testid="issue-exec-plan-panel"`,
		`data-testid="issue-exec-plan-body"`,
		`COLIN-135 - ExecPlan attachment`,
		`attachment-99`,
		`# Plan`,
		`Inspect the stored exec plan body.`,
		`href="/linear/issues/issue-135/metadata"`,
		`href="/linear/issues/issue-135/exec-plan"`,
		`href="https://linear.app/example/issue/COLIN-135"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("render missing %q\n%s", want, html)
		}
	}
}

func TestFunnelSetupPageRendersChecksAndURLs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 30, 19, 0, 0, 0, time.UTC)
	html := renderNode(t, FunnelSetupPage(domain.FunnelSetupStatus{
		GeneratedAt:           now,
		Ready:                 true,
		LocalBaseURL:          "http://127.0.0.1:8888",
		LocalWebhookBaseURL:   "http://127.0.0.1:8998",
		TailnetUIBaseURL:      "https://colin.tail.example.ts.net",
		PublicBaseURL:         "https://colin.tail.example.ts.net:8443",
		LinearWebhookURL:      "https://colin.tail.example.ts.net:8443/webhooks/linear",
		GitHubWebhookURL:      "https://colin.tail.example.ts.net:8443/webhooks/github",
		SuggestedServeCommand: "tailscale serve --bg 8888",
		SuggestedCommand:      "tailscale funnel --bg --https=8443 --set-path=/webhooks 8998",
		Checks: []domain.SetupCheck{
			{
				ID:        "tailscale_local_api",
				Label:     "Colin can reach the local Tailscale daemon",
				Status:    "ok",
				Detail:    "Connected to the local Tailscale daemon.",
				CheckedAt: now,
			},
		},
	}, now))

	for _, want := range []string{
		`data-testid="funnel-urls"`,
		`data-testid="funnel-checks"`,
		`Tailscale ready`,
		`Tailnet UI base URL`,
		`Local webhook base URL`,
		`https://colin.tail.example.ts.net:8443/webhooks/github`,
		`tailscale serve --bg 8888`,
		`tailscale funnel --bg --https=8443 --set-path=/webhooks 8998`,
		`data-testid="funnel-check-tailscale_local_api"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("render missing %q\n%s", want, html)
		}
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

func int64Ptr(value int64) *int64 {
	return &value
}
