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
		RepoCacheRoot: "./.colin/_repos",
		ServerPort:    7777,
		WantsWebhook:  true,
		WebhookPort:   8998,
	})
	if err != nil {
		t.Fatalf("RenderWorkflow() error = %v", err)
	}

	for _, want := range []string{
		"targets:",
		`name: "project-1"`,
		`project_slug: "project-1"`,
		"api_key: $LINEAR_API_KEY",
		"webhook_signing_secret: $LINEAR_WEBHOOK_SECRET",
		"# app_webhook_signing_secret: $LINEAR_APP_WEBHOOK_SECRET",
		"codex_pr_reviews_enabled: false",
		"api_token: $GITHUB_TOKEN",
		`repo_url: "git@github.com:acme/repo.git"`,
		`base_ref: "main"`,
		`repo_cache_root: "./.colin/_repos"`,
		"# checkout_path: /path/to/existing/checkout",
		"port: 7777",
		"webhook_port: 8998",
		"# Optional: enable Slack issue summaries plus the Slack App Home view for tracked issues.",
		"#   bot_token: $SLACK_BOT_TOKEN",
		"#   channel_id: C0123456789",
		"#   signing_secret: $SLACK_SIGNING_SECRET",
		"prompts:",
		"coding_fallback: |",
		"coding_continuation: |",
		"exec_plan_decision: |",
		"exec_plan_decision_retry: |",
		"exec_plan_generation: |",
		"exec_plan_tracking: |",
		"merge_recovery: |",
		"merge_recovery_continuation: |",
		"{{.issue.identifier}}",
		"{{.exec_plan_decisions.exec_plan_line}}",
		"COLIN_OUTCOME: READY_FOR_REVIEW",
		"## Why",
		"## Evidence",
		"Playwright screenshot path",
		"Screenshot blocker:",
		"browser-visible changes",
		"text comments to Linear",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderWorkflow() missing %q in output:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"  project_slug: \"project-1\"\n  active_states:",
		"workspace:\n  root: \"./.colin/workspaces\"\n  repo_url:",
		"repo:\n  backend: \"github\"\n  api_token: $GITHUB_TOKEN\n  publish_states:\n    - Review\n  merge_states:\n    - Merge\n  remote_name:",
		"repo:\n  backend: \"github\"\n  api_token: $GITHUB_TOKEN\n  publish_states:\n    - Review\n  merge_states:\n    - Merge\n  branch_template:",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("RenderWorkflow() unexpectedly contained legacy config %q in output:\n%s", unwanted, got)
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
		"",
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
	if !strings.Contains(gotFile, "targets:") {
		t.Fatalf("workflow file = %q, want targets section", gotFile)
	}
	if !strings.Contains(gotFile, `project_slug: "project-1"`) {
		t.Fatalf("workflow file = %q, want project slug", gotFile)
	}
	if !strings.Contains(gotFile, `repo_url: "git@github.com:acme/repo.git"`) {
		t.Fatalf("workflow file = %q, want repo url", gotFile)
	}
	if strings.Contains(gotFile, "  project_slug: \"project-1\"\n  active_states:") {
		t.Fatalf("workflow file = %q, want project slug only in targets", gotFile)
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
		RepoCacheRoot: "./.colin/_repos",
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
	if len(cfg.Targets) != 1 {
		t.Fatalf("len(cfg.Targets) = %d, want 1", len(cfg.Targets))
	}
	if got := cfg.Targets[0].Name; got != "project-1" {
		t.Fatalf("cfg.Targets[0].Name = %q, want project-1", got)
	}
	if got := cfg.Targets[0].ProjectSlug; got != "project-1" {
		t.Fatalf("cfg.Targets[0].ProjectSlug = %q, want project-1", got)
	}
	if got := cfg.Targets[0].RepoURL; got != "git@github.com:acme/repo.git" {
		t.Fatalf("cfg.Targets[0].RepoURL = %q, want git@github.com:acme/repo.git", got)
	}
	if got := cfg.Targets[0].BaseRef; got != "main" {
		t.Fatalf("cfg.Targets[0].BaseRef = %q, want main", got)
	}
	if got := cfg.Targets[0].RemoteName; got != "origin" {
		t.Fatalf("cfg.Targets[0].RemoteName = %q, want origin", got)
	}
	if got := cfg.Targets[0].MergeMethod; got != "squash" {
		t.Fatalf("cfg.Targets[0].MergeMethod = %q, want squash", got)
	}
	if got := cfg.Targets[0].BranchTemplate; got != "colin/{{.issue.title}}" {
		t.Fatalf("cfg.Targets[0].BranchTemplate = %q, want colin/{{.issue.title}}", got)
	}
	wantRepoCacheRoot, err := filepath.Abs(filepath.Clean("./.colin/_repos"))
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	if got := cfg.Workspace.RepoCacheRoot; got != wantRepoCacheRoot {
		t.Fatalf("cfg.Workspace.RepoCacheRoot = %q, want %q", got, wantRepoCacheRoot)
	}
}
