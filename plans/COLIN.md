# Align Colin With the Symphony Service Specification

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

This document follows `PLANS.md` at the repository root and must be maintained in accordance with that file.

## Purpose / Big Picture

After this change, Colin will stop being primarily a hard-coded Linear workflow engine and become a repository-driven orchestration service that behaves like the system described in `SPEC.md`. An operator will be able to start Colin in a repository that contains a machine-readable `WORKFLOW.md`, and Colin will load runtime settings and the agent prompt from that file, poll Linear for eligible issues, create or reuse a per-issue workspace, launch a Codex app-server session with bounded concurrency, retry or cancel runs when the tracker state changes, and expose enough runtime status to debug active work. The result will be visible by starting the worker, watching structured logs describe dispatch and reconciliation, inspecting a runtime snapshot, and verifying that issues move through active and terminal eligibility exactly as the workflow contract says.

The implementation must preserve a safe migration path from the current Colin code. Today the repository already has a functioning polling worker, a Linear client, Git worktree bootstrap logic, merge execution, and Codex thread integration. What it does not have is the spec's central idea: `WORKFLOW.md` as the authoritative runtime contract, a single orchestrator-owned in-memory scheduling state, dynamic workflow reload, generic workspace lifecycle hooks, or live session telemetry. This plan closes those gaps without forcing a risky one-shot rewrite.

## Tracker Mapping

Workflow: `WORKFLOW.md`.

Epic: `COLIN-86` ("Align Colin with the Symphony service specification").

Child issues:

- `COLIN-87` implement repository-owned workflow loader and prompt rendering.
- `COLIN-88` replace static Colin config with workflow-derived runtime config and reload semantics.
- `COLIN-89` introduce orchestrator-owned runtime state, reconciliation, and retry scheduling.
- `COLIN-90` generalize workspace lifecycle management and hook execution.
- `COLIN-91` rework Codex runner integration for live session tracking and continuation turns.
- `COLIN-92` add observability surfaces, migration path, and conformance validation.

## Progress

- [x] (2026-03-11 17:08Z) Reviewed repository instructions in `AGENTS.md`, `APP.md`, `LANGUAGE.md`, `WORKFLOW.md`, and `PLANS.md`.
- [x] (2026-03-11 17:18Z) Audited `SPEC.md` against the current runtime in `cmd/worker.go`, `internal/config/config.go`, `internal/worker/runner.go`, `internal/worker/task_bootstrap.go`, `internal/codexexec/executor.go`, and `internal/linear/client.go`.
- [x] (2026-03-11 17:29Z) Created tracker issue `COLIN-86` and milestone child issues `COLIN-87` through `COLIN-92` so the migration work is explicitly tracked.
- [x] (2026-03-11 17:34Z) Authored this ExecPlan in `plans/COLIN.md`.
- [x] (2026-03-11 18:02Z) Implemented `COLIN-87`: added `internal/workflowfile` for `WORKFLOW.md` loading, front matter parsing, strict prompt rendering, and workflow-specific tests; converted `WORKFLOW.md` to include YAML front matter while preserving the existing prose body.
- [x] (2026-03-11 18:10Z) Implemented the first compatibility slice of `COLIN-88`: added `--workflow`, loaded workflow-derived config through `internal/config`, wired repo-owned prompt/workspace settings into `cmd` and `codexexec`, and added a last-known-good config provider with reload tests.
- [x] (2026-03-12 01:06Z) Completed `COLIN-88`: extended workflow-derived config with active/terminal states, hook settings, retry/time budget settings, Codex runtime config, and wired the reloadable provider into the new orchestrator tick loop.
- [x] (2026-03-12 01:22Z) Implemented `COLIN-89`: added `internal/orchestrator` with claim/running/retry ownership, per-issue backoff, startup cleanup, active-run reconciliation, stall detection, and runtime snapshots; switched `cmd/worker.go` to run through the orchestrator by default.
- [x] (2026-03-12 01:30Z) Implemented `COLIN-90`: added `internal/workspace` with sanitized workspace keys, hook execution, safe under-root deletes, startup terminal cleanup, and a git bootstrap compatibility populator backed by `internal/worker/task_bootstrap.go`.
- [x] (2026-03-12 01:37Z) Implemented `COLIN-91`: converted `internal/codexexec` to a streamed attempt API, emitted live `SessionUpdate` telemetry, supported continuation prompts on resumed threads, and added targeted streaming tests.
- [x] (2026-03-12 01:48Z) Implemented `COLIN-92`: added orchestrator/workspace/config/Linear conformance tests, updated `APP.md` and `WORKFLOW.md` to describe the workflow-file-first orchestrator model, and verified the repo with targeted package tests plus the full suite.

