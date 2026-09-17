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
import { createListRevision } from "./listRevision";
import { createStoreLifecycle, type StoreLifecycle } from "./storeLifecycle";

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
    // The revision moves now, not when the refetch lands: a host keying
    // derived data on it wants to know the set changed as soon as the hub
    // says so.
    onNotified: () => store.setState((s) => ({ pluginRevision: s.pluginRevision + 1 })),
    onFence: (set) => {
      listRevision.fence();
      // Nothing is coming to lower it.
      set({ pluginsLoading: false });
    },
    established: (s) => s.plugins !== null || s.pluginsError !== null,
  });

  const store = createFrameworkFreeStore<PluginsState>((publish) => {
    const set = lifecycle.guard(publish);

    /** Runs one mutation: its response's list is written only if no later
     * revision has committed since. Rejects as the request does. */
    async function mutate(request: () => Promise<PluginListResponse>): Promise<void> {
      const revision = listRevision.next();
      let resp: PluginListResponse;
      try {
        resp = await request();
      } catch (err) {
        // Nothing to publish, so nothing to own: see listRevision's retract.
        listRevision.retract(revision);
        throw err;
      }
      // The same three fields a read's success writes. A response that
      // commits is the store's newest word on the list, so it owns the error
      // and the loading flag too - a read this one outran writes none of the
      // three when it lands, including the flag it raised on its way out.
      listRevision.publish(revision, () => {
        set({ plugins: resp.plugins, pluginsLoading: false, pluginsError: null });
      });
    }

    const mutation = (method: PluginRefMethod) => (plugin: string, marketplace: string) =>
      mutate(() => client.request(method, { plugin, marketplace }));

    return {
      plugins: null,
      pluginsLoading: false,
      pluginsError: null,
      pluginRevision: 0,

      async fetchPlugins() {
        const revision = listRevision.next();
        set({ pluginsLoading: true, pluginsError: null });
        try {
          const resp = await client.request("evener/plugin/list", {});
          // The loading flag and the error belong to this response as much as
          // its list does, so an outrun fetch publishes none of the three: its
          // success would clear an error a newer fetch posted or hide a load
          // still running, and its failure would put "Failed to load" over a
          // newer mutation's list. Held, not dropped: if everything that
          // outran it retracts, this is the newest answer there is.
          listRevision.publish(revision, () => {
            set({ plugins: resp.plugins, pluginsLoading: false, pluginsError: null });
          });
        } catch (err) {
          listRevision.publish(revision, () => {
            set({ pluginsLoading: false, pluginsError: errorText(err) });
          });
        }
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

  const { start, connectionChanged, reset, dispose } = lifecycle;
  return { ...store, start, connectionChanged, reset, dispose };
}
