package codexexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/codex-sdk-go/rpc"
	"github.com/pmenglund/colin/internal/execution"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
)

type fakeThread struct {
	id             string
	turnResult     *codex.TurnResult
	runErr         error
	streamNotes    []rpc.Notification
	streamErr      error
	lastInputs     []codex.Input
	lastTurnOpts   *codex.TurnOptions
	startThreadErr error
}

func (f *fakeThread) ID() string { return f.id }

func (f *fakeThread) RunInputs(_ context.Context, inputs []codex.Input, opts *codex.TurnOptions) (*codex.TurnResult, error) {
	f.lastInputs = append([]codex.Input(nil), inputs...)
	f.lastTurnOpts = opts
	if f.runErr != nil {
		return nil, f.runErr
	}
	return f.turnResult, nil
}

func (f *fakeThread) RunStreamed(_ context.Context, inputs []codex.Input, opts *codex.TurnOptions) (codexTurnStream, error) {
	f.lastInputs = append([]codex.Input(nil), inputs...)
	f.lastTurnOpts = opts
	if f.runErr != nil {
		return nil, f.runErr
	}
	notes := append([]rpc.Notification(nil), f.streamNotes...)
	if len(notes) == 0 && f.turnResult != nil {
		notes = append(notes,
			rpc.Notification{
				Method: "turn/started",
				Raw:    mustJSONRaw(map[string]any{"threadId": f.id, "turn": map[string]any{"id": "turn_1", "status": "running"}}),
			},
			rpc.Notification{
				Method: "item/completed",
				Raw:    mustJSONRaw(map[string]any{"threadId": f.id, "item": map[string]any{"text": f.turnResult.FinalResponse}}),
			},
			rpc.Notification{
				Method: "turn/completed",
				Raw:    mustJSONRaw(map[string]any{"threadId": f.id, "turn": map[string]any{"id": "turn_1", "status": "completed"}}),
			},
		)
	}
	return &fakeTurnStream{notes: notes, err: f.streamErr}, nil
}

type fakeTurnStream struct {
	notes []rpc.Notification
	err   error
}

func (f *fakeTurnStream) Next(context.Context) (rpc.Notification, error) {
	if len(f.notes) == 0 {
		if f.err != nil {
			return rpc.Notification{}, f.err
		}
		return rpc.Notification{}, io.EOF
	}
	note := f.notes[0]
	f.notes = f.notes[1:]
	return note, nil
}

func (f *fakeTurnStream) Close() {}

