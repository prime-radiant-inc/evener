// The web's store of the hub's credential statuses (evener/auth/list): the
// package's framework-free core (state/credentials/authStatuses.ts) over this
// window's connection. The Providers & credentials section reads it beside the
// instance listing to show a provider whose credential the hub found an error
// with (#3539). It follows evener/auth/updated and re-reads on reconnect once
// something has read it.
import { type AuthStatusesState, createAuthStatusesStore } from "@evener/appwire-client/state/credentials";
import { useStore } from "zustand";
import {
  type ConnectionStoreState,
  connectedClientPort,
  connectionStore,
  onConnectionNotification,
} from "./connection";

const { request } = connectedClientPort("auth statuses");

// The notification side is a port that follows the connection (the lifecycle's
// "port host", storeLifecycle.ts), so a replaced client's frames never reach
// the store.
export const authStatusesStore = createAuthStatusesStore({ request, onNotification: onConnectionNotification });
authStatusesStore.start();

function syncConnection(state: Pick<ConnectionStoreState, "client" | "state">): void {
  authStatusesStore.connectionChanged(state.client, state.state);
}
connectionStore.subscribe(syncConnection);
syncConnection(connectionStore.getState());

export function useAuthStatusesStore<T>(selector: (state: AuthStatusesState) => T): T {
  return useStore(authStatusesStore, selector);
}

export function resetAuthStatusesStoreForTests(): void {
  authStatusesStore.reset();
}
