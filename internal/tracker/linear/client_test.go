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
)

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
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
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
		endpoint: server.URL,
		apiKey:   "token",
		client:   &http.Client{Timeout: 5 * time.Second},
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

func TestFetchCandidateIssuesExtractsLatestReviewPublishDirective(t *testing.T) {
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
							"comments": map[string]any{
								"nodes": []map[string]any{
									{
										"id":        "comment-1",
										"body":      "[colin] Ready for review.\n\n<!-- colin:review_publish=publish -->",
										"createdAt": base.Add(1 * time.Minute).Format(time.RFC3339),
										"children":  map[string]any{"nodes": []map[string]any{}},
									},
									{
										"id":        "comment-2",
										"body":      "[colin] The spec should be improved before implementation.\n\n<!-- colin:review_publish=skip -->",
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
	if issues[0].ReviewPublishDirective != "skip" {
		t.Fatalf("ReviewPublishDirective = %q, want %q", issues[0].ReviewPublishDirective, "skip")
	}
}
