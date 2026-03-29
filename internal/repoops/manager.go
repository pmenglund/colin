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
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/workflow"
)

var (
	ErrNotGitRepository = errors.New("not_git_repository")
	ErrNoPullRequest    = errors.New("no_pull_request")
)

const codexReviewBotLogin = "chatgpt-codex-connector[bot]"

// Result captures the outcome of a publish or merge operation.
type Result struct {
	Branch   string
	BaseRef  string
	PRNumber int
	PRURL    string
	PRState  string
	Commit   string
	Action   string
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

	if _, err := m.run(ctx, workspacePath, 2*time.Minute, "git", "push", "-u", m.cfg.Repo.RemoteName, branch); err != nil {
		return Result{}, err
	}
	if result.Action == "" {
		result.Action = "pushed"
	} else {
		result.Action = "committed_and_pushed"
	}

	pr, err := m.findPullRequest(ctx, workspacePath, branch)
	if err != nil {
		return Result{}, err
	}
	if pr == nil {
		url, err := m.createPullRequest(ctx, workspacePath, issue, branch)
		if err != nil {
			return Result{}, err
		}
		pr, err = m.findPullRequest(ctx, workspacePath, branch)
		if err != nil {
			return Result{}, err
		}
		if pr == nil {
			return Result{}, ErrNoPullRequest
		}
		pr.URL = url
		if result.Action == "pushed" {
			result.Action = "pushed_and_opened_pr"
		} else {
			result.Action += "_and_opened_pr"
		}
	} else if result.Action == "" {
		result.Action = "pr_already_open"
	}

	result.PRNumber = pr.Number
	result.PRURL = pr.URL
	result.PRState = pr.State
	return result, nil
}

// Merge ensures the current branch is published and merges its PR.
func (m *Manager) Merge(ctx context.Context, issue domain.Issue, workspacePath string) (Result, error) {
	result, err := m.Publish(ctx, issue, workspacePath)
	if err != nil {
		return Result{}, err
	}
	if strings.EqualFold(result.PRState, "MERGED") {
		result.Action = "already_merged"
		return result, nil
	}
	if result.PRNumber == 0 {
		return Result{}, ErrNoPullRequest
	}

	args := []string{"pr", "merge", fmt.Sprintf("%d", result.PRNumber), "--" + m.cfg.Repo.MergeMethod}
	if _, err := m.run(ctx, workspacePath, 2*time.Minute, "gh", args...); err != nil {
		return Result{}, err
	}
	result.Action = "merged"
	result.PRState = "MERGED"
	return result, nil
}

// ReviewContext returns the current PR and unresolved review threads for the issue branch.
func (m *Manager) ReviewContext(ctx context.Context, issue domain.Issue, workspacePath string) (ReviewContext, error) {
	branch := ""
	if issue.BranchName != nil {
		branch = strings.TrimSpace(*issue.BranchName)
	}
	if branch == "" {
		current, err := m.currentBranch(ctx, workspacePath)
		if err != nil {
			return ReviewContext{}, err
		}
		branch = current
	}

	pr, err := m.findPullRequest(ctx, workspacePath, branch)
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
			Number: pr.Number,
			URL:    pr.URL,
			State:  pr.State,
		},
		Threads:                threads,
		CodexReviewThreads:     codexThreads,
		CodexReviewRequestedAt: requestedAt,
		CodexReviewApprovedAt:  approvedAt,
	}, nil
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
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
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
		"--json", "number,url,state",
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
