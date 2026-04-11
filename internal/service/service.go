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

	"github.com/pmenglund/colin/internal/app"
	"github.com/pmenglund/colin/internal/automation"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/logbuffer"
	"github.com/pmenglund/colin/internal/notify"
	slacknotify "github.com/pmenglund/colin/internal/notify/slack"
	"github.com/pmenglund/colin/internal/orchestrator"
	"github.com/pmenglund/colin/internal/repohost"
	"github.com/pmenglund/colin/internal/repoops"
	tsdiag "github.com/pmenglund/colin/internal/tailscale"
	"github.com/pmenglund/colin/internal/tracker"
	"github.com/pmenglund/colin/internal/userworkflow"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workspace"
)

type tailscaleInspector interface {
	Check(context.Context, tsdiag.Options) domain.FunnelSetupStatus
	Resolve(context.Context, tsdiag.Options) domain.FunnelSetupStatus
	ResolveUIBaseURL(context.Context, *int) string
}

type slackHomePublisherClient interface {
	PublishHome(context.Context, string, userworkflow.SlackHomeView) error
}

const (
	runtimePreflightTimeout        = 20 * time.Second
	githubStartupValidationTimeout = 10 * time.Second
	delegationAckRecentTTL         = 15 * time.Second
)

var errDuplicateListenerPorts = errors.New("server.port and server.webhook_port must be different when both are enabled")

var newSlackHomeNotifier = func(cfg domain.ServiceConfig) slackHomePublisherClient {
	if strings.TrimSpace(cfg.Slack.BotToken) == "" || strings.TrimSpace(cfg.Slack.ChannelID) == "" {
		return nil
	}
	return slacknotify.New(cfg.Slack.BotToken, cfg.Slack.ChannelID, nil)
}

type slackSocketModeRunner interface {
	Run(context.Context) error
}

var newSlackSocketModeRunner = func(cfg domain.SlackConfig, logger *slog.Logger, observer slacknotify.SocketModeStatusObserver) slackSocketModeRunner {
	return slacknotify.NewSocketMode(cfg.AppToken, cfg.BotToken, logger, observer)
}

// Service wires startup, workflow reload, and the orchestrator loop into one process lifecycle.
type Service struct {
	logger               *slog.Logger
	logBuffer            *logbuffer.Buffer
	loader               workflow.Loader
	workflowPath         string
	options              options
	serverPort           *int
	webhookPort          *int
	serverMu             sync.RWMutex
	serverURL            string
	webhookURL           string
	runtimeMu            sync.RWMutex
	runtime              orchestrator.Runtime
	slackMu              sync.RWMutex
	slackStatus          domain.SlackSocketModeStatus
	webhookMu            sync.RWMutex
	webhooks             map[string]domain.WebhookStatus
	delegationAckMu      sync.Mutex
	recentDelegationAcks map[string]time.Time
	orch                 *orchestrator.Orchestrator
	inspector            tailscaleInspector
}

// New constructs the service and loads the initial runtime from WORKFLOW.md.
func New(ctx context.Context, logger *slog.Logger, workflowPath string, optionFns ...Option) (*Service, error) {
	loader := workflow.Loader{}
	path := loader.ResolvePath(workflowPath)
	options := buildOptions(optionFns...)
	buffer := logbuffer.New(domain.DefaultLogBufferLines)
	logger = wrapLogger(logger, buffer)
	runtime, err := loadRuntime(ctx, path, logger, options)
	if err != nil {
		return nil, err
	}
	buffer.Resize(runtime.Config.Server.LogBufferLines)
	orch := orchestrator.New(runtime, logger)
	return &Service{
		logger:               logger,
		logBuffer:            buffer,
		loader:               loader,
		workflowPath:         path,
		options:              options,
		serverPort:           clonePort(runtime.Config.Server.Port),
		webhookPort:          clonePort(runtime.Config.Server.WebhookPort),
		runtime:              runtime,
		slackStatus:          slackSocketModeStatusForConfig(runtime.Config.Slack),
		webhooks:             map[string]domain.WebhookStatus{},
		recentDelegationAcks: map[string]time.Time{},
		orch:                 orch,
		inspector:            tsdiag.NewInspector(),
	}, nil
}

// Run starts startup cleanup, workflow reload watching, and the orchestrator loop.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("service starting", "workflow_path", s.workflowPath)
	if err := s.startUIServer(ctx); err != nil {
		return err
	}
	if err := s.startWebhookServer(ctx); err != nil {
		return err
	}
	s.startSlackSocketMode(ctx)
	stopGOPS := startGOPSAgent(ctx, s.logger, defaultGOPSHooks)
	defer stopGOPS()
	s.applyUIBaseURLResolver(s.currentRuntime())
	if err := s.orch.StartupTerminalCleanup(ctx); err != nil {
		s.logger.Warn("startup cleanup skipped", "error", err)
	}
	s.logger.Info("workflow watch started", "path", s.workflowPath, "interval_seconds", 2)
	go s.watchWorkflow(ctx)
	return s.orch.Run(ctx)
}

