import type { Thread, ThreadCapabilities } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { askDockStore, resetAskDockStoreForTests } from "../panes/session/composer/askDock/askDockStore";
import { ackAskUserCall } from "../panes/session/composer/askDock/askDockTestUtils";
import { resetComposerFocusStoreForTests } from "../panes/session/composer/composerFocus";
import { readComposerDraft, writeComposerDraft } from "../panes/session/composer/draft";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { type ComposerSourceState, createComposerSourceState } from "../panes/session/composer/sourceState";
import { replaceEditorText, selectEditorText } from "../panes/session/testing/editor";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { connectionStore } from "../stores/connection";
import { MutationOutboxIndexedDB } from "../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../stores/threads";
import { ComicComposer } from "./ComicComposer";
import { ComicInteractions } from "./ComicInteractions";
import { ComicSession } from "./ComicSession";

const firstRef = "local:interaction-A";
const secondRef = "local:interaction-B";
const resolveMethod = "evener/sandbox/escalation/resolve";
let storage: MutationOutboxIndexedDB;
let sources: ComposerSourceState[];
beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  installLocalStorage(new MemoryStorage());
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
  resetComposerFocusStoreForTests();
  resetPendingTurnsStoreForTests();
  storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  sources = [];
});
afterEach(async () => {
  await act(async () => {
    await flushPendingTurnsProjectionForTests();
  });
  cleanup();
  for (const source of sources) source.dispose();
  resetThreadsStoreForTests();
  storage.close();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
async function setup() {
  const fake = new FakeClient("ready");
  fake.on("thread/read", ({ ref }) => ({
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 1 },
    thread: {
      id: `thr_${ref}`,
      sessionId: `sess_${ref}`,
      preview: "",
      ephemeral: false,
      modelProvider: "test",
      createdAt: 1,
      updatedAt: 1,
      status: { type: "idle" },
      cwd: "/tmp",
      cliVersion: "test",
      source: "evener",
      turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
      evener: {
        ref,
        capabilities: { send: true, queue: true, interrupt: true } as ThreadCapabilities,
        mutationStateAuthoritative: true,
        queue: { revision: 0 },
      },
    } as Thread,
  }));
  await act(async () => {
    connectionStore.getState().connect(fake);
    await threadsStore.getState().ensureThread(firstRef);
    await threadsStore.getState().ensureThread(secondRef);
    await flushPendingTurnsProjectionForTests();
  });
  return fake;
}
function page(ref: string) {
  return <ComicSession sessionRef={ref} onSelectRef={() => {}} onReplaceRef={() => true} />;
}
const editor = () => screen.getByRole("textbox", { name: "Message" });
function approval(fake: FakeClient, ref = firstRef) {
  act(() =>
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: {
        ref,
        threadId: `thr_${ref}`,
        escalationId: "request-1",
        mode: "workspace-write",
        tool: "write_file",
        kind: "file",
        deniedPath: "/test-owned/outside/proof.txt",
      },
    }),
  );
}
function ask(fake: FakeClient) {
  act(() =>
    ackAskUserCall(fake, firstRef, "turn_1", "ask-item", "ask-call", [
      {
        header: "SINGLE_FIXTURE",
        question: "ONE_FIXTURE",
        options: [
          { label: "ALPHA", detail: "" },
          { label: "BETA", detail: "" },
        ],
      },
      {
        header: "MULTI_FIXTURE",
        question: "MANY_FIXTURE",
        multi_select: true,
        options: [
          { label: "RED", detail: "" },
          { label: "BLUE", detail: "" },
        ],
      },
      { header: "TEXT_FIXTURE", question: "TEXT_QUESTION", options: [{ label: "OTHER_FIXTURE", detail: "" }] },
    ]),
  );
}

