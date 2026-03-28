package config

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workflowfile"
)

const (
	defaultLinearBaseURL  = "https://api.linear.app/graphql"
	defaultLinearBackend  = LinearBackendHTTP
	defaultGitHubAPIURL   = "https://api.github.com"
	defaultBaseBranch     = "main"
	defaultPushAfterMerge = true
	defaultPollEvery      = 30 * time.Second
	defaultLeaseTTL       = 5 * time.Minute
	defaultMaxConcurrency = 10
	defaultMaxTurns       = 20
	defaultHookTimeout    = 60 * time.Second
	defaultRetryBackoff   = 5 * time.Minute
	defaultCodexCommand   = "codex app-server"
	defaultReadTimeout    = 5 * time.Second
	defaultTurnTimeout    = time.Hour
	defaultStallTimeout   = 5 * time.Minute
	// DefaultConfigPath is the default config file path for CLI execution.
	DefaultConfigPath = "colin.toml"
)

const (
	// LinearBackendHTTP uses the Linear GraphQL API over HTTP.
	LinearBackendHTTP = "http"
	// LinearBackendFake uses an in-memory fake Linear client.
	LinearBackendFake = "fake"
)

// Config is runtime configuration for the Linear worker.
type Config struct {
	LinearAPIToken          string
	LinearTeamID            string
	LinearBaseURL           string
	LinearBackend           string
	LinearProjectSlug       string
	GitHubAPIURL            string
	GitHubAppID             string
	GitHubAppInstallationID string
	GitHubAppPrivateKey     string
	GitHubAppPrivateKeyPath string
	BaseBranch              string
	PushAfterMerge          bool
	ProjectFilter           []string
	WorkPromptPath          string
	MergePromptPath         string
	WorkflowPath            string
	WorkflowPromptTemplate  string
	WorkspaceRoot           string
	ActiveStates            []string
	TerminalStates          []string
	Hooks                   HookConfig
	ColinHome               string
	WorkerID                string
	PollEvery               time.Duration
	LeaseTTL                time.Duration
	MaxConcurrency          int
	MaxTurns                int
	MaxRetryBackoff         time.Duration
	MaxConcurrencyByState   map[string]int
	Codex                   CodexConfig
	DryRun                  bool
	WorkflowStates          WorkflowStates
}

// HookConfig controls workspace lifecycle hooks.
type HookConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	Timeout      time.Duration
}

// CodexConfig controls Codex session launch and timeout behavior.
type CodexConfig struct {
	Command           string
	ApprovalPolicy    string
	ThreadSandbox     string
	TurnSandboxPolicy string
	ReadTimeout       time.Duration
	TurnTimeout       time.Duration
	StallTimeout      time.Duration
}

type fileConfig struct {
	LinearAPIToken          string         `toml:"linear_api_token"`
	LinearTeamID            string         `toml:"linear_team_id"`
	LinearBaseURL           string         `toml:"linear_base_url"`
	LinearBackend           string         `toml:"linear_backend"`
	GitHubAPIURL            string         `toml:"github_api_url"`
	GitHubAppID             string         `toml:"github_app_id"`
	GitHubAppInstallationID string         `toml:"github_app_installation_id"`
	GitHubAppPrivateKey     string         `toml:"github_app_private_key"`
	GitHubAppPrivateKeyPath string         `toml:"github_app_private_key_path"`
	BaseBranch              string         `toml:"base_branch"`
	PushAfterMerge          *bool          `toml:"push_after_merge"`
	ProjectFilter           string         `toml:"project_filter"`
	WorkPromptPath          string         `toml:"work_prompt_path"`
	MergePromptPath         string         `toml:"merge_prompt_path"`
	ColinHome               string         `toml:"colin_home"`
	WorkerID                string         `toml:"worker_id"`
	PollEvery               string         `toml:"poll_every"`
	LeaseTTL                string         `toml:"lease_ttl"`
	MaxConcurrency          *int           `toml:"max_concurrency"`
	DryRun                  *bool          `toml:"dry_run"`
	WorkflowStates          WorkflowStates `toml:"workflow_states"`
}

// WorkflowStates configures canonical workflow states to actual Linear state names.
type WorkflowStates struct {
	Todo       string `toml:"todo"`
	InProgress string `toml:"in_progress"`
	Refine     string `toml:"refine"`
	Review     string `toml:"review"`
	Merge      string `toml:"merge"`
	Merged     string `toml:"merged"`
	Done       string `toml:"done"`
}

