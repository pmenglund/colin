package repoops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	"github.com/pmenglund/colin/internal/workflow"
)

var (
	ErrNotGitRepository           = errors.New("not_git_repository")
	ErrNoPullRequest              = errors.New("no_pull_request")
	ErrNoReviewableChanges        = errors.New("no_reviewable_changes")
	ErrDuplicatePullRequests      = errors.New("duplicate_pull_requests")
	ErrTrackedPullRequestMismatch = errors.New("tracked_pull_request_mismatch")
)

type MergeFailureKind string

const (
	MergeFailureKindTransient    MergeFailureKind = "transient"
	MergeFailureKindBaseAdvanced MergeFailureKind = "base_advanced"
	MergeFailureKindManual       MergeFailureKind = "manual"
)

var codexReviewLogins = []string{
	"chatgpt-codex-connector",
	"chatgpt-codex-connector[bot]",
}

// Result captures the outcome of a publish or merge operation.
type Result struct {
	Branch      string
	BaseRef     string
	BaseSHA     string
	RemoteName  string
	MergeMethod string
	PRNumber    int
	PRURL       string
	PRState     string
	PRHeadRef   string
	PRBaseRef   string
	PRBackend   string
	PROwner     string
	PRRepoName  string
	Commit      string
	Action      string
}

// MergeFailure classifies a merge failure so callers can choose between retrying and human handoff.
type MergeFailure struct {
	Kind            MergeFailureKind
	Err             error
	ExpectedBaseSHA string
	CurrentBaseSHA  string
}

func (m *MergeFailure) Error() string {
	if m == nil || m.Err == nil {
		return ""
	}
	return m.Err.Error()
}

func (m *MergeFailure) Unwrap() error {
	if m == nil {
		return nil
	}
	return m.Err
}

func IsMergeFailureKind(err error, kind MergeFailureKind) bool {
	var mergeFailure *MergeFailure
	return errors.As(err, &mergeFailure) && mergeFailure.Kind == kind
}

// MergeRecoveryValidation reports whether a claimed merge recovery actually updated the branch for merge retry.
type MergeRecoveryValidation struct {
	PreviousHeadSHA      string
	CurrentHeadSHA       string
	RemoteHeadSHA        string
	ExpectedBaseSHA      string
	CurrentBaseSHA       string
	MergeBaseSHA         string
	HeadChanged          bool
	RemoteHeadMatches    bool
	ContainsExpectedBase bool
}

func (v MergeRecoveryValidation) Valid() bool {
	return v.HeadChanged && v.RemoteHeadMatches && v.ContainsExpectedBase
}

// ReviewContext captures the PR, unresolved review threads, and Codex review signals for an issue branch.
type ReviewContext struct {
	PullRequest              domain.PullRequestRef
	Threads                  []domain.ReviewThread
	CodexReviewThreads       []domain.ReviewThread
	CodexReviewObserved      bool
	CodexReviewRequestedAt   *time.Time
	CodexReviewApprovedAt    *time.Time
	CodexPRReviewsEnabled    bool
	CodexPRReviewPolicyKnown bool
}

// ReviewCommentApproval is one collaborator approval on an invited Codex review comment.
type ReviewCommentApproval struct {
	Thread     domain.ReviewThread
	CommentID  string
	ReactionID string
	Reactor    string
}

// ReviewFollowUpScan captures unresolved review threads plus qualifying follow-up signals.
type ReviewFollowUpScan struct {
	PullRequest   domain.PullRequestRef
	Threads       []domain.ReviewThread
	Approvals     []ReviewCommentApproval
	HumanFeedback []domain.ReviewThread
}

// Manager performs git and repository-host operations for a workspace.
type Manager struct {
	cfg      domain.ServiceConfig
	logger   *slog.Logger
	host     repohost.Client
	hostOnce sync.Once
	hostErr  error
}

type repositoryCollaboratorChecker interface {
	IsCollaborator(ctx context.Context, owner, repo, user string) (bool, error)
}

// NewManager constructs a repository automation manager.
func NewManager(cfg domain.ServiceConfig, logger *slog.Logger) *Manager {
	return &Manager{cfg: cfg, logger: logger}
}

