package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootCommandRunsWorkerByDefault(t *testing.T) {
	configPath := writeRootTestConfig(t)
	rootCmd := NewRootCommand()
	rootCmd.SetArgs([]string{"--config", configPath, "--once"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
}

func TestRootCommandVerboseFlag(t *testing.T) {
	configPath := writeRootTestConfig(t)
	rootCmd := NewRootCommand()
	rootCmd.SetArgs([]string{"--config", configPath, "--once", "--verbose"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
}

func TestRootCommandUsesCOLIN_CONFIGWhenConfigFlagUnset(t *testing.T) {
	configPath := writeRootTestConfig(t)
	t.Setenv("COLIN_CONFIG", configPath)

	rootCmd := NewRootCommand()
	rootCmd.SetArgs([]string{"--once"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
}

func TestRootCommandRootFlagsIncludeWorkerExecutionFlags(t *testing.T) {
	rootCmd := NewRootCommand()
	if rootCmd.Flags().Lookup("once") == nil {
		t.Fatal("expected root command to expose --once")
	}
	if rootCmd.Flags().Lookup("dry-run") == nil {
		t.Fatal("expected root command to expose --dry-run")
	}
}

func TestRootCommandHelpIncludesVerboseFlag(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(strings.Builder)
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
	if strings.Contains(helpOutput, "\n  worker") {
		t.Fatalf("expected help output to omit worker command, got: %q", helpOutput)
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