// DefaultWorkflowStates returns canonical workflow state mapping defaults.
func DefaultWorkflowStates() WorkflowStates {
	return WorkflowStates{
		Todo:       workflow.StateTodo,
		InProgress: workflow.StateInProgress,
		Refine:     workflow.StateRefine,
		Review:     workflow.StateReview,
		Merge:      workflow.StateMerge,
		Merged:     workflow.StateMerged,
		Done:       workflow.StateDone,
	}
}

// WithDefaults fills blank configured values with canonical defaults.
func (w WorkflowStates) WithDefaults() WorkflowStates {
	defaults := DefaultWorkflowStates()
	if strings.TrimSpace(w.Todo) == "" {
		w.Todo = defaults.Todo
	}
	if strings.TrimSpace(w.InProgress) == "" {
		w.InProgress = defaults.InProgress
	}
	if strings.TrimSpace(w.Refine) == "" {
		w.Refine = defaults.Refine
	}
	if strings.TrimSpace(w.Review) == "" {
		w.Review = defaults.Review
	}
	if strings.TrimSpace(w.Merge) == "" {
		w.Merge = defaults.Merge
	}
	if strings.TrimSpace(w.Merged) == "" {
		w.Merged = defaults.Merged
	}
	if strings.TrimSpace(w.Done) == "" {
		w.Done = defaults.Done
	}
	return w
}

// Validate reports whether configured workflow states are complete and unique.
func (w WorkflowStates) Validate() error {
	return w.AsRuntimeStates().Validate()
}

// AsRuntimeStates returns workflow runtime state names with defaults applied.
func (w WorkflowStates) AsRuntimeStates() workflow.States {
	w = w.WithDefaults()
	return workflow.States{
		Todo:       strings.TrimSpace(w.Todo),
		InProgress: strings.TrimSpace(w.InProgress),
		Refine:     strings.TrimSpace(w.Refine),
		Review:     strings.TrimSpace(w.Review),
		Merge:      strings.TrimSpace(w.Merge),
		Merged:     strings.TrimSpace(w.Merged),
		Done:       strings.TrimSpace(w.Done),
	}
}

// Load reads configuration from colin.toml (or COLIN_CONFIG) and applies
// environment variable overrides.
func Load() (Config, error) {
	return LoadWithOptions(LoadOptions{
		ConfigPath: strings.TrimSpace(os.Getenv("COLIN_CONFIG")),
	})
}

// LoadFromPath reads configuration from the provided path and applies
// environment variable overrides. Empty path falls back to DefaultConfigPath.
func LoadFromPath(configPath string) (Config, error) {
	return LoadWithOptions(LoadOptions{ConfigPath: configPath})
}

// LoadOptions controls config source selection.
type LoadOptions struct {
	ConfigPath     string
	WorkflowPath   string
	SkipConfigFile bool
}

