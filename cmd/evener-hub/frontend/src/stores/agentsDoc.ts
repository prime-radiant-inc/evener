// The personal AGENTS.md store: the hub-authoritative file behind
// Settings -> AGENTS.md (spec 2026-09-07 §1). One document, read whole and
// written whole; no revision or conflict handling by design - the file is
// also hand-edited, and the hub's own rule is last write wins. What the
// store DOES keep current is `doc`: every evener/settings/agentsDoc/changed
// broadcast (this client's own save included) replaces it, so a second tab
// or a save from the TUI lands here without a refetch.
//
// An editor must key off `doc.content`, never `doc` itself: one save yields
// two referentially-distinct `doc` objects carrying the same content - the
// save's own result, then the hub's broadcast echo behind it - so an
// identity-keyed effect would wipe anything typed during the round-trip.
//
// This module is the ONE exception among the host-scoped settings stores: the
// package exposes no agents-doc store, so the calls are written here, and they
// can route per call (component 07b). The controller's own document is the
// `agentsDocStore` singleton below, read through the plain connection; a remote
// host's is its own instance (agentsDocStoreForHost), whose two RPCs go through
// evener/host/request and whose `changed` broadcasts arrive wrapped for it. Two
// instances, never one - one `doc` cannot be two hosts' files at once.
//
// requireClient() throws outside any try/catch, matching stores/credentials.ts:
// "no client connected" is a programmer error, not a state to degrade into.

import type { AgentsDocResponse, AnyNotification, AppwireClientLike } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { connectedClientPort, connectionStore, onConnectionNotification } from "./connection";
import { hostRequest, isLocalHost } from "./hostRouting";
import { remoteHostStoreClient } from "./hostStoreClient";
import {
  currentHostRegistration,
  type HostRegistration,
  hostRegistrationChanged,
  hostsStore,
  registrySaysHostGone,
} from "./hosts";

export interface AgentsDocStoreState {
  /** The last document the hub confirmed - null until the first fetch lands. */
  doc: AgentsDocResponse | null;
  loading: boolean;
  error: string | null;
  fetch(): Promise<void>;
  /** Replaces the file whole. Resolves with the hub's view of the saved file,
   * which is also what `doc` becomes while this client is still the store's
   * and no newer view of the file landed behind the write. When one did - a
   * `changed` broadcast, or a read started after the write - that newer
   * document stands and is resolved in this response's place.
   * Rejections propagate: the section owns the inline error and the toast. */
  save(content: string): Promise<AgentsDocResponse>;
}

/** AgentsDocPort is where one instance's reads, writes and `changed`
 * broadcasts go: the plain connection for the controller's own document, the
 * evener/host/request proxy for a remote host's. `read`/`write` take the client
 * the instance already resolved, so a response can be fenced against the client
 * it was issued on (the store's own connection check). */
interface AgentsDocPort {
  requireClient(): AppwireClientLike;
  read(client: AppwireClientLike): Promise<AgentsDocResponse>;
  write(client: AppwireClientLike, content: string): Promise<AgentsDocResponse>;
  /** Subscribes to THIS instance's own agentsDoc notifications, detachably. */
  onNotification(cb: (n: AnyNotification) => void): () => void;
}

/** AgentsDocInstance is one host's document store plus the teardown a test uses
 * to end its subscriptions. */
interface AgentsDocInstance {
  store: StoreApi<AgentsDocStoreState>;
  /** Back to the not-yet-read state; the wiring is left alone (a production
   * singleton) unless the caller disposes, which unwires everything. */
  reset(): void;
  /** Terminal: unwires the notification and connection subscriptions. */
  dispose(): void;
}

