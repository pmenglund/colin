package bootstrap

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/pmenglund/colin/internal/service"
	"github.com/pmenglund/colin/internal/tracker/linear"
)

const (
	tuiStepIntro = iota
	tuiStepLinearAPIKey
	tuiStepGitHubToken
	tuiStepProjectSlug
	tuiStepRepoURL
	tuiStepBaseRef
	tuiStepWorkspaceRoot
	tuiStepServerPort
	tuiStepWebhook
	tuiStepReview
	tuiStepSuccess

	linearAPIKeyInputWidth = 51
)

type tuiPreflightMsg struct {
	report service.ConfigPreflightReport
	err    error
}

type tuiSaveMsg struct {
	result Result
	err    error
}

type tuiProjectsMsg struct {
	projects []linear.ProjectSummary
	err      error
}

type tuiModel struct {
	opts         Options
	workflowPath string
	prereqs      Prerequisites
	defaults     detectedDefaults

	step int

	linearAPIKey  string
	githubToken   string
	projectSlug   string
	projectFilter string
	repoURL       string
	baseRef       string
	workspaceRoot string
	serverPort    string
	wantsWebhook  bool

	cursorPos              int
	projectFilterCursorPos int
	inlineError            string
	fatalErr               error

	projectManualMode     bool
	projectFetchAttempted bool
	projectLoading        bool
	projectFetchError     string
	projectOptions        []linear.ProjectSummary
	projectSelection      int

	preflightAttempted bool
	preflightRunning   bool
	preflightDirty     bool
	preflightReport    service.ConfigPreflightReport
	preflightErr       error

	overwriteRequired  bool
	overwriteConfirmed bool

	saving bool

	result Result
}

var (
	tuiTitleStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	tuiSubtitleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiLabelStyle        = lipgloss.NewStyle().Bold(true)
	tuiHintStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiInputStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	tuiFocusInputStyle   = tuiInputStyle.Copy().BorderForeground(lipgloss.Color("12"))
	tuiErrorStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	tuiSuccessStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	tuiWarnStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	tuiReviewValueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	tuiSelectedListStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	tuiListStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	tuiProgressBarFilled = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("■")
	tuiProgressBarEmpty  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("■")
)

var listLinearProjects = linear.ListProjects

func RunTUI(in io.Reader, out io.Writer, opts Options) (Result, error) {
	resolved, err := resolveOptions(opts)
	if err != nil {
		return Result{}, err
	}

	model := tuiModel{
		opts:              opts,
		workflowPath:      resolved.workflowPath,
		prereqs:           resolved.prereqs,
		defaults:          resolved.defaults,
		step:              tuiStepIntro,
		linearAPIKey:      resolved.linearAPIKey,
		githubToken:       resolved.githubToken,
		projectSlug:       resolved.defaults.ProjectSlug,
		repoURL:           resolved.defaults.RepoURL,
		baseRef:           resolved.defaults.BaseRef,
		workspaceRoot:     resolved.defaults.WorkspaceRoot,
		serverPort:        fmt.Sprintf("%d", resolved.defaults.ServerPort),
		projectManualMode: !isValidLinearAPIKey(resolved.linearAPIKey),
		preflightDirty:    true,
	}
	model.cursorPos = utf8.RuneCountInString(model.projectSlug)

	program := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out))
	finalModel, err := program.Run()
	if err != nil {
		return Result{}, err
	}
	final, ok := finalModel.(tuiModel)
	if !ok {
		return Result{}, fmt.Errorf("unexpected config UI model %T", finalModel)
	}
	if final.fatalErr != nil {
		return Result{}, final.fatalErr
	}
	return final.result, nil
}

func (m tuiModel) Init() tea.Cmd {
	return nil
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case tea.PasteMsg:
		return m.updatePaste(msg)
	case tuiProjectsMsg:
		m.projectLoading = false
		m.projectFetchAttempted = true
		if msg.err != nil {
			m.projectFetchError = msg.err.Error()
			m.projectManualMode = true
			m.inlineError = "Failed to fetch Linear projects. Enter a slug manually or press r to retry."
			return m, nil
		}
		if len(msg.projects) == 0 {
			m.projectFetchError = "No accessible Linear projects were returned."
			m.projectManualMode = true
			m.inlineError = "No projects found. Enter a slug manually or press r to retry."
			return m, nil
		}
		m.projectFetchError = ""
		m.projectOptions = msg.projects
		m.projectSelection = 0
		m.projectManualMode = false
		m.inlineError = ""
		return m, nil
	case tuiPreflightMsg:
		m.preflightRunning = false
		m.preflightAttempted = true
		m.preflightReport = msg.report
		m.preflightErr = msg.err
		if msg.err != nil {
			m.inlineError = "Linear preflight failed. Review the checks below."
		} else {
			m.inlineError = ""
		}
		return m, nil
	case tuiSaveMsg:
		m.saving = false
		if msg.err != nil {
			m.inlineError = msg.err.Error()
			return m, nil
		}
		m.result = msg.result
		m.step = tuiStepSuccess
		m.inlineError = ""
		return m, nil
	}
	return m, nil
}

