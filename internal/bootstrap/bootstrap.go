package bootstrap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pmenglund/colin/internal/clioutput"
	"github.com/pmenglund/colin/internal/githubauth"
	"github.com/pmenglund/colin/internal/repohost"
)

var ErrAborted = errors.New("bootstrap_aborted")

// Options configures one interactive first-run setup session.
type Options struct {
	WorkflowPath string
	WorkingDir   string
	AutoStart    bool
}

// Answers captures the operator choices used to render WORKFLOW.md.
type Answers struct {
	Backend       string
	ProjectSlug   string
	RepoURL       string
	BaseRef       string
	WorkspaceRoot string
	ServerPort    int
	WantsWebhook  bool
	WebhookPort   int
}

// Prerequisites summarizes the local environment checks shown during setup.
type Prerequisites struct {
	LinearAPIKeyConfigured bool
	GitHubTokenConfigured  bool
	GitAvailable           bool
	CodexAvailable         bool
}

// Result is the operator-facing outcome of one onboarding run.
type Result struct {
	WorkflowPath              string
	WroteWorkflow             bool
	Answers                   Answers
	Prereqs                   Prerequisites
	SessionLinearAPIKeyLoaded bool
	SessionGitHubTokenLoaded  bool
}

// Run collects onboarding answers, writes WORKFLOW.md, and reports next steps.
func Run(in io.Reader, out io.Writer, opts Options) (Result, error) {
	resolved, err := resolveOptions(opts)
	if err != nil {
		return Result{}, err
	}
	reader := bufio.NewReader(in)

	fmt.Fprintln(out, "Colin configuration")
	fmt.Fprintln(out)
	printPrerequisiteSummary(out, resolved.prereqs)

	projectSlug, err := promptRequiredString(reader, out, "Linear project slug", resolved.defaults.ProjectSlug)
	if err != nil {
		return Result{}, err
	}
	repoURL, err := promptRequiredString(reader, out, "Repository URL", resolved.defaults.RepoURL)
	if err != nil {
		return Result{}, err
	}
	if !resolved.prereqs.GitHubTokenConfigured {
		printGitHubSetupGuidance(out, repoURL)
	}
	baseRef, err := promptRequiredString(reader, out, "Base branch", resolved.defaults.BaseRef)
	if err != nil {
		return Result{}, err
	}
	workspaceRoot, err := promptRequiredString(reader, out, "Workspace root", resolved.defaults.WorkspaceRoot)
	if err != nil {
		return Result{}, err
	}
	serverPort, err := promptRequiredInt(reader, out, "Server port", resolved.defaults.ServerPort)
	if err != nil {
		return Result{}, err
	}
	wantsWebhook, err := promptBool(reader, out, "Set up a webhook after configuration? This requires Tailscale", false)
	if err != nil {
		return Result{}, err
	}
	webhookPort := 0
	if wantsWebhook {
		webhookPort, err = promptRequiredInt(reader, out, "Webhook port", resolved.defaults.WebhookPort)
		if err != nil {
			return Result{}, err
		}
	}
	writeFile, err := promptBool(reader, out, fmt.Sprintf("Write workflow file to %s", resolved.workflowPath), true)
	if err != nil {
		return Result{}, err
	}
	if !writeFile {
		return Result{}, ErrAborted
	}

	answers := Answers{
		Backend:       string(repohost.HostKindGitHub),
		ProjectSlug:   projectSlug,
		RepoURL:       repoURL,
		BaseRef:       baseRef,
		WorkspaceRoot: workspaceRoot,
		ServerPort:    serverPort,
		WantsWebhook:  wantsWebhook,
		WebhookPort:   webhookPort,
	}

	overwrite, err := confirmOverwrite(reader, out, resolved.workflowPath)
	if err != nil {
		return Result{}, err
	}
	content, err := RenderWorkflow(answers)
	if err != nil {
		return Result{}, err
	}
	if err := WriteWorkflow(resolved.workflowPath, content, overwrite); err != nil {
		return Result{}, err
	}

	result := resultFromAnswers(resolved.workflowPath, answers, resolved.prereqs)
	if err := applySessionLinearAPIKey(resolved.linearAPIKey); err != nil {
		return Result{}, err
	}
	if err := applySessionGitHubToken(resolved.githubToken); err != nil {
		return Result{}, err
	}
	result.SessionLinearAPIKeyLoaded = isValidLinearAPIKey(resolved.linearAPIKey) && !resolved.prereqs.LinearAPIKeyConfigured
	result.SessionGitHubTokenLoaded = isValidGitHubToken(resolved.githubToken) && !resolved.prereqs.GitHubTokenConfigured
	printCompletion(out, result, opts.AutoStart)
	return result, nil
}

