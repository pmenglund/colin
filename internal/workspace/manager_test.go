package workspace

import (
	"context"
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
	if got := strings.TrimSpace(string(output)); got != "feature_ABC-123" {
		t.Fatalf("current branch = %q", got)
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
