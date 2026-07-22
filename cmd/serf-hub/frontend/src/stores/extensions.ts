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
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type {
  AnyNotification,
  LaunchConfigLayer,
  MarketplaceAddParams,
  MarketplaceCatalogPlugin,
  MarketplaceEntry,
  PathValidateResponse,
  PluginEntry,
} from "../protocol/types.gen";
import { connectionStore } from "./connection";

// One cached browse result per marketplace name - permanent until an
// explicit refreshMarketplace/removeMarketplace invalidates it, exactly
// like the legacy plugins-manager.html's own browseCatalogs cache
// ("re-expanding never re-fetches"). "loading" is written synchronously
// BEFORE the request is sent (see browseMarketplace below), the same
// synchronous-marker trick the legacy's toggleMarketplaceExpanded uses to
// keep a concurrent second call a no-op without needing a separate
// in-flight-promise map.
export type MarketplaceCatalogEntry =
  | { status: "loading" }
  | { status: "loaded"; description?: string; plugins: MarketplaceCatalogPlugin[] }
  | { status: "error"; error: string };

export interface ExtensionsStoreState {
  marketplaces: MarketplaceEntry[] | null;
  marketplacesLoading: boolean;
  marketplacesError: string | null;
  fetchMarketplaces(): Promise<void>;
  addMarketplace(params: MarketplaceAddParams): Promise<void>;
  removeMarketplace(name: string): Promise<void>;
  refreshMarketplace(name: string): Promise<void>;

  browseCatalogs: Map<string, MarketplaceCatalogEntry>;
  browseMarketplace(name: string): Promise<void>;

  plugins: PluginEntry[] | null;
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
  // Serf-launch/Per-project domain, not this store's).
  launchLayer: LaunchConfigLayer | null;
  launchLayerLoading: boolean;
  launchLayerError: string | null;
  fetchLaunchLayer(): Promise<void>;
  setLaunchLayer(next: LaunchConfigLayer): Promise<void>;

