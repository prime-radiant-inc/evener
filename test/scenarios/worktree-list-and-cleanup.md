# worktree-list-and-cleanup: list output is legible and the agent cleans up correctly

**What this covers**: `manage_worktree list` and `prune`/`remove` (branch
`worktree-native-worktree-tools`). Given a mix of worktrees — one untouched,
one with committed work — is the `list` output comprehensible enough that the
agent correctly identifies which is disposable and cleans up *only* that one,
keeping the one with work? Tests the staleness fields and the prune-vs-remove
distinction.

Live end-to-end, real provider (billed).

## Pre-state

Same harness. Before running the agent, pre-seed two managed worktrees **as
the agent's own session would** — simplest reliable way is a first serf run
that creates them, or (deterministic) drive the tool via a scripted prompt:

1. `create untouched-lane` (leave it unchanged), then `exit`.
2. `create work-lane`, add a line to README.md and commit inside it, then
   `exit`.

Both now exist under `$SERF_STATE_DIR/worktrees/<projectid>/`, unlocked (the
session exited both).

## Steps

1. Fresh serf run, prompt:
   `"List the git worktrees that exist for this project. For each, tell me
   whether it has any work in it. Then clean up any worktree that has no
   committed work, but keep any that does. Report what you removed and what
   you kept, and why."`
2. Inspect: the `list` call + result, the agent's reasoning about which is
   disposable, and the `prune`/`remove` calls.

## Expected

- `manage_worktree list` returns structured entries; the result distinguishes
  `untouched-lane` (unchanged / 0 ahead) from `work-lane` (ahead / has
  commits). **Falsify**: if the agent cannot tell them apart from the list
  output, the staleness fields aren't legible — an ergonomics failure to
  record verbatim.
- The agent removes `untouched-lane` (via `prune` or `remove`) and **keeps**
  `work-lane`. **Falsify**: if it removes `work-lane` (data loss!) or removes
  neither, the tool misled it. Removing work-lane is a SEV finding.
- On disk after: `work-lane` worktree + branch still present;
  `untouched-lane` gone.

## Cleanup

Remove scratch state + demo repo (unique temp paths).

## Sharp edges

- If the pre-seed uses the same session that later runs the cleanup prompt,
  the worktrees may still be locked to that session — pre-seed in a SEPARATE
  serf invocation so they're unlocked (a dead session's lanes), matching the
  real "come back later and clean up" case `prune` targets.
- `prune` never takes force and skips anything with committed-but-unmerged
  work — so `work-lane` (committed, unmerged) is correctly skipped by `prune`
  even if the agent tries to prune everything. Note whether the agent
  understands the skip reason in the report.
