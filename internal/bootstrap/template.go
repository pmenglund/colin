package bootstrap

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

const defaultWorkflowTemplate = `---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  # Optional once you create a Linear webhook with ` + "`colin setup linear webhook`" + `.
  webhook_signing_secret: $LINEAR_WEBHOOK_SECRET
  project_slug: {{yaml .ProjectSlug}}
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
    - Merged
    - Closed
    - Cancelled
    - Canceled
    - Duplicate

polling:
  interval_ms: 30000

workspace:
  root: {{yaml .WorkspaceRoot}}
  repo_url: {{yaml .RepoURL}}
  base_ref: {{yaml .BaseRef}}

repo:
  backend: {{yaml .Backend}}
  api_token: $GITHUB_TOKEN
  publish_states:
    - Review
  merge_states:
    - Merge
  # When true, Colin waits in Merge for Codex PR review to start before merging.
  codex_pr_reviews_enabled: false
  remote_name: origin
  merge_method: squash
  branch_template: colin/{{"{{.issue.title}}"}}

hooks:
  timeout_ms: 60000

agent:
  max_concurrent_agents: 4
  max_turns: 8
  max_retry_backoff_ms: 300000
  create_exec_plan: true

codex:
  command: codex app-server
  approval_policy: never
  thread_sandbox: danger-full-access
  turn_sandbox_policy:
    type: dangerFullAccess
  turn_timeout_ms: 3600000
  read_timeout_ms: 5000
  stall_timeout_ms: 300000

server:
  port: {{.ServerPort}}
{{- if .WantsWebhook}}
  webhook_port: {{.WebhookPort}}
{{- end}}
  # Optional when Colin metadata links should use a stable external UI origin.
  # ui_url: https://colin.example.com

# Optional: enable Slack issue summaries for tracked issues.
# slack:
#   bot_token: $SLACK_BOT_TOKEN
#   channel_id: C0123456789
---

You are working on Linear issue {{"{{.issue.identifier}}"}}: {{"{{.issue.title}}"}}.

Repository rules:
- Follow ` + "`AGENTS.md`" + `.
- Make changes only in this repository workspace.
- Prefer the smallest correct change that resolves the issue.
- Run relevant Go tests before you finish.

Issue context:
- State: {{"{{.issue.state}}"}} 
{{"{{- if .issue.url }}"}}
- URL: {{"{{.issue.url}}"}}
{{"{{- end }}"}}
{{"{{- if .issue.labels }}"}}
- Labels:
{{"{{- range .issue.labels }}"}}
  - {{"{{ . }}"}}
{{"{{- end }}"}}
{{"{{- end }}"}}

{{"{{- if .issue.description }}"}}
Issue description:

{{"{{.issue.description}}"}}
{{"{{- end }}"}}

{{"{{- if .issue.review_feedback }}"}}
Review feedback:
{{"{{- range .issue.review_feedback }}"}}
- {{"{{ .body }}"}}
{{"{{- end }}"}}

{{"{{- end }}"}}

{{"{{- if .issue.review_threads }}"}}
GitHub review threads:
{{"{{- range .issue.review_threads }}"}}
- {{"{{ .path }}"}}{{"{{- if .line }}:{{ .line }}{{ end }}"}} by {{"{{ .author }}"}}: {{"{{ .body }}"}}
  {{"{{- if .comment_url }} ({{ .comment_url }}){{ end }}"}}
{{"{{- end }}"}}

{{"{{- end }}"}}

{{"{{- if .attempt }}"}}
This is continuation or retry attempt {{"{{.attempt}}"}}. Reuse the existing workspace state and continue from prior progress rather than restarting from scratch.
{{"{{- end }}"}}

Definition of done:
- Implement the requested change.
- Add or update tests when behavior changes.
- Leave the repo in a clean, reviewable state ready for ` + "`Review`" + `.
- When Colin provides an ExecPlan working-copy path, keep that file updated as the living plan while you work.
- For ExecPlan-backed issues, do not treat the work as done until every checkbox under the ExecPlan's ` + "`## Progress`" + ` section is complete.
- Do not merge changes yourself during coding turns; Colin will publish in ` + "`Review`" + ` and merge in ` + "`Merge`" + `.
- Summarize what changed in a way that makes the result easy to verify: describe the before and after, what was tested, and any remaining risk.

Output contract:
- If the issue is still too underspecified to implement safely, begin your final response with ` + "`COLIN_OUTCOME: NEEDS_SPEC`" + `.
- After ` + "`COLIN_OUTCOME: NEEDS_SPEC`" + `, explain what information is missing and include the exact sentence ` + "`The spec should be improved before implementation.`" + `
- For ExecPlan-backed issues, also use ` + "`COLIN_OUTCOME: NEEDS_SPEC`" + ` when you cannot safely complete the remaining ` + "`## Progress`" + ` tasks.
- If the issue is implementable, begin your final response with ` + "`COLIN_OUTCOME: READY_FOR_REVIEW`" + `.
- For ExecPlan-backed issues, use ` + "`COLIN_OUTCOME: READY_FOR_REVIEW`" + ` only after every checkbox under ` + "`## Progress`" + ` is complete.
- After ` + "`COLIN_OUTCOME: READY_FOR_REVIEW`" + `, include a short review-friendly summary with the change, the before and after, the verification you ran, and any remaining risk.
- Prefer Playwright screenshots for browser-visible changes, and prefer terminal or TUI screen captures for terminal-visible changes.
- Colin posts text comments to Linear, so always include textual descriptions of the before and after and mention any screenshots or screen grabs in words even when capture succeeds.
- ` + "`Review`" + ` is PR-only. Clarification-only handoffs go to ` + "`Refine`" + `.`

// RenderWorkflow renders the default workflow file from the collected answers.
func RenderWorkflow(answers Answers) (string, error) {
	if strings.TrimSpace(answers.Backend) == "" {
		answers.Backend = "github"
	}
	tpl, err := template.New("workflow").Funcs(template.FuncMap{
		"yaml": func(value string) string {
			return strconv.Quote(value)
		},
	}).Parse(defaultWorkflowTemplate)
	if err != nil {
		return "", fmt.Errorf("parse workflow template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, answers); err != nil {
		return "", fmt.Errorf("render workflow template: %w", err)
	}
	return strings.TrimSpace(buf.String()) + "\n", nil
}
