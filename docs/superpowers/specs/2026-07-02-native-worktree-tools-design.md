# Native Worktree Tools — Design Spec

Date: 2026-07-02
Status: Draft (rev 4, post cross-harness comparison + live-repo evidence)

## Summary

Serf gets native git-worktree support, modeled on codex's detection approach
(linked-worktree root resolution) and Claude Code's creation lifecycle, with
one correction the live evidence demands: **disposal is designed in from day
one**. An audit of this repo found ~90 stale harness-created worktrees and
orphaned branches under `.claude/worktrees/` — the residue of a design that
made creation easy and cleanup an afterthought. This spec ships the disposal
primitives (per-worktree metadata, an `unchanged` predicate, `exit`, `prune`,
safe branch deletion) in v1.

Four coupled changes:

1. **Detection plumbing (minimal):** a new `ResolveMainRepoRoot` in
   `agent/execenv/gitpath.go`, plus a small local-filesystem equivalent for hub
   launch config, that resolves the main repo root through linked-worktree
   `.git` pointer files. State-keying paths must use it where they need stable
   repository identity rather than the current linked-worktree directory —
   including the `RuntimeDir` calls in `cmd/serf/run.go` and `cmd/serf/serve.go`.
2. **`manage_worktree` tool:** a single agent-facing tool with
   `operation: create|list|switch|exit|remove|prune` that manages the full
   worktree lifecycle and swaps the session's `env` to enter/leave worktrees.
3. **Session-env mutation discipline:** an explicit env accessor/swap helper so
   changing the session working root does not introduce data races or stale
   prompt/env metadata — including across **session resume**.
4. **Phase 2 — delegate worktree isolation:** `isolation: "worktree"` on the
   `delegate` tool. The harness creates a managed worktree per delegate, roots
   the child env there (so lane-straying is structurally impossible), and
   auto-removes it when the job finishes without changes.

Worktrees live outside the project tree under
`<worktreeRoot>/<projectid>/<name>/`, where `worktreeRoot` is derived from the
session's runtime state directory (see §6). They cannot pollute `git status`,
no `.gitignore` dance is needed, and they are immune to `git clean -dxf` run
in the main checkout.

## Goals

- Satisfy the `using-git-worktrees` skill's Step 1a: a native tool the agent
  uses instead of falling back to manual `git worktree add` + `cd`.
- Make `create`/`switch` actually *enter* the worktree so subsequent tools
  (shell, read_file, edit_file, grep) operate inside it with no per-call `cwd`.
- Make it possible to *leave* a worktree without destroying it (`exit`), and to
  return to it later (`switch`). File tools are hard-confined to the env root,
  so without `exit` the agent cannot even read the main checkout from inside a
  worktree.
- Resolve the main repo root correctly inside linked worktrees (the codex port).
- Use the resolved main root where this feature needs stable repository
  identity: managed worktree placement, `serf run`/`serve` runtime state
  keying, and hub launch trust/project state.
- Keep the full lifecycle (create, list, switch, exit, remove, prune) in one
  tool, with disposal safe by default: no destruction of uncommitted files or
  unmerged commits without an explicit force.
- Give parallel delegate lanes native worktree isolation (Phase 2) — the
  dominant real-world use, and the one manual management has repeatedly botched
  (lanes writing to the wrong checkout).

## Non-goals (this spec)

- **Full config/hook/trust layering** for linked worktrees (codex's
  "linked_worktree_project_layers_keep_worktree_config_but_use_root_repo_hooks").
  That is a separate config-subsystem change with its own spec.
- **Scheduled/automatic orphan sweeping** (Claude Code's `cleanupPeriodDays`
  daemon sweep). The *primitives* — metadata, the `unchanged` predicate, and
  the `prune` operation — ship now; a background trigger is future daemon work
  that calls the same `prune` logic.
- **`fresh`/`head` baseRef enum** with origin/HEAD fetch-and-fallback. The tool
  takes a `base_ref` commit-ish; fetch/network behavior is out of scope.
- Bare-repo and submodule edge cases beyond documented best-effort (see
  "Known limitations").

## Design decisions (with rationale)

