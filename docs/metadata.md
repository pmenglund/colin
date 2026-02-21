# Colin Metadata in Linear

Colin stores workflow metadata on Linear issue attachments (not in issue descriptions).

## Attachment URL

Colin writes metadata to a Linear attachment with this URL:

`https://github.com/pmenglund/colin/blob/main/docs/metadata.md`

Colin expects the attachment `metadata` object to contain string key/value pairs.

## Metadata Keys

- `colin.lease_owner`: Worker identifier that currently owns the lease for an `In Progress` issue.
- `colin.execution_id`: Unique execution identifier for the active lease.
- `colin.lease_expires_at`: Lease expiry timestamp in RFC3339 UTC.
- `colin.last_heartbeat`: Last worker heartbeat timestamp in RFC3339 UTC.
- `colin.reason`: Human-readable reason for the current automated decision.
- `colin.needs_refine`: `"true"` or `"false"` flag indicating whether the issue needs refinement.
- `colin.ready_for_human_review`: `"true"` or `"false"` flag indicating readiness for human review.
- `colin.in_progress_outcome`: In-progress outcome label (`refine` or `human_review`).
- `colin.in_progress_comment_id`: Fingerprint for the generated in-progress comment, used for idempotence.
- `colin.spec_ready`: `"true"` or `"false"` flag indicating whether spec gating is satisfied for `Todo` processing.
- `colin.merge_ready`: `"true"` or `"false"` flag enabling `Merge -> Done` transition.
- `colin.worktree_path`: Absolute filesystem path to the task worktree.
- `colin.branch_name`: Git branch used for the task.
- `colin.thread_id`: Codex thread identifier associated with task execution.

## Notes

- All values are stored and read as strings.
- Unknown keys are ignored by workflow logic.
