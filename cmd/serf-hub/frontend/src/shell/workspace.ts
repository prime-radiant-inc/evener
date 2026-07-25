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

import type { DockviewApi, IDockviewPanel, SerializedDockview } from "dockview-core";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { type PaneTypeId, paneFor } from "./paneRegistry";

// Which of the workspace's two slots a pane lives in. The main slot holds
// exactly ONE pane - the big one in the top left, beside the rail - and
// everything else stacks as tabs in the secondary group to its right. A main
// pane therefore never shares a tab bar with anything, which is the whole
// point (Jesse: "there should only ever be one pane in the 'main group'").
export type PaneSlot = "main" | "secondary";

export interface OpenPaneRecord {
  id: string;
  type: PaneTypeId;
  params: unknown;
  // Which slot this pane belongs to. Assigned once, at open time, by the
  // policy in openPane below; DockHost turns it into the dockview addPanel
  // `position` that actually places the panel (see its own comment on the
  // slot -> position mapping). It is not re-derived afterward - dockview owns
  // geometry (splits, drag-reorder) from that point forward, and DockHost's
  // reconciliation only ever adds a panel for an id once.
  slot: PaneSlot;
}

export interface WorkspaceStoreState {
  panes: OpenPaneRecord[];
  focusedPaneId: string | null;
  // Returns the paneId - a fresh one for a genuinely new pane, or the
  // existing one when opening a singleton type that's already open (its
  // params are updated in place if they differ) or a non-singleton pane
  // with identical params to one already open (deep-equal via sameParams).
  // Either way the (possibly pre-existing) pane becomes focused, unless
  // keepExistingFocus asks otherwise.
  //
  // keepExistingFocus: focus a pane this call had to CREATE, but leave focus
  // alone when it resolved to one already open. DockHost's boot merge is the
  // one caller: on a reload the URL-routed pane is normally already in the
  // restored layout, so focusing it would overwrite the active tab the saved
  // layout just restored - while a deep link to a pane the layout does NOT
  // contain is genuinely new and must still take focus.
  //
  // Placement is this function's own decision, not a caller's: the FIRST pane
  // takes the main slot, every later one goes secondary. Putting the policy
  // here rather than at the ~21 call sites is what makes it one rule instead
  // of twenty-one (see PaneSlot above for the rule itself).
  openPane(type: PaneTypeId, params?: unknown, opts?: { keepExistingFocus?: boolean }): string;
  closePane(paneId: string): void;
  // The pane occupying the main slot, or null when it is empty (the state
  // DockHost relaunches welcome into). Exposed because "is the main slot
  // empty" is one predicate with two consumers - DockHost's boot fallback and
  // its per-close relaunch.
  mainPane(): OpenPaneRecord | null;
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

// getDockviewApi exposes the live api (or null when no dockview host is
// mounted - a mobile StackHost session, or before DockHost's onReady) for the
// two imperative pane actions that genuinely need it: shell/paneActions.ts's
// popOutPane (dockview-native popout has no store-level equivalent) and its
// openBeside (a null api is the "no split capability here" signal that drives
// the mobile degrade-to-plain-open). A read-only accessor beside the existing
// registerDockviewApi/layoutJSON/restoreLayout, NOT store state - the same
// "the live thing a store rides but doesn't own the lifecycle of" precedent
// those already follow.
export function getDockviewApi(): DockviewApi | null {
  return dockviewApi;
}

// Type predicate: true only for a value that's both a string AND
// currently registered in the pane registry. paneFor() (imported above) is
// the single source of truth for "is this a real pane type" - registering
// a SECOND, hand-maintained list of PaneTypeId's own union members here
// (an earlier version did exactly that, via a `Set` literal) would drift
// silently the moment PaneTypeId's union in paneRegistry.ts ever changes,
// with no compiler check to catch it. The cast reflects paneFor()'s own
// runtime check as the actual source of truth, not a hidden assumption -
// the registry Map's own .get() (which paneFor calls) doesn't "trust" its
// argument's type at runtime, it just looks the key up; whether that
// lookup succeeds IS the validation.
function isRegistered(type: unknown): type is PaneTypeId {
  if (typeof type !== "string") return false;
  try {
    paneFor(type as PaneTypeId);
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
  if (!raw || !isRegistered(raw.paneType)) {
    throw new Error(`workspace: restored panel "${panel.id}" has unrecognized params`);
  }
  return { paneType: raw.paneType, paneParams: raw.paneParams };
}

// Restored panel ids came from a PREVIOUS page load's own independently-
// numbered nextPaneId counter (nextPaneSeq resets to 0 on every fresh page
// load - see nextPaneId's own comment above), so a freshly-minted id AFTER
// a restore can collide with one just restored - e.g. both sessions' very
// first pane is "pane_doc_1". Left unguarded, the store would then hold two
// DIFFERENT logical panes sharing one id, and DockHost's reconciliation
// effect (which looks dockview panels up BY id) would silently push the
// second one's params into the first one's real dockview panel instead of
// creating a new one - discarding whichever pane's content lost that race,
// with no error or warning. Bumping the counter past every id restoreLayout
// just brought back makes every id minted AFTER this collision-proof
// against it. This was always a latent hazard (any pane opened after a
// restore could have hit it), but DockHost's merge-restore boot sequence
// (handleReady re-opening a routed pane on top of a restored base) made it
// routine rather than rare - caught via a live save/restore/re-open round
// trip in that flow's own test, not by inspection.
function bumpPastRestoredIds(panes: { id: string }[]): void {
  for (const p of panes) {
    const suffix = /_(\d+)$/.exec(p.id)?.[1];
    if (suffix !== undefined) nextPaneSeq = Math.max(nextPaneSeq, Number(suffix));
  }
}

export const workspaceStore = createStore<WorkspaceStoreState>((set, get) => ({
  panes: [],
  focusedPaneId: null,

  mainPane() {
    return get().panes.find((p) => p.slot === "main") ?? null;
  },

  openPane(type, params = {}, opts) {
    const descriptor = paneFor(type);
    const state = get();

    const keepFocus = opts?.keepExistingFocus === true;

    if (descriptor.singleton) {
      const existing = state.panes.find((p) => p.type === type);
      if (existing) {
        const paramsChanged = !sameParams(existing.params, params);
        const focusChanged = !keepFocus && state.focusedPaneId !== existing.id;
        if (paramsChanged || focusChanged) {
          set({
            panes: paramsChanged ? state.panes.map((p) => (p.id === existing.id ? { ...p, params } : p)) : state.panes,
            ...(keepFocus ? {} : { focusedPaneId: existing.id }),
          });
        }
        return existing.id;
      }
    } else {
      const existing = state.panes.find((p) => p.type === type && sameParams(p.params, params));
      if (existing) {
        if (!keepFocus && state.focusedPaneId !== existing.id) set({ focusedPaneId: existing.id });
        return existing.id;
      }
    }

    // The one placement rule, in the one place every open goes through: the
    // main slot takes a pane only when it is empty, so it holds exactly one at
    // a time and everything else stacks in the secondary group to its right.
    //
    // A welcome pane in the main slot counts as empty. Welcome IS the main
    // slot's empty state ("No session open"), so the first real pane replaces
    // it instead of opening beside it - without this, navigating from "/" to a
    // session left the workspace split between a session and a placeholder
    // telling the user nothing was open (seen in a real browser, not a
    // fixture). Only the main slot's welcome is displaced, and only by a pane
    // that is not itself welcome.
    const id = nextPaneId(type);
    const main = state.panes.find((p) => p.slot === "main");
    const displaced = type !== "welcome" && main?.type === "welcome" ? main : undefined;
    const slot: PaneSlot = main === undefined || displaced !== undefined ? "main" : "secondary";
    const kept = displaced ? state.panes.filter((p) => p.id !== displaced.id) : state.panes;
    set({ panes: [...kept, { id, type, params, slot }], focusedPaneId: id });
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
      // Slots come back from dockview's OWN restored geometry, not from the
      // saved JSON: api.panels is in grid order, so the FIRST panel is the one
      // in the top-left group - the main pane. Read only `panels` (plus each
      // panel's own group), never api.groups: this function's whole
      // dockview surface stays the four members it already used
      // (fromJSON/panels/activePanel/clear), which is also what keeps the unit
      // doubles in workspace.test.ts/paneRestore.test.ts honest doubles rather
      // than a growing mirror of the real api.
      //
      // A layout that somehow restores a SECOND panel into that same first
      // group leaves only the first as "main" - impossible for a layout this
      // build saved (the one-pane rule holds on the way in), and it degrades to
      // a stacked main group rather than a crash for anything else.
      const panes = dockviewApi.panels.map((panel, index) => {
        const { paneType, paneParams } = readPanelParams(panel);
        const slot: PaneSlot = index === 0 ? "main" : "secondary";
        return { id: panel.id, type: paneType, params: paneParams, slot };
      });
      bumpPastRestoredIds(panes);
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
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
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
