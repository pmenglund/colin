package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	formatconfig "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/transport"
	githttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	xworktree "github.com/go-git/go-git/v6/x/plumbing/worktree"
)

const (
	branchConfigSection = "branch"
	gitDirName          = ".git"
	gitDirFileName      = "gitdir"
)

func openRepository(path string) (*git.Repository, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || trimmed == "." {
		return nil, errors.New("repository path is required")
	}

	repo, err := git.PlainOpenWithOptions(filepath.Clean(trimmed), &git.PlainOpenOptions{
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return nil, err
	}
	return repo, nil
}

func resolveCommit(repo *git.Repository, revision string) (plumbing.Hash, error) {
	hash, err := repo.ResolveRevision(plumbing.Revision(strings.TrimSpace(revision)))
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if hash == nil || hash.IsZero() {
		return plumbing.ZeroHash, fmt.Errorf("resolve revision %q", strings.TrimSpace(revision))
	}
	return *hash, nil
}

func branchRef(branchName string) plumbing.ReferenceName {
	return plumbing.NewBranchReferenceName(strings.TrimSpace(branchName))
}

func gitBranchExists(repo *git.Repository, branchName string) (bool, error) {
	_, err := repo.Reference(branchRef(branchName), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func currentBranchName(repo *git.Repository) (string, error) {
	head, err := repo.Head()
	if err != nil {
		return "", err
	}

	if head.Name() == plumbing.HEAD {
		return "HEAD", nil
	}
	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}
	return head.Name().String(), nil
}

func checkoutBranch(repo *git.Repository, branchName string) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}
	return worktree.Checkout(&git.CheckoutOptions{Branch: branchRef(branchName)})
}

func checkoutBranchFromRevision(repo *git.Repository, branchName string, revision string) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}

	baseHash, err := resolveCommit(repo, revision)
	if err != nil {
		return err
	}

	return worktree.Checkout(&git.CheckoutOptions{
		Branch: branchRef(branchName),
		Create: true,
		Hash:   baseHash,
	})
}

func checkoutDetachedHead(repo *git.Repository) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}

	head, err := repo.Head()
	if err != nil {
		return err
	}

	return worktree.Checkout(&git.CheckoutOptions{Hash: head.Hash()})
}

func isAncestorCommit(repo *git.Repository, ancestor plumbing.Hash, descendant plumbing.Hash) (bool, error) {
	if ancestor == descendant {
		return true, nil
	}

	queue := []plumbing.Hash{descendant}
	visited := make(map[plumbing.Hash]struct{}, len(queue))

	for len(queue) > 0 {
		hash := queue[0]
		queue = queue[1:]

		if hash == ancestor {
			return true, nil
		}
		if _, ok := visited[hash]; ok {
			continue
		}
		visited[hash] = struct{}{}

		commit, err := repo.CommitObject(hash)
		if err != nil {
			return false, err
		}
		queue = append(queue, commit.ParentHashes...)
	}

	return false, nil
}

func isAncestorBranch(repo *git.Repository, ancestorBranch string, descendantBranch string) (bool, error) {
	ancestorRef, err := repo.Reference(branchRef(ancestorBranch), true)
	if err != nil {
		return false, err
	}
	descendantRef, err := repo.Reference(branchRef(descendantBranch), true)
	if err != nil {
		return false, err
	}

	return isAncestorCommit(repo, ancestorRef.Hash(), descendantRef.Hash())
}

func fastForwardBranch(repo *git.Repository, baseBranch string, sourceBranch string) error {
	baseRefName := branchRef(baseBranch)
	baseRef, err := repo.Reference(baseRefName, true)
	if err != nil {
		return err
	}

	sourceRef, err := repo.Reference(branchRef(sourceBranch), true)
	if err != nil {
		return err
	}

	alreadyMerged, err := isAncestorCommit(repo, sourceRef.Hash(), baseRef.Hash())
	if err != nil {
		return err
	}
	if alreadyMerged {
		return nil
	}

	fastForwardPossible, err := isAncestorCommit(repo, baseRef.Hash(), sourceRef.Hash())
	if err != nil {
		return err
	}
	if !fastForwardPossible {
		return git.ErrFastForwardMergeNotPossible
	}

	if err := repo.Storer.SetReference(plumbing.NewHashReference(baseRefName, sourceRef.Hash())); err != nil {
		return err
	}

	head, err := repo.Head()
	if err == nil && head.Name() == baseRefName {
		worktree, wtErr := repo.Worktree()
		if wtErr != nil {
			return wtErr
		}
		if resetErr := worktree.Reset(&git.ResetOptions{
			Mode:   git.HardReset,
			Commit: sourceRef.Hash(),
		}); resetErr != nil {
			return resetErr
		}
	}

	return nil
}

