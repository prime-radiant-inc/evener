// credentials.ts is the thin wire-truth gateway for the Providers &
// credentials settings section: evener/instance/{list,create,edit,remove,
// setDefault} plus the evener/auth/* RPCs the section's OAuth/API-key/device
// flows drive. Follows stores/threads.ts's own requireClient()-via-
// connectionStore pattern (this store has no connect() of its own).
//
// Every evener/instance/* mutation's Go handler returns the FULL updated
// InstanceListResponse (appwire/types.go) - so create/edit/remove/setDefault
// apply that response directly to `instances`/`availableProviders`/
// `diagnostics`/`userLayer`/`writesRefused` instead of issuing a separate
// evener/instance/list refetch, same round-trip the legacy credentials.html's
// own instanceCreate/instanceEdit/... + refresh() pattern achieves in two
// calls.
//
// Never-echo invariant: no method here stores a secret VALUE anywhere in
// this store's state - setApiKey/loginComplete/deviceStart/devicePoll return
// (and this store passes through) only AuthStatusResponse/AuthDeviceStart
// Response/AuthDevicePollResponse shapes, none of which carry the secret
// itself (write-only fields on the wire).
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { AppwireClientLike } from "../protocol/clientLike";
import { errorText } from "../protocol/errors";
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
  ProviderDescriptor,
} from "../protocol/types.gen";
import { connectionStore } from "./connection";

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("credentials store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

export interface CredentialsStoreState {
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
  // A marker that changes ONLY when a state transition came from the store's
  // own post-mutation refresh (see the auth wrappers below). Subscriptions
  // that watch for unrelated changes compare it across a transition to tell
  // this client's own refresh apart from a foreign listing change - the
  // listing-update twin of the own-echo correlation above.
  selfRefresh: number;
  // fetch resolves true when the response it carried was applied to the
  // listing, false when a newer request superseded it or the read failed
  // (the failure lands in `error`) - a resolved promise alone is never
  // proof the listing moved.
  fetch(): Promise<boolean>;
  // fetchSelf is fetch() for a read the caller is performing as its own work -
  // today the guided flow's refresh and its endpoint-refusal recovery. The
  // transition carries the selfRefresh marker, so a subscriber watching the
  // listing for unrelated changes does not read the caller's own read as one,
  // which is what keeps the flow from invalidating the operation that asked.
  // Only a caller's own operation qualifies: a read that could be reporting
  // someone else's change has to stay unmarked, because the marker is the only
  // signal that tells the two apart.
  fetchSelf(): Promise<boolean>;
  // create/edit/remove resolve true when the listing they answered with is
  // the one the store now holds, false when a newer request superseded it.
  // Callers that steer a flow on the strength of their own write (the sheet,
  // the add dialog, the removal report) act on the store's verdict, never on
  // the raw response.
  create(params: InstanceCreateParams): Promise<boolean>;
  edit(params: InstanceEditParams): Promise<boolean>;
  remove(name: string, expectedEndpointFingerprint?: string): Promise<boolean>;
  setDefault(name: string): Promise<void>;
  // Auth mutations return the raw wire response and never touch
  // instances/availableProviders synchronously - on success the store
  // schedules its own listing refresh (see the wrappers below), which
  // survives the issuing dialog unmounting before the RPC resolves;
  // callers may still fetch for their own steering, and failures surface as
  // inline errors/toasts in the caller rather than this store swallowing
  // them into an `error` field.
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
  // behind an active oauth/adc sign-in (issue #713).
  clearStoredKey(provider: string, expectedEndpointFingerprint?: string): Promise<AuthStatusResponse>;
  logout(provider: string, expectedEndpointFingerprint?: string): Promise<AuthLogoutResponse>;
  loginStart(provider: string): Promise<AuthLoginStartResponse>;
  loginComplete(provider: string, flowId: string, redirectUrl: string): Promise<AuthLoginCompleteResponse>;
  deviceStart(provider: string): Promise<AuthDeviceStartResponse>;
  devicePoll(provider: string, flowId: string): Promise<AuthDevicePollResponse>;
  testCredentials(provider: string, expectedEndpointFingerprint?: string): Promise<AuthTestResponse>;
}

// listState normalizes one instance/list answer into the store's own always-
// present shape. Every reader of the listing goes through it, so a field
// added to InstanceListResponse is defaulted in exactly one place.
type ListState = Pick<
  CredentialsStoreState,
  "instances" | "availableProviders" | "diagnostics" | "userLayer" | "writesRefused"
>;

function listState(resp: InstanceListResponse): ListState {
  return {
    instances: resp.instances,
    availableProviders: resp.availableProviders,
    diagnostics: resp.diagnostics ?? [],
    userLayer: resp.userLayer ?? "",
    writesRefused: resp.writesRefused ?? false,
  };
}

// emptyListState is the listing state before anything has been fetched, and
// the state resetCredentialsStoreForTests returns to. A function, not a
// shared literal: each caller gets its own arrays.
function emptyListState(): ListState {
  return { instances: [], availableProviders: [], diagnostics: [], userLayer: "", writesRefused: false };
}

let requestVersion = 0;
let requestedList = false;

// Reads and writes share ordering: only the most recently started request
// can replace the listing, even when responses arrive out of order. Reports
// whether THIS response is the one that replaced it: a superseded response
// carries a listing the store discarded, and a caller steering a view on the
// strength of its own write has to be able to tell the two apart.
async function applyMutation(request: () => Promise<InstanceListResponse>): Promise<boolean> {
  const version = ++requestVersion;
  try {
    const response = await request();
    if (version !== requestVersion) return false;
    credentialsStore.setState({ ...listState(response), loading: false, error: null });
    return true;
  } finally {
    if (version === requestVersion) credentialsStore.setState({ loading: false });
  }
}

let selfRefreshCounter = 0;

// Every listing read - a caller's fetch() and the store's own post-mutation
// refresh - shares this one read and its ordering guard. `self` marks THIS
// client's own refresh: each state transition it touches carries a fresh
// selfRefresh marker (and no other read ever changes it), so a subscription
// comparing the marker across a transition can tell the client's own refresh
// apart from a foreign listing change. Resolves true only when the response
// was applied - a superseded or failed read resolves false without throwing,
// so a resolved promise alone is never confirmation the listing moved.
async function readListing(self: boolean): Promise<boolean> {
  const client = requireClient();
  requestedList = true;
  const version = ++requestVersion;
  const mark = () => (self ? { selfRefresh: ++selfRefreshCounter } : {});
  credentialsStore.setState({ loading: true, error: null, ...mark() });
  try {
    const resp = await client.request("evener/instance/list", {});
    if (version !== requestVersion || connectionStore.getState().client !== client) return false;
    credentialsStore.setState({ ...listState(resp), loading: false, ...mark() });
    return true;
  } catch (err) {
    if (version !== requestVersion || connectionStore.getState().client !== client) return false;
    credentialsStore.setState({ loading: false, error: errorText(err), ...mark() });
    return false;
  }
}

export const credentialsStore = createStore<CredentialsStoreState>(() => ({
  ...emptyListState(),
  loading: false,
  error: null,
  selfRefresh: 0,

  async fetch() {
    return readListing(false);
  },

  async fetchSelf() {
    return readListing(true);
  },

  async create(params) {
    const client = requireClient();
    return applyMutation(() => client.request("evener/instance/create", params));
  },

  async edit(params) {
    const client = requireClient();
    return applyMutation(() => client.request("evener/instance/edit", params));
  },

  async remove(name, expectedEndpointFingerprint) {
    const client = requireClient();
    return applyMutation(() =>
      client.request("evener/instance/remove", {
        name,
        ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
      }),
    );
  },

  async setDefault(name) {
    const client = requireClient();
    await applyMutation(() => client.request("evener/instance/setDefault", { name }));
  },

  async setApiKey(provider, value, expectedEndpointFingerprint) {
    const client = requireClient();
    const generation = connectionGeneration;
    noteLocalAuthMutation(provider);
    try {
      const result = await client.request("evener/auth/apiKey/set", {
        provider,
        value,
        ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
      });
      // The store owns the post-mutation listing refresh: the caller that
      // issued the save may be canceled, hidden, or unmounted before this
      // resolves, and its own refresh would die with it. The helper also
      // re-stamps this mutation's echo window from the response, and the
      // `self` mark lets subscriptions (ProviderConnection's invalidation
      // watch) tell the refresh it schedules apart from a foreign listing
      // change.
      completeLocalAuthMutation(provider, generation);
      return result;
    } catch (err) {
      endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
      throw err;
    }
  },

  async setCredentialJson(provider, value, expectedEndpointFingerprint) {
    const client = requireClient();
    const generation = connectionGeneration;
    noteLocalAuthMutation(provider);
    try {
      const result = await client.request("evener/auth/credentialJson/set", {
        provider,
        value,
        ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
      });
      completeLocalAuthMutation(provider, generation); // same rationale as setApiKey
      return result;
    } catch (err) {
      endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
      throw err;
    }
  },

  async clearStoredKey(provider, expectedEndpointFingerprint) {
    const client = requireClient();
    const generation = connectionGeneration;
    noteLocalAuthMutation(provider);
    try {
      const result = await client.request("evener/auth/apiKey/clear", {
        provider,
        ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
      });
      completeLocalAuthMutation(provider, generation); // same rationale as setApiKey
      return result;
    } catch (err) {
      endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
      throw err;
    }
  },

  async logout(provider, expectedEndpointFingerprint) {
    const client = requireClient();
    const generation = connectionGeneration;
    noteLocalAuthMutation(provider);
    try {
      const result = await client.request("evener/auth/logout", {
        provider,
        ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
      });
      completeLocalAuthMutation(provider, generation); // same rationale as setApiKey
      return result;
    } catch (err) {
      endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
      throw err;
    }
  },

  async loginStart(provider) {
    const client = requireClient();
    return client.request("evener/auth/login/start", { provider });
  },

  async loginComplete(provider, flowId, redirectUrl) {
    const client = requireClient();
    const generation = connectionGeneration;
    noteLocalAuthMutation(provider);
    try {
      const result = await client.request("evener/auth/login/complete", { provider, flowId, redirectUrl });
      completeLocalAuthMutation(provider, generation); // same rationale as setApiKey
      return result;
    } catch (err) {
      endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
      throw err;
    }
  },

  async deviceStart(provider) {
    const client = requireClient();
    return client.request("evener/auth/device/start", { provider });
  },

  async devicePoll(provider, flowId) {
    const client = requireClient();
    const generation = connectionGeneration;
    noteLocalAuthMutation(provider);
    try {
      const resp = await client.request("evener/auth/device/poll", { provider, flowId });
      // Only an authorized poll broadcasts evener/auth/updated; a routine
      // pending/expired tick must not keep the marker armed, or a poll loop
      // would silence unrelated same-provider changes tick after tick. An
      // authorized poll also refreshes the listing through the store - the
      // polling dialog may already be closed by the time authorization lands.
      if (resp.state === "authorized") completeLocalAuthMutation(provider, generation);
      else endUnconfirmedAuthMutation(provider, generation);
      return resp;
    } catch (err) {
      endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
      throw err;
    }
  },

  async testCredentials(provider, expectedEndpointFingerprint) {
    const client = requireClient();
    return client.request("evener/auth/test", {
      provider,
      ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
    });
  },
}));

export function useCredentialsStore(): CredentialsStoreState;
export function useCredentialsStore<T>(selector: (state: CredentialsStoreState) => T): T;
export function useCredentialsStore<T>(selector?: (state: CredentialsStoreState) => T): T | CredentialsStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(credentialsStore, selector) : useStore(credentialsStore);
}

