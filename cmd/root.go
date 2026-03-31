package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

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
	runSetupTailscale     func(*cobra.Command, string, bool) int
	runSetupLinearWebhook func(*cobra.Command, string, string) int
}

type configOptions struct {
	workflowPath string
	autoStart    bool
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
		workflowPath: "WORKFLOW.md",
		port:         -1,
	}

	cmd := &cobra.Command{
		Use:               "colin",
		Short:             "Run the Colin service",
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		Args:              maximumArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if shouldRunConfig(opts.workflowPath, cmd.Flags().Changed("workflow")) {
				cmd.Printf("%s was not found. Starting first-run setup.\n", workflow.Loader{}.ResolvePath(opts.workflowPath))
				if code := deps.runConfig(cmd, configOptions{
					workflowPath: opts.workflowPath,
					autoStart:    true,
				}); code != 0 {
					return exitCode(code)
				}
			}
			return exitCode(deps.runRoot(cmd, *opts))
		},
	}
	configureCommand(cmd, stdin, stdout, stderr)
	cmd.PersistentFlags().StringVar(&opts.workflowPath, "workflow", opts.workflowPath, "path to workflow file")
	cmd.Flags().IntVar(&opts.port, "port", opts.port, "dashboard port override; uses the workflow file setting when unset")
	if flag := cmd.Flags().Lookup("port"); flag != nil {
		flag.DefValue = "workflow file setting"
	}
	cmd.Flags().BoolVar(&opts.verbose, "verbose", false, "print structured service logs")
	cmd.AddCommand(newConfigCmd(stdin, stdout, stderr, opts, deps))
	cmd.AddCommand(newSetupCmd(stdin, stdout, stderr, opts, deps))

	return cmd
}

func defaultCommandDeps() commandDeps {
	return commandDeps{
		runRoot:               runRoot,
		runConfig:             runConfig,
		runSetupTailscale:     runSetupTailscale,
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

func shouldRunConfig(workflowPath string, explicitWorkflowPath bool) bool {
	if explicitWorkflowPath {
		return false
	}
	path := workflow.Loader{}.ResolvePath(workflowPath)
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}
