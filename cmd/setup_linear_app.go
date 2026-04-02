package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

func runSetupLinearApp(cmd *cobra.Command, workflowPath string) int {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	result, err := service.LoadLinearAppSetup(ctx, workflowPath)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	cmd.Printf("Linear project: %s\n", result.ProjectSlug)
	cmd.Printf("Webhook URL: %s\n", result.WebhookURL)
	cmd.Printf("App actor: %s\n", result.ActorType)
	cmd.Printf("Assignment behavior: %s\n", result.AssignmentBehavior)
	cmd.Printf("Required webhook categories: %s\n", strings.Join(result.RequiredWebhookCategories, ", "))
	cmd.Printf("Optional classic wake-up webhooks: %s\n", strings.Join(result.OptionalWakeupEvents, ", "))
	if result.SigningSecretConfigured {
		cmd.Println("Signing secret: configured")
	} else {
		cmd.Printf("Next step: store the shared webhook secret in `tracker.webhook_signing_secret: $%s`.\n", result.SigningSecretEnvVar)
	}
	cmd.Println("Autonomous behavior: Colin should proactively create an agent session when it picks up work on its own.")
	cmd.Println("Note: Keep Colin's existing issue-webhook and polling wake-up path enabled. The Linear app should share `/webhooks/linear`; it should not disable the webhook.")
	cmd.Println("Note: `colin setup linear` is the legacy team-webhook helper. Configure app-owned webhooks from the Linear app setup itself.")
	return 0
}
