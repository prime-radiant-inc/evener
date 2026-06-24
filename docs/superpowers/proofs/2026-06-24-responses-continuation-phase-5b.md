# Responses Continuation Phase 5B Proof

## Scope

Phase 5B adds an adapter-callable attempt recorder for OpenAI Responses-to-Chat fallback attempts.

The adapter reports value-like attempts only. The session owns attempt-index allocation and transcript emission. `llm.APILogger` composes with the same callback to write matching `api.jsonl` and raw-log entries, and suppresses the old outer stream line when explicit adapter attempts were recorded.

## Substrate Recheck

- Phase 5A attempt group identity exists on `ModelAttemptMetadata`, transcript `api_call`, `llm.APILogContext`, `llm.APILogEntry`, and raw HTTP log entries.
- `callModelWithFallback` creates one attempt group before provider dispatch and now installs the session-owned adapter recorder into `llm.APILogContext`.
- `processOneInput` still calls `logAPICall` once at the ordered transcript boundary; Phase 5B uses that boundary to append stored adapter attempts in order.
- The OpenAI adapter fallback sites are the immediate `Adapter.Stream` Responses open error and the empty-stream branch in `decodeStream`.
- `APILogger.WrapStream` is the wire-log middleware for streaming calls.

## RED Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai ./agent -run 'TestAPILoggerWritesAdapterAttemptRecords|TestAdapter_Stream_Records.*FallbackAttempts|TestSession_OpenAIResponsesContinuationPhase5B' -count=1 -v
```

Initial result: `./llm` and `./llm/providers/openai` failed to build because `AdapterAttemptRecord`, `AttemptRecorder`, and `RecordAdapterAttempt` did not exist. The session test failed with one transcript `api_call` instead of the required two fallback attempts.

## GREEN Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai ./agent -run 'TestAPILoggerWritesAdapterAttemptRecords|TestAPILoggerWrapStreamWritesRawAttemptMetadataOnError|TestAdapter_Stream_Records.*FallbackAttempts|TestStream_ResponsesAPI_404_FallsBackToChatCompletions|TestAdapter_Stream_StampsEndpointURL_ChatCompletionsFallback|TestSession_OpenAIResponsesContinuationPhase5B' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- The session-owned recorder assigns a shared attempt group id and 1-based indexes to adapter-reported attempts.
- Immediate OpenAI Responses-to-Chat fallback reports a failed Responses attempt followed by a terminal Chat fallback attempt.
- Empty Responses stream fallback reports a failed Responses attempt followed by a terminal Chat fallback attempt.
- Transcript `api_call` records preserve the two fallback attempts in order under one attempt group, with `final_attempt_count=2` on the terminal Chat fallback attempt.
- `llm.APILogger` writes matching per-attempt API/raw records and avoids an extra outer final stream record when adapter attempts are explicit.
- The OpenAI adapter still does not write transcripts directly.

## Remaining Work

- Phase 6 must complete full-history fallback message cloning for fallback-capable adapter paths.
- Phase 7 must add continuation-error classification and Chat fallback ordering.
- Phase 8 must add same-Responses-endpoint full-history retry before model fallback.
- Runtime continuation registry entries remain disabled.
