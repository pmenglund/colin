package codexexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/codex-sdk-go/rpc"

	"github.com/pmenglund/colin/internal/execution"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/internal/workflowfile"
	"github.com/pmenglund/colin/prompts"
)

// Options controls Codex-backed issue execution.
type Options struct {
	Cwd string

	CodexPath          string
	ConfigOverrides    []string
	Model              string
	Logger             *slog.Logger
	WorkPromptPath     string
	MergePromptPath    string
	WorkflowPromptBody string
}

// Executor runs in-progress issue work through a Codex thread.
type Executor struct {
	cwd                string
	model              string
	workPromptPath     string
	workflowPromptBody string
	newClient          func(ctx context.Context) (codexClient, error)
	readFile           func(path string) ([]byte, error)
}

// New builds an executor backed by github.com/pmenglund/codex-sdk-go.
func New(opts Options) *Executor {
	return &Executor{
		cwd:                strings.TrimSpace(opts.Cwd),
		model:              strings.TrimSpace(opts.Model),
		workPromptPath:     strings.TrimSpace(opts.WorkPromptPath),
		workflowPromptBody: strings.TrimSpace(opts.WorkflowPromptBody),
		newClient: func(ctx context.Context) (codexClient, error) {
			client, err := codex.New(ctx, codex.Options{
				Logger: opts.Logger,
				Spawn: codex.SpawnOptions{
					CodexPath:       strings.TrimSpace(opts.CodexPath),
					ConfigOverrides: append([]string(nil), opts.ConfigOverrides...),
				},
			})
			if err != nil {
				return nil, err
			}
			return realCodexClient{client: client}, nil
		},
		readFile: os.ReadFile,
	}
}

// EvaluateAndExecute starts Codex, opens a thread, and executes an in-progress issue task.
func (e *Executor) EvaluateAndExecute(ctx context.Context, issue linear.Issue) (execution.InProgressExecutionResult, error) {
	result, err := e.RunAttempt(ctx, execution.AttemptRequest{Issue: issue}, nil)
	if err != nil {
		return execution.InProgressExecutionResult{}, err
	}
	return execution.InProgressExecutionResult{
		IsWellSpecified:      result.IsWellSpecified,
		NeedsInputSummary:    result.NeedsInputSummary,
		ExecutionSummary:     result.ExecutionSummary,
		ExecutionContext:     result.ExecutionContext,
		ThreadID:             result.ThreadID,
		ResumedFromThreadID:  result.ResumedFromThreadID,
		ResumeFallbackReason: result.ResumeFallbackReason,
		BeforeEvidenceRef:    result.BeforeEvidenceRef,
		AfterEvidenceRef:     result.AfterEvidenceRef,
	}, nil
}

// EvaluateAndExecuteStreamed executes one issue turn while forwarding live session updates.
func (e *Executor) EvaluateAndExecuteStreamed(
	ctx context.Context,
	issue linear.Issue,
	sink func(execution.SessionUpdate),
) (execution.InProgressExecutionResult, error) {
	result, err := e.RunAttempt(ctx, execution.AttemptRequest{Issue: issue}, sink)
	if err != nil {
		return execution.InProgressExecutionResult{}, err
	}
	return execution.InProgressExecutionResult{
		IsWellSpecified:      result.IsWellSpecified,
		NeedsInputSummary:    result.NeedsInputSummary,
		ExecutionSummary:     result.ExecutionSummary,
		ExecutionContext:     result.ExecutionContext,
		ThreadID:             result.ThreadID,
		ResumedFromThreadID:  result.ResumedFromThreadID,
		ResumeFallbackReason: result.ResumeFallbackReason,
		BeforeEvidenceRef:    result.BeforeEvidenceRef,
		AfterEvidenceRef:     result.AfterEvidenceRef,
	}, nil
}

