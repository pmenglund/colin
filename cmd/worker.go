package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/pmenglund/colin/internal/codexexec"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/logging"
	"github.com/pmenglund/colin/internal/worker"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/spf13/cobra"
)

func newWorkerCommand(rootOpts *RootOptions) *cobra.Command {
	workerCmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the Linear issue worker",
	}

	runOpts := &workerRunOptions{}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run worker reconciliation",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorker(cmd, rootOpts, *runOpts)
		},
	}

	addWorkerRunFlags(runCmd, runOpts)

	workerCmd.AddCommand(runCmd)
	return workerCmd
}

type workerRunOptions struct {
	once   bool
	dryRun bool
}

func addWorkerRunFlags(cmd *cobra.Command, opts *workerRunOptions) {
	cmd.Flags().BoolVar(&opts.once, "once", false, "Run one poll cycle and exit")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Log decisions without writing to Linear")
}

func runWorker(cmd *cobra.Command, rootOpts *RootOptions, opts workerRunOptions) error {
	configPath := config.DefaultConfigPath
	if rootOpts != nil {
		configPath = rootOpts.ConfigPath
	}

	cfg, err := config.LoadFromPath(configPath)
	if err != nil {
		return err
	}

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

	executor := newInProgressExecutor(cfg, cwd, cmd.ErrOrStderr())
	mergeExecutor := newMergeExecutor(cfg, cwd, cmd.ErrOrStderr(), runtimeStates)
	pullRequestManager := newPullRequestManager(cfg, cwd)
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
		Logger:             logging.NewSlog(cmd.ErrOrStderr(), logging.LevelInfo),
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

func newInProgressExecutor(cfg config.Config, cwd string, stderr io.Writer) worker.InProgressExecutor {
	if cfg.LinearBackend == config.LinearBackendFake {
		// Keep fake backend fully local/offline by skipping Codex execution paths.
		return nil
	}

	codexLogger := logging.NewSlog(stderr, logging.LevelInfo)
	return codexexec.New(codexexec.Options{
		Cwd:            cwd,
		Logger:         codexLogger,
		WorkPromptPath: cfg.WorkPromptPath,
	})
}

func newMergeExecutor(cfg config.Config, cwd string, stderr io.Writer, states workflow.States) worker.MergeExecutor {
	if cfg.LinearBackend == config.LinearBackendFake {
		return worker.NoopMergeExecutor{}
	}

	codexLogger := logging.NewSlog(stderr, logging.LevelInfo)
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
		MergePreparer:  mergePreparer,
		States:         states,
	})
}

func newPullRequestManager(cfg config.Config, cwd string) worker.PullRequestManager {
	if cfg.LinearBackend == config.LinearBackendFake {
		return nil
	}

	return worker.NewGitPullRequestManager(worker.GitPullRequestManagerOptions{
		RepoRoot:   cwd,
		BaseBranch: cfg.BaseBranch,
		RemoteName: "origin",
		Binary:     "gh",
	})
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
