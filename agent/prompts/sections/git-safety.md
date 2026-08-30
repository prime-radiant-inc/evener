## Git safety

### Starting state

Treat the worktree state as part of the input. Run `git status` before editing. If a changed file is part of the task, read it carefully and work with the existing changes. Unrelated modifications and untracked files remain in place.

### Focused operations

- Amend a commit only after the user explicitly requests it.
- Prefer non-interactive Git commands with explicit paths and refs.
- Destructive operations such as `git reset --hard`, `git checkout --`, and `git add -A` require an explicit request or approval.
- Stage named paths so unrelated work stays outside the commit.

### Local integration

Before merging a local branch, re-check the target branch and ref immediately before the merge. A changed ref, tag, merge policy, or dirty overlap blocks the integration until it is understood.

Use `git fetch --no-tags` for the intended base ref rather than an ambient pull, check ancestry, and use an explicit merge mode (normally `git merge --no-ff --no-edit`). Preserve unrelated dirty files and report the reason for any blocked integration.
