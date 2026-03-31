package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/service"
)

func runSetupLinearWebhook(cmd *cobra.Command, workflowPath string, webhookName string) int {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	result, err := service.SetupLinearWebhook(ctx, workflowPath, webhookName)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	cmd.Printf("Linear webhook: %s\n", result.Action)
	cmd.Printf("Webhook name: %s\n", result.WebhookName)
	cmd.Printf("Team: %s (%s)\n", result.TeamName, result.TeamID)
	cmd.Printf("Webhook URL: %s\n", result.WebhookURL)
	cmd.Printf("Webhook ID: %s\n", result.WebhookID)
	if result.SigningSecretConfigured {
		cmd.Println("Signing secret: configured")
	} else {
		cmd.Printf("Next step: copy the Linear signing secret from the webhook detail page into `%s`, then reference it from `WORKFLOW.md` as `tracker.webhook_signing_secret: $%s`.\n", result.SigningSecretEnvVar, result.SigningSecretEnvVar)
	}
	cmd.Printf("Note: Colin currently acknowledges Linear webhook deliveries but does not yet trigger orchestration directly from them.\n")
	return 0
}
