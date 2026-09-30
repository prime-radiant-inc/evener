// The hub's credential statuses (evener/auth/list), one per provider instance:
// whether an account is signed in or needs signing in again, and the error the
// hub found with a credential - a provider that rejected it, or a stored
// record it could not read - in words the hub writes. A Providers surface reads
// them beside the instance listing to show a provider's status (the redesign
// spec's "Sign-in expired" and "Error", section 12).
//
// createAuthStatusesStore is a factory returning a FrameworkFreeStore (see
// frameworkFreeStore.ts) whose state holds the read. It follows
// evener/auth/updated, which the hub broadcasts after any credential change and
// whenever it records or clears a rejection, and reads the statuses again then
// and on reconnect, through the lifecycle the extensions stores share
// (storeLifecycle.ts). The read never throws; a failure keeps the statuses it
// had and records why.
//
// The client is a port, read at request time, so a host whose connection can be
// replaced hands in an object that resolves the current client on each call.

import type { AppwireClient } from "../../client";
import { errorText } from "../../errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type { AuthStatusResponse } from "../../types.gen";
import { createListRevision, readRevisioned } from "../extensions/listRevision";
import { attachLifecycle, createStoreLifecycle, type StoreLifecycle } from "../extensions/storeLifecycle";

export type AuthStatusesClient = Pick<AppwireClient, "request" | "onNotification">;

export interface AuthStatusesState {
  /** Each provider's status by name; null until a read lands, since before
   * then nothing is known (an account sign-in must not read as signed in). */
  authStatuses: ReadonlyMap<string, AuthStatusResponse> | null;
  /** The failed read's own text (errorText); null once a read succeeds. */
  authStatusesError: string | null;
  fetchAuthStatuses(): Promise<void>;
}

export interface AuthStatusesStore
  extends FrameworkFreeStore<AuthStatusesState>,
    Omit<StoreLifecycle<AuthStatusesState>, "guard"> {
  /** Follows evener/auth/updated. Idempotent. */
  start(): void;
  /** Back to the initial state; reads still in flight publish nothing. */
  reset(): void;
  /** Terminal: unsubscribes and drops every reply still in flight. */
  dispose(): void;
}

export const AUTH_STATUSES_REFETCH_DEBOUNCE_MS = 250;

export function createAuthStatusesStore(client: AuthStatusesClient): AuthStatusesStore {
  const listRevision = createListRevision();

  const lifecycle = createStoreLifecycle<AuthStatusesState>(client, {
    method: "evener/auth/updated",
    debounceMs: AUTH_STATUSES_REFETCH_DEBOUNCE_MS,
    store: () => store,
    refetch: (state) => state.fetchAuthStatuses(),
    revision: listRevision,
    // A fence is a replaced client (another hub), a reset or a dispose: the
    // statuses read so far describe a hub this store no longer speaks to, so
    // they go with the read in flight, and no error of the previous hub is
    // shown against the next one's providers, even if its read fails.
    onFence: (set) => set({ authStatuses: null, authStatusesError: null }),
    // An in-flight read counts too: the lifecycle ORs listRevision.hasLive() in.
    wantsList: (s) => s.authStatuses !== null || s.authStatusesError !== null,
  });

  const store = createFrameworkFreeStore<AuthStatusesState>((publish) => {
    const set = lifecycle.guard(publish);
    return {
      authStatuses: null,
      authStatusesError: null,

      fetchAuthStatuses() {
        set({ authStatusesError: null });
        return readRevisioned(listRevision, () => client.request("evener/auth/list", {}), {
          onAnswer: (response) => () =>
            set({
              // Go sends an empty (nil) slice as null.
              authStatuses: new Map((response.providers ?? []).map((status) => [status.provider, status])),
              authStatusesError: null,
            }),
          onFailure: (err) => () => set({ authStatusesError: errorText(err) }),
        });
      },
    };
  });

  return attachLifecycle(store, lifecycle);
}