func (m tuiModel) updatePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if m.saving {
		return m, nil
	}

	text := sanitizePastedText(msg.String())
	if text == "" {
		return m, nil
	}

	switch m.step {
	case tuiStepLinearAPIKey, tuiStepGitHubToken, tuiStepRepoURL, tuiStepBaseRef, tuiStepWorkspaceRoot, tuiStepServerPort:
		m.insertText(text)
		m.inlineError = m.liveValidationError()
		if m.step != tuiStepLinearAPIKey {
			m.preflightDirty = true
		}
		return m, nil
	case tuiStepProjectSlug:
		if m.projectManualMode {
			m.insertText(text)
			m.preflightDirty = true
		} else {
			m.insertProjectFilter(text)
		}
		m.inlineError = ""
		return m, nil
	default:
		return m, nil
	}
}

func (m tuiModel) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		if m.step == tuiStepSuccess {
			return m, quitCmd()
		}
		m.fatalErr = ErrAborted
		return m, quitCmd()
	}

	if m.saving {
		return m, nil
	}

	switch m.step {
	case tuiStepIntro:
		return m.updateIntroKey(msg)
	case tuiStepLinearAPIKey:
		return m.updateLinearAPIKeyKey(msg)
	case tuiStepGitHubToken:
		return m.updateGitHubTokenKey(msg)
	case tuiStepProjectSlug:
		return m.updateProjectKey(msg)
	case tuiStepRepoURL, tuiStepBaseRef, tuiStepWorkspaceRoot, tuiStepServerPort:
		return m.updateTextStepKey(msg)
	case tuiStepWebhook:
		return m.updateWebhookKey(msg)
	case tuiStepReview:
		return m.updateReviewKey(msg)
	case tuiStepSuccess:
		switch msg.String() {
		case "enter", "q":
			return m, quitCmd()
		}
	}

	return m, nil
}

func (m tuiModel) updateIntroKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", " ", "right", "n":
		m.step = m.nextVisibleStep(m.step)
		m.cursorPos = utf8.RuneCountInString(m.currentTextValue())
		m.inlineError = ""
		if m.step == tuiStepProjectSlug {
			if cmd := m.enterProjectStepCmd(); cmd != nil {
				m.projectLoading = true
				return m, cmd
			}
		}
	case "q":
		m.fatalErr = ErrAborted
		return m, quitCmd()
	}
	return m, nil
}

func (m tuiModel) updateLinearAPIKeyKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "tab":
		if errText := validateOptionalLinearAPIKey(m.linearAPIKey); errText != "" {
			m.inlineError = errText
			return m, nil
		}
		m.projectFetchAttempted = false
		m.projectFetchError = ""
		m.projectOptions = nil
		m.projectSelection = 0
		m.projectFilter = ""
		m.projectFilterCursorPos = 0
		m.projectManualMode = !m.hasLinearAPIKey()
		m.preflightDirty = true
		return m.advanceStep()
	case "shift+tab":
		return m.previousStep(), nil
	case "backspace":
		m.deleteLeft()
	case "delete":
		m.deleteRight()
	case "home", "ctrl+a":
		m.cursorPos = 0
	case "end", "ctrl+e":
		m.cursorPos = utf8.RuneCountInString(m.currentTextValue())
	case "right":
		if m.cursorPos < utf8.RuneCountInString(m.currentTextValue()) {
			m.cursorPos++
		}
	default:
		key := msg.Key()
		if key.Text != "" {
			m.insertText(key.Text)
		}
	}
	m.inlineError = m.liveValidationError()
	return m, nil
}

func (m tuiModel) updateGitHubTokenKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "tab":
		if errText := validateOptionalGitHubToken(m.githubToken); errText != "" {
			m.inlineError = errText
			return m, nil
		}
		return m.advanceStep()
	case "shift+tab":
		return m.previousStep(), nil
	case "backspace":
		m.deleteLeft()
	case "delete":
		m.deleteRight()
	case "home", "ctrl+a":
		m.cursorPos = 0
	case "end", "ctrl+e":
		m.cursorPos = utf8.RuneCountInString(m.currentTextValue())
	case "right":
		if m.cursorPos < utf8.RuneCountInString(m.currentTextValue()) {
			m.cursorPos++
		}
	default:
		key := msg.Key()
		if key.Text != "" {
			m.insertText(key.Text)
		}
	}
	m.inlineError = m.liveValidationError()
	return m, nil
}