| Decision | Choice | Rationale |
|---|---|---|
| Detection + creation | Both (C) | Detection makes creation correct; creation satisfies the skill |
| Operation set | create/list/switch/exit/remove/prune | Full lifecycle incl. non-destructive leave and one-call cleanup |
| Tool shape | Single tool, `operation` param (A) | Operations share state; matches `task_list`/`job_watch` pattern |
| Tool name | `manage_worktree` | `manage` honestly covers the verbs; fits `verb_noun` convention |
| "Enter" mechanism | Session-env swap (A) | Subsequent tools auto-operate in the worktree; matches the skill's intent |
| "Leave" mechanism | `exit` restores saved env, keeps worktree | Claude Code's ExitWorktree(keep) proved this is a required verb; file-tool confinement makes it mandatory here |
| Git control commands | Dedicated main-repo control env | Git lifecycle operations may need the main root even after the user-facing env has entered a worktree |
| Placement | `<worktreeRoot>/<projectid>/<name>/` | Outside the project tree → no gitignore, immune to `git clean -dxf`; honors runtime state-dir selection |
| projectid | `<basename>-<sha256[:16]>` of canonical abs root | Collision-resistant, fixed-length (see §6) |
| baseRef default | active HEAD, optional validated `base_ref` arg | Parity with `git worktree add -b <name>`; no network |
| `delete_branch` | optional, default false; `-d` unless `force` | `git branch -d` refuses unmerged branches — committed work is never silently lost |
| Disposal metadata | sidecar JSON per worktree (creator, base SHA, job id) | Retrofitting metadata onto anonymous directories is the mess we audited; provenance enables prune, resume, and cross-session safety |
| Orphan cleanup | `prune` operation now; scheduled trigger deferred | The evidence says disposal is the hard problem; primitives can't be deferred |
| Detection scope | root identity only | Full config/hook/trust layering remains separate |
| env-restore model | single saved env (not a stack) | Restore to original repo root, not a previous worktree |
| Delegate isolation | Phase 2, same spec | Shares placement/metadata/control-env core; designing auto-clean now avoids retrofit |

## Cross-harness evidence (why rev 4 looks like this)

Claude Code's `EnterWorktree`/`ExitWorktree` contracts and its Agent-level
`isolation: "worktree"` were reviewed against rev 3, alongside a live audit of
this repo's worktree population. What transferred:

- **Exit is a first-class verb.** ExitWorktree restores the original cwd and
  carries the keep/remove decision, with a `discard_changes` gate that refuses
  to remove a worktree holding uncommitted files *or commits not on the
  original branch*, listing what would be lost. Rev 3 had no way back to the
  main root short of `remove`, and its dirty check missed unmerged commits.
- **Ownership scoping.** ExitWorktree only removes worktrees it created this
  session — never manual ones, never prior-session ones. Rev 4 records creator
  provenance in metadata and defaults to not destroying other sessions' work.
- **Enter-existing by path, validated by git.** EnterWorktree(path) admits any
  worktree registered in `git worktree list`. Serf users keep long-lived
  sibling worktrees; `switch` gains a `path` mode for them (§4).
- **Per-agent isolation is the dominant use.** Nearly all of the ~90 stale
  worktrees audited were created by agent fan-outs, not by a session entering
  a worktree for itself. Hence Phase 2 (§9).
- **"Auto-clean if unchanged" still leaks.** Changed worktrees are kept
  forever and nobody returns for them. Hence metadata + `prune` + staleness
  reporting in `list` — cleanup must be one call, not ninety.

## 1. Detection plumbing

### `ResolveMainRepoRoot(env, cwd) string`

**File:** `agent/execenv/gitpath.go` (new function, additive —
`GitRootOrEmpty` unchanged for callers that intentionally want the active
working-tree root).

Algorithm:

1. Find the nearest ancestor directory containing a `.git` entry (file or
   directory), walking up from `cwd`. **The walk and the `.git` pointer read
   use direct `os` calls, not the env's confined file API** — when serf is
   launched in a repo subdirectory, `.git` lives above `RootDir`, where
   `ensureUnderRoot` would reject env file reads and silently break the
   structural path.
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
Two companion call-site migrations are in scope:

- **Hub launch config/trust** uses local filesystem paths rather than
  `execenv.ExecutionEnvironment`, so it needs a small companion helper, e.g.
  `internal/gitpath.ResolveMainRepoRootLocal(cwd)`. The launch resolver should
  accept both roots: read repo/project config content from the active content
  root, but key trust metadata and legacy project state under the stable
  identity root.
- **Runtime state keying at launch:** `cmd/serf/run.go` (and the equivalent in
  `serve.go`) currently derive the default state dir via
  `agent.RuntimeDir(originURL, cfg.workDir, "")`. `RuntimeDir` keys off
  `originURL` when present — stable across worktrees — but for origin-less
  repos it falls back to `workDir`, so launching inside a worktree computes a
  *different* project state dir (and hence a different worktreeRoot, invisible
  to `list`). Fix at the source: pass
  `ResolveMainRepoRootLocal(cfg.workDir)` (falling back to `cfg.workDir` when
  not in a repo) as the `workDir` argument. This stabilizes *all* project
  state, not just worktrees.

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

The tool description must carry usage policy, mirroring Claude Code's guard:
worktrees are for isolated/parallel/risky work, **not** for ordinary branch
creation or switching — plain git commands cover those. The description is the
always-loaded surface; the `using-git-worktrees` skill loads conditionally.

### Definition

Single tool, `operation` enum (`create|list|switch|exit|remove|prune`),
mirroring `task_list`'s `action` pattern. Args by operation:

| Operation | Args |
|---|---|
| `create` | `name` (required), `base_ref` (optional, commit-ish, default active HEAD) |
| `list` | none |
| `switch` | `name` OR `path` (exactly one) |
| `exit` | none |
| `remove` | `name` (required), `force` (optional bool, default false), `delete_branch` (optional bool, default false) |
| `prune` | none |

