== panel 7a3fcc63-5c8d-489f-b149-c122a02ccd97 head f947bba6b outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=22719 verdict=1 chars=147
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=22720 verdict=1 chars=178
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=22721 verdict=0 chars=2387

######## member 0 (codex default)
No issues found.

Summary: Adds a monotonic marketplace snapshot publication version that survives resets and advances only for accepted snapshots.

######## member 1 (codex default)
No issues found.
Summary: Exposes a monotonic marketplace snapshot version that advances only on accepted list publications while preserving it across fence, reset, and dispose.


######## member 2 (pi default)
## Summary

The commit adds a `marketplacesPublicationVersion` counter to the marketplaces store state. It advances exactly once per accepted whole-list snapshot publication: successful `evener/marketplace/list` reads, successful mutations (add/remove/refresh/edit) whose response owns the list revision, and clone-litter failures whose `data.applied` list is published. Failed reads, ordinary mutation failures, and unconfirmed applied payloads do not advance it. A new generic `resetState` hook in `storeLifecycle` lets fields survive `reset()` (and `dispose()`), so the counter stays monotonic across a fence/reset rather than falling back to 0. Tests cover all of these paths.

The core counting logic is sound: the increment happens inside the state updater, so it only runs when the guarded setter actually commits (dispose drops the call before invoking the updater), and held writes only run when the list revision promotes them to owner, so there is no double-count or count-without-publish.

---

**Severity: low**

`appwire-client/typescript/state/extensions/storeLifecycle.ts:248` (with `marketplaces.ts:212`)

`reset()` now produces a state that differs from `store.getInitialState()` for marketplaces, because `resetState` restores the live `marketplacesPublicationVersion` while `getInitialState()` still reports `0`. The lifecycle and store docs still describe `reset()` as "Back to the initial state", and `frameworkFreeStore.ts:17` documents `getInitialState()` as "what setState takes to reset the store" — so the two documented reset recipes now disagree. This is already exercised: `cmd/evener-hub/frontend/src/stores/extensions.ts` calls `marketplaces.reset()` and then overwrites the combined store with `...marketplaces.getInitialState()`, leaving the combined store's `marketplacesPublicationVersion` at 0 while the core's internal counter is still ahead. No current consumer reads the field, so there is no functional fallout today; but a future consumer keyed on the published version would observe the value drop to 0 on that reset and then jump past the counter's real value on the next publication.

Suggested fix: update the `reset()` doc comments to state that `resetState` fields survive, and make the `getInitialState()`-based reset path in `extensions.ts` preserve the field (or move the counter so it cannot be clobbered by an initial-state snapshot).