function createAgentsDocInstance(port: AgentsDocPort): AgentsDocInstance {
  // Only the most recently started read may land: a response from an
  // overlapping older fetch, or from a client the store has since replaced,
  // carries a view of the file that is already out of date. requestedDoc is
  // what tells the reconnect subscriber below that a view has asked for this
  // file at all.
  let requestVersion = 0;
  let requestedDoc = false;

  const store = createStore<AgentsDocStoreState>((set, get) => ({
    doc: null,
    loading: false,
    error: null,

    async fetch() {
      const client = port.requireClient();
      requestedDoc = true;
      const version = ++requestVersion;
      set({ loading: true, error: null });
      try {
        const doc = await port.read(client);
        if (version !== requestVersion || connectionStore.getState().client !== client) return;
        // A fetch that lands has the hub's fresh document, so an earlier
        // reload's error is moot - as when a save lands and when a `changed`
        // broadcast arrives.
        set({ doc, loading: false, error: null });
      } catch (err) {
        if (version !== requestVersion || connectionStore.getState().client !== client) return;
        set({ loading: false, error: errorText(err) });
      }
    },

    async save(content) {
      const client = port.requireClient();
      // Bumped before the request, not after it: a read already in flight can
      // only land holding the pre-save file, so the write fences it out the way
      // a newer read does. The bump leaves nothing to turn `loading` off, so
      // the save does it, as the connection subscriber does for its own bump.
      const version = ++requestVersion;
      set({ loading: false });
      const doc = await port.write(client, content);
      // A response from a client the store has since replaced speaks for a
      // socket that is gone, so it goes back to the caller without landing.
      if (connectionStore.getState().client !== client) return doc;
      // Something bumped the version behind this write - a `changed` broadcast
      // carrying someone else's write, or a read started after this one - so
      // this branch commits nothing and defers to whatever that newer read or
      // broadcast lands, which is what keeps the store only ever moving
      // forward. The caller gets the newest document the store has, whose
      // content its content-keyed sync then shows against the draft, and this
      // response itself when the store has none: the `??` is honesty about the
      // type, since a bump that landed no document cannot follow a landed write.
      if (version !== requestVersion) return get().doc ?? doc;
      // A write that landed is the freshest view of the file there is, so an
      // earlier read's error is moot - and the section's notice for one
      // ("saving would overwrite anything changed on disk since") is now false.
      set({ doc, error: null });
      return doc;
    },
  }));

  // --- notification wiring --------------------------------------------------

  const stopNotifications = port.onNotification((n) => {
    if (n.method !== "evener/settings/agentsDoc/changed") return;
    // A broadcast is the hub's own authoritative view of the file, so a read
    // that started before it is stale by definition and any earlier reload
    // failure is moot. The bump leaves nothing to turn `loading` off, so the
    // broadcast does it, as the save and the connection subscriber do for theirs.
    ++requestVersion;
    store.setState({ doc: n.params, error: null, loading: false });
  });

  // React to the connection store rather than reading it once: this module can
  // be evaluated before AppShell's connect() effect runs (stores/extensions.ts
  // documents the mount-order race in full). The notification subscription is
  // the shared wiring (stores/connection.ts's thin wrapper over the package's
  // onConnectionNotification): it wires whichever client the store holds now and
  // re-wires on every swap, detaching the replaced one, so this module owns only
  // the connection transitions it acts on.
  const unsubscribeConnection = connectionStore.subscribe((state, previous) => {
    if (state.client !== previous.client || state.state !== previous.state) {
      requestVersion += 1;
      // The bump just dropped whatever read was in flight, so nothing is left
      // to turn `loading` off - and a drop that never recovers would leave the
      // section on its Skeleton forever (stores/credentials.ts does the same).
      store.setState({ loading: false });
    }
    // Once a view has read the file, every reconnect has to re-read it - an
    // automatic one reuses this same client, so a drop and recovery is a state
    // transition and nothing else. The `changed` broadcasts that landed while
    // the socket was down are gone, and the next Save would push pre-drop
    // content over whatever the hub now has.
    if (
      requestedDoc &&
      state.client &&
      state.state === "ready" &&
      (state.client !== previous.client || previous.state !== "ready")
    ) {
      void store
        .getState()
        .fetch()
        .catch(() => {});
    }
  });

  return {
    store,
    reset() {
      requestVersion += 1;
      requestedDoc = false;
      store.setState({ doc: null, loading: false, error: null });
    },
    dispose() {
      requestVersion += 1;
      stopNotifications();
      unsubscribeConnection();
    },
  };
}

const { requireClient } = connectedClientPort("agentsDoc");

const localInstance = createAgentsDocInstance({
  requireClient,
  read: (client) => client.request("evener/settings/agentsDoc/get", {}),
  write: (client, content) => client.request("evener/settings/agentsDoc/set", { content }),
  onNotification: (cb) => onConnectionNotification(cb),
});

export const agentsDocStore = localInstance.store;

export function useAgentsDocStore(): AgentsDocStoreState;
export function useAgentsDocStore<T>(selector: (state: AgentsDocStoreState) => T): T;
export function useAgentsDocStore<T>(selector?: (state: AgentsDocStoreState) => T): T | AgentsDocStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(agentsDocStore, selector) : useStore(agentsDocStore);
}

// --- Host-scoped instances (component 07b) ----------------------------------

interface AgentsDocHostEntry {
  instance: AgentsDocInstance;
  /** What the registry said REGISTERED this host when the instance was built
   * (stores/hosts.ts's HostRegistration). The instance's `doc` IS one host's
   * file, so a host removed and re-added under the same name must not be handed
   * the previous registration's document - and an in-flight read the old
   * registration owed must have nowhere left to land. */
  registration: HostRegistration;
}

const hostInstances = new Map<string, AgentsDocHostEntry>();

/** goneAgentsDocInstance is what a lookup for a host the registry does not list
 * returns: ONE shared instance over a port that refuses every read and write and
 * subscribes to nothing, so a host that is not registered is not dialed and no
 * subscription is left behind for a file nothing can reach. It is shared rather
 * than built per lookup - building one per call would itself wire a subscription
 * per call for a host that does not exist. */