// NewManagerWithRepoHostClient constructs a repository automation manager with an injected repository-host client.
func NewManagerWithRepoHostClient(cfg domain.ServiceConfig, logger *slog.Logger, client repohost.Client) *Manager {
	return &Manager{cfg: cfg, logger: logger, host: client}
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

// IsRepositoryCollaborator reports whether the given GitHub user can collaborate on the repository.
func (m *Manager) IsRepositoryCollaborator(ctx context.Context, owner, repo, user string) (bool, error) {
	client, err := m.repoHostClient()
	if err != nil {
		return false, err
	}
	checker, ok := client.(repositoryCollaboratorChecker)
	if !ok {
		return false, errors.New("repository host client does not support collaborator checks")
	}
	return checker.IsCollaborator(ctx, owner, repo, user)
}

// FindUnresolvedReviewThreadByCommentID resolves one unresolved review thread from a review comment id.
func (m *Manager) FindUnresolvedReviewThreadByCommentID(ctx context.Context, issue domain.Issue, workspacePath, commentID string) (domain.PullRequestRef, domain.ReviewThread, bool, error) {
	pr, _, err := m.resolvePullRequest(ctx, issue, workspacePath, "", false)
	if err != nil {
		return domain.PullRequestRef{}, domain.ReviewThread{}, false, err
	}
	if pr == nil {
		return domain.PullRequestRef{}, domain.ReviewThread{}, false, nil
	}

	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return domain.PullRequestRef{}, domain.ReviewThread{}, false, err
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath, target.EffectiveRemoteName(m.cfg.Repo.RemoteName))
	if err != nil {
		return domain.PullRequestRef{}, domain.ReviewThread{}, false, err
	}

	thread, ok, err := m.findReviewThreadByCommentID(ctx, owner, name, pr.Number, commentID)
	if err != nil {
		return domain.PullRequestRef{}, domain.ReviewThread{}, false, err
	}
	return domain.PullRequestRef{
		Number:  pr.Number,
		URL:     pr.URL,
		State:   pr.State,
		HeadRef: pr.HeadRefName,
		BaseRef: pr.BaseRefName,
	}, thread, ok, nil
}

// Publish commits workspace changes, pushes the issue branch, and creates or reuses a PR.
func (m *Manager) Publish(ctx context.Context, issue domain.Issue, workspacePath string) (Result, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return Result{}, err
	}
	branch, err := m.currentBranch(ctx, workspacePath)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Branch:      branch,
		BaseRef:     target.BaseRef,
		RemoteName:  target.EffectiveRemoteName(m.cfg.Repo.RemoteName),
		MergeMethod: target.EffectiveMergeMethod(m.cfg.Repo.MergeMethod),
	}
	result.BaseSHA = m.captureBaseSHA(ctx, workspacePath, result.RemoteName, target.BaseRef)

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
		reviewable, err := m.ReviewableArtifact(ctx, workspacePath, issue)
		if err != nil {
			return Result{}, err
		}
		if !reviewable {
			return Result{}, ErrNoReviewableChanges
		}
	}

	if _, err := m.pushBranch(ctx, workspacePath, result.RemoteName, branch); err != nil {
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
	owner, name, err := m.remoteRepository(ctx, workspacePath, result.RemoteName)
	if err != nil {
		return Result{}, err
	}
	result.PRBackend = repohost.NormalizeBackend(m.cfg.Repo.Backend)
	result.PROwner = owner
	result.PRRepoName = name
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
	remoteName := strings.TrimSpace(result.RemoteName)
	if remoteName == "" {
		remoteName = strings.TrimSpace(m.cfg.Repo.RemoteName)
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath, remoteName)
	if err != nil {
		return result, err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return result, err
	}
	mergeMethod := strings.TrimSpace(result.MergeMethod)
	if mergeMethod == "" {
		mergeMethod = strings.TrimSpace(m.cfg.Repo.MergeMethod)
	}
	if err := client.MergePullRequest(ctx, owner, name, result.PRNumber, mergeMethod); err != nil {
		if !isRetryableNotMergeableError(err) {
			return result, err
		}

		refreshed, refreshErr := client.PullRequestByNumber(ctx, owner, name, result.PRNumber)
		if refreshErr != nil {
			return result, err
		}
		if refreshed == nil || refreshed.Mergeable == nil || !*refreshed.Mergeable {
			return result, m.classifyMergeFailure(ctx, workspacePath, result, refreshed, err)
		}

		m.logger.Info(
			"retrying merge after refresh because pull request is already mergeable",
			"workspace_path", workspacePath,
			"pr_number", result.PRNumber,
			"pr_url", result.PRURL,
		)
		if err := client.MergePullRequest(ctx, owner, name, result.PRNumber, mergeMethod); err != nil {
			return result, err
		}
	}
	result.Action = "merged"
	result.PRState = "MERGED"
	return result, nil
}

func isRetryableNotMergeableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "pull request is not mergeable") || strings.Contains(message, "not mergeable")
}

func (m *Manager) classifyMergeFailure(ctx context.Context, workspacePath string, result Result, refreshed *repohost.PullRequest, err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	currentBaseSHA := strings.TrimSpace(m.captureBaseSHA(ctx, workspacePath, result.RemoteName, result.BaseRef))
	kind := MergeFailureKindManual
	switch {
	case strings.Contains(message, "base branch was modified"):
		kind = MergeFailureKindBaseAdvanced
	case strings.Contains(message, "merge commit cannot be cleanly created"),
		strings.Contains(message, "resolve the merge conflicts locally"):
		kind = MergeFailureKindManual
	case refreshed == nil || refreshed.Mergeable == nil:
		kind = MergeFailureKindTransient
	case strings.TrimSpace(result.BaseSHA) != "" && currentBaseSHA != "" && currentBaseSHA != strings.TrimSpace(result.BaseSHA):
		kind = MergeFailureKindBaseAdvanced
	}
	return &MergeFailure{
		Kind:            kind,
		Err:             err,
		ExpectedBaseSHA: strings.TrimSpace(result.BaseSHA),
		CurrentBaseSHA:  currentBaseSHA,
	}
}

