package e2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestWorkerRunUsesFakeLinearBackendWithoutNetwork(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "WORKFLOW.md")
	workflowContent := `---
tracker:
  kind: fake
polling:
  interval_ms: 1000
workspace:
  root: ` + filepath.Join(tmpDir, "workspaces") + `
colin:
  linear_backend: fake
  worker_id: e2e-worker
  dry_run: true
---
Issue {{ LINEAR_ID }}
`
	if err := os.WriteFile(workflowPath, []byte(workflowContent), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", workflowPath, err)
	}

	cmd := exec.Command("go", "run", ".", "--workflow", workflowPath, "--once")
	cmd.Dir = filepath.Clean("..")
	cmd.Env = append(os.Environ(),
		"LINEAR_API_TOKEN=",
		"LINEAR_TEAM_ID=",
		"LINEAR_BASE_URL=http://127.0.0.1:1/graphql",
		"COLIN_WORKFLOW_PATH=",
		"COLIN_LINEAR_BACKEND=",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput:\n%s", err, string(output))
	}

	outputText := stripANSIEscapeCodes(string(output))
	if strings.Contains(outputText, "LINEAR_API_TOKEN is required") {
		t.Fatalf("expected fake backend workflow to avoid Linear credential validation, got:\n%s", outputText)
	}
}

var ansiEscapeCodePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSIEscapeCodes(text string) string {
	return ansiEscapeCodePattern.ReplaceAllString(text, "")
}
