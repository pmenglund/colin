package github

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/githubauth"
	"github.com/pmenglund/colin/internal/repohost"
)

type Adapter struct{}

func init() {
	repohost.Register(Adapter{})
}

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
	repo, err := githubauth.ParseRepositoryURL(raw)
	if err != nil {
		if errors.Is(err, githubauth.ErrUnsupportedRepositoryURL) {
			return repohost.Repository{}, fmt.Errorf("%w: %v", repohost.ErrUnsupportedRepositoryURL, err)
		}
		return repohost.Repository{}, err
	}
	return repohost.Repository{
		Host:  "github.com",
		Owner: repo.Owner,
		Name:  repo.Name,
		URL:   repo.URL,
	}, nil
}

func (Adapter) ParsePullRequestURL(raw string) (string, string, int, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", 0, false
	}
	if !strings.EqualFold(parsed.Host, "github.com") {
		return "", "", 0, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[2], "pull") {
		return "", "", 0, false
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", "", 0, false
	}
	return parts[0], parts[1], number, true
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
