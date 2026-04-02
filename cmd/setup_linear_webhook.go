package cmd

import (
	"context"
	"time"

	"github.com/pmenglund/colin/internal/clioutput"
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

	renderer := newCommandRenderer(cmd)
	renderer.Section("Overview")
	renderer.Item("Linear webhook", result.Action)
	renderer.Item("Webhook name", result.WebhookName)
	renderer.Item("Team", result.TeamName+" ("+result.TeamID+")")
	renderer.Item("Webhook URL", result.WebhookURL)
	renderer.Item("Webhook ID", result.WebhookID)

	renderer.Section("Checks")
	if result.SigningSecretConfigured {
		renderer.Status(clioutput.StatusOK, "Signing secret", "configured")
	} else {
		renderer.Status(clioutput.StatusAction, "Signing secret", "copy the Linear signing secret from the webhook detail page into `"+result.SigningSecretEnvVar+"`, then reference it from `WORKFLOW.md` as `tracker.webhook_signing_secret: $"+result.SigningSecretEnvVar+"`")
	}

	renderer.Section("Notes")
	renderer.Status(clioutput.StatusInfo, "", "Colin uses relevant Linear issue webhooks to queue an immediate reconciliation pass, while polling remains active as a fallback")
	return 0
}
