package tracker

import (
	"context"

	"github.com/pmenglund/colin/internal/domain"
)

// Client describes the tracker operations the orchestrator depends on.
type Client interface {
	FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error)
	FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error)
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error)
	CreateIssueComment(ctx context.Context, issueID string, body string) (string, error)
	CreateCommentReply(ctx context.Context, issueID string, parentCommentID string, body string) (string, error)
}