// RunAttempt executes one Codex attempt with optional streaming updates.
func (e *Executor) RunAttempt(
	ctx context.Context,
	req execution.AttemptRequest,
	sink func(execution.SessionUpdate),
) (execution.AttemptResult, error) {
	if e == nil || e.newClient == nil {
		return execution.AttemptResult{}, fmt.Errorf("codex executor is not configured")
	}

	client, err := e.newClient(ctx)
	if err != nil {
		return execution.AttemptResult{}, fmt.Errorf("start codex: %w", err)
	}
	defer client.Close()

	issue := req.Issue
	threadCWD := resolveThreadCWD(e.cwd, firstNonEmpty(req.WorkspacePath, issue.Metadata[workflow.MetaWorktreePath]))
	thread, resumedFromThreadID, resumeFallbackReason, err := e.startOrResumeThread(ctx, client, issue, threadCWD)
	if err != nil {
		return execution.AttemptResult{}, err
	}

	prompt, err := e.buildAttemptPrompt(issue, req.Attempt, req.Continuation)
	if err != nil {
		return execution.AttemptResult{}, fmt.Errorf("build work prompt: %w", err)
	}

	turn, err := e.runTurn(ctx, thread, threadCWD, prompt, sink)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return execution.AttemptResult{Status: execution.AttemptStatusCanceled}, err
		}
		return execution.AttemptResult{Status: execution.AttemptStatusFailed}, fmt.Errorf("run codex turn: %w", err)
	}

	payload, err := parseResponse(turn.FinalResponse)
	if err != nil {
		return execution.AttemptResult{Status: execution.AttemptStatusFailed}, fmt.Errorf("parse codex response: %w", err)
	}
	if !payload.IsWellSpecified && strings.TrimSpace(payload.NeedsInputSummary) == "" {
		payload.NeedsInputSummary = "Provide clear scope, acceptance criteria, and constraints before retrying."
	}
	if payload.IsWellSpecified && strings.TrimSpace(payload.ExecutionSummary) == "" {
		payload.ExecutionSummary = "Execution completed without a summary from Codex."
	}
	if payload.IsWellSpecified {
		if err := e.commitTurnChanges(ctx, issue); err != nil {
			return execution.AttemptResult{Status: execution.AttemptStatusFailed}, fmt.Errorf("commit turn changes: %w", err)
		}
	}

	return execution.AttemptResult{
		Status:               execution.AttemptStatusSucceeded,
		IsWellSpecified:      payload.IsWellSpecified,
		NeedsInputSummary:    strings.TrimSpace(payload.NeedsInputSummary),
		ExecutionSummary:     strings.TrimSpace(payload.ExecutionSummary),
		ExecutionContext:     strings.TrimSpace(prompt),
		ThreadID:             strings.TrimSpace(thread.ID()),
		TurnID:               strings.TrimSpace(turn.TurnID),
		ResumedFromThreadID:  strings.TrimSpace(resumedFromThreadID),
		ResumeFallbackReason: strings.TrimSpace(resumeFallbackReason),
		BeforeEvidenceRef:    strings.TrimSpace(payload.BeforeEvidenceRef),
		AfterEvidenceRef:     strings.TrimSpace(payload.AfterEvidenceRef),
	}, nil
}