func mustJSONRaw(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

type fakeClient struct {
	thread               *fakeThread
	resumeThread         *fakeThread
	closed               bool
	startErr             error
	resumeErr            error
	startCalls           int
	resumeCalls          int
	lastStartThreadOpts  *codex.ThreadStartOptions
	lastResumeThreadOpts *codex.ThreadResumeOptions
}

func (f *fakeClient) StartThread(_ context.Context, opts codex.ThreadStartOptions) (codexThread, error) {
	optsCopy := opts
	f.startCalls++
	f.lastStartThreadOpts = &optsCopy
	if f.startErr != nil {
		return nil, f.startErr
	}
	return f.thread, nil
}

func (f *fakeClient) ResumeThread(_ context.Context, opts codex.ThreadResumeOptions) (codexThread, error) {
	optsCopy := opts
	f.resumeCalls++
	f.lastResumeThreadOpts = &optsCopy
	if f.resumeErr != nil {
		return nil, f.resumeErr
	}
	if f.resumeThread != nil {
		return f.resumeThread, nil
	}
	return f.thread, nil
}

func (f *fakeClient) Close() error {
	f.closed = true
	return nil
}

func TestExecutorEvaluateAndExecuteNotWellSpecified(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_1",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":false,"needs_input_summary":"Need acceptance criteria","execution_summary":"","before_evidence_ref":"","after_evidence_ref":""}`},
	}
	client := &fakeClient{thread: thread}

	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	issue := linear.Issue{
		ID:          "1",
		Identifier:  "COLIN-1",
		Title:       "Test",
		Description: "<!-- colin:metadata {\"k\":\"v\"} -->",
		Metadata: map[string]string{
			workflow.MetaWorktreePath: "/tmp/colin/worktrees/COLIN-1",
		},
	}

	result, err := executor.EvaluateAndExecute(context.Background(), issue)
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}
	if result.IsWellSpecified {
		t.Fatalf("IsWellSpecified = %t, want false", result.IsWellSpecified)
	}
	if result.NeedsInputSummary != "Need acceptance criteria" {
		t.Fatalf("NeedsInputSummary = %q", result.NeedsInputSummary)
	}
	if result.ThreadID != "thr_1" {
		t.Fatalf("ThreadID = %q", result.ThreadID)
	}
	if !strings.Contains(result.ExecutionContext, "Issue identifier: COLIN-1") {
		t.Fatalf("ExecutionContext missing issue identifier: %q", result.ExecutionContext)
	}
	if strings.Contains(result.ExecutionContext, "colin:metadata") {
		t.Fatalf("ExecutionContext should strip metadata block, got %q", result.ExecutionContext)
	}
	if !client.closed {
		t.Fatal("expected client.Close() to be called")
	}
	if len(thread.lastInputs) != 1 {
		t.Fatalf("expected exactly one input, got %d", len(thread.lastInputs))
	}
	if client.lastStartThreadOpts == nil {
		t.Fatal("expected start thread options to be set")
	}
	if client.lastStartThreadOpts.Cwd != "/tmp/colin/worktrees/COLIN-1" {
		t.Fatalf("thread start cwd = %q, want %q", client.lastStartThreadOpts.Cwd, "/tmp/colin/worktrees/COLIN-1")
	}
	if thread.lastTurnOpts == nil {
		t.Fatal("expected turn options to be set")
	}
	if thread.lastTurnOpts.Cwd != "/tmp/colin/worktrees/COLIN-1" {
		t.Fatalf("turn cwd = %q, want %q", thread.lastTurnOpts.Cwd, "/tmp/colin/worktrees/COLIN-1")
	}
	text := thread.lastInputs[0].Text
	if strings.Contains(text, "colin:metadata") {
		t.Fatalf("prompt should strip metadata block, got %q", text)
	}
}

func TestExecutorEvaluateAndExecuteWellSpecified(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_2",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Before: old behavior. After: new behavior. How verified: go test ./...","before_evidence_ref":"https://linear.app/example/attachment/COLIN-1-before","after_evidence_ref":"https://linear.app/example/attachment/COLIN-1-after"}`},
	}
	client := &fakeClient{thread: thread}

	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	result, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{Identifier: "COLIN-1", Title: "Title", Description: "Spec"})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}
	if !result.IsWellSpecified {
		t.Fatalf("IsWellSpecified = %t, want true", result.IsWellSpecified)
	}
	if result.ExecutionSummary != "Before: old behavior. After: new behavior. How verified: go test ./..." {
		t.Fatalf("ExecutionSummary = %q", result.ExecutionSummary)
	}
	if !strings.Contains(result.ExecutionContext, "Issue identifier: COLIN-1") {
		t.Fatalf("ExecutionContext missing issue identifier: %q", result.ExecutionContext)
	}
	if result.BeforeEvidenceRef != "https://linear.app/example/attachment/COLIN-1-before" {
		t.Fatalf("BeforeEvidenceRef = %q", result.BeforeEvidenceRef)
	}
	if result.AfterEvidenceRef != "https://linear.app/example/attachment/COLIN-1-after" {
		t.Fatalf("AfterEvidenceRef = %q", result.AfterEvidenceRef)
	}
	if thread.lastTurnOpts == nil {
		t.Fatal("expected turn options to be set")
	}
	if client.lastStartThreadOpts == nil {
		t.Fatal("expected start thread options to be set")
	}
	if client.lastStartThreadOpts.Cwd != "/tmp" {
		t.Fatalf("thread start cwd = %q, want %q", client.lastStartThreadOpts.Cwd, "/tmp")
	}
	if thread.lastTurnOpts.Cwd != "/tmp" {
		t.Fatalf("turn cwd = %q, want %q", thread.lastTurnOpts.Cwd, "/tmp")
	}
	outputSchemaBytes, err := json.Marshal(thread.lastTurnOpts.OutputSchema)
	if err != nil {
		t.Fatalf("marshal output schema: %v", err)
	}
	outputSchema := string(outputSchemaBytes)
	if !strings.Contains(outputSchema, "\"before_evidence_ref\"") {
		t.Fatalf("output schema missing before_evidence_ref: %s", outputSchema)
	}
	if !strings.Contains(outputSchema, "\"after_evidence_ref\"") {
		t.Fatalf("output schema missing after_evidence_ref: %s", outputSchema)
	}
}