// LoadWithOptions reads configuration from workflow, file, and environment
// sources. Workflow front matter takes precedence over TOML because the
// repository-owned workflow contract is the preferred runtime configuration
// source; environment variables still override both.
func LoadWithOptions(opts LoadOptions) (Config, error) {
	cfg := Config{
		LinearBaseURL:  defaultLinearBaseURL,
		LinearBackend:  defaultLinearBackend,
		GitHubAPIURL:   defaultGitHubAPIURL,
		BaseBranch:     defaultBaseBranch,
		PushAfterMerge: defaultPushAfterMerge,
		ActiveStates:   defaultActiveStates(),
		TerminalStates: defaultTerminalStates(),
		Hooks: HookConfig{
			Timeout: defaultHookTimeout,
		},
		ColinHome:             defaultColinHome(),
		WorkerID:              defaultWorkerID(),
		PollEvery:             defaultPollEvery,
		LeaseTTL:              defaultLeaseTTL,
		MaxConcurrency:        defaultMaxConcurrency,
		MaxTurns:              defaultMaxTurns,
		MaxRetryBackoff:       defaultRetryBackoff,
		MaxConcurrencyByState: map[string]int{},
		Codex: CodexConfig{
			Command:      defaultCodexCommand,
			ReadTimeout:  defaultReadTimeout,
			TurnTimeout:  defaultTurnTimeout,
			StallTimeout: defaultStallTimeout,
		},
		WorkflowStates: DefaultWorkflowStates(),
	}

	configPath := strings.TrimSpace(opts.ConfigPath)
	if !opts.SkipConfigFile {
		if configPath == "" {
			configPath = DefaultConfigPath
		}
		if err := applyFileConfig(&cfg, configPath); err != nil {
			return Config{}, err
		}
	}
	if err := applyWorkflowConfig(&cfg, strings.TrimSpace(opts.WorkflowPath)); err != nil {
		return Config{}, err
	}
	if err := applyEnvOverrides(&cfg); err != nil {
		return Config{}, err
	}
	cfg.LinearBackend = normalizeLinearBackend(cfg.LinearBackend)
	cfg.WorkflowPath = resolvedWorkflowPath(strings.TrimSpace(opts.WorkflowPath))
	cfg.ActiveStates = normalizeStringList(cfg.ActiveStates)
	cfg.TerminalStates = normalizeStringList(cfg.TerminalStates)
	cfg.MaxConcurrencyByState = normalizeStateLimitMap(cfg.MaxConcurrencyByState)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// LoadFromEnv reads worker configuration only from environment variables.
func LoadFromEnv() (Config, error) {
	return LoadWithOptions(LoadOptions{SkipConfigFile: true})
}

func applyFileConfig(cfg *Config, path string) error {
	cleanPath := filepath.Clean(path)
	content, err := os.ReadFile(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config file %q: %w", cleanPath, err)
	}

	var parsed fileConfig
	if err := toml.Unmarshal(content, &parsed); err != nil {
		return fmt.Errorf("parse config file %q: %w", cleanPath, err)
	}

	if strings.TrimSpace(parsed.LinearAPIToken) != "" {
		cfg.LinearAPIToken = strings.TrimSpace(parsed.LinearAPIToken)
	}
	if strings.TrimSpace(parsed.LinearTeamID) != "" {
		cfg.LinearTeamID = strings.TrimSpace(parsed.LinearTeamID)
	}
	if strings.TrimSpace(parsed.LinearBaseURL) != "" {
		cfg.LinearBaseURL = strings.TrimSpace(parsed.LinearBaseURL)
	}
	if strings.TrimSpace(parsed.LinearBackend) != "" {
		cfg.LinearBackend = strings.TrimSpace(parsed.LinearBackend)
	}
	if strings.TrimSpace(parsed.GitHubAPIURL) != "" {
		cfg.GitHubAPIURL = strings.TrimSpace(parsed.GitHubAPIURL)
	}
	if strings.TrimSpace(parsed.GitHubAppID) != "" {
		cfg.GitHubAppID = strings.TrimSpace(parsed.GitHubAppID)
	}
	if strings.TrimSpace(parsed.GitHubAppInstallationID) != "" {
		cfg.GitHubAppInstallationID = strings.TrimSpace(parsed.GitHubAppInstallationID)
	}
	if strings.TrimSpace(parsed.GitHubAppPrivateKey) != "" {
		cfg.GitHubAppPrivateKey = strings.TrimSpace(parsed.GitHubAppPrivateKey)
	}
	if strings.TrimSpace(parsed.GitHubAppPrivateKeyPath) != "" {
		cfg.GitHubAppPrivateKeyPath = strings.TrimSpace(parsed.GitHubAppPrivateKeyPath)
	}
	if strings.TrimSpace(parsed.BaseBranch) != "" {
		cfg.BaseBranch = strings.TrimSpace(parsed.BaseBranch)
	}
	if parsed.PushAfterMerge != nil {
		cfg.PushAfterMerge = *parsed.PushAfterMerge
	}
	if strings.TrimSpace(parsed.ProjectFilter) != "" {
		cfg.ProjectFilter = parseCSVList(parsed.ProjectFilter)
	}
	if strings.TrimSpace(parsed.WorkPromptPath) != "" {
		cfg.WorkPromptPath = strings.TrimSpace(parsed.WorkPromptPath)
	}
	if strings.TrimSpace(parsed.MergePromptPath) != "" {
		cfg.MergePromptPath = strings.TrimSpace(parsed.MergePromptPath)
	}
	if strings.TrimSpace(parsed.ColinHome) != "" {
		cfg.ColinHome = strings.TrimSpace(parsed.ColinHome)
	}
	if strings.TrimSpace(parsed.WorkerID) != "" {
		cfg.WorkerID = strings.TrimSpace(parsed.WorkerID)
	}
	if strings.TrimSpace(parsed.PollEvery) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(parsed.PollEvery))
		if err != nil {
			return fmt.Errorf("parse poll_every in %q: %w", cleanPath, err)
		}
		cfg.PollEvery = d
	}
	if strings.TrimSpace(parsed.LeaseTTL) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(parsed.LeaseTTL))
		if err != nil {
			return fmt.Errorf("parse lease_ttl in %q: %w", cleanPath, err)
		}
		cfg.LeaseTTL = d
	}
	if parsed.DryRun != nil {
		cfg.DryRun = *parsed.DryRun
	}
	if parsed.MaxConcurrency != nil {
		cfg.MaxConcurrency = *parsed.MaxConcurrency
	}
	cfg.WorkflowStates = mergeWorkflowStateOverrides(cfg.WorkflowStates, parsed.WorkflowStates)

	return nil
}

