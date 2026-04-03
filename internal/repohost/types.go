package repohost

import (
	"context"
	"log/slog"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

type HostKind string

const (
	HostKindGitHub HostKind = "github"
)

// Repository identifies one repository on a remote hosting backend.
type Repository struct {
	Host  string
	Owner string
	Name  string
	URL   string
}

// CreatePullRequestInput captures the fields needed to open a pull request.
type CreatePullRequestInput struct {
	Title string
	Head  string
	Base  string
	Body  string
}

// PullRequest is the minimal pull-request data Colin needs for repo automation.
type PullRequest struct {
	Number      int
	URL         string
	State       string
	Body        string
	Mergeable   *bool
	HeadRefName string
	BaseRefName string
}

// ReviewComment is the minimal review comment payload Colin consumes.
type ReviewComment struct {
	ID          string
	Body        string
	URL         string
	AuthorLogin string
	CreatedAt   *time.Time
}

// ReviewCommentConnection captures the subset of comment pagination Colin needs.
type ReviewCommentConnection struct {
	Comments    []ReviewComment
	HasNextPage bool
	EndCursor   string
}

// ReviewThread is the typed review-thread payload consumed outside the backend transport.
type ReviewThread struct {
	ID               string
	Path             string
	Line             *int
	StartLine        *int
	IsResolved       bool
	IsOutdated       bool
	ViewerCanReply   bool
	ViewerCanResolve bool
	Comments         ReviewCommentConnection
}

// ReviewThreadPage is one page of review threads from the backend API.
type ReviewThreadPage struct {
	Threads     []ReviewThread
	HasNextPage bool
	EndCursor   string
}

// ReviewThreadCommentPage is one page of review thread comments from the backend API.
type ReviewThreadCommentPage struct {
	Comments    []ReviewComment
	HasNextPage bool
	EndCursor   string
}

// Reaction captures the minimal reaction data Colin uses for Codex review signals.
type Reaction struct {
	Content   string
	CreatedAt *time.Time
	UserLogin string
}

// ReactionPage is one page of pull-request reactions from the backend API.
type ReactionPage struct {
	Reactions   []Reaction
	HasNextPage bool
	EndCursor   string
}

// Client wraps the repository host operations Colin needs for publish, review, and merge automation.
type Client interface {
	ValidateAuth(ctx context.Context) error
	PullRequestByHead(ctx context.Context, owner, repo, head, base string) (*PullRequest, error)
	PullRequestByNumber(ctx context.Context, owner, repo string, number int) (*PullRequest, error)
	CreatePullRequest(ctx context.Context, owner, repo string, input CreatePullRequestInput) (*PullRequest, error)
	MergePullRequest(ctx context.Context, owner, repo string, number int, method string) error
	BranchExists(ctx context.Context, owner, repo, branch string) (bool, error)
	ReviewThreads(ctx context.Context, owner, repo string, number int, cursor string) (ReviewThreadPage, error)
	ReviewThreadComments(ctx context.Context, threadID, cursor string) (ReviewThreadCommentPage, error)
	PullRequestReactions(ctx context.Context, owner, repo string, number int, cursor string) (ReactionPage, error)
	ReplyToReviewThread(ctx context.Context, threadID, body string) error
	ResolveReviewThread(ctx context.Context, threadID string) error
}

// Adapter describes one supported repository hosting backend.
type Adapter interface {
	Kind() HostKind
	DisplayName() string
	CurrentToken() string
	IsValidToken(value string) bool
	RecommendedEnvVar() string
	ValidateTokenMessage() string
	ParseRepositoryURL(raw string) (Repository, error)
	ParsePullRequestURL(raw string) (owner string, repo string, number int, ok bool)
	RenderSetupInstructions(repo Repository, setupCommand string) string
	NewClient(cfg domain.ServiceConfig, logger *slog.Logger) (Client, error)
}