func pushBranch(
	ctx context.Context,
	repo *git.Repository,
	remoteName string,
	branchName string,
	auth transport.AuthMethod,
) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	trimmedRemote := strings.TrimSpace(remoteName)
	trimmedBranch := strings.TrimSpace(branchName)
	refspec := gitconfig.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", trimmedBranch, trimmedBranch))
	err := repo.PushContext(ctx, &git.PushOptions{
		RemoteName: trimmedRemote,
		RefSpecs:   []gitconfig.RefSpec{refspec},
		Auth:       auth,
	})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

func pushAuthMethodForRemote(
	ctx context.Context,
	remoteURL string,
	tokenProvider GitHubTokenProvider,
) (transport.AuthMethod, error) {
	if tokenProvider == nil {
		return nil, nil
	}

	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return nil, nil
	}
	if isLocalPathRemote(remoteURL) {
		return nil, nil
	}
	if isSSHRemote(remoteURL) {
		return nil, fmt.Errorf("github app auth requires an HTTPS remote URL, got %q", remoteURL)
	}

	u, err := url.Parse(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("parse remote URL %q: %w", remoteURL, err)
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, nil
	}

	token, err := tokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve GitHub installation token: %w", err)
	}
	return &githttp.BasicAuth{
		Username: "x-access-token",
		Password: token,
	}, nil
}

func isLocalPathRemote(remoteURL string) bool {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return false
	}
	if strings.HasPrefix(remoteURL, "/") || strings.HasPrefix(remoteURL, "./") || strings.HasPrefix(remoteURL, "../") {
		return true
	}
	if strings.HasPrefix(remoteURL, "file://") {
		return true
	}
	if strings.Contains(remoteURL, "://") {
		return false
	}
	return !strings.Contains(remoteURL, "@") && !strings.Contains(remoteURL, ":")
}

func isSSHRemote(remoteURL string) bool {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return false
	}
	if strings.HasPrefix(remoteURL, "ssh://") {
		return true
	}
	if strings.HasPrefix(remoteURL, "git@") && strings.Contains(remoteURL, ":") {
		return true
	}
	return false
}

func remoteURL(repo *git.Repository, remoteName string) (string, error) {
	remote, err := repo.Remote(strings.TrimSpace(remoteName))
	if err != nil {
		return "", err
	}

	cfg := remote.Config()
	if cfg == nil || len(cfg.URLs) == 0 {
		return "", errors.New("remote has no configured URLs")
	}

	return strings.TrimSpace(cfg.URLs[0]), nil
}

func readBranchConfigOption(repo *git.Repository, branchName string, optionName string) (string, error) {
	cfg, err := loadRepositoryConfig(repo)
	if err != nil {
		return "", err
	}

	subsection := cfg.Section(branchConfigSection).Subsection(strings.TrimSpace(branchName))
	return strings.TrimSpace(subsection.Option(strings.TrimSpace(optionName))), nil
}

func writeBranchConfigOption(repo *git.Repository, branchName string, optionName string, optionValue string) error {
	cfg, err := loadRepositoryConfig(repo)
	if err != nil {
		return err
	}

	cfg.Section(branchConfigSection).Subsection(strings.TrimSpace(branchName)).SetOption(
		strings.TrimSpace(optionName),
		strings.TrimSpace(optionValue),
	)
	return saveRepositoryConfig(repo, cfg)
}

