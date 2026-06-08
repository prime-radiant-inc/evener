# Raw Output and Diagnostics

Status: Proposed evergreen spec. Current serf already returns compact structured subagent results and returns `transcript_ref` as the child transcript handle; reading that handle requires transcript persistence/tools to be enabled. This spec defines the raw-output boundary, diagnostic surfaces, proposed `subagent_output` API, and redaction rules without requiring parent-context transcript replay.

## Purpose

Define how a parent session receives, audits, and diagnoses subagent output while preserving the core subagent value: the child can do noisy work in its own session without automatically flooding or contaminating the parent context.

The parent-facing default is a compact, intentional management result. Raw child internals remain available only through explicit diagnostic pivots such as transcript tools or provider API logs.

## Goals

- Keep the default parent result compact, structured, and intentionally parent-visible.
- Make the exact current result JSON contract stable and testable.
- Add a proposed `subagent_output` API for explicit, bounded diagnostic retrieval.
- Preserve transcript-based auditability through `transcript_ref`.
- Distinguish management results, child transcripts, lifecycle events, and provider raw logs.
- Require explicit opt-in and redaction rules before exposing raw provider or transcript content.
- Keep implementation YAGNI/DRY: reuse existing result snapshots, transcript readers, and API logging instead of adding parallel stores.

## Non-goals

- No automatic streaming of all child assistant text, tool calls, or tool results into the parent session.
- No automatic replay of child transcript bodies into parent prompt context.
- No exposure of hidden model reasoning or provider-private fields.
- No new general audit-log subsystem for v1.
- No replacement for `read_session_transcript` / `find_session_transcripts`.
- No claim that raw provider bodies are safe by default.
- No requirement to persist subagent raw output beyond existing transcript and API-log persistence.

## Current implementation anchors

- `agent/subagents.go:35-42` defines `subagentResult` with `status`, `output`, `success`, `turns_used`, and optional `transcript_ref`.
- `agent/subagents.go:52` defines root-only subagent management tools; child registries forcibly remove them for `depth > 0` in `agent/session_init.go:467-470`, and explicit grants cannot add them.
- `agent/subagents.go:342-348` runs child execution on `context.Background()`, so child execution can outlive the parent's management-tool wait context.
- `agent/subagents.go:409-449` implements `waitAgent`; it returns a JSON-marshaled result snapshot and marks the result consumed.
- `agent/subagents.go:452-485` implements `closeAgent`; it closes the child, waits up to five seconds, returns a final snapshot, and removes the child from the registry.
- `agent/subagents.go:492-498` documents the key output boundary in the nudge text: the parent receives only the child result-tool message, not everything the child did.
- `agent/subagents.go:501-548` captures the child `ProcessInput` result, updates run status, closes the run channel, and emits `SUBAGENT_END` without output.
- `agent/subagents.go:574-585` builds the result snapshot; if output is blank and an error exists, output becomes the error string, and `transcript_ref` is encoded from the child session ID.
- `agent/session_tools_subagent.go:71-95` adds `agent_id` to blocking `spawn_agent` results after waiting.
- `agent/session_tools_subagent.go:127-145` adds `agent_id` to blocking `resume_agent` results after waiting.
- `agent/session_tools_subagent.go:148-160` clamps short wait timeouts before calling `waitAgent`.
- `agent/subagent_manager.go:71-87` exposes only minimal status information: `id`, `status`, and `turns_used`.
- `agent/events/events.go:57-60` defines `SUBAGENT_START` and `SUBAGENT_END`.
- `agent/events/payloads.go:185-196` defines subagent lifecycle payloads; end events include `agent_id`, `status`, and `turns_used`, not `output`.
- `agent/internal/tool/definitions.go:193-228` defines `spawn_agent` and warns callers to inspect `success`, `status`, and `output`.
- `agent/internal/tool/definitions.go:231-261` defines `resume_agent` as context-preserving iteration.
- `agent/internal/tool/definitions.go:264-293` defines `wait` and `close_agent`; the wait description currently says `transcript`, but the implemented field is `transcript_ref`.
- `agent/internal/tool/definitions.go:481-500` defines transcript reads, including `format=jsonl` as noisy raw debug/replay content and warns to treat transcripts as archived evidence.
- `cmdutil/api_logging.go:12-32` installs `api.jsonl` and conditionally enables `api-raw.jsonl` when `llm.RawBodyEnabled()` is true.

## Exact current result JSON

