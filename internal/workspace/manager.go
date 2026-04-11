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
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/workflow"
)

var (
	ErrWorkspaceOutsideRoot = errors.New("invalid_workspace_cwd")
	ErrWorkspacePathExists  = errors.New("workspace_path_not_directory")
	ErrMissingIssueID       = errors.New("missing_issue_id")
)

var invalidWorkspaceChar = regexp.MustCompile(`[^A-Za-z0-9._-]`)
var invalidBranchChar = regexp.MustCompile(`[^A-Za-z0-9._/-]`)

// Manager owns per-issue workspace lifecycle operations.
type Manager struct {
	root     string
	cfg      domain.ServiceConfig
	logger   *slog.Logger
	gitSetup sync.Mutex
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
	workspace, target, err := m.WorkspaceForIssue(issue)
	if err != nil {
		return domain.Workspace{}, err
	}
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return domain.Workspace{}, err
	}

	info, err := os.Stat(workspace.Path)
	createdNow := false
	switch {
	case err == nil && !info.IsDir():
		return domain.Workspace{}, ErrWorkspacePathExists
	case err == nil:
	case os.IsNotExist(err):
		if err := os.MkdirAll(workspace.Path, 0o755); err != nil {
			return domain.Workspace{}, err
		}
		createdNow = true
	default:
		return domain.Workspace{}, err
	}
	workspace.CreatedNow = createdNow

	if strings.TrimSpace(target.RepoURL) != "" {
		if err := m.populateGitWorkspace(ctx, workspace, issue, target); err != nil {
			if createdNow {
				if cleanupErr := m.cleanupNewWorkspaceAfterError(ctx, workspace.Path); cleanupErr != nil {
					m.logger.Warn("failed to clean up newly created workspace after git setup error", "workspace_path", workspace.Path, "error", cleanupErr)
				}
			}
			return domain.Workspace{}, err
		}
	}

	if createdNow && strings.TrimSpace(m.cfg.Hooks.AfterCreate) != "" {
		if err := m.runHook(ctx, "after_create", m.cfg.Hooks.AfterCreate, workspace.Path); err != nil {
			if cleanupErr := m.cleanupNewWorkspaceAfterError(ctx, workspace.Path); cleanupErr != nil {
				m.logger.Warn("failed to clean up newly created workspace after hook error", "workspace_path", workspace.Path, "error", cleanupErr)
			}
			return domain.Workspace{}, err
		}
	}

	return workspace, nil
}

// WorkspaceForIssue returns the managed workspace path for an issue without creating it.
func (m *Manager) WorkspaceForIssue(issue domain.Issue) (domain.Workspace, domain.TargetConfig, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return domain.Workspace{}, domain.TargetConfig{}, err
	}
	key, relative, err := workspaceKeyAndRelativePath(m.cfg, target, issue)
	if err != nil {
		return domain.Workspace{}, domain.TargetConfig{}, err
	}
	path := filepath.Join(m.root, relative)
	if err := ensureWithinRoot(m.root, path); err != nil {
		return domain.Workspace{}, domain.TargetConfig{}, err
	}
	return domain.Workspace{
		Path:         path,
		WorkspaceKey: key,
	}, target, nil
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
	if isLinkedWorktree(workspacePath) {
		branch, branchErr := currentBranch(ctx, workspacePath)
		if branchErr != nil {
			m.logger.Warn("failed to read worktree branch before cleanup", "workspace_path", workspacePath, "error", branchErr)
		}
		commonDir, commonErr := gitCommonDir(ctx, workspacePath)
		if commonErr != nil {
			m.logger.Warn("failed to read worktree common git dir before cleanup", "workspace_path", workspacePath, "error", commonErr)
		}
		if err := runCommand(ctx, workspacePath, 30*time.Second, "git", "worktree", "remove", "--force", workspacePath); err != nil {
			return fmt.Errorf("git worktree remove: %w", err)
		}
		if branchErr == nil && commonErr == nil && m.isManagedCommonDir(commonDir) {
			if err := deleteLocalBranch(ctx, commonDir, branch); err != nil {
				m.logger.Warn("failed to delete local worktree branch", "workspace_path", workspacePath, "branch", branch, "error", err)
			}
		}
		return nil
	}
	return os.RemoveAll(workspacePath)
}

