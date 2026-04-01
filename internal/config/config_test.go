package config

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/workflow"
	"gopkg.in/yaml.v3"
)

func workflowDefinition(t *testing.T, raw map[string]any) domain.WorkflowDefinition {
	t.Helper()

	var body bytes.Buffer
	body.WriteString("---\n")
	if err := yaml.NewEncoder(&body).Encode(raw); err != nil {
		t.Fatalf("encode workflow config: %v", err)
	}
	body.WriteString("---\n")

	def, err := workflow.Parse("WORKFLOW.md", body.Bytes())
	if err != nil {
		t.Fatalf("workflow.Parse() error = %v", err)
	}
	return def
}

func TestBuildResolvesEnvAndDefaults(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "token-from-env")
	t.Setenv("WS_ROOT", "/tmp/colin-workspaces")
	t.Setenv("GITHUB_TOKEN", "github-token-from-env")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "cli",
			"api_key":      "$LINEAR_API_KEY",
		},
		"workspace": map[string]any{
			"root": "$WS_ROOT",
		},
	},
	)

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
	if cfg.Agent.CreateExecPlan {
		t.Fatal("cfg.Agent.CreateExecPlan = true, want false by default")
	}
	if cfg.Repo.BranchTemplate != "colin/{{.issue.identifier}}-{{.issue.title}}" {
		t.Fatalf("cfg.Repo.BranchTemplate = %q", cfg.Repo.BranchTemplate)
	}
	if cfg.Repo.APIToken != "github-token-from-env" {
		t.Fatalf("cfg.Repo.APIToken = %q", cfg.Repo.APIToken)
	}
	if cfg.Repo.Backend != "github" {
		t.Fatalf("cfg.Repo.Backend = %q, want %q", cfg.Repo.Backend, "github")
	}
	if cfg.Repo.CodexPRReviewsEnabled {
		t.Fatal("cfg.Repo.CodexPRReviewsEnabled = true, want false by default")
	}
	if cfg.Server.Port == nil || *cfg.Server.Port != 8888 {
		t.Fatalf("cfg.Server.Port = %v, want 8888", cfg.Server.Port)
	}
	if cfg.Server.WebhookPort != nil {
		t.Fatalf("cfg.Server.WebhookPort = %v, want nil by default", cfg.Server.WebhookPort)
	}
	if cfg.Server.LogBufferLines != domain.DefaultLogBufferLines {
		t.Fatalf("cfg.Server.LogBufferLines = %d, want %d", cfg.Server.LogBufferLines, domain.DefaultLogBufferLines)
	}
}

