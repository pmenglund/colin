package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

var (
	ErrUnsupportedTrackerKind  = errors.New("unsupported_tracker_kind")
	ErrMissingTrackerAPIKey    = errors.New("missing_tracker_api_key")
	ErrMissingTrackerProject   = errors.New("missing_tracker_project_slug")
	ErrMissingCodexCommand     = errors.New("missing_codex_command")
	ErrMissingWorkflowPath     = errors.New("missing_workflow_file")
	ErrInvalidWorkflowConfig   = errors.New("invalid_workflow_config")
	ErrInvalidWorkspaceGitConf = errors.New("invalid_workspace_git_config")
)

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
		Hooks: domain.HookConfig{
			Timeout: 60 * time.Second,
		},
		Agent: domain.AgentConfig{
			MaxConcurrentAgents:        10,
			MaxRetryBackoff:            5 * time.Minute,
			MaxConcurrentAgentsByState: map[string]int{},
			MaxTurns:                   20,
		},
		Codex: domain.CodexConfig{
			Command:        "codex app-server",
			TurnTimeout:    1 * time.Hour,
			ReadTimeout:    5 * time.Second,
			StallTimeout:   5 * time.Minute,
			ApprovalPolicy: "never",
			ThreadSandbox:  "danger-full-access",
			TurnSandboxPolicy: map[string]any{
				"mode": "danger-full-access",
			},
		},
	}

	if err := applyTrackerConfig(&cfg, readMap(def.Config, "tracker")); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyPollingConfig(&cfg, readMap(def.Config, "polling")); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyWorkspaceConfig(&cfg, readMap(def.Config, "workspace")); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyHooksConfig(&cfg, readMap(def.Config, "hooks")); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyAgentConfig(&cfg, readMap(def.Config, "agent")); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyCodexConfig(&cfg, readMap(def.Config, "codex")); err != nil {
		return domain.ServiceConfig{}, err
	}
	if err := applyServerConfig(&cfg, readMap(def.Config, "server")); err != nil {
		return domain.ServiceConfig{}, err
	}

	normalizeStateList(cfg.Tracker.ActiveStates)
	normalizeStateList(cfg.Tracker.TerminalStates)

	if cfg.Hooks.Timeout <= 0 {
		cfg.Hooks.Timeout = 60 * time.Second
	}

	if cfg.Workspace.RepoURL != "" || cfg.Workspace.BaseRef != "" {
		if cfg.Workspace.RepoURL == "" || cfg.Workspace.BaseRef == "" {
			return domain.ServiceConfig{}, ErrInvalidWorkspaceGitConf
		}
	}

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
	if cfg.Tracker.ProjectSlug == "" {
		return ErrMissingTrackerProject
	}
	if strings.TrimSpace(cfg.Codex.Command) == "" {
		return ErrMissingCodexCommand
	}
	return nil
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

func readMap(root map[string]any, key string) map[string]any {
	if root == nil {
		return nil
	}
	value, ok := root[key]
	if !ok {
		return nil
	}
	out, ok := value.(map[string]any)
	if ok {
		return out
	}
	return nil
}

func applyTrackerConfig(cfg *domain.ServiceConfig, raw map[string]any) error {
	if raw == nil {
		cfg.Tracker.APIKey = strings.TrimSpace(os.Getenv("LINEAR_API_KEY"))
		return nil
	}
	if value, ok := readString(raw, "kind"); ok {
		cfg.Tracker.Kind = value
	}
	if value, ok := readString(raw, "endpoint"); ok {
		cfg.Tracker.Endpoint = value
	}
	if value, ok := readString(raw, "api_key"); ok {
		cfg.Tracker.APIKey = resolveEnvToken(value)
	}
	if value, ok := readString(raw, "project_slug"); ok {
		cfg.Tracker.ProjectSlug = value
	}
	if value, ok := readStringSlice(raw, "active_states"); ok && len(value) > 0 {
		cfg.Tracker.ActiveStates = value
	}
	if value, ok := readStringSlice(raw, "terminal_states"); ok && len(value) > 0 {
		cfg.Tracker.TerminalStates = value
	}
	if cfg.Tracker.APIKey == "" {
		cfg.Tracker.APIKey = strings.TrimSpace(os.Getenv("LINEAR_API_KEY"))
	}
	return nil
}

