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
)

var (
	ErrWorkspaceOutsideRoot = errors.New("invalid_workspace_cwd")
	ErrWorkspacePathExists  = errors.New("workspace_path_not_directory")
)

var invalidWorkspaceChar = regexp.MustCompile(`[^A-Za-z0-9._-]`)

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

// Ensure creates or reuses the workspace for an issue and runs first-creation setup.
func (m *Manager) Ensure(ctx context.Context, issue domain.Issue) (domain.Workspace, error) {
	key := SanitizeWorkspaceKey(issue.Identifier)
	path := filepath.Join(m.root, key)
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

	if m.cfg.Workspace.RepoURL != "" {
		if err := m.populateGitWorkspace(ctx, workspace, issue); err != nil {
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

func (m *Manager) populateGitWorkspace(ctx context.Context, workspace domain.Workspace, issue domain.Issue) error {
	gitDir := filepath.Join(workspace.Path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := runCommand(ctx, workspace.Path, 2*time.Minute, "git", "clone", m.cfg.Workspace.RepoURL, "."); err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
	}
	if err := runCommand(ctx, workspace.Path, 2*time.Minute, "git", "fetch", "origin", "--prune"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	if err := runCommand(ctx, workspace.Path, 2*time.Minute, "git", "checkout", m.cfg.Workspace.BaseRef); err != nil {
		return fmt.Errorf("git checkout base ref: %w", err)
	}
	branch := workspace.WorkspaceKey
	if issue.BranchName != nil && strings.TrimSpace(*issue.BranchName) != "" {
		branch = SanitizeWorkspaceKey(*issue.BranchName)
	}
	if err := ensureBranch(ctx, workspace.Path, branch); err != nil {
		return err
	}
	return nil
}

func ensureBranch(ctx context.Context, cwd, branch string) error {
	if err := runCommand(ctx, cwd, 30*time.Second, "git", "rev-parse", "--verify", branch); err == nil {
		return runCommand(ctx, cwd, 30*time.Second, "git", "checkout", branch)
	}
	return runCommand(ctx, cwd, 30*time.Second, "git", "checkout", "-b", branch)
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
