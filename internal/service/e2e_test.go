package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

const serviceE2EWaitTimeout = 10 * time.Second

func TestServiceRunsIssueEndToEnd(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "codex.marker")
	cwdLogPath := filepath.Join(tempDir, "codex.cwd.log")
	relativeWorkspaceRoot := fmt.Sprintf("./.colin-e2e-%d/workspaces", time.Now().UnixNano())
	workspaceRoot, err := filepath.Abs(filepath.Clean(relativeWorkspaceRoot))
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Dir(workspaceRoot))
	})

	linear := newFakeLinearServer(markerPath)
	server := httptest.NewServer(linear)
	defer server.Close()

	workflowPath := filepath.Join(tempDir, "WORKFLOW.md")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_MARKER=%q COLIN_FAKE_CODEX_CWD_LOG=%q %q -test.run=TestHelperProcessFakeCodex --",
		markerPath,
		cwdLogPath,
		os.Args[0],
	)
	workflow := fmt.Sprintf(`---
tracker:
  kind: linear
  endpoint: %q
  api_key: test-linear-key
  project_slug: test-project
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
polling:
  interval_ms: 100
workspace:
  root: %q
agent:
  max_concurrent_agents: 1
  max_turns: 1
  max_retry_backoff_ms: 500
codex:
  command: %q
  turn_timeout_ms: 3000
  read_timeout_ms: 1000
  stall_timeout_ms: 3000
  approval_policy: never
  thread_sandbox: danger-full-access
  turn_sandbox_policy:
    type: dangerFullAccess
server:
  port: 0
---
Work on {{ .issue.identifier }}.
`, server.URL, relativeWorkspaceRoot, command)
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	logger := newLogger(io.Discard, false)
	svc, err := New(context.Background(), logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitFor(t, "fake Codex completion marker", serviceE2EWaitTimeout, func() bool {
		_, err := os.Stat(markerPath)
		return err == nil
	})

	waitFor(t, "Linear progress reply", serviceE2EWaitTimeout, func() bool {
		return linear.ReplyCount() > 0
	})
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop after cancellation")
	}

	workspacePath := filepath.Join(workspaceRoot, "test-project", "COLIN-93")
	gotCWD, err := os.ReadFile(cwdLogPath)
	if err != nil {
		t.Fatalf("read cwd log: %v", err)
	}
	lines := nonEmptyLines(string(gotCWD))
	if len(lines) != 1 {
		t.Fatalf("helper invocation count = %d, want 1; log=%q", len(lines), string(gotCWD))
	}
	if lines[0] != workspacePath {
		t.Fatalf("helper cwd = %q, want %q", lines[0], workspacePath)
	}

	if linear.AuthHeader() != "test-linear-key" {
		t.Fatalf("Authorization header = %q, want %q", linear.AuthHeader(), "test-linear-key")
	}
	if linear.CandidateFetches() == 0 {
		t.Fatal("expected candidate issue fetches")
	}
	if linear.CommentCount() == 0 {
		t.Fatal("expected Linear progress comments")
	}
	root := linear.RootComment()
	if root == nil {
		t.Fatal("expected top-level Linear progress comment")
	}
	if !strings.HasPrefix(root.Body, "[colin]") {
		t.Fatalf("root comment body = %q, want [colin] prefix", root.Body)
	}
	if !strings.Contains(root.Body, "Workspace: `"+workspacePath+"`") {
		t.Fatalf("root comment body = %q, want workspace path", root.Body)
	}
	if !strings.Contains(root.Body, "Session ID: `thread-1-turn-1`") {
		t.Fatalf("root comment body = %q, want session id", root.Body)
	}
	if strings.Contains(root.Body, "Issue: `") {
		t.Fatalf("root comment body = %q, want no redundant issue line", root.Body)
	}
	if strings.Contains(root.Body, "State: `") {
		t.Fatalf("root comment body = %q, want no redundant state line", root.Body)
	}
	if linear.ReplyCount() == 0 {
		t.Fatal("expected Linear progress replies")
	}
	for _, comment := range linear.Comments() {
		if !strings.HasPrefix(comment.Body, "[colin]") {
			t.Fatalf("comment body = %q, want [colin] prefix", comment.Body)
		}
		if strings.Contains(comment.Body, "Colin scheduled retry attempt") {
			t.Fatalf("comment body = %q, want hidden internal verification retries", comment.Body)
		}
		if strings.Contains(comment.Body, "Colin is starting retry attempt") {
			t.Fatalf("comment body = %q, want hidden internal verification retries", comment.Body)
		}
	}
}

