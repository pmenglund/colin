package service

import (
	"context"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	tsdiag "github.com/pmenglund/colin/internal/tailscale"
	"github.com/pmenglund/colin/internal/workflow"
)

func loadConfig(workflowPath string, opts options) (string, domain.ServiceConfig, error) {
	loader := workflow.Loader{}
	path := loader.ResolvePath(workflowPath)
	def, err := loader.Load(path)
	if err != nil {
		return "", domain.ServiceConfig{}, err
	}
	cfg, err := config.Build(def, path)
	if err != nil {
		return "", domain.ServiceConfig{}, err
	}
	if opts.serverPortOverride != nil {
		cfg.Server.Port = intPtr(*opts.serverPortOverride)
	}
	return path, cfg, nil
}

// LoadFunnelSetupStatus loads WORKFLOW.md and reports current Funnel readiness.
func LoadFunnelSetupStatus(ctx context.Context, workflowPath string, optionFns ...Option) (domain.FunnelSetupStatus, error) {
	opts := buildOptions(optionFns...)
	_, cfg, err := loadConfig(workflowPath, opts)
	if err != nil {
		return domain.FunnelSetupStatus{}, err
	}
	inspector := tsdiag.NewInspector()
	return inspector.Check(ctx, tsdiag.Options{
		LocalPort:         cfg.Server.Port,
		ExplicitPublicURL: cfg.Server.PublicURL,
	}), nil
}
