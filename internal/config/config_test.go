package config

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	"github.com/pmenglund/colin/internal/repohost/builtin"
	"github.com/pmenglund/colin/internal/workflow"
	"gopkg.in/yaml.v3"
)

func workflowDefinition(t *testing.T, raw map[string]any) domain.WorkflowDefinition {
	t.Helper()
	builtin.Register()

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

type fakeConfigAdapter struct{}

func (fakeConfigAdapter) Kind() repohost.HostKind { return "configtest" }
func (fakeConfigAdapter) DisplayName() string     { return "Config Test" }
func (fakeConfigAdapter) CurrentToken() string    { return "" }
func (fakeConfigAdapter) IsValidToken(string) bool {
	return true
}
func (fakeConfigAdapter) RecommendedEnvVar() string    { return "CONFIGTEST_TOKEN" }
func (fakeConfigAdapter) ValidateTokenMessage() string { return "" }
func (fakeConfigAdapter) RenderSetupInstructions(repohost.Repository, string) string {
	return ""
}
func (fakeConfigAdapter) NewClient(domain.ServiceConfig, *slog.Logger) (repohost.Client, error) {
	return nil, nil
}
func (fakeConfigAdapter) ParsePullRequestURL(string) (string, string, int, bool) {
	return "", "", 0, false
}
func (fakeConfigAdapter) ParseRepositoryURL(raw string) (repohost.Repository, error) {
	if strings.TrimSpace(raw) != "configtest://scm/acme/widgets" {
		return repohost.Repository{}, repohost.ErrUnsupportedRepositoryURL
	}
	return repohost.Repository{
		Host:  "configtest.example",
		Owner: "acme",
		Name:  "widgets",
		URL:   raw,
	}, nil
}

type fakeConfigClient struct{}

func (fakeConfigClient) ValidateAuth(context.Context) error { return nil }
func (fakeConfigClient) PullRequestByHead(context.Context, string, string, string, string) (*repohost.PullRequest, error) {
	return nil, nil
}
func (fakeConfigClient) PullRequestByNumber(context.Context, string, string, int) (*repohost.PullRequest, error) {
	return nil, nil
}
func (fakeConfigClient) CreatePullRequest(context.Context, string, string, repohost.CreatePullRequestInput) (*repohost.PullRequest, error) {
	return nil, nil
}
func (fakeConfigClient) MergePullRequest(context.Context, string, string, int, string) error {
	return nil
}
func (fakeConfigClient) BranchExists(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (fakeConfigClient) ReviewThreads(context.Context, string, string, int, string) (repohost.ReviewThreadPage, error) {
	return repohost.ReviewThreadPage{}, nil
}
func (fakeConfigClient) ReviewThreadComments(context.Context, string, string) (repohost.ReviewThreadCommentPage, error) {
	return repohost.ReviewThreadCommentPage{}, nil
}
func (fakeConfigClient) PullRequestReactions(context.Context, string, string, int, string) (repohost.ReactionPage, error) {
	return repohost.ReactionPage{}, nil
}
func (fakeConfigClient) ReplyToReviewThread(context.Context, string, string) error { return nil }
func (fakeConfigClient) ResolveReviewThread(context.Context, string) error         { return nil }

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
	if got := cfg.Agent.MaxConcurrentAgentsByState[StateKey("Merge")]; got != 1 {
		t.Fatalf("cfg.Agent.MaxConcurrentAgentsByState[merge] = %d, want 1", got)
	}
	if cfg.Agent.CreateExecPlan {
		t.Fatal("cfg.Agent.CreateExecPlan = true, want false by default")
	}
	if cfg.Codex.CLICommand != "codex" {
		t.Fatalf("cfg.Codex.CLICommand = %q, want %q", cfg.Codex.CLICommand, "codex")
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
	if cfg.Slack.BotToken != "" || cfg.Slack.AppToken != "" || cfg.Slack.ChannelID != "" {
		t.Fatalf("cfg.Slack = %#v, want disabled by default", cfg.Slack)
	}
}

func TestBuildAppliesTrackerAppMode(t *testing.T) {
	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":            "linear",
			"project_slug":    "cli",
			"api_key":         "token",
			"oauth_client_id": "client-123",
			"app_mode":        true,
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !cfg.Tracker.AppMode {
		t.Fatal("cfg.Tracker.AppMode = false, want true")
	}
	if cfg.Tracker.OAuthClientID != "client-123" {
		t.Fatalf("cfg.Tracker.OAuthClientID = %q, want %q", cfg.Tracker.OAuthClientID, "client-123")
	}
}

func TestBuildAllowsExplicitMergeConcurrencyOverride(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "cli",
			"api_key":      "token",
		},
		"agent": map[string]any{
			"max_concurrent_agents_by_state": map[string]any{
				"Merge": 3,
			},
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Agent.MaxConcurrentAgentsByState[StateKey("Merge")]; got != 3 {
		t.Fatalf("cfg.Agent.MaxConcurrentAgentsByState[merge] = %d, want 3", got)
	}
}

func TestBuildReadsSlackConfigFromEnv(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test-token")
	t.Setenv("SLACK_APP_TOKEN", "xapp-test-token")
	t.Setenv("SLACK_SIGNING_SECRET", "slack-signing-secret")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"slack": map[string]any{
			"bot_token":      "$SLACK_BOT_TOKEN",
			"app_token":      "$SLACK_APP_TOKEN",
			"channel_id":     "C12345678",
			"signing_secret": "$SLACK_SIGNING_SECRET",
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Slack.BotToken; got != "xoxb-test-token" {
		t.Fatalf("cfg.Slack.BotToken = %q, want %q", got, "xoxb-test-token")
	}
	if got := cfg.Slack.AppToken; got != "xapp-test-token" {
		t.Fatalf("cfg.Slack.AppToken = %q, want %q", got, "xapp-test-token")
	}
	if got := cfg.Slack.ChannelID; got != "C12345678" {
		t.Fatalf("cfg.Slack.ChannelID = %q, want %q", got, "C12345678")
	}
	if got := cfg.Slack.SigningSecret; got != "slack-signing-secret" {
		t.Fatalf("cfg.Slack.SigningSecret = %q, want %q", got, "slack-signing-secret")
	}
}

func TestBuildRejectsPartialSlackConfig(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"slack": map[string]any{
			"channel_id": "C12345678",
		},
	})

	_, err := Build(def, "WORKFLOW.md")
	if err == nil {
		t.Fatal("Build() error = nil, want invalid slack config")
	}
	if !strings.Contains(err.Error(), "slack.bot_token and slack.channel_id must both be set") {
		t.Fatalf("Build() error = %v, want slack config guidance", err)
	}
}

func TestBuildRejectsSlackConfigWithoutAppToken(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test-token")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"slack": map[string]any{
			"bot_token":  "$SLACK_BOT_TOKEN",
			"channel_id": "C12345678",
		},
	})

	_, err := Build(def, "WORKFLOW.md")
	if err == nil {
		t.Fatal("Build() error = nil, want invalid slack config")
	}
	if !strings.Contains(err.Error(), "slack.app_token must be set when Slack is enabled") {
		t.Fatalf("Build() error = %v, want missing slack app token guidance", err)
	}
}

