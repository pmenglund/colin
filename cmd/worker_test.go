package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerRunHelp(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"worker", "run", "--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "--once") {
		t.Fatalf("expected --once in help output, got: %q", out)
	}
	if !strings.Contains(out, "--dry-run") {
		t.Fatalf("expected --dry-run in help output, got: %q", out)
	}
}

func TestWorkerRunRequiresLinearEnv(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")

	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "missing.toml"), "worker", "run", "--once"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when required Linear env vars are missing")
	}
	if !strings.Contains(err.Error(), "LINEAR_API_TOKEN is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWorkerRunLoadsFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	content := `linear_api_token = "file-token"
linear_team_id = "file-team"
dry_run = true
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")
	t.Setenv("LINEAR_BASE_URL", "http://127.0.0.1:1/graphql")

	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--config", configPath, "worker", "run", "--once"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected run error from fake endpoint, config load should have succeeded")
	}
	if strings.Contains(err.Error(), "LINEAR_API_TOKEN is required") {
		t.Fatalf("unexpected config load failure: %v", err)
	}
}
