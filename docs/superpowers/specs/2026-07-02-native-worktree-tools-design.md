# Native Worktree Tools — Design Spec

Date: 2026-07-02
Status: Draft (rev 2, post adversarial review)

## Summary

Two coupled changes give serf native git-worktree support, modeled on codex's
detection approach (linked-worktree root resolution) and Claude Code's
creation lifecycle (a single agent-facing tool that creates, enters, lists,
removes, and switches worktrees):

1. **Detection plumbing (minimal):** a new `ResolveMainRepoRoot` in
   `agent/execenv/gitpath.go` that resolves the main repo root through
   linked-worktree `.git` pointer files, fixing a real existing bug (trust and
   config keying off the worktree dir instead of the main root in linked
   worktrees).
2. **`manage_worktree` tool:** a single agent-facing tool with
   `operation: create|list|remove|switch` that manages the full worktree
   lifecycle and swaps the session's `env` to enter/leave worktrees.

Worktrees live outside the project tree under
`<stateRoot>/worktrees/<projectid>/<name>/` (where `stateRoot` is
`DefaultStateRoot()` — `$SERF_STATE_DIR` else `~/.serf`), so they cannot pollute
`git status` and no `.gitignore` dance is needed.

## Goals

- Satisfy the `using-git-worktrees` skill's Step 1a: a native tool the agent
  uses instead of falling back to manual `git worktree add` + `cd`.
- Make `create`/`switch` actually *enter* the worktree so subsequent tools
  (shell, read_file, edit_file, grep) operate inside it with no per-call `cwd`.
- Resolve the main repo root correctly inside linked worktrees (the codex port).
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
| Placement | `<stateRoot>/worktrees/<projectid>/<name>/` | Outside the project tree → no gitignore; matches `~/.serf` state layout |
| projectid | `<basename>-<sha256[:16]>` of canonical abs root | Collision-resistant, fixed-length, case-irrelevant (see §6) |
| baseRef default | current HEAD, optional `base_ref` arg | Parity with `git worktree add -b <name>`; no network |
| `delete_branch` | optional, default false | Safe — leaves branch for review/merge |
| Orphan cleanup | deferred | Harness/UX concern, not an agent-tool concern |
| Detection scope | minimal | Full layering port is a separate config-subsystem spec |
| env-restore model | single saved env (not a stack) | Restore to original repo root, not a previous worktree |

## 1. Detection plumbing

### `ResolveMainRepoRoot(env, cwd) string`

**File:** `agent/execenv/gitpath.go` (new function, additive —
`GitRootOrEmpty` unchanged).

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

`ResolveMainRepoRoot` gets its own cache entry in `gitRootCache` (keyed by
`cwd`), mirroring `GitRootOrEmpty`'s memoization. The cache is per-env; a
`WithWorkingDirectory` swap creates a fresh cache (existing behavior), so
post-swap lookups re-resolve against the new `RootDir`.

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

### Definition

Single tool, `operation` enum (`create|list|remove|switch`), mirroring
`task_list`'s `action` pattern. Args by operation:

| Operation | Args |
|---|---|
| `create` | `name` (required), `base_ref` (optional, commit-ish, default current HEAD) |
| `list` | none |
| `remove` | `name` (required), `force` (optional bool, default false), `delete_branch` (optional bool, default false) |
| `switch` | `name` (required) |

### `name` validation