func TestServiceAppliesTargetCodexSecurityPolicy(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	origin := newServiceE2EGitOrigin(t)
	markerPath := filepath.Join(tempDir, "codex.marker")
	requestLogPath := filepath.Join(tempDir, "codex.requests.jsonl")

	linear := newFakeLinearServer(markerPath)
	server := httptest.NewServer(linear)
	defer server.Close()

	workflowPath := filepath.Join(tempDir, "WORKFLOW.md")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_MARKER=%q COLIN_FAKE_CODEX_REQUEST_LOG=%q %q -test.run=TestHelperProcessFakeCodex --",
		markerPath,
		requestLogPath,
		os.Args[0],
	)
	workflow := fmt.Sprintf(`---
tracker:
  kind: linear
  endpoint: %q
  api_key: test-linear-key
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
polling:
  interval_ms: 100
workspace:
  root: %q
agent:
  max_concurrent_agents: 1
  max_turns: 1
  max_retry_backoff_ms: 500
targets:
  - name: guarded
    project_slug: test-project
    repo_url: %q
    base_ref: main
    codex:
      approval_policy: on-request
      thread_sandbox: read-only
      turn_sandbox_policy:
        type: readOnly
codex:
  command: %q
  approval_policy: never
  thread_sandbox: danger-full-access
  turn_sandbox_policy:
    type: dangerFullAccess
  turn_timeout_ms: 3000
  read_timeout_ms: 1000
  stall_timeout_ms: 3000
server:
  port: 0
---
Work on {{ .issue.identifier }}.
`, server.URL, filepath.Join(tempDir, "workspaces"), origin, command)
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	logger := newLogger(io.Discard, false)
	svc, err := New(context.Background(), logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitFor(t, "fake Codex marker", serviceE2EWaitTimeout, func() bool {
		_, err := os.Stat(markerPath)
		return err == nil
	})
	waitFor(t, "fake Codex thread and turn requests", serviceE2EWaitTimeout, func() bool {
		requests, err := readFakeCodexRequests(requestLogPath)
		if err != nil {
			return false
		}
		return fakeCodexRequestByMethod(requests, "thread/start") != nil && fakeCodexRequestByMethod(requests, "turn/start") != nil
	})
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop after cancellation")
	}

	requests, err := readFakeCodexRequests(requestLogPath)
	if err != nil {
		t.Fatalf("read fake Codex requests: %v", err)
	}
	threadStart := fakeCodexRequestByMethod(requests, "thread/start")
	if threadStart == nil {
		t.Fatalf("fake Codex request log = %#v, want thread/start", requests)
	}
	threadParams, _ := threadStart["params"].(map[string]any)
	if got, _ := threadParams["approvalPolicy"].(string); got != "on-request" {
		t.Fatalf("thread/start approvalPolicy = %q, want on-request", got)
	}
	if got, _ := threadParams["sandbox"].(string); got != "read-only" {
		t.Fatalf("thread/start sandbox = %q, want read-only", got)
	}

	turnStart := fakeCodexRequestByMethod(requests, "turn/start")
	if turnStart == nil {
		t.Fatalf("fake Codex request log = %#v, want turn/start", requests)
	}
	turnParams, _ := turnStart["params"].(map[string]any)
	if got, _ := turnParams["approvalPolicy"].(string); got != "on-request" {
		t.Fatalf("turn/start approvalPolicy = %q, want on-request", got)
	}
	sandboxPolicy, _ := turnParams["sandboxPolicy"].(map[string]any)
	if got, _ := sandboxPolicy["type"].(string); got != "readOnly" {
		t.Fatalf("turn/start sandboxPolicy.type = %q, want readOnly", got)
	}
}

func TestServiceExposesDashboardState(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "codex.marker")

	linear := newFakeLinearServer(markerPath)
	server := httptest.NewServer(linear)
	defer server.Close()

	workflowPath := filepath.Join(tempDir, "WORKFLOW.md")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_DELAY_MS=1500 %q -test.run=TestHelperProcessFakeCodex --",
		os.Args[0],
	)
	workflow := fmt.Sprintf(`---
tracker:
  kind: linear
  endpoint: %q
  api_key: test-linear-key
  project_slug: test-project
polling:
  interval_ms: 100
workspace:
  root: %q
agent:
  max_concurrent_agents: 1
  max_turns: 1
codex:
  command: %q
  turn_timeout_ms: 5000
  read_timeout_ms: 1000
  stall_timeout_ms: 5000
server:
  port: 0
---
Work on {{ .issue.identifier }}.
`, server.URL, filepath.Join(tempDir, "workspaces"), command)
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := New(context.Background(), logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitFor(t, "dashboard URL", serviceE2EWaitTimeout, func() bool {
		return svc.DashboardURL() != ""
	})

	waitFor(t, "running dashboard state", serviceE2EWaitTimeout, func() bool {
		resp, err := http.Get(svc.DashboardURL() + "/api/v1/state")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(body), `"running":1`)
	})

	waitFor(t, "info log entry", serviceE2EWaitTimeout, func() bool {
		snapshot, err := fetchBufferedLogs(svc.DashboardURL() + "/api/v1/logs?level=info")
		if err != nil {
			return false
		}
		return containsBufferedLog(snapshot, "service starting")
	})

	waitFor(t, "debug log entry", serviceE2EWaitTimeout, func() bool {
		snapshot, err := fetchBufferedLogs(svc.DashboardURL() + "/api/v1/logs?level=debug")
		if err != nil {
			return false
		}
		return containsBufferedLog(snapshot, "poll tick started") || containsBufferedLog(snapshot, "poll tick completed")
	})

	resp, err := http.Get(svc.DashboardURL() + "/")
	if err != nil {
		t.Fatalf("GET dashboard: %v", err)
	}
	defer resp.Body.Close()
	html, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !strings.Contains(string(html), "COLIN-93") {
		t.Fatalf("dashboard body = %q, want issue identifier", string(html))
	}

	infoLogs, err := fetchBufferedLogs(svc.DashboardURL() + "/api/v1/logs?level=info")
	if err != nil {
		t.Fatalf("fetch info logs: %v", err)
	}
	if !containsBufferedLog(infoLogs, "service starting") {
		t.Fatalf("info logs = %#v, want service starting", infoLogs.Entries)
	}
	if containsBufferedLog(infoLogs, "poll tick started") || containsBufferedLog(infoLogs, "poll tick completed") {
		t.Fatalf("info logs = %#v, want poll tick logs filtered out", infoLogs.Entries)
	}

	debugLogs, err := fetchBufferedLogs(svc.DashboardURL() + "/api/v1/logs?level=debug")
	if err != nil {
		t.Fatalf("fetch debug logs: %v", err)
	}
	if !containsBufferedLog(debugLogs, "poll tick started") && !containsBufferedLog(debugLogs, "poll tick completed") {
		t.Fatalf("debug logs = %#v, want poll tick debug log", debugLogs.Entries)
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop after cancellation")
	}
}

