---
name: running-jobs
description: Use when delegating work, running background or detached jobs, or setting job watches.
user-invocable: false
---
# Running jobs and delegates

## Delegation briefs and isolation
- Do small tasks yourself when briefing takes longer than the work; delegate independent units in parallel and check `job_list` first to avoid duplicate workers.
- Keep external waits (CI, deploys) in your own session because a delegate's shell jobs stop when its activation ends.
- Start delegates clean by default (`fork_context=true` only when full history is required). Give a self-contained brief (goal, facts and paths, owned vs. untouched files, acceptance checks, expected evidence), put multi-step plans in `task_list`, and ask research delegates for sources, dates, and confidence.
- Share your checkout for read-only or single-writer work. Use `isolation="worktree"` with a short `name` in local Git checkouts when writers collide, or run colliding writers sequentially outside Git.
- Have sandboxed delegates return reports in their result rather than `artifacts_dir`, since they write only inside their workspace and scratch.

## Background work and turn ends
- Background jobs and delegates notify you on completion; yield quietly with your configured result tool (default `communicate`, `next="log_quietly_and_end_turn"`) while waiting.
- Keep user-requested services (including session-owned background jobs when `mode="detached"` is unavailable) and needed checks alive across turns; stop unneeded helpers before ending your turn.
- In one-shot (`TurnEndsProcess`) runs, delegates survive turn end and wake you; attach `job_watch(operation="create", source=<job_id>, progress_interval_ms=180000)` to keep a background shell alive until completion, and reserve `output_match` for waking on a specific output condition.
- Keep parallel work within `<resource_caps>`, leaving memory for Evener.

## Observers, watches, and verification
- Ask `watch_parent=true` observers to report via your configured result tool (default `communicate`), and clear parent watches when observation ends.
- Use timers only for external state, inspect quiet targets with `job_status` before stopping, read delegate transcripts with `read_transcript`, and verify deciding checks yourself.
