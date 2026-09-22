// The credential instances store core: the wire-truth gateway for a Providers
// & credentials surface's evener/instance/* listing traffic and the evener/auth/*
// RPCs its API-key, credential-file, sign-out, sign-in and probe flows drive,
// as a framework-free store (frameworkFreeStore.ts). createCredentialInstancesStore
// is a factory - each app builds the one instance it wires to its view layer and
// its connection holder, tests build their own - so two stores share no
// bookkeeping.
//
// Every evener/instance/* mutation's Go handler returns the FULL updated
// InstanceListResponse (appwire/types.go), so create/edit/remove/setDefault
// apply that response directly to the listing instead of issuing a separate
// evener/instance/list refetch. Auth mutations return the raw wire response
// and the store schedules its own listing refresh, correlated with the hub's
// evener/auth/updated echo (see the own-echo section below).
//
// Never-echo invariant: no method here stores a secret VALUE anywhere in the
// state - setApiKey/setCredentialJson pass the value to the wire and return
// only AuthStatusResponse shapes, none of which carry the secret itself
// (write-only fields on the wire). instances.test.ts drives sentinel secrets
// through every path and asserts they appear in no state, error or log.
//
// The store never holds a client of its own: connectionChanged(client, state)
// is how the app tells it which connection the rows on screen belong to, and
// the store reads and listens through that client until the next call. Pure
// logic - no DOM, no React, no scheduler beyond setTimeout for the coalesced
// refetch.

import type { AppwireClient, ConnectionState } from "../../client";
import { CONNECTION_REPLACED_ERROR } from "../../credentialLabels";
import { errorText } from "../../errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type {
  AnyNotification,
  AuthDevicePollResponse,
  AuthDeviceStartResponse,
  AuthLoginCompleteResponse,
  AuthLoginStartResponse,
  AuthLogoutResponse,
  AuthStatusResponse,
  AuthTestResponse,
  InstanceCreateParams,
  InstanceEditParams,
  InstanceEntry,
  InstanceListResponse,
  InstanceModelEntry,
  InstanceSetModelDisabledParams,
  ProviderDescriptor,
} from "../../types.gen";

/** The client surface the store calls: requests, and the notification feed the
 * evener/auth/updated refetch listens on. */
export type CredentialInstancesClient = Pick<AppwireClient, "request" | "onNotification">;

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
  // True once a full listing has landed - an empty one counts - and from then
  // on, across any client change, until the store is reset: it says a listing
  // has been applied, while listingFromPreviousConnection says whose rows they
  // are. It distinguishes "nothing read yet" from "read, and there are no
  // rows", and, together with listingFromPreviousConnection, tells a
  // refreshModels answer whether a name it does not find was removed.
  listingEstablished: boolean;
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
  edit(params: InstanceEditParams, reconcile?: EditSupersededReconcile): Promise<boolean>;
  remove(name: string, expectedEndpointFingerprint?: string): Promise<boolean>;
  setDefault(name: string): Promise<boolean>;
  // setModelDisabled flips one model row's disabled flag and applies the
  // returned list, like setDefault: the sheet steers on the store's list.
  setModelDisabled(params: InstanceSetModelDisabledParams): Promise<void>;
  // refreshModels fetches one instance's live listing, then applies the
  // returned inventory (exact catalog rows plus cached live ids).
  refreshModels(name: string): Promise<void>;
  // Auth mutations return the raw wire response and never touch the listing
  // synchronously - on success the store schedules its own listing refresh
  // (see authMutation below), which survives the issuing dialog unmounting
  // before the RPC resolves; callers may still fetch for their own steering,
  // and failures surface as inline errors/toasts in the caller rather than
  // this store swallowing them into `error`. Issuing one retires any listing
  // read still in flight: its answer predates the write.
  // expectedEndpointFingerprint is the endpoint the caller showed the user
  // (InstanceEntry.endpointFingerprint). The hub refuses the write when the
  // name resolves elsewhere by then, which is the one gap the caller cannot
  // close itself: its own comparison reads a listing a concurrent change can
  // outdate.
  setApiKey(provider: string, value: string, expectedEndpointFingerprint?: string): Promise<AuthStatusResponse>;
  setCredentialJson(provider: string, value: string, expectedEndpointFingerprint?: string): Promise<AuthStatusResponse>;
  // clearStoredKey removes only the credentials.toml entry, leaving any
  // OAuth/ADC/env credential untouched - the counterpart to setApiKey, and
  // the narrow alternative to logout() for a stray stored key shadowed
  // behind an active oauth/adc sign-in.
  clearStoredKey(provider: string, expectedEndpointFingerprint?: string): Promise<AuthStatusResponse>;
  logout(provider: string, expectedEndpointFingerprint?: string): Promise<AuthLogoutResponse>;
  loginStart(provider: string): Promise<AuthLoginStartResponse>;
  loginComplete(provider: string, flowId: string, redirectUrl: string): Promise<AuthLoginCompleteResponse>;
  deviceStart(provider: string): Promise<AuthDeviceStartResponse>;
  devicePoll(provider: string, flowId: string): Promise<AuthDevicePollResponse>;
  // authStatus reads one provider's credential status: a plain read, arming no
  // echo marker and refreshing nothing; a reply from a connection since
  // replaced is refused (CONNECTION_REPLACED_ERROR) rather than returned.
  authStatus(provider: string): Promise<AuthStatusResponse>;
  // testCredentials is a probe, not a listing read: it dials the endpoint the
  // row names and asserts the fingerprint it carries, so it takes the same gate
  // as a write (the stale-listing refusal) - a probe issued from a listing that
  // belongs to a connection that is gone would reach a destination this
  // connection never read. Callers treat the refusal as the changed connection
  // it is (isStaleListingRefusal), never as a failed test.
  testCredentials(provider: string, expectedEndpointFingerprint?: string): Promise<AuthTestResponse>;
}

