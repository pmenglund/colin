package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// Page renders the full document shell for the dashboard.
func Page(snapshot domain.Snapshot, shellRenderedAt time.Time) g.Node {
	shellID := shellRenderedAt.UTC().Format(time.RFC3339Nano)
	return h.Doctype(h.HTML(
		h.Lang("en"),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
			h.Title("Colin Live Tasks"),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/app.css")),
			h.Script(h.Src("/assets/htmx.min.js"), h.Defer()),
		),
		h.Body(
			h.Class("page-shell"),
			h.Div(
				h.Class("page-inner"),
				h.Header(
					h.Class("hero"),
					h.Div(
						h.Class("hero-grid"),
						h.Div(
							h.Span(h.Class("hero-label"), g.Text("Live Orchestrator View")),
							h.H1(g.Text("Colin")),
							h.P(
								g.Text("Colin is a Go service that watches a Linear project, runs Codex in per-issue workspaces, and hands off review-ready changes. "),
								h.A(
									h.Href("https://github.com/pmenglund/colin"),
									g.Text("View the GitHub repository"),
								),
								g.Text("."),
							),
						),
						h.Div(
							h.Class("shell-meta"),
							h.Div(
								h.Class("card"),
								h.Data("testid", "shell-instance"),
								h.Span(h.Class("badge badge-info"), g.Text("Shell Render")),
								h.Div(h.Class("issue-title"), g.Text(shellID)),
							),
						),
					),
				),
				Dashboard(snapshot),
				h.Footer(
					h.Class("footnote"),
					g.Text("JSON API: "),
					h.A(h.Href("/api/v1/state"), g.Text("/api/v1/state")),
					g.Text(" | "),
					h.A(h.Href("/setup/funnel"), g.Text("Tailscale webhook setup")),
				),
			),
		),
	))
}

// IssueMetadataPage renders a standalone page for one issue's Colin metadata.
func IssueMetadataPage(issue domain.Issue, shellRenderedAt time.Time) g.Node {
	title := strings.TrimSpace(issue.Identifier)
	if title == "" {
		title = "Issue metadata"
	}
	if strings.TrimSpace(issue.Title) != "" {
		title += " - " + issue.Title
	}

	metadata := issue.ColinMetadata
	return h.Doctype(h.HTML(
		h.Lang("en"),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
			h.Title("Colin Metadata: "+title),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/app.css")),
		),
		h.Body(
			h.Class("page-shell"),
			h.Div(
				h.Class("page-inner"),
				h.Header(
					h.Class("hero"),
					h.Div(
						h.Class("hero-grid"),
						h.Div(
							h.Span(h.Class("hero-label"), g.Text("Linear Issue Metadata")),
							h.H1(g.Text(title)),
							h.P(g.Text("Colin metadata and captured Codex output for this issue.")),
						),
						h.Div(
							h.Class("shell-meta"),
							h.Div(
								h.Class("card"),
								h.Data("testid", "metadata-rendered-at"),
								h.Span(h.Class("badge badge-info"), g.Text("Rendered")),
								h.Div(h.Class("issue-title"), g.Text(shellRenderedAt.UTC().Format(time.RFC3339))),
							),
						),
					),
				),
				h.Main(
					h.Class("dashboard-root"),
					h.Section(
						h.Class("table-card"),
						h.Data("testid", "issue-metadata-panel"),
						h.H3(g.Text("Issue")),
						h.Div(
							h.Class("worker-grid"),
							metadataStatCard("Identifier", fallback(issue.Identifier, "unknown")),
							metadataStatCard("State", fallback(issue.State, "unknown")),
							metadataStatCard("ExecPlan decision", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return string(value.ExecPlanDecision) }), "not recorded")),
							metadataStatCard("Last run type", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return string(value.LastRunType) }), "unknown")),
							metadataStatCard("Last outcome", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return string(value.LastOutcome) }), "unknown")),
							metadataStatCard("Summary comment", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return value.LastSummaryCommentID }), "not recorded")),
							metadataStatCard("Updated", fallback(metadataTimestamp(metadata), "not recorded")),
						),
						issueLinks(issue),
					),
					h.Section(
						h.Class("table-card"),
						h.Data("testid", "issue-metadata-output"),
						h.H3(g.Text("Codex output")),
						h.P(g.Text("Captured output for the latest Colin run on this issue.")),
						h.Div(
							h.Class("worker-output-list"),
							renderOutputEntries(metadataOutput(metadata)),
						),
					),
				),
				h.Footer(
					h.Class("footnote"),
					h.A(h.Href("/"), g.Text("Back to dashboard")),
				),
			),
		),
	))
}

