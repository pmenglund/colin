# Colin

Colin is a Go service that watches a Linear project, prepares a per-issue workspace, runs Codex against issues in active states, hands successful runs to either `Review` or `Refine`, and handles publish and merge automation from there.

## Colin and Symphony

Colin is this repository's Go implementation of the service model described by [openai/symphony](https://github.com/openai/symphony). The upstream Symphony project defines the language-agnostic orchestration model and also ships an experimental reference implementation; Colin applies that model to this repository's current Linear, GitHub, and Codex workflow.

`SPEC.md` is the local copy of the Symphony service specification that Colin uses as a design reference and conformance checklist when the service is changed. It is not loaded at runtime. The file Colin actually reads at startup and on reload is `WORKFLOW.md`, whose front matter provides typed runtime configuration and whose Markdown body provides the prompt template for coding runs.

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

## How Colin Works

Colin runs as a long-lived orchestrator:

1. It loads `WORKFLOW.md` for runtime configuration and the prompt template.
2. It polls Linear for issues in the configured project and tracked states.
3. It creates or reuses one workspace per issue under the configured workspace root.
4. When ExecPlan support is enabled, it decides once whether the issue is a one-shot change or needs a stored ExecPlan and reuses that decision on later turns.
5. It runs Codex for issues in active coding states.
6. It moves successful coding work into `Review`, or into `Refine` when human clarification is still needed.
7. It performs publish and merge automation for issues in the configured handoff states.
8. It logs progress locally and posts high-level progress updates back to Linear.

## Linear State Handling

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

## Further Reading

The root README stays intentionally short. For the full operational reference, use:

- [OPERATIONS.md](OPERATIONS.md) for setup details, workflow defaults, detailed Linear state handling, webhook readiness, and operational notes
- [WORKFLOW.md](WORKFLOW.md) for runtime configuration and the Codex prompt template
- [APP.md](APP.md) for repository architecture
- [SPEC.md](SPEC.md) for the local Symphony design reference
