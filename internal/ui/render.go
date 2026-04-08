package ui

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	gothalert "github.com/pmenglund/goth/components/alert"
	gothbadge "github.com/pmenglund/goth/components/badge"
	gothbutton "github.com/pmenglund/goth/components/button"
	gothcard "github.com/pmenglund/goth/components/card"
	"github.com/pmenglund/goth/components/classmode"
	gothtable "github.com/pmenglund/goth/components/table"
	gothhtmx "github.com/pmenglund/goth/htmx"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

func colinCard(className string, children ...g.Node) g.Node {
	return colinCardWith(gothcard.Props{Class: className}, children...)
}

func colinCardWith(props gothcard.Props, children ...g.Node) g.Node {
	props.ClassMode = classmode.ClassReplace
	return gothcard.Card(props, children...)
}

func colinAlertWith(props gothalert.Props, children ...g.Node) g.Node {
	props.ClassMode = classmode.ClassReplace
	return gothalert.Alert(props, children...)
}

func colinButtonWith(props gothbutton.Props, children ...g.Node) g.Node {
	props.ClassMode = classmode.ClassReplace
	return gothbutton.Button(props, children...)
}

func colinBadge(className string, children ...g.Node) g.Node {
	return gothbadge.Badge(gothbadge.Props{
		ClassMode: classmode.ClassReplace,
		Class:     className,
	}, children...)
}

func colinBadgeWith(props gothbadge.Props, children ...g.Node) g.Node {
	props.ClassMode = classmode.ClassReplace
	return gothbadge.Badge(props, children...)
}

func appScripts() g.Node {
	return g.Group([]g.Node{
		gothhtmx.Script(gothhtmx.ScriptProps{}),
		h.Script(h.Src("/assets/app.js"), h.Defer()),
	})
}

