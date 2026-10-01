import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import {
  ACTIVITY_VIEW_LIMIT,
  ACTIVITY_VIEW_STORAGE_KEY,
  activitySidebarStore,
  resetActivitySidebarStoreForTests,
} from "./activitySidebarStore";

beforeEach(() => installLocalStorage(new MemoryStorage()));

afterEach(() => {
  resetActivitySidebarStoreForTests();
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
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

test("a focus change while the desktop sidebar is unmounted does not persist inherited open intent", () => {
  const state = activitySidebarStore.getState();
  state.retarget("source:parent");
  state.openWith("jobs");
  const writes = vi.spyOn(localStorage, "setItem");
  state.retarget("source:child");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
  expect(writes).not.toHaveBeenCalled();
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
