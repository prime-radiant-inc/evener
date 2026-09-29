// The activity sidebar's open/tab state: a tiny shell-chrome store in the
// chromeStore.ts pattern (vanilla zustand + useStore + a test reset). Open
// carries the tab to show so a status-bar chip can open the sidebar straight
// onto its own kind; close keeps the tab for the next open.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ActivityTab } from "../statusbar/statusScope";

export interface ActivitySidebarState {
  open: boolean;
  tab: ActivityTab;
  openWith(tab?: ActivityTab): void;
  close(): void;
  setTab(tab: ActivityTab): void;
}

export const activitySidebarStore = createStore<ActivitySidebarState>()((set) => ({
  open: false,
  tab: "agents",
  openWith: (tab) => set((state) => ({ open: true, tab: tab ?? state.tab })),
  close: () => set({ open: false }),
  setTab: (tab) => set({ tab }),
}));

export function useActivitySidebarStore<T>(selector: (state: ActivitySidebarState) => T): T {
  return useStore(activitySidebarStore, selector);
}

// resetActivitySidebarStoreForTests restores the initial state between tests -
// mirrors chromeStore's resetChromeStoreForTests precedent. No production code
// should ever call this.
export function resetActivitySidebarStoreForTests(): void {
  activitySidebarStore.setState({ open: false, tab: "agents" });
}
