// workspace.ts owns which logical panes are open (type + params) and which
// one is focused - the single source of truth DockHost's dockview instance
// mirrors, not the other way around. dockview-driven interactions DockHost
// can't intercept before they happen (dragging a tab closed, clicking a
// different tab) still round-trip back into this store via DockHost's own
// onDidRemovePanel/onDidActivePanelChange handlers, so this store is always
// the answer to "what's open and what's focused" regardless of which side
// initiated a change. Keeping the pane list independent of any particular
// host (rather than reading it back out of a live DockviewApi) is also what
// lets a future mobile host (Task 4) share it without dockview at all.
import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";
import type { DockviewApi, IDockviewPanel, SerializedDockview } from "dockview-core";
import { paneFor, type PaneTypeId } from "./paneRegistry";

export interface OpenPaneRecord {
  id: string;
  type: PaneTypeId;
  params: unknown;
  // Positioning hint, consumed once by DockHost when it creates the
  // dockview panel for a freshly-opened pane (addPanel's `position`
  // option). Stays on the record afterward but goes unread from then on -
  // dockview owns geometry (splits, drag-reorder) from that point forward,
  // and DockHost's reconciliation only ever adds a panel for an id once.
  beside?: string;
}

export interface WorkspaceStoreState {
  panes: OpenPaneRecord[];
  focusedPaneId: string | null;
  // Returns the paneId - a fresh one for a genuinely new pane, or the
  // existing one when opening a singleton type that's already open (its
  // params are updated in place if they differ) or a non-singleton pane
  // with identical params to one already open (deep-equal via sameParams).
  // Either way the (possibly pre-existing) pane becomes focused.
  openPane(type: PaneTypeId, params?: unknown, opts?: { beside?: string }): string;
  closePane(paneId: string): void;
  focusPane(paneId: string): void;
  layoutJSON(): unknown;
  restoreLayout(json: unknown): boolean;
}

// The wire shape stored in each dockview panel's own `params` bag - how a
// logical pane (type + its own params) round-trips through dockview's
// addPanel()/toJSON()/fromJSON(). DockHost imports this to type its
// addPanel() calls and its generic pane-host renderer's props consistently
// with what restoreLayout() below reconstructs them into.
export interface PanePanelParams {
  paneType: PaneTypeId;
  paneParams: unknown;
}

