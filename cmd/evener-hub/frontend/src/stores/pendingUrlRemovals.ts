import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

// The in-flight URL-removal guard has to outlive the notes panel. The mobile
// sheet unmounts the body while a request is still in flight, so a guard held in
// component state resets on remount and lets a duplicate request through; and a
// pane reused across sessions would carry one session's pending id into the
// next. The guard therefore lives here, keyed by the session and the entry id
// (roborev's seventh round asked for exactly that).
//
// beginUrlRemoval is the synchronous half: the caller checks and marks in one
// step, before any render, which is what two clicks inside one tick need.

const pending = createStore<{ bySession: Map<string, ReadonlySet<string>> }>(() => ({
  bySession: new Map(),
}));

const EMPTY: ReadonlySet<string> = new Set();

/** beginUrlRemoval marks (sessionRef, urlID) pending; false when it already is. */
export function beginUrlRemoval(sessionRef: string, urlID: string): boolean {
  let started = false;
  pending.setState((state) => {
    const current = state.bySession.get(sessionRef) ?? EMPTY;
    if (current.has(urlID)) return state;
    const next = new Set(current);
    next.add(urlID);
    const bySession = new Map(state.bySession);
    bySession.set(sessionRef, next);
    started = true;
    return { bySession };
  });
  return started;
}

/** endUrlRemoval releases the guard; safe when nothing is pending. */
export function endUrlRemoval(sessionRef: string, urlID: string): void {
  pending.setState((state) => {
    const current = state.bySession.get(sessionRef);
    if (!current?.has(urlID)) return state;
    const next = new Set(current);
    next.delete(urlID);
    const bySession = new Map(state.bySession);
    if (next.size === 0) bySession.delete(sessionRef);
    else bySession.set(sessionRef, next);
    return { bySession };
  });
}

/** usePendingUrlRemovals returns the ids pending for one session's rows. */
export function usePendingUrlRemovals(sessionRef: string): ReadonlySet<string> {
  return useStore(pending, (state) => state.bySession.get(sessionRef) ?? EMPTY);
}

/** resetPendingUrlRemovals clears the store; tests call it between cases. */
export function resetPendingUrlRemovals(): void {
  pending.setState({ bySession: new Map() });
}
