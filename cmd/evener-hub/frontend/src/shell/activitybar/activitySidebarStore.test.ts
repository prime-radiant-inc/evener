import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import {
  ACTIVITY_VIEW_LIMIT,
  ACTIVITY_VIEW_STORAGE_KEY,
  activitySidebarReturnFocusTarget,
  activitySidebarScopeRef,
  activitySidebarStore,
  resetActivitySidebarStoreForTests,
} from "./activitySidebarStore";

beforeEach(() => installLocalStorage(new MemoryStorage()));

afterEach(() => {
  resetActivitySidebarStoreForTests();
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
});

test("About reload preserves another category's view intent", () => {
  const store = activitySidebarStore;
  store.getState().retarget("remote:a");
  store.getState().openWith("jobs");
  store.getState().setCategoryView("remote:a", "jobs", {
    shown: 60,
    anchor: { id: "later-page-job", offset: 17 },
  });
  store.getState().setTab("about");
  store.getState().close();
  resetActivitySidebarStoreForTests({ preserveStorage: true });
  store.getState().retarget("remote:a");
  expect(store.getState()).toMatchObject({ open: false, tab: "about", ref: "remote:a" });
  expect(store.getState().views.get("remote:a")?.categories.jobs).toEqual({
    shown: 60,
    anchor: { id: "later-page-job", offset: 17 },
  });
  store.getState().openWith();
  expect(store.getState()).toMatchObject({ open: true, tab: "about" });
});

test("empty workspace bootstrap and passive restore never overwrite retained intent", () => {
  const retained = { "source:owner": { open: true, tab: "jobs", categories: {} } };
  localStorage.setItem(ACTIVITY_VIEW_STORAGE_KEY, JSON.stringify(retained));
  const writes = vi.spyOn(localStorage, "setItem");
  workspaceStore.setState({ panes: [], focusedPaneId: null });
  activitySidebarStore.getState().retarget(null);
  expect(writes).not.toHaveBeenCalled();
  activitySidebarStore.getState().retarget("source:owner");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
  expect(writes).not.toHaveBeenCalled();
});

test("retargeting into an open view returns focus to the current navigation control", () => {
  const priorOpener = document.body.appendChild(document.createElement("button"));
  const currentOpener = document.body.appendChild(document.createElement("button"));
  try {
    const state = activitySidebarStore.getState();
    state.retarget("source:open");
    state.openWith("jobs");
    state.retarget("source:closed");
    state.close();
    state.openWith("agents", priorOpener);
    state.close();

    currentOpener.focus();
    state.retarget("source:open");

    expect(activitySidebarStore.getState().open).toBe(true);
    expect(activitySidebarReturnFocusTarget("jobs")).toBe(currentOpener);
  } finally {
    priorOpener.remove();
    currentOpener.remove();
  }
});

test("focus return falls back to a remaining activity chip when the opener pane disappears", () => {
  const remainingChip = document.body.appendChild(document.createElement("button"));
  remainingChip.dataset.activityTab = "jobs";
  remainingChip.dataset.paneId = "pane-remaining";
  remainingChip.dataset.sessionRef = "source:owner";
  const removedOpener = document.body.appendChild(document.createElement("button"));
  workspaceStore.setState({ focusedPaneId: "pane-removed" });
  activitySidebarStore.getState().retarget("source:owner");
  activitySidebarStore.getState().openWith("jobs", removedOpener);

  removedOpener.remove();

  try {
    expect(activitySidebarReturnFocusTarget("jobs")).toBe(remainingChip);
  } finally {
    remainingChip.remove();
  }
});

