# repo-root-runtime — Learnings

## Task 5: Cwd resolution and validation in execenv

### Implementation notes

- Changed the condition from `params.EnableAgentWorkdir && params.AgentWorkdir != ""` to just `params.EnableAgentWorkdir`. This allows the empty/whitespace-only case to enter the validation block and log a warning, as required by the plan.
- Used `strings.FieldsFunc` splitting on both `/` and `\` in `hasDotDotSegment` so the helper works cross-platform.
- The containment check (`HasPrefix` on resolved vs workDir) is defense-in-depth — after path validation (relative-only, no `..`), `filepath.Join` cannot escape, but the check guards against future code changes.
- `os.MkdirAll(env.Cwd, 0o755)` is called unconditionally after Cwd resolution. When Cwd == workDir, this is a no-op since workDir was already created above.
- `writeContextFiles` still receives `workDir` (not `env.Cwd`) — Task 7 will update this.
- `prepareCursorMcpConfig` and `prepareOpenclawConfig` still receive `workDir` — Task 10 will update these.

### Test notes

- The pre-existing `TestPrepareAgentCwd` test from Task 4 had to be updated: "keeps absolute AgentWorkdir" now expects fallback, and "empty AgentWorkdir keeps default" verifies a warning is logged.
- Added `TestResolveAgentCwd` with 10 table-driven subtests covering all required cases.
- Added `TestResolveAgentCwdDisabled` to verify the disabled path.
- Log capture uses `slog.NewTextHandler` writing to `bytes.Buffer` — a lightweight pattern matching the codebase style.

### Key decisions

- Absolute paths are always rejected (not resolved as-is). The API handler also rejects them, so this is defense-in-depth.
- `my..dir` is accepted because `..` is checked as a path segment, not a substring.
- Trailing slashes are cleaned by `filepath.Clean` before joining.

## Task 6: Pre-checkout github_repo resources

### Implementation notes

- Exported `repoNameFromURL` as `RepoNamesFromURL` in `repocache/cache.go`. An identical unexported copy lives in `execenv/git.go` (used by the context file writer); both remain independent.
- Pre-checkout inserted in `runTask` right after `env` is set (from reuse or prepare), before the belt-and-suspenders mark and temp dir setup.
- Guard: `task.EnableAgentWorkdir && !env.LocalDirectory`. Local directory tasks are already excluded by the claim handler but this is defense-in-depth.
- Collision guards: skip repo if `repoName == task.AgentWorkdir` (warn), skip duplicate repo names (warn). Both are non-fatal.
- Ref resolution mirrors the health checkout endpoint: use `repo.Ref` if set, otherwise fall back to `d.taskRepoDefaultRef`.
- `CoAuthoredByEnabled` uses `d.workspaceCoAuthoredByEnabled(task.WorkspaceID)`; `IsolatedGitMetadata` uses `repoCheckoutModeFor(provider, runtime.GOOS) == repoCheckoutModeIsolated` — same pattern as the health endpoint at `health.go:208-216`.
- Failed `CreateWorktree` calls are logged as warnings and do not block task launch (best-effort pre-checkout).

### Key decisions

- Pre-checkout runs for both fresh and reused environments. `CreateWorktree` handles existing worktrees gracefully (it updates rather than recreates), so this is safe.
- Reused envs may already have repos checked out from a prior run — the update path in `CreateWorktree` is cheap (git fetch + branch reset) compared to skipping repos and risking stale code.

## Task 7: Write context/runtime files to agent Cwd

### Implementation notes

- `writeContextFiles` and `InjectRuntimeConfig` already accept a directory parameter — no function signature changes needed. Only the call sites are updated.
- At `execenv.go:364` (Prepare): changed from `writeContextFiles(workDir, ...)` to `writeContextFiles(env.Cwd, ...)`. Since `env.Cwd` falls back to `workDir` when `EnableAgentWorkdir` is false, behavior is unchanged for disabled tasks.
- At `daemon.go:4382` (runTask): changed from `InjectRuntimeConfig(env.WorkDir, ...)` to `InjectRuntimeConfig(env.Cwd, ...)`. Same fallback property.
- The Reuse path (`execenv.go:605`) needs no change: `shouldReusePriorWorkdir` (daemon.go:3749) returns false when `task.EnableAgentWorkdir` is true, so the Reuse code path is unreachable with custom workdir enabled.
- Cleanup paths (`CleanupRuntimeConfig`, `CleanupSidecars`) need no change: custom workdir is blocked for `local_directory` tasks (where cleanup matters); cloud tasks get their envRoot wiped wholesale by the GC loop.

### Key decisions

- Used `env.Cwd` unconditionally rather than a conditional (`if params.EnableAgentWorkdir { env.Cwd } else { workDir }`) because the fallback is structurally guaranteed — `Cwd` is set to `workDir` at initialization and only diverges inside the `if params.EnableAgentWorkdir` block.
- Added `TestPrepareContextFilesWithAgentCwd` with two subtests: enabled (files go to Cwd) and disabled (files go to WorkDir). The subtests verify both `writeContextFiles` output and `InjectRuntimeConfig` placement.
- The `ReuseParams` struct was not modified because custom-workdir reuse is blocked upstream.

## Task 8: Set agent ExecOptions.Cwd to resolved Cwd

### Implementation notes

- Changed `daemon.go:4629` from `Cwd: env.WorkDir` to `Cwd: env.Cwd`. This is safe unconditionally because `env.Cwd` is initialized to `workDir` in `execenv.Prepare` and only diverges inside the `if params.EnableAgentWorkdir` block — when disabled, `env.Cwd == env.WorkDir` and behavior is unchanged.
- No additional conditional guard needed since the Environment struct guarantees `Cwd` always holds the correct value regardless of feature toggle state.
- The change flows through `agent.Backend.Execute` → `cmd.Dir = opts.Cwd` (e.g. claude.go:65), so the spawned agent process inherits the correct working directory.

### Test notes

- Added `TestRunTaskSetsAgentCwd` in `daemon_test.go` with two subtests:
  - `enabled sets Cwd to subdirectory`: verifies the agent process PWD ends with `workdir/src` when `EnableAgentWorkdir: true, AgentWorkdir: "src"`.
  - `disabled uses WorkDir`: verifies the agent process PWD ends with just `workdir` (no subdirectory) when `EnableAgentWorkdir: false`.
- Follows the `leader_workdir_reuse_test.go` pattern: a fake shell-script agent binary that records `$PWD` and outputs valid Claude stream-json events. The test creates a non-leader `Task` and calls `runTask` directly.
- Both subtests verify the agent completed (`result.Status == "completed"`) before checking the recorded Cwd, ensuring the test fails on backend errors rather than silently passing on a stale Cwd file.

### Key decisions

- Used the `leader_workdir_reuse_test.go` integration-test pattern (real `runTask` call, fake agent binary) rather than a unit-level mock because the ExecOptions construction is deep inside `runTask` and extracting it for a standalone test would require non-trivial refactoring.
- The "disabled" subtest includes a negative assertion (checking the subdirectory does NOT appear in PWD) to guard against accidental Cwd contamination.

## Task 9: Update runtime brief for pre-checked-out repos and custom Cwd

### Implementation notes

- Added `EnableAgentWorkdir`, `WorkDir`, and `AgentWorkdir` fields to `TaskContextForEnv` in `execenv/execenv.go`. These are populated in `runTask()` from the task and env structs — `EnableAgentWorkdir` and `AgentWorkdir` come from the task at construction time, while `WorkDir` is set right after `env` is established (post-reuse/prepare + pre-checkout).
- Updated `writeRepositories` (`runtime_config_sections.go`): when `EnableAgentWorkdir` is true, the section tells the agent repos are already checked out at `{WorkDir}/{repoName}` and accessible from Cwd as `../{repoName}`. Also mentions the agent's Cwd path. When disabled, emits the existing `multica repo checkout <url>` instructions unchanged.
- Updated `writeProjectContext`: when `EnableAgentWorkdir` is true, replaces the `multica repo checkout` instruction with pre-checked-out path listings for `github_repo` resources. Parses each `github_repo` resource's `resource_ref` JSON to extract the URL and compute the repo name via the existing `repoNameFromURL` helper.
- Updated `writeWorkflowChat`: added `ctx TaskContextForEnv` parameter. When `EnableAgentWorkdir` is true and repos exist, the chat workflow says "Repos are already checked out" instead of instructing `multica repo checkout`. When disabled, the legacy instruction is unchanged.
- The `encoding/json` import was added to `runtime_config_sections.go` for parsing github_repo resource_ref payloads in `writeProjectContext`.

### Key decisions

- `repoNameFromURL` (unexported, in `execenv/git.go`) was reused since both files are in the same `execenv` package — no need to export or duplicate.
- `WorkDir` is set on `taskCtx` after the `env` is available (after the reuse/prepare block), not in the initial struct literal. The existing code flow already passes `taskCtx` to `InjectRuntimeConfig` later, so the deferred assignment is safe.
- The `writeAvailableCommands` section (which lists all CLI commands) intentionally still includes `multica repo checkout` even when custom workdir is enabled — the CLI command still exists and the agent may use it for non-pre-checked-out repos.
- Tests verify section-level content (not file-level) to avoid false negatives from the Available Commands section.
- Five new tests added: `TestPreCheckoutReposInMetaSkill`, `TestPreCheckoutReposInMetaSkillDisabled`, `TestPreCheckoutProjectResourcesInMetaSkill`, `TestPreCheckoutChatWorkflow`, `TestPreCheckoutChatWorkflowDisabled`.

## Task 10: Adjust OpenClaw and Cursor sidecar configs to use resolved Cwd

### Implementation notes

- Changed two call sites in `execenv.go` Prepare to pass `env.Cwd` instead of `workDir`:
  - Line 443: `prepareCursorMcpConfig(envRoot, env.Cwd, ...)` — Cursor `.cursor/mcp.json` now lands under the agent's Cwd when custom workdir is enabled.
  - Line 461: `prepareOpenclawConfig(envRoot, env.Cwd, ...)` — OpenClaw wrapper pins `agents.defaults.workspace` (and per-agent workspace) to the agent's Cwd.
- Used `env.Cwd` unconditionally rather than a conditional because `env.Cwd` is initialized to `workDir` at Prepare's start and only diverges inside the `if params.EnableAgentWorkdir` block. This is the same pattern as Tasks 7 and 8.
- Reuse path (lines 678, 699) was intentionally NOT changed because `shouldReusePriorWorkdir` returns false for custom-workdir tasks, making the Reuse code path unreachable when the feature is on. When it is reached (disabled mode), `env.Cwd == params.WorkDir` by construction.
- No signature changes to `prepareCursorMcpConfig` or `prepareOpenclawConfig` — the second parameter already accepts a path, and we simply pass `env.Cwd` instead of `workDir`.

### Test notes

- Added four new tests in `execenv_test.go`:
  - `TestPrepareOpenclawConfigUsesCwdWhenWorkdirEnabled`: Uses the existing `installOpenclawStub` pattern to synthesize a per-task wrapper, then asserts `agents.defaults.workspace` equals `env.Cwd` (not `env.WorkDir`), and verifies Cwd ends with the subdirectory suffix.
  - `TestPrepareOpenclawConfigUsesWorkDirWhenDisabled`: Same stub pattern, asserts workspace equals `env.WorkDir` (which equals Cwd by default).
  - `TestPrepareCursorMcpConfigUsesCwdWhenWorkdirEnabled`: Calls `Prepare` with cursor provider + managed mcp_config + `EnableAgentWorkdir: true`, then verifies `.cursor/mcp.json` exists under `env.Cwd` and NOT under `env.WorkDir`.
  - `TestPrepareCursorMcpConfigUsesWorkDirWhenDisabled`: Same but with feature disabled, verifies `.cursor/mcp.json` under `env.WorkDir`.

### Key decisions

- OpenClaw's `prepareOpenclawConfig` is complex (CLI delegation, $include wrapping, managed mcp_config snapshot isolation). Rather than refactoring the function to accept an explicit workspace parameter, the simplest change was at the single call site — pass `env.Cwd` instead of `workDir`. The function's internal `buildPerTaskOpenclawConfig` uses the second parameter as the workspace value for both `agents.defaults.workspace` and `agents.list[].workspace`, so this correctly targets both.
- The Cursor test verifies the `.cursor/mcp.json` file location (under Cwd or WorkDir) rather than the approval/payload content, since `cursorProjectRoot` derives from the passed directory and the approval keys depend on the project root. The file location is the user-visible effect.
- Hermes and Codex sidecar configs were NOT touched — they manage provider-specific home/overlays and don't pin workspace directories.

## Task 13: Verify Cwd resolution unit test coverage

### Findings

- The `TestResolveAgentCwd` table-driven test added in Task 5 already contains 10 subtests covering all required acceptance criteria:
  - valid relative paths: "valid relative path accepted", "nested relative path accepted"
  - `..` segments: "dot-dot segment falls back", "dot-dot in nested path falls back"
  - absolute paths: "absolute path falls back"
  - empty string: "empty string with enable=true falls back"
  - whitespace-only: "whitespace-only falls back"
  - paths that clean to `.`: "dot resolves to root, falls back"
- Paths that escape `WorkDir` are covered by the `..` segment rejection cases, which are the canonical escape vectors. The earlier `TestPrepareAgentCwd` also covers the absolute/empty fallbacks.
- `TestResolveAgentCwdDisabled` confirms the feature toggle is respected.
- No new tests were needed; the task is already complete.

### Verification

- `go test ./internal/daemon/execenv/...` passes: `ok github.com/multica-ai/multica/server/internal/daemon/execenv 1.705s`
- `go vet ./internal/daemon/execenv/...` reports no issues.
- `gofmt -l internal/daemon/execenv/execenv_test.go internal/daemon/execenv/execenv.go` reports no formatting issues.

## Task 12: Update core project types and mutation hooks

### Implementation notes

- `Project.settings` changed from required (`settings: Record<string, unknown>`) to optional (`settings?: Record<string, unknown>`) for backward compatibility. The DB column is `NOT NULL DEFAULT '{}'` so the API always returns it, but making it optional is defensive.
- `UpdateProjectRequest.settings` changed from `settings?: Record<string, unknown>` to `settings?: Record<string, unknown> | null`. The `| null` is needed to allow callers to send `{ settings: null }` to clear the settings — the Go handler treats `nil` as "don't update" and only clears when the key is present with a null value.
- The mutation hook `useUpdateProject` needs a normalization step in `onMutate`: `settings ?? undefined` collapses `null` to `undefined` because `Project.settings` excludes `null` (the server always returns `{}` after clearing, never `null`). Without this, the spread `{ ...old, ...data }` would assign `null` to `Project.settings`, which violates its type.
- The `api.updateProject` method in `packages/core/api/client.ts` already serializes the entire `UpdateProjectRequest` body with `JSON.stringify(data)` — no changes needed to the API client.
- `CreateProjectRequest` was not modified — it does not include `settings` (matching the Go handler, which does not accept settings on project creation).

### Key decisions

- Used `?? undefined` (nullish coalescing with `undefined`) rather than a ternary or `delete` for the null→undefined normalization. TypeScript correctly narrows `data.settings ?? undefined` to `Record<string, unknown> | undefined`, making the spread type-safe without a cast.
- Did NOT add a typed `ProjectSettings` interface (e.g., `{ enable_agent_workdir?: boolean; agent_workdir?: string }`) — the plan specifies `Record<string, unknown>` to keep settings extensible, matching the workspace settings pattern.
- Did NOT add settings support to `CreateProjectRequest` — the create-project flow doesn't need it (Task 11 only adds the toggle to the detail sidebar).

## Task 14: Daemon tests for pre-checkout and Cwd propagation

### Findings

- The existing `TestPreCheckoutRepos` (8 subtests, Task 6) already covers the direct `preCheckoutRepos` behavior: CreateWorktree params, collision guards, error handling, disabled/local_directory cases, and ref resolution.
- The existing `TestRunTaskSetsAgentCwd` (2 subtests, Task 8) already covers end-to-end Cwd propagation using a fake shell-script agent binary: `enabled` runs in `workdir/src`, `disabled` runs in `workdir`.
- Those two tests together cover the required behaviors, but they do not exercise them in a single integrated `runTask` invocation. To verify the combined path explicitly, I added `TestRunTaskPreChecksOutReposAndSetsCwd` in `server/internal/daemon/daemon_test.go`.
- The new test uses the same fake agent binary pattern as `TestRunTaskSetsAgentCwd` and a `mockRepoCache` (already defined for `TestPreCheckoutRepos`). It verifies:
  - `repoCache.CreateWorktree` is called once for each `github_repo` in `task.Repos` with the expected `WorkspaceID`, `WorkDir`, `AgentName`, and `TaskID`.
  - The spawned agent process records a Cwd ending with `workdir/src` (the resolved `AgentWorkdir`).
- The test is skipped on Windows because the fixture is a POSIX shell script.

### Verification

- `go test ./internal/daemon/... -run 'TestRunTaskPreChecksOutReposAndSetsCwd|TestRunTaskSetsAgentCwd|TestPreCheckoutRepos' -count=1` passes.
- `go test ./internal/daemon/... -count=1` passes for the `daemon` package.
- `go vet ./internal/daemon/...` reports no issues.
- `gofmt -l internal/daemon/daemon_test.go` reports no formatting issues.
- A pre-existing `execenv` test failure was observed: `TestPrepareOpenclawConfigUsesCwdWhenWorkdirEnabled` fails because `openclaw` is not on `PATH`. This is unrelated to Task 14 and is documented in `issues.md`.
