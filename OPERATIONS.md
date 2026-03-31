# Colin Operations Reference

This document holds the detailed operational and workflow reference that used to live in the root [README.md](README.md).

## Colin and Symphony

Colin is this repository's Go implementation of the service model described by [openai/symphony](https://github.com/openai/symphony). The upstream Symphony project defines the language-agnostic orchestration model and also ships an experimental reference implementation; Colin applies that model to this repository's current Linear, GitHub, and Codex workflow.

`SPEC.md` is the local copy of the Symphony service specification that Colin uses as a design reference and conformance checklist when the service is changed. It is not loaded at runtime. The file Colin actually reads at startup and on reload is `WORKFLOW.md`, whose front matter provides typed runtime configuration and whose Markdown body provides the prompt template for coding runs.

## High-Level Flow

Colin runs as a long-lived process:

1. It loads `WORKFLOW.md` for runtime configuration and the prompt template.
2. It polls Linear for candidate issues in the configured project and tracked states.
3. It creates or reuses a workspace for each issue under the configured workspace root.
4. When ExecPlan support is enabled, it decides whether each issue should be handled as a one-shot change or should get a stored ExecPlan, persists that decision on the issue, and only creates a plan for the second case.
5. It runs Codex for issues in coding states.
6. It moves a successful coding run into `Review`, or into `Refine` when human clarification is still needed.
7. It performs git and GitHub automation for issues in publish or merge states.
8. It logs progress locally and posts high-level progress updates back to Linear as a comment thread on the issue.

## Startup and Setup Details

By default Colin is started with:

```bash
go run .
```

This uses `--workflow WORKFLOW.md` implicitly. To point Colin at a different workflow file, pass the shared `--workflow` flag:

```bash
go run . --workflow /path/to/WORKFLOW.md
```

By default Colin prints a single startup line with both the local dashboard URL and the local Funnel setup page, for example `Colin is running. Web UI: http://127.0.0.1:8888 Setup: http://127.0.0.1:8888/setup/funnel`.

To keep the previous structured log stream on the terminal, pass `--verbose`:

```bash
go run . --verbose
```

Even without `--verbose`, Colin keeps recent internal logs in memory and serves them from `/api/v1/logs`. Add `?level=info` to hide `debug` records, or `?level=debug` to inspect the full buffer.

To override the dashboard port, either set `server.port` in `WORKFLOW.md` or pass `--port`:

```bash
go run . --port 9999
```

If operators need explicit URLs instead of Colin's defaults, set `server.webhook_public_url` for externally reachable webhook URLs and `server.ui_url` for operator-facing dashboard or metadata links. `server.public_url` remains as a deprecated fallback for the webhook public URL.

Before configuring incoming Linear or GitHub webhooks, use Colin's Tailscale readiness flow to make sure the webhook endpoints are publicly reachable:

```bash
go run . setup tailscale
```

That command checks Tailscale, explains that Colin uses Tailscale Funnel only for public webhook exposure, shows the exact `tailscale funnel` command Colin expects, and prints the final webhook URLs Colin will later accept.

To create or repair the watched project's Linear webhook after public ingress is ready, run:

```bash
go run . setup linear
```

That command creates or repairs one team-scoped Linear webhook for the watched project, points it at `<public-base-url>/webhooks/linear`, and reminds you to store the signing secret as `tracker.webhook_signing_secret: $LINEAR_WEBHOOK_SECRET`.

Because `--workflow` is a persistent root flag, the same override also applies to setup commands:

```bash
go run . --workflow /path/to/WORKFLOW.md setup tailscale
```

## Detailed Workflow Behavior

- Colin watches a single Linear project configured in `WORKFLOW.md`.
- The runtime behavior is driven by workflow front matter, including polling cadence, workspace root, tracked states, Codex command, and repo automation settings.
- `WORKFLOW.md` also carries the Codex prompt template and can optionally define `repo.pr_template` for the PR body Colin opens in GitHub. If that field is omitted, Colin uses its built-in default PR template.
- Each issue gets its own workspace directory. Colin preserves that workspace across retries and continuation runs.
- Colin keeps one orchestrator loop that reconciles running work, dispatches new work when slots are available, and retries stalled or incomplete work.
- On startup, Colin ensures the Linear issue label `paused` exists and never dispatches issues carrying that label.
- On startup, Colin also ensures the managed Codex PR review labels `codex-review: pending`, `codex-review: approved`, and `codex-review: unresolved-feedback` exist.
- During a run, Colin creates a top-level Linear progress comment and adds high-level replies as work advances so the current session can be followed without reading process logs.
- Colin prefixes its own Linear comments with `[colin]` and, when an issue returns from `Review` to `Todo`, injects human review comments from that latest review cycle into the next coding prompt as review feedback.
- Colin stores its own workflow metadata on the Linear issue via a dedicated `Colin metadata` attachment instead of hiding machine markers inside comment bodies.
- That attachment links to `/linear/issues/<issue-id>/metadata` in Colin and shows the latest persisted Colin metadata plus the captured Codex output for that issue.
- When `agent.create_exec_plan` is enabled, Colin first records whether the issue should be handled as `one_shot` or `exec_plan` in the `Colin metadata` attachment.
- When that stored decision is `exec_plan`, Colin keeps exactly one dedicated `Colin ExecPlan` attachment on the Linear issue and injects that plan into the first implementation turn.
- If an issue ever has multiple `Colin ExecPlan` attachments, Colin fails closed, moves the issue to `Refine`, and requires human cleanup instead of guessing which plan to use.
- Colin also records the canonical GitHub PR number, URL, state, head ref, and base ref in that metadata so one Linear issue stays bound to one PR.
- Colin also mirrors unresolved GitHub PR review threads back into the next coding prompt, waits for delayed review feedback to appear before starting that round only when the issue already has an associated PR, and reports review-sync status back to Linear while it waits.
- For non-terminal tracked issues that already have a linked GitHub PR, Colin mirrors the current Codex PR review status back into Linear labels so the board shows whether Codex review is pending, approved, or still has unresolved feedback. Colin removes stale Codex review labels when no current Codex review status applies.
- If the same failure repeats 3 times in a row for the same run type and issue state, Colin adds the `paused` label, posts a `[colin]` explanation, and stops retrying until a human removes the label.
- Colin uses `Refine` for clarification-only handoffs that do not yet have reviewable code or a PR.
- Colin also exposes the same live orchestrator snapshot through a loopback web UI at `/`, JSON state at `/api/v1/state`, and buffered internal logs at `/api/v1/logs`.

