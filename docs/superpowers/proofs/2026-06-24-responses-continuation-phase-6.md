# Responses Continuation Phase 6 Proof

## Scope

Phase 6 completes OpenAI Chat Completions fallback cloning for test-driven delta-shaped requests. It does not broaden real-session `responses_delta` selection for fallback-capable OpenAI paths.

## Substrate Recheck

- `llm.Request.FullHistoryFallbackMessages` is the paired full-history message slice intended for Chat fallback.
- OpenAI immediate fallback enters `fallbackToChatCompletions`.
- OpenAI empty-stream fallback enters the `decodeStream` empty-stream branch.
- `buildChatCompletionsBody` remains the provider-safe replay serialization boundary.
- Phase 5B fallback attempt records remain in place and are tested alongside Phase 6.

## RED Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages' -count=1 -v
```

Initial result: failed. Both immediate and empty-stream Chat fallback request bodies contained `PHASE6_DELTA_ONLY_MARKER` and omitted `PHASE6_FULL_HISTORY_MARKER`.

## GREEN Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages|TestAdapter_Stream_Records.*FallbackAttempts|TestStream_ResponsesAPI_404_FallsBackToChatCompletions|TestAdapter_Stream_StampsEndpointURL_ChatCompletionsFallback' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- Immediate Responses fallback builds Chat Completions from `FullHistoryFallbackMessages` when present.
- Empty-stream Responses fallback builds Chat Completions from `FullHistoryFallbackMessages` when present.
- Chat fallback clears `PreviousResponseID`, `ConversationID`, continuation metadata, and the fallback message sidecar before serialization.
- Chat fallback stamps `history_mode=chat_completions_fallback`.
- Existing OpenAI fallback behavior and endpoint URL stamping still pass.
- Real-session `responses_delta` selection remains unchanged; runtime continuation entries remain disabled.

## Remaining Work

- Phase 7 must classify continuation errors and bypass Chat fallback for continuation rejections.
- Phase 8 must add same-Responses-endpoint full-history retry before model fallback.
- Phase 9 must complete broad real-session delta behavior and replay sanitizer coverage.
