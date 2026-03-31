package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/pmenglund/colin/internal/githubauth"
	"github.com/pmenglund/colin/internal/workflow"
)

var ErrMissingGitHubRepository = errors.New("missing_github_repository")

// GitHubTokenSetupResult is the operator-facing GitHub token setup guidance for one repo.
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

// LoadGitHubTokenSetup loads the watched repository and returns the recommended token settings.
func LoadGitHubTokenSetup(workflowPath string, workingDir string, optionFns ...Option) (GitHubTokenSetupResult, error) {
	opts := buildOptions(optionFns...)

	repoURL, source, err := resolveGitHubRepositoryURL(workflowPath, workingDir, opts)
	if err != nil {
		return GitHubTokenSetupResult{}, err
	}
	repo, err := githubauth.ParseRepositoryURL(repoURL)
	if err != nil {
		return GitHubTokenSetupResult{}, err
	}
	details := githubauth.BuildSetupDetails(repo)

	return GitHubTokenSetupResult{
		RepositoryURL:         repo.URL,
		RepositoryOwner:       repo.Owner,
		RepositoryName:        repo.Name,
		RepositorySource:      source,
		FineGrainedTokenURL:   details.FineGrainedTokenURL,
		RecommendedEnvVar:     githubauth.RecommendedEnvVar,
		ClassicFallbackScope:  githubauth.ClassicFallbackScope,
		RecommendedPermission: "Contents: Read and write; Pull requests: Read and write",
	}, nil
}

func resolveGitHubRepositoryURL(workflowPath string, workingDir string, opts options) (string, string, error) {
	loader := workflow.Loader{}
	path := loader.ResolvePath(workflowPath)
	if _, err := os.Stat(path); err == nil {
		_, cfg, err := loadConfig(workflowPath, opts)
		if err != nil {
			return "", "", err
		}
		if repoURL := strings.TrimSpace(cfg.Workspace.RepoURL); repoURL != "" {
			return repoURL, path, nil
		}
	}

	remoteURL := gitOutput(workingDir, "config", "--get", "remote.origin.url")
	if remoteURL != "" {
		return remoteURL, "git remote origin", nil
	}

	return "", "", fmt.Errorf("%w: configure `workspace.repo_url` in WORKFLOW.md or set `remote.origin.url` in this checkout", ErrMissingGitHubRepository)
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