export interface CredentialInstancesStore extends FrameworkFreeStore<CredentialInstancesState> {
  // connectionChanged tells the store which connection the rows belong to
  // now; a call that names the connection it already holds changes nothing.
  // A different client marks the held listing as a replaced connection's,
  // drops the bookkeeping written under the old one and moves the
  // evener/auth/updated listener; any transition retires in-flight requests,
  // a pending refetch and the own-echo markers; a client becoming ready
  // restores the listing if a view has ever read it.
  connectionChanged(client: CredentialInstancesClient | null, state: ConnectionState): void;
  // resetForTests returns the store to its initial state, including the
  // bookkeeping and connection above. No production code should call this.
  resetForTests(): void;
}

export interface CredentialInstancesDeps {
  // The identity this client stamps on its auth mutations as originClientId.
  // The hub echoes it into evener/auth/updated, so every client - this one and
  // the others - attributes the change to its origin by identity rather than
  // by provider plus timing.
  ownClientId: () => string;
}

/** The listing half of the state: InstanceListResponse with every optional
 * field present. */
const LISTING_FIELDS = ["instances", "availableProviders", "diagnostics", "userLayer", "writesRefused"] as const;
export type CredentialListing = Pick<CredentialInstancesState, (typeof LISTING_FIELDS)[number]>;

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
  // Spelled out rather than looped: CredentialListing derives from
  // LISTING_FIELDS, so a field added there fails to compile here.
  const { instances, availableProviders, diagnostics, userLayer, writesRefused } = state;
  return { instances, availableProviders, diagnostics, userLayer, writesRefused };
}

/** listingChanged reports whether a store transition replaced the listing:
 * every applied read or write installs fresh rows, while a loading or error
 * patch leaves the same arrays in place. */
export function listingChanged(state: CredentialListing, previous: CredentialListing): boolean {
  return LISTING_FIELDS.some((field) => state[field] !== previous[field]);
}

type ListingTransition = CredentialListing & Pick<CredentialInstancesState, "selfRefresh" | "loading" | "error">;

/** foreignListingChange reports whether a store transition is a change a
 * credential probe or a guided flow must not be trusted against: the rows
 * moved, or a read began or failed, and the transition is NOT the store's own
 * self-marked refresh (selfRefresh moves only on those). A host that shows a
 * probe result, or steers a flow on a listing, invalidates on exactly this. */