// RequestShutdownDrain asks the orchestrator to stop dispatching new work and let active workers finish.
func (s *Service) RequestShutdownDrain() bool {
	if s == nil || s.orch == nil {
		return false
	}
	return s.orch.RequestShutdownDrain()
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

func (s *Service) webhookBaseURL() string {
	s.serverMu.RLock()
	defer s.serverMu.RUnlock()
	return s.webhookURL
}

// Snapshot returns the current in-memory orchestrator snapshot.
func (s *Service) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	snapshot, err := s.orch.Snapshot(ctx)
	if err != nil {
		return domain.Snapshot{}, err
	}
	runtime := s.currentRuntime()
	snapshot.WorkflowPath = runtime.Config.WorkflowPath
	snapshot.Targets = snapshotTargets(runtime.Config)
	snapshot.SlackSocketMode = s.currentSlackSocketModeStatus()
	snapshot.Webhooks = s.currentWebhookStatuses()
	return snapshot, nil
}

// BufferedLogs returns the current in-memory structured log buffer.
func (s *Service) BufferedLogs(_ context.Context, minLevel *slog.Level) (domain.BufferedLogSnapshot, error) {
	if s.logBuffer == nil {
		return domain.BufferedLogSnapshot{}, nil
	}
	return s.logBuffer.Snapshot(minLevel), nil
}

// FunnelSetupStatus returns the current Funnel readiness snapshot.
func (s *Service) FunnelSetupStatus(ctx context.Context) domain.FunnelSetupStatus {
	return s.funnelSetupStatus(ctx)
}

func loadRuntime(ctx context.Context, path string, logger *slog.Logger, opts options) (orchestrator.Runtime, error) {
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
	if err := validateListenerPorts(cfg.Server.Port, cfg.Server.WebhookPort); err != nil {
		return orchestrator.Runtime{}, err
	}
	preflightCtx, cancel := runtimePreflightContext(ctx)
	defer cancel()
	trackerClient, _, err := PreflightTrackerConfig(preflightCtx, cfg)
	if err != nil {
		return orchestrator.Runtime{}, err
	}
	manager := workspace.NewManager(cfg, logger)
	repoManager := repoops.NewManager(cfg, logger)
	runner := automation.NewRunner(cfg, def, trackerClient, manager, logger)
	notifier := notify.NewNoop()
	if strings.TrimSpace(cfg.Slack.BotToken) != "" && strings.TrimSpace(cfg.Slack.ChannelID) != "" {
		notifier = slacknotify.New(cfg.Slack.BotToken, cfg.Slack.ChannelID, logger)
	}
	logger.Info(
		"runtime loaded",
		"workflow_path", path,
		"target_count", len(cfg.Targets),
		"target_projects", cfg.WatchedProjectSlugs(),
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
		Notifier:  notifier,
	}, nil
}

func runtimePreflightContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, runtimePreflightTimeout)
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

			runtime, err := loadRuntime(ctx, s.workflowPath, s.logger, s.options)
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

func validateRepoAccess(ctx context.Context, cfg domain.ServiceConfig, manager *repoops.Manager) error {
	if strings.TrimSpace(cfg.Repo.APIToken) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, githubStartupValidationTimeout)
	defer cancel()
	return manager.ValidateRepoAccess(ctx)
}

func validateGitHubAccess(ctx context.Context, cfg domain.ServiceConfig, manager *repoops.Manager) error {
	return validateRepoAccess(ctx, cfg, manager)
}

func (s *Service) startUIServer(ctx context.Context) error {
	if s.serverPort == nil {
		return nil
	}

	handler, err := s.newDashboardHandler()
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

func (s *Service) startWebhookServer(ctx context.Context) error {
	if !hasEnabledPort(s.webhookPort) {
		return nil
	}

	handler := app.NewWebhookHandler(s.linearWebhookTrigger(), s.linearWebhookSecretProvider(), s.githubWebhookTrigger(), s.githubWebhookSecretProvider(), s.slackWebhookPublisher(), s.slackWebhookSecretProvider(), s.logger)

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(*s.webhookPort)))
	if err != nil {
		return fmt.Errorf("listen webhook server: %w", err)
	}

	s.serverMu.Lock()
	s.webhookURL = "http://" + listener.Addr().String()
	s.serverMu.Unlock()
	s.logger.Info("webhook server started", "url", s.webhookBaseURL())

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Warn("webhook server shutdown failed", "error", err)
		}
	}()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("webhook server exited", "error", err)
		}
	}()

	return nil
}

