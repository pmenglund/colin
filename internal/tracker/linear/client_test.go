package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/tracker"
)

func TestNewValidatesWorkflowStates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
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
	}))
	defer server.Close()

	client, err := New(domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:           "linear",
			Endpoint:       server.URL,
			APIKey:         "token",
			ProjectSlug:    "project-1",
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
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client == nil {
		t.Fatal("New() returned nil client")
	}
	if got := client.WatchedProjectID(); got != "project-1" {
		t.Fatalf("WatchedProjectID() = %q, want %q", got, "project-1")
	}
}

func TestNewFailsWhenWorkflowStateMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
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

	_, err := New(domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:           "linear",
			Endpoint:       server.URL,
			APIKey:         "token",
			ProjectSlug:    "project-1",
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
	})
	if !errors.Is(err, ErrMissingWorkflowState) {
		t.Fatalf("New() error = %v, want ErrMissingWorkflowState", err)
	}
	if !strings.Contains(err.Error(), "Refine") {
		t.Fatalf("New() error = %q, want missing Refine state", err)
	}
}

func TestNewTracksMultipleWatchedProjects(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		slug, _ := request.Variables["slug"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"projects": map[string]any{
					"nodes": []map[string]any{
						{
							"id": slug + "-id",
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
	}))
	defer server.Close()

	client, err := New(domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:           "linear",
			Endpoint:       server.URL,
			APIKey:         "token",
			ProjectSlug:    "project-1",
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Done"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
		Targets: []domain.TargetConfig{
			{ProjectSlug: "project-1"},
			{ProjectSlug: "project-2"},
		},
		Codex: domain.CodexConfig{
			Command: "codex app-server",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	got := client.WatchedProjectIDs()
	if len(got) != 2 || got[0] != "project-1-id" || got[1] != "project-2-id" {
		t.Fatalf("WatchedProjectIDs() = %v, want [project-1-id project-2-id]", got)
	}
}

func TestListProjectsPaginatesAndSortsByName(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		requestCount++

		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !strings.Contains(request.Query, "ProjectList") {
			t.Fatalf("unexpected query: %s", request.Query)
		}

		variables := request.Variables
		after, _ := variables["after"].(string)
		switch requestCount {
		case 1:
			if after != "" {
				t.Fatalf("after = %q on first request, want empty", after)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"pageInfo": map[string]any{
							"hasNextPage": true,
							"endCursor":   "cursor-1",
						},
						"nodes": []map[string]any{
							{
								"name":   "Zulu",
								"slugId": "zulu",
								"teams": map[string]any{
									"nodes": []map[string]any{{"name": "Product"}},
								},
							},
						},
					},
				},
			})
		case 2:
			if after != "cursor-1" {
				t.Fatalf("after = %q on second request, want cursor-1", after)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"pageInfo": map[string]any{
							"hasNextPage": false,
							"endCursor":   nil,
						},
						"nodes": []map[string]any{
							{
								"name":   "Alpha",
								"slugId": "alpha",
								"teams": map[string]any{
									"nodes": []map[string]any{{"name": "Platform"}, {"name": "Infra"}},
								},
							},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected request count: %d", requestCount)
		}
	}))
	defer server.Close()

	projects, err := ListProjects(context.Background(), server.URL, "token")
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("requestCount = %d, want 2", requestCount)
	}
	if len(projects) != 2 {
		t.Fatalf("len(projects) = %d, want 2", len(projects))
	}
	if projects[0].Slug != "alpha" || projects[1].Slug != "zulu" {
		t.Fatalf("projects = %#v, want alphabetical order by name", projects)
	}
	if got := projects[0].TeamNames; len(got) != 2 || got[0] != "Platform" || got[1] != "Infra" {
		t.Fatalf("projects[0].TeamNames = %#v, want [Platform Infra]", got)
	}
}

func TestNewAllowsTerminalStateAliasesWhenOneExists(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
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
												{"name": "Canceled"},
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
	}))
	defer server.Close()

	client, err := New(domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:           "linear",
			Endpoint:       server.URL,
			APIKey:         "token",
			ProjectSlug:    "project-1",
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Cancelled", "Canceled"},
		},
		Repo: domain.RepoConfig{
			PublishStates: []string{"Review"},
			MergeStates:   []string{"Merge"},
		},
		Codex: domain.CodexConfig{
			Command: "codex app-server",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client == nil {
		t.Fatal("New() returned nil client")
	}
}

