import type { AppwireClientLike, HostEntry, HostRow } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { create, useStore } from "zustand";
import { connectedClientPort, connectionStore } from "./connection";

// hosts.ts is the Hosts settings section's store (component 08 slice 1):
// the evener/host/list cache plus the add/Connect/remove calls behind the
// section's rows, add dialog, and confirm. It follows the settingsOverview
// pattern: no persistent subscription, the client resolves fresh from
// connectionStore at request time, so reconnects need no rewiring.

export type HostsLoadState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; hosts: HostRow[] };

interface HostsStoreState {
  load: HostsLoadState;
  /**
   * The one version of "the registry's answer could have changed": it advances
   * when a materially DIFFERENT snapshot is published, and when the connection's
   * client is replaced. Everything cached against a registry snapshot records
   * this revision and is current only while it still matches (see
   * stores/credentials.ts's useHostInstances) - so a re-registration, a removal
   * and re-add, and a hub swap invalidate the same way, with no second
   * mechanism. An unchanged poll publishes nothing and does not advance it, so
   * nothing re-reads on the poll cadence.
   */
  revision: number;
  /**
   * The revision at which the registry last SUCCESSFULLY published a snapshot, or
   * null while it never has. `revision` alone cannot tell "no snapshot has been
   * published" from "a snapshot was published at revision 0", and a remote
   * listing read in the first state must not pass for verified once the registry
   * has been consulted and failed (see stores/credentials.ts's useHostInstances).
   */
  publishedRevision: number | null;
  /**
   * How many registry list requests are in flight right now. A read issued
   * against the registry's CURRENT answer is what a partition records, so a
   * remote listing waits for one that is already on its way rather than reading
   * under an answer that is about to be replaced (see stores/credentials.ts's
   * useHostInstances). A COUNT, not a flag: a foreground fetch and the poll can
   * overlap, and one of them settling must not say "not reading" while the other
   * request is still out.
   */
  reading: number;
  fetch: () => Promise<void>;
  /**
   * Quiet re-read for the section's background poll. Unlike fetch it never
   * flips the phase to "loading" (the rows stay rendered) and a failure keeps
   * the last snapshot instead of blanking the section, because the next tick
   * retries. The mid-attach poll calls it while any row reports a server-side
   * attach in flight, so a host that settles outside this tab's actions (the
   * SSH supervisor's background reconnect, another client's Connect) still
   * converges instead of sitting on "connecting" forever.
   */
  refresh: () => Promise<void>;
  add: (entry: HostEntry) => Promise<HostRow>;
  update: (params: { name: string; entry: HostEntry }) => Promise<HostRow>;
  connect: (name: string) => Promise<void>;
  remove: (name: string) => Promise<void>;
  resetForTests: () => void;
}

// requireClient resolves connectionStore's CURRENT client, labelled by this
// store - the shared port (stores/connection.ts), not a hand-rolled twin.
const { requireClient } = connectedClientPort("hosts");

// HOST_GATE_TIMEOUT_MS is the client-side bound for a host RPC whose server
// side queues on or holds the per-host gate. A supervisor may hold that gate
// for a whole reconnect/ensure cycle, beyond the client's ordinary deadline.
const HOST_GATE_TIMEOUT_MS = 35 * 60_000;

// At most one background refresh runs at a time; concurrent callers join the
// same promise (mirrors stores/daemonResidents.ts).
let refreshInflight: Promise<void> | null = null;

// The two ends of one registry list request, so `reading` counts every request
// whichever path issued it. Hoisted function declarations: they are called from
// quietReRead and fetch, which are defined above the store they update.
function beginListRequest(): void {
  hostsStore.setState((previous) => ({ reading: previous.reading + 1 }));
}
function endListRequest(): void {
  hostsStore.setState((previous) => ({ reading: Math.max(0, previous.reading - 1) }));
}

// The client the last revision was derived from. A REPLACEMENT (both sides
// non-null and different) is a different hub, so every snapshot read from the
// old one is invalid: advance the revision. A disconnect or reconnect of the
// same client is not a replacement - the rows it read still describe it - so
// they survive, as they always have.
let lastClient = connectionStore.getState().client;
connectionStore.subscribe((state) => {
  if (state.client === lastClient) return;
  const replaced = lastClient !== null && state.client !== null;
  lastClient = state.client;
  if (replaced) hostsStore.setState((previous) => ({ revision: previous.revision + 1 }));
});

