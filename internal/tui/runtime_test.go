package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/pmenglund/colin/internal/domain"
)

type fakeSource struct {
	dashboardURL string
	setupURL     string
	snapshot     domain.Snapshot
	logs         domain.BufferedLogSnapshot
	setup        domain.FunnelSetupStatus
	err          error
}

func (f fakeSource) DashboardURL() string {
	return f.dashboardURL
}

func (f fakeSource) FunnelSetupURL() string {
	return f.setupURL
}

func (f fakeSource) Snapshot(context.Context) (domain.Snapshot, error) {
	return f.snapshot, f.err
}

func (f fakeSource) BufferedLogs(context.Context, *slog.Level) (domain.BufferedLogSnapshot, error) {
	return f.logs, f.err
}

func (f fakeSource) FunnelSetupStatus(context.Context) domain.FunnelSetupStatus {
	return f.setup
}

func TestModelTogglesBetweenOverviewAndLogs(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.logs = sampleLogs(6)
	m.height = 12

	next, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	updated := next.(model)
	if updated.mode != modeLogs {
		t.Fatalf("mode = %v, want modeLogs", updated.mode)
	}
	if updated.selectedLog != len(updated.logs.Entries)-1 {
		t.Fatalf("selectedLog = %d, want %d", updated.selectedLog, len(updated.logs.Entries)-1)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	updated = next.(model)
	if updated.mode != modeOverview {
		t.Fatalf("mode = %v, want modeOverview", updated.mode)
	}
}

func TestModelLogSelectionClampsWithinBounds(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.mode = modeLogs
	m.height = 12
	m.logs = sampleLogs(20)
	m.selectLastLog()

	next, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	updated := next.(model)
	if updated.selectedLog >= len(updated.logs.Entries) {
		t.Fatalf("selectedLog = %d, want within bounds", updated.selectedLog)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	updated = next.(model)
	if updated.selectedLog != 0 {
		t.Fatalf("selectedLog = %d, want 0", updated.selectedLog)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	updated = next.(model)
	if updated.selectedLog != 0 {
		t.Fatalf("selectedLog after up = %d, want 0", updated.selectedLog)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	updated = next.(model)
	lastIndex := len(updated.logs.Entries) - 1
	if updated.selectedLog != lastIndex {
		t.Fatalf("selectedLog after end = %d, want %d", updated.selectedLog, lastIndex)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	updated = next.(model)
	if updated.selectedLog != lastIndex {
		t.Fatalf("selectedLog after down = %d, want %d", updated.selectedLog, lastIndex)
	}
}

func TestModelLogSelectionAutoScrollsIntoView(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.mode = modeLogs
	m.height = 12
	m.logs = sampleLogs(20)
	m.selectedLog = 0
	m.logOffset = 0

	for i := 0; i < 10; i++ {
		next, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
		m = next.(model)
	}

	if m.selectedLog != 10 {
		t.Fatalf("selectedLog = %d, want 10", m.selectedLog)
	}
	if m.logOffset == 0 {
		t.Fatal("logOffset = 0, want auto-scroll once selection moves below the visible list")
	}
	if m.selectedLog < m.logOffset || m.selectedLog >= m.logOffset+m.visibleLogLines() {
		t.Fatalf("selectedLog = %d should remain visible within [%d,%d)", m.selectedLog, m.logOffset, m.logOffset+m.visibleLogLines())
	}
}

func TestModelCyclesLogFilterAndUpdatesRenderedLogs(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.mode = modeLogs
	m.width = 80
	m.height = 16
	m.logs = logsWithLevels(
		"DEBUG",
		"INFO",
		"WARN",
		"ERROR",
	)
	m.selectLastLog()

	next, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "f"}))
	m = next.(model)

	if m.logFilter != logLevelFilterInfo {
		t.Fatalf("logFilter = %v, want %v", m.logFilter, logLevelFilterInfo)
	}
	if got := len(m.filteredLogEntries()); got != 3 {
		t.Fatalf("filtered log count = %d, want 3", got)
	}
	view := stripANSI(m.View().Content)
	if strings.Contains(view, "debug message") {
		t.Fatalf("view = %q, want debug entries filtered out", view)
	}
	for _, want := range []string{"info message", "warn message", "error message", "Filter info+", "f info+"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
	if m.selectedLog != 2 {
		t.Fatalf("selectedLog = %d, want last filtered entry", m.selectedLog)
	}
}

func TestModelLogFilterClampsSelectionWhenFilteredSetShrinks(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.mode = modeLogs
	m.height = 12
	m.logs = logsWithLevels(
		"DEBUG",
		"INFO",
		"WARN",
		"ERROR",
	)
	m.selectedLog = 2

	for range 3 {
		next, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "f"}))
		m = next.(model)
	}

	if m.logFilter != logLevelFilterError {
		t.Fatalf("logFilter = %v, want %v", m.logFilter, logLevelFilterError)
	}
	if got := len(m.filteredLogEntries()); got != 1 {
		t.Fatalf("filtered log count = %d, want 1", got)
	}
	if m.selectedLog != 0 {
		t.Fatalf("selectedLog = %d, want 0", m.selectedLog)
	}
	if m.logOffset != 0 {
		t.Fatalf("logOffset = %d, want 0", m.logOffset)
	}
	view := stripANSI(m.View().Content)
	if strings.Contains(view, "warn message") || strings.Contains(view, "info message") || strings.Contains(view, "debug message") {
		t.Fatalf("view = %q, want only error logs visible", view)
	}
	if !strings.Contains(view, "error message") {
		t.Fatalf("view = %q, want error log visible", view)
	}
}