## Detailed Linear State Handling

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

Colin does not recompute the planning strategy when an issue returns from `Review` to `Todo`. It reuses the stored `exec_plan_decision`, so one-shot issues continue directly into coding and plan-backed issues continue from the existing canonical `Colin ExecPlan` attachment.

Additional `Todo` rule:

- Colin will not dispatch a `Todo` issue if any blocker is not in a terminal state.
- Colin will not dispatch any issue carrying the `paused` label.
- If the issue is returning from `Review` and already has an associated PR, Colin first polls that GitHub PR for unresolved review threads. Because GitHub review feedback can appear late, Colin keeps the issue in `Todo` and posts `[colin]` status updates in Linear until those threads appear or the sync window times out.
- If the issue returns to `Todo` without any associated PR, Colin skips GitHub review sync and starts work immediately using the Linear review feedback already on the issue.
- Once unresolved GitHub review threads are visible, Colin injects them into the next coding prompt alongside the human Linear review feedback from that same review cycle.

### Refine handoff state

`Refine` is a human-only clarification state. Colin does not dispatch coding, publish, or merge automation from it.

Colin moves an issue to `Refine` when:

- the coding run concludes the request is still too underspecified to implement safely
- the coding run reaches its maximum turn count without producing reviewable code
- the issue metadata is invalid, such as multiple `Colin ExecPlan` attachments on the same issue

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
Colin only moves a coding run into `Review` after Codex explicitly emits `COLIN_OUTCOME: READY_FOR_REVIEW` and the issue workspace contains reviewable repository changes. A clean workspace on a branch that is not ahead of base is not reviewable and will not be handed off to `Review`.

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
- keeps the issue in `Merge` while `chatgpt-codex-connector[bot]` review is still pending after a newer `eyes` reaction than `thumbs up`, and only moves it back to `Review` with a Linear comment when unresolved review threads from that bot remain
- if GitHub reports that the PR cannot be merged cleanly, asks Codex to merge the latest base branch into the issue branch, resolve conflicts in the workspace, and retry the merge while the issue stays in `Merge`
- moves the issue back to `Review` with a Linear comment when that automatic conflict-repair attempt fails, or when the repaired branch receives unresolved Codex review feedback; otherwise it keeps waiting in `Merge` while fresh Codex review is still pending
- merges the PR using the configured merge method
- checks the team's configured Linear git `merge` automation target and, when one is configured, updates the issue to that state as part of merge completion

Human action is still required after merge only if no post-merge Linear automation state is configured:

- move the Linear issue to its final terminal state

Human action is required before another merge attempt when Colin returns an issue from `Merge` to `Review`:

- resolve the merge problem on the PR branch, usually by merging the latest base branch and fixing conflicts
- push the updated branch
- move the issue back to `Merge` when it is ready to land

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
- decide once per issue whether the work is a one-shot change or needs a canonical ExecPlan, then reuse that stored decision on later coding turns
- use `codex app-server` for coding runs
- serve the loopback web UI on `http://127.0.0.1:8888` unless `server.ui_url` is set for an alternate operator-facing address
- keep the last `1000` internal log lines in memory by default, configurable with `server.log_buffer_lines`

## Operational Notes

