import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { retainedTranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { parseZoomParams } from "../panes/zoom/intent";
import {
  attentionEntryOwnsReader,
  currentAttentionEntryToken,
  goBackInAttentionHistory,
  openAttentionSession,
  restoreCurrentAttentionEntry,
} from "./attentionHistory";
import { conversationPaneLifetime } from "./paneLifetime";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "./paneRegistry";
import { navigate, openSessionRoute } from "./routing";
import { openSessionByRef } from "./sessionPlacement";
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
  const a = openedRef("local:a");
  if (!a) throw new Error("A reader missing");
  const xId = workspaceStore.getState().openPane("session", { ref: "local:x" });
  const x = openedRef("local:x");
  if (!x) throw new Error("X reader missing");
  const xLifetime = conversationPaneLifetime(x);
  const xReadView = retainedTranscriptReadView(xLifetime, "local:x", "session");
  workspaceStore.getState().focusPane(aId);
  const pushState = vi.spyOn(history, "pushState");
  try {
    openAttentionSession("local:b");
  } catch (error) {
    pushState.mockRestore();
    throw error;
  }
  const b = openedRef("local:b");
  if (!b) throw new Error("B reader missing");
  const bLifetime = conversationPaneLifetime(b);
  const bReadView = retainedTranscriptReadView(bLifetime, "local:b", "session");
  expect(b.slot).toBe("main");
  expect(x.slot).toBe("secondary");

  workspaceStore.getState().focusPane(xId);
  openAttentionSession("local:c");
  expect(pushState).toHaveBeenCalledTimes(2);
  pushState.mockRestore();
  const c = openedRef("local:c");
  if (!c) throw new Error("C reader missing");
  const cLifetime = conversationPaneLifetime(c);
  const cReadView = retainedTranscriptReadView(cLifetime, "local:c", "session");
  const popped = expectPop();
  history.back();
  await popped;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().panes).toContain(x);
  expect(workspaceStore.getState().focusedPaneId).toBe(xId);
  expect(conversationPaneLifetime(x)).toBe(xLifetime);
  expect(conversationPaneLifetime(x).serial).toBe(xLifetime.serial);
  expect(retainedTranscriptReadView(xLifetime, "local:x", "session")).toBe(xReadView);
  expect(retainedTranscriptReadView(xLifetime, "local:x", "session").id).toBe(xReadView.id);
  expect(workspaceStore.getState().panes).toContain(b);
  expect(conversationPaneLifetime(b)).toBe(bLifetime);
  expect(retainedTranscriptReadView(bLifetime, "local:b", "session")).toBe(bReadView);
  expect(b.slot).toBe("main");
  const forward = expectPop();
  history.forward();
  await forward;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().panes).toContain(c);
  expect(workspaceStore.getState().focusedPaneId).toBe(c.id);
  expect(conversationPaneLifetime(c)).toBe(cLifetime);
  expect(retainedTranscriptReadView(cLifetime, "local:c", "session")).toBe(cReadView);
});

test("a cascade leaf retype is rebound to its exact live reader before the next hop", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  const pushState = vi.spyOn(history, "pushState");
  try {
    openAttentionSession("local:b");
  } catch (error) {
    pushState.mockRestore();
    throw error;
  }
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
  const cascadeLifetime = conversationPaneLifetime(cascadeB);
  const cascadeReadView = retainedTranscriptReadView(cascadeLifetime, "local:b", "cascade");

  openAttentionSession("local:c");
  expect(pushState).toHaveBeenCalledTimes(2);
  pushState.mockRestore();
  const c = openedRef("local:c");
  if (!c) throw new Error("C reader missing");
  const cLifetime = conversationPaneLifetime(c);
  const cReadView = retainedTranscriptReadView(cLifetime, "local:c", "session");
  const popped = expectPop();
  history.back();
  await popped;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().panes).toContain(cascadeB);
  expect(workspaceStore.getState().focusedPaneId).toBe(cascadeB.id);
  expect(conversationPaneLifetime(cascadeB)).toBe(cascadeLifetime);
  expect(conversationPaneLifetime(cascadeB).serial).toBe(cascadeLifetime.serial);
  expect(retainedTranscriptReadView(cascadeLifetime, "local:b", "cascade")).toBe(cascadeReadView);
  const forward = expectPop();
  history.forward();
  await forward;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(c.id);
  expect(conversationPaneLifetime(c)).toBe(cLifetime);
  expect(retainedTranscriptReadView(cLifetime, "local:c", "session")).toBe(cReadView);
});