func (s *Service) startSlackSocketMode(ctx context.Context) {
	runtime := s.currentRuntime()
	if strings.TrimSpace(runtime.Config.Slack.AppToken) == "" {
		s.setSlackSocketModeStatus(slackSocketModeStatusForConfig(runtime.Config.Slack))
		return
	}

	s.setSlackSocketModeStatus(domain.SlackSocketModeStatus{
		Enabled:     true,
		State:       "connecting",
		LastEvent:   "starting",
		LastEventAt: time.Now().UTC(),
	})
	runner := newSlackSocketModeRunner(runtime.Config.Slack, s.logger, s.setSlackSocketModeStatus)
	if runner == nil {
		return
	}

	s.logger.Info("slack socket mode enabled")
	go func() {
		if err := runner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.setSlackSocketModeStatus(domain.SlackSocketModeStatus{
				Enabled:     true,
				State:       "error",
				LastEvent:   "run_error",
				LastEventAt: time.Now().UTC(),
				LastError:   err.Error(),
			})
			s.logger.Warn("slack socket mode exited", "error", err)
		}
	}()
}

func slackSocketModeStatusForConfig(cfg domain.SlackConfig) domain.SlackSocketModeStatus {
	if strings.TrimSpace(cfg.AppToken) == "" {
		return domain.SlackSocketModeStatus{State: "disabled"}
	}
	return domain.SlackSocketModeStatus{
		Enabled: true,
		State:   "configured",
	}
}

func (s *Service) setSlackSocketModeStatus(status domain.SlackSocketModeStatus) {
	if s == nil {
		return
	}
	s.slackMu.Lock()
	defer s.slackMu.Unlock()
	s.slackStatus = cloneSlackSocketModeStatus(status)
}

func (s *Service) currentSlackSocketModeStatus() domain.SlackSocketModeStatus {
	if s == nil {
		return domain.SlackSocketModeStatus{}
	}
	s.slackMu.RLock()
	defer s.slackMu.RUnlock()
	return cloneSlackSocketModeStatus(s.slackStatus)
}

func cloneSlackSocketModeStatus(input domain.SlackSocketModeStatus) domain.SlackSocketModeStatus {
	out := input
	out.Sockets = append([]domain.SlackWebSocketStatus(nil), input.Sockets...)
	return out
}

func (s *Service) markWebhookMessage(name string) {
	if s == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	s.webhookMu.Lock()
	defer s.webhookMu.Unlock()
	if s.webhooks == nil {
		s.webhooks = map[string]domain.WebhookStatus{}
	}
	s.webhooks[name] = domain.WebhookStatus{LastMessageAt: time.Now().UTC()}
}

func (s *Service) currentWebhookStatuses() map[string]domain.WebhookStatus {
	if s == nil {
		return nil
	}
	s.webhookMu.RLock()
	defer s.webhookMu.RUnlock()
	if len(s.webhooks) == 0 {
		return map[string]domain.WebhookStatus{}
	}
	out := make(map[string]domain.WebhookStatus, len(s.webhooks))
	for key, value := range s.webhooks {
		out[key] = value
	}
	return out
}

func clonePort(value *int) *int {
	if value == nil {
		return nil
	}
	return intPtr(*value)
}

func validateListenerPorts(serverPort *int, webhookPort *int) error {
	if !hasEnabledPort(serverPort) || !hasEnabledPort(webhookPort) {
		return nil
	}
	if *serverPort == *webhookPort {
		return fmt.Errorf("%w: %d", errDuplicateListenerPorts, *serverPort)
	}
	return nil
}

func hasEnabledPort(value *int) bool {
	return value != nil && *value > 0
}

