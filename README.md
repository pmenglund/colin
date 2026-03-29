# Colin

Colin is a Go service that watches a Linear project, prepares a per-issue workspace, runs Codex against issues in active states, moves successful coding runs into the publish handoff state, and handles publish and merge automation from there.

## High-Level Flow

Colin runs as a long-lived process:

1. It loads `WORKFLOW.md` for runtime configuration and the prompt template.
2. It polls Linear for candidate issues in the configured project and tracked states.
3. It creates or reuses a workspace for each issue under the configured workspace root.
4. It runs Codex for issues in coding states.
5. It moves a successful coding run into the first configured publish state.
6. It performs git and GitHub automation for issues in publish or merge states.
7. It logs progress locally and posts high-level progress updates back to Linear as a comment thread on the issue.

By default Colin is started with:

```bash
go run .
```

That also starts the local dashboard on `http://127.0.0.1:8888`.

To override the dashboard port, either set `server.port` in `WORKFLOW.md` or pass `--port`:

```bash
go run . --port 9999
```

You can also point it at a specific workflow file:

```bash
go run . /path/to/WORKFLOW.md
```

## How Colin Works

- Colin watches a single Linear project configured in `WORKFLOW.md`.
- The runtime behavior is driven by workflow front matter, including polling cadence, workspace root, tracked states, Codex command, and repo automation settings.
- Each issue gets its own workspace directory. Colin preserves that workspace across retries and continuation runs.
- Colin keeps one orchestrator loop that reconciles running work, dispatches new work when slots are available, and retries stalled or incomplete work.
- During a run, Colin creates a top-level Linear progress comment and adds high-level replies as work advances so the current session can be followed without reading process logs.
- Colin prefixes its own Linear comments with `[colin]` and, when an issue returns from `Review` to `Todo`, injects human review comments from that latest review cycle into the next coding prompt as review feedback.
- Colin also mirrors unresolved GitHub PR review threads back into the next coding prompt, waits for delayed review feedback to appear before starting that round, and reports review-sync status back to Linear while it waits.
- Colin can also move an underspecified issue to `Review` without opening a branch or PR when the coding run concludes the request needs clarification; in that case it leaves a `[colin]` comment explaining what needs to be improved in the spec.
- Colin also exposes the same live orchestrator snapshot through a loopback web UI at `/` and JSON at `/api/v1/state`.

## Linear State Handling

Colin moves a successful coding run from an active state into the first configured publish state. After that, it reacts to the issue's current state and performs the matching automation.

### Active coding states

These are configured in `WORKFLOW.md` under `tracker.active_states` and currently are:

- `Todo`
- `In Progress`

When an issue is in one of these states, Colin:

- dispatches Codex work for the issue
- moves `Todo` issues into `In Progress` when Colin actually starts working them
- keeps retrying or continuing while the issue remains active
- moves the issue to the first configured publish state when a coding run succeeds and the issue is still active
- stops the coding run when the issue leaves the active state set

When an issue moves from `Review` back to `Todo`, Colin reads the latest `Review -> Todo` cycle from the Linear timeline and injects human comments from that review window into the next prompt as review feedback. Comments starting with `[colin]` are treated as Colin-authored status updates and are excluded.

Additional `Todo` rule:

- Colin will not dispatch a `Todo` issue if any blocker is not in a terminal state.
- If the issue is returning from `Review`, Colin first polls the linked GitHub PR for unresolved review threads. Because GitHub review feedback can appear late, Colin keeps the issue in `Todo` and posts `[colin]` status updates in Linear until those threads appear or the sync window times out.
- Once unresolved GitHub review threads are visible, Colin injects them into the next coding prompt alongside the human Linear review feedback from that same review cycle.

### Publish handoff state

This is configured in `WORKFLOW.md` under `repo.publish_states` and currently is:

- `Review`

When an issue is moved to `Review`, Colin does not run another coding turn. Instead it:

- reuses the issue workspace
- usually commits any local changes if the workspace is dirty
- usually pushes the issue branch to the configured remote
- usually creates or reuses a GitHub pull request targeting the configured base branch

If the coding run determines the issue is still too underspecified to implement safely, Colin can also move the issue to `Review` without publishing a branch or PR. In that case it leaves a `[colin]` comment asking for the spec to be improved before implementation.

If Colin reaches the configured maximum turn count before it can finish a coding run, it still moves the issue to `Review`, but it includes a `[colin]` comment explaining that the run hit the turn cap and is being handed off for human review.

Human action is expected in `Review`:

- review the code and PR
- either move the issue back to an active state for more work
- or move the issue to `Merge` when it is ready to land

When Colin sends an issue back to `Review` after a returned review cycle, it first:

- replies to and resolves the unresolved GitHub review threads it addressed
- verifies whether any unresolved review threads remain
- posts a Linear summary that says what changed, what was tested, how many review threads were handled, and whether the issue is ready for review

If GitHub review-thread actions fail or unresolved review threads remain, Colin keeps the issue in `Todo` and posts that status in Linear instead of moving it to `Review`.

### Merge handoff state

This is configured in `WORKFLOW.md` under `repo.merge_states` and currently is:

- `Merge`

When an issue is moved to `Merge`, Colin:

- ensures the branch and PR exist
- merges the PR using the configured merge method
- checks the team's configured Linear git `merge` automation target and, when one is configured, updates the issue to that state as part of merge completion

Human action is still required after merge only if no post-merge Linear automation state is configured:

- move the Linear issue to its final terminal state

### Terminal states

These are configured in `WORKFLOW.md` under `tracker.terminal_states` and currently are:

- `Done`
- `Merged`
- `Closed`
- `Cancelled`
- `Canceled`
- `Duplicate`

When an issue enters a terminal state, Colin stops working it. If the issue was actively running, Colin cancels the run and cleans up the workspace for terminal completion.

## Current Workflow Defaults

The checked-in `WORKFLOW.md` currently configures Colin to:

- watch Linear project slug `0ece25450f8d`
- poll every 30 seconds
- use `./.colin/workspaces` as the workspace root
- clone `git@github.com:pmenglund/colin.git`
- base publish and merge automation on branch `symphony`
- use `codex app-server` for coding runs

## Operational Notes

- Colin uses structured logging so `go run .` shows service activity, dispatches, retries, Codex session progress, and handoff automation.
- Progress is also written back to Linear as one top-level comment thread per run phase, with replies for major events such as session start, turn completion, retries, publish completion, and merge completion.
- Colin's own Linear comments are prefixed with `[colin]` so they can be distinguished from human review feedback even when Colin posts through the same Linear account.
- The dashboard binds loopback only by default. The default port is `8888`, `server.port: 0` requests an ephemeral port for development/tests, and CLI `--port` overrides `server.port`.
- The dashboard shows current running issues, queued retries, token totals, and the latest rate-limit snapshot. HTMX refreshes the task fragment in place so operators can inspect live work without reloading the whole page.
- Colin automatically moves successful coding runs into the first configured publish state, which is currently `Review`.
- Colin does not automatically leave `Review`; a human still decides whether the issue goes back to `Todo` for another round or forward to `Merge`.
- Colin can automatically move an issue out of `Merge` when the Linear team has a git `merge` automation target configured, which this repository currently does (`Merged`).
