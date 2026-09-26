# Delegate lane branch names

Status: proposed. Companion amendment to the native worktree tools design
(`docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md`), §9.

## Problem

A worktree-isolated delegate's lane is invisible in `git branch`. The branch, the
worktree directory, the sidecar filename, and every message use the opaque delegate
id, so a human merging a lane types `git merge dlg_01JXYZABCD0123456789ABCDEF`.
Neither model can influence the name. The `delegate` tool has no name argument, and
the child is denied `manage_worktree`, so the runtime's choice is the only choice:
`ReserveCreate` mints the id and joins the lane path from it
(`agent/delegate_tree_start.go:139-152`), and `createDelegateWorktree` passes it as
the single `name` argument to the create core
(`agent/session_tools_worktree.go:1338-1346`).

Decision already made with Jesse: the parent model names the lane at spawn time.
This document is the simplest design that gets there.

## Design

`delegate` gains an optional `name` argument that names the lane's **git
branch** only (amended 2026-09-22, delegate name labels: `name` is now accepted
for every delegate as a display label — see the tool-schema note below). Every
machine identity stays keyed to the delegate id: the worktree directory, the
sidecar filename, the lock marker, and dispose addressing. The branch is the
one surface a human reads, and it is the one place a mnemonic buys anything.

Three facts make this the simplest possible cut:

1. The branch is already recorded where consumers need it. The create core writes
   `sc.Branch` at create time (`session_tools_worktree.go:1193`), and the per-job
   worktree report already reads `sidecar.Branch` (`agent/job_delegate.go:259`).
   Reporting a branch that differs from the lane name already works.
2. The lane directory sits under `<stateDir>/worktrees/<projectID>/<name>`, a
   state path nobody browses. A mnemonic directory would cost code and buy
   nothing.
3. Absent `name`, the branch is the delegate id, exactly today's behavior. Every
   existing lane, test, and caller keeps its meaning.

## The changes

**Tool schema** (`agent/internal/tool/definitions.go`): optional `name` on
`delegate`. As originally proposed, the name was refused without
`isolation:"worktree"`; amended later the same day (2026-09-22, delegate name
labels), `name` is accepted for every delegate. With `isolation:"worktree"` it
becomes the lane's branch as this section describes — refused if invalid or if
the branch already exists, and an absent name branches with the delegate id.
Without isolation it is a display-only label carried on the delegate
descriptor and surfaced in listings and notifications. The lane directory and
all addressing keep the delegate id either way; current semantics live in
`docs/developing-evener/worktrees.md` ("Delegate worktree isolation").

**Dispatch** (`agent/job_delegate.go`): parse into `delegateArgs.Name`, validate
with `worktree.ValidateName`, and fail fast with `invalid_request` before
reserving capacity. A slash is legal (`feat/parser` is a valid ref); consumers
resolve the branch from the sidecar record, never by reconstruction. The create
core re-validates with `check-ref-format --branch` and `branchExists`, exactly as
it does for a session's own lane.

**Descriptor** (`agent/internal/delegatestore/record.go`): `WorktreeBranch
string` with `json:"worktree_branch,omitempty"`. Additive and omitempty, so
restore and fold/replay fixtures are unaffected. The field rides the existing
`Isolation` plumbing to `prepareIsolation`.

**Create core** (`agent/session_tools_worktree.go`): `worktreeCreateCore` gains a
`branch` parameter. It validates the branch as it validates the name today, cuts
the lane with `worktree add -b branch`, and records `sc.Branch` and the result's
`Branch` from it. `worktreeCreate` passes `branch == name`;
`createDelegateWorktree` passes `descriptor.WorktreeBranch`, defaulting to the
delegate id.

**Every branch-acting site resolves the branch from the sidecar.** These
functions currently assume the branch equals the lane name. Each one already
holds the sidecar at the point it acts, and each switches to one resolution
rule: `Sidecar.BranchOrName()` in `agent/internal/worktree`, the package that
owns the sidecar format and already holds the other pure decision cores
(`Decide`, `Adopted`, `ValidateName`). It returns `sc.Branch`, falling back to
`sc.Name` when a sidecar records no branch, so the rule is defined once and
tested with the package that owns it:

- the rollback path: `rollbackFreshDelegateWorktree`, whose primitive already
  takes the branch and the sidecar name as separate arguments
  (`session_tools_worktree.go:1395`); `delegateIsolation` carries the branch so
  `cleanup` can thread it (`agent/delegate_runtime.go:2075`)
- `worktreeDispose`, every arm: `session_tools_worktree_dispose.go:241` (the
  half-removed `branch -D`), `:290` (the half-removed tip resolution),
  `:311-314` (the remnants cleanup), plus the result's `Branch` fields
