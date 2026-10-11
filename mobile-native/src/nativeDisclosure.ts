// The native app's one disclosure store: the package's framework-free factory
// bound to zustand's useStore for the reactive read, so a timeline row's
// expansion survives the remount a list re-render inflicts. The instance is
// exported for the adapter's own test; screens go through the hook and the
// toggle.
import { createDisclosureStore, isDisclosureOpenIn } from "@evener/appwire-client";
import { useStore } from "zustand";

export const nativeDisclosureStore = createDisclosureStore();

/** Reactive: re-renders the caller when this id's open state changes. */
export function useDisclosureOpen(id: string, fallback: boolean): boolean {
	return useStore(nativeDisclosureStore, (s) => isDisclosureOpenIn(s, id, fallback));
}

/** Reactive, for a disclosure stored under several ids (a run, under each of
 * its steps): the latest explicit choice among them, else the first id's
 * default. */
export function useDisclosureOpenAmong(ids: readonly string[], fallback: boolean): boolean {
	return useStore(nativeDisclosureStore, (s) => {
		let latest: { open: boolean; revision: number } | undefined;
		for (const id of ids) {
			const choice = s.open.get(id);
			if (choice && (latest === undefined || choice.revision > latest.revision)) latest = choice;
		}
		return latest?.open ?? isDisclosureOpenIn(s, ids[0] ?? "", fallback);
	});
}

/** Records one choice under every id a disclosure is stored under. */
export function setDisclosureOpenAll(ids: readonly string[], open: boolean): void {
	for (const id of ids) nativeDisclosureStore.setOpen(id, open);
}

export const toggleDisclosure = nativeDisclosureStore.toggle;
