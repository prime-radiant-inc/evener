import type { SessionActivityContext, ThreadReadResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "zustand";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import {
  consumePaneFocus,
  type OpenPaneRecord,
  requestPaneFocus,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import {
  activityContext,
  activityDelegate,
  activitySummary,
  activityThread,
} from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { resetTranscriptViewRegistryForTests } from "../session/transcript/flow/transcriptViewRegistry";
import { holdReaderFrames, readerWireTurns } from "../session/transcript/transcriptReaderTestUtils";
import { installTranscriptGeometry } from "../session/transcript/transcriptReadingGeometryTestUtils";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { resetTranscriptPagingForTests } from "../session/transcript/useTranscript";
import { enterAgentCascade, popAgentCascade } from "./actions";
import { recordCascadeOrigin } from "./inspectionOrigin";
import type { SessionZoomParams } from "./intent";
import Zoom from "./Zoom";
import "./index";

let height: PropertyDescriptor | undefined;
beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetWorkspaceStoreForTests();
  resetTranscriptViewRegistryForTests();
  height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetWorkspaceStoreForTests();
  connectionStore.setState({ state: "idle", client: null });
  if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
  else Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
});

function currentPane(): OpenPaneRecord {
  const record = workspaceStore.getState().panes.find((pane) => pane.id === "cascade");
  if (!record) throw new Error("Missing cascade pane");
  return record;
}
function currentParams(): SessionZoomParams {
  return currentPane().params as SessionZoomParams;
}
function CommittedZoom() {
  const pane = useStore(workspaceStore, (state) => state.panes.find((item) => item.id === "cascade"));
  if (pane?.type !== "sessionZoom") return null;
  return <Zoom paneId={pane.id} params={pane.params as SessionZoomParams} focused />;
}

