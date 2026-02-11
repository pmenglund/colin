# Colin Architecture Notes

This file captures application-specific context that should stay stable across tasks.

## Purpose

Colin is an automation tool that executes a deterministic workflow on top of Linear issues. Milestone 1 established Linear integration and deterministic state management. Milestone 2 adds Codex-thread execution for `In Progress` issues so specification sufficiency is evaluated before work is executed.

## System Boundaries

- Primary runtime(s): macOS (CLI process)
- External services: Linear GraphQL API
- Data stores: Linear issue state and metadata stored in issue descriptions

## Repository Layout

- `cmd/` - Cobra command wiring (`root`, `worker run`)
- `internal/config/` - environment and runtime configuration parsing
- `internal/codexexec/` - Codex SDK adapter for evaluating/executing `In Progress` issues
- `internal/linear/` - Linear GraphQL client and metadata persistence helpers
- `internal/workflow/` - deterministic state transition and lease logic
- `internal/worker/` - polling loop and orchestration
- `docs/` - operator runbooks and milestone docs
- `plans/` - living ExecPlans for tracked milestones

## Core Components

- `internal/linear`: transport adapter for querying and mutating Linear issues.
- `internal/codexexec`: side-effect adapter that starts Codex, opens threads, and returns structured execution outcomes.
- `internal/workflow`: pure transition engine and lease semantics used for deterministic decisions.
- `internal/worker`: execution loop that reconciles issue snapshots with the workflow engine.

## Architecture Rules

- Keep all transition decisions in `internal/workflow`; this package should remain pure and testable without network calls.
- Keep Linear API specifics in `internal/linear`; other packages must rely on the `linear.Client` interface.
- Keep Codex SDK specifics in `internal/codexexec`; other packages should depend on `worker.InProgressExecutor`.
- Keep orchestration and retries in `internal/worker`; do not embed state-machine logic in Cobra command files.
- Record significant architecture tradeoffs in the active ExecPlan decision log.

## Local Development

- Install dependencies: `go mod download`
- Optional config template: copy `colin.toml.example` to `colin.toml` and fill values
- Override config path with root flag: `go run . --config /path/to/colin.toml worker run --once`
- Show CLI help: `go run . --help`
- Run worker once (dry-run): `LINEAR_API_TOKEN=... LINEAR_TEAM_ID=... go run . worker run --once --dry-run`
- Run tests locally: `go test ./...`
- Lint/format checks: `go vet ./...` and `gofmt -w .`

## Operational Constraints

- Security and privacy requirements: Linear API token must be provided via `colin.toml` or environment variables and must not be logged.
- Configuration precedence: `colin.toml` (or `COLIN_CONFIG`) is loaded first, then environment variables override file values.
- CLI precedence: root `--config` flag controls which file is loaded (default `colin.toml`).
- Performance expectations: polling loop should be lightweight, deterministic, and safe to run repeatedly.
- Compatibility constraints: workflow state names are currently hard-coded to `Todo`, `Refine`, `In Progress`, `Human Review`, `Merge`, `Done`, and `Cancelled`.
- Codex runtime constraint: Codex app-server must be able to write session state under `CODEX_HOME` (or default `~/.codex`), and authentication must be available for turn execution.
- When processing Linear issues, it must happen in a go routine, so that multiple issues can run concurrently, as some operations take multiple hours to complete.

## Change Checklist for Contributors

- Update this file when architecture, paths, or commands change.
- Keep examples and commands copy/paste ready.
- Ensure this file stays consistent with `WORKFLOW.md`, `LANGUAGE.md`, and active milestone plans in `plans/`.