func mergeWorkflowStateOverrides(base WorkflowStates, overrides WorkflowStates) WorkflowStates {
	base = base.WithDefaults()
	if strings.TrimSpace(overrides.Todo) != "" {
		base.Todo = strings.TrimSpace(overrides.Todo)
	}
	if strings.TrimSpace(overrides.InProgress) != "" {
		base.InProgress = strings.TrimSpace(overrides.InProgress)
	}
	if strings.TrimSpace(overrides.Refine) != "" {
		base.Refine = strings.TrimSpace(overrides.Refine)
	}
	if strings.TrimSpace(overrides.Review) != "" {
		base.Review = strings.TrimSpace(overrides.Review)
	}
	if strings.TrimSpace(overrides.Merge) != "" {
		base.Merge = strings.TrimSpace(overrides.Merge)
	}
	if strings.TrimSpace(overrides.Merged) != "" {
		base.Merged = strings.TrimSpace(overrides.Merged)
	}
	if strings.TrimSpace(overrides.Done) != "" {
		base.Done = strings.TrimSpace(overrides.Done)
	}
	return base
}

func applyEnvOverrides(cfg *Config) error {
	if v, ok := readString("LINEAR_API_TOKEN"); ok {
		cfg.LinearAPIToken = v
	}
	if v, ok := readString("LINEAR_TEAM_ID"); ok {
		cfg.LinearTeamID = v
	}
	if v, ok := readString("LINEAR_BASE_URL"); ok {
		cfg.LinearBaseURL = v
	}
	if v, ok := readString("LINEAR_PROJECT_SLUG"); ok {
		cfg.LinearProjectSlug = v
	}
	if v, ok := readString("COLIN_LINEAR_BACKEND"); ok {
		cfg.LinearBackend = v
	}
	if v, ok := readString("GITHUB_API_URL"); ok {
		cfg.GitHubAPIURL = v
	}
	if v, ok := readString("GITHUB_APP_ID"); ok {
		cfg.GitHubAppID = v
	}
	if v, ok := readString("GITHUB_APP_INSTALLATION_ID"); ok {
		cfg.GitHubAppInstallationID = v
	}
	if v, ok := readString("GITHUB_APP_PRIVATE_KEY"); ok {
		cfg.GitHubAppPrivateKey = v
	}
	if v, ok := readString("GITHUB_APP_PRIVATE_KEY_PATH"); ok {
		cfg.GitHubAppPrivateKeyPath = v
	}
	if v, ok := readString("COLIN_BASE_BRANCH"); ok {
		cfg.BaseBranch = v
	}
	if raw, ok := readString("COLIN_PUSH_AFTER_MERGE"); ok {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("parse COLIN_PUSH_AFTER_MERGE: %w", err)
		}
		cfg.PushAfterMerge = parsed
	}
	if raw, ok := readString("COLIN_PROJECT_FILTER"); ok {
		cfg.ProjectFilter = parseCSVList(raw)
	}
	if v, ok := readString("COLIN_WORK_PROMPT_PATH"); ok {
		cfg.WorkPromptPath = v
	}
	if v, ok := readString("COLIN_MERGE_PROMPT_PATH"); ok {
		cfg.MergePromptPath = v
	}
	if v, ok := readString("COLIN_WORKFLOW_PATH"); ok {
		cfg.WorkflowPath = v
	}
	if v, ok := readString("COLIN_WORKSPACE_ROOT"); ok {
		cfg.WorkspaceRoot = v
	}
	if v, ok := readString("COLIN_ACTIVE_STATES"); ok {
		cfg.ActiveStates = parseCSVList(v)
	}
	if v, ok := readString("COLIN_TERMINAL_STATES"); ok {
		cfg.TerminalStates = parseCSVList(v)
	}
	if v, ok := readString("COLIN_HOME"); ok {
		cfg.ColinHome = v
	}
	if v, ok := readString("COLIN_WORKER_ID"); ok {
		cfg.WorkerID = v
	}

	if raw, ok := readString("COLIN_POLL_EVERY"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse COLIN_POLL_EVERY: %w", err)
		}
		cfg.PollEvery = d
	}
	if raw, ok := readString("COLIN_LEASE_TTL"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse COLIN_LEASE_TTL: %w", err)
		}
		cfg.LeaseTTL = d
	}
	if raw, ok := readString("COLIN_DRY_RUN"); ok {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("parse COLIN_DRY_RUN: %w", err)
		}
		cfg.DryRun = parsed
	}
	if raw, ok := readString("COLIN_MAX_CONCURRENCY"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("parse COLIN_MAX_CONCURRENCY: %w", err)
		}
		cfg.MaxConcurrency = parsed
	}
	if raw, ok := readString("COLIN_MAX_TURNS"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("parse COLIN_MAX_TURNS: %w", err)
		}
		cfg.MaxTurns = parsed
	}
	if raw, ok := readString("COLIN_MAX_RETRY_BACKOFF"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse COLIN_MAX_RETRY_BACKOFF: %w", err)
		}
		cfg.MaxRetryBackoff = d
	}
	if raw, ok := readString("COLIN_HOOK_TIMEOUT"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse COLIN_HOOK_TIMEOUT: %w", err)
		}
		cfg.Hooks.Timeout = d
	}
	if v, ok := readString("COLIN_CODEX_COMMAND"); ok {
		cfg.Codex.Command = v
	}

	return nil
}

