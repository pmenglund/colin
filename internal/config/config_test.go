package config

import (
	"testing"

	"github.com/pmenglund/colin/internal/domain"
)

func TestBuildResolvesEnvAndDefaults(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "token-from-env")
	t.Setenv("WS_ROOT", "/tmp/colin-workspaces")

	def := domain.WorkflowDefinition{
		Config: map[string]any{
			"tracker": map[string]any{
				"kind":         "linear",
				"project_slug": "cli",
				"api_key":      "$LINEAR_API_KEY",
			},
			"workspace": map[string]any{
				"root": "$WS_ROOT",
			},
		},
	}

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if cfg.Tracker.APIKey != "token-from-env" {
		t.Fatalf("cfg.Tracker.APIKey = %q", cfg.Tracker.APIKey)
	}
	if cfg.Workspace.Root != "/tmp/colin-workspaces" {
		t.Fatalf("cfg.Workspace.Root = %q", cfg.Workspace.Root)
	}
	if cfg.Agent.MaxTurns != 20 {
		t.Fatalf("cfg.Agent.MaxTurns = %d", cfg.Agent.MaxTurns)
	}
}

func TestBuildRejectsPartialWorkspaceGitConfig(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{
		Config: map[string]any{
			"workspace": map[string]any{
				"repo_url": "git@example.com/repo.git",
			},
		},
	}

	_, err := Build(def, "WORKFLOW.md")
	if err != ErrInvalidWorkspaceGitConf {
		t.Fatalf("Build() error = %v, want %v", err, ErrInvalidWorkspaceGitConf)
	}
}

func TestValidateDispatchRequiresTrackerAndCodex(t *testing.T) {
	t.Parallel()

	cfg := domain.ServiceConfig{}
	if err := ValidateDispatch(cfg); err != ErrUnsupportedTrackerKind {
		t.Fatalf("ValidateDispatch() error = %v", err)
	}

	cfg.Tracker.Kind = "linear"
	cfg.Tracker.APIKey = "token"
	cfg.Tracker.ProjectSlug = "cli"
	cfg.Codex.Command = " "
	if err := ValidateDispatch(cfg); err != ErrMissingCodexCommand {
		t.Fatalf("ValidateDispatch() error = %v", err)
	}
}
