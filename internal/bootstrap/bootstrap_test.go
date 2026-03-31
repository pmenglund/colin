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
	})
	if err != nil {
		t.Fatalf("RenderWorkflow() error = %v", err)
	}

	for _, want := range []string{
		`project_slug: "project-1"`,
		"api_key: $LINEAR_API_KEY",
		"webhook_signing_secret: $LINEAR_WEBHOOK_SECRET",
		"codex_pr_reviews_enabled: false",
		`repo_url: "git@github.com:acme/repo.git"`,
		`base_ref: "main"`,
		"port: 7777",
		"COLIN_OUTCOME: READY_FOR_REVIEW",
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

	gotOutput := output.String()
	if !strings.Contains(gotOutput, "Wrote "+filepath.Join(tempDir, "WORKFLOW.md")) {
		t.Fatalf("output = %q, want written message", gotOutput)
	}
	if !strings.Contains(gotOutput, "Webhook setup skipped.") {
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
	if !strings.Contains(got, "run `colin setup linear`") {
		t.Fatalf("output = %q, want setup linear guidance", got)
	}
}
