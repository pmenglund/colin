//go:build livee2e

package e2e_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
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
	liveEnvLinearToken = "COLIN_LIVE_LINEAR_API_TOKEN"
	liveEnvLinearTeam  = "COLIN_LIVE_LINEAR_TEAM"
)

type liveHarnessEnv struct {
	LinearToken string
	LinearTeam  string
}

func TestLifecycleLiveExternalSystems(t *testing.T) {
	env := loadLiveHarnessEnvOrFail(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	suffix, err := liveRunSuffix()
	if err != nil {
		t.Fatalf("generate run suffix: %v", err)
	}

	admin := newLiveLinearAdmin(env.LinearToken, env.LinearTeam)
	teamID, err := admin.resolveTeamIDByKey(ctx)
	if err != nil {
		t.Fatalf("resolve team id for %q: %v", env.LinearTeam, err)
	}
	admin.teamID = teamID

	stateIDs, err := admin.stateIDByName(ctx)
	if err != nil {
		t.Fatalf("load state ids: %v", err)
	}
	todoStateName, todoStateID := mustResolveLiveState(t, stateIDs, workflow.StateTodo)
	inProgressStateName, _ := mustResolveLiveState(t, stateIDs, workflow.StateInProgress)
	mergeStateName, mergeStateID := mustResolveLiveState(t, stateIDs, workflow.StateMerge)
	refineStateName, _ := mustResolveLiveState(t, stateIDs, workflow.StateRefine)
	reviewStateName, _ := mustResolveLiveState(t, stateIDs, workflow.StateReview)
	doneStateName, _ := mustResolveLiveState(t, stateIDs, workflow.StateDone)

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
				if isLiveNotFoundError(err) {
					t.Logf("archive issue %s skipped: %v", issueID, err)
					continue
				}
				t.Errorf("archive issue %s: %v", issueID, err)
			}
		}
		if err := admin.deleteProject(cleanupCtx, project.ID); err != nil {
			if isLiveNotFoundError(err) {
				t.Logf("delete project %s skipped: %v", project.ID, err)
				return
			}
			t.Errorf("delete project %s: %v", project.ID, err)
		}
	}()

	sandbox := prepareLiveGitSandbox(t)
	repoRoot := mustRepoRoot(t)
	promptPath := filepath.Join(repoRoot, "e2e", "testdata", "live_work_prompt.md")
	if _, err := os.Stat(promptPath); err != nil {
		t.Fatalf("stat prompt template %s: %v", promptPath, err)
	}

	colinHome := filepath.Join(t.TempDir(), "colin-home")
	if err := os.MkdirAll(colinHome, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", colinHome, err)
	}
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	if err := prepareTempCodexHome(codexHome); err != nil {
		t.Fatalf("prepare CODEX_HOME (%s): %v", codexHome, err)
	}
	configPath := filepath.Join(t.TempDir(), "colin-live-e2e.toml")
	workerID := "live-e2e-" + suffix
	if err := writeLiveWorkerConfig(configPath, env, workerID, colinHome, promptPath, workflow.States{
		Todo:       todoStateName,
		InProgress: inProgressStateName,
		Refine:     refineStateName,
		Review:     reviewStateName,
		Merge:      mergeStateName,
		Done:       doneStateName,
	}); err != nil {
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
	if err := admin.upsertIssueMetadata(ctx, issueMerge.ID, map[string]string{
		workflow.MetaMergeReady:   "true",
		workflow.MetaBranchName:   mergeFixture.BranchName,
		workflow.MetaWorktreePath: mergeFixture.WorktreePath,
	}); err != nil {
		t.Fatalf("upsert merge issue metadata attachment: %v", err)
	}

	binaryPath := buildColinBinary(t, repoRoot)

	trackedIssueIDs := []string{issueRefine.ID, issueReview.ID, issueMerge.ID}
	terminalStates := map[string]struct{}{
		reviewStateName: {},
		refineStateName: {},
		doneStateName:   {},
	}

	issuesByID := map[string]liveLinearIssue{}
	maxCycles := 14
	converged := false
	for cycle := 1; cycle <= maxCycles; cycle++ {
		output, err := runLiveWorkerOnce(binaryPath, sandbox.RepoRoot, configPath, codexHome)
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

	if finalRefine.StateName != refineStateName {
		t.Fatalf("refine issue final state = %q, want %q", finalRefine.StateName, refineStateName)
	}
	if finalReview.StateName != reviewStateName {
		t.Fatalf("review issue final state = %q, want %q", finalReview.StateName, reviewStateName)
	}
	if finalMerge.StateName != doneStateName {
		t.Fatalf("merge issue final state = %q, want %q", finalMerge.StateName, doneStateName)
	}

	assertLiveIssueContainsComment(t, finalRefine, fmt.Sprintf("Moved to **%s**", refineStateName))
	assertLiveIssueContainsComment(t, finalReview, fmt.Sprintf("Moved to **%s**", reviewStateName))

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

func loadLiveHarnessEnvOrFail(t *testing.T) liveHarnessEnv {
	t.Helper()

	required := map[string]string{
		liveEnvLinearToken: strings.TrimSpace(os.Getenv(liveEnvLinearToken)),
		liveEnvLinearTeam:  strings.TrimSpace(os.Getenv(liveEnvLinearTeam)),
	}
	missing := make([]string, 0, len(required))
	for key, value := range required {
		if value == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("live harness requires env vars: %s", strings.Join(missing, ", "))
	}

	return liveHarnessEnv{
		LinearToken: required[liveEnvLinearToken],
		LinearTeam:  required[liveEnvLinearTeam],
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

func mustResolveLiveState(t *testing.T, stateIDs map[string]string, canonical string) (string, string) {
	t.Helper()

	if id, ok := stateIDs[canonical]; ok && strings.TrimSpace(id) != "" {
		return canonical, strings.TrimSpace(id)
	}

	normalizedCanonical := normalizeLiveStateName(canonical)
	for name, id := range stateIDs {
		if normalizeLiveStateName(name) == normalizedCanonical && strings.TrimSpace(id) != "" {
			return name, strings.TrimSpace(id)
		}
	}

	for _, alias := range liveStateAliases(canonical) {
		if id, ok := stateIDs[alias]; ok && strings.TrimSpace(id) != "" {
			return alias, strings.TrimSpace(id)
		}
		normalizedAlias := normalizeLiveStateName(alias)
		for name, id := range stateIDs {
			if normalizeLiveStateName(name) == normalizedAlias && strings.TrimSpace(id) != "" {
				return name, strings.TrimSpace(id)
			}
		}
	}

	available := make([]string, 0, len(stateIDs))
	for name := range stateIDs {
		available = append(available, name)
	}
	sort.Strings(available)
	t.Fatalf("state id missing for %q (available states: %s)", canonical, strings.Join(available, ", "))
	return "", ""
}

func normalizeLiveStateName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

func liveStateAliases(canonical string) []string {
	switch normalizeLiveStateName(canonical) {
	case normalizeLiveStateName(workflow.StateReview):
		return []string{"In Review", "Human Review"}
	default:
		return nil
	}
}

func isLiveNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "entity not found") || strings.Contains(message, "not found")
}

func prepareTempCodexHome(target string) error {
	if err := ensureWritableDir(target); err != nil {
		return err
	}

	source, err := codexHomeSeedSource()
	if err != nil {
		return err
	}
	return copyDirContents(source, target)
}

func codexHomeSeedSource() (string, error) {
	source := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if source == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		source = filepath.Join(home, ".codex")
	}
	source = filepath.Clean(source)

	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("resolve codex home seed source %q: %w", source, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("codex home seed source %q is not a directory", source)
	}
	return source, nil
}

func copyDirContents(source string, target string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read seed source %q: %w", source, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(source, entry.Name())
		dstPath := filepath.Join(target, entry.Name())
		if err := copyEntry(srcPath, dstPath, entry); err != nil {
			return err
		}
	}
	return nil
}

