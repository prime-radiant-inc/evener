// Opening a document from outside its session (the Board's Continue reading,
// spec 7.1): the session first, then the Reader over it, so Back from the
// Reader lands in the session, as when the document is opened from there.
import type { NativeStackNavigationProp } from "@react-navigation/native-stack";
import type { Routes } from "../screens";

export type ReaderParams = Routes["Reader"];

export function openDocumentInSession(
	navigation: Pick<NativeStackNavigationProp<Routes>, "push">,
	params: ReaderParams,
): void {
	navigation.push("Conversation", { hubId: params.hubId, ref: params.sessionRef, title: params.sessionTitle });
	navigation.push("Reader", params);
}
