// Opening a document from outside its session (the Board's Continue reading,
// spec 7.1): the session first, then the Reader over it, so Back from the
// Reader lands in the session, as when the document is opened from there.
// The caller supplies the captured reference, not a path to rebind on opening.
import type { NativeStackNavigationProp } from "@react-navigation/native-stack";
import type { Routes } from "../screens";
import { resolveOwnerIdentity } from "../session/resolveOwnerIdentity";

export type ReaderParams = Routes["Reader"];

export function openDocumentInSession(
	navigation: Pick<NativeStackNavigationProp<Routes>, "push">,
	params: ReaderParams,
): void {
	navigation.push("Conversation", {
		hubId: params.hubId,
		ref: resolveOwnerIdentity({
			hubId: params.hubId,
			documentRef: params.sessionRef,
			openedMessageRef: params.messageSessionRef ?? params.sessionRef,
			currentRef: params.messageSessionRef ?? params.sessionRef,
		}).current(),
		title: params.sessionTitle,
	});
	navigation.push("Reader", params);
}