test("openFor preserves the focused pane when the session has multiple instances", () => {
  workspaceStore.setState({
    panes: [
      { id: "first-instance", type: "session", params: { ref: "remote:owner" }, slot: "main" },
      {
        id: "focused-instance",
        type: "session",
        params: { ref: "remote:owner", instance: "second" },
        slot: "secondary",
      },
    ],
    focusedPaneId: "focused-instance",
  });
  activitySidebarStore.getState().openFor("remote:owner", "about");
  expect(workspaceStore.getState().focusedPaneId).toBe("focused-instance");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, ref: "remote:owner", tab: "about" });
  // A focus-carried open holds no explicit scope: the sidebar follows focus.
  expect(activitySidebarStore.getState().explicitScopeRef).toBeNull();
});

test("an embedded column's explicit scope holds until the workspace scope moves or the sidebar closes", () => {
  workspaceStore.setState({ panes: [], focusedPaneId: null });
  activitySidebarStore.getState().openFor("remote:column", "agents");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, ref: "remote:column" });
  expect(activitySidebarScopeRef()).toBe("remote:column");
  // The workspace scope arriving at the held ref releases the hold even
  // though retarget early-returns on the same ref.
  workspaceStore.setState({
    panes: [{ id: "pane-child", type: "session", params: { ref: "remote:column" }, slot: "main" }],
    focusedPaneId: "pane-child",
  });
  expect(activitySidebarScopeRef()).toBe("remote:column");
  expect(activitySidebarStore.getState().explicitScopeRef).toBeNull();
  // Closing the sidebar ends the inspection: a chip open follows focus again.
  activitySidebarStore.getState().openFor("remote:column", "agents");
  activitySidebarStore.getState().close();
  expect(activitySidebarScopeRef()).toBe("remote:column");
  activitySidebarStore.getState().openWith("jobs");
  expect(activitySidebarStore.getState().explicitScopeRef).toBeNull();
});

test("closing the sidebar retargets its preference saves to the focused scope", () => {
  // A drilled cascade: the focused zoom names its selected leaf, so no
  // workspace scope change ever releases the held column itself.
  workspaceStore.setState({
    panes: [
      {
        id: "zoom",
        type: "sessionZoom",
        params: { ref: "remote:leaf", source: { type: "session", params: { ref: "remote:root" } }, edges: [] },
        slot: "main",
      },
    ],
    focusedPaneId: "zoom",
  });
  activitySidebarStore.getState().openFor("remote:column", "agents");
  activitySidebarStore.getState().setTab("watches");
  activitySidebarStore.getState().close();
  expect(activitySidebarStore.getState().explicitScopeRef).toBeNull();
  // Closing ends the column's inspection: later preference saves belong to
  // the focused conversation, not the retired column.
  expect(activitySidebarStore.getState().ref).toBe("remote:leaf");
  activitySidebarStore.getState().setTab("jobs");
  const saved = JSON.parse(localStorage.getItem(ACTIVITY_VIEW_STORAGE_KEY) ?? "{}");
  expect(saved["remote:leaf"]).toMatchObject({ open: false, tab: "jobs" });
  expect(saved["remote:column"]).toMatchObject({ open: false, tab: "watches" });
});

test("closing the held column does not reopen the focused leaf's saved-open Overview", () => {
  // The same drilled cascade as the retarget test above, but the focused
  // leaf's Overview was open before the column inspection began, so the
  // leaf's saved view carries open: true. Closing the sidebar is a
  // dismissal: the retarget back onto the leaf must not replay that old
  // preference and reopen the panel the user just closed.
  workspaceStore.setState({
    panes: [
      {
        id: "zoom",
        type: "sessionZoom",
        params: { ref: "remote:leaf", source: { type: "session", params: { ref: "remote:root" } }, edges: [] },
        slot: "main",
      },
    ],
    focusedPaneId: "zoom",
  });
  activitySidebarStore.getState().openFor("remote:leaf", "jobs");
  activitySidebarStore.getState().openFor("remote:column", "agents");
  activitySidebarStore.getState().close();
  expect(activitySidebarStore.getState().open).toBe(false);
  expect(activitySidebarStore.getState().ref).toBe("remote:leaf");
  // The dismissal records as the focused conversation's explicit close
  // choice, so a later retarget does not reopen it either.
  activitySidebarStore.getState().setTab("jobs");
  const saved = JSON.parse(localStorage.getItem(ACTIVITY_VIEW_STORAGE_KEY) ?? "{}");
  expect(saved["remote:leaf"]).toMatchObject({ open: false, tab: "jobs" });
  expect(saved["remote:column"]).toMatchObject({ open: false, tab: "agents" });
});