func workspaceRelativePath(cfg domain.ServiceConfig, target domain.TargetConfig, issueKey string) string {
	if !cfg.MultiTarget() {
		return issueKey
	}
	return filepath.Join(SanitizeWorkspaceKey(target.Key), issueKey)
}

func workspaceKeyAndRelativePath(cfg domain.ServiceConfig, target domain.TargetConfig, issue domain.Issue) (string, string, error) {
	if strings.TrimSpace(target.CheckoutPath) != "" {
		issueKey := SanitizeWorkspaceKey(issue.ID)
		if strings.TrimSpace(issueKey) == "" {
			return "", "", ErrMissingIssueID
		}
		projectKey := SanitizeWorkspaceKey(target.ProjectSlug)
		if strings.TrimSpace(projectKey) == "" {
			projectKey = SanitizeWorkspaceKey(target.Key)
		}
		if strings.TrimSpace(projectKey) == "" {
			projectKey = "project"
		}
		return issueKey, filepath.Join(projectKey, issueKey), nil
	}
	key := SanitizeWorkspaceKey(issue.Identifier)
	return key, workspaceRelativePath(cfg, target, key), nil
}

func (m *Manager) populateGitWorkspace(ctx context.Context, workspace domain.Workspace, issue domain.Issue, target domain.TargetConfig) error {
	branch, err := branchName(issue, target.EffectiveBranchTemplate(m.cfg.Repo.BranchTemplate))
	if err != nil {
		return err
	}
	remoteName := target.EffectiveRemoteName(m.cfg.Repo.RemoteName)
	if strings.TrimSpace(target.CheckoutPath) != "" {
		return m.populateCheckoutPathWorkspace(ctx, workspace.Path, target, remoteName, branch)
	}
	if isGitCheckout(ctx, workspace.Path) {
		if err := runCommand(ctx, workspace.Path, 2*time.Minute, "git", "fetch", remoteName, "--prune"); err != nil {
			return fmt.Errorf("git fetch: %w", err)
		}
		if err := prepareBranch(ctx, workspace.Path, remoteName, target.BaseRef, branch, false); err != nil {
			return err
		}
		return nil
	}
	if empty, err := isEmptyDir(workspace.Path); err != nil {
		return err
	} else if !empty {
		return ErrWorkspacePathExists
	}

	m.gitSetup.Lock()
	defer m.gitSetup.Unlock()

	source, err := m.prepareSharedCheckout(ctx, target)
	if err != nil {
		return err
	}
	if err := addWorktree(ctx, source, workspace.Path, remoteName, target.BaseRef, branch); err != nil {
		return err
	}
	return nil
}

func (m *Manager) populateCheckoutPathWorkspace(ctx context.Context, workspacePath string, target domain.TargetConfig, remoteName, branch string) error {
	if isGitCheckout(ctx, workspacePath) {
		if !isLinkedWorktree(workspacePath) {
			return ErrWorkspacePathExists
		}
		if _, err := m.prepareSharedCheckout(ctx, target); err != nil {
			return err
		}
		return prepareBranch(ctx, workspacePath, remoteName, target.BaseRef, branch, false)
	}
	if empty, err := isEmptyDir(workspacePath); err != nil {
		return err
	} else if !empty {
		return ErrWorkspacePathExists
	}

	m.gitSetup.Lock()
	defer m.gitSetup.Unlock()

	source, err := m.prepareSharedCheckout(ctx, target)
	if err != nil {
		return err
	}
	if err := addWorktree(ctx, source, workspacePath, remoteName, target.BaseRef, branch); err != nil {
		return err
	}
	return nil
}

