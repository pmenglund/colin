# Colin

Colin is a Go service that watches a Linear project, prepares a per-issue workspace, runs Codex against issues in active states, hands successful runs to either `Review` or `Refine`, and handles publish and merge automation from there.

## Colin and Symphony

Colin is this repository's Go implementation of the service model described by [openai/symphony](https://github.com/openai/symphony). The upstream Symphony project defines the language-agnostic orchestration model and also ships an experimental reference implementation; Colin applies that model to this repository's current Linear, GitHub, and Codex workflow.

In practice, that means Colin follows the same core shape as Symphony: a long-running orchestrator, repository-owned workflow policy, per-issue workspaces, agent runs for active tracker states, and explicit handoff states for review and merge. Colin also keeps Colin-specific repo automation and Linear/GitHub behaviors where this repository needs them.

`SPEC.md` is the local copy of the Symphony service specification that Colin uses as a design reference and conformance checklist when the service is changed. It is not loaded at runtime. The file Colin actually reads at startup and on reload is `WORKFLOW.md`, whose front matter provides typed runtime configuration and whose Markdown body provides the prompt template for coding runs.

## High-Level Flow

Colin runs as a long-lived process:

1. It loads `WORKFLOW.md` for runtime configuration and the prompt template.
2. It polls Linear for candidate issues in the configured project and tracked states.
3. It creates or reuses a workspace for each issue under the configured workspace root.
4. It runs Codex for issues in coding states.
5. It moves a successful coding run into `Review`, or into `Refine` when human clarification is still needed.
6. It performs git and GitHub automation for issues in publish or merge states.
7. It logs progress locally and posts high-level progress updates back to Linear as a comment thread on the issue.

By default Colin is started with:

```bash
go run .
```

By default Colin prints a single startup line with the local dashboard URL, for example `Colin is running. Web UI: http://127.0.0.1:8888`.

To keep the previous structured log stream on the terminal, pass `--verbose`:

```bash
go run . --verbose
```

To override the dashboard port, either set `server.port` in `WORKFLOW.md` or pass `--port`:

```bash
go run . --port 9999
```

If Colin is exposed through a reverse proxy or any non-loopback address, set `server.public_url` in `WORKFLOW.md` so the `Colin metadata` attachment in Linear points at the externally reachable web UI address instead of the local loopback bind.

You can also point it at a specific workflow file:

```bash
go run . /path/to/WORKFLOW.md
```

## How Colin Works

- Colin watches a single Linear project configured in `WORKFLOW.md`.
- The runtime behavior is driven by workflow front matter, including polling cadence, workspace root, tracked states, Codex command, and repo automation settings.
- `WORKFLOW.md` also carries the Codex prompt template and can optionally define `repo.pr_template` for the PR body Colin opens in GitHub. If that field is omitted, Colin uses its built-in default PR template.
- Each issue gets its own workspace directory. Colin preserves that workspace across retries and continuation runs.
- Colin keeps one orchestrator loop that reconciles running work, dispatches new work when slots are available, and retries stalled or incomplete work.
- During a run, Colin creates a top-level Linear progress comment and adds high-level replies as work advances so the current session can be followed without reading process logs.
- Colin prefixes its own Linear comments with `[colin]` and, when an issue returns from `Review` to `Todo`, injects human review comments from that latest review cycle into the next coding prompt as review feedback.
- Colin stores its own workflow metadata on the Linear issue via a dedicated `Colin metadata` attachment instead of hiding machine markers inside comment bodies.
- That attachment links to `/linear/issues/<issue-id>/metadata` in Colin and shows the latest persisted Colin metadata plus the captured Codex output for that issue.
- Colin also mirrors unresolved GitHub PR review threads back into the next coding prompt, waits for delayed review feedback to appear before starting that round, and reports review-sync status back to Linear while it waits.
- Colin uses `Refine` for clarification-only handoffs that do not yet have reviewable code or a PR.
- Colin also exposes the same live orchestrator snapshot through a loopback web UI at `/` and JSON at `/api/v1/state`.

## Linear State Handling

Colin moves a successful coding run from an active state into the appropriate handoff state. After that, it reacts to the issue's current state and performs the matching automation.

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

### Refine handoff state

`Refine` is a human-only clarification state. Colin does not dispatch coding, publish, or merge automation from it.

Colin moves an issue to `Refine` when:

- the coding run concludes the request is still too underspecified to implement safely
- the coding run reaches its maximum turn count without producing reviewable code

When Colin hands an issue to `Refine`, it posts a `[colin]` comment that explains what information is missing or why the run was capped.

Human action is expected in `Refine`:

- improve the issue description or requirements
- then move the issue back to `Todo` for another coding pass

### Publish handoff state

This is configured in `WORKFLOW.md` under `repo.publish_states` and currently is:

- `Review`

When an issue is moved to `Review`, Colin does not run another coding turn. Instead it:

- reuses the issue workspace
- usually commits any local changes if the workspace is dirty
- usually pushes the issue branch to the configured remote
- usually creates or reuses a GitHub pull request targeting the configured base branch
- uses `repo.branch_template` to choose a default branch name when the tracker does not provide one
- renders the PR body from `repo.pr_template` when one is configured, otherwise uses the built-in default template

`Review` is PR-only. Colin should only leave an issue in `Review` when the branch and PR are the intended next artifact for human review.

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
- checks the PR for Codex web review status before merging
- moves the issue back to `Review` with a Linear comment instead of merging when `chatgpt-codex-connector[bot]` has left a newer `eyes` reaction than `thumbs up`, or when unresolved review threads from that bot remain
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
- default issue branches to `colin/{{.issue.title}}` when Linear has no explicit branch name
- base publish and merge automation on branch `symphony`
- use `codex app-server` for coding runs
- serve the loopback web UI on `http://127.0.0.1:8888` unless `server.public_url` is set for an external address

## Operational Notes

- By default `go run .` stays quiet after startup and only prints the single `Colin is running. Web UI: ...` line.
- Pass `--verbose` to restore the structured service log stream for startup, dispatches, retries, Codex session progress, and handoff automation.
- Progress is also written back to Linear as one top-level comment thread per run phase, with replies for major events such as session start, turn completion, retries, publish completion, and merge completion.
- Colin's own Linear comments are prefixed with `[colin]` so they can be distinguished from human review feedback even when Colin posts through the same Linear account.
- The dashboard binds loopback only by default. The default port is `8888`, `server.port: 0` requests an ephemeral port for development/tests, and CLI `--port` overrides `server.port`.
- When `server.public_url` is unset, Colin uses the loopback UI address for Linear metadata links. Set `server.public_url` when operators need those links to resolve through a reverse proxy or another externally reachable hostname.
- The dashboard shows current running issues, queued retries, token totals, and the latest rate-limit snapshot. HTMX refreshes the task fragment in place so operators can inspect live work without reloading the whole page.
- The issue-specific metadata page is separate from the main dashboard and is meant for reviewing one issue's latest Colin run, including the captured Codex output that Colin persisted to Linear metadata.
- Colin automatically moves successful, reviewable coding runs into the first configured publish state, which is currently `Review`.
- Colin moves clarification-only or max-turn no-PR handoffs into `Refine` instead of `Review`.
- Colin does not automatically leave `Review`; a human still decides whether the issue goes back to `Todo` for another round or forward to `Merge`.
- Colin can automatically move an issue out of `Merge` when the Linear team has a git `merge` automation target configured, which this repository currently does (`Merged`).
