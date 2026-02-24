# Reuse Review Context On Rework Retries

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

This document follows [PLANS.md](/Users/pme/src/pmenglund/colin/PLANS.md).

## Purpose / Big Picture

When a reviewer sends an issue from `Review` back to `Todo`, Colin currently starts a new Codex thread and only sends the issue description. That makes rework feel like a restart and loses reviewer context unless humans rewrite it into the description. After this change, Colin will reuse the prior Codex thread whenever possible and inject reviewer feedback comments into the next prompt so rework continues from the existing context.

## Tracker Mapping

Workflow: `WORKFLOW.md`. Issue: `COLIN-81`.

## Progress

- [x] (2026-02-24 00:00Z) Investigated current behavior and confirmed root cause (`StartThread` always used, comments not included in prompt).
- [x] (2026-02-24 00:00Z) Implemented `linear.Client.ListIssueComments` with HTTP + in-memory support and tests.
- [x] (2026-02-24 00:00Z) Implemented runner rework-feedback extraction/filtering and prompt augmentation (`## Review Feedback To Address`).
- [x] (2026-02-24 00:00Z) Implemented executor thread resume/fallback behavior and runner fallback note comments.
- [x] (2026-02-24 00:00Z) Updated docs and validated with `go test ./...`.

## Surprises & Discoveries

- Observation: `APP.md` currently lists `colin.codex_thread_id` and `colin.codex_session_id`, but runtime code uses `colin.thread_id` and branch git metadata `branch.<name>.colinSessionId`.
  Evidence: `APP.md` vs `internal/workflow/lease.go` and `internal/worker/branch_metadata.go`.

## Decision Log

- Decision: Treat reviewer feedback as all non-worker comments since the latest worker review transition comment.
  Rationale: It captures full requested rework without requiring manual copying or comment tagging.
  Date/Author: 2026-02-24 / Codex

- Decision: On thread resume failure, fallback to a new thread and leave a Linear note.
  Rationale: Keeps automation moving while preserving observability and auditability.
  Date/Author: 2026-02-24 / Codex

## Outcomes & Retrospective

Implemented and validated all planned work:
- Rework now reuses previous thread context when `colin.thread_id` can be resumed.
- Rework prompts now include reviewer comments since the last worker review marker.
- Worker-generated comments are filtered from rework prompt injection.
- Resume failures now fall back safely to a new thread and produce a deterministic Linear note.
- Full repository tests passed (`go test ./...`).

## Context and Orientation

`internal/worker/runner.go` orchestrates workflow transitions and calls the in-progress executor. `internal/codexexec/executor.go` runs Codex and currently always starts a new thread. `internal/linear/client.go` and `internal/linear/inmemory_client.go` are the real/fake Linear backends behind `linear.Client`. `internal/workflow/lease.go` defines metadata keys, including `colin.thread_id`.

## Plan of Work

Add issue comment listing support to the Linear client interface and both backends. Update runner in-progress processing to gather comments, derive human rework feedback since the latest worker review marker, and append that feedback block to the issue description sent to execution. Update Codex executor to resume from existing `colin.thread_id` first, then fallback to new thread when resume fails, and return resume diagnostics. Runner will post a one-time note when fallback occurs. Update docs and tests accordingly.

## Concrete Steps

From repository root `/Users/pme/src/pmenglund/colin`:

1. Edit linear types/client/backends/tests for issue comment listing.
2. Edit runner to augment issue description with review feedback and post fallback note.
3. Edit executor and tests for resume/fallback behavior.
4. Update docs (`docs/review-state-evidence.md`, `APP.md` metadata section).
5. Run:
   - `go test ./...`

## Validation and Acceptance

Acceptance is met when:
- Rework runs after `Review -> Todo` include reviewer comments in the prompt block.
- Existing `colin.thread_id` is resumed when valid.
- Resume failure still transitions work and posts a note indicating fallback.
- Existing state transitions and test suite remain green.

## Idempotence and Recovery

Changes are additive and safe to rerun. If resume metadata is stale, fallback behavior keeps workflow moving and records the failure reason in Linear comments for recovery/debugging.

## Artifacts and Notes

Implementation and test output will be added as this plan progresses.

## Interfaces and Dependencies

New/changed interfaces:
- `internal/linear.Client` adds:
  - `ListIssueComments(ctx context.Context, issueID string) ([]IssueComment, error)`
- `internal/linear.IssueComment` adds:
  - `ID string`
  - `Body string`
  - `CreatedAt time.Time`
- `internal/execution.InProgressExecutionResult` adds:
  - `ResumedFromThreadID string`
  - `ResumeFallbackReason string`

Change Note (2026-02-24): Initial plan added from approved user specification to track implementation progress in-repo.
Change Note (2026-02-24): Updated progress/outcomes after implementation completion and full test run.
