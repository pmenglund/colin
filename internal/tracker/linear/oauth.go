package linear

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	OAuthClientIDEnvVar      = "LINEAR_OAUTH_CLIENT_ID"
	defaultOAuthAuthorizeURL = "https://linear.app/oauth/authorize"
	defaultOAuthTokenURL     = "https://api.linear.app/oauth/token"
	authFileName             = "auth.json"
)

var authFileMu sync.Mutex
var oauthAuthorizeEndpoint = defaultOAuthAuthorizeURL
var oauthTokenEndpoint = defaultOAuthTokenURL

// AuthFile is the persisted auth state stored under .colin/auth.json.
type AuthFile struct {
	Linear LinearStoredAuth `json:"linear,omitempty"`
}

// LinearStoredAuth is the persisted Linear auth state for one workflow root.
type LinearStoredAuth struct {
	ClientID              string              `json:"client_id,omitempty"`
	AccessToken           string              `json:"access_token,omitempty"`
	RefreshToken          string              `json:"refresh_token,omitempty"`
	Scope                 string              `json:"scope,omitempty"`
	ExpiresAt             *time.Time          `json:"expires_at,omitempty"`
	ActorID               string              `json:"actor_id,omitempty"`
	ActorName             string              `json:"actor_name,omitempty"`
	ActorType             string              `json:"actor_type,omitempty"`
	SupportsAgentSessions bool                `json:"supports_agent_sessions,omitempty"`
	WorkspaceID           string              `json:"workspace_id,omitempty"`
	WorkspaceName         string              `json:"workspace_name,omitempty"`
	PendingOAuth          *LinearPendingOAuth `json:"pending_oauth,omitempty"`
}

