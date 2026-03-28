# Colin Architecture Notes

This file captures application-specific context that should stay stable across tasks.

## Purpose

Colin is now a repository-driven orchestration service. It continuously reads eligible Linear issues, creates or reuses a per-issue workspace, runs a Codex session inside that workspace, reconciles active runs against tracker state, and exposes an in-memory runtime snapshot for observability.

The repository-owned `WORKFLOW.md` file is the primary runtime contract. YAML front matter controls tracker, polling, workspace, hook, and Codex settings; the Markdown body or referenced prompt assets provide the agent instructions.

Colin can operate on multiple issues at the same time. The orchestrator owns concurrency, claims, retries, and live session telemetry. Linear issue dependencies still determine whether `Todo` work is blocked.

## State

Colin now has two distinct state layers:

- Tracker state: Linear states such as `Todo`, `In Progress`, `Review`, or `Done`.
- Runtime state: orchestrator-owned `claimed`, `running`, and `retry_attempts` entries.

Tracker state decides whether an issue is active or terminal. Runtime state decides whether Colin may dispatch, retry, or cancel a worker attempt.

### Todo

This is the initial state for a task, and Colin will automatically start to work on any task in this state, unless it is blocked by a dependency. This moves the task into the In Progress state.

### In Progress

This is the state used when Colin is working on a task. When it is done, Colin will either move the task to `Refine` if the task needs more specification, or to `Review` if a human needs to review the implementation.

When Colin initially picks up the task, it will determine if this is a small change that can be performed without any additional planning, or if an ExecPlan should be created for it.

### Refine

This state means Colin needs a better specification of the task. Once provided, the human should move the task back to `Todo`.

### Review

This state means the work is complete, and a human should verify that the implementation matches intent. Colin must ensure a pull request exists before transitioning an issue to `Review`. Tasks should ideally produce a screenshot or screen recording of a webpage, or terminal output of a CLI, to show before and after.
For expected `Review` comment payload structure and reviewer checks, use `docs/review-state-evidence.md`.

If the task is done, a human moves the task to `Merge` so it can be merged into the main branch. If it needs more work, the changes are described in a Linear comment and a human moves the task back to `Todo`.

### Merge

This state is used as a merge queue. Colin automatically attempts merge execution for issues in `Merge` and transitions `Merge -> Done` when merge execution succeeds.

Transition to `Done` happens only after merge execution succeeds end-to-end (merge branch, optional base-branch push when enabled and remote exists, delete branch, delete worktree).
Merge execution is strict fail-fast: if source branch or worktree path is missing/stale, merge fails and the issue stays in `Merge`.

### Done

The task has been merged into the main branch.
Colin also reconciles `Done` for stale merge state: if a `colin/*` source branch still exists, Colin reopens the issue to `Merge` with a recovery comment so merge/cleanup can run.

## Starting a Task

When Colin dispatches an issue it:

1. claims the issue in orchestrator memory
2. creates or reuses the per-issue workspace
3. runs configured workspace hooks
4. starts or resumes a Codex thread in that workspace
5. tracks live session metadata in memory while the turn runs

Git worktree/bootstrap behavior remains available as a workspace-population adapter, but it is no longer the orchestrator’s core abstraction.

### Canonical Metadata Keys

Colin stores startup execution metadata using these canonical keys:

- Linear metadata key `colin.worktree_path`: absolute path to the task worktree
- Linear metadata key `colin.branch_name`: git branch used for the task
- Linear metadata key `colin.thread_id`: Codex thread identifier for in-progress execution context
- Git branch metadata key `branch.<branch>.colinSessionId`: local git config entry storing the latest Codex session ID per branch

## Merging a Task

Steps to merge a task

1. ensure the change has passed human review and is ready to merge
2. move the issue to `Merge`
3. Colin merges the git branch into the base branch, optionally pushes upstream when configured and available, and cleans up branch/worktree
4. verify the issue transitioned to `Done`

Merge coordinates are read from issue metadata keys `colin.branch_name` and `colin.worktree_path` when available. If branch metadata is missing, Colin falls back to `colin/<issue-identifier>`.
If merge coordinates are inconsistent (for example: missing branch or missing worktree path), Colin fails the merge cycle and retries after the underlying git state is repaired.

## System Boundaries

- Primary runtime(s): macOS (CLI process)
- External services: Linear GraphQL API and Codex app-server
- Data stores: Linear issue state and metadata stored in Linear attachments, plus optional git branch metadata for the git workspace compatibility path

