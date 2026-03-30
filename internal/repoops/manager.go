package repoops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/workflow"
)

var (
	ErrNotGitRepository           = errors.New("not_git_repository")
	ErrNoPullRequest              = errors.New("no_pull_request")
	ErrNoReviewableChanges        = errors.New("no_reviewable_changes")
	ErrDuplicatePullRequests      = errors.New("duplicate_pull_requests")
	ErrTrackedPullRequestMismatch = errors.New("tracked_pull_request_mismatch")
)

const codexReviewBotLogin = "chatgpt-codex-connector[bot]"

// Result captures the outcome of a publish or merge operation.
type Result struct {
	Branch    string
	BaseRef   string
	PRNumber  int
	PRURL     string
	PRState   string
	PRHeadRef string
	PRBaseRef string
	Commit    string
	Action    string
}

// ReviewContext captures the PR, unresolved review threads, and Codex review signals for an issue branch.
type ReviewContext struct {
	PullRequest            domain.PullRequestRef
	Threads                []domain.GitHubReviewThread
	CodexReviewThreads     []domain.GitHubReviewThread
	CodexReviewRequestedAt *time.Time
	CodexReviewApprovedAt  *time.Time
}

// Manager performs git and GitHub operations for a workspace.
type Manager struct {
	cfg    domain.ServiceConfig
	logger *slog.Logger
}

// NewManager constructs a repository automation manager.
func NewManager(cfg domain.ServiceConfig, logger *slog.Logger) *Manager {
	return &Manager{cfg: cfg, logger: logger}
}

// Publish commits workspace changes, pushes the issue branch, and creates or reuses a PR.
func (m *Manager) Publish(ctx context.Context, issue domain.Issue, workspacePath string) (Result, error) {
	branch, err := m.currentBranch(ctx, workspacePath)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Branch:  branch,
		BaseRef: m.cfg.Workspace.BaseRef,
	}

	dirty, err := m.isDirty(ctx, workspacePath)
	if err != nil {
		return Result{}, err
	}
	if dirty {
		if err := m.ensureIdentity(ctx, workspacePath); err != nil {
			return Result{}, err
		}
		if _, err := m.run(ctx, workspacePath, 30*time.Second, "git", "add", "-A"); err != nil {
			return Result{}, err
		}
		message := commitMessage(issue)
		if _, err := m.run(ctx, workspacePath, 30*time.Second, "git", "commit", "-m", message); err != nil {
			return Result{}, err
		}
		result.Action = "committed"
	}

	commit, err := m.revParse(ctx, workspacePath, "HEAD")
	if err == nil {
		result.Commit = commit
	}

	pr, created, err := m.resolvePullRequest(ctx, issue, workspacePath, branch, false)
	if err != nil {
		return Result{}, err
	}
	if pr == nil {
		reviewable, err := m.ReviewableArtifact(ctx, workspacePath)
		if err != nil {
			return Result{}, err
		}
		if !reviewable {
			return Result{}, ErrNoReviewableChanges
		}
	}

	if _, err := m.run(ctx, workspacePath, 2*time.Minute, "git", "push", "-u", m.cfg.Repo.RemoteName, branch); err != nil {
		return Result{}, err
	}
	if result.Action == "" {
		result.Action = "pushed"
	} else {
		result.Action = "committed_and_pushed"
	}

	if pr == nil {
		pr, created, err = m.resolvePullRequest(ctx, issue, workspacePath, branch, true)
		if err != nil {
			return Result{}, err
		}
		if pr == nil {
			return Result{}, ErrNoPullRequest
		}
	}
	if created {
		if result.Action == "pushed" {
			result.Action = "pushed_and_opened_pr"
		} else {
			result.Action += "_and_opened_pr"
		}
	}

	result.PRNumber = pr.Number
	result.PRURL = pr.URL
	result.PRState = pr.State
	result.PRHeadRef = pr.HeadRefName
	result.PRBaseRef = pr.BaseRefName
	return result, nil
}

