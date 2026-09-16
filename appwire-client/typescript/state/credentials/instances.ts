// The credential instances listing core: the wire-truth gateway for a
// Providers & credentials surface's evener/instance/{list,create,edit,remove,
// setDefault,setModelDisabled,refreshModels} traffic, as a framework-free store
// (frameworkFreeStore.ts). createCredentialInstancesStore is a factory - each
// app builds the one instance it wires to its view layer and its connection
// holder, tests build their own - so two stores share no bookkeeping.
//
// Every evener/instance/* mutation's Go handler returns the FULL updated
// InstanceListResponse (appwire/types.go), so create/edit/remove/setDefault
// apply that response directly to the listing instead of issuing a separate
// evener/instance/list refetch.
//
// The store never holds a client of its own: connectionChanged(client, state)
// is how the app tells it which connection the rows on screen belong to, and
// the store reads through that client until the next call. Pure logic - no
// DOM, no React, no scheduler beyond setTimeout for the coalesced refetch.

import type { AppwireClient, ConnectionState } from "../../client";
import { CONNECTION_REPLACED_ERROR } from "../../credentialLabels";
import { errorText } from "../../errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type {
  InstanceCreateParams,
  InstanceEditParams,
  InstanceEntry,
  InstanceListResponse,
  InstanceModelEntry,
  InstanceSetModelDisabledParams,
  ProviderDescriptor,
} from "../../types.gen";

/** The client surface the listing core calls: request only. */
export type CredentialInstancesClient = Pick<AppwireClient, "request">;

// StaleListingRefusal is what requireWritableClient throws: a write - or a
// probe, anything that acts ON the rows - refused because the rows on screen
// were read by a connection that is gone and this one has not answered with
// its own listing yet. It is exported as its own type, with isStaleListingRefusal
// as the cheap test, because the refusal is not a failure to report: what it
// asks for is a re-read and a retry, and a caller that cannot tell it apart
// from any other store error would show the user a message no user action
// resolves. Its message is the same sentence those callers show
// (CONNECTION_REPLACED_ERROR), so the unclassified path degrades to honest
// words rather than naming this store's internals.
export class StaleListingRefusal extends Error {
  constructor() {
    super(CONNECTION_REPLACED_ERROR);
  }
}

export function isStaleListingRefusal(err: unknown): boolean {
  return err instanceof StaleListingRefusal;
}

// staleListingHeld names the condition the refusal and every surface that
// gates its controls on it share: the rows on screen are not this connection's
// (listingFromPreviousConnection) AND there are rows to act on. It exists so the
// two cannot drift - a surface that refused what the store would allow, or
// allowed what it would refuse, is a mismatch the user sees as controls that
// look refused and work, or look ready and do nothing.
export function staleListingHeld(
  state: Pick<CredentialInstancesState, "instances" | "availableProviders" | "listingFromPreviousConnection">,
): boolean {
  return state.listingFromPreviousConnection && (state.instances.length > 0 || state.availableProviders.length > 0);
}