`name` is used as **both** the git branch name and the final path component.
Strict validation before any git call (R2 MAJOR #4):

- Must match `^[A-Za-z0-9][A-Za-z0-9._/-]*$`.
- Reject git-ref rules: no `..`, no leading `-`, no trailing `/`, no `@{`, no
  `..` anywhere.
- `/` is allowed (e.g. `feature/foo`) → the path becomes a nested
  `<projectid>/feature/foo`, requiring `MkdirAll` of the parent.
- Every interpolated path and branch token passed to a shell command must be
  `shellEscape`d (`agent/execenv/local.go:1224` has the helper; the design
  mandates its use to prevent command injection via `$(...)`/backticks in
  `name`).

## 3. `create` semantics

1. Resolve the main repo root via `ResolveMainRepoRoot(env, cwd)`.
2. Compute `projectid` (see §6).
3. Compute worktree path:
   `filepath.Join(stateRoot, "worktrees", projectid, name)`.
4. `MkdirAll` the parent of the worktree path (handles slash-containing names).
5. Run `git worktree add <path> -b <name> [<base_ref>]` in the main repo root,
   with all tokens `shellEscape`d. The branch name is `<name>` (no forced
   prefix). If the branch already exists, error and suggest `switch`.
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
3. If the session is currently in a worktree, restore to the original env
   first; then swap to the target's `WithWorkingDirectory`.
4. Recompute `s.envInfo` + invalidate the cached system prompt (§7).
5. Return the worktree path and branch.

## 5. `list` and `remove` semantics

### `list`

1. Run `git worktree prune` in the main repo root first (clears stale metadata
   for already-deleted worktree dirs — R1 MAJOR #7).
2. Run `git worktree list --porcelain` in the main repo root.
3. Filter to serf-managed worktrees: keep only entries whose worktree path is
   under `<stateRoot>/worktrees/<projectid>/`. Use
   `filepath.Rel(projectidDir, entryPath)` and reject `..`-prefixed results,
   or `strings.HasPrefix(filepath.Clean(entryPath), projectidDir +
   string(filepath.Separator))` — **not** bare `HasPrefix`, which collides
   when one projectid prefixes another (R2 MAJOR #5).
4. Return structured output: for each worktree, its path, branch/HEAD, and
   whether the session is currently in it.

### `remove`

1. Resolve the worktree path from `name`.
2. **Subagent guard (R2 MAJOR #6):** if the session is currently in the
   worktree being removed AND in-flight subagents/delegates exist, refuse with
   a clear error (their env points at the worktree dir that `git worktree
   remove` will delete). Otherwise proceed.
3. If the session is currently in this worktree, restore `s.env` to the
   pre-worktree env via `worktreeGuard`, then recompute `s.envInfo` + invalidate
   the cached system prompt (§7).
4. Run `git worktree remove <path>` in the main repo root. If the worktree is
   dirty (uncommitted changes or untracked files), `git worktree remove`
   refuses unless `force: true`; the tool surfaces that error and requires the
   agent to pass `force`.
5. If `delete_branch: true`, run `git branch -D <name>` in the main repo root.
   Default false.
6. Run `git worktree prune`.
7. Return confirmation with the removed path and whether the branch was
   deleted.

## 6. projectid derivation

`projectid = <basename>-<sha256[:16]>` where:

- `<basename>` is `filepath.Base` of the canonical (symlink-resolved) absolute
  repo root.
- `<sha256[:16]>` is the first 16 hex chars of `sha256(canonicalAbsRoot)`.

Example: `/home/jesse/git/prime-radiant/serf` → `serf-a1b2c3d4e5f6a7b8`.

This replaces the originally-proposed sanitized-path scheme (option C), which
adversarial review (R2 MAJOR #3) showed collides (`/a/b` and `/a/b_` both
sanitize to `_a_b`), overflows the 255-byte filesystem name limit on deeply
nested repos, and collides on case-insensitive filesystems. The hash form is
fixed-length, collision-resistant, and case-irrelevant.

`stateRoot` is `DefaultStateRoot()` (`$SERF_STATE_DIR` else `~/.serf`),
resolved via `cmdutil.DefaultStateRoot()`. It is **not** `s.stateDir` (which is
per-session and may be empty when persistence is off). The tool carries its own
`worktreesRoot` populated at init from `DefaultStateRoot()`, independent of
per-session `stateDir` (R1 MAJOR #6).

## 7. Session-env swap mechanism

### The `worktreeGuard` closure

Exposed in `toolDeps` (mirroring `taskGuard`/`goalGuard`):

- `currentWorktree() (path string, inWorktree bool)` — whether the session is
  currently in a worktree and which one.
- `enterWorktree(path string)` — swap `s.env` to `WithWorkingDirectory(path)`,
  saving the prior env.
- `exitWorktree()` — restore the prior `s.env`.

All three operate under `s.mu`.

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

The `worktreeGuard` writes through `currentEnv()`; reads go through it. The
turn loop owns `s.env`, and tools run on the turn loop, so no new concurrency
hazard once the accessor is in place.

### envInfo + system-prompt refresh (BLOCKER fix — R1 #1, R2 #2)

`s.envInfo` is snapshotted once at init (`session_init.go:512-522`) and used
frozen in the system prompt (`session_prompts.go:45-57`) and persisted meta
(`session_state.go:50`). After an env swap, the model would believe it's still
in the old directory, and `GitRootOrEmpty(s.env, s.envInfo.WorkingDir)` returns
`""` because the stale cwd is outside the new `RootDir` (empirically verified
by R2).

Fix: on every successful `create`/`switch`/`remove`-that-restores, **under
`s.mu`**:

1. `s.envInfo = envInfoFromEnv(s.env, s.sclock())`
2. `s.rebuildToolDefsCache()`
3. `s.refreshSystemPromptCache()`

This mirrors `SetModel`'s invalidation pattern at `session.go:560-561` exactly.

### env-restore model

Single saved env, not a stack. `enterWorktree` saves the current env; `exitWorktree`
restores it. If the session creates worktree A, switches to B, then removes B —
`exitWorktree` restores to the *original* repo root, not A. Rationale: A's
state may have changed while we were in B; landing at the stable original root
is safer than a stale A. The agent can `switch` back to A explicitly if needed.

### `WithWorkingDirectory` correctness

`WithWorkingDirectory` (`local.go:119`) shares `runningPIDs` and `fs` with the
parent env — so PID tracking and afero backing survive a swap. The swap must
always use `WithWorkingDirectory`, never `NewLocalExecutionEnvironment` (which
would lose PID sharing). The spec mandates a comment + test asserting this.

### Confinement is sound (not a blocker)

Adversarial review (R2) empirically verified that `ensureUnderRoot`
(`local.go:1102-1112`) confines writes to the worktree's `RootDir` (the
specific worktree leaf path, e.g.
`~/.serf/worktrees/<projectid>/<name>/`), **not** to `~/.serf` broadly. A
write to `~/.serf/sessions/...` is rejected because it's not under the
worktree's `RootDir`. The originally-raised "sandbox escape" (R1 BLOCKER #2)
is a **false positive** — discarded.

## 8. Error handling

- Not in a git repo → `create` errors with a clear message.
- `name` fails validation → error before any git call.
- `name` already exists as a branch or worktree → `create` errors (suggest
  `switch`).
- `switch`/`remove` to a nonexistent worktree → error.
- `remove` on a dirty worktree without `force` → error surfacing `git worktree
  remove`'s output.
- `remove`-current while in-flight subagents exist → error (subagent guard).
- `git` unavailable → `ResolveMainRepoRoot` uses the structural `.git`-pointer
  path (no git binary needed for the common linked-worktree case);
  `create`/`remove`/`list` require git and error clearly if absent.

## 9. Testing

Per `AGENTS.md`: no network, no provider credentials, deterministic. The
worktree tool is pure plumbing — git operations on temp repos. Tests use real
`git` (a build tool, always available in CI) on temp directories:

- `create` → verify linked worktree exists, `.git` is a pointer file, `s.env`
  swapped, `envInfo` recomputed, system prompt invalidated.
- `switch` between two worktrees → verify env points at the right one, envInfo
  refreshed.
- `remove` clean and dirty (with/without `force`, with/without
  `delete_branch`).
- `remove`-current while in-flight subagents exist → verify refusal.
- `list` returns expected entries; prunable entries cleared; non-serf worktrees
  excluded; prefix-collision filtering correct.
- `ResolveMainRepoRoot` on a linked worktree returns the main root, not the
  worktree dir; submodule falls through to `--git-common-dir`.
- projectid: two distinct repos with the same basename produce distinct ids;
  fixed-length regardless of path depth.
- `name` validation: rejects `..`, leading `-`, trailing `/`, `@{`, spaces;
  allows `feature/foo`.
- Data-race: `-race` run of create/switch/remove with concurrent tool
  dispatch passes.
- Fuzz target for arg validation (extends `FuzzToolArgsValidate` table).

## 10. Known limitations

- **Bare repos:** `ResolveMainRepoRoot`'s structural walk-up from
  `bare.git/worktrees/<n>` yields `bare.git`'s parent, not `bare.git` itself;
  `--git-common-dir` returns `bare.git` (correct). Documented best-effort; a
  future spec can add bare-repo detection (R2 MINOR #7).
- **Submodules:** neither the structural path nor `--show-toplevel` resolves
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