// ExecPlanPage renders a standalone page for one issue's stored ExecPlan.
func ExecPlanPage(issue domain.Issue, shellRenderedAt time.Time) g.Node {
	title := strings.TrimSpace(issue.Identifier)
	if title == "" {
		title = "ExecPlan"
	}
	if strings.TrimSpace(issue.Title) != "" {
		title += " - " + issue.Title
	}

	plan := issue.ExecPlan
	return h.Doctype(h.HTML(
		h.Lang("en"),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
			h.Title("Colin ExecPlan: "+title),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/app.css")),
		),
		h.Body(
			h.Class("page-shell"),
			h.Div(
				h.Class("page-inner"),
				h.Header(
					h.Class("hero"),
					h.Div(
						h.Class("hero-grid"),
						h.Div(
							h.Span(h.Class("hero-label"), g.Text("Linear Issue ExecPlan")),
							h.H1(g.Text(title)),
							h.P(g.Text("Stored ExecPlan attachment content for this issue.")),
						),
						h.Div(
							h.Class("shell-meta"),
							h.Div(
								h.Class("card"),
								h.Data("testid", "exec-plan-rendered-at"),
								h.Span(h.Class("badge badge-info"), g.Text("Rendered")),
								h.Div(h.Class("issue-title"), g.Text(shellRenderedAt.UTC().Format(time.RFC3339))),
							),
						),
					),
				),
				h.Main(
					h.Class("dashboard-root"),
					h.Section(
						h.Class("table-card"),
						h.Data("testid", "issue-exec-plan-panel"),
						h.H3(g.Text("Issue")),
						h.Div(
							h.Class("worker-grid"),
							metadataStatCard("Identifier", fallback(issue.Identifier, "unknown")),
							metadataStatCard("State", fallback(issue.State, "unknown")),
							metadataStatCard("Attachment", fallback(execPlanValue(plan, func(value *domain.ExecPlan) string { return value.AttachmentID }), "not recorded")),
							metadataStatCard("Updated", fallback(execPlanTimestamp(plan), "not recorded")),
						),
						issueLinks(issue),
					),
					h.Section(
						h.Class("table-card"),
						h.Data("testid", "issue-exec-plan-body"),
						h.H3(g.Text("ExecPlan")),
						h.P(g.Text("This is the canonical plan Colin stored on the Linear issue.")),
						renderExecPlanBody(plan),
					),
				),
				h.Footer(
					h.Class("footnote"),
					h.A(h.Href("/"), g.Text("Back to dashboard")),
				),
			),
		),
	))
}

