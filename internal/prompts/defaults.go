package prompts

import (
	"strings"

	"github.com/pmenglund/colin/internal/domain"
)

// Defaults returns Colin's built-in Codex prompt templates.
func Defaults() domain.PromptConfig {
	return domain.PromptConfig{
		CodingFallback: "You are working on an issue from Linear.",
		CodingContinuation: `Continue working on {{.issue.identifier}} without restating the original plan. Pick up from the current thread history and leave the issue in a handoff-ready state if appropriate.{{if .exec_plan_working_copy_path}}

{{.exec_plan_tracking}}{{end}}`,
		ExecPlanDecision: `Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.

Return a short answer.
The first line must be exactly one of:
{{.exec_plan_decisions.one_shot_line}}
{{.exec_plan_decisions.exec_plan_line}}

After the first line, include a brief rationale in 1-3 sentences.
Choose ` + "`ONE_SHOT`" + ` only when the change is small and safe enough to implement directly without a stored plan.
Choose ` + "`EXEC_PLAN`" + ` when the issue is large, risky, multi-step, or would benefit from a persistent implementation plan.

Issue context:
- Identifier: {{.issue.identifier}}
- Title: {{.issue.title}}
- State: {{.issue.state}}{{if .issue.url}}
- URL: {{.issue.url}}{{end}}{{if .issue.labels}}
- Labels:{{range .issue.labels}}
  - {{.}}{{end}}{{end}}{{if .issue.description}}

Issue description:

{{.issue.description}}{{end}}{{if .issue.review_feedback}}

Review feedback:{{range .issue.review_feedback}}
- {{.body}}{{end}}{{end}}{{if .issue.review_threads}}

GitHub review threads:{{range .issue.review_threads}}
- {{.path}}{{if .line}}:{{.line}}{{end}} by {{.author}}: {{.body}}{{end}}{{end}}`,
		ExecPlanDecisionRetry: `Your previous ExecPlan strategy response could not be parsed.

Return a short answer.
The first line must be exactly one of:
{{.exec_plan_decisions.one_shot_line}}
{{.exec_plan_decisions.exec_plan_line}}

After the first line, include a brief rationale in 1-3 sentences.
Do not repeat the original question or issue description.{{if .previous_first_line}}
Your previous first line was: {{printf "%q" .previous_first_line}}{{end}}`,
		ExecPlanGeneration: `Create an ExecPlan for the Linear issue below.

Do not modify repository files or implement the change yet.
Return only the final ExecPlan markdown document as file contents, without surrounding commentary and without wrapping it in an outer triple-backtick fence.

Issue context:
- Identifier: {{.issue.identifier}}
- Title: {{.issue.title}}
- State: {{.issue.state}}{{if .issue.url}}
- URL: {{.issue.url}}{{end}}{{if .issue.labels}}
- Labels:{{range .issue.labels}}
  - {{.}}{{end}}{{end}}{{if .issue.description}}

Issue description:

{{.issue.description}}{{end}}{{if .issue.review_feedback}}

Review feedback:{{range .issue.review_feedback}}
- {{.body}}{{end}}{{end}}{{if .issue.review_threads}}

GitHub review threads:{{range .issue.review_threads}}
- {{.path}}{{if .line}}:{{.line}}{{end}} by {{.author}}: {{.body}}{{end}}{{end}}

ExecPlan authoring guide:

{{.exec_plan_authoring_guide}}`,
		ExecPlanTracking: `ExecPlan working copy: {{.exec_plan_working_copy_path}}
Keep that file updated as you work. It is the live copy Colin will sync back to the Linear issue after each turn.
For ExecPlan-backed issues, do not return ` + "`{{.outcomes.ready_for_review}}`" + ` until every checkbox under ` + "`## Progress`" + ` is complete.
If you cannot safely complete the remaining ` + "`## Progress`" + ` tasks, return ` + "`{{.outcomes.needs_spec}}`" + ` and explain the blocker.{{if .remaining_progress_tasks}}
Remaining ` + "`## Progress`" + ` tasks:{{range .remaining_progress_tasks}}
- {{.}}{{end}}{{end}}`,
		MergeRecovery: `Repair the merge conflict for the Linear issue below so Colin can retry the GitHub merge.

You are working in the issue branch workspace that GitHub reported as not mergeable.
Fetch the base branch, merge it into the current branch, resolve any conflicts without dropping valid changes from either side, run focused verification, and leave the branch ready for Colin to publish and retry the merge.

Return a short answer.
The first line must be exactly:
{{.outcomes.ready_for_merge_retry}}

Only return that line when the branch is ready for Colin to retry the merge.
After the first line, include a brief 1-3 sentence summary of what you resolved and any verification you ran.

Issue context:
- Identifier: {{.issue.identifier}}
- Title: {{.issue.title}}
- State: {{.issue.state}}{{if .merge.pr_number}}
- PR: #{{.merge.pr_number}}{{end}}{{if .merge.pr_url}}
- PR URL: {{.merge.pr_url}}{{end}}{{if .merge.branch}}
- Branch: {{.merge.branch}}{{end}}{{if .merge.base_ref}}
- Base ref: {{.merge.base_ref}}{{end}}{{if .merge.error}}

Original merge error:

{{.merge.error}}{{end}}

Requirements:
- Merge the latest base branch into the current branch inside this workspace.
- Resolve conflicts fully; do not leave conflict markers or an unfinished merge.
- Preserve both the branch changes and the relevant base-branch updates.
- Run the most relevant focused checks for the conflicted files.
- Do not move the Linear issue or open/close PRs yourself; Colin will handle publish and merge retry after your turn.`,
		MergeRecoveryContinuation: `Continue resolving the merge conflict for {{.issue.identifier}}.{{if .merge.base_ref}} The goal is to leave the current branch ready to merge {{.merge.base_ref}}.{{end}}

Return ` + "`{{.outcomes.ready_for_merge_retry}}`" + ` only when the branch is fully ready for Colin to retry the merge.`,
	}
}

