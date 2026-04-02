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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gopsagent "github.com/google/gops/agent"

	"github.com/pmenglund/colin/internal/app"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/orchestrator"
	"github.com/pmenglund/colin/internal/repoops"
	tsdiag "github.com/pmenglund/colin/internal/tailscale"
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

func TestValidateGitHubAccessReturnsManagerError(t *testing.T) {
	t.Parallel()

	cfg := domain.ServiceConfig{Repo: domain.RepoConfig{APIToken: "test-token"}}
	manager := repoops.NewManagerWithGitHubClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), &serviceGitHubStub{validateErr: errors.New("bad credentials")})

	err := validateGitHubAccess(cfg, manager)
	if err == nil {
		t.Fatal("validateGitHubAccess() error = nil, want bad credentials")
	}
	if !strings.Contains(err.Error(), "bad credentials") {
		t.Fatalf("validateGitHubAccess() error = %v, want bad credentials", err)
	}
}

func TestPreflightTrackerConfigSkipsGitHubCheckWhenTokenMissing(t *testing.T) {
	server := newServiceLinearPreflightServer(t)
	defer server.Close()

	cfg := testServicePreflightConfig(server.URL)
	oldNewPreflightRepoManager := newPreflightRepoManager
	newPreflightRepoManager = func(domain.ServiceConfig) *repoops.Manager {
		t.Fatal("newPreflightRepoManager() should not be called when GitHub token is missing")
		return nil
	}
	defer func() {
		newPreflightRepoManager = oldNewPreflightRepoManager
	}()

	_, report, err := PreflightTrackerConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("PreflightTrackerConfig() error = %v", err)
	}
	if len(report.Checks) != 4 {
		t.Fatalf("check count = %d, want 4", len(report.Checks))
	}
	if got := report.Checks[3].Status; got != PreflightStatusSkipped {
		t.Fatalf("github check status = %q, want %q", got, PreflightStatusSkipped)
	}
	if got := report.Checks[3].Detail; !strings.Contains(got, "not configured") {
		t.Fatalf("github check detail = %q, want token guidance", got)
	}
	if !report.Ready() {
		t.Fatal("report.Ready() = false, want true when GitHub check is skipped")
	}
}

func TestPreflightTrackerConfigReturnsGitHubValidationError(t *testing.T) {
	server := newServiceLinearPreflightServer(t)
	defer server.Close()

	cfg := testServicePreflightConfig(server.URL)
	cfg.Repo.APIToken = "github_pat_test"

	oldNewPreflightRepoManager := newPreflightRepoManager
	newPreflightRepoManager = func(cfg domain.ServiceConfig) *repoops.Manager {
		return repoops.NewManagerWithGitHubClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), &serviceGitHubStub{validateErr: errors.New("bad credentials")})
	}
	defer func() {
		newPreflightRepoManager = oldNewPreflightRepoManager
	}()

	_, report, err := PreflightTrackerConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("PreflightTrackerConfig() error = nil, want bad credentials")
	}
	if !strings.Contains(err.Error(), "bad credentials") {
		t.Fatalf("PreflightTrackerConfig() error = %v, want bad credentials", err)
	}
	if got := report.Checks[3].Status; got != PreflightStatusError {
		t.Fatalf("github check status = %q, want %q", got, PreflightStatusError)
	}
	if got := report.Checks[3].Detail; !strings.Contains(got, "bad credentials") {
		t.Fatalf("github check detail = %q, want bad credentials", got)
	}
}

