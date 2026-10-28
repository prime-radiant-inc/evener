import {
  pruneBackgroundHistoryBodies,
  type Thread,
  type ThreadCapabilities,
  type ThreadReadResponse,
} from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { subagentOutcomesDelegatesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { askDockStore, resetAskDockStoreForTests } from "../panes/session/composer/askDock/askDockStore";
import { Composer } from "../panes/session/composer/Composer";
import { resetComposerFocusStoreForTests } from "../panes/session/composer/composerFocus";
import { readComposerDraft, writeComposerDraft } from "../panes/session/composer/draft";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { requestQuoteInsert, resetQuoteInsertStoreForTests } from "../panes/session/composer/quoteInsert";
import { type ComposerSourceState, createComposerSourceState } from "../panes/session/composer/sourceState";
import { CommittedSession } from "../panes/session/testing/CommittedSession";
import { editorCursor, replaceEditorText, selectEditorText } from "../panes/session/testing/editor";
import { ClientProvider } from "../shell/clientContext";
import { NeedsYouProvider } from "../shell/NeedsYouProvider";
import { CommandPalette } from "../shell/palette/CommandPalette";
import { openPalette, paletteStore } from "../shell/palette/paletteController";
import { navigate, paneToURL } from "../shell/routing";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { useCommandCatalog } from "../stores/commandCatalog";
import { connectionStore } from "../stores/connection";
import { MutationOutboxIndexedDB } from "../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { prefsStore, resetPrefsStoreForTests } from "../stores/prefs";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../stores/transcriptDisplay";
import { getToasts, resetToastStoreForTests } from "../widgets/toast/store";
import { ComicComposer } from "./ComicComposer";
import { readComicPosition, writeComicPosition } from "./readerPosition";

const ref = "local:comic-source";
const capabilities = {
  send: true,
  queue: true,
  steer: true,
  interrupt: true,
  skillInput: true,
  commandInput: true,
  clear: true,
  goal: true,
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
  resetAskDockStoreForTests();
  resetPendingTurnsStoreForTests();
  resetPrefsStoreForTests();
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  resetToastStoreForTests();
  useCommandCatalog.setState(useCommandCatalog.getInitialState());
  paletteStore.setState({ open: false, query: "", openSeq: 0 });
  resetTranscriptDisplayStoreForTests();
  transcriptDisplayStore.getState().setPresentation("desktop", "comic");
  storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  sources = [];
});
afterEach(async () => {
  await flushPendingTurnsProjectionForTests();
  cleanup();
  resetWorkspaceStoreForTests();
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
async function setup(status = "idle", askPending = false) {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (p) => ({
    ...(askPending ? { bootGeneration: "1", epoch: 1, snapshot: { incarnation: "ask-history", length: 1 } } : {}),
    thread: {
      id: `thread-${p.ref}`,
      sessionId: p.ref?.slice(p.ref.indexOf(":") + 1),
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
          items: askPending
            ? [
                {
                  id: `item-${p.ref}`,
                  turnId: `turn-${p.ref}`,
                  type: "commandExecution",
                  toolName: "ask_user",
                  status: "completed",
                  callId: "pending-call",
                  transcriptKey: "ask-key",
                  position: { entry: 0, item: 0 },
                  version: 1,
                  argumentsJson: JSON.stringify({
                    questions: [
                      {
                        header: "QUESTION_SENTINEL",
                        question: "BODY_SENTINEL",
                        options: [{ label: "OPTION_SENTINEL", detail: "" }],
                      },
                    ],
                  }),
                },
              ]
            : [{ id: `item-${p.ref}`, type: "agentMessage", text: `CONTENT_${p.ref}` }],
        },
      ],
      evener: {
        ref: p.ref,
        askPending,
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
  await flushPendingTurnsProjectionForTests();
  return fake;
}
function page(target: string) {
  return (
    <ClientProvider client={connectionStore.getState().client!}>
      <CommittedSession key={target} paneId="comic-composer-pane" params={{ ref: target }} focused />
    </ClientProvider>
  );
}
const editor = () => screen.getByRole("textbox", { name: "Message" });
async function settle(source?: ComposerSourceState) {
  await act(async () => {
    await source?.flushRecoveryWrites();
  });
  await flushPendingTurnsProjectionForTests();
  await act(async () => {
    await source?.flushRecoveryWrites();
  });
}

test.each([
  ["/goal investigate", "goal/set"],
  ["/clear", "thread/clear"],
] as const)("palette handoff %s executes the builtin rather than a chat turn", async (invocation, method) => {
  const fake = await setup();
  fake.on("goal/set", () => ({ started: false }));
  fake.on("thread/clear", async (params) => ({
    ref,
    thread: { ...(await fake.request("thread/read", { ref, includeTurns: true })).thread, turns: [] },
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: `thread-${ref}`,
      projectionState: "reflected",
    },
  }));
  const source = sourceFor();
  const paneId = "palette-comic";
  workspaceStore.setState({
    panes: [{ id: paneId, type: "session", params: { ref }, slot: "main" }],
    focusedPaneId: paneId,
  });
  render(
    <ClientProvider client={fake}>
      <NeedsYouProvider>
        <CommandPalette />
        <ComicComposer source={source} paneId={paneId} />
      </NeedsYouProvider>
    </ClientProvider>,
  );
  await settle(source);
  act(() => openPalette(invocation));
  const handoff = screen.getAllByRole("option").find((row) => row.dataset.kind === "handoff");
  expect(handoff).toBeDefined();
  fireEvent.click(handoff!);
  await waitFor(() => expect(source.getSnapshot().text).toBe(`${invocation} `));
  expect(paletteStore.getState().open).toBe(false);
  expect(fake.calls.some((call) => call.method === method)).toBe(false);
  await act(async () => fireEvent.click(screen.getByTestId("composer-submit")));
  await settle(source);
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/queue")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === method)).toHaveLength(1);
  if (method === "goal/set") {
    expect(fake.calls.find((call) => call.method === method)?.params).toEqual({ ref, objective: "investigate" });
  } else {
    expect(threadsStore.getState().threads.get(ref)?.turns).toEqual([]);
  }
  await waitFor(() => expect(editor().textContent).toBe(""));
  expect(readComposerDraft(ref).text).toBe("");
});

test.each([
  { outcome: "failure", edit: "none" },
  { outcome: "failure", edit: "newer" },
  { outcome: "success", edit: "newer" },
  { outcome: "success", edit: "same-text" },
])("builtin $outcome preserves $edit edits and rejects duplicate presses", async ({ outcome, edit }) => {
  const fake = await setup();
  const command = deferred<{ started: boolean }>();
  fake.on("goal/set", () => command.promise);
  const source = sourceFor();
  const view = render(<ComicComposer source={source} paneId="command-pane" />);
  await settle(source);
  const invocation = "/goal COMMAND_OBJECTIVE";
  act(() => requestQuoteInsert(ref, invocation, "prefix", "command-pane"));
  const form = view.container.querySelector("form")!;
  act(() => {
    fireEvent.submit(form);
    fireEvent.submit(form);
  });
  expect(fake.calls.filter((call) => call.method === "goal/set")).toHaveLength(1);
  expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(true);
  if (edit !== "none") {
    replaceEditorText(editor(), "NEWER_DRAFT");
    if (edit === "same-text") replaceEditorText(editor(), invocation);
  }
  // A newer ordinary message cannot escape the command's busy fence either.
  fireEvent.submit(form);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  await act(async () => {
    if (outcome === "failure") command.reject(new Error("COMMAND_FAILURE"));
    else command.resolve({ started: false });
  });
  await waitFor(() => expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(false));
  const expected = edit === "newer" ? "NEWER_DRAFT" : invocation;
  expect(source.getSnapshot().text).toBe(expected);
  expect(editor().textContent).toBe(expected);
  expect(readComposerDraft(ref).text).toBe(expected);
  expect(getToasts().map((toast) => toast.kind)).toEqual(outcome === "failure" ? ["error"] : []);
  expect(fake.calls.filter((call) => call.method === "goal/set")).toHaveLength(1);
});

test.each(["unknown", "plugin", "skill", "command", "attachment"] as const)(
  "%s input falls through builtin interception without losing its content",
  async (kind) => {
    const fake = await setup();
    useCommandCatalog.setState({ commands: [{ name: "review", description: "", source: "plugin" }] });
    const text =
      kind === "unknown"
        ? "/unknown PAYLOAD"
        : kind === "plugin"
          ? "/review PAYLOAD"
          : kind === "attachment"
            ? "/goal PAYLOAD [image 1]"
            : "/goal PAYLOAD";
    writeComposerDraft(ref, {
      text,
      skillNames: kind === "skill" ? ["goal"] : [],
      commandNames: kind === "command" ? ["goal"] : [],
      mentions: kind === "skill" || kind === "command" ? [{ kind, name: "goal", offset: 0 }] : [],
    });
    const source = sourceFor();
    if (kind === "attachment") {
      source.attachments.setState({
        items: [{ marker: 1, name: "fixture.png", mediaType: "image/png", data: "AQID", pending: false }],
      });
    }
    render(<ComicComposer source={source} />);
    await settle(source);
    act(() => connectionStore.setState({ state: "idle" }));
    fireEvent.click(screen.getByTestId("composer-submit"));
    await waitFor(async () => expect(await storage.listOutbox(ref)).toHaveLength(1));
    const [row] = await storage.listOutbox(ref);
    expect(row).toMatchObject({
      method: "turn/start",
      composerText: text,
      payload: {
        ref,
        input: [
          { type: "text", text: kind === "attachment" ? expect.stringContaining("/goal PAYLOAD") : text },
          ...(kind === "skill" || kind === "command"
            ? [{ type: kind, name: "goal" }]
            : kind === "attachment"
              ? [expect.objectContaining({ type: "image", mediaType: "image/png", data: "AQID" })]
              : []),
        ],
      },
    });
    expect(fake.calls.filter((call) => call.method === "goal/set")).toEqual([]);
    await waitFor(() => expect(editor().textContent).toBe(""));
  },
);

test.each(["/clear", "/queue QUEUED_BODY", "/steer STEERED_BODY", "/drain-as-steer"])(
  "a live recovery fence refuses %s without parking intent",
  async (invocation) => {
    const fake = await setup("active");
    const source = sourceFor();
    const view = render(<ComicComposer source={source} paneId="fenced-pane" />);
    await settle(source);
    act(() => requestQuoteInsert(ref, invocation, "prefix", "fenced-pane"));
    act(() => {
      // Arm after the rendered command catalog, then press before React commits.
      threadsStore.setState({ restartBlockingObligations: new Map([[ref, Symbol("command-recovery")]]) });
      fireEvent.submit(view.container.querySelector("form")!);
    });
    await settle(source);
    expect(getToasts().map((toast) => toast.kind)).toEqual(["error"]);
    expect(source.getSnapshot().text).toBe(invocation);
    expect(await storage.listOutbox(ref)).toEqual([]);
    expect(fake.calls.filter((call) => call.method === "thread/clear" || call.method.startsWith("turn/"))).toEqual([]);
  },
);

test.each(["queue", "steer"] as const)("/%s submits only its parsed body with durable ownership", async (verb) => {
  await setup("active");
  const source = sourceFor();
  render(<ComicComposer source={source} paneId="message-command-pane" />);
  await settle(source);
  act(() => connectionStore.setState({ state: "idle" }));
  act(() => requestQuoteInsert(ref, `/${verb} MESSAGE_BODY`, "prefix", "message-command-pane"));
  fireEvent.keyDown(editor(), { key: "Enter", ctrlKey: true });
  await waitFor(async () => expect(await storage.listOutbox(ref)).toHaveLength(1));
  expect(await storage.listOutbox(ref)).toMatchObject([
    {
      method: `turn/${verb}`,
      payload: { ref, input: [{ type: "text", text: "MESSAGE_BODY" }] },
      clientMutationId: expect.any(String),
    },
  ]);
  await waitFor(() => expect(editor().textContent).toBe(""));
  expect(readComposerDraft(ref).text).toBe("");
});

test("a live unavailable steer preserves the literal invocation without sending", async () => {
  const fake = await setup("active");
  const source = sourceFor();
  const view = render(<ComicComposer source={source} paneId="steer-pane" />);
  await settle(source);
  act(() => requestQuoteInsert(ref, "/steer MESSAGE_BODY", "prefix", "steer-pane"));
  act(() => {
    const state = threadsStore.getState();
    const model = state.threads.get(ref)!;
    threadsStore.setState({ threads: new Map(state.threads).set(ref, { ...model, status: { type: "idle" } }) });
    fireEvent.submit(view.container.querySelector("form")!);
  });
  await settle(source);
  expect(getToasts().map((toast) => toast.kind)).toEqual(["error"]);
  expect(source.getSnapshot().text).toBe("/steer MESSAGE_BODY");
  expect(await storage.listOutbox(ref)).toEqual([]);
  expect(fake.calls.filter((call) => call.method.startsWith("turn/"))).toEqual([]);
});

test.each(["comic", "standard"] as const)(
  "%s composer stays hidden and inert across pending body pruning and restoration",
  async (presentation) => {
    const fake = await setup("awaiting", true);
    const source = sourceFor();
    source.editText("RETAINED_COMPOSER_DRAFT");
    const view = render(
      <ClientProvider client={fake}>
        {presentation === "comic" ? (
          <ComicComposer source={source} />
        ) : (
          <Composer ref={ref} focused={false} source={source} />
        )}
      </ClientProvider>,
    );
    await settle(source);
    const message = view.container.querySelector('[contenteditable="true"]');
    expect(message).not.toBeNull();
    const assertBlocked = () => {
      expect(message?.closest("[hidden][inert]")).not.toBeNull();
      expect(
        askDockStore
          .getState()
          .byRef.get(ref)
          ?.batches.flatMap((b) => b.questions.map((q) => q.key)),
      ).toEqual(["pending-call:0"]);
      expect(source.getSnapshot().text).toBe("RETAINED_COMPOSER_DRAFT");
    };
    assertBlocked();
    const model = threadsStore.getState().threads.get(ref);
    if (!model) throw new Error("Missing thread");
    const pruned = pruneBackgroundHistoryBodies(model);
    expect(pruned.turns.flatMap((turn) => turn.items)).toEqual([]);
    expect(threadsStore.getState().localOpenings.get(ref)).toBeUndefined();
    act(() => threadsStore.setState((state) => ({ threads: new Map(state.threads).set(ref, pruned) })));
    assertBlocked();
    act(() => threadsStore.setState((state) => ({ threads: new Map(state.threads).set(ref, model) })));
    assertBlocked();
    await act(async () =>
      threadsStore.setState((state) => ({ threads: new Map(state.threads).set(ref, { ...model, askPending: false }) })),
    );
    expect(message?.closest("[hidden], [inert]")).toBeNull();
    expect(source.getSnapshot().text).toBe("RETAINED_COMPOSER_DRAFT");
  },
);

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

test.each([
  { text: "RETAINED_STOP_DRAFT", skillNames: [] },
  { text: "/retained-skill", skillNames: ["retained-skill"] },
])("Stop preserves an unsent draft %j", async (draft) => {
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
  source.editText(draft.text);
  source.editSkillNames(draft.skillNames);
  render(<ComicComposer source={source} />);
  expect(source.getSnapshot()).toMatchObject(draft);
  const stop = screen.getByTestId("composer-stop");
  const submit = screen.getByTestId("composer-submit");
  expect(stop).not.toBe(submit);
  expect((stop as HTMLButtonElement).disabled).toBe(false);
  await act(async () => fireEvent.click(stop));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toHaveLength(1));
  expect(fake.calls.find((call) => call.method === "turn/interrupt")?.params).toMatchObject({ ref });
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/queue")).toEqual([]);
  expect(source.getSnapshot()).toMatchObject(draft);
  expect(editor().textContent).toBe(draft.text);
});

test("opening a stopped full-page conversation does not resume and keeps a usable draft", async () => {
  const fake = await setup("notLoaded");
  writeComposerDraft(ref, { text: "saved followup", skillNames: [] });
  render(page(ref));
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
  window.history.replaceState(null, "", paneToURL("session", { ref }));
  const fake = await setup("notLoaded");
  transcriptDisplayStore.getState().setPresentation("desktop", "comic");
  const page = (target: string) => (
    <ClientProvider client={fake}>
      <CommittedSession key={target} paneId="resume-pane" params={{ ref: target }} focused />
    </ClientProvider>
  );
  let view!: ReturnType<typeof render>;
  await act(async () => {
    view = render(page(ref));
  });
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
    navigate(paneToURL("session", { ref: "local:B" }) ?? "/");
    view.rerender(page("local:B"));
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
  expect(window.location.pathname).toBe(paneToURL("session", { ref: "local:B" }));
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
    context: { ...fixture.context, ref: p.ref, sessionId: p.ref.slice(p.ref.indexOf(":") + 1) },
    scope: "session",
    delegates: { known: true, total: p.ref === root ? 1 : 0, active: 0, failed: 0, completed: 0 },
    jobs: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
    watches: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
  }));
  fake.on("evener/thread/delegates/list", (p) => ({
    ...fixture,
    scope: "session",
    context: { ...fixture.context, ref: p.ref, sessionId: p.ref.slice(p.ref.indexOf(":") + 1) },
    delegates: p.ref === root ? [child] : [],
  }));
  const position = {
    mode: "away" as const,
    sessionRef: root,
    path: [],
    entryId: `item-${root}`,
    offset: 0,
    viewportOffset: 24,
  };
  writeComicPosition(root, position);
  const view = render(page(root));
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
  fireEvent.click(within(header).getByRole("button", { name: /open conversation/i }));
  expect(window.location.pathname).toBe(paneToURL("session", { ref: child.childRef }));
  view.rerender(page(child.childRef));
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
  await act(async () => {
    connectionStore.setState({ state: "ready" });
    navigate(paneToURL("session", { ref: root })!);
    view.rerender(page(root));
  });
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
