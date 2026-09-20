== panel 5de6f480-6ac8-4faa-9f37-34f5efcf4a18 head 7db6c56bb outcome  synthesis 
-- member 0 codex/gpt-5.6-luna type=default status=done job=20978 verdict=0 chars=738
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20979 verdict=0 chars=1242
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=20980 verdict=0 chars=3661

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
  **Location**: `mobile-native/src/NativePreferencesProvider.tsx:32-62`
  **Problem**: Production still uses the old `JSON.parse` backend and exact `JSON.stringify` comparisons. The new `rawStringDraftBackend` is only exercised by tests, so malformed drafts remain `storageUnavailable` instead of discardable unreadable records, and equivalent JSON with different key order can fail compare-and-swap.
  **Fix**: Build the production backend with `rawStringDraftBackend(Storage, () => Crypto.randomUUID())` and add an integration test through `NativePreferencesProvider`.

## Summary

The change adds shared draft parsing and decoding helpers, but the native production backend does not use them.

######## member 1 (codex default)
## Review Findings
- **Severity**: High
- **Location**: `mobile-native/src/nativePreferenceDrafts.test.ts:28`, `mobile-native/src/nativePreferenceDrafts.ts:142`, `mobile-native/src/NativePreferencesProvider.tsx:31-60`
- **Problem**: Tests claim `rawStringDraftBackend` is the same function production calls, but production still uses an inline backend with different semantics: `get` uses bare `JSON.parse` that throws on malformed bytes and returns `null` for stored `null`, and `matches` uses strict `JSON.stringify` equality. The new backend uses `parseDraftBytes` markers plus `matchesStoredBytes` canonical comparison, so the new key-order removal test and marker-collision tests pass against code production never executes while production retains the key-order mismatch and malformed-bytes-as-`storageUnavailable` behavior.
- **Fix**: Wire `NativePreferencesProvider.tsx` through `rawStringDraftBackend` over `Storage` and remove the divergent inline `matches`/`get`/`set`/`deleteIf`/`replaceIf`, or correct the test comment if the migration is intentionally deferred.
## Summary
Shares draft decode/classification helpers and hardens draft markers against JSON collisions, but leaves production on a divergent backend implementation.


######## member 2 (pi default)
## Summary

The series merges current `main` into the offline-draft-storage branch and adds one feature commit that (a) fixes marker collisions in `nativePreferenceDrafts.ts` by branding the `storedNull`/`unparseable` sentinels with module-private `Symbol`s instead of string-tagged shapes, (b) extracts `rawStringDraftBackend`, `readDraftOutcomeWithValue`, and `decodeKeybindingDraftFields` so callers can share classification/decoding, and (c) points the existing tests at those extracted helpers. The symbol branding is a genuine fix: a stored `{"kind":"...unparseable","raw":"..."}` record previously re-parsed into something `isUnparseableDraftBytes` accepted, and the new tests at `nativePreferenceDrafts.test.ts:329-337` now prove that collision is closed.

## Issues

**Medium — The tests assert the shared backend is production's, but production still runs a different backend with divergent behavior for malformed records.**
`mobile-native/src/nativePreferenceDrafts.test.ts:26-29` (new comment), exercised by `nativePreferenceDrafts.test.ts:60-72`.

The new comment states `rawStringDraftBackend` "is the same function production calls over it, not a parallel reimplementation." That is not true. Production (`NativePreferencesProvider.tsx:30-62`, `nativeKeybindingDrafts`/`nativeTranscriptDrafts` call sites at lines 83/103) still defines its own `backend` whose `get` calls bare `JSON.parse` and whose `matches` compares `Storage.getItemSync(key) === JSON.stringify(value)`; `rawStringDraftBackend` (with `parseDraftBytes` and `matchesStoredBytes`) is used only by tests. The divergence is observable: for a corrupt keybinding record, `rawStringDraftBackend.get` returns the `UnparseableDraftBytes` marker, so `nativeKeybindingDrafts.load()` returns it, `draftCheckpoint` throws `UnreadableDraftError`, and the store/`discardStoredKeybindingDraft` take the unreadable/`"removed"` recovery path the tests assert. Production's `get` instead throws a `SyntaxError` on the same bytes, which propagates to the `catch` in `restoreDraft`/`discardStoredDraft` and is classified `storageUnavailable` — the exact `draftUnreadable`/discard recovery these tests claim to cover never runs in production, and `JSON.parse` failures are never normalized to the marker identity. Fix: wire the provider to `rawStringDraftBackend(Storage, () => Crypto.randomUUID())` (or otherwise make production call the extracted function), or, if the migration is intentionally staged, correct the comment and note that the tests validate only the helper.

---

**Low — The extracted helpers have no in-repo consumer, so the "share" stated in the commit message is not achieved.**
`appwire-client/typescript/keybindingsStore.ts:432` (`decodeKeybindingDraftFields`), `mobile-native/src/nativePreferenceDrafts.ts:142` (`rawStringDraftBackend`), `mobile-native/src/nativePreferenceDrafts.ts:196` (`readDraftOutcomeWithValue`).

A repo-wide search finds these identifiers only in their definitions, the new tests, and the `index.ts` re-export — no production or offline caller uses any of them (the pre-existing `readDraftOutcome`/`classifyDraftRead` likewise have no non-test callers). `decodeKeybindingDraftFields` is now committed to the public `@evener/appwire-client` surface (`index.ts:226`) despite nothing consuming it. If the offline caller this doc comment describes is meant to land in a later change, that is fine, but as standalone the change adds exported, untested-against-production API with no user; if the consumer is supposed to be in this range, the series' goal is incomplete. Fix: land the consumer in the same change, or drop the public export until it is used.

verification_snapshot_utc=2026-09-19T05:33:36Z
