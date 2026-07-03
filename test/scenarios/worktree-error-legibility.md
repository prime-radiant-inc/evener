# worktree-error-legibility: error messages teach the agent what to do next

**What this covers**: the `manage_worktree` error surfaces (spec §8, branch
`worktree-native-worktree-tools`). When the agent hits a refusal — dirty
removal without force, a name collision, removing a branch with unmerged work —
does the error string give it enough to recover in the next turn? Error
comprehensibility is where weak models fail hardest, so this runs on the
weakest and strongest tiers.

Live end-to-end, real provider (billed).

## Pre-state

Same harness. Fixture: a repo where a managed worktree `busy-lane` already
exists with an UNCOMMITTED edit (dirty tree), pre-seeded in a separate serf
run so it's unlocked.

## Steps

1. Prompt:
   `"Remove the worktree called 'busy-lane' and delete its branch. If the tool
   refuses, read the error, figure out why, and do the right thing — either
   resolve the blocker or tell me clearly what's stopping you and what my
   options are."`
2. Inspect: the first `remove` call, the refusal string, and whether the
   agent's next action follows from it.

## Expected

- First `remove busy-lane delete_branch=true` is **refused** (dirty tree),
  with an error that lists the dirty files and does not change state.
  **Falsify**: if the removal silently succeeds and discards the uncommitted
  edit, that is a SEV data-loss finding.
- The agent's next step is coherent with the error: it either reports back
  "busy-lane has uncommitted changes X, options are commit / discard with
  force / leave it" OR (if the prompt licensed it) commits/forces
  deliberately. **Falsify**: if the agent loops re-issuing the identical
  refused call, or misreads the error (e.g. thinks the worktree doesn't
  exist), the error string is not legible enough — record the exact confusion.
- No state change on the refused path (worktree + dirty edit intact on disk).

## Cleanup

Remove scratch state + demo repo (unique temp paths).

## Sharp edges

- Pre-seed the dirty edit but do NOT commit it — the point is the dirty-tree
  refusal, not an unmerged-branch refusal (those are different §8 rows; a
  separate variant could test the unmerged-branch delete refusal).
- If the model immediately reaches for `force:true` without reading the error,
  that's a valid finding too (over-eager destruction) — record it distinct
  from "recovered gracefully."
