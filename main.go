package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/service"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "setup" {
		return runSetup(args[1:], stdout, stderr)
	}

	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", -1, "dashboard port override; default is 8888")
	verbose := flags.Bool("verbose", false, "print structured service logs")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: colin [--port PORT] [--verbose] [path-to-WORKFLOW.md]")
		fmt.Fprintln(stderr, "       colin setup funnel [--json] [path-to-WORKFLOW.md]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 1 {
		flags.Usage()
		return 2
	}

	workflowPath := ""
	if flags.NArg() == 1 {
		workflowPath = flags.Arg(0)
	}

	var options []service.Option
	if *port >= 0 {
		options = append(options, service.WithServerPortOverride(*port))
	}

	logger := service.NewDefaultLogger(*verbose)
	svc, err := service.New(logger, workflowPath, options...)
	if err != nil {
		logger.Error("startup failed", "error", service.DescribeStartupError(err))
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- svc.Run(ctx)
	}()

	if !*verbose {
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

func runSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "funnel" {
		fmt.Fprintln(stderr, "usage: colin setup funnel [--json] [path-to-WORKFLOW.md]")
		return 2
	}

	flags := flag.NewFlagSet("setup funnel", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "print JSON output")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: colin setup funnel [--json] [path-to-WORKFLOW.md]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() > 1 {
		flags.Usage()
		return 2
	}

	workflowPath := ""
	if flags.NArg() == 1 {
		workflowPath = flags.Arg(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, err := service.LoadFunnelSetupStatus(ctx, workflowPath)
	if err != nil {
		fmt.Fprintln(stderr, service.DescribeStartupError(err))
		return 1
	}

	if *jsonOutput {
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
