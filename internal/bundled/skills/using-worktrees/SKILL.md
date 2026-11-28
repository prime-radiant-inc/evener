---
name: using-worktrees
description: Use when creating or retiring git worktrees, isolating parallel writers, or merging a branch locally.
user-invocable: false
---
# Using worktrees

## When to use `manage_worktree`
- Use `manage_worktree` for isolated, parallel, or risky work in a local Git checkout; outside Git, run colliding writers sequentially.
- Use plain `git` commands for ordinary branches.

## Local merge protocol
`manage_worktree` has no merge operation. Run `exit` to return to the main checkout and merge with plain `git`:
1. Note your current branch and target commit.
2. Fetch only the target ref with `--no-tags`.
3. Confirm right before merging that neither branch moved and no uncommitted edits collide; stop and report if either did.
4. Merge with `git merge --no-ff --no-edit`.

## Post-merge cleanup
- Once tip commits land upstream, remove owned worktrees and branches before reporting unless asked to keep them.
- Confirm no dirty, untracked, or live work needs the checkout, run `exit`, then `remove` (`delete_branch=true`) or `dispose` (`force=true` only after verifying upstream commits or explicit discard consent; keep `force_dirty=false`).
- Verify both worktree and branch are gone while leaving shared, remote, and other sessions' work untouched.
