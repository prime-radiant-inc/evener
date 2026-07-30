# Malformed Tool-Call History Recovery

## Problem

Session `033wtttaNuBna9dXsZMO34` contains a durable tool result for
`tool_v0T1OucHwVb5xYFsyIIfsNm3` without the assistant tool call that produced
it. Kimi emitted malformed JSON arguments for a shell call. Serf retained the
assistant response in live history, but transcript JSON marshaling rejected the
invalid `json.RawMessage`. Serf treated that write failure as a warning, then
durably recorded the pre-validation error result. On resume, the Anthropic
request contained a `tool_result` without a matching `tool_use`, so Kimi
rejected the request with HTTP 400.

Raw provider evidence belongs in the canonical API log. The transcript's
runtime contract is semantic replay: every stored turn must be valid JSON and
every stored tool result must have its assistant call.

## Goals

- Recover session `033wtttaNuBna9dXsZMO34` without a generic migration for
  other historical sessions.
- Store future malformed tool calls as valid semantic history while preserving
  their call ID, name, assistant text, and thinking.
- Keep tool validation precise and prevent malformed calls from executing.
- Abort before tool dispatch when an assistant turn cannot be recorded.
- Let the model receive the linked validation error and issue a corrected call
  on the next round.

## Non-goals

- Preserve malformed argument bytes in the transcript.
- Change provider request serializers or canonical API logging.
- Add generic reverse-orphan history repair.
- Change transcript schemas or repair other historical sessions.
- Change `serf-doctor`.

## Design

### One-time session repair

Before editing, verify no process has the target transcript open for writing and
make a byte-for-byte backup beside it.

Replace doctor turn 3071 (transcript `seq=3070`) from an orphan
`TOOL_RESULTS` turn into a `STEERING` turn with the same sequence number and
timestamp. Its user-role text states that the prior shell call had malformed
JSON arguments, was rejected before execution, and may be retried if still
needed.

This preserves transcript framing and sequence numbers, removes the invalid
provider-visible tool result, and retains the useful semantic fact. It does not
rewrite or renumber later turns.

### Safe assistant history

Before `appendAssistantTurn` records a response, it builds a copy-on-write
message for semantic history:

- content parts and tool-call values are copied only when a tool call has
  non-empty, syntactically invalid JSON arguments;
- that stored call's arguments become `{}`;
- call ID, item ID, name, type, assistant text, thinking, and all response
  metadata remain unchanged;
- the original provider response is not mutated.

The session loop extracts the original calls before assistant persistence.
Consequently, tool pre-validation still receives the malformed bytes and emits
the existing `PrevalOnly` error result. The stored assistant call and that result
retain the same call ID, so the next provider request is structurally valid and
the model can retry.

### Fail-closed assistant persistence

`appendAssistantTurn` returns an error. It writes the safe assistant turn to the
transcript before adding the same turn to live history. `emitAssistantResponse`
propagates that error to the session loop.

The session loop already calls `emitAssistantResponse` before canonicalizing or
dispatching tool calls. A persistence error therefore ends the round before any
tool executes or any tool result can be written. The error is surfaced locally
instead of permitting live and durable history to diverge.

This ordering is assistant-specific. Other turn recording behavior is outside
scope: a missing result after a durable assistant call is already handled by
the existing interrupted-tool repair path.

## Error behavior

- Malformed tool arguments are not a user-visible session failure. The call is
  stored with `{}`, rejected by normal pre-validation, and followed by a linked
  error result. The model gets another round and may correct the call.
- An assistant transcript-write failure aborts the round before tool dispatch
  and returns a local persistence error.
- No runtime path drops a malformed call's result or silently executes the
  sanitized `{}` arguments.

## Tests

All tests are deterministic and keep the fake boundary at the provider or
filesystem, as required by `docs/testing.md`.

1. Extend the existing malformed-tool-call session regression to prove:
   - the original malformed arguments reach pre-validation;
   - the tool does not execute;
   - live and durable history contain the assistant call with `{}`;
   - the linked result is `PrevalOnly` and carries the same call ID;
   - the next provider request is structurally valid and the scripted provider
     can issue a corrected call.
2. Close and restore the test session, then prove its replay history still
   contains the call before the result and accepts another input.
3. Inject an assistant transcript-write failure and prove:
   - the input returns an error;
   - no requested tool executes;
   - no assistant turn or tool result is added to live history after the failed
     write.
4. Mutation-check both protections:
   - removing argument sanitization restores the real transcript-marshal
     failure;
   - restoring warning-only persistence allows the no-dispatch regression to
     fail.

## Verification

- Focused malformed-call and assistant-persistence tests.
- `go test ./agent -count=1`.
- `go test ./... -count=1`.
- Relevant lint and `git diff --check`.
- `serf-doctor transcript` confirms the repaired real session has no orphan
  result at turn 3071.
- Resume the repaired session and send a message successfully.

## Acceptance criteria

- Session `033wtttaNuBna9dXsZMO34` accepts a new message without Kimi's
  `tool_call_id is not found` rejection.
- A future malformed tool call produces a durable `{}` assistant call followed
  by its linked validation error result.
- A transcript-write failure cannot be followed by tool execution.
- No transcript schema, provider serializer, generic history repair, or other
  historical session changes are included.
