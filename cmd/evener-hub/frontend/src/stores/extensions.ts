// extensions.ts is the thin wire-truth store behind the Extensions settings
// cluster (Marketplaces & Plugins, Plugins/Skills directories, MCP servers -
// panes/settings/sections/{marketplacesPlugins,dirListSetting,pluginsDirs,
// skillsDirs,mcp}). It rides the single AppwireClientLike connection.ts wires
// via useConnectionStore.getState().connect(client), same as threads.ts/
// tree.ts - this store has no connect() of its own.
//
// Split, deliberately, into two halves with different failure conventions,
// mirroring the legacy JS's own two conventions (plugins-manager.html toasts
// on every mutation failure; plugins.html/skills.html/mcp.html show add-time
// validation failures inline instead):
//   - FETCHES (fetchMarketplaces/fetchPlugins/fetchLaunchLayer) never throw -
//     they track loading/error in state, exactly like tree.ts's refresh().
//   - MUTATIONS (add/remove/refresh/install/upgrade/.../setLaunchLayer)
//     reject on failure, exactly like threads.ts's send/steer/queue/
//     interrupt - this store is a plain module, not a hook, so it cannot
//     call useToasts() itself; the section components (which DO run inside
//     React) catch the rejection and toast, per the app's toast-on-failure
//     convention.

import type { AppwireClientLike, LaunchConfigLayer, PathValidateResponse } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import {
  createMarketplacesStore,
  createPluginsStore,
  type MarketplacesClient,
  type MarketplacesState,
  type PluginsClient,
  type PluginsState,
} from "@evener/appwire-client/state/extensions";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { connectionStore, onConnectionNotification } from "./connection";

export type { MarketplaceCatalogEntry } from "@evener/appwire-client/state/extensions";

export interface ExtensionsStoreState extends MarketplacesState, PluginsState {
  // The global launch-config layer - backs Plugins/Skills directories and
  // MCP's editable config-files/inline-servers lists (all four are fields
  // on this same object). Deliberately NOT layer-aware (always cwd:"/",
  // layer:"global") - matches every one of §§13-15's legacy partials, none
  // of which is layer-parameterized either (Appendix B's schema-driven
  // engine is the one that supports project-layer editing, and it's T2's
  // Evener-launch/Per-project domain, not this store's).
  launchLayer: LaunchConfigLayer | null;
  launchLayerLoading: boolean;
  launchLayerError: string | null;
  fetchLaunchLayer(): Promise<void>;
  setLaunchLayer(next: LaunchConfigLayer): Promise<void>;

