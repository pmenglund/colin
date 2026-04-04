package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	_ "github.com/pmenglund/colin/internal/repohost/github"
)

var (
	ErrUnsupportedTrackerKind  = errors.New("unsupported_tracker_kind")
	ErrMissingTrackerAPIKey    = errors.New("missing_tracker_api_key")
	ErrMissingTrackerProject   = errors.New("missing_tracker_project_slug")
	ErrMissingCodexCommand     = errors.New("missing_codex_command")
	ErrMissingWorkflowPath     = errors.New("missing_workflow_file")
	ErrInvalidWorkflowConfig   = errors.New("invalid_workflow_config")
	ErrInvalidWorkspaceGitConf = errors.New("invalid_workspace_git_config")
	ErrInvalidRepoMergeMethod  = errors.New("invalid_repo_merge_method")
	ErrUnsupportedRepoBackend  = errors.New("unsupported_repo_backend")
	ErrMixedTargetConfig       = errors.New("mixed_target_config")
)

const defaultPRTemplate = `## Summary

Automated changes for {{.issue.identifier}}.

## Linear

- Issue: {{.issue.identifier}}
{{- if .issue.url }}
- URL: {{ .issue.url }}
{{- end }}`

const defaultBranchTemplate = `colin/{{.issue.identifier}}-{{.issue.title}}`

// Build converts a workflow definition into typed runtime configuration with defaults applied.
func Build(def domain.WorkflowDefinition, workflowPath string) (domain.ServiceConfig, error) {
	cfg := domain.ServiceConfig{
		WorkflowPath: workflowPath,
		Tracker: domain.TrackerConfig{
			Endpoint:       "https://api.linear.app/graphql",
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"},
		},
		Polling: domain.PollingConfig{Interval: 30 * time.Second},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(os.TempDir(), "symphony_workspaces"),
		},
		Repo: domain.RepoConfig{
			Backend:               string(repohost.HostKindGitHub),
			PublishStates:         []string{"Review"},
			MergeStates:           []string{"Merge"},
			RemoteName:            "origin",
			MergeMethod:           "merge",
			BranchTemplate:        defaultBranchTemplate,
			PRTemplate:            defaultPRTemplate,
			CodexPRReviewsEnabled: false,
		},
		Hooks: domain.HookConfig{
			Timeout: 60 * time.Second,
		},
		Agent: domain.AgentConfig{
			MaxConcurrentAgents:        10,
			MaxRetryBackoff:            5 * time.Minute,
			MaxConcurrentAgentsByState: map[string]int{},
			MaxTurns:                   20,
			CreateExecPlan:             false,
		},
		Codex: domain.CodexConfig{
			Command:           "codex app-server",
			CLICommand:        "codex",
			TurnTimeout:       1 * time.Hour,
			ReadTimeout:       5 * time.Second,
			StallTimeout:      5 * time.Minute,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: domain.SandboxPolicy{Type: "dangerFullAccess"},
		},
		Server: domain.ServerConfig{
			Port:           intPtr(8888),
			LogBufferLines: domain.DefaultLogBufferLines,
		},
	}

	if err := applyTrackerConfig(&cfg, def.Config.Tracker); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyPollingConfig(&cfg, def.Config.Polling); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyWorkspaceConfig(&cfg, def.Config.Workspace); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyRepoConfig(&cfg, def.Config.Repo); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyHooksConfig(&cfg, def.Config.Hooks); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyAgentConfig(&cfg, def.Config.Agent); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyCodexConfig(&cfg, def.Config.Codex); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyServerConfig(&cfg, def.Config.Server); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyTargetsConfig(&cfg, def.Config); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applySlackConfig(&cfg, def.Config.Slack); err != nil {
		return domain.ServiceConfig{}, err
	}

	normalizeStateList(cfg.Tracker.ActiveStates)
	normalizeStateList(cfg.Tracker.TerminalStates)
	normalizeStateList(cfg.Repo.PublishStates)
	normalizeStateList(cfg.Repo.MergeStates)

	if cfg.Hooks.Timeout <= 0 {
		cfg.Hooks.Timeout = 60 * time.Second
	}

	if !validMergeMethod(cfg.Repo.MergeMethod) {
		return domain.ServiceConfig{}, ErrInvalidRepoMergeMethod
	}
	if _, err := repohost.Lookup(cfg.Repo.Backend); err != nil {
		return domain.ServiceConfig{}, ErrUnsupportedRepoBackend
	}
	cfg.Codex.TurnSandboxPolicy = normalizeSandboxPolicy(cfg.Codex.TurnSandboxPolicy)

	return cfg, nil
}

