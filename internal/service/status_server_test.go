package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/orchestrator"
)

type fakeTrackerClient struct {
	issues              []domain.Issue
	fetchIssuesByStates func(context.Context, []string) ([]domain.Issue, error)
}

func (f *fakeTrackerClient) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	return append([]domain.Issue(nil), f.issues...), nil
}

func (f *fakeTrackerClient) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	if f.fetchIssuesByStates != nil {
		return f.fetchIssuesByStates(ctx, states)
	}
	return append([]domain.Issue(nil), f.issues...), nil
}

func (f *fakeTrackerClient) FetchIssueStatesByIDs(context.Context, []string) ([]domain.Issue, error) {
	return nil, nil
}

func TestBuildStatusPageIncludesIssuesWithPullRequestsOutsideActiveStates(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	number := 12

	snapshot := domain.Snapshot{
		GeneratedAt: now,
		Running: []domain.SnapshotRunning{
			{IssueID: "running-1", Identifier: "COLIN-94"},
		},
		Retrying: []domain.RetryEntry{
			{IssueID: "retry-1", Identifier: "COLIN-95", Attempt: 2, DueAt: now.Add(10 * time.Second)},
		},
	}
	issues := []domain.Issue{
		{
			ID:         "review-1",
			Identifier: "COLIN-82",
			Title:      "Needs review",
			State:      "Review",
			PullRequests: []domain.PullRequest{{
				URL:          "https://github.com/pmenglund/colin/pull/12",
				Title:        "COLIN-82: add PR info",
				Number:       &number,
				Status:       "open",
				Branch:       "colin/COLIN-82",
				TargetBranch: "main",
				RepoLogin:    "pmenglund",
				RepoName:     "colin",
			}},
		},
		{
			ID:         "running-1",
			Identifier: "COLIN-94",
			Title:      "Currently running",
			State:      "Review",
		},
		{
			ID:         "retry-1",
			Identifier: "COLIN-95",
			Title:      "Waiting to retry",
			State:      "Done",
		},
		{
			ID:         "done-1",
			Identifier: "COLIN-70",
			Title:      "Finished with no PR",
			State:      "Done",
		},
	}

	page := buildStatusPage(snapshot, issues, []string{"Todo", "In Progress"})

	if page.VisibleIssues != 3 {
		t.Fatalf("page.VisibleIssues = %d, want 3", page.VisibleIssues)
	}
	if page.PendingPRCount != 1 {
		t.Fatalf("page.PendingPRCount = %d, want 1", page.PendingPRCount)
	}
	if page.OpenPRCount != 1 {
		t.Fatalf("page.OpenPRCount = %d, want 1", page.OpenPRCount)
	}
	if got := page.Issues[0].Identifier; got != "COLIN-94" {
		t.Fatalf("first issue = %q, want running issue first", got)
	}

	var foundPRIssue bool
	for _, issue := range page.Issues {
		if issue.Identifier == "COLIN-82" {
			foundPRIssue = true
			if issue.Runtime != "idle" {
				t.Fatalf("issue.Runtime = %q, want idle", issue.Runtime)
			}
			if len(issue.PRs) != 1 {
				t.Fatalf("len(issue.PRs) = %d, want 1", len(issue.PRs))
			}
			if issue.PRs[0].StatusClass != "open" {
				t.Fatalf("issue.PRs[0].StatusClass = %q, want open", issue.PRs[0].StatusClass)
			}
		}
		if issue.Identifier == "COLIN-70" {
			t.Fatal("done issue without PRs should not be visible")
		}
	}
	if !foundPRIssue {
		t.Fatal("issue with pull request was not included")
	}
}

func TestBuildStatusPageTreatsMetadataLightPullRequestsAsPendingNotOpen(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)

	page := buildStatusPage(domain.Snapshot{GeneratedAt: now}, []domain.Issue{{
		ID:         "issue-1",
		Identifier: "COLIN-84",
		Title:      "Bare attachment",
		State:      "Review",
		PullRequests: []domain.PullRequest{{
			URL:   "https://github.com/pmenglund/colin/pull/4",
			Title: "COLIN-84: bare attachment",
		}},
	}}, []string{"Todo", "In Progress"})

	if page.PendingPRCount != 1 {
		t.Fatalf("page.PendingPRCount = %d, want 1", page.PendingPRCount)
	}
	if page.OpenPRCount != 0 {
		t.Fatalf("page.OpenPRCount = %d, want 0", page.OpenPRCount)
	}
	if len(page.Issues) != 1 {
		t.Fatalf("len(page.Issues) = %d, want 1", len(page.Issues))
	}
	if len(page.Issues[0].PRs) != 1 {
		t.Fatalf("len(page.Issues[0].PRs) = %d, want 1", len(page.Issues[0].PRs))
	}
	if page.Issues[0].PRs[0].StatusClass != "pending" {
		t.Fatalf("page.Issues[0].PRs[0].StatusClass = %q, want pending", page.Issues[0].PRs[0].StatusClass)
	}
	if page.Issues[0].PRs[0].Status != "pending" {
		t.Fatalf("page.Issues[0].PRs[0].Status = %q, want pending", page.Issues[0].PRs[0].Status)
	}
	if page.Issues[0].PRs[0].Number != "#4" {
		t.Fatalf("page.Issues[0].PRs[0].Number = %q, want #4", page.Issues[0].PRs[0].Number)
	}
	if page.Issues[0].PRs[0].Repository != "pmenglund/colin" {
		t.Fatalf("page.Issues[0].PRs[0].Repository = %q, want pmenglund/colin", page.Issues[0].PRs[0].Repository)
	}
}