test("foreign-generation state is not exposed as a current attention binding", () => {
  history.replaceState({ __evenerAttention: { generation: "foreign-page", id: "entry-foreign" } }, "", "/s/local%3Aa");
  expect(currentAttentionEntryToken()).toBeNull();
  expect(restoreCurrentAttentionEntry()).toBe(false);
  expect(goBackInAttentionHistory()).toBe(false);
});

test("distinct readers at one URL restore independently on Back and Forward", async () => {
  history.replaceState({}, "", "/s/local%3Ab");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const bId = workspaceStore.getState().openPane("session", { ref: "local:b" });
  const a = openedRef("local:a");
  const b = openedRef("local:b");
  if (!a || !b) throw new Error("Same-URL readers missing");
  const aLifetime = conversationPaneLifetime(a);
  const aReadView = retainedTranscriptReadView(aLifetime, "local:a", "session");
  const bLifetime = conversationPaneLifetime(b);
  const bReadView = retainedTranscriptReadView(bLifetime, "local:b", "session");
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
  const back = expectPop();
  history.back();
  await back;
  expect(location.pathname).toBe("/s/local%3Ab");
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(aId);
  expect(conversationPaneLifetime(a)).toBe(aLifetime);
  expect(retainedTranscriptReadView(aLifetime, "local:a", "session")).toBe(aReadView);
  const forward = expectPop();
  history.forward();
  await forward;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(bId);
  expect(conversationPaneLifetime(b)).toBe(bLifetime);
  expect(retainedTranscriptReadView(bLifetime, "local:b", "session")).toBe(bReadView);
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

test("a surviving branch reference keeps a shared parked reader when another reference is pruned", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const a = openedRef("local:a");
  if (!a) throw new Error("A reader missing");
  const lifetimeA = conversationPaneLifetime(a);
  const readViewA = retainedTranscriptReadView(lifetimeA, "local:a", "session");

  openAttentionSession("local:b");
  openAttentionSession("local:a");
  const backToB = expectPop();
  history.back();
  await backToB;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).not.toBe(aId);
  expect(a.slot).toBe("parked");

  openAttentionSession("local:c");
  expect(workspaceStore.getState().panes).toContain(a);
  expect(lifetimeA.alive).toBe(true);
  expect(readViewA.alive).toBe(true);
  expect(retainedTranscriptReadView(lifetimeA, "local:a", "session")).toBe(readViewA);
});

test("a rebound current reader cannot own or restore its stale exact read view", () => {
  history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  openAttentionSession("local:b");
  const oldB = openedRef("local:b");
  if (!oldB) throw new Error("B reader missing");
  const oldLifetime = conversationPaneLifetime(oldB);
  const oldReadView = retainedTranscriptReadView(oldLifetime, "local:b", "session");
  workspaceStore.getState().closePane(oldB.id);

  const reboundB: OpenPaneRecord = {
    id: oldB.id,
    type: "session",
    params: { ref: "local:b" },
    slot: "main",
  };
  workspaceStore.setState({ panes: [reboundB], focusedPaneId: reboundB.id });
  const reboundLifetime = conversationPaneLifetime(reboundB);
  const reboundReadView = retainedTranscriptReadView(reboundLifetime, "local:b", "session");
  expect(reboundLifetime).not.toBe(oldLifetime);
  expect(reboundReadView.id).not.toBe(oldReadView.id);

  expect(attentionEntryOwnsReader(reboundB.id, "local:b")).toBe(false);
  expect(restoreCurrentAttentionEntry()).toBe(false);
  expect(workspaceStore.getState().focusedPaneId).toBe(reboundB.id);
  expect(workspaceStore.getState().panes).toContain(reboundB);
});