func TestServiceMovesNeedsSpecOutcomeToRefine(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "codex.marker")

	linear := newFakeLinearServer(markerPath)
	server := httptest.NewServer(linear)
	defer server.Close()

	workflowPath := filepath.Join(tempDir, "WORKFLOW.md")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_OUTCOME=needs_spec COLIN_FAKE_CODEX_MARKER=%q %q -test.run=TestHelperProcessFakeCodex --",
		markerPath,
		os.Args[0],
	)
	workflow := fmt.Sprintf(`---
tracker:
  kind: linear
  endpoint: %q
  api_key: test-linear-key
  project_slug: test-project
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
polling:
  interval_ms: 100
workspace:
  root: %q
agent:
  max_concurrent_agents: 1
  max_turns: 1
  max_retry_backoff_ms: 500
codex:
  command: %q
  turn_timeout_ms: 3000
  read_timeout_ms: 1000
  stall_timeout_ms: 3000
server:
  port: 0
---
Work on {{ .issue.identifier }}.
`, server.URL, filepath.Join(tempDir, "workspaces"), command)
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	logger := newLogger(io.Discard, false)
	svc, err := New(context.Background(), logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitFor(t, "fake Codex completion marker", serviceE2EWaitTimeout, func() bool {
		_, err := os.Stat(markerPath)
		return err == nil
	})
	waitFor(t, "Linear issue moved to Refine", serviceE2EWaitTimeout, func() bool {
		return linear.IssueState("COLIN-93") == "Refine"
	})
	waitFor(t, "needs-spec progress comment", serviceE2EWaitTimeout, func() bool {
		for _, comment := range linear.CommentsForIssue("COLIN-93") {
			if strings.Contains(comment.Body, "The spec should be improved before implementation.") {
				return true
			}
		}
		return false
	})
	if got := linear.IssueState("COLIN-93"); got == "Review" {
		t.Fatalf("issue state = %q, want Refine", got)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop after cancellation")
	}
}

func TestServiceSkipsPausedIssueUntilLabelRemoved(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "codex.marker")
	requestLogPath := filepath.Join(tempDir, "codex.requests.jsonl")

	linear := newFakeLinearServerWithIssues(markerPath, fakeLinearIssue{
		ID:          "issue-paused",
		Identifier:  "COLIN-220",
		Title:       "Paused e2e issue",
		Description: "Verify paused issue gating",
		ProjectSlug: "test-project",
		BranchName:  "colin-220",
		URL:         "https://linear.example/COLIN-220",
		State:       "Todo",
		Labels:      []string{"e2e", domain.PausedIssueLabel},
	})
	server := httptest.NewServer(linear)
	defer server.Close()

	workflowPath := filepath.Join(tempDir, "WORKFLOW.md")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_MARKER=%q COLIN_FAKE_CODEX_REQUEST_LOG=%q %q -test.run=TestHelperProcessFakeCodex --",
		markerPath,
		requestLogPath,
		os.Args[0],
	)
	workflow := fmt.Sprintf(`---
tracker:
  kind: linear
  endpoint: %q
  api_key: test-linear-key
  project_slug: test-project
  active_states:
    - Todo
  terminal_states:
    - Done
polling:
  interval_ms: 100
workspace:
  root: %q
agent:
  max_concurrent_agents: 1
  max_turns: 1
  max_retry_backoff_ms: 500
codex:
  command: %q
  turn_timeout_ms: 3000
  read_timeout_ms: 1000
  stall_timeout_ms: 3000
server:
  port: 0
---
Work on {{ .issue.identifier }}.
`, server.URL, filepath.Join(tempDir, "workspaces"), command)
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	logger := newLogger(io.Discard, false)
	svc, err := New(context.Background(), logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitFor(t, "paused candidate fetches", serviceE2EWaitTimeout, func() bool {
		return linear.CandidateFetches() >= 2
	})
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("fake Codex marker exists while issue is paused")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat fake Codex marker: %v", err)
	}
	if _, err := os.Stat(requestLogPath); err == nil {
		t.Fatal("fake Codex request log exists while issue is paused")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat fake Codex request log: %v", err)
	}

	linear.SetIssueLabels("COLIN-220", []string{"e2e"})
	waitFor(t, "fake Codex marker after unpausing", serviceE2EWaitTimeout, func() bool {
		_, err := os.Stat(markerPath)
		return err == nil
	})

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop after cancellation")
	}
}

