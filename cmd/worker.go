package cmd

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/pmenglund/colin/internal/codexexec"
	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/worker"
	"github.com/spf13/cobra"
)

func newWorkerCommand(rootOpts *RootOptions) *cobra.Command {
	workerCmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the Linear issue worker",
	}

	var once bool
	var dryRun bool

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run worker reconciliation",
		RunE: func(cmd *cobra.Command, _ []string) error {
			configPath := config.DefaultConfigPath
			if rootOpts != nil {
				configPath = rootOpts.ConfigPath
			}

			cfg, err := config.LoadFromPath(configPath)
			if err != nil {
				return err
			}

			if cmd.Flags().Changed("dry-run") {
				cfg.DryRun = dryRun
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			client, err := newLinearClient(cfg)
			if err != nil {
				return err
			}
			executor := newInProgressExecutor(cfg, cwd, cmd.ErrOrStderr())
			bootstrapper := newTaskBootstrapper(cfg, cwd)

			runner := &worker.Runner{
				Linear:         client,
				Executor:       executor,
				Bootstrapper:   bootstrapper,
				TeamID:         cfg.LinearTeamID,
				WorkerID:       cfg.WorkerID,
				PollEvery:      cfg.PollEvery,
				LeaseTTL:       cfg.LeaseTTL,
				MaxConcurrency: cfg.MaxConcurrency,
				DryRun:         cfg.DryRun,
				Logger:         slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo})),
			}

			if once {
				return runner.RunOnce(cmd.Context())
			}

			return runner.Run(cmd.Context())
		},
	}

	runCmd.Flags().BoolVar(&once, "once", false, "Run one poll cycle and exit")
	runCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Log decisions without writing to Linear")

	workerCmd.AddCommand(runCmd)
	return workerCmd
}

func newLinearClient(cfg config.Config) (linear.Client, error) {
	switch cfg.LinearBackend {
	case config.LinearBackendHTTP:
		return linear.NewHTTPClient(cfg.LinearBaseURL, cfg.LinearAPIToken, cfg.LinearTeamID, nil), nil
	case config.LinearBackendFake:
		return linear.NewDefaultInMemoryClient(), nil
	default:
		return nil, fmt.Errorf("unsupported linear backend %q", cfg.LinearBackend)
	}
}

func newInProgressExecutor(cfg config.Config, cwd string, stderr io.Writer) worker.InProgressExecutor {
	if cfg.LinearBackend == config.LinearBackendFake {
		// Keep fake backend fully local/offline by skipping Codex execution paths.
		return nil
	}

	codexLogger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return codexexec.New(codexexec.Options{
		Cwd:    cwd,
		Logger: codexLogger,
	})
}

func newTaskBootstrapper(cfg config.Config, cwd string) worker.TaskBootstrapper {
	if cfg.LinearBackend == config.LinearBackendFake {
		// Keep fake backend fully local/offline by skipping git workspace side effects.
		return nil
	}

	return worker.NewGitTaskBootstrapper(worker.GitTaskBootstrapperOptions{
		RepoRoot:  cwd,
		ColinHome: cfg.ColinHome,
	})
}