const goneAgentsDocInstance = createAgentsDocInstance({
  requireClient() {
    throw new Error("this host is not registered: the registry lists no such host, so it has no AGENTS.md store");
  },
  read: async () => {
    throw new Error("this host is not registered: the registry lists no such host, so it has no AGENTS.md store");
  },
  write: async () => {
    throw new Error("this host is not registered: the registry lists no such host, so it has no AGENTS.md store");
  },
  onNotification: () => () => {},
});

/** agentsDocStoreForHost returns the AGENTS.md document store for `host`: the
 * controller's own singleton for the local hub (and for an absent host), and a
 * per-host instance for a remote one. Its reads and writes go through
 * evener/host/request, and only that host's own `changed` broadcasts - which
 * arrive wrapped in evener/host/notification - land in it, so a remote
 * selection never shows this hub's file and never writes it. */
export function agentsDocStoreForHost(host: string | null | undefined): StoreApi<AgentsDocStoreState> {
  if (isLocalHost(host)) return agentsDocStore;
  const name = host as string;
  const registration = currentHostRegistration(name);
  const recorded = hostInstances.get(name);
  // The registry says this host is not configured (stores/hosts.ts's
  // registrySaysHostGone - the same answer the eviction subscription above acts
  // on): nothing is kept for it, so a lookup that races that subscription - or
  // one made before this module was loaded - cannot rebuild an instance whose
  // subscriptions nothing would ever unwind.
  if (registrySaysHostGone(hostsStore.getState().load, name)) {
    evictHostInstance(name);
    return goneAgentsDocInstance.store;
  }
  if (recorded !== undefined && !hostRegistrationChanged(recorded.registration, registration)) {
    // The first answer after an instance was built before the registry had one
    // identifies it from here on; every other unchanged answer is the same one.
    if (registration !== undefined) recorded.registration = registration;
    return recorded.instance.store;
  }
  // A re-registration: the replaced instance's notification and connection
  // subscriptions are unwired (dispose() is terminal) so it cannot keep
  // fetching, or land a document, for a registration the registry has left.
  if (recorded !== undefined) recorded.instance.dispose();
  const instance = createAgentsDocInstance(remotePort(name));
  hostInstances.set(name, { instance, registration });
  return instance.store;
}

/** useAgentsDocStoreForHost reads the AGENTS.md store for `host`: the
 * controller's own singleton for the local hub, a per-host instance for a
 * remote one. */
export function useAgentsDocStoreForHost<T>(
  host: string | null | undefined,
  selector: (state: AgentsDocStoreState) => T,
): T {
  return useStore(agentsDocStoreForHost(host), selector);
}

function remotePort(host: string): AgentsDocPort {
  const client = remoteHostStoreClient(host);
  return {
    requireClient() {
      const current = connectionStore.getState().client;
      if (!current) {
        throw new Error(
          `host ${host} store: no client connected; call connectionStore.getState().connect(client) first`,
        );
      }
      return current;
    },
    read: (current) => hostRequest(current, host, "evener/settings/agentsDoc/get", {}),
    write: (current, content) => hostRequest(current, host, "evener/settings/agentsDoc/set", { content }),
    onNotification: client.onNotification,
  };
}

/** evictHostInstance unwires `host`'s instance and drops it. dispose() is
 * terminal, which is what keeps a removed host's `changed` subscription and its
 * connection subscriber from outliving the registration they were built for. */
function evictHostInstance(name: string): void {
  const recorded = hostInstances.get(name);
  if (recorded === undefined) return;
  recorded.instance.dispose();
  hostInstances.delete(name);
}

// The registry's own transition evicts - never a lookup. A host that leaves the
// registry is rendered as the frame's own refusal instead of its body
// (hostScopedSurface.tsx), so nothing calls this module's accessor for it again:
// left to the accessor, the instance would stay in this map for the rest of the
// session with its subscriptions live, and its connection subscriber would go on
// re-reading a document for a host the registry no longer lists - forwarding
// evener/host/request on every reconnect. Only the registry's ANSWERED "gone"
// evicts (stores/hosts.ts's registrySaysHostGone), so a host that is merely
// unattached, or one whose read has not answered yet, is never dropped.
hostsStore.subscribe(() => {
  const load = hostsStore.getState().load;
  for (const name of [...hostInstances.keys()]) {
    if (registrySaysHostGone(load, name)) evictHostInstance(name);
  }
});

/** resetAgentsDocHostInstancesForTests disposes and forgets every per-host
 * instance, so a test's remote hosts do not leak subscriptions into the next
 * test. No production code should call this. */
export function resetAgentsDocHostInstancesForTests(): void {
  for (const entry of hostInstances.values()) entry.instance.dispose();
  hostInstances.clear();
}

// resetAgentsDocStoreForTests resets the singleton between tests. The
// notification wiring above is left alone: onConnectionNotification detaches
// the outgoing client on its own the moment the store's `client` clears (the
// tests' own beforeEach does that), so a reset has nothing to unwind. No
// production code should ever call this.
export function resetAgentsDocStoreForTests(): void {
  localInstance.reset();
}