func TestBuildReadsWebhookPort(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"server": map[string]any{
			"port":         8888,
			"webhook_port": 8998,
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if cfg.Server.Port == nil || *cfg.Server.Port != 8888 {
		t.Fatalf("cfg.Server.Port = %v, want 8888", cfg.Server.Port)
	}
	if cfg.Server.WebhookPort == nil || *cfg.Server.WebhookPort != 8998 {
		t.Fatalf("cfg.Server.WebhookPort = %v, want 8998", cfg.Server.WebhookPort)
	}
}

func TestBuildReadsServerWebhookAndUIURLs(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"server": map[string]any{
			"webhook_public_url": "https://hooks.colin.example.test",
			"ui_url":             "https://ui.colin.example.test",
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Server.WebhookPublicURL; got != "https://hooks.colin.example.test" {
		t.Fatalf("cfg.Server.WebhookPublicURL = %q, want %q", got, "https://hooks.colin.example.test")
	}
	if got := cfg.Server.UIURL; got != "https://ui.colin.example.test" {
		t.Fatalf("cfg.Server.UIURL = %q, want %q", got, "https://ui.colin.example.test")
	}
}

func TestBuildReadsTrackerWebhookSigningSecretFromEnv(t *testing.T) {
	t.Setenv("LINEAR_WEBHOOK_SECRET", "secret-from-env")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":                   "linear",
			"project_slug":           "project-1",
			"api_key":                "token",
			"webhook_signing_secret": "$LINEAR_WEBHOOK_SECRET",
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Tracker.WebhookSigningSecret; got != "secret-from-env" {
		t.Fatalf("cfg.Tracker.WebhookSigningSecret = %q, want %q", got, "secret-from-env")
	}
}

func TestBuildReadsRepoWebhookSigningSecretFromEnv(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "github-secret-from-env")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"repo": map[string]any{
			"webhook_signing_secret": "$GITHUB_WEBHOOK_SECRET",
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Repo.WebhookSigningSecret; got != "github-secret-from-env" {
		t.Fatalf("cfg.Repo.WebhookSigningSecret = %q, want %q", got, "github-secret-from-env")
	}
}

func TestBuildReadsDeprecatedServerPublicURLFallback(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"server": map[string]any{
			"public_url": "https://colin.example.test",
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Server.PublicURL; got != "https://colin.example.test" {
		t.Fatalf("cfg.Server.PublicURL = %q, want %q", got, "https://colin.example.test")
	}
}

func TestBuildReadsServerLogBufferLines(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"server": map[string]any{
			"log_buffer_lines": 250,
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Server.LogBufferLines; got != 250 {
		t.Fatalf("cfg.Server.LogBufferLines = %d, want %d", got, 250)
	}
}

func TestBuildRejectsPartialWorkspaceGitConfig(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"workspace": map[string]any{
			"repo_url": "git@example.com/repo.git",
		},
	},
	)

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

	def := workflowDefinition(t, map[string]any{
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
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Codex.TurnSandboxPolicy.Type; got != "dangerFullAccess" {
		t.Fatalf("sandbox policy type = %v", got)
	}
}

func TestBuildMakesWorkspaceRootAbsolute(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "cli",
			"api_key":      "token",
		},
		"workspace": map[string]any{
			"root": "./.colin/workspaces",
		},
	},
	)

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

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"repo": map[string]any{
			"pr_template": "Issue {{.issue.identifier}}",
		},
	},
	)

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

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"repo": map[string]any{
			"branch_template": "feature/{{.issue.identifier}}",
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Repo.BranchTemplate; got != "feature/{{.issue.identifier}}" {
		t.Fatalf("cfg.Repo.BranchTemplate = %q, want %q", got, "feature/{{.issue.identifier}}")
	}
}

func TestBuildReadsRepoAPITokenFromConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "fallback-token")
	t.Setenv("COLIN_GITHUB_TOKEN", "config-token")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"repo": map[string]any{
			"api_token": "$COLIN_GITHUB_TOKEN",
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Repo.APIToken; got != "config-token" {
		t.Fatalf("cfg.Repo.APIToken = %q, want %q", got, "config-token")
	}
}

func TestBuildReadsRepoBackendAndAPIBaseURL(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"repo": map[string]any{
			"backend":      "github",
			"api_base_url": "https://github.example.test/api/v3/",
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Repo.Backend; got != "github" {
		t.Fatalf("cfg.Repo.Backend = %q, want %q", got, "github")
	}
	if got := cfg.Repo.APIBaseURL; got != "https://github.example.test/api/v3/" {
		t.Fatalf("cfg.Repo.APIBaseURL = %q, want %q", got, "https://github.example.test/api/v3/")
	}
}

func TestBuildFallsBackToGHToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gh-token")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Repo.APIToken; got != "gh-token" {
		t.Fatalf("cfg.Repo.APIToken = %q, want %q", got, "gh-token")
	}
}

func TestBuildReadsCreateExecPlan(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"agent": map[string]any{
			"create_exec_plan": true,
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !cfg.Agent.CreateExecPlan {
		t.Fatal("cfg.Agent.CreateExecPlan = false, want true")
	}
}

func TestBuildReadsCodexPRReviewsEnabled(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"repo": map[string]any{
			"codex_pr_reviews_enabled": true,
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !cfg.Repo.CodexPRReviewsEnabled {
		t.Fatal("cfg.Repo.CodexPRReviewsEnabled = false, want true")
	}
}
