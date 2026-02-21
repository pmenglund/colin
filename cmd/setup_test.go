package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/config"
)

func TestSetupCommandHelp(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"setup", "--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Ensure required Linear workflow states") {
		t.Fatalf("setup help output missing description: %q", out)
	}
}

func TestRunSetupRejectsFakeBackend(t *testing.T) {
	err := runSetup(context.Background(), &bytes.Buffer{}, config.Config{
		LinearBackend: config.LinearBackendFake,
	})
	if err == nil {
		t.Fatal("runSetup() error = nil, want backend error")
	}
	if !strings.Contains(err.Error(), `linear_backend="http"`) {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestRunSetupCreatesMissingStatesAndPrintsSummary(t *testing.T) {
	createCalls := 0
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
				"data": map[string]any{"teams": map[string]any{"nodes": []map[string]any{{"id": "team-123", "key": "COLIN"}}}},
			})
		case strings.Contains(req.Query, "query WorkflowStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"workflowStates": map[string]any{"nodes": []map[string]any{
					{"id": "s-1", "name": "Todo", "type": "unstarted"},
					{"id": "s-2", "name": "In Progress", "type": "started"},
					{"id": "s-3", "name": "Refine", "type": "started"},
					{"id": "s-4", "name": "Review", "type": "started"},
					{"id": "s-6", "name": "Done", "type": "completed"},
				}}},
			})
		case strings.Contains(req.Query, "mutation WorkflowStateCreate"):
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"workflowStateCreate": map[string]any{
					"success":       true,
					"workflowState": map[string]any{"id": "s-5", "name": "Merge", "type": "started"},
				}},
			})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	defer srv.Close()

	cfg := config.Config{
		LinearBackend:  config.LinearBackendHTTP,
		LinearBaseURL:  srv.URL,
		LinearAPIToken: "token",
		LinearTeamID:   "COLIN",
		WorkflowStates: config.DefaultWorkflowStates(),
	}

	var out bytes.Buffer
	if err := runSetup(context.Background(), &out, cfg); err != nil {
		t.Fatalf("runSetup() error = %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", createCalls)
	}
	result := out.String()
	if !strings.Contains(result, "Linear team: COLIN (team-123)") {
		t.Fatalf("output missing team summary: %q", result)
	}
	if !strings.Contains(result, `- merge: "Merge" -> "Merge" [created, type=started]`) {
		t.Fatalf("output missing merge created line: %q", result)
	}
	if !strings.Contains(result, `- review => "Review"`) {
		t.Fatalf("output missing resolved review mapping: %q", result)
	}
}
