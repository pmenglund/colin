package service

import (
	"context"
	"strings"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/tracker/linear"
)

const LinearWebhookSigningSecretEnvVar = "LINEAR_WEBHOOK_SECRET"
const LinearOAuthClientIDEnvVar = linear.OAuthClientIDEnvVar

var newLinearAppSetupClient = linear.New

// LinearAppSetupResult is the operator-facing Linear app sketch for one workflow.
type LinearAppSetupResult struct {
	ProjectSlug               string
	ProjectSlugs              []string
	WebhookURL                string
	ConnectURL                string
	CallbackURL               string
	AuthFilePath              string
	AppModeEnabled            bool
	OAuthClientIDConfigured   bool
	StoredAuthConfigured      bool
	AuthSource                string
	ActorName                 string
	ActorType                 string
	SupportsAgentSessions     bool
	WorkspaceName             string
	AssignmentBehavior        string
	RequiredWebhookCategories []string
	OptionalWakeupEvents      []string
	SigningSecretConfigured   bool
	SigningSecretEnvVar       string
}

// LoadLinearAppSetup loads WORKFLOW.md and returns the intended self-hosted Linear app shape.
func LoadLinearAppSetup(ctx context.Context, workflowPath string, optionFns ...Option) (LinearAppSetupResult, error) {
	opts := buildOptions(optionFns...)
	_, cfg, err := loadConfig(workflowPath, opts)
	if err != nil {
		return LinearAppSetupResult{}, err
	}
	projectSlugs := cfg.WatchedProjectSlugs()
	if len(projectSlugs) == 0 {
		return LinearAppSetupResult{}, config.ErrMissingTrackerProject
	}
	projectSlug := strings.TrimSpace(projectSlugs[0])

	uiBaseURL := resolveUIBaseURL(ctx, nil, cfg.Server)
	baseURL := resolveWebhookPublicBaseURL(ctx, nil, cfg.Server, "")
	connectURL := linearOAuthConnectURL(uiBaseURL)
	callbackURL := linearOAuthCallbackURL(uiBaseURL)
	actorName := "unresolved"
	actorType := "unknown"
	supportsAgentSessions := false
	workspaceName := ""
	authSource := "none"
	authFile, authErr := linear.LoadAuthFile(workflowPath)
	if strings.TrimSpace(cfg.Tracker.APIKey) != "" {
		authSource = "env"
	} else if authErr == nil && strings.TrimSpace(authFile.Linear.AccessToken) != "" {
		authSource = "auth_file"
		actorName = strings.TrimSpace(authFile.Linear.ActorName)
		actorType = strings.TrimSpace(authFile.Linear.ActorType)
		supportsAgentSessions = authFile.Linear.SupportsAgentSessions
		workspaceName = strings.TrimSpace(authFile.Linear.WorkspaceName)
	}
	if trackerClient, err := newLinearAppSetupClient(cfg); err == nil {
		if identity, err := trackerClient.ActorIdentity(ctx); err == nil {
			actorName = identity.Name
			actorType = identity.Type()
			supportsAgentSessions = identity.SupportsAgentSessions
		}
	}

	return LinearAppSetupResult{
		ProjectSlug:               projectSlug,
		ProjectSlugs:              projectSlugs,
		WebhookURL:                linearWebhookURL(baseURL),
		ConnectURL:                connectURL,
		CallbackURL:               callbackURL,
		AuthFilePath:              linear.AuthFilePath(workflowPath),
		AppModeEnabled:            cfg.Tracker.AppMode,
		OAuthClientIDConfigured:   strings.TrimSpace(cfg.Tracker.OAuthClientID) != "",
		StoredAuthConfigured:      authErr == nil && strings.TrimSpace(authFile.Linear.AccessToken) != "",
		AuthSource:                authSource,
		ActorName:                 actorName,
		ActorType:                 actorType,
		SupportsAgentSessions:     supportsAgentSessions,
		WorkspaceName:             workspaceName,
		AssignmentBehavior:        "assigning an issue to Colin should delegate the work while the human owner remains accountable",
		RequiredWebhookCategories: []string{"AgentSessionEvent"},
		OptionalWakeupEvents:      []string{"Issue create", "Issue update"},
		SigningSecretConfigured:   strings.TrimSpace(cfg.Tracker.WebhookSigningSecret) != "",
		SigningSecretEnvVar:       LinearWebhookSigningSecretEnvVar,
	}, nil
}

func linearWebhookURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + "/webhooks/linear"
}
