package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/pmenglund/colin/internal/codexexec"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/githubapp"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/logging"
	"github.com/pmenglund/colin/internal/worker"
	"github.com/pmenglund/colin/internal/workflow"
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
	cfg, err := loadCLIConfig(rootOpts)
	if err != nil {
		return err
	}
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
	if cfg.LinearBackend == config.LinearBackendHTTP {
		admin := linear.NewWorkflowStateAdmin(cfg.LinearBaseURL, cfg.LinearAPIToken, cfg.LinearTeamID, nil)
		resolved, err := admin.ResolveWorkflowStates(cmd.Context(), runtimeStates)
		if err != nil {
			return fmt.Errorf("resolve workflow states: %w; run `colin setup` to create/validate mapped states", err)
		}
		runtimeStates = resolved.RuntimeStates()
		configureLinearRuntimeState(client, runtimeStates, resolved.StateIDByName())
	}

	executor := newInProgressExecutor(cfg, cwd, cmd.ErrOrStderr(), noColor)
	githubTokenProvider, err := newGitHubTokenProvider(cfg)
	if err != nil {
		return err
	}
	mergeExecutor := newMergeExecutor(cfg, cwd, cmd.ErrOrStderr(), runtimeStates, noColor, githubTokenProvider)
	pullRequestManager := newPullRequestManager(cfg, cwd, githubTokenProvider)
	bootstrapper := newTaskBootstrapper(cfg, cwd)

	runner := &worker.Runner{
		Linear:             client,
		Executor:           executor,
		MergeExecutor:      mergeExecutor,
		PullRequestManager: pullRequestManager,
		RequirePullRequest: cfg.LinearBackend == config.LinearBackendHTTP,
		Bootstrapper:       bootstrapper,
		TeamID:             cfg.LinearTeamID,
		ProjectFilter:      cfg.ProjectFilter,
		WorkerID:           cfg.WorkerID,
		PollEvery:          cfg.PollEvery,
		LeaseTTL:           cfg.LeaseTTL,
		MaxConcurrency:     cfg.MaxConcurrency,
		DryRun:             cfg.DryRun,
		States:             runtimeStates,
		Logger:             logging.NewSlog(cmd.ErrOrStderr(), logging.LevelInfo, noColor),
	}

	if opts.once {
		return runner.RunOnce(cmd.Context())
	}

	return runner.Run(cmd.Context())
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

func newInProgressExecutor(cfg config.Config, cwd string, stderr io.Writer, noColor bool) worker.InProgressExecutor {
	if cfg.LinearBackend == config.LinearBackendFake {
		// Keep fake backend fully local/offline by skipping Codex execution paths.
		return nil
	}

	codexLogger := logging.NewSlog(stderr, logging.LevelInfo, noColor)
	return codexexec.New(codexexec.Options{
		Cwd:            cwd,
		Logger:         codexLogger,
		WorkPromptPath: cfg.WorkPromptPath,
	})
}

func newMergeExecutor(
	cfg config.Config,
	cwd string,
	stderr io.Writer,
	states workflow.States,
	noColor bool,
	tokenProvider worker.GitHubTokenProvider,
) worker.MergeExecutor {
	if cfg.LinearBackend == config.LinearBackendFake {
		return worker.NoopMergeExecutor{}
	}

	codexLogger := logging.NewSlog(stderr, logging.LevelInfo, noColor)
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
		RepoRoot:   cwd,
		ColinHome:  cfg.ColinHome,
		BaseBranch: cfg.BaseBranch,
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