## Surprises & Discoveries

- Observation: The repository already has a file named `WORKFLOW.md`, but it currently documents contributor process rules rather than the runtime workflow contract described in `SPEC.md`.
  Evidence: `WORKFLOW.md` contains planning and Linear process guidance, while `SPEC.md` section 5 says `WORKFLOW.md` must hold YAML front matter and the per-issue prompt template.

- Observation: Current configuration is sourced from `colin.toml` plus environment variables, not from repository-owned workflow front matter.
  Evidence: `internal/config/config.go` defines `LoadFromPath`, TOML parsing via `github.com/pelletier/go-toml/v2`, and environment overrides such as `LINEAR_API_TOKEN`, `COLIN_POLL_EVERY`, and `COLIN_MAX_CONCURRENCY`.

- Observation: Current orchestration logic is based on Colin-owned Linear state transitions, not on an orchestrator-owned claim/retry model.
  Evidence: `internal/workflow/decision.go` decides `Todo -> In Progress -> Review/Refine -> Merge -> Merged -> Done`, and `internal/worker/runner.go` applies those transitions directly via `UpdateIssueState`.

- Observation: Current workspace management is a Git worktree bootstrapper tied to branch naming rules, not a generic workspace manager with hooks.
  Evidence: `internal/worker/task_bootstrap.go` always creates worktrees under `<COLIN_HOME>/worktrees/<issueIdentifier>` and branches named `colin/<issueIdentifier>`, and there is no hook execution package.

- Observation: Current Codex integration is a one-turn request/response wrapper with no live session event stream exposed to the worker.
  Evidence: `internal/codexexec/executor.go` builds a single prompt, calls `thread.RunInputs`, reads `turn.FinalResponse`, and returns a summary struct without streaming intermediate events or token updates into runtime state.

- Observation: The current worker already has some polling retry behavior, but it only retries the overall run loop after cycle failure rather than maintaining per-issue retry entries owned by the orchestrator.
  Evidence: `internal/worker/runner.go` uses `runRetryDelayForError` around `RunOnce` and does not keep `claimed` or `retry_attempts` maps.

- Observation: `SPEC.md` deliberately treats tracker writes as agent-owned workflow behavior, but Colin currently owns many tracker writes itself.
  Evidence: `internal/worker/runner.go` updates Linear state, metadata, and comments in multiple paths, including in-progress completion, recovery comments, and merge lifecycle changes.

- Observation: Keeping the existing prose body in `WORKFLOW.md` would make it a poor default Codex task prompt, so the compatibility path needs front matter to point at the existing prompt assets during migration.
  Evidence: the prose body describes contributor workflow rather than issue execution context, while `prompts/work.md` already contains task-specific placeholders such as `{{ LINEAR_ID }}` and `{{ LINEAR_DESCRIPTION }}`.

## Decision Log

- Decision: Implement the new architecture incrementally behind compatibility adapters instead of replacing the current worker in one step.
  Rationale: The repository already has working Linear, Git, and Codex integrations. Reusing those behind new interfaces keeps the migration testable and reduces the risk of losing existing behavior before the new orchestrator is stable.
  Date/Author: 2026-03-11 / Codex

- Decision: Keep the file name `WORKFLOW.md` and add machine-readable front matter to it rather than inventing a second runtime file.
  Rationale: `SPEC.md` makes `WORKFLOW.md` the canonical repository-owned contract. Keeping the file name avoids a permanent divergence from the specification. The existing prose can remain as prompt body and operator guidance, so this is a migration of format rather than a content loss.
  Date/Author: 2026-03-11 / Codex

- Decision: Preserve the current CLI entrypoint `colin worker run` during the migration, adding workflow-path selection and deprecation notices before removing TOML-centric paths.
  Rationale: Operators and tests already depend on the current Cobra commands. A stable CLI surface makes it possible to swap the runtime architecture underneath without breaking every integration at once.
  Date/Author: 2026-03-11 / Codex