func TestBuildStatusPageCountsDraftPullRequestsSeparately(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	number := 9

	page := buildStatusPage(domain.Snapshot{GeneratedAt: now}, []domain.Issue{{
		ID:         "issue-1",
		Identifier: "COLIN-89",
		Title:      "Draft PR",
		State:      "Review",
		PullRequests: []domain.PullRequest{{
			URL:       "https://github.com/pmenglund/colin/pull/9",
			Title:     "COLIN-89: draft pr",
			Number:    &number,
			Status:    "open",
			Draft:     true,
			RepoLogin: "pmenglund",
			RepoName:  "colin",
		}},
	}}, []string{"Todo", "In Progress"})

	if page.PendingPRCount != 1 {
		t.Fatalf("page.PendingPRCount = %d, want 1", page.PendingPRCount)
	}
	if page.OpenPRCount != 0 {
		t.Fatalf("page.OpenPRCount = %d, want 0", page.OpenPRCount)
	}
	if page.DraftPRCount != 1 {
		t.Fatalf("page.DraftPRCount = %d, want 1", page.DraftPRCount)
	}
	if got := page.Issues[0].PRs[0].StatusClass; got != "draft" {
		t.Fatalf("page.Issues[0].PRs[0].StatusClass = %q, want draft", got)
	}
	if got := page.Issues[0].PRs[0].Status; got != "draft" {
		t.Fatalf("page.Issues[0].PRs[0].Status = %q, want draft", got)
	}
}

func TestBuildStatusPageTreatsUnknownNonTerminalPullRequestStatusAsPending(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)

	page := buildStatusPage(domain.Snapshot{GeneratedAt: now}, []domain.Issue{{
		ID:         "issue-1",
		Identifier: "COLIN-97",
		Title:      "PR waiting on review",
		State:      "Review",
		PullRequests: []domain.PullRequest{{
			URL:       "https://github.com/pmenglund/colin/pull/17",
			Title:     "COLIN-97: waiting on review",
			Status:    "review_required",
			RepoLogin: "pmenglund",
			RepoName:  "colin",
		}},
	}}, []string{"Todo", "In Progress"})

	if page.VisibleIssues != 1 {
		t.Fatalf("page.VisibleIssues = %d, want 1", page.VisibleIssues)
	}
	if page.PendingPRCount != 1 {
		t.Fatalf("page.PendingPRCount = %d, want 1", page.PendingPRCount)
	}
	if page.OpenPRCount != 0 {
		t.Fatalf("page.OpenPRCount = %d, want 0", page.OpenPRCount)
	}
	if got := page.Issues[0].PRs[0].StatusClass; got != "pending" {
		t.Fatalf("page.Issues[0].PRs[0].StatusClass = %q, want pending", got)
	}
	if got := page.Issues[0].PRs[0].Status; got != "review_required" {
		t.Fatalf("page.Issues[0].PRs[0].Status = %q, want review_required", got)
	}
}

func TestBuildStatusPageTreatsDraftStatusStringAsDraft(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)

	page := buildStatusPage(domain.Snapshot{GeneratedAt: now}, []domain.Issue{{
		ID:         "issue-1",
		Identifier: "COLIN-99",
		Title:      "PR is draft",
		State:      "Review",
		PullRequests: []domain.PullRequest{{
			URL:       "https://github.com/pmenglund/colin/pull/19",
			Title:     "COLIN-99: draft status only",
			Status:    "draft",
			RepoLogin: "pmenglund",
			RepoName:  "colin",
		}},
	}}, []string{"Todo", "In Progress"})

	if page.PendingPRCount != 1 {
		t.Fatalf("page.PendingPRCount = %d, want 1", page.PendingPRCount)
	}
	if page.OpenPRCount != 0 {
		t.Fatalf("page.OpenPRCount = %d, want 0", page.OpenPRCount)
	}
	if page.DraftPRCount != 1 {
		t.Fatalf("page.DraftPRCount = %d, want 1", page.DraftPRCount)
	}
	if got := page.Issues[0].PRs[0].StatusClass; got != "draft" {
		t.Fatalf("page.Issues[0].PRs[0].StatusClass = %q, want draft", got)
	}
}