// --- notification-triggered refetch --------------------------------------
//
// evener/auth/updated BroadcastAlls to every connected client after a
// successful auth mutation (login/logout/apiKey set/an authorized device
// poll) from ANY of them - InstanceEntry's own activeSource/hasStoredOAuth/
// hasStoredFile/storedEmail fields are exactly what such a mutation changes,
// so a browser tab that already loaded the instance list goes stale
// otherwise. Mirrors stores/extensions.ts's identical
// wiring, applied here to this store's one wire-truth list. On the wire
// evener/auth/updated carries {provider, activeSource} (notifyAuthUpdated,
// cmd/evener-hub/app_rpc.go) matching the generated EvenerAuthUpdatedParams,
// and `provider` is what the own-echo correlation below reads.
//
// The originator is in that audience too, and it must keep receiving the
// notification - other consumers (the model-list cache's epoch guard) depend
// on the originating client's own echo to refresh after its own save. But the
// LISTING refetch is redundant for the originator: the STORE schedules its own
// refresh the moment a local auth mutation succeeds (see the wrappers below) -
// a refresh owned by the store survives the issuing dialog being canceled,
// hidden, or unmounted before the RPC resolves, which a caller-scoped refresh
// (ProviderConnection's refreshAndCheck, CredentialValueDialog, the OAuth
// dialogs' completions) does not. Worse, the echo's refetch is misread by a
// save/check in flight: ProviderConnection's subscription treats the
// echo-driven listing change as an unrelated change and invalidates the fresh
// result ("Connection or configuration changed") or cancels the check. So the
// originator's own echo schedules a self-marked refresh rather than a foreign
// one: it coalesces with the store's own post-save refresh, and the mark is
// what keeps the flow from invalidating on the read that follows. It is
// correlated narrowly:
//
// - Marked per provider when the mutation is ISSUED, then re-stamped when its
//   RPC response lands (completeLocalAuthMutation above): the broadcast can
//   reach this client before the RPC response does, so a resolve-time marker
//   alone would miss an early echo, and a hub whose mutation + broadcast
//   outlasts the window would otherwise have its own LATE echo read as
//   foreign. The echo's window runs from whichever came last - issue or
//   response.
// - COUNTED, not a single timestamp: back-to-back same-provider mutations
//   (two saves in the guided flow, a retry, a poll landing on top of a save)
//   each broadcast one echo, so a lone per-provider marker would let the
//   first echo consume the second mutation's marker and leave the second self
//   echo to be misread as an unrelated client's change - spuriously
//   invalidating the guided flow. The per-provider entry holds the count of
//   outstanding mutations plus the latest stamp (the issue, or the response
//   that re-stamped it); each matching notification consumes exactly one, and
//   only a notification beyond the outstanding count is foreign and still
//   refetches.
// - Cleared when the response proves no broadcast will follow: a failed RPC,
//   or a device poll that comes back pending/expired rather than authorized.
//   One outstanding marker is retired per such outcome (a floor, not an
//   unconditional clear): which mutation failed does not matter, only how
//   many echoed mutations remain outstanding. A failed save must not silence
//   the next unrelated change, and a poll loop must not keep re-arming the
//   window tick after tick.
// - Bounded by a short age window from the marker's latest stamp (issue or
//   response), so a marker that is never consumed (the echo was lost, or the
//   notification arrived pre-response and the client disconnected) cannot
//   outlive its meaning.
//
// Anything unmatched still refetches - other providers, unattributed
// notifications, the same provider with no live marker - so unrelated
// clients' changes keep arriving.
const REFETCH_DEBOUNCE_MS = 250;
// Age budget from the marker's latest stamp - the issue, or the RPC response
// that re-stamped it (completeLocalAuthMutation below).
const SELF_ECHO_WINDOW_MS = 2000;
interface LocalAuthMutationMarker {
  // Outstanding same-provider mutations issued but not yet consumed (by an
  // echo) or retired (by an unconfirmed outcome).
  count: number;
  // The entry's latest life event: the most recent issue, or the RPC
  // response that re-stamped it; ages the whole entry out together.
  issuedAt: number;
}
const localAuthMutations = new Map<string, LocalAuthMutationMarker>();

