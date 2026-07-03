# worktree-delegate-isolation: isolated delegate lanes are kept-if-changed, removed-if-not

**What this covers**: the §9 delegate-isolation path (branch
`worktree-native-worktree-tools`) — the highest-risk code in the feature
because it *auto-removes* lanes at parent-session close. A parent delegates
with `isolation: "worktree"`; the harness creates a managed lane named
`<delegate_id>`, roots the child there, and at parent close disposes the lane
**only if unchanged**, keeping (and leaving resumable) any lane with commits or
a dirty tree. This path had ZERO live coverage before this card.

Live end-to-end, real provider (billed). Needs delegate capability (default
`serf run` at depth ≥ 1).

## Pre-state

Fresh binary + hermetic repo + isolated `SERF_STATE_DIR` (config symlinked), as
in the other worktree cards.

## Steps — two runs

**Run A (changed lane must be KEPT):**
1. Prompt: `"Delegate a task to a subagent WITH worktree isolation: have it add
   a function Farewell() to main.go and git-commit it inside its isolated
   worktree. Wait for it to finish, then report the delegate's worktree path,
   its branch, how many commits it is ahead, and whether it had changes."`
2. After the run ends (parent session closes → disposal fires), inspect on-disk
   state.

**Run B (unchanged lane must be REMOVED):**
1. Prompt: `"Delegate a task to a subagent WITH worktree isolation: have it
   only READ main.go and report what the main function prints — it must make
   NO edits and NO commits. Wait for it, then report the delegate's worktree
   path and that it made no changes."`
2. After the run ends, inspect on-disk state.

## Expected

- Both: the parent uses `delegate` with `isolation: "worktree"`; a managed lane
  `<delegate_id>` is created and the child works inside it. **Falsify**: if the
  child works in the main checkout (no isolation lane created), isolation
  didn't engage.
- **Run A**: after close, the lane's branch still exists (`git branch --list`)
  and either the worktree dir survives or the branch carries the `Farewell()`
  commit. The parent's job result reported path/branch/ahead≥1/dirty. **Falsify
  (SEV data-loss)**: if the committed `Farewell()` work is gone — no branch, no
  commit anywhere — the changed lane was wrongly disposed.
- **Run B**: after close, the lane worktree dir is gone and no leftover
  `<delegate_id>` branch remains (unchanged → auto-removed). **Falsify**: if an
  unchanged lane persists, disposal didn't fire (leak).

## Cleanup

Remove the scratch state + demo repo (unique temp paths).

## Sharp edges

- Disposal runs at *parent-session close*; in a one-shot `serf run` that's when
  the run ends, so inspect on-disk state AFTER the process exits, not during.
- The delegate id is the branch/lane name (`dlg_…`); find it in the job result
  or via `git branch --list 'dlg_*'`.
- If the weak model can't reliably drive a delegate + isolation arg, run this on
  the strong tier (kimi) — the path under test is the harness's disposal logic,
  not the model's delegation skill. A model that never issues the isolation arg
  is a separate (prompting) finding, not a disposal failure.
