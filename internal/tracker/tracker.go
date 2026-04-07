package tracker

import (
	"context"
	"errors"

	"github.com/pmenglund/colin/internal/domain"
)

var ErrDuplicateExecPlans = errors.New("duplicate_exec_plans")

// Client describes the tracker operations the orchestrator depends on.
type Client interface {
	// FetchCandidateIssueSnapshots returns lightweight scheduling snapshots for active issues.
	// Callers must not assume detail-only fields such as ColinMetadata, ExecPlan,
	// AttachedPullRequests, ReviewCycle, or ReviewFeedback are populated.
	FetchCandidateIssueSnapshots(ctx context.Context) ([]domain.Issue, error)
	// FetchIssueSnapshotsByStates returns lightweight snapshots for issues in the supplied states.
	// Callers must not assume detail-only fields such as ColinMetadata, ExecPlan,
	// AttachedPullRequests, ReviewCycle, or ReviewFeedback are populated.
	FetchIssueSnapshotsByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error)
	// FetchIssueSchedulingMetadataByIDs returns persisted Colin metadata for the supplied issues.
	// Only the returned metadata map is guaranteed to be populated.
	FetchIssueSchedulingMetadataByIDs(ctx context.Context, issueIDs []string) (map[string]domain.ColinMetadata, error)
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error)
	// FetchIssueByID returns the full issue detail used by UI pages, review preparation, and prompts.
	FetchIssueByID(ctx context.Context, issueID string) (domain.Issue, error)
	UpdateIssueState(ctx context.Context, issueID string, stateName string) error
	EnsureIssueLabel(ctx context.Context, labelName string) error
	AddIssueLabel(ctx context.Context, issueID string, labelName string) error
	RemoveIssueLabel(ctx context.Context, issueID string, labelName string) error
	ResolveGitAutomationState(ctx context.Context, issueID string, event string, targetBranch string) (string, bool, error)
	CreateIssueComment(ctx context.Context, issueID string, body string) (string, error)
	CreateCommentReply(ctx context.Context, issueID string, parentCommentID string, body string) (string, error)
	CreateAgentActivityThought(ctx context.Context, sessionID string, body string) error
	UpsertIssueMetadata(ctx context.Context, issueID string, metadata domain.ColinMetadata) (domain.ColinMetadata, error)
	UpsertIssueExecPlan(ctx context.Context, issueID string, plan domain.ExecPlan) (domain.ExecPlan, error)
	CurrentRateLimits() domain.RateLimitSnapshot
}

// RuntimeMetadata exposes tracker runtime behavior needed by service wiring.
type RuntimeMetadata interface {
	WatchedProjectIDs() []string
	SetUIBaseURLResolver(func(context.Context) string)
}

// RuntimeClient is the tracker contract required at service runtime.
type RuntimeClient interface {
	Client
	RuntimeMetadata
}
