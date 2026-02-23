# Colin Operator Runbook

This runbook is for operating Colin in production-like environments. It is written against the current code in `cmd/worker.go`, `cmd/setup.go`, `internal/config/config.go`, and `internal/workflow/`. For error-specific recovery, use `docs/troubleshooting.md`.

## What Colin Automates

Colin continuously polls Linear and processes only the configured candidate states (`workflow_states.todo`, `workflow_states.in_progress`, `workflow_states.merge`, `workflow_states.merged`, `workflow_states.done`). At startup, Colin resolves those configured names against the team’s real Linear workflow states and fails fast if any mapped state is missing.

A `todo` issue with required specification is moved to `in_progress` and receives lease metadata. `in_progress` issues are evaluated by Codex and moved to `review` or `refine`. `merge` issues move to `merged` after merge-phase execution, and `merged` issues move to `done` after cleanup.

## Required Configuration

Colin loads configuration in this order:

1. Config file (`colin.toml` by default, or `--config <path>`)
2. Environment variables (override file values)

For `linear_backend = "http"` (default), these values are required:

- `LINEAR_API_TOKEN`
- `LINEAR_TEAM_ID`

Common runtime controls:

- `COLIN_HOME` / `colin_home`: root for worker artifacts and worktrees (default `~/.colin`)
- `COLIN_BASE_BRANCH` / `base_branch`: branch used for task worktree bootstrap and merge target (default `main`)
- `COLIN_PUSH_AFTER_MERGE` / `push_after_merge`: push base branch after merge when configured remote exists (default `true`)
- `CODEX_HOME`: must be writable by Codex runtime (default `~/.codex`)
- `COLIN_LINEAR_BACKEND` / `linear_backend`: `http` or `fake`
- `COLIN_MAX_CONCURRENCY` / `max_concurrency`: number of concurrent issue workers
- `COLIN_POLL_EVERY` / `poll_every`: polling interval
- `COLIN_LEASE_TTL` / `lease_ttl`: lease expiration window
- `COLIN_DRY_RUN` / `dry_run`: compute decisions but do not write to Linear

Workflow mapping controls (`colin.toml` only in this iteration):

- `[workflow_states]`
- `todo`
- `in_progress`
- `refine`
- `review`
- `merge`
- `merged`
- `done`

## Startup Runbook

### 1. Prepare configuration

From repository root:

    cp colin.toml.example colin.toml

Edit `colin.toml` and set at least:

- `linear_api_token`
- `linear_team_id`
- `worker_id` (recommended explicit value)

### 2. Validate CLI wiring

    go run . --help
    go run . setup --help
    go run . worker run --help

Expected commands include `setup` and `worker run`; expected flags include `--config`, `--once`, and `--dry-run`.

### 3. Ensure workflow states

    go run . --config ./colin.toml setup

Expected behavior:

- Command prints created/validated states and resolved runtime mapping.
- Missing mapped states are created.
- Existing mapped states are validated to required type.

### 4. Dry-run a single cycle

    go run . --config ./colin.toml worker run --once --dry-run

Expected behavior:

- Command exits after one reconciliation cycle.
- Logs include `action=cycle_start`, `action=issues_fetched`, and one or more `action=...` decisions.
- No Linear state or metadata writes are performed.

### 5. Start continuous worker

    go run . --config ./colin.toml worker run

Expected behavior:

- Logs show `action=run_start` and repeated cycle logs.
- Process continues until interrupted.

## Ongoing Operations Runbook

### Monitor health from logs

Normal loop pattern:

- `action=run_start`
- `action=cycle_start`
- `action=issues_fetched`
- `action=cycle_complete`

If a cycle fails, logs include `action=cycle_error` with a `stage` value.

### Safe operational checks

Use one-shot cycles for diagnostics without changing runtime mode:

    go run . --config ./colin.toml worker run --once --dry-run

### Worker artifacts

When a `Todo` issue transitions to `In Progress` (non-dry-run), Colin creates:

- Worktree: `COLIN_HOME/worktrees/<ISSUE_IDENTIFIER>`
- Branch in that worktree: `colin/<ISSUE_IDENTIFIER>`

If a worktree already exists, Colin reuses it.

## Merge Queue Runbook

Colin treats `Merge` and `Merged` as candidate states and executes merge-side effects automatically.
When merge-phase execution succeeds, Colin transitions `Merge -> Merged`.
When cleanup succeeds, Colin transitions `Merged -> Done`.
Merge-phase execution is strict fail-fast: missing source branch or missing/stale worktree path aborts execution and keeps the issue in `Merge`.

Colin also reconciles `Done` issues. If a Colin task branch still exists for an issue in `Done`, Colin reopens it to:
- `Merge` when the branch is not merged into the base branch
- `Merged` when the branch is already merged but cleanup is incomplete

Operational guidance:

- Merge queue processing is serialized: only one `Merge`/`Merged` issue is processed per cycle.
- Place issues in `Merge` in the order you want them merged.

Metadata is stored in a per-issue Linear attachment with URL:

    https://github.com/pmenglund/colin/blob/main/docs/metadata.md

If merge execution fails, Colin leaves the issue in `Merge` or `Merged` (depending on phase) and retries on the next cycle.

## Disaster Recovery Runbook

### Worker crash or host restart

1. Re-run setup and then restart the worker with the same config:

       go run . --config ./colin.toml setup

       go run . --config ./colin.toml worker run

2. Colin is idempotent for repeated cycles and resumes from current Linear state.

### Stuck `In Progress` due to active lease

Symptom: issue remains in `In Progress` and logs indicate active lease owned by another worker.

Recovery options:

1. Wait for lease expiration (`lease_ttl`, default 5 minutes).
2. If immediate recovery is needed, clear lease metadata keys from the issue metadata attachment:
   - `colin.lease_owner`
   - `colin.execution_id`
   - `colin.lease_expires_at`

### Corrupted or stale worktree

Symptom: bootstrap errors when creating/reusing `COLIN_HOME/worktrees/<ISSUE_IDENTIFIER>`.

Recovery:

1. Stop the worker.
2. Remove the affected worktree path.
3. Run `git worktree prune` from repository root.
4. Restart the worker; Colin recreates the worktree on next eligible transition.

### Missing or invalid metadata attachment

Symptom: expected metadata keys are absent or malformed in attachment metadata, preventing expected transitions.

Recovery:

1. Edit/create the issue metadata attachment at `https://github.com/pmenglund/colin/blob/main/docs/metadata.md`.
2. Ensure required keys are present with string values.
3. Re-run one cycle:

       go run . --config ./colin.toml worker run --once

## Offline/Fake Backend Runbook

For local verification without network writes:

1. Set `linear_backend = "fake"` in `colin.toml`.
2. Run:

       go run . --config ./colin.toml worker run --once

With fake backend, Linear credentials are not required and Codex execution paths are skipped.
