package cmd

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/clioutput"
	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

func runSetupGitHubWebhook(cmd *cobra.Command, workflowPath string) int {
	workingDir, err := os.Getwd()
	if err != nil {
		cmd.PrintErrln(err)
		return 1
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	result, err := service.LoadGitHubWebhookSetup(ctx, workflowPath, workingDir)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	renderer := newCommandRenderer(cmd)
	renderer.Section("Overview")
	renderer.Item(result.BackendDisplayName+" repository", result.RepositoryOwner+"/"+result.RepositoryName)
	renderer.Item("Repository source", result.RepositorySource)
	renderer.Item("Repository URL", result.RepositoryURL)
	renderer.Item("Webhook URL", result.WebhookURL)
	renderer.Item("Subscribe GitHub to these events", strings.Join(result.Events, ", "))

	renderer.Section("Checks")
	if result.SigningSecretConfigured {
		renderer.Status(clioutput.StatusOK, "Signing secret", "configured")
	} else {
		renderer.Status(clioutput.StatusAction, "Signing secret", "set `repo.webhook_signing_secret: $"+result.SigningSecretEnvVar+"` in `WORKFLOW.md`, then use the same value as the GitHub webhook secret")
	}

	renderer.Section("Notes")
	renderer.Status(clioutput.StatusInfo, "", "Colin uses relevant GitHub webhook deliveries to queue an immediate reconciliation pass, while polling remains active as a fallback")
	return 0
}
