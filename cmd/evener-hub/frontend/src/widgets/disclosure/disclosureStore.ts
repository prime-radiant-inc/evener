// disclosureStore holds the open/closed state of every Disclosure keyed by a
// stable id, so a disclosure's expansion survives the remount that kills a
// component-local useState: VirtualList unmounts off-window transcript rows and
// dockview unmounts the whole pane tree on a layout change (yt2q). Mirrors
// subagentModuleStore.ts's own singleton createStore/useStore idiom (note the
// split import: useStore from "zustand", createStore from "zustand/vanilla").
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

interface DisclosureState {
  /** Explicit reader choices. Defaults are kept separately so a display
   * configuration cannot fight a manual toggle on every render. */
  open: Map<string, boolean>;
  baselines: Map<string, DisclosureBaseline>;
}

interface DisclosureBaseline {
  open: boolean;
  ids: Set<string>;
}

const SCOPE_SEPARATOR = "\0";

/** The store's stable key for a disclosure inside a render scope. */
export function scopedDisclosureId(scope: string, id: string): string {
  return `${scope}${SCOPE_SEPARATOR}${id}`;
}

const store = createStore<DisclosureState>(() => ({ open: new Map(), baselines: new Map() }));

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

/**
 * Establish the default posture for the eligible ids in one render scope.
 *
 * A transition from a closed baseline to an open one is the Full-level
 * baseline boundary. Only that boundary clears old explicit closed choices;
 * repeated calls with the same open baseline merely refresh the eligible-id
 * inventory and therefore cannot re-open a row the reader just collapsed.
 */
export function beginDisclosureBaseline(scope: string, ids: readonly string[], open: boolean): void {
  store.setState((state) => {
    const previous = state.baselines.get(scope);
    const enteringOpenBaseline = open && previous?.open !== true;
    const nextOpen = new Map(state.open);
    if (enteringOpenBaseline) {
      for (const id of ids) nextOpen.delete(scopedDisclosureId(scope, id));
    }
    const baselines = new Map(state.baselines);
    baselines.set(scope, { open, ids: new Set(ids) });
    return { open: nextOpen, baselines };
  });
}

/** Resolve a configuration default without replacing an explicit choice. */
export function disclosureDefault(scope: string, id: string, fallback: boolean): boolean {
  const baseline = store.getState().baselines.get(scope);
  if (baseline?.open === true) return true;
  if (baseline?.ids.has(id)) return baseline.open;
  return fallback;
}

/** Remove all explicit choices and baseline defaults belonging to a scope. */
export function clearDisclosureScope(scope: string): void {
  store.setState((state) => {
    const prefix = `${scope}${SCOPE_SEPARATOR}`;
    const open = new Map(state.open);
    for (const id of open.keys()) if (id.startsWith(prefix)) open.delete(id);
    const baselines = new Map(state.baselines);
    baselines.delete(scope);
    return { open, baselines };
  });
}

export function resetDisclosureStoreForTests(): void {
  store.setState({ open: new Map(), baselines: new Map() });
}
