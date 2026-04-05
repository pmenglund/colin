package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func TestRenderLogLineEscapesEmbeddedControlWhitespace(t *testing.T) {
	t.Parallel()

	entry := domain.BufferedLogEntry{
		Timestamp: time.Date(2026, 4, 5, 20, 49, 22, 0, time.UTC),
		Level:     "WARN",
		Message:   "codex stderr\ncontinued",
		Fields: []string{
			"message=apply_patch verification failed:\nFailed to find expected lines",
			"path=\tinternal/orchestrator/review_sync_test.go",
		},
	}

	got := stripANSI(renderLogLine(entry))
	if strings.Contains(got, "\n") {
		t.Fatalf("renderLogLine() = %q, want single rendered line", got)
	}
	if !strings.Contains(got, `codex stderr\ncontinued`) {
		t.Fatalf("renderLogLine() = %q, want escaped message newline", got)
	}
	if !strings.Contains(got, `message=apply_patch verification failed:\nFailed to find expected lines`) {
		t.Fatalf("renderLogLine() = %q, want escaped field newline", got)
	}
	if !strings.Contains(got, `path=\tinternal/orchestrator/review_sync_test.go`) {
		t.Fatalf("renderLogLine() = %q, want escaped tab", got)
	}
}
