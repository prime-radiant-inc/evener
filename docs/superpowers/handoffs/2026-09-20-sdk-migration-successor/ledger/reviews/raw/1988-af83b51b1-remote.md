== panel cdabb5af-4c06-4847-bb4c-ae878e1d91de head af83b51b1 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=21190 verdict=0 chars=717
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=21191 verdict=1 chars=138
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=21192 verdict=1 chars=2009

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
- **Location**: `mobile-native/src/KeybindingPreferencesScreen.tsx:91-108, 243-334`
- **Problem**: The new `offlineDraftUnreadable`, `draftUnreadable`, and `discardUnreadableKeybindingsDraft` recovery API is never consumed by the screen. A corrupt draft therefore leaves the editor blocked with no discard action, including during a cold offline start.
- **Fix**: Render a discard/recovery action when either unreadable flag is set, invoke the provider’s store-free discard method, and surface storage/refusal outcomes.

## Summary

Adds retained keybinding-draft reconciliation across connection changes, but the new unreadable-draft recovery remains inaccessible to users.

######## member 1 (codex default)
No issues found.
Summary: Retained offline draft reconciliation preserves diagnostics and matches store semantics with thorough coverage.


######## member 2 (pi default)
## Summary

The series hardens the native preferences provider's handling of a locally persisted keybindings draft when the live model is no longer authoritative (connection dropped, hub switched, or a replacement client is still handshaking). It:

- Adds `DraftReadWithValue`/`readDraftOutcomeWithValue` plumbing so the disk record is read once and both classified and decoded from the same snapshot.
- Adds `reconcileRetainedDraftProjection()` to patch a retained `bound` snapshot to match the current on-disk draft (absent / readable / unreadable / storage-unavailable), recomputing `conflict`, `writeUncertain`, `draftUnreadable`, `draftError`, and the derived `error`.
- Wires that reconciliation into a new effect and into the store-free `discardUnreadableKeybindingsDraft()` path.
- Exposes `draftUnreadable` on `PreferenceState` and the raw `draftError`/`hubError`/`loadError` sources on the keybindings domain, with `keybindingsErrorMessage()` centralizing the draft → load → hub precedence.
- Adds extensive provider and model tests covering readable replacements, unreadable classification, storage failures/recovery, discarded/absent records, hub-source preservation, and same-client disconnected reconciliation.

I traced the reconciliation against the shared store's `restoreDraft`/`staleDraft`/`discardDraft` semantics: the `absent`, `readable`, and `unreadable` branches match the store's own projections (including `conflict = draft.revision !== confirmed.revision`), the `storageUnavailable` branch correctly treats the classification as unknown and leaves `draftUnreadable` alone, and the derived `error` is recomputed with the same precedence the live domain uses. The effect's skip condition (`bound.client === client && state === "ready"`) correctly leaves the live store in charge, and the `setBound` guards prevent cross-hub patching. Changes are reflected in the tests, and the harness fixture additions (`failed`, `keybindingsError`) exercise the new failure paths.

No issues found.
