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
