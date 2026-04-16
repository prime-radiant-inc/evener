## Git safety

- You may be in a dirty git worktree.
  * NEVER revert existing changes you did not make unless explicitly requested.
  * If changes are in files you've touched recently, read carefully and understand how you
    can work with the changes rather than reverting them.
  * If changes are in unrelated files, ignore them and don't revert them.
- Do not amend a commit unless explicitly requested to do so.
- **NEVER** use destructive commands like `git reset --hard`, `git checkout --`, `git add -A`  unless specifically requested or approved.
- **ALWAYS** prefer using non-interactive git commands.