func TestBuildStatusPageHidesNonActiveIssuesWithMergedStatusStringOnly(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)

	page := buildStatusPage(domain.Snapshot{GeneratedAt: now}, []domain.Issue{{
		ID:         "issue-1",
		Identifier: "COLIN-100",
		Title:      "Merged PR only",
		State:      "Review",
		PullRequests: []domain.PullRequest{{
			URL:       "https://github.com/pmenglund/colin/pull/20",
			Title:     "COLIN-100: merged status only",
			Status:    "merged",
			RepoLogin: "pmenglund",
			RepoName:  "colin",
		}},
	}}, []string{"Todo", "In Progress"})

	if page.VisibleIssues != 0 {
		t.Fatalf("page.VisibleIssues = %d, want 0", page.VisibleIssues)
	}
	if page.PendingPRCount != 0 {
		t.Fatalf("page.PendingPRCount = %d, want 0", page.PendingPRCount)
	}
}

func TestBuildStatusPageFetchesAllProjectIssues(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)

	var gotStates []string
	svc := &Service{
		logger: logger,
		orch:   orch,
		runtime: orchestrator.Runtime{
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					ActiveStates: []string{"Todo", "In Progress"},
				},
			},
			Tracker: &fakeTrackerClient{
				fetchIssuesByStates: func(_ context.Context, states []string) ([]domain.Issue, error) {
					gotStates = states
					return []domain.Issue{{
						ID:         "issue-1",
						Identifier: "COLIN-82",
						Title:      "Needs review",
						State:      "Review",
						PullRequests: []domain.PullRequest{{
							URL:    "https://github.com/pmenglund/colin/pull/12",
							Title:  "COLIN-82: add PR info",
							Status: "open",
						}},
					}}, nil
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	page, err := svc.buildStatusPage(context.Background())
	if err != nil {
		t.Fatalf("buildStatusPage() error = %v", err)
	}
	if gotStates != nil {
		t.Fatalf("FetchIssuesByStates() states = %v, want nil", gotStates)
	}
	if page.VisibleIssues != 1 {
		t.Fatalf("page.VisibleIssues = %d, want 1", page.VisibleIssues)
	}
}

func TestBuildStatusPageHidesNonActiveIssuesWithOnlyClosedOrMergedPullRequests(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	mergedAt := now.Add(-time.Hour)
	closedAt := now.Add(-30 * time.Minute)

	page := buildStatusPage(domain.Snapshot{GeneratedAt: now}, []domain.Issue{
		{
			ID:         "issue-closed",
			Identifier: "COLIN-85",
			Title:      "Closed PR only",
			State:      "Review",
			PullRequests: []domain.PullRequest{{
				URL:    "https://github.com/pmenglund/colin/pull/5",
				Title:  "COLIN-85: closed pr",
				Status: "closed",
			}},
		},
		{
			ID:         "issue-closed-at",
			Identifier: "COLIN-85A",
			Title:      "Closed by timestamp only",
			State:      "Review",
			PullRequests: []domain.PullRequest{{
				URL:      "https://github.com/pmenglund/colin/pull/5a",
				Title:    "COLIN-85A: closed pr",
				ClosedAt: &closedAt,
			}},
		},
		{
			ID:         "issue-merged",
			Identifier: "COLIN-86",
			Title:      "Merged PR only",
			State:      "Done",
			PullRequests: []domain.PullRequest{{
				URL:      "https://github.com/pmenglund/colin/pull/6",
				Title:    "COLIN-86: merged pr",
				Status:   "open",
				MergedAt: &mergedAt,
			}},
		},
	}, []string{"Todo", "In Progress"})

	if page.VisibleIssues != 0 {
		t.Fatalf("page.VisibleIssues = %d, want 0", page.VisibleIssues)
	}
	if page.PendingPRCount != 0 {
		t.Fatalf("page.PendingPRCount = %d, want 0", page.PendingPRCount)
	}
}

func TestBuildStatusPageSortsPullRequestsDeterministically(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	issue := domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-103",
		Title:      "Multiple pull requests",
		State:      "Review",
		PullRequests: []domain.PullRequest{
			{
				URL:    "https://github.com/pmenglund/colin/pull/12",
				Title:  "COLIN-103: later pull request",
				Status: "open",
			},
			{
				URL:    "https://github.com/pmenglund/colin/pull/3",
				Title:  "COLIN-103: earlier pull request",
				Status: "open",
			},
			{
				URL:    "https://github.com/other/repo/pull/1",
				Title:  "COLIN-103: different repository",
				Status: "open",
			},
			{
				URL:    "https://example.com/not-a-pr",
				Title:  "COLIN-103: fallback ordering",
				Status: "open",
			},
		},
	}

	page := buildStatusPage(domain.Snapshot{GeneratedAt: now}, []domain.Issue{issue}, []string{"Todo", "In Progress"})

	if len(page.Issues) != 1 {
		t.Fatalf("len(page.Issues) = %d, want 1", len(page.Issues))
	}
	if len(page.Issues[0].PRs) != 4 {
		t.Fatalf("len(page.Issues[0].PRs) = %d, want 4", len(page.Issues[0].PRs))
	}

	got := []string{
		page.Issues[0].PRs[0].URL,
		page.Issues[0].PRs[1].URL,
		page.Issues[0].PRs[2].URL,
		page.Issues[0].PRs[3].URL,
	}
	want := []string{
		"https://example.com/not-a-pr",
		"https://github.com/other/repo/pull/1",
		"https://github.com/pmenglund/colin/pull/3",
		"https://github.com/pmenglund/colin/pull/12",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("page.Issues[0].PRs[%d].URL = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildStatusPullRequestTreatsClosedTimestampAsClosed(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)

	view := buildStatusPullRequest(domain.PullRequest{
		URL:      "https://github.com/pmenglund/colin/pull/15",
		Title:    "COLIN-94: timestamp-only close",
		ClosedAt: &now,
	})

	if view.StatusClass != "closed" {
		t.Fatalf("view.StatusClass = %q, want closed", view.StatusClass)
	}
	if view.Status != "closed" {
		t.Fatalf("view.Status = %q, want closed", view.Status)
	}
}

func TestBuildStatusPullRequestTreatsUnknownStatusAsPendingClass(t *testing.T) {
	view := buildStatusPullRequest(domain.PullRequest{
		URL:    "https://github.com/pmenglund/colin/pull/18",
		Title:  "COLIN-98: unknown status",
		Status: "review_required",
	})

	if view.StatusClass != "pending" {
		t.Fatalf("view.StatusClass = %q, want pending", view.StatusClass)
	}
	if view.Status != "review_required" {
		t.Fatalf("view.Status = %q, want review_required", view.Status)
	}
}

func TestBuildStatusPullRequestTreatsDraftStatusStringAsDraft(t *testing.T) {
	view := buildStatusPullRequest(domain.PullRequest{
		URL:    "https://github.com/pmenglund/colin/pull/19",
		Title:  "COLIN-99: draft status only",
		Status: "draft",
	})

	if view.StatusClass != "draft" {
		t.Fatalf("view.StatusClass = %q, want draft", view.StatusClass)
	}
	if view.Status != "draft" {
		t.Fatalf("view.Status = %q, want draft", view.Status)
	}
}

func TestBuildStatusPullRequestTreatsMergedStatusStringAsMerged(t *testing.T) {
	view := buildStatusPullRequest(domain.PullRequest{
		URL:    "https://github.com/pmenglund/colin/pull/20",
		Title:  "COLIN-100: merged status only",
		Status: "merged",
	})

	if view.StatusClass != "merged" {
		t.Fatalf("view.StatusClass = %q, want merged", view.StatusClass)
	}
	if view.Status != "merged" {
		t.Fatalf("view.Status = %q, want merged", view.Status)
	}
}

func TestBuildStatusPullRequestPrefersTerminalStatusOverDraftFlag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		pr              domain.PullRequest
		wantStatusClass string
		wantStatus      string
	}{
		{
			name: "closed",
			pr: domain.PullRequest{
				URL:    "https://github.com/pmenglund/colin/pull/21",
				Title:  "COLIN-101: closed draft",
				Status: "closed",
				Draft:  true,
			},
			wantStatusClass: "closed",
			wantStatus:      "closed",
		},
		{
			name: "merged",
			pr: domain.PullRequest{
				URL:    "https://github.com/pmenglund/colin/pull/22",
				Title:  "COLIN-102: merged draft",
				Status: "merged",
				Draft:  true,
			},
			wantStatusClass: "merged",
			wantStatus:      "merged",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := buildStatusPullRequest(tc.pr)
			if view.StatusClass != tc.wantStatusClass {
				t.Fatalf("view.StatusClass = %q, want %q", view.StatusClass, tc.wantStatusClass)
			}
			if view.Status != tc.wantStatus {
				t.Fatalf("view.Status = %q, want %q", view.Status, tc.wantStatus)
			}
		})
	}
}

