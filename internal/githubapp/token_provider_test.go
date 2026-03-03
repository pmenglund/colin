package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstallationTokenProviderTokenFetchesAndCachesToken(t *testing.T) {
	privateKeyPEM := testRSAPrivateKeyPEM(t)
	now := time.Date(2026, 3, 1, 22, 0, 0, 0, time.UTC)
	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/app/installations/999/access_tokens" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		if got := r.Header.Get("Accept"); got != githubAcceptHeader {
			t.Fatalf("Accept header = %q, want %q", got, githubAcceptHeader)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "inst-token",
			"expires_at": now.Add(5 * time.Minute).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	provider, err := NewInstallationTokenProvider(InstallationTokenProviderOptions{
		AppID:          "123",
		InstallationID: "999",
		PrivateKeyPEM:  privateKeyPEM,
		APIBaseURL:     srv.URL,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewInstallationTokenProvider() error = %v", err)
	}

	token1, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	token2, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() second call error = %v", err)
	}

	if token1 != "inst-token" || token2 != "inst-token" {
		t.Fatalf("tokens = %q, %q", token1, token2)
	}
	if calls != 1 {
		t.Fatalf("access token endpoint call count = %d, want 1", calls)
	}
}

func TestInstallationTokenProviderTokenRefreshesNearExpiry(t *testing.T) {
	privateKeyPEM := testRSAPrivateKeyPEM(t)
	now := time.Date(2026, 3, 1, 22, 0, 0, 0, time.UTC)

	current := now
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		token := "inst-token-1"
		if calls > 1 {
			token = "inst-token-2"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      token,
			"expires_at": now.Add(2 * time.Minute).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	provider, err := NewInstallationTokenProvider(InstallationTokenProviderOptions{
		AppID:          "123",
		InstallationID: "999",
		PrivateKeyPEM:  privateKeyPEM,
		APIBaseURL:     srv.URL,
		Now:            func() time.Time { return current },
	})
	if err != nil {
		t.Fatalf("NewInstallationTokenProvider() error = %v", err)
	}

	token1, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	current = now.Add(1*time.Minute + 1*time.Second)
	token2, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() refresh call error = %v", err)
	}

	if token1 != "inst-token-1" {
		t.Fatalf("first token = %q", token1)
	}
	if token2 != "inst-token-2" {
		t.Fatalf("second token = %q", token2)
	}
	if calls != 2 {
		t.Fatalf("access token endpoint call count = %d, want 2", calls)
	}
}

func TestNewInstallationTokenProviderRejectsInvalidInputs(t *testing.T) {
	privateKeyPEM := testRSAPrivateKeyPEM(t)

	_, err := NewInstallationTokenProvider(InstallationTokenProviderOptions{
		AppID:          "0",
		InstallationID: "1",
		PrivateKeyPEM:  privateKeyPEM,
	})
	if err == nil {
		t.Fatal("expected error for invalid app id")
	}

	_, err = NewInstallationTokenProvider(InstallationTokenProviderOptions{
		AppID:          "1",
		InstallationID: "1",
		PrivateKeyPEM:  "not-a-key",
	})
	if err == nil {
		t.Fatal("expected error for invalid private key")
	}
}

func testRSAPrivateKeyPEM(t *testing.T) string {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
	return string(pem.EncodeToMemory(block))
}
