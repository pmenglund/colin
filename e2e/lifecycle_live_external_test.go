//go:build livee2e

package e2e_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/workflow"
)

const (
	liveEnvEnabled        = "COLIN_LIVE_E2E"
	liveEnvLinearToken    = "COLIN_LIVE_LINEAR_API_TOKEN"
	liveEnvLinearTeamKey  = "COLIN_LIVE_LINEAR_TEAM_KEY"
	liveEnvLinearTeamID   = "COLIN_LIVE_LINEAR_TEAM_ID"
	liveEnvSandboxRepoURL = "COLIN_LIVE_GIT_SANDBOX_REPO_URL"
)

type liveHarnessEnv struct {
	LinearToken    string
	LinearTeamKey  string
	LinearTeamID   string
	SandboxRepoURL string
	CodexHome      string
}

func TestLifecycleLiveExternalSystems(t *testing.T) {
	env := loadLiveHarnessEnvOrSkip(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	suffix, err := liveRunSuffix()
	if err != nil {
		t.Fatalf("generate run suffix: %v", err)
	}

	admin := newLiveLinearAdmin(env.LinearToken, env.LinearTeamID, env.LinearTeamKey)
	stateIDs, err := admin.stateIDByName(ctx)
	if err != nil {
		t.Fatalf("load state ids: %v", err)
	}
	todoStateID, ok := stateIDs[workflow.StateTodo]
	if !ok {
		t.Fatalf("state id missing for %q", workflow.StateTodo)
	}
	mergeStateID, ok := stateIDs[workflow.StateMerge]
	if !ok {
		t.Fatalf("state id missing for %q", workflow.StateMerge)
	}

	projectName := "COLIN LIVE E2E " + strings.ToUpper(suffix)
	project, err := admin.createProject(ctx, projectName, "Temporary project created by TestLifecycleLiveExternalSystems")
	if err != nil {
		t.Fatalf("create live project: %v", err)
	}
	artifactIssueIDs := []string{}
	cleanupOnSuccess := false
	defer func() {
		if !cleanupOnSuccess {
			t.Logf("preserving live artifacts for investigation: project=%s (%s) issues=%v", project.ID, project.URL, artifactIssueIDs)
			return
		}

		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()

		for _, issueID := range artifactIssueIDs {
			if err := admin.archiveIssue(cleanupCtx, issueID); err != nil {
				t.Errorf("archive issue %s: %v", issueID, err)
			}
		}
		if err := admin.archiveProject(cleanupCtx, project.ID); err != nil {
			t.Errorf("archive project %s: %v", project.ID, err)
		}
	}()

	sandbox := prepareLiveGitSandbox(t, env.SandboxRepoURL)
	repoRoot := mustRepoRoot(t)
	promptPath := filepath.Join(repoRoot, "e2e", "testdata", "live_work_prompt.md")
	if _, err := os.Stat(promptPath); err != nil {
		t.Fatalf("stat prompt template %s: %v", promptPath, err)
	}

	colinHome := filepath.Join(t.TempDir(), "colin-home")
	if err := os.MkdirAll(colinHome, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", colinHome, err)
	}
	configPath := filepath.Join(t.TempDir(), "colin-live-e2e.toml")
	workerID := "live-e2e-" + suffix
	if err := writeLiveWorkerConfig(configPath, env, workerID, colinHome, promptPath); err != nil {
		t.Fatalf("write config: %v", err)
	}

	issueRefine, err := admin.createIssue(ctx, liveLinearIssueInput{
		Title:       fmt.Sprintf("LIVE E2E REFINE %s", strings.ToUpper(suffix)),
		Description: "[LIVE_E2E_FORCE_REFINE] intentional refine branch for live harness",
		ProjectID:   project.ID,
		StateID:     todoStateID,
	})
	if err != nil {
		t.Fatalf("create refine issue: %v", err)
	}
	issueReview, err := admin.createIssue(ctx, liveLinearIssueInput{
		Title:       fmt.Sprintf("LIVE E2E REVIEW %s", strings.ToUpper(suffix)),
		Description: "[LIVE_E2E_FORCE_REVIEW] intentional review branch for live harness",
		ProjectID:   project.ID,
		StateID:     todoStateID,
	})
	if err != nil {
		t.Fatalf("create review issue: %v", err)
	}
	issueMerge, err := admin.createIssue(ctx, liveLinearIssueInput{
		Title:       fmt.Sprintf("LIVE E2E MERGE %s", strings.ToUpper(suffix)),
		Description: "intentional merge branch for live harness",
		ProjectID:   project.ID,
		StateID:     mergeStateID,
	})
	if err != nil {
		t.Fatalf("create merge issue: %v", err)
	}
	artifactIssueIDs = append(artifactIssueIDs, issueRefine.ID, issueReview.ID, issueMerge.ID)
	t.Logf("live artifacts created: project=%s issues=%v", project.URL, []string{issueRefine.URL, issueReview.URL, issueMerge.URL})

	mergeFixture := sandbox.createMergeFixture(t, issueMerge.Identifier, colinHome)
	mergeDescription, err := upsertLiveMetadata(issueMerge.Description, map[string]string{
		workflow.MetaMergeReady:   "true",
		workflow.MetaBranchName:   mergeFixture.BranchName,
		workflow.MetaWorktreePath: mergeFixture.WorktreePath,
	})
	if err != nil {
		t.Fatalf("build merge issue metadata: %v", err)
	}
	if err := admin.updateIssueDescription(ctx, issueMerge.ID, mergeDescription); err != nil {
		t.Fatalf("update merge issue metadata: %v", err)
	}

	binaryPath := buildColinBinary(t, repoRoot)

	trackedIssueIDs := []string{issueRefine.ID, issueReview.ID, issueMerge.ID}
	terminalStates := map[string]struct{}{
		workflow.StateReview: {},
		workflow.StateRefine: {},
		workflow.StateDone:   {},
	}

	issuesByID := map[string]liveLinearIssue{}
	maxCycles := 14
	converged := false
	for cycle := 1; cycle <= maxCycles; cycle++ {
		output, err := runLiveWorkerOnce(binaryPath, sandbox.RepoRoot, configPath, env.CodexHome)
		t.Logf("worker cycle %d output:\n%s", cycle, strings.TrimSpace(output))
		if err != nil {
			t.Fatalf("run worker cycle %d: %v", cycle, err)
		}

		issuesByID = map[string]liveLinearIssue{}
		stateSummary := make([]string, 0, len(trackedIssueIDs))
		for _, issueID := range trackedIssueIDs {
			issue, getErr := admin.getIssue(ctx, issueID)
			if getErr != nil {
				t.Fatalf("get issue %s after cycle %d: %v", issueID, cycle, getErr)
			}
			issuesByID[issueID] = issue
			stateSummary = append(stateSummary, fmt.Sprintf("%s=%s", issue.Identifier, issue.StateName))
		}
		sort.Strings(stateSummary)
		t.Logf("worker cycle %d states: %s", cycle, strings.Join(stateSummary, ", "))

		if allIssuesInTerminalStates(issuesByID, trackedIssueIDs, terminalStates) {
			converged = true
			break
		}
	}

	if !converged {
		t.Fatalf("issues did not converge to terminal states within %d cycles; project=%s issues=%v", maxCycles, project.URL, []string{issueRefine.URL, issueReview.URL, issueMerge.URL})
	}

	finalRefine := issuesByID[issueRefine.ID]
	finalReview := issuesByID[issueReview.ID]
	finalMerge := issuesByID[issueMerge.ID]

	if finalRefine.StateName != workflow.StateRefine {
		t.Fatalf("refine issue final state = %q, want %q", finalRefine.StateName, workflow.StateRefine)
	}
	if finalReview.StateName != workflow.StateReview {
		t.Fatalf("review issue final state = %q, want %q", finalReview.StateName, workflow.StateReview)
	}
	if finalMerge.StateName != workflow.StateDone {
		t.Fatalf("merge issue final state = %q, want %q", finalMerge.StateName, workflow.StateDone)
	}

	assertLiveIssueContainsComment(t, finalRefine, "Moved to **Refine**")
	assertLiveIssueContainsComment(t, finalReview, "Moved to **Review**")

	assertLiveMetadataKeyPresent(t, finalReview, workflow.MetaWorktreePath)
	assertLiveMetadataKeyPresent(t, finalReview, workflow.MetaBranchName)
	assertLiveMetadataKeyPresent(t, finalReview, workflow.MetaThreadID)

	if got := strings.TrimSpace(finalMerge.Metadata[workflow.MetaMergeReady]); got != "false" {
		t.Fatalf("merge issue metadata %s = %q, want %q", workflow.MetaMergeReady, got, "false")
	}

	if _, err := os.Stat(mergeFixture.FilePath); err != nil {
		t.Fatalf("expected merged file %s in sandbox repo: %v", mergeFixture.FilePath, err)
	}
	if sandbox.branchExists(t, mergeFixture.BranchName) {
		t.Fatalf("expected merged branch %q to be deleted", mergeFixture.BranchName)
	}
	if _, err := os.Stat(mergeFixture.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected merge worktree %s to be deleted, stat error: %v", mergeFixture.WorktreePath, err)
	}
	if localMain, remoteMain := sandbox.localMainRevision(t), sandbox.remoteMainRevision(t); localMain != remoteMain {
		t.Fatalf("sandbox repo main was not pushed: local=%s remote=%s", localMain, remoteMain)
	}

	cleanupOnSuccess = true
}

func loadLiveHarnessEnvOrSkip(t *testing.T) liveHarnessEnv {
	t.Helper()

	if strings.TrimSpace(os.Getenv(liveEnvEnabled)) != "1" {
		t.Skipf("set %s=1 to run live external lifecycle harness", liveEnvEnabled)
	}

	required := map[string]string{
		liveEnvLinearToken:    strings.TrimSpace(os.Getenv(liveEnvLinearToken)),
		liveEnvLinearTeamKey:  strings.TrimSpace(os.Getenv(liveEnvLinearTeamKey)),
		liveEnvLinearTeamID:   strings.TrimSpace(os.Getenv(liveEnvLinearTeamID)),
		liveEnvSandboxRepoURL: strings.TrimSpace(os.Getenv(liveEnvSandboxRepoURL)),
		"CODEX_HOME":          strings.TrimSpace(os.Getenv("CODEX_HOME")),
	}
	missing := make([]string, 0, len(required))
	for key, value := range required {
		if value == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Skipf("live harness requires env vars: %s", strings.Join(missing, ", "))
	}

	codexHome := required["CODEX_HOME"]
	if err := ensureWritableDir(codexHome); err != nil {
		t.Skipf("CODEX_HOME is not writable (%s): %v", codexHome, err)
	}

	return liveHarnessEnv{
		LinearToken:    required[liveEnvLinearToken],
		LinearTeamKey:  required[liveEnvLinearTeamKey],
		LinearTeamID:   required[liveEnvLinearTeamID],
		SandboxRepoURL: required[liveEnvSandboxRepoURL],
		CodexHome:      codexHome,
	}
}

func ensureWritableDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is empty")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(path, fmt.Sprintf(".colin-live-e2e-probe-%d", time.Now().UnixNano()))
	if err := os.WriteFile(probe, []byte("ok\n"), 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

func liveRunSuffix() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + strings.ToLower(hex.EncodeToString(random)), nil
}

func writeLiveWorkerConfig(path string, env liveHarnessEnv, workerID string, colinHome string, promptPath string) error {
	content := fmt.Sprintf(`linear_api_token = %q
linear_team_id = %q
linear_base_url = %q
linear_backend = "http"
work_prompt_path = %q
colin_home = %q
worker_id = %q
poll_every = "1s"
lease_ttl = "5m"
max_concurrency = 4
dry_run = false
`, env.LinearToken, env.LinearTeamKey, liveLinearEndpoint, promptPath, colinHome, workerID)
	return os.WriteFile(path, []byte(content), 0o644)
}

func buildColinBinary(t *testing.T, repoRoot string) string {
	t.Helper()

	binaryPath := filepath.Join(t.TempDir(), "colin-live-e2e")
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build colin binary failed: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	return binaryPath
}

func runLiveWorkerOnce(binaryPath string, sandboxRepoRoot string, configPath string, codexHome string) (string, error) {
	cmd := exec.Command(binaryPath, "--config", configPath, "worker", "run", "--once")
	cmd.Dir = sandboxRepoRoot
	cmd.Env = append(os.Environ(),
		"CODEX_HOME="+codexHome,
		"COLIN_CONFIG=",
		"COLIN_LINEAR_BACKEND=",
		"COLIN_WORK_PROMPT_PATH=",
		"COLIN_HOME=",
		"COLIN_WORKER_ID=",
		"COLIN_POLL_EVERY=",
		"COLIN_LEASE_TTL=",
		"COLIN_DRY_RUN=",
		"COLIN_MAX_CONCURRENCY=",
		"LINEAR_API_TOKEN=",
		"LINEAR_TEAM_ID=",
		"LINEAR_BASE_URL=",
	)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func allIssuesInTerminalStates(issuesByID map[string]liveLinearIssue, issueIDs []string, terminalStates map[string]struct{}) bool {
	for _, issueID := range issueIDs {
		issue, ok := issuesByID[issueID]
		if !ok {
			return false
		}
		if _, ok := terminalStates[issue.StateName]; !ok {
			return false
		}
	}
	return true
}

func assertLiveIssueContainsComment(t *testing.T, issue liveLinearIssue, needle string) {
	t.Helper()
	for _, comment := range issue.Comments {
		if strings.Contains(comment, needle) {
			return
		}
	}
	t.Fatalf("issue %s (%s) did not include expected comment text %q", issue.Identifier, issue.URL, needle)
}

func assertLiveMetadataKeyPresent(t *testing.T, issue liveLinearIssue, key string) {
	t.Helper()
	if strings.TrimSpace(issue.Metadata[key]) == "" {
		t.Fatalf("issue %s missing metadata key %s in description", issue.Identifier, key)
	}
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	e2eDir := filepath.Dir(file)
	return filepath.Clean(filepath.Join(e2eDir, ".."))
}
