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
	return useStore(nativeDisclosureStore, (s) =>
		isDisclosureOpenIn(s, id, fallback),
	);
}

export const toggleDisclosure = nativeDisclosureStore.toggle;
