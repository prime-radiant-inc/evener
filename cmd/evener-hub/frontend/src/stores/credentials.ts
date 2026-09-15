// credentials.ts is the web's Providers & credentials store: the package's
// credential instances store core (@evener/appwire-client/state/credentials)
// wired to connectionStore and stamped with this page's mutation identity.
// Follows stores/threads.ts's own connectionStore pattern (this store has no
// connect() of its own): the connection subscription below is what tells the
// core which client the rows belong to, and the core listens for
// evener/auth/updated on that client itself.

import {
  type CredentialInstancesState,
  createCredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { createStore, useStore } from "zustand";
import type { InstanceEntry, ProviderDescriptor } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { type ConnectionStoreState, connectionStore } from "./connection";
import { hostRequest, isLocalHost } from "./hostRouting";
import { ownClientId } from "./mutationClientIdentity";

export { isStaleListingRefusal, StaleListingRefusal, staleListingHeld } from "@evener/appwire-client/state/credentials";

export type CredentialsStoreState = CredentialInstancesState;

// The hub echoes ownClientId into the evener/auth/updated broadcast, so this
// page attributes its own echo by identity rather than provider plus timing.
export const credentialsStore = createCredentialInstancesStore({ ownClientId });

export function useCredentialsStore(): CredentialsStoreState;
export function useCredentialsStore<T>(selector: (state: CredentialsStoreState) => T): T;
export function useCredentialsStore<T>(selector?: (state: CredentialsStoreState) => T): T | CredentialsStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(credentialsStore, selector) : useStore(credentialsStore);
}

// Watches connectionStore for the client becoming available and hands every
// client or connection-state transition to the core - see stores/extensions.ts's
// identical wiring for the full "why react to the store instead of reading it
// once" rationale (a mount-order race between this module and AppShell's own
// connect() effect).
function syncConnection(state: Pick<ConnectionStoreState, "client" | "state">): void {
  credentialsStore.connectionChanged(state.client, state.state);
}

connectionStore.subscribe(syncConnection);
syncConnection(connectionStore.getState());

// resetCredentialsStoreForTests resets this singleton store between tests,
// mirroring resetThreadsStoreForTests/resetTreeStoreForTests. No production
// code should ever call this.
export function resetCredentialsStoreForTests(): void {
  credentialsStore.resetForTests();
}

// --- Host-scoped instance listings (component 07b) --------------------------

/** HostInstanceState is one non-controller host's own provider listing: the
 * same rows the controller's own listing carries, read from THAT host's
 * registry through evener/host/request, plus that host's own request status. A
 * remote host's load cannot move the controller's `loading`/`error`, and the
 * controller's own refetch cannot move a remote host's. */
export interface HostInstanceState {
  instances: InstanceEntry[];
  availableProviders: ProviderDescriptor[];
  writesRefused: boolean;
  loading: boolean;
  error: string | null;
}

/** The empty partition, a module constant rather than a fresh literal: a host
 * with no entry yet keeps a stable snapshot identity instead of re-rendering
 * forever. */
export const EMPTY_HOST_INSTANCE_STATE: HostInstanceState = Object.freeze({
  instances: [],
  availableProviders: [],
  writesRefused: false,
  loading: false,
  error: null,
});

export interface HostInstancesState {
  hosts: Record<string, HostInstanceState>;
}

/** hostInstancesStore holds one partition per non-controller host. It is a
 * store of its own rather than part of the package's credential store above:
 * those fields ARE the controller's rows, and everything that acts on them -
 * the package store's evener/auth/updated refetch, its writes - must never see
 * another host's listing. */
export const hostInstancesStore = createStore<HostInstancesState>(() => ({ hosts: {} }));

/** hostPartition reads one host's own partition, or the empty one before that
 * host has ever been loaded. */
export function hostPartition(state: HostInstancesState, host: string): HostInstanceState {
  return state.hosts[host] ?? EMPTY_HOST_INSTANCE_STATE;
}

export function useHostInstances(host: string): HostInstanceState {
  return useStore(hostInstancesStore, (state) => hostPartition(state, host));
}

/** fetchHost reads `host`'s own instance listing through evener/host/request
 * (component 07b), so the spawn form's provider setup describes the machine the
 * launch will use. The controller's host is not a partition - its rows are the
 * package store's - so this is a no-op for LOCAL_HOST and for an absent host;
 * callers read the controller's own store for those. */
export async function fetchHost(host: string): Promise<void> {
  if (isLocalHost(host)) return;
  const client = connectionStore.getState().client;
  if (!client) return;
  setHostPartition(host, (previous) => ({ ...previous, loading: true, error: null }));
  try {
    const resp = await hostRequest(client, host, "evener/instance/list", {});
    if (connectionStore.getState().client !== client) return;
    setHostPartition(host, () => ({
      instances: resp.instances,
      availableProviders: resp.availableProviders,
      writesRefused: resp.writesRefused ?? false,
      loading: false,
      error: null,
    }));
  } catch (err) {
    if (connectionStore.getState().client !== client) return;
    setHostPartition(host, (previous) => ({ ...previous, loading: false, error: errorText(err) }));
  }
}

/** resetHostInstancesForTests clears the partitions between tests. No
 * production code should call this. */
export function resetHostInstancesForTests(): void {
  hostInstancesStore.setState({ hosts: {} });
}

function setHostPartition(host: string, update: (previous: HostInstanceState) => HostInstanceState): void {
  hostInstancesStore.setState((state) => ({
    hosts: { ...state.hosts, [host]: update(hostPartition(state, host)) },
  }));
}
