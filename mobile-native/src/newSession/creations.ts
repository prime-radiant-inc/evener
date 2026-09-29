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
// rebinds nothing and the start in flight survives. When the connection moves
// on (another hub, a new connection, or none), ConnectionProvider releases
// every store still bound to the old client (releaseCreations): a start sent
// there reads as uncertain at once rather than starting forever, and Start
// holds that draft until it changes (startMayRepeat).
import type { NativeStackNavigationProp } from "@react-navigation/native-stack";
import { createNewSessionService, type NewSessionService } from "../../../mobile/src/services/newSession";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { perHub } from "../board/perHub";
import type { CreationDraftRepository } from "../creationDraftRepository";
import { createNewSessionStore } from "../newSession";
import type { NewSessionRoutes, NewSessionStore } from "./newSessionContext";

/** Where a hub's store keeps its draft: the device's draft storage in the
 * app (NewSessionSheet hands it in), a double in tests. Handed in rather than
 * imported, so the form can reach this module without loading native storage. */
export type DraftStorage = () => Pick<CreationDraftRepository, "read" | "write" | "clear">;

const stores = perHub((hubId, storage: DraftStorage) => createNewSessionStore(hubId, storage));
const services = new WeakMap<ConversationClientLike, NewSessionService>();
/** Each store bound to a client, and which. */
const bound = new Map<NewSessionStore, ConversationClientLike>();

/** The hub's creation store, made on first use with `storage`, which it
 * keeps, and kept until the hub is removed. */
export function creationStore(hubId: string, storage: DraftStorage): NewSessionStore {
	return stores.get(hubId, storage);
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

/** Binds the store to this client's service, or to none. */
export function bindCreation(store: NewSessionStore, client: ConversationClientLike | null): void {
	store.getState().bind(client ? creationService(client) : null);
	if (client) bound.set(store, client);
	else bound.delete(store);
}

/** The connection now runs on `client`, or on none (disconnected, another
 * hub, a new connection): every store still bound to another client lets go
 * of it, so a start sent there is known to be uncertain now, rather than
 * waiting on a client that may never answer. */
export function releaseCreations(client: ConversationClientLike | null): void {
	for (const [store, boundTo] of [...bound]) if (boundTo !== client) bindCreation(store, null);
}

/** A removed hub's store goes, retired, so nothing it was doing lands or
 * says anything. */
export function forgetCreationForHub(hubId: string): void {
	const store = stores.forget(hubId);
	if (!store) return;
	bound.delete(store);
	store.getState().retire();
}

/** The form showing a store: when a start lands, the form in front then
 * (perhaps a sheet reopened meanwhile) opens the session or shows why it
 * failed, and with none in front an alert says so. */
export interface FormFront {
	navigation: Pick<NativeStackNavigationProp<NewSessionRoutes, "Form">, "isFocused" | "getParent">;
	latest: { current: { ready: boolean; client: unknown } };
}

/** Each store's forms, oldest first: the last is in front. */
const fronts = new WeakMap<NewSessionStore, FormFront[]>();

/** Makes `front` the store's form in front until the returned release runs;
 * then the form under it, if any, is in front again. */
export function showForm(store: NewSessionStore, front: FormFront): () => void {
	fronts.set(store, [...(fronts.get(store) ?? []), front]);
	return () => {
		const rest = (fronts.get(store) ?? []).filter((shown) => shown !== front);
		if (rest.length > 0) fronts.set(store, rest);
		else fronts.delete(store);
	};
}

export function formFront(store: NewSessionStore): FormFront | undefined {
	return fronts.get(store)?.at(-1);
}
