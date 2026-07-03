# worktree-lifecycle-merge-back: create → commit → exit → verify isolation → remove

**What this covers**: the full merge-back flow the spec's §4 `exit` exists for
(branch `worktree-native-worktree-tools`). Does the agent understand that work
committed in the lane is *isolated* from the main checkout until merged, that
`exit` returns it to the main root, and that `remove` disposes the lane? The
core comprehension test: does the agent grasp the isolation boundary?

Live end-to-end, real provider (billed).

## Pre-state

Same harness as `worktree-create-and-orient`: fresh serf binary, hermetic repo
with a committed `main.go`, isolated `SERF_STATE_DIR` with config symlinked.

## Steps

1. Single prompt:
   `"Create an isolated worktree called 'feature-x'. Inside it, add a new
   function 'Greet()' to main.go and commit it. Then return to the main
   checkout and confirm that main.go there does NOT contain Greet(). Finally,
   remove the worktree and its branch. Report what you did at each step."`
2. Inspect the transcript: the ordered `manage_worktree` calls, the shell/edit
   calls in between, and the agent's confirmation of isolation.

## Expected

- `create feature-x` → enter; `edit_file`/`shell` adds `Greet()`; a `git
  commit` inside the lane; then `manage_worktree exit`.
- After `exit`, the agent reads main.go at the main checkout and correctly
  states Greet() is **absent** there. **Falsify**: if it claims the change is
  visible in main, or never leaves the worktree to check, it did not
  understand isolation — a comprehension failure.
- `manage_worktree remove name=feature-x delete_branch=true` (or two calls)
  succeeds. **Falsify**: if `remove` errors because the agent forgot to `exit`
  first and the tool couldn't restore, note whether the error message told it
  what to do.
- On disk after cleanup: the worktree dir is gone; `git branch --list
  feature-x` is empty; `git worktree list` no longer shows it.

## Cleanup

Remove the scratch state + demo repo (unique temp paths).

## Sharp edges

- The agent must `git add && git commit` *inside* the lane for the isolation
  check to be meaningful; if it only edits without committing, exit's clean
  check and the main-checkout diff still show isolation, but the "commit"
  assertion is vacuous — note which path the agent took.
- `remove` with `delete_branch=true` on an unmerged branch is refused by
  serf's own merge gate unless `force` — the agent may need `force:true` since
  feature-x was never merged. Watch whether it discovers that from the refusal
  message.
