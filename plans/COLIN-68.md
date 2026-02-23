# Replace Git Shell Commands with go-git v6 (COLIN-68)

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

`PLANS.md` is checked into this repository at `PLANS.md`, and this document must be maintained in accordance with it.

## Purpose / Big Picture

After this change, Colin performs runtime git operations through `github.com/go-git/go-git/v6` instead of spawning `git` subprocesses from production code. This removes shell-command coupling from worker/bootstrap/merge/execution paths while preserving observable task behavior (worktree provisioning, branch/session metadata persistence, merge queue execution, and Codex turn auto-commit).

## Tracker Mapping

Workflow source: `WORKFLOW.md`.

Parent/Epic identifier: `COLIN-68`.

Child identifiers in scope: `COLIN-68`.

## Progress

- [x] (2026-02-23 00:49Z) Assessed issue scope, located all non-test git shell-outs, and classified work as complex.
- [x] (2026-02-23 00:49Z) Created this ExecPlan for `COLIN-68`.
- [x] (2026-02-23 01:00Z) Replaced worker shell-command git helpers with go-git v6 operations in new `internal/worker/git_ops.go` and migrated worker call sites.
- [x] (2026-02-23 01:02Z) Migrated Codex executor auto-commit path in `internal/codexexec/executor.go` to go-git v6 (`Status`, `AddWithOptions`, `Commit`).
- [x] (2026-02-23 01:08Z) Preserved branch metadata persistence by switching to raw git config parsing/encoding through go-git plumbing format APIs.
- [x] (2026-02-23 01:10Z) Validated migration with `go test ./internal/worker ./internal/codexexec` and full `go test ./...`.

## Surprises & Discoveries

- Observation: Linked worktree add/remove/list functionality is not in the core `git` package API; it lives in `github.com/go-git/go-git/v6/x/plumbing/worktree`.
  Evidence: v6 module source under `x/plumbing/worktree/worktree.go` provides `Add`, `Remove`, `List`, and linked worktree `Open` logic.

- Observation: go-git v6 repository merge API supports only fast-forward merge strategy directly (`Repository.Merge` with `FastForwardMerge`).
  Evidence: `repository.go` defines `ErrUnsupportedMergeStrategy` and rejects non-fast-forward strategies.

- Observation: go-git high-level `config.Config` marshaling drops unknown branch subsection options, which broke `branch.<name>.colinSessionId` persistence on the first implementation attempt.
  Evidence: `internal/worker` tests failed to read persisted branch session metadata until branch metadata writes were switched to raw config document updates.

## Decision Log

- Decision: Treat the issue as complex and require an ExecPlan before implementation.
  Rationale: Migration touches multiple production packages (`internal/worker`, `internal/codexexec`), linked worktree behavior, branch metadata persistence, and merge semantics.
  Date/Author: 2026-02-23 / Codex

- Decision: Keep test fixture shell commands (`exec.Command("git", ...)`) in tests for setup/assertion, while removing shelling from production code paths.
  Rationale: The issue targets runtime git operations. Test harness shell usage remains practical and does not affect production runtime behavior.
  Date/Author: 2026-02-23 / Codex

- Decision: Replace merge step with a go-git fast-forward update (`fastForwardBranch`) and hard-reset working tree to merged commit, instead of synthesizing `--no-ff` merge commits.
  Rationale: go-git v6 runtime merge API supports fast-forward strategy; Colin merge preparation already rebases task branches onto base branch, making fast-forward merge the intended deterministic path.
  Date/Author: 2026-02-23 / Codex

- Decision: Persist branch session metadata by editing raw git config document (`plumbing/format/config`) via repository storage filesystem.
  Rationale: Required to preserve arbitrary `colinSessionId` option under `[branch \"...\"]` subsections, which is not retained through high-level config struct marshaling.
  Date/Author: 2026-02-23 / Codex

## Outcomes & Retrospective

`COLIN-68` is implemented. Production git subprocess calls were removed from `internal/worker` and `internal/codexexec`, and replaced with go-git v6 APIs for repository/worktree/ref/config/push operations plus linked worktree management through `x/plumbing/worktree`.

Behavioral coverage remained intact through existing tests:

- task workspace bootstrap still verifies base branch, creates/reuses linked worktrees, and checks out/creates issue branches.
- merge executor still performs branch validation, merge progression, push, branch cleanup, and worktree cleanup with fail-fast semantics.
- branch session metadata still persists and reloads from branch-scoped git config.
- Codex turn auto-commit still stages all changes and commits only when worktree is dirty.

One planned/accepted difference is merge implementation: runtime now performs fast-forward merge updates (with worktree hard reset) rather than shelling `git merge --no-ff`. This matches the existing rebase-based merge preparation flow and passed current merge/recovery tests.

## Context and Orientation

