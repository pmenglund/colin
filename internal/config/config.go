package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pmenglund/colin/internal/workflow"
)

const (
	defaultLinearBaseURL  = "https://api.linear.app/graphql"
	defaultLinearBackend  = LinearBackendHTTP
	defaultPollEvery      = 30 * time.Second
	defaultLeaseTTL       = 5 * time.Minute
	defaultMaxConcurrency = 8
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
	LinearAPIToken string
	LinearTeamID   string
	LinearBaseURL  string
	LinearBackend  string
	WorkPromptPath string
	ColinHome      string
	WorkerID       string
	PollEvery      time.Duration
	LeaseTTL       time.Duration
	MaxConcurrency int
	DryRun         bool
	WorkflowStates WorkflowStates
}

type fileConfig struct {
	LinearAPIToken string         `toml:"linear_api_token"`
	LinearTeamID   string         `toml:"linear_team_id"`
	LinearBaseURL  string         `toml:"linear_base_url"`
	LinearBackend  string         `toml:"linear_backend"`
	WorkPromptPath string         `toml:"work_prompt_path"`
	ColinHome      string         `toml:"colin_home"`
	WorkerID       string         `toml:"worker_id"`
	PollEvery      string         `toml:"poll_every"`
	LeaseTTL       string         `toml:"lease_ttl"`
	MaxConcurrency *int           `toml:"max_concurrency"`
	DryRun         *bool          `toml:"dry_run"`
	WorkflowStates WorkflowStates `toml:"workflow_states"`
}

// WorkflowStates configures canonical workflow states to actual Linear state names.
type WorkflowStates struct {
	Todo       string `toml:"todo"`
	InProgress string `toml:"in_progress"`
	Refine     string `toml:"refine"`
	Review     string `toml:"review"`
	Merge      string `toml:"merge"`
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
		Done:       strings.TrimSpace(w.Done),
	}
}

// Load reads configuration from colin.toml (or COLIN_CONFIG) and applies
// environment variable overrides.
func Load() (Config, error) {
	configPath := strings.TrimSpace(os.Getenv("COLIN_CONFIG"))
	return LoadFromPath(configPath)
}

// LoadFromPath reads configuration from the provided path and applies
// environment variable overrides. Empty path falls back to DefaultConfigPath.
func LoadFromPath(configPath string) (Config, error) {
	cfg := Config{
		LinearBaseURL:  defaultLinearBaseURL,
		LinearBackend:  defaultLinearBackend,
		ColinHome:      defaultColinHome(),
		WorkerID:       defaultWorkerID(),
		PollEvery:      defaultPollEvery,
		LeaseTTL:       defaultLeaseTTL,
		MaxConcurrency: defaultMaxConcurrency,
		WorkflowStates: DefaultWorkflowStates(),
	}

	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		configPath = DefaultConfigPath
	}

	if err := applyFileConfig(&cfg, configPath); err != nil {
		return Config{}, err
	}
	if err := applyEnvOverrides(&cfg); err != nil {
		return Config{}, err
	}
	cfg.LinearBackend = normalizeLinearBackend(cfg.LinearBackend)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// LoadFromEnv reads worker configuration only from environment variables.
func LoadFromEnv() (Config, error) {
	cfg := Config{
		LinearBaseURL:  defaultLinearBaseURL,
		LinearBackend:  defaultLinearBackend,
		ColinHome:      defaultColinHome(),
		WorkerID:       defaultWorkerID(),
		PollEvery:      defaultPollEvery,
		LeaseTTL:       defaultLeaseTTL,
		MaxConcurrency: defaultMaxConcurrency,
		WorkflowStates: DefaultWorkflowStates(),
	}

	if err := applyEnvOverrides(&cfg); err != nil {
		return Config{}, err
	}
	cfg.LinearBackend = normalizeLinearBackend(cfg.LinearBackend)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
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
	if strings.TrimSpace(parsed.WorkPromptPath) != "" {
		cfg.WorkPromptPath = strings.TrimSpace(parsed.WorkPromptPath)
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
	if v, ok := readString("COLIN_LINEAR_BACKEND"); ok {
		cfg.LinearBackend = v
	}
	if v, ok := readString("COLIN_WORK_PROMPT_PATH"); ok {
		cfg.WorkPromptPath = v
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
	if strings.TrimSpace(c.ColinHome) == "" {
		return errors.New("COLIN_HOME must not be empty")
	}
	if c.WorkerID == "" {
		return errors.New("COLIN_WORKER_ID must not be empty")
	}
	if err := c.WorkflowStates.Validate(); err != nil {
		return fmt.Errorf("workflow_states: %w", err)
	}
	return nil
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
