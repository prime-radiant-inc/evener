import type { AppwireClientLike, HostEntry, HostMutationPair, HostRow } from "@evener/appwire-client";
import {
  committedMutationRow,
  createHostMutations,
  errorText,
  HOST_GATE_TIMEOUT_MS,
  HostMutationOutcomeError,
} from "@evener/appwire-client";
import { create, useStore } from "zustand";
import { connectedClientPort, connectionStore } from "./connection";
import { createSecureUUID } from "./secureUUID";

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
   * null while the CURRENT connection never has. `revision` alone cannot tell "no
   * snapshot has been published" from "a snapshot was published at revision 0", and
   * a remote listing read in the first state must not pass for verified once the
   * registry has been consulted and failed (see stores/credentials.ts's
   * useHostInstances). A client replacement resets it to null: the previous
   * connection's publish says nothing about the new one, which has published
   * nothing yet.
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
  /**
   * Per host, how many times the registry has reported it coming BACK - an
   * `attached` answer that follows one saying the host was away (see the
   * transition subscription below). A host-scoped pane depends on it so that a
   * host which was away when the pane read re-reads when it returns: the same
   * host under the same registration, so a re-read and never a remount.
   */
  attachEpochs: Record<string, number>;
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
  /**
   * Forced quiet re-read: issues a NEW list request even while a background
   * refresh is in flight, and answers whether THIS read's response became the
   * current snapshot (false when the read failed or another response already
   * owned the publish). `refresh` coalesces on the in-flight promise, and that
   * response was captured before the caller's refusal proved the snapshot
   * stale - a retry that waited on it would echo the same stale answer (the
   * deploy/restart stale-entry retry is the caller; the guarded remove's retry
   * re-reads through quietReRead directly for the same reason). Still a quiet read: no loading
   * flip, and a failure keeps the last snapshot.
   */
  reReadForced: () => Promise<boolean>;
  add: (entry: HostEntry) => Promise<HostRow>;
  /** expected is the pair of the row the edit dialog opened on. */
  update: (params: { name: string; entry: HostEntry; expected: HostMutationPair }) => Promise<HostRow>;
  connect: (name: string) => Promise<void>;
  remove: (name: string) => Promise<void>;
  resetForTests: () => void;
}

// requireClient resolves connectionStore's CURRENT client, labelled by this
// store - the shared port (stores/connection.ts), not a hand-rolled twin.
const { requireClient } = connectedClientPort("hosts");

// The guarded-mutation core and its constants live in the shared package
// (hostMutations.ts); this store's callers keep importing them from here.
export type { HostMutationPair } from "@evener/appwire-client";
export { ErrorStaleEntry, HOST_GATE_TIMEOUT_MS } from "@evener/appwire-client";

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

