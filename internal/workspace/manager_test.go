package workspace

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/domain"
)

func TestEnsureRunsAfterCreateOnlyOnce(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{Root: root},
		Hooks: domain.HookConfig{
			AfterCreate: `if [ -e count.txt ]; then touch repeated.txt; fi
touch count.txt`,
		},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	issue := domain.Issue{Identifier: "ABC-123"}

	ws, err := manager.Ensure(context.Background(), issue)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "count.txt")); err != nil {
		t.Fatalf("count.txt missing: %v", err)
	}

	if _, err := manager.Ensure(context.Background(), issue); err != nil {
		t.Fatalf("Ensure() second error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "repeated.txt")); !os.IsNotExist(err) {
		t.Fatalf("repeated.txt should not exist, err = %v", err)
	}
}

func TestEnsurePopulatesGitWorkspace(t *testing.T) {
	t.Parallel()

	origin := filepath.Join(t.TempDir(), "origin")
	mustRun(t, "", "git", "init", "-b", "main", origin)
	mustRun(t, origin, "git", "config", "user.email", "test@example.com")
	mustRun(t, origin, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, origin, "git", "add", "README.md")
	mustRun(t, origin, "git", "commit", "-m", "init")

	root := filepath.Join(t.TempDir(), "workspaces")
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:    root,
			RepoURL: origin,
			BaseRef: "main",
		},
		Repo: domain.RepoConfig{
			BranchTemplate: "colin/{{.issue.title}}",
		},
		Hooks: domain.HookConfig{},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	branch := "feature/ABC-123"
	ws, err := manager.Ensure(context.Background(), domain.Issue{
		Identifier: "ABC-123",
		BranchName: &branch,
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws.Path, "README.md"))
	if err != nil {
		t.Fatalf("README.md missing: %v", err)
	}
	if strings.TrimSpace(string(data)) != "hello" {
		t.Fatalf("README.md = %q", string(data))
	}

	output, err := exec.Command("git", "-C", ws.Path, "branch", "--show-current").CombinedOutput()
	if err != nil {
		t.Fatalf("branch --show-current: %v (%s)", err, string(output))
	}
	if got := strings.TrimSpace(string(output)); got != "feature/ABC-123" {
		t.Fatalf("current branch = %q", got)
	}
}

func TestEnsureCreatesWorktreesFromManagedRepoCache(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t)
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "workspaces")
	repoCacheRoot := filepath.Join(tempDir, "_repos")
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:          root,
			RepoCacheRoot: repoCacheRoot,
			RepoURL:       origin,
			BaseRef:       "main",
		},
		Repo: domain.RepoConfig{
			RemoteName:     "origin",
			BranchTemplate: "colin/{{.issue.identifier}}",
		},
	}
	cfg.Targets = []domain.TargetConfig{{
		Key:         "project-repo",
		Name:        "target-name",
		ProjectSlug: "project",
		RepoURL:     origin,
		BaseRef:     "main",
	}}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	first, err := manager.Ensure(context.Background(), domain.Issue{Identifier: "ABC-123", ProjectSlug: "project"})
	if err != nil {
		t.Fatalf("Ensure(first) error = %v", err)
	}
	second, err := manager.Ensure(context.Background(), domain.Issue{Identifier: "ABC-124", ProjectSlug: "project"})
	if err != nil {
		t.Fatalf("Ensure(second) error = %v", err)
	}
	if first.Path != filepath.Join(root, "target-name", "ABC-123") {
		t.Fatalf("first.Path = %q, want target-name/ABC-123 layout", first.Path)
	}
	if second.Path != filepath.Join(root, "target-name", "ABC-124") {
		t.Fatalf("second.Path = %q, want target-name/ABC-124 layout", second.Path)
	}

	cachePath := filepath.Join(repoCacheRoot, "project-repo")
	if _, err := os.Stat(filepath.Join(cachePath, ".git")); err != nil {
		t.Fatalf("managed cache .git missing: %v", err)
	}
	for _, path := range []string{first.Path, second.Path} {
		info, err := os.Stat(filepath.Join(path, ".git"))
		if err != nil {
			t.Fatalf("%s .git missing: %v", path, err)
		}
		if info.IsDir() {
			t.Fatalf("%s .git is a directory, want linked worktree file", path)
		}
	}

	firstPath := realPath(t, first.Path)
	secondPath := realPath(t, second.Path)
	output := mustRunOutput(t, cachePath, "git", "worktree", "list", "--porcelain")
	if !strings.Contains(output, "worktree "+firstPath) || !strings.Contains(output, "worktree "+secondPath) {
		t.Fatalf("worktree list = %q, want both issue worktrees", output)
	}
}

