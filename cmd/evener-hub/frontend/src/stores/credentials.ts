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
import { useEffect, useRef } from "react";
import { createStore, useStore } from "zustand";
import { type ConnectionStoreState, connectionStore, onConnectionNotification, useConnectionStore } from "./connection";
import { hostRequest, isLocalHost } from "./hostRouting";
import { type HostsLoadState, hostsStore, useHostsStore } from "./hosts";
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
  /** hostsStore's revision when this partition's most recent read was issued, or
   * null before any read. It is the ONE tie between a partition and the registry:
   * the partition is current only while this equals the registry's revision, and
   * that revision advances whenever the registry's answer could have changed (a
   * different published snapshot, or a replacement connection client). Set when
   * the read STARTS, so a failed or still-in-flight read is tied to a registry
   * state too. */
  registryRevision: number | null;
  /** True once a read under `registryRevision` completed successfully. That is
   * what makes an unanswered (or transition-orphaned) read unable to pass for an
   * empty listing. */
  read: boolean;
  /** hostInstancesStore's connection generation when this read was issued. A
   * reconnect on the SAME client does not move the registry revision, but it is
   * still a different connection and the host's listing may have changed with
   * it, so a partition is current only under the generation it was read in. */
  readGeneration: number;
  /** Whether the registry had published a snapshot when this read was issued.
   * False means the listing was read while the registry had never answered - the
   * spawn pane's own case, whose hosts come from the navigation manifest - and it
   * cannot count as verified once the registry has been consulted without
   * publishing (see useHostInstances). */
  readPublished: boolean;
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
  registryRevision: null,
  read: false,
  readGeneration: 0,
  readPublished: false,
});

/** PENDING_HOST_INSTANCE_STATE is what a consumer sees while there is no
 * partition for the current registry revision: a read is on its way (or the
 * registry has yet to name the host), so "loading" is the honest state - never
 * "empty". A frozen constant, like the empty one, so a consumer's snapshot stays
 * stable and it does not re-render forever. */
