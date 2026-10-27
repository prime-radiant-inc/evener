import type { Thread, ThreadCapabilities } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { readComicPosition, writeComicPosition } from "../../comic/readerPosition";
import { chromeStore, resetChromeStoreForTests } from "../../shell/chromeStore";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import { ackAskUserCall } from "./composer/askDock/askDockTestUtils";
import { requestComposerFocus, resetComposerFocusStoreForTests } from "./composer/composerFocus";
import { readComposerDraft, writeComposerDraft } from "./composer/draft";
import { resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./composer/queue/testing/flushPendingTurnsProjection";
import { requestQuoteInsert, resetQuoteInsertStoreForTests, useQuoteInsertRequest } from "./composer/quoteInsert";
import { CommittedSession } from "./testing/CommittedSession";
import { editorCursor, replaceEditorText, selectEditorText } from "./testing/editor";
import "./testing/editorGeometry";
import "./index";
import { captureTranscriptView } from "./transcript/flow/transcriptViewRegistry";
import { installTranscriptGeometry } from "./transcript/transcriptReadingGeometryTestUtils";
import { retainedTranscriptReadView } from "./transcript/transcriptReadView";

const ref = "local:pane-comic";
const paneId = "comic-pane";
let storage: MutationOutboxIndexedDB;
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
  sessionStorage.clear();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetComposerFocusStoreForTests();
  resetQuoteInsertStoreForTests();
  resetTranscriptDisplayStoreForTests();
  storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  window.history.replaceState(null, "", `/s/${encodeURIComponent(ref)}`);
});
afterEach(async () => {
  await flushPendingTurnsProjectionForTests();
  cleanup();
  resetChromeStoreForTests();
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  storage.close();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
async function setup(status = "idle") {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (p) => ({
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 1 },
    thread: {
      id: `thr_${p.ref}`,
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
          id: "turn-1",
          status: "completed",
          itemsView: "full",
          items: [
            { id: "item-1", type: "agentMessage", text: "PANE_MESSAGE_ONE" },
            { id: "item-2", type: "agentMessage", text: "PANE_MESSAGE_TWO" },
          ],
        },
      ],
      evener: {
        ref: p.ref,
        capabilities: {
          send: true,
          queue: true,
          steer: true,
          interrupt: true,
          skillInput: true,
          commandInput: true,
        } as ThreadCapabilities,
        mutationStateAuthoritative: true,
        queue: { revision: 0 },
        ...(status === "notLoaded" ? { resumeRequired: true } : {}),
      },
    } as Thread,
  }));
  await act(async () => {
    connectionStore.getState().connect(fake);
    await threadsStore.getState().ensureThread(ref);
  });
  await flushPendingTurnsProjectionForTests();
  return fake;
}
async function mount(fake: FakeClient) {
  let view!: ReturnType<typeof render>;
  await act(async () => {
    view = render(
      <ClientProvider client={fake}>
        <CommittedSession paneId={paneId} params={{ ref }} focused />
      </ClientProvider>,
    );
  });
  return view;
}
function lifetime() {
  const pane = workspaceStore.getState().panes.find((p) => p.id === paneId);
  if (!pane) throw Error("missing pane");
  return conversationPaneLifetime(pane);
}
async function presentation(value: "standard" | "comic") {
  await act(async () =>
    transcriptDisplayStore.getState().setPresentation(transcriptDisplayStore.getState().viewport, value),
  );
}
function preservedDraft() {
  const value = lifetime().composer?.getSnapshot();
  if (!value) throw Error("missing composer");
  const { text, skillNames, commandNames, mentions, activeRecoveryId } = value;
  return { text, skillNames, commandNames, mentions, activeRecoveryId };
}
const editor = () => screen.getByRole("textbox", { name: "Message" });

