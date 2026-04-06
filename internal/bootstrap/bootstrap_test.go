package bootstrap

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderWorkflowIncludesConfiguredValues(t *testing.T) {
	t.Parallel()

	got, err := RenderWorkflow(Answers{
		ProjectSlug:   "project-1",
		RepoURL:       "git@github.com:acme/repo.git",
		BaseRef:       "main",
		WorkspaceRoot: "./.colin/workspaces",
		ServerPort:    7777,
		WantsWebhook:  true,
		WebhookPort:   8998,
	})
	if err != nil {
		t.Fatalf("RenderWorkflow() error = %v", err)
	}

	for _, want := range []string{
		`project_slug: "project-1"`,
		"api_key: $LINEAR_API_KEY",
		"webhook_signing_secret: $LINEAR_WEBHOOK_SECRET",
		"# app_webhook_signing_secret: $LINEAR_APP_WEBHOOK_SECRET",
		"codex_pr_reviews_enabled: false",
		"api_token: $GITHUB_TOKEN",
		`repo_url: "git@github.com:acme/repo.git"`,
		`base_ref: "main"`,
		"port: 7777",
		"webhook_port: 8998",
		"# Optional: enable Slack issue summaries plus the Slack App Home view for tracked issues.",
		"#   bot_token: $SLACK_BOT_TOKEN",
		"#   channel_id: C0123456789",
		"#   signing_secret: $SLACK_SIGNING_SECRET",
		"COLIN_OUTCOME: READY_FOR_REVIEW",
		"## Why",
		"## Evidence",
		"Playwright screenshots",
		"text comments to Linear",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderWorkflow() missing %q in output:\n%s", want, got)
		}
	}
}

