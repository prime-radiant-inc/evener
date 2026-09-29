// archivedList.ts — one project's archived sessions, read a page at a time
// from evener/archived/list. Archived rows are not part of navigation: the
// list has no revisions or invalidations, so this store refetches when a fold
// opens, on an explicit refresh, and after an action that can change the
// project's archived rows (archive, unarchive, pin, delete).
//
// Lists are keyed by catalog and project key, because one project key can
// exist in two catalogs.

import type { ArchivedListParams, NavigationSessionSummary } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { decodeArchivedListSessions } from "@evener/appwire-client/state/navigation";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { connectedClientPort } from "./connection";

export const ARCHIVED_LIST_CATALOGS = ["projects", "archived_projects", "test_runs"] as const;
export type ArchivedListCatalog = (typeof ARCHIVED_LIST_CATALOGS)[number];

export interface ArchivedList {
  rows: NavigationSessionSummary[];
  /** The cursor for the next page; absent on the last page. */
  nextCursor?: string;
  /** Every archived session of the project, loaded or not. */
  total: number;
  loading: boolean;
  /** Non-null when the most recent request failed. Loaded rows are kept. */
  error: string | null;
}

export interface ArchivedListState {
  lists: Record<string, ArchivedList>;
}

export function archivedListKey(catalog: ArchivedListCatalog, projectKey: string): string {
  return `${catalog}|${projectKey}`;
}

export const archivedListStore = createStore<ArchivedListState>(() => ({ lists: {} }));

const { requireClient } = connectedClientPort("archivedList");

// generations counts requests per list, so a response from a request that a
// newer one has overtaken (a load-more answered after a refresh) is dropped.
const generations = new Map<string, number>();

const emptyList: ArchivedList = { rows: [], total: 0, loading: false, error: null };

function patch(key: string, change: (list: ArchivedList) => Partial<ArchivedList>): void {
  archivedListStore.setState((state) => {
    const list = state.lists[key] ?? emptyList;
    return { lists: { ...state.lists, [key]: { ...list, ...change(list) } } };
  });
}

async function fetchPage(catalog: ArchivedListCatalog, projectKey: string, cursor: string | undefined): Promise<void> {
  const key = archivedListKey(catalog, projectKey);
  const generation = (generations.get(key) ?? 0) + 1;
  generations.set(key, generation);
  patch(key, () => ({ loading: true, error: null }));
  const params: ArchivedListParams = { catalog, projectKey, ...(cursor ? { cursor } : {}) };
  try {
    const response = await requireClient().request("evener/archived/list", params);
    if (generations.get(key) !== generation) return;
    const rows = decodeArchivedListSessions(response.sessions);
    patch(key, (list) => ({
      rows: cursor ? [...list.rows, ...rows] : rows,
      nextCursor: response.nextCursor,
      total: response.total,
      loading: false,
    }));
  } catch (err) {
    if (generations.get(key) !== generation) return;
    patch(key, () => ({ loading: false, error: errorText(err) }));
  }
}

/** Fetches the first page of the project's archived list, replacing any rows
 * already loaded. Opening a fold and refreshing it are the same request. */
export function refreshArchivedList(catalog: ArchivedListCatalog, projectKey: string): Promise<void> {
  return fetchPage(catalog, projectKey, undefined);
}

/** Appends the next page; does nothing on the last page or before the first. */
export function loadMoreArchivedList(catalog: ArchivedListCatalog, projectKey: string): Promise<void> {
  const cursor = archivedListStore.getState().lists[archivedListKey(catalog, projectKey)]?.nextCursor;
  if (!cursor) return Promise.resolve();
  return fetchPage(catalog, projectKey, cursor);
}

/** Refreshes every loaded list of the project, in any catalog: an action on
 * one of its sessions can move rows in or out of its archived tier. */
export async function refreshArchivedListsForProject(projectKey: string): Promise<void> {
  const { lists } = archivedListStore.getState();
  const loaded = ARCHIVED_LIST_CATALOGS.filter((catalog) => lists[archivedListKey(catalog, projectKey)]);
  await Promise.all(loaded.map((catalog) => refreshArchivedList(catalog, projectKey)));
}

export function useArchivedList(catalog: ArchivedListCatalog, projectKey: string): ArchivedList | undefined {
  return useStore(archivedListStore, (state) => state.lists[archivedListKey(catalog, projectKey)]);
}

// Test-only.
export function resetArchivedListStoreForTests(): void {
  generations.clear();
  archivedListStore.setState({ lists: {} });
}
