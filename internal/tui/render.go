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
	}

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
	lines := []string{
		titleStyle.Render("Colin logs"),
		renderHeaderStatus(m),
		"",
	}

	visible := m.visibleLogLines()
	start := clampInt(m.logOffset, 0, maxInt(len(m.logs.Entries)-visible, 0))
	end := start + visible
	if end > len(m.logs.Entries) {
		end = len(m.logs.Entries)
	}
	if len(m.logs.Entries) == 0 {
		lines = append(lines, subtleStyle.Render("No buffered logs yet."))
	} else {
		contentWidth := maxInt(m.width-4, 20)
		for idx := start; idx < end; idx++ {
			line := truncateRunes(renderLogLine(m.logs.Entries[idx]), contentWidth)
			if idx == m.selectedLog {
				line = selectedLogStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "", renderSelectedLogDetails(m))
	lines = append(lines, "", subtleStyle.Render(fmt.Sprintf("Showing %d-%d of %d  |  selected %s  |  ↑/↓ select  PgUp/PgDn page  Home/End jump", minInt(start+1, len(m.logs.Entries)), end, len(m.logs.Entries), renderSelectedLogPosition(m))))
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

func renderWorkerLine(worker domain.SnapshotRunning, width int) string {
	state := renderState(worker.State)
	status := renderWorkerRuntime(worker.StartedAt)
	return fmt.Sprintf(
		"%s  %s  %s  %s  %s",
		titleStyle.Render(padRight(worker.Identifier, 12)),
		state,
		subtleStyle.Render(fmt.Sprintf("turn %d", worker.TurnCount)),
		subtleStyle.Render(truncateRunes(worker.SessionID, 18)),
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

	line := []string{
		logTimestampStyle.Render(entry.Timestamp.Local().Format("15:04:05")),
		levelStyle.Render(strings.ToUpper(entry.Level)),
		entry.Message,
	}
	if len(entry.Fields) > 0 {
		line = append(line, logFieldStyle.Render(strings.Join(entry.Fields, " ")))
	}
	return strings.Join(line, "  ")
}

func renderSelectedLogDetails(m model) string {
	if len(m.logs.Entries) == 0 || m.selectedLog < 0 || m.selectedLog >= len(m.logs.Entries) {
		return subtleStyle.Render("Selected entry: none")
	}

	contentWidth := maxInt(m.width-4, 20)
	label := labelStyle.Render(fmt.Sprintf("Selected log %d/%d", m.selectedLog+1, len(m.logs.Entries)))
	detail := lipgloss.NewStyle().Width(contentWidth).MaxWidth(contentWidth).Render(renderLogLine(m.logs.Entries[m.selectedLog]))
	return label + "\n" + detail
}

func renderSelectedLogPosition(m model) string {
	if len(m.logs.Entries) == 0 || m.selectedLog < 0 {
		return "none"
	}
	return fmt.Sprintf("%d/%d", m.selectedLog+1, len(m.logs.Entries))
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
