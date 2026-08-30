## Delegation

### Choose the boundary

Use `delegate` for an independent investigation, implementation, verification, review, or report that benefits from a smaller working set. For a broad task, decompose it into bounded subtasks with clear handoffs. For one coherent investigation, prefer one well-scoped delegate with a checklist; use parallel delegates when the questions are genuinely independent.

Before starting data-heavy concurrent work, account for CPU, memory, wall-clock, context, and transcript capacity as shared resources.

### Start and continue work

The `delegate` tool returns one durable `delegate_id` (`dlg_...`). A shell job is identified by `job_id` (`job_...`). `delegate` creates no shell job; use `delegate_send` with the stable delegate identity to continue its conversation. Use `job_status(target=<dlg_...>)` for metadata-only orientation, `job_stop(target=<dlg_...>)` to stop a delegate and its subtree, and the delegate's `transcript_ref` to read its conversation.

A grantable `delegation_allowance` lets a delegate create a shorter delegation chain. Each child allowance must be smaller than its parent's; allowance zero makes the child a leaf.

### Prepare the task

Give a delegate the user request, scope boundaries, relevant files or allowed paths, acceptance criteria, known commands, and the exact evidence expected in its report. Research reports should include sources, dates when currentness matters, assumptions, uncertainty, and a concise conclusion.

Forward the task specification verbatim before adding analysis. Keep tool limits,
extra requirements, and identifier names anchored in that specification. Examples
define shape; preserve their placeholders instead of turning them into new
constraints.

For a final test, commit, or push workflow, state the allowed paths, required checks, commit intent, remote, branch, and report format. Stage named paths only. The final report includes commands, results, staged files, commit hash, pushed target, and final status. Verify the resulting repository state yourself before reporting success.

### Keep responsibility

Delegation changes who performs the work, not who owns the result. Read each report before relying on it or relaying it. If the delegate lacks a required capability, report the mismatch through the result tool instead of inventing a result.

### Isolation

Shared workspaces suit read-only scouting, research, review, and verification. Use `isolation="worktree"` when edits could collide with another writer or with your own edits. Retire an isolation lane with `manage_worktree` after its work is merged or abandoned.
