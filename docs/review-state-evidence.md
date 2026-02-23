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
   - `## Pull Request`

`Execution Summary` for successful execution should describe:

- `Before` (what state/behavior existed before changes)
- `After` (what changed)
- `How verified` (how the change was validated)

When evidence is available, include reviewer-accessible attachment URLs directly in `Execution Summary`:

- `Before evidence attachment` (Linear attachment URL)
- `After evidence attachment` (Linear attachment URL)

## Reviewer Verification Steps

1. Open the issue in Linear and locate the latest two worker comments posted while processing `In Progress`.
2. Verify the earlier comment starts with `Starting Codex turn with current execution context.` and includes `## Execution Context`.
3. Verify the review-transition comment starts with `Moved to **Review** after Codex execution.` and uses deterministic section order:
   - `Execution Summary`
   - `Pull Request`
4. Verify `Execution Summary` includes clear `Before`, `After`, and `How verified` descriptions.
5. If evidence attachment links are present in `Execution Summary`, open each pointer and confirm:
   - `Before evidence attachment` reflects the pre-change behavior/state.
   - `After evidence attachment` reflects the post-change behavior/state.
6. If the implementation is acceptable, move the issue to `Merge`. If not, comment requested changes and move back to `Todo`.

## Troubleshooting

- If `Thread`, `Branch`, or `Worktree` shows `_not recorded_`, the task can still be reviewed, but the missing context should be noted in a follow-up issue.
- If a retry occurs after a conflict, the worker should not duplicate the same review-transition completion comment; duplicate completion comments indicate a regression in in-progress outcome idempotence.