// Validate reports whether the configuration contains all required fields and
// valid runtime values.
func (c Config) Validate() error {
	switch c.LinearBackend {
	case LinearBackendHTTP:
		if c.LinearAPIToken == "" {
			return errors.New("LINEAR_API_TOKEN is required")
		}
		if c.LinearTeamID == "" {
			return errors.New("LINEAR_TEAM_ID is required")
		}
		if strings.TrimSpace(c.GitHubAppID) == "" {
			return errors.New("GITHUB_APP_ID is required")
		}
		if strings.TrimSpace(c.GitHubAppInstallationID) == "" {
			return errors.New("GITHUB_APP_INSTALLATION_ID is required")
		}
		if strings.TrimSpace(c.GitHubAppPrivateKey) == "" && strings.TrimSpace(c.GitHubAppPrivateKeyPath) == "" {
			return errors.New("either GITHUB_APP_PRIVATE_KEY or GITHUB_APP_PRIVATE_KEY_PATH is required")
		}
	case LinearBackendFake:
		// Fake backend does not require real Linear credentials.
	default:
		return fmt.Errorf("COLIN_LINEAR_BACKEND must be one of %q or %q, got %q", LinearBackendHTTP, LinearBackendFake, c.LinearBackend)
	}
	if c.PollEvery <= 0 {
		return fmt.Errorf("COLIN_POLL_EVERY must be > 0, got %s", c.PollEvery)
	}
	if c.LeaseTTL <= 0 {
		return fmt.Errorf("COLIN_LEASE_TTL must be > 0, got %s", c.LeaseTTL)
	}
	if c.MaxConcurrency <= 0 {
		return fmt.Errorf("COLIN_MAX_CONCURRENCY must be > 0, got %d", c.MaxConcurrency)
	}
	if c.MaxTurns <= 0 {
		return fmt.Errorf("agent.max_turns must be > 0, got %d", c.MaxTurns)
	}
	if c.MaxRetryBackoff <= 0 {
		return fmt.Errorf("agent.max_retry_backoff_ms must be > 0, got %s", c.MaxRetryBackoff)
	}
	if strings.TrimSpace(c.BaseBranch) == "" {
		return errors.New("COLIN_BASE_BRANCH must not be empty")
	}
	if strings.TrimSpace(c.GitHubAPIURL) == "" {
		return errors.New("GITHUB_API_URL must not be empty")
	}
	if strings.TrimSpace(c.ColinHome) == "" {
		return errors.New("COLIN_HOME must not be empty")
	}
	if strings.TrimSpace(c.ResolvedWorkspaceRoot()) == "" {
		return errors.New("COLIN_WORKSPACE_ROOT must not be empty")
	}
	if len(c.ActiveStates) == 0 {
		return errors.New("tracker.active_states must not be empty")
	}
	if len(c.TerminalStates) == 0 {
		return errors.New("tracker.terminal_states must not be empty")
	}
	if c.WorkerID == "" {
		return errors.New("COLIN_WORKER_ID must not be empty")
	}
	if strings.TrimSpace(c.Codex.Command) == "" {
		return errors.New("codex.command must not be empty")
	}
	if c.Hooks.Timeout <= 0 {
		return fmt.Errorf("hooks.timeout_ms must be > 0, got %s", c.Hooks.Timeout)
	}
	if c.Codex.ReadTimeout <= 0 {
		return fmt.Errorf("codex.read_timeout_ms must be > 0, got %s", c.Codex.ReadTimeout)
	}
	if c.Codex.TurnTimeout <= 0 {
		return fmt.Errorf("codex.turn_timeout_ms must be > 0, got %s", c.Codex.TurnTimeout)
	}
	if err := c.WorkflowStates.Validate(); err != nil {
		return fmt.Errorf("workflow_states: %w", err)
	}
	return nil
}

