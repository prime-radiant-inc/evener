import { isDisclosureOpenIn, scopedDisclosureId } from "@evener/appwire-client";
import { expect, it, vi } from "vitest";
import { nativeDisclosureStore, toggleDisclosure } from "./nativeDisclosure";

// The hook is useStore(nativeDisclosureStore, isDisclosureOpenIn(...)), so
// what a rendered TimelineItem sees is the store's snapshot through that
// selector, notified through subscribe: the two things asserted here. (The
// native test install has no React renderer to mount the hook itself.)
it("toggleDisclosure flips the app's own store, notifying subscribers with the snapshot the hook selects from", () => {
	const id = scopedDisclosureId(
		JSON.stringify(["hub", "ref"]),
		JSON.stringify(["activity", "item-1"]),
	);
	const listener = vi.fn();
	const unsubscribe = nativeDisclosureStore.subscribe(listener);
	expect(isDisclosureOpenIn(nativeDisclosureStore.getState(), id, false)).toBe(
		false,
	);

	toggleDisclosure(id, false);
	expect(listener).toHaveBeenCalledTimes(1);
	expect(isDisclosureOpenIn(nativeDisclosureStore.getState(), id, false)).toBe(
		true,
	);

	toggleDisclosure(id, false);
	expect(isDisclosureOpenIn(nativeDisclosureStore.getState(), id, true)).toBe(
		false,
	);
	unsubscribe();
	nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
});
