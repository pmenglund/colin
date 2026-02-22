# Colin

Colin is a Codex helper application that runs on your computer and works on Linear issues that you create.

![Colin architecture diagram](docs/colin.svg)

It will use `codex` to work on any issue in the `Todo` state, and starts by determining if it is well enough defined to be worked on. If it isn't, the issue state is set to `Refine` and a comment is added on what needs to be refined.

If it is well-defined, it uses `codex` to implement it, and set the state to `Review`. It will add a comment on the Linear issue with a before and after view of the change, if possible, or else a description of what changed.

This lets the user decide to either accept the change, and move it to the `Merge` status, or the user can add a comment on the Linear issue and move it back to the `Todo` and it will be sent back to `codex` for another turn.

## Quick start

### 1) Prerequisites

- Go (latest stable)
- A Linear API token and team ID (for the default HTTP backend)

### 2) Build the `colin` binary

From the repository root:

```bash
go build -o ./bin/colin .
```

To run commands as `colin`, add the local `bin/` directory to your shell `PATH`:

```bash
export PATH="$(pwd)/bin:$PATH"
```

### 3) Configure Colin

From the repository root:

```bash
cp colin.toml.example colin.toml
```

Edit `colin.toml` and set:

- `linear_api_token`
- `linear_team_id`
- `worker_id` (recommended)
- Optional: `base_branch` (defaults to `main`; set to `master` or another branch when needed)
- Optional: `project_filter` (comma-separated project IDs/names to scope candidate issues)

### 4) Validate setup

```bash
colin --config ./colin.toml setup
```

This ensures required workflow states exist (or creates them) and prints the resolved runtime mapping.

### 5) Run one safe dry-run cycle

```bash
colin --config ./colin.toml worker run --once --dry-run
```

This performs one reconciliation cycle without writing changes to Linear.

### 6) Start continuous processing

```bash
colin --config ./colin.toml worker run
```

## How to use Colin

- `colin setup`: create/validate required Linear workflow states.
- `colin metadata <ISSUE-ID>`: print Colin metadata currently stored for one issue.
- `colin worker run --once`: run a single reconciliation cycle and exit.
- `colin worker run --dry-run`: compute decisions without writing to Linear.
- `colin worker run`: run continuously on the configured poll interval.

`project_filter` can also be set from `COLIN_PROJECT_FILTER` as a comma-separated list. Matching is exact (case-insensitive) against project ID or project name.

## Detailed documentation

For specifics, use the docs index:

- [`docs/README.md`](docs/README.md)

Recommended reading order:

1. [`docs/getting-started.md`](docs/getting-started.md) – first-time setup and first successful run.
2. [`docs/usage.md`](docs/usage.md) – day-to-day commands and operating patterns.
3. [`docs/operator-runbook.md`](docs/operator-runbook.md) – production-like operation procedures.
4. [`docs/troubleshooting.md`](docs/troubleshooting.md) – symptom-based recovery steps.