// WriteWorkflow writes the rendered workflow file to disk.
func WriteWorkflow(path string, content string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%w: %s already exists", ErrAborted, path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat workflow path: %w", err)
		}
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create workflow directory: %w", err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write workflow file: %w", err)
	}
	return nil
}

type detectedDefaults struct {
	ProjectSlug   string
	RepoURL       string
	BaseRef       string
	WorkspaceRoot string
	ServerPort    int
	WebhookPort   int
}

func detectPrerequisites() Prerequisites {
	return Prerequisites{
		LinearAPIKeyConfigured: isValidLinearAPIKey(currentLinearAPIKey()),
		GitHubTokenConfigured:  isValidGitHubToken(githubauth.CurrentToken()),
		GitAvailable:           commandExists("git"),
		CodexAvailable:         commandExists("codex"),
	}
}

func currentLinearAPIKey() string {
	return strings.TrimSpace(os.Getenv("LINEAR_API_KEY"))
}

func detectDefaults(workingDir string) detectedDefaults {
	return detectedDefaults{
		RepoURL:       gitOutput(workingDir, "config", "--get", "remote.origin.url"),
		BaseRef:       firstNonEmpty(gitDefaultBranch(workingDir), "main"),
		WorkspaceRoot: "./.colin/workspaces",
		ServerPort:    8888,
		WebhookPort:   8998,
	}
}

func gitDefaultBranch(workingDir string) string {
	value := gitOutput(workingDir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	value = strings.TrimPrefix(value, "origin/")
	return strings.TrimSpace(value)
}

func gitOutput(workingDir string, args ...string) string {
	if !commandExists("git") {
		return ""
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = workingDir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func printPrerequisiteSummary(out io.Writer, prereqs Prerequisites) {
	renderer := newBootstrapRenderer(out)
	renderer.Section("Prerequisites")
	if prereqs.LinearAPIKeyConfigured {
		renderer.Status(clioutput.StatusOK, "LINEAR_API_KEY", "configured")
	} else {
		renderer.Status(clioutput.StatusAction, "LINEAR_API_KEY", "export it before running Colin")
	}
	if prereqs.GitHubTokenConfigured {
		renderer.Status(clioutput.StatusOK, "GITHUB_TOKEN or GH_TOKEN", "configured")
	} else {
		renderer.Status(clioutput.StatusAction, "GITHUB_TOKEN or GH_TOKEN", "export it before moving issues into `Review` or `Merge`")
	}
	if prereqs.GitAvailable {
		renderer.Status(clioutput.StatusOK, "git", "available")
	} else {
		renderer.Status(clioutput.StatusWarn, "git", "not found in PATH")
	}
	if prereqs.CodexAvailable {
		renderer.Status(clioutput.StatusOK, "codex", "available")
	} else {
		renderer.Status(clioutput.StatusWarn, "codex", "not found in PATH")
	}
	renderer.Line("")
}

func printCompletion(out io.Writer, result Result, autoStart bool) {
	renderCompletion(out, result, autoStart)
}

func printGitHubSetupGuidance(out io.Writer, repoURL string) {
	details, err := githubSetupDetails(repoURL)
	if err != nil {
		renderer := newBootstrapRenderer(out)
		renderer.Section("GitHub token setup")
		renderer.Status(clioutput.StatusAction, "", "Colin recommends exporting GITHUB_TOKEN before moving issues into `Review` or `Merge`")
		renderer.Status(clioutput.StatusAction, "", "Run `colin setup repo` after configuration for the exact token settings")
		renderer.Line("")
		return
	}
	renderInstructionSection(out, "GitHub token setup", githubauth.RenderInstructions(details, "colin setup repo"))
	renderer := newBootstrapRenderer(out)
	renderer.Line("")
}

func githubSetupDetails(repoURL string) (githubauth.SetupDetails, error) {
	repo, err := githubauth.ParseRepositoryURL(repoURL)
	if err != nil {
		return githubauth.SetupDetails{}, err
	}
	return githubauth.BuildSetupDetails(repo), nil
}

func confirmOverwrite(reader *bufio.Reader, out io.Writer, workflowPath string) (bool, error) {
	if _, err := os.Stat(workflowPath); errors.Is(err, os.ErrNotExist) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("stat workflow path: %w", err)
	}
	return promptBool(reader, out, fmt.Sprintf("%s already exists. Overwrite it", workflowPath), false)
}

func promptRequiredString(reader *bufio.Reader, out io.Writer, label string, defaultValue string) (string, error) {
	for {
		value, err := promptString(reader, out, label, defaultValue)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) == "" {
			fmt.Fprintf(out, "%s is required.\n", label)
			continue
		}
		return value, nil
	}
}

func promptRequiredInt(reader *bufio.Reader, out io.Writer, label string, defaultValue int) (int, error) {
	for {
		text, err := promptString(reader, out, label, strconv.Itoa(defaultValue))
		if err != nil {
			return 0, err
		}
		value, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || value <= 0 {
			fmt.Fprintf(out, "%s must be a positive integer.\n", label)
			continue
		}
		return value, nil
	}
}

func promptBool(reader *bufio.Reader, out io.Writer, label string, defaultYes bool) (bool, error) {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	for {
		fmt.Fprintf(out, "%s %s: ", label, suffix)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("read %s: %w", label, err)
		}
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "" {
			return defaultYes, nil
		}
		switch line {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintf(out, "Please answer yes or no.\n")
		}
		if errors.Is(err, io.EOF) {
			return false, io.EOF
		}
	}
}

