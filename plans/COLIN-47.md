# Move Colin Metadata Persistence to Linear Attachments (COLIN-47)

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

`PLANS.md` is checked into this repository at `PLANS.md`, and this document must be maintained in accordance with it.

## Purpose / Big Picture

After this change, Colin no longer stores workflow metadata in `<!-- colin:metadata ... -->` blocks embedded in Linear issue descriptions. Instead, Colin reads and writes metadata on a deterministic per-issue Linear attachment (`colin://issue/<issue-id>/metadata`). This keeps issue descriptions clean and makes metadata persistence explicit and structured.

## Tracker Mapping

Workflow source: `WORKFLOW.md`.

Parent/Epic identifier: `COLIN-47`.

Child identifiers in scope: `COLIN-47`.

## Progress

- [x] (2026-02-21 20:50Z) Created Linear tracking issue `COLIN-47` for metadata persistence migration.
- [x] (2026-02-21 20:56Z) Created this ExecPlan and mapped it to tracker `COLIN-47`.
- [x] (2026-02-21 20:59Z) Replaced description-block metadata merge logic with pure metadata map patch helpers in `internal/linear/types.go`.
- [x] (2026-02-21 21:07Z) Migrated HTTP Linear client metadata read/write path to attachment-backed GraphQL queries/mutations in `internal/linear/client.go`.
- [x] (2026-02-21 21:08Z) Updated in-memory Linear client metadata updates to mutate only `Issue.Metadata`.
- [x] (2026-02-21 21:18Z) Reworked `internal/linear/client_test.go` and `internal/linear/inmemory_client_test.go` for attachment-backed metadata behavior.
- [x] (2026-02-21 21:24Z) Updated live e2e helpers/tests (`e2e/live_linear_admin_test.go`, `e2e/lifecycle_live_external_test.go`) to use attachment metadata helpers.
- [x] (2026-02-21 21:27Z) Updated architecture and operator docs (`APP.md`, `docs/operator-runbook.md`, `docs/troubleshooting.md`) for attachment-backed metadata.
- [x] (2026-02-21 20:56Z) Ran full validation (`go test ./...`) successfully.
- [x] (2026-02-21 20:56Z) Finalized this ExecPlan with outcomes and validation evidence.
- [x] (2026-02-21 21:03Z) Added `docs/metadata.md` and switched attachment URL to the repository metadata doc URL across runtime/live helpers/docs.

## Surprises & Discoveries

- Observation: Existing tests in `internal/linear/client_test.go` were tightly coupled to issue-description mutation (`issueUpdate` on description), so migration required broad test rewrites rather than small edits.
  Evidence: Prior tests asserted presence of `colin:metadata` text and `UpdateIssueDescription` mutation behavior.

## Decision Log

- Decision: Keep `linear.Client` interface unchanged and migrate only persistence internals.
  Rationale: Worker and workflow orchestration code should remain stable; migration is transport-layer behavior.
  Date/Author: 2026-02-21 / Codex

- Decision: Use deterministic attachment URL `colin://issue/<issue-id>/metadata` and `attachmentCreate` for idempotent upsert.
  Rationale: URL-keyed upsert avoids attachment proliferation and preserves simple read path.
  Date/Author: 2026-02-21 / Codex

- Decision: Perform strict cutover with no legacy description metadata read fallback and no migration helper.
  Rationale: Matches requested rollout policy and avoids dual-source ambiguity.
  Date/Author: 2026-02-21 / Codex

- Decision: Use `https://github.com/pmenglund/colin/blob/main/docs/metadata.md` as the attachment URL for Colin metadata records.
  Rationale: Requested to align metadata attachment URL with human-readable metadata documentation.
  Date/Author: 2026-02-21 / Codex

## Outcomes & Retrospective

`COLIN-47` is implemented as specified. Metadata persistence for worker runtime logic now uses Linear attachment metadata keyed by deterministic URL (`colin://issue/<issue-id>/metadata`) rather than description-embedded metadata comments.

The `linear.Client` interface remains unchanged, worker logic remains unchanged, and metadata value shape remains `map[string]string`. HTTP client metadata reads/writes are attachment-backed, and the in-memory client now mutates only `Issue.Metadata` on metadata updates. Live e2e admin/test helpers and operator docs were updated accordingly.

Validation succeeded via full test suite (`go test ./...`).

## Context and Orientation

Current metadata keys used by workflow logic (leases, heartbeats, merge flags, review flags, workspace pointers) are string keys in `internal/workflow/lease.go`. Worker orchestration consumes `Issue.Metadata` via `linear.Client` in `internal/worker/runner.go` and is intentionally decoupled from storage representation.

