package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/repoops/fakes"
	"github.com/pmenglund/colin/internal/workspace"
)

type collaboratorGitHubClient struct {
	*fakes.FakeRepoHostClient
	allowed bool
}

func (c *collaboratorGitHubClient) IsCollaborator(context.Context, string, string, string) (bool, error) {
	return c.allowed, nil
}

func newReviewFollowUpTestOrchestrator(t *testing.T, threads []repoops.GitHubReviewThread) (*trackerStub, *Orchestrator) {
	return newReviewFollowUpTestOrchestratorWithCollaborator(t, threads, true)
}

func newReviewFollowUpTestOrchestratorWithCollaborator(t *testing.T, threads []repoops.GitHubReviewThread, allowed bool) (*trackerStub, *Orchestrator) {
	t.Helper()

	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Repo.PublishStates = []string{"Review"}

	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://github.com/pmenglund/colin/pull/11",
		State:       "OPEN",
		HeadRefName: "colin-123",
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{Threads: threads}, nil)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, &collaboratorGitHubClient{FakeRepoHostClient: fakeGitHub, allowed: allowed}),
			Workspace: workspace.NewManager(cfg, logger),
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}
	return tracker, orch
}

func reviewFollowUpIssue() domain.Issue {
	return domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-123",
		Title:      "Address review feedback",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:     11,
			PullRequestURL:        "https://github.com/pmenglund/colin/pull/11",
			PullRequestHeadRef:    "colin-123",
			PullRequestBaseRef:    "symphony",
			ProgressRootCommentID: "root",
		},
	}
}

func reviewThreadWithComments(id string, comments ...repoops.GitHubReviewComment) repoops.GitHubReviewThread {
	return repoops.GitHubReviewThread{
		ID:               id,
		IsResolved:       false,
		IsOutdated:       false,
		ViewerCanReply:   true,
		ViewerCanResolve: true,
		Path:             "internal/foo.go",
		Comments: repoops.GitHubReviewCommentConnection{
			Comments: comments,
		},
	}
}

