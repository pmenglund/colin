package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootCommandDefaultRun(t *testing.T) {
	configPath := writeRootTestConfig(t)
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--config", configPath, "--once"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
}

func TestRootCommandVerboseFlag(t *testing.T) {
	configPath := writeRootTestConfig(t)
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--config", configPath, "--once", "--verbose"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
}

func TestRootCommandHelpIncludesVerboseFlag(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	helpOutput := buf.String()
	if !strings.Contains(helpOutput, "--verbose") {
		t.Fatalf("expected help output to contain --verbose, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "--config") {
		t.Fatalf("expected help output to contain --config, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "--once") {
		t.Fatalf("expected help output to contain --once, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "--dry-run") {
		t.Fatalf("expected help output to contain --dry-run, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "worker") {
		t.Fatalf("expected help output to contain worker command, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "setup") {
		t.Fatalf("expected help output to contain setup command, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "metadata") {
		t.Fatalf("expected help output to contain metadata command, got: %q", helpOutput)
	}
}

func writeRootTestConfig(t *testing.T) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "colin.toml")
	content := `linear_backend = "fake"
worker_id = "test-worker"
dry_run = true
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return configPath
}
