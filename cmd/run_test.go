package cmd

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/service"
)

type fakeRuntimeService struct {
	run func(context.Context) error
}

func (f fakeRuntimeService) Run(ctx context.Context) error {
	if f.run != nil {
		return f.run(ctx)
	}
	return nil
}

func (fakeRuntimeService) DashboardEnabled() bool { return false }
func (fakeRuntimeService) DashboardURL() string   { return "" }
func (fakeRuntimeService) FunnelSetupURL() string { return "" }

func (fakeRuntimeService) Snapshot(context.Context) (domain.Snapshot, error) {
	return domain.Snapshot{}, nil
}

func (fakeRuntimeService) BufferedLogs(context.Context, *slog.Level) (domain.BufferedLogSnapshot, error) {
	return domain.BufferedLogSnapshot{}, nil
}

func (fakeRuntimeService) FunnelSetupStatus(context.Context) domain.FunnelSetupStatus {
	return domain.FunnelSetupStatus{}
}

func TestRunRootUsesRuntimeTUIWhenInteractiveAndNotVerbose(t *testing.T) {
	restore := patchRunRootSeams(t)
	defer restore()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(bytes.NewBuffer(nil))
	cmd.SetOut(io.Discard)

	stopped := false
	newRuntimeService = func(logger *slog.Logger, workflowPath string, options ...service.Option) (runtimeService, error) {
		return fakeRuntimeService{
			run: func(ctx context.Context) error {
				<-ctx.Done()
				stopped = true
				return nil
			},
		}, nil
	}
	runtimeIsInteractiveTerminal = func(io.Reader, io.Writer) bool { return true }

	var tuiCalls int
	runRuntimeTUI = func(ctx context.Context, in io.Reader, out io.Writer, source runtimeService, serviceErrCh <-chan error, stop func()) error {
		tuiCalls++
		stop()
		return <-serviceErrCh
	}

	if code := runRoot(cmd, rootOptions{workflowPath: "WORKFLOW.md"}); code != 0 {
		t.Fatalf("runRoot() exit code = %d, want 0", code)
	}
	if tuiCalls != 1 {
		t.Fatalf("runtime TUI calls = %d, want 1", tuiCalls)
	}
	if !stopped {
		t.Fatal("service was not stopped by the TUI path")
	}
}

func TestRunRootSkipsRuntimeTUIWhenVerbose(t *testing.T) {
	restore := patchRunRootSeams(t)
	defer restore()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(bytes.NewBuffer(nil))
	cmd.SetOut(io.Discard)

	newRuntimeService = func(logger *slog.Logger, workflowPath string, options ...service.Option) (runtimeService, error) {
		return fakeRuntimeService{run: func(context.Context) error { return nil }}, nil
	}
	runtimeIsInteractiveTerminal = func(io.Reader, io.Writer) bool { return true }

	runRuntimeTUI = func(ctx context.Context, in io.Reader, out io.Writer, source runtimeService, serviceErrCh <-chan error, stop func()) error {
		t.Fatal("runRuntimeTUI should not be called for verbose mode")
		return nil
	}

	if code := runRoot(cmd, rootOptions{workflowPath: "WORKFLOW.md", verbose: true}); code != 0 {
		t.Fatalf("runRoot() exit code = %d, want 0", code)
	}
}

func TestRunRootSkipsRuntimeTUIWhenNonInteractive(t *testing.T) {
	restore := patchRunRootSeams(t)
	defer restore()

	var stdout bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(bytes.NewBuffer(nil))
	cmd.SetOut(&stdout)

	newRuntimeService = func(logger *slog.Logger, workflowPath string, options ...service.Option) (runtimeService, error) {
		return fakeRuntimeService{run: func(context.Context) error { return nil }}, nil
	}
	runtimeIsInteractiveTerminal = func(io.Reader, io.Writer) bool { return false }

	runRuntimeTUI = func(ctx context.Context, in io.Reader, out io.Writer, source runtimeService, serviceErrCh <-chan error, stop func()) error {
		t.Fatal("runRuntimeTUI should not be called for non-interactive mode")
		return nil
	}

	if code := runRoot(cmd, rootOptions{workflowPath: "WORKFLOW.md"}); code != 0 {
		t.Fatalf("runRoot() exit code = %d, want 0", code)
	}
	if got := stdout.String(); got != "Colin is running.\n" {
		t.Fatalf("stdout = %q, want %q", got, "Colin is running.\n")
	}
}

func patchRunRootSeams(t *testing.T) func() {
	t.Helper()

	origNewRuntimeService := newRuntimeService
	origRunRuntimeTUI := runRuntimeTUI
	origInteractive := runtimeIsInteractiveTerminal
	return func() {
		newRuntimeService = origNewRuntimeService
		runRuntimeTUI = origRunRuntimeTUI
		runtimeIsInteractiveTerminal = origInteractive
	}
}
