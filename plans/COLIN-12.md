# Add Live External Lifecycle Harness Test (COLIN-12)

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

`PLANS.md` is checked into this repository at `PLANS.md`, and this document must be maintained in accordance with PLANS.md.

## Purpose / Big Picture

Keep the live external harness simple: use caller-provided Linear team context, avoid script/TestMain orchestration, and avoid requiring an external sandbox repo URL.

The live test should create its own temporary git sandbox (local bare remote + local clone with seeded `main`) so operators only provide Linear/Codex credentials.

## Tracker Mapping

Workflow source: `WORKFLOW.md`.

Parent/Epic identifier: `COLIN-2`.

Child issue identifier in scope: `COLIN-12`.

## Progress

- [x] (2026-02-21 17:59Z) Created and moved tracker issue `COLIN-12` to `In Progress`.
- [x] (2026-02-21 19:08Z) Removed script-based setup/teardown (`e2e/scripts/live_linear_env.sh`) and `livee2e` `TestMain` wrapper (`e2e/live_testmain_test.go`).
- [x] (2026-02-21 19:08Z) Refactored `e2e/lifecycle_live_external_test.go` back to self-contained temporary project create/archive behavior using provided team key/id.
- [x] (2026-02-21 19:08Z) Reverted `Taskfile.yaml` `e2e-live` description to generic live env requirement wording.
- [x] (2026-02-21 19:10Z) Ran validation commands: `go test ./e2e`, `go test ./...`, and `go test -v -run TestLifecycleLiveExternalSystems -tags livee2e ./e2e`.
- [x] (2026-02-21 19:22Z) Removed `COLIN_LIVE_GIT_SANDBOX_REPO_URL` requirement and switched live harness to a local temporary git sandbox created during test execution.
- [x] (2026-02-21 19:31Z) Removed `COLIN_LIVE_E2E` runtime gate; live test now relies on `livee2e` build tag and fails fast when required env vars are missing.
- [x] (2026-02-21 19:44Z) Removed caller-provided `CODEX_HOME` requirement; live harness now creates a per-run temporary `CODEX_HOME` directory and injects it into worker runs.
- [x] (2026-02-21 19:58Z) Replaced `COLIN_LIVE_LINEAR_TEAM_KEY`/`COLIN_LIVE_LINEAR_TEAM_ID` with `COLIN_LIVE_LINEAR_TEAM`; harness now resolves team ID via GraphQL from the provided team key before running create mutations.
- [x] (2026-02-21 20:05Z) Seeded temporary `CODEX_HOME` from authenticated host Codex home to avoid runtime 401 in live threads.
- [x] (2026-02-21 20:08Z) Updated Codex JSON schema `required` keys to include `transcript_ref` and `screenshot_ref` to satisfy current API validation.
- [x] (2026-02-21 20:16Z) Added workflow state alias resolution in HTTP Linear client (`Review` maps to `In Review`/`Human Review`) and unit tests.
- [x] (2026-02-21 20:22Z) Updated live harness to resolve canonical states to actual team state names for terminal checks/assertions; made cleanup ignore not-found archive errors.
- [x] (2026-02-21 20:24Z) Ran live tagged harness with `.envrc` and confirmed pass.

## Surprises & Discoveries

- Observation: Script/TestMain setup added complexity without solving key-scoping because Linear key creation/restriction is not project-scoped via API.
  Evidence: User direction to remove setup/teardown once key-scope limitation was confirmed.
- Observation: Fresh temporary `CODEX_HOME` without seeded auth caused Codex turn failures (`401 Missing bearer`).
  Evidence: Live run failed in cycle 2 until temporary home was seeded from authenticated host state.
- Observation: Linear team review state name is `Human Review`, not `Review`.
  Evidence: Live GraphQL `workflowStates` query returned `Human Review`, causing strict `Review` lookups/assertions to fail.

## Decision Log

- Decision: Remove script/TestMain orchestration and keep live harness env-driven.
  Rationale: Temporary setup/teardown layers were not useful for API-key isolation and increased moving parts.
  Date/Author: 2026-02-21 / Codex
- Decision: Remove external sandbox repo URL input and create a temporary local git sandbox inside the test.
  Rationale: Local temp sandbox preserves real git behavior while reducing required operator setup and avoiding dependency on a pre-provisioned remote URL.
  Date/Author: 2026-02-21 / Codex