- Decision: Treat Colin's Git worktree creation, branch metadata, merge execution, and GitHub operations as a Colin-specific workspace population and cleanup layer, not as the orchestrator's core state machine.
  Rationale: `SPEC.md` allows implementation-defined workspace population. This lets the orchestrator become generic while still preserving Colin's current repository automation capabilities.
  Date/Author: 2026-03-11 / Codex

- Decision: Move business workflow decisions about ticket progression out of `internal/workflow/decision.go` and into the repository-owned workflow prompt and tracker state eligibility rules.
  Rationale: The spec's main boundary is that the service schedules and reconciles work, while ticket semantics are typically carried by the agent prompt and tooling. Keeping Colin's current hard-coded state engine as the default would leave the implementation visibly non-conformant.
  Date/Author: 2026-03-11 / Codex

- Decision: Use `WORKFLOW.md` front matter to reference the existing `prompts/work.md` and `prompts/merge.md` assets during the migration instead of immediately replacing the `WORKFLOW.md` prose body with a new task prompt.
  Rationale: This preserves the human-facing contributor guidance already referenced by `AGENTS.md` while still making runtime configuration repository-owned and allowing strict prompt rendering support to land now.
  Date/Author: 2026-03-11 / Codex

- Decision: Keep the legacy `internal/worker.Runner` as a compatibility layer for tests and git-specific workflow behavior while routing `cmd/worker.go` through the new orchestrator.
  Rationale: This isolates the old deterministic transition engine instead of deleting it in one risky patch, while making the live CLI use the spec-aligned scheduler and runtime snapshot model by default.
  Date/Author: 2026-03-12 / Codex

- Decision: Set `agent.max_turns: 1` in this repository’s `WORKFLOW.md` front matter even though the generic runtime default is higher.
  Rationale: The orchestrator and Codex runner now support continuation attempts and resumed threads, but this repository’s current work prompt and compatibility workflow are still built around one decisive turn per dispatch. Keeping the repo workflow explicit avoids accidental multi-turn loops during migration.
  Date/Author: 2026-03-12 / Codex

## Outcomes & Retrospective

The migration is complete. Colin now boots from repository-owned workflow config, reloads that config on each orchestrator tick, owns `claimed`/`running`/`retry_attempts` in memory, manages safe per-issue workspaces through `internal/workspace`, and receives live streamed session updates from `internal/codexexec`.

The old deterministic worker and git-oriented task bootstrap logic still exist, but they are now compatibility adapters underneath the new runtime instead of the main scheduling model. The CLI path in `cmd/worker.go` uses the orchestrator by default, and the repository documents the new workflow-file-first architecture in `APP.md` and `WORKFLOW.md`.

## Context and Orientation

Colin is currently organized as a Go CLI with Cobra entrypoints in `cmd/`, configuration loading in `internal/config/`, workflow loading in `internal/workflowfile/`, Linear API access in `internal/linear/`, workspace lifecycle in `internal/workspace/`, orchestration in `internal/orchestrator/`, compatibility adapters in `internal/worker/`, and Codex session execution in `internal/codexexec/`. The runtime starts in [cmd/worker.go](/Users/pme/src/pmenglund/colin/cmd/worker.go), constructs the orchestrator from the reloadable config provider, workspace manager, tracker adapter, and Codex runner, then repeatedly reconciles and dispatches work from that in-memory state.

A few terms used in this plan are worth defining in plain language.

An "orchestrator" is the part of the program that owns the global runtime view of which issues are currently running, which ones are waiting for retry, and which ones are eligible to start. In `SPEC.md`, that orchestrator is the single authority for dispatch, reconciliation, and retry scheduling. Colin now implements that in `internal/orchestrator`, with `claimed`, `running`, and `retry_attempts` maps plus a runtime snapshot.

A "workflow contract" is the repository-owned file that tells Colin how to behave at runtime. In the spec, that file is `WORKFLOW.md`, with YAML front matter for typed settings and Markdown body for the prompt template sent to the coding agent. Colin does not have this today. The current [WORKFLOW.md](/Users/pme/src/pmenglund/colin/WORKFLOW.md) is human-readable process documentation, and the current [internal/config/config.go](/Users/pme/src/pmenglund/colin/internal/config/config.go) loads TOML and environment variables instead.

