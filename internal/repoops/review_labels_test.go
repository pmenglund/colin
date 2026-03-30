package repoops

import (
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func TestCodexReviewStateFromContext(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.March, 30, 10, 0, 0, 0, time.UTC)
	approvedAt := requestedAt.Add(1 * time.Minute)
	approvedBeforeRequest := requestedAt.Add(-1 * time.Minute)

	tests := []struct {
		name    string
		context ReviewContext
		want    CodexReviewState
	}{
		{
			name: "none without codex review signals",
			want: CodexReviewStateNone,
		},
		{
			name: "pending when request exists without approval",
			context: ReviewContext{
				CodexReviewRequestedAt: &requestedAt,
			},
			want: CodexReviewStatePending,
		},
		{
			name: "pending when approval is older than latest request",
			context: ReviewContext{
				CodexReviewRequestedAt: &requestedAt,
				CodexReviewApprovedAt:  &approvedBeforeRequest,
			},
			want: CodexReviewStatePending,
		},
		{
			name: "approved when approval is newer than latest request",
			context: ReviewContext{
				CodexReviewRequestedAt: &requestedAt,
				CodexReviewApprovedAt:  &approvedAt,
			},
			want: CodexReviewStateApproved,
		},
		{
			name: "unresolved feedback takes precedence",
			context: ReviewContext{
				CodexReviewRequestedAt: &requestedAt,
				CodexReviewApprovedAt:  &approvedAt,
				CodexReviewThreads:     []domain.GitHubReviewThread{{ID: "thread-1"}},
			},
			want: CodexReviewStateUnresolvedFeedback,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := CodexReviewStateFromContext(test.context); got != test.want {
				t.Fatalf("CodexReviewStateFromContext() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLinearLabelForCodexReviewState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state CodexReviewState
		want  string
	}{
		{state: CodexReviewStateNone, want: ""},
		{state: CodexReviewStatePending, want: domain.CodexReviewPendingLabel},
		{state: CodexReviewStateApproved, want: domain.CodexReviewApprovedLabel},
		{state: CodexReviewStateUnresolvedFeedback, want: domain.CodexReviewUnresolvedLabel},
	}

	for _, test := range tests {
		if got := LinearLabelForCodexReviewState(test.state); got != test.want {
			t.Fatalf("LinearLabelForCodexReviewState(%q) = %q, want %q", test.state, got, test.want)
		}
	}
}
