// Panel visibility and disclosure belong to the view; activity reads and recovery belong to the SDK.
import type { SessionActivityCollection } from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { registerPanelStoreEvictor } from "./panelStoreEviction";

export interface ActivityPanelEntry {
  sheetOpen: boolean;
  expandedFoldIDs: string[];
  detailOverrides: ReadonlyMap<string, boolean>;
  resolvedSessionId?: string;
  loadedExtent: Partial<Record<SessionActivityCollection, string>>;
}
export type ActivityPanelProgress = Record<
  SessionActivityCollection,
  { sessionId?: string; ids: readonly string[]; complete: boolean }
>;
export const EMPTY_ACTIVITY_PANEL_ENTRY: ActivityPanelEntry = {
  sheetOpen: false,
  expandedFoldIDs: [],
  detailOverrides: new Map(),
  loadedExtent: {},
};
export interface ActivityPanelStoreState {
  entries: Map<string, ActivityPanelEntry>;
  setSheetOpen(ref: string, open: boolean): void;
  setDetailOpen(ref: string, rowID: string, open: boolean): void;
  toggleFold(ref: string, foldID: string): void;
  recordLoadedExtent(ref: string, sessionId: string, progress: ActivityPanelProgress): void;
  resetForTests(): void;
}
export const activityPanelStore = createStore<ActivityPanelStoreState>((set) => ({
  entries: new Map(),
  setSheetOpen(ref, open) {
    set((state) => {
      const entry = state.entries.get(ref) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
      if (entry.sheetOpen === open) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, sheetOpen: open, loadedExtent: open ? entry.loadedExtent : {} });
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
  recordLoadedExtent(ref, sessionId, progress) {
    set((state) => {
      const saved = state.entries.get(ref) ?? EMPTY_ACTIVITY_PANEL_ENTRY;
      const replaced = saved.resolvedSessionId !== undefined && saved.resolvedSessionId !== sessionId;
      const entry = replaced ? { ...EMPTY_ACTIVITY_PANEL_ENTRY, sheetOpen: saved.sheetOpen } : saved;
      const loadedExtent = { ...entry.loadedExtent };
      let changed = entry.resolvedSessionId !== sessionId;
      for (const resource of ["delegates", "jobs", "watches"] as const) {
        const current = progress[resource];
        if (current.sessionId !== sessionId) continue;
        const boundary = loadedExtent[resource];
        // A cold first page is not evidence that the old displayed extent vanished.
        if (boundary && !current.ids.includes(boundary) && !current.complete) continue;
        const last = current.ids.at(-1);
        if (last === boundary) continue;
        changed = true;
        if (last === undefined) delete loadedExtent[resource];
        else loadedExtent[resource] = last;
      }
      if (!changed) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, resolvedSessionId: sessionId, loadedExtent });
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
