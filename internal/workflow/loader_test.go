package workflow

import (
	"testing"

	"github.com/pmenglund/colin/internal/domain"
)

func TestLoaderParsesFrontMatter(t *testing.T) {
	t.Parallel()

	config, body, err := parseWorkflow([]byte(`---
tracker:
  kind: linear
  project_slug: test
---
Work on {{.issue.identifier}}
`))
	if err != nil {
		t.Fatalf("parseWorkflow() error = %v", err)
	}
	if got := config["tracker"].(map[string]any)["kind"]; got != "linear" {
		t.Fatalf("tracker.kind = %v, want linear", got)
	}
	if body != "Work on {{.issue.identifier}}" {
		t.Fatalf("body = %q", body)
	}
}

func TestRenderPromptFailsOnUnknownVariable(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{PromptTemplate: `{{.issue.unknown}}`}
	_, err := RenderPrompt(def, domain.Issue{Identifier: "ABC-123"}, nil)
	if err == nil {
		t.Fatal("RenderPrompt() error = nil, want template render error")
	}
}