func TestNewEnsuresManagedLabelsDuringStartupPreflight(t *testing.T) {
	t.Parallel()

	var createdLabels []string
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
													{"name": "Refine"},
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
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			name, _ := request.Variables["name"].(string)
			nodes := []map[string]any{}
			if name == domain.PausedIssueLabel {
				nodes = append(nodes, map[string]any{
					"id":   "label-existing",
					"name": domain.PausedIssueLabel,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": nodes,
					},
				},
			})
		case strings.Contains(request.Query, "mutation CreateIssueLabel"):
			input, _ := request.Variables["input"].(map[string]any)
			name, _ := input["name"].(string)
			createdLabels = append(createdLabels, name)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabelCreate": map[string]any{
						"success": true,
						"issueLabel": map[string]any{
							"id":   "label-" + strconv.Itoa(len(createdLabels)),
							"name": name,
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
---
Work on {{ .issue.identifier }}.
`
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), workflowPath); err != nil {
		t.Fatalf("New() error = %v", err)
	}

	wantCreated := []string{
		domain.CodexReviewPendingLabel,
		domain.CodexReviewApprovedLabel,
		domain.CodexReviewUnresolvedLabel,
	}
	if len(createdLabels) != len(wantCreated) {
		t.Fatalf("created label count = %d, want %d", len(createdLabels), len(wantCreated))
	}
	for i, want := range wantCreated {
		if createdLabels[i] != want {
			t.Fatalf("createdLabels[%d] = %q, want %q", i, createdLabels[i], want)
		}
	}
}

func TestNewFailsWhenManagedLabelPreflightFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		switch {
		case strings.Contains(request.Query, "ProjectTeamStates"):
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
													{"name": "Refine"},
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
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			http.Error(w, "boom", http.StatusInternalServerError)
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
---
Work on {{ .issue.identifier }}.
`
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), workflowPath)
	if err == nil {
		t.Fatal("New() error = nil, want preflight failure")
	}
	if !strings.Contains(err.Error(), "ensure paused label") {
		t.Fatalf("New() error = %q, want label context", err)
	}
}

func TestNewFailsWhenUIAndWebhookPortsMatch(t *testing.T) {
	t.Parallel()

	server := newServiceLinearPreflightServer(t)
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
  port: 8998
  webhook_port: 8998
---
Work on {{ .issue.identifier }}.
`
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), workflowPath)
	if !errors.Is(err, errDuplicateListenerPorts) {
		t.Fatalf("New() error = %v, want errDuplicateListenerPorts", err)
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

func TestLoadGitHubTokenSetupUsesWorkflowRepositoryURL(t *testing.T) {
	t.Parallel()

	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	workflow := `---
tracker:
  kind: linear
  api_key: test-linear-key
  project_slug: test-project
workspace:
  repo_url: git@github.com:acme/widgets.git
  base_ref: main
codex:
  command: codex app-server
---
Work on {{ .issue.identifier }}.
`
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := LoadGitHubTokenSetup(workflowPath, t.TempDir())
	if err != nil {
		t.Fatalf("LoadGitHubTokenSetup() error = %v", err)
	}
	if result.RepositoryOwner != "acme" || result.RepositoryName != "widgets" {
		t.Fatalf("repository = %s/%s, want acme/widgets", result.RepositoryOwner, result.RepositoryName)
	}
	if result.RepositorySource != workflowPath {
		t.Fatalf("RepositorySource = %q, want %q", result.RepositorySource, workflowPath)
	}
	if !strings.Contains(result.FineGrainedTokenURL, "pull_requests=write") {
		t.Fatalf("FineGrainedTokenURL = %q, want pull_requests=write", result.FineGrainedTokenURL)
	}
}

func TestLoadGitHubTokenSetupFallsBackToGitRemote(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "remote", "add", "origin", "git@github.com:acme/widgets.git")

	result, err := LoadGitHubTokenSetup(filepath.Join(repoDir, "missing.md"), repoDir)
	if err != nil {
		t.Fatalf("LoadGitHubTokenSetup() error = %v", err)
	}
	if result.RepositorySource != "git remote origin" {
		t.Fatalf("RepositorySource = %q, want git remote origin", result.RepositorySource)
	}
	if result.RepositoryURL != "git@github.com:acme/widgets.git" {
		t.Fatalf("RepositoryURL = %q, want git@github.com:acme/widgets.git", result.RepositoryURL)
	}
}

func TestLoadGitHubTokenSetupFailsWithoutRepositorySource(t *testing.T) {
	t.Parallel()

	_, err := LoadGitHubTokenSetup(filepath.Join(t.TempDir(), "missing.md"), t.TempDir())
	if !errors.Is(err, ErrMissingGitHubRepository) {
		t.Fatalf("LoadGitHubTokenSetup() error = %v, want ErrMissingGitHubRepository", err)
	}
}

func TestLoadGitHubWebhookSetupUsesWorkflowRepositoryAndPublicWebhookURL(t *testing.T) {
	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	workflow := `---
tracker:
  kind: linear
  api_key: test-linear-key
  project_slug: test-project
workspace:
  repo_url: git@github.com:acme/widgets.git
  base_ref: main
repo:
  backend: github
  webhook_signing_secret: $GITHUB_WEBHOOK_SECRET
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
	t.Setenv("GITHUB_WEBHOOK_SECRET", "secret")

	result, err := LoadGitHubWebhookSetup(context.Background(), workflowPath, t.TempDir())
	if err != nil {
		t.Fatalf("LoadGitHubWebhookSetup() error = %v", err)
	}
	if result.RepositoryOwner != "acme" || result.RepositoryName != "widgets" {
		t.Fatalf("repository = %s/%s, want acme/widgets", result.RepositoryOwner, result.RepositoryName)
	}
	if result.WebhookURL != "https://hooks.colin.example.test/webhooks/github" {
		t.Fatalf("WebhookURL = %q, want %q", result.WebhookURL, "https://hooks.colin.example.test/webhooks/github")
	}
	if !result.SigningSecretConfigured {
		t.Fatal("SigningSecretConfigured = false, want true")
	}
	if result.SigningSecretEnvVar != GitHubWebhookSigningSecretEnvVar {
		t.Fatalf("SigningSecretEnvVar = %q, want %q", result.SigningSecretEnvVar, GitHubWebhookSigningSecretEnvVar)
	}
	if got := strings.Join(result.Events, ","); got != "pull_request,pull_request_review,pull_request_review_comment,pull_request_review_thread,reaction" {
		t.Fatalf("Events = %q", got)
	}
}

