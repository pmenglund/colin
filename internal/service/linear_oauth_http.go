package service

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/tracker/linear"
)

const (
	linearOAuthSetupPath    = "/setup/linear/app"
	linearOAuthCallbackPath = "/callbacks/linear"
)

type LinearOAuthHTTPHandlerConfig struct {
	WorkflowPath    string
	Endpoint        string
	OAuthClientID   string
	CallbackBaseURL string
	Logger          *slog.Logger
	OnResult        func(error)
}

func NewLinearOAuthHTTPHandler(cfg LinearOAuthHTTPHandlerConfig) http.Handler {
	mux := http.NewServeMux()
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	callbackURL := strings.TrimRight(strings.TrimSpace(cfg.CallbackBaseURL), "/") + linearOAuthCallbackPath

	mux.HandleFunc(linearOAuthSetupPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(cfg.OAuthClientID) == "" {
			renderLinearOAuthResult(w, http.StatusBadRequest, "Linear OAuth is not configured", "Set `tracker.oauth_client_id` or export `LINEAR_OAUTH_CLIENT_ID`, then rerun `colin setup linear app --connect`.")
			return
		}
		if strings.TrimSpace(cfg.CallbackBaseURL) == "" {
			renderLinearOAuthResult(w, http.StatusBadRequest, "Tailnet callback URL is unavailable", "Configure Tailscale Serve or set `server.ui_url`, then rerun `colin setup linear app --connect`.")
			return
		}

		pending, err := linear.NewPendingOAuth(callbackURL, time.Now().UTC())
		if err != nil {
			logger.Error("failed to create pending Linear OAuth state", "error", err)
			renderLinearOAuthResult(w, http.StatusInternalServerError, "Unable to start Linear OAuth", err.Error())
			return
		}
		if err := linear.UpdateAuthFile(cfg.WorkflowPath, func(file *linear.AuthFile) error {
			file.Linear.ClientID = strings.TrimSpace(cfg.OAuthClientID)
			file.Linear.PendingOAuth = &pending
			return nil
		}); err != nil {
			logger.Error("failed to persist pending Linear OAuth state", "error", err)
			renderLinearOAuthResult(w, http.StatusInternalServerError, "Unable to save Linear OAuth state", err.Error())
			return
		}

		authorizeURL, err := linear.AuthorizeURL(cfg.OAuthClientID, pending)
		if err != nil {
			logger.Error("failed to build Linear OAuth authorize URL", "error", err)
			renderLinearOAuthResult(w, http.StatusInternalServerError, "Unable to start Linear OAuth", err.Error())
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusFound)
			return
		}
		http.Redirect(w, r, authorizeURL, http.StatusFound)
	})

	mux.HandleFunc(linearOAuthCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		state := strings.TrimSpace(r.URL.Query().Get("state"))
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		authFile, err := linear.LoadAuthFile(cfg.WorkflowPath)
		if err != nil {
			reportLinearOAuthResult(logger, cfg.OnResult, err)
			renderLinearOAuthResult(w, http.StatusInternalServerError, "Unable to read Linear auth state", err.Error())
			return
		}
		pending := authFile.Linear.PendingOAuth
		if pending == nil || strings.TrimSpace(pending.State) == "" {
			err := fmt.Errorf("missing pending Linear OAuth state")
			reportLinearOAuthResult(logger, cfg.OnResult, err)
			renderLinearOAuthResult(w, http.StatusBadRequest, "Linear OAuth session is missing", "Start again from `/setup/linear/app`.")
			return
		}
		if state == "" || state != strings.TrimSpace(pending.State) {
			err := fmt.Errorf("invalid Linear OAuth state")
			reportLinearOAuthResult(logger, cfg.OnResult, err)
			renderLinearOAuthResult(w, http.StatusBadRequest, "Linear OAuth state did not match", "Start again from `/setup/linear/app`.")
			return
		}
		if code == "" {
			err := fmt.Errorf("missing Linear OAuth code")
			reportLinearOAuthResult(logger, cfg.OnResult, err)
			renderLinearOAuthResult(w, http.StatusBadRequest, "Linear OAuth code is missing", "Start again from `/setup/linear/app`.")
			return
		}
		clientID := strings.TrimSpace(authFile.Linear.ClientID)
		if clientID == "" {
			clientID = strings.TrimSpace(cfg.OAuthClientID)
		}
		auth, err := linear.CompletePKCEOAuth(r.Context(), cfg.Endpoint, clientID, code, *pending)
		if err != nil {
			reportLinearOAuthResult(logger, cfg.OnResult, err)
			renderLinearOAuthResult(w, http.StatusBadGateway, "Unable to finish Linear OAuth", err.Error())
			return
		}
		if err := linear.UpdateAuthFile(cfg.WorkflowPath, func(file *linear.AuthFile) error {
			file.Linear = auth
			return nil
		}); err != nil {
			reportLinearOAuthResult(logger, cfg.OnResult, err)
			renderLinearOAuthResult(w, http.StatusInternalServerError, "Unable to save Linear OAuth credentials", err.Error())
			return
		}
		reportLinearOAuthResult(logger, cfg.OnResult, nil)
		renderLinearOAuthResult(w, http.StatusOK, "Linear OAuth connected", "Colin stored the Linear app credentials in `"+linear.AuthFilePath(cfg.WorkflowPath)+"`. You can close this page and return to the terminal.")
	})

	return mux
}

func renderLinearOAuthResult(w http.ResponseWriter, status int, title string, body string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>",
		html.EscapeString(title),
		html.EscapeString(title),
		html.EscapeString(body),
	)
}

func reportLinearOAuthResult(logger *slog.Logger, onResult func(error), err error) {
	if err != nil {
		logger.Error("Linear OAuth flow failed", "error", err)
	} else {
		logger.Info("Linear OAuth flow completed")
	}
	if onResult != nil {
		onResult(err)
	}
}

func linearOAuthConnectURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + linearOAuthSetupPath
}

func linearOAuthCallbackURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + linearOAuthCallbackPath
}

func waitForLinearOAuthResult(ctx context.Context, resultCh <-chan error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-resultCh:
		return err
	}
}
