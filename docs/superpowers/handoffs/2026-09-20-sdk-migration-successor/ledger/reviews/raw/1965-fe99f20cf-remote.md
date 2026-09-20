== panel e7185fb6-25ad-4e6c-9a1a-21c18d04e526 head fe99f20cf outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=20966 verdict=1 chars=142
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20967 verdict=1 chars=182
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=20968 verdict=1 chars=1458

######## member 0 (codex default)
No issues found.

Summary: The changes align native SQLite deserialization with the mutation storage contract and expand conformance coverage.

######## member 1 (codex default)
No issues found.
Summary: Aligns native SQLite decoding with the web adapter shapes and extends conformance coverage for attachments, identity, attempt evidence, and target scoping.


######## member 2 (pi default)
## Summary

The series closes out conformance gaps in the native mutation outbox SQLite adapter and aligns two read-shape behaviors with the web IndexedDB oracle:

- `mutationOutboxStorage.ts`: `fromRow` now maps a persisted JSON `null` `optimistic_display` back to `undefined` (matching how an absent display is represented in the port's in-memory records), and omits `attempted` entirely for `accepted` records (matching the oracle's `settleReceipt`, which builds its accepted record from an explicit field list with no `attempted`). Comments were trimmed of stacked-PR/round labels.
- `mutationOutboxStorage.test.ts`: adds a round-trip assertion for the null/undefined display, a new test covering attachment, composer-text, and `getOwnClientId` persistence through the public read path, a test that accepted records carry no attempt evidence, and a stronger `restoreProvenAbsent` target-scoping test; comment blocks were updated.

I verified the omissions are type-correct (`MutationOptimisticRecord` declares no `attempted`) and that no consumer distinguishes `null` from `undefined` for `optimisticDisplay` (`pendingEntries.outboxInput`, `settleReceipt`/`updateRecoveryInput` all use truthiness or `typeof`/`Array.isArray` checks), so the lossy null→undefined round-trip has no functional impact. I also confirmed `attempted` is only read from outbox records (`listOutbox`/`markAttempted`/`markUnknown` paths), which still receive it.

No issues found.

verification_snapshot_utc=2026-09-19T05:33:36Z
