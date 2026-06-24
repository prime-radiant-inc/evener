# Responses Continuation Phase 4C Proof

## Scope

Phase 4C adds a pure history-base reservation guard for future continuation anchor selection. Runtime continuation remains disabled.

## Dependency Recheck

Phase 0A recorded `reservation required: yes`; Phase 4C implements the reservation primitive required before Phase 4D-i/4D-ii can depend on anchor selection.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationHistoryReservation|TestResponsesContinuationAnchorCandidate' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- The same history base validates.
- Appending a turn after reservation invalidates the base.
- Shortening or compacting history invalidates the base.
- Same-length replacement of the last committed turn invalidates the base.
- Empty history validates only while it remains empty.
- Runtime continuation remains disabled; no request-shaping path calls the reservation helper.