test("a focus-based open request releases the held column scope and saves against its own session", () => {
  // A drilled cascade with an ordinary pane for its selected leaf: the
  // focused scope never moves, so only the request itself can release the
  // held column scope.
  workspaceStore.setState({
    panes: [
      {
        id: "zoom",
        type: "sessionZoom",
        params: { ref: "remote:leaf", source: { type: "session", params: { ref: "remote:root" } }, edges: [] },
        slot: "main",
      },
      { id: "pane-leaf", type: "session", params: { ref: "remote:leaf" }, slot: "secondary" },
    ],
    focusedPaneId: "zoom",
  });
  activitySidebarStore.getState().openFor("remote:column", "agents");
  activitySidebarStore.getState().setTab("watches");
  // The footer chip's request: focus the ordinary pane, then open its
  // conversation's overview naming it as the source.
  workspaceStore.getState().focusPane("pane-leaf");
  activitySidebarStore.getState().openFor("remote:leaf", "jobs", undefined, "pane-leaf");
  expect(activitySidebarStore.getState().explicitScopeRef).toBeNull();
  expect(activitySidebarStore.getState().ref).toBe("remote:leaf");
  activitySidebarStore.getState().setTab("tasks");
  const saved = JSON.parse(localStorage.getItem(ACTIVITY_VIEW_STORAGE_KEY) ?? "{}");
  expect(saved["remote:leaf"]).toMatchObject({ open: true, tab: "tasks" });
  expect(saved["remote:column"]).toMatchObject({ open: true, tab: "watches" });
});

function sessionActions(parent: HTMLElement, ref: string, paneId: string): HTMLButtonElement {
  const button = parent.appendChild(document.createElement("button"));
  const marker = button.appendChild(document.createElement("span"));
  marker.dataset.sessionActionsRef = ref;
  marker.dataset.paneId = paneId;
  return button;
}

test.each(["removed", "hidden ancestor", "invisible ancestor", "disabled"])(
  "About returns to its originating pane's visible action when its opener is %s",
  (condition) => {
    const root = document.body.appendChild(document.createElement("div"));
    const ref = 'remote:owner"]';
    try {
      sessionActions(root, "remote:other", "origin");
      sessionActions(root, ref, "other-pane");
      const fallback = sessionActions(root, ref, "origin");
      const holder = root.appendChild(document.createElement("div"));
      const opener = holder.appendChild(document.createElement("button"));
      workspaceStore.setState({ focusedPaneId: "origin" });
      activitySidebarStore.getState().retarget(ref);
      opener.focus();
      activitySidebarStore.getState().openWith("about");
      expect(activitySidebarReturnFocusTarget("about")).toBe(opener);
      if (condition === "removed") opener.remove();
      if (condition === "hidden ancestor") holder.style.display = "none";
      if (condition === "invisible ancestor") holder.style.visibility = "hidden";
      if (condition === "disabled") opener.disabled = true;
      expect(activitySidebarReturnFocusTarget("about")).toBe(fallback);
      activitySidebarStore.getState().setTab("jobs");
      activitySidebarStore.getState().setTab("about");
      expect(activitySidebarReturnFocusTarget("about")).toBe(fallback);
    } finally {
      root.remove();
    }
  },
);

