package worker

import (
	"context"
	"strings"
	"testing"
)

func TestGitBranchMetadataStoreSetAndGetBranchSessionID(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	store := NewGitBranchMetadataStore(GitBranchMetadataStoreOptions{
		RepoRoot: repoRoot,
	})

	const (
		branchName = "colin/COLIN-5"
		sessionID  = "session-123"
	)
	if err := store.SetBranchSessionID(context.Background(), branchName, sessionID); err != nil {
		t.Fatalf("SetBranchSessionID() error = %v", err)
	}

	got, err := store.GetBranchSessionID(context.Background(), branchName)
	if err != nil {
		t.Fatalf("GetBranchSessionID() error = %v", err)
	}
	if got != sessionID {
		t.Fatalf("sessionID = %q, want %q", got, sessionID)
	}

	raw := runGit(t, "-C", repoRoot, "config", "--local", "--get", "branch."+branchName+".colinSessionId")
	if raw != sessionID {
		t.Fatalf("git config value = %q, want %q", raw, sessionID)
	}
}

func TestGitBranchMetadataStoreGetBranchSessionIDMissingReturnsEmpty(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	store := NewGitBranchMetadataStore(GitBranchMetadataStoreOptions{
		RepoRoot: repoRoot,
	})

	got, err := store.GetBranchSessionID(context.Background(), "colin/COLIN-5")
	if err != nil {
		t.Fatalf("GetBranchSessionID() error = %v", err)
	}
	if got != "" {
		t.Fatalf("sessionID = %q, want empty", got)
	}
}

func TestGitBranchMetadataStoreSetBranchSessionIDRequiresSessionID(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	store := NewGitBranchMetadataStore(GitBranchMetadataStoreOptions{
		RepoRoot: repoRoot,
	})

	err := store.SetBranchSessionID(context.Background(), "colin/COLIN-5", "")
	if err == nil {
		t.Fatal("SetBranchSessionID() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "session id is required") {
		t.Fatalf("error = %q, want session id validation", err.Error())
	}
}
