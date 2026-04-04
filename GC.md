# Colin Review Findings

This file captures concrete cleanup targets found during a repository review against `APP.md` and the local `AGENTS.md` guidance.

## 1. Layering violations and cross-package leaks

### 1.1 GitHub adapter registration is scattered into lower layers

- `internal/config/config.go:11-13` blank-imports `internal/repohost/github` so config loading can populate the repo-host registry.
- `internal/repoops/manager.go:16-19` does the same inside repo automation.
- `internal/repoops/go_github_client.go:7-19` re-exports the concrete GitHub client from inside `repoops`.

Why this is a problem:

- `APP.md` says backend abstraction belongs in `internal/repohost` and repo publish/merge logic belongs in `internal/repoops`.
- Side-effect registration from `config` and `repoops` means backend selection is no longer owned by the composition root.
- It also keeps GitHub-specific compatibility surface alive inside packages that are supposed to be backend-neutral.

Suggested cleanup:

- Move adapter registration to one explicit wiring point in `internal/service` or `main`.
- Remove the GitHub compatibility wrapper from `repoops` once tests can target `repohost.Client` directly.

### 1.2 The Linear tracker parses GitHub pull request URLs directly

- `internal/tracker/linear/client.go:1999-2043` extracts attached PRs with `parseGitHubPullRequestAttachment`.

Why this is a problem:

- `internal/tracker/linear` is supposed to be tracker transport logic.
- PR URL parsing belongs behind the repo-host abstraction in `internal/repohost`, not inside the Linear adapter.
- This hard-codes `github.com/.../pull/...` into tracker code and breaks the backend boundary the repo is already trying to establish.

Suggested cleanup:

- Move attachment URL parsing behind `repohost.Adapter.ParsePullRequestURL`.
- Pass the configured adapter into the tracker, or normalize attachment URLs in a backend-aware helper closer to the service boundary.

## 2. Reimplemented validation or sanitization

### 2.1 GitHub webhook relevance is validated twice with duplicated logic

- `internal/app/github_webhook.go:175-202` filters deliveries with `shouldTriggerGitHubWebhook`.
- `internal/service/service.go:696-738` rechecks the same event/action matrix in `shouldQueueImmediateGitHubRefresh`.

Why this is a problem:

- The app layer already guarantees that only relevant GitHub webhook events reach the trigger callback.
- The service layer reimplements the same action whitelist, which creates drift risk.
- A future event update now requires touching two copies of the same policy.

Suggested cleanup:

- Keep repo matching in the service layer, but make the event/action relevance decision in exactly one place.
- Prefer passing a normalized "already relevant" event from `internal/app` to `internal/service`.

## 3. Bespoke helpers that should be replaced

### 3.1 Repository URL parsing is hand-rolled in multiple places

- `internal/githubauth/setup.go:35-136` implements `ParseRepositoryURL`.
- `internal/repoops/manager.go:643-677` implements a second parser in `parseRemoteRepository`.

Why this is a problem:

- The parsers already diverge.
- `githubauth.ParseRepositoryURL` only accepts `github.com`.
- `parseRemoteRepository` accepts arbitrary SCP-style hosts and guesses owner/repo from string splitting.
- Git remote parsing is subtle enough that Colin should not own multiple ad hoc parsers.

Suggested cleanup:

- Replace the raw string parsing with a battle-tested parser such as `github.com/whilp/git-urls`.
- Apply backend-specific validation after parsing rather than baking URL grammar into multiple helpers.

### 3.2 `colin resume` builds shell commands with a homemade quoting helper

- `cmd/resume.go:61-82` builds a shell string and quotes arguments with `shellQuote`.

Why this is a problem:

- This keeps shell parsing and escaping semantics inside Colin code.
- `cli_command` is still concatenated as raw shell text.
- The repo already carries `mvdan.cc/sh/v3` indirectly, which is safer than hand-maintaining quoting rules.

Suggested cleanup:

- Prefer an argv-based execution path instead of `bash -lc` when possible.
- If shell parsing must stay, use a battle-tested shell parser/quoting library rather than `shellQuote`.

## 4. Hidden assumptions that should be explicit

### 4.1 `tracker.Client` is not the real contract the service expects

- `internal/service/service.go:619-624` uses `client any` plus a private `watchedProjectIDsProvider`.
- `internal/service/service.go:751-758` hard-casts `runtime.Tracker` to `*linear.Client`.
- `internal/tracker/tracker.go:12-27` does not expose either `WatchedProjectIDs` or `SetUIBaseURLResolver`.

Why this is a problem:

- The service layer is implicitly coupled to the Linear implementation even though it is typed as `tracker.Client`.
- That coupling is currently hidden behind `any` and concrete type assertions rather than an explicit interface.