function fixture(ancestryKnown = true) {
  const fake = new FakeClient("ready");
  const statuses = new Map<string, string>();
  const identities = new Map([
    ["root", "old-root"],
    ["child", "child-id"],
    ["grandchild", "grandchild-id"],
    ["sibling", "sibling-id"],
  ]);
  let epoch = "epoch-1";
  const lineage = (ref: string) => (ref === "grandchild" ? ["root", "child"] : ref === "root" ? [] : ["root"]);
  const context = (ref: string): SessionActivityContext => ({
    ...activityContext(ref),
    sessionId: identities.get(ref) ?? ref,
    rootRef: "root",
    epoch,
    ancestryKnown,
    ...(ref === "root"
      ? {}
      : {
          parentRef: ref === "grandchild" ? "child" : "root",
          delegateId: ref === "grandchild" ? "d2" : ref === "sibling" ? "d3" : "d1",
        }),
    ancestors: lineage(ref).map((ancestor) => ({
      ref: ancestor,
      sessionId: identities.get(ancestor) ?? ancestor,
      title: ancestor,
      ...(ancestor === "child" ? { delegateId: "d1" } : {}),
    })),
  });
  const response = (ref: string): ThreadReadResponse => {
    const read = activityThread(ref);
    const identity = identities.get(ref) ?? ref;
    return {
      ...read,
      thread: {
        ...read.thread,
        id: `wire-${identity}`,
        sessionId: identity,
        name: ref,
        status: { type: statuses.get(ref) ?? "idle" },
        turns: [
          {
            id: `${identity}-turn`,
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: `${identity}-message`,
                turnId: `${identity}-turn`,
                type: "userMessage",
                text: `${ref} content ${identity}`,
                status: "completed",
              },
            ],
          },
        ],
      },
    };
  };
  fake.on("thread/read", ({ ref, requestGeneration, includeTurns }) => {
    if (!ref) throw new Error("thread/read requires ref");
    const read = response(ref);
    return { ...read, requestGeneration, thread: includeTurns === false ? { ...read.thread, turns: [] } : read.thread };
  });
  fake.on("thread/unsubscribe", () => ({}));
  fake.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    context: context(ref),
  }));
  fake.on("evener/thread/delegates/list", ({ ref }) => ({
    context: context(ref),
    scope: "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(fake);
  const params: SessionZoomParams = {
    ref: "child",
    source: { type: "session", params: { ref: "root" } },
    edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
  };
  const pane: OpenPaneRecord = { id: "cascade", type: "sessionZoom", slot: "main", params };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  return {
    fake,
    statuses,
    identities,
    response,
    context,
    setEpoch(value: string) {
      epoch = value;
    },
  };
}
function mount(fake: FakeClient) {
  return render(
    <ClientProvider client={fake}>
      <CommittedZoom />
    </ClientProvider>,
  );
}
function columnRefs() {
  return screen.queryAllByTestId("cascade-column").map((column) => column.getAttribute("data-scope-ref"));
}

test("genuine cascade column movement supersedes reflow without Return or neighboring movement", async () => {
  const { fake, response } = fixture();
  fake.on("thread/read", ({ ref, requestGeneration, includeTurns }) => {
    if (!ref) throw new Error("Missing real column ref");
    const read = response(ref);
    return {
      ...read,
      requestGeneration,
      thread: { ...read.thread, turns: includeTurns === false ? [] : readerWireTurns(ref) },
    };
  });
  const rootGeometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
  const childGeometry = { width: 400, viewportHeight: 400, rowHeights: [1600, 1000] };
  const external = installTranscriptGeometry((element) =>
    element.closest('[data-scope-ref="root"]') ? rootGeometry : childGeometry,
  );
  const frames = holdReaderFrames();
  const mounted = mount(fake);
  try {
    await screen.findByText("root current reading content");
    await screen.findByText("child current reading content");
    const portFor = (ref: string) => {
      const port = mounted.container.querySelector<HTMLElement>(
        `[data-scope-ref="${ref}"] [data-testid="transcript-virtual-list"] > div`,
      );
      if (!port) throw new Error("Real cascade column has no scroll port");
      return port;
    };
    const root = portFor("root");
    const child = portFor("child");
    await act(async () => external.notify());
    await act(async () => frames.release());
    await act(async () => frames.release());
    await act(async () => {
      fireEvent.wheel(root, { deltaY: -100 });
      root.scrollTop = 1000;
      fireEvent.scroll(root);
      fireEvent.wheel(child, { deltaY: -100 });
      child.scrollTop = 600;
      fireEvent.scroll(child);
    });
    await act(async () => {
      root.scrollTop = 900;
      fireEvent.scroll(root);
      child.scrollTop = 500;
      fireEvent.scroll(child);
    });
    const inspect = screen.getByRole("button", { name: "Return to previous view" });
    inspect.focus();
    rootGeometry.width = 352;
    rootGeometry.rowHeights[0] = 700;
    await act(async () => external.notify((target) => target === root));
    await act(async () => {
      fireEvent.wheel(root, { deltaY: -800 });
      root.scrollTop = 100;
      fireEvent.scroll(root);
    });
    await act(async () => external.notify());
    expect(child.scrollTop).toBe(500);
    expect(root.scrollTop).toBe(100);
    expect(document.activeElement).toBe(inspect);
    expect(columnRefs()).toEqual(["root", "child"]);
  } finally {
    mounted.unmount();
    external.restore();
    frames.restore();
  }
});

test("root and child render through real read-only readers inside one scaffold", async () => {
  const { fake } = fixture();
  const mounted = mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  expect(columnRefs()).toEqual(["root", "child"]);
  expect(mounted.container.querySelectorAll('[data-pane-scaffold="cascade"]')).toHaveLength(1);
  expect(screen.getAllByTestId("transcript-virtual-list")).toHaveLength(2);
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.getByRole("button", { name: "Return to previous view" })).toBeTruthy();
  expect(fake.calls.filter((call) => /send|resume|steer|interrupt/.test(call.method))).toHaveLength(0);
});

test("separated read-only Zoom Return closes inspection and focuses its surviving source", async () => {
  const { fake } = fixture();
  const source: OpenPaneRecord = { id: "source", type: "session", params: { ref: "root" }, slot: "main" };
  const inspector: OpenPaneRecord = {
    ...currentPane(),
    slot: "secondary",
    params: {
      ref: "child",
      source: { type: "transcript", params: { ref: "root" } },
      edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
      inspection: { origin: { paneId: source.id, type: "session", ref: "root" } },
    } satisfies SessionZoomParams,
  };
  workspaceStore.setState({ panes: [source, inspector], focusedPaneId: inspector.id });
  recordCascadeOrigin(inspector, source);
  const sourceLifetime = conversationPaneLifetime(source);
  const inspectorLifetime = conversationPaneLifetime(inspector);
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(inspectorLifetime.composer).toBeNull();
  requestPaneFocus(inspector.id);
  act(() => fireEvent.click(screen.getByRole("button", { name: "Return to previous view" })));
  expect(workspaceStore.getState().panes).toEqual([source]);
  expect(workspaceStore.getState().focusedPaneId).toBe(source.id);
  expect(consumePaneFocus(source.id)).toBe(true);
  expect(consumePaneFocus(inspector.id)).toBe(false);
  expect(sourceLifetime.alive).toBe(true);
  expect(inspectorLifetime.alive).toBe(false);
  expect(fake.calls.filter((call) => /send|resume|steer|interrupt/.test(call.method))).toHaveLength(0);
});

test.each(["deleted", "missing"] as const)(
  "a $0 child has a scoped terminal explanation while parent, Open conversation and Return stay usable",
  async (failure) => {
    const { fake, response, context } = fixture();
    const error =
      failure === "deleted"
        ? new WireError("target has been deleted: child", -32001, {
            evenerErrorInfo: "actionUnavailable",
            mutationOutcome: "targetDeleted",
            retryDisposition: "none",
          })
        : new WireError("thread not found: child", -32001, { evenerErrorInfo: "sessionUnavailable" });
    fake.on("thread/read", ({ ref, requestGeneration, includeTurns }) => {
      if (!ref) throw new Error("thread/read requires ref");
      if (ref === "child") throw error;
      const read = response(ref);
      return {
        ...read,
        requestGeneration,
        thread: includeTurns === false ? { ...read.thread, turns: [] } : read.thread,
      };
    });
    fake.on("evener/thread/activity/read", ({ ref, scope }) => {
      if (ref === "child" && failure === "missing") throw error;
      return { ...activitySummary(ref, scope), context: context(ref) };
    });
    mount(fake);
    await screen.findByText("root content old-root");
    const child = screen
      .getAllByTestId("cascade-column")
      .find((column) => column.getAttribute("data-scope-ref") === "child");
    if (!child) throw new Error("Missing child column");
    await within(child).findByText(failure === "deleted" ? "This session was deleted" : "Transcript unavailable");
    expect(within(child).queryByText("Loading transcript…")).toBeNull();
    expect(screen.getByText("root content old-root")).toBeTruthy();
    if (failure === "deleted") expect(threadsStore.getState().deletedRefs.has("child")).toBe(true);
    act(() => fireEvent.click(within(child).getByRole("button", { name: "Open conversation" })));
    expect(
      workspaceStore
        .getState()
        .panes.some((pane) => pane.type === "session" && (pane.params as { ref: string }).ref === "child"),
    ).toBe(true);
    act(() =>
      fireEvent.click(
        within(screen.getByRole("navigation", { name: "Agent path" })).getByRole("button", { name: "root" }),
      ),
    );
    expect(currentParams().ref).toBe("root");
    expect(columnRefs()).toEqual(["root"]);
    expect(screen.getByText("root content old-root")).toBeTruthy();
    act(() => fireEvent.click(screen.getByRole("button", { name: "Return to previous view" })));
    expect(currentPane()).toMatchObject({ id: "cascade", type: "session", params: { ref: "root" } });
    expect(fake.calls.filter((call) => /send|resume|steer|interrupt/.test(call.method))).toHaveLength(0);
  },
);

test("a deleted child hides its retained transcript without hiding the parent", async () => {
  const { fake, response } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  fake.on("thread/read", ({ ref, requestGeneration, includeTurns }) => {
    if (!ref) throw new Error("thread/read requires ref");
    if (ref === "child") {
      throw new WireError("target has been deleted: child", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    }
    const read = response(ref);
    return { ...read, requestGeneration, thread: includeTurns === false ? { ...read.thread, turns: [] } : read.thread };
  });
  await act(async () => {
    await expect(threadsStore.getState().refreshThread("child")).rejects.toThrow("target has been deleted");
  });
  expect(threadsStore.getState().threads.get("child")).toBeDefined();
  expect(screen.getByText("This session was deleted")).toBeTruthy();
  expect(screen.queryByText("child content child-id")).toBeNull();
  expect(screen.getByText("root content old-root")).toBeTruthy();
});

test("unsupported child activity does not hide its healthy transcript", async () => {
  const { fake, context } = fixture();
  fake.on("evener/thread/activity/read", ({ ref, scope }) => {
    if (ref === "child") throw new WireError("activity unavailable", -32601, { evenerErrorInfo: "methodNotFound" });
    return { ...activitySummary(ref, scope), context: context(ref) };
  });
  mount(fake);
  await screen.findByText("child content child-id");
  expect(screen.queryByText("Transcript unavailable")).toBeNull();
  expect(currentParams().ref).toBe("child");
  expect(columnRefs()).toEqual(["root", "child"]);
});

test("a transient child read failure recovers through the existing owner without changing the cascade", async () => {
  const { fake, response, context } = fixture();
  const failed = deferred<void>();
  let healed = false;
  fake.on("thread/read", ({ ref, requestGeneration, includeTurns }) => {
    if (!ref) throw new Error("thread/read requires ref");
    if (ref === "child" && !healed) {
      failed.resolve();
      throw new WireError("connection reset", -32001, { evenerErrorInfo: "sessionUnavailable" });
    }
    const read = response(ref);
    return { ...read, requestGeneration, thread: includeTurns === false ? { ...read.thread, turns: [] } : read.thread };
  });
  fake.on("evener/thread/activity/read", ({ ref, scope }) => {
    if (ref === "child" && !healed)
      throw new WireError("connection reset", -32001, { evenerErrorInfo: "sessionUnavailable" });
    return { ...activitySummary(ref, scope), context: context(ref) };
  });
  mount(fake);
  await act(async () => failed.promise);
  expect(screen.queryByText("This session was deleted")).toBeNull();
  expect(screen.queryByText("Transcript unavailable")).toBeNull();
  const source = currentPane();
  healed = true;
  act(() =>
    fake.emitNotification({ method: "evener/thread/resync", params: { ref: "child", threadId: "wire-child-id" } }),
  );
  await screen.findByText("child content child-id");
  await screen.findByText("root content old-root");
  expect(currentPane()).toBe(source);
  expect(columnRefs()).toEqual(["root", "child"]);
  expect(workspaceStore.getState().focusedPaneId).toBe("cascade");
});

test("deeper drill retains the root view as a paused spine and pop reuses its source role", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  const lifetime = conversationPaneLifetime(currentPane());
  const rootView = retainedTranscriptReadView(lifetime, "root", "session");
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await screen.findByText("grandchild content grandchild-id");
  expect(columnRefs()).toEqual(["child", "grandchild"]);
  expect(screen.getByTestId("cascade-spine").getAttribute("data-scope-ref")).toBe("root");
  expect(rootView.alive).toBe(true);
  expect(rootView.readable).toBe(false);
  act(() => popAgentCascade("cascade", "root"));
  await screen.findByText("root content old-root");
  expect(columnRefs()).toEqual(["root"]);
  expect(rootView.readable).toBe(true);
  expect(screen.queryByTestId("cascade-spine")).toBeNull();
});

// The drill reveals the leaf by scrolling the column track to its end. The
// pane can narrow right after (on narrow desktop, a sidebar taking its width
// mid-drill), and columns keep animating their widths, which leaves the leaf
// past the track's right edge unless the track keeps to its end.
type Geometry = { scrollWidth: number; clientWidth: number };

function trackGeometry() {
  const resized: ResizeObserverCallback[] = [];
  const observed = new Set<Element>();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: ResizeObserverCallback) {
        resized.push(callback);
      }
      observe(target: Element) {
        observed.add(target);
      }
      unobserve() {}
      disconnect() {}
    },
  );
  const geometry: Geometry = { scrollWidth: 1172, clientWidth: 372 };
  return {
    geometry,
    observed,
    attach(track: HTMLElement) {
      Object.defineProperty(track, "scrollWidth", { configurable: true, get: () => geometry.scrollWidth });
      Object.defineProperty(track, "clientWidth", { configurable: true, get: () => geometry.clientWidth });
    },
    resize(change: Partial<Geometry>) {
      Object.assign(geometry, change);
      act(() => {
        for (const callback of resized) callback([], {} as ResizeObserver);
      });
    },
  };
}

