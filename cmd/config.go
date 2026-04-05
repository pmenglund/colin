package cmd

import (
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/bootstrap"
)

var (
	runBootstrapPrompt = bootstrap.Run
	runBootstrapTUI    = bootstrap.RunTUI
	configInteractive  = isInteractiveTerminal
)

func newConfigCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "config",
		Short:         "Create or refresh the Colin workflow configuration",
		Long:          "Create a WORKFLOW.md file by walking through the minimum Colin setup, including optional webhook follow-up guidance.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runConfig(cmd, configOptions{workflowPath: opts.resolvedWorkflowPath()}))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin config\n  colin --workflow /path/to/WORKFLOW.md config"
	return cmd
}

func runConfig(cmd *cobra.Command, opts configOptions) int {
	workingDir, err := os.Getwd()
	if err != nil {
		cmd.PrintErrln(err)
		return 1
	}

	runner := runBootstrapPrompt
	if configInteractive(cmd.InOrStdin(), cmd.OutOrStdout()) {
		runner = runBootstrapTUI
	}

	_, err = runner(cmd.InOrStdin(), cmd.OutOrStdout(), bootstrap.Options{
		WorkflowPath: opts.workflowPath,
		WorkingDir:   workingDir,
		AutoStart:    opts.autoStart,
	})
	if err == nil {
		return 0
	}
	if errors.Is(err, bootstrap.ErrAborted) {
		if opts.autoStart {
			cmd.PrintErrln("Setup canceled. Colin did not start.")
			return 1
		}
		cmd.Println("Setup canceled. No workflow file was written.")
		return 0
	}
	cmd.PrintErrln(err)
	return 1
}
