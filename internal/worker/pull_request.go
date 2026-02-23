package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

const (
	defaultPullRequestBaseBranch = "main"
	defaultPullRequestRemoteName = "origin"
	defaultPullRequestBinary     = "gh"
)

// PullRequestManager ensures review-ready issues have a pull request URL.
type PullRequestManager interface {
	EnsurePullRequest(ctx context.Context, issue linear.Issue) (string, error)
}

// GitPullRequestManagerOptions configures GitPullRequestManager.
type GitPullRequestManagerOptions struct {
	RepoRoot   string
	BaseBranch string
	RemoteName string
	Binary     string
}

type commandOutput struct {
	Stdout string
	Stderr string
}

type commandRunner func(ctx context.Context, dir string, binary string, args []string) (commandOutput, error)

// GitPullRequestManager creates and looks up GitHub pull requests via gh CLI.
type GitPullRequestManager struct {
	repoRoot   string
	baseBranch string
	remoteName string
	binary     string
	runCommand commandRunner
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
	binary := strings.TrimSpace(opts.Binary)
	if binary == "" {
		binary = defaultPullRequestBinary
	}

	return &GitPullRequestManager{
		repoRoot:   filepath.Clean(strings.TrimSpace(opts.RepoRoot)),
		baseBranch: baseBranch,
		remoteName: remoteName,
		binary:     binary,
		runCommand: runExternalCommand,
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
	if _, err := remoteURL(repo, m.remoteName); err != nil {
		return "", fmt.Errorf("verify remote %q in %q: %w", m.remoteName, m.repoRoot, err)
	}
	if err := pushBranch(ctx, repo, m.remoteName, branchName); err != nil {
		return "", fmt.Errorf("push branch %q to remote %q: %w", branchName, m.remoteName, err)
	}

	prURL, err := m.findPullRequestURL(ctx, branchName, "open")
	if err != nil {
		return "", err
	}
	if prURL != "" {
		return prURL, nil
	}

	if err := m.createPullRequest(ctx, issue, branchName); err != nil {
		return "", err
	}

	prURL, err = m.findPullRequestURL(ctx, branchName, "open")
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

func (m *GitPullRequestManager) findPullRequestURL(ctx context.Context, branchName string, state string) (string, error) {
	args := []string{
		"pr", "list",
		"--head", strings.TrimSpace(branchName),
		"--base", strings.TrimSpace(m.baseBranch),
		"--state", strings.TrimSpace(state),
		"--limit", "1",
		"--json", "url",
	}
	out, err := m.runGH(ctx, args)
	if err != nil {
		return "", fmt.Errorf("lookup pull request for branch %q: %w", branchName, err)
	}

	var listed []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.Stdout)), &listed); err != nil {
		return "", fmt.Errorf("decode pull request lookup output for branch %q: %w", branchName, err)
	}
	if len(listed) == 0 {
		return "", nil
	}
	return strings.TrimSpace(listed[0].URL), nil
}

func (m *GitPullRequestManager) createPullRequest(ctx context.Context, issue linear.Issue, branchName string) error {
	title := buildPullRequestTitle(issue)
	body := buildPullRequestBody(issue)
	args := []string{
		"pr", "create",
		"--head", strings.TrimSpace(branchName),
		"--base", strings.TrimSpace(m.baseBranch),
		"--title", title,
		"--body", body,
	}
	if _, err := m.runGH(ctx, args); err != nil {
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

func (m *GitPullRequestManager) runGH(ctx context.Context, args []string) (commandOutput, error) {
	if m.runCommand == nil {
		m.runCommand = runExternalCommand
	}
	out, err := m.runCommand(ctx, m.repoRoot, m.binary, args)
	if err != nil {
		summary := strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(out.Stdout),
			strings.TrimSpace(out.Stderr),
		}, "\n"))
		if summary != "" {
			return out, fmt.Errorf("%w (%s)", err, summary)
		}
		return out, err
	}
	return out, nil
}

func runExternalCommand(ctx context.Context, dir string, binary string, args []string) (commandOutput, error) {
	cmd := exec.CommandContext(ctx, strings.TrimSpace(binary), args...)
	cmd.Dir = strings.TrimSpace(dir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return commandOutput{
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}, err
}