// ValidateDispatch checks the minimum config required to poll and dispatch work.
func ValidateDispatch(cfg domain.ServiceConfig) error {
	if cfg.Tracker.Kind == "" {
		return ErrUnsupportedTrackerKind
	}
	if strings.ToLower(cfg.Tracker.Kind) != "linear" {
		return ErrUnsupportedTrackerKind
	}
	if cfg.Tracker.APIKey == "" {
		return ErrMissingTrackerAPIKey
	}
	if len(cfg.Targets) == 0 && cfg.Tracker.ProjectSlug == "" {
		return ErrMissingTrackerProject
	}
	if strings.TrimSpace(cfg.Codex.Command) == "" {
		return ErrMissingCodexCommand
	}
	if _, err := repohost.Lookup(cfg.Repo.Backend); err != nil {
		return ErrUnsupportedRepoBackend
	}
	return nil
}

func applyTargetsConfig(cfg *domain.ServiceConfig, raw domain.WorkflowConfig) error {
	hasLegacyProject := stringValue(raw.Tracker.ProjectSlug) != ""
	hasLegacyRepoURL := stringValue(raw.Workspace.RepoURL) != ""
	hasLegacyBaseRef := stringValue(raw.Workspace.BaseRef) != ""
	hasLegacy := hasLegacyProject || hasLegacyRepoURL || hasLegacyBaseRef
	hasTargets := len(raw.Targets) > 0

	if hasTargets && hasLegacy {
		return fmt.Errorf("%w: %w: use either targets or tracker.project_slug/workspace.repo_url/workspace.base_ref, not both", ErrInvalidWorkflowConfig, ErrMixedTargetConfig)
	}

	targets := make([]domain.TargetConfig, 0, max(1, len(raw.Targets)))
	if hasTargets {
		for i, target := range raw.Targets {
			normalized, err := normalizeTargetConfig(cfg, target, i)
			if err != nil {
				return err
			}
			targets = append(targets, normalized)
		}
	} else {
		if cfg.Workspace.RepoURL != "" || cfg.Workspace.BaseRef != "" {
			if cfg.Workspace.RepoURL == "" || cfg.Workspace.BaseRef == "" {
				return ErrInvalidWorkspaceGitConf
			}
		}
		if strings.TrimSpace(cfg.Tracker.ProjectSlug) != "" {
			targets = append(targets, domain.TargetConfig{
				Key:         deriveTargetKey(cfg.Tracker.ProjectSlug, cfg.Workspace.RepoURL),
				Name:        cfg.Tracker.ProjectSlug,
				ProjectSlug: cfg.Tracker.ProjectSlug,
				RepoURL:     cfg.Workspace.RepoURL,
				BaseRef:     cfg.Workspace.BaseRef,
			})
		}
	}

	if len(targets) == 0 {
		cfg.Targets = nil
		return nil
	}
	seenProjects := map[string]struct{}{}
	seenKeys := map[string]struct{}{}
	for _, target := range targets {
		projectKey := strings.ToLower(strings.TrimSpace(target.ProjectSlug))
		if projectKey == "" {
			return fmt.Errorf("%w: target project_slug is required", ErrInvalidWorkflowConfig)
		}
		if _, ok := seenProjects[projectKey]; ok {
			return fmt.Errorf("%w: duplicate target project_slug %q", ErrInvalidWorkflowConfig, target.ProjectSlug)
		}
		seenProjects[projectKey] = struct{}{}
		key := strings.ToLower(strings.TrimSpace(target.Key))
		if key == "" {
			return fmt.Errorf("%w: target key is required", ErrInvalidWorkflowConfig)
		}
		if _, ok := seenKeys[key]; ok {
			return fmt.Errorf("%w: duplicate target key %q", ErrInvalidWorkflowConfig, target.Key)
		}
		seenKeys[key] = struct{}{}
	}

	cfg.Targets = targets
	cfg.Tracker.ProjectSlug = targets[0].ProjectSlug
	cfg.Workspace.RepoURL = targets[0].RepoURL
	cfg.Workspace.BaseRef = targets[0].BaseRef
	return nil
}

