package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/workflow"
)

func TestWorkflowStateAdminResolveWorkflowStates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query ResolveTeamByKey"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"teams": map[string]any{
						"nodes": []map[string]any{{"id": "team-1", "key": "COLIN"}},
					},
				},
			})
		case strings.Contains(req.Query, "query WorkflowStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"workflowStates": map[string]any{
						"nodes": []map[string]any{
							{"id": "s-1", "name": "Todo Queue", "type": "unstarted"},
							{"id": "s-2", "name": "Building", "type": "started"},
							{"id": "s-3", "name": "Needs Spec", "type": "started"},
							{"id": "s-4", "name": "Human Review", "type": "started"},
							{"id": "s-5", "name": "Merge Queue", "type": "started"},
							{"id": "s-6", "name": "Merged Queue", "type": "started"},
							{"id": "s-7", "name": "Shipped", "type": "completed"},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	defer srv.Close()

	admin := NewWorkflowStateAdmin(srv.URL, "token", "COLIN", srv.Client())
	resolved, err := admin.ResolveWorkflowStates(context.Background(), workflow.States{
		Todo:       "Todo Queue",
		InProgress: "Building",
		Refine:     "Needs Spec",
		Review:     "Human Review",
		Merge:      "Merge Queue",
		Merged:     "Merged Queue",
		Done:       "Shipped",
	})
	if err != nil {
		t.Fatalf("ResolveWorkflowStates() error = %v", err)
	}

	if resolved.TeamID != "team-1" {
		t.Fatalf("TeamID = %q, want %q", resolved.TeamID, "team-1")
	}
	states := resolved.RuntimeStates()
	if states.Review != "Human Review" {
		t.Fatalf("review state = %q, want %q", states.Review, "Human Review")
	}
	if states.Done != "Shipped" {
		t.Fatalf("done state = %q, want %q", states.Done, "Shipped")
	}
	ids := resolved.StateIDByName()
	if ids["Merge Queue"] != "s-5" {
		t.Fatalf("StateIDByName()[Merge Queue] = %q, want %q", ids["Merge Queue"], "s-5")
	}
	if ids["Merged Queue"] != "s-6" {
		t.Fatalf("StateIDByName()[Merged Queue] = %q, want %q", ids["Merged Queue"], "s-6")
	}
	if resolved.Mappings["review"].Created {
		t.Fatal("review mapping Created = true, want false")
	}
}

func TestWorkflowStateAdminResolveWorkflowStatesMissingState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query ResolveTeamByKey"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"teams": map[string]any{"nodes": []map[string]any{{"id": "team-1", "key": "COLIN"}}}},
			})
		case strings.Contains(req.Query, "query WorkflowStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"workflowStates": map[string]any{"nodes": []map[string]any{
					{"id": "s-1", "name": "Todo", "type": "unstarted"},
					{"id": "s-2", "name": "In Progress", "type": "started"},
					{"id": "s-3", "name": "Refine", "type": "started"},
					{"id": "s-5", "name": "Merge", "type": "started"},
					{"id": "s-6", "name": "Merged", "type": "started"},
					{"id": "s-7", "name": "Done", "type": "completed"},
				}}},
			})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	defer srv.Close()

	admin := NewWorkflowStateAdmin(srv.URL, "token", "COLIN", srv.Client())
	_, err := admin.ResolveWorkflowStates(context.Background(), workflow.DefaultStates())
	if err == nil {
		t.Fatal("ResolveWorkflowStates() error = nil, want missing state error")
	}
	if !strings.Contains(err.Error(), `review="Review"`) {
		t.Fatalf("error = %q, want missing review mapping", err.Error())
	}
}