A "workspace" is the per-issue directory where the coding agent runs. Colin currently represents this as a Git worktree rooted under `COLIN_HOME/worktrees/<issueIdentifier>` in [internal/worker/task_bootstrap.go](/Users/pme/src/pmenglund/colin/internal/worker/task_bootstrap.go). The spec keeps the same user-visible idea, but makes workspace lifecycle a first-class component with sanitized identifiers, hooks, cleanup rules, and no mandatory Git assumption.

A "live session" is the in-memory record of an active Codex run: thread ID, turn ID, tokens, last event, timestamps, and whether the run is stalled or still healthy. Colin currently returns only the final turn output from [internal/codexexec/executor.go](/Users/pme/src/pmenglund/colin/internal/codexexec/executor.go). That is sufficient for the current review/refine workflow, but not for the continuous orchestration model in `SPEC.md`.

The current code paths that will be touched most heavily are:

- [cmd/worker.go](/Users/pme/src/pmenglund/colin/cmd/worker.go) for command startup, dependency wiring, and migration flags.
- [internal/config/config.go](/Users/pme/src/pmenglund/colin/internal/config/config.go) for replacement or reshaping into a workflow-derived runtime config layer.
- [internal/worker/runner.go](/Users/pme/src/pmenglund/colin/internal/worker/runner.go) for extraction of orchestration responsibilities into a dedicated package.
- [internal/workflow/decision.go](/Users/pme/src/pmenglund/colin/internal/workflow/decision.go) for retirement or narrowing once state-machine ownership moves out of the service.
- [internal/worker/task_bootstrap.go](/Users/pme/src/pmenglund/colin/internal/worker/task_bootstrap.go) and [internal/worker/git_ops.go](/Users/pme/src/pmenglund/colin/internal/worker/git_ops.go) for reuse behind a workspace manager.
- [internal/codexexec/executor.go](/Users/pme/src/pmenglund/colin/internal/codexexec/executor.go) for the new session-aware runner path.
- [internal/linear/client.go](/Users/pme/src/pmenglund/colin/internal/linear/client.go) and [internal/linear/types.go](/Users/pme/src/pmenglund/colin/internal/linear/types.go) for the spec's normalized issue fields and additional query shapes.
- [SPEC.md](/Users/pme/src/pmenglund/colin/SPEC.md) as the target behavior and conformance checklist.

## Plan of Work

The implementation will proceed in six milestones that map directly to `COLIN-87` through `COLIN-92`. Each milestone ends with observable behavior and passing tests so the migration can pause safely between pull requests.

### Milestone 1: Make `WORKFLOW.md` the runtime contract (`COLIN-87`)

This milestone introduces a new package, preferably `internal/workflowfile`, whose only job is to load `WORKFLOW.md`, parse optional YAML front matter, and preserve the remaining Markdown body as the prompt template. The loader must support the path resolution order defined in `SPEC.md`: an explicit workflow path from CLI/runtime settings first, then `WORKFLOW.md` in the current working directory. It must classify failures as missing-file, parse, or template problems instead of silently falling back to embedded prompts.

The most delicate part of this milestone is the repository file itself. The existing `WORKFLOW.md` cannot simply be deleted because `AGENTS.md` instructs contributors to follow it. The safe migration is to prepend YAML front matter to `WORKFLOW.md` while keeping the current prose body usable both as human instructions and as the starting prompt body. If the current prose is too process-heavy for the runtime prompt, split the human-only sections into a new markdown file and leave a concise runtime prompt body in `WORKFLOW.md`, then update `AGENTS.md` to point to the new location. That file move is part of this milestone if needed.

Prompt rendering should be strict. Use a minimal renderer that fails on missing variables. Prefer standard-library `text/template` with `Option("missingkey=error")` and a map-shaped data model whose top-level keys are exactly `issue` and `attempt`; if that proves insufficient for the prompt examples in this repository, add a Liquid-compatible library and document the decision in the plan. The current `internal/codexexec/executor.go` string replacement logic must be removed from the main path once this loader exists.

The milestone is accepted when Colin can load a fixture `WORKFLOW.md`, parse front matter and body, render a prompt using a normalized issue object, and fail predictably on missing workflow files and missing template keys.

### Milestone 2: Replace TOML-centric config with workflow-derived runtime config (`COLIN-88`)

