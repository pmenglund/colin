package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadFromEnvWithDefaults(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")
	t.Setenv("LINEAR_BASE_URL", "")
	t.Setenv("COLIN_LINEAR_BACKEND", "")
	t.Setenv("COLIN_PROJECT_FILTER", "")
	t.Setenv("COLIN_WORK_PROMPT_PATH", "")
	t.Setenv("COLIN_MERGE_PROMPT_PATH", "")
	t.Setenv("COLIN_HOME", "")
	t.Setenv("COLIN_WORKER_ID", "")
	t.Setenv("COLIN_POLL_EVERY", "")
	t.Setenv("COLIN_LEASE_TTL", "")
	t.Setenv("COLIN_MAX_CONCURRENCY", "")
	t.Setenv("COLIN_DRY_RUN", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.LinearBaseURL != defaultLinearBaseURL {
		t.Fatalf("LinearBaseURL = %q, want %q", cfg.LinearBaseURL, defaultLinearBaseURL)
	}
	if cfg.LinearBackend != defaultLinearBackend {
		t.Fatalf("LinearBackend = %q, want %q", cfg.LinearBackend, defaultLinearBackend)
	}
	if cfg.ColinHome != defaultColinHome() {
		t.Fatalf("ColinHome = %q, want %q", cfg.ColinHome, defaultColinHome())
	}
	if cfg.PollEvery != defaultPollEvery {
		t.Fatalf("PollEvery = %s, want %s", cfg.PollEvery, defaultPollEvery)
	}
	if cfg.LeaseTTL != defaultLeaseTTL {
		t.Fatalf("LeaseTTL = %s, want %s", cfg.LeaseTTL, defaultLeaseTTL)
	}
	if cfg.MaxConcurrency != defaultMaxConcurrency {
		t.Fatalf("MaxConcurrency = %d, want %d", cfg.MaxConcurrency, defaultMaxConcurrency)
	}
	if cfg.WorkerID == "" {
		t.Fatal("WorkerID should not be empty")
	}
	if cfg.DryRun {
		t.Fatal("DryRun should default to false")
	}
	if cfg.WorkPromptPath != "" {
		t.Fatalf("WorkPromptPath = %q, want empty", cfg.WorkPromptPath)
	}
	if cfg.MergePromptPath != "" {
		t.Fatalf("MergePromptPath = %q, want empty", cfg.MergePromptPath)
	}
	if len(cfg.ProjectFilter) != 0 {
		t.Fatalf("ProjectFilter = %#v, want empty", cfg.ProjectFilter)
	}
	if cfg.WorkflowStates != DefaultWorkflowStates() {
		t.Fatalf("WorkflowStates = %#v, want %#v", cfg.WorkflowStates, DefaultWorkflowStates())
	}
}

func TestLoadFromEnvOverrides(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")
	t.Setenv("LINEAR_BASE_URL", "https://linear.invalid/graphql")
	t.Setenv("COLIN_LINEAR_BACKEND", "fake")
	t.Setenv("COLIN_PROJECT_FILTER", "proj-a, Project One,proj-a,project one")
	t.Setenv("COLIN_WORK_PROMPT_PATH", "/tmp/custom-work-prompt.md")
	t.Setenv("COLIN_MERGE_PROMPT_PATH", "/tmp/custom-merge-prompt.md")
	t.Setenv("COLIN_HOME", "/tmp/colin-home")
	t.Setenv("COLIN_WORKER_ID", "worker-a")
	t.Setenv("COLIN_POLL_EVERY", "45s")
	t.Setenv("COLIN_LEASE_TTL", "10m")
	t.Setenv("COLIN_MAX_CONCURRENCY", "12")
	t.Setenv("COLIN_DRY_RUN", "true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if cfg.LinearBaseURL != "https://linear.invalid/graphql" {
		t.Fatalf("LinearBaseURL = %q", cfg.LinearBaseURL)
	}
	if cfg.LinearBackend != LinearBackendFake {
		t.Fatalf("LinearBackend = %q", cfg.LinearBackend)
	}
	if cfg.ColinHome != "/tmp/colin-home" {
		t.Fatalf("ColinHome = %q", cfg.ColinHome)
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
	if cfg.MaxConcurrency != 12 {
		t.Fatalf("MaxConcurrency = %d", cfg.MaxConcurrency)
	}
	if !cfg.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if cfg.WorkPromptPath != "/tmp/custom-work-prompt.md" {
		t.Fatalf("WorkPromptPath = %q", cfg.WorkPromptPath)
	}
	if cfg.MergePromptPath != "/tmp/custom-merge-prompt.md" {
		t.Fatalf("MergePromptPath = %q", cfg.MergePromptPath)
	}
	if want := []string{"proj-a", "Project One"}; !slices.Equal(cfg.ProjectFilter, want) {
		t.Fatalf("ProjectFilter = %#v, want %#v", cfg.ProjectFilter, want)
	}
}

func TestLoadFromEnvParsesQuotedProjectFilterCSV(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")
	t.Setenv("COLIN_LINEAR_BACKEND", "fake")
	t.Setenv("COLIN_PROJECT_FILTER", "\"Project, One\", project-two")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	if want := []string{"Project, One", "project-two"}; !slices.Equal(cfg.ProjectFilter, want) {
		t.Fatalf("ProjectFilter = %#v, want %#v", cfg.ProjectFilter, want)
	}
}

func TestLoadFromEnvRequiresTokenAndTeam(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")
	t.Setenv("COLIN_LINEAR_BACKEND", "")
	t.Setenv("COLIN_PROJECT_FILTER", "")

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for missing required env vars")
	}
}

func TestLoadFromEnvFakeBackendDoesNotRequireTokenAndTeam(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")
	t.Setenv("COLIN_LINEAR_BACKEND", "fake")
	t.Setenv("COLIN_PROJECT_FILTER", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.LinearBackend != LinearBackendFake {
		t.Fatalf("LinearBackend = %q", cfg.LinearBackend)
	}
}

func TestLoadFromEnvRejectsInvalidBackend(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")
	t.Setenv("COLIN_LINEAR_BACKEND", "unknown")
	t.Setenv("COLIN_PROJECT_FILTER", "")

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for invalid COLIN_LINEAR_BACKEND")
	}
}

func TestLoadFromEnvRejectsInvalidMaxConcurrency(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")
	t.Setenv("COLIN_MAX_CONCURRENCY", "0")
	t.Setenv("COLIN_PROJECT_FILTER", "")

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for invalid COLIN_MAX_CONCURRENCY")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	content := `
linear_api_token = "file-token"
linear_team_id = "file-team"
linear_base_url = "https://file.invalid/graphql"
linear_backend = "http"
project_filter = "PROJ-123, Website Revamp , proj-123"
work_prompt_path = "/tmp/file-work-prompt.md"
merge_prompt_path = "/tmp/file-merge-prompt.md"
colin_home = "/tmp/file-colin-home"
worker_id = "file-worker"
poll_every = "15s"
	lease_ttl = "3m"
	max_concurrency = 6
	dry_run = true
	`
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("COLIN_CONFIG", filepath.Join(t.TempDir(), "other.toml"))
	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")
	t.Setenv("LINEAR_BASE_URL", "")
	t.Setenv("COLIN_LINEAR_BACKEND", "")
	t.Setenv("COLIN_PROJECT_FILTER", "")
	t.Setenv("COLIN_WORK_PROMPT_PATH", "")
	t.Setenv("COLIN_MERGE_PROMPT_PATH", "")
	t.Setenv("COLIN_HOME", "")
	t.Setenv("COLIN_WORKER_ID", "")
	t.Setenv("COLIN_POLL_EVERY", "")
	t.Setenv("COLIN_LEASE_TTL", "")
	t.Setenv("COLIN_MAX_CONCURRENCY", "")
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
	if cfg.LinearBackend != defaultLinearBackend {
		t.Fatalf("LinearBackend = %q", cfg.LinearBackend)
	}
	if cfg.ColinHome != "/tmp/file-colin-home" {
		t.Fatalf("ColinHome = %q", cfg.ColinHome)
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
	if cfg.MaxConcurrency != 6 {
		t.Fatalf("MaxConcurrency = %d", cfg.MaxConcurrency)
	}
	if !cfg.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if cfg.WorkPromptPath != "/tmp/file-work-prompt.md" {
		t.Fatalf("WorkPromptPath = %q", cfg.WorkPromptPath)
	}
	if cfg.MergePromptPath != "/tmp/file-merge-prompt.md" {
		t.Fatalf("MergePromptPath = %q", cfg.MergePromptPath)
	}
	if want := []string{"PROJ-123", "Website Revamp"}; !slices.Equal(cfg.ProjectFilter, want) {
		t.Fatalf("ProjectFilter = %#v, want %#v", cfg.ProjectFilter, want)
	}
	if cfg.WorkflowStates != DefaultWorkflowStates() {
		t.Fatalf("WorkflowStates = %#v, want %#v", cfg.WorkflowStates, DefaultWorkflowStates())
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
	t.Setenv("COLIN_LINEAR_BACKEND", "fake")
	t.Setenv("COLIN_PROJECT_FILTER", "env-project,ENV-PROJECT, release train")
	t.Setenv("COLIN_WORK_PROMPT_PATH", "/tmp/env-work-prompt.md")
	t.Setenv("COLIN_MERGE_PROMPT_PATH", "/tmp/env-merge-prompt.md")
	t.Setenv("COLIN_HOME", "/tmp/env-colin-home")
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
	if cfg.LinearBackend != LinearBackendFake {
		t.Fatalf("LinearBackend = %q", cfg.LinearBackend)
	}
	if cfg.ColinHome != "/tmp/env-colin-home" {
		t.Fatalf("ColinHome = %q", cfg.ColinHome)
	}
	if !cfg.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if cfg.WorkPromptPath != "/tmp/env-work-prompt.md" {
		t.Fatalf("WorkPromptPath = %q", cfg.WorkPromptPath)
	}
	if cfg.MergePromptPath != "/tmp/env-merge-prompt.md" {
		t.Fatalf("MergePromptPath = %q", cfg.MergePromptPath)
	}
	if want := []string{"env-project", "release train"}; !slices.Equal(cfg.ProjectFilter, want) {
		t.Fatalf("ProjectFilter = %#v, want %#v", cfg.ProjectFilter, want)
	}
}

func TestLoadWithoutFileFallsBackToEnv(t *testing.T) {
	t.Setenv("LINEAR_API_TOKEN", "token")
	t.Setenv("LINEAR_TEAM_ID", "team")
	t.Setenv("COLIN_PROJECT_FILTER", "")

	if _, err := LoadFromPath(filepath.Join(t.TempDir(), "missing.toml")); err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}
}

func TestLoadFromFileFakeBackendWithoutCredentials(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	if err := os.WriteFile(configPath, []byte("linear_backend = \"fake\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")
	t.Setenv("COLIN_LINEAR_BACKEND", "")
	t.Setenv("COLIN_PROJECT_FILTER", "")

	cfg, err := LoadFromPath(configPath)
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}
	if cfg.LinearBackend != LinearBackendFake {
		t.Fatalf("LinearBackend = %q", cfg.LinearBackend)
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
	t.Setenv("COLIN_PROJECT_FILTER", "")

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

func TestLoadFromFileWorkflowStatesPartialOverride(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	content := `linear_api_token = "file-token"
linear_team_id = "file-team"

[workflow_states]
review = "Human Review"
refine = "Needs Spec"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")
	t.Setenv("COLIN_PROJECT_FILTER", "")

	cfg, err := LoadFromPath(configPath)
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}

	want := DefaultWorkflowStates()
	want.Review = "Human Review"
	want.Refine = "Needs Spec"
	if cfg.WorkflowStates != want {
		t.Fatalf("WorkflowStates = %#v, want %#v", cfg.WorkflowStates, want)
	}
}

func TestLoadFromPathRejectsDuplicateWorkflowStates(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	content := `linear_api_token = "file-token"
linear_team_id = "file-team"

[workflow_states]
todo = "Todo"
in_progress = "todo"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")
	t.Setenv("COLIN_PROJECT_FILTER", "")

	_, err := LoadFromPath(configPath)
	if err == nil {
		t.Fatal("LoadFromPath() error = nil, want duplicate workflow states error")
	}
	if !strings.Contains(err.Error(), "workflow_states") {
		t.Fatalf("error = %q, want workflow_states context", err.Error())
	}
}
