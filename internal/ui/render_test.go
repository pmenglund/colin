package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	gothhtmx "github.com/pmenglund/goth/htmx"
	g "maragu.dev/gomponents"
)

func TestPageRendersDashboardShell(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.app/example/issue/COLIN-93"
	snapshot := domain.Snapshot{
		GeneratedAt:       time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		ShutdownRequested: true,
		Counts:            map[string]int{"running": 1, "retrying": 1},
		IssueStates:       map[string]int{"Backlog": 2, "Todo": 4, "In Progress": 1, "Refine": 0, "Review": 1, "Merge": 1, "Done": 2},
		StateIssues: map[string][]domain.StateIssueSummary{
			"In Progress": {
				{
					ID:         "issue-1",
					Identifier: "COLIN-93",
					Title:      "Add live dashboard",
					URL:        issueURL,
				},
			},
			"Review": {
				{
					ID:         "issue-2",
					Identifier: "COLIN-94",
					Title:      "Polish review labels",
					URL:        "https://linear.app/example/issue/COLIN-94",
				},
			},
		},
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
			IssueID:       "issue-1",
			Identifier:    "COLIN-93",
			Title:         "Add live dashboard",
			URL:           &issueURL,
			State:         "In Progress",
			SessionID:     "session-1",
			TurnCount:     3,
			LastEvent:     "turn_completed",
			LastMessage:   "Still working",
			StartedAt:     time.Date(2026, 3, 28, 11, 50, 0, 0, time.UTC),
			LastEventAt:   ptr(time.Date(2026, 3, 28, 11, 59, 0, 0, time.UTC)),
			InputTokens:   11,
			OutputTokens:  22,
			TotalTokens:   33,
			ContextWindow: &domain.ContextWindowUsage{UsedTokens: 78400, LimitTokens: 258000},
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
		`data-live-refresh-mode="fragment"`,
		`hx-trigger="colin:refresh"`,
		`src="` + gothhtmx.ScriptPath + `"`,
		`src="/assets/app.js"`,
		`data-testid="refresh-button"`,
		`data-testid="refresh-status"`,
		`data-testid="snapshot-age"`,
		`data-testid="shutdown-alert"`,
		`data-testid="shutdown-alert-badge"`,
		`data-refresh-status="live"`,
		`data-generated-at="2026-03-28T12:00:00Z"`,
		`title="Last successful update at 2026-03-28T12:00:00Z"`,
		`Live data`,
		`0s old`,
		`Warning`,
		`Shutdown in progress`,
		`Colin will not start new work`,
		`data-refresh-toggle="true"`,
		`❚❚`,
		`<h1>Colin</h1>`,
		`Colin is a Go service that watches a Linear project, runs Codex in per-issue workspaces, and hands off review-ready changes.`,
		`href="https://github.com/pmenglund/colin"`,
		`View the GitHub repository`,
		`data-testid="linear-state-counts"`,
		`data-testid="worker-card-COLIN-93"`,
		`data-testid="context-window-COLIN-93"`,
		`data-testid="context-window-bar-COLIN-93"`,
		`data-testid="worker-output-COLIN-93"`,
		`data-codex-output-panel="true"`,
		`data-codex-output-issue-id="issue-1"`,
		`data-codex-output-load-url="/api/v1/issues/issue-1/codex-output"`,
		`data-codex-output-events-url="/api/v1/issues/issue-1/codex-output/events"`,
		`Open to load Codex output.`,
		`hx-get="/"`,
		`/api/v1/state`,
		`/api/v1/logs`,
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
		`data-testid="state-issues-trigger-in-progress"`,
		`data-testid="state-issues-in-progress"`,
		`Issue ID`,
		`Title`,
		`href="https://linear.app/example/issue/COLIN-93"`,
		`href="/linear/issues/issue-1/metadata"`,
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
		`data-testid="rate-limits-codex-primary"`,
		`data-testid="rate-limits-codex-secondary"`,
		`data-testid="rate-limits-linear"`,
		`data-testid="rate-limits-linear-linear_requests"`,
		`Codex`,
		`Linear`,
		`aria-label="Codex 5h window used"`,
		`aria-valuenow="5"`,
		`aria-label="Codex 1w window used"`,
		`aria-valuenow="9"`,
		`aria-label="Linear requests used"`,
		`aria-valuenow="75"`,
		`5h`,
		`1w`,
		`Requests`,
		`resets in 5h32m`,
		`resets in 1w`,
		`next request in 3s`,
		`Context window: 70% left (78.4K used / 258K)`,
		`aria-valuenow="30"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("render missing %q\n%s", want, html)
		}
	}
	if strings.Contains(html, `data-testid="shell-instance"`) {
		t.Fatalf("render should not include shell renderer card\n%s", html)
	}
	if strings.Contains(html, `hx-trigger="every 5s"`) {
		t.Fatalf("render should not include timer-driven polling\n%s", html)
	}
	if strings.Contains(html, `src="/assets/htmx.min.js"`) {
		t.Fatalf("render should not include Colin's former HTMX shim asset\n%s", html)
	}
	if strings.Contains(html, "Tracked Issues") {
		t.Fatalf("render should not include tracked issues summary card\n%s", html)
	}
	if strings.Contains(html, "API snapshot") {
		t.Fatalf("render should not include API snapshot card\n%s", html)
	}
	if strings.Contains(html, `5% used of 5h window which resets in 5h32m`) {
		t.Fatalf("codex rate limits should render progress bars instead of old text rows\n%s", html)
	}
	if strings.Contains(html, `25 of 100 remaining`) {
		t.Fatalf("linear rate limit should not render the remaining-count detail\n%s", html)
	}
}

func TestPausedIndicatorRendersWithoutLinkWhenURLMissing(t *testing.T) {
	t.Parallel()

	html := renderNode(t, stateCountCard("Review", 3, nil, domain.PausedStateSummary{Count: 1}))
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

func TestRateLimitPanelKeepsCodexBucketWhenUsageUnavailable(t *testing.T) {
	t.Parallel()

	html := renderNode(t, rateLimitsPanel(domain.Snapshot{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		RateLimits: domain.RateLimitSnapshot{
			"primary": {
				ResetsAt:              ptr(time.Date(2026, 3, 28, 17, 32, 0, 0, time.UTC)),
				WindowDurationMinutes: int64Ptr(300),
			},
		},
	}))

	for _, want := range []string{
		`data-testid="rate-limits-codex"`,
		`data-testid="rate-limits-codex-primary"`,
		`aria-label="Codex 5h window used"`,
		`aria-valuetext="usage unavailable"`,
		`usage unavailable`,
		`resets in 5h32m`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("render missing %q\n%s", want, html)
		}
	}
	if strings.Contains(html, `aria-valuenow=`) {
		t.Fatalf("unknown codex usage should not render aria-valuenow\n%s", html)
	}
	if strings.Contains(html, `data-testid="rate-limits-codex">none reported`) {
		t.Fatalf("unknown codex usage should not collapse to none reported\n%s", html)
	}
}

func TestStateIssuePopoverRendersLinks(t *testing.T) {
	t.Parallel()

	html := renderNode(t, stateCountCard("Review", 1, []domain.StateIssueSummary{
		{
			ID:          "issue-2",
			Identifier:  "COLIN-94",
			ProjectName: "goth",
			ProjectSlug: "goth-be879f2c89f9",
			Title:       "Polish review labels",
			URL:         "https://linear.app/example/issue/COLIN-94",
		},
	}, domain.PausedStateSummary{}))

	for _, want := range []string{
		`data-testid="state-issues-trigger-review"`,
		`data-testid="state-issues-review"`,
		`class="table-wrap state-issues-table-wrap"`,
		`class="table state-issues-table"`,
		`Issue ID`,
		`Project`,
		`Title`,
		`data-testid="state-issue-review-COLIN-94"`,
		`class="state-issue-id-link" href="https://linear.app/example/issue/COLIN-94"`,
		`<td>goth</td>`,
		`href="/linear/issues/issue-2/metadata"`,
		`>COLIN-94</a>`,
		`>Polish review labels</a>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("render missing %q\n%s", want, html)
		}
	}
	for _, unwanted := range []string{`goth-be879f2c89f9`, `be879f2c89f9`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("render should not expose raw project slug %q\n%s", unwanted, html)
		}
	}
}