func TestSyncGitHubReviewFollowUpStoresReactionTargetWithoutMovingIssue(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Repo.PublishStates = []string{"Review"}

	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://github.com/pmenglund/colin/pull/11",
		State:       "OPEN",
		HeadRefName: "colin-123",
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			{
				ID:               "thread-1",
				IsResolved:       false,
				IsOutdated:       false,
				ViewerCanReply:   true,
				ViewerCanResolve: true,
				Path:             "internal/foo.go",
				Comments: repoops.GitHubReviewCommentConnection{
					Comments: []repoops.GitHubReviewComment{
						{
							ID:          "PRRC_kwDOExample0",
							DatabaseID:  "3035904923",
							Body:        "**review**\n\nUseful? React with 👍 / 👎.",
							URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904923",
							AuthorLogin: "chatgpt-codex-connector[bot]",
						},
					},
				},
			},
		},
	}, nil)
	fakeGitHub.PullRequestReviewCommentReactionsReturns(repoops.GitHubReviewCommentReactionPage{
		Reactions: []repoops.GitHubReaction{
			{ID: 377554834, Content: "+1", UserLogin: "pmenglund"},
		},
	}, nil)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, &collaboratorGitHubClient{FakeRepoHostClient: fakeGitHub, allowed: true}),
			Workspace: workspace.NewManager(cfg, logger),
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{"issue-1": "Review"},
		issueStates: map[string]int{
			"Review": 1,
			"Todo":   0,
		},
		stateIssues: map[string][]domain.StateIssueSummary{
			"Review": {
				{ID: "issue-1", Identifier: "COLIN-123", Title: "Address review feedback"},
			},
		},
	}

	now := time.Date(2026, time.April, 4, 19, 18, 0, 0, time.UTC)
	updated, queued := orch.syncGitHubReviewFollowUp(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-123",
		Title:      "Address review feedback",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:     11,
			PullRequestURL:        "https://github.com/pmenglund/colin/pull/11",
			PullRequestHeadRef:    "colin-123",
			PullRequestBaseRef:    "symphony",
			ProgressRootCommentID: "root",
		},
	}, now)

	if queued {
		t.Fatal("syncGitHubReviewFollowUp() queued = true, want false")
	}
	if updated.State != "Review" {
		t.Fatalf("updated.State = %q, want Review", updated.State)
	}
	if got := len(tracker.updatedStates); got != 0 {
		t.Fatalf("updatedStates length = %d, want 0", got)
	}
	if tracker.metadata.PendingReviewThreadID != "thread-1" {
		t.Fatalf("PendingReviewThreadID = %q, want thread-1", tracker.metadata.PendingReviewThreadID)
	}
	if tracker.metadata.PendingReviewCommentID != "3035904923" {
		t.Fatalf("PendingReviewCommentID = %q, want 3035904923", tracker.metadata.PendingReviewCommentID)
	}
	if tracker.metadata.PendingReviewReactionID != "377554834" {
		t.Fatalf("PendingReviewReactionID = %q, want 377554834", tracker.metadata.PendingReviewReactionID)
	}
	if tracker.metadata.PendingReviewReactor != "pmenglund" {
		t.Fatalf("PendingReviewReactor = %q, want pmenglund", tracker.metadata.PendingReviewReactor)
	}
	if len(tracker.metadata.QueuedReviewFollowUps) != 0 {
		t.Fatalf("QueuedReviewFollowUps = %#v, want empty queue", tracker.metadata.QueuedReviewFollowUps)
	}
	if got := tracker.metadata.ReviewReactionWatermarks["3035904923"]; got != "377554834" {
		t.Fatalf("ReviewReactionWatermarks = %#v, want comment watermark", tracker.metadata.ReviewReactionWatermarks)
	}
	if len(tracker.commentReplies) != 0 {
		t.Fatalf("commentReplies = %#v, want no status comments", tracker.commentReplies)
	}
	if got := orch.completed["issue-1"]; got != "Review" {
		t.Fatalf("completed[issue-1] = %q, want Review", got)
	}
	if got := orch.issueStates["Review"]; got != 1 {
		t.Fatalf("issueStates[Review] = %d, want 1", got)
	}
	if got := orch.issueStates["Todo"]; got != 0 {
		t.Fatalf("issueStates[Todo] = %d, want 0", got)
	}
	if got := len(orch.stateIssues["Review"]); got != 1 {
		t.Fatalf("len(stateIssues[Review]) = %d, want 1", got)
	}
	if got := len(orch.stateIssues["Todo"]); got != 0 {
		t.Fatalf("len(stateIssues[Todo]) = %d, want 0", got)
	}
}

func TestSyncGitHubReviewFollowUpMovesIssueForHumanReviewComment(t *testing.T) {
	tracker, orch := newReviewFollowUpTestOrchestrator(t, []repoops.GitHubReviewThread{
		reviewThreadWithComments("thread-1", repoops.GitHubReviewComment{
			ID:          "PRRC_kwDOHumanFeedback",
			DatabaseID:  "3035904923",
			Body:        "Please rename this helper.",
			URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904923",
			AuthorLogin: "pmenglund",
		}),
	})

	updated, queued := orch.syncGitHubReviewFollowUp(context.Background(), reviewFollowUpIssue(), time.Date(2026, time.April, 4, 19, 18, 0, 0, time.UTC))

	if !queued {
		t.Fatal("syncGitHubReviewFollowUp() queued = false, want true")
	}
	if updated.State != "Todo" {
		t.Fatalf("updated.State = %q, want Todo", updated.State)
	}
	if got := tracker.updatedStates; len(got) != 1 || got[0] != "issue-1:Todo" {
		t.Fatalf("updatedStates = %#v, want issue-1 moved to Todo", got)
	}
	if tracker.metadata.PendingReviewThreadID != "thread-1" {
		t.Fatalf("PendingReviewThreadID = %q, want thread-1", tracker.metadata.PendingReviewThreadID)
	}
	if tracker.metadata.PendingReviewCommentID != "3035904923" {
		t.Fatalf("PendingReviewCommentID = %q, want 3035904923", tracker.metadata.PendingReviewCommentID)
	}
	if tracker.metadata.PendingReviewReactionID != "" {
		t.Fatalf("PendingReviewReactionID = %q, want empty for human review comment", tracker.metadata.PendingReviewReactionID)
	}
	if tracker.metadata.PendingReviewReactor != "pmenglund" {
		t.Fatalf("PendingReviewReactor = %q, want pmenglund", tracker.metadata.PendingReviewReactor)
	}
	if len(tracker.metadata.ReviewReactionWatermarks) != 0 {
		t.Fatalf("ReviewReactionWatermarks = %#v, want no reaction watermark", tracker.metadata.ReviewReactionWatermarks)
	}
	if len(tracker.commentReplies) != 1 {
		t.Fatalf("commentReplies = %#v, want one status reply", tracker.commentReplies)
	}
	if !strings.Contains(tracker.commentReplies[0], "left unresolved PR feedback") {
		t.Fatalf("comment reply = %q, want PR-feedback status", tracker.commentReplies[0])
	}
}

