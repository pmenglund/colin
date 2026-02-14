package codexexec

import (
	"context"
	"errors"
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
	if result.SessionID != "thr_1" {
		t.Fatalf("SessionID = %q", result.SessionID)
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
		turnResult: &codex.TurnResult{FinalResponse: `{"is_well_specified":true,"needs_input_summary":"","execution_summary":"Implemented tests"}`},
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
		t.Fatalf("SessionID = %q", result.SessionID)
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