func TestBuildRejectsSlackAppTokenWithoutSummaryConfig(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-test-token")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "project-1",
			"api_key":      "token",
		},
		"slack": map[string]any{
			"app_token": "$SLACK_APP_TOKEN",
		},
	})

	_, err := Build(def, "WORKFLOW.md")
	if err == nil {
		t.Fatal("Build() error = nil, want invalid slack app token config")
	}
	if !strings.Contains(err.Error(), "slack.app_token requires slack.bot_token and slack.channel_id") {
		t.Fatalf("Build() error = %v, want slack app token guidance", err)
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

func TestBuildReadsTrackerAppWebhookSigningSecretFromEnv(t *testing.T) {
	t.Setenv("LINEAR_APP_WEBHOOK_SECRET", "app-secret-from-env")

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":                       "linear",
			"project_slug":               "project-1",
			"api_key":                    "token",
			"app_webhook_signing_secret": "$LINEAR_APP_WEBHOOK_SECRET",
		},
	},
	)

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Tracker.AppWebhookSigningSecret; got != "app-secret-from-env" {
		t.Fatalf("cfg.Tracker.AppWebhookSigningSecret = %q, want %q", got, "app-secret-from-env")
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

func TestBuildNormalizesExplicitTargets(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":    "linear",
			"api_key": "token",
		},
		"targets": []map[string]any{
			{
				"name":                     "api",
				"project_slug":             "project-1",
				"repo_url":                 "git@github.com:acme/api.git",
				"base_ref":                 "main",
				"checkout_path":            "./api-checkout",
				"remote_name":              "upstream",
				"merge_method":             "rebase",
				"branch_template":          "feature/{{.issue.identifier}}",
				"pr_template":              "target pr {{.issue.identifier}}",
				"codex_pr_reviews_enabled": true,
			},
			{
				"name":         "web",
				"project_slug": "project-2",
				"repo_url":     "git@github.com:acme/web.git",
				"base_ref":     "trunk",
			},
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("len(cfg.Targets) = %d, want 2", len(cfg.Targets))
	}
	wantCheckout, err := filepath.Abs(filepath.Clean("./api-checkout"))
	if err != nil {
		t.Fatalf("filepath.Abs(checkout) error = %v", err)
	}
	if cfg.Targets[0].ProjectSlug != "project-1" || cfg.Targets[0].RepoURL != "git@github.com:acme/api.git" || cfg.Targets[0].BaseRef != "main" || cfg.Targets[0].CheckoutPath != wantCheckout || cfg.Targets[0].RemoteName != "upstream" || cfg.Targets[0].MergeMethod != "rebase" || cfg.Targets[0].BranchTemplate != "feature/{{.issue.identifier}}" || cfg.Targets[0].PRTemplate != "target pr {{.issue.identifier}}" || !cfg.Targets[0].CodexPRReviewsEnabled {
		t.Fatalf("cfg.Targets[0] = %+v", cfg.Targets[0])
	}
	if cfg.Targets[1].ProjectSlug != "project-2" || cfg.Targets[1].RepoURL != "git@github.com:acme/web.git" || cfg.Targets[1].BaseRef != "trunk" {
		t.Fatalf("cfg.Targets[1] = %+v", cfg.Targets[1])
	}
}

