package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWorkerRunUsesConfigFileForAccess(t *testing.T) {
	t.Parallel()

	const (
		configToken = "config-token"
		configTeam  = "config-team"
	)

	var (
		mu             sync.Mutex
		authHeaders    []string
		listTeamKeys   []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()

		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch {
		case strings.Contains(req.Query, "query ListIssues"):
			teamKey, _ := req.Variables["teamKey"].(string)
			mu.Lock()
			listTeamKeys = append(listTeamKeys, teamKey)
			mu.Unlock()

			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issues": map[string]any{
						"nodes": []map[string]any{
							{
								"id":          "issue-1",
								"identifier":  "COL-1",
								"title":       "Config file e2e",
								"description": "spec is present",
								"updatedAt":   "2026-02-11T00:00:00Z",
								"state":       map[string]any{"name": "Todo"},
							},
						},
					},
				},
			})
			return

		case strings.Contains(req.Query, "query GetIssue"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id":          "issue-1",
						"identifier":  "COL-1",
						"title":       "Config file e2e",
						"description": "spec is present",
						"updatedAt":   "2026-02-11T00:00:00Z",
						"state":       map[string]any{"name": "Todo"},
					},
				},
			})
			return

		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "colin.toml")
	configContent := "linear_api_token = \"" + configToken + "\"\n" +
		"linear_team_id = \"" + configTeam + "\"\n" +
		"linear_base_url = \"" + srv.URL + "\"\n" +
		"worker_id = \"e2e-worker\"\n" +
		"dry_run = true\n"
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", configPath, err)
	}

	cmd := exec.Command("go", "run", ".", "--config", configPath, "worker", "run", "--once")
	cmd.Dir = filepath.Clean("..")
	cmd.Env = append(os.Environ(),
		"LINEAR_API_TOKEN=",
		"LINEAR_TEAM_ID=",
		"LINEAR_BASE_URL=",
		"COLIN_CONFIG=",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput:\n%s", err, string(output))
	}

	outputText := string(output)
	if !strings.Contains(outputText, `action=claim_and_transition to="In Progress"`) {
		t.Fatalf("expected transition log in output, got:\n%s", outputText)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(authHeaders) == 0 {
		t.Fatal("expected at least one request to mock Linear server")
	}
	for _, h := range authHeaders {
		if h != configToken {
			t.Fatalf("authorization header = %q, want %q", h, configToken)
		}
	}

	if len(listTeamKeys) == 0 {
		t.Fatal("expected ListIssues request with teamKey")
	}
	for _, v := range listTeamKeys {
		if v != configTeam {
			t.Fatalf("teamKey = %q, want %q", v, configTeam)
		}
	}
}
