// New session's one owner per hub for a start (#3104).
//
// Why the store outlives the sheet: a start keeps running after its sheet is
// swiped away, and it has to land somewhere that can clear its draft and say
// what happened. With a store per sheet, a sheet reopened mid-start built a
// second store from the saved draft, so the start could be sent twice and the
// first one's landing deleted what was typed since. With one store per hub, a
// reopened sheet shows the same form, still starting, and can't start it again.
//
// Each client gets one service, so a sheet reopened on the same connection
// rebinds nothing and the start in flight survives. A store left bound to a
// client that has since gone is harmless: the request already sent settles or
// fails on that client, and the next sheet to open binds the current one,
// which leaves a start still out uncertain, as any new connection does.
import { createNewSessionService, type NewSessionService } from "../../../mobile/src/services/newSession";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { nativeDrafts } from "../nativeDrafts";
import { createNewSessionStore } from "../newSession";
import type { NewSessionStore } from "./newSessionContext";

const stores = new Map<string, NewSessionStore>();
/** Stores whose hub was removed: a start of theirs that lands has nothing to
 * say, since the hub is gone. */
const forgotten = new WeakSet<NewSessionStore>();
const services = new WeakMap<ConversationClientLike, NewSessionService>();

/** The hub's creation store, made on first use and kept until the hub is
 * removed. */
export function creationStore(hubId: string): NewSessionStore {
	let store = stores.get(hubId);
	if (!store) {
		store = createNewSessionStore(hubId, () => nativeDrafts().creation);
		stores.set(hubId, store);
	}
	return store;
}

/** The one New session service for this client. */
export function creationService(client: ConversationClientLike): NewSessionService {
	let service = services.get(client);
	if (!service) {
		service = createNewSessionService(client);
		services.set(client, service);
	}
	return service;
}

/** A removed hub's store goes, unbound, so nothing it was doing lands. */
export function forgetCreationForHub(hubId: string): void {
	const store = stores.get(hubId);
	if (!store) return;
	forgotten.add(store);
	store.getState().bind(null);
	stores.delete(hubId);
}

/** Whether this store's hub was removed. */
export function creationForgotten(store: NewSessionStore): boolean {
	return forgotten.has(store);
}
