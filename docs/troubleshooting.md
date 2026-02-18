# Colin Troubleshooting Playbook

Use this playbook with `docs/operator-runbook.md` when Colin fails to start, fails to reconcile issues, or gets stuck in a workflow state.

## Quick Triage

1. Run a single dry-run cycle to reduce noise:

       go run . --config ./colin.toml worker run --once --dry-run

2. Capture the exact error text.
3. Match the error text to a case below.

## Startup and Configuration Failures

### `LINEAR_API_TOKEN is required`

Cause:

- Backend is `http` (default) and no API token is set in config or environment.

Fix:

1. Set `linear_api_token` in `colin.toml` or `LINEAR_API_TOKEN` in environment.
2. Re-run one-shot validation:

       go run . --config ./colin.toml worker run --once --dry-run

### `LINEAR_TEAM_ID is required`

Cause:

- Backend is `http` and no team id is configured.

Fix:

1. Set `linear_team_id` in `colin.toml` or `LINEAR_TEAM_ID` in environment.
2. Re-run one-shot validation.

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
2. If urgent, remove these metadata keys from the issue description metadata block:
   - `colin.lease_owner`
   - `colin.execution_id`
   - `colin.lease_expires_at`
3. Run one cycle again.

### Issue in `Merge` does not transition to `Done`

Cause:

- `colin.merge_ready` is missing or not `"true"`.

Fix:

1. Add/update metadata block in issue description:

       <!-- colin:metadata {"colin.merge_ready":"true"} -->

2. Run one worker cycle.

### Metadata parse error from malformed `<!-- colin:metadata ... -->`

Cause:

- Metadata block JSON is invalid.

Fix:

1. Edit issue description and repair/remove malformed JSON.
2. Re-run one cycle:

       go run . --config ./colin.toml worker run --once

## Workspace and Git Failures

### Worktree bootstrap failure for a `Todo -> In Progress` transition

Cause:

- Worktree path under `COLIN_HOME/worktrees/<ISSUE_IDENTIFIER>` is stale/corrupt.
- Base branch (`main`) is missing locally.

Fix:

1. Ensure repository has `main` branch locally.
2. Stop worker.
3. Remove stale worktree path.
4. Run `git worktree prune`.
5. Start worker and retry transition.
