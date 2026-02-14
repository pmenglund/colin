# Colin Architecture Notes

This file captures application-specific context that should stay stable across tasks.

## Purpose

Colin is an automation tool that executes a deterministic workflow on top of Linear issues, also referred to as **tasks**.

It aims to work tasks automatically and autonomously, so a human only needs to define the task and decide whether Codex implemented it correctly.

Colin can operate on multiple tasks at the same time, each using its own codex thread, which runs in a separate go routine.

Linear issue dependencies determine which tasks Colin can work on. A task is considered blocked when Linear returns an inverse relation with `type = "blocks"` for that task, and blocked tasks are skipped until the blocking issue is in `Done`.

When Colin starts working on a task, it will create a git worktree in `COLIN_HOME/worktrees` for it and a branch named using the Linear issue ID, e.g. `colin/COL-123`.

## State

Colin uses the Linear states to track the tasks.

### Todo

This is the initial state for a task, and Colin will automatically start to work on any task in this state, unless it is blocked by a dependency. This moves the task into the In Progress state.

### In Progress

This is the state used when Colin is working on a task. When it is done, Colin will either move the task to `Refine` if the task needs more specification, or to `Review` if a human needs to review the implementation.

When Colin initially picks up the task, it will determine if this is a small change that can be performed without any additional planning, or if an ExecPlan should be created for it.

### Refine

This state means Colin needs a better specification of the task. Once provided, the human should move the task back to `Todo`.

### Review

This state means the work is complete, and a human should verify that the implementation matches intent. Tasks should ideally produce a screenshot or screen recording of a webpage, or terminal output of a CLI, to show before and after.

If the task is done, a human moves the task to `Merge` so it can be merged into the main branch. If it needs more work, the changes are described in a Linear comment and a human moves the task back to `Todo`.

### Merge

This state is used as a merge queue, and when in this state Colin will pick up the tasks one at a time, and merge the change into the main branch.

Only one task at a time can be merged. Once complete, the task is moved to `Done`.

### Done

The task has been merged into the main branch.

## Starting a Task

The first time a task is being worked on

1. create a git worktree
2. create a git branch
3. create a Codex thread
4. update the Linear issue with the worktree path, branch name, and Codex session ID
5. add the Codex session ID as git branch metadata

### Canonical Metadata Keys

Colin stores startup execution metadata using these canonical keys:

- Linear metadata key `colin.worktree_path`: absolute path to the task worktree
- Linear metadata key `colin.branch_name`: git branch used for the task
- Linear metadata key `colin.codex_thread_id`: Codex thread identifier for the task execution
- Linear metadata key `colin.codex_session_id`: Codex session identifier used to resume execution context
- Git branch metadata key `branch.<branch>.colinSessionId`: local git config entry storing `colin.codex_session_id` per branch

## Merging a Task

Steps to merge a task

1. merge the git branch into main branch
2. push the main branch upstream
3. delete the git branch
4. delete the git worktree

## System Boundaries

- Primary runtime(s): macOS (CLI process)
- External services: Linear GraphQL API
- Data stores: Linear issue state and metadata stored in issue descriptions, plus metadata stored in git branch metadata

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
- Set `COLIN_HOME` (or `colin_home` in config) to control where task worktrees are created; default is `~/.colin`
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
- Compatibility constraints: workflow state names are currently hard-coded to `Todo`, `Refine`, `In Progress`, `Review`, `Merge`, and `Done`.
- Codex runtime constraint: Codex app-server must be able to write session state under `CODEX_HOME` (or default `~/.codex`), and authentication must be available for turn execution.
- When processing Linear issues, processing happens in goroutines so multiple issues can run concurrently. Concurrency is controlled by the `MaxConcurrency` configuration value.

## Testing

To allow fast iteration, a fake in-memory implementation of Linear is implemented in Go and can be swapped out using configuration. This implementation is thread-safe and generated using `maxbrunsfeld/counterfeiter`.

## Change Checklist for Contributors

- Update this file when architecture, paths, or commands change.
- Keep examples and commands copy/paste ready.
- Ensure this file stays consistent with `WORKFLOW.md`, `LANGUAGE.md`, and active milestone plans in `plans/`.
