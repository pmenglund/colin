package repoops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

var (
	ErrNotGitRepository = errors.New("not_git_repository")
	ErrNoPullRequest    = errors.New("no_pull_request")
)

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
	body := prBody(issue)
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

func prBody(issue domain.Issue) string {
	lines := []string{
		fmt.Sprintf("Automated changes for %s.", issue.Identifier),
	}
	if issue.URL != nil && strings.TrimSpace(*issue.URL) != "" {
		lines = append(lines, "", fmt.Sprintf("Linear: %s", strings.TrimSpace(*issue.URL)))
	}
	return strings.Join(lines, "\n")
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
