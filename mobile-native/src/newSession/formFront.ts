// Which New session form is in front for a hub's creation store (#3104):
// view state the store's owner (creations.ts) doesn't hold. When a start
// lands, the form in front then (perhaps a sheet reopened meanwhile) opens the
// session or shows why it failed.
import type { NativeStackNavigationProp } from "@react-navigation/native-stack";
import type { NewSessionRoutes, NewSessionStore } from "./newSessionContext";

/** A form showing a store. With none in front when a start lands, an alert
 * says what happened. */
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