// FunnelSetupPage renders the Tailscale webhook ingress readiness page.
func FunnelSetupPage(status domain.FunnelSetupStatus, shellRenderedAt time.Time) g.Node {
	stateText := "Needs setup"
	if status.Ready {
		stateText = "Ready for webhooks"
	}

	return h.Doctype(h.HTML(
		h.Lang("en"),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
			h.Title("Colin Webhook Setup"),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/app.css")),
		),
		h.Body(
			h.Class("page-shell"),
			h.Div(
				h.Class("page-inner"),
				h.Header(
					h.Class("hero"),
					h.Div(
						h.Class("hero-grid"),
						h.Div(
							h.Span(h.Class("hero-label"), g.Text("Tailscale Webhook Setup")),
							h.H1(g.Text("Webhook ingress readiness")),
							h.P(g.Text("Verify only Colin's `/webhooks` endpoints are publicly reachable before configuring Linear or GitHub webhooks. The dashboard and metadata pages stay local unless you publish them separately.")),
						),
						h.Div(
							h.Class("shell-meta"),
							h.Div(
								h.Class("card"),
								h.Data("testid", "funnel-status"),
								h.Span(h.Class("badge badge-info"), g.Text(stateText)),
								h.Div(h.Class("issue-title"), g.Text(shellRenderedAt.UTC().Format(time.RFC3339))),
							),
						),
					),
				),
				h.Main(
					h.Class("dashboard-root"),
					h.Section(
						h.Class("table-card"),
						h.Data("testid", "funnel-urls"),
						h.H3(g.Text("URLs")),
						h.Div(
							h.Class("worker-grid"),
							metadataStatCard("Local UI base URL", fallback(status.LocalBaseURL, "not available")),
							metadataStatCard("Public webhook base URL", fallback(status.PublicBaseURL, "not available")),
							metadataStatCard("Linear webhook URL", fallback(status.LinearWebhookURL, "not available")),
							metadataStatCard("GitHub webhook URL", fallback(status.GitHubWebhookURL, "not available")),
						),
						h.P(g.Text("Expose only `/webhooks/*` through Tailscale Funnel. Dashboard pages continue to use the local or explicitly configured UI URL.")),
						h.P(g.Text("Suggested command: "), h.Code(g.Text(fallback(status.SuggestedCommand, "none")))),
					),
					h.Section(
						h.Class("table-card"),
						h.Data("testid", "funnel-checks"),
						h.H3(g.Text("Checks")),
						h.Div(
							h.Class("table-wrap"),
							h.Table(
								h.Class("table"),
								h.THead(
									h.Tr(
										h.Th(g.Text("Check")),
										h.Th(g.Text("Status")),
										h.Th(g.Text("Detail")),
									),
								),
								h.TBody(g.Map(status.Checks, func(check domain.SetupCheck) g.Node {
									detail := check.Detail
									if detail == "" {
										detail = check.Remediation
									} else if check.Remediation != "" {
										detail += " " + check.Remediation
									}
									return h.Tr(
										h.Data("testid", "funnel-check-"+check.ID),
										h.Td(g.Text(check.Label)),
										h.Td(g.Text(strings.ToUpper(check.Status))),
										h.Td(g.Text(fallback(detail, "none"))),
									)
								})),
							),
						),
					),
				),
				h.Footer(
					h.Class("footnote"),
					h.A(h.Href("/"), g.Text("Back to dashboard")),
					g.Text(" | "),
					h.A(h.Href("/api/v1/setup/funnel"), g.Text("JSON status")),
				),
			),
		),
	))
}

// Dashboard renders the HTMX-replaceable dashboard fragment.
func Dashboard(snapshot domain.Snapshot) g.Node {
	return h.Main(
		h.ID("dashboard-root"),
		h.Class("dashboard-root"),
		h.Data("testid", "dashboard-root"),
		g.Attr("hx-get", "/"),
		g.Attr("hx-trigger", "every 5s"),
		g.Attr("hx-target", "#dashboard-root"),
		g.Attr("hx-swap", "outerHTML"),
		toolbar(snapshot),
		statsGrid(snapshot),
		h.Div(
			h.Class("stack"),
			stateCountsPanel(snapshot),
			runningPanel(snapshot),
			retryingPanel(snapshot),
			rateLimitsPanel(snapshot),
			apiPanel(snapshot),
		),
	)
}

func toolbar(snapshot domain.Snapshot) g.Node {
	generatedAt := snapshot.GeneratedAt.UTC().Format(time.RFC3339)
	return h.Section(
		h.Class("card dashboard-toolbar"),
		h.Div(
			h.Class("dashboard-title"),
			h.H2(g.Text("Current task surface")),
			h.P(g.Text("HTMX keeps this fragment fresh without reloading the full page shell.")),
		),
		h.Div(
			h.Class("toolbar-actions"),
			h.Span(h.Class("badge badge-success"), h.Data("testid", "refresh-status"), g.Attr("data-refresh-status", "live"), g.Attr("data-generated-at", generatedAt), g.Attr("aria-live", "polite"), g.Attr("title", "Last successful update at "+generatedAt), g.Text("Live data")),
			h.Span(h.Class("badge badge-accent"), h.Data("testid", "snapshot-generated"), g.Text(generatedAt)),
			h.Button(
				h.Type("button"),
				h.Class("btn"),
				h.Class("refresh-toggle"),
				h.Data("testid", "refresh-button"),
				g.Attr("data-refresh-toggle", "true"),
				g.Attr("aria-label", "Pause automatic refresh"),
				g.Attr("title", "Pause automatic refresh"),
				g.Text("❚❚"),
			),
		),
	)
}

