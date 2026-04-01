package repoops

import (
	"log/slog"
	"net/http"

	"github.com/pmenglund/colin/internal/domain"
	repogithub "github.com/pmenglund/colin/internal/repohost/github"
)

type goGitHubClient = repogithub.Client

// NewGitHubClientFromConfig constructs the real GitHub API client used by repo automation.
func NewGitHubClientFromConfig(cfg domain.ServiceConfig, logger *slog.Logger) (GitHubClient, error) {
	return repogithub.NewClientFromConfig(cfg, logger)
}

func newGoGitHubClient(token string, httpClient *http.Client, baseURL string, logger *slog.Logger) (*goGitHubClient, error) {
	return repogithub.NewClient(token, httpClient, baseURL, logger)
}