func copyEntry(srcPath string, dstPath string, entry os.DirEntry) error {
	if entry.IsDir() {
		if err := os.MkdirAll(dstPath, 0o755); err != nil {
			return fmt.Errorf("mkdir %q: %w", dstPath, err)
		}
		return copyDirContents(srcPath, dstPath)
	}

	if entry.Type()&os.ModeSymlink != 0 {
		linkTarget, err := os.Readlink(srcPath)
		if err != nil {
			return fmt.Errorf("read symlink %q: %w", srcPath, err)
		}
		if err := os.Symlink(linkTarget, dstPath); err != nil {
			return fmt.Errorf("create symlink %q -> %q: %w", dstPath, linkTarget, err)
		}
		return nil
	}

	if !entry.Type().IsRegular() {
		return nil
	}

	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("stat %q: %w", srcPath, err)
	}
	return copyRegularFile(srcPath, dstPath, info.Mode().Perm())
}

func copyRegularFile(srcPath string, dstPath string, perm os.FileMode) error {
	sourceFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %q: %w", srcPath, err)
	}
	defer sourceFile.Close()

	targetFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create %q: %w", dstPath, err)
	}
	defer targetFile.Close()

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return fmt.Errorf("copy %q to %q: %w", srcPath, dstPath, err)
	}
	return nil
}

func liveRunSuffix() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + strings.ToLower(hex.EncodeToString(random)), nil
}

func writeLiveWorkerConfig(path string, env liveHarnessEnv, workerID string, colinHome string, promptPath string, states workflow.States) error {
	states = states.WithDefaults()
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

[workflow_states]
todo = %q
in_progress = %q
refine = %q
review = %q
merge = %q
done = %q
`, env.LinearToken, env.LinearTeam, liveLinearEndpoint, promptPath, colinHome, workerID, states.Todo, states.InProgress, states.Refine, states.Review, states.Merge, states.Done)
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
		t.Fatalf("issue %s missing metadata key %s in metadata attachment", issue.Identifier, key)
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
