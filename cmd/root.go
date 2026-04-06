package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/repohost/builtin"
	"github.com/pmenglund/colin/internal/workflow"
)

type rootOptions struct {
	workflowPath string
	port         int
	verbose      bool
}

type commandDeps struct {
	runRoot               func(*cobra.Command, rootOptions) int
	runConfig             func(*cobra.Command, configOptions) int
	runResume             func(*cobra.Command, resumeOptions) int
	runSetupRepo          func(*cobra.Command, string) int
	runSetupSlack         func(*cobra.Command, string) int
	runSetupGitHub        func(*cobra.Command, string) int
	runSetupGitHubWebhook func(*cobra.Command, string) int
	runSetupTailscale     func(*cobra.Command, string, bool) int
	runSetupLinearApp     func(*cobra.Command, string) int
	runSetupLinearWebhook func(*cobra.Command, string, string) int
	isInteractive         func(*cobra.Command) bool
}

type configOptions struct {
	workflowPath string
	autoStart    bool
}

type resumeOptions struct {
	workflowPath string
	target       string
}

type usageError struct {
	Command *cobra.Command
	Err     error
}

func (e *usageError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "invalid command usage"
}

func (e *usageError) Unwrap() error {
	return e.Err
}

type commandExitError struct {
	Code int
}

func (e *commandExitError) Error() string {
	return fmt.Sprintf("command exited with status %d", e.Code)
}

// Execute runs the Colin CLI and returns the process exit code.
func Execute(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	builtin.Register()
	return run(args, stdin, stdout, stderr, defaultCommandDeps())
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, deps commandDeps) int {
	cmd := newRootCmd(stdin, stdout, stderr, deps)
	cmd.SetContext(context.Background())
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		var exitErr *commandExitError
		if errors.As(err, &exitErr) {
			return exitErr.Code
		}

		var usageErr *usageError
		if errors.As(err, &usageErr) {
			if usageErr.Err != nil {
				usageErr.Command.PrintErrln(usageErr.Err)
			}
			usageErr.Command.SetOut(usageErr.Command.ErrOrStderr())
			_ = usageErr.Command.Usage()
			return 2
		}

		cmd.PrintErrln(err)
		cmd.SetOut(cmd.ErrOrStderr())
		_ = cmd.Usage()
		return 2
	}

	return 0
}

func newRootCmd(stdin io.Reader, stdout, stderr io.Writer, deps commandDeps) *cobra.Command {
	opts := &rootOptions{
		port: -1,
	}

	cmd := &cobra.Command{
		Use:               "colin",
		Short:             "Run the Colin service",
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		Args:              maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedWorkflowPath := opts.resolvedWorkflowPath()
			if shouldRunConfig(resolvedWorkflowPath) {
				if !isInteractiveCommand(cmd, deps) {
					cmd.PrintErrln(missingWorkflowMessage(resolvedWorkflowPath))
					return exitCode(1)
				}
				cmd.Printf("%s was not found. Starting first-run setup.\n", resolvedWorkflowPath)
				if code := deps.runConfig(cmd, configOptions{
					workflowPath: resolvedWorkflowPath,
					autoStart:    true,
				}); code != 0 {
					return exitCode(code)
				}
			}
			effectiveOpts := *opts
			effectiveOpts.workflowPath = resolvedWorkflowPath
			return exitCode(deps.runRoot(cmd, effectiveOpts))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.PersistentFlags().StringVar(&opts.workflowPath, "workflow", opts.workflowPath, "path to workflow file")
	if flag := cmd.PersistentFlags().Lookup("workflow"); flag != nil {
		flag.DefValue = "$" + workflow.WorkflowPathEnvVar + " or " + workflow.DefaultPath
	}
	cmd.Flags().IntVar(&opts.port, "port", opts.port, "dashboard port override; uses the workflow file setting when unset")
	if flag := cmd.Flags().Lookup("port"); flag != nil {
		flag.DefValue = "workflow file setting"
	}
	cmd.Flags().BoolVar(&opts.verbose, "verbose", false, "print structured service logs")
	cmd.AddCommand(newConfigCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newResumeCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupCmd(stdin, stdout, stderr, opts, deps))

	return cmd
}

func defaultCommandDeps() commandDeps {
	return commandDeps{
		runRoot:               runRoot,
		runConfig:             runConfig,
		runResume:             runResume,
		runSetupRepo:          runSetupRepo,
		runSetupSlack:         runSetupSlack,
		runSetupGitHub:        runSetupGitHub,
		runSetupGitHubWebhook: runSetupGitHubWebhook,
		runSetupTailscale:     runSetupTailscale,
		runSetupLinearApp:     runSetupLinearApp,
		runSetupLinearWebhook: runSetupLinearWebhook,
	}
}

func configureCommand(cmd *cobra.Command, stdin io.Reader, stdout, stderr io.Writer) {
	if stdin != nil {
		cmd.SetIn(stdin)
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetFlagErrorFunc(wrapFlagError)
}

func exitCode(code int) error {
	if code == 0 {
		return nil
	}
	return &commandExitError{Code: code}
}

func wrapFlagError(cmd *cobra.Command, err error) error {
	return &usageError{Command: cmd, Err: err}
}

func maximumArgs(limit int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= limit {
			return nil
		}
		return &usageError{
			Command: cmd,
			Err:     fmt.Errorf("accepts at most %d arg(s), received %d", limit, len(args)),
		}
	}
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == count {
			return nil
		}
		return &usageError{
			Command: cmd,
			Err:     fmt.Errorf("accepts exactly %d arg(s), received %d", count, len(args)),
		}
	}
}

func shouldRunConfig(resolvedWorkflowPath string) bool {
	_, err := os.Stat(resolvedWorkflowPath)
	return errors.Is(err, os.ErrNotExist)
}

func isInteractiveCommand(cmd *cobra.Command, deps commandDeps) bool {
	if deps.isInteractive != nil {
		return deps.isInteractive(cmd)
	}
	return isInteractiveTerminal(cmd.InOrStdin(), cmd.OutOrStdout())
}

func isInteractiveTerminal(in io.Reader, out io.Writer) bool {
	return isTerminalStream(in) && isTerminalStream(out)
}

func isTerminalStream(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (o rootOptions) resolvedWorkflowPath() string {
	return workflow.Loader{}.ResolvePath(o.workflowPath)
}

func missingWorkflowMessage(resolvedWorkflowPath string) string {
	return fmt.Sprintf("workflow file not found: %s. Run `colin --workflow %s config` from an interactive terminal to create it.", resolvedWorkflowPath, resolvedWorkflowPath)
}
