# Trust Model

Colin is trusted automation for the Linear projects and repositories you configure in `WORKFLOW.md`. It can read and update watched Linear issues, create and modify local issue workspaces, run Codex with the configured sandbox and approval policy, push branches, open pull requests, and attempt merges in configured merge states.

Linear is Colin's control plane. Issue state, app-mode delegation, managed labels, review feedback, and configured project targets decide which work Colin is allowed to start or continue. In app mode, Colin only acts on `Todo`, `In Progress`, `Review`, or `Merge` issues delegated to the Colin app user; without app mode, anyone who can move issues into Colin-managed states in a watched project can trigger the corresponding automation.

Repository access is limited by the repository backend token and workflow target configuration. Colin stores work in per-issue Git worktrees under the configured workspace root and never runs agent work in a configured `checkout_path`. Codex receives the issue prompt and repository workspace, so treat issue descriptions, review comments, and repository content as input to an autonomous coding agent with the permissions configured under `codex:`.

Secrets should stay outside checked-in workflow files. Colin resolves credentials from environment variables or `.colin/auth.json`, validates configured provider credentials at startup where possible, and verifies Linear and GitHub webhook signatures when signing secrets are configured. Slack support is outbound and read-only for issue summaries unless you also configure the Slack app token and signing secret for interactive button handling and the app Home view.

Colin intentionally leaves human judgment at handoff points. `Review` is where humans inspect the pull request and decide whether the Linear issue should move forward. `Refine` requires a human to improve the issue or metadata. Merge automation only runs for issues moved into configured merge states.

Colin does not itself enforce an explicit human PR approval requirement before merge. If `codex_pr_reviews_enabled` is true for a target, Colin waits for the configured Codex Code Review signal before merging. Whether GitHub requires a human approval, passing checks, or other gates before accepting the merge is determined by that repository's GitHub branch protection rules, rulesets, and repository settings.