func promptString(reader *bufio.Reader, out io.Writer, label string, defaultValue string) (string, error) {
	if strings.TrimSpace(defaultValue) != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read %s: %w", label, err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = defaultValue
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", io.EOF
	}
	return line, nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func renderCompletion(out io.Writer, result Result, autoStart bool) {
	renderer := newBootstrapRenderer(out)
	renderer.Section("Overview")
	renderer.Item("Workflow file", result.WorkflowPath)

	renderer.Section("Checks")
	if result.SessionLinearAPIKeyLoaded {
		renderer.Status(clioutput.StatusAction, "LINEAR_API_KEY", "loaded for this setup session only; export it in your shell before the next Colin run")
	} else if result.Prereqs.LinearAPIKeyConfigured {
		renderer.Status(clioutput.StatusOK, "LINEAR_API_KEY", "already configured in this shell")
	} else {
		renderer.Status(clioutput.StatusAction, "LINEAR_API_KEY", "export it in your shell before running Colin")
	}
	if result.SessionGitHubTokenLoaded {
		renderer.Status(clioutput.StatusAction, "GITHUB_TOKEN", "loaded for this setup session only; export it in your shell before the next Colin run")
	} else if result.Prereqs.GitHubTokenConfigured {
		renderer.Status(clioutput.StatusOK, "GITHUB_TOKEN or GH_TOKEN", "already configured in this shell")
	} else {
		renderer.Status(clioutput.StatusAction, "GITHUB_TOKEN", "export it before moving issues into `Review` or `Merge`. Run `colin setup repo` for the exact token settings")
	}
	if !result.Prereqs.CodexAvailable {
		renderer.Status(clioutput.StatusWarn, "codex", "not found in PATH. Install or expose Codex before expecting Colin to launch coding runs")
	}
	if !result.Prereqs.GitAvailable {
		renderer.Status(clioutput.StatusWarn, "git", "not found in PATH. Colin needs git for workspace preparation")
	}

	if result.Answers.WantsWebhook {
		renderer.Section("Next steps")
		if autoStart {
			renderer.Status(clioutput.StatusAction, "Webhooks", "Webhook setup requires Tailscale. Once the setup URL is printed, open it and then run `colin setup linear webhook` after Funnel is ready")
		} else {
			renderer.Status(clioutput.StatusAction, "Webhooks", "Webhook setup requires Tailscale. After Colin is running, use `colin setup tailscale` and then `colin setup linear webhook`, or open `/setup/funnel` from the local UI")
		}
		return
	}

	renderer.Section("Notes")
	renderer.Status(clioutput.StatusInfo, "Webhooks", "setup skipped")
}

func renderInstructionSection(out io.Writer, title string, instructions string) {
	renderer := newBootstrapRenderer(out)
	renderer.Section(title)
	for _, line := range strings.Split(strings.TrimSpace(instructions), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasSuffix(trimmed, "setup:") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			renderer.Status(clioutput.StatusAction, "", strings.TrimPrefix(trimmed, "- "))
			continue
		}
		renderer.Line(trimmed)
	}
}

func newBootstrapRenderer(out io.Writer) *clioutput.Renderer {
	return clioutput.New(out, bootstrapIsTerminalStream(out))
}

func bootstrapIsTerminalStream(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
