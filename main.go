package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pmenglund/colin/internal/service"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", -1, "dashboard port override; default is 8888")
	verbose := flags.Bool("verbose", false, "print structured service logs")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: colin [--port PORT] [--verbose] [path-to-WORKFLOW.md]")
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
		exited, err := announceStartup(stdout, svc.DashboardEnabled(), svc.DashboardURL, runErrCh)
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

func announceStartup(stdout io.Writer, dashboardEnabled bool, dashboardURL func() string, runErrCh <-chan error) (bool, error) {
	if !dashboardEnabled {
		_, _ = fmt.Fprintln(stdout, "Colin is running.")
		return false, nil
	}

	if url := dashboardURL(); url != "" {
		_, _ = fmt.Fprintf(stdout, "Colin is running. Web UI: %s\n", url)
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
				_, _ = fmt.Fprintf(stdout, "Colin is running. Web UI: %s\n", url)
				return false, nil
			}
		}
	}
}