// latestGeneration increments with every list request fetch, refresh, and
// mutation re-read issue, ordering the responses; latestPublishedGeneration
// records the newest generation whose response was accepted to publish
// (mirrors stores/daemonResidents.ts). fetch is the section's foreground
// load, refresh the background poll, and the mutation re-reads their own
// requests: without a publish guard, a background response that lands after
// an add/remove's re-read would overwrite the newer rows. Comparing against
// the request counter alone is not enough: a request that FAILED advanced it
// too, so fetch's earlier success would be discarded and the section would
// stay on the loading skeleton. A response is stale only when a newer
// response published — a failed request publishes nothing and invalidates
// nothing.
let latestGeneration = 0;
let latestPublishedGeneration = 0;

// listHosts reads the section's one endpoint, and reports WHICH client answered:
// the registry snapshot belongs to that connection, so a response whose client
// has since been replaced must not publish. It throws on a transport or server
// failure and on a response with no hosts array, so fetch can surface the
// failure and refresh can swallow it.
async function listHosts(): Promise<{ client: AppwireClientLike; hosts: HostRow[] }> {
  const client = requireClient();
  const res = await client.request("evener/host/list", {});
  if (res.hosts === undefined) throw new Error("evener/host/list returned no hosts");
  return { client, hosts: res.hosts };
}

// sameRoots compares two rows' roots as lists, treating an absent value and an
// empty one as the same: the wire omits empty optional arrays, so the two
// spellings describe the same host, and neither may make the comparator report a
// change the publish guard would re-render for.
function sameRoots(a: readonly string[] | undefined, b: readonly string[] | undefined): boolean {
  const left = a ?? [];
  const right = b ?? [];
  return left.length === right.length && left.every((value, i) => value === right[i]);
}

// hostRowEqual compares two rows on every field the wire contract carries;
// an absent row counts as different, so the poll's skip test can pass a
// possibly-undefined index result straight through.
function hostRowEqual(a: HostRow, b: HostRow | undefined): boolean {
  return (
    b !== undefined &&
    a.name === b.name &&
    a.address === b.address &&
    a.user === b.user &&
    a.keyPath === b.keyPath &&
    a.evenerPath === b.evenerPath &&
    a.configPath === b.configPath &&
    a.addr === b.addr &&
    sameRoots(a.roots, b.roots) &&
    a.origin === b.origin &&
    a.attached === b.attached &&
    a.serverName === b.serverName &&
    a.serverVersion === b.serverVersion &&
    a.hubVersion === b.hubVersion &&
    a.os === b.os &&
    a.arch === b.arch &&
    a.lastAttachError === b.lastAttachError &&
    a.midAttach === b.midAttach &&
    a.removed === b.removed
  );
}

// publishReady is the one generation-guarded ready-publish fetch and refresh
// share. A response is discarded only when a NEWER response already
// published; the accepted decision still advances the published marker so an
// older in-flight response can never publish after it. When the currently
// published rows are already exactly the ones this response carries, the
// setState is skipped: the 2s poll would otherwise swap in a fresh array
// every tick and force a re-render of an unchanged section.
function publishReady(generation: number, client: AppwireClientLike, hosts: HostRow[]): void {
  if (generation <= latestPublishedGeneration) {
    return;
  }
  // The client fence (round 7, finding 2): a snapshot read through a connection
  // that has since been replaced describes the hub that was, so it must not
  // publish over the replacement's answer. The published marker is left where it
  // is - nothing published - so the client now connected still publishes its own.
  if (connectionStore.getState().client !== client) {
    return;
  }
  latestPublishedGeneration = generation;
  const load = hostsStore.getState().load;
  if (
    load.phase === "ready" &&
    load.hosts.length === hosts.length &&
    load.hosts.every((row, i) => hostRowEqual(row, hosts[i]))
  ) {
    // The same answer again: publish nothing and advance nothing, so nothing
    // derived from the registry re-reads on the poll cadence.
    return;
  }
  hostsStore.setState({ load: { phase: "ready", hosts } });
}

/** selectableHostRows is the registry's non-removed rows, or none while it is
 * still loading or has failed. Shared by the picker and by the panes that must
 * decide what an unknown host means. */