test("actual batches alone make ordinary input inert and preserve structured drafts", async () => {
  const fake = await setup();
  writeComposerDraft(firstRef, {
    text: "/alpha /build retained",
    skillNames: ["alpha"],
    commandNames: ["build"],
    mentions: [
      { kind: "skill", name: "alpha", offset: 0 },
      { kind: "command", name: "build", offset: 7 },
    ],
  });
  const view = render(page(firstRef));
  await screen.findByRole("textbox", { name: "Message" });
  act(() =>
    fake.emitNotification({
      method: "thread/status/changed",
      params: { ref: firstRef, threadId: `thr_${firstRef}`, status: { type: "awaiting" }, askPending: true },
    }),
  );
  expect(editor().closest("[inert]")).toBeNull();
  ask(fake);
  expect(screen.getAllByRole("tab")).toHaveLength(3);
  expect(view.container.querySelector("[data-comic-composer] form")?.hasAttribute("inert")).toBe(true);
  expect(view.container.querySelector("[data-comic-composer] form")?.hasAttribute("hidden")).toBe(true);
  fireEvent.submit(view.container.querySelector("[data-comic-composer] form")!);
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/queue")).toEqual([]);
  await act(async () => {
    expect(await storage.listOutbox(firstRef)).toEqual([]);
  });
  fireEvent.click(screen.getByRole("radio", { name: /ALPHA/ }));
  fireEvent.click(screen.getByRole("checkbox", { name: /RED/ }));
  fireEvent.click(screen.getByRole("checkbox", { name: /BLUE/ }));
  fireEvent.click(screen.getByRole("button", { name: /next question/i }));
  fireEvent.click(screen.getByRole("radio", { name: /something else/i }));
  const free = screen.getByRole("textbox", { name: /something else/i });
  fireEvent.change(free, { target: { value: "FREE_ANSWER" } });
  expect(screen.getByTestId("ask-dock-count").dataset.answered).toBe("3");
  view.unmount();
  render(page(firstRef));
  expect((screen.getByRole("textbox", { name: /something else/i }) as HTMLInputElement).value).toBe("FREE_ANSWER");
  act(() => connectionStore.setState({ state: "idle" }));
  fireEvent.click(screen.getByRole("button", { name: /send answers/i }));
  fireEvent.click(screen.getByRole("button", { name: /send answers/i }));
  await waitFor(async () => expect(await storage.listOutbox(firstRef)).toHaveLength(1));
  const rows = await storage.listOutbox(firstRef);
  expect(JSON.stringify(rows[0]?.payload)).toContain("FREE_ANSWER");
  expect(JSON.stringify(rows[0]?.payload)).toContain("ALPHA");
  expect(JSON.stringify(rows[0]?.payload)).toContain("RED");
  expect(JSON.stringify(rows[0]?.payload)).toContain("BLUE");
  await waitFor(() => expect(screen.queryByRole("tab")).toBeNull());
  expect(editor().textContent).toBe("/alpha /build retained");
  expect(readComposerDraft(firstRef)).toEqual({
    text: "/alpha /build retained",
    skillNames: ["alpha"],
    commandNames: ["build"],
    mentions: [
      { kind: "skill", name: "alpha", offset: 0 },
      { kind: "command", name: "build", offset: 7 },
    ],
  });
  await waitFor(() => expect(document.activeElement).toBe(editor()));
});

