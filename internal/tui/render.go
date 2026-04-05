package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/pmenglund/colin/internal/domain"
)

var (
	pageStyle         = lipgloss.NewStyle().Padding(1, 2)
	titleStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	subtleStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	labelStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	healthyURLStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warnStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	shutdownStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208"))
	errorStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	infoStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	debugStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	successStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	waitingStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	runningStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	logTimestampStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	logFieldStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	selectedLogStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4"))
)

func renderOverviewView(m model) string {
	lines := []string{
		titleStyle.Render("Colin runtime dashboard"),
		renderHeaderStatus(m),
		"",
		labelStyle.Render("URLs"),
		renderURLLine("dashboard", dashboardDisplayURL(m)),
		renderURLLine("linear hook", m.setup.LinearWebhookURL),
		renderURLLine("github hook", m.setup.GitHubWebhookURL),
		"",
		labelStyle.Render("Workflow"),
		renderWorkflowPathLine(m.snapshot.WorkflowPath),
	}
	lines = append(lines, renderTargetTable(m.snapshot.Targets, maxInt(m.width, defaultWidth))...)
	lines = append(lines,
		"",
		labelStyle.Render("Integrations"),
		renderSlackSocketModeLine(m.snapshot.SlackSocketMode),
	)
	lines = append(lines, renderWebhookStatusLines(m.snapshot.Webhooks)...)

	if m.refreshErr != nil {
		lines = append(lines, errorStyle.Render("refresh error: "+m.refreshErr.Error()))
	}

	lines = append(lines, "", labelStyle.Render("Workers"))
	if len(m.snapshot.Running) == 0 {
		lines = append(lines, subtleStyle.Render("No running workers."))
	} else {
		for _, worker := range m.snapshot.Running {
			lines = append(lines, renderWorkerLine(worker, m.width))
		}
	}
	if notice := renderShutdownNotice(m); notice != "" {
		lines = append(lines, "", notice)
	}

	if len(m.snapshot.Retrying) > 0 {
		lines = append(lines, "", labelStyle.Render("Retries"))
		for _, retry := range m.snapshot.Retrying {
			lines = append(lines, renderRetryLine(retry))
		}
	}

	return pageStyle.Width(maxInt(m.width, defaultWidth)).Render(strings.Join(lines, "\n"))
}

