# Establish Deterministic Linear State Management (Milestone 1)

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

`PLANS.md` is checked into this repository at `PLANS.md`, and this document must be maintained in accordance with it.

## Purpose / Big Picture

After this milestone, `colin` will be able to read Linear issues, claim eligible work when an issue enters `Todo`, and move issues through the allowed workflow transitions in a deterministic and idempotent way. A user will be able to run a single worker command locally and observe state transitions and metadata updates in Linear without any git actions, pull request actions, or repository file changes beyond this codebase itself.

The user-visible behavior enabled by this milestone is that Linear becomes the execution control plane: issue state and issue metadata alone determine what `colin` does next, and repeated worker runs do not duplicate transitions or create conflicting claims.

## Tracker Mapping

Workflow: `WORKFLOW.md` (this repository currently has no `workflows/` directory, so `WORKFLOW.md` is the active workflow source).

Parent/Epic identifier for this milestone: `COLIN-M1-LINEAR-STATE` (replace with the actual Linear epic ID before implementation starts).

Child task identifiers covered by this plan: `COLIN-M1-01` (Linear API client), `COLIN-M1-02` (workflow state model), `COLIN-M1-03` (claim and lease idempotence), `COLIN-M1-04` (worker loop and reconciliation), `COLIN-M1-05` (tests and acceptance harness), `COLIN-M1-06` (operator docs).

## Progress

- [x] (2026-02-11 02:25Z) Created the initial ExecPlan for milestone 1 with explicit scope boundaries and implementation details.
- [x] (2026-02-11 02:35Z) Implemented `COLIN-M1-01`: added `internal/config` environment loader and `internal/linear` GraphQL client with metadata patch support.
- [x] (2026-02-11 02:35Z) Implemented `COLIN-M1-02`: added deterministic workflow transition model in `internal/workflow/states.go` and `internal/workflow/decision.go`.
- [x] (2026-02-11 02:35Z) Implemented `COLIN-M1-03`: added lease schema and conflict-aware metadata update path with re-fetch-before-write behavior.
- [x] (2026-02-11 02:35Z) Implemented `COLIN-M1-04`: added `cmd/worker.go` and `internal/worker/runner.go` with `RunOnce` and continuous polling loop.
- [x] (2026-02-11 02:35Z) Implemented `COLIN-M1-05`: added unit and integration-style tests for transitions, lease behavior, conflicts, and idempotence.
- [x] (2026-02-11 02:35Z) Implemented `COLIN-M1-06`: updated `APP.md` and added `docs/milestone1-linear-state.md` runbook.
- [x] (2026-02-11 03:09Z) Extended configuration loading to support `colin.toml` with environment-variable precedence, including tests and docs.
- [x] (2026-02-11 03:09Z) Added root command `--config` flag (default `colin.toml`) and wired worker loading through explicit config path.
- [x] (2026-02-11 03:09Z) Added e2e test coverage that runs the CLI with `--config <temp colin.toml>` and verifies config-file-based Linear access.
- [x] (2026-02-11 04:04Z) Replaced hand-written `linear.Client` worker test double with `counterfeiter`-generated fake and updated tests to stub generated methods.
- [x] (2026-02-11 04:15Z) Updated candidate issue selection to exclude `Todo` issues that are blocked by at least one other issue.
- [x] (2026-02-11 04:42Z) Added cycle-level worker logs (`cycle_start`, `issues_fetched`, `cycle_complete`) so `worker run --once` provides visible operational output even with an empty queue.

## Surprises & Discoveries

- Observation: The repository has only a minimal Cobra bootstrap (`main.go`, `cmd/root.go`) and no existing Linear integration package.
  Evidence: `find . -maxdepth 3 -type f` shows no `internal/linear` or workflow engine files.

- Observation: The repo currently uses `WORKFLOW.md` at root and does not contain `workflows/LINEAR.md`.
  Evidence: `ls workflows` returned no directory.

- Observation: Linear custom fields are not directly guaranteed in this repository bootstrap, so metadata needed a stable persistence strategy without schema migration work.
  Evidence: No existing field IDs/config were present in code or docs, so a metadata block in issue descriptions was chosen for milestone 1.

- Observation: Running temporary local mock server commands in this sandbox emits `zsh: nice(5) failed: operation not permitted` even when execution succeeds.
  Evidence: dry-run command completed and logged expected decision output despite that warning.

- Observation: `LoadFromEnv`-only behavior was too restrictive for operator workflows that prefer checked-in local defaults.
  Evidence: Follow-up requirement requested `colin.toml` loading support in addition to environment variables.

## Decision Log

