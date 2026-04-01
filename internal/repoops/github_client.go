package repoops

import (
	"context"
	"time"
)

//go:generate go tool counterfeiter -o ./fakes/fake_github_client.go . GitHubClient

// GitHubPullRequest is the minimal pull-request data Colin needs for repo automation.
type GitHubPullRequest struct {
	Number      int
	URL         string
	State       string
	Body        string
	HeadRefName string
	BaseRefName string
}

// CreatePullRequestInput captures the fields needed to open a pull request.
type CreatePullRequestInput struct {
	Title string
	Head  string
	Base  string
	Body  string
}

// GitHubReviewComment is the minimal review comment payload Colin consumes.
type GitHubReviewComment struct {
	ID          string
	Body        string
	URL         string
	AuthorLogin string
	CreatedAt   *time.Time
}

// GitHubReviewCommentConnection captures the subset of GraphQL comment pagination Colin needs.
type GitHubReviewCommentConnection struct {
	Comments    []GitHubReviewComment
	HasNextPage bool
	EndCursor   string
}

// GitHubReviewThread is the typed review-thread payload consumed outside the GitHub transport.
type GitHubReviewThread struct {
	ID               string
	Path             string
	Line             *int
	StartLine        *int
	IsResolved       bool
	IsOutdated       bool
	ViewerCanReply   bool
	ViewerCanResolve bool
	Comments         GitHubReviewCommentConnection
}

// GitHubReviewThreadPage is one page of review threads from GitHub GraphQL.
type GitHubReviewThreadPage struct {
	Threads     []GitHubReviewThread
	HasNextPage bool
	EndCursor   string
}

// GitHubReviewThreadCommentPage is one page of review thread comments from GitHub GraphQL.
type GitHubReviewThreadCommentPage struct {
	Comments    []GitHubReviewComment
	HasNextPage bool
	EndCursor   string
}

// GitHubReaction captures the minimal reaction data Colin uses for Codex review signals.
type GitHubReaction struct {
	Content   string
	CreatedAt *time.Time
	UserLogin string
}

// GitHubReactionPage is one page of pull-request reactions from GitHub GraphQL.
type GitHubReactionPage struct {
	Reactions   []GitHubReaction
	HasNextPage bool
	EndCursor   string
}

// GitHubClient wraps the GitHub operations Colin needs for publish, review, and merge automation.
type GitHubClient interface {
	ValidateAuth(ctx context.Context) error
	PullRequestByHead(ctx context.Context, owner, repo, head, base string) (*GitHubPullRequest, error)
	PullRequestByNumber(ctx context.Context, owner, repo string, number int) (*GitHubPullRequest, error)
	CreatePullRequest(ctx context.Context, owner, repo string, input CreatePullRequestInput) (*GitHubPullRequest, error)
	MergePullRequest(ctx context.Context, owner, repo string, number int, method string) error
	BranchExists(ctx context.Context, owner, repo, branch string) (bool, error)
	ReviewThreads(ctx context.Context, owner, repo string, number int, cursor string) (GitHubReviewThreadPage, error)
	ReviewThreadComments(ctx context.Context, threadID, cursor string) (GitHubReviewThreadCommentPage, error)
	PullRequestReactions(ctx context.Context, owner, repo string, number int, cursor string) (GitHubReactionPage, error)
	ReplyToReviewThread(ctx context.Context, threadID, body string) error
	ResolveReviewThread(ctx context.Context, threadID string) error
}
