// The marketplaces store: a hub's registered plugin marketplaces and one cached
// browse result per marketplace, behind the web's Marketplaces & Plugins
// settings section and the native marketplace browser. createMarketplacesStore
// is a factory - each app builds the one instance it wires up, and tests build
// their own - returning a FrameworkFreeStore (see frameworkFreeStore.ts) whose
// state holds the store-bound actions.
//
// Two failure conventions, deliberately:
//   - FETCHES (fetchMarketplaces, browseMarketplace, reloadCatalog) never
//     throw - they track loading/error in state.
//   - MUTATIONS (add/remove/refresh/edit) reject on failure; the host decides
//     how to surface a rejection (the web toasts, native shows inline copy).
//
// The client is a port, read at request time, so a host whose connection can
// be replaced (the web's connection store) hands in an object that resolves
// the current client on each call.

import type { AppwireClient } from "../../client";
import { errorText } from "../../errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type {
  MarketplaceAddParams,
  MarketplaceCatalogPlugin,
  MarketplaceEditParams,
  MarketplaceEntry,
} from "../../types.gen";
import { createListRevision } from "./listRevision";
import { createStoreLifecycle, type StoreLifecycle } from "./storeLifecycle";

export type MarketplacesClient = Pick<AppwireClient, "request" | "onNotification">;

// One cached browse result per marketplace name - permanent until an explicit
// refreshMarketplace/removeMarketplace/editMarketplace/reloadCatalog retires
// it ("re-expanding never re-fetches"). "loading" is written synchronously
// BEFORE the request is sent (see browseMarketplace below), so a concurrent
// second call sends no second request; the in-flight map below only lets such
// a call wait for the request it did not start.
export type MarketplaceCatalogEntry =
  | { status: "loading" }
  | { status: "loaded"; description?: string; plugins: MarketplaceCatalogPlugin[] }
  | { status: "error"; error: string };

export interface MarketplacesState {
  marketplaces: MarketplaceEntry[] | null;
  marketplacesLoading: boolean;
  /** The failed list request's own text (errorText), for the host to
   * translate at render; null once a list succeeds. */
  marketplacesError: string | null;
  fetchMarketplaces(): Promise<void>;
  addMarketplace(params: MarketplaceAddParams): Promise<void>;
  removeMarketplace(name: string): Promise<void>;
  refreshMarketplace(name: string): Promise<void>;
  /** Rename and/or re-source a marketplace. The browse cache for the old AND
   * new names is dropped: a re-source changes the catalog, and a renamed
   * entry's catalog is keyed by its new name. */
  editMarketplace(params: MarketplaceEditParams): Promise<void>;

  browseCatalogs: Map<string, MarketplaceCatalogEntry>;
  /** Loads this marketplace's catalog into browseCatalogs, resolving once the
   * catalog is settled - whether or not this call is the one that started the
   * request. A call for a catalog already in flight sends nothing and waits
   * for that request; a call for a settled catalog sends nothing at all. */
  browseMarketplace(name: string): Promise<void>;
  /** Drops this marketplace's cached catalog, whatever its status, and loads
   * it again: the retry after a failed browse and the pull-to-refresh. */
  reloadCatalog(name: string): Promise<void>;
}

export interface MarketplacesStore
  extends FrameworkFreeStore<MarketplacesState>,
    Omit<StoreLifecycle<MarketplacesState>, "guard"> {
  /** Follows evener/marketplace/updated, which the hub broadcasts to every
   * client after any client's successful mutation: every cached catalog is
   * retired (the notification names nothing) and the list is refetched after
   * a short debounce. Idempotent. */
  start(): void;
  /** Back to the initial state; requests still in flight publish nothing when
   * they land. The notification subscription, if started, stays. */
  reset(): void;
  /** Terminal: unsubscribes, cancels a pending refetch and drops every reply
   * still in flight, so subscribers hear nothing more - for a host whose
   * screen unmounts. start() refuses afterwards. */
  dispose(): void;
}