This milestone creates the typed configuration layer described in sections 5 and 6 of `SPEC.md`. The new package can replace `internal/config` or live alongside it temporarily, but the resulting runtime object must expose typed values for tracker settings, polling cadence, workspace root, hook scripts and timeouts, agent concurrency, Codex launch settings, and optional server status settings.

The new config layer must read YAML front matter from `WORKFLOW.md`, apply environment-variable indirection for values such as `tracker.api_key` and `workspace.root`, normalize defaults, and validate only the fields needed for scheduling. It must also support dynamic reload. The simplest safe design is a provider with three responsibilities: read the file, compile the effective typed config and prompt template, and publish updates to subscribers only after validation succeeds. Invalid reloads must not crash the process; they should log an operator-visible error and keep the last good config active.

Because the current CLI already exposes `--config` and expects `colin.toml`, migration must be explicit. Add a `--workflow` flag in [cmd/root.go](/Users/pme/src/pmenglund/colin/cmd/root.go) and [cmd/worker.go](/Users/pme/src/pmenglund/colin/cmd/worker.go). Keep TOML loading only as a legacy compatibility path during this milestone, and emit a clear deprecation warning when it is used. The worker should prefer `WORKFLOW.md` when both are present. The legacy TOML path should be deleted only after all runtime dependencies have moved to the workflow-derived config.

The milestone is accepted when a temporary `WORKFLOW.md` fixture can drive polling interval, concurrency, workspace root, and Codex launch settings without reading `colin.toml`, and when an on-disk workflow edit changes future dispatch behavior without restarting the process.

### Milestone 3: Introduce a real orchestrator (`COLIN-89`)

This milestone is the architectural center of the migration. Create a new package, preferably `internal/orchestrator`, that owns the runtime state described in sections 7 and 8 of `SPEC.md`: current poll interval, `running`, `claimed`, `retry_attempts`, completed bookkeeping, aggregate token counts, and rate-limit snapshots. The current `worker.Runner` should become either a thin compatibility wrapper around the orchestrator or be retired entirely.

The orchestrator must reconcile before dispatch on every tick. It must fetch active candidates using the workflow-configured active states, skip issues already claimed or running, sort by priority then age then identifier, and dispatch only while concurrency slots remain. It must also refresh active runs by tracker state, cancel runs that are no longer active, clean up terminal issue workspaces, and queue retries with the spec's fixed continuation delay or exponential failure backoff.

This is also the milestone where Colin must stop using `internal/workflow/decision.go` as the main source of truth for issue progression. That package can remain temporarily for legacy mode, but the default path must no longer hard-code `Todo -> In Progress -> Review -> Merge -> Done`. Instead, the service should treat the tracker's active and terminal states as configuration and allow the coding agent prompt or external tooling to own ticket writes. Any remaining service-driven state writes must be explicitly documented as a Colin compatibility profile rather than the default behavior.

The Linear client must grow the read shapes required by the orchestrator: list active candidates, fetch current states for a set of running IDs, and fetch terminal issues on startup cleanup. Extend [internal/linear/types.go](/Users/pme/src/pmenglund/colin/internal/linear/types.go) so the normalized issue model includes `priority`, `branch_name`, `url`, `labels`, structured blockers, and timestamps rather than only the current subset.

The milestone is accepted when a fake backend test can prove all of the following: only one orchestrator claim exists per issue, clean worker exits schedule a short continuation retry, failures back off per issue rather than globally, reconciliation cancels a running issue when its tracker state leaves the active set, and terminal issues are released and cleaned up.

### Milestone 4: Generalize workspace management (`COLIN-90`)

This milestone introduces `internal/workspace` as a first-class package. The manager must sanitize issue identifiers into safe workspace keys, build `<workspace.root>/<workspace_key>` paths, ensure directories exist, run `after_create`, `before_run`, `after_run`, and `before_remove` hooks with timeouts, and enforce that all managed paths stay under the configured root. Those are the safety invariants in section 9 of `SPEC.md`.

Colin's current Git worktree behavior should move behind this manager instead of remaining inside the orchestrator. The safest approach is to define a workspace population adapter that can prepare a newly created workspace from the current repository using the existing code in [internal/worker/task_bootstrap.go](/Users/pme/src/pmenglund/colin/internal/worker/task_bootstrap.go) and [internal/worker/git_ops.go](/Users/pme/src/pmenglund/colin/internal/worker/git_ops.go). That keeps Git-specific logic available, but the orchestrator will no longer assume all workspaces are Git worktrees by construction.

