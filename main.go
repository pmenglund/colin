package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/service"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cmd := newRootCmd(stdout, stderr)
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
				_, _ = fmt.Fprintln(stderr, usageErr.Err)
			}
			usageErr.Command.SetOut(stderr)
			_ = usageErr.Command.Usage()
			return 2
		}

		_, _ = fmt.Fprintln(stderr, err)
		cmd.SetOut(stderr)
		_ = cmd.Usage()
		return 2
	}

	return 0
}

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	var port int
	var verbose bool

	cmd := &cobra.Command{
		Use:               "colin [path-to-WORKFLOW.md]",
		Short:             "Run the Colin service",
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		Args:              maximumArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(runRoot(cmd.Context(), stdout, stderr, args, port, verbose))
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetFlagErrorFunc(wrapFlagError)
	cmd.Flags().IntVar(&port, "port", -1, "dashboard port override; default is 8888")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "print structured service logs")

	setupCmd := &cobra.Command{
		Use:           "setup",
		Short:         "Run setup helpers",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return &usageError{Command: cmd}
		},
	}
	setupCmd.SetOut(stdout)
	setupCmd.SetErr(stderr)
	setupCmd.SetFlagErrorFunc(wrapFlagError)

	var jsonOutput bool
	setupFunnelCmd := &cobra.Command{
		Use:           "funnel [path-to-WORKFLOW.md]",
		Short:         "Inspect Funnel readiness",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          maximumArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitCode(runSetupFunnel(stdout, stderr, args, jsonOutput))
		},
	}
	setupFunnelCmd.SetOut(stdout)
	setupFunnelCmd.SetErr(stderr)
	setupFunnelCmd.SetFlagErrorFunc(wrapFlagError)
	setupFunnelCmd.Flags().BoolVar(&jsonOutput, "json", false, "print JSON output")

	setupCmd.AddCommand(setupFunnelCmd)
	cmd.AddCommand(setupCmd)

	return cmd
}

func runRoot(ctx context.Context, stdout, stderr io.Writer, args []string, port int, verbose bool) int {
	workflowPath := ""
	if len(args) == 1 {
		workflowPath = args[0]
	}

	var options []service.Option
	if port >= 0 {
		options = append(options, service.WithServerPortOverride(port))
	}

	logger := service.NewDefaultLogger(verbose)
	svc, err := service.New(logger, workflowPath, options...)
	if err != nil {
		logger.Error("startup failed", "error", service.DescribeStartupError(err))
		return 1
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- svc.Run(ctx)
	}()

	if !verbose {
		exited, err := announceStartup(stdout, svc.DashboardEnabled(), svc.DashboardURL, svc.FunnelSetupURL, runErrCh)
		if exited {
			if err != nil {
				logger.Error("service exited abnormally", "error", err)
				return 1
			}
			return 0
		}
	}

	if err := <-runErrCh; err != nil {
		logger.Error("service exited abnormally", "error", err)
		return 1
	}

	return 0
}

func runSetupFunnel(stdout, stderr io.Writer, args []string, jsonOutput bool) int {
	workflowPath := ""
	if len(args) == 1 {
		workflowPath = args[0]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, err := service.LoadFunnelSetupStatus(ctx, workflowPath)
	if err != nil {
		fmt.Fprintln(stderr, service.DescribeStartupError(err))
		return 1
	}

	if jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(status); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		renderSetupStatus(stdout, status)
	}

	if status.Ready {
		return 0
	}
	return 1
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

func announceStartup(stdout io.Writer, dashboardEnabled bool, dashboardURL func() string, setupURL func() string, runErrCh <-chan error) (bool, error) {
	if !dashboardEnabled {
		_, _ = fmt.Fprintln(stdout, "Colin is running.")
		return false, nil
	}

	if url := dashboardURL(); url != "" {
		_, _ = fmt.Fprintf(stdout, "Colin is running. Web UI: %s Setup: %s\n", url, setupURL())
		return false, nil
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-runErrCh:
			return true, err
		case <-ticker.C:
			if url := dashboardURL(); url != "" {
				_, _ = fmt.Fprintf(stdout, "Colin is running. Web UI: %s Setup: %s\n", url, setupURL())
				return false, nil
			}
		}
	}
}

func renderSetupStatus(stdout io.Writer, status domain.FunnelSetupStatus) {
	fmt.Fprintf(stdout, "Funnel ready: %t\n", status.Ready)
	if status.LocalBaseURL != "" {
		fmt.Fprintf(stdout, "Local URL: %s\n", status.LocalBaseURL)
	}
	if status.PublicBaseURL != "" {
		fmt.Fprintf(stdout, "Public URL: %s\n", status.PublicBaseURL)
	}
	if status.LinearWebhookURL != "" {
		fmt.Fprintf(stdout, "Linear webhook URL: %s\n", status.LinearWebhookURL)
	}
	if status.GitHubWebhookURL != "" {
		fmt.Fprintf(stdout, "GitHub webhook URL: %s\n", status.GitHubWebhookURL)
	}
	if status.SuggestedCommand != "" {
		fmt.Fprintf(stdout, "Suggested command: %s\n", status.SuggestedCommand)
	}
	fmt.Fprintln(stdout, "Checks:")
	for _, check := range status.Checks {
		line := check.Detail
		if line == "" {
			line = check.Remediation
		} else if check.Remediation != "" {
			line += " " + check.Remediation
		}
		fmt.Fprintf(stdout, "- [%s] %s", strings.ToUpper(check.Status), check.Label)
		if line != "" {
			fmt.Fprintf(stdout, ": %s", line)
		}
		fmt.Fprintln(stdout)
	}
}
