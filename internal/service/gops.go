package service

import (
	"context"
	"log/slog"
	"sync"

	gopsagent "github.com/google/gops/agent"
)

type gopsHooks struct {
	listen func(gopsagent.Options) error
	close  func()
}

var defaultGOPSHooks = gopsHooks{
	listen: gopsagent.Listen,
	close:  gopsagent.Close,
}

func startGOPSAgent(ctx context.Context, logger *slog.Logger, hooks gopsHooks) func() {
	if hooks.listen == nil || hooks.close == nil {
		return func() {}
	}

	if err := hooks.listen(gopsagent.Options{ShutdownCleanup: false}); err != nil {
		if logger != nil {
			logger.Warn("gops agent disabled", "error", err)
		}
		return func() {}
	}

	if logger != nil {
		logger.Info("gops agent started")
	}

	var once sync.Once
	stop := func() {
		once.Do(hooks.close)
	}

	go func() {
		<-ctx.Done()
		stop()
	}()

	return stop
}