  validatePath(path: string, kind: string): Promise<PathValidateResponse>;
  // Backs PathPicker's listChildren prop. serf/dirs/complete does double
  // duty as both "fuzzy-complete a typed prefix" and "list every child of a
  // directory" depending on whether the given prefix ends in "/"
  // (TestHubRPCDirsCompleteReturnsMatchingDirectories) - PathPicker always
  // wants the latter (it does its own client-side prefix filtering), so
  // this always normalizes to a trailing slash before asking.
  listDirChildren(path: string): Promise<string[]>;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("extensions store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

const GLOBAL_LAYER_PARAMS = { cwd: "/", layer: "global" } as const;

export const extensionsStore = createStore<ExtensionsStoreState>((set, get) => ({
  marketplaces: null,
  marketplacesLoading: false,
  marketplacesError: null,

  async fetchMarketplaces() {
    set({ marketplacesLoading: true, marketplacesError: null });
    try {
      const client = requireClient();
      const resp = await client.request("serf/marketplace/list", {});
      set({ marketplaces: resp.marketplaces, marketplacesLoading: false, marketplacesError: null });
    } catch (err) {
      set({ marketplacesLoading: false, marketplacesError: errorMessage(err) });
    }
  },

  async addMarketplace(params) {
    const client = requireClient();
    const resp = await client.request("serf/marketplace/add", params);
    set({ marketplaces: resp.marketplaces });
  },

  async removeMarketplace(name) {
    const client = requireClient();
    const resp = await client.request("serf/marketplace/remove", { name });
    set((s) => {
      const nextCatalogs = new Map(s.browseCatalogs);
      nextCatalogs.delete(name);
      return { marketplaces: resp.marketplaces, browseCatalogs: nextCatalogs };
    });
  },

  async refreshMarketplace(name) {
    const client = requireClient();
    const resp = await client.request("serf/marketplace/refresh", { name });
    set((s) => {
      const nextCatalogs = new Map(s.browseCatalogs);
      nextCatalogs.delete(name);
      return { marketplaces: resp.marketplaces, browseCatalogs: nextCatalogs };
    });
  },

  browseCatalogs: new Map(),

  async browseMarketplace(name) {
    if (get().browseCatalogs.has(name)) return; // loaded, errored, or already in flight - see MarketplaceCatalogEntry's own doc comment
    const client = requireClient();
    set((s) => {
      const next = new Map(s.browseCatalogs);
      next.set(name, { status: "loading" });
      return { browseCatalogs: next };
    });
    try {
      const resp = await client.request("serf/marketplace/browse", { name });
      set((s) => {
        const next = new Map(s.browseCatalogs);
        next.set(name, { status: "loaded", description: resp.description, plugins: resp.plugins });
        return { browseCatalogs: next };
      });
    } catch (err) {
      set((s) => {
        const next = new Map(s.browseCatalogs);
        next.set(name, { status: "error", error: errorMessage(err) });
        return { browseCatalogs: next };
      });
    }
  },

  plugins: null,
  pluginsLoading: false,
  pluginsError: null,

  async fetchPlugins() {
    set({ pluginsLoading: true, pluginsError: null });
    try {
      const client = requireClient();
      const resp = await client.request("serf/plugin/list", {});
      set({ plugins: resp.plugins, pluginsLoading: false, pluginsError: null });
    } catch (err) {
      set({ pluginsLoading: false, pluginsError: errorMessage(err) });
    }
  },

  async installPlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("serf/plugin/install", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async upgradePlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("serf/plugin/upgrade", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async removePlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("serf/plugin/remove", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async enablePlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("serf/plugin/enable", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async disablePlugin(plugin, marketplace) {
    const client = requireClient();
    const resp = await client.request("serf/plugin/disable", { plugin, marketplace });
    set({ plugins: resp.plugins });
  },

  async setPluginAutoUpgrade(plugin, marketplace, autoUpgrade) {
    const client = requireClient();
    const resp = await client.request("serf/plugin/setAutoUpgrade", { plugin, marketplace, autoUpgrade });
    set({ plugins: resp.plugins });
  },

  launchLayer: null,
  launchLayerLoading: false,
  launchLayerError: null,

  async fetchLaunchLayer() {
    set({ launchLayerLoading: true, launchLayerError: null });
    try {
      const client = requireClient();
      const layer = await client.request("serf/launch/getLayer", GLOBAL_LAYER_PARAMS);
      set({ launchLayer: layer, launchLayerLoading: false, launchLayerError: null });
    } catch (err) {
      set({ launchLayerLoading: false, launchLayerError: errorMessage(err) });
    }
  },

  async setLaunchLayer(next) {
    const client = requireClient();
    // setLayer's response is a LaunchConfigResolved (effective + a
    // per-layer map), not the plain layer this store tracks - and
    // FromWire/ToWire (cmd/serf-hub/internal/launchconfig/wire.go) are a
    // straight field-for-field copy with no server-side normalization, so
    // `next` (what was just successfully saved) IS the new global layer.
    // Trusting our own outgoing payload avoids taking a dependency on the
    // resolved response's internal layer-name keying, which nothing in
    // this store otherwise needs to know.
    await client.request("serf/launch/setLayer", { ...GLOBAL_LAYER_PARAMS, config: next });
    set({ launchLayer: next });
  },

  async validatePath(path, kind) {
    const client = requireClient();
    return client.request("serf/path/validate", { path, kind });
  },

  async listDirChildren(path) {
    const client = requireClient();
    const prefix = path.endsWith("/") ? path : `${path}/`;
    const resp = await client.request("serf/dirs/complete", { prefix });
    return resp.data;
  },
}));

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
// The hub BroadcastAlls these three notifications to every connected client
// after a successful mutation from ANY of them - the cross-client staleness
// gap this store had until now (a change made in one browser tab never
// reached any other tab's already-loaded marketplaces/plugins/launchLayer
// until a manual re-open of the section). Mirrors stores/tree.ts's own
// identical wiring for the sidebar's REST-backed refetch, applied here to
// this store's three RPC-backed fetches - three independent debounced
// channels (not one shared one like tree.ts's) since the three lists are
// unrelated fetches that should each coalesce their own bursts without
// waiting on each other. Every one of these three notifications carries an
// empty payload on the wire (see NotificationTypes in
// protocol/types.gen.ts) - nothing to apply directly, so a debounced
// re-fetch of the affected list is the only option, exactly like
// serf/tree/changed's own "just refetch" contract.
const REFETCH_DEBOUNCE_MS = 250;

let wiredClient: AppwireClientLike | null = null;
let marketplaceRefetchTimer: ReturnType<typeof setTimeout> | undefined;
let pluginRefetchTimer: ReturnType<typeof setTimeout> | undefined;
let launchLayerRefetchTimer: ReturnType<typeof setTimeout> | undefined;

function scheduleMarketplaceRefetch(): void {
  clearTimeout(marketplaceRefetchTimer);
  marketplaceRefetchTimer = setTimeout(() => {
    void extensionsStore.getState().fetchMarketplaces();
  }, REFETCH_DEBOUNCE_MS);
}

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

function handleNotification(n: AnyNotification): void {
  if (n.method === "serf/marketplace/updated") scheduleMarketplaceRefetch();
  else if (n.method === "serf/plugin/updated") schedulePluginRefetch();
  else if (n.method === "serf/launch/updated") scheduleLaunchLayerRefetch();
}

function attachNotifications(client: AppwireClientLike): void {
  if (client === wiredClient) return; // already wired to this exact client
  wiredClient = client;
  client.onNotification(handleNotification);
}

// Watches connectionStore for the client becoming available and attaches
// this store's own notification handler to it - see stores/tree.ts's
// identical wiring for the full "why react to the store instead of reading
// it once" rationale (a mount-order race between this module and AppShell's
// own connect() effect).
connectionStore.subscribe((state) => {
  if (state.client) attachNotifications(state.client);
});
const initialClient = connectionStore.getState().client;
if (initialClient) attachNotifications(initialClient);

// resetExtensionsStoreForTests resets the store to its initial state,
// including the module-private wiring/debounce bookkeeping above.
// extensions.ts is a singleton store shared by the whole app, so
// extensions.test.ts must reset it between tests to keep them isolated - no
// production code should ever call this (mirrors threads.ts/tree.ts's own
// reset*StoreForTests precedent).
export function resetExtensionsStoreForTests(): void {
  wiredClient = null;
  clearTimeout(marketplaceRefetchTimer);
  marketplaceRefetchTimer = undefined;
  clearTimeout(pluginRefetchTimer);
  pluginRefetchTimer = undefined;
  clearTimeout(launchLayerRefetchTimer);
  launchLayerRefetchTimer = undefined;
  extensionsStore.setState({
    marketplaces: null,
    marketplacesLoading: false,
    marketplacesError: null,
    browseCatalogs: new Map(),
    plugins: null,
    pluginsLoading: false,
    pluginsError: null,
    launchLayer: null,
    launchLayerLoading: false,
    launchLayerError: null,
  });
}