export interface CredentialInstancesState {
  instances: InstanceEntry[];
  availableProviders: ProviderDescriptor[];
  // diagnostics/userLayer/writesRefused mirror InstanceListResponse's own
  // optional fields (appwire/types.go), normalized here to always-present
  // values (spec §11.3) so components never need an `?? []`/`?? false`
  // fallback of their own.
  diagnostics: string[];
  userLayer: string;
  writesRefused: boolean;
  loading: boolean;
  error: string | null;
  // True while `instances` holds a listing that was NOT read on the
  // connection the store would write to now: a REPLACED client leaves the
  // previous connection's rows on screen until its own read lands (readListing
  // discards a response read through a client that is no longer wired), and
  // those rows name instances and endpoints of a connection that is gone. Set
  // on the client identity changing, never on a state transition the same
  // client makes - a transport flap to reconnecting, or a failed read's error
  // state, leaves the rows as much this client's as they were. Consumers gate
  // the actions that would act on such a listing on this flag, not on
  // `loading`: every listing read sets `loading`, including a same-connection
  // refresh whose rows still belong to the user, and a control that goes
  // disabled under the keyboard's own focus drops focus to <body> (the
  // credential dialog's rows are that dialog's focus targets).
  listingFromPreviousConnection: boolean;
  // A marker that changes ONLY when a state transition came from the store's
  // own self-marked refresh (fetchSelf, or scheduleRefetch(true)).
  // Subscriptions that watch for unrelated changes compare it across a
  // transition to tell this client's own refresh apart from a foreign listing
  // change.
  selfRefresh: number;
  // fetch resolves true when the response it carried was applied to the
  // listing, false when a newer request superseded it or the read failed
  // (the failure lands in `error`) - a resolved promise alone is never
  // proof the listing moved.
  fetch(): Promise<boolean>;
  // fetchSelf is fetch() for a read the caller is performing as its own work -
  // a guided flow's refresh and its endpoint-refusal recovery. The transition
  // carries the selfRefresh marker, so a subscriber watching the listing for
  // unrelated changes does not read the caller's own read as one, which is
  // what keeps the flow from invalidating the operation that asked. Only a
  // caller's own operation qualifies: a read that could be reporting someone
  // else's change has to stay unmarked, because the marker is the only signal
  // that tells the two apart.
  fetchSelf(): Promise<boolean>;
  // create/edit/remove resolve true when the listing they answered with is
  // the one the store now holds, false when a newer request superseded it.
  // Callers that steer a flow on the strength of their own write (the sheet,
  // the add dialog, the removal report) act on the store's verdict, never on
  // the raw response.
  create(params: InstanceCreateParams): Promise<boolean>;
  edit(params: InstanceEditParams): Promise<boolean>;
  remove(name: string, expectedEndpointFingerprint?: string): Promise<boolean>;
  setDefault(name: string): Promise<boolean>;
  // setModelDisabled flips one model row's disabled flag and applies the
  // returned list, like setDefault: the sheet steers on the store's list.
  setModelDisabled(params: InstanceSetModelDisabledParams): Promise<void>;
  // refreshModels fetches one instance's live listing, then applies the
  // returned inventory (exact catalog rows plus cached live ids).
  refreshModels(name: string): Promise<void>;
}

// CredentialInstancesSeam is what an app adapter that still owns credential
// mutations of its own (the auth RPCs, until they move into this layer)
// composes with: the write gate, the landed-write count, and the coalesced
// refetch, each bound to one store instance.
export interface CredentialInstancesSeam {
  // Writes go through this: while the store still holds the previous
  // connection's listing, the rows on screen name instances and endpoints of a
  // connection that is gone, and a write issued from them would be submitted
  // to a connection that never read them - an editor's captured endpoint
  // fingerprint, an instance name, a default flag all describe the old
  // listing. Refused (StaleListingRefusal) until this connection's own read
  // lands; reads stay available, and a read is what clears the mark.
  //
  // What is refused is a write from a listing that is HELD: a connection that
  // has not read one yet (a fresh client, or a view that never asked for one)
  // has nothing stale on screen to act on, and its writes run as they always
  // have.
  requireWritableClient(): CredentialInstancesClient;
  // noteLandedMutation records one landed write against the instance it
  // touched, so only THAT instance's in-flight refreshModels is retired.
  noteLandedMutation(instance: string): void;
  // scheduleRefetch coalesces every request in a short window into one
  // listing read. `self` marks the read as this client's own; a foreign
  // request anywhere in the window wins, because the read observes that
  // change either way.
  scheduleRefetch(self?: boolean): void;
}

export interface CredentialInstancesStore<Extra extends object = Record<never, never>>
  extends FrameworkFreeStore<CredentialInstancesState & Extra>,
    CredentialInstancesSeam {
  // connectionChanged tells the store which connection the rows belong to
  // now. A different client marks the held listing as a replaced connection's
  // and drops the bookkeeping written under the old one; any transition
  // retires in-flight requests and a pending refetch; a client becoming ready
  // restores the listing if a view has ever read it.
  connectionChanged(client: CredentialInstancesClient | null, state: ConnectionState): void;
  // resetForTests returns the store to its initial state, including the
  // bookkeeping and connection above. No production code should call this.
  resetForTests(): void;
}