func TestServiceWaitsForBlockedIssueUntilBlockerCompletes(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "codex.marker")
	cwdLogPath := filepath.Join(tempDir, "codex.cwd.log")

	linear := newFakeLinearServerWithIssues(markerPath,
		fakeLinearIssue{
			ID:          "issue-blocker",
			Identifier:  "COLIN-219",
			Title:       "Blocker e2e issue",
			Description: "Blocks the follow-up issue",
			ProjectSlug: "test-project",
			BranchName:  "colin-219",
			URL:         "https://linear.example/COLIN-219",
			State:       "In Progress",
			Labels:      []string{"e2e"},
		},
		fakeLinearIssue{
			ID:          "issue-blocked",
			Identifier:  "COLIN-220",
			Title:       "Blocked e2e issue",
			Description: "Verify blocker gating",
			ProjectSlug: "test-project",
			BranchName:  "colin-220",
			URL:         "https://linear.example/COLIN-220",
			State:       "Todo",
			Labels:      []string{"e2e"},
			BlockedBy: []fakeLinearBlocker{{
				ID:         "issue-blocker",
				Identifier: "COLIN-219",
				State:      "In Progress",
			}},
		},
	)
	server := httptest.NewServer(linear)
	defer server.Close()

	workflowPath := filepath.Join(tempDir, "WORKFLOW.md")
	workspaceRoot := filepath.Join(tempDir, "workspaces")
	command := fmt.Sprintf(
		"env COLIN_FAKE_CODEX=1 COLIN_FAKE_CODEX_MARKER=%q COLIN_FAKE_CODEX_CWD_LOG=%q %q -test.run=TestHelperProcessFakeCodex --",
		markerPath,
		cwdLogPath,
		os.Args[0],
	)
	workflow := fmt.Sprintf(`---
tracker:
  kind: linear
  endpoint: %q
  api_key: test-linear-key
  project_slug: test-project
  active_states:
    - Todo
  terminal_states:
    - Done
polling:
  interval_ms: 100
workspace:
  root: %q
agent:
  max_concurrent_agents: 1
  max_turns: 1
  max_retry_backoff_ms: 500
codex:
  command: %q
  turn_timeout_ms: 3000
  read_timeout_ms: 1000
  stall_timeout_ms: 3000
server:
  port: 0
---
Work on {{ .issue.identifier }}.
`, server.URL, workspaceRoot, command)
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	logger := newLogger(io.Discard, false)
	svc, err := New(context.Background(), logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitFor(t, "blocked candidate fetches", serviceE2EWaitTimeout, func() bool {
		return linear.CandidateFetches() >= 2
	})
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("fake Codex marker exists while issue is blocked")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat fake Codex marker: %v", err)
	}
	if _, err := os.Stat(cwdLogPath); err == nil {
		t.Fatal("fake Codex cwd log exists while issue is blocked")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat fake Codex cwd log: %v", err)
	}

	linear.SetIssueState("COLIN-219", "Done")
	waitFor(t, "fake Codex marker after blocker completes", serviceE2EWaitTimeout, func() bool {
		_, err := os.Stat(markerPath)
		return err == nil
	})
	gotCWD, err := os.ReadFile(cwdLogPath)
	if err != nil {
		t.Fatalf("read cwd log: %v", err)
	}
	if !strings.Contains(string(gotCWD), filepath.Join(workspaceRoot, "test-project", "COLIN-220")) {
		t.Fatalf("fake Codex cwd log = %q, want blocked issue workspace", string(gotCWD))
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop after cancellation")
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

func runFakeCodex() error {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

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
		if method == "thread/start" || method == "turn/start" {
			if err := appendFakeCodexRequestLog(msg); err != nil {
				return err
			}
		}
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
					"thread": map[string]any{
						"id": threadID,
					},
				},
			}); err != nil {
				return err
			}
			if err := assertAbsoluteCWD(msg, "thread/start"); err != nil {
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
			if err := assertAbsoluteCWD(msg, "turn/start"); err != nil {
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
					"cwd":      cwd,
				},
			}); err != nil {
				return err
			}

			approval, err := readJSONMessage(reader)
			if err != nil {
				return err
			}
			if approvalID, _ := approval["id"].(string); approvalID != "approval-1" {
				return fmt.Errorf("approval response id = %v, want approval-1", approval["id"])
			}
			result, _ := approval["result"].(map[string]any)
			if decision, _ := result["decision"].(string); decision != "accept" {
				return fmt.Errorf("approval decision = %q, want accept", decision)
			}

			if cwdLog := os.Getenv("COLIN_FAKE_CODEX_CWD_LOG"); cwdLog != "" {
				file, err := os.OpenFile(cwdLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(file, cwd); err != nil {
					_ = file.Close()
					return err
				}
				if err := file.Close(); err != nil {
					return err
				}
			}
			if delay, ok := os.LookupEnv("COLIN_FAKE_CODEX_DELAY_MS"); ok {
				duration, err := time.ParseDuration(delay + "ms")
				if err != nil {
					return err
				}
				time.Sleep(duration)
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
						"text": fakeCodexFinalText(),
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
			if markerPath := os.Getenv("COLIN_FAKE_CODEX_MARKER"); markerPath != "" {
				if err := os.WriteFile(markerPath, []byte("ran\n"), 0o644); err != nil {
					return err
				}
			}
		}
	}
}

func fakeCodexFinalText() string {
	if text := os.Getenv("COLIN_FAKE_CODEX_FINAL_TEXT"); text != "" {
		return text
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COLIN_FAKE_CODEX_OUTCOME"))) {
	case "", "ready_for_review":
		return domain.OutcomeReadyForReviewLine + "\n\nImplemented the requested change."
	case "needs_spec":
		return domain.OutcomeNeedsSpecLine + "\n\nThe spec should be improved before implementation."
	default:
		return domain.OutcomeReadyForReviewLine + "\n\nImplemented the requested change."
	}
}

