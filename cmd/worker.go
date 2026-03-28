package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/pmenglund/colin/internal/codexexec"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/execution"
	"github.com/pmenglund/colin/internal/githubapp"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/logging"
	"github.com/pmenglund/colin/internal/orchestrator"
	"github.com/pmenglund/colin/internal/worker"
	"github.com/pmenglund/colin/internal/workflow"
	workspacepkg "github.com/pmenglund/colin/internal/workspace"
	"github.com/spf13/cobra"
)

type workerRunOptions struct {
	once   bool
	dryRun bool
}

func addWorkerRunFlags(cmd *cobra.Command, opts *workerRunOptions) {
	cmd.Flags().BoolVar(&opts.once, "once", false, "Run one poll cycle and exit")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Log decisions without writing to Linear")
}

func runWorker(cmd *cobra.Command, rootOpts *RootOptions, opts workerRunOptions) error {
	provider, err := loadCLIConfigProvider(rootOpts)
	if err != nil {
		return err
	}
	cfg := provider.Current()
	noColor := rootOpts != nil && rootOpts.NoColor

	if cmd.Flags().Changed("dry-run") {
		cfg.DryRun = opts.dryRun
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	runtimeStates := cfg.WorkflowStates.AsRuntimeStates()
	client := newLinearClient(cfg, runtimeStates)
	logLevel := rootLogLevel(rootOpts)
	if cfg.LinearBackend == config.LinearBackendHTTP {
		admin := linear.NewWorkflowStateAdmin(cfg.LinearBaseURL, cfg.LinearAPIToken, cfg.LinearTeamID, nil)
		resolved, err := admin.ResolveWorkflowStates(cmd.Context(), runtimeStates)
		if err != nil {
			return fmt.Errorf("resolve workflow states: %w; run `colin setup` to create/validate mapped states", err)
		}
		runtimeStates = resolved.RuntimeStates()
		configureLinearRuntimeState(client, runtimeStates, resolved.StateIDByName())
	}

	executor := newInProgressExecutor(cfg, cwd, cmd.ErrOrStderr(), noColor, logLevel)
	githubTokenProvider, err := newGitHubTokenProvider(cfg)
	if err != nil {
		return err
	}
	mergeExecutor := newMergeExecutor(cfg, cwd, cmd.ErrOrStderr(), runtimeStates, noColor, githubTokenProvider, logLevel)
	pullRequestManager := newPullRequestManager(cfg, cwd, githubTokenProvider)
	bootstrapper := newTaskBootstrapper(cfg, cwd)
	_ = mergeExecutor
	_ = pullRequestManager
	logger := logging.NewSlog(cmd.ErrOrStderr(), logLevel, noColor)
	workspaceManager, err := newWorkspaceManager(cfg, bootstrapper, logger)
	if err != nil {
		return err
	}
	runtime := &workerCompatibleRunner{
		cfg:      cfg,
		executor: executor,
		logger:   logger,
	}
	service, err := orchestrator.New(orchestrator.Options{
		Tracker:    linearOrchestratorClient{client: client},
		Configs:    provider,
		Runner:     runtime,
		Workspaces: workspaceManager,
		Logger:     logger,
	})
	if err != nil {
		return err
	}

	if opts.once {
		return service.RunOnce(cmd.Context())
	}

	return service.Run(cmd.Context())
}

func newLinearClient(cfg config.Config, states workflow.States) linear.Client {
	switch cfg.LinearBackend {
	case config.LinearBackendHTTP:
		client := linear.NewHTTPClient(cfg.LinearBaseURL, cfg.LinearAPIToken, cfg.LinearTeamID, nil)
		_ = client.SetWorkflowStates(states)
		return client
	case config.LinearBackendFake:
		client := linear.NewDefaultInMemoryClient()
		_ = client.SetWorkflowStates(states)
		return client
	}
	client := linear.NewHTTPClient(cfg.LinearBaseURL, cfg.LinearAPIToken, cfg.LinearTeamID, nil)
	_ = client.SetWorkflowStates(states)
	return client
}

func newInProgressExecutor(
	cfg config.Config,
	cwd string,
	stderr io.Writer,
	noColor bool,
	logLevel slog.Level,
) worker.InProgressExecutor {
	if cfg.LinearBackend == config.LinearBackendFake {
		// Keep fake backend fully local/offline by skipping Codex execution paths.
		return nil
	}

	codexLogger := logging.NewSlog(stderr, logLevel, noColor)
	return codexexec.New(codexexec.Options{
		Cwd:                cwd,
		Logger:             codexLogger,
		WorkPromptPath:     cfg.WorkPromptPath,
		WorkflowPromptBody: cfg.WorkflowPromptTemplate,
	})
}

func newMergeExecutor(
	cfg config.Config,
	cwd string,
	stderr io.Writer,
	states workflow.States,
	noColor bool,
	tokenProvider worker.GitHubTokenProvider,
	logLevel slog.Level,
) worker.MergeExecutor {
	if cfg.LinearBackend == config.LinearBackendFake {
		return worker.NoopMergeExecutor{}
	}

	codexLogger := logging.NewSlog(stderr, logLevel, noColor)
	mergePreparer := codexexec.NewMergePreparer(codexexec.Options{
		Cwd:             cwd,
		Logger:          codexLogger,
		MergePromptPath: cfg.MergePromptPath,
	})
	pushBaseBranch := cfg.PushAfterMerge

	return worker.NewGitMergeExecutor(worker.GitMergeExecutorOptions{
		RepoRoot:       cwd,
		BaseBranch:     cfg.BaseBranch,
		PushBaseBranch: &pushBaseBranch,
		TokenProvider:  tokenProvider,
		MergePreparer:  mergePreparer,
		States:         states,
	})
}

func rootLogLevel(rootOpts *RootOptions) slog.Level {
	if rootOpts != nil && rootOpts.Verbose {
		return slog.LevelDebug
	}
	return logging.LevelInfo
}

func newPullRequestManager(cfg config.Config, cwd string, tokenProvider worker.GitHubTokenProvider) worker.PullRequestManager {
	if cfg.LinearBackend == config.LinearBackendFake {
		return nil
	}

	return worker.NewGitPullRequestManager(worker.GitPullRequestManagerOptions{
		RepoRoot:      cwd,
		BaseBranch:    cfg.BaseBranch,
		RemoteName:    "origin",
		APIBaseURL:    cfg.GitHubAPIURL,
		TokenProvider: tokenProvider,
	})
}

func newGitHubTokenProvider(cfg config.Config) (worker.GitHubTokenProvider, error) {
	if cfg.LinearBackend == config.LinearBackendFake {
		return nil, nil
	}

	privateKey, err := cfg.ResolvedGitHubAppPrivateKey()
	if err != nil {
		return nil, err
	}

	provider, err := githubapp.NewInstallationTokenProvider(githubapp.InstallationTokenProviderOptions{
		AppID:          cfg.GitHubAppID,
		InstallationID: cfg.GitHubAppInstallationID,
		PrivateKeyPEM:  privateKey,
		APIBaseURL:     cfg.GitHubAPIURL,
	})
	if err != nil {
		return nil, fmt.Errorf("configure GitHub App installation token provider: %w", err)
	}
	return provider, nil
}

func newTaskBootstrapper(cfg config.Config, cwd string) worker.TaskBootstrapper {
	if cfg.LinearBackend == config.LinearBackendFake {
		// Keep fake backend fully local/offline by skipping git workspace side effects.
		return nil
	}

	return worker.NewGitTaskBootstrapper(worker.GitTaskBootstrapperOptions{
		RepoRoot:      cwd,
		ColinHome:     cfg.ColinHome,
		WorkspaceRoot: cfg.ResolvedWorkspaceRoot(),
		BaseBranch:    cfg.BaseBranch,
	})
}

func newWorkspaceManager(
	cfg config.Config,
	bootstrapper worker.TaskBootstrapper,
	logger *slog.Logger,
) (*workspacepkg.Manager, error) {
	var populator workspacepkg.Populator
	if bootstrapper != nil {
		populator = gitWorkspacePopulator{bootstrapper: bootstrapper}
	}
	return workspacepkg.New(workspacepkg.ManagerOptions{
		Root: cfg.ResolvedWorkspaceRoot(),
		Hooks: workspacepkg.HookConfig{
			AfterCreate:  cfg.Hooks.AfterCreate,
			BeforeRun:    cfg.Hooks.BeforeRun,
			AfterRun:     cfg.Hooks.AfterRun,
			BeforeRemove: cfg.Hooks.BeforeRemove,
			Timeout:      cfg.Hooks.Timeout,
		},
		Populator: populator,
		Logger:    logger,
	})
}

func configureLinearRuntimeState(client linear.Client, states workflow.States, stateIDs map[string]string) {
	type workflowStateSetter interface {
		SetWorkflowStates(states workflow.States) error
	}
	type stateIDSetter interface {
		SetStateIDs(stateIDs map[string]string)
	}

	if stateful, ok := client.(workflowStateSetter); ok {
		_ = stateful.SetWorkflowStates(states)
	}
	if cached, ok := client.(stateIDSetter); ok {
		cached.SetStateIDs(stateIDs)
	}
}

type linearOrchestratorClient struct {
	client linear.Client
}

func (c linearOrchestratorClient) ListCandidateIssues(ctx context.Context, teamID string) ([]linear.Issue, error) {
	return c.client.ListCandidateIssues(ctx, teamID)
}

func (c linearOrchestratorClient) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) (map[string]linear.Issue, error) {
	type fetcher interface {
		FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) (map[string]linear.Issue, error)
	}
	if typed, ok := c.client.(fetcher); ok {
		return typed.FetchIssueStatesByIDs(ctx, issueIDs)
	}
	return map[string]linear.Issue{}, nil
}

