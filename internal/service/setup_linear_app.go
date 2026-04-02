package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/pmenglund/colin/internal/config"
)

const LinearWebhookSigningSecretEnvVar = "LINEAR_WEBHOOK_SECRET"

// LinearAppSetupResult is the operator-facing Linear app sketch for one workflow.
type LinearAppSetupResult struct {
	ProjectSlug               string
	WebhookURL                string
	ActorType                 string
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
	projectSlug := strings.TrimSpace(cfg.Tracker.ProjectSlug)
	if projectSlug == "" {
		return LinearAppSetupResult{}, config.ErrMissingTrackerProject
	}

	baseURL := resolveWebhookPublicBaseURL(ctx, nil, cfg.Server, "")
	if baseURL == "" {
		return LinearAppSetupResult{}, fmt.Errorf("%w: configure `server.webhook_public_url` or run `colin setup tailscale` first", ErrMissingWebhookPublicURL)
	}

	return LinearAppSetupResult{
		ProjectSlug:               projectSlug,
		WebhookURL:                strings.TrimRight(baseURL, "/") + "/webhooks/linear",
		ActorType:                 "app",
		AssignmentBehavior:        "assigning an issue to Colin should delegate the work while the human owner remains accountable",
		RequiredWebhookCategories: []string{"AgentSessionEvent"},
		OptionalWakeupEvents:      []string{"Issue create", "Issue update"},
		SigningSecretConfigured:   strings.TrimSpace(cfg.Tracker.WebhookSigningSecret) != "",
		SigningSecretEnvVar:       LinearWebhookSigningSecretEnvVar,
	}, nil
}