func applyPollingConfig(cfg *domain.ServiceConfig, raw map[string]any) error {
	if raw == nil {
		return nil
	}
	if value, ok := readDurationMillis(raw, "interval_ms"); ok && value > 0 {
		cfg.Polling.Interval = value
	}
	return nil
}

func applyWorkspaceConfig(cfg *domain.ServiceConfig, raw map[string]any) error {
	if raw == nil {
		cfg.Workspace.Root = expandPath(cfg.Workspace.Root)
		return nil
	}
	if value, ok := readString(raw, "root"); ok {
		cfg.Workspace.Root = expandPath(resolveEnvToken(value))
	} else {
		cfg.Workspace.Root = expandPath(cfg.Workspace.Root)
	}
	if value, ok := readString(raw, "repo_url"); ok {
		cfg.Workspace.RepoURL = strings.TrimSpace(value)
	}
	if value, ok := readString(raw, "base_ref"); ok {
		cfg.Workspace.BaseRef = strings.TrimSpace(value)
	}
	return nil
}

func applyHooksConfig(cfg *domain.ServiceConfig, raw map[string]any) error {
	if raw == nil {
		return nil
	}
	if value, ok := readString(raw, "after_create"); ok {
		cfg.Hooks.AfterCreate = value
	}
	if value, ok := readString(raw, "before_run"); ok {
		cfg.Hooks.BeforeRun = value
	}
	if value, ok := readString(raw, "after_run"); ok {
		cfg.Hooks.AfterRun = value
	}
	if value, ok := readString(raw, "before_remove"); ok {
		cfg.Hooks.BeforeRemove = value
	}
	if value, ok := readDurationMillis(raw, "timeout_ms"); ok {
		cfg.Hooks.Timeout = value
	}
	return nil
}

func applyAgentConfig(cfg *domain.ServiceConfig, raw map[string]any) error {
	if raw == nil {
		return nil
	}
	if value, ok := readInt(raw, "max_concurrent_agents"); ok && value > 0 {
		cfg.Agent.MaxConcurrentAgents = value
	}
	if value, ok := readDurationMillis(raw, "max_retry_backoff_ms"); ok && value > 0 {
		cfg.Agent.MaxRetryBackoff = value
	}
	if value, ok := readInt(raw, "max_turns"); ok && value > 0 {
		cfg.Agent.MaxTurns = value
	}
	if rawMap, ok := raw["max_concurrent_agents_by_state"].(map[string]any); ok {
		cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
		for key, value := range rawMap {
			number, ok := toInt(value)
			if !ok || number <= 0 {
				continue
			}
			cfg.Agent.MaxConcurrentAgentsByState[StateKey(key)] = number
		}
	}
	return nil
}

func applyCodexConfig(cfg *domain.ServiceConfig, raw map[string]any) error {
	if raw == nil {
		return nil
	}
	if value, ok := readString(raw, "command"); ok {
		cfg.Codex.Command = value
	}
	if value, ok := readString(raw, "approval_policy"); ok {
		cfg.Codex.ApprovalPolicy = value
	}
	if value, ok := readString(raw, "thread_sandbox"); ok {
		cfg.Codex.ThreadSandbox = value
	}
	if value, ok := raw["turn_sandbox_policy"].(map[string]any); ok {
		cfg.Codex.TurnSandboxPolicy = value
	}
	if value, ok := readDurationMillis(raw, "turn_timeout_ms"); ok && value > 0 {
		cfg.Codex.TurnTimeout = value
	}
	if value, ok := readDurationMillis(raw, "read_timeout_ms"); ok && value > 0 {
		cfg.Codex.ReadTimeout = value
	}
	if value, ok := readDurationMillis(raw, "stall_timeout_ms"); ok {
		cfg.Codex.StallTimeout = value
	}
	return nil
}

func applyServerConfig(cfg *domain.ServiceConfig, raw map[string]any) error {
	if raw == nil {
		return nil
	}
	value, exists := raw["port"]
	if !exists || value == nil {
		return nil
	}
	port, ok := toExactInt(value)
	if !ok || port < 0 || port > 65535 {
		return ErrInvalidWorkflowConfig
	}
	cfg.Server.Port = &port
	return nil
}
