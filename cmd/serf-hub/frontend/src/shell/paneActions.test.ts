import type { DockviewApi } from "dockview-core";
import { type ComponentType, lazy } from "react";
import { beforeAll, beforeEach, expect, test, vi } from "vitest";
import { openBeside, popOutPane } from "./paneActions";
import { type PaneDescriptor, type PaneProps, registerPane } from "./paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

// A never-resolving lazy component: openBeside/popOutPane never render a pane,
// they only mutate the workspace store / call the dockview api, so the fixture
// pane type needs a real descriptor shape but never a real component (mirrors
// workspace.test.ts's own fixtureDescriptor).
function fixtureDescriptor<P>(
  id: PaneDescriptor<P>["id"],
  overrides: Partial<PaneDescriptor<P>> = {},
): PaneDescriptor<P> {
  return {
    id,
    title: () => `title for ${id}`,
    component: lazy(() => new Promise<{ default: ComponentType<PaneProps<P>> }>(() => {})),
    ...overrides,
  };
}

// "doc" is a real (non-singleton) PaneTypeId - the union is closed, so an
// open-beside fixture must be drawn from it. The read-only "transcript" pane
// this stream registers for real is left out here so these tests exercise
// openBeside's store/split wiring, not that pane's own registration.
beforeAll(() => {
  registerPane(fixtureDescriptor("doc"));
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

// A minimal DockviewApi stand-in: openBeside only asks "is there a host at
// all" (non-null), so most tests just need a non-null object; popOutPane needs
// getPanel + addPopoutGroup. Cast through unknown - the real api is far wider
// than either function reads.
function fakeApi(overrides: Partial<DockviewApi> = {}): DockviewApi {
  return overrides as unknown as DockviewApi;
}

// A DockviewApi that resolves getPanel(id) to `panel` (and nothing else) and
// records addPopoutGroup calls - the two methods popOutPane touches.
function popoutApi(panel: { id: string } | undefined): { api: DockviewApi; addPopoutGroup: ReturnType<typeof vi.fn> } {
  const addPopoutGroup = vi.fn(() => Promise.resolve(true));
  const getPanel = (id: string) => (panel && id === panel.id ? panel : undefined);
  const api = fakeApi({
    getPanel: getPanel as DockviewApi["getPanel"],
    addPopoutGroup: addPopoutGroup as unknown as DockviewApi["addPopoutGroup"],
  });
  return { api, addPopoutGroup };
}

test("openBeside opens the pane in a split beside the currently-focused pane (desktop host present)", () => {
  registerDockviewApi(fakeApi());
  const first = workspaceStore.getState().openPane("doc", { ref: "a" });

  openBeside({ type: "doc", params: { ref: "b" } });

  const second = workspaceStore.getState().panes.find((p) => p.id !== first);
  expect(second).toBeDefined();
  expect(second?.beside).toBe(first); // split beside the pane that was focused
  expect(workspaceStore.getState().focusedPaneId).toBe(second?.id); // and focused
});

test("openBeside dedups an already-open pane, focusing it instead of opening a duplicate split", () => {
  registerDockviewApi(fakeApi());
  const first = workspaceStore.getState().openPane("doc", { ref: "a" });
  workspaceStore.getState().openPane("doc", { ref: "b" }); // a second, now-focused pane

  openBeside({ type: "doc", params: { ref: "a" } }); // re-open the first

  expect(workspaceStore.getState().panes).toHaveLength(2); // no third pane
  expect(workspaceStore.getState().focusedPaneId).toBe(first); // existing one focused
});

test("openBeside degrades to a plain open (no split) when there is no dockview host (mobile StackHost)", () => {
  registerDockviewApi(null); // StackHost registers no api - the mobile signal
  const first = workspaceStore.getState().openPane("doc", { ref: "a" });

  openBeside({ type: "doc", params: { ref: "b" } });

  const second = workspaceStore.getState().panes.find((p) => p.id !== first);
  expect(second?.beside).toBeUndefined(); // no split hint on mobile
  expect(workspaceStore.getState().focusedPaneId).toBe(second?.id); // still becomes the focused (full-screen) pane
});

test("openBeside opens with no split hint when nothing is focused yet, even with a host", () => {
  registerDockviewApi(fakeApi());

  openBeside({ type: "doc", params: { ref: "a" } });

  expect(workspaceStore.getState().panes[0]?.beside).toBeUndefined();
});

test("popOutPane promotes the named pane's panel to a dockview popout window", () => {
  const panel = { id: "pane_doc_1" };
  const { api, addPopoutGroup } = popoutApi(panel);
  registerDockviewApi(api);

  popOutPane("pane_doc_1");

  expect(addPopoutGroup).toHaveBeenCalledWith(panel);
});

test("popOutPane is a safe no-op when there is no dockview host", () => {
  registerDockviewApi(null);
  expect(() => popOutPane("pane_doc_1")).not.toThrow();
});

test("popOutPane is a safe no-op for an unknown (closed) pane id - never pops out a phantom", () => {
  const { api, addPopoutGroup } = popoutApi(undefined);
  registerDockviewApi(api);

  expect(() => popOutPane("nope")).not.toThrow();
  expect(addPopoutGroup).not.toHaveBeenCalled();
});
