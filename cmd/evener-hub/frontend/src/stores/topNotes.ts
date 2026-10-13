import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { canWriteHumanNote } from "./humanNoteDrafts";
import { openConversationRefs, registerPanelStoreEvictor } from "./panelStoreEviction";
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
  // The pane the request was addressed to, or null when it names none. One
  // conversation can hold two notes panels at once (an ordinary session pane
  // and a cascade column), so a request that names its seat is served only by
  // that panel; an unaddressed request is served by whichever mounted panel
  // for the ref gets it.
  paneId: string | null;
}

export interface TopNotesStoreState {
  expanded: Map<string, boolean>;
  pendingFocus: Map<string, PendingTopNotesFocus>;
  isExpanded(ref: string): boolean;
  hasPendingFocus(ref: string): boolean;
  // Serves at most one outstanding request: true exactly once per request.
  takePendingFocus(ref: string, paneId?: string): boolean;
  setExpanded(ref: string, expanded: boolean): void;
  toggle(ref: string): void;
  // Expand and leave a focus request for the panel to take - immediately
  // when it is already mounted, on mount otherwise.
  openAndFocus(ref: string, paneId?: string): void;
  // Toggle that requests editor focus when it lands on open - the one
  // action every opener that means "show me my notes" (the /notes command)
  // wants, so no caller re-derives the request bookkeeping.
  toggleAndFocus(ref: string, paneId?: string): void;
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
  takePendingFocus(ref: string, paneId?: string): boolean {
    const request = get().pendingFocus.get(ref);
    if (!request) return false;
    // An addressed request belongs to its seat alone: the other panel showing
    // the same conversation must leave it for the addressed one.
    if (request.paneId !== null && request.paneId !== paneId) return false;
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
  openAndFocus(ref: string, paneId?: string): void {
    // The origin era is judged from the same predicate the panel uses to
    // serve the request, read from the store at issue time: the panel's
    // props can lag a pane-reuse or remount boundary, but the request's
    // era must not.
    const model = threadsStore.getState().threads.get(ref);
    const request: PendingTopNotesFocus = { originReadOnly: !canWriteHumanNote(model), paneId: paneId ?? null };
    set((state) => {
      const nextExpanded = new Map(state.expanded);
      nextExpanded.set(ref, true);
      const nextPending = new Map(state.pendingFocus);
      nextPending.set(ref, request);
      return { expanded: nextExpanded, pendingFocus: nextPending };
    });
  },
  toggleAndFocus(ref: string, paneId?: string): void {
    const current = get().expanded.get(ref) ?? false;
    if (current) get().setExpanded(ref, false);
    else get().openAndFocus(ref, paneId);
  },
  resetForTests(): void {
    set({ expanded: new Map(), pendingFocus: new Map() });
  },
}));

registerPanelStoreEvictor({
  refs: () => new Set([...topNotesStore.getState().expanded.keys(), ...topNotesStore.getState().pendingFocus.keys()]),
  // Top-notes state exists only inside the conversation surface: the session
  // pane's notes bar, or any readable column of a sessionZoom cascade (whose
  // embedded ConversationBody mounts the same bar per scope ref). Companion
  // panes (details/tasks/activity) for the same ref cannot show the notes bar,
  // so they must not keep its state alive either - an expanded flag that
  // outlives the conversation surface is invisible, and the next /notes on the
  // reopened conversation would toggle it CLOSED instead of opening.
  keepAliveRefs: (panes) => openConversationRefs(panes),
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

// A request born writable is redeemed as if the user just asked for it -
// true only while the write capability it was issued under still holds.
// Once the session turns read-only (or drops out of the store) before a
// mounted panel serves it, the click that asked for focus is stale, and a
// later resume must not redeem it as if fresh or it would steal focus
// from wherever the user has since moved. Drop such requests at the
// capability flip itself, whether or not any panel is mounted; requests
// born read-only keep their later silent-redemption semantics instead.
threadsStore.subscribe(() => {
  const pending = topNotesStore.getState().pendingFocus;
  if (pending.size === 0) return;
  const threads = threadsStore.getState().threads;
  let next: Map<string, PendingTopNotesFocus> | null = null;
  for (const [ref, request] of pending) {
    if (request.originReadOnly || canWriteHumanNote(threads.get(ref))) continue;
    next ??= new Map(pending);
    next.delete(ref);
  }
  if (next) topNotesStore.setState({ pendingFocus: next });
});

export function useTopNotesExpanded(ref: string): boolean {
  return useStore(topNotesStore, (s) => s.expanded.get(ref) ?? false);
}

export function usePendingTopNotesFocus(ref: string): PendingTopNotesFocus | undefined {
  return useStore(topNotesStore, (s) => s.pendingFocus.get(ref));
}
