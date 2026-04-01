package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/pmenglund/colin/internal/tracker/linear"
)

func TestValidateRepoURL(t *testing.T) {
	t.Parallel()

	valid := []string{
		"git@github.com:acme/repo.git",
		"https://github.com/acme/repo.git",
		"ssh://git@github.com/acme/repo.git",
	}
	for _, value := range valid {
		if got := validateRepoURL(value); got != "" {
			t.Fatalf("validateRepoURL(%q) = %q, want empty", value, got)
		}
	}
	if got := validateRepoURL("not a repo"); got == "" {
		t.Fatal("validateRepoURL() = empty, want error")
	}
}

func TestTUIReviewBlocksOverwriteUntilConfirmed(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:              tuiStepReview,
		workflowPath:      "WORKFLOW.md",
		projectSlug:       "project-1",
		repoURL:           "git@github.com:acme/repo.git",
		baseRef:           "main",
		workspaceRoot:     "./.colin/workspaces",
		serverPort:        "8888",
		overwriteRequired: true,
		prereqs:           Prerequisites{},
	}

	next, cmd := model.updateReviewKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd != nil {
		t.Fatal("updateReviewKey() returned save command before overwrite confirmation")
	}
	if !strings.Contains(updated.inlineError, "Confirm overwrite") {
		t.Fatalf("inlineError = %q, want overwrite confirmation message", updated.inlineError)
	}
}

func TestTUIReviewWritesWorkflowWhenChecksAreSkipped(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	workflowPath := filepath.Join(tempDir, "WORKFLOW.md")
	model := tuiModel{
		opts:          Options{},
		step:          tuiStepReview,
		workflowPath:  workflowPath,
		prereqs:       Prerequisites{},
		projectSlug:   "project-1",
		repoURL:       "git@github.com:acme/repo.git",
		baseRef:       "main",
		workspaceRoot: "./.colin/workspaces",
		serverPort:    "8888",
	}

	next, cmd := model.updateReviewKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd == nil {
		t.Fatal("updateReviewKey() command = nil, want save command")
	}
	if !updated.saving {
		t.Fatal("model.saving = false, want true")
	}

	msg := cmd()
	finalModel, _ := updated.Update(msg)
	saved := finalModel.(tuiModel)
	if saved.step != tuiStepSuccess {
		t.Fatalf("step = %d, want success", saved.step)
	}
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `project_slug: "project-1"`) {
		t.Fatalf("workflow file = %q, want project slug", string(data))
	}
}

func TestTUIProjectStepFetchesProjectsAndSelectsFilteredResult(t *testing.T) {
	oldListLinearProjects := listLinearProjects
	listLinearProjects = func(_ context.Context, endpoint string, apiKey string) ([]linear.ProjectSummary, error) {
		if endpoint != "" {
			t.Fatalf("endpoint = %q, want empty", endpoint)
		}
		if apiKey != "lin_api_token" {
			t.Fatalf("apiKey = %q, want lin_api_token", apiKey)
		}
		return []linear.ProjectSummary{
			{Name: "Alpha", Slug: "alpha"},
			{Name: "Beta Platform", Slug: "beta"},
		}, nil
	}
	defer func() {
		listLinearProjects = oldListLinearProjects
	}()

	model := tuiModel{
		step:         tuiStepIntro,
		prereqs:      Prerequisites{LinearAPIKeyConfigured: true, GitHubTokenConfigured: true},
		linearAPIKey: "lin_api_token",
		githubToken:  "github_pat_token",
		baseRef:      "main",
		repoURL:      "git@github.com:acme/repo.git",
		serverPort:   "8888",
	}

	next, cmd := model.updateIntroKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd == nil {
		t.Fatal("updateIntroKey() command = nil, want project fetch command")
	}
	if !updated.projectLoading {
		t.Fatal("projectLoading = false, want true")
	}

	msg := cmd()
	withProjectsModel, _ := updated.Update(msg)
	withProjects := withProjectsModel.(tuiModel)
	if withProjects.projectManualMode {
		t.Fatal("projectManualMode = true, want false")
	}
	if len(withProjects.projectOptions) != 2 {
		t.Fatalf("projectOptions = %d, want 2", len(withProjects.projectOptions))
	}

	next, _ = withProjects.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "b"}))
	filteredModel := next.(tuiModel)
	next, _ = filteredModel.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "e"}))
	filteredModel = next.(tuiModel)
	if got := filteredModel.filteredProjects(); len(got) != 1 || got[0].Slug != "beta" {
		t.Fatalf("filteredProjects() = %#v, want beta only", got)
	}

	next, _ = filteredModel.updateProjectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	selected := next.(tuiModel)
	if selected.projectSlug != "beta" {
		t.Fatalf("projectSlug = %q, want beta", selected.projectSlug)
	}
	if selected.step != tuiStepRepoURL {
		t.Fatalf("step = %d, want %d", selected.step, tuiStepRepoURL)
	}
}