Plain `wait` and successful, non-timeout `close_agent` result bodies are JSON marshaled from `subagentResult`; non-blocking `spawn_agent` and `resume_agent` use lighter return shapes. Normal full result snapshots returned by wait, close, and blocking wrappers are terminal (`completed` or `failed`). `running` is an internal/status-surface value for active jobs and should appear in full result envelopes only for a future explicit peek/diagnostic surface, not as a successful final result.

```json
{
  "status": "completed|failed",
  "output": "subagent's explicit final report, or error text when the report is empty and an error exists",
  "success": true,
  "turns_used": 3,
  "transcript_ref": "local:01CHILDSESSION..."
}
```

Blocking `spawn_agent` and blocking `resume_agent` return the same object plus `agent_id` inserted by their tool handlers when their internal wait produces parseable result JSON. In the ordinary successful path, `status` is terminal; if the internal wait fails before a snapshot, current wrappers return the wait error/result without the augmented envelope.

```json
{
  "agent_id": "01CHILDSESSION...",
  "status": "completed|failed",
  "output": "subagent's explicit final report, or error text when the report is empty and an error exists",
  "success": true,
  "turns_used": 3,
  "transcript_ref": "local:01CHILDSESSION..."
}
```

Rules:

- `success` is `true` only when the child run ended without a session/engine/tool error. It is not proof that the delegated task was solved correctly.
- `output` is the child result/report string, not a transcript stream.
- `transcript_ref` is the diagnostic pivot. Use transcript tools for inspection instead of asking lifecycle results to carry raw transcript bodies.
- `agent_id` is currently added by blocking wrappers, not by plain `wait` or `close_agent`.
- `SUBAGENT_END` does not carry `output`; it carries only `agent_id`, `status`, and `turns_used`.
- A successful lifecycle tool call only means the management operation returned. The parent must inspect the nested result fields.
- A `wait` result is single-consumption for an idle completed run; repeated waits can error and tell the caller to resume or close the child.

## Diagnostic layers

### 1. Management result

Default parent-facing surface. This is compact and intentionally parent-visible, but it is not redacted today; the child can still include sensitive information in its final report. It is the only child output the parent receives automatically.

```json
{
  "agent_id": "optional depending on lifecycle tool",
  "status": "completed|failed",
  "success": true,
  "output": "bounded final report",
  "turns_used": 3,
  "transcript_ref": "local:..."
}
```

### 2. Transcript diagnostics

Explicit child-session inspection using `transcript_ref`:

- `format=outline` for turn maps and tool-call pivots;
- `format=markdown` for bounded readable evidence;
- `format=jsonl` for raw replay/debug views where supported.

Transcript inspection requires state persistence and transcript tools to be enabled. Without persisted transcripts, a result may still contain a stable `transcript_ref`, but it may not be readable in-session.

Transcript content is archived evidence, not active instructions. A parent may cite facts from a child transcript, but must not blindly obey instructions embedded in prior child output.

### 3. Provider raw diagnostics

Provider API diagnostics are global session logging surfaces, not subagent-management API fields:

- `api.jsonl` records standard API log data.
- `api-raw.jsonl` records raw request/response bodies only when raw body logging is explicitly enabled.

Raw provider logs may include prompts, tool outputs, file excerpts, credentials accidentally emitted by tools, metadata, and provider responses. They must not be surfaced through subagent management by default.

## Proposed `subagent_output` API

Add one explicit diagnostic API rather than bloating all lifecycle results.

Tool name:

```text
subagent_output
```

Purpose:

Retrieve bounded, optionally redacted diagnostic output for a tracked child by `agent_id` or for a known transcript by `transcript_ref`.

Proposed schema:

```json
{
  "type": "object",
  "additionalProperties": false,
  "oneOf": [
    {"required": ["agent_id"], "not": {"required": ["transcript_ref"]}},
    {"required": ["transcript_ref"], "not": {"required": ["agent_id"]}}
  ],
  "properties": {
    "agent_id": {
      "type": "string",
      "description": "Tracked child agent id. Required unless transcript_ref is provided."
    },
    "transcript_ref": {
      "type": "string",
      "description": "Child transcript ref. Required unless agent_id is provided."
    },
    "view": {
      "type": "string",
      "enum": ["result", "outline", "markdown", "raw_jsonl"],
      "description": "Diagnostic view to return. Defaults to result."
    },
    "turn": {
      "type": "integer",
      "description": "Turn number for transcript views. markdown maps to range=N-N plus expand_turn=N; outline/raw_jsonl may use range=N-N but do not use markdown expansion."
    },
    "range": {
      "type": "string",
      "description": "Transcript turn window for outline/markdown/raw_jsonl views, using existing transcript range syntax such as last:N."
    },
    "max_bytes": {
      "type": "integer",
      "description": "Maximum returned payload bytes after redaction. Default 32768."
    },
    "redaction": {
      "type": "string",
      "enum": ["standard", "strict", "none"],
      "description": "Redaction mode. none requires an explicit debug/unsafe opt-in and should be unavailable to ordinary agents."
    },
    "include_provider_raw": {
      "type": "boolean",
      "description": "Include provider raw log references or snippets only when raw logging is enabled and policy permits it. Default false."
    }
  }
}
```