// The client the last revision was derived from, and the last NON-NULL one seen.
// A REPLACEMENT - a different client than the last non-null one, whether wired
// directly or after the store was momentarily cleared - is a different hub, so
// every snapshot read from the old one is invalid and its registry's publish
// says nothing about the new one. A disconnect (client -> null) or a reconnect of
// the SAME client is not a replacement - the rows it read still describe it - so
// they survive, as they always have; only a different non-null client is one.
let lastClient = connectionStore.getState().client;
let lastNonNullClient: AppwireClientLike | null = lastClient;
connectionStore.subscribe((state) => {
  if (state.client === lastClient) return;
  lastClient = state.client;
  if (state.client === null) return;
  const replaced = lastNonNullClient !== null && state.client !== lastNonNullClient;
  lastNonNullClient = state.client;
  if (replaced) {
    // A different hub whose registry has answered nothing. Drop the old
    // connection's quiet-read slot: a refresh after the swap must issue its OWN
    // request rather than join a promise whose response the client fence below
    // will discard. The outstanding `reading` count is left intact - the old
    // connection's in-flight reads decrement it themselves when they settle, so
    // it never collides with a read this connection starts. Advance the
    // revision, clear the published marker, and reset the load to "loading": the
    // new connection's registry is unconsulted, so a remote listing is re-read
    // on this connection alone (the spawn-only path, stores/credentials.ts's
    // useHostInstances) instead of being held. publishReady lets this
    // connection's first answer publish - and advance the revision - even when
    // byte-identical to the old connection's, by treating a null
    // publishedRevision as "nothing published by this connection yet".
    refreshInflight = null;
    hostsStore.setState((previous) => ({
      revision: previous.revision + 1,
      publishedRevision: null,
      load: { phase: "loading" },
    }));
  }
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

// --- the guarded-mutation pair -----------------------------------------------
//
// `update` and `remove` carry the idempotency key plus the (generation,
// incarnation id) pair the row they target currently has (registry spec 08 §4,
// §11): the hub refuses a mismatch with the typed `stale-entry` refusal, so a
// caller whose snapshot moved must re-read and retry instead of overwriting an
// intervening change. The one rule this store keeps: the pair always comes from
// a row the registry answered — the held snapshot, or a fresh list read — never
// from a fabricated or defaulted value.

/** heldPairFor reads name's pair from the store's current ready snapshot, or
 * undefined when no row for the name is held. */
function heldPairFor(name: string): HostMutationPair | undefined {
  const load = hostsStore.getState().load;
  if (load.phase !== "ready") return undefined;
  const row = load.hosts.find((candidate) => candidate.name === name);
  if (row === undefined) return undefined;
  return { generation: row.generation, incarnationId: row.incarnationId };
}

// The shared guarded update/remove (hostMutations.ts), reading pairs off this
// store's snapshot and re-reading through its quiet read. quietReRead, not
// refresh: refresh coalesces on an in-flight poll whose response predates the
// refusal it is recovering from.
const hostMutations = createHostMutations({
  client: requireClient,
  heldPair: heldPairFor,
  reRead: quietReRead,
  newMutationId: createSecureUUID,
});

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
    // The guarded-mutation pair is part of the row's contract (registry spec 08
    // §11): a new generation with otherwise identical content is a row the
    // stale-retry path must observe, so the publish guard cannot swallow it.
    a.generation === b.generation &&
    a.incarnationId === b.incarnationId &&
    a.attached === b.attached &&
    a.serverName === b.serverName &&
    a.serverVersion === b.serverVersion &&
    a.hubVersion === b.hubVersion &&
    a.os === b.os &&
    a.arch === b.arch &&
    a.lastAttachError === b.lastAttachError &&
    a.midAttach === b.midAttach &&
    a.removed === b.removed &&
    // The remnant fence's own fields are part of the row's contract too
    // (registry spec 08 §11): a remnant that opens, escalates, or clears with
    // no other row change must publish, or the repair affordance appears late
    // or never clears (S16).
    a.openRemnantId === b.openRemnantId &&
    // Presence only: the age is the whole-second age of the remnant's commit
    // instant, recomputed on every read, so comparing the integer would make
    // every poll publish (and advance the revision its caches key on). The
    // affordance keys off presence, and the undefined <-> defined transition
    // still publishes.
    (a.escalationAgeSec === undefined) === (b.escalationAgeSec === undefined)
  );
}

