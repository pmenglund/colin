package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/clioutput"
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

	renderer := newCommandRenderer(cmd)
	renderer.Section("Overview")
	renderer.Item("Linear project", result.ProjectSlug)
	renderer.Item("Webhook URL", result.WebhookURL)
	renderer.Item("App actor", result.ActorType)
	renderer.Item("Assignment behavior", result.AssignmentBehavior)
	renderer.Item("Required webhook categories", strings.Join(result.RequiredWebhookCategories, ", "))
	renderer.Item("Optional classic wake-up webhooks", strings.Join(result.OptionalWakeupEvents, ", "))

	renderer.Section("Checks")
	if result.SigningSecretConfigured {
		renderer.Status(clioutput.StatusOK, "Signing secret", "configured")
	} else {
		renderer.Status(clioutput.StatusAction, "Signing secret", "store the shared webhook secret in `tracker.webhook_signing_secret: $"+result.SigningSecretEnvVar+"`")
	}

	renderer.Section("Notes")
	renderer.Status(clioutput.StatusInfo, "Autonomous behavior", "Colin should proactively create an agent session when it picks up work on its own")
	renderer.Status(clioutput.StatusInfo, "", "Keep Colin's existing issue-webhook and polling wake-up path enabled. The Linear app should share `/webhooks/linear`; it should not disable the webhook")
	renderer.Status(clioutput.StatusInfo, "", "`colin setup linear webhook` is the team-webhook helper. Configure app-owned webhooks from the Linear app setup itself")
	return 0
}
