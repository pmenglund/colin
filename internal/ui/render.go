package ui

import (
	"encoding/json"
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
							h.H1(g.Text("Inspect what Colin is working on right now.")),
							h.P(g.Text("The dashboard shows active issue runs, queued retries, token usage, and the latest orchestrator state without tailing logs.")),
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
			h.Class("content-grid"),
			h.Div(
				h.Class("stack"),
				stateCountsPanel(snapshot),
				runningPanel(snapshot),
				retryingPanel(snapshot),
			),
			h.Div(
				h.Class("stack"),
				rateLimitsPanel(snapshot),
				apiPanel(snapshot),
			),
		),
	)
}

func toolbar(snapshot domain.Snapshot) g.Node {
	return h.Section(
		h.Class("card dashboard-toolbar"),
		h.Div(
			h.Class("dashboard-title"),
			h.H2(g.Text("Current task surface")),
			h.P(g.Text("HTMX keeps this fragment fresh without reloading the full page shell.")),
		),
		h.Div(
			h.Class("toolbar-actions"),
			h.Span(h.Class("badge badge-accent"), h.Data("testid", "snapshot-generated"), g.Text(snapshot.GeneratedAt.Format(time.RFC3339))),
			h.Button(
				h.Type("button"),
				h.Class("btn"),
				h.Data("testid", "refresh-button"),
				g.Attr("hx-get", "/"),
				g.Attr("hx-target", "#dashboard-root"),
				g.Attr("hx-swap", "outerHTML"),
				g.Text("Refresh now"),
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
	if len(snapshot.IssueStates) == 0 {
		return emptyPanel("Linear issue counts", "No tracked issue counts are available yet.")
	}

	type stateCount struct {
		State string
		Count int
	}
	rows := make([]stateCount, 0, len(snapshot.IssueStates))
	total := 0
	for state, count := range snapshot.IssueStates {
		rows = append(rows, stateCount{State: state, Count: count})
		total += count
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].State < rows[j].State
	})

	return h.Section(
		h.Class("table-card"),
		h.Data("testid", "linear-state-counts"),
		h.H3(g.Text("Linear issue counts")),
		h.P(g.Text("Tracked Linear issues grouped by current state.")),
		h.Div(
			h.Class("state-count-grid"),
			statCard("Tracked Issues", strconv.Itoa(total), "issues in the configured Linear state set"),
			g.Map(rows, func(row stateCount) g.Node {
				return statCard(row.State, strconv.Itoa(row.Count), "issues currently in this state")
			}),
		),
	)
}

func statCard(title, value, desc string) g.Node {
	return h.Div(
		h.Class("stat"),
		h.Div(h.Class("stat-title"), g.Text(title)),
		h.Div(h.Class("stat-value"), g.Text(value)),
		h.Div(h.Class("stat-desc"), g.Text(desc)),
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
	body := "none reported"
	if len(snapshot.RateLimits) > 0 {
		pretty, err := json.MarshalIndent(snapshot.RateLimits, "", "  ")
		if err == nil {
			body = string(pretty)
		}
	}

	return h.Section(
		h.Class("table-card"),
		h.H3(g.Text("Rate limits")),
		h.P(g.Text("Latest limits reported by the Codex runner.")),
		h.Pre(h.Class("mockup-code"), h.Data("testid", "rate-limits"), g.Text(body)),
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

func emptyPanel(title, body string) g.Node {
	return h.Section(
		h.Class("alert empty-state"),
		h.H3(g.Text(title)),
		h.P(g.Text(body)),
	)
}

func runningCard(entry domain.SnapshotRunning) g.Node {
	issueNode := g.Group{
		h.Span(h.Class("badge badge-info"), g.Text(entry.Identifier)),
		h.Div(h.Class("issue-title"), g.Text(entry.Title)),
		h.Div(
			h.Class("issue-meta"),
			h.Span(h.Class(stateBadgeClass(entry.State)), g.Text(entry.State)),
		),
	}
	if entry.URL != nil && strings.TrimSpace(*entry.URL) != "" {
		issueNode = g.Group{
			h.A(h.Href(*entry.URL), issueNode),
		}
	}

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
			h.Span(h.Class("badge badge-success"), g.Text(fallback(entry.LastEvent, "none"))),
		),
		h.Div(
			h.Class("worker-metrics"),
			h.Div(
				h.Class("card"),
				h.H3(g.Text("Session")),
				h.Span(g.Text("Session: "+fallback(entry.SessionID, "not assigned"))),
				h.Span(g.Text("Turns: "+strconv.Itoa(entry.TurnCount))),
				h.Span(g.Text("Started: "+entry.StartedAt.Format(time.RFC3339))),
			),
			h.Div(
				h.Class("card"),
				h.H3(g.Text("Activity")),
				h.Span(g.Text(fallback(entry.LastMessage, "no message"))),
				h.Span(g.Text("Last event at: "+lastEventAt)),
			),
			h.Div(
				h.Class("card"),
				h.H3(g.Text("Usage")),
				h.Span(h.Data("testid", "turn-count-"+entry.Identifier), g.Text("Turns: "+strconv.Itoa(entry.TurnCount))),
				h.Span(g.Text("Input: "+formatInt(entry.InputTokens))),
				h.Span(g.Text("Output: "+formatInt(entry.OutputTokens))),
				h.Span(g.Text("Total: "+formatInt(entry.TotalTokens))),
			),
		),
		workerOutput(entry),
	)
}

func workerOutput(entry domain.SnapshotRunning) g.Node {
	lines := make([]string, 0, len(entry.OutputLog))
	for _, item := range entry.OutputLog {
		lines = append(lines, fmt.Sprintf("[%s] %s %s", item.Timestamp.Format("15:04:05"), item.Event, item.Message))
	}
	if len(lines) == 0 {
		lines = append(lines, "No Codex output captured yet.")
	}

	return h.Details(
		h.Class("worker-output"),
		h.Summary(g.Text("Codex output")),
		h.Pre(
			h.Class("mockup-code"),
			h.Data("testid", "worker-output-"+entry.Identifier),
			g.Text(strings.Join(lines, "\n")),
		),
	)
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

func fallback(value, otherwise string) string {
	if strings.TrimSpace(value) == "" {
		return otherwise
	}
	return value
}