test.each([
  ["comic", "prefix"],
  ["comic", "append"],
  ["standard", "prefix"],
] as const)("%s consumes %s insertion once across Standard handoff", async (initialPresentation, placement) => {
  const original = "/alpha /build kept";
  writeComposerDraft(ref, {
    text: original,
    skillNames: ["alpha"],
    commandNames: ["build"],
    mentions: [
      { kind: "skill", name: "alpha", offset: 0 },
      { kind: "command", name: "build", offset: 7 },
    ],
  });
  const fake = await setup();
  await presentation(initialPresentation);
  await mount(fake);
  const source = lifetime().composer!;
  const pending = renderHook(() => useQuoteInsertRequest(ref, paneId));
  const inserted = placement === "prefix" ? "/review " : "> quoted line\n\n";
  const expected = placement === "prefix" ? inserted + original : `${original}\n\n${inserted}`;
  selectEditorText(editor(), original.length);
  act(() => {
    requestQuoteInsert(ref, inserted, placement, paneId);
    requestComposerFocus(ref, paneId);
  });
  await waitFor(() => expect(editor().textContent).toBe(expected));
  expect(document.activeElement).toBe(editor());
  expect(editorCursor(editor())).toBe(placement === "prefix" ? inserted.length : expected.length);
  expect(pending.result.current).toBeUndefined();
  const offset = placement === "prefix" ? inserted.length : 0;
  expect(source.getSnapshot()).toMatchObject({
    text: expected,
    skillNames: ["alpha"],
    commandNames: ["build"],
    mentions: [
      { kind: "skill", name: "alpha", offset },
      { kind: "command", name: "build", offset: offset + 7 },
    ],
  });
  expect(within(editor()).getAllByTestId("composer-skill-chip")).toHaveLength(1);
  const before = preservedDraft();
  await presentation("standard");
  expect(lifetime().composer).toBe(source);
  expect(editor().textContent).toBe(expected);
  expect(preservedDraft()).toEqual(before);
  await presentation("comic");
  expect(editor().textContent).toBe(expected);
  act(() => requestQuoteInsert(ref, inserted, placement, paneId));
  const twice = placement === "prefix" ? inserted + expected : expected + inserted;
  await waitFor(() => expect(editor().textContent).toBe(twice));
  expect(pending.result.current).toBeUndefined();
});

test("Comic routes focus only to the addressed of two retained seats for one conversation", async () => {
  const fake = await setup();
  await presentation("comic");
  await act(async () =>
    render(
      <ClientProvider client={fake}>
        <div data-testid="seat-a">
          <CommittedSession paneId={paneId} params={{ ref }} focused={false} />
        </div>
        <div data-testid="seat-b">
          <CommittedSession paneId="comic-second-seat" params={{ ref }} focused={false} />
        </div>
      </ClientProvider>,
    ),
  );
  const first = within(screen.getByTestId("seat-a")).getByRole("textbox", { name: "Message" });
  const second = within(screen.getByTestId("seat-b")).getByRole("textbox", { name: "Message" });
  selectEditorText(second, 0);
  act(() => requestComposerFocus(ref, paneId));
  expect(document.activeElement).toBe(first);
  act(() => requestComposerFocus(ref, "comic-second-seat"));
  expect(document.activeElement).toBe(second);
});

test("Comic consumes only the addressed insertion with two retained seats", async () => {
  const fake = await setup();
  await presentation("comic");
  await act(async () =>
    render(
      <ClientProvider client={fake}>
        <div data-testid="seat-a">
          <CommittedSession paneId={paneId} params={{ ref }} focused={false} />
        </div>
        <div data-testid="seat-b">
          <CommittedSession paneId="comic-second-seat" params={{ ref }} focused={false} />
        </div>
      </ClientProvider>,
    ),
  );
  const first = within(screen.getByTestId("seat-a")).getByRole("textbox", { name: "Message" });
  const second = within(screen.getByTestId("seat-b")).getByRole("textbox", { name: "Message" });
  replaceEditorText(first, "first draft");
  replaceEditorText(second, "second draft");
  const pending = renderHook(() => useQuoteInsertRequest(ref));
  act(() => requestQuoteInsert(ref, "not ours", "prefix", "absent-seat"));
  expect(first.textContent).toBe("first draft");
  expect(second.textContent).toBe("second draft");
  expect(pending.result.current?.target).toBe("absent-seat");
  act(() => requestQuoteInsert(ref, "/review ", "prefix", paneId));
  await waitFor(() => expect(first.textContent).toBe("/review first draft"));
  expect(second.textContent).toBe("second draft");
  expect(document.activeElement).toBe(first);
  expect(editorCursor(first)).toBe(8);
  expect(pending.result.current).toBeUndefined();
});

