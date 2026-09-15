import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { canWriteHumanNote } from "./humanNoteDrafts";
import { registerPanelStoreEvictor } from "./panelStoreEviction";
import { threadsStore } from "./threads";

// One outstanding "focus the editor" request per ref. A request survives
// until a MOUNTED panel takes it, so the /notes command can open the
// session pane (mounting the panel after the command returns) and still
// deliver focus; and per-ref tokens keep a pane reused for another session
// from serving - or swallowing - the other session's request.
export interface PendingTopNotesFocus {
  // Whether the session could not write its note when the request was
  // issued. Such a request may be redeemed much later - the session
  // resumes, the panel unmounts and remounts - when the user may have
  // moved on, so the panel serves it without stealing focus from an
  // active control. A request born writable was just asked for and
  // focuses normally.
  originReadOnly: boolean;
}

export interface TopNotesStoreState {
  expanded: Map<string, boolean>;
  pendingFocus: Map<string, PendingTopNotesFocus>;
  isExpanded(ref: string): boolean;
  hasPendingFocus(ref: string): boolean;
  // Serves at most one outstanding request: true exactly once per request.
  takePendingFocus(ref: string): boolean;
  setExpanded(ref: string, expanded: boolean): void;
  toggle(ref: string): void;
  // Expand and leave a focus request for the panel to take - immediately
  // when it is already mounted, on mount otherwise.
  openAndFocus(ref: string): void;
  // Toggle that requests editor focus when it lands on open - the one
  // action every opener that means "show me my notes" (the /notes command)
  // wants, so no caller re-derives the request bookkeeping.
  toggleAndFocus(ref: string): void;
  resetForTests(): void;
}

export const topNotesStore = createStore<TopNotesStoreState>((set, get) => ({
  expanded: new Map(),
  pendingFocus: new Map(),
  isExpanded(ref: string): boolean {
    return get().expanded.get(ref) ?? false;
  },
  hasPendingFocus(ref: string): boolean {
    return get().pendingFocus.get(ref) !== undefined;
  },
  takePendingFocus(ref: string): boolean {
    if (!get().pendingFocus.get(ref)) return false;
    set((state) => {
      const next = new Map(state.pendingFocus);
      next.delete(ref);
      return { pendingFocus: next };
    });
    return true;
  },
  setExpanded(ref: string, expanded: boolean): void {
    set((state) => {
      const next = new Map(state.expanded);
      next.set(ref, expanded);
      return { expanded: next };
    });
  },
  toggle(ref: string): void {
    set((state) => {
      const current = state.expanded.get(ref) ?? false;
      const next = new Map(state.expanded);
      next.set(ref, !current);
      return { expanded: next };
    });
  },
  openAndFocus(ref: string): void {
    // The origin era is judged from the same predicate the panel uses to
    // serve the request, read from the store at issue time: the panel's
    // props can lag a pane-reuse or remount boundary, but the request's
    // era must not.
    const model = threadsStore.getState().threads.get(ref);
    const request: PendingTopNotesFocus = { originReadOnly: !canWriteHumanNote(model) };
    set((state) => {
      const nextExpanded = new Map(state.expanded);
      nextExpanded.set(ref, true);
      const nextPending = new Map(state.pendingFocus);
      nextPending.set(ref, request);
      return { expanded: nextExpanded, pendingFocus: nextPending };
    });
  },
  toggleAndFocus(ref: string): void {
    const current = get().expanded.get(ref) ?? false;
    if (current) get().setExpanded(ref, false);
    else get().openAndFocus(ref);
  },
  resetForTests(): void {
    set({ expanded: new Map(), pendingFocus: new Map() });
  },
}));

registerPanelStoreEvictor({
  refs: () => new Set([...topNotesStore.getState().expanded.keys(), ...topNotesStore.getState().pendingFocus.keys()]),
  // Top-notes state exists only inside the SESSION pane: companion panes
  // (details/tasks/activity) for the same ref cannot show the notes bar, so
  // they must not keep its state alive either - an expanded flag that
  // outlives the session pane is invisible, and the next /notes on the
  // reopened session would toggle it CLOSED instead of opening.
  keepAliveRefs: (panes) => {
    const refs = new Set<string>();
    for (const pane of panes) {
      if (pane.type !== "session") continue;
      const ref = (pane.params as { ref?: unknown } | null | undefined)?.ref;
      if (typeof ref === "string" && ref !== "") refs.add(ref);
    }
    return refs;
  },
  evict: (ref: string) => {
    const state = topNotesStore.getState();
    if (!state.expanded.has(ref) && !state.pendingFocus.has(ref)) return;
    const nextExpanded = new Map(state.expanded);
    nextExpanded.delete(ref);
    const nextPending = new Map(state.pendingFocus);
    nextPending.delete(ref);
    topNotesStore.setState({ expanded: nextExpanded, pendingFocus: nextPending });
  },
});

export function useTopNotesExpanded(ref: string): boolean {
  return useStore(topNotesStore, (s) => s.expanded.get(ref) ?? false);
}

export function usePendingTopNotesFocus(ref: string): PendingTopNotesFocus | undefined {
  return useStore(topNotesStore, (s) => s.pendingFocus.get(ref));
}