func TestWorkflowStateAdminEnsureWorkflowStatesCreatesMissing(t *testing.T) {
	createCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query ResolveTeamByKey"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"teams": map[string]any{"nodes": []map[string]any{{"id": "team-1", "key": "COLIN"}}}},
			})
		case strings.Contains(req.Query, "query WorkflowStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"workflowStates": map[string]any{"nodes": []map[string]any{
					{"id": "s-1", "name": "Todo", "type": "unstarted"},
					{"id": "s-2", "name": "In Progress", "type": "started"},
					{"id": "s-3", "name": "Refine", "type": "started"},
					{"id": "s-4", "name": "Review", "type": "started"},
					{"id": "s-5", "name": "Merge", "type": "started"},
					{"id": "s-6", "name": "Done", "type": "completed"},
				}}},
			})
		case strings.Contains(req.Query, "mutation WorkflowStateCreate"):
			createCalls++
			input := req.Variables["input"].(map[string]any)
			if input["name"] != "Merged" {
				t.Fatalf("created state name = %v, want Merged", input["name"])
			}
			if input["type"] != "started" {
				t.Fatalf("created state type = %v, want started", input["type"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"workflowStateCreate": map[string]any{
					"success":       true,
					"workflowState": map[string]any{"id": "s-7", "name": "Merged", "type": "started"},
				}},
			})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	defer srv.Close()

	admin := NewWorkflowStateAdmin(srv.URL, "token", "COLIN", srv.Client())
	resolved, err := admin.EnsureWorkflowStates(context.Background(), workflow.DefaultStates())
	if err != nil {
		t.Fatalf("EnsureWorkflowStates() error = %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	if !resolved.Mappings["merged"].Created {
		t.Fatal("merged mapping Created = false, want true")
	}
	if resolved.Mappings["todo"].Created {
		t.Fatal("todo mapping Created = true, want false")
	}
}

func TestWorkflowStateAdminEnsureWorkflowStatesValidatesType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "query ResolveTeamByKey"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"teams": map[string]any{"nodes": []map[string]any{{"id": "team-1", "key": "COLIN"}}}},
			})
		case strings.Contains(req.Query, "query WorkflowStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"workflowStates": map[string]any{"nodes": []map[string]any{
					{"id": "s-1", "name": "Todo", "type": "started"},
					{"id": "s-2", "name": "In Progress", "type": "started"},
					{"id": "s-3", "name": "Refine", "type": "started"},
					{"id": "s-4", "name": "Review", "type": "started"},
					{"id": "s-5", "name": "Merge", "type": "started"},
					{"id": "s-6", "name": "Merged", "type": "started"},
					{"id": "s-7", "name": "Done", "type": "completed"},
				}}},
			})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	defer srv.Close()

	admin := NewWorkflowStateAdmin(srv.URL, "token", "COLIN", srv.Client())
	_, err := admin.EnsureWorkflowStates(context.Background(), workflow.DefaultStates())
	if err == nil {
		t.Fatal("EnsureWorkflowStates() error = nil, want type mismatch")
	}
	if !strings.Contains(err.Error(), "expected \"unstarted\"") {
		t.Fatalf("error = %q, want expected unstarted", err.Error())
	}
}

func TestWorkflowStateAdminResolveWorkflowStatesReturnsRateLimitErrorWithRetryIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{
			"errors": [{
				"message": "Rate limit exceeded",
				"extensions": {
					"code": "RATELIMITED",
					"statusCode": 429,
					"retry_in": 6
				}
			}]
		}`))
	}))
	defer srv.Close()

	admin := NewWorkflowStateAdmin(srv.URL, "token", "COLIN", srv.Client())
	_, err := admin.ResolveWorkflowStates(context.Background(), workflow.DefaultStates())
	if err == nil {
		t.Fatal("ResolveWorkflowStates() error = nil, want rate limit error")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("errors.Is(err, ErrRateLimited) = false, err=%v", err)
	}
	retryIn, ok := RetryIn(err)
	if !ok {
		t.Fatal("RetryIn(err) ok = false, want true")
	}
	if retryIn != 6*time.Second {
		t.Fatalf("RetryIn(err) = %s, want %s", retryIn, 6*time.Second)
	}
}
