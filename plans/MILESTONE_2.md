# Automate In-Progress Task Execution with Codex Threads (Milestone 2)

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

`PLANS.md` is checked into this repository at `PLANS.md`, and this document must be maintained in accordance with it.

## Purpose / Big Picture

After this milestone, `colin` will no longer treat `In Progress` issues as passive metadata-driven transitions. Instead, for each eligible issue in `In Progress`, the worker will start a Codex session through `github.com/pmenglund/codex-sdk-go`, open a thread, and use that thread to decide whether the issue is sufficiently specified to execute without additional human input.

If an issue is not specified well enough, the worker will move it to `Refine` and add a Linear comment that explicitly states which inputs are missing. If an issue is sufficiently specified, the worker will execute the issue task through the Codex thread, then move the issue to `Human Review` and add a Linear comment summarizing the work performed. A user can observe this by running one worker cycle and inspecting issue state plus comments in Linear.

## Tracker Mapping

Workflow source: `WORKFLOW.md`.

Parent/Epic identifier: `MILESTONE_2` (tracked directly by this plan file because there is currently no separate Linear project/initiative for team `Colin`).

Child issue identifiers in scope: `COLIN-1`.

## Progress

- [x] (2026-02-11 05:28Z) Created `plans/MILESTONE_2.md` from the `PLANS.md` skeleton and mapped scope to the active `In Progress` issue.
- [x] (2026-02-11 05:36Z) Added `internal/codexexec` with a `codex-sdk-go` backed executor that starts Codex, opens a thread, requests structured output, and parses specification/execution results.
- [x] (2026-02-11 05:37Z) Extended `internal/worker/runner.go` with an `InProgressExecutor` path that gates `In Progress` issues by specification sufficiency and routes to `Refine` or `Human Review` with comments.
- [x] (2026-02-11 05:34Z) Added `CreateIssueComment` to `internal/linear.Client` and implemented `commentCreate` mutation support in `internal/linear/client.go`.
- [x] (2026-02-11 05:38Z) Added/updated tests in `internal/codexexec/executor_test.go`, `internal/worker/runner_test.go`, and `internal/linear/client_test.go`.
- [x] (2026-02-11 05:44Z) Added `docs/milestone2-codex-execution.md` and updated `APP.md` to reflect Codex execution architecture and runtime requirements.
- [x] (2026-02-11 05:40Z) Ran `go test ./...` successfully after implementation and fake regeneration.
- [x] (2026-02-11 05:42Z) Ran a live worker cycle using `CODEX_HOME=/tmp/codex-home go run . worker run --once`; Codex thread processed `COLIN-1`, classified it as underspecified, and the worker moved it to `Refine` with a comment listing missing inputs.
- [x] (2026-02-11 05:43Z) Updated this ExecPlan with final outcomes, decisions, and validation artifacts.
- [x] (2026-02-11 06:05Z) Added metadata-based idempotence markers for `In Progress` outcomes to prevent duplicate comments after partial failures (comment write succeeded, state transition conflicted).
- [x] (2026-02-11 06:06Z) Added retry regression test (`TestRunnerInProgressRetryAfterConflictDoesNotDuplicateComment`) and re-ran `go test ./...`.
- [x] (2026-02-11 06:06Z) Ran a post-fix live cycle (`CODEX_HOME=/tmp/codex-home go run . worker run --once`) confirming worker health (`count=0` with no runtime errors).

## Surprises & Discoveries

- Observation: The `Colin` team currently has one `In Progress` issue, `COLIN-1`, and its description contains only `colin` metadata without user-facing task details.
  Evidence: Linear issue payload for `COLIN-1` has description `<!-- colin:metadata ... -->` and no specification text.

- Observation: There is no Linear project/initiative currently attached to team `Colin`.
  Evidence: `list_projects(team="Colin")` returned an empty result set.

- Observation: Running Codex app-server with default home failed in this sandbox because the process could not write session files under `/Users/pme/.codex/sessions`.
  Evidence: `worker run --once` failed with `Codex cannot access session files at /Users/pme/.codex/sessions (permission denied)`.

- Observation: Sending `SandboxPolicy` as the simple string value in `TurnOptions` caused `turn/start` schema validation errors.
  Evidence: Codex returned `Invalid request: invalid type: string "workspace-write", expected internally tagged enum SandboxPolicy`.

- Observation: Setting `CODEX_HOME` to a writable directory and seeding it with existing Codex auth/config allowed thread creation and turn completion.
  Evidence: `CODEX_HOME=/tmp/codex-home go run . worker run --once` completed a turn and transitioned `COLIN-1`.

- Observation: The first Milestone 2 implementation could duplicate comments when metadata/comment writes succeeded but the final state update conflicted and retried.
  Evidence: `applyInProgressOutcome` wrote comment before state transition and had no persisted outcome marker to suppress retry comments.

## Decision Log

