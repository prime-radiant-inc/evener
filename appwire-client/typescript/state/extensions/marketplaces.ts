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
import { ErrorMarketplaceRemoveApplied, errorText, WireError } from "../../errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type {
  MarketplaceAddParams,
  MarketplaceCatalogPlugin,
  MarketplaceEditParams,
  MarketplaceEntry,
} from "../../types.gen";
import { createKeyedRevision } from "./keyedRevision";
import { createListRevision, readRevisioned, writeRevisioned } from "./listRevision";
import { attachLifecycle, createStoreLifecycle, type StoreLifecycle } from "./storeLifecycle";

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
  /** Advances only when an accepted whole-list writer publishes a snapshot. */
  marketplacesPublicationVersion: number;
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
  /** Back to the reset state; publication version survives while requests
   * still in flight publish nothing when they land. The notification
   * subscription, if started, stays. */
  reset(): void;
  /** Terminal: unsubscribes, cancels a pending refetch and drops every reply
   * still in flight, so subscribers hear nothing more - for a host whose
   * screen unmounts. start() refuses afterwards. */
  dispose(): void;
}

export const MARKETPLACE_REFETCH_DEBOUNCE_MS = 250;

/** Classifies the hub's post-apply marketplace removal rejections
 * (appwire/errors.go) for UI consumers. Both markers mean the removal already
 * stood on the hub, so no classified outcome is ever retryable - the caller
 * reports what happened and reconciles through its normal fetch path:
 *  - marketplaceUnregisteredCloneRemains
 *    (appwire.MarketplaceUnregisteredCloneRemainsData): the unregister
 *    applied before its clone's own removal failed, so Data.applied is the
 *    current list with the target already gone - reconcile from it even
 *    though the call still rejects, the same way keybindingsStore's
 *    rejectionPayload reads a post-rename durable failure's applied state.
 *    A recognized marker with missing, unavailable, or malformed applied
 *    data - a list whose members do not each decode as an
 *    appwire.MarketplaceEntry counts as malformed - is still an
 *    applied-but-unconfirmed outcome: the unregister already landed, but
 *    callers must reconcile rather than treat the zero value as an
 *    authoritative empty list.
 *  - marketplaceRemoveApplied (appwire.MarketplaceRemoveAppliedData): the
 *    removal and its clone cleanup completed, but the fresh list read
 *    failed - removed, with no clone litter to warn about.
 * Any other rejection returns undefined and stays retryable. */
export type MarketplaceRemovalOutcome =
  | { kind: "applied"; marketplaces: MarketplaceEntry[] }
  | { kind: "unavailable" }
  | { kind: "removed" };

/** Structural check for one applied-list member: appwire.MarketplaceEntry's
 * wire shape, as generated into types.gen.ts - name a string, lastUpdated a
 * number, installLocation a string when present, and source an object whose
 * kind is a string (every other source field is omitempty, so each is checked
 * only when present, the same string-or-absent rule fromWireOverrides applies
 * to loadError). The hub's own validation already ran; this is the
 * trust-boundary re-check, so a malformed member degrades the whole payload to
 * the applied-but-unconfirmed outcome instead of being miscast into the one
 * list this failure path publishes as authoritative. */
function isMarketplaceEntry(value: unknown): value is MarketplaceEntry {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const entry = value as Record<string, unknown>;
  if (typeof entry.name !== "string") return false;
  if (typeof entry.lastUpdated !== "number") return false;
  if (entry.installLocation !== undefined && typeof entry.installLocation !== "string") return false;
  const source = entry.source;
  if (typeof source !== "object" || source === null || Array.isArray(source)) return false;
  const candidate = source as Record<string, unknown>;
  if (typeof candidate.kind !== "string") return false;
  for (const field of ["repo", "url", "path", "ref", "sha"] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string") return false;
  }
  return true;
}

/** Classifies the hub's post-apply marketplace removal rejections for UI
 * consumers; each marker's reconcile rule is documented on
 * MarketplaceRemovalOutcome above. Any other rejection returns undefined and
 * stays retryable. */
