# Add GitHub App authentication flow for Colin GitHub operations

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

This document follows `PLANS.md` at the repository root and must be maintained in accordance with that file.

## Purpose / Big Picture

After this change, Colin will authenticate to GitHub using a GitHub App private key instead of depending on local `gh` CLI login or ambient git credentials. When Colin needs to push branches or manage pull requests, it will first mint a short-lived GitHub App JWT, exchange it for an installation access token, and then use that token for the GitHub operation. Operators can verify this by running tests and by starting Colin with GitHub App settings; PR operations should succeed without `gh auth login`.

## Tracker Mapping

Workflow: `WORKFLOW.md`.
Epic/issue: `COLIN-84`.
Child tasks covered in this plan: configuration additions, token provider implementation, git push auth integration, pull request REST integration, docs/tests updates.

## Progress

- [x] (2026-03-01 22:08Z) Reviewed current architecture, workflow instructions, and existing GitHub integration points (`internal/worker/pull_request.go`, `internal/worker/git_ops.go`, `cmd/worker.go`, config/docs).
- [x] (2026-03-01 22:14Z) Implemented `internal/githubapp` installation token provider with JWT signing, installation token exchange (`POST /app/installations/{id}/access_tokens`), and expiry-aware caching.
- [x] (2026-03-01 22:18Z) Extended configuration with GitHub App fields (`github_api_url`, `github_app_id`, `github_app_installation_id`, private key value/path), env overrides, validation, and private key resolution helper.
- [x] (2026-03-01 22:22Z) Integrated installation-token auth into git push paths in pull-request and merge execution via go-git HTTP basic auth (`x-access-token` username).
- [x] (2026-03-01 22:27Z) Replaced gh CLI pull-request lookup/create with GitHub REST API calls in `internal/worker/pull_request.go`.
- [x] (2026-03-01 22:35Z) Updated and expanded tests in `internal/githubapp`, `internal/config`, `internal/worker`, and `cmd`; updated docs/config examples; `go test ./...` passes.
- [x] (2026-03-01 22:36Z) Recorded outcomes and retrospective.

## Surprises & Discoveries

- Observation: Existing pull request logic shells out to `gh pr list/create` and does not have an HTTP API client path.
  Evidence: `internal/worker/pull_request.go` uses `runExternalCommand` with binary default `gh`.

- Observation: Existing git push path uses go-git push with no explicit authentication, relying on whatever remote credentials are already configured.
  Evidence: `internal/worker/git_ops.go` function `pushBranch` sets `PushOptions` with remote/refspec only.

- Observation: Existing pull-request tests use a local bare git remote path, which cannot be parsed as a GitHub owner/repo URL.
  Evidence: Previous tests configured `origin` as `/tmp/.../origin.git`, requiring repository parsing override in updated REST API tests.

## Decision Log

- Decision: Create a new Linear issue (`COLIN-84`) before code changes because this is substantial cross-cutting work and must remain tracked.
  Rationale: Repository workflow requires significant changes to map to a tracker item and use an ExecPlan.
  Date/Author: 2026-03-01 / Codex.

- Decision: Keep branch creation and commit creation local in git, and authenticate remote push + PR API calls with installation tokens.
  Rationale: Colin already performs local worktree/branch/commit operations; replacing those with Git object REST endpoints would be unnecessary complexity for this requirement. Remote branch creation still happens on authenticated push.
  Date/Author: 2026-03-01 / Codex.

- Decision: Require GitHub App configuration when `linear_backend = "http"` during config validation.
  Rationale: GitHub operations are mandatory for review/merge lifecycle in HTTP mode; failing fast at startup is safer than deferred runtime failures.
  Date/Author: 2026-03-01 / Codex.

## Outcomes & Retrospective

Implemented end-to-end GitHub App authentication and token usage for Colin GitHub operations. Colin now uses a signed GitHub App JWT and installation token exchange flow, and applies installation tokens to both push and pull-request API operations. Pull-request management no longer depends on `gh` CLI shelling.

What worked well:

- Existing abstraction boundaries (`cmd` wiring, worker managers, config package) allowed clean injection of a token provider without broad architectural churn.
- Test suite already had good local git fixtures, so adding API-level tests with `httptest` was straightforward.

Remaining gap:

- SSH remotes now return a clear error for token-based push auth. Operators should use HTTPS remotes for GitHub App auth.

## Context and Orientation