- `disposeUnchangedLaneMechanics`, the close-time disposal
  (`session_worktree_close.go:427`); `isolationLane` gains a `Branch` field so
  its sites read the branch off the lane record
- `worktreeRemove` steps 9 and 10 (`session_tools_worktree.go:2470, 2496, 2521`)
- the prune path: `collectLane` (`session_tools_worktree.go:3033`) and
  `worktreePruneSweep2` (`:3112, :3124, :3169-3171`). The `branchExists` gate at
  `:3112` matters most: it would otherwise misread a mnemonic-branch orphan lane
  as having no branch, delete the sidecar, and strand the branch behind it.

The rule lives in one place; the calls cannot. The six surfaces act on a lane
branch with deliberately different error semantics: dispose's half-removed arm
warns and continues, `collectLane` returns an error that prune aborts on and the
close-time eviction treats as a lost race, sweep 2 records a skip reason, and
remove reports `BranchKeptReason` on an otherwise successful result. A shared
delete operation would have to flatten those semantics or grow options flags,
and the shared primitives (`branchExists`, `checkoutLocationOf`) already take a
branch string, so each site changes only which string it passes. Go cannot make
the name unusable at the type level; the backstop is the canary test below.

Where the sidecar is unreadable (dispose's already-disposed reporting arms), the
branch is unknown and nothing destructive follows; those paths keep the id in the
report, which already carries the unreadable-sidecar caveat.

**Prompt** (`agent/prompts/sections/delegation.md`): one sentence after the
isolation guidance: give a worktree-isolated delegate a short kebab-case `name`,
like `parser-rename`, so `git branch` and merges read clearly.

**Spec §9**: step 1 keeps "named `<delegate_id>`" for the worktree and adds that
its branch is the optional `name` argument, defaulting to the delegate id.
`docs/developing-evener/worktrees.md` gains the matching sentence.

## Collisions

Refuse on collision. The core's existing `branchExists` check runs before the
sidecar write, so a refused spawn leaves no residue, and the error names the
branch. The parent retries with a different name or omits it. Lane branches share
`refs/heads/` with the user's own branches, so refusing also protects names like
`main`: the spawn fails loudly instead of shadowing a real branch. No
auto-suffixing; a silently mutated name is worse than a refused one.

## What deliberately does not change

The directory name, the sidecar filename, the lock marker, remove's cascade check
(`name == delegate id` still holds for the directory), the reserve-time path
join, per-job reporting (already `sidecar.Branch`), `manage_worktree list`
(porcelain reports the real branch already), and dispose addressing by the
`dlg_…` id.

## Rejected alternatives

- **Mnemonic directory too.** Costs the reserve-time join, the sidecar filename,
  the cascade check, and a directory-namespace collision policy. Buys legible
  paths under a state directory nobody browses.
- **Runtime-derived slug from the task title.** Needs no model cooperation but
  yields weaker names, and every code change except the schema entry is
  identical.
- **Child names its own lane.** The child holds no lane-management authority by
  design.
- **Post-hoc `git branch -m`.** Breaks dispose and prune, which delete the branch
  by its recorded name.

## Tests

Red first, per TDD:

1. Dispatch: an invalid `name` returns `invalid_request`; a valid `name`
   reaches the descriptor. As originally proposed, a `name` without
   `isolation:"worktree"` was also refused; amended 2026-09-22 (delegate name
   labels), it is accepted as a display label.
2. Create core: a branch that differs from the name cuts the lane on that branch
   (including a slash name such as `feat/parser`), and `sc.Branch` and the result
   record it.
3. Collision: a pre-existing branch refuses the spawn with zero residue.
4. Default: an absent `name` leaves the branch equal to the delegate id, pinning
   today's behavior.
5. Every disposal surface deletes the mnemonic branch: dispose in each arm,
   close-time disposal, `worktreeRemove` steps 9-10, and the prune path. An
   empty-Branch sidecar falls back to the name.
6. A failed-spawn rollback deletes the mnemonic branch.
7. The terminal job report carries the mnemonic branch.
8. Scripted end-to-end: spawn a named delegate, merge its lane by that name from
   the main root.
9. A lifecycle canary drives one divergent lane (`Branch` differs from `Name`)
   through every surface: a failed-spawn rollback, close-time disposal, dispose in
   each arm, remove steps 9-10, and both prune sweeps. A surface that regresses to
   the name fails behaviorally, which is the only enforcement available for a
   rule the Go compiler cannot check.

## Size

About 150 production lines across nine files
(`definitions.go`, `job_delegate.go`, `record.go`,
`session_tools_worktree.go`, `delegate_runtime.go`,
`session_tools_worktree_dispose.go`, `session_worktree_close.go`,
`internal/worktree/sidecar.go`, `delegation.md`), comparable test additions,
and two one-paragraph doc amendments.