func TestBuildDerivesTargetKeyWithConfiguredRepoBackend(t *testing.T) {
	repohost.Register(fakeConfigAdapter{})

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":    "linear",
			"api_key": "token",
		},
		"repo": map[string]any{
			"backend": "configtest",
		},
		"targets": []map[string]any{
			{
				"project_slug": "project-1",
				"repo_url":     "configtest://scm/acme/widgets",
				"base_ref":     "main",
			},
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("len(cfg.Targets) = %d, want 1", len(cfg.Targets))
	}
	if got := cfg.Targets[0].Key; got != "project-1-widgets" {
		t.Fatalf("cfg.Targets[0].Key = %q, want %q", got, "project-1-widgets")
	}
}

func TestBuildRejectsMixedLegacyAndTargets(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"api_key":      "token",
			"project_slug": "project-1",
		},
		"workspace": map[string]any{
			"repo_url": "git@github.com:acme/api.git",
			"base_ref": "main",
		},
		"targets": []map[string]any{
			{
				"project_slug": "project-2",
				"repo_url":     "git@github.com:acme/web.git",
				"base_ref":     "trunk",
			},
		},
	})

	_, err := Build(def, "WORKFLOW.md")
	if !errors.Is(err, ErrInvalidWorkflowConfig) {
		t.Fatalf("Build() error = %v, want ErrInvalidWorkflowConfig", err)
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

func TestBuildReadsTargetCodexSecurityOverrides(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":    "linear",
			"api_key": "token",
		},
		"codex": map[string]any{
			"command":         "codex app-server",
			"approval_policy": "never",
			"thread_sandbox":  "danger-full-access",
			"turn_sandbox_policy": map[string]any{
				"type": "dangerFullAccess",
			},
		},
		"targets": []map[string]any{
			{
				"project_slug": "api-project",
				"repo_url":     "git@github.com:acme/api.git",
				"base_ref":     "main",
				"codex": map[string]any{
					"approval_policy": "on-request",
					"thread_sandbox":  "read-only",
					"turn_sandbox_policy": map[string]any{
						"mode": "workspace-write",
					},
				},
			},
			{
				"project_slug": "web-project",
				"repo_url":     "git@github.com:acme/web.git",
				"base_ref":     "main",
			},
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("len(cfg.Targets) = %d, want 2", len(cfg.Targets))
	}
	if got := cfg.Targets[0].CodexSecurity.ApprovalPolicy; got != "on-request" {
		t.Fatalf("target approval policy = %q, want on-request", got)
	}
	if got := cfg.Targets[0].CodexSecurity.ThreadSandbox; got != "read-only" {
		t.Fatalf("target thread sandbox = %q, want read-only", got)
	}
	if got := cfg.Targets[0].CodexSecurity.TurnSandboxPolicy.Type; got != "workspaceWrite" {
		t.Fatalf("target turn sandbox type = %q, want workspaceWrite", got)
	}
	if got := cfg.Targets[1].CodexSecurity; got != (domain.CodexSecurityPolicy{}) {
		t.Fatalf("second target CodexSecurity = %#v, want zero value", got)
	}

	apiCodex, err := domain.ResolveCodexConfigForIssue(cfg, domain.Issue{ProjectSlug: "api-project"})
	if err != nil {
		t.Fatalf("ResolveCodexConfigForIssue(api) error = %v", err)
	}
	if apiCodex.Command != "codex app-server" {
		t.Fatalf("api Codex command = %q, want inherited command", apiCodex.Command)
	}
	if apiCodex.ApprovalPolicy != "on-request" {
		t.Fatalf("api approval policy = %q, want on-request", apiCodex.ApprovalPolicy)
	}
	if apiCodex.ThreadSandbox != "read-only" {
		t.Fatalf("api thread sandbox = %q, want read-only", apiCodex.ThreadSandbox)
	}
	if apiCodex.TurnSandboxPolicy.Type != "workspaceWrite" {
		t.Fatalf("api turn sandbox type = %q, want workspaceWrite", apiCodex.TurnSandboxPolicy.Type)
	}

	webCodex, err := domain.ResolveCodexConfigForIssue(cfg, domain.Issue{ProjectSlug: "web-project"})
	if err != nil {
		t.Fatalf("ResolveCodexConfigForIssue(web) error = %v", err)
	}
	if webCodex.ApprovalPolicy != "never" {
		t.Fatalf("web approval policy = %q, want inherited never", webCodex.ApprovalPolicy)
	}
	if webCodex.ThreadSandbox != "danger-full-access" {
		t.Fatalf("web thread sandbox = %q, want inherited danger-full-access", webCodex.ThreadSandbox)
	}
	if webCodex.TurnSandboxPolicy.Type != "dangerFullAccess" {
		t.Fatalf("web turn sandbox type = %q, want inherited dangerFullAccess", webCodex.TurnSandboxPolicy.Type)
	}
}

