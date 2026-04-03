package repoops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	_ "github.com/pmenglund/colin/internal/repohost/github"
	"github.com/pmenglund/colin/internal/workflow"
)

var (
	ErrNotGitRepository           = errors.New("not_git_repository")
	ErrNoPullRequest              = errors.New("no_pull_request")
	ErrNoReviewableChanges        = errors.New("no_reviewable_changes")
	ErrDuplicatePullRequests      = errors.New("duplicate_pull_requests")
	ErrTrackedPullRequestMismatch = errors.New("tracked_pull_request_mismatch")
)

var codexReviewLogins = []string{
	"chatgpt-codex-connector",
	"chatgpt-codex-connector[bot]",
}

// Result captures the outcome of a publish or merge operation.
type Result struct {
	Branch    string
	BaseRef   string
	PRNumber  int
	PRURL     string
	PRState   string
	PRHeadRef string
	PRBaseRef string
	Commit    string
	Action    string
}

// ReviewContext captures the PR, unresolved review threads, and Codex review signals for an issue branch.
type ReviewContext struct {
	PullRequest            domain.PullRequestRef
	Threads                []domain.ReviewThread
	CodexReviewThreads     []domain.ReviewThread
	CodexReviewObserved    bool
	CodexReviewRequestedAt *time.Time
	CodexReviewApprovedAt  *time.Time
}

// Manager performs git and repository-host operations for a workspace.
type Manager struct {
	cfg      domain.ServiceConfig
	logger   *slog.Logger
	host     repohost.Client
	hostOnce sync.Once
	hostErr  error
}

// NewManager constructs a repository automation manager.
func NewManager(cfg domain.ServiceConfig, logger *slog.Logger) *Manager {
	return &Manager{cfg: cfg, logger: logger}
}

// NewManagerWithRepoHostClient constructs a repository automation manager with an injected repository-host client.
func NewManagerWithRepoHostClient(cfg domain.ServiceConfig, logger *slog.Logger, client repohost.Client) *Manager {
	return &Manager{cfg: cfg, logger: logger, host: client}
}

// NewManagerWithGitHubClient constructs a repository automation manager with an injected GitHub client.
func NewManagerWithGitHubClient(cfg domain.ServiceConfig, logger *slog.Logger, client GitHubClient) *Manager {
	return NewManagerWithRepoHostClient(cfg, logger, client)
}

// ValidateRepoAccess verifies that a configured repository-host token can authenticate before startup continues.
func (m *Manager) ValidateRepoAccess(ctx context.Context) error {
	if m == nil || strings.TrimSpace(m.cfg.Repo.APIToken) == "" {
		return nil
	}
	client, err := m.repoHostClient()
	if err != nil {
		return err
	}
	return client.ValidateAuth(ctx)
}

// ValidateGitHubAccess is kept as a compatibility wrapper while the codebase migrates to backend-neutral naming.
func (m *Manager) ValidateGitHubAccess(ctx context.Context) error {
	return m.ValidateRepoAccess(ctx)
}

