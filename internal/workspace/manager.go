package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Workspace describes one managed per-issue workspace.
type Workspace struct {
	Path         string
	WorkspaceKey string
	CreatedNow   bool
	Metadata     map[string]string
}

// Populator prepares or reuses a workspace at the resolved path.
type Populator interface {
	Prepare(ctx context.Context, issueIdentifier string, workspacePath string) (map[string]string, error)
}

// HookConfig configures lifecycle hooks.
type HookConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	Timeout      time.Duration
}

// ManagerOptions configures a workspace manager.
type ManagerOptions struct {
	Root      string
	Hooks     HookConfig
	Populator Populator
	Logger    *slog.Logger
}

// Manager creates, reuses, and removes per-issue workspaces safely.
type Manager struct {
	root      string
	hooks     HookConfig
	populator Populator
	logger    *slog.Logger
}

// New returns a workspace manager rooted under opts.Root.
func New(opts ManagerOptions) (*Manager, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("workspace root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root %q: %w", root, err)
	}
	hooks := opts.Hooks
	if hooks.Timeout <= 0 {
		hooks.Timeout = time.Minute
	}
	return &Manager{
		root:      filepath.Clean(absRoot),
		hooks:     hooks,
		populator: opts.Populator,
		logger:    opts.Logger,
	}, nil
}

// Ensure creates or reuses a workspace for the issue identifier.
func (m *Manager) Ensure(ctx context.Context, issueIdentifier string) (Workspace, error) {
	if m == nil {
		return Workspace{}, errors.New("workspace manager is nil")
	}
	key := SanitizeKey(issueIdentifier)
	if key == "" {
		return Workspace{}, errors.New("issue identifier is required")
	}
	path, err := m.workspacePath(key)
	if err != nil {
		return Workspace{}, err
	}

	createdNow := false
	if info, statErr := os.Stat(path); statErr == nil {
		if !info.IsDir() {
			return Workspace{}, fmt.Errorf("workspace path %q exists and is not a directory", path)
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		createdNow = true
		if err := os.MkdirAll(m.root, 0o755); err != nil {
			return Workspace{}, fmt.Errorf("ensure workspace root %q: %w", m.root, err)
		}
	} else {
		return Workspace{}, fmt.Errorf("stat workspace path %q: %w", path, statErr)
	}

	metadata := map[string]string{}
	if m.populator != nil {
		populated, err := m.populator.Prepare(ctx, issueIdentifier, path)
		if err != nil {
			return Workspace{}, err
		}
		for key, value := range populated {
			metadata[key] = value
		}
	} else if createdNow {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return Workspace{}, fmt.Errorf("create workspace %q: %w", path, err)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return Workspace{}, fmt.Errorf("stat workspace path %q after prepare: %w", path, err)
	}
	if !info.IsDir() {
		return Workspace{}, fmt.Errorf("workspace path %q is not a directory", path)
	}

	ws := Workspace{Path: path, WorkspaceKey: key, CreatedNow: createdNow, Metadata: metadata}
	if createdNow && strings.TrimSpace(m.hooks.AfterCreate) != "" {
		if err := m.runHook(ctx, "after_create", ws.Path, m.hooks.AfterCreate); err != nil {
			_ = os.RemoveAll(ws.Path)
			return Workspace{}, err
		}
	}
	return ws, nil
}

// BeforeRun executes the configured before_run hook.
func (m *Manager) BeforeRun(ctx context.Context, ws Workspace) error {
	if m == nil || strings.TrimSpace(m.hooks.BeforeRun) == "" {
		return nil
	}
	return m.runHook(ctx, "before_run", ws.Path, m.hooks.BeforeRun)
}

// AfterRun executes the configured after_run hook and logs failures.
func (m *Manager) AfterRun(ctx context.Context, ws Workspace, _ error) {
	if m == nil || strings.TrimSpace(m.hooks.AfterRun) == "" {
		return
	}
	if err := m.runHook(ctx, "after_run", ws.Path, m.hooks.AfterRun); err != nil {
		m.log("workspace hook ignored", "hook", "after_run", "workspace", ws.Path, "error", err)
	}
}

// Remove deletes a workspace after the optional before_remove hook.
func (m *Manager) Remove(ctx context.Context, ws Workspace) error {
	if m == nil {
		return nil
	}
	path := strings.TrimSpace(ws.Path)
	if path == "" {
		return nil
	}
	if err := m.ensureUnderRoot(path); err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if strings.TrimSpace(m.hooks.BeforeRemove) != "" {
		if err := m.runHook(ctx, "before_remove", path, m.hooks.BeforeRemove); err != nil {
			m.log("workspace hook ignored", "hook", "before_remove", "workspace", path, "error", err)
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove workspace %q: %w", path, err)
	}
	return nil
}

// CleanupTerminal removes workspaces for terminal issue identifiers.
func (m *Manager) CleanupTerminal(ctx context.Context, issueIdentifiers []string) error {
	for _, identifier := range issueIdentifiers {
		path, err := m.workspacePath(SanitizeKey(identifier))
		if err != nil {
			return err
		}
		if err := m.Remove(ctx, Workspace{Path: path}); err != nil {
			return err
		}
	}
	return nil
}

// Root returns the normalized workspace root.
func (m *Manager) Root() string {
	if m == nil {
		return ""
	}
	return m.root
}

// SanitizeKey converts an issue identifier into a safe workspace directory name.
func SanitizeKey(issueIdentifier string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(issueIdentifier) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func (m *Manager) workspacePath(key string) (string, error) {
	if key == "" {
		return "", errors.New("workspace key is required")
	}
	path := filepath.Join(m.root, key)
	if err := m.ensureUnderRoot(path); err != nil {
		return "", err
	}
	return path, nil
}

func (m *Manager) ensureUnderRoot(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve workspace path %q: %w", path, err)
	}
	rel, err := filepath.Rel(m.root, absPath)
	if err != nil {
		return fmt.Errorf("resolve workspace path %q: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("workspace path %q escapes root %q", absPath, m.root)
	}
	return nil
}

func (m *Manager) runHook(ctx context.Context, name string, cwd string, script string) error {
	if err := m.ensureUnderRoot(cwd); err != nil {
		return err
	}
	hookCtx := ctx
	if hookCtx == nil {
		hookCtx = context.Background()
	}
	var cancel context.CancelFunc
	hookCtx, cancel = context.WithTimeout(hookCtx, m.hooks.Timeout)
	defer cancel()

	m.log("workspace hook start", "hook", name, "workspace", cwd)
	cmd := exec.CommandContext(hookCtx, "sh", "-lc", script)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if hookCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s hook timed out: %w", name, hookCtx.Err())
	}
	if err != nil {
		return fmt.Errorf("%s hook failed: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) log(msg string, args ...any) {
	if m.logger != nil {
		m.logger.Info(msg, args...)
	}
}
