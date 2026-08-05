import type { DockviewApi } from "dockview-core";
import { type ComponentType, lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { prefsStore, resetPrefsStoreForTests } from "../stores/prefs";
import { inheritOpenerTheme, openBeside, popOutPane, togglePane } from "./paneActions";
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
  resetPrefsStoreForTests();
});

afterEach(() => {
  // The popout-theme tests set data-theme on the shared jsdom root; clear it so
  // it never leaks into another test's opener-theme reading.
  document.documentElement.removeAttribute("data-theme");
});

// A minimal DockviewApi stand-in: openBeside only asks "is there a host at
// all" (non-null), so most tests just need a non-null object; popOutPane needs
// getPanel + addPopoutGroup. getPopouts defaults to empty - the module-level
// theme-resync observer (paneActions.ts) calls it on every data-theme
// mutation as long as ANY api is registered, including in tests that
// registered one for something else entirely and never expected it read.
// Cast through unknown - the real api is far wider than any one function
// here reads.
function fakeApi(overrides: Partial<DockviewApi> = {}): DockviewApi {
  return { getPopouts: () => [], ...overrides } as unknown as DockviewApi;
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

test("openBeside opens the pane in the secondary group, leaving the main pane in place (desktop host present)", () => {
  registerDockviewApi(fakeApi());
  const first = workspaceStore.getState().openPane("doc", { ref: "a" });

  openBeside({ type: "doc", params: { ref: "b" } });

  const second = workspaceStore.getState().panes.find((p) => p.id !== first);
  expect(second).toBeDefined();
  expect(second?.slot).toBe("secondary"); // beside the main pane, not stacked on it
  expect(workspaceStore.getState().mainPane()?.id).toBe(first); // main pane untouched
  expect(workspaceStore.getState().focusedPaneId).toBe(second?.id); // and focused
});

test("togglePane closes the same logical pane after it has moved outside the grid", () => {
  const pane = workspaceStore.getState().openPane("doc", { ref: "a" });
  const api = fakeApi({ getPanel: ((id: string) => (id === pane ? { id } : undefined)) as DockviewApi["getPanel"] });
  registerDockviewApi(api);

  expect(togglePane({ type: "doc", params: { ref: "a" } })).toEqual({ paneId: pane, opened: false });
  expect(workspaceStore.getState().panes).toEqual([]);
});

test("openBeside dedups an already-open pane, focusing it instead of opening a duplicate split", () => {
  registerDockviewApi(fakeApi());
  const first = workspaceStore.getState().openPane("doc", { ref: "a" });
  workspaceStore.getState().openPane("doc", { ref: "b" }); // a second, now-focused pane

  openBeside({ type: "doc", params: { ref: "a" } }); // re-open the first

  expect(workspaceStore.getState().panes).toHaveLength(2); // no third pane
  expect(workspaceStore.getState().focusedPaneId).toBe(first); // existing one focused
});

test("openBeside still opens (and focuses) with no dockview host at all (mobile StackHost)", () => {
  registerDockviewApi(null); // StackHost registers no api - the mobile signal
  const first = workspaceStore.getState().openPane("doc", { ref: "a" });

  openBeside({ type: "doc", params: { ref: "b" } });

  const second = workspaceStore.getState().panes.find((p) => p.id !== first);
  expect(second).toBeDefined();
  // Mobile has no groups to split; the pane simply becomes the focused,
  // full-screen screen the stack shows.
  expect(workspaceStore.getState().focusedPaneId).toBe(second?.id);
});

test("openBeside into an empty workspace takes the main slot - there is nothing yet to sit beside", () => {
  registerDockviewApi(fakeApi());

  openBeside({ type: "doc", params: { ref: "a" } });

  expect(workspaceStore.getState().panes[0]?.slot).toBe("main");
});

test("popOutPane promotes the named pane's panel to a dockview popout window", () => {
  const panel = { id: "pane_doc_1" };
  const { api, addPopoutGroup } = popoutApi(panel);
  registerDockviewApi(api);

  popOutPane("pane_doc_1");

  // The panel is the first arg; the second is the popout options carrying the
  // onDidOpen theme hook (see the theme-inheritance test below).
  expect(addPopoutGroup).toHaveBeenCalledWith(panel, expect.objectContaining({ onDidOpen: expect.any(Function) }));
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

test("inheritOpenerTheme copies a light opener root's data-theme onto the popout root", () => {
  const opener = document.createElement("html");
  opener.setAttribute("data-theme", "light");
  const popout = document.createElement("html");

  inheritOpenerTheme(opener, popout);

  expect(popout.getAttribute("data-theme")).toBe("light");
});

test("inheritOpenerTheme leaves the popout root bare when the opener has no data-theme (dark default)", () => {
  const opener = document.createElement("html"); // dark default carries no attribute
  const popout = document.createElement("html");

  inheritOpenerTheme(opener, popout);

  expect(popout.hasAttribute("data-theme")).toBe(false);
});

test("popOutPane copies the opener's theme onto the popout document once it loads", () => {
  const panel = { id: "pane_doc_1" };
  const { api, addPopoutGroup } = popoutApi(panel);
  registerDockviewApi(api);
  document.documentElement.setAttribute("data-theme", "light");

  popOutPane("pane_doc_1");

  // dockview calls onDidOpen with the popout window BEFORE it navigates to
  // /popout.html, so the copy is deferred to that window's load. Drive the
  // captured hook, then fire load, and assert the popout root inherited light.
  const options = addPopoutGroup.mock.calls[0]?.[1] as { onDidOpen: (e: { id: string; window: Window }) => void };
  const popoutDoc = document.implementation.createHTMLDocument("");
  const loadHandlers: Array<() => void> = [];
  const fakeWindow = {
    document: popoutDoc,
    addEventListener: (type: string, cb: () => void) => {
      if (type === "load") loadHandlers.push(cb);
    },
  } as unknown as Window;

  options.onDidOpen({ id: "dv-1", window: fakeWindow });
  for (const cb of loadHandlers) cb();

  expect(popoutDoc.documentElement.getAttribute("data-theme")).toBe("light");
});

test("flipping the opener's theme after a popout is open re-syncs the popout's data-theme (y2ct)", async () => {
  // Same fake shape popOutPane's own theme-inheritance test above already
  // uses for a popout window; getPopouts() is dockview's own live-enumeration
  // API (component.api.d.ts) - a real popout registers with it the instant
  // addPopoutGroup resolves, before this test ever runs.
  const popoutDoc = document.implementation.createHTMLDocument("");
  const api = fakeApi({
    getPopouts: (() => [
      { id: "dv-1", group: {}, window: { document: popoutDoc } },
    ]) as unknown as DockviewApi["getPopouts"],
  });
  registerDockviewApi(api);
  document.documentElement.setAttribute("data-theme", "light"); // opener starts light

  prefsStore.getState().setTheme("dark"); // flip in the opener, popout already open

  // The re-sync is a MutationObserver callback - a microtask, not synchronous
  // with the attribute write above.
  await vi.waitFor(() => {
    expect(popoutDoc.documentElement.getAttribute("data-theme")).toBe("dark");
  });
});

test("flipping the opener's theme with no popout open is a safe no-op (nothing to walk, never throws)", async () => {
  registerDockviewApi(fakeApi({ getPopouts: (() => []) as unknown as DockviewApi["getPopouts"] }));

  expect(() => prefsStore.getState().setTheme("light")).not.toThrow();
  await Promise.resolve(); // let the observer callback run; still nothing to assert on
});

test("flipping the opener's theme with no dockview host at all is a safe no-op (mobile StackHost)", async () => {
  registerDockviewApi(null); // StackHost registers no api - the mobile signal

  expect(() => prefsStore.getState().setTheme("light")).not.toThrow();
  await Promise.resolve(); // let the observer callback run; still nothing to assert on
});