test("return focus prefers the originating pane's chip before another pane's action for the same ref", () => {
  const root = document.body.appendChild(document.createElement("div"));
  try {
    const focusedAction = sessionActions(root, "remote:other", "origin");
    const otherAction = sessionActions(root, "remote:owner", "other-pane");
    const chip = root.appendChild(document.createElement("button"));
    chip.dataset.activityTab = "jobs";
    chip.dataset.paneId = "origin";
    chip.dataset.sessionRef = "remote:owner";
    const opener = root.appendChild(document.createElement("button"));
    workspaceStore.setState({ focusedPaneId: "origin" });
    activitySidebarStore.getState().retarget("remote:owner");
    activitySidebarStore.getState().openWith("jobs", opener);
    opener.remove();
    expect(activitySidebarReturnFocusTarget("jobs")).toBe(chip);
    chip.hidden = true;
    expect(activitySidebarReturnFocusTarget("jobs")).toBe(otherAction);
    otherAction.disabled = true;
    // The revealed pane owns the last resort: the read-only crumb the old
    // chain ended on is gone, so the focused pane's own action takes focus.
    expect(activitySidebarReturnFocusTarget("jobs")).toBe(focusedAction);
  } finally {
    root.remove();
  }
});

test("an internal Overview opener does not replace its external return target", () => {
  const root = document.body.appendChild(document.createElement("div"));
  try {
    const opener = sessionActions(root, "remote:owner", "origin");
    const sidebar = root.appendChild(document.createElement("aside"));
    sidebar.dataset.testid = "activity-sidebar";
    const internal = sidebar.appendChild(document.createElement("button"));
    workspaceStore.setState({ focusedPaneId: "origin" });
    activitySidebarStore.getState().retarget("remote:owner");
    activitySidebarStore.getState().openWith("about", opener);
    internal.focus();
    activitySidebarStore.getState().openWith("jobs");
    activitySidebarStore.getState().setTab("about");
    expect(activitySidebarReturnFocusTarget("about")).toBe(opener);
  } finally {
    root.remove();
  }
});

test("return focus prefers the originating pane's action to its category chip", () => {
  const root = document.body.appendChild(document.createElement("div"));
  try {
    const action = sessionActions(root, "remote:owner", "origin");
    const chip = root.appendChild(document.createElement("button"));
    chip.dataset.activityTab = "jobs";
    chip.dataset.paneId = "origin";
    chip.dataset.sessionRef = "remote:owner";
    workspaceStore.setState({ focusedPaneId: "origin" });
    activitySidebarStore.getState().retarget("remote:owner");
    const opener = root.appendChild(document.createElement("button"));
    activitySidebarStore.getState().openWith("jobs", opener);
    opener.remove();
    expect(activitySidebarReturnFocusTarget("jobs")).toBe(action);
    action.hidden = true;
    expect(activitySidebarReturnFocusTarget("jobs")).toBe(chip);
  } finally {
    root.remove();
  }
});

test("return focus retains the intended ref when the open inspection retargets", () => {
  const root = document.body.appendChild(document.createElement("div"));
  try {
    sessionActions(root, "remote:other", "origin");
    const action = sessionActions(root, "remote:owner", "origin");
    const opener = root.appendChild(document.createElement("button"));
    workspaceStore.setState({ focusedPaneId: "origin" });
    activitySidebarStore.getState().retarget("remote:owner");
    activitySidebarStore.getState().openWith("about", opener);
    opener.remove();
    activitySidebarStore.getState().retarget("remote:other");
    expect(activitySidebarReturnFocusTarget("about")).toBe(action);
  } finally {
    root.remove();
  }
});

