import "../doc";
import "../session";
import "../transcript";
import "./index";
import { createDockview } from "dockview";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { StubResizeObserver } from "../../resizeObserverTestUtils";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { refParam } from "../../shell/routing";
import {
  type OpenPaneRecord,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { associatedCascade, cascadeOrigin, recordCascadeOrigin } from "./inspectionOrigin";
import { drillZoomIntent, parseZoomParams } from "./intent";

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
  installLocalStorage(new MemoryStorage());
  resetWorkspaceStoreForTests();
});
afterEach(() => {
  resetWorkspaceStoreForTests();
  vi.unstubAllGlobals();
});

function record(id: string): OpenPaneRecord {
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.id === id);
  if (!pane) throw new Error(`Missing committed pane ${id}`);
  return pane;
}

function inspectorFor(origin: OpenPaneRecord): OpenPaneRecord {
  const ref = refParam(origin.params);
  if (!ref || (origin.type !== "session" && origin.type !== "transcript")) {
    throw new Error("Expected an ordinary conversation origin");
  }
  const inspector = record(
    workspaceStore.getState().openPane(
      "sessionZoom",
      {
        ref: "child",
        source: { type: "transcript", params: { ref } },
        edges: [{ ownerRef: ref, childRef: "child", delegateId: "d1" }],
        inspection: { origin: { paneId: origin.id, type: origin.type, ref } },
      },
      { slot: "secondary" },
    ),
  );
  recordCascadeOrigin(inspector, origin);
  return inspector;
}

test("a separated inspector binds to the exact committed ordinary conversation", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const lifetime = conversationPaneLifetime(origin);
  const inspector = inspectorFor(origin);
  expect(cascadeOrigin(inspector)).toBe(origin);
  expect(associatedCascade(origin)).toBe(inspector);
  expect(conversationPaneLifetime(inspector)).not.toBe(lifetime);
  expect(conversationPaneLifetime(inspector).composer).toBeNull();
});

test("removal retires the locator before an identical replacement can claim it", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const originLifetime = conversationPaneLifetime(origin);
  const inspector = inspectorFor(origin);
  expect(cascadeOrigin(inspector)).toBe(origin);
  workspaceStore.getState().closePane(origin.id);
  const replacement: OpenPaneRecord = { ...origin, params: { ref: "root" } };
  workspaceStore.setState({
    panes: [replacement, ...workspaceStore.getState().panes],
    focusedPaneId: replacement.id,
  });
  expect(conversationPaneLifetime(replacement)).not.toBe(originLifetime);
  const survivor = record(inspector.id);
  expect(cascadeOrigin(survivor)).toBeNull();
  expect(associatedCascade(replacement)).toBeNull();
  expect(parseZoomParams(survivor.params)?.inspection).toEqual({ origin: null });
  expect(conversationPaneLifetime(survivor).composer).toBeNull();
});

test("a workspace reset retires both owners even when their IDs are reused", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = inspectorFor(origin);
  const originLifetime = conversationPaneLifetime(origin);
  const inspectorLifetime = conversationPaneLifetime(inspector);
  expect(associatedCascade(origin)).toBe(inspector);
  resetWorkspaceStoreForTests();
  const replacement = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  expect(replacement.id).toBe(origin.id);
  expect(originLifetime.alive).toBe(false);
  expect(inspectorLifetime.alive).toBe(false);
  expect(cascadeOrigin(inspector)).toBeNull();
  expect(associatedCascade(replacement)).toBeNull();
});

test("closing an inspector disposes only inspection and preserves the source composer", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const lifetime = conversationPaneLifetime(origin);
  const composer = lifetime.composer;
  composer?.editText("keep the source draft");
  const inspector = inspectorFor(origin);
  const inspectorLifetime = conversationPaneLifetime(inspector);
  expect(associatedCascade(origin)).toBe(inspector);
  workspaceStore.getState().closePane(inspector.id);
  expect(inspectorLifetime.alive).toBe(false);
  expect(cascadeOrigin(inspector)).toBeNull();
  expect(associatedCascade(origin)).toBeNull();
  expect(record(origin.id)).toBe(origin);
  expect(lifetime.alive).toBe(true);
  expect(lifetime.composer).toBe(composer);
  expect(composer?.getSnapshot().text).toBe("keep the source draft");
});

test("two independent same-ref origins retain different associated inspectors", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const independent: OpenPaneRecord = { ...origin, id: "independent-root", slot: "secondary" };
  workspaceStore.setState({ panes: [origin, independent], focusedPaneId: origin.id });
  const inspector = inspectorFor(origin);
  const otherInspector = inspectorFor(independent);
  expect(inspector.id).not.toBe(otherInspector.id);
  expect(cascadeOrigin(inspector)).toBe(origin);
  expect(cascadeOrigin(otherInspector)).toBe(independent);
  expect(associatedCascade(origin)).toBe(inspector);
  expect(associatedCascade(independent)).toBe(otherInspector);
});

test("drill retype retains the association through the inspector lifetime", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = inspectorFor(origin);
  const lifetime = conversationPaneLifetime(inspector);
  const params = parseZoomParams(inspector.params);
  if (!params) throw new Error("Expected readable cascade params");
  const drilled = drillZoomIntent(params, { ownerRef: "child", childRef: "grandchild", delegateId: "d2" });
  expect(workspaceStore.getState().retypePane(inspector, "sessionZoom", drilled)).toBe(true);
  const replacement = record(inspector.id);
  expect(replacement).not.toBe(inspector);
  expect(conversationPaneLifetime(replacement)).toBe(lifetime);
  expect(cascadeOrigin(replacement)).toBe(origin);
  expect(associatedCascade(origin)).toBe(replacement);
  expect(cascadeOrigin(inspector)).toBeNull();
});

