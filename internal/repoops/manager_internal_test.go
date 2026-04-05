package repoops

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
)

type stubRepoAdapter struct{}

func (stubRepoAdapter) Kind() repohost.HostKind { return "repoopstest" }
func (stubRepoAdapter) DisplayName() string     { return "RepoOps Test" }
func (stubRepoAdapter) CurrentToken() string    { return "" }
func (stubRepoAdapter) IsValidToken(string) bool {
	return true
}
func (stubRepoAdapter) RecommendedEnvVar() string    { return "REPOOPS_TEST_TOKEN" }
func (stubRepoAdapter) ValidateTokenMessage() string { return "" }
func (stubRepoAdapter) ParseRepositoryURL(string) (repohost.Repository, error) {
	return repohost.Repository{}, repohost.ErrUnsupportedRepositoryURL
}
func (stubRepoAdapter) ParsePullRequestURL(raw string) (string, string, int, bool) {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "fallback://acme/widgets#17":
		return "acme", "widgets", 17, true
	case "fallback://acme/console#17":
		return "acme", "console", 17, true
	default:
		return "", "", 0, false
	}
}
func (stubRepoAdapter) RenderSetupInstructions(repohost.Repository, string) string { return "" }
func (stubRepoAdapter) NewClient(domain.ServiceConfig, *slog.Logger) (repohost.Client, error) {
	return nil, nil
}

type stubRepoClient struct{}

func (stubRepoClient) ValidateAuth(context.Context) error { return nil }
func (stubRepoClient) PullRequestByHead(context.Context, string, string, string, string) (*repohost.PullRequest, error) {
	return nil, nil
}
func (stubRepoClient) PullRequestByNumber(context.Context, string, string, int) (*repohost.PullRequest, error) {
	return nil, nil
}
func (stubRepoClient) CreatePullRequest(context.Context, string, string, repohost.CreatePullRequestInput) (*repohost.PullRequest, error) {
	return nil, nil
}
func (stubRepoClient) MergePullRequest(context.Context, string, string, int, string) error {
	return nil
}
func (stubRepoClient) BranchExists(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (stubRepoClient) ReviewThreads(context.Context, string, string, int, string) (repohost.ReviewThreadPage, error) {
	return repohost.ReviewThreadPage{}, nil
}
func (stubRepoClient) ReviewThreadComments(context.Context, string, string) (repohost.ReviewThreadCommentPage, error) {
	return repohost.ReviewThreadCommentPage{}, nil
}
func (stubRepoClient) PullRequestReactions(context.Context, string, string, int, string) (repohost.ReactionPage, error) {
	return repohost.ReactionPage{}, nil
}
func (stubRepoClient) ReplyToReviewThread(context.Context, string, string) error { return nil }
func (stubRepoClient) ResolveReviewThread(context.Context, string) error         { return nil }

func TestAttachedPullRequestsForRepositoryUsesNormalizedIdentity(t *testing.T) {
	t.Parallel()

	prs := attachedPullRequestsForRepository([]domain.PullRequestRef{
		{
			Number:          17,
			URL:             "unparseable://ignored",
			Backend:         "repoopstest",
			RepositoryOwner: "acme",
			RepositoryName:  "widgets",
		},
	}, "acme", "widgets", stubRepoAdapter{})

	if len(prs) != 1 {
		t.Fatalf("len(prs) = %d, want 1", len(prs))
	}
	if prs[0].Number != 17 {
		t.Fatalf("prs[0].Number = %d, want 17", prs[0].Number)
	}
}

func TestAttachedPullRequestsForRepositoryFallsBackToURLParsingForLegacyRefs(t *testing.T) {
	t.Parallel()

	prs := attachedPullRequestsForRepository([]domain.PullRequestRef{
		{Number: 17, URL: "fallback://acme/widgets#17"},
		{Number: 17, URL: "fallback://acme/console#17"},
	}, "acme", "widgets", stubRepoAdapter{})

	if len(prs) != 1 {
		t.Fatalf("len(prs) = %d, want 1", len(prs))
	}
	if prs[0].URL != "fallback://acme/widgets#17" {
		t.Fatalf("prs[0].URL = %q, want fallback widgets URL", prs[0].URL)
	}
}