// The connection the markers belong to. A marker's echo can only arrive on the
// connection its mutation was issued on, so a callback that lands after that
// connection is replaced or reconnects must not touch the markers of the
// connection now in place - nor schedule a `self`-marked read there, which
// would tell the guided flow that a read carrying someone else's change was
// its own. Bumped exactly where the markers are cleared, so "the marker is
// gone" and "the callback is stale" stay one fact (see the wrappers below).
let connectionGeneration = 0;

function noteLocalAuthMutation(provider: string): void {
  const existing = localAuthMutations.get(provider);
  localAuthMutations.set(provider, { count: (existing?.count ?? 0) + 1, issuedAt: Date.now() });
}

function clearLocalAuthMutation(provider: string): void {
  localAuthMutations.delete(provider);
}

// Ends a local auth mutation whose outcome proves it will NOT broadcast (a
// failed RPC, or a device poll that returned pending/expired rather than
// authorized). The marker only ever suppressed a notification's own refetch,
// and a matched notification now schedules the store's own refresh itself, so
// there is no swallowed change left to make up: retiring one outstanding
// marker is all this owes, and once the count reaches zero the next
// same-provider notification is foreign again. Which mutation ended does not
// matter - only how many echoed mutations remain outstanding - so this
// decrements whether or not it was the mutation that failed. A callback that
// lands after the issuing connection is gone is ignored whole: its marker was
// cleared with that connection, and an entry still here belongs to the new one.
function endUnconfirmedAuthMutation(provider: string, generation: number): void {
  if (generation !== connectionGeneration) return;
  const existing = localAuthMutations.get(provider);
  if (existing === undefined) return;
  if (existing.count <= 1) localAuthMutations.delete(provider);
  else localAuthMutations.set(provider, { count: existing.count - 1, issuedAt: existing.issuedAt });
}