- Decision: Remove `COLIN_LIVE_E2E` skip gate and require fail-fast semantics for missing live env when tagged live test is invoked.
  Rationale: The `livee2e` build tag already provides explicit opt-in; once invoked, test intent is execution, so missing prerequisites should fail, not skip.
  Date/Author: 2026-02-21 / Codex
- Decision: Create `CODEX_HOME` under test tempdir instead of requiring operator-provided `CODEX_HOME`.
  Rationale: Keeps harness self-contained and removes one operator prerequisite while still giving Codex runtime a writable home.
  Date/Author: 2026-02-21 / Codex
- Decision: Collapse live team inputs to one env var (`COLIN_LIVE_LINEAR_TEAM`) and resolve team ID at runtime.
  Rationale: Team key is sufficient for query scoping and easier for operators; team ID can be derived once and reused for project/issue mutations.
  Date/Author: 2026-02-21 / Codex
- Decision: Seed temporary `CODEX_HOME` from existing authenticated Codex home before running worker cycles.
  Rationale: Keeps ephemeral test isolation while preserving required auth/session state for real Codex execution.
  Date/Author: 2026-02-21 / Codex
- Decision: Accept team-specific review aliases (`In Review`, `Human Review`) for state resolution and live assertions.
  Rationale: Real Linear teams vary state naming; canonical workflow decisions still target review semantics.
  Date/Author: 2026-02-21 / Codex
- Decision: Treat not-found archive errors as non-fatal in successful live cleanup.
  Rationale: Cleanup should be best-effort and not fail successful lifecycle validation when artifacts are already absent.
  Date/Author: 2026-02-21 / Codex

## Outcomes & Retrospective

Simplification completed:

- Live harness now expects caller-provided `COLIN_LIVE_LINEAR_TEAM` (team key) and resolves team ID from it at startup.
- Live harness now creates a temporary local git remote/clone with seeded `main` and no longer requires `COLIN_LIVE_GIT_SANDBOX_REPO_URL`.
- Live harness no longer checks `COLIN_LIVE_E2E`; tagged execution now fails fast if required env is missing.
- Live harness now creates `CODEX_HOME` in `t.TempDir()`, seeds it from authenticated host Codex state, and injects it into each worker cycle.
- Live harness and HTTP client now tolerate team-specific review state naming (`Human Review`/`In Review`) while preserving canonical workflow behavior.
- No package-level setup/cleanup hook remains.
- No external shell orchestration dependency remains for test startup.

Validation after simplification:

- `go test ./e2e` passed.
- `go test ./...` passed.
- `go test -v -run TestLifecycleLiveExternalSystems -tags livee2e ./e2e` now fails fast with missing-env error when live credentials are not provided.
- `go test -v -run TestLifecycleLiveExternalSystems -tags livee2e ./e2e` passed with live credentials from `.envrc`.

## Context and Orientation

Files in scope after simplification:

- `e2e/lifecycle_live_external_test.go`
- `e2e/live_git_sandbox_test.go`
- `Taskfile.yaml`
- `plans/COLIN-12.md`

Removed from scope:

- `e2e/scripts/live_linear_env.sh`
- `e2e/live_testmain_test.go`

## Plan of Work

Delete the setup/teardown orchestration files, restore env contract and project lifecycle logic directly in the live test, and revalidate all impacted test commands.

## Concrete Steps

Run from `/Users/pme/src/pmenglund/colin`:

1. `go test ./e2e`
2. `go test ./...`
3. `go test -v -run TestLifecycleLiveExternalSystems -tags livee2e ./e2e`

## Validation and Acceptance

Acceptance conditions:

1. `go test ./e2e` and `go test ./...` pass.
2. Tagged test fails fast (does not skip) when required live env vars are missing.
3. Live harness no longer depends on script/TestMain setup/teardown artifacts.

## Idempotence and Recovery

- Re-running tests is safe.
- Artifact cleanup remains controlled by existing in-test project/issue cleanup behavior.

## Artifacts and Notes

No script context artifacts remain after this simplification.

## Interfaces and Dependencies

Live-run environment contract now expects operator-provided:

- `COLIN_LIVE_LINEAR_API_TOKEN`
- `COLIN_LIVE_LINEAR_TEAM`

Revision Note (2026-02-21, Codex): Simplified `COLIN-12` by removing script/TestMain setup/teardown path per request.