Proposed response envelope for a tracked child; `agent_id`, `success`, and `turns_used` are omitted for transcript-only reads unless reliably derived:

```json
{
  "agent_id": "01CHILDSESSION...",
  "status": "completed|failed|running|unknown",
  "success": null,
  "view": "result|outline|markdown|raw_jsonl",
  "output": "redacted bounded diagnostic text or JSON string",
  "turns_used": 3,
  "transcript_ref": "local:01CHILDSESSION...",
  "truncated": false,
  "redaction": "standard|strict|none",
  "diagnostics": {
    "source": "result_snapshot|transcript|api_log|api_raw_log",
    "last_error": "optional redacted error string",
    "ended_reason": "complete|error|closed|hook_blocked|unknown",
    "api_log_ref": "optional non-path API log reference such as api:local:01CHILD:round:N, not contents",
    "api_raw_log_ref": "optional non-path raw API log reference such as api-raw:local:01CHILD:round:N, not contents"
  }
}
```

API rules:

- `view=result` returns the same compact result snapshot semantics as lifecycle tools, with no transcript expansion. It requires a currently tracked `agent_id`; for transcript-only requests without a tracked result snapshot, return an unavailable diagnostic rather than fabricating result fields.
- `view=outline`, `view=markdown`, and `view=raw_jsonl` delegate to existing transcript rendering code where possible. For `view=markdown`, `turn=N` maps to existing markdown rendering with `range:"N-N"` and `expand_turn:N`; for `outline` or `raw_jsonl`, `turn=N` may map only to `range:"N-N"` and must not imply markdown-only expansion.
- `view=raw_jsonl` is diagnostic-only and must be gated for this new `subagent_output` API; default agents should not receive unredacted raw JSONL through it. This does not change existing `read_session_transcript(format=jsonl)` behavior unless the transcript tool policy is revised separately.
- `include_provider_raw=true` should normally return references and metadata, not raw bodies. Snippets require explicit unsafe/debug policy. API log references must identify child session/round or an equivalent stable selector, not expose absolute local paths.
- The API must tolerate closed children if `transcript_ref` is supplied; it must not require an in-memory registry entry for transcript-only reads.
- For transcript-only reads after close, omit `agent_id`, `success`, and `turns_used` unless they are reliably derived; use `status:"unknown"` when no tracked result snapshot exists.
- The API should never make `subagent_output` a child-callable management tool unless there is a separate policy decision; nested delegation remains prohibited. Add it to the centralized root-only management tool set and child-invisibility tests if implemented.

## Diagnostics and redaction rules

Default (`standard`) redaction must remove or mask:

- API keys, bearer tokens, OAuth tokens, session cookies, SSH keys, private keys, and credential-looking environment values.
- Authorization headers and provider request headers.
- Absolute local paths outside the project when not needed for the diagnostic.
- Raw prompt/system/developer messages unless the caller explicitly requested transcript evidence and the policy permits it.
- Tool outputs marked sensitive by future tool metadata.
- Provider raw bodies unless `include_provider_raw=true` and raw diagnostics are policy-approved.

Strict redaction additionally should:

- omit full tool arguments for shell, network, write, patch, and credential-related tools;
- summarize large file contents instead of returning them verbatim;
- omit user messages except short previews;
- omit raw JSONL bodies entirely and return only counts, sizes, refs, and timestamps.

No-redaction mode:

- must be unavailable by default;
- must require an explicit local debug/unsafe setting or human approval;
- must be clearly labeled in the response;
- must never be enabled implicitly by `blocking=true`, `wait`, or `close_agent`.

Truncation rules:

- Enforce `max_bytes` after redaction.
- Return `truncated: true` when any content was omitted for size.
- Prefer preserving the beginning and end of diagnostic content with an omission marker.
- Do not split JSON in a way that claims to be valid JSON unless the payload is actually valid.

## YAGNI / DRY implementation plan