// ReviewContext returns the current PR and unresolved review threads for the issue branch.
func (m *Manager) ReviewContext(ctx context.Context, issue domain.Issue, workspacePath string) (ReviewContext, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return ReviewContext{}, err
	}
	pr, _, err := m.resolvePullRequest(ctx, issue, workspacePath, "", false)
	if err != nil {
		return ReviewContext{}, err
	}
	if pr == nil {
		return ReviewContext{}, nil
	}

	owner, name, err := m.remoteRepository(ctx, workspacePath, target.EffectiveRemoteName(m.cfg.Repo.RemoteName))
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
			Number:          pr.Number,
			URL:             pr.URL,
			State:           pr.State,
			HeadRef:         pr.HeadRefName,
			BaseRef:         pr.BaseRefName,
			Backend:         repohost.NormalizeBackend(m.cfg.Repo.Backend),
			RepositoryOwner: owner,
			RepositoryName:  name,
		},
		Threads:                  threads,
		CodexReviewThreads:       codexThreads,
		CodexReviewObserved:      codexObserved,
		CodexReviewRequestedAt:   requestedAt,
		CodexReviewApprovedAt:    approvedAt,
		CodexPRReviewsEnabled:    targetCodexPRReviewsEnabled(m.cfg, target),
		CodexPRReviewPolicyKnown: true,
	}, nil
}

// PullRequestChecks returns the current GitHub checks for the issue's pull request.
func (m *Manager) PullRequestChecks(ctx context.Context, issue domain.Issue, workspacePath string) (repohost.PullRequestCheckRollup, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return repohost.PullRequestCheckRollup{}, err
	}
	pr, _, err := m.resolvePullRequest(ctx, issue, workspacePath, "", false)
	if err != nil {
		return repohost.PullRequestCheckRollup{}, err
	}
	if pr == nil {
		return repohost.PullRequestCheckRollup{}, nil
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath, target.EffectiveRemoteName(m.cfg.Repo.RemoteName))
	if err != nil {
		return repohost.PullRequestCheckRollup{}, err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return repohost.PullRequestCheckRollup{}, err
	}
	rollup, err := client.PullRequestChecks(ctx, owner, name, pr.Number)
	if err != nil {
		return repohost.PullRequestCheckRollup{}, err
	}
	if rollup.PullRequest.Number == 0 {
		rollup.PullRequest = *pr
	}
	if strings.TrimSpace(rollup.PullRequest.URL) == "" {
		rollup.PullRequest.URL = pr.URL
	}
	if strings.TrimSpace(rollup.HeadSHA) == "" {
		rollup.HeadSHA = strings.TrimSpace(rollup.PullRequest.HeadSHA)
	}
	return rollup, nil
}

func targetCodexPRReviewsEnabled(cfg domain.ServiceConfig, target domain.TargetConfig) bool {
	if target.CodexPRReviewsEnabled {
		return true
	}
	return len(cfg.Targets) == 0 && cfg.Repo.CodexPRReviewsEnabled
}

// ReviewFollowUpScan returns unresolved review threads and qualifying collaborator approvals on invited Codex comments.
func (m *Manager) ReviewFollowUpScan(ctx context.Context, issue domain.Issue, workspacePath string) (ReviewFollowUpScan, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return ReviewFollowUpScan{}, err
	}
	pr, _, err := m.resolvePullRequest(ctx, issue, workspacePath, "", false)
	if err != nil {
		return ReviewFollowUpScan{}, err
	}
	if pr == nil {
		return ReviewFollowUpScan{}, nil
	}

	owner, name, err := m.remoteRepository(ctx, workspacePath, target.EffectiveRemoteName(m.cfg.Repo.RemoteName))
	if err != nil {
		return ReviewFollowUpScan{}, err
	}
	scan, err := m.fetchReviewFollowUpScan(ctx, owner, name, pr.Number)
	if err != nil {
		return ReviewFollowUpScan{}, err
	}
	scan.PullRequest = domain.PullRequestRef{
		Number:  pr.Number,
		URL:     pr.URL,
		State:   pr.State,
		HeadRef: pr.HeadRefName,
		BaseRef: pr.BaseRefName,
	}
	return scan, nil
}

// CurrentBranch returns the current checked-out git branch for the workspace.
func (m *Manager) CurrentBranch(ctx context.Context, workspacePath string) (string, error) {
	return m.currentBranch(ctx, workspacePath)
}