test.each([false, true])("Comic Appearance keeps controls and owners with mobile=%s", async (mobile) => {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: mobile && query === "(max-width: 899px)",
    media: query,
    addEventListener() {},
    removeEventListener() {},
  }));
  const fake = await setup();
  await mount(fake);
  await presentation("comic");
  const owner = lifetime();
  const composer = owner.composer;
  const reads = fake.calls.filter((c) => c.method === "thread/read").length;
  const trigger = screen.getByRole("button", { name: "Appearance" });
  expect(screen.queryByRole("combobox", { name: "View" })).toBeNull();
  expect(screen.queryByRole("combobox", { name: "Scene" })).toBeNull();
  trigger.focus();
  fireEvent.click(trigger);
  const popup = screen.getByRole("dialog", { name: "Appearance" });
  expect(within(popup).getAllByRole("combobox")).toHaveLength(3);
  const readerPort = document.querySelector("[data-comic-scroll]")?.firstElementChild;
  if (!readerPort) throw Error("missing reader port");
  fireEvent.scroll(readerPort);
  expect(screen.queryByRole("dialog", { name: "Appearance" })).toBe(popup);
  const scene = within(popup).getByRole("combobox", { name: "Scene" });
  expect(
    within(scene)
      .getAllByRole("option")
      .map((o) => (o as HTMLOptionElement).value),
  ).toEqual(["room", "field", "white"]);
  fireEvent.change(scene, { target: { value: "field" } });
  const expression = within(popup).getByRole("combobox", { name: "Human expression" });
  expect(
    within(expression)
      .getAllByRole("option")
      .map((o) => (o as HTMLOptionElement).value),
  ).toEqual(["neutral", "happy-wave", "angry", "scared", "point", "celebrate"]);
  fireEvent.change(expression, { target: { value: "celebrate" } });
  fireEvent.keyDown(expression, { key: "Escape" });
  expect(screen.queryByRole("dialog", { name: "Appearance" })).toBeNull();
  await waitFor(() => expect(document.activeElement).toBe(trigger));
  fireEvent.click(trigger);
  expect((screen.getByRole("combobox", { name: "Scene" }) as HTMLSelectElement).value).toBe("field");
  expect((screen.getByRole("combobox", { name: "Human expression" }) as HTMLSelectElement).value).toBe("celebrate");
  fireEvent.change(screen.getByRole("combobox", { name: "View" }), { target: { value: "plain" } });
  expect(document.querySelector("[data-comic-mode='plain']")).toBeTruthy();
  fireEvent.change(screen.getByRole("combobox", { name: "View" }), { target: { value: "comic" } });
  expect((screen.getByRole("combobox", { name: "Scene" }) as HTMLSelectElement).value).toBe("field");
  fireEvent.mouseDown(document.body);
  expect(screen.queryByRole("dialog", { name: "Appearance" })).toBeNull();
  fireEvent.click(trigger);
  expect(screen.queryByRole("dialog", { name: "Appearance" })).not.toBeNull();
  const view = trigger.ownerDocument.defaultView;
  if (!view) throw Error("missing owner window");
  fireEvent.resize(view);
  expect(screen.queryByRole("dialog", { name: "Appearance" })).toBeNull();
  await waitFor(() => expect(document.activeElement).toBe(trigger));
  fireEvent.click(trigger);
  expect(screen.queryByRole("dialog", { name: "Appearance" })).not.toBeNull();
  fireEvent.scroll(document.body);
  expect(screen.queryByRole("dialog", { name: "Appearance" })).toBeNull();
  expect(lifetime()).toBe(owner);
  expect(lifetime().composer).toBe(composer);
  expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(reads);
  expect(screen.getByRole("button", { name: "Jump to latest" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Send" })).toBeTruthy();
});

test("open Comic Appearance follows its pane into a popout and back", async () => {
  const fake = await setup();
  const mounted = await mount(fake);
  await presentation("comic");
  const owner = lifetime();
  const composer = owner.composer;
  const reads = fake.calls.filter((c) => c.method === "thread/read").length;
  const trigger = screen.getByRole("button", { name: "Appearance" });
  const frame = document.createElement("iframe");
  document.body.append(frame);
  const popout = frame.contentDocument;
  if (!popout?.defaultView) throw Error("missing popout window");
  try {
    for (const destination of [popout, document]) {
      const previousWindow = trigger.ownerDocument.defaultView;
      const destinationWindow = destination.defaultView;
      if (!previousWindow || !destinationWindow) throw Error("missing pane window");
      trigger.focus();
      fireEvent.click(trigger);
      expect(within(trigger.ownerDocument.body).queryByRole("dialog", { name: "Appearance" })).not.toBeNull();
      await act(async () => {
        destination.body.append(mounted.container);
        chromeStore.getState().setPaneDocumentsFor([paneId], destination);
      });
      const popup = await within(destination.body).findByRole("dialog", { name: "Appearance" });
      fireEvent.resize(previousWindow);
      expect(within(destination.body).queryByRole("dialog", { name: "Appearance" })).toBe(popup);
      fireEvent.resize(destinationWindow);
      expect(within(destination.body).queryByRole("dialog", { name: "Appearance" })).toBeNull();
      await waitFor(() => expect(destination.activeElement).toBe(trigger));
      fireEvent.click(trigger);
      const reopened = within(destination.body).getByRole("dialog", { name: "Appearance" });
      const readerPort = mounted.container.querySelector("[data-comic-scroll]")?.firstElementChild;
      if (!readerPort) throw Error("missing reader port");
      fireEvent.scroll(readerPort);
      expect(within(destination.body).queryByRole("dialog", { name: "Appearance" })).toBe(reopened);
      fireEvent.scroll(destination.body);
      expect(within(destination.body).queryByRole("dialog", { name: "Appearance" })).toBeNull();
      expect(lifetime()).toBe(owner);
      expect(lifetime().composer).toBe(composer);
      expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(reads);
    }
  } finally {
    await act(async () => {
      document.body.append(mounted.container);
      chromeStore.getState().setPaneDocumentsFor([paneId], document);
    });
    frame.remove();
  }
});

test.each([false, true])("Comic keeps one menu with mobile=%s", async (mobile) => {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: mobile && query === "(max-width: 899px)",
    media: query,
    addEventListener() {},
    removeEventListener() {},
  }));
  const fake = await setup("active");
  act(() => {
    const model = threadsStore.getState().threads.get(ref)!;
    threadsStore.setState({ threads: new Map([[ref, { ...model, lastFrameAt: Date.now() - 300_000 }]]) });
  });
  await mount(fake);
  await presentation("comic");
  const menus = screen.getAllByRole("button", { name: "Session actions" });
  expect(menus).toHaveLength(1);
  expect(!!menus[0]?.closest("[data-comic-composer]")).toBe(mobile);
  const statuses = screen.getAllByTestId("liveness-line");
  expect(statuses).toHaveLength(1);
  expect(statuses[0]?.textContent).toBe("Quiet ~5m");
  expect(!!statuses[0]?.closest("[data-comic-composer]")).toBe(mobile);
  expect(screen.getByRole("button", { name: "Jump to latest" }).textContent).toBe(mobile ? "Latest" : "Jump to latest");
  await waitFor(() =>
    expect(screen.getByLabelText("Comic font unavailable").textContent).toBe(
      mobile
        ? "Comic Sans unavailable, using fallback font."
        : "Comic Sans could not load. The conversation remains available in the fallback font.",
    ),
  );
  fireEvent.click(menus[0]!);
  expect(screen.getByRole("menuitem", { name: "Verbosity…" })).toBeTruthy();
});

