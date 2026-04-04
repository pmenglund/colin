package service

import (
	"os"
	"strings"

	"github.com/pmenglund/colin/internal/workflow"
)

const (
	SlackBotTokenEnvVar = "SLACK_BOT_TOKEN"
	SlackAppTokenEnvVar = "SLACK_APP_TOKEN"
)

var slackBotTokenScopes = []string{"chat:write"}
var slackAppTokenScopes = []string{"connections:write"}
var slackAppFeatures = []string{"Socket Mode", "Interactivity"}

// SlackSetupResult is the operator-facing Slack setup guidance for one workflow.
type SlackSetupResult struct {
	BotTokenDeclared    bool
	BotTokenConfigured  bool
	BotTokenSource      string
	BotTokenEnvVar      string
	AppTokenDeclared    bool
	AppTokenConfigured  bool
	AppTokenSource      string
	AppTokenEnvVar      string
	ChannelDeclared     bool
	ChannelConfigured   bool
	ChannelID           string
	ChannelSource       string
	ChannelEnvVar       string
	RequiredBotScopes   []string
	RequiredAppScopes   []string
	RequiredAppFeatures []string
}

// LoadSlackSetup inspects the workflow's Slack section and reports what Colin expects.
func LoadSlackSetup(workflowPath string) (SlackSetupResult, error) {
	loader := workflow.Loader{}
	path := loader.ResolvePath(workflowPath)
	def, err := loader.Load(path)
	if err != nil {
		return SlackSetupResult{}, err
	}

	botSource := strings.TrimSpace(slackStringValue(def.Config.Slack.BotToken))
	appSource := strings.TrimSpace(slackStringValue(def.Config.Slack.AppToken))
	channelSource := strings.TrimSpace(slackStringValue(def.Config.Slack.ChannelID))

	botToken, botEnvVar := resolveSlackValue(botSource)
	appToken, appEnvVar := resolveSlackValue(appSource)
	channelID, channelEnvVar := resolveSlackValue(channelSource)

	return SlackSetupResult{
		BotTokenDeclared:    botSource != "",
		BotTokenConfigured:  botToken != "",
		BotTokenSource:      botSource,
		BotTokenEnvVar:      botEnvVar,
		AppTokenDeclared:    appSource != "",
		AppTokenConfigured:  appToken != "",
		AppTokenSource:      appSource,
		AppTokenEnvVar:      appEnvVar,
		ChannelDeclared:     channelSource != "",
		ChannelConfigured:   channelID != "",
		ChannelID:           channelID,
		ChannelSource:       channelSource,
		ChannelEnvVar:       channelEnvVar,
		RequiredBotScopes:   append([]string(nil), slackBotTokenScopes...),
		RequiredAppScopes:   append([]string(nil), slackAppTokenScopes...),
		RequiredAppFeatures: append([]string(nil), slackAppFeatures...),
	}, nil
}

func slackStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func resolveSlackValue(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if strings.HasPrefix(value, "$") && len(value) > 1 {
		envVar := strings.TrimSpace(strings.TrimPrefix(value, "$"))
		return strings.TrimSpace(os.Getenv(envVar)), envVar
	}
	return value, ""
}