func (m tuiModel) updateProjectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "shift+tab":
		return m.previousStep(), nil
	case "ctrl+r":
		if !m.hasLinearAPIKey() {
			m.inlineError = "LINEAR_API_KEY is not available. Shift+Tab to add one for this setup session."
			return m, nil
		}
		m.projectManualMode = false
		m.inlineError = ""
		m.projectFetchError = ""
		m.projectLoading = true
		return m, m.fetchProjectsCmd()
	}

	if m.projectManualMode {
		switch msg.String() {
		case "enter":
			if errText := validateProjectSlug(m.projectSlug); errText != "" {
				m.inlineError = errText
				return m, nil
			}
			return m.advanceStep()
		case "tab":
			if errText := validateProjectSlug(m.projectSlug); errText != "" {
				m.inlineError = errText
				return m, nil
			}
			return m.advanceStep()
		case "s":
			if len(m.projectOptions) > 0 {
				m.projectManualMode = false
				m.inlineError = ""
				m.projectFilterCursorPos = utf8.RuneCountInString(m.projectFilter)
				return m, nil
			}
		case "backspace":
			m.deleteLeft()
		case "delete":
			m.deleteRight()
		case "home", "ctrl+a":
			m.cursorPos = 0
		case "end", "ctrl+e":
			m.cursorPos = utf8.RuneCountInString(m.currentTextValue())
		case "right":
			if m.cursorPos < utf8.RuneCountInString(m.currentTextValue()) {
				m.cursorPos++
			}
		default:
			key := msg.Key()
			if key.Text != "" {
				m.insertText(key.Text)
			}
		}
		m.inlineError = ""
		m.preflightDirty = true
		return m, nil
	}

	if m.projectLoading {
		switch msg.String() {
		case "m":
			m.projectManualMode = true
			m.cursorPos = utf8.RuneCountInString(m.projectSlug)
			m.inlineError = ""
		}
		return m, nil
	}

	switch msg.String() {
	case "up":
		if m.projectSelection > 0 {
			m.projectSelection--
		}
	case "down":
		if m.projectSelection < len(m.filteredProjects())-1 {
			m.projectSelection++
		}
	case "enter", "tab":
		filtered := m.filteredProjects()
		if len(filtered) == 0 {
			m.inlineError = "No projects match the current filter. Adjust the filter or press m for manual entry."
			return m, nil
		}
		selected := filtered[minInt(m.projectSelection, len(filtered)-1)]
		m.projectSlug = selected.Slug
		m.preflightDirty = true
		m.inlineError = ""
		return m.advanceStep()
	case "m":
		m.projectManualMode = true
		m.cursorPos = utf8.RuneCountInString(m.projectSlug)
		m.inlineError = ""
	case "backspace":
		m.deleteProjectFilterLeft()
	case "delete":
		m.deleteProjectFilterRight()
	case "home", "ctrl+a":
		m.projectFilterCursorPos = 0
	case "end", "ctrl+e":
		m.projectFilterCursorPos = utf8.RuneCountInString(m.projectFilter)
	case "left":
		if m.projectFilterCursorPos > 0 {
			m.projectFilterCursorPos--
		}
	case "right":
		if m.projectFilterCursorPos < utf8.RuneCountInString(m.projectFilter) {
			m.projectFilterCursorPos++
		}
	default:
		key := msg.Key()
		if key.Text != "" {
			m.insertProjectFilter(key.Text)
		}
	}
	m.inlineError = ""
	return m, nil
}

func (m tuiModel) updateTextStepKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if errText := m.validateCurrentStep(); errText != "" {
			m.inlineError = errText
			return m, nil
		}
		return m.advanceStep()
	case "shift+tab":
		return m.previousStep(), nil
	case "tab":
		if errText := m.validateCurrentStep(); errText != "" {
			m.inlineError = errText
			return m, nil
		}
		return m.advanceStep()
	case "backspace":
		m.deleteLeft()
	case "delete":
		m.deleteRight()
	case "home", "ctrl+a":
		m.cursorPos = 0
	case "end", "ctrl+e":
		m.cursorPos = utf8.RuneCountInString(m.currentTextValue())
	case "right":
		if m.cursorPos < utf8.RuneCountInString(m.currentTextValue()) {
			m.cursorPos++
		}
	default:
		key := msg.Key()
		if key.Text != "" {
			m.insertText(key.Text)
		}
	}
	m.inlineError = ""
	m.preflightDirty = true
	return m, nil
}

func (m tuiModel) updateWebhookKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "h", "n":
		m.wantsWebhook = false
	case "right", "l", "y", " ":
		m.wantsWebhook = true
	case "shift+tab":
		return m.previousStep(), nil
	case "tab", "enter":
		return m.advanceStep()
	}
	m.inlineError = ""
	m.preflightDirty = true
	return m, nil
}

func (m tuiModel) updateReviewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "shift+tab":
		return m.previousStep(), nil
	case "r":
		if !m.hasLinearAPIKey() {
			m.inlineError = "LINEAR_API_KEY is not available, so live Linear checks are skipped."
			return m, nil
		}
		m.inlineError = ""
		m.preflightRunning = true
		return m, m.runPreflightCmd()
	case "o", " ":
		if m.overwriteRequired {
			m.overwriteConfirmed = !m.overwriteConfirmed
			m.inlineError = ""
		}
	case "enter":
		if m.preflightRunning {
			return m, nil
		}
		if m.hasLinearAPIKey() && (!m.preflightAttempted || m.preflightErr != nil || !m.preflightReport.Ready()) {
			m.inlineError = "Resolve the Linear preflight errors before writing the workflow."
			return m, nil
		}
		if m.overwriteRequired && !m.overwriteConfirmed {
			m.inlineError = "Confirm overwrite before writing the workflow."
			return m, nil
		}
		m.saving = true
		m.inlineError = ""
		return m, m.saveWorkflowCmd()
	}
	return m, nil
}

