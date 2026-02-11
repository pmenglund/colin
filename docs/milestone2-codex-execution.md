# Milestone 2 Runbook: Codex Execution for In-Progress Issues

This runbook covers milestone 2 behavior where `colin` executes `In Progress` issues through Codex threads.

## Behavior

For each candidate issue in `In Progress`, `colin` now:

1. Starts Codex via `github.com/pmenglund/codex-sdk-go`.
2. Starts a thread and asks Codex to first determine whether the issue is sufficiently specified.
3. If underspecified: moves issue to `Refine` and comments with missing input requirements.
4. If sufficiently specified: executes task work and moves issue to `Human Review` with a completion summary comment.

`Todo` and `Merge` state behavior from milestone 1 remains unchanged.

## Required Configuration

Linear configuration is unchanged from milestone 1:

- `LINEAR_API_TOKEN`
- `LINEAR_TEAM_ID`

Codex execution requirements:

- `codex` CLI must be installed and available on `PATH`.
- Codex must have valid authentication in `CODEX_HOME` (or default `~/.codex`).
- Codex must be able to write session files in that location.

If your default home path is not writable in your execution environment, set `CODEX_HOME` to a writable path.

## Commands

Run one cycle:

    go run . worker run --once

Run one cycle with explicit writable Codex home:

    CODEX_HOME=/tmp/codex-home go run . worker run --once

Run continuously:

    go run . worker run

## Expected Output Shape

You should see log lines similar to:

    worker=<id> action=issues_fetched execution_id=<id> count=<n>
    ... codex thread started ...
    ... codex turn completed ...
    execution_id=<id> issue=<identifier> action=transition to="Refine" reason="specification requires refinement"

Or for executable issues:

    execution_id=<id> issue=<identifier> action=transition to="Human Review" reason="issue processed by codex"

## Recovery

If Codex fails to start due session-path permissions, either:

- make default `~/.codex` writable by the running user, or
- use a writable `CODEX_HOME` and ensure auth/config are present there.

If turn execution fails due auth/model access, refresh Codex authentication and rerun `worker run --once`.

## Validation

Before production use:

    go test ./...

Then run one cycle and verify in Linear that:

- state changed to `Refine` or `Human Review` as expected, and
- a matching comment was posted by the worker.