type TrackStep = (element: HTMLElement, geometry: Geometry) => void;
const scrollTo = (element: HTMLElement, left: number) => {
  element.scrollLeft = left;
  fireEvent.scroll(element);
};
const narrow = { clientWidth: 152 };

test.each<{ name: string; before?: TrackStep; change: Partial<Geometry>; want: number }>([
  { name: "the drilled leaf stays revealed when the track narrows", change: narrow, want: 1172 - 152 },
  {
    name: "a reader who scrolled back keeps their place when the track narrows",
    before: (element) => scrollTo(element, 100),
    change: narrow,
    want: 100,
  },
  {
    name: "a reader who scrolls back to the end follows the leaf again",
    before: (element) => {
      scrollTo(element, 100);
      scrollTo(element, 1172 - 372);
    },
    change: narrow,
    want: 1172 - 152,
  },
  {
    name: "the reveal's own scroll event, after a column briefly widened the track, keeps the leaf followed",
    before: (element, geometry) => {
      geometry.scrollWidth = 1182;
      fireEvent.scroll(element);
    },
    change: narrow,
    want: 1182 - 152,
  },
  {
    name: "a scroll the browser clamped to a shrunk end keeps the leaf followed",
    before: (element, geometry) => {
      geometry.scrollWidth = 1150;
      scrollTo(element, 1150 - 372);
    },
    change: narrow,
    want: 1150 - 152,
  },
  {
    name: "a column widening after the reveal keeps the leaf revealed",
    change: { scrollWidth: 1600 },
    want: 1600 - 372,
  },
])("$name", async ({ before, change, want }) => {
  const track = trackGeometry();
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("child content child-id");
  const element = screen.getAllByTestId("cascade-column")[0]?.parentElement;
  if (!element) throw new Error("Missing cascade track");
  track.attach(element);
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await screen.findByText("grandchild content grandchild-id");
  expect(element.scrollLeft).toBe(1172 - 372);
  // Columns change the track's content width without resizing the track.
  for (const column of element.children) expect(track.observed.has(column)).toBe(true);
  before?.(element, track.geometry);

  track.resize(change);

  expect(element.scrollLeft).toBe(want);
});