func snapshotTargets(cfg domain.ServiceConfig) []domain.SnapshotTarget {
	if len(cfg.Targets) == 0 {
		return nil
	}
	targets := make([]domain.SnapshotTarget, 0, len(cfg.Targets))
	for _, target := range cfg.Targets {
		targets = append(targets, domain.SnapshotTarget{
			Name:        strings.TrimSpace(target.Name),
			ProjectSlug: strings.TrimSpace(target.ProjectSlug),
			RepoURL:     strings.TrimSpace(target.RepoURL),
			BaseRef:     strings.TrimSpace(target.BaseRef),
		})
	}
	return targets
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

func (s *Service) newDashboardHandler() (http.Handler, error) {
	provider := func(snapshotCtx context.Context) (domain.Snapshot, error) {
		return s.Snapshot(snapshotCtx)
	}
	issueProvider := func(snapshotCtx context.Context, issueID string) (domain.Issue, error) {
		runtime := s.currentRuntime()
		if runtime.Tracker == nil {
			return domain.Issue{}, errors.New("tracker unavailable")
		}
		return runtime.Tracker.FetchIssueByID(snapshotCtx, issueID)
	}
	setupProvider := func(snapshotCtx context.Context) (domain.FunnelSetupStatus, error) {
		return s.funnelSetupStatus(snapshotCtx), nil
	}
	logProvider := func(_ context.Context, minLevel *slog.Level) (domain.BufferedLogSnapshot, error) {
		if s.logBuffer == nil {
			return domain.BufferedLogSnapshot{}, nil
		}
		return s.logBuffer.Snapshot(minLevel), nil
	}
	streamProvider := func(ctx context.Context) (domain.SnapshotUpdate, <-chan domain.SnapshotUpdate, error) {
		if s.orch == nil {
			return domain.SnapshotUpdate{}, nil, nil
		}
		return s.orch.LatestSnapshotUpdate(), s.orch.SubscribeSnapshotUpdates(ctx), nil
	}
	if !hasEnabledPort(s.webhookPort) {
		return app.NewObservabilityServer(provider, issueProvider, setupProvider, logProvider, streamProvider, s.linearWebhookTrigger(), s.linearWebhookSecretProvider(), s.githubWebhookTrigger(), s.githubWebhookSecretProvider(), s.slackWebhookPublisher(), s.slackWebhookSecretProvider(), s.logger)
	}
	return app.NewUIHandler(provider, issueProvider, setupProvider, logProvider, streamProvider)
}

func (s *Service) linearWebhookTrigger() app.LinearWebhookTrigger {
	return func(ctx context.Context, event app.LinearWebhookEvent) app.LinearWebhookTriggerResult {
		s.markWebhookMessage("linear")
		runtime := s.currentRuntime()
		event, hydratedIssue := s.hydrateLinearWebhookEvent(ctx, runtime, event)
		if !shouldQueueImmediateLinearRefresh(event, watchedProjectIDs(runtime.Tracker)) {
			return app.LinearWebhookTriggerResult{}
		}
		s.acknowledgeLinearAgentSession(ctx, runtime, event, hydratedIssue)
		s.acknowledgeDelegatedLinearIssue(ctx, runtime, event, hydratedIssue)
		reason := fmt.Sprintf("linear webhook delivery=%s event=%s action=%s resource_type=%s", event.DeliveryID, event.Event, event.Action, event.ResourceType)
		if event.IssueID != "" {
			reason += " issue_id=" + event.IssueID
		}
		if event.ProjectID != "" {
			reason += " project_id=" + event.ProjectID
		}
		if s.orch == nil {
			return app.LinearWebhookTriggerResult{Relevant: true}
		}
		queued, coalesced := s.orch.RequestRefresh(reason)
		if queued {
			return app.LinearWebhookTriggerResult{Relevant: true, Queued: true, Coalesced: coalesced}
		}
		if !s.orch.RefreshReady() {
			return app.LinearWebhookTriggerResult{Relevant: true, Suppressed: true}
		}
		return app.LinearWebhookTriggerResult{Relevant: true}
	}
}

func (s *Service) hydrateLinearWebhookEvent(ctx context.Context, runtime orchestrator.Runtime, event app.LinearWebhookEvent) (app.LinearWebhookEvent, *domain.Issue) {
	if runtime.Tracker == nil {
		return event, nil
	}
	if strings.TrimSpace(event.IssueID) == "" || strings.TrimSpace(event.ProjectID) != "" {
		return event, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	issue, err := runtime.Tracker.FetchIssueByID(lookupCtx, event.IssueID)
	if err != nil {
		s.logger.Warn("failed to load Linear issue for project-scoped webhook", "issue_id", event.IssueID, "resource_type", event.ResourceType, "error", err)
		return event, nil
	}
	if strings.TrimSpace(issue.ID) == "" {
		return event, nil
	}
	event.ProjectID = strings.TrimSpace(issue.ProjectID)
	return event, &issue
}

func (s *Service) acknowledgeLinearAgentSession(ctx context.Context, runtime orchestrator.Runtime, event app.LinearWebhookEvent, hydratedIssue *domain.Issue) {
	if runtime.Tracker == nil || !runtime.Config.Tracker.AppMode {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(event.ResourceType), "AgentSessionEvent") {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(event.Action), "created") {
		return
	}
	if strings.TrimSpace(event.SessionID) == "" || strings.TrimSpace(event.IssueID) == "" {
		return
	}

	ackCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	issue := domain.Issue{}
	if hydratedIssue != nil {
		issue = *hydratedIssue
	} else {
		var err error
		issue, err = runtime.Tracker.FetchIssueByID(ackCtx, event.IssueID)
		if err != nil {
			s.logger.Warn("failed to load delegated Linear issue for agent-session acknowledgement", "issue_id", event.IssueID, "error", err)
			return
		}
	}
	if strings.TrimSpace(issue.ID) == "" || !issue.DelegatedToColin {
		return
	}

	ackKind, body := delegationAcknowledgement(runtime.Config, issue)
	if strings.TrimSpace(body) == "" {
		return
	}
	if !shouldPostDelegationAcknowledgement(issue.ColinMetadata, issue.State, ackKind, event.SessionID) {
		return
	}
	if err := runtime.Tracker.CreateAgentActivityThought(ackCtx, event.SessionID, body); err != nil {
		s.logger.Warn("failed to create Linear agent-session acknowledgement activity", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "session_id", event.SessionID, "error", err)
	}
}

func (s *Service) acknowledgeDelegatedLinearIssue(ctx context.Context, runtime orchestrator.Runtime, event app.LinearWebhookEvent, hydratedIssue *domain.Issue) {
	if runtime.Tracker == nil || !runtime.Config.Tracker.AppMode {
		return
	}
	if !shouldAcknowledgeDelegationFromLinearEvent(event) {
		return
	}
	if strings.TrimSpace(event.IssueID) == "" {
		return
	}

	ackCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	issue := domain.Issue{}
	if hydratedIssue != nil {
		issue = *hydratedIssue
	} else {
		var err error
		issue, err = runtime.Tracker.FetchIssueByID(ackCtx, event.IssueID)
		if err != nil {
			s.logger.Warn("failed to load delegated Linear issue for acknowledgement", "issue_id", event.IssueID, "error", err)
			return
		}
	}
	if strings.TrimSpace(issue.ID) == "" || !issue.DelegatedToColin {
		return
	}

	ackKind, body := delegationAcknowledgement(runtime.Config, issue)
	if strings.TrimSpace(body) == "" {
		return
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if !shouldPostDelegationAcknowledgement(issue.ColinMetadata, issue.State, ackKind, sessionID) {
		return
	}
	if strings.TrimSpace(event.SourceCommentID) == "" && s.hasRecentDelegationAcknowledgement(issue.ID, issue.State, ackKind, time.Now().UTC()) {
		return
	}
	ackKey := ""
	if strings.TrimSpace(event.SourceCommentID) != "" {
		var ok bool
		ackKey, ok = s.reserveDelegationAcknowledgement(issue.ID, issue.State, ackKind, time.Now().UTC())
		if !ok {
			return
		}
	}

	commentID, progressRootID, err := postDelegationAcknowledgement(ackCtx, runtime.Tracker, issue, event, body)
	if err != nil {
		s.releaseDelegationAcknowledgement(ackKey)
		s.logger.Warn("failed to post Linear delegation acknowledgement", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return
	}

	metadata := domain.ColinMetadata{
		ProgressRootCommentID:  strings.TrimSpace(progressRootID),
		ColinCommentIDs:        appendUniqueTrimmed(nil, commentID),
		DelegationAckKind:      ackKind,
		DelegationAckState:     strings.TrimSpace(issue.State),
		DelegationAckSessionID: sessionID,
	}
	if issue.ColinMetadata != nil {
		metadata = *issue.ColinMetadata
		metadata.ProgressRootCommentID = strings.TrimSpace(progressRootID)
		metadata.ColinCommentIDs = appendUniqueTrimmed(metadata.ColinCommentIDs, commentID)
		metadata.DelegationAckKind = ackKind
		metadata.DelegationAckState = strings.TrimSpace(issue.State)
		metadata.DelegationAckSessionID = sessionID
	}
	now := time.Now().UTC()
	metadata.UpdatedAt = &now
	if _, err := runtime.Tracker.UpsertIssueMetadata(ackCtx, issue.ID, metadata); err != nil {
		s.logger.Warn("failed to persist Linear delegation acknowledgement metadata", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
	}
}

func delegationAcknowledgement(cfg domain.ServiceConfig, issue domain.Issue) (kind string, body string) {
	state := strings.TrimSpace(issue.State)
	if config.ContainsState(config.CandidateStates(cfg), state) {
		return "ready", fmt.Sprintf("Colin is assigned and will start work while this issue stays delegated in `%s`.", fallbackStateName(state))
	}

	humanStates := delegationAcknowledgementHumanStates(cfg)
	quotedStates := make([]string, 0, len(humanStates))
	for _, candidate := range humanStates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		quotedStates = append(quotedStates, fmt.Sprintf("`%s`", candidate))
	}
	if len(quotedStates) == 0 {
		return "waiting", fmt.Sprintf("Colin is assigned, but this issue is in `%s`. Move it into a Colin-managed state to start work.", fallbackStateName(state))
	}
	return "waiting", fmt.Sprintf(
		"Colin is assigned, but this issue is in `%s`. To start work, keep it delegated to Colin and move it to one of: %s.",
		fallbackStateName(state),
		strings.Join(quotedStates, ", "),
	)
}

func delegationAcknowledgementHumanStates(cfg domain.ServiceConfig) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 1+len(cfg.Repo.MergeStates))
	if len(cfg.Tracker.ActiveStates) > 0 {
		state := strings.TrimSpace(cfg.Tracker.ActiveStates[0])
		if state != "" {
			key := config.StateKey(state)
			seen[key] = struct{}{}
			out = append(out, state)
		}
	}
	for _, state := range cfg.Repo.MergeStates {
		state = strings.TrimSpace(state)
		if state == "" {
			continue
		}
		key := config.StateKey(state)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, state)
	}
	return out
}

func shouldPostDelegationAcknowledgement(metadata *domain.ColinMetadata, state string, kind string, sessionID string) bool {
	if metadata == nil {
		return true
	}
	if strings.TrimSpace(metadata.DelegationAckKind) != strings.TrimSpace(kind) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(metadata.DelegationAckState), strings.TrimSpace(state)) {
		return true
	}
	if strings.TrimSpace(sessionID) == "" {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(metadata.DelegationAckSessionID), strings.TrimSpace(sessionID))
}