// Confirms a local auth mutation whose RPC response landed: the hub accepted
// the write, so its broadcast is (or was) on its way. Re-stamping the
// provider's marker moves the echo window to this moment, which covers the
// order the issue-time stamp alone misses - a hub whose mutation and broadcast
// outlast the window (a loaded host, a network-mounted state root) answers
// later than SELF_ECHO_WINDOW_MS after the issue, and its own echo would then
// be read as a foreign change: the coalesced post-save refresh loses the self
// mark and the guided flow answers a successful save with "Connection or
// configuration changed". A marker an early echo already consumed is gone, so
// re-stamping it is a no-op; a marker with no echo still ages out, now from
// the later stamp. Also schedules the store's own refresh, which the echo
// coalesces with instead of duplicating (see the correlation comment above).
// A response from a connection that has since been replaced or reconnected is
// ignored whole: its marker went with that connection, and the read it would
// schedule would carry the new connection's `self` mark. Whatever the old
// connection's write did to the listing is covered by the reconnect's own
// restore load instead.
function completeLocalAuthMutation(provider: string, generation: number): void {
  if (generation !== connectionGeneration) return;
  const existing = localAuthMutations.get(provider);
  if (existing !== undefined) {
    localAuthMutations.set(provider, { count: existing.count, issuedAt: Date.now() });
  }
  scheduleRefetch(true);
}

