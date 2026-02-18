package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	codexsdk "github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/colin/internal/linear"
)

type fakeThread struct {
	id             string
	turnResult     *codexsdk.TurnResult
	runErr         error
	lastInputs     []codexsdk.Input
	lastTurnOpts   *codexsdk.TurnOptions
	startThreadErr error
}

func (f *fakeThread) ID() string { return f.id }

func (f *fakeThread) RunInputs(_ context.Context, inputs []codexsdk.Input, opts *codexsdk.TurnOptions) (*codexsdk.TurnResult, error) {
	f.lastInputs = append([]codexsdk.Input(nil), inputs...)
	f.lastTurnOpts = opts
	if f.runErr != nil {
		return nil, f.runErr
	}
	return f.turnResult, nil
}

type fakeClient struct {
	thread      *fakeThread
	closed      bool
	startErr    error
	resumeErr   error
	startCalls  int
	resumeCalls int
	lastResume  codexsdk.ThreadResumeOptions
}

func (f *fakeClient) StartThread(_ context.Context, _ codexsdk.ThreadStartOptions) (codexThread, error) {
	f.startCalls++
	if f.startErr != nil {
		return nil, f.startErr
	}
	return f.thread, nil
}

func (f *fakeClient) ResumeThread(_ context.Context, opts codexsdk.ThreadResumeOptions) (codexThread, error) {
	f.resumeCalls++
	f.lastResume = opts
	if f.resumeErr != nil {
		return nil, f.resumeErr
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
		turnResult: &codexsdk.TurnResult{FinalResponse: `{"is_well_specified":false,"needs_input_summary":"Need acceptance criteria","execution_summary":""}`},
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
	if result.SessionID != "thr_1" {
		t.Fatalf("SessionID = %q, want thread-derived session id", result.SessionID)
	}
	if result.ThreadResumed {
		t.Fatalf("ThreadResumed = %t, want false", result.ThreadResumed)
	}
	if client.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", client.startCalls)
	}
	if client.resumeCalls != 0 {
		t.Fatalf("resumeCalls = %d, want 0", client.resumeCalls)
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
		turnResult: &codexsdk.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented tests"}`},
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
	if result.SessionID != "thr_2" {
		t.Fatalf("SessionID = %q, want %q", result.SessionID, "thr_2")
	}
	if result.ThreadResumed {
		t.Fatalf("ThreadResumed = %t, want false", result.ThreadResumed)
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

func TestExecutorEvaluateAndExecuteResumesThreadFromMetadata(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_3",
		turnResult: &codexsdk.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"done"}`},
	}
	client := &fakeClient{thread: thread}

	executor := &Executor{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	result, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{
		Identifier:  "COLIN-1",
		Title:       "Title",
		Description: "Spec",
		Metadata: map[string]string{
			"colin.codex_thread_id":  "thr_3",
			"colin.codex_session_id": "ses_3",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateAndExecute() error = %v", err)
	}
	if !result.ThreadResumed {
		t.Fatalf("ThreadResumed = %t, want true", result.ThreadResumed)
	}
	if result.ThreadID != "thr_3" {
		t.Fatalf("ThreadID = %q, want %q", result.ThreadID, "thr_3")
	}
	if result.SessionID != "ses_3" {
		t.Fatalf("SessionID = %q, want %q", result.SessionID, "ses_3")
	}
	if client.startCalls != 0 {
		t.Fatalf("startCalls = %d, want 0", client.startCalls)
	}
	if client.resumeCalls != 1 {
		t.Fatalf("resumeCalls = %d, want 1", client.resumeCalls)
	}
	if client.lastResume.ThreadID != "thr_3" {
		t.Fatalf("lastResume.ThreadID = %q, want %q", client.lastResume.ThreadID, "thr_3")
	}
}

func TestExecutorEvaluateAndExecuteResumeMetadataMismatchIsActionable(t *testing.T) {
	executor := &Executor{
		newClient: func(context.Context) (codexClient, error) {
			return &fakeClient{}, nil
		},
	}

	_, err := executor.EvaluateAndExecute(context.Background(), linear.Issue{
		Identifier: "COLIN-1",
		Metadata: map[string]string{
			"colin.codex_thread_id": "thr_9",
		},
	})
	if err == nil {
		t.Fatal("EvaluateAndExecute() error = nil, want metadata validation error")
	}
	if !strings.Contains(err.Error(), "incomplete codex metadata") {
		t.Fatalf("error = %q, want actionable metadata error", err.Error())
	}
}
