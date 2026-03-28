package workflowfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWithoutFrontMatterTreatsWholeFileAsPrompt(t *testing.T) {
	t.Parallel()

	def, err := Parse("WORKFLOW.md", []byte("Issue {{ LINEAR_ID }}"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(def.Config) != 0 {
		t.Fatalf("Config = %#v, want empty", def.Config)
	}
	if def.PromptTemplate != "Issue {{ LINEAR_ID }}" {
		t.Fatalf("PromptTemplate = %q", def.PromptTemplate)
	}
}

func TestParseReadsFrontMatterMapAndBody(t *testing.T) {
	t.Parallel()

	content := `---
tracker:
  kind: linear
polling:
  interval_ms: 1500
---
Issue {{ .Issue.Identifier }}
`

	def, err := Parse("WORKFLOW.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	tracker, ok := def.Config["tracker"].(map[string]any)
	if !ok {
		t.Fatalf("tracker config = %#v", def.Config["tracker"])
	}
	if tracker["kind"] != "linear" {
		t.Fatalf("tracker.kind = %#v", tracker["kind"])
	}
	if def.PromptTemplate != "Issue {{ .Issue.Identifier }}" {
		t.Fatalf("PromptTemplate = %q", def.PromptTemplate)
	}
}

func TestParseRejectsNonMapFrontMatterRoot(t *testing.T) {
	t.Parallel()

	_, err := Parse("WORKFLOW.md", []byte("---\n- linear\n---\nBody"))
	if err == nil {
		t.Fatal("Parse() error = nil, want error")
	}
	var wfErr *Error
	if !errors.As(err, &wfErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if wfErr.Kind != KindFrontMatterNotMap {
		t.Fatalf("Kind = %q, want %q", wfErr.Kind, KindFrontMatterNotMap)
	}
}

func TestLoadReturnsMissingFileKind(t *testing.T) {
	t.Parallel()

	_, err := Load(filepath.Join(t.TempDir(), "missing.md"))
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	var wfErr *Error
	if !errors.As(err, &wfErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if wfErr.Kind != KindMissingFile {
		t.Fatalf("Kind = %q, want %q", wfErr.Kind, KindMissingFile)
	}
}

func TestRenderPromptSupportsStrictTemplateAndLegacyPlaceholders(t *testing.T) {
	t.Parallel()

	prompt, err := RenderPrompt(
		"Issue {{ LINEAR_ID }} / {{ .Issue.Title }} / {{ .WorktreePath }}",
		PromptData{
			Issue: PromptIssue{
				Identifier: "COLIN-1",
				Title:      "Implement workflow support",
			},
			LinearID:     "COLIN-1",
			LinearTitle:  "Implement workflow support",
			WorktreePath: "/tmp/COLIN-1",
		},
	)
	if err != nil {
		t.Fatalf("RenderPrompt() error = %v", err)
	}
	if prompt != "Issue COLIN-1 / Implement workflow support / /tmp/COLIN-1" {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestRenderPromptFailsOnMissingTemplateKey(t *testing.T) {
	t.Parallel()

	_, err := RenderPrompt("Issue {{ .Issue.Title }} / {{ .Issue.Unknown }}", PromptData{
		Issue: PromptIssue{Title: "Implement workflow support"},
	})
	if err == nil {
		t.Fatal("RenderPrompt() error = nil, want error")
	}
	var wfErr *Error
	if !errors.As(err, &wfErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if wfErr.Kind != KindTemplateExecError {
		t.Fatalf("Kind = %q, want %q", wfErr.Kind, KindTemplateExecError)
	}
}

func TestLoadReadsWorkflowFromDisk(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := "---\ntracker:\n  kind: linear\n---\nIssue {{ LINEAR_ID }}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	def, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !strings.Contains(def.PromptTemplate, "{{ LINEAR_ID }}") {
		t.Fatalf("PromptTemplate = %q", def.PromptTemplate)
	}
}
