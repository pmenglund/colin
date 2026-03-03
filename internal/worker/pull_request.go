package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

const (
	defaultPullRequestBaseBranch = "main"
	defaultPullRequestRemoteName = "origin"
	defaultGitHubAPIURL          = "https://api.github.com"
)

// PullRequestManager ensures review-ready issues have a pull request URL.
type PullRequestManager interface {
	EnsurePullRequest(ctx context.Context, issue linear.Issue) (string, error)
}

// GitPullRequestManagerOptions configures GitPullRequestManager.
type GitPullRequestManagerOptions struct {
	RepoRoot      string
	BaseBranch    string
	RemoteName    string
	APIBaseURL    string
	HTTPClient    *http.Client
	TokenProvider GitHubTokenProvider
}

// GitPullRequestManager creates and looks up GitHub pull requests via REST API.
type GitPullRequestManager struct {
	repoRoot       string
	baseBranch     string
	remoteName     string
	apiBaseURL     string
	httpClient     *http.Client
	tokenProvider  GitHubTokenProvider
	pushBranchFn   func(context.Context, *git.Repository, string, string, transport.AuthMethod) error
	resolveRepoURL func(string) (string, string, error)
	pushAuthFn     func(context.Context, string, GitHubTokenProvider) (transport.AuthMethod, error)
}

// NewGitPullRequestManager builds a git-backed pull-request manager.
func NewGitPullRequestManager(opts GitPullRequestManagerOptions) *GitPullRequestManager {
	baseBranch := strings.TrimSpace(opts.BaseBranch)
	if baseBranch == "" {
		baseBranch = defaultPullRequestBaseBranch
	}
	remoteName := strings.TrimSpace(opts.RemoteName)
	if remoteName == "" {
		remoteName = defaultPullRequestRemoteName
	}
	apiBaseURL := strings.TrimSpace(opts.APIBaseURL)
	if apiBaseURL == "" {
		apiBaseURL = defaultGitHubAPIURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &GitPullRequestManager{
		repoRoot:       filepath.Clean(strings.TrimSpace(opts.RepoRoot)),
		baseBranch:     baseBranch,
		remoteName:     remoteName,
		apiBaseURL:     strings.TrimRight(apiBaseURL, "/"),
		httpClient:     httpClient,
		tokenProvider:  opts.TokenProvider,
		pushBranchFn:   pushBranch,
		resolveRepoURL: parseGitHubRepository,
		pushAuthFn:     pushAuthMethodForRemote,
	}
}

// EnsurePullRequest creates a PR when needed and returns its URL.
func (m *GitPullRequestManager) EnsurePullRequest(ctx context.Context, issue linear.Issue) (string, error) {
	if m == nil {
		return "", errors.New("git pull request manager is nil")
	}
	if m.repoRoot == "" || m.repoRoot == "." {
		return "", errors.New("repo root is required")
	}

	existing := strings.TrimSpace(issue.Metadata[workflow.MetaPRURL])
	if existing != "" {
		return existing, nil
	}
	if m.tokenProvider == nil {
		return "", errors.New("github token provider is required")
	}

	branchName, err := pullRequestBranchName(issue)
	if err != nil {
		return "", err
	}

	repo, err := openRepository(m.repoRoot)
	if err != nil {
		return "", fmt.Errorf("inspect repository in %q: %w", m.repoRoot, err)
	}
	exists, err := gitBranchExists(repo, branchName)
	if err != nil {
		return "", fmt.Errorf("check branch %q in %q: %w", branchName, m.repoRoot, err)
	}
	if !exists {
		return "", fmt.Errorf("source branch %q does not exist in %q", branchName, m.repoRoot)
	}

	remoteRefURL, err := remoteURL(repo, m.remoteName)
	if err != nil {
		return "", fmt.Errorf("verify remote %q in %q: %w", m.remoteName, m.repoRoot, err)
	}
	auth, err := m.pushAuthFn(ctx, remoteRefURL, m.tokenProvider)
	if err != nil {
		return "", fmt.Errorf("resolve push auth for remote %q: %w", m.remoteName, err)
	}
	if err := m.pushBranchFn(ctx, repo, m.remoteName, branchName, auth); err != nil {
		return "", fmt.Errorf("push branch %q to remote %q: %w", branchName, m.remoteName, err)
	}

	owner, repoName, err := m.resolveRepoURL(remoteRefURL)
	if err != nil {
		return "", fmt.Errorf("resolve GitHub repository from remote %q: %w", m.remoteName, err)
	}

	prURL, err := m.findPullRequestURL(ctx, owner, repoName, branchName, "open")
	if err != nil {
		return "", err
	}
	if prURL != "" {
		return prURL, nil
	}

	if err := m.createPullRequest(ctx, owner, repoName, issue, branchName); err != nil {
		return "", err
	}

	prURL, err = m.findPullRequestURL(ctx, owner, repoName, branchName, "open")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prURL) == "" {
		return "", fmt.Errorf("created pull request for %q but could not resolve pull request URL", branchName)
	}
	return strings.TrimSpace(prURL), nil
}