test.each([true, false])("explicit approval choice %s recovers after failure without replay", async (approve) => {
  const fake = await setup();
  let reject!: (error: Error) => void;
  fake.on(
    resolveMethod,
    () =>
      new Promise((_, fail) => {
        reject = fail;
      }),
  );
  render(page(firstRef));
  await screen.findByRole("textbox", { name: "Message" });
  approval(fake);
  const group = screen.getByRole("group", { name: /sandbox approval/i });
  expect(group.textContent).toContain("/test-owned/outside/proof.txt");
  expect(group.textContent).toContain("workspace-write");
  expect(group.textContent).toContain("write_file");
  expect(fake.calls.filter((c) => c.method === resolveMethod)).toHaveLength(0);
  const button = within(group).getByRole("button", { name: approve ? /allow/i : /deny/i }) as HTMLButtonElement;
  fireEvent.click(button);
  expect(button.disabled).toBe(true);
  fireEvent.click(button);
  await waitFor(() => expect(reject).toBeTypeOf("function"));
  await act(async () => reject(new Error("APPROVAL_RETRY_FIXTURE")));
  expect(button.disabled).toBe(false);
  expect(group.textContent).toContain("APPROVAL_RETRY_FIXTURE");
  fake.on(resolveMethod, () => ({}));
  fireEvent.click(button);
  await waitFor(() => expect(screen.queryByRole("group", { name: /sandbox approval/i })).toBeNull());
  expect(fake.calls.filter((c) => c.method === resolveMethod).map((c) => c.params)).toEqual([
    { ref: firstRef, escalationId: "request-1", approve },
    { ref: firstRef, escalationId: "request-1", approve },
  ]);
  expect(await storage.listOutbox(firstRef)).toEqual([]);
  expect(editor().closest("[inert]")).toBeNull();
});

