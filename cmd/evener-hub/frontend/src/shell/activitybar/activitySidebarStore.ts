// Sidebar intent belongs to the inspected public session, independently of
// collection lifetimes. Storage contains only reader choices, never rows or
// continuation state; the activity store still owns all reads and recovery.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { focusedActivityScopeRef, useFocusedActivityScopeRef } from "../focusedSession";
import type { ActivityTab } from "../statusbar/statusScope";
import { currentSessionRef, workspaceStore } from "../workspace";

export const ACTIVITY_VIEW_STORAGE_KEY = "evener.activity-sidebar.v1";
export const ACTIVITY_VIEW_LIMIT = 100;
const TABS: readonly ActivityTab[] = ["agents", "jobs", "watches", "tasks"];
let hydrated = false;

export interface ActivityScrollAnchor {
  id: string;
  offset: number;
}

export interface ActivityCategoryView {
  anchor?: ActivityScrollAnchor;
  shown?: number;
}

interface ActivitySessionView {
  open: boolean;
  tab: ActivityTab;
  categories: Partial<Record<ActivityTab, ActivityCategoryView>>;
}

function parseView(raw: unknown): ActivitySessionView | undefined {
  if (!raw || typeof raw !== "object" || !("open" in raw) || typeof raw.open !== "boolean") return;
  if (!("tab" in raw) || !TABS.includes(raw.tab as ActivityTab)) return;
  const categories: ActivitySessionView["categories"] = {};
  if ("categories" in raw && raw.categories && typeof raw.categories === "object") {
    for (const tab of TABS) {
      const value = (raw.categories as Record<string, unknown>)[tab];
      if (!value || typeof value !== "object") continue;
      const view: ActivityCategoryView = {};
      if ("shown" in value && typeof value.shown === "number" && Number.isSafeInteger(value.shown) && value.shown > 0)
        view.shown = value.shown;
      if ("anchor" in value && value.anchor && typeof value.anchor === "object") {
        const anchor = value.anchor;
        if (
          "id" in anchor &&
          typeof anchor.id === "string" &&
          anchor.id !== "" &&
          "offset" in anchor &&
          typeof anchor.offset === "number" &&
          Number.isFinite(anchor.offset)
        )
          view.anchor = { id: anchor.id, offset: anchor.offset };
      }
      categories[tab] = view;
    }
  }
  return { open: raw.open, tab: raw.tab as ActivityTab, categories };
}

function readViews(): Map<string, ActivitySessionView> {
  const views = new Map<string, ActivitySessionView>();
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(ACTIVITY_VIEW_STORAGE_KEY) ?? "null");
    if (raw && typeof raw === "object" && !Array.isArray(raw)) {
      for (const [ref, value] of Object.entries(raw).slice(-ACTIVITY_VIEW_LIMIT)) {
        const view = parseView(value);
        if (view) views.set(ref, view);
      }
    }
  } catch {
    // Private/full storage must not stop the live controls from working.
  }
  return views;
}

function rememberView(views: ReadonlyMap<string, ActivitySessionView>, ref: string, view: ActivitySessionView) {
  const next = new Map(views);
  next.delete(ref);
  next.set(ref, view);
  for (const oldest of next.keys()) {
    if (next.size <= ACTIVITY_VIEW_LIMIT) break;
    next.delete(oldest);
  }
  return next;
}

function saveViews(views: ReadonlyMap<string, ActivitySessionView>): void {
  try {
    localStorage.setItem(ACTIVITY_VIEW_STORAGE_KEY, JSON.stringify(Object.fromEntries(views)));
  } catch {
    // This is best-effort UI intent, like the workspace layout.
  }
}

export interface ActivitySidebarState {
  open: boolean;
  tab: ActivityTab;
  ref: string | null;
  views: ReadonlyMap<string, ActivitySessionView>;
  openWith(tab?: ActivityTab): void;
  close(): void;
  setTab(tab: ActivityTab): void;
  toggle(): void;
  retarget(ref: string | null): void;
  setCategoryView(ref: string, tab: ActivityTab, patch: Partial<ActivityCategoryView>): void;
}

export const activitySidebarStore = createStore<ActivitySidebarState>()((set, get) => {
  function update(patch: Partial<Pick<ActivitySessionView, "open" | "tab">>) {
    const state = get();
    const { open, tab } = { ...state, ...patch };
    if (state.ref === null) return set({ open, tab });
    const view = { ...state.views.get(state.ref), open, tab, categories: state.views.get(state.ref)?.categories ?? {} };
    const views = rememberView(state.views, state.ref, view);
    saveViews(views);
    set({ open, tab, views });
  }
  return {
    open: false,
    tab: "agents",
    ref: null,
    views: new Map(),
    openWith: (tab) => update({ open: true, tab: tab ?? get().tab }),
    close: () => update({ open: false }),
    setTab: (tab) => update({ tab }),
    toggle: () => update({ open: !get().open }),
    retarget(ref) {
      const state = get();
      if (ref === state.ref) return;
      if (ref === null) return set({ ref });
      const views = hydrated ? state.views : readViews();
      hydrated = true;
      // A fresh child keeps the ongoing inspection open on its current kind;
      // a visited session restores its own category and explicit close choice.
      const view = views.get(ref) ?? { open: state.open, tab: state.tab, categories: {} };
      set({ ref, open: view.open, tab: view.tab, views: rememberView(views, ref, view) });
    },
    setCategoryView(ref, tab, patch) {
      const state = get();
      const previous = state.views.get(ref) ?? { open: false, tab, categories: {} };
      const view = {
        ...previous,
        categories: { ...previous.categories, [tab]: { ...previous.categories[tab], ...patch } },
      };
      const views = rememberView(state.views, ref, view);
      saveViews(views);
      set({ views });
    },
  };
});

workspaceStore.subscribe((state, previous) => {
  const ref = currentSessionRef(state);
  if (ref !== currentSessionRef(previous)) activitySidebarStore.getState().retarget(ref);
});

export function useActivitySidebarStore<T>(selector: (state: ActivitySidebarState) => T): T {
  return useStore(activitySidebarStore, selector);
}

/** The Activity ✓ semantics, shared by the rail row and the session chrome:
 * the sidebar is open AND scoped to this session (the sidebar always describes
 * the focused session, so a bare "open" would mark every row). Subscribes to
 * BOTH inputs: the sidebar store's open, and the workspace scope - a pure
 * focus move touches no sidebar state, and without the second subscription
 * the ✓ keeps naming the session the sidebar showed before the move (the
 * menu holds its open state internally, so re-opening it never re-renders
 * the owner into a fresh read). */
export function useActivitySidebarOpenFor(ref: string): boolean {
  const open = useActivitySidebarStore((state) => state.open);
  const scopeRef = useFocusedActivityScopeRef();
  return open && scopeRef === ref;
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
export function resetActivitySidebarStoreForTests({ preserveStorage = false } = {}): void {
  if (!preserveStorage) {
    try {
      localStorage.removeItem(ACTIVITY_VIEW_STORAGE_KEY);
    } catch {
      /* best effort */
    }
  }
  hydrated = false;
  activitySidebarStore.setState({ open: false, tab: "agents", ref: null, views: new Map() });
}
