# COLIN-9 Lifecycle Test Coverage

This note documents what the automated lifecycle suite now verifies and which gaps remain.

## Covered Scenarios

The following tests run offline without real Linear or Codex network calls:

- `e2e/config_file_access_test.go`:
  - Verifies CLI worker execution path can run with `linear_backend = "fake"` and no external API connectivity.

- `e2e/lifecycle_happy_path_test.go`:
  - `TestLifecycleHappyPathTodoToDone` validates `Todo -> In Progress -> Review -> Merge -> Done` progression across worker cycles.
  - `TestLifecycleBlockedDependencyUnblocksWhenDependencyDone` validates blocked dependency gating and unblocking flow.
  - `TestLifecycleMergeQueueSerializedAcrossCycles` validates one-at-a-time merge queue progression across cycles.
  - Happy-path assertions include metadata persistence for:
    - `colin.worktree_path`
    - `colin.branch_name`
    - `colin.thread_id`
  - Happy-path rerun assertions verify idempotent behavior after completion.

- `internal/linear/inmemory_client_test.go`:
  - `TestInMemoryClientListCandidateIssuesSkipsBlockedUntilDependencyDone` validates fake backend candidate filtering for blocked `Todo` issues.

- `internal/worker/runner_test.go`:
  - Bootstrap metadata persistence and rerun idempotence.
  - In-progress thread metadata persistence.
  - Merge queue serialization behavior in the runner.
  - Concurrency and retry/conflict safety around in-progress execution paths.

## Known Gaps

- The suite does not validate real git-side merge/cleanup operations (`merge branch`, `push main`, `delete branch`, `delete worktree`) because current automated merge behavior in this branch is metadata-driven (`colin.merge_ready`) and uses fake Linear/test doubles.
- The suite validates thread metadata persistence using test doubles, but does not execute real Codex sessions in e2e tests.
- Branch-level git metadata persistence is not directly validated by the e2e suite.

## How to Run

Run all lifecycle-related tests from repository root:

    go test ./e2e ./internal/worker ./internal/linear

Run full repository validation:

    go test ./...
