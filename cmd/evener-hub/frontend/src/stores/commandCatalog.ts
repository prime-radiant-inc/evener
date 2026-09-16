// The app's one command catalog: the package's hub-wide catalog store bound to
// zustand's useStore for the reactive read. The palette loads it lazily on
// open (paletteController) and the composer's slash menu reads it; a plugin
// change on the hub re-reads it. The store is created once and outlives any
// connection: its client port forwards each request to whichever client the
// connection store holds now, and follows that store so the plugin-change
// subscription moves to a replacement client. Each consumer scopes the catalog
// to a session itself, from the diagnostics the threads store already holds.
import { type CommandCatalogClient, type CommandCatalogState, createCommandCatalog } from "@evener/appwire-client";
import { useStore } from "zustand";
import { connectionStore } from "./connection";

const connectionClient: CommandCatalogClient = {
  request: (method, params, opts) => {
    const client = connectionStore.getState().client;
    if (!client) return Promise.reject(new Error("Not connected to the hub"));
    return client.request(method, params, opts);
  },
  onNotification: (handler) => {
    let unwire = connectionStore.getState().client?.onNotification(handler);
    const stop = connectionStore.subscribe((state, previous) => {
      if (state.client === previous.client) return;
      unwire?.();
      unwire = state.client?.onNotification(handler);
    });
    return () => {
      stop();
      unwire?.();
    };
  },
};

const store = createCommandCatalog(connectionClient);
store.watch();

function useCommandCatalogState<T>(selector: (state: CommandCatalogState) => T): T {
  return useStore(store, selector);
}

export const useCommandCatalog = Object.assign(useCommandCatalogState, store);