- Decision: Keep Milestone 2 scoped to the existing worker architecture by adding a Codex execution component that is invoked only from `In Progress` processing.
  Rationale: This preserves deterministic state handling for `Todo` and `Merge` while adding the minimum required side-effect boundary for Codex thread work.
  Date/Author: 2026-02-11 / Codex

- Decision: Treat specification sufficiency as an explicit, first step executed by Codex thread analysis before any task execution step.
  Rationale: This directly matches the workflow requirement that underspecified tasks move to `Refine` with actionable feedback.
  Date/Author: 2026-02-11 / Codex

- Decision: Keep `Todo` and `Merge` handling in `internal/workflow.Decide` and route only `In Progress` through the new executor branch in `internal/worker/runner.go`.
  Rationale: This keeps milestone 1 deterministic transition behavior stable while introducing Codex side effects only where required.
  Date/Author: 2026-02-11 / Codex

- Decision: Remove `TurnOptions.SandboxPolicy` usage and rely on thread-level defaults after discovering runtime schema mismatch for simple string policy values.
  Rationale: This unblocked real turn execution while still preserving the required Codex thread flow.
  Date/Author: 2026-02-11 / Codex

- Decision: Use `CODEX_HOME=/tmp/codex-home` for live validation in this sandbox.
  Rationale: The default home directory was not writable by spawned app-server in this environment, but `/tmp` is writable and supports session state.
  Date/Author: 2026-02-11 / Codex

- Decision: Persist outcome/comment fingerprint metadata (`colin.in_progress_outcome`, `colin.in_progress_comment_id`) and skip comment creation when they already match.
  Rationale: This preserves idempotence across retries when a previous attempt wrote metadata/comment but failed on state transition conflict.
  Date/Author: 2026-02-11 / Codex

## Outcomes & Retrospective

Milestone 2 is implemented for the targeted workflow behavior. `colin` now uses `github.com/pmenglund/codex-sdk-go` to start Codex and process `In Progress` issues through a thread before deciding whether to continue execution or request refinement.

Observed result for `COLIN-1`:

- Codex thread ran and determined the issue was underspecified.
- Worker transitioned the issue from `In Progress` to `Refine`.
- Worker posted a comment listing missing inputs (objective/problem statement, expected behavior, scope, acceptance criteria, and validation requirements).

This matches the requested behavior for underspecified tasks. The path for sufficiently specified tasks is implemented and covered by unit tests (`In Progress -> Human Review` with execution summary comment), but was not exercised against a live issue in this run because the only active issue was intentionally underspecified.

Post-implementation hardening added retry-safe comment idempotence for the `In Progress` execution path. This closes the gap where repeated cycles could post duplicate refine/review comments after a conflict on final state write.

## Context and Orientation

Milestone 1 established a deterministic Linear state worker with three automated candidate states (`Todo`, `In Progress`, `Merge`) and pure workflow decision logic in `internal/workflow`. In milestone 1, `In Progress` transitions were controlled by metadata flags (`colin.needs_refine`, `colin.ready_for_human_review`) rather than true execution.

For Milestone 2, the main change is that `In Progress` now represents active execution. In plain terms, “active execution” means the worker uses Codex to inspect the issue, decide whether the instructions are complete enough, and then either request refinement or complete a first-pass implementation. In this repository, that behavior belongs in `internal/worker`, while Linear API details remain in `internal/linear`, and pure transition constraints remain in `internal/workflow`.

The current key files are `internal/worker/runner.go` for polling and orchestration, `internal/linear/client.go` for GraphQL operations, `internal/workflow/decision.go` for deterministic transitions, `cmd/worker.go` for command wiring, and `internal/config/config.go` for runtime settings.

## Plan of Work

First, add a Codex adapter package that wraps `github.com/pmenglund/codex-sdk-go` behind an internal interface designed for testability. This package will expose one high-level operation for `In Progress` issues that returns a structured result with fields for `IsWellSpecified`, `NeedsInputSummary`, and `ExecutionSummary`. The adapter will start a Codex instance, create or reuse a thread, send the issue context, and parse a constrained response format that the worker can act on deterministically.

Second, extend `internal/worker/runner.go` so `processIssue` routes `In Progress` issues through a dedicated handler before running generic metadata/state logic. That handler will run the specification sufficiency check first. If insufficient, it will move the issue to `Refine` and post a comment that lists needed missing inputs. If sufficient, it will execute the task in Codex, move state to `Human Review`, and post a completion comment that summarizes what was done.

Third, extend `internal/linear/client.go` and `internal/linear/types.go` with a comment API (for example `CreateIssueComment`). The worker will use this API for both refine and human-review outcomes so issue history clearly records why the transition occurred.

Fourth, keep idempotence by recording execution markers in metadata. If the same issue/execution pair is retried, the worker should avoid duplicate comments and duplicate state changes. The exact metadata keys will be documented in `internal/workflow/lease.go` constants or a closely related file.

