// credentials.ts is the web's Providers & credentials store: the package's
// credential instances store core (@evener/appwire-client/state/credentials)
// wired to connectionStore and stamped with this page's mutation identity.
// Follows stores/threads.ts's own connectionStore pattern (this store has no
// connect() of its own): the connection subscription below is what tells the
// core which client the rows belong to, and the core listens for
// evener/auth/updated on that client itself.

import type { InstanceEntry, ProviderDescriptor } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import {
  type CredentialInstancesState,
  createCredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { createStore, useStore } from "zustand";
import { type ConnectionStoreState, connectionStore, onConnectionNotification } from "./connection";
import { hostRequest, isLocalHost } from "./hostRouting";
import { type HostsLoadState, hostIdentity, hostsStore } from "./hosts";
import { ownClientId } from "./mutationClientIdentity";

export {
  foreignListingChange,
  isStaleListingRefusal,
  StaleListingRefusal,
  staleListingHeld,
} from "@evener/appwire-client/state/credentials";

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
  /** The host registry's own load warnings (InstanceListResponse.diagnostics):
   * a malformed or partially loaded provider config on THAT host. Carried so the
   * read-only remote view can say the listing is partial instead of reading as
   * complete. */
  diagnostics: string[];
  writesRefused: boolean;
  loading: boolean;
  error: string | null;
  /** The registry identity (hosts.ts's hostIdentity) these rows were read UNDER,
   * recorded only when the read succeeded. The map is keyed by host NAME, so a
   * name re-registered as a different host would otherwise inherit the previous
   * registration's rows: a reader compares this against the identity the
   * registry gives the name now and refuses to show rows they do not match.
   * Null means no successful read under a known identity yet, which is why an
   * unanswered read can never pass for an empty listing. */
  readIdentity: string | null;
  /** The registration the MOST RECENT read of this name was issued under - the
   * identity the registry named at that moment, or null when the registry was
   * still unread then. Unlike readIdentity (success-only, for display
   * verification), this is set when the read STARTS, so it also ties a failed or
   * still-in-flight read to a registration. It is what survivesRegistry
   * invalidates against: rows that cannot be tied to the registration the
   * registry names now - including a read taken while the registry was unread -
   * do not outlive the registry's first ready snapshot. */
  readRegistration: string | null;
}

/** The empty partition, a module constant rather than a fresh literal: a host
 * with no entry yet keeps a stable snapshot identity instead of re-rendering
 * forever. */
export const EMPTY_HOST_INSTANCE_STATE: HostInstanceState = Object.freeze({
  instances: [],
  availableProviders: [],
  diagnostics: [],
  writesRefused: false,
  loading: false,
  error: null,
  readIdentity: null,
  readRegistration: null,
});

export interface HostInstancesState {
  hosts: Record<string, HostInstanceState>;
  // generation advances on every connection transition. A remote read captures
  // it before its request and commits only if it is unchanged: an answer that
  // arrives across a transition belongs to a client that is gone, and the
  // listing it was refreshing is still the one this page holds. The transition
  // also releases every partition's in-flight status, so a form reopened after
  // a reconnect re-reads instead of waiting on an answer that will never be
  // accepted.
  generation: number;
}

/** hostInstancesStore holds one partition per non-controller host. It is a
 * store of its own rather than part of the package's credential store above:
 * those fields ARE the controller's rows, and everything that acts on them -
 * the package store's evener/auth/updated refetch, its writes - must never see
 * another host's listing. */
export const hostInstancesStore = createStore<HostInstancesState>(() => ({ hosts: {}, generation: 0 }));

// The controller-scoped store above owns the page's rows; this subscription
// only ends the remote reads a connection transition orphans.
let lastConnectionState = connectionStore.getState().state;
let lastConnectionClient = connectionStore.getState().client;
connectionStore.subscribe((state) => {
  if (state.state === lastConnectionState && state.client === lastConnectionClient) return;
  lastConnectionState = state.state;
  lastConnectionClient = state.client;
  hostInstancesStore.setState((previous) => ({
    generation: previous.generation + 1,
    hosts: Object.fromEntries(
      Object.entries(previous.hosts).map(([host, partition]) => [
        host,
        partition.loading ? { ...partition, loading: false } : partition,
      ]),
    ) as Record<string, HostInstanceState>,
  }));
});

// A partition is cached under a host NAME, so a host removed - or re-registered
// under the same name pointing somewhere else - must not inherit the previous
// registration's rows. The registry is the authority for which host a name means
// now, but only once it has answered: a registry still being read says nothing
// about that, so invalidation waits for its ready snapshot.
hostsStore.subscribe((state) => {
  forgetPartitionsForRegistry(state.load);
});

