package cmd

import (
	"os"
	"strings"

	"github.com/pmenglund/colin/internal/config"
)

func loadCLIConfig(rootOpts *RootOptions) (config.Config, error) {
	opts := loadCLIConfigOptions(rootOpts)
	return config.LoadWithOptions(opts)
}

func loadCLIConfigProvider(rootOpts *RootOptions) (*config.Provider, error) {
	return config.NewProvider(loadCLIConfigOptions(rootOpts))
}

func loadCLIConfigOptions(rootOpts *RootOptions) config.LoadOptions {
	configPath := ""
	workflowPath := ""
	if rootOpts != nil {
		configPath = strings.TrimSpace(rootOpts.ConfigPath)
		workflowPath = strings.TrimSpace(rootOpts.WorkflowPath)
	}
	if configPath == "" {
		configPath = strings.TrimSpace(os.Getenv("COLIN_CONFIG"))
	}
	return config.LoadOptions{
		ConfigPath:   configPath,
		WorkflowPath: workflowPath,
	}
}