// Merge ensures the current branch is published and merges its PR.
func (m *Manager) Merge(ctx context.Context, issue domain.Issue, workspacePath string) (Result, error) {
	result, err := m.Publish(ctx, issue, workspacePath)
	if err != nil {
		return Result{}, err
	}
	return m.MergePullRequest(ctx, workspacePath, result)
}

// MergePullRequest merges the pull request described by a prior publish result.
func (m *Manager) MergePullRequest(ctx context.Context, workspacePath string, result Result) (Result, error) {
	if strings.EqualFold(result.PRState, "MERGED") {
		result.Action = "already_merged"
		return result, nil
	}
	if result.PRNumber == 0 {
		return Result{}, ErrNoPullRequest
	}

	args := []string{"pr", "merge", fmt.Sprintf("%d", result.PRNumber), "--" + m.cfg.Repo.MergeMethod}
	if _, err := m.run(ctx, workspacePath, 2*time.Minute, "gh", args...); err != nil {
		return result, err
	}
	result.Action = "merged"
	result.PRState = "MERGED"
	return result, nil
}

// ReviewContext returns the current PR and unresolved review threads for the issue branch.
func (m *Manager) ReviewContext(ctx context.Context, issue domain.Issue, workspacePath string) (ReviewContext, error) {
	pr, _, err := m.resolvePullRequest(ctx, issue, workspacePath, "", false)
	if err != nil {
		return ReviewContext{}, err
	}
	if pr == nil {
		return ReviewContext{}, nil
	}

	owner, name, err := m.remoteRepository(ctx, workspacePath)
	if err != nil {
		return ReviewContext{}, err
	}

	threads, codexThreads, err := m.fetchReviewThreads(ctx, workspacePath, owner, name, pr.Number)
	if err != nil {
		return ReviewContext{}, err
	}
	requestedAt, approvedAt, err := m.fetchCodexReviewReactions(ctx, workspacePath, owner, name, pr.Number)
	if err != nil {
		return ReviewContext{}, err
	}
	return ReviewContext{
		PullRequest: domain.PullRequestRef{
			Number:  pr.Number,
			URL:     pr.URL,
			State:   pr.State,
			HeadRef: pr.HeadRefName,
			BaseRef: pr.BaseRefName,
		},
		Threads:                threads,
		CodexReviewThreads:     codexThreads,
		CodexReviewRequestedAt: requestedAt,
		CodexReviewApprovedAt:  approvedAt,
	}, nil
}

// CurrentBranch returns the current checked-out git branch for the workspace.
func (m *Manager) CurrentBranch(ctx context.Context, workspacePath string) (string, error) {
	return m.currentBranch(ctx, workspacePath)
}

// ReviewableArtifact reports whether the workspace contains reviewable repository changes.
func (m *Manager) ReviewableArtifact(ctx context.Context, workspacePath string) (bool, error) {
	dirty, err := m.isDirty(ctx, workspacePath)
	if err != nil {
		return false, err
	}
	if dirty {
		return true, nil
	}
	return m.branchAheadOfBase(ctx, workspacePath)
}

// ReplyAndResolveReviewThread posts a reply and resolves a review thread.
func (m *Manager) ReplyAndResolveReviewThread(ctx context.Context, workspacePath string, thread domain.GitHubReviewThread, body string) error {
	if strings.TrimSpace(thread.ID) == "" {
		return errors.New("missing review thread id")
	}
	if !thread.CanReply {
		return errors.New("review thread not replyable")
	}
	if !thread.CanResolve {
		return errors.New("review thread not resolvable")
	}
	replyBody := strings.TrimSpace(body)
	if replyBody == "" {
		return errors.New("missing review reply body")
	}

	const replyMutation = `mutation ReplyReviewThread($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: { pullRequestReviewThreadId: $threadId, body: $body }) {
    comment { id url }
  }
}`
	if _, err := m.runGraphQL(ctx, workspacePath, 30*time.Second, replyMutation, map[string]string{
		"threadId": thread.ID,
		"body":     replyBody,
	}); err != nil {
		return err
	}

	const resolveMutation = `mutation ResolveReviewThread($threadId: ID!) {
  resolveReviewThread(input: { threadId: $threadId }) {
    thread { id isResolved }
  }
}`
	_, err := m.runGraphQL(ctx, workspacePath, 30*time.Second, resolveMutation, map[string]string{
		"threadId": thread.ID,
	})
	return err
}

