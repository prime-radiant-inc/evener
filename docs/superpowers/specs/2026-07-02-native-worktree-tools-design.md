# Native Worktree Tools — Design Spec

Date: 2026-07-02
Status: Draft (rev 6, post competitive adversarial review of rev 5)

## Summary

Serf gets native git-worktree support, modeled on codex's detection approach
(linked-worktree root resolution) and Claude Code's creation lifecycle, with
one correction the live evidence demands: **disposal is designed in from day
one**. An audit of this repo found ~90 stale harness-created worktrees and
orphaned branches under `.claude/worktrees/` — the residue of a design that
made creation easy and cleanup an afterthought. This spec ships the disposal
primitives (per-worktree metadata, an `unchanged` predicate, `exit`, `prune`,
safe branch deletion, git-native occupancy locks) in v1.

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
4. **Delegate worktree isolation:** `isolation: "worktree"` on the
   `delegate` tool. The harness creates a managed worktree per delegate, roots
   the child env there (so lane-straying is structurally impossible), and
   auto-removes it at parent-session close when the delegate left no changes.
   This ships in the same delivery as the tool — it is the dominant real-world
   use, and it consumes the same placement/metadata/control-env core.

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
  tool, with disposal safe by default: no destruction of uncommitted files,
  unmerged commits, or another session's active root without an explicit,
  deliberate act.
- Give parallel delegate lanes native worktree isolation — the dominant
  real-world use, and the one manual management has repeatedly botched
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
- **A session-liveness registry.** Occupancy is tracked with git-native
  `worktree lock` markers (§5); reconciling a *stale* lock left by a crashed
  session is a deliberate act (human or agent via `git worktree unlock`), not
  something this tool automates.
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
| Base resolution | Always via the **active** env root, always passed as an explicit SHA | HEAD is a per-worktree ref; resolving through the main-root control env would branch lane-off-a-lane from the wrong tip |
| Occupancy | `git worktree lock` on enter, unlock on leave | Git itself then refuses removal/prune from any tool, in any session — no bespoke registry needed |
| Placement | `<worktreeRoot>/<projectid>/<name>/` | Outside the project tree → no gitignore, immune to `git clean -dxf`; honors runtime state-dir selection |
| projectid | `<basename>-<sha256[:16]>` of canonical abs root | Collision-resistant, fixed-length (see §6) |
| baseRef default | active HEAD, optional validated `base_ref` arg | Parity with `git worktree add -b <name>`; no network |
| `delete_branch` | optional, default false; `-d` unless `force` | `git branch -d` refuses unmerged branches — committed work is never silently lost |
| Disposal metadata | sidecar JSON per worktree (creator, base SHA, delegate id) | Retrofitting metadata onto anonymous directories is the mess we audited; provenance enables prune, resume, and residue reconciliation |
| Orphan cleanup | `prune` operation now; scheduled trigger deferred | The evidence says disposal is the hard problem; primitives can't be deferred |
| Detection scope | root identity only | Full config/hook/trust layering remains separate |
| env-restore model | single saved env (not a stack) | Restore to original repo root, not a previous worktree |
| Delegate isolation | In scope, same delivery | Dominant real-world use; shares the placement/metadata/control-env core |
| Delegate disposal point | Parent-session close (post-drain), not job end | Delegates are durable multi-job sessions; per-job disposal would delete the root under an idle-but-restorable child |

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
  a worktree for itself. Hence delegate isolation (§9).
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
   **The output is relative from a main checkout but absolute from a linked
   worktree** (empirically verified): apply `filepath.Join(cwd, common)` only
   when `!filepath.IsAbs(common)`, then `EvalSymlinks`, then take the parent
   as the candidate root.
   - **Sanity-check the candidate before returning it:** the candidate must
     itself contain a `.git` entry that resolves back to the common dir. In a
     submodule the common dir is `<super>/.git/modules/<sub>`, whose parent is
     `<super>/.git/modules` — not a working tree at all, and the *same* path
     for every submodule of the superproject. When the check fails, run
     `git rev-parse --show-toplevel` instead and return that: inside a
     submodule's primary checkout this is the submodule working-tree root,
     which is the intended submodule-as-own-project identity. (Linked
     worktrees *of a submodule* remain unsupported — see Known limitations.)
   - **Do NOT use `git rev-parse --show-toplevel` as the general fallback** —
     it returns the worktree dir, not the main root, when run inside a linked
     worktree of a normal repo.
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

- Must match `^[A-Za-z0-9_][A-Za-z0-9_./-]*$` and be at most 100 bytes.
  Underscore is a legal git-ref character and appears in serf's own generated
  ids (`dlg_…`, `job_…`), which §9 uses as worktree names — a regex without it
  would reject the feature's own names (rev-5 review finding).
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