test.each([
  ["active", "Active", "Running", null],
  ["awaiting", "Awaiting", "Needs you", "needs-you"],
  ["warning", "Warning", "Needs you", "needs-you"],
  ["restartRequired", "RestartRequired", "Needs you", "needs-you"],
  ["systemError", "SystemError", "Broken", "failed"],
  ["idle", "Idle", null, null],
  ["closed", "Closed", null, null],
  ["notLoaded", "NotLoaded", null, null],
  ["futureState", "FutureState", null, null],
] as const)(
  "a %s ancestor uses the session-list signal and keeps its exact runtime text available",
  async (state, runtimeText, signalLabel, signalState) => {
    const { fake, statuses } = fixture();
    statuses.set("root", state);
    mount(fake);
    await screen.findByText("child content child-id");
    act(() =>
      enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
    );
    await screen.findByText("grandchild content grandchild-id");
    const spine = screen.getByTestId("cascade-spine");
    const runtime = within(spine).getByTitle(runtimeText);
    expect(runtime.getAttribute("data-runtime-state")).toBe(state);
    expect(within(runtime).getByText(runtimeText).getAttribute("aria-hidden")).not.toBe("true");
    if (signalLabel) {
      const signal = within(runtime).getByRole("img", { name: signalLabel });
      expect(signal.getAttribute("data-status")).toBe(signalState);
    } else {
      expect(within(runtime).queryByRole("img")).toBeNull();
    }
    expect(columnRefs()).toEqual(["child", "grandchild"]);
    act(() => fireEvent.click(within(spine).getByRole("button", { name: "Show root" })));
    await screen.findByText("root content old-root");
    const column = screen.getByTestId("cascade-column");
    expect(within(column).getByText(runtimeText).getAttribute("data-runtime-state")).toBe(state);
    expect(screen.queryByTestId("cascade-spine")).toBeNull();
  },
);

