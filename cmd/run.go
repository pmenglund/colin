package cmd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	"github.com/pmenglund/colin/internal/clioutput"
	"github.com/spf13/cobra"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/service"
	"github.com/pmenglund/colin/internal/tui"
)

type runtimeService interface {
	Run(context.Context) error
	RequestShutdownDrain() bool
	DashboardEnabled() bool
	DashboardURL() string
	FunnelSetupURL() string
	Snapshot(context.Context) (domain.Snapshot, error)
	BufferedLogs(context.Context, *slog.Level) (domain.BufferedLogSnapshot, error)
	FunnelSetupStatus(context.Context) domain.FunnelSetupStatus
}

var (
	newRuntimeService = func(ctx context.Context, logger *slog.Logger, workflowPath string, options ...service.Option) (runtimeService, error) {
		return service.New(ctx, logger, workflowPath, options...)
	}
	runRuntimeTUI = func(ctx context.Context, in io.Reader, out io.Writer, source runtimeService, serviceErrCh <-chan error, requestShutdownDrain func() bool, stop func()) error {
		return tui.Run(ctx, in, out, source, serviceErrCh, requestShutdownDrain, stop)
	}
	runtimeIsInteractiveTerminal = isInteractiveTerminal
)

func runRoot(cmd *cobra.Command, opts rootOptions) int {
	var options []service.Option
	if opts.port >= 0 {
		options = append(options, service.WithServerPortOverride(opts.port))
	}

	logger := service.NewDefaultLogger(opts.verbose)
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	svc, err := newRuntimeService(ctx, logger, opts.workflowPath, options...)
	if err != nil {
		logger.Error("startup failed", "error", service.DescribeStartupError(err))
		return 1
	}

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
		if err := runRuntimeTUI(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), svc, runErrCh, svc.RequestShutdownDrain, stop); err != nil {
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
	renderSetupStatusWithRenderer(newCommandRenderer(cmd), status)
}

func renderSetupStatusWithRenderer(renderer *clioutput.Renderer, status domain.FunnelSetupStatus) {
	renderer.Section("Overview")
	if status.Ready {
		renderer.Status(clioutput.StatusOK, "Funnel ready", "true")
	} else {
		renderer.Status(clioutput.StatusAction, "Funnel ready", "false")
	}
	if status.LocalBaseURL != "" {
		renderer.Item("Local UI URL", status.LocalBaseURL)
	}
	if status.TailnetUIBaseURL != "" {
		renderer.Item("Tailnet UI URL", status.TailnetUIBaseURL)
	}
	if status.LocalWebhookBaseURL != "" {
		renderer.Item("Local webhook URL", status.LocalWebhookBaseURL)
	}
	if status.PublicBaseURL != "" {
		renderer.Item("Public webhook URL", status.PublicBaseURL)
	}
	if status.LinearWebhookURL != "" {
		renderer.Item("Linear webhook URL", status.LinearWebhookURL)
	}
	if status.GitHubWebhookURL != "" {
		renderer.Item("GitHub webhook URL", status.GitHubWebhookURL)
	}

	renderer.Section("Checks")
	for _, check := range status.Checks {
		line := check.Detail
		if line == "" {
			line = check.Remediation
		} else if check.Remediation != "" {
			line += " " + check.Remediation
		}
		renderer.Status(renderSetupCheckStatus(check.Status), check.Label, line)
	}

	if status.SuggestedServeCommand != "" || status.SuggestedCommand != "" {
		renderer.Section("Next steps")
	}
	if status.SuggestedServeCommand != "" {
		renderer.Status(clioutput.StatusAction, "Suggested Serve command", status.SuggestedServeCommand)
	}
	if status.SuggestedCommand != "" {
		renderer.Status(clioutput.StatusAction, "Suggested Funnel command", status.SuggestedCommand)
	}
}

func renderSetupCheckStatus(status string) clioutput.StatusKind {
	switch status {
	case "ok":
		return clioutput.StatusOK
	case "error":
		return clioutput.StatusError
	case "disabled", "skipped":
		return clioutput.StatusInfo
	case "warn":
		return clioutput.StatusWarn
	default:
		return clioutput.StatusInfo
	}
}
