// A project's archived sessions as a page source. Navigation v3 serves no
// archived rows: they come from evener/archived/list through the connection's
// archived list store (archivedLists.ts). The list has no revisions and no
// invalidations: it is read when a screen opens it unloaded, again after any
// accepted organize change (navigationActions.ts, and the Conversation
// screen's own Archive and Undo), and from the top once its connection
// recovers.
import {
	type ArchivedList,
	type ArchivedListCatalog,
	type ArchivedListStore,
	archivedListKey,
	type NavigationSessionSummary,
} from "@evener/appwire-client";
import type { PageSource, PageState } from "./navigationPages";

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
	private snapshot = pageState(undefined);
	constructor(
		private readonly store: ArchivedListStore,
		private readonly catalog: ArchivedListCatalog,
		private readonly projectKey: string,
	) {
		this.key = archivedListKey(catalog, projectKey);
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
	refresh() {
		return this.store.refresh(this.catalog, this.projectKey);
	}
	// An archived list is read from the hub's current navigation, which an
	// accepted change has already rebuilt, so the receipt adds nothing.
	refreshAfter() {
		return this.refresh();
	}
	more() {
		return this.store.loadMore(this.catalog, this.projectKey);
	}
	// The list follows no invalidations and holds no read to pause, so there
	// is nothing to watch, cancel or resume.
	watch() {
		return () => {};
	}
	cancel() {}
	resume() {}
}