// Publish commits workspace changes, pushes the issue branch, and creates or reuses a PR.
func (m *Manager) Publish(ctx context.Context, issue domain.Issue, workspacePath string) (Result, error) {
	branch, err := m.currentBranch(ctx, workspacePath)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Branch:  branch,
		BaseRef: m.cfg.Workspace.BaseRef,
	}

	dirty, err := m.isDirty(ctx, workspacePath)
	if err != nil {
		return Result{}, err
	}
	if dirty {
		if err := m.ensureIdentity(ctx, workspacePath); err != nil {
			return Result{}, err
		}
		if _, err := m.run(ctx, workspacePath, 30*time.Second, "git", "add", "-A"); err != nil {
			return Result{}, err
		}
		message := commitMessage(issue)
		if _, err := m.run(ctx, workspacePath, 30*time.Second, "git", "commit", "-m", message); err != nil {
			return Result{}, err
		}
		result.Action = "committed"
	}

	commit, err := m.revParse(ctx, workspacePath, "HEAD")
	if err == nil {
		result.Commit = commit
	}

	pr, created, err := m.resolvePullRequest(ctx, issue, workspacePath, branch, false)
	if err != nil {
		return Result{}, err
	}
	if pr == nil {
		reviewable, err := m.ReviewableArtifact(ctx, workspacePath)
		if err != nil {
			return Result{}, err
		}
		if !reviewable {
			return Result{}, ErrNoReviewableChanges
		}
	}

	if _, err := m.pushBranch(ctx, workspacePath, branch); err != nil {
		return Result{}, err
	}
	if commit, err := m.revParse(ctx, workspacePath, "HEAD"); err == nil {
		result.Commit = commit
	}
	if result.Action == "" {
		result.Action = "pushed"
	} else {
		result.Action = "committed_and_pushed"
	}

	if pr == nil {
		pr, created, err = m.resolvePullRequest(ctx, issue, workspacePath, branch, true)
		if err != nil {
			return Result{}, err
		}
		if pr == nil {
			return Result{}, ErrNoPullRequest
		}
	}
	if created {
		if result.Action == "pushed" {
			result.Action = "pushed_and_opened_pr"
		} else {
			result.Action += "_and_opened_pr"
		}
	}

	result.PRNumber = pr.Number
	result.PRURL = pr.URL
	result.PRState = pr.State
	result.PRHeadRef = pr.HeadRefName
	result.PRBaseRef = pr.BaseRefName
	return result, nil
}

// Merge ensures the current branch is published and merges its PR.
func (m *Manager) Merge(ctx context.Context, issue domain.Issue, workspacePath string) (Result, error) {
	result, err := m.Publish(ctx, issue, workspacePath)
	if err != nil {
		return Result{}, err
	}
	return m.MergePullRequest(ctx, workspacePath, result)
}

// MergePullRequest merges the pull request described by a prior publish result.
func (m *Manager) MergePullRequest(ctx context.Context, workspacePath string, result Result) (Result, error) {
	if strings.EqualFold(result.PRState, "MERGED") {
		result.Action = "already_merged"
		return result, nil
	}
	if result.PRNumber == 0 {
		return Result{}, ErrNoPullRequest
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath)
	if err != nil {
		return result, err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return result, err
	}
	if err := client.MergePullRequest(ctx, owner, name, result.PRNumber, m.cfg.Repo.MergeMethod); err != nil {
		return result, err
	}
	result.Action = "merged"
	result.PRState = "MERGED"
	return result, nil
}

// ReviewContext returns the current PR and unresolved review threads for the issue branch.
func (m *Manager) ReviewContext(ctx context.Context, issue domain.Issue, workspacePath string) (ReviewContext, error) {
	pr, _, err := m.resolvePullRequest(ctx, issue, workspacePath, "", false)
	if err != nil {
		return ReviewContext{}, err
	}
	if pr == nil {
		return ReviewContext{}, nil
	}

	owner, name, err := m.remoteRepository(ctx, workspacePath)
	if err != nil {
		return ReviewContext{}, err
	}

	threads, codexThreads, codexObserved, err := m.fetchReviewThreads(ctx, workspacePath, owner, name, pr.Number)
	if err != nil {
		return ReviewContext{}, err
	}
	requestedAt, approvedAt, err := m.fetchCodexReviewReactions(ctx, workspacePath, owner, name, pr.Number)
	if err != nil {
		return ReviewContext{}, err
	}
	return ReviewContext{
		PullRequest: domain.PullRequestRef{
			Number:  pr.Number,
			URL:     pr.URL,
			State:   pr.State,
			HeadRef: pr.HeadRefName,
			BaseRef: pr.BaseRefName,
		},
		Threads:                threads,
		CodexReviewThreads:     codexThreads,
		CodexReviewObserved:    codexObserved,
		CodexReviewRequestedAt: requestedAt,
		CodexReviewApprovedAt:  approvedAt,
	}, nil
}

