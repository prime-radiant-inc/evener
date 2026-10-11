// hostStore.tsx is the React seam the Marketplaces & Plugins subtree reads its
// host-scoped extensions store through (component 07b). The subtree's
// components were written against the module singleton (stores/extensions.ts);
// this context hands them the SELECTED host's instance instead - the
// controller's own store for the local hub, a per-host instance over
// evener/host/request for a remote one - so a remote host's marketplaces and
// plugins are both shown and changed from here, and never this hub's.
//
// The context's default IS the singleton, so a component rendered on its own
// (a section test) reads exactly today's local store; only
// MarketplacesPluginsSection provides a host-scoped one.
import { createContext, type ReactNode, useContext } from "react";
import { useStore } from "zustand";
import type { StoreApi } from "zustand/vanilla";
import { type ExtensionsStoreState, extensionsStore, extensionsStoreForHost } from "../../../../stores/extensions";
import type { DirectoryActions } from "../../../../widgets/pathfield";

const ExtensionsStoreContext = createContext<StoreApi<ExtensionsStoreState>>(extensionsStore);

export function ExtensionsStoreProvider({ host, children }: { host: string; children: ReactNode }) {
  return (
    <ExtensionsStoreContext.Provider value={extensionsStoreForHost(host)}>{children}</ExtensionsStoreContext.Provider>
  );
}

/** The selected host's extensions store, for the subtree's imperative calls. */
export function useExtensionsHostStore(): StoreApi<ExtensionsStoreState> {
  return useContext(ExtensionsStoreContext);
}

/** The selected host's extensions state, for the subtree's reactive reads. */
export function useExtensionsHostState(): ExtensionsStoreState;
export function useExtensionsHostState<T>(selector: (state: ExtensionsStoreState) => T): T;
export function useExtensionsHostState<T>(selector?: (state: ExtensionsStoreState) => T): T | ExtensionsStoreState {
  const store = useContext(ExtensionsStoreContext);
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(store, selector) : useStore(store);
}

/** The selected host's path-validation/creation actions, bound to that host's
 * launch-config gateway (the controller's own for the local hub). */
export function useHostDirectoryActions(): DirectoryActions {
  const store = useExtensionsHostStore();
  return {
    validatePath: (path, kind) => store.getState().validatePath(path, kind),
    createDirectory: (path) => store.getState().createDirectory(path),
  };
}
