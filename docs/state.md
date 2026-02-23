# Colin State Reference

This document explains the workflow states Colin uses and when transitions happen.

Notes:
- State names below are canonical defaults. Your Linear state names can differ via `[workflow_states]` mapping in `colin.toml`.
- Colin actively processes only candidate states: `Todo`, `In Progress`, `Merge`, `Merged`, and `Done`.
- `Refine` and `Review` are part of the workflow, but are primarily human-driven in practice.

## Lifecycle at a glance

Primary path:

`Todo -> In Progress -> Review -> Merge -> Merged -> Done`

Refinement path:

`Todo -> Refine`
`In Progress -> Refine`
`Refine -> Todo` (human)

Recovery path from `Done`:

- `Done -> Merge` when branch is still unmerged.
- `Done -> Merged` when branch is merged but cleanup is incomplete.

## State-by-state

### `Todo`

Purpose:
- Work is ready to be picked up by Colin.

Transitions out:
- `Todo -> In Progress` (Colin): issue is claimable, not blocked, and spec is ready.
- `Todo -> Refine` (Colin): required specification is missing.

### `Refine`

Purpose:
- Specification needs clarification or completion before execution.

Transitions out:
- `Refine -> Todo` (Human): spec is updated and issue is ready for Colin to retry.

### `In Progress`

Purpose:
- Colin has claimed the issue and is executing work.

Transitions out:
- `In Progress -> Review` (Colin): work is ready for human review (includes review evidence/comment path).
- `In Progress -> Refine` (Colin): spec became insufficient or was marked as needing refinement.

### `Review`

Purpose:
- Human validation of implementation and PR changes.

Transitions out:
- `Review -> Todo` (Human): reviewer requests rework.
- `Review -> Merge` (Human/GitHub automation): PR approved/ready to merge.

### `Merge`

Purpose:
- PR is ready; merge orchestration or merge status reconciliation is in progress.

Transitions out:
- `Merge -> Merged` (GitHub automation primary, Colin fallback): PR is confirmed merged.

Details:
- In GitHub-backed mode, Colin can keep issue in `Merge` while merge is still pending.
- This prevents premature movement when auto-merge is enabled but not yet completed.

### `Merged`

Purpose:
- PR is merged; local repository cleanup is pending.

Transitions out:
- `Merged -> Done` (Colin): cleanup succeeds (worktree/branch/session metadata cleanup).

Details:
- If cleanup fails, issue remains in `Merged` and retries in later cycles.

### `Done`

Purpose:
- Terminal state after merge and cleanup are both complete.

Transitions out (recovery only):
- `Done -> Merge` (Colin): stale branch exists and is not merged into base.
- `Done -> Merged` (Colin): stale branch exists but is already merged; only cleanup remains.

## Other Linear states

Teams may also keep states like `Backlog`, `Canceled`, or `Duplicate`.
Colin does not actively process those states in the worker loop.
