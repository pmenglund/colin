package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
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

func TestParseReviewDirectiveSummaryNeedsSpec(t *testing.T) {
	t.Parallel()

	directive, summary := parseReviewDirectiveSummary(outcomeNeedsSpec + "\n\nThe spec should be improved before implementation.")
	if directive != domain.ReviewPublishDirectiveSkip {
		t.Fatalf("directive = %q, want %q", directive, domain.ReviewPublishDirectiveSkip)
	}
	if summary != "The spec should be improved before implementation." {
		t.Fatalf("summary = %q", summary)
	}
}

func TestParseReviewDirectiveSummaryDefaultsToPublish(t *testing.T) {
	t.Parallel()

	directive, summary := parseReviewDirectiveSummary("Implemented the requested change.")
	if directive != domain.ReviewPublishDirectivePublish {
		t.Fatalf("directive = %q, want %q", directive, domain.ReviewPublishDirectivePublish)
	}
	if summary != "Implemented the requested change." {
		t.Fatalf("summary = %q", summary)
	}
}

func TestPersistReviewDirectiveSummary(t *testing.T) {
	t.Parallel()

	got := persistReviewDirectiveSummary("Ready for review.", domain.ReviewPublishDirectiveSkip)
	if want := "Ready for review.\n\n<!-- colin:review_publish=skip -->"; got != want {
		t.Fatalf("persistReviewDirectiveSummary() = %q, want %q", got, want)
	}
}

func TestMoveSuccessfulActiveIssueToPublishStateUsesSkipDirective(t *testing.T) {
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

	issue, err := runner.moveSuccessfulCodingRunToPublishState(context.Background(), domain.Issue{
		ID:         "issue-1",
		Identifier: "COLIN-94",
		State:      "Todo",
	}, domain.ReviewPublishDirectiveSkip)
	if err != nil {
		t.Fatalf("moveSuccessfulCodingRunToPublishState() error = %v", err)
	}
	if issue.State != "Review" {
		t.Fatalf("issue.State = %q, want %q", issue.State, "Review")
	}
	if issue.ReviewPublishDirective != domain.ReviewPublishDirectiveSkip {
		t.Fatalf("issue.ReviewPublishDirective = %q, want %q", issue.ReviewPublishDirective, domain.ReviewPublishDirectiveSkip)
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
	want := "Implemented the change.\n\nColin reached the maximum of `6` turns while the issue remained in `In Progress`, so it is handing off for human review."
	if got != want {
		t.Fatalf("appendMaxTurnsSummary() = %q, want %q", got, want)
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

func (s *stubTracker) CreateIssueComment(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *stubTracker) CreateCommentReply(context.Context, string, string, string) (string, error) {
	return "", nil
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
			turnID = "turn-1"
			if err := writeJSONMessage(writer, map[string]any{
				"id":     msg["id"],
				"result": map[string]any{},
			}); err != nil {
				return err
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
						"text": "done",
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