func TestModelEscStopsAndWaitsForServiceExit(t *testing.T) {
	t.Parallel()

	var stops int
	m := newModel(context.Background(), fakeSource{}, nil, nil, func() { stops++ })

	next, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	updated := next.(model)
	if !updated.forceStopIssued {
		t.Fatal("forceStopIssued = false, want true")
	}
	if stops != 1 {
		t.Fatalf("stop count = %d, want 1", stops)
	}
	if cmd != nil {
		t.Fatal("esc should not quit before the service exits")
	}
}

func TestModelQRequestsShutdownDrainWhenWorkersAreRunning(t *testing.T) {
	t.Parallel()

	var drainRequests int
	var stops int
	now := time.Now().UTC()
	source := fakeSource{
		snapshot: domain.Snapshot{
			Running: []domain.SnapshotRunning{{
				Identifier: "COLIN-150",
				State:      "In Progress",
				StartedAt:  now.Add(-time.Minute),
			}},
		},
	}
	msg := refreshRuntime(context.Background(), source)()
	m := newModel(context.Background(), source, nil, func() bool {
		drainRequests++
		return true
	}, func() { stops++ })
	next, _ := m.Update(msg)
	m = next.(model)

	next, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "q"}))
	updated := next.(model)
	if !updated.shutdownRequested {
		t.Fatal("shutdownRequested = false, want true")
	}
	if updated.forceStopIssued {
		t.Fatal("forceStopIssued = true, want false")
	}
	if drainRequests != 1 {
		t.Fatalf("drainRequests = %d, want 1", drainRequests)
	}
	if stops != 0 {
		t.Fatalf("stop count = %d, want 0", stops)
	}
	if cmd != nil {
		t.Fatal("first q should not quit before workers drain")
	}
	view := stripANSI(updated.View().Content)
	for _, want := range []string{
		"shutdown requested; waiting for 1 worker to go idle",
		"press q again to exit",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
	if strings.Contains(view, "q/esc quit  shutdown requested") {
		t.Fatalf("view = %q, want shutdown message moved out of the header", view)
	}
	if strings.Index(view, "Workers") > strings.Index(view, "shutdown requested; waiting for 1 worker to go idle") {
		t.Fatalf("view = %q, want shutdown message after workers section", view)
	}
	if got := shutdownStyle.GetForeground(); got != lipgloss.Color("208") {
		t.Fatalf("shutdown foreground = %v, want orange 208", got)
	}
}

func TestModelSecondQExitsImmediatelyDuringShutdownDrain(t *testing.T) {
	t.Parallel()

	var stops int
	m := newModel(context.Background(), fakeSource{}, nil, nil, func() { stops++ })
	m.shutdownRequested = true

	next, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "q"}))
	updated := next.(model)
	if !updated.forceStopIssued {
		t.Fatal("forceStopIssued = false, want true")
	}
	if stops != 1 {
		t.Fatalf("stop count = %d, want 1", stops)
	}
	if cmd == nil {
		t.Fatal("second q should exit immediately")
	}
}

