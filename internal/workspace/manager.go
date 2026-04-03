package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/workflow"
)

var (
	ErrWorkspaceOutsideRoot = errors.New("invalid_workspace_cwd")
	ErrWorkspacePathExists  = errors.New("workspace_path_not_directory")
)

var invalidWorkspaceChar = regexp.MustCompile(`[^A-Za-z0-9._-]`)
var invalidBranchChar = regexp.MustCompile(`[^A-Za-z0-9._/-]`)

// Manager owns per-issue workspace lifecycle operations.
type Manager struct {
	root   string
	cfg    domain.ServiceConfig
	logger *slog.Logger
}

// NewManager creates a workspace manager for the current runtime config.
func NewManager(cfg domain.ServiceConfig, logger *slog.Logger) *Manager {
	return &Manager{
		root:   cfg.Workspace.Root,
		cfg:    cfg,
		logger: logger,
	}
}

// SanitizeWorkspaceKey converts an issue identifier into a safe directory name.
func SanitizeWorkspaceKey(identifier string) string {
	return invalidWorkspaceChar.ReplaceAllString(identifier, "_")
}

// SanitizeBranchName converts a rendered branch name into a git-safe ref name.
func SanitizeBranchName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "@{", "_")
	value = invalidBranchChar.ReplaceAllString(value, "_")
	for strings.Contains(value, "..") {
		value = strings.ReplaceAll(value, "..", "_")
	}
	parts := strings.Split(value, "/")
	sanitized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, ".")
		if part == "" {
			continue
		}
		if strings.HasSuffix(part, ".lock") {
			part = strings.TrimSuffix(part, ".lock") + "_lock"
		}
		sanitized = append(sanitized, part)
	}
	value = strings.Join(sanitized, "/")
	value = strings.Trim(value, "/.")
	if strings.HasPrefix(value, "-") {
		value = "_" + strings.TrimLeft(value, "-")
	}
	return value
}

// Ensure creates or reuses the workspace for an issue and runs first-creation setup.
func (m *Manager) Ensure(ctx context.Context, issue domain.Issue) (domain.Workspace, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return domain.Workspace{}, err
	}
	key := SanitizeWorkspaceKey(issue.Identifier)
	path := filepath.Join(m.root, workspaceRelativePath(m.cfg, target, key))
	if err := ensureWithinRoot(m.root, path); err != nil {
		return domain.Workspace{}, err
	}
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return domain.Workspace{}, err
	}

	info, err := os.Stat(path)
	createdNow := false
	switch {
	case err == nil && !info.IsDir():
		return domain.Workspace{}, ErrWorkspacePathExists
	case err == nil:
	case os.IsNotExist(err):
		if err := os.MkdirAll(path, 0o755); err != nil {
			return domain.Workspace{}, err
		}
		createdNow = true
	default:
		return domain.Workspace{}, err
	}

	workspace := domain.Workspace{
		Path:         path,
		WorkspaceKey: key,
		CreatedNow:   createdNow,
	}

	if strings.TrimSpace(target.RepoURL) != "" {
		if err := m.populateGitWorkspace(ctx, workspace, issue, target); err != nil {
			if createdNow {
				if cleanupErr := os.RemoveAll(path); cleanupErr != nil {
					m.logger.Warn("failed to clean up newly created workspace after git setup error", "workspace_path", path, "error", cleanupErr)
				}
			}
			return domain.Workspace{}, err
		}
	}

	if createdNow && strings.TrimSpace(m.cfg.Hooks.AfterCreate) != "" {
		if err := m.runHook(ctx, "after_create", m.cfg.Hooks.AfterCreate, workspace.Path); err != nil {
			if cleanupErr := os.RemoveAll(path); cleanupErr != nil {
				m.logger.Warn("failed to clean up newly created workspace after hook error", "workspace_path", path, "error", cleanupErr)
			}
			return domain.Workspace{}, err
		}
	}

	return workspace, nil
}

// RunBeforeRun executes the pre-run hook if configured.
func (m *Manager) RunBeforeRun(ctx context.Context, workspacePath string) error {
	if strings.TrimSpace(m.cfg.Hooks.BeforeRun) == "" {
		return nil
	}
	return m.runHook(ctx, "before_run", m.cfg.Hooks.BeforeRun, workspacePath)
}

// RunAfterRun executes the post-run hook if configured.
func (m *Manager) RunAfterRun(ctx context.Context, workspacePath string) error {
	if strings.TrimSpace(m.cfg.Hooks.AfterRun) == "" {
		return nil
	}
	return m.runHook(ctx, "after_run", m.cfg.Hooks.AfterRun, workspacePath)
}

// Remove deletes a workspace after running the best-effort before-remove hook.
func (m *Manager) Remove(ctx context.Context, workspacePath string) error {
	if workspacePath == "" {
		return nil
	}
	if err := ensureWithinRoot(m.root, workspacePath); err != nil {
		return err
	}
	if _, err := os.Stat(workspacePath); os.IsNotExist(err) {
		return nil
	}
	if strings.TrimSpace(m.cfg.Hooks.BeforeRemove) != "" {
		if err := m.runHook(ctx, "before_remove", m.cfg.Hooks.BeforeRemove, workspacePath); err != nil {
			m.logger.Warn("workspace hook failed", "hook", "before_remove", "workspace_path", workspacePath, "error", err)
		}
	}
	return os.RemoveAll(workspacePath)
}

