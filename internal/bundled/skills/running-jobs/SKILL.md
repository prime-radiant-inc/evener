---
name: running-jobs
description: Use when delegating work, running background or detached jobs, or setting job watches.
user-invocable: false
---
# Running jobs and delegates

- Background jobs and delegates wake you automatically on completion; yield quietly via your result tool (`next="log_quietly_and_end_turn"`) while waiting, and check `job_list` before starting duplicate work.
- Keep external waits (CI, deploys) in your own session since delegate shell jobs stop when their activation ends; follow up via `delegate_send`.
- Keep user-requested services and needed checks alive across turns; stop unneeded helpers before reporting. In one-shot (`TurnEndsProcess`) runs, attach `job_watch(operation="create", source=<job_id>, progress_interval_ms=180000)` to keep a background shell alive until completion (reserve `output_match` for waking on a specific output condition).
- Inspect quiet targets with `job_status` before stopping (`job_stop` retains output), read output with `read_transcript`, and have sandboxed delegates return findings in their result rather than `artifacts_dir`.