func (m *Manager) prepareSharedCheckout(ctx context.Context, target domain.TargetConfig) (string, error) {
	remoteName := target.EffectiveRemoteName(m.cfg.Repo.RemoteName)
	if checkoutPath := strings.TrimSpace(target.CheckoutPath); checkoutPath != "" {
		if !isGitCheckout(ctx, checkoutPath) {
			return "", fmt.Errorf("checkout_path is not a git checkout: %s", checkoutPath)
		}
		if err := runCommand(ctx, checkoutPath, 2*time.Minute, "git", "fetch", remoteName, "--prune"); err != nil {
			return "", fmt.Errorf("git fetch checkout_path: %w", err)
		}
		return checkoutPath, nil
	}

	cacheRoot := strings.TrimSpace(m.cfg.Workspace.RepoCacheRoot)
	if cacheRoot == "" {
		cacheRoot = filepath.Join(filepath.Dir(m.root), "_repos")
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return "", fmt.Errorf("create repo cache root: %w", err)
	}
	source := filepath.Join(cacheRoot, repoCacheKey(target))
	if info, err := os.Stat(source); err == nil && !info.IsDir() {
		return "", ErrWorkspacePathExists
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if isGitCheckout(ctx, source) {
		if err := runCommand(ctx, source, 2*time.Minute, "git", "fetch", remoteName, "--prune"); err != nil {
			return "", fmt.Errorf("git fetch repo cache: %w", err)
		}
		return source, nil
	}
	if empty, err := pathMissingOrEmptyDir(source); err != nil {
		return "", err
	} else if !empty {
		return "", ErrWorkspacePathExists
	}
	if err := runCommand(ctx, filepath.Dir(source), 5*time.Minute, "git", "clone", target.RepoURL, source); err != nil {
		return "", fmt.Errorf("git clone repo cache: %w", err)
	}
	if err := runCommand(ctx, source, 2*time.Minute, "git", "fetch", remoteName, "--prune"); err != nil {
		return "", fmt.Errorf("git fetch repo cache: %w", err)
	}
	return source, nil
}

func addWorktree(ctx context.Context, sourcePath, workspacePath, remoteName, baseRef, branch string) error {
	switch {
	case branchExists(ctx, sourcePath, branch):
		return runCommand(ctx, sourcePath, 2*time.Minute, "git", "worktree", "add", workspacePath, branch)
	case remoteBranchExists(ctx, sourcePath, remoteName, branch):
		remoteBranch := remoteName + "/" + branch
		return runCommand(ctx, sourcePath, 2*time.Minute, "git", "worktree", "add", "-b", branch, "--track", workspacePath, remoteBranch)
	default:
		base, err := resolveBaseRef(ctx, sourcePath, remoteName, baseRef)
		if err != nil {
			return err
		}
		return runCommand(ctx, sourcePath, 2*time.Minute, "git", "worktree", "add", "-b", branch, workspacePath, base)
	}
}

func repoCacheKey(target domain.TargetConfig) string {
	if key := SanitizeWorkspaceKey(target.Key); strings.TrimSpace(key) != "" {
		return key
	}
	if name := strings.TrimSuffix(filepath.Base(strings.TrimSpace(target.RepoURL)), ".git"); name != "" && name != "." && name != string(filepath.Separator) {
		return SanitizeWorkspaceKey(name)
	}
	return "repo"
}

func branchExists(ctx context.Context, cwd, branch string) bool {
	if strings.TrimSpace(branch) == "" {
		return false
	}
	return runCommand(ctx, cwd, 30*time.Second, "git", "rev-parse", "--verify", "refs/heads/"+branch) == nil
}

func remoteBranchExists(ctx context.Context, cwd, remoteName, branch string) bool {
	if strings.TrimSpace(remoteName) == "" || strings.TrimSpace(branch) == "" {
		return false
	}
	return runCommand(ctx, cwd, 30*time.Second, "git", "show-ref", "--verify", "--quiet", "refs/remotes/"+remoteName+"/"+branch) == nil
}

func refExists(ctx context.Context, cwd, ref string) bool {
	if strings.TrimSpace(ref) == "" {
		return false
	}
	return runCommand(ctx, cwd, 30*time.Second, "git", "rev-parse", "--verify", ref) == nil
}

func resolveBaseRef(ctx context.Context, cwd, remoteName, baseRef string) (string, error) {
	baseRef = strings.TrimSpace(baseRef)
	if baseRef == "" {
		return "", errors.New("missing base ref")
	}
	if refExists(ctx, cwd, baseRef) {
		return baseRef, nil
	}
	if remoteBranchExists(ctx, cwd, remoteName, baseRef) {
		return remoteName + "/" + baseRef, nil
	}
	return "", fmt.Errorf("git base ref not found: %s", baseRef)
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

func prepareBranch(ctx context.Context, cwd, remoteName, baseRef, branch string, clonedNow bool) error {
	if clonedNow {
		if err := runCommand(ctx, cwd, 2*time.Minute, "git", "checkout", baseRef); err != nil {
			return fmt.Errorf("git checkout base ref: %w", err)
		}
		return ensureBranch(ctx, cwd, remoteName, baseRef, branch)
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
	return ensureBranch(ctx, cwd, remoteName, baseRef, branch)
}

func ensureBranch(ctx context.Context, cwd, remoteName, baseRef, branch string) error {
	if err := runCommand(ctx, cwd, 30*time.Second, "git", "rev-parse", "--verify", branch); err == nil {
		return runCommand(ctx, cwd, 30*time.Second, "git", "checkout", branch)
	}
	if err := runCommand(ctx, cwd, 30*time.Second, "git", "show-ref", "--verify", "--quiet", "refs/remotes/"+remoteName+"/"+branch); err == nil {
		return runCommand(ctx, cwd, 30*time.Second, "git", "checkout", "-b", branch, "--track", remoteName+"/"+branch)
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

func isGitCheckout(ctx context.Context, path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	output, err := runOutput(ctx, path, 15*time.Second, "git", "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(output) == "true"
}

func isLinkedWorktree(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && !info.IsDir()
}

func gitCommonDir(ctx context.Context, workspacePath string) (string, error) {
	output, err := runOutput(ctx, workspacePath, 15*time.Second, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(output)
	if path == "" {
		return "", errors.New("empty git common dir")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspacePath, path)
	}
	return filepath.Abs(path)
}

func deleteLocalBranch(ctx context.Context, commonDir, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return nil
	}
	return runCommand(ctx, "", 30*time.Second, "git", "--git-dir", commonDir, "branch", "-D", "--", branch)
}

func isEmptyDir(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func pathMissingOrEmptyDir(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func (m *Manager) isManagedCommonDir(commonDir string) bool {
	cacheRoot := strings.TrimSpace(m.cfg.Workspace.RepoCacheRoot)
	if cacheRoot == "" {
		cacheRoot = filepath.Join(filepath.Dir(m.root), "_repos")
	}
	if strings.TrimSpace(cacheRoot) == "" || strings.TrimSpace(commonDir) == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(cacheRoot); err == nil {
		cacheRoot = resolved
	}
	if resolved, err := filepath.EvalSymlinks(commonDir); err == nil {
		commonDir = resolved
	}
	return ensureWithinRoot(cacheRoot, commonDir) == nil
}

func (m *Manager) cleanupNewWorkspaceAfterError(ctx context.Context, workspacePath string) error {
	if isLinkedWorktree(workspacePath) {
		return runCommand(ctx, workspacePath, 30*time.Second, "git", "worktree", "remove", "--force", workspacePath)
	}
	return os.RemoveAll(workspacePath)
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
	_, err := runOutput(parent, cwd, timeout, name, args...)
	return err
}

func runOutput(parent context.Context, cwd string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, truncateOutput(output))
	}
	return string(output), nil
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
