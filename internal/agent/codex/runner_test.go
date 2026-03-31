package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/repoops/fakes"
	"github.com/pmenglund/colin/internal/workspace"
)

func TestRunnerMovesSuccessfulActiveIssueToPublishState(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	repoURL := createRunnerGitOrigin(t, tempDir)
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 %q -test.run=TestHelperProcessFakeCodex --",
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo"},
		},
		Workspace: domain.WorkspaceConfig{
			Root:    filepath.Join(tempDir, "workspaces"),
			RepoURL: repoURL,
			BaseRef: "symphony",
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			RemoteName:    "origin",
		},
		Hooks: domain.HookConfig{
			BeforeRun: "printf 'hello\\n' > feature.txt",
		},
		Agent: domain.AgentConfig{
			MaxTurns: 1,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-94",
			Title:      "Move issue to review",
			State:      "Todo",
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-94",
		Title:      "Move issue to review",
		State:      "Todo",
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if result.Issue.State != "Review" {
		t.Fatalf("result state = %q, want %q", result.Issue.State, "Review")
	}
	if tracker.updatedIssueID != "issue-1" {
		t.Fatalf("updated issue id = %q, want %q", tracker.updatedIssueID, "issue-1")
	}
	if tracker.updatedState != "Review" {
		t.Fatalf("updated state = %q, want %q", tracker.updatedState, "Review")
	}
}

func TestRunnerKeepsCodingWhenReadyForReviewHasNoRepoChanges(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	repoURL := createRunnerGitOrigin(t, tempDir)
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 %q -test.run=TestHelperProcessFakeCodex --",
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo", "In Progress"},
		},
		Workspace: domain.WorkspaceConfig{
			Root:    filepath.Join(tempDir, "workspaces"),
			RepoURL: repoURL,
			BaseRef: "symphony",
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			RemoteName:    "origin",
		},
		Agent: domain.AgentConfig{
			MaxTurns: 1,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-95",
			Title:      "Keep coding",
			State:      "In Progress",
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-95",
		Title:      "Keep coding",
		State:      "Todo",
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if result.Issue.State != "Refine" {
		t.Fatalf("Issue.State = %q, want %q", result.Issue.State, "Refine")
	}
	if strings.Contains(result.Summary, "Implemented the requested change.") {
		t.Fatalf("Summary = %q, want ready-for-review text cleared after no-change handoff", result.Summary)
	}
	if !strings.Contains(result.Summary, "Colin reached the maximum of `1` turns") {
		t.Fatalf("Summary = %q, want max-turn handoff note", result.Summary)
	}
}

func TestClearManagedCodexReviewLabelsBestEffortRemovesOnlyManagedReviewLabels(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue := runner.clearManagedCodexReviewLabelsBestEffort(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-128",
		Labels: []string{
			"e2e",
			domain.CodexReviewPendingLabel,
			domain.CodexReviewApprovedLabel,
		},
	})

	if got, want := strings.Join(tracker.removedLabels, ","), "issue-1:"+domain.CodexReviewPendingLabel+",issue-1:"+domain.CodexReviewApprovedLabel; got != want {
		t.Fatalf("removedLabels = %q, want %q", got, want)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "e2e" {
		t.Fatalf("remaining labels = %v, want [e2e]", issue.Labels)
	}
}

func TestApplyPostMergeStateUsesConfiguredGitAutomationTarget(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{
		resolvedMergeState:  "Merged",
		resolveMergeStateOK: true,
	}
	runner := &Runner{
		cfg:     domain.ServiceConfig{},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue := domain.Issue{ID: "issue-1", Identifier: "COLIN-94", State: "Merge"}
	updated := runner.applyPostMergeState(context.Background(), issue, "main")

	if updated.State != "Merged" {
		t.Fatalf("updated state = %q, want %q", updated.State, "Merged")
	}
	if tracker.updatedIssueID != "issue-1" {
		t.Fatalf("updated issue id = %q, want %q", tracker.updatedIssueID, "issue-1")
	}
	if tracker.updatedState != "Merged" {
		t.Fatalf("updated state = %q, want %q", tracker.updatedState, "Merged")
	}
}

func TestApplyPostMergeStateSkipsWhenNoAutomationTargetExists(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		cfg:     domain.ServiceConfig{},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue := domain.Issue{ID: "issue-1", Identifier: "COLIN-94", State: "Merge"}
	updated := runner.applyPostMergeState(context.Background(), issue, "main")

	if updated.State != "Merge" {
		t.Fatalf("updated state = %q, want %q", updated.State, "Merge")
	}
	if tracker.updatedIssueID != "" {
		t.Fatalf("updated issue id = %q, want empty", tracker.updatedIssueID)
	}
}

func TestBlockMergeForCodexReviewKeepsIssueInMergeWhenApprovalPending(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.March, 28, 18, 1, 0, 0, time.UTC)
	tracker := &stubTracker{}
	runner := &Runner{
		cfg: domain.ServiceConfig{
			Repo: domain.RepoConfig{
				PublishStates:         []string{"Review"},
				CodexPRReviewsEnabled: true,
			},
		},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue, summary, blocked, err := runner.blockMergeForCodexReview(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-94",
		State:      "Merge",
	}, repoops.ReviewContext{
		PullRequest:            domain.PullRequestRef{Number: 1, URL: "https://example.test/pr/1", State: "OPEN"},
		CodexReviewRequestedAt: &requestedAt,
	})
	if err != nil {
		t.Fatalf("blockMergeForCodexReview() error = %v", err)
	}
	if !blocked {
		t.Fatal("blocked = false, want true")
	}
	if issue.State != "Merge" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "Merge")
	}
	if tracker.updatedState != "" {
		t.Fatalf("updated state = %q, want empty", tracker.updatedState)
	}
	if !strings.Contains(summary, "thumbs up") {
		t.Fatalf("summary = %q, want thumbs up blocker", summary)
	}
	if !strings.Contains(summary, "Keeping issue in `Merge`") {
		t.Fatalf("summary = %q, want keep in merge message", summary)
	}
}

func TestBlockMergeForCodexReviewKeepsIssueInMergeWhileWaitingForPickup(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		cfg: domain.ServiceConfig{
			Repo: domain.RepoConfig{
				PublishStates:         []string{"Review"},
				CodexPRReviewsEnabled: true,
			},
		},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue, summary, blocked, err := runner.blockMergeForCodexReview(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-134",
		State:      "Merge",
	}, repoops.ReviewContext{
		PullRequest: domain.PullRequestRef{Number: 1, URL: "https://example.test/pr/1", State: "OPEN"},
	})
	if err != nil {
		t.Fatalf("blockMergeForCodexReview() error = %v", err)
	}
	if !blocked {
		t.Fatal("blocked = false, want true")
	}
	if issue.State != "Merge" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "Merge")
	}
	if tracker.updatedState != "" {
		t.Fatalf("updated state = %q, want empty", tracker.updatedState)
	}
	if !strings.Contains(summary, "waiting for Codex PR review to start") {
		t.Fatalf("summary = %q, want wait-for-pickup message", summary)
	}
	if !strings.Contains(summary, "eyes") {
		t.Fatalf("summary = %q, want eyes reaction guidance", summary)
	}
}