test.each(["visible", "hidden", "disabled", "inside Overview", "wrong ref"])(
  "return focus qualifies its last navigation fallback when it is %s",
  (condition) => {
    const root = document.body.appendChild(document.createElement("div"));
    const ref = 'remote:parent"]';
    try {
      const navigation = root.appendChild(document.createElement("button"));
      navigation.dataset.sessionNavigationRef = condition === "wrong ref" ? "remote:other" : ref;
      if (condition === "hidden") navigation.hidden = true;
      if (condition === "disabled") navigation.disabled = true;
      if (condition === "inside Overview") root.dataset.testid = "activity-sidebar";
      const action = sessionActions(document.body, ref, "origin");
      try {
        const opener = document.body.appendChild(document.createElement("button"));
        workspaceStore.setState({ focusedPaneId: "origin" });
        activitySidebarStore.getState().retarget(ref);
        activitySidebarStore.getState().openWith("about", opener);
        opener.remove();
        activitySidebarStore.getState().retarget("remote:child");
        expect(activitySidebarReturnFocusTarget("about")).toBe(action);
        action.remove();
        expect(activitySidebarReturnFocusTarget("about")).toBe(condition === "visible" ? navigation : null);
      } finally {
        action.remove();
      }
    } finally {
      root.remove();
    }
  },
);

test("a focus change while the desktop sidebar is unmounted does not persist inherited open intent", () => {
  vi.useFakeTimers();
  try {
    const state = activitySidebarStore.getState();
    state.retarget("source:parent");
    state.openWith("jobs");
    state.retarget("source:other");
    state.openWith("jobs");
    const writes = vi.spyOn(localStorage, "setItem");
    state.retarget("source:parent");
    vi.runOnlyPendingTimers();
    window.dispatchEvent(new Event("pagehide"));
    expect(writes).not.toHaveBeenCalled();
    state.retarget("source:child");
    expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
    vi.runOnlyPendingTimers();
    window.dispatchEvent(new Event("pagehide"));
    expect(writes).not.toHaveBeenCalled();
  } finally {
    vi.useRealTimers();
  }
});

test.each(["{broken", "[]", '{"source:owner":{"open":true,"tab":"unknown"}}'])(
  "malformed saved state %s leaves the live controls usable",
  (raw) => {
    localStorage.setItem(ACTIVITY_VIEW_STORAGE_KEY, raw);
    activitySidebarStore.getState().retarget("source:owner");
    expect(activitySidebarStore.getState().open).toBe(false);
    activitySidebarStore.getState().openWith("tasks");
    expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "tasks" });
  },
);

test("blocked storage preserves current open, tab and close choices", () => {
  vi.spyOn(localStorage, "getItem").mockImplementation(() => {
    throw new Error("blocked");
  });
  vi.spyOn(localStorage, "setItem").mockImplementation(() => {
    throw new Error("full");
  });
  activitySidebarStore.getState().retarget("source:owner");
  activitySidebarStore.getState().openWith("jobs");
  activitySidebarStore.getState().setTab("watches");
  activitySidebarStore.getState().close();
  expect(activitySidebarStore.getState()).toMatchObject({ open: false, tab: "watches" });
});

test("retained session intent is bounded and a recently revisited scope keeps its choices", () => {
  const state = activitySidebarStore.getState();
  for (let index = 0; index < ACTIVITY_VIEW_LIMIT; index++) {
    state.retarget(`source:${index}`);
    state.openWith("jobs");
  }
  state.retarget("source:0");
  state.setTab("watches");
  state.retarget("source:new");
  state.openWith("tasks");
  expect(activitySidebarStore.getState().views.size).toBe(ACTIVITY_VIEW_LIMIT);
  const saved = JSON.parse(localStorage.getItem(ACTIVITY_VIEW_STORAGE_KEY) ?? "{}");
  expect(Object.keys(saved)).toHaveLength(ACTIVITY_VIEW_LIMIT);
  expect(saved["source:1"]).toBeUndefined();
  expect(saved["source:0"].tab).toBe("watches");
});

