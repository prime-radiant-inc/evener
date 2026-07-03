# Native Worktree Tools — Design Spec

Date: 2026-07-02
Status: Draft (rev 3, post implementation-feasibility review)

## Summary

Three coupled changes give serf native git-worktree support, modeled on codex's
detection approach (linked-worktree root resolution) and Claude Code's
creation lifecycle (a single agent-facing tool that creates, enters, lists,
removes, and switches worktrees):

1. **Detection plumbing (minimal):** a new `ResolveMainRepoRoot` in
   `agent/execenv/gitpath.go`, plus a small local-filesystem equivalent for hub
   launch config, that resolves the main repo root through linked-worktree
   `.git` pointer files. State-keying paths must use it where they need stable
   repository identity rather than the current linked-worktree directory.
2. **`manage_worktree` tool:** a single agent-facing tool with
   `operation: create|list|remove|switch` that manages the full worktree
   lifecycle and swaps the session's `env` to enter/leave worktrees.
3. **Session-env mutation discipline:** an explicit env accessor/swap helper so
   changing the session working root does not introduce data races or stale
   prompt/env metadata.

Worktrees live outside the project tree under
`<worktreeRoot>/<projectid>/<name>/`, where `worktreeRoot` is derived from the
session's runtime state directory (see §6). They cannot pollute `git status`,
and no `.gitignore` dance is needed.

## Goals

- Satisfy the `using-git-worktrees` skill's Step 1a: a native tool the agent
  uses instead of falling back to manual `git worktree add` + `cd`.
- Make `create`/`switch` actually *enter* the worktree so subsequent tools
  (shell, read_file, edit_file, grep) operate inside it with no per-call `cwd`.
- Resolve the main repo root correctly inside linked worktrees (the codex port).
- Use the resolved main root where this feature needs stable repository
  identity: managed worktree placement and hub launch trust/project state.
- Keep the full lifecycle (create, list, remove, switch) in one tool.

## Non-goals (this spec)

- **Full config/hook/trust layering** for linked worktrees (codex's
  "linked_worktree_project_layers_keep_worktree_config_but_use_root_repo_hooks").
  That is a separate config-subsystem change with its own spec.
