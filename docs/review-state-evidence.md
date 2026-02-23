# COLIN-10 Review-State Evidence Format

This runbook defines the expected Linear comment payload when Colin processes an issue in `In Progress` and transitions it to `Review`.

## Expected Comment Sequence

The worker posts two deterministic markdown comments:

1. A pre-turn execution-context comment before Codex starts:

    Starting Codex turn with current execution context.

   This comment includes `## Execution Context` with:
   - `Thread` (Codex thread id from issue metadata, or `_not recorded_`)
   - `Branch` (git branch name, or `_not recorded_`)
   - `Worktree` (worktree path, or `_not recorded_`)

2. A turn-complete review comment:

    Moved to **Review** after Codex execution.

   Section order is:
   - `## Execution Summary`
   - Optional `## Evidence` (only when evidence pointers are present)

`Evidence` may include:

- `Terminal transcript` (for example a transcript path or URL)
- `Screenshot` (for example a screenshot path or URL)

If no evidence pointers are provided, the `## Evidence` section is omitted from the review comment.

## Reviewer Verification Steps

1. Open the issue in Linear and locate the latest two worker comments posted while processing `In Progress`.
2. Verify the earlier comment starts with `Starting Codex turn with current execution context.` and includes `## Execution Context`.
3. Verify the review-transition comment starts with `Moved to **Review** after Codex execution.` and uses deterministic section order:
   - `Execution Summary`
   - optional `Evidence`
4. If an `Evidence` section is present, open each pointer and confirm it matches the claimed behavior.
5. If the implementation is acceptable, move the issue to `Merge`. If not, comment requested changes and move back to `Todo`.

## Troubleshooting

- If `Thread`, `Branch`, or `Worktree` shows `_not recorded_`, the task can still be reviewed, but the missing context should be noted in a follow-up issue.
- If a retry occurs after a conflict, the worker should not duplicate the same review-transition completion comment; duplicate completion comments indicate a regression in in-progress outcome idempotence.
