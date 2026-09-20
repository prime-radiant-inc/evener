## PR 1902
== panel 08412ff8-ac55-440d-9d7c-fcbdbe8ff51b head 90812476e outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19972 verdict=0 chars=1265
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19973 verdict=0 chars=2387
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19974 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19975 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
- **Location**: `mobile-native/src/nativePreferences.ts:29-39,144-164`; `mobile-native/src/KeybindingPreferencesScreen.tsx:91-170`
- **Problem**: The new offline probe and discard API are never consumed. `draftUnreadable` is also omitted from the native snapshot projection, so an unreadable draft still leaves the screen locked with no recovery action.
- **Fix**: Project `state.draftUnreadable` through `NativePreferences` and wire the screen to render the recovery action, using the store-free discard path when disconnected.

---

- **Severity**: Medium
- **Location**: `mobile-native/src/NativePreferencesProvider.tsx:159-176`
- **Problem**: The provider always nudges the live model with `discardKeybindingsDraft()`, even when the follow-up read finds a readable record. If the live model has classified that same readable checkpoint, its CAS discard can delete the valid replacement that the store-free path correctly refused to remove.
- **Fix**: Avoid calling the discard operation when the current record is readable; use a non-destructive reclassification/refresh path instead.

## Summary

The change adds offline draft probing, store-free discard support, and safer native draft parsing/CAS identity handling.