func TestBuildStatusPullRequestParsesGitHubURLWithSuffix(t *testing.T) {
	view := buildStatusPullRequest(domain.PullRequest{
		URL:   "https://github.com/pmenglund/colin/pull/15/files",
		Title: "COLIN-94: review changes",
	})

	if view.Number != "#15" {
		t.Fatalf("view.Number = %q, want #15", view.Number)
	}
	if view.Repository != "pmenglund/colin" {
		t.Fatalf("view.Repository = %q, want pmenglund/colin", view.Repository)
	}
	if view.URL != "https://github.com/pmenglund/colin/pull/15" {
		t.Fatalf("view.URL = %q, want canonical github pull request url", view.URL)
	}
}

func TestStatusPageTemplateRendersPullRequestInfo(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	page := statusPageData{
		GeneratedAt:    now,
		VisibleIssues:  1,
		PendingPRCount: 1,
		OpenPRCount:    1,
		Issues: []statusIssueView{{
			Identifier: "COLIN-82",
			Title:      "Needs review",
			State:      "Review",
			Runtime:    "idle",
			IssueURL:   "https://linear.app/issue/COLIN-82",
			BranchName: "colin/COLIN-82",
			PRs: []statusPullRequest{{
				URL:          "https://github.com/pmenglund/colin/pull/12",
				Title:        "COLIN-82: add PR info",
				Status:       "open",
				StatusClass:  "open",
				Number:       "#12",
				Branch:       "colin/COLIN-82",
				TargetBranch: "main",
				Repository:   "pmenglund/colin",
			}},
		}},
	}

	var buf bytes.Buffer
	if err := statusPageTemplate.Execute(&buf, page); err != nil {
		t.Fatalf("statusPageTemplate.Execute() error = %v", err)
	}

	rendered := buf.String()
	for _, want := range []string{
		"COLIN-82: add PR info",
		"https://github.com/pmenglund/colin/pull/12",
		"pmenglund/colin",
		"colin/COLIN-82",
		"open",
		"<strong>1</strong><span>Open PRs</span>",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered page missing %q", want)
		}
	}
}