/** The listing half of the state: InstanceListResponse with every optional
 * field present. */
export type CredentialListing = Pick<
  CredentialInstancesState,
  "instances" | "availableProviders" | "diagnostics" | "userLayer" | "writesRefused"
>;

// listState normalizes one instance/list answer into the store's own always-
// present shape. Every reader of the listing goes through it, so a field
// added to InstanceListResponse is defaulted in exactly one place.
function listState(resp: InstanceListResponse): CredentialListing {
  return {
    instances: resp.instances,
    availableProviders: resp.availableProviders,
    diagnostics: resp.diagnostics ?? [],
    userLayer: resp.userLayer ?? "",
    writesRefused: resp.writesRefused ?? false,
  };
}

/** listingOf picks the listing out of a store snapshot, for a consumer that
 * hands the rows on as one value rather than selecting fields. */
export function listingOf(state: CredentialListing): CredentialListing {
  const { instances, availableProviders, diagnostics, userLayer, writesRefused } = state;
  return { instances, availableProviders, diagnostics, userLayer, writesRefused };
}

// MutationReconcile tells applyMutation how to place its answer among newer
// reads: supersededFor receives the answer a newer request outran, instance
// names the row the landed-write count belongs to, and written names the
// model a toggle wrote.
interface MutationReconcile {
  instance: string;
  supersededFor?: (response: InstanceListResponse) => void;
  written?: { instance: string; model: string };
}

// withToggledFlag sets one model's disabled flag in a model list: the one
// place a toggle's outcome is written, whether it lands on the store's newer
// inventory or reconciles a superseded answer.
function withToggledFlag(
  models: InstanceModelEntry[],
  model: string,
  disabled: boolean | undefined,
): InstanceModelEntry[] {
  return models.map((entry) => (entry.id === model ? { ...entry, disabled } : entry));
}

// changedCounts names the keys whose count moved between a snapshot taken when
// a request started and the counts now: the per-instance comparison both the
// read and the write path make to tell work that landed DURING a request's
// flight apart from work that landed before it.
function changedCounts(before: Map<string, number>, now: Map<string, number>): Set<string> {
  return new Set([...now].filter(([name, count]) => (before.get(name) ?? 0) !== count).map(([name]) => name));
}

const REFETCH_DEBOUNCE_MS = 250;