// True exactly when this notification is this client's own echo of a
// just-issued auth mutation; consumes one outstanding marker either way, so a
// stale entry cannot suppress a later notification. A stale entry (its latest
// stamp - the mutation's issue time, or the RPC response that re-stamped it -
// older than the window) counts as no marker at all and is dropped whole.
//
// The correlation is provider + the marker's latest stamp (the issue, then
// the RPC response once it lands - see completeLocalAuthMutation), and that is
// as exact as the wire allows: evener/auth/updated carries only {provider,
// activeSource} (types.gen.ts), with no originator id - the hub's
// notifyAuthUpdated broadcasts to every client alike (app_rpc.go). A corner
// remains: another client's same-provider change arriving inside the window is
// read as this client's own echo. It is deliberately not resolved by treating
// same-provider notifications as foreign, which would make the originator's
// own echo look foreign and re-invalidate a successful guided save (the
// round-8 defect); narrowing the window instead would miss the echo that
// arrives late. Exact attribution needs an origin id on the auth mutation
// RPCs, echoed back in the broadcast - a wire change, not a frontend one. The
// residual is bounded: a matched notification still re-reads the listing
// (round 20), so only the guided flow's invalidation is skipped, and only for
// a same-provider change landing within two seconds of this client's own
// mutation's latest stamp - re-stamping on the response widens that blind spot
// by the response's own latency, the cost of not misreading a slow hub's late
// echo as foreign.
function consumeOwnAuthEcho(provider: string | undefined): boolean {
  if (provider === undefined) return false;
  const marker = localAuthMutations.get(provider);
  if (marker === undefined) return false;
  if (Date.now() - marker.issuedAt > SELF_ECHO_WINDOW_MS) {
    localAuthMutations.delete(provider); // stale: no marker, no echo of ours left
    return false;
  }
  // One mutation broadcasts one echo: consume exactly one outstanding marker.
  if (marker.count <= 1) clearLocalAuthMutation(provider);
  else localAuthMutations.set(provider, { count: marker.count - 1, issuedAt: marker.issuedAt });
  return true;
}