- Decision: Milestone 1 will include only Linear integration and workflow state transitions, and will explicitly exclude git operations, Codex thread operations, pull request creation, and non-Linear side effects.
  Rationale: This isolates the highest-risk coordination logic first and gives a stable control plane before adding code-writing or merge automation.
  Date/Author: 2026-02-11 / Codex

- Decision: The transition engine will be implemented as pure Go logic that accepts current issue state plus metadata and returns either a no-op decision or one concrete transition action.
  Rationale: Pure transition logic is easier to test exhaustively and guarantees deterministic behavior across retries.
  Date/Author: 2026-02-11 / Codex

- Decision: Claims will be represented with a lease (owner + expiry + execution id) written to Linear issue metadata, and transitions will require an active valid lease.
  Rationale: A lease prevents two workers from acting on one issue and enables safe recovery when a worker dies.
  Date/Author: 2026-02-11 / Codex

- Decision: Validation for this milestone will rely on mocked or local test doubles for the Linear API plus deterministic integration tests, not live production API calls.
  Rationale: Repeatable tests are required for idempotence and race conditions; local test doubles keep tests fast and stable.
  Date/Author: 2026-02-11 / Codex

- Decision: Persist `colin` metadata inside a structured HTML comment block in the Linear issue description for milestone 1.
  Rationale: This keeps all execution metadata in Linear without requiring prior custom-field provisioning and keeps local tests self-contained.
  Date/Author: 2026-02-11 / Codex

- Decision: Worker writes metadata patch before state transition and enforces transition validity with `workflow.CanTransition`.
  Rationale: Metadata-first writes keep heartbeat/lease information auditable even when state update conflicts, and transition guards prevent illegal state mutations.
  Date/Author: 2026-02-11 / Codex

- Decision: Add `config.Load()` that reads `colin.toml` (or `COLIN_CONFIG`) first, then applies environment variable overrides.
  Rationale: This supports local declarative configuration while preserving env-driven automation and secret override patterns.
  Date/Author: 2026-02-11 / Codex

- Decision: Use a persistent root `--config` flag to drive file selection in CLI execution, defaulting to `colin.toml`.
  Rationale: Explicit CLI control is clearer for operators than environment-only file selection and satisfies predictable command behavior.
  Date/Author: 2026-02-11 / Codex

- Decision: Use `maxbrunsfeld/counterfeiter` as the source of truth for `linear.Client` fakes and generate `internal/linear/linearfakes/fake_client.go`.
  Rationale: Generated fakes reduce test-maintenance burden and keep interface-level test doubles synchronized with `linear.Client`.
  Date/Author: 2026-02-11 / Codex

- Decision: Filter blocked `Todo` issues at Linear issue selection time by querying `blockedByIssues(first: 1)` and skipping when non-empty.
  Rationale: Worker should only consider immediately actionable `Todo` issues; this keeps blocked work out of the runnable queue.
  Date/Author: 2026-02-11 / Codex

## Outcomes & Retrospective

Milestone 1 is implemented. The repository now contains a deterministic Linear state worker with explicit workflow transitions, lease-based claim semantics, and idempotent behavior verified by tests. The CLI now exposes `colin worker run` with `--once` and `--dry-run` options, and the worker only performs Linear API reads/writes in this phase.

Validation outcomes:

- `go test ./...` passes across command, config, Linear client, workflow, and worker packages.
- Unit tests confirm deterministic decisions for identical snapshots.
- Worker integration-style tests confirm idempotence (`RunOnce` twice leads to one transition).
- A dry-run against a local mock Linear endpoint logs expected transition decisions without writes.

Remaining gap: no live production Linear workspace run was performed in this implementation session. This is intentionally deferred to operator validation using real workspace credentials.

## Context and Orientation

This repository now includes a Cobra root command (`cmd/root.go`) plus worker command wiring (`cmd/worker.go`), configuration parsing (`internal/config/config.go`), Linear API integration (`internal/linear`), deterministic workflow logic (`internal/workflow`), and worker orchestration (`internal/worker/runner.go`).

In this plan, the term "deterministic state machine" means a function that produces the same decision every time for the same input issue snapshot. The term "idempotent" means repeating the same worker cycle does not cause duplicate claims, duplicate transitions, or conflicting metadata writes.

The target workflow states are `Todo`, `Refine`, `In Progress`, `Human Review`, `Merge`, `Done`, and `Cancelled`. For milestone 1, the worker handles only these workflow movements:

`Todo -> In Progress` when an issue is claimable and sufficiently specified according to a local rule set.

`Todo -> Refine` when required specification fields are missing.

`In Progress -> Human Review` when the milestone-specific completion condition is met (for this milestone, completion means workflow checks passed and state metadata is consistent, not code implementation in another repository).

`In Progress -> Refine` when new specification gaps are detected while processing.

