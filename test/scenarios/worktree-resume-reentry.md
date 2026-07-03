# worktree-resume-reentry: a resumed session re-enters its worktree

**What this covers**: the §7 resume re-entry path (branch
`worktree-native-worktree-tools`) — a session that was occupying a managed
worktree, killed, then resumed, must re-enter the lane (re-locking it) so
subsequent tools operate inside it; the foreign-lock variant lands at the
restore root with a legible notice. Unit-tested only before this card; resume
is a real user path and the notice strings are model-facing.

Live end-to-end, real provider (billed).

## Pre-state

Fresh binary + hermetic repo + isolated `SERF_STATE_DIR` (config symlinked).

## Steps

1. **Run 1 (create + occupy, then end):** prompt: `"Create an isolated worktree
   called resume-lane and stay in it. Add a file NOTES.md containing the word
   BEACON inside the worktree (do not commit). Report the worktree path, then
   stop."` Capture the session id from the run output.
2. **Run 2 (resume):** `serf --resume <id> --dir <repo>` with prompt: `"Where
   are you working right now? Run pwd and list the files in the current
   directory, and tell me whether NOTES.md with BEACON is present here."`
3. Inspect Run 2's transcript + the tool the agent sees.

## Expected

- Run 1 leaves `resume-lane` on disk with `NOTES.md` (BEACON) inside it; the
  session meta records the active worktree path.
- Run 2: the resumed session's cwd is the **worktree** path (not the launch
  dir), and the agent confirms `NOTES.md`/BEACON is present in the current
  directory. **Falsify**: if the resumed agent reports the main repo root, or
  says NOTES.md is absent, re-entry did not fire — the swap silently reverted
  on resume (the exact bug §7 exists to prevent).
- On disk: the lane is re-locked with the resumed session's `serf:` marker
  (`git worktree list --porcelain` shows `locked serf:<id>`).

## Cleanup

Remove the scratch state + demo repo (unique temp paths).

## Sharp edges

- Run 1 must END while still inside the lane (don't exit the worktree) so meta
  records it as active. A clean end unlocks the lane on close; resume re-locks
  it — that's the tested path.
- The resume must use the SAME `SERF_STATE_DIR` and `--dir` so the session and
  its worktree resolve. `--resume <id>` needs the id from Run 1's output
  (`--list-sessions` also shows it).
- If Run 1's model exits the worktree before ending, meta records no active
  lane and Run 2 correctly lands at the root — that's a model-behavior caveat,
  not a re-entry failure; re-run with an explicit "stay in it" instruction.