func TestStatusPageTemplateDoesNotRenderLeadingSeparatorWithoutRepository(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	page := statusPageData{
		GeneratedAt:   now,
		VisibleIssues: 1,
		Issues: []statusIssueView{{
			Identifier: "COLIN-82",
			Title:      "Needs review",
			State:      "Review",
			Runtime:    "idle",
			PRs: []statusPullRequest{{
				URL:          "https://github.com/pmenglund/colin/pull/12",
				Title:        "COLIN-82: add PR info",
				Status:       "open",
				StatusClass:  "open",
				Number:       "#12",
				Branch:       "colin/COLIN-82",
				TargetBranch: "main",
			}},
		}},
	}

	var buf bytes.Buffer
	if err := statusPageTemplate.Execute(&buf, page); err != nil {
		t.Fatalf("statusPageTemplate.Execute() error = %v", err)
	}

	rendered := buf.String()
	if strings.Contains(rendered, "· <code>colin/COLIN-82</code> to <code>main</code>") {
		t.Fatal("rendered page included a leading separator without repository metadata")
	}
	if !strings.Contains(rendered, "<code>colin/COLIN-82</code> to <code>main</code>") {
		t.Fatal("rendered page missing branch metadata")
	}
}

func TestStatusPageTemplateOmitsEmptyPRMetaContainer(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	page := statusPageData{
		GeneratedAt:   now,
		VisibleIssues: 1,
		Issues: []statusIssueView{{
			Identifier: "COLIN-84",
			Title:      "Bare attachment",
			State:      "Review",
			Runtime:    "idle",
			PRs: []statusPullRequest{{
				URL:         "https://github.com/pmenglund/colin/pull/4",
				Title:       "COLIN-84: bare attachment",
				Status:      "pending",
				StatusClass: "pending",
				Number:      "#4",
			}},
		}},
	}

	var buf bytes.Buffer
	if err := statusPageTemplate.Execute(&buf, page); err != nil {
		t.Fatalf("statusPageTemplate.Execute() error = %v", err)
	}

	rendered := buf.String()
	if strings.Contains(rendered, `<div class="pr-meta">`) {
		t.Fatalf("rendered page included empty PR metadata container: %q", rendered)
	}
}

func TestStatusPageTemplateRendersSourceBranchWithoutTarget(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	page := statusPageData{
		GeneratedAt:   now,
		VisibleIssues: 1,
		Issues: []statusIssueView{{
			Identifier: "COLIN-82",
			Title:      "Needs review",
			State:      "Review",
			Runtime:    "idle",
			PRs: []statusPullRequest{{
				URL:         "https://github.com/pmenglund/colin/pull/12",
				Title:       "COLIN-82: add PR info",
				Status:      "open",
				StatusClass: "open",
				Number:      "#12",
				Branch:      "colin/COLIN-82",
				Repository:  "pmenglund/colin",
			}},
		}},
	}

	var buf bytes.Buffer
	if err := statusPageTemplate.Execute(&buf, page); err != nil {
		t.Fatalf("statusPageTemplate.Execute() error = %v", err)
	}

	rendered := buf.String()
	if !strings.Contains(rendered, "pmenglund/colin") {
		t.Fatalf("rendered page missing repository metadata: %q", rendered)
	}
	if !strings.Contains(rendered, "branch <code>colin/COLIN-82</code>") {
		t.Fatalf("rendered page missing source-only branch metadata: %q", rendered)
	}
}