test("a paused ancestor updates its signal through runtime changes without replacing the selected branch", async () => {
  const { fake, statuses } = fixture();
  statuses.set("root", "active");
  mount(fake);
  await screen.findByText("child content child-id");
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await screen.findByText("grandchild content grandchild-id");
  const spine = screen.getByTestId("cascade-spine");
  const branch = currentPane();
  expect(within(spine).getByRole("img", { name: "Running" })).toBeTruthy();
  for (const [state, text, label] of [
    ["awaiting", "Awaiting", "Needs you"],
    ["systemError", "SystemError", "Broken"],
    ["idle", "Idle", null],
  ] as const) {
    act(() =>
      fake.emitNotification({
        method: "thread/status/changed",
        params: { ref: "root", threadId: "wire-old-root", status: { type: state } },
      }),
    );
    const runtime = within(spine).getByTitle(text);
    if (label) expect(within(runtime).getByRole("img", { name: label })).toBeTruthy();
    else expect(within(runtime).queryByRole("img")).toBeNull();
    expect(currentPane()).toBe(branch);
    expect(columnRefs()).toEqual(["child", "grandchild"]);
  }
});

test("an ancestor with pending runtime metadata stays explicitly unknown until its existing read recovers", async () => {
  const { fake, response } = fixture();
  const held = deferred<ThreadReadResponse>();
  fake.on("thread/read", async ({ ref, requestGeneration, includeTurns }) => {
    if (!ref) throw new Error("thread/read requires ref");
    const read = ref === "root" ? await held.promise : response(ref);
    return { ...read, requestGeneration, thread: includeTurns === false ? { ...read.thread, turns: [] } : read.thread };
  });
  mount(fake);
  await screen.findByText("child content child-id");
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await screen.findByText("grandchild content grandchild-id");
  const spine = screen.getByTestId("cascade-spine");
  const branch = currentPane();
  try {
    const runtime = within(spine).getByTitle("State unknown");
    expect(runtime.getAttribute("data-runtime-state")).toBe("unknown");
    expect(within(runtime).getByText("State unknown")).toBeTruthy();
    expect(within(runtime).queryByRole("img")).toBeNull();
  } finally {
    await act(async () => held.resolve(response("root")));
  }
  await within(spine).findByTitle("Idle");
  expect(within(spine).queryByRole("img")).toBeNull();
  expect(currentPane()).toBe(branch);
  expect(columnRefs()).toEqual(["child", "grandchild"]);
});

