package logbuffer

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func TestBufferKeepsNewestEntries(t *testing.T) {
	t.Parallel()

	buffer := New(2)
	buffer.append(storedEntry{
		timestamp: domainEntry(time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC), slog.LevelInfo, "one", nil),
		level:     slog.LevelInfo,
	})
	buffer.append(storedEntry{
		timestamp: domainEntry(time.Date(2026, 3, 30, 10, 0, 1, 0, time.UTC), slog.LevelWarn, "two", nil),
		level:     slog.LevelWarn,
	})
	buffer.append(storedEntry{
		timestamp: domainEntry(time.Date(2026, 3, 30, 10, 0, 2, 0, time.UTC), slog.LevelError, "three", nil),
		level:     slog.LevelError,
	})

	got := buffer.Snapshot(nil)
	if got.Capacity != 2 {
		t.Fatalf("Capacity = %d, want 2", got.Capacity)
	}
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2", got.Count)
	}
	if got.Entries[0].Message != "two" || got.Entries[1].Message != "three" {
		t.Fatalf("entries = %#v, want newest entries in order", got.Entries)
	}
}

func TestBufferSnapshotFiltersByLevel(t *testing.T) {
	t.Parallel()

	buffer := New(4)
	buffer.append(storedEntry{
		timestamp: domainEntry(time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC), slog.LevelDebug, "debug", nil),
		level:     slog.LevelDebug,
	})
	buffer.append(storedEntry{
		timestamp: domainEntry(time.Date(2026, 3, 30, 10, 0, 1, 0, time.UTC), slog.LevelInfo, "info", nil),
		level:     slog.LevelInfo,
	})
	buffer.append(storedEntry{
		timestamp: domainEntry(time.Date(2026, 3, 30, 10, 0, 2, 0, time.UTC), slog.LevelWarn, "warn", nil),
		level:     slog.LevelWarn,
	})

	level := slog.LevelInfo
	got := buffer.Snapshot(&level)
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2", got.Count)
	}
	if got.Entries[0].Message != "info" || got.Entries[1].Message != "warn" {
		t.Fatalf("entries = %#v, want info and warn", got.Entries)
	}
}

func TestBufferResizeKeepsNewestEntries(t *testing.T) {
	t.Parallel()

	buffer := New(4)
	for i, msg := range []string{"one", "two", "three", "four"} {
		buffer.append(storedEntry{
			timestamp: domainEntry(time.Date(2026, 3, 30, 10, 0, i, 0, time.UTC), slog.LevelInfo, msg, nil),
			level:     slog.LevelInfo,
		})
	}

	buffer.Resize(2)
	got := buffer.Snapshot(nil)
	if got.Capacity != 2 {
		t.Fatalf("Capacity = %d, want 2", got.Capacity)
	}
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2", got.Count)
	}
	if got.Entries[0].Message != "three" || got.Entries[1].Message != "four" {
		t.Fatalf("entries = %#v, want newest entries after resize", got.Entries)
	}
}

func TestHandlerCapturesSuppressedInfoAndDebug(t *testing.T) {
	t.Parallel()

	buffer := New(10)
	logger := slog.New(NewHandler(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}), buffer))

	logger.Debug("debug entry", "issue_id", "issue-1")
	logger.Info("info entry", "attempt", 2)
	logger.Error("error entry")

	debug := slog.LevelDebug
	got := buffer.Snapshot(&debug)
	if got.Count != 3 {
		t.Fatalf("Count = %d, want 3", got.Count)
	}
	if got.Entries[0].Message != "debug entry" {
		t.Fatalf("Entries[0].Message = %q, want debug entry", got.Entries[0].Message)
	}
	if fields := strings.Join(got.Entries[0].Fields, ","); !strings.Contains(fields, "issue_id=issue-1") {
		t.Fatalf("debug fields = %q, want issue_id", fields)
	}
	if fields := strings.Join(got.Entries[1].Fields, ","); !strings.Contains(fields, "attempt=2") {
		t.Fatalf("info fields = %q, want attempt", fields)
	}
}

func TestHandlerPreservesWrappedOutputBehavior(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	buffer := New(10)
	logger := slog.New(NewHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelError}), buffer))

	logger.Info("hidden")
	logger.Error("visible")

	text := output.String()
	if strings.Contains(text, "hidden") {
		t.Fatalf("output = %q, unexpected hidden info log", text)
	}
	if !strings.Contains(text, "visible") {
		t.Fatalf("output = %q, missing visible error log", text)
	}
}

func TestHandlerWithAttrsAndGroupsAddsBufferedFields(t *testing.T) {
	t.Parallel()

	buffer := New(10)
	logger := slog.New(NewHandler(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}), buffer))
	logger = logger.With("component", "service").WithGroup("tracker")

	logger.Info("buffered", slog.String("project", "colin"))

	got := buffer.Snapshot(nil)
	if got.Count != 1 {
		t.Fatalf("Count = %d, want 1", got.Count)
	}
	fields := strings.Join(got.Entries[0].Fields, ",")
	if !strings.Contains(fields, "component=service") {
		t.Fatalf("fields = %q, want component", fields)
	}
	if !strings.Contains(fields, "tracker.project=colin") {
		t.Fatalf("fields = %q, want grouped project", fields)
	}
}

func domainEntry(ts time.Time, level slog.Level, message string, fields []string) domain.BufferedLogEntry {
	return domain.BufferedLogEntry{
		Timestamp: ts,
		Level:     level.String(),
		Message:   message,
		Fields:    append([]string(nil), fields...),
	}
}