func shouldAcknowledgeDelegationFromLinearEvent(event app.LinearWebhookEvent) bool {
	resourceType := strings.ToLower(strings.TrimSpace(event.ResourceType))
	action := strings.ToLower(strings.TrimSpace(event.Action))
	switch resourceType {
	case "agentsessionevent":
		return action == "created" || action == "prompted"
	case "issue":
		if action != "update" {
			return false
		}
		return containsChangedField(event.ChangedFields, "delegateid")
	default:
		return false
	}
}

func postDelegationAcknowledgement(ctx context.Context, tracker tracker.Client, issue domain.Issue, event app.LinearWebhookEvent, body string) (commentID string, progressRootID string, err error) {
	progressRootID = ""
	if issue.ColinMetadata != nil {
		progressRootID = strings.TrimSpace(issue.ColinMetadata.ProgressRootCommentID)
	}
	if sourceCommentID := strings.TrimSpace(event.SourceCommentID); sourceCommentID != "" {
		commentID, err = tracker.CreateCommentReply(ctx, issue.ID, sourceCommentID, body)
		if err != nil {
			return "", progressRootID, err
		}
		return commentID, progressRootID, nil
	}
	if progressRootID == "" {
		commentID, err = tracker.CreateIssueComment(ctx, issue.ID, body)
		if err != nil {
			return "", "", err
		}
		return commentID, commentID, nil
	}
	commentID, err = tracker.CreateCommentReply(ctx, issue.ID, progressRootID, body)
	if err != nil {
		return "", "", err
	}
	return commentID, progressRootID, nil
}

