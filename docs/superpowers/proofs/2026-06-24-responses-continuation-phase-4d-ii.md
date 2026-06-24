# Responses Continuation Phase 4D-ii Proof

## Scope

Phase 4D-ii proves real-session delta consumption of a persisted anchor-shaped Responses assistant turn through a Responses-only fake provider and an injected enabled registry. Production runtime enablement remains disabled.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4D|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestResponsesContinuationAnchorCandidate|TestResponsesContinuationHistoryReservation' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- The fake-provider delta request uses `responses_delta`.
- The delta request sends `previous_response_id`.
- The delta request input contains only the new local user turn plus the system/developer prompt, not pre-anchor local history.
- Delta metadata includes previous-response hash, anchor index, delta count, delta turn kind, endpoint family, request fingerprint, storage-scope fingerprint, context marker, and storage policy.
- Real OpenAI adapter paths remain full-history while Chat Completions fallback cloning is absent.
- No production registry entry is enabled and no live provider calls are made.