func (m tuiModel) advanceStep() (tea.Model, tea.Cmd) {
	if m.step < tuiStepReview {
		m.step = m.nextVisibleStep(m.step)
	}
	if m.step == tuiStepProjectSlug {
		m.cursorPos = utf8.RuneCountInString(m.projectSlug)
		if cmd := m.enterProjectStepCmd(); cmd != nil {
			m.projectLoading = true
			return m, cmd
		}
		return m, nil
	}
	if m.step == tuiStepReview {
		overwrite, err := overwriteNeeded(m.workflowPath)
		if err != nil {
			m.inlineError = err.Error()
			return m, nil
		}
		m.overwriteRequired = overwrite
		m.overwriteConfirmed = !overwrite
		m.cursorPos = 0
		m.inlineError = ""
		if m.hasLinearAPIKey() && m.preflightDirty {
			m.preflightDirty = false
			m.preflightRunning = true
			m.preflightErr = nil
			m.preflightReport = service.ConfigPreflightReport{}
			return m, m.runPreflightCmd()
		}
	}
	m.cursorPos = utf8.RuneCountInString(m.currentTextValue())
	return m, nil
}

func (m tuiModel) previousStep() tuiModel {
	if m.step > tuiStepIntro {
		m.step = m.previousVisibleStep(m.step)
	}
	m.inlineError = ""
	m.cursorPos = utf8.RuneCountInString(m.currentTextValue())
	return m
}

func (m tuiModel) hasLinearAPIKey() bool {
	return isValidLinearAPIKey(m.linearAPIKey)
}

func (m tuiModel) hasGitHubToken() bool {
	return isValidGitHubToken(m.githubToken)
}

func (m tuiModel) visibleSteps() []int {
	steps := []int{tuiStepIntro}
	if !m.hasLinearAPIKey() {
		steps = append(steps, tuiStepLinearAPIKey)
	}
	if !m.hasGitHubToken() {
		steps = append(steps, tuiStepGitHubToken)
	}
	return append(steps,
		tuiStepProjectSlug,
		tuiStepRepoURL,
		tuiStepBaseRef,
		tuiStepWorkspaceRoot,
		tuiStepServerPort,
		tuiStepWebhook,
		tuiStepReview,
	)
}

func (m tuiModel) nextVisibleStep(step int) int {
	steps := m.visibleSteps()
	for _, candidate := range steps {
		if candidate > step {
			return candidate
		}
	}
	return step
}

func (m tuiModel) previousVisibleStep(step int) int {
	steps := m.visibleSteps()
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i] < step {
			return steps[i]
		}
	}
	return step
}

func (m tuiModel) currentTextValue() string {
	switch m.step {
	case tuiStepLinearAPIKey:
		return m.linearAPIKey
	case tuiStepGitHubToken:
		return m.githubToken
	case tuiStepProjectSlug:
		return m.projectSlug
	case tuiStepRepoURL:
		return m.repoURL
	case tuiStepBaseRef:
		return m.baseRef
	case tuiStepWorkspaceRoot:
		return m.workspaceRoot
	case tuiStepServerPort:
		return m.serverPort
	default:
		return ""
	}
}

func (m *tuiModel) setCurrentTextValue(value string) {
	switch m.step {
	case tuiStepLinearAPIKey:
		m.linearAPIKey = value
	case tuiStepGitHubToken:
		m.githubToken = value
	case tuiStepProjectSlug:
		m.projectSlug = value
	case tuiStepRepoURL:
		m.repoURL = value
	case tuiStepBaseRef:
		m.baseRef = value
	case tuiStepWorkspaceRoot:
		m.workspaceRoot = value
	case tuiStepServerPort:
		m.serverPort = value
	}
}

func (m *tuiModel) insertText(text string) {
	value := []rune(m.currentTextValue())
	pos := minInt(m.cursorPos, len(value))
	value = append(value[:pos], append([]rune(text), value[pos:]...)...)
	m.setCurrentTextValue(string(value))
	m.cursorPos = pos + utf8.RuneCountInString(text)
}

func (m *tuiModel) deleteLeft() {
	value := []rune(m.currentTextValue())
	if m.cursorPos <= 0 || len(value) == 0 {
		return
	}
	pos := minInt(m.cursorPos, len(value))
	value = append(value[:pos-1], value[pos:]...)
	m.setCurrentTextValue(string(value))
	m.cursorPos = pos - 1
}

func (m *tuiModel) deleteRight() {
	value := []rune(m.currentTextValue())
	pos := minInt(m.cursorPos, len(value))
	if pos >= len(value) {
		return
	}
	value = append(value[:pos], value[pos+1:]...)
	m.setCurrentTextValue(string(value))
}

func (m tuiModel) validateCurrentStep() string {
	switch m.step {
	case tuiStepLinearAPIKey:
		return validateOptionalLinearAPIKey(m.linearAPIKey)
	case tuiStepGitHubToken:
		return validateOptionalGitHubToken(m.githubToken)
	case tuiStepRepoURL:
		return validateRepoURL(m.repoURL)
	case tuiStepBaseRef:
		return validateBaseRef(m.baseRef)
	case tuiStepWorkspaceRoot:
		return validateWorkspaceRoot(m.workspaceRoot)
	case tuiStepServerPort:
		return validateServerPort(m.serverPort)
	default:
		return ""
	}
}

