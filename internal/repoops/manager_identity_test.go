package repoops

import (
	"testing"

	"github.com/pmenglund/colin/internal/domain"
	githubhost "github.com/pmenglund/colin/internal/repohost/github"
)

func TestAttachedPullRequestsForRepositoryUsesStoredIdentity(t *testing.T) {
	t.Parallel()

	prs := []domain.PullRequestRef{
		{
			Backend:         "github",
			RepositoryOwner: "pmenglund",
			RepositoryName:  "colin",
			Number:          11,
			URL:             "not-a-parseable-pr-url",
		},
		{
			Backend:         "github",
			RepositoryOwner: "pmenglund",
			RepositoryName:  "sibling",
			Number:          11,
			URL:             "https://github.com/pmenglund/sibling/pull/11",
		},
	}

	filtered := attachedPullRequestsForRepository(prs, "pmenglund", "colin", githubhost.Adapter{})
	if len(filtered) != 1 {
		t.Fatalf("filtered length = %d, want 1", len(filtered))
	}
	if filtered[0].RepositoryOwner != "pmenglund" || filtered[0].RepositoryName != "colin" || filtered[0].Number != 11 {
		t.Fatalf("filtered[0] = %+v, want pmenglund/colin#11", filtered[0])
	}
	if filtered[0].URL != "not-a-parseable-pr-url" {
		t.Fatalf("filtered[0].URL = %q, want malformed URL preserved", filtered[0].URL)
	}
}
