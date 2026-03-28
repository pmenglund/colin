package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/orchestrator"
	"github.com/pmenglund/colin/internal/tracker/linear"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
)

// Service wires startup, workflow reload, and the orchestrator loop into one process lifecycle.
type Service struct {
	logger       *slog.Logger
	loader       workflow.Loader
	workflowPath string
	orch         *orchestrator.Orchestrator
}

// New constructs the service and loads the initial runtime from WORKFLOW.md.
func New(logger *slog.Logger, workflowPath string) (*Service, error) {
	loader := workflow.Loader{}
	path := loader.ResolvePath(workflowPath)
	runtime, err := loadRuntime(path, logger)
	if err != nil {
		return nil, err
	}
	orch := orchestrator.New(runtime, logger)
	return &Service{
		logger:       logger,
		loader:       loader,
		workflowPath: path,
		orch:         orch,
	}, nil
}

// Run starts startup cleanup, workflow reload watching, and the orchestrator loop.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("service starting", "workflow_path", s.workflowPath)
	if err := s.orch.StartupTerminalCleanup(ctx); err != nil {
		s.logger.Warn("startup cleanup skipped", "error", err)
	}
	s.logger.Info("workflow watch started", "path", s.workflowPath, "interval_seconds", 2)
	go s.watchWorkflow(ctx)
	return s.orch.Run(ctx)
}

func loadRuntime(path string, logger *slog.Logger) (orchestrator.Runtime, error) {
	loader := workflow.Loader{}
	def, err := loader.Load(path)
	if err != nil {
		return orchestrator.Runtime{}, err
	}
	cfg, err := config.Build(def, path)
	if err != nil {
		return orchestrator.Runtime{}, err
	}
	if err := config.ValidateDispatch(cfg); err != nil {
		return orchestrator.Runtime{}, err
	}
	trackerClient, err := linear.New(cfg)
	if err != nil {
		return orchestrator.Runtime{}, err
	}
	manager := workspace.NewManager(cfg, logger)
	runner := codex.NewRunner(cfg, def, trackerClient, manager, logger)
	logger.Info(
		"runtime loaded",
		"workflow_path", path,
		"project_slug", cfg.Tracker.ProjectSlug,
		"poll_interval", cfg.Polling.Interval.String(),
		"workspace_root", cfg.Workspace.Root,
		"max_concurrent_agents", cfg.Agent.MaxConcurrentAgents,
	)
	return orchestrator.Runtime{
		Workflow:  def,
		Config:    cfg,
		Tracker:   trackerClient,
		Workspace: manager,
		Runner:    runner,
	}, nil
}

func (s *Service) watchWorkflow(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastModTime time.Time
	var lastSize int64

	stat, err := os.Stat(s.workflowPath)
	if err == nil {
		lastModTime = stat.ModTime()
		lastSize = stat.Size()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stat, err := os.Stat(s.workflowPath)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					s.logger.Warn("workflow stat failed", "path", s.workflowPath, "error", err)
				}
				continue
			}
			if stat.ModTime().Equal(lastModTime) && stat.Size() == lastSize {
				continue
			}
			lastModTime = stat.ModTime()
			lastSize = stat.Size()

			runtime, err := loadRuntime(s.workflowPath, s.logger)
			if err != nil {
				s.logger.Error("workflow reload failed; keeping last good config", "path", s.workflowPath, "error", err)
				continue
			}
			s.logger.Info("workflow reloaded", "path", s.workflowPath)
			s.orch.UpdateRuntime(runtime)
		}
	}
}

// NewDefaultLogger returns the repo-default structured logger.
func NewDefaultLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// DescribeStartupError converts common startup failures into operator-facing text.
func DescribeStartupError(err error) string {
	switch {
	case errors.Is(err, workflow.ErrMissingWorkflowFile):
		return fmt.Sprintf("workflow file not found: %v", err)
	default:
		return err.Error()
	}
}
