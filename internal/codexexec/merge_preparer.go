package codexexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pmenglund/codex-sdk-go"

	"github.com/pmenglund/colin/internal/linear"
	"github.com/pmenglund/colin/prompts"
)

// MergePreparer runs merge-preparation instructions through a Codex thread.
type MergePreparer struct {
	cwd             string
	model           string
	mergePromptPath string
	newClient       func(ctx context.Context) (codexClient, error)
	readFile        func(path string) ([]byte, error)
}

// NewMergePreparer builds a Codex-backed merge preparer.
func NewMergePreparer(opts Options) *MergePreparer {
	return &MergePreparer{
		cwd:             strings.TrimSpace(opts.Cwd),
		model:           strings.TrimSpace(opts.Model),
		mergePromptPath: strings.TrimSpace(opts.MergePromptPath),
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

// PrepareMerge executes merge-preparation instructions and validates readiness.
func (m *MergePreparer) PrepareMerge(
	ctx context.Context,
	issue linear.Issue,
	branchName string,
	worktreePath string,
	baseBranch string,
	remoteName string,
) error {
	if m == nil || m.newClient == nil {
		return errors.New("codex merge preparer is not configured")
	}

	client, err := m.newClient(ctx)
	if err != nil {
		return fmt.Errorf("start codex: %w", err)
	}
	defer client.Close()

	threadCWD := resolveThreadCWD(m.cwd, worktreePath)
	sandboxPolicy := mergePreparationSandboxPolicy(m.cwd, worktreePath)
	thread, err := client.StartThread(ctx, codex.ThreadStartOptions{
		Model:          m.model,
		Cwd:            threadCWD,
		ApprovalPolicy: codex.ApprovalPolicyNever,
		SandboxPolicy:  sandboxPolicy,
	})
	if err != nil {
		return fmt.Errorf("start thread: %w", err)
	}

	prompt, err := m.buildPrompt(issue, branchName, worktreePath, baseBranch, remoteName)
	if err != nil {
		return fmt.Errorf("build merge prompt: %w", err)
	}

	responseSchema := codex.MustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"is_ready_to_merge":   map[string]any{"type": "boolean"},
			"preparation_summary": map[string]any{"type": "string"},
		},
		"required":             []string{"is_ready_to_merge", "preparation_summary"},
		"additionalProperties": false,
	})
	turn, err := thread.RunInputs(ctx, []codex.Input{codex.TextInput(prompt)}, &codex.TurnOptions{
		Cwd:          threadCWD,
		Model:        m.model,
		OutputSchema: responseSchema,
	})
	if err != nil {
		return fmt.Errorf("run codex turn: %w", err)
	}

	payload, err := parseMergePreparationResponse(turn.FinalResponse)
	if err != nil {
		return fmt.Errorf("parse codex response: %w", err)
	}
	if !payload.IsReadyToMerge {
		summary := strings.TrimSpace(payload.PreparationSummary)
		if summary == "" {
			summary = "Codex reported merge preparation was not completed."
		}
		return fmt.Errorf("merge preparation failed: %s", summary)
	}

	return nil
}

func mergePreparationSandboxPolicy(defaultCWD string, worktreePath string) any {
	trimmedWorktree := strings.TrimSpace(worktreePath)
	if trimmedWorktree == "" {
		return codex.SandboxModeWorkspaceWrite
	}

	trimmedCWD := strings.TrimSpace(defaultCWD)
	if trimmedCWD == "" {
		return codex.SandboxModeDangerFullAccess
	}

	insideRepoRoot, err := pathWithin(trimmedWorktree, trimmedCWD)
	if err != nil {
		return codex.SandboxModeDangerFullAccess
	}
	if insideRepoRoot {
		return codex.SandboxModeWorkspaceWrite
	}

	// Linked worktrees commonly live under COLIN_HOME while git metadata writes
	// go through the main repository's .git/worktrees directory.
	return codex.SandboxModeDangerFullAccess
}

func pathWithin(path string, root string) (bool, error) {
	absPath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return false, fmt.Errorf("resolve absolute path %q: %w", path, err)
	}
	absRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return false, fmt.Errorf("resolve absolute root %q: %w", root, err)
	}

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false, fmt.Errorf("compute relative path from %q to %q: %w", absRoot, absPath, err)
	}

	if rel == "." {
		return true, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}

	return true, nil
}

func (m *MergePreparer) buildPrompt(
	issue linear.Issue,
	branchName string,
	worktreePath string,
	baseBranch string,
	remoteName string,
) (string, error) {
	template, err := m.loadPromptTemplate()
	if err != nil {
		return "", err
	}

	replacements := strings.NewReplacer(
		"{{ LINEAR_ID }}", strings.TrimSpace(issue.Identifier),
		"{{ LINEAR_TITLE }}", strings.TrimSpace(issue.Title),
		"{{ LINEAR_DESCRIPTION }}", issuePromptDescription(issue),
		"{{ SOURCE_BRANCH }}", strings.TrimSpace(branchName),
		"{{ BASE_BRANCH }}", strings.TrimSpace(baseBranch),
		"{{ REMOTE_NAME }}", strings.TrimSpace(remoteName),
		"{{ WORKTREE_PATH }}", strings.TrimSpace(worktreePath),
	)
	prompt := strings.TrimSpace(replacements.Replace(template))
	if prompt == "" {
		return "", errors.New("merge prompt template is empty")
	}

	// Preparation mode keeps merge/push/cleanup deterministic in git executor code.
	prompt += "\n\nPreparation mode for this run:\n"
	prompt += "- Execute only preparation steps before the final merge operation.\n"
	prompt += "- Do not run `git merge`, `git push`, `git branch -d`, or `git worktree remove`.\n"
	prompt += "- Validation applicability: if `go test ./...` fails only because there is no Go module (for example, missing `go.mod`), mark validation as not applicable and continue.\n"
	prompt += "- If preparation cannot be completed, explain why in `preparation_summary` and set `is_ready_to_merge` to false.\n"

	return prompt, nil
}

func (m *MergePreparer) loadPromptTemplate() (string, error) {
	if strings.TrimSpace(m.mergePromptPath) == "" {
		return embeddedMergePromptTemplate(), nil
	}
	if m.readFile == nil {
		return "", errors.New("prompt override is configured but file reader is unavailable")
	}

	path := strings.TrimSpace(m.mergePromptPath)
	if !filepath.IsAbs(path) && strings.TrimSpace(m.cwd) != "" {
		path = filepath.Join(m.cwd, path)
	}
	content, err := m.readFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt override %q: %w", path, err)
	}

	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return "", fmt.Errorf("prompt override %q is empty", path)
	}
	return trimmed, nil
}

func embeddedMergePromptTemplate() string {
	return strings.TrimSpace(prompts.MergeMarkdown)
}

type mergePreparationResponse struct {
	IsReadyToMerge     bool   `json:"is_ready_to_merge"`
	PreparationSummary string `json:"preparation_summary"`
}

func parseMergePreparationResponse(raw string) (mergePreparationResponse, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return mergePreparationResponse{}, fmt.Errorf("empty codex response")
	}

	var out mergePreparationResponse
	if err := json.Unmarshal([]byte(text), &out); err == nil {
		return out, nil
	}

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end < start {
		return mergePreparationResponse{}, fmt.Errorf("response is not JSON: %q", text)
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return mergePreparationResponse{}, err
	}
	return out, nil
}