// survivesRegistry answers whether `partition` may be kept for `name`: the
// registry must still list the name, and the registration the partition's most
// recent read was issued under must be the one the registry names now. An untied
// read (readRegistration null - the registry was unread when it was issued, M3)
// cannot be attributed to any registration, so it does not survive a ready
// snapshot that names the host; only a partition with nothing read yet (which
// the map never holds) would be kept. readRegistration is meaningful for a
// FAILED or in-flight read too, so a re-registration also retires those.
function survivesRegistry(
  load: Extract<HostsLoadState, { phase: "ready" }>,
  name: string,
  partition: HostInstanceState,
): boolean {
  const row = load.hosts.find((candidate) => candidate.name === name && !candidate.removed);
  if (row === undefined) return false;
  return partition.readRegistration !== null && partition.readRegistration === hostIdentity(row);
}

/** forgetPartitionsForRegistry drops every partition whose rows were read under
 * a registration the registry no longer lists: the name is gone (removed, or no
 * longer configured) or its entry has changed, so the rows belong to a host this
 * browser can no longer name.
 *
 * Dropping also invalidates the reads already in flight for that name - the same
 * per-host ordering guard two overlapping reads of one host use - because such
 * an answer was issued by the registration being forgotten and would otherwise
 * re-create the partition this drop just removed.
 *
 * The store then RE-READS every dropped name the registry still lists, under the
 * registration it names now (M2). The drop removes rows a consumer may be
 * showing; a consumer that keys only on the partition - the spawn form's
 * useProviderSetup, the model-catalog pane - has no other trigger, and without
 * this it would sit on the empty partition the drop left (a false "no
 * providers"). Owning the refresh here is what keeps that defect closed for
 * every consumer instead of each one learning about registry identity. */
export function forgetPartitionsForRegistry(load: HostsLoadState): void {
  if (load.phase !== "ready") return;
  const current = hostInstancesStore.getState().hosts;
  const forgotten = new Set(
    Object.entries(current)
      .filter(([name, partition]) => !survivesRegistry(load, name, partition))
      .map(([name]) => name),
  );
  if (forgotten.size === 0) return;
  hostInstancesStore.setState((previous) => ({
    ...previous,
    hosts: Object.fromEntries(Object.entries(previous.hosts).filter(([name]) => !forgotten.has(name))) as Record<
      string,
      HostInstanceState
    >,
  }));
  for (const name of forgotten) {
    hostRequestVersions.set(name, (hostRequestVersions.get(name) ?? 0) + 1);
    // A removed name has nothing to re-read; a re-registered (or newly-named)
    // one is re-read under the identity the snapshot gives it.
    const row = load.hosts.find((candidate) => candidate.name === name && !candidate.removed);
    if (row !== undefined) void fetchHost(name, hostIdentity(row));
  }
}

/** hostPartition reads one host's own partition, or the empty one before that
 * host has ever been loaded. */
export function hostPartition(state: HostInstancesState, host: string): HostInstanceState {
  return state.hosts[host] ?? EMPTY_HOST_INSTANCE_STATE;
}

export function useHostInstances(host: string): HostInstanceState {
  return useStore(hostInstancesStore, (state) => hostPartition(state, host));
}

/** hostRequestVersions is one monotonic sequence per host, the same ordering
 * guard the package's own credential instances store puts on its listing reads
 * (appwire-client's state/credentials/instances.ts, requestVersion). The
 * connection generation above cannot order two reads of the SAME host: it
 * advances on connection transitions rather than per request, so two
 * overlapping fetchHost calls for one host capture the same generation and the
 * OLDER answer can commit over the newer partition. The product reaches that
 * state without anything unusual - a mount-time load of the selected host raced
 * by the wrapped-notification refetch below, or a user's retry - and the newer
 * listing is the one that describes the host. */
const hostRequestVersions = new Map<string, number>();

/** registryIdentityFor is hosts.ts's identity for `host` as of the registry's
 * current READY snapshot - the same snapshot forgetPartitionsForRegistry
 * invalidates against - or null while that snapshot does not name the host. A
 * read handed no identity still records this, so a name re-registered as a
 * different host is detectable for an identity-less consumer too; an unread
 * registry records nothing, exactly as the identity-less read always did. */
function registryIdentityFor(host: string): string | null {
  const load = hostsStore.getState().load;
  if (load.phase !== "ready") return null;
  const row = load.hosts.find((candidate) => candidate.name === host && !candidate.removed);
  return row === undefined ? null : hostIdentity(row);
}

/** fetchHost reads `host`'s own instance listing through evener/host/request
 * (component 07b), so the spawn form's provider setup describes the machine the
 * launch will use. The controller's host is not a partition - its rows are the
 * package store's - so this is a no-op for LOCAL_HOST and for an absent host;
 * callers read the controller's own store for those.
 *
 * `identity` is the registry identity (hosts.ts's hostIdentity) the caller knows
 * the name by - the settings scope passes the one the registry gives the
 * selected host. It is recorded on a successful read and compared at the start
 * of the next one, so rows read under a different registration are dropped
 * rather than kept for a name that now means another host. Callers with no
 * registry row to hand (the spawn form, the wrapped-notification refetch) omit
 * it, and the read records instead the identity the registry names for the host
 * RIGHT NOW (registryIdentityFor) - so that consumer's rows are still tied to a
 * registration, and a later re-registration drops them too. An unread registry
 * cannot tie the read to any registration (readRegistration null), so the rows
 * are invalid and re-read as soon as the registry first answers. */
