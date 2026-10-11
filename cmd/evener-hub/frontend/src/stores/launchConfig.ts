// launchConfig.ts is the web's one instance of the package's launch-config
// gateway (createLaunchConfigStore in @evener/appwire-client), bound to
// whichever client connectionStore currently holds. Follows stores/threads.ts's
// own requireClient()-via-connectionStore pattern (no connect() of its own):
// the client is resolved on every call rather than captured at module load,
// so a call before connect() fails loudly and a reconnect is picked up
// without rebuilding the store - which is also what keeps the schema cache
// alive across the session, since the schema is server-global.

import type { LaunchConfigStoreState } from "@evener/appwire-client";
import { createLaunchConfigStore, type LaunchConfigClient, type LaunchConfigStore } from "@evener/appwire-client";
import { useStore } from "zustand";
import { connectedClientPort, onConnectionNotification, onConnectionReplacedOrRecovered } from "./connection";
import { isLocalHost } from "./hostRouting";
import { remoteHostStoreClient } from "./hostStoreClient";
import {
  currentHostRegistration,
  type HostRegistration,
  hostRegistrationChanged,
  hostsStore,
  registrySaysHostGone,
} from "./hosts";

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

/** goneLaunchConfigStore is what a lookup for a host the registry does not list
 * returns: ONE shared store whose port refuses every call, so nothing a caller
 * reaches here can read or write a host that is not registered. It holds no
 * cache and no subscription, which is why it is shared rather than built per
 * lookup - an instance per call would build something for a host that does not
 * exist. */
const goneClient: LaunchConfigClient = {
  request: async () => {
    throw new Error("this host is not registered: the registry lists no such host, so it has no launch-config store");
  },
  onNotification: () => () => {},
};
const goneLaunchConfigStore = createLaunchConfigStore(goneClient);

/** launchConfigStoreForHost returns the launch-config gateway for `host`: the
 * controller's singleton for the local hub (and for an absent host), and a
 * per-host instance for a remote one. */
export function launchConfigStoreForHost(host: string | null | undefined): LaunchConfigStore {
  if (isLocalHost(host)) return launchConfigStore;
  return hostEntry(host as string).store;
}

/**
 * onLaunchConfigUpdated subscribes `handler` to `host`'s own launch-config
 * CHANGE, which is what lets a mounted launch-config pane converge when the
 * configuration moves underneath it:
 *
 *   - A REMOTE host's own evener/launch/updated reaches this browser wrapped in
 *     evener/host/notification, tagged with the host that owns it (the hub's
 *     relayHostNotifications fan-out). It is unwrapped here through the same
 *     host-bound port every read of that host already goes through
 *     (remoteHostStoreClient), so a change tagged with any OTHER host - and the
 *     controller's own plain frames - never reaches this subscription. Handling
 *     the wrapper at the client seam rather than bypassing it is what makes the
 *     tag load-bearing.
 *
 *   - The LOCAL hub's launch config is this browser's own, and its
 *     evener/launch/updated arrives plainly over the connection the shared port
 *     follows across a client swap (onConnectionNotification). It has no host
 *     registration and so no attach epoch (stores/hosts.ts), which is why a
 *     REPLACED controller connection or a RECOVERY is its second signal: a
 *     change made while this browser was away carried no notification it saw.
 *
 * The handler fires once per notification; coalescing a burst is the caller's
 * business (see useLaunchConfigRefresh). */
export function onLaunchConfigUpdated(host: string, handler: () => void): () => void {
  if (isLocalHost(host)) {
    const stopNotifications = onConnectionNotification((n) => {
      if (n.method === "evener/launch/updated") handler();
    });
    const stopConnection = onConnectionReplacedOrRecovered(handler);
    return () => {
      stopNotifications();
      stopConnection();
    };
  }
  return remoteHostStoreClient(host).onNotification((n) => {
    if (n.method === "evener/launch/updated") handler();
  });
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
  // The registry says this host is not configured (stores/hosts.ts's
  // registrySaysHostGone - the same answer the eviction subscription above acts
  // on): no instance is kept for it, so a lookup that races that subscription -
  // or one made before this module was loaded - cannot rebuild one and hand out
  // a store that dials a host nothing lists. The shared refusing store is handed
  // back instead, and a re-add builds a fresh instance as it always did.
  if (registrySaysHostGone(hostsStore.getState().load, name)) {
    evictHostEntry(name);
    return { store: goneLaunchConfigStore, registration: null };
  }
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

/** evictHostEntry drops `host`'s instance. createLaunchConfigStore holds no
 * subscription (its port is used for `request` alone), so there is nothing to
 * unwind: the cached schema goes with the entry. */
function evictHostEntry(name: string): void {
  hostStores.delete(name);
}

// The registry's own transition evicts - never a lookup. A host that leaves the
// registry is rendered as the frame's own refusal instead of its body
// (hostScopedSurface.tsx), so nothing calls this module's accessor for it again:
// left to the accessor, its entry (and the schema it cached) would sit in this
// map for the rest of the session. Only the registry's ANSWERED "gone" evicts
// (stores/hosts.ts's registrySaysHostGone), so a host that is merely unattached,
// or one whose read has not answered yet, is never dropped.
hostsStore.subscribe(() => {
  const load = hostsStore.getState().load;
  for (const name of [...hostStores.keys()]) {
    if (registrySaysHostGone(load, name)) evictHostEntry(name);
  }
});

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
