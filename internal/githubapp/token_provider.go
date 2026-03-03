package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultGitHubAPIURL   = "https://api.github.com"
	jwtLifetime           = 9 * time.Minute
	tokenRefreshSkew      = 1 * time.Minute
	githubAcceptHeader    = "application/vnd.github+json"
	githubAPIVersionValue = "2022-11-28"
)

// TokenProvider returns installation tokens for authenticated GitHub calls.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// InstallationTokenProviderOptions configures InstallationTokenProvider.
type InstallationTokenProviderOptions struct {
	AppID          string
	InstallationID string
	PrivateKeyPEM  string
	APIBaseURL     string
	HTTPClient     *http.Client
	Now            func() time.Time
}

// InstallationTokenProvider mints GitHub App JWTs and exchanges them for
// installation access tokens.
type InstallationTokenProvider struct {
	appID          int64
	installationID int64
	privateKey     *rsa.PrivateKey
	apiBaseURL     string
	httpClient     *http.Client
	now            func() time.Time

	mu           sync.Mutex
	cachedToken  string
	cachedExpiry time.Time
}

// NewInstallationTokenProvider builds a token provider for a GitHub App.
func NewInstallationTokenProvider(opts InstallationTokenProviderOptions) (*InstallationTokenProvider, error) {
	appID, err := strconv.ParseInt(strings.TrimSpace(opts.AppID), 10, 64)
	if err != nil || appID <= 0 {
		return nil, fmt.Errorf("parse app id %q: %w", strings.TrimSpace(opts.AppID), errInvalidPositiveInteger)
	}
	installationID, err := strconv.ParseInt(strings.TrimSpace(opts.InstallationID), 10, 64)
	if err != nil || installationID <= 0 {
		return nil, fmt.Errorf("parse installation id %q: %w", strings.TrimSpace(opts.InstallationID), errInvalidPositiveInteger)
	}

	privateKey, err := parseRSAPrivateKey(strings.TrimSpace(opts.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse github app private key: %w", err)
	}

	apiBaseURL := strings.TrimSpace(opts.APIBaseURL)
	if apiBaseURL == "" {
		apiBaseURL = defaultGitHubAPIURL
	}
	if _, err := url.ParseRequestURI(apiBaseURL); err != nil {
		return nil, fmt.Errorf("parse GitHub API URL %q: %w", apiBaseURL, err)
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &InstallationTokenProvider{
		appID:          appID,
		installationID: installationID,
		privateKey:     privateKey,
		apiBaseURL:     strings.TrimRight(apiBaseURL, "/"),
		httpClient:     httpClient,
		now:            now,
	}, nil
}

var errInvalidPositiveInteger = errors.New("must be a positive integer")

// Token returns a cached installation token or refreshes it when needed.
func (p *InstallationTokenProvider) Token(ctx context.Context) (string, error) {
	if p == nil {
		return "", errors.New("installation token provider is nil")
	}

	now := p.now()
	p.mu.Lock()
	if strings.TrimSpace(p.cachedToken) != "" && now.Before(p.cachedExpiry.Add(-tokenRefreshSkew)) {
		token := p.cachedToken
		p.mu.Unlock()
		return token, nil
	}
	p.mu.Unlock()

	token, expiresAt, err := p.fetchInstallationToken(ctx, now)
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	p.cachedToken = token
	p.cachedExpiry = expiresAt
	p.mu.Unlock()

	return token, nil
}

func (p *InstallationTokenProvider) fetchInstallationToken(ctx context.Context, now time.Time) (string, time.Time, error) {
	jwtToken, err := signGitHubAppJWT(p.appID, now, p.privateKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign GitHub App JWT: %w", err)
	}

	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", p.apiBaseURL, p.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build installation token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", githubAcceptHeader)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersionValue)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("request installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read installation token response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf(
			"request installation token: unexpected status %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}

	var payload struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", time.Time{}, fmt.Errorf("decode installation token response: %w", err)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return "", time.Time{}, errors.New("installation token response missing token")
	}
	if payload.ExpiresAt.IsZero() {
		return "", time.Time{}, errors.New("installation token response missing expires_at")
	}

	return strings.TrimSpace(payload.Token), payload.ExpiresAt, nil
}

func signGitHubAppJWT(appID int64, now time.Time, privateKey *rsa.PrivateKey) (string, error) {
	if privateKey == nil {
		return "", errors.New("private key is required")
	}

	headerJSON, err := json.Marshal(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
	})
	if err != nil {
		return "", err
	}

	payloadJSON, err := json.Marshal(map[string]any{
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}

	headerPart := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadPart := base64.RawURLEncoding.EncodeToString(payloadJSON)
	unsigned := headerPart + "." + payloadPart

	hash := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}

	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func parseRSAPrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	if privateKeyPEM == "" {
		return nil, errors.New("private key is required")
	}

	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("invalid PEM private key")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return rsaKey, nil
}