######## member 1 (codex default)
## Review Findings
- **Severity**: Medium
- **Location**: `mobile-native/src/NativePreferencesProvider.tsx:85`
- **Problem**: Offline probe, store-free discard branching, `probedHubId` reset, `storageUnavailable` preservation, and live-model nudge guard have no exercised behavior test; only trivial helpers are unit-tested.
- **Fix**: Add plumbing test with scripted storage/provider exercising probe sets `offlineDraftUnreadable`, discard clears it, port-throw sets `offlineStorageUnavailable`, and nudge only fires when `bound.hubId`/`bound.client` match.
---
- **Severity**: Low
- **Location**: `appwire-client/typescript/keybindingsStore.ts:437`
- **Problem**: Exported `decodeKeybindingDraftFields` has no production caller and conflates unreadable with absent to the same null pair, so its doc claim about the store-free path needing it is inaccurate.
- **Fix**: Remove export until a caller needs it, or wire the offline re-read through it and preserve an unreadable signal.
---
- **Severity**: Low
- **Location**: `mobile-native/src/nativePreferenceDrafts.ts:211`
- **Problem**: `readDraftOutcomeWithValue` hands back `value` but production only uses `readDraftOutcome().outcome`; the single-load test covers speculative generality with no in-repo consumer.
- **Fix**: Inline to single-load `readDraftOutcome` only, or add the readable-decode caller that justifies the extra return.
---
- **Severity**: Low
- **Location**: `mobile-native/src/nativePreferenceDrafts.ts:243`, `mobile-native/src/nativePreferenceDrafts.ts:254`
- **Problem**: `draftUnreadableAfterDiscard` and `clearsErrorAfterOfflineDiscard` are one-line predicates with tautological mapping tests that cannot fail for a real bug.
- **Fix**: Inline the comparisons at call sites and drop the dedicated tests, keeping coverage on the provider discard outcome instead.
---
- **Severity**: Low
- **Location**: `mobile-native/src/NativePreferencesProvider.tsx:176`
- **Problem**: Live-model nudge uses fire-and-forget `catch(() => {})`, silently swallowing a genuine `discardKeybindingsDraft` rejection.
- **Fix**: Surface or log the rejection instead of empty catch while keeping the store-free outcome as the return value.
## Summary
Adds offline unreadable-draft probe and store-free discard plumbing with shared backend/parser fixes, but leaves new provider behavior untested and adds unused/trivial abstractions.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1904
== panel 42b11883-fa61-4d6c-864b-0c6bce74fde7 head 3c6436d6f outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19722 verdict=0 chars=1088
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19723 verdict=1 chars=197
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19724 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19725 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile-native/src/keybindingOfflineRecovery.ts:45-47`  
  **Problem**: The unreadable-draft discard action is disabled while `domain.loading` is true, even though discarding is local-only and the store explicitly allows it during hub loading. A hanging or slow refresh can therefore prevent the only recovery action.  
  **Fix**: Do not include `loading` in the discard-disabled condition; only gate on an active save or unresolved write.

---

- **Severity**: Low  
  **Location**: `mobile-native/src/KeybindingPreferencesScreen.tsx:181-186`  
  **Problem**: `offlineStorageUnavailable` is displayed even after a connected model successfully recovers storage, because the flag is only refreshed on connection-state changes and can remain stale.  
  **Fix**: Clear the offline flag when the live model reports successful storage recovery, or only render it while disconnected/no live domain exists.

## Summary

The change adds shared draft CAS storage and unreadable-draft recovery across the SDK and native preferences flow.

######## member 1 (codex default)
No issues found.
Summary: Generic draft checkpoint port with atomic CAS semantics adopted across SDK and native layers, plus offline probe and store-free discard for unreadable keybindings drafts.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1792
== panel 8c43fcd0-66c9-4929-aea3-2cd0208700ef head 41fc2f2e8 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19628 verdict=1 chars=167
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19629 verdict=1 chars=158
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19630 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19631 verdict= chars=0

######## member 0 (codex default)
No issues found.

Summary: The changes add shared draft checkpointing, CAS-safe persistence, unreadable-draft recovery, generation fencing, and native offline support.

######## member 1 (codex default)
No issues found.
Summary: Extracts draft checkpoint storage to a shared CAS-guarded port with offline unreadable-draft recovery across SDK and native layers.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1841
== panel d8c8dfcb-0e71-4f67-8a1d-9df28957f87c head c2afb3836 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19613 verdict=0 chars=1055
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19614 verdict=1 chars=192
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19615 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19616 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
- **Location**: `mobile-native/src/nativePreferenceDrafts.ts:25-97`
- **Problem**: Marker detection is based only on object shape, so a stored marker-shaped JSON object can compare equal to a different malformed/null record. A CAS delete or replace may therefore accept a stale identity and remove or overwrite a newer unreadable record.
- **Fix**: Brand internal markers with a non-serializable unique symbol or preserve and compare the original raw bytes without shape collisions.

---

- **Severity**: Low
- **Location**: `appwire-client/typescript/keybindingsStore.test.ts:1342-1348`
- **Problem**: The regression test relies on a real 200 ms timeout, making the default deterministic test suite timing-dependent and potentially flaky under load.
- **Fix**: Use fake timers or a controlled deferred settlement to verify the gate without wall-clock timing.

## Summary

The change adds shared draft CAS/recovery infrastructure, generation fencing, and native offline recovery for unreadable keybinding drafts.

######## member 1 (codex default)
No issues found.
Summary: Extracts shared draft-checkpoint and hub-generation primitives with atomic CAS semantics and offline unreadable-draft recovery, backed by extensive behavioral tests.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1844
== panel b0754233-0db8-4911-b1cc-70cb7dd7574a head a50ac1a95 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19608 verdict=1 chars=165
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19609 verdict=1 chars=189
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19610 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19611 verdict= chars=0

######## member 0 (codex default)
No issues found.

Summary: The changes add shared checkpointed-draft storage, generation fencing, native offline recovery, and comprehensive race-condition coverage.

######## member 1 (codex default)
No issues found.
Summary: Extracts checkpointed-draft, generation-fence, and native offline-recovery primitives into shared modules with atomic CAS semantics and broad regression coverage.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1845
== panel 11083e8d-41ef-4aff-8363-e10840c54fb7 head 737b7cebd outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19668 verdict=1 chars=155
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19669 verdict=1 chars=187
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19670 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19671 verdict= chars=0

######## member 0 (codex default)
No issues found.

Summary: Adds generation-fenced settings stores, CAS-backed draft persistence, transcript defaults, and native unreadable-draft recovery.

######## member 1 (codex default)
No issues found.
Summary: Extracts shared checkpointed-draft, fence, and hub-generation primitives while adding the transcript display store with offline recovery and full test coverage.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1890
== panel 562eec31-8745-49eb-b15a-62e7b8c2fcbc head bda74b984 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19982 verdict=0 chars=682
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19983 verdict=0 chars=1260
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19984 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19985 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
- **Location**: `appwire-client/typescript/state/extensions/marketplaces.ts:241-250`
- **Problem**: The clone-litter rejection reconciles state outside the store’s generation and `listRevision` fences. A delayed rejection can repopulate state after `reset()`/connection replacement or overwrite a newer mutation/list response with its stale `applied` snapshot.
- **Fix**: Apply this reconciliation through the same generation/revision guard as normal mutations, and add deferred-error tests covering reset and newer responses.

## Summary

Adds typed clone-litter handling across the plugin manager, hub, CLI, and marketplace client store.

######## member 1 (codex default)
## Review Findings
- **Severity**: Medium
- **Location**: `appwire-client/typescript/state/extensions/marketplaces.ts:240-250`
- **Problem**: The litter-path `set()` bypasses the `generation` and `listRevision` fences that `mutate()` enforces. A `removeMarketplace` issued before `reset()` will still publish its stale `Applied` list after the reset, violating the documented reset contract, and can overwrite a newer concurrent mutation's list.
- **Fix**: Capture `issuedIn = generation` before `mutate()`, skip the reconcile when `issuedIn !== generation`, and publish the reconciled list through the revision fence instead of a direct `set()`.
---
- **Severity**: Low
- **Location**: `appwire-client/typescript/state/extensions/marketplaces.ts:101-103`
- **Problem**: `cloneLitterApplied` returns `data.applied?.marketplaces` without validating it is an array. A wire `null` for `marketplaces` passes the caller's `!== undefined` check and sets `marketplaces` to `null`, conflating an empty list with the not-loaded state.
- **Fix**: Return the list only when `Array.isArray`, otherwise return `undefined`.
## Summary
Reorders marketplace removal to save-then-clean with typed litter errors across all boundaries, with two client reconcile robustness gaps.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1897
== panel 967500dc-2241-4edc-bc9b-0582a621c0fb head d3c099449 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19977 verdict=0 chars=1129
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19978 verdict=0 chars=1292
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19979 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19980 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `appwire-client/typescript/state/extensions/marketplaces.ts:241-251`  
  **Problem**: The clone-litter rejection directly updates state outside the list-revision and generation fences. A stale rejection can overwrite a newer mutation/refetch or repopulate state after `reset()`, and can retire a newly loaded browse cache.  
  **Fix**: Apply the authoritative list through the same revision/generation-aware publication path as successful mutations.

---

- **Severity**: Medium  
  **Location**: `appwire-client/typescript/state/extensions/marketplaces.ts:249-250`  
  **Problem**: Reconciliation updates only `marketplaces` and `browseCatalogs`, leaving `marketplacesError` and `marketplacesLoading` stale. A previous list error can therefore keep the UI on “Failed to load” despite receiving a valid applied list.  
  **Fix**: Reconcile all list state fields atomically, clearing the error and loading flag when the applied response is accepted.

## Summary

The change adds typed clone-litter removal errors, client reconciliation, and plugin-store path scrubbing.

######## member 1 (codex default)
## Review Findings
- **Severity**: Medium
- **Location**: `appwire-client/typescript/state/extensions/marketplaces.ts: cloneLitterApplied` and `removeMarketplace` catch
- **Problem**: The litter reconcile does `return data.applied?.marketplaces` without validating it is an array. A JSON `null` list passes the caller's `applied !== undefined` check and is written into `marketplaces`, whose type is `MarketplaceEntry[] | null` where `null` means never-loaded.
- **Fix**: Guard with `Array.isArray`, returning `undefined` for non-array payloads so nothing is reconciled from an unusable `Applied`.
---
- **Severity**: Medium
- **Location**: `appwire-client/typescript/state/extensions/marketplaces.ts: removeMarketplace` catch `set`
- **Problem**: `writeRevisioned` retracts the failed write's revision, then the catch writes `marketplaces` with an unfenced `set`. A newer live fetch or mutation can be clobbered when the litter rejection lands after it.
- **Fix**: Route the reconciled list through the `listRevision` fence or gate it on no newer live revision instead of an unconditional `set`.
## Summary
Adds applied-with-litter handling for marketplace removal across manager, hub, CLI, and web store, but the client reconcile bypasses ordering protection and trusts non-array payloads.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1906
== panel cad49ed1-b17a-40a3-9e0a-17a628c5c6fd head c4761e308 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19859 verdict=0 chars=867
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19860 verdict=0 chars=1240
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19861 verdict=1 chars=2219
-- member 3 codex/glm-5.3-vision type=default status=failed job=19862 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
- **Location**: `agent/session_lifecycle.go:1345-1360`; terminal handlers in `agent/session_model_call.go:869` and `agent/session_tool_round.go:38-50`
- **Problem**: The pending-aware state fix only runs in the outer non-provider error path, but provider failures and exhausted no-tool retries settle the session to `SessionIdle` internally first. A human-note carrier leaves `askPending` intact, so live state becomes idle while restore correctly derives awaiting with the pending question.
- **Fix**: Centralize terminal-state selection and use it in these inner failure paths, preserving `SessionAwaiting` whenever pending asks survive.

## Summary

Adds provenance-aware ask-boundary restoration and pending-question accumulation, but misses some terminal error paths that can leave live and restored state inconsistent.

######## member 1 (codex default)
## Review Findings
- **Severity**: Medium
- **Location**: `agent/session_tools_ask.go` in `roundEntryResolvesAskBoundary`
- **Problem**: The backward entry walk stops at the first `TurnUserInput`/`TurnSteering`/`TurnFailure`, so a non-carrier `TurnFailure` interleaved between the real entry and the round's `TurnAssistant`/`TurnToolResults` chain is misread as the entry itself and returns false even when the real entry resolves.
- **Fix**: Treat only `TurnUserInput`, `TurnSteering`, or a resolving carrier `TurnFailure` as terminal; skip non-carrier `TurnFailure` as transparent bookkeeping like the outer scan does.
---
- **Severity**: Low
- **Location**: `agent/session_tools_ask.go` in `clearAskPendingForResolvingSteer`
- **Problem**: The live clear calls `steeringAnswersAsk` with empty `textEvidence` while `turnResolvesAskBoundary` passes `turn.Message.Text()`, so a kindless provenanceless note-shaped steer clears live but does not resolve on restore.
- **Fix**: Pass the turn's text to `steeringAnswersAsk` in the live clear to match the restore predicate.
## Summary
Unifies live and restore ask-boundary handling with provenance scoping and accumulation, with one entry-walk edge case around non-carrier failures remaining.


######## member 2 (pi default)
## Summary

The series closes a set of restore/live divergences around `ask_user` pending state. The final aggregate change:

1. Factors a single `turnResolvesAskBoundary` used by both `deriveRestoredState` and `deriveRestoredAskPending`, so the two scans share the same notion of a "resolving" turn (a `TurnUserInput`, a resolving user `TurnSteering`, an `SteeringKindInterrupted` marker, or a `TurnFailure` tagged `SteeringCarrier`).
2. Collapses the live predicates (`clearAskPendingForResolvingSteer`, `steeringCarrierClaimAnswersAsk`) and the restore scan onto one `steeringAnswersAsk`, which reads steering provenance via `steeringOriginForTurn`/`isHumanNoteSteer`, including the write-path text-shape fallback.
3. Scopes provenance lookup to the session's own turns via the shared `steeringOriginBoundary`, so a forked child's reused client-mutation id can't reclassify an inherited parent turn.
4. Makes `deriveRestoredAskPending` accumulate ask_user questions across consecutive non-resolving carrier rounds (oldest-round-first, call order preserved), matching live's append-not-replace semantics, and only treats a generic completion as decisive when the round's own entry actually resolved the ask.
5. Fixes the live generic-failure boundary so a failed human-note carrier settles `SessionAwaiting` (not `SessionIdle`) while a question is still pending, and samples state/pending atomically via `awaitingOrHasPendingAsk`.

I traced the backward scans across the boundary cases (replies, accepted steers, interrupts, human notes, failed carriers, fork prefixes, multi-round accumulation, compaction/round-entry-walk) and verified `deriveRestoredState` and `deriveRestoredAskPending` cannot disagree in the direction that would yield `SessionIdle` with a non-empty pending set: any turn `deriveRestoredState` treats as await-worthy is either a decisive ask round (which accumulates) or a generic completion deriveRestoredAskPending continues past, yielding `Awaiting` with the recovered set. The new tests drive production paths (real `ProcessPendingUserInput`, `acceptSteeringCarrierInput`, streaming cancellation, journal-backed fork prefixes) rather than hand-built turns where it matters.

No issues found.

######## member 3 (codex default)
<reviewer unavailable>


## PR 1907
== panel 324912f6-a252-4ebe-866e-9fe00bb5836b head fd069ec96 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19854 verdict=1 chars=179
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19855 verdict=1 chars=244
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19856 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19857 verdict= chars=0

######## member 0 (codex default)
No issues found.

Summary: The changes align live and restored ask-boundary handling, steering provenance, failure tagging, and carrier cleanup with extensive regression coverage.

######## member 1 (codex default)
No issues found.
Summary: Restores ask-pending and session state from the same live-path boundaries with provenance-scoped derivation, panic-safe carrier cleanup, and awaiting-preserving failure handling, covered by live-session restore tests.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1915
== panel 425a8d6b-dd0e-4ae1-ad5c-dacc5b972e0c head b689acb3b outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19999 verdict=0 chars=1833
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20000 verdict=0 chars=624
-- member 2 pi/deepseek-4.1-flash type=default status=done job=20001 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=done job=20002 verdict=0 chars=2314

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile-native/src/PluginsScreen.tsx:107-131`, `mobile-native/src/MarketplaceBrowser.tsx:65-78`  
  **Problem**: The extension stores are never given `connectionChanged(client, state)`, so they cannot refetch lists after a reconnect. Plugin and marketplace data can remain stale after a flap.  
  **Fix**: Propagate the client and every connection-state transition to both stores, and coordinate the initial fetch with readiness.

