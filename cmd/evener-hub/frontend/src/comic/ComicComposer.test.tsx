import type { Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { subagentOutcomesDelegatesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { Composer } from "../panes/session/composer/Composer";
import { readComposerDraft, writeComposerDraft } from "../panes/session/composer/draft";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { type ComposerSourceState, createComposerSourceState } from "../panes/session/composer/sourceState";
import { editorCursor, replaceEditorText, selectEditorText } from "../panes/session/testing/editor";
import { ClientProvider } from "../shell/clientContext";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { connectionStore } from "../stores/connection";
import { MutationOutboxIndexedDB } from "../stores/mutationOutboxIndexedDB";
import { prefsStore, resetPrefsStoreForTests } from "../stores/prefs";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../stores/threads";
import { ComicApp } from "./ComicApp";
import { ComicComposer } from "./ComicComposer";
import { ComicSession } from "./ComicSession";
import { readComicPosition, writeComicPosition } from "./readerPosition";

const ref = "local:comic-source";
const capabilities = {
  send: true,
  queue: true,
  steer: true,
  interrupt: true,
  skillInput: true,
  commandInput: true,
} as ThreadCapabilities;
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
  resetPendingTurnsStoreForTests();
  resetPrefsStoreForTests();
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
  sessionStorage.clear();
  storage.close();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
function sourceFor(target = ref) {
  const source = createComposerSourceState(target, `test-owner-${sources.length}`);
  sources.push(source);
  return source;
}
async function setup(status = "idle") {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (p) => ({
    thread: {
      id: `thread-${p.ref}`,
      sessionId: `session-${p.ref}`,
      preview: "",
      ephemeral: false,
      modelProvider: "test",
      createdAt: 1,
      updatedAt: 1,
      status: { type: status },
      cwd: "/tmp",
      cliVersion: "test",
      source: "evener",
      turns: [
        {
          id: `turn-${p.ref}`,
          status: "completed",
          itemsView: "full",
          items: [{ id: `item-${p.ref}`, type: "agentMessage", text: `CONTENT_${p.ref}` }],
        },
      ],
      evener: {
        ref: p.ref,
        capabilities,
        ...(status === "notLoaded" ? { resumeRequired: true } : {}),
        mutationStateAuthoritative: true,
        queue: { revision: 0 },
      },
    } as Thread,
  }));
  await act(async () => {
    connectionStore.getState().connect(fake);
    await threadsStore.getState().ensureThread(ref);
  });
  await act(async () => {
    await flushPendingTurnsProjectionForTests();
  });
  return fake;
}
const editor = () => screen.getByRole("textbox", { name: "Message" });
async function settle(source?: ComposerSourceState) {
  await act(async () => {
    await source?.flushRecoveryWrites();
    await flushPendingTurnsProjectionForTests();
    await source?.flushRecoveryWrites();
  });
}

test.each([
  ["idle", "turn/start"],
  ["active", "turn/queue"],
] as const)("%s types and submits literal text through shared durable actions", async (status, method) => {
  await setup(status);
  const source = sourceFor();
  render(<ComicComposer source={source} />);
  connectionStore.setState({ state: "idle" });
  replaceEditorText(editor(), "/settings literal message");
  expect(source.getSnapshot().text).toBe("/settings literal message");
  const submit = screen.getByTestId("composer-submit");
  expect(screen.getByRole("button", { name: submit.textContent ?? "" })).toBe(submit);
  fireEvent.click(screen.getByTestId("composer-submit"));
  await waitFor(async () => expect(await storage.listOutbox(ref)).toHaveLength(1));
  expect(await storage.listOutbox(ref)).toMatchObject([
    {
      method,
      payload: { ref, input: [{ type: "text", text: "/settings literal message" }] },
      clientMutationId: expect.any(String),
    },
  ]);
  await waitFor(() => expect(editor().textContent).toBe(""));
});

test("IME confirmation never submits and keyboard reads the current enterToSend preference", async () => {
  await setup();
  const source = sourceFor();
  render(<ComicComposer source={source} />);
  connectionStore.setState({ state: "idle" });
  replaceEditorText(editor(), "keyboard payload");
  fireEvent.keyDown(editor(), { key: "Enter", isComposing: true });
  await settle(source);
  expect(await storage.listOutbox(ref)).toEqual([]);
  act(() => prefsStore.setState({ enterToSend: false }));
  fireEvent.keyDown(editor(), { key: "Enter" });
  await settle(source);
  expect(await storage.listOutbox(ref)).toEqual([]);
  act(() => prefsStore.setState({ enterToSend: true }));
  fireEvent.keyDown(editor(), { key: "Enter" });
  await waitFor(async () => expect(await storage.listOutbox(ref)).toHaveLength(1));
});

test("Send and Stop are distinct controls with the existing activation delay", async () => {
  const fake = await setup("active");
  fake.on("turn/interrupt", (p) => ({
    receipt: {
      clientMutationId: p.clientMutationId,
      disposition: "applied",
      threadId: `thread-${ref}`,
      projectionState: "reflected",
    },
  }));
  const source = sourceFor();
  source.editText("draft");
  render(<ComicComposer source={source} />);
  const send = screen.getByTestId("composer-submit");
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  replaceEditorText(editor(), "");
  const stop = screen.getByTestId("composer-stop") as HTMLButtonElement;
  expect(stop).not.toBe(send);
  expect(send.isConnected).toBe(false);
  expect(stop.disabled).toBe(true);
  fireEvent.click(stop);
  expect(fake.calls.filter((c) => c.method === "turn/interrupt")).toEqual([]);
  act(() => vi.advanceTimersByTime(499));
  expect(stop.disabled).toBe(true);
  act(() => vi.advanceTimersByTime(1));
  expect(stop.disabled).toBe(false);
  vi.useRealTimers();
  await act(async () => {
    fireEvent.click(stop);
  });
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "turn/interrupt")).toHaveLength(1));
});

