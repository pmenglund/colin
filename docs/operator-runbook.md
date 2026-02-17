# Colin Operator Runbook

This runbook is for operating Colin in production-like environments. It is written against the current code in `cmd/worker.go`, `internal/config/config.go`, and `internal/workflow/`. For error-specific recovery, use `docs/troubleshooting.md`.

## What Colin Automates

Colin continuously polls Linear and only processes issues in these states:

- `Todo`
- `In Progress`
- `Merge`

A `Todo` issue with required specification is moved to `In Progress` and receives lease metadata. `In Progress` issues are evaluated by Codex and moved to `Review` or `Refine`. `Merge` issues move to `Done` when merge metadata says they are ready.

## Required Configuration

Colin loads configuration in this order:

1. Config file (`colin.toml` by default, or `--config <path>`)
2. Environment variables (override file values)

For `linear_backend = "http"` (default), these values are required:

- `LINEAR_API_TOKEN`
- `LINEAR_TEAM_ID`

Common runtime controls:

- `COLIN_HOME` / `colin_home`: root for worker artifacts and worktrees (default `~/.colin`)
- `CODEX_HOME`: must be writable by Codex runtime (default `~/.codex`)
- `COLIN_LINEAR_BACKEND` / `linear_backend`: `http` or `fake`
- `COLIN_MAX_CONCURRENCY` / `max_concurrency`: number of concurrent issue workers
- `COLIN_POLL_EVERY` / `poll_every`: polling interval
- `COLIN_LEASE_TTL` / `lease_ttl`: lease expiration window
- `COLIN_DRY_RUN` / `dry_run`: compute decisions but do not write to Linear

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
    go run . worker run --help

Expected flags include `--config`, `--once`, and `--dry-run`.

### 3. Dry-run a single cycle

    go run . --config ./colin.toml worker run --once --dry-run

Expected behavior:

- Command exits after one reconciliation cycle.
- Logs include `action=cycle_start`, `action=issues_fetched`, and one or more `action=...` decisions.
- No Linear state or metadata writes are performed.

### 4. Start continuous worker

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

Colin treats `Merge` as a candidate state and transitions `Merge -> Done` when issue metadata contains:

- `colin.merge_ready = "true"`

Operational guidance:

- Set merge-ready metadata for only one issue at a time to preserve queue semantics.
- After an issue moves to `Done`, set the next issue in `Merge` to merge-ready.

Metadata is stored in the issue description as a block like:

    <!-- colin:metadata {"colin.merge_ready":"true"} -->

If metadata is missing or set to false, Colin leaves the issue in `Merge`.

## Disaster Recovery Runbook

### Worker crash or host restart

1. Restart the worker with the same config:

       go run . --config ./colin.toml worker run

2. Colin is idempotent for repeated cycles and resumes from current Linear state.

### Stuck `In Progress` due to active lease

Symptom: issue remains in `In Progress` and logs indicate active lease owned by another worker.

Recovery options:

1. Wait for lease expiration (`lease_ttl`, default 5 minutes).
2. If immediate recovery is needed, clear lease metadata keys from issue description metadata block:
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

### Invalid metadata JSON in issue description

Symptom: metadata parse errors prevent expected transitions.

Recovery:

1. Edit the issue description and remove/fix malformed `<!-- colin:metadata ... -->` block.
2. Re-run one cycle:

       go run . --config ./colin.toml worker run --once

## Offline/Fake Backend Runbook

For local verification without network writes:

1. Set `linear_backend = "fake"` in `colin.toml`.
2. Run:

       go run . --config ./colin.toml worker run --once

With fake backend, Linear credentials are not required and Codex execution paths are skipped.
