package workflowfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultPath is the repository-owned workflow contract path.
	DefaultPath = "WORKFLOW.md"

	KindMissingFile        = "missing_workflow_file"
	KindParseError         = "workflow_parse_error"
	KindFrontMatterNotMap  = "workflow_front_matter_not_a_map"
	KindTemplateParseError = "template_parse_error"
	KindTemplateExecError  = "template_render_error"
)

// Error classifies workflow contract failures.
type Error struct {
	Kind string
	Path string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Path == "" {
		return fmt.Sprintf("%s: %v", e.Kind, e.Err)
	}
	return fmt.Sprintf("%s (%s): %v", e.Kind, e.Path, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Definition is the parsed repository workflow contract.
type Definition struct {
	Path           string
	Config         map[string]any
	PromptTemplate string
}

// PromptIssue is the template-visible normalized issue payload.
type PromptIssue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	ProjectID   string
	ProjectName string
	State       string
	Blocked     bool
	BlockedBy   []string
	Metadata    map[string]string
	URL         string
	BranchName  string
	Labels      []string
}

// PromptData is the full template context.
type PromptData struct {
	Issue             PromptIssue
	Attempt           *int
	LinearID          string
	LinearTitle       string
	LinearDescription string
	SourceBranch      string
	BaseBranch        string
	RemoteName        string
	WorktreePath      string
}

// Load reads and parses a workflow definition from disk.
func Load(path string) (Definition, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		target = DefaultPath
	}
	cleanPath := filepath.Clean(target)
	content, err := os.ReadFile(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Definition{}, &Error{Kind: KindMissingFile, Path: cleanPath, Err: err}
		}
		return Definition{}, &Error{Kind: KindParseError, Path: cleanPath, Err: err}
	}
	def, err := Parse(cleanPath, content)
	if err != nil {
		return Definition{}, err
	}
	return def, nil
}

// Parse decodes a workflow definition from markdown content.
func Parse(path string, content []byte) (Definition, error) {
	text := string(content)
	def := Definition{
		Path:   filepath.Clean(strings.TrimSpace(path)),
		Config: map[string]any{},
	}

	frontMatter, body, hasFrontMatter, err := splitFrontMatter(text)
	if err != nil {
		return Definition{}, &Error{Kind: KindParseError, Path: def.Path, Err: err}
	}
	if hasFrontMatter {
		var raw any
		if err := yaml.Unmarshal([]byte(frontMatter), &raw); err != nil {
			return Definition{}, &Error{Kind: KindParseError, Path: def.Path, Err: err}
		}
		typed, ok := normalizeMap(raw).(map[string]any)
		if !ok {
			return Definition{}, &Error{
				Kind: KindFrontMatterNotMap,
				Path: def.Path,
				Err:  fmt.Errorf("front matter root must be a map"),
			}
		}
		def.Config = typed
	}
	def.PromptTemplate = strings.TrimSpace(body)
	return def, nil
}

// RenderPrompt renders a prompt template with strict missing-key semantics.
func RenderPrompt(promptTemplate string, data PromptData) (string, error) {
	funcs := template.FuncMap{
		"LINEAR_ID":          func() string { return data.LinearID },
		"LINEAR_TITLE":       func() string { return data.LinearTitle },
		"LINEAR_DESCRIPTION": func() string { return data.LinearDescription },
		"SOURCE_BRANCH":      func() string { return data.SourceBranch },
		"BASE_BRANCH":        func() string { return data.BaseBranch },
		"REMOTE_NAME":        func() string { return data.RemoteName },
		"WORKTREE_PATH":      func() string { return data.WorktreePath },
	}

	tmpl, err := template.New("workflow-prompt").Option("missingkey=error").Funcs(funcs).Parse(promptTemplate)
	if err != nil {
		return "", &Error{Kind: KindTemplateParseError, Err: err}
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		return "", &Error{Kind: KindTemplateExecError, Err: err}
	}
	return strings.TrimSpace(rendered.String()), nil
}

func splitFrontMatter(text string) (frontMatter string, body string, hasFrontMatter bool, err error) {
	trimmedLeft := strings.TrimLeft(text, "\r\n\t ")
	if !strings.HasPrefix(trimmedLeft, "---\n") && !strings.HasPrefix(trimmedLeft, "---\r\n") {
		return "", text, false, nil
	}

	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false, fmt.Errorf("workflow front matter must start on the first line")
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", false, fmt.Errorf("workflow front matter is missing a closing delimiter")
	}

	return strings.Join(lines[1:end], "\n"), strings.Join(lines[end+1:], "\n"), true, nil
}

func normalizeMap(raw any) any {
	switch typed := raw.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = normalizeMap(value)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[fmt.Sprint(key)] = normalizeMap(value)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, value := range typed {
			out[i] = normalizeMap(value)
		}
		return out
	default:
		return raw
	}
}