func renderLogsView(m model) string {
	filteredEntries := m.filteredLogEntries()
	lines := []string{
		titleStyle.Render("Colin logs"),
		renderHeaderStatus(m),
		"",
	}

	visible := m.visibleLogLines()
	start := clampInt(m.logOffset, 0, maxInt(len(filteredEntries)-visible, 0))
	end := start + visible
	if end > len(filteredEntries) {
		end = len(filteredEntries)
	}
	if len(filteredEntries) == 0 {
		lines = append(lines, subtleStyle.Render("No logs match the current filter."))
	} else {
		contentWidth := maxInt(m.width-4, 20)
		for idx := start; idx < end; idx++ {
			line := truncateRunes(renderLogLine(filteredEntries[idx]), contentWidth)
			if idx == m.selectedLog {
				line = selectedLogStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "", renderSelectedLogDetails(m))
	lines = append(lines, "", subtleStyle.Render(fmt.Sprintf("Filter %s  |  Showing %d-%d of %d  |  selected %s  |  f cycle filter  |  ↑/↓ select  PgUp/PgDn page  Home/End jump", m.logFilter.label(), minInt(start+1, len(filteredEntries)), end, len(filteredEntries), renderSelectedLogPosition(m))))
	if notice := renderShutdownNotice(m); notice != "" {
		lines = append(lines, "", notice)
	}
	return pageStyle.Width(maxInt(m.width, defaultWidth)).Render(strings.Join(lines, "\n"))
}

func renderHeaderStatus(m model) string {
	parts := []string{
		subtleStyle.Render("l logs"),
		subtleStyle.Render("q/esc quit"),
	}
	if m.mode == modeLogs {
		parts = append(parts, subtleStyle.Render("f "+m.logFilter.label()))
	}
	if m.hasUnseenLogAlerts() {
		parts = append(parts, warnStyle.Render("warn/err in logs"))
	}
	if !m.lastRefresh.IsZero() {
		parts = append(parts, subtleStyle.Render("refreshed "+humanizeAge(m.lastRefresh)))
	}
	return strings.Join(parts, "  ")
}

func renderShutdownNotice(m model) string {
	if !m.shutdownRequested {
		return ""
	}

	running := len(m.snapshot.Running)
	switch {
	case m.forceStopIssued:
		return shutdownStyle.Render("shutting down")
	case running > 0:
		return shutdownStyle.Render(fmt.Sprintf("shutdown requested; waiting for %d %s to go idle  press q again to exit immediately", running, pluralizeWorker(running)))
	default:
		return shutdownStyle.Render("shutdown requested; workers are idle, exiting")
	}
}

func dashboardDisplayURL(m model) string {
	if strings.TrimSpace(m.setup.TailnetUIBaseURL) != "" {
		return m.setup.TailnetUIBaseURL
	}
	return m.dashboardURL
}

func renderURLLine(label string, value string) string {
	name := subtleStyle.Render(padRight(label, 11))
	if strings.TrimSpace(value) == "" {
		return name + " " + subtleStyle.Render("not available")
	}
	return name + " " + healthyURLStyle.Render(value)
}

func renderWorkflowPathLine(path string) string {
	name := subtleStyle.Render(padRight("file", 11))
	if strings.TrimSpace(path) == "" {
		return name + " " + subtleStyle.Render("not available")
	}
	return name + " " + healthyURLStyle.Render(path)
}

func renderTargetTable(targets []domain.SnapshotTarget, width int) []string {
	if len(targets) == 0 {
		return []string{subtleStyle.Render("No targets configured.")}
	}

	const (
		nameMaxWidth    = 18
		projectMaxWidth = 20
		baseMaxWidth    = 12
	)

	nameWidth := len("name")
	projectWidth := len("project")
	baseWidth := len("base")
	for _, target := range targets {
		nameWidth = minInt(maxInt(nameWidth, len([]rune(strings.TrimSpace(target.Name)))), nameMaxWidth)
		projectWidth = minInt(maxInt(projectWidth, len([]rune(strings.TrimSpace(target.ProjectSlug)))), projectMaxWidth)
		baseWidth = minInt(maxInt(baseWidth, len([]rune(strings.TrimSpace(target.BaseRef)))), baseMaxWidth)
	}

	repoHeader := "repo"
	repoWidth := maxInt(width-nameWidth-projectWidth-baseWidth-10, len(repoHeader))
	renderRow := func(name string, project string, base string, repo string) string {
		cols := []string{
			padRight(truncateRunes(name, nameWidth), nameWidth),
			padRight(truncateRunes(project, projectWidth), projectWidth),
			padRight(truncateRunes(base, baseWidth), baseWidth),
			truncateRunes(repo, repoWidth),
		}
		return strings.Join(cols, "  ")
	}

	lines := []string{
		subtleStyle.Render(renderRow("name", "project", "base", repoHeader)),
	}
	for _, target := range targets {
		lines = append(lines, renderRow(
			strings.TrimSpace(target.Name),
			strings.TrimSpace(target.ProjectSlug),
			strings.TrimSpace(target.BaseRef),
			strings.TrimSpace(target.RepoURL),
		))
	}
	return lines
}

func renderSlackSocketModeLine(status domain.SlackSocketModeStatus) string {
	name := subtleStyle.Render(padRight("slack ws", 15))
	switch {
	case status.Connected:
		line := successStyle.Render("connected")
		if !status.LastEventAt.IsZero() {
			line += subtleStyle.Render(" " + humanizeAge(status.LastEventAt))
		}
		return name + " " + line
	case !status.Enabled:
		return name + " " + subtleStyle.Render("disabled")
	case strings.TrimSpace(status.LastError) != "":
		return name + " " + errorStyle.Render(status.State+": "+status.LastError)
	default:
		state := strings.TrimSpace(status.State)
		if state == "" {
			state = "connecting"
		}
		line := warnStyle.Render(state)
		if !status.LastEventAt.IsZero() {
			line += subtleStyle.Render(" " + humanizeAge(status.LastEventAt))
		}
		return name + " " + line
	}
}

func renderSlackSocketModeDetails(status domain.SlackSocketModeStatus) []string {
	_ = status
	return nil
}

func renderWebhookStatusLines(webhooks map[string]domain.WebhookStatus) []string {
	return []string{
		renderWebhookStatusLine("slack webhook", webhooks["slack"]),
		renderWebhookStatusLine("linear webhook", webhooks["linear"]),
	}
}

func renderWebhookStatusLine(label string, status domain.WebhookStatus) string {
	name := subtleStyle.Render(padRight(label, 15))
	if status.LastMessageAt.IsZero() {
		return name + " " + subtleStyle.Render("no messages yet")
	}
	return name + " " + subtleStyle.Render("last msg "+humanizeAge(status.LastMessageAt))
}

func renderWorkerLine(worker domain.SnapshotRunning, width int) string {
	state := renderState(worker.State)
	status := renderWorkerRuntime(worker.StartedAt)
	return fmt.Sprintf(
		"%s  %s  %s  %s  %s",
		titleStyle.Render(padRight(worker.Identifier, 12)),
		state,
		subtleStyle.Render(fmt.Sprintf("turn %d", worker.TurnCount)),
		subtleStyle.Render(worker.SessionID),
		subtleStyle.Render(truncateRunes(status, maxInt(width-56, 16))),
	)
}

func renderWorkerRuntime(startedAt time.Time) string {
	if startedAt.IsZero() {
		return "running time unknown"
	}
	elapsed := time.Since(startedAt).Round(time.Second)
	if elapsed < 0 {
		elapsed = 0
	}
	return "running " + elapsed.String()
}

func renderRetryLine(retry domain.RetryEntry) string {
	dueIn := retry.DueAt.Sub(time.Now().UTC()).Round(time.Second)
	return fmt.Sprintf(
		"%s  %s  %s",
		titleStyle.Render(padRight(retry.Identifier, 12)),
		waitingStyle.Render(fmt.Sprintf("attempt %d", retry.Attempt)),
		subtleStyle.Render(fmt.Sprintf("due in %s", dueIn)),
	)
}

func renderLogLine(entry domain.BufferedLogEntry) string {
	levelStyle := infoStyle
	switch strings.ToUpper(entry.Level) {
	case "ERROR":
		levelStyle = errorStyle
	case "WARN":
		levelStyle = warnStyle
	case "INFO":
		levelStyle = infoStyle
	case "DEBUG":
		levelStyle = debugStyle
	}

	fields := make([]string, 0, len(entry.Fields))
	for _, field := range entry.Fields {
		fields = append(fields, sanitizeLogText(field))
	}

	line := []string{
		logTimestampStyle.Render(entry.Timestamp.Local().Format("15:04:05")),
		levelStyle.Render(strings.ToUpper(entry.Level)),
		sanitizeLogText(entry.Message),
	}
	if len(fields) > 0 {
		line = append(line, logFieldStyle.Render(strings.Join(fields, " ")))
	}
	return strings.Join(line, "  ")
}

func sanitizeLogText(value string) string {
	replacer := strings.NewReplacer(
		"\r\n", `\n`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return replacer.Replace(value)
}

func renderSelectedLogDetails(m model) string {
	filteredEntries := m.filteredLogEntries()
	if len(filteredEntries) == 0 || m.selectedLog < 0 || m.selectedLog >= len(filteredEntries) {
		return subtleStyle.Render("Selected entry: none")
	}

	contentWidth := maxInt(m.width-4, 20)
	label := labelStyle.Render(fmt.Sprintf("Selected log %d/%d", m.selectedLog+1, len(filteredEntries)))
	detail := lipgloss.NewStyle().Width(contentWidth).MaxWidth(contentWidth).Render(renderLogLine(filteredEntries[m.selectedLog]))
	return label + "\n" + detail
}

func renderSelectedLogPosition(m model) string {
	filteredEntries := m.filteredLogEntries()
	if len(filteredEntries) == 0 || m.selectedLog < 0 {
		return "none"
	}
	return fmt.Sprintf("%d/%d", m.selectedLog+1, len(filteredEntries))
}

func renderState(state string) string {
	normalized := strings.ToLower(strings.TrimSpace(state))
	switch {
	case normalized == "":
		return subtleStyle.Render("unknown")
	case strings.Contains(normalized, "todo"):
		return waitingStyle.Render(state)
	case strings.Contains(normalized, "progress"):
		return runningStyle.Render(state)
	case strings.Contains(normalized, "review"), strings.Contains(normalized, "merge"):
		return infoStyle.Render(state)
	case strings.Contains(normalized, "done"), strings.Contains(normalized, "closed"), strings.Contains(normalized, "cancel"):
		return successStyle.Render(state)
	default:
		return infoStyle.Render(state)
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func padRight(value string, width int) string {
	if width <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func humanizeAge(ts time.Time) string {
	age := time.Since(ts).Round(time.Second)
	if age < 0 {
		age = 0
	}
	return age.String() + " ago"
}

func pluralizeWorker(count int) string {
	if count == 1 {
		return "worker"
	}
	return "workers"
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
