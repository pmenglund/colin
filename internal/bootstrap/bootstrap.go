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

	"github.com/pmenglund/colin/internal/githubauth"
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
	ProjectSlug   string
	RepoURL       string
	BaseRef       string
	WorkspaceRoot string
	ServerPort    int
	WantsWebhook  bool
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
	writeFile, err := promptBool(reader, out, fmt.Sprintf("Write workflow file to %s", resolved.workflowPath), true)
	if err != nil {
		return Result{}, err
	}
	if !writeFile {
		return Result{}, ErrAborted
	}

	answers := Answers{
		ProjectSlug:   projectSlug,
		RepoURL:       repoURL,
		BaseRef:       baseRef,
		WorkspaceRoot: workspaceRoot,
		ServerPort:    serverPort,
		WantsWebhook:  wantsWebhook,
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
}

func detectPrerequisites() Prerequisites {
	return Prerequisites{
		LinearAPIKeyConfigured: isValidLinearAPIKey(currentLinearAPIKey()),
		GitHubTokenConfigured:  isValidGitHubToken(currentGitHubToken()),
		GitAvailable:           commandExists("git"),
		CodexAvailable:         commandExists("codex"),
	}
}

func currentLinearAPIKey() string {
	return strings.TrimSpace(os.Getenv("LINEAR_API_KEY"))
}

func currentGitHubToken() string {
	return strings.TrimSpace(firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")))
}

func detectDefaults(workingDir string) detectedDefaults {
	return detectedDefaults{
		RepoURL:       gitOutput(workingDir, "config", "--get", "remote.origin.url"),
		BaseRef:       firstNonEmpty(gitDefaultBranch(workingDir), "main"),
		WorkspaceRoot: "./.colin/workspaces",
		ServerPort:    8888,
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
	fmt.Fprintln(out, "Prerequisite check:")
	fmt.Fprintf(out, "- LINEAR_API_KEY configured: %s\n", yesNo(prereqs.LinearAPIKeyConfigured))
	fmt.Fprintf(out, "- GITHUB_TOKEN or GH_TOKEN configured: %s\n", yesNo(prereqs.GitHubTokenConfigured))
	fmt.Fprintf(out, "- git available: %s\n", yesNo(prereqs.GitAvailable))
	fmt.Fprintf(out, "- codex available: %s\n", yesNo(prereqs.CodexAvailable))
	fmt.Fprintln(out)
}

func printCompletion(out io.Writer, result Result, autoStart bool) {
	fmt.Fprintln(out, completionText(result, autoStart))
}

func printGitHubSetupGuidance(out io.Writer, repoURL string) {
	details, err := githubSetupDetails(repoURL)
	if err != nil {
		fmt.Fprintln(out, "GitHub token setup:")
		fmt.Fprintln(out, "- Colin recommends exporting GITHUB_TOKEN before moving issues into `Review` or `Merge`.")
		fmt.Fprintln(out, "- Run `colin setup github` after configuration for the exact token settings.")
		fmt.Fprintln(out)
		return
	}
	fmt.Fprintln(out, githubauth.RenderInstructions(details, "colin setup github"))
	fmt.Fprintln(out)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
