// A project's archived sessions as a page source. Navigation v3 serves no
// archived rows: they come from evener/archived/list through the shared
// package's archived list store, one per connection. The list has no
// revisions and no invalidations, so it re-reads when a screen refreshes it
// and after any organize action lands (navigationActions.ts).
import {
	type ArchivedList,
	type ArchivedListCatalog,
	type ArchivedListStore,
	archivedListKey,
	createArchivedListStore,
	type NavigationSessionSummary,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type { PageSource, PageState } from "./navigationPages";

const stores = new WeakMap<ConversationClientLike, ArchivedListStore>();

/** The connection's archived list store. A new connection gets a new store,
 * so no list outlives the connection that served it. */
export function archivedListStoreFor(client: ConversationClientLike): ArchivedListStore {
	let store = stores.get(client);
	if (!store) {
		store = createArchivedListStore(client);
		stores.set(client, store);
	}
	return store;
}

function pageState(list: ArchivedList | undefined): PageState<NavigationSessionSummary> {
	if (!list) return { loaded: false, rows: [], remaining: 0, loading: false, error: null, stale: false };
	return {
		loaded: list.loaded,
		rows: list.rows,
		// Only a cursor says another page exists.
		remaining: list.nextCursor ? Math.max(1, list.total - list.rows.length) : 0,
		loading: list.loading,
		error: list.error,
		stale: false,
	};
}

export class ArchivedPages implements PageSource<NavigationSessionSummary> {
	readonly navigationVersioned = false;
	private readonly key: string;
	private list: ArchivedList | undefined;
	private snapshot: PageState<NavigationSessionSummary>;
	constructor(
		private readonly store: ArchivedListStore,
		private readonly catalog: ArchivedListCatalog,
		private readonly projectKey: string,
	) {
		this.key = archivedListKey(catalog, projectKey);
		this.list = store.getState().lists[this.key];
		this.snapshot = pageState(this.list);
	}
	/** One snapshot until this project's list changes, as a view binding needs. */
	getSnapshot = () => {
		const list = this.store.getState().lists[this.key];
		if (list !== this.list) {
			this.list = list;
			this.snapshot = pageState(list);
		}
		return this.snapshot;
	};
	getResourceVersion = () => null;
	subscribe = (listener: () => void) =>
		this.store.subscribe((state, previous) => {
			if (state.lists[this.key] !== previous.lists[this.key]) listener();
		});
	watch = () => () => {};
	refresh = () => this.store.refresh(this.catalog, this.projectKey);
	refreshAfter = () => this.refresh();
	more = () => this.store.loadMore(this.catalog, this.projectKey);
	cancel() {}
	resume() {}
}