Startup cleanup belongs here. When the orchestrator starts, it should ask the tracker client for terminal issues and remove their workspace directories safely, running `before_remove` if configured and ignoring hook failures after logging them. This cleanup must be idempotent so repeated starts do not drift the filesystem.

The milestone is accepted when workspace creation, reuse, hook execution, and terminal cleanup are all covered by deterministic tests, and when existing Colin Git worktree behavior still functions through the new manager.

### Milestone 5: Rework Codex execution into a session-aware runner (`COLIN-91`)

This milestone replaces the current one-turn `EvaluateAndExecute` model with a runner that understands attempts, continuation turns, cancellation, and live telemetry. The package can remain `internal/codexexec`, but it needs new types. Define a request type that includes the normalized issue, the rendered prompt, the workspace path, and the current attempt count. Define an event sink or callback interface that the runner uses to publish live session updates to the orchestrator: thread ID, turn ID, PID if available, last event kind, timestamps, human-readable summaries, and token counts.

Before writing much code, perform a small proof-of-concept against `github.com/pmenglund/codex-sdk-go` to determine whether the current SDK exposes sufficient streaming information for section 10 of `SPEC.md`. If it does, keep the dependency and add an adapter. If it does not, implement a small direct app-server client against stdio for the fields the orchestrator needs. Record the result in the decision log before moving further.

The runner must support the spec's continuation semantics. The first turn on a fresh session sends the full rendered prompt. Additional turns on the same live session must send only continuation guidance and rely on thread history instead of repeating the original prompt. The orchestrator, not the runner, decides whether another turn is allowed, whether a clean exit should schedule a continuation retry, and whether a stalled session should be terminated. Existing evidence URL validation and Git commit logic from [internal/codexexec/executor.go](/Users/pme/src/pmenglund/colin/internal/codexexec/executor.go) can stay, but they need to move into the new attempt lifecycle.

The milestone is accepted when fake Codex tests can show live event propagation into orchestrator state, continuation turns on the same thread, cancellation on reconciliation, stall timeout behavior, and clear mapping of failures into `Succeeded`, `Failed`, `TimedOut`, `Stalled`, or `CanceledByReconciliation`.

### Milestone 6: Finish observability, migration cleanup, and conformance validation (`COLIN-92`)

This final milestone hardens the system into a spec-conformant service. Add structured logs that clearly distinguish poll ticks, dispatches, retries, session updates, cancellations, and cleanup. Add a runtime snapshot interface that can be consumed by tests and optionally exposed through a lightweight HTTP server extension if the repository still wants that path. The HTTP server is optional in `SPEC.md`, so it should not block conformance if the status snapshot already exists and is accessible through tests or CLI output.

This milestone also finishes the migration away from legacy Colin behavior. Remove or isolate the TOML-only config path. Narrow `internal/workflow` so it no longer pretends to be the main runtime decision engine. Update `APP.md`, `WORKFLOW.md`, and operator-facing docs so they describe the new architecture instead of the old state-driven worker. Expand the end-to-end tests so they prove the final operator-visible story: start Colin in a repository with a configured `WORKFLOW.md`, observe issue dispatch, inspect workspace creation, watch a live session appear in status output, and verify cleanup and retry behavior.

The milestone is accepted when `go test ./...` passes with the new packages and end-to-end scenarios, the docs describe the workflow-file-first model, and the remaining legacy paths are either removed or clearly marked as compatibility-only.

## Concrete Steps

All commands in this plan are run from `/Users/pme/src/pmenglund/colin` unless stated otherwise.

To refresh the current baseline before implementation, run:

    sed -n '1,260p' SPEC.md
    sed -n '1,260p' WORKFLOW.md
    sed -n '1,240p' cmd/worker.go
    sed -n '1,260p' internal/config/config.go
    sed -n '1,260p' internal/worker/runner.go
    sed -n '1,260p' internal/codexexec/executor.go
    go test ./...

Expected result today: tests pass, `WORKFLOW.md` is prose-only, and the inspected Go files show TOML-based config, deterministic Linear state transitions, Git worktree bootstrap, and a one-turn Codex wrapper.

During `COLIN-87`, create fixture workflow files and run targeted tests:

    go test ./internal/... -run 'Workflow|Prompt'

Expected result after the milestone: tests prove front matter parsing, prompt rendering, missing-key failures, and file-path precedence.

