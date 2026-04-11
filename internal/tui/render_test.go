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

func TestRenderTailscaleStatusLineShowsReadyOnly(t *testing.T) {
	t.Parallel()

	got := stripANSI(renderTailscaleStatusLine(domain.FunnelSetupStatus{
		Ready:            true,
		TailnetUIBaseURL: "https://colin.tail.example.ts.net",
		PublicBaseURL:    "https://colin.tail.example.ts.net:8443",
	}, 120))

	for _, want := range []string{
		"tailscale",
		"ready",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderTailscaleStatusLine() = %q, want %q", got, want)
		}
	}
	for _, unwanted := range []string{
		"ui https://colin.tail.example.ts.net",
		"webhooks https://colin.tail.example.ts.net:8443",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("renderTailscaleStatusLine() = %q, want no %q", got, unwanted)
		}
	}
}

func TestRenderTailscaleStatusLineShowsFirstFailedCheck(t *testing.T) {
	t.Parallel()

	got := stripANSI(renderTailscaleStatusLine(domain.FunnelSetupStatus{
		Checks: []domain.SetupCheck{
			{
				Label:  "Colin can reach the local Tailscale daemon",
				Status: "ok",
				Detail: "Connected to the local Tailscale daemon.",
			},
			{
				Label:  "Tailscale is running",
				Status: "error",
				Detail: "Backend state is `Stopped`.",
			},
		},
	}, 120))

	for _, want := range []string{
		"tailscale",
		"error Tailscale is running: Backend state is `Stopped`.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderTailscaleStatusLine() = %q, want %q", got, want)
		}
	}
}