test.each(["success", "retry"])("an in-flight approval survives presentation handoff: %s", async (outcome) => {
  const fake = await setup();
  let finish!: (value: object) => void;
  let fail!: (error: Error) => void;
  fake.on(
    "evener/sandbox/escalation/resolve",
    () =>
      new Promise((resolve, reject) => {
        finish = resolve;
        fail = reject;
      }),
  );
  await mount(fake);
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
        deniedPath: "/outside/proof.txt",
      },
    }),
  );
  const approve = () =>
    within(screen.getByRole("group", { name: /sandbox approval/i })).getByRole("button", {
      name: /allow/i,
    }) as HTMLButtonElement;
  fireEvent.click(approve());
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  await presentation("comic");
  expect(approve().disabled).toBe(true);
  const group = screen.getByRole("group", { name: /sandbox approval/i });
  const deny = within(group).getByRole("button", { name: /deny/i }) as HTMLButtonElement;
  expect(deny.disabled).toBe(true);
  fireEvent.click(approve());
  fireEvent.click(deny);
  fireEvent.keyDown(group, { key: "Enter", ctrlKey: true });
  expect(fake.calls.filter((c) => c.method === "evener/sandbox/escalation/resolve")).toHaveLength(1);
  if (outcome === "retry") {
    await act(async () => fail(new Error("APPROVAL_HANDOFF_FAILURE")));
    await presentation("standard");
    expect(screen.getByRole("group", { name: /sandbox approval/i }).textContent).toContain("APPROVAL_HANDOFF_FAILURE");
    expect(approve().disabled).toBe(false);
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await act(async () => fireEvent.click(approve()));
  } else {
    await act(async () => finish({}));
  }
  expect(screen.queryByRole("group", { name: /sandbox approval/i })).toBeNull();
  expect(fake.calls.filter((c) => c.method === "evener/sandbox/escalation/resolve").map((c) => c.params)).toEqual(
    Array.from({ length: outcome === "retry" ? 2 : 1 }, () => ({ ref, escalationId: "request-1", approve: true })),
  );
});

