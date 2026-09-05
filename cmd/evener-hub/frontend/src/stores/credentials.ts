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
import { errorText } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
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
  fetch(): Promise<void>;
  create(params: InstanceCreateParams): Promise<void>;
  edit(params: InstanceEditParams): Promise<void>;
  remove(name: string): Promise<void>;
  setDefault(name: string): Promise<void>;
  // Auth mutations return the raw wire response and never touch
  // instances/availableProviders themselves - the caller (CredentialsSection)
  // re-fetches on success, matching the legacy's own "close editor +
  // refresh()" sequencing, and surfaces failures as inline errors/toasts
  // itself rather than this store swallowing them into an `error` field.
  setApiKey(provider: string, value: string): Promise<AuthStatusResponse>;
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
// can replace the listing, even when responses arrive out of order.
async function applyMutation(request: () => Promise<InstanceListResponse>): Promise<void> {
  const version = ++requestVersion;
  try {
    const response = await request();
    if (version === requestVersion) {
      credentialsStore.setState({ ...listState(response), loading: false, error: null });
    }
  } finally {
    if (version === requestVersion) credentialsStore.setState({ loading: false });
  }
}

export const credentialsStore = createStore<CredentialsStoreState>((set) => ({
  ...emptyListState(),
  loading: false,
  error: null,

  async fetch() {
    const client = requireClient();
    requestedList = true;
    const version = ++requestVersion;
    set({ loading: true, error: null });
    try {
      const resp = await client.request("evener/instance/list", {});
      if (version !== requestVersion || connectionStore.getState().client !== client) return;
      set({ ...listState(resp), loading: false });
    } catch (err) {
      if (version !== requestVersion || connectionStore.getState().client !== client) return;
      set({ loading: false, error: errorText(err) });
    }
  },

  async create(params) {
    const client = requireClient();
    await applyMutation(() => client.request("evener/instance/create", params));
  },

  async edit(params) {
    const client = requireClient();
    await applyMutation(() => client.request("evener/instance/edit", params));
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
const REFETCH_DEBOUNCE_MS = 250;

let wiredClient: AppwireClientLike | null = null;
let unsubscribeNotifications: (() => void) | undefined;
let refetchTimer: ReturnType<typeof setTimeout> | undefined;

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

function handleNotification(n: AnyNotification): void {
  if (n.method === "evener/auth/updated") scheduleRefetch();
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
  unsubscribeNotifications?.();
  unsubscribeNotifications = undefined;
  wiredClient = null;
  clearTimeout(refetchTimer);
  refetchTimer = undefined;
  credentialsStore.setState({ ...emptyListState(), loading: false, error: null });
}
