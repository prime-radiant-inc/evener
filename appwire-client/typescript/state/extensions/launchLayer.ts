// The global launch-config layer store: the one LaunchConfigLayer object the
// extensions settings edit, behind the web's Plugins/Skills directories and
// MCP servers sections (its pluginDirs, skillsDirs, mcpConfigFiles and
// mcpServers are four fields of that same object). createLaunchLayerStore is a
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
import { createListRevision } from "./listRevision";
import { createStoreLifecycle, type StoreLifecycle } from "./storeLifecycle";

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
    onFence: (set) => {
      listRevision.fence();
      // Nothing is coming to lower it.
      set({ launchLayerLoading: false });
    },
    // A setLaunchLayer issued before any fetchLaunchLayer call touches none
    // of these three fields, so listRevision.hasLive() is what carries its
    // intent - a write issues the same live revision a read does (see
    // listRevision.ts and marketplaces.ts/plugins.ts's own wantsList).
    wantsList: (s) =>
      s.launchLayer !== null || s.launchLayerError !== null || s.launchLayerLoading || listRevision.hasLive(),
  });

  const store = createFrameworkFreeStore<LaunchLayerState>((publish) => {
    const set = lifecycle.guard(publish);

    return {
      launchLayer: null,
      launchLayerLoading: false,
      launchLayerError: null,

      async fetchLaunchLayer() {
        const revision = listRevision.next();
        set({ launchLayerLoading: true, launchLayerError: null });
        try {
          const layer = await client.request("evener/launch/getLayer", GLOBAL_LAYER_PARAMS);
          // The loading flag and the error belong to this answer as much as
          // the layer does, so an outrun read writes none of the three: its
          // success would clear an error a newer read posted or hide a read
          // still running, and its failure would put "Failed to load" over a
          // newer write's layer.
          listRevision.publish(revision, () =>
            set({ launchLayer: layer, launchLayerLoading: false, launchLayerError: null }),
          );
        } catch (err) {
          listRevision.publish(revision, () => set({ launchLayerLoading: false, launchLayerError: errorText(err) }));
        }
      },

      async setLaunchLayer(next) {
        const revision = listRevision.next();
        // setLayer's response is a LaunchConfigResolved (effective + a
        // per-layer map), not the plain layer this store tracks - and
        // FromWire/ToWire (cmd/evener-hub/internal/launchconfig/wire.go) are a
        // straight field-for-field copy with no server-side normalization, so
        // `next` (what was just successfully saved) IS the new global layer.
        // Trusting our own outgoing payload avoids taking a dependency on the
        // resolved response's internal layer-name keying, which nothing here
        // otherwise needs to know.
        try {
          await client.request("evener/launch/setLayer", { ...GLOBAL_LAYER_PARAMS, config: next });
        } catch (err) {
          // Nothing to publish, so nothing to own: see listRevision's retract.
          listRevision.retract(revision);
          throw err;
        }
        listRevision.publish(revision, () => set({ launchLayer: next }));
      },
    };
  });

  const { start, connectionChanged, reset, dispose } = lifecycle;
  return { ...store, start, connectionChanged, reset, dispose };
}
