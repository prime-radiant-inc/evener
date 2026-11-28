---
name: reading-transcripts
description: Use when auditing prior sessions, recovering compacted context, reading missing delegate reports, or paging retained job or artifact output.
user-invocable: false
---
# Reading transcripts

- Page or search retained `job:<job_id>` and `artifact:<id>` output with `read_transcript` (`offset_bytes` or `output_match` with `context_lines`), and wait for completion notices instead of polling.
- For unfamiliar sessions, discover refs with `find_session_transcripts`, read `format="outline"` first to locate turns, then fetch focused `range` windows or `expand_turn` with `offset_bytes`.
- Use `doctor_evener` for API-log diagnostics.