// publishReady is the one generation-guarded ready-publish fetch and refresh
// share. A response is discarded only when a NEWER response already
// published; the accepted decision still advances the published marker so an
// older in-flight response can never publish after it. When the currently
// published rows are already exactly the ones this response carries AND a
// snapshot has been published for this connection, the setState is skipped: the
// 2s poll would otherwise swap in a fresh array every tick and force a
// re-render of an unchanged section. The published check is what lets the NEW
// connection's first answer publish - and advance the revision the caches key
// on - even when a quiet read (refresh, which never flips the load to
// "loading") carries rows byte-identical to the replaced connection's. It answers
// whether THIS generation was accepted (true when it publishes or accepts the
// equal snapshot, false on either discard) — forcedReRead reports that answer
// to its caller, and the shared marker cannot stand in for it: a mutation's
// re-read fence writes the marker ahead of its own response, so a discarded
// response can still see `latestPublishedGeneration === generation`.
function publishReady(generation: number, client: AppwireClientLike, hosts: HostRow[]): boolean {
  if (generation <= latestPublishedGeneration) {
    return false;
  }
  // The client fence (round 7, finding 2): a snapshot read through a connection
  // that has since been replaced describes the hub that was, so it must not
  // publish over the replacement's answer. The published marker is left where it
  // is - nothing published - so the client now connected still publishes its own.
  if (connectionStore.getState().client !== client) {
    return false;
  }
  latestPublishedGeneration = generation;
  const load = hostsStore.getState().load;
  // No snapshot has been published for the CURRENT connection yet: a fresh
  // session, or a client replacement. This answer is that connection's first, so
  // the memo is dropped and it publishes - advancing the revision the caches key
  // on - even when it carries rows byte-identical to the replaced connection's.
  // Without this a quiet read (refresh, which never flips the load to "loading")
  // would take the equal-snapshot short-circuit, publish nothing, and leave
  // publishedRevision null, holding every remote listing on "loading" forever
  // (useHostInstances' shouldRead requires a published marker once the registry
  // has been consulted).
  const firstPublishForConnection = hostsStore.getState().publishedRevision === null;
  if (
    !firstPublishForConnection &&
    load.phase === "ready" &&
    load.hosts.length === hosts.length &&
    load.hosts.every((row, i) => hostRowEqual(row, hosts[i]))
  ) {
    // The same answer again, with a snapshot already published for this
    // connection: publish nothing and advance nothing, so nothing derived from
    // the registry re-reads on the poll cadence.
    return true;
  }
  if (firstPublishForConnection) lastPublished = null;
  hostsStore.setState({ load: { phase: "ready", hosts } });
  return true;
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

/** registrySaysHostGone answers whether the registry's own answer says `host` is
 * not a configured host: not listed at all, or listed as removed. It is the ONE
 * predicate the frame's refusal (hostScopedSurface.tsx) and the per-host
 * registries' eviction (stores/launchConfig.ts, stores/extensions.ts,
 * stores/agentsDoc.ts) share, so the two can never disagree about what "gone"
 * means - a host refused by one and served by the other is exactly the leak this
 * exists to close.
 *
 * It answers false while the registry has no answer - a read in flight, or one
 * that failed - because an unanswered registry is no evidence: evicting on it
 * would drop a host's caches for a host nobody has described yet. A host that is
 * merely UNATTACHED is still a configured row (attachment is live session state,
 * not the registration, see hostInstanceIdentity), so it is never gone either. */
export function registrySaysHostGone(load: HostsLoadState, host: string): boolean {
  return load.phase === "ready" && !isConfiguredHost(load, host);
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

// forcedReRead is the quiet read that reports whether its OWN response became
// the current snapshot. reReadForced's caller (the deploy/restart stale-entry
// retry) must never read a pair off a snapshot older than the refusal it is
// recovering from: a failed read keeps the last snapshot, and a publish that
// another response already owns changes nothing, so both answer false.
async function forcedReRead(): Promise<boolean> {
  const generation = ++latestGeneration;
  beginListRequest();
  try {
    const read = await listHosts();
    return publishReady(generation, read.client, read.hosts);
  } catch {
    // A failed quiet read keeps the last snapshot: the mutation or poll that
    // issued it still stands, the rows stay rendered, and the next tick
    // retries anyway.
    return false;
  } finally {
    endListRequest();
  }
}

// quietReRead is the shared quiet list read (forcedReRead, its answer
// discarded): publishReady's generation-guarded publish with neither fetch's
// loading skeleton nor its error state. refresh runs it behind the in-flight
// gate for the background poll, and the mutation paths run it through
// reReadAfterMutation once their mutation has landed — the mutation's own
// response already proved the change landed, so a failed re-read must not flip
// the section to the error state and hide the rows that were rendering; the
// rows stay, and the next poll or fetch converges them.
async function quietReRead(): Promise<void> {
  await forcedReRead();
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

// reReadOnOutcomeError runs a guarded mutation's commit and then the quiet
// re-read: on success as before, and on the one rejection that is a
// non-commit ARM of the mutation-result union (HostMutationOutcomeError,
// hostMutations.ts), which may still have committed, so its row has to appear.
// Any other rejection - a hub refusal (WireError) - passes through with no
// re-read, exactly as before.
async function reReadOnOutcomeError<T>(commit: () => Promise<T> | T): Promise<T> {
  let result: T;
  try {
    result = await commit();
  } catch (error) {
    if (!(error instanceof HostMutationOutcomeError)) throw error;
    await reReadAfterMutation();
    throw error;
  }
  await reReadAfterMutation();
  return result;
}

export const hostsStore = create<HostsStoreState>((set) => ({
  load: { phase: "loading" },
  revision: 0,
  publishedRevision: null,
  reading: 0,
  attachEpochs: {},

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

  reReadForced: () => forcedReRead(),

  add: async (entry) => {
    // The wire carries one entry object (component 08 slice 2's shape, the
    // design record's own), so the store passes what the dialog collected
    // straight through rather than picking three fields out of it. The
    // mutationId is the add's client idempotency key (registry spec 08 §4):
    // a keyed add is dedup-safe, so a transport-level retry cannot double-add.
    const result = await requireClient().request("evener/host/add", { entry, mutationId: createSecureUUID() });
    // The result is the mutation-result union (registry spec 08 §11), so the
    // store narrows it explicitly: only the committed arm is a success, and the
    // teardown-failure, collision-dropped, and ambiguous arms throw. The
    // re-read (rather than appending: the server owns ordering and the row's
    // attached state, and the list read is cheap and never dials) runs for
    // every arm.
    return reReadOnOutcomeError(() => committedMutationRow(result, "evener/host/add"));
  },

  update: async (params) => {
    // The host being edited is named by the request's own field: name is
    // immutable, so it is the target rather than a value here (spec §3.1).
    // The guarded pair travels with it as params.expected, the pair of the row
    // the edit dialog opened on; a stale-entry refusal rejects unretried, so
    // an edit someone else made meanwhile is never overwritten.
    return reReadOnOutcomeError(() => hostMutations.update(params));
  },

  connect: async (name) => {
    // The same fire-and-wait the spawn picker's Connect trigger issues
    // (Spawn.tsx's connectHost): evener/host/attach, then re-read the row.
    // A failure throws to the caller — the row keeps its retry affordance.
    await requireClient().request("evener/host/attach", { host: name }, { timeoutMs: HOST_GATE_TIMEOUT_MS });
    await reReadAfterMutation();
  },

  remove: async (name) => {
    await reReadOnOutcomeError(() => hostMutations.remove(name));
  },

  resetForTests: () => {
    hostInstanceIdentities.clear();
    nextHostInstanceToken = 1;
    lastAttached.clear();
    refreshInflight = null;
    latestGeneration = 0;
    latestPublishedGeneration = 0;
    lastClient = connectionStore.getState().client;
    lastNonNullClient = lastClient;
    lastPublished = null;
    set({ load: { phase: "loading" }, revision: 0, publishedRevision: null, reading: 0, attachEpochs: {} });
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

// --- attachment transitions --------------------------------------------------
//
// A remote host that is merely AWAY refuses evener/host/request (the hub has no
// channel to forward through), so a pane that loaded in that window sits on its
// failure. Nothing else re-issues that read: a host's attachment is live session
// state, deliberately not part of the registration identity
// (hostInstanceIdentity above), so the per-host store instance is the same
// across an attach cycle and every pane's [store]-keyed load effect stays put.
//
// So the registry's own answer drives the retry: when a host's `attached` flips
// from false to true, its attach epoch advances and a host-scoped pane that
// depends on the epoch re-issues its read. There is no host lifecycle
// notification on the wire (see HostPicker), and the 2s poll is what reports the
// transition, so this is the only signal available.
//
// It is emphatically NOT an identity. A re-registration is a change of the
// registration FIELDS, which rebuilds the instance and remounts the body
// (hostInstanceIdentity); this advances for a live session flag on the SAME
// registration, so nothing is remounted and nothing reseeded - a reconnect
// refreshes the data and leaves what the user typed alone. The two cannot be
// confused: one is compared from the row's configuration fields, the other from
// `attached` alone, and a host that re-registers does not advance this unless it
// also reports the away -> back sequence.
//
// Only false -> true advances it, and only for a host the registry has already
// described: a host first seen attached has not come back from anywhere, and a
// host reported away has nothing to re-read. `lastAttached` is deliberately not
// cleared by a re-read or a failed read - a section's foreground fetch passes
// through "loading" with no rows at all, and the flip that follows it must still
// be seen (a memory derived from the previous snapshot alone would lose it).
const lastAttached = new Map<string, boolean>();
hostsStore.subscribe((state) => {
  if (state.load.phase !== "ready") return;
  let next: Record<string, number> | null = null;
  for (const row of state.load.hosts) {
    const was = lastAttached.get(row.name);
    if (was === row.attached) continue;
    if (row.attached && was === false) {
      next ??= { ...state.attachEpochs };
      next[row.name] = (next[row.name] ?? 0) + 1;
    }
    lastAttached.set(row.name, row.attached);
  }
  if (next !== null) hostsStore.setState({ attachEpochs: next });
});

/** useHostAttachEpoch is `host`'s attach epoch: it advances each time the
 * registry reports this host coming back from away, and never otherwise. A
 * host-scoped pane that can be left on a failure state depends on it so a host
 * that returns re-issues the read it refused while it was gone (see the
 * transition block above). */
export function useHostAttachEpoch(host: string): number {
  return useHostsStore((state) => state.attachEpochs[host] ?? 0);
}

export function useHostsStore<T>(selector: (state: HostsStoreState) => T): T {
  return useStore(hostsStore, selector);
}
