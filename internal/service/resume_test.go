package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadResumeSessionPreparesWorkspaceAndUsesDefaultCLICommand(t *testing.T) {
	t.Parallel()

	server := newResumeTestLinearServer(t, func(w http.ResponseWriter, request map[string]any) {
		projectIDs, ok := request["projectIDs"].([]any)
		if !ok || len(projectIDs) != 1 || projectIDs[0] != "project-1" {
			t.Fatalf("IssuesByCodexThreadID projectIDs = %#v, want [project-1]", request["projectIDs"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						resumeTestIssueNode("issue-1", "COLIN-123", "thread-123"),
					},
				},
			},
		})
	})
	defer server.Close()

	workflowPath := writeResumeTestWorkflow(t, server.URL, "")

	session, err := LoadResumeSession(context.Background(), workflowPath, "thread-123")
	if err != nil {
		t.Fatalf("LoadResumeSession() error = %v", err)
	}
	if got := session.Issue.Identifier; got != "COLIN-123" {
		t.Fatalf("Issue.Identifier = %q, want %q", got, "COLIN-123")
	}
	if got := session.CLICommand; got != "codex" {
		t.Fatalf("CLICommand = %q, want %q", got, "codex")
	}
	if got := session.ThreadID; got != "thread-123" {
		t.Fatalf("ThreadID = %q, want %q", got, "thread-123")
	}
	if _, err := os.Stat(session.WorkspacePath); err != nil {
		t.Fatalf("workspace path %q was not created: %v", session.WorkspacePath, err)
	}
	if want := filepath.Join(filepath.Dir(workflowPath), ".colin", "workspaces", "COLIN-123"); session.WorkspacePath != want {
		t.Fatalf("WorkspacePath = %q, want %q", session.WorkspacePath, want)
	}
}

func TestLoadResumeSessionReturnsNotFoundError(t *testing.T) {
	t.Parallel()

	server := newResumeTestLinearServer(t, func(w http.ResponseWriter, request map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						resumeTestIssueNode("issue-1", "COLIN-123", "thread-999"),
					},
				},
			},
		})
	})
	defer server.Close()

	workflowPath := writeResumeTestWorkflow(t, server.URL, "")

	_, err := LoadResumeSession(context.Background(), workflowPath, "thread-123")
	var notFoundErr *ResumeThreadNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("LoadResumeSession() error = %v, want ResumeThreadNotFoundError", err)
	}
}

func TestLoadResumeSessionReturnsAmbiguousError(t *testing.T) {
	t.Parallel()

	server := newResumeTestLinearServer(t, func(w http.ResponseWriter, request map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						resumeTestIssueNode("issue-1", "COLIN-123", "thread-123"),
						resumeTestIssueNode("issue-2", "COLIN-456", "thread-123"),
					},
				},
			},
		})
	})
	defer server.Close()

	workflowPath := writeResumeTestWorkflow(t, server.URL, "codex --profile local")

	_, err := LoadResumeSession(context.Background(), workflowPath, "thread-123")
	var ambiguousErr *AmbiguousResumeThreadError
	if !errors.As(err, &ambiguousErr) {
		t.Fatalf("LoadResumeSession() error = %v, want AmbiguousResumeThreadError", err)
	}
	if got := strings.Join(ambiguousErr.IssueIdentifiers, ","); got != "COLIN-123,COLIN-456" {
		t.Fatalf("IssueIdentifiers = %q, want %q", got, "COLIN-123,COLIN-456")
	}
}

func TestLoadResumeSessionResolvesLinearIssueIdentifier(t *testing.T) {
	t.Parallel()

	server := newResumeTestLinearServer(t, func(w http.ResponseWriter, request map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						resumeTestIssueNode("issue-1", "COLIN-123", "thread-123"),
					},
				},
			},
		})
	})
	defer server.Close()

	workflowPath := writeResumeTestWorkflow(t, server.URL, "")

	session, err := LoadResumeSession(context.Background(), workflowPath, "COLIN-123")
	if err != nil {
		t.Fatalf("LoadResumeSession() error = %v", err)
	}
	if got := session.Issue.Identifier; got != "COLIN-123" {
		t.Fatalf("Issue.Identifier = %q, want %q", got, "COLIN-123")
	}
	if got := session.ThreadID; got != "thread-123" {
		t.Fatalf("ThreadID = %q, want %q", got, "thread-123")
	}
}

