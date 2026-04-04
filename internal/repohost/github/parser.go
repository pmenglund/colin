package github

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/pmenglund/colin/internal/repohost"
	giturls "github.com/whilp/git-urls"
)

func parseRepository(raw string) (repohost.Repository, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return repohost.Repository{}, fmt.Errorf("%w: empty repository url", repohost.ErrUnsupportedRepositoryURL)
	}
	parsed, err := giturls.Parse(raw)
	if err != nil {
		return repohost.Repository{}, fmt.Errorf("%w: %v", repohost.ErrUnsupportedRepositoryURL, err)
	}
	if !strings.EqualFold(strings.TrimSpace(parsed.Host), "github.com") {
		return repohost.Repository{}, fmt.Errorf("%w: host %q is not github.com", repohost.ErrUnsupportedRepositoryURL, parsed.Host)
	}
	repoPath := strings.Trim(strings.TrimSpace(parsed.Path), "/")
	parts := strings.Split(repoPath, "/")
	if len(parts) < 2 {
		return repohost.Repository{}, fmt.Errorf("%w: unsupported repository path %q", repohost.ErrUnsupportedRepositoryURL, parsed.Path)
	}
	owner := strings.TrimSpace(parts[len(parts)-2])
	name := strings.TrimSuffix(strings.TrimSpace(parts[len(parts)-1]), ".git")
	if owner == "" || name == "" {
		return repohost.Repository{}, fmt.Errorf("%w: unsupported repository path %q", repohost.ErrUnsupportedRepositoryURL, parsed.Path)
	}
	return repohost.Repository{
		Host:  "github.com",
		Owner: owner,
		Name:  name,
		URL:   raw,
	}, nil
}

func parsePullRequest(raw string) (owner string, repo string, number int, ok bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", 0, false
	}
	if !strings.EqualFold(strings.TrimSpace(parsed.Host), "github.com") {
		return "", "", 0, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[2], "pull") {
		return "", "", 0, false
	}
	number, err = strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", "", 0, false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), number, true
}