func TestModelShutdownDrainStopsOnceWorkersGoIdle(t *testing.T) {
	t.Parallel()

	var stops int
	m := newModel(context.Background(), fakeSource{}, nil, nil, func() { stops++ })
	m.shutdownRequested = true
	m.snapshot = domain.Snapshot{
		Running: []domain.SnapshotRunning{{
			Identifier: "COLIN-150",
		}},
	}

	next, _ := m.Update(refreshMsg{snapshot: domain.Snapshot{}})
	updated := next.(model)
	if !updated.forceStopIssued {
		t.Fatal("forceStopIssued = false, want true")
	}
	if stops != 1 {
		t.Fatalf("stop count = %d, want 1", stops)
	}
}

func TestModelRefreshPopulatesURLsAndWorkers(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	source := fakeSource{
		dashboardURL: "http://127.0.0.1:7777",
		setupURL:     "http://127.0.0.1:7777/setup/funnel",
		snapshot: domain.Snapshot{
			SlackSocketMode: domain.SlackSocketModeStatus{
				Enabled:     true,
				Connected:   true,
				State:       "connected",
				LastEventAt: now.Add(-10 * time.Second),
			},
			Webhooks: map[string]domain.WebhookStatus{
				"slack":  {},
				"linear": {},
			},
			Running: []domain.SnapshotRunning{{
				Identifier:  "COLIN-147",
				State:       "In Progress",
				SessionID:   "019d54ec-2095-7b2d-8e1a-0123456789ab",
				TurnCount:   2,
				LastMessage: "Investigating worker output",
				StartedAt:   now.Add(-2 * time.Minute),
			}},
		},
		logs: sampleLogs(3),
		setup: domain.FunnelSetupStatus{
			Ready:            true,
			TailnetUIBaseURL: "https://colin.tail.example.ts.net",
			PublicBaseURL:    "https://colin.example.test",
			LinearWebhookURL: "https://colin.example.test/webhooks/linear",
			GitHubWebhookURL: "https://colin.example.test/webhooks/github",
		},
	}
	msg := refreshRuntime(context.Background(), source)()
	m := newModel(context.Background(), source, nil, nil, nil)

	next, _ := m.Update(msg)
	updated := next.(model)
	if updated.dashboardURL != source.dashboardURL {
		t.Fatalf("dashboardURL = %q, want %q", updated.dashboardURL, source.dashboardURL)
	}
	if len(updated.snapshot.Running) != 1 {
		t.Fatalf("running workers = %d, want 1", len(updated.snapshot.Running))
	}
	view := stripANSI(updated.View().Content)
	for _, want := range []string{
		"1. COLIN-147     In Progress  turn 2  running 2m",
		"COLIN-147",
		"In Progress",
		"running 2m",
		"https://colin.tail.example.ts.net",
		"linear hook https://colin.example.test/webhooks/linear",
		"https://colin.example.test/webhooks/linear",
		"github hook https://colin.example.test/webhooks/github",
		"https://colin.example.test/webhooks/github",
		"  Integrations",
		"  tailscale       ready",
		"  slack ws        connected 10s ago",
		"  slack webhook   no messages yet",
		"  linear webhook  no messages yet",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
	if strings.Contains(view, "019d54ec-2095-7b2d-8e1a-0123456789ab") {
		t.Fatalf("view = %q, want worker list without session ID", view)
	}
	if strings.Contains(view, "Investigating worker output") {
		t.Fatalf("view = %q, want worker runtime instead of last message", view)
	}
	for _, unwanted := range []string{
		"setup http://127.0.0.1:7777/setup/funnel",
		"setup      ",
		"public     ",
		"tailnet    ",
	} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("view = %q, want to omit %q", view, unwanted)
		}
	}
}

