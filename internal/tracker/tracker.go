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
}