func normalizeTargetConfig(cfg *domain.ServiceConfig, raw domain.WorkflowTargetConfig, index int) (domain.TargetConfig, error) {
	projectSlug := stringValue(raw.ProjectSlug)
	repoURL := stringValue(raw.RepoURL)
	baseRef := stringValue(raw.BaseRef)
	if projectSlug == "" || repoURL == "" || baseRef == "" {
		return domain.TargetConfig{}, fmt.Errorf("%w: target %d must set project_slug, repo_url, and base_ref", ErrInvalidWorkflowConfig, index+1)
	}
	name := stringValue(raw.Name)
	if name == "" {
		name = projectSlug
	}
	return domain.TargetConfig{
		Key:         deriveTargetKey(projectSlug, repoURL),
		Name:        name,
		ProjectSlug: projectSlug,
		RepoURL:     repoURL,
		BaseRef:     baseRef,
	}, nil
}

func deriveTargetKey(projectSlug string, repoURL string) string {
	projectSlug = strings.ToLower(strings.TrimSpace(projectSlug))
	repoName := strings.ToLower(strings.TrimSpace(repoURL))
	if adapter, err := repohost.Lookup(string(repohost.HostKindGitHub)); err == nil {
		if parsed, err := adapter.ParseRepositoryURL(repoURL); err == nil {
			repoName = strings.ToLower(strings.TrimSpace(parsed.Name))
		}
	}
	projectSlug = sanitizeTargetKeyPart(projectSlug)
	repoName = sanitizeTargetKeyPart(repoName)
	switch {
	case projectSlug != "" && repoName != "":
		return projectSlug + "-" + repoName
	case projectSlug != "":
		return projectSlug
	case repoName != "":
		return repoName
	default:
		return "target"
	}
}

func sanitizeTargetKeyPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// NormalizedStateSet converts state names into a lowercase lookup set.
func NormalizedStateSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		out[normalized] = struct{}{}
	}
	return out
}

// StateKey normalizes a tracker state name for map lookups.
func StateKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ContainsState reports whether the normalized state name exists in the supplied list.
func ContainsState(values []string, state string) bool {
	_, ok := NormalizedStateSet(values)[StateKey(state)]
	return ok
}