func appendUniqueTrimmed(values []string, items ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(items))
	out := make([]string, 0, len(values)+len(items))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func fallbackStateName(state string) string {
	if trimmed := strings.TrimSpace(state); trimmed != "" {
		return trimmed
	}
	return "unknown"
}

func delegationAcknowledgementKey(issueID string, state string, kind string) string {
	return strings.Join([]string{
		strings.TrimSpace(issueID),
		strings.ToLower(strings.TrimSpace(state)),
		strings.ToLower(strings.TrimSpace(kind)),
	}, "|")
}

func (s *Service) reserveDelegationAcknowledgement(issueID string, state string, kind string, now time.Time) (string, bool) {
	key := delegationAcknowledgementKey(issueID, state, kind)
	if strings.TrimSpace(key) == "||" {
		return "", true
	}
	s.delegationAckMu.Lock()
	defer s.delegationAckMu.Unlock()
	s.pruneRecentDelegationAcknowledgementsLocked(now)
	if expiresAt, ok := s.recentDelegationAcks[key]; ok && expiresAt.After(now) {
		return key, false
	}
	s.recentDelegationAcks[key] = now.Add(delegationAckRecentTTL)
	return key, true
}

func (s *Service) hasRecentDelegationAcknowledgement(issueID string, state string, kind string, now time.Time) bool {
	key := delegationAcknowledgementKey(issueID, state, kind)
	if strings.TrimSpace(key) == "||" {
		return false
	}
	s.delegationAckMu.Lock()
	defer s.delegationAckMu.Unlock()
	s.pruneRecentDelegationAcknowledgementsLocked(now)
	expiresAt, ok := s.recentDelegationAcks[key]
	return ok && expiresAt.After(now)
}

func (s *Service) releaseDelegationAcknowledgement(key string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	s.delegationAckMu.Lock()
	defer s.delegationAckMu.Unlock()
	delete(s.recentDelegationAcks, key)
}

func (s *Service) pruneRecentDelegationAcknowledgementsLocked(now time.Time) {
	if s.recentDelegationAcks == nil {
		s.recentDelegationAcks = map[string]time.Time{}
	}
	for existingKey, expiresAt := range s.recentDelegationAcks {
		if !expiresAt.After(now) {
			delete(s.recentDelegationAcks, existingKey)
		}
	}
}

