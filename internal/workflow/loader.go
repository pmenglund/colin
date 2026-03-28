package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"gopkg.in/yaml.v3"
)

var (
	ErrMissingWorkflowFile       = errors.New("missing_workflow_file")
	ErrWorkflowParseError        = errors.New("workflow_parse_error")
	ErrWorkflowFrontMatterNotMap = errors.New("workflow_front_matter_not_a_map")
	ErrTemplateParseError        = errors.New("template_parse_error")
	ErrTemplateRenderError       = errors.New("template_render_error")
)

// Loader resolves and parses workflow files from disk.
type Loader struct{}

// ResolvePath chooses the explicit workflow path when provided or defaults to ./WORKFLOW.md.
func (Loader) ResolvePath(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	return filepath.Join(".", "WORKFLOW.md")
}

// Load reads and parses a workflow file from disk.
func (Loader) Load(path string) (domain.WorkflowDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return domain.WorkflowDefinition{}, ErrMissingWorkflowFile
		}
		return domain.WorkflowDefinition{}, fmt.Errorf("%w: %v", ErrMissingWorkflowFile, err)
	}
	config, body, err := parseWorkflow(data)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	return domain.WorkflowDefinition{
		Config:         config,
		PromptTemplate: body,
		SourcePath:     path,
	}, nil
}

// RenderPrompt renders the workflow prompt with strict missing-key behavior.
func RenderPrompt(def domain.WorkflowDefinition, issue domain.Issue, attempt *int) (string, error) {
	text := strings.TrimSpace(def.PromptTemplate)
	if text == "" {
		text = "You are working on an issue from Linear."
	}
	tpl, err := template.New("workflow").
		Option("missingkey=error").
		Parse(text)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTemplateParseError, err)
	}
	var buffer bytes.Buffer
	payload := map[string]any{
		"issue":   issueToMap(issue),
		"attempt": attempt,
	}
	if err := tpl.Execute(&buffer, payload); err != nil {
		return "", fmt.Errorf("%w: %v", ErrTemplateRenderError, err)
	}
	return strings.TrimSpace(buffer.String()), nil
}

func parseWorkflow(data []byte) (map[string]any, string, error) {
	source := string(data)
	if !strings.HasPrefix(source, "---") {
		return map[string]any{}, strings.TrimSpace(source), nil
	}
	lines := strings.Split(source, "\n")
	if len(lines) < 3 {
		return nil, "", fmt.Errorf("%w: unterminated front matter", ErrWorkflowParseError)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return nil, "", fmt.Errorf("%w: unterminated front matter", ErrWorkflowParseError)
	}
	frontMatter := strings.Join(lines[1:end], "\n")
	body := strings.Join(lines[end+1:], "\n")

	var decoded any
	if err := yaml.Unmarshal([]byte(frontMatter), &decoded); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrWorkflowParseError, err)
	}
	if decoded == nil {
		return map[string]any{}, strings.TrimSpace(body), nil
	}
	root, ok := convertMap(decoded).(map[string]any)
	if !ok {
		return nil, "", ErrWorkflowFrontMatterNotMap
	}
	return root, strings.TrimSpace(body), nil
}

func convertMap(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = convertMap(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[fmt.Sprint(key)] = convertMap(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = convertMap(item)
		}
		return out
	default:
		return v
	}
}

func issueToMap(issue domain.Issue) map[string]any {
	return map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": derefString(issue.Description),
		"priority":    derefInt(issue.Priority),
		"state":       issue.State,
		"branch_name": derefString(issue.BranchName),
		"url":         derefString(issue.URL),
		"labels":      cloneStrings(issue.Labels),
		"blocked_by":  blockersToMaps(issue.BlockedBy),
		"created_at":  derefTime(issue.CreatedAt),
		"updated_at":  derefTime(issue.UpdatedAt),
	}
}

func blockersToMaps(blockers []domain.BlockerRef) []map[string]any {
	out := make([]map[string]any, 0, len(blockers))
	for _, blocker := range blockers {
		out = append(out, map[string]any{
			"id":         derefString(blocker.ID),
			"identifier": derefString(blocker.Identifier),
			"state":      derefString(blocker.State),
		})
	}
	return out
}

func cloneStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func derefString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func derefInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func derefTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format(time.RFC3339)
}
