// The app's one disclosure store: the package's framework-free factory bound
// to zustand's useStore for the reactive read. A disclosure's expansion lives
// here rather than in component-local useState so it survives the remount
// that VirtualList (off-window transcript rows) and dockview (a layout change
// unmounting the pane tree) inflict (yt2q). Tests that need a clean slate call
// resetDisclosureStoreForTests; Vitest file isolation keeps this instance from
// crossing files.
import { createDisclosureStore, type DisclosureReadOptions, isDisclosureOpenIn } from "@evener/appwire-client";
import { useStore } from "zustand";

const store = createDisclosureStore();

/** Reactive: re-renders the caller when this id's open state changes. This IS
 * a custom hook (it rides zustand's useStore, exactly as useSubagentRows does);
 * it is only ever called at the top of a component's render (Disclosure). Its
 * name follows the boolean-predicate shape the interface specifies rather than
 * a use- prefix, so biome's hook-name heuristic can't recognize it as a hook. */
export function isDisclosureOpen(id: string, fallback: boolean, options?: DisclosureReadOptions): boolean {
  // biome-ignore lint/correctness/useHookAtTopLevel: custom hook wrapping useStore; called unconditionally at the top of Disclosure's render, only the non-use- name defeats the heuristic
  return useStore(store, (s) => isDisclosureOpenIn(s, id, fallback, options));
}

export const setDisclosureOpen = store.setOpen;
export const toggleDisclosure = store.toggle;
export const beginDisclosureBaseline = store.beginBaseline;
export const disclosureDefault = store.defaultFor;
export const clearDisclosureScope = store.clearScope;

export function resetDisclosureStoreForTests(): void {
  store.setState(store.getInitialState());
}
