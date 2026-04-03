package userworkflow

import (
	"testing"

	"github.com/pmenglund/colin/internal/domain"
)

func TestSlackIssueSummaryBuildsLinksAndFingerprint(t *testing.T) {
	t.Parallel()

	issueURL := "https://linear.example.test/COLIN-153"
	summary := SlackIssueSummary(domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Done", "Merged"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
	}, domain.Issue{
		Identifier: "COLIN-153",
		Title:      "Slack support",
		State:      "Review",
		URL:        &issueURL,
		ColinMetadata: &domain.ColinMetadata{
			URL:            "https://colin.example.test/linear/issues/issue-1/metadata",
			PullRequestURL: "https://github.com/pmenglund/colin/pull/153",
		},
		ExecPlan: &domain.ExecPlan{
			URL: "https://colin.example.test/linear/issues/issue-1/exec-plan",
		},
	})

	if summary.NextAction != "Review the PR, then move the issue back to active work or forward to Merge." {
		t.Fatalf("NextAction = %q", summary.NextAction)
	}
	if summary.LinearURL != issueURL {
		t.Fatalf("LinearURL = %q, want %q", summary.LinearURL, issueURL)
	}
	if summary.PullRequestURL != "https://github.com/pmenglund/colin/pull/153" {
		t.Fatalf("PullRequestURL = %q", summary.PullRequestURL)
	}
	if summary.MetadataURL != "https://colin.example.test/linear/issues/issue-1/metadata" {
		t.Fatalf("MetadataURL = %q", summary.MetadataURL)
	}
	if summary.ExecPlanURL != "https://colin.example.test/linear/issues/issue-1/exec-plan" {
		t.Fatalf("ExecPlanURL = %q", summary.ExecPlanURL)
	}
	if summary.Fingerprint == "" {
		t.Fatal("Fingerprint = empty, want stable digest")
	}
}

func TestSlackIssueSummaryUsesStateSpecificNextActions(t *testing.T) {
	t.Parallel()

	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Done", "Merged"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
	}

	tests := []struct {
		name  string
		issue domain.Issue
		want  string
	}{
		{
			name:  "active",
			issue: domain.Issue{State: "Todo"},
			want:  "Colin is actively working this issue and will keep retrying while it stays in an active state.",
		},
		{
			name:  "paused active",
			issue: domain.Issue{State: "Todo", Labels: []string{"paused"}},
			want:  "Human action is required to clear the pause before Colin will continue.",
		},
		{
			name:  "merge",
			issue: domain.Issue{State: "Merge"},
			want:  "Colin is handling merge automation. Human action is only required if the issue returns to Review.",
		},
		{
			name:  "refine",
			issue: domain.Issue{State: "Refine"},
			want:  "Clarify the issue and move it back to active work when it is ready.",
		},
		{
			name:  "terminal",
			issue: domain.Issue{State: "Done"},
			want:  "The workflow is complete unless the issue is reopened.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SlackIssueSummary(cfg, tt.issue)
			if got.NextAction != tt.want {
				t.Fatalf("NextAction = %q, want %q", got.NextAction, tt.want)
			}
		})
	}
}
