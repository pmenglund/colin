# Colin Documentation Index

This directory contains operator documentation for the current worker implementation in:

- `cmd/worker.go`
- `internal/config/config.go`
- `internal/worker/`
- `internal/workflow/`

Current automation scope (as implemented in code):

- Candidate states are `Todo`, `In Progress`, and `Merge`.
- `Todo` moves to `In Progress` when specification is present; otherwise to `Refine`.
- `In Progress` is handled by Codex execution when the HTTP Linear backend is enabled, and can transition to `Review` or `Refine`.
- `Merge` moves to `Done` when metadata `colin.merge_ready` is `true`.
- HTTP backend candidate filtering skips blocked/dependent `Todo` issues based on inverse relations.

Operator-facing docs:

- `operator-runbook.md`: startup, ongoing operations, logs, merge queue behavior, fake-backend/offline mode, and disaster recovery.
- `troubleshooting.md`: symptom-driven recovery for config, runtime, workflow, and git/worktree failures.
