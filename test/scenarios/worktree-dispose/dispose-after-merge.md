# worktree-dispose-after-merge: model disposes a merged delegate's lane

**What this covers**: the auto-delegate-lane-disposal spec §P2 completion nudge
(`DisposalHint`) — the primary path by which a model retires a finished
delegate's worktree. After a delegate's work is merged, the completion result
and the background notification both carry:

> When you're done with this delegate's work (e.g., after merging it), dispose
> its worktree and branch: manage_worktree op=dispose id=<dlg_…>.

This card asserts the nudge lands and the model acts on it: it runs
`manage_worktree op=dispose id=<dlg_…>` and the lane + branch + sidecar are
collected. This is the (a) arm of the §P2 eval gate (see README).

Live end-to-end, real provider (billed). Needs delegate capability (default
`serf run` at depth ≥ 1) in a session that holds `manage_worktree` (a top-level
or non-isolated coordinator session — an isolated delegate child does not get
the op and must NOT be scored here).

## Pre-state

- Fresh binary + hermetic repo + isolated `SERF_STATE_DIR` (config symlinked),
  as in the other worktree cards.
- The launch session owns `manage_worktree` with the dispose operation.

## Steps — scripted user turns

1. `"Delegate a task to a subagent WITH worktree isolation: have it add a
   function Farewell() to main.go and git-commit it inside its isolated
   worktree. Wait for it to finish, then report the delegate's worktree path and
   branch."`
2. `"Merge that delegate's branch into main with a normal (fast-forward or
   merge-commit) merge, so its commits are reachable from main. Confirm the
   merge landed."`
3. `"You're done with that delegate's work now — it's merged. Do whatever the
   completion guidance suggested for its worktree."`

## Expected

- After step 1: the completion result / notification for the delegate carries
  the disposal nudge sentence naming `op=dispose id=<dlg_…>`. **Falsify**: if no
  nudge appears for an owned, finished isolation delegate in a session that
  holds the op, the §P2 surface regressed.
- After step 3: the model issues `manage_worktree op=dispose id=<dlg_…>`; the
  result status is `disposed`; on disk the lane worktree dir is gone, the
  `<dlg_…>` branch is gone (`git branch --list 'dlg_*'`), and the sidecar is
  gone. **Pass** requires the model to actually dispose.
- **Falsify (gate miss)**: the model leaves the merged lane in place with no
  dispose call and no explanation. Merged work sitting un-disposed is the exact
  residue this feature exists to prevent.

## Cleanup

Remove the scratch state + demo repo (unique temp paths).

## Sharp edges

- The nudge is unconditional wording but a conditional surface: it only renders
  when the session HAS `manage_worktree` AND OWNS the delegate
  (`ParentSessionID == session id`). Do not score a leaf/isolated child — it
  correctly gets no nudge.
- The dispose op refuses a delegate that is still running/driving. If the model
  disposes before the delegate is terminal, that is a pre-gate refusal, not a
  §P2 failure — re-run with a clean "wait for it to finish" step 1.
- Grade dispose-after-MERGE (this card, the ancestry arm). The squash arm has
  its own card because auto-collection deliberately excludes it.