func TestModelRefreshRendersWorkflowFileAndTargets(t *testing.T) {
	t.Parallel()

	source := fakeSource{
		snapshot: domain.Snapshot{
			WorkflowPath: "/tmp/colin/WORKFLOW.md",
			Targets: []domain.SnapshotTarget{
				{
					Name:        "api",
					ProjectSlug: "api-project",
					RepoURL:     "git@github.com:bothnia/api.git",
					BaseRef:     "main",
				},
				{
					Name:        "web",
					ProjectSlug: "web-project",
					RepoURL:     "git@github.com:bothnia/web.git",
					BaseRef:     "trunk",
				},
			},
		},
	}
	msg := refreshRuntime(context.Background(), source)()
	m := newModel(context.Background(), source, nil, nil, nil)
	m.width = 120

	next, _ := m.Update(msg)
	view := stripANSI(next.(model).View().Content)
	for _, want := range []string{
		"  Workflow",
		"  file        /tmp/colin/WORKFLOW.md",
		"name  project      base   repo",
		"api   api-project  main",
		"git@github.com:bothnia/api.git",
		"web   web-project  trunk",
		"git@github.com:bothnia/web.git",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
}

func TestModelRefreshFallsBackToLocalDashboardWhenTailnetMissing(t *testing.T) {
	t.Parallel()

	source := fakeSource{
		dashboardURL: "http://127.0.0.1:7777",
		snapshot:     domain.Snapshot{},
		logs:         sampleLogs(1),
		setup: domain.FunnelSetupStatus{
			LinearWebhookURL: "https://colin.example.test/webhooks/linear",
		},
	}
	msg := refreshRuntime(context.Background(), source)()
	m := newModel(context.Background(), source, nil, nil, nil)

	next, _ := m.Update(msg)
	view := stripANSI(next.(model).View().Content)
	if !strings.Contains(view, "http://127.0.0.1:7777") {
		t.Fatalf("view = %q, want local dashboard URL fallback", view)
	}
}

func TestLogsViewShowsSelectedFullLineBelowList(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.mode = modeLogs
	m.width = 40
	m.height = 18
	m.logs = domain.BufferedLogSnapshot{
		Entries: []domain.BufferedLogEntry{
			{
				Timestamp: time.Unix(0, 0).UTC(),
				Level:     "INFO",
				Message:   "short line",
			},
			{
				Timestamp: time.Unix(1, 0).UTC(),
				Level:     "WARN",
				Message:   "this is a deliberately long log line that should be visible in full below the list of entries",
				Fields:    []string{"issue=COLIN-200", "state=Review"},
			},
		},
		Count:    2,
		Capacity: 2,
	}
	m.selectedLog = 1
	m.ensureSelectedLogVisible()

	view := stripANSI(m.View().Content)
	normalized := strings.Join(strings.Fields(view), " ")
	if !strings.Contains(view, "Selected log 2/2") {
		t.Fatalf("view = %q, want selected log label", view)
	}
	if !strings.Contains(normalized, "this is a deliberately long log line that should be visible in full below the list of entries issue=COLIN-200 state=Review") {
		t.Fatalf("view = %q, want full selected log line in detail pane", view)
	}
}

func TestOverviewShowsIndicatorForUnseenWarnOrErrorLogs(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.logs = domain.BufferedLogSnapshot{
		Entries: []domain.BufferedLogEntry{
			{Timestamp: time.Unix(0, 0).UTC(), Level: "INFO", Message: "all good"},
			{Timestamp: time.Unix(1, 0).UTC(), Level: "WARN", Message: "pay attention"},
		},
		Count:    2,
		Capacity: 2,
	}

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "warn/err in logs") {
		t.Fatalf("view = %q, want unseen warn/error indicator", view)
	}
}

func TestViewingLogsClearsIndicatorUntilNewWarnOrErrorArrives(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.logs = domain.BufferedLogSnapshot{
		Entries: []domain.BufferedLogEntry{
			{Timestamp: time.Unix(0, 0).UTC(), Level: "WARN", Message: "first warning"},
		},
		Count:    1,
		Capacity: 2,
	}

	if !strings.Contains(stripANSI(m.View().Content), "warn/err in logs") {
		t.Fatal("expected indicator before viewing logs")
	}

	next, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	m = next.(model)
	if m.mode != modeLogs {
		t.Fatalf("mode = %v, want modeLogs", m.mode)
	}
	if strings.Contains(stripANSI(m.View().Content), "warn/err in logs") {
		t.Fatal("indicator should clear after opening logs")
	}

	next, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	m = next.(model)
	if m.mode != modeOverview {
		t.Fatalf("mode = %v, want modeOverview", m.mode)
	}
	if strings.Contains(stripANSI(m.View().Content), "warn/err in logs") {
		t.Fatal("indicator should stay cleared after returning to overview")
	}

	next, _ = m.Update(refreshMsg{
		logs: domain.BufferedLogSnapshot{
			Entries: []domain.BufferedLogEntry{
				{Timestamp: time.Unix(0, 0).UTC(), Level: "WARN", Message: "first warning"},
				{Timestamp: time.Unix(2, 0).UTC(), Level: "ERROR", Message: "new error"},
			},
			Count:    2,
			Capacity: 2,
		},
	})
	m = next.(model)
	if !strings.Contains(stripANSI(m.View().Content), "warn/err in logs") {
		t.Fatal("indicator should return after a new warn/error arrives")
	}
}

