# Colin Documentation Index

This directory contains operator and implementation-facing documentation for the current worker implementation in:

- `cmd/worker.go`
- `internal/config/config.go`
- `internal/linear/`
- `internal/codexexec/`
- `internal/worker/`
- `internal/workflow/`

Current automation scope (as implemented in code):

- Candidate states are `Todo`, `In Progress`, and `Merge`.
- `Todo` moves to `In Progress` when specification is present; otherwise to `Refine`.
- `In Progress` is handled by Codex execution when the HTTP Linear backend is enabled, and can transition to `Review` or `Refine`.
- `Merge` moves to `Done` when metadata `colin.merge_ready` is `true`.
- Merge queue processing is serialized: the runner processes at most one `Merge` issue per cycle.
- Candidate filtering skips issues with active blocking dependencies (HTTP backend via inverse relations; fake backend via `BlockedBy` references).
- Default work prompt is embedded from `prompts/work.md`; it can be overridden with `work_prompt_path` / `COLIN_WORK_PROMPT_PATH`.
- Default merge-prep prompt is embedded from `prompts/merge.md`; it can be overridden with `merge_prompt_path` / `COLIN_MERGE_PROMPT_PATH`.

Available docs:

- `getting-started.md`: first-time setup, initial validation, and first continuous run.
- `usage.md`: day-to-day CLI usage patterns and command quick reference.
- `operator-runbook.md`: startup, ongoing operations, logs, merge queue behavior, fake-backend/offline mode, and disaster recovery.
- `troubleshooting.md`: symptom-driven recovery for config, runtime, workflow, and git/worktree failures.
- `review-state-evidence.md`: deterministic `In Progress -> Review` comment format and reviewer verification checklist.
- `colin-9-lifecycle-coverage.md`: lifecycle/e2e coverage matrix, known gaps, and test commands.
