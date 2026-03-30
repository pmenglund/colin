package config

import (
	"path/filepath"
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
	if cfg.Repo.BranchTemplate != "colin/{{.issue.identifier}}-{{.issue.title}}" {
		t.Fatalf("cfg.Repo.BranchTemplate = %q", cfg.Repo.BranchTemplate)
	}
	if cfg.Server.Port == nil || *cfg.Server.Port != 8888 {
		t.Fatalf("cfg.Server.Port = %v, want 8888", cfg.Server.Port)
	}
}

func TestBuildReadsServerPublicURL(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{
		Config: map[string]any{
			"tracker": map[string]any{
				"kind":         "linear",
				"project_slug": "project-1",
				"api_key":      "token",
			},
			"server": map[string]any{
				"public_url": "https://colin.example.test",
			},
		},
	}

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Server.PublicURL; got != "https://colin.example.test" {
		t.Fatalf("cfg.Server.PublicURL = %q, want %q", got, "https://colin.example.test")
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

func TestBuildNormalizesTurnSandboxPolicy(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{
		Config: map[string]any{
			"tracker": map[string]any{
				"kind":         "linear",
				"project_slug": "cli",
				"api_key":      "token",
			},
			"codex": map[string]any{
				"turn_sandbox_policy": map[string]any{
					"mode": "danger-full-access",
				},
			},
		},
	}

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Codex.TurnSandboxPolicy["type"]; got != "dangerFullAccess" {
		t.Fatalf("sandbox policy type = %v", got)
	}
	if _, ok := cfg.Codex.TurnSandboxPolicy["mode"]; ok {
		t.Fatal("sandbox policy still contains mode")
	}
}

func TestBuildMakesWorkspaceRootAbsolute(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{
		Config: map[string]any{
			"tracker": map[string]any{
				"kind":         "linear",
				"project_slug": "cli",
				"api_key":      "token",
			},
			"workspace": map[string]any{
				"root": "./.colin/workspaces",
			},
		},
	}

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want, err := filepath.Abs(filepath.Clean("./.colin/workspaces"))
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	if cfg.Workspace.Root != want {
		t.Fatalf("cfg.Workspace.Root = %q, want %q", cfg.Workspace.Root, want)
	}
}

func TestCandidateStatesIncludesRepoAutomationStatesOnce(t *testing.T) {
	t.Parallel()

	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo", "In Progress", "Review"}},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
	}

	got := CandidateStates(cfg)
	want := []string{"Todo", "In Progress", "Review", "Merge"}
	if len(got) != len(want) {
		t.Fatalf("CandidateStates() len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CandidateStates()[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

func TestBuildReadsPRTemplate(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{
		Config: map[string]any{
			"tracker": map[string]any{
				"kind":         "linear",
				"project_slug": "project-1",
				"api_key":      "token",
			},
			"repo": map[string]any{
				"pr_template": "Issue {{.issue.identifier}}",
			},
		},
	}

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Repo.PRTemplate; got != "Issue {{.issue.identifier}}" {
		t.Fatalf("cfg.Repo.PRTemplate = %q, want %q", got, "Issue {{.issue.identifier}}")
	}
}

func TestBuildReadsBranchTemplate(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{
		Config: map[string]any{
			"tracker": map[string]any{
				"kind":         "linear",
				"project_slug": "project-1",
				"api_key":      "token",
			},
			"repo": map[string]any{
				"branch_template": "feature/{{.issue.identifier}}",
			},
		},
	}

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Repo.BranchTemplate; got != "feature/{{.issue.identifier}}" {
		t.Fatalf("cfg.Repo.BranchTemplate = %q, want %q", got, "feature/{{.issue.identifier}}")
	}
}
