== panel ef75813d-52aa-40a4-8090-c97f779447c2 head ee578eab4 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=20854 verdict=1 chars=170
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20855 verdict=1 chars=177
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=20856 verdict=1 chars=2137
-- member 3 codex/glm-5.3-vision-background type=default status=canceled job=20857 verdict= chars=0

######## member 0 (codex default)
No issues found.

Summary: The changes align restore-time ask-boundary derivation with live steering, interruption, failure, provenance, fork, and accumulation semantics.

######## member 1 (codex default)
No issues found.
Summary: Restore now derives pending asks from the same live-path boundary with fork-scoped provenance and accumulation, backed by live-path integration tests.


######## member 2 (pi default)
## Summary

The commit series unifies the ask-boundary logic used by live session state and restore-time derivation. Concretely:

- `turnResolvesAskBoundary` becomes the single oracle for "this turn resolves a pending ask": `TurnUserInput`, an interrupted `TurnSteering`, a user steer whose provenance (kind, journal method, or write-path text shape) says it answers, and a `SteeringCarrier`-tagged `TurnFailure`.
- `steeringAnswersAsk` collapses the former `steeringSourceAnswersAsk` and `steeringKind()` split so live mid-round clears, steering-carrier claims, and the restore scan share one predicate, adding the text-shape fallback for kindless/provenance-less inherited notes.
- `deriveRestoredAskPending` now accumulates ask_user questions across non-resolving rounds (human-note carriers), stops at the first round whose own entry resolves (`roundEntryResolvesAskBoundary`), and orders questions oldest-round-first to match the live append semantics.
- `deriveRestoredState`/`deriveRestoredAskPending`/`recomputeRestoredState` take `divergenceTurn` and journal provenance, scoped by the shared `steeringOriginBoundary` clamp, so a forked child's inherited prefix is never reclassified by a colliding id in its own journal.
- Tests cover interrupts (same and later round), accepted user steers, failed/selection-failed carriers, failed human-note carriers, kindless legacy notes with and without provenance, accumulation, fork provenance collisions, and the bookkeeping-turn walk. Production tagging at `acceptSteeringCarrierInput` and `recordFailedSteeringSelection` is gated on the same predicate.

I traced the live-vs-restore boundary equivalence (including the text-shape fallback surviving history escaping, the append/reversal order, the round-entry walk across mid-round steering, tagged vs untagged failures, and interrupt ordering) and the state/pending-set consistency; the two functions cannot produce `SessionIdle` with a non-empty pending set, and every intentional divergence (e.g. a generic completion in a non-resolving carrier round remaining `SessionAwaiting`) is consistent with live behavior.

No issues found.

######## member 3 (codex default)
<no output; error: >

verification_snapshot_utc=2026-09-19T05:51:48Z