func statsGrid(snapshot domain.Snapshot) g.Node {
	return h.Section(
		h.Class("stats"),
		statCard("Running", strconv.Itoa(snapshot.Counts["running"]), "active issue workspaces"),
		statCard("Retrying", strconv.Itoa(snapshot.Counts["retrying"]), "queued follow-up attempts"),
		statCard("Total Tokens", formatInt(snapshot.CodexTotals.TotalTokens), "aggregate across active runs"),
		statCard("Run Seconds", formatInt(int64(snapshot.CodexTotals.SecondsRunning)), "combined wall clock"),
	)
}

func stateCountsPanel(snapshot domain.Snapshot) g.Node {
	states := []string{"Backlog", "Todo", "In Progress", "Refine", "Review", "Merge"}

	return h.Section(
		h.Class("table-card"),
		h.Data("testid", "linear-state-counts"),
		h.H3(g.Text("Linear issues")),
		h.P(g.Text("Tracked Linear issues in the active handoff pipeline.")),
		h.Div(
			h.Class("state-count-grid"),
			g.Map(states, func(state string) g.Node {
				return stateCountCard(state, snapshot.IssueStates[state], snapshot.PausedIssueStates[state])
			}),
		),
	)
}

func stateDescription(state string) string {
	switch state {
	case "Backlog":
		return "Issue is parked outside the active handoff states."
	case "Todo":
		return "Issue is ready for Colin to pick up."
	case "In Progress":
		return "Issue is actively being worked."
	case "Refine":
		return "Issue needs human clarification before a PR can be reviewed."
	case "Review":
		return "Issue has a PR and is awaiting human review."
	case "Merge":
		return "Issue is approved and waiting to be merged."
	default:
		return "Issue is currently in this state."
	}
}

func statCard(title, value, desc string) g.Node {
	return h.Div(
		h.Class("stat"),
		h.Div(h.Class("stat-title"), g.Text(title)),
		h.Div(h.Class("stat-value"), g.Text(value)),
		h.Div(h.Class("stat-desc"), g.Text(desc)),
	)
}

func stateCountCard(state string, total int, paused domain.PausedStateSummary) g.Node {
	return h.Div(
		h.Class("stat"),
		h.Class("state-card"),
		h.Div(h.Class("stat-title"), g.Text(state)),
		h.Div(h.Class("stat-value"), g.Text(strconv.Itoa(total))),
		h.Div(h.Class("stat-desc"), g.Text(stateDescription(state))),
		pausedIndicator(state, paused),
	)
}

func pausedIndicator(state string, paused domain.PausedStateSummary) g.Node {
	if paused.Count <= 0 {
		return nil
	}

	label := fmt.Sprintf("%d paused", paused.Count)
	testID := "paused-issues-" + stateSlug(state)
	if strings.TrimSpace(paused.URL) == "" {
		return h.Span(
			h.Class("paused-indicator"),
			h.Data("testid", testID),
			g.Text(label),
		)
	}
	return h.A(
		h.Class("paused-indicator"),
		h.Data("testid", testID),
		h.Href(paused.URL),
		g.Text(label),
	)
}

func stateSlug(state string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(state), " ", "-"))
}

func metadataStatCard(title, value string) g.Node {
	return h.Div(
		h.Class("card"),
		h.Div(h.Class("stat-title"), g.Text(title)),
		h.Div(h.Class("issue-title"), g.Text(value)),
	)
}

func issueLinks(issue domain.Issue) g.Node {
	items := g.Group{
		h.A(h.Href("/"), g.Text("Dashboard")),
	}
	if strings.TrimSpace(issue.ID) != "" {
		items = append(items, g.Text(" | "), h.A(h.Href(domain.ColinMetadataPath(issue.ID)), g.Text("Metadata")))
		items = append(items, g.Text(" | "), h.A(h.Href(domain.ColinExecPlanPath(issue.ID)), g.Text("ExecPlan")))
	}
	if issue.URL != nil && strings.TrimSpace(*issue.URL) != "" {
		items = append(items, g.Text(" | "), h.A(h.Href(*issue.URL), g.Text("Linear issue")))
	}
	return h.P(items)
}

func metadataValue(value *domain.ColinMetadata, field func(*domain.ColinMetadata) string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(field(value))
}

