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

let draftStorage: DraftStorage | undefined;
const stores = perHub((hubId) => createNewSessionStore(hubId, draftStorage));
const services = new WeakMap<ConversationClientLike, NewSessionService>();

/** The hub's creation store, made on first use with `storage` and kept until
 * the hub is removed. */
export function creationStore(hubId: string, storage: DraftStorage): NewSessionStore {
	draftStorage = storage;
	return stores.get(hubId);
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

/** A removed hub's store goes, retired, so nothing it was doing lands or
 * says anything. */
export function forgetCreationForHub(hubId: string): void {
	stores.forget(hubId)?.getState().retire();
}

/** The form showing a store: when a start lands, the form in front then
 * (perhaps a sheet reopened meanwhile) opens the session or shows why it
 * failed, and with none in front an alert says so. */
export interface FormFront {
	navigation: Pick<NativeStackNavigationProp<NewSessionRoutes, "Form">, "isFocused" | "getParent">;
	latest: { current: { ready: boolean; client: unknown } };
}

const fronts = new WeakMap<NewSessionStore, FormFront>();

/** Makes `front` the store's form in front, newest first, until the returned
 * release runs. */
export function showForm(store: NewSessionStore, front: FormFront): () => void {
	fronts.set(store, front);
	return () => {
		if (fronts.get(store) === front) fronts.delete(store);
	};
}

export function formFront(store: NewSessionStore): FormFront | undefined {
	return fronts.get(store);
}
