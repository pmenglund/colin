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

func TestMergePreparerPrepareMergeUsesPromptTemplateFromMarkdown(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_merge_1",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_ready_to_merge":true,"preparation_summary":"prepared"}`},
	}
	client := &fakeClient{thread: thread}
	preparer := &MergePreparer{
		cwd:             "/workspace",
		model:           "gpt-5",
		mergePromptPath: "overrides/merge.md",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
		readFile: func(path string) ([]byte, error) {
			if path != "/workspace/overrides/merge.md" {
				return nil, fmt.Errorf("unexpected prompt path %q", path)
			}
			return []byte("Issue {{ LINEAR_ID }}\nTitle {{ LINEAR_TITLE }}\nDesc {{ LINEAR_DESCRIPTION }}\nSource {{ SOURCE_BRANCH }}\nBase {{ BASE_BRANCH }}\nRemote {{ REMOTE_NAME }}\nWorktree {{ WORKTREE_PATH }}"), nil
		},
	}

	err := preparer.PrepareMerge(
		context.Background(),
		linear.Issue{
			Identifier:  "COLIN-88",
			Title:       "Merge prep",
			Description: "<!-- colin:metadata {\"k\":\"v\"} -->\nActual description",
		},
		"colin/COLIN-88",
		"/tmp/worktree/COLIN-88",
		"main",
		"origin",
	)
	if err != nil {
		t.Fatalf("PrepareMerge() error = %v", err)
	}

	if len(thread.lastInputs) != 1 {
		t.Fatalf("expected one input, got %d", len(thread.lastInputs))
	}
	prompt := thread.lastInputs[0].Text
	if !strings.Contains(prompt, "Issue COLIN-88") {
		t.Fatalf("prompt missing issue identifier: %q", prompt)
	}
	if !strings.Contains(prompt, "Title Merge prep") {
		t.Fatalf("prompt missing issue title: %q", prompt)
	}
	if !strings.Contains(prompt, "Desc Actual description") {
		t.Fatalf("prompt missing description: %q", prompt)
	}
	if !strings.Contains(prompt, "Source colin/COLIN-88") {
		t.Fatalf("prompt missing source branch: %q", prompt)
	}
	if !strings.Contains(prompt, "Base main") {
		t.Fatalf("prompt missing base branch: %q", prompt)
	}
	if !strings.Contains(prompt, "Remote origin") {
		t.Fatalf("prompt missing remote: %q", prompt)
	}
	if !strings.Contains(prompt, "Worktree /tmp/worktree/COLIN-88") {
		t.Fatalf("prompt missing worktree path: %q", prompt)
	}
	if !strings.Contains(prompt, "Preparation mode for this run") {
		t.Fatalf("prompt missing preparation mode instructions: %q", prompt)
	}
	if !strings.Contains(prompt, "Validation applicability") {
		t.Fatalf("prompt missing validation applicability instructions: %q", prompt)
	}
	if thread.lastTurnOpts == nil {
		t.Fatal("expected turn options to be set")
	}
	if client.lastStartThreadOpts == nil {
		t.Fatal("expected start thread options to be set")
	}
	if client.lastStartThreadOpts.Cwd != "/tmp/worktree/COLIN-88" {
		t.Fatalf("thread start cwd = %q, want %q", client.lastStartThreadOpts.Cwd, "/tmp/worktree/COLIN-88")
	}
	if client.lastStartThreadOpts.SandboxPolicy != codex.SandboxModeDangerFullAccess {
		t.Fatalf(
			"thread start sandbox policy = %v, want %v",
			client.lastStartThreadOpts.SandboxPolicy,
			codex.SandboxModeDangerFullAccess,
		)
	}
	if thread.lastTurnOpts.Cwd != "/tmp/worktree/COLIN-88" {
		t.Fatalf("turn cwd = %q, want %q", thread.lastTurnOpts.Cwd, "/tmp/worktree/COLIN-88")
	}
	outputSchemaBytes, err := json.Marshal(thread.lastTurnOpts.OutputSchema)
	if err != nil {
		t.Fatalf("marshal output schema: %v", err)
	}
	outputSchema := string(outputSchemaBytes)
	if !strings.Contains(outputSchema, "\"is_ready_to_merge\"") {
		t.Fatalf("output schema missing is_ready_to_merge: %s", outputSchema)
	}
	if !strings.Contains(outputSchema, "\"preparation_summary\"") {
		t.Fatalf("output schema missing preparation_summary: %s", outputSchema)
	}
}

func TestMergePreparerPrepareMergeFailsWhenNotReady(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_merge_2",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_ready_to_merge":false,"preparation_summary":"rebase conflict on cmd/worker.go"}`},
	}
	client := &fakeClient{thread: thread}
	preparer := &MergePreparer{
		cwd:   "/tmp",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	err := preparer.PrepareMerge(
		context.Background(),
		linear.Issue{Identifier: "COLIN-89", Title: "Merge prep failure"},
		"colin/COLIN-89",
		"/tmp/worktree/COLIN-89",
		"main",
		"origin",
	)
	if err == nil {
		t.Fatal("PrepareMerge() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "merge preparation failed") {
		t.Fatalf("error = %q, want merge preparation failure context", err.Error())
	}
	if !strings.Contains(err.Error(), "rebase conflict on cmd/worker.go") {
		t.Fatalf("error = %q, want preparation summary", err.Error())
	}
	if client.lastStartThreadOpts == nil {
		t.Fatal("expected start thread options to be set")
	}
	if client.lastStartThreadOpts.Cwd != "/tmp/worktree/COLIN-89" {
		t.Fatalf("thread start cwd = %q, want %q", client.lastStartThreadOpts.Cwd, "/tmp/worktree/COLIN-89")
	}
	if thread.lastTurnOpts == nil {
		t.Fatal("expected turn options to be set")
	}
	if thread.lastTurnOpts.Cwd != "/tmp/worktree/COLIN-89" {
		t.Fatalf("turn cwd = %q, want %q", thread.lastTurnOpts.Cwd, "/tmp/worktree/COLIN-89")
	}
}

func TestMergePreparerPrepareMergeFallsBackToConfiguredCWDWhenWorktreeMissing(t *testing.T) {
	thread := &fakeThread{
		id:         "thr_merge_3",
		turnResult: &codex.TurnResult{FinalResponse: `{"is_ready_to_merge":true,"preparation_summary":"prepared"}`},
	}
	client := &fakeClient{thread: thread}
	preparer := &MergePreparer{
		cwd:   "/tmp/fallback",
		model: "gpt-5",
		newClient: func(context.Context) (codexClient, error) {
			return client, nil
		},
	}

	err := preparer.PrepareMerge(
		context.Background(),
		linear.Issue{Identifier: "COLIN-90", Title: "Merge prep fallback"},
		"colin/COLIN-90",
		"",
		"main",
		"origin",
	)
	if err != nil {
		t.Fatalf("PrepareMerge() error = %v", err)
	}
	if client.lastStartThreadOpts == nil {
		t.Fatal("expected start thread options to be set")
	}
	if client.lastStartThreadOpts.Cwd != "/tmp/fallback" {
		t.Fatalf("thread start cwd = %q, want %q", client.lastStartThreadOpts.Cwd, "/tmp/fallback")
	}
	if client.lastStartThreadOpts.SandboxPolicy != codex.SandboxModeWorkspaceWrite {
		t.Fatalf(
			"thread start sandbox policy = %v, want %v",
			client.lastStartThreadOpts.SandboxPolicy,
			codex.SandboxModeWorkspaceWrite,
		)
	}
	if thread.lastTurnOpts == nil {
		t.Fatal("expected turn options to be set")
	}
	if thread.lastTurnOpts.Cwd != "/tmp/fallback" {
		t.Fatalf("turn cwd = %q, want %q", thread.lastTurnOpts.Cwd, "/tmp/fallback")
	}
}

func TestMergePreparerLoadPromptTemplateUsesEmbeddedTemplateWhenNoOverride(t *testing.T) {
	preparer := &MergePreparer{
		readFile: func(string) ([]byte, error) {
			t.Fatal("readFile should not be called when no override is configured")
			return nil, nil
		},
	}

	template, err := preparer.loadPromptTemplate()
	if err != nil {
		t.Fatalf("loadPromptTemplate() error = %v", err)
	}
	if template == "" {
		t.Fatal("template should not be empty")
	}
	if !strings.Contains(template, "{{ SOURCE_BRANCH }}") {
		t.Fatalf("template missing expected placeholders: %q", template)
	}
}

func TestMergePreparerLoadPromptTemplateErrorsWhenOverrideFileMissing(t *testing.T) {
	preparer := &MergePreparer{
		cwd:             "/workspace",
		mergePromptPath: "missing.md",
		readFile: func(string) ([]byte, error) {
			return nil, errors.New("missing")
		},
	}

	_, err := preparer.loadPromptTemplate()
	if err == nil {
		t.Fatal("loadPromptTemplate() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "read prompt override") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergePreparationSandboxPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		defaultCWD   string
		worktreePath string
		want         any
	}{
		{
			name:         "external worktree escalates",
			defaultCWD:   "/workspace/repo",
			worktreePath: "/tmp/worktrees/COLIN-1",
			want:         codex.SandboxModeDangerFullAccess,
		},
		{
			name:         "worktree under repo root keeps workspace sandbox",
			defaultCWD:   "/workspace/repo",
			worktreePath: "/workspace/repo/.colin/COLIN-1",
			want:         codex.SandboxModeWorkspaceWrite,
		},
		{
			name:         "missing worktree keeps workspace sandbox",
			defaultCWD:   "/workspace/repo",
			worktreePath: "",
			want:         codex.SandboxModeWorkspaceWrite,
		},
		{
			name:         "missing repo root escalates",
			defaultCWD:   "",
			worktreePath: "/tmp/worktrees/COLIN-1",
			want:         codex.SandboxModeDangerFullAccess,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := mergePreparationSandboxPolicy(tc.defaultCWD, tc.worktreePath)
			if got != tc.want {
				t.Fatalf(
					"mergePreparationSandboxPolicy(%q, %q) = %v, want %v",
					tc.defaultCWD,
					tc.worktreePath,
					got,
					tc.want,
				)
			}
		})
	}
}
