// The web's one connection store: the package's createConnectionStore with
// zustand's useStore for the reactive read. The client-swap safety and
// notification-following are the package's; serverInfo/features stay plain
// fields the web writes from AppShell's and ConnectionBanner's own handshake
// read (the one InitializeResponse), exactly as before.
import type { AnyNotification } from "@evener/appwire-client";
import {
  type ConnectionStoreState,
  onConnectionNotification as coreOnConnectionNotification,
  createConnectionStore,
} from "@evener/appwire-client/state/connection";
import { useStore } from "zustand";

export type { ConnectionStoreState };

export const connectionStore = createConnectionStore();

// Subscribes `handler` to the client this store holds now and to every client
// it wires later - a store module that loads before AppShell's own connect()
// effect has no client to read once, so it reacts to the store instead (the
// navigation store's original rationale). Thin wrapper over the package's
// onConnectionNotification, bound to the web's one store instance.
export function onConnectionNotification(handler: (n: AnyNotification) => void): () => void {
  return coreOnConnectionNotification(connectionStore, handler);
}

export function useConnectionStore(): ConnectionStoreState;
export function useConnectionStore<T>(selector: (state: ConnectionStoreState) => T): T;
export function useConnectionStore<T>(selector?: (state: ConnectionStoreState) => T): T | ConnectionStoreState {
  // Not actually a conditional hook call: zustand's own useStore is
  // `function useStore(api, selector = identity)` (node_modules/zustand/
  // esm/react.mjs) - a JS default parameter, not internal branching - so
  // both ternary arms run the exact same useSyncExternalStore/useCallback
  // sequence regardless of which one a given render takes. TypeScript's
  // overloads for useStore don't have a variant accepting a possibly-
  // undefined selector, which is the only reason this is two call sites
  // instead of one (see this same pattern + comment in stores/threads.ts,
  // stores/navigation/store.ts, shell/workspace.ts).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see above
  return selector ? useStore(connectionStore, selector) : useStore(connectionStore);
}
