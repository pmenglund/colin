package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newSetupCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "setup",
		Short:         "Prepare or inspect external setup for this workflow",
		Long:          "Inspect or prepare the external environment Colin uses for a workflow, such as repository credentials, Tailscale ingress, and webhook settings.\n\nUse `colin config` to create or refresh WORKFLOW.md. Use `colin setup ...` after the workflow exists to prepare the supporting integrations it references.",
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
	cmd.AddCommand(newSetupRepoCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupSlackCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupGitHubCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupTailscaleCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupLinearCmd(stdin, stdout, stderr, opts, deps))

	return cmd
}

func newSetupRepoCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Print the easiest repository backend token setup for this workflow",
		Long: "Inspect the watched repository from WORKFLOW.md or the current checkout and print the recommended token settings for the configured repository backend.\n\n" +
			"This command is backend-aware. Today Colin only implements GitHub, but this generic entrypoint is the stable setup surface for future backends.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupRepo(cmd, opts.workflowPath))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup repo\n  colin --workflow /path/to/WORKFLOW.md setup repo"
	return cmd
}

func newSetupGitHubCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "GitHub setup helpers",
		Long: "Inspect GitHub-specific setup for this workflow.\n\n" +
			"With no subcommand, this prints the recommended GitHub token settings for the watched repository. Use `colin setup github webhook` to print the webhook settings for the same repository.\n\n" +
			"Use `colin setup repo` when you want the backend-agnostic repository-token guidance instead.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupGitHub(cmd, opts.workflowPath))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup github\n  colin setup github token\n  colin setup github webhook\n  colin --workflow /path/to/WORKFLOW.md setup github"
	cmd.AddCommand(newSetupGitHubTokenCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupGitHubWebhookCmd(stdin, stdout, stderr, opts, deps))
	return cmd
}

func newSetupSlackCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "slack",
		Short: "Print the Slack setup Colin expects for this workflow",
		Long: "Inspect the workflow's Slack section and print the Slack app, token, and channel setup Colin expects.\n\n" +
			"This command helps you prepare Slack summaries and Socket Mode for Colin. It checks whether `slack.bot_token`, `slack.app_token`, and `slack.channel_id` are declared and resolved, then reminds you to enable Socket Mode and interactivity for the Slack app and invite the app to the target channel.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupSlack(cmd, opts.workflowPath))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup slack\n  colin --workflow /path/to/WORKFLOW.md setup slack"
	return cmd
}

func newSetupGitHubTokenCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the GitHub token setup for this workflow",
		Long: "Inspect the watched repository from WORKFLOW.md or the current checkout and print the recommended GitHub token settings for Colin.\n\n" +
			"This command prefers a fine-grained personal access token scoped to the watched repository, with `Contents` and `Pull requests` set to `Read and write`, and expects you to export it as `GITHUB_TOKEN`. It is GitHub-specific; prefer `colin setup repo` when you want the backend-aware repository token path.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupGitHub(cmd, opts.workflowPath))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup github token\n  colin --workflow /path/to/WORKFLOW.md setup github token"
	return cmd
}

func newSetupGitHubWebhookCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Print the GitHub webhook settings for this workflow",
		Long: "Inspect the watched GitHub repository and current webhook public URL, then print the settings needed to configure GitHub to call Colin at `/webhooks/github`.\n\n" +
			"This command uses `server.webhook_public_url` when configured, or the current Tailscale Funnel public base URL when available. It also reminds you to store the shared secret in `repo.webhook_signing_secret` via `$GITHUB_WEBHOOK_SECRET` and prints the GitHub event subscriptions that wake Colin's orchestrator loop, including `pull_request`, `pull_request_review`, `pull_request_review_comment`, `pull_request_review_thread`, and `reaction`.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupGitHubWebhook(cmd, opts.workflowPath))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup github webhook\n  colin --workflow /path/to/WORKFLOW.md setup github webhook"
	return cmd
}

func newSetupTailscaleCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "tailscale",
		Short: "Prepare Tailscale for incoming webhook exposure",
		Long: "Check whether this machine and workflow are ready to expose Colin through Tailscale.\n\n" +
			"This command is used before configuring Linear and GitHub webhooks. It verifies the local Tailscale setup, outlines that Colin uses Tailscale Serve for the web UI and Tailscale Funnel only for the `/webhooks` paths, shows the exact `tailscale serve` and `tailscale funnel` commands Colin expects, and keeps the dashboard and setup UI private unless you publish them separately.",
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

func newSetupLinearCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	var webhookName string

	cmd := &cobra.Command{
		Use:   "linear",
		Short: "Linear setup helpers",
		Long: "Inspect or provision Linear setup for this workflow.\n\n" +
			"With no subcommand, this creates or repairs the watched project's team webhook. Use `colin setup linear app` to print the self-hosted Linear app sketch for the same workflow.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupLinearWebhook(cmd, opts.workflowPath, webhookName))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup linear\n  colin setup linear webhook\n  colin setup linear app\n  colin --workflow /path/to/WORKFLOW.md setup linear"
	cmd.Flags().StringVar(&webhookName, "name", "colin", "Linear webhook label to create or repair")
	cmd.AddCommand(newSetupLinearWebhookCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupLinearAppCmd(stdin, stdout, stderr, opts, deps))
	return cmd
}

func newSetupLinearWebhookCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	var webhookName string

	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Create or repair the Linear webhook for this workflow",
		Long: "Create or repair the watched project's Linear webhook so it points at Colin's public `/webhooks/linear` endpoint.\n\n" +
			"This command uses `server.webhook_public_url` when configured, or the current Tailscale Funnel public base URL when available. It manages one team-scoped Linear webhook for the watched project, sets the webhook label with `--name`, and reminds you to store the Linear signing secret in `tracker.webhook_signing_secret` via `$LINEAR_WEBHOOK_SECRET`.\n\n" +
			"For the self-hosted Linear app sketch, use `colin setup linear app` instead. App-owned webhooks should be configured from the Linear app setup, not replaced through this legacy team-webhook helper.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupLinearWebhook(cmd, opts.workflowPath, webhookName))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup linear webhook\n  colin --workflow /path/to/WORKFLOW.md setup linear webhook"
	cmd.Flags().StringVar(&webhookName, "name", "colin", "Linear webhook label to create or repair")
	return cmd
}

func newSetupLinearAppCmd(stdin io.Reader, stdout, stderr io.Writer, opts *rootOptions, deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Print the self-hosted Linear app sketch for this workflow",
		Long: "Print the expected Linear app shape for Colin when you want an assignable app user that can both be delegated work and act on its own.\n\n" +
			"This command resolves the public Colin webhook URL, points the Linear app at `/webhooks/linear`, lists the required `AgentSessionEvent` webhook category, and explains that app mode should not disable Colin's existing issue-webhook or polling wake-up path.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(deps.runSetupLinearApp(cmd, opts.workflowPath))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.Example = "  colin setup linear app\n  colin --workflow /path/to/WORKFLOW.md setup linear app"
	return cmd
}