During `COLIN-88`, validate reload behavior with a temporary workflow file:

    go test ./internal/... -run 'Config|Reload'

Expected result after the milestone: tests prove typed config derivation, environment indirection, and last-known-good fallback on invalid reload.

During `COLIN-89`, validate dispatch and reconciliation behavior:

    go test ./internal/... -run 'Orchestrator|Retry|Reconcile'

Expected result after the milestone: tests prove claimed/running/retry ownership, per-issue backoff, continuation retries, and cancellation when tracker state changes.

During `COLIN-90`, validate workspace behavior:

    go test ./internal/... -run 'Workspace|Hook|Cleanup'

Expected result after the milestone: tests prove workspace path sanitization, hook timeout handling, and terminal cleanup safety.

During `COLIN-91`, validate Codex runner behavior:

    go test ./internal/... -run 'Codex|Session|Continuation|Stall'

Expected result after the milestone: tests prove live session updates, continuation turns, timeout mapping, and cancellation.

At the end of `COLIN-92`, run the full suite and an observable local scenario:

    go test ./...
    go run . worker run --once --workflow ./WORKFLOW.md

Expected result after the full migration: the test suite passes, the worker loads workflow front matter from `WORKFLOW.md`, logs a dispatch/reconciliation cycle using the new orchestrator, and no longer depends on `colin.toml` for the main runtime path.

## Validation and Acceptance

The final system is acceptable only if a human can observe the behavior promised by `SPEC.md`.

First, a repository-owned `WORKFLOW.md` must be enough to start Colin. A test fixture containing tracker settings, polling interval, workspace root, agent limits, Codex launch settings, and prompt body must produce a valid runtime config without a TOML file. Editing that workflow file while the worker is running must change future dispatch behavior without restarting the process, and an invalid edit must leave the previous good config active while emitting an error.

Second, orchestration must become tracker-driven rather than hard-coded-state-driven. With a fake Linear backend, Colin must claim each issue only once, skip blocked `Todo` work, stop active sessions whose tracker state leaves the active set, and schedule retries per issue rather than globally. Clean exits must schedule the short continuation delay required by `SPEC.md`.

Third, workspace management must be visibly safe. Workspaces must be created under the configured root with sanitized names, hooks must run with timeouts in the workspace directory, and startup terminal cleanup must remove only the intended issue workspace directories.

Fourth, Codex execution must be visible while it is running. The orchestrator must retain live session state that includes thread identity, turn identity, last event timing, and token counts. A stalled or canceled run must be distinguishable from a successful run in both logs and test assertions.

Fifth, the repository must prove conformance through tests. At minimum, add focused unit tests for workflow loading, config derivation and reload, orchestrator scheduling, workspace safety, Linear normalization, and Codex runner behavior, plus an end-to-end fake-backend test that exercises the full stack from `WORKFLOW.md` to workspace creation to session completion. `go test ./...` must pass before the plan can be considered complete.

## Idempotence and Recovery

Each milestone in this plan is designed to be additive and restartable. If implementation stops mid-migration, rerun the targeted test package for that milestone, then continue. The workflow-loader and config-provider milestones are naturally idempotent because they operate on read-only files plus in-memory state. The orchestrator milestone is safe to repeat because the authoritative state is in memory and can be rebuilt from the tracker and filesystem on restart.

Workspace operations are the only filesystem-destructive area. All workspace deletes must remain scoped under the configured workspace root, and terminal cleanup must continue to ignore missing directories so it can be safely retried. Hook failures in `after_run` and `before_remove` must be logged and ignored as the spec requires, which makes reruns safe after partial failures.

During the migration period, keep the legacy TOML path and old runner available until the new workflow-driven path passes the same tests. If a regression appears after cutover, temporarily switch the CLI back to the compatibility path behind a guarded flag while fixing the new implementation. Do not delete the compatibility path until `COLIN-92` proves the new stack end to end.

## Artifacts and Notes

Current-state evidence collected while writing this plan:

- `git status --short` showed `?? SPEC.md`, confirming the specification was newly added in this working tree.
- `WORKFLOW.md` currently contains process guidance rather than YAML front matter.
- `internal/config/config.go` currently loads TOML and env vars such as `COLIN_POLL_EVERY` and `COLIN_MAX_CONCURRENCY`.
- `internal/worker/runner.go` currently performs direct Linear state changes and uses cycle-level retry handling.
- `internal/worker/task_bootstrap.go` currently hard-codes worktree and branch layout under `COLIN_HOME`.
- `internal/codexexec/executor.go` currently sends one full prompt to Codex, waits for `turn.FinalResponse`, and returns a final summary without streaming live events.

