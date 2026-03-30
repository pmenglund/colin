package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
