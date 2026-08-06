# WS1: Responses-API Response Recording Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recorded `llm.Response`s for OpenAI Responses-API sessions carry the
real text/tool-call content (fixing the fleet-wide `text_length=0,
tool_call_count=0, empty=true` apilog records), and serf-doctor's apilog
commands become fast on large logs and able to recompute historical records.

**Architecture:** Fix the recording at its source — `decodeResponsesStream`
builds the settled Response from the terminal `response.completed` payload,
which this provider family sends with an empty `output` array even when the
stream's `response.output_item.done` events carried real content. Synthesize
from accumulated stream items when the terminal payload is empty. Then make
`apilog` decode bodies lazily (metadata-only mode) and add `--recompute` so
pre-fix logs stay diagnosable.

**Tech stack:** Go. Modules: `llm` (provider adapter + apilog codec),
`agent/doctor`, `cmd/serf-doctor`. Test conventions per `docs/testing.md`.

**Context (verified 2026-08-06, file:line on main):**
- Counts are computed once at call time: `llm/api_attempt.go:519-520`
  (`response.TextLength = optionalAPILogInt(len(result.Response.Text()), true)`
  and the ToolCalls equivalent). serf-doctor only copies them
  (`agent/doctor/apilog.go:464-492`, `rowFromAttempt`; `Empty` computed at
  483-485).
- The Responses-API SSE state machine is
  `llm/providers/openai/responses.go:309` (`decodeResponsesStream`):
  `response.function_call_arguments.delta` case at :412,
  `response.output_item.done` at :493, `response.completed` at :559. The
  settled Response is built only when a terminal event was seen
  (:574-577, :605-608) via `fromResponses()` (:1118), which walks the
  terminal payload's `output` array (text parts :1143, function_call :1161).
- Affected sessions' raw SSE shows `response.completed` with `output: []`
  while earlier `response.output_item` events in the same stream carried the
  content (study evidence: 03410RPBSoDZIffktXxI9i, 0340WP5AwkXSbcQo6TNMA1).
- apilog decode cost: `EncodedBody.UnmarshalJSON` base64-decodes every body
  during record unmarshal (`llm/apilog/body.go:73,98`), then
  `validateRecord` decodes the same bodies again purely for byte-count/UTF-8
  checks (`llm/apilog/record.go:109,143,190,197`). `doctor.APILog`
  (`agent/doctor/apilog.go:116`) streams via `apilog.NewDecoder`
  (`llm/apilog/codec.go`) but pays full decode per record; `--errors` and
  `--empty` share this path (`cmd/serf-doctor/main.go:271`).
- `openaicompat` reuses the same Responses adapter
  (`llm/providers/openaicompat/adapter.go:372,381,389`), so one fix covers
  the codex-continuation family too.

## Global Constraints

- Fixtures must be **synthetic** SSE streams replicating the wire shape —
  never copy real session bodies into the repo (they contain conversation
  content).