### Base resolution (always from the ACTIVE root, always an explicit SHA)

HEAD is a **per-worktree** ref. The control env (below) is rooted at the main
repo root, so resolving `HEAD` — or any per-worktree ref — through it yields
the *main checkout's* tip, not the tip the agent is looking at. Rev 5
prescribed exactly that by accident (review finding, both reviewers): "default
= active HEAD" cannot be delivered by an unqualified `git worktree add`
through the control env.

Therefore:

- `base_ref` defaults to the literal `HEAD` when absent. When present, trim
  it, reject whitespace/control characters and leading `-`.
- Resolve it **against the session's active root**, not the control root:
  `git -C <activeRoot> rev-parse --verify --quiet <base_ref>^{commit}`
  (run through the control env; `-C` makes git do the directory change, so
  `ExecCommand`'s workingDir confinement is untouched). `<activeRoot>` is the
  current env's `RootDir` — the worktree if the session is in one, else the
  original root.
- **Always pass the resolved SHA explicitly** to `git worktree add`; never
  invoke it with the base omitted. The same resolved SHA is what the metadata
  sidecar records as `base_sha` (§6).

This makes lane-off-a-lane and delegate isolation from a worktree-resident
parent branch from the tip the agent (or parent) is actually on, catches typos
before creating directories, avoids option-like commit-ish values, and keeps
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

Commands that operate *on a worktree or another checkout from outside it*
(dirty checks, the `unchanged` predicate, active-root base resolution above)
run through the control env using `git -C <path> …` — git changes directory
itself; the control env's `workingDir` stays at the main root.

## 3. `create` semantics

1. Resolve the main repo root via `ResolveMainRepoRoot(env, cwd)`.
2. Compute `projectid` (see §6).
3. Compute worktree path:
   `filepath.Join(worktreeRoot, projectid, name)`.
4. Validate `name` (regex + `git check-ref-format --branch`); resolve the base
   to a SHA from the active root (§2). If the branch already exists, error;
   suggest `switch` only when a managed worktree of that name actually exists
   (otherwise the suggestion would also fail).
5. `MkdirAll` the parent of the worktree path (handles slash-containing
   names) and **write the metadata sidecar first** (§6), recording the
   resolved `base_sha`, creator session, and original root. Sidecar-first
   ordering means a crash at any later step leaves a sidecar without a
   worktree, which `prune`'s reconciliation sweep (§5) detects and cleans; the
   reverse order would leave an anonymous worktree that `prune` must skip and
   `remove` refuses without `force`.
6. Run `git worktree add -b <name> -- <path> <resolved-base-sha>` through the
   git control env, with all tokens escaped.
7. **Lock the worktree**: `git worktree lock --reason "serf:<session-id>" --
   <path>`. The lock is the occupancy marker (§5): while the session is rooted
   here, git itself refuses `worktree remove`/`prune` from any tool in any
   session.
8. On success, swap `s.env` to `WithWorkingDirectory(<worktree path>)` via the
   `worktreeGuard`, **then** recompute `s.envInfo` and refresh the cached
   system prompt (see §7). Store the pre-worktree env for later restoration.
9. Return the worktree absolute path, the branch name, the base SHA, and the
   main repo root.

Creating while already inside a managed worktree is allowed; the default base
resolves to the **active worktree's HEAD** via the active-root rule in §2, and
the tool result states the base SHA so this is never ambiguous.

No gitignore dance — worktrees live outside the repo. No network/fetch —
`base_ref` is a local commit-ish or remote-tracking ref that must already
exist.

## 4. `switch` and `exit` semantics

### `switch`

By `name` (managed worktrees):

1. Resolve the worktree path from `name` (same `projectid` derivation, using
   the session's current main repo root).
2. Verify the path exists and is a valid worktree (has the `.git` pointer
   file). If it is locked with a reason other than this session's own marker,
   refuse — another session (or a delegate) is rooted there.
3. If currently in a managed worktree, **unlock it** (only if the lock reason
   is this session's own marker); **lock the target** with this session's
   marker. Locks follow occupancy, not creation.
4. Swap `s.env` directly to the target's `WithWorkingDirectory` — no
   intermediate restore step. The first `enterWorktree` saved the original env;
   later switches must not touch that saved env (§7), and an intermediate
   restore would only double the refresh cost.
5. Recompute `s.envInfo` + refresh the cached system prompt (§7).
6. Return the worktree path and branch.

By `path` (any registered worktree — Claude Code parity):

1. `EvalSymlinks`-canonicalize the argument; require it to match (after the
   same canonicalization) a worktree path in `git worktree list --porcelain`
   run through the control env. Paths not registered to this repository are
   rejected — git's own registry is the validator.
2. Same env swap + refresh as above. **No lock choreography**: serf does not
   mutate lock state on worktrees it does not manage (the user may have their
   own locking conventions). If leaving a *managed* worktree for a by-path
   one, the managed worktree is unlocked as in step 3 above.
3. Worktrees entered by `path` that live outside the managed directory can be
   switched into and exited from, but never removed or pruned by this tool —
   `remove` stays managed-only.

This admits the human conventions that already exist around real repos:
long-lived sibling worktrees and hand-made lanes. Without it, the file tools'
root confinement makes those directories unreachable.

### `exit`

1. If the session is not currently in a worktree entered via this tool, return
   a clear non-destructive error ("not in a worktree").
2. If the current worktree is managed and locked with this session's marker,
   unlock it.
3. Restore the saved pre-worktree env via `worktreeGuard.exitWorktree()`,
   clear the saved env (so a later `create`/`switch` saves afresh), recompute
   `s.envInfo`, refresh the cached system prompt (§7).
4. The worktree, its branch, and its metadata are left untouched.
5. Return the restored root and the path of the worktree just left.

`exit` is what makes the merge-back workflow possible: create → work → commit
→ `exit` → merge/review from the main root → `remove`. Without it, `remove`
was the only way home and "peek at the main checkout, then come back" was
impossible under file-tool confinement.

## 5. `list`, `remove`, and `prune` semantics

### Occupancy locks (shared mechanism)

A session or isolated delegate rooted in a managed worktree holds a
`git worktree lock` on it with a structured reason (`serf:<session-id>` or
`serf:dlg:<delegate-id>:<parent-session-id>`), taken on enter/create and
released on exit/switch-away/disposal. This is the one mechanism that makes
all the occupancy hazards found in review converge:

- git itself refuses `git worktree remove`/`git worktree prune` on a locked
  tree — even from other serf sessions, other tools, or a bare `git` command;
- `list` can *show* who is where (the porcelain output carries
  `locked <reason>`);
- `prune` gets a creator-independent skip rule that protects live occupants
  without starving dead sessions' leftovers (a dead session's worktree is
  normally unlocked, because `exit`/close unlocked it).

A crash while inside a worktree leaves a **stale lock**. That is deliberate
fail-safe behavior: the reason names the owning session, `list` surfaces it,
and clearing it is an explicit `git worktree unlock` by a human or agent who
has verified the owner is dead. `manage_worktree` never force-unlocks and
`force` does not escalate to git's `remove -f -f` double-force.

### `list`

1. Run `git worktree list --porcelain` through the git control env. **Do not
   run `git worktree prune` here** (rev-5 review finding: bare `worktree
   prune` is repo-wide and instantly, irreversibly deregisters *any* worktree
   whose directory is momentarily absent — e.g. a user's sibling worktree on
   an unmounted volume — which `list` must never do). Instead, surface the
   porcelain `prunable <reason>` annotation on affected entries.
2. Filter to serf-managed worktrees: keep only entries whose worktree path is
   under `<worktreeRoot>/<projectid>/`. **Canonicalize both sides with
   `filepath.EvalSymlinks` before comparing** (git prints recorded paths;
   symlinked state homes and macOS `/var → /private/var` otherwise make
   managed worktrees silently vanish). Then use
   `filepath.Rel(projectidDir, entryPath)` and reject `..`-prefixed results,
   or `strings.HasPrefix(filepath.Clean(entryPath), projectidDir +
   string(filepath.Separator))` — **not** bare `HasPrefix`, which collides
   when one projectid prefixes another (R2 MAJOR #5).
3. Return structured output per worktree: path, branch/HEAD, whether the
   session is currently in it, lock state + reason, prunable annotation, and
   **disposal-relevant state** from metadata + cheap git queries: age, dirty
   (yes/no), commits ahead of recorded base, merged into the main root's HEAD
   (tip is an ancestor), creator session id, and owning delegate id if
   delegate-created (§9). This is what makes stale worktrees visible instead
   of silently accumulating.

### `remove`

1. Resolve the worktree path from `name`.
2. Verify the target path is under `<worktreeRoot>/<projectid>/` (canonicalized
   comparison as in `list`); never remove an arbitrary path by name.
3. **Lock guard:** if the target is locked and the reason is not this
   session's own marker, refuse — regardless of `force` — naming the lock
   reason. Releasing someone else's lock is a deliberate `git worktree
   unlock`, not a tool flag.
4. **Live child/job guard (R2 MAJOR #6, widened):** refuse if any live subagent,
   delegate, or background shell job has a working directory equal to the target
   path or under it. This guard is based on live env/job working dirs, not only
   whether the parent session is currently in the target. A child may have been
   started with a working dir under a worktree while the parent has already
   switched elsewhere. **New plumbing:** delegate restore descriptors already
   record `WorkingDir`; background shell job records do not record a working
   directory today and must gain a launch-workdir field for this guard. The
   guard is best-effort — a shell command that `cd`s elsewhere after launch is
   invisible to it.
5. **Cross-session ownership guard:** if metadata records a different creator
   session, refuse without `force` and say who created it. (Occupancy is the
   lock's job — this guard is about provenance: don't casually delete work you
   didn't create.)
6. If `force` is false, preflight dirtiness before leaving the current worktree:
   run `git -C <path> status --porcelain=v1 --untracked-files=all` through the
   control env. If output is non-empty, error **listing the files at stake**
   and leave `s.env` unchanged. This preserves the user's current context when
   removal cannot proceed.
7. If the session is currently in this worktree, unlock it and restore `s.env`
   to the pre-worktree env via `worktreeGuard`, then recompute `s.envInfo` +
   refresh the cached system prompt (§7). If there is no safe restore env (for
   example, the session started directly inside the managed worktree being
   removed), refuse with a clear error instead of deleting the active root out
   from under the session. If a later step fails, the session is safely at the
   main root and the worktree still exists — state stays consistent.
8. Run `git worktree remove [--force] -- <path>` through the git control env.
   `--force` is included only when `force: true` (and only covers git's
   dirty/untracked refusal — never locks, per step 3).
9. If `delete_branch: true`, run `git branch -d -- <name>` through the git
   control env — **`-d`, not `-D`**: git refuses to delete an unmerged branch,
   so committed-but-unmerged work is never silently destroyed by a
   clean-tree removal. Escalate to `-D` only when `force: true`. On an `-d`
   refusal, surface git's message (which names the unmerged tip) so the agent
   can merge first or re-invoke with `force`.
10. **Sidecar disposition:** if the branch is now gone (deleted here, or never
    existed), delete the metadata sidecar. **If the branch survives**
    (`delete_branch: false`, or the `-d` refusal path), *keep* the sidecar and
    mark it `worktree_removed: true` — the sidecar is then the only record
    tying the surviving branch to its `base_sha`, and it is what lets `prune`
    later recognize the branch as merged/unchanged residue and collect it
    (rev-5 review finding: without this, every default `remove` manufactures
    an untracked orphan branch — the exact leak class the Summary cites).
11. Return confirmation with the removed path and whether the branch was
    deleted.

### `prune`

One call that does what ninety manual `remove`s never happen to do. Three
sweeps, all through the control env:

1. **Registered managed worktrees** (enumerated as in `list`): remove a
   worktree (dir + branch + sidecar) iff **all** hold:
   - not locked (the occupancy rule — protects the current session, other
     sessions, and live delegates without any creator comparison; a *dead*
     session's worktree is normally unlocked and therefore collectible,
     fixing rev-5's starvation finding where creator-mismatch skips made
     prior-session leftovers uncollectable forever);
   - no live work under it per this session's `liveWorkUnder` (belt and
     braces alongside the lock);
   - clean per `git -C <path> status --porcelain=v1 --untracked-files=all`;
   - **disposable**: either `unchanged` (HEAD equals the recorded base SHA —
     nothing was ever committed) or **merged** (branch tip is an ancestor of
     the main root's HEAD). Unchanged branches are deleted with `-d` falling
     back to `-D` (a tip equal to base can be unreachable from any upstream,
     which `-d` refuses on a technicality); merged branches always satisfy
     `-d`.
   Sidecar-less worktrees inside the managed dir are skipped (provenance
   unknown → not ours to judge).
2. **Sidecar reconciliation** (rev-5 review finding — residue is otherwise
   invisible once the worktree is gone): for every sidecar under
   `<projectid>/.meta/` with no matching registered worktree:
   - branch gone too → stale sidecar; delete it (covers the crash window
     between sidecar write and `worktree add`, and out-of-band deletions);
   - branch exists and is merged or tip == `base_sha` → delete the branch
     (`-d`, `-D` only for the tip==base technicality) and the sidecar;
   - branch exists with unmerged commits → keep, report as branch residue.
3. **Git registry hygiene:** run repo-wide `git worktree prune` **only if**
   every `prunable`-annotated entry in the porcelain output is under the
   managed directory; if any non-managed entry is prunable, skip this step
   and report it instead (deregistering a user's temporarily-absent worktree
   is irreversible and is not this tool's call).

Report removed and skipped entries with per-entry reasons. `prune` never
takes `force`; anything it skips is `remove`'s job (or a deliberate
`git worktree unlock`), deliberately.

The `unchanged` predicate (clean tree AND tip == recorded base SHA) is shared
with delegate auto-disposal (§9). It is why metadata must record the base SHA
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
their nesting under `.meta/`). Written **before** `git worktree add` (§3
step 5); deleted when both the worktree and its branch are gone (§5 remove
step 10, prune sweeps). Never inside the worktree's working tree (would dirty
it) and never inside `.git` (not ours). Fields:

```json
{
  "name": "feature/foo",
  "branch": "feature/foo",
  "base_sha": "<resolved base commit>",
  "original_root": "/abs/path/to/main/repo",
  "creator_session": "<session id>",
  "delegate_id": "<delegate id, when delegate-created (§9), else omitted>",
  "worktree_removed": false,
  "created_at": "RFC3339"
}
```

Consumers: `remove`'s cross-session guard, `prune`'s `unchanged` predicate and
reconciliation sweep, `list`'s staleness report, resume re-entry (§7), and
delegate auto-disposal (§9). A worktree without a sidecar (hand-made inside
the managed dir, or a crash between `worktree add` and nothing — impossible
under sidecar-first ordering, but strays predate the feature) is listed with
`"unmanaged_meta": true`, skipped by `prune`, and treated by `remove` as
another session's (refuse without `force`).

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
  launch-workdir field (§5 remove step 4).

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
   origin URL) — **and pre-warm the new env's git-root cache by calling
   `execenv.GitRootOrEmpty(newEnv, newWorkingDir)` here**. This last call is
   load-bearing, not an optimization (rev-5 review finding, both reviewers):
   `refreshSystemPromptCache` is an *eager render* whose `renderSystemPrompt`
   calls `GitRootOrEmpty` (`session_prompts.go:28-30, 141`), and the freshly
   swapped env's memoization cache is empty (`local.go:126`) — without the
   pre-warm, step 2 would fork `git rev-parse` while holding `s.mu`, exactly
   what this step exists to prevent. The git snapshot forks several `git`
   subprocesses and `status` can take seconds on a big repo; **`s.mu` must not
   be held across any of them** — holding it would stall every event emit,
   `Meta()` autosave, and hub poll. `manage_worktree` is non-read-only and
   serialized in the tool stream, so nothing else can be swapping the env
   concurrently; computing against the new env before publishing it is safe.
2. **Under `s.mu`, atomically:** assign `s.env` and `s.envInfo`, then
   `s.rebuildToolDefsCache()` and `s.refreshSystemPromptCache()` (which now
   hits the pre-warmed cache and renders without forking).

The cache steps mirror `SetModel`'s pattern (`session.go:560-561`) — but note
as a caution, not a license: `SetModel` calls `refreshSystemPromptCache` under
`s.mu` too, and only avoids forking because init already warmed the *same*
env's cache. An env swap replaces the env, so it does not inherit that warmth;
hence the explicit pre-warm above.

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

- Persist worktree state in `schema.SessionMeta`: the active worktree path —
  **managed or path-entered; both `switch` modes swap the env, so both must
  survive resume** (rev-5 review finding: persisting only the managed path
  silently reverts by-path sessions) — plus a `managed` flag and the restore
  root (the original env's `RootDir`). Written by the same meta-save that
  already persists `EnvInfo`.
- On `RestoreSessionFromMeta*`: if meta records an active worktree, the path
  still exists, and it is still a registered worktree of the repo (validated
  as in `switch` by path), re-enter it **before** `initSessionState` runs —
  root the session env at the worktree, set the saved restore env from the
  recorded restore root, and (managed only) re-take the occupancy lock with
  the resumed session's marker. Init's normal `envInfoFromEnv` snapshot then
  sees the right directory; no special refresh needed.
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
- `base_ref` fails validation or cannot resolve to a commit **from the active
  root** → error before `git worktree add`.
- `name` already exists as a branch or worktree → `create` errors (suggest
  `switch` only when the managed worktree exists).
- `switch`/`remove` to a nonexistent worktree → error.
- `switch` by `path` to a path not in `git worktree list` → error.
- `switch`/`remove` on a worktree locked by another session/delegate → error
  naming the lock reason; `force` does not override locks.
- `remove` target resolves outside the managed worktree directory → error.
- `exit` when not in a worktree → clear non-destructive error.
- `remove` on a dirty worktree without `force` → error listing the dirty
  files, without changing the session env.
- `remove` with `delete_branch` on an unmerged branch without `force` →
  worktree removed, branch deletion refused with git's unmerged-tip message,
  sidecar retained as branch-residue record.
- `remove` when live children/delegates/shell jobs are rooted under the target
  worktree → error (live work guard).
- `remove` of a worktree created by another session without `force` → error
  naming the creator.
- `remove` of the active worktree with no safe restore env → error.
- `prune` → never errors on skips; reports per-entry skip reasons (locked +
  reason, dirty, unmerged, sidecar-less, non-managed prunable entry).
- Non-local execution environment → `manage_worktree` errors clearly.
- `git` unavailable → `ResolveMainRepoRoot` uses the structural `.git`-pointer
  path (no git binary needed for the common linked-worktree case);
  `create`/`switch`/`exit`/`remove`/`list`/`prune` require git for lifecycle
  operations and error clearly if absent.

## 9. Delegate worktree isolation

The dominant real-world worktree use is not a session entering a worktree for
itself; it is parallel delegate lanes needing isolation from each other and
from the main checkout. Manual lane management has produced exactly the
failures this feature exists to prevent (lanes writing to the wrong checkout).
This section makes isolation a property of the *delegation*, not a behavior
the child must remember to perform. It ships in the same delivery as the rest
of this spec.

**The lifecycle unit is the delegate, not the job.** Serf delegates are
durable multi-job sessions: `delegate` returns a durable `delegate_id`, and
`delegate_send(to=<delegate_id>, on_idle="start")` starts further jobs on the
same retained (or restored) child, whose env — and persisted restore
descriptor `WorkingDir` — must keep pointing at a directory that exists
(`agent/internal/tool/definitions.go:113-169`; `job_delegate.go:444-487,
841-861`). Rev 5 disposed of the worktree at *job* completion, which either
deletes the root out from under an idle-but-restorable child or (if the idle
child counts as live work) never fires at all — the competing reviewers'
shared top finding. The worktree therefore lives exactly as long as the
delegate does.

### Tool surface

`delegate` gains an optional `isolation: "worktree"` argument (absent →
today's behavior). Only valid for local execution environments; errors
clearly otherwise. (`delegate` has no working-directory argument today —
`prepareSubagentRun` is called with a hardcoded `""` at `job_delegate.go:187`
— so there is nothing to be mutually exclusive with; if such an argument is
ever added, it and `isolation` must be mutually exclusive.)

### Lifecycle

1. **At delegate creation**, the parent-side harness (not the child): resolves
   the main root; creates a managed worktree named `<delegate_id>` (e.g.
   `dlg_01H…` — the id is regex-legal under §2's underscore-inclusive rule and
   needs no extra prefix), branched from the **parent's active HEAD** resolved
   per §2's active-root rule (`git -C <parent activeRoot> rev-parse
   --verify HEAD^{commit}`, SHA passed explicitly); writes the metadata
   sidecar with `delegate_id` and the parent's session id; **locks the
   worktree** with reason `serf:dlg:<delegate_id>:<parent-session-id>`; and
   roots the child env at the worktree via `WithWorkingDirectory`. The child's
   restore descriptor `WorkingDir` is the worktree path, so delegate resume
   works through the existing machinery unchanged. All jobs sent to this
   delegate run in this same worktree.
2. **The child cannot stray**: its `RootDir` is the worktree leaf; file tools
   are confined there and `ExecCommand` rejects working dirs outside it.
   `manage_worktree` is **denied by name** for isolation children via the
   existing name-granular tool policy (`allowedToolNames`/`deniedToolNames`,
   `subagents.go:492-496`; `restoredDelegateAllowedTools`,
   `job_delegate.go:892-900`) — the existing plumbing cannot express
   per-operation gating, and granting the tool would let the child swap its
   own env, defeating "structurally impossible" (rev-5 review finding). The
   child can still run read-only `git worktree list` through its shell if it
   is curious.
3. **Per-job reporting:** every terminal job result from an isolated delegate
   carries the worktree path, branch, commits-ahead-of-base count, and dirty
   state, so the parent can merge a lane's commits (from the main root or via
   `switch`) at any point between jobs without guessing where the lane lives.
4. **Disposal at parent-session close** (after `DrainJobTree` has quiesced the
   delegate subtree, so children are terminal and their sessions closed): for
   each isolation worktree this session created, evaluate the shared
   `unchanged` predicate (§5) through the control env:
   - **unchanged** → unlock, remove worktree + branch + sidecar silently;
   - **changed** (commits or dirty tree) → unlock and keep; the close-time
     output (one-shot result or hub close event) lists the kept lanes with
     path/branch/ahead/dirty, and `prune` collects each one once its branch is
     merged (or `remove` disposes of it explicitly).
5. **Hard parent death** (crash, SIGKILL): no disposal ran; the worktrees
   remain locked with the `serf:dlg:…` reason. `list` shows them; clearing
   the stale lock is the deliberate `git worktree unlock` act described in §5,
   after which `prune` collects the unchanged/merged ones. This is fail-safe
   in the same direction as everything else in this spec: crashes never
   destroy work.

### Guards

`remove`/`prune` refuse while the delegate's lock is held (§5), and the
live-work guard additionally refuses while any of the delegate's jobs are
running. Because the delegate holds its lock for its entire lifetime, the
parent cannot `switch` into an isolated delegate's worktree at all while the
delegate exists (§4 step 2 refuses on the foreign lock) — inspection of a
lane happens read-only from outside (shell `git -C <path> log/diff/status`
from the main root, or the per-job report). After close-time disposal keeps a
changed lane, it is unlocked and `switch`-able like any managed worktree.

## 10. Testing

Per `AGENTS.md`: no network, no provider credentials, deterministic. The
worktree tool is pure plumbing — git operations on temp repos. Tests use real
`git` (a build tool, always available in CI) on temp directories:

- `create` → verify linked worktree exists, `.git` is a pointer file, `s.env`
  swapped, git snapshot fields recomputed, system prompt refreshed, metadata
  sidecar written **before** the worktree (crash-window ordering), worktree
  locked with the session marker, base SHA recorded and passed explicitly.
- `create` from inside a worktree → base resolves to the **active worktree's
  HEAD** (distinct from the main root's HEAD in the fixture), via
  `git -C <activeRoot>`; same for a user-supplied `base_ref` of `HEAD`.
- `switch` between two worktrees → verify env points at the right one, envInfo
  refreshed, saved restore env untouched, single swap (no intermediate
  restore), lock moved (old unlocked, new locked).
- `switch` by `path` → registered non-managed worktree accepted; unregistered
  path rejected; symlinked path spelling accepted (canonicalization); no lock
  mutation on the non-managed target.
- `switch`/`remove` on a worktree locked by a foreign marker → refused, reason
  surfaced, `force` does not override.
- `exit` → env restored to original root, saved env cleared, worktree
  unlocked, branch and sidecar intact; `exit` outside a worktree errors
  without side effects; create→exit→switch round-trip returns to the same
  worktree and re-locks it.
- Same-response ordering: read-only call before `manage_worktree` sees old env;
  read-only call after it sees the new env.
- `remove` clean and dirty (with/without `force`, with/without
  `delete_branch`).
- `remove` with `delete_branch` on a branch with unmerged commits → branch
  survives (`-d` refusal surfaced) without `force`; deleted with `force`;
  sidecar retained with `worktree_removed: true` whenever the branch survives.
- Dirty remove without force leaves `s.env` unchanged and lists the files.
- `remove`-current with no safe restore env → verify refusal.
- `remove` of a worktree whose metadata names another creator session →
  refusal without `force`.
- `remove` while live subagents/delegates/shell jobs have working dirs under
  the target → verify refusal, including the case where the parent has already
  switched elsewhere, and including a background shell job (exercises the new
  launch-workdir field on job records).
- `prune` → removes unchanged and merged-clean worktrees (branches and
  metadata included); skips locked (foreign and stale alike), live-work,
  dirty/unmerged, and sidecar-less entries — each with a reason; collects an
  *unlocked* worktree whose creator session no longer exists (the dead-session
  case rev 5 starved); reconciliation sweep deletes stale sidecars (no
  worktree, no branch), collects merged branch-residue from prior
  `delete_branch: false` removals, and keeps unmerged residue with a report;
  repo-wide `git worktree prune` runs only when no non-managed entry is
  prunable (fixture: a deregistrable non-managed worktree must survive a
  serf `prune`).
- `list` returns expected entries with staleness fields (age, dirty, ahead,
  merged, creator, lock state, prunable annotation); **does not** run
  `git worktree prune` (fixture: a momentarily-absent non-managed worktree
  stays registered after `list`); non-serf worktrees excluded;
  prefix-collision filtering correct; symlinked worktreeRoot still matches
  (canonicalization).
- Resume: persist meta with an active managed worktree → `RestoreSessionFromMeta*`
  re-enters it (env rooted at the worktree, restore root recorded, lock
  re-taken with the resumed session's marker); same for a **path-entered**
  non-managed worktree (no lock taken); worktree deleted between runs → resume
  lands at the restore root with a notice.
- `ResolveMainRepoRoot` on a linked worktree returns the main root, not the
  worktree dir; handles the absolute `--git-common-dir` output from inside a
  worktree (no bogus `filepath.Join`); **submodule** falls through to the
  candidate sanity-check and returns the submodule working-tree root (not
  `<super>/.git/modules`), and two submodules of one superproject get distinct
  projectids; works when the session was launched in a repo *subdirectory*
  (structural walk must not be confined by the env root) and without the git
  binary for the standard pointer-file case.
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
  over-length names; **accepts underscores** (`dlg_01H…`, `my_feature`);
  rejects git-invalid refnames caught by `git check-ref-format`; allows
  `feature/foo`.
- `base_ref` validation rejects whitespace/control chars, leading `-`, and
  nonexistent refs; accepts a local branch, tag, remote-tracking ref, and SHA.
- Data-race: `-race` run of create/switch/exit/remove with concurrent read-only
  tool dispatch, child creation, delegate restore env creation, status events,
  and Close cleanup passes.
- Lock discipline: the env-swap helper computes the git snapshot **and the
  `GitRootOrEmpty` pre-warm** outside `s.mu`; assert no `git` invocation
  happens while the lock is held (a PATH-shimmed `git` that records whether
  `s.mu` is held via a test hook, or at minimum a code-review checklist item
  plus the race test above). Include the first post-swap system-prompt render
  in the assertion window (the rev-5 gap was precisely there).
- Delegate isolation: delegate with `isolation: "worktree"` → child env rooted
  at a fresh managed worktree named `<delegate_id>` with `delegate_id`
  metadata and a `serf:dlg:` lock; a **second job via `delegate_send`** runs
  in the same, still-existing worktree; every job result carries
  path/branch/ahead/dirty; parent close after drain → unchanged lane
  auto-removed (worktree, branch, sidecar, lock all gone), changed lane
  unlocked + kept + reported; killed/crashed parent → worktree kept and still
  locked; parent `remove`/`prune` of a live delegate's worktree → refused;
  `manage_worktree` absent from the isolated child's advertised tools;
  delegate resume lands back in its worktree via the existing restore
  descriptor.
- Fuzz target for arg validation (extends `FuzzToolArgsValidate` table).

## 11. Known limitations

- **Bare repos:** `ResolveMainRepoRoot`'s structural walk-up from
  `bare.git/worktrees/<n>` yields `bare.git`'s parent, not `bare.git` itself;
  `--git-common-dir` returns `bare.git` (correct). Documented best-effort; a
  future spec can add bare-repo detection (R2 MINOR #7).
- **Submodules:** the fallback's sanity check (§1 step 5) returns the
  submodule's working-tree root — submodule-as-own-project identity. **Linked
  worktrees of a submodule** are unsupported: there `--show-toplevel` returns
  the worktree dir and the structural pointer walks into `.git/modules/…`;
  the resolver returns the worktree dir as best effort, so managed worktrees
  created from inside one key off the wrong identity. Niche-of-niche;
  documented.
- **Stale occupancy locks:** a session or delegate that dies hard while
  rooted in a managed worktree leaves its `git worktree lock` in place. This
  is deliberate (crashes must never unlock → destroy work), but it means
  `prune` skips those worktrees until a human or agent verifies the owner is
  dead and runs `git worktree unlock`. The lock reason names the owner to make
  that verification tractable; a future liveness registry could automate it.
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
projectid case-sensitivity claim; and delegate worktree isolation.
Placement stayed in the state dir (decided against in-repo `.serf/worktrees/`:
immunity to `git clean -dxf` outweighs path legibility, and the run/serve
keying fix removes the discovery-instability argument).

Rev 5 removed the phasing: delegate worktree isolation (§9) is integrated
into the single delivery rather than staged as a follow-on phase — one spec,
one implementation campaign.

Rev 6 followed a second two-reviewer competitive adversarial round against
rev 5 (both reviewers code- and git-verified their claims; 15 distinct
legitimate findings after dedup, zero fabrications; Reviewer B won on count,
12 vs 8, while Reviewer A landed the sole BLOCKER and three unique majors).
Fixes: **base/HEAD resolution always via the active root as an explicit SHA**
(both reviewers: the control env would have branched lane-off-a-lane from the
main root's HEAD); **delegate disposal moved from job end to parent-session
close** (shared top finding: delegates are durable multi-job sessions —
per-job disposal deletes the root under an idle-but-restorable child, or
never fires); **`GitRootOrEmpty` pre-warm before the locked swap** (both: the
eager prompt render would have forked git under `s.mu`, and the `SetModel`
justification was wrong in the cold-cache case); **git-native occupancy locks**
(unifying four findings: prune's same-creator occupant deletion, prune's
dead-session starvation, delegate-worktree occupancy, and unhandled locked
worktrees); **underscore admitted in names** (the regex rejected serf's own
`dlg_`/`job_` ids); **sidecar-first creation ordering + `worktree_removed`
retention + prune reconciliation sweep** (branch/sidecar residue was invisible
once the worktree was gone); **`list` no longer runs `git worktree prune`**
(it irreversibly deregistered any momentarily-absent worktree, including
non-managed ones; the guarded variant lives in `prune` only); **resume covers
path-entered worktrees** (only managed ones persisted before); **submodule
fallback corrected** (parent of `--git-common-dir` in a submodule is
`<super>/.git/modules` — not a working tree; §11's old text described behavior
the algorithm didn't produce); **absolute `--git-common-dir` output handled**
(`filepath.Join` only when relative); **the phantom `working_dir` exclusivity
clause removed** (`delegate` has no such parameter); and **isolation children
deny `manage_worktree` by name** (the existing tool policy is name-granular;
per-operation gating would be new plumbing).