func workspaceRelativePath(cfg domain.ServiceConfig, target domain.TargetConfig, issueKey string) string {
	if !cfg.MultiTarget() {
		return issueKey
	}
	return filepath.Join(SanitizeWorkspaceKey(target.Key), issueKey)
}

func (m *Manager) populateGitWorkspace(ctx context.Context, workspace domain.Workspace, issue domain.Issue, target domain.TargetConfig) error {
	gitDir := filepath.Join(workspace.Path, ".git")
	clonedNow := false
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := runCommand(ctx, workspace.Path, 2*time.Minute, "git", "clone", target.RepoURL, "."); err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
		clonedNow = true
	}
	if err := runCommand(ctx, workspace.Path, 2*time.Minute, "git", "fetch", "origin", "--prune"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	branch, err := branchName(issue, m.cfg.Repo.BranchTemplate)
	if err != nil {
		return err
	}
	if err := prepareBranch(ctx, workspace.Path, target.BaseRef, branch, clonedNow); err != nil {
		return err
	}
	return nil
}

func branchName(issue domain.Issue, templateText string) (string, error) {
	if issue.BranchName != nil && strings.TrimSpace(*issue.BranchName) != "" {
		return SanitizeBranchName(*issue.BranchName), nil
	}
	if rendered, err := workflow.RenderTemplate(strings.TrimSpace(templateText), map[string]any{
		"issue": map[string]any{
			"id":          issue.ID,
			"identifier":  issue.Identifier,
			"title":       issue.Title,
			"description": derefString(issue.Description),
			"state":       issue.State,
			"branch_name": derefString(issue.BranchName),
			"url":         derefString(issue.URL),
			"labels":      append([]string(nil), issue.Labels...),
		},
	}); err == nil {
		if branch := SanitizeBranchName(rendered); branch != "" {
			return branch, nil
		}
	} else {
		return "", fmt.Errorf("render branch template: %w", err)
	}
	return SanitizeBranchName(issue.Identifier), nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func prepareBranch(ctx context.Context, cwd, baseRef, branch string, clonedNow bool) error {
	if clonedNow {
		if err := runCommand(ctx, cwd, 2*time.Minute, "git", "checkout", baseRef); err != nil {
			return fmt.Errorf("git checkout base ref: %w", err)
		}
		return ensureBranch(ctx, cwd, baseRef, branch)
	}

	dirty, err := isDirty(ctx, cwd)
	if err != nil {
		return err
	}
	current, err := currentBranch(ctx, cwd)
	if err != nil {
		return err
	}
	if dirty {
		if current == branch {
			return nil
		}
		return fmt.Errorf("git workspace has uncommitted changes on branch %q; expected %q", current, branch)
	}
	if current == branch {
		return nil
	}
	return ensureBranch(ctx, cwd, baseRef, branch)
}

func ensureBranch(ctx context.Context, cwd, baseRef, branch string) error {
	if err := runCommand(ctx, cwd, 30*time.Second, "git", "rev-parse", "--verify", branch); err == nil {
		return runCommand(ctx, cwd, 30*time.Second, "git", "checkout", branch)
	}
	if err := runCommand(ctx, cwd, 30*time.Second, "git", "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch); err == nil {
		return runCommand(ctx, cwd, 30*time.Second, "git", "checkout", "-b", branch, "--track", "origin/"+branch)
	}
	if err := runCommand(ctx, cwd, 30*time.Second, "git", "checkout", baseRef); err != nil {
		return fmt.Errorf("git checkout base ref: %w", err)
	}
	return runCommand(ctx, cwd, 30*time.Second, "git", "checkout", "-b", branch)
}

func currentBranch(ctx context.Context, cwd string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, truncateOutput(output))
	}
	return strings.TrimSpace(string(output)), nil
}

func isDirty(ctx context.Context, cwd string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return false, ctx.Err()
	}
	if err != nil {
		return false, fmt.Errorf("%w: %s", err, truncateOutput(output))
	}
	return strings.TrimSpace(string(output)) != "", nil
}

func (m *Manager) runHook(parent context.Context, name, script, cwd string) error {
	if err := ensureWithinRoot(m.root, cwd); err != nil {
		return err
	}
	timeout := m.cfg.Hooks.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", script)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s hook timeout: %w", name, ctx.Err())
	}
	if err != nil {
		m.logger.Error("workspace hook failed", "hook", name, "workspace_path", cwd, "error", err, "output", truncateOutput(output))
		return fmt.Errorf("%s hook failed: %w", name, err)
	}
	m.logger.Info("workspace hook completed", "hook", name, "workspace_path", cwd)
	return nil
}

func runCommand(parent context.Context, cwd string, timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, truncateOutput(output))
	}
	return nil
}

func ensureWithinRoot(root, candidate string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ErrWorkspaceOutsideRoot
	}
	return nil
}

func truncateOutput(data []byte) string {
	out := strings.TrimSpace(string(data))
	if len(out) <= 4096 {
		return out
	}
	return out[:4096]
}
