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

import type {
  AppwireClientLike,
  HostNotificationParams,
  LaunchConfigLayer,
  PathValidateResponse,
  PluginEntry,
} from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { createMarketplacesStore, type MarketplacesState } from "@evener/appwire-client/state/extensions";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { connectionStore, onConnectionNotification } from "./connection";
import { isLocalHost } from "./hostRouting";

export type { MarketplaceCatalogEntry } from "@evener/appwire-client/state/extensions";

export interface ExtensionsStoreState extends MarketplacesState {
  plugins: PluginEntry[] | null;
  pluginRevision: number;
  pluginsLoading: boolean;
  pluginsError: string | null;
  fetchPlugins(): Promise<void>;
  installPlugin(plugin: string, marketplace: string): Promise<void>;
  upgradePlugin(plugin: string, marketplace: string): Promise<void>;
  removePlugin(plugin: string, marketplace: string): Promise<void>;
  enablePlugin(plugin: string, marketplace: string): Promise<void>;
  disablePlugin(plugin: string, marketplace: string): Promise<void>;
  setPluginAutoUpgrade(plugin: string, marketplace: string, autoUpgrade: boolean): Promise<void>;

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

// The marketplaces store proper lives in the package; this is the app's one
// instance, over a client port that resolves connectionStore's CURRENT client
// at request time. It publishes into extensionsStore (below the store) so the
// sections keep reading one store; the plugins and launch-layer slices are
// still written here.
const marketplaces = createMarketplacesStore({
  request: (method, params, opts) => requireClient().request(method, params, opts),
  onNotification: onConnectionNotification,
});
marketplaces.start();

export const extensionsStore = createStore<ExtensionsStoreState>((set) => ({
  ...marketplaces.getState(),

  plugins: null,
  pluginRevision: 0,
  pluginsLoading: false,
  pluginsError: null,

  async fetchPlugins() {
    set({ pluginsLoading: true, pluginsError: null });
    try {
      const client = requireClient();
      const resp = await client.request("evener/plugin/list", {});
      set({ plugins: resp.plugins, pluginsLoading: false, pluginsError: null });
    } catch (err) {
      set({ pluginsLoading: false, pluginsError: errorText(err) });
    }
  },

  async installPlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("evener/plugin/install", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async upgradePlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("evener/plugin/upgrade", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async removePlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("evener/plugin/remove", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async enablePlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("evener/plugin/enable", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async disablePlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("evener/plugin/disable", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async setPluginAutoUpgrade(plugin, marketplace, autoUpgrade) {
    const client = requireClient();
    const resp = await client.request("evener/plugin/setAutoUpgrade", { plugin, marketplace, autoUpgrade });
    set({ plugins: resp.plugins });
  },

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

// Each marketplaces publish lands here synchronously, as the fields it
// changed: a whole-snapshot copy would make the core the owner of every
// marketplaces field and overwrite a value written straight into this store
// (the section tests seed their fixtures that way).
marketplaces.subscribe((state, previous) => {
  const changed: Partial<MarketplacesState> = {};
  for (const key of Object.keys(state) as (keyof MarketplacesState)[]) {
    if (state[key] !== previous[key]) Object.assign(changed, { [key]: state[key] });
  }
  extensionsStore.setState(changed);
});

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
// The hub BroadcastAlls these notifications to every connected client after a
// successful mutation from ANY of them - the cross-client staleness gap this
// store had until now (a change made in one browser tab never reached any
// other tab's already-loaded plugins/launchLayer until a manual re-open of
// the section). Mirrors the navigation store's identical wiring for the
// sidebar's REST-backed refetch, applied here to this store's RPC-backed
// fetches - independent debounced channels (not one shared one like
// tree.ts's) since the lists are unrelated fetches that should each coalesce
// their own bursts without waiting on each other; the marketplaces store
// runs its own channel inside the package. The notifications' generated
// payload types are empty ({}) in protocol/types.gen.ts, so there is nothing
// to apply directly and a debounced re-fetch of the affected list is the
// only option, exactly like evener/navigation/invalidated's own "just
// refetch" contract. On the wire evener/plugin/updated genuinely sends an
// empty map (notifyPluginUpdated, cmd/evener-hub/app_rpc.go:663), while
// evener/launch/updated carries {cwd, layer} (notifyLaunchUpdated,
// app_rpc.go:772-775) whose fields the generated type drops because codegen
// can't see into Go's untyped map[string]string; this refetch is
// payload-agnostic either way.
const REFETCH_DEBOUNCE_MS = 250;

let pluginRefetchTimer: ReturnType<typeof setTimeout> | undefined;
let launchLayerRefetchTimer: ReturnType<typeof setTimeout> | undefined;

function schedulePluginRefetch(): void {
  clearTimeout(pluginRefetchTimer);
  pluginRefetchTimer = setTimeout(() => {
    void extensionsStore.getState().fetchPlugins();
  }, REFETCH_DEBOUNCE_MS);
}

function scheduleLaunchLayerRefetch(): void {
  clearTimeout(launchLayerRefetchTimer);
  launchLayerRefetchTimer = setTimeout(() => {
    void extensionsStore.getState().fetchLaunchLayer();
  }, REFETCH_DEBOUNCE_MS);
}

onConnectionNotification((n) => {
  if (n.method === "evener/plugin/updated") {
    extensionsStore.setState((state) => ({ pluginRevision: state.pluginRevision + 1 }));
    schedulePluginRefetch();
  } else if (n.method === "evener/launch/updated") {
    scheduleLaunchLayerRefetch();
  } else if (n.method === "evener/host/notification") {
    handleHostNotification(n.params);
  }
});

// handleHostNotification consumes a REMOTE host's config notification, which the
// hub re-emits to this browser wrapped in evener/host/notification tagged with
// the host that owns it (cmd/evener-hub/app_host_admin.go's
// relayHostNotifications, broadcast to every client for every subscribed host,
// over the remoteHostConfigNotifications allowlist that names both
// evener/plugin/updated and evener/launch/updated).
//
// The plugin half is load-bearing for the spawn form. pluginRevision is the
// revision the HOST-SCOPED plugin consumers key their requests on:
// usePluginPreview and useSpawnSlashCatalog build their logical key from it and
// then issue their own evener/plugin/preview / evener/spawn/slashCatalog against
// the SELECTED host through evener/host/request. Without the bump a plugin
// enabled or disabled on that host is never observed, so the form keeps
// rendering the pre-change list - and a plugin reconciled from that stale
// preview is sent as a thread/start launchOverride the host no longer has. The
// bump therefore happens for a wrapped update exactly as it does for the
// controller's own (component 07b review, round three).
//
// The refetches are a different matter: evener/plugin/list and
// evener/launch/getLayer read THIS hub over the plain connection, so they stay
// gated to a notification the controller emitted itself. A remote host's change
// is not evidence about this hub's plugins, and its launch layer has no
// host-scoped consumer here at all - the only launchLayer readers are the
// controller-scoped settings sections (dirListSetting/mcp).
function handleHostNotification(n: HostNotificationParams): void {
  if (n.method === "evener/plugin/updated") {
    extensionsStore.setState((state) => ({ pluginRevision: state.pluginRevision + 1 }));
    if (isLocalHost(n.host)) schedulePluginRefetch();
  } else if (n.method === "evener/launch/updated") {
    if (isLocalHost(n.host)) scheduleLaunchLayerRefetch();
  }
}

// resetExtensionsStoreForTests resets the store to its initial state,
// including the module-private wiring/debounce bookkeeping above.
// extensions.ts is a singleton store shared by the whole app, so
// extensions.test.ts must reset it between tests to keep them isolated - no
// production code should ever call this (mirrors threads.ts/tree.ts's own
// reset*StoreForTests precedent).
export function resetExtensionsStoreForTests(): void {
  marketplaces.reset();
  clearTimeout(pluginRefetchTimer);
  pluginRefetchTimer = undefined;
  clearTimeout(launchLayerRefetchTimer);
  launchLayerRefetchTimer = undefined;
  // The marketplaces fields are written here as well as by the core's reset:
  // the mirror above forwards only what the core changed, so a value a test
  // seeded straight into this store, over a core already at its initial
  // state, would otherwise survive the reset.
  extensionsStore.setState({
    marketplaces: null,
    marketplacesLoading: false,
    marketplacesError: null,
    browseCatalogs: new Map(),
    plugins: null,
    pluginRevision: 0,
    pluginsLoading: false,
    pluginsError: null,
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
