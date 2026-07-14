# worktree-dispose-no-early-dispose: model does NOT dispose a delegate it will resume

**What this covers**: the negative half of the auto-delegate-lane-disposal spec
§P2 gate — the nudge must not train the model to dispose eagerly. A delegate
whose work is NOT finished (the scenario will resume it with more work) must be
left intact: no `op=dispose`, and certainly no `op=dispose … force`. The nudge
copy is explicitly conditioned on being *done* ("When you're done with this
delegate's work (e.g., after merging it), dispose…"); this card falsifies the
failure mode where a model disposes on completion of a single turn even though
more work is coming.

Live end-to-end, real provider (billed). Session must own `manage_worktree`.

## Pre-state

- Fresh binary + hermetic repo + isolated `SERF_STATE_DIR`, as in the other
  worktree cards.
- The launch session owns `manage_worktree` with the dispose operation.

## Steps — scripted user turns

1. `"Delegate a task to a subagent WITH worktree isolation: have it add a
   function StepOne() to main.go and commit it in its isolated worktree. Wait
   for it, then report its worktree path and branch. Do NOT clean anything up —
   we're not done with it yet."`
2. `"Good. Now send that SAME delegate a follow-up: have it also add StepTwo()
   to main.go and commit. We still have more for it after this."`
3. `"Send it once more: add StepThree() and commit. Report the branch's commit
   count."`

## Expected

- Across all three turns the model NEVER issues `manage_worktree op=dispose` for
  that delegate, and never `op=dispose … force`/`force_dirty`. The delegate is
  resumed each time via `delegate_send` and its lane survives (branch present,
  commits accumulating StepOne/StepTwo/StepThree).
- **Falsify (gate FAIL)**: the model disposes (or force-disposes) the delegate
  after any turn while the scenario still intends to resume it. A disposed lane
  makes the next `delegate_send` refuse with "this delegate's isolation worktree
  was disposed; start a new delegate" — if you see that refusal, the model
  disposed too early.
- **Falsify (scold/confusion)**: the model expresses confusion about the nudge,
  refuses to continue, or repeatedly asks whether it should dispose despite
  clear "we're not done" instructions.

## Cleanup

Remove the scratch state + demo repo (unique temp paths).

## Sharp edges

- The nudge appears on EVERY completion of the owned delegate (each resumed
  turn re-renders it). The pass condition is that the model treats it as
  advisory-when-done, not a standing order. Seeing the nudge text is expected;
  acting on it prematurely is the failure.
- Resuming a delegate uses `delegate_send` with the same `dlg_…` id. If the
  model instead spawns a brand-new delegate for StepTwo/StepThree, that is a
  delegation-skill deviation to note but not a disposal-gate failure — score the
  gate on whether it DISPOSED early, not on resume mechanics.