test("Comic paper theme stays scoped to the conversation pane", async () => {
  const originalTheme = document.documentElement.getAttribute("data-theme");
  const fake = await setup();
  const view = await mount(fake);
  await presentation("comic");
  expect(view.container.querySelector(`[data-session-ref="${ref}"]`)?.getAttribute("data-theme")).toBe("light");
  expect(document.documentElement.getAttribute("data-theme")).toBe(originalTheme);
  await presentation("standard");
  expect(view.container.querySelector("[data-comic-font]")).toBeNull();
  expect(document.documentElement.getAttribute("data-theme")).toBe(originalTheme);
});

test("unavailable Comic fonts announce fallback while the shared composer still queues", async () => {
  const saved = Object.getOwnPropertyDescriptor(document, "fonts");
  Object.defineProperty(document, "fonts", {
    configurable: true,
    value: Object.assign(new EventTarget(), { load: async () => [], ready: Promise.resolve() }),
  });
  try {
    const fake = await setup("active");
    const view = await mount(fake);
    const source = lifetime().composer;
    await presentation("comic");
    expect(view.container.querySelector(`[data-session-ref="${ref}"]`)?.getAttribute("data-comic-font")).toBe(
      "unavailable",
    );
    expect(screen.getByRole("status", { name: /font/i })).toBeDefined();
    act(() => connectionStore.setState({ state: "idle" }));
    replaceEditorText(editor(), "FONT_FALLBACK_MESSAGE");
    await act(async () => fireEvent.click(screen.getByTestId("composer-submit")));
    await waitFor(async () => expect(await storage.listOutbox(ref)).toHaveLength(1));
    await act(async () =>
      expect(await storage.listOutbox(ref)).toMatchObject([
        { method: "turn/queue", payload: { input: [{ type: "text", text: "FONT_FALLBACK_MESSAGE" }] } },
      ]),
    );
    expect(lifetime().composer).toBe(source);
  } finally {
    if (saved) Object.defineProperty(document, "fonts", saved);
    else Reflect.deleteProperty(document, "fonts");
  }
});

test("the live Comic menu returns to Standard without replacing the pane", async () => {
  const fake = await setup();
  await mount(fake);
  await presentation("comic");
  expect(document.querySelector("[data-comic-composer]")).not.toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /session actions/i }));
  fireEvent.click(screen.getByRole("menuitem", { name: /verbosity/i }));
  await act(async () => fireEvent.click(screen.getByRole("radio", { name: "Standard" })));
  expect(document.querySelector("[data-comic-composer]")).toBeNull();
  expect(workspaceStore.getState().panes.filter((p) => p.id === paneId)).toHaveLength(1);
});

