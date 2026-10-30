# Native document memory: preserve unsent comments (#3711)

## Scope and cause

Issue: <https://github.com/prime-radiant-inc/evener/issues/3711>.

This change is limited to `mobile-native/src/reader/documentMemory.ts`, its
`documentMemory.test.ts` tests, and this specification. The native Reader and its
comment, comments and review sheets share one `DocumentMemory` per hub through
`nativeDocumentMemory.ts`. Device storage owns saved comments across app launches.
The native app contract is S03 in `docs/product/subsystems.md`; there is no separate
Reader/document-memory row. No ownership or client boundary changes here.

`update` currently sorts records with comments first but truncates the entire map
to 100 records. With 101 commented documents it deletes authored work, then writes
the loss to storage. The defect is the unconditional total-record cap, not comment
parsing or an explicit clear action.

## Retention rule

A document record with at least one unsent comment is protected from cache
eviction. Every such record, with all comment fields and their order intact,
survives subsequent document writes and constructing a new `DocumentMemory` over
the same successfully written storage. Protection does not depend on insertion
order, timestamp order, distinct timestamps, or refreshing commented documents.

Only the existing deliberate comment-removal paths remove authored comments:
`removeComment` deletes the named comment; `clearComments` clears that document
after review submission. Existing explicit hub removal still forgets its records.
Removing one of several comments does not remove protection. Once no comments
remain, including an empty comments array, the record is disposable again.

## Disposable-metadata bound

Keep at most **100 comment-free document records per hub**, choosing the most
recently touched by the existing `touchedAt` clock. Keep every commented record in
addition to that budget. After each update, if C records hold comments, the map
contains at most C + 100 records and at most 100 have no comments. Equal timestamps
need no new tie-breaking policy; either equally recent disposable record may be
evicted, but a commented one may not.

Retain the existing position and last-read fields alongside protected comments.
They remain one record, not an independent metadata history. This deliberately
exempts whole commented records instead of introducing a separate comment store,
stripping useful reading state from authored work, or allowing disposable metadata
to grow without a bound. Commented documents do not consume the 100-record
disposable cache budget. With no comments, the existing 100-record bound is
unchanged. After the final comment is removed, that record competes normally for
those 100 places and is evicted by later reading activity if old enough.

Use the existing storage keys, record shapes, parsing, synchronous writes and
subscriber notification behavior. No migration or constructor write is needed:
existing retained comments parse as before, and every subsequent update applies
the new policy. Already evicted comments cannot be recovered by this fix.

## Non-goals and preserved behavior

- No annotation-store work, review-comment redesign, new UI, or labels.
- No changes to comment IDs, document identity, working-directory/artifacts root
  separation, per-hub isolation, or review submission/removal behavior.
- No changes to reading-position restoration, last-read freshness, or the separate
  two-hour Continue reading trail.
- No new persistence retry, quota handling, corruption repair, concurrent-owner
  protocol, or recovery guarantee when device storage itself fails. Existing
  best-effort `writeJson` behavior remains unchanged.
- No evergreen documentation changes outside the allowed files. Explain the
  durable retention rule at the eviction implementation itself.

## Verification

Use the real `DocumentMemory` with the existing `memoryStorage` external-storage
fake and an injected deterministic clock. Do not mock eviction, parsing or update.

1. **Red-first restart regression:** add authored comments to at least 101 distinct
   document keys. Keep an independent expected full comment array for every key.
   Never refresh an earlier commented key while writing later keys. Construct a
   fresh owner over the same storage and compare every comment field and order.
   Run forward, reverse and interleaved key orders with both increasing and tied
   timestamps. Then churn more than 100 unrelated reading records without touching
   commented documents, restart again and repeat all comment assertions. Observe
   this regression fail on the pre-fix source, then pass with the fix.
2. **Separate disposable-cache test:** write more than 100 comment-free records
   using both position and last-read metadata. Assert exactly 100 disposable
   records persist, the least recent ones are absent after restart, and the newest
   100 retain their fields. Run with no protected records and with more than 100
   protected records, checking the disposable budget does not depend on C. Include
   a recency refresh that makes write order differ from first insertion order.
3. **Protection lifecycle:** cover removing only one of several comments, removing
   the final comment, and clearing all comments. After subsequent cache pressure
   and restart, surviving comments remain exact and comment-free records are
   eligible for eviction. Empty arrays receive no exemption.
4. Run the complete `documentMemory.test.ts` file through the native Vitest runner,
   `npm run check` in `mobile-native`, and Biome only on the touched native files.
   `npm test` runs more than this file, so use the documented underlying runner:
   `PATH="$PWD/node_modules/.bin:$PATH" sh ../scripts/native/run-tests.sh src/reader/documentMemory.test.ts`.
5. Run `make build` from the repository root because production app source changed.
   It builds the repository's web/Go app artifacts, not an iOS device binary. The
   full native test and Metro bundle lanes belong to CI; do not run the full native
   suite locally or claim simulator/device qualification.

Before implementation, freeze this specification and review it with two
independent competing PAR reviewers. Review the immutable implementation diff in
another PAR round, simplify it, commit, and run branch RoboRev. Merge current main
before the specification and again before final push if it moved. One push starts
one review/CI round. Merge only with all required checks green for the exact head
and all RoboRev findings low or conclusively refuted. Verify GitHub's merged state,
merge SHA and intended content before cleanup.
