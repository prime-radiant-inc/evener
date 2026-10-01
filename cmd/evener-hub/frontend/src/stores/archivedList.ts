// archivedList.ts is the web's instance of the package's archived list store
// (createArchivedListStore): each project's archived sessions, read a page at
// a time from evener/archived/list. The rail refetches a list when it sees
// the navigation archived count move to a total the list does not hold, and
// after an action that can change the project's archived rows (archive,
// unarchive, pin, delete). Paging, overtaking and the failed-refresh-keeps-
// rows rule live in the package.
//
// The client port resolves connectionStore's CURRENT client at request time,
// and a replaced or recovered connection resets the store, so no list
// outlives the connection that served it.

import {
  type ArchivedList,
  type ArchivedListCatalog,
  type ArchivedListState,
  archivedListKey,
  createArchivedListStore,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { connectedClientPort, onConnectionReplacedOrRecovered } from "./connection";

export type { ArchivedList, ArchivedListCatalog, ArchivedListState };
export { archivedListKey };

export const archivedListStore = createArchivedListStore({ request: connectedClientPort("archivedList").request });

// A new connection may serve another hub's rows, or rows that changed while
// this one was away.
onConnectionReplacedOrRecovered(() => archivedListStore.reset());

export const refreshArchivedList = archivedListStore.refresh;
export const loadMoreArchivedList = archivedListStore.loadMore;
export const refreshLoadedArchivedLists = archivedListStore.refreshLoaded;

export function useArchivedList(catalog: ArchivedListCatalog, projectKey: string): ArchivedList | undefined {
  return useStore(archivedListStore, (state) => state.lists[archivedListKey(catalog, projectKey)]);
}

export function useArchivedLists(): Record<string, ArchivedList> {
  return useStore(archivedListStore, (state) => state.lists);
}

// Test-only.
export function resetArchivedListStoreForTests(): void {
  archivedListStore.reset();
}
