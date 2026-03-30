package cmd

import (
	"bytes"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/domain"
)

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

	if code := run([]string{"--help"}, &stdout, &stderr, defaultCommandDeps()); code != 0 {
		t.Fatalf("run(--help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	got := stdout.String()
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

	if code := run([]string{"one", "two"}, &stdout, &stderr, defaultCommandDeps()); code != 2 {
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

	if code := run([]string{"setup"}, &stdout, &stderr, defaultCommandDeps()); code != 2 {
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

	if code := run([]string{"setup", "tailscale", "one"}, &stdout, &stderr, defaultCommandDeps()); code != 2 {
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
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotWorkflow string

	deps := commandDeps{
		runRoot: func(cmd *cobra.Command, opts rootOptions) int {
			gotWorkflow = opts.workflowPath
			return 0
		},
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
	}

	if code := run(nil, &stdout, &stderr, deps); code != 0 {
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
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			t.Fatal("runSetupTailscale should not be called")
			return 0
		},
	}

	if code := run([]string{"--workflow", "/tmp/custom.md"}, &stdout, &stderr, deps); code != 0 {
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
		runSetupTailscale: func(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
			gotWorkflow = workflowPath
			return 0
		},
	}

	if code := run([]string{"setup", "tailscale", "--workflow", "/tmp/custom.md"}, &stdout, &stderr, deps); code != 0 {
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

	if code := run([]string{"setup", "tailscale", "--help"}, &stdout, &stderr, defaultCommandDeps()); code != 0 {
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

func TestRunRejectsRemovedSetupFunnelCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "funnel"}, &stdout, &stderr, defaultCommandDeps()); code != 2 {
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