func (c linearOrchestratorClient) FetchIssuesByStates(ctx context.Context, teamID string, stateNames []string) ([]linear.Issue, error) {
	type fetcher interface {
		FetchIssuesByStates(ctx context.Context, teamID string, stateNames []string) ([]linear.Issue, error)
	}
	if typed, ok := c.client.(fetcher); ok {
		return typed.FetchIssuesByStates(ctx, teamID, stateNames)
	}
	return nil, nil
}

type gitWorkspacePopulator struct {
	bootstrapper worker.TaskBootstrapper
}

func (p gitWorkspacePopulator) Prepare(ctx context.Context, issueIdentifier string, workspacePath string) (map[string]string, error) {
	result, err := p.bootstrapper.EnsureTaskWorkspace(ctx, issueIdentifier)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(result.WorktreePath) != strings.TrimSpace(workspacePath) {
		return nil, fmt.Errorf("bootstrapper created %q, expected %q", result.WorktreePath, workspacePath)
	}
	return map[string]string{
		workflow.MetaWorktreePath: result.WorktreePath,
		workflow.MetaBranchName:   result.BranchName,
	}, nil
}

type workerCompatibleRunner struct {
	cfg      config.Config
	executor worker.InProgressExecutor
	logger   *slog.Logger
}

func (r *workerCompatibleRunner) RunAttempt(
	ctx context.Context,
	req execution.AttemptRequest,
	sink func(execution.SessionUpdate),
) (execution.AttemptResult, error) {
	if r.executor == nil {
		return execution.AttemptResult{Status: execution.AttemptStatusSucceeded}, nil
	}

	if streamed, ok := r.executor.(worker.StreamingInProgressExecutor); ok {
		result, err := streamed.EvaluateAndExecuteStreamed(ctx, req.Issue, sink)
		if err != nil {
			return execution.AttemptResult{Status: execution.AttemptStatusFailed}, err
		}
		return execution.AttemptResult{
			Status:               execution.AttemptStatusSucceeded,
			IsWellSpecified:      result.IsWellSpecified,
			NeedsInputSummary:    result.NeedsInputSummary,
			ExecutionSummary:     result.ExecutionSummary,
			ExecutionContext:     result.ExecutionContext,
			ThreadID:             result.ThreadID,
			ResumedFromThreadID:  result.ResumedFromThreadID,
			ResumeFallbackReason: result.ResumeFallbackReason,
			BeforeEvidenceRef:    result.BeforeEvidenceRef,
			AfterEvidenceRef:     result.AfterEvidenceRef,
		}, nil
	}

	result, err := r.executor.EvaluateAndExecute(ctx, req.Issue)
	if err != nil {
		return execution.AttemptResult{Status: execution.AttemptStatusFailed}, err
	}
	return execution.AttemptResult{
		Status:               execution.AttemptStatusSucceeded,
		IsWellSpecified:      result.IsWellSpecified,
		NeedsInputSummary:    result.NeedsInputSummary,
		ExecutionSummary:     result.ExecutionSummary,
		ExecutionContext:     result.ExecutionContext,
		ThreadID:             result.ThreadID,
		ResumedFromThreadID:  result.ResumedFromThreadID,
		ResumeFallbackReason: result.ResumeFallbackReason,
		BeforeEvidenceRef:    result.BeforeEvidenceRef,
		AfterEvidenceRef:     result.AfterEvidenceRef,
	}, nil
}