test.each(["pagehide", "timer"])(
  "a committed revisit saved by %s survives reload before capacity eviction",
  (boundary) => {
    vi.useFakeTimers();
    try {
      const state = activitySidebarStore.getState();
      for (let index = 0; index < ACTIVITY_VIEW_LIMIT; index++) {
        state.retarget(`source:${index}`);
        state.openWith(index === 0 ? "watches" : "jobs");
      }
      const otherRef = `source:${ACTIVITY_VIEW_LIMIT - 1}`;
      for (const ref of ["source:0", otherRef]) {
        state.retarget(ref);
        state.retainOpenView(ref);
        if (boundary === "pagehide") window.dispatchEvent(new Event("pagehide"));
        else vi.runOnlyPendingTimers();
      }

      resetActivitySidebarStoreForTests({ preserveStorage: true });
      state.retarget(otherRef);
      state.retainOpenView(otherRef);
      state.retarget("source:new");
      state.openWith("tasks");

      const saved = JSON.parse(localStorage.getItem(ACTIVITY_VIEW_STORAGE_KEY) ?? "{}");
      expect(saved["source:0"]).toEqual({ open: true, tab: "watches", categories: {} });
      expect(saved["source:1"]).toBeUndefined();
      expect(Object.keys(saved)).toHaveLength(ACTIVITY_VIEW_LIMIT);
      expect(activitySidebarStore.getState().views.size).toBe(ACTIVITY_VIEW_LIMIT);
    } finally {
      vi.useRealTimers();
    }
  },
);

test("committed revisit writes are deferred and an already newest view remains a no-op", () => {
  vi.useFakeTimers();
  try {
    const state = activitySidebarStore.getState();
    state.retarget("source:owner");
    state.openWith("jobs");
    state.retarget("source:other");
    state.openWith("jobs");
    const writes = vi.spyOn(localStorage, "setItem");
    state.retarget("source:owner");
    state.retainOpenView("source:owner");
    state.retainOpenView("source:owner");
    expect(writes).not.toHaveBeenCalled();
    vi.runOnlyPendingTimers();
    expect(writes).toHaveBeenCalledTimes(1);

    state.retainOpenView("source:owner");
    vi.runOnlyPendingTimers();
    window.dispatchEvent(new Event("pagehide"));
    expect(writes).toHaveBeenCalledTimes(1);
  } finally {
    vi.useRealTimers();
  }
});

test("equivalent scroll and disclosure-count choices do not rewrite storage or notify the view", () => {
  const state = activitySidebarStore.getState();
  state.retarget("source:owner");
  state.setCategoryView("source:owner", "agents", { anchor: { id: "delegate:one", offset: -12 }, shown: 40 });
  const snapshot = activitySidebarStore.getState();
  const writes = vi.spyOn(localStorage, "setItem");
  state.setCategoryView("source:owner", "agents", { anchor: { id: "delegate:one", offset: -12 } });
  state.setCategoryView("source:owner", "agents", { shown: 40 });
  expect(writes).not.toHaveBeenCalled();
  expect(activitySidebarStore.getState()).toBe(snapshot);
  state.setCategoryView("source:owner", "agents", { anchor: { id: "delegate:one", offset: -13 } });
  expect(writes).not.toHaveBeenCalled();
  window.dispatchEvent(new Event("pagehide"));
  expect(writes).toHaveBeenCalledTimes(1);
});

describe("activitySidebarStore", () => {
  test("starts closed on the agents tab", () => {
    expect(activitySidebarStore.getState().open).toBe(false);
    expect(activitySidebarStore.getState().tab).toBe("agents");
  });

  test("openWith opens onto the given tab", () => {
    activitySidebarStore.getState().openWith("watches");
    expect(activitySidebarStore.getState().open).toBe(true);
    expect(activitySidebarStore.getState().tab).toBe("watches");
  });

  test("openWith with no tab keeps the current tab", () => {
    activitySidebarStore.getState().setTab("jobs");
    activitySidebarStore.getState().openWith();
    expect(activitySidebarStore.getState().tab).toBe("jobs");
  });

  test("close keeps the tab for the next open", () => {
    activitySidebarStore.getState().openWith("tasks");
    activitySidebarStore.getState().close();
    expect(activitySidebarStore.getState().open).toBe(false);
    activitySidebarStore.getState().openWith();
    expect(activitySidebarStore.getState().tab).toBe("tasks");
  });

  test("toggle flips open and keeps the tab", () => {
    activitySidebarStore.getState().setTab("watches");
    activitySidebarStore.getState().toggle();
    expect(activitySidebarStore.getState().open).toBe(true);
    expect(activitySidebarStore.getState().tab).toBe("watches");
    activitySidebarStore.getState().toggle();
    expect(activitySidebarStore.getState().open).toBe(false);
  });
});

