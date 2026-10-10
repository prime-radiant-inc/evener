// The archived sessions of the projects an app opens, each read a page at a
// time from evener/archived/list. Archived rows are not part of navigation:
// the list has no invalidations, so the host refreshes a list when it has
// reason to think the rows moved (the navigation archived count changed, or
// an archive, unarchive, pin, unpin or delete ran). A list does carry a
// revision: a refresh sends the one the store holds, and a list the hub
// answers unchanged is not read again. The host resets
// the store when its connection is replaced or recovers, so no list outlives
// the connection that served it.
//
// Lists are keyed by catalog and project key, because one project key can
// exist in two catalogs. A caller that knows only the project key (an older
// hub's location, which names no catalog) names none, and the hub reads the
// catalog that holds the project now; that is a list of its own. A framework-free store
// over a request-only client port; each app wraps one for its own view layer.

import type { AppwireClient } from "./client";
import { errorText } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { decodeArchivedListSessions } from "./state/navigation/codec";
import type { ArchivedListParams, NavigationCatalogs, NavigationSessionSummary } from "./types.gen";

export type ArchivedListClient = Pick<AppwireClient, "request">;

export type ArchivedListCatalog = keyof NavigationCatalogs;

export interface ArchivedList {
  rows: NavigationSessionSummary[];
  /** The cursor for the next page; absent on the last page. */
  nextCursor?: string;
  /** Every archived session of the project, loaded or not. */
  total: number;
  /** Whether a page has arrived. Until one does, total is unknown. */
  loaded: boolean;
  loading: boolean;
  /** Non-null when the most recent request failed. Loaded rows are kept. */
  error: string | null;
  /** The hub's revision of the whole list, held only while every loaded page
   * carried it (pages read at different revisions vouch for none); a refresh
   * sends it and keeps the rows when the hub answers unchanged. Absent from
   * an older hub. */
  revision?: string;
}

export interface ArchivedListState {
  lists: Record<string, ArchivedList>;
}

export interface ArchivedListStore extends FrameworkFreeStore<ArchivedListState> {
  /** Reloads the project's archived list from its first page, through as
   * many pages as the list already held (at least one), so a refresh does not
   * undo the user's paging. The rows are swapped in once, when the last page
   * has arrived. Opening a list and refreshing it are the same request. */
  refresh(catalog: ArchivedListCatalog | undefined, projectKey: string): Promise<void>;
  /** Appends the next page; does nothing on the last page or before the first. */
  loadMore(catalog: ArchivedListCatalog | undefined, projectKey: string): Promise<void>;
  /** Refreshes every loaded list: an archive, unarchive, pin, unpin or delete
   * can move rows in or out of a project's archived tier. A list whose
   * revision the hub says is unchanged costs one empty answer. */
  refreshLoaded(): Promise<void>;
  /** Drops every list; an answer a request still owes lands nowhere. */
  reset(): void;
}

// archivedListKey is an encoded pair, so a project key holding any character
// still parses back (refreshLoaded). No catalog encodes as null.
export function archivedListKey(catalog: ArchivedListCatalog | undefined, projectKey: string): string {
  return JSON.stringify([catalog ?? null, projectKey]);
}

const emptyList: ArchivedList = { rows: [], total: 0, loaded: false, loading: false, error: null };

// revisionAcross is the revision a list holds after adding a page: the one it
// held, only if the page carried it too.
function revisionAcross(held: string | undefined, page: string | undefined): string | undefined {
  return held === page ? held : undefined;
}

export function createArchivedListStore(client: ArchivedListClient): ArchivedListStore {
  // Every request takes a new generation from one counter, and generations
  // holds each list's newest, so an answer from a request that has been
  // overtaken (a load-more answered after a refresh) or that was still owed
  // when the store reset is dropped.
  let lastGeneration = 0;
  const generations = new Map<string, number>();
  const store = createFrameworkFreeStore<ArchivedListState>(() => ({ lists: {} }));

  function patch(key: string, change: (list: ArchivedList) => Partial<ArchivedList>): void {
    store.setState((state) => {
      const list = state.lists[key] ?? emptyList;
      return { lists: { ...state.lists, [key]: { ...list, ...change(list) } } };
    });
  }

  // startRequest moves the list's generation on, so any request already in
  // flight for it is dropped, and returns a check for whether this one is
  // still the newest.
  function startRequest(key: string): () => boolean {
    const generation = ++lastGeneration;
    generations.set(key, generation);
    patch(key, () => ({ loading: true, error: null }));
    return () => generations.get(key) === generation;
  }

  async function requestPage(
    catalog: ArchivedListCatalog | undefined,
    projectKey: string,
    cursor: string | undefined,
    revision?: string,
  ) {
    const params: ArchivedListParams = {
      ...(catalog ? { catalog } : {}),
      projectKey,
      ...(cursor ? { cursor } : {}),
      ...(revision ? { revision } : {}),
    };
    const response = await client.request("evener/archived/list", params);
    return {
      rows: decodeArchivedListSessions(response.sessions),
      nextCursor: response.nextCursor,
      total: response.total,
      revision: response.revision,
      unchanged: response.unchanged === true,
    };
  }

  async function refresh(catalog: ArchivedListCatalog | undefined, projectKey: string): Promise<void> {
    const key = archivedListKey(catalog, projectKey);
    const held = store.getState().lists[key];
    const wanted = held?.rows.length ?? 0;
    const isNewest = startRequest(key);
    try {
      let page = await requestPage(catalog, projectKey, undefined, held?.revision);
      if (page.unchanged) {
        if (isNewest()) patch(key, () => ({ total: page.total, loading: false }));
        return;
      }
      let revision = page.revision;
      const rows = [...page.rows];
      while (isNewest() && rows.length < wanted && page.nextCursor) {
        page = await requestPage(catalog, projectKey, page.nextCursor);
        rows.push(...page.rows);
        revision = revisionAcross(revision, page.revision);
      }
      if (!isNewest()) return;
      patch(key, () => ({
        rows,
        nextCursor: page.nextCursor,
        total: page.total,
        loaded: true,
        loading: false,
        revision,
      }));
    } catch (err) {
      if (!isNewest()) return;
      patch(key, () => ({ loading: false, error: errorText(err) }));
    }
  }

  async function loadMore(catalog: ArchivedListCatalog | undefined, projectKey: string): Promise<void> {
    const key = archivedListKey(catalog, projectKey);
    const cursor = store.getState().lists[key]?.nextCursor;
    if (!cursor) return;
    const isNewest = startRequest(key);
    try {
      const page = await requestPage(catalog, projectKey, cursor);
      if (!isNewest()) return;
      patch(key, (list) => ({
        rows: [...list.rows, ...page.rows],
        nextCursor: page.nextCursor,
        total: page.total,
        loaded: true,
        loading: false,
        revision: revisionAcross(list.revision, page.revision),
      }));
    } catch (err) {
      if (!isNewest()) return;
      patch(key, () => ({ loading: false, error: errorText(err) }));
    }
  }

  async function refreshLoaded(): Promise<void> {
    await Promise.all(
      Object.keys(store.getState().lists).map((key) => {
        const [catalog, projectKey] = JSON.parse(key) as [ArchivedListCatalog | null, string];
        return refresh(catalog ?? undefined, projectKey);
      }),
    );
  }

  function reset(): void {
    generations.clear();
    store.setState({ lists: {} });
  }

  return { ...store, refresh, loadMore, refreshLoaded, reset };
}
