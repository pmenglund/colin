package service

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// LinearOAuthSetupSession serves the temporary tailnet-visible OAuth routes used during setup.
type LinearOAuthSetupSession struct {
	ConnectURL  string
	CallbackURL string
	server      *http.Server
	resultCh    chan error
}

// StartLinearOAuthSetupSession starts a temporary local HTTP server for the Linear OAuth setup flow.
func StartLinearOAuthSetupSession(ctx context.Context, workflowPath string, logger *slog.Logger, optionFns ...Option) (*LinearOAuthSetupSession, error) {
	opts := buildOptions(optionFns...)
	_, cfg, err := loadConfig(workflowPath, opts)
	if err != nil {
		return nil, err
	}
	uiBaseURL := resolveUIBaseURL(ctx, nil, cfg.Server)
	if strings.TrimSpace(uiBaseURL) == "" {
		return nil, fmt.Errorf("%w: configure `server.ui_url` or run `colin setup tailscale` first", ErrMissingWebhookPublicURL)
	}
	if strings.TrimSpace(cfg.Tracker.OAuthClientID) == "" {
		return nil, fmt.Errorf("configure `tracker.oauth_client_id` or export %s before starting the Linear OAuth flow", LinearOAuthClientIDEnvVar)
	}
	port := 8888
	if cfg.Server.Port != nil && *cfg.Server.Port > 0 {
		port = *cfg.Server.Port
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	resultCh := make(chan error, 1)
	handler := NewLinearOAuthHTTPHandler(LinearOAuthHTTPHandlerConfig{
		WorkflowPath:    workflowPath,
		Endpoint:        cfg.Tracker.Endpoint,
		OAuthClientID:   cfg.Tracker.OAuthClientID,
		CallbackBaseURL: uiBaseURL,
		Logger:          logger,
		OnResult: func(err error) {
			select {
			case resultCh <- err:
			default:
			}
		},
	})
	server := &http.Server{Handler: handler}
	go func() {
		serveErr := server.Serve(listener)
		if serveErr != nil && serveErr != http.ErrServerClosed {
			select {
			case resultCh <- serveErr:
			default:
			}
		}
	}()
	return &LinearOAuthSetupSession{
		ConnectURL:  linearOAuthConnectURL(uiBaseURL),
		CallbackURL: linearOAuthCallbackURL(uiBaseURL),
		server:      server,
		resultCh:    resultCh,
	}, nil
}

// Wait blocks until the OAuth flow finishes or the context is canceled.
func (s *LinearOAuthSetupSession) Wait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return waitForLinearOAuthResult(ctx, s.resultCh)
}

// Close shuts down the temporary HTTP server.
func (s *LinearOAuthSetupSession) Close(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}
