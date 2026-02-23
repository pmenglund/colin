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

	git "github.com/go-git/go-git/v6"
	"github.com/pmenglund/codex-sdk-go"

	"github.com/pmenglund/colin/internal/execution"
	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/internal/workflow"
	"github.com/pmenglund/colin/prompts"
)

// Options controls Codex-backed issue execution.
type Options struct {
	Cwd string

	CodexPath       string
	ConfigOverrides []string
	Model           string
	Logger          *slog.Logger
	WorkPromptPath  string
	MergePromptPath string
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
	if e == nil || e.newClient == nil {
		return execution.InProgressExecutionResult{}, fmt.Errorf("codex executor is not configured")
	}

	client, err := e.newClient(ctx)
	if err != nil {
		return execution.InProgressExecutionResult{}, fmt.Errorf("start codex: %w", err)
	}
	defer client.Close()

	thread, err := client.StartThread(ctx, codex.ThreadStartOptions{
		Model:          e.model,
		Cwd:            e.resolveThreadCWD(issue),
		ApprovalPolicy: codex.ApprovalPolicyNever,
		SandboxPolicy:  codex.SandboxModeWorkspaceWrite,
	})
	if err != nil {
		return execution.InProgressExecutionResult{}, fmt.Errorf("start thread: %w", err)
	}

	responseSchema := codex.MustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"is_well_specified":   map[string]any{"type": "boolean"},
			"needs_input_summary": map[string]any{"type": "string"},
			"execution_summary":   map[string]any{"type": "string"},
			"transcript_ref":      map[string]any{"type": "string"},
			"screenshot_ref":      map[string]any{"type": "string"},
		},
		"required":             []string{"is_well_specified", "needs_input_summary", "execution_summary", "transcript_ref", "screenshot_ref"},
		"additionalProperties": false,
	})

	prompt, err := e.buildPrompt(issue)
	if err != nil {
		return execution.InProgressExecutionResult{}, fmt.Errorf("build work prompt: %w", err)
	}

	turn, err := thread.RunInputs(ctx, []codex.Input{codex.TextInput(prompt)}, &codex.TurnOptions{
		Cwd:          e.resolveThreadCWD(issue),
		Model:        e.model,
		OutputSchema: responseSchema,
	})
	if err != nil {
		return execution.InProgressExecutionResult{}, fmt.Errorf("run codex turn: %w", err)
	}

	payload, err := parseResponse(turn.FinalResponse)
	if err != nil {
		return execution.InProgressExecutionResult{}, fmt.Errorf("parse codex response: %w", err)
	}
	if !payload.IsWellSpecified && strings.TrimSpace(payload.NeedsInputSummary) == "" {
		payload.NeedsInputSummary = "Provide clear scope, acceptance criteria, and constraints before retrying."
	}
	if payload.IsWellSpecified && strings.TrimSpace(payload.ExecutionSummary) == "" {
		payload.ExecutionSummary = "Execution completed without a summary from Codex."
	}
	if payload.IsWellSpecified {
		if err := e.commitTurnChanges(ctx, issue); err != nil {
			return execution.InProgressExecutionResult{}, fmt.Errorf("commit turn changes: %w", err)
		}
	}

	return execution.InProgressExecutionResult{
		IsWellSpecified:   payload.IsWellSpecified,
		NeedsInputSummary: strings.TrimSpace(payload.NeedsInputSummary),
		ExecutionSummary:  strings.TrimSpace(payload.ExecutionSummary),
		ExecutionContext:  strings.TrimSpace(prompt),
		ThreadID:          strings.TrimSpace(thread.ID()),
		TranscriptRef:     strings.TrimSpace(payload.TranscriptRef),
		ScreenshotRef:     strings.TrimSpace(payload.ScreenshotRef),
	}, nil
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
	TranscriptRef     string `json:"transcript_ref"`
	ScreenshotRef     string `json:"screenshot_ref"`
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
	description := linear.StripMetadataBlock(issue.Description)
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

type codexClient interface {
	StartThread(ctx context.Context, options codex.ThreadStartOptions) (codexThread, error)
	Close() error
}

type codexThread interface {
	ID() string
	RunInputs(ctx context.Context, inputs []codex.Input, opts *codex.TurnOptions) (*codex.TurnResult, error)
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