## Repository Layout

- `cmd/` - Cobra command wiring (root worker execution plus `setup` and `metadata` subcommands)
- `internal/config/` - environment and runtime configuration parsing
- `internal/codexexec/` - Codex SDK adapter for evaluating/executing `In Progress` issues
- `internal/linear/` - Linear GraphQL client and metadata persistence helpers
- `internal/workflow/` - deterministic state transition and lease logic
- `internal/worker/` - polling loop and orchestration
- `docs/` - operator runbooks (`operator-runbook.md`, `troubleshooting.md`) and supporting notes
- `plans/` - living ExecPlans for tracked milestones
- `prompts/` - prompts in markdown format

## Core Components

- `internal/config`: workflow-derived runtime config and reload provider.
- `internal/workflowfile`: `WORKFLOW.md` loader, front matter parser, and strict prompt renderer.
- `internal/linear`: transport adapter for tracker reads plus legacy metadata writes.
- `internal/workspace`: sanitized workspace lifecycle manager with hooks and cleanup.
- `internal/codexexec`: streamed Codex runner with live session updates.
- `internal/orchestrator`: runtime scheduler, reconciliation loop, retry queue, and snapshot state.
- `internal/worker`: legacy compatibility runner and git-oriented adapters that still back the Colin-specific workflow path.

## Architecture Rules

- Keep repository-owned runtime behavior in `WORKFLOW.md` and `internal/config`; avoid re-hard-coding workflow policy in the service.
- Keep tracker reads in `internal/linear` and runtime scheduling in `internal/orchestrator`.
- Keep filesystem safety and hook execution in `internal/workspace`.
- Keep Codex SDK specifics in `internal/codexexec`; orchestration should consume streamed attempt results rather than SDK internals directly.
- Treat `internal/worker` as the Colin compatibility layer for git/bootstrap/merge behavior, not as the primary scheduler.
- Record significant architecture tradeoffs in the active ExecPlan decision log.

## Local Development

- Install dependencies: `go mod download`
- Optional config template: copy `colin.toml.example` to `colin.toml` and fill values
- Set `COLIN_HOME` (or `colin_home` in config) to control where task worktrees are created; default is `~/.colin`
- Override config path with root flag: `go run . --config /path/to/colin.toml --once`
- Show CLI help: `go run . --help`
- Ensure required workflow states exist/are valid: `go run . --config ./colin.toml setup`
- Run worker once (dry-run): `go run . --config ./colin.toml --once --dry-run`
- Run worker once with fake backend (offline): set `linear_backend = "fake"` and run `go run . --config ./colin.toml --once`
- Run tests locally: `go test ./...`
- Lint/format checks: `go vet ./...` and `gofmt -w .`
- Operator docs: `docs/operator-runbook.md` and `docs/troubleshooting.md`

## Operational Constraints

- Security and privacy requirements: Linear API token must be provided via `colin.toml` or environment variables and must not be logged.
- Configuration precedence: `colin.toml` (or `COLIN_CONFIG`) is loaded first, then environment variables override file values.
- CLI precedence: root `--config` flag controls which file is loaded (default `colin.toml`).
- Backend constraint: `COLIN_LINEAR_BACKEND`/`linear_backend` must be either `http` or `fake`.
- Performance expectations: polling loop should be lightweight, deterministic, and safe to run repeatedly.
- Compatibility constraints: workflow state names are resolved at startup from `[workflow_states]` in `colin.toml`; runtime fails fast when mapped names cannot be resolved to actual Linear workflow states.
- Codex runtime constraint: Codex app-server must be able to write session state under `CODEX_HOME` (or default `~/.codex`), and authentication must be available for turn execution.
- When processing Linear issues, processing happens in goroutines so multiple issues can run concurrently. Concurrency is controlled by the `MaxConcurrency` configuration value.
- Prompt should be embedded, but have a config option to read an alternative from a file.

## Testing

To allow fast iteration, a fake in-memory implementation of Linear is implemented in Go and can be swapped out using configuration.
- The implementation must be concurrency-safe and generated using the go module `maxbrunsfeld/counterfeiter`.
- The generated fakes should be saved in a directory called `fakes`.

## Change Checklist for Contributors

- Update this file when architecture, paths, or commands change.
- Keep examples and commands copy/paste ready.
- Ensure this file stays consistent with `WORKFLOW.md`, `LANGUAGE.md`, and active milestone plans in `plans/`.