export const PENDING_HOST_INSTANCE_STATE: HostInstanceState = Object.freeze({
  ...EMPTY_HOST_INSTANCE_STATE,
  loading: true,
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

/** hostPartition reads one host's own partition exactly as stored, INCLUDING a
 * partition whose registryRevision is no longer current. Consumers read through
 * useHostInstances, which withholds a stale partition; this raw accessor is for
 * the store's own bookkeeping and for tests that need to see what was read. */
export function hostPartition(state: HostInstancesState, host: string): HostInstanceState {
  return state.hosts[host] ?? EMPTY_HOST_INSTANCE_STATE;
}

/** useHostInstances is one host's own listing as of the CURRENT registry
 * snapshot, and the ONE place a remote listing is read: a consumer watching a
 * remote host, with no partition from the current revision, gets a read issued
 * here. Consumers never have to know about registry revisions, which is what
 * keeps the next consumer - and the one after that - correct.
 *
 * A partition from an older revision is withheld (the pending state) rather than
 * shown, because the registry that produced it no longer describes this host and
 * its replacement read is already on its way. Absence counts as stale, which is
 * what lets a host removed and re-added converge with nothing having to track
 * which partitions were dropped. */
export function useHostInstances(host: string): HostInstanceState {
  const partition = useStore(hostInstancesStore, (state) => hostPartition(state, host));
  const generation = useStore(hostInstancesStore, (state) => state.generation);
  const revision = useHostsStore((state) => state.revision);
  const load = useHostsStore((state) => state.load);
  const reading = useHostsStore((state) => state.reading);
  const publishedRevision = useHostsStore((state) => state.publishedRevision);
  const { client, state: connection } = useConnectionStore();
  // "The registry has been consulted": it has answered (publishedRevision), its
  // load has left the idle state, or a read is in flight right now. While it has
  // NOT been consulted - a spawn-only session, where the host list comes from the
  // navigation manifest - a listing is read and accepted on the connection alone,
  // exactly as before.
  const registryConsulted = publishedRevision !== null || load.phase !== "loading" || reading > 0;
  const current =
    partition.registryRevision !== null &&
    partition.registryRevision === revision &&
    partition.readGeneration === generation &&
    // A listing read before the registry ever answered is tied to revision 0,
    // which a never-published registry also reports: it is accepted only while
    // the registry has NOT been consulted. Once it has - and especially when its
    // read FAILED, where revision 0 then means "nothing was ever published" - the
    // listing is not verified.
    (partition.readPublished || !registryConsulted);
  // Three states hold the read back:
  //  - the registry has FAILED (see CredentialsHostScope's unverifiable state) -
  //    its failure is what the pane shows, so a read taken then is not fetched,
  //  - a registry read is already on its way, whose answer is what this read
  //    would have to be stamped with, so it waits for that one rather than
  //    reading under an answer that is about to be replaced (one wasted request
  //    per deep-link otherwise), and
  //  - the registry has been consulted without ever publishing a snapshot, so a
  //    read now could not be tied to one. It is read only while the registry is
  //    untouched at all - the spawn pane's own case, whose hosts come from the
  //    navigation manifest.
  const shouldRead =
    !isLocalHost(host) &&
    client !== null &&
    connection === "ready" &&
    !current &&
    load.phase !== "error" &&
    (publishedRevision !== null || !registryConsulted);
  // `revision`, `reading` and the phase are deliberate trigger-only dependencies:
  // the effect decides with the LIVE store (a sibling's mount effect can have
  // started a registry read in this same commit), but a registry answer, the
  // settling of a registry read, or the registry coming back is what makes that
  // decision change.
  // The last SETTLED registry phase: `fetch` passes through "loading" on the way
  // back, so a recovery is error -> (loading) -> ready, not error -> ready.
  const lastSettledPhase = useRef<HostsLoadState["phase"] | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: trigger-only deps - see comment above
  useEffect(() => {
    const recovered = lastSettledPhase.current === "error" && load.phase === "ready";
    if (load.phase !== "loading") lastSettledPhase.current = load.phase;
    if (isLocalHost(host) || client === null || connection !== "ready") return;
    // A registry that failed and then answered again re-reads this host once: the
    // failure is what held every read back, and an unchanged snapshot advances no
    // revision, so nothing else would. One read per recovery - not per poll tick.
    if (!recovered) {
      if (!shouldRead) return;
      // Re-checked against the store rather than the rendered values: a sibling's
      // own mount effect can have started a registry read in this same commit, and
      // this read has to wait for that one.
      const live = hostsStore.getState();
      if (live.reading > 0 || live.load.phase === "error") return;
    }
    void fetchHost(host);
  }, [shouldRead, host, revision, reading, generation, load.phase]);
  if (current) return partition;
  // M1: the registry has FAILED and this host has no listing for the current
  // revision. The read is held (see shouldRead), so a consumer that knows
  // nothing about the registry - the spawn form's provider setup - would
  // otherwise sit on the pending state forever: no verdict, no retry. Hand it the
  // registry's own failure instead, in the state shape every consumer already
  // reads (`error`), so the failure is NAMEABLE and retryable from anywhere.
  // `instances` is EMPTY's shared array, so a consumer's dependency on it is
  // stable.
  if (load.phase === "error") return { ...EMPTY_HOST_INSTANCE_STATE, error: load.message };
  return PENDING_HOST_INSTANCE_STATE;
}

/** retryHostRead is a consumer's retry for a remote host's listing. The read is
 * held while the registry's own read has FAILED (see useHostInstances), so the
 * retry re-reads the registry first when that is what failed - the listing then
 * follows on its own; otherwise it re-reads the host. Consumers that know nothing
 * about the registry (the spawn form) get a retry that works anyway. */
export function retryHostRead(host: string): void {
  if (isLocalHost(host)) return;
  if (hostsStore.getState().load.phase === "error") {
    // The registry's own failure held the read back: re-read IT, and the host's
    // listing is read once it answers - even when its snapshot is unchanged and
    // advanced no revision (one retry, not a poll loop).
    void hostsStore
      .getState()
      .fetch()
      .then(() => {
        if (hostsStore.getState().load.phase !== "error") void fetchHost(host);
      });
    return;
  }
  void fetchHost(host);
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

/** fetchHost reads `host`'s own instance listing through evener/host/request
 * (component 07b), so the spawn form's provider setup describes the machine the
 * launch will use. The controller's host is not a partition - its rows are the
 * package store's - so this is a no-op for LOCAL_HOST and for an absent host;
 * callers read the controller's own store for those.
 *
 * The read is stamped with the registry revision it was issued under, so the
 * partition it commits is current only while the registry still holds that
 * answer. Starting it also CLEARS the partition: rows read under an older
 * revision must not show while this read is out. */
export async function fetchHost(host: string): Promise<void> {
  if (isLocalHost(host)) return;
  const client = connectionStore.getState().client;
  if (!client) return;
  const version = (hostRequestVersions.get(host) ?? 0) + 1;
  hostRequestVersions.set(host, version);
  const generation = hostInstancesStore.getState().generation;
  const registryRevision = hostsStore.getState().revision;
  const readPublished = hostsStore.getState().publishedRevision !== null;
  setHostPartition(host, (previous) => startHostRead(previous, registryRevision, generation, readPublished));
  try {
    const resp = await hostRequest(client, host, "evener/instance/list", {});
    if (version !== hostRequestVersions.get(host) || hostInstancesStore.getState().generation !== generation) return;
    setHostPartition(host, () => ({
      instances: resp.instances,
      availableProviders: resp.availableProviders,
      diagnostics: resp.diagnostics ?? [],
      writesRefused: resp.writesRefused ?? false,
      loading: false,
      error: null,
      registryRevision,
      read: true,
      readGeneration: generation,
      readPublished,
    }));
  } catch (err) {
    if (version !== hostRequestVersions.get(host) || hostInstancesStore.getState().generation !== generation) return;
    setHostPartition(host, (previous) => ({ ...previous, loading: false, error: errorText(err), read: false }));
  }
}

// startHostRead begins a read of `host`'s listing against `registryRevision`.
// Rows read under the SAME revision (a retry, a notification refetch) are kept
// while the read is out - they are still this host's as far as the registry is
// concerned. Rows from an OLDER revision (or none) are not: they are cleared, so
// nothing read under a snapshot the registry has moved past is ever shown.
// `read` is left as it was in the kept case, and false in the cleared one, so an
// unanswered read can never pass for an empty listing.
function startHostRead(
  previous: HostInstanceState,
  registryRevision: number,
  generation: number,
  readPublished: boolean,
): HostInstanceState {
  if (previous.registryRevision === registryRevision) {
    return { ...previous, loading: true, error: null, readGeneration: generation, readPublished };
  }
  return { ...EMPTY_HOST_INSTANCE_STATE, loading: true, registryRevision, readGeneration: generation, readPublished };
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