func appendFakeCodexRequestLog(msg map[string]any) error {
	path := os.Getenv("COLIN_FAKE_CODEX_REQUEST_LOG")
	if path == "" {
		return nil
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func assertAbsoluteCWD(msg map[string]any, method string) error {
	params, _ := msg["params"].(map[string]any)
	cwd, _ := params["cwd"].(string)
	if cwd == "" {
		return fmt.Errorf("%s missing cwd", method)
	}
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("%s cwd = %q, want absolute path", method, cwd)
	}
	return nil
}

func readFakeCodexRequests(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := nonEmptyLines(string(data))
	requests := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var request map[string]any
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, nil
}

func fakeCodexRequestByMethod(requests []map[string]any, method string) map[string]any {
	for _, request := range requests {
		if got, _ := request["method"].(string); got == method {
			return request
		}
	}
	return nil
}

func readJSONMessage(reader *bufio.Reader) (map[string]any, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &msg); err != nil {
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

func waitFor(t *testing.T, label string, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s condition not met before timeout", label)
}

func nonEmptyLines(value string) []string {
	raw := strings.Split(value, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func newServiceE2EGitOrigin(t *testing.T) string {
	t.Helper()

	origin := filepath.Join(t.TempDir(), "origin")
	runServiceE2EGit(t, "", "init", "-b", "main", origin)
	runServiceE2EGit(t, origin, "config", "user.email", "test@example.com")
	runServiceE2EGit(t, origin, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runServiceE2EGit(t, origin, "add", "README.md")
	runServiceE2EGit(t, origin, "commit", "-m", "init")
	return origin
}

func runServiceE2EGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func fetchBufferedLogs(url string) (domain.BufferedLogSnapshot, error) {
	resp, err := http.Get(url)
	if err != nil {
		return domain.BufferedLogSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return domain.BufferedLogSnapshot{}, fmt.Errorf("status = %d body = %s", resp.StatusCode, string(body))
	}
	var snapshot domain.BufferedLogSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return domain.BufferedLogSnapshot{}, err
	}
	return snapshot, nil
}

func containsBufferedLog(snapshot domain.BufferedLogSnapshot, message string) bool {
	for _, entry := range snapshot.Entries {
		if entry.Message == message {
			return true
		}
	}
	return false
}

type fakeLinearServer struct {
	mu               sync.Mutex
	markerPath       string
	authHeader       string
	candidateFetches int
	stateFetches     int
	issues           map[string]*fakeLinearIssue
	issueOrder       []string
	comments         []fakeLinearComment
	metadata         map[string]map[string]any
	attachmentIssue  map[string]string
	labels           map[string]string
	labelNames       map[string]string
	nextCommentID    int
	nextLabelID      int
}

type fakeLinearIssue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	ProjectID   string
	ProjectSlug string
	BranchName  string
	URL         string
	State       string
	Priority    int
	Labels      []string
	BlockedBy   []fakeLinearBlocker
}

type fakeLinearBlocker struct {
	ID         string
	Identifier string
	State      string
}

func newFakeLinearServer(markerPath string) *fakeLinearServer {
	return newFakeLinearServerWithIssues(markerPath, fakeLinearIssue{
		ID:          "issue-1",
		Identifier:  "COLIN-93",
		Title:       "SDK integration e2e",
		Description: "Verify Colin end-to-end",
		ProjectSlug: "test-project",
		BranchName:  "colin-93",
		URL:         "https://linear.example/COLIN-93",
		State:       "Todo",
		Priority:    1,
		Labels:      []string{"e2e"},
	})
}

func newFakeLinearServerWithIssues(markerPath string, issues ...fakeLinearIssue) *fakeLinearServer {
	server := &fakeLinearServer{
		markerPath:      markerPath,
		issues:          map[string]*fakeLinearIssue{},
		metadata:        map[string]map[string]any{},
		attachmentIssue: map[string]string{},
		labels:          map[string]string{},
		labelNames:      map[string]string{},
		nextCommentID:   1,
		nextLabelID:     1,
	}
	for _, issue := range issues {
		normalized := normalizeFakeLinearIssue(issue)
		server.issues[normalized.ID] = &normalized
		server.issueOrder = append(server.issueOrder, normalized.ID)
		for _, label := range normalized.Labels {
			server.ensureFakeLabelLocked(label)
		}
	}
	return server
}

func normalizeFakeLinearIssue(issue fakeLinearIssue) fakeLinearIssue {
	if strings.TrimSpace(issue.ID) == "" {
		issue.ID = "issue-1"
	}
	if strings.TrimSpace(issue.Identifier) == "" {
		issue.Identifier = issue.ID
	}
	if strings.TrimSpace(issue.Title) == "" {
		issue.Title = issue.Identifier
	}
	if strings.TrimSpace(issue.Description) == "" {
		issue.Description = "Verify Colin end-to-end"
	}
	if strings.TrimSpace(issue.ProjectID) == "" {
		issue.ProjectID = "project-1"
	}
	if strings.TrimSpace(issue.ProjectSlug) == "" {
		issue.ProjectSlug = "test-project"
	}
	if strings.TrimSpace(issue.BranchName) == "" {
		issue.BranchName = strings.ToLower(issue.Identifier)
	}
	if strings.TrimSpace(issue.URL) == "" {
		issue.URL = "https://linear.example/" + issue.Identifier
	}
	if strings.TrimSpace(issue.State) == "" {
		issue.State = "Todo"
	}
	if issue.Priority == 0 {
		issue.Priority = 1
	}
	if len(issue.Labels) == 0 {
		issue.Labels = []string{"e2e"}
	}
	return issue
}

func (s *fakeLinearServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	s.mu.Lock()
	s.authHeader = r.Header.Get("Authorization")
	s.mu.Unlock()

	var request struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch {
	case strings.Contains(request.Query, "issueLabels"):
		name, _ := request.Variables["name"].(string)
		s.mu.Lock()
		labelID := s.labels[strings.ToLower(strings.TrimSpace(name))]
		s.mu.Unlock()
		nodes := []map[string]any{}
		if strings.TrimSpace(labelID) != "" {
			nodes = append(nodes, map[string]any{
				"id":   labelID,
				"name": name,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueLabels": map[string]any{
					"nodes": nodes,
				},
			},
		})
	case strings.Contains(request.Query, "issueLabelCreate"):
		input, _ := request.Variables["input"].(map[string]any)
		name, _ := input["name"].(string)
		s.mu.Lock()
		labelID := s.ensureFakeLabelLocked(name)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueLabelCreate": map[string]any{
					"success": true,
					"issueLabel": map[string]any{
						"id":   labelID,
						"name": name,
					},
				},
			},
		})
	case strings.Contains(request.Query, "issueAddLabel"):
		issueID, _ := request.Variables["id"].(string)
		labelID, _ := request.Variables["labelId"].(string)
		s.mu.Lock()
		if issue := s.issues[issueID]; issue != nil {
			if labelName := s.labelNames[labelID]; labelName != "" {
				issue.Labels = appendMissingLabel(issue.Labels, labelName)
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueAddLabel": map[string]any{
					"success": true,
					"issue": map[string]any{
						"id": issueID,
					},
				},
			},
		})
	case strings.Contains(request.Query, "issueRemoveLabel"):
		issueID, _ := request.Variables["id"].(string)
		labelID, _ := request.Variables["labelId"].(string)
		s.mu.Lock()
		if issue := s.issues[issueID]; issue != nil {
			if labelName := s.labelNames[labelID]; labelName != "" {
				issue.Labels = removeLabel(issue.Labels, labelName)
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueRemoveLabel": map[string]any{
					"success": true,
					"issue": map[string]any{
						"id": issueID,
					},
				},
			},
		})
	case strings.Contains(request.Query, "ProjectTeamStates"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"projects": map[string]any{
					"nodes": []map[string]any{
						{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{
									{
										"id":   "team-1",
										"name": "Colin",
										"states": map[string]any{
											"nodes": fakeLinearWorkflowStates(),
										},
									},
								},
							},
						},
					},
				},
			},
		})
	case strings.Contains(request.Query, "commentCreate"):
		commentID := s.recordComment(request.Variables)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"commentCreate": map[string]any{
					"success": true,
					"comment": map[string]any{
						"id": commentID,
					},
				},
			},
		})
	case strings.Contains(request.Query, "attachmentCreate"):
		input, _ := request.Variables["input"].(map[string]any)
		issueID, _ := input["issueId"].(string)
		metadata, _ := input["metadata"].(map[string]any)
		attachmentID := "attachment-" + strings.TrimSpace(issueID)
		if attachmentID == "attachment-" {
			attachmentID = "attachment-1"
			issueID = s.firstIssueID()
		}
		s.mu.Lock()
		s.metadata[issueID] = metadata
		s.attachmentIssue[attachmentID] = issueID
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"attachmentCreate": map[string]any{
					"success": true,
					"attachment": map[string]any{
						"id":       attachmentID,
						"title":    "Colin metadata",
						"url":      "http://127.0.0.1:8888/linear/issues/" + issueID + "/metadata",
						"metadata": metadata,
					},
				},
			},
		})
	case strings.Contains(request.Query, "IssueSchedulingMetadata"):
		ids := stringSlice(request.Variables["ids"])
		nodes := []map[string]any{}
		s.mu.Lock()
		for _, issueID := range ids {
			if _, ok := s.issues[issueID]; ok {
				nodes = append(nodes, s.issueMetadataNodeLocked(issueID))
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": nodes,
				},
			},
		})
	case strings.Contains(request.Query, "IssueMetadataAttachments"):
		issueID, _ := request.Variables["id"].(string)
		nodes := s.metadataAttachmentNodes(issueID)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"attachments": map[string]any{
						"nodes": nodes,
					},
				},
			},
		})
	case strings.Contains(request.Query, "attachmentUpdate"):
		attachmentID, _ := request.Variables["id"].(string)
		input, _ := request.Variables["input"].(map[string]any)
		metadata, _ := input["metadata"].(map[string]any)
		s.mu.Lock()
		issueID := s.attachmentIssue[attachmentID]
		if issueID == "" {
			issueID = s.firstIssueIDLocked()
			s.attachmentIssue[attachmentID] = issueID
		}
		s.metadata[issueID] = metadata
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"attachmentUpdate": map[string]any{
					"success": true,
					"attachment": map[string]any{
						"id":       attachmentID,
						"title":    "Colin metadata",
						"url":      "http://127.0.0.1:8888/linear/issues/" + issueID + "/metadata",
						"metadata": metadata,
					},
				},
			},
		})
	case strings.Contains(request.Query, "IssueTeamStates"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"team": map[string]any{
						"states": map[string]any{
							"nodes": fakeLinearWorkflowStates(),
						},
					},
				},
			},
		})
	case strings.Contains(request.Query, "issueUpdate"):
		issueID, _ := request.Variables["id"].(string)
		inputStateID, _ := request.Variables["stateId"].(string)
		nextState := fakeLinearStateName(inputStateID)
		if nextState == "" {
			http.Error(w, "unknown state id", http.StatusBadRequest)
			return
		}
		s.SetIssueStateByID(issueID, nextState)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueUpdate": map[string]any{
					"success": true,
					"issue": map[string]any{
						"id": issueID,
						"state": map[string]any{
							"name": nextState,
						},
					},
				},
			},
		})
	case strings.Contains(request.Query, "IssueStates"):
		ids := stringSlice(request.Variables["ids"])
		s.mu.Lock()
		s.stateFetches++
		nodes := make([]map[string]any, 0, len(ids))
		for _, issueID := range ids {
			if issue := s.issues[issueID]; issue != nil {
				nodes = append(nodes, s.issueSnapshotNodeLocked(*issue))
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": nodes,
				},
			},
		})
	case strings.Contains(request.Query, "IssueByID"):
		issueID, _ := request.Variables["id"].(string)
		issue := s.issueByID(issueID)
		if issue == nil {
			http.Error(w, "unknown issue", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": s.issueDetailNode(*issue),
			},
		})
	case strings.Contains(request.Query, "CandidateIssueSnapshots"):
		s.mu.Lock()
		s.candidateFetches++
		requestedStates, _ := request.Variables["states"].([]any)
		nodes := []map[string]any{}
		for _, issueID := range s.issueOrder {
			issue := s.issues[issueID]
			if issue != nil && stateAllowed(requestedStates, issue.State) {
				nodes = append(nodes, s.issueSnapshotNodeLocked(*issue))
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": nodes,
				},
			},
		})
	default:
		http.Error(w, "unknown query", http.StatusBadRequest)
	}
}