test("scroll bursts retain latest intent without synchronous persistence per frame", () => {
  vi.useFakeTimers();
  try {
    const state = activitySidebarStore.getState();
    state.retarget("source:owner");
    state.openWith("jobs");
    const writes = vi.spyOn(localStorage, "setItem");
    for (let offset = 0; offset < 20; offset++)
      state.setCategoryView("source:owner", "jobs", { anchor: { id: "row", offset } });
    expect(activitySidebarStore.getState().views.get("source:owner")?.categories.jobs?.anchor?.offset).toBe(19);
    expect(writes).not.toHaveBeenCalled();
    vi.runOnlyPendingTimers();
    expect(writes).toHaveBeenCalledTimes(1);
    expect(
      JSON.parse(localStorage.getItem(ACTIVITY_VIEW_STORAGE_KEY) ?? "{}")["source:owner"].categories.jobs.anchor.offset,
    ).toBe(19);
  } finally {
    vi.useRealTimers();
  }
});

test.each(["close", "category", "retarget", "pagehide", "shown"])(
  "%s flushes the latest pending anchor and absorbs its timer",
  (boundary) => {
    vi.useFakeTimers();
    try {
      const state = activitySidebarStore.getState();
      state.retarget("source:owner");
      state.openWith("jobs");
      state.setCategoryView("source:owner", "jobs", { anchor: { id: "row", offset: 19 } });
      const writes = vi.spyOn(localStorage, "setItem");
      if (boundary === "close") state.close();
      if (boundary === "category") state.setTab("watches");
      if (boundary === "retarget") state.retarget("source:child");
      if (boundary === "pagehide") window.dispatchEvent(new Event("pagehide"));
      if (boundary === "shown") state.setCategoryView("source:owner", "jobs", { shown: 40 });
      const saved = JSON.parse(localStorage.getItem(ACTIVITY_VIEW_STORAGE_KEY) ?? "{}");
      expect(saved["source:owner"].categories.jobs.anchor.offset).toBe(19);
      expect(writes).toHaveBeenCalledTimes(1);
      vi.runOnlyPendingTimers();
      expect(writes).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  },
);

test("reset cancels pending persistence and blocked storage leaves current anchors usable", () => {
  vi.useFakeTimers();
  try {
    const state = activitySidebarStore.getState();
    state.retarget("source:owner");
    state.openWith("jobs");
    state.setCategoryView("source:owner", "jobs", { anchor: { id: "row", offset: 12 } });
    const writes = vi.spyOn(localStorage, "setItem");
    resetActivitySidebarStoreForTests();
    vi.runOnlyPendingTimers();
    expect(writes).not.toHaveBeenCalled();
    writes.mockImplementation(() => {
      throw new Error("full");
    });
    state.retarget("source:other");
    state.openWith("jobs");
    state.setCategoryView("source:other", "jobs", { anchor: { id: "other-row", offset: 7 } });
    expect(() => window.dispatchEvent(new Event("pagehide"))).not.toThrow();
    expect(activitySidebarStore.getState().views.get("source:other")?.categories.jobs?.anchor?.offset).toBe(7);
    expect(vi.getTimerCount()).toBe(0);
  } finally {
    vi.useRealTimers();
  }
});
