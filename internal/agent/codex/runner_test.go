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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repoops"
	"github.com/pmenglund/colin/internal/workspace"
)

func TestRunnerMovesSuccessfulActiveIssueToPublishState(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 %q -test.run=TestHelperProcessFakeCodex --",
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

func TestBlockMergeForCodexReviewReturnsIssueToReviewWhenApprovalPending(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.March, 28, 18, 1, 0, 0, time.UTC)
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
	if issue.State != "Review" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "Review")
	}
	if tracker.updatedState != "Review" {
		t.Fatalf("updated state = %q, want %q", tracker.updatedState, "Review")
	}
	if !strings.Contains(summary, "thumbs up") {
		t.Fatalf("summary = %q, want thumbs up blocker", summary)
	}
}

func TestBlockMergeForCodexReviewReturnsIssueToReviewWhenThreadsRemain(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.March, 28, 18, 1, 0, 0, time.UTC)
	approvedAt := requestedAt.Add(time.Minute)
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

	logData, err := os.ReadFile(promptLogPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "Create an ExecPlan for the Linear issue below.") {
		t.Fatalf("prompts log missing exec plan turn: %q", logText)
	}
	if !strings.Contains(logText, "Work on COLIN-108.\n\nExecPlan:\n\n# Fake ExecPlan\n\nPlan details.") {
		t.Fatalf("prompts log missing coding prompt with injected plan: %q", logText)
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
			if err := writeJSONMessage(writer, map[string]any{
				"method": "item/completed",
				"params": map[string]any{
					"threadId": threadID,
					"item": map[string]any{
						"text": fakeCodexTurnText(promptText),
					},
				},
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

func fakeCodexTurnText(prompt string) string {
	if strings.Contains(prompt, "Create an ExecPlan for the Linear issue below.") {
		if value, ok := os.LookupEnv("COLIN_FAKE_CODEX_EXEC_PLAN_TEXT"); ok {
			return value
		}
		return "# Fake ExecPlan\n\nPlan details."
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