// LinearPendingOAuth is the transient PKCE state required to complete one OAuth flow.
type LinearPendingOAuth struct {
	State        string    `json:"state,omitempty"`
	CodeVerifier string    `json:"code_verifier,omitempty"`
	RedirectURI  string    `json:"redirect_uri,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        any    `json:"scope"`
}

// AuthFilePath returns the per-workflow auth file path.
func AuthFilePath(workflowPath string) string {
	root := filepath.Dir(strings.TrimSpace(workflowPath))
	if root == "" {
		root = "."
	}
	return filepath.Join(root, ".colin", authFileName)
}

// LoadAuthFile reads the persisted auth state. A missing file returns an empty state.
func LoadAuthFile(workflowPath string) (AuthFile, error) {
	path := AuthFilePath(workflowPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AuthFile{}, nil
		}
		return AuthFile{}, err
	}
	var file AuthFile
	if err := json.Unmarshal(data, &file); err != nil {
		return AuthFile{}, err
	}
	return file, nil
}

// UpdateAuthFile applies one in-process auth-file mutation and writes the result atomically.
func UpdateAuthFile(workflowPath string, update func(*AuthFile) error) error {
	authFileMu.Lock()
	defer authFileMu.Unlock()

	file, err := LoadAuthFile(workflowPath)
	if err != nil {
		return err
	}
	if err := update(&file); err != nil {
		return err
	}
	return writeAuthFile(workflowPath, file)
}

func writeAuthFile(workflowPath string, file AuthFile) error {
	path := AuthFilePath(workflowPath)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, "auth-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// NewPendingOAuth creates one PKCE-backed pending OAuth session.
func NewPendingOAuth(redirectURI string, now time.Time) (LinearPendingOAuth, error) {
	state, err := randomURLSafeString(32)
	if err != nil {
		return LinearPendingOAuth{}, err
	}
	verifier, err := randomURLSafeString(48)
	if err != nil {
		return LinearPendingOAuth{}, err
	}
	return LinearPendingOAuth{
		State:        state,
		CodeVerifier: verifier,
		RedirectURI:  strings.TrimSpace(redirectURI),
		CreatedAt:    now.UTC(),
	}, nil
}

// AuthorizeURL returns the Linear OAuth authorize URL for the supplied client and pending PKCE state.
func AuthorizeURL(clientID string, pending LinearPendingOAuth) (string, error) {
	values := url.Values{}
	values.Set("client_id", strings.TrimSpace(clientID))
	values.Set("redirect_uri", strings.TrimSpace(pending.RedirectURI))
	values.Set("response_type", "code")
	values.Set("scope", "read,write")
	values.Set("state", strings.TrimSpace(pending.State))
	values.Set("actor", "app")
	values.Set("code_challenge", codeChallengeS256(pending.CodeVerifier))
	values.Set("code_challenge_method", "S256")
	if values.Get("client_id") == "" || values.Get("redirect_uri") == "" || values.Get("state") == "" || strings.TrimSpace(pending.CodeVerifier) == "" {
		return "", fmt.Errorf("%w: missing OAuth authorize parameters", ErrAPIRequest)
	}
	return oauthAuthorizeEndpoint + "?" + values.Encode(), nil
}

// CompletePKCEOAuth exchanges the callback code for a token set and resolves the resulting actor/workspace identity.
func CompletePKCEOAuth(ctx context.Context, endpoint string, clientID string, code string, pending LinearPendingOAuth) (LinearStoredAuth, error) {
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("client_id", strings.TrimSpace(clientID))
	values.Set("code", strings.TrimSpace(code))
	values.Set("redirect_uri", strings.TrimSpace(pending.RedirectURI))
	values.Set("code_verifier", strings.TrimSpace(pending.CodeVerifier))

	token, err := exchangeOAuthToken(ctx, values)
	if err != nil {
		return LinearStoredAuth{}, err
	}
	metadata, err := fetchOAuthViewerMetadata(ctx, endpoint, token.AccessToken)
	if err != nil {
		return LinearStoredAuth{}, err
	}

	auth := LinearStoredAuth{
		ClientID:              strings.TrimSpace(clientID),
		AccessToken:           token.AccessToken,
		RefreshToken:          token.RefreshToken,
		Scope:                 parseOAuthScope(token.Scope),
		ActorID:               metadata.ActorID,
		ActorName:             metadata.ActorName,
		ActorType:             metadata.ActorType,
		SupportsAgentSessions: metadata.SupportsAgentSessions,
		WorkspaceID:           metadata.WorkspaceID,
		WorkspaceName:         metadata.WorkspaceName,
	}
	if token.ExpiresIn > 0 {
		expiresAt := time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second)
		auth.ExpiresAt = &expiresAt
	}
	return auth, nil
}

// RefreshStoredAuth exchanges a refresh token for a new access token.
func RefreshStoredAuth(ctx context.Context, auth LinearStoredAuth) (LinearStoredAuth, error) {
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", strings.TrimSpace(auth.RefreshToken))
	values.Set("client_id", strings.TrimSpace(auth.ClientID))
	token, err := exchangeOAuthToken(ctx, values)
	if err != nil {
		return LinearStoredAuth{}, err
	}
	auth.AccessToken = token.AccessToken
	auth.RefreshToken = strings.TrimSpace(token.RefreshToken)
	auth.Scope = parseOAuthScope(token.Scope)
	if token.ExpiresIn > 0 {
		expiresAt := time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second)
		auth.ExpiresAt = &expiresAt
	} else {
		auth.ExpiresAt = nil
	}
	return auth, nil
}

type oauthViewerMetadata struct {
	ActorID               string
	ActorName             string
	ActorType             string
	SupportsAgentSessions bool
	WorkspaceID           string
	WorkspaceName         string
}

func fetchOAuthViewerMetadata(ctx context.Context, endpoint string, accessToken string) (oauthViewerMetadata, error) {
	client := newBearerAPIClient(endpoint, accessToken)
	const query = `
query ViewerOAuthMetadata {
  viewer {
    id
    name
    displayName
    app
    supportsAgentSessions
    organization {
      id
      name
    }
  }
}
`
	resp, err := client.doQuery(ctx, query, nil)
	if err != nil {
		return oauthViewerMetadata{}, err
	}
	viewer, ok := nestedMap(resp, "data", "viewer")
	if !ok {
		return oauthViewerMetadata{}, ErrUnknownPayload
	}
	identity := oauthViewerMetadata{}
	identity.ActorID, _ = stringValue(viewer["id"])
	displayName, _ := stringValue(viewer["displayName"])
	identity.ActorName = strings.TrimSpace(displayName)
	if identity.ActorName == "" {
		identity.ActorName, _ = stringValue(viewer["name"])
	}
	isApp, _ := viewer["app"].(bool)
	if isApp {
		identity.ActorType = "app"
	} else {
		identity.ActorType = "user"
	}
	identity.SupportsAgentSessions, _ = viewer["supportsAgentSessions"].(bool)
	if organization, ok := nestedMap(viewer, "organization"); ok {
		identity.WorkspaceID, _ = stringValue(organization["id"])
		identity.WorkspaceName, _ = stringValue(organization["name"])
	}
	if strings.TrimSpace(identity.ActorID) == "" || strings.TrimSpace(identity.ActorName) == "" {
		return oauthViewerMetadata{}, ErrUnknownPayload
	}
	return identity, nil
}

func exchangeOAuthToken(ctx context.Context, values url.Values) (oauthTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthTokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthTokenResponse{}, fmt.Errorf("%w: %v", ErrAPIRequest, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return oauthTokenResponse{}, fmt.Errorf("%w: %v", ErrAPIRequest, err)
	}
	defer resp.Body.Close()
	var token oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return oauthTokenResponse{}, fmt.Errorf("%w: %v", ErrUnknownPayload, err)
	}
	if resp.StatusCode != http.StatusOK {
		return oauthTokenResponse{}, fmt.Errorf("%w: status=%d", ErrAPIStatus, resp.StatusCode)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return oauthTokenResponse{}, ErrUnknownPayload
	}
	return token, nil
}

func newBearerAPIClient(endpoint string, accessToken string) *Client {
	return &Client{
		endpoint: endpoint,
		auth: &staticAuthorizationProvider{
			value: "Bearer " + strings.TrimSpace(accessToken),
		},
		labelIDs:     map[string]string{},
		projectsByID: map[string]string{},
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func parseOAuthScope(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func randomURLSafeString(size int) (string, error) {
	if size <= 0 {
		size = 32
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(buf), "="), nil
}

func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(sum[:]), "=")
}
