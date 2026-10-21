// Sidebar intent belongs to the inspected public session, independently of
// collection lifetimes. Storage contains only reader choices, never rows or
// continuation state; the activity store still owns all reads and recovery.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { zoomPaneCoversConversation } from "../../panes/zoom/cascadeCoverage";
import { isRendered } from "../../widgets/focusscope/tabbable";
import { focusedActivityScopeRef, useFocusedActivityScopeRef } from "../focusedSession";
import { refParam } from "../routing";
import type { ActivityTab } from "../statusbar/statusScope";
import { currentSessionRef, presentedPanes, workspaceStore } from "../workspace";

export const ACTIVITY_VIEW_STORAGE_KEY = "evener.activity-sidebar.v1";
export const ACTIVITY_VIEW_LIMIT = 100;
const TABS: readonly ActivityTab[] = ["agents", "jobs", "watches", "tasks", "about"];
let hydrated = false;
let saveTimer: ReturnType<typeof setTimeout> | undefined;
const SAVE_DELAY_MS = 400;

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
  clearTimeout(saveTimer);
  saveTimer = undefined;
  try {
    localStorage.setItem(ACTIVITY_VIEW_STORAGE_KEY, JSON.stringify(Object.fromEntries(views)));
  } catch {
    // This is best-effort UI intent, like the workspace layout.
  }
}

function flushViews(): void {
  if (saveTimer === undefined) return;
  saveViews(activitySidebarStore.getState().views);
}

function scheduleSave(): void {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(flushViews, SAVE_DELAY_MS);
}

// Page departure must retain the latest gesture even before its trailing save.
if (typeof window !== "undefined") window.addEventListener("pagehide", flushViews);

export interface ActivitySidebarState {
  open: boolean;
  tab: ActivityTab;
  ref: string | null;
  /** The scope an open request named that focus does not resolve to (an
   * embedded cascade column's Overview). Held until the workspace scope
   * moves or the sidebar closes; null means the sidebar follows the focused
   * scope. */
  explicitScopeRef: string | null;
  views: ReadonlyMap<string, ActivitySessionView>;
  openWith(tab?: ActivityTab, opener?: HTMLElement): void;
  openFor(ref: string, tab?: ActivityTab, opener?: HTMLElement, sourcePaneId?: string): void;
  close(): void;
  setTab(tab: ActivityTab): void;
  toggle(): void;
  retarget(ref: string | null): void;
  retainOpenView(ref: string): void;
  setCategoryView(ref: string, tab: ActivityTab, patch: Partial<ActivityCategoryView>): void;
}

let activitySidebarOpener: HTMLElement | null = null;
let activitySidebarOpenerPaneId: string | null = null;
let activitySidebarOpenerRef: string | null = null;

