// chromeStore carries the shell chrome's cross-pane state, published
// host-agnostically and rendered by whichever workspace host is showing.
// StackHost (the <900px host) reads it; DockHost never does - every channel
// is always written, so a pane never asks "am I mobile?"
// (2026-07-30-mobile-session-layout-design.md, decision 2).
//
// paneTitle: the focused pane's TITLE, published by PaneScaffold (the
// widget that already owns pane titles). null whenever no pane is
// publishing (no pane mounted, or the publishing pane just unmounted), and
// StackHost's bar simply shows nothing between back and the drawer
// trigger, as before.
//
// paneBack: the focused pane's own "up" target, for a pane whose internal
// drill-down the workspace cannot see - settings is a SINGLETON pane, so
// section switches update its params in place (workspace.ts openPane),
// the pane id never changes, and StackHost's focus-keyed back-stack never
// observes the transition (2026-08-16 settings mobile-nav design). While
// published, StackHost's top-bar Back invokes this handler instead of
// walking the pane stack; null means Back keeps its ordinary meaning.
// Settings' section list (the drill-down's ROOT) publishes nothing, so
// Back there still exits the pane - correct at a root.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export interface ChromeStoreState {
  paneTitle: string | null;
  setPaneTitle(title: string | null): void;
  paneBack: (() => void) | null;
  setPaneBack(handler: (() => void) | null): void;
}

export const chromeStore = createStore<ChromeStoreState>()((set) => ({
  paneTitle: null,
  setPaneTitle: (title) => set({ paneTitle: title }),
  paneBack: null,
  setPaneBack: (handler) => set({ paneBack: handler }),
}));

export function useChromeStore<T>(selector: (state: ChromeStoreState) => T): T {
  return useStore(chromeStore, selector);
}

// resetChromeStoreForTests restores the initial state between tests -
// mirrors workspace.ts's resetWorkspaceStoreForTests precedent. No
// production code should ever call this.
export function resetChromeStoreForTests(): void {
  chromeStore.setState({ paneTitle: null, paneBack: null });
}
