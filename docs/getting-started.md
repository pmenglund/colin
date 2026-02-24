# Colin Getting Started

This guide helps first-time users get Colin running end-to-end.

## Prerequisites

- Go installed and available on `PATH`
- Access to a Linear workspace
- Linear API token and team ID
- GitHub CLI (`gh`) installed and authenticated (required for automatic PR creation)

## 1. Build the binary

From repository root:

```bash
go build -o ./bin/colin .
```

## 2. Create config

From repository root:

```bash
cp colin.toml.example colin.toml
```

Set at minimum:

- `linear_api_token`
- `linear_team_id`
- `worker_id` (recommended for shared environments)

Optional but common:

- `colin_home` (defaults to `~/.colin`)
- `base_branch` (defaults to `main`)
- `push_after_merge` (defaults to `true`; pushes base branch only when remote exists)
- `project_filter` (optional comma-separated project IDs/names)
- `poll_every`
- `lease_ttl`
- `max_concurrency`

## 3. Verify CLI

```bash
./bin/colin --help
./bin/colin setup --help
```

You should see `setup`, `metadata`, and `worker` commands plus the root `--config`, `--once`, and `--dry-run` flags.

## 4. Initialize/validate Linear workflow states

```bash
./bin/colin --config ./colin.toml setup
```

Expected behavior:

- Colin prints your Linear team details.
- Required workflow states are validated or created.
- Required states include `todo`, `in_progress`, `refine`, `review`, `merge`, `merged`, and `done`.
- Resolved runtime mapping is displayed.

## 5. Run a safe one-shot validation

```bash
./bin/colin --config ./colin.toml --once --dry-run
```

Expected behavior:

- Exactly one reconciliation cycle runs.
- Logs show cycle activity.
- No writes are made to Linear because dry-run is enabled.

## 6. Run continuously

```bash
./bin/colin --config ./colin.toml
```

Colin now polls Linear continuously and processes candidate issues.

## Offline mode (optional)

If you want local verification without Linear writes, switch config to fake backend:

```toml
linear_backend = "fake"
```

Then run:

```bash
./bin/colin --config ./colin.toml --once
```

## Next steps

- Read [`docs/usage.md`](usage.md) for daily operations.
- Use [`docs/operator-runbook.md`](operator-runbook.md) for production-like workflows.
- Use [`docs/troubleshooting.md`](troubleshooting.md) for recovery.
