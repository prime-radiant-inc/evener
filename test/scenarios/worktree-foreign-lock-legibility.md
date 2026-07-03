# worktree-foreign-lock-legibility: a foreign-locked lane refuses legibly

**What this covers**: the occupancy-lock refusal surface (spec §5, branch
`worktree-native-worktree-tools`) — when a lane is locked by *another* live
session, `switch`/`remove` must refuse with a message that names the owner and
recovery path, and the agent must read it and not thrash. Unit-tested only;
this is the model-facing legibility check for the concurrency guard.

Live end-to-end, real provider (billed).

## Pre-state

Fresh binary + hermetic repo + isolated `SERF_STATE_DIR` (config symlinked).
Pre-seed a lane `held-lane` that is **locked by a different session id**: the
simplest deterministic way is `git worktree add` the lane under the managed
`<worktreeRoot>/<projectid>/` dir, write a matching sidecar, then
`git worktree lock --reason "serf:01OTHERSESSION..." -- <lanepath>` so it reads
as foreign-owned. (This mirrors "another live session holds it.")

## Steps

1. Prompt: `"Switch into the worktree called held-lane and make a change. If
   the tool won't let you, read the error and tell me exactly why and what my
   options are — do not repeat the same failing call."`
2. Inspect: the `switch` (and/or `remove`) call, the refusal string, and the
   agent's follow-up.

## Expected

- The `switch name=held-lane` (or `remove`) is refused with a message naming
  the lock/owner (`locked serf:01OTHERSESSION…`) and stating force does not
  override a lock. **Falsify**: if the agent enters or removes the
  foreign-locked lane, the concurrency guard failed (SEV — two writers).
- The agent's next action is coherent with the error: it reports the lane is
  held by another session and does NOT loop the identical failing call.
  **Falsify**: if it re-issues the same refused call two+ times, or misreads
  the lock as "worktree not found," the refusal isn't legible enough — record
  the confusion verbatim.

## Cleanup

Remove the scratch state + demo repo (unique temp paths). (Unlock is
unnecessary — the whole tree is discarded.)

## Sharp edges

- The foreign lock reason must parse as a serf marker with a DIFFERENT session
  id than the running session, or it'll read as this session's own crash
  residue (auto-released) rather than foreign. Use an obviously-different id.
- `force` must NOT override the lock (spec §5); if the model reaches for
  `force:true` and it's still refused, that's the correct, testable behavior —
  note whether the agent understood force doesn't apply here.