func TestLoadResumeSessionReturnsIssueNotFoundForIssueSelector(t *testing.T) {
	t.Parallel()

	server := newResumeTestLinearServer(t, func(w http.ResponseWriter, request map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						resumeTestIssueNode("issue-1", "COLIN-999", "thread-999"),
					},
				},
			},
		})
	})
	defer server.Close()

	workflowPath := writeResumeTestWorkflow(t, server.URL, "")

	_, err := LoadResumeSession(context.Background(), workflowPath, "COLIN-123")
	var issueErr *ResumeIssueNotFoundError
	if !errors.As(err, &issueErr) {
		t.Fatalf("LoadResumeSession() error = %v, want ResumeIssueNotFoundError", err)
	}
}

func TestLoadResumeSessionReturnsIssueHasNoThreadError(t *testing.T) {
	t.Parallel()

	server := newResumeTestLinearServer(t, func(w http.ResponseWriter, request map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]any{
						resumeTestIssueNode("issue-1", "COLIN-123", ""),
					},
				},
			},
		})
	})
	defer server.Close()

	workflowPath := writeResumeTestWorkflow(t, server.URL, "")

	_, err := LoadResumeSession(context.Background(), workflowPath, "COLIN-123")
	var noThreadErr *ResumeIssueHasNoThreadError
	if !errors.As(err, &noThreadErr) {
		t.Fatalf("LoadResumeSession() error = %v, want ResumeIssueHasNoThreadError", err)
	}
}

func writeResumeTestWorkflow(t *testing.T, endpoint string, cliCommand string) string {
	t.Helper()

	baseDir := t.TempDir()
	root := filepath.Join(baseDir, ".colin", "workspaces")
	workflowPath := filepath.Join(baseDir, "WORKFLOW.md")
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString("tracker:\n")
	builder.WriteString("  kind: linear\n")
	builder.WriteString("  endpoint: " + endpoint + "\n")
	builder.WriteString("  api_key: token\n")
	builder.WriteString("  project_slug: project-1\n")
	builder.WriteString("workspace:\n")
	builder.WriteString("  root: " + root + "\n")
	builder.WriteString("codex:\n")
	builder.WriteString("  command: codex app-server\n")
	if cliCommand != "" {
		builder.WriteString("  cli_command: " + cliCommand + "\n")
	}
	builder.WriteString("---\n")

	if err := os.WriteFile(workflowPath, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return workflowPath
}

func newResumeTestLinearServer(t *testing.T, issues func(http.ResponseWriter, map[string]any)) *httptest.Server {
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
											"name": "Product",
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
		case strings.Contains(request.Query, "IssuesByCodexThreadID"):
			issues(w, request.Variables)
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
}

func resumeTestIssueNode(issueID string, identifier string, threadID string) map[string]any {
	return map[string]any{
		"id":         issueID,
		"identifier": identifier,
		"title":      identifier + " title",
		"project": map[string]any{
			"id":     "project-1",
			"slugId": "project-1",
		},
		"state": map[string]any{
			"name": "Todo",
		},
		"attachments": map[string]any{
			"nodes": []map[string]any{
				{
					"id":        "attachment-" + issueID,
					"title":     "Colin metadata",
					"url":       "https://colin.invalid/linear/issues/" + issueID + "/metadata",
					"createdAt": "2026-04-03T12:00:00Z",
					"updatedAt": "2026-04-03T12:00:00Z",
					"metadata": map[string]any{
						"codex_thread_id": threadID,
					},
				},
			},
		},
	}
}
