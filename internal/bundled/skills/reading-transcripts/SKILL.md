---
name: reading-transcripts
description: Use when auditing prior sessions, recovering compacted context, reading missing delegate reports, or paging retained job or artifact output.
user-invocable: false
---
# Reading transcripts

## When to use transcript tools
- Rely on live tool results, job outputs, and notifications while work runs; wait for completion notices instead of polling `read_transcript`.
- Use transcript tools rather than raw disk files for audits, prior sessions, missing delegate reports, post-compaction recovery, and truncated output or retained logs.

## Discovery and paging
- Find archived sessions with `find_session_transcripts` when available (`seq: -1` snippets are display markers, not turn addresses).
- On unfamiliar sessions, read `format="outline"` first to locate turns, then fetch focused `range` windows or default `format="markdown"` with `expand_turn` and `offset_bytes` for long turns.
- Page or search retained `job:<job_id>` and `artifact:<id>` refs with `offset_bytes` or `output_match`, and use `doctor_evener` for API-log diagnostics.