func TestProjectLabelStripsLinearHashSuffix(t *testing.T) {
	t.Parallel()

	got := projectLabel(domain.StateIssueSummary{ProjectSlug: "goth-be879f2c89f9"})
	if got != "goth" {
		t.Fatalf("projectLabel() = %q, want goth", got)
	}
}

func TestProjectLabelStripsDefaultProjectNameHashSuffix(t *testing.T) {
	t.Parallel()

	got := projectLabel(domain.StateIssueSummary{
		ProjectName: "goth-be879f2c89f9",
		ProjectSlug: "goth-be879f2c89f9",
	})
	if got != "goth" {
		t.Fatalf("projectLabel() = %q, want goth", got)
	}
}

func TestProjectLabelDoesNotExposeOpaqueHash(t *testing.T) {
	t.Parallel()

	got := projectLabel(domain.StateIssueSummary{
		ProjectName: "be879f2c89f9",
		ProjectSlug: "be879f2c89f9",
	})
	if got != "unknown" {
		t.Fatalf("projectLabel() = %q, want unknown", got)
	}
}

func TestWorkerOutputShellUsesLazyLoadEndpoints(t *testing.T) {
	t.Parallel()

	html := renderNode(t, workerOutput(domain.SnapshotRunning{
		IssueID:    "issue-1",
		Identifier: "COLIN-93",
	}))

	for _, want := range []string{
		`data-codex-output-panel="true"`,
		`data-codex-output-issue-id="issue-1"`,
		`data-codex-output-load-url="/api/v1/issues/issue-1/codex-output"`,
		`data-codex-output-events-url="/api/v1/issues/issue-1/codex-output/events"`,
		`data-codex-output-loaded="false"`,
		`Open to load Codex output.`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("worker output shell missing %q\n%s", want, html)
		}
	}
	if strings.Contains(html, `class="worker-output-entry"`) {
		t.Fatalf("worker output shell should not embed entries before hydration\n%s", html)
	}
}