Colin currently has two GitHub-facing behaviors in the worker runtime:

1. In `internal/worker/pull_request.go`, `GitPullRequestManager` pushes the task branch and then uses the GitHub CLI (`gh`) to list/create pull requests.
2. In `internal/worker/merge_executor.go`, merge phase optionally pushes the base branch to the remote after a local fast-forward merge.

Both push paths call `pushBranch` in `internal/worker/git_ops.go`, which currently does not accept explicit credentials. Configuration in `internal/config/config.go` has no GitHub App fields, and docs in `colin.toml.example`, `docs/getting-started.md`, and `docs/operator-runbook.md` currently describe `gh` authentication.

This plan introduces a GitHub App auth component and threads it through worker constructors so all GitHub network operations use installation tokens.

## Plan of Work

Implement a new package (under `internal/githubapp`) that can:

- Parse and hold GitHub App credentials.
- Create RS256-signed JWTs with required claims (`iat`, `exp`, `iss`).
- Exchange a JWT for an installation token using `POST /app/installations/{id}/access_tokens`.
- Cache tokens in memory until close to expiry, then refresh.

Extend `internal/config/config.go` to support GitHub App fields from file/env, with defaults and validation semantics suitable for the HTTP backend. Wire this into `cmd/worker.go` by constructing a token source once at startup and injecting it into both merge and pull-request managers.

Refactor git push helpers so `pushBranch` optionally takes a credential callback or auth provider and uses go-git HTTP basic auth (`username` as `x-access-token`, password as installation token) for HTTPS GitHub remotes.

Refactor `internal/worker/pull_request.go` to replace gh CLI calls with direct REST API calls:

- `GET /repos/{owner}/{repo}/pulls?head={owner}:{branch}&base={base}&state=open&per_page=1`
- `POST /repos/{owner}/{repo}/pulls`

Determine `{owner, repo}` by parsing remote URL (`https://github.com/owner/repo(.git)` and `git@github.com:owner/repo(.git)`).

Update unit tests for config, token provider behavior, and PR manager behavior with a fake HTTP server. Update docs to remove/adjust `gh` requirement and describe GitHub App settings.

## Concrete Steps

From repository root (`/Users/pme/src/pmenglund/colin`):

1. Add new files for GitHub App auth package and tests.
2. Update config structs/load/env/validate tests.
3. Update worker constructor wiring and push helper signatures.
4. Rewrite PR manager tests to use HTTP fakes instead of gh command stubs.
5. Update operator and getting-started docs plus `colin.toml.example`.
6. Run:

    go test ./...

Expected: all tests pass, including new GitHub App auth and PR manager tests.

## Validation and Acceptance

Acceptance is satisfied when:

- Colin can run with HTTP backend and GitHub App config without requiring `gh` CLI authentication.
- PR manager uses installation-token-authenticated GitHub REST calls for PR lookup/create.
- Git push operations use installation token auth path for GitHub HTTPS remotes.
- `go test ./...` passes with new tests covering JWT/token exchange and PR API interactions.

## Idempotence and Recovery

Code and config changes are additive and safe to re-apply. Tests are deterministic and use local fake HTTP servers. If implementation fails mid-way, rerun targeted tests for modified packages, then full `go test ./...`. No destructive migrations are involved.

## Artifacts and Notes

Implemented artifacts:

- New package `internal/githubapp` with `InstallationTokenProvider`.
- Updated worker auth and PR/merge flows in `internal/worker`.
- Updated configuration parsing/validation in `internal/config`.
- Updated docs in `README.md`, `docs/getting-started.md`, `docs/operator-runbook.md`, `docs/usage.md`, and `colin.toml.example`.
- Validation command run:

    go test ./...

## Interfaces and Dependencies

New interfaces/types to exist after implementation:

- `internal/githubapp.TokenProvider` with method `Token(ctx context.Context) (string, error)`.
- `internal/githubapp.InstallationTokenProvider` implementation for JWT signing and installation token exchange.
- Worker constructors accept/inject a token provider into merge and pull-request managers.

Updated worker interfaces:

- Pull request manager no longer depends on external binary execution.
- Git push helper supports optional auth method provision for pushes requiring installation token.

Revision note (2026-03-01): Initial plan authored after repository/codebase discovery and tracker mapping for `COLIN-84`.
Revision note (2026-03-01): Plan updated after implementation completion to reflect concrete file-level outcomes, decisions, and passing validation results.
