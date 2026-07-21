// credentials.ts is the thin wire-truth gateway for the Providers &
// credentials settings section: serf/instance/{list,create,edit,remove,
// setDefault} plus the serf/auth/* RPCs the section's OAuth/API-key/device
// flows drive. Follows stores/threads.ts's own requireClient()-via-
// connectionStore pattern (this store has no connect() of its own).
//
// Every serf/instance/* mutation's Go handler returns the FULL updated
// InstanceListResponse (appwire/types.go) - so create/edit/remove/setDefault
// apply that response directly to `instances`/`availableTypes` instead of
// issuing a separate serf/instance/list refetch, same round-trip the legacy
// credentials.html's own instanceCreate/instanceEdit/... + refresh() pattern
// achieves in two calls.
//
// Never-echo invariant: no method here stores a secret VALUE anywhere in
// this store's state - setApiKey/loginComplete/deviceStart/devicePoll return
// (and this store passes through) only AuthStatusResponse/AuthDeviceStart
// Response/AuthDevicePollResponse shapes, none of which carry the secret
// itself (write-only fields on the wire).
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type {
  AuthDevicePollResponse,
  AuthDeviceStartResponse,
  AuthLoginCompleteResponse,
  AuthLoginStartResponse,
  AuthLogoutResponse,
  AuthStatusResponse,
  InstanceCreateParams,
  InstanceEditParams,
  InstanceEntry,
  InstanceListResponse,
} from "../protocol/types.gen";
import { connectionStore } from "./connection";

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("credentials store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

export interface CredentialsStoreState {
  instances: InstanceEntry[];
  availableTypes: string[];
  loading: boolean;
  error: string | null;
  fetch(): Promise<void>;
  create(params: InstanceCreateParams): Promise<void>;
  edit(params: InstanceEditParams): Promise<void>;
  remove(name: string): Promise<void>;
  setDefault(name: string): Promise<void>;
  // Auth mutations return the raw wire response and never touch
  // instances/availableTypes themselves - the caller (CredentialsSection)
  // re-fetches on success, matching the legacy's own "close editor +
  // refresh()" sequencing, and surfaces failures as inline errors/toasts
  // itself rather than this store swallowing them into an `error` field.
  setApiKey(provider: string, value: string): Promise<AuthStatusResponse>;
  logout(provider: string): Promise<AuthLogoutResponse>;
  loginStart(provider: string): Promise<AuthLoginStartResponse>;
  loginComplete(provider: string, flowId: string, redirectUrl: string): Promise<AuthLoginCompleteResponse>;
  deviceStart(provider: string): Promise<AuthDeviceStartResponse>;
  devicePoll(provider: string, flowId: string): Promise<AuthDevicePollResponse>;
}

function applyList(resp: InstanceListResponse): void {
  credentialsStore.setState({ instances: resp.instances, availableTypes: resp.availableTypes });
}

export const credentialsStore = createStore<CredentialsStoreState>((set) => ({
  instances: [],
  availableTypes: [],
  loading: false,
  error: null,

  async fetch() {
    const client = requireClient();
    set({ loading: true, error: null });
    try {
      const resp = await client.request("serf/instance/list", {});
      set({ instances: resp.instances, availableTypes: resp.availableTypes, loading: false });
    } catch (err) {
      set({ loading: false, error: errorMessage(err) });
    }
  },

  async create(params) {
    const client = requireClient();
    applyList(await client.request("serf/instance/create", params));
  },

  async edit(params) {
    const client = requireClient();
    applyList(await client.request("serf/instance/edit", params));
  },

  async remove(name) {
    const client = requireClient();
    applyList(await client.request("serf/instance/remove", { name }));
  },

  async setDefault(name) {
    const client = requireClient();
    applyList(await client.request("serf/instance/setDefault", { name }));
  },

  async setApiKey(provider, value) {
    const client = requireClient();
    return client.request("serf/auth/apiKey/set", { provider, value });
  },

  async logout(provider) {
    const client = requireClient();
    return client.request("serf/auth/logout", { provider });
  },

  async loginStart(provider) {
    const client = requireClient();
    return client.request("serf/auth/login/start", { provider });
  },

  async loginComplete(provider, flowId, redirectUrl) {
    const client = requireClient();
    return client.request("serf/auth/login/complete", { provider, flowId, redirectUrl });
  },

  async deviceStart(provider) {
    const client = requireClient();
    return client.request("serf/auth/device/start", { provider });
  },

  async devicePoll(provider, flowId) {
    const client = requireClient();
    return client.request("serf/auth/device/poll", { provider, flowId });
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

// resetCredentialsStoreForTests resets this singleton store's state between
// tests, mirroring resetThreadsStoreForTests/resetTreeStoreForTests. No
// production code should ever call this.
export function resetCredentialsStoreForTests(): void {
  credentialsStore.setState({ instances: [], availableTypes: [], loading: false, error: null });
}
