package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func TestAuthorizeURLUsesAppActorPKCE(t *testing.T) {
	t.Parallel()

	pending := LinearPendingOAuth{
		State:        "state-123",
		CodeVerifier: "verifier-123",
		RedirectURI:  "https://colin.tail.example.ts.net/callbacks/linear",
	}
	raw, err := AuthorizeURL("client-123", pending)
	if err != nil {
		t.Fatalf("AuthorizeURL() error = %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if got := parsed.Query().Get("actor"); got != "app" {
		t.Fatalf("actor = %q, want %q", got, "app")
	}
	if got := parsed.Query().Get("scope"); got != linearAppOAuthScopes {
		t.Fatalf("scope = %q, want %q", got, linearAppOAuthScopes)
	}
	if got := parsed.Query().Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want %q", got, "S256")
	}
	if got := parsed.Query().Get("redirect_uri"); got != pending.RedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, pending.RedirectURI)
	}
}

func TestUpdateAuthFileWritesMode600(t *testing.T) {
	t.Parallel()

	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := UpdateAuthFile(workflowPath, func(file *AuthFile) error {
		file.Linear = LinearStoredAuth{
			ClientID:    "client-123",
			AccessToken: "access-123",
			ActorName:   "Colin",
			ActorType:   "app",
		}
		return nil
	}); err != nil {
		t.Fatalf("UpdateAuthFile() error = %v", err)
	}
	authPath := AuthFilePath(workflowPath)
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want %#o", got, 0o600)
	}
	file, err := LoadAuthFile(workflowPath)
	if err != nil {
		t.Fatalf("LoadAuthFile() error = %v", err)
	}
	if got := file.Linear.ActorName; got != "Colin" {
		t.Fatalf("ActorName = %q, want %q", got, "Colin")
	}
}

func TestCompletePKCEOAuthReturnsAppActorMetadata(t *testing.T) {
	t.Parallel()

	oldTokenEndpoint := oauthTokenEndpoint
	oldAuthorizeEndpoint := oauthAuthorizeEndpoint
	t.Cleanup(func() {
		oauthTokenEndpoint = oldTokenEndpoint
		oauthAuthorizeEndpoint = oldAuthorizeEndpoint
	})

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "authorization_code" {
			t.Fatalf("grant_type = %q, want %q", got, "authorization_code")
		}
		if got := r.Form.Get("client_id"); got != "client-123" {
			t.Fatalf("client_id = %q, want %q", got, "client-123")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "oauth-access",
			"refresh_token": "oauth-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "read write",
		})
	}))
	defer tokenServer.Close()
	oauthTokenEndpoint = tokenServer.URL

	graphQLServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-access" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer oauth-access")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"viewer": map[string]any{
					"id":                    "actor-123",
					"name":                  "Colin",
					"displayName":           "Colin",
					"app":                   true,
					"supportsAgentSessions": true,
					"organization": map[string]any{
						"id":   "workspace-123",
						"name": "bothnia",
					},
				},
			},
		})
	}))
	defer graphQLServer.Close()

	auth, err := CompletePKCEOAuth(context.Background(), graphQLServer.URL, "client-123", "code-123", LinearPendingOAuth{
		State:        "state-123",
		CodeVerifier: "verifier-123",
		RedirectURI:  "https://colin.tail.example.ts.net/callbacks/linear",
	})
	if err != nil {
		t.Fatalf("CompletePKCEOAuth() error = %v", err)
	}
	if auth.AccessToken != "oauth-access" {
		t.Fatalf("AccessToken = %q, want %q", auth.AccessToken, "oauth-access")
	}
	if auth.RefreshToken != "oauth-refresh" {
		t.Fatalf("RefreshToken = %q, want %q", auth.RefreshToken, "oauth-refresh")
	}
	if auth.ActorType != "app" {
		t.Fatalf("ActorType = %q, want %q", auth.ActorType, "app")
	}
	if auth.WorkspaceName != "bothnia" {
		t.Fatalf("WorkspaceName = %q, want %q", auth.WorkspaceName, "bothnia")
	}
	if auth.ExpiresAt == nil || time.Until(auth.ExpiresAt.UTC()) <= 0 {
		t.Fatalf("ExpiresAt = %v, want future value", auth.ExpiresAt)
	}
}

func TestNewAuthorizationProviderUsesStoredOAuthAuth(t *testing.T) {
	t.Parallel()

	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := UpdateAuthFile(workflowPath, func(file *AuthFile) error {
		file.Linear = LinearStoredAuth{
			ClientID:    "client-123",
			AccessToken: "oauth-access",
		}
		return nil
	}); err != nil {
		t.Fatalf("UpdateAuthFile() error = %v", err)
	}
	provider, err := newAuthorizationProvider(domain.ServiceConfig{
		WorkflowPath: workflowPath,
		Tracker: domain.TrackerConfig{
			Endpoint: defaultEndpoint,
		},
	})
	if err != nil {
		t.Fatalf("newAuthorizationProvider() error = %v", err)
	}
	auth, err := provider.Authorization(context.Background())
	if err != nil {
		t.Fatalf("Authorization() error = %v", err)
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("Authorization() = %q, want Bearer token", auth)
	}
}
