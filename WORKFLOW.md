---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
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
  base_ref: symphony

repo:
  publish_states:
    - Review
  merge_states:
    - Merge
  remote_name: origin
  merge_method: squash

hooks:
  timeout_ms: 60000

agent:
  max_concurrent_agents: 4
  max_turns: 8
  max_retry_backoff_ms: 300000

codex:
  command: codex app-server
  approval_policy: never
  thread_sandbox: danger-full-access
  turn_sandbox_policy:
    type: dangerFullAccess
  turn_timeout_ms: 3600000
  read_timeout_ms: 5000
  stall_timeout_ms: 300000
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
