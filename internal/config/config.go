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
)

const (
	defaultLinearBaseURL = "https://api.linear.app/graphql"
	defaultPollEvery     = 30 * time.Second
	defaultLeaseTTL      = 5 * time.Minute
	// DefaultConfigPath is the default config file path for CLI execution.
	DefaultConfigPath = "colin.toml"
)

// Config is runtime configuration for the Linear worker.
type Config struct {
	LinearAPIToken string
	LinearTeamID   string
	LinearBaseURL  string
	WorkerID       string
	PollEvery      time.Duration
	LeaseTTL       time.Duration
	DryRun         bool
}

type fileConfig struct {
	LinearAPIToken string `toml:"linear_api_token"`
	LinearTeamID   string `toml:"linear_team_id"`
	LinearBaseURL  string `toml:"linear_base_url"`
	WorkerID       string `toml:"worker_id"`
	PollEvery      string `toml:"poll_every"`
	LeaseTTL       string `toml:"lease_ttl"`
	DryRun         *bool  `toml:"dry_run"`
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
		LinearBaseURL: defaultLinearBaseURL,
		WorkerID:      defaultWorkerID(),
		PollEvery:     defaultPollEvery,
		LeaseTTL:      defaultLeaseTTL,
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

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// LoadFromEnv reads worker configuration only from environment variables.
func LoadFromEnv() (Config, error) {
	cfg := Config{
		LinearBaseURL: defaultLinearBaseURL,
		WorkerID:      defaultWorkerID(),
		PollEvery:     defaultPollEvery,
		LeaseTTL:      defaultLeaseTTL,
	}

	if err := applyEnvOverrides(&cfg); err != nil {
		return Config{}, err
	}
	if err := validate(cfg); err != nil {
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

	return nil
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

	return nil
}

func validate(cfg Config) error {
	if cfg.LinearAPIToken == "" {
		return errors.New("LINEAR_API_TOKEN is required")
	}
	if cfg.LinearTeamID == "" {
		return errors.New("LINEAR_TEAM_ID is required")
	}
	if cfg.PollEvery <= 0 {
		return fmt.Errorf("COLIN_POLL_EVERY must be > 0, got %s", cfg.PollEvery)
	}
	if cfg.LeaseTTL <= 0 {
		return fmt.Errorf("COLIN_LEASE_TTL must be > 0, got %s", cfg.LeaseTTL)
	}
	if cfg.WorkerID == "" {
		return errors.New("COLIN_WORKER_ID must not be empty")
	}
	return nil
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