Expected new or heavily changed files by the end of the plan:

- `WORKFLOW.md` with YAML front matter and prompt body that matches the runtime contract.
- `internal/workflowfile/loader.go` and tests for parsing and rendering.
- `internal/config/` replacement or reshaping for workflow-derived typed config and reload.
- `internal/orchestrator/` for runtime state, reconciliation, scheduling, and snapshots.
- `internal/workspace/` for workspace lifecycle and hooks.
- `internal/codexexec/` additions for session-aware execution and telemetry.
- `internal/linear/` updates for normalized issue fields and additional query methods.
- `cmd/worker.go` and related CLI files for workflow-path selection and runtime wiring.
- Updated docs in `APP.md` and any operator-facing documentation that still describes the old model.

## Interfaces and Dependencies

The end state should expose interfaces that make the new layers explicit.

In `internal/workflowfile`, define a loader and renderer roughly equivalent to:

    type Definition struct {
        Path           string
        PromptTemplate string
        Config         map[string]any
    }

    func Load(path string) (Definition, error)
    func Render(def Definition, issue map[string]any, attempt *int) (string, error)

In the new typed config layer, define a workflow-derived runtime object and provider:

    type RuntimeConfig struct {
        Tracker   TrackerConfig
        Polling   PollingConfig
        Workspace WorkspaceConfig
        Hooks     HookConfig
        Agent     AgentConfig
        Codex     CodexConfig
        Server    ServerConfig
    }

    type Provider interface {
        Current() RuntimeConfig
        CurrentDefinition() workflowfile.Definition
        Reload(ctx context.Context) error
        Updates() <-chan RuntimeConfig
    }

In `internal/orchestrator`, define the service and snapshot types that own scheduling state:

    type Service struct { ... }

    type Snapshot struct {
        PollIntervalMS       int64
        MaxConcurrentAgents  int
        Running              map[string]RunningEntry
        Claimed              map[string]struct{}
        RetryAttempts        map[string]RetryEntry
        Completed            map[string]struct{}
        CodexTotals          Totals
        CodexRateLimits      RateLimitSnapshot
    }

    func (s *Service) Run(ctx context.Context) error
    func (s *Service) Snapshot() Snapshot

In `internal/workspace`, define a manager that is generic enough for hooks and cleanup but can still host Colin's Git-specific behavior:

    type Workspace struct {
        Path         string
        WorkspaceKey string
        CreatedNow   bool
    }

    type Manager interface {
        Ensure(ctx context.Context, issueIdentifier string) (Workspace, error)
        BeforeRun(ctx context.Context, ws Workspace) error
        AfterRun(ctx context.Context, ws Workspace, attemptErr error) error
        Remove(ctx context.Context, ws Workspace) error
        CleanupTerminal(ctx context.Context, issueIdentifiers []string) error
    }

In `internal/codexexec`, define a runner that emits live session updates instead of only returning a final response:

    type AttemptRequest struct {
        Issue         linear.Issue
        Prompt        string
        WorkspacePath string
        Attempt       *int
        Continuation  bool
    }

    type EventSink interface {
        OnSessionUpdate(SessionUpdate)
    }

    type Runner interface {
        RunAttempt(ctx context.Context, req AttemptRequest, sink EventSink) (AttemptResult, error)
    }

The Linear integration in `internal/linear` must support the read patterns the orchestrator requires. At minimum, extend the client interface so it can list active candidates, fetch specific issues by ID for reconciliation, and list terminal issues for startup cleanup. Keep the existing HTTP and in-memory fake backends in lockstep so every orchestration rule can be tested locally.

Revision note (2026-03-11): Initial plan authored after reviewing `SPEC.md`, current Colin implementation, repository instructions, and tracker state; created `COLIN-86` through `COLIN-92` to map the migration into executable milestones.
Revision note (2026-03-11): Updated progress after landing `internal/workflowfile`, workflow-derived config loading, `--workflow` CLI support, workflow-backed prompt integration, and the last-known-good config provider; full `go test ./...` passed after these changes.
