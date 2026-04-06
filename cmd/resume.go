package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/anmitsu/go-shlex"
	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

var (
	loadResumeSession = service.LoadResumeSession
	execResumeCommand = runResumeCommand
)

func newResumeCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "resume <thread-id-or-issue-id>",
		Short:         "Resume a Colin-managed Codex thread locally",
		Long:          "Resolve a Colin-managed Codex thread or Linear issue through Colin's Linear metadata, prepare the owning issue workspace locally, and launch an interactive `codex resume` session in that workspace.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runResume(cmd, resumeOptions{
				workflowPath: opts.resolvedWorkflowPath(),
				target:       args[0],
			}))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin resume thread-123\n  colin resume COLIN-123\n  colin --workflow /path/to/WORKFLOW.md resume COLIN-123"
	return cmd
}

func runResume(cmd *cobra.Command, opts resumeOptions) int {
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	session, err := loadResumeSession(ctx, opts.workflowPath, opts.target)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	cmd.Printf("Resuming %s in %s\n", session.Issue.Identifier, session.WorkspacePath)
	args, err := buildResumeArgs(session.CLICommand, session.ThreadID, session.WorkspacePath)
	if err != nil {
		cmd.PrintErrln(err)
		return 1
	}
	if err := execResumeCommand(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		cmd.PrintErrln(err)
		return 1
	}
	return 0
}

func runResumeCommand(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("resume command is empty")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func buildResumeArgs(cliCommand string, threadID string, workspacePath string) ([]string, error) {
	cliCommand = strings.TrimSpace(cliCommand)
	if cliCommand == "" {
		cliCommand = "codex"
	}
	args, err := shlex.Split(cliCommand, true)
	if err != nil {
		return nil, fmt.Errorf("parse codex cli command: %w", err)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("codex cli command is empty")
	}
	args = append(args, "resume", threadID, "-C", workspacePath)
	return args, nil
}
