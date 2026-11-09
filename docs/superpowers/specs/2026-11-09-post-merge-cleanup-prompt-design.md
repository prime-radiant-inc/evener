# Post-merge cleanup prompt design

## Intent

The default system prompt makes local branch/worktree cleanup part of finishing after upstream merge. It also requires removal of unneeded scratch artifacts. Refer to the person as “your human partner.”

## Scope and approach

Add the rule to `agent/prompts/system.md.tmpl`, under Finishing. Keep cleanup model-directed through existing tools; do not add automatic deletion or change worktree safety gates. A global prune is too broad for task-owned cleanup. Preserve custom base-instruction overrides.

## Required behavior

- Confirm all work at the current task branch tip landed upstream before removing the local task branch and worktree. A squash or rebase merge needs evidence beyond ancestry.
- Preserve uncommitted/untracked work, later commits, running work and required deliverables. Honor retention requests. Report what remains and why.
- Move the session to a surviving checkout before removing its worktree. Leave shared and remote branches and other sessions' work alone. Verify worktree and branch deletion separately.
- Use capability-gated `manage_worktree` guidance. Root sessions remove their task lane with `delete_branch=true`; parents dispose of their delegate lanes. Delegates never remove their own isolation worktree.
- A verified squash/rebase merge may justify the existing merge-check override, never discarding dirty work.
- List scratch before the final report and remove no-longer-needed temporary files, logs, copies and generated artifacts. Retain needed files and report why. Protect runtime files and other sessions' files.
- Use “your human partner” for references to the person throughout the default template. Keep machine identifiers such as `ask_user` and role tokens unchanged.

## Verification and limits

Use complete Finishing-section golden snapshots for root sessions with and without worktree tools, a delegating child with disposal capability, and a leaf child. Run existing prompt assembly, override, and tool-availability tests. Review ordinary merge, squash merge, dirty work, retained deliverables and parent-owned cleanup against the wording. This proves prompt composition, not live-model compliance; no live model evaluation is claimed.

Update the evergreen worktree guide with the model-directed responsibility, leaving runtime semantics unchanged. This task creates a PR; it does not merge it or remove its unmerged branch.