test("switching an active pane keeps one delayed Stop action on the shared ref", async () => {
  const fake = await setup("active");
  fake.on("turn/interrupt", (p) => ({
    receipt: {
      clientMutationId: p.clientMutationId,
      disposition: "applied",
      threadId: `thr_${ref}`,
      projectionState: "reflected",
    },
  }));
  await mount(fake);
  replaceEditorText(editor(), "unfinished draft");
  await presentation("comic");
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  replaceEditorText(editor(), "");
  expect(screen.getAllByTestId("composer-stop")).toHaveLength(1);
  const stop = screen.getByTestId("composer-stop") as HTMLButtonElement;
  expect(stop.disabled).toBe(true);
  fireEvent.click(stop);
  expect(fake.calls.filter((c) => c.method === "turn/interrupt")).toEqual([]);
  act(() => vi.advanceTimersByTime(500));
  expect(stop.disabled).toBe(false);
  vi.useRealTimers();
  await act(async () => fireEvent.click(stop));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "turn/interrupt")).toHaveLength(1));
  await presentation("standard");
  expect(fake.calls.filter((c) => c.method === "turn/interrupt").map((c) => (c.params as { ref: string }).ref)).toEqual(
    [ref],
  );
});

test("restart-required Comic keeps its recovery explanation and menu without submitting", async () => {
  const fake = await setup("restartRequired");
  await mount(fake);
  await presentation("comic");
  expect(document.querySelector('[data-reason="version-mismatch"]')).not.toBeNull();
  expect(screen.getAllByRole("button", { name: /session actions/i })).toHaveLength(1);
  expect(document.querySelector("[data-comic-composer]")).not.toBeNull();
  expect(fake.calls.filter((c) => c.method === "thread/resume" || c.method === "turn/start")).toEqual([]);
});

test.each(["loading", "deleted"])("%s keeps the pane's presentation control reachable", async (state) => {
  const fake = state === "deleted" ? await setup() : new FakeClient("idle");
  if (state === "deleted") act(() => threadsStore.setState({ deletedRefs: new Set([ref]) }));
  await presentation("comic");
  await mount(fake);
  fireEvent.click(screen.getByRole("button", { name: /verbosity/i }));
  await act(async () => fireEvent.click(screen.getByRole("radio", { name: "Standard" })));
  expect(transcriptDisplayStore.getState().presentation.desktop).toBe("standard");
  expect(document.querySelector("[data-comic-composer]")).toBeNull();
  expect(workspaceStore.getState().panes.find((p) => p.id === paneId)?.params).toEqual({ ref });
});

test("a selected recovery owner and its structured attachment survive both modes and remount", async () => {
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
    threadId: `thr_${ref}`,
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
  await storage.transferToRecovery(row.clientMutationId, "rejected");
  const fake = await setup();
  let view = await mount(fake);
  await waitFor(() => expect(lifetime().composer?.getSnapshot().activeRecoveryId).toBe(row.clientMutationId));
  const source = lifetime().composer;
  await act(async () => source?.flushRecoveryWrites());
  const before = preservedDraft();
  const images = source?.attachments
    .getState()
    .items.map(({ marker, name, mediaType, data }) => ({ marker, name, mediaType, data }));
  expect(before).toMatchObject({
    text,
    skillNames: ["alpha"],
    commandNames: ["build"],
    mentions: [
      { kind: "skill", name: "alpha", offset: 0 },
      { kind: "command", name: "build", offset: 7 },
    ],
  });
  await presentation("comic");
  expect(document.querySelector("[data-comic-composer]")).not.toBeNull();
  expect(lifetime().composer).toBe(source);
  await presentation("standard");
  expect(preservedDraft()).toEqual(before);
  expect(
    source?.attachments
      .getState()
      .items.map(({ marker, name, mediaType, data }) => ({ marker, name, mediaType, data })),
  ).toEqual(images);
  view.unmount();
  view = await mount(fake);
  await presentation("comic");
  expect(lifetime().composer).toBe(source);
  expect(preservedDraft()).toEqual(before);
  await act(async () => {
    expect(await storage.getRecovery(row.clientMutationId)).not.toBeNull();
  });
  await act(async () => {
    expect(await storage.listOutbox(ref)).toEqual([]);
  });
  view.unmount();
});