- By default `go run .` stays quiet after startup and only prints the single `Colin is running. Web UI: ...` line.
- Pass `--verbose` to restore the structured service log stream for startup, dispatches, retries, Codex session progress, and handoff automation.
- Progress is also written back to Linear as one top-level comment thread per run phase, with replies for major events such as session start, turn completion, retries, publish completion, and merge completion.
- Colin's own Linear comments are prefixed with `[colin]` so they can be distinguished from human review feedback even when Colin posts through the same Linear account.
- Colin creates the Linear issue label `paused` at startup if it does not already exist.
- The `paused` label is a hard stop for Colin. Remove it when you want Colin to resume work on that issue.
- Colin automatically applies `paused` after 3 consecutive identical failures for the same run type and issue state, including repeated `review_publish` failures such as PR creation loops.
- Colin treats the PR recorded in Linear metadata as the canonical PR for that issue and will not silently switch to or create another PR if that record conflicts with the current branch or GitHub state.
- If multiple GitHub PR attachments are already linked to the same Linear issue and no canonical PR is recorded yet, Colin stops and requires human cleanup instead of guessing.
- The dashboard binds loopback only by default. The default port is `8888`, `server.port: 0` requests an ephemeral port for development/tests, and CLI `--port` overrides `server.port`.
- Colin keeps dashboard and metadata URLs private by default. If `server.ui_url` is unset, Linear metadata links point at the local Colin UI address.
- Colin uses Tailscale Funnel only for `/webhooks/*`. When `server.webhook_public_url` is unset, Colin auto-detects an active Funnel for the Colin port and derives the public webhook base URL from that Funnel. `server.public_url` is still accepted as a deprecated fallback for `server.webhook_public_url`.
- Colin can provision a Linear webhook for the watched project with `colin setup linear`. The Linear signing secret should be stored via `tracker.webhook_signing_secret: $LINEAR_WEBHOOK_SECRET`.
- Colin keeps a structured in-memory log buffer and exposes it at `/api/v1/logs`. The default buffer size is `1000` lines, and `server.log_buffer_lines` changes that retention count.
- `/api/v1/logs?level=info` hides `debug` chatter while keeping higher-severity records. `/api/v1/logs?level=debug` returns the full retained buffer.
- The dashboard shows current running issues, queued retries, token totals, the latest rate-limit snapshot, and paused issue indicators inside the `Linear issues` card. Clicking a paused indicator opens a Linear search for the paused issues in that state. The embedded browser refresh keeps the task fragment current without reloading the full page shell, and if a refresh fails the toolbar marks the dashboard as stale so operators know they are looking at the last successful snapshot.
- The issue-specific metadata page is separate from the main dashboard and is meant for reviewing one issue's latest Colin run, including the captured Codex output that Colin persisted to Linear metadata.
- Poll-loop logs such as tick start and rate-limit deferrals now log at `debug` level so they remain available for diagnosis without overwhelming the normal info-level view.
- Colin automatically moves successful, reviewable coding runs into the first configured publish state, which is currently `Review`.
- Colin moves clarification-only or max-turn no-PR handoffs into `Refine` instead of `Review`.
- If `review_publish` finds no reviewable repository changes, Colin moves the issue back to the working active state instead of retrying PR creation or applying the `paused` label.
- Colin does not automatically leave `Review`; a human still decides whether the issue goes back to `Todo` for another round or forward to `Merge`.
- Colin can automatically move an issue out of `Merge` when the Linear team has a git `merge` automation target configured, which this repository currently does (`Merged`).

## Tailscale Funnel Webhook Readiness

Colin now includes a dedicated readiness flow for the public ingress you need before configuring incoming webhooks.

Use either:

```bash
go run . setup tailscale
```

or the browser page at `/setup/funnel` once Colin is running.

To inspect readiness against a non-default workflow file, use:

```bash
go run . --workflow /path/to/WORKFLOW.md setup tailscale
```

The readiness flow checks:

- `tailscale` is installed and the backend is running
- MagicDNS is enabled
- a `/webhooks`-mounted Tailscale Funnel is proxying Colin's local port
- Colin responds locally at `/webhooks/readyz`
- Colin responds publicly at `/webhooks/readyz`

The recommended command is:

```bash
tailscale funnel --bg --https=443 --set-path=/webhooks 8888
```

If port `443` is already occupied by another Serve or Funnel configuration, Colin suggests `8443` or `10000` instead.

Tailscale Funnel requirements come from Tailscale itself and currently include:

- MagicDNS enabled
- HTTPS certificates enabled for the tailnet
- a `funnel` node attribute in the tailnet policy
- on macOS, a Tailscale client variant that supports Funnel port sharing

When Funnel is active and `server.webhook_public_url` is unset, Colin derives its public webhook base URL from the active Funnel automatically. If `server.webhook_public_url` is set, Colin uses that value instead and still shows Funnel diagnostics on the setup page.

Dashboard and issue-metadata pages are not meant to be exposed through Funnel in this setup. If operators need non-local UI links, set `server.ui_url` separately.

The setup page and CLI both show the final URLs you will paste into provider webhook settings later:

- GitHub: `<public-base-url>/webhooks/github`
- Linear: `<public-base-url>/webhooks/linear`

Colin now acknowledges `POST` requests to `/webhooks/linear` and verifies `Linear-Signature` when `tracker.webhook_signing_secret` is configured. GitHub webhook paths remain reserved and still return `501 Not Implemented`. The readiness endpoint is live today at `/webhooks/readyz`.
