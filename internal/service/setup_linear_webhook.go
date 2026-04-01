package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tsdiag "github.com/pmenglund/colin/internal/tailscale"
	"github.com/pmenglund/colin/internal/tracker/linear"
)

var ErrMissingWebhookPublicURL = errors.New("missing_webhook_public_url")

// LinearWebhookSetupResult is the operator-facing outcome of webhook setup.
type LinearWebhookSetupResult struct {
	Action                  string
	WebhookID               string
	WebhookName             string
	WebhookURL              string
	TeamID                  string
	TeamName                string
	SigningSecretConfigured bool
	SigningSecretEnvVar     string
}

// SetupLinearWebhook provisions or repairs the watched project's Linear webhook.
func SetupLinearWebhook(ctx context.Context, workflowPath string, webhookName string, optionFns ...Option) (LinearWebhookSetupResult, error) {
	opts := buildOptions(optionFns...)
	_, cfg, err := loadConfig(workflowPath, opts)
	if err != nil {
		return LinearWebhookSetupResult{}, err
	}

	inspector := tsdiag.NewInspector()
	status := inspector.Resolve(ctx, tsdiag.Options{
		WebhookPort:              cfg.Server.WebhookPort,
		ExplicitWebhookPublicURL: webhookPublicURL(cfg.Server),
	})
	baseURL := strings.TrimSpace(status.PublicBaseURL)
	if baseURL == "" {
		return LinearWebhookSetupResult{}, fmt.Errorf("%w: configure `server.webhook_public_url` or run `colin setup tailscale` first", ErrMissingWebhookPublicURL)
	}
	webhookURL := strings.TrimRight(baseURL, "/") + "/webhooks/linear"

	client, err := linear.New(cfg)
	if err != nil {
		return LinearWebhookSetupResult{}, err
	}
	result, err := client.EnsureProjectIssueWebhook(ctx, webhookURL, webhookName)
	if err != nil {
		return LinearWebhookSetupResult{}, err
	}

	return LinearWebhookSetupResult{
		Action:                  result.Action,
		WebhookID:               result.WebhookID,
		WebhookName:             result.Label,
		WebhookURL:              result.URL,
		TeamID:                  result.TeamID,
		TeamName:                result.TeamName,
		SigningSecretConfigured: strings.TrimSpace(cfg.Tracker.WebhookSigningSecret) != "",
		SigningSecretEnvVar:     "LINEAR_WEBHOOK_SECRET",
	}, nil
}
