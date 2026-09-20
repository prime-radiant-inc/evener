// The global launch-config layer store: the one LaunchConfigLayer object the
// extensions settings edit, behind the web's Plugins/Skills directories and
// MCP servers sections (its pluginDirs, skillsDirs, mcpConfigs and mcps are
// four fields of that same object). createLaunchLayerStore is a
// factory - each app builds the one instance it wires up, and tests build
// their own - returning a FrameworkFreeStore (see frameworkFreeStore.ts) whose
// state holds the store-bound actions.
//
// Deliberately NOT layer-aware: it reads and writes cwd "/" at the "global"
// layer and nothing else. Per-project and per-repo layers are the schema-driven
// launch-config editor's domain (launchConfig.ts's own getLayer/setLayer take a
// cwd and a layer name); these sections edit one machine-wide list each.
//
// Two failure conventions, shared with marketplaces.ts and plugins.ts:
//   - The READ (fetchLaunchLayer) never throws - it tracks loading/error in
//     state.
//   - The WRITE (setLaunchLayer) rejects on failure; the host decides how to
//     surface a rejection (the web toasts, from the section component).
//
// The client is a port, read at request time, so a host whose connection can
// be replaced (the web's connection store) hands in an object that resolves
// the current client on each call.

import type { AppwireClient } from "../../client";
import { errorText } from "../../errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type { LaunchConfigLayer } from "../../types.gen";
import { createListRevision, readRevisioned, writeRevisioned } from "./listRevision";
import { attachLifecycle, createStoreLifecycle, type StoreLifecycle } from "./storeLifecycle";

export type LaunchLayerClient = Pick<AppwireClient, "request" | "onNotification">;

export interface LaunchLayerState {
  launchLayer: LaunchConfigLayer | null;
  launchLayerLoading: boolean;
  /** The failed read's own text (errorText), for the host to translate at
   * render; null once a read succeeds. */
  launchLayerError: string | null;
  fetchLaunchLayer(): Promise<void>;
  setLaunchLayer(next: LaunchConfigLayer): Promise<void>;
}

export interface LaunchLayerStore
  extends FrameworkFreeStore<LaunchLayerState>,
    Omit<StoreLifecycle<LaunchLayerState>, "guard"> {
  /** Follows evener/launch/updated, which the hub broadcasts to every client
   * after any client's successful setLayer, so a change made in one window
   * reaches every other window's loaded layer after a short debounce.
   * Idempotent. */
  start(): void;
  /** Back to the initial state; requests still in flight publish nothing when
   * they land. The notification subscription, if started, stays. */
  reset(): void;
  /** Terminal: unsubscribes, cancels a pending refetch and drops every reply
   * still in flight, so subscribers hear nothing more - for a host whose
   * screen unmounts. start() refuses afterwards. */
  dispose(): void;
}

export const LAUNCH_LAYER_REFETCH_DEBOUNCE_MS = 250;

/** The scope every call is made at; see the module comment. */
const GLOBAL_LAYER_PARAMS = { cwd: "/", layer: "global" } as const;

export function createLaunchLayerStore(client: LaunchLayerClient): LaunchLayerStore {
  // Read and write both replace the whole layer from their own answer; see
  // listRevision.ts for the fence.
  const listRevision = createListRevision();

  const lifecycle = createStoreLifecycle<LaunchLayerState>(client, {
    method: "evener/launch/updated",
    debounceMs: LAUNCH_LAYER_REFETCH_DEBOUNCE_MS,
    store: () => store,
    refetch: (state) => state.fetchLaunchLayer(),
    revision: listRevision,
    // The lifecycle fences listRevision; nothing is coming to lower the flag a
    // fenced read raised.
    onFence: (set) => set({ launchLayerLoading: false }),
    // A setLaunchLayer issued before any fetchLaunchLayer call touches none of
    // these three fields; the lifecycle ORs listRevision.hasLive() in for that
    // case (see storeLifecycle.ts's revision option).
    wantsList: (s) => s.launchLayer !== null || s.launchLayerError !== null || s.launchLayerLoading,
  });

  const store = createFrameworkFreeStore<LaunchLayerState>((publish) => {
    const set = lifecycle.guard(publish);

    return {
      launchLayer: null,
      launchLayerLoading: false,
      launchLayerError: null,

      fetchLaunchLayer() {
        set({ launchLayerLoading: true, launchLayerError: null });
        // The loading flag and the error belong to this answer as much as
        // the layer does, so an outrun read writes none of the three: its
        // success would clear an error a newer read posted or hide a read
        // still running, and its failure would put "Failed to load" over a
        // newer write's layer.
        return readRevisioned(listRevision, () => client.request("evener/launch/getLayer", GLOBAL_LAYER_PARAMS), {
          onAnswer: (layer) => () => set({ launchLayer: layer, launchLayerLoading: false, launchLayerError: null }),
          onFailure: (err) => () => set({ launchLayerLoading: false, launchLayerError: errorText(err) }),
        });
      },

      setLaunchLayer: (next): Promise<void> =>
        // setLayer's response is a LaunchConfigResolved (effective + a
        // per-layer map), not the plain layer this store tracks - and
        // FromWire/ToWire (cmd/evener-hub/internal/launchconfig/wire.go) are a
        // straight field-for-field copy with no server-side normalization, so
        // `next` (what was just successfully saved) IS the new global layer.
        // Trusting our own outgoing payload avoids taking a dependency on the
        // resolved response's internal layer-name keying, which nothing here
        // otherwise needs to know.
        writeRevisioned(
          listRevision,
          () => client.request("evener/launch/setLayer", { ...GLOBAL_LAYER_PARAMS, config: next }),
          // The same three fields a read's success writes. A write that owns
          // the layer owns the error and the loading flag with it - an
          // outrun read behind it writes none of the three (see
          // plugins.ts's and marketplaces.ts's own mutate).
          () => () => set({ launchLayer: next, launchLayerLoading: false, launchLayerError: null }),
        ),
    };
  });

  return attachLifecycle(store, lifecycle);
}