func TestSyncGitHubReviewFollowUpIgnoresNonCollaboratorHumanReviewComment(t *testing.T) {
	tracker, orch := newReviewFollowUpTestOrchestratorWithCollaborator(t, []repoops.GitHubReviewThread{
		reviewThreadWithComments("thread-1", repoops.GitHubReviewComment{
			ID:          "PRRC_kwDOHumanFeedback",
			DatabaseID:  "3035904923",
			Body:        "Please rename this helper.",
			URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904923",
			AuthorLogin: "external-user",
		}),
	}, false)

	updated, queued := orch.syncGitHubReviewFollowUp(context.Background(), reviewFollowUpIssue(), time.Date(2026, time.April, 4, 19, 18, 0, 0, time.UTC))

	if queued {
		t.Fatal("syncGitHubReviewFollowUp() queued = true, want false")
	}
	if updated.State != "Review" {
		t.Fatalf("updated.State = %q, want Review", updated.State)
	}
	if len(tracker.updatedStates) != 0 {
		t.Fatalf("updatedStates = %#v, want no state changes", tracker.updatedStates)
	}
	if tracker.metadata.PendingReviewThreadID != "" {
		t.Fatalf("PendingReviewThreadID = %q, want empty", tracker.metadata.PendingReviewThreadID)
	}
	if len(tracker.metadata.QueuedReviewFollowUps) != 0 {
		t.Fatalf("QueuedReviewFollowUps = %#v, want empty queue", tracker.metadata.QueuedReviewFollowUps)
	}
	if len(tracker.commentReplies) != 0 {
		t.Fatalf("commentReplies = %#v, want no status comments", tracker.commentReplies)
	}
}

func TestSyncGitHubReviewFollowUpDoesNotAutoStartPureCodexReviewThread(t *testing.T) {
	tracker, orch := newReviewFollowUpTestOrchestrator(t, []repoops.GitHubReviewThread{
		reviewThreadWithComments("thread-1", repoops.GitHubReviewComment{
			ID:          "PRRC_kwDOCodexFeedback",
			DatabaseID:  "3035904923",
			Body:        "**review**\n\nUseful? React with 👍 / 👎.",
			URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904923",
			AuthorLogin: "chatgpt-codex-connector[bot]",
		}),
	})

	updated, queued := orch.syncGitHubReviewFollowUp(context.Background(), reviewFollowUpIssue(), time.Date(2026, time.April, 4, 19, 18, 0, 0, time.UTC))

	if queued {
		t.Fatal("syncGitHubReviewFollowUp() queued = true, want false")
	}
	if updated.State != "Review" {
		t.Fatalf("updated.State = %q, want Review", updated.State)
	}
	if len(tracker.updatedStates) != 0 {
		t.Fatalf("updatedStates = %#v, want no state changes", tracker.updatedStates)
	}
	if tracker.metadata.PendingReviewThreadID != "" {
		t.Fatalf("PendingReviewThreadID = %q, want empty", tracker.metadata.PendingReviewThreadID)
	}
	if len(tracker.commentReplies) != 0 {
		t.Fatalf("commentReplies = %#v, want no status comments", tracker.commentReplies)
	}
}