func TestBlockMergeForCodexReviewSkipsPickupWaitWhenDisabled(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		cfg: domain.ServiceConfig{
			Repo: domain.RepoConfig{PublishStates: []string{"Review"}},
		},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue, summary, blocked, err := runner.blockMergeForCodexReview(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-134",
		State:      "Merge",
	}, repoops.ReviewContext{
		PullRequest: domain.PullRequestRef{Number: 1, URL: "https://example.test/pr/1", State: "OPEN"},
	})
	if err != nil {
		t.Fatalf("blockMergeForCodexReview() error = %v", err)
	}
	if blocked {
		t.Fatal("blocked = true, want false")
	}
	if summary != "" {
		t.Fatalf("summary = %q, want empty", summary)
	}
	if issue.State != "Merge" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "Merge")
	}
}

func TestBlockMergeForCodexReviewIgnoresThreadsWhenDisabled(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		cfg: domain.ServiceConfig{
			Repo: domain.RepoConfig{PublishStates: []string{"Review"}},
		},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue, summary, blocked, err := runner.blockMergeForCodexReview(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-134",
		State:      "Merge",
	}, repoops.ReviewContext{
		PullRequest: domain.PullRequestRef{Number: 1, URL: "https://example.test/pr/1", State: "OPEN"},
		CodexReviewThreads: []domain.GitHubReviewThread{
			{ID: "thread-1", Path: "internal/foo.go", Body: "Please fix this."},
		},
	})
	if err != nil {
		t.Fatalf("blockMergeForCodexReview() error = %v", err)
	}
	if blocked {
		t.Fatal("blocked = true, want false")
	}
	if summary != "" {
		t.Fatalf("summary = %q, want empty", summary)
	}
	if issue.State != "Merge" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "Merge")
	}
	if len(issue.ReviewThreads) != 0 {
		t.Fatalf("issue.ReviewThreads length = %d, want 0", len(issue.ReviewThreads))
	}
}

func TestBlockMergeForCodexReviewReturnsIssueToReviewWhenThreadsRemain(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.March, 28, 18, 1, 0, 0, time.UTC)
	approvedAt := requestedAt.Add(time.Minute)
	tracker := &stubTracker{}
	runner := &Runner{
		cfg: domain.ServiceConfig{
			Repo: domain.RepoConfig{
				PublishStates:         []string{"Review"},
				CodexPRReviewsEnabled: true,
			},
		},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue, summary, blocked, err := runner.blockMergeForCodexReview(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-94",
		State:      "Merge",
	}, repoops.ReviewContext{
		PullRequest:            domain.PullRequestRef{Number: 1, URL: "https://example.test/pr/1", State: "OPEN"},
		CodexReviewRequestedAt: &requestedAt,
		CodexReviewApprovedAt:  &approvedAt,
		CodexReviewThreads: []domain.GitHubReviewThread{
			{ID: "thread-1", Path: "internal/foo.go", Body: "Please fix this."},
		},
	})
	if err != nil {
		t.Fatalf("blockMergeForCodexReview() error = %v", err)
	}
	if !blocked {
		t.Fatal("blocked = false, want true")
	}
	if issue.State != "Review" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "Review")
	}
	if len(issue.ReviewThreads) != 1 {
		t.Fatalf("issue.ReviewThreads length = %d, want 1", len(issue.ReviewThreads))
	}
	if !strings.Contains(summary, "Unresolved Codex review threads") {
		t.Fatalf("summary = %q, want unresolved thread blocker", summary)
	}
}

func TestParseCodingSummaryOutcomeNeedsSpec(t *testing.T) {
	t.Parallel()

	outcome, summary := parseCodingSummaryOutcome(outcomeNeedsSpec + "\n\nThe spec should be improved before implementation.")
	if outcome != outcomeNeedsSpec {
		t.Fatalf("outcome = %q, want %q", outcome, outcomeNeedsSpec)
	}
	if summary != "The spec should be improved before implementation." {
		t.Fatalf("summary = %q", summary)
	}
}

func TestParseCodingSummaryOutcomeDefaultsToReview(t *testing.T) {
	t.Parallel()

	outcome, summary := parseCodingSummaryOutcome("Implemented the requested change.")
	if outcome != outcomeReadyForReview {
		t.Fatalf("outcome = %q, want %q", outcome, outcomeReadyForReview)
	}
	if summary != "Implemented the requested change." {
		t.Fatalf("summary = %q", summary)
	}
}

func TestCodingOutcomeUsesNeedsSpecDirective(t *testing.T) {
	t.Parallel()

	if got := codingOutcome(outcomeNeedsSpec, false); got != metadataOutcomeSpec {
		t.Fatalf("codingOutcome() = %q, want %q", got, metadataOutcomeSpec)
	}
}

func TestCodingHandoffStateUsesRefineForNeedsSpec(t *testing.T) {
	t.Parallel()

	if got := codingHandoffState(outcomeNeedsSpec, false, []string{"Review"}); got != refineStateName {
		t.Fatalf("codingHandoffState() = %q, want %q", got, refineStateName)
	}
}

func TestCodingHandoffStateUsesRefineForMaxTurns(t *testing.T) {
	t.Parallel()

	if got := codingHandoffState(outcomeReadyForReview, true, []string{"Review"}); got != refineStateName {
		t.Fatalf("codingHandoffState() = %q, want %q", got, refineStateName)
	}
}

func TestMoveSuccessfulActiveIssueToHandoffStateMovesToRefine(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		cfg: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo"}},
			Repo:    domain.RepoConfig{PublishStates: []string{"Review"}},
		},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue, err := runner.moveSuccessfulCodingRunToHandoffState(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-94",
		State:      "Todo",
	}, refineStateName)
	if err != nil {
		t.Fatalf("moveSuccessfulCodingRunToHandoffState() error = %v", err)
	}
	if issue.State != refineStateName {
		t.Fatalf("issue.State = %q, want %q", issue.State, refineStateName)
	}
	if tracker.updatedState != refineStateName {
		t.Fatalf("updated state = %q, want %q", tracker.updatedState, refineStateName)
	}
}

