import { bindFilePath } from "@evener/appwire-client/docContent";
import { act, cleanup, render } from "@testing-library/react";
import type { DockviewApi } from "dockview-core";
import { lazy } from "react";
import { afterEach, beforeAll, expect, test } from "vitest";
import { openDocBeside } from "../panes/doc/openDoc";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { DockHost } from "./DockHost";
import { type PaneProps, registerPaneForTests } from "./paneRegistry";
import {
  documentPaneState,
  getDockviewApi,
  recordDocumentPaneState,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "./workspace";

// Real host, workspace, document opener and pinned Dockview. Only pane bodies
// and browser resize/storage ports are fixtures, so these assert group topology,
// panel/record identity and selection, not browser pixel or reader behavior.
function Body({ paneId }: PaneProps) {
  return <div data-testid={paneId}>geometry fixture</div>;
}

beforeAll(() => {
  globalThis.ResizeObserver = StubResizeObserver;
  installLocalStorage(new MemoryStorage());
  for (const id of ["settings", "session", "doc"] as const) {
    registerPaneForTests({
      id,
      singleton: id === "settings",
      title: () => id,
      component: lazy(() => Promise.resolve({ default: Body })),
    });
  }
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  localStorage.clear();
});

const store = workspaceStore;
const layoutKey = "evener.workspace.layout.v2";

function panel(api: DockviewApi, id: string) {
  const result = api.getPanel(id);
  if (!result) throw new Error(`real panel missing: ${id}`);
  return result;
}

function record(id: string) {
  const result = store.getState().panes.find((pane) => pane.id === id);
  if (!result) throw new Error(`living record missing: ${id}`);
  return result;
}

async function mount() {
  const host = await act(async () => render(<DockHost />));
  const api = getDockviewApi();
  if (!api) throw new Error("real Dockview API was not registered");
  await act(async () => api.layout(1200, 700));
  return { host, api };
}

async function splitWorkspace() {
  const main = store.getState().openPane("settings", { section: "appearance" });
  const { host, api } = await mount();
  const a = await act(async () => store.getState().openPane("doc", { path: "a.md" }));
  const b = await act(async () => store.getState().openPane("doc", { path: "b.md" }));
  expect(api.groups).toHaveLength(2);
  expect(panel(api, a).group).toBe(panel(api, b).group);
  await act(async () => {
    panel(api, b).api.moveTo({ group: panel(api, a).group, position: "right", skipSetActive: true });
    api.layout(1200, 700);
  });
  expect(panel(api, a).group.id).not.toBe(panel(api, b).group.id);
  expect(api.groups).toHaveLength(3);
  expect(document.querySelectorAll(".dv-groupview")).toHaveLength(3);
  return { host, api, main, a, b };
}

async function splitAgain(api: DockviewApi, a: string, b: string) {
  await act(async () => {
    if (panel(api, a).group === panel(api, b).group) {
      panel(api, b).api.moveTo({ group: panel(api, a).group, position: "right", skipSetActive: true });
    }
    api.layout(1200, 700);
  });
  expect(panel(api, a).group).not.toBe(panel(api, b).group);
  expect(api.groups).toHaveLength(3);
}

function placements(api: DockviewApi) {
  return new Map(api.panels.map((item) => [item.id, item.group.id]));
}

function expectMain(api: DockviewApi, id: string) {
  expect(panel(api, id).group.panels.map((item) => item.id)).toEqual([id]);
  let node = api.toJSON().grid.root;
  while (Array.isArray(node.data)) {
    const first = node.data[0];
    if (!first) throw new Error("main grid is empty");
    node = first;
  }
  expect(node.data.views).toEqual([id]);
  expect(panel(api, id).group.model.header.hidden).toBe(true);
  for (const group of api.groups.filter((item) => item !== panel(api, id).group)) {
    expect(group.model.header.hidden).toBe(false);
  }
}

test.each(["singleton params", "document binding", "add", "remove", "record replacement"] as const)(
  "unchanged-slot user splits survive %s publication",
  async (mutation) => {
    const { api, main, a, b } = await splitWorkspace();
    const spare = await act(async () => store.getState().openPane("doc", { path: "spare.md" }));
    await splitAgain(api, a, b);
    await act(async () => store.getState().focusPane(b));
    const before = placements(api);
    const records = [record(a), record(b)];
    const panels = [panel(api, a), panel(api, b)];
    const slots = store.getState().panes.map((item) => [item.id, item.slot]);
    const focus = store.getState().focusedPaneId;
    const previousPanes = store.getState().panes;
    const reference = bindFilePath("b.md", "/work/geometry");
    if (!reference) throw new Error("fixture file reference did not bind");
    await act(async () => {
      switch (mutation) {
        case "singleton params":
          store.getState().openPane("settings", { section: "models" }, { keepExistingFocus: true });
          break;
        case "document binding":
          recordDocumentPaneState(record(b), { reference, origin: undefined, reopen: 1 });
          break;
        case "add":
          store.getState().openPane("doc", { path: "new.md" });
          break;
        case "remove":
          store.getState().closePane(spare);
          break;
        case "record replacement":
          expect(store.getState().retypePane(record(main), "settings", { section: "models" })).toBe(true);
          break;
      }
    });
    expect(store.getState().panes).not.toBe(previousPanes);
    for (const id of [main, a, b]) expect(record(id).slot).toBe(slots.find(([key]) => key === id)?.[1]);
    expect([record(a), record(b)]).toEqual(records);
    expect(record(a)).toBe(records[0]);
    expect(record(b)).toBe(records[1]);
    expect(panel(api, a)).toBe(panels[0]);
    expect(panel(api, b)).toBe(panels[1]);
    if (mutation !== "add") expect(store.getState().focusedPaneId).toBe(focus);
    expect(api.activePanel?.id).toBe(store.getState().focusedPaneId);
    for (const id of [main, a, b]) expect(panel(api, id).group.id).toBe(before.get(id));
    expect(api.groups).toHaveLength(3);
    expect(document.querySelectorAll(".dv-groupview")).toHaveLength(3);
    expectMain(api, main);
  },
);

test.each([false, true])(
  "real document source promotion preserves unrelated splits, source isolated=%s",
  async (isolated) => {
    const { api, main, a, b } = await splitWorkspace();
    const source = await act(async () => store.getState().openPane("session", { ref: "source-session" }));
    await splitAgain(api, a, b);
    if (isolated) {
      await act(async () => {
        panel(api, source).api.moveTo({ group: panel(api, b).group, position: "right", skipSetActive: true });
        api.layout(1200, 700);
      });
    }
    const before = placements(api);
    const living = store.getState().panes.slice();
    const objects = new Map(api.panels.map((item) => [item.id, item]));
    const reference = bindFilePath("opened.md", "/work/geometry");
    if (!reference) throw new Error("fixture file reference did not bind");
    await act(async () => openDocBeside({ session: "source-session", sourcePaneId: source, reference }));
    const document = store
      .getState()
      .panes.find((item) => item.type === "doc" && (item.params as { path: string }).path === "opened.md");
    if (!document) throw new Error("source-bound document missing");
    expect(documentPaneState(document)?.origin).toBe(record(source));
    expect(documentPaneState(document)?.reference).toEqual(reference);
    for (const item of living) {
      expect(record(item.id)).toBe(item);
      expect(panel(api, item.id)).toBe(objects.get(item.id));
    }
    expect(record(source).slot).toBe("main");
    expect(record(main).slot).toBe("secondary");
    expect(store.getState().focusedPaneId).toBe(document.id);
    expect(api.activePanel?.id).toBe(document.id);
    expectMain(api, source);
    expect(panel(api, main).group).not.toBe(panel(api, source).group);
    expect(panel(api, a).group.id).toBe(before.get(a));
    expect(panel(api, b).group.id).toBe(before.get(b));
    expect(panel(api, a).group).not.toBe(panel(api, b).group);
    expect(api.groups).toHaveLength(3);
  },
);

test("promotion creates a secondary group when only the source was beside main", async () => {
  const main = store.getState().openPane("settings");
  const { api } = await mount();
  const source = await act(async () => store.getState().openPane("session", { ref: "source" }));
  const objects = [panel(api, main), panel(api, source)];
  await act(async () => store.getState().promotePane(source));
  expectMain(api, source);
  expect(panel(api, main)).toBe(objects[0]);
  expect(panel(api, source)).toBe(objects[1]);
  expect(panel(api, main).group).not.toBe(panel(api, source).group);
  expect(api.groups).toHaveLength(2);
  expect(api.activePanel?.id).toBe(store.getState().focusedPaneId);
});

test.each(["cold", "live", "phone promotion"] as const)(
  "%s desktop restore preserves unchanged-slot saved groups",
  async (mode) => {
    const { host, api, main, a, b } = await splitWorkspace();
    const source = await act(async () => store.getState().openPane("session", { ref: "restore-source" }));
    await splitAgain(api, a, b);
    await act(async () => store.getState().focusPane(b));
    const before = placements(api);
    const living = store.getState().panes.slice();
    const grid = api.toJSON().grid;
    localStorage.setItem(layoutKey, JSON.stringify(store.getState().layoutJSON()));
    await act(async () => host.unmount());
    expect(getDockviewApi()).toBe(null);
    if (mode === "cold") resetWorkspaceStoreForTests();
    if (mode === "phone promotion") {
      store.getState().promotePane(source);
      store.getState().focusPane(b);
    }
    const restored = await mount();
    const currentMain = mode === "phone promotion" ? source : main;
    expectMain(restored.api, currentMain);
    expect(store.getState().focusedPaneId).toBe(b);
    expect(restored.api.activePanel?.id).toBe(b);
    for (const item of living) {
      if (mode === "cold") expect(record(item.id)).not.toBe(item);
      else expect(record(item.id)).toBe(item);
    }
    for (const id of mode === "phone promotion" ? [a, b] : [main, a, b, source]) {
      expect(panel(restored.api, id).group.id).toBe(before.get(id));
    }
    expect(panel(restored.api, a).group).not.toBe(panel(restored.api, b).group);
    expect(restored.api.groups).toHaveLength(3);
    expect(document.querySelectorAll(".dv-groupview")).toHaveLength(3);
    if (mode !== "phone promotion") expect(restored.api.toJSON().grid).toEqual(grid);
    else {
      expect(record(main).slot).toBe("secondary");
      expect(panel(restored.api, main).group).not.toBe(panel(restored.api, source).group);
    }
  },
);