func TestSyncGitHubReviewFollowUpTreatsHumanReplyAsFeedback(t *testing.T) {
	tracker, orch := newReviewFollowUpTestOrchestrator(t, []repoops.GitHubReviewThread{
		reviewThreadWithComments(
			"thread-1",
			repoops.GitHubReviewComment{
				ID:          "PRRC_kwDOCodexFeedback",
				DatabaseID:  "3035904923",
				Body:        "**review**\n\nUseful? React with 👍 / 👎.",
				URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904923",
				AuthorLogin: "chatgpt-codex-connector[bot]",
			},
			repoops.GitHubReviewComment{
				ID:          "PRRC_kwDOHumanReply",
				DatabaseID:  "3035904999",
				Body:        "Yes, please do the rename instead.",
				URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904999",
				AuthorLogin: "pmenglund",
			},
		),
	})

	_, queued := orch.syncGitHubReviewFollowUp(context.Background(), reviewFollowUpIssue(), time.Date(2026, time.April, 4, 19, 18, 0, 0, time.UTC))

	if !queued {
		t.Fatal("syncGitHubReviewFollowUp() queued = false, want true")
	}
	if tracker.metadata.PendingReviewThreadID != "thread-1" {
		t.Fatalf("PendingReviewThreadID = %q, want thread-1", tracker.metadata.PendingReviewThreadID)
	}
	if tracker.metadata.PendingReviewCommentID != "3035904999" {
		t.Fatalf("PendingReviewCommentID = %q, want latest human reply", tracker.metadata.PendingReviewCommentID)
	}
	if tracker.metadata.PendingReviewReactionID != "" {
		t.Fatalf("PendingReviewReactionID = %q, want empty", tracker.metadata.PendingReviewReactionID)
	}
	if tracker.metadata.PendingReviewReactor != "pmenglund" {
		t.Fatalf("PendingReviewReactor = %q, want pmenglund", tracker.metadata.PendingReviewReactor)
	}
}

func TestSyncGitHubReviewFollowUpRequiresDelegationInAppMode(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	cfg.Tracker.AppMode = true
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Repo.PublishStates = []string{"Review"}

	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://github.com/pmenglund/colin/pull/11",
		State:       "OPEN",
		HeadRefName: "colin-123",
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			{
				ID:               "thread-1",
				IsResolved:       false,
				IsOutdated:       false,
				ViewerCanReply:   true,
				ViewerCanResolve: true,
				Path:             "internal/foo.go",
				Comments: repoops.GitHubReviewCommentConnection{
					Comments: []repoops.GitHubReviewComment{
						{
							ID:          "PRRC_kwDOExample0",
							DatabaseID:  "3035904923",
							Body:        "**review**\n\nUseful? React with 👍 / 👎.",
							URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904923",
							AuthorLogin: "chatgpt-codex-connector[bot]",
						},
					},
				},
			},
		},
	}, nil)
	fakeGitHub.PullRequestReviewCommentReactionsReturns(repoops.GitHubReviewCommentReactionPage{
		Reactions: []repoops.GitHubReaction{
			{ID: 377554834, Content: "+1", UserLogin: "pmenglund"},
		},
	}, nil)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, &collaboratorGitHubClient{FakeRepoHostClient: fakeGitHub, allowed: true}),
			Workspace: workspace.NewManager(cfg, logger),
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{"issue-1": "Review"},
	}

	now := time.Date(2026, time.April, 4, 19, 18, 0, 0, time.UTC)
	updated, queued := orch.syncGitHubReviewFollowUp(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-123",
		Title:      "Address review feedback",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:     11,
			PullRequestURL:        "https://github.com/pmenglund/colin/pull/11",
			PullRequestHeadRef:    "colin-123",
			PullRequestBaseRef:    "symphony",
			ProgressRootCommentID: "root",
		},
	}, now)

	if queued {
		t.Fatal("syncGitHubReviewFollowUp() queued = true, want false while issue is not delegated")
	}
	if updated.State != "Review" {
		t.Fatalf("updated.State = %q, want Review", updated.State)
	}
	if got := len(tracker.updatedStates); got != 0 {
		t.Fatalf("updatedStates length = %d, want 0", got)
	}
}

