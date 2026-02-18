package codexexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/colin/internal/linear"
)

type fakeThread struct {
	id             string
	turnResult     *codex.TurnResult
	runErr         error
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

type fakeClient struct {
	thread   *fakeThread
	closed   bool
	startErr error
}

func (f *fakeClient) StartThread(_ context.Context, _ codex.ThreadStartOptions) (codexThread, error) {
	if f.startErr != nil {
		return nil, f.startErr
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
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":false,"needs_input_summary":"Need acceptance criteria","execution_summary":""}`},
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
	if !client.closed {
		t.Fatal("expected client.Close() to be called")
	}
	if len(thread.lastInputs) != 1 {
		t.Fatalf("expected exactly one input, got %d", len(thread.lastInputs))
	}
	text := thread.lastInputs[0].Text
	if strings.Contains(text, "colin:metadata") {
		t.Fatalf("prompt should strip metadata block, got %q", text)
	}
}

func TestExecutorEvaluateAndExecuteWellSpecified(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_2",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented tests","transcript_ref":"terminal://logs/COLIN-1.txt","screenshot_ref":"https://example.invalid/result.png"}`},
	}

	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return &fakeClient{thread: thread}, nil
		},
	}

	result, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{Identifier: "COLIN-1", Title: "Title", Description: "Spec"})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}
	if !result.IsWellSpecified {
		t.Fatalf("IsWellSpecified = %t, want true", result.IsWellSpecified)
	}
	if result.ExecutionSummary != "Implemented tests" {
		t.Fatalf("ExecutionSummary = %q", result.ExecutionSummary)
	}
	if result.TranscriptRef != "terminal://logs/COLIN-1.txt" {
		t.Fatalf("TranscriptRef = %q", result.TranscriptRef)
	}
	if result.ScreenshotRef != "https://example.invalid/result.png" {
		t.Fatalf("ScreenshotRef = %q", result.ScreenshotRef)
	}
	if thread.lastTurnOpts == nil {
		t.Fatal("expected turn options to be set")
	}
	outputSchemaBytes, err := json.Marshal(thread.lastTurnOpts.OutputSchema)
	if err != nil {
		t.Fatalf("marshal output schema: %v", err)
	}
	outputSchema := string(outputSchemaBytes)
	if !strings.Contains(outputSchema, "\"transcript_ref\"") {
		t.Fatalf("output schema missing transcript_ref: %s", outputSchema)
	}
	if !strings.Contains(outputSchema, "\"screenshot_ref\"") {
		t.Fatalf("output schema missing screenshot_ref: %s", outputSchema)
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
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented"}`},
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
