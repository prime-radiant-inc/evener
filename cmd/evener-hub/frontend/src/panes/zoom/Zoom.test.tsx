import type { SessionActivityContext, ThreadReadResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "zustand";
import { accessibleName } from "../../accessibleNameTestUtils";
import { chromeStore } from "../../shell/chromeStore";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { installFocusedScope, summaryOf } from "../../shell/statusbar/scopeTestUtils";
import {
  consumePaneFocus,
  type OpenPaneRecord,
  requestPaneFocus,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import {
  activityContext,
  activityDelegate,
  activitySummary,
  activityThread,
} from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { emptyStateReasons } from "../../widgets/emptystate/emptyStateTestUtils";
import { readDraft } from "../session/composer/draft";
import { replaceEditorText } from "../session/testing/editor";
import { resetTranscriptViewRegistryForTests } from "../session/transcript/flow/transcriptViewRegistry";
import { readerWireTurns } from "../session/transcript/transcriptReaderTestUtils";
import { holdReaderFrames, installTranscriptGeometry } from "../session/transcript/transcriptReadingGeometryTestUtils";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { resetTranscriptPagingForTests } from "../session/transcript/useTranscript";
import { enterAgentCascade, popAgentCascade, returnFromAgentCascade } from "./actions";
import { cascadeScope } from "./cascadeTestUtils";
import { recordCascadeOrigin } from "./inspectionOrigin";
import type { SessionZoomParams } from "./intent";
import Zoom from "./Zoom";
import "./index";

let height: PropertyDescriptor | undefined;
beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetNavigationStoreForTests();
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
  resetNavigationStoreForTests();
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

function fixture(ancestryKnown = true, leafRef = "child") {
  const fake = new FakeClient("ready");
  const statuses = new Map<string, string>();
  const identities = new Map([
    ["root", "old-root"],
    ["child", "child-id"],
    ["grandchild", "grandchild-id"],
    ["greatgrandchild", "greatgrandchild-id"],
    ["sibling", "sibling-id"],
  ]);
  let epoch = "epoch-1";
  const LINEAGE: Record<string, string[]> = {
    root: [],
    child: ["root"],
    sibling: ["root"],
    grandchild: ["root", "child"],
    greatgrandchild: ["root", "child", "grandchild"],
  };
  const PARENT_REF: Record<string, string> = {
    child: "root",
    sibling: "root",
    grandchild: "child",
    greatgrandchild: "grandchild",
  };
  const DELEGATE_ID: Record<string, string> = { child: "d1", grandchild: "d2", greatgrandchild: "d4", sibling: "d3" };
  const lineage = (ref: string) => LINEAGE[ref] ?? ["root"];
  const context = (ref: string): SessionActivityContext => ({
    ...activityContext(ref),
    sessionId: identities.get(ref) ?? ref,
    rootRef: "root",
    epoch,
    ancestryKnown,
    ...(ref === "root" ? {} : { parentRef: PARENT_REF[ref] ?? "root", delegateId: DELEGATE_ID[ref] ?? "d1" }),
    ancestors: lineage(ref).map((ancestor) => ({
      ref: ancestor,
      sessionId: identities.get(ancestor) ?? ancestor,
      title: ancestor,
      ...(ancestor === "root" ? {} : { delegateId: DELEGATE_ID[ancestor] ?? "d1" }),
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
    ref: leafRef,
    source: { type: "session", params: { ref: "root" } },
    edges: [{ ownerRef: "root", childRef: leafRef, delegateId: "d1" }],
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
function spineRefs() {
  return screen.queryAllByTestId("cascade-spine").map((spine) => spine.getAttribute("data-scope-ref"));
}
function inspectedCascade() {
  const source: OpenPaneRecord = { id: "source", type: "session", params: { ref: "root" }, slot: "main" };
  const inspector: OpenPaneRecord = {
    ...currentPane(),
    slot: "secondary",
    params: {
      ref: "child",
      source: { type: "session", params: { ref: "root" } },
      edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
      inspection: { origin: { paneId: source.id, type: "session", ref: "root" } },
    } satisfies SessionZoomParams,
  };
  workspaceStore.setState({ panes: [source, inspector], focusedPaneId: inspector.id });
  recordCascadeOrigin(inspector, source);
  return { source, inspector };
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

test("root and child render through real conversation readers inside one scaffold", async () => {
  const { fake } = fixture();
  const mounted = mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  expect(columnRefs()).toEqual(["root", "child"]);
  expect(mounted.container.querySelectorAll('[data-pane-scaffold="cascade"]')).toHaveLength(1);
  expect(screen.getAllByTestId("transcript-virtual-list")).toHaveLength(2);
  // The per-column activity controls render below the transcript content.
  for (const column of screen.getAllByTestId("cascade-column")) {
    const content = within(column).getByTestId("cascade-column-content");
    const controls = within(column).getByTestId("cascade-activity-controls");
    expect(content.compareDocumentPosition(controls) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  }
  // Every readable column renders its conversation: one composer each, no
  // more than the columns on screen.
  expect(screen.getAllByRole("textbox")).toHaveLength(2);
  expect(screen.getByRole("button", { name: "Return to previous view" })).toBeTruthy();
  expect(fake.calls.filter((call) => /send|resume|steer|interrupt/.test(call.method))).toHaveLength(0);
});

test("an unloaded scope uses its navigation title through column, spine and pop", async () => {
  const navigationTitle = "nav-4212-root";
  const liveTitle = "model-4212-root";
  installFocusedScope("child", summaryOf({ ref: "root", title: navigationTitle }));
  const { fake, response } = fixture();
  const held = deferred<ThreadReadResponse>();
  const rootRead = response("root");
  const namedRoot = { ...rootRead, thread: { ...rootRead.thread, name: liveTitle } };
  fake.on("thread/read", async ({ ref, requestGeneration, includeTurns }) => {
    if (!ref) throw new Error("thread/read requires ref");
    const read = ref === "root" ? await held.promise : response(ref);
    return { ...read, requestGeneration, thread: includeTurns === false ? { ...read.thread, turns: [] } : read.thread };
  });
  mount(fake);
  try {
    await screen.findByText("child content child-id");
    expect(threadsStore.getState().threads.has("root")).toBe(false);
    const rootColumn = cascadeScope("root", "cascade-column");
    const crumb = screen
      .getByRole("navigation", { name: "Scope" })
      .querySelector('[data-session-navigation-ref="root"]');
    expect(crumb?.textContent).toBe(navigationTitle);
    expect.soft(within(rootColumn).getByRole("heading", { level: 3 }).textContent).toBe(navigationTitle);
    expect.soft(rootColumn.getAttribute("aria-label")).toBe(navigationTitle);

    act(() =>
      enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
    );
    await screen.findByText("grandchild content grandchild-id");
    expect(threadsStore.getState().threads.has("root")).toBe(false);
    const spine = cascadeScope("root", "cascade-spine");
    const pop = spine.querySelector<HTMLButtonElement>("button[aria-label]");
    if (!pop) throw new Error("Missing spine pop control");
    expect.soft(pop.textContent).toBe(navigationTitle);
    expect.soft(accessibleName(pop)).toContain(navigationTitle);
    expect.soft(spine.getAttribute("aria-label")).toBe(navigationTitle);
    act(() => fireEvent.click(pop));
    expect(currentParams().ref).toBe("root");
    expect(currentParams().edges).toEqual([]);
    expect(columnRefs()).toEqual(["root"]);
    expect
      .soft(within(cascadeScope("root", "cascade-column")).getByRole("heading", { level: 3 }).textContent)
      .toBe(navigationTitle);
  } finally {
    await act(async () => held.resolve(namedRoot));
  }
  await screen.findByText("root content old-root");
  expect(within(cascadeScope("root", "cascade-column")).getByRole("heading", { level: 3 }).textContent).toBe(liveTitle);
  expect(currentParams().ref).toBe("root");
});

test("a rebound scope rejects its stale model name and transcript until a matching image recovers", async () => {
  const navigationTitle = "nav-4212-rebound";
  const staleTitle = "model-4212-stale";
  const liveTitle = "model-4212-recovered";
  installFocusedScope("child", summaryOf({ ref: "root", title: navigationTitle }));
  const { fake, response, identities } = fixture();
  const held = deferred<ThreadReadResponse>();
  let holdRoot = false;
  fake.on("thread/read", async ({ ref, requestGeneration, includeTurns }) => {
    if (!ref) throw new Error("thread/read requires ref");
    const read = ref === "root" && holdRoot ? await held.promise : response(ref);
    return {
      ...read,
      requestGeneration,
      thread: {
        ...read.thread,
        name: ref === "root" && !holdRoot ? staleTitle : read.thread.name,
        turns: includeTurns === false ? [] : read.thread.turns,
      },
    };
  });
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  const childView = retainedTranscriptReadView(conversationPaneLifetime(currentPane()), "child", "cascade");
  holdRoot = true;
  identities.set("root", "new-root");
  try {
    act(() =>
      fake.emitNotification(
        activityChangedNotification({ ref: "root", threadId: "wire-old-root", sessionId: "old-root" }, ["summary"]),
      ),
    );
    await waitFor(() => expect(currentParams().ref).toBe("root"));
    expect(threadsStore.getState().threads.get("root")).toMatchObject({ name: staleTitle, imageSessionId: "old-root" });
    const rootColumn = cascadeScope("root", "cascade-column");
    expect.soft(within(rootColumn).getByRole("heading", { level: 3 }).textContent).toBe(navigationTitle);
    expect.soft(rootColumn.getAttribute("aria-label")).toBe(navigationTitle);
    expect(screen.queryByText("root content old-root")).toBeNull();
    expect(screen.queryByText("child content child-id")).toBeNull();
    expect(childView.alive).toBe(false);
    expect(retainedTranscriptReadView(conversationPaneLifetime(currentPane()), "root", "session").readable).toBe(false);
  } finally {
    const read = response("root");
    await act(async () => held.resolve({ ...read, thread: { ...read.thread, name: liveTitle } }));
  }
  await screen.findByText("root content new-root");
  expect(within(cascadeScope("root", "cascade-column")).getByRole("heading", { level: 3 }).textContent).toBe(liveTitle);
  expect(currentParams().ref).toBe("root");
});

test("separated Zoom Return closes inspection and focuses its surviving source", async () => {
  const { fake } = fixture();
  const { source, inspector } = inspectedCascade();
  const sourceLifetime = conversationPaneLifetime(source);
  const inspectorLifetime = conversationPaneLifetime(inspector);
  mount(fake);
  await screen.findByText("child content child-id");
  // The origin conversation is already live as its own pane, so the cascade
  // never renders it: the first drill shows only the subagent's conversation.
  expect(columnRefs()).toEqual(["child"]);
  expect(spineRefs()).toEqual([]);
  expect(within(screen.getByTestId("cascade-column")).getByRole("textbox")).toBeTruthy();
  // The cascade fabricates no composer for the origin ref: the origin
  // conversation never renders in the pane.
  expect(inspectorLifetime.composers.get("root")).toBeUndefined();
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

test("a live origin conversation never renders in the pane, at any depth", async () => {
  const { fake } = fixture();
  const { source, inspector } = inspectedCascade();
  mount(fake);
  await screen.findByText("child content child-id");
  // No Agent path nav and no pane title: the spines are the breadcrumb and
  // the tab label carries the selected conversation.
  expect(screen.queryByRole("navigation", { name: "Agent path" })).toBeNull();
  expect(screen.queryByText("Agent cascade")).toBeNull();
  // The deleted path nav carried the pane's positive name resolution; its
  // replacement surface is the mobile top bar's published title: the
  // selected conversation's resolved name, never a pane-generic label.
  expect(chromeStore.getState().paneTitle).toBe("child");
  // The single column fills the pane rather than pinning at 440px, so the
  // first drill reads as the subagent's own conversation pane.
  expect(columnRefs()).toEqual(["child"]);
  expect(spineRefs()).toEqual([]);
  expect(screen.getByTestId("cascade-column").style.width).toBe("100%");
  // Deeper drills keep the readable pair above the removed origin: level 2
  // has no spines at all, and level 3 collapses the first non-origin
  // ancestor while the origin stays absent.
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), inspector.id),
  );
  await screen.findByText("grandchild content grandchild-id");
  expect(columnRefs()).toEqual(["child", "grandchild"]);
  expect(spineRefs()).toEqual([]);
  act(() =>
    enterAgentCascade(
      activityDelegate({ ownerRef: "grandchild", childRef: "greatgrandchild", delegateId: "d4" }),
      inspector.id,
    ),
  );
  await screen.findByText("greatgrandchild content greatgrandchild-id");
  expect(columnRefs()).toEqual(["grandchild", "greatgrandchild"]);
  expect(spineRefs()).toEqual(["child"]);
  // Popping back to depth one keeps the origin filtered while it lives...
  act(() => popAgentCascade(inspector.id, "child"));
  await screen.findByText("child content child-id");
  expect(columnRefs()).toEqual(["child"]);
  expect(spineRefs()).toEqual([]);
  // ...and closing the origin retires the association, so the readable
  // parent column returns without disturbing the selected leaf.
  act(() => workspaceStore.getState().closePane(source.id));
  await screen.findByText("root content old-root");
  expect(columnRefs()).toEqual(["root", "child"]);
  expect(spineRefs()).toEqual([]);
});

test("a cascade with no live origin keeps the readable parent column", async () => {
  const { fake } = fixture();
  const inspector: OpenPaneRecord = {
    ...currentPane(),
    slot: "secondary",
    params: {
      ref: "child",
      source: { type: "session", params: { ref: "root" } },
      edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
      inspection: { origin: null },
    } satisfies SessionZoomParams,
  };
  workspaceStore.setState({ panes: [inspector], focusedPaneId: inspector.id });
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  expect(columnRefs()).toEqual(["root", "child"]);
});

test("a local notLoaded column with no send surface keeps its session actions menu", async () => {
  // The drilled column's conversation is a local notLoaded snapshot with
  // send:false, no recovery obligation and no retained owner. The embedded
  // composer cannot render its card (or its own session menu) in that state,
  // and the column's Open conversation control is gone, so the column must
  // keep the fallback session menu a standalone pane shows here - its Force
  // shutdown action is the only recovery this column can offer.
  const { fake, statuses } = fixture(true, "local:child");
  statuses.set("local:child", "notLoaded");
  fake.on("evener/thread/forceStop", () => {
    statuses.set("local:child", "idle");
    return {};
  });
  mount(fake);
  await screen.findByText("local:child content local:child");
  const leaf = cascadeScope("local:child", "cascade-column");
  expect(leaf.querySelector("[data-composer] [role='textbox']")).toBeNull();
  const trigger = within(leaf).getByRole("button", { name: "Session actions" });
  fireEvent.click(trigger);
  fireEvent.click(await screen.findByRole("menuitem", { name: "Force shutdown…" }));
  fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Force shutdown" }));
  // Recovery: the confirmed shutdown refreshes the snapshot and the column's
  // composer card returns, so the fallback menu's one recovery action leads
  // back to a usable conversation in the column.
  await waitFor(() => expect(leaf.querySelector("[data-composer] [role='textbox']")).not.toBeNull());
});

test.each(["deleted", "missing"] as const)(
  "a $0 child has a scoped terminal explanation while parent and Return stay usable",
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
    const child = cascadeScope("child", "cascade-column");
    await waitFor(() => expect(emptyStateReasons(child)).toEqual([failure === "deleted" ? "deleted" : "unavailable"]));
    expect(within(child).queryByRole("status", { name: "Loading" })).toBeNull();
    expect(screen.getByText("root content old-root")).toBeTruthy();
    if (failure === "deleted") expect(threadsStore.getState().deletedRefs.has("child")).toBe(true);
    act(() => popAgentCascade("cascade", "root"));
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
  expect(emptyStateReasons(cascadeScope("child", "cascade-column"))).toEqual(["deleted"]);
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
  expect(emptyStateReasons(cascadeScope("child", "cascade-column"))).toEqual([]);
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
  expect(emptyStateReasons(document.body)).not.toContain("deleted");
  expect(emptyStateReasons(document.body)).not.toContain("unavailable");
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

test("a store-driven resume replaces the drilled column's identity in place", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await screen.findByText("grandchild content grandchild-id");
  expect(columnRefs()).toEqual(["child", "grandchild"]);
  const lifetime = conversationPaneLifetime(currentPane());
  const childView = retainedTranscriptReadView(lifetime, "child", "cascade");
  // A draft typed in the parent column must follow the resumed identity.
  const editor = within(cascadeScope("child")).getByRole("textbox", { name: "Message" });
  replaceEditorText(editor, "draft follows the resume");

  // The resume driver's seam: the store resumed the conversation the column
  // was showing onto a new ref, already hydrated by the driver's refresh
  // before the identity publishes.
  await act(async () => {
    await threadsStore.getState().ensureThread("child-resumed");
  });
  act(() =>
    threadsStore.setState((state) => ({
      resumedIdentities: new Map(state.resumedIdentities).set("child", "child-resumed"),
    })),
  );
  await screen.findByText("child-resumed content child-resumed");

  // The path entry is replaced in place: the column keeps its position, the
  // drilled descendant keeps its edge, and the superseded identity's retained
  // reader is retired.
  expect(columnRefs()).toEqual(["child-resumed", "grandchild"]);
  expect(screen.getByText("grandchild content grandchild-id")).toBeTruthy();
  expect(childView.alive).toBe(false);
  expect(currentParams()).toEqual({
    ref: "grandchild",
    source: { type: "session", params: { ref: "root" } },
    edges: [
      { ownerRef: "root", childRef: "child-resumed", delegateId: "d1" },
      { ownerRef: "child-resumed", childRef: "grandchild", delegateId: "d2" },
    ],
  });
  await waitFor(() =>
    expect(within(cascadeScope("child-resumed")).getByRole("textbox", { name: "Message" }).textContent).toBe(
      "draft follows the resume",
    ),
  );
});

test("simultaneous parent and leaf resumes each replace their own scope", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await screen.findByText("grandchild content grandchild-id");
  expect(columnRefs()).toEqual(["child", "grandchild"]);

  // One commit carries both columns' identity changes: both callbacks hold
  // the same pane record, and whichever resume runs first retypes it before
  // the other applies.
  await act(async () => {
    await threadsStore.getState().ensureThread("child-resumed");
    await threadsStore.getState().ensureThread("grandchild-resumed");
  });
  act(() =>
    threadsStore.setState((state) => ({
      resumedIdentities: new Map(state.resumedIdentities)
        .set("child", "child-resumed")
        .set("grandchild", "grandchild-resumed"),
    })),
  );
  await screen.findByText("child-resumed content child-resumed");
  expect(currentParams().ref).toBe("grandchild-resumed");
  expect(currentParams().edges).toEqual([
    { ownerRef: "root", childRef: "child-resumed", delegateId: "d1" },
    { ownerRef: "child-resumed", childRef: "grandchild-resumed", delegateId: "d2" },
  ]);
  // The leaf column's conversation renders the resumed identity too.
  await screen.findByText("grandchild-resumed content grandchild-resumed");
});

test("resuming the rendered source moves the cascade's source ownership to the resumed identity", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  // A draft typed in the source column follows the resumed identity.
  const editor = within(cascadeScope("root")).getByRole("textbox", { name: "Message" });
  replaceEditorText(editor, "source draft carries to the resumed identity");

  await act(async () => {
    await threadsStore.getState().ensureThread("root-resumed");
  });
  act(() =>
    threadsStore.setState((state) => ({
      resumedIdentities: new Map(state.resumedIdentities).set("root", "root-resumed"),
    })),
  );
  await screen.findByText("root-resumed content root-resumed");

  // The source column moved onto the resumed identity, and the source
  // descriptor and retained source ownership moved with it.
  expect(columnRefs()).toEqual(["root-resumed", "child"]);
  expect(currentParams().source).toMatchObject({ type: "session", params: { ref: "root-resumed" } });
  expect(conversationPaneLifetime(currentPane()).sourceRef).toBe("root-resumed");
  const resumedEditor = within(cascadeScope("root-resumed")).getByRole("textbox", { name: "Message" });
  expect(resumedEditor.textContent).toBe("source draft carries to the resumed identity");
  // Subsequent editing persists under the resumed identity: the retained
  // state binds persistence to the ref it was created with, so a reload of
  // the resumed conversation finds the newer text while the superseded
  // identity does not.
  replaceEditorText(resumedEditor, "edited after the source resume");
  expect(readDraft("root-resumed")).toBe("edited after the source resume");
  expect(readDraft("root")).not.toBe("edited after the source resume");

  // Return reopens the resumed conversation with its carried draft, not
  // the superseded identity.
  act(() => returnFromAgentCascade("cascade"));
  expect(currentPane()).toMatchObject({ type: "session", params: { ref: "root-resumed" } });
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

// The deeper drill leaves two readable columns (the leaf and its immediate
// parent) sharing one hosting pane, and the dispatcher runs same-action
// handlers in registration order until one accepts - so keyboard scroll
// must belong to the leaf alone, or the parent column's registration would
// swallow every Alt+Arrow and Alt+Home/End keypress forever.
test("keyboard scroll drives the drilled leaf column, not the first-registered parent", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await screen.findByText("grandchild content grandchild-id");
  expect(columnRefs()).toEqual(["child", "grandchild"]);
  expect(workspaceStore.getState().focusedPaneId).toBe("cascade");

  // VirtualList's markup is port > sizer > item > row, so each column's
  // scroll port is three parents above one of its transcript rows.
  const scrollPort = (column: HTMLElement) => {
    const row = column.querySelector('[data-testid="transcript-row"]');
    const port = row?.parentElement?.parentElement?.parentElement ?? null;
    if (!port) throw new Error("Missing column scroll port");
    return port;
  };
  const parentPort = scrollPort(cascadeScope("child", "cascade-column"));
  const leafPort = scrollPort(cascadeScope("grandchild", "cascade-column"));
  const parentBefore = parentPort.scrollTop;
  const leafBefore = leafPort.scrollTop;

  act(() => {
    fireEvent.keyDown(document.body, { key: "ArrowDown", code: "ArrowDown", altKey: true });
  });

  expect(leafPort.scrollTop).toBeGreaterThan(leafBefore);
  expect(parentPort.scrollTop).toBe(parentBefore);
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
  ["active", "Active", "working"],
  ["awaiting", "Awaiting", "needs-you"],
  ["warning", "Warning", "needs-you"],
  ["restartRequired", "RestartRequired", "needs-you"],
  ["systemError", "SystemError", "failed"],
  ["idle", "Idle", null],
  ["closed", "Closed", null],
  ["notLoaded", "NotLoaded", null],
  ["futureState", "FutureState", null],
] as const)(
  "a %s ancestor keeps the session-list signal glyph but hides its own family label from assistive tech",
  async (state, runtimeText, glyphState) => {
    // The active fixture thread carries no stop capability, so its column's
    // composer reports the stopless state through its diagnostic canary —
    // expected output for this fixture, asserted rather than silenced.
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
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
    // The shared session-list glyph is still rendered and still carries the
    // same signal family, but it is decorative: the compact ancestor status
    // exposes no second, family-labelled node beside its runtime text.
    expect(within(runtime).queryByRole("img")).toBeNull();
    const glyph = runtime.querySelector("[data-state]");
    if (glyphState) {
      expect(glyph?.getAttribute("data-state")).toBe(glyphState);
      expect(glyph?.getAttribute("aria-hidden")).toBe("true");
    } else {
      expect(glyph).toBeNull();
    }
    expect(columnRefs()).toEqual(["child", "grandchild"]);
    act(() => fireEvent.click(within(spine).getByRole("button", { name: "Show root" })));
    await screen.findByText("root content old-root");
    const column = screen.getByTestId("cascade-column");
    expect(within(column).getByText(runtimeText).getAttribute("data-runtime-state")).toBe(state);
    expect(screen.queryByTestId("cascade-spine")).toBeNull();
    if (state === "active") {
      expect(warn).toHaveBeenCalledWith(expect.stringContaining("no Stop"), expect.objectContaining({ ref: "root" }));
    } else {
      expect(warn).not.toHaveBeenCalled();
    }
    warn.mockRestore();
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
  const active = within(spine).getByTitle("Active");
  expect(active.querySelector("[data-state]")?.getAttribute("data-state")).toBe("working");
  expect(within(active).queryByRole("img")).toBeNull();
  for (const [state, text, glyphState] of [
    ["awaiting", "Awaiting", "needs-you"],
    ["systemError", "SystemError", "failed"],
    ["idle", "Idle", null],
  ] as const) {
    act(() =>
      fake.emitNotification({
        method: "thread/status/changed",
        params: { ref: "root", threadId: "wire-old-root", status: { type: state } },
      }),
    );
    const runtime = within(spine).getByTitle(text);
    expect(within(runtime).queryByRole("img")).toBeNull();
    const glyph = runtime.querySelector("[data-state]");
    if (glyphState) expect(glyph?.getAttribute("data-state")).toBe(glyphState);
    else expect(glyph).toBeNull();
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
    const runtime = spine.querySelector('[data-runtime-state="unknown"]');
    if (!(runtime instanceof HTMLElement)) throw new Error("Missing unknown runtime state");
    const label = runtime.querySelector("[data-runtime-label]");
    expect(label).not.toBeNull();
    expect(label?.textContent?.trim()).not.toBe("");
    // The unknown state names itself exactly as the runtime label it renders -
    // read off the element, so no literal copy is pinned - and carries no glyph.
    expect(accessibleName(runtime)).toBe(label?.textContent?.trim());
    expect(within(runtime).queryByRole("img")).toBeNull();
  } finally {
    await act(async () => held.resolve(response("root")));
  }
  await waitFor(() => expect(spine.querySelector('[data-runtime-state="idle"]')).not.toBeNull());
  expect(within(spine).queryByRole("img")).toBeNull();
  expect(currentPane()).toBe(branch);
  expect(columnRefs()).toEqual(["child", "grandchild"]);
});

test("unknown ancestry labels the proven clicked segment without blocking child reading", async () => {
  const { fake } = fixture(false);
  mount(fake);
  await screen.findByText("child content child-id");
  expect(screen.getByTestId("cascade-ancestry-incomplete")).toBeTruthy();
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

test("a rebound live origin retires its stale descendants even though it never renders", async () => {
  const { fake, identities } = fixture();
  inspectedCascade();
  mount(fake);
  await screen.findByText("child content child-id");
  expect(columnRefs()).toEqual(["child"]);
  const childView = retainedTranscriptReadView(conversationPaneLifetime(currentPane()), "child", "cascade");
  // The origin pane beside the cascade is the origin conversation's only
  // rendered reader; its replacement must still retire the cascade's stale
  // descendants through the live-origin binding.
  identities.set("root", "new-root");
  act(() =>
    fake.emitNotification({ method: "evener/thread/resync", params: { ref: "root", threadId: "wire-new-root" } }),
  );
  await waitFor(() => expect(currentParams().ref).toBe("root"));
  expect(currentParams().edges).toEqual([]);
  expect(childView.alive).toBe(false);
  // The popped origin never renders: the emptied pane keeps only the Return
  // path back to the source conversation.
  expect(columnRefs()).toEqual([]);
  expect(spineRefs()).toEqual([]);
  expect(screen.getByRole("button", { name: "Return to previous view" })).toBeTruthy();
});

test("a live origin replaced before its first summary resolves still retires stale descendants", async () => {
  const { fake, identities, context } = fixture();
  inspectedCascade();
  const held = deferred<ReturnType<typeof activitySummary>>();
  // The origin's own activity summary stays pending while the child's
  // ancestry — which already carries the origin's identity — is known.
  fake.on("evener/thread/activity/read", ({ ref, scope }) => {
    const summary = () => ({ ...activitySummary(ref, scope), context: context(ref) });
    if (ref === "root" && scope !== "subtree") return held.promise.then(summary);
    return summary();
  });
  mount(fake);
  await screen.findByText("child content child-id");
  expect(columnRefs()).toEqual(["child"]);
  const childView = retainedTranscriptReadView(conversationPaneLifetime(currentPane()), "child", "cascade");
  identities.set("root", "new-root");
  act(() =>
    fake.emitNotification({ method: "evener/thread/resync", params: { ref: "root", threadId: "wire-new-root" } }),
  );
  await act(async () => held.resolve(activitySummary("root", "session")));
  await waitFor(() => expect(currentParams().ref).toBe("root"));
  expect(currentParams().edges).toEqual([]);
  expect(childView.alive).toBe(false);
  expect(columnRefs()).toEqual([]);
  expect(screen.getByRole("button", { name: "Return to previous view" })).toBeTruthy();
});

// AppShell.test.tsx's installMobileViewport, verbatim: the mobile query
// matches, every other query (the reduced-motion read included) does not.
function installMobileViewport(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(max-width: 899px)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

test("a replaced nested origin never renders a non-leaf column on the phone", async () => {
  const { fake, identities } = fixture();
  installMobileViewport();
  // A nested origin: the cascade inspects child, drilled to grandchild, so the
  // full path is root -> child(origin) -> grandchild and only grandchild
  // renders on the phone.
  const origin: OpenPaneRecord = { id: "child-origin", type: "session", params: { ref: "child" }, slot: "main" };
  const inspector: OpenPaneRecord = {
    ...currentPane(),
    slot: "secondary",
    params: {
      ref: "grandchild",
      source: { type: "session", params: { ref: "child" } },
      edges: [{ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }],
      inspection: { origin: { paneId: origin.id, type: "session", ref: "child" } },
    } satisfies SessionZoomParams,
  };
  workspaceStore.setState({ panes: [origin, inspector], focusedPaneId: inspector.id });
  recordCascadeOrigin(inspector, origin);
  mount(fake);
  await screen.findByText("grandchild content grandchild-id");
  expect(columnRefs()).toEqual(["grandchild"]);
  identities.set("child", "new-child");
  act(() =>
    fake.emitNotification({ method: "evener/thread/resync", params: { ref: "child", threadId: "wire-new-child" } }),
  );
  await waitFor(() => expect(currentParams().ref).toBe("child"));
  // The popped origin never renders, at any depth: the phone keeps only the
  // Return path, never the non-leaf root column under the selected
  // conversation's name.
  expect(columnRefs()).toEqual([]);
  expect(spineRefs()).toEqual([]);
  expect(screen.getByRole("button", { name: "Return to previous view" })).toBeTruthy();
  expect(chromeStore.getState().paneTitle).toBe("child");
});

test("the phone cascade keeps one title, with the conversation's composer at the column bottom", async () => {
  const { fake } = fixture();
  installMobileViewport();
  mount(fake);
  await screen.findByText("child content child-id");
  // The mobile top bar owns the conversation name: the column's own h3 title
  // is desktop-only, so the phone shows no duplicate title row.
  expect(chromeStore.getState().paneTitle).toBe("child");
  expect(document.querySelector('[data-testid="cascade-column"] h3')).toBeNull();
  // The pane footer's status bar is desktop-only on the cascade: the
  // column's own controls below the transcript carry the activity, so no
  // second chips row piles under the transcript.
  expect(screen.queryByTestId("statusbar")).toBeNull();
  expect(screen.getByTestId("cascade-activity-controls")).toBeTruthy();
  // The column's runtime state and the conversation's own composer sit
  // below the transcript content, in the column's own footer bar — the bar
  // the home-indicator clearance pads.
  const footerBar = screen.getByTestId("cascade-column-footer");
  expect(
    screen.getByTestId("cascade-column-content").compareDocumentPosition(footerBar) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(footerBar.querySelector("[data-composer]")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Open conversation" })).toBeNull();
  expect(
    screen.getByTestId("cascade-column-content").compareDocumentPosition(footerBar) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
});

test("the desktop cascade column keeps its own title and the pane footer status bar", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("child content child-id");
  const childColumn = document.querySelector('[data-scope-ref="child"]') as HTMLElement;
  expect(childColumn.querySelector("h3")?.textContent).toBe("child");
  expect(screen.queryByTestId("cascade-column-footer")).toBeNull();
  expect(screen.getByTestId("statusbar")).toBeTruthy();
  const inColumn = within(childColumn);
  // The conversation's composer rides below the transcript content inside
  // the column; the header keeps the title and runtime state only.
  const composer = childColumn.querySelector("[data-composer]");
  expect(composer).toBeTruthy();
  expect(
    inColumn.getByTestId("cascade-column-content").compareDocumentPosition(composer as Node) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(inColumn.queryByRole("button", { name: "Open conversation" })).toBeNull();
  const header = childColumn.querySelector("header");
  if (!header) throw new Error("Missing column header");
  expect(
    header.compareDocumentPosition(inColumn.getByTestId("cascade-column-content") as Node) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
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
    fake.emitNotification(
      activityChangedNotification({ ref: "root", threadId: "wire-old-root", sessionId: "old-root" }, ["summary"]),
    ),
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
  const siblingColumn = cascadeScope("sibling", "cascade-column");
  const open = siblingColumn.querySelector<HTMLElement>('[data-composer] [role="textbox"]');
  if (!open) throw new Error("Missing sibling composer");
  act(() => {
    open.focus();
  });
  await act(async () => held.resolve(response("grandchild")));
  expect(currentParams().ref).toBe("sibling");
  expect(columnRefs()).toEqual(["root", "sibling"]);
  expect(screen.queryByText("grandchild content grandchild-id")).toBeNull();
  expect(workspaceStore.getState().focusedPaneId).toBe("cascade");
  expect(document.activeElement).toBe(open);
});
