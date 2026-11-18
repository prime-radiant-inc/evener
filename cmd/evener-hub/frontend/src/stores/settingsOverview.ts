// settingsOverview.ts is the web's instance of the package's hub overview
// store (createHubOverviewStore): the fetch-once cache of evener/settings/
// overview behind the five overview-backed Settings sections - General, Hub,
// Storage, Agents, and MCP-discovered. Its shape is PINNED across all three
// streams (wave-7 plan): `{ data, loading, error, fetch() }`, plus an additive
// `refresh()` - do not change this surface without checking both sibling
// streams' tests, which are written against it directly.
//
// Unlike threads.ts, this store never holds a persistent client subscription
// to go stale on reconnect: the client port handed to the package resolves
// connectionStore's CURRENT client fresh, at request time, so there is no
// wiring to move to a new client after a reconnect. Caching, in-flight
// dedup and the failed-refresh-keeps-data rule all live in the package.

import { createHubOverviewStore, type HubOverviewState } from "@evener/appwire-client";
import { useStore } from "zustand";
import { connectedClientPort, onConnectionReplacedOrRecovered } from "./connection";

export type SettingsOverviewStoreState = HubOverviewState;

export const settingsOverviewStore = createHubOverviewStore({
  // Reads the client connection.ts wired via
  // connectionStore.getState().connect(client) through the shared port - the
  // same labelled current-client resolution every other store uses; this store
  // has no connect() of its own, same rationale as threads.ts's own
  // requireClient.
  request: connectedClientPort("settingsOverview").request,
});

// missedReconnect records that a replaced or recovered connection may serve a
// hub this tab has not read - a restart that quarantined the operation store
// answers the next read with a signal the cached overview cannot carry. It is
// module state, not a section's, because the reader is often on another
// Settings section when the reconnect lands, and a section-scoped listener
// would miss exactly that read. Nothing is requested here: a subscription that
// read on every reconnect would fire on connections no section has asked
// about. loadSettingsOverview is what spends it.
let missedReconnect = false;
onConnectionReplacedOrRecovered(() => {
  missedReconnect = true;
});

/** loadSettingsOverview reads the overview, or re-reads it when a reconnect has
 * been missed since the last read. fetch() is fetch-once, so the re-read is the
 * only thing that can surface a change made while the tab was away. refresh()
 * leaves the last successful overview in place if the request fails. */
export function loadSettingsOverview(): void {
  if (!missedReconnect) {
    void settingsOverviewStore.getState().fetch();
    return;
  }
  missedReconnect = false;
  void settingsOverviewStore.getState().refresh();
}

export function useSettingsOverviewStore(): SettingsOverviewStoreState;
export function useSettingsOverviewStore<T>(selector: (state: SettingsOverviewStoreState) => T): T;
export function useSettingsOverviewStore<T>(
  selector?: (state: SettingsOverviewStoreState) => T,
): T | SettingsOverviewStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(settingsOverviewStore, selector) : useStore(settingsOverviewStore);
}

// resetSettingsOverviewStoreForTests returns the store to its initial state -
// settingsOverview.ts is a singleton store shared by the whole app, so
// settingsOverview.test.ts (and any section test that touches it) must reset
// it between tests to keep them isolated. No production code should ever call
// this (mirrors threads.ts's resetThreadsStoreForTests / tree.ts's
// resetTreeStoreForTests precedent).
export function resetSettingsOverviewStoreForTests(): void {
  missedReconnect = false;
  settingsOverviewStore.reset();
}
