# Milestone 1 Runbook: Linear State Worker

This runbook covers operation for milestone 1 of `colin`, which includes only Linear integration and deterministic state management.

## Scope

Included in this milestone:

- Poll Linear issues in candidate states (`Todo`, `In Progress`, `Merge`).
- Determine state transitions using deterministic workflow rules.
- Write lease and execution metadata to Linear issue metadata.

Excluded in this milestone:

- Git operations.
- Codex thread operations.
- Pull request creation.
- Repository modifications outside this codebase.

## Required Configuration

Set these values either in `colin.toml` or environment variables:

- `LINEAR_API_TOKEN`: Linear API token.
- `LINEAR_TEAM_ID`: Team identifier used for issue queries.

## Configuration File

`colin` also supports loading runtime configuration from `colin.toml` in the working directory.

The root command has a `--config` flag with default `colin.toml`, so this also works:

    go run . --config /path/to/colin.toml worker run --once

You can generate a starting point from:

    cp colin.toml.example colin.toml

Or provide a specific file path via environment variable:

    COLIN_CONFIG=/path/to/colin.toml go run . worker run --once

Precedence rule: values in environment variables override values from the TOML file.

Optional environment variables:

- `LINEAR_BASE_URL`: GraphQL endpoint. Default: `https://api.linear.app/graphql`.
- `COLIN_WORKER_ID`: Worker identity for leases. Default: `<hostname>-<pid>`.
- `COLIN_POLL_EVERY`: Poll interval duration. Default: `30s`.
- `COLIN_LEASE_TTL`: Lease duration. Default: `5m`.
- `COLIN_DRY_RUN`: `true` or `false`. Default: `false`.

## Commands

Show help:

    go run . worker run --help

Run one reconciliation cycle in dry-run mode:

    LINEAR_API_TOKEN=... LINEAR_TEAM_ID=... go run . worker run --once --dry-run

Run continuously:

    LINEAR_API_TOKEN=... LINEAR_TEAM_ID=... go run . worker run

## Expected Log Shape

Each processed issue emits one structured line:

    issue=COL-123 state="Todo" action=claim_and_transition to="In Progress" reason="claimed todo issue"

Conflicts are logged and skipped for that cycle:

    issue=<issue-id> action=conflict detail=linear conflict

## Recovery

If a worker crashes during processing, rerun `worker run` or `worker run --once`. Lease metadata in Linear protects against duplicate execution while a lease is active.

If an issue is stuck due to stale lease metadata, wait for lease expiry (`COLIN_LEASE_TTL`) or manually clear metadata keys:

- `colin.lease_owner`
- `colin.execution_id`
- `colin.lease_expires_at`

Then rerun one cycle:

    LINEAR_API_TOKEN=... LINEAR_TEAM_ID=... go run . worker run --once

## Validation Checklist

- `go test ./...` passes.
- `worker run --once --dry-run` starts and logs decisions.
- Running `--once` twice does not create duplicate transitions for already reconciled issues.