- When both accumulated items and a non-empty terminal `output` exist, the
  terminal payload wins (it is the provider's settled truth).
- No behavior change to the live streaming path consumed by the agent — this
  work changes only what gets *recorded/settled*, plus doctor-side reading.
- Multi-module gates per `docs/conventions/go-workspace.md`: build and test
  every touched module (`llm`, `agent`, `cmd/serf-doctor`) before commit.
- Error messages and new flags follow existing serf-doctor conventions
  (`cmd/serf-doctor/main.go` flag patterns, `--json` support where output is
  structured).

---

### Task 1: Synthesize the settled Response from accumulated stream items

**Files:**
- Modify: `llm/providers/openai/responses.go` (`decodeResponsesStream` ~:309-620, `fromResponses` ~:1118)
- Test: `llm/providers/openai/responses_recording_test.go` (new)

**Interfaces:**
- Consumes: existing `decodeResponsesStream` internals — the accumulated
  output-item state it already maintains for streaming, `fromResponses(raw
  map[string]any, requestedModel string) llm.Response`.
- Produces: unchanged public API. The settled `llm.Response` handed to
  `attempt.Complete` now has non-empty `Text()`/`ToolCalls()` whenever the
  stream carried content, regardless of the terminal payload's `output`.

- [ ] **Step 1: Write the failing test.** Build a synthetic Responses-API
  SSE stream (as `[]byte` or a testdata file) that mirrors the affected wire
  shape: `response.created`, one `response.output_item.added` +
  `response.function_call_arguments.delta` sequence +
  `response.output_item.done` for a `function_call` item (name
  `write_file`, args `{"path":"x"}`), one text output item, and a terminal
  `response.completed` whose `response.output` is `[]` and which carries
  usage. Drive it through the adapter's stream decoding the same way
  existing tests in `llm/providers/openai` drive SSE (follow the prevailing
  test harness in that package). Assert the settled Response has
  `len(Text()) > 0` and `len(ToolCalls()) == 1` with the right name.
- [ ] **Step 2: Run it, confirm it fails** with both counts zero (current
  behavior: terminal payload wins even when empty).
- [ ] **Step 3: Implement.** In the terminal-settlement path of
  `decodeResponsesStream`: when the terminal payload's `output` is empty and
  accumulated output items exist, build the settled message content from the
  accumulated items (reusing the same conversion logic `fromResponses` uses
  for output entries — extract a shared helper rather than duplicating the
  walk). When the terminal `output` is non-empty, keep it authoritative.
  Track a disagreement (terminal non-empty but count differs from
  accumulated) with the package's existing logging/counter idiom — one line,
  not per-item.
- [ ] **Step 4: Second test: terminal-wins.** Same stream but with a
  populated terminal `output` differing from accumulated items; assert the
  settled Response matches the terminal payload.
- [ ] **Step 5: Run the `llm` module tests; all green.**
- [ ] **Step 6: Commit** (`fix(openai): settle Responses-API recordings from accumulated stream items when the terminal output is empty`).

### Task 2: Metadata-only decode mode for apilog summarization

**Files:**
- Modify: `llm/apilog/codec.go` (decoder options), `llm/apilog/body.go`, `llm/apilog/record.go` (validation split)
- Modify: `agent/doctor/apilog.go` (use the new mode)
- Test: `llm/apilog/codec_metadata_test.go` (new), plus a doctor-level test in `agent/doctor`

**Interfaces:**
- Produces: a decoder construction option (follow the package's existing
  option style; e.g. a variant constructor or an options struct field) that
  skips materializing/validating body bytes: `EncodedBody` fields carry the
  encoded form untouched, and `validateRecord` skips the body byte-count/
  UTF-8 revalidation in this mode. Full-decode behavior unchanged by
  default; `--validate` keeps strict decoding.
- Consumes: Task 1 is independent; no ordering dependency.

- [ ] **Step 1: Failing test:** construct a record whose body is large and
  *deliberately corrupt base64*; assert metadata-only decode still yields
  the attempt's scalar fields (model, tokens, TextLength) without error,
  while the default strict decode rejects it (existing behavior pinned in
  the same test).
- [ ] **Step 2: Implement** the mode. The two body decodes to bypass:
  `EncodedBody.UnmarshalJSON`'s `DecodeBody` call (`llm/apilog/body.go:98`)
  and `validateRecord`'s (`llm/apilog/record.go:143,197`).
- [ ] **Step 3: Switch `doctor.APILog` summarization** (`agent/doctor/apilog.go:128`)
  to metadata-only; `--validate` path keeps strict. Add/adjust a doctor test
  asserting summaries over a fixture log are unchanged between modes.
- [ ] **Step 4: Benchmark guard (informal):** add a Go benchmark over a
  generated many-record log; record the before/after ratio in the commit
  message (no CI perf gate).
- [ ] **Step 5: All touched module tests green; commit**
  (`perf(apilog): metadata-only decode for doctor summarization`).

### Task 3: `serf-doctor apilog --recompute` for historical logs

**Files:**
- Modify: `agent/doctor/apilog.go`, `cmd/serf-doctor/main.go` (flag)
- Modify (export seam if needed): `llm/providers/openai/responses.go` (`fromResponses` is unexported; add a small exported entry point in the `openai` package for offline re-extraction, e.g. a func taking the raw response body bytes and returning the canonical `llm.Response` — name it by domain: `ExtractRecordedResponse`)
- Test: `agent/doctor/apilog_recompute_test.go` (new)

**Interfaces:**
- Consumes: Task 2's decoder (recompute needs full bodies — it uses strict
  or a body-on-demand read, not metadata-only), Task 1's shared conversion
  helper.
- Produces: `--recompute` on `serf-doctor apilog`: for rows with
  `TextLength==0 && ToolCalls==0` and a stored response body, re-extract
  via the provider-shape parser (Responses-API SSE or chat-completions JSON,
  dispatched on the recorded endpoint/body shape) and report
  `recorded=0 recomputed=N` on those rows; totals gain a
  `recomputed_nonempty` count. `--json` includes both figures.

- [ ] **Step 1: Failing test:** fixture log containing one record whose
  stored body is the Task 1 synthetic SSE (empty terminal output) and
  recorded counts 0; assert `--recompute` reports recomputed tool_calls=1
  and the summary counts it.
- [ ] **Step 2: Implement** the re-extraction dispatch + row/summary/JSON
  plumbing, following `cmdAPILog`'s existing flag and output patterns.
- [ ] **Step 3: Manual acceptance (documented in the SDD report, not a
  repo fixture):** run against session 0340eCRdIZ5UJ4oIgD2Jrw (150 calls,
  currently 150 "empty") from local state; expect a large majority
  recomputed non-empty. Do not commit any session data.
- [ ] **Step 4: All gates green; commit**
  (`feat(serf-doctor): apilog --recompute re-extracts zeroed historical records`).

## Acceptance (whole workstream)

- Task 1 fixture decodes with real counts; terminal-wins pinned.
- A fresh session on an OpenAI-family model records nonzero
  `tool_call_count` (manual check via `serf-doctor apilog` after merge).
- `apilog --summary` on a multi-hundred-MB log completes in minutes
  (benchmark ratio recorded).
- `--recompute` corrects the known-bad historical session.
