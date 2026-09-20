== panel f796f03d-6ef0-4987-9a33-39b1e34f6605 head e4f03c86f outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=21138 verdict=0 chars=1457
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=21139 verdict=1 chars=157
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=21140 verdict=0 chars=4193

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile-native/src/NativePreferencesProvider.tsx:40-62`; `mobile-native/src/KeybindingPreferencesScreen.tsx:91-169`  
  **Problem**: The new offline recovery flags and discard action are never consumed by the keybinding screen. With an unreadable draft while offline, `domain` is null, so the screen only shows the loading indicator and provides no way to discard the corrupted record.  
  **Fix**: Render the unreadable-draft recovery state from `offlineDraftUnreadable` and invoke `discardUnreadableKeybindingsDraft`, including appropriate handling for storage failures.

---

- **Severity**: Low  
  **Location**: `mobile-native/src/NativePreferencesProvider.tsx:144-152`  
  **Problem**: `"refused"` is used to preserve `offlineDraftUnreadable`, but `discardStoredKeybindingDraft` also returns `"refused"` when its initial read finds a readable replacement. If the follow-up read then fails, the provider incorrectly retains the unreadable-draft notice despite having just observed a readable record.  
  **Fix**: Preserve the classification from the successful initial read, or return enough information from the discard helper to distinguish a readable refusal from a CAS refusal.

## Summary

The change adds native offline draft probing and store-free discard plumbing, but the recovery action is not wired into the user-facing keybinding screen and has an edge-case classification error.

######## member 1 (codex default)
No issues found.
Summary: Adds offline unreadable-draft probing and store-free discard recovery to `NativePreferencesProvider` with provider-level coverage.


######## member 2 (pi default)
1. **Summary**

The two commits add provider-level recovery for a corrupt offline keybindings draft. The production draft backend is refactored to `rawStringDraftBackend` (the same implementation the tests exercise), a `useEffect` in `NativePreferencesProvider` independently probes the hub's stored record on `hubId`/connection-`state` changes (so a cold offline start can surface an unreadable record without a live model), and a new `discardUnreadableKeybindingsDraft()` performs the store-free discard, re-reads the record afterwards, and nudges a live model's own `discardKeybindingsDraft()` so its store reclassifies through `keybindingsStore`'s existing recovery seam. `draftUnreadableAfterDiscard` and unit tests for both the helper and the provider are added.

---

2. **Findings**

**low — New offline recovery API has no production consumer**
File: `mobile-native/src/NativePreferencesProvider.tsx:40-62` (context value at `:232-234`); consumer `mobile-native/src/KeybindingPreferencesScreen.tsx`
`offlineDraftUnreadable`, `offlineStorageUnavailable`, and `discardUnreadableKeybindingsDraft` are referenced only by the provider and `NativePreferencesProvider.test.tsx`; the settings screen still exposes discard only through a live `model`. From the exact case these fields were built for — a cold offline start with a corrupt draft, where `snapshot` is `null` and no model exists — nothing renders the "Discard unreadable draft" action the field docs promise, so the new path is unreachable by a user. Suggested fix: consume the fields in `KeybindingPreferencesScreen` (render the action when `offlineDraftUnreadable`, call `discardUnreadableKeybindingsDraft()`, and surface `offlineStorageUnavailable`).

---

**low — Doc claims a snapshot field that does not exist**
File: `mobile-native/src/NativePreferencesProvider.tsx:44-45` (referencing `mobile-native/src/nativePreferences.ts:144-164`)
The comment says the offline flag is "Superseded by `snapshot.keybindings.draftUnreadable` the moment a model publishes," but `PreferenceState` and `keybindingsDomain` never carry `draftUnreadable` — the store's unreadable classification is dropped before it reaches any snapshot. A caller following the comment will look for a field that cannot exist, and the live unreadable signal is silently unavailable. Suggested fix: either add `draftUnreadable` to `PreferenceState`/`keybindingsDomain`, or correct the comment to say the offline flag is the only signal.

---

**low — Follow-up read failure guesses the unreadable flag from a refusal**
File: `mobile-native/src/NativePreferencesProvider.tsx:149-152`
`setOfflineDraftUnreadable(outcome === "refused")` is reached when `discardStoredKeybindingDraft` returned `"refused"` (which can mean the current record *decodes as readable* — see `discardCheckpointPort.ts`'s `isReadable` branch) and the subsequent `readDraftOutcome` then failed. In that window the code sets the unreadable notice `true` even though the only known fact is that the record was readable moments earlier. This contradicts the flag's own documented invariant (`:49-51`: when storage is unavailable nothing about `offlineDraftUnreadable` is known, "it is left at whatever it last was rather than guessed at") and can leave a bogus recovery action up. Suggested fix: leave the previous value (or set it `false`) in that branch rather than deriving it from `outcome`.

---

**low — No coverage for the `state`-change re-probe**
File: `mobile-native/src/NativePreferencesProvider.test.tsx`
The effect deliberately depends on `state` (`NativePreferencesProvider.tsx:124`, comment at `:97-100`) so a record that becomes corrupt while offline "would otherwise never surface ... until `hubId` itself changed." No test exercises that path: the suite covers corruption discovered at mount, storage failure, and a hub switch, but never a same-hub `state` change after a readable record turns unreadable. Add a test that mounts with a readable draft and a stable hub, overwrites the record with unreadable bytes, flips `harness.connection.state`, rerenders, and asserts `offlineDraftUnreadable === true` (this exercises the `state` dependency, which is otherwise unverified).
