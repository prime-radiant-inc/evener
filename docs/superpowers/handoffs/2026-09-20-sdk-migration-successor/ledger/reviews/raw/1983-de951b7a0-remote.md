== panel 3ee9beb3-379e-4c8f-ae6d-972d16f930ff head de951b7a0 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=20962 verdict=0 chars=1594
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20963 verdict=0 chars=502
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=20964 verdict=1 chars=1770

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile-native/src/MarketplaceBrowser.tsx:487-497`  
  **Problem**: `AddMarketplace` treats the new `"not-ready"` mutation result as success and closes the modal, even though the request was never executed. This can discard the user's draft and falsely imply the marketplace was added.  
  **Fix**: Handle `"not-ready"` explicitly by leaving the modal open and preserving its draft.

---

- **Severity**: Medium  
  **Location**: `cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/MarketplaceSheet.tsx:285-300`  
  **Problem**: A typed cleanup failure from a replaced/old connection still produces a warning toast and triggers reconciliation on the current connection, even though `onAppliedRemoval` correctly rejects the stale owner.  
  **Fix**: Check that the captured removal client is still current before showing the warning or fetching, or make `onAppliedRemoval` return a boolean and abort when it returns false.

---

- **Severity**: Low  
  **Location**: `mobile-native/src/ProviderEditor.tsx:74-100`  
  **Problem**: Provider creation/editing relies only on the render-time `disabled` prop; its save handler has no live readiness check, so a stale handler can issue a mutation after the connection has dropped.  
  **Fix**: Pass a live readiness predicate into `ProviderEditor` and check it in `save()`/the save handler.

## Summary

The change adds applied marketplace-removal outcomes and reconnect-aware native screens, but has stale-connection handling gaps in marketplace and provider mutations.

######## member 1 (codex default)
## Review Findings
- **Severity**: Medium
- **Location**: `mobile-native/src/MarketplaceBrowser.tsx:489`
- **Problem**: `AddMarketplace.submit()` treats `not-ready` as success and calls `onClose()`, closing the modal and discarding the draft when a flap lands between press and gate check.
- **Fix**: Return early on `not-ready` without closing or setting an error, matching the other mutation paths.
## Summary
Adds applied-with-litter marketplace removal handling with reconnect-safe mobile screens.


######## member 2 (pi default)
## Summary

This 59-commit series threads one new durable outcome — a marketplace unregister whose clone cleanup failed on disk — end to end, and separately makes mobile-native ready-only screens survive a connection flap.

- **SDK/plugin core**: `plugins.RemoveMarketplace` now saves the registry *before* deleting the clone; a clone-removal failure after the save is a typed sentinel (`ErrMarketplaceUnregisteredCloneRemains`) rather than a log-only warning. A lookup miss returns before deriving `marketplaceDir(name)`, so unvalidated names (`""`, `".."`) never reach the filesystem.
- **Wire**: a new `ErrorMarketplaceUnregisteredCloneRemains` / `MarketplaceUnregisteredCloneRemainsData` carries the re-listed `applied` list (or `appliedUnavailable`), mirroring the keybindings post-rename shape. The hub, CLI, and `writeRevisioned`'s new `onFailure` hook all classify it; `marketplacesPublicationVersion` lets consumers know when an authoritative snapshot landed.
- **Web/native UI**: applied-with-litter removals warn, reconcile from `applied`, and fence retries via connection-scoped guard maps; mobile adds `connectionDisplay` (`wall`/`banner`/`none`), `useRenderClient`, `useLiveReadiness`, `whenReady`, and a `not-ready` gate outcome so mutations are refused before hitting a dead socket.

I traced the fence/held-write semantics (`listRevision` + `storeLifecycle`), the applied-list classification (empty slice marshals as `[]`, so a legitimate empty list is not misread as `unavailable`), the guard baseline/clear rules across web and native, and the hub/client error plumbing (`client.ts` preserves `error.data`). I did not find a defect that would cause incorrect results, data loss, or a security regression in the final aggregate state.

No issues found.

verification_snapshot_utc=2026-09-19T05:42:43Z
