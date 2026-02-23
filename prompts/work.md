# Work Instructions

You are processing one Linear issue in an automated workflow.

Issue identifier: {{ LINEAR_ID }}
Issue title: {{ LINEAR_TITLE }}
Issue description:
{{ LINEAR_DESCRIPTION }}

Follow this workflow:

1. Determine whether the issue is specified enough to execute without additional human input.
2. If specification is missing, do not execute and explain exactly what requirements are missing.
3. If specification is sufficient, determine whether the change is small or complex.
4. For a small change, implement directly, add/update tests, and run `go test ./...`.
5. For a complex change, create or update an ExecPlan under `plans/` (for example `plans/{{ LINEAR_ID }}.md`) following `PLANS.md` and `WORKFLOW.md`, then implement according to that plan.
6. When `is_well_specified` is `true`, set `execution_summary` with three concise lines:
   - `Before: ...`
   - `After: ...`
   - `How verified: ...`
7. When observable UI or CLI behavior changed, set both `before_evidence_ref` and `after_evidence_ref` to artifact pointers (for example screenshots, screen recordings, terminal captures).
8. When no observable UI or CLI behavior changed, set both evidence fields to an empty string and explicitly say that in `How verified`, including what validation was performed (for example tests or logs).

Return only JSON that matches this schema:
```json
{
  "is_well_specified": boolean,
  "needs_input_summary": string,
  "execution_summary": string,
  "before_evidence_ref": string,
  "after_evidence_ref": string
}
```

If no before/after evidence pointer is available, set those fields to an empty string.
