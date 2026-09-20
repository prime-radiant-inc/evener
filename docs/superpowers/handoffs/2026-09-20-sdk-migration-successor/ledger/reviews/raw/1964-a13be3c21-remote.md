== panel 87101b90-9b7c-4c30-8242-aef4a0fe3a1b head a13be3c21 outcome  synthesis 
-- member 0 codex/gpt-5.6-luna type=default status=done job=20724 verdict=0 chars=1561
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20725 verdict=1 chars=198
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=20726 verdict=1 chars=1680
-- member 3 codex/glm-5.3-vision-background type=default status=running job=20727 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile-native/src/NativePreferencesProvider.tsx:40-65`, `mobile-native/src/nativePreferences.ts:47-181`, `mobile-native/src/KeybindingPreferencesScreen.tsx:167-334`  
  **Problem**: Unreadable drafts cannot be recovered through the UI. The provider exposes recovery state and a discard function, but the keybinding screen never consumes them; its existing discard action only renders when `domain.draft` exists, while unreadable drafts set it to `null`. The `draftUnreadable` state is also omitted from `KeybindingsPreferenceState`.  
  **Fix**: Project `draftUnreadable` through `keybindingsDomain`, then render a recovery/discard action using either the live model or the provider’s store-free discard path when offline.

---

- **Severity**: Medium  
  **Location**: `mobile-native/src/NativePreferencesProvider.tsx:97-102`  
  **Problem**: Retained snapshots classify a newly unreadable record as `storageUnavailable` but leave `draftError` unchanged. If the previous snapshot had a valid draft and no error, the resulting state has no draft and no user-facing error, unlike `restoreDraft`, which reports the draft-restore failure.  
  **Fix**: Set the same draft-restore diagnostic used by the shared keybindings store when the retained record is unreadable, and clear it when the record is absent or readable.

## Summary

The changes add native draft classification and reconciliation plumbing, but the unreadable-draft recovery state is not fully projected or connected to the keybinding UI.

######## member 1 (codex default)
No issues found.
Summary: Shares keybinding draft decoding and offline discard plumbing between the live store and native provider with retained-snapshot reconciliation and diagnostic preservation.


######## member 2 (pi default)
## Summary

The 18-commit series builds offline keybindings-draft recovery for the native app. At the shared-client layer it extracts the old inline `restoreDraft` projection into an exported `decodeKeybindingDraftFields` (`appwire-client/typescript/keybindingsStore.ts`), and it replaces the string-tagged `STORED_NULL`/`UNPARSEABLE` sentinels with symbol brands so stored bytes can no longer parse back into a marker and masquerade as a different identity (`nativePreferenceDrafts.ts`). At the native layer it moves the real `Storage`-backed backend into a shared, test-exercisable `rawStringDraftBackend`, adds `readDraftOutcomeWithValue`/`draftUnreadableAfterDiscard`, and wires `NativePreferencesProvider` with an offline probe plus a store-free `discardUnreadableKeybindingsDraft` that reconciles a retained snapshot and nudges a still-bound live store to reclassify. `nativePreferences.ts` exposes `draftError`/`hubError`/`loadError` so diagnostic ownership survives recovery. Tests cover the invalid-record/readable-replacement/cross-hub/storage-failure paths and the marker-collision regressions.

I reviewed the reconciliation guards (hub/client/state checks), the CAS-based discard paths, the symbol-brand identity comparison, and the diagnostic projection against the live store's `restoreDraft`. The failure branches deliberately preserve `draftError` while the readable/absent branches clear it, consistent with the "preserve/clear diagnostics" intent and the offline flag covering the no-model window; the new marker predicates are correctly un-forgeable by `JSON.parse`, and the added tests exercise real behavior rather than restating constants.

No issues found.

######## member 3 (codex default)
<no output; error: >