test.each([false, true])("question answers and approvals hand off through the menu with mobile=%s", async (mobile) => {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: mobile && query === "(max-width: 899px)",
    media: query,
    addEventListener() {},
    removeEventListener() {},
  }));
  transcriptDisplayStore.getState().setViewport(mobile ? "mobile" : "desktop");
  const geometry = installTranscriptGeometry(() => ({ width: 800, viewportHeight: 900, rowHeights: [100, 100, 400] }));
  onTestFinished(() => geometry.restore());
  const fake = await setup();
  await mount(fake);
  await waitFor(() => expect(editor()).toBeDefined());
  const owner = lifetime();
  const composer = owner.composer;
  async function choosePresentation(value: "Standard" | "Comic") {
    expect(screen.getAllByRole("button", { name: "Session actions", hidden: true })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Session actions" }));
    fireEvent.click(screen.getByRole("menuitem", { name: /verbosity/i }));
    await act(async () => fireEvent.click(screen.getByRole("radio", { name: value })));
    expect(lifetime()).toBe(owner);
    expect(lifetime().composer).toBe(composer);
  }
  act(() => {
    ackAskUserCall(fake, ref, "turn-1", "ask-item", "ask-call", [
      {
        header: "CHOICE",
        question: "FIXTURE_QUESTION",
        options: [
          { label: "ALPHA", detail: "" },
          { label: "BETA", detail: "" },
        ],
      },
    ]);
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: {
        ref,
        threadId: `thr_${ref}`,
        escalationId: "request-1",
        mode: "workspace-write",
        tool: "write_file",
        kind: "file",
        deniedPath: "/outside/proof.txt",
      },
    });
  });
  await act(async () => geometry.notify());
  fireEvent.click(await screen.findByRole("radio", { name: /ALPHA/ }));
  expect(document.querySelector('[contenteditable="true"]')?.closest("[hidden][inert]")).toBeInstanceOf(HTMLElement);
  await choosePresentation("Comic");
  expect(screen.getAllByRole("radio", { name: /ALPHA/ })).toHaveLength(1);
  expect((screen.getByRole("radio", { name: /ALPHA/ }) as HTMLInputElement).checked).toBe(true);
  expect(
    within(screen.getByRole("group", { name: /sandbox approval/i })).getAllByRole("button", { name: /allow/i }),
  ).toHaveLength(1);
  expect(document.querySelector("[data-comic-composer] form")?.hasAttribute("inert")).toBe(true);
  await choosePresentation("Standard");
  expect((screen.getByRole("radio", { name: /ALPHA/ }) as HTMLInputElement).checked).toBe(true);
  expect(document.querySelector('[contenteditable="true"]')?.closest("[hidden][inert]")).toBeInstanceOf(HTMLElement);
  await choosePresentation("Comic");
  act(() => connectionStore.setState({ state: "idle" }));
  fireEvent.click(screen.getByRole("button", { name: /send answers/i }));
  await waitFor(async () => expect(await storage.listOutbox(ref)).toHaveLength(1));
  await act(async () => {
    expect(JSON.stringify((await storage.listOutbox(ref))[0]?.payload)).toContain("ALPHA");
  });
  await choosePresentation("Standard");
  expect(screen.queryByRole("radio", { name: /ALPHA/ })).toBeNull();
  await act(async () => {
    expect(await storage.listOutbox(ref)).toHaveLength(1);
  });
});