func TestBuildReadsCodexCLICommand(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "cli",
			"api_key":      "token",
		},
		"codex": map[string]any{
			"cli_command": "my-codex --profile local",
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := cfg.Codex.CLICommand; got != "my-codex --profile local" {
		t.Fatalf("cfg.Codex.CLICommand = %q, want %q", got, "my-codex --profile local")
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

func TestBuildReadsWorkspaceRepoCacheRootAndCheckoutPath(t *testing.T) {
	t.Parallel()

	def := workflowDefinition(t, map[string]any{
		"tracker": map[string]any{
			"kind":         "linear",
			"project_slug": "cli",
			"api_key":      "token",
		},
		"workspace": map[string]any{
			"root":            "./.colin/workspaces",
			"repo_url":        "git@github.com:acme/repo.git",
			"base_ref":        "main",
			"repo_cache_root": "./.colin/_repos",
			"checkout_path":   "./repo-checkout",
		},
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	wantCache, err := filepath.Abs(filepath.Clean("./.colin/_repos"))
	if err != nil {
		t.Fatalf("filepath.Abs(repo cache) error = %v", err)
	}
	if cfg.Workspace.RepoCacheRoot != wantCache {
		t.Fatalf("cfg.Workspace.RepoCacheRoot = %q, want %q", cfg.Workspace.RepoCacheRoot, wantCache)
	}
	wantCheckout, err := filepath.Abs(filepath.Clean("./repo-checkout"))
	if err != nil {
		t.Fatalf("filepath.Abs(checkout) error = %v", err)
	}
	if cfg.Workspace.CheckoutPath != wantCheckout {
		t.Fatalf("cfg.Workspace.CheckoutPath = %q, want %q", cfg.Workspace.CheckoutPath, wantCheckout)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].CheckoutPath != wantCheckout {
		t.Fatalf("cfg.Targets = %+v, want checkout path %q", cfg.Targets, wantCheckout)
	}
}

func TestBuildDefaultsWorkspaceRepoCacheRootNextToWorkspaceRoot(t *testing.T) {
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
	})

	cfg, err := Build(def, "WORKFLOW.md")
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want, err := filepath.Abs(filepath.Clean("./.colin/_repos"))
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	if cfg.Workspace.RepoCacheRoot != want {
		t.Fatalf("cfg.Workspace.RepoCacheRoot = %q, want %q", cfg.Workspace.RepoCacheRoot, want)
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
	if len(cfg.Targets) != 1 || cfg.Targets[0].PRTemplate != "Issue {{.issue.identifier}}" {
		t.Fatalf("cfg.Targets = %+v, want inherited PR template", cfg.Targets)
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
	if len(cfg.Targets) != 1 || cfg.Targets[0].BranchTemplate != "feature/{{.issue.identifier}}" {
		t.Fatalf("cfg.Targets = %+v, want inherited branch template", cfg.Targets)
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
	if len(cfg.Targets) != 1 || !cfg.Targets[0].CodexPRReviewsEnabled {
		t.Fatalf("cfg.Targets = %+v, want inherited codex PR review setting", cfg.Targets)
	}
}