func TestEnsureUsesConfiguredCheckoutPathForWorktrees(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t)
	tempDir := t.TempDir()
	checkoutPath := filepath.Join(tempDir, "checkout")
	mustRun(t, "", "git", "clone", origin, checkoutPath)
	root := filepath.Join(tempDir, "workspaces")
	repoCacheRoot := filepath.Join(tempDir, "_repos")
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:          root,
			RepoCacheRoot: repoCacheRoot,
			RepoURL:       origin,
			BaseRef:       "main",
			CheckoutPath:  checkoutPath,
		},
		Repo: domain.RepoConfig{
			RemoteName:     "origin",
			BranchTemplate: "colin/{{.issue.identifier}}",
		},
	}
	cfg.Targets = []domain.TargetConfig{{
		Key:          "project-repo",
		Name:         "target-name",
		ProjectSlug:  "project",
		RepoURL:      origin,
		BaseRef:      "main",
		CheckoutPath: checkoutPath,
	}}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ws, err := manager.Ensure(context.Background(), domain.Issue{
		ID:          "issue-uuid",
		Identifier:  "ABC-123",
		ProjectSlug: "project",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	wantPath := filepath.Join(root, "target-name", "ABC-123")
	if ws.Path != wantPath {
		t.Fatalf("ws.Path = %q, want %q", ws.Path, wantPath)
	}
	if _, err := os.Stat(repoCacheRoot); !os.IsNotExist(err) {
		t.Fatalf("repo cache root should not exist when checkout_path is used, err = %v", err)
	}
	wsPath := realPath(t, ws.Path)
	output := mustRunOutput(t, checkoutPath, "git", "worktree", "list", "--porcelain")
	if got := strings.Count(output, "worktree "+wsPath); got != 1 {
		t.Fatalf("worktree list contains workspace %d times, want 1\n%s", got, output)
	}

	reused, err := manager.Ensure(context.Background(), domain.Issue{
		ID:          "issue-uuid",
		Identifier:  "ABC-123",
		ProjectSlug: "project",
	})
	if err != nil {
		t.Fatalf("Ensure() reuse error = %v", err)
	}
	if reused.Path != ws.Path {
		t.Fatalf("reused.Path = %q, want %q", reused.Path, ws.Path)
	}
	output = mustRunOutput(t, checkoutPath, "git", "worktree", "list", "--porcelain")
	if got := strings.Count(output, "worktree "+wsPath); got != 1 {
		t.Fatalf("worktree list contains workspace %d times after reuse, want 1\n%s", got, output)
	}
}

func TestEnsureUsesConfiguredCheckoutPathWhenWorkspaceRootIsInsideCheckout(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t)
	tempDir := t.TempDir()
	checkoutPath := filepath.Join(tempDir, "checkout")
	mustRun(t, "", "git", "clone", origin, checkoutPath)
	root := filepath.Join(checkoutPath, ".colin", "workspaces")
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:         root,
			RepoURL:      origin,
			BaseRef:      "main",
			CheckoutPath: checkoutPath,
		},
		Repo: domain.RepoConfig{
			RemoteName:     "origin",
			BranchTemplate: "colin/{{.issue.identifier}}",
		},
		Targets: []domain.TargetConfig{{
			Key:          "project-repo",
			Name:         "target-name",
			ProjectSlug:  "project",
			RepoURL:      origin,
			BaseRef:      "main",
			CheckoutPath: checkoutPath,
		}},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ws, err := manager.Ensure(context.Background(), domain.Issue{
		ID:          "issue-uuid",
		Identifier:  "ABC-123",
		ProjectSlug: "project",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	wantPath := filepath.Join(root, "target-name", "ABC-123")
	if ws.Path != wantPath {
		t.Fatalf("ws.Path = %q, want %q", ws.Path, wantPath)
	}
	wsPath := realPath(t, ws.Path)
	output := mustRunOutput(t, checkoutPath, "git", "worktree", "list", "--porcelain")
	if got := strings.Count(output, "worktree "+wsPath); got != 1 {
		t.Fatalf("worktree list contains workspace %d times, want 1\n%s", got, output)
	}
}

func TestEnsureRejectsStandaloneCloneAtCheckoutPathWorkspace(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t)
	tempDir := t.TempDir()
	checkoutPath := filepath.Join(tempDir, "checkout")
	mustRun(t, "", "git", "clone", origin, checkoutPath)
	root := filepath.Join(tempDir, "workspaces")
	workspacePath := filepath.Join(root, "target-name", "ABC-123")
	if err := os.MkdirAll(filepath.Dir(workspacePath), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "", "git", "clone", origin, workspacePath)
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{Root: root},
		Repo: domain.RepoConfig{
			RemoteName:     "origin",
			BranchTemplate: "colin/{{.issue.identifier}}",
		},
		Targets: []domain.TargetConfig{{
			Key:          "project-repo",
			Name:         "target-name",
			ProjectSlug:  "project",
			RepoURL:      origin,
			BaseRef:      "main",
			CheckoutPath: checkoutPath,
		}},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	_, err := manager.Ensure(context.Background(), domain.Issue{
		ID:          "issue-uuid",
		Identifier:  "ABC-123",
		ProjectSlug: "project",
	})
	if !errors.Is(err, ErrWorkspacePathExists) {
		t.Fatalf("Ensure() error = %v, want %v", err, ErrWorkspacePathExists)
	}
}

