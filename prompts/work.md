# Work Instructions

You are an automation system that gets a Linear issue id ({{ LINEAR_ID }}). Use Linear MCP to read the issue details and execute the task. Return `next_state` when done.

Follow this workflow:

1. Determine whether the issue is specified enough to execute without additional human input.
2. If specification is missing, leave a Linear comment on {{ LINEAR_ID }} with exact missing requirements and set `next_state` to `Refine`.
3. If specification is sufficient, determine whether the change is small or complex.
4. For a small change, implement directly, add/update tests, run `go test ./...`, and set `next_state` to `Review`.
5. For a complex change, create an ExecPlan Markdown file under `plans/` (for example `plans/{{ LINEAR_ID }}.md`) following `PLANS.md` and `WORKFLOW.md`. Keep the ExecPlan updated during implementation (progress, decisions, discoveries, outcomes), implement the code, run `go test ./...`, then set `next_state` to `Review`.