// ResolvedGitHubAppPrivateKey returns the configured GitHub App private key.
// Inline key takes precedence over file path.
func (c Config) ResolvedGitHubAppPrivateKey() (string, error) {
	if v := strings.TrimSpace(c.GitHubAppPrivateKey); v != "" {
		return v, nil
	}

	path := strings.TrimSpace(c.GitHubAppPrivateKeyPath)
	if path == "" {
		return "", errors.New("github app private key is not configured")
	}

	content, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read GitHub App private key file %q: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return "", fmt.Errorf("GitHub App private key file %q is empty", path)
	}
	return trimmed, nil
}

func normalizeLinearBackend(raw string) string {
	backend := strings.ToLower(strings.TrimSpace(raw))
	if backend == "" {
		return defaultLinearBackend
	}
	return backend
}

func readString(key string) (string, bool) {
	v, ok := readRaw(key)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

func readRaw(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	return v, ok
}

func parseCSVList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	reader := csv.NewReader(strings.NewReader(raw))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil && !errors.Is(err, io.EOF) {
		records = [][]string{strings.Split(raw, ",")}
	}

	tokenCount := 0
	for _, record := range records {
		tokenCount += len(record)
	}
	out := make([]string, 0, tokenCount)
	seen := make(map[string]struct{}, tokenCount)
	for _, record := range records {
		for _, token := range record {
			trimmed := strings.TrimSpace(token)
			if trimmed == "" {
				continue
			}
			normalized := strings.ToLower(trimmed)
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeStringList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeStateLimitMap(values map[string]int) map[string]int {
	if len(values) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(values))
	for state, limit := range values {
		trimmed := strings.TrimSpace(state)
		if trimmed == "" || limit <= 0 {
			continue
		}
		out[strings.ToLower(trimmed)] = limit
	}
	return out
}

func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func defaultColinHome() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".colin"
	}
	return filepath.Join(home, ".colin")
}

func defaultActiveStates() []string {
	return []string{workflow.StateTodo, workflow.StateInProgress}
}

func defaultTerminalStates() []string {
	return []string{"Closed", "Cancelled", "Canceled", "Duplicate", workflow.StateDone}
}

// ResolvedWorkspaceRoot returns the effective workspace root path.
func (c Config) ResolvedWorkspaceRoot() string {
	if trimmed := strings.TrimSpace(c.WorkspaceRoot); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(c.ColinHome); trimmed != "" {
		return filepath.Join(trimmed, "worktrees")
	}
	return filepath.Join(defaultColinHome(), "worktrees")
}

