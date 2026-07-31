# serf-doctor `apilog` — API-call diagnostics

Status: implemented (2026-06-20). Extends [2026-06-19-serf-doctor-unified-design.md](2026-06-19-serf-doctor-unified-design.md).

## Why

`tools/transcripts/api-log-analyze.py` was the only transcript tool whose function serf
had nowhere else. It answered serf-quality/cost questions — "which calls came back empty?",
"which errored?", "where is cache spend leaking?", "how many tokens did this session burn?" —
that matter regardless of any benchmark. The rest of the transcript scripts were either
already covered (serf-doctor `transcript`/`tree`, serf-hub HTML, `serf --resume-with`) or
purely terminal-bench. So this capability folds into serf-doctor; the scripts are removed.

## Data source

serf already records every LLM round to the transcript as an `api_call` line
(`agent/session_model_call.go` → `transcript.AppendAPICall`). The canonical type is
`transcript.APICall`, carrying `Round`, `LatencyMs`, `Request` (`llm.APILogRequest`:
model, provider, reasoning effort), `Response` (`llm.APILogResponse`: finish reason,
text length, tool-call count, `llm.Usage`), and `Error`/diagnostic fields on failure.

serf-doctor already reads these lines (verbatim, for the `--count` mention scan). `apilog`
parses them into `transcript.APICall` — serf's own type, so a schema change flows through
or fails to compile. The standalone `api.jsonl` latency log is **not** read; the in-transcript
`api_call` lines carry the same request/response/usage snapshot and keep the read-only,
single-session, canonical-types design intact. Malformed `api_call` lines are skipped
(diagnostic data, never load-bearing), matching the loader's existing tolerance.

## Surface

```
serf-doctor apilog <selector> [--empty] [--errors] [--cache-spikes [--threshold N]]
                              [--summary] [--validate] [--json]
```

- default — one row per call: round, model, latency, input/output tokens, cache-read,
  uncached-input, finish reason, text length, tool-call count, `ERROR` marker.
- `--summary` — session aggregate only: calls, empties, errors, total in/out/cache tokens,
  total tokens, average latency.
- `--empty` — only empty responses (response present, zero text **and** zero tool calls).
- `--errors` — only failed calls.
- `--cache-spikes [--threshold N]` — only calls whose uncached input (input − cache-read)
  is ≥ N (default 50000). Surfaces cache misses driving spend.
- `--validate` — a different operation, not a row filter (added by kata 7x84; short-circuits
  before `--empty`/`--errors`/`--cache-spikes`/`--threshold`/`--summary` are consulted, same
  as `transcript --count` short-circuits past `--format`/`--range`): strictly decode every
  record in the canonical API log from offset zero through clean EOF via `apilog.Decoder`,
  reporting every corrupt, malformed, oversized, or unsupported record with its byte offset
  instead of stopping at the first one, and separating a genuine partial final fragment from a
  decode defect. Explicit operator diagnostics — proportional to file size, never run at logger
  open (open-time recovery stays bounded to the tail; see
  `2026-07-17-project-2-yagni-landing-design.md`). Exits nonzero if the scan finds any
  structural problem; a bare partial tail alone does not. `--json` →
  `APILogValidationResult{session_id, api_log_path, file_size, records_ok, problems[],
  problem_count, problems_truncated, partial_tail, clean}`.
- `--json` — `APILogResult{session_id, calls[], totals{}}` (or `APILogValidationResult` under
  `--validate`, above).

Filters narrow the displayed rows; `totals` always reflects the whole session. Single-session
by selector, like every other subcommand; for a coordinator+subagents picture, walk
`serf-doctor tree` and run `apilog` per session.

Dropped from the Python tool as out-of-scope: directory recursion / multi-session scan
(eval-shaped), `--raw` (use `serf-doctor transcript`), `--session` (you select the session).