export function selectableHostRows(load: HostsLoadState): HostRow[] {
  return load.phase === "ready" ? load.hosts.filter((row) => !row.removed) : [];
}

/** isConfiguredHost answers whether the registry currently lists `host`. A false
 * while the registry is still unread is ambiguous, so callers pair it with the
 * load phase. */
export function isConfiguredHost(load: HostsLoadState, host: string): boolean {
  return selectableHostRows(load).some((row) => row.name === host);
}

// --- registration identity ---------------------------------------------------
//
// The registry's own row is the identity every per-host store instance is built
// against (stores/launchConfig.ts, stores/extensions.ts, stores/agentsDoc.ts):
// a host removed and re-added under the same name is a DIFFERENT registration,
// and that registration's schema, catalogs and document are its own, not the
// previous one's. Keying an instance by the name alone serves the previous
// registration's cache to the new one, and lets a response the old registration
// had in flight land in it.
//
// The identity is the row's REGISTRATION fields, not the whole row: the hub
// reports live session state on the same row (attach, versions, os/arch), and
// rebuilding an instance for one of those would throw away that host's cache
// and re-read everything on every attach. The registry's revision (above) is
// not usable for this either - it advances for those same live changes.

/** HostRegistration is what the registry says REGISTERS a host right now:
 *
 *   - a HostRow, when its current snapshot lists that host;
 *   - null, when its snapshot lists no such host (removed, or never added);
 *   - undefined, when it has no snapshot to answer from at all - still reading,
 *     or its read failed. That is no evidence either way, so an instance built
 *     before the answer is kept. */
export type HostRegistration = HostRow | null | undefined;

/** currentHostRegistration reads that answer for `host` from the registry's
 * current snapshot. */
export function currentHostRegistration(host: string): HostRegistration {
  const load = hostsStore.getState().load;
  if (load.phase !== "ready") return undefined;
  return load.hosts.find((row) => row.name === host) ?? null;
}

/** sameHostRegistration compares two rows on the fields that REGISTER a host -
 * the configuration a per-host store instance and its caches are built against
 * - and deliberately not on the live session fields the hub reports about a
 * connection (attached, serverName/serverVersion/hubVersion, os/arch,
 * lastAttachError, midAttach): those move without the registration changing, so
 * an instance rebuilt on one would lose that host's cache for no reason. Roots
 * compare as lists, so an absent and an empty one describe the same host (the
 * wire omits empty optional arrays - sameRoots). */
export function sameHostRegistration(a: HostRow, b: HostRow): boolean {
  return (
    a.name === b.name &&
    a.address === b.address &&
    a.user === b.user &&
    a.keyPath === b.keyPath &&
    a.evenerPath === b.evenerPath &&
    a.configPath === b.configPath &&
    a.addr === b.addr &&
    sameRoots(a.roots, b.roots) &&
    a.origin === b.origin &&
    a.removed === b.removed
  );
}

/** hostRegistrationChanged answers whether an instance recorded against
 * `recorded` is invalidated by the registry's current answer `current`:
 *
 *   - no answer (undefined) is no evidence, so nothing changes;
 *   - an instance built before the registry ever answered (recorded undefined)
 *     ADOPTS the first answer rather than being rebuilt: nothing has named that
 *     host's registration differently yet, and a name-addressed instance is the
 *     same host wherever the row points (the deep-link case - a pane can resolve
 *     a remote host before the picker's own list read lands);
 *   - otherwise any change invalidates it - a different registration, the row
 *     appearing, or the host leaving the registry.
 *
 * An unchanged snapshot re-publishes nothing (see publishReady) and even a
 * changed one leaves every host whose configuration did not move with the same
 * registration, so nothing here is rebuilt on the poll cadence or on an attach.
 */
export function hostRegistrationChanged(recorded: HostRegistration, current: HostRegistration): boolean {
  if (current === undefined || recorded === undefined) return false;
  if (recorded === null || current === null) return recorded !== current;
  return !sameHostRegistration(recorded, current);
}

