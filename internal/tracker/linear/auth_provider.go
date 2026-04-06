package linear

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
)

type authorizationProvider interface {
	Authorization(context.Context) (string, error)
	Refresh(context.Context) error
}

type staticAuthorizationProvider struct {
	value string
}

func (p *staticAuthorizationProvider) Authorization(context.Context) (string, error) {
	value := strings.TrimSpace(p.value)
	if value == "" {
		return "", config.ErrMissingTrackerAPIKey
	}
	return value, nil
}

func (p *staticAuthorizationProvider) Refresh(context.Context) error {
	return errors.New("static Linear auth cannot refresh")
}

type oauthAuthorizationProvider struct {
	workflowPath string

	mu   sync.Mutex
	auth LinearStoredAuth
}

func newOAuthAuthorizationProvider(workflowPath string, auth LinearStoredAuth) *oauthAuthorizationProvider {
	return &oauthAuthorizationProvider{
		workflowPath: workflowPath,
		auth:         auth,
	}
}

func newAuthorizationProvider(cfg domain.ServiceConfig) (authorizationProvider, error) {
	if token := strings.TrimSpace(cfg.Tracker.APIKey); token != "" {
		return &staticAuthorizationProvider{value: token}, nil
	}
	authFile, err := LoadAuthFile(cfg.WorkflowPath)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(authFile.Linear.AccessToken); token != "" {
		if strings.TrimSpace(authFile.Linear.ClientID) == "" {
			authFile.Linear.ClientID = strings.TrimSpace(cfg.Tracker.OAuthClientID)
		}
		return newOAuthAuthorizationProvider(cfg.WorkflowPath, authFile.Linear), nil
	}
	return nil, config.ErrMissingTrackerAPIKey
}

func (p *oauthAuthorizationProvider) Authorization(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if needsRefresh(p.auth) {
		if err := p.refreshLocked(ctx); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(p.auth.AccessToken) == "" {
		return "", config.ErrMissingTrackerAPIKey
	}
	return "Bearer " + strings.TrimSpace(p.auth.AccessToken), nil
}

func (p *oauthAuthorizationProvider) Refresh(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refreshLocked(ctx)
}

func (p *oauthAuthorizationProvider) refreshLocked(ctx context.Context) error {
	if strings.TrimSpace(p.auth.RefreshToken) == "" || strings.TrimSpace(p.auth.ClientID) == "" {
		return config.ErrMissingTrackerAPIKey
	}
	refreshed, err := RefreshStoredAuth(ctx, p.auth)
	if err != nil {
		return err
	}
	p.auth = refreshed
	return UpdateAuthFile(p.workflowPath, func(file *AuthFile) error {
		file.Linear.AccessToken = refreshed.AccessToken
		file.Linear.RefreshToken = refreshed.RefreshToken
		file.Linear.Scope = refreshed.Scope
		file.Linear.ExpiresAt = refreshed.ExpiresAt
		file.Linear.ClientID = refreshed.ClientID
		file.Linear.ActorID = refreshed.ActorID
		file.Linear.ActorName = refreshed.ActorName
		file.Linear.ActorType = refreshed.ActorType
		file.Linear.SupportsAgentSessions = refreshed.SupportsAgentSessions
		file.Linear.WorkspaceID = refreshed.WorkspaceID
		file.Linear.WorkspaceName = refreshed.WorkspaceName
		return nil
	})
}

func needsRefresh(auth LinearStoredAuth) bool {
	if auth.ExpiresAt == nil {
		return false
	}
	return time.Until(auth.ExpiresAt.UTC()) <= time.Minute
}
