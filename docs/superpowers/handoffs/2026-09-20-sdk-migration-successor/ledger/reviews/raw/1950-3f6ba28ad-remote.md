== panel 150409b4-5c52-4632-8729-4ddf12af5af7 head 3f6ba28ad outcome  synthesis 
-- member 0 codex/gpt-5.6-luna type=default status=done job=20864 verdict=0 chars=681
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20865 verdict=0 chars=2427
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=20866 verdict=0 chars=4256
-- member 3 codex/glm-5.3-vision-background type=default status=running job=20867 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
  **Location**: `mobile-native/src/NativePreferencesProvider.tsx:35-60`
  **Problem**: Production still uses the inline backend instead of `rawStringDraftBackend`. Malformed stored JSON therefore throws as `storageUnavailable` rather than becoming a removable unreadable draft, and canonical identity matching is not applied.
  **Fix**: Construct the provider backend with `rawStringDraftBackend(Storage, () => Crypto.randomUUID())` and remove the duplicate inline implementation.

## Summary

The change adds shared draft decoding and raw-storage handling, but the new backend is only exercised by tests and is not wired into production.

######## member 1 (codex default)
## Review Findings
- **Severity**: High
- **Location**: `mobile-native/src/NativePreferencesProvider.tsx:32-59`, `mobile-native/src/nativePreferenceDrafts.ts:142`
- **Problem**: Production still uses `Storage.getItemSync(key) === JSON.stringify(value)` and `JSON.parse`, while tests use new `rawStringDraftBackend` with `matchesStoredBytes`/`canonicalJson`/`parseDraftBytes`. Key-order delete, unparseable bytes, and stored `null` behave differently in prod (strict-equality refuse, throw, conflate with absent) than in tests.
- **Fix**: Delegate the provider backend to `rawStringDraftBackend` over `Storage` and delete the duplicated `matches`/`get`/`deleteIf`/`replaceIf` logic.
---
- **Severity**: Medium
- **Location**: `appwire-client/typescript/keybindingsStore.ts:432`
- **Problem**: `decodeKeybindingDraftFields` returns `{draft: null, writeUncertain: false}` for invalid values, identical to absent. Called without prior `classifyDraftRead`/`isReadableKeybindingDraft`, an unreadable record silently reads as no draft.
- **Fix**: Throw on invalid, return an outcome union, or accept only `KeybindingDraftCheckpoint | null` so invalid cannot be passed.
---
- **Severity**: Medium
- **Location**: `mobile-native/src/nativePreferenceDrafts.ts:19`, `mobile-native/src/nativePreferenceDrafts.ts:44`
- **Problem**: Exported `StoredNullRecord`/`UnparseableDraftBytes` reference non-exported `unique symbol` brands. External code cannot name the brand, declaration emit uses a private name, and `structuredClone`/`canonicalJson` in `draftBackend.testkit.ts` strips symbol keys so marker identity is lost there.
- **Fix**: Export the brands or make markers opaque, and make the fake backend compare via `draftIdentityKey`/`matchesStoredBytes` instead of plain `canonicalJson`.
---
- **Severity**: Low
- **Location**: `appwire-client/typescript/keybindingsStore.test.ts:244`
- **Problem**: Decode test passes raw bytes string `"{not json"`, which `load()` never returns (`load()` returns an `UnparseableDraftBytes` marker). Real invalid shapes are uncovered.
- **Fix**: Assert `decodeKeybindingDraftFields` on `parseDraftBytes("{not json")`, `parseDraftBytes("null")`, and a wrong-shaped object maps to no draft.
## Summary
Shares draft-field decoding and single-read classification plus a canonical raw-string backend, but leaves production on the old strict-equality/throwing backend with a silent invalid-as-absent decoder.


######## member 2 (pi default)
## Summary

The series merges current `main` into the offline-draft-storage branch and adds one feature commit, `feat(native): share draft classification and decoded fields`. That commit:

- Exposes `decodeKeybindingDraftFields` from `keybindingsStore.ts` (and the package root), factoring the checkpoint→`{draft, writeUncertain}` projection out of `restoreDraft` into a shared `draftFieldsFrom` helper so a store-free/offline caller can decode a readable record the same way the live store does.
- Replaces the string-tagged `StoredNullRecord` / `UnparseableDraftBytes` sentinels in `nativePreferenceDrafts.ts` with `unique symbol` brands, on the argument that `JSON.parse` can never synthesize a symbol-keyed property.
- Adds `readDraftOutcomeWithValue` (one read, returning both classification and raw value) and reimplements `readDraftOutcome` on top of it.
- Adds `rawStringDraftBackend` plus tests that exercise it over a Map-backed `RawStringStorage`.

The refactors in `keybindingsStore.ts` and the `readDraftOutcome*` pair are behavior-preserving and correct. The two concerns below are about the new backend/test sharing claim and the weakened sentinel identity in canonical-JSON comparison paths.

---

**Medium** — `mobile-native/src/nativePreferenceDrafts.test.ts:26-31` (and `mobile-native/src/nativePreferenceDrafts.ts:140-142`)

The new test comment asserts that `rawStringDraftBackend` "is the same function production calls over it, not a parallel reimplementation," but production does not call it. `NativePreferencesProvider.tsx:30-56` still constructs its own inline backend whose `get()` uses `JSON.parse` (not `parseDraftBytes`) and whose `matches()` is a byte-for-byte `Storage.getItemSync(key) === JSON.stringify(value)` compare (not `matchesStoredBytes`/canonical JSON). `grep` shows `rawStringDraftBackend` is referenced only from the test file. The consequence is that the newly advertised coverage — malformed bytes classified as an unreadable record, stored JSON `null` as a present record, and a key-order-changed record matching on `removeIf` — validates the helper, not the backend the app actually runs. In production those cases still behave differently: malformed bytes make `get()` throw and get reported as `storageUnavailable`, and a key-order-different record refuses `deleteIf`. The suite can stay green while the production draft port lacks the unreadable-record recovery the tests purport to lock down. Fix: wire `NativePreferencesProvider`'s `backend` through `rawStringDraftBackend(Storage, Crypto.randomUUID)` and delete the inline copy (the shared helper already handles the transcript `StoredNullRecord` case), or, if that wiring is a follow-up, correct the comment and add a test that exercises the production backend rather than a stand-in.

---

**Low** — `mobile-native/src/nativePreferenceDrafts.ts:29-48`

Replacing the string `kind` tag with a `unique symbol` brand makes the sentinels invisible to `canonicalJson` and `structuredClone`, both of which only see string-keyed own properties. `canonicalJson(STOPPED_NULL_RECORD)` is now `{}` and `canonicalJson(unparseableMarker)` is `{"raw":...}`, so any identity compare that uses canonical JSON rather than `matchesStoredBytes` can no longer tell a sentinel from an ordinary object with the same serializable fields — e.g. `fakeDraftBackend` (`mobile-native/src/draftBackend.testkit.ts:45-48`) and `memoryDraftStorage` (`appwire-client/typescript/testing/draftStorage.ts:65,71`), which the repo relies on to model the real byte-aware port. Likewise `fakeDraftBackend.get`/`set` `structuredClone` a value, which strips symbol keys, so a stored sentinel would come back as a plain `{}`/`{raw}` object and fail `isStoredNullRecord`/`isUnparseableDraftBytes`. No current test routes a sentinel through those doubles, so there is no immediate failure, but the next test that stores one will silently lose its identity (and a canonical-JSON port could match a different record). Fix: keep the symbol brand for the `isReadable` predicates but route the test doubles' compares through the same marker-aware identity function (`draftIdentityKey`) that `matchesStoredBytes` uses, or expose that function so the doubles can use it instead of raw `canonicalJson`.

######## member 3 (codex default)
<no output; error: >
