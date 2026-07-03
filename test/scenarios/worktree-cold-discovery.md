# worktree-cold-discovery: the tool sells itself with zero hand-holding

**What this covers**: pure discoverability of `manage_worktree` (branch
`worktree-native-worktree-tools`). The prompt never says "worktree", "branch",
or names any tool — it describes a *need* ("isolate this so it doesn't disturb
my current files"). Does the tool's description alone lead the agent to reach
for it over ad-hoc alternatives (a copy of the dir, a new branch in place, a
stash)? The strongest ergonomics signal, and the one that most separates model
tiers.

Live end-to-end, real provider (billed).

## Pre-state

Same harness: fresh binary, hermetic repo with committed `main.go`, isolated
`SERF_STATE_DIR` with config symlinked.

## Steps

1. Prompt, deliberately tool-agnostic:
   `"I want to try a big speculative refactor of main.go, but I do NOT want it
   to disturb the files I'm currently working on — I might throw it away. Set
   me up a clean, separate place to do this experiment that won't touch my
   current working copy, and tell me where it is."`
2. Inspect: which mechanism the agent chose.

## Expected

- The agent reaches for `manage_worktree create`. **Falsify / record as miss**
  if it instead: makes a `cp -r` copy, creates a branch in place (which does
  NOT satisfy "won't touch my current working copy"), `git stash`es, or asks a
  clarifying question instead of acting. Each alternative is a distinct
  ergonomics data point — record exactly which, per model.
- If it does use the tool, the final message points the user at the worktree
  path.
- This card's PASS bar is softer than the others: the interesting output is
  the *distribution* of choices across model tiers, not a single verdict.
  Record what each model chose and its stated reasoning.

## Cleanup

Remove scratch state + demo repo (unique temp paths).

## Sharp edges

- A branch-in-place is a plausible-but-wrong answer (it changes the current
  checkout's branch); if a model picks it, that's a real finding about whether
  the tool description communicates the *isolation* property strongly enough.
- Some models narrate a plan without acting; `--max-rounds` must be high
  enough that "set me up" is actually executed, not just described.
