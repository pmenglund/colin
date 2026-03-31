package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/orchestrator"
	"github.com/pmenglund/colin/internal/tracker/linear"
)

func TestNewLoggerSuppressesInfoWhenNotVerbose(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := newLogger(&output, false)

	logger.Info("hidden")
	logger.Error("visible")

	got := output.String()
	if strings.Contains(got, "hidden") {
		t.Fatalf("logger output = %q, unexpected info log", got)
	}
	if !strings.Contains(got, "visible") {
		t.Fatalf("logger output = %q, missing error log", got)
	}
}

func TestNewLoggerIncludesInfoWhenVerbose(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := newLogger(&output, true)

	logger.Info("visible")

	if got := output.String(); !strings.Contains(got, "visible") {
		t.Fatalf("logger output = %q, missing info log", got)
	}
}

func TestNewFailsWhenRequiredLinearStateIsMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !strings.Contains(request.Query, "ProjectTeamStates") {
			t.Fatalf("unexpected query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"projects": map[string]any{
					"nodes": []map[string]any{
						{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{
									{
										"id":   "team-1",
										"name": "Colin",
										"states": map[string]any{
											"nodes": []map[string]any{
												{"name": "Todo"},
												{"name": "In Progress"},
												{"name": "Review"},
												{"name": "Merge"},
												{"name": "Done"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	workflow := `---
tracker:
  kind: linear
  endpoint: ` + server.URL + `
  api_key: test-linear-key
  project_slug: test-project
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
repo:
  publish_states:
    - Review
  merge_states:
    - Merge
codex:
  command: codex app-server
---
Work on {{ .issue.identifier }}.
`
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), workflowPath)
	if !errors.Is(err, linear.ErrMissingWorkflowState) {
		t.Fatalf("New() error = %v, want linear.ErrMissingWorkflowState", err)
	}
}

func TestEnsureManagedLabelsEnsuresPausedAndCodexReviewLabels(t *testing.T) {
	t.Parallel()

	tracker := &serviceTrackerStub{}
	svc := &Service{
		runtime: orchestrator.Runtime{
			Tracker: tracker,
		},
	}

	if err := svc.ensureManagedLabels(context.Background()); err != nil {
		t.Fatalf("ensureManagedLabels() error = %v", err)
	}

	if len(tracker.ensuredLabels) != len(domain.ManagedIssueLabels()) {
		t.Fatalf("ensured label count = %d, want %d", len(tracker.ensuredLabels), len(domain.ManagedIssueLabels()))
	}
	for i, want := range domain.ManagedIssueLabels() {
		if tracker.ensuredLabels[i] != want {
			t.Fatalf("ensuredLabels[%d] = %q, want %q", i, tracker.ensuredLabels[i], want)
		}
	}
}

func TestSetupLinearWebhookCreatesManagedWebhook(t *testing.T) {
	t.Parallel()

	var createdInput map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectTeamStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"nodes": []map[string]any{{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{{
									"id":   "team-1",
									"name": "Colin",
									"states": map[string]any{
										"nodes": []map[string]any{
											{"name": "Todo"},
											{"name": "In Progress"},
											{"name": "Review"},
											{"name": "Merge"},
											{"name": "Done"},
											{"name": "Refine"},
										},
									},
								}},
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "ProjectTeamInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"nodes": []map[string]any{{
							"id": "project-1",
							"teams": map[string]any{
								"nodes": []map[string]any{{
									"id":   "team-1",
									"name": "Colin",
								}},
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "OrganizationWebhooks"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhooks": map[string]any{
						"nodes": []map[string]any{},
					},
				},
			})
		case strings.Contains(request.Query, "CreateWebhook"):
			createdInput, _ = request.Variables["input"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookCreate": map[string]any{
						"success": true,
						"webhook": map[string]any{
							"id":      "webhook-1",
							"enabled": true,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	workflow := `---
tracker:
  kind: linear
  endpoint: ` + server.URL + `
  api_key: test-linear-key
  project_slug: test-project
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
repo:
  publish_states:
    - Review
  merge_states:
    - Merge
codex:
  command: codex app-server
server:
  webhook_public_url: https://hooks.colin.example.test
---
Work on {{ .issue.identifier }}.
`
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := SetupLinearWebhook(context.Background(), workflowPath, "colin")
	if err != nil {
		t.Fatalf("SetupLinearWebhook() error = %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("Action = %q, want %q", result.Action, "created")
	}
	if result.WebhookURL != "https://hooks.colin.example.test/webhooks/linear" {
		t.Fatalf("WebhookURL = %q", result.WebhookURL)
	}
	if result.WebhookID != "webhook-1" {
		t.Fatalf("WebhookID = %q, want %q", result.WebhookID, "webhook-1")
	}
	if result.WebhookName != "colin" {
		t.Fatalf("WebhookName = %q, want %q", result.WebhookName, "colin")
	}
	if result.TeamID != "team-1" {
		t.Fatalf("TeamID = %q, want %q", result.TeamID, "team-1")
	}
	if got, _ := createdInput["url"].(string); got != "https://hooks.colin.example.test/webhooks/linear" {
		t.Fatalf("create input url = %q", got)
	}
	if got, _ := createdInput["teamId"].(string); got != "team-1" {
		t.Fatalf("create input teamId = %q", got)
	}
	if got, _ := createdInput["label"].(string); got != "colin" {
		t.Fatalf("create input label = %q", got)
	}
}

type serviceTrackerStub struct {
	ensuredLabels []string
}

func (s *serviceTrackerStub) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	return nil, nil
}

func (s *serviceTrackerStub) FetchIssuesByStates(context.Context, []string) ([]domain.Issue, error) {
	return nil, nil
}

func (s *serviceTrackerStub) FetchIssueStatesByIDs(context.Context, []string) ([]domain.Issue, error) {
	return nil, nil
}

func (s *serviceTrackerStub) FetchIssueByID(context.Context, string) (domain.Issue, error) {
	return domain.Issue{}, nil
}

func (s *serviceTrackerStub) UpdateIssueState(context.Context, string, string) error {
	return nil
}

func (s *serviceTrackerStub) EnsureIssueLabel(_ context.Context, labelName string) error {
	s.ensuredLabels = append(s.ensuredLabels, labelName)
	return nil
}

func (s *serviceTrackerStub) AddIssueLabel(context.Context, string, string) error {
	return nil
}

func (s *serviceTrackerStub) RemoveIssueLabel(context.Context, string, string) error {
	return nil
}

func (s *serviceTrackerStub) ResolveGitAutomationState(context.Context, string, string, string) (string, bool, error) {
	return "", false, nil
}

func (s *serviceTrackerStub) CreateIssueComment(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *serviceTrackerStub) CreateCommentReply(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (s *serviceTrackerStub) UpsertIssueMetadata(context.Context, string, domain.ColinMetadata) (domain.ColinMetadata, error) {
	return domain.ColinMetadata{}, nil
}

func (s *serviceTrackerStub) UpsertIssueExecPlan(context.Context, string, domain.ExecPlan) (domain.ExecPlan, error) {
	return domain.ExecPlan{}, nil
}

func (s *serviceTrackerStub) CurrentRateLimits() map[string]any {
	return nil
}