Current runtime git subprocess usage is centralized in `internal/worker/git_command.go` and duplicated helper functions in `internal/codexexec/executor.go`. These helpers are consumed by:

- `internal/worker/task_bootstrap.go` for base-branch checks, linked worktree creation, branch checkout/create, and branch existence checks.
- `internal/worker/branch_metadata.go` for reading/writing branch-scoped `colinSessionId` in local git config.
- `internal/worker/merge_executor.go` for branch existence/ancestry checks, base checkout, merge, push, branch cleanup, and worktree cleanup/discovery.
- `internal/codexexec/executor.go` for worktree change detection and auto-commit after successful well-specified turns.

The target library `github.com/go-git/go-git/v6` is already available upstream and includes linked worktree support via `x/plumbing/worktree`, plus core repository/worktree operations for checkout, status/add/commit, refs, config, and push.

## Plan of Work

First, add go-git v6 dependencies and replace `internal/worker/git_command.go` with a worker-scoped go-git adapter that offers the existing behavior-level primitives (branch existence, ancestor checks, base branch verification, worktree add/remove/open, push, and config read/write). Keep function names in worker call sites as stable as practical to reduce churn.

Second, migrate `internal/worker/task_bootstrap.go`, `internal/worker/branch_metadata.go`, and `internal/worker/merge_executor.go` to use adapter primitives instead of command/argument strings. Preserve existing error contexts (`verify base branch`, `source branch`, `worktree path`, etc.) so tests and operator messages remain actionable.

Third, migrate `internal/codexexec/executor.go` auto-commit path to go-git v6 (`Status`, `AddWithOptions{All:true}`, `Commit`) and remove `os/exec` helper duplication.

Fourth, update or add tests when behavior shifts (for example linked-worktree fallback discovery and merge semantics edge cases).

Finally, run the full test suite and update this ExecPlan sections (`Progress`, `Outcomes & Retrospective`, `Artifacts and Notes`, revision note).

## Concrete Steps

Run from repository root: `/Users/pme/.colin/worktrees/COLIN-68`.

1. Implement runtime migration to go-git v6 and adjust worker/codexexec code paths.

    go test ./internal/worker ./internal/codexexec

2. Resolve any test regressions and run full validation.

    go test ./...

Expected result: no production `exec.CommandContext(..., "git", ...)` usage remains; runtime behavior and tests remain green.

## Validation and Acceptance

Acceptance is behavior-based.

- Production code under `internal/worker` and `internal/codexexec` no longer shells out to git.
- Runtime still creates/reuses worktrees and issue branches, records branch session metadata, merges/pushes/cleans up in merge queue, and auto-commits Codex worktree changes.
- Existing tests for bootstrap, branch metadata, merge execution/recovery, and executor auto-commit pass.
- `go test ./...` passes.

## Idempotence and Recovery

Bootstrap and merge operations remain retry-safe:

- Worktree creation remains idempotent (existing worktree reused).
- Branch/session metadata writes remain overwrite-safe.
- Merge remains fail-fast on missing/stale branch/worktree inputs, preserving retry semantics in workflow state handling.

If migration introduces a behavior mismatch, recovery is to keep existing error contexts and iterate tests until pre-existing workflow invariants are restored.

## Artifacts and Notes

Validation transcript:

    $ go test ./internal/worker ./internal/codexexec
    ok  	github.com/pmenglund/colin/internal/worker	2.237s
    ok  	github.com/pmenglund/colin/internal/codexexec	(cached)

    $ go test ./...
    ?   	github.com/pmenglund/colin	[no test files]
    ok  	github.com/pmenglund/colin/cmd	0.371s
    ok  	github.com/pmenglund/colin/e2e	1.832s
    ok  	github.com/pmenglund/colin/internal/codexexec	(cached)
    ok  	github.com/pmenglund/colin/internal/config	1.326s
    ?   	github.com/pmenglund/colin/internal/execution	[no test files]
    ok  	github.com/pmenglund/colin/internal/linear	0.859s
    ?   	github.com/pmenglund/colin/internal/linear/fakes	[no test files]
    ok  	github.com/pmenglund/colin/internal/logging	1.069s
    ok  	github.com/pmenglund/colin/internal/worker	(cached)
    ok  	github.com/pmenglund/colin/internal/workflow	1.537s
    ?   	github.com/pmenglund/colin/prompts	[no test files]

## Interfaces and Dependencies

Primary dependency changes:

- Add direct dependency on `github.com/go-git/go-git/v6`.
- Use linked worktree helper package `github.com/go-git/go-git/v6/x/plumbing/worktree`.

No public interface changes are planned for `worker` or `codexexec` exported contracts.

Revision Note (2026-02-23, Codex): Created initial ExecPlan for `COLIN-68` migration from git shell commands to go-git v6.
Revision Note (2026-02-23, Codex): Marked implementation complete, documented merge/config decisions discovered during migration, and attached passing test evidence.
