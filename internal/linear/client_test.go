package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpsertMetadata(t *testing.T) {
	description := "Issue details"
	patched, meta, err := upsertMetadata(description, MetadataPatch{Set: map[string]string{"a": "1", "b": "2"}})
	if err != nil {
		t.Fatalf("upsertMetadata() error = %v", err)
	}
	if !strings.Contains(patched, "colin:metadata") {
		t.Fatalf("expected metadata block in %q", patched)
	}
	if meta["a"] != "1" || meta["b"] != "2" {
		t.Fatalf("unexpected metadata map: %#v", meta)
	}

	patched, meta, err = upsertMetadata(patched, MetadataPatch{Delete: []string{"a"}})
	if err != nil {
		t.Fatalf("upsertMetadata() delete error = %v", err)
	}
	if _, ok := meta["a"]; ok {
		t.Fatalf("expected key a to be deleted: %#v", meta)
	}
	if meta["b"] != "2" {
		t.Fatalf("expected key b to stay set: %#v", meta)
	}
}

func TestListCandidateIssuesFiltersStates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if strings.Contains(req.Query, "query ListIssues") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{"id": "1", "identifier": "COL-1", "title": "todo-unblocked", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Todo"}, "inverseRelations": map[string]any{"nodes": []map[string]any{}}},
							{"id": "2", "identifier": "COL-2", "title": "todo-blocked", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Todo"}, "inverseRelations": map[string]any{"nodes": []map[string]any{
								{"type": "blocks", "issue": map[string]any{"id": "dep-2", "state": map[string]any{"name": "In Progress"}}, "relatedIssue": map[string]any{"id": "2", "state": map[string]any{"name": "Todo"}}},
							}}},
							{"id": "3", "identifier": "COL-3", "title": "todo-unblocked", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Todo"}, "inverseRelations": map[string]any{"nodes": []map[string]any{
								{"type": "blocks", "issue": map[string]any{"id": "dep-3", "state": map[string]any{"name": "Done"}}, "relatedIssue": map[string]any{"id": "3", "state": map[string]any{"name": "Todo"}}},
							}}},
							{"id": "4", "identifier": "COL-4", "title": "inprogress-blocked", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "In Progress"}, "inverseRelations": map[string]any{"nodes": []map[string]any{
								{"type": "blocks", "issue": map[string]any{"id": "dep-4", "state": map[string]any{"name": "Todo"}}, "relatedIssue": map[string]any{"id": "4", "state": map[string]any{"name": "In Progress"}}},
							}}},
							{"id": "5", "identifier": "COL-5", "title": "done", "description": "x", "updatedAt": "2026-02-11T00:00:00Z", "state": map[string]any{"name": "Done"}, "inverseRelations": map[string]any{"nodes": []map[string]any{}}},
						},
					},
				},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	issues, err := client.ListCandidateIssues(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 candidate issues, got %d", len(issues))
	}
	if issues[0].Identifier != "COL-1" {
		t.Fatalf("unexpected first issue identifier: %q", issues[0].Identifier)
	}
	if issues[1].Identifier != "COL-3" {
		t.Fatalf("unexpected second issue identifier: %q", issues[1].Identifier)
	}
}

func TestUpdateIssueMetadataDetectsConflict(t *testing.T) {
	var mutationCalled bool
	getIssueCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query GetIssue"):
			getIssueCalls++
			desc := "A"
			if getIssueCalls == 2 {
				desc = "B"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":          "1",
						"identifier":  "COL-1",
						"title":       "x",
						"description": desc,
						"updatedAt":   "2026-02-11T00:00:00Z",
						"state":       map[string]any{"name": "Todo"},
					},
				},
			})
			return
		case strings.Contains(req.Query, "mutation UpdateIssueDescription"):
			mutationCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{"success": true},
				},
			})
			return
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	err := client.UpdateIssueMetadata(context.Background(), "1", MetadataPatch{
		Set: map[string]string{"k": "v"},
	})

	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if mutationCalled {
		t.Fatal("expected mutation not to be called after conflict detection")
	}
}

func TestCreateIssueComment(t *testing.T) {
	var mutationCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if strings.Contains(req.Query, "mutation CreateIssueComment") {
			mutationCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"commentCreate": map[string]any{"success": true},
				},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "team", srv.Client())
	if err := client.CreateIssueComment(context.Background(), "1", "hello"); err != nil {
		t.Fatalf("CreateIssueComment() error = %v", err)
	}
	if !mutationCalled {
		t.Fatal("expected commentCreate mutation")
	}
}

func TestResolveStateIDFromMapSupportsReviewAlias(t *testing.T) {
	stateID := resolveStateIDFromMap(map[string]string{
		"In Review": "state-in-review",
	}, "Review")
	if stateID != "state-in-review" {
		t.Fatalf("resolveStateIDFromMap() = %q, want %q", stateID, "state-in-review")
	}
}

func TestResolveStateIDFromMapSupportsHumanReviewAlias(t *testing.T) {
	stateID := resolveStateIDFromMap(map[string]string{
		"Human Review": "state-human-review",
	}, "Review")
	if stateID != "state-human-review" {
		t.Fatalf("resolveStateIDFromMap() = %q, want %q", stateID, "state-human-review")
	}
}

func TestUpdateIssueStateResolvesReviewAlias(t *testing.T) {
	var updateStateID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query GetIssue"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":          "1",
						"identifier":  "COL-1",
						"title":       "test",
						"description": "x",
						"updatedAt":   "2026-02-11T00:00:00Z",
						"state":       map[string]any{"name": "In Progress"},
					},
				},
			})
			return
		case strings.Contains(req.Query, "query WorkflowStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"workflowStates": map[string]any{
						"nodes": []map[string]any{
							{"id": "state-in-review", "name": "In Review"},
						},
					},
				},
			})
			return
		case strings.Contains(req.Query, "mutation UpdateIssueState"):
			updateStateID = req.Variables["stateId"]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issueUpdate": map[string]any{"success": true},
				},
			})
			return
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, "token", "COLIN", srv.Client())
	if err := client.UpdateIssueState(context.Background(), "1", "Review"); err != nil {
		t.Fatalf("UpdateIssueState() error = %v", err)
	}
	if updateStateID != "state-in-review" {
		t.Fatalf("UpdateIssueState() stateId = %q, want %q", updateStateID, "state-in-review")
	}
}
