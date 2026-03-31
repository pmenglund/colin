package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newSetupCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
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
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.AddCommand(newSetupGitHubCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupTailscaleCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupLinearWebhookCmd(stdin, stdout, stderr, opts, deps))

	return cmd
}

func newSetupGitHubCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Print the easiest GitHub token setup for this workflow",
		Long: "Inspect the watched repository from WORKFLOW.md or the current checkout and print the recommended GitHub token settings for Colin.\n\n" +
			"This command prefers a fine-grained personal access token scoped to the watched repository, with `Contents` and `Pull requests` set to `Read and write`, and prints a pre-filled GitHub token creation URL that you can open directly. Export the resulting token as `GITHUB_TOKEN` for the generated workflow.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupGitHub(cmd, opts.workflowPath))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup github\n  colin --workflow /path/to/WORKFLOW.md setup github"
	return cmd
}

func newSetupTailscaleCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "tailscale",
		Short: "Prepare Tailscale for incoming webhook exposure",
		Long: "Check whether this machine and workflow are ready to expose Colin through Tailscale.\n\n" +
			"This command is used before configuring Linear and GitHub webhooks. It verifies the local Tailscale setup, outlines that Colin uses Tailscale Funnel to expose only the `/webhooks` paths publicly, shows the exact `tailscale funnel` command Colin expects, and keeps the dashboard and setup UI private unless you publish them separately.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupTailscale(cmd, opts.workflowPath, jsonOutput))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup tailscale\n  colin --workflow /path/to/WORKFLOW.md setup tailscale"
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print JSON output")

	return cmd
}

func newSetupLinearWebhookCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	var webhookName string

	cmd := &cobra.Command{
		Use:   "linear",
		Short: "Create or repair the Linear webhook for this workflow",
		Long: "Create or repair the watched project's Linear webhook so it points at Colin's public `/webhooks/linear` endpoint.\n\n" +
			"This command uses `server.webhook_public_url` when configured, or the current Tailscale Funnel public base URL when available. It manages one team-scoped Linear webhook for the watched project, sets the webhook label with `--name`, and reminds you to store the Linear signing secret in `tracker.webhook_signing_secret` via `$LINEAR_WEBHOOK_SECRET`.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupLinearWebhook(cmd, opts.workflowPath, webhookName))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup linear\n  colin --workflow /path/to/WORKFLOW.md setup linear"
	cmd.Flags().StringVar(&webhookName, "name", "colin", "Linear webhook label to create or repair")
	return cmd
}