func TestLoadGitHubWebhookSetupReportsMissingSigningSecret(t *testing.T) {
	t.Parallel()

	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	workflow := `---
tracker:
  kind: linear
  api_key: test-linear-key
  project_slug: test-project
workspace:
  repo_url: git@github.com:acme/widgets.git
  base_ref: main
repo:
  backend: github
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

	result, err := LoadGitHubWebhookSetup(context.Background(), workflowPath, t.TempDir())
	if err != nil {
		t.Fatalf("LoadGitHubWebhookSetup() error = %v", err)
	}
	if result.SigningSecretConfigured {
		t.Fatal("SigningSecretConfigured = true, want false")
	}
}

func TestLoadLinearAppSetupUsesPublicWebhookURL(t *testing.T) {
	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	workflow := `---
tracker:
  kind: linear
  api_key: test-linear-key
  webhook_signing_secret: $LINEAR_WEBHOOK_SECRET
  project_slug: test-project
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
	t.Setenv("LINEAR_WEBHOOK_SECRET", "secret")

	result, err := LoadLinearAppSetup(context.Background(), workflowPath)
	if err != nil {
		t.Fatalf("LoadLinearAppSetup() error = %v", err)
	}
	if result.ProjectSlug != "test-project" {
		t.Fatalf("ProjectSlug = %q, want %q", result.ProjectSlug, "test-project")
	}
	if result.WebhookURL != "https://hooks.colin.example.test/webhooks/linear" {
		t.Fatalf("WebhookURL = %q, want %q", result.WebhookURL, "https://hooks.colin.example.test/webhooks/linear")
	}
	if result.ActorType != "app" {
		t.Fatalf("ActorType = %q, want %q", result.ActorType, "app")
	}
	if !result.SigningSecretConfigured {
		t.Fatal("SigningSecretConfigured = false, want true")
	}
	if result.SigningSecretEnvVar != LinearWebhookSigningSecretEnvVar {
		t.Fatalf("SigningSecretEnvVar = %q, want %q", result.SigningSecretEnvVar, LinearWebhookSigningSecretEnvVar)
	}
	if got := strings.Join(result.RequiredWebhookCategories, ","); got != "AgentSessionEvent" {
		t.Fatalf("RequiredWebhookCategories = %q, want %q", got, "AgentSessionEvent")
	}
	if got := strings.Join(result.OptionalWakeupEvents, ","); got != "Issue create,Issue update" {
		t.Fatalf("OptionalWakeupEvents = %q, want %q", got, "Issue create,Issue update")
	}
}

func TestLoadLinearAppSetupRequiresPublicWebhookURL(t *testing.T) {
	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	workflow := `---
tracker:
  kind: linear
  api_key: test-linear-key
  project_slug: test-project
codex:
  command: codex app-server
---
Work on {{ .issue.identifier }}.
`
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadLinearAppSetup(context.Background(), workflowPath)
	if !errors.Is(err, ErrMissingWebhookPublicURL) {
		t.Fatalf("LoadLinearAppSetup() error = %v, want ErrMissingWebhookPublicURL", err)
	}
}

