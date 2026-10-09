import type {
  DestinationProvenance,
  DocumentKey,
  SessionActivityContext,
  ThreadReadResponse,
} from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import { useStore } from "zustand";
import { ClientProvider } from "../../shell/clientContext";
import * as paneLifetimeModule from "../../shell/paneLifetime";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import {
  browserDraftKey,
  getBrowserDraftRepository,
  resetBrowserDraftRepositoryForTests,
} from "../../stores/browserDraftRepository";
import { connectionStore } from "../../stores/connection";
import {
  type BrowserDocumentAnnotationRepository,
  createDocumentAnnotationRepository,
} from "../../stores/documentAnnotationIndexedDB";
import {
  activityContext,
  activityDelegate,
  activitySummary,
  activityThread,
} from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { acceptedOperation } from "../doc/testing/annotationRepositoryFixtures";
import type { ComposerSourceState } from "../session/composer/sourceState";
import { resetTranscriptViewRegistryForTests } from "../session/transcript/flow/transcriptViewRegistry";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { resetTranscriptPagingForTests } from "../session/transcript/useTranscript";
import { enterAgentCascade, popAgentCascade, returnFromAgentCascade } from "./actions";
import type { SessionZoomParams } from "./intent";
import Zoom from "./Zoom";
import "../doc"; // registers the real "doc" pane so openBeside resolves a doc open
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

