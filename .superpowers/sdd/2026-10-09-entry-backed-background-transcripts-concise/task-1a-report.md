# Task 1a report: bounded entry storage and codec

## Result

Task 1a implementation is complete and committed in the task changes. The entry-cache fixture now supports versioned item/turn batches, atomic coverage and encoded-byte accounting, and bounded index-backed windows. Production cache routing and its version-2 schema remain unchanged; entry stores are rejected for the production database name and enabled only for isolated fixture databases.

## Test-first evidence

- RED: from `cmd/evener-hub/frontend`, `npx vitest run src/stores/sessionEntryCache.test.ts -t 'orders bounded windows'` failed against the incomplete position index. The API returned the `sub-0` row where the independent expected order required `sub-2` first.
- GREEN: after indexing `(historyKey, entry, item, sub)` and normalizing missing `sub` to zero in the stored index field, the same focused test passed (1 passed, 3 skipped).
- Additional focused behavior tests cover codec round trips for omitted versus empty text, identity-only records and rich item fields; older-version rejection; item-free turn metadata; coverage provenance, suffix gaps and shorter-window preservation; clear-epoch refusal; byte totals; affected-key request counts over preloaded history; and real transaction abort/commit boundaries.

## Implementation and API

- `appwire-client/typescript/reducer.ts`, `reducer.test.ts`, `index.ts`, `README.md`: exported `mergeTranscriptTurnVersioned` so the cache shares the reducer's supersedes semantics while preserving held turn items; documented and tested the existing item encode/decode and version-merge helpers.
- `cmd/evener-hub/frontend/src/stores/sessionCacheSchema.ts`: added typed item, item-free turn, coverage, per-history and aggregate metadata rows; a collision-safe `entryHistoryKey`; and fixture-only stores/indexes. `positionSub` is an internal index field, stripped from returned rows.
- `sessionEntryCache.ts`: `commitBatch` returns `committed`, `refused` (`clear-epoch`), or `failed`. It reads only affected item/turn keys and required metadata, then commits item rows, turn rows, coverage and encoded byte totals in one readwrite transaction. It compares the batch's captured clear epoch against the durable legacy metadata row inside that transaction. `putItems` remains as the existing repository's callable compatibility surface and routes through the same transaction.
- `EntryCoverageInput` requires explicit read or accepted-suffix provenance. A read supplies its range, represented snapshot, boundary and optional opaque server cursor. A suffix supplies its read and continuity identities plus previous/current boundaries. `getLatestWindow(historyKey, limit, requestedBoundary)` uses the position index for bounded ascending content and the coverage boundary index for certificate lookup; it returns rows, the matching coverage, boundary/cursor where available, and `complete`/`stale`. Isolated rows remain available through `getLatest` without certifying coverage.
- `sessionCacheIndexedDB.ts` shares the production database-name constant; `sessionCacheCurrent.test.ts` asserts the production database remains version 2 without entry stores.

## Verification

All commands ran from `cmd/evener-hub/frontend` unless noted.

- Required focused suite: `npx vitest run ../../../appwire-client/typescript/reducer.test.ts src/stores/sessionEntryCache.test.ts src/stores/transcriptRepository.test.ts src/stores/sessionCacheCurrent.test.ts src/stores/sessionCacheIndexedDB.test.ts src/stores/threads.sessionCache.test.ts` — exit 0; 6 files and 424 tests passed.
- TypeScript: `npm run typecheck` — exit 0 (`tsc --noEmit --incremental false`).
- Formatter/lint: `node_modules/.bin/biome check` on the exact nine owned TS paths listed below — exit 0 with pinned Biome 2.5.5; checked 9 files, no fixes applied.
- The typecheck and focused suite were rerun after formatting; both passed.

The exact Biome paths were `../../../appwire-client/typescript/reducer.ts`, `../../../appwire-client/typescript/reducer.test.ts`, `../../../appwire-client/typescript/index.ts`, `src/stores/sessionCacheSchema.ts`, `src/stores/sessionEntryCache.ts`, `src/stores/sessionEntryCache.test.ts`, `src/stores/testing/entryCacheFixture.ts`, `src/stores/sessionCacheIndexedDB.ts`, and `src/stores/sessionCacheCurrent.test.ts`.

## Self-review and remaining boundaries

The writer calculates byte deltas from serialized stored rows and updates both history and aggregate scalar totals in the same transaction. Real IndexedDB event holds prove the batch is not visible before commit, an abort leaves item/coverage/totals unchanged, and a successful commit publishes them together. Instrumented request tests establish that append and old-item completion use affected-key requests, not a full-history read. The bounded query returns rows in ascending position order and marks them stale unless the requested boundary has a matching certificate containing the returned positions.

Coverage is only as authoritative as caller-supplied read/suffix provenance. The next ingestion unit must supply genuine server cursor, represented snapshot and accepted-input continuity, and must report abandoned suffix work so it cannot be bridged. This unit does not own repository publication or overlay behavior, migration, aggregate admission/eviction, browser proof, or Task 2/3/4/6 work; none is claimed complete here. `getLatestWindow` reads only the one indexed boundary certificate selected by its request, not a reusable tab-local currentness marker.

## Changed paths

- `appwire-client/typescript/README.md`
- `appwire-client/typescript/index.ts`
- `appwire-client/typescript/reducer.test.ts`
- `appwire-client/typescript/reducer.ts`
- `cmd/evener-hub/frontend/src/stores/sessionCacheCurrent.test.ts`
- `cmd/evener-hub/frontend/src/stores/sessionCacheIndexedDB.ts` (shared schema/name extraction only)
- `cmd/evener-hub/frontend/src/stores/sessionCacheSchema.ts`
- `cmd/evener-hub/frontend/src/stores/sessionEntryCache.test.ts`
- `cmd/evener-hub/frontend/src/stores/sessionEntryCache.ts`