// ReviewableArtifact reports whether the workspace contains reviewable repository changes.
func (m *Manager) ReviewableArtifact(ctx context.Context, workspacePath string, issue ...domain.Issue) (bool, error) {
	dirty, err := m.isDirty(ctx, workspacePath)
	if err != nil {
		return false, err
	}
	if dirty {
		return true, nil
	}
	if len(issue) > 0 {
		return m.branchAheadOfBase(ctx, workspacePath, issue[0])
	}
	return m.branchAheadOfBase(ctx, workspacePath, domain.Issue{})
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

func (m *Manager) captureBaseSHA(ctx context.Context, workspacePath string, remoteName string, baseRef string) string {
	baseRef = strings.TrimSpace(baseRef)
	if baseRef == "" {
		return ""
	}
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		remoteName = strings.TrimSpace(m.cfg.Repo.RemoteName)
	}
	if remoteName != "" {
		if sha, err := m.remoteRefSHA(ctx, workspacePath, remoteName, baseRef); err == nil {
			return sha
		}
	}
	candidates := []string{
		remoteName + "/" + baseRef,
		baseRef,
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if sha, err := m.revParse(ctx, workspacePath, candidate); err == nil {
			return sha
		}
	}
	return ""
}

func (m *Manager) mergeBase(ctx context.Context, workspacePath, left, right string) (string, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "merge-base", left, right)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (m *Manager) isAncestorCommit(ctx context.Context, workspacePath, ancestor, descendant string) (bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = workspacePath
	output, err := cmd.CombinedOutput()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return false, fmt.Errorf("git merge-base --is-ancestor timeout: %w", cmdCtx.Err())
	}
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	m.logger.Warn(
		"repo command failed",
		"command", commandString("git", []string{"merge-base", "--is-ancestor", ancestor, descendant}),
		"workspace_path", workspacePath,
		"error", err,
		"output", truncateOutput(string(output)),
	)
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w: %s", ancestor, descendant, err, truncateOutput(string(output)))
}

// ValidateMergeRecovery verifies that a reported merge recovery actually advanced the branch and incorporated the expected base commit.
func (m *Manager) ValidateMergeRecovery(ctx context.Context, workspacePath string, before Result, after Result) (MergeRecoveryValidation, error) {
	branch := strings.TrimSpace(after.Branch)
	if branch == "" {
		branch = strings.TrimSpace(before.Branch)
	}

	currentHead := strings.TrimSpace(after.Commit)
	if currentHead == "" {
		head, err := m.revParse(ctx, workspacePath, "HEAD")
		if err != nil {
			return MergeRecoveryValidation{}, fmt.Errorf("resolve repaired branch head: %w", err)
		}
		currentHead = head
	}

	currentBaseSHA := strings.TrimSpace(after.BaseSHA)
	if currentBaseSHA == "" {
		baseRef := strings.TrimSpace(after.BaseRef)
		if baseRef == "" {
			baseRef = strings.TrimSpace(before.BaseRef)
		}
		currentBaseSHA = strings.TrimSpace(m.captureBaseSHA(ctx, workspacePath, after.RemoteName, baseRef))
	}

	remoteHeadSHA := ""
	remoteName := strings.TrimSpace(after.RemoteName)
	if remoteName == "" {
		remoteName = strings.TrimSpace(m.cfg.Repo.RemoteName)
	}
	if remoteName != "" && branch != "" {
		sha, err := m.remoteRefSHA(ctx, workspacePath, remoteName, branch)
		if err != nil {
			return MergeRecoveryValidation{}, fmt.Errorf("resolve remote branch head for %s/%s: %w", remoteName, branch, err)
		}
		remoteHeadSHA = strings.TrimSpace(sha)
	}

	mergeBaseSHA := ""
	if currentBaseSHA != "" && currentHead != "" {
		mergeBase, err := m.mergeBase(ctx, workspacePath, currentHead, currentBaseSHA)
		if err != nil {
			return MergeRecoveryValidation{}, fmt.Errorf("resolve merge-base for repaired branch: %w", err)
		}
		mergeBaseSHA = mergeBase
	}

	containsExpectedBase := true
	expectedBase := strings.TrimSpace(before.BaseSHA)
	if expectedBase != "" && currentHead != "" {
		ok, err := m.isAncestorCommit(ctx, workspacePath, expectedBase, currentHead)
		if err != nil {
			return MergeRecoveryValidation{}, fmt.Errorf("check repaired branch ancestry: %w", err)
		}
		containsExpectedBase = ok
	}

	validation := MergeRecoveryValidation{
		PreviousHeadSHA:      strings.TrimSpace(before.Commit),
		CurrentHeadSHA:       currentHead,
		RemoteHeadSHA:        remoteHeadSHA,
		ExpectedBaseSHA:      expectedBase,
		CurrentBaseSHA:       currentBaseSHA,
		MergeBaseSHA:         mergeBaseSHA,
		HeadChanged:          strings.TrimSpace(before.Commit) != "" && currentHead != "" && strings.TrimSpace(before.Commit) != currentHead,
		RemoteHeadMatches:    remoteHeadSHA == "" || currentHead == "" || remoteHeadSHA == currentHead,
		ContainsExpectedBase: containsExpectedBase,
	}

	return validation, nil
}