func TestStatusPageTemplateRendersTargetBranchWithoutSource(t *testing.T) {
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	page := statusPageData{
		GeneratedAt:   now,
		VisibleIssues: 1,
		Issues: []statusIssueView{{
			Identifier: "COLIN-82",
			Title:      "Needs review",
			State:      "Review",
			Runtime:    "idle",
			PRs: []statusPullRequest{{
				URL:          "https://github.com/pmenglund/colin/pull/12",
				Title:        "COLIN-82: add PR info",
				Status:       "open",
				StatusClass:  "open",
				Number:       "#12",
				TargetBranch: "main",
				Repository:   "pmenglund/colin",
			}},
		}},
	}

	var buf bytes.Buffer
	if err := statusPageTemplate.Execute(&buf, page); err != nil {
		t.Fatalf("statusPageTemplate.Execute() error = %v", err)
	}

	rendered := buf.String()
	if !strings.Contains(rendered, "pmenglund/colin") {
		t.Fatalf("rendered page missing repository metadata: %q", rendered)
	}
	if !strings.Contains(rendered, "target <code>main</code>") {
		t.Fatalf("rendered page missing target-only branch metadata: %q", rendered)
	}
}

func TestStatusHandlerServesStatusAPIWithPullRequestInfo(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	number := 12
	svc := &Service{
		logger: logger,
		orch:   orch,
		runtime: orchestrator.Runtime{
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					ActiveStates: []string{"Todo", "In Progress"},
				},
			},
			Tracker: &fakeTrackerClient{
				issues: []domain.Issue{{
					ID:         "issue-1",
					Identifier: "COLIN-82",
					Title:      "Needs review",
					State:      "Review",
					PullRequests: []domain.PullRequest{{
						URL:       "https://github.com/pmenglund/colin/pull/12",
						Title:     "COLIN-82: add PR info",
						Number:    &number,
						Status:    "open",
						RepoLogin: "pmenglund",
						RepoName:  "colin",
					}},
				}},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var page statusPageData
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if page.PendingPRCount != 1 {
		t.Fatalf("page.PendingPRCount = %d, want 1", page.PendingPRCount)
	}
	if page.OpenPRCount != 1 {
		t.Fatalf("page.OpenPRCount = %d, want 1", page.OpenPRCount)
	}
	if len(page.Issues) != 1 {
		t.Fatalf("len(page.Issues) = %d, want 1", len(page.Issues))
	}
	if len(page.Issues[0].PRs) != 1 {
		t.Fatalf("len(page.Issues[0].PRs) = %d, want 1", len(page.Issues[0].PRs))
	}
	if page.Issues[0].PRs[0].Repository != "pmenglund/colin" {
		t.Fatalf("page.Issues[0].PRs[0].Repository = %q, want pmenglund/colin", page.Issues[0].PRs[0].Repository)
	}
}

func TestStatusHandlerServesStatusAPIWithPendingPullRequestInfo(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	svc := &Service{
		logger: logger,
		orch:   orch,
		runtime: orchestrator.Runtime{
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					ActiveStates: []string{"Todo", "In Progress"},
				},
			},
			Tracker: &fakeTrackerClient{
				issues: []domain.Issue{{
					ID:         "issue-1",
					Identifier: "COLIN-84",
					Title:      "Bare attachment",
					State:      "Review",
					PullRequests: []domain.PullRequest{{
						URL:   "https://github.com/pmenglund/colin/pull/4",
						Title: "COLIN-84: bare attachment",
					}},
				}},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusOK)
	}

	var page statusPageData
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if page.PendingPRCount != 1 {
		t.Fatalf("page.PendingPRCount = %d, want 1", page.PendingPRCount)
	}
	if page.OpenPRCount != 0 {
		t.Fatalf("page.OpenPRCount = %d, want 0", page.OpenPRCount)
	}
	if got := page.Issues[0].PRs[0].StatusClass; got != "pending" {
		t.Fatalf("page.Issues[0].PRs[0].StatusClass = %q, want pending", got)
	}
}

