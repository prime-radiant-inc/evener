import type { HostEntry, HostRow } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { create, useStore } from "zustand";
import { connectedClientPort } from "./connection";

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

// At most one background refresh runs at a time; concurrent callers join the
// same promise (mirrors stores/daemonResidents.ts).
let refreshInflight: Promise<void> | null = null;

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

// listHosts reads the section's one endpoint. It throws on a transport or
// server failure and on a response with no hosts array, so fetch can surface
// the failure and refresh can swallow it.
async function listHosts(): Promise<HostRow[]> {
  const res = await requireClient().request("evener/host/list", {});
  if (res.hosts === undefined) throw new Error("evener/host/list returned no hosts");
  return res.hosts;
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
function publishReady(generation: number, hosts: HostRow[]): void {
  if (generation <= latestPublishedGeneration) {
    return;
  }
  latestPublishedGeneration = generation;
  const load = hostsStore.getState().load;
  if (
    load.phase === "ready" &&
    load.hosts.length === hosts.length &&
    load.hosts.every((row, i) => hostRowEqual(row, hosts[i]))
  ) {
    return;
  }
  hostsStore.setState({ load: { phase: "ready", hosts } });
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
  try {
    publishReady(generation, await listHosts());
  } catch {
    // A failed quiet read keeps the last snapshot: the mutation or poll that
    // issued it still stands, the rows stay rendered, and the next tick
    // retries anyway.
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

  fetch: async () => {
    const generation = ++latestGeneration;
    set({ load: { phase: "loading" } });
    try {
      publishReady(generation, await listHosts());
    } catch (err) {
      // The error publish is guarded the same way and advances the published
      // marker with it: an older fetch's failure must not blank the rows a
      // newer response delivered, and a newer error must not be overwritten
      // by an older response landing after it.
      if (generation > latestPublishedGeneration) {
        latestPublishedGeneration = generation;
        set({ load: { phase: "error", message: errorText(err) } });
      }
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
      { timeoutMs: 35 * 60_000 },
    );
    await reReadAfterMutation();
    return row;
  },

  connect: async (name) => {
    // The same fire-and-wait the spawn picker's Connect trigger issues
    // (Spawn.tsx's connectHost): evener/host/attach, then re-read the row.
    // A failure throws to the caller — the row keeps its retry affordance.
    await requireClient().request("evener/host/attach", { host: name }, { timeoutMs: 35 * 60_000 });
    await reReadAfterMutation();
  },

  remove: async (name) => {
    await requireClient().request("evener/host/remove", { name });
    await reReadAfterMutation();
  },

  resetForTests: () => {
    refreshInflight = null;
    latestGeneration = 0;
    latestPublishedGeneration = 0;
    set({ load: { phase: "loading" } });
  },
}));

export function useHostsStore<T>(selector: (state: HostsStoreState) => T): T {
  return useStore(hostsStore, selector);
}