// --- the identity a pane's body is keyed on ----------------------------------
//
// hostScopedSurface keys a pane's BODY on this, and the launcher hands it to
// LaunchConfigForm as the owner of its draft. A body left mounted across a
// re-registration keeps pane-local state that belongs to the registration the
// user left - a launch draft, an MCP add-form's draft, a pending Remove
// confirmation - and Save then writes it to the NEW registration. The host NAME
// cannot express that: a host removed and re-added under the same name has the
// same one. `revision` cannot either: it advances for the live session fields an
// attach moves, which must not remount a body whose host never moved.
//
// So this is the identity of the per-host store INSTANCE that answer implies,
// derived by the SAME rule the instances themselves are built on
// (currentHostRegistration + hostRegistrationChanged) and therefore changing
// exactly when one of them is rebuilt: a re-registration, an appearance after a
// removal, a removal. An instance built before the registry had an answer ADOPTS
// the first one rather than being replaced, and so does this - a body whose host
// came from a deep link is not remounted when the picker's own list read lands.
const hostInstanceIdentities = new Map<string, { token: number; registration: HostRegistration }>();
let nextHostInstanceToken = 1;

/** hostInstanceIdentity is the surface identity of `host`: a token that changes
 * exactly when the registry says that host's registration changed - and for no
 * other reason. It is what a React key or a draft owner is built on. */
export function hostInstanceIdentity(host: string): number {
  const current = currentHostRegistration(host);
  const recorded = hostInstanceIdentities.get(host);
  if (recorded !== undefined && !hostRegistrationChanged(recorded.registration, current)) {
    // The first answer after a token was minted before the registry had one
    // identifies it from here on; every other unchanged answer is the same one.
    if (current !== undefined) recorded.registration = current;
    return recorded.token;
  }
  const token = nextHostInstanceToken++;
  hostInstanceIdentities.set(host, { token, registration: current });
  return token;
}

// quietReRead is the shared quiet list read: publishReady's generation-guarded
// publish with neither fetch's loading skeleton nor its error state. refresh
// runs it behind the in-flight gate for the background poll, and the mutation
// paths run it through reReadAfterMutation once their mutation has landed —
// the mutation's own response already proved the change landed, so a failed
// re-read must not flip the section to the error state and hide the rows that
// were rendering; the rows stay, and the next poll or fetch converges them.
async function quietReRead(): Promise<void> {
  const generation = ++latestGeneration;
  beginListRequest();
  try {
    const read = await listHosts();
    publishReady(generation, read.client, read.hosts);
  } catch {
    // A failed quiet read keeps the last snapshot: the mutation or poll that
    // issued it still stands, the rows stay rendered, and the next tick
    // retries anyway.
  } finally {
    endListRequest();
  }
}

// reReadAfterMutation is the quiet list read behind a landed add, Connect, or
// remove. The mutation's response proved the change landed, so every list
// request already in flight carries the snapshot from before it: publishing
// one of those now would show rows the server has already changed — an added
// host missing, a removed one back — and the failed quiet read that leaves
// this path with nothing published issues no fence of its own, so an older
// response would otherwise still land. Setting the published marker to the
// newest generation issued so far fences exactly the responses that predate
// the mutation; this re-read issues after that, so it still publishes. The
// marker is only ever set from a generation that was issued, so it never
// exceeds the issued counter.
//
// The fence protects rows that are rendering, and only those: with none to
// protect — a foreground fetch (the reconnect or Retry path) parked the
// section on the loading skeleton and dropped the rows — it would instead
// strand that fetch, the one read left that can clear the skeleton, whenever
// this re-read's own request then fails. So it stands down while the section
// is loading and lets that response land; the next poll converges it either
// way.
async function reReadAfterMutation(): Promise<void> {
  if (hostsStore.getState().load.phase !== "loading") {
    latestPublishedGeneration = latestGeneration;
  }
  await quietReRead();
}