func TestTUIProjectStepFallsBackToManualOnFetchError(t *testing.T) {
	oldListLinearProjects := listLinearProjects
	listLinearProjects = func(context.Context, string, string) ([]linear.ProjectSummary, error) {
		return nil, errors.New("boom")
	}
	defer func() {
		listLinearProjects = oldListLinearProjects
	}()

	model := tuiModel{
		step:         tuiStepIntro,
		prereqs:      Prerequisites{LinearAPIKeyConfigured: true, GitHubTokenConfigured: true},
		linearAPIKey: "lin_api_token",
		githubToken:  "github_pat_token",
	}

	next, cmd := model.updateIntroKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd == nil {
		t.Fatal("updateIntroKey() command = nil, want project fetch command")
	}

	msg := cmd()
	withErrorModel, _ := updated.Update(msg)
	withError := withErrorModel.(tuiModel)
	if !withError.projectManualMode {
		t.Fatal("projectManualMode = false, want true")
	}
	if !strings.Contains(withError.inlineError, "Failed to fetch Linear projects") {
		t.Fatalf("inlineError = %q, want fetch failure guidance", withError.inlineError)
	}
	if got := withError.projectFetchError; got != "boom" {
		t.Fatalf("projectFetchError = %q, want boom", got)
	}
}

func TestTUIIntroPromptsForLinearAPIKeyWhenShellIsMissingIt(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:    tuiStepIntro,
		prereqs: Prerequisites{},
	}

	next, cmd := model.updateIntroKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd != nil {
		t.Fatal("updateIntroKey() returned project fetch command before API key entry")
	}
	if updated.step != tuiStepLinearAPIKey {
		t.Fatalf("step = %d, want %d", updated.step, tuiStepLinearAPIKey)
	}
}

func TestTUIIntroPromptsForGitHubTokenWhenLinearTokenIsAvailable(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:         tuiStepIntro,
		prereqs:      Prerequisites{},
		linearAPIKey: "lin_api_token",
	}

	next, cmd := model.updateIntroKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd != nil {
		t.Fatal("updateIntroKey() returned command before GitHub token entry")
	}
	if updated.step != tuiStepGitHubToken {
		t.Fatalf("step = %d, want %d", updated.step, tuiStepGitHubToken)
	}
}

func TestTUIProjectStepAllowsManualSlugEntryWithoutLinearToken(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:              tuiStepProjectSlug,
		projectManualMode: true,
	}

	next, _ := model.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "m"}))
	updated := next.(tuiModel)
	next, _ = updated.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "y"}))
	updated = next.(tuiModel)
	next, _ = updated.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "-"}))
	updated = next.(tuiModel)
	next, _ = updated.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "p"}))
	updated = next.(tuiModel)
	next, _ = updated.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "r"}))
	updated = next.(tuiModel)
	next, _ = updated.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "o"}))
	updated = next.(tuiModel)
	next, _ = updated.updateProjectKey(tea.KeyPressMsg(tea.Key{Text: "j"}))
	updated = next.(tuiModel)

	if updated.projectSlug != "my-proj" {
		t.Fatalf("projectSlug = %q, want my-proj", updated.projectSlug)
	}

	next, cmd := updated.updateProjectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	advanced := next.(tuiModel)
	if cmd != nil {
		t.Fatal("updateProjectKey() command != nil, want nil after manual slug entry")
	}
	if advanced.step != tuiStepRepoURL {
		t.Fatalf("step = %d, want %d", advanced.step, tuiStepRepoURL)
	}
}

func TestTUILinearAPIKeyStepAcceptsPaste(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step: tuiStepLinearAPIKey,
	}

	nextModel, _ := model.Update(tea.PasteMsg{Content: "lin_api_123\n"})
	next := nextModel.(tuiModel)
	if next.linearAPIKey != "lin_api_123" {
		t.Fatalf("linearAPIKey = %q, want lin_api_123", next.linearAPIKey)
	}
}

func TestTUILinearAPIKeyStepAdvancesAfterValidPaste(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:         tuiStepLinearAPIKey,
		linearAPIKey: "lin_api_valid",
	}

	next, cmd := model.updateLinearAPIKeyKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd != nil {
		t.Fatal("updateLinearAPIKeyKey() command != nil, want nil after advancing to GitHub token step")
	}
	if updated.step != tuiStepGitHubToken {
		t.Fatalf("step = %d, want %d", updated.step, tuiStepGitHubToken)
	}
}