Because the tool is non-read-only, `execToolBatch` serializes it between any
read-only batches. "Subsequent tools operate inside the worktree" means
subsequent calls in the ordered execution stream and every later tool round. A
read-only call that appears before `manage_worktree` in the same model response
still sees the old env, which is correct.

### `name` validation

`name` is used as **both** the git branch name and the final path component.
Strict validation before any git call (R2 MAJOR #4):

- Must match `^[A-Za-z0-9][A-Za-z0-9._/-]*$` and be at most 100 bytes.
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
`s.env` is rooted at `<worktree path>`, and `ExecCommand` rejects a
`workingDir` outside the env's `RootDir`. The implementation must not call
`currentEnv.ExecCommand(..., workingDir=mainRepoRoot, ...)` after entering a
worktree.

For local envs, build the control env with
`currentLocalEnv.WithWorkingDirectory(mainRepoRoot)` and use it only inside
`manage_worktree`. Note the asymmetry that makes this legal:
`ExecCommand` validates its `workingDir` against `RootDir`, but
`WithWorkingDirectory` re-roots unconditionally (`local.go:121-129`) — it may
target any directory, and shares `runningPIDs`/`fs` with the parent. Normal
shell/file/grep tools continue to receive the user-facing env for the active
worktree. If the execution environment is not a `LocalExecutionEnvironment`,
the tool should error clearly because env swapping and local git worktree
management are not supported.

Commands that operate *on a worktree from outside it* (dirty checks, the
`unchanged` predicate) run through the control env using `git -C <path> …` —
git changes directory itself; the control env's `workingDir` stays at the main
root.

## 3. `create` semantics

1. Resolve the main repo root via `ResolveMainRepoRoot(env, cwd)`.
2. Compute `projectid` (see §6).
3. Compute worktree path:
   `filepath.Join(worktreeRoot, projectid, name)`.
4. `MkdirAll` the parent of the worktree path (handles slash-containing names).
5. Run `git worktree add -b <name> -- <path> [<resolved-base-sha>]` through the
   git control env, with all tokens escaped. The branch name is `<name>` (no
   forced prefix). If the branch already exists, error; suggest `switch` only
   when a managed worktree of that name actually exists (otherwise the
   suggestion would also fail).
6. Write the metadata sidecar (§6) recording creator session, base SHA,
   original root, and creation time.
7. On success, swap `s.env` to `WithWorkingDirectory(<worktree path>)` via the
   `worktreeGuard`, **then** recompute `s.envInfo` and invalidate the cached
   system prompt (see §7). Store the pre-worktree env for later restoration.
8. Return the worktree absolute path, the branch name, and the main repo root.

Creating while already inside a managed worktree is allowed; the default
`base_ref` is the **active worktree's HEAD** (that is what "current HEAD"
means after an env swap), which is normally what a lane-off-a-lane wants. The
tool result states the base SHA so this is never ambiguous.

No gitignore dance — worktrees live outside the repo. No network/fetch —
`base_ref` is a local commit-ish or remote-tracking ref that must already
exist.

## 4. `switch` and `exit` semantics

### `switch`

By `name` (managed worktrees):

