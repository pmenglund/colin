# Colin Architecture Notes

This file captures the stable, repo-specific architecture context for Colin.

## Purpose

Colin is a long-running Go service that watches a Linear project, prepares a per-issue workspace, runs Codex for active issues, moves successful coding runs into the publish handoff state, and performs publish and merge automation from there.

An embedded loopback HTTP server exposes the orchestrator snapshot for live operator inspection, a Tailscale Funnel setup/readiness flow, and stable reserved webhook paths. It still does not participate in orchestration correctness.

The runtime contract lives in `WORKFLOW.md`:

- YAML front matter defines tracker, polling, workspace, repo, hook, and Codex settings.
- The Markdown body is rendered as the prompt for coding runs.
- Colin ensures a Linear `paused` label exists at startup and treats that label as a global dispatch gate.

## State Model

Colin moves a successful coding run from an active state into the first configured publish state, then reacts to the current state and performs the corresponding work.

- Active states: Colin runs Codex work for issues in `tracker.active_states` and moves successful runs into the first state in `repo.publish_states`.
- Publish states: Colin performs git push and PR creation for issues in `repo.publish_states`.
- Merge states: Colin merges the PR for issues in `repo.merge_states`.
- Terminal states: Colin stops work for issues in `tracker.terminal_states`.

Today, the checked-in workflow uses:

- Active: `Todo`, `In Progress`
- Publish: `Review`
- Merge: `Merge`
- Terminal: `Done`, `Closed`, `Cancelled`, `Canceled`, `Duplicate`

## Core Runtime Flow

1. Load and validate `WORKFLOW.md`.
2. Poll Linear for candidate issues in the configured project and tracked states.
3. Reconcile running issues and queued retries.
4. Create or reuse a workspace for each dispatched issue.
5. For active issues, optionally decide once whether the work is a one-shot change or needs a stored ExecPlan, then run Codex with that persisted decision.
6. Run repo automation for handoff states.
7. Post high-level progress back to Linear as a comment thread.

The orchestrator owns claims, running sessions, retries, and live telemetry.

## Repository Layout

- `main.go` - process entrypoint
- `internal/service/` - service startup, logging, workflow loading, and runtime wiring
- `internal/workflow/` - `WORKFLOW.md` loader and prompt rendering
- `internal/config/` - typed runtime config and validation
- `internal/tracker/` - tracker interface
- `internal/tracker/linear/` - Linear GraphQL adapter for issue reads, state writes, comment writes, and `paused` label management
- `internal/workspace/` - per-issue workspace lifecycle and hooks
- `internal/agent/codex/` - Codex app-server integration, transport, and protocol/event normalization
- `internal/automation/` - issue-run orchestration, workflow handoff policy, ExecPlan decisions, and merge-recovery automation
- `internal/orchestrator/` - dispatch, reconciliation, retries, loop protection, and observability state
- `internal/app/` - embedded HTTP dashboard, Funnel setup/readiness pages, and reserved webhook routes
- `internal/ui/` - gomponents-based HTML for the dashboard
- `internal/repoops/` - publish and merge automation via git and the GitHub API client

## Architecture Rules

- Keep workflow policy in `WORKFLOW.md` and `internal/config`, not scattered through the service.
- Keep tracker transport logic in `internal/tracker/linear` and scheduling logic in `internal/orchestrator`.
- Keep filesystem safety and workspace lifecycle concerns in `internal/workspace`.
- Keep Codex protocol handling in `internal/agent/codex` and workflow execution policy in `internal/automation`.
- Keep repo publish and merge behavior in `internal/repoops`.

## Contributor Notes

- Update this file when the service architecture, major package boundaries, or runtime state model changes.
- Keep this file consistent with `README.md`, `WORKFLOW.md`, and `AGENTS.md`.