func (m *Manager) remoteRefSHA(ctx context.Context, workspacePath, remoteName, ref string) (string, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "ls-remote", remoteName, "refs/heads/"+ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("missing ls-remote output for %s/%s", remoteName, ref)
	}
	return fields[0], nil
}

func (m *Manager) isDirty(ctx context.Context, workspacePath string) (bool, error) {
	out, err := m.run(ctx, workspacePath, 15*time.Second, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (m *Manager) branchAheadOfBase(ctx context.Context, workspacePath string, issue domain.Issue) (bool, error) {
	baseRef, err := m.baseComparisonRef(ctx, workspacePath, issue)
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

func (m *Manager) baseComparisonRef(ctx context.Context, workspacePath string, issue domain.Issue) (string, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return "", err
	}
	candidates := []string{
		strings.TrimSpace(target.BaseRef),
		target.EffectiveRemoteName(m.cfg.Repo.RemoteName) + "/" + strings.TrimSpace(target.BaseRef),
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
	return "", fmt.Errorf("resolve base ref %q", strings.TrimSpace(target.BaseRef))
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

func (m *Manager) findPullRequest(ctx context.Context, issue domain.Issue, workspacePath, branch string) (*repohost.PullRequest, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return nil, err
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath, target.EffectiveRemoteName(m.cfg.Repo.RemoteName))
	if err != nil {
		return nil, err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return nil, err
	}
	return client.PullRequestByHead(ctx, owner, name, branch, target.BaseRef)
}

func (m *Manager) findPullRequestByNumber(ctx context.Context, workspacePath, remoteName string, number int) (*repohost.PullRequest, error) {
	if number <= 0 {
		return nil, nil
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath, remoteName)
	if err != nil {
		return nil, err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return nil, err
	}
	return client.PullRequestByNumber(ctx, owner, name, number)
}

func (m *Manager) resolvePullRequest(ctx context.Context, issue domain.Issue, workspacePath, currentBranch string, allowCreate bool) (*repohost.PullRequest, bool, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return nil, false, err
	}
	remoteName := target.EffectiveRemoteName(m.cfg.Repo.RemoteName)
	tracked, hasTracked := trackedPullRequest(issue)
	if hasTracked {
		pr, err := m.resolveTrackedPullRequest(ctx, workspacePath, remoteName, tracked, currentBranch)
		return pr, false, err
	}

	owner, name, err := m.remoteRepository(ctx, workspacePath, remoteName)
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
		pr, err := m.findPullRequestByNumber(ctx, workspacePath, remoteName, attached[0].Number)
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
		pr, err := m.findPullRequest(ctx, issue, workspacePath, currentBranch)
		if err != nil {
			return nil, false, err
		}
		if pr != nil {
			return pr, false, nil
		}
	} else {
		for _, branch := range m.reviewLookupBranches(ctx, issue, workspacePath) {
			pr, err := m.findPullRequest(ctx, issue, workspacePath, branch)
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
	pr, err := m.findPullRequest(ctx, issue, workspacePath, currentBranch)
	if err != nil {
		return nil, false, err
	}
	if pr == nil {
		return nil, false, ErrNoPullRequest
	}
	pr.URL = url
	return pr, true, nil
}

func (m *Manager) resolveTrackedPullRequest(ctx context.Context, workspacePath, remoteName string, tracked domain.PullRequestRef, currentBranch string) (*repohost.PullRequest, error) {
	pr, err := m.findPullRequestByNumber(ctx, workspacePath, remoteName, tracked.Number)
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
		Number:          issue.ColinMetadata.PullRequestNumber,
		URL:             strings.TrimSpace(issue.ColinMetadata.PullRequestURL),
		State:           strings.TrimSpace(issue.ColinMetadata.PullRequestState),
		HeadRef:         strings.TrimSpace(issue.ColinMetadata.PullRequestHeadRef),
		BaseRef:         strings.TrimSpace(issue.ColinMetadata.PullRequestBaseRef),
		Backend:         strings.TrimSpace(issue.ColinMetadata.PullRequestBackend),
		RepositoryOwner: strings.TrimSpace(issue.ColinMetadata.PullRequestRepoOwner),
		RepositoryName:  strings.TrimSpace(issue.ColinMetadata.PullRequestRepoName),
	}, true
}

func attachedPullRequestsForRepository(prs []domain.PullRequestRef, owner, name string, adapter repohost.Adapter) []domain.PullRequestRef {
	if strings.EqualFold(owner, "local") {
		return prs
	}

	filtered := make([]domain.PullRequestRef, 0, len(prs))
	for _, pr := range prs {
		prOwner := strings.TrimSpace(pr.RepositoryOwner)
		prName := strings.TrimSpace(pr.RepositoryName)
		number := pr.Number
		if prOwner == "" || prName == "" || number <= 0 {
			var ok bool
			prOwner, prName, number, ok = adapter.ParsePullRequestURL(pr.URL)
			if !ok || pr.Number <= 0 || pr.Number != number {
				continue
			}
		}
		if backend := strings.TrimSpace(pr.Backend); backend != "" && !strings.EqualFold(backend, string(adapter.Kind())) {
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
		refs = append(refs, pullRequestDisplayRef(pr))
	}
	return fmt.Errorf("%w: multiple pull requests linked to this issue: %s", ErrDuplicatePullRequests, strings.Join(refs, ", "))
}

func pullRequestDisplayRef(pr domain.PullRequestRef) string {
	if strings.TrimSpace(pr.RepositoryOwner) != "" && strings.TrimSpace(pr.RepositoryName) != "" && pr.Number > 0 {
		return fmt.Sprintf("%s/%s#%d", pr.RepositoryOwner, pr.RepositoryName, pr.Number)
	}
	if pr.Number > 0 {
		return fmt.Sprintf("#%d", pr.Number)
	}
	if url := strings.TrimSpace(pr.URL); url != "" {
		return url
	}
	return "unknown"
}

func (m *Manager) createPullRequest(ctx context.Context, workspacePath string, issue domain.Issue, branch string) (string, error) {
	target, err := domain.ResolveTargetForIssue(m.cfg, issue)
	if err != nil {
		return "", err
	}
	title := fmt.Sprintf("%s: %s", issue.Identifier, issue.Title)
	body, err := m.prBody(issue, branch, title, target)
	if err != nil {
		return "", err
	}
	owner, name, err := m.remoteRepository(ctx, workspacePath, target.EffectiveRemoteName(m.cfg.Repo.RemoteName))
	if err != nil {
		return "", err
	}
	client, err := m.repoHostClient()
	if err != nil {
		return "", err
	}
	pr, err := client.CreatePullRequest(ctx, owner, name, repohost.CreatePullRequestInput{
		Title: title,
		Head:  branch,
		Base:  target.BaseRef,
		Body:  body,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(pr.URL), nil
}

func (m *Manager) remoteRepository(ctx context.Context, workspacePath, remoteName string) (string, string, error) {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		remoteName = strings.TrimSpace(m.cfg.Repo.RemoteName)
	}
	if remoteName == "" {
		remoteName = "origin"
	}
	remoteURL, err := m.run(ctx, workspacePath, 15*time.Second, "git", "remote", "get-url", remoteName)
	if err != nil {
		return "", "", err
	}
	remoteURL = strings.TrimSpace(remoteURL)
	adapter, err := repohost.Lookup(m.cfg.Repo.Backend)
	if err != nil {
		return "", "", err
	}
	repo, err := adapter.ParseRepositoryURL(remoteURL)
	if err != nil {
		if owner, name, ok := localRemoteRepository(remoteURL); ok {
			return owner, name, nil
		}
		return "", "", err
	}
	return strings.TrimSpace(repo.Owner), strings.TrimSpace(repo.Name), nil
}

func localRemoteRepository(remoteURL string) (string, string, bool) {
	raw := strings.TrimSpace(remoteURL)
	if raw == "" {
		return "", "", false
	}
	if parsed, err := url.Parse(raw); err == nil && strings.EqualFold(parsed.Scheme, "file") {
		raw = parsed.Path
	}
	if strings.Contains(raw, "://") || strings.Contains(raw, "@") {
		return "", "", false
	}
	name := strings.TrimSpace(strings.TrimSuffix(filepath.Base(raw), ".git"))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "", "", false
	}
	return "local", name, true
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

func (m *Manager) fetchReviewFollowUpScan(ctx context.Context, owner, name string, prNumber int) (ReviewFollowUpScan, error) {
	client, err := m.repoHostClient()
	if err != nil {
		return ReviewFollowUpScan{}, err
	}
	var (
		cursor            string
		threads           []domain.ReviewThread
		approvals         []ReviewCommentApproval
		humanFeedback     []domain.ReviewThread
		collaboratorCache = map[string]bool{}
	)
	for {
		resp, err := client.ReviewThreads(ctx, owner, name, prNumber, cursor)
		if err != nil {
			return ReviewFollowUpScan{}, err
		}
		if len(resp.Threads) == 0 && !resp.HasNextPage {
			return ReviewFollowUpScan{Threads: threads, Approvals: approvals, HumanFeedback: humanFeedback}, nil
		}
		for _, node := range resp.Threads {
			thread, ok := parseReviewThread(node)
			if !ok || thread.IsResolved {
				continue
			}
			threads = append(threads, thread)
			if isHumanReviewFeedbackThread(thread) {
				humanFeedback = append(humanFeedback, thread)
			}

			comments, err := m.reviewThreadComments(ctx, node)
			if err != nil {
				return ReviewFollowUpScan{}, err
			}
			for _, comment := range comments {
				if !isInvitedCodexReviewComment(comment) {
					continue
				}
				commentID := reviewCommentID(comment)
				commentNumber, err := strconv.ParseInt(commentID, 10, 64)
				if err != nil {
					continue
				}
				reactionID, reactor, found, err := m.latestCollaboratorReviewCommentApproval(ctx, owner, name, commentNumber, collaboratorCache)
				if err != nil {
					return ReviewFollowUpScan{}, err
				}
				if !found {
					continue
				}
				approvals = append(approvals, ReviewCommentApproval{
					Thread:     thread,
					CommentID:  commentID,
					ReactionID: reactionID,
					Reactor:    reactor,
				})
			}
		}
		if !resp.HasNextPage || strings.TrimSpace(resp.EndCursor) == "" {
			break
		}
		cursor = resp.EndCursor
	}
	return ReviewFollowUpScan{Threads: threads, Approvals: approvals, HumanFeedback: humanFeedback}, nil
}

func (m *Manager) findReviewThreadByCommentID(ctx context.Context, owner, name string, prNumber int, commentID string) (domain.ReviewThread, bool, error) {
	commentID = strings.TrimSpace(commentID)
	if commentID == "" {
		return domain.ReviewThread{}, false, nil
	}
	client, err := m.repoHostClient()
	if err != nil {
		return domain.ReviewThread{}, false, err
	}
	cursor := ""
	for {
		resp, err := client.ReviewThreads(ctx, owner, name, prNumber, cursor)
		if err != nil {
			return domain.ReviewThread{}, false, err
		}
		for _, node := range resp.Threads {
			thread, ok := parseReviewThread(node)
			if !ok || thread.IsResolved {
				continue
			}
			match, err := m.reviewThreadHasCommentID(ctx, node, commentID)
			if err != nil {
				return domain.ReviewThread{}, false, err
			}
			if match {
				return thread, true, nil
			}
		}
		if !resp.HasNextPage || strings.TrimSpace(resp.EndCursor) == "" {
			return domain.ReviewThread{}, false, nil
		}
		cursor = resp.EndCursor
	}
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

func (m *Manager) reviewThreadHasCommentID(ctx context.Context, node repohost.ReviewThread, commentID string) (bool, error) {
	if reviewThreadPageHasCommentID(node, commentID) {
		return true, nil
	}
	if !reviewThreadCommentsHasNextPage(node) {
		return false, nil
	}
	threadID := strings.TrimSpace(node.ID)
	if threadID == "" {
		return false, nil
	}
	return m.fetchReviewThreadCommentID(ctx, threadID, commentID)
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

func (m *Manager) fetchReviewThreadCommentID(ctx context.Context, threadID, commentID string) (bool, error) {
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
			if reviewCommentMatchesID(node, commentID) {
				return true, nil
			}
		}
		if !resp.HasNextPage || strings.TrimSpace(resp.EndCursor) == "" {
			return false, nil
		}
		cursor = resp.EndCursor
	}
}

func (m *Manager) reviewThreadComments(ctx context.Context, node repohost.ReviewThread) ([]repohost.ReviewComment, error) {
	comments := append([]repohost.ReviewComment(nil), node.Comments.Comments...)
	if !reviewThreadCommentsHasNextPage(node) {
		return comments, nil
	}
	threadID := strings.TrimSpace(node.ID)
	if threadID == "" {
		return comments, nil
	}
	client, err := m.repoHostClient()
	if err != nil {
		return nil, err
	}
	cursor := strings.TrimSpace(node.Comments.EndCursor)
	for {
		resp, err := client.ReviewThreadComments(ctx, threadID, cursor)
		if err != nil {
			return nil, err
		}
		comments = append(comments, resp.Comments...)
		if !resp.HasNextPage || strings.TrimSpace(resp.EndCursor) == "" {
			return comments, nil
		}
		cursor = resp.EndCursor
	}
}

func (m *Manager) latestCollaboratorReviewCommentApproval(ctx context.Context, owner, name string, commentID int64, collaboratorCache map[string]bool) (string, string, bool, error) {
	client, err := m.repoHostClient()
	if err != nil {
		return "", "", false, err
	}
	page := 1
	var (
		latestID   int64
		latestUser string
	)
	for {
		resp, err := client.PullRequestReviewCommentReactions(ctx, owner, name, commentID, page)
		if err != nil {
			return "", "", false, err
		}
		for _, reaction := range resp.Reactions {
			if !strings.EqualFold(strings.TrimSpace(reaction.Content), "+1") || reaction.ID <= 0 {
				continue
			}
			login := strings.TrimSpace(reaction.UserLogin)
			if login == "" {
				continue
			}
			allowed, ok := collaboratorCache[login]
			if !ok {
				allowed, err = m.IsRepositoryCollaborator(ctx, owner, name, login)
				if err != nil {
					return "", "", false, err
				}
				collaboratorCache[login] = allowed
			}
			if !allowed {
				continue
			}
			if reaction.ID > latestID {
				latestID = reaction.ID
				latestUser = login
			}
		}
		if resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}
	if latestID == 0 {
		return "", "", false, nil
	}
	return strconv.FormatInt(latestID, 10), latestUser, true, nil
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
		CommentID:  reviewCommentID(comment),
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

func reviewThreadPageHasCommentID(node repohost.ReviewThread, commentID string) bool {
	for _, comment := range node.Comments.Comments {
		if reviewCommentMatchesID(comment, commentID) {
			return true
		}
	}
	return false
}

func reviewCommentID(comment repohost.ReviewComment) string {
	if value := strings.TrimSpace(comment.DatabaseID); value != "" {
		return value
	}
	return strings.TrimSpace(comment.ID)
}

func reviewCommentMatchesID(comment repohost.ReviewComment, commentID string) bool {
	target := strings.TrimSpace(commentID)
	if target == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(comment.ID), target) || strings.EqualFold(strings.TrimSpace(comment.DatabaseID), target)
}

func isInvitedCodexReviewComment(comment repohost.ReviewComment) bool {
	if !isCodexReviewAuthor(comment.AuthorLogin) {
		return false
	}
	body := strings.ToLower(strings.TrimSpace(comment.Body))
	return strings.Contains(body, "useful? react with")
}

func isHumanReviewFeedbackThread(thread domain.ReviewThread) bool {
	author := strings.TrimSpace(thread.Author)
	if author == "" || isCodexReviewAuthor(author) {
		return false
	}
	if strings.EqualFold(author, "colin") || strings.EqualFold(author, "colin[bot]") {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(thread.Body), "[colin]")
}

func isCodexReviewAuthor(login string) bool {
	for _, candidate := range codexReviewLogins {
		if strings.EqualFold(strings.TrimSpace(login), candidate) {
			return true
		}
	}
	return false
}

// IsCodexReviewAuthor reports whether the login belongs to the Codex review app.
func IsCodexReviewAuthor(login string) bool {
	return isCodexReviewAuthor(login)
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

func (m *Manager) pushBranch(ctx context.Context, workspacePath, remoteName, branch string) (string, error) {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		remoteName = strings.TrimSpace(m.cfg.Repo.RemoteName)
	}
	if remoteName == "" {
		remoteName = "origin"
	}
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

func (m *Manager) prBody(issue domain.Issue, branch string, prTitle string, target domain.TargetConfig) (string, error) {
	templateText := target.EffectivePRTemplate(m.cfg.Repo.PRTemplate)
	if templateText == "" {
		templateText = defaultPRTemplate()
	}
	return workflow.RenderTemplate(templateText, map[string]any{
		"issue":        prIssueMap(issue),
		"branch":       branch,
		"base_ref":     target.BaseRef,
		"pr_title":     prTitle,
		"last_summary": issueLastSummary(issue),
	})
}

func defaultPRTemplate() string {
	return `{{- if .last_summary -}}
{{ .last_summary }}
{{- else -}}
## Why

Colin opened this pull request for {{.issue.identifier}}: {{.issue.title}}.

- Linear issue: {{.issue.identifier}}
{{- if .issue.url }}
- URL: {{ .issue.url }}
{{- end }}
- PR title: {{.pr_title}}

## Before

No coding handoff summary was available when Colin opened this pull request.

## After

Review the commits in this pull request for the implementation details.

## Evidence

No coding handoff evidence was available in Colin metadata.
{{- end -}}`
}

func prIssueMap(issue domain.Issue) map[string]any {
	return map[string]any{
		"id":             issue.ID,
		"identifier":     issue.Identifier,
		"title":          issue.Title,
		"description":    derefString(issue.Description),
		"state":          issue.State,
		"branch_name":    derefString(issue.BranchName),
		"url":            derefString(issue.URL),
		"labels":         append([]string(nil), issue.Labels...),
		"colin_metadata": prColinMetadataMap(issue.ColinMetadata),
	}
}

func prColinMetadataMap(metadata *domain.ColinMetadata) map[string]any {
	if metadata == nil {
		return map[string]any{
			"last_summary": "",
		}
	}
	return map[string]any{
		"last_summary": metadata.LastSummary,
	}
}

func issueLastSummary(issue domain.Issue) string {
	if issue.ColinMetadata == nil {
		return ""
	}
	return strings.TrimSpace(issue.ColinMetadata.LastSummary)
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