func (e *Executor) startOrResumeThread(
	ctx context.Context,
	client codexClient,
	issue linear.Issue,
	threadCWD string,
) (codexThread, string, string, error) {
	existingThreadID := strings.TrimSpace(issue.Metadata[workflow.MetaThreadID])
	if existingThreadID != "" {
		thread, err := client.ResumeThread(ctx, codex.ThreadResumeOptions{
			ThreadID:       existingThreadID,
			Model:          e.model,
			Cwd:            threadCWD,
			ApprovalPolicy: codex.ApprovalPolicyNever,
			Sandbox:        codex.SandboxModeWorkspaceWrite,
		})
		if err == nil {
			return thread, existingThreadID, "", nil
		}

		fallbackThread, startErr := client.StartThread(ctx, codex.ThreadStartOptions{
			Model:          e.model,
			Cwd:            threadCWD,
			ApprovalPolicy: codex.ApprovalPolicyNever,
			SandboxPolicy:  codex.SandboxModeWorkspaceWrite,
		})
		if startErr != nil {
			return nil, "", "", fmt.Errorf(
				"resume thread %q failed (%v), and start fallback thread failed: %w",
				existingThreadID,
				err,
				startErr,
			)
		}
		return fallbackThread, "", fmt.Sprintf("resume thread %q failed: %v", existingThreadID, err), nil
	}

	thread, err := client.StartThread(ctx, codex.ThreadStartOptions{
		Model:          e.model,
		Cwd:            threadCWD,
		ApprovalPolicy: codex.ApprovalPolicyNever,
		SandboxPolicy:  codex.SandboxModeWorkspaceWrite,
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("start thread: %w", err)
	}
	return thread, "", "", nil
}

func (e *Executor) commitTurnChanges(ctx context.Context, issue linear.Issue) error {
	worktreePath := strings.TrimSpace(issue.Metadata[workflow.MetaWorktreePath])
	if worktreePath == "" {
		return nil
	}

	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	repo, err := git.PlainOpenWithOptions(worktreePath, &git.PlainOpenOptions{
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return fmt.Errorf("inspect git status in %q: %w", worktreePath, err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("inspect git status in %q: %w", worktreePath, err)
	}

	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("inspect git status in %q: %w", worktreePath, err)
	}
	if status.IsClean() {
		return nil
	}

	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return fmt.Errorf("stage changes in %q: %w", worktreePath, err)
	}
	if _, err := worktree.Commit(turnCommitMessage(issue.Identifier), &git.CommitOptions{}); err != nil {
		return fmt.Errorf("commit changes in %q: %w", worktreePath, err)
	}

	return nil
}

func turnCommitMessage(issueIdentifier string) string {
	trimmed := strings.TrimSpace(issueIdentifier)
	if trimmed == "" {
		return "Apply Codex turn changes"
	}
	return trimmed + ": apply Codex turn changes"
}

type codexResponse struct {
	IsWellSpecified   bool   `json:"is_well_specified"`
	NeedsInputSummary string `json:"needs_input_summary"`
	ExecutionSummary  string `json:"execution_summary"`
	BeforeEvidenceRef string `json:"before_evidence_ref"`
	AfterEvidenceRef  string `json:"after_evidence_ref"`
}

func parseResponse(raw string) (codexResponse, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return codexResponse{}, fmt.Errorf("empty codex response")
	}

	var out codexResponse
	if err := decodeResponse([]byte(text), &out); err == nil {
		return out, nil
	}

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end < start {
		return codexResponse{}, fmt.Errorf("response is not JSON: %q", text)
	}
	if err := decodeResponse([]byte(text[start:end+1]), &out); err != nil {
		return codexResponse{}, err
	}
	return out, nil
}

func decodeResponse(raw []byte, out *codexResponse) error {
	required := []string{
		"is_well_specified",
		"needs_input_summary",
		"execution_summary",
		"before_evidence_ref",
		"after_evidence_ref",
	}
	allowed := map[string]struct{}{
		"is_well_specified":   {},
		"needs_input_summary": {},
		"execution_summary":   {},
		"before_evidence_ref": {},
		"after_evidence_ref":  {},
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}

	for _, key := range required {
		if _, ok := payload[key]; !ok {
			return fmt.Errorf("response is missing required field %q", key)
		}
	}

	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("response contains unsupported field %q", key)
		}
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	if err := validateEvidenceURL(out.BeforeEvidenceRef, "before_evidence_ref"); err != nil {
		return err
	}
	if err := validateEvidenceURL(out.AfterEvidenceRef, "after_evidence_ref"); err != nil {
		return err
	}
	return nil
}

func validateEvidenceURL(raw string, field string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return nil
	}
	return fmt.Errorf("response field %q must be a reviewer-accessible URL when provided", field)
}

