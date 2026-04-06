package cmd

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/clioutput"
	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

func runSetupLinearApp(cmd *cobra.Command, workflowPath string, connect bool) int {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	result, err := service.LoadLinearAppSetup(ctx, workflowPath)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	renderer := newCommandRenderer(cmd)
	renderer.Section("Overview")
	if len(result.ProjectSlugs) > 1 {
		renderer.Item("Linear projects", strings.Join(result.ProjectSlugs, ", "))
	} else {
		renderer.Item("Linear project", result.ProjectSlug)
	}
	renderer.Item("Webhook URL", result.WebhookURL)
	renderer.Item("Connect URL", result.ConnectURL)
	renderer.Item("Callback URL", result.CallbackURL)
	renderer.Item("Auth file", result.AuthFilePath)
	if result.AppModeEnabled {
		renderer.Item("App mode", "enabled")
	} else {
		renderer.Item("App mode", "disabled")
	}
	renderer.Item("Auth source", result.AuthSource)
	renderer.Item("App actor", result.ActorName)
	renderer.Item("Actor type", result.ActorType)
	if strings.TrimSpace(result.WorkspaceName) != "" {
		renderer.Item("Workspace", result.WorkspaceName)
	}
	renderer.Item("Assignment behavior", result.AssignmentBehavior)
	renderer.Item("Required OAuth scopes", strings.Join(result.RequiredOAuthScopes, ", "))
	renderer.Item("Required webhook categories", strings.Join(result.RequiredWebhookCategories, ", "))
	renderer.Item("Optional classic wake-up webhooks", strings.Join(result.OptionalWakeupEvents, ", "))

	renderer.Section("Checks")
	if result.AppWebhookSigningSecretConfigured {
		renderer.Status(clioutput.StatusOK, "App webhook secret", "configured")
	} else {
		renderer.Status(clioutput.StatusAction, "App webhook secret", "store the Linear app webhook secret in `tracker.app_webhook_signing_secret: $"+result.AppWebhookSigningSecretEnvVar+"`")
	}
	if result.OAuthClientIDConfigured {
		renderer.Status(clioutput.StatusOK, "OAuth client ID", "configured")
	} else {
		renderer.Status(clioutput.StatusAction, "OAuth client ID", "set `tracker.oauth_client_id` or export `"+service.LinearOAuthClientIDEnvVar+"`")
	}
	if result.StoredAuthConfigured {
		renderer.Status(clioutput.StatusOK, "Stored auth", "present in `.colin/auth.json`")
	} else {
		renderer.Status(clioutput.StatusAction, "Stored auth", "run `colin setup linear app --connect` to complete the tailnet-only OAuth flow")
	}
	switch {
	case result.ActorType == "app":
		renderer.Status(clioutput.StatusOK, "Token identity", "resolves to the Linear app actor")
	case result.ActorType == "unknown":
		renderer.Status(clioutput.StatusAction, "Token identity", "complete OAuth into `.colin/auth.json` or export LINEAR_API_KEY for the Linear app actor, then rerun this command")
	default:
		renderer.Status(clioutput.StatusAction, "Token identity", "current credentials resolve to a user actor; app mode requires a Linear app token")
	}
	if result.SupportsAgentSessions {
		renderer.Status(clioutput.StatusOK, "Agent sessions", "supported by the current actor")
	} else {
		renderer.Status(clioutput.StatusAction, "Agent sessions", "confirm the current actor supports agent sessions before enabling app mode")
	}

	renderer.Section("Notes")
	renderer.Status(clioutput.StatusInfo, "Autonomous behavior", "Colin should proactively create an agent session when it picks up work on its own")
	renderer.Status(clioutput.StatusInfo, "", "Keep Colin's existing issue-webhook and polling wake-up path enabled. The Linear app should share `/webhooks/linear`; it should not disable the webhook")
	renderer.Status(clioutput.StatusInfo, "", "If the Linear app webhook uses a different signing secret than the team webhook, store it separately in `tracker.app_webhook_signing_secret`")
	renderer.Status(clioutput.StatusInfo, "", "The OAuth connect and callback URLs live on the tailnet-only Serve side, not on the public Funnel `/webhooks` paths")
	renderer.Status(clioutput.StatusInfo, "", "`colin setup linear webhook` is the team-webhook helper. Configure app-owned webhooks from the Linear app setup itself")
	if !connect {
		return 0
	}

	session, err := service.StartLinearOAuthSetupSession(cmd.Context(), workflowPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}
	defer session.Close(context.Background())

	renderer.Section("Connect")
	renderer.Item("Open this URL on the tailnet", session.ConnectURL)
	renderer.Item("Registered callback URL", session.CallbackURL)
	renderer.Status(clioutput.StatusInfo, "", "Leave this command running until Linear redirects back to Colin")

	waitCtx, waitCancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer waitCancel()
	if err := session.Wait(waitCtx); err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}
	renderer.Section("Complete")
	renderer.Status(clioutput.StatusOK, "Linear OAuth", "credentials stored in `.colin/auth.json`")
	return 0
}
