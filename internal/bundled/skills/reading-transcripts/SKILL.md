---
name: reading-transcripts
description: Choose live evidence vs. transcript tools and page archived sessions cleanly.
user-invocable: false
---
# Reading transcripts

## Live evidence vs. transcripts

Use live tool results, job outputs, and notifications while work runs. Use transcript tools for audits, prior sessions, missing delegate reports, post-compaction recovery, or truncated output and retained logs. Read transcripts only through these tools (never disk files), and never poll `read_transcript` for a running job.

## Discovery and paging workflow

Search sessions with `find_session_transcripts` (`{}`, `query`, or `children_of`); `seq: -1` snippets are metadata display markers, not turn addresses. On unfamiliar sessions, read `format="outline"` first to locate turns, then fetch focused `range` windows or page long turns with `expand_turn` and `offset_bytes`. Use `job:<job_id>` and `artifact:<id>` refs for bounded output/logs, and `doctor_evener` for API-log diagnostics.