---

- **Severity**: Medium  
  **Location**: `mobile-native/src/PluginsScreen.tsx:129-136`, `mobile-native/src/MarketplaceBrowser.tsx:76-83`, `mobile-native/src/HubSettingsScreen.tsx:161-166`  
  **Problem**: A newly created client is published while still `connecting`, but these mount/focus effects immediately issue requests. AppWire rejects them, and no ready-transition retry is scheduled, leaving screens in an error or empty state until manual refresh.  
  **Fix**: Defer initial reads until ready and retry them when the connection reaches ready.

---

- **Severity**: Medium  
  **Location**: `mobile-native/src/HubSettingsScreen.tsx:199-201`, `mobile-native/src/PluginsScreen.tsx:165-176`, `mobile-native/src/ProvidersScreen.tsx:190-199`  
  **Problem**: Confirmation callbacks capture `ready` from the render that opened the alert. If the connection drops before confirmation, the callback still starts the operation; for upgrades this writes a pending checkpoint before the rejected RPC, falsely leaving the upgrade uncertain.  
  **Fix**: Recheck current readiness inside deferred confirmation callbacks, rather than relying on the captured boolean.

## Summary

The change adds reconnect-aware screen banners and readiness gating, but misses store recovery wiring, ready-transition reads, and deferred-action readiness checks.

