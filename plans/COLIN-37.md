# Add `colin setup` command and startup workflow state mapping resolution (COLIN-37)

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

`PLANS.md` is checked into this repository at `PLANS.md`, and this document must be maintained in accordance with PLANS.md.

## Purpose / Big Picture

Operators should be able to run `colin setup` once to ensure required Linear workflow states exist for Colin and then run the worker without hard-coded state-name assumptions. Colin should support team-specific state names via config and resolve those names at startup so runtime behavior is deterministic and fail-fast when misconfigured.

## Tracker Mapping

Workflow source: `WORKFLOW.md`.

Parent/Epic identifier: `COLIN-2`.

Child issue identifier in scope: `COLIN-37`.

## Progress

- [x] (2026-02-21 19:46Z) Created and moved tracker issue `COLIN-37` to `In Progress`.
- [x] (2026-02-21 19:58Z) Added `[workflow_states]` config model/defaults/validation in `internal/config` and updated `colin.toml.example`.
- [x] (2026-02-21 19:58Z) Added `internal/linear/workflow_state_admin.go` with setup/startup ensure/resolve flows and unit tests.
- [x] (2026-02-21 19:58Z) Added `colin setup` command (`cmd/setup.go`) and command tests; wired command into root CLI.
- [x] (2026-02-21 19:58Z) Integrated worker startup resolution and runtime state propagation through `cmd/worker.go`, `internal/worker/runner.go`, and `internal/workflow/decision.go`.
- [x] (2026-02-21 19:58Z) Updated Linear HTTP/in-memory clients to use runtime states for candidates, done checks, and state ID resolution cache.
- [x] (2026-02-21 19:58Z) Updated docs (`docs/operator-runbook.md`, `docs/troubleshooting.md`, `APP.md`) and validated with `go test ./...` plus live tagged e2e run.

## Surprises & Discoveries

- Observation: live e2e failed immediately after startup-resolution integration because the live team uses `Human Review` while default mapping expected `Review`.
  Evidence: `resolve workflow states: required workflow states not found: review="Review"; run 'colin setup' ...` from `go test -v -run TestLifecycleLiveExternalSystems -tags livee2e ./e2e`.
- Observation: live cleanup can race with already-archived/missing project records.
  Evidence: `archive project ... skipped: graphql error: Entity not found: ProjectUpdate` while test still passed.

## Decision Log

- Decision: Track this change under a new dedicated issue (`COLIN-37`) and plan file.
  Rationale: Scope spans CLI, config, runtime, and docs; dedicated tracking aligns with repo workflow requirements.
  Date/Author: 2026-02-21 / Codex
- Decision: Keep explicit mapping semantics with no implicit runtime aliases and fail-fast startup errors.
  Rationale: Prevent silent drift between configured and actual team workflow states; setup/startup should be deterministic and operator-visible.
  Date/Author: 2026-02-21 / Codex
- Decision: Feed resolved live-team state names into live e2e generated config `[workflow_states]`.
  Rationale: The live harness should exercise real team naming (for example `Human Review`) while still asserting deterministic behavior.
  Date/Author: 2026-02-21 / Codex

## Outcomes & Retrospective

Implemented `colin setup` and startup state resolution end-to-end. Worker/runtime paths now operate on resolved actual state names rather than hard-coded canonical names, and both HTTP and in-memory Linear clients consume runtime state sets for candidate filtering and done checks. `go test ./...` passed and live tagged e2e passed after wiring live config to resolved names.

## Context and Orientation

Current behavior hard-codes workflow names across `internal/workflow`, `internal/worker`, and `internal/linear`. `cmd/worker.go` loads config and starts the worker directly with `cfg.LinearTeamID`. `internal/linear/client.go` resolves state IDs lazily from Linear state names and currently includes alias behavior. There is no setup/admin command for provisioning Linear workflow states.

## Plan of Work

Implement config support for explicit workflow state mapping, add a Linear admin module to resolve team/state metadata and ensure missing states, add a new root `setup` command, and wire worker startup to resolve mappings to actual states before processing any issue. Then propagate resolved names and IDs through runner and Linear client logic, remove implicit alias assumptions from startup/setup resolution paths, and update docs/tests.

## Concrete Steps

Run from `/Users/pme/src/pmenglund/colin`:

1. Add config model updates in `internal/config` and update tests.
2. Add new `internal/linear/workflow_state_admin.go` and tests.
3. Add `cmd/setup.go` and command tests.
4. Update worker startup and runtime state usage across worker/linear/workflow.
5. Update docs and `colin.toml.example`.
6. Run `go test ./...` and live tagged command.

## Validation and Acceptance

Acceptance criteria:

1. `colin setup` creates missing required mapped states and validates existing state types.
2. Worker startup resolves configured mapping to actual Linear states and fails fast with actionable error when unresolved.
3. Runtime transitions and candidate filtering operate on resolved actual names, not hard-coded canonical names.
4. `colin.toml.example` documents `[workflow_states]`.
5. `go test ./...` passes.

## Idempotence and Recovery

`colin setup` should be idempotent: repeated runs validate existing states and create only missing ones. If setup fails due to type mismatch, operator can update mapping/config and re-run without destructive operations.

## Artifacts and Notes

Validation commands executed:

1. `go test ./...` (pass)
2. `go test -v -run TestLifecycleLiveExternalSystems -tags livee2e ./e2e` using `.envrc` values (first run failed on missing review mapping; second run passed after live config mapping update)

## Interfaces and Dependencies

Primary touched modules:

- `cmd/` for new CLI surface (`setup`) and startup resolution wiring.
- `internal/config` for workflow-state mapping config.
- `internal/linear` for state admin/resolution and runtime lookup data.
- `internal/workflow` and `internal/worker` for runtime state-driven transitions.
- `docs/` and `colin.toml.example` for operator guidance.

Revision Note (2026-02-21, Codex): Initial plan created for `COLIN-37`.
Revision Note (2026-02-21, Codex): Marked implementation complete, added decisions/discoveries, and recorded validation evidence including live tagged e2e run.
