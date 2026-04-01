package cmd

import (
	"context"
	"os"
	"strings"
	"time"

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

	cmd.Printf("%s repository: %s/%s\n", result.BackendDisplayName, result.RepositoryOwner, result.RepositoryName)
	cmd.Printf("Repository source: %s\n", result.RepositorySource)
	cmd.Printf("Repository URL: %s\n", result.RepositoryURL)
	cmd.Printf("Webhook URL: %s\n", result.WebhookURL)
	cmd.Printf("Subscribe GitHub to these events: %s\n", strings.Join(result.Events, ", "))
	if result.SigningSecretConfigured {
		cmd.Println("Signing secret: configured")
	} else {
		cmd.Printf("Next step: set `repo.webhook_signing_secret: $%s` in `WORKFLOW.md`, then use the same value as the GitHub webhook secret.\n", result.SigningSecretEnvVar)
	}
	cmd.Println("Note: Colin uses relevant GitHub webhook deliveries to queue an immediate reconciliation pass, while polling remains active as a fallback.")
	return 0
}
