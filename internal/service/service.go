package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/agent/codex"
	"github.com/pmenglund/colin/internal/app"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/logbuffer"
	"github.com/pmenglund/colin/internal/orchestrator"
	"github.com/pmenglund/colin/internal/repoops"
	tsdiag "github.com/pmenglund/colin/internal/tailscale"
	"github.com/pmenglund/colin/internal/tracker/linear"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
)

// Service wires startup, workflow reload, and the orchestrator loop into one process lifecycle.
type Service struct {
	logger       *slog.Logger
	logBuffer    *logbuffer.Buffer
	loader       workflow.Loader
	workflowPath string
	options      options
	serverPort   *int
	serverMu     sync.RWMutex
	serverURL    string
	runtimeMu    sync.RWMutex
	runtime      orchestrator.Runtime
	orch         *orchestrator.Orchestrator
	inspector    *tsdiag.Inspector
}

// New constructs the service and loads the initial runtime from WORKFLOW.md.
func New(logger *slog.Logger, workflowPath string, optionFns ...Option) (*Service, error) {
	loader := workflow.Loader{}
	path := loader.ResolvePath(workflowPath)
	options := buildOptions(optionFns...)
	buffer := logbuffer.New(domain.DefaultLogBufferLines)
	logger = wrapLogger(logger, buffer)
	runtime, err := loadRuntime(path, logger, options)
	if err != nil {
		return nil, err
	}
	buffer.Resize(runtime.Config.Server.LogBufferLines)
	orch := orchestrator.New(runtime, logger)
	return &Service{
		logger:       logger,
		logBuffer:    buffer,
		loader:       loader,
		workflowPath: path,
		options:      options,
		serverPort:   clonePort(runtime.Config.Server.Port),
		runtime:      runtime,
		orch:         orch,
		inspector:    tsdiag.NewInspector(),
	}, nil
}

// Run starts startup cleanup, workflow reload watching, and the orchestrator loop.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("service starting", "workflow_path", s.workflowPath)
	if err := s.startHTTPServer(ctx); err != nil {
		return err
	}
	s.applyUIBaseURLResolver(s.currentRuntime())
	if err := s.ensureManagedLabels(ctx); err != nil {
		return err
	}
	if err := s.orch.StartupTerminalCleanup(ctx); err != nil {
		s.logger.Warn("startup cleanup skipped", "error", err)
	}
	s.logger.Info("workflow watch started", "path", s.workflowPath, "interval_seconds", 2)
	go s.watchWorkflow(ctx)
	return s.orch.Run(ctx)
}

// DashboardURL returns the dashboard bind URL when the HTTP server is enabled.
func (s *Service) DashboardURL() string {
	s.serverMu.RLock()
	defer s.serverMu.RUnlock()
	return s.serverURL
}

// DashboardEnabled reports whether the dashboard server is configured to start.
func (s *Service) DashboardEnabled() bool {
	return s.serverPort != nil
}

// FunnelSetupURL returns the local setup page URL when the HTTP server is enabled.
func (s *Service) FunnelSetupURL() string {
	base := strings.TrimRight(s.DashboardURL(), "/")
	if base == "" {
		return ""
	}
	return base + "/setup/funnel"
}

func loadRuntime(path string, logger *slog.Logger, opts options) (orchestrator.Runtime, error) {
	loader := workflow.Loader{}
	def, err := loader.Load(path)
	if err != nil {
		return orchestrator.Runtime{}, err
	}
	_, cfg, err := loadConfig(path, opts)
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
	repoManager := repoops.NewManager(cfg, logger)
	runner := codex.NewRunner(cfg, def, trackerClient, manager, logger)
	logger.Info(
		"runtime loaded",
		"workflow_path", path,
		"project_slug", cfg.Tracker.ProjectSlug,
		"poll_interval", cfg.Polling.Interval.String(),
		"workspace_root", cfg.Workspace.Root,
		"publish_states", cfg.Repo.PublishStates,
		"merge_states", cfg.Repo.MergeStates,
		"max_concurrent_agents", cfg.Agent.MaxConcurrentAgents,
	)
	return orchestrator.Runtime{
		Workflow:  def,
		Config:    cfg,
		Tracker:   trackerClient,
		Repo:      repoManager,
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

			runtime, err := loadRuntime(s.workflowPath, s.logger, s.options)
			if err != nil {
				s.logger.Error("workflow reload failed; keeping last good config", "path", s.workflowPath, "error", err)
				continue
			}
			if s.logBuffer != nil {
				s.logBuffer.Resize(runtime.Config.Server.LogBufferLines)
			}
			s.logger.Info("workflow reloaded", "path", s.workflowPath)
			s.setRuntime(runtime)
			s.orch.UpdateRuntime(runtime)
			s.applyUIBaseURLResolver(runtime)
		}
	}
}

