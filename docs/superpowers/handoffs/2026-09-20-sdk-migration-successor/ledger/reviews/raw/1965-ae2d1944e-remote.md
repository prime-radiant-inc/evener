== panel 3dc0b8c9-c05b-48e3-af9a-b41ffbc2ac8a head ae2d1944e outcome  synthesis 
-- member 0 codex/gpt-5.6-luna type=default status=done job=20814 verdict=0 chars=588
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20815 verdict=1 chars=240
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=20816 verdict=0 chars=2533
-- member 3 codex/glm-5.3-vision-background type=default status=running job=20817 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Low
- **Location**: `mobile-native/src/mutationOutboxStorage.ts:137`
- **Problem**: Explicit `optimisticDisplay: null` is read back as `undefined`, so the adapter no longer round-trips the `unknown` field faithfully; `null` is used by existing mutation intents.
- **Fix**: Preserve parsed `null`, or introduce a distinct storage encoding for `undefined` and add coverage for both values.

## Summary

Adds native outbox conformance coverage and normalizes accepted-record reads, but loses the distinction between null and undefined optimistic displays.

######## member 1 (codex default)
No issues found.
Summary: Native outbox `fromRow` now round-trips absent display as `undefined` and omits `attempted` on accepted records, with conformance tests covering those contracts plus attachments/identity and target-scoped restore.


######## member 2 (pi default)
## Summary

The commit is primarily a test-suite cleanup for the native `MutationOutboxSQLite` adapter: it rewrites stale "D25d-1a/1b" comments, adds a round-trip test for attachments/composer text/client identity, adds a test that `settleReceipt` clears attempt evidence on the promoted optimistic row, broadens the `restoreProvenAbsent` test to check target scoping and authoritative-record preservation, and strengthens the absent-`optimisticDisplay` test to assert the read path. It also makes two small production conformance changes in `fromRow`: absent display (`JSON "null"`) reads back as `undefined` instead of `null`, and `attempted` is omitted from records whose stored state is `"accepted"` (matching `MutationOptimisticRecord`, which does not declare it). Both production changes are exercised by the new tests and align with the web IndexedDB adapter for the absent/undefined case.

## Issues

**Severity: low** — `mobile-native/src/mutationOutboxStorage.ts:137`

`optimisticDisplay: optimisticDisplay === null ? undefined : optimisticDisplay` collapses an explicitly-persisted `null` display into `undefined` on read. The write side (`insertValues` at line 427: `JSON.stringify(record.optimisticDisplay ?? null)`) stores both `undefined` and explicit `null` as `JSON "null"`, so the two cannot be distinguished at rest, and a record enqueued with `optimisticDisplay: null` (a value the package's own fixture builder uses — `appwire-client/typescript/state/mutation/testing.ts:57`) reloads differently than it was stored. The web oracle stores the record as given and returns `null`, so this is a real divergence from the oracle the suite comment claims to mirror: a consumer doing a strict equality or full-record reload comparison (`expect(reloaded).toEqual(persisted)`, the shape of the oracle's "reload restores the complete persisted intent" contract) on a `null`-display record would now fail against native storage.

No current consumer in `mobile-native` distinguishes `null` from `undefined` (both are falsy in `pendingEntries.outboxInput` and `settleReceipt`'s `retainsOptimisticDisplay`), so there is no functional break today; the impact is data-fidelity and future conformance.

Suggested fix: store SQL `NULL` for `undefined` and `JSON.stringify(null)` for explicit `null`, then map `row.optimistic_display === null` (SQL NULL) back to `undefined` and let a `"null"` string parse to `null`. If collapsing both is intentional, say so in the port comment and drop the conformance claim for explicit `null`.

######## member 3 (codex default)
<no output; error: >