Suggested cleanup:

- Split tracker responsibilities into explicit interfaces such as `tracker.Client` and `tracker.RuntimeMetadata`.
- Make the service depend on those interfaces directly instead of `any` and `*linear.Client`.

### 4.2 Colin comment detection depends on a magic string prefix

- `internal/orchestrator/comments.go:227-235` prefixes Colin-authored comments with `[colin]`.
- `internal/tracker/linear/client.go:1441-1459` drops any review feedback comment whose body matches that prefix.
- `internal/tracker/linear/client.go:1597-1598` encodes the assumption in `isColinComment`.

Why this is a problem:

- This is an undocumented contract between comment creation and feedback extraction.
- If the prefix changes, or a human writes a comment starting with `[colin]`, review feedback classification changes silently.

Suggested cleanup:

- Persist an explicit marker in metadata or attachment-backed state instead of inferring authorship from free-form comment text.
- At minimum, document the contract in the tracker/comment interface.

### 4.3 Linear attachment metadata is an implicit schema, not a typed interface

- `internal/tracker/linear/client.go:1720-1804` reads Colin metadata from `map[string]any`.
- `internal/tracker/linear/client.go:1807-1824` reads ExecPlan metadata the same way.
- `internal/tracker/linear/client.go:1827-1855` writes the schema back out as another `map[string]any`.

Why this is a problem:

- The attachment title, URL shape, and metadata keys together form a storage protocol.
- That protocol is real, but it is expressed only through string keys and ad hoc coercions.
- Enum-like fields such as `exec_plan_decision`, `last_run_type`, and `last_outcome` are accepted as arbitrary strings with no validation.

Suggested cleanup:

- Introduce a typed codec for Colin attachment payloads with explicit validation.
- Keep the wire format in one place and fail loudly on invalid enum values instead of silently accepting them.

## 5. Unsafe `any` / untyped boundary handling

### 5.1 Linear GraphQL responses are left as `map[string]any` across the whole adapter

- `internal/tracker/linear/client.go:1242-1280` returns decoded GraphQL responses as `map[string]any`.
- `internal/tracker/linear/client.go:1333-1804` then walks those maps through `normalizeIssue`, `extractColinMetadata`, `extractExecPlan`, and other helpers.

Why this is a problem:

- The transport boundary never narrows the response into typed structs.
- Every downstream helper now has to guess field types and silently skip malformed data.

Suggested cleanup:

- Decode GraphQL responses into typed Go structs at `doQuery` call sites or behind typed query helpers.
- Keep the untyped JSON shape confined to the transport edge.

### 5.2 GitHub GraphQL responses follow the same unsafe pattern

- `internal/repohost/github/client.go:316-337` decodes GraphQL responses into `map[string]any`.
- `internal/repohost/github/client.go:394-557` then traverses them with `nestedValue`, `nestedSlice`, `stringValue`, and similar helpers.

Why this is a problem:

- Repo-host transport code has the same "untyped everywhere" issue as the Linear adapter.
- The boundary is especially important here because review-thread parsing drives publish/merge behavior.

Suggested cleanup:

- Replace `map[string]any` traversal with typed response structs or a typed GraphQL client.
- Keep parsing failures near the transport edge instead of sprinkling silent coercions across the package.

### 5.3 Codex protocol helpers accept raw messages instead of typed notifications

- `internal/agent/codex/app_server.go:149-170` builds a raw `msg` and passes it into summary and usage extraction.
- `internal/agent/codex/protocol.go:29-240` then processes `map[string]any` recursively.

Why this is a problem:

- The Codex SDK already provides typed notification payloads for some cases.
- The fallback path keeps a raw message map alive deep into event processing.

Suggested cleanup:

- Normalize notifications into a small typed internal event shape before they reach `summarizeMessage`, `extractUsage`, and `extractContextWindowUsage`.

## 6. Dead or unused compatibility surface

### 6.1 Some compatibility helpers are only kept alive by tests

- `internal/service/service.go:291-293` has `validateGitHubAccess`, which just delegates to `validateRepoAccess` and is only referenced in tests.
- `internal/service/service.go:627-633` has `watchedProjectID`, which is also only referenced in tests.
- `internal/tracker/linear/client.go:81` defines `Client.project`, and `internal/tracker/linear/client.go:101` assigns it, but nothing reads it.

Why this is a problem:

- These helpers and fields no longer contribute to production behavior.
- They make the backend-neutral migration look less complete than it is and increase the surface area future changes need to understand.

Suggested cleanup:

- Delete the dead helpers and field, and update tests to target the remaining production contract.