test("late approval stays attached to the original ref", async () => {
  const fake = await setup();
  let finish!: (result: object) => void;
  const resolvedRefs: string[] = [];
  fake.on(resolveMethod, (params) => {
    resolvedRefs.push(params.ref!);
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  const firstSource = createComposerSourceState(firstRef, "first-seat");
  const secondSource = createComposerSourceState(secondRef, "second-seat");
  sources.push(firstSource, secondSource);
  const surface = (source: ComposerSourceState) => (
    <>
      <ComicInteractions sessionRef={source.ref} />
      <ComicComposer key={source.ref} source={source} />
    </>
  );
  const view = render(surface(firstSource));
  approval(fake);
  fireEvent.click(screen.getByRole("button", { name: /allow/i }));
  approval(fake, secondRef);
  view.rerender(surface(secondSource));
  await waitFor(() =>
    expect(view.container.querySelector("[data-comic-composer]")?.getAttribute("data-comic-composer")).toBe(secondRef),
  );
  replaceEditorText(editor(), "B_DRAFT");
  selectEditorText(editor(), 1, 3);
  await act(async () => {
    await flushPendingTurnsProjectionForTests();
  });
  const secondBefore = secondSource.getSnapshot();
  const secondEditor = editor();
  const allow = screen.getByRole("button", { name: /allow/i }) as HTMLButtonElement;
  expect(allow.disabled).toBe(false);
  await act(async () => finish({}));
  expect(resolvedRefs).toEqual([firstRef]);
  expect(secondSource.getSnapshot()).toEqual(secondBefore);
  const replayedApprovalRequests = fake.calls.filter((c) => c.method === resolveMethod).slice(1);
  expect(replayedApprovalRequests).toHaveLength(0);
  expect(fake.calls.find((c) => c.method === resolveMethod)?.params).toEqual({
    ref: firstRef,
    escalationId: "request-1",
    approve: true,
  });
  expect(editor()).toBe(secondEditor);
  expect(document.activeElement).toBe(secondEditor);
  expect(document.getSelection()?.toString()).toBe("_D");
  expect(allow.disabled).toBe(false);
  expect(threadsStore.getState().threads.get(secondRef)?.pendingEscalations).toHaveLength(1);
  expect(askDockStore.getState().byRef.get(secondRef)?.batches ?? []).toEqual([]);
});

test("answer persistence failure keeps the batch editable for explicit retry", async () => {
  const fake = await setup();
  render(page(firstRef));
  await screen.findByRole("textbox", { name: "Message" });
  act(() => ackAskUserCall(fake, firstRef, "turn_1", "one", "call-one"));
  fireEvent.click(screen.getByRole("radio", { name: "Yes" }));
  const before = askDockStore.getState().byRef.get(firstRef)?.answers;
  const original = IDBObjectStore.prototype.add;
  const failure = vi.spyOn(IDBObjectStore.prototype, "add").mockImplementation(function (
    this: IDBObjectStore,
    ...args
  ) {
    if (this.name === "outbox") throw new DOMException("QUOTA_FIXTURE", "QuotaExceededError");
    return original.apply(this, args);
  });
  fireEvent.click(screen.getByRole("button", { name: /send answers/i }));
  await waitFor(() =>
    expect((screen.getByRole("button", { name: /send answers/i }) as HTMLButtonElement).disabled).toBe(false),
  );
  expect(failure).toHaveBeenCalled();
  expect(askDockStore.getState().byRef.get(firstRef)?.answers).toEqual(before);
  expect(await storage.listOutbox(firstRef)).toEqual([]);
  failure.mockRestore();
  act(() => connectionStore.setState({ state: "idle" }));
  fireEvent.click(screen.getByRole("button", { name: /send answers/i }));
  await waitFor(async () => expect(await storage.listOutbox(firstRef)).toHaveLength(1));
  await waitFor(() => expect(editor().closest("[inert]")).toBeNull());
});

test("a settled external batch disappears without submitting or changing the draft", async () => {
  const fake = await setup();
  render(page(firstRef));
  await screen.findByRole("textbox", { name: "Message" });
  replaceEditorText(editor(), "RETAINED_ON_EXTERNAL_SETTLEMENT");
  ask(fake);
  expect(screen.getAllByRole("tab")).toHaveLength(3);
  act(() =>
    fake.emitNotification({
      method: "thread/status/changed",
      params: {
        ref: firstRef,
        threadId: `thr_${firstRef}`,
        status: { type: "idle" },
        askPending: false,
      },
    }),
  );
  expect(screen.queryByRole("tab")).toBeNull();
  expect(editor().textContent).toBe("RETAINED_ON_EXTERNAL_SETTLEMENT");
  await act(async () => {
    expect(await storage.listOutbox(firstRef)).toEqual([]);
  });
});

test("late answer receipt cannot focus or change the second source", async () => {
  const fake = await setup();
  let finish!: () => void;
  fake.on(
    "turn/start",
    (params) =>
      new Promise((resolve) => {
        finish = () =>
          resolve({
            turn: { id: "answer-turn", status: "completed", items: [], itemsView: "full" },
            receipt: {
              clientMutationId: params.clientMutationId,
              disposition: "applied",
              threadId: `thr_${firstRef}`,
              projectionState: "reflected",
            },
          });
      }),
  );
  const firstSource = createComposerSourceState(firstRef, "answer-first");
  const secondSource = createComposerSourceState(secondRef, "answer-second");
  sources.push(firstSource, secondSource);
  const surface = (source: ComposerSourceState) => (
    <>
      <ComicInteractions sessionRef={source.ref} />
      <ComicComposer key={source.ref} source={source} />
    </>
  );
  const view = render(surface(firstSource));
  act(() => ackAskUserCall(fake, firstRef, "turn_1", "one", "call-one"));
  fireEvent.click(screen.getByRole("radio", { name: "Yes" }));
  fireEvent.click(screen.getByRole("button", { name: /send answers/i }));
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  view.rerender(surface(secondSource));
  replaceEditorText(editor(), "SECOND_ANSWER_DRAFT");
  selectEditorText(editor(), 1, 4);
  await act(async () => {
    await flushPendingTurnsProjectionForTests();
  });
  const secondBefore = secondSource.getSnapshot();
  const secondEditor = editor();
  await act(async () => finish());
  await waitFor(async () => expect(await storage.listOutbox(firstRef)).toEqual([]));
  expect(secondSource.getSnapshot()).toEqual(secondBefore);
  expect(document.activeElement).toBe(secondEditor);
  expect(document.getSelection()?.toString()).toBe("ECO");
  expect(fake.calls.filter((c) => c.method === "turn/start").map((c) => (c.params as { ref: string }).ref)).toEqual([
    firstRef,
  ]);
});