export function foreignListingChange(
  state: ListingTransition,
  previous: ListingTransition,
  moved: boolean = listingChanged(state, previous),
): boolean {
  return (
    state.selfRefresh === previous.selfRefresh &&
    (moved || state.loading !== previous.loading || state.error !== previous.error)
  );
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

/** An edit whose answer a newer request superseded is discarded from the
 * listing, but that answer still holds the hub's authoritative post-write row
 * for the one instance the edit wrote. onSuperseded receives it so a caller
 * steering on its own write can compare the listing now held against the row
 * the edit actually produced - the authoritative endpoint identity - instead of
 * inferring the destination from the listing's sanitized display fields. */
export interface EditSupersededReconcile {
  onSuperseded?: (response: InstanceListResponse) => void;
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
// Age budget from an own-echo marker's latest stamp - the issue, or the RPC
// response that re-stamped it (authMutation's and applyMutation's landed
// branches).
const SELF_ECHO_WINDOW_MS = 2000;
// LocalMutationMarker is ONE outstanding mutation the store issued, not yet
// consumed (by its echo) or retired (by an unconfirmed outcome). One marker per
// mutation rather than one per provider is what lets a provider-instance
// mutation correlate at all: its evener/auth/updated echo carries no provider,
// so it is matched by the marker's absent provider plus the echoed
// originClientId. It also keeps two same-subject mutations from sharing a
// single stamp.
interface LocalMutationMarker {
  // The provider an auth mutation named, or undefined for a provider-instance
  // mutation, whose echo names no provider.
  provider: string | undefined;
  // The marker's latest life event: the most recent issue, or the RPC response
  // that re-stamped it; ages the marker out.
  issuedAt: number;
  // True once the mutation's RPC response has landed. An unsettled marker
  // belongs to a mutation still in flight, so it is never pruned however old it
  // looks: a slow response must still be able to restamp it, and that
  // mutation's late echo must still find it.
  settled: boolean;
}

// staleLocalMutation reports whether a marker has outlived its meaning. Only a
// SETTLED marker can go stale - the echo window ran from its response, so a
// later echo is not this marker's. A marker still in flight is kept however old
// its stamp looks.
function staleLocalMutation(marker: LocalMutationMarker, now: number): boolean {
  return marker.settled && now - marker.issuedAt > SELF_ECHO_WINDOW_MS;
}

export function createCredentialInstancesStore(deps: CredentialInstancesDeps): CredentialInstancesStore {
  // Replaced whole on every transition, so its identity is the generation a
  // mutation was issued under: a callback that lands after the connection it
  // was issued on is replaced or reconnects compares against it (see
  // authMutation) and leaves the connection now in place alone.
  let connection: { client: CredentialInstancesClient | null; state: ConnectionState } = {
    client: null,
    state: "idle",
  };
  let requestVersion = 0;
  let requestedList = false;
  // landedMutations counts listing writes that LANDED, PER INSTANCE, whose own
  // answer installed that instance's held row (create/edit/remove/setDefault/
  // setModelDisabled). refreshModels captures its instance's count at start, and
  // only such a write on THAT instance landing while the refresh is out - never
  // a routine read, never a write on another instance - supersedes it: counting
  // globally dropped a refresh for one instance whenever anything was written to
  // another, and the sheet silently lost the live rows it asked for. The count
  // is also what lets a read keep the store's newer row (keepWrittenRows), an
  // invariant only a write that installed its own row satisfies - a credential
  // write installs none, so it is deliberately absent here and retires an
  // in-flight refresh directly instead (retireInFlightRefresh, authMutation).
  const landedMutations = new Map<string, number>();
  // refreshVersions tracks one version per refreshed instance: two sheets may
  // refresh different instances concurrently, and starting B's refresh must not
  // cancel A's. The token is monotonic within a bookkeeping generation and is
  // never deleted - deleting it when the newest of several overlapping refreshes
  // settled let a later refresh reuse a version an older, still-out refresh was
  // holding, so that stale answer could land. A retired token is likewise left
  // in place. That is why refreshVersions' own presence cannot answer "is a
  // refresh in flight".
  const refreshVersions = new Map<string, number>();
  // inFlightRefreshes counts, per instance, refreshModels calls that have not
  // settled yet: the one thing that answers "is a refresh out for this
  // instance" (retireInFlightRefresh). Distinct from refreshVersions because the
  // token outlives its refresh.
  const inFlightRefreshes = new Map<string, number>();
  // bookkeepingGeneration increments whenever clearClientBookkeeping drops the
  // bookkeeping written under one client. A refresh captures it at start and is
  // stale whole once it moves: clearing lets a client swap back to an earlier
  // object, so neither connection identity nor a version token alone can tell a
  // late reply from a current one.
  let bookkeepingGeneration = 0;
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

  // connectionStillCurrent reports whether a reply asked for on `client` may
  // still be used: one that arrives after the connection it was asked on is
  // gone describes a hub that is no longer wired.
  function connectionStillCurrent(client: CredentialInstancesClient): boolean {
    return connection.client === client;
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

  // retireInFlightRefresh invalidates one instance's in-flight refreshModels by
  // advancing its version token, so an answer computed before the caller's write
  // cannot land over the post-write listing. This is the credential-write
  // counterpart to a landed mutation: a credential write installs no row, so it
  // must NOT be counted in landedMutations (that count also makes a read keep
  // the pre-write held row - keepWrittenRows - an invariant only a write with
  // its own row satisfies), yet it still supersedes a refresh by name. Reports
  // whether a refresh was actually out and retired - inFlightRefreshes, never
  // the token's presence, since the token outlives its refresh - so the
  // ambiguous-write path can tell that it discarded a live read. Bumps nothing
  // when no refresh is out.
  function retireInFlightRefresh(name: string): boolean {
    if ((inFlightRefreshes.get(name) ?? 0) === 0) return false;
    refreshVersions.set(name, (refreshVersions.get(name) ?? 0) + 1);
    return true;
  }

  // landedListing is the one transition that installs a listing: every applied
  // read or write goes through it. The rows are this connection's (the same
  // claim whichever request made them), a listing has now been applied, and
  // the read state is clean.
  function landedListing(listing: CredentialListing): Partial<CredentialInstancesState> {
    return { ...listing, loading: false, error: null, listingFromPreviousConnection: false, listingEstablished: true };
  }

  // settleLoading drops the loading flag a retired read left behind, once the
  // request that retired it is itself the latest and has settled. Guarded on
  // the flag because the store notifies on a no-op set.
  function settleLoading(version: number): void {
    if (version === requestVersion && store.getState().loading) store.setState({ loading: false });
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
    store.setState({
      instances: current.map((entry) => (entry.name === params.name ? { ...entry, models } : entry)),
    });
  }

  // A refused write reads nothing of its own, for instance writes and credential
  // writes alike: a failed reply may still follow a write the hub applied, and
  // the hub broadcasts evener/auth/updated for every write it applies
  // (notifyInstanceUpdated, cmd/evener-hub/app_rpc.go) - the refused write's
  // marker is retired, so its echo reads as foreign here - so the echo is what
  // re-reads, and a refused write that applied nothing changed nothing to read.
  //
  // Reads and writes share ordering: only the most recently started request
  // can replace the listing, even when responses arrive out of order. Reports
  // whether THIS response is the one that replaced it: a superseded response
  // carries a listing the store discarded, and a caller steering a view on the
  // strength of its own write has to be able to tell the two apart. A write
  // the server applied still knows the authoritative outcome of the ONE row it
  // wrote, which is what `reconcile` (MutationReconcile) places among newer
  // reads.
  async function applyMutation(
    request: (origin: { originClientId: string }) => Promise<InstanceListResponse>,
    reconcile: MutationReconcile,
  ): Promise<boolean> {
    const version = ++requestVersion;
    const client = connection.client;
    // Refreshes already landed when this write started: the answer is newer
    // than those rows and replaces them, as it always has.
    const before = new Map(refreshedInstances);
    // An instance mutation's echo names no provider, so its marker is the one
    // with an undefined subject (see the own-echo section below).
    const marker = noteLocalMutation(undefined);
    try {
      const response = await request({ originClientId: deps.ownClientId() });
      const sameClient = connection.client === client;
      if (sameClient) bump(landedMutations, reconcile.instance);
      // The write reached the hub - whether or not a newer request outran its
      // answer, since every return below is still this request's own echo - so
      // re-stamp so a late echo inside the window still correlates. A reconnect
      // already cleared the marker, making this a no-op.
      restampLocalMutation(marker);
      if (version !== requestVersion) {
        // A superseded answer from a client that is gone describes a listing
        // this client never had: reconciling it would write the dead client's
        // row into the current one.
        if (sameClient) reconcile.supersededFor?.(response);
        return false;
      }
      const refreshedDuringFlight = changedCounts(before, refreshedInstances);
      refreshedInstances.clear();
      const applied = listState(response);
      if (refreshedDuringFlight.size > 0) {
        applied.instances = keepRefreshedModels(applied.instances, refreshedDuringFlight, reconcile.written);
      }
      // The mutation ran on the connection the store is wired to now (a request
      // left over from a replaced one is discarded by the version guard above),
      // so the listing it answered with is this connection's.
      store.setState(landedListing(applied));
      return true;
    } catch (err) {
      // Refused: this mutation's marker retires, so an echo that follows after
      // all - the write landed and only its reply was lost - reads as foreign
      // and re-reads. A reconnect already cleared the marker, making this a
      // no-op.
      retireLocalMutation(marker);
      throw err;
    } finally {
      settleLoading(version);
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
    store.setState({ loading: true, error: null, ...mark() });
    try {
      const resp = await client.request("evener/instance/list", {});
      if (version !== requestVersion || !connectionStillCurrent(client)) return false;
      const instances = mergeNewerRows(resp.instances, writes, refreshes);
      store.setState({ ...landedListing(listState({ ...resp, instances })), ...mark() });
      return true;
    } catch (err) {
      if (version !== requestVersion || !connectionStillCurrent(client)) return false;
      store.setState({ loading: false, error: errorText(err), ...mark() });
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
    bookkeepingGeneration += 1;
    refreshedInstances.clear();
    refreshVersions.clear();
    inFlightRefreshes.clear();
    landedMutations.clear();
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
    // instance, a row-installing write on THIS instance landing while this read
    // is out (landedMutations), or a credential write for it (which advances
    // refreshVersions directly - retireInFlightRefresh) supersedes it - routine
    // reads and writes on other instances never do.
    const basis = landedMutations.get(name) ?? 0;
    const generation = bookkeepingGeneration;
    const version = (refreshVersions.get(name) ?? 0) + 1;
    refreshVersions.set(name, version);
    const marker = noteLocalMutation(undefined);
    bump(inFlightRefreshes, name);
    try {
      const response = await client.request("evener/instance/refreshModels", {
        name,
        originClientId: deps.ownClientId(),
      });
      // The refresh reached the hub - whether or not a newer request outran its
      // answer, since every return below is still this request's own echo - so
      // re-stamp so a late echo inside the window still correlates. A reconnect
      // already cleared the marker, making this a no-op.
      restampLocalMutation(marker);
      if (
        generation !== bookkeepingGeneration ||
        refreshVersions.get(name) !== version ||
        (landedMutations.get(name) ?? 0) !== basis ||
        !connectionStillCurrent(client)
      )
        return;
      const row = response.instances.find((entry) => entry.name === name);
      if (!row) return;
      const state = store.getState();
      const known = state.instances.some((existing) => existing.name === name);
      // A removal that landed while this refresh was out: merging nothing
      // preserves it instead of resurrecting a phantom stub row. Only this
      // connection's own listing can say a name was removed; a replaced
      // connection's rows are handled below.
      if (state.listingEstablished && !state.listingFromPreviousConnection && !known) return;
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
        store.setState({ instances: [...state.instances, { ...row }] });
        return;
      }
      // Only the MODEL INVENTORY comes from this answer: every other field
      // of the row may have been updated by a newer read or write that
      // landed while this refresh was in flight, and a refresh only ever
      // knows about live models.
      const merged = state.instances.map((entry) => (entry.name === name ? { ...entry, models: row.models } : entry));
      if (!known) merged.push({ ...row });
      bump(refreshedInstances, name);
      store.setState({ instances: merged, error: null });
    } catch (err) {
      // Refused: this refresh's marker retires, so an echo that follows after
      // all - the refresh landed and only its reply was lost - reads as foreign
      // and re-reads. A reconnect already cleared the marker, making this a
      // no-op.
      retireLocalMutation(marker);
      throw err;
    } finally {
      // Drop this request's own in-flight count only while its bookkeeping
      // generation still stands: a cleared generation has already forgotten it,
      // and its count must not be decremented against whatever the next
      // generation (a client swapped back to the same object included) put
      // there. The version token is deliberately left in place - it is the
      // monotonic guard that keeps a later refresh from reusing this version.
      if (generation === bookkeepingGeneration) {
        const remaining = (inFlightRefreshes.get(name) ?? 1) - 1;
        if (remaining > 0) inFlightRefreshes.set(name, remaining);
        else inFlightRefreshes.delete(name);
      }
    }
  }

  // --- credential mutations and their own-echo correlation ------------------
  //
  // evener/auth/updated BroadcastAlls to every connected client after a
  // successful auth mutation (login/logout/apiKey set/an authorized device
  // poll) or provider-instance CRUD mutation (create/edit/remove/setDefault/
  // setModelDisabled/refreshModels) from ANY of them - InstanceEntry's own
  // activeSource/hasStoredOAuth/hasStoredFile/storedEmail fields, and the list
  // itself, are exactly what such a mutation changes, so a client that already
  // loaded the instance list goes stale otherwise. On the wire it carries
  // {provider, activeSource, originClientId} for an auth mutation
  // (notifyAuthUpdated, cmd/evener-hub/app_rpc.go) and just {originClientId} for
  // a provider-instance one (notifyInstanceUpdated, where no single
  // provider/activeSource pair honestly summarizes "the list changed"),
  // matching the generated EvenerAuthUpdatedParams.
  //
  // The originator is in that audience too, and it must keep receiving the
  // notification - other consumers of the broadcast depend on the originating
  // client's own echo to refresh after its own save. But the LISTING refetch is
  // redundant for the originator: an auth mutation schedules the store's own
  // refresh the moment a local write succeeds, and a provider-instance mutation
  // already applied the full-list answer its RPC returned - a refresh owned by
  // the store survives the issuing dialog being canceled, hidden, or unmounted
  // before the RPC resolves, which a caller-scoped refresh does not. Worse, the
  // echo's refetch is misread by a save/check in flight: a subscriber comparing
  // selfRefresh across the transition reads the echo-driven listing change as
  // someone else's and invalidates the fresh result or cancels the check. So
  // the originator's own echo schedules a self-marked
  // refresh rather than a foreign one: it coalesces with the store's own
  // post-write refresh, and the mark is what keeps the flow from invalidating on
  // the read that follows. It is correlated narrowly:
  //
  // - ONE MARKER PER MUTATION, armed when the mutation is ISSUED, then
  //   re-stamped when its RPC response lands (authMutation's and applyMutation's
  //   landed branches): the broadcast can reach this client before the RPC
  //   response does, so a resolve-time marker alone would miss an early echo, and
  //   a hub whose mutation + broadcast outlasts the window would otherwise have
  //   its own LATE echo read as foreign. The echo's window runs from whichever
  //   came last - issue or response. A marker records the subject its echo can
  //   name: the provider an auth write targeted, or nothing for an instance
  //   mutation, whose echo names no provider.
  // - COUNTED BY SUBJECT, not a single timestamp: back-to-back mutations (two
  //   saves in a guided flow, a retry, a poll landing on top of a save, an edit
  //   on top of a create) each broadcast one echo, so a lone marker would be
  //   spent by the first echo and leave the second self echo to be misread as an
  //   unrelated client's change. Each matching notification consumes exactly one
  //   marker - the oldest outstanding one for its subject, since echoes arrive
  //   in issue order - and only a notification beyond the outstanding count is
  //   foreign and still refetches.
  // - Retired when the response proves no echo will be attributed to it: a
  //   failed RPC, or a device poll that came back pending/expired rather than
  //   authorized. Each mutation retires exactly the marker it armed (never a
  //   later mutation's), so an older reply that lands after a newer mutation was
  //   issued cannot spend the newer mutation's echo. That covers the write that
  //   applied before its later step failed (cmd/evener-hub/app_write_applied.go):
  //   its broadcast is the no-data form - no provider, and no origin either,
  //   because the hub drops the origin for an errored write - so it is
  //   unattributable and always reads as foreign here, and the failing mutation
  //   has already retired the marker it armed, so it cannot absorb that echo.
  //   The store cannot tell "applied, no echo attributable" from "never
  //   applied" without the wire saying which, which it does not yet.
  // - Bounded by a short age window from the marker's latest stamp, so a marker
  //   that is never consumed (the echo was lost, or the notification arrived
  //   pre-response and the client disconnected) cannot outlive its meaning.
  //
  // Anything unmatched still refetches - other providers, an id-less
  // provider-instance notification (which names no mutation this client could
  // correlate on), a subject with no live marker - so unrelated clients'
  // changes keep arriving.
  let localMutations: LocalMutationMarker[] = [];
  let unsubscribeNotifications: (() => void) | undefined;

  // firstMarker finds the OLDEST outstanding marker for a subject: the provider
  // an auth notification named, or undefined for a provider-instance one.
  // Echoes for one subject arrive on one connection in issue order, so the
  // oldest is the one an arriving echo is for; taking the newest instead would
  // let an early echo spend a LATER mutation's marker and leave that mutation's
  // own echo to read as foreign.
  function firstMarker(provider: string | undefined): LocalMutationMarker | undefined {
    for (const marker of localMutations) {
      if (marker.provider === provider) return marker;
    }
    return undefined;
  }

  // firstLiveMarker is firstMarker with the stale markers in front of it dropped
  // as it looks: a marker whose echo was lost must not shadow a newer live
  // marker for the same subject and turn that newer mutation's id-less echo
  // foreign.
  function firstLiveMarker(provider: string | undefined): LocalMutationMarker | undefined {
    const now = Date.now();
    for (;;) {
      const marker = firstMarker(provider);
      if (marker === undefined) return undefined;
      if (!staleLocalMutation(marker, now)) return marker;
      retireLocalMutation(marker); // stale: no echo of ours left for it
    }
  }

  // noteLocalMutation arms one marker for a mutation the store is about to
  // issue and returns it, so that mutation can re-stamp or retire exactly its
  // own marker. Acting on "the latest marker for the subject" instead would let
  // one mutation's reply retarget another's marker when same-subject mutations
  // overlap.
  //
  // Arming also drops markers that have outlived their meaning. A
  // provider-instance marker can never be spent by an id-less notification
  // (those stay foreign), so without this a hub that ignores originClientId -
  // an older build - would strand one marker per successful instance mutation
  // for the life of the connection. Only SETTLED markers are dropped: an
  // in-flight marker must survive so its slow response can still restamp it and
  // its late echo still correlate.
  function noteLocalMutation(provider: string | undefined): LocalMutationMarker {
    const now = Date.now();
    localMutations = localMutations.filter((marker) => !staleLocalMutation(marker, now));
    const marker: LocalMutationMarker = { provider, issuedAt: now, settled: false };
    localMutations.push(marker);
    return marker;
  }

  // retireLocalMutation drops the exact marker a mutation armed, if it is still
  // outstanding: its echo arrived, or an outcome proved no echo will (a failed
  // RPC, a device poll that came back pending/expired). A marker an echo already
  // consumed - or a reconnect already cleared - is gone, so this is then a
  // no-op.
  function retireLocalMutation(marker: LocalMutationMarker): void {
    const index = localMutations.indexOf(marker);
    if (index !== -1) localMutations.splice(index, 1);
  }

  // restampLocalMutation settles the exact marker a mutation armed when its RPC
  // response lands, moving its echo window to now; a marker an early echo
  // already consumed is gone, so this is then a no-op.
  function restampLocalMutation(marker: LocalMutationMarker): void {
    if (!localMutations.includes(marker)) return;
    marker.issuedAt = Date.now();
    marker.settled = true;
  }

  // True exactly when this notification is this client's own echo of a
  // just-issued mutation; consumes one outstanding marker, so a stale marker
  // cannot suppress a later notification.
  //
  // The broadcast carries the id the originating mutation sent, so an echo is
  // attributed by identity first: a notification whose originClientId is this
  // client's own is its echo, and one naming a different client is foreign
  // however close in time. The provider-plus-latest-stamp rule stays for an
  // id-less AUTH notification - an older build, or a mutation made from the TUI -
  // where it is still as exact as that wire allows, and where the residual stays
  // bounded: a matched notification still re-reads the listing, so only the
  // guided flow's invalidation is skipped, and only within the window. A
  // provider-less notification with no id gets no such fallback: it names no
  // provider to correlate on, so it could be any client's instance write or the
  // server's own live-prefetch pass, and it stays foreign.
  function consumeOwnEcho(provider: string | undefined, originClientId: string | undefined): boolean {
    if (originClientId) {
      if (originClientId !== deps.ownClientId()) return false;
      // The id proves this echo is ours, so the stale markers firstLiveMarker
      // retires on the way are safe to drop. Consuming the OLDEST marker
      // instead - stale or not - would leave a live marker behind for a later
      // foreign id-less notification to absorb, presenting that foreign change
      // as this store's own refresh.
      const marker = firstLiveMarker(provider);
      if (marker === undefined) return false;
      retireLocalMutation(marker);
      return true;
    }
    // Id-less: only a provider-bearing auth notification can be correlated this
    // way, and only against a marker still inside its window. A stale marker in
    // front of a live one is dropped rather than ending the search.
    if (provider === undefined) return false;
    const marker = firstLiveMarker(provider);
    if (marker === undefined) return false;
    retireLocalMutation(marker);
    return true;
  }

  function handleNotification(n: AnyNotification): void {
    if (n.method !== "evener/auth/updated") return;
    // A notification this client takes for its own echo is suppressed as a
    // separate refresh, but it still schedules the store's own refresh: if the
    // real echo was lost and another client's same-subject change arrived
    // first, the marker is consumed by that change, and the store's post-save
    // refresh has already run - without this the foreign change would stay
    // invisible until something else refetched. The self mark keeps the guided
    // flow from invalidating on the coalesced read while the listing moves.
    scheduleRefetch(consumeOwnEcho(n.params.provider, n.params.originClientId));
  }

  function listenTo(client: CredentialInstancesClient | null): void {
    unsubscribeNotifications?.();
    unsubscribeNotifications = client?.onNotification(handleNotification);
  }

  // withFingerprint adds the endpoint the caller showed the user only when it
  // captured one: the hub reads the key's presence as the request to check it.
  function withFingerprint(expectedEndpointFingerprint: string | undefined): { expectedEndpointFingerprint?: string } {
    return expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {};
  }

  // authMutation runs one credential write that the hub may echo: it takes the
  // write gate, arms the provider's marker, and on a landed outcome re-stamps
  // it and schedules the store's own refresh; a refused or unconfirmed outcome
  // retires the marker instead. A landed outcome and an errored one (the write
  // may have applied before a later step failed - see the catch) also retire
  // that instance's in-flight refreshModels, whose inventory predates the
  // write; an unconfirmed outcome that wrote nothing (a pending device poll)
  // retires only the marker. `landed` lets a device poll report that only
  // an authorized tick is a write the hub broadcasts. Issuing the write bumps
  // the request ordering, so a listing read still in flight cannot publish
  // rows that predate it; the store's own refresh lands the post-write rows,
  // and the flag such a retired read left behind is dropped once the write
  // settles as the latest request.
  //
  // Re-stamping the marker when the response lands moves the echo window to
  // that moment, which covers the order the issue-time stamp alone misses - a
  // hub whose mutation and broadcast outlast the window (a loaded host, a
  // network-mounted state root) answers later than SELF_ECHO_WINDOW_MS after
  // the issue, and its own echo would then be read as a foreign change. A
  // marker an early echo already consumed is gone, so re-stamping it is a
  // no-op; a marker with no echo still ages out, now from the later stamp.
  //
  // A response from a connection since replaced or reconnected is ignored
  // whole: its marker went with that connection, and the read it would
  // schedule would carry the new connection's `self` mark - the reconnect's
  // own restore load covers whatever the old connection's write did.
  async function authMutation<T>(
    provider: string,
    request: (client: CredentialInstancesClient, origin: { originClientId: string }) => Promise<T>,
    landed: (result: T) => boolean = () => true,
  ): Promise<T> {
    const client = requireWritableClient();
    const issued = connection;
    const version = ++requestVersion;
    const marker = noteLocalMutation(provider);
    try {
      const result = await request(client, { originClientId: deps.ownClientId() });
      if (connection !== issued) return result;
      if (landed(result)) {
        restampLocalMutation(marker);
        scheduleRefetch(true);
        // A landed credential write installs no row of its own, so it is not a
        // landed mutation (keepWrittenRows must not keep the pre-write row over
        // a read's newer answer); it still retires that instance's in-flight
        // refreshModels, whose inventory predates the write.
        retireInFlightRefresh(provider);
      } else retireLocalMutation(marker);
      return result;
    } catch (err) {
      // Refused: the marker retires, so an echo that follows after all - the
      // write landed and only its reply was lost - reads as foreign and re-reads.
      // The same uncertainty governs the in-flight refreshModels: a credential
      // write can persist before a later step fails (the hub reports that with
      // writeApplied and still broadcasts it - notifyAuthWrite's writeDidApply
      // branch - so the client sees only an ordinary error, with no wire signal
      // to tell the two apart). Treating an errored write as possibly-applied,
      // exactly as the marker retirement above already does, retires that
      // instance's in-flight refresh: its inventory may predate the write and
      // would otherwise merge over the post-write listing.
      if (connection === issued) {
        retireLocalMutation(marker);
        // A discarded refresh could have been the sheet's only live inventory,
        // and a failure that never applied would leave nothing to replace it.
        // Re-read the listing so the model inventory is current either way; a
        // write that retired no refresh reads nothing of its own, the hub's echo
        // covering one that did land.
        if (retireInFlightRefresh(provider)) scheduleRefetch();
      }
      throw err;
    } finally {
      settleLoading(version);
    }
  }

  const store = createFrameworkFreeStore<CredentialInstancesState>(() => ({
    ...listState({ instances: [], availableProviders: [] }),
    loading: false,
    error: null,
    selfRefresh: 0,
    listingFromPreviousConnection: false,
    listingEstablished: false,

    async fetch() {
      return readListing(false);
    },

    async fetchSelf() {
      return readListing(true);
    },

    async create(params) {
      const client = requireWritableClient();
      const applied = await applyMutation(
        (origin) => client.request("evener/instance/create", { ...params, ...origin }),
        {
          instance: params.name,
        },
      );
      // Like setDefault: a superseded response lost the store's ordering race,
      // and a read may have snapshotted before this write landed, so that read
      // cannot reconcile the write. Schedule the store's own read to land the
      // post-mutation view. Owning it in the core - a timer on the store, not a
      // subscription in the issuing screen - is what makes it survive that
      // screen unmounting and remounting around this store.
      if (!applied) scheduleRefetch();
      return applied;
    },

    async edit(params, reconcile) {
      const client = requireWritableClient();
      const applied = await applyMutation(
        (origin) => client.request("evener/instance/edit", { ...params, ...origin }),
        {
          instance: params.name,
          supersededFor: reconcile?.onSuperseded,
        },
      );
      if (!applied) scheduleRefetch();
      return applied;
    },

    async remove(name, expectedEndpointFingerprint) {
      const client = requireWritableClient();
      const applied = await applyMutation(
        (origin) =>
          client.request("evener/instance/remove", {
            name,
            ...withFingerprint(expectedEndpointFingerprint),
            ...origin,
          }),
        { instance: name },
      );
      if (!applied) scheduleRefetch();
      return applied;
    },

    async setDefault(name) {
      const client = requireWritableClient();
      const applied = await applyMutation(
        (origin) => client.request("evener/instance/setDefault", { name, ...origin }),
        { instance: name },
      );
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
      await applyMutation((origin) => client.request("evener/instance/setModelDisabled", { ...params, ...origin }), {
        instance: params.name,
        supersededFor: (response) => reconcileToggledModel(response, params),
        written: { instance: params.name, model: params.model },
      });
    },

    refreshModels,

    setApiKey(provider, value, expectedEndpointFingerprint) {
      return authMutation(provider, (client, origin) =>
        client.request("evener/auth/apiKey/set", {
          provider,
          value,
          ...withFingerprint(expectedEndpointFingerprint),
          ...origin,
        }),
      );
    },

    setCredentialJson(provider, value, expectedEndpointFingerprint) {
      return authMutation(provider, (client, origin) =>
        client.request("evener/auth/credentialJson/set", {
          provider,
          value,
          ...withFingerprint(expectedEndpointFingerprint),
          ...origin,
        }),
      );
    },

    clearStoredKey(provider, expectedEndpointFingerprint) {
      return authMutation(provider, (client, origin) =>
        client.request("evener/auth/apiKey/clear", {
          provider,
          ...withFingerprint(expectedEndpointFingerprint),
          ...origin,
        }),
      );
    },

    logout(provider, expectedEndpointFingerprint) {
      return authMutation(provider, (client, origin) =>
        client.request("evener/auth/logout", { provider, ...withFingerprint(expectedEndpointFingerprint), ...origin }),
      );
    },

    async loginStart(provider) {
      return requireWritableClient().request("evener/auth/login/start", { provider });
    },

    loginComplete(provider, flowId, redirectUrl) {
      return authMutation(provider, (client, origin) =>
        client.request("evener/auth/login/complete", { provider, flowId, redirectUrl, ...origin }),
      );
    },

    async deviceStart(provider) {
      return requireWritableClient().request("evener/auth/device/start", { provider });
    },

    async authStatus(provider) {
      const client = requireClient();
      const status = await client.request("evener/auth/status", { provider });
      if (!connectionStillCurrent(client)) throw new Error(CONNECTION_REPLACED_ERROR);
      return status;
    },

    devicePoll(provider, flowId) {
      // Only an authorized poll broadcasts evener/auth/updated; a routine
      // pending/expired tick must not keep the marker armed, or a poll loop
      // would silence unrelated same-provider changes tick after tick. An
      // authorized poll also refreshes the listing through the store - the
      // polling dialog may already be closed by the time authorization lands.
      return authMutation(
        provider,
        (client, origin) => client.request("evener/auth/device/poll", { provider, flowId, ...origin }),
        (resp) => resp.state === "authorized",
      );
    },

    async testCredentials(provider, expectedEndpointFingerprint) {
      // A write's own gate: the probe dials the endpoint its row names, so a
      // probe from the previous connection's listing would reach a destination
      // this connection never read.
      return requireWritableClient().request("evener/auth/test", {
        provider,
        ...withFingerprint(expectedEndpointFingerprint),
      });
    },
  }));

  function connectionChanged(client: CredentialInstancesClient | null, state: ConnectionState): void {
    const previous = connection;
    const clientChanged = client !== previous.client;
    // Not a transition: the connection object, and with it the generation of
    // every mutation in flight, stays exactly what it was.
    if (!clientChanged && state === previous.state) return;
    connection = { client, state };
    requestVersion += 1;
    store.setState({ loading: false });
    cancelRefetch();
    // A marker belongs to the connection its mutation was issued on: the echo
    // cannot arrive on a different one, so a marker left over from a replaced
    // or reconnected client is pure suppression risk for whatever
    // same-subject notification comes next on the new connection.
    localMutations = [];
    // Whatever listing state holds was read through the connection that just
    // went away (or through the client being replaced); the rows stay on
    // screen until this connection's own read lands, but nothing may act on
    // them in the meantime. Keyed on the CLIENT alone: a transition on the same
    // client - a transport flap to reconnecting, a failed read's error state -
    // leaves the rows as much this client's as they were, and the actions on
    // them stay the user's to retry once it is ready again. Marking those stale
    // refused them with a message claiming a replacement that never happened.
    if (clientChanged) {
      store.setState({ listingFromPreviousConnection: true });
      clearClientBookkeeping();
      listenTo(client);
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
    localMutations = [];
    listenTo(null);
    connection = { client: null, state: "idle" };
    store.setState(store.getInitialState());
  }

  return { ...store, connectionChanged, resetForTests };
}