1. Resolve the worktree path from `name` (same `projectid` derivation, using
   the session's current main repo root).
2. Verify the path exists and is a valid worktree (has the `.git` pointer
   file).
3. Swap `s.env` directly to the target's `WithWorkingDirectory` — no
   intermediate restore step. The first `enterWorktree` saved the original env;
   later switches must not touch that saved env (§7), and an intermediate
   restore would only double the refresh cost.
4. Recompute `s.envInfo` + invalidate the cached system prompt (§7).
5. Return the worktree path and branch.

By `path` (any registered worktree — Claude Code parity):

1. `EvalSymlinks`-canonicalize the argument; require it to match (after the
   same canonicalization) a worktree path in `git worktree list --porcelain`
   run through the control env. Paths not registered to this repository are
   rejected — git's own registry is the validator.
2. Same env swap + refresh as above.
3. Worktrees entered by `path` that live outside the managed directory can be
   switched into and exited from, but never removed or pruned by this tool —
   `remove` stays managed-only.

This admits the human conventions that already exist around real repos:
long-lived sibling worktrees and hand-made lanes. Without it, the file tools'
root confinement makes those directories unreachable.

### `exit`

1. If the session is not currently in a worktree entered via this tool, return
   a clear non-destructive error ("not in a worktree").
2. Restore the saved pre-worktree env via `worktreeGuard.exitWorktree()`,
   clear the saved env (so a later `create`/`switch` saves afresh), recompute
   `s.envInfo`, invalidate the cached system prompt (§7).
3. The worktree, its branch, and any metadata are left untouched.
4. Return the restored root and the path of the worktree just left.

`exit` is what makes the merge-back workflow possible: create → work → commit
→ `exit` → merge/review from the main root → `remove`. Without it, `remove`
was the only way home and "peek at the main checkout, then come back" was
impossible under file-tool confinement.

## 5. `list`, `remove`, and `prune` semantics

### `list`

1. Run `git worktree prune` through the git control env first (clears stale
   metadata for already-deleted worktree dirs — R1 MAJOR #7).
2. Run `git worktree list --porcelain` through the git control env.
3. Filter to serf-managed worktrees: keep only entries whose worktree path is
   under `<worktreeRoot>/<projectid>/`. **Canonicalize both sides with
   `filepath.EvalSymlinks` before comparing** (git prints recorded paths;
   symlinked state homes and macOS `/var → /private/var` otherwise make
   managed worktrees silently vanish). Then use
   `filepath.Rel(projectidDir, entryPath)` and reject `..`-prefixed results,
   or `strings.HasPrefix(filepath.Clean(entryPath), projectidDir +
   string(filepath.Separator))` — **not** bare `HasPrefix`, which collides
   when one projectid prefixes another (R2 MAJOR #5).
4. Return structured output per worktree: path, branch/HEAD, whether the
   session is currently in it, and **disposal-relevant state** from metadata +
   cheap git queries: age, dirty (yes/no), commits ahead of recorded base,
   merged into the main root's HEAD (tip is an ancestor), creator session id,
   and owning job id if delegate-created (§9). This is what makes stale
   worktrees visible instead of silently accumulating.

### `remove`

1. Resolve the worktree path from `name`.
2. Verify the target path is under `<worktreeRoot>/<projectid>/` (canonicalized
   comparison as in `list`); never remove an arbitrary path by name.
3. **Live child/job guard (R2 MAJOR #6, widened):** refuse if any live subagent,
   delegate, or background shell job has a working directory equal to the target
   path or under it. This guard is based on live env/job working dirs, not only
   whether the parent session is currently in the target. A child may have been
   started with a working dir under a worktree while the parent has already
   switched elsewhere. **New plumbing:** delegate restore descriptors already
   record `WorkingDir`; background shell job records do not record a working
   directory today and must gain a launch-workdir field for this guard. The
   guard is best-effort — a shell command that `cd`s elsewhere after launch is
   invisible to it.
4. **Cross-session ownership guard:** if metadata records a different creator
   session, refuse without `force` and say who created it. The live-work guard
   above only sees *this* session's children; provenance is the only defense
   against yanking another session's active root (see Known limitations).
5. If `force` is false, preflight dirtiness before leaving the current worktree:
   run `git -C <path> status --porcelain=v1 --untracked-files=all` through the
   control env. If output is non-empty, error **listing the files at stake**
   and leave `s.env` unchanged. This preserves the user's current context when
   removal cannot proceed.
6. If the session is currently in this worktree, restore `s.env` to the
   pre-worktree env via `worktreeGuard`, then recompute `s.envInfo` + invalidate
   the cached system prompt (§7). If there is no safe restore env (for example,
   the session started directly inside the managed worktree being removed),
   refuse with a clear error instead of deleting the active root out from under
   the session. If a later step fails, the session is safely at the main root
   and the worktree still exists — state stays consistent.
7. Run `git worktree remove [--force] -- <path>` through the git control env.
   `--force` is included only when `force: true`.
8. If `delete_branch: true`, run `git branch -d -- <name>` through the git
   control env — **`-d`, not `-D`**: git refuses to delete an unmerged branch,
   so committed-but-unmerged work is never silently destroyed by a
   clean-tree removal. Escalate to `-D` only when `force: true`. On an `-d`
   refusal, surface git's message (which names the unmerged tip) so the agent
   can merge first or re-invoke with `force`.
9. Remove the metadata sidecar; run `git worktree prune`.
10. Return confirmation with the removed path and whether the branch was
    deleted.

### `prune`

One call that does what ninety manual `remove`s never happen to do:

1. Enumerate managed worktrees as in `list`.
2. For each candidate, remove it (worktree + branch + metadata) iff **all**
   hold:
   - not the session's current worktree, and no live work under it (same
     guards as `remove` steps 3-4; other-session creators are skipped, never
     forced);
   - clean per `git -C <path> status --porcelain=v1 --untracked-files=all`;
   - **disposable**: either `unchanged` (HEAD equals the recorded base SHA —
     nothing was ever committed) or **merged** (branch tip is an ancestor of
     the main root's HEAD). Unchanged branches are deleted with `-d` falling
     back to `-D` (a tip equal to base can be unreachable from any upstream,
     which `-d` refuses on a technicality); merged branches always satisfy
     `-d`.
3. Report removed and skipped entries with per-entry reasons. `prune` never
   takes `force`; anything it skips is `remove`'s job, deliberately.

The `unchanged` predicate (clean tree AND tip == recorded base SHA) is shared
with Phase 2's auto-disposal (§9). It is why metadata must record the base SHA
at creation.

## 6. projectid, worktreeRoot, and metadata

### projectid

`projectid = <safe-basename>-<sha256[:16]>` where:

- `<safe-basename>` is `filepath.Base` of the canonical (symlink-resolved)
  absolute repo root, sanitized to `[A-Za-z0-9._-]`, trimmed of leading `.`/`-`,
  truncated to 48 bytes, and replaced with `repo` if empty.
- `<sha256[:16]>` is the first 16 hex chars of `sha256(canonicalAbsRoot)`.

Example: `/home/jesse/git/prime-radiant/serf` → `serf-a1b2c3d4e5f6a7b8`.

This replaces the originally-proposed sanitized-path scheme (option C), which
adversarial review (R2 MAJOR #3) showed collides (`/a/b` and `/a/b_` both
sanitize to `_a_b`) and overflows the 255-byte filesystem name limit on deeply
nested repos. The hash form is fixed-length and collision-resistant. One
caveat the earlier draft got backwards: `sha256(path)` is case-*sensitive*, so
on a case-insensitive filesystem two casings of the same root produce two
distinct projectids (identity split, not collision). OS-reported cwds have
consistent casing in practice; documented, not defended against.

### worktreeRoot derivation

`worktreeRoot` must honor the runtime state directory that launched this
session:

1. If `s.stateDir` is non-empty, use `filepath.Join(s.stateDir, "worktrees")`.
   This respects `--state-dir`, `SERF_STATE_DIR`, and the normal
   `agent.RuntimeDir(...)` project state directory selected by `serf run` and
   `serf serve` — which, per §1, must key off the resolved main repo root so
   that sessions launched inside a linked worktree land in the same state dir.
2. If `s.stateDir` is empty (tests or persistence-off sessions), derive a
   fallback with agent-owned state logic, not `cmdutil.DefaultStateRoot()`.
   A small helper can use `agent.RuntimeDir(gitOriginURL(env, mainRoot),
   mainRoot, "")` and then append `worktrees`.

The agent package should not import `cmdutil`. `cmdutil.DefaultStateRoot()`
currently points at provider/auth state (`$SERF_STATE_DIR` else `~/.serf`) and
would ignore the resolved runtime state directory in common launches.

### Metadata sidecar

Every managed worktree gets a JSON sidecar at
`<worktreeRoot>/<projectid>/.meta/<name>.json` (slash-containing names mirror
their nesting under `.meta/`). Written at create, deleted at remove/prune.
Never inside the worktree's working tree (would dirty it) and never inside
`.git` (not ours). Fields:

```json
{
  "name": "feature/foo",
  "branch": "feature/foo",
  "base_sha": "<resolved base commit>",
  "original_root": "/abs/path/to/main/repo",
  "creator_session": "<session id>",
  "job_id": "<job id, when delegate-created (§9), else omitted>",
  "created_at": "RFC3339"
}
```

Consumers: `remove`'s cross-session guard, `prune`'s `unchanged` predicate,
`list`'s staleness report, resume re-entry (§7), and Phase 2 auto-disposal
(§9). A worktree without a sidecar (hand-made inside the managed dir, or a
pre-metadata stray) is listed with `"unmanaged_meta": true` and is skipped by
`prune`; `remove` treats it as another session's (refuse without `force`).

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
- `exitWorktree()` — restore the saved env and clear it, or report that no
  safe restore env exists.
- `liveWorkUnder(path string) []string` — returns live child/delegate/shell
  work handles whose working directory is equal to `path` or below it. `remove`
  and `prune` use this for the guard in §5. Requires the new shell-job
  launch-workdir field (§5 remove step 3).

All methods operate under `s.mu` or return snapshots built under `s.mu` — with
the one deliberate exception below (git snapshot computation).

### `currentEnv()` accessor (BLOCKER fix — R1 #3/#5/#8)

Introducing the first-ever mutation of `s.env` makes every existing unlocked
`s.env` read a data race. Fix: add a `currentEnv()` accessor under `s.mu`
(mirrors `currentProfile()` at `session.go:455-459`). Audit and convert the
unlocked `s.env` reads — at minimum these, **re-grepped at implementation
time** (the tree moves; adjacent reads in the same functions such as
`subagents.go:500` and `job_delegate.go:842/854/858` are covered by auditing
whole functions, not just the listed lines):

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

Fix: on every successful `create`/`switch`/`exit`/`remove`-that-restores,
through a single helper:

1. **Outside `s.mu`:** compute the new `EnvInfo` — `envInfoFromEnv(newEnv,
   s.sclock())` plus the git snapshot fields normally populated during init
   (`IsGitRepo`, `GitBranch`, modified/untracked file counts, recent commits,
   origin URL). The git snapshot forks several `git` subprocesses and `status`
   can take seconds on a big repo; **`s.mu` must not be held across them** —
   holding it would stall every event emit, `Meta()` autosave, and hub poll.
   `manage_worktree` is non-read-only and serialized in the tool stream, so
   nothing else can be swapping the env concurrently; computing against the
   new env before publishing it is safe.
2. **Under `s.mu`, atomically:** assign `s.env` and `s.envInfo`, then
   `s.rebuildToolDefsCache()` and `s.refreshSystemPromptCache()`.

This mirrors `SetModel`'s invalidation pattern (`session.go:560-561`) for the
cache steps; note `SetModel` also does no subprocess work under the lock.

**What deliberately stays frozen:** project docs, project skills, and project
MCP config are loaded once at init (`session_init.go:548/599/1120`) and are
*not* reloaded on a swap — consistent with today's behavior across a plain
`git checkout`. Project prompt *sections* do follow the swap, because
`renderSystemPrompt` recomputes the git root per render
(`session_prompts.go:141`). This is a documented scope line, not an accident:
a worktree branched from the current HEAD has identical docs anyway, and
reloading skills/MCP mid-session is its own project.

### env-restore model

Single saved env, not a stack. The first successful `enterWorktree` saves the
current env; later `switch` calls do not replace that saved env. If the session
creates worktree A, switches to B, then removes B, restore lands at the
original repo root, not A. Rationale: A's state may have changed while we
were in B; landing at the stable original root is safer than a stale A. The
agent can `switch` back to A explicitly if needed. `exitWorktree` clears the
saved env after restoring, so the next enter saves afresh.

If a session starts directly inside a managed worktree, there may be no safe
non-target restore env. `remove` of that active worktree must refuse unless a
safe restore env can be constructed and verified.

### Persistence and resume (new in rev 4)

The env swap is in-memory, and today's resume path rebuilds the env from the
launch cwd (`cmd/serf/run.go:169`: `env :=
execenv.NewLocalExecutionEnvironment(cfg.workDir)`), so without explicit
handling a session that dies inside a worktree resumes at the launch root
while its transcript, persisted `EnvInfo`, and system-prompt history all say
it is in the worktree — exactly the stale-identity confusion this section
exists to prevent, reintroduced through the restore door.

Fix:

- Persist worktree state in `schema.SessionMeta`: the active managed worktree
  path (empty when not in one) and the restore root (the original env's
  `RootDir`). Written by the same meta-save that already persists `EnvInfo`.
- On `RestoreSessionFromMeta*`: if meta records an active worktree, the path
  still exists, and it is still a registered worktree of the repo (validated
  as in `switch` by path), re-enter it **before** `initSessionState` runs —
  root the session env at the worktree and set the saved restore env from the
  recorded restore root. Init's normal `envInfoFromEnv` snapshot then sees the
  right directory; no special refresh needed.
- If the worktree is gone (removed between runs), start at the restore root
  and surface a clear notice in the session-start output so the model is told
  its previous working directory no longer exists.

Delegate children need no extra work: their restore descriptors already
persist `WorkingDir` (`job_delegate.go:881-889`), which will simply be a
worktree path.

### `WithWorkingDirectory` correctness

`WithWorkingDirectory` (`local.go:119`) shares `runningPIDs` and `fs` with the
parent env — so PID tracking and afero backing survive a swap, and a
post-swap `s.env.Cleanup()` still terminates processes started before the
swap. The swap must always use `WithWorkingDirectory`, never
`NewLocalExecutionEnvironment` (which would lose PID sharing). The spec
mandates a comment + test asserting this.

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
  `switch` only when the managed worktree exists).
- `switch`/`remove` to a nonexistent worktree → error.
- `switch` by `path` to a path not in `git worktree list` → error.
- `remove` target resolves outside the managed worktree directory → error.
- `exit` when not in a worktree → clear non-destructive error.
- `remove` on a dirty worktree without `force` → error listing the dirty
  files, without changing the session env.
- `remove` with `delete_branch` on an unmerged branch without `force` →
  worktree removed, branch deletion refused with git's unmerged-tip message.
- `remove` when live children/delegates/shell jobs are rooted under the target
  worktree → error (live work guard).
- `remove` of a worktree created by another session without `force` → error
  naming the creator.
- `remove` of the active worktree with no safe restore env → error.
- `prune` → never errors on skips; reports per-entry skip reasons.
- Non-local execution environment → `manage_worktree` errors clearly.
- `git` unavailable → `ResolveMainRepoRoot` uses the structural `.git`-pointer
  path (no git binary needed for the common linked-worktree case);
  `create`/`switch`/`exit`/`remove`/`list`/`prune` require git for lifecycle
  operations and error clearly if absent.

## 9. Phase 2 — delegate worktree isolation

The dominant real-world worktree use is not a session entering a worktree for
itself; it is parallel delegate lanes needing isolation from each other and
from the main checkout. Manual lane management has produced exactly the
failures this feature exists to prevent (lanes writing to the wrong checkout).
Phase 2 makes isolation a property of the *delegation*, not a behavior the
child must remember to perform.

### Tool surface

`delegate` gains an optional `isolation: "worktree"` argument (absent →
today's behavior). Only valid for local execution environments; errors
clearly otherwise. Mutually exclusive with an explicit `working_dir`.

### Lifecycle

1. **At job creation**, the parent-side harness (not the child): resolves the
   main root, creates a managed worktree named `job-<jobid>` branched from the
   **parent's active HEAD** (the worktree branch tip if the parent is in one —
   the lane sees what the parent is looking at), writes the metadata sidecar
   with `job_id` and the parent's session id, and roots the child env at the
   worktree via `WithWorkingDirectory`. The child's restore descriptor
   `WorkingDir` is the worktree path, so delegate resume works through the
   existing machinery unchanged.
2. **The child cannot stray**: its `RootDir` is the worktree leaf; file tools
   are confined there and `ExecCommand` rejects working dirs outside it. The
   child does not get `manage_worktree` mutations over its own isolation
   (create/switch/exit/remove are root-session concerns; `list` is harmless).
3. **At terminal job completion** (result finalized), the parent-side harness
   evaluates the shared `unchanged` predicate (§5) through the control env:
   - **unchanged** → remove worktree + branch + metadata silently; the job
     result mentions nothing was left behind.
   - **changed** (commits or dirty tree) → keep; the job's final result
     carries the worktree path, branch, commits-ahead count, and dirty state
     so the parent can merge, inspect via `switch`, or `remove` explicitly.
4. **On job stop/kill or an unclean child death** → keep (safety); the
   worktree shows up in `list` with its `job_id`, and `prune` collects it once
   merged or if it turns out unchanged.

Auto-disposal on completion plus `prune` for the remainder is the answer to
the audited leak: unchanged lanes vanish immediately, merged lanes are one
`prune` away, and only genuinely unfinished work persists — visibly.

### Guards

`remove`/`prune` already refuse while live work is rooted under a worktree
(§5); a live delegate job in its isolation worktree is exactly that case.
`DrainJobTree`/session close do not remove worktrees — keep-and-record is the
close-time policy, and the metadata makes later collection safe.

## 10. Testing

Per `AGENTS.md`: no network, no provider credentials, deterministic. The
worktree tool is pure plumbing — git operations on temp repos. Tests use real
`git` (a build tool, always available in CI) on temp directories:

- `create` → verify linked worktree exists, `.git` is a pointer file, `s.env`
  swapped, git snapshot fields recomputed, system prompt invalidated, metadata
  sidecar written with the resolved base SHA.
- `create` from inside a worktree → base defaults to the active worktree's
  HEAD, not the main root's.
- `switch` between two worktrees → verify env points at the right one, envInfo
  refreshed, saved restore env untouched, single swap (no intermediate
  restore).
- `switch` by `path` → registered non-managed worktree accepted; unregistered
  path rejected; symlinked path spelling accepted (canonicalization).
- `exit` → env restored to original root, saved env cleared, worktree and
  branch intact; `exit` outside a worktree errors without side effects;
  create→exit→switch round-trip returns to the same worktree.
- Same-response ordering: read-only call before `manage_worktree` sees old env;
  read-only call after it sees the new env.
- `remove` clean and dirty (with/without `force`, with/without
  `delete_branch`).
- `remove` with `delete_branch` on a branch with unmerged commits → branch
  survives (`-d` refusal surfaced) without `force`; deleted with `force`.
- Dirty remove without force leaves `s.env` unchanged and lists the files.
- `remove`-current with no safe restore env → verify refusal.
- `remove` of a worktree whose metadata names another creator session →
  refusal without `force`.
- `remove` while live subagents/delegates/shell jobs have working dirs under
  the target → verify refusal, including the case where the parent has already
  switched elsewhere, and including a background shell job (exercises the new
  launch-workdir field on job records).
- `prune` → removes unchanged and merged-clean worktrees (branches and
  metadata included); skips the active worktree, live-work worktrees,
  dirty/unmerged ones, other-session creators, and sidecar-less strays — each
  with a reason in the report.
- `list` returns expected entries with staleness fields (age, dirty, ahead,
  merged, creator); prunable git metadata cleared; non-serf worktrees
  excluded; prefix-collision filtering correct; symlinked worktreeRoot still
  matches (canonicalization).
- Resume: persist meta with an active worktree → `RestoreSessionFromMeta*`
  re-enters it (env rooted at the worktree, restore root recorded); worktree
  deleted between runs → resume lands at the restore root with a notice.
- `ResolveMainRepoRoot` on a linked worktree returns the main root, not the
  worktree dir; submodule falls through to `--git-common-dir`; works when the
  session was launched in a repo *subdirectory* (structural walk must not be
  confined by the env root) and without the git binary for the standard
  pointer-file case.
- Root semantics: project docs, project skills, project MCP config, and project
  prompt sections continue to read from the active worktree root, while managed
  worktree placement and hub launch trust/meta state key off the stable main
  repo root from inside a linked worktree. `serf run`/`serve` state keying:
  launching from inside a linked worktree of an origin-less repo resolves the
  same runtime state dir as launching from the main root.
- Git control env: after entering a worktree, `list`, `switch`, `remove`,
  `prune`, and a second `create` still work even though the main repo root is
  outside the user-facing env root.
- Tool visibility: `manage_worktree` appears in `ToolDefinitions()` as a
  registry-only tool and is non-read-only.
- State root: worktree placement honors explicit `StateDir`/`SERF_STATE_DIR`
  test overrides and does not use provider/auth state by accident.
- projectid: two distinct repos with the same basename produce distinct ids;
  fixed-length regardless of path depth; unsafe basename characters are
  sanitized.
- `name` validation: rejects `..`, leading `-`, trailing `/`, `@{`, spaces,
  over-length names; rejects git-invalid refnames caught by
  `git check-ref-format`; allows `feature/foo`.
- `base_ref` validation rejects whitespace/control chars, leading `-`, and
  nonexistent refs; accepts a local branch, tag, remote-tracking ref, and SHA.
- Data-race: `-race` run of create/switch/exit/remove with concurrent read-only
  tool dispatch, child creation, delegate restore env creation, status events,
  and Close cleanup passes.
- Lock discipline: the env-swap helper computes the git snapshot outside
  `s.mu` (assert via a test hook or by verifying no `git` invocation happens
  while the lock is held — at minimum, a code-review checklist item plus the
  race test above).
- Phase 2: delegate with `isolation: "worktree"` → child env rooted at a fresh
  managed worktree with `job_id` metadata; unchanged child → auto-removed on
  completion (worktree, branch, sidecar all gone); changed child → kept, job
  result carries path/branch/ahead/dirty; killed child → kept; parent `remove`
  of a live delegate's worktree → refused; delegate resume lands back in its
  worktree via the existing restore descriptor.
- Fuzz target for arg validation (extends `FuzzToolArgsValidate` table).

## 11. Known limitations

- **Bare repos:** `ResolveMainRepoRoot`'s structural walk-up from
  `bare.git/worktrees/<n>` yields `bare.git`'s parent, not `bare.git` itself;
  `--git-common-dir` returns `bare.git` (correct). Documented best-effort; a
  future spec can add bare-repo detection (R2 MINOR #7).
- **Submodules:** neither the structural path nor `--git-common-dir` resolves
  the superproject root; `--git-common-dir` returns the submodule's `.git`.
  The projectid keys off the submodule root, not the superproject. This is
  probably acceptable (submodule-as-own-project) but is documented (R2 MINOR
  #8).
- **Cross-session live-work blindness:** the live-work guard sees only this
  session's children. Another session concurrently *rooted* in a worktree is
  protected only by the creator-session metadata guard (refuse without
  `force`) — a determined `force` from session B can still yank session A's
  root. Full protection needs a liveness registry lookup; deferred.
- **Case-insensitive filesystems:** two casings of the same repo root produce
  two projectids (see §6). Not defended against.
- **Shell `cd` escapes:** the live-work guard keys off recorded launch working
  dirs; a background command that `cd`s into a worktree after launch is
  invisible to it.
- **Docs/skills/MCP staleness after switch:** loaded once at init by design
  (§7); a worktree on a divergent branch keeps the original root's versions
  for the rest of the session.
- **Scheduled orphan sweeping:** deferred; `prune` is the primitive a future
  daemon trigger calls.

## Adversarial review record

Two subagents reviewed the original design adversarially. Reviewer #2 won the
scoring (5 pts): 8/8 legitimate findings vs reviewer #1's 9/10 (1
false-positive sandbox-escape blocker that R2 empirically disproved). All 8
legitimate findings are incorporated above as revision notes. The false
positive (sandbox-escape via `RootDir` under `~/.serf`) is discarded with
evidence.

Rev 3 added the follow-up implementation-feasibility fixes: root-sensitive
call-site migration, runtime-state-based worktree placement, a git control env,
tool visibility, stronger ref/base validation, safer remove semantics, and a
wider live-work guard.

Rev 4 followed a code-verified adversarial review of rev 3 (every cited line
and API semantic checked against the tree) plus a comparison against Claude
Code's EnterWorktree/ExitWorktree/agent-isolation contracts and a live audit
of this repo's worktree population (~90 stale harness worktrees). Changes:
added `exit` (rev 3 had no non-destructive way back under file-tool
confinement); persistence/resume semantics (rev 3's swap silently reverted on
resume); git snapshot moved outside `s.mu` (rev 3 held the session lock across
git forks); disposal designed in (metadata sidecar, `unchanged` predicate,
`prune`, `-d`-unless-force branch deletion, `list` staleness, close-time
keep-and-record); shell-job launch-workdir plumbing named (the live-work guard
had no data source for shell jobs); cross-session creator guard;
`switch`-by-path for git-registered worktrees; `serf run`/`serve` RuntimeDir
keying moved to the main root (origin-less repos); symlink-canonicalized path
comparisons; structural walk pinned to direct `os` calls; corrected the
projectid case-sensitivity claim; and Phase 2 delegate worktree isolation.
Placement stayed in the state dir (decided against in-repo `.serf/worktrees/`:
immunity to `git clean -dxf` outweighs path legibility, and the run/serve
keying fix removes the discovery-instability argument).
