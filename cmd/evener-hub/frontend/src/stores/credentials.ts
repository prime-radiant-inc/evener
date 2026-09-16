// credentials.ts is the web's Providers & credentials store: the package's
// credential instances store core (@evener/appwire-client/state/credentials)
// wired to connectionStore and stamped with this page's mutation identity.
// Follows stores/threads.ts's own connectionStore pattern (this store has no
// connect() of its own): the connection subscription below is what tells the
// core which client the rows belong to, and the core listens for
// evener/auth/updated on that client itself.

import {
  type CredentialInstancesState,
  createCredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { useStore } from "zustand";
import { type ConnectionStoreState, connectionStore } from "./connection";
import { ownClientId } from "./mutationClientIdentity";

export { isStaleListingRefusal, StaleListingRefusal, staleListingHeld } from "@evener/appwire-client/state/credentials";

export type CredentialsStoreState = CredentialInstancesState;

// The hub echoes ownClientId into the evener/auth/updated broadcast, so this
// page attributes its own echo by identity rather than provider plus timing.
export const credentialsStore = createCredentialInstancesStore({ ownClientId });

export function useCredentialsStore(): CredentialsStoreState;
export function useCredentialsStore<T>(selector: (state: CredentialsStoreState) => T): T;
export function useCredentialsStore<T>(selector?: (state: CredentialsStoreState) => T): T | CredentialsStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(credentialsStore, selector) : useStore(credentialsStore);
}

// Watches connectionStore for the client becoming available and hands every
// client or connection-state transition to the core - see stores/extensions.ts's
// identical wiring for the full "why react to the store instead of reading it
// once" rationale (a mount-order race between this module and AppShell's own
// connect() effect).
function syncConnection(state: Pick<ConnectionStoreState, "client" | "state">): void {
  credentialsStore.connectionChanged(state.client, state.state);
}

connectionStore.subscribe(syncConnection);
syncConnection(connectionStore.getState());

// resetCredentialsStoreForTests resets this singleton store between tests,
// mirroring resetThreadsStoreForTests/resetTreeStoreForTests. No production
// code should ever call this.
export function resetCredentialsStoreForTests(): void {
  credentialsStore.resetForTests();
}
