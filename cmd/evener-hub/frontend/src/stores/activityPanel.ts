// Panel visibility and disclosure belong to the view; activity reads and recovery belong to the SDK.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { registerPanelStoreEvictor } from "./panelStoreEviction";

export interface ActivityPanelEntry {
  sheetOpen: boolean;
  expandedFoldIDs: string[];
  detailOverrides: ReadonlyMap<string, boolean>;
}
export const EMPTY_ACTIVITY_PANEL_ENTRY: ActivityPanelEntry = {
  sheetOpen: false,
  expandedFoldIDs: [],
  detailOverrides: new Map(),
};
export interface ActivityPanelStoreState {
  entries: Map<string, ActivityPanelEntry>;
  setSheetOpen(ref: string, open: boolean): void;
  setDetailOpen(ref: string, rowID: string, open: boolean): void;
  toggleFold(ref: string, foldID: string): void;
  resetForTests(): void;
}
export const activityPanelStore = createStore<ActivityPanelStoreState>((set) => ({
  entries: new Map(),
  setSheetOpen(ref, open) {
    set((state) => {
      const entry = state.entries.get(ref) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
      if (entry.sheetOpen === open) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, sheetOpen: open });
      return { entries };
    });
  },
  setDetailOpen(ref, rowID, open) {
    set((state) => {
      const entry = state.entries.get(ref) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
      const detailOverrides = new Map(entry.detailOverrides);
      detailOverrides.set(rowID, open);
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, detailOverrides });
      return { entries };
    });
  },
  toggleFold(ref, foldID) {
    set((state) => {
      const entry = state.entries.get(ref) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
      const entries = new Map(state.entries);
      entries.set(ref, {
        ...entry,
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