func metadataTimestamp(value *domain.ColinMetadata) string {
	if value == nil || value.UpdatedAt == nil {
		return ""
	}
	return value.UpdatedAt.UTC().Format(time.RFC3339)
}

func metadataOutput(value *domain.ColinMetadata) []domain.OutputLog {
	if value == nil {
		return nil
	}
	return value.CodexOutput
}

func execPlanValue(value *domain.ExecPlan, field func(*domain.ExecPlan) string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(field(value))
}

func execPlanTimestamp(value *domain.ExecPlan) string {
	if value == nil || value.UpdatedAt == nil {
		return ""
	}
	return value.UpdatedAt.UTC().Format(time.RFC3339)
}

func renderExecPlanBody(value *domain.ExecPlan) g.Node {
	if value == nil || strings.TrimSpace(value.Body) == "" {
		return h.Pre(
			h.Class("mockup-code"),
			g.Text("No ExecPlan is currently recorded for this issue."),
		)
	}
	return h.Pre(
		h.Class("mockup-code"),
		g.Text(strings.TrimSpace(value.Body)),
	)
}

func runningPanel(snapshot domain.Snapshot) g.Node {
	if len(snapshot.Running) == 0 {
		return emptyPanel("Running tasks", "No active tasks are running at the moment.")
	}

	return h.Section(
		h.Class("table-card"),
		h.Data("testid", "running-panel"),
		h.H3(g.Text("Running tasks")),
		h.P(g.Text("Each worker card shows its live state, token usage, and an expandable Codex event stream.")),
		h.Div(
			h.Class("worker-grid"),
			g.Map(snapshot.Running, runningCard),
		),
	)
}

func retryingPanel(snapshot domain.Snapshot) g.Node {
	if len(snapshot.Retrying) == 0 {
		return emptyPanel("Retry queue", "No retries are waiting. Colin is either idle or actively running work.")
	}

	now := snapshot.GeneratedAt
	return h.Section(
		h.Class("table-card"),
		h.Data("testid", "retry-panel"),
		h.H3(g.Text("Retry queue")),
		h.P(g.Text("Queued issues waiting for the next retry window or open slot.")),
		h.Div(
			h.Class("table-wrap"),
			h.Table(
				h.Class("table"),
				h.THead(
					h.Tr(
						h.Th(g.Text("Issue")),
						h.Th(g.Text("Attempt")),
						h.Th(g.Text("Due")),
						h.Th(g.Text("Reason")),
					),
				),
				h.TBody(g.Map(snapshot.Retrying, func(entry domain.RetryEntry) g.Node {
					return h.Tr(
						h.Data("testid", "retry-row-"+entry.Identifier),
						h.Td(
							h.Span(h.Class("badge badge-warning"), g.Text(entry.Identifier)),
						),
						h.Td(g.Text(strconv.Itoa(entry.Attempt))),
						h.Td(g.Text(formatDuration(entry.DueAt.Sub(now)))),
						h.Td(g.Text(fallback(entry.Error, "waiting for next attempt"))),
					)
				})),
			),
		),
	)
}

func rateLimitsPanel(snapshot domain.Snapshot) g.Node {
	codexLines, linearLines := rateLimitRows(snapshot.GeneratedAt, snapshot.RateLimits)

	return h.Section(
		h.Class("table-card"),
		h.H3(g.Text("Rate limits")),
		h.P(g.Text("Latest limits reported by Codex and Linear.")),
		h.Div(
			h.Class("rate-limit-grid"),
			rateLimitBox("Codex", "rate-limits-codex", codexLines),
			rateLimitBox("Linear", "rate-limits-linear", linearLines),
		),
	)
}

func apiPanel(snapshot domain.Snapshot) g.Node {
	return h.Section(
		h.Class("alert"),
		h.H3(g.Text("API snapshot")),
		h.P(g.Text("Use the JSON endpoint for scripts or debugging outside the browser.")),
		h.Pre(h.Class("mockup-code"), g.Text(fmt.Sprintf("{\"generated_at\":\"%s\",\"running\":%d,\"retrying\":%d}", snapshot.GeneratedAt.Format(time.RFC3339), snapshot.Counts["running"], snapshot.Counts["retrying"]))),
	)
}

