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
import { hostRequest, isLocalHost, LOCAL_HOST } from "./hostRouting";

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("credentials store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

export interface CredentialsStoreState {
  // instances/availableProviders/diagnostics/userLayer/writesRefused are the
  // CONTROLLER's own (LOCAL_HOST) provider listing, exactly as before component
  // 07b: Settings > Credentials and ConnectProviderDialog are controller-scoped
  // editors and read these. A remote host's listing is kept in `hosts` below,
  // partitioned by host id, so a host-scoped load can never overwrite (or be
  // overwritten by) the controller's list.
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
  // hosts holds one partition per NON-CONTROLLER host id, each the listing
  // evener/instance/list returned for that host through evener/host/request.
  // Keyed by host so a remote spawn form reads the registry of the machine it
  // is about to launch on without the controller-scoped top-level fields -- and
  // without the evener/auth/updated refetch -- ever seeing it.
  hosts: Record<string, HostInstanceState>;
  fetch(): Promise<void>;
  // fetchHost scopes evener/instance/list to the selected host (component
  // 07b): a remote host's provider instances are read from that host's own
  // registry through evener/host/request, so the spawn form's provider setup
  // describes the machine it is about to launch on. It shares fetch()'s
  // ordering guard and normalization, but NOT its slot: the controller's list
  // stays in the top-level fields and a remote host's lands in `hosts[host]`,
  // so neither load can overwrite the other. fetch() itself is controller-only.
  fetchHost(host: string): Promise<void>;
  create(params: InstanceCreateParams): Promise<void>;
  // Resolves true when the listing this edit answered with is the one the
  // store now holds, false when a newer request superseded it. The instance
  // sheet steers itself on the store's verdict, never on the raw response.
  edit(params: InstanceEditParams): Promise<boolean>;
  remove(name: string): Promise<void>;
  setDefault(name: string): Promise<void>;
  // Auth mutations return the raw wire response and never touch
  // instances/availableProviders themselves - the caller (CredentialsSection)
  // re-fetches on success, matching the legacy's own "close editor +
  // refresh()" sequencing, and surfaces failures as inline errors/toasts
  // itself rather than this store swallowing them into an `error` field.
  setApiKey(provider: string, value: string): Promise<AuthStatusResponse>;
  setCredentialJson(provider: string, value: string): Promise<AuthStatusResponse>;
  // clearStoredKey removes only the credentials.toml entry, leaving any
  // OAuth/ADC/env credential untouched - the counterpart to setApiKey, and
  // the narrow alternative to logout() for a stray stored key shadowed
  // behind an active oauth/adc sign-in (issue #713).
  clearStoredKey(provider: string): Promise<AuthStatusResponse>;
  logout(provider: string): Promise<AuthLogoutResponse>;
  loginStart(provider: string): Promise<AuthLoginStartResponse>;
  loginComplete(provider: string, flowId: string, redirectUrl: string): Promise<AuthLoginCompleteResponse>;
  deviceStart(provider: string): Promise<AuthDeviceStartResponse>;
  devicePoll(provider: string, flowId: string): Promise<AuthDevicePollResponse>;
  testCredentials(provider: string): Promise<AuthTestResponse>;
}

// listState normalizes one instance/list answer into the store's own always-
// present shape. Every reader of the listing goes through it, so a field
// added to InstanceListResponse is defaulted in exactly one place.
type ListState = Pick<
  CredentialsStoreState,
  "instances" | "availableProviders" | "diagnostics" | "userLayer" | "writesRefused"
>;

// HostInstanceState is one non-controller host's own provider listing: the
// same normalized fields as the controller's, plus that host's own request
// status. A remote host's load cannot move the controller's `loading`/`error`,
// and the controller's refetch cannot move a remote host's.
export interface HostInstanceState extends ListState {
  loading: boolean;
  error: string | null;
}

// EMPTY_HOST_INSTANCE_STATE is a host partition before its first load. A module
// constant, not a fresh literal: useProviderSetup selects it so a host with no
// entry yet has a stable snapshot identity instead of re-rendering forever.
export const EMPTY_HOST_INSTANCE_STATE: HostInstanceState = Object.freeze({
  instances: [],
  availableProviders: [],
  diagnostics: [],
  userLayer: "",
  writesRefused: false,
  loading: false,
  error: null,
});

/**
 * hostPartition reads one non-controller host's own partition out of the store
 * state, or the empty partition before that host has ever been loaded. The
 * controller's own listing is NOT a partition (it is the top-level fields), so
 * callers reading a selected host choose that branch themselves - see
 * useProviderSetup.
 */
export function hostPartition(state: CredentialsStoreState, host: string): HostInstanceState {
  return state.hosts[host] ?? EMPTY_HOST_INSTANCE_STATE;
}

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

// One request counter per host plus a connection epoch. Ordering is per host:
// a slow remote load must neither cancel an unrelated controller read nor be
// cancelled by it (and two different remote hosts must not race each other),
// while a connection change still retires every in-flight load at once.
let connectionEpoch = 0;
const hostVersions = new Map<string, number>();
let requestedList = false;

type RequestToken = { host: string; version: number; epoch: number };

function beginRequest(host: string): RequestToken {
  const version = (hostVersions.get(host) ?? 0) + 1;
  hostVersions.set(host, version);
  return { host, version, epoch: connectionEpoch };
}

// Reports whether THIS request is still the most recent one started for its
// host on the same connection. Reads and writes share this: only the latest
// request per host can replace that host's listing, even when responses arrive
// out of order.
function isCurrentRequest(token: RequestToken, client: AppwireClientLike): boolean {
  return (
    hostVersions.get(token.host) === token.version &&
    connectionEpoch === token.epoch &&
    connectionStore.getState().client === client
  );
}

// applyMutation applies one controller-scoped evener/instance/* response to the
// controller's own listing. Reports whether THIS response is the one that
// replaced it: a superseded response carries a listing the store discarded, and
// a caller steering a view on the strength of its own write has to be able to
// tell the two apart.
async function applyMutation(request: () => Promise<InstanceListResponse>): Promise<boolean> {
  const client = requireClient();
  const token = beginRequest(LOCAL_HOST);
  try {
    const response = await request();
    if (!isCurrentRequest(token, client)) return false;
    credentialsStore.setState({ ...listState(response), loading: false, error: null });
    return true;
  } finally {
    if (isCurrentRequest(token, client)) credentialsStore.setState({ loading: false });
  }
}

// loadInstances is the single evener/instance/list path. fetch() and
// fetchHost(host) both route through it, so the ordering guard and the
// always-present list state are applied in exactly one place. The controller's
// own list (LOCAL_HOST) lands in the top-level fields; every other host's lands
// in its own `hosts` partition.
async function loadInstances(host: string): Promise<void> {
  const client = requireClient();
  requestedList = true;
  const token = beginRequest(host);
  if (isLocalHost(host)) {
    credentialsStore.setState({ loading: true, error: null });
    try {
      const resp = await hostRequest(client, host, "evener/instance/list", {});
      if (!isCurrentRequest(token, client)) return;
      credentialsStore.setState({ ...listState(resp), loading: false });
    } catch (err) {
      if (!isCurrentRequest(token, client)) return;
      credentialsStore.setState({ loading: false, error: errorText(err) });
    }
    return;
  }
  setHostState(host, (previous) => ({ ...previous, loading: true, error: null }));
  try {
    const resp = await hostRequest(client, host, "evener/instance/list", {});
    if (!isCurrentRequest(token, client)) return;
    setHostState(host, () => ({ ...listState(resp), loading: false, error: null }));
  } catch (err) {
    if (!isCurrentRequest(token, client)) return;
    setHostState(host, (previous) => ({ ...previous, loading: false, error: errorText(err) }));
  }
}

// setHostState replaces one host's partition through the same `set` every
// reader sees: the function form keeps the other partitions' identities stable,
// so subscribing selectors re-render only for the host that changed.
function setHostState(host: string, update: (previous: HostInstanceState) => HostInstanceState): void {
  credentialsStore.setState((state) => {
    const previous = state.hosts[host] ?? EMPTY_HOST_INSTANCE_STATE;
    const next = update(previous);
    if (next === previous) return {};
    return { hosts: { ...state.hosts, [host]: next } };
  });
}

export const credentialsStore = createStore<CredentialsStoreState>(() => ({
  ...emptyListState(),
  hosts: {},
  loading: false,
  error: null,

  async fetch() {
    await loadInstances(LOCAL_HOST);
  },

  async fetchHost(host) {
    await loadInstances(host);
  },

  async create(params) {
    const client = requireClient();
    await applyMutation(() => client.request("evener/instance/create", params));
  },

  async edit(params) {
    const client = requireClient();
    return applyMutation(() => client.request("evener/instance/edit", params));
  },

  async remove(name) {
    const client = requireClient();
    await applyMutation(() => client.request("evener/instance/remove", { name }));
  },

  async setDefault(name) {
    const client = requireClient();
    await applyMutation(() => client.request("evener/instance/setDefault", { name }));
  },

  async setApiKey(provider, value) {
    const client = requireClient();
    return client.request("evener/auth/apiKey/set", { provider, value });
  },

  async setCredentialJson(provider, value) {
    const client = requireClient();
    return client.request("evener/auth/credentialJson/set", { provider, value });
  },

  async clearStoredKey(provider) {
    const client = requireClient();
    return client.request("evener/auth/apiKey/clear", { provider });
  },

  async logout(provider) {
    const client = requireClient();
    return client.request("evener/auth/logout", { provider });
  },

  async loginStart(provider) {
    const client = requireClient();
    return client.request("evener/auth/login/start", { provider });
  },

  async loginComplete(provider, flowId, redirectUrl) {
    const client = requireClient();
    return client.request("evener/auth/login/complete", { provider, flowId, redirectUrl });
  },

  async deviceStart(provider) {
    const client = requireClient();
    return client.request("evener/auth/device/start", { provider });
  },

  async devicePoll(provider, flowId) {
    const client = requireClient();
    return client.request("evener/auth/device/poll", { provider, flowId });
  },

  async testCredentials(provider) {
    const client = requireClient();
    return client.request("evener/auth/test", { provider });
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
// cmd/evener-hub/app_rpc.go:764-767), but its generated
// EvenerAuthUpdatedPayload type is empty ({}) because codegen can't see
// into Go's untyped map[string]string - and this refetch is
// payload-agnostic anyway (nothing reads those fields), so a debounced
// evener/instance/list refetch is the only option, exactly like
// evener/navigation/invalidated's own "just refetch" contract.
//
// A credential change made ON a remote host does not arrive as the unwrapped
// evener/auth/updated above: the hub's host-notification fan-out re-emits that
// host's own evener/auth/updated to this browser wrapped in
// evener/host/notification tagged with the host
// (cmd/evener-hub/app_host_admin.go's remoteHostConfigNotifications/relayHostNotifications).
// It is reconciled against the host's OWN partition, never the controller's:
// fetch() and the top-level fields are controller-only, so a remote change
// debounces a fetchHost(host) instead.
const REFETCH_DEBOUNCE_MS = 250;

let wiredClient: AppwireClientLike | null = null;
let unsubscribeNotifications: (() => void) | undefined;
let refetchTimer: ReturnType<typeof setTimeout> | undefined;
// hostRefetchTimers debounces each remote host's own refetch independently. One
// shared timer would collapse a burst from two hosts into a load of whichever
// arrived last, leaving the other host's partition stale.
const hostRefetchTimers = new Map<string, ReturnType<typeof setTimeout>>();

function scheduleRefetch(): void {
  clearTimeout(refetchTimer);
  refetchTimer = setTimeout(() => {
    // fetch()'s own requireClient() throws outside its try/catch, by design
    // (see this file's own top comment) - a real rejection here would be an
    // unobserved background call with nothing awaiting it, so a rare
    // disconnect-during-the-debounce-window race must be swallowed here
    // rather than surfacing as an unhandled rejection.
    credentialsStore
      .getState()
      .fetch()
      .catch(() => {});
  }, REFETCH_DEBOUNCE_MS);
}

// scheduleHostRefetch debounces a remote host's own listing reload so a
// credential/model change on that host refreshes `hosts[host]` -- and, through
// it, the spawn form's provider verdict and catalog -- without touching the
// controller's fields. Mirrors scheduleRefetch's own swallow: fetchHost's
// requireClient() throws outside its try/catch by design, and a rare
// disconnect-during-the-debounce race must not surface as an unhandled
// rejection on a call nothing awaits.
function scheduleHostRefetch(host: string): void {
  clearTimeout(hostRefetchTimers.get(host));
  hostRefetchTimers.set(
    host,
    setTimeout(() => {
      hostRefetchTimers.delete(host);
      credentialsStore
        .getState()
        .fetchHost(host)
        .catch(() => {});
    }, REFETCH_DEBOUNCE_MS),
  );
}

function clearHostRefetchTimers(): void {
  for (const timer of hostRefetchTimers.values()) clearTimeout(timer);
  hostRefetchTimers.clear();
}

function handleNotification(n: AnyNotification): void {
  if (n.method === "evener/auth/updated") {
    scheduleRefetch();
    return;
  }
  // The host fan-out only re-emits the allow-listed host-owned config
  // notifications (app_host_admin.go:189-195); of those, this store's own
  // wire-truth list is the instance listing, so only a wrapped
  // evener/auth/updated is actionable here.
  if (n.method === "evener/host/notification" && n.params.method === "evener/auth/updated") {
    scheduleHostRefetch(n.params.host);
  }
}

function attachNotifications(client: AppwireClientLike | null): void {
  if (client === wiredClient) return; // already wired to this exact client
  unsubscribeNotifications?.();
  clearTimeout(refetchTimer);
  refetchTimer = undefined;
  clearHostRefetchTimers();
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
    // A new connection retires every in-flight load at once, and no host
    // partition may stay stuck on "loading" behind a request that can no
    // longer commit.
    connectionEpoch += 1;
    credentialsStore.setState((current) => ({ loading: false, hosts: clearHostLoading(current.hosts) }));
    clearTimeout(refetchTimer);
    refetchTimer = undefined;
    clearHostRefetchTimers();
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

// clearHostLoading releases every remote host's in-flight status without
// discarding the listing it holds - a reconnect's refetch either replaces that
// listing or reports its own failure, exactly like the controller's own fields.
function clearHostLoading(hosts: Record<string, HostInstanceState>): Record<string, HostInstanceState> {
  let changed = false;
  const next: Record<string, HostInstanceState> = {};
  for (const [host, hostState] of Object.entries(hosts)) {
    if (hostState.loading) {
      next[host] = { ...hostState, loading: false };
      changed = true;
    } else {
      next[host] = hostState;
    }
  }
  return changed ? next : hosts;
}

// resetCredentialsStoreForTests resets this singleton store's state between
// tests, including the module-private wiring/debounce bookkeeping above -
// mirroring resetThreadsStoreForTests/resetTreeStoreForTests. No production
// code should ever call this.
export function resetCredentialsStoreForTests(): void {
  connectionEpoch += 1;
  hostVersions.clear();
  requestedList = false;
  unsubscribeNotifications?.();
  unsubscribeNotifications = undefined;
  wiredClient = null;
  clearTimeout(refetchTimer);
  refetchTimer = undefined;
  clearHostRefetchTimers();
  credentialsStore.setState({ ...emptyListState(), hosts: {}, loading: false, error: null });
}
