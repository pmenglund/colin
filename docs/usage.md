# Colin Usage Guide

This guide summarizes the commands and normal operating patterns for Colin.

## Core commands

### Show help

```bash
./bin/colin --help
```

### Setup Linear workflow states

```bash
./bin/colin --config ./colin.toml setup
```

Use this whenever you first configure a workspace or change workflow-state mappings.

### Run one reconciliation cycle

```bash
./bin/colin --config ./colin.toml worker run --once
```

Useful for validation, controlled rollouts, and incident checks.

### Run one dry-run reconciliation cycle

```bash
./bin/colin --config ./colin.toml worker run --once --dry-run
```

Use this to inspect decisions without writing state or metadata.

### Run continuously

```bash
./bin/colin --config ./colin.toml worker run
```

This is the standard operating mode.

## Common options

- `--config <path>`: use a specific TOML configuration file.
- `--once`: process exactly one poll cycle and exit.
- `--dry-run`: compute transitions/decisions but skip Linear writes.

## Typical operating flow

1. Run `setup` after configuring or updating mappings.
2. Run one cycle with `--once --dry-run` to validate behavior safely.
3. Start continuous worker mode.
4. Use one-shot dry-runs for diagnostics while system is live.

## Configuration notes

Most deployments configure via `colin.toml` plus environment overrides.

Most important values:

- `linear_api_token`
- `linear_team_id`
- `worker_id`
- `linear_backend` (`http` or `fake`)
- `project_filter` (optional comma-separated project IDs/names)
- `poll_every`
- `lease_ttl`
- `max_concurrency`

Environment variables override config file values. To scope processing to specific projects at runtime, set `COLIN_PROJECT_FILTER` (comma-separated IDs/names).

## When to use the runbook and troubleshooting docs

- Use [`operator-runbook.md`](operator-runbook.md) for operations, lifecycle expectations, and disaster recovery.
- Use [`troubleshooting.md`](troubleshooting.md) for error-specific fixes.