######## member 1 (codex default)
## Review Findings
- **Severity**: Low
- **Location**: `mobile-native/src/PluginsScreen.tsx:161`
- **Problem**: `remove()` checks `if (!selected || busy)` and shows the remove confirmation `Alert` without checking `ready`, while `MarketplaceBrowser.tsx` `remove()` and `ProvidersScreen.tsx` `confirm()` both bail when `!ready`.
- **Fix**: Add the same readiness guard, e.g. `if (!selected || busy || !ready) return;`, so a disconnected screen cannot present the destructive confirmation.
## Summary
Keeps ready-only mobile screens mounted behind a reconnect banner and gates mutations and refreshes on connection readiness.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
## Review Findings
**Severity**: High  
**Location**: `mobile-native/src/PluginsScreen.tsx:129` (also `mobile-native/src/MarketplaceBrowser.tsx:76`, `mobile-native/src/HubSettingsScreen.tsx:161`)  
**Problem**: Keeping these screens mounted removes their previous remount-on-ready refresh, but the retained stores are never told about reconnection. The plugin and marketplace stores expose `connectionChanged`, yet the screens never call it; HubSettings’ focus effect also ignores `ready`. A passive flap can therefore leave stale data, and a manual retry can recreate a store while the replacement client is still connecting, leaving its initial read failed until the user manually retries.  
**Fix**: Feed the current client and connection state into the plugin and marketplace store lifecycles, and refresh HubOverview/upgrade state on ready transitions. Delay initial reads until ready, and add a ready-after-flap integration test asserting the stores re-read through the new connection.

---

**Severity**: Medium  
**Location**: `mobile-native/src/HubUpgradeSection.tsx:54`  
**Problem**: The confirmation alert captures the render-time `onStart` wrapper. If the connection drops while the alert remains open, confirming still invokes `upgrade.start()` with stale readiness; the controller writes a pending checkpoint before the disconnected request rejects, creating a spurious uncertain upgrade state.  
**Fix**: Check live readiness inside confirmation callbacks using a ref or current-state predicate, and add a test that disconnects between opening the alert and confirming. Consider also validating readiness before the upgrade controller writes its checkpoint.

---

**Severity**: Low  
**Location**: `mobile-native/src/pluginMutationGate.ts:86`  
**Problem**: `runGatedMutation` collapses “not ready” into the existing `"refused"` outcome, so a disconnected programmatic mutation is displayed as `PLUGIN_MUTATION_BUSY` even though no mutation is running.  
**Fix**: Return a distinct not-ready outcome or a refusal reason, then map it to connection-aware copy or a no-op rather than the busy message.

## Summary
The change keeps provider, plugin, and hub-setting screens mounted through nonfatal connection flaps, adds fatal protocol-close handling, and gates their network affordances on readiness.


## PR 1922
== panel cc4e11d3-1b18-4241-a99b-25ed2af5de99 head 7d5d68b09 outcome  synthesis 
-- member 0 codex/gpt-5.6-luna type=default status=done job=20199 verdict=0 chars=1336
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20200 verdict=0 chars=1410
-- member 2 pi/deepseek-4.1-flash type=default status=done job=20201 verdict=0 chars=3561
-- member 3 codex/glm-5.3-vision type=default status=running job=20202 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
- **Location**: `mobile-native/src/connectionDisplay.ts:62-74`; confirmation callbacks in `ProvidersScreen.tsx`, `PluginsScreen.tsx`, and `HubSettingsScreen.tsx`
- **Problem**: `whenReady` and `runGatedMutation` capture `ready` during render. An alert opened while connected can be confirmed after a disconnect, invoking writes or `upgrade.start()` with stale `ready=true`; upgrades can even persist a pending checkpoint before the request fails.
- **Fix**: Re-check live connection state inside delayed callbacks and at the operation boundary, including all upgrade and mutation paths.

---

- **Severity**: Medium
- **Location**: `mobile-native/src/connectionDisplay.ts:95-107`; `PluginsScreen.tsx:144-146`
- **Problem**: After a fatal close, retry clears `fatal` before the replacement client is ready, while `useRenderClient` still returns the closed previous client. The screen changes back to a banner and remounts children whose mount effects immediately fetch through that closed client.
- **Fix**: Keep the fatal wall or clear the retained client until the new connection reaches `ready`; gate mount-time fetches on readiness.

