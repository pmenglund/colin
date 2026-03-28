package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pmenglund/colin/internal/service"
)

func main() {
	logger := service.NewDefaultLogger()

	var workflowPath string
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: colin [path-to-WORKFLOW.md]")
		os.Exit(2)
	}
	if len(os.Args) == 2 {
		workflowPath = os.Args[1]
	}

	svc, err := service.New(logger, workflowPath)
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
