import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { parseZoomParams } from "../panes/zoom/intent";
import {
  attentionEntryOwnsReader,
  currentAttentionEntryToken,
  goBackInAttentionHistory,
  openAttentionSession,
  restoreCurrentAttentionEntry,
} from "./attentionHistory";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "./paneRegistry";
import { type OpenPaneRecord, presentedPanes, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

function fixtureDescriptor<P>(
  id: PaneDescriptor<P>["id"],
  overrides: Partial<PaneDescriptor<P>> = {},
): PaneDescriptor<P> {
  return {
    id,
    title: () => id,
    component: lazy(() => new Promise<{ default: React.ComponentType<PaneProps<P>> }>(() => {})),
    ...overrides,
  };
}

beforeAll(() => {
  registerPaneForTests(fixtureDescriptor("session"));
  registerPaneForTests(fixtureDescriptor("sessionZoom", { parseParams: parseZoomParams }));
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  history.replaceState({ unrelated: "kept" }, "", "/");
});

afterEach(() => {
  resetWorkspaceStoreForTests();
  history.replaceState({}, "", "/");
});

function openedRef(ref: string): OpenPaneRecord | undefined {
  return workspaceStore.getState().panes.find((pane) => pane.params && (pane.params as { ref?: string }).ref === ref);
}

function expectPop(): Promise<PopStateEvent> {
  return new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
}

test("an attention hop binds the real initiating reader and keeps unrelated history state", () => {
  history.replaceState({ unrelated: "kept" }, "", "/s/local%3Aa");
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const initialLength = history.length;
  openAttentionSession("local:b");
  expect(history.length).toBe(initialLength + 1);
  expect(history.state).toMatchObject({
    unrelated: "kept",
    __evenerAttention: { generation: expect.any(String), id: expect.any(String) },
  });
  expect(attentionEntryOwnsReader(a, "local:a")).toBe(false);
  expect(openedRef("local:b")).toBeDefined();
  expect(currentAttentionEntryToken()).not.toBeNull();
});

test("Back and Forward restore exact A, B and C runtime readers through browser events", async () => {
  const initialLength = history.length;
  history.replaceState({ unrelated: "kept" }, "", "/s/local%3Aa");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  openAttentionSession("local:b");
  const b = openedRef("local:b");
  if (!b) throw new Error("B reader missing");
  openAttentionSession("local:c");
  const c = openedRef("local:c");
  if (!c) throw new Error("C reader missing");
  expect(history.length).toBe(initialLength + 2);

  let popped = expectPop();
  expect(goBackInAttentionHistory()).toBe(true);
  await popped;
  expect(location.pathname).toBe("/s/local%3Ab");
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(b.id);

  popped = expectPop();
  expect(goBackInAttentionHistory()).toBe(true);
  await popped;
  expect(location.pathname).toBe("/s/local%3Aa");
  expect(currentAttentionEntryToken()).not.toBeNull();
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(aId);

  popped = expectPop();
  history.forward();
  await popped;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(b.id);
  popped = expectPop();
  history.forward();
  await popped;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(c.id);
});

test("a focus-only change rebinds the B entry to exact secondary reader X", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const xId = workspaceStore.getState().openPane("session", { ref: "local:x" });
  const x = openedRef("local:x");
  if (!x) throw new Error("X reader missing");
  workspaceStore.getState().focusPane(aId);
  openAttentionSession("local:b");
  const b = openedRef("local:b");
  if (!b) throw new Error("B reader missing");
  expect(b.slot).toBe("main");
  expect(x.slot).toBe("secondary");

  workspaceStore.getState().focusPane(xId);
  openAttentionSession("local:c");
  const c = openedRef("local:c");
  if (!c) throw new Error("C reader missing");
  const popped = expectPop();
  history.back();
  await popped;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().panes).toContain(x);
  expect(workspaceStore.getState().focusedPaneId).toBe(xId);
  expect(workspaceStore.getState().panes).toContain(b);
  expect(b.slot).toBe("main");
  const forward = expectPop();
  history.forward();
  await forward;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().panes).toContain(c);
  expect(workspaceStore.getState().focusedPaneId).toBe(c.id);
});

