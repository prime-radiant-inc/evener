// A project's archived sessions as a page source. Navigation v3 serves no
// archived rows: they come from evener/archived/list through the connection's
// archived list store (archivedLists.ts). The list has no revisions of its
// own: it is read when a screen opens it unloaded or starts following the
// hub with it loaded, again when the hub announces its project changed (once
// shown, if it was out of view), after any accepted organize change
// (navigationActions.ts, and the Conversation screen's own Archive and Undo),
// and from the top once its connection recovers.
import {
	type ArchivedList,
	type ArchivedListCatalog,
	type ArchivedListStore,
	archivedListKey,
	type NavigationInvalidationTarget,
	type NavigationSessionSummary,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { archivedListStoreFor } from "./archivedLists";
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
	private readonly store: ArchivedListStore;
	private list: ArchivedList | undefined;
	private snapshot = pageState(undefined);
	private paused = false;
	/** The hub announced a change while the list was paused. */
	private owed = false;
	constructor(
		private readonly client: ConversationClientLike,
		/** Undefined reads the catalog holding the project now (a session's
		 * location names none). */
		private readonly catalog: ArchivedListCatalog | undefined,
		private readonly projectKey: string,
	) {
		this.store = archivedListStoreFor(client);
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
	/** An explicit read means the list is shown again, and stands in for any
	 * read owed while it was paused. */
	refresh() {
		this.paused = false;
		this.owed = false;
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
	/** Reads the list again when the hub announces a change to this project's
	 * pages (a session archived on another device, or one ageing into the
	 * archived tier); the hub's navigation names no archived list itself. A
	 * list already loaded is read again now: nothing followed the hub for it
	 * since the view that loaded it closed. */
	watch() {
		if (this.getSnapshot().loaded) this.follow();
		return this.client.onNotification((event) => {
			if (event.method === "evener/navigation/invalidated" && event.params.targets.some(this.names)) this.follow();
		});
	}
	private names = (target: NavigationInvalidationTarget) =>
		target.kind === "all_loaded_projects" || (target.kind === "project" && target.projectKey === this.projectKey);
	/** A read on the hub's behalf: owed until resume while paused, and none
	 * on a connection that isn't ready, which would reject it (once ready, it
	 * has dropped every list, which its view reads afresh). */
	private follow() {
		if (this.paused) this.owed = true;
		else if (this.client.state === "ready") {
			this.owed = false;
			void this.store.refresh(this.catalog, this.projectKey);
		}
	}
	/** Out of view, the list reads nothing on the hub's behalf; what it owes
	 * is read on resume. A read already out lands. */
	cancel() {
		this.paused = true;
	}
	resume() {
		this.paused = false;
		if (this.owed) this.follow();
	}
}