func TestEnsureRequiresIssueIdentifierForCheckoutPathWorkspace(t *testing.T) {
	t.Parallel()

	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{Root: t.TempDir()},
		Targets: []domain.TargetConfig{{
			Key:          "project-repo",
			ProjectSlug:  "project",
			RepoURL:      "git@example.com:acme/repo.git",
			BaseRef:      "main",
			CheckoutPath: "/tmp/source",
		}},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	_, err := manager.Ensure(context.Background(), domain.Issue{
		ProjectSlug: "project",
	})
	if !errors.Is(err, ErrMissingIssueID) {
		t.Fatalf("Ensure() error = %v, want %v", err, ErrMissingIssueID)
	}
}

func TestRemoveDeletesManagedWorktreeAndLocalBranch(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t)
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "workspaces")
	repoCacheRoot := filepath.Join(tempDir, "_repos")
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:          root,
			RepoCacheRoot: repoCacheRoot,
			RepoURL:       origin,
			BaseRef:       "main",
		},
		Repo: domain.RepoConfig{
			RemoteName:     "origin",
			BranchTemplate: "colin/{{.issue.identifier}}",
		},
	}
	cfg.Targets = []domain.TargetConfig{{
		Key:         "project-repo",
		ProjectSlug: "project",
		RepoURL:     origin,
		BaseRef:     "main",
	}}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ws, err := manager.Ensure(context.Background(), domain.Issue{Identifier: "ABC-123", ProjectSlug: "project"})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	cachePath := filepath.Join(repoCacheRoot, "project-repo")
	if err := manager.Remove(context.Background(), ws.Path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace path should be removed, err = %v", err)
	}
	output := mustRunOutput(t, cachePath, "git", "worktree", "list", "--porcelain")
	if strings.Contains(output, ws.Path) {
		t.Fatalf("worktree list = %q, want removed workspace absent", output)
	}
	branches := mustRunOutput(t, cachePath, "git", "branch", "--list", "colin/ABC-123")
	if strings.TrimSpace(branches) != "" {
		t.Fatalf("local branch list = %q, want issue branch deleted", branches)
	}
}

func TestEnsureReusesStandaloneCloneWorkspace(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t)
	root := filepath.Join(t.TempDir(), "workspaces")
	workspacePath := filepath.Join(root, "ABC-123")
	mustRun(t, "", "git", "clone", origin, workspacePath)
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:    root,
			RepoURL: origin,
			BaseRef: "main",
		},
		Repo: domain.RepoConfig{
			RemoteName:     "origin",
			BranchTemplate: "colin/{{.issue.identifier}}",
		},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ws, err := manager.Ensure(context.Background(), domain.Issue{Identifier: "ABC-123"})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(ws.Path, ".git"))
	if err != nil {
		t.Fatalf(".git missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf(".git is not a directory; wanted standalone clone compatibility")
	}
	output := mustRunOutput(t, ws.Path, "git", "branch", "--show-current")
	if got := strings.TrimSpace(output); got != "colin/ABC-123" {
		t.Fatalf("current branch = %q, want colin/ABC-123", got)
	}
}

