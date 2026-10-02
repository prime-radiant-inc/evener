import type { SessionActivityContext, ThreadReadResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { useStore } from "zustand";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import {
  activityContext,
  activityDelegate,
  activitySummary,
  activityThread,
} from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { resetTranscriptViewRegistryForTests } from "../session/transcript/flow/transcriptViewRegistry";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { resetTranscriptPagingForTests } from "../session/transcript/useTranscript";
import { enterAgentCascade, popAgentCascade } from "./actions";
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
  fake.on("evener/thread/activity/read", ({ ref }) => ({ ...activitySummary(ref), context: context(ref) }));
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
  const rootReads = () =>
    fake.calls.filter(
      (call) => call.method === "evener/thread/activity/read" && (call.params as { ref: string }).ref === "root",
    ).length;
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
