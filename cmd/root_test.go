package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/bootstrap"
	"github.com/pmenglund/colin/internal/clioutput"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/workflow"
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
	if !strings.Contains(got, "resume") {
		t.Fatalf("help output = %q, want to mention resume", got)
	}
	if !strings.Contains(got, "setup") {
		t.Fatalf("help output = %q, want to mention setup", got)
	}
	if !strings.Contains(got, "--workflow") {
		t.Fatalf("help output = %q, want to mention workflow flag", got)
	}
	if !strings.Contains(got, "COLIN_WORKFLOW") {
		t.Fatalf("help output = %q, want to mention COLIN_WORKFLOW", got)
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

func TestRunRejectsResumeWithoutThreadID(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"resume"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 2 {
		t.Fatalf("run(resume) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "accepts exactly 1 arg(s)") {
		t.Fatalf("stderr = %q, want exact arg error", got)
	}
}

func TestRunDispatchesResumeCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var got resumeOptions

	deps := commandDeps{
		runResume: func(cmd *cobra.Command, opts resumeOptions) int {
			got = opts
			return 0
		},
	}

	if code := run([]string{"--workflow", "custom.md", "resume", "thread-123"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(resume) exit code = %d, want 0, stderr=%q", code, stderr.String())
	}
	if got.workflowPath != "custom.md" {
		t.Fatalf("workflowPath = %q, want %q", got.workflowPath, "custom.md")
	}
	if got.threadID != "thread-123" {
		t.Fatalf("threadID = %q, want %q", got.threadID, "thread-123")
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
		isInteractive: func(*cobra.Command) bool {
			return true
		},
	}

	if code := run(nil, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run() exit code = %d, want 0", code)
	}
	if gotWorkflow != "WORKFLOW.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "WORKFLOW.md")
	}
}

func TestRunUsesWorkflowEnvVarWhenFlagUnset(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	workflowPath := filepath.Join(tempDir, "from-env.md")
	t.Setenv(workflow.WorkflowPathEnvVar, workflowPath)
	if err := os.WriteFile(workflowPath, []byte("seed\n"), 0o644); err != nil {
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
		isInteractive: func(*cobra.Command) bool {
			return true
		},
	}

	if code := run(nil, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run() exit code = %d, want 0", code)
	}
	if gotWorkflow != workflowPath {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, workflowPath)
	}
}