## Summary

Adds reconnect banners, fatal connection walls, retained clients, readiness-gated mutations, and reconnect recovery for native settings screens.

######## member 1 (codex default)
## Review Findings
- **Severity**: Medium
- **Location**: `mobile-native/src/MarketplaceBrowser.tsx:132`, `mobile-native/src/PluginsScreen.tsx:163`
- **Problem**: `runGatedMutation` returns `refused` for both gate-busy and `!ready`, and both callers map `refused` to `PLUGIN_MUTATION_BUSY` (“Another change is still running”). A write refused only because the connection flapped therefore shows busy copy instead of connection copy or no error.
- **Fix**: Distinguish not-ready from busy — early-return before touching error state when `!ready` like `ProvidersScreen.tsx` `act()` does, or add a separate outcome for not-ready.
---
- **Severity**: Low
- **Location**: `mobile-native/src/PluginsScreen.tsx:177`
- **Problem**: `remove()` guards only `if (!selected || busy)` before opening the destructive `Alert`, while `MarketplaceBrowser.tsx:151` and `ProvidersScreen.tsx` `confirm()` guard `!ready` before alerting. The disabled button covers the normal path, but the missing guard leaves the Alert reachable on a race and its confirm then reports busy via the issue above.
- **Fix**: Add `|| !ready` to the early return in `remove()`, matching `MarketplaceBrowser.tsx`, and re-check readiness in the Alert confirm callback before calling `act()`.
## Summary
Keeps provider, plugin, marketplace, and hub-settings screens mounted behind a reconnect banner with readiness-gated mutations and fatal-close walls.


######## member 2 (pi default)
## Summary

The series makes connection flaps survivable on the native screens instead of dropping them to a full-screen "connect to …" wall. It extracts the wall/banner decision and readiness predicate into a new `connectionDisplay.ts`, adds a `fatal` flag to `hubConnection` (only a protocol close is non-retryable), threads it through `ConnectionProvider`, and wires store reconnect recovery via `connectionChanged` (plugins/marketplaces) or `useReconnectRecovery` (hub overview). Mutations across Providers/Plugins/Marketplace/HubSettings are gated on `ready` and `runGatedMutation` refuses before touching the wire. Tests are added for the display logic, fatal classification, and flap recovery. Net intent is achieved.

---

**Medium — Providers list mounts while the credential store is unbound, producing an unhandled rejection after a fatal-close retry**

File: `mobile-native/src/ProvidersScreen.tsx:81` (guard at `:71`), with `model.start()` at `:139`.

`ProvidersScreen` now renders `<Providers store={store} ready={ready} …>` whenever `display !== "wall"` (line 81). `store` comes from `useCredentialStore()`, which binds `connectionChanged(client, state)` with the *raw* context client. During the retry gap after a fatal protocol close, `client` is `null` and `state` is `"connecting"`, so the store is explicitly unbound — but `display` is `"banner"` (`everReady` true, `fatal` cleared by the retry effect), so `<Providers>` is *remounted* (it had unmounted at the wall) and its mount effect calls `model.start()`.

`ProviderInstances.start()` (providerInstances.ts) sees `listingEstablished === true` but `listingFromPreviousConnection === true`, so it calls `refresh()` → `core.getState().fetch()`. The credentials core's `readListing` calls `requireClient()` first and deliberately throws `"credentials store: no client connected…"`; because `refresh()` is invoked as `void this.refresh()`, that rejection is unhandled. The user also briefly sees "No provider instances available." before the ready read restores the rows.

This is the one path the diff's own comment misses: `useCredentialStore` survives passive flaps and non-fatal retries (the component never remounts), but the fatal → retry → banner transition is a genuine remount of `<Providers>` with no bound client. Suggested fix: don't mount/start the list until a client is bound (e.g. gate on `ready` for Providers, since it has no retained-client fallback, or bind a retained client in the retry gap the way Plugins/HubSettings do), or have `ProviderInstances.start()` skip `refresh()` when the store has no client.

---

**Low — `runGatedMutation`'s new not-ready refusal is reported to users as "another change is still running"**

File: `mobile-native/src/pluginMutationGate.ts:81-85`, call sites `mobile-native/src/PluginsScreen.tsx:169`, `mobile-native/src/MarketplaceBrowser.tsx:137` and `:410`.

`runGatedMutation(gate, false, action)` returns `"refused"`, the same outcome as a busy gate. Every caller maps `"refused"` to `PLUGIN_MUTATION_BUSY` ("Another change is still running. Wait for it to finish."). The guards (`disabled` + `whenReady`) normally prevent this, but the stated purpose of the `ready` check is to catch a caller that forgets a guard or fires a handler programmatically — and on that path the user is told a different write is in progress rather than that the connection is down. Suggested fix: give the not-ready case its own outcome/copy (e.g. return a distinct value or have callers branch on `!ready`), so the defense-in-depth message is truthful.

######## member 3 (codex default)
<reviewer unavailable>

## PR 1916
== panel 4a97dc91-041e-45ae-b9ca-1938361e7a04 head 8d7732de2 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19876 verdict=1 chars=204
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19877 verdict=1 chars=179
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19878 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19879 verdict= chars=0

######## member 0 (codex default)
No issues found.

Summary: Adds transactional SQLite-backed mutation outbox write operations with deterministic tests for persistence, sequencing, settlement, recovery, rollback, and secure ID generation.

