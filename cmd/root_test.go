package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/domain"
)

func emptyInput() *strings.Reader {
	return strings.NewReader("")
}

func TestAnnounceStartupPrintsDashboardURL(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)

	var url atomic.Pointer[string]
	runErrCh := make(chan error, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		value := "http://127.0.0.1:9999"
		url.Store(&value)
	}()

	exited, err := announceStartup(cmd, true, func() string {
		current := url.Load()
		if current == nil {
			return ""
		}
		return *current
	}, func() string {
		current := url.Load()
		if current == nil {
			return ""
		}
		return *current + "/setup/funnel"
	}, runErrCh)
	if exited {
		t.Fatal("announceStartup() reported service exit before startup announcement")
	}
	if err != nil {
		t.Fatalf("announceStartup() error = %v", err)
	}

	got := stdout.String()
	want := "Colin is running. Web UI: http://127.0.0.1:9999 Setup: http://127.0.0.1:9999/setup/funnel\n"
	if got != want {
		t.Fatalf("startup output = %q, want %q", got, want)
	}
}

func TestAnnounceStartupReturnsRunErrorBeforeDashboardReady(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)

	runErrCh := make(chan error, 1)
	wantErr := errors.New("boom")
	runErrCh <- wantErr

	exited, err := announceStartup(cmd, true, func() string { return "" }, func() string { return "" }, runErrCh)
	if !exited {
		t.Fatal("announceStartup() reported startup announcement instead of service exit")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("announceStartup() error = %v, want %v", err, wantErr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("startup output = %q, want empty", stdout.String())
	}
}

func TestAnnounceStartupWithoutDashboardPrintsRunningMessage(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)

	runErrCh := make(chan error)

	exited, err := announceStartup(cmd, false, func() string { return "" }, func() string { return "" }, runErrCh)
	if exited {
		t.Fatal("announceStartup() reported service exit for dashboard-disabled service")
	}
	if err != nil {
		t.Fatalf("announceStartup() error = %v", err)
	}
	if got, want := stdout.String(), "Colin is running.\n"; got != want {
		t.Fatalf("startup output = %q, want %q", got, want)
	}
}

func TestRunHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(--help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "config") {
		t.Fatalf("help output = %q, want to mention config", got)
	}
	if !strings.Contains(got, "setup") {
		t.Fatalf("help output = %q, want to mention setup", got)
	}
	if !strings.Contains(got, "--workflow") {
		t.Fatalf("help output = %q, want to mention workflow flag", got)
	}
	if !strings.Contains(got, "uses the workflow file setting when unset") {
		t.Fatalf("help output = %q, want updated port flag help", got)
	}
	if strings.Contains(got, "default -1") {
		t.Fatalf("help output = %q, want to avoid internal port sentinel default", got)
	}
}

func TestRunRejectsExtraRootArgs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"one", "two"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 2 {
		t.Fatalf("run(extra root args) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "accepts at most 0 arg(s)") {
		t.Fatalf("stderr = %q, want positional arg error", got)
	}
	if !strings.Contains(got, "colin [flags]") {
		t.Fatalf("stderr = %q, want root usage", got)
	}
}

func TestRunRejectsSetupWithoutSubcommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 2 {
		t.Fatalf("run(setup) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "colin setup [command]") {
		t.Fatalf("stderr = %q, want setup help", got)
	}
}

func TestRunRejectsExtraSetupTailscaleArgs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "tailscale", "one"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 2 {
		t.Fatalf("run(setup tailscale extra args) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "accepts at most 0 arg(s)") {
		t.Fatalf("stderr = %q, want positional arg error", got)
	}
	if !strings.Contains(got, "tailscale [flags]") {
		t.Fatalf("stderr = %q, want setup tailscale usage", got)
	}
}

func TestRunUsesDefaultWorkflowFlag(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)
	if err := os.WriteFile(filepath.Join(tempDir, "WORKFLOW.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotWorkflow string

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			gotWorkflow = opts.workflowPath
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			t.Fatal("runConfig should not be called")
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
		runSetupLinearWebhook: func(cmd *cobra.Command, workflowPath string, webhookName string) int {
			t.Fatal("runSetupLinearWebhook should not be called")
			return 0
		},
	}

	if code := run(nil, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run() exit code = %d, want 0", code)
	}
	if gotWorkflow != "WORKFLOW.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "WORKFLOW.md")
	}
}

func TestRunPassesWorkflowFlagToRootCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotWorkflow string

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			gotWorkflow = opts.workflowPath
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			t.Fatal("runConfig should not be called")
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
		runSetupLinearWebhook: func(cmd *cobra.Command, workflowPath string, webhookName string) int {
			t.Fatal("runSetupLinearWebhook should not be called")
			return 0
		},
	}

	if code := run([]string{"--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(--workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
}

func TestRunPassesWorkflowFlagToSetupTailscale(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotWorkflow string

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			t.Fatal("runRoot should not be called")
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			t.Fatal("runConfig should not be called")
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			gotWorkflow = workflowPath
			return 0
		},
		runSetupLinearWebhook: func(cmd *cobra.Command, workflowPath string, webhookName string) int {
			t.Fatal("runSetupLinearWebhook should not be called")
			return 0
		},
	}

	if code := run([]string{"setup", "tailscale", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup tailscale --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
}

func TestSetupTailscaleHelpExplainsPurpose(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "tailscale", "--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(setup tailscale --help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	got := stdout.String()
	if !strings.Contains(got, "Tailscale") {
		t.Fatalf("help output = %q, want Tailscale context", got)
	}
	if !strings.Contains(got, "Linear and GitHub webhooks") {
		t.Fatalf("help output = %q, want webhook explanation", got)
	}
	if !strings.Contains(got, "Tailscale Funnel") {
		t.Fatalf("help output = %q, want Tailscale Funnel explanation", got)
	}
	if !strings.Contains(got, "tailscale funnel") {
		t.Fatalf("help output = %q, want funnel command explanation", got)
	}
}

func TestRunPassesWorkflowFlagToSetupGitHub(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotWorkflow string

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			t.Fatal("runRoot should not be called")
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			t.Fatal("runConfig should not be called")
			return 0
		},
		runSetupGitHub: func(cmd *cobra.Command, workflowPath string) int {
			gotWorkflow = workflowPath
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
		runSetupLinearWebhook: func(cmd *cobra.Command, workflowPath string, webhookName string) int {
			t.Fatal("runSetupLinearWebhook should not be called")
			return 0
		},
	}

	if code := run([]string{"setup", "github", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup github --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
}

func TestSetupGitHubHelpExplainsPurpose(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "github", "--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(setup github --help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	got := stdout.String()
	for _, want := range []string{
		"fine-grained personal access token",
		"GITHUB_TOKEN",
		"Pull requests",
		"Contents",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output = %q, want %q", got, want)
		}
	}
}

func TestRunPassesWorkflowFlagToSetupLinearWebhook(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotWorkflow string
	var gotName string

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			t.Fatal("runRoot should not be called")
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			t.Fatal("runConfig should not be called")
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
		runSetupLinearWebhook: func(cmd *cobra.Command, workflowPath string, webhookName string) int {
			gotWorkflow = workflowPath
			gotName = webhookName
			return 0
		},
	}

	if code := run([]string{"setup", "linear", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup linear --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
	if gotName != "colin" {
		t.Fatalf("webhook name = %q, want %q", gotName, "colin")
	}
}

func TestRunPassesWebhookNameToSetupLinearWebhook(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotName string

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			t.Fatal("runRoot should not be called")
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			t.Fatal("runConfig should not be called")
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
		runSetupLinearWebhook: func(cmd *cobra.Command, workflowPath string, webhookName string) int {
			gotName = webhookName
			return 0
		},
	}

	if code := run([]string{"setup", "linear", "--name", "colin-dev"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup linear --name) exit code = %d, want 0", code)
	}
	if gotName != "colin-dev" {
		t.Fatalf("webhook name = %q, want %q", gotName, "colin-dev")
	}
}

func TestSetupLinearWebhookHelpExplainsPurpose(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "linear", "--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(setup linear --help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	got := stdout.String()
	if !strings.Contains(got, "/webhooks/linear") {
		t.Fatalf("help output = %q, want webhook URL context", got)
	}
	if !strings.Contains(got, "LINEAR_WEBHOOK_SECRET") {
		t.Fatalf("help output = %q, want signing secret env var", got)
	}
	if !strings.Contains(got, "--name") {
		t.Fatalf("help output = %q, want name flag", got)
	}
	if !strings.Contains(got, "setup linear") {
		t.Fatalf("help output = %q, want command name", got)
	}
}

func TestRunRejectsRemovedSetupFunnelCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "funnel"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 2 {
		t.Fatalf("run(setup funnel) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command \"funnel\" for \"colin setup\"") {
		t.Fatalf("stderr = %q, want removed command error", got)
	}
}

func TestRenderSetupStatusColorizesCheckLabels(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)

	renderSetupStatus(cmd, domain.FunnelSetupStatus{
		Checks: []domain.SetupCheck{
			{Status: "ok", Label: "Tailscale is running"},
			{Status: "error", Label: "MagicDNS is enabled"},
		},
	})

	got := stdout.String()
	if !strings.Contains(got, setupStatusOKStyle.Render("[OK]")) {
		t.Fatalf("output = %q, want colored OK label", got)
	}
	if !strings.Contains(got, setupStatusErrorStyle.Render("[ERROR]")) {
		t.Fatalf("output = %q, want colored ERROR label", got)
	}
}