func TestCreateIssueComment(t *testing.T) {
	t.Parallel()

	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		input, _ := request.Variables["input"].(map[string]any)
		gotBody, _ = input["body"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"commentCreate": map[string]any{
					"success": true,
					"comment": map[string]any{
						"id": "comment-1",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	commentID, err := client.CreateIssueComment(context.Background(), "issue-1", "hello world")
	if err != nil {
		t.Fatalf("CreateIssueComment() error = %v", err)
	}
	if commentID != "comment-1" {
		t.Fatalf("commentID = %q, want %q", commentID, "comment-1")
	}
	if gotBody != "hello world" {
		t.Fatalf("body = %q, want %q", gotBody, "hello world")
	}
}

func TestCreateCommentReply(t *testing.T) {
	t.Parallel()

	var gotParentID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		input, _ := request.Variables["input"].(map[string]any)
		gotParentID, _ = input["parentId"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"commentCreate": map[string]any{
					"success": true,
					"comment": map[string]any{
						"id": "comment-2",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	commentID, err := client.CreateCommentReply(context.Background(), "issue-1", "comment-1", "reply")
	if err != nil {
		t.Fatalf("CreateCommentReply() error = %v", err)
	}
	if commentID != "comment-2" {
		t.Fatalf("commentID = %q, want %q", commentID, "comment-2")
	}
	if gotParentID != "comment-1" {
		t.Fatalf("parentId = %q, want %q", gotParentID, "comment-1")
	}
}

func TestEnsureProjectIssueWebhookCreatesMissingWebhook(t *testing.T) {
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

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	result, err := client.EnsureProjectIssueWebhook(context.Background(), "https://hooks.colin.example.test/webhooks/linear", "colin")
	if err != nil {
		t.Fatalf("EnsureProjectIssueWebhook() error = %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("Action = %q, want %q", result.Action, "created")
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
	resourceTypes, _ := createdInput["resourceTypes"].([]any)
	if len(resourceTypes) != 1 || resourceTypes[0] != linearWebhookResourceTypeIssue {
		t.Fatalf("resourceTypes = %#v, want [%q]", resourceTypes, linearWebhookResourceTypeIssue)
	}
}

func TestEnsureProjectIssueWebhookLeavesMatchingWebhookUnchanged(t *testing.T) {
	t.Parallel()

	createCalls := 0
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		switch {
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
						"nodes": []map[string]any{{
							"id":      "webhook-1",
							"label":   "colin",
							"url":     "https://hooks.colin.example.test/webhooks/linear",
							"enabled": true,
							"team": map[string]any{
								"id":   "team-1",
								"name": "Colin",
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "CreateWebhook"):
			createCalls++
			t.Fatalf("CreateWebhook should not be called")
		case strings.Contains(request.Query, "DeleteWebhook"):
			deleteCalls++
			t.Fatalf("DeleteWebhook should not be called")
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	result, err := client.EnsureProjectIssueWebhook(context.Background(), "https://hooks.colin.example.test/webhooks/linear", "colin")
	if err != nil {
		t.Fatalf("EnsureProjectIssueWebhook() error = %v", err)
	}
	if result.Action != "unchanged" {
		t.Fatalf("Action = %q, want %q", result.Action, "unchanged")
	}
	if createCalls != 0 || deleteCalls != 0 {
		t.Fatalf("createCalls=%d deleteCalls=%d, want 0", createCalls, deleteCalls)
	}
}

func TestEnsureProjectIssueWebhookReplacesManagedWebhookAtOldURL(t *testing.T) {
	t.Parallel()

	deletedIDs := []string{}
	createCalls := 0
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
						"nodes": []map[string]any{{
							"id":      "webhook-old",
							"label":   "colin",
							"url":     "https://old.colin.example.test/webhooks/linear",
							"enabled": true,
							"team": map[string]any{
								"id":   "team-1",
								"name": "Colin",
							},
						}},
					},
				},
			})
		case strings.Contains(request.Query, "DeleteWebhook"):
			id, _ := request.Variables["id"].(string)
			deletedIDs = append(deletedIDs, id)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookDelete": map[string]any{
						"success": true,
					},
				},
			})
		case strings.Contains(request.Query, "CreateWebhook"):
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookCreate": map[string]any{
						"success": true,
						"webhook": map[string]any{
							"id":      "webhook-new",
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

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	result, err := client.EnsureProjectIssueWebhook(context.Background(), "https://hooks.colin.example.test/webhooks/linear", "colin")
	if err != nil {
		t.Fatalf("EnsureProjectIssueWebhook() error = %v", err)
	}
	if result.Action != "replaced" {
		t.Fatalf("Action = %q, want %q", result.Action, "replaced")
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != "webhook-old" {
		t.Fatalf("deletedIDs = %#v, want [webhook-old]", deletedIDs)
	}
}

func TestUpsertIssueMetadata(t *testing.T) {
	t.Parallel()

	var (
		gotIssueID  string
		gotTitle    string
		gotURL      string
		gotMetadata map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		input, _ := request.Variables["input"].(map[string]any)
		gotIssueID, _ = input["issueId"].(string)
		gotTitle, _ = input["title"].(string)
		gotURL, _ = input["url"].(string)
		gotMetadata, _ = input["metadata"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"attachmentCreate": map[string]any{
					"success": true,
					"attachment": map[string]any{
						"id":       "attachment-1",
						"title":    gotTitle,
						"url":      gotURL,
						"metadata": gotMetadata,
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	now := time.Date(2026, 3, 29, 17, 0, 0, 0, time.UTC)
	metadata, err := client.UpsertIssueMetadata(context.Background(), "issue-1", domain.ColinMetadata{
		CodexThreadID:           "thread-1",
		ProgressRootCommentID:   "comment-root-1",
		ActualBranchName:        "colin-94",
		ExecPlanDecision:        domain.ExecPlanDecisionOneShot,
		ReviewPublishDirective:  domain.ReviewPublishDirectiveSkip,
		LastRunType:             "coding",
		LastOutcome:             "needs_spec",
		LastSummaryCommentID:    "comment-1",
		PullRequestNumber:       11,
		PullRequestURL:          "https://github.com/pmenglund/colin/pull/11",
		PullRequestState:        "OPEN",
		PullRequestHeadRef:      "pmenglund/colin-94",
		PullRequestBaseRef:      "main",
		LoopFailureFingerprint:  "review_publish\nReview\nno commits",
		LoopFailureCount:        2,
		PausedAt:                &now,
		PausedRunType:           "review_publish",
		PausedState:             "Review",
		PausedReason:            "no commits between main and branch",
		SlackChannelID:          "C12345678",
		SlackMessageTS:          "1743270000.123456",
		SlackPermalink:          "https://example.slack.com/archives/C12345678/p1743270000123456",
		SlackSummaryFingerprint: "fp-1",
		UpdatedAt:               &now,
	})
	if err != nil {
		t.Fatalf("UpsertIssueMetadata() error = %v", err)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("issueId = %q, want %q", gotIssueID, "issue-1")
	}
	if gotTitle != "Colin metadata" {
		t.Fatalf("title = %q, want %q", gotTitle, "Colin metadata")
	}
	if gotURL != "https://colin.example.test/root/linear/issues/issue-1/metadata" {
		t.Fatalf("url = %q, want %q", gotURL, "https://colin.example.test/root/linear/issues/issue-1/metadata")
	}
	if gotMetadata["review_publish_directive"] != "skip" {
		t.Fatalf("review_publish_directive = %v, want skip", gotMetadata["review_publish_directive"])
	}
	if gotMetadata["codex_thread_id"] != "thread-1" {
		t.Fatalf("codex_thread_id = %v, want thread-1", gotMetadata["codex_thread_id"])
	}
	if gotMetadata["progress_root_comment_id"] != "comment-root-1" {
		t.Fatalf("progress_root_comment_id = %v, want comment-root-1", gotMetadata["progress_root_comment_id"])
	}
	if gotMetadata["exec_plan_decision"] != string(domain.ExecPlanDecisionOneShot) {
		t.Fatalf("exec_plan_decision = %v, want %q", gotMetadata["exec_plan_decision"], domain.ExecPlanDecisionOneShot)
	}
	if gotMetadata["pull_request_number"] != float64(11) && gotMetadata["pull_request_number"] != 11 {
		t.Fatalf("pull_request_number = %v, want 11", gotMetadata["pull_request_number"])
	}
	if gotMetadata["pull_request_url"] != "https://github.com/pmenglund/colin/pull/11" {
		t.Fatalf("pull_request_url = %v, want GitHub PR URL", gotMetadata["pull_request_url"])
	}
	if gotMetadata["pull_request_head_ref"] != "pmenglund/colin-94" {
		t.Fatalf("pull_request_head_ref = %v, want pmenglund/colin-94", gotMetadata["pull_request_head_ref"])
	}
	if gotMetadata["pull_request_base_ref"] != "main" {
		t.Fatalf("pull_request_base_ref = %v, want main", gotMetadata["pull_request_base_ref"])
	}
	if gotMetadata["loop_failure_fingerprint"] != "review_publish\nReview\nno commits" {
		t.Fatalf("loop_failure_fingerprint = %v", gotMetadata["loop_failure_fingerprint"])
	}
	if gotMetadata["loop_failure_count"] != float64(2) && gotMetadata["loop_failure_count"] != 2 {
		t.Fatalf("loop_failure_count = %v", gotMetadata["loop_failure_count"])
	}
	if gotMetadata["paused_run_type"] != "review_publish" {
		t.Fatalf("paused_run_type = %v", gotMetadata["paused_run_type"])
	}
	if gotMetadata["paused_state"] != "Review" {
		t.Fatalf("paused_state = %v", gotMetadata["paused_state"])
	}
	if gotMetadata["paused_reason"] != "no commits between main and branch" {
		t.Fatalf("paused_reason = %v", gotMetadata["paused_reason"])
	}
	if gotMetadata["slack_channel_id"] != "C12345678" {
		t.Fatalf("slack_channel_id = %v, want C12345678", gotMetadata["slack_channel_id"])
	}
	if gotMetadata["slack_message_ts"] != "1743270000.123456" {
		t.Fatalf("slack_message_ts = %v, want 1743270000.123456", gotMetadata["slack_message_ts"])
	}
	if gotMetadata["slack_permalink"] != "https://example.slack.com/archives/C12345678/p1743270000123456" {
		t.Fatalf("slack_permalink = %v, want permalink", gotMetadata["slack_permalink"])
	}
	if gotMetadata["slack_summary_fingerprint"] != "fp-1" {
		t.Fatalf("slack_summary_fingerprint = %v, want fp-1", gotMetadata["slack_summary_fingerprint"])
	}
	if gotMetadata["actual_branch_name"] != "colin-94" {
		t.Fatalf("actual_branch_name = %v, want colin-94", gotMetadata["actual_branch_name"])
	}
	if metadata.AttachmentID != "attachment-1" {
		t.Fatalf("metadata.AttachmentID = %q, want %q", metadata.AttachmentID, "attachment-1")
	}
	if metadata.URL != "https://colin.example.test/root/linear/issues/issue-1/metadata" {
		t.Fatalf("metadata.URL = %q, want metadata attachment URL", metadata.URL)
	}
	if metadata.CodexThreadID != "thread-1" {
		t.Fatalf("metadata.CodexThreadID = %q, want thread-1", metadata.CodexThreadID)
	}
	if metadata.ProgressRootCommentID != "comment-root-1" {
		t.Fatalf("metadata.ProgressRootCommentID = %q, want comment-root-1", metadata.ProgressRootCommentID)
	}
	if metadata.ActualBranchName != "colin-94" {
		t.Fatalf("metadata.ActualBranchName = %q, want %q", metadata.ActualBranchName, "colin-94")
	}
	if metadata.ExecPlanDecision != domain.ExecPlanDecisionOneShot {
		t.Fatalf("metadata.ExecPlanDecision = %q, want %q", metadata.ExecPlanDecision, domain.ExecPlanDecisionOneShot)
	}
	if metadata.PullRequestNumber != 11 {
		t.Fatalf("metadata.PullRequestNumber = %d, want 11", metadata.PullRequestNumber)
	}
	if metadata.PullRequestHeadRef != "pmenglund/colin-94" {
		t.Fatalf("metadata.PullRequestHeadRef = %q, want %q", metadata.PullRequestHeadRef, "pmenglund/colin-94")
	}
	if metadata.LoopFailureCount != 2 {
		t.Fatalf("metadata.LoopFailureCount = %d, want 2", metadata.LoopFailureCount)
	}
	if metadata.SlackPermalink != "https://example.slack.com/archives/C12345678/p1743270000123456" {
		t.Fatalf("metadata.SlackPermalink = %q, want permalink", metadata.SlackPermalink)
	}
}

func TestUpsertIssueMetadataUsesDynamicPublicURLResolver(t *testing.T) {
	t.Parallel()

	var gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		input, _ := request.Variables["input"].(map[string]any)
		gotURL, _ = input["url"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"attachmentCreate": map[string]any{
					"success": true,
					"attachment": map[string]any{
						"id":       "attachment-1",
						"title":    "Colin metadata",
						"url":      gotURL,
						"metadata": map[string]any{},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint:  server.URL,
		apiKey:    "token",
		uiBaseURL: "http://127.0.0.1:8888",
		client:    &http.Client{Timeout: 5 * time.Second},
	}
	client.SetUIBaseURLResolver(func(context.Context) string {
		return "https://colin.tail.example.ts.net"
	})

	if _, err := client.UpsertIssueMetadata(context.Background(), "issue-1", domain.ColinMetadata{}); err != nil {
		t.Fatalf("UpsertIssueMetadata() error = %v", err)
	}
	if gotURL != "https://colin.tail.example.ts.net/linear/issues/issue-1/metadata" {
		t.Fatalf("url = %q", gotURL)
	}
}

func TestEnsureIssueLabelCreatesMissingLabel(t *testing.T) {
	t.Parallel()

	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		queries = append(queries, request.Query)

		switch {
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": []map[string]any{},
					},
				},
			})
		case strings.Contains(request.Query, "mutation CreateIssueLabel"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabelCreate": map[string]any{
						"success": true,
						"issueLabel": map[string]any{
							"id":   "label-1",
							"name": "paused",
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.EnsureIssueLabel(context.Background(), domain.PausedIssueLabel); err != nil {
		t.Fatalf("EnsureIssueLabel() error = %v", err)
	}
	if got := client.labelIDs[domain.PausedIssueLabel]; got != "label-1" {
		t.Fatalf("cached label id = %q, want %q", got, "label-1")
	}
	if len(queries) != 2 {
		t.Fatalf("query count = %d, want 2", len(queries))
	}
}

func TestAddIssueLabelUsesExistingLabelID(t *testing.T) {
	t.Parallel()

	var gotIssueID string
	var gotLabelID string
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
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": []map[string]any{
							{
								"id":   "label-1",
								"name": "paused",
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation AddIssueLabel"):
			gotIssueID, _ = request.Variables["id"].(string)
			gotLabelID, _ = request.Variables["labelId"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueAddLabel": map[string]any{
						"success": true,
						"issue": map[string]any{
							"id": gotIssueID,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.AddIssueLabel(context.Background(), "issue-1", domain.PausedIssueLabel); err != nil {
		t.Fatalf("AddIssueLabel() error = %v", err)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("gotIssueID = %q, want %q", gotIssueID, "issue-1")
	}
	if gotLabelID != "label-1" {
		t.Fatalf("gotLabelID = %q, want %q", gotLabelID, "label-1")
	}
	if got := client.labelIDs[domain.PausedIssueLabel]; got != "label-1" {
		t.Fatalf("cached label id = %q, want %q", got, "label-1")
	}
}

func TestRemoveIssueLabelUsesExistingLabelID(t *testing.T) {
	t.Parallel()

	var gotIssueID string
	var gotLabelID string
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
		case strings.Contains(request.Query, "query IssueLabelsByName"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueLabels": map[string]any{
						"nodes": []map[string]any{
							{
								"id":   "label-1",
								"name": "paused",
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation RemoveIssueLabel"):
			gotIssueID, _ = request.Variables["id"].(string)
			gotLabelID, _ = request.Variables["labelId"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueRemoveLabel": map[string]any{
						"success": true,
						"issue": map[string]any{
							"id": gotIssueID,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.RemoveIssueLabel(context.Background(), "issue-1", domain.PausedIssueLabel); err != nil {
		t.Fatalf("RemoveIssueLabel() error = %v", err)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("gotIssueID = %q, want %q", gotIssueID, "issue-1")
	}
	if gotLabelID != "label-1" {
		t.Fatalf("gotLabelID = %q, want %q", gotLabelID, "label-1")
	}
}

func TestRemoveIssueLabelNoopWhenLabelMissing(t *testing.T) {
	t.Parallel()

	queryCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		queryCount++
		if !strings.Contains(request.Query, "query IssueLabelsByName") {
			t.Fatalf("unexpected query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issueLabels": map[string]any{
					"nodes": []map[string]any{},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
		labelIDs: map[string]string{},
	}

	if err := client.RemoveIssueLabel(context.Background(), "issue-1", domain.PausedIssueLabel); err != nil {
		t.Fatalf("RemoveIssueLabel() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("query count = %d, want 1", queryCount)
	}
}

func TestUpsertIssueExecPlan(t *testing.T) {
	t.Parallel()

	var (
		queryCount  int
		createCount int
		gotIssueID  string
		gotTitle    string
		gotURL      string
		gotMetadata map[string]any
	)
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
		case strings.Contains(request.Query, "query IssueExecPlans"):
			queryCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpsertIssueExecPlan"):
			createCount++
			input, _ := request.Variables["input"].(map[string]any)
			gotIssueID, _ = input["issueId"].(string)
			gotTitle, _ = input["title"].(string)
			gotURL, _ = input["url"].(string)
			gotMetadata, _ = input["metadata"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentCreate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       "attachment-2",
							"title":    gotTitle,
							"url":      gotURL,
							"metadata": gotMetadata,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	now := time.Date(2026, 3, 29, 17, 5, 0, 0, time.UTC)
	plan, err := client.UpsertIssueExecPlan(context.Background(), "issue-1", domain.ExecPlan{
		Body:      "# Plan\n\nDetails.",
		UpdatedAt: &now,
	})
	if err != nil {
		t.Fatalf("UpsertIssueExecPlan() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("queryCount = %d, want 1", queryCount)
	}
	if createCount != 1 {
		t.Fatalf("createCount = %d, want 1", createCount)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("issueId = %q, want %q", gotIssueID, "issue-1")
	}
	if gotTitle != "Colin ExecPlan" {
		t.Fatalf("title = %q, want %q", gotTitle, "Colin ExecPlan")
	}
	if gotURL != "http://127.0.0.1/linear/issues/issue-1/exec-plan" {
		t.Fatalf("url = %q, want %q", gotURL, "http://127.0.0.1/linear/issues/issue-1/exec-plan")
	}
	if gotMetadata["body"] != "# Plan\n\nDetails." {
		t.Fatalf("body = %v, want plan body", gotMetadata["body"])
	}
	if plan.AttachmentID != "attachment-2" {
		t.Fatalf("plan.AttachmentID = %q, want %q", plan.AttachmentID, "attachment-2")
	}
}

func TestUpsertIssueExecPlanUpdatesExistingAttachment(t *testing.T) {
	t.Parallel()

	var (
		queryCount      int
		updateCount     int
		gotAttachmentID string
		gotTitle        string
		gotMetadata     map[string]any
	)
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
		case strings.Contains(request.Query, "query IssueExecPlans"):
			queryCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{
								{
									"id":    "attachment-2",
									"title": "Colin ExecPlan",
									"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
									"metadata": map[string]any{
										"body": "# Old plan",
									},
								},
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpdateIssueExecPlan"):
			updateCount++
			gotAttachmentID, _ = request.Variables["id"].(string)
			input, _ := request.Variables["input"].(map[string]any)
			gotTitle, _ = input["title"].(string)
			if _, ok := input["url"]; ok {
				t.Fatal("attachment update input unexpectedly included url")
			}
			gotMetadata, _ = input["metadata"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attachmentUpdate": map[string]any{
						"success": true,
						"attachment": map[string]any{
							"id":       gotAttachmentID,
							"title":    gotTitle,
							"url":      "http://127.0.0.1/linear/issues/issue-1/exec-plan",
							"metadata": gotMetadata,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	plan, err := client.UpsertIssueExecPlan(context.Background(), "issue-1", domain.ExecPlan{
		Body:      "# Updated plan\n\n## Progress\n\n- [x] Done",
		UpdatedAt: &now,
	})
	if err != nil {
		t.Fatalf("UpsertIssueExecPlan() error = %v", err)
	}
	if queryCount != 1 {
		t.Fatalf("queryCount = %d, want 1", queryCount)
	}
	if updateCount != 1 {
		t.Fatalf("updateCount = %d, want 1", updateCount)
	}
	if gotAttachmentID != "attachment-2" {
		t.Fatalf("attachment id = %q, want %q", gotAttachmentID, "attachment-2")
	}
	if gotTitle != "Colin ExecPlan" {
		t.Fatalf("title = %q, want %q", gotTitle, "Colin ExecPlan")
	}
	if gotMetadata["body"] != "# Updated plan\n\n## Progress\n\n- [x] Done" {
		t.Fatalf("body = %v, want updated plan body", gotMetadata["body"])
	}
	if plan.AttachmentID != "attachment-2" {
		t.Fatalf("plan.AttachmentID = %q, want %q", plan.AttachmentID, "attachment-2")
	}
}

func TestUpsertIssueExecPlanRejectsDuplicates(t *testing.T) {
	t.Parallel()

	createCount := 0
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
		case strings.Contains(request.Query, "query IssueExecPlans"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"attachments": map[string]any{
							"nodes": []map[string]any{
								{
									"id":    "attachment-1",
									"title": "Colin ExecPlan",
									"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
									"metadata": map[string]any{
										"body": "# Plan A",
									},
								},
								{
									"id":    "attachment-2",
									"title": "Colin ExecPlan",
									"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
									"metadata": map[string]any{
										"body": "# Plan B",
									},
								},
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "mutation UpsertIssueExecPlan"):
			createCount++
			t.Fatalf("unexpected attachmentCreate mutation when duplicate exec plans already exist")
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	_, err := client.UpsertIssueExecPlan(context.Background(), "issue-1", domain.ExecPlan{Body: "# Plan\n\nDetails."})
	if !errors.Is(err, tracker.ErrDuplicateExecPlans) {
		t.Fatalf("UpsertIssueExecPlan() error = %v, want ErrDuplicateExecPlans", err)
	}
	if createCount != 0 {
		t.Fatalf("createCount = %d, want 0", createCount)
	}
}

func TestUpdateIssueState(t *testing.T) {
	t.Parallel()

	var gotIssueID string
	var gotStateID string

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
		case strings.Contains(request.Query, "IssueTeamStates"):
			gotIssueID, _ = request.Variables["id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"team": map[string]any{
							"states": map[string]any{
								"nodes": []map[string]any{
									{"id": "state-review", "name": "Review"},
									{"id": "state-merge", "name": "Merge"},
								},
							},
						},
					},
				},
			})
		case strings.Contains(request.Query, "UpdateIssueState"):
			gotIssueID, _ = request.Variables["id"].(string)
			gotStateID, _ = request.Variables["stateId"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{
						"success": true,
						"issue": map[string]any{
							"id":    "issue-1",
							"state": map[string]any{"name": "Review"},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	if err := client.UpdateIssueState(context.Background(), "issue-1", "Review"); err != nil {
		t.Fatalf("UpdateIssueState() error = %v", err)
	}
	if gotIssueID != "issue-1" {
		t.Fatalf("issueID = %q, want %q", gotIssueID, "issue-1")
	}
	if gotStateID != "state-review" {
		t.Fatalf("stateId = %q, want %q", gotStateID, "state-review")
	}
}

func TestUpdateIssueStateUnknownState(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"team": map[string]any{
						"states": map[string]any{
							"nodes": []map[string]any{{"id": "state-merge", "name": "Merge"}},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	err := client.UpdateIssueState(context.Background(), "issue-1", "Review")
	if !errors.Is(err, ErrUnknownState) {
		t.Fatalf("UpdateIssueState() error = %v, want ErrUnknownState", err)
	}
}

func TestCurrentRateLimitsCapturesRequestHeaders(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().UTC().Add(90 * time.Second).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Requests-Limit", "100")
		w.Header().Set("X-RateLimit-Requests-Remaining", "25")
		w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(resetAt.Unix(), 10))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes":    []map[string]any{},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		active:   []string{"Todo"},
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	if _, err := client.FetchCandidateIssues(context.Background()); err != nil {
		t.Fatalf("FetchCandidateIssues() error = %v", err)
	}

	limits := client.CurrentRateLimits()
	linearRequests, ok := limits["linear_requests"]
	if !ok {
		t.Fatalf("linear_requests missing from rate limits: %#v", limits)
	}
	if linearRequests.Limit == nil || *linearRequests.Limit != 100 {
		t.Fatalf("limit = %v, want 100", linearRequests.Limit)
	}
	if linearRequests.Remaining == nil || *linearRequests.Remaining != 25 {
		t.Fatalf("remaining = %v, want 25", linearRequests.Remaining)
	}
	if linearRequests.ResetsAt == nil || !linearRequests.ResetsAt.Equal(resetAt.UTC()) {
		t.Fatalf("resetsAt = %v, want %v", linearRequests.ResetsAt, resetAt.UTC())
	}
	if linearRequests.NextAllowedAt == nil {
		t.Fatalf("nextAllowedAt missing from rate limits: %#v", linearRequests)
	}
	if !linearRequests.NextAllowedAt.After(time.Now().UTC()) {
		t.Fatalf("nextAllowedAt = %v, want future timestamp", linearRequests.NextAllowedAt)
	}
}

func TestResolveGitAutomationStatePrefersBranchSpecificMatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issue": map[string]any{
					"team": map[string]any{
						"gitAutomationStates": map[string]any{
							"nodes": []map[string]any{
								{
									"event": "merge",
									"state": map[string]any{"name": "Merged"},
								},
								{
									"event": "merge",
									"state": map[string]any{"name": "Deployed"},
									"targetBranch": map[string]any{
										"branchPattern": "main",
										"isRegex":       false,
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

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	stateName, ok, err := client.ResolveGitAutomationState(context.Background(), "issue-1", "merge", "main")
	if err != nil {
		t.Fatalf("ResolveGitAutomationState() error = %v", err)
	}
	if !ok {
		t.Fatal("ResolveGitAutomationState() ok = false, want true")
	}
	if stateName != "Deployed" {
		t.Fatalf("stateName = %q, want %q", stateName, "Deployed")
	}
}

func TestFetchCandidateIssuesIncludesLatestHumanReviewFeedback(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 28, 18, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if !strings.Contains(request.Query, "comments(first: 50)") {
			t.Fatalf("query missing comments fetch: %s", request.Query)
		}
		if !strings.Contains(request.Query, "history(first: 100)") {
			t.Fatalf("query missing history fetch: %s", request.Query)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id":         "issue-1",
							"identifier": "COLIN-94",
							"title":      "Address review",
							"state":      map[string]any{"name": "Todo"},
							"labels":     map[string]any{"nodes": []map[string]any{}},
							"inverseRelations": map[string]any{
								"nodes": []map[string]any{},
							},
							"comments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":        "comment-old",
										"body":      "Old review cycle feedback",
										"createdAt": base.Add(20 * time.Minute).Format(time.RFC3339),
										"children":  map[string]any{"nodes": []map[string]any{}},
									},
									{
										"id":        "comment-human",
										"body":      "Address the code review feedback.",
										"createdAt": base.Add(70 * time.Minute).Format(time.RFC3339),
										"children": map[string]any{
											"nodes": []map[string]any{
												{
													"id":        "reply-human",
													"body":      "Then mark the PR comment resolved.",
													"createdAt": base.Add(71 * time.Minute).Format(time.RFC3339),
													"parentId":  "comment-human",
												},
											},
										},
									},
									{
										"id":        "comment-colin",
										"body":      "[colin] Colin started work on this issue.",
										"createdAt": base.Add(72 * time.Minute).Format(time.RFC3339),
										"children":  map[string]any{"nodes": []map[string]any{}},
									},
									{
										"id":        "comment-after",
										"body":      "This was added after the issue moved back to Todo.",
										"createdAt": base.Add(95 * time.Minute).Format(time.RFC3339),
										"children":  map[string]any{"nodes": []map[string]any{}},
									},
								},
							},
							"history": map[string]any{
								"nodes": []map[string]any{
									{
										"createdAt": base.Add(10 * time.Minute).Format(time.RFC3339),
										"fromState": map[string]any{"name": "In Progress"},
										"toState":   map[string]any{"name": "Review"},
									},
									{
										"createdAt": base.Add(30 * time.Minute).Format(time.RFC3339),
										"fromState": map[string]any{"name": "Review"},
										"toState":   map[string]any{"name": "Todo"},
									},
									{
										"createdAt": base.Add(60 * time.Minute).Format(time.RFC3339),
										"fromState": map[string]any{"name": "In Progress"},
										"toState":   map[string]any{"name": "Review"},
									},
									{
										"createdAt": base.Add(90 * time.Minute).Format(time.RFC3339),
										"fromState": map[string]any{"name": "Review"},
										"toState":   map[string]any{"name": "Todo"},
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

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		active:   []string{"Todo"},
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1", len(issues))
	}

	got := issues[0].ReviewFeedback
	if len(got) != 2 {
		t.Fatalf("review feedback length = %d, want 2", len(got))
	}
	if got[0].Body != "Address the code review feedback." {
		t.Fatalf("first review feedback = %q, want %q", got[0].Body, "Address the code review feedback.")
	}
	if got[1].Body != "Then mark the PR comment resolved." {
		t.Fatalf("second review feedback = %q, want %q", got[1].Body, "Then mark the PR comment resolved.")
	}
	if got[1].ParentID == nil || *got[1].ParentID != "comment-human" {
		t.Fatalf("reply parent id = %v, want %q", got[1].ParentID, "comment-human")
	}
}

func TestFetchCandidateIssuesDedupesRepliesReturnedAtMultipleLevels(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 28, 18, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id":         "issue-1",
							"identifier": "COLIN-94",
							"title":      "Address review",
							"state":      map[string]any{"name": "Todo"},
							"labels":     map[string]any{"nodes": []map[string]any{}},
							"inverseRelations": map[string]any{
								"nodes": []map[string]any{},
							},
							"comments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":        "comment-human",
										"body":      "Address the review feedback.",
										"createdAt": base.Add(10 * time.Minute).Format(time.RFC3339),
										"children": map[string]any{
											"nodes": []map[string]any{
												{
													"id":        "reply-human",
													"body":      "Mark the PR thread resolved.",
													"createdAt": base.Add(11 * time.Minute).Format(time.RFC3339),
													"parentId":  "comment-human",
												},
											},
										},
									},
									{
										"id":        "reply-human",
										"body":      "Mark the PR thread resolved.",
										"createdAt": base.Add(11 * time.Minute).Format(time.RFC3339),
										"parentId":  "comment-human",
										"children":  map[string]any{"nodes": []map[string]any{}},
									},
								},
							},
							"history": map[string]any{
								"nodes": []map[string]any{
									{
										"createdAt": base.Add(5 * time.Minute).Format(time.RFC3339),
										"fromState": map[string]any{"name": "In Progress"},
										"toState":   map[string]any{"name": "Review"},
									},
									{
										"createdAt": base.Add(20 * time.Minute).Format(time.RFC3339),
										"fromState": map[string]any{"name": "Review"},
										"toState":   map[string]any{"name": "Todo"},
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

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		active:   []string{"Todo"},
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1", len(issues))
	}

	got := issues[0].ReviewFeedback
	if len(got) != 2 {
		t.Fatalf("review feedback length = %d, want 2", len(got))
	}
	if got[0].Body != "Address the review feedback." {
		t.Fatalf("first review feedback = %q, want %q", got[0].Body, "Address the review feedback.")
	}
	if got[1].Body != "Mark the PR thread resolved." {
		t.Fatalf("second review feedback = %q, want %q", got[1].Body, "Mark the PR thread resolved.")
	}
}

func TestFetchCandidateIssuesExtractsColinMetadataFromAttachment(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 29, 18, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id":         "issue-1",
							"identifier": "COLIN-94",
							"title":      "Needs more detail",
							"state":      map[string]any{"name": "Review"},
							"labels":     map[string]any{"nodes": []map[string]any{}},
							"inverseRelations": map[string]any{
								"nodes": []map[string]any{},
							},
							"attachments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":    "attachment-1",
										"title": "Colin metadata",
										"url":   "https://colin.example.test/linear/issues/issue-1/metadata",
										"metadata": map[string]any{
											"codex_thread_id":           "thread-1",
											"progress_root_comment_id":  "comment-root-1",
											"actual_branch_name":        "colin-94",
											"exec_plan_decision":        "one_shot",
											"review_publish_directive":  "skip",
											"last_run_type":             "coding",
											"last_outcome":              "needs_spec",
											"last_summary_comment_id":   "comment-2",
											"loop_failure_fingerprint":  "review_publish\nReview\nno commits",
											"loop_failure_count":        3,
											"paused_at":                 base.Add(3 * time.Minute).Format(time.RFC3339),
											"paused_run_type":           "review_publish",
											"paused_state":              "Review",
											"paused_reason":             "no commits between main and branch",
											"slack_channel_id":          "C12345678",
											"slack_message_ts":          "1743270000.123456",
											"slack_permalink":           "https://example.slack.com/archives/C12345678/p1743270000123456",
											"slack_summary_fingerprint": "fp-1",
											"updated_at":                base.Add(2 * time.Minute).Format(time.RFC3339),
											"codex_output": []map[string]any{
												{
													"timestamp": base.Add(90 * time.Second).Format(time.RFC3339),
													"event":     "turn_completed",
													"message":   "Implemented the change.",
												},
											},
										},
									},
								},
							},
							"comments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":        "comment-1",
										"body":      "[colin] Ready for review.",
										"createdAt": base.Add(1 * time.Minute).Format(time.RFC3339),
										"children":  map[string]any{"nodes": []map[string]any{}},
									},
									{
										"id":        "comment-2",
										"body":      "[colin] The spec should be improved before implementation.",
										"createdAt": base.Add(2 * time.Minute).Format(time.RFC3339),
										"children":  map[string]any{"nodes": []map[string]any{}},
									},
								},
							},
							"history": map[string]any{
								"nodes": []map[string]any{},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		active:   []string{"Review"},
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1", len(issues))
	}
	if issues[0].ColinMetadata == nil {
		t.Fatal("issues[0].ColinMetadata = nil, want metadata")
	}
	if issues[0].ColinMetadata.URL != "https://colin.example.test/linear/issues/issue-1/metadata" {
		t.Fatalf("URL = %q, want metadata attachment URL", issues[0].ColinMetadata.URL)
	}
	if issues[0].ColinMetadata.ReviewPublishDirective != "skip" {
		t.Fatalf("ReviewPublishDirective = %q, want %q", issues[0].ColinMetadata.ReviewPublishDirective, "skip")
	}
	if issues[0].ColinMetadata.ExecPlanDecision != domain.ExecPlanDecisionOneShot {
		t.Fatalf("ExecPlanDecision = %q, want %q", issues[0].ColinMetadata.ExecPlanDecision, domain.ExecPlanDecisionOneShot)
	}
	if got := len(issues[0].ColinMetadata.CodexOutput); got != 1 {
		t.Fatalf("CodexOutput length = %d, want 1", got)
	}
	if got := issues[0].ColinMetadata.CodexOutput[0].Message; got != "Implemented the change." {
		t.Fatalf("CodexOutput[0].Message = %q, want %q", got, "Implemented the change.")
	}
	if issues[0].ColinMetadata.ActualBranchName != "colin-94" {
		t.Fatalf("ActualBranchName = %q, want %q", issues[0].ColinMetadata.ActualBranchName, "colin-94")
	}
	if issues[0].ColinMetadata.CodexThreadID != "thread-1" {
		t.Fatalf("CodexThreadID = %q, want thread-1", issues[0].ColinMetadata.CodexThreadID)
	}
	if issues[0].ColinMetadata.ProgressRootCommentID != "comment-root-1" {
		t.Fatalf("ProgressRootCommentID = %q, want comment-root-1", issues[0].ColinMetadata.ProgressRootCommentID)
	}
	if issues[0].ColinMetadata.LoopFailureCount != 3 {
		t.Fatalf("LoopFailureCount = %d, want 3", issues[0].ColinMetadata.LoopFailureCount)
	}
	if issues[0].ColinMetadata.PausedRunType != "review_publish" {
		t.Fatalf("PausedRunType = %q, want review_publish", issues[0].ColinMetadata.PausedRunType)
	}
	if issues[0].ColinMetadata.SlackPermalink != "https://example.slack.com/archives/C12345678/p1743270000123456" {
		t.Fatalf("SlackPermalink = %q, want permalink", issues[0].ColinMetadata.SlackPermalink)
	}
}

func TestFetchCandidateIssuesExtractsAttachedPullRequests(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"nodes": []map[string]any{
						{
							"id":         "issue-1",
							"identifier": "COLIN-112",
							"title":      "Prevent duplicate PRs",
							"state":      map[string]any{"name": "Review"},
							"labels":     map[string]any{"nodes": []map[string]any{}},
							"inverseRelations": map[string]any{
								"nodes": []map[string]any{},
							},
							"attachments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":    "attachment-1",
										"title": "PR 11",
										"url":   "https://github.com/pmenglund/colin/pull/11",
									},
									{
										"id":    "attachment-2",
										"title": "PR 14",
										"url":   "https://github.com/pmenglund/colin/pull/14",
									},
									{
										"id":    "attachment-3",
										"title": "Metadata",
										"url":   "https://colin.example.test/root/linear/issues/issue-1/metadata",
									},
								},
							},
							"comments": map[string]any{"nodes": []map[string]any{}},
							"history":  map[string]any{"nodes": []map[string]any{}},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		active:   []string{"Review"},
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1", len(issues))
	}
	if len(issues[0].AttachedPullRequests) != 2 {
		t.Fatalf("AttachedPullRequests length = %d, want 2", len(issues[0].AttachedPullRequests))
	}
	if issues[0].AttachedPullRequests[0].Number != 11 {
		t.Fatalf("AttachedPullRequests[0].Number = %d, want 11", issues[0].AttachedPullRequests[0].Number)
	}
	if issues[0].AttachedPullRequests[1].Number != 14 {
		t.Fatalf("AttachedPullRequests[1].Number = %d, want 14", issues[0].AttachedPullRequests[1].Number)
	}
}

func TestFetchCandidateIssuesExtractsExecPlanFromAttachment(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 29, 18, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id":         "issue-1",
							"identifier": "COLIN-108",
							"title":      "Add exec plans",
							"state":      map[string]any{"name": "In Progress"},
							"labels":     map[string]any{"nodes": []map[string]any{}},
							"inverseRelations": map[string]any{
								"nodes": []map[string]any{},
							},
							"attachments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":    "attachment-2",
										"title": "Colin ExecPlan",
										"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
										"metadata": map[string]any{
											"body":       "# Plan\n\nDetails.",
											"updated_at": base.Format(time.RFC3339),
										},
									},
								},
							},
							"comments": map[string]any{
								"nodes": []map[string]any{},
							},
							"history": map[string]any{
								"nodes": []map[string]any{},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		active:   []string{"In Progress"},
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1", len(issues))
	}
	if issues[0].ExecPlan == nil {
		t.Fatal("issues[0].ExecPlan = nil, want plan")
	}
	if issues[0].ExecPlanCount != 1 {
		t.Fatalf("ExecPlanCount = %d, want 1", issues[0].ExecPlanCount)
	}
	if issues[0].ExecPlan.Body != "# Plan\n\nDetails." {
		t.Fatalf("ExecPlan.Body = %q, want plan body", issues[0].ExecPlan.Body)
	}
	if issues[0].ExecPlan.URL != "http://127.0.0.1/linear/issues/issue-1/exec-plan" {
		t.Fatalf("ExecPlan.URL = %q, want attachment URL", issues[0].ExecPlan.URL)
	}
}

func TestFetchCandidateIssuesRejectsDuplicateExecPlanAttachments(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"issues": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes": []map[string]any{
						{
							"id":         "issue-1",
							"identifier": "COLIN-110",
							"title":      "Repair exec plan metadata",
							"state":      map[string]any{"name": "Todo"},
							"labels":     map[string]any{"nodes": []map[string]any{}},
							"inverseRelations": map[string]any{
								"nodes": []map[string]any{},
							},
							"attachments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":    "attachment-1",
										"title": "Colin ExecPlan",
										"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
										"metadata": map[string]any{
											"body": "# Plan A",
										},
									},
									{
										"id":    "attachment-2",
										"title": "Colin ExecPlan",
										"url":   "http://127.0.0.1/linear/issues/issue-1/exec-plan",
										"metadata": map[string]any{
											"body": "# Plan B",
										},
									},
								},
							},
							"comments": map[string]any{
								"nodes": []map[string]any{},
							},
							"history": map[string]any{
								"nodes": []map[string]any{},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL,
		apiKey:   "token",
		project:  "project-1",
		active:   []string{"Todo"},
		client:   &http.Client{Timeout: 5 * time.Second},
	}

	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1", len(issues))
	}
	if issues[0].ExecPlan != nil {
		t.Fatalf("issues[0].ExecPlan = %#v, want nil when duplicates exist", issues[0].ExecPlan)
	}
	if issues[0].ExecPlanCount != 2 {
		t.Fatalf("ExecPlanCount = %d, want 2", issues[0].ExecPlanCount)
	}
}
