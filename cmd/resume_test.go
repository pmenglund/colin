package cmd

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

func TestRunResumeLaunchesResolvedThread(t *testing.T) {
	restore := patchResumeSeams(t)
	defer restore()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	loadResumeSession = func(ctx context.Context, workflowPath string, threadID string) (service.ResumeSession, error) {
		return service.ResumeSession{
			Issue: domain.Issue{
				Identifier: "COLIN-123",
			},
			WorkspacePath: "/tmp/work tree",
			CLICommand:    "codex --profile local",
		}, nil
	}

	var gotCommand string
	execResumeCommand = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, command string) error {
		gotCommand = command
		return nil
	}

	if code := runResume(cmd, resumeOptions{workflowPath: "WORKFLOW.md", threadID: "thread'123"}); code != 0 {
		t.Fatalf("runResume() exit code = %d, want 0", code)
	}
	if gotCommand != "codex --profile local resume 'thread'\"'\"'123' -C '/tmp/work tree'" {
		t.Fatalf("command = %q", gotCommand)
	}
	if got := stdout.String(); got != "Resuming COLIN-123 in /tmp/work tree\n" {
		t.Fatalf("stdout = %q, want summary line", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunResumePropagatesChildExitCode(t *testing.T) {
	restore := patchResumeSeams(t)
	defer restore()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	loadResumeSession = func(ctx context.Context, workflowPath string, threadID string) (service.ResumeSession, error) {
		return service.ResumeSession{
			Issue:         domain.Issue{Identifier: "COLIN-123"},
			WorkspacePath: "/tmp/workspace",
			CLICommand:    "codex",
		}, nil
	}
	execResumeCommand = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, command string) error {
		return exec.Command("bash", "-lc", "exit 7").Run()
	}

	if code := runResume(cmd, resumeOptions{workflowPath: "WORKFLOW.md", threadID: "thread-123"}); code != 7 {
		t.Fatalf("runResume() exit code = %d, want 7", code)
	}
}

func TestRunResumePrintsLookupError(t *testing.T) {
	restore := patchResumeSeams(t)
	defer restore()

	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)

	loadResumeSession = func(ctx context.Context, workflowPath string, threadID string) (service.ResumeSession, error) {
		return service.ResumeSession{}, &service.ResumeThreadNotFoundError{ThreadID: threadID}
	}

	if code := runResume(cmd, resumeOptions{workflowPath: "WORKFLOW.md", threadID: "thread-123"}); code != 1 {
		t.Fatalf("runResume() exit code = %d, want 1", code)
	}
	if got := stderr.String(); !strings.Contains(got, "codex thread \"thread-123\" is not linked") {
		t.Fatalf("stderr = %q, want lookup guidance", got)
	}
}

func patchResumeSeams(t *testing.T) func() {
	t.Helper()

	origLoad := loadResumeSession
	origExec := execResumeCommand
	return func() {
		loadResumeSession = origLoad
		execResumeCommand = origExec
	}
}
