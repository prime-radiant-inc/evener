// The connection's archived list store: one per client, so a replaced
// connection starts with no lists. A client also reconnects in place, and a
// recovered connection may serve rows that changed while it was away, so its
// store drops every list then; a screen still showing one reads it again.
import { type ArchivedListStore, createArchivedListStore } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

const stores = new WeakMap<ConversationClientLike, ArchivedListStore>();

export function archivedListStoreFor(client: ConversationClientLike): ArchivedListStore {
	let store = stores.get(client);
	if (!store) {
		const created = createArchivedListStore(client);
		client.onReady(() => created.reset());
		stores.set(client, created);
		store = created;
	}
	return store;
}

/** Reads every archived list loaded on this connection again. Archived lists
 * follow no invalidations, and an accepted organize change can move rows in
 * or out of any project's archived tier. A connection with no store has no
 * list loaded. */
export function refreshLoadedArchivedLists(client: ConversationClientLike): void {
	void stores.get(client)?.refreshLoaded();
}