// The address bar of an attention entry names its departure URL, so activating
// the reader that URL names is a same-URL navigation. It has to leave the
// attention binding behind; otherwise navigate() no-ops, no ordinary
// reconciliation runs, and the press does nothing.
test("an ordinary activation at an attention-owned URL leaves the binding", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const x = workspaceStore.getState().openPane("session", { ref: "local:x" }, { slot: "secondary" });
  workspaceStore.getState().focusPane(x);
  openAttentionSession("local:target");
  const popped = expectPop();
  history.back();
  await popped;
  // Back lands on the departure entry, which the hop also bound: its header
  // names A while its placement restores X.
  expect(currentAttentionEntryToken()).not.toBeNull();
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(location.pathname).toBe("/s/local%3Aa");
  expect(workspaceStore.getState().focusedPaneId).toBe(x);

  const routeEvents = vi.fn();
  window.addEventListener("popstate", routeEvents, { once: true });
  openSessionRoute("local:a");

  expect(currentAttentionEntryToken()).toBeNull();
  expect(routeEvents).toHaveBeenCalledTimes(1);
  expect(location.pathname).toBe("/s/local%3Aa");
  expect(a).toBeDefined();
});

// The rail, scope crumbs and session links all follow a ref rather than a URL,
// so they reach the same-URL case through their own seam.
test("following a session ref at an attention-owned URL leaves the binding", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const x = workspaceStore.getState().openPane("session", { ref: "local:x" }, { slot: "secondary" });
  workspaceStore.getState().focusPane(x);
  openAttentionSession("local:target");
  const popped = expectPop();
  history.back();
  await popped;
  expect(currentAttentionEntryToken()).not.toBeNull();
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(location.pathname).toBe("/s/local%3Aa");

  workspaceStore.getState().focusPane(a);
  const routeEvents = vi.fn();
  window.addEventListener("popstate", routeEvents, { once: true });
  openSessionByRef("local:a");

  expect(currentAttentionEntryToken()).toBeNull();
  expect(routeEvents).toHaveBeenCalledTimes(1);
  expect(location.pathname).toBe("/s/local%3Aa");
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

test("an ordinary route between attention hops keeps still-reachable parked readers", async () => {
  const initialLength = history.length;
  history.replaceState({}, "", "/s/local%3Aa");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const a = openedRef("local:a");
  if (!a) throw new Error("A reader missing");
  const lifetimeA = conversationPaneLifetime(a);
  const readViewA = retainedTranscriptReadView(lifetimeA, "local:a", "session");

  openAttentionSession("local:b");
  const b = openedRef("local:b");
  if (!b) throw new Error("B reader missing");
  const lifetimeB = conversationPaneLifetime(b);
  const readViewB = retainedTranscriptReadView(lifetimeB, "local:b", "session");

  openSessionRoute("local:x");
  const xId = workspaceStore.getState().openPane("session", { ref: "local:x" });
  const x = openedRef("local:x");
  if (!x) throw new Error("X reader missing");
  expect(x.slot).toBe("secondary");
  expect(workspaceStore.getState().focusedPaneId).toBe(xId);
  openAttentionSession("local:c");
  expect(history.length).toBe(initialLength + 3);

  expect(workspaceStore.getState().panes).toContain(a);
  expect(lifetimeA.alive).toBe(true);
  expect(readViewA.alive).toBe(true);

  const pop = expectPop();
  history.back();
  await pop;
  expect(location.pathname).toBe("/s/local%3Ax");
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(x.id);

  const backToB = expectPop();
  history.back();
  await backToB;
  const backToA = expectPop();
  history.back();
  await backToA;
  expect(location.pathname).toBe("/s/local%3Aa");
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(aId);
  expect(conversationPaneLifetime(a)).toBe(lifetimeA);
  expect(retainedTranscriptReadView(lifetimeA, "local:a", "session")).toBe(readViewA);
  expect(readViewB.alive).toBe(true);
});

test("ordinary replacement between attention hops keeps the Back-reachable reader", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  const a = openedRef("local:a");
  if (!a) throw new Error("A reader missing");
  const lifetimeA = conversationPaneLifetime(a);
  const readViewA = retainedTranscriptReadView(lifetimeA, "local:a", "session");

  openAttentionSession("local:b");
  navigate("/s/local%3Ax", { replace: true });
  workspaceStore.getState().openPane("session", { ref: "local:x" });
  openAttentionSession("local:c");

  expect(workspaceStore.getState().panes).toContain(a);
  expect(lifetimeA.alive).toBe(true);
  expect(readViewA.alive).toBe(true);
  const backToX = expectPop();
  history.back();
  await backToX;
  const backToA = expectPop();
  history.back();
  await backToA;
  expect(location.pathname).toBe("/s/local%3Aa");
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().panes).toContain(a);
  expect(conversationPaneLifetime(a)).toBe(lifetimeA);
  expect(retainedTranscriptReadView(lifetimeA, "local:a", "session")).toBe(readViewA);
});