`Merge -> Done` when merge-ready criteria metadata indicates completion (within milestone 1 this is simulated by metadata flags only, with no git merge operation).

Human-driven movements `Human Review -> Todo`, `Human Review -> Merge`, and manual cancellation are observed and respected by the worker, but never initiated by this milestone's automation unless explicitly configured in tests.

## Plan of Work

First, add configuration and Linear client infrastructure. Create `internal/config/config.go` to load `colin.toml` (or `COLIN_CONFIG`) and then apply environment-variable overrides for `LINEAR_API_TOKEN`, `LINEAR_TEAM_ID`, poll interval, lease duration, and dry-run mode. Create `internal/linear/client.go` and `internal/linear/types.go` to encapsulate all API calls needed by this milestone: list candidate issues, read issue details, update issue state, and update issue metadata fields used by leases and execution tracking.

Second, add the deterministic workflow model. Create `internal/workflow/states.go` for state constants and allowed transitions. Create `internal/workflow/decision.go` that implements the transition function. This function accepts a normalized issue snapshot and returns one action: no-op, claim-and-transition, transition-to-refine, transition-to-human-review, transition-to-done, or release-lease. Keep this logic free of network calls so it can be tested as pure functions.

Third, add claim and idempotence behavior. Create `internal/workflow/lease.go` for lease calculation and validation. Lease metadata includes `lease_owner`, `lease_expires_at`, and `execution_id`. Add compare-and-set semantics in `internal/linear/client.go` by re-fetching the issue before write and aborting when the observed version or lease metadata changed. If Linear cannot provide optimistic concurrency primitives directly, emulate this with read-before-write and deterministic conflict detection.

Fourth, add the worker loop. Create `internal/worker/runner.go` with a polling loop that fetches candidate issues, runs each through the decision engine, applies at most one transition per issue per cycle, and records a heartbeat metadata field. Add a new command file `cmd/worker.go` with a `worker run` subcommand that starts the loop and logs each decision. Keep logs structured and concise for auditability.

Fifth, add tests before closing the milestone. Add unit tests in `internal/workflow/decision_test.go` to cover every allowed state transition and rejection path. Add lease tests in `internal/workflow/lease_test.go` for expiry and stale claim handling. Add integration tests in `internal/worker/runner_test.go` using an in-memory fake Linear client that simulates conflicting writes and repeated polls. The tests must prove idempotence by running the same cycle twice and observing only one resulting transition.

Sixth, document operation and recovery. Update `APP.md` with the new packages and add run instructions for `colin worker run`. Add a short operator runbook in `docs/milestone1-linear-state.md` describing required environment variables, expected logs, and how to recover from stale leases safely.

## Concrete Steps

Run all commands from repository root: `/Users/pme/src/pmenglund/colin`.

1. Create package directories and new files.

    mkdir -p internal/config internal/linear internal/workflow internal/worker docs cmd

2. Implement configuration and Linear client.

    go test ./internal/config ./internal/linear

Expected result: tests pass and verify environment parsing plus client request formatting.

3. Implement deterministic workflow decision engine and leases.

    go test ./internal/workflow

Expected result: transition matrix tests pass, and invalid transitions fail with explicit error messages.

4. Implement worker command and loop.

    go test ./internal/worker ./cmd

Expected result: runner tests pass, including duplicate-poll idempotence cases.

5. Run full validation suite.

    go test ./...
    go run . worker run --help

Expected result: tests pass, and worker command help shows `--once` and `--dry-run` flags.

6. Run one dry cycle against a local mock Linear endpoint (no external side effects).

    LINEAR_API_TOKEN=test-token LINEAR_TEAM_ID=test-team LINEAR_BASE_URL=http://127.0.0.1:18082/graphql go run . worker run --once --dry-run

Expected result: log output includes a decision line such as `action=claim_and_transition to="In Progress"` for a mock `Todo` issue.

## Validation and Acceptance

Acceptance for this milestone is behavior-based and must be observable from Linear and test output.

The first acceptance condition is deterministic transitions. Given the same issue snapshot twice, `internal/workflow/decision.go` must emit the same decision both times. This is proven by unit tests that call the decision function repeatedly with identical input and compare results.

The second acceptance condition is idempotent claiming. If the worker runs twice in a row against the same `Todo` issue, the first run may claim and transition it, and the second run must produce no additional transition. This is proven by integration tests and by a manual `--once` followed by a second `--once` in a sandbox Linear workspace.

The third acceptance condition is lease safety. If a lease is active and unexpired for another worker, this worker must not transition the issue. If the lease expires, this worker may reclaim the issue deterministically. This is proven by lease unit tests and one integration test with simulated clock movement.