######## member 1 (codex default)
No issues found.
Summary: Adds the native mutation outbox write path over SQLite with gap-free sequencing and atomic settlement semantics, plus thorough behavior-mirroring tests.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1917
== panel f8b853cf-34cf-486e-aaae-c3bf3f40b46b head ee0013dc8 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19871 verdict=0 chars=962
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19872 verdict=1 chars=169
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19873 verdict=0 chars=2137
-- member 3 codex/glm-5.3-vision type=default status=failed job=19874 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile-native/src/mutationOutboxStorage.ts:152-163`  
  **Problem**: Sequence allocation reads `last_sequence` before writing inside a deferred savepoint. Separate SQLite handles can both compute the same next sequence, and the schema has no unique `(target_ref, intent_sequence)` constraint, breaking FIFO ordering.  
  **Fix**: Serialize allocation with an immediate write transaction or atomic counter update, and enforce uniqueness with a matching index.

---

- **Severity**: Low  
  **Location**: `mobile-native/src/mutationOutboxStorage.ts:240-253`  
  **Problem**: `settleReceipt` omits `composerText` when moving an outbox record into optimistic storage, silently losing part of the persisted intent.  
  **Fix**: Copy `composerText: source.composerText` and add a round-trip assertion.

## Summary

Adds a SQLite-backed native mutation outbox implementation and comprehensive storage tests.

######## member 1 (codex default)
No issues found.
Summary: Native SQLite outbox faithfully mirrors the IndexedDB oracle's write/read contracts with atomic handoffs and thorough behavior-covering tests.


######## member 2 (pi default)
## Summary

The four commits add a new native adapter, `mobile-native/src/mutationOutboxStorage.ts`, implementing the shared `MutationOutboxStorage` port over a synchronous SQLite handle (expo-sqlite in production, `node:sqlite` in tests), plus a 469-line test file that validates each write's effect against raw rows and each read through the port. The adapter covers enqueue with per-target gap-free sequence allocation (rolled back on duplicate id), attempted/unknown transitions, receipt settlement with optimistic-display promotion, applied settlement, recovery transfer, and the read path (`listTargetRefs`, `getOptimistic`, `listOptimistic`, `getRecovery`, `nextDispatchable`, `restoreProvenAbsent`), with compound writes wrapped in savepoints. The implementation tracks the frontend IndexedDB oracle's contracts closely, and the tests cover the dupe-id rollback, crypto default, atomicity faults, and the read-path methods. I verified the port has 13 methods and the class implements all 13, with matching signatures.

---

**Severity: low** — `mobile-native/src/mutationOutboxStorage.ts:125`

The class comment claims it "implements the package's `MutationOutboxStorage` port," but the class declaration has no `implements MutationOutboxStorage<A>`. The comparable native adapter in this repo enforces port conformance at the type level (`hubUpgradeRepository.ts:29` — `export class HubUpgradeRepository implements UpgradeStorage`), as does `nativeImagePicker` via a type annotation. Without the clause, a future change to the shared port (a renamed/removed method, a widened parameter, a new required call) produces no compile error here; the mismatch only surfaces at runtime once the class is wired into `MutationOutbox`/`MutationDispatcher` (e.g. as a missing-method/invalid-argument crash), and the wiring for this adapter is a separate upcoming change. Suggested fix: import the port type and declare `export class MutationOutboxSQLite<A extends MutationAttachmentRef = MutationAttachmentRef> implements MutationOutboxStorage<A> {`. I confirmed all 13 current signatures already match, so this is a no-op at runtime today.

######## member 3 (codex default)
<reviewer unavailable>


## PR 1919
== panel 14090a4d-5646-41fa-baf5-ecb52c02d8aa head 6c0271680 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19905 verdict=0 chars=811
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19906 verdict=0 chars=1617
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19907 verdict=0 chars=3014
-- member 3 codex/glm-5.3-vision type=default status=failed job=19908 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile/src/state/conversation.ts:1944-1955`  
  **Problem**: Rehydrate treats any matching turn ID as fully covered by the fresh read. Since transcript paging can split one turn across pages, this drops the older fragment from `conversation.turns` and fails to carry the older page’s cursor, leaving the turn model and retained items/cursor inconsistent.  
  **Fix**: Merge overlapping turns by item identity using the package merge semantics, and preserve the accumulated cursor whenever the older page contributed items or a turn fragment—not only when it added a turn with a new ID.

## Summary

The change propagates paged turn data into mobile conversation state, but rehydrate still mishandles overlapping fragments of the same paginated turn.

######## member 1 (codex default)
## Review Findings
- **Severity**: Medium
- **Location**: `mobile/src/state/conversation.ts:1944`
- **Problem**: Overlapping turns on rehydrate are filtered with `turnsMatch` and the fresh copy is kept verbatim, discarding the accumulated fragment's fallback fields. A split fragment carrying `usage`/`items` with no `id` match is lost, while `loadOlder` reconciles the same case with `mergePageTurn` (`newer.usage ?? older.usage`).
- **Fix**: Merge overlapping turns instead of dropping them, reusing the package merge (`mergePageTurn`/`mergeOlderItemPage`) with fresh-wins-with-fallback semantics.
---
- **Severity**: Low
- **Location**: `mobile/src/state/conversation.ts:1947`
- **Problem**: Rehydrate builds `mergedTurns` as fresh-first plus page-only older turns last, reversing the older-first chronological invariant that `mergeOlderItemPage` maintains. This contradicts the adjacent `older first` comment.
- **Fix**: Prepend page-only turns (`[...pageOnlyTurns, ...conversation.turns]`) or merge through `mergeOlderItemPage` to preserve ordering.
---
- **Severity**: Low
- **Location**: `mobile/src/state/conversation.ts:1953`
- **Problem**: `hasOlderTurns` re-detects a contribution with an id-only set while `pageOnlyTurns` was computed with identity-aware `turnsMatch`. The two rules can diverge for split fragments with different ids.
- **Fix**: Derive the cursor decision from the merge result directly, e.g. `if (pageOnlyTurns.length > 0)`.
## Summary
Keeps paged turns and wire cursor in sync across `loadOlder`/rehydrate, but the rehydrate turn merge drops fragment fallbacks and reverses turn order.


######## member 2 (pi default)
## Summary

The series keeps the mobile conversation store's `conversation.turns` and `conversation.olderCursor` (the `ThreadModel` fields `sessionTokens` reads) coherent with paging and rehydrate:

- `loadOlder` now returns the page's own wire turns (`turnsPage`), and the store folds them into `conversation.turns` via the package's identity-aware `mergeOlderItemPage`/`turnsMatch`, while setting `conversation.olderCursor` to the wire's `nextCursor` (never the item-cap-supressed store cursor).
- `turnsMatch` is exported from `appwire-client` for the rehydrate merge.
- Page-owned turn IDs are tracked separately from item IDs (`pageOwnedTurnIds`), never pruned, so a page whose display rows were all deduped/evicted still keeps its turns across a rehydrate.
- `preservePageHistory`/`preserveTurnHistory` now gate on actual ownership rather than `loadOlderToken` merely changing, so a failed `loadOlder` racing a rehydrate cannot force stale history/cursors.
- Rehydrate carries the accumulated wire cursor only when the merge actually contributed a turn beyond the fresh reread's window.

The final diff matches the stated intent. One correctness gap remains.

---

**Severity: medium** — `mobile/src/state/conversation.ts:1944-1951`

In the rehydrate path, an accumulated turn that matches a fresh turn (`turnsMatch`) is excluded from `pageOnlyTurns` and therefore *replaced* by the fresh copy:

```ts
const pageOnlyTurns = currentConvForMerge.turns.filter(
  (turn) => !conversation.turns.some((fresh) => turnsMatch(turn, fresh)),
);
mergedTurns = [...conversation.turns, ...pageOnlyTurns];
```

This is asymmetric with `loadOlder`, which folds the same two turns through `mergePageTurn` and preserves the older value when the newer one is absent (`usage: newer.usage ?? older.usage`). Since `thread/read` and `thread/turns/list` are both item-paginated (the very reason this change uses `turnsMatch`), a turn can be split into fragments across the reread window boundary, and a fragment may carry no `usage` of its own — the new round-6(a) test itself models exactly that ("usage arrives on the fragment that continues further back"). If the accumulated (page-merged) turn has usage from the older fragment and the fresh reread's matching turn is a usage-less fragment, the accumulated usage is discarded outright. `sessionTokens(conv)` then undercounts (or returns `null`) — the exact failure this series exists to fix, just in the opposite direction.

Not covered by the new tests: round 6(a) exercises the split only through `loadOlder`; round 7(1) only checks that a fresh turn's *present* usage wins.

Suggested fix: for matched turns, merge rather than drop — e.g. reuse the package's per-turn rule with the fresh turn as `newer` (`mergePageTurn(current, fresh)`, in which `fresh.usage ?? current.usage` keeps the old usage when the fresh fragment has none), exporting `mergePageTurn` if needed, and add a case where a rehydrate's fresh fragment omits usage that an accumulated page fragment carried.

######## member 3 (codex default)
<reviewer unavailable>


## PR 1920
== panel 711184fe-73b3-4308-9928-6578009c04cf head d40bf549b outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19674 verdict=0 chars=762
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19675 verdict=1 chars=215
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19676 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19677 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
- **Location**: `mobile-native/src/transcriptPresentation.ts:227-229`
- **Problem**: When cumulative usage is absent, `sessionTokens(conversation)` sums only `conversation.turns`. Older pages loaded by the native store are projected into `conversation.items` without updating `conversation.turns`, so the footer continues reporting only the initial window’s token sum even after the user loads older transcript pages.
- **Fix**: Merge paged `TurnModel`s into the canonical conversation model (and update its cursor), or maintain a separate accumulated usage total when paging.

## Summary

Adds shared token-unit labeling and native session usage derivation, including cumulative cache/total rows and per-turn fallback.

######## member 1 (codex default)
No issues found.
Summary: Centralizes token unit labeling in the shared package and fixes the mobile transcript footer to derive session totals from loaded turns while preserving cumulative cache and total figures.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1921
== panel 197ce35f-e861-4e2c-94f7-2c0a9a43d323 head 0a08b4d16 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19960 verdict=0 chars=702
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19961 verdict=1 chars=173
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19962 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19963 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
  **Location**: `agent/session_retirement_evidence_test.go:1464-1481`
  **Problem**: The regression test relies on 10,000 `runtime.Gosched()` calls and a one-shot non-blocking select to ensure the claim goroutine runs before the namer is released. A buggy implementation that skips `sendersWG.Wait()` can therefore be descheduled until after release and pass the test.
  **Fix**: Replace scheduler yielding with a deterministic test hook or synchronization point that records an early `TryClaim` while the namer remains held.

## Summary

The changes centralize sender draining and add a namer-race regression test, but that test is still scheduler-dependent.

######## member 1 (codex default)
No issues found.
Summary: Test-only fix serializes retirement eligibility checks behind the async session namer via `sendersWG` and adds a regression test proving the wait.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1931
== panel 5b98fbca-ce53-4373-83fd-745c8b50248f head 4d2daddc8 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19994 verdict=0 chars=644
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19995 verdict=1 chars=150
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19996 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=done job=19997 verdict=0 chars=1053

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
  **Location**: `cmd/evener-hub/frontend/src/panes/session/composer/askDock/askDock.testFixture.ts:10`
  **Problem**: This helper imports the in-repo `testing/` subpath, but its `.testFixture.ts` filename is not recognized by the frontend package gate as a test or test-support file. `make test-web` will therefore report it as an invalid production import.
  **Fix**: Rename it to a recognized `*TestUtils.ts` filename, such as `askDockTestUtils.ts`, and update its imports.

## Summary

Refactors warning handling, ask resolution, transcript layout classification, and shared ask-dock test fixtures.

######## member 1 (codex default)
No issues found.
Summary: Refactor consolidates shared ask-resolution and warning-fold helpers with equivalent behavior and stronger bounds handling.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
## Review Findings
- **Severity**: High
- **Location**: cmd/evener-hub/frontend/src/panes/session/composer/askDock/askDock.testFixture.ts:10
- **Problem**: This filename is not allowed to import `@evener/appwire-client/testing/*`; `check-package-tests` treats it as a production source file, so the frontend gate fails.
- **Fix**: Rename the module to an allowed test-support name such as `askDockTestUtils.ts`, or move it under `__tests__/`, and update imports.
---
- **Severity**: Low
- **Location**: appwire-client/typescript/reducer.ts:1202
- **Problem**: `foldWarningParams` still scans and bounds oversized message/title/hint values twice: `hasWarningText` computes and discards `boundedWarningText`, then `boundedContent` or the field assignments compute it again.
- **Fix**: Compute each bounded value once, derive the text branch from those cached results, and reuse them in `WarningFold`.

## Summary
The change deduplicates ask-dock fixtures, hardens warning-frame pruning and composition, and shares steering/warning behavior across clients.


## PR 1737
== panel 52b0d139-338d-4350-8645-07e448348962 head d4f256200 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19886 verdict=0 chars=859
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19887 verdict=0 chars=1122
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19888 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19889 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
  **Location**: `mobile/src/conversation/project.ts:1072-1076`, `mobile-native/src/questionAnswers.ts:14-18`
  **Problem**: `truncateItem` truncates interactive question fields such as option labels, headers, and `ifUnanswered`. The native state stores these truncated refs in `conversation.items`, and `pendingQuestions`/`sendAnswers` use them to compose responses, so oversized options can be submitted with altered labels that no longer match the agent's original choices.
  **Fix**: Keep canonical question refs separate from display-bounded copies; derive answer composition from the untruncated model while using bounded values only for rendering.

## Summary

The change centralizes native projection, identity, caching, truncation, and retention helpers, while adding canonical warning and ask-user handling.

######## member 1 (codex default)
## Review Findings
- **Severity**: Low
- **Location**: `mobile/src/conversation/project.ts:912-950` and `mobile/src/state/conversation.ts:65-105`
- **Problem**: Row-identity helpers (`attachmentSourceId`, `attachmentSourceIdentity`, `timelineIdentity`, `activityIdentity`, `ownTimelineIdentities`, `timelineIdentities`) are exact duplicates in both modules, while this same change centralizes truncation constants for the same reason.
- **Fix**: Export the single copy from `conversation/project.ts` and import it in `state/conversation.ts`.
---
- **Severity**: Low
- **Location**: `mobile/src/conversation/project.ts:358-362`
- **Problem**: Comment claims `liveAsksFor` is shared by `projectTimeline` and `pendingQuestions (mobile-native/src/questionAnswers.ts)`, but that function reads already-projected `conversation.items` and never calls `liveAsksFor`.
- **Fix**: Correct the comment to name the actual second caller or remove the stale reference.
## Summary
Centralizes truncation/cap logic and fixes warning, failure-id, and ask-memo behavior, leaving only duplicated identity helpers and a stale sharing comment.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1738
== panel aa0c5dfd-e236-47f8-bf50-2ddbde576e7a head ffb4cbae1 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19898 verdict=0 chars=1162
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19899 verdict=0 chars=617
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19900 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=19901 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile-native/src/questionAnswers.ts:78-88`, `mobile-native/src/draftRepository.ts:93-98`  
  **Problem**: Existing persisted drafts use full question objects as signatures, but the new digest signature never matches them, so unchanged saved answers are silently discarded after upgrading.  
  **Fix**: Preserve compatibility by normalizing or migrating legacy signatures before comparing definitions.

---

- **Severity**: Medium  
  **Location**: `mobile/src/conversation/project.ts:575-601`, `mobile/src/state/conversation.ts:2795-2800`  
  **Problem**: Canonical projection now renders warnings as notices, while live notifications still append failure rows. Users therefore see different warning types until a reread, and live warnings may remain inconsistent indefinitely.  
  **Fix**: Route live warning handling through the canonical notice projection or trigger an authoritative reprojection immediately.

## Summary

The change centralizes native projection, question identity, truncation, and caching, but introduces persisted-draft incompatibility and inconsistent live warning rendering.

######## member 1 (codex default)
## Review Findings
- **Severity**: Low
- **Location**: `mobile-native/src/questionAnswers.ts:30`
- **Problem**: Comment claims `boundQuestionText` is shared by `questionsIdentity`, but `questionsIdentity` hashes canonical `JSON.stringify(question)` and never calls the bound helper; only the sheet display copy uses it.
- **Fix**: Correct the comment to state the bound is shared by the sheet display copy and question-row truncation, while the identity hashes canonical fields.
## Summary
Centralizes truncation, identity, and projection caching so question UI reads bounded copies while answers use canonical refs.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>


## PR 1740
== panel e5be5481-31c0-4c66-b82d-18190e862d01 head 55c6eab8b outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=20014 verdict=0 chars=1005
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20015 verdict=1 chars=169
-- member 2 pi/deepseek-4.1-flash type=default status=done job=20016 verdict=0 chars=26
-- member 3 codex/glm-5.3-vision type=default status=failed job=20017 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `appwire-client/typescript/reducer.ts:1354-1356`, `mobile/src/state/conversation.ts:1902-1918`  
  **Problem**: Live warning items remain attached to the turn after a bare `turn/completed` notification. The store then reprojects the updated model without rereading, so warnings that are explicitly non-persistent can remain visible indefinitely.  
  **Fix**: Remove transient warning items when settling the turn, or trigger a canonical reread/drop of those items on completion.

---

- **Severity**: Low  
  **Location**: `mobile/src/state/conversation.ts:25-37`  
  **Problem**: `foldWarningParams`, `isActiveItem`, `joinWarningParts`, and the `ItemModel` type are unused imports left behind after the incremental row logic was removed.  
  **Fix**: Remove the unused imports.

## Summary

The change centralizes native conversation projection and bounds question/timeline data, but transient warnings can outlive the turn that produced them.

######## member 1 (codex default)
No issues found.
Summary: Refactor moves native rows to a model projection with shared bounds, stable identities, and gap-driven rereads, with tests updated throughout.


######## member 2 (pi default)
No review output generated

######## member 3 (codex default)
<reviewer unavailable>