func TestBuildStatusPageHidesNonActiveIssuesWithTerminalStatusEvenIfDraftFlagSet(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	page := buildStatusPage(domain.Snapshot{GeneratedAt: now}, []domain.Issue{
		{
			ID:         "issue-closed",
			Identifier: "COLIN-101",
			Title:      "Closed draft PR",
			State:      "Review",
			PullRequests: []domain.PullRequest{{
				URL:    "https://github.com/pmenglund/colin/pull/21",
				Title:  "COLIN-101: closed draft",
				Status: "closed",
				Draft:  true,
			}},
		},
		{
			ID:         "issue-merged",
			Identifier: "COLIN-102",
			Title:      "Merged draft PR",
			State:      "Review",
			PullRequests: []domain.PullRequest{{
				URL:    "https://github.com/pmenglund/colin/pull/22",
				Title:  "COLIN-102: merged draft",
				Status: "merged",
				Draft:  true,
			}},
		},
	}, []string{"Todo", "In Progress"})

	if page.VisibleIssues != 0 {
		t.Fatalf("page.VisibleIssues = %d, want 0", page.VisibleIssues)
	}
	if page.PendingPRCount != 0 {
		t.Fatalf("page.PendingPRCount = %d, want 0", page.PendingPRCount)
	}
	if page.DraftPRCount != 0 {
		t.Fatalf("page.DraftPRCount = %d, want 0", page.DraftPRCount)
	}
}

