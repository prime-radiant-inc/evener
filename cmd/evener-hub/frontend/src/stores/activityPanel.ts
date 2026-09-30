// Disclosure belongs to the view; activity reads and recovery belong to the SDK.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { registerPanelStoreEvictor } from "./panelStoreEviction";

export interface ActivityPanelEntry {
  expandedFoldIDs: string[];
}
export const EMPTY_ACTIVITY_PANEL_ENTRY: ActivityPanelEntry = { expandedFoldIDs: [] };
export interface ActivityPanelStoreState {
  entries: Map<string, ActivityPanelEntry>;
  toggleFold(ref: string, foldID: string): void;
  resetForTests(): void;
}
export const activityPanelStore = createStore<ActivityPanelStoreState>((set) => ({
  entries: new Map(),
  toggleFold(ref, foldID) {
    set((state) => {
      const entry = state.entries.get(ref) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
      const entries = new Map(state.entries);
      entries.set(ref, {
        expandedFoldIDs: entry.expandedFoldIDs.includes(foldID)
          ? entry.expandedFoldIDs.filter((id) => id !== foldID)
          : [...entry.expandedFoldIDs, foldID],
      });
      return { entries };
    });
  },
  resetForTests() {
    set({ entries: new Map() });
  },
}));
registerPanelStoreEvictor({
  refs: () => activityPanelStore.getState().entries.keys(),
  evict(ref) {
    activityPanelStore.setState((state) => {
      const entries = new Map(state.entries);
      entries.delete(ref);
      return { entries };
    });
  },
});
export function useActivityPanelStore<T>(selector: (state: ActivityPanelStoreState) => T): T {
  return useStore(activityPanelStore, selector);
}
export function resetActivityPanelStoreForTests(): void {
  activityPanelStore.getState().resetForTests();
}
