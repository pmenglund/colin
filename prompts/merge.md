# Merge Instructions

Issue identifier: `{{ LINEAR_ID }}`
Issue title: `{{ LINEAR_TITLE }}`
Source branch: `{{ SOURCE_BRANCH }}`
Base branch: `{{ BASE_BRANCH }}`
Remote: `{{ REMOTE_NAME }}`
Worktree path: `{{ WORKTREE_PATH }}`

You are responsible for merging a completed change branch into the configured base branch using safe, reviewable git practices.

Follow this workflow:

1. Confirm the source branch is not `{{ BASE_BRANCH }}` and the working tree is clean.
2. Fetch the latest remote state:
   - `git fetch {{ REMOTE_NAME }}`
3. Ensure local `{{ BASE_BRANCH }}` includes the latest remote base branch:
   - `git merge-base --is-ancestor {{ REMOTE_NAME }}/{{ BASE_BRANCH }} {{ BASE_BRANCH }}`
   - If this command exits non-zero, stop and report that local `{{ BASE_BRANCH }}` is behind `{{ REMOTE_NAME }}/{{ BASE_BRANCH }}` and must be synced in the canonical base-branch worktree before merge preparation can continue.
4. Rebase the source branch onto local `{{ BASE_BRANCH }}`:
   - `git checkout {{ SOURCE_BRANCH }}`
   - `git rebase {{ BASE_BRANCH }}`
5. If conflicts occur, resolve them carefully, run tests, and continue the rebase.
6. Run project validation before merge:
   - If a `go.mod` file exists in the current repo/worktree, run `go test ./...` from that module root.
   - If no `go.mod` exists, record that validation is not applicable and continue.
7. Merge the source branch:
   - Prefer `git merge --ff-only {{ SOURCE_BRANCH }}`.
   - If fast-forward is not possible, use `git merge --no-ff {{ SOURCE_BRANCH }}` to preserve merge context.
8. Verify the resulting history and status:
   - `git status`
   - `git log --oneline --decorate -n 10`
9. Push `{{ BASE_BRANCH }}`:
   - `git push {{ REMOTE_NAME }} {{ BASE_BRANCH }}`

Rules:

- Never merge without first rebasing the source branch on local `{{ BASE_BRANCH }}`.
- Never continue when local `{{ BASE_BRANCH }}` is behind `{{ REMOTE_NAME }}/{{ BASE_BRANCH }}`.
- Never check out `{{ BASE_BRANCH }}` in the issue worktree.
- Never run `git pull` in the issue worktree.
- Never use destructive history rewrites on shared branches.
- Never skip tests after conflict resolution.
- If `go test ./...` fails only because the repo is not a Go module, treat that as "validation not applicable" rather than a merge blocker.
- If any step fails, stop and report the exact blocker and command output.