func (e *Executor) buildAttemptPrompt(issue linear.Issue, attempt *int, continuation bool) (string, error) {
	if continuation {
		return buildContinuationPrompt(issue, attempt), nil
	}
	return e.buildPrompt(issue, attempt)
}

func buildContinuationPrompt(issue linear.Issue, attempt *int) string {
	identifier := strings.TrimSpace(issue.Identifier)
	if identifier == "" {
		identifier = strings.TrimSpace(issue.ID)
	}
	attemptLabel := "continuation"
	if attempt != nil && *attempt > 0 {
		attemptLabel = fmt.Sprintf("continuation attempt %d", *attempt)
	}
	return strings.TrimSpace(fmt.Sprintf(
		"Continue working on issue %s in the existing thread context.\n"+
			"This is %s in the same workspace.\n"+
			"Do not restate prior work. Continue from the current thread state and return the same JSON response schema when this turn ends.",
		identifier,
		attemptLabel,
	))
}

func (e *Executor) buildPrompt(issue linear.Issue, attempt *int) (string, error) {
	template, err := e.loadPromptTemplate()
	if err != nil {
		return "", err
	}
	return workflowfile.RenderPrompt(template, workflowfile.PromptData{
		Issue: workflowfile.PromptIssue{
			ID:          strings.TrimSpace(issue.ID),
			Identifier:  strings.TrimSpace(issue.Identifier),
			Title:       strings.TrimSpace(issue.Title),
			Description: issuePromptDescription(issue),
			ProjectID:   strings.TrimSpace(issue.ProjectID),
			ProjectName: strings.TrimSpace(issue.ProjectName),
			State:       strings.TrimSpace(issue.StateName),
			Blocked:     issue.Blocked,
			BlockedBy:   append([]string(nil), issue.BlockedBy...),
			Metadata:    mapsClone(issue.Metadata),
			URL:         strings.TrimSpace(issue.URL),
			BranchName:  strings.TrimSpace(issue.BranchName),
			Labels:      append([]string(nil), issue.Labels...),
		},
		Attempt:           attempt,
		LinearID:          strings.TrimSpace(issue.Identifier),
		LinearTitle:       strings.TrimSpace(issue.Title),
		LinearDescription: issuePromptDescription(issue),
	})
}

func (e *Executor) runTurn(
	ctx context.Context,
	thread codexThread,
	threadCWD string,
	prompt string,
	sink func(execution.SessionUpdate),
) (*codex.TurnResult, error) {
	opts := &codex.TurnOptions{
		Cwd:          threadCWD,
		Model:        e.model,
		OutputSchema: workResponseSchema(),
	}
	if sink == nil {
		return thread.RunInputs(ctx, []codex.Input{codex.TextInput(prompt)}, opts)
	}

	stream, err := thread.RunStreamed(ctx, []codex.Input{codex.TextInput(prompt)}, opts)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	result := &codex.TurnResult{}
	for {
		note, err := stream.Next(ctx)
		if err != nil {
			return nil, err
		}
		result.Notifications = append(result.Notifications, note)
		updateTurnResult(result, note)
		sink(sessionUpdateFromNotification(thread.ID(), note))

		switch note.Method {
		case "turn/completed":
			if turnErr := notificationError(note); turnErr != nil {
				return nil, turnErr
			}
			return result, nil
		case "turn/failed":
			turnErr := notificationError(note)
			if turnErr == nil {
				turnErr = errors.New("turn failed")
			}
			return nil, turnErr
		case "error":
			if turnErr := notificationError(note); turnErr != nil {
				return nil, turnErr
			}
		}
	}
}

func workResponseSchema() any {
	return codex.MustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"is_well_specified":   map[string]any{"type": "boolean"},
			"needs_input_summary": map[string]any{"type": "string"},
			"execution_summary":   map[string]any{"type": "string"},
			"before_evidence_ref": map[string]any{"type": "string"},
			"after_evidence_ref":  map[string]any{"type": "string"},
		},
		"required":             []string{"is_well_specified", "needs_input_summary", "execution_summary", "before_evidence_ref", "after_evidence_ref"},
		"additionalProperties": false,
	})
}

