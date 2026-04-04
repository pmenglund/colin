package github

import (
	"log/slog"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/githubauth"
	"github.com/pmenglund/colin/internal/repohost"
)

type Adapter struct{}

func (Adapter) Kind() repohost.HostKind {
	return repohost.HostKindGitHub
}

func (Adapter) DisplayName() string {
	return "GitHub"
}

func (Adapter) CurrentToken() string {
	return githubauth.CurrentToken()
}

func (Adapter) IsValidToken(value string) bool {
	return githubauth.IsValidToken(value)
}

func (Adapter) RecommendedEnvVar() string {
	return githubauth.RecommendedEnvVar
}

func (Adapter) ValidateTokenMessage() string {
	return "GITHUB_TOKEN should start with github_pat_ or ghp_."
}

func (Adapter) ParseRepositoryURL(raw string) (repohost.Repository, error) {
	return parseRepository(raw)
}

func (Adapter) ParsePullRequestURL(raw string) (string, string, int, bool) {
	return parsePullRequest(raw)
}

func (Adapter) RenderSetupInstructions(repo repohost.Repository, setupCommand string) string {
	return githubauth.RenderInstructions(githubauth.BuildSetupDetails(githubauth.Repository{
		Owner: repo.Owner,
		Name:  repo.Name,
		URL:   repo.URL,
	}), setupCommand)
}

func (Adapter) NewClient(cfg domain.ServiceConfig, logger *slog.Logger) (repohost.Client, error) {
	return NewClientFromConfig(cfg, logger)
}
