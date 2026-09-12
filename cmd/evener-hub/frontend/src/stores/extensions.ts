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
import { errorText } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type {
  AnyNotification,
  LaunchConfigLayer,
  MarketplaceAddParams,
  MarketplaceCatalogPlugin,
  MarketplaceEditParams,
  MarketplaceEntry,
  PathValidateResponse,
  PluginEntry,
} from "../protocol/types.gen";
import { connectionStore } from "./connection";

// One cached browse result per marketplace name - permanent until an
// explicit refreshMarketplace/removeMarketplace/editMarketplace retires it, exactly
// like the legacy plugins-manager.html's own browseCatalogs cache
// ("re-expanding never re-fetches"). "loading" is written synchronously
// BEFORE the request is sent (see browseMarketplace below), the same
// synchronous-marker trick the legacy's toggleMarketplaceExpanded uses to
// keep a concurrent second call from sending a second request; the
// browseInFlight map below only lets such a call wait for the request it
// did not start.
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
  /** Rename and/or re-source a marketplace (spec 2026-09-07 §3). The browse
   * cache for the old AND new names is dropped: a re-source changes the
   * catalog, and a renamed entry's catalog is keyed by its new name. */
  editMarketplace(params: MarketplaceEditParams): Promise<void>;

  browseCatalogs: Map<string, MarketplaceCatalogEntry>;
  /** Loads this marketplace's catalog into browseCatalogs, resolving once the
   * catalog is settled - whether or not this call is the one that started the
   * request. A call for a catalog already in flight sends nothing and waits
   * for that request. */
  browseMarketplace(name: string): Promise<void>;

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

// A browse response is keyed by marketplace name, so it can outlive the
// catalog it describes: a request started before an edit, a re-source or a
// removal resolves afterwards and would put the retired catalog straight back
// into the entry that mutation just dropped - most sharply on a same-name
// re-source, where the name survives and only the contents change. Each
// marketplace therefore carries a generation, bumped when its cache entry is
// retired, and a browse whose generation moved while it was in flight lands
// nothing.
const browseGenerations = new Map<string, number>();

// The promise of each browse still on the wire, keyed the same way. A caller
// that finds a catalog already loading - the browse filter, whose query can
// arrive after a click or a retire started the request - has to wait for it,
// and the "loading" marker alone says nothing about when it lands. A retire
// can leave this holding the promise of a request whose entry is already
// gone, which is why the finally below deletes only its own registration.
const browseInFlight = new Map<string, Promise<void>>();

function browseGeneration(name: string): number {
  return browseGenerations.get(name) ?? 0;
}

/** Drops these names' cached catalogs and moves their generations, returning
 * the next browseCatalogs map. Called only once a mutation has landed: a bump
 * ahead of a request that then fails would fence out the in-flight browse and
 * strand the "loading" entry it had already written, leaving a permanent
 * spinner behind a failure that changed nothing. */
function retireBrowseCatalogs(
  catalogs: Map<string, MarketplaceCatalogEntry>,
  names: (string | undefined)[],
): Map<string, MarketplaceCatalogEntry> {
  const next = new Map(catalogs);
  for (const name of names) {
    if (!name) continue;
    next.delete(name);
    browseGenerations.set(name, browseGeneration(name) + 1);
  }
  return next;
}

// Every marketplace mutation, and the notification refetch, replaces the whole
// list from its own response. The hub answers one connection's requests in the
// order they were sent - every marketplace method goes through the connection's
// serial worker (appserver's concurrentDispatchMethod names the few thread
// reads that do not) - and a dropped connection rejects everything it had in
// flight, so a later response is always the newer list. The revision each
// request takes as it starts, the same shape as the browse generations above,
// is the fence should that ever stop holding: a response writes its list only
// if no later revision has committed since, and a list that snapshotted before
// an in-flight mutation committed is put right by the refetch that mutation's
// broadcast triggers. A retirement in the same response still applies -
// retiring is monotonic, and a catalog stale under the older list is stale
// under the newer one too.
let marketplaceRevisions = 0;
let appliedMarketplaceRevision = 0;

function nextMarketplaceRevision(): number {
  marketplaceRevisions += 1;
  return marketplaceRevisions;
}

/** Whether the response that took `revision` is still the store's newest word
 * on the marketplaces, and records it as such when it is. A failed list counts:
 * its error is marketplace state too, so a success that started earlier must
 * not clear it. */
function commitMarketplaceRevision(revision: number): boolean {
  if (revision < appliedMarketplaceRevision) return false;
  appliedMarketplaceRevision = revision;
  return true;
}

/** The `marketplaces` half of a response's state patch: its own list, or
 * nothing at all once a later revision has committed one. */
function marketplacesFrom(revision: number, marketplaces: MarketplaceEntry[]): { marketplaces?: MarketplaceEntry[] } {
  return commitMarketplaceRevision(revision) ? { marketplaces } : {};
}