func TestWorkerOutputListRendersNewestEntryFirst(t *testing.T) {
	t.Parallel()

	html := renderNode(t, WorkerOutputList("COLIN-93", []domain.OutputLog{
		{Timestamp: time.Date(2026, 3, 28, 11, 58, 1, 0, time.UTC), Event: "session_started", Message: "session started"},
		{Timestamp: time.Date(2026, 3, 28, 11, 59, 2, 0, time.UTC), Event: "turn_completed", Message: "Still working"},
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

	html := renderNode(t, WorkerOutputEntry(domain.OutputLog{
		Timestamp: time.Date(2026, 3, 28, 11, 59, 2, 0, time.UTC),
		Event:     "turn_completed",
		Message:   "turn_completed",
	}))

	if strings.Contains(html, `<pre class="mockup-code">turn_completed</pre>`) {
		t.Fatalf("turn_completed should not render redundant pre block\n%s", html)
	}
	if !strings.Contains(html, `class="badge badge-turn-completed"`) {
		t.Fatalf("turn_completed should use completed badge styling\n%s", html)
	}
}

func TestRenderContextWindowUsageUnavailableFallback(t *testing.T) {
	t.Parallel()

	html := renderNode(t, renderContextWindowUsage(domain.SnapshotRunning{Identifier: "COLIN-93"}))
	if !strings.Contains(html, `data-testid="context-window-COLIN-93"`) {
		t.Fatalf("context window label missing test id\n%s", html)
	}
	if !strings.Contains(html, `Context window: unavailable`) {
		t.Fatalf("context window fallback missing text\n%s", html)
	}
	if strings.Contains(html, `data-testid="context-window-bar-COLIN-93"`) {
		t.Fatalf("context window fallback should not render progress bar\n%s", html)
	}
}

func TestDashboardFragmentOmitsDocumentShell(t *testing.T) {
	t.Parallel()

	html := renderNode(t, Dashboard(domain.Snapshot{
		GeneratedAt:       time.Now().UTC(),
		ShutdownRequested: true,
		Counts:            map[string]int{},
	}))
	if strings.Contains(html, "<html") {
		t.Fatalf("fragment should not render document shell:\n%s", html)
	}
	if !strings.Contains(html, `id="dashboard-root"`) {
		t.Fatalf("fragment missing dashboard root:\n%s", html)
	}
	if !strings.Contains(html, `data-live-refresh-mode="fragment"`) {
		t.Fatalf("fragment missing live refresh mode:\n%s", html)
	}
	if !strings.Contains(html, `hx-trigger="colin:refresh"`) {
		t.Fatalf("fragment missing explicit live refresh trigger:\n%s", html)
	}
	if strings.Contains(html, `hx-trigger="every 5s"`) {
		t.Fatalf("fragment should not render timer-driven polling:\n%s", html)
	}
	if !strings.Contains(html, `data-testid="refresh-status"`) {
		t.Fatalf("fragment missing refresh status badge:\n%s", html)
	}
	if !strings.Contains(html, `data-testid="snapshot-age"`) {
		t.Fatalf("fragment missing snapshot age badge:\n%s", html)
	}
	if !strings.Contains(html, `data-refresh-status="live"`) {
		t.Fatalf("fragment missing live refresh status:\n%s", html)
	}
	if !strings.Contains(html, `data-testid="shutdown-alert"`) {
		t.Fatalf("fragment missing shutdown alert:\n%s", html)
	}
	if strings.Contains(html, "API snapshot") {
		t.Fatalf("fragment should not render API snapshot:\n%s", html)
	}
}

func TestFormatIntAddsCommas(t *testing.T) {
	t.Parallel()

	if got := formatInt(1234567890); got != "1,234,567,890" {
		t.Fatalf("formatInt() = %q, want %q", got, "1,234,567,890")
	}
}

func TestFormatRuntimeSecondsUsesCompactDuration(t *testing.T) {
	t.Parallel()

	if got := formatRuntimeSeconds((5 * time.Hour).Seconds() + (3 * time.Minute).Seconds()); got != "5h3m" {
		t.Fatalf("formatRuntimeSeconds() = %q, want %q", got, "5h3m")
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
			SlackChannelID:       "C12345678",
			SlackMessageTS:       "1743630000.123456",
			SlackPermalink:       "https://example.slack.com/archives/C12345678/p1743630000123456",
			UpdatedAt:            &updatedAt,
			CodexOutput: []domain.OutputLog{
				{Timestamp: time.Date(2026, 3, 29, 18, 3, 0, 0, time.UTC), Event: "turn_completed", Message: "Updated the metadata link."},
			},
		},
	}, updatedAt))

	for _, want := range []string{
		`data-testid="issue-metadata-panel"`,
		`data-testid="issue-metadata-output"`,
		`data-live-refresh-mode="reload"`,
		`src="` + gothhtmx.ScriptPath + `"`,
		`src="/assets/app.js"`,
		`COLIN-111 - Update metadata link`,
		`ExecPlan decision`,
		`one_shot`,
		`Last run type`,
		`ready_for_review`,
		`comment-12`,
		`Slack channel`,
		`C12345678`,
		`1743630000.123456`,
		`class="issue-title metadata-value-link" href="https://example.slack.com/archives/C12345678/p1743630000123456"`,
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

func TestIssueMetadataPageRendersUnsafeSlackPermalinkAsPlainText(t *testing.T) {
	t.Parallel()

	html := renderNode(t, IssueMetadataPage(domain.Issue{
		ID:         "issue-unsafe",
		Identifier: "COLIN-191",
		Title:      "Reject unsafe slack permalink scheme",
		ColinMetadata: &domain.ColinMetadata{
			SlackPermalink: "javascript:alert('owned')",
		},
	}, time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)))

	if !strings.Contains(html, `javascript:alert(&#39;owned&#39;)`) {
		t.Fatalf("render missing unsafe permalink text\n%s", html)
	}
	if strings.Contains(html, `href="javascript:alert(`) {
		t.Fatalf("render should not emit unsafe permalink href\n%s", html)
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
		`data-live-refresh-mode="reload"`,
		`src="` + gothhtmx.ScriptPath + `"`,
		`src="/assets/app.js"`,
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