func (s *Service) startHTTPServer(ctx context.Context) error {
	if s.serverPort == nil {
		return nil
	}

	handler, err := app.NewObservabilityServer(
		func(snapshotCtx context.Context) (domain.Snapshot, error) {
			return s.orch.Snapshot(snapshotCtx)
		},
		func(snapshotCtx context.Context, issueID string) (domain.Issue, error) {
			runtime := s.currentRuntime()
			if runtime.Tracker == nil {
				return domain.Issue{}, errors.New("tracker unavailable")
			}
			return runtime.Tracker.FetchIssueByID(snapshotCtx, issueID)
		},
		func(snapshotCtx context.Context) (domain.FunnelSetupStatus, error) {
			return s.funnelSetupStatus(snapshotCtx), nil
		},
		func(_ context.Context, minLevel *slog.Level) (domain.BufferedLogSnapshot, error) {
			if s.logBuffer == nil {
				return domain.BufferedLogSnapshot{}, nil
			}
			return s.logBuffer.Snapshot(minLevel), nil
		},
	)
	if err != nil {
		return fmt.Errorf("create dashboard server: %w", err)
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(*s.serverPort)))
	if err != nil {
		return fmt.Errorf("listen dashboard server: %w", err)
	}

	s.serverMu.Lock()
	s.serverURL = "http://" + listener.Addr().String()
	s.serverMu.Unlock()
	s.logger.Info("dashboard server started", "url", s.DashboardURL())

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Warn("dashboard shutdown failed", "error", err)
		}
	}()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("dashboard server exited", "error", err)
		}
	}()

	return nil
}

func clonePort(value *int) *int {
	if value == nil {
		return nil
	}
	return intPtr(*value)
}

func (s *Service) currentRuntime() orchestrator.Runtime {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtime
}

func (s *Service) setRuntime(runtime orchestrator.Runtime) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runtime = runtime
}

func intPtr(value int) *int {
	return &value
}

func (s *Service) ensureManagedLabels(ctx context.Context) error {
	runtime := s.currentRuntime()
	if runtime.Tracker == nil {
		return nil
	}
	for _, labelName := range domain.ManagedIssueLabels() {
		if err := runtime.Tracker.EnsureIssueLabel(ctx, labelName); err != nil {
			return fmt.Errorf("ensure %s label: %w", labelName, err)
		}
	}
	return nil
}

func (s *Service) funnelSetupStatus(ctx context.Context) domain.FunnelSetupStatus {
	runtime := s.currentRuntime()
	inspector := s.inspector
	if inspector == nil {
		inspector = tsdiag.NewInspector()
	}
	return inspector.Check(ctx, tsdiag.Options{
		LocalPort:                runtime.Config.Server.Port,
		LocalDashboardURL:        s.DashboardURL(),
		ExplicitWebhookPublicURL: webhookPublicURL(runtime.Config.Server),
	})
}

func (s *Service) effectiveWebhookPublicBaseURL(ctx context.Context, cfg domain.ServerConfig) string {
	inspector := s.inspector
	if inspector == nil {
		inspector = tsdiag.NewInspector()
	}
	status := inspector.Resolve(ctx, tsdiag.Options{
		LocalPort:                cfg.Port,
		LocalDashboardURL:        s.DashboardURL(),
		ExplicitWebhookPublicURL: webhookPublicURL(cfg),
	})
	return strings.TrimSpace(status.PublicBaseURL)
}

func (s *Service) effectiveUIBaseURL(cfg domain.ServerConfig) string {
	if value := strings.TrimSpace(cfg.UIURL); value != "" {
		return value
	}
	if value := strings.TrimSpace(s.DashboardURL()); value != "" {
		return value
	}
	if cfg.Port != nil {
		return fmt.Sprintf("http://127.0.0.1:%d", *cfg.Port)
	}
	return "http://127.0.0.1"
}

func webhookPublicURL(cfg domain.ServerConfig) string {
	if value := strings.TrimSpace(cfg.WebhookPublicURL); value != "" {
		return value
	}
	return strings.TrimSpace(cfg.PublicURL)
}

func (s *Service) applyUIBaseURLResolver(runtime orchestrator.Runtime) {
	client, ok := runtime.Tracker.(*linear.Client)
	if !ok || client == nil {
		return
	}
	client.SetUIBaseURLResolver(func(context.Context) string {
		return s.effectiveUIBaseURL(runtime.Config.Server)
	})
}

// NewDefaultLogger returns the repo-default structured logger.
func NewDefaultLogger(verbose bool) *slog.Logger {
	return newLogger(os.Stderr, verbose)
}

func wrapLogger(logger *slog.Logger, buffer *logbuffer.Buffer) *slog.Logger {
	if logger == nil {
		logger = NewDefaultLogger(false)
	}
	if buffer == nil {
		return logger
	}
	return slog.New(logbuffer.NewHandler(logger.Handler(), buffer))
}

func newLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelError
	if verbose {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
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
