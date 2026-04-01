---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  # Optional once you create a Linear webhook with `colin setup linear`.
  webhook_signing_secret: $LINEAR_WEBHOOK_SECRET
  project_slug: 0ece25450f8d
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
  repo_url: git@github.com:pmenglund/colin.git
  base_ref: main

repo:
  api_token: $GITHUB_TOKEN
  publish_states:
    - Review
  merge_states:
    - Merge
  codex_pr_reviews_enabled: true
  remote_name: origin
  merge_method: squash
  branch_template: colin/{{.issue.title}}

hooks:
  timeout_ms: 60000

agent:
  max_concurrent_agents: 4
  max_turns: 8
  max_retry_backoff_ms: 300000
  # When enabled, Colin first decides whether an issue is small enough to one-shot
  # or should get a persisted ExecPlan, then reuses that stored decision on later runs.
  create_exec_plan: true

codex:
  command: codex app-server
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
- Do not merge changes yourself during coding turns; Colin will publish in `Review` and merge in `Merge`.
- Summarize what changed, what was tested, and any remaining risk.

Output contract:
- If the issue is still too underspecified to implement safely, begin your final response with `COLIN_OUTCOME: NEEDS_SPEC`.
- After `COLIN_OUTCOME: NEEDS_SPEC`, explain what information is missing and include the exact sentence `The spec should be improved before implementation.`
- If the issue is implementable, begin your final response with `COLIN_OUTCOME: READY_FOR_REVIEW`.
- `Review` is PR-only. Clarification-only handoffs go to `Refine`.
