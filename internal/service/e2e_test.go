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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := New(logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitFor(t, 5*time.Second, func() bool {
		if _, err := os.Stat(markerPath); err != nil {
			return false
		}
		return linear.StateFetches() > 0
	})

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop after cancellation")
	}

	workspacePath := filepath.Join(workspaceRoot, "COLIN-93")
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
	if !strings.Contains(root.Body, "Workspace: `"+workspacePath+"`") {
		t.Fatalf("root comment body = %q, want workspace path", root.Body)
	}
	if !strings.Contains(root.Body, "Session ID: `thread-1-turn-1`") {
		t.Fatalf("root comment body = %q, want session id", root.Body)
	}
	if linear.ReplyCount() == 0 {
		t.Fatal("expected Linear progress replies")
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
	svc, err := New(logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitFor(t, 5*time.Second, func() bool {
		return svc.DashboardURL() != ""
	})

	waitFor(t, 5*time.Second, func() bool {
		resp, err := http.Get(svc.DashboardURL() + "/api/v1/state")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(body), `"running":1`)
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

			if markerPath := os.Getenv("COLIN_FAKE_CODEX_MARKER"); markerPath != "" {
				if err := os.WriteFile(markerPath, []byte("ran\n"), 0o644); err != nil {
					return err
				}
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

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
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

type fakeLinearServer struct {
	mu               sync.Mutex
	markerPath       string
	authHeader       string
	candidateFetches int
	stateFetches     int
	comments         []fakeLinearComment
	nextCommentID    int
}

func newFakeLinearServer(markerPath string) *fakeLinearServer {
	return &fakeLinearServer{markerPath: markerPath, nextCommentID: 1}
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
	case strings.Contains(request.Query, "IssueStates"):
		s.mu.Lock()
		s.stateFetches++
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": []map[string]any{s.issueNode(s.currentState())},
				},
			},
		})
	case strings.Contains(request.Query, "CandidateIssues"):
		s.mu.Lock()
		s.candidateFetches++
		s.mu.Unlock()

		state := s.currentState()
		nodes := []map[string]any{}
		if requestedStates, ok := request.Variables["states"].([]any); ok && stateAllowed(requestedStates, state) {
			nodes = append(nodes, s.issueNode(state))
		}
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

func (s *fakeLinearServer) currentState() string {
	if _, err := os.Stat(s.markerPath); err == nil {
		return "Done"
	}
	return "Todo"
}

func (s *fakeLinearServer) issueNode(state string) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	return map[string]any{
		"id":          "issue-1",
		"identifier":  "COLIN-93",
		"title":       "SDK integration e2e",
		"description": "Verify Colin end-to-end",
		"priority":    1,
		"branchName":  "colin-93",
		"url":         "https://linear.example/COLIN-93",
		"createdAt":   now,
		"updatedAt":   now,
		"state": map[string]any{
			"name": state,
		},
		"labels": map[string]any{
			"nodes": []map[string]any{{"name": "e2e"}},
		},
		"inverseRelations": map[string]any{
			"nodes": []map[string]any{},
		},
	}
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
