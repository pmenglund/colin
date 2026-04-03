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

// Parse reads and parses workflow content without loading it from disk.
func Parse(path string, data []byte) (domain.WorkflowDefinition, error) {
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
	return RenderTemplate(text, map[string]any{
		"issue":   issueToMap(issue),
		"attempt": attempt,
	})
}

// RenderTemplate renders arbitrary workflow-configured text with strict missing-key behavior.
func RenderTemplate(text string, payload map[string]any) (string, error) {
	tpl, err := template.New("workflow").
		Option("missingkey=error").
		Parse(text)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTemplateParseError, err)
	}
	var buffer bytes.Buffer
	if err := tpl.Execute(&buffer, payload); err != nil {
		return "", fmt.Errorf("%w: %v", ErrTemplateRenderError, err)
	}
	return strings.TrimSpace(buffer.String()), nil
}

func parseWorkflow(data []byte) (domain.WorkflowConfig, string, error) {
	source := string(data)
	if !strings.HasPrefix(source, "---") {
		return domain.WorkflowConfig{}, strings.TrimSpace(source), nil
	}
	lines := strings.Split(source, "\n")
	if len(lines) < 3 {
		return domain.WorkflowConfig{}, "", fmt.Errorf("%w: unterminated front matter", ErrWorkflowParseError)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return domain.WorkflowConfig{}, "", fmt.Errorf("%w: unterminated front matter", ErrWorkflowParseError)
	}
	frontMatter := strings.Join(lines[1:end], "\n")
	body := strings.Join(lines[end+1:], "\n")

	decoder := yaml.NewDecoder(strings.NewReader(frontMatter))
	decoder.KnownFields(true)

	var decoded domain.WorkflowConfig
	if err := decoder.Decode(&decoded); err != nil {
		return domain.WorkflowConfig{}, "", fmt.Errorf("%w: %v", ErrWorkflowParseError, err)
	}
	return decoded, strings.TrimSpace(body), nil
}

func issueToMap(issue domain.Issue) map[string]any {
	return map[string]any{
		"id":              issue.ID,
		"identifier":      issue.Identifier,
		"title":           issue.Title,
		"description":     derefString(issue.Description),
		"priority":        derefInt(issue.Priority),
		"project_id":      issue.ProjectID,
		"project_slug":    issue.ProjectSlug,
		"state":           issue.State,
		"branch_name":     derefString(issue.BranchName),
		"url":             derefString(issue.URL),
		"labels":          cloneStrings(issue.Labels),
		"colin_metadata":  colinMetadataToMap(issue.ColinMetadata),
		"exec_plan":       execPlanToMap(issue.ExecPlan),
		"blocked_by":      blockersToMaps(issue.BlockedBy),
		"review_cycle":    reviewCycleToMap(issue.ReviewCycle),
		"review_feedback": reviewFeedbackToMaps(issue.ReviewFeedback),
		"review_threads":  reviewThreadsToMaps(issue.ReviewThreads),
		"pull_request":    pullRequestToMap(issue.PullRequest),
		"created_at":      derefTime(issue.CreatedAt),
		"updated_at":      derefTime(issue.UpdatedAt),
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

func reviewFeedbackToMaps(items []domain.ReviewFeedback) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"body":       item.Body,
			"created_at": item.CreatedAt.Format(time.RFC3339),
			"parent_id":  derefString(item.ParentID),
		})
	}
	return out
}

func reviewCycleToMap(value *domain.ReviewCycle) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"entered_review_at":   value.EnteredReviewAt.Format(time.RFC3339),
		"returned_to_todo_at": value.ReturnedToTodoAt.Format(time.RFC3339),
	}
}

func reviewThreadsToMaps(items []domain.GitHubReviewThread) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"id":          item.ID,
			"path":        item.Path,
			"line":        derefInt(item.Line),
			"start_line":  derefInt(item.StartLine),
			"comment_id":  item.CommentID,
			"comment_url": item.CommentURL,
			"author":      item.Author,
			"body":        item.Body,
			"created_at":  derefTime(item.CreatedAt),
			"is_resolved": item.IsResolved,
			"is_outdated": item.IsOutdated,
			"can_reply":   item.CanReply,
			"can_resolve": item.CanResolve,
		})
	}
	return out
}

func pullRequestToMap(value *domain.PullRequestRef) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"number":   value.Number,
		"url":      value.URL,
		"state":    value.State,
		"head_ref": value.HeadRef,
		"base_ref": value.BaseRef,
	}
}

func colinMetadataToMap(value *domain.ColinMetadata) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"attachment_id":             value.AttachmentID,
		"url":                       value.URL,
		"actual_branch_name":        value.ActualBranchName,
		"exec_plan_decision":        value.ExecPlanDecision,
		"review_publish_directive":  value.ReviewPublishDirective,
		"last_run_type":             value.LastRunType,
		"last_outcome":              value.LastOutcome,
		"last_summary_comment_id":   value.LastSummaryCommentID,
		"pull_request_number":       value.PullRequestNumber,
		"pull_request_url":          value.PullRequestURL,
		"pull_request_state":        value.PullRequestState,
		"pull_request_head_ref":     value.PullRequestHeadRef,
		"pull_request_base_ref":     value.PullRequestBaseRef,
		"slack_channel_id":          value.SlackChannelID,
		"slack_message_ts":          value.SlackMessageTS,
		"slack_permalink":           value.SlackPermalink,
		"slack_summary_fingerprint": value.SlackSummaryFingerprint,
		"updated_at":                derefTime(value.UpdatedAt),
	}
}

func execPlanToMap(value *domain.ExecPlan) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"attachment_id": value.AttachmentID,
		"url":           value.URL,
		"body":          value.Body,
		"updated_at":    derefTime(value.UpdatedAt),
	}
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
