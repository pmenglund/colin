package tracker

import (
	"context"
	"errors"

	"github.com/pmenglund/colin/internal/domain"
)

var ErrDuplicateExecPlans = errors.New("duplicate_exec_plans")

// Client describes the tracker operations the orchestrator depends on.
type Client interface {
	FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error)
	FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error)
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error)
	FetchIssueByID(ctx context.Context, issueID string) (domain.Issue, error)
	UpdateIssueState(ctx context.Context, issueID string, stateName string) error
	EnsureIssueLabel(ctx context.Context, labelName string) error
	AddIssueLabel(ctx context.Context, issueID string, labelName string) error
	RemoveIssueLabel(ctx context.Context, issueID string, labelName string) error
	ResolveGitAutomationState(ctx context.Context, issueID string, event string, targetBranch string) (string, bool, error)
	CreateIssueComment(ctx context.Context, issueID string, body string) (string, error)
	CreateCommentReply(ctx context.Context, issueID string, parentCommentID string, body string) (string, error)
	UpsertIssueMetadata(ctx context.Context, issueID string, metadata domain.ColinMetadata) (domain.ColinMetadata, error)
	UpsertIssueExecPlan(ctx context.Context, issueID string, plan domain.ExecPlan) (domain.ExecPlan, error)
	CurrentRateLimits() domain.RateLimitSnapshot
}