function fixture(ancestryKnown = true, sourceType: "session" | "transcript" = "session") {
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
              // A real file tool card (read_file -> descriptor.openBesidePath)
              // and a real agent prose link, both in the session cwd, so each
              // column has BOTH opening producers the provenance task covers.
              {
                id: `${identity}-card`,
                turnId: `${identity}-turn`,
                type: "commandExecution",
                toolName: "read_file",
                argumentsJson: JSON.stringify({ file_path: "/scratch/card.md" }),
                output: "card body",
                status: "completed",
              },
              {
                id: `${identity}-link`,
                turnId: `${identity}-turn`,
                type: "agentMessage",
                text: "See [notes](/scratch/link.md).",
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
    source:
      sourceType === "session"
        ? { type: "session", params: { ref: "root" } }
        : { type: "transcript", params: { ref: "root" } },
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

function column(ref: string): HTMLElement {
  const found = screen.getAllByTestId("cascade-column").find((item) => item.getAttribute("data-scope-ref") === ref);
  if (!found) throw new Error(`Missing ${ref} column`);
  return found;
}

function docPanes(): OpenPaneRecord[] {
  return workspaceStore.getState().panes.filter((pane) => pane.type === "doc");
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
    fake.on("evener/thread/activity/read", ({ ref }) => {
      if (ref === "child" && failure === "missing") throw error;
      return { ...activitySummary(ref), context: context(ref) };
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
  fake.on("evener/thread/activity/read", ({ ref }) => {
    if (ref === "child") throw new WireError("activity unavailable", -32601, { evenerErrorInfo: "methodNotFound" });
    return { ...activitySummary(ref), context: context(ref) };
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
  fake.on("evener/thread/activity/read", ({ ref }) => {
    if (ref === "child" && !healed)
      throw new WireError("connection reset", -32001, { evenerErrorInfo: "sessionUnavailable" });
    return { ...activitySummary(ref), context: context(ref) };
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

// --- Opening provenance (Task 4). Each real column's file card and prose link
// open through the real producer into real workspace document records; the
// source column's opening carries the retained session composer, a child
// column's carries explicit browser-readonly-session. New openings must never
// fall into missing-origin compatibility. ------------------------------------

test("a source-column and a child-column file-card opening each carry explicit origin, never missing-origin", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  const owner = conversationPaneLifetime(currentPane()).composer;
  if (!owner) throw new Error("cascade source lifetime lacks its composer");
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
  act(() => fireEvent.click(within(column("child")).getByRole("button", { name: "Open beside: card.md" })));
  const docs = docPanes();
  expect(docs).toHaveLength(2);
  const rootOpen = docs.find((pane) => (pane.params as { session?: string }).session === "root");
  const childOpen = docs.find((pane) => (pane.params as { session?: string }).session === "child");
  // Source column: the retained composer identity, by its runtime source id.
  expect(rootOpen?.params).toEqual({
    session: "root",
    path: "card.md",
    kind: "file",
    origin: { kind: "browser-origin", paneId: "cascade", compositionOwnerId: owner.sourceId },
  });
  // Child column: an explicit read-only origin for the displayed session.
  expect(childOpen?.params).toEqual({
    session: "child",
    path: "card.md",
    kind: "file",
    origin: { kind: "browser-readonly-session", sourcePaneId: "cascade" },
  });
});

test("a source-column and a child-column prose-link opening each carry the same explicit origin rules", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  const owner = conversationPaneLifetime(currentPane()).composer;
  if (!owner) throw new Error("cascade source lifetime lacks its composer");
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: link.md" })));
  act(() => fireEvent.click(within(column("child")).getByRole("button", { name: "Open beside: link.md" })));
  const docs = docPanes();
  expect(docs).toHaveLength(2);
  const rootOpen = docs.find((pane) => (pane.params as { session?: string }).session === "root");
  const childOpen = docs.find((pane) => (pane.params as { session?: string }).session === "child");
  expect(rootOpen?.params).toEqual({
    session: "root",
    path: "link.md",
    kind: "file",
    origin: { kind: "browser-origin", paneId: "cascade", compositionOwnerId: owner.sourceId },
  });
  expect(childOpen?.params).toEqual({
    session: "child",
    path: "link.md",
    kind: "file",
    origin: { kind: "browser-readonly-session", sourcePaneId: "cascade" },
  });
});

test("re-opening the same file control focuses the same record instead of duplicating it", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
  const first = docPanes()[0];
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
  expect(docPanes()).toHaveLength(1);
  expect(docPanes()[0]).toBe(first);
});

test("the source composer keeps its runtime identity through a cascade entry and Return", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  const owner = conversationPaneLifetime(currentPane()).composer;
  if (!owner) throw new Error("cascade source lifetime lacks its composer");
  act(() =>
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), "cascade"),
  );
  await screen.findByText("grandchild content grandchild-id");
  act(() => popAgentCascade("cascade", "root"));
  await screen.findByText("root content old-root");
  // Entry and Return are the same retained lifetime with the same composer.
  expect(conversationPaneLifetime(currentPane()).composer).toBe(owner);
  // An opening after Return still carries the retained composer identity.
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
  expect(docPanes()[0]?.params).toMatchObject({
    session: "root",
    path: "card.md",
    kind: "file",
    origin: { kind: "browser-origin", paneId: "cascade", compositionOwnerId: owner.sourceId },
  });
});

// A transcript-source Zoom pane owns no composer; every column's opening, the
// source column included, must be explicit browser-readonly-session. There is no
// browser-origin column here at all.
test("a transcript-source Zoom carries browser-readonly-session from every column, never browser-origin", async () => {
  const { fake } = fixture(true, "transcript");
  mount(fake);
  await screen.findByText("root content old-root");
  await screen.findByText("child content child-id");
  const lifetime = conversationPaneLifetime(currentPane());
  expect(lifetime.sourceType).toBe("transcript");
  expect(lifetime.composer).toBeNull();
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
  act(() => fireEvent.click(within(column("child")).getByRole("button", { name: "Open beside: link.md" })));
  const docs = docPanes();
  expect(docs).toHaveLength(2);
  for (const pane of docs) {
    expect((pane.params as { origin?: unknown }).origin).toEqual({
      kind: "browser-readonly-session",
      sourcePaneId: "cascade",
    });
  }
  expect(docs.some((pane) => (pane.params as { origin?: { kind?: string } }).origin?.kind === "browser-origin")).toBe(
    false,
  );
});

// A host unmount/remount must retain the same pane-owned composer and reopen the
// SAME document record (identical destination dedupes), not a second one.
test("a host remount retains the source composer identity and dedupes a repeated opening", async () => {
  const { fake } = fixture();
  const mounted = mount(fake);
  await screen.findByText("root content old-root");
  const owner = conversationPaneLifetime(currentPane()).composer;
  if (!owner) throw new Error("cascade source lifetime lacks its composer");
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
  const first = docPanes()[0];
  mounted.unmount();
  mount(fake);
  await screen.findByText("root content old-root");
  expect(conversationPaneLifetime(currentPane()).composer).toBe(owner);
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
  expect(docPanes()).toHaveLength(1);
  expect(docPanes()[0]).toBe(first);
});

// Return restores the pane's ORIGINAL type (sessionZoom -> its session source)
// while the same lifetime/composer drives the saved destination, and a later
// focus change must not rewrite an already-serialized doc destination.
test("Return to the original pane type retains the composer and a focus change leaves saved destinations unchanged", async () => {
  const { fake } = fixture();
  mount(fake);
  await screen.findByText("root content old-root");
  const owner = conversationPaneLifetime(currentPane()).composer;
  if (!owner) throw new Error("cascade source lifetime lacks its composer");
  act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
  const savedParams = docPanes()[0]?.params;
  act(() => returnFromAgentCascade("cascade"));
  expect(currentPane()).toMatchObject({ id: "cascade", type: "session", params: { ref: "root" } });
  expect(conversationPaneLifetime(currentPane()).composer).toBe(owner);
  // A later focus change must not alter the already-saved doc destination.
  act(() => workspaceStore.setState({ focusedPaneId: null }));
  expect(docPanes()[0]?.params).toEqual(savedParams);
  expect(savedParams).toMatchObject({
    session: "root",
    path: "card.md",
    kind: "file",
    origin: { kind: "browser-origin", paneId: "cascade", compositionOwnerId: owner.sourceId },
  });
});

// The destination a note reconciles against must come from the ACTUAL opening
// action and its real serialized doc record, not a reconstruction. This is the
// journey's first boundary: the annotation pipeline (accepted lease -> save ->
// pending -> Return -> `retainedComposerSourceFor`) can only start from the
// origin the producer captured here. This test now drives that whole connected
// journey end to end; `shell/paneLifetime.test.ts`'s final case is a sibling
// storage-level case, never a substitute for this connected one.
function retainedComposerSourceFor() {
  const candidate = (paneLifetimeModule as Record<string, unknown>).retainedComposerSourceFor;
  if (typeof candidate !== "function") throw new Error("retainedComposerSourceFor is not exported yet");
  return candidate as (paneId: string, sourceId: string, sessionRef: string) => unknown;
}

// The complete reference block `formatAnnotationReference` would build for THIS
// journey, written literally so the expectation never runs the formatter or the
// append helper under test. The capture fields match `acceptedOperation`.
const CONNECTED_REFERENCE =
  'A note on `card.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."\n' +
  'Captured whole-file revision: "rev-A".\n' +
  'Context before: "First occurrence: "; after: "\\n\\nOther context for a second occurrence is nearby: same\\n".\n' +
  "Verify the quotation and context against current content before relying on these captured coordinates.";
const CONNECTED_ORIGINAL_VALUE = {
  text: "Existing /plan draft",
  skillNames: ["plan"],
  mentions: [{ kind: "skill" as const, name: "plan", offset: 9 }],
};
const CONNECTED_EXPECTED_TEXT = `${CONNECTED_ORIGINAL_VALUE.text}\n\n${CONNECTED_REFERENCE}`;

test("an actual file-card opening serializes the destination a note would reconcile against", async () => {
  // Isolated IndexedDB backing for the real annotation pipeline: the source
  // composer, draft repository and annotation adapter share one factory.
  const originalIndexedDB = globalThis.indexedDB;
  globalThis.indexedDB = new IDBFactory();
  resetBrowserDraftRepositoryForTests();
  const { fake } = fixture();
  let mounted = mount(fake);
  // Source handles are captured at CREATION so teardown never creates a lifetime
  // or looks one up through the workspace.
  const ownedSources: ComposerSourceState[] = [];
  let originalSource: ComposerSourceState | null = null;
  let repo: BrowserDocumentAnnotationRepository | undefined;
  let bodyError: unknown;
  let bodyFailed = false;
  try {
    await screen.findByText("root content old-root");
    originalSource = conversationPaneLifetime(currentPane()).composer;
    if (!originalSource) throw new Error("cascade source lifetime lacks its composer");
    const source = originalSource;
    ownedSources.push(source);
    await source.ready();
    source.acceptEdit(CONNECTED_ORIGINAL_VALUE);
    await source.whenSettled();

    act(() => fireEvent.click(within(column("root")).getByRole("button", { name: "Open beside: card.md" })));
    const record = docPanes()[0];
    if (!record) throw new Error("opening produced no doc record");
    const params = record.params as { session: string; path: string; kind: string; origin?: unknown };
    // The real record's session/path/kind identify the document; its origin is the
    // destination identity reconciliation must resolve.
    expect({ session: params.session, path: params.path, kind: params.kind }).toEqual({
      session: "root",
      path: "card.md",
      kind: "file",
    });
    expect(params.origin).toEqual({
      kind: "browser-origin",
      paneId: "cascade",
      compositionOwnerId: source.sourceId,
    });
    // Never substitute a constructed destination when the producer omitted it.
    const destination = params.origin as DestinationProvenance;
    if (!destination) throw new Error("the real opening carried no destination origin");
    const documentKey: DocumentKey = { ...browserDraftKey(params.session), path: params.path };

    // Distinct real child and focused sources with meaningful text; their
    // lifetimes stay alive until cleanup.
    const childPane: OpenPaneRecord = {
      id: "child-conn",
      type: "session",
      params: { ref: "child" },
      slot: "secondary",
    };
    const focusedPane: OpenPaneRecord = {
      id: "focused-conn",
      type: "session",
      params: { ref: "focused" },
      slot: "secondary",
    };
    workspaceStore.setState({
      panes: [...workspaceStore.getState().panes, childPane, focusedPane],
      focusedPaneId: focusedPane.id,
    });
    const childSource = conversationPaneLifetime(childPane).composer;
    const focusedSource = conversationPaneLifetime(focusedPane).composer;
    if (!childSource || !focusedSource) throw new Error("other live sources are missing");
    ownedSources.push(childSource, focusedSource);
    await Promise.all([childSource.ready(), focusedSource.ready()]);
    childSource.editor.write("child draft", 11);
    focusedSource.editor.write("focused draft", 13);
    await Promise.all([childSource.whenSettled(), focusedSource.whenSettled()]);
    const childSnapshot = childSource.getSnapshot();
    const focusedSnapshot = focusedSource.getSnapshot();

    const drafts = getBrowserDraftRepository();
    const childBacking = await drafts.load(browserDraftKey("child"));
    const focusedBacking = await drafts.load(browserDraftKey("focused"));
    const beforeBacking = await drafts.load(browserDraftKey("root"));
    if (!beforeBacking) throw new Error("original backing missing before handoff");
    const originalRevision = beforeBacking.token.sessionRevision;

    repo = createDocumentAnnotationRepository({
      drafts,
      targetFor: (target, key) => {
        if (target.kind !== "browser-origin") return null;
        const resolve = retainedComposerSourceFor();
        return resolve(target.paneId, target.compositionOwnerId, key.sessionRef) as ComposerSourceState | null;
      },
    });

    // Save a real operation for the ACTUAL serialized destination; before any
    // handoff it is exactly one genuinely pending row with the committed receipt.
    const operation = await acceptedOperation(repo, documentKey, "op-connected", "Keep this scope.", destination);
    const receipt = await repo.saveAnnotation(operation);
    const saved = await repo.listAnnotations(documentKey);
    expect(saved).toHaveLength(1);
    expect(saved[0]?.receipt).toEqual(receipt);
    expect(saved[0]?.handoff).toEqual({ state: "pending", reason: "owner-unavailable" });

    // Change focus to the other live source, unmount/remount the host while the
    // workspace records are retained, and click the real Return control.
    act(() => workspaceStore.setState({ focusedPaneId: focusedPane.id }));
    mounted.unmount();
    mounted = mount(fake);
    await screen.findByText("root content old-root");
    act(() => fireEvent.click(screen.getByRole("button", { name: "Return to previous view" })));
    expect(currentPane()).toMatchObject({ id: "cascade", type: "session", params: { ref: "root" } });
    expect(conversationPaneLifetime(currentPane()).composer).toBe(source);
    // The serialized doc destination is asserted against independently captured
    // expected values, not against the same record object it was read from.
    expect(docPanes()[0]?.params).toEqual({
      session: "root",
      path: "card.md",
      kind: "file",
      origin: { kind: "browser-origin", paneId: "cascade", compositionOwnerId: source.sourceId },
    });

    // Reconciliation resolves the original destination through the real retained
    // source resolver and appends the literal reference ONCE.
    const applied = await repo.handoff(documentKey, "op-connected");
    expect(applied).toEqual({ state: "applied", draftRevision: originalRevision + 1 });
    const appliedRevision = (applied as { draftRevision: number }).draftRevision;
    const firstLive = source.getSnapshot();
    expect(firstLive.text).toBe(CONNECTED_EXPECTED_TEXT);
    expect(firstLive.skillNames).toEqual(CONNECTED_ORIGINAL_VALUE.skillNames);
    expect(firstLive.mentions).toEqual(CONNECTED_ORIGINAL_VALUE.mentions);
    const firstBacking = await drafts.load(browserDraftKey("root"));
    expect(firstBacking?.text).toBe(CONNECTED_EXPECTED_TEXT);
    expect(firstBacking?.token.sessionRevision).toBe(originalRevision + 1);
    expect(firstBacking?.skillNames).toEqual(CONNECTED_ORIGINAL_VALUE.skillNames);
    expect(firstBacking?.mentions).toEqual(CONNECTED_ORIGINAL_VALUE.mentions);

    // Every other source is unchanged, live and freshly loaded.
    expect(childSource.getSnapshot()).toEqual(childSnapshot);
    expect(focusedSource.getSnapshot()).toEqual(focusedSnapshot);
    expect(await drafts.load(browserDraftKey("child"))).toEqual(childBacking);
    expect(await drafts.load(browserDraftKey("focused"))).toEqual(focusedBacking);

    // Replay is `already-applied`; every fresh live/durable read is unchanged.
    const replayed = await repo.handoff(documentKey, "op-connected");
    expect(replayed).toEqual({ state: "already-applied", draftRevision: appliedRevision });
    expect(source.getSnapshot()).toEqual(firstLive);
    expect(await drafts.load(browserDraftKey("root"))).toEqual(firstBacking);
    expect(childSource.getSnapshot()).toEqual(childSnapshot);
    expect(focusedSource.getSnapshot()).toEqual(focusedSnapshot);
    expect(await drafts.load(browserDraftKey("child"))).toEqual(childBacking);
    expect(await drafts.load(browserDraftKey("focused"))).toEqual(focusedBacking);
    expect(fake.calls.filter((call) => /send|resume|steer|interrupt/.test(call.method))).toHaveLength(0);
  } catch (error) {
    // Capture, do not rethrow yet: cleanup below must still run.
    bodyError = error;
    bodyFailed = true;
  }
  // Unmount the host FIRST, then attempt to join and dispose EVERY owned source
  // (including the original) while its backing is still installed. Close owned
  // adapters and restore globals even if a step fails, and surface every
  // collected cleanup error instead of swallowing it. No lifetime is created
  // here: the handles were captured at creation.
  const failures: unknown[] = [];
  try {
    mounted.unmount();
  } catch (error) {
    failures.push(error);
  }
  for (const owned of ownedSources) {
    try {
      await owned.whenSettled();
    } catch (error) {
      failures.push(error);
    }
    try {
      owned.dispose();
    } catch (error) {
      failures.push(error);
    }
  }
  try {
    repo?.close();
  } catch (error) {
    failures.push(error);
  }
  try {
    resetBrowserDraftRepositoryForTests();
  } catch (error) {
    failures.push(error);
  }
  globalThis.indexedDB = originalIndexedDB;
  if (bodyFailed && failures.length > 0) {
    throw new AggregateError([bodyError, ...failures], "connected Return body and cleanup failed");
  }
  if (bodyFailed) throw bodyError;
  if (failures.length > 0) throw new AggregateError(failures, "connected Return cleanup failed");
});