func addLinkedWorktree(repoRoot string, worktreePath string, baseRevision string) error {
	repo, err := openRepository(repoRoot)
	if err != nil {
		return err
	}

	manager, err := xworktree.New(repo.Storer)
	if err != nil {
		return err
	}

	baseHash, err := resolveCommit(repo, baseRevision)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		return err
	}

	worktreeName := linkedWorktreeName(worktreePath)
	wtFS := osfs.New(filepath.Clean(strings.TrimSpace(worktreePath)), osfs.WithBoundOS())
	return manager.Add(
		wtFS,
		worktreeName,
		xworktree.WithCommit(baseHash),
		xworktree.WithDetachedHead(),
	)
}

func removeLinkedWorktree(repoRoot string, worktreePath string) error {
	repo, err := openRepository(repoRoot)
	if err != nil {
		return err
	}

	manager, err := xworktree.New(repo.Storer)
	if err != nil {
		return err
	}

	if err := manager.Remove(linkedWorktreeName(worktreePath)); err != nil {
		return err
	}

	return os.RemoveAll(strings.TrimSpace(worktreePath))
}

func findLinkedWorktreePathByBranch(repoRoot string, branchName string) (string, error) {
	repo, err := openRepository(repoRoot)
	if err != nil {
		return "", err
	}

	manager, err := xworktree.New(repo.Storer)
	if err != nil {
		return "", err
	}

	worktreeNames, err := manager.List()
	if err != nil {
		return "", err
	}

	targetBranch := strings.TrimSpace(branchName)
	for _, name := range worktreeNames {
		worktreePath, pathErr := linkedWorktreePath(repoRoot, name)
		if pathErr != nil {
			return "", pathErr
		}
		if strings.TrimSpace(worktreePath) == "" {
			continue
		}

		worktreeRepo, openErr := openRepository(worktreePath)
		if openErr != nil {
			continue
		}

		head, headErr := worktreeRepo.Head()
		if headErr != nil || !head.Name().IsBranch() {
			continue
		}
		if head.Name().Short() == targetBranch {
			return worktreePath, nil
		}
	}

	return "", nil
}

func linkedWorktreeName(worktreePath string) string {
	trimmed := strings.TrimSpace(worktreePath)
	if trimmed == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(trimmed))
}

func linkedWorktreePath(repoRoot string, worktreeName string) (string, error) {
	trimmedRoot := filepath.Clean(strings.TrimSpace(repoRoot))
	trimmedName := strings.TrimSpace(worktreeName)
	if trimmedRoot == "." || trimmedRoot == "" || trimmedName == "" {
		return "", nil
	}

	gitDirPath := filepath.Join(trimmedRoot, gitDirName, "worktrees", trimmedName, gitDirFileName)
	content, err := os.ReadFile(gitDirPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	worktreeGitPath := strings.TrimSpace(string(content))
	if worktreeGitPath == "" {
		return "", nil
	}
	if !filepath.IsAbs(worktreeGitPath) {
		worktreeGitPath = filepath.Join(filepath.Dir(gitDirPath), worktreeGitPath)
	}

	return filepath.Clean(filepath.Dir(worktreeGitPath)), nil
}

type repositoryFilesystemStorer interface {
	Filesystem() billy.Filesystem
}

func repositoryFilesystem(repo *git.Repository) (billy.Filesystem, error) {
	storer, ok := repo.Storer.(repositoryFilesystemStorer)
	if !ok {
		return nil, errors.New("repository storer does not expose filesystem")
	}
	return storer.Filesystem(), nil
}

func loadRepositoryConfig(repo *git.Repository) (*formatconfig.Config, error) {
	repoFS, err := repositoryFilesystem(repo)
	if err != nil {
		return nil, err
	}

	cfg := formatconfig.New()
	file, err := repoFS.Open("config")
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	decoder := formatconfig.NewDecoder(file)
	if err := decoder.Decode(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func saveRepositoryConfig(repo *git.Repository, cfg *formatconfig.Config) error {
	repoFS, err := repositoryFilesystem(repo)
	if err != nil {
		return err
	}

	file, err := repoFS.OpenFile("config", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	encoder := formatconfig.NewEncoder(file)
	return encoder.Encode(cfg)
}
