package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	codexsdk "github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/worker"
	"github.com/pmenglund/colin/internal/workflow"
)

var metadataCommentRegexp = regexp.MustCompile(`(?s)<!--\s*colin:metadata\s+\{.*?\}\s*-->`)

// Options controls Codex-backed issue execution.
type Options struct {
	Cwd string

	CodexPath       string
	ConfigOverrides []string
	Model           string
	Logger          *slog.Logger
	WorkPromptPath  string
}

// Executor runs in-progress issue work through a Codex thread.
type Executor struct {
	cwd            string
	model          string
	workPromptPath string
	newClient      func(ctx context.Context) (codexClient, error)
	readFile       func(path string) ([]byte, error)
}

// New builds an executor backed by github.com/pmenglund/codex-sdk-go.
func New(opts Options) *Executor {
	return &Executor{
		cwd:            strings.TrimSpace(opts.Cwd),
		model:          strings.TrimSpace(opts.Model),
		workPromptPath: strings.TrimSpace(opts.WorkPromptPath),
		newClient: func(ctx context.Context) (codexClient, error) {
			client, err := codexsdk.New(ctx, codexsdk.Options{
				Logger: opts.Logger,
				Spawn: codexsdk.SpawnOptions{
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
func (e *Executor) EvaluateAndExecute(ctx context.Context, issue linear.Issue) (worker.InProgressExecutionResult, error) {
	if e == nil || e.newClient == nil {
		return worker.InProgressExecutionResult{}, fmt.Errorf("codex executor is not configured")
	}

	client, err := e.newClient(ctx)
	if err != nil {
		return worker.InProgressExecutionResult{}, fmt.Errorf("start codex: %w", err)
	}
	defer client.Close()

	threadID, sessionID, err := codexIdentityFromMetadata(issue.Metadata)
	if err != nil {
		return worker.InProgressExecutionResult{}, err
	}

	var thread codexThread
	threadResumed := false
	if threadID != "" {
		thread, err = client.ResumeThread(ctx, codexsdk.ThreadResumeOptions{
			ThreadID:       threadID,
			Model:          e.model,
			Cwd:            e.cwd,
			ApprovalPolicy: codexsdk.ApprovalPolicyNever,
			Sandbox:        codexsdk.SandboxModeWorkspaceWrite,
		})
		if err != nil {
			return worker.InProgressExecutionResult{}, fmt.Errorf("resume thread %q: %w", threadID, err)
		}
		threadResumed = true
	} else {
		thread, err = client.StartThread(ctx, codexsdk.ThreadStartOptions{
			Model:          e.model,
			Cwd:            e.cwd,
			ApprovalPolicy: codexsdk.ApprovalPolicyNever,
			SandboxPolicy:  codexsdk.SandboxModeWorkspaceWrite,
		})
		if err != nil {
			return worker.InProgressExecutionResult{}, fmt.Errorf("start thread: %w", err)
		}
	}

	responseSchema := codexsdk.MustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"is_well_specified":   map[string]any{"type": "boolean"},
			"needs_input_summary": map[string]any{"type": "string"},
			"execution_summary":   map[string]any{"type": "string"},
		},
		"required":             []string{"is_well_specified", "needs_input_summary", "execution_summary"},
		"additionalProperties": false,
	})

	turn, err := thread.RunInputs(ctx, []codexsdk.Input{codexsdk.TextInput(buildPrompt(issue))}, &codexsdk.TurnOptions{
		Cwd:          e.cwd,
		Model:        e.model,
		OutputSchema: responseSchema,
	})
	if err != nil {
		return worker.InProgressExecutionResult{}, fmt.Errorf("run codex turn: %w", err)
	}

	payload, err := parseResponse(turn.FinalResponse)
	if err != nil {
		return worker.InProgressExecutionResult{}, fmt.Errorf("parse codex response: %w", err)
	}
	if !payload.IsWellSpecified && strings.TrimSpace(payload.NeedsInputSummary) == "" {
		payload.NeedsInputSummary = "Provide clear scope, acceptance criteria, and constraints before retrying."
	}
	if payload.IsWellSpecified && strings.TrimSpace(payload.ExecutionSummary) == "" {
		payload.ExecutionSummary = "Execution completed without a summary from Codex."
	}

	activeThreadID := strings.TrimSpace(thread.ID())
	if sessionID == "" {
		// The SDK currently does not expose a separate session id, so we persist the thread id.
		sessionID = activeThreadID
	}

	return worker.InProgressExecutionResult{
		IsWellSpecified:   payload.IsWellSpecified,
		NeedsInputSummary: strings.TrimSpace(payload.NeedsInputSummary),
		ExecutionSummary:  strings.TrimSpace(payload.ExecutionSummary),
		ThreadID:          activeThreadID,
		SessionID:         strings.TrimSpace(sessionID),
		ThreadResumed:     threadResumed,
	}, nil
}

type codexResponse struct {
	IsWellSpecified   bool   `json:"is_well_specified"`
	NeedsInputSummary string `json:"needs_input_summary"`
	ExecutionSummary  string `json:"execution_summary"`
}

func parseResponse(raw string) (codexResponse, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return codexResponse{}, fmt.Errorf("empty codex response")
	}

	var out codexResponse
	if err := json.Unmarshal([]byte(text), &out); err == nil {
		return out, nil
	}

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end < start {
		return codexResponse{}, fmt.Errorf("response is not JSON: %q", text)
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return codexResponse{}, err
	}
	return out, nil
}

func (e *Executor) buildPrompt(issue linear.Issue) (string, error) {
	template, err := e.loadPromptTemplate()
	if err != nil {
		return "", err
	}
	description := issuePromptDescription(issue)
	replacements := strings.NewReplacer(
		"{{ LINEAR_ID }}", strings.TrimSpace(issue.Identifier),
		"{{ LINEAR_TITLE }}", strings.TrimSpace(issue.Title),
		"{{ LINEAR_DESCRIPTION }}", description,
	)
	return replacements.Replace(template), nil
}

func issuePromptDescription(issue linear.Issue) string {
	description := strings.TrimSpace(metadataCommentRegexp.ReplaceAllString(issue.Description, ""))
	if description == "" {
		description = "(empty description)"
	}
	return description
}

func (e *Executor) loadPromptTemplate() (string, error) {
	if strings.TrimSpace(e.workPromptPath) == "" {
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

func embeddedWorkPromptTemplate() string {
	return strings.TrimSpace(prompts.WorkMarkdown)
}

type codexClient interface {
	StartThread(ctx context.Context, options codexsdk.ThreadStartOptions) (codexThread, error)
	ResumeThread(ctx context.Context, options codexsdk.ThreadResumeOptions) (codexThread, error)
	Close() error
}

type codexThread interface {
	ID() string
	RunInputs(ctx context.Context, inputs []codexsdk.Input, opts *codexsdk.TurnOptions) (*codexsdk.TurnResult, error)
}

type realCodexClient struct {
	client *codexsdk.Codex
}

func (c realCodexClient) StartThread(ctx context.Context, options codexsdk.ThreadStartOptions) (codexThread, error) {
	thread, err := c.client.StartThread(ctx, options)
	if err != nil {
		return nil, err
	}
	return realCodexThread{thread: thread}, nil
}

func (c realCodexClient) ResumeThread(ctx context.Context, options codexsdk.ThreadResumeOptions) (codexThread, error) {
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
	thread *codexsdk.Thread
}

func (t realCodexThread) ID() string {
	return t.thread.ID()
}

func (t realCodexThread) RunInputs(ctx context.Context, inputs []codexsdk.Input, opts *codexsdk.TurnOptions) (*codexsdk.TurnResult, error) {
	return t.thread.RunInputs(ctx, inputs, opts)
}

func codexIdentityFromMetadata(meta map[string]string) (string, string, error) {
	threadID := strings.TrimSpace(meta[workflow.MetaCodexThreadID])
	sessionID := strings.TrimSpace(meta[workflow.MetaCodexSessionID])

	if threadID == "" && sessionID == "" {
		return "", "", nil
	}
	if threadID == "" || sessionID == "" {
		return "", "", fmt.Errorf(
			"incomplete codex metadata (%s=%q, %s=%q): clear both metadata keys and retry",
			workflow.MetaCodexThreadID, threadID,
			workflow.MetaCodexSessionID, sessionID,
		)
	}

	return threadID, sessionID, nil
}