// CurrentBranch returns the current checked-out git branch for the workspace.
func (m *Manager) CurrentBranch(ctx context.Context, workspacePath string) (string, error) {
	return m.currentBranch(ctx, workspacePath)
}

// ReviewableArtifact reports whether the workspace contains reviewable repository changes.
func (m *Manager) ReviewableArtifact(ctx context.Context, workspacePath string) (bool, error) {
	dirty, err := m.isDirty(ctx, workspacePath)
	if err != nil {
		return false, err
	}
	if dirty {
		return true, nil
	}
	return m.branchAheadOfBase(ctx, workspacePath)
}

// ReplyAndResolveReviewThread posts a reply and resolves a review thread.
func (m *Manager) ReplyAndResolveReviewThread(ctx context.Context, workspacePath string, thread domain.ReviewThread, body string) error {
	if strings.TrimSpace(thread.ID) == "" {
		return errors.New("missing review thread id")
	}
	if !thread.CanReply {
		return errors.New("review thread not replyable")
	}
	if !thread.CanResolve {
		return errors.New("review thread not resolvable")
	}
	replyBody := strings.TrimSpace(body)
	if replyBody == "" {
		return errors.New("missing review reply body")
	}
	client, err := m.repoHostClient()
	if err != nil {
		return err
	}
	if err := client.ReplyToReviewThread(ctx, thread.ID, replyBody); err != nil {
		return err
	}
	return client.ResolveReviewThread(ctx, thread.ID)
}

func (m *Manager) currentBranch(ctx context.Context, workspacePath string) (string, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(out)
	if branch == "" || branch == "HEAD" {
		return "", ErrNotGitRepository
	}
	return branch, nil
}

func (m *Manager) reviewLookupBranches(ctx context.Context, issue domain.Issue, workspacePath string) []string {
	branches := make([]string, 0, 3)
	addBranch := func(branch string) {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			return
		}
		for _, existing := range branches {
			if existing == branch {
				return
			}
		}
		branches = append(branches, branch)
	}

	if current, err := m.currentBranch(ctx, workspacePath); err == nil {
		addBranch(current)
	}
	if issue.ColinMetadata != nil {
		addBranch(issue.ColinMetadata.ActualBranchName)
	}
	if issue.BranchName != nil {
		addBranch(*issue.BranchName)
	}
	return branches
}