func rateLimitBox(title, testID string, lines []string) g.Node {
	return h.Div(
		h.Class("rate-limit-box"),
		h.H4(g.Text(title)),
		h.Pre(
			h.Class("mockup-code"),
			h.Data("testid", testID),
			g.Text(strings.Join(fallbackLines(lines), "\n")),
		),
	)
}

func rateLimitRows(now time.Time, rateLimits domain.RateLimitSnapshot) ([]string, []string) {
	if len(rateLimits) == 0 {
		return nil, nil
	}

	codexRows := make([]string, 0, 2)
	for _, name := range []string{"primary", "secondary"} {
		limit, ok := rateLimits[name]
		if !ok {
			continue
		}
		codexRows = append(codexRows, fmt.Sprintf(
			"%s of %s window which resets in %s",
			rateLimitUsed(limit.UsedPercent),
			rateLimitWindow(limit.WindowDurationMinutes),
			rateLimitResetDuration(now, limit.ResetsAt),
		))
	}
	var linearRows []string
	for name, limit := range rateLimits {
		if name == "primary" || name == "secondary" {
			continue
		}
		remaining, limitValue, ok := rateLimitRemaining(limit)
		if !ok {
			continue
		}
		line := fmt.Sprintf(
			"resets in %s, %d of %d remaining",
			rateLimitResetDuration(now, limit.ResetsAt),
			remaining,
			limitValue,
		)
		if nextAllowedAt, ok := rateLimitNextAllowed(limit); ok && nextAllowedAt.After(now) {
			line = fmt.Sprintf("%s next request in %s", line, formatDuration(nextAllowedAt.Sub(now)))
		}
		linearRows = append(linearRows, line)
	}
	sort.Strings(linearRows)
	return codexRows, linearRows
}

func fallbackLines(lines []string) []string {
	if len(lines) == 0 {
		return []string{"none reported"}
	}
	return lines
}

func rateLimitRemaining(limit domain.RateLimitWindow) (int64, int64, bool) {
	if limit.Remaining == nil || limit.Limit == nil {
		return 0, 0, false
	}
	return *limit.Remaining, *limit.Limit, true
}

func rateLimitNextAllowed(limit domain.RateLimitWindow) (time.Time, bool) {
	if limit.NextAllowedAt == nil {
		return time.Time{}, false
	}
	return limit.NextAllowedAt.UTC(), true
}

func emptyPanel(title, body string) g.Node {
	return h.Section(
		h.Class("alert empty-state"),
		h.H3(g.Text(title)),
		h.P(g.Text(body)),
	)
}

func runningCard(entry domain.SnapshotRunning) g.Node {
	titleNode := g.Node(h.Span(h.Class("worker-issue-title"), g.Text(entry.Title)))
	if entry.URL != nil && strings.TrimSpace(*entry.URL) != "" {
		titleNode = h.A(h.Class("worker-issue-link"), h.Href(*entry.URL), g.Text(entry.Title))
	}
	issueNode := h.Div(
		h.Class("worker-issue"),
		h.Span(h.Class("badge badge-info"), g.Text(entry.Identifier)),
		titleNode,
	)

	lastEventAt := "waiting for first event"
	if entry.LastEventAt != nil {
		lastEventAt = entry.LastEventAt.Format(time.RFC3339)
	}

	return h.Article(
		h.Class("worker-card"),
		h.Data("testid", "worker-card-"+entry.Identifier),
		h.Div(
			h.Class("worker-header"),
			issueNode,
			h.Span(h.Class(stateBadgeClass(entry.State)), g.Text(entry.State)),
		),
		h.Div(
			h.Class("worker-metrics"),
			h.Div(
				h.Class("card"),
				h.H3(g.Text("Session")),
				h.Div(h.Class("metric-line"), g.Text("Session: "+fallback(entry.SessionID, "not assigned"))),
				h.Div(h.Class("metric-line"), g.Text("Turns: "+strconv.Itoa(entry.TurnCount))),
				h.Div(h.Class("metric-line"), g.Text("Started: "+entry.StartedAt.Format(time.RFC3339))),
			),
			h.Div(
				h.Class("card"),
				h.H3(g.Text("Activity")),
				h.Div(h.Class("metric-line"), g.Text(fallback(entry.LastMessage, "no message"))),
				h.Div(h.Class("metric-line"), g.Text("Last event at: "+lastEventAt)),
			),
			h.Div(
				h.Class("card"),
				h.H3(g.Text("Usage")),
				h.Div(h.Class("metric-line"), h.Data("testid", "turn-count-"+entry.Identifier), g.Text("Turns: "+strconv.Itoa(entry.TurnCount))),
				h.Div(h.Class("metric-line"), g.Text("Input: "+formatInt(entry.InputTokens))),
				h.Div(h.Class("metric-line"), g.Text("Output: "+formatInt(entry.OutputTokens))),
				h.Div(h.Class("metric-line"), g.Text("Total: "+formatInt(entry.TotalTokens))),
			),
		),
		workerOutput(entry),
	)
}

