package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newSetupCmd(stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "setup",
		Short:         "Run setup helpers",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &usageError{
					Command: cmd,
					Err:     fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath()),
				}
			}
			return &usageError{Command: cmd}
		},
	}
	configureCommand(cmd, stdout, stderr)
	cmd.AddCommand(newSetupTailscaleCmd(stdout, stderr, opts, deps))

	return cmd
}

func newSetupTailscaleCmd(stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "tailscale",
		Short: "Prepare Tailscale for incoming webhook exposure",
		Long: "Check whether this machine and workflow are ready to expose Colin through Tailscale.\n\n" +
			"This command is used before configuring Linear and GitHub webhooks. It verifies the local Tailscale setup, outlines that Colin uses Tailscale Funnel to expose the required public webhook URLs, and shows the exact `tailscale funnel` command Colin expects.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupTailscale(cmd, opts.workflowPath, jsonOutput))
		},
	}
	configureCommand(cmd, stdout, stderr)
	cmd.Example = "  colin setup tailscale\n  colin --workflow /path/to/WORKFLOW.md setup tailscale"
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print JSON output")

	return cmd
}