The fourth acceptance condition is strict scope control. Running this milestone does not perform any git operation, pull request operation, Codex thread operation, or external repository edits. This is proven by code inspection of `internal/worker` and by tests that assert all side effects go through the Linear client interface only.

## Idempotence and Recovery

All worker actions are designed to be safely repeatable. Re-running `go run . worker run --once` should either apply one missing transition or no-op if the issue is already reconciled.

If the worker crashes after writing a lease but before transitioning state, recovery is automatic: the next cycle sees the active lease and either resumes if it is the same worker/execution id or waits for lease expiry. No manual database cleanup is needed because Linear stores the lease metadata.

If metadata becomes inconsistent (for example, state changed manually while a lease remains), the worker records a conflict note in issue metadata, drops the lease if it owns it, and no-ops until the next poll. This avoids destructive retries.

If configuration is invalid, startup fails fast with a clear error and zero side effects. Operators fix environment variables and rerun.

## Artifacts and Notes

Implementation artifacts captured:

    $ go test ./...
    ?   	github.com/pmenglund/colin	[no test files]
    ok  	github.com/pmenglund/colin/cmd
    ok  	github.com/pmenglund/colin/internal/config
    ok  	github.com/pmenglund/colin/internal/linear
    ok  	github.com/pmenglund/colin/internal/worker
    ok  	github.com/pmenglund/colin/internal/workflow

    $ go run . worker run --once --dry-run
    2026/02/10 18:35:31 issue=COL-1 state="Todo" action=claim_and_transition to="In Progress" reason="claimed todo issue"

    $ go test ./internal/worker -run TestRunnerRunOnceIsIdempotentForTodoClaim -v
    === RUN   TestRunnerRunOnceIsIdempotentForTodoClaim
    --- PASS: TestRunnerRunOnceIsIdempotentForTodoClaim (0.00s)
    PASS

## Interfaces and Dependencies

Use `github.com/spf13/cobra` for CLI command wiring (already present) and the Go standard library `net/http`, `encoding/json`, and `context` for Linear communication. Do not add a heavy SDK in this milestone unless manual HTTP wiring proves unworkable.

Define these interfaces and key types exactly to keep dependencies explicit:

In `internal/linear/client.go`, define:

    type Client interface {
        ListCandidateIssues(ctx context.Context, teamID string) ([]Issue, error)
        GetIssue(ctx context.Context, issueID string) (Issue, error)
        UpdateIssueState(ctx context.Context, issueID string, toState string) error
        UpdateIssueMetadata(ctx context.Context, issueID string, metadata MetadataPatch) error
    }

In `internal/workflow/decision.go`, define:

    type Decision struct {
        Action       ActionType
        ToState      string
        Reason       string
        LeasePatch   *Lease
        MetadataPatch map[string]string
    }

    func Decide(snapshot IssueSnapshot, now time.Time) Decision

In `internal/workflow/lease.go`, define:

    type Lease struct {
        Owner        string
        ExecutionID  string
        ExpiresAtUTC time.Time
    }

    func IsLeaseActive(lease Lease, now time.Time) bool
    func BuildLease(owner string, executionID string, now time.Time, ttl time.Duration) Lease

In `internal/worker/runner.go`, define:

    type Runner struct {
        Linear     linear.Client
        TeamID     string
        WorkerID   string
        PollEvery  time.Duration
        LeaseTTL   time.Duration
        DryRun     bool
        Clock      func() time.Time
        Logger     *log.Logger
    }

    func (r *Runner) RunOnce(ctx context.Context) error
    func (r *Runner) Run(ctx context.Context) error

These names are intentionally stable so milestone 2 can build on them without rework.

Revision Note (2026-02-11, Codex): Created the initial milestone 1 ExecPlan in response to a request for a Linear-only state management phase. The plan intentionally excludes git, Codex-thread, PR, and repository-change automation to reduce scope and risk for first implementation.
Revision Note (2026-02-11, Codex): Updated the plan after implementation to mark all milestone tasks complete, record implementation decisions, capture test/runtime artifacts, and document remaining validation gap (live workspace run).
Revision Note (2026-02-11, Codex): Added follow-up configuration enhancement for `colin.toml` support with environment overrides, plus updated tests and documentation.
Revision Note (2026-02-11, Codex): Added root-level `--config` option with default `colin.toml` and adjusted CLI/config tests for explicit path loading.
Revision Note (2026-02-11, Codex): Added end-to-end CLI validation for config-file-based access in `e2e/config_file_access_test.go`.
Revision Note (2026-02-11, Codex): Adopted counterfeiter-generated fake for `linear.Client` and migrated worker tests from manual fake implementation.
Revision Note (2026-02-11, Codex): Changed Linear candidate selection so blocked `Todo` issues are excluded from runnable work and added client tests for this behavior.
