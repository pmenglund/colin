package cmd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/service"
	"github.com/pmenglund/colin/internal/tui"
)

var (
	setupStatusOKStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	setupStatusErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	setupStatusWarnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

type runtimeService interface {
	Run(context.Context) error
	DashboardEnabled() bool
	DashboardURL() string
	FunnelSetupURL() string
	Snapshot(context.Context) (domain.Snapshot, error)
	BufferedLogs(context.Context, *slog.Level) (domain.BufferedLogSnapshot, error)
	FunnelSetupStatus(context.Context) domain.FunnelSetupStatus
}

var (
	newRuntimeService = func(logger *slog.Logger, workflowPath string, options ...service.Option) (runtimeService, error) {
		return service.New(logger, workflowPath, options...)
	}
	runRuntimeTUI = func(ctx context.Context, in io.Reader, out io.Writer, source runtimeService, serviceErrCh <-chan error, stop func()) error {
		return tui.Run(ctx, in, out, source, serviceErrCh, stop)
	}
	runtimeIsInteractiveTerminal = isInteractiveTerminal
)

func runRoot(cmd *cobra.Command, opts rootOptions) int {
	var options []service.Option
	if opts.port >= 0 {
		options = append(options, service.WithServerPortOverride(opts.port))
	}

	logger := service.NewDefaultLogger(opts.verbose)
	svc, err := newRuntimeService(logger, opts.workflowPath, options...)
	if err != nil {
		logger.Error("startup failed", "error", service.DescribeStartupError(err))
		return 1
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- svc.Run(ctx)
	}()

	if opts.verbose {
		if err := <-runErrCh; err != nil {
			logger.Error("service exited abnormally", "error", err)
			return 1
		}
		return 0
	}

	if runtimeIsInteractiveTerminal(cmd.InOrStdin(), cmd.OutOrStdout()) {
		if err := runRuntimeTUI(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), svc, runErrCh, stop); err != nil {
			logger.Error("service exited abnormally", "error", err)
			return 1
		}
		return 0
	}

	exited, err := announceStartup(cmd, svc.DashboardEnabled(), svc.DashboardURL, svc.FunnelSetupURL, runErrCh)
	if exited {
		if err != nil {
			logger.Error("service exited abnormally", "error", err)
			return 1
		}
		return 0
	}

	if err := <-runErrCh; err != nil {
		logger.Error("service exited abnormally", "error", err)
		return 1
	}

	return 0
}

func runSetupTailscale(cmd *cobra.Command, workflowPath string, jsonOutput bool) int {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	status, err := service.LoadFunnelSetupStatus(ctx, workflowPath)
	if err != nil {
		cmd.PrintErrln(service.DescribeStartupError(err))
		return 1
	}

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(status); err != nil {
			cmd.PrintErrln(err)
			return 1
		}
	} else {
		renderSetupStatus(cmd, status)
	}

	if status.Ready {
		return 0
	}
	return 1
}

func announceStartup(cmd *cobra.Command, dashboardEnabled bool, dashboardURL func() string, setupURL func() string, runErrCh <-chan error) (bool, error) {
	if !dashboardEnabled {
		cmd.Println("Colin is running.")
		return false, nil
	}

	if url := dashboardURL(); url != "" {
		cmd.Printf("Colin is running. Web UI: %s Setup: %s\n", url, setupURL())
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
				cmd.Printf("Colin is running. Web UI: %s Setup: %s\n", url, setupURL())
				return false, nil
			}
		}
	}
}

func renderSetupStatus(cmd *cobra.Command, status domain.FunnelSetupStatus) {
	cmd.Printf("Funnel ready: %t\n", status.Ready)
	if status.LocalBaseURL != "" {
		cmd.Printf("Local URL: %s\n", status.LocalBaseURL)
	}
	if status.PublicBaseURL != "" {
		cmd.Printf("Public URL: %s\n", status.PublicBaseURL)
	}
	if status.LinearWebhookURL != "" {
		cmd.Printf("Linear webhook URL: %s\n", status.LinearWebhookURL)
	}
	if status.GitHubWebhookURL != "" {
		cmd.Printf("GitHub webhook URL: %s\n", status.GitHubWebhookURL)
	}
	if status.SuggestedCommand != "" {
		cmd.Printf("Suggested command: %s\n", status.SuggestedCommand)
	}
	cmd.Println("Checks:")
	for _, check := range status.Checks {
		line := check.Detail
		if line == "" {
			line = check.Remediation
		} else if check.Remediation != "" {
			line += " " + check.Remediation
		}
		cmd.Printf("- %s %s", renderSetupCheckStatus(check.Status), check.Label)
		if line != "" {
			cmd.Printf(": %s", line)
		}
		cmd.Println()
	}
}

func renderSetupCheckStatus(status string) string {
	label := "[" + strings.ToUpper(status) + "]"

	switch strings.ToLower(status) {
	case "ok":
		return setupStatusOKStyle.Render(label)
	case "error":
		return setupStatusErrorStyle.Render(label)
	case "warn":
		return setupStatusWarnStyle.Render(label)
	default:
		return label
	}
}
