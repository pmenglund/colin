# COLIN-10 Review-State Evidence Format

This runbook defines the expected Linear comment payload when Colin transitions an issue from `In Progress` to `Review`.

## Expected Comment Structure

The worker posts one deterministic markdown comment with sections in this exact order:

1. Header line:

    Moved to **Review** after Codex execution.

2. `## Execution Summary`
3. `## Execution Context`
4. Optional `## Evidence` (only when evidence pointers are present)

`Execution Context` always includes three rows:

- `Thread` (Codex thread id, or `_not recorded_`)
- `Branch` (git branch name, or `_not recorded_`)
- `Worktree` (worktree path, or `_not recorded_`)

`Evidence` may include:

- `Terminal transcript` (for example a transcript path or URL)
- `Screenshot` (for example a screenshot path or URL)

If no evidence pointers are provided, the `## Evidence` section is omitted.

## Reviewer Verification Steps

1. Open the issue in Linear and locate the latest comment generated at transition to `Review`.
2. Confirm section order is deterministic:
   - header
   - `Execution Summary`
   - `Execution Context`
   - optional `Evidence`
3. Verify `Execution Summary` explains what changed in plain language.
4. Verify `Execution Context` includes a thread id and the expected branch/worktree metadata for the task.
5. If an `Evidence` section is present, open each pointer and confirm it matches the claimed behavior.
6. If the implementation is acceptable, move the issue to `Merge`. If not, comment requested changes and move back to `Todo`.

## Troubleshooting

- If `Thread`, `Branch`, or `Worktree` shows `_not recorded_`, the task can still be reviewed, but the missing context should be noted in a follow-up issue.
- If a retry occurs after a conflict, the worker should not duplicate the same review comment; duplicate comments indicate a regression in in-progress outcome idempotence.