func TestRunnerMovesTodoIssueIntoInProgressBeforeCoding(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		cfg: domain.ServiceConfig{
			Tracker: domain.TrackerConfig{ActiveStates: []string{"Todo", "In Progress"}},
		},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue, err := runner.moveActiveIssueToWorkingState(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-94",
		State:      "Todo",
	})
	if err != nil {
		t.Fatalf("moveActiveIssueToWorkingState() error = %v", err)
	}
	if issue.State != "In Progress" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "In Progress")
	}
	if len(tracker.updatedStates) != 1 {
		t.Fatalf("updatedStates length = %d, want 1", len(tracker.updatedStates))
	}
	if tracker.updatedStates[0] != "In Progress" {
		t.Fatalf("updatedStates[0] = %q, want %q", tracker.updatedStates[0], "In Progress")
	}
}

func TestAppendMaxTurnsSummary(t *testing.T) {
	t.Parallel()

	got := appendMaxTurnsSummary("Implemented the change.", "In Progress", 6)
	want := "Implemented the change.\n\nColin reached the maximum of `6` turns while the issue remained in `In Progress`, so it is handing off for human refinement before more implementation work."
	if got != want {
		t.Fatalf("appendMaxTurnsSummary() = %q, want %q", got, want)
	}
}

func TestPersistActualBranchNameValueBestEffortPreservesExistingMetadata(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	issue := domain.Issue{
		ID: "issue-1",
		ColinMetadata: &domain.ColinMetadata{
			ReviewPublishDirective: domain.ReviewPublishDirectiveSkip,
			LastRunType:            "coding",
			LastOutcome:            "ready_for_review",
			LastSummaryCommentID:   "comment-1",
		},
	}
	updated := runner.persistActualBranchNameValueBestEffort(context.Background(), issue, "colin-94")

	if updated.ColinMetadata == nil {
		t.Fatal("updated.ColinMetadata = nil, want metadata")
	}
	if updated.ColinMetadata.ActualBranchName != "colin-94" {
		t.Fatalf("ActualBranchName = %q, want %q", updated.ColinMetadata.ActualBranchName, "colin-94")
	}
	if updated.ColinMetadata.ReviewPublishDirective != domain.ReviewPublishDirectiveSkip {
		t.Fatalf("ReviewPublishDirective = %q, want %q", updated.ColinMetadata.ReviewPublishDirective, domain.ReviewPublishDirectiveSkip)
	}
	if tracker.metadata.ActualBranchName != "colin-94" {
		t.Fatalf("persisted ActualBranchName = %q, want %q", tracker.metadata.ActualBranchName, "colin-94")
	}
}

func TestCodexMetadataWithResultStoresPullRequestIdentity(t *testing.T) {
	t.Parallel()

	issue := domain.Issue{
		ColinMetadata: &domain.ColinMetadata{
			ReviewPublishDirective: domain.ReviewPublishDirectiveSkip,
			LastSummaryCommentID:   "comment-1",
		},
	}

	metadata := codexMetadataWithResult(issue, RunTypeReviewPublish, metadataOutcomeReady, "", repoops.Result{
		PRNumber:  14,
		PRURL:     "https://github.com/pmenglund/colin/pull/14",
		PRState:   "OPEN",
		PRHeadRef: "pmenglund/colin-112",
		PRBaseRef: "main",
	})

	if metadata.PullRequestNumber != 14 {
		t.Fatalf("PullRequestNumber = %d, want 14", metadata.PullRequestNumber)
	}
	if metadata.PullRequestURL != "https://github.com/pmenglund/colin/pull/14" {
		t.Fatalf("PullRequestURL = %q, want GitHub PR URL", metadata.PullRequestURL)
	}
	if metadata.PullRequestHeadRef != "pmenglund/colin-112" {
		t.Fatalf("PullRequestHeadRef = %q, want %q", metadata.PullRequestHeadRef, "pmenglund/colin-112")
	}
	if metadata.PullRequestBaseRef != "main" {
		t.Fatalf("PullRequestBaseRef = %q, want main", metadata.PullRequestBaseRef)
	}
	if metadata.ReviewPublishDirective != domain.ReviewPublishDirectiveSkip {
		t.Fatalf("ReviewPublishDirective = %q, want %q", metadata.ReviewPublishDirective, domain.ReviewPublishDirectiveSkip)
	}
}

func TestHandleMergeFailureReturnsIssueToReview(t *testing.T) {
	t.Parallel()

	tracker := &stubTracker{}
	runner := &Runner{
		cfg: domain.ServiceConfig{
			Repo: domain.RepoConfig{PublishStates: []string{"Review"}},
		},
		tracker: tracker,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	result := runner.handleMergeFailure(
		context.Background(),
		domain.Issue{ID: "issue-1", Identifier: "COLIN-112", State: "Merge"},
		"/tmp/workspace",
		repoops.Result{
			Branch:   "colin-112",
			BaseRef:  "main",
			PRNumber: 11,
			PRURL:    "https://example.test/pr/11",
			PRState:  "OPEN",
		},
		errors.New("gh pr merge 11 --squash: exit status 1: X Pull request pmenglund/colin#11 is not mergeable: the merge commit cannot be cleanly created."),
	)

	if result.Status != "succeeded" {
		t.Fatalf("result.Status = %q, want %q", result.Status, "succeeded")
	}
	if result.Issue.State != "Review" {
		t.Fatalf("result.Issue.State = %q, want %q", result.Issue.State, "Review")
	}
	if tracker.updatedState != "Review" {
		t.Fatalf("updated state = %q, want %q", tracker.updatedState, "Review")
	}
	if !strings.Contains(result.Summary, "moved the issue back to `Review`") {
		t.Fatalf("result.Summary = %q, want review handoff message", result.Summary)
	}
	if !strings.Contains(result.Summary, "gh pr checkout 11 && git fetch origin main && git merge origin/main") {
		t.Fatalf("result.Summary = %q, want conflict resolution command", result.Summary)
	}
}

func TestRunnerRepairsMergeConflictAndRetriesMerge(t *testing.T) {
	tempDir := t.TempDir()
	repoURL := createRunnerGitOrigin(t, tempDir)
	branch := "pmenglund/colin-124-internal-log-buffer"
	prepareRunnerMergeConflict(t, tempDir, repoURL, branch, "symphony")
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q COLIN_FAKE_CODEX_MERGE_RECOVERY_FILE_CONTENT=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		"base branch text\nfeature branch text\n",
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:    filepath.Join(tempDir, "workspaces"),
			RepoURL: repoURL,
			BaseRef: "symphony",
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
			RemoteName:    "origin",
			MergeMethod:   "merge",
		},
		Agent: domain.AgentConfig{
			MaxTurns: 1,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       5 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      5 * time.Second,
		},
	}
	tracker := &stubTracker{
		resolvedMergeState:  "Merged",
		resolveMergeStateOK: true,
	}
	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(&repoops.GitHubPullRequest{
		Number:      19,
		URL:         "https://github.com/pmenglund/colin/pull/19",
		State:       "OPEN",
		HeadRefName: branch,
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{}, nil)
	fakeGitHub.PullRequestReactionsReturns(repoops.GitHubReactionPage{}, nil)
	fakeGitHub.MergePullRequestReturnsOnCall(0, errors.New("X Pull request pmenglund/colin#19 is not mergeable: the merge commit cannot be cleanly created."))
	fakeGitHub.MergePullRequestReturnsOnCall(1, nil)

	runner := newRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		repoops.NewManagerWithGitHubClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakeGitHub),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-124",
		Title:      "internal log buffer",
		State:      "Merge",
		BranchName: testStringPtr(branch),
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if result.Issue.State != "Merged" {
		t.Fatalf("result.Issue.State = %q, want %q", result.Issue.State, "Merged")
	}
	if tracker.updatedStates[len(tracker.updatedStates)-1] != "Merged" {
		t.Fatalf("last updated state = %q, want %q", tracker.updatedStates[len(tracker.updatedStates)-1], "Merged")
	}

	if got := fakeGitHub.MergePullRequestCallCount(); got != 2 {
		t.Fatalf("MergePullRequestCallCount() = %d, want 2", got)
	}

	promptLog := readRunnerFile(t, promptLogPath)
	if !strings.Contains(promptLog, "Repair the merge conflict for the Linear issue below so Colin can retry the GitHub merge.") {
		t.Fatalf("prompt log = %q, want merge recovery prompt", promptLog)
	}
	if !strings.Contains(promptLog, outcomeReadyForMergeRetry) {
		t.Fatalf("prompt log = %q, want merge retry outcome marker", promptLog)
	}
}

