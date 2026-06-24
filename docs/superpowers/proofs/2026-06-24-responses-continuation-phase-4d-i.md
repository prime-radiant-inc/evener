# Responses Continuation Phase 4D-i Proof

## Scope

Phase 4D-i proves real-session full-history anchor production through a Responses-only fake provider and an injected enabled registry. Runtime delta dispatch remains disabled.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4DI|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestResponsesContinuationAnchorCandidate|TestResponsesContinuationHistoryReservation' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- The production default registry remains disabled.
- A test-injected enabled public OpenAI registry can produce a full-history anchor request without `previous_response_id`.
- The full-history anchor request uses continuation-owned `store:true`.
- The assistant turn persists response id, response id hash, endpoint, request fingerprint, storage-scope fingerprint, context marker, and request model.
- Fallback-capable paths without `FullHistoryFallbackMessages` stay full-history with no continuation metadata.
- No live provider calls are made.