func pullRequestBranchName(issue linear.Issue) (string, error) {
	branchName := strings.TrimSpace(issue.Metadata[workflow.MetaBranchName])
	if branchName != "" {
		return branchName, nil
	}
	identifier := strings.TrimSpace(issue.Identifier)
	if identifier == "" {
		return "", errors.New("issue identifier is required when metadata branch name is empty")
	}
	return "colin/" + identifier, nil
}

func (m *GitPullRequestManager) findPullRequestURL(
	ctx context.Context,
	owner string,
	repoName string,
	branchName string,
	state string,
) (string, error) {
	head := url.QueryEscape(owner + ":" + strings.TrimSpace(branchName))
	base := url.QueryEscape(strings.TrimSpace(m.baseBranch))
	stateQuery := url.QueryEscape(strings.TrimSpace(state))
	endpoint := fmt.Sprintf(
		"/repos/%s/%s/pulls?head=%s&base=%s&state=%s&per_page=1",
		url.PathEscape(owner),
		url.PathEscape(repoName),
		head,
		base,
		stateQuery,
	)

	body, err := m.doGitHubRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("lookup pull request for branch %q: %w", branchName, err)
	}

	var listed []struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		return "", fmt.Errorf("decode pull request lookup output for branch %q: %w", branchName, err)
	}
	if len(listed) == 0 {
		return "", nil
	}
	return strings.TrimSpace(listed[0].HTMLURL), nil
}

func (m *GitPullRequestManager) createPullRequest(
	ctx context.Context,
	owner string,
	repoName string,
	issue linear.Issue,
	branchName string,
) error {
	title := buildPullRequestTitle(issue)
	body := buildPullRequestBody(issue)

	payload, err := json.Marshal(map[string]string{
		"title": title,
		"head":  strings.TrimSpace(branchName),
		"base":  strings.TrimSpace(m.baseBranch),
		"body":  body,
	})
	if err != nil {
		return fmt.Errorf("marshal pull request payload for branch %q: %w", branchName, err)
	}

	endpoint := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(repoName))
	if _, err := m.doGitHubRequest(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload))); err != nil {
		return fmt.Errorf("create pull request for branch %q: %w", branchName, err)
	}
	return nil
}

func buildPullRequestTitle(issue linear.Issue) string {
	identifier := strings.TrimSpace(issue.Identifier)
	title := strings.TrimSpace(issue.Title)
	switch {
	case identifier != "" && title != "":
		return identifier + ": " + title
	case identifier != "":
		return identifier
	case title != "":
		return title
	default:
		return "Automated pull request from Colin"
	}
}

func buildPullRequestBody(issue linear.Issue) string {
	identifier := strings.TrimSpace(issue.Identifier)
	if identifier == "" {
		return "Automated pull request created by Colin."
	}
	return "Automated pull request created by Colin for Linear issue " + identifier + "."
}

func (m *GitPullRequestManager) doGitHubRequest(
	ctx context.Context,
	method string,
	endpoint string,
	body io.Reader,
) ([]byte, error) {
	if m.tokenProvider == nil {
		return nil, errors.New("github token provider is required")
	}
	if m.httpClient == nil {
		m.httpClient = http.DefaultClient
	}

	token, err := m.tokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve GitHub installation token: %w", err)
	}

	fullURL := strings.TrimRight(m.apiBaseURL, "/") + endpoint
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, endpoint, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response %s %s: %w", method, endpoint, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(
			"request %s %s returned status %d: %s",
			method,
			endpoint,
			resp.StatusCode,
			strings.TrimSpace(string(responseBody)),
		)
	}
	return responseBody, nil
}

func parseGitHubRepository(remoteURL string) (string, string, error) {
	trimmed := strings.TrimSpace(remoteURL)
	if trimmed == "" {
		return "", "", errors.New("remote URL is required")
	}

	var repositoryPath string
	switch {
	case strings.HasPrefix(trimmed, "git@") && strings.Contains(trimmed, ":"):
		parts := strings.SplitN(trimmed, ":", 2)
		repositoryPath = parts[1]
	case strings.Contains(trimmed, "://"):
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", "", fmt.Errorf("parse remote URL %q: %w", trimmed, err)
		}
		repositoryPath = parsed.Path
	default:
		return "", "", fmt.Errorf("unsupported remote URL format %q", trimmed)
	}

	repositoryPath = strings.TrimSpace(repositoryPath)
	repositoryPath = strings.TrimPrefix(repositoryPath, "/")
	repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	repositoryPath = path.Clean(repositoryPath)
	if repositoryPath == "." || repositoryPath == "" {
		return "", "", fmt.Errorf("remote URL %q does not contain repository path", trimmed)
	}

	segments := strings.Split(repositoryPath, "/")
	if len(segments) < 2 {
		return "", "", fmt.Errorf("remote URL %q does not include owner and repository", trimmed)
	}

	owner := strings.TrimSpace(segments[len(segments)-2])
	repoName := strings.TrimSpace(segments[len(segments)-1])
	if owner == "" || repoName == "" {
		return "", "", fmt.Errorf("remote URL %q does not include owner and repository", trimmed)
	}

	return owner, repoName, nil
}
