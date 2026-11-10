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

import type { AppwireClientLike, RequestPort } from "../../clientLike";
import { friendlyErrorMessage, isMethodNotFound } from "../../errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import { pluginComponentsResultOrMissing } from "../../scopedHostRead";
import type {
  HostRequestMethod,
  PluginComponents,
  PluginComponentsResult,
  PluginEntry,
  PluginListResponse,
} from "../../types.gen";
import { HubWriteBusyError, type HubWriteGate } from "./hubWriteGate";
import { createKeyedRevision } from "./keyedRevision";
import { createListRevision, readRevisioned, writeRevisioned } from "./listRevision";
import { attachLifecycle, createStoreLifecycle, type HostLifecycle } from "./storeLifecycle";

export type PluginsClient = RequestPort<HostRequestMethod> & Pick<AppwireClientLike, "onNotification">;

/** The two target forms evener/plugin/components reads: an installed
 * plugin/marketplace pair, or a directory path. Exactly one form per target -
 * the wire's both-or-neither targets are a caller bug the hub rejects whole,
 * and this union cannot express them. */
export type PluginComponentsReadTarget = { plugin: string; marketplace: string } | { path: string };

/** The cache key for one plugin-components target: "<plugin>@<marketplace>"
 * for an installed pair, "path:<path>" for a directory. The one canonical
 * construction - the cache's consumers key their lookups with this, never a
 * hand-built string. */
export function pluginComponentsKey(target: PluginComponentsReadTarget): string {
  return "path" in target ? `path:${target.path}` : `${target.plugin}@${target.marketplace}`;
}

// One cached component inventory per plugin target - permanent until a
// plugin-list mutation (or the evener/plugin/updated that follows one) retires
// it. "loading" is written synchronously BEFORE the batch request is sent, so
// a concurrent call for the same key sends no second request; the keyed
// revision's in-flight registry is what lets such a call wait for the request
// it did not start.
export type PluginComponentsEntry =
  | { status: "loading" }
  | { status: "loaded"; components: PluginComponents }
  | { status: "error"; source: "target" | "request"; error: string };

export interface PluginsState {
  plugins: PluginEntry[] | null;
  pluginsLoading: boolean;
  /** The failed list request's friendlyErrorMessage, translated while the
   * error still has its type so a hub's own WireError explanation survives;
   * render it as-is. null once a list succeeds. */
  pluginsError: string | null;
  /** How many evener/plugin/updated notifications a started store has seen,
   * moved synchronously as each arrives - ahead of the debounced refetch - so
   * a host can re-key data it derives from the hub's plugin set (the web's
   * spawn-time plugin preview) the moment that set is known to have changed. */
  pluginRevision: number;
  /** One cached component inventory per plugin target, keyed by
   * pluginComponentsKey - the counts the installed list's rows show. Entries
   * land through fetchPluginComponents; a plugin-list mutation (or the
   * notification that follows one) retires them all, and the next fetchPlugins
   * re-issues the batch. */
  pluginComponents: Map<string, PluginComponentsEntry>;
  /** Reads many targets' component inventories in one batch call, landing one
   * entry per target under its key. A target already in the map is never
   * re-requested - loading keys wait for the request already carrying them,
   * settled keys send nothing - so concurrent calls for the same missing keys
   * coalesce into the first request. Never throws; state tracks it. */
  fetchPluginComponents(targets: ReadonlyArray<PluginComponentsReadTarget>): Promise<void>;
  /** Drops these targets' cached entries, whatever their status, and reads
   * them again: the retry after a failed read (an older hub's method error)
   * - fetchPluginComponents never re-requests a key the cache already holds,
   * so only dropping the entry makes a fresh read go out. Never throws;
   * state tracks it. */
  reloadPluginComponents(targets: ReadonlyArray<PluginComponentsReadTarget>): Promise<void>;
  fetchPlugins(): Promise<void>;
  /** Asks the hub whether each plugin has a newer version: a git-backed one's
   * remote, or a marketplace-relative one's refreshed marketplace clone
   * (evener/plugin/checkUpdates), then re-reads the list, which carries the
   * flags the hub now holds. A host calls it when its plugins view opens. It
   * never throws, and a failed check publishes nothing: an older hub without
   * the method leaves every plugin unflagged, so no Upgrade is offered.
   * Resolves true once the hub has given its answer (the flags, or an older
   * hub's "method not found"), false when the request never reached one (a
   * dropped connection, a remote host the proxy couldn't reach), which a host
   * may ask again once it reconnects. */
  checkPluginUpdates(): Promise<boolean>;
  installPlugin(plugin: string, marketplace: string): Promise<void>;
  upgradePlugin(plugin: string, marketplace: string): Promise<void>;
  removePlugin(plugin: string, marketplace: string): Promise<void>;
  enablePlugin(plugin: string, marketplace: string): Promise<void>;
  disablePlugin(plugin: string, marketplace: string): Promise<void>;
  setPluginAutoUpgrade(plugin: string, marketplace: string, autoUpgrade: boolean): Promise<void>;
}