func (s *fakeLinearServer) IssueState(identifier string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue := s.issueByIdentifierLocked(identifier); issue != nil {
		return issue.State
	}
	return ""
}

func (s *fakeLinearServer) SetIssueState(identifier string, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue := s.issueByIdentifierLocked(identifier); issue != nil {
		issue.State = state
	}
	for _, issue := range s.issues {
		for i := range issue.BlockedBy {
			if issue.BlockedBy[i].Identifier == identifier || issue.BlockedBy[i].ID == identifier {
				issue.BlockedBy[i].State = state
			}
		}
	}
}

func (s *fakeLinearServer) SetIssueStateByID(issueID string, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue := s.issues[issueID]; issue != nil {
		issue.State = state
	}
	for _, issue := range s.issues {
		for i := range issue.BlockedBy {
			if issue.BlockedBy[i].ID == issueID {
				issue.BlockedBy[i].State = state
			}
		}
	}
}

func (s *fakeLinearServer) SetIssueLabels(identifier string, labels []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue := s.issueByIdentifierLocked(identifier); issue != nil {
		issue.Labels = append([]string(nil), labels...)
		for _, label := range labels {
			s.ensureFakeLabelLocked(label)
		}
	}
}

func (s *fakeLinearServer) CommentsForIssue(identifier string) []fakeLinearComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	issue := s.issueByIdentifierLocked(identifier)
	if issue == nil {
		return nil
	}
	out := []fakeLinearComment{}
	for _, comment := range s.comments {
		if comment.IssueID == issue.ID {
			out = append(out, comment)
		}
	}
	return out
}