func workerOutput(entry domain.SnapshotRunning) g.Node {
	return h.Details(
		h.ID("worker-output-details-"+entry.Identifier),
		h.Class("worker-output"),
		g.Attr("data-preserve-open", "true"),
		h.Summary(g.Text("Codex output")),
		h.Div(
			h.Class("worker-output-list"),
			h.Data("testid", "worker-output-"+entry.Identifier),
			renderOutputEntries(entry.OutputLog),
		),
	)
}

func renderOutputEntries(log []domain.OutputLog) g.Node {
	if len(log) == 0 {
		return h.Pre(
			h.Class("mockup-code"),
			g.Text("No Codex output captured yet."),
		)
	}

	entries := make(g.Group, 0, len(log))
	for i := len(log) - 1; i >= 0; i-- {
		item := log[i]
		message := strings.TrimSpace(item.Message)
		if message == "" {
			message = item.Event
		}
		timestamp := item.Timestamp.UTC().Format(time.RFC3339)
		entryChildren := g.Group{
			h.Div(
				h.Class("worker-output-meta"),
				h.Span(
					h.Class("worker-output-time"),
					g.Attr("data-local-time", "true"),
					g.Attr("data-timestamp", timestamp),
					g.Text(item.Timestamp.UTC().Format("15:04:05 MST")),
				),
				h.Span(h.Class(outputEventBadgeClass(item.Event)), g.Text(item.Event)),
			),
		}
		if outputMessageAddsDetail(item.Event, message) {
			entryChildren = append(entryChildren, h.Pre(
				h.Class("mockup-code"),
				g.Text(message),
			))
		}
		entries = append(entries, h.Div(
			h.Class("worker-output-entry"),
			entryChildren,
		))
	}

	return entries
}

func outputEventBadgeClass(eventName string) string {
	switch strings.ToLower(strings.TrimSpace(eventName)) {
	case "session_started":
		return "badge badge-session"
	case "turn_completed":
		return "badge badge-turn-completed"
	default:
		return "badge badge-info"
	}
}

func outputMessageAddsDetail(eventName, message string) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	normalize := func(value string) string {
		return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "_", " ")))
	}
	return normalize(message) != normalize(eventName)
}

func stateBadgeClass(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "review", "merge":
		return "badge badge-warning"
	case "done", "closed", "cancelled", "canceled", "duplicate":
		return "badge badge-danger"
	default:
		return "badge badge-accent"
	}
}

func formatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return value.Round(time.Second).String()
}

func rateLimitResetDuration(now time.Time, value *time.Time) string {
	if value == nil {
		return "unknown"
	}
	return formatCompactDuration(value.Sub(now))
}

func rateLimitUsed(value *int64) string {
	if value == nil {
		return "unknown used"
	}
	return fmt.Sprintf("%d%% used", *value)
}

func rateLimitWindow(value *int64) string {
	if value == nil {
		return "unknown"
	}
	minutes := time.Duration(*value) * time.Minute
	return formatCompactDuration(minutes)
}

func formatCompactDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	value = value.Round(time.Minute)
	if value < time.Minute {
		return "0m"
	}

	const week = 7 * 24 * time.Hour
	var parts []string
	if weeks := value / week; weeks > 0 {
		parts = append(parts, fmt.Sprintf("%dw", weeks))
		value -= weeks * week
	}
	if hours := value / time.Hour; hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
		value -= hours * time.Hour
	}
	if minutes := value / time.Minute; minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if len(parts) == 0 {
		return "0m"
	}
	return strings.Join(parts, "")
}

func fallback(value, otherwise string) string {
	if strings.TrimSpace(value) == "" {
		return otherwise
	}
	return value
}
