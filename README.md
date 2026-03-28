# Colin

Colin is a Go service that watches a Linear project, prepares a per-issue workspace, runs Codex against issues in active states, and handles publish and merge automation when issues move into handoff states.

## High-Level Flow

Colin runs as a long-lived process:

1. It loads `WORKFLOW.md` for runtime configuration and the prompt template.
2. It polls Linear for candidate issues in the configured project and tracked states.
3. It creates or reuses a workspace for each issue under the configured workspace root.
4. It runs Codex for issues in coding states.
5. It performs git and GitHub automation for issues in publish or merge states.
6. It logs progress locally and posts high-level progress updates back to Linear as a comment thread on the issue.

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
- Colin also exposes the same live orchestrator snapshot through a loopback web UI at `/` and JSON at `/api/v1/state`.

## Linear State Handling

Colin does not currently move Linear issues between states itself. Instead, it reacts to the issue's current state and performs the matching automation.

### Active coding states

These are configured in `WORKFLOW.md` under `tracker.active_states` and currently are:

- `Todo`
- `In Progress`

When an issue is in one of these states, Colin:

- dispatches Codex work for the issue
- keeps retrying or continuing while the issue remains active
- stops the coding run when the issue leaves the active state set

Additional `Todo` rule:

- Colin will not dispatch a `Todo` issue if any blocker is not in a terminal state.

### Publish handoff state

This is configured in `WORKFLOW.md` under `repo.publish_states` and currently is:

- `Review`

When an issue is moved to `Review`, Colin does not run another coding turn. Instead it:

- reuses the issue workspace
- commits any local changes if the workspace is dirty
- pushes the issue branch to the configured remote
- creates or reuses a GitHub pull request targeting the configured base branch

Human action is expected in `Review`:

- review the code and PR
- either move the issue back to an active state for more work
- or move the issue to `Merge` when it is ready to land

### Merge handoff state

This is configured in `WORKFLOW.md` under `repo.merge_states` and currently is:

- `Merge`

When an issue is moved to `Merge`, Colin:

- ensures the branch and PR exist
- merges the PR using the configured merge method

Human action is still required after merge:

- move the Linear issue to its final terminal state

### Terminal states

These are configured in `WORKFLOW.md` under `tracker.terminal_states` and currently are:

- `Done`
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
- The dashboard binds loopback only by default. The default port is `8888`, `server.port: 0` requests an ephemeral port for development/tests, and CLI `--port` overrides `server.port`.
- The dashboard shows current running issues, queued retries, token totals, and the latest rate-limit snapshot. HTMX refreshes the task fragment in place so operators can inspect live work without reloading the whole page.
- Colin currently automates repository actions, but it does not automatically change the Linear issue state after review or merge.