func TestExecutorEvaluateAndExecuteStreamedEmitsSessionUpdates(t *testing.T) {
	thread := &fakeThread{
		id: "thr_stream",
		streamNotes: []rpc.Notification{
			{
				Method: "turn/started",
				Raw:    mustJSONRaw(map[string]any{"threadId": "thr_stream", "turn": map[string]any{"id": "turn_stream", "status": "running"}}),
			},
			{
				Method: "thread/tokenUsage/updated",
				Raw: mustJSONRaw(map[string]any{
					"threadId": "thr_stream",
					"turnId":   "turn_stream",
					"tokenUsage": map[string]any{
						"last":  map[string]any{"inputTokens": 3, "outputTokens": 5, "totalTokens": 8},
						"total": map[string]any{"inputTokens": 11, "outputTokens": 13, "totalTokens": 24},
					},
				}),
			},
			{
				Method: "item/completed",
				Raw: mustJSONRaw(map[string]any{
					"threadId": "thr_stream",
					"item":     map[string]any{"text": `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"done","before_evidence_ref":"","after_evidence_ref":""}`},
				}),
			},
			{
				Method: "turn/completed",
				Raw:    mustJSONRaw(map[string]any{"threadId": "thr_stream", "turn": map[string]any{"id": "turn_stream", "status": "completed"}}),
			},
		},
	}
	client := &fakeClient{thread: thread}
	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	var updates []execution.SessionUpdate
	result, err := executor.EvaluateAndExecuteStreamed(context.Background(), linear.Issue{Identifier: "COLIN-1", Title: "Title", Description: "Spec"}, func(update execution.SessionUpdate) {
		updates = append(updates, update)
	})
	if err != nil {
		t.Fatalf("EvaluateAndExecuteStreamed() error = %v", err)
	}
	if !result.IsWellSpecified {
		t.Fatal("expected well specified result")
	}
	if len(updates) != 4 {
		t.Fatalf("session update count = %d, want 4", len(updates))
	}
	var tokenUpdate execution.SessionUpdate
	for _, update := range updates {
		if update.TotalTokens > 0 {
			tokenUpdate = update
			break
		}
	}
	if tokenUpdate.ThreadID != "thr_stream" {
		t.Fatalf("ThreadID = %q, want %q", tokenUpdate.ThreadID, "thr_stream")
	}
	if tokenUpdate.TurnID != "turn_stream" {
		t.Fatalf("TurnID = %q, want %q", tokenUpdate.TurnID, "turn_stream")
	}
	if tokenUpdate.TotalTokens != 24 {
		t.Fatalf("TotalTokens = %d, want 24", tokenUpdate.TotalTokens)
	}
	if tokenUpdate.ReportedTotalTokens != 8 {
		t.Fatalf("ReportedTotalTokens = %d, want 8", tokenUpdate.ReportedTotalTokens)
	}
}

