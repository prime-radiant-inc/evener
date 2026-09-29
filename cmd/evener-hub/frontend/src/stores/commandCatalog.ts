// The app's one command catalog: the package's hub-wide catalog store bound to
// zustand's useStore for the reactive read. Every ready, client-wired
// connection loads it (the subscription below); the palette re-reads it on
// open (paletteController), the composer's slash menu reads it, and a plugin
// change on the hub re-reads it. The store is created once and outlives any
// connection: its client port forwards each request to whichever client the
// connection store holds now, and follows that store so the plugin-change
// subscription moves to a replacement client. Each consumer scopes the catalog
// to a session itself, from the diagnostics the threads store already holds.
import { type CommandCatalogClient, type CommandCatalogState, createCommandCatalog } from "@evener/appwire-client";
import { useStore } from "zustand";
import { connectionStore, readyConnectionTransition } from "./connection";

const connectionClient: CommandCatalogClient = {
  request: (method, params, opts) => {
    const client = connectionStore.getState().client;
    if (!client) return Promise.reject(new Error("Not connected to the hub"));
    return client.request(method, params, opts).then(
      (response) => {
        // A response whose client the connection has since left describes THAT
        // connection's catalog, not this one's. The loop's dirty coalescing
        // already supersedes an in-flight read when a re-read is triggered, but
        // a swap to a not-yet-ready client triggers none, so a late response
        // would publish over the replacement connection here. Answer it with
        // the catalog the store already holds instead: the supersede stays
        // silent (no technical error flash for an expected internal
        // cancellation), the last catalog is kept, and the new connection's
        // own ready-transition load replaces it.
        if (connectionStore.getState().client !== client) {
          return { ...response, commands: store.getState().commands };
        }
        return response;
      },
      (error) => {
        // The rejection arm of the guard above: a request the old client
        // failed while closing is the same expected internal cancellation -
        // only a failure on the CURRENT connection is a real read failure
        // worth the error state.
        if (connectionStore.getState().client !== client) {
          return { commands: store.getState().commands };
        }
        throw error;
      },
    );
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

// The catalog's "read once per connection" half: the palette's lazy open and
// evener/plugin/updated only ever re-read a catalog someone loaded first, so
// without this a fresh browser's composer slash menu held no plugin or user
// commands until the palette had been opened once on a session page. Every
// transition into a ready, client-wired connection - first connect, client
// replacement, recovery after a gap whose plugin changes went unheard - is a
// connection whose catalog this store has not read, so it reads now.
// onConnectionReplacedOrRecovered (connection.ts) is NOT the predicate here:
// it deliberately skips the first connect ("nothing was missed"), and the
// first connect is exactly the case this load exists for.
connectionStore.subscribe((state, previous) => {
  if (readyConnectionTransition(state, previous)) void store.getState().refresh();
});

function useCommandCatalogState<T>(selector: (state: CommandCatalogState) => T): T {
  return useStore(store, selector);
}

export const useCommandCatalog = Object.assign(useCommandCatalogState, store);