test("whole-pane selection preserves the workspace identity and exact structured composer source", async () => {
  writeComposerDraft(ref, {
    text: "/alpha /build kept",
    skillNames: ["alpha"],
    commandNames: ["build"],
    mentions: [
      { kind: "skill", name: "alpha", offset: 0 },
      { kind: "command", name: "build", offset: 7 },
    ],
  });
  const fake = await setup();
  await mount(fake);
  await waitFor(() => expect(editor().textContent).toContain("kept"));
  const source = lifetime().composer;
  const before = preservedDraft();
  const panes = workspaceStore.getState().panes;
  const url = window.location.href;
  await presentation("comic");
  expect(document.querySelector(`[data-comic-composer="${ref}"]`)).not.toBeNull();
  expect(screen.getAllByRole("textbox", { name: "Message" })).toHaveLength(1);
  expect(lifetime().composer).toBe(source);
  expect(preservedDraft()).toEqual(before);
  expect(document.querySelector('[data-testid="transcript-virtual-list"]')).toBeNull();
  expect(screen.getAllByRole("button", { name: /session actions/i })).toHaveLength(1);
  await presentation("standard");
  expect(document.querySelector("[data-comic-composer]")).toBeNull();
  expect(preservedDraft()).toEqual(before);
  expect(readComposerDraft(ref)).toMatchObject({
    text: before?.text,
    skillNames: before?.skillNames,
    commandNames: before?.commandNames,
    mentions: before?.mentions,
  });
  expect(workspaceStore.getState().panes).toBe(panes);
  expect(window.location.href).toBe(url);
  expect(connectionStore.getState().client).toBe(fake);
  expect(fake.calls.filter((c) => c.method === "thread/resume" || c.method === "turn/start")).toEqual([]);
});

test("switching viewport uses its presentation without reconnecting and submits only once", async () => {
  const fake = await setup("active");
  await mount(fake);
  await waitFor(() => expect(editor()).toBeDefined());
  act(() => connectionStore.setState({ state: "idle" }));
  replaceEditorText(editor(), "queued from either view");
  const source = lifetime().composer;
  act(() => transcriptDisplayStore.getState().setPresentation("mobile", "comic"));
  expect(document.querySelector("[data-comic-composer]")).toBeNull();
  await act(async () => transcriptDisplayStore.getState().setViewport("mobile"));
  expect(document.querySelector("[data-comic-composer]")).not.toBeNull();
  expect(lifetime().composer).toBe(source);
  fireEvent.click(screen.getByTestId("composer-submit"));
  await waitFor(async () => expect(await storage.listOutbox(ref)).toHaveLength(1));
  await presentation("standard");
  await presentation("comic");
  await act(async () => {
    expect(await storage.listOutbox(ref)).toMatchObject([
      { method: "turn/queue", payload: { input: [{ type: "text", text: "queued from either view" }] } },
    ]);
  });
  expect(connectionStore.getState().client).toBe(fake);
});

test("standard anchor and Comic semantic path survive mode handoff and pane remount", async () => {
  const geometry = installTranscriptGeometry(() => ({ width: 800, viewportHeight: 300, rowHeights: [900, 900] }));
  try {
    const fake = await setup();
    let view = await mount(fake);
    await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeDefined());
    await act(async () => geometry.notify());
    const port = screen.getByTestId("transcript-virtual-list").firstElementChild as HTMLElement;
    fireEvent.wheel(port, { deltaY: -500 });
    port.scrollTop = 150;
    await act(async () => geometry.notify());
    const readView = retainedTranscriptReadView(lifetime(), ref, "session");
    const capture = captureTranscriptView(readView.id);
    expect(capture).toMatchObject({ followingBottom: false });
    expect(capture?.anchorId).toBeDefined();
    const position = {
      sessionRef: ref,
      path: [],
      mode: "away" as const,
      entryId: "item-1",
      offset: 0,
      viewportOffset: 12,
    };
    writeComicPosition(ref, position);
    await presentation("comic");
    expect(document.querySelector("[data-comic-scroll]")).not.toBeNull();
    expect(readView.getCapture()).toMatchObject({ anchorId: capture?.anchorId, anchorOffset: capture?.anchorOffset });
    await presentation("standard");
    await act(async () => geometry.notify());
    expect(captureTranscriptView(readView.id)).toMatchObject({
      anchorId: capture?.anchorId,
      anchorOffset: capture?.anchorOffset,
    });
    expect(readComicPosition(ref)).toMatchObject(position);
    view.unmount();
    view = await mount(fake);
    await presentation("comic");
    expect(readComicPosition(ref)).toMatchObject(position);
    view.unmount();
  } finally {
    geometry.restore();
  }
});
