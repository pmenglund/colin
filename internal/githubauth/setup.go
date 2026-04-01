package githubauth

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
)

const (
	RecommendedEnvVar     = "GITHUB_TOKEN"
	ClassicFallbackScope  = "repo"
	FineGrainedTokenType  = "fine-grained personal access token"
	DefaultExpirationDays = 90
)

var ErrUnsupportedRepositoryURL = errors.New("unsupported_github_repository_url")

// Repository identifies one GitHub repository Colin should access.
type Repository struct {
	Owner string
	Name  string
	URL   string
}

// SetupDetails captures the token settings Colin recommends for one repository.
type SetupDetails struct {
	Repository          Repository
	FineGrainedTokenURL string
}

// ParseRepositoryURL extracts the owner and repository name from a github.com remote URL.
func ParseRepositoryURL(raw string) (Repository, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Repository{}, fmt.Errorf("%w: empty repository url", ErrUnsupportedRepositoryURL)
	}

	if strings.Contains(raw, "://") {
		return parseURLRepository(raw)
	}
	return parseSCPRepository(raw)
}

// CurrentToken returns the first configured GitHub token Colin accepts.
func CurrentToken() string {
	return strings.TrimSpace(firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")))
}

// IsValidToken reports whether the value looks like a GitHub token Colin can use.
func IsValidToken(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "github_pat_") || strings.HasPrefix(trimmed, "ghp_")
}

// BuildSetupDetails returns the recommended GitHub token settings for one repository.
func BuildSetupDetails(repo Repository) SetupDetails {
	values := url.Values{}
	values.Set("name", tokenName(repo.Name))
	values.Set("description", fmt.Sprintf("Colin publish and merge automation for %s/%s", repo.Owner, repo.Name))
	values.Set("target_name", repo.Owner)
	values.Set("expires_in", fmt.Sprintf("%d", DefaultExpirationDays))
	values.Set("contents", "write")
	values.Set("pull_requests", "write")

	return SetupDetails{
		Repository:          repo,
		FineGrainedTokenURL: "https://github.com/settings/personal-access-tokens/new?" + values.Encode(),
	}
}

// RenderInstructions returns the operator-facing token setup steps for one repository.
func RenderInstructions(details SetupDetails, setupCommand string) string {
	var lines []string
	lines = append(lines, "GitHub token setup:")
	lines = append(lines, "- Recommended token type: "+FineGrainedTokenType)
	lines = append(lines, "- Resource owner: "+details.Repository.Owner)
	lines = append(lines, "- Repository access: Only select repositories")
	lines = append(lines, "- Selected repository: "+details.Repository.Name)
	lines = append(lines, "- Repository permissions: Contents: Read and write; Pull requests: Read and write")
	lines = append(lines, "- Export it as: "+RecommendedEnvVar)
	lines = append(lines, "- Colin also accepts GH_TOKEN and repo.api_token, but "+RecommendedEnvVar+" is the recommended path.")
	if strings.TrimSpace(setupCommand) != "" {
		lines = append(lines, "- Fast path: "+strings.TrimSpace(setupCommand))
	}
	lines = append(lines, "- Create token: "+details.FineGrainedTokenURL)
	lines = append(lines, "- GitHub still requires you to choose the repository in the UI before you generate the token.")
	lines = append(lines, "- Fallback: use a classic personal access token with the `"+ClassicFallbackScope+"` scope if fine-grained tokens are blocked.")
	lines = append(lines, "- If the org uses SAML SSO, classic tokens may need `Configure SSO` after creation.")
	return strings.Join(lines, "\n")
}

func parseURLRepository(raw string) (Repository, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return Repository{}, fmt.Errorf("%w: %v", ErrUnsupportedRepositoryURL, err)
	}
	if !strings.EqualFold(parsed.Host, "github.com") {
		return Repository{}, fmt.Errorf("%w: host %q is not github.com", ErrUnsupportedRepositoryURL, parsed.Host)
	}
	owner, name, err := repositoryParts(parsed.Path)
	if err != nil {
		return Repository{}, err
	}
	return Repository{Owner: owner, Name: name, URL: raw}, nil
}

func parseSCPRepository(raw string) (Repository, error) {
	hostAndPath := strings.SplitN(raw, ":", 2)
	if len(hostAndPath) != 2 {
		return Repository{}, fmt.Errorf("%w: unsupported repository url %q", ErrUnsupportedRepositoryURL, raw)
	}
	host := hostAndPath[0]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if !strings.EqualFold(host, "github.com") {
		return Repository{}, fmt.Errorf("%w: host %q is not github.com", ErrUnsupportedRepositoryURL, host)
	}
	owner, name, err := repositoryParts(hostAndPath[1])
	if err != nil {
		return Repository{}, err
	}
	return Repository{Owner: owner, Name: name, URL: raw}, nil
}

func repositoryParts(rawPath string) (string, string, error) {
	repoPath := strings.Trim(path.Clean(strings.TrimSpace(rawPath)), "/")
	parts := strings.Split(repoPath, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("%w: unsupported repository path %q", ErrUnsupportedRepositoryURL, rawPath)
	}
	owner := strings.TrimSpace(parts[len(parts)-2])
	name := strings.TrimSuffix(strings.TrimSpace(parts[len(parts)-1]), ".git")
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("%w: unsupported repository path %q", ErrUnsupportedRepositoryURL, rawPath)
	}
	return owner, name, nil
}

func tokenName(repo string) string {
	name := strings.TrimSpace(repo)
	if name == "" {
		return "Colin"
	}
	name = "Colin " + name
	if len(name) <= 40 {
		return name
	}
	return name[:40]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