func TestStatusHandlerServesStatusPageWithPullRequestInfo(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	number := 12
	svc := &Service{
		logger: logger,
		orch:   orch,
		runtime: orchestrator.Runtime{
			Config: domain.ServiceConfig{
				Tracker: domain.TrackerConfig{
					ActiveStates: []string{"Todo", "In Progress"},
				},
			},
			Tracker: &fakeTrackerClient{
				issues: []domain.Issue{{
					ID:         "issue-1",
					Identifier: "COLIN-82",
					Title:      "Needs review",
					State:      "Review",
					PullRequests: []domain.PullRequest{{
						URL:          "https://github.com/pmenglund/colin/pull/12",
						Title:        "COLIN-82: add PR info",
						Number:       &number,
						Status:       "open",
						Branch:       "colin/COLIN-82",
						TargetBranch: "main",
						RepoLogin:    "pmenglund",
						RepoName:     "colin",
					}},
				}},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"Colin Status",
		"COLIN-82: add PR info",
		"https://github.com/pmenglund/colin/pull/12",
		"pmenglund/colin",
		"Open PRs",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("response body missing %q", want)
		}
	}
}

func TestStatusHandlerServesAPIV1State(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	svc := &Service{
		logger: logger,
		orch:   orch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if !strings.Contains(rec.Body.String(), "\"generated_at\"") {
		t.Fatalf("response missing generated_at: %q", rec.Body.String())
	}
}

func TestStatusHandlerReturnsNotFoundForUnknownPath(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	svc := &Service{
		logger: logger,
		orch:   orch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestStatusHandlerRejectsUnsupportedMethodForStatusAPI(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	svc := &Service{
		logger: logger,
		orch:   orch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodPost, "/api/status", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want %q", got, http.MethodGet)
	}
}

func TestStatusHandlerRejectsUnsupportedMethodForStateAPI(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	svc := &Service{
		logger: logger,
		orch:   orch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/state", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want %q", got, http.MethodGet)
	}
}

func TestStatusHandlerRejectsUnsupportedMethodForStatusPage(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	svc := &Service{
		logger: logger,
		orch:   orch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want %q", got, http.MethodGet)
	}
}

func TestStatusHandlerReturnsNotFoundForUnknownPathOnUnsupportedMethod(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	svc := &Service{
		logger: logger,
		orch:   orch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	req := httptest.NewRequest(http.MethodPost, "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	svc.statusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestStartStatusServerServesStatusAPI(t *testing.T) {
	handler := &captureLogHandler{}
	logger := slog.New(handler)
	orch := orchestrator.New(orchestrator.Runtime{}, logger)
	port := 0
	svc := &Service{
		logger: logger,
		orch:   orch,
		runtime: orchestrator.Runtime{
			Config: domain.ServiceConfig{
				Server: domain.ServerConfig{Port: &port},
				Tracker: domain.TrackerConfig{
					ActiveStates: []string{"Todo", "In Progress"},
				},
			},
			Tracker: &fakeTrackerClient{
				issues: []domain.Issue{{
					ID:         "issue-1",
					Identifier: "COLIN-82",
					Title:      "Needs review",
					State:      "Review",
					PullRequests: []domain.PullRequest{{
						URL:    "https://github.com/pmenglund/colin/pull/12",
						Title:  "COLIN-82: add PR info",
						Status: "open",
					}},
				}},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = orch.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	if err := svc.startStatusServer(ctx); err != nil {
		t.Fatalf("startStatusServer() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		address, ok := handler.value("status server started", "address")
		if !ok || strings.TrimSpace(address) == "" {
			time.Sleep(25 * time.Millisecond)
			continue
		}

		statusURL := fmt.Sprintf("http://%s/api/status", address)
		resp, err := http.Get(statusURL)
		if err != nil {
			time.Sleep(25 * time.Millisecond)
			continue
		}

		var page statusPageData
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			_ = resp.Body.Close()
			t.Fatalf("json.NewDecoder() error = %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		if page.PendingPRCount != 1 {
			t.Fatalf("page.PendingPRCount = %d, want 1", page.PendingPRCount)
		}
		if len(page.Issues) != 1 {
			t.Fatalf("len(page.Issues) = %d, want 1", len(page.Issues))
		}
		return
	}

	t.Fatal("status server did not report a reachable address before deadline")
}

func TestServiceRunReturnsStatusServerBindError(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime := orchestrator.Runtime{
		Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: time.Hour},
			Agent: domain.AgentConfig{
				MaxConcurrentAgents: 1,
			},
			Codex: domain.CodexConfig{
				Command: "codex app-server",
			},
			Server: domain.ServerConfig{Port: &port},
			Tracker: domain.TrackerConfig{
				Kind:         "linear",
				APIKey:       "test-key",
				ProjectSlug:  "test-project",
				ActiveStates: []string{"Todo", "In Progress"},
			},
		},
		Tracker: &fakeTrackerClient{},
	}
	svc := &Service{
		logger:  logger,
		orch:    orchestrator.New(runtime, logger),
		runtime: runtime,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = svc.Run(ctx)
	if err == nil {
		t.Fatal("Run() error = nil, want bind error")
	}
	if !strings.Contains(err.Error(), "start status server") {
		t.Fatalf("Run() error = %v, want status server context", err)
	}
}

func TestServiceRunStartsStatusServerAndStopsOnCancel(t *testing.T) {
	t.Parallel()

	handler := &captureLogHandler{}
	logger := slog.New(handler)
	port := 0
	runtime := orchestrator.Runtime{
		Config: domain.ServiceConfig{
			Polling: domain.PollingConfig{Interval: time.Hour},
			Agent: domain.AgentConfig{
				MaxConcurrentAgents: 1,
			},
			Codex: domain.CodexConfig{
				Command: "codex app-server",
			},
			Server: domain.ServerConfig{Port: &port},
			Tracker: domain.TrackerConfig{
				Kind:           "linear",
				APIKey:         "test-key",
				ProjectSlug:    "test-project",
				ActiveStates:   []string{"Todo", "In Progress"},
				TerminalStates: nil,
			},
		},
		Tracker: &fakeTrackerClient{
			issues: []domain.Issue{{
				ID:         "issue-1",
				Identifier: "COLIN-94",
				Title:      "Add basic GitHub PR info",
				State:      "Review",
				PullRequests: []domain.PullRequest{{
					URL:    "https://github.com/pmenglund/colin/pull/12",
					Title:  "COLIN-94: add basic GitHub PR info",
					Status: "open",
				}},
			}},
		},
	}
	workflowPath := t.TempDir() + "/WORKFLOW.md"
	svc := &Service{
		logger:       logger,
		workflowPath: workflowPath,
		orch:         orchestrator.New(runtime, logger),
		runtime:      runtime,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	var address string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		value, ok := handler.value("status server started", "address")
		if !ok || strings.TrimSpace(value) == "" {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		address = value
		break
	}
	if address == "" {
		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("Run() error after startup timeout = %v", err)
		}
		t.Fatal("status server did not start before deadline")
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/api/status", address))
	if err != nil {
		cancel()
		if runErr := <-errCh; runErr != nil {
			t.Fatalf("Run() error after GET failure = %v", runErr)
		}
		t.Fatalf("http.Get() error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		if runErr := <-errCh; runErr != nil {
			t.Fatalf("Run() error after bad status = %v", runErr)
		}
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

type captureLogHandler struct {
	mu      sync.Mutex
	records []capturedLogRecord
}

type capturedLogRecord struct {
	message string
	attrs   map[string]string
}

func (h *captureLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureLogHandler) Handle(_ context.Context, record slog.Record) error {
	entry := capturedLogRecord{
		message: record.Message,
		attrs:   map[string]string{},
	}
	record.Attrs(func(attr slog.Attr) bool {
		entry.attrs[attr.Key] = attr.Value.String()
		return true
	})

	h.mu.Lock()
	h.records = append(h.records, entry)
	h.mu.Unlock()
	return nil
}

func (h *captureLogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureLogHandler) WithGroup(string) slog.Handler {
	return h
}

func (h *captureLogHandler) value(message string, key string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := len(h.records) - 1; i >= 0; i-- {
		record := h.records[i]
		if record.message != message {
			continue
		}
		value, ok := record.attrs[key]
		if ok {
			return value, true
		}
	}
	return "", false
}
