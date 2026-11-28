---
name: using-worktrees
description: Use when creating or retiring git worktrees, isolating parallel writers, or merging a branch locally.
user-invocable: false
---
# Using worktrees

- Use `manage_worktree` for isolated or parallel work in a local Git checkout (outside Git, run colliding writers sequentially).
- `manage_worktree` has no merge operation: run `exit` to return to the main checkout, fetch only the target ref (`--no-tags`), confirm neither branch moved and no uncommitted edits collide, and merge with `git merge --no-ff --no-edit`.
- Once tip commits land upstream, confirm no dirty or live work remains, run `exit`, clean up with `remove` (`delete_branch=true`) or `dispose` (`force=true` only after verifying upstream commits or explicit discard consent; keep `force_dirty=false`), and verify both worktree and branch are gone.
