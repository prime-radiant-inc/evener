// disclosureStore holds the open/closed state of every Disclosure keyed by a
// stable id, so a disclosure's expansion survives the remount that kills a
// component-local useState: VirtualList unmounts off-window transcript rows and
// dockview unmounts the whole pane tree on a layout change (yt2q). Mirrors
// subagentModuleStore.ts's own singleton createStore/useStore idiom (note the
// split import: useStore from "zustand", createStore from "zustand/vanilla").
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

interface DisclosureState {
  open: Map<string, boolean>;
}

const store = createStore<DisclosureState>(() => ({ open: new Map() }));

/** Reactive: re-renders the caller when this id's open state changes. This IS
 * a custom hook (it rides zustand's useStore, exactly as useSubagentRows does);
 * it is only ever called at the top of a component's render (Disclosure). Its
 * name follows the boolean-predicate shape the interface specifies rather than
 * a use- prefix, so biome's hook-name heuristic can't recognize it as a hook. */
export function isDisclosureOpen(id: string, fallback: boolean): boolean {
  // biome-ignore lint/correctness/useHookAtTopLevel: custom hook wrapping useStore; called unconditionally at the top of Disclosure's render, only the non-use- name defeats the heuristic
  return useStore(store, (s) => s.open.get(id) ?? fallback);
}

export function setDisclosureOpen(id: string, open: boolean): void {
  store.setState((s) => {
    const next = new Map(s.open);
    next.set(id, open);
    return { open: next };
  });
}

export function toggleDisclosure(id: string, fallback: boolean): void {
  const current = store.getState().open.get(id) ?? fallback;
  setDisclosureOpen(id, !current);
}

export function resetDisclosureStoreForTests(): void {
  store.setState({ open: new Map() });
}