// CandidateStates returns the union of coding and repo-automation states that should be polled.
func CandidateStates(cfg domain.ServiceConfig) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, state := range append(append([]string{}, cfg.Tracker.ActiveStates...), append(cfg.Repo.PublishStates, cfg.Repo.MergeStates...)...) {
		normalized := strings.TrimSpace(state)
		if normalized == "" {
			continue
		}
		key := StateKey(normalized)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func applyTrackerConfig(cfg *domain.ServiceConfig, raw domain.WorkflowTrackerConfig) error {
	if value := stringValue(raw.Kind); value != "" {
		cfg.Tracker.Kind = value
	}
	if value := stringValue(raw.Endpoint); value != "" {
		cfg.Tracker.Endpoint = value
	}
	if value := stringValue(raw.APIKey); value != "" {
		cfg.Tracker.APIKey = resolveEnvToken(value)
	}
	if value := stringValue(raw.WebhookSigningSecret); value != "" {
		cfg.Tracker.WebhookSigningSecret = resolveEnvToken(value)
	}
	if value := stringValue(raw.ProjectSlug); value != "" {
		cfg.Tracker.ProjectSlug = value
	}
	if len(raw.ActiveStates) > 0 {
		cfg.Tracker.ActiveStates = append([]string(nil), raw.ActiveStates...)
	}
	if len(raw.TerminalStates) > 0 {
		cfg.Tracker.TerminalStates = append([]string(nil), raw.TerminalStates...)
	}
	if cfg.Tracker.APIKey == "" {
		cfg.Tracker.APIKey = strings.TrimSpace(os.Getenv("LINEAR_API_KEY"))
	}
	return nil
}

func applyPollingConfig(cfg *domain.ServiceConfig, raw domain.WorkflowPollingConfig) error {
	if value, ok := durationMillisValue(raw.IntervalMillis); ok && value > 0 {
		cfg.Polling.Interval = value
	}
	return nil
}

func applyWorkspaceConfig(cfg *domain.ServiceConfig, raw domain.WorkflowWorkspaceConfig) error {
	if value := stringValue(raw.Root); value != "" {
		cfg.Workspace.Root = expandPath(resolveEnvToken(value))
	} else {
		cfg.Workspace.Root = expandPath(cfg.Workspace.Root)
	}
	if value := stringValue(raw.RepoURL); value != "" {
		cfg.Workspace.RepoURL = value
	}
	if value := stringValue(raw.BaseRef); value != "" {
		cfg.Workspace.BaseRef = value
	}
	return nil
}

func applyHooksConfig(cfg *domain.ServiceConfig, raw domain.WorkflowHookConfig) error {
	if value := stringValue(raw.AfterCreate); value != "" {
		cfg.Hooks.AfterCreate = value
	}
	if value := stringValue(raw.BeforeRun); value != "" {
		cfg.Hooks.BeforeRun = value
	}
	if value := stringValue(raw.AfterRun); value != "" {
		cfg.Hooks.AfterRun = value
	}
	if value := stringValue(raw.BeforeRemove); value != "" {
		cfg.Hooks.BeforeRemove = value
	}
	if value, ok := durationMillisValue(raw.TimeoutMillis); ok {
		cfg.Hooks.Timeout = value
	}
	return nil
}

func applyRepoConfig(cfg *domain.ServiceConfig, raw domain.WorkflowRepoConfig) error {
	if value := stringValue(raw.Backend); value != "" {
		cfg.Repo.Backend = repohost.NormalizeBackend(value)
	}
	if value := stringValue(raw.APIBaseURL); value != "" {
		cfg.Repo.APIBaseURL = resolveEnvToken(value)
	}
	if len(raw.PublishStates) > 0 {
		cfg.Repo.PublishStates = append([]string(nil), raw.PublishStates...)
	}
	if len(raw.MergeStates) > 0 {
		cfg.Repo.MergeStates = append([]string(nil), raw.MergeStates...)
	}
	if value := stringValue(raw.RemoteName); value != "" {
		cfg.Repo.RemoteName = value
	}
	if value := stringValue(raw.MergeMethod); value != "" {
		cfg.Repo.MergeMethod = strings.ToLower(value)
	}
	if value := stringValue(raw.BranchTemplate); value != "" {
		cfg.Repo.BranchTemplate = value
	}
	if value := stringValue(raw.PRTemplate); value != "" {
		cfg.Repo.PRTemplate = value
	}
	if value := stringValue(raw.APIToken); value != "" {
		cfg.Repo.APIToken = resolveEnvToken(value)
	}
	if value := stringValue(raw.WebhookSigningSecret); value != "" {
		cfg.Repo.WebhookSigningSecret = resolveEnvToken(value)
	}
	if raw.CodexPRReviewsEnabled != nil {
		cfg.Repo.CodexPRReviewsEnabled = *raw.CodexPRReviewsEnabled
	}
	if cfg.Repo.APIToken == "" {
		cfg.Repo.APIToken = currentRepoToken(cfg.Repo.Backend)
	}
	return nil
}

func applyAgentConfig(cfg *domain.ServiceConfig, raw domain.WorkflowAgentConfig) error {
	if value, ok := intValue(raw.MaxConcurrentAgents); ok && value > 0 {
		cfg.Agent.MaxConcurrentAgents = value
	}
	if value, ok := durationMillisValue(raw.MaxRetryBackoffMillis); ok && value > 0 {
		cfg.Agent.MaxRetryBackoff = value
	}
	if value, ok := intValue(raw.MaxTurns); ok && value > 0 {
		cfg.Agent.MaxTurns = value
	}
	if raw.CreateExecPlan != nil {
		cfg.Agent.CreateExecPlan = *raw.CreateExecPlan
	}
	if len(raw.MaxConcurrentAgentsByState) > 0 {
		cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
		for key, number := range raw.MaxConcurrentAgentsByState {
			if number <= 0 {
				continue
			}
			cfg.Agent.MaxConcurrentAgentsByState[StateKey(key)] = number
		}
	}
	return nil
}

func applyCodexConfig(cfg *domain.ServiceConfig, raw domain.WorkflowCodexConfig) error {
	if value := stringValue(raw.Command); value != "" {
		cfg.Codex.Command = value
	}
	if value := stringValue(raw.CLICommand); value != "" {
		cfg.Codex.CLICommand = value
	}
	if value := stringValue(raw.ApprovalPolicy); value != "" {
		cfg.Codex.ApprovalPolicy = value
	}
	if value := stringValue(raw.ThreadSandbox); value != "" {
		cfg.Codex.ThreadSandbox = value
	}
	if raw.TurnSandboxPolicy != nil {
		cfg.Codex.TurnSandboxPolicy = domain.SandboxPolicy{
			Type: stringValue(raw.TurnSandboxPolicy.Type),
		}
		if cfg.Codex.TurnSandboxPolicy.Type == "" {
			cfg.Codex.TurnSandboxPolicy.Type = stringValue(raw.TurnSandboxPolicy.Mode)
		}
	}
	if value, ok := durationMillisValue(raw.TurnTimeoutMillis); ok && value > 0 {
		cfg.Codex.TurnTimeout = value
	}
	if value, ok := durationMillisValue(raw.ReadTimeoutMillis); ok && value > 0 {
		cfg.Codex.ReadTimeout = value
	}
	if value, ok := durationMillisValue(raw.StallTimeoutMillis); ok {
		cfg.Codex.StallTimeout = value
	}
	return nil
}

func applyServerConfig(cfg *domain.ServiceConfig, raw domain.WorkflowServerConfig) error {
	if value, ok := intValue(raw.Port); ok {
		cfg.Server.Port = &value
	}
	if value, ok := intValue(raw.WebhookPort); ok {
		cfg.Server.WebhookPort = &value
	}
	if value := stringValue(raw.PublicURL); value != "" {
		cfg.Server.PublicURL = resolveEnvToken(value)
	}
	if value := stringValue(raw.WebhookPublicURL); value != "" {
		cfg.Server.WebhookPublicURL = resolveEnvToken(value)
	}
	if value := stringValue(raw.UIURL); value != "" {
		cfg.Server.UIURL = resolveEnvToken(value)
	}
	if value, ok := intValue(raw.LogBufferLines); ok && value > 0 {
		cfg.Server.LogBufferLines = value
	}
	return nil
}

func applySlackConfig(cfg *domain.ServiceConfig, raw domain.WorkflowSlackConfig) error {
	if value := stringValue(raw.BotToken); value != "" {
		cfg.Slack.BotToken = resolveEnvToken(value)
	}
	if value := stringValue(raw.AppToken); value != "" {
		cfg.Slack.AppToken = resolveEnvToken(value)
	}
	if value := stringValue(raw.ChannelID); value != "" {
		cfg.Slack.ChannelID = strings.TrimSpace(resolveEnvToken(value))
	}
	if value := stringValue(raw.SigningSecret); value != "" {
		cfg.Slack.SigningSecret = strings.TrimSpace(resolveEnvToken(value))
	}
	if (cfg.Slack.BotToken == "") != (cfg.Slack.ChannelID == "") {
		return fmt.Errorf("%w: slack.bot_token and slack.channel_id must both be set", ErrInvalidWorkflowConfig)
	}
	if (cfg.Slack.BotToken != "" || cfg.Slack.ChannelID != "") && strings.TrimSpace(cfg.Slack.AppToken) == "" {
		return fmt.Errorf("%w: slack.app_token must be set when Slack is enabled", ErrInvalidWorkflowConfig)
	}
	if strings.TrimSpace(cfg.Slack.AppToken) != "" && (cfg.Slack.BotToken == "" || cfg.Slack.ChannelID == "") {
		return fmt.Errorf("%w: slack.app_token requires slack.bot_token and slack.channel_id", ErrInvalidWorkflowConfig)
	}
	return nil
}

func validMergeMethod(value string) bool {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "merge", "squash", "rebase":
		return true
	default:
		return false
	}
}

func intPtr(value int) *int {
	return &value
}