func (s *fakeLinearServer) issueSnapshotNodeLocked(issue fakeLinearIssue) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	return map[string]any{
		"id":         issue.ID,
		"identifier": issue.Identifier,
		"title":      issue.Title,
		"priority":   issue.Priority,
		"project": map[string]any{
			"id":     issue.ProjectID,
			"slugId": issue.ProjectSlug,
		},
		"branchName": issue.BranchName,
		"url":        issue.URL,
		"createdAt":  now,
		"updatedAt":  now,
		"state": map[string]any{
			"name": issue.State,
		},
		"labels": map[string]any{
			"nodes": s.labelNodesLocked(issue.Labels),
		},
		"inverseRelations": s.inverseRelationsLocked(issue),
	}
}

func (s *fakeLinearServer) issueDetailNode(issue fakeLinearIssue) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	return map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": issue.Description,
		"priority":    issue.Priority,
		"project": map[string]any{
			"id":     issue.ProjectID,
			"slugId": issue.ProjectSlug,
		},
		"branchName": issue.BranchName,
		"url":        issue.URL,
		"createdAt":  now,
		"updatedAt":  now,
		"state": map[string]any{
			"name": issue.State,
		},
		"labels": map[string]any{
			"nodes": s.labelNodes(issue.Labels),
		},
		"inverseRelations": s.inverseRelations(issue),
		"attachments": map[string]any{
			"nodes": s.metadataAttachmentNodes(issue.ID),
		},
		"comments": map[string]any{
			"nodes": []map[string]any{},
		},
		"history": map[string]any{
			"nodes": []map[string]any{},
		},
	}
}

func (s *fakeLinearServer) issueMetadataNodeLocked(issueID string) map[string]any {
	return map[string]any{
		"id": issueID,
		"attachments": map[string]any{
			"nodes": s.metadataAttachmentNodesLocked(issueID),
		},
	}
}

func (s *fakeLinearServer) metadataAttachmentNodes(issueID string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadataAttachmentNodesLocked(issueID)
}

func (s *fakeLinearServer) metadataAttachmentNodesLocked(issueID string) []map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	attachments := []map[string]any{}
	metadata := s.metadata[issueID]
	if metadata != nil {
		attachments = append(attachments, map[string]any{
			"id":        "attachment-" + issueID,
			"title":     "Colin metadata",
			"url":       "http://127.0.0.1:8888/linear/issues/" + issueID + "/metadata",
			"createdAt": now,
			"updatedAt": now,
			"metadata":  metadata,
		})
	}
	return attachments
}

func (s *fakeLinearServer) issueByID(issueID string) *fakeLinearIssue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue := s.issues[issueID]; issue != nil {
		copy := *issue
		copy.Labels = append([]string(nil), issue.Labels...)
		copy.BlockedBy = append([]fakeLinearBlocker(nil), issue.BlockedBy...)
		return &copy
	}
	return nil
}

