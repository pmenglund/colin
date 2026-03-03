package cmd

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerFlagsAvailableOnRootHelp(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--help"})

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
	rootCmd.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "missing.toml"), "--once"})

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
	keyPath := writeTestGitHubAppKey(t)
	content := fmt.Sprintf(`linear_api_token = "file-token"
linear_team_id = "file-team"
github_app_id = "123"
github_app_installation_id = "456"
github_app_private_key_path = %q
dry_run = true
`, keyPath)
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
	rootCmd.SetArgs([]string{"--config", configPath, "--once"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected run error from fake endpoint, config load should have succeeded")
	}
	if strings.Contains(err.Error(), "LINEAR_API_TOKEN is required") {
		t.Fatalf("unexpected config load failure: %v", err)
	}
}

func TestWorkerRunFakeBackendDoesNotRequireLinearEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	content := `linear_backend = "fake"
worker_id = "test-worker"
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
	rootCmd.SetArgs([]string{"--config", configPath, "--once"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
}

func writeTestGitHubAppKey(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	path := filepath.Join(t.TempDir(), "github-app.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}