export function createCredentialInstancesStore<Extra extends object = Record<never, never>>(
  options: {
    // extend adds an app's own store-bound methods to the state, alongside the
    // listing actions, so the view layer selects them from one snapshot. The
    // seam it receives is bound to the store being built.
    extend?: (seam: CredentialInstancesSeam) => Extra;
  } = {},
): CredentialInstancesStore<Extra> {
  type State = CredentialInstancesState & Extra;
  // Every listing transition patches the listing half of the state; the cast is
  // the one place the app-extended state type meets the listing core.
  function patch(partial: Partial<CredentialInstancesState>): void {
    store.setState(partial as Partial<State>);
  }

  let connection: { client: CredentialInstancesClient | null; state: ConnectionState } = {
    client: null,
    state: "idle",
  };
  let requestVersion = 0;
  let requestedList = false;
  // landedMutations counts authoritative writes that LANDED, PER INSTANCE (a
  // write the server applied, whether or not its answer is the one the store
  // applied). refreshModels captures its instance's count at start, and only a
  // write on THAT instance landing while the refresh is out - never a routine
  // read, never a write on another instance - supersedes it: counting globally
  // dropped a refresh for one instance whenever anything was written to another,
  // and the sheet silently lost the live rows it asked for.
  const landedMutations = new Map<string, number>();
  // refreshVersions tracks one in-flight version per refreshed instance: two
  // sheets may refresh different instances concurrently, and starting B's
  // refresh must not cancel A's.
  const refreshVersions = new Map<string, number>();
  // listEstablished turns true on the first applied full-list answer: it
  // distinguishes "the store never held this name" from "a remove dropped it".
  let listEstablished = false;
  // refreshedInstances counts, per instance, the refreshes that landed since the
  // last full-list apply: a read that started before one merges around those rows
  // instead of replacing them, and a COUNT rather than a name is what tells a
  // refresh that landed during the read apart from one that landed before it -
  // only the former is newer than the read's own answer. Reads and writes
  // snapshot these counts when they start and compare them when their answer
  // lands.
  const refreshedInstances = new Map<string, number>();
  let selfRefreshCounter = 0;
  let refetchTimer: ReturnType<typeof setTimeout> | undefined;
  // Schedule provenance for the coalesced refresh. The debounce collapses every
  // request in its window into one read, so provenance is not last-writer-wins
  // and not symmetric: the read is marked as this client's own only when every
  // request in its window was self. A foreign request wins whichever side of the
  // window it lands on, because the read observes that change either way, and
  // presenting a listing that carries a foreign change as the store's own
  // refresh would keep the guided flow from invalidating on it. `undefined`
  // means no request is pending.
  let pendingRefetchSelf: boolean | undefined;

  function requireClient(): CredentialInstancesClient {
    if (!connection.client) {
      throw new Error("credentials store: no client connected; call connectionChanged(client, state) first");
    }
    return connection.client;
  }

  function requireWritableClient(): CredentialInstancesClient {
    const client = requireClient();
    if (staleListingHeld(store.getState())) {
      throw new StaleListingRefusal();
    }
    return client;
  }

  function bump(counts: Map<string, number>, name: string): void {
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }

  function noteLandedMutation(instance: string): void {
    bump(landedMutations, instance);
  }

  // keepRefreshedModels keeps, for the instances a refresh landed for during a
  // write's flight, the store's own (newer) model inventory instead of the
  // answer's older one. Every other field comes from the answer, which is
  // authoritative for the write itself; the written model's flag, when the
  // caller names one, is taken from the answer so the row it wrote still shows
  // what the server recorded.
  function keepRefreshedModels(
    instances: InstanceEntry[],
    names: Set<string>,
    written?: MutationReconcile["written"],
  ): InstanceEntry[] {
    const current = store.getState().instances;
    return instances.map((entry) => {
      if (!names.has(entry.name)) return entry;
      const held = current.find((row) => row.name === entry.name);
      if (!held?.models) return entry;
      const answered =
        written?.instance === entry.name ? entry.models?.find((model) => model.id === written.model) : undefined;
      if (!answered) return { ...entry, models: held.models };
      return { ...entry, models: withToggledFlag(held.models, answered.id, answered.disabled) };
    });
  }

  // keepWrittenRows keeps the store's own row for every instance a write landed
  // for while a read was in flight: that read's answer was computed before the
  // write, so its copy of the row is the older one, and applying it would flip
  // back what the write just set.
  function keepWrittenRows(instances: InstanceEntry[], names: Set<string>): InstanceEntry[] {
    if (names.size === 0) return instances;
    const current = store.getState().instances;
    return instances.map((entry) => {
      const held = names.has(entry.name) ? current.find((row) => row.name === entry.name) : undefined;
      return held ?? entry;
    });
  }

  // mergeNewerRows keeps, in a listing read's answer, the rows newer work has
  // already made authoritative: the model inventory a refresh landed for while
  // this read was out (only its models are newer than the answer), and the
  // whole row of an instance a write landed for (the stored copy is what that
  // write's own answer installed, and this answer predates it). A row the
  // answer omits is a row the server no longer has, so nothing is staged back
  // in.
  function mergeNewerRows(
    instances: InstanceEntry[],
    writes: Map<string, number>,
    refreshes: Map<string, number>,
  ): InstanceEntry[] {
    const written = changedCounts(writes, landedMutations);
    // Only the instances a refresh landed for DURING this read: one that landed
    // before the read began describes a row this answer already postdates, so
    // preserving the store's older copy of it would overwrite the newer one.
    const refreshed = changedCounts(refreshes, refreshedInstances);
    refreshedInstances.clear();
    if (refreshed.size === 0 && written.size === 0) return instances;
    return keepWrittenRows(keepRefreshedModels(instances, refreshed), written);
  }

  // reconcileToggledModel merges one superseded toggle's own outcome into the
  // listing the store holds: only the model that call wrote is taken from its
  // answer, because everything else there is older than the store's copy.
  // Nothing happens when the answer, the instance, or the model row is gone: a
  // stale answer never resurrects a row the store dropped.
  function reconcileToggledModel(response: InstanceListResponse, params: InstanceSetModelDisabledParams): void {
    const answered = response.instances.find((entry) => entry.name === params.name);
    const toggled = answered?.models?.find((model) => model.id === params.model);
    if (!toggled) return;
    const current = store.getState().instances;
    const held = current.find((entry) => entry.name === params.name)?.models;
    if (!held?.some((model) => model.id === params.model)) return;
    const models = withToggledFlag(held, toggled.id, toggled.disabled);
    patch({
      instances: current.map((entry) => (entry.name === params.name ? { ...entry, models } : entry)),
    });
  }

  // Reads and writes share ordering: only the most recently started request
  // can replace the listing, even when responses arrive out of order. Reports
  // whether THIS response is the one that replaced it: a superseded response
  // carries a listing the store discarded, and a caller steering a view on the
  // strength of its own write has to be able to tell the two apart. A write
  // the server applied still knows the authoritative outcome of the ONE row it
  // wrote, which is what `reconcile` (MutationReconcile) places among newer
  // reads.
  async function applyMutation(
    request: () => Promise<InstanceListResponse>,
    reconcile: MutationReconcile,
  ): Promise<boolean> {
    const version = ++requestVersion;
    const client = connection.client;
    // Refreshes already landed when this write started: the answer is newer
    // than those rows and replaces them, as it always has.
    const before = new Map(refreshedInstances);
    try {
      const response = await request();
      const sameClient = connection.client === client;
      if (sameClient) noteLandedMutation(reconcile.instance);
      if (version !== requestVersion) {
        // A superseded answer from a client that is gone describes a listing
        // this client never had: reconciling it would write the dead client's
        // row into the current one.
        if (sameClient) reconcile.supersededFor?.(response);
        return false;
      }
      const refreshedDuringFlight = changedCounts(before, refreshedInstances);
      refreshedInstances.clear();
      listEstablished = true;
      const applied = listState(response);
      if (refreshedDuringFlight.size > 0) {
        applied.instances = keepRefreshedModels(applied.instances, refreshedDuringFlight, reconcile.written);
      }
      // The mutation ran on the connection the store is wired to now (a request
      // left over from a replaced one is discarded by the version guard above),
      // so the listing it answered with is this connection's - the same claim a
      // read through the current client makes, and it clears the same mark.
      patch({ ...applied, loading: false, error: null, listingFromPreviousConnection: false });
      return true;
    } finally {
      if (version === requestVersion) patch({ loading: false });
    }
  }

  // Every listing read - a caller's fetch() and the store's own coalesced
  // refetch - shares this one read and its ordering guard. `self` marks THIS
  // client's own refresh: each state transition it touches carries a fresh
  // selfRefresh marker (and no other read ever changes it), so a subscription
  // comparing the marker across a transition can tell the client's own refresh
  // apart from a foreign listing change. Resolves true only when the response
  // was applied - a superseded or failed read resolves false without throwing,
  // so a resolved promise alone is never confirmation the listing moved.
  //
  // A read is also where an older snapshot can arrive after a write: the hub
  // serves List() without holding the write lock, so an answer computed before
  // a write landed can carry the pre-write row. mergeNewerRows keeps the newer
  // copy for the instances a landed write named.
  async function readListing(self: boolean): Promise<boolean> {
    const client = requireClient();
    requestedList = true;
    const version = ++requestVersion;
    const writes = new Map(landedMutations);
    const refreshes = new Map(refreshedInstances);
    const mark = () => (self ? { selfRefresh: ++selfRefreshCounter } : {});
    patch({ loading: true, error: null, ...mark() });
    try {
      const resp = await client.request("evener/instance/list", {});
      if (version !== requestVersion || connection.client !== client) return false;
      listEstablished = true;
      const instances = mergeNewerRows(resp.instances, writes, refreshes);
      patch({
        ...listState({ ...resp, instances }),
        loading: false,
        listingFromPreviousConnection: false,
        ...mark(),
      });
      return true;
    } catch (err) {
      if (version !== requestVersion || connection.client !== client) return false;
      patch({ loading: false, error: errorText(err), ...mark() });
      return false;
    }
  }

  function scheduleRefetch(self = false): void {
    pendingRefetchSelf = pendingRefetchSelf === undefined ? self : pendingRefetchSelf && self;
    clearTimeout(refetchTimer);
    refetchTimer = setTimeout(() => {
      const selfRequest = pendingRefetchSelf ?? false;
      pendingRefetchSelf = undefined;
      refetchTimer = undefined;
      // readListing's requireClient() throws outside its try/catch, by design -
      // a real rejection here would be an unobserved background call with
      // nothing awaiting it, so a rare disconnect-during-the-debounce-window
      // race is swallowed here rather than surfacing as an unhandled rejection.
      readListing(selfRequest).catch(() => {});
    }, REFETCH_DEBOUNCE_MS);
  }

  function cancelRefetch(): void {
    clearTimeout(refetchTimer);
    refetchTimer = undefined;
    pendingRefetchSelf = undefined;
  }

  // clearClientBookkeeping drops what was written under one client: a row a
  // previous client's refresh marked, or a count its late answer bumped, must
  // not decide the next client's listing, and a refresh landing before that
  // client's first read is a fresh row to stage, not a removal to preserve.
  function clearClientBookkeeping(): void {
    refreshedInstances.clear();
    refreshVersions.clear();
    landedMutations.clear();
    listEstablished = false;
  }

  async function refreshModels(name: string): Promise<void> {
    // A read keeps the read gate: reads stay available while the rows on screen
    // are a replaced connection's - a read is what clears the mark - and this one
    // never clears it. Gating it would refuse a view the very read that repairs
    // it. Its ANSWER is another matter: it is dropped outright when the client it
    // was issued on is gone, and while the rows on screen are still a replaced
    // connection's it is not applied at all (see the apply below) - what makes a
    // row a refresh can speak about is this connection's own listing read.
    const client = requireClient();
    // Per-instance version, not the global counter: a refresh for B must not
    // cancel an in-flight refresh for A. Only a newer refresh for THIS
    // instance, or a write on THIS instance landing while this read is out,
    // supersedes it - routine reads and writes on other instances never do.
    const basis = landedMutations.get(name) ?? 0;
    const version = (refreshVersions.get(name) ?? 0) + 1;
    refreshVersions.set(name, version);
    try {
      const response = await client.request("evener/instance/refreshModels", { name });
      if (
        refreshVersions.get(name) !== version ||
        (landedMutations.get(name) ?? 0) !== basis ||
        connection.client !== client
      )
        return;
      const row = response.instances.find((entry) => entry.name === name);
      if (!row) return;
      const state = store.getState();
      const known = state.instances.some((existing) => existing.name === name);
      // A removal that landed while this refresh was out: merging nothing
      // preserves it instead of resurrecting a phantom stub row.
      if (listEstablished && !known) return;
      if (state.listingFromPreviousConnection) {
        // The rows on screen are a replaced connection's, and an answer naming
        // one of them describes the hub that is there now: merging its
        // inventory into that row presents the old row as current, and
        // clearing the failed full read's error would make the pane look
        // recovered. The listing read that lands next is what makes such a row
        // this refresh's to speak about.
        if (known) return;
        // A row this connection never held is this read's own data, so it is
        // staged - the reconnect is not blank while its listing is out - with
        // the error left exactly where it is.
        bump(refreshedInstances, name);
        patch({ instances: [...state.instances, { ...row }] });
        return;
      }
      // Only the MODEL INVENTORY comes from this answer: every other field
      // of the row may have been updated by a newer read or write that
      // landed while this refresh was in flight, and a refresh only ever
      // knows about live models.
      const merged = state.instances.map((entry) => (entry.name === name ? { ...entry, models: row.models } : entry));
      if (!known) merged.push({ ...row });
      bump(refreshedInstances, name);
      patch({ instances: merged, error: null });
    } finally {
      // Only this client's entry: a reconnect clears the map, and the next
      // client's refresh for the same instance restarts at version 1 - the
      // old request's cleanup must not delete that newer token and discard
      // its answer.
      if (connection.client === client && refreshVersions.get(name) === version) {
        refreshVersions.delete(name);
      }
    }
  }

  const seam: CredentialInstancesSeam = { requireWritableClient, noteLandedMutation, scheduleRefetch };

  const store = createFrameworkFreeStore<State>(() => ({
    ...listState({ instances: [], availableProviders: [] }),
    loading: false,
    error: null,
    selfRefresh: 0,
    listingFromPreviousConnection: false,

    async fetch() {
      return readListing(false);
    },

    async fetchSelf() {
      return readListing(true);
    },

    async create(params) {
      const client = requireWritableClient();
      return applyMutation(() => client.request("evener/instance/create", params), { instance: params.name });
    },

    async edit(params) {
      const client = requireWritableClient();
      return applyMutation(() => client.request("evener/instance/edit", params), { instance: params.name });
    },

    async remove(name, expectedEndpointFingerprint) {
      const client = requireWritableClient();
      return applyMutation(
        () =>
          client.request("evener/instance/remove", {
            name,
            ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
          }),
        { instance: name },
      );
    },

    async setDefault(name) {
      const client = requireWritableClient();
      const applied = await applyMutation(() => client.request("evener/instance/setDefault", { name }), {
        instance: name,
      });
      // A superseded response lost the store's ordering race: the read that won it
      // may have started before the hub applied the new default, so the listing
      // would keep the old flag until something else refreshed. The store's own
      // read lands the post-mutation view - foreign-marked, because a default
      // change is not the guided flow's own edit.
      if (!applied) scheduleRefetch();
      return applied;
    },

    async setModelDisabled(params) {
      const client = requireWritableClient();
      // A toggle whose answer a newer request outran is not dropped: it holds
      // the authoritative outcome for the one model it wrote, so that row is
      // reconciled into whatever listing the store now has.
      await applyMutation(() => client.request("evener/instance/setModelDisabled", params), {
        instance: params.name,
        supersededFor: (response) => reconcileToggledModel(response, params),
        written: { instance: params.name, model: params.model },
      });
    },

    refreshModels,

    ...(options.extend?.(seam) ?? ({} as Extra)),
  }));

  function connectionChanged(client: CredentialInstancesClient | null, state: ConnectionState): void {
    const previous = connection;
    connection = { client, state };
    const clientChanged = client !== previous.client;
    if (clientChanged || state !== previous.state) {
      requestVersion += 1;
      patch({ loading: false });
      cancelRefetch();
      // Whatever listing state holds was read through the connection that just
      // went away (or through the client being replaced); the rows stay on
      // screen until this connection's own read lands, but nothing may act on
      // them in the meantime. Keyed on the CLIENT alone: a transition on the same
      // client - a transport flap to reconnecting, a failed read's error state -
      // leaves the rows as much this client's as they were, and the actions on
      // them stay the user's to retry once it is ready again. Marking those stale
      // refused them with a message claiming a replacement that never happened.
      if (clientChanged) {
        patch({ listingFromPreviousConnection: true });
        clearClientBookkeeping();
      }
    }
    // Once a view has requested the listing, reconnects must restore it even
    // if its one-shot mount loader was interrupted.
    if (requestedList && client && state === "ready" && (clientChanged || previous.state !== "ready")) {
      readListing(false).catch(() => {});
    }
  }

  function resetForTests(): void {
    requestVersion += 1;
    requestedList = false;
    clearClientBookkeeping();
    cancelRefetch();
    connection = { client: null, state: "idle" };
    store.setState(store.getInitialState());
  }

  return { ...store, ...seam, connectionChanged, resetForTests };
}