Before this change, `internal/linear/types.go` parsed and rewrote metadata blocks inside issue descriptions, and both HTTP and in-memory clients relied on that behavior. This plan migrates those code paths to attachment-backed metadata while preserving the `Issue.Metadata map[string]string` contract.

## Plan of Work

First, replace metadata block manipulation helpers in `internal/linear/types.go` with pure map patch helpers (`applyMetadataPatch`, map clone/equality helpers) and keep only `StripMetadataBlock` for prompt cleanup compatibility.

Second, update `internal/linear/client.go` to read metadata from `attachmentsForURL(url: ...)` and write metadata through `attachmentCreate(input: ...)` using deterministic per-issue URL and static title/subtitle. Update conflict detection to compare observed vs current metadata maps instead of descriptions.

Third, update `internal/linear/inmemory_client.go` so metadata updates only mutate `Issue.Metadata`, leaving issue descriptions untouched.

Fourth, update tests in `internal/linear/client_test.go` and `internal/linear/inmemory_client_test.go` to validate attachment-backed metadata behavior and keep state-transition behavior stable.

Fifth, update live e2e helpers/tests to seed/read metadata through attachment helpers instead of embedding metadata blocks in descriptions.

Finally, update operator-facing docs and architecture notes to reflect attachment storage and strict cutover expectations.

## Concrete Steps

Run from repository root: `/Users/pme/src/pmenglund/colin`.

1. Implement metadata helper and client changes.

    go test ./internal/linear

2. Update live e2e helpers and docs.

    go test ./...

3. Validate full suite and capture outcomes.

    go test ./...

Expected result: all tests pass and metadata persistence no longer depends on issue-description metadata blocks.

## Validation and Acceptance

Acceptance is behavior-based.

- `internal/linear/client.go` reads `Issue.Metadata` from attachment metadata and updates metadata via `attachmentCreate`, not `issueUpdate(description: ...)`.
- `internal/linear/inmemory_client.go` metadata updates do not mutate issue descriptions.
- Existing worker and workflow behaviors continue to consume `Issue.Metadata` unchanged.
- Unit tests for attachment-backed metadata pass and `go test ./...` is green.
- Operator docs describe attachment-backed metadata operations and no longer instruct editing `<!-- colin:metadata ... -->` blocks.

## Idempotence and Recovery

The attachment URL is deterministic per issue. Re-applying metadata patch writes through `attachmentCreate` for the same URL, yielding idempotent behavior. Conflict detection remains read-before-write based and returns `linear.ErrConflict` when state or metadata changed between reads.

Since strict cutover is required, legacy description blocks are ignored for runtime logic. Recovery for missing metadata requires setting attachment metadata values explicitly.

## Artifacts and Notes

Validation transcript:

    $ go test ./...
    ?    github.com/pmenglund/colin [no test files]
    ok   github.com/pmenglund/colin/cmd 1.170s
    ok   github.com/pmenglund/colin/e2e 1.658s
    ok   github.com/pmenglund/colin/internal/codexexec 1.401s
    ok   github.com/pmenglund/colin/internal/config (cached)
    ?    github.com/pmenglund/colin/internal/execution [no test files]
    ok   github.com/pmenglund/colin/internal/linear 0.658s
    ?    github.com/pmenglund/colin/internal/linear/fakes [no test files]
    ok   github.com/pmenglund/colin/internal/worker 2.631s
    ok   github.com/pmenglund/colin/internal/workflow (cached)
    ?    github.com/pmenglund/colin/prompts [no test files]

## Interfaces and Dependencies

No public interface changes are planned.

- `internal/linear/client.go`: keep `Client` interface unchanged; change GraphQL variable transport to `map[string]any`.
- `internal/linear/types.go`: add pure metadata patch helpers for `map[string]string`.
- `internal/linear/inmemory_client.go`: metadata writes update only `Issue.Metadata`.
- `e2e/live_linear_admin_test.go`: add attachment metadata helper methods.

Revision Note (2026-02-21, Codex): Created and maintained as active ExecPlan for `COLIN-47` metadata persistence migration.

Revision Note (2026-02-21, Codex): Updated progress to complete, recorded full-test validation evidence, and finalized outcomes after implementing attachment-backed metadata persistence.
Revision Note (2026-02-21, Codex): Added documentation file `docs/metadata.md` and changed metadata attachment URL from `colin://issue/<id>/metadata` to `https://github.com/pmenglund/colin/blob/main/docs/metadata.md`.