func TestRunnerKeepsMergeConflictInMergeWhenRepairNeedsFreshCodexApproval(t *testing.T) {
	tempDir := t.TempDir()
	repoURL := createRunnerGitOrigin(t, tempDir)
	branch := "pmenglund/colin-124-internal-log-buffer"
	prepareRunnerMergeConflict(t, tempDir, repoURL, branch, "symphony")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_MERGE_RECOVERY_FILE_CONTENT=%q %q -test.run=TestHelperProcessFakeCodex --",
		"base branch text\nfeature branch text\n",
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			Root:    filepath.Join(tempDir, "workspaces"),
			RepoURL: repoURL,
			BaseRef: "symphony",
		},
		Repo: domain.RepoConfig{
			PublishStates:         []string{"Review"},
			MergeStates:           []string{"Merge"},
			RemoteName:            "origin",
			MergeMethod:           "merge",
			CodexPRReviewsEnabled: true,
		},
		Agent: domain.AgentConfig{
			MaxTurns: 1,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       5 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      5 * time.Second,
		},
	}
	tracker := &stubTracker{}
	fakeGitHub := &fakes.FakeGitHubClient{}
	fakeGitHub.PullRequestByHeadReturns(&repoops.GitHubPullRequest{
		Number:      19,
		URL:         "https://github.com/pmenglund/colin/pull/19",
		State:       "OPEN",
		HeadRefName: branch,
		BaseRefName: "symphony",
	}, nil)
	fakeGitHub.ReviewThreadsReturns(repoops.GitHubReviewThreadPage{}, nil)
	requestedAt := testTimePtr(time.Date(2026, time.March, 30, 19, 51, 30, 0, time.UTC))
	approvedAt := testTimePtr(time.Date(2026, time.March, 30, 19, 51, 45, 0, time.UTC))
	fakeGitHub.PullRequestReactionsReturnsOnCall(0, repoops.GitHubReactionPage{
		Reactions: []repoops.GitHubReaction{
			{
				Content:   "EYES",
				UserLogin: "chatgpt-codex-connector[bot]",
				CreatedAt: requestedAt,
			},
			{
				Content:   "THUMBS_UP",
				UserLogin: "chatgpt-codex-connector[bot]",
				CreatedAt: approvedAt,
			},
		},
	}, nil)
	fakeGitHub.PullRequestReactionsReturnsOnCall(1, repoops.GitHubReactionPage{
		Reactions: []repoops.GitHubReaction{
			{
				Content:   "EYES",
				UserLogin: "chatgpt-codex-connector[bot]",
				CreatedAt: testTimePtr(time.Date(2026, time.March, 30, 19, 52, 30, 0, time.UTC)),
			},
		},
	}, nil)
	fakeGitHub.MergePullRequestReturnsOnCall(0, errors.New("X Pull request pmenglund/colin#19 is not mergeable: the merge commit cannot be cleanly created."))

	runner := newRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		repoops.NewManagerWithGitHubClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), fakeGitHub),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-124",
		Title:      "internal log buffer",
		State:      "Merge",
		BranchName: testStringPtr(branch),
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if result.Issue.State != "Merge" {
		t.Fatalf("result.Issue.State = %q, want %q", result.Issue.State, "Merge")
	}
	if tracker.updatedState != "" {
		t.Fatalf("updated state = %q, want empty", tracker.updatedState)
	}
	if !strings.Contains(result.Summary, "repaired the merge conflict") {
		t.Fatalf("result.Summary = %q, want repaired merge conflict note", result.Summary)
	}
	if !strings.Contains(result.Summary, "waiting for a `thumbs up` reaction") {
		t.Fatalf("result.Summary = %q, want thumbs up blocker", result.Summary)
	}
	if !strings.Contains(result.Summary, "Keep the issue in `Merge`") {
		t.Fatalf("result.Summary = %q, want keep in merge instruction", result.Summary)
	}

	if got := fakeGitHub.MergePullRequestCallCount(); got != 1 {
		t.Fatalf("MergePullRequestCallCount() = %d, want 1", got)
	}
}

