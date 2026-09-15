import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { registerPanelStoreEvictor } from "./panelStoreEviction";

export interface TopNotesStoreState {
  expanded: Map<string, boolean>;
  focusEpochs: Map<string, number>;
  isExpanded(ref: string): boolean;
  getFocusEpoch(ref: string): number;
  setExpanded(ref: string, expanded: boolean): void;
  toggle(ref: string): void;
  openAndFocus(ref: string): void;
  resetForTests(): void;
}

export const topNotesStore = createStore<TopNotesStoreState>((set, get) => ({
  expanded: new Map(),
  focusEpochs: new Map(),
  isExpanded(ref: string): boolean {
    return get().expanded.get(ref) ?? false;
  },
  getFocusEpoch(ref: string): number {
    return get().focusEpochs.get(ref) ?? 0;
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
    set((state) => {
      const nextExpanded = new Map(state.expanded);
      nextExpanded.set(ref, true);
      const nextEpochs = new Map(state.focusEpochs);
      nextEpochs.set(ref, (nextEpochs.get(ref) ?? 0) + 1);
      return { expanded: nextExpanded, focusEpochs: nextEpochs };
    });
  },
  resetForTests(): void {
    set({ expanded: new Map(), focusEpochs: new Map() });
  },
}));

registerPanelStoreEvictor({
  refs: () => topNotesStore.getState().expanded.keys(),
  evict: (ref: string) => {
    const state = topNotesStore.getState();
    if (!state.expanded.has(ref) && !state.focusEpochs.has(ref)) return;
    const nextExpanded = new Map(state.expanded);
    nextExpanded.delete(ref);
    const nextEpochs = new Map(state.focusEpochs);
    nextEpochs.delete(ref);
    topNotesStore.setState({ expanded: nextExpanded, focusEpochs: nextEpochs });
  },
});

export function useTopNotesExpanded(ref: string): boolean {
  return useStore(topNotesStore, (s) => s.expanded.get(ref) ?? false);
}

export function useTopNotesFocusEpoch(ref: string): number {
  return useStore(topNotesStore, (s) => s.focusEpochs.get(ref) ?? 0);
}
