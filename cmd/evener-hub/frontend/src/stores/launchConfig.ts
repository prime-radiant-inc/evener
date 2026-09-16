// launchConfig.ts is the web's one instance of the package's launch-config
// gateway (createLaunchConfigStore in @evener/appwire-client), bound to
// whichever client connectionStore currently holds. Follows stores/threads.ts's
// own requireClient()-via-connectionStore pattern (no connect() of its own):
// the client is resolved on every call rather than captured at module load,
// so a call before connect() fails loudly and a reconnect is picked up
// without rebuilding the store - which is also what keeps the schema cache
// alive across the session, since the schema is server-global.

import {
  type AppwireClientLike,
  createLaunchConfigStore,
  type LaunchConfigClient,
  type LaunchConfigStoreState,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { connectionStore } from "./connection";

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error(
      "launchConfig store: no client connected; call useConnectionStore.getState().connect(client) first",
    );
  }
  return client;
}

const connectedClient: LaunchConfigClient = {
  request: (method, params, opts) => requireClient().request(method, params, opts),
  onNotification: (cb) => requireClient().onNotification(cb),
};

export type { LaunchConfigStoreState };

export const launchConfigStore = createLaunchConfigStore(connectedClient);

export function useLaunchConfigStore(): LaunchConfigStoreState;
export function useLaunchConfigStore<T>(selector: (state: LaunchConfigStoreState) => T): T;
export function useLaunchConfigStore<T>(selector?: (state: LaunchConfigStoreState) => T): T | LaunchConfigStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(launchConfigStore, selector) : useStore(launchConfigStore);
}

// resetLaunchConfigStoreForTests clears the schema cache between tests - this
// store's own reactive state has nothing to reset (it holds only methods), but
// the cache is a singleton that would otherwise leak a fixture from one test
// into the next. No production code should ever call this.
export function resetLaunchConfigStoreForTests(): void {
  launchConfigStore.getState().invalidateSchema();
}