  validatePath(path: string, kind: string): Promise<PathValidateResponse>;
  createDirectory(path: string): Promise<void>;
  // Backs PathField. The prefix passes through verbatim, because the widget
  // picks which of the RPC's two duties it wants per keystroke: a trailing
  // slash lists a directory's children, a bare prefix fuzzy-completes it
  // (TestHubRPCPathsCompleteReturnsMatchingDirectories). includeFiles adds
  // files to the dirs-only default, and then directory entries come back with
  // a trailing slash.
  completePaths(prefix: string, includeFiles: boolean): Promise<string[]>;
}

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("extensions store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

const GLOBAL_LAYER_PARAMS = { cwd: "/", layer: "global" } as const;

// The marketplaces and installed-plugins stores proper live in the package;
// these are the app's one instance of each, over a client port that resolves
// connectionStore's CURRENT client at request time. They publish into
// extensionsStore (below the store) so the sections keep reading one store;
// the launch-layer slice is still written here.
const hubClient = {
  request: (method, params, opts) => requireClient().request(method, params, opts),
  onNotification: onConnectionNotification,
} satisfies MarketplacesClient & PluginsClient;
const marketplaces = createMarketplacesStore(hubClient);
marketplaces.start();
const plugins = createPluginsStore(hubClient);
plugins.start();

export const extensionsStore = createStore<ExtensionsStoreState>((set) => ({
  ...marketplaces.getState(),
  ...plugins.getState(),

  launchLayer: null,
  launchLayerLoading: false,
  launchLayerError: null,

  async fetchLaunchLayer() {
    set({ launchLayerLoading: true, launchLayerError: null });
    try {
      const client = requireClient();
      const layer = await client.request("evener/launch/getLayer", GLOBAL_LAYER_PARAMS);
      set({ launchLayer: layer, launchLayerLoading: false, launchLayerError: null });
    } catch (err) {
      set({ launchLayerLoading: false, launchLayerError: errorText(err) });
    }
  },

  async setLaunchLayer(next) {
    const client = requireClient();
    // setLayer's response is a LaunchConfigResolved (effective + a
    // per-layer map), not the plain layer this store tracks - and
    // FromWire/ToWire (cmd/evener-hub/internal/launchconfig/wire.go) are a
    // straight field-for-field copy with no server-side normalization, so
    // `next` (what was just successfully saved) IS the new global layer.
    // Trusting our own outgoing payload avoids taking a dependency on the
    // resolved response's internal layer-name keying, which nothing in
    // this store otherwise needs to know.
    await client.request("evener/launch/setLayer", { ...GLOBAL_LAYER_PARAMS, config: next });
    set({ launchLayer: next });
  },

  async validatePath(path, kind) {
    const client = requireClient();
    return client.request("evener/path/validate", { path, kind });
  },

  async createDirectory(path) {
    await requireClient().request("evener/dirs/create", { path });
  },

  async completePaths(prefix, includeFiles) {
    const client = requireClient();
    const resp = await client.request("evener/paths/complete", { prefix, includeFiles });
    // Defence in depth against a null `data`. The hub sends [] and the wire type
    // says string[], but a null here would reach every PathField on the page and
    // a form must not come down over an empty directory listing.
    return resp.data ?? [];
  },
}));

// Each core's publish lands here synchronously, as the fields it changed: a
// whole-snapshot copy would make the core the owner of every one of its
// fields and overwrite a value written straight into this store (the section
// tests seed their fixtures that way).
function publishChangedFields<S extends Partial<ExtensionsStoreState>>(core: {
  subscribe(listener: (state: S, previous: S) => void): () => void;
}): void {
  core.subscribe((state, previous) => {
    const changed: Partial<ExtensionsStoreState> = {};
    for (const key of Object.keys(state) as (keyof S & keyof ExtensionsStoreState)[]) {
      if (state[key] !== previous[key]) Object.assign(changed, { [key]: state[key] });
    }
    extensionsStore.setState(changed);
  });
}
publishChangedFields(marketplaces);
publishChangedFields(plugins);

export function useExtensionsStore(): ExtensionsStoreState;
export function useExtensionsStore<T>(selector: (state: ExtensionsStoreState) => T): T;
export function useExtensionsStore<T>(selector?: (state: ExtensionsStoreState) => T): T | ExtensionsStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(extensionsStore, selector) : useStore(extensionsStore);
}

// --- notification-triggered refetch --------------------------------------
//
// The hub BroadcastAlls evener/launch/updated to every connected client after
// any client's successful setLayer, so a change made in one browser tab
// reaches every other tab's loaded launchLayer. Its own debounced channel,
// like the ones the marketplaces and plugins stores run inside the package:
// the three lists are unrelated fetches that should each coalesce their own
// bursts. On the wire the notification carries {cwd, layer}
// (notifyLaunchUpdated, app_rpc.go) whose fields the generated type drops
// because codegen can't see into Go's untyped map[string]string; this refetch
// is payload-agnostic either way.
const REFETCH_DEBOUNCE_MS = 250;

let launchLayerRefetchTimer: ReturnType<typeof setTimeout> | undefined;

function scheduleLaunchLayerRefetch(): void {
  clearTimeout(launchLayerRefetchTimer);
  launchLayerRefetchTimer = setTimeout(() => {
    void extensionsStore.getState().fetchLaunchLayer();
  }, REFETCH_DEBOUNCE_MS);
}

onConnectionNotification((n) => {
  if (n.method === "evener/launch/updated") scheduleLaunchLayerRefetch();
});

// resetExtensionsStoreForTests resets the store to its initial state,
// including the module-private wiring/debounce bookkeeping above.
// extensions.ts is a singleton store shared by the whole app, so
// extensions.test.ts must reset it between tests to keep them isolated - no
// production code should ever call this (mirrors threads.ts/tree.ts's own
// reset*StoreForTests precedent).
export function resetExtensionsStoreForTests(): void {
  marketplaces.reset();
  plugins.reset();
  clearTimeout(launchLayerRefetchTimer);
  launchLayerRefetchTimer = undefined;
  // The cores' fields are written here as well as by their resets: the mirror
  // above forwards only what a core changed, so a value a test seeded
  // straight into this store, over a core already at its initial state,
  // would otherwise survive the reset.
  extensionsStore.setState({
    ...marketplaces.getInitialState(),
    ...plugins.getInitialState(),
    launchLayer: null,
    launchLayerLoading: false,
    launchLayerError: null,
  });
}

/** Directory actions shared by settings fields; the widget stays wire-free. */
export const directoryActions = {
  validatePath: (path: string, kind: string) => extensionsStore.getState().validatePath(path, kind),
  createDirectory: (path: string) => extensionsStore.getState().createDirectory(path),
};
