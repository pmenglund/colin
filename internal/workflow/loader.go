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

const (
	DefaultPath        = "WORKFLOW.md"
	WorkflowPathEnvVar = "COLIN_WORKFLOW"
)

// Loader resolves and parses workflow files from disk.
type Loader struct{}

// ResolvePath chooses the explicit workflow path when provided, otherwise
// falls back to $COLIN_WORKFLOW, then to ./WORKFLOW.md.
func (Loader) ResolvePath(explicit string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv(WorkflowPathEnvVar)); value != "" {
		return value
	}
	return filepath.Join(".", DefaultPath)
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
		"id":                    issue.ID,
		"identifier":            issue.Identifier,
		"title":                 issue.Title,
		"description":           derefString(issue.Description),
		"priority":              derefInt(issue.Priority),
		"team_id":               issue.TeamID,
		"project_id":            issue.ProjectID,
		"project_slug":          issue.ProjectSlug,
		"state":                 issue.State,
		"branch_name":           derefString(issue.BranchName),
		"url":                   derefString(issue.URL),
		"labels":                cloneStrings(issue.Labels),
		"colin_metadata":        colinMetadataToMap(issue.ColinMetadata),
		"exec_plan":             execPlanToMap(issue.ExecPlan),
		"blocked_by":            blockersToMaps(issue.BlockedBy),
		"review_cycle":          reviewCycleToMap(issue.ReviewCycle),
		"review_feedback":       reviewFeedbackToMaps(issue.ReviewFeedback),
		"review_threads":        reviewThreadsToMaps(issue.ReviewThreads),
		"pull_request":          pullRequestToMap(issue.PullRequest),
		"pending_check_failure": pendingCheckFailureToMap(pendingCheckFailure(issue.ColinMetadata)),
		"created_at":            derefTime(issue.CreatedAt),
		"updated_at":            derefTime(issue.UpdatedAt),
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

func reviewThreadsToMaps(items []domain.ReviewThread) []map[string]any {
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
		"number":           value.Number,
		"url":              value.URL,
		"state":            value.State,
		"head_ref":         value.HeadRef,
		"base_ref":         value.BaseRef,
		"backend":          value.Backend,
		"owner":            value.RepositoryOwner,
		"repository":       value.RepositoryName,
		"repository_owner": value.RepositoryOwner,
		"repository_name":  value.RepositoryName,
	}
}

func pendingCheckFailure(metadata *domain.ColinMetadata) *domain.PendingPullRequestCheckFailure {
	if metadata == nil {
		return nil
	}
	return metadata.PendingCheckFailure
}

func pendingCheckFailureToMap(value *domain.PendingPullRequestCheckFailure) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"name":         value.Name,
		"failure_kind": value.FailureKind,
		"status":       value.Status,
		"conclusion":   value.Conclusion,
		"details_url":  value.DetailsURL,
		"summary":      value.Summary,
		"head_sha":     value.HeadSHA,
		"pr_number":    value.PRNumber,
		"pr_url":       value.PRURL,
		"observed_at":  derefTime(value.ObservedAt),
	}
}

func colinMetadataToMap(value *domain.ColinMetadata) any {
	if value == nil {
		return nil
	}
	return map[string]any{
		"attachment_id":               value.AttachmentID,
		"url":                         value.URL,
		"delegation_ack_kind":         value.DelegationAckKind,
		"delegation_ack_state":        value.DelegationAckState,
		"delegation_ack_session_id":   value.DelegationAckSessionID,
		"actual_branch_name":          value.ActualBranchName,
		"pending_review_comment_id":   value.PendingReviewCommentID,
		"pending_review_thread_id":    value.PendingReviewThreadID,
		"pending_review_reaction_id":  value.PendingReviewReactionID,
		"pending_review_reactor":      value.PendingReviewReactor,
		"pending_review_requested_at": derefTime(value.PendingReviewRequestedAt),
		"queued_review_follow_ups":    pendingReviewFollowUpsToMaps(value.QueuedReviewFollowUps),
		"pending_check_failure":       pendingCheckFailureToMap(value.PendingCheckFailure),
		"review_reaction_watermarks":  value.ReviewReactionWatermarks,
		"review_feedback_issue_links": reviewFeedbackIssueLinksToMaps(value.ReviewFeedbackIssueLinks),
		"exec_plan_decision":          value.ExecPlanDecision,
		"review_publish_directive":    value.ReviewPublishDirective,
		"last_run_type":               value.LastRunType,
		"last_outcome":                value.LastOutcome,
		"last_summary":                value.LastSummary,
		"last_summary_comment_id":     value.LastSummaryCommentID,
		"pull_request_number":         value.PullRequestNumber,
		"pull_request_url":            value.PullRequestURL,
		"pull_request_state":          value.PullRequestState,
		"pull_request_head_ref":       value.PullRequestHeadRef,
		"pull_request_base_ref":       value.PullRequestBaseRef,
		"pull_request_backend":        value.PullRequestBackend,
		"pull_request_repo_owner":     value.PullRequestRepoOwner,
		"pull_request_repo_name":      value.PullRequestRepoName,
		"last_check_head_sha":         value.LastCheckHeadSHA,
		"last_check_state":            value.LastCheckState,
		"slack_channel_id":            value.SlackChannelID,
		"slack_message_ts":            value.SlackMessageTS,
		"slack_permalink":             value.SlackPermalink,
		"slack_summary_fingerprint":   value.SlackSummaryFingerprint,
		"updated_at":                  derefTime(value.UpdatedAt),
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

func pendingReviewFollowUpsToMaps(values []domain.PendingReviewFollowUp) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		out = append(out, map[string]any{
			"thread_id":    value.ThreadID,
			"comment_id":   value.CommentID,
			"reaction_id":  value.ReactionID,
			"reactor":      value.Reactor,
			"requested_at": derefTime(value.RequestedAt),
		})
	}
	return out
}

func reviewFeedbackIssueLinksToMaps(values []domain.ReviewFeedbackIssueLink) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		out = append(out, map[string]any{
			"thread_id":        value.ThreadID,
			"comment_id":       value.CommentID,
			"reaction_id":      value.ReactionID,
			"reactor":          value.Reactor,
			"issue_id":         value.IssueID,
			"issue_identifier": value.IssueIdentifier,
			"issue_url":        value.IssueURL,
			"requested_at":     derefTime(value.RequestedAt),
			"created_at":       derefTime(value.CreatedAt),
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