func TestRunnerCreatesExecPlanAndInjectsItIntoCodingPrompt(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-108",
			Title:      "Add exec plans",
			State:      "Todo",
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:          "issue-1",
		Identifier:  "COLIN-108",
		Title:       "Add exec plans",
		Description: testStringPtr("Create and reuse an execution plan."),
		State:       "Todo",
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if result.Issue.ExecPlan == nil {
		t.Fatal("result.Issue.ExecPlan = nil, want exec plan")
	}
	if result.Issue.ExecPlan.Body != "# Fake ExecPlan\n\nPlan details." {
		t.Fatalf("ExecPlan.Body = %q, want fake plan", result.Issue.ExecPlan.Body)
	}
	if tracker.execPlan.Body != "# Fake ExecPlan\n\nPlan details." {
		t.Fatalf("tracker exec plan = %q, want fake plan", tracker.execPlan.Body)
	}
	if tracker.metadata.ExecPlanDecision != domain.ExecPlanDecisionExecPlan {
		t.Fatalf("metadata.ExecPlanDecision = %q, want %q", tracker.metadata.ExecPlanDecision, domain.ExecPlanDecisionExecPlan)
	}

	logData, err := os.ReadFile(promptLogPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.") {
		t.Fatalf("prompts log missing exec plan decision turn: %q", logText)
	}
	if !strings.Contains(logText, "Create an ExecPlan for the Linear issue below.") {
		t.Fatalf("prompts log missing exec plan turn: %q", logText)
	}
	if !strings.Contains(logText, "Work on COLIN-108.\n\nExecPlan:\n\n# Fake ExecPlan\n\nPlan details.") {
		t.Fatalf("prompts log missing coding prompt with injected plan: %q", logText)
	}
}

func TestRunnerReusesExistingExecPlanWithoutCreatingAnother(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"In Progress"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:            "issue-1",
			Identifier:    "COLIN-109",
			Title:         "Reuse exec plans",
			State:         "In Progress",
			ExecPlanCount: 1,
			ExecPlan: &domain.ExecPlan{
				Body: "# Persisted plan\n\nExisting details.",
			},
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:            "issue-1",
		Identifier:    "COLIN-109",
		Title:         "Reuse exec plans",
		State:         "In Progress",
		ExecPlanCount: 1,
		ExecPlan: &domain.ExecPlan{
			Body: "# Persisted plan\n\nExisting details.",
		},
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if tracker.execPlan.Body != "" {
		t.Fatalf("tracker exec plan = %q, want no new exec plan persisted", tracker.execPlan.Body)
	}
	if tracker.metadata.ExecPlanDecision != domain.ExecPlanDecisionExecPlan {
		t.Fatalf("metadata.ExecPlanDecision = %q, want %q", tracker.metadata.ExecPlanDecision, domain.ExecPlanDecisionExecPlan)
	}

	logData, err := os.ReadFile(promptLogPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logText := string(logData)
	if strings.Contains(logText, "Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.") {
		t.Fatalf("prompts log unexpectedly recomputed the exec plan decision: %q", logText)
	}
	if strings.Contains(logText, "Create an ExecPlan for the Linear issue below.") {
		t.Fatalf("prompts log unexpectedly created a second exec plan: %q", logText)
	}
	if !strings.Contains(logText, "Work on COLIN-109.\n\nExecPlan:\n\n# Persisted plan\n\nExisting details.") {
		t.Fatalf("prompts log missing coding prompt with reused plan: %q", logText)
	}
}

func TestRunnerSkipsExecPlanForOneShotDecision(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q COLIN_FAKE_CODEX_EXEC_PLAN_DECISION_TEXT=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		execPlanDecisionOneShotLine,
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-126",
			Title:      "ExecPlan decision",
			State:      "Todo",
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:          "issue-1",
		Identifier:  "COLIN-126",
		Title:       "ExecPlan decision",
		Description: testStringPtr("Decide whether to one-shot the work or persist an ExecPlan."),
		State:       "Todo",
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if result.Issue.ExecPlan != nil {
		t.Fatalf("result.Issue.ExecPlan = %#v, want nil", result.Issue.ExecPlan)
	}
	if tracker.execPlan.Body != "" {
		t.Fatalf("tracker.execPlan.Body = %q, want no exec plan persisted", tracker.execPlan.Body)
	}
	if tracker.metadata.ExecPlanDecision != domain.ExecPlanDecisionOneShot {
		t.Fatalf("metadata.ExecPlanDecision = %q, want %q", tracker.metadata.ExecPlanDecision, domain.ExecPlanDecisionOneShot)
	}

	logData, err := os.ReadFile(promptLogPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.") {
		t.Fatalf("prompts log missing exec plan decision turn: %q", logText)
	}
	if strings.Contains(logText, "Create an ExecPlan for the Linear issue below.") {
		t.Fatalf("prompts log unexpectedly created an exec plan: %q", logText)
	}
	if strings.Contains(logText, "ExecPlan:\n\n") {
		t.Fatalf("prompts log unexpectedly injected an exec plan into the coding prompt: %q", logText)
	}
	if !strings.Contains(logText, "Work on COLIN-126.") {
		t.Fatalf("prompts log missing coding prompt: %q", logText)
	}
}

func TestRunnerReusesPersistedOneShotDecisionWithoutCreatingExecPlan(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"In Progress"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-127",
			Title:      "Reuse one-shot decision",
			State:      "In Progress",
			ColinMetadata: &domain.ColinMetadata{
				ExecPlanDecision: domain.ExecPlanDecisionOneShot,
			},
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-127",
		Title:      "Reuse one-shot decision",
		State:      "In Progress",
		ColinMetadata: &domain.ColinMetadata{
			ExecPlanDecision: domain.ExecPlanDecisionOneShot,
		},
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if tracker.execPlan.Body != "" {
		t.Fatalf("tracker.execPlan.Body = %q, want no exec plan persisted", tracker.execPlan.Body)
	}
	if tracker.metadata.ExecPlanDecision != domain.ExecPlanDecisionOneShot {
		t.Fatalf("metadata.ExecPlanDecision = %q, want %q", tracker.metadata.ExecPlanDecision, domain.ExecPlanDecisionOneShot)
	}

	logData, err := os.ReadFile(promptLogPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logText := string(logData)
	if strings.Contains(logText, "Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.") {
		t.Fatalf("prompts log unexpectedly recomputed the exec plan decision: %q", logText)
	}
	if strings.Contains(logText, "Create an ExecPlan for the Linear issue below.") {
		t.Fatalf("prompts log unexpectedly created an exec plan: %q", logText)
	}
	if strings.Contains(logText, "ExecPlan:\n\n") {
		t.Fatalf("prompts log unexpectedly injected an exec plan into the coding prompt: %q", logText)
	}
}

func TestRunnerFailsWhenExecPlanDecisionOutputIsMalformed(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q COLIN_FAKE_CODEX_EXEC_PLAN_DECISION_TEXT=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		"maybe one-shot",
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-128",
			Title:      "Invalid decision output",
			State:      "Todo",
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-128",
		Title:      "Invalid decision output",
		State:      "Todo",
	}, nil, nil)

	if result.Status != "failed" {
		t.Fatalf("Run() status = %q, want %q", result.Status, "failed")
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), "unexpected decision") {
		t.Fatalf("Run() error = %v, want unexpected decision failure", result.Err)
	}
	if tracker.execPlan.Body != "" {
		t.Fatalf("tracker.execPlan.Body = %q, want no exec plan persisted", tracker.execPlan.Body)
	}
	if tracker.metadata.ExecPlanDecision != "" {
		t.Fatalf("metadata.ExecPlanDecision = %q, want empty", tracker.metadata.ExecPlanDecision)
	}
}

