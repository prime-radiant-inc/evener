---
name: using-worktrees
description: Use when creating or retiring git worktrees, isolating parallel writers, or merging a branch locally.
user-invocable: false
---
# Using worktrees

## When to use `manage_worktree`

Use `manage_worktree` for isolated, parallel, or risky work, such as scratch experiments or isolating a delegate's edits. Creating a worktree requires a local Git checkout; outside Git, run colliding writers sequentially. Use plain `git` commands for ordinary branches.

## Local git merge protocol

`manage_worktree` has no merge operation. Exit to the main checkout and merge with plain `git`:

1. Note your current branch and target commit.
2. Fetch only that ref with `--no-tags`.
3. Confirm right before merging that neither branch moved and no uncommitted edits collide; stop and report if either moved or collides.
4. Merge with `git merge --no-ff --no-edit`.

## Post-merge cleanup

Once tip commits land upstream, remove owned task worktrees and branches before reporting unless asked to keep them. Confirm no dirty, untracked, or live work still needs that checkout, leave it (`exit`), run `remove` with `delete_branch=true` or `dispose` (use `force=true` only after verifying tip commits landed upstream or when explicitly asked to discard them, keeping `force_dirty=false`), and verify both the worktree and branch are gone while leaving shared, remote, and other sessions' work alone.
