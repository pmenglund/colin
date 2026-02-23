# GitHub + Linear Merge Lifecycle With New `Merged` State (COLIN-79)

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

`PLANS.md` is checked into this repository at `PLANS.md`, and this document must be maintained in accordance with it.

## Purpose / Big Picture

After this change, Colin will model merge completion in two explicit steps:

1. `Merge -> Merged` means the pull request merged in GitHub.
2. `Merged -> Done` means local git/worktree cleanup succeeded.

This matches the Linear workflow and pull-request automations configured by the operator and avoids `Done` churn when merge happened but cleanup has not.

## Tracker Mapping

Workflow source: `WORKFLOW.md`.

Parent/Epic identifier: `COLIN-79`.

Child identifiers in scope: `COLIN-79`.

## Progress

- [x] (2026-02-23 00:00Z) Captured implementation scope and decision-complete behavior from planning discussion.
- [x] (2026-02-23 01:05Z) Implemented state/config/setup plumbing for `Merged` (`internal/workflow`, `internal/config`, `cmd/setup`, `internal/linear/workflow_state_admin`, `colin.toml.example`).
- [x] (2026-02-23 01:30Z) Implemented workflow/runner/merge execution behavior for `Merge -> Merged -> Done`, including explicit recovery targets and pending sentinel handling.
- [x] (2026-02-23 01:42Z) Added/updated tests for state transitions, setup resolution, e2e lifecycle coverage, and merge recovery target selection.
- [x] (2026-02-23 01:55Z) Updated docs and diagram source for the new lifecycle (`docs/README.md`, runbook, metadata, evidence, usage, getting-started, troubleshooting, lifecycle note, `docs/colin.d2`).
- [x] (2026-02-23 02:00Z) Validated with `go test ./...`.

## Surprises & Discoveries

- Observation: Runtime currently models merge as a single `Merge -> Done` transition and does not include a `Merged` state.
  Evidence: `internal/workflow/decision.go` and `internal/workflow/states.go`.

- Observation: Serializing both `Merge` and `Merged` states means two issues in queue complete over four cycles (`Merge`/`Merged` per issue), not two.
  Evidence: Updated assertions in `e2e/lifecycle_happy_path_test.go` and `internal/worker/runner_test.go`.

## Decision Log

- Decision: Keep Linear PR automations as primary state drivers, with Colin fallback handling when automation misses.
  Rationale: Linear already has native PR automation and should remain source of workflow event updates.
  Date/Author: 2026-02-23 / Codex

- Decision: Reopen `Done` issues conditionally to `Merge` (not merged) or `Merged` (merged but not cleaned).
  Rationale: Distinguishes unresolved merge from cleanup-only recovery and aligns operator expectations.
  Date/Author: 2026-02-23 / Codex

- Decision: Keep `GitMergeExecutor.ExecuteMerge` backward-compatible for callers/tests that omit `Issue.StateName`, while runner-driven execution uses phase-aware state behavior.
  Rationale: Avoids unnecessary churn in direct executor tests while preserving runtime two-phase lifecycle.
  Date/Author: 2026-02-23 / Codex

## Outcomes & Retrospective

Implemented end-to-end.

What changed:

- Added first-class `Merged` workflow state across runtime state modeling, config mapping, setup, and workflow-state admin resolution.
- Changed deterministic decisions from `Merge -> Done` to `Merge -> Merged`, and added `Merged -> Done`.
- Updated runner merge orchestration to execute merge side effects for both phases and to treat `ErrMergePending` as non-fatal/no-transition.
- Introduced explicit done-recovery targets (`Merge` vs `Merged`) and updated recovery comments/transition behavior accordingly.
- Added `colin.pr_url` metadata key and optional `## Pull Request` section in review comments.
- Updated e2e/unit coverage and operator docs to reflect the two-phase lifecycle.

Result:

- Runtime lifecycle is now `Review -> Merge -> Merged -> Done`.
- Merge and cleanup retries are separated by state.
- Full test suite passes.

## Context and Orientation

Key modules:

- `internal/workflow/`: canonical states, allowed transitions, deterministic decisions.
- `internal/config/`: TOML/env configuration and runtime workflow-state mapping.
- `internal/linear/workflow_state_admin.go`: setup/startup resolution and creation of workflow states in Linear.
- `internal/worker/runner.go`: orchestration and side effects for state transitions, including merge execution and done-state recovery.
- `internal/worker/merge_executor.go`: git-backed merge and cleanup operations.

## Plan of Work

First, add `Merged` to workflow state models (`workflow`, `config`, setup/state admin). Second, update deterministic decisions to insert `Merged` between `Merge` and `Done`. Third, update runner and merge executor contracts so merge-side effects are phase-aware and done-recovery targets can be `Merge` or `Merged`. Fourth, update tests and docs to match the new lifecycle. Finally, run full test validation.

## Concrete Steps

From repository root:

1. Implement state/config/setup updates and tests.
2. Implement workflow/runner/merge executor updates and tests.
3. Update docs.
4. Run:

    go test ./...

## Validation and Acceptance

Acceptance criteria:

- Colin recognizes `Merged` as a first-class state in config, setup, and runtime.
- State progression is `Review -> Merge -> Merged -> Done`.
- Done recovery chooses `Merge` vs `Merged` based on merged status.
- Updated tests pass and full test suite is green.

## Idempotence and Recovery

The lifecycle remains retry-safe:

- Issues can remain in `Merge` while merge is pending.
- Issues can remain in `Merged` while cleanup retries.
- Recovery from `Done` is deterministic and non-destructive.

## Artifacts and Notes

Validation transcript:

    $ go test ./...
    ?   	github.com/pmenglund/colin	[no test files]
    ok  	github.com/pmenglund/colin/cmd
    ok  	github.com/pmenglund/colin/e2e
    ok  	github.com/pmenglund/colin/internal/codexexec
    ok  	github.com/pmenglund/colin/internal/config
    ?   	github.com/pmenglund/colin/internal/execution	[no test files]
    ok  	github.com/pmenglund/colin/internal/linear
    ?   	github.com/pmenglund/colin/internal/linear/fakes	[no test files]
    ok  	github.com/pmenglund/colin/internal/logging
    ok  	github.com/pmenglund/colin/internal/worker
    ok  	github.com/pmenglund/colin/internal/workflow
    ?   	github.com/pmenglund/colin/prompts	[no test files]

## Interfaces and Dependencies

Planned internal interface changes:

- Extend `workflow.States` with `Merged`.
- Extend `config.WorkflowStates` with `Merged`.
- Extend merge recovery probe contract to return an explicit recovery target (`Merge` or `Merged`).
- Add metadata key `colin.pr_url` for review comment rendering and diagnostics.

Revision Note (2026-02-23, Codex): Created initial ExecPlan from approved implementation plan before coding.
Revision Note (2026-02-23, Codex): Implemented `Merged` two-phase lifecycle and updated tests/docs to match runtime behavior.
