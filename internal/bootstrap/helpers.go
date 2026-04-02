package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/githubauth"
	"github.com/pmenglund/colin/internal/workflow"
)

type resolvedOptions struct {
	workflowPath string
	workingDir   string
	prereqs      Prerequisites
	defaults     detectedDefaults
	linearAPIKey string
	githubToken  string
}

func resolveOptions(opts Options) (resolvedOptions, error) {
	workflowPath := strings.TrimSpace(opts.WorkflowPath)
	if workflowPath == "" {
		workflowPath = "WORKFLOW.md"
	}

	workingDir := strings.TrimSpace(opts.WorkingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return resolvedOptions{}, fmt.Errorf("resolve current working directory: %w", err)
		}
		workingDir = cwd
	}

	return resolvedOptions{
		workflowPath: workflowPath,
		workingDir:   workingDir,
		prereqs:      detectPrerequisites(),
		defaults:     detectDefaults(workingDir),
		linearAPIKey: currentLinearAPIKey(),
		githubToken:  githubauth.CurrentToken(),
	}, nil
}

func isValidLinearAPIKey(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "lin_api_")
}

func validateOptionalLinearAPIKey(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if !isValidLinearAPIKey(trimmed) {
		return "LINEAR_API_KEY should start with lin_api_."
	}
	return ""
}

func isValidGitHubToken(value string) bool {
	return githubauth.IsValidToken(value)
}

func validateOptionalGitHubToken(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if !isValidGitHubToken(trimmed) {
		return "GITHUB_TOKEN should start with github_pat_ or ghp_."
	}
	return ""
}

func validateProjectSlug(value string) string {
	if strings.TrimSpace(value) == "" {
		return "Linear project slug is required."
	}
	return ""
}

func validateRepoURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "Repository URL is required."
	}
	if _, err := githubauth.ParseRepositoryURL(trimmed); err != nil {
		return "Repository URL must be a supported github.com SSH or HTTPS remote."
	}
	return ""
}

func validateBaseRef(value string) string {
	if strings.TrimSpace(value) == "" {
		return "Base branch is required."
	}
	return ""
}

func validateWorkspaceRoot(value string) string {
	if strings.TrimSpace(value) == "" {
		return "Workspace root is required."
	}
	return ""
}

func validateServerPort(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "Server port is required."
	}
	port, err := strconv.Atoi(trimmed)
	if err != nil || port <= 0 {
		return "Server port must be a positive integer."
	}
	return ""
}

func validateWebhookPort(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "Webhook port is required."
	}
	port, err := strconv.Atoi(trimmed)
	if err != nil || port <= 0 {
		return "Webhook port must be a positive integer."
	}
	return ""
}

func parseServerPort(value string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("server port must be a positive integer")
	}
	return port, nil
}

func parseWebhookPort(value string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("webhook port must be a positive integer")
	}
	return port, nil
}

func previewWorkspaceRoot(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			switch {
			case value == "~":
				value = home
			case strings.HasPrefix(value, "~/"):
				value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
			}
		}
	}
	cleaned := filepath.Clean(value)
	if abs, err := filepath.Abs(cleaned); err == nil {
		return abs
	}
	return cleaned
}

func overwriteNeeded(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat workflow path: %w", err)
}

func resultFromAnswers(path string, answers Answers, prereqs Prerequisites) Result {
	return Result{
		WorkflowPath:  path,
		WroteWorkflow: true,
		Answers:       answers,
		Prereqs:       prereqs,
	}
}

func completionText(result Result, autoStart bool) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Wrote %s", result.WorkflowPath))
	if result.SessionLinearAPIKeyLoaded {
		lines = append(lines, "LINEAR_API_KEY was loaded for this setup session only. Export it in your shell before the next Colin run.")
	} else if result.Prereqs.LinearAPIKeyConfigured {
		lines = append(lines, "LINEAR_API_KEY is already configured in this shell.")
	} else {
		lines = append(lines, "Next step: export LINEAR_API_KEY in your shell before running Colin.")
	}
	if result.SessionGitHubTokenLoaded {
		lines = append(lines, "GITHUB_TOKEN was loaded for this setup session only. Export it in your shell before the next Colin run.")
	} else if result.Prereqs.GitHubTokenConfigured {
		lines = append(lines, "GITHUB_TOKEN or GH_TOKEN is already configured in this shell.")
	} else {
		lines = append(lines, "Next step: export GITHUB_TOKEN before moving issues into `Review` or `Merge`. Run `colin setup repo` for the exact token settings.")
	}
	if !result.Prereqs.CodexAvailable {
		lines = append(lines, "Warning: `codex` was not found in PATH. Install or expose Codex before expecting Colin to launch coding runs.")
	}
	if !result.Prereqs.GitAvailable {
		lines = append(lines, "Warning: `git` was not found in PATH. Colin needs git for workspace preparation.")
	}
	if result.Answers.WantsWebhook {
		if autoStart {
			lines = append(lines, "Webhook setup requires Tailscale. Colin will continue starting; once the setup URL is printed, open it and then run `colin setup linear webhook` after Funnel is ready.")
		} else {
			lines = append(lines, "Webhook setup requires Tailscale. After Colin is running, use `colin setup tailscale` and then `colin setup linear webhook`, or open `/setup/funnel` from the local UI.")
		}
	} else {
		lines = append(lines, "Webhook setup skipped.")
	}
	return strings.Join(lines, "\n")
}

func buildConfigFromAnswers(workflowPath string, answers Answers, linearAPIKey string, githubToken string) (string, domain.ServiceConfig, error) {
	content, err := RenderWorkflow(answers)
	if err != nil {
		return "", domain.ServiceConfig{}, err
	}
	def, err := workflow.Parse(workflowPath, []byte(content))
	if err != nil {
		return "", domain.ServiceConfig{}, err
	}
	cfg, err := config.Build(def, workflowPath)
	if err != nil {
		return "", domain.ServiceConfig{}, err
	}
	if key := strings.TrimSpace(linearAPIKey); isValidLinearAPIKey(key) {
		cfg.Tracker.APIKey = key
	}
	if token := strings.TrimSpace(githubToken); isValidGitHubToken(token) {
		cfg.Repo.APIToken = token
	}
	return content, cfg, nil
}

func applySessionLinearAPIKey(value string) error {
	value = strings.TrimSpace(value)
	if !isValidLinearAPIKey(value) || isValidLinearAPIKey(currentLinearAPIKey()) {
		return nil
	}
	if err := os.Setenv("LINEAR_API_KEY", value); err != nil {
		return fmt.Errorf("set LINEAR_API_KEY for current session: %w", err)
	}
	return nil
}

func applySessionGitHubToken(value string) error {
	value = strings.TrimSpace(value)
	if !isValidGitHubToken(value) || isValidGitHubToken(githubauth.CurrentToken()) {
		return nil
	}
	if err := os.Setenv("GITHUB_TOKEN", value); err != nil {
		return fmt.Errorf("set GITHUB_TOKEN for current session: %w", err)
	}
	return nil
}
