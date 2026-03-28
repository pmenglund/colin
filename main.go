package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pmenglund/colin/internal/service"
)

func main() {
	logger := service.NewDefaultLogger()

	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	port := flags.Int("port", -1, "dashboard port override; default is 8888")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: colin [--port PORT] [path-to-WORKFLOW.md]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() > 1 {
		flags.Usage()
		os.Exit(2)
	}

	workflowPath := ""
	if flags.NArg() == 1 {
		workflowPath = flags.Arg(0)
	}

	var options []service.Option
	if *port >= 0 {
		options = append(options, service.WithServerPortOverride(*port))
	}

	svc, err := service.New(logger, workflowPath, options...)
	if err != nil {
		logger.Error("startup failed", "error", service.DescribeStartupError(err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := svc.Run(ctx); err != nil {
		logger.Error("service exited abnormally", "error", err)
		os.Exit(1)
	}
}