func (m tuiModel) answers() (Answers, error) {
	port, err := parseServerPort(m.serverPort)
	if err != nil {
		return Answers{}, err
	}
	return Answers{
		ProjectSlug:   strings.TrimSpace(m.projectSlug),
		RepoURL:       strings.TrimSpace(m.repoURL),
		BaseRef:       strings.TrimSpace(m.baseRef),
		WorkspaceRoot: strings.TrimSpace(m.workspaceRoot),
		ServerPort:    port,
		WantsWebhook:  m.wantsWebhook,
	}, nil
}

func (m tuiModel) runPreflightCmd() tea.Cmd {
	answers, err := m.answers()
	if err != nil {
		return func() tea.Msg {
			return tuiPreflightMsg{err: err}
		}
	}
	return func() tea.Msg {
		_, cfg, err := buildConfigFromAnswers(m.workflowPath, answers, m.linearAPIKey, m.githubToken)
		if err != nil {
			return tuiPreflightMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, report, err := service.PreflightTrackerConfig(ctx, cfg)
		return tuiPreflightMsg{report: report, err: err}
	}
}

func (m tuiModel) saveWorkflowCmd() tea.Cmd {
	answers, err := m.answers()
	if err != nil {
		return func() tea.Msg {
			return tuiSaveMsg{err: err}
		}
	}
	return func() tea.Msg {
		content, _, err := buildConfigFromAnswers(m.workflowPath, answers, m.linearAPIKey, m.githubToken)
		if err != nil {
			return tuiSaveMsg{err: err}
		}
		if err := WriteWorkflow(m.workflowPath, content, m.overwriteRequired); err != nil {
			return tuiSaveMsg{err: err}
		}
		if err := applySessionLinearAPIKey(m.linearAPIKey); err != nil {
			return tuiSaveMsg{err: err}
		}
		if err := applySessionGitHubToken(m.githubToken); err != nil {
			return tuiSaveMsg{err: err}
		}
		result := resultFromAnswers(m.workflowPath, answers, m.prereqs)
		result.SessionLinearAPIKeyLoaded = m.hasLinearAPIKey() && !m.prereqs.LinearAPIKeyConfigured
		result.SessionGitHubTokenLoaded = m.hasGitHubToken() && !m.prereqs.GitHubTokenConfigured
		return tuiSaveMsg{
			result: result,
		}
	}
}

func (m tuiModel) View() tea.View {
	content := strings.TrimSpace(m.render())
	if content == "" {
		content = "Loading Colin config..."
	}
	view := tea.NewView(content + "\n")
	view.AltScreen = true
	return view
}

func (m tuiModel) render() string {
	switch m.step {
	case tuiStepIntro:
		return m.renderIntro()
	case tuiStepLinearAPIKey:
		return m.renderLinearAPIKeyStep()
	case tuiStepGitHubToken:
		return m.renderGitHubTokenStep()
	case tuiStepProjectSlug:
		return m.renderProjectStep()
	case tuiStepRepoURL, tuiStepBaseRef, tuiStepWorkspaceRoot, tuiStepServerPort:
		return m.renderTextStep()
	case tuiStepWebhook:
		return m.renderWebhookStep()
	case tuiStepReview:
		return m.renderReview()
	case tuiStepSuccess:
		return m.renderSuccess()
	default:
		return ""
	}
}

func (m tuiModel) renderIntro() string {
	lines := []string{
		tuiTitleStyle.Render("Colin config"),
		tuiSubtitleStyle.Render("Bubble Tea setup wizard for WORKFLOW.md"),
		"",
		tuiLabelStyle.Render("Prerequisites"),
		fmt.Sprintf("- LINEAR_API_KEY available: %s", yesNo(m.hasLinearAPIKey())),
		fmt.Sprintf("- GITHUB_TOKEN or GH_TOKEN configured: %s", yesNo(m.hasGitHubToken())),
		fmt.Sprintf("- codex available: %s", yesNo(m.prereqs.CodexAvailable)),
		"",
		tuiHintStyle.Render("Enter starts the wizard. Esc cancels."),
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderLinearAPIKeyStep() string {
	lines := []string{
		m.renderProgress(),
		tuiTitleStyle.Render("Linear API key"),
		tuiSubtitleStyle.Render("Enter LINEAR_API_KEY for this setup session. It is not written to WORKFLOW.md."),
		"",
		m.renderMaskedInputWidth(m.linearAPIKey, "Leave blank to skip", linearAPIKeyInputWidth),
		tuiHintStyle.Render("When provided, LINEAR_API_KEY should start with lin_api_."),
	}
	if m.inlineError != "" {
		lines = append(lines, "", tuiErrorStyle.Render(m.inlineError))
	}
	lines = append(lines, "", tuiHintStyle.Render("Enter or Tab continues. Shift+Tab goes back. Esc cancels."))
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderGitHubTokenStep() string {
	lines := []string{
		m.renderProgress(),
		tuiTitleStyle.Render("GitHub token"),
		tuiSubtitleStyle.Render("Enter GITHUB_TOKEN for this setup session. It is not written to WORKFLOW.md."),
		"",
		m.renderMaskedInput(m.githubToken, "Leave blank to skip"),
		tuiHintStyle.Render("When provided, GITHUB_TOKEN should start with github_pat_ or ghp_."),
	}
	if m.inlineError != "" {
		lines = append(lines, "", tuiErrorStyle.Render(m.inlineError))
	}
	lines = append(lines, "", tuiHintStyle.Render("Enter or Tab continues. Shift+Tab goes back. Esc cancels."))
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderTextStep() string {
	label, description, value := m.textStepDetails()
	input := m.renderInput(value)
	lines := []string{
		m.renderProgress(),
		tuiTitleStyle.Render(label),
		tuiSubtitleStyle.Render(description),
		"",
		input,
	}
	if preview := m.textStepPreview(); preview != "" {
		lines = append(lines, tuiHintStyle.Render(preview))
	}
	if m.inlineError != "" {
		lines = append(lines, "", tuiErrorStyle.Render(m.inlineError))
	}
	lines = append(lines, "", tuiHintStyle.Render("Enter or Tab advances. Shift+Tab goes back. Esc cancels."))
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderProjectStep() string {
	lines := []string{
		m.renderProgress(),
		tuiTitleStyle.Render("Linear project"),
	}

	if m.projectManualMode {
		lines = append(lines,
			tuiSubtitleStyle.Render("Enter the Linear project slug manually."),
			"",
			m.renderInput(m.projectSlug),
		)
		if len(m.projectOptions) > 0 {
			lines = append(lines, tuiHintStyle.Render("Press s to return to the project selector."))
		} else if m.hasLinearAPIKey() {
			lines = append(lines, tuiHintStyle.Render("Press Ctrl+R to retry loading projects from Linear."))
		} else {
			lines = append(lines, tuiHintStyle.Render("Shift+Tab to add a LINEAR_API_KEY for project lookup."))
		}
	} else {
		lines = append(lines,
			tuiSubtitleStyle.Render("Filter and select the Linear project Colin should watch."),
			"",
			tuiFocusInputStyle.Render(m.renderProjectFilter()),
		)
		if m.projectLoading {
			lines = append(lines, "", tuiWarnStyle.Render("Loading projects from Linear..."))
		} else {
			filtered := m.filteredProjects()
			if len(filtered) == 0 {
				lines = append(lines, "", tuiWarnStyle.Render("No projects match the current filter."))
			} else {
				nameWidth, slugWidth := projectColumnWidths(filtered)
				lines = append(lines, "", tuiLabelStyle.Render("Projects"))
				for i, project := range filtered {
					if i >= 8 {
						lines = append(lines, tuiHintStyle.Render(fmt.Sprintf("...and %d more", len(filtered)-i)))
						break
					}
					lines = append(lines, m.renderProjectOption(i, project, nameWidth, slugWidth))
				}
			}
		}
		if m.projectSlug != "" {
			lines = append(lines, "", tuiHintStyle.Render("Selected slug: "+m.projectSlug))
		}
	}

	if m.projectFetchError != "" {
		lines = append(lines, "", tuiWarnStyle.Render(m.projectFetchError))
	}
	if m.inlineError != "" {
		lines = append(lines, "", tuiErrorStyle.Render(m.inlineError))
	}

	hint := "Enter or Tab advances. Shift+Tab goes back. Esc cancels."
	if m.projectManualMode {
		if m.hasLinearAPIKey() {
			hint = "Enter or Tab advances. Ctrl+R retries project lookup. Shift+Tab goes back. Esc cancels."
		} else {
			hint = "Enter or Tab advances. Shift+Tab adds a LINEAR_API_KEY. Esc cancels."
		}
	} else {
		hint = "Type to filter, Up/Down selects, Enter chooses, m switches to manual slug entry."
	}
	lines = append(lines, "", tuiHintStyle.Render(hint))
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderWebhookStep() string {
	noStyle := tuiInputStyle
	yesStyle := tuiInputStyle
	if !m.wantsWebhook {
		noStyle = tuiFocusInputStyle
	}
	if m.wantsWebhook {
		yesStyle = tuiFocusInputStyle
	}
	lines := []string{
		m.renderProgress(),
		tuiTitleStyle.Render("Webhook guidance"),
		tuiSubtitleStyle.Render("Choose whether setup should include Tailscale webhook follow-up guidance."),
		"",
		lipgloss.JoinHorizontal(lipgloss.Top,
			noStyle.Render("No"),
			"  ",
			yesStyle.Render("Yes"),
		),
	}
	if m.inlineError != "" {
		lines = append(lines, "", tuiErrorStyle.Render(m.inlineError))
	}
	lines = append(lines, "", tuiHintStyle.Render("Left/Right or Y/N toggles. Enter continues. Shift+Tab goes back."))
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderReview() string {
	answers, _ := m.answers()
	lines := []string{
		m.renderProgress(),
		tuiTitleStyle.Render("Review and write"),
		tuiSubtitleStyle.Render("Confirm the workflow values, review live Linear checks, and write the file."),
		"",
		fmt.Sprintf("%s %s", tuiLabelStyle.Render("Workflow path:"), tuiReviewValueStyle.Render(m.workflowPath)),
		fmt.Sprintf("%s %s", tuiLabelStyle.Render("Linear project slug:"), tuiReviewValueStyle.Render(answers.ProjectSlug)),
		fmt.Sprintf("%s %s", tuiLabelStyle.Render("Repository URL:"), tuiReviewValueStyle.Render(answers.RepoURL)),
		fmt.Sprintf("%s %s", tuiLabelStyle.Render("Base branch:"), tuiReviewValueStyle.Render(answers.BaseRef)),
		fmt.Sprintf("%s %s", tuiLabelStyle.Render("Workspace root:"), tuiReviewValueStyle.Render(previewWorkspaceRoot(answers.WorkspaceRoot))),
		fmt.Sprintf("%s %d", tuiLabelStyle.Render("Server port:"), answers.ServerPort),
		fmt.Sprintf("%s %s", tuiLabelStyle.Render("Webhook guidance:"), tuiReviewValueStyle.Render(yesNo(answers.WantsWebhook))),
		"",
		tuiLabelStyle.Render("Preflight checks"),
	}

	if !m.hasLinearAPIKey() {
		lines = append(lines, tuiWarnStyle.Render("- Skipped: LINEAR_API_KEY is not available for this setup session."))
	} else if m.preflightRunning {
		lines = append(lines, tuiWarnStyle.Render("- Running live Linear checks..."))
	} else if !m.preflightAttempted {
		lines = append(lines, tuiWarnStyle.Render("- Waiting to run live Linear checks. Press r to retry manually."))
	} else {
		for _, check := range m.preflightReport.Checks {
			status := check.Status
			if status == "" {
				status = service.PreflightStatusSkipped
			}
			lines = append(lines, m.renderPreflightCheck(check.Label, status, check.Detail))
		}
		if m.preflightErr != nil {
			lines = append(lines, tuiErrorStyle.Render("- Overall result: "+m.preflightErr.Error()))
		}
	}

	if m.overwriteRequired {
		confirm := " "
		if m.overwriteConfirmed {
			confirm = "x"
		}
		lines = append(lines,
			"",
			tuiWarnStyle.Render("Workflow file already exists."),
			fmt.Sprintf("[%s] Confirm overwrite", confirm),
		)
	}
	if m.inlineError != "" {
		lines = append(lines, "", tuiErrorStyle.Render(m.inlineError))
	}
	if m.saving {
		lines = append(lines, "", tuiWarnStyle.Render("Writing workflow file..."))
	}
	lines = append(lines, "", tuiHintStyle.Render("Enter writes the file. r reruns checks. o toggles overwrite confirmation. Shift+Tab goes back."))
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderSuccess() string {
	lines := []string{
		tuiTitleStyle.Render("Workflow written"),
		tuiSuccessStyle.Render(completionText(m.result, m.opts.AutoStart)),
		"",
		tuiHintStyle.Render("Press Enter or q to exit."),
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderPreflightCheck(label, status, detail string) string {
	prefix := "[ ]"
	style := tuiHintStyle
	switch status {
	case service.PreflightStatusOK:
		prefix = "[ok]"
		style = tuiSuccessStyle
	case service.PreflightStatusError:
		prefix = "[error]"
		style = tuiErrorStyle
	case service.PreflightStatusSkipped:
		prefix = "[skip]"
		style = tuiWarnStyle
	}
	text := fmt.Sprintf("%s %s", prefix, label)
	if strings.TrimSpace(detail) != "" {
		text += ": " + strings.TrimSpace(detail)
	}
	return style.Render(text)
}

func (m tuiModel) renderProgress() string {
	steps := m.visibleSteps()
	currentIndex := 0
	for i, step := range steps {
		if step == m.step {
			currentIndex = i
			break
		}
	}
	var items []string
	for i := range steps {
		if i <= currentIndex {
			items = append(items, tuiProgressBarFilled)
		} else {
			items = append(items, tuiProgressBarEmpty)
		}
	}
	return strings.Join(items, " ") + "\n"
}

func (m tuiModel) textStepDetails() (string, string, string) {
	switch m.step {
	case tuiStepRepoURL:
		return "Repository URL", "Accepts SSH, HTTPS, or Git remote formats.", m.repoURL
	case tuiStepBaseRef:
		return "Base branch", "This is the branch Colin should branch and merge from.", m.baseRef
	case tuiStepWorkspaceRoot:
		return "Workspace root", "Colin will create per-issue workspaces under this directory.", m.workspaceRoot
	case tuiStepServerPort:
		return "Server port", "The local dashboard and setup UI port.", m.serverPort
	default:
		return "", "", ""
	}
}

func (m tuiModel) textStepPreview() string {
	switch m.step {
	case tuiStepWorkspaceRoot:
		if preview := previewWorkspaceRoot(m.workspaceRoot); preview != "" {
			return "Expanded path: " + preview
		}
	}
	return ""
}

func (m tuiModel) renderInput(value string) string {
	return m.renderInputWithPlaceholder(value, "Type here", false, 0)
}

func (m tuiModel) renderMaskedInput(value string, placeholder string) string {
	return m.renderInputWithPlaceholder(value, placeholder, true, 0)
}

func (m tuiModel) renderMaskedInputWidth(value string, placeholder string, width int) string {
	return m.renderInputWithPlaceholder(value, placeholder, true, width)
}

func (m tuiModel) renderInputWithPlaceholder(value string, placeholder string, masked bool, width int) string {
	runes := []rune(value)
	pos := minInt(m.cursorPos, len(runes))
	display := value
	if masked && len(runes) > 0 {
		display = strings.Repeat("•", len(runes))
		runes = []rune(display)
	}
	var rendered string
	if pos >= len(runes) {
		rendered = display + lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("█")
	} else {
		rendered = string(runes[:pos]) + lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(string(runes[pos])) + string(runes[pos+1:])
	}
	if value == "" {
		rendered = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(placeholder) + lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("█")
	}
	style := tuiFocusInputStyle
	if width > 0 {
		style = style.Copy().Width(width)
	}
	return style.Render(rendered)
}

func (m tuiModel) liveValidationError() string {
	switch m.step {
	case tuiStepLinearAPIKey:
		if strings.TrimSpace(m.linearAPIKey) == "" {
			return ""
		}
		return validateOptionalLinearAPIKey(m.linearAPIKey)
	case tuiStepGitHubToken:
		if strings.TrimSpace(m.githubToken) == "" {
			return ""
		}
		return validateOptionalGitHubToken(m.githubToken)
	default:
		return ""
	}
}

func (m tuiModel) enterProjectStepCmd() tea.Cmd {
	if !m.hasLinearAPIKey() || m.projectManualMode || m.projectLoading || len(m.projectOptions) > 0 || m.projectFetchAttempted {
		return nil
	}
	return m.fetchProjectsCmd()
}

func (m tuiModel) fetchProjectsCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		projects, err := listLinearProjects(ctx, "", strings.TrimSpace(m.linearAPIKey))
		return tuiProjectsMsg{projects: projects, err: err}
	}
}

func (m *tuiModel) insertProjectFilter(text string) {
	value := []rune(m.projectFilter)
	pos := minInt(m.projectFilterCursorPos, len(value))
	value = append(value[:pos], append([]rune(text), value[pos:]...)...)
	m.projectFilter = string(value)
	m.projectFilterCursorPos = pos + utf8.RuneCountInString(text)
	m.projectSelection = 0
}

func (m *tuiModel) deleteProjectFilterLeft() {
	value := []rune(m.projectFilter)
	if m.projectFilterCursorPos <= 0 || len(value) == 0 {
		return
	}
	pos := minInt(m.projectFilterCursorPos, len(value))
	value = append(value[:pos-1], value[pos:]...)
	m.projectFilter = string(value)
	m.projectFilterCursorPos = pos - 1
	m.projectSelection = 0
}

func (m *tuiModel) deleteProjectFilterRight() {
	value := []rune(m.projectFilter)
	pos := minInt(m.projectFilterCursorPos, len(value))
	if pos >= len(value) {
		return
	}
	value = append(value[:pos], value[pos+1:]...)
	m.projectFilter = string(value)
	m.projectSelection = 0
}

func (m tuiModel) filteredProjects() []linear.ProjectSummary {
	if strings.TrimSpace(m.projectFilter) == "" {
		return append([]linear.ProjectSummary(nil), m.projectOptions...)
	}
	filter := strings.ToLower(strings.TrimSpace(m.projectFilter))
	var filtered []linear.ProjectSummary
	for _, project := range m.projectOptions {
		name := strings.ToLower(project.Name)
		slug := strings.ToLower(project.Slug)
		if strings.Contains(name, filter) || strings.Contains(slug, filter) {
			filtered = append(filtered, project)
		}
	}
	return filtered
}

func (m tuiModel) renderProjectOption(index int, project linear.ProjectSummary, nameWidth int, slugWidth int) string {
	prefix := "  "
	style := tuiListStyle
	if index == m.projectSelection {
		prefix = "> "
		style = tuiSelectedListStyle
	}
	label := formatProjectOption(prefix, project, nameWidth, slugWidth)
	if len(project.TeamNames) > 0 {
		label += " - " + strings.Join(project.TeamNames, ", ")
	}
	return style.Render(label)
}

func projectColumnWidths(projects []linear.ProjectSummary) (int, int) {
	nameWidth := 0
	slugWidth := 0
	for _, project := range projects {
		nameWidth = maxInt(nameWidth, utf8.RuneCountInString(project.Name))
		slugWidth = maxInt(slugWidth, utf8.RuneCountInString(project.Slug))
	}
	return nameWidth, slugWidth
}

func formatProjectOption(prefix string, project linear.ProjectSummary, nameWidth int, slugWidth int) string {
	return fmt.Sprintf("%s%-*s [%-*s]", prefix, nameWidth, project.Name, slugWidth, project.Slug)
}

func (m tuiModel) renderProjectFilter() string {
	value := []rune(m.projectFilter)
	pos := minInt(m.projectFilterCursorPos, len(value))
	var rendered string
	if pos >= len(value) {
		rendered = m.projectFilter + lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("█")
	} else {
		rendered = string(value[:pos]) + lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(string(value[pos])) + string(value[pos+1:])
	}
	if m.projectFilter == "" {
		rendered = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Filter projects") + lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("█")
	}
	return rendered
}

func quitCmd() tea.Cmd {
	return func() tea.Msg {
		return tea.Quit()
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sanitizePastedText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "")
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, "\r", "")
	return value
}
