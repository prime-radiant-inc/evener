import { bindFilePath } from "@evener/appwire-client/docContent";
import { act, cleanup, render } from "@testing-library/react";
import type { DockviewApi } from "dockview-core";
import { lazy } from "react";
import { afterEach, beforeAll, expect, test } from "vitest";
import { openDocBeside } from "../panes/doc/openDoc";
import { enterAgentCascade, returnFromAgentCascade } from "../panes/zoom/actions";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { activityDelegate } from "../stores/sessionActivityTestUtils";
import { captureAttentionPlacement, presentAttentionTarget, restoreAttentionPlacement } from "./attentionPlacement";
import { DockHost } from "./DockHost";
import { StackHost } from "./mobile/StackHost";
import { conversationPaneLifetime } from "./paneLifetime";
import { type PaneProps, registerPaneForTests } from "./paneRegistry";
import {
  documentPaneState,
  getDockviewApi,
  presentedPanes,
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
  for (const id of ["settings", "session", "sessionZoom", "doc"] as const) {
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

test.each(["main", "secondary", "sole secondary"] as const)(
  "parking preserves independent groups and tab order: %s",
  async (seat) => {
    const a = store.getState().openPane("session", { ref: "local:a" });
    const { api } = await mount();
    const x = await act(async () => store.getState().openPane("doc", { path: "x.md" }));
    const y = await act(async () => store.getState().openPane("doc", { path: "y.md" }));
    const secondary = await act(async () => store.getState().openPane("session", { ref: "local:secondary" }));
    await act(async () => {
      panel(api, y).api.moveTo({ group: panel(api, x).group, position: "right", skipSetActive: true });
      if (seat === "sole secondary")
        panel(api, secondary).api.moveTo({ group: panel(api, y).group, position: "right", skipSetActive: true });
      api.layout(1200, 700);
      store.getState().focusPane(seat === "main" ? a : secondary);
    });
    const origin = captureAttentionPlacement();
    if (!origin) throw new Error("missing geometry origin");
    const lifetime = conversationPaneLifetime(origin.pane);
    const groups = api.groups.map((group) => ({
      id: group.id,
      tabs: group.panels.map((item) => item.id),
      width: group.width,
      height: group.height,
    }));
    const sourceGroup = panel(api, origin.pane.id).group.id;
    const destination = await act(async () => presentAttentionTarget("local:b"));
    if (!destination) throw new Error("missing geometry destination");
    expect(origin.pane.slot).toBe("parked");
    expect(lifetime.alive).toBe(true);
    expect(api.groups).toHaveLength(groups.length);
    expect(api.getPanel(origin.pane.id)).toBeUndefined();
    expect(panel(api, destination.pane.id).group.id).toBe(sourceGroup);
    for (const before of groups) {
      const group = api.groups.find((item) => item.id === before.id);
      expect(group?.panels.map((item) => item.id)).toEqual(
        before.tabs.map((id) => (id === origin.pane.id ? "pane_session_5" : id)),
      );
      expect([group?.width, group?.height]).toEqual([before.width, before.height]);
    }
    expect(presentedPanes(store.getState().panes).map((pane) => pane.id)).toEqual(
      seat === "main"
        ? ["pane_doc_2", "pane_doc_3", "pane_session_4", "pane_session_5"]
        : ["pane_session_1", "pane_doc_2", "pane_doc_3", "pane_session_5"],
    );
    expectMain(api, seat === "main" ? destination.pane.id : a);
    const saved = store.getState().layoutJSON() as ReturnType<DockviewApi["toJSON"]>;
    expect(Object.keys(saved.panels).sort()).toEqual(
      seat === "main"
        ? ["pane_doc_2", "pane_doc_3", "pane_session_4", "pane_session_5"]
        : ["pane_doc_2", "pane_doc_3", "pane_session_1", "pane_session_5"],
    );
    await act(async () => {
      expect(restoreAttentionPlacement(origin)).toBe(true);
    });
    for (const before of groups)
      expect(api.groups.find((group) => group.id === before.id)?.panels.map((item) => item.id)).toEqual(before.tabs);
    expect(conversationPaneLifetime(origin.pane)).toBe(lifetime);
    // A real native removal, unlike substitution, is explicit Close.
    await act(async () => api.removePanel(panel(api, secondary)));
    expect(store.getState().panes.some((pane) => pane.id === secondary)).toBe(false);
  },
);

test("attention records survive desktop phone desktop reconstruction", async () => {
  store.getState().openPane("session", { ref: "local:a" });
  const { host, api } = await mount();
  const x = await act(async () => store.getState().openPane("doc", { path: "x.md" }));
  await act(async () => store.getState().focusPane("pane_session_1"));
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing swap origin");
  const lifetime = conversationPaneLifetime(origin.pane);
  const destination = await act(async () => presentAttentionTarget("local:b"));
  if (!destination) throw new Error("missing swap target");
  const xGroup = panel(api, x).group.id;
  localStorage.setItem(layoutKey, JSON.stringify(store.getState().layoutJSON()));
  await act(async () => host.unmount());
  const phone = await act(async () => render(<StackHost routeDeferred />));
  expect(store.getState().panes).toContain(origin.pane);
  await act(async () => {
    expect(restoreAttentionPlacement(origin)).toBe(true);
  });
  await act(async () => phone.unmount());
  const restored = await mount();
  expect(conversationPaneLifetime(origin.pane)).toBe(lifetime);
  expect(destination.pane.slot).toBe("parked");
  expect(restored.api.getPanel(destination.pane.id)).toBeUndefined();
  expect(restored.api.groups).toHaveLength(2);
  expect(panel(restored.api, x).group.id).toBe(xGroup);
  expectMain(restored.api, origin.pane.id);
});

test("layout serialization excludes parked panels before host reconciliation", async () => {
  store.getState().openPane("session", { ref: "local:a" });
  const { api } = await mount();
  await act(async () => store.getState().openPane("doc", { path: "x.md" }));
  await act(async () => store.getState().focusPane("pane_session_1"));
  await act(async () => {
    presentAttentionTarget("local:b");
    // This synchronous read precedes DockHost's effect, so the old panel is
    // still in the actual grid. It must not leak into a cold saved layout.
    expect(api.getPanel("pane_session_1")).toBeDefined();
    const saved = store.getState().layoutJSON() as ReturnType<DockviewApi["toJSON"]>;
    expect(Object.keys(saved.panels)).toEqual(["pane_doc_2"]);
    const views: string[] = [];
    function visit(node: typeof saved.grid.root) {
      if (Array.isArray(node.data)) node.data.forEach(visit);
      else views.push(...node.data.views);
    }
    visit(saved.grid.root);
    expect(views).toEqual(["pane_doc_2"]);
  });
});

test("attention restoration follows a surviving seat without undoing user layout changes", async () => {
  const { api, main, a, b } = await splitWorkspace();
  const source = await act(async () => store.getState().openPane("session", { ref: "local:a" }));
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing source");
  const destination = await act(async () => presentAttentionTarget("local:b"));
  if (!destination) throw new Error("missing target");
  await act(async () => panel(api, destination.pane.id).api.moveTo({ group: panel(api, b).group, position: "center" }));
  const userGroup = panel(api, b).group.id;
  const independentGroup = panel(api, a).group.id;
  await act(async () => {
    expect(restoreAttentionPlacement(origin)).toBe(true);
  });
  expect(panel(api, source).group.id).toBe(userGroup);
  expect(panel(api, a).group.id).toBe(independentGroup);
  await act(async () => panel(api, source).api.moveTo({ group: panel(api, a).group, position: "center" }));
  const grid = api.toJSON().grid;
  await act(async () => {
    expect(restoreAttentionPlacement(origin)).toBe(true);
  });
  expect(api.toJSON().grid).toEqual(grid);
  expectMain(api, main);
});

test("inspection Return lets Dockview choose its normal fallback for a parked origin", async () => {
  const sourceId = store.getState().openPane("session", { ref: "local:a" });
  const source = record(sourceId);
  const { api } = await mount();
  const independentId = await act(async () => store.getState().openPane("doc", { path: "independent.md" }));
  const inspectorId = await act(async () =>
    enterAgentCascade(
      activityDelegate({ ownerRef: "local:a", childRef: "local:child", delegateId: "delegate-1" }),
      sourceId,
    ),
  );
  await act(async () => store.getState().focusPane(sourceId));
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing parked inspector origin");
  const destination = await act(async () => presentAttentionTarget("local:b"));
  if (!destination) throw new Error("missing attention destination");
  expect(source.slot).toBe("parked");
  expect(api.getPanel(inspectorId)).toBeDefined();

  await act(async () => returnFromAgentCascade(inspectorId));

  expect(api.getPanel(inspectorId)).toBeUndefined();
  expect(api.activePanel?.id).not.toBe(inspectorId);
  expect(api.activePanel?.id).toBe(destination.pane.id);
  expect(api.getPanel(independentId)).toBeDefined();
  expect(store.getState().panes).toContain(source);
  expect(source.slot).toBe("parked");
  expect(conversationPaneLifetime(source).alive).toBe(true);
  await act(async () => expect(restoreAttentionPlacement(origin)).toBe(true));
});

test("restoring a parked source does not resurrect a deliberately removed group", async () => {
  const { api, main, a, b } = await splitWorkspace();
  const source = await act(async () => store.getState().openPane("session", { ref: "local:a" }));
  await act(async () => panel(api, source).api.moveTo({ group: panel(api, b).group, position: "right" }));
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing source");
  const destination = await act(async () => presentAttentionTarget("local:b"));
  if (!destination) throw new Error("missing target");
  const retiredGroup = panel(api, destination.pane.id).group.id;
  await act(async () => api.removePanel(panel(api, destination.pane.id)));
  expect(api.groups).toHaveLength(3);
  const independent = placements(api);
  await act(async () => {
    expect(restoreAttentionPlacement(origin)).toBe(true);
  });
  expect(api.groups.some((group) => group.id === retiredGroup)).toBe(false);
  expect(api.groups).toHaveLength(3);
  for (const id of [main, a, b]) expect(panel(api, id).group.id).toBe(independent.get(id));
});

test("batched attention hops keep the original sole-panel group", async () => {
  store.getState().openPane("session", { ref: "local:a" });
  const { api } = await mount();
  const group = panel(api, "pane_session_1").group.id;
  await act(async () => {
    presentAttentionTarget("local:b");
    presentAttentionTarget("local:c");
  });
  expect(api.panels.map((pane) => pane.id)).toEqual(["pane_session_3"]);
  expect(panel(api, "pane_session_3").group.id).toBe(group);
  expect(api.groups).toHaveLength(1);
});

test("attention before the first desktop host is already a living workspace", async () => {
  store.getState().openPane("settings");
  const seed = await mount();
  const saved = store.getState().layoutJSON();
  await act(async () => seed.host.unmount());
  resetWorkspaceStoreForTests();
  localStorage.setItem(layoutKey, JSON.stringify(saved));
  store.getState().openPane("session", { ref: "local:a" });
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing source");
  const lifetime = conversationPaneLifetime(origin.pane);
  presentAttentionTarget("local:b");
  const { api } = await mount();
  expect(store.getState().panes).toContain(origin.pane);
  expect(lifetime.alive).toBe(true);
  expect(api.panels.map((pane) => pane.id)).toEqual(["pane_session_2"]);
});

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