export async function fetchHost(host: string, identity: string | null = null): Promise<void> {
  if (isLocalHost(host)) return;
  const client = connectionStore.getState().client;
  if (!client) return;
  const version = (hostRequestVersions.get(host) ?? 0) + 1;
  hostRequestVersions.set(host, version);
  const generation = hostInstancesStore.getState().generation;
  // What this read records: the identity handed in, or the registry's current one
  // for the name when the caller had none to hand (see this function's doc).
  const readIdentity = identity ?? registryIdentityFor(host);
  setHostPartition(host, (previous) => ({
    ...startHostRead(previous, readIdentity),
    loading: true,
    error: null,
    // Ties even an unanswered or failing read to the registration it was issued
    // under, so a later re-registration retires it (survivesRegistry).
    readRegistration: readIdentity,
  }));
  try {
    const resp = await hostRequest(client, host, "evener/instance/list", {});
    if (version !== hostRequestVersions.get(host) || hostInstancesStore.getState().generation !== generation) return;
    setHostPartition(host, (previous) => {
      // The registration these rows are attributed to, never lowered to unknown:
      // a read that resolved to no identity cannot erase what a read that had one
      // established. readRegistration mirrors it, so the rows survive exactly the
      // registration they are attributed to (and an untied read stays untied).
      const recorded = readIdentity ?? previous.readIdentity;
      return {
        instances: resp.instances,
        availableProviders: resp.availableProviders,
        diagnostics: resp.diagnostics ?? [],
        writesRefused: resp.writesRefused ?? false,
        loading: false,
        error: null,
        readIdentity: recorded,
        readRegistration: recorded,
      };
    });
  } catch (err) {
    if (version !== hostRequestVersions.get(host) || hostInstancesStore.getState().generation !== generation) return;
    setHostPartition(host, (previous) => ({ ...previous, loading: false, error: errorText(err) }));
  }
}

// startHostRead begins a read of `host`'s listing under `identity`. Rows read
// under a DIFFERENT registry identity are not this host's, so they are dropped
// rather than kept on screen for a name that now means something else; an
// unknown identity on either side proves no mismatch and keeps them.
function startHostRead(previous: HostInstanceState, identity: string | null): HostInstanceState {
  if (previous.readIdentity !== null && identity !== null && previous.readIdentity !== identity) {
    return { ...EMPTY_HOST_INSTANCE_STATE };
  }
  return previous;
}

// A credential change made ON a remote host reaches this browser wrapped in
// evener/host/notification, tagged with the host whose own evener/auth/updated
// it re-emits (app_host_admin.go's fan-out). That host's partition is what a
// remote spawn reads, so the wrapped notification refetches it -- debounced,
// like the controller store's own refresh -- and never the controller's
// listing.
const HOST_REFETCH_DEBOUNCE_MS = 250;
const hostRefetchTimers = new Map<string, ReturnType<typeof setTimeout>>();

function scheduleHostRefetch(host: string): void {
  const pending = hostRefetchTimers.get(host);
  if (pending !== undefined) clearTimeout(pending);
  hostRefetchTimers.set(
    host,
    setTimeout(() => {
      hostRefetchTimers.delete(host);
      void fetchHost(host);
    }, HOST_REFETCH_DEBOUNCE_MS),
  );
}

onConnectionNotification((notification) => {
  if (notification.method !== "evener/host/notification") return;
  const { host, method } = (notification.params ?? {}) as { host?: string; method?: string };
  if (!host || isLocalHost(host)) return;
  // The instance listing is this store's only wire-truth data, so only a
  // credential change on that host is actionable here. The fan-out also wraps
  // launch/plugin/agents-doc updates, which this store does not read.
  if (method !== "evener/auth/updated") return;
  scheduleHostRefetch(host);
});

/** resetHostInstancesForTests clears the partitions between tests. No
 * production code should call this. */
export function resetHostInstancesForTests(): void {
  for (const pending of hostRefetchTimers.values()) clearTimeout(pending);
  hostRefetchTimers.clear();
  hostRequestVersions.clear();
  hostInstancesStore.setState({ hosts: {}, generation: 0 });
}

function setHostPartition(host: string, update: (previous: HostInstanceState) => HostInstanceState): void {
  hostInstancesStore.setState((state) => ({
    hosts: { ...state.hosts, [host]: update(hostPartition(state, host)) },
  }));
}