func TestExecutorRunAttemptUsesContinuationPrompt(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_continue",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"done","before_evidence_ref":"","after_evidence_ref":""}`},
	}
	client := &fakeClient{resumeThread: thread}
	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	attempt := 2
	_, err := executor.RunAttempt(context.Background(), execution.AttemptRequest{
		Issue: linear.Issue{
			Identifier: "COLIN-2",
			Metadata: map[string]string{
				workflow.MetaThreadID: "thr_existing",
			},
		},
		Attempt:      &attempt,
		Continuation: true,
	}, nil)
	if err != nil {
		t.Fatalf("RunAttempt() error = %v", err)
	}
	if len(thread.lastInputs) != 1 {
		t.Fatalf("expected one input, got %d", len(thread.lastInputs))
	}
	if !strings.Contains(thread.lastInputs[0].Text, "continuation attempt 2") {
		t.Fatalf("continuation prompt = %q", thread.lastInputs[0].Text)
	}
}

func TestExecutorEvaluateAndExecuteResumesExistingThread(t *testing.T) {
	resumedThread := &fakeThread{
		id:         "thr_resumed",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented","before_evidence_ref":"","after_evidence_ref":""}`},
	}
	client := &fakeClient{
		thread:       &fakeThread{id: "thr_new"},
		resumeThread: resumedThread,
	}
	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	result, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{
		Identifier: "COLIN-201",
		Metadata: map[string]string{
			workflow.MetaThreadID: "thr_existing",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}
	if result.ThreadID != "thr_resumed" {
		t.Fatalf("ThreadID = %q, want %q", result.ThreadID, "thr_resumed")
	}
	if result.ResumedFromThreadID != "thr_existing" {
		t.Fatalf("ResumedFromThreadID = %q, want %q", result.ResumedFromThreadID, "thr_existing")
	}
	if result.ResumeFallbackReason != "" {
		t.Fatalf("ResumeFallbackReason = %q, want empty", result.ResumeFallbackReason)
	}
	if client.resumeCalls != 1 {
		t.Fatalf("resumeCalls = %d, want 1", client.resumeCalls)
	}
	if client.startCalls != 0 {
		t.Fatalf("startCalls = %d, want 0", client.startCalls)
	}
	if client.lastResumeThreadOpts == nil {
		t.Fatal("expected resume thread options to be set")
	}
	if client.lastResumeThreadOpts.ThreadID != "thr_existing" {
		t.Fatalf("resume thread id = %q, want %q", client.lastResumeThreadOpts.ThreadID, "thr_existing")
	}
}

func TestExecutorEvaluateAndExecuteResumeFallbackStartsNewThread(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_new",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented","before_evidence_ref":"","after_evidence_ref":""}`},
	}
	client := &fakeClient{
		thread:    thread,
		resumeErr: errors.New("thread missing"),
	}
	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	result, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{
		Identifier: "COLIN-202",
		Metadata: map[string]string{
			workflow.MetaThreadID: "thr_existing",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}
	if result.ThreadID != "thr_new" {
		t.Fatalf("ThreadID = %q, want %q", result.ThreadID, "thr_new")
	}
	if result.ResumedFromThreadID != "" {
		t.Fatalf("ResumedFromThreadID = %q, want empty", result.ResumedFromThreadID)
	}
	if !strings.Contains(result.ResumeFallbackReason, "resume thread \"thr_existing\" failed") {
		t.Fatalf("ResumeFallbackReason = %q, want resume failure context", result.ResumeFallbackReason)
	}
	if client.resumeCalls != 1 {
		t.Fatalf("resumeCalls = %d, want 1", client.resumeCalls)
	}
	if client.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", client.startCalls)
	}
	if client.lastResumeThreadOpts == nil {
		t.Fatal("expected resume thread options to be set")
	}
}

func TestExecutorEvaluateAndExecuteWellSpecifiedCommitsWorktreeChanges(t *testing.T) {
	repoRoot := initExecutorTestGitRepo(t)
	changePath := filepath.Join(repoRoot, "turn-change.txt")
	if err := os.WriteFile(changePath, []byte("from codex turn\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", changePath, err)
	}

	thread := &fakeThread{
		id:         "thr_commit_1",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented update","before_evidence_ref":"","after_evidence_ref":""}`},
	}
	client := &fakeClient{thread: thread}
	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	_, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{
		Identifier: "COLIN-101",
		Metadata: map[string]string{
			workflow.MetaWorktreePath: repoRoot,
		},
	})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}

	status := runGit(t, "-C", repoRoot, "status", "--porcelain=v1")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("worktree should be clean after auto-commit, status = %q", status)
	}
	subject := runGit(t, "-C", repoRoot, "log", "-1", "--pretty=%s")
	if subject != "COLIN-101: apply Codex turn changes" {
		t.Fatalf("last commit subject = %q, want %q", subject, "COLIN-101: apply Codex turn changes")
	}
}

