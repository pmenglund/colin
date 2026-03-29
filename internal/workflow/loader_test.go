package workflow

import (
	"testing"
	"time"

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

func TestRenderPromptIncludesReviewFeedback(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 3, 28, 18, 0, 0, 0, time.UTC)
	def := domain.WorkflowDefinition{PromptTemplate: `{{range .issue.review_feedback}}- {{.body}}
{{end}}`}

	prompt, err := RenderPrompt(def, domain.Issue{
		Identifier: "ABC-123",
		ReviewFeedback: []domain.ReviewFeedback{
			{Body: "Address the review comment.", CreatedAt: createdAt},
			{Body: "Mark the PR thread resolved.", CreatedAt: createdAt.Add(time.Minute)},
		},
	}, nil)
	if err != nil {
		t.Fatalf("RenderPrompt() error = %v", err)
	}

	want := "- Address the review comment.\n- Mark the PR thread resolved."
	if prompt != want {
		t.Fatalf("prompt = %q, want %q", prompt, want)
	}
}

func TestRenderTemplateIncludesArbitraryPayload(t *testing.T) {
	t.Parallel()

	rendered, err := RenderTemplate(
		`Issue {{.issue.identifier}} on {{.base_ref}} via {{.branch}}`,
		map[string]any{
			"issue": map[string]any{
				"identifier": "ABC-123",
			},
			"base_ref": "main",
			"branch":   "feature/abc-123",
		},
	)
	if err != nil {
		t.Fatalf("RenderTemplate() error = %v", err)
	}
	if want := "Issue ABC-123 on main via feature/abc-123"; rendered != want {
		t.Fatalf("rendered = %q, want %q", rendered, want)
	}
}