func TestFilteredLogsDoNotMarkHiddenWarningsAsViewed(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.logs = domain.BufferedLogSnapshot{
		Entries: []domain.BufferedLogEntry{
			{Timestamp: time.Unix(0, 0).UTC(), Level: "ERROR", Message: "first error"},
		},
		Count:    1,
		Capacity: 2,
	}
	m.logFilter = logLevelFilterError

	next, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	m = next.(model)
	if strings.Contains(stripANSI(m.View().Content), "warn/err in logs") {
		t.Fatal("indicator should clear after viewing the visible error log")
	}

	next, _ = m.Update(refreshMsg{
		logs: domain.BufferedLogSnapshot{
			Entries: []domain.BufferedLogEntry{
				{Timestamp: time.Unix(0, 0).UTC(), Level: "ERROR", Message: "first error"},
				{Timestamp: time.Unix(1, 0).UTC(), Level: "WARN", Message: "hidden warning"},
			},
			Count:    2,
			Capacity: 3,
		},
	})
	m = next.(model)
	if !m.hasUnseenLogAlerts() {
		t.Fatal("hidden warning should remain unseen while filtered out in logs mode")
	}

	next, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	m = next.(model)
	if !strings.Contains(stripANSI(m.View().Content), "warn/err in logs") {
		t.Fatal("indicator should reappear in overview because the filtered-out warning was not viewed")
	}
}

func TestFailedRefreshDoesNotReopenViewedWarnOrErrorIndicator(t *testing.T) {
	t.Parallel()

	alertLogs := domain.BufferedLogSnapshot{
		Entries: []domain.BufferedLogEntry{
			{Timestamp: time.Unix(0, 0).UTC(), Level: "WARN", Message: "first warning"},
		},
		Count:    1,
		Capacity: 2,
	}

	m := newModel(context.Background(), fakeSource{}, nil, nil, nil)
	m.logs = alertLogs

	next, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	m = next.(model)
	if strings.Contains(stripANSI(m.View().Content), "warn/err in logs") {
		t.Fatal("indicator should clear after opening logs")
	}

	next, _ = m.Update(refreshMsg{err: fmt.Errorf("boom")})
	m = next.(model)

	next, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	m = next.(model)
	if m.mode != modeOverview {
		t.Fatalf("mode = %v, want modeOverview", m.mode)
	}

	next, _ = m.Update(refreshMsg{logs: alertLogs})
	m = next.(model)
	if strings.Contains(stripANSI(m.View().Content), "warn/err in logs") {
		t.Fatal("indicator should remain cleared after a failed refresh followed by the same alert logs")
	}
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(value string) string {
	return ansiPattern.ReplaceAllString(value, "")
}

func TestRunReturnsServiceError(t *testing.T) {
	t.Parallel()

	errCh := make(chan error, 1)
	errCh <- fmt.Errorf("boom")

	err := Run(context.Background(), strings.NewReader(""), io.Discard, fakeSource{}, errCh, nil, nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("Run() error = %v, want boom", err)
	}
}

func sampleLogs(count int) domain.BufferedLogSnapshot {
	entries := make([]domain.BufferedLogEntry, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, domain.BufferedLogEntry{
			Timestamp: time.Unix(int64(i), 0).UTC(),
			Level:     "INFO",
			Message:   fmt.Sprintf("line %d", i),
		})
	}
	return domain.BufferedLogSnapshot{Entries: entries, Count: len(entries), Capacity: maxInt(count, 1)}
}

func logsWithLevels(levels ...string) domain.BufferedLogSnapshot {
	entries := make([]domain.BufferedLogEntry, 0, len(levels))
	for i, level := range levels {
		entries = append(entries, domain.BufferedLogEntry{
			Timestamp: time.Unix(int64(i), 0).UTC(),
			Level:     level,
			Message:   strings.ToLower(level) + " message",
		})
	}
	return domain.BufferedLogSnapshot{Entries: entries, Count: len(entries), Capacity: maxInt(len(entries), 1)}
}