func TestExecutorEvaluateAndExecuteWellSpecifiedSkipsCommitWhenNoChanges(t *testing.T) {
	repoRoot := initExecutorTestGitRepo(t)
	headBefore := runGit(t, "-C", repoRoot, "rev-parse", "HEAD")

	thread := &fakeThread{
		id:         "thr_commit_2",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"No-op turn","before_evidence_ref":"","after_evidence_ref":""}`},
	}
	client := &fakeClient{thread: thread}
	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	_, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{
		Identifier: "COLIN-102",
		Metadata: map[string]string{
			workflow.MetaWorktreePath: repoRoot,
		},
	})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}

	headAfter := runGit(t, "-C", repoRoot, "rev-parse", "HEAD")
	if headAfter != headBefore {
		t.Fatalf("HEAD changed from %q to %q for clean worktree", headBefore, headAfter)
	}
}

func TestExecutorEvaluateAndExecuteStartThreadError(t *testing.T) {
	executor := &Executor{
		newClient: func(context.Context) (codexClient, error) {
			return &fakeClient{startErr: errors.New("boom")}, nil
		},
	}

	_, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{Identifier: "COLIN-1"})
	if err == nil || !strings.Contains(err.Error(), "start thread") {
		t.Fatalf("error = %v, want start thread failure", err)
	}
}

func TestExecutorEvaluateAndExecuteUsesPromptTemplateFromMarkdown(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_3",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented","before_evidence_ref":"","after_evidence_ref":""}`},
	}

	executor := &Executor{
		cwd:            "/workspace",
		model:          "gpt-5",
		workPromptPath: "overrides/work.md",
		newClient: func(context.Context) (codexClient, error) {
			return &fakeClient{thread: thread}, nil
		},
		readFile: func(path string) ([]byte, error) {
			if path != "/workspace/overrides/work.md" {
				return nil, fmt.Errorf("unexpected prompt path %q", path)
			}
			return []byte("Issue {{ LINEAR_ID }}\nTitle {{ LINEAR_TITLE }}\nDesc {{ LINEAR_DESCRIPTION }}"), nil
		},
	}

	_, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{
		Identifier:  "COLIN-77",
		Title:       "Prompt path test",
		Description: "<!-- colin:metadata {\"k\":\"v\"} -->\nActual description",
	})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}

	if len(thread.lastInputs) != 1 {
		t.Fatalf("expected one input, got %d", len(thread.lastInputs))
	}
	prompt := thread.lastInputs[0].Text
	if !strings.Contains(prompt, "Issue COLIN-77") {
		t.Fatalf("prompt missing identifier substitution: %q", prompt)
	}
	if !strings.Contains(prompt, "Title Prompt path test") {
		t.Fatalf("prompt missing title substitution: %q", prompt)
	}
	if !strings.Contains(prompt, "Desc Actual description") {
		t.Fatalf("prompt missing description substitution: %q", prompt)
	}
	if strings.Contains(prompt, "colin:metadata") {
		t.Fatalf("prompt should strip metadata block, got %q", prompt)
	}
}

