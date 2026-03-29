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