func (s *fakeLinearServer) issueByIdentifierLocked(identifier string) *fakeLinearIssue {
	for _, issue := range s.issues {
		if issue.Identifier == identifier || issue.ID == identifier {
			return issue
		}
	}
	return nil
}

func (s *fakeLinearServer) firstIssueID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstIssueIDLocked()
}

func (s *fakeLinearServer) firstIssueIDLocked() string {
	if len(s.issueOrder) == 0 {
		return ""
	}
	return s.issueOrder[0]
}

func (s *fakeLinearServer) ensureFakeLabelLocked(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return ""
	}
	if labelID := s.labels[key]; labelID != "" {
		return labelID
	}
	labelID := fmt.Sprintf("label-%d", s.nextLabelID)
	s.nextLabelID++
	s.labels[key] = labelID
	s.labelNames[labelID] = name
	return labelID
}

func (s *fakeLinearServer) labelNodes(labels []string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.labelNodesLocked(labels)
}

func (s *fakeLinearServer) labelNodesLocked(labels []string) []map[string]any {
	nodes := make([]map[string]any, 0, len(labels))
	for _, label := range labels {
		nodes = append(nodes, map[string]any{"name": label})
	}
	return nodes
}

func (s *fakeLinearServer) inverseRelations(issue fakeLinearIssue) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inverseRelationsLocked(issue)
}

func (s *fakeLinearServer) inverseRelationsLocked(issue fakeLinearIssue) map[string]any {
	nodes := make([]map[string]any, 0, len(issue.BlockedBy))
	for _, blocker := range issue.BlockedBy {
		state := blocker.State
		if blocker.ID != "" {
			if blockingIssue := s.issues[blocker.ID]; blockingIssue != nil {
				state = blockingIssue.State
			}
		}
		nodes = append(nodes, map[string]any{
			"type": "blocks",
			"issue": map[string]any{
				"id":         blocker.ID,
				"identifier": blocker.Identifier,
				"state": map[string]any{
					"name": state,
				},
			},
		})
	}
	return map[string]any{"nodes": nodes}
}

func (s *fakeLinearServer) AuthHeader() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authHeader
}

func (s *fakeLinearServer) CandidateFetches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.candidateFetches
}

func (s *fakeLinearServer) StateFetches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateFetches
}

func (s *fakeLinearServer) CommentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.comments)
}

func (s *fakeLinearServer) ReplyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, comment := range s.comments {
		if comment.ParentID != "" {
			count++
		}
	}
	return count
}

func (s *fakeLinearServer) RootComment() *fakeLinearComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, comment := range s.comments {
		if comment.ParentID == "" {
			copy := comment
			return &copy
		}
	}
	return nil
}

func (s *fakeLinearServer) Comments() []fakeLinearComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fakeLinearComment, len(s.comments))
	copy(out, s.comments)
	return out
}

func (s *fakeLinearServer) recordComment(variables map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	input, _ := variables["input"].(map[string]any)
	commentID := fmt.Sprintf("comment-%d", s.nextCommentID)
	s.nextCommentID++
	comment := fakeLinearComment{ID: commentID}
	if issueID, _ := input["issueId"].(string); issueID != "" {
		comment.IssueID = issueID
	}
	if parentID, _ := input["parentId"].(string); parentID != "" {
		comment.ParentID = parentID
	}
	if body, _ := input["body"].(string); body != "" {
		comment.Body = body
	}
	s.comments = append(s.comments, comment)
	return commentID
}

type fakeLinearComment struct {
	ID       string
	IssueID  string
	ParentID string
	Body     string
}

func stateAllowed(requested []any, want string) bool {
	for _, value := range requested {
		if state, ok := value.(string); ok && state == want {
			return true
		}
	}
	return false
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok {
				out = append(out, value)
			}
		}
		return out
	default:
		return nil
	}
}

func appendMissingLabel(labels []string, label string) []string {
	key := strings.ToLower(strings.TrimSpace(label))
	if key == "" {
		return labels
	}
	for _, existing := range labels {
		if strings.ToLower(strings.TrimSpace(existing)) == key {
			return labels
		}
	}
	return append(labels, label)
}

func removeLabel(labels []string, label string) []string {
	key := strings.ToLower(strings.TrimSpace(label))
	out := labels[:0]
	for _, existing := range labels {
		if strings.ToLower(strings.TrimSpace(existing)) == key {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func fakeLinearStateName(stateID string) string {
	switch stateID {
	case "state-todo":
		return "Todo"
	case "state-in-progress":
		return "In Progress"
	case "state-refine":
		return "Refine"
	case "state-review":
		return "Review"
	case "state-merge":
		return "Merge"
	case "state-done":
		return "Done"
	case "state-merged":
		return "Merged"
	case "state-closed":
		return "Closed"
	case "state-cancelled":
		return "Cancelled"
	case "state-canceled":
		return "Canceled"
	case "state-duplicate":
		return "Duplicate"
	default:
		return ""
	}
}

func fakeLinearWorkflowStates() []map[string]any {
	return []map[string]any{
		{"id": "state-todo", "name": "Todo"},
		{"id": "state-in-progress", "name": "In Progress"},
		{"id": "state-refine", "name": "Refine"},
		{"id": "state-review", "name": "Review"},
		{"id": "state-merge", "name": "Merge"},
		{"id": "state-done", "name": "Done"},
		{"id": "state-merged", "name": "Merged"},
		{"id": "state-closed", "name": "Closed"},
		{"id": "state-cancelled", "name": "Cancelled"},
		{"id": "state-canceled", "name": "Canceled"},
		{"id": "state-duplicate", "name": "Duplicate"},
	}
}