test("replace preserves Back and Forward readers, then a push prunes only Forward readers", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  const aId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const a = openedRef("local:a");
  if (!a) throw new Error("A reader missing");
  const lifetimeA = conversationPaneLifetime(a);
  const readViewA = retainedTranscriptReadView(lifetimeA, "local:a", "session");

  openAttentionSession("local:b");
  openAttentionSession("local:c");
  const c = openedRef("local:c");
  if (!c) throw new Error("C reader missing");
  const lifetimeC = conversationPaneLifetime(c);
  const readViewC = retainedTranscriptReadView(lifetimeC, "local:c", "session");
  const cToken = history.state.__evenerAttention;
  const backToB = expectPop();
  history.back();
  await backToB;
  expect(restoreCurrentAttentionEntry()).toBe(true);

  navigate("/s/local%3Ax", { replace: true });
  workspaceStore.getState().openPane("session", { ref: "local:x" });
  expect(history.state).not.toHaveProperty("__evenerAttention");
  const forwardToC = expectPop();
  history.forward();
  await forwardToC;
  expect(history.state.__evenerAttention).toEqual(cToken);
  expect(workspaceStore.getState().panes).toContain(c);
  expect(conversationPaneLifetime(c)).toBe(lifetimeC);
  expect(retainedTranscriptReadView(lifetimeC, "local:c", "session")).toBe(readViewC);

  const backToX = expectPop();
  history.back();
  await backToX;
  openSessionRoute("local:d");
  workspaceStore.getState().openPane("session", { ref: "local:d" });
  expect(workspaceStore.getState().panes).not.toContain(c);
  expect(lifetimeC.alive).toBe(false);
  expect(readViewC.alive).toBe(false);
  expect(workspaceStore.getState().panes).toContain(a);
  expect(lifetimeA.alive).toBe(true);
  expect(readViewA.alive).toBe(true);

  const backToXAgain = expectPop();
  history.back();
  await backToXAgain;
  const backToA = expectPop();
  history.back();
  await backToA;
  expect(restoreCurrentAttentionEntry()).toBe(true);
  expect(workspaceStore.getState().focusedPaneId).toBe(aId);
  expect(conversationPaneLifetime(a)).toBe(lifetimeA);
  expect(retainedTranscriptReadView(lifetimeA, "local:a", "session")).toBe(readViewA);
});

test("a repeated ordinary URL does not prune parked attention entries behind the second occurrence", async () => {
  history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  openAttentionSession("local:b");
  openSessionRoute("local:x");
  workspaceStore.getState().openPane("session", { ref: "local:x" });
  openSessionRoute("local:y");
  workspaceStore.getState().openPane("session", { ref: "local:y" });
  openAttentionSession("local:c");
  const c = openedRef("local:c");
  if (!c) throw new Error("C reader missing");
  const lifetimeC = conversationPaneLifetime(c);
  const readViewC = retainedTranscriptReadView(lifetimeC, "local:c", "session");
  openAttentionSession("local:d");

  openSessionRoute("local:x");
  workspaceStore.getState().openPane("session", { ref: "local:x" });
  openSessionRoute("local:z");
  workspaceStore.getState().openPane("session", { ref: "local:z" });
  const backToSecondX = expectPop();
  history.back();
  await backToSecondX;
  expect(location.pathname).toBe("/s/local%3Ax");
  openSessionRoute("local:w");
  workspaceStore.getState().openPane("session", { ref: "local:w" });
  expect(workspaceStore.getState().panes).toContain(c);
  expect(lifetimeC.alive).toBe(true);
  expect(readViewC.alive).toBe(true);
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