func (s *Service) linearWebhookSecretProvider() app.LinearWebhookSecretProvider {
	return func(context.Context) []string {
		cfg := s.currentRuntime().Config.Tracker
		return []string{
			cfg.WebhookSigningSecret,
			cfg.AppWebhookSigningSecret,
		}
	}
}

func (s *Service) slackWebhookPublisher() app.SlackWebhookPublisher {
	return func(ctx context.Context, event app.SlackWebhookEvent) error {
		s.markWebhookMessage("slack")
		runtime := s.currentRuntime()
		if runtime.Tracker == nil {
			return errors.New("tracker unavailable")
		}
		notifier := newSlackHomeNotifier(runtime.Config)
		if notifier == nil {
			return nil
		}
		issues, err := runtime.Tracker.FetchIssueSnapshotsByStates(ctx, userworkflow.SlackHomeStateNames(runtime.Config))
		if err != nil {
			return err
		}
		return notifier.PublishHome(ctx, event.UserID, userworkflow.SlackHomeIssueView(runtime.Config, issues))
	}
}

func (s *Service) slackWebhookSecretProvider() app.SlackWebhookSecretProvider {
	return func(context.Context) string {
		return s.currentRuntime().Config.Slack.SigningSecret
	}
}

func (s *Service) githubWebhookTrigger() app.GitHubWebhookTrigger {
	return func(_ context.Context, event app.GitHubWebhookEvent) app.GitHubWebhookTriggerResult {
		runtime := s.currentRuntime()
		if !shouldQueueImmediateGitHubRefresh(event, watchedRepositoryFullNames(runtime.Config)) {
			return app.GitHubWebhookTriggerResult{}
		}
		reason := fmt.Sprintf("github webhook delivery=%s event=%s action=%s repository=%s", event.DeliveryID, event.Event, event.Action, event.RepositoryFullName)
		if event.PullRequestNumber > 0 {
			reason += fmt.Sprintf(" pull_request_number=%d", event.PullRequestNumber)
		}
		if s.orch == nil {
			return app.GitHubWebhookTriggerResult{Relevant: true}
		}
		queued, coalesced := s.orch.RequestRefresh(reason)
		if queued {
			return app.GitHubWebhookTriggerResult{Relevant: true, Queued: true, Coalesced: coalesced}
		}
		if !s.orch.RefreshReady() {
			return app.GitHubWebhookTriggerResult{Relevant: true, Suppressed: true}
		}
		return app.GitHubWebhookTriggerResult{Relevant: true}
	}
}

func (s *Service) githubWebhookSecretProvider() app.GitHubWebhookSecretProvider {
	return func(context.Context) string {
		return s.currentRuntime().Config.Repo.WebhookSigningSecret
	}
}

func (s *Service) ensureManagedLabels(ctx context.Context) error {
	return ensureManagedIssueLabels(ctx, s.currentRuntime().Tracker)
}

func ensureManagedIssueLabels(ctx context.Context, issueTracker orchestratorTracker) error {
	if issueTracker == nil {
		return nil
	}
	for _, labelName := range domain.ManagedIssueLabels() {
		if err := issueTracker.EnsureIssueLabel(ctx, labelName); err != nil {
			return fmt.Errorf("ensure %s label: %w", labelName, err)
		}
	}
	return nil
}

type orchestratorTracker interface {
	EnsureIssueLabel(context.Context, string) error
}

func (s *Service) funnelSetupStatus(ctx context.Context) domain.FunnelSetupStatus {
	runtime := s.currentRuntime()
	inspector := s.inspector
	if inspector == nil {
		inspector = tsdiag.NewInspector()
	}
	return inspector.Check(ctx, tsdiag.Options{
		UIPort:                   runtime.Config.Server.Port,
		LocalUIBaseURL:           s.DashboardURL(),
		WebhookPort:              runtime.Config.Server.WebhookPort,
		LocalWebhookBaseURL:      s.webhookBaseURL(),
		ExplicitWebhookPublicURL: webhookPublicURL(runtime.Config.Server),
	})
}

func (s *Service) effectiveWebhookPublicBaseURL(ctx context.Context, cfg domain.ServerConfig) string {
	return resolveWebhookPublicBaseURL(ctx, s.inspector, cfg, s.webhookBaseURL())
}

