// chromeStore carries the shell chrome's cross-pane state, published
// host-agnostically and rendered by whichever workspace host is showing.
// StackHost (the <900px host) reads the focused title and back action;
// DockHost reads hydrated titles by pane ID. Every channel is written
// independently of the host, so a pane never asks "am I mobile?"
// (2026-07-30-mobile-session-layout-design.md, decision 2).
//
// paneTitle: the focused pane's TITLE, published by PaneScaffold (the
// widget that already owns pane titles). null whenever no pane is
// publishing (no pane mounted, or the publishing pane just unmounted), and
// StackHost's bar simply shows nothing between back and the drawer
// trigger, as before.
//
// paneTitles: hydrated titles keyed by the owning pane, so desktop tabs can
// name a resource already loaded by its pane without another data reader.
// The descriptor supplies the fallback until that pane publishes a title.
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
  /** The document each pane lives in. Dockview moves a panel's DOM into a popout
   * window, so a shell-level observer needs the focused reader's own document. */
  paneDocuments: ReadonlyMap<string, Document>;
  setPaneDocumentFor(paneId: string, document: Document | null): void;
  /** Publish one document for several panes at once - a popout move carries a
   * whole group, and one store write keeps observers from seeing a half-moved
   * set. */
  setPaneDocumentsFor(paneIds: readonly string[], document: Document | null): void;
  setPaneTitle(title: string | null): void;
  paneTitles: ReadonlyMap<string, string>;
  setPaneTitleFor(paneId: string, title: string | null): void;
  paneBack: (() => void) | null;
  setPaneBack(handler: (() => void) | null): void;
}

export const chromeStore = createStore<ChromeStoreState>()((set) => ({
  paneTitle: null,
  setPaneTitle: (title) => set({ paneTitle: title }),
  paneDocuments: new Map(),
  setPaneDocumentFor: (paneId, document) =>
    set((state) => {
      if ((state.paneDocuments.get(paneId) ?? null) === document) return state;
      const paneDocuments = new Map(state.paneDocuments);
      if (document === null) paneDocuments.delete(paneId);
      else paneDocuments.set(paneId, document);
      return { paneDocuments };
    }),
  setPaneDocumentsFor: (paneIds, document) =>
    set((state) => {
      let changed = false;
      const paneDocuments = new Map(state.paneDocuments);
      for (const paneId of paneIds) {
        if ((paneDocuments.get(paneId) ?? null) === document) continue;
        changed = true;
        if (document === null) paneDocuments.delete(paneId);
        else paneDocuments.set(paneId, document);
      }
      return changed ? { paneDocuments } : state;
    }),
  paneTitles: new Map(),
  setPaneTitleFor: (paneId, title) =>
    set((state) => {
      if ((state.paneTitles.get(paneId) ?? null) === title) return state;
      const paneTitles = new Map(state.paneTitles);
      if (title === null) paneTitles.delete(paneId);
      else paneTitles.set(paneId, title);
      return { paneTitles };
    }),
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
  chromeStore.setState({ paneTitle: null, paneTitles: new Map(), paneBack: null, paneDocuments: new Map() });
}
