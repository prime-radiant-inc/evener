# Worktrees

Status: Evergreen guide to the shipped `manage_worktree` tool and delegate worktree isolation.

## Summary

A worktree is a second checkout of the same repository, on its own branch, sharing
history with the main checkout but with its own working files. Serf gives agents a
single tool, `manage_worktree`, to create, enter, leave, inspect, and clean up
worktrees — instead of the agent hand-rolling `git worktree add` and `cd`. The same
mechanism backs **delegate worktree isolation**: a delegate spawned with
`isolation: "worktree"` gets its own lane automatically, so parallel delegate fan-outs
can no longer write to the wrong checkout.

Worktrees are for isolated, parallel, or risky work — a delegate lane, a spike you
might throw away, a branch you want to build in without disturbing the session's
current checkout. They are not a replacement for ordinary `git checkout`/`git branch`;
plain git commands are the right tool for routine branch switching.

Managed worktrees live outside the project tree, under the session's runtime state
directory, keyed by the repository's identity. That placement means they never show
up in `git status` in the main checkout, need no `.gitignore` entries, and survive a
`git clean -dxf` run there.

## The `manage_worktree` tool

One tool, seven operations, selected by `operation`:

| Operation | What it does | Key arguments |
| --- | --- | --- |
| `create` | Creates a new worktree on a new branch and enters it — subsequent shell/file/grep calls operate inside it. | `name` (required — doubles as the branch name and the worktree's directory name), `base_ref` (optional; defaults to the current branch tip) |
| `switch` | Enters an existing worktree without creating one. | `name` (a worktree this tool manages) or `path` (any worktree git already knows about, including hand-made ones) |
| `exit` | Leaves the current worktree and returns to where the session was before, without touching the worktree itself. | none |
| `list` | Reports every managed worktree: path, branch, lock/occupant state, and how stale it is (age, dirty, commits ahead, merged). | none |
| `remove` | Deletes a worktree (and, optionally, its branch). | `name` (required), `force` (skip the dirty-tree safety check), `delete_branch` (also delete the branch, if it's safe to) |
| `prune` | Sweeps every managed worktree in one call and removes the ones that are safe to remove. | none |
| `dispose` | Retires one finished delegate isolation lane by its delegate id — unlocks it, removes the worktree, deletes its branch, and marks the delegate disposed. | `id` (required — the `dlg_…` delegate id), `force` (discard unmerged commits), `force_dirty` (discard uncommitted changes) |

Because entering or leaving a worktree changes where every later tool call runs,
`create`/`switch`/`exit`/`remove` are ordered against other tool calls in the same
turn: a read-only call that appears *before* `manage_worktree` in that turn still sees
the old location; everything after it, and every later turn, sees the new one.

`exit` is what makes a "peek at the main checkout, then come back" workflow possible.
Without it, a session that entered a worktree would have no way to read anything
outside it — worktree file access is confined to the worktree itself. The typical
loop is: `create` → work → commit → `exit` → review/merge from the main checkout →
`remove`.

## Lock semantics

While a session (or an isolated delegate) is sitting inside a managed worktree, it
holds a lock on it. The lock is git's own `git worktree lock`, not a bespoke serf
registry, so it's enforced everywhere — another session's `switch` or `remove`
against a locked worktree is refused, and a bare `git worktree remove` run by a human
would refuse too. `list` shows who's locked into what.

What this means day to day:

- You can't accidentally `switch` into (or `remove`) a worktree someone else — another
  session, or a live delegate lane — is actively working in. The tool tells you the
  lock reason (which session or delegate holds it) so you know who to ask.
- Leaving a worktree (`exit`, switching away, or ending the session cleanly) releases
  the lock automatically.
- `force` never overrides a lock. Locks and dirty-tree safety are different
  protections: `force` skips the "are there uncommitted changes here" check on
  `remove`; unlocking someone else's worktree is a deliberate act (`git worktree
  unlock`), never a tool flag.
- If a session or delegate dies without a clean shutdown — a crash, a kill — its lock
  is left behind. This is intentional fail-safe behavior: a stale lock just means
  nobody has confirmed the work is abandoned yet. The lock names its former owner, so
  a human (or an agent that has verified the owner is really gone) can clear it with
  `git worktree unlock` and let `prune` collect it afterward. If the same session
  resumes later, it recognizes its own lock and picks the worktree back up rather than
  erroring.

## Disposal: `remove`, `prune`, and `dispose`

`remove` deletes one named worktree. Before it does, it checks that removal is safe:
uncommitted changes block it unless you pass `force`; commits that were never merged
anywhere block *branch* deletion even with `delete_branch: true`, unless you also pass
`force` (deleting the worktree and abandoning the branch — orphaned but not gone — are
different levels of destructive). If you ask it to delete the branch too, it only does
so once it can show the branch's work is actually captured elsewhere (merged, or
never diverged) — it does not trust git's own "already merged" check, because that
check is relative to whatever the main checkout happens to have checked out right now,
which can be misleading (e.g. a detached-HEAD review).

`prune` is the "clean up everything that's safe to clean up" call — the one-shot
alternative to remembering to `remove` every finished lane by hand. It walks every
managed worktree and removes the ones that are simultaneously: unlocked, not currently
in use by any live shell job or delegate under this session, clean (no uncommitted
changes), and either untouched since creation or already merged into the branch it
was created from. Everything else is reported with a reason instead of being touched —
locked, dirty, unmerged, checked out somewhere else, or too new to judge yet. `prune`
never takes `force`; anything it declines to remove is `remove`'s job, or a deliberate
unlock.

`dispose` retires one finished **delegate isolation lane** by its delegate id — the
synchronous move a live session makes the moment it finishes a lane's work, rather
than waiting for a close or a `prune`. It unlocks the lane, removes its worktree,
deletes its branch, and marks the delegate disposed, so a later `delegate_send` to
that delegate gets a clear refusal instead of reviving into a deleted checkout. Like
`remove`, it refuses a lane whose commits aren't merged (pass `force` to discard them)
or whose tree is dirty (pass `force_dirty`) — but unlike the automatic collectors
below, `dispose` *will* retire a rebase- or squash-merged lane, on the model's
judgment that the work actually landed.

**Behavior change: a session's own merged delegate lanes no longer survive its
close.** A session now disposes the isolation lanes it created whose commits are
reachable from the branch they were cut from — collecting them at its own close
instead of leaving them for a later `prune`. Every such commit stays reachable from
the merge target, so nothing is lost; but a merged lane's worktree and branch ref are
gone once its owner closes, rather than lingering for post-close inspection. Lanes
that are unmerged, only rebase/squash-merged, or dirty are still kept (see *Delegate
worktree isolation* below).

Two safety rules worth knowing:

- **Dirty or unmerged worktrees are never silently destroyed.** Both `remove` (without
  `force`) and `prune` leave them alone and tell you why.
- **A branch someone kept building on is never collected**, even if serf once tracked
  it as "removed, pending merge." If you (or another agent) later check that branch
  out again and add commits, serf treats it as adopted and drops its own claim on it —
  `prune` will never delete a branch out from under work that continued after serf
  stopped tracking it.

One honest gap: if a lane's work lands via a **multi-commit squash merge**, serf can't
prove the merge happened (the squash commit is the sum of several lane commits, so
there's no single commit to match against). Such lanes are reported `unmerged` even
though the work is safely merged, and the automatic collectors leave them alone. Once
you've verified the squash landed, retire such a lane with `dispose` and `force` — the
merge check refuses it without `force`, and the `force` is your attestation that the
work is safely captured — informed by `list`'s staleness report.

## Delegate worktree isolation

Passing `isolation: "worktree"` to `delegate` gives that delegate its own worktree,
automatically, for its entire lifetime — not just its first job. The lane is named
after the delegate's own id, so it never collides with a sibling delegate or with the
main checkout, and the delegate's file/shell tools are confined to it: there is no way
for the delegate to wander back into the main checkout or into another delegate's
lane. A leaf delegate does not get `manage_worktree` itself — it can look around with
read-only git commands via its shell tool, but it can't create, switch, or remove
worktrees.

A delegate that is itself a coordinator — spawned with a delegation allowance so it
can fan out its *own* isolated sub-delegates — gets a **dispose-only** `manage_worktree`:
the single `dispose` operation, nothing else. That lets it retire its sub-delegates'
lanes as their work merges (the same automatic disposal a top-level session does at
close, but reachable mid-life), while it still can't create, switch, or remove
worktrees — including its own lane, which its parent owns and disposes.

Every job result from an isolated delegate reports the lane's path, its branch, how
many commits it's ahead of where it started, and whether it's dirty. That's enough for
the parent to merge a lane's work from the main checkout at any point between jobs,
without switching into the lane itself — in fact it can't: the delegate holds the
lock on its own lane for as long as the delegate exists, so `switch` into a live
delegate's worktree is refused.

What happens to the lane depends on whether the delegate did anything:

- **Unchanged** (no commits, no uncommitted files) — the lane is deleted automatically
  when the parent session closes.
- **Merged** (its commits are reachable from the branch it was cut from) — collected
  automatically at the parent's close: worktree removed, branch deleted, delegate
  marked disposed. This is the behavior change noted under *Disposal* above — a merged
  lane no longer lingers past close for inspection, though every commit remains
  reachable from the merge target.
- **Kept** (unmerged, only rebase/squash-merged, or dirty) — the lane is kept and
  unlocked, so it survives the parent closing and is inspectable/mergeable/`switch`-able
  afterward. Merged residue a session leaves behind — an owner that crashed, or a lane
  whose branch merged only after its owner closed — is swept automatically by later
  top-level sessions: once about ten minutes after such a session opens, and once at
  its close, collecting only lanes that are unlocked and ancestry-merged and only after
  a thirty-minute grace. Unmerged, dirty, and squash-only lanes are never swept
  automatically — retire them with `dispose` (or `remove`) once their work lands.
- If the delegate is later revived (a follow-up `delegate_send` on a kept lane), it
  re-locks its lane before resuming work in it — so a revived delegate is protected
  the same way a fresh one is. A resumed session likewise re-locks its own still-kept
  lanes at startup, so they aren't swept out from under it as foreign residue.

## Limitations

These are documented trade-offs, not bugs:

- **Multi-commit squash merges aren't auto-detected** (see above) — neither close-time
  collection nor the automatic sweeps touch such a lane; the agent disposes of it with
  an explicit `dispose force` (or `remove`) at merge time.
- **The live-work guard is best-effort.** `remove`/`prune` check for shell jobs and
  delegates whose *recorded* working directory is under the target worktree. A shell
  command that `cd`s elsewhere after it launches is invisible to that check — don't
  rely on it as a hard guarantee against removing a worktree a background process is
  still touching.
- **Two processes resuming the same session id can't be told apart.** The occupancy
  lock is keyed by session id; if the same session is somehow resumed by two processes
  at once, both look like the legitimate owner, and either one exiting will release
  the lock out from under the other. This is an existing "don't do that" case for
  serf sessions generally, not something specific to worktrees.
- **A hard crash leaves the lane locked.** There's no automatic recovery from a
  session or delegate dying without a clean shutdown — the lock stays until a human
  or agent verifies the owner is really gone and runs `git worktree unlock`, after
  which `prune` can collect it if it's otherwise safe to.