test("unknown ancestry labels the proven clicked segment without blocking child reading", async () => {
  const { fake } = fixture(false);
  mount(fake);
  await screen.findByText("child content child-id");
  expect(screen.getByText("Earlier ancestry is incomplete")).toBeTruthy();
  expect(columnRefs()).toEqual(["root", "child"]);
});

test("a rebound root retires old child edges and readers before replacement content is visible", async () => {
  const { fake, identities } = fixture();
  const mounted = mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  const lifetime = conversationPaneLifetime(currentPane());
  const childView = retainedTranscriptReadView(lifetime, "child", "cascade");
  const mixtures: string[] = [];
  const observer = new MutationObserver(() => {
    const text = mounted.container.textContent ?? "";
    if (text.includes("root content new-root") && (childView.alive || text.includes("child content child-id")))
      mixtures.push(text);
  });
  observer.observe(mounted.container, { childList: true, subtree: true, characterData: true });
  try {
    const button = screen.getByRole("button", { name: "Return to previous view" });
    button.focus();
    identities.set("root", "new-root");
    act(() =>
      fake.emitNotification({ method: "evener/thread/resync", params: { ref: "root", threadId: "wire-new-root" } }),
    );
    await waitFor(() => expect(currentParams().ref).toBe("root"));
    expect(currentParams().source).toEqual({ type: "session", params: { ref: "root" } });
    expect(currentParams().edges).toEqual([]);
    expect(childView.alive).toBe(false);
    await screen.findByText("root content new-root");
    expect(columnRefs()).toEqual(["root"]);
    expect(mixtures).toEqual([]);
    expect(document.activeElement).toBe(button);
    expect(workspaceStore.getState().focusedPaneId).toBe("cascade");
  } finally {
    observer.disconnect();
  }
});