export const extensionsStore = createStore<ExtensionsStoreState>((set, get) => ({
  marketplaces: null,
  marketplacesLoading: false,
  marketplacesError: null,

  async fetchMarketplaces() {
    const revision = nextMarketplaceRevision();
    set({ marketplacesLoading: true, marketplacesError: null });
    try {
      const client = requireClient();
      const resp = await client.request("evener/marketplace/list", {});
      // The loading flag and the error belong to this response as much as its
      // list does, so an outrun fetch writes none of the three: its success
      // would clear an error a newer fetch posted or hide a load still
      // running, and its failure would put "Failed to load" over a newer
      // mutation's list.
      if (!commitMarketplaceRevision(revision)) return;
      set({ marketplaces: resp.marketplaces, marketplacesLoading: false, marketplacesError: null });
    } catch (err) {
      if (!commitMarketplaceRevision(revision)) return;
      set({ marketplacesLoading: false, marketplacesError: errorText(err) });
    }
  },

  async addMarketplace(params) {
    const revision = nextMarketplaceRevision();
    const client = requireClient();
    const resp = await client.request("evener/marketplace/add", params);
    set(marketplacesFrom(revision, resp.marketplaces));
  },

  async removeMarketplace(name) {
    const revision = nextMarketplaceRevision();
    const client = requireClient();
    const resp = await client.request("evener/marketplace/remove", { name });
    set((s) => ({
      ...marketplacesFrom(revision, resp.marketplaces),
      browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [name]),
    }));
  },

  async refreshMarketplace(name) {
    const revision = nextMarketplaceRevision();
    const client = requireClient();
    const resp = await client.request("evener/marketplace/refresh", { name });
    set((s) => ({
      ...marketplacesFrom(revision, resp.marketplaces),
      browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [name]),
    }));
  },

  async editMarketplace(params) {
    const revision = nextMarketplaceRevision();
    const client = requireClient();
    const resp = await client.request("evener/marketplace/edit", params);
    set((s) => ({
      ...marketplacesFrom(revision, resp.marketplaces),
      browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [params.name, params.newName]),
    }));
  },

  browseCatalogs: new Map(),

  async browseMarketplace(name) {
    // Loaded, errored, or already in flight - see MarketplaceCatalogEntry's
    // own doc comment. A settled entry has no in-flight promise, so that case
    // resolves straight away.
    if (get().browseCatalogs.has(name)) return browseInFlight.get(name);
    const client = requireClient();
    const generation = browseGeneration(name);
    let settled!: (value: void | PromiseLike<void>) => void;
    const inFlight = new Promise<void>((resolve) => {
      settled = resolve;
    });
    browseInFlight.set(name, inFlight);
    set((s) => {
      const next = new Map(s.browseCatalogs);
      next.set(name, { status: "loading" });
      return { browseCatalogs: next };
    });
    try {
      const resp = await client.request("evener/marketplace/browse", { name });
      if (browseGeneration(name) !== generation) return;
      set((s) => {
        const next = new Map(s.browseCatalogs);
        next.set(name, { status: "loaded", description: resp.description, plugins: resp.plugins });
        return { browseCatalogs: next };
      });
    } catch (err) {
      // Fenced the same way a success is: an error from a catalog that has
      // since been retired says nothing about the one that replaced it.
      if (browseGeneration(name) !== generation) return;
      set((s) => {
        const next = new Map(s.browseCatalogs);
        next.set(name, { status: "error", error: errorText(err) });
        return { browseCatalogs: next };
      });
    } finally {
      // A retire can drop this name's entry while this request is on the wire
      // and a replacement request take its place; that one is what the map
      // must keep and what this request's waiters actually want, since this
      // one's answer is fenced out.
      const current = browseInFlight.get(name);
      if (current === inFlight) browseInFlight.delete(name);
      settled(current === inFlight ? undefined : current);
    }
  },

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
// until a manual re-open of the section). Mirrors the navigation store's
// identical wiring for the sidebar's REST-backed refetch, applied here to
// this store's three RPC-backed fetches - three independent debounced
// channels (not one shared one like tree.ts's) since the three lists are
// unrelated fetches that should each coalesce their own bursts without
// waiting on each other. All three notifications' generated payload types
// are empty ({}) in protocol/types.gen.ts, so there is nothing to apply
// directly and a debounced re-fetch of the affected list is the only
// option, exactly like evener/navigation/invalidated's own "just refetch" contract.
// On the wire evener/marketplace/updated and evener/plugin/updated genuinely
// send empty maps (notifyMarketplaceUpdated/notifyPluginUpdated,
// cmd/evener-hub/app_rpc.go:657,663), while evener/launch/updated carries
// {cwd, layer} (notifyLaunchUpdated, app_rpc.go:772-775) whose fields the
// generated type drops because codegen can't see into Go's untyped
// map[string]string; this refetch is payload-agnostic either way.
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
  if (n.method === "evener/marketplace/updated") {
    // The notification names nothing, so every cached catalog may now describe
    // a marketplace another client has since added to, removed, refreshed,
    // renamed or re-sourced. All of them are retired, and the generation bump
    // fences the browses already in flight, which would otherwise land their
    // pre-change catalogs after this. Expanded nodes re-request their own -
    // see BrowseSection's MarketplaceNode.
    extensionsStore.setState((s) => ({
      browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [...s.browseCatalogs.keys()]),
    }));
    scheduleMarketplaceRefetch();
  } else if (n.method === "evener/plugin/updated") {
    extensionsStore.setState((state) => ({ pluginRevision: state.pluginRevision + 1 }));
    schedulePluginRefetch();
  } else if (n.method === "evener/launch/updated") scheduleLaunchLayerRefetch();
}

function attachNotifications(client: AppwireClientLike): void {
  if (client === wiredClient) return; // already wired to this exact client
  wiredClient = client;
  client.onNotification(handleNotification);
}

// Watches connectionStore for the client becoming available and attaches
// this store's own notification handler to it - see the navigation store's
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
  browseGenerations.clear();
  browseInFlight.clear();
  marketplaceRevisions = 0;
  appliedMarketplaceRevision = 0;
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