func TestRunnerRetriesMalformedExecPlanDecisionOnce(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q COLIN_FAKE_CODEX_EXEC_PLAN_DECISION_TEXT=%q COLIN_FAKE_CODEX_EXEC_PLAN_DECISION_RETRY_TEXT=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		"maybe one-shot",
		execPlanDecisionOneShotLine,
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-128",
			Title:      "Retry malformed decision",
			State:      "Todo",
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-128",
		Title:      "Retry malformed decision",
		State:      "Todo",
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if tracker.metadata.ExecPlanDecision != domain.ExecPlanDecisionOneShot {
		t.Fatalf("metadata.ExecPlanDecision = %q, want %q", tracker.metadata.ExecPlanDecision, domain.ExecPlanDecisionOneShot)
	}
	logData, err := os.ReadFile(promptLogPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(logData), "Your previous ExecPlan strategy response could not be parsed.") {
		t.Fatalf("prompts log missing retry prompt: %q", string(logData))
	}
}

func TestRunnerParsesExecPlanTurnsFromCompletedItemTextOnly(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q COLIN_FAKE_CODEX_ITEM_COMPLETED_PARAMS_TEXT=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		"Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.",
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-128",
			Title:      "Completed item text only",
			State:      "Todo",
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-128",
		Title:      "Completed item text only",
		State:      "Todo",
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if tracker.metadata.ExecPlanDecision != domain.ExecPlanDecisionExecPlan {
		t.Fatalf("metadata.ExecPlanDecision = %q, want %q", tracker.metadata.ExecPlanDecision, domain.ExecPlanDecisionExecPlan)
	}
	if tracker.execPlan.Body != "# Fake ExecPlan\n\nPlan details." {
		t.Fatalf("tracker.execPlan.Body = %q, want fake exec plan body", tracker.execPlan.Body)
	}
}

func TestRunnerDoesNotInjectPersistedExecPlanWhenDisabled(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: false,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-108",
			Title:      "Add exec plans",
			State:      "Todo",
			ExecPlan: &domain.ExecPlan{
				Body: "# Persisted plan\n\nExisting details.",
			},
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:          "issue-1",
		Identifier:  "COLIN-108",
		Title:       "Add exec plans",
		Description: testStringPtr("Create and reuse an execution plan."),
		State:       "Todo",
		ExecPlan: &domain.ExecPlan{
			Body: "# Persisted plan\n\nExisting details.",
		},
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}

	logData, err := os.ReadFile(promptLogPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logText := string(logData)
	if strings.Contains(logText, "ExecPlan:\n\n# Persisted plan\n\nExisting details.") {
		t.Fatalf("prompts log unexpectedly injected persisted exec plan: %q", logText)
	}
}

func TestRunnerClearsExecPlanSummaryBeforeCodingTurn(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	promptLogPath := filepath.Join(tempDir, "prompts.log")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_PROMPTS_LOG=%q COLIN_FAKE_CODEX_CODING_TEXT= %q -test.run=TestHelperProcessFakeCodex --",
		promptLogPath,
		os.Args[0],
	)
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"Todo", "In Progress"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
		Codex: domain.CodexConfig{
			Command:           command,
			ApprovalPolicy:    "never",
			ThreadSandbox:     "danger-full-access",
			TurnSandboxPolicy: map[string]any{"type": "dangerFullAccess"},
			TurnTimeout:       3 * time.Second,
			ReadTimeout:       time.Second,
			StallTimeout:      3 * time.Second,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:         "issue-1",
			Identifier: "COLIN-108",
			Title:      "Add exec plans",
			State:      "In Progress",
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:          "issue-1",
		Identifier:  "COLIN-108",
		Title:       "Add exec plans",
		Description: testStringPtr("Create and reuse an execution plan."),
		State:       "Todo",
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if result.Issue.State != "Refine" {
		t.Fatalf("Issue.State = %q, want %q", result.Issue.State, "Refine")
	}
	if strings.Contains(result.Summary, "# Fake ExecPlan") {
		t.Fatalf("Summary = %q, want exec plan summary cleared before coding turn", result.Summary)
	}
	if !strings.Contains(result.Summary, "Colin reached the maximum of `1` turns") {
		t.Fatalf("Summary = %q, want max-turn handoff note", result.Summary)
	}
}

func TestRunnerMovesIssueToRefineWhenExecPlanAttachmentsAreDuplicated(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	cfg := domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			ActiveStates: []string{"In Progress"},
		},
		Workspace: domain.WorkspaceConfig{
			Root: filepath.Join(tempDir, "workspaces"),
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
		},
		Agent: domain.AgentConfig{
			MaxTurns:       1,
			CreateExecPlan: true,
		},
	}
	tracker := &stubTracker{
		refreshedIssue: domain.Issue{
			ID:            "issue-1",
			Identifier:    "COLIN-110",
			Title:         "Repair exec plan metadata",
			State:         "In Progress",
			ExecPlanCount: 2,
		},
	}
	runner := NewRunner(
		cfg,
		domain.WorkflowDefinition{PromptTemplate: "Work on {{ .issue.identifier }}."},
		tracker,
		workspace.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	result := runner.Run(context.Background(), domain.Issue{
		ID:            "issue-1",
		Identifier:    "COLIN-110",
		Title:         "Repair exec plan metadata",
		State:         "In Progress",
		ExecPlanCount: 2,
	}, nil, nil)

	if result.Status != "succeeded" {
		t.Fatalf("Run() status = %q, want %q (err=%v)", result.Status, "succeeded", result.Err)
	}
	if result.Issue.State != "Refine" {
		t.Fatalf("Issue.State = %q, want %q", result.Issue.State, "Refine")
	}
	if tracker.updatedState != "Refine" {
		t.Fatalf("updated state = %q, want %q", tracker.updatedState, "Refine")
	}
	if tracker.metadata.LastOutcome != metadataOutcomePlan {
		t.Fatalf("metadata.LastOutcome = %q, want %q", tracker.metadata.LastOutcome, metadataOutcomePlan)
	}
	if !strings.Contains(result.Summary, "multiple `Colin ExecPlan` attachments") {
		t.Fatalf("Summary = %q, want duplicate exec plan guidance", result.Summary)
	}
}