func TestSyncGitHubReviewFollowUpQueuesAdditionalApprovals(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Repo.PublishStates = []string{"Review"}

	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://github.com/pmenglund/colin/pull/11",
		State:       "OPEN",
		HeadRefName: "colin-123",
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			{
				ID:               "thread-1",
				IsResolved:       false,
				IsOutdated:       false,
				ViewerCanReply:   true,
				ViewerCanResolve: true,
				Path:             "internal/foo.go",
				Comments: repoops.GitHubReviewCommentConnection{
					Comments: []repoops.GitHubReviewComment{
						{
							ID:          "PRRC_kwDOExample1",
							DatabaseID:  "3035904923",
							Body:        "**review one**\n\nUseful? React with 👍 / 👎.",
							URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904923",
							AuthorLogin: "chatgpt-codex-connector[bot]",
						},
					},
				},
			},
			{
				ID:               "thread-2",
				IsResolved:       false,
				IsOutdated:       false,
				ViewerCanReply:   true,
				ViewerCanResolve: true,
				Path:             "internal/bar.go",
				Comments: repoops.GitHubReviewCommentConnection{
					Comments: []repoops.GitHubReviewComment{
						{
							ID:          "PRRC_kwDOExample2",
							DatabaseID:  "3035904999",
							Body:        "**review two**\n\nUseful? React with 👍 / 👎.",
							URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904999",
							AuthorLogin: "chatgpt-codex-connector[bot]",
						},
					},
				},
			},
		},
	}, nil)
	fakeGitHub.PullRequestReviewCommentReactionsCalls(func(_ context.Context, _ string, _ string, commentID int64, _ int) (repoops.GitHubReviewCommentReactionPage, error) {
		switch commentID {
		case 3035904923:
			return repoops.GitHubReviewCommentReactionPage{
				Reactions: []repoops.GitHubReaction{{ID: 100, Content: "+1", UserLogin: "pmenglund"}},
			}, nil
		case 3035904999:
			return repoops.GitHubReviewCommentReactionPage{
				Reactions: []repoops.GitHubReaction{{ID: 200, Content: "+1", UserLogin: "pmenglund"}},
			}, nil
		default:
			return repoops.GitHubReviewCommentReactionPage{}, nil
		}
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, &collaboratorGitHubClient{FakeRepoHostClient: fakeGitHub, allowed: true}),
			Workspace: workspace.NewManager(cfg, logger),
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	_, queued := orch.syncGitHubReviewFollowUp(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-123",
		Title:      "Address review feedback",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:  11,
			PullRequestURL:     "https://github.com/pmenglund/colin/pull/11",
			PullRequestHeadRef: "colin-123",
			PullRequestBaseRef: "symphony",
		},
	}, time.Date(2026, time.April, 4, 19, 19, 0, 0, time.UTC))

	if queued {
		t.Fatal("syncGitHubReviewFollowUp() queued = true, want false")
	}
	if got := len(tracker.updatedStates); got != 0 {
		t.Fatalf("updatedStates length = %d, want 0", got)
	}
	if tracker.metadata.PendingReviewThreadID != "thread-1" || tracker.metadata.PendingReviewCommentID != "3035904923" {
		t.Fatalf("pending follow-up = %#v, want first approval promoted", tracker.metadata)
	}
	if len(tracker.metadata.QueuedReviewFollowUps) != 1 {
		t.Fatalf("QueuedReviewFollowUps = %#v, want one queued follow-up", tracker.metadata.QueuedReviewFollowUps)
	}
	if queuedItem := tracker.metadata.QueuedReviewFollowUps[0]; queuedItem.ThreadID != "thread-2" || queuedItem.CommentID != "3035904999" || queuedItem.ReactionID != "200" {
		t.Fatalf("queued follow-up = %#v, want second approval queued", queuedItem)
	}
	if got := tracker.metadata.ReviewReactionWatermarks["3035904923"]; got != "100" {
		t.Fatalf("ReviewReactionWatermarks = %#v, want first watermark", tracker.metadata.ReviewReactionWatermarks)
	}
	if got := tracker.metadata.ReviewReactionWatermarks["3035904999"]; got != "200" {
		t.Fatalf("ReviewReactionWatermarks = %#v, want second watermark", tracker.metadata.ReviewReactionWatermarks)
	}
}