Fifth, add tests in `internal/worker/runner_test.go` for both `In Progress -> Refine` and `In Progress -> Human Review` branches with fake Codex and fake Linear clients. Update any workflow tests that assumed metadata-only `In Progress` transitions so they still validate allowed transitions without owning Codex side effects.

Finally, update docs that currently state Codex thread operations are out of scope for milestone 1 (`docs/milestone1-linear-state.md`) by adding milestone 2 notes or a new milestone 2 runbook that explains required environment and expected logs/comments.

## Concrete Steps

Run all commands from repository root: `/Users/pme/src/pmenglund/colin`.

1. Add dependency and inspect Codex SDK API.

    go get github.com/pmenglund/codex-sdk-go@latest
    go mod tidy

Expected result: `go.mod` and `go.sum` include `github.com/pmenglund/codex-sdk-go` and required transitive dependencies.

2. Implement Codex adapter and worker integration.

    go test ./internal/worker ./internal/linear ./internal/workflow ./cmd

Expected result: compile succeeds and tests for new In Progress flow pass.

3. Run full validation suite.

    go test ./...

Expected result: all package tests pass.

4. Execute one real reconciliation cycle.

    CODEX_HOME=/tmp/codex-home go run . worker run --once

Expected result: for the active `In Progress` issue, worker evaluates specification sufficiency via Codex and then either:

- moves issue to `Refine` and comments with missing information, or
- executes task and moves issue to `Human Review` with execution summary comment.

## Validation and Acceptance

Acceptance is behavior-focused.

The first acceptance condition is specification gating. For an `In Progress` issue with insufficient task details, the worker must move state to `Refine` and create a comment listing what is needed from a human.

The second acceptance condition is execution path. For an `In Progress` issue with sufficient details, the worker must execute the task through Codex thread operations and then move state to `Human Review` with a comment describing what was completed.

The third acceptance condition is deterministic orchestration under retries. Re-running `worker run --once` should not spam duplicate comments or toggle issue state back and forth for unchanged issue content.

The fourth acceptance condition is test coverage. Unit tests must verify both branches (`Refine` and `Human Review`) and keep existing `Todo` and `Merge` behavior intact.

## Idempotence and Recovery

This plan is safe to run repeatedly. If execution fails after posting a comment but before state update, the next run should re-check current state and avoid duplicate comment content using metadata execution markers. If execution fails before comment creation, rerunning should retry safely.

If Codex SDK invocation fails transiently, the worker should return an error for visibility and avoid partially applying state transitions. Operators can rerun the command after fixing environment or service issues.

If Linear write conflicts occur, existing conflict handling (`linear.ErrConflict`) remains the retry mechanism, and no destructive rollback is required.

## Artifacts and Notes

Proof snippets from this implementation:

    $ go test ./...
    ok   github.com/pmenglund/colin/cmd
    ok   github.com/pmenglund/colin/e2e
    ok   github.com/pmenglund/colin/internal/codexexec
    ok   github.com/pmenglund/colin/internal/config
    ok   github.com/pmenglund/colin/internal/linear
    ok   github.com/pmenglund/colin/internal/worker
    ok   github.com/pmenglund/colin/internal/workflow

    $ CODEX_HOME=/tmp/codex-home go run . worker run --once
    worker=pme action=issues_fetched execution_id=... count=1
    ... codex thread started ...
    ... codex turn completed ...
    execution_id=... issue=COLIN-1 action=transition to="Refine" reason="specification requires refinement"
    worker=pme action=cycle_complete execution_id=... processed=1 conflicts=0

    $ CODEX_HOME=/tmp/codex-home go run . worker run --once
    worker=pme action=issues_fetched execution_id=... count=0
    worker=pme action=cycle_complete execution_id=... processed=0 conflicts=0

Linear verification after run:

- Issue `COLIN-1` state is `Refine`.
- Issue comment was created with missing input requirements summary.

## Interfaces and Dependencies

Use `github.com/pmenglund/codex-sdk-go` for Codex lifecycle and thread operations.

In `internal/linear/client.go`, extend `Client` with a comment API:

    CreateIssueComment(ctx context.Context, issueID string, body string) error

In a new Codex integration package (for example `internal/codex`), define a worker-facing interface:

    type TaskExecutor interface {
        EvaluateAndExecute(ctx context.Context, issue linear.Issue) (Result, error)
    }

    type Result struct {
        IsWellSpecified   bool
        NeedsInputSummary string
        ExecutionSummary  string
    }

In `internal/worker/runner.go`, integrate this result as follows:

- `IsWellSpecified == false`: transition to `workflow.StateRefine` and comment `NeedsInputSummary`.
- `IsWellSpecified == true`: transition to `workflow.StateHumanReview` and comment `ExecutionSummary`.

Revision Note (2026-02-11, Codex): Added a post-implementation hardening update for retry idempotence on `In Progress` outcomes, including new metadata markers, regression tests, and a post-fix runtime verification cycle.