test("a new cursor epoch for the same root preserves child branch and retained readers", async () => {
  const { fake, setEpoch } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  const childView = retainedTranscriptReadView(conversationPaneLifetime(currentPane()), "child", "cascade");
  // The root's session summary; its subtree summary (the Agents count) re-reads too.
  const rootReads = () =>
    fake.calls.filter((call) => {
      const params = call.params as { ref: string; scope?: string };
      return call.method === "evener/thread/activity/read" && params.ref === "root" && params.scope !== "subtree";
    }).length;
  const initialReads = rootReads();
  setEpoch("epoch-2");
  act(() =>
    fake.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref: "root", threadId: "wire-old-root", sessionId: "old-root", resources: ["summary"] },
    }),
  );
  await waitFor(() => expect(rootReads()).toBe(initialReads + 1));
  expect(currentParams().ref).toBe("child");
  expect(childView.alive).toBe(true);
  expect(columnRefs()).toEqual(["root", "child"]);
});

test("a held abandoned grandchild read cannot replace a later sibling or move focus", async () => {
  const { fake, response } = fixture();
  mount(fake);
  await screen.findByText("child content child-id");
  const held = deferred<ThreadReadResponse>();
  const entered = deferred<void>();
  fake.on("thread/read", ({ ref, includeTurns, requestGeneration }) => {
    if (!ref) throw new Error("thread/read requires ref");
    if (ref === "grandchild" && includeTurns !== false) {
      entered.resolve();
      return held.promise.then((read) => ({ ...read, requestGeneration }));
    }
    const read = response(ref);
    return { ...read, requestGeneration, thread: includeTurns === false ? { ...read.thread, turns: [] } : read.thread };
  });
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await act(async () => entered.promise);
  const grandchild = retainedTranscriptReadView(conversationPaneLifetime(currentPane()), "grandchild", "cascade");
  act(() => {
    popAgentCascade("cascade", "root");
    enterAgentCascade(activityDelegate({ ownerRef: "root", childRef: "sibling", delegateId: "d3" }), "cascade");
  });
  await screen.findByText("sibling content sibling-id");
  expect(grandchild.alive).toBe(false);
  const siblingColumn = screen
    .getAllByTestId("cascade-column")
    .find((column) => column.getAttribute("data-scope-ref") === "sibling");
  if (!siblingColumn) throw new Error("Missing sibling column");
  const open = within(siblingColumn).getByRole("button", { name: "Open conversation" });
  open.focus();
  await act(async () => held.resolve(response("grandchild")));
  expect(currentParams().ref).toBe("sibling");
  expect(columnRefs()).toEqual(["root", "sibling"]);
  expect(screen.queryByText("grandchild content grandchild-id")).toBeNull();
  expect(workspaceStore.getState().focusedPaneId).toBe("cascade");
  expect(document.activeElement).toBe(open);
});