func TestTUILinearAPIKeyStepShowsImmediateFeedbackAfterInvalidPaste(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step: tuiStepLinearAPIKey,
	}

	nextModel, _ := model.Update(tea.PasteMsg{Content: "bad-token\n"})
	next := nextModel.(tuiModel)
	if next.linearAPIKey != "bad-token" {
		t.Fatalf("linearAPIKey = %q, want bad-token", next.linearAPIKey)
	}
	if !strings.Contains(next.inlineError, "lin_api_") {
		t.Fatalf("inlineError = %q, want lin_api_ guidance immediately after paste", next.inlineError)
	}
}

func TestTUILinearAPIKeyStepRejectsInvalidPrefix(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:         tuiStepLinearAPIKey,
		linearAPIKey: "bad-token",
	}

	next, cmd := model.updateLinearAPIKeyKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd != nil {
		t.Fatal("updateLinearAPIKeyKey() command != nil, want nil on invalid token")
	}
	if !strings.Contains(updated.inlineError, "lin_api_") {
		t.Fatalf("inlineError = %q, want lin_api_ guidance", updated.inlineError)
	}
}

func TestTUIGitHubTokenStepAcceptsPaste(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step: tuiStepGitHubToken,
	}

	nextModel, _ := model.Update(tea.PasteMsg{Content: "github_pat_123\n"})
	next := nextModel.(tuiModel)
	if next.githubToken != "github_pat_123" {
		t.Fatalf("githubToken = %q, want github_pat_123", next.githubToken)
	}
}

func TestTUIGitHubTokenStepRejectsInvalidPrefix(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:        tuiStepGitHubToken,
		githubToken: "ghx_old",
	}

	next, cmd := model.updateGitHubTokenKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd != nil {
		t.Fatal("updateGitHubTokenKey() command != nil, want nil on invalid token")
	}
	if !strings.Contains(updated.inlineError, "github_pat_") {
		t.Fatalf("inlineError = %q, want GitHub token prefix guidance", updated.inlineError)
	}
}

func TestTUIGitHubTokenStepAcceptsClassicPATPrefix(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:        tuiStepGitHubToken,
		githubToken: "ghp_classic",
	}

	next, cmd := model.updateGitHubTokenKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	updated := next.(tuiModel)
	if cmd != nil {
		t.Fatal("updateGitHubTokenKey() command != nil, want nil after advancing")
	}
	if updated.step != tuiStepProjectSlug {
		t.Fatalf("step = %d, want %d", updated.step, tuiStepProjectSlug)
	}
	if updated.inlineError != "" {
		t.Fatalf("inlineError = %q, want empty", updated.inlineError)
	}
}

func TestTUIIntroShowsGitHubTokenInsteadOfGitAvailability(t *testing.T) {
	t.Parallel()

	model := tuiModel{
		step:        tuiStepIntro,
		githubToken: "github_pat_token",
	}

	view := model.renderIntro()
	if !strings.Contains(view, "GITHUB_TOKEN or GH_TOKEN configured: yes") {
		t.Fatalf("renderIntro() = %q, want GitHub token prerequisite", view)
	}
	if strings.Contains(view, "git available:") {
		t.Fatalf("renderIntro() = %q, unexpected git available prerequisite", view)
	}
}

func TestLinearAPIKeyInputWidth(t *testing.T) {
	t.Parallel()

	if linearAPIKeyInputWidth != 51 {
		t.Fatalf("linearAPIKeyInputWidth = %d, want 51", linearAPIKeyInputWidth)
	}
}

func TestFormatProjectOptionJustifiesColumns(t *testing.T) {
	t.Parallel()

	projects := []linear.ProjectSummary{
		{Name: "bias", Slug: "1c96f77d4505"},
		{Name: "cli", Slug: "0ece25450f8d"},
		{Name: "Mondo", Slug: "9a21ddb36749"},
	}
	nameWidth, slugWidth := projectColumnWidths(projects)

	got := []string{
		formatProjectOption("  ", projects[0], nameWidth, slugWidth),
		formatProjectOption("> ", projects[1], nameWidth, slugWidth),
		formatProjectOption("  ", projects[2], nameWidth, slugWidth),
	}
	want := []string{
		"  bias  [1c96f77d4505]",
		"> cli   [0ece25450f8d]",
		"  Mondo [9a21ddb36749]",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("formatProjectOption() row %d = %q, want %q", i, got[i], want[i])
		}
	}
}
