package cmd

import (
	"strings"

	"github.com/pmenglund/colin/internal/clioutput"
	"github.com/pmenglund/colin/internal/service"
	"github.com/spf13/cobra"
)

func runSetupSlack(cmd *cobra.Command, workflowPath string) int {
	result, err := service.LoadSlackSetup(workflowPath)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	renderer := newCommandRenderer(cmd)
	renderer.Section("Overview")
	if result.ChannelConfigured {
		renderer.Item("Slack channel", result.ChannelID)
	} else {
		renderer.Item("Slack channel", "not configured")
	}
	renderer.Item("Delivery mode", "bot token for messages plus Socket Mode for button acknowledgements")
	renderer.Item("Bot token scopes", strings.Join(result.RequiredBotScopes, ", "))
	renderer.Item("App token scopes", strings.Join(result.RequiredAppScopes, ", "))
	renderer.Item("Slack app features", strings.Join(result.RequiredAppFeatures, ", "))

	renderer.Section("Checks")
	renderSlackSettingCheck(renderer, "slack.bot_token", "add `slack.bot_token: $"+service.SlackBotTokenEnvVar+"` to `WORKFLOW.md`", result.BotTokenDeclared, result.BotTokenConfigured, result.BotTokenSource, result.BotTokenEnvVar)
	renderSlackSettingCheck(renderer, "slack.app_token", "add `slack.app_token: $"+service.SlackAppTokenEnvVar+"` to `WORKFLOW.md`", result.AppTokenDeclared, result.AppTokenConfigured, result.AppTokenSource, result.AppTokenEnvVar)
	renderSlackSettingCheck(renderer, "slack.channel_id", "add `slack.channel_id: C0123456789` to `WORKFLOW.md`", result.ChannelDeclared, result.ChannelConfigured, result.ChannelSource, result.ChannelEnvVar)

	renderer.Section("Notes")
	renderer.Status(clioutput.StatusInfo, "Slack app", "Create or reuse one Slack app installed in the target workspace")
	renderer.Status(clioutput.StatusInfo, "OAuth", "Generate the bot token from the app's OAuth configuration and export it before starting Colin")
	renderer.Status(clioutput.StatusInfo, "App token", "Generate an app-level token with `connections:write`, then export it before starting Colin")
	renderer.Status(clioutput.StatusInfo, "Slack settings", "Enable both Socket Mode and Interactivity for the Slack app")
	if result.ChannelConfigured {
		renderer.Status(clioutput.StatusInfo, "Channel access", "Invite the Slack app to `"+result.ChannelID+"` before expecting Colin to post updates there")
	} else {
		renderer.Status(clioutput.StatusInfo, "Channel access", "Invite the Slack app to the channel you configure in `slack.channel_id` before expecting Colin to post updates there")
	}
	return 0
}

func renderSlackSettingCheck(renderer *clioutput.Renderer, label string, missingDetail string, declared bool, configured bool, source string, envVar string) {
	switch {
	case configured:
		if source != "" {
			renderer.Status(clioutput.StatusOK, label, "configured as `"+source+"`")
			return
		}
		renderer.Status(clioutput.StatusOK, label, "configured")
	case declared && envVar != "":
		renderer.Status(clioutput.StatusAction, label, "set `"+envVar+"` in the environment so `"+label+"` resolves")
	case declared:
		renderer.Status(clioutput.StatusAction, label, "update `"+label+"` so it resolves to a non-empty value")
	default:
		renderer.Status(clioutput.StatusAction, label, missingDetail)
	}
}
