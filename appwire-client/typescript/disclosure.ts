// The disclosure store: a framework-free store holding the open/closed state
// of every Disclosure keyed by a stable id, so a row's expansion survives the
// remount that kills a component-local state (a virtual list unmounting
// off-window rows, a layout change unmounting the whole pane tree).
// createDisclosureStore is a factory - each app builds the one instance it
// wraps in its own view-layer hook, and tests build their own - returning the
// store triple plus the store-bound actions. Pure logic - no DOM, no React.

import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";

export interface DisclosureState {
  /** Explicit reader choices. Defaults are kept separately so a display
   * configuration cannot fight a manual toggle on every render. */
  readonly open: ReadonlyMap<string, ExplicitChoice>;
  readonly baselines: ReadonlyMap<string, DisclosureBaseline>;
  readonly revision: number;
}

export interface ExplicitChoice {
  readonly open: boolean;
  readonly revision: number;
  readonly baselineGeneration: number | undefined;
}

export interface DisclosureBaseline {
  readonly open: boolean;
  readonly ids: ReadonlySet<string>;
  readonly generation: number;
  readonly startedRevision: number;
}

export interface DisclosureStore extends FrameworkFreeStore<DisclosureState> {
  /** Whether this id is open: an explicit choice first, then its scope's
   * baseline, then the fallback. The reactive form is isDisclosureOpenIn
   * over a subscribed snapshot. */
  isOpen(id: string, fallback: boolean): boolean;
  setOpen(id: string, open: boolean): void;
  toggle(id: string, fallback: boolean): void;
  /**
   * Establish the default posture for the eligible ids in one render scope.
   *
   * A transition from a closed baseline to an open one is the Full-level
   * baseline boundary. Only that boundary clears old explicit closed choices;
   * repeated calls with the same open baseline merely refresh the eligible-id
   * inventory and therefore cannot re-open a row the reader just collapsed.
   */
  beginBaseline(scope: string, ids: readonly string[], open: boolean): void;
  /** Remove all explicit choices and baseline defaults belonging to a scope. */
  clearScope(scope: string): void;
  /** Resolve a configuration default without replacing an explicit choice. */
  defaultFor(scope: string, id: string, fallback: boolean): boolean;
}

const SCOPE_SEPARATOR = "\0";

/** The store's stable key for a disclosure inside a render scope. */
export function scopedDisclosureId(scope: string, id: string): string {
  return `${scope}${SCOPE_SEPARATOR}${id}`;
}

/** The open/closed answer for one id read off a state snapshot: the selector
 * each app's hook passes to its store binding. */
export function isDisclosureOpenIn(state: DisclosureState, id: string, fallback: boolean): boolean {
  const explicit = state.open.get(id);
  if (explicit !== undefined) return explicit.open;
  return baselineAnswer(baselineForId(state.baselines, id), id, fallback);
}

function baselineAnswer(baseline: DisclosureBaseline | undefined, id: string, fallback: boolean): boolean {
  if (baseline?.open === true) return true;
  if (baseline?.ids.has(id)) return baseline.open;
  return fallback;
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

/** Builds an empty disclosure store. */
export function createDisclosureStore(): DisclosureStore {
  const store = createFrameworkFreeStore<DisclosureState>(() => ({
    open: new Map(),
    baselines: new Map(),
    revision: 0,
  }));
  const { getState, setState } = store;

  const setOpen: DisclosureStore["setOpen"] = (id, open) => {
    setState((s) => {
      const next = new Map(s.open);
      const baseline = baselineForId(s.baselines, id);
      const revision = s.revision + 1;
      next.set(id, { open, revision, baselineGeneration: baseline?.generation });
      return { open: next, revision };
    });
  };

  return {
    ...store,

    isOpen: (id, fallback) => isDisclosureOpenIn(getState(), id, fallback),

    setOpen,

    toggle(id, fallback) {
      const current = getState().open.get(id)?.open ?? fallback;
      setOpen(id, !current);
    },

    beginBaseline(scope, ids, open) {
      setState((s) => {
        const previous = s.baselines.get(scope);
        const enteringOpenBaseline = open && previous?.open !== true;
        const generation = enteringOpenBaseline ? (previous?.generation ?? 0) + 1 : (previous?.generation ?? 0);
        const startedRevision = enteringOpenBaseline ? s.revision : (previous?.startedRevision ?? s.revision);
        const nextOpen = new Map(s.open);
        if (open) {
          for (const id of ids) {
            const key = scopedDisclosureId(scope, id);
            const choice = nextOpen.get(key);
            // A false value written before this Full baseline (or before a
            // newly eligible id joined it) is stale. A true value is an
            // explicit open and survives every baseline transition.
            if (choice?.open === false && choice.revision <= startedRevision) nextOpen.delete(key);
          }
        }
        const baselines = new Map(s.baselines);
        baselines.set(scope, { open, ids: new Set(ids), generation, startedRevision });
        return { open: nextOpen, baselines };
      });
    },

    clearScope(scope) {
      setState((s) => {
        const prefix = `${scope}${SCOPE_SEPARATOR}`;
        const open = new Map(s.open);
        for (const id of open.keys()) if (id.startsWith(prefix)) open.delete(id);
        const baselines = new Map(s.baselines);
        baselines.delete(scope);
        return { open, baselines };
      });
    },

    defaultFor: (scope, id, fallback) => baselineAnswer(getState().baselines.get(scope), id, fallback),
  };
}