func TestSyncGitHubReviewFollowUpIgnoresAlreadySeenReaction(t *testing.T) {
	cfg, fakeGitHub := setupReviewSyncTestRuntime(t)
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Repo.PublishStates = []string{"Review"}

	fakeGitHub.PullRequestByNumberReturns(&repoops.GitHubPullRequest{
		Number:      11,
		URL:         "https://github.com/pmenglund/colin/pull/11",
		State:       "OPEN",
		HeadRefName: "colin-123",
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{
		Threads: []repoops.GitHubReviewThread{
			{
				ID:               "thread-1",
				IsResolved:       false,
				IsOutdated:       false,
				ViewerCanReply:   true,
				ViewerCanResolve: true,
				Path:             "internal/foo.go",
				Comments: repoops.GitHubReviewCommentConnection{
					Comments: []repoops.GitHubReviewComment{
						{
							ID:          "PRRC_kwDOExample3",
							DatabaseID:  "3035904923",
							Body:        "**review**\n\nUseful? React with 👍 / 👎.",
							URL:         "https://github.com/pmenglund/colin/pull/11#discussion_r3035904923",
							AuthorLogin: "chatgpt-codex-connector[bot]",
						},
					},
				},
			},
		},
	}, nil)
	fakeGitHub.PullRequestReviewCommentReactionsReturns(repoops.GitHubReviewCommentReactionPage{
		Reactions: []repoops.GitHubReaction{
			{ID: 377554834, Content: "+1", UserLogin: "pmenglund"},
		},
	}, nil)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := &trackerStub{}
	orch := &Orchestrator{
		logger: logger,
		runtime: Runtime{
			Config:    cfg,
			Tracker:   tracker,
			Repo:      repoops.NewManagerWithRepoHostClient(cfg, logger, &collaboratorGitHubClient{FakeRepoHostClient: fakeGitHub, allowed: true}),
			Workspace: workspace.NewManager(cfg, logger),
		},
		running:   map[string]*runningEntry{},
		claimed:   map[string]struct{}{},
		retrying:  map[string]*retryState{},
		completed: map[string]string{},
	}

	updated, queued := orch.syncGitHubReviewFollowUp(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-123",
		Title:      "Address review feedback",
		State:      "Review",
		ColinMetadata: &domain.ColinMetadata{
			PullRequestNumber:        11,
			PullRequestURL:           "https://github.com/pmenglund/colin/pull/11",
			PullRequestHeadRef:       "colin-123",
			PullRequestBaseRef:       "symphony",
			ReviewReactionWatermarks: map[string]string{"3035904923": "377554834"},
		},
	}, time.Date(2026, time.April, 4, 19, 20, 0, 0, time.UTC))

	if queued {
		t.Fatal("syncGitHubReviewFollowUp() queued = true, want false")
	}
	if updated.State != "Review" {
		t.Fatalf("updated.State = %q, want Review", updated.State)
	}
	if len(tracker.updatedStates) != 0 {
		t.Fatalf("updatedStates = %#v, want no state changes", tracker.updatedStates)
	}
	if len(tracker.commentReplies) != 0 {
		t.Fatalf("commentReplies = %#v, want no status comments", tracker.commentReplies)
	}
}