function captureActivitySidebarOpener(opener?: HTMLElement): void {
  const active = opener ?? document.activeElement;
  if (active instanceof HTMLElement && active.closest('[data-testid="activity-sidebar"]')) return;
  activitySidebarOpener = active instanceof HTMLElement && active !== document.body ? active : null;
  activitySidebarOpenerPaneId = workspaceStore.getState().focusedPaneId;
  activitySidebarOpenerRef = activitySidebarStore.getState().ref;
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
    explicitScopeRef: null,
    views: new Map(),
    openWith: (tab, opener) => {
      captureActivitySidebarOpener(opener);
      update({ open: true, tab: tab ?? get().tab });
    },
    openFor: (ref, tab, opener, sourcePaneId) => {
      const workspace = workspaceStore.getState();
      const presented = presentedPanes(workspace.panes);
      const source = presented.find((candidate) => candidate.id === sourcePaneId);
      if (source && source.type === "sessionZoom" && zoomPaneCoversConversation(source, ref)) {
        // An embedded column's open request: the hosting cascade keeps
        // focus — an ordinary pane for the same conversation must not
        // steal it — and the requested scope holds explicitly whenever the
        // focused leaf does not already describe it.
        set({ explicitScopeRef: focusedActivityScopeRef() === ref ? null : ref });
        if (get().ref !== ref) get().retarget(ref);
        get().openWith(tab, opener);
        return;
      }
      const panes = presented.filter((candidate) => candidate.type === "session" && refParam(candidate.params) === ref);
      const pane = panes.find((candidate) => candidate.id === workspace.focusedPaneId) ?? panes[0];
      if (pane) {
        // A focus-based request releases any held column scope and saves
        // against its own conversation, even when focus does not move.
        set({ explicitScopeRef: null });
        workspaceStore.getState().focusPane(pane.id);
        if (get().ref !== ref) get().retarget(ref);
      } else {
        // No pane carries the conversation (a cascade column, or a session
        // nothing has opened): focus cannot carry the request, so hold the
        // requested scope explicitly and the sidebar describes it rather
        // than the focused leaf.
        get().retarget(ref);
        set({ explicitScopeRef: ref });
      }
      get().openWith(tab, opener);
    },
    close: () => {
      update({ open: false });
      // The inspection ended: release any held column scope and leave
      // preference saves with the focused conversation.
      set({ explicitScopeRef: null });
      const focused = focusedActivityScopeRef();
      if (get().ref !== focused) {
        get().retarget(focused);
        // retarget restores the focused scope's saved view, which can carry
        // open: true from an earlier inspection of the same conversation.
        // The dismissal must survive that restoration, and records as the
        // focused conversation's explicit close choice.
        if (get().open) update({ open: false });
      }
    },
    setTab: (tab) => update({ tab }),
    toggle: () => {
      if (get().open) {
        get().close();
        return;
      }
      captureActivitySidebarOpener();
      update({ open: true });
    },
    retarget(ref) {
      const state = get();
      if (ref === state.ref) return;
      flushViews();
      if (ref === null) return set({ ref });
      const views = hydrated ? state.views : readViews();
      hydrated = true;
      // A fresh child keeps the ongoing inspection open on its current kind;
      // a visited session restores its own category and explicit close choice.
      const view = views.get(ref) ?? { open: state.open, tab: state.tab, categories: {} };
      set({ ref, open: view.open, tab: view.tab, views: rememberView(views, ref, view) });
      if (view.open && !state.open) captureActivitySidebarOpener();
    },
    retainOpenView(ref) {
      const state = get();
      const view = state.views.get(ref);
      if (!view || !state.open || state.ref !== ref) return;
      // A committed view records inherited child inspection intent.
      // Global focus can also move while the sidebar is not mounted.
      const savedViews = readViews();
      const saved = savedViews.get(ref);
      if (saved?.open === view.open && saved.tab === view.tab) {
        // Revisiting a committed view must retain its recency across reload.
        if ([...savedViews.keys()].at(-1) !== ref) scheduleSave();
        return;
      }
      saveViews(state.views);
    },
    setCategoryView(ref, tab, patch) {
      const state = get();
      const previous = state.views.get(ref) ?? { open: false, tab, categories: {} };
      const before = previous.categories[tab];
      const category = { ...before, ...patch };
      if (
        category.shown === before?.shown &&
        category.anchor?.id === before?.anchor?.id &&
        category.anchor?.offset === before?.anchor?.offset
      )
        return;
      const view = {
        ...previous,
        categories: { ...previous.categories, [tab]: category },
      };
      const views = rememberView(state.views, ref, view);
      set({ views });
      if (patch.shown !== undefined) saveViews(views);
      else scheduleSave();
    },
  };
});

workspaceStore.subscribe((state, previous) => {
  const ref = currentSessionRef(state);
  if (ref === currentSessionRef(previous)) return;
  // The workspace scope moved: an explicitly held column scope releases even
  // when retarget early-returns on the same ref, and the sidebar follows
  // focus again.
  activitySidebarStore.setState({ explicitScopeRef: null });
  activitySidebarStore.getState().retarget(ref);
});