func TestRunPrefersWorkflowFlagOverEnvVar(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(workflow.WorkflowPathEnvVar, filepath.Join(tempDir, "from-env.md"))

	customPath := filepath.Join(tempDir, "custom.md")
	if err := os.WriteFile(customPath, []byte("seed\n"), 0o644); err != nil {
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

	if code := run([]string{"--workflow", customPath}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(--workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != customPath {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, customPath)
	}
}

func TestRunPassesWorkflowFlagToRootCommand(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	customPath := filepath.Join(tempDir, "custom.md")
	if err := os.WriteFile(customPath, []byte("seed\n"), 0o644); err != nil {
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

	if code := run([]string{"--workflow", customPath}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(--workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != customPath {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, customPath)
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

func TestRunPassesWorkflowFlagToSetupRepo(t *testing.T) {
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
		runSetupRepo: func(cmd *cobra.Command, workflowPath string) int {
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

	if code := run([]string{"setup", "repo", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup repo --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
}

func TestRunPassesWorkflowFlagToSetupSlack(t *testing.T) {
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
		runSetupSlack: func(cmd *cobra.Command, workflowPath string) int {
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

	if code := run([]string{"setup", "slack", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup slack --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
}

func TestSetupGitHubHelpExplainsPurpose(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "github", "token", "--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(setup github token --help) exit code = %d, want 0", code)
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

func TestSetupSlackHelpExplainsPurpose(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "slack", "--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(setup slack --help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	got := stdout.String()
	for _, want := range []string{
		"slack.bot_token",
		"slack.app_token",
		"slack.channel_id",
		"Socket Mode",
		"interactivity",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output = %q, want %q", got, want)
		}
	}
}

func TestSetupGitHubHelpListsSubcommands(t *testing.T) {
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
	for _, want := range []string{"token", "webhook", "setup github webhook"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output = %q, want %q", got, want)
		}
	}
}

func TestRunRejectsRemovedSetupGitHubWebhookAlias(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "github-webhook"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 2 {
		t.Fatalf("run(setup github-webhook) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command \"github-webhook\" for \"colin setup\"") {
		t.Fatalf("stderr = %q, want removed command error", got)
	}
}

func TestRunPassesWorkflowFlagToSetupGitHubWebhookSubcommand(t *testing.T) {
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
		runSetupGitHubWebhook: func(cmd *cobra.Command, workflowPath string) int {
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

	if code := run([]string{"setup", "github", "webhook", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup github webhook --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
}

func TestRunPassesWorkflowFlagToSetupGitHubTokenSubcommand(t *testing.T) {
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

	if code := run([]string{"setup", "github", "token", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup github token --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
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

func TestRunPassesWorkflowFlagToSetupLinearWebhookSubcommand(t *testing.T) {
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

	if code := run([]string{"setup", "linear", "webhook", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup linear webhook --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
	if gotName != "colin" {
		t.Fatalf("webhook name = %q, want %q", gotName, "colin")
	}
}

func TestRunRejectsRemovedSetupLinearAppAlias(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "linear-app"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 2 {
		t.Fatalf("run(setup linear-app) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command \"linear-app\" for \"colin setup\"") {
		t.Fatalf("stderr = %q, want removed command error", got)
	}
}

func TestRunPassesWorkflowFlagToSetupLinearAppSubcommand(t *testing.T) {
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
		runSetupLinearApp: func(cmd *cobra.Command, workflowPath string) int {
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

	if code := run([]string{"setup", "linear", "app", "--workflow", "/tmp/custom.md"}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(setup linear app --workflow) exit code = %d, want 0", code)
	}
	if gotWorkflow != "/tmp/custom.md" {
		t.Fatalf("workflow path = %q, want %q", gotWorkflow, "/tmp/custom.md")
	}
}

func TestSetupGitHubWebhookHelpExplainsPurpose(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "github", "webhook", "--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(setup github webhook --help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	got := stdout.String()
	if !strings.Contains(got, "/webhooks/github") {
		t.Fatalf("help output = %q, want github webhook path", got)
	}
	if !strings.Contains(got, "GITHUB_WEBHOOK_SECRET") {
		t.Fatalf("help output = %q, want secret env var", got)
	}
	if !strings.Contains(got, "pull_request_review") {
		t.Fatalf("help output = %q, want event subscription", got)
	}
	if !strings.Contains(got, "setup github webhook") {
		t.Fatalf("help output = %q, want command name", got)
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

	if code := run([]string{"setup", "linear", "webhook", "--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(setup linear webhook --help) exit code = %d, want 0", code)
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
	if !strings.Contains(got, "setup linear webhook") {
		t.Fatalf("help output = %q, want command name", got)
	}
}

func TestSetupLinearHelpListsSubcommands(t *testing.T) {
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
	for _, want := range []string{"webhook", "app", "setup linear app"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output = %q, want %q", got, want)
		}
	}
}

func TestSetupLinearAppHelpExplainsPurpose(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "linear", "app", "--help"}, emptyInput(), &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(setup linear app --help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	got := stdout.String()
	if !strings.Contains(got, "/webhooks/linear") {
		t.Fatalf("help output = %q, want webhook URL context", got)
	}
	if !strings.Contains(got, "AgentSessionEvent") {
		t.Fatalf("help output = %q, want agent webhook category", got)
	}
	if !strings.Contains(got, "should not disable Colin's existing issue-webhook or polling wake-up path") {
		t.Fatalf("help output = %q, want webhook guidance", got)
	}
	if !strings.Contains(got, "setup linear app") {
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
	renderSetupStatusWithRenderer(clioutput.New(&stdout, true), domain.FunnelSetupStatus{
		Checks: []domain.SetupCheck{
			{Status: "ok", Label: "Tailscale is running"},
			{Status: "error", Label: "MagicDNS is enabled"},
			{Status: "disabled", Label: "Webhook listener is disabled"},
		},
	})

	got := stdout.String()
	if !strings.Contains(got, "[OK] Tailscale is running") {
		t.Fatalf("output = %q, want colored OK label", got)
	}
	if !strings.Contains(got, "[ERROR] MagicDNS is enabled") {
		t.Fatalf("output = %q, want colored ERROR label", got)
	}
	if !strings.Contains(got, "[INFO] Webhook listener is disabled") {
		t.Fatalf("output = %q, want INFO label for disabled checks", got)
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
	if got := stdout.String(); !strings.Contains(got, "Workflow file: WORKFLOW.md") {
		t.Fatalf("stdout = %q, want workflow summary", got)
	}
}

func TestRunConfigCommandUsesWorkflowEnvVar(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	customPath := filepath.Join(tempDir, "from-env.md")
	t.Setenv(workflow.WorkflowPathEnvVar, customPath)

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

	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("Stat(%q) error = %v", customPath, err)
	}
	if got := stdout.String(); !strings.Contains(got, "Workflow file: "+customPath) {
		t.Fatalf("stdout = %q, want workflow summary for env-selected path", got)
	}
}

func TestRunWithMissingWorkflowFromEnvFailsClearlyWhenNonInteractive(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)
	customPath := filepath.Join(tempDir, "from-env.md")
	t.Setenv(workflow.WorkflowPathEnvVar, customPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var rootCalls int
	var configCalls int

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			rootCalls++
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			configCalls++
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
		isInteractive: func(*cobra.Command) bool {
			return false
		},
	}

	if code := run(nil, emptyInput(), &stdout, &stderr, deps); code != 1 {
		t.Fatalf("run(missing env workflow non-interactive) exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if configCalls != 0 {
		t.Fatalf("runConfig calls = %d, want 0", configCalls)
	}
	if rootCalls != 0 {
		t.Fatalf("runRoot calls = %d, want 0", rootCalls)
	}
	got := stderr.String()
	if !strings.Contains(got, "workflow file not found: "+customPath) {
		t.Fatalf("stderr = %q, want missing workflow message", got)
	}
	if !strings.Contains(got, "colin --workflow "+customPath+" config") {
		t.Fatalf("stderr = %q, want env-selected config hint", got)
	}
}

func TestRunConfigUsesTUIWhenInteractive(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	oldInteractive := configInteractive
	oldPrompt := runBootstrapPrompt
	oldTUI := runBootstrapTUI
	t.Cleanup(func() {
		configInteractive = oldInteractive
		runBootstrapPrompt = oldPrompt
		runBootstrapTUI = oldTUI
	})

	var promptCalls int
	var tuiCalls int
	configInteractive = func(io.Reader, io.Writer) bool { return true }
	runBootstrapPrompt = func(io.Reader, io.Writer, bootstrap.Options) (bootstrap.Result, error) {
		promptCalls++
		return bootstrap.Result{}, nil
	}
	runBootstrapTUI = func(io.Reader, io.Writer, bootstrap.Options) (bootstrap.Result, error) {
		tuiCalls++
		return bootstrap.Result{}, nil
	}

	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	if code := runConfig(cmd, configOptions{workflowPath: "WORKFLOW.md"}); code != 0 {
		t.Fatalf("runConfig() exit code = %d, want 0", code)
	}
	if tuiCalls != 1 {
		t.Fatalf("RunTUI calls = %d, want 1", tuiCalls)
	}
	if promptCalls != 0 {
		t.Fatalf("Run prompt calls = %d, want 0", promptCalls)
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
		isInteractive: func(*cobra.Command) bool {
			return true
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

func TestRunWithExplicitMissingWorkflowInvokesConfigFlowWhenInteractive(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var configCalls int
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
			configCalls++
			if !opts.autoStart {
				t.Fatal("runConfig autoStart = false, want true")
			}
			if opts.workflowPath != filepath.Join(tempDir, "custom.md") {
				t.Fatalf("workflow path = %q, want %q", opts.workflowPath, filepath.Join(tempDir, "custom.md"))
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
		isInteractive: func(*cobra.Command) bool {
			return true
		},
	}

	customPath := filepath.Join(tempDir, "custom.md")
	if code := run([]string{"--workflow", customPath}, emptyInput(), &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(--workflow missing) exit code = %d, want 0", code)
	}
	if configCalls != 1 {
		t.Fatalf("runConfig calls = %d, want 1", configCalls)
	}
	if rootCalls != 1 {
		t.Fatalf("runRoot calls = %d, want 1", rootCalls)
	}
	if got := stdout.String(); !strings.Contains(got, customPath+" was not found. Starting first-run setup.") {
		t.Fatalf("stdout = %q, want first-run message", got)
	}
}

func TestRunWithMissingWorkflowFailsClearlyWhenNonInteractive(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var rootCalls int
	var configCalls int

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			rootCalls++
			return 0
		},
		runConfig: func(cmd *cobra.Command, opts configOptions) int {
			configCalls++
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
		isInteractive: func(*cobra.Command) bool {
			return false
		},
	}

	customPath := filepath.Join(tempDir, "custom.md")
	if code := run([]string{"--workflow", customPath}, emptyInput(), &stdout, &stderr, deps); code != 1 {
		t.Fatalf("run(--workflow missing non-interactive) exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if configCalls != 0 {
		t.Fatalf("runConfig calls = %d, want 0", configCalls)
	}
	if rootCalls != 0 {
		t.Fatalf("runRoot calls = %d, want 0", rootCalls)
	}
	got := stderr.String()
	if !strings.Contains(got, "workflow file not found: "+customPath) {
		t.Fatalf("stderr = %q, want missing workflow message", got)
	}
	if !strings.Contains(got, "colin --workflow "+customPath+" config") {
		t.Fatalf("stderr = %q, want config guidance", got)
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
