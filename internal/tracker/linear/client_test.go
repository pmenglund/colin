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
		publicURL: "https://colin.example.test/root/",
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
		publicURL: "https://colin.example.test/root/",
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
		publicURL: "https://colin.example.test/root/",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	now := time.Date(2026, 3, 29, 17, 0, 0, 0, time.UTC)
	metadata, err := client.UpsertIssueMetadata(context.Background(), "issue-1", domain.ColinMetadata{
		ActualBranchName:       "colin-94",
		ReviewPublishDirective: domain.ReviewPublishDirectiveSkip,
		LastRunType:            "coding",
		LastOutcome:            "needs_spec",
		LastSummaryCommentID:   "comment-1",
		PullRequestNumber:      11,
		PullRequestURL:         "https://github.com/pmenglund/colin/pull/11",
		PullRequestState:       "OPEN",
		PullRequestHeadRef:     "pmenglund/colin-94",
		PullRequestBaseRef:     "main",
		LoopFailureFingerprint: "review_publish\nReview\nno commits",
		LoopFailureCount:       2,
		PausedAt:               &now,
		PausedRunType:          "review_publish",
		PausedState:            "Review",
		PausedReason:           "no commits between main and branch",
		UpdatedAt:              &now,
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
	if gotMetadata["actual_branch_name"] != "colin-94" {
		t.Fatalf("actual_branch_name = %v, want colin-94", gotMetadata["actual_branch_name"])
	}
	if metadata.AttachmentID != "attachment-1" {
		t.Fatalf("metadata.AttachmentID = %q, want %q", metadata.AttachmentID, "attachment-1")
	}
	if metadata.ActualBranchName != "colin-94" {
		t.Fatalf("metadata.ActualBranchName = %q, want %q", metadata.ActualBranchName, "colin-94")
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

func TestUpsertIssueExecPlan(t *testing.T) {
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
						"id":       "attachment-2",
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
	if gotIssueID != "issue-1" {
		t.Fatalf("issueId = %q, want %q", gotIssueID, "issue-1")
	}
	if gotTitle != "Colin ExecPlan" {
		t.Fatalf("title = %q, want %q", gotTitle, "Colin ExecPlan")
	}
	if gotURL != "https://colin.invalid/linear/issues/issue-1/exec-plan" {
		t.Fatalf("url = %q, want %q", gotURL, "https://colin.invalid/linear/issues/issue-1/exec-plan")
	}
	if gotMetadata["body"] != "# Plan\n\nDetails." {
		t.Fatalf("body = %v, want plan body", gotMetadata["body"])
	}
	if plan.AttachmentID != "attachment-2" {
		t.Fatalf("plan.AttachmentID = %q, want %q", plan.AttachmentID, "attachment-2")
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
	linearRequests, ok := limits["linear_requests"].(map[string]any)
	if !ok {
		t.Fatalf("linear_requests missing from rate limits: %#v", limits)
	}
	if got, ok := linearRequests["limit"].(int64); !ok || got != 100 {
		t.Fatalf("limit = %d, want 100", got)
	}
	if got, ok := linearRequests["remaining"].(int64); !ok || got != 25 {
		t.Fatalf("remaining = %d, want 25", got)
	}
	if got, ok := linearRequests["resetsAt"].(int64); !ok || got != resetAt.Unix() {
		t.Fatalf("resetsAt = %d, want %d", got, resetAt.Unix())
	}
	nextAllowedAt, ok := linearRequests["nextAllowedAt"].(int64)
	if !ok {
		t.Fatalf("nextAllowedAt missing from rate limits: %#v", linearRequests)
	}
	if nextAllowedAt <= time.Now().UTC().Unix() {
		t.Fatalf("nextAllowedAt = %d, want future timestamp", nextAllowedAt)
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
											"actual_branch_name":       "colin-94",
											"review_publish_directive": "skip",
											"last_run_type":            "coding",
											"last_outcome":             "needs_spec",
											"last_summary_comment_id":  "comment-2",
											"loop_failure_fingerprint": "review_publish\nReview\nno commits",
											"loop_failure_count":       3,
											"paused_at":                base.Add(3 * time.Minute).Format(time.RFC3339),
											"paused_run_type":          "review_publish",
											"paused_state":             "Review",
											"paused_reason":            "no commits between main and branch",
											"updated_at":               base.Add(2 * time.Minute).Format(time.RFC3339),
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
	if issues[0].ColinMetadata.ReviewPublishDirective != "skip" {
		t.Fatalf("ReviewPublishDirective = %q, want %q", issues[0].ColinMetadata.ReviewPublishDirective, "skip")
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
	if issues[0].ColinMetadata.LoopFailureCount != 3 {
		t.Fatalf("LoopFailureCount = %d, want 3", issues[0].ColinMetadata.LoopFailureCount)
	}
	if issues[0].ColinMetadata.PausedRunType != "review_publish" {
		t.Fatalf("PausedRunType = %q, want review_publish", issues[0].ColinMetadata.PausedRunType)
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
										"url":   "https://colin.invalid/linear/issues/issue-1/exec-plan",
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
	if issues[0].ExecPlan.Body != "# Plan\n\nDetails." {
		t.Fatalf("ExecPlan.Body = %q, want plan body", issues[0].ExecPlan.Body)
	}
}
