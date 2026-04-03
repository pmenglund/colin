# Colin

Colin turns a Linear board into a managed delivery pipeline for coding work. Instead of manually driving one task at a time, you can keep many issues moving in parallel while Colin picks up ready work, hands implementation off to [Codex](https://platform.openai.com/docs/codex/overview), maintains a dedicated workspace for each issue, and pushes each task toward the next useful outcome. One Colin process can now supervise multiple Linear projects and multiple Git repositories from a single `WORKFLOW.md` when they share the same Linear and repository-host credentials.

The value is operational leverage: more tasks advancing at once, less branch and PR babysitting, and clearer handoffs for the moments where human judgment actually matters. Because Colin is driven through Linear state changes, you can manage the flow from the Linear app on your phone instead of being tied to a laptop session. Colin currently ships with a GitHub repository backend, and now routes repository-host setup and API access through a backend abstraction so GitLab or Gitea can be added later without rewriting all of setup and repo automation. Colin also works best with [Codex Code Review](https://help.openai.com/en/articles/11369540/) enabled on your GitHub repos so reviewable PRs get an additional automated pass before merge; OpenAI's setup instructions are [here](https://help.openai.com/en/articles/11369540/). When the optional `slack` workflow section is configured, Colin also mirrors each tracked issue into one Slack message that shows the current state and next action while linking out to the full details.

## Prerequisites

Before you run Colin, make sure you have:

- access to [Codex](https://platform.openai.com/docs/codex/overview) and a GitHub account or organization connected to it
- a repository backend token available to Colin via `repo.api_token`, `GITHUB_TOKEN`, or `GH_TOKEN` so publish and merge automation can talk to the configured backend API; today the only supported backend is GitHub, `GITHUB_TOKEN` is the recommended env var, and when a token is configured Colin validates it during startup and workflow reload so broken credentials fail fast
- a Linear project and workflow with the states Colin uses for active work and handoffs

Optional but encouraged:

- [Codex Code Review](https://help.openai.com/en/articles/11369540/) enabled for the repositories where Colin will open pull requests, with `repo.codex_pr_reviews_enabled: true` set in `WORKFLOW.md` when you want Colin to wait for that review before merging
- public webhook ingress ready for Colin, typically via the Tailscale Funnel setup described in [OPERATIONS.md](OPERATIONS.md), plus `LINEAR_WEBHOOK_SECRET` and `GITHUB_WEBHOOK_SECRET` exported when you enable signed provider webhooks
- a Slack bot token exported as `SLACK_BOT_TOKEN` plus a channel ID in `WORKFLOW.md` when you want Colin to keep one issue-summary message per tracked issue in Slack

## What Using Colin Looks Like

Put work into `Todo`, let Colin pull it into `In Progress`, and let the board tell you what needs attention. Colin can keep multiple issues moving at the same time, route ready work to review, route unclear work to clarification, and finish merges once a PR is approved.

![Linear board showing Colin-managed issues moving through active and handoff states](docs/board.png)

Colin actively works issues in these coding states:

- `Todo`
- `In Progress`

When Colin starts a `Todo` issue, it moves it to `In Progress`, keeps retrying while the issue remains active, and stops work if the issue leaves the active state set. If a reviewed issue is moved from `Review` back to `Todo` on the same PR, Colin resumes work immediately, reuses the same persisted Codex thread for that issue when available, and reuses any review feedback or still-open review threads it can already see. For issues backed by a stored ExecPlan, Colin also keeps the ExecPlan's `## Progress` section up to date during implementation and will not hand the issue to `Review` until every listed task is complete.

Colin uses these handoff states:

- `Review`: Colin prepares the branch and pull request for human review. Human action is required to review the PR and then move the issue either back to `Todo` for more work on the same PR or forward to `Merge`. ExecPlan-backed issues only reach `Review` after every task in the plan's `## Progress` section is checked off.
- `Refine`: Colin stops for clarification because the issue is underspecified, capped, or has invalid metadata. Human action is required to improve the issue and move it back to `Todo`. Colin also uses `Refine` when an ExecPlan-backed run cannot safely complete its remaining plan tasks or when the stored ExecPlan is invalid.
- `Merge`: Colin performs merge automation. Human action is only required if Colin sends the issue back to `Review` because of merge or review problems, or if no post-merge Linear automation target is configured.

Colin's `[colin]` comments are meant to make the next step explicit. Colin keeps one Linear progress thread per issue and continues replying in that same thread across retries, review returns, and merge follow-up until the issue reaches `Refine` or a terminal state. When an issue returns from `Review` to `Todo`, Colin says whether it is still waiting for GitHub review threads to sync or whether more PR feedback still needs to be addressed. When an issue is in `Merge`, Colin says whether it is retrying automatically while Codex review is pending, or whether a human needs to resolve review feedback or a merge problem before moving the issue forward again.

When Slack support is enabled, Colin mirrors that same high-level workflow view into Slack. Each tracked issue gets one Slack message that updates in place as the issue moves through the workflow. The message shows the issue state and next action directly, and links to Linear, the PR, Colin metadata, and the stored ExecPlan instead of inlining those details.

Colin treats these as terminal states and stops work when an issue enters them:

- `Done`
- `Merged`
- `Closed`
- `Cancelled`
- `Canceled`
- `Duplicate`

## Operate Many Tasks At Once

Colin is built to supervise a queue, not a single foreground session. It keeps one workspace per issue, tracks retries and rate limits, and gives operators a live dashboard so they can monitor fleet-level progress instead of watching individual coding runs. Colin itself is also developed using Colin, so the workflow is exercised continuously in the project that builds it.

When Colin is running, it also starts a local [`gops`](https://github.com/google/gops) agent so you can inspect the live process with commands such as `gops`, `gops stack <pid>`, or `gops memstats <pid>` without changing Colin's normal startup or shutdown flow.

![Colin dashboard showing active runs, workspace status, and API snapshot](docs/ui.png)

## How Colin Works

Colin runs as a long-lived orchestrator:

1. It watches the configured Linear project targets for issues in active states.
2. It creates or reuses a per-issue workspace so work can continue cleanly across retries and follow-up turns.
3. It routes each issue to the repository and base branch configured for that issue's target.
4. It advances ready issues toward the next handoff state: `Review`, `Refine`, or `Merge`.
5. It posts progress back to Linear and exposes a local dashboard for operators.

When a coding run finishes and Colin hands work off, the Linear issue comment is meant to be reviewable on its own. Colin now asks Codex to describe the change in before/after terms, include verification details, and prefer Playwright screenshots for browser-visible work or terminal or TUI captures for terminal-visible work, with textual fallback because the issue comment itself is text-only.

Those handoff comments also explain what Colin is doing next and what human action is required. That includes returned-review cases where GitHub review feedback has not synced yet, cases where review feedback still keeps the issue in `Todo`, and merge-conflict cases where Colin either repairs the branch automatically or sends the issue back to `Review` with concrete follow-up instructions.

Watched-project Linear `Issue` `create` webhooks, plus watched-project `Issue` `update` webhooks that change scheduling-relevant fields such as `stateId`, can also trigger a best-effort immediate reconciliation between poll intervals so Colin does not always wait for the next scheduled poll to react.

## Getting Started

The fastest way to get Colin running is:

Run these commands from the root of the git repository Colin should manage so `WORKFLOW.md` and any git-derived defaults apply to the correct checkout.

1. Export a valid `LINEAR_API_KEY` and `GITHUB_TOKEN` in your shell.
2. Run `colin config` to generate `WORKFLOW.md`.
3. Start Colin with `colin`.
4. Optionally set up Tailscale plus the watched-project Linear and GitHub webhooks if you want immediate refreshes between polling intervals.

The workflow-authoring command is:

```bash
colin config
```

If the selected workflow file is missing and Colin is running in an interactive terminal, Colin starts the same first-run setup automatically instead of failing immediately. This applies both to the default `WORKFLOW.md` and to custom paths passed with `--workflow`.

In an interactive terminal, `colin config` launches a Bubble Tea wizard that:

- collects the watched Linear project, repository URL, base branch, workspace root, port, and webhook preference
- validates token prefixes and required fields inline while you type
- fetches accessible Linear projects when `LINEAR_API_KEY` is available, while still allowing manual slug entry
- runs live preflight checks before writing `WORKFLOW.md`
- writes the workflow file without storing secrets in it

![`colin config` starting the interactive setup wizard before guiding the operator through project selection, validation, and workflow creation](docs/wizard.gif)

The setup wizard generates `WORKFLOW.md` and explains what still needs to be configured in the shell. It reads `LINEAR_API_KEY` and `GITHUB_TOKEN` from the current environment when available, and if either one is missing it can ask for a session-only value without writing that secret into `WORKFLOW.md`. New workflows write `repo.backend: github` explicitly so the repository backend is no longer implicit. Valid Linear keys must start with `lin_api_`, and GitHub tokens can be either fine-grained `github_pat_...` tokens or classic `ghp_...` tokens. In non-interactive contexts, Colin falls back to the line-oriented prompt flow so pipes and scripted tests still work.

### 1. Export the required secrets

Colin keeps secrets out of `WORKFLOW.md`. Export them in your shell before running setup or startup:

```bash
export LINEAR_API_KEY=lin_api_...
export GITHUB_TOKEN=github_pat_...
export LINEAR_WEBHOOK_SECRET=...
export GITHUB_WEBHOOK_SECRET=...
export SLACK_BOT_TOKEN=xoxb-...
```

`GITHUB_TOKEN` is the recommended variable name, though Colin also accepts `GH_TOKEN`. Fine-grained `github_pat_...` tokens are preferred, but classic `ghp_...` PATs also work.

If you enable the optional `slack` section in `WORKFLOW.md`, export `SLACK_BOT_TOKEN` before starting Colin. The workflow file should also name the destination `channel_id`.

### 2. Generate or refresh `WORKFLOW.md`

Run `colin config` when you need to create or refresh `WORKFLOW.md`. The wizard will guide you through:

- the default Linear project Colin should watch
- the default repository Colin should prepare branches and PRs for
- the default base branch Colin should branch and merge from
- the workspace root Colin should use for per-issue worktrees
- the local dashboard port
- whether you want webhook follow-up guidance

At the review step Colin runs live checks when it has the required credentials:

- Linear config validation
- required Linear workflow states
- required managed Linear labels
- repository backend API access

Once the review passes, the wizard writes `WORKFLOW.md`.

For multi-target workflows, keep shared credentials and shared state lists at the top level, then add a `targets:` list where each item provides `project_slug`, `repo_url`, and `base_ref`. The interactive setup flow still writes a single-target workflow today, so multi-target workflows are edited directly in `WORKFLOW.md`.

Once the workflow file and `LINEAR_API_KEY` are available, Colin validates that the configured Linear states exist and ensures its managed labels exist before startup completes.

### 3. Create the repository backend token if you do not already have one

For the current GitHub backend, the fastest path is:

```bash
colin setup repo
```

That command dispatches through the configured repository backend. Today it prints a pre-filled GitHub fine-grained token link for the watched repo and the exact settings Colin expects:

- resource owner: the repo owner or org
- repository access: `Only select repositories`
- selected repository: the watched repo
- repository permissions: `Contents: Read and write` and `Pull requests: Read and write`
- export target: `GITHUB_TOKEN`

If fine-grained personal access tokens are blocked by org policy or approval flow, fall back to a classic personal access token with the `repo` scope. Classic tokens may also require `Configure SSO` after creation in orgs that use SAML SSO.

### 4. Start Colin

Start Colin with the checked-in or newly generated workflow:

```bash
colin
```

In an interactive terminal, `colin` opens a Bubble Tea runtime dashboard by default. The overview shows the local and public URLs Colin is serving plus the current workers and their state. Press `l` to switch to the buffered log view, use the arrow keys or page keys to scroll, and press `q` to begin a shutdown drain. Once shutdown begins, Colin stops starting new work, shows a shutdown indicator in both dashboards, and waits for active workers to go idle; press `q` again or `esc` to exit immediately.

These docs assume `colin` is installed on your `PATH`.

Useful flags:

- `colin --verbose` restores the structured service log stream in the terminal.
- `colin --workflow /path/to/WORKFLOW.md` points Colin at a different workflow file.
- `colin --port 9999` overrides the dashboard port.
- `colin --workflow /path/to/WORKFLOW.md config` generates or refreshes a workflow file at a custom path.

### 5. Optional: enable webhook-driven refreshes

If you opted into webhooks during setup, Colin will remind you that webhook exposure requires Tailscale. Before configuring webhooks, make sure public ingress is ready:

```bash
colin setup tailscale
```

After public ingress is available, use `colin setup ...` to prepare the external integrations referenced by `WORKFLOW.md`, such as the watched project's Linear webhook and the watched repository's GitHub webhook settings:

```bash
colin setup linear webhook
colin setup linear app
colin setup github webhook
```

Once those webhooks are configured, Colin acknowledges `POST` requests to `/webhooks/linear` and `/webhooks/github`, verifies `Linear-Signature` when `tracker.webhook_signing_secret` is configured, verifies `X-Hub-Signature-256` when `repo.webhook_signing_secret` is configured, and uses relevant watched-project Linear issue deliveries plus relevant watched-repository GitHub pull-request review deliveries to queue best-effort immediate reconciliation. The webhook never dispatches workers directly, and polling remains the fallback path if a webhook is delayed, dropped, or arrives before the orchestrator is ready to accept immediate refreshes.

`colin setup linear app` prints the current self-hosted Linear app sketch for this workflow: use an assignable app user, point the app at the same `/webhooks/linear` endpoint, subscribe the app to `AgentSessionEvent`, and keep the existing issue-webhook wake-up path enabled. The answer to "should it disable the webhook?" is no. App-triggered sessions should be additive to Colin's current poll-plus-webhook scheduling, not a replacement for it.

`server.port` controls the local Colin UI. When webhook setup is enabled, `colin config` also writes `server.webhook_port`, which defaults to `8998`, so Tailscale Serve can proxy the UI while Tailscale Funnel proxies `/webhooks` on a separate public HTTPS port such as `8443`.

Linear metadata attachments point at `server.ui_url` when configured. If that is unset but Tailscale Serve proxies Colin from `/`, Colin uses the preferred Tailscale Serve URL for metadata links, favoring HTTPS when available; otherwise it falls back to the local loopback dashboard URL.

## Releasing

Colin releases are built with GoReleaser and published from GitHub Actions when you push a version tag that starts with `v`, for example `v0.1.0`.

To validate the release packaging locally without publishing anything, run:

```bash
task release-check
task release-snapshot
```

That writes release archives and `checksums.txt` into `dist/`.

To cut a real release after the release workflow is present on the default branch, create and push an annotated tag:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

GitHub Actions should then run the `release` workflow and publish a GitHub Release for that tag with downloadable archives and checksums. For the manual repository settings this workflow depends on, see [OPERATIONS.md](OPERATIONS.md).

## Further Reading

The root README stays intentionally short. For the full operational reference, use:
- [OPERATIONS.md](OPERATIONS.md) for setup details, workflow defaults, detailed Linear state handling, webhook readiness, and operational notes
- [WORKFLOW.md](WORKFLOW.md) for runtime configuration and the Codex prompt template
- [APP.md](APP.md) for repository architecture
- [SPEC.md](SPEC.md) for the local Symphony design reference
