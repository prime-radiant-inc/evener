// New session's one owner per hub for a start (#3104). A start keeps running
// after its sheet closes, so the creation store belongs to the hub rather than
// to one sheet: a sheet reopened mid-start shows the same form, still
// starting, and can't start it twice. Each client gets one service, so a sheet
// reopened on the same connection rebinds nothing and a start in flight
// survives; a new connection still rebinds, which leaves that start uncertain.
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