export const MARKETPLACE_REFETCH_DEBOUNCE_MS = 250;

export function createMarketplacesStore(client: MarketplacesClient): MarketplacesStore {
  // A browse response is keyed by marketplace name, so it can outlive the
  // catalog it describes: a request started before an edit, a re-source or a
  // removal resolves afterwards and would put the retired catalog straight
  // back into the entry that mutation just dropped - most sharply on a
  // same-name re-source, where the name survives and only the contents
  // change. Each marketplace therefore carries a generation, bumped when its
  // cache entry is retired, and a browse whose generation moved while it was
  // in flight lands nothing.
  const browseGenerations = new Map<string, number>();

  // The promise of each browse still on the wire, keyed the same way. A caller
  // that finds a catalog already loading has to wait for it, and the "loading"
  // marker alone says nothing about when it lands. A retire can leave this
  // holding the promise of a request whose entry is already gone, which is why
  // the finally below deletes only its own registration.
  const browseInFlight = new Map<string, Promise<void>>();

  // Every marketplace mutation, and the notification refetch, replaces the
  // whole list from its own response; see listRevision.ts for the fence. A
  // list that snapshotted before an in-flight mutation committed is put right
  // by the refetch that mutation's broadcast triggers. A retirement in the
  // same response still applies - retiring is monotonic, and a catalog stale
  // under the older list is stale under the newer one too.
  const listRevision = createListRevision();

  // What reset() and dispose() end. The list revision alone cannot say it: an
  // outrun mutation still retires the catalogs it names, because retiring is
  // monotonic and a catalog stale under the older list is stale under the
  // newer one too. That holds only WITHIN a generation - after a reset the
  // store has forgotten what it read, and a browse since then is about the
  // state the reset left behind, not the one an older reply belongs to.
  let generation = 0;

  function browseGeneration(name: string): number {
    return browseGenerations.get(name) ?? 0;
  }

  /** Moves this name's generation, so a browse of it still on the wire lands
   * nothing. */
  function retireGeneration(name: string): void {
    browseGenerations.set(name, browseGeneration(name) + 1);
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
      retireGeneration(name);
    }
    return next;
  }

  const lifecycle = createStoreLifecycle<MarketplacesState>(client, {
    method: "evener/marketplace/updated",
    debounceMs: MARKETPLACE_REFETCH_DEBOUNCE_MS,
    store: () => store,
    refetch: (state) => state.fetchMarketplaces(),
    // The notification names nothing, so every cached catalog may now
    // describe a marketplace another client has since added to, removed,
    // refreshed, renamed or re-sourced. All of them are retired, and the
    // generation bump fences the browses already in flight, which would
    // otherwise land their pre-change catalogs after this. Expanded views
    // re-request their own.
    onNotified: () =>
      store.setState((s) => ({ browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [...s.browseCatalogs.keys()]) })),
    // Every browse still on the wire is fenced with the list: reset and
    // dispose both want a reply that started before them to land nothing -
    // its catalogs included, which is what the generation below counts.
    onFence: (set) => {
      generation += 1;
      listRevision.fence();
      for (const name of browseInFlight.keys()) retireGeneration(name);
      browseInFlight.clear();
      // Nothing is coming to lower it.
      set({ marketplacesLoading: false });
    },
    wantsList: (s) => s.marketplaces !== null || s.marketplacesError !== null || s.marketplacesLoading,
  });

  const store = createFrameworkFreeStore<MarketplacesState>((publish, get) => {
    const set = lifecycle.guard(publish);

    /** Runs one mutation: its response's list is written only if no later
     * revision has committed since, and the catalogs it names are retired
     * either way within the generation it was issued in (retiring is
     * monotonic). Rejects as the request does. */
    async function mutate(
      request: () => Promise<{ marketplaces: MarketplaceEntry[] }>,
      retire: (string | undefined)[],
    ): Promise<void> {
      const revision = listRevision.next();
      const issuedIn = generation;
      let resp: { marketplaces: MarketplaceEntry[] };
      try {
        resp = await request();
      } catch (err) {
        // Nothing to publish, so nothing to own: see listRevision's retract.
        listRevision.retract(revision);
        throw err;
      }
      if (issuedIn !== generation) return;
      // The catalogs this mutation names are retired whether or not its list
      // is the live answer: retiring is monotonic within the generation, so a
      // catalog stale under an older list is stale under a newer one too.
      if (retire.length) set((s) => ({ browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, retire) }));
      // The list, and the two fields that belong to it, go through the fence:
      // see plugins.ts's mutate for why the live answer owns all three.
      listRevision.publish(revision, () =>
        set({ marketplaces: resp.marketplaces, marketplacesLoading: false, marketplacesError: null }),
      );
    }

    const setCatalog = (name: string, entry: MarketplaceCatalogEntry): void =>
      set((s) => ({ browseCatalogs: new Map(s.browseCatalogs).set(name, entry) }));

    return {
      marketplaces: null,
      marketplacesLoading: false,
      marketplacesError: null,

      async fetchMarketplaces() {
        const revision = listRevision.next();
        set({ marketplacesLoading: true, marketplacesError: null });
        try {
          const resp = await client.request("evener/marketplace/list", {});
          // The loading flag and the error belong to this response as much as
          // its list does, so an outrun fetch writes none of the three: its
          // success would clear an error a newer fetch posted or hide a load
          // still running, and its failure would put "Failed to load" over a
          // newer mutation's list.
          listRevision.publish(revision, () =>
            set({ marketplaces: resp.marketplaces, marketplacesLoading: false, marketplacesError: null }),
          );
        } catch (err) {
          listRevision.publish(revision, () => set({ marketplacesLoading: false, marketplacesError: errorText(err) }));
        }
      },

      addMarketplace: (params) => mutate(() => client.request("evener/marketplace/add", params), []),
      removeMarketplace: (name) => mutate(() => client.request("evener/marketplace/remove", { name }), [name]),
      refreshMarketplace: (name) => mutate(() => client.request("evener/marketplace/refresh", { name }), [name]),
      editMarketplace: (params) =>
        mutate(() => client.request("evener/marketplace/edit", params), [params.name, params.newName]),

      browseCatalogs: new Map(),

      async browseMarketplace(name) {
        // Loaded, errored, or already in flight - see MarketplaceCatalogEntry's
        // own doc comment. A settled entry has no in-flight promise, so that
        // case resolves straight away.
        if (get().browseCatalogs.has(name)) return browseInFlight.get(name);
        const generation = browseGeneration(name);
        let settled!: (value: void | PromiseLike<void>) => void;
        const inFlight = new Promise<void>((resolve) => {
          settled = resolve;
        });
        browseInFlight.set(name, inFlight);
        setCatalog(name, { status: "loading" });
        try {
          const resp = await client.request("evener/marketplace/browse", { name });
          if (browseGeneration(name) !== generation) return;
          setCatalog(name, { status: "loaded", description: resp.description, plugins: resp.plugins });
        } catch (err) {
          // Fenced the same way a success is: an error from a catalog that has
          // since been retired says nothing about the one that replaced it.
          if (browseGeneration(name) !== generation) return;
          setCatalog(name, { status: "error", error: errorText(err) });
        } finally {
          // A retire can drop this name's entry while this request is on the
          // wire and a replacement request take its place; that one is what
          // the map must keep and what this request's waiters actually want,
          // since this one's answer is fenced out.
          const current = browseInFlight.get(name);
          if (current === inFlight) browseInFlight.delete(name);
          settled(current === inFlight ? undefined : current);
        }
      },

      reloadCatalog(name) {
        set((s) => ({ browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [name]) }));
        return get().browseMarketplace(name);
      },
    };
  });

  const { start, connectionChanged, reset, dispose } = lifecycle;
  return { ...store, start, connectionChanged, reset, dispose };
}