- **Orphan auto-cleanup** (Claude Code's `cleanupPeriodDays` sweep + `git
  worktree lock` during runs). That is daemon/harness work; the tool exposes
  `list` for a future sweeper to use.
- **`fresh`/`head` baseRef enum** with origin/HEAD fetch-and-fallback. The tool
  takes a `base_ref` commit-ish; fetch/network behavior is out of scope.
- Bare-repo and submodule edge cases beyond documented best-effort (see
  "Known limitations").

## Design decisions (with rationale)

| Decision | Choice | Rationale |
|---|---|---|
| Detection + creation | Both (C) | Detection makes creation correct; creation satisfies the skill |
| Operation set | Full lifecycle (B) | create/list/remove/switch covers the agent workflow |
| Tool shape | Single tool, `operation` param (A) | Operations share state; matches `task_list`/`job_watch` pattern |
| Tool name | `manage_worktree` | `manage` honestly covers the verbs; fits `verb_noun` convention |
| "Enter" mechanism | Session-env swap (A) | Subsequent tools auto-operate in the worktree; matches the skill's intent |
| Git control commands | Dedicated main-repo control env | Git lifecycle operations may need the main root even after the user-facing env has entered a worktree |
| Placement | `<worktreeRoot>/<projectid>/<name>/` | Outside the project tree → no gitignore; honors runtime state-dir selection |
| projectid | `<basename>-<sha256[:16]>` of canonical abs root | Collision-resistant, fixed-length, case-irrelevant (see §6) |
| baseRef default | current HEAD, optional validated `base_ref` arg | Parity with `git worktree add -b <name>`; no network |
| `delete_branch` | optional, default false | Safe — leaves branch for review/merge |
| Orphan cleanup | deferred | Harness/UX concern, not an agent-tool concern |
| Detection scope | root identity only | Full config/hook/trust layering remains separate |
| env-restore model | single saved env (not a stack) | Restore to original repo root, not a previous worktree |

## 1. Detection plumbing

### `ResolveMainRepoRoot(env, cwd) string`

**File:** `agent/execenv/gitpath.go` (new function, additive —
`GitRootOrEmpty` unchanged for callers that intentionally want the active
working-tree root).

Algorithm:

1. Find the nearest ancestor directory containing a `.git` entry (file or
   directory), walking up from `cwd`.
2. **If `.git` is a directory** → it's the main repo root; return it.
3. **If `.git` is a file** → read it, parse `gitdir: <path>`. Resolve the path
   (relative paths resolved against the found ancestor dir;
   `filepath.EvalSymlinks` on the result to handle macOS `/var → /private/var`
   and `~/.serf` symlink targets). The resolved path should end in
   `.git/worktrees/<id>`. Walk up two levels (→ `worktrees/` → common `.git`
   dir), then one more to the main repo root. Return that root.
4. **If the pointer doesn't match the `worktrees/<id>` shape** (e.g. a
   submodule, where `gitdir:` points at `.git/modules/<sub>` whose parent is
   `modules`, not `worktrees`) → fall through to the git-binary path.
5. **Git-binary fallback (when the structural parse fails):** run
   `git rev-parse --git-common-dir` in `cwd`. This returns the main `.git` dir
   for both main repos (`→ .git`) and linked worktrees (`→ /main/.git`).
   Resolve relative via `filepath.Join(cwd, common)`, `EvalSymlinks`, take the
   parent. Return it. For submodules this returns the submodule's own `.git`
   root (not the superproject) — see "Known limitations".
   - **Do NOT use `git rev-parse --show-toplevel`** — it returns the worktree
     dir, not the main root, when run inside a linked worktree.
6. **If `git` is unavailable AND the structural parse fails** → return `""`
   (cannot resolve). The structural path (step 3) handles the common
   linked-worktree case without git, so this is a rare fallback.

The structural path (steps 2-4) is primary — it handles both main repos and
standard linked worktrees without invoking git, matching codex's approach. The
git-binary fallback (step 5) catches edge cases the structural parse misses
(e.g. moved worktrees whose `gitdir:` pointer no longer lives under
`.git/worktrees/`).

`ResolveMainRepoRoot` must not share `GitRootOrEmpty`'s cache slot because the
same `cwd` can legitimately map to two different values in a linked worktree
(worktree root vs main repo root). Either add a second cache field on
`LocalExecutionEnvironment` or key the existing cache by lookup kind plus `cwd`.
The cache remains per-env; a `WithWorkingDirectory` swap creates a fresh cache
(existing behavior), so post-swap lookups re-resolve against the new `RootDir`.

### Active content root vs stable identity root

Linked worktrees need two distinct roots:

- **Active content root:** the checked-out worktree root. Keep using
  `GitRootOrEmpty` for branch-sensitive files such as project docs, project
  skills, project MCP config, and project prompt sections. Those files should
  reflect the active branch's checkout.
- **Stable identity root:** the common main repo root from
  `ResolveMainRepoRoot`. Use this for managed worktree placement and persistent
  project identity.

Adding `ResolveMainRepoRoot` alone does not fix linked-worktree state keying.
Hub launch config/trust uses local filesystem paths rather than
`execenv.ExecutionEnvironment`, so it needs a small companion helper, e.g.
`internal/gitpath.ResolveMainRepoRootLocal(cwd)`. The launch resolver should
accept both roots: read repo/project config content from the active content root,
but key trust metadata and legacy project state under the stable identity root.
This is still only root identity; it does not implement the full
config/hook/trust layering port.

### Why `--git-common-dir`, not `--show-toplevel`

`git rev-parse --show-toplevel` returns the *worktree* root inside a linked
worktree, not the main repo root — so it computes the wrong `projectid`. `git
rev-parse --git-common-dir` returns the shared `.git` directory in both cases.
This was flagged by adversarial review (R2 MAJOR #1): the original design's
"`--show-toplevel` fallback if git unavailable" was self-contradictory (you
can't run `git rev-parse` when git is unavailable) and returned the wrong root
for worktrees.

## 2. The `manage_worktree` tool

### Registration

**File:** `agent/session_tools_worktree.go` (new), registered via a
`registerWorktreeTool(reg, deps)` alongside the other `registerXTools`
functions. The handler reaches session state through a new `worktreeGuard`
closure in `toolDeps` (same pattern as `taskGuard`/`goalGuard`).

The tool definition lives in `agent/internal/tool/definitions.go` as
`DefManageWorktree()`. It is registered directly on the registry, not added to
every provider profile. `rebuildToolDefsCache` already advertises registry-only
tools after profile and MCP tools, so this keeps the feature provider-neutral.
Register it with `ReadOnly: false`; even `list` is part of a stateful lifecycle
tool and should serialize with env-changing operations.

### Definition

Single tool, `operation` enum (`create|list|remove|switch`), mirroring
`task_list`'s `action` pattern. Args by operation:

| Operation | Args |
|---|---|
| `create` | `name` (required), `base_ref` (optional, commit-ish, default current HEAD) |
| `list` | none |
| `remove` | `name` (required), `force` (optional bool, default false), `delete_branch` (optional bool, default false) |
| `switch` | `name` (required) |

Because the tool is non-read-only, `execToolBatch` serializes it between any
read-only batches. "Subsequent tools operate inside the worktree" means
subsequent calls in the ordered execution stream and every later tool round. A
read-only call that appears before `manage_worktree` in the same model response
still sees the old env, which is correct.

### `name` validation

`name` is used as **both** the git branch name and the final path component.
Strict validation before any git call (R2 MAJOR #4):

- Must match `^[A-Za-z0-9][A-Za-z0-9._/-]*$`.
- Reject git-ref rules: no `..`, no leading `-`, no trailing `/`, no `@{`, no
  `..` anywhere.
- `/` is allowed (e.g. `feature/foo`) → the path becomes a nested
  `<projectid>/feature/foo`, requiring `MkdirAll` of the parent.
- Every interpolated path and branch token passed to a shell command must be
  escaped with a shared helper. `agent/execenv/local.go` currently has an
  unexported `shellEscape`; either export an execenv helper for command assembly
  or implement a small local equivalent in `session_tools_worktree.go`. Do not
  hand-build shell command strings.
- Validate the git ref with git itself before `create`: run
  `git check-ref-format --branch <name>` and keep the explicit rejects above so
  branch-stack syntax such as `@{-1}` cannot be interpreted. The regex is only
  an early user-facing filter; git is the source of truth for ref validity.

### `base_ref` validation

`base_ref` is optional. When present, trim it, reject whitespace/control
characters and leading `-`, then resolve it before creation with:

`git rev-parse --verify --quiet <base_ref>^{commit}`

Use the resolved commit SHA in `git worktree add`. This avoids option-like
commit-ish values, catches typos before creating directories, and keeps
fetch/network behavior out of scope.

### Git control environment

Lifecycle git commands run through a control env rooted at the resolved main
repo root. This is necessary because, after `create`/`switch`, the user-facing
`s.env` is rooted at `<worktree path>`, and `LocalExecutionEnvironment`
rejects a `workingDir` outside its own root. The implementation must not call
`currentEnv.ExecCommand(..., workingDir=mainRepoRoot, ...)` after entering a
worktree.

For local envs, build the control env with
`currentLocalEnv.WithWorkingDirectory(mainRepoRoot)` and use it only inside
`manage_worktree`. Normal shell/file/grep tools continue to receive the
user-facing env for the active worktree. If the execution environment is not a
`LocalExecutionEnvironment`, the tool should error clearly because env swapping
and local git worktree management are not supported.

## 3. `create` semantics

1. Resolve the main repo root via `ResolveMainRepoRoot(env, cwd)`.
2. Compute `projectid` (see §6).
3. Compute worktree path:
   `filepath.Join(worktreeRoot, projectid, name)`.
4. `MkdirAll` the parent of the worktree path (handles slash-containing names).
5. Run `git worktree add -b <name> -- <path> [<resolved-base-sha>]` through the
   git control env, with all tokens escaped. The branch name is `<name>` (no
   forced prefix). If the branch already exists, error and suggest `switch`.
6. On success, swap `s.env` to `WithWorkingDirectory(<worktree path>)` via the
   `worktreeGuard`, **then** recompute `s.envInfo` and invalidate the cached
   system prompt (see §7). Store the pre-worktree env for later restoration.
7. Return the worktree absolute path, the branch name, and the main repo root.

No gitignore dance — worktrees live outside the repo. No network/fetch —
`base_ref` is a local commit-ish or remote-tracking ref that must already
exist.

## 4. `switch` semantics

1. Resolve the worktree path from `name` (same `projectid` derivation, using
   the session's current main repo root).
2. Verify the path exists and is a valid worktree (has the `.git` pointer
   file).
3. If the target is not under the current project's managed worktree directory,
   reject it. `switch` never enters arbitrary external worktrees.
4. If the session is currently in a worktree, restore to the original env
   first; then swap to the target's `WithWorkingDirectory`.
5. Recompute `s.envInfo` + invalidate the cached system prompt (§7).
6. Return the worktree path and branch.

## 5. `list` and `remove` semantics

### `list`

1. Run `git worktree prune` through the git control env first (clears stale
   metadata for already-deleted worktree dirs — R1 MAJOR #7).
2. Run `git worktree list --porcelain` through the git control env.
3. Filter to serf-managed worktrees: keep only entries whose worktree path is
   under `<worktreeRoot>/<projectid>/`. Use
   `filepath.Rel(projectidDir, entryPath)` and reject `..`-prefixed results,
   or `strings.HasPrefix(filepath.Clean(entryPath), projectidDir +
   string(filepath.Separator))` — **not** bare `HasPrefix`, which collides
   when one projectid prefixes another (R2 MAJOR #5).
4. Return structured output: for each worktree, its path, branch/HEAD, and
   whether the session is currently in it.

### `remove`

1. Resolve the worktree path from `name`.
2. Verify the target path is under `<worktreeRoot>/<projectid>/`; never remove
   an arbitrary path by name.
3. **Live child/job guard (R2 MAJOR #6, widened):** refuse if any live subagent,
   delegate, or background shell job has a working directory equal to the target
   path or under it. This guard is based on live env/job working dirs, not only
   whether the parent session is currently in the target. A child may have been
   started with `working_dir` under a worktree while the parent has already
   switched elsewhere.
4. If `force` is false, preflight dirtiness before leaving the current worktree:
   run `git -C <path> status --porcelain=v1 --untracked-files=all`. If output is
   non-empty, error and leave `s.env` unchanged. This preserves the user's
   current context when removal cannot proceed.
5. If the session is currently in this worktree, restore `s.env` to the
   pre-worktree env via `worktreeGuard`, then recompute `s.envInfo` + invalidate
   the cached system prompt (§7). If there is no safe restore env (for example,
   the session started directly inside the managed worktree being removed),
   refuse with a clear error instead of deleting the active root out from under
   the session.
6. Run `git worktree remove [--force] -- <path>` through the git control env.
   `--force` is included only when `force: true`.
7. If `delete_branch: true`, run `git branch -D -- <name>` through the git
   control env.
   Default false.
8. Run `git worktree prune`.
9. Return confirmation with the removed path and whether the branch was
   deleted.

## 6. projectid derivation

`projectid = <safe-basename>-<sha256[:16]>` where:

- `<safe-basename>` is `filepath.Base` of the canonical (symlink-resolved)
  absolute repo root, sanitized to `[A-Za-z0-9._-]`, trimmed of leading `.`/`-`,
  truncated to 48 bytes, and replaced with `repo` if empty.
- `<sha256[:16]>` is the first 16 hex chars of `sha256(canonicalAbsRoot)`.

Example: `/home/jesse/git/prime-radiant/serf` → `serf-a1b2c3d4e5f6a7b8`.

This replaces the originally-proposed sanitized-path scheme (option C), which
adversarial review (R2 MAJOR #3) showed collides (`/a/b` and `/a/b_` both
sanitize to `_a_b`), overflows the 255-byte filesystem name limit on deeply
nested repos, and collides on case-insensitive filesystems. The hash form is
fixed-length, collision-resistant, and case-irrelevant.

### worktreeRoot derivation

`worktreeRoot` must honor the runtime state directory that launched this
session:

1. If `s.stateDir` is non-empty, use `filepath.Join(s.stateDir, "worktrees")`.
   This respects `--state-dir`, `SERF_STATE_DIR`, and the normal
   `agent.RuntimeDir(...)` project state directory selected by `serf run` and
   `serf serve`.
2. If `s.stateDir` is empty (tests or persistence-off sessions), derive a
   fallback with agent-owned state logic, not `cmdutil.DefaultStateRoot()`.
   A small helper can use `agent.RuntimeDir(gitOriginURL(env, mainRoot),
   mainRoot, "")` and then append `worktrees`.

The agent package should not import `cmdutil`. `cmdutil.DefaultStateRoot()`
currently points at provider/auth state (`$SERF_STATE_DIR` else `~/.serf`) and
would ignore the resolved runtime state directory in common launches.

## 7. Session-env swap mechanism

### The `worktreeGuard` closure

Exposed in `toolDeps` (mirroring `taskGuard`/`goalGuard`):

- `state() worktreeState` — snapshot of the current env, saved restore env,
  resolved main repo root, worktree root, and current managed worktree path if
  any.
- `controlEnv(mainRepoRoot string) (execenv.ExecutionEnvironment, error)` —
  returns a local env rooted at the main repo root for lifecycle git commands.
- `enterWorktree(path string)` — swap `s.env` to `WithWorkingDirectory(path)`,
  saving the prior env if no restore env is already saved.
- `exitWorktree()` — restore the saved env, or report that no safe restore env
  exists.
- `liveWorkUnder(path string) []string` — returns live child/delegate/shell
  work handles whose working directory is equal to `path` or below it. `remove`
  uses this for the guard in §5.

All methods operate under `s.mu` or return snapshots built under `s.mu`.

### `currentEnv()` accessor (BLOCKER fix — R1 #3/#5/#8)

Introducing the first-ever mutation of `s.env` makes every existing unlocked
`s.env` read a data race. Fix: add a `currentEnv()` accessor under `s.mu`
(mirrors `currentProfile()` at `session.go:455-459`). Audit and convert the
unlocked `s.env` reads at:

- `agent/session_tools.go:388` (`execTool`)
- `agent/subagents.go:498`
- `agent/job_delegate.go:853`
- `agent/session_lifecycle.go:127` (`s.env.Cleanup()`)
- `agent/session_init.go:512/514/520/548/599/1120`
- `agent/session_events.go:166`
- `agent/session_tools.go:758/769/780`
- `agent/session_prompts.go:141`

Reads go through `currentEnv()`. Writes go through one locked helper, e.g.
`swapEnvAndRefreshLocked(next execenv.ExecutionEnvironment)`, so the env,
`envInfo`, and prompt/tool caches move together. Update the `Session.mu` lock
discipline comment in `session.go` to include `env` and `envInfo`.

### envInfo + system-prompt refresh (BLOCKER fix — R1 #1, R2 #2)

`s.envInfo` is snapshotted once at init (`session_init.go:512-522`) and used
frozen in the system prompt (`session_prompts.go:45-57`) and persisted meta
(`session_state.go:50`). After an env swap, the model would believe it's still
in the old directory, and `GitRootOrEmpty(s.env, s.envInfo.WorkingDir)` returns
`""` because the stale cwd is outside the new `RootDir` (empirically verified
by R2).

Fix: on every successful `create`/`switch`/`remove`-that-restores, under
`s.mu` and through a single helper:

1. `s.envInfo = envInfoFromEnv(s.env, s.sclock())`
2. Re-run the git snapshot fields normally populated during init:
   `IsGitRepo`, `GitBranch`, modified/untracked file counts, recent commits,
   and origin URL.
3. `s.rebuildToolDefsCache()`
4. `s.refreshSystemPromptCache()`

This mirrors `SetModel`'s invalidation pattern at `session.go:560-561` exactly.

### env-restore model

Single saved env, not a stack. The first successful `enterWorktree` saves the
current env; later `switch` calls do not replace that saved env. If the session
creates worktree A, switches to B, then removes B, `exitWorktree` restores to
the original repo root, not A. Rationale: A's state may have changed while we
were in B; landing at the stable original root is safer than a stale A. The
agent can `switch` back to A explicitly if needed.

If a session starts directly inside a managed worktree, there may be no safe
non-target restore env. `remove` of that active worktree must refuse unless a
safe restore env can be constructed and verified.

### `WithWorkingDirectory` correctness

`WithWorkingDirectory` (`local.go:119`) shares `runningPIDs` and `fs` with the
parent env — so PID tracking and afero backing survive a swap. The swap must
always use `WithWorkingDirectory`, never `NewLocalExecutionEnvironment` (which
would lose PID sharing). The spec mandates a comment + test asserting this.

### Confinement is sound (not a blocker)

Adversarial review (R2) empirically verified that `ensureUnderRoot`
(`local.go:1102-1112`) confines writes to the worktree's `RootDir` (the
specific worktree leaf path, e.g.
`<worktreeRoot>/<projectid>/<name>/`), **not** to the broader state directory.
A write to unrelated session state is rejected because it's not under the
worktree's `RootDir`. The originally-raised "sandbox escape" (R1 BLOCKER #2) is
a **false positive** — discarded.

## 8. Error handling

- Not in a git repo → `create` errors with a clear message.
- `name` fails validation → error before any git call.
- `base_ref` fails validation or cannot resolve to a commit → error before
  `git worktree add`.
- `name` already exists as a branch or worktree → `create` errors (suggest
  `switch`).
- `switch`/`remove` to a nonexistent worktree → error.
- `switch`/`remove` target resolves outside the managed worktree directory →
  error.
- `remove` on a dirty worktree without `force` → error surfacing `git worktree
  status` evidence, without changing the session env.
- `remove` when live children/delegates/shell jobs are rooted under the target
  worktree → error (live work guard).
- `remove` of the active worktree with no safe restore env → error.
- Non-local execution environment → `manage_worktree` errors clearly.
- `git` unavailable → `ResolveMainRepoRoot` uses the structural `.git`-pointer
  path (no git binary needed for the common linked-worktree case);
  `create`/`remove`/`list` require git and error clearly if absent.

## 9. Testing

Per `AGENTS.md`: no network, no provider credentials, deterministic. The
worktree tool is pure plumbing — git operations on temp repos. Tests use real
`git` (a build tool, always available in CI) on temp directories:

- `create` → verify linked worktree exists, `.git` is a pointer file, `s.env`
  swapped, git snapshot fields recomputed, system prompt invalidated.
- `switch` between two worktrees → verify env points at the right one, envInfo
  refreshed.
- Same-response ordering: read-only call before `manage_worktree` sees old env;
  read-only call after it sees the new env.
- `remove` clean and dirty (with/without `force`, with/without
  `delete_branch`).
- Dirty remove without force leaves `s.env` unchanged.
- `remove`-current with no safe restore env → verify refusal.
- `remove` while live subagents/delegates/shell jobs have working dirs under the
  target → verify refusal, including the case where the parent has already
  switched elsewhere.
- `list` returns expected entries; prunable entries cleared; non-serf worktrees
  excluded; prefix-collision filtering correct.
- `ResolveMainRepoRoot` on a linked worktree returns the main root, not the
  worktree dir; submodule falls through to `--git-common-dir`.
- Root semantics: project docs, project skills, project MCP config, and project
  prompt sections continue to read from the active worktree root, while managed
  worktree placement and hub launch trust/meta state key off the stable main
  repo root from inside a linked worktree.
- Git control env: after entering a worktree, `list`, `switch`, `remove`, and a
  second `create` still work even though the main repo root is outside the
  user-facing env root.
- Tool visibility: `manage_worktree` appears in `ToolDefinitions()` as a
  registry-only tool and is non-read-only.
- State root: worktree placement honors explicit `StateDir`/`SERF_STATE_DIR`
  test overrides and does not use provider/auth state by accident.
- projectid: two distinct repos with the same basename produce distinct ids;
  fixed-length regardless of path depth; unsafe basename characters are
  sanitized.
- `name` validation: rejects `..`, leading `-`, trailing `/`, `@{`, spaces;
  rejects git-invalid refnames caught by `git check-ref-format`; allows
  `feature/foo`.
- `base_ref` validation rejects whitespace/control chars, leading `-`, and
  nonexistent refs; accepts a local branch, tag, remote-tracking ref, and SHA.
- Data-race: `-race` run of create/switch/remove with concurrent read-only tool
  dispatch, child creation, delegate restore env creation, status events, and
  Close cleanup passes.
- Fuzz target for arg validation (extends `FuzzToolArgsValidate` table).

## 10. Known limitations

- **Bare repos:** `ResolveMainRepoRoot`'s structural walk-up from
  `bare.git/worktrees/<n>` yields `bare.git`'s parent, not `bare.git` itself;
  `--git-common-dir` returns `bare.git` (correct). Documented best-effort; a
  future spec can add bare-repo detection (R2 MINOR #7).
- **Submodules:** neither the structural path nor `--git-common-dir` resolves
  the superproject root; `--git-common-dir` returns the submodule's `.git`.
  The projectid keys off the submodule root, not the superproject. This is
  probably acceptable (submodule-as-own-project) but is documented (R2 MINOR
  #8).
- **Orphan cleanup:** deferred. The tool exposes `list`; a future harness
  sweeper can use it.

## Adversarial review record

Two subagents reviewed this design adversarially. Reviewer #2 won the scoring
(5 pts): 8/8 legitimate findings vs reviewer #1's 9/10 (1 false-positive
sandbox-escape blocker that R2 empirically disproved). All 8 legitimate
findings are incorporated above as revision notes. The false positive
(sandbox-escape via `RootDir` under `~/.serf`) is discarded with evidence.

Rev 3 adds the follow-up implementation-feasibility fixes: root-sensitive
call-site migration, runtime-state-based worktree placement, a git control env,
tool visibility, stronger ref/base validation, safer remove semantics, and a
wider live-work guard.