func (m *Manager) revParse(ctx context.Context, workspacePath string, ref string) (string, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (m *Manager) isDirty(ctx context.Context, workspacePath string) (bool, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (m *Manager) branchAheadOfBase(ctx context.Context, workspacePath string) (bool, error) {
	baseRef, err := m.baseComparisonRef(ctx, workspacePath)
	if err != nil {
		return false, err
	}
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "rev-list", "--left-right", "--count", baseRef+"...HEAD")
	if err != nil {
		return false, err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return false, fmt.Errorf("unexpected git rev-list output: %q", strings.TrimSpace(out))
	}
	ahead, err := strconv.Atoi(fields[1])
	if err != nil {
		return false, fmt.Errorf("parse git rev-list ahead count: %w", err)
	}
	return ahead > 0, nil
}

func (m *Manager) baseComparisonRef(ctx context.Context, workspacePath string) (string, error) {
	candidates := []string{
		strings.TrimSpace(m.cfg.Workspace.BaseRef),
		strings.TrimSpace(m.cfg.Repo.RemoteName) + "/" + strings.TrimSpace(m.cfg.Workspace.BaseRef),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, err := m.revParse(ctx, workspacePath, candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("resolve base ref %q", strings.TrimSpace(m.cfg.Workspace.BaseRef))
}

func (m *Manager) ensureIdentity(ctx context.Context, workspacePath string) error {
	name, err := m.run(ctx, workspacePath, 15*time.Second, "git", "config", "user.name")
	if err == nil && strings.TrimSpace(name) != "" {
		email, err := m.run(ctx, workspacePath, 15*time.Second, "git", "config", "user.email")
		if err == nil && strings.TrimSpace(email) != "" {
			return nil
		}
	}
	if _, err := m.run(ctx, workspacePath, 15*time.Second, "git", "config", "user.name", "Colin"); err != nil {
		return err
	}
	if _, err := m.run(ctx, workspacePath, 15*time.Second, "git", "config", "user.email", "colin@local"); err != nil {
		return err
	}
	return nil
}

func (m *Manager) findPullRequest(ctx context.Context, workspacePath, branch string) (*GitHubPullRequest, error) {
	owner, name, err := m.remoteRepository(ctx, workspacePath)
	if err != nil {
		return nil, err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return nil, err
	}
	return client.PullRequestByHead(ctx, owner, name, branch, m.cfg.Workspace.BaseRef)
}

func (m *Manager) findPullRequestByNumber(ctx context.Context, workspacePath string, number int) (*GitHubPullRequest, error) {
	if number <= 0 {
		return nil, nil
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath)
	if err != nil {
		return nil, err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return nil, err
	}
	return client.PullRequestByNumber(ctx, owner, name, number)
}

func (m *Manager) resolvePullRequest(ctx context.Context, issue domain.Issue, workspacePath, currentBranch string, allowCreate bool) (*GitHubPullRequest, bool, error) {
	tracked, hasTracked := trackedPullRequest(issue)
	if hasTracked {
		pr, err := m.resolveTrackedPullRequest(ctx, workspacePath, tracked, currentBranch)
		return pr, false, err
	}

	owner, name, err := m.remoteRepository(ctx, workspacePath)
	if err != nil {
		return nil, false, err
	}
	adapter, err := repohost.Lookup(m.cfg.Repo.Backend)
	if err != nil {
		return nil, false, err
	}

	attached := attachedPullRequestsForRepository(issue.AttachedPullRequests, owner, name, adapter)
	switch len(attached) {
	case 0:
	case 1:
		pr, err := m.findPullRequestByNumber(ctx, workspacePath, attached[0].Number)
		if err != nil {
			return nil, false, fmt.Errorf("resolve attached pull request #%d: %w", attached[0].Number, err)
		}
		if pr == nil {
			return nil, false, fmt.Errorf("attached pull request #%d not found", attached[0].Number)
		}
		return pr, false, nil
	default:
		return nil, false, duplicatePullRequestsError(attached)
	}

	if currentBranch != "" {
		pr, err := m.findPullRequest(ctx, workspacePath, currentBranch)
		if err != nil {
			return nil, false, err
		}
		if pr != nil {
			return pr, false, nil
		}
	} else {
		for _, branch := range m.reviewLookupBranches(ctx, issue, workspacePath) {
			pr, err := m.findPullRequest(ctx, workspacePath, branch)
			if err != nil {
				return nil, false, err
			}
			if pr != nil {
				return pr, false, nil
			}
		}
	}

	if !allowCreate || strings.TrimSpace(currentBranch) == "" {
		return nil, false, nil
	}

	url, err := m.createPullRequest(ctx, workspacePath, issue, currentBranch)
	if err != nil {
		return nil, false, err
	}
	pr, err := m.findPullRequest(ctx, workspacePath, currentBranch)
	if err != nil {
		return nil, false, err
	}
	if pr == nil {
		return nil, false, ErrNoPullRequest
	}
	pr.URL = url
	return pr, true, nil
}

func (m *Manager) resolveTrackedPullRequest(ctx context.Context, workspacePath string, tracked domain.PullRequestRef, currentBranch string) (*GitHubPullRequest, error) {
	pr, err := m.findPullRequestByNumber(ctx, workspacePath, tracked.Number)
	if err != nil {
		return nil, fmt.Errorf("view tracked pull request #%d: %w", tracked.Number, err)
	}
	if pr == nil {
		return nil, fmt.Errorf("tracked pull request #%d not found", tracked.Number)
	}
	if value := strings.TrimSpace(tracked.URL); value != "" && value != strings.TrimSpace(pr.URL) {
		return nil, fmt.Errorf("%w: tracked pull request url %q does not match repository url %q", ErrTrackedPullRequestMismatch, value, pr.URL)
	}
	if value := strings.TrimSpace(tracked.HeadRef); value != "" && value != strings.TrimSpace(pr.HeadRefName) {
		return nil, fmt.Errorf("%w: tracked pull request head %q does not match repository head %q", ErrTrackedPullRequestMismatch, value, pr.HeadRefName)
	}
	if value := strings.TrimSpace(tracked.BaseRef); value != "" && value != strings.TrimSpace(pr.BaseRefName) {
		return nil, fmt.Errorf("%w: tracked pull request base %q does not match repository base %q", ErrTrackedPullRequestMismatch, value, pr.BaseRefName)
	}
	if value := strings.TrimSpace(currentBranch); value != "" && strings.TrimSpace(pr.HeadRefName) != "" && value != strings.TrimSpace(pr.HeadRefName) {
		return nil, fmt.Errorf("%w: current branch %q does not match tracked pull request head %q", ErrTrackedPullRequestMismatch, value, pr.HeadRefName)
	}
	return pr, nil
}

func trackedPullRequest(issue domain.Issue) (domain.PullRequestRef, bool) {
	if issue.ColinMetadata == nil || issue.ColinMetadata.PullRequestNumber <= 0 {
		return domain.PullRequestRef{}, false
	}
	return domain.PullRequestRef{
		Number:  issue.ColinMetadata.PullRequestNumber,
		URL:     strings.TrimSpace(issue.ColinMetadata.PullRequestURL),
		State:   strings.TrimSpace(issue.ColinMetadata.PullRequestState),
		HeadRef: strings.TrimSpace(issue.ColinMetadata.PullRequestHeadRef),
		BaseRef: strings.TrimSpace(issue.ColinMetadata.PullRequestBaseRef),
	}, true
}

func attachedPullRequestsForRepository(prs []domain.PullRequestRef, owner, name string, adapter repohost.Adapter) []domain.PullRequestRef {
	if strings.EqualFold(owner, "local") {
		return prs
	}

	filtered := make([]domain.PullRequestRef, 0, len(prs))
	for _, pr := range prs {
		prOwner, prName, number, ok := adapter.ParsePullRequestURL(pr.URL)
		if !ok || pr.Number <= 0 || pr.Number != number {
			continue
		}
		if !strings.EqualFold(prOwner, owner) || !strings.EqualFold(prName, name) {
			continue
		}
		filtered = append(filtered, pr)
	}
	return filtered
}

func duplicatePullRequestsError(prs []domain.PullRequestRef) error {
	refs := make([]string, 0, len(prs))
	for _, pr := range prs {
		refs = append(refs, fmt.Sprintf("#%d", pr.Number))
	}
	return fmt.Errorf("%w: multiple pull requests linked to this issue: %s", ErrDuplicatePullRequests, strings.Join(refs, ", "))
}

func (m *Manager) createPullRequest(ctx context.Context, workspacePath string, issue domain.Issue, branch string) (string, error) {
	title := fmt.Sprintf("%s: %s", issue.Identifier, issue.Title)
	body, err := m.prBody(issue, branch, title)
	if err != nil {
		return "", err
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath)
	if err != nil {
		return "", err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return "", err
	}
	pr, err := client.CreatePullRequest(ctx, owner, name, CreatePullRequestInput{
		Title: title,
		Head:  branch,
		Base:  m.cfg.Workspace.BaseRef,
		Body:  body,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(pr.URL), nil
}

func (m *Manager) remoteRepository(ctx context.Context, workspacePath string) (string, string, error) {
	remoteURL, err := m.run(ctx, workspacePath, 15*time.Second, "git", "remote", "get-url", m.cfg.Repo.RemoteName)
	if err != nil {
		return "", "", err
	}
	return parseRemoteRepository(strings.TrimSpace(remoteURL))
}

func parseRemoteRepository(remoteURL string) (string, string, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", "", errors.New("empty remote url")
	}
	if strings.Contains(remoteURL, "://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return "", "", err
		}
		repoPath := strings.Trim(path.Clean(u.Path), "/")
		parts := strings.Split(repoPath, "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("unsupported remote url: %s", remoteURL)
		}
		return parts[len(parts)-2], strings.TrimSuffix(parts[len(parts)-1], ".git"), nil
	}
	if idx := strings.Index(remoteURL, ":"); idx >= 0 {
		repoPath := strings.Trim(remoteURL[idx+1:], "/")
		parts := strings.Split(repoPath, "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("unsupported remote url: %s", remoteURL)
		}
		return parts[len(parts)-2], strings.TrimSuffix(parts[len(parts)-1], ".git"), nil
	}
	if strings.HasSuffix(remoteURL, ".git") {
		return "local", strings.TrimSuffix(path.Base(remoteURL), ".git"), nil
	}
	return "", "", fmt.Errorf("unsupported remote url: %s", remoteURL)
}

func (m *Manager) fetchReviewThreads(ctx context.Context, workspacePath, owner, name string, prNumber int) ([]domain.ReviewThread, []domain.ReviewThread, bool, error) {
	var (
		cursor       string
		threads      []domain.ReviewThread
		codexThreads []domain.ReviewThread
		codexSeen    bool
	)
	client, err := m.repoHostClient()
	if err != nil {
		return nil, nil, false, err
	}
	for {
		resp, err := client.ReviewThreads(ctx, owner, name, prNumber, cursor)
		if err != nil {
			return nil, nil, false, err
		}

		if len(resp.Threads) == 0 && !resp.HasNextPage {
			return threads, codexThreads, codexSeen, nil
		}
		for _, node := range resp.Threads {
			thread, ok := parseReviewThread(node)
			if !ok {
				continue
			}
			containsAuthor, err := m.reviewThreadContainsCodexAuthor(ctx, workspacePath, node)
			if err != nil {
				return nil, nil, false, err
			}
			if containsAuthor {
				codexSeen = true
			}
			if thread.IsResolved {
				continue
			}
			threads = append(threads, thread)
			if containsAuthor {
				codexThreads = append(codexThreads, thread)
			}
		}
		if !resp.HasNextPage {
			break
		}
		if strings.TrimSpace(resp.EndCursor) == "" {
			break
		}
		cursor = resp.EndCursor
	}
	return threads, codexThreads, codexSeen, nil
}

func (m *Manager) reviewThreadContainsCodexAuthor(ctx context.Context, workspacePath string, node repohost.ReviewThread) (bool, error) {
	if reviewThreadPageContainsCodexAuthor(node) {
		return true, nil
	}
	if !reviewThreadCommentsHasNextPage(node) {
		return false, nil
	}
	threadID := node.ID
	if strings.TrimSpace(threadID) == "" {
		return false, nil
	}
	return m.fetchReviewThreadCommentAuthor(ctx, workspacePath, threadID)
}

func (m *Manager) fetchReviewThreadCommentAuthor(ctx context.Context, workspacePath, threadID string) (bool, error) {
	client, err := m.repoHostClient()
	if err != nil {
		return false, err
	}
	cursor := ""
	for {
		resp, err := client.ReviewThreadComments(ctx, threadID, cursor)
		if err != nil {
			return false, err
		}
		for _, node := range resp.Comments {
			if isCodexReviewAuthor(node.AuthorLogin) {
				return true, nil
			}
		}
		if !resp.HasNextPage {
			return false, nil
		}
		if strings.TrimSpace(resp.EndCursor) == "" {
			return false, nil
		}
		cursor = resp.EndCursor
	}
}

func (m *Manager) fetchCodexReviewReactions(ctx context.Context, workspacePath, owner, name string, prNumber int) (*time.Time, *time.Time, error) {
	var (
		cursor    string
		requested *time.Time
		approved  *time.Time
	)
	client, err := m.repoHostClient()
	if err != nil {
		return nil, nil, err
	}
	for {
		resp, err := client.PullRequestReactions(ctx, owner, name, prNumber, cursor)
		if err != nil {
			return nil, nil, err
		}

		if len(resp.Reactions) == 0 && !resp.HasNextPage {
			return requested, approved, nil
		}
		for _, node := range resp.Reactions {
			login := node.UserLogin
			if !isCodexReviewAuthor(login) {
				continue
			}
			if node.CreatedAt == nil {
				continue
			}
			switch strings.TrimSpace(node.Content) {
			case "EYES":
				requested = latestTimePtr(requested, *node.CreatedAt)
			case "THUMBS_UP":
				approved = latestTimePtr(approved, *node.CreatedAt)
			}
		}
		if !resp.HasNextPage {
			break
		}
		if strings.TrimSpace(resp.EndCursor) == "" {
			break
		}
		cursor = resp.EndCursor
	}
	return requested, approved, nil
}

func parseReviewThread(node repohost.ReviewThread) (domain.ReviewThread, bool) {
	if strings.TrimSpace(node.ID) == "" || strings.TrimSpace(node.Path) == "" {
		return domain.ReviewThread{}, false
	}
	if len(node.Comments.Comments) == 0 {
		return domain.ReviewThread{}, false
	}
	comment := node.Comments.Comments[len(node.Comments.Comments)-1]

	thread := domain.ReviewThread{
		ID:         node.ID,
		Path:       node.Path,
		CommentID:  comment.ID,
		CommentURL: comment.URL,
		Author:     comment.AuthorLogin,
		Body:       strings.TrimSpace(comment.Body),
		IsResolved: node.IsResolved,
		IsOutdated: node.IsOutdated,
		CanReply:   node.ViewerCanReply,
		CanResolve: node.ViewerCanResolve,
	}
	thread.Line = node.Line
	thread.StartLine = node.StartLine
	thread.CreatedAt = comment.CreatedAt
	return thread, true
}

func reviewThreadPageContainsCodexAuthor(node repohost.ReviewThread) bool {
	for _, comment := range node.Comments.Comments {
		if isCodexReviewAuthor(comment.AuthorLogin) {
			return true
		}
	}
	return false
}

func isCodexReviewAuthor(login string) bool {
	for _, candidate := range codexReviewLogins {
		if strings.EqualFold(strings.TrimSpace(login), candidate) {
			return true
		}
	}
	return false
}

func reviewThreadCommentsHasNextPage(node repohost.ReviewThread) bool {
	return node.Comments.HasNextPage
}

func latestTimePtr(current *time.Time, candidate time.Time) *time.Time {
	if current == nil || candidate.After(*current) {
		value := candidate
		return &value
	}
	return current
}

func (m *Manager) pushBranch(ctx context.Context, workspacePath, branch string) (string, error) {
	remoteName := strings.TrimSpace(m.cfg.Repo.RemoteName)
	output, err := m.run(ctx, workspacePath, 2*time.Minute, "git", "push", "-u", remoteName, branch)
	if err == nil {
		return output, nil
	}
	if !isNonFastForwardPushError(err) {
		return "", err
	}

	remoteBranch := remoteTrackingBranch(remoteName, branch)
	m.logger.Info(
		"push rejected as non-fast-forward; rebasing onto remote branch",
		"workspace_path", workspacePath,
		"branch", branch,
		"remote_branch", remoteBranch,
	)
	if _, fetchErr := m.run(ctx, workspacePath, 2*time.Minute, "git", "fetch", remoteName, branch); fetchErr != nil {
		return "", fmt.Errorf("push rejected and fetch for rebase failed: %w", fetchErr)
	}
	if _, resolveErr := m.revParse(ctx, workspacePath, remoteBranch); resolveErr != nil {
		return "", fmt.Errorf("push rejected and remote branch %s is unavailable for rebase: %w", remoteBranch, resolveErr)
	}
	if _, rebaseErr := m.run(ctx, workspacePath, 2*time.Minute, "git", "rebase", remoteBranch); rebaseErr != nil {
		m.abortRebase(ctx, workspacePath)
		return "", fmt.Errorf("push rejected and rebase onto %s failed: %w", remoteBranch, rebaseErr)
	}
	return m.run(ctx, workspacePath, 2*time.Minute, "git", "push", "-u", remoteName, branch)
}

func (m *Manager) abortRebase(ctx context.Context, workspacePath string) {
	if _, err := m.run(ctx, workspacePath, 30*time.Second, "git", "rebase", "--abort"); err != nil {
		m.logger.Warn("failed to abort rebase after publish recovery error", "workspace_path", workspacePath, "error", err)
	}
}

func isNonFastForwardPushError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "non-fast-forward") || strings.Contains(text, "failed to push some refs")
}

func remoteTrackingBranch(remoteName, branch string) string {
	return strings.TrimSpace(remoteName) + "/" + strings.TrimSpace(branch)
}

func (m *Manager) run(ctx context.Context, cwd string, timeout time.Duration, name string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, name, args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timeout: %w", name, cmdCtx.Err())
	}
	if err != nil {
		m.logger.Warn(
			"repo command failed",
			"command", commandString(name, args),
			"workspace_path", cwd,
			"error", err,
			"output", truncateOutput(string(output)),
		)
		return "", fmt.Errorf("%s: %w: %s", commandString(name, args), err, truncateOutput(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func commitMessage(issue domain.Issue) string {
	return fmt.Sprintf("%s: %s", issue.Identifier, issue.Title)
}

func (m *Manager) prBody(issue domain.Issue, branch string, prTitle string) (string, error) {
	templateText := strings.TrimSpace(m.cfg.Repo.PRTemplate)
	if templateText == "" {
		templateText = defaultPRTemplate()
	}
	return workflow.RenderTemplate(templateText, map[string]any{
		"issue":    prIssueMap(issue),
		"branch":   branch,
		"base_ref": m.cfg.Workspace.BaseRef,
		"pr_title": prTitle,
	})
}

func defaultPRTemplate() string {
	return `## Summary

Automated changes for {{.issue.identifier}}.

## Linear

- Issue: {{.issue.identifier}}
{{- if .issue.url }}
- URL: {{ .issue.url }}
{{- end }}`
}

func prIssueMap(issue domain.Issue) map[string]any {
	return map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": derefString(issue.Description),
		"state":       issue.State,
		"branch_name": derefString(issue.BranchName),
		"url":         derefString(issue.URL),
		"labels":      append([]string(nil), issue.Labels...),
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func commandString(name string, args []string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

func truncateOutput(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4096 {
		return value
	}
	return value[:4096]
}

func (m *Manager) repoHostClient() (repohost.Client, error) {
	m.hostOnce.Do(func() {
		if m.host != nil {
			return
		}
		adapter, err := repohost.Lookup(m.cfg.Repo.Backend)
		if err != nil {
			m.hostErr = err
			return
		}
		m.host, m.hostErr = adapter.NewClient(m.cfg, m.logger)
	})
	if m.hostErr != nil {
		return nil, m.hostErr
	}
	return m.host, nil
}