type pullRequest struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
}

func (m *Manager) currentBranch(ctx context.Context, workspacePath string) (string, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(out)
	if branch == "" || branch == "HEAD" {
		return "", ErrNotGitRepository
	}
	return branch, nil
}

func (m *Manager) reviewLookupBranches(ctx context.Context, issue domain.Issue, workspacePath string) []string {
	branches := make([]string, 0, 3)
	addBranch := func(branch string) {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			return
		}
		for _, existing := range branches {
			if existing == branch {
				return
			}
		}
		branches = append(branches, branch)
	}

	if current, err := m.currentBranch(ctx, workspacePath); err == nil {
		addBranch(current)
	}
	if issue.ColinMetadata != nil {
		addBranch(issue.ColinMetadata.ActualBranchName)
	}
	if issue.BranchName != nil {
		addBranch(*issue.BranchName)
	}
	return branches
}

func (m *Manager) revParse(ctx context.Context, workspacePath string, ref string) (string, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (m *Manager) isDirty(ctx context.Context, workspacePath string) (bool, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (m *Manager) branchAheadOfBase(ctx context.Context, workspacePath string) (bool, error) {
	baseRef, err := m.baseComparisonRef(ctx, workspacePath)
	if err != nil {
		return false, err
	}
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "rev-list", "--left-right", "--count", baseRef+"...HEAD")
	if err != nil {
		return false, err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return false, fmt.Errorf("unexpected git rev-list output: %q", strings.TrimSpace(out))
	}
	ahead, err := strconv.Atoi(fields[1])
	if err != nil {
		return false, fmt.Errorf("parse git rev-list ahead count: %w", err)
	}
	return ahead > 0, nil
}

func (m *Manager) baseComparisonRef(ctx context.Context, workspacePath string) (string, error) {
	candidates := []string{
		strings.TrimSpace(m.cfg.Workspace.BaseRef),
		strings.TrimSpace(m.cfg.Repo.RemoteName) + "/" + strings.TrimSpace(m.cfg.Workspace.BaseRef),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, err := m.revParse(ctx, workspacePath, candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("resolve base ref %q", strings.TrimSpace(m.cfg.Workspace.BaseRef))
}

func (m *Manager) ensureIdentity(ctx context.Context, workspacePath string) error {
	name, err := m.run(ctx, workspacePath, 15*time.Second, "git", "config", "user.name")
	if err == nil && strings.TrimSpace(name) != "" {
		email, err := m.run(ctx, workspacePath, 15*time.Second, "git", "config", "user.email")
		if err == nil && strings.TrimSpace(email) != "" {
			return nil
		}
	}
	if _, err := m.run(ctx, workspacePath, 15*time.Second, "git", "config", "user.name", "Colin"); err != nil {
		return err
	}
	if _, err := m.run(ctx, workspacePath, 15*time.Second, "git", "config", "user.email", "colin@local"); err != nil {
		return err
	}
	return nil
}

func (m *Manager) findPullRequest(ctx context.Context, workspacePath, branch string) (*pullRequest, error) {
	out, err := m.run(
		ctx,
		workspacePath,
		30*time.Second,
		"gh", "pr", "list",
		"--head", branch,
		"--base", m.cfg.Workspace.BaseRef,
		"--state", "all",
		"--json", "number,url,state,headRefName,baseRefName",
	)
	if err != nil {
		return nil, err
	}
	var prs []pullRequest
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

func (m *Manager) findPullRequestByNumber(ctx context.Context, workspacePath string, number int) (*pullRequest, error) {
	if number <= 0 {
		return nil, nil
	}
	out, err := m.run(
		ctx,
		workspacePath,
		30*time.Second,
		"gh", "pr", "view",
		fmt.Sprintf("%d", number),
		"--json", "number,url,state,headRefName,baseRefName",
	)
	if err != nil {
		return nil, err
	}
	var pr pullRequest
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return nil, err
	}
	if pr.Number == 0 {
		return nil, nil
	}
	return &pr, nil
}

func (m *Manager) resolvePullRequest(ctx context.Context, issue domain.Issue, workspacePath, currentBranch string, allowCreate bool) (*pullRequest, bool, error) {
	tracked, hasTracked := trackedPullRequest(issue)
	if hasTracked {
		pr, err := m.resolveTrackedPullRequest(ctx, workspacePath, tracked, currentBranch)
		return pr, false, err
	}

	owner, name, err := m.remoteRepository(ctx, workspacePath)
	if err != nil {
		return nil, false, err
	}

	attached := attachedPullRequestsForRepository(issue.AttachedPullRequests, owner, name)
	switch len(attached) {
	case 0:
	case 1:
		pr, err := m.findPullRequestByNumber(ctx, workspacePath, attached[0].Number)
		if err != nil {
			return nil, false, fmt.Errorf("resolve attached pull request #%d: %w", attached[0].Number, err)
		}
		if pr == nil {
			return nil, false, fmt.Errorf("attached pull request #%d not found", attached[0].Number)
		}
		return pr, false, nil
	default:
		return nil, false, duplicatePullRequestsError(attached)
	}

	if currentBranch != "" {
		pr, err := m.findPullRequest(ctx, workspacePath, currentBranch)
		if err != nil {
			return nil, false, err
		}
		if pr != nil {
			return pr, false, nil
		}
	} else {
		for _, branch := range m.reviewLookupBranches(ctx, issue, workspacePath) {
			pr, err := m.findPullRequest(ctx, workspacePath, branch)
			if err != nil {
				return nil, false, err
			}
			if pr != nil {
				return pr, false, nil
			}
		}
	}

	if !allowCreate || strings.TrimSpace(currentBranch) == "" {
		return nil, false, nil
	}

	url, err := m.createPullRequest(ctx, workspacePath, issue, currentBranch)
	if err != nil {
		return nil, false, err
	}
	pr, err := m.findPullRequest(ctx, workspacePath, currentBranch)
	if err != nil {
		return nil, false, err
	}
	if pr == nil {
		return nil, false, ErrNoPullRequest
	}
	pr.URL = url
	return pr, true, nil
}

func (m *Manager) resolveTrackedPullRequest(ctx context.Context, workspacePath string, tracked domain.PullRequestRef, currentBranch string) (*pullRequest, error) {
	pr, err := m.findPullRequestByNumber(ctx, workspacePath, tracked.Number)
	if err != nil {
		return nil, fmt.Errorf("view tracked pull request #%d: %w", tracked.Number, err)
	}
	if pr == nil {
		return nil, fmt.Errorf("tracked pull request #%d not found", tracked.Number)
	}
	if value := strings.TrimSpace(tracked.URL); value != "" && value != strings.TrimSpace(pr.URL) {
		return nil, fmt.Errorf("%w: tracked pull request url %q does not match GitHub url %q", ErrTrackedPullRequestMismatch, value, pr.URL)
	}
	if value := strings.TrimSpace(tracked.HeadRef); value != "" && value != strings.TrimSpace(pr.HeadRefName) {
		return nil, fmt.Errorf("%w: tracked pull request head %q does not match GitHub head %q", ErrTrackedPullRequestMismatch, value, pr.HeadRefName)
	}
	if value := strings.TrimSpace(tracked.BaseRef); value != "" && value != strings.TrimSpace(pr.BaseRefName) {
		return nil, fmt.Errorf("%w: tracked pull request base %q does not match GitHub base %q", ErrTrackedPullRequestMismatch, value, pr.BaseRefName)
	}
	if value := strings.TrimSpace(currentBranch); value != "" && strings.TrimSpace(pr.HeadRefName) != "" && value != strings.TrimSpace(pr.HeadRefName) {
		return nil, fmt.Errorf("%w: current branch %q does not match tracked pull request head %q", ErrTrackedPullRequestMismatch, value, pr.HeadRefName)
	}
	return pr, nil
}

func trackedPullRequest(issue domain.Issue) (domain.PullRequestRef, bool) {
	if issue.ColinMetadata == nil || issue.ColinMetadata.PullRequestNumber <= 0 {
		return domain.PullRequestRef{}, false
	}
	return domain.PullRequestRef{
		Number:  issue.ColinMetadata.PullRequestNumber,
		URL:     strings.TrimSpace(issue.ColinMetadata.PullRequestURL),
		State:   strings.TrimSpace(issue.ColinMetadata.PullRequestState),
		HeadRef: strings.TrimSpace(issue.ColinMetadata.PullRequestHeadRef),
		BaseRef: strings.TrimSpace(issue.ColinMetadata.PullRequestBaseRef),
	}, true
}

func attachedPullRequestsForRepository(prs []domain.PullRequestRef, owner, name string) []domain.PullRequestRef {
	if strings.EqualFold(owner, "local") {
		return prs
	}

	filtered := make([]domain.PullRequestRef, 0, len(prs))
	for _, pr := range prs {
		prOwner, prName, number, ok := parseGitHubPullRequestURL(pr.URL)
		if !ok || pr.Number <= 0 || pr.Number != number {
			continue
		}
		if !strings.EqualFold(prOwner, owner) || !strings.EqualFold(prName, name) {
			continue
		}
		filtered = append(filtered, pr)
	}
	return filtered
}

func parseGitHubPullRequestURL(rawURL string) (string, string, int, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
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

func duplicatePullRequestsError(prs []domain.PullRequestRef) error {
	refs := make([]string, 0, len(prs))
	for _, pr := range prs {
		refs = append(refs, fmt.Sprintf("#%d", pr.Number))
	}
	return fmt.Errorf("%w: multiple pull requests linked to this issue: %s", ErrDuplicatePullRequests, strings.Join(refs, ", "))
}

func (m *Manager) createPullRequest(ctx context.Context, workspacePath string, issue domain.Issue, branch string) (string, error) {
	title := fmt.Sprintf("%s: %s", issue.Identifier, issue.Title)
	body, err := m.prBody(issue, branch, title)
	if err != nil {
		return "", err
	}
	out, err := m.run(
		ctx,
		workspacePath,
		2*time.Minute,
		"gh", "pr", "create",
		"--base", m.cfg.Workspace.BaseRef,
		"--head", branch,
		"--title", title,
		"--body", body,
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (m *Manager) remoteRepository(ctx context.Context, workspacePath string) (string, string, error) {
	remoteURL, err := m.run(ctx, workspacePath, 15*time.Second, "git", "remote", "get-url", m.cfg.Repo.RemoteName)
	if err != nil {
		return "", "", err
	}
	return parseRemoteRepository(strings.TrimSpace(remoteURL))
}

func parseRemoteRepository(remoteURL string) (string, string, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", "", errors.New("empty remote url")
	}
	if strings.Contains(remoteURL, "://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return "", "", err
		}
		repoPath := strings.Trim(path.Clean(u.Path), "/")
		parts := strings.Split(repoPath, "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("unsupported remote url: %s", remoteURL)
		}
		return parts[len(parts)-2], strings.TrimSuffix(parts[len(parts)-1], ".git"), nil
	}
	if idx := strings.Index(remoteURL, ":"); idx >= 0 {
		repoPath := strings.Trim(remoteURL[idx+1:], "/")
		parts := strings.Split(repoPath, "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("unsupported remote url: %s", remoteURL)
		}
		return parts[len(parts)-2], strings.TrimSuffix(parts[len(parts)-1], ".git"), nil
	}
	if strings.HasSuffix(remoteURL, ".git") {
		return "local", strings.TrimSuffix(path.Base(remoteURL), ".git"), nil
	}
	return "", "", fmt.Errorf("unsupported remote url: %s", remoteURL)
}

func (m *Manager) fetchReviewThreads(ctx context.Context, workspacePath, owner, name string, prNumber int) ([]domain.GitHubReviewThread, []domain.GitHubReviewThread, error) {
	const query = `query ReviewThreads($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 50, after: $cursor) {
        nodes {
          id
          isResolved
          isOutdated
          viewerCanReply
          viewerCanResolve
          path
          line
          startLine
          comments(first: 20) {
            nodes {
              id
              body
              url
              createdAt
              author { login }
            }
            pageInfo {
              hasNextPage
              endCursor
            }
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

	var (
		cursor       string
		threads      []domain.GitHubReviewThread
		codexThreads []domain.GitHubReviewThread
	)
	for {
		resp, err := m.runGraphQL(ctx, workspacePath, 45*time.Second, query, map[string]string{
			"owner":  owner,
			"name":   name,
			"number": fmt.Sprintf("%d", prNumber),
			"cursor": cursor,
		})
		if err != nil {
			return nil, nil, err
		}

		nodes, ok := nestedSlice(resp, "data", "repository", "pullRequest", "reviewThreads", "nodes")
		if !ok {
			return nil, nil, nil
		}
		for _, node := range nodes {
			thread, ok := parseReviewThread(node)
			if !ok || thread.IsResolved {
				continue
			}
			threads = append(threads, thread)
			containsAuthor, err := m.reviewThreadContainsAuthor(ctx, workspacePath, node, codexReviewBotLogin)
			if err != nil {
				return nil, nil, err
			}
			if containsAuthor {
				codexThreads = append(codexThreads, thread)
			}
		}
		hasNextPage, _ := nestedBool(resp, "data", "repository", "pullRequest", "reviewThreads", "pageInfo", "hasNextPage")
		if !hasNextPage {
			break
		}
		nextCursor, ok := nestedString(resp, "data", "repository", "pullRequest", "reviewThreads", "pageInfo", "endCursor")
		if !ok || strings.TrimSpace(nextCursor) == "" {
			break
		}
		cursor = nextCursor
	}
	return threads, codexThreads, nil
}

func (m *Manager) reviewThreadContainsAuthor(ctx context.Context, workspacePath string, node map[string]any, login string) (bool, error) {
	if reviewThreadPageContainsAuthor(node, login) {
		return true, nil
	}
	if !reviewThreadCommentsHasNextPage(node) {
		return false, nil
	}
	threadID, _ := stringValue(node["id"])
	if strings.TrimSpace(threadID) == "" {
		return false, nil
	}
	return m.fetchReviewThreadCommentAuthor(ctx, workspacePath, threadID, login)
}

func (m *Manager) fetchReviewThreadCommentAuthor(ctx context.Context, workspacePath, threadID, login string) (bool, error) {
	const query = `query ReviewThreadComments($threadId: ID!, $cursor: String) {
  node(id: $threadId) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $cursor) {
        nodes {
          author { login }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

	cursor := ""
	for {
		resp, err := m.runGraphQL(ctx, workspacePath, 45*time.Second, query, map[string]string{
			"threadId": threadID,
			"cursor":   cursor,
		})
		if err != nil {
			return false, err
		}
		nodes, ok := nestedSlice(resp, "data", "node", "comments", "nodes")
		if ok {
			for _, node := range nodes {
				author, _ := nestedString(node, "author", "login")
				if strings.EqualFold(strings.TrimSpace(author), login) {
					return true, nil
				}
			}
		}
		hasNextPage, _ := nestedBool(resp, "data", "node", "comments", "pageInfo", "hasNextPage")
		if !hasNextPage {
			return false, nil
		}
		nextCursor, ok := nestedString(resp, "data", "node", "comments", "pageInfo", "endCursor")
		if !ok || strings.TrimSpace(nextCursor) == "" {
			return false, nil
		}
		cursor = nextCursor
	}
}

func (m *Manager) fetchCodexReviewReactions(ctx context.Context, workspacePath, owner, name string, prNumber int) (*time.Time, *time.Time, error) {
	const query = `query PullRequestReactions($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reactions(first: 100, after: $cursor) {
        nodes {
          content
          createdAt
          user { login }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

	var (
		cursor    string
		requested *time.Time
		approved  *time.Time
	)
	for {
		resp, err := m.runGraphQL(ctx, workspacePath, 45*time.Second, query, map[string]string{
			"owner":  owner,
			"name":   name,
			"number": fmt.Sprintf("%d", prNumber),
			"cursor": cursor,
		})
		if err != nil {
			return nil, nil, err
		}

		nodes, ok := nestedSlice(resp, "data", "repository", "pullRequest", "reactions", "nodes")
		if !ok {
			return requested, approved, nil
		}
		for _, node := range nodes {
			login, _ := nestedString(node, "user", "login")
			if !strings.EqualFold(strings.TrimSpace(login), codexReviewBotLogin) {
				continue
			}
			content, _ := stringValue(node["content"])
			createdAt, ok := parseTimestamp(node["createdAt"])
			if !ok {
				continue
			}
			switch strings.TrimSpace(content) {
			case "EYES":
				requested = latestTimePtr(requested, createdAt)
			case "THUMBS_UP":
				approved = latestTimePtr(approved, createdAt)
			}
		}
		hasNextPage, _ := nestedBool(resp, "data", "repository", "pullRequest", "reactions", "pageInfo", "hasNextPage")
		if !hasNextPage {
			break
		}
		nextCursor, ok := nestedString(resp, "data", "repository", "pullRequest", "reactions", "pageInfo", "endCursor")
		if !ok || strings.TrimSpace(nextCursor) == "" {
			break
		}
		cursor = nextCursor
	}
	return requested, approved, nil
}

func (m *Manager) runGraphQL(ctx context.Context, cwd string, timeout time.Duration, query string, vars map[string]string) (map[string]any, error) {
	args := []string{"api", "graphql", "-f", "query=" + query}
	for key, value := range vars {
		if strings.TrimSpace(value) == "" {
			continue
		}
		flag := "-F"
		if key == "body" {
			flag = "-f"
		}
		args = append(args, flag, fmt.Sprintf("%s=%s", key, value))
	}
	out, err := m.run(ctx, cwd, timeout, "gh", args...)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		return nil, err
	}
	if errorsField, ok := decoded["errors"]; ok && errorsField != nil {
		return nil, fmt.Errorf("graphql errors: %v", errorsField)
	}
	return decoded, nil
}

func parseReviewThread(node map[string]any) (domain.GitHubReviewThread, bool) {
	id, _ := stringValue(node["id"])
	pathValue, _ := stringValue(node["path"])
	if strings.TrimSpace(id) == "" || strings.TrimSpace(pathValue) == "" {
		return domain.GitHubReviewThread{}, false
	}
	comments, ok := nestedSlice(node, "comments", "nodes")
	if !ok || len(comments) == 0 {
		return domain.GitHubReviewThread{}, false
	}
	comment := comments[len(comments)-1]
	commentID, _ := stringValue(comment["id"])
	body, _ := stringValue(comment["body"])
	commentURL, _ := stringValue(comment["url"])
	author, _ := nestedString(comment, "author", "login")

	thread := domain.GitHubReviewThread{
		ID:         id,
		Path:       pathValue,
		CommentID:  commentID,
		CommentURL: commentURL,
		Author:     author,
		Body:       strings.TrimSpace(body),
		IsResolved: boolValue(node["isResolved"]),
		IsOutdated: boolValue(node["isOutdated"]),
		CanReply:   boolValue(node["viewerCanReply"]),
		CanResolve: boolValue(node["viewerCanResolve"]),
	}
	if value, ok := intValue(node["line"]); ok {
		thread.Line = &value
	}
	if value, ok := intValue(node["startLine"]); ok {
		thread.StartLine = &value
	}
	if createdAt, ok := parseTimestamp(comment["createdAt"]); ok {
		thread.CreatedAt = &createdAt
	}
	return thread, true
}

func reviewThreadPageContainsAuthor(node map[string]any, login string) bool {
	comments, ok := nestedSlice(node, "comments", "nodes")
	if !ok {
		return false
	}
	for _, comment := range comments {
		author, _ := nestedString(comment, "author", "login")
		if strings.EqualFold(strings.TrimSpace(author), login) {
			return true
		}
	}
	return false
}

func reviewThreadCommentsHasNextPage(node map[string]any) bool {
	hasNextPage, _ := nestedBool(node, "comments", "pageInfo", "hasNextPage")
	return hasNextPage
}

func nestedSlice(root map[string]any, keys ...string) ([]map[string]any, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok || value == nil {
		return nil, false
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		asMap, ok := item.(map[string]any)
		if ok {
			out = append(out, asMap)
		}
	}
	return out, true
}

func nestedBool(root map[string]any, keys ...string) (bool, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return false, false
	}
	return boolValue(value), true
}

func nestedString(root map[string]any, keys ...string) (string, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return "", false
	}
	return stringValue(value)
}

func nestedValue(root map[string]any, keys ...string) (any, bool) {
	current := any(root)
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = asMap[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func stringValue(value any) (string, bool) {
	v, ok := value.(string)
	return v, ok
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func parseTimestamp(value any) (time.Time, bool) {
	raw, ok := stringValue(value)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func latestTimePtr(current *time.Time, candidate time.Time) *time.Time {
	if current == nil || candidate.After(*current) {
		value := candidate
		return &value
	}
	return current
}

func (m *Manager) run(ctx context.Context, cwd string, timeout time.Duration, name string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, name, args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timeout: %w", name, cmdCtx.Err())
	}
	if err != nil {
		m.logger.Warn(
			"repo command failed",
			"command", commandString(name, args),
			"workspace_path", cwd,
			"error", err,
			"output", truncateOutput(string(output)),
		)
		return "", fmt.Errorf("%s: %w: %s", commandString(name, args), err, truncateOutput(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func commitMessage(issue domain.Issue) string {
	return fmt.Sprintf("%s: %s", issue.Identifier, issue.Title)
}

func (m *Manager) prBody(issue domain.Issue, branch string, prTitle string) (string, error) {
	templateText := strings.TrimSpace(m.cfg.Repo.PRTemplate)
	if templateText == "" {
		templateText = defaultPRTemplate()
	}
	return workflow.RenderTemplate(templateText, map[string]any{
		"issue":    prIssueMap(issue),
		"branch":   branch,
		"base_ref": m.cfg.Workspace.BaseRef,
		"pr_title": prTitle,
	})
}

func defaultPRTemplate() string {
	return `## Summary

Automated changes for {{.issue.identifier}}.

## Linear

- Issue: {{.issue.identifier}}
{{- if .issue.url }}
- URL: {{ .issue.url }}
{{- end }}`
}

func prIssueMap(issue domain.Issue) map[string]any {
	return map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": derefString(issue.Description),
		"state":       issue.State,
		"branch_name": derefString(issue.BranchName),
		"url":         derefString(issue.URL),
		"labels":      append([]string(nil), issue.Labels...),
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func commandString(name string, args []string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

func truncateOutput(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4096 {
		return value
	}
	return value[:4096]
}