func updateTurnResult(result *codex.TurnResult, note rpc.Notification) {
	if result == nil {
		return
	}
	switch note.Method {
	case "item/completed":
		var payload struct {
			Item json.RawMessage `json:"item"`
		}
		if err := note.UnmarshalParams(&payload); err == nil && len(payload.Item) > 0 {
			result.Items = append(result.Items, payload.Item)
			if text, ok := extractTextFromItemRaw(payload.Item); ok {
				result.FinalResponse = text
			}
		}
	case "turn/started", "turn/completed", "turn/failed":
		var payload struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := note.UnmarshalParams(&payload); err == nil && strings.TrimSpace(payload.Turn.ID) != "" {
			result.TurnID = strings.TrimSpace(payload.Turn.ID)
		}
	}
}

func notificationError(note rpc.Notification) error {
	switch note.Method {
	case "error":
		var payload struct {
			WillRetry *bool `json:"willRetry"`
			Error     *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := note.UnmarshalParams(&payload); err != nil {
			return errors.New("turn error")
		}
		if payload.WillRetry != nil && *payload.WillRetry {
			return nil
		}
		if payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
			return errors.New(strings.TrimSpace(payload.Error.Message))
		}
		return errors.New("turn error")
	case "turn/completed", "turn/failed":
		var payload struct {
			Turn struct {
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if err := note.UnmarshalParams(&payload); err != nil {
			if note.Method == "turn/failed" {
				return errors.New("turn failed")
			}
			return nil
		}
		if note.Method == "turn/completed" && payload.Turn.Status != "failed" {
			return nil
		}
		if payload.Turn.Error != nil && strings.TrimSpace(payload.Turn.Error.Message) != "" {
			return errors.New(strings.TrimSpace(payload.Turn.Error.Message))
		}
		if note.Method == "turn/failed" {
			return errors.New("turn failed")
		}
	}
	return nil
}

func extractTextFromItemRaw(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var direct struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &direct); err == nil && direct.Text != "" {
		return direct.Text, true
	}

	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err != nil || len(wrapper) != 1 {
		return "", false
	}
	for _, inner := range wrapper {
		var nested struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(inner, &nested); err == nil && nested.Text != "" {
			return nested.Text, true
		}
	}
	return "", false
}

func sessionUpdateFromNotification(threadID string, note rpc.Notification) execution.SessionUpdate {
	update := execution.SessionUpdate{
		ThreadID:      strings.TrimSpace(threadID),
		LastEvent:     note.Method,
		LastTimestamp: time.Now().UTC(),
	}

	var base struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := note.UnmarshalParams(&base); err == nil {
		update.ThreadID = firstNonEmpty(strings.TrimSpace(base.ThreadID), update.ThreadID)
		update.TurnID = firstNonEmpty(strings.TrimSpace(base.TurnID), strings.TrimSpace(base.Turn.ID))
	}

	switch note.Method {
	case "thread/tokenUsage/updated":
		var payload struct {
			ThreadID   string `json:"threadId"`
			TurnID     string `json:"turnId"`
			TokenUsage struct {
				Last struct {
					InputTokens  int `json:"inputTokens"`
					OutputTokens int `json:"outputTokens"`
					TotalTokens  int `json:"totalTokens"`
				} `json:"last"`
				Total struct {
					InputTokens  int `json:"inputTokens"`
					OutputTokens int `json:"outputTokens"`
					TotalTokens  int `json:"totalTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if err := note.UnmarshalParams(&payload); err == nil {
			update.ThreadID = firstNonEmpty(strings.TrimSpace(payload.ThreadID), update.ThreadID)
			update.TurnID = firstNonEmpty(strings.TrimSpace(payload.TurnID), update.TurnID)
			update.InputTokens = payload.TokenUsage.Total.InputTokens
			update.OutputTokens = payload.TokenUsage.Total.OutputTokens
			update.TotalTokens = payload.TokenUsage.Total.TotalTokens
			update.ReportedInputTokens = payload.TokenUsage.Last.InputTokens
			update.ReportedOutputTokens = payload.TokenUsage.Last.OutputTokens
			update.ReportedTotalTokens = payload.TokenUsage.Last.TotalTokens
		}
	case "item/agentMessage/delta":
		var payload struct {
			Delta string `json:"delta"`
		}
		if err := note.UnmarshalParams(&payload); err == nil {
			update.LastMessage = strings.TrimSpace(payload.Delta)
		}
	}

	return update
}

func issuePromptDescription(issue linear.Issue) string {
	description := linear.StripMetadataBlock(issue.Description)
	if description == "" {
		description = "(empty description)"
	}
	return description
}

func (e *Executor) loadPromptTemplate() (string, error) {
	if strings.TrimSpace(e.workPromptPath) == "" {
		if strings.TrimSpace(e.workflowPromptBody) != "" {
			return e.workflowPromptBody, nil
		}
		return embeddedWorkPromptTemplate(), nil
	}
	if e.readFile == nil {
		return "", errors.New("prompt override is configured but file reader is unavailable")
	}

	path := strings.TrimSpace(e.workPromptPath)
	if !filepath.IsAbs(path) && strings.TrimSpace(e.cwd) != "" {
		path = filepath.Join(e.cwd, path)
	}
	content, err := e.readFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt override %q: %w", path, err)
	}

	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return "", fmt.Errorf("prompt override %q is empty", path)
	}
	return trimmed, nil
}

func mapsClone(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func embeddedWorkPromptTemplate() string {
	return strings.TrimSpace(prompts.WorkMarkdown)
}

func (e *Executor) resolveThreadCWD(issue linear.Issue) string {
	worktreePath := strings.TrimSpace(issue.Metadata[workflow.MetaWorktreePath])
	return resolveThreadCWD(e.cwd, worktreePath)
}

func resolveThreadCWD(defaultCWD string, worktreePath string) string {
	if trimmed := strings.TrimSpace(worktreePath); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(defaultCWD)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type codexClient interface {
	StartThread(ctx context.Context, options codex.ThreadStartOptions) (codexThread, error)
	ResumeThread(ctx context.Context, options codex.ThreadResumeOptions) (codexThread, error)
	Close() error
}

type codexThread interface {
	ID() string
	RunInputs(ctx context.Context, inputs []codex.Input, opts *codex.TurnOptions) (*codex.TurnResult, error)
	RunStreamed(ctx context.Context, inputs []codex.Input, opts *codex.TurnOptions) (codexTurnStream, error)
}

type codexTurnStream interface {
	Next(ctx context.Context) (rpc.Notification, error)
	Close()
}

type realCodexClient struct {
	client *codex.Codex
}

func (c realCodexClient) StartThread(ctx context.Context, options codex.ThreadStartOptions) (codexThread, error) {
	thread, err := c.client.StartThread(ctx, options)
	if err != nil {
		return nil, err
	}
	return realCodexThread{thread: thread}, nil
}

func (c realCodexClient) ResumeThread(ctx context.Context, options codex.ThreadResumeOptions) (codexThread, error) {
	thread, err := c.client.ResumeThread(ctx, options)
	if err != nil {
		return nil, err
	}
	return realCodexThread{thread: thread}, nil
}

func (c realCodexClient) Close() error {
	return c.client.Close()
}

type realCodexThread struct {
	thread *codex.Thread
}

func (t realCodexThread) ID() string {
	return t.thread.ID()
}

func (t realCodexThread) RunInputs(ctx context.Context, inputs []codex.Input, opts *codex.TurnOptions) (*codex.TurnResult, error) {
	return t.thread.RunInputs(ctx, inputs, opts)
}

func (t realCodexThread) RunStreamed(ctx context.Context, inputs []codex.Input, opts *codex.TurnOptions) (codexTurnStream, error) {
	return t.thread.RunStreamed(ctx, inputs, opts)
}
