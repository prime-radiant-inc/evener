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
import { connectionStore } from "./connection";

export type SettingsOverviewStoreState = HubOverviewState;

export const settingsOverviewStore = createHubOverviewStore({
  // Reads the client connection.ts wired via
  // useConnectionStore.getState().connect(client) - this store has no
  // connect() of its own, same rationale as threads.ts's own requireClient.
  request: (method, params, opts) => {
    const client = connectionStore.getState().client;
    if (!client) {
      throw new Error(
        "settingsOverview store: no client connected; call useConnectionStore.getState().connect(client) first",
      );
    }
    return client.request(method, params, opts);
  },
});

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
  settingsOverviewStore.reset();
}
