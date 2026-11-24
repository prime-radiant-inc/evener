---
name: running-jobs
description: Use when delegating work, running background or detached jobs, or setting job watches.
user-invocable: false
---
# Running jobs and delegates

## Sizing and briefs

When `delegate` is available, do small pieces yourself when briefing takes longer than the work, delegate independent units in parallel, check `job_list` first to avoid duplicate workers, and keep external waits (CI, deploys) in your own session since delegate shell jobs end with their activation.

Start delegates clean by default (`fork_context=true` only when full history is required). Write a self-contained `prompt` covering the request, known facts and paths, owned vs. untouched files, exact acceptance checks, and expected evidence. Put multi-step plans in `task_list`, and ask research delegates for sources, dates, and confidence.

## Isolation and confinement

Share your checkout for read-only or single-writer work. Use `isolation="worktree"` in a local Git checkout with a short mnemonic `name` when writers collide, or run writers sequentially outside Git. Sandboxed delegates write only inside their workspace and temporary scratch, so have them return reports in their result rather than `artifacts_dir`.

## Background work and turn ends

Use `mode="background"` for session-owned jobs and `mode="detached"` (when available) only for processes that outlive the session. Keep user-requested services (including background jobs when detach is unavailable, which stop at session shutdown) and checks needed for a result alive across turns; stop unneeded helpers before ending your turn. In one-shot (`TurnEndsProcess`) runs, delegates survive turn end and wake you, while background shell jobs need an active `job_watch` (`progress_interval_ms` of a few minutes) to survive until completion. Keep parallel work within `<resource_caps>`, leaving memory for Evener.

## Observers, watches, and verification

Ask `watch_parent=true` observers to report via your configured result tool (default `communicate`), and clear parent watches when observation ends. Use timers only for external state, and check `job_status` before stopping a quiet delegate. Read delegate transcripts with `read_transcript` and verify deciding checks yourself before relying on reports.