export interface PluginsStore extends FrameworkFreeStore<PluginsState>, HostLifecycle<PluginsState> {
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

/** The check waits on every plugin's remote, a few at a time and each under
 * the hub's own per-remote timeout, so it can run far past a plain read's
 * default timeout. The hub cuts off the whole check at its overall deadline
 * (internal/plugins updateCheckDeadline), which is not on the wire, so
 * this number is set by hand: it must exceed that deadline with room for the
 * hub's own overhead, or the client gives up on a check the hub is still
 * finishing. */
export const PLUGIN_UPDATE_CHECK_TIMEOUT_MS = 120_000;

/** The five mutations addressed by a plugin reference alone; setAutoUpgrade
 * carries its flag as well. */
type PluginRefMethod =
  | "evener/plugin/install"
  | "evener/plugin/upgrade"
  | "evener/plugin/remove"
  | "evener/plugin/enable"
  | "evener/plugin/disable";

export function createPluginsStore(client: PluginsClient, gate: HubWriteGate): PluginsStore {
  // Every mutation, and the notification refetch, replaces the whole list from
  // its own response; see listRevision.ts for the fence.
  const listRevision = createListRevision();

  // A component-inventory answer is keyed by its target's key, so it can
  // outlive the inventory it describes: a batch started before a mutation
  // resolves after it and would put the retired inventory straight back into
  // the entry that mutation just dropped - most sharply on an upgrade, where
  // the key survives and only the contents change. Each key therefore carries
  // a generation, bumped when its cache entry is retired, and a batch whose
  // generation moved while it was in flight lands nothing for that key.
  const components = createKeyedRevision();

  /** Drops these keys' cached inventories and retires their generations,
   * returning the next pluginComponents map. Called only once a mutation has
   * landed (or a notification says one landed elsewhere): a bump ahead of a
   * request that then fails would fence out the in-flight batch and strand
   * the "loading" entries it had already written. Also called by
   * reloadPluginComponents, whose re-read immediately re-registers for the
   * same keys, so a read it fences cannot strand its marker behind it. */
  function retirePluginComponents(
    cached: Map<string, PluginComponentsEntry>,
    keys: (string | undefined)[],
  ): Map<string, PluginComponentsEntry> {
    const next = new Map(cached);
    for (const key of keys) {
      if (!key) continue;
      next.delete(key);
      components.retire(key);
    }
    return next;
  }

  const lifecycle = createStoreLifecycle<PluginsState>(client, {
    method: "evener/plugin/updated",
    debounceMs: PLUGIN_REFETCH_DEBOUNCE_MS,
    store: () => store,
    refetch: (state) => state.fetchPlugins(),
    revision: listRevision,
    // The revision moves now, not when the refetch lands: a host keying
    // derived data on it wants to know the set changed as soon as the hub
    // says so.
    // The notification names nothing, so every cached inventory may describe a
    // plugin another client has since installed, upgraded or removed: all of
    // them are retired exactly as a local mutation's landing retires them, and
    // the refetch this notification schedules re-issues the batch.
    onNotified: () =>
      store.setState((s) => ({
        pluginRevision: s.pluginRevision + 1,
        pluginComponents: retirePluginComponents(s.pluginComponents, [...s.pluginComponents.keys()]),
      })),
    onFence: (set) => {
      const fenced = components.retireInFlight();
      // A fenced batch's entries are "loading" - nothing else was going to
      // answer them - and left behind they read as settled: fetchPluginComponents
      // would see them, find no live registration to wait on (retireInFlight
      // just forgot it), and resolve having sent nothing. Dropping them here,
      // in the fence path, is the same rule reset() and dispose() apply to the
      // list itself.
      set((s) => {
        if (!fenced.length) return { pluginsLoading: false };
        const pluginComponents = new Map(s.pluginComponents);
        for (const key of fenced) pluginComponents.delete(key);
        return { pluginsLoading: false, pluginComponents };
      });
    },
    // A mutation issued before any fetchPlugins call touches none of these
    // three fields; the lifecycle ORs listRevision.hasLive() in for that case
    // (see storeLifecycle.ts's revision option).
    wantsList: (s) => s.plugins !== null || s.pluginsError !== null || s.pluginsLoading,
  });

  const store = createFrameworkFreeStore<PluginsState>((publish, get) => {
    const set = lifecycle.guard(publish);

    const entryFor = (row: PluginComponentsResult | undefined): PluginComponentsEntry => {
      const result = pluginComponentsResultOrMissing(row);
      if (!row) return { status: "error", source: "request", error: result.error ?? "" };
      if (result.error) return { status: "error", source: "target", error: result.error };
      return { status: "loaded", components: result.components ?? {} };
    };

    /** Issues one batch read for the missing targets, marking every key
     * "loading" BEFORE the request is sent so a concurrent call for any of
     * them waits for this read instead of starting its own. Each key's waiter
     * resolves when that key is settled - or when a later read replaced this
     * one for it (a retirement in between), which is the read the waiter
     * actually wants. */
    const readPluginComponents = async (
      missing: { key: string; target: PluginComponentsReadTarget }[],
    ): Promise<void> => {
      const issued = missing.map(({ key }) => {
        const revision = components.issue(key);
        let settle!: (value: void | PromiseLike<void>) => void;
        const read = new Promise<void>((resolve) => {
          settle = resolve;
        });
        components.begin(key, read);
        return { key, revision, settle, read };
      });
      set((s) => {
        const pluginComponents = new Map(s.pluginComponents);
        for (const { key } of issued) pluginComponents.set(key, { status: "loading" });
        return { pluginComponents };
      });
      // The whole call's answer is one shape: the rows, or the failure that
      // reached no rows (an older hub without the method, a dropped
      // connection). Either way every requested key lands - rows under their
      // keys, the failure as an error entry for each - unless a retirement
      // fenced the key first.
      let rows: PluginComponentsResult[] | undefined;
      let failure: unknown;
      let failed = false;
      try {
        rows = (await client.request("evener/plugin/components", { targets: missing.map(({ target }) => target) }))
          .results;
      } catch (err) {
        failed = true;
        failure = err;
      }
      let landed: Map<string, PluginComponentsEntry> | undefined;
      issued.forEach(({ key, revision }, index) => {
        if (!components.current(key, revision)) return;
        landed ??= new Map(get().pluginComponents);
        landed.set(
          key,
          failed
            ? { status: "error", source: "request", error: friendlyErrorMessage(failure) }
            : entryFor(rows?.[index]),
        );
      });
      if (landed) set({ pluginComponents: landed });
      for (const entry of issued) {
        entry.settle(components.settle(entry.key, entry.read));
      }
      await Promise.all(issued.map((entry) => entry.read));
    };

    /** Runs one mutation under the shared hub write gate: its response's list
     * is written only if no later revision has committed since. Rejects as the
     * request does, or with HubWriteBusyError when another plugin or
     * marketplace write already holds the gate. */
    // The same three fields a read's success writes. A response that owns the
    // list owns the error and the loading flag with it - a read this one
    // outran publishes none of the three, the flag it raised included.
    const mutate = async (request: () => Promise<PluginListResponse>): Promise<void> => {
      const ran = await gate.run(async () => {
        const issuedIn = lifecycle.epoch();
        let retired = false;
        let published = false;
        await writeRevisioned(listRevision, request, (resp) => {
          // A reset or a dispose ended the generation this write was issued in:
          // its answer is about a store that has forgotten everything it read.
          if (issuedIn !== lifecycle.epoch()) return null;
          // Every cached inventory retires whether or not this write's list is
          // the live answer: any mutation can have changed any plugin's files,
          // and retiring is monotonic within the generation. The next
          // fetchPlugins re-issues the batch for the list it lands.
          set((s) => ({
            pluginComponents: retirePluginComponents(s.pluginComponents, [...s.pluginComponents.keys()]),
          }));
          retired = true;
          return () => {
            published = true;
            set({ plugins: resp.plugins, pluginsLoading: false, pluginsError: null });
          };
        });
        // A newer list may already have refilled before this answer retired
        // inventories. Its list remains authoritative, and no notification is
        // left to refill it, so the superseded write must do that work itself.
        if (retired && !published && issuedIn === lifecycle.epoch()) {
          await get().fetchPluginComponents(
            (get().plugins ?? []).map(({ plugin, marketplace }) => ({ plugin, marketplace })),
          );
        }
      });
      if (!ran) throw new HubWriteBusyError();
    };

    const mutation = (method: PluginRefMethod) => (plugin: string, marketplace: string) =>
      mutate(() => client.request(method, { plugin, marketplace }));

    return {
      plugins: null,
      pluginsLoading: false,
      pluginsError: null,
      pluginRevision: 0,
      pluginComponents: new Map(),

      async fetchPluginComponents(targets) {
        const waits: Promise<void>[] = [];
        const missing: { key: string; target: PluginComponentsReadTarget }[] = [];
        const requested = new Set<string>();
        for (const target of targets) {
          const key = pluginComponentsKey(target);
          if (requested.has(key)) continue;
          requested.add(key);
          // A key in the map is this store's word on it: a settled entry is
          // never re-requested (a retirement is what clears it), and a loading
          // entry's in-flight read is what this call waits for instead of
          // sending its own.
          if (get().pluginComponents.has(key)) {
            const read = components.inFlight(key);
            if (read) waits.push(read);
            continue;
          }
          missing.push({ key, target });
        }
        if (missing.length) waits.push(readPluginComponents(missing));
        await Promise.all(waits);
      },

      async reloadPluginComponents(targets) {
        const keys = targets.map((target) => pluginComponentsKey(target));
        set((s) => ({ pluginComponents: retirePluginComponents(s.pluginComponents, keys) }));
        await get().fetchPluginComponents(targets);
      },

      fetchPlugins() {
        set({ pluginsLoading: true, pluginsError: null });
        // The loading flag and the error belong to this answer as much as its
        // list does, so an outrun read publishes none of the three: its success
        // would clear an error a newer read posted or hide a load still
        // running, and its failure would put "Failed to load" over a newer
        // write's list.
        return readRevisioned(listRevision, () => client.request("evener/plugin/list", {}), {
          onAnswer: (resp) => () => {
            set({ plugins: resp.plugins, pluginsLoading: false, pluginsError: null });
            // The list's rows show component counts without per-row reads, so
            // its landing issues one batch for every installed pair key. Keys
            // already settled send nothing; only the keys a mutation or a
            // notification retired come back.
            void get().fetchPluginComponents(resp.plugins.map(({ plugin, marketplace }) => ({ plugin, marketplace })));
          },
          onFailure: (err) => () => set({ pluginsLoading: false, pluginsError: friendlyErrorMessage(err) }),
        });
      },

      async checkPluginUpdates() {
        // The check's own answer is not published: a list issued while the
        // hub was still asking remotes (a toggle, a notification refetch)
        // would outrank it and drop the flags. The list read issued after the
        // check outranks all of those, and the hub's list carries the flags.
        // No revision fences the check itself, so it compares the lifecycle's
        // epoch across its request instead.
        const issuedIn = lifecycle.epoch();
        try {
          await client.request("evener/plugin/checkUpdates", {}, { timeoutMs: PLUGIN_UPDATE_CHECK_TIMEOUT_MS });
        } catch (err) {
          // Only an older hub's "method not found" is a final answer. Any other
          // failure, a transport error or a wire error a proxy made of one (a
          // lost host channel, a busy pool), never reached a check.
          return isMethodNotFound(err);
        }
        if (lifecycle.epoch() === issuedIn) await store.getState().fetchPlugins();
        return true;
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
