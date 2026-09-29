// The activity sidebar's open/tab state: a tiny shell-chrome store in the
// chromeStore.ts pattern (vanilla zustand + useStore + a test reset). Open
// carries the tab to show so a status-bar chip can open the sidebar straight
// onto its own kind; close keeps the tab for the next open.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { focusedActivityScopeRef } from "../focusedSession";
import type { ActivityTab } from "../statusbar/statusScope";
import { workspaceStore } from "../workspace";

export interface ActivitySidebarState {
  open: boolean;
  tab: ActivityTab;
  openWith(tab?: ActivityTab): void;
  close(): void;
  setTab(tab: ActivityTab): void;
  toggle(): void;
}

export const activitySidebarStore = createStore<ActivitySidebarState>()((set) => ({
  open: false,
  tab: "agents",
  openWith: (tab) => set((state) => ({ open: true, tab: tab ?? state.tab })),
  close: () => set({ open: false }),
  setTab: (tab) => set({ tab }),
  toggle: () => set((state) => ({ open: !state.open })),
}));

export function useActivitySidebarStore<T>(selector: (state: ActivitySidebarState) => T): T {
  return useStore(activitySidebarStore, selector);
}

/** The Activity ✓ semantics, shared by the rail row and the session chrome:
 * the sidebar is open AND scoped to this session (the sidebar always describes
 * the focused session, so a bare "open" would mark every row). Subscribes to
 * the sidebar store alone - a toggle re-renders the menu's owner, and the
 * selector re-reads the scope (imperatively) at that render and at every
 * subsequent one. No workspace subscription: focus changes don't need to
 * re-render rows for a marker that's only read when a menu is open. */
export function useActivitySidebarOpenFor(ref: string): boolean {
  return useActivitySidebarStore((state) => state.open && focusedActivityScopeRef() === ref);
}

/** The imperative twin for non-React call sites. */
export function activitySidebarOpenFor(ref: string): boolean {
  return activitySidebarStore.getState().open && focusedActivityScopeRef() === ref;
}

/** Closes any sessionActivity panes for a session. Desktop Activity retargeted
 * to the sidebar, so a pane that survives an upgrade or a restored layout has
 * no affordance that opens it and no ✓ that marks it: an orphan. Opening the
 * sidebar on a session supersedes its leftover panes (the mobile rail keeps
 * opening them, so this only runs on the desktop paths). */
export function closeSessionActivityPanes(ref: string): void {
  const workspace = workspaceStore.getState();
  for (const pane of workspace.panes) {
    if (pane.type === "sessionActivity" && (pane.params as { ref?: string }).ref === ref) {
      workspace.closePane(pane.id);
    }
  }
}

// resetActivitySidebarStoreForTests restores the initial state between tests -
// mirrors chromeStore's resetChromeStoreForTests precedent. No production code
// should ever call this.
export function resetActivitySidebarStoreForTests(): void {
  activitySidebarStore.setState({ open: false, tab: "agents" });
}
