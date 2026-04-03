package clioutput

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRendererWithoutColor(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := New(&out, false)

	renderer.Section("Overview")
	renderer.Item("Repository", "acme/widgets")
	renderer.Section("Checks")
	renderer.Status(StatusAction, "Signing secret", "set repo.webhook_signing_secret")
	renderer.Status(StatusInfo, "Note", "polling remains enabled")

	got := out.String()
	for _, want := range []string{
		"Overview\n- Repository: acme/widgets\n\nChecks\n- [ACTION] Signing secret: set repo.webhook_signing_secret\n- [INFO] Note: polling remains enabled\n",
	} {
		if got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestRendererWithColor(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := New(&out, true)

	renderer.Section("Checks")
	renderer.Status(StatusOK, "Ready", "configured")
	renderer.Status(StatusError, "Webhook URL", "missing")

	got := out.String()
	if !strings.Contains(got, lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("[OK]")) {
		t.Fatalf("output = %q, want colored OK badge", got)
	}
	if !strings.Contains(got, lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("[ERROR]")) {
		t.Fatalf("output = %q, want colored ERROR badge", got)
	}
}