func applyWorkflowConfig(cfg *Config, requestedPath string) error {
	workflowPath := resolvedWorkflowPath(requestedPath)
	if workflowPath == "" {
		return nil
	}

	def, err := workflowfile.Load(workflowPath)
	if err != nil {
		var wfErr *workflowfile.Error
		if errors.As(err, &wfErr) && wfErr.Kind == workflowfile.KindMissingFile && strings.TrimSpace(requestedPath) == "" {
			return nil
		}
		return err
	}

	cfg.WorkflowPath = def.Path
	cfg.WorkflowPromptTemplate = def.PromptTemplate

	if tracker, ok := nestedMap(def.Config, "tracker"); ok {
		if raw, ok := resolvedString(tracker["endpoint"]); ok {
			cfg.LinearBaseURL = raw
		}
		if raw, ok := resolvedString(tracker["api_key"]); ok {
			cfg.LinearAPIToken = raw
		}
		if raw, ok := resolvedString(tracker["kind"]); ok {
			switch strings.ToLower(strings.TrimSpace(raw)) {
			case "linear":
				cfg.LinearBackend = LinearBackendHTTP
			case "fake":
				cfg.LinearBackend = LinearBackendFake
			}
		}
		if raw, ok := resolvedString(tracker["project_slug"]); ok {
			cfg.LinearProjectSlug = raw
		}
		if values, ok := stringListValue(tracker["active_states"]); ok {
			cfg.ActiveStates = values
		}
		if values, ok := stringListValue(tracker["terminal_states"]); ok {
			cfg.TerminalStates = values
		}
	}

	if polling, ok := nestedMap(def.Config, "polling"); ok {
		if interval, ok := durationFromMilliseconds(polling["interval_ms"]); ok {
			cfg.PollEvery = interval
		}
	}

	if workspace, ok := nestedMap(def.Config, "workspace"); ok {
		if raw, ok := resolvedPath(workspace["root"]); ok {
			cfg.WorkspaceRoot = raw
		}
	}

	if hooks, ok := nestedMap(def.Config, "hooks"); ok {
		if raw, ok := resolvedString(hooks["after_create"]); ok {
			cfg.Hooks.AfterCreate = raw
		}
		if raw, ok := resolvedString(hooks["before_run"]); ok {
			cfg.Hooks.BeforeRun = raw
		}
		if raw, ok := resolvedString(hooks["after_run"]); ok {
			cfg.Hooks.AfterRun = raw
		}
		if raw, ok := resolvedString(hooks["before_remove"]); ok {
			cfg.Hooks.BeforeRemove = raw
		}
		if value, ok := durationFromMilliseconds(hooks["timeout_ms"]); ok {
			cfg.Hooks.Timeout = value
		}
	}

	if agent, ok := nestedMap(def.Config, "agent"); ok {
		if value, ok := positiveInt(agent["max_concurrent_agents"]); ok {
			cfg.MaxConcurrency = value
		}
		if value, ok := positiveInt(agent["max_turns"]); ok {
			cfg.MaxTurns = value
		}
		if value, ok := durationFromMilliseconds(agent["max_retry_backoff_ms"]); ok {
			cfg.MaxRetryBackoff = value
		}
		if value, ok := stateLimitMapValue(agent["max_concurrent_agents_by_state"]); ok {
			cfg.MaxConcurrencyByState = value
		}
	}

	if codexMap, ok := nestedMap(def.Config, "codex"); ok {
		if raw, ok := resolvedString(codexMap["command"]); ok {
			cfg.Codex.Command = raw
		}
		if raw, ok := resolvedString(codexMap["approval_policy"]); ok {
			cfg.Codex.ApprovalPolicy = raw
		}
		if raw, ok := resolvedString(codexMap["thread_sandbox"]); ok {
			cfg.Codex.ThreadSandbox = raw
		}
		if raw, ok := resolvedString(codexMap["turn_sandbox_policy"]); ok {
			cfg.Codex.TurnSandboxPolicy = raw
		}
		if value, ok := durationFromMilliseconds(codexMap["read_timeout_ms"]); ok {
			cfg.Codex.ReadTimeout = value
		}
		if value, ok := durationFromMilliseconds(codexMap["turn_timeout_ms"]); ok {
			cfg.Codex.TurnTimeout = value
		}
		if value, ok := durationFromMilliseconds(codexMap["stall_timeout_ms"]); ok {
			cfg.Codex.StallTimeout = value
		}
	}

	if colin, ok := nestedMap(def.Config, "colin"); ok {
		if raw, ok := resolvedString(colin["team_id"]); ok {
			cfg.LinearTeamID = raw
		}
		if raw, ok := resolvedString(colin["linear_backend"]); ok {
			cfg.LinearBackend = raw
		}
		if raw, ok := resolvedString(colin["github_api_url"]); ok {
			cfg.GitHubAPIURL = raw
		}
		if raw, ok := resolvedString(colin["github_app_id"]); ok {
			cfg.GitHubAppID = raw
		}
		if raw, ok := resolvedString(colin["github_app_installation_id"]); ok {
			cfg.GitHubAppInstallationID = raw
		}
		if raw, ok := resolvedString(colin["github_app_private_key"]); ok {
			cfg.GitHubAppPrivateKey = raw
		}
		if raw, ok := resolvedPath(colin["github_app_private_key_path"]); ok {
			cfg.GitHubAppPrivateKeyPath = raw
		}
		if raw, ok := resolvedString(colin["base_branch"]); ok {
			cfg.BaseBranch = raw
		}
		if raw, ok := resolvedString(colin["work_prompt_path"]); ok {
			cfg.WorkPromptPath = raw
		}
		if raw, ok := resolvedString(colin["merge_prompt_path"]); ok {
			cfg.MergePromptPath = raw
		}
		if raw, ok := resolvedPath(colin["colin_home"]); ok {
			cfg.ColinHome = raw
		}
		if raw, ok := resolvedPath(colin["workspace_root"]); ok {
			cfg.WorkspaceRoot = raw
		}
		if raw, ok := resolvedString(colin["worker_id"]); ok {
			cfg.WorkerID = raw
		}
		if raw, ok := boolValue(colin["push_after_merge"]); ok {
			cfg.PushAfterMerge = raw
		}
		if raw, ok := boolValue(colin["dry_run"]); ok {
			cfg.DryRun = raw
		}
		if raw, ok := durationValue(colin["lease_ttl"]); ok {
			cfg.LeaseTTL = raw
		}
		if raw, ok := projectFilterValue(colin["project_filter"]); ok {
			cfg.ProjectFilter = raw
		}
		if statesMap, ok := nestedMap(colin, "workflow_states"); ok {
			cfg.WorkflowStates = mergeWorkflowStateOverrides(cfg.WorkflowStates, WorkflowStates{
				Todo:       resolvedStringOrEmpty(statesMap["todo"]),
				InProgress: resolvedStringOrEmpty(statesMap["in_progress"]),
				Refine:     resolvedStringOrEmpty(statesMap["refine"]),
				Review:     resolvedStringOrEmpty(statesMap["review"]),
				Merge:      resolvedStringOrEmpty(statesMap["merge"]),
				Merged:     resolvedStringOrEmpty(statesMap["merged"]),
				Done:       resolvedStringOrEmpty(statesMap["done"]),
			})
		}
	}

	return nil
}