test("ordinary source retype retains the exact lifetime without claiming its stale record", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = inspectorFor(origin);
  const lifetime = conversationPaneLifetime(origin);
  workspaceStore.getState().retypePane(origin, "transcript", origin.params);
  const replacement = record(origin.id);
  expect(conversationPaneLifetime(replacement)).toBe(lifetime);
  expect(cascadeOrigin(inspector)).toBe(replacement);
  expect(associatedCascade(replacement)).toBe(inspector);
  expect(associatedCascade(origin)).toBeNull();
});

test("an unbound live locator is never lazily resolved by pane ID", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = record(
    workspaceStore.getState().openPane(
      "sessionZoom",
      {
        ref: "child",
        source: { type: "transcript", params: { ref: "root" } },
        edges: [],
        inspection: { origin: { paneId: origin.id, type: "session", ref: "root" } },
      },
      { slot: "secondary" },
    ),
  );
  expect(cascadeOrigin(inspector)).toBeNull();
  expect(associatedCascade(origin)).toBeNull();
  workspaceStore.getState().focusPane(origin.id);
  expect(cascadeOrigin(inspector)).toBeNull();
});

test("recording a stale origin retires the locator rather than binding its same-ID replacement", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = inspectorFor(origin);
  const replacement: OpenPaneRecord = { ...origin, params: { ref: "root" } };
  workspaceStore.setState({
    panes: [replacement, ...workspaceStore.getState().panes.filter((pane) => pane !== origin)],
  });
  recordCascadeOrigin(record(inspector.id), origin);
  const survivor = record(inspector.id);
  expect(cascadeOrigin(survivor)).toBeNull();
  expect(associatedCascade(replacement)).toBeNull();
  expect(parseZoomParams(survivor.params)?.inspection).toEqual({ origin: null });
});

test("recording a stale inspector cannot change its committed replacement", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = inspectorFor(origin);
  const params = parseZoomParams(inspector.params);
  if (!params) throw new Error("Expected readable cascade params");
  workspaceStore.getState().retypePane(inspector, "sessionZoom", { ...params, ref: "sibling", edges: [] });
  const replacement = record(inspector.id);
  recordCascadeOrigin(inspector, null);
  expect(record(inspector.id)).toBe(replacement);
  expect(cascadeOrigin(replacement)).toBe(origin);
});

function layoutFor(panes: readonly OpenPaneRecord[]) {
  const container = document.createElement("div");
  document.body.append(container);
  const api = createDockview(container, {
    disableAutoResizing: true,
    createComponent: () => ({ element: document.createElement("div"), init() {} }),
  });
  for (const pane of panes) {
    api.addPanel({
      id: pane.id,
      component: "pane",
      params: { paneType: pane.type, paneParams: pane.params },
      ...(pane.slot === "secondary" ? { position: { direction: "right" as const } } : {}),
    });
  }
  registerDockviewApi(api);
  return {
    api,
    dispose() {
      registerDockviewApi(null);
      api.dispose();
      container.remove();
    },
  };
}

test("successful restore binds fresh validated owners before publishing the record list", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = inspectorFor(origin);
  const originLifetime = conversationPaneLifetime(origin);
  const inspectorLifetime = conversationPaneLifetime(inspector);
  const host = layoutFor([origin, inspector]);
  const observed: (OpenPaneRecord | null)[] = [];
  const unsubscribe = workspaceStore.subscribe((state) => {
    const restored = state.panes.find((pane) => pane.id === inspector.id);
    if (restored && restored !== inspector) observed.push(cascadeOrigin(restored));
  });
  try {
    expect(workspaceStore.getState().restoreLayout(host.api.toJSON())).toBe(true);
    const restoredOrigin = record(origin.id);
    const restoredInspector = record(inspector.id);
    expect(restoredOrigin).not.toBe(origin);
    expect(originLifetime.alive).toBe(false);
    expect(inspectorLifetime.alive).toBe(false);
    expect(observed).toEqual([restoredOrigin]);
    expect(cascadeOrigin(restoredInspector)).toBe(restoredOrigin);
    expect(associatedCascade(restoredOrigin)).toBe(restoredInspector);
    expect(conversationPaneLifetime(restoredInspector).composer).toBeNull();
  } finally {
    unsubscribe();
    host.dispose();
  }
});

test("restore without the original panel retires its locator without waiting for lazy content", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = inspectorFor(origin);
  const host = layoutFor([inspector]);
  try {
    expect(workspaceStore.getState().restoreLayout(host.api.toJSON())).toBe(true);
    const restored = record(inspector.id);
    expect(parseZoomParams(restored.params)?.inspection).toEqual({ origin: null });
    const replacement: OpenPaneRecord = { ...origin, params: { ref: "root" } };
    workspaceStore.setState({ panes: [replacement, restored] });
    expect(cascadeOrigin(restored)).toBeNull();
    expect(associatedCascade(replacement)).toBeNull();
  } finally {
    host.dispose();
  }
});

test("layout saves retired logical metadata before the host reconciles panel params", () => {
  const origin = record(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspector = inspectorFor(origin);
  const host = layoutFor([origin, inspector]);
  try {
    const before = host.api.toJSON();
    workspaceStore.getState().closePane(origin.id);
    const layout = workspaceStore.getState().layoutJSON() as ReturnType<typeof host.api.toJSON>;
    const saved = layout.panels[inspector.id]?.params?.paneParams;
    expect(parseZoomParams(saved)?.inspection).toEqual({ origin: null });
    expect(layout.grid).toEqual(before.grid);
    expect(parseZoomParams(host.api.getPanel(inspector.id)?.params?.paneParams)?.inspection?.origin?.paneId).toBe(
      origin.id,
    );
  } finally {
    host.dispose();
  }
});
