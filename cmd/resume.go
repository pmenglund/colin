package cmd

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"

	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

var (
	loadResumeSession = service.LoadResumeSession
	execResumeCommand = runResumeCommand
)

func newResumeCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "resume <thread-id>",
		Short:         "Resume a Colin-managed Codex thread locally",
		Long:          "Resolve a Colin-managed Codex thread through Linear metadata, prepare the owning issue workspace locally, and launch an interactive `codex resume` session in that workspace.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runResume(cmd, resumeOptions{
				workflowPath: opts.workflowPath,
				threadID:     args[0],
			}))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin resume thread-123\n  colin --workflow /path/to/WORKFLOW.md resume thread-123"
	return cmd
}

func runResume(cmd *cobra.Command, opts resumeOptions) int {
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	session, err := loadResumeSession(ctx, opts.workflowPath, opts.threadID)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	cmd.Printf("Resuming %s in %s\n", session.Issue.Identifier, session.WorkspacePath)
	if err := execResumeCommand(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), buildResumeShellCommand(session.CLICommand, opts.threadID, session.WorkspacePath)); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		cmd.PrintErrln(err)
		return 1
	}
	return 0
}

func runResumeCommand(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, command string) error {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func buildResumeShellCommand(cliCommand string, threadID string, workspacePath string) string {
	cliCommand = strings.TrimSpace(cliCommand)
	if cliCommand == "" {
		cliCommand = "codex"
	}
	return cliCommand + " resume " + shellQuote(threadID) + " -C " + shellQuote(workspacePath)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
