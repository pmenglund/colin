package worker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	defaultBranchMetadataGitBinary = "git"
	branchSessionConfigKeySuffix   = "colinSessionId"
)

// BranchMetadataStore persists and reads task metadata scoped to git branches.
type BranchMetadataStore interface {
	GetBranchSessionID(ctx context.Context, branchName string) (string, error)
	SetBranchSessionID(ctx context.Context, branchName string, sessionID string) error
}

// GitBranchMetadataStore stores branch metadata in local git config.
type GitBranchMetadataStore struct {
	repoRoot  string
	gitBinary string
}

// GitBranchMetadataStoreOptions configures a git-backed BranchMetadataStore.
type GitBranchMetadataStoreOptions struct {
	RepoRoot  string
	GitBinary string
}

// NewGitBranchMetadataStore builds a git-backed branch metadata store.
func NewGitBranchMetadataStore(opts GitBranchMetadataStoreOptions) *GitBranchMetadataStore {
	gitBinary := strings.TrimSpace(opts.GitBinary)
	if gitBinary == "" {
		gitBinary = defaultBranchMetadataGitBinary
	}
	return &GitBranchMetadataStore{
		repoRoot:  filepath.Clean(strings.TrimSpace(opts.RepoRoot)),
		gitBinary: gitBinary,
	}
}

// GetBranchSessionID reads the persisted Codex session ID for branchName.
func (s *GitBranchMetadataStore) GetBranchSessionID(ctx context.Context, branchName string) (string, error) {
	if s == nil {
		return "", errors.New("git branch metadata store is nil")
	}
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return "", errors.New("branch name is required")
	}
	if s.repoRoot == "." || s.repoRoot == "" {
		return "", errors.New("repo root is required")
	}

	key := branchSessionConfigKey(branchName)
	value, err := gitOutputAllowMissing(ctx, s.gitBinary, "-C", s.repoRoot, "config", "--local", "--get", key)
	if err != nil {
		return "", fmt.Errorf("read git metadata %q for branch %q in %q: %w", key, branchName, s.repoRoot, err)
	}
	return strings.TrimSpace(value), nil
}

// SetBranchSessionID persists sessionID as branch metadata for branchName.
func (s *GitBranchMetadataStore) SetBranchSessionID(ctx context.Context, branchName string, sessionID string) error {
	if s == nil {
		return errors.New("git branch metadata store is nil")
	}
	branchName = strings.TrimSpace(branchName)
	sessionID = strings.TrimSpace(sessionID)
	if branchName == "" {
		return errors.New("branch name is required")
	}
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if s.repoRoot == "." || s.repoRoot == "" {
		return errors.New("repo root is required")
	}

	key := branchSessionConfigKey(branchName)
	if err := gitRun(ctx, s.gitBinary, "-C", s.repoRoot, "config", "--local", key, sessionID); err != nil {
		return fmt.Errorf("write git metadata %q for branch %q in %q: %w", key, branchName, s.repoRoot, err)
	}
	return nil
}

func branchSessionConfigKey(branchName string) string {
	return "branch." + strings.TrimSpace(branchName) + "." + branchSessionConfigKeySuffix
}
