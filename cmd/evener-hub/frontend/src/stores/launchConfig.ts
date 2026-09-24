// launchConfig.ts is the web's one instance of the package's launch-config
// gateway (createLaunchConfigStore in @evener/appwire-client), bound to
// whichever client connectionStore currently holds. Follows stores/threads.ts's
// own requireClient()-via-connectionStore pattern (no connect() of its own):
// the client is resolved on every call rather than captured at module load,
// so a call before connect() fails loudly and a reconnect is picked up
// without rebuilding the store - which is also what keeps the schema cache
// alive across the session, since the schema is server-global.

import type { LaunchConfigStoreState } from "@evener/appwire-client";
import { createLaunchConfigStore, type LaunchConfigStore } from "@evener/appwire-client";
import { useStore } from "zustand";
import { connectedClientPort } from "./connection";
import { isLocalHost } from "./hostRouting";
import { remoteHostStoreClient } from "./hostStoreClient";
import { currentHostRegistration, type HostRegistration, hostRegistrationChanged } from "./hosts";

// The port's `request` is async so a call before connect() rejects, as a real
// client's would, rather than throwing at the call site.
const connectedClient = connectedClientPort("launchConfig");

export type { LaunchConfigStoreState };

export const launchConfigStore = createLaunchConfigStore(connectedClient);

// --- Host-scoped instances (component 07b) ----------------------------------
//
// A REMOTE host's launch settings (schema, layers, resolve, path helpers) are
// that host's own, and a store instance caches the schema (launchConfig.ts in
// the package: schemaCache is per instance). One instance with a per-call
// routing port would serve host A's cached schema for host B, and invalidating
// on host change is racy because the package's in-flight guard is per-request
// rather than per-host. So each remote host gets its own instance - its own
// schema cache and its own in-flight tracking - over a client that forwards
// through evener/host/request (stores/hostStoreClient.ts). The controller's own
// singleton above is untouched: the local hub is byte-for-byte today's store.
interface LaunchConfigHostEntry {
  store: LaunchConfigStore;
  /** What the registry said REGISTERED this host when the instance was built
   * (stores/hosts.ts's HostRegistration). The instance is current only while
   * the registry still gives the same answer - an unchanged snapshot gives the
   * same one, so nothing is rebuilt on the poll cadence. */
  registration: HostRegistration;
}

const hostStores = new Map<string, LaunchConfigHostEntry>();

/** launchConfigStoreForHost returns the launch-config gateway for `host`: the
 * controller's singleton for the local hub (and for an absent host), and a
 * per-host instance for a remote one. */
export function launchConfigStoreForHost(host: string | null | undefined): LaunchConfigStore {
  if (isLocalHost(host)) return launchConfigStore;
  return hostEntry(host as string).store;
}

/** hostEntry is the one accessor of the per-host cache and the ONE place a
 * re-registration is noticed: an instance whose recorded registration the
 * registry no longer gives is dropped and a fresh one built, so a host removed
 * and re-added under the same name never serves the previous registration's
 * cached schema, and a response that registration still had in flight lands in
 * the store it was issued for rather than this one. A dropped store needs no
 * teardown: createLaunchConfigStore holds no subscription (its
 * LaunchConfigClient port is used for `request` alone). */
function hostEntry(name: string): LaunchConfigHostEntry {
  const registration = currentHostRegistration(name);
  const recorded = hostStores.get(name);
  if (recorded !== undefined && !hostRegistrationChanged(recorded.registration, registration)) {
    // The first answer after an instance was built before the registry had one
    // identifies it from here on; every other unchanged answer is the same one.
    if (registration !== undefined) recorded.registration = registration;
    return recorded;
  }
  const entry: LaunchConfigHostEntry = {
    store: createLaunchConfigStore(remoteHostStoreClient(name)),
    registration,
  };
  hostStores.set(name, entry);
  return entry;
}

/** resetLaunchConfigHostStoresForTests drops every per-host instance between
 * tests. No production code should call this. */
export function resetLaunchConfigHostStoresForTests(): void {
  hostStores.clear();
}

export function useLaunchConfigStore(): LaunchConfigStoreState;
export function useLaunchConfigStore<T>(selector: (state: LaunchConfigStoreState) => T): T;
export function useLaunchConfigStore<T>(selector?: (state: LaunchConfigStoreState) => T): T | LaunchConfigStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(launchConfigStore, selector) : useStore(launchConfigStore);
}

// resetLaunchConfigStoreForTests clears the schema cache between tests - this
// store's own reactive state has nothing to reset (it holds only methods), but
// the cache is a singleton that would otherwise leak a fixture from one test
// into the next. No production code should ever call this.
export function resetLaunchConfigStoreForTests(): void {
  launchConfigStore.getState().invalidateSchema();
}
