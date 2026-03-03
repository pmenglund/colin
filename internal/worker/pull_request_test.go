package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

func TestGitPullRequestManagerEnsurePullRequestCreatesNewPullRequest(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := initTestGitRemote(t)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	createTaskBranchWithCommit(t, repoRoot, "colin/COLIN-90", "feature.txt", "feature change\n")

	tokenProvider := &stubTokenProvider{token: "token-123"}
	listCalls := 0
	createCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
			t.Fatalf("Authorization header = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/pmenglund/colin/pulls":
			listCalls++
			if listCalls == 1 {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_, _ = w.Write([]byte(`[{"html_url":"https://github.com/pmenglund/colin/pull/90"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/pmenglund/colin/pulls":
			createCalls++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if payload["head"] != "colin/COLIN-90" {
				t.Fatalf("head = %v", payload["head"])
			}
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/pmenglund/colin/pull/90"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	manager := NewGitPullRequestManager(GitPullRequestManagerOptions{
		RepoRoot:      repoRoot,
		BaseBranch:    "main",
		RemoteName:    "origin",
		APIBaseURL:    srv.URL,
		TokenProvider: tokenProvider,
	})
	manager.resolveRepoURL = func(string) (string, string, error) {
		return "pmenglund", "colin", nil
	}

	url, err := manager.EnsurePullRequest(context.Background(), linear.Issue{
		Identifier: "COLIN-90",
		Title:      "Auto PR test",
		Metadata: map[string]string{
			workflow.MetaBranchName: "colin/COLIN-90",
		},
	})
	if err != nil {
		t.Fatalf("EnsurePullRequest() error = %v", err)
	}
	if url != "https://github.com/pmenglund/colin/pull/90" {
		t.Fatalf("EnsurePullRequest() url = %q", url)
	}
	if createCalls != 1 {
		t.Fatalf("create call count = %d, want 1", createCalls)
	}
	if listCalls != 2 {
		t.Fatalf("list call count = %d, want 2", listCalls)
	}
	if tokenProvider.calls != 3 {
		t.Fatalf("token provider calls = %d, want 3", tokenProvider.calls)
	}
	if got := runGit(t, "--git-dir", remotePath, "rev-parse", "refs/heads/colin/COLIN-90"); strings.TrimSpace(got) == "" {
		t.Fatalf("remote branch ref is empty")
	}
}

func TestGitPullRequestManagerEnsurePullRequestUsesExistingPullRequest(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := initTestGitRemote(t)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	createTaskBranchWithCommit(t, repoRoot, "colin/COLIN-91", "feature.txt", "feature change\n")

	tokenProvider := &stubTokenProvider{token: "token-123"}
	createCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/pmenglund/colin/pulls":
			_, _ = w.Write([]byte(`[{"html_url":"https://github.com/pmenglund/colin/pull/91"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/pmenglund/colin/pulls":
			createCalls++
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	manager := NewGitPullRequestManager(GitPullRequestManagerOptions{
		RepoRoot:      repoRoot,
		BaseBranch:    "main",
		RemoteName:    "origin",
		APIBaseURL:    srv.URL,
		TokenProvider: tokenProvider,
	})
	manager.resolveRepoURL = func(string) (string, string, error) {
		return "pmenglund", "colin", nil
	}

	url, err := manager.EnsurePullRequest(context.Background(), linear.Issue{
		Identifier: "COLIN-91",
		Title:      "Existing PR test",
		Metadata: map[string]string{
			workflow.MetaBranchName: "colin/COLIN-91",
		},
	})
	if err != nil {
		t.Fatalf("EnsurePullRequest() error = %v", err)
	}
	if url != "https://github.com/pmenglund/colin/pull/91" {
		t.Fatalf("EnsurePullRequest() url = %q", url)
	}
	if createCalls != 0 {
		t.Fatalf("create call count = %d, want 0", createCalls)
	}
	if tokenProvider.calls != 1 {
		t.Fatalf("token provider calls = %d, want 1", tokenProvider.calls)
	}
}

func TestGitPullRequestManagerEnsurePullRequestAcceptsStoredPullRequestURL(t *testing.T) {
	manager := NewGitPullRequestManager(GitPullRequestManagerOptions{
		RepoRoot:   "/tmp/repo",
		BaseBranch: "main",
		RemoteName: "origin",
	})

	url, err := manager.EnsurePullRequest(context.Background(), linear.Issue{
		Identifier: "COLIN-92",
		Metadata: map[string]string{
			workflow.MetaPRURL: "https://github.com/pmenglund/colin/pull/92",
		},
	})
	if err != nil {
		t.Fatalf("EnsurePullRequest() error = %v", err)
	}
	if url != "https://github.com/pmenglund/colin/pull/92" {
		t.Fatalf("EnsurePullRequest() url = %q", url)
	}
}

func TestGitPullRequestManagerEnsurePullRequestRequiresTokenProvider(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	remotePath := initTestGitRemote(t)
	runGit(t, "-C", repoRoot, "remote", "add", "origin", remotePath)
	createTaskBranchWithCommit(t, repoRoot, "colin/COLIN-93", "feature.txt", "feature change\n")

	manager := NewGitPullRequestManager(GitPullRequestManagerOptions{
		RepoRoot:   repoRoot,
		BaseBranch: "main",
		RemoteName: "origin",
	})

	_, err := manager.EnsurePullRequest(context.Background(), linear.Issue{
		Identifier: "COLIN-93",
		Metadata: map[string]string{
			workflow.MetaBranchName: "colin/COLIN-93",
		},
	})
	if err == nil {
		t.Fatal("expected error when token provider is missing")
	}
	if !strings.Contains(err.Error(), "github token provider is required") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestParseGitHubRepository(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		owner     string
		repo      string
		wantErr   bool
	}{
		{name: "https", remoteURL: "https://github.com/pmenglund/colin.git", owner: "pmenglund", repo: "colin"},
		{name: "ssh", remoteURL: "git@github.com:pmenglund/colin.git", owner: "pmenglund", repo: "colin"},
		{name: "invalid", remoteURL: "/tmp/origin.git", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := parseGitHubRepository(tc.remoteURL)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGitHubRepository() error = %v", err)
			}
			if owner != tc.owner || repo != tc.repo {
				t.Fatalf("owner/repo = %q/%q, want %q/%q", owner, repo, tc.owner, tc.repo)
			}
		})
	}
}

func initTestGitRemote(t *testing.T) string {
	t.Helper()

	remotePath := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "init", "--bare", remotePath)
	return remotePath
}

func createTaskBranchWithCommit(t *testing.T, repoRoot string, branchName string, fileName string, content string) {
	t.Helper()

	runGit(t, "-C", repoRoot, "checkout", "-b", branchName)
	runGit(t, "-C", repoRoot, "config", "user.email", "colin-tests@example.com")
	runGit(t, "-C", repoRoot, "config", "user.name", "Colin Tests")
	runGit(t, "-C", repoRoot, "add", ".")
	writeFilePath := filepath.Join(repoRoot, fileName)
	if err := os.WriteFile(writeFilePath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", writeFilePath, err)
	}
	runGit(t, "-C", repoRoot, "add", fileName)
	runGit(t, "-C", repoRoot, "commit", "-m", "task commit")
	runGit(t, "-C", repoRoot, "checkout", "main")
}

type stubTokenProvider struct {
	token string
	err   error
	calls int
}

func (s *stubTokenProvider) Token(context.Context) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.token, nil
}