export function marketplaceRemovalOutcome(error: unknown): MarketplaceRemovalOutcome | undefined {
  if (!(error instanceof WireError)) return undefined;
  // The hub emits this marker only after the removal and its cleanup both
  // landed (appwire/errors.go), so the marker alone is the proof.
  if (error.evenerErrorInfo === ErrorMarketplaceRemoveApplied) return { kind: "removed" };
  if (error.evenerErrorInfo !== "marketplaceUnregisteredCloneRemains") return undefined;
  if (!error.data || typeof error.data !== "object") return { kind: "unavailable" };
  const data = error.data as { applied?: unknown; appliedUnavailable?: unknown };
  if (data.appliedUnavailable) return { kind: "unavailable" };
  if (!data.applied || typeof data.applied !== "object") return { kind: "unavailable" };
  const marketplaces = (data.applied as { marketplaces?: unknown }).marketplaces;
  return Array.isArray(marketplaces) && marketplaces.every(isMarketplaceEntry)
    ? { kind: "applied", marketplaces: marketplaces as MarketplaceEntry[] }
    : { kind: "unavailable" };
}

function cloneLitterApplied(error: unknown): MarketplaceEntry[] | undefined {
  const outcome = marketplaceRemovalOutcome(error);
  return outcome?.kind === "applied" ? outcome.marketplaces : undefined;
}