// Page renders the full document shell for the dashboard.
func Page(snapshot domain.Snapshot, _ time.Time) g.Node {
	return h.Doctype(h.HTML(
		h.Lang("en"),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
			h.Title("Colin Live Tasks"),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/app.css")),
			appScripts(),
		),
		h.Body(
			h.Class("page-shell"),
			h.Div(
				h.Class("page-inner"),
				h.Header(
					h.Class("hero"),
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
				),
				Dashboard(snapshot),
				h.Footer(
					h.Class("footnote"),
					g.Text("JSON API: "),
					h.A(h.Href("/api/v1/state"), g.Text("/api/v1/state")),
					g.Text(" | "),
					h.A(h.Href("/api/v1/logs"), g.Text("/api/v1/logs")),
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
			appScripts(),
		),
		h.Body(
			h.Class("page-shell"),
			g.Attr("data-live-refresh-mode", "reload"),
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
							colinCardWith(
								gothcard.Props{Class: "card", DataTestID: "metadata-rendered-at"},
								colinBadge("badge badge-info", g.Text("Rendered")),
								h.Div(h.Class("issue-title"), g.Text(shellRenderedAt.UTC().Format(time.RFC3339))),
							),
						),
					),
				),
				h.Main(
					h.Class("dashboard-root"),
					colinCardWith(
						gothcard.Props{Class: "table-card", DataTestID: "issue-metadata-panel"},
						h.H3(g.Text("Issue")),
						h.Div(
							h.Class("worker-grid"),
							metadataStatCard("Identifier", fallback(issue.Identifier, "unknown")),
							metadataStatCard("State", fallback(issue.State, "unknown")),
							metadataStatCard("ExecPlan decision", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return string(value.ExecPlanDecision) }), "not recorded")),
							metadataStatCard("Last run type", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return string(value.LastRunType) }), "unknown")),
							metadataStatCard("Last outcome", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return string(value.LastOutcome) }), "unknown")),
							metadataStatCard("Summary comment", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return value.LastSummaryCommentID }), "not recorded")),
							metadataStatCard("Slack channel", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return value.SlackChannelID }), "not recorded")),
							metadataStatCard("Slack message", fallback(metadataValue(metadata, func(value *domain.ColinMetadata) string { return value.SlackMessageTS }), "not recorded")),
							metadataLinkStatCard("Slack permalink", metadataValue(metadata, func(value *domain.ColinMetadata) string { return value.SlackPermalink }), "not recorded"),
							metadataStatCard("Updated", fallback(metadataTimestamp(metadata), "not recorded")),
						),
						issueLinks(issue),
					),
					colinCardWith(
						gothcard.Props{Class: "table-card", DataTestID: "issue-metadata-output"},
						h.H3(g.Text("Codex output")),
						h.P(g.Text("Captured output for the latest Colin run on this issue.")),
						WorkerOutputList(issue.Identifier, metadataOutput(metadata)),
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
			appScripts(),
		),
		h.Body(
			h.Class("page-shell"),
			g.Attr("data-live-refresh-mode", "reload"),
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
							colinCardWith(
								gothcard.Props{Class: "card", DataTestID: "exec-plan-rendered-at"},
								colinBadge("badge badge-info", g.Text("Rendered")),
								h.Div(h.Class("issue-title"), g.Text(shellRenderedAt.UTC().Format(time.RFC3339))),
							),
						),
					),
				),
				h.Main(
					h.Class("dashboard-root"),
					colinCardWith(
						gothcard.Props{Class: "table-card", DataTestID: "issue-exec-plan-panel"},
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
					colinCardWith(
						gothcard.Props{Class: "table-card", DataTestID: "issue-exec-plan-body"},
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
	stateText := "Needs Tailscale setup"
	if status.Ready {
		stateText = "Tailscale ready"
	}

	return h.Doctype(h.HTML(
		h.Lang("en"),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
			h.Title("Colin Webhook Setup"),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/app.css")),
			appScripts(),
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
							h.Span(h.Class("hero-label"), g.Text("Tailscale Setup")),
							h.H1(g.Text("UI and webhook readiness")),
							h.P(g.Text("Verify Colin's UI is available on the tailnet through Tailscale Serve, and expose only Colin's `/webhooks` endpoints publicly through Tailscale Funnel when webhook support is enabled.")),
						),
						h.Div(
							h.Class("shell-meta"),
							colinCardWith(
								gothcard.Props{Class: "card", DataTestID: "funnel-status"},
								colinBadge("badge badge-info", g.Text(stateText)),
								h.Div(h.Class("issue-title"), g.Text(shellRenderedAt.UTC().Format(time.RFC3339))),
							),
						),
					),
				),
				h.Main(
					h.Class("dashboard-root"),
					colinCardWith(
						gothcard.Props{Class: "table-card", DataTestID: "funnel-urls"},
						h.H3(g.Text("URLs")),
						h.Div(
							h.Class("worker-grid"),
							metadataStatCard("Local UI base URL", fallback(status.LocalBaseURL, "not available")),
							metadataStatCard("Tailnet UI base URL", fallback(status.TailnetUIBaseURL, "not available")),
							metadataStatCard("Local webhook base URL", fallback(status.LocalWebhookBaseURL, "disabled")),
							metadataStatCard("Public webhook base URL", fallback(status.PublicBaseURL, "not available")),
							metadataStatCard("Linear webhook URL", fallback(status.LinearWebhookURL, "not available")),
							metadataStatCard("GitHub webhook URL", fallback(status.GitHubWebhookURL, "not available")),
						),
						h.P(g.Text("Use Tailscale Serve for the UI on `/`. Use Tailscale Funnel only for `/webhooks/*` when `server.webhook_port` is configured.")),
						h.P(g.Text("Suggested Serve command: "), h.Code(g.Text(fallback(status.SuggestedServeCommand, "none")))),
						h.P(g.Text("Suggested Funnel command: "), h.Code(g.Text(fallback(status.SuggestedCommand, "none")))),
					),
					colinCardWith(
						gothcard.Props{Class: "table-card", DataTestID: "funnel-checks"},
						h.H3(g.Text("Checks")),
						funnelChecksTable(status.Checks),
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

func funnelChecksTable(checks []domain.SetupCheck) g.Node {
	rows := make([]gothtable.Row, 0, len(checks))
	for _, check := range checks {
		detail := check.Detail
		if detail == "" {
			detail = check.Remediation
		} else if check.Remediation != "" {
			detail += " " + check.Remediation
		}
		rows = append(rows, gothtable.Row{
			DataTestID: "funnel-check-" + check.ID,
			CellItems: []gothtable.Cell{
				{Content: g.Text(check.Label)},
				{Content: g.Text(strings.ToUpper(check.Status))},
				{Content: g.Text(fallback(detail, "none"))},
			},
		})
	}
	return gothtable.Table(gothtable.Props{
		ClassMode:  classmode.ClassReplace,
		Class:      "table-wrap",
		TableClass: "table",
		Columns: []gothtable.Column{
			{Header: "Check"},
			{Header: "Status"},
			{Header: "Detail"},
		},
		Rows: rows,
	})
}

// Dashboard renders the HTMX-replaceable dashboard fragment.
func Dashboard(snapshot domain.Snapshot) g.Node {
	return h.Main(
		h.ID("dashboard-root"),
		h.Class("dashboard-root"),
		h.Data("testid", "dashboard-root"),
		g.Attr("data-live-refresh-mode", "fragment"),
		g.Attr("hx-get", "/"),
		g.Attr("hx-trigger", "colin:refresh"),
		g.Attr("hx-target", "#dashboard-root"),
		g.Attr("hx-swap", "outerHTML"),
		toolbar(snapshot),
		shutdownAlert(snapshot),
		statsGrid(snapshot),
		h.Div(
			h.Class("stack"),
			stateCountsPanel(snapshot),
			runningPanel(snapshot),
			retryingPanel(snapshot),
			rateLimitsPanel(snapshot),
		),
	)
}

func shutdownAlert(snapshot domain.Snapshot) g.Node {
	if !snapshot.ShutdownRequested {
		return nil
	}

	return colinAlertWith(gothalert.Props{
		Class:      "alert alert-warning",
		DataTestID: "shutdown-alert",
		Attributes: []g.Node{
			g.Attr("aria-live", "polite"),
		},
	},
		h.Div(
			h.Class("alert-header"),
			colinBadgeWith(gothbadge.Props{Class: "badge badge-warning", DataTestID: "shutdown-alert-badge"}, g.Text("Warning")),
			h.Div(h.Class("alert-title"), g.Text("Shutdown in progress")),
		),
		h.P(g.Text("Shutdown has begun. Colin will not start new work, and active workers are draining before exit.")),
	)
}

func toolbar(snapshot domain.Snapshot) g.Node {
	generatedAt := snapshot.GeneratedAt.UTC().Format(time.RFC3339)
	return colinCard("card dashboard-toolbar",
		h.Div(
			h.Class("dashboard-title"),
			h.H2(g.Text("Current task surface")),
			h.P(g.Text("HTMX keeps this fragment fresh without reloading the full page shell.")),
		),
		h.Div(
			h.Class("toolbar-actions"),
			colinBadgeWith(
				gothbadge.Props{
					Class:      "badge badge-success",
					DataTestID: "refresh-status",
					Attributes: []g.Node{
						g.Attr("data-refresh-status", "live"),
						g.Attr("data-generated-at", generatedAt),
						g.Attr("aria-live", "polite"),
						g.Attr("title", "Last successful update at "+generatedAt),
					},
				},
				g.Text("Live data"),
			),
			colinBadgeWith(
				gothbadge.Props{
					Class:      "badge badge-accent",
					DataTestID: "snapshot-age",
					Attributes: []g.Node{
						g.Attr("data-data-age", "true"),
						g.Attr("data-generated-at", generatedAt),
						g.Attr("title", "Snapshot generated at "+generatedAt),
					},
				},
				g.Text("0s old"),
			),
			colinButtonWith(
				gothbutton.Props{
					Class:      "btn refresh-toggle",
					DataTestID: "refresh-button",
					Attributes: []g.Node{
						g.Attr("data-refresh-toggle", "true"),
						g.Attr("aria-label", "Pause automatic refresh"),
						g.Attr("title", "Pause automatic refresh"),
					},
				},
				g.Text("❚❚"),
			),
		),
	)
}

func statsGrid(snapshot domain.Snapshot) g.Node {
	return colinCard("stats",
		statCard("Running", strconv.Itoa(snapshot.Counts["running"]), "active issue workspaces"),
		statCard("Retrying", strconv.Itoa(snapshot.Counts["retrying"]), "queued follow-up attempts"),
		statCard("Total Tokens", formatInt(snapshot.CodexTotals.TotalTokens), "aggregate across active runs"),
		statCard("Runtime", formatRuntimeSeconds(snapshot.CodexTotals.SecondsRunning), "combined wall clock"),
	)
}

func stateCountsPanel(snapshot domain.Snapshot) g.Node {
	return colinCardWith(
		gothcard.Props{Class: "table-card", DataTestID: "linear-state-counts"},
		h.H3(g.Text("Linear issues")),
		h.P(g.Text("Tracked Linear issues in the active handoff pipeline.")),
		h.Div(
			h.Class("state-count-grid"),
			g.Map(domain.DashboardStateNames(), func(state string) g.Node {
				return stateCountCard(state, snapshot.IssueStates[state], snapshot.StateIssues[state], snapshot.PausedIssueStates[state])
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
	return colinCard("stat",
		h.Div(h.Class("stat-title"), g.Text(title)),
		h.Div(h.Class("stat-value"), g.Text(value)),
		h.Div(h.Class("stat-desc"), g.Text(desc)),
	)
}

func stateCountCard(state string, total int, issues []domain.StateIssueSummary, paused domain.PausedStateSummary) g.Node {
	return colinCard("stat state-card",
		h.Div(h.Class("stat-title"), g.Text(state)),
		stateCountValue(state, total, issues),
		h.Div(h.Class("stat-desc"), g.Text(stateDescription(state))),
		pausedIndicator(state, paused),
	)
}

func stateCountValue(state string, total int, issues []domain.StateIssueSummary) g.Node {
	if len(issues) == 0 {
		return h.Div(h.Class("stat-value"), g.Text(strconv.Itoa(total)))
	}

	return h.Div(
		h.Class("stat-value"),
		stateIssuesPopover(state, total, issues),
	)
}

func stateIssuesPopover(state string, total int, issues []domain.StateIssueSummary) g.Node {
	slug := stateSlug(state)
	label := issueCountLabel(len(issues))

	return h.Details(
		h.ID("state-issues-details-"+slug),
		h.Class("state-issues-popup"),
		g.Attr("data-preserve-open", "true"),
		h.Summary(
			h.Class("state-issues-trigger"),
			h.Data("testid", "state-issues-trigger-"+slug),
			g.Text(strconv.Itoa(total)),
		),
		h.Div(
			h.Class("state-issues-panel"),
			h.Data("testid", "state-issues-"+slug),
			h.Div(
				h.Class("state-issues-header"),
				colinBadge("badge badge-info", g.Text(state)),
				h.Span(h.Class("state-issues-count"), g.Text(label)),
			),
			renderStateIssueList(state, issues),
		),
	)
}

func issueCountLabel(total int) string {
	if total == 1 {
		return "1 issue"
	}
	return fmt.Sprintf("%d issues", total)
}

func renderStateIssueList(state string, issues []domain.StateIssueSummary) g.Node {
	if len(issues) == 0 {
		return h.P(
			h.Class("state-issues-empty"),
			g.Text("No issues are currently in this state."),
		)
	}

	slug := stateSlug(state)
	return h.Div(
		h.Class("table-wrap state-issues-table-wrap"),
		h.Table(
			h.Class("table state-issues-table"),
			h.THead(
				h.Tr(
					h.Th(g.Text("Issue ID")),
					h.Th(g.Text("Project")),
					h.Th(g.Text("Title")),
				),
			),
			h.TBody(
				g.Map(issues, func(issue domain.StateIssueSummary) g.Node {
					title := fallback(issue.Title, issue.Identifier)

					issueID := g.Node(g.Text(issue.Identifier))
					if strings.TrimSpace(issue.URL) != "" {
						issueID = h.A(h.Class("state-issue-id-link"), h.Href(issue.URL), g.Text(issue.Identifier))
					}

					issueTitle := g.Node(g.Text(title))
					if strings.TrimSpace(issue.ID) != "" {
						issueTitle = h.A(h.Class("state-issue-title-link"), h.Href(domain.ColinMetadataPath(issue.ID)), g.Text(title))
					}

					return h.Tr(
						h.Class("state-issue-row"),
						h.Data("testid", "state-issue-"+slug+"-"+issue.Identifier),
						h.Td(issueID),
						h.Td(g.Text(projectLabel(issue))),
						h.Td(issueTitle),
					)
				}),
			),
		),
	)
}

func projectLabel(issue domain.StateIssueSummary) string {
	if label := projectLabelValue(issue.ProjectName); label != "" {
		return label
	}
	if label := projectLabelValue(issue.ProjectSlug); label != "" {
		return label
	}
	return "unknown"
}

func projectLabelValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 13 && value[len(value)-13] == '-' && isHex(value[len(value)-12:]) {
		return strings.TrimSpace(value[:len(value)-13])
	}
	if len(value) == 12 && isHex(value) {
		return ""
	}
	return value
}

func isHex(value string) bool {
	if len(value) == 0 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
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
	return metadataStatCardContent(title, h.Div(h.Class("issue-title"), g.Text(value)))
}

func metadataLinkStatCard(title, href, empty string) g.Node {
	href = strings.TrimSpace(href)
	if href == "" {
		return metadataStatCard(title, empty)
	}
	if !safeExternalURLScheme(href) {
		return metadataStatCard(title, href)
	}
	return metadataStatCardContent(
		title,
		h.A(
			h.Class("issue-title metadata-value-link"),
			h.Href(href),
			g.Text(href),
		),
	)
}

func safeExternalURLScheme(raw string) bool {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return true
	default:
		return false
	}
}

func metadataStatCardContent(title string, content g.Node) g.Node {
	return colinCard("card",
		h.Div(h.Class("stat-title"), g.Text(title)),
		content,
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

	return colinCardWith(
		gothcard.Props{Class: "table-card", DataTestID: "running-panel"},
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
	rows := make([]gothtable.Row, 0, len(snapshot.Retrying))
	for _, entry := range snapshot.Retrying {
		rows = append(rows, gothtable.Row{
			DataTestID: "retry-row-" + entry.Identifier,
			CellItems: []gothtable.Cell{
				{Content: colinBadge("badge badge-warning", g.Text(entry.Identifier))},
				{Content: g.Text(strconv.Itoa(entry.Attempt))},
				{Content: g.Text(formatDuration(entry.DueAt.Sub(now)))},
				{Content: g.Text(fallback(entry.Error, "waiting for next attempt"))},
			},
		})
	}
	return colinCardWith(
		gothcard.Props{Class: "table-card", DataTestID: "retry-panel"},
		h.H3(g.Text("Retry queue")),
		h.P(g.Text("Queued issues waiting for the next retry window or open slot.")),
		gothtable.Table(gothtable.Props{
			ClassMode:  classmode.ClassReplace,
			Class:      "table-wrap",
			TableClass: "table",
			Columns: []gothtable.Column{
				{Header: "Issue"},
				{Header: "Attempt"},
				{Header: "Due"},
				{Header: "Reason"},
			},
			Rows: rows,
		}),
	)
}

func rateLimitsPanel(snapshot domain.Snapshot) g.Node {
	codexLimits, linearLimits := rateLimitRows(snapshot.GeneratedAt, snapshot.RateLimits)

	return colinCard("table-card",
		h.H3(g.Text("Rate limits")),
		h.P(g.Text("Latest limits reported by Codex and Linear.")),
		h.Div(
			h.Class("rate-limit-grid"),
			rateLimitBox("Codex", renderRateLimitRows("rate-limits-codex", codexLimits)),
			rateLimitBox("Linear", renderRateLimitRows("rate-limits-linear", linearLimits)),
		),
	)
}

func rateLimitBox(title string, content g.Node) g.Node {
	return h.Div(
		h.Class("rate-limit-box"),
		h.H4(g.Text(title)),
		content,
	)
}

type rateLimitProgressRow struct {
	TestIDPart  string
	AriaLabel   string
	Label       string
	UsedLabel   string
	UsedPercent *int
	ResetIn     string
	Detail      string
}

func renderRateLimitRows(testID string, rows []rateLimitProgressRow) g.Node {
	if len(rows) == 0 {
		return renderRateLimitLines(testID, nil)
	}

	items := make(g.Group, 0, len(rows))
	for _, row := range rows {
		barClass := "rate-limit-progress-bar"
		barNodes := g.Group{
			g.Attr("role", "progressbar"),
			g.Attr("aria-label", row.AriaLabel),
			g.Attr("aria-valuemin", "0"),
			g.Attr("aria-valuemax", "100"),
			g.Attr("aria-valuetext", row.UsedLabel),
		}
		if row.UsedPercent != nil {
			barNodes = append(barNodes, g.Attr("aria-valuenow", strconv.Itoa(*row.UsedPercent)))
			barNodes = append(barNodes, h.Div(
				h.Class("rate-limit-progress-fill"),
				g.Attr("style", fmt.Sprintf("width: %d%%;", *row.UsedPercent)),
			))
		} else {
			barClass += " rate-limit-progress-bar-unknown"
		}

		items = append(items, h.Div(
			h.Class("rate-limit-progress-row"),
			h.Data("testid", testID+"-"+row.TestIDPart),
			h.Div(
				h.Class("rate-limit-progress-meta"),
				h.Span(h.Class("rate-limit-progress-window"), g.Text(row.Label)),
				h.Span(h.Class("rate-limit-progress-used"), g.Text(row.UsedLabel)),
				h.Span(h.Class("rate-limit-progress-reset"), g.Text("resets in "+row.ResetIn)),
			),
			h.Div(append(g.Group{h.Class(barClass)}, barNodes...)...),
			renderRateLimitDetail(row.Detail),
		))
	}

	return h.Div(
		h.Class("rate-limit-progress-list"),
		h.Data("testid", testID),
		items,
	)
}

func renderRateLimitDetail(detail string) g.Node {
	if strings.TrimSpace(detail) == "" {
		return nil
	}
	return h.Span(h.Class("rate-limit-progress-detail"), g.Text(detail))
}

func renderRateLimitLines(testID string, lines []string) g.Node {
	return h.Pre(
		h.Class("mockup-code"),
		h.Data("testid", testID),
		g.Text(strings.Join(fallbackLines(lines), "\n")),
	)
}

func rateLimitRows(now time.Time, rateLimits domain.RateLimitSnapshot) ([]rateLimitProgressRow, []rateLimitProgressRow) {
	if len(rateLimits) == 0 {
		return nil, nil
	}

	codexRows := make([]rateLimitProgressRow, 0, 2)
	for _, name := range []string{"primary", "secondary"} {
		limit, ok := rateLimits[name]
		if !ok {
			continue
		}
		window := rateLimitWindow(limit.WindowDurationMinutes)
		row := rateLimitProgressRow{
			TestIDPart: name,
			AriaLabel:  "Codex " + window + " window used",
			Label:      window,
			UsedLabel:  "usage unavailable",
			ResetIn:    rateLimitResetDuration(now, limit.ResetsAt),
		}
		if usedPercent, ok := rateLimitUsedPercent(limit.UsedPercent); ok {
			row.UsedPercent = intPtr(usedPercent)
			row.UsedLabel = fmt.Sprintf("%d%% used", usedPercent)
		}
		codexRows = append(codexRows, row)
	}

	linearRows := make([]rateLimitProgressRow, 0, len(rateLimits))
	for name, limit := range rateLimits {
		if name == "primary" || name == "secondary" {
			continue
		}
		remaining, limitValue, ok := rateLimitRemaining(limit)
		if !ok || limitValue <= 0 {
			continue
		}
		displayName := rateLimitDisplayName(name)
		detail := ""
		if nextAllowedAt, ok := rateLimitNextAllowed(limit); ok && nextAllowedAt.After(now) {
			detail = fmt.Sprintf("next request in %s", formatDuration(nextAllowedAt.Sub(now)))
		}
		usedPercent := 100 - int(math.Round((float64(remaining)/float64(limitValue))*100))
		if usedPercent < 0 {
			usedPercent = 0
		}
		if usedPercent > 100 {
			usedPercent = 100
		}
		linearRows = append(linearRows, rateLimitProgressRow{
			TestIDPart:  name,
			AriaLabel:   "Linear " + strings.ToLower(displayName) + " used",
			Label:       displayName,
			UsedLabel:   fmt.Sprintf("%d%% used", usedPercent),
			UsedPercent: intPtr(usedPercent),
			ResetIn:     rateLimitResetDuration(now, limit.ResetsAt),
			Detail:      detail,
		})
	}
	sort.Slice(linearRows, func(i, j int) bool {
		return linearRows[i].Label < linearRows[j].Label
	})
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
	return colinAlertWith(gothalert.Props{Class: "alert empty-state"},
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
		colinBadge("badge badge-info", g.Text(entry.Identifier)),
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
			colinBadge(stateBadgeClass(entry.State), g.Text(entry.State)),
		),
		h.Div(
			h.Class("worker-metrics"),
			colinCard("card",
				h.H3(g.Text("Session")),
				h.Div(h.Class("metric-line"), g.Text("Session: "+fallback(entry.SessionID, "not assigned"))),
				h.Div(h.Class("metric-line"), g.Text("Turns: "+strconv.Itoa(entry.TurnCount))),
				h.Div(h.Class("metric-line"), g.Text("Started: "+entry.StartedAt.Format(time.RFC3339))),
			),
			colinCard("card",
				h.H3(g.Text("Activity")),
				h.Div(h.Class("metric-line"), g.Text(fallback(entry.LastMessage, "no message"))),
				h.Div(h.Class("metric-line"), g.Text("Last event at: "+lastEventAt)),
			),
			colinCard("card",
				h.H3(g.Text("Usage")),
				h.Div(h.Class("metric-line"), h.Data("testid", "turn-count-"+entry.Identifier), g.Text("Turns: "+strconv.Itoa(entry.TurnCount))),
				h.Div(h.Class("metric-line"), g.Text("Input: "+formatInt(entry.InputTokens))),
				h.Div(h.Class("metric-line"), g.Text("Output: "+formatInt(entry.OutputTokens))),
				h.Div(h.Class("metric-line"), g.Text("Total: "+formatInt(entry.TotalTokens))),
				renderContextWindowUsage(entry),
			),
		),
		workerOutput(entry),
	)
}

func renderContextWindowUsage(entry domain.SnapshotRunning) g.Node {
	label := "Context window: unavailable"
	if entry.ContextWindow == nil || entry.ContextWindow.LimitTokens <= 0 {
		return h.Div(
			h.Class("context-window-usage"),
			h.Div(
				h.Class("metric-line"),
				h.Class("context-window-label"),
				h.Data("testid", "context-window-"+entry.Identifier),
				g.Text(label),
			),
		)
	}

	usedPercent := contextWindowUsedPercent(entry.ContextWindow)
	leftPercent := 100 - usedPercent
	label = fmt.Sprintf(
		"Context window: %d%% left (%s used / %s)",
		leftPercent,
		formatCompactTokens(entry.ContextWindow.UsedTokens),
		formatCompactTokens(entry.ContextWindow.LimitTokens),
	)
	return h.Div(
		h.Class("context-window-usage"),
		h.Div(
			h.Class("metric-line"),
			h.Class("context-window-label"),
			h.Data("testid", "context-window-"+entry.Identifier),
			g.Text(label),
		),
		h.Div(
			h.Class("context-window-bar"),
			h.Data("testid", "context-window-bar-"+entry.Identifier),
			g.Attr("role", "progressbar"),
			g.Attr("aria-label", "Context window used"),
			g.Attr("aria-valuemin", "0"),
			g.Attr("aria-valuemax", "100"),
			g.Attr("aria-valuenow", strconv.Itoa(usedPercent)),
			h.Div(
				h.Class("context-window-bar-fill"),
				g.Attr("style", fmt.Sprintf("width: %d%%;", usedPercent)),
			),
		),
	)
}

func workerOutput(entry domain.SnapshotRunning) g.Node {
	loadPath := domain.ColinCodexOutputPath(entry.IssueID)
	eventsPath := domain.ColinCodexOutputEventsPath(entry.IssueID)
	return h.Details(
		h.ID("worker-output-details-"+entry.Identifier),
		h.Class("worker-output"),
		g.Attr("data-preserve-open", "true"),
		g.Attr("data-codex-output-panel", "true"),
		g.Attr("data-codex-output-issue-id", entry.IssueID),
		g.Attr("data-codex-output-load-url", loadPath),
		g.Attr("data-codex-output-events-url", eventsPath),
		g.Attr("data-codex-output-loaded", "false"),
		g.Attr("data-codex-output-cursor", ""),
		h.Summary(g.Text("Codex output")),
		workerOutputPlaceholder(entry.Identifier),
	)
}

// WorkerOutputList renders the dashboard or metadata output list container.
func WorkerOutputList(identifier string, log []domain.OutputLog) g.Node {
	return h.Div(
		h.Class("worker-output-list"),
		h.Data("testid", "worker-output-"+identifier),
		g.Attr("data-codex-output-body", "true"),
		g.Attr("data-codex-output-cursor", outputCursor(log)),
		renderOutputEntries(log),
	)
}

// WorkerOutputEntry renders one output entry node.
func WorkerOutputEntry(item domain.OutputLog) g.Node {
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
			colinBadge(outputEventBadgeClass(item.Event), g.Text(item.Event)),
		),
	}
	if outputMessageAddsDetail(item.Event, message) {
		entryChildren = append(entryChildren, h.Pre(
			h.Class("mockup-code"),
			g.Text(message),
		))
	}
	return h.Div(
		h.Class("worker-output-entry"),
		entryChildren,
	)
}

func workerOutputPlaceholder(identifier string) g.Node {
	return h.Div(
		h.Class("worker-output-list"),
		h.Data("testid", "worker-output-"+identifier),
		g.Attr("data-codex-output-body", "true"),
		g.Attr("data-codex-output-cursor", ""),
		h.Pre(
			h.Class("mockup-code"),
			g.Text("Open to load Codex output."),
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
		entries = append(entries, WorkerOutputEntry(log[i]))
	}

	return entries
}

func outputCursor(log []domain.OutputLog) string {
	if len(log) == 0 {
		return ""
	}
	item := log[len(log)-1]
	return item.Timestamp.UTC().Format(time.RFC3339Nano) + "|" + item.Event + "|" + item.Message
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
	formatted := strconv.FormatInt(value, 10)
	start := 0
	if strings.HasPrefix(formatted, "-") {
		start = 1
	}
	digits := len(formatted) - start
	if digits <= 3 {
		return formatted
	}

	var builder strings.Builder
	builder.Grow(len(formatted) + ((digits - 1) / 3))
	if start == 1 {
		builder.WriteByte('-')
	}

	prefix := digits % 3
	if prefix == 0 {
		prefix = 3
	}
	builder.WriteString(formatted[start : start+prefix])
	for i := start + prefix; i < len(formatted); i += 3 {
		builder.WriteByte(',')
		builder.WriteString(formatted[i : i+3])
	}
	return builder.String()
}

func formatCompactTokens(value int64) string {
	type unit struct {
		suffix string
		size   float64
	}
	units := []unit{
		{suffix: "T", size: 1_000_000_000_000},
		{suffix: "B", size: 1_000_000_000},
		{suffix: "M", size: 1_000_000},
		{suffix: "K", size: 1_000},
	}
	absValue := math.Abs(float64(value))
	for _, current := range units {
		if absValue < current.size {
			continue
		}
		scaled := float64(value) / current.size
		rounded := math.Round(scaled*10) / 10
		if math.Abs(rounded) >= 100 || rounded == math.Trunc(rounded) {
			return fmt.Sprintf("%.0f%s", rounded, current.suffix)
		}
		return fmt.Sprintf("%.1f%s", rounded, current.suffix)
	}
	return strconv.FormatInt(value, 10)
}

func contextWindowUsedPercent(window *domain.ContextWindowUsage) int {
	if window == nil || window.LimitTokens <= 0 {
		return 0
	}
	used := float64(window.UsedTokens)
	limit := float64(window.LimitTokens)
	percent := int(math.Round((used / limit) * 100))
	switch {
	case percent < 0:
		return 0
	case percent > 100:
		return 100
	default:
		return percent
	}
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return value.Round(time.Second).String()
}

func formatRuntimeSeconds(value float64) string {
	if value <= 0 {
		return "0m"
	}
	return formatCompactDuration(time.Duration(math.Round(value)) * time.Second)
}

func rateLimitResetDuration(now time.Time, value *time.Time) string {
	if value == nil {
		return "unknown"
	}
	return formatCompactDuration(value.Sub(now))
}

func rateLimitUsedPercent(value *int64) (int, bool) {
	if value == nil {
		return 0, false
	}
	percent := int(*value)
	switch {
	case percent < 0:
		return 0, true
	case percent > 100:
		return 100, true
	default:
		return percent, true
	}
}

func intPtr(value int) *int {
	return &value
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

func rateLimitDisplayName(name string) string {
	label := strings.TrimSpace(strings.TrimPrefix(name, "linear_"))
	label = strings.ReplaceAll(label, "_", " ")
	if label == "" {
		return "Unknown"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func fallback(value, otherwise string) string {
	if strings.TrimSpace(value) == "" {
		return otherwise
	}
	return value
}