// Plain structural equality for the small, always-JSON-safe param objects
// every pane type uses ({ref}, {ref,path}, {section?}, {}). JSON.stringify
// is deterministic here because none of them have more than a couple of
// primitive-valued keys with no realistic key-ordering ambiguity - a full
// deep-equal dependency would be more machinery than this actually needs.
function sameParams(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

let nextPaneSeq = 0;
function nextPaneId(type: PaneTypeId): string {
  nextPaneSeq += 1;
  return `pane_${type}_${nextPaneSeq}`;
}

// The live DockviewApi, registered by DockHost once dockview is ready and
// cleared again on unmount. layoutJSON()/restoreLayout() are the only two
// locked methods that need it - both stay inert (null / false) with no host
// mounted, matching the plan's "dockview serialization (desktop only)". A
// module-private mutable reference, not store state, mirrors connection.ts/
// threads.ts's own precedent for "the live thing a store rides but doesn't
// own the lifecycle of".
let dockviewApi: DockviewApi | null = null;

export function registerDockviewApi(api: DockviewApi | null): void {
  dockviewApi = api;
}

const PANE_TYPE_IDS = new Set<PaneTypeId>(["session", "transcript", "doc", "spawn", "settings", "welcome"]);

function isPaneTypeId(value: unknown): value is PaneTypeId {
  return typeof value === "string" && PANE_TYPE_IDS.has(value as PaneTypeId);
}

function isRegistered(type: PaneTypeId): boolean {
  try {
    paneFor(type);
    return true;
  } catch {
    return false;
  }
}

// Reconstructs one dockview panel's params back into our own wire shape,
// throwing on anything that doesn't match a currently-registered pane type.
// restoreLayout's single catch block treats that the same as a fromJSON()
// structural-validation failure: a saved-but-unloadable layout, not a
// partial-recovery case (a layout saved by a later build, once a pane type
// this one hasn't shipped yet exists, loaded by an older one - the realistic
// way this happens given layouts persist to localStorage across deploys).
function readPanelParams(panel: IDockviewPanel): PanePanelParams {
  const raw = panel.params;
  if (!raw || !isPaneTypeId(raw.paneType) || !isRegistered(raw.paneType)) {
    throw new Error(`workspace: restored panel "${panel.id}" has unrecognized params`);
  }
  return { paneType: raw.paneType, paneParams: raw.paneParams };
}

export const workspaceStore = createStore<WorkspaceStoreState>((set, get) => ({
  panes: [],
  focusedPaneId: null,

  openPane(type, params = {}, opts) {
    const descriptor = paneFor(type);
    const state = get();

    if (descriptor.singleton) {
      const existing = state.panes.find((p) => p.type === type);
      if (existing) {
        const paramsChanged = !sameParams(existing.params, params);
        const focusChanged = state.focusedPaneId !== existing.id;
        if (paramsChanged || focusChanged) {
          set({
            panes: paramsChanged ? state.panes.map((p) => (p.id === existing.id ? { ...p, params } : p)) : state.panes,
            focusedPaneId: existing.id,
          });
        }
        return existing.id;
      }
    } else {
      const existing = state.panes.find((p) => p.type === type && sameParams(p.params, params));
      if (existing) {
        if (state.focusedPaneId !== existing.id) set({ focusedPaneId: existing.id });
        return existing.id;
      }
    }

    const id = nextPaneId(type);
    set({ panes: [...state.panes, { id, type, params, beside: opts?.beside }], focusedPaneId: id });
    return id;
  },

  closePane(paneId) {
    const state = get();
    if (!state.panes.some((p) => p.id === paneId)) return; // already closed: no-op
    set({
      panes: state.panes.filter((p) => p.id !== paneId),
      focusedPaneId: state.focusedPaneId === paneId ? null : state.focusedPaneId,
    });
  },

  focusPane(paneId) {
    const state = get();
    if (state.focusedPaneId === paneId) return; // already focused: no-op
    if (!state.panes.some((p) => p.id === paneId)) return; // unknown id: no-op
    set({ focusedPaneId: paneId });
  },

  layoutJSON() {
    return dockviewApi ? dockviewApi.toJSON() : null;
  },

  restoreLayout(json) {
    if (!dockviewApi) return false;
    try {
      dockviewApi.fromJSON(json as SerializedDockview);
      const panes = dockviewApi.panels.map((panel) => {
        const { paneType, paneParams } = readPanelParams(panel);
        return { id: panel.id, type: paneType, params: paneParams };
      });
      set({ panes, focusedPaneId: dockviewApi.activePanel?.id ?? panes[0]?.id ?? null });
      return true;
    } catch {
      // fromJSON's own structural failures leave dockview already cleared
      // (see dockview-core's source), but a failure raised by OUR OWN
      // validation above (readPanelParams) happens after fromJSON already
      // applied a layout dockview considers well-formed - clear it
      // explicitly so a rejected restore never leaves the user staring at
      // panels this app can't render (paneFor() would throw for real).
      dockviewApi?.clear();
      set({ panes: [], focusedPaneId: null });
      return false;
    }
  },
}));

export function useWorkspaceStore(): WorkspaceStoreState;
export function useWorkspaceStore<T>(selector: (state: WorkspaceStoreState) => T): T;
export function useWorkspaceStore<T>(selector?: (state: WorkspaceStoreState) => T): T | WorkspaceStoreState {
  return selector ? useStore(workspaceStore, selector) : useStore(workspaceStore);
}

// resetWorkspaceStoreForTests resets every module-private/store field to its
// initial state - workspace.ts is a singleton store (one pane list, one
// dockviewApi reference, one id counter) shared by the whole app, so
// workspace.test.ts (and DockHost.test.tsx) must reset it between tests to
// keep them isolated. No production code should ever call this (mirrors
// threads.ts's resetThreadsStoreForTests precedent).
export function resetWorkspaceStoreForTests(): void {
  dockviewApi = null;
  nextPaneSeq = 0;
  workspaceStore.setState({ panes: [], focusedPaneId: null });
}