func TestEnsureReusesDirtyGitWorkspaceWithoutResettingToBase(t *testing.T) {
	t.Parallel()

	origin := filepath.Join(t.TempDir(), "origin")
	mustRun(t, "", "git", "init", "-b", "main", origin)
	mustRun(t, origin, "git", "config", "user.email", "test@example.com")
	mustRun(t, origin, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, origin, "git", "add", "README.md")
	mustRun(t, origin, "git", "commit", "-m", "init")

	root := filepath.Join(t.TempDir(), "workspaces")
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:    root,
			RepoURL: origin,
			BaseRef: "main",
		},
		Repo: domain.RepoConfig{
			BranchTemplate: "colin/{{.issue.title}}",
		},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	branch := "feature/ABC-123"
	issue := domain.Issue{
		Identifier: "ABC-123",
		BranchName: &branch,
	}
	ws, err := manager.Ensure(context.Background(), issue)
	if err != nil {
		t.Fatalf("Ensure() initial error = %v", err)
	}

	modified := "hello from dirty workspace\n"
	if err := os.WriteFile(filepath.Join(ws.Path, "README.md"), []byte(modified), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := manager.Ensure(context.Background(), issue); err != nil {
		t.Fatalf("Ensure() with dirty workspace error = %v", err)
	}

	output, err := exec.Command("git", "-C", ws.Path, "branch", "--show-current").CombinedOutput()
	if err != nil {
		t.Fatalf("branch --show-current: %v (%s)", err, string(output))
	}
	if got := strings.TrimSpace(string(output)); got != "feature/ABC-123" {
		t.Fatalf("current branch = %q", got)
	}

	data, err := os.ReadFile(filepath.Join(ws.Path, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(data); got != modified {
		t.Fatalf("README.md = %q, want %q", got, modified)
	}
}

func TestEnsureUsesBranchTemplateWhenTrackerDoesNotProvideBranch(t *testing.T) {
	t.Parallel()

	origin := filepath.Join(t.TempDir(), "origin")
	mustRun(t, "", "git", "init", "-b", "main", origin)
	mustRun(t, origin, "git", "config", "user.email", "test@example.com")
	mustRun(t, origin, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, origin, "git", "add", "README.md")
	mustRun(t, origin, "git", "commit", "-m", "init")

	root := filepath.Join(t.TempDir(), "workspaces")
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:    root,
			RepoURL: origin,
			BaseRef: "main",
		},
		Repo: domain.RepoConfig{
			BranchTemplate: "colin/{{.issue.title}}",
		},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ws, err := manager.Ensure(context.Background(), domain.Issue{
		Identifier: "ABC-123",
		Title:      "Support setting a branch template",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	output, err := exec.Command("git", "-C", ws.Path, "branch", "--show-current").CombinedOutput()
	if err != nil {
		t.Fatalf("branch --show-current: %v (%s)", err, string(output))
	}
	if got := strings.TrimSpace(string(output)); got != "colin/Support_setting_a_branch_template" {
		t.Fatalf("current branch = %q", got)
	}
}

func TestEnsureNamespacesWorkspaceByTargetWhenMultiTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{Root: root},
		Targets: []domain.TargetConfig{
			{Key: "project-1-api", ProjectSlug: "project-1"},
			{Key: "project-2-web", ProjectSlug: "project-2"},
		},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ws, err := manager.Ensure(context.Background(), domain.Issue{
		Identifier:  "ABC-123",
		ProjectSlug: "project-2",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	want := filepath.Join(root, "project-2-web", "ABC-123")
	if ws.Path != want {
		t.Fatalf("ws.Path = %q, want %q", ws.Path, want)
	}
}

func mustRun(t *testing.T, cwd string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v (%s)", name, args, err, string(output))
	}
}

func mustRunOutput(t *testing.T, cwd string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v (%s)", name, args, err, string(output))
	}
	return string(output)
}

func newTestOrigin(t *testing.T) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin")
	mustRun(t, "", "git", "init", "-b", "main", origin)
	mustRun(t, origin, "git", "config", "user.email", "test@example.com")
	mustRun(t, origin, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, origin, "git", "add", "README.md")
	mustRun(t, origin, "git", "commit", "-m", "init")
	return origin
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", path, err)
	}
	return resolved
}