func (s *Service) effectiveUIBaseURL(ctx context.Context, cfg domain.ServerConfig) string {
	if value := strings.TrimSpace(cfg.UIURL); value != "" {
		return value
	}
	inspector := s.inspector
	if inspector == nil {
		inspector = tsdiag.NewInspector()
	}
	if value := strings.TrimSpace(inspector.ResolveUIBaseURL(ctx, cfg.Port)); value != "" {
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

func watchedProjectIDs(client tracker.RuntimeMetadata) []string {
	if client == nil {
		return nil
	}
	return client.WatchedProjectIDs()
}

func shouldQueueImmediateLinearRefresh(event app.LinearWebhookEvent, watchedProjectIDs []string) bool {
	if len(watchedProjectIDs) == 0 {
		return false
	}
	resourceType := strings.ToLower(strings.TrimSpace(event.ResourceType))
	switch resourceType {
	case "issue":
		projectID := strings.TrimSpace(event.ProjectID)
		if projectID == "" || !matchesWatchedProject(projectID, watchedProjectIDs) {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(event.Action)) {
		case "create":
			return true
		case "update":
			return hasRelevantIssueChange(event.ChangedFields)
		default:
			return false
		}
	case "issuelabel":
		projectID := strings.TrimSpace(event.ProjectID)
		if projectID != "" && !matchesWatchedProject(projectID, watchedProjectIDs) {
			return false
		}
		if strings.TrimSpace(event.IssueID) == "" {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(event.Action)) {
		case "create", "update", "remove":
			return true
		default:
			return false
		}
	case "agentsessionevent":
		if strings.TrimSpace(event.IssueID) == "" {
			return false
		}
		projectID := strings.TrimSpace(event.ProjectID)
		if projectID != "" && !matchesWatchedProject(projectID, watchedProjectIDs) {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(event.Action)) {
		case "created", "prompted":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func watchedRepositoryFullNames(cfg domain.ServiceConfig) []string {
	adapter, err := repohost.Lookup(cfg.Repo.Backend)
	if err != nil {
		return nil
	}
	repos := make([]string, 0, len(cfg.Targets))
	seen := map[string]struct{}{}
	for _, repoURL := range cfg.WatchedRepoURLs() {
		repo, err := adapter.ParseRepositoryURL(repoURL)
		if err != nil {
			continue
		}
		if strings.TrimSpace(repo.Owner) == "" || strings.TrimSpace(repo.Name) == "" {
			continue
		}
		fullName := normalizeRepositoryFullName(repo.Owner + "/" + repo.Name)
		if _, ok := seen[fullName]; ok {
			continue
		}
		seen[fullName] = struct{}{}
		repos = append(repos, fullName)
	}
	return repos
}

func watchedRepositoryFullName(cfg domain.ServiceConfig) string {
	repos := watchedRepositoryFullNames(cfg)
	if len(repos) == 0 {
		return ""
	}
	return repos[0]
}

func shouldQueueImmediateGitHubRefresh(event app.GitHubWebhookEvent, watchedRepos []string) bool {
	if !event.Relevant || len(watchedRepos) == 0 {
		return false
	}
	repoName := normalizeRepositoryFullName(event.RepositoryFullName)
	matched := false
	for _, watchedRepo := range watchedRepos {
		if repoName == normalizeRepositoryFullName(watchedRepo) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	return true
}

func normalizeRepositoryFullName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func hasRelevantIssueChange(changedFields []string) bool {
	for _, field := range changedFields {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "stateid", "projectid", "teamid", "priority", "title", "description", "branchname", "labelids", "delegateid":
			return true
		}
	}
	return false
}

func containsChangedField(changedFields []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return false
	}
	for _, field := range changedFields {
		if strings.EqualFold(strings.TrimSpace(field), want) {
			return true
		}
	}
	return false
}

func matchesWatchedProject(projectID string, watchedProjectIDs []string) bool {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false
	}
	for _, watchedProjectID := range watchedProjectIDs {
		if strings.EqualFold(projectID, strings.TrimSpace(watchedProjectID)) {
			return true
		}
	}
	return false
}

func (s *Service) applyUIBaseURLResolver(runtime orchestrator.Runtime) {
	client := runtime.Tracker
	if client == nil {
		return
	}
	client.SetUIBaseURLResolver(func(ctx context.Context) string {
		return s.effectiveUIBaseURL(ctx, runtime.Config.Server)
	})
}

// NewDefaultLogger returns the repo-default structured logger.
func NewDefaultLogger(verbose bool) *slog.Logger {
	return NewDefaultLoggerForWriter(os.Stderr, verbose)
}

// NewDefaultLoggerForWriter returns the repo-default structured logger writing to w.
func NewDefaultLoggerForWriter(w io.Writer, verbose bool) *slog.Logger {
	if w == nil {
		w = io.Discard
	}
	return newLogger(w, verbose)
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
	case errors.Is(err, ErrMissingGitHubRepository):
		return fmt.Sprintf("github repository not found: %v", err)
	case errors.Is(err, repohost.ErrUnsupportedRepositoryURL):
		return fmt.Sprintf("unsupported repository URL: %v", err)
	default:
		return err.Error()
	}
}
