package cmd

import (
	"strings"

	"github.com/pmenglund/colin/internal/config"
)

func loadCLIConfig(rootOpts *RootOptions) (config.Config, error) {
	configPath := ""
	if rootOpts != nil {
		configPath = strings.TrimSpace(rootOpts.ConfigPath)
	}
	if configPath == "" {
		return config.Load()
	}
	return config.LoadFromPath(configPath)
}
