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

	m := newModel(context.Background(), fakeSource{}, nil, nil)
	m.logs = sampleLogs(6)
	m.height = 12

	next, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	updated := next.(model)
	if updated.mode != modeLogs {
		t.Fatalf("mode = %v, want modeLogs", updated.mode)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Text: "l"}))
	updated = next.(model)
	if updated.mode != modeOverview {
		t.Fatalf("mode = %v, want modeOverview", updated.mode)
	}
}

func TestModelLogScrollClampsWithinBounds(t *testing.T) {
	t.Parallel()

	m := newModel(context.Background(), fakeSource{}, nil, nil)
	m.mode = modeLogs
	m.height = 12
	m.logs = sampleLogs(20)
	m.pinLogsToBottom()

	next, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	updated := next.(model)
	if updated.logOffset >= len(updated.logs.Entries) {
		t.Fatalf("logOffset = %d, want within bounds", updated.logOffset)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	updated = next.(model)
	if updated.logOffset != 0 {
		t.Fatalf("logOffset = %d, want 0", updated.logOffset)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	updated = next.(model)
	if updated.logOffset != 0 {
		t.Fatalf("logOffset after up = %d, want 0", updated.logOffset)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	updated = next.(model)
	maxOffset := maxInt(len(updated.logs.Entries)-updated.visibleLogLines(), 0)
	if updated.logOffset != maxOffset {
		t.Fatalf("logOffset after end = %d, want %d", updated.logOffset, maxOffset)
	}

	next, _ = updated.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	updated = next.(model)
	if updated.logOffset != maxOffset {
		t.Fatalf("logOffset after down = %d, want %d", updated.logOffset, maxOffset)
	}
}

func TestModelEscStopsAndWaitsForServiceExit(t *testing.T) {
	t.Parallel()

	var stops int
	m := newModel(context.Background(), fakeSource{}, nil, func() { stops++ })

	next, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	updated := next.(model)
	if !updated.quitting {
		t.Fatal("quitting = false, want true")
	}
	if stops != 1 {
		t.Fatalf("stop count = %d, want 1", stops)
	}
	if cmd != nil {
		t.Fatal("esc should not quit before the service exits")
	}
}

func TestModelRefreshPopulatesURLsAndWorkers(t *testing.T) {
	t.Parallel()

	source := fakeSource{
		dashboardURL: "http://127.0.0.1:7777",
		setupURL:     "http://127.0.0.1:7777/setup/funnel",
		snapshot: domain.Snapshot{
			Running: []domain.SnapshotRunning{{
				Identifier:  "COLIN-147",
				State:       "In Progress",
				SessionID:   "thread-1",
				TurnCount:   2,
				LastMessage: "Investigating worker output",
			}},
		},
		logs: sampleLogs(3),
		setup: domain.FunnelSetupStatus{
			TailnetUIBaseURL: "https://colin.tail.example.ts.net",
			PublicBaseURL:    "https://colin.example.test",
			LinearWebhookURL: "https://colin.example.test/webhooks/linear",
		},
	}
	msg := refreshRuntime(context.Background(), source)()
	m := newModel(context.Background(), source, nil, nil)

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
		"COLIN-147",
		"In Progress",
		"http://127.0.0.1:7777",
		"https://colin.tail.example.ts.net",
		"linear hook https://colin.example.test/webhooks/linear",
		"https://colin.example.test/webhooks/linear",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
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

	err := Run(context.Background(), strings.NewReader(""), io.Discard, fakeSource{}, errCh, nil)
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