test("opening a stopped full-page conversation does not resume and keeps a usable draft", async () => {
  const fake = await setup("notLoaded");
  writeComposerDraft(ref, { text: "saved followup", skillNames: [] });
  render(<ComicSession sessionRef={ref} onReplaceRef={() => true} onSelectRef={() => {}} />);
  await waitFor(() => expect(editor().textContent).toBe("saved followup"));
  expect(fake.calls.filter((c) => c.method === "thread/resume" || c.method === "turn/start")).toEqual([]);
  act(() => connectionStore.setState({ state: "idle" }));
  replaceEditorText(editor(), "offline edit");
  expect(readComposerDraft(ref).text).toBe("offline edit");
});

test("cross-client editing preserves structured recovery", async () => {
  const { Blob: NodeBlob } = await vi.importActual<{ Blob: typeof Blob }>("node:buffer");
  vi.stubGlobal("Blob", NodeBlob);
  const text = "/alpha /build retained [image 1]";
  const input = [
    { type: "text", text },
    { type: "skill", name: "alpha" },
    { type: "command", name: "build" },
    { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
  ];
  const row = await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thread-${ref}`,
    method: "turn/start",
    payload: { ref, input },
    composerText: text,
    attachments: [
      {
        presentationId: "proof-image",
        marker: 1,
        name: "proof.png",
        mediaType: "image/png",
        blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
      },
    ],
    optimisticDisplay: { method: "turn/start", input },
  });
  const recovered = await storage.transferToRecovery(row.clientMutationId, "rejected");
  if (!recovered) throw Error("missing recovery fixture");
  const fake = await setup();
  let source = sourceFor();
  let view = render(
    <ClientProvider client={fake}>
      <Composer ref={ref} focused={false} source={source} />
    </ClientProvider>,
  );
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(row.clientMutationId));
  await settle(source);
  const before = source.getSnapshot();
  expect(before.skillNames).toEqual(["alpha"]);
  expect(before.commandNames).toEqual(["build"]);
  expect(before.mentions?.length).toBe(2);
  const recoveredImageBefore = source.attachments
    .getState()
    .items.map(({ marker, name, mediaType, data }) => ({ marker, name, mediaType, data }));
  const recordBefore = await storage.getRecovery(row.clientMutationId);
  view.unmount();
  source.dispose();
  source = sourceFor();
  view = render(<ComicComposer source={source} />);
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(row.clientMutationId));
  await settle(source);
  const after = source.getSnapshot();
  const recoveredImageAfter = source.attachments
    .getState()
    .items.map(({ marker, name, mediaType, data }) => ({ marker, name, mediaType, data }));
  expect(after.skillNames).toEqual(before.skillNames);
  expect(after.commandNames).toEqual(before.commandNames);
  expect(after.mentions).toEqual(before.mentions);
  expect(after.activeRecoveryId).toBe(before.activeRecoveryId);
  expect(recoveredImageAfter).toEqual(recoveredImageBefore);
  expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();
  const field = editor();
  selectEditorText(field, text.length);
  fireEvent.paste(field, { clipboardData: { getData: () => " edited", types: ["text/plain"], files: [], items: [] } });
  await settle(source);
  expect(source.getSnapshot().text).toBe(`${text} edited`);
  const edited = await storage.getRecovery(row.clientMutationId);
  expect(edited?.compositionRevision).toBeGreaterThan(recordBefore?.compositionRevision ?? 0);
  expect(edited?.compositionOwnerId).toBe(source.compositionOwnerId);
  view.unmount();
  source.dispose();
  source = sourceFor();
  view = render(
    <ClientProvider client={fake}>
      <Composer ref={ref} focused={false} source={source} />
    </ClientProvider>,
  );
  await waitFor(() => expect(editor().textContent).toBe(`${text} edited`));
  await settle(source);
  expect(source.getSnapshot()).toMatchObject({
    skillNames: before.skillNames,
    commandNames: before.commandNames,
    mentions: before.mentions,
    activeRecoveryId: before.activeRecoveryId,
  });
  expect(source.attachments.getState().items).toMatchObject(recoveredImageBefore);
});

test("pending A resume cannot change B route, text or focus", async () => {
  window.history.replaceState(null, "", `/comic/s/${encodeURIComponent(ref)}`);
  const view = render(<ComicApp />);
  const fake = await setup("notLoaded");
  let finish!: (response: ThreadReadResponse) => void;
  fake.on(
    "thread/resume",
    () =>
      new Promise<ThreadReadResponse>((resolve) => {
        finish = resolve;
      }),
  );
  await screen.findByRole("textbox", { name: "Message" });
  replaceEditorText(editor(), "A submitted");
  fireEvent.click(screen.getByTestId("composer-submit"));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/resume")).toHaveLength(1));
  expect(await storage.listOutbox(ref)).toMatchObject([
    { payload: { input: [{ type: "text", text: "A submitted" }] } },
  ]);
  act(() => {
    window.history.pushState(null, "", "/comic/s/local%3AB");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await waitFor(() =>
    expect(view.container.querySelector("[data-comic-composer]")?.getAttribute("data-comic-composer")).toBe("local:B"),
  );
  replaceEditorText(editor(), "B newer draft");
  const bEditor = editor();
  selectEditorText(bEditor, 2, 5);
  const response = await fake.request("thread/read", { ref: "local:A-resumed", includeTurns: true });
  await act(async () => {
    finish(response);
  });
  await waitFor(() => expect(threadsStore.getState().resumedIdentities.get(ref)).toBe("local:A-resumed"));
  expect(window.location.pathname).toBe("/comic/s/local%3AB");
  expect(editor()).toBe(bEditor);
  expect(editor().textContent).toBe("B newer draft");
  expect(document.activeElement).toBe(bEditor);
  expect(editorCursor(bEditor)).toBe(5);
  expect(readComposerDraft("local:B").text).toBe("B newer draft");
  expect(
    fake.calls.filter((c) => c.method === "turn/start" && (c.params as { ref?: string }).ref === "local:B"),
  ).toEqual([]);
});

test("inline children stay readers and full-page child send targets only the canonical child", async () => {
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.hasAttribute("data-index") ? 40 : 1000;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(760);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(1000);
  const fake = await setup();
  const fixture = subagentOutcomesDelegatesResponse();
  const child = fixture.delegates[0];
  if (!child) throw Error("missing child fixture");
  const root = fixture.context.ref;
  fake.on("evener/thread/activity/read", (p) => ({
    context: { ...fixture.context, ref: p.ref },
    scope: "session",
    delegates: { known: true, total: p.ref === root ? 1 : 0, active: 0, failed: 0, completed: 0 },
    jobs: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
    watches: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
  }));
  fake.on("evener/thread/delegates/list", (p) => ({
    ...fixture,
    scope: "session",
    context: { ...fixture.context, ref: p.ref },
    delegates: p.ref === root ? [child] : [],
  }));
  const select = vi.fn();
  const view = render(<ComicSession key={root} sessionRef={root} onSelectRef={select} onReplaceRef={() => true} />);
  await waitFor(() =>
    expect(view.container.querySelector(`[data-comic-child-ref="${child.childRef}"]`)).not.toBeNull(),
  );
  replaceEditorText(editor(), "parent draft retained");
  const parentEditor = editor();
  const header = view.container.querySelector(`[data-comic-child-ref="${child.childRef}"]`) as HTMLElement;
  fireEvent.click(within(header).getByRole("button", { name: /expand/i }));
  await waitFor(() =>
    expect(
      fake.calls.some((c) => c.method === "thread/read" && (c.params as { ref?: string }).ref === child.childRef),
    ).toBe(true),
  );
  expect(screen.getAllByRole("textbox")).toHaveLength(1);
  expect(editor()).toBe(parentEditor);
  expect(view.container.querySelector("[data-comic-composer]")?.getAttribute("data-comic-composer")).toBe(root);
  fireEvent.click(within(header).getByRole("button", { name: /collapse/i }));
  expect(editor()).toBe(parentEditor);
  const position = {
    mode: "away" as const,
    sessionRef: root,
    path: [],
    entryId: JSON.stringify([root, child.delegateId]),
    offset: 0,
    viewportOffset: 24,
  };
  writeComicPosition(root, position);
  fireEvent.click(within(header).getByRole("button", { name: /open conversation/i }));
  expect(select).toHaveBeenCalledWith(child.childRef);
  view.rerender(
    <ComicSession key={child.childRef} sessionRef={child.childRef} onSelectRef={select} onReplaceRef={() => true} />,
  );
  await waitFor(() => expect(editor().textContent).toBe(""));
  act(() => connectionStore.setState({ state: "idle" }));
  replaceEditorText(editor(), "child-only message");
  fireEvent.click(screen.getByTestId("composer-submit"));
  await waitFor(async () => expect(await storage.listOutbox(child.childRef)).toHaveLength(1));
  expect(await storage.listOutbox(child.childRef)).toMatchObject([
    {
      targetRef: child.childRef,
      payload: { ref: child.childRef, input: [{ type: "text", text: "child-only message" }] },
    },
  ]);
  expect(await storage.listOutbox(root)).toEqual([]);
  view.rerender(<ComicSession key={root} sessionRef={root} onSelectRef={select} onReplaceRef={() => true} />);
  await waitFor(() => expect(editor().textContent).toBe("parent draft retained"));
  expect(readComicPosition(root)).toEqual(position);
});

test("background recovery ownership rollover preserves Comic editor selection", async () => {
  await setup();
  const input = [{ type: "text", text: "recovered composition" }];
  const row = await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thread-${ref}`,
    method: "turn/start",
    payload: { ref, input },
    composerText: "recovered composition",
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  const recovery = await storage.transferToRecovery(row.clientMutationId, "rejected");
  if (!recovery) throw Error("missing recovery fixture");
  const source = sourceFor();
  render(<ComicComposer source={source} />);
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(row.clientMutationId));
  await settle(source);
  const peer = new MutationOutboxIndexedDB();
  try {
    const record = await peer.getRecovery(row.clientMutationId);
    if (!record) throw Error("missing owned recovery");
    const resent = await peer.resendRecovery(
      record.clientMutationId,
      {
        targetRef: ref,
        threadId: `thread-${ref}`,
        method: record.method,
        payload: record.payload,
        attachments: record.attachments,
        optimisticDisplay: record.optimisticDisplay,
      },
      undefined,
      { [record.clientMutationId]: record.compositionRevision ?? 0 },
    );
    expect(resent).toBeDefined();
    replaceEditorText(editor(), "newer composition");
    selectEditorText(editor(), 2, 6);
    await settle(source);
    const newId = source.getSnapshot().activeRecoveryId;
    expect(newId).not.toBeNull();
    expect(newId).not.toBe(row.clientMutationId);
    if (!newId) throw Error("preservation did not establish ownership");
    expect(await storage.getRecovery(newId)).toMatchObject({ composerText: "newer composition" });
    expect(editor().textContent).toBe("newer composition");
    expect(editorCursor(editor())).toBe(6);
    expect(document.getSelection()?.toString()).toBe("wer ");
  } finally {
    peer.close();
  }
});

test("explicit recovery Edit restores caret and focus only in the mounted composer", async () => {
  await setup();
  writeComposerDraft(ref, { text: "current", skillNames: [] });
  const input = [{ type: "text", text: "recovered" }];
  const row = await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thread-${ref}`,
    method: "turn/start",
    payload: { ref, input },
    composerText: "recovered",
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(row.clientMutationId, "rejected");
  const source = sourceFor();
  render(<ComicComposer source={source} />);
  await settle(source);
  expect(editor().textContent).toBe("current");
  fireEvent.click(await screen.findByRole("button", { name: "Edit message" }));
  await waitFor(() => expect(editor().textContent).toBe("current\n\nrecovered"));
  expect(document.activeElement).toBe(editor());
  expect(editorCursor(editor())).toBe("current\n\nrecovered".length);
});
