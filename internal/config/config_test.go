package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFromEnvWithDefaults(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")
	t.Setenv("LINEAR_BASE_URL", "")
	t.Setenv("COLIN_WORKER_ID", "")
	t.Setenv("COLIN_POLL_EVERY", "")
	t.Setenv("COLIN_LEASE_TTL", "")
	t.Setenv("COLIN_DRY_RUN", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.LinearBaseURL != defaultLinearBaseURL {
		t.Fatalf("LinearBaseURL = %q, want %q", cfg.LinearBaseURL, defaultLinearBaseURL)
	}
	if cfg.PollEvery != defaultPollEvery {
		t.Fatalf("PollEvery = %s, want %s", cfg.PollEvery, defaultPollEvery)
	}
	if cfg.LeaseTTL != defaultLeaseTTL {
		t.Fatalf("LeaseTTL = %s, want %s", cfg.LeaseTTL, defaultLeaseTTL)
	}
	if cfg.WorkerID == "" {
		t.Fatal("WorkerID should not be empty")
	}
	if cfg.DryRun {
		t.Fatal("DryRun should default to false")
	}
}

func TestLoadFromEnvOverrides(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")
	t.Setenv("LINEAR_BASE_URL", "https://linear.invalid/graphql")
	t.Setenv("COLIN_WORKER_ID", "worker-a")
	t.Setenv("COLIN_POLL_EVERY", "45s")
	t.Setenv("COLIN_LEASE_TTL", "10m")
	t.Setenv("COLIN_DRY_RUN", "true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.LinearBaseURL != "https://linear.invalid/graphql" {
		t.Fatalf("LinearBaseURL = %q", cfg.LinearBaseURL)
	}
	if cfg.WorkerID != "worker-a" {
		t.Fatalf("WorkerID = %q", cfg.WorkerID)
	}
	if cfg.PollEvery != 45*time.Second {
		t.Fatalf("PollEvery = %s", cfg.PollEvery)
	}
	if cfg.LeaseTTL != 10*time.Minute {
		t.Fatalf("LeaseTTL = %s", cfg.LeaseTTL)
	}
	if !cfg.DryRun {
		t.Fatal("DryRun = false, want true")
	}
}

func TestLoadFromEnvRequiresTokenAndTeam(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for missing required env vars")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	content := `
linear_api_token = "file-token"
linear_team_id = "file-team"
linear_base_url = "https://file.invalid/graphql"
worker_id = "file-worker"
poll_every = "15s"
lease_ttl = "3m"
dry_run = true
`
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("COLIN_CONFIG", filepath.Join(t.TempDir(), "other.toml"))
	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")
	t.Setenv("LINEAR_BASE_URL", "")
	t.Setenv("COLIN_WORKER_ID", "")
	t.Setenv("COLIN_POLL_EVERY", "")
	t.Setenv("COLIN_LEASE_TTL", "")
	t.Setenv("COLIN_DRY_RUN", "")

	cfg, err := LoadFromPath(configPath)
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}

	if cfg.LinearAPIToken != "file-token" {
		t.Fatalf("LinearAPIToken = %q", cfg.LinearAPIToken)
	}
	if cfg.LinearTeamID != "file-team" {
		t.Fatalf("LinearTeamID = %q", cfg.LinearTeamID)
	}
	if cfg.LinearBaseURL != "https://file.invalid/graphql" {
		t.Fatalf("LinearBaseURL = %q", cfg.LinearBaseURL)
	}
	if cfg.WorkerID != "file-worker" {
		t.Fatalf("WorkerID = %q", cfg.WorkerID)
	}
	if cfg.PollEvery != 15*time.Second {
		t.Fatalf("PollEvery = %s", cfg.PollEvery)
	}
	if cfg.LeaseTTL != 3*time.Minute {
		t.Fatalf("LeaseTTL = %s", cfg.LeaseTTL)
	}
	if !cfg.DryRun {
		t.Fatal("DryRun = false, want true")
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	if err := os.WriteFile(configPath, []byte("linear_api_token = \"file-token\"\nlinear_team_id = \"file-team\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("LINEAR_API_TOKEN", "env-token")
	t.Setenv("LINEAR_TEAM_ID", "env-team")
	t.Setenv("COLIN_DRY_RUN", "true")

	cfg, err := LoadFromPath(configPath)
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}

	if cfg.LinearAPIToken != "env-token" {
		t.Fatalf("LinearAPIToken = %q", cfg.LinearAPIToken)
	}
	if cfg.LinearTeamID != "env-team" {
		t.Fatalf("LinearTeamID = %q", cfg.LinearTeamID)
	}
	if !cfg.DryRun {
		t.Fatal("DryRun = false, want true")
	}
}

func TestLoadWithoutFileFallsBackToEnv(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")

	if _, err := LoadFromPath(filepath.Join(t.TempDir(), "missing.toml")); err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}
}

func TestLoadUsesCOLIN_CONFIGByDefault(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	if err := os.WriteFile(configPath, []byte("linear_api_token = \"file-token\"\nlinear_team_id = \"file-team\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("COLIN_CONFIG", configPath)
	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LinearAPIToken != "file-token" {
		t.Fatalf("LinearAPIToken = %q", cfg.LinearAPIToken)
	}
	if cfg.LinearTeamID != "file-team" {
		t.Fatalf("LinearTeamID = %q", cfg.LinearTeamID)
	}
}
