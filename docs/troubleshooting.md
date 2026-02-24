# Colin Troubleshooting Playbook

Use this playbook with `docs/operator-runbook.md` when Colin fails to start, fails to reconcile issues, or gets stuck in a workflow state.

## Quick Triage

1. Run a single dry-run cycle to reduce noise:

       go run . --config ./colin.toml --once --dry-run

2. Capture the exact error text.
3. Match the error text to a case below.

## Startup and Configuration Failures

### `LINEAR_API_TOKEN is required`

Cause:

- Backend is `http` (default) and no API token is set in config or environment.

Fix:

1. Set `linear_api_token` in `colin.toml` or `LINEAR_API_TOKEN` in environment.
2. Re-run one-shot validation:

       go run . --config ./colin.toml --once --dry-run

### `LINEAR_TEAM_ID is required`

Cause:

- Backend is `http` and no team id is configured.

Fix:

1. Set `linear_team_id` in `colin.toml` or `LINEAR_TEAM_ID` in environment.
2. Re-run one-shot validation.

### `resolve workflow states: required workflow states not found: ...; run 'colin setup' to create/validate mapped states`

Cause:

- Worker startup could not resolve one or more configured `[workflow_states]` names to actual team states.

Fix:

1. Verify `[workflow_states]` names in `colin.toml`.
2. Run setup:

       go run . --config ./colin.toml setup

3. Re-run worker once.

### `ensure workflow states: workflow state "<key>" mapped to "<name>" has type "<actual>", expected "<expected>"`

Cause:

- A mapped state exists but has an incompatible Linear workflow type.

Fix:

1. Point the mapping to a state with the required type:
   - `todo` must be `unstarted`
   - `done` must be `completed`
   - `in_progress`, `refine`, `review`, `merge` must be `started`
2. Re-run setup:

       go run . --config ./colin.toml setup

### `COLIN_LINEAR_BACKEND must be one of "http" or "fake"`

Cause:

- Unsupported backend value.

Fix:

1. Set `linear_backend` / `COLIN_LINEAR_BACKEND` to `http` or `fake`.
2. Re-run worker.

### `COLIN_MAX_CONCURRENCY must be > 0`

Cause:

- Invalid zero or negative concurrency.

Fix:

1. Set `max_concurrency` (`colin.toml`) or `COLIN_MAX_CONCURRENCY` (env) to a positive integer.
2. Start with a conservative value such as `1` or `2` while debugging.

### `COLIN_HOME must not be empty`

Cause:

- `colin_home` or `COLIN_HOME` is set to an empty/whitespace value.

Fix:

1. Remove the empty override.
2. Set a valid writable path, for example `~/.colin`.
3. Re-run worker once.

### `COLIN_WORKER_ID must not be empty`

Cause:

- `worker_id`/`COLIN_WORKER_ID` is empty.

Fix:

1. Set a stable worker id (for example `worker_id = "colin-prod-1"`).
2. Re-run worker.

### Duration parse errors (`COLIN_POLL_EVERY` or `COLIN_LEASE_TTL`)

Cause:

- Invalid duration format.

Fix:

1. Use Go duration strings (`30s`, `5m`, `1h`).
2. Re-run worker.

## Runtime and Environment Failures

### Codex cannot write under `CODEX_HOME`

Symptoms:

- In-progress issue execution fails before review/refine transition.
- Runtime errors mention session state, permissions, or missing Codex auth/session files.

Fix:

1. Ensure `CODEX_HOME` points to a writable directory.
2. Validate permissions for the runtime user.
3. Confirm Codex authentication/session setup is valid for non-interactive execution.
4. Re-run one cycle and confirm `In Progress` issues can transition.

### GraphQL transport failures (HTTP status, timeout, connection errors)

Cause:

- Invalid `linear_base_url`, bad token, network outage, or Linear API transient failure.

Fix:

1. Confirm `linear_base_url` (normally `https://api.linear.app/graphql`).
2. Confirm token/team values.
3. Retry with `--once` first; then resume continuous run.

## Workflow and State Stalls

### Issue stuck in `In Progress` with lease owned by another worker

Cause:

- Active lease metadata indicates another worker still owns the issue.

Fix:

1. Wait for lease expiry (`lease_ttl`, default 5m).
2. If urgent, remove these metadata keys from the issue metadata attachment:
   - `colin.lease_owner`
   - `colin.execution_id`
   - `colin.lease_expires_at`
3. Run one cycle again.

### Issue in `Merge`/`Merged` does not transition forward

Cause:

- Merge-phase or cleanup-phase execution failed (for example: merge conflict, push failure, missing source branch, missing/stale worktree path).

Fix:

1. Check worker logs for `execute merge for issue <ID>` errors.
2. Resolve the reported git/worktree issue for the current phase (`Merge` or `Merged`).
3. Re-run one worker cycle.

### Issue reached `Done` but branch/worktree still exists

Cause:

- Workflow bypassed merge queue (for example: manual `Review -> Done` transition), or prior cleanup failed.

Fix:

1. Run one worker cycle. Colin now auto-detects unresolved task branches on `Done` issues and reopens them to `Merge` (not merged) or `Merged` (merged but cleanup pending).
2. Confirm the issue was moved back with a recovery comment naming the target state.
3. Re-run another cycle after resolving any reported merge precondition errors.

### Missing or malformed metadata attachment values

Cause:

- Metadata attachment is missing required keys or contains unexpected value types.

Fix:

1. Edit/create metadata attachment `https://github.com/pmenglund/colin/blob/main/docs/metadata.md` and set valid string values.
2. Re-run one cycle:

       go run . --config ./colin.toml --once

## Workspace and Git Failures

### Worktree bootstrap failure for a `Todo -> In Progress` transition

Cause:

- Worktree path under `COLIN_HOME/worktrees/<ISSUE_IDENTIFIER>` is stale/corrupt.
- Configured base branch (`base_branch` / `COLIN_BASE_BRANCH`) is missing locally.

Fix:

1. Ensure repository has the configured base branch locally (default `main`).
2. Stop worker.
3. Remove stale worktree path.
4. Run `git worktree prune`.
5. Start worker and retry transition.