func TestExecutorEvaluateAndExecuteUsesWorkflowPromptBodyWhenNoOverridePath(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_workflow_prompt",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented","before_evidence_ref":"","after_evidence_ref":""}`},
	}

	executor := &Executor{
		cwd:                "/workspace",
		model:              "gpt-5",
		workflowPromptBody: "Issue {{ .Issue.Identifier }} / {{ LINEAR_TITLE }} / {{ .Issue.Description }}",
		newClient: func(context.Context) (codexClient, error) {
			return &fakeClient{thread: thread}, nil
		},
	}

	_, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{
		Identifier:  "COLIN-120",
		Title:       "Workflow prompt path test",
		Description: "<!-- colin:metadata {\"k\":\"v\"} -->\nActual description",
	})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}

	prompt := thread.lastInputs[0].Text
	if !strings.Contains(prompt, "Issue COLIN-120 / Workflow prompt path test / Actual description") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestExecutorEvaluateAndExecuteRejectsLegacyEvidenceFields(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_legacy",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented tests","transcript_ref":"terminal://logs/COLIN-1.txt","screenshot_ref":"https://example.invalid/result.png"}`},
	}
	client := &fakeClient{thread: thread}
	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	_, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{Identifier: "COLIN-1", Title: "Title", Description: "Spec"})
	if err == nil {
		t.Fatal("EvaluateAndExecute() error = nil, want strict schema failure")
	}
	if !strings.Contains(err.Error(), "response is missing required field \"before_evidence_ref\"") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecutorEvaluateAndExecuteRejectsLocalEvidencePaths(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_local_path",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented tests","before_evidence_ref":"/tmp/COLIN-80-before.hex","after_evidence_ref":"https://linear.app/example/attachment/COLIN-80-after"}`},
	}
	client := &fakeClient{thread: thread}
	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	_, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{Identifier: "COLIN-80", Title: "Title", Description: "Spec"})
	if err == nil {
		t.Fatal("EvaluateAndExecute() error = nil, want invalid evidence URL failure")
	}
	if !strings.Contains(err.Error(), "response field \"before_evidence_ref\" must be a reviewer-accessible URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadPromptTemplateUsesEmbeddedTemplateWhenNoOverride(t *testing.T) {
	executor := &Executor{
		readFile: func(string) ([]byte, error) {
			t.Fatal("readFile should not be called when no override is configured")
			return nil, nil
		},
	}

	template, err := executor.loadPromptTemplate()
	if err != nil {
		t.Fatalf("loadPromptTemplate() error = %v", err)
	}
	if template == "" {
		t.Fatal("template should not be empty")
	}
	if !strings.Contains(template, "{{ LINEAR_ID }}") {
		t.Fatalf("template missing expected placeholders: %q", template)
	}
}

func TestLoadPromptTemplateErrorsWhenOverrideFileMissing(t *testing.T) {
	executor := &Executor{
		cwd:            "/workspace",
		workPromptPath: "missing.md",
		readFile: func(string) ([]byte, error) {
			return nil, errors.New("missing")
		},
	}

	_, err := executor.loadPromptTemplate()
	if err == nil {
		t.Fatal("loadPromptTemplate() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "read prompt override") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func initExecutorTestGitRepo(t *testing.T) string {
	t.Helper()

	repoRoot := t.TempDir()
	runGit(t, "init", repoRoot)
	runGit(t, "-C", repoRoot, "config", "user.email", "colin-tests@example.com")
	runGit(t, "-C", repoRoot, "config", "user.name", "Colin Tests")

	readmePath := filepath.Join(repoRoot, "README.md")
	if err := os.WriteFile(readmePath, []byte("# test repo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", readmePath, err)
	}
	runGit(t, "-C", repoRoot, "add", "README.md")
	runGit(t, "-C", repoRoot, "commit", "-m", "seed")
	runGit(t, "-C", repoRoot, "branch", "-M", "main")

	return repoRoot
}

func runGit(t *testing.T, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}