export function createMarketplacesStore(client: MarketplacesClient): MarketplacesStore {
  // A browse response is keyed by marketplace name, so it can outlive the
  // catalog it describes: a request started before an edit, a re-source or a
  // removal resolves afterwards and would put the retired catalog straight
  // back into the entry that mutation just dropped - most sharply on a
  // same-name re-source, where the name survives and only the contents
  // change. Each marketplace therefore carries a generation, bumped when its
  // cache entry is retired, and a browse whose generation moved while it was
  // in flight lands nothing.
  const browses = createKeyedRevision();

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
  let nextMarketplacesPublicationVersion = 0;

  /** Drops these names' cached catalogs and retires their keys, returning
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
      browses.retire(name);
    }
    return next;
  }

  function retireCatalogsAbsentFrom(
    catalogs: Map<string, MarketplaceCatalogEntry>,
    marketplaces: MarketplaceEntry[],
  ): Map<string, MarketplaceCatalogEntry> {
    const present = new Set(marketplaces.map(({ name }) => name));
    return retireBrowseCatalogs(
      catalogs,
      [...catalogs.keys()].filter((name) => !present.has(name)),
    );
  }

  function publishMarketplaceSnapshot(marketplaces: MarketplaceEntry[]) {
    nextMarketplacesPublicationVersion += 1;
    return {
      marketplaces,
      marketplacesPublicationVersion: nextMarketplacesPublicationVersion,
      marketplacesLoading: false,
      marketplacesError: null,
    };
  }

  const lifecycle = createStoreLifecycle<MarketplacesState>(client, {
    method: "evener/marketplace/updated",
    debounceMs: MARKETPLACE_REFETCH_DEBOUNCE_MS,
    store: () => store,
    refetch: (state) => state.fetchMarketplaces(),
    revision: listRevision,
    // The notification names nothing, so every cached catalog may now
    // describe a marketplace another client has since added to, removed,
    // refreshed, renamed or re-sourced. All of them are retired, and the
    // generation bump fences the browses already in flight, which would
    // otherwise land their pre-change catalogs after this. Expanded views
    // re-request their own.
    onNotified: () =>
      store.setState((s) => ({ browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [...s.browseCatalogs.keys()]) })),
    // Every browse still on the wire is fenced alongside the list revision:
    // reset and dispose both want a reply that started before them to land
    // nothing - its catalogs included, which is what the generation below
    // counts.
    onFence: (set) => {
      generation += 1;
      const fenced = browses.retireInFlight();
      // A fenced browse's catalog entry is "loading" - nothing else was going
      // to answer it - and left behind it reads as settled: browseMarketplace
      // would see it, find no live registration to wait on (retireInFlight
      // just forgot it), and resolve having sent nothing. Dropping the entry
      // here, in the fence path, is the same rule reset() and dispose() apply
      // to the list itself.
      set((s) => {
        if (!fenced.length) return { marketplacesLoading: false };
        const browseCatalogs = new Map(s.browseCatalogs);
        for (const name of fenced) browseCatalogs.delete(name);
        return { marketplacesLoading: false, browseCatalogs };
      });
    },
    resetState: (state) => ({ marketplacesPublicationVersion: state.marketplacesPublicationVersion }),
    // A mutation issued before any fetchMarketplaces call touches none of
    // these three fields; the lifecycle ORs listRevision.hasLive() in for that
    // case (see storeLifecycle.ts's revision option).
    wantsList: (s) => s.marketplaces !== null || s.marketplacesError !== null || s.marketplacesLoading,
  });

  const store = createFrameworkFreeStore<MarketplacesState>((publish, get) => {
    const set = lifecycle.guard(publish);

    /** Runs one mutation: its response's list is written only if no later
     * revision has committed since, and the catalogs it names are retired
     * either way within the generation it was issued in (retiring is
     * monotonic). Rejects as the request does. */
    function mutate(
      request: () => Promise<{ marketplaces: MarketplaceEntry[] }>,
      retire: (string | undefined)[],
      onFailure?: (error: unknown) => MarketplaceEntry[] | undefined,
    ): Promise<void> {
      const issuedIn = generation;
      return writeRevisioned(
        listRevision,
        request,
        (resp) => {
          // A reset or a dispose ended the generation this write was issued in:
          // its answer is about a store that has forgotten everything it read.
          if (issuedIn !== generation) return null;
          // The catalogs this write names are retired whether or not its list is
          // the live answer: retiring is monotonic within the generation, so a
          // catalog stale under an older list is stale under a newer one too.
          if (retire.length) set((s) => ({ browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, retire) }));
          // The list, and the two fields that belong to it, go through the fence:
          // see plugins.ts's mutate for why the live answer owns all three.
          return () =>
            set((s) => ({
              ...publishMarketplaceSnapshot(resp.marketplaces),
              browseCatalogs: retireCatalogsAbsentFrom(s.browseCatalogs, resp.marketplaces),
            }));
        },
        onFailure
          ? (error) => {
              const applied = onFailure(error);
              if (applied === undefined || issuedIn !== generation) return null;
              return () => {
                if (issuedIn !== generation) return;
                set((s) => ({
                  ...publishMarketplaceSnapshot(applied),
                  browseCatalogs: retireCatalogsAbsentFrom(
                    retire.length ? retireBrowseCatalogs(s.browseCatalogs, retire) : s.browseCatalogs,
                    applied,
                  ),
                }));
              };
            }
          : undefined,
      );
    }

    const setCatalog = (name: string, entry: MarketplaceCatalogEntry): void =>
      set((s) => ({ browseCatalogs: new Map(s.browseCatalogs).set(name, entry) }));

    return {
      marketplaces: null,
      marketplacesLoading: false,
      marketplacesError: null,
      marketplacesPublicationVersion: 0,

      fetchMarketplaces() {
        set({ marketplacesLoading: true, marketplacesError: null });
        // The loading flag and the error belong to this answer as much as its
        // list does, so an outrun read publishes none of the three: its success
        // would clear an error a newer read posted or hide a load still
        // running, and its failure would put "Failed to load" over a newer
        // write's list.
        return readRevisioned(listRevision, () => client.request("evener/marketplace/list", {}), {
          onAnswer: (resp) => () =>
            set((s) => ({
              ...publishMarketplaceSnapshot(resp.marketplaces),
              browseCatalogs: retireCatalogsAbsentFrom(s.browseCatalogs, resp.marketplaces),
            })),
          onFailure: (err) => () => set({ marketplacesLoading: false, marketplacesError: errorText(err) }),
        });
      },

      addMarketplace: (params) => mutate(() => client.request("evener/marketplace/add", params), []),
      removeMarketplace: (name) =>
        mutate(() => client.request("evener/marketplace/remove", { name }), [name], cloneLitterApplied),
      refreshMarketplace: (name) => mutate(() => client.request("evener/marketplace/refresh", { name }), [name]),
      editMarketplace: (params) =>
        mutate(() => client.request("evener/marketplace/edit", params), [params.name, params.newName]),

      browseCatalogs: new Map(),

      async browseMarketplace(name) {
        // Loaded, errored, or already in flight - see MarketplaceCatalogEntry's
        // own doc comment. A settled entry has no in-flight promise, so that
        // case resolves straight away.
        if (get().browseCatalogs.has(name)) return browses.inFlight(name);
        const revision = browses.issue(name);
        let settled!: (value: void | PromiseLike<void>) => void;
        const read = new Promise<void>((resolve) => {
          settled = resolve;
        });
        browses.begin(name, read);
        setCatalog(name, { status: "loading" });
        try {
          const resp = await client.request("evener/marketplace/browse", { name });
          if (!browses.current(name, revision)) return;
          setCatalog(name, { status: "loaded", description: resp.description, plugins: resp.plugins });
        } catch (err) {
          // Fenced the same way a success is: an error from a catalog that has
          // since been retired says nothing about the one that replaced it.
          if (!browses.current(name, revision)) return;
          setCatalog(name, { status: "error", error: errorText(err) });
        } finally {
          settled(browses.settle(name, read));
        }
      },

      reloadCatalog(name) {
        set((s) => ({ browseCatalogs: retireBrowseCatalogs(s.browseCatalogs, [name]) }));
        return get().browseMarketplace(name);
      },
    };
  });

  return attachLifecycle(store, lifecycle);
}