func TestHelperProcessFakeCodex(t *testing.T) {
	if os.Getenv("COLIN_FAKE_CODEX") != "1" {
		return
	}
	if err := runFakeCodex(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type stubTracker struct {
	refreshedIssue      domain.Issue
	updatedIssueID      string
	updatedState        string
	updatedStates       []string
	removedLabels       []string
	issueComments       []string
	commentReplies      []string
	metadata            domain.ColinMetadata
	execPlan            domain.ExecPlan
	resolvedMergeState  string
	resolveMergeStateOK bool
}

func (s *stubTracker) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	return nil, nil
}

func (s *stubTracker) FetchIssuesByStates(context.Context, []string) ([]domain.Issue, error) {
	return nil, nil
}

func (s *stubTracker) FetchIssueStatesByIDs(context.Context, []string) ([]domain.Issue, error) {
	return []domain.Issue{s.refreshedIssue}, nil
}

func (s *stubTracker) FetchIssueByID(context.Context, string) (domain.Issue, error) {
	return s.refreshedIssue, nil
}

func (s *stubTracker) UpdateIssueState(_ context.Context, issueID string, stateName string) error {
	s.updatedIssueID = issueID
	s.updatedState = stateName
	s.updatedStates = append(s.updatedStates, stateName)
	return nil
}

func (s *stubTracker) EnsureIssueLabel(context.Context, string) error {
	return nil
}

func (s *stubTracker) AddIssueLabel(context.Context, string, string) error {
	return nil
}

func (s *stubTracker) RemoveIssueLabel(_ context.Context, issueID string, labelName string) error {
	s.removedLabels = append(s.removedLabels, issueID+":"+labelName)
	return nil
}

func (s *stubTracker) ResolveGitAutomationState(_ context.Context, issueID string, event string, targetBranch string) (string, bool, error) {
	if issueID == "" || event == "" || targetBranch == "" {
		return "", false, nil
	}
	return s.resolvedMergeState, s.resolveMergeStateOK, nil
}

func (s *stubTracker) CreateIssueComment(_ context.Context, _ string, body string) (string, error) {
	s.issueComments = append(s.issueComments, body)
	return "", nil
}

func (s *stubTracker) CreateCommentReply(_ context.Context, _ string, _ string, body string) (string, error) {
	s.commentReplies = append(s.commentReplies, body)
	return "", nil
}

func (s *stubTracker) UpsertIssueMetadata(_ context.Context, _ string, metadata domain.ColinMetadata) (domain.ColinMetadata, error) {
	s.metadata = metadata
	return metadata, nil
}

func (s *stubTracker) UpsertIssueExecPlan(_ context.Context, _ string, plan domain.ExecPlan) (domain.ExecPlan, error) {
	s.execPlan = plan
	return plan, nil
}

func (s *stubTracker) CurrentRateLimits() map[string]any {
	return nil
}

func runFakeCodex() error {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	var threadID string
	var turnID string

	for {
		msg, err := readJSONMessage(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		method, _ := msg["method"].(string)
		switch method {
		case "initialize":
			if err := writeJSONMessage(writer, map[string]any{
				"id": msg["id"],
				"result": map[string]any{
					"serverInfo": map[string]any{
						"name":    "fake-codex",
						"version": "1.0.0",
					},
				},
			}); err != nil {
				return err
			}
		case "initialized":
			continue
		case "thread/start":
			threadID = "thread-1"
			if err := writeJSONMessage(writer, map[string]any{
				"id": msg["id"],
				"result": map[string]any{
					"thread": map[string]any{"id": threadID},
				},
			}); err != nil {
				return err
			}
		case "turn/start":
			promptText := extractPromptText(msg)
			promptCwd := extractPromptCwd(msg)
			turnID = "turn-1"
			if err := writeJSONMessage(writer, map[string]any{
				"id":     msg["id"],
				"result": map[string]any{},
			}); err != nil {
				return err
			}
			if promptLog := os.Getenv("COLIN_FAKE_CODEX_PROMPTS_LOG"); promptLog != "" {
				if err := appendPromptLog(promptLog, promptText); err != nil {
					return err
				}
			}
			if isMergeRecoveryPrompt(promptText) {
				if err := runFakeMergeRecovery(promptCwd, promptText); err != nil {
					return err
				}
			}
			if err := writeJSONMessage(writer, map[string]any{
				"id":     "approval-1",
				"method": "item/commandExecution/requestApproval",
				"params": map[string]any{
					"itemId":   "item-1",
					"threadId": threadID,
					"turnId":   turnID,
					"command":  "echo hello",
					"cwd":      mustGetwd(),
				},
			}); err != nil {
				return err
			}

			approval, err := readJSONMessage(reader)
			if err != nil {
				return err
			}
			result, _ := approval["result"].(map[string]any)
			if decision, _ := result["decision"].(string); decision != "accept" {
				return fmt.Errorf("approval decision = %q, want accept", decision)
			}

			if err := writeJSONMessage(writer, map[string]any{
				"method": "turn/started",
				"params": map[string]any{
					"threadId": threadID,
					"turn": map[string]any{
						"id":     turnID,
						"status": "in_progress",
					},
				},
			}); err != nil {
				return err
			}
			itemCompletedParams := map[string]any{
				"threadId": threadID,
				"item": map[string]any{
					"text": fakeCodexTurnText(promptText),
				},
			}
			if value, ok := os.LookupEnv("COLIN_FAKE_CODEX_ITEM_COMPLETED_PARAMS_TEXT"); ok {
				itemCompletedParams["text"] = value
			}
			if err := writeJSONMessage(writer, map[string]any{
				"method": "item/completed",
				"params": itemCompletedParams,
			}); err != nil {
				return err
			}
			if err := writeJSONMessage(writer, map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn": map[string]any{
						"id":     turnID,
						"status": "completed",
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}

func testStringPtr(value string) *string {
	return &value
}

func testTimePtr(value time.Time) *time.Time {
	return &value
}

func fakeCodexTurnText(prompt string) string {
	if strings.Contains(prompt, "Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.") {
		if value, ok := os.LookupEnv("COLIN_FAKE_CODEX_EXEC_PLAN_DECISION_TEXT"); ok {
			return value
		}
		return execPlanDecisionExecPlanLine + "\n\nThis issue needs a persisted plan."
	}
	if strings.Contains(prompt, "Your previous ExecPlan strategy response could not be parsed.") {
		if value, ok := os.LookupEnv("COLIN_FAKE_CODEX_EXEC_PLAN_DECISION_RETRY_TEXT"); ok {
			return value
		}
		if value, ok := os.LookupEnv("COLIN_FAKE_CODEX_EXEC_PLAN_DECISION_TEXT"); ok {
			return value
		}
		return execPlanDecisionExecPlanLine + "\n\nThis issue needs a persisted plan."
	}
	if strings.Contains(prompt, "Create an ExecPlan for the Linear issue below.") {
		if value, ok := os.LookupEnv("COLIN_FAKE_CODEX_EXEC_PLAN_TEXT"); ok {
			return value
		}
		return "# Fake ExecPlan\n\nPlan details."
	}
	if isMergeRecoveryPrompt(prompt) {
		if value, ok := os.LookupEnv("COLIN_FAKE_CODEX_MERGE_RECOVERY_TEXT"); ok {
			return value
		}
		return outcomeReadyForMergeRetry + "\n\nResolved the merge conflict and ran focused verification."
	}
	if value, ok := os.LookupEnv("COLIN_FAKE_CODEX_CODING_TEXT"); ok {
		return value
	}
	return outcomeReadyForReview + "\n\nImplemented the requested change."
}

func extractPromptText(msg map[string]any) string {
	params, _ := msg["params"].(map[string]any)
	values := promptTextValues(params)
	return strings.TrimSpace(strings.Join(values, "\n\n"))
}

func extractPromptCwd(msg map[string]any) string {
	params, _ := msg["params"].(map[string]any)
	if cwd, _ := params["cwd"].(string); strings.TrimSpace(cwd) != "" {
		return strings.TrimSpace(cwd)
	}
	return ""
}

func promptTextValues(value any) []string {
	var out []string

	var walk func(any)
	walk = func(current any) {
		switch v := current.(type) {
		case map[string]any:
			for key, item := range v {
				if strings.EqualFold(strings.TrimSpace(key), "text") {
					if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
						out = append(out, strings.TrimSpace(text))
					}
					continue
				}
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}

	walk(value)
	return out
}

func appendPromptLog(path string, prompt string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := fmt.Fprintln(file, "===TURN==="); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(file, prompt); err != nil {
		return err
	}
	_, err = fmt.Fprintln(file, "===END===")
	return err
}

func isMergeRecoveryPrompt(prompt string) bool {
	return strings.Contains(prompt, "Repair the merge conflict for the Linear issue below so Colin can retry the GitHub merge.")
}

func runFakeMergeRecovery(cwd string, prompt string) error {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return errors.New("missing merge recovery cwd")
	}
	baseRef := extractPromptField(prompt, "- Base ref:")
	if baseRef == "" {
		baseRef = "symphony"
	}
	if _, err := runFakeGitCmd(cwd, "git", "config", "user.name", "Fake Codex"); err != nil {
		return err
	}
	if _, err := runFakeGitCmd(cwd, "git", "config", "user.email", "fake-codex@example.com"); err != nil {
		return err
	}
	if _, err := runFakeGitCmd(cwd, "git", "fetch", "origin", baseRef); err != nil {
		return err
	}
	if _, err := runFakeGitCmd(cwd, "git", "merge", "origin/"+baseRef); err != nil {
		// Ignore the expected conflict and resolve it below.
	}
	if content, ok := os.LookupEnv("COLIN_FAKE_CODEX_MERGE_RECOVERY_FILE_CONTENT"); ok {
		if err := os.WriteFile(filepath.Join(cwd, "README.md"), []byte(content), 0o644); err != nil {
			return err
		}
	}
	if _, err := runFakeGitCmd(cwd, "git", "add", "README.md"); err != nil {
		return err
	}
	if output, err := runFakeGitCmd(cwd, "git", "commit", "-m", "Resolve merge conflict"); err != nil {
		if !strings.Contains(output, "nothing to commit") {
			return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(output))
		}
	}
	if reactions, ok := os.LookupEnv("COLIN_FAKE_CODEX_MERGE_RECOVERY_REACTIONS_JSON"); ok {
		path := strings.TrimSpace(os.Getenv("COLIN_FAKE_GH_REACTIONS"))
		if path == "" {
			return errors.New("missing fake GitHub reactions path")
		}
		if err := os.WriteFile(path, []byte(reactions), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func extractPromptField(prompt string, prefix string) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, prefix))
	}
	return ""
}

func runFakeGitCmd(cwd string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func createRunnerGitOrigin(t *testing.T, tempDir string) string {
	t.Helper()

	remotePath := filepath.Join(tempDir, "origin.git")
	seedPath := filepath.Join(tempDir, "seed")

	runRunnerCmd(t, "", "git", "init", "--bare", remotePath)
	runRunnerCmd(t, "", "git", "init", seedPath)
	runRunnerCmd(t, seedPath, "git", "config", "user.name", "Test User")
	runRunnerCmd(t, seedPath, "git", "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(seedPath, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runRunnerCmd(t, seedPath, "git", "add", "README.md")
	runRunnerCmd(t, seedPath, "git", "commit", "-m", "seed")
	runRunnerCmd(t, seedPath, "git", "branch", "-M", "symphony")
	runRunnerCmd(t, seedPath, "git", "remote", "add", "origin", remotePath)
	runRunnerCmd(t, seedPath, "git", "push", "-u", "origin", "symphony")

	return remotePath
}

func prepareRunnerMergeConflict(t *testing.T, tempDir string, remotePath string, branch string, baseRef string) {
	t.Helper()

	authorPath := filepath.Join(tempDir, "author")
	runRunnerCmd(t, "", "git", "clone", remotePath, authorPath)
	runRunnerCmd(t, authorPath, "git", "config", "user.name", "Test User")
	runRunnerCmd(t, authorPath, "git", "config", "user.email", "test@example.com")
	runRunnerCmd(t, authorPath, "git", "checkout", baseRef)
	runRunnerCmd(t, authorPath, "git", "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(authorPath, "README.md"), []byte("feature branch text\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runRunnerCmd(t, authorPath, "git", "add", "README.md")
	runRunnerCmd(t, authorPath, "git", "commit", "-m", "feature change")
	runRunnerCmd(t, authorPath, "git", "push", "-u", "origin", branch)

	runRunnerCmd(t, authorPath, "git", "checkout", baseRef)
	if err := os.WriteFile(filepath.Join(authorPath, "README.md"), []byte("base branch text\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runRunnerCmd(t, authorPath, "git", "add", "README.md")
	runRunnerCmd(t, authorPath, "git", "commit", "-m", "base change")
	runRunnerCmd(t, authorPath, "git", "push", "origin", baseRef)
}

func readRunnerFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}

func runRunnerCmd(t *testing.T, cwd string, name string, args ...string) string {
	t.Helper()

	cmd := exec.Command(name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, string(output))
	}
	return strings.TrimSpace(string(output))
}

func readJSONMessage(reader *bufio.Reader) (map[string]any, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func writeJSONMessage(writer *bufio.Writer, msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(data, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}
