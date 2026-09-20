// The installed-plugins store: the plugins installed on a hub and the six
// mutations that change them, behind the web's Marketplaces & Plugins
// settings section and the native plugins screen. createPluginsStore is a
// factory - each app builds the one instance it wires up, and tests build
// their own - returning a FrameworkFreeStore (see frameworkFreeStore.ts)
// whose state holds the store-bound actions.
//
// Two failure conventions, deliberately, shared with marketplaces.ts:
//   - The FETCH (fetchPlugins) never throws - it tracks loading/error in
//     state.
//   - MUTATIONS (install/upgrade/remove/enable/disable/setAutoUpgrade)
//     reject on failure; the host decides how to surface a rejection (the
//     web toasts, native shows inline copy).
//
// The client is a port, read at request time, so a host whose connection can
// be replaced (the web's connection store) hands in an object that resolves
// the current client on each call.

import type { AppwireClient } from "../../client";
import { errorText } from "../../errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type { PluginEntry, PluginListResponse } from "../../types.gen";
import { createListRevision, readRevisioned, writeRevisioned } from "./listRevision";
import { attachLifecycle, createStoreLifecycle, type StoreLifecycle } from "./storeLifecycle";

export type PluginsClient = Pick<AppwireClient, "request" | "onNotification">;

export interface PluginsState {
  plugins: PluginEntry[] | null;
  pluginsLoading: boolean;
  /** The failed list request's own text (errorText), for the host to
   * translate at render; null once a list succeeds. */
  pluginsError: string | null;
  /** How many evener/plugin/updated notifications a started store has seen,
   * moved synchronously as each arrives - ahead of the debounced refetch - so
   * a host can re-key data it derives from the hub's plugin set (the web's
   * spawn-time plugin preview) the moment that set is known to have changed. */
  pluginRevision: number;
  fetchPlugins(): Promise<void>;
  installPlugin(plugin: string, marketplace: string): Promise<void>;
  upgradePlugin(plugin: string, marketplace: string): Promise<void>;
  removePlugin(plugin: string, marketplace: string): Promise<void>;
  enablePlugin(plugin: string, marketplace: string): Promise<void>;
  disablePlugin(plugin: string, marketplace: string): Promise<void>;
  setPluginAutoUpgrade(plugin: string, marketplace: string, autoUpgrade: boolean): Promise<void>;
}

export interface PluginsStore extends FrameworkFreeStore<PluginsState>, Omit<StoreLifecycle<PluginsState>, "guard"> {
  /** Follows evener/plugin/updated, which the hub broadcasts to every client
   * after any client's successful mutation (and after a marketplace edit,
   * which can re-key installed plugins): pluginRevision moves at once and the
   * list is refetched after a short debounce. Idempotent. */
  start(): void;
  /** Back to the initial state; requests still in flight publish nothing when
   * they land. The notification subscription, if started, stays. */
  reset(): void;
  /** Terminal: unsubscribes, cancels a pending refetch and drops every reply
   * still in flight, so subscribers hear nothing more - for a host whose
   * screen unmounts. start() refuses afterwards. */
  dispose(): void;
}

export const PLUGIN_REFETCH_DEBOUNCE_MS = 250;

/** The five mutations addressed by a plugin reference alone; setAutoUpgrade
 * carries its flag as well. */
type PluginRefMethod =
  | "evener/plugin/install"
  | "evener/plugin/upgrade"
  | "evener/plugin/remove"
  | "evener/plugin/enable"
  | "evener/plugin/disable";

export function createPluginsStore(client: PluginsClient): PluginsStore {
  // Every mutation, and the notification refetch, replaces the whole list from
  // its own response; see listRevision.ts for the fence.
  const listRevision = createListRevision();

  const lifecycle = createStoreLifecycle<PluginsState>(client, {
    method: "evener/plugin/updated",
    debounceMs: PLUGIN_REFETCH_DEBOUNCE_MS,
    store: () => store,
    refetch: (state) => state.fetchPlugins(),
    revision: listRevision,
    // The revision moves now, not when the refetch lands: a host keying
    // derived data on it wants to know the set changed as soon as the hub
    // says so.
    onNotified: () => store.setState((s) => ({ pluginRevision: s.pluginRevision + 1 })),
    // The lifecycle fences listRevision; nothing is coming to lower the flag a
    // fenced read raised.
    onFence: (set) => set({ pluginsLoading: false }),
    // A mutation issued before any fetchPlugins call touches none of these
    // three fields; the lifecycle ORs listRevision.hasLive() in for that case
    // (see storeLifecycle.ts's revision option).
    wantsList: (s) => s.plugins !== null || s.pluginsError !== null || s.pluginsLoading,
  });

  const store = createFrameworkFreeStore<PluginsState>((publish) => {
    const set = lifecycle.guard(publish);

    /** Runs one mutation: its response's list is written only if no later
     * revision has committed since. Rejects as the request does. */
    // The same three fields a read's success writes. A response that owns the
    // list owns the error and the loading flag with it - a read this one
    // outran publishes none of the three, the flag it raised included.
    const mutate = (request: () => Promise<PluginListResponse>): Promise<void> =>
      writeRevisioned(listRevision, request, (resp) => () => {
        set({ plugins: resp.plugins, pluginsLoading: false, pluginsError: null });
      });

    const mutation = (method: PluginRefMethod) => (plugin: string, marketplace: string) =>
      mutate(() => client.request(method, { plugin, marketplace }));

    return {
      plugins: null,
      pluginsLoading: false,
      pluginsError: null,
      pluginRevision: 0,

      fetchPlugins() {
        set({ pluginsLoading: true, pluginsError: null });
        // The loading flag and the error belong to this answer as much as its
        // list does, so an outrun read publishes none of the three: its success
        // would clear an error a newer read posted or hide a load still
        // running, and its failure would put "Failed to load" over a newer
        // write's list.
        return readRevisioned(listRevision, () => client.request("evener/plugin/list", {}), {
          onAnswer: (resp) => () => set({ plugins: resp.plugins, pluginsLoading: false, pluginsError: null }),
          onFailure: (err) => () => set({ pluginsLoading: false, pluginsError: errorText(err) }),
        });
      },

      installPlugin: mutation("evener/plugin/install"),
      upgradePlugin: mutation("evener/plugin/upgrade"),
      removePlugin: mutation("evener/plugin/remove"),
      enablePlugin: mutation("evener/plugin/enable"),
      disablePlugin: mutation("evener/plugin/disable"),
      setPluginAutoUpgrade: (plugin, marketplace, autoUpgrade) =>
        mutate(() => client.request("evener/plugin/setAutoUpgrade", { plugin, marketplace, autoUpgrade })),
    };
  });

  return attachLifecycle(store, lifecycle);
}
