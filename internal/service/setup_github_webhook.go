package service

import (
	"context"
	"fmt"
	"strings"
)

const GitHubWebhookSigningSecretEnvVar = "GITHUB_WEBHOOK_SECRET"

var githubWebhookEvents = []string{
	"pull_request",
	"pull_request_review",
	"pull_request_review_comment",
	"pull_request_review_thread",
}

// GitHubWebhookSetupResult is the operator-facing outcome of GitHub webhook setup guidance.
type GitHubWebhookSetupResult struct {
	BackendDisplayName      string
	RepositoryURL           string
	RepositoryOwner         string
	RepositoryName          string
	RepositorySource        string
	WebhookURL              string
	SigningSecretConfigured bool
	SigningSecretEnvVar     string
	Events                  []string
}

// LoadGitHubWebhookSetup loads the watched GitHub repository and current public webhook URL.
func LoadGitHubWebhookSetup(ctx context.Context, workflowPath string, workingDir string, optionFns ...Option) (GitHubWebhookSetupResult, error) {
	opts := buildOptions(optionFns...)
	_, cfg, err := loadConfig(workflowPath, opts)
	if err != nil {
		return GitHubWebhookSetupResult{}, err
	}

	result, err := LoadRepoTokenSetup(workflowPath, workingDir, optionFns...)
	if err != nil {
		return GitHubWebhookSetupResult{}, err
	}
	if !strings.EqualFold(result.Backend, "github") {
		return GitHubWebhookSetupResult{}, fmt.Errorf("workflow backend is %q, use `colin setup repo` instead", result.Backend)
	}

	baseURL := resolveWebhookPublicBaseURL(ctx, nil, cfg.Server, "")
	if baseURL == "" {
		return GitHubWebhookSetupResult{}, fmt.Errorf("%w: configure `server.webhook_public_url` or run `colin setup tailscale` first", ErrMissingWebhookPublicURL)
	}

	return GitHubWebhookSetupResult{
		BackendDisplayName:      result.BackendDisplayName,
		RepositoryURL:           result.RepositoryURL,
		RepositoryOwner:         result.RepositoryOwner,
		RepositoryName:          result.RepositoryName,
		RepositorySource:        result.RepositorySource,
		WebhookURL:              strings.TrimRight(baseURL, "/") + "/webhooks/github",
		SigningSecretConfigured: strings.TrimSpace(cfg.Repo.WebhookSigningSecret) != "",
		SigningSecretEnvVar:     GitHubWebhookSigningSecretEnvVar,
		Events:                  append([]string(nil), githubWebhookEvents...),
	}, nil
}