export const hostsStore = create<HostsStoreState>((set) => ({
  load: { phase: "loading" },
  revision: 0,
  publishedRevision: null,
  reading: 0,

  fetch: async () => {
    const generation = ++latestGeneration;
    // The client this read is issued on, so the failure path can tell whether it
    // still describes the connection that asked.
    const client = connectionStore.getState().client;
    set({ load: { phase: "loading" } });
    beginListRequest();
    try {
      const read = await listHosts();
      publishReady(generation, read.client, read.hosts);
    } catch (err) {
      // The error publish is guarded the same way and advances the published
      // marker with it: an older fetch's failure must not blank the rows a
      // newer response delivered, and a newer error must not be overwritten
      // by an older response landing after it. It is fenced by client too: a
      // failure on a connection that is gone says nothing about this one.
      if (generation > latestPublishedGeneration && connectionStore.getState().client === client) {
        latestPublishedGeneration = generation;
        set({ load: { phase: "error", message: errorText(err) } });
      }
    } finally {
      endListRequest();
    }
  },

  refresh: async () => {
    if (refreshInflight) return refreshInflight;
    // quietReRead is an async function, so it never throws synchronously: a
    // requireClient refusal inside it becomes the returned promise's
    // swallowed rejection, never an exception past this point. The cleanup
    // still cannot run before the assignment below stores the promise — a
    // cleanup that cleared refreshInflight while the assignment stored an
    // already-settled promise wedged the gate non-null forever, turning
    // every later refresh() into a no-op — so it lives in a finally callback
    // that also checks it is still the current one.
    const p = quietReRead();
    refreshInflight = p;
    return p.finally(() => {
      if (refreshInflight === p) {
        refreshInflight = null;
      }
    });
  },

  add: async (entry) => {
    // The wire carries one entry object (component 08 slice 2's shape, the
    // design record's own), so the store passes what the dialog collected
    // straight through rather than picking three fields out of it.
    const row = await requireClient().request("evener/host/add", { entry });
    // Re-read quietly rather than appending: the server owns ordering and
    // the row's attached state, and the list read is cheap and never dials.
    await reReadAfterMutation();
    return row;
  },

  update: async (params) => {
    // The host being edited is named by the request's own field: name is
    // immutable, so it is the target rather than a value here (spec §3.1).
    const { host: row } = await requireClient().request(
      "evener/host/update",
      {
        name: params.name,
        entry: params.entry,
      },
      { timeoutMs: HOST_GATE_TIMEOUT_MS },
    );
    await reReadAfterMutation();
    return row;
  },

  connect: async (name) => {
    // The same fire-and-wait the spawn picker's Connect trigger issues
    // (Spawn.tsx's connectHost): evener/host/attach, then re-read the row.
    // A failure throws to the caller — the row keeps its retry affordance.
    await requireClient().request("evener/host/attach", { host: name }, { timeoutMs: HOST_GATE_TIMEOUT_MS });
    await reReadAfterMutation();
  },

  remove: async (name) => {
    await requireClient().request("evener/host/remove", { name }, { timeoutMs: HOST_GATE_TIMEOUT_MS });
    await reReadAfterMutation();
  },

  resetForTests: () => {
    hostInstanceIdentities.clear();
    nextHostInstanceToken = 1;
    refreshInflight = null;
    latestGeneration = 0;
    latestPublishedGeneration = 0;
    lastClient = connectionStore.getState().client;
    lastPublished = null;
    set({ load: { phase: "loading" }, revision: 0, publishedRevision: null, reading: 0 });
  },
}));

// The revision is DERIVED from the published snapshot rather than bumped by any
// one writer: whenever the ready rows change - whoever wrote them - the
// registry's answer could have changed, so every partition cached against it is
// stale. An unchanged snapshot advances nothing, which is what keeps the 2s poll
// from re-reading anything.
//
// null means NOTHING has been published yet, which is not the same as having
// published an empty list: the registry's FIRST answer is an answer, so it
// advances the revision even when it names no host, and a read taken while the
// registry was unread cannot survive it.
let lastPublished: HostRow[] | null = null;
function sameSnapshot(a: readonly HostRow[], b: readonly HostRow[]): boolean {
  return a.length === b.length && a.every((row, i) => hostRowEqual(row, b[i]));
}
hostsStore.subscribe((state) => {
  if (state.load.phase !== "ready") return;
  const hosts = state.load.hosts;
  if (lastPublished !== null && sameSnapshot(lastPublished, hosts)) return;
  lastPublished = hosts;
  // The first answer - an empty list included - is an answer: it advances the
  // revision AND records that a snapshot now exists, which is what lets a remote
  // listing read before it stop counting as verified.
  hostsStore.setState((previous) => ({
    revision: previous.revision + 1,
    publishedRevision: previous.revision + 1,
  }));
});

export function useHostsStore<T>(selector: (state: HostsStoreState) => T): T {
  return useStore(hostsStore, selector);
}