func TestResolveWebhookPublicBaseURLPrefersExplicitWebhookPublicURL(t *testing.T) {
	t.Parallel()

	got := resolveWebhookPublicBaseURL(context.Background(), serviceInspectorStub{}, domain.ServerConfig{
		WebhookPublicURL: "https://hooks.colin.example.test/",
	}, "http://127.0.0.1:8998")
	if got != "https://hooks.colin.example.test" {
		t.Fatalf("resolveWebhookPublicBaseURL() = %q, want %q", got, "https://hooks.colin.example.test")
	}
}

func TestResolveWebhookPublicBaseURLFallsBackToInspector(t *testing.T) {
	t.Parallel()

	got := resolveWebhookPublicBaseURL(context.Background(), serviceInspectorStub{
		status: domain.FunnelSetupStatus{
			PublicBaseURL: "https://colin.tail.example.ts.net",
		},
	}, domain.ServerConfig{}, "http://127.0.0.1:8998")
	if got != "https://colin.tail.example.ts.net" {
		t.Fatalf("resolveWebhookPublicBaseURL() = %q, want %q", got, "https://colin.tail.example.ts.net")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func TestEffectiveUIBaseURLPrefersExplicitUIURL(t *testing.T) {
	t.Parallel()

	svc := &Service{
		inspector: serviceInspectorStub{
			uiBaseURL: "https://colin.tail.example.ts.net",
		},
	}

	got := svc.effectiveUIBaseURL(context.Background(), domain.ServerConfig{
		Port:  intPtr(8888),
		UIURL: "https://ui.colin.example.test",
	})
	if got != "https://ui.colin.example.test" {
		t.Fatalf("effectiveUIBaseURL() = %q", got)
	}
}

func TestEffectiveUIBaseURLUsesTailscaleServeURLWhenAvailable(t *testing.T) {
	t.Parallel()

	svc := &Service{
		inspector: serviceInspectorStub{
			uiBaseURL: "https://colin.tail.example.ts.net",
		},
	}

	got := svc.effectiveUIBaseURL(context.Background(), domain.ServerConfig{
		Port: intPtr(8888),
	})
	if got != "https://colin.tail.example.ts.net" {
		t.Fatalf("effectiveUIBaseURL() = %q", got)
	}
}

func TestEffectiveUIBaseURLFallsBackToLocalDashboard(t *testing.T) {
	t.Parallel()

	svc := &Service{
		inspector: serviceInspectorStub{},
		serverURL: "http://127.0.0.1:9999",
	}

	got := svc.effectiveUIBaseURL(context.Background(), domain.ServerConfig{
		Port: intPtr(8888),
	})
	if got != "http://127.0.0.1:9999" {
		t.Fatalf("effectiveUIBaseURL() = %q", got)
	}
}

func TestDashboardHandlerServesWebhookRoutesWhenWebhookPortUnset(t *testing.T) {
	t.Parallel()

	svc := &Service{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		serverPort: intPtr(8888),
		runtime: orchestrator.Runtime{
			Config: domain.ServiceConfig{
				Server:  domain.ServerConfig{Port: intPtr(8888)},
				Tracker: domain.TrackerConfig{},
			},
		},
	}

	handler, err := svc.newDashboardHandler()
	if err != nil {
		t.Fatalf("newDashboardHandler() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL+"/webhooks/linear", "application/json", strings.NewReader(`{"webhookTimestamp":1735689600000}`))
	if err != nil {
		t.Fatalf("POST /webhooks/linear error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestDashboardHandlerDoesNotServeWebhookRoutesWhenDedicatedWebhookPortConfigured(t *testing.T) {
	t.Parallel()

	svc := &Service{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		serverPort:  intPtr(8888),
		webhookPort: intPtr(8998),
		runtime: orchestrator.Runtime{
			Config: domain.ServiceConfig{
				Server:  domain.ServerConfig{Port: intPtr(8888), WebhookPort: intPtr(8998)},
				Tracker: domain.TrackerConfig{},
			},
		},
	}

	handler, err := svc.newDashboardHandler()
	if err != nil {
		t.Fatalf("newDashboardHandler() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL+"/webhooks/linear", "application/json", strings.NewReader(`{"webhookTimestamp":1735689600000}`))
	if err != nil {
		t.Fatalf("POST /webhooks/linear error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestStartGOPSAgentStartsWithServiceManagedCleanup(t *testing.T) {
	var mu sync.Mutex
	listenCalls := 0
	gotOptions := gopsagent.Options{}
	closeCalls := 0
	closed := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	stop := startGOPSAgent(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), gopsHooks{
		listen: func(opts gopsagent.Options) error {
			mu.Lock()
			defer mu.Unlock()
			listenCalls++
			gotOptions = opts
			return nil
		},
		close: func() {
			mu.Lock()
			closeCalls++
			mu.Unlock()
			closed <- struct{}{}
		},
	})
	cancel()

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("gops close was not triggered after context cancellation")
	}

	stop()

	mu.Lock()
	defer mu.Unlock()
	if listenCalls != 1 {
		t.Fatalf("listen calls = %d, want 1", listenCalls)
	}
	if gotOptions.ShutdownCleanup {
		t.Fatal("ShutdownCleanup = true, want false so Colin keeps signal ownership")
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
}

func TestStartGOPSAgentReturnsNoopStopWhenListenFails(t *testing.T) {
	t.Parallel()

	stop := startGOPSAgent(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), gopsHooks{
		listen: func(gopsagent.Options) error {
			return errors.New("boom")
		},
		close: func() {
			t.Fatal("close should not be called when listen fails")
		},
	})

	stop()
}

func TestShouldQueueImmediateLinearRefresh(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		event     app.LinearWebhookEvent
		projectID string
		want      bool
	}{
		{
			name: "create in watched project",
			event: app.LinearWebhookEvent{
				Action:    "create",
				ProjectID: "project-1",
			},
			projectID: "project-1",
			want:      true,
		},
		{
			name: "update with relevant field in watched project",
			event: app.LinearWebhookEvent{
				Action:        "update",
				ProjectID:     "project-1",
				ChangedFields: []string{"stateid", "updatedat"},
			},
			projectID: "project-1",
			want:      true,
		},
		{
			name: "update with irrelevant field in watched project",
			event: app.LinearWebhookEvent{
				Action:        "update",
				ProjectID:     "project-1",
				ChangedFields: []string{"updatedat"},
			},
			projectID: "project-1",
			want:      false,
		},
		{
			name: "update in different project",
			event: app.LinearWebhookEvent{
				Action:        "update",
				ProjectID:     "project-2",
				ChangedFields: []string{"stateid"},
			},
			projectID: "project-1",
			want:      false,
		},
		{
			name: "missing watched project id",
			event: app.LinearWebhookEvent{
				Action:        "update",
				ProjectID:     "project-1",
				ChangedFields: []string{"stateid"},
			},
			projectID: "",
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldQueueImmediateLinearRefresh(tc.event, tc.projectID); got != tc.want {
				t.Fatalf("shouldQueueImmediateLinearRefresh() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestShouldQueueImmediateGitHubRefresh(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		event       app.GitHubWebhookEvent
		watchedRepo string
		want        bool
	}{
		{
			name: "pull request review in watched repository",
			event: app.GitHubWebhookEvent{
				Event:              "pull_request_review",
				Action:             "submitted",
				RepositoryFullName: "acme/widgets",
				HasPullRequest:     true,
			},
			watchedRepo: "acme/widgets",
			want:        true,
		},
		{
			name: "repository name match is case insensitive",
			event: app.GitHubWebhookEvent{
				Event:              "pull_request",
				Action:             "synchronize",
				RepositoryFullName: "Acme/Widgets",
				HasPullRequest:     true,
			},
			watchedRepo: "acme/widgets",
			want:        true,
		},
		{
			name: "irrelevant action",
			event: app.GitHubWebhookEvent{
				Event:              "pull_request",
				Action:             "assigned",
				RepositoryFullName: "acme/widgets",
				HasPullRequest:     true,
			},
			watchedRepo: "acme/widgets",
			want:        false,
		},
		{
			name: "different repository",
			event: app.GitHubWebhookEvent{
				Event:              "pull_request_review_comment",
				Action:             "created",
				RepositoryFullName: "acme/other",
				HasPullRequest:     true,
			},
			watchedRepo: "acme/widgets",
			want:        false,
		},
		{
			name: "missing watched repository",
			event: app.GitHubWebhookEvent{
				Event:              "pull_request_review",
				Action:             "submitted",
				RepositoryFullName: "acme/widgets",
				HasPullRequest:     true,
			},
			watchedRepo: "",
			want:        false,
		},
		{
			name: "missing pull request context",
			event: app.GitHubWebhookEvent{
				Event:              "reaction",
				Action:             "created",
				RepositoryFullName: "acme/widgets",
			},
			watchedRepo: "acme/widgets",
			want:        false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldQueueImmediateGitHubRefresh(tc.event, tc.watchedRepo); got != tc.want {
				t.Fatalf("shouldQueueImmediateGitHubRefresh() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestWatchedRepositoryFullNameUsesConfiguredRepoURL(t *testing.T) {
	t.Parallel()

	cfg := domain.ServiceConfig{
		Workspace: domain.WorkspaceConfig{
			RepoURL: "git@github.com:acme/widgets.git",
		},
		Repo: domain.RepoConfig{
			Backend: "github",
		},
	}

	if got := watchedRepositoryFullName(cfg); got != "acme/widgets" {
		t.Fatalf("watchedRepositoryFullName() = %q, want %q", got, "acme/widgets")
	}
}

func TestWatchedProjectIDUsesProviderWhenAvailable(t *testing.T) {
	t.Parallel()

	if got := watchedProjectID(providerStub{}); got != "project-1" {
		t.Fatalf("watchedProjectID() = %q, want %q", got, "project-1")
	}
}

type serviceInspectorStub struct {
	status    domain.FunnelSetupStatus
	uiBaseURL string
}

type providerStub struct{}

func (providerStub) WatchedProjectID() string {
	return "project-1"
}

func (s serviceInspectorStub) Check(context.Context, tsdiag.Options) domain.FunnelSetupStatus {
	return s.status
}

func (s serviceInspectorStub) Resolve(context.Context, tsdiag.Options) domain.FunnelSetupStatus {
	return s.status
}

func (s serviceInspectorStub) ResolveUIBaseURL(context.Context, *int) string {
	return s.uiBaseURL
}

type serviceTrackerStub struct {
	ensuredLabels []string
}

type serviceGitHubStub struct {
	validateErr error
}

func (s *serviceGitHubStub) ValidateAuth(context.Context) error {
	return s.validateErr
}

func newServiceLinearPreflightServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
													{"name": "Refine"},
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
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			name, _ := request.Variables["name"].(string)
			nodes := []map[string]any{}
			if name == domain.PausedIssueLabel {
				nodes = append(nodes, map[string]any{
					"id":   "label-existing",
					"name": domain.PausedIssueLabel,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": nodes,
					},
				},
			})
		case strings.Contains(request.Query, "mutation CreateIssueLabel"):
			input, _ := request.Variables["input"].(map[string]any)
			name, _ := input["name"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabelCreate": map[string]any{
						"success": true,
						"issueLabel": map[string]any{
							"id":   "label-" + name,
							"name": name,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
}

func testServicePreflightConfig(endpoint string) domain.ServiceConfig {
	return domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:           "linear",
			Endpoint:       endpoint,
			APIKey:         "test-linear-key",
			ProjectSlug:    "test-project",
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Done"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
		Codex: domain.CodexConfig{
			Command: "codex app-server",
		},
	}
}

func (s *serviceGitHubStub) PullRequestByHead(context.Context, string, string, string, string) (*repoops.GitHubPullRequest, error) {
	return nil, nil
}

func (s *serviceGitHubStub) PullRequestByNumber(context.Context, string, string, int) (*repoops.GitHubPullRequest, error) {
	return nil, nil
}

func (s *serviceGitHubStub) CreatePullRequest(context.Context, string, string, repoops.CreatePullRequestInput) (*repoops.GitHubPullRequest, error) {
	return nil, nil
}

func (s *serviceGitHubStub) MergePullRequest(context.Context, string, string, int, string) error {
	return nil
}

func (s *serviceGitHubStub) BranchExists(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func (s *serviceGitHubStub) ReviewThreads(context.Context, string, string, int, string) (repoops.GitHubReviewThreadPage, error) {
	return repoops.GitHubReviewThreadPage{}, nil
}

func (s *serviceGitHubStub) ReviewThreadComments(context.Context, string, string) (repoops.GitHubReviewThreadCommentPage, error) {
	return repoops.GitHubReviewThreadCommentPage{}, nil
}

func (s *serviceGitHubStub) PullRequestReactions(context.Context, string, string, int, string) (repoops.GitHubReactionPage, error) {
	return repoops.GitHubReactionPage{}, nil
}

func (s *serviceGitHubStub) ReplyToReviewThread(context.Context, string, string) error {
	return nil
}

func (s *serviceGitHubStub) ResolveReviewThread(context.Context, string) error {
	return nil
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

func (s *serviceTrackerStub) CurrentRateLimits() domain.RateLimitSnapshot {
	return nil
}
