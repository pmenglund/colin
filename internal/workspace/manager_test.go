package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakePopulator struct {
	callCount int
	lastPath  string
	metadata  map[string]string
	err       error
}

func (f *fakePopulator) Prepare(_ context.Context, _ string, workspacePath string) (map[string]string, error) {
	f.callCount++
	f.lastPath = workspacePath
	if f.err != nil {
		return nil, f.err
	}
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return nil, err
	}
	return f.metadata, nil
}

func TestSanitizeKey(t *testing.T) {
	t.Parallel()

	if got := SanitizeKey("COLIN-1 / weird?"); got != "COLIN-1___weird" {
		t.Fatalf("SanitizeKey() = %q", got)
	}
}

func TestManagerEnsureRunsAfterCreateOnlyOnce(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	hookFile := filepath.Join(root, "after-create.txt")
	manager, err := New(ManagerOptions{
		Root: root,
		Hooks: HookConfig{
			AfterCreate: "echo created >> " + filepath.Base(hookFile),
			Timeout:     time.Second,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ws1, err := manager.Ensure(context.Background(), "COLIN-1")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	ws2, err := manager.Ensure(context.Background(), "COLIN-1")
	if err != nil {
		t.Fatalf("Ensure() second error = %v", err)
	}
	if !ws1.CreatedNow {
		t.Fatal("expected first ensure to create workspace")
	}
	if ws2.CreatedNow {
		t.Fatal("expected second ensure to reuse workspace")
	}
	content, err := os.ReadFile(filepath.Join(ws1.Path, filepath.Base(hookFile)))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Count(string(content), "created") != 1 {
		t.Fatalf("hook output = %q, want one line", string(content))
	}
}

func TestManagerBeforeRunFailureIsFatal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager, err := New(ManagerOptions{
		Root: root,
		Hooks: HookConfig{
			BeforeRun: "exit 9",
			Timeout:   time.Second,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ws, err := manager.Ensure(context.Background(), "COLIN-2")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if err := manager.BeforeRun(context.Background(), ws); err == nil {
		t.Fatal("BeforeRun() error = nil, want failure")
	}
}

func TestManagerAfterRunIgnoresHookFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager, err := New(ManagerOptions{
		Root: root,
		Hooks: HookConfig{
			AfterRun: "exit 7",
			Timeout:  time.Second,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ws, err := manager.Ensure(context.Background(), "COLIN-3")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	manager.AfterRun(context.Background(), ws, nil)
}

func TestManagerCleanupTerminalRemovesOnlyManagedPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manager, err := New(ManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ws, err := manager.Ensure(context.Background(), "COLIN-4")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := manager.CleanupTerminal(context.Background(), []string{"COLIN-4"}); err != nil {
		t.Fatalf("CleanupTerminal() error = %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside path should remain, stat err = %v", err)
	}
}

func TestManagerEnsureUsesPopulatorMetadata(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	populator := &fakePopulator{metadata: map[string]string{"branch": "colin/COLIN-9"}}
	manager, err := New(ManagerOptions{Root: root, Populator: populator})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ws, err := manager.Ensure(context.Background(), "COLIN-9")
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if populator.callCount != 1 {
		t.Fatalf("populator call count = %d, want 1", populator.callCount)
	}
	if ws.Metadata["branch"] != "colin/COLIN-9" {
		t.Fatalf("metadata = %#v", ws.Metadata)
	}
}
