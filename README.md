# Colin

Colin turns a Linear board into a managed delivery pipeline for coding work. Instead of manually driving one task at a time, you can keep many issues moving in parallel while Colin picks up ready work, hands implementation off to [Codex](https://platform.openai.com/docs/codex/overview), maintains a dedicated workspace for each issue, and pushes each task toward the next useful outcome.

The value is operational leverage: more tasks advancing at once, less branch and PR babysitting, and clearer handoffs for the moments where human judgment actually matters. Because Colin is driven through Linear state changes, you can manage the flow from the Linear app on your phone instead of being tied to a laptop session. Colin also works best with [Codex Code Review](https://help.openai.com/en/articles/11369540/) enabled on your GitHub repos so reviewable PRs get an additional automated pass before merge; OpenAI's setup instructions are [here](https://help.openai.com/en/articles/11369540/).

## Prerequisites

Before you run Colin, make sure you have:

- access to [Codex](https://platform.openai.com/docs/codex/overview) and a GitHub account or organization connected to it
- a GitHub token available to Colin via `repo.api_token`, `GITHUB_TOKEN`, or `GH_TOKEN` so publish and merge automation can talk to the GitHub API
- a Linear project and workflow with the states Colin uses for active work and handoffs

Optional but encouraged:

- [Codex Code Review](https://help.openai.com/en/articles/11369540/) enabled for the repositories where Colin will open pull requests
- public webhook ingress ready for Colin, typically via the Tailscale Funnel setup described in [OPERATIONS.md](OPERATIONS.md)

## What Using Colin Looks Like

Put work into `Todo`, let Colin pull it into `In Progress`, and let the board tell you what needs attention. Colin can keep multiple issues moving at the same time, route ready work to review, route unclear work to clarification, and finish merges once a PR is approved.

![Linear board showing Colin-managed issues moving through active and handoff states](docs/board.png)

Colin actively works issues in these coding states:

- `Todo`
- `In Progress`

When Colin starts a `Todo` issue, it moves it to `In Progress`, keeps retrying while the issue remains active, and stops work if the issue leaves the active state set.

Colin uses these handoff states:

- `Review`: Colin prepares the branch and pull request for human review. Human action is required to review the PR and then move the issue either back to `Todo` for more work or forward to `Merge`.
- `Refine`: Colin stops for clarification because the issue is underspecified, capped, or has invalid metadata. Human action is required to improve the issue and move it back to `Todo`.
- `Merge`: Colin performs merge automation. Human action is only required if Colin sends the issue back to `Review` because of merge or review problems, or if no post-merge Linear automation target is configured.
Colin treats these as terminal states and stops work when an issue enters them:

- `Done`
- `Merged`
- `Closed`
- `Cancelled`
- `Canceled`
- `Duplicate`

## Operate Many Tasks At Once

Colin is built to supervise a queue, not a single foreground session. It keeps one workspace per issue, tracks retries and rate limits, and gives operators a live dashboard so they can monitor fleet-level progress instead of watching individual coding runs. Colin itself is also developed using Colin, so the workflow is exercised continuously in the project that builds it.

![Colin dashboard showing active runs, workspace status, and API snapshot](docs/ui.png)

## How Colin Works

Colin runs as a long-lived orchestrator:

1. It watches the configured Linear project for issues in active states.
2. It creates or reuses a per-issue workspace so work can continue cleanly across retries and follow-up turns.
3. It advances ready issues toward the next handoff state: `Review`, `Refine`, or `Merge`.
4. It posts progress back to Linear and exposes a local dashboard for operators.

Relevant Linear `Issue` webhooks can also trigger a best-effort immediate reconciliation between poll intervals so Colin does not always wait for the next scheduled poll to react.

## Getting Started

Start Colin with the checked-in workflow:

```bash
go run .
```

Useful flags:

- `go run . --verbose` restores the structured service log stream in the terminal.
- `go run . --workflow /path/to/WORKFLOW.md` points Colin at a different workflow file.
- `go run . --port 9999` overrides the dashboard port.

Before configuring webhooks, make sure public ingress is ready:

```bash
go run . setup tailscale
```

After public ingress is available, create or repair the watched project's Linear webhook:

```bash
go run . setup linear
```

Once that webhook is configured, Colin acknowledges `POST` requests to `/webhooks/linear`, verifies `Linear-Signature` when `tracker.webhook_signing_secret` is configured, and uses relevant `Issue` deliveries to queue best-effort immediate reconciliation. Polling remains the fallback path if a webhook is delayed or dropped.

## Further Reading

The root README stays intentionally short. For the full operational reference, use:
- [OPERATIONS.md](OPERATIONS.md) for setup details, workflow defaults, detailed Linear state handling, webhook readiness, and operational notes
- [WORKFLOW.md](WORKFLOW.md) for runtime configuration and the Codex prompt template
- [APP.md](APP.md) for repository architecture
- [SPEC.md](SPEC.md) for the local Symphony design reference