func resolvedWorkflowPath(requestedPath string) string {
	if trimmed := strings.TrimSpace(requestedPath); trimmed != "" {
		return trimmed
	}
	if raw, ok := readString("COLIN_WORKFLOW_PATH"); ok {
		return raw
	}
	return workflowfile.DefaultPath
}

func nestedMap(values map[string]any, key string) (map[string]any, bool) {
	raw, ok := values[key]
	if !ok {
		return nil, false
	}
	mapped, ok := raw.(map[string]any)
	return mapped, ok
}

func resolvedString(raw any) (string, bool) {
	switch typed := raw.(type) {
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return "", false
		}
		if strings.HasPrefix(value, "$") && len(value) > 1 {
			return readString(strings.TrimPrefix(value, "$"))
		}
		return value, true
	default:
		return "", false
	}
}

func resolvedStringOrEmpty(raw any) string {
	value, _ := resolvedString(raw)
	return value
}

func resolvedPath(raw any) (string, bool) {
	value, ok := resolvedString(raw)
	if !ok {
		return "", false
	}
	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			if value == "~" {
				value = home
			} else if strings.HasPrefix(value, "~/") {
				value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
			}
		}
	}
	return filepath.Clean(value), true
}

func positiveInt(raw any) (int, bool) {
	switch typed := raw.(type) {
	case int:
		return typed, typed > 0
	case int64:
		return int(typed), typed > 0
	case float64:
		value := int(typed)
		return value, value > 0
	case string:
		value, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return value, value > 0
	default:
		return 0, false
	}
}

func durationFromMilliseconds(raw any) (time.Duration, bool) {
	value, ok := positiveInt(raw)
	if !ok {
		return 0, false
	}
	return time.Duration(value) * time.Millisecond, true
}

func boolValue(raw any) (bool, bool) {
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		value, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err != nil {
			return false, false
		}
		return value, true
	default:
		return false, false
	}
}

func durationValue(raw any) (time.Duration, bool) {
	switch typed := raw.(type) {
	case string:
		value, err := time.ParseDuration(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return value, true
	case int:
		return time.Duration(typed) * time.Millisecond, true
	case int64:
		return time.Duration(typed) * time.Millisecond, true
	case float64:
		return time.Duration(int(typed)) * time.Millisecond, true
	default:
		return 0, false
	}
}

func projectFilterValue(raw any) ([]string, bool) {
	switch typed := raw.(type) {
	case string:
		return parseCSVList(typed), true
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := resolvedString(item); ok {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			return nil, false
		}
		return parseCSVList(strings.Join(values, ",")), true
	default:
		return nil, false
	}
}

func stringListValue(raw any) ([]string, bool) {
	switch typed := raw.(type) {
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := resolvedString(item); ok {
				values = append(values, value)
			}
		}
		values = normalizeStringList(values)
		return values, len(values) > 0
	case string:
		values := normalizeStringList(parseCSVList(typed))
		return values, len(values) > 0
	default:
		return nil, false
	}
}

func stateLimitMapValue(raw any) (map[string]int, bool) {
	typed, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]int, len(typed))
	for key, value := range typed {
		limit, ok := positiveInt(value)
		if !ok {
			continue
		}
		out[key] = limit
	}
	out = normalizeStateLimitMap(out)
	return out, len(out) > 0
}
