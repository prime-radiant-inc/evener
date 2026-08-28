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
  open: Map<string, ExplicitChoice>;
  baselines: Map<string, DisclosureBaseline>;
  revision: number;
}

interface ExplicitChoice {
  open: boolean;
  revision: number;
  baselineGeneration: number | undefined;
}

interface DisclosureBaseline {
  open: boolean;
  ids: Set<string>;
  generation: number;
  startedRevision: number;
}

const SCOPE_SEPARATOR = "\0";

/** The store's stable key for a disclosure inside a render scope. */
export function scopedDisclosureId(scope: string, id: string): string {
  return `${scope}${SCOPE_SEPARATOR}${id}`;
}

const store = createStore<DisclosureState>(() => ({ open: new Map(), baselines: new Map(), revision: 0 }));

/** Reactive: re-renders the caller when this id's open state changes. This IS
 * a custom hook (it rides zustand's useStore, exactly as useSubagentRows does);
 * it is only ever called at the top of a component's render (Disclosure). Its
 * name follows the boolean-predicate shape the interface specifies rather than
 * a use- prefix, so biome's hook-name heuristic can't recognize it as a hook. */
export function isDisclosureOpen(id: string, fallback: boolean): boolean {
  // biome-ignore lint/correctness/useHookAtTopLevel: custom hook wrapping useStore; called unconditionally at the top of Disclosure's render, only the non-use- name defeats the heuristic
  return useStore(store, (s) => {
    const explicit = s.open.get(id);
    if (explicit !== undefined) return explicit.open;
    const baseline = baselineForId(s.baselines, id);
    if (baseline?.open === true) return true;
    if (baseline?.ids.has(id)) return baseline.open;
    return fallback;
  });
}

export function setDisclosureOpen(id: string, open: boolean): void {
  store.setState((s) => {
    const next = new Map(s.open);
    const baseline = baselineForId(s.baselines, id);
    const revision = s.revision + 1;
    next.set(id, {
      open,
      revision,
      baselineGeneration: baseline?.generation,
    });
    return { open: next, revision };
  });
}

export function toggleDisclosure(id: string, fallback: boolean): void {
  const current = store.getState().open.get(id)?.open ?? fallback;
  setDisclosureOpen(id, !current);
}

function baselineForId(baselines: ReadonlyMap<string, DisclosureBaseline>, id: string): DisclosureBaseline | undefined {
  let match: DisclosureBaseline | undefined;
  let matchLength = -1;
  for (const [scope, baseline] of baselines) {
    const prefixLength = scope.length + SCOPE_SEPARATOR.length;
    if (id.startsWith(`${scope}${SCOPE_SEPARATOR}`) && prefixLength > matchLength) {
      match = baseline;
      matchLength = prefixLength;
    }
  }
  return match;
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
    const generation = enteringOpenBaseline ? (previous?.generation ?? 0) + 1 : (previous?.generation ?? 0);
    const startedRevision = enteringOpenBaseline ? state.revision : (previous?.startedRevision ?? state.revision);
    const nextOpen = new Map(state.open);
    if (open) {
      for (const id of ids) {
        const key = scopedDisclosureId(scope, id);
        const choice = nextOpen.get(key);
        // A false value written before this Full baseline (or before a newly
        // eligible id joined it) is stale. A true value is an explicit open
        // and survives every baseline transition.
        if (choice?.open === false && choice.revision <= startedRevision) nextOpen.delete(key);
      }
    }
    const baselines = new Map(state.baselines);
    baselines.set(scope, { open, ids: new Set(ids), generation, startedRevision });
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
  store.setState({ open: new Map(), baselines: new Map(), revision: 0 });
}