func TestRunWritesWorkflowAndPrintsWebhookSkip(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	var output bytes.Buffer
	input := strings.NewReader(strings.Join([]string{
		"project-1",
		"git@github.com:acme/repo.git",
		"main",
		"",
		"8888",
		"n",
		"y",
		"",
	}, "\n"))

	result, err := Run(input, &output, Options{
		WorkflowPath: filepath.Join(tempDir, "WORKFLOW.md"),
		WorkingDir:   tempDir,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.WroteWorkflow {
		t.Fatal("Run() did not report writing the workflow")
	}

	data, err := os.ReadFile(filepath.Join(tempDir, "WORKFLOW.md"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	gotFile := string(data)
	if !strings.Contains(gotFile, `project_slug: "project-1"`) {
		t.Fatalf("workflow file = %q, want project slug", gotFile)
	}
	if !strings.Contains(gotFile, `repo_url: "git@github.com:acme/repo.git"`) {
		t.Fatalf("workflow file = %q, want repo url", gotFile)
	}
	if strings.Contains(gotFile, "webhook_port:") {
		t.Fatalf("workflow file = %q, want webhook port omitted when webhooks are disabled", gotFile)
	}

	gotOutput := output.String()
	if !strings.Contains(gotOutput, "Workflow file: "+filepath.Join(tempDir, "WORKFLOW.md")) {
		t.Fatalf("output = %q, want workflow file summary", gotOutput)
	}
	if !strings.Contains(gotOutput, "Webhooks: setup skipped") {
		t.Fatalf("output = %q, want webhook skipped message", gotOutput)
	}
}

func TestRunDeclinesOverwrite(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	workflowPath := filepath.Join(tempDir, "WORKFLOW.md")
	original := "original\n"
	if err := os.WriteFile(workflowPath, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var output bytes.Buffer
	input := strings.NewReader(strings.Join([]string{
		"project-1",
		"git@github.com:acme/repo.git",
		"main",
		"",
		"8888",
		"n",
		"y",
		"n",
		"",
	}, "\n"))

	_, err := Run(input, &output, Options{
		WorkflowPath: workflowPath,
		WorkingDir:   tempDir,
	})
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("Run() error = %v, want ErrAborted", err)
	}

	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != original {
		t.Fatalf("workflow file = %q, want %q", string(data), original)
	}
}

func TestRunPrintsAutoStartWebhookGuidance(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	var output bytes.Buffer
	input := strings.NewReader(strings.Join([]string{
		"project-1",
		"git@github.com:acme/repo.git",
		"main",
		"",
		"8888",
		"y",
		"",
		"y",
		"",
	}, "\n"))

	_, err := Run(input, &output, Options{
		WorkflowPath: filepath.Join(tempDir, "WORKFLOW.md"),
		WorkingDir:   tempDir,
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := output.String()
	if !strings.Contains(got, "Webhook setup requires Tailscale.") {
		t.Fatalf("output = %q, want webhook guidance", got)
	}
	if !strings.Contains(got, "run `colin setup linear webhook`") {
		t.Fatalf("output = %q, want setup linear guidance", got)
	}
	data, err := os.ReadFile(filepath.Join(tempDir, "WORKFLOW.md"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "webhook_port: 8998") {
		t.Fatalf("workflow file = %q, want default webhook port", string(data))
	}
}

func TestRunPrintsGitHubSetupGuidanceWhenTokenMissing(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")

	tempDir := t.TempDir()
	var output bytes.Buffer
	input := strings.NewReader(strings.Join([]string{
		"project-1",
		"git@github.com:acme/repo.git",
		"main",
		"",
		"8888",
		"n",
		"y",
		"",
	}, "\n"))

	_, err := Run(input, &output, Options{
		WorkflowPath: filepath.Join(tempDir, "WORKFLOW.md"),
		WorkingDir:   tempDir,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := output.String()
	for _, want := range []string{
		"[ACTION] GITHUB_TOKEN or GH_TOKEN: export it before moving issues into `Review` or `Merge`",
		"fine-grained personal access token",
		"Contents: Read and write; Pull requests: Read and write",
		"colin setup repo",
		"[ACTION] GITHUB_TOKEN: export it before moving issues into `Review` or `Merge`.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestRunPrintsGitHubTokenConfiguredWhenPresent(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "github_pat_test-token")

	tempDir := t.TempDir()
	var output bytes.Buffer
	input := strings.NewReader(strings.Join([]string{
		"project-1",
		"git@github.com:acme/repo.git",
		"main",
		"",
		"8888",
		"n",
		"y",
		"",
	}, "\n"))

	_, err := Run(input, &output, Options{
		WorkflowPath: filepath.Join(tempDir, "WORKFLOW.md"),
		WorkingDir:   tempDir,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := output.String()
	if !strings.Contains(got, "[OK] GITHUB_TOKEN or GH_TOKEN: already configured in this shell") {
		t.Fatalf("output = %q, want configured token message", got)
	}
}

func TestDetectPrerequisitesRequiresExpectedTokenPrefixes(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "token")
	t.Setenv("GITHUB_TOKEN", "token")
	t.Setenv("GH_TOKEN", "")

	prereqs := detectPrerequisites()
	if prereqs.LinearAPIKeyConfigured {
		t.Fatal("LinearAPIKeyConfigured = true, want false for invalid prefix")
	}
	if prereqs.GitHubTokenConfigured {
		t.Fatal("GitHubTokenConfigured = true, want false for invalid prefix")
	}

	t.Setenv("LINEAR_API_KEY", "lin_api_valid")
	t.Setenv("GITHUB_TOKEN", "github_pat_valid")

	prereqs = detectPrerequisites()
	if !prereqs.LinearAPIKeyConfigured {
		t.Fatal("LinearAPIKeyConfigured = false, want true for lin_api_ token")
	}
	if !prereqs.GitHubTokenConfigured {
		t.Fatal("GitHubTokenConfigured = false, want true for github_pat_ token")
	}

	t.Setenv("GITHUB_TOKEN", "ghp_valid")

	prereqs = detectPrerequisites()
	if !prereqs.GitHubTokenConfigured {
		t.Fatal("GitHubTokenConfigured = false, want true for ghp_ token")
	}
}

func TestBuildConfigFromAnswersUsesSessionTokens(t *testing.T) {
	t.Parallel()

	_, cfg, err := buildConfigFromAnswers("WORKFLOW.md", Answers{
		ProjectSlug:   "project-1",
		RepoURL:       "git@github.com:acme/repo.git",
		BaseRef:       "main",
		WorkspaceRoot: "./.colin/workspaces",
		ServerPort:    8888,
	}, "lin_api_test", "github_pat_test")
	if err != nil {
		t.Fatalf("buildConfigFromAnswers() error = %v", err)
	}
	if got := cfg.Tracker.APIKey; got != "lin_api_test" {
		t.Fatalf("cfg.Tracker.APIKey = %q, want lin_api_test", got)
	}
	if got := cfg.Repo.APIToken; got != "github_pat_test" {
		t.Fatalf("cfg.Repo.APIToken = %q, want github_pat_test", got)
	}
}