let wiredClient: AppwireClientLike | null = null;
let unsubscribeNotifications: (() => void) | undefined;
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

function scheduleRefetch(self = false): void {
  pendingRefetchSelf = pendingRefetchSelf === undefined ? self : pendingRefetchSelf && self;
  clearTimeout(refetchTimer);
  refetchTimer = setTimeout(() => {
    const selfRequest = pendingRefetchSelf ?? false;
    pendingRefetchSelf = undefined;
    refetchTimer = undefined;
    // fetch()'s own requireClient() throws outside its try/catch, by design
    // (see this file's own top comment) - a real rejection here would be an
    // unobserved background call with nothing awaiting it, so a rare
    // disconnect-during-the-debounce-window race must be swallowed here
    // rather than surfacing as an unhandled rejection.
    readListing(selfRequest).catch(() => {});
  }, REFETCH_DEBOUNCE_MS);
}

function handleNotification(n: AnyNotification): void {
  if (n.method !== "evener/auth/updated") return;
  // A notification this client takes for its own echo is suppressed as a
  // separate refresh, but it still schedules the store's own refresh: if the
  // real echo was lost and another client's same-provider change arrived
  // first, the marker is consumed by that change, and the store's post-save
  // refresh has already run - without this the foreign change would stay
  // invisible until something else refetched. The self mark keeps the guided
  // flow from invalidating on the coalesced read while the listing moves.
  scheduleRefetch(consumeOwnAuthEcho(n.params.provider));
}

function attachNotifications(client: AppwireClientLike | null): void {
  if (client === wiredClient) return; // already wired to this exact client
  unsubscribeNotifications?.();
  clearTimeout(refetchTimer);
  refetchTimer = undefined;
  wiredClient = client;
  unsubscribeNotifications = client?.onNotification(handleNotification);
}

// Watches connectionStore for the client becoming available and attaches
// this store's own notification handler to it - see stores/extensions.ts's
// identical wiring for the full "why react to the store instead of reading
// it once" rationale (a mount-order race between this module and AppShell's
// own connect() effect).
connectionStore.subscribe((state, previous) => {
  if (state.client !== previous.client || state.state !== previous.state) {
    requestVersion += 1;
    credentialsStore.setState({ loading: false });
    clearTimeout(refetchTimer);
    refetchTimer = undefined;
    pendingRefetchSelf = undefined;
    // A marker belongs to the connection its mutation was issued on: the echo
    // cannot arrive on a different one, so a marker left over from a replaced
    // or reconnected client is pure suppression risk for whatever
    // same-provider notification comes next on the new connection, and the
    // callbacks of mutations still in flight on the old one belong to a
    // connection that is gone (see connectionGeneration).
    localAuthMutations.clear();
    connectionGeneration += 1;
  }
  attachNotifications(state.client);
  // Once a view has requested credentials, reconnects must restore its list
  // even if its one-shot mount loader was interrupted.
  if (
    requestedList &&
    state.client &&
    state.state === "ready" &&
    (state.client !== previous.client || previous.state !== "ready")
  ) {
    void credentialsStore
      .getState()
      .fetch()
      .catch(() => {});
  }
});
const initialClient = connectionStore.getState().client;
if (initialClient) attachNotifications(initialClient);

// resetCredentialsStoreForTests resets this singleton store's state between
// tests, including the module-private wiring/debounce bookkeeping above -
// mirroring resetThreadsStoreForTests/resetTreeStoreForTests. No production
// code should ever call this.
export function resetCredentialsStoreForTests(): void {
  requestVersion += 1;
  requestedList = false;
  localAuthMutations.clear();
  connectionGeneration += 1;
  unsubscribeNotifications?.();
  unsubscribeNotifications = undefined;
  wiredClient = null;
  clearTimeout(refetchTimer);
  refetchTimer = undefined;
  pendingRefetchSelf = undefined;
  credentialsStore.setState({ ...emptyListState(), loading: false, error: null, selfRefresh: 0 });
}
