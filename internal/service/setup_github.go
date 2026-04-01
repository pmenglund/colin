package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/pmenglund/colin/internal/githubauth"
	"github.com/pmenglund/colin/internal/repohost"
	_ "github.com/pmenglund/colin/internal/repohost/github"
	"github.com/pmenglund/colin/internal/workflow"
)

var ErrMissingGitHubRepository = errors.New("missing_github_repository")

// RepoTokenSetupResult is the operator-facing repository-host token setup guidance for one repo.
type RepoTokenSetupResult struct {
	Backend            string
	BackendDisplayName string
	RepositoryURL      string
	RepositoryOwner    string
	RepositoryName     string
	RepositorySource   string
	Instructions       string
	RecommendedEnvVar  string
}

// GitHubTokenSetupResult is kept as a compatibility result shape for legacy callers.
type GitHubTokenSetupResult struct {
	RepositoryURL         string
	RepositoryOwner       string
	RepositoryName        string
	RepositorySource      string
	FineGrainedTokenURL   string
	RecommendedEnvVar     string
	ClassicFallbackScope  string
	RecommendedPermission string
}

// LoadRepoTokenSetup loads the watched repository and returns the recommended token settings for the configured backend.
func LoadRepoTokenSetup(workflowPath string, workingDir string, optionFns ...Option) (RepoTokenSetupResult, error) {
	opts := buildOptions(optionFns...)
	backend, repoURL, source, err := resolveRepoSetup(workflowPath, workingDir, opts)
	if err != nil {
		return RepoTokenSetupResult{}, err
	}

	adapter, err := repohost.Lookup(backend)
	if err != nil {
		return RepoTokenSetupResult{}, err
	}
	repo, err := adapter.ParseRepositoryURL(repoURL)
	if err != nil {
		return RepoTokenSetupResult{}, err
	}

	return RepoTokenSetupResult{
		Backend:            backend,
		BackendDisplayName: adapter.DisplayName(),
		RepositoryURL:      repo.URL,
		RepositoryOwner:    repo.Owner,
		RepositoryName:     repo.Name,
		RepositorySource:   source,
		Instructions:       adapter.RenderSetupInstructions(repo, "colin setup repo"),
		RecommendedEnvVar:  adapter.RecommendedEnvVar(),
	}, nil
}

// LoadGitHubTokenSetup loads the watched repository and returns the recommended GitHub token settings.
func LoadGitHubTokenSetup(workflowPath string, workingDir string, optionFns ...Option) (GitHubTokenSetupResult, error) {
	result, err := LoadRepoTokenSetup(workflowPath, workingDir, optionFns...)
	if err != nil {
		return GitHubTokenSetupResult{}, err
	}
	if !strings.EqualFold(result.Backend, string(repohost.HostKindGitHub)) {
		return GitHubTokenSetupResult{}, fmt.Errorf("workflow backend is %q, use `colin setup repo` instead", result.Backend)
	}
	details := githubauth.BuildSetupDetails(githubauth.Repository{
		Owner: result.RepositoryOwner,
		Name:  result.RepositoryName,
		URL:   result.RepositoryURL,
	})
	return GitHubTokenSetupResult{
		RepositoryURL:         result.RepositoryURL,
		RepositoryOwner:       result.RepositoryOwner,
		RepositoryName:        result.RepositoryName,
		RepositorySource:      result.RepositorySource,
		FineGrainedTokenURL:   details.FineGrainedTokenURL,
		RecommendedEnvVar:     githubauth.RecommendedEnvVar,
		ClassicFallbackScope:  githubauth.ClassicFallbackScope,
		RecommendedPermission: "Contents: Read and write; Pull requests: Read and write",
	}, nil
}

func resolveRepoSetup(workflowPath string, workingDir string, opts options) (string, string, string, error) {
	backend := string(repohost.HostKindGitHub)
	loader := workflow.Loader{}
	path := loader.ResolvePath(workflowPath)
	if _, err := os.Stat(path); err == nil {
		_, cfg, err := loadConfig(workflowPath, opts)
		if err != nil {
			return "", "", "", err
		}
		backend = repohost.NormalizeBackend(cfg.Repo.Backend)
		if repoURL := strings.TrimSpace(cfg.Workspace.RepoURL); repoURL != "" {
			return backend, repoURL, path, nil
		}
	}

	remoteURL := gitOutput(workingDir, "config", "--get", "remote.origin.url")
	if remoteURL != "" {
		return backend, remoteURL, "git remote origin", nil
	}

	return "", "", "", fmt.Errorf("%w: configure `workspace.repo_url` in WORKFLOW.md or set `remote.origin.url` in this checkout", ErrMissingGitHubRepository)
}

func gitOutput(workingDir string, args ...string) string {
	if strings.TrimSpace(workingDir) == "" {
		return ""
	}
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = workingDir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