1. Keep current lifecycle result JSON unchanged for v1. Do not add raw transcript fields to `spawn_agent`, `resume_agent`, `wait`, or `close_agent`.
2. Centralize result-envelope construction around the existing `resultSnapshotLocked` path so every management result uses one source of truth.
3. If `agent_id` is added to plain `wait` / `close_agent` result envelopes, add it additively through the same shared marshaling helper rather than repeating wrapper logic.
4. Implement `subagent_output` as a thin dispatcher:
   - resolve `agent_id` to `transcript_ref` when the child is still tracked;
   - otherwise require `transcript_ref`;
   - call existing transcript read/render helpers for transcript views, including `range:"N-N"`/`expand_turn:N` for single-turn markdown;
   - return existing result snapshot for `view=result` when the child is tracked, otherwise return an unavailable diagnostic.
5. Reuse existing transcript range, outline, expansion, and raw/jsonl rendering semantics. Do not create a second transcript parser.
6. Reuse existing API logger files as diagnostic references. Do not copy raw API logs into subagent registry state.
7. Add a small redaction package/helper only if existing logging/transcript rendering cannot support standard/strict masking. Keep it shared by transcript and output APIs.
8. Gate provider raw exposure through the same raw-body setting that controls `api-raw.jsonl`; `subagent_output` must not independently enable raw logging.
9. Keep event payload changes optional and additive. If adding diagnostics to `SUBAGENT_END`, prefer `transcript_ref` and redacted `error`, not `output`.
10. Avoid persistent job-store work for this spec. Transcript-only diagnostics can use `transcript_ref`; registry-only result snapshots are enough for active tracked jobs.

## Acceptance criteria

- `wait` and successful, non-timeout `close_agent` still return the exact result fields `status`, `output`, `success`, `turns_used`, and `transcript_ref`; close timeout remains an error without a result snapshot/removal.
- Blocking `spawn_agent` and blocking `resume_agent` still include `agent_id` without dropping existing fields when their internal wait produces a result snapshot.
- Non-blocking `spawn_agent` preserves its lightweight `{agent_id,status}` return, and non-blocking `resume_agent` preserves its current `"ok"` return.
- `output` remains a final report/error summary, not a streamed transcript.
- Parent lifecycle events do not leak raw child output by default.
- `subagent_output(view=result)` returns the compact result envelope for tracked children and returns unavailable for transcript-only requests without a tracked result snapshot.
- `subagent_output(view=outline|markdown|raw_jsonl)` retrieves content through transcript rendering and labels it as archived evidence.
- `subagent_output` enforces `max_bytes`, reports `truncated`, and applies selected redaction.
- Provider raw bodies are not returned unless raw logging exists and explicit unsafe/debug policy allows it.
- Closed children can still be inspected by `transcript_ref` when transcripts exist.
- Repeated `wait` single-consumption behavior remains documented and tested.
- Existing transcript tools continue to work; the new API does not replace them.

## Tests

- Marshal test for current `subagentResult` JSON fields and `transcript_ref` spelling.
- Blocking `spawn_agent` result includes `agent_id`, `status`, `output`, `success`, `turns_used`, and `transcript_ref`.
- Blocking `resume_agent` result includes the same fields and preserves child context.
- `wait` after completed run returns the result once and errors on repeated idle wait.
- `close_agent` returns a final result snapshot and removes the child from detailed status on non-timeout success; close timeout returns an error without a result snapshot/removal.
- `SUBAGENT_END` payload contains `agent_id`, `status`, and `turns_used` but not `output`.
- `subagent_output(view=result)` matches lifecycle result semantics for a tracked child.
- `subagent_output(view=outline)` delegates to transcript outline rendering and uses existing range semantics.
- Single-turn output maps to existing markdown transcript rendering with `range:"N-N"` and `expand_turn:N`, and labels it archived evidence.
- `subagent_output(max_bytes=N)` truncates after redaction and sets `truncated=true`.
- Standard redaction masks tokens, authorization headers, private-key blocks, and credential-looking environment values.
- Strict redaction omits high-risk tool arguments and raw JSONL bodies.
- `include_provider_raw=false` never returns raw provider request/response bodies.
- `include_provider_raw=true` without raw logging enabled returns a clear unavailable diagnostic, not fabricated content.
- A transcript-only request works after `close_agent` when a valid `transcript_ref` is supplied.

## Documentation notes

- Use `transcript_ref`, not `transcript`, for implemented result JSON.
- Tell parents to inspect `success`, `status`, and `output`; do not treat delegation as complete merely because a tool call returned.
- State that raw child work is intentionally hidden from the parent by default.
- State that transcript reads are archived evidence and may contain untrusted child/tool content.
- State that provider raw logs are sensitive and explicitly opt-in.