export function activitySidebarReturnFocusTarget(tab: ActivityTab): HTMLElement | null {
  const visible = (element: HTMLElement | null): element is HTMLElement =>
    element?.isConnected === true && !element.matches(":disabled") && isRendered(element);
  if (visible(activitySidebarOpener)) return activitySidebarOpener;
  const actions = Array.from(document.querySelectorAll<HTMLElement>("[data-session-actions-ref]"))
    .filter((marker) => marker.dataset.sessionActionsRef === activitySidebarOpenerRef)
    .flatMap((marker) => {
      const button = marker.closest<HTMLButtonElement>("button");
      return visible(button) ? [{ button, paneId: marker.dataset.paneId }] : [];
    });
  const chips = Array.from(document.querySelectorAll<HTMLElement>("[data-activity-tab]")).filter(
    (chip) => chip.dataset.activityTab === tab && chip.dataset.sessionRef === activitySidebarOpenerRef && visible(chip),
  );
  return (
    actions.find((action) => action.paneId === activitySidebarOpenerPaneId)?.button ??
    chips.find((chip) => chip.dataset.paneId === activitySidebarOpenerPaneId) ??
    actions[0]?.button ??
    chips[0] ??
    Array.from(document.querySelectorAll<HTMLElement>("[data-session-navigation-ref]")).find(
      (control) =>
        control.dataset.sessionNavigationRef === activitySidebarOpenerRef &&
        !control.closest('[data-testid="activity-sidebar"]') &&
        visible(control),
    ) ??
    // Drilling moved focus to a pane the opener scoping cannot see, and the
    // read-only pane that used to render the opener's navigation link is
    // gone: the revealed conversation owns the return focus through its own
    // actions control.
    Array.from(document.querySelectorAll<HTMLElement>("[data-session-actions-ref]"))
      .find((marker) => {
        const button = marker.closest<HTMLButtonElement>("button");
        return visible(button) && marker.dataset.paneId === workspaceStore.getState().focusedPaneId;
      })
      ?.closest<HTMLButtonElement>("button") ??
    null
  );
}

export function useActivitySidebarStore<T>(selector: (state: ActivitySidebarState) => T): T {
  return useStore(activitySidebarStore, selector);
}

/** The scope the sidebar describes right now: an open request's explicitly
 * held column scope while it holds, else the focused scope. */
export function activitySidebarScopeRef(): string | null {
  return activitySidebarStore.getState().explicitScopeRef ?? focusedActivityScopeRef();
}

export function useActivitySidebarScopeRef(): string | null {
  const explicit = useActivitySidebarStore((state) => state.explicitScopeRef);
  const focused = useFocusedActivityScopeRef();
  return explicit ?? focused;
}

/** The Activity ✓ semantics, shared by the rail row and the session chrome:
 * the sidebar is open AND describing this session. Subscribes to BOTH inputs:
 * the sidebar store's open, and the displayed scope - a focus move can keep
 * the same open value, and without the scope subscription the ✓ keeps naming
 * the session the sidebar showed before the move (the menu holds its open
 * state internally, so re-opening it never re-renders the owner into a fresh
 * read). */
export function useActivitySidebarOpenFor(ref: string): boolean {
  const open = useActivitySidebarStore((state) => state.open);
  const scopeRef = useActivitySidebarScopeRef();
  return open && scopeRef === ref;
}

/** The imperative twin for non-React call sites. */
export function activitySidebarOpenFor(ref: string): boolean {
  return activitySidebarStore.getState().open && activitySidebarScopeRef() === ref;
}

// resetActivitySidebarStoreForTests restores the initial state between tests -
// mirrors chromeStore's resetChromeStoreForTests precedent. No production code
// should ever call this.
export function resetActivitySidebarStoreForTests({ preserveStorage = false } = {}): void {
  activitySidebarOpener = null;
  activitySidebarOpenerPaneId = null;
  activitySidebarOpenerRef = null;
  if (!preserveStorage) {
    try {
      localStorage.removeItem(ACTIVITY_VIEW_STORAGE_KEY);
    } catch {
      /* best effort */
    }
  }
  clearTimeout(saveTimer);
  saveTimer = undefined;
  hydrated = false;
  activitySidebarStore.setState({ open: false, tab: "agents", ref: null, explicitScopeRef: null, views: new Map() });
}
