---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  # Optional for the tailnet-only Linear OAuth app flow driven by `colin setup linear app --connect`.
  oauth_client_id: 2a1218310b843851e0579bd3f19df4ef
  # Colin's current PKCE OAuth flow does not read a client secret from WORKFLOW.md.
  # Optional once you create a Linear webhook with `colin setup linear webhook`.
  webhook_signing_secret: $LINEAR_WEBHOOK_SECRET
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
    - Merged
    - Closed
    - Cancelled
    - Canceled
    - Duplicate

polling:
  interval_ms: 30000

workspace:
  root: ./.colin/workspaces

repo:
  api_token: $GITHUB_TOKEN
  # Optional once you configure the GitHub webhook with `colin setup github webhook`.
  webhook_signing_secret: $GITHUB_WEBHOOK_SECRET
  publish_states:
    - Review
  merge_states:
    - Merge
  codex_pr_reviews_enabled: true
  remote_name: origin
  merge_method: squash
  branch_template: colin/{{.issue.title}}

# Replace this example target with your own Linear project slug, repository URL,
# and base branch before running Colin.
targets:
  - name: example-target
    project_slug: your-linear-project-slug
    repo_url: git@github.com:your-org/your-repo.git
    base_ref: main

hooks:
  timeout_ms: 60000

agent:
  max_concurrent_agents: 4
  max_concurrent_agents_by_state:
    Merge: 1
  max_turns: 8
  max_retry_backoff_ms: 300000
  # When enabled, Colin first decides whether an issue is small enough to one-shot
  # or should get a persisted ExecPlan, then reuses that stored decision on later runs.
  create_exec_plan: true

codex:
  command: codex app-server
  cli_command: codex
  approval_policy: never
  thread_sandbox: danger-full-access
  turn_sandbox_policy:
    type: dangerFullAccess
  turn_timeout_ms: 3600000
  read_timeout_ms: 5000
  stall_timeout_ms: 300000

server:
  port: 8888
  # Optional: enable a dedicated local webhook listener, typically 8998.
  webhook_port: 8998
  # Optional when Colin webhook endpoints are reachable through a reverse proxy or another public host.
  # When unset, Colin will use an active Tailscale Funnel for `webhook_port` if one exists.
  # public_url: https://colin.example.com

# Optional: enable Slack issue summaries for tracked issues.
# Add `signing_secret: $SLACK_SIGNING_SECRET` if you also want the Slack App Home view.
# slack:
#   app_token: $SLACK_APP_TOKEN
#   bot_token: $SLACK_BOT_TOKEN
#   channel_id: C0123456789
#   signing_secret: $SLACK_SIGNING_SECRET
---

You are working on Linear issue {{.issue.identifier}}: {{.issue.title}}.

Repository rules:
- Follow `AGENTS.md`.
- Make changes only in this repository workspace.
- Prefer the smallest correct change that resolves the issue.
- Run relevant Go tests before you finish.

Issue context:
- State: {{.issue.state}}
{{- if .issue.url }}
- URL: {{.issue.url}}
{{- end }}
{{- if .issue.labels }}
- Labels:
{{- range .issue.labels }}
  - {{ . }}
{{- end }}
{{- end }}

{{- if .issue.description }}
Issue description:

{{.issue.description}}
{{- end }}

{{- if .issue.review_feedback }}
Review feedback:
{{- range .issue.review_feedback }}
- {{ .body }}
{{- end }}

{{- end }}

{{- if .issue.review_threads }}
GitHub review threads:
{{- range .issue.review_threads }}
- {{ .path }}{{- if .line }}:{{ .line }}{{ end }} by {{ .author }}: {{ .body }}
  {{- if .comment_url }} ({{ .comment_url }}){{ end }}
{{- end }}

{{- end }}

{{- if .attempt }}
This is continuation or retry attempt {{.attempt}}. Reuse the existing workspace state and continue from prior progress rather than restarting from scratch.
{{- end }}

Definition of done:
- Implement the requested change.
- Add or update tests when behavior changes.
- Leave the repo in a clean, reviewable state ready for `Review`.
- When Colin provides an ExecPlan working-copy path, keep that file updated as the living plan while you work.
- For ExecPlan-backed issues, do not treat the work as done until every checkbox under the ExecPlan's `## Progress` section is complete.
- Do not merge changes yourself during coding turns; Colin will publish in `Review` and merge in `Merge`.
- Summarize review handoff details using clear markdown sections so the Linear comment is easy to scan.

Output contract:
- If the issue is still too underspecified to implement safely, begin your final response with `COLIN_OUTCOME: NEEDS_SPEC`.
- After `COLIN_OUTCOME: NEEDS_SPEC`, explain what information is missing and include the exact sentence `The spec should be improved before implementation.`
- For ExecPlan-backed issues, also use `COLIN_OUTCOME: NEEDS_SPEC` when you cannot safely complete the remaining `## Progress` tasks.
- If the issue is implementable, begin your final response with `COLIN_OUTCOME: READY_FOR_REVIEW`.
- For ExecPlan-backed issues, use `COLIN_OUTCOME: READY_FOR_REVIEW` only after every checkbox under `## Progress` is complete.
- After `COLIN_OUTCOME: READY_FOR_REVIEW`, use exactly these markdown sections in this order: `## Why`, `## Before`, `## After`, and `## Evidence`.
- In `## Why`, explain why this change was made and what reviewer context or motivation matters for this PR.
- In `## Before`, describe the reviewer baseline for this PR only.
- In `## After`, describe only the change introduced by this PR.
- In `## Evidence`, prefer a screenshot. Otherwise include short terminal output in a fenced code block. Otherwise include the exact test command plus the specific tests that cover the change.
- Prefer Playwright screenshots for browser-visible changes, and prefer terminal or TUI screen captures for terminal-visible changes.
- Colin posts text comments to Linear, so always keep the section contents textual and mention any screenshots or screen grabs in words even when capture succeeds.
- `Review` is PR-only. Clarification-only handoffs go to `Refine`.
