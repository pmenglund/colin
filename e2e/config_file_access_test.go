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
	configPath := filepath.Join(tmpDir, "colin.toml")
	configContent := `linear_backend = "fake"
worker_id = "e2e-worker"
dry_run = true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", configPath, err)
	}

	cmd := exec.Command("go", "run", ".", "--config", configPath, "--once")
	cmd.Dir = filepath.Clean("..")
	cmd.Env = append(os.Environ(),
		"LINEAR_API_TOKEN=",
		"LINEAR_TEAM_ID=",
		"LINEAR_BASE_URL=http://127.0.0.1:1/graphql",
		"COLIN_CONFIG=",
		"COLIN_LINEAR_BACKEND=",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput:\n%s", err, string(output))
	}

	outputText := stripANSIEscapeCodes(string(output))
	if !strings.Contains(outputText, `action=claim_and_transition to="In Progress"`) {
		t.Fatalf("expected transition log in output, got:\n%s", outputText)
	}
}

var ansiEscapeCodePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSIEscapeCodes(text string) string {
	return ansiEscapeCodePattern.ReplaceAllString(text, "")
}
