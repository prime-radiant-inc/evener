// chromeStore carries the shell chrome's one piece of cross-pane state: the
// focused pane's TITLE, published by PaneScaffold (the widget that already
// owns pane titles) and rendered by whichever workspace host is showing.
// StackHost (the <900px host) renders it in its top bar; DockHost never
// reads it - the channel is always written, host-agnostically, so a pane
// never asks "am I mobile?" (2026-07-30-mobile-session-layout-design.md,
// decision 2). paneTitle is null whenever no pane is publishing (no pane
// mounted, or the publishing pane just unmounted), and StackHost's bar
// simply shows nothing between back and the drawer trigger, as before.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export interface ChromeStoreState {
  paneTitle: string | null;
  setPaneTitle(title: string | null): void;
}

export const chromeStore = createStore<ChromeStoreState>()((set) => ({
  paneTitle: null,
  setPaneTitle: (title) => set({ paneTitle: title }),
}));

export function useChromeStore<T>(selector: (state: ChromeStoreState) => T): T {
  return useStore(chromeStore, selector);
}

// resetChromeStoreForTests restores the initial state between tests -
// mirrors workspace.ts's resetWorkspaceStoreForTests precedent. No
// production code should ever call this.
export function resetChromeStoreForTests(): void {
  chromeStore.setState({ paneTitle: null });
}