// WithDefaults fills empty prompt templates with Colin's built-in defaults.
func WithDefaults(overrides domain.PromptConfig) domain.PromptConfig {
	defaults := Defaults()
	apply := func(target *string, source string) {
		if value := strings.TrimSpace(source); value != "" {
			*target = value
		}
	}
	apply(&defaults.CodingFallback, overrides.CodingFallback)
	apply(&defaults.CodingContinuation, overrides.CodingContinuation)
	apply(&defaults.ExecPlanDecision, overrides.ExecPlanDecision)
	apply(&defaults.ExecPlanDecisionRetry, overrides.ExecPlanDecisionRetry)
	apply(&defaults.ExecPlanGeneration, overrides.ExecPlanGeneration)
	apply(&defaults.ExecPlanTracking, overrides.ExecPlanTracking)
	apply(&defaults.MergeRecovery, overrides.MergeRecovery)
	apply(&defaults.MergeRecoveryContinuation, overrides.MergeRecoveryContinuation)
	return defaults
}

// WorkflowYAML renders a WORKFLOW.md prompts section from the supplied config.
func WorkflowYAML(cfg domain.PromptConfig) string {
	cfg = WithDefaults(cfg)
	var b strings.Builder
	b.WriteString("prompts:\n")
	writeBlock(&b, "coding_fallback", cfg.CodingFallback)
	writeBlock(&b, "coding_continuation", cfg.CodingContinuation)
	writeBlock(&b, "exec_plan_decision", cfg.ExecPlanDecision)
	writeBlock(&b, "exec_plan_decision_retry", cfg.ExecPlanDecisionRetry)
	writeBlock(&b, "exec_plan_generation", cfg.ExecPlanGeneration)
	writeBlock(&b, "exec_plan_tracking", cfg.ExecPlanTracking)
	writeBlock(&b, "merge_recovery", cfg.MergeRecovery)
	writeBlock(&b, "merge_recovery_continuation", cfg.MergeRecoveryContinuation)
	return strings.TrimRight(b.String(), "\n")
}

func writeBlock(b *strings.Builder, key string, value string) {
	b.WriteString("  ")
	b.WriteString(key)
	b.WriteString(": |\n")
	value = strings.TrimSpace(value)
	if value == "" {
		b.WriteString("    \n")
		return
	}
	for _, line := range strings.Split(value, "\n") {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
}