test("a cascade leaf retype is rebound to its exact live reader before the next hop", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  openAttentionSession("local:b");
  const sessionB = openedRef("local:b");
  if (!sessionB) throw new Error("B reader missing");
  const zoomParams = {
    ref: "local:b",
    source: { type: "session", params: { ref: "local:a" } },
    edges: [{ ownerRef: "local:a", childRef: "local:b", delegateId: "edge-b" }],
  };
  expect(workspaceStore.getState().retypePane(sessionB, "sessionZoom", zoomParams)).toBe(true);
  const cascadeB = openedRef("local:b");
  if (!cascadeB) throw new Error("cascade B reader missing");
  expect(cascadeB.type).toBe("sessionZoom");

  openAttentionSession("local:c");
  const popped = expectPop();
  history.back();
  await popped;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().panes).toContain(cascadeB);
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeB.id);
});

test("foreign-generation state is not exposed as a current attention binding", () => {
  history.replaceState({ __evenerAttention: { generation: "foreign-page", id: "entry-foreign" } }, "", "/s/local%3Aa");
  expect(currentAttentionEntryToken()).toBeNull();
  expect(restoreCurrentAttentionEntry()).toBe(false);
  expect(goBackInAttentionHistory()).toBe(false);
});

test("an attention reader can push a distinct same-URL destination exactly once", () => {
  history.replaceState({}, "", "/s/local%3Ab");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const bId = workspaceStore.getState().openPane("session", { ref: "local:b" });
  workspaceStore.getState().focusPane(aId);
  const pushState = vi.spyOn(history, "pushState");
  try {
    openAttentionSession("local:b");
    expect(pushState).toHaveBeenCalledTimes(1);
  } finally {
    pushState.mockRestore();
  }
  expect(location.pathname).toBe("/s/local%3Ab");
  expect(workspaceStore.getState().focusedPaneId).toBe(bId);
  expect(currentAttentionEntryToken()).not.toBeNull();
});

test("closing the bound predecessor invalidates Back without moving browser history", () => {
  history.replaceState({}, "", "/s/local%3Aa");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  openAttentionSession("local:b");
  const oldA = workspaceStore.getState().panes.find((pane) => pane.id === aId);
  if (!oldA) throw new Error("A reader missing");
  workspaceStore.getState().closePane(aId);
  const reusedA: OpenPaneRecord = { id: aId, type: "session", params: { ref: "local:a" }, slot: "parked" };
  workspaceStore.setState({ panes: [...workspaceStore.getState().panes, reusedA] });
  expect(reusedA).not.toBe(oldA);
  const beforeURL = location.pathname;
  expect(goBackInAttentionHistory()).toBe(false);
  expect(location.pathname).toBe(beforeURL);
  expect(restoreCurrentAttentionEntry()).toBe(true);
});

test("ordinary app navigation from Back prunes the Forward target but preserves presented panes", async () => {
  history.replaceState({ unrelated: "kept" }, "", "/s/local%3Aa");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  openAttentionSession("local:b");
  const b = openedRef("local:b");
  if (!b) throw new Error("B reader missing");
  const popped = expectPop();
  history.back();
  await popped;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(aId);
  const { openSessionRoute } = await import("./routing");
  const routeEvents = vi.fn();
  window.addEventListener("popstate", routeEvents, { once: true });
  openSessionRoute("local:x");
  expect(location.pathname).toBe("/s/local%3Ax");
  expect(routeEvents).toHaveBeenCalledTimes(1);
  expect(workspaceStore.getState().panes).not.toContain(b);
  expect(presentedPanes(workspaceStore.getState().panes).some((pane) => pane === b)).toBe(false);
});

test("a new attention hop from Back does not reuse the discarded Forward token", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  openAttentionSession("local:b");
  const bToken = currentAttentionEntryToken();
  const popped = expectPop();
  history.back();
  await popped;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  openAttentionSession("local:c");
  expect(currentAttentionEntryToken()).not.toBe(bToken);
  expect(location.pathname).toBe("/s/local%3Ac");
  expect(
    workspaceStore.getState().panes.some((pane) => pane.params && (pane.params as { ref?: string }).ref === "local:b"),
  ).toBe(false);
});