func TestRunConfigCommandWritesWorkflow(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
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

	if code := run([]string{"config"}, input, &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(config) exit code = %d, want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	data, err := os.ReadFile(filepath.Join(tempDir, "WORKFLOW.md"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	gotFile := string(data)
	if !strings.Contains(gotFile, `project_slug: "project-1"`) {
		t.Fatalf("workflow file = %q, want project slug", gotFile)
	}
	if !strings.Contains(gotFile, "api_key: $LINEAR_API_KEY") {
		t.Fatalf("workflow file = %q, want env token", gotFile)
	}
	if got := stdout.String(); !strings.Contains(got, "Wrote WORKFLOW.md") {
		t.Fatalf("stdout = %q, want written message", got)
	}
}

func TestRunWithoutWorkflowInvokesConfigFlow(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var configCalls int
	var rootCalls int

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			rootCalls++
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			configCalls++
			if !opts.autoStart {
				t.Fatal("runConfig autoStart = false, want true")
			}
			if err := os.WriteFile(filepath.Join(tempDir, "WORKFLOW.md"), []byte("seed\n"), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
		runSetupLinearWebhook: func(cmd *cobra.Command, workflowPath string, webhookName string) int {
			t.Fatal("runSetupLinearWebhook should not be called")
			return 0
		},
	}

	if code := run(nil, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run() exit code = %d, want 0, stderr=%q", code, stderr.String())
	}
	if configCalls != 1 {
		t.Fatalf("runConfig calls = %d, want 1", configCalls)
	}
	if rootCalls != 1 {
		t.Fatalf("runRoot calls = %d, want 1", rootCalls)
	}
	if got := stdout.String(); !strings.Contains(got, "WORKFLOW.md was not found. Starting first-run setup.") {
		t.Fatalf("stdout = %q, want first-run message", got)
	}
}

func TestRunWithExplicitMissingWorkflowDoesNotInvokeConfigFlow(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var rootCalls int

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			rootCalls++
			if opts.workflowPath != filepath.Join(tempDir, "custom.md") {
				t.Fatalf("workflow path = %q, want %q", opts.workflowPath, filepath.Join(tempDir, "custom.md"))
			}
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			t.Fatal("runConfig should not be called")
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
		runSetupLinearWebhook: func(cmd *cobra.Command, workflowPath string, webhookName string) int {
			t.Fatal("runSetupLinearWebhook should not be called")
			return 0
		},
	}

	customPath := filepath.Join(tempDir, "custom.md")
	if code := run([]string{"--workflow", customPath}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(--workflow missing) exit code = %d, want 0", code)
	}
	if rootCalls != 1 {
		t.Fatalf("runRoot calls = %d, want 1", rootCalls)
	}
	if got := stdout.String(); strings.Contains(got, "Starting first-run setup") {
		t.Fatalf("stdout = %q, want no first-run message", got)
	}
}

func TestConfigCommandDeclinesOverwrite(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)
	if err := os.WriteFile(filepath.Join(tempDir, "WORKFLOW.md"), []byte("original\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
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

	if code := run([]string{"config"}, input, &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(config overwrite decline) exit code = %d, want 0, stderr=%q", code, stderr.String())
	}

	data, err := os.ReadFile(filepath.Join(tempDir, "WORKFLOW.md"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(data); got != "original\n" {
		t.Fatalf("workflow file = %q, want original content", got)
	}
	if got := stdout.String(); !strings.Contains(got, "Setup canceled. No workflow file was written.") {
		t.Fatalf("stdout = %q, want canceled message", got)
	}
}
