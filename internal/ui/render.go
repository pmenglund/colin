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
				return statCard(state, strconv.Itoa(snapshot.IssueStates[state]), stateDescription(state))
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

func rateLimitRows(now time.Time, rateLimits map[string]any) ([]string, []string) {
	if len(rateLimits) == 0 {
		return nil, nil
	}

	codexRows := make([]string, 0, 2)
	for _, name := range []string{"primary", "secondary"} {
		limit, ok := nestedMap(rateLimits, name)
		if !ok {
			continue
		}
		codexRows = append(codexRows, fmt.Sprintf(
			"%s of %s window which resets in %s",
			rateLimitUsed(limit["usedPercent"]),
			rateLimitWindow(limit["windowDurationMins"]),
			rateLimitResetDuration(now, limit["resetsAt"]),
		))
	}
	var linearRows []string
	for name, raw := range rateLimits {
		if name == "primary" || name == "secondary" {
			continue
		}
		limit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		remaining, limitValue, ok := rateLimitRemaining(limit)
		if !ok {
			continue
		}
		line := fmt.Sprintf(
			"resets in %s, %d of %d remaining",
			rateLimitResetDuration(now, limit["resetsAt"]),
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

func nestedMap(root map[string]any, key string) (map[string]any, bool) {
	value, ok := root[key]
	if !ok {
		return nil, false
	}
	out, ok := value.(map[string]any)
	return out, ok
}

func rateLimitRemaining(limit map[string]any) (int64, int64, bool) {
	remaining, ok := int64Any(limit["remaining"])
	if !ok {
		return 0, 0, false
	}
	limitValue, ok := int64Any(limit["limit"])
	if !ok {
		return 0, 0, false
	}
	return remaining, limitValue, true
}

func rateLimitNextAllowed(limit map[string]any) (time.Time, bool) {
	unix, ok := int64Any(limit["nextAllowedAt"])
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(unix, 0).UTC(), true
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

func rateLimitResetDuration(now time.Time, value any) string {
	resetAt, ok := parseRateLimitTime(value)
	if !ok {
		return "unknown"
	}
	return formatCompactDuration(resetAt.Sub(now))
}

func rateLimitUsed(value any) string {
	number, ok := numericValue(value)
	if !ok {
		return "unknown used"
	}
	if number == float64(int64(number)) {
		return fmt.Sprintf("%d%% used", int64(number))
	}
	return fmt.Sprintf("%s%% used", strconv.FormatFloat(number, 'f', -1, 64))
}

func rateLimitWindow(value any) string {
	number, ok := numericValue(value)
	if !ok {
		return "unknown"
	}
	minutes := time.Duration(number * float64(time.Minute))
	return formatCompactDuration(minutes)
}

func int64Any(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

func parseRateLimitTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return parsed, true
		}
		if number, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return unixRateLimitTime(number), true
		}
		return time.Time{}, false
	case int:
		return unixRateLimitTime(int64(typed)), true
	case int64:
		return unixRateLimitTime(typed), true
	case int32:
		return unixRateLimitTime(int64(typed)), true
	case float64:
		return unixRateLimitTime(int64(typed)), true
	case float32:
		return unixRateLimitTime(int64(typed)), true
	default:
		return time.Time{}, false
	}
}

func unixRateLimitTime(value int64) time.Time {
	if value >= 1_000_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		number, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
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
