package workflow

import (
	"path/filepath"
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
	if config.Tracker.Kind == nil || *config.Tracker.Kind != "linear" {
		t.Fatalf("tracker.kind = %v, want linear", config.Tracker.Kind)
	}
	if body != "Work on {{.issue.identifier}}" {
		t.Fatalf("body = %q", body)
	}
}

func TestLoaderParsesPromptConfig(t *testing.T) {
	t.Parallel()

	config, _, err := parseWorkflow([]byte(`---
tracker:
  kind: linear
prompts:
  exec_plan_decision: |
    Decide for {{.issue.identifier}}
---
Work on {{.issue.identifier}}
`))
	if err != nil {
		t.Fatalf("parseWorkflow() error = %v", err)
	}
	if config.Prompts.ExecPlanDecision == nil {
		t.Fatal("prompts.exec_plan_decision = nil, want parsed value")
	}
	if got := *config.Prompts.ExecPlanDecision; got != "Decide for {{.issue.identifier}}" {
		t.Fatalf("prompts.exec_plan_decision = %q, want configured template", got)
	}
}

func TestResolvePathUsesDefaultWhenUnset(t *testing.T) {
	t.Setenv(WorkflowPathEnvVar, "")

	got := Loader{}.ResolvePath("")
	want := filepath.Join(".", DefaultPath)
	if got != want {
		t.Fatalf("ResolvePath(\"\") = %q, want %q", got, want)
	}
}

func TestResolvePathUsesEnvWhenExplicitPathMissing(t *testing.T) {
	t.Setenv(WorkflowPathEnvVar, "/tmp/colin-workflow.md")

	got := Loader{}.ResolvePath("")
	if got != "/tmp/colin-workflow.md" {
		t.Fatalf("ResolvePath(\"\") = %q, want %q", got, "/tmp/colin-workflow.md")
	}
}

func TestResolvePathPrefersExplicitPathOverEnv(t *testing.T) {
	t.Setenv(WorkflowPathEnvVar, "/tmp/from-env.md")

	got := Loader{}.ResolvePath("/tmp/from-flag.md")
	if got != "/tmp/from-flag.md" {
		t.Fatalf("ResolvePath(explicit) = %q, want %q", got, "/tmp/from-flag.md")
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

func TestRenderPromptIncludesPendingCheckFailure(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{PromptTemplate: `{{.issue.pending_check_failure.name}} {{.issue.colin_metadata.pending_check_failure.failure_kind}} {{.issue.pending_check_failure.head_sha}}`}
	prompt, err := RenderPrompt(def, domain.Issue{
		Identifier: "ABC-123",
		ColinMetadata: &domain.ColinMetadata{
			PendingCheckFailure: &domain.PendingPullRequestCheckFailure{
				Name:        "go test",
				FailureKind: "actual",
				HeadSHA:     "abc123",
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("RenderPrompt() error = %v", err)
	}
	if prompt != "go test actual abc123" {
		t.Fatalf("prompt = %q, want check failure context", prompt)
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

func TestTemplatePayloadSupportsIssueFields(t *testing.T) {
	t.Parallel()

	payload := TemplatePayload(domain.Issue{
		Identifier: "ABC-123",
		Title:      "Prompt config",
		Labels:     []string{"backend", "prompt"},
	}, nil)
	rendered, err := RenderTemplate(`{{.issue.identifier}} {{.issue.title}}{{range .issue.labels}} {{.}}{{end}}`, payload)
	if err != nil {
		t.Fatalf("RenderTemplate() error = %v", err)
	}
	if want := "ABC-123 Prompt config backend prompt"; rendered != want {
		t.Fatalf("rendered = %q, want %q", rendered, want)
	}
}

func TestRenderPromptIncludesExecPlan(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{PromptTemplate: `{{.issue.exec_plan.body}}`}
	prompt, err := RenderPrompt(def, domain.Issue{
		Identifier: "ABC-123",
		ExecPlan: &domain.ExecPlan{
			Body: "Plan body",
		},
	}, nil)
	if err != nil {
		t.Fatalf("RenderPrompt() error = %v", err)
	}
	if prompt != "Plan body" {
		t.Fatalf("prompt = %q, want %q", prompt, "Plan body")
	}
}

func TestRenderPromptIncludesExecPlanDecision(t *testing.T) {
	t.Parallel()

	def := domain.WorkflowDefinition{PromptTemplate: `{{.issue.colin_metadata.exec_plan_decision}}`}
	prompt, err := RenderPrompt(def, domain.Issue{
		Identifier: "ABC-123",
		ColinMetadata: &domain.ColinMetadata{
			ExecPlanDecision: domain.ExecPlanDecisionOneShot,
		},
	}, nil)
	if err != nil {
		t.Fatalf("RenderPrompt() error = %v", err)
	}
	if prompt != string(domain.ExecPlanDecisionOneShot) {
		t.Fatalf("prompt = %q, want %q", prompt, domain.ExecPlanDecisionOneShot)
	}
}
