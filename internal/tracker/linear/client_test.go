package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
