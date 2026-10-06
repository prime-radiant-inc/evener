// Wave-5 integration wiring: T2's Composer.tsx mounts T3's QueueStrip and
// T4's AskDock inside its own tree (the wave controller's own task, per
// w5-integration-wiring-report.md - every stream's own test suite already
// covers ITS component in isolation; these tests instead drive the REAL
// assembled tree through the real stores with wire-true FakeClient
// notifications, proving the seam props (getComposerText/
// onRestoreToComposer/onDrainSuccess/useAskDockPending) are wired correctly
// - not re-deriving QueueStrip's or
// AskDock's own already-covered internal behavior.

import type { MethodTypes, Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { resetMobileViewportForTests } from "../../../shell/useIsMobile";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { holdIndexedDBEvent, holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import {
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { settleActivityDiscovery } from "../testing/activityDiscovery";
import { createTestComposerSource } from "../testing/composerSource";
import { pastePngInto, replaceEditorText, selectEditorText } from "../testing/editor";
import { installControlledImageEncoding } from "../testing/imageEncoding";
import { askDockStore, resetAskDockStoreForTests } from "./askDock/askDockStore";
import { ackAskUserCall } from "./askDock/askDockTestUtils";
import { Composer as ComposerView } from "./Composer";
import { readComposerDraft, readDraft, writeComposerDraft } from "./draft";
import { usePendingTurnEntries } from "./queue";
import { resetPendingTurnsStoreForTests, subscribeComposerSubmissionCommitted } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";

function Composer(props: React.ComponentProps<typeof ComposerView>) {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("Composer integration test rendered without a connected client");
  return (
    <ClientProvider client={client}>
      <ComposerView {...props} />
    </ClientProvider>
  );
}

class PausedCommitStorage extends MutationOutboxIndexedDB {
  readonly commitStarted: Promise<void>;
  private markCommitStarted: (() => void) | undefined;
  private releaseCommit: (() => void) | undefined;
  private readonly commitGate: Promise<void>;

  constructor() {
    super();
    this.commitStarted = new Promise((resolve) => {
      this.markCommitStarted = resolve;
    });
    this.commitGate = new Promise((resolve) => {
      this.releaseCommit = resolve;
    });
  }

  release(): void {
    this.releaseCommit?.();
  }

  override async enqueueIntent(
    intent: Parameters<MutationOutboxIndexedDB["enqueueIntent"]>[0],
  ): ReturnType<MutationOutboxIndexedDB["enqueueIntent"]> {
    this.markCommitStarted?.();
    await this.commitGate;
    return super.enqueueIntent(intent);
  }
}

class CommitObservedStorage extends MutationOutboxIndexedDB {
  readonly committed: Promise<void>;
  private markCommitted: (() => void) | undefined;

  constructor() {
    super();
    this.committed = new Promise((resolve) => {
      this.markCommitted = resolve;
    });
  }

  override async enqueueIntent(
    intent: Parameters<MutationOutboxIndexedDB["enqueueIntent"]>[0],
  ): ReturnType<MutationOutboxIndexedDB["enqueueIntent"]> {
    const record = await super.enqueueIntent(intent);
    this.markCommitted?.();
    return record;
  }
}

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

const FULL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "active" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref,
      mutationStateAuthoritative: true,
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return {
    thread: testThread(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

async function mountComposer(
  ref: string,
  overrides: Partial<Thread> = {},
  source = createTestComposerSource(ref),
): Promise<FakeClient> {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref={ref} source={source} focused={false} />
    </ClientProvider>,
  );
  await flushPendingTurnsProjectionForTests();
  return fake;
}

beforeEach(() => {
  resetToastStoreForTests();
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetMobileViewportForTests();
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetMobileViewportForTests();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

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

function textarea(): HTMLDivElement | null {
  return screen.queryByRole("textbox", { name: /message/i }) as HTMLDivElement | null;
}

// QueueStrip's drain-as-steer button. Its accessible name is exactly
// "Steer queue now" (no KeyHint), so match it exactly.
function drainButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: "Steer queue now" }) as HTMLButtonElement;
}

// The composer's OWN Steer control, addressed by its stable testid: both this
// and the drain button above start with "Steer" once a non-empty queue renders
// both, so an accessible-name query here would be navigating by a string that
// two different controls share. The names themselves are asserted in
// Composer.test.tsx's own spoken-name tests.
function composerSteerButton(): HTMLButtonElement {
  return screen.getByTestId("composer-steer") as HTMLButtonElement;
}

function skillFocusThread(ref: string): Partial<Thread> {
  return {
    ...idleFocusThread(ref),
    evener: {
      ref,
      mutationStateAuthoritative: true,
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      diagnostics: {
        skills: [
          {
            name: "skill-1",
            description: "first skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
          {
            name: "skill-2",
            description: "second skill",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  };
}

async function selectSkill(user: ReturnType<typeof userEvent.setup>, name: string): Promise<void> {
  const editor = screen.getByRole("textbox", { name: "Message" });
  replaceEditorText(editor, `Run /${name}`);
  await user.click(within(screen.getByTestId("composer-slash-menu")).getByRole("option"));
}

function selectImageFile(file: File): void {
  const picker = document.querySelector('input[type="file"]');
  if (!picker) throw new Error("Composer has no image picker");
  fireEvent.change(picker, { target: { files: [file] } });
}

test("source text survives a view remount without sending", async () => {
  const source = createTestComposerSource("root");
  const fake = await mountComposer("root", idleFocusThread("root"), source);
  await settleActivityDiscovery("root");
  await userEvent.setup().type(screen.getByRole("textbox", { name: "Message" }), "keep this draft");
  cleanup();
  render(<Composer ref="root" source={source} focused={false} />);
  await flushPendingTurnsProjectionForTests();
  expect(screen.getByRole("textbox", { name: "Message" }).textContent).toBe("keep this draft");
  expect(source.getSnapshot().text).toBe("keep this draft");
  expect(readComposerDraft("root")).toEqual({ text: "keep this draft", skillNames: [] });
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
});

test("selected source skill chips survive a view remount without sending", async () => {
  const source = createTestComposerSource("root");
  const fake = await mountComposer("root", skillFocusThread("root"), source);
  await settleActivityDiscovery("root");
  await selectSkill(userEvent.setup(), "skill-1");
  expect(source.getSnapshot().skillNames).toEqual(["skill-1"]);
  cleanup();
  render(<Composer ref="root" source={source} focused={false} />);
  await flushPendingTurnsProjectionForTests();
  expect(within(screen.getByRole("textbox", { name: "Message" })).getByTestId("composer-skill-chip").textContent).toBe(
    "/skill-1",
  );
  expect(source.getSnapshot().skillNames).toEqual(["skill-1"]);
  expect(readComposerDraft("root")).toEqual({ text: "Run /skill-1 ", skillNames: ["skill-1"] });
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
});

// Comparing only prose and skills would clear the newly inert command token.
test("held acceptance preserves a same-text command edit through source Return", async () => {
  const text = "🙂 /same /same /same";
  const mentions = [
    { kind: "command" as const, name: "same", offset: 3 },
    { kind: "skill" as const, name: "same", offset: 9 },
  ];
  writeComposerDraft("root", { text, skillNames: ["same"], commandNames: ["same"], mentions });
  const source = createTestComposerSource("root");
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const overrides = idleFocusThread("root");
  const fake = await mountComposer(
    "root",
    {
      ...overrides,
      evener: {
        ...overrides.evener,
        ref: "root",
        queue: { revision: 0 },
        capabilities: { ...FULL_CAPABILITIES, skillInput: true, commandInput: true },
      },
    },
    source,
  );
  await settleActivityDiscovery("root");
  const delivered = deferred<void>();
  fake.on("turn/start", (params) => {
    delivered.resolve();
    return {
      turn: { id: "accepted", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_root",
        projectionState: "reflected",
      },
    };
  });
  const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  try {
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Send" }));
    await hold.reached;
    const durable = await storage.listOutbox("root");
    expect(durable).toHaveLength(1);
    const mutationId = durable[0]?.clientMutationId;
    const editor = screen.getByRole("textbox", { name: "Message" });
    expect(editor.querySelectorAll("[data-command-name]")).toHaveLength(1);
    selectEditorText(editor, 3, 8);
    await user.keyboard("{Backspace}");
    await user.type(editor, "/same", { skipClick: true });
    expect(editor.textContent).toBe(text);
    expect(editor.querySelectorAll("[data-command-name]")).toHaveLength(0);
    expect(editor.querySelectorAll("[data-skill-name]")).toHaveLength(1);
    cleanup();
    await act(async () => {
      hold.release();
      await delivered.promise;
    });
    await flushPendingTurnsProjectionForTests();
    render(<Composer ref="root" source={source} focused={false} />);
    await flushPendingTurnsProjectionForTests();
    const returned = screen.getByRole("textbox", { name: "Message" });
    expect(returned.textContent).toBe(text);
    expect(returned.querySelectorAll("[data-command-name]")).toHaveLength(0);
    expect(returned.querySelectorAll("[data-skill-name]")).toHaveLength(1);
    expect(readComposerDraft("root")).toEqual({
      text,
      skillNames: ["same"],
      mentions: [{ kind: "skill", name: "same", offset: 9 }],
    });
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([
      {
        method: "turn/start",
        params: expect.objectContaining({
          ref: "root",
          clientMutationId: mutationId,
          input: [
            { type: "text", text, mentions },
            { type: "skill", name: "same" },
            { type: "command", name: "same" },
          ],
        }),
      },
    ]);
  } finally {
    hold.release();
  }
});

test.each([
  { recovery: false, newer: false },
  { recovery: false, newer: true },
  { recovery: true, newer: false },
  { recovery: true, newer: true },
])(
  "detached delayed acceptance retains source ownership, recovery $recovery, newer $newer",
  async ({ recovery, newer }) => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const input = [
      { type: "text" as const, text: "Run /skill-1 " },
      { type: "skill" as const, name: "skill-1" },
    ];
    const original = recovery
      ? await storage.enqueueIntent({
          targetRef: "root",
          method: "turn/start",
          payload: { ref: "root", input },
          attachments: [],
          optimisticDisplay: { method: "turn/start", input },
        })
      : null;
    if (original) await storage.transferToRecovery(original.clientMutationId, "rejected");
    const source = createTestComposerSource("root");
    const fake = await mountComposer("root", skillFocusThread("root"), source);
    await settleActivityDiscovery("root");
    const user = userEvent.setup();
    if (!recovery) await selectSkill(user, "skill-1");
    expect(source.getSnapshot().activeRecoveryId).toBe(original?.clientMutationId ?? null);
    expect(source.getSnapshot().skillNames).toEqual(["skill-1"]);
    const delivered = deferred<void>();
    fake.on("turn/start", (params) => {
      delivered.resolve();
      return {
        turn: { id: "accepted", status: "inProgress", itemsView: "full", items: [] },
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thr_root",
          projectionState: "reflected",
        },
      };
    });
    await flushPendingTurnsProjectionForTests();
    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    try {
      await user.click(screen.getByRole("button", { name: "Send" }));
      await hold.reached;
      expect(source.getSnapshot().activeRecoveryId).toBe(original?.clientMutationId ?? null);
      const durable = await storage.listOutbox("root");
      expect(durable).toHaveLength(1);
      const mutationId = durable[0]?.clientMutationId;
      expect(mutationId).toEqual(expect.any(String));
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
      if (newer) await selectSkill(user, "skill-2");
      cleanup();
      await act(async () => {
        hold.release();
        await delivered.promise;
      });
      await flushPendingTurnsProjectionForTests();
      expect(source.getSnapshot().activeRecoveryId).toBeNull();
      expect(source.getSnapshot().text).toBe(newer ? "Run /skill-2 " : "");
      expect(source.getSnapshot().skillNames).toEqual(newer ? ["skill-2"] : []);
      render(<Composer ref="root" source={source} focused={false} />);
      await flushPendingTurnsProjectionForTests();
      expect(screen.getByRole("textbox", { name: "Message" }).textContent).toBe(newer ? "Run /skill-2 " : "");
      expect(readComposerDraft("root")).toEqual(
        newer ? { text: "Run /skill-2 ", skillNames: ["skill-2"] } : { text: "", skillNames: [] },
      );
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([
        { method: "turn/start", params: expect.objectContaining({ ref: "root", clientMutationId: mutationId, input }) },
      ]);
      expect(fake.calls.filter((call) => call.method === "turn/steer")).toEqual([]);
      if (original) expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
    } finally {
      hold.release();
    }
  },
);

test.each([
  { action: "close", pending: "recovery edit" },
  { action: "reset", pending: "recovery edit" },
  { action: "close", pending: "acceptance" },
  { action: "reset", pending: "acceptance" },
])("pane $action fences held $pending completion from a same-ID replacement", async ({ action, pending }) => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const input = [
    { type: "text" as const, text: "Run /skill-1 " },
    { type: "skill" as const, name: "skill-1" },
  ];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const pane: OpenPaneRecord = { id: "session-1", type: "session", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const source = lifetime.composer;
  if (!source) throw new Error("session pane has no source composer");
  const fake = await mountComposer("root", skillFocusThread("root"), source);
  await settleActivityDiscovery("root");
  expect(source.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
  const delivered = deferred<void>();
  fake.on("turn/start", (params) => {
    delivered.resolve();
    return {
      turn: { id: "accepted", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_root",
        projectionState: "reflected",
      },
    };
  });
  const hold = holdNextWriteTransaction(
    pending === "recovery edit" ? ["recovery"] : ["outbox", "optimistic", "recovery", "sequences"],
  );
  try {
    let completion: Promise<void> | undefined;
    if (pending === "recovery edit") {
      completion = source.queueRecoveryPersistence(original.clientMutationId, "Run /skill-1 ", [], ["skill-1"]);
    } else {
      await userEvent.setup().click(screen.getByRole("button", { name: "Send" }));
    }
    await hold.reached;
    cleanup();
    if (action === "close") workspaceStore.getState().closePane(pane.id);
    else resetWorkspaceStoreForTests();
    const replacementPane: OpenPaneRecord = { ...pane, params: { ref: "root" } };
    workspaceStore.setState({ panes: [replacementPane], focusedPaneId: replacementPane.id });
    const replacement = conversationPaneLifetime(replacementPane);
    const replacementSource = replacement.composer;
    if (!replacementSource) throw new Error("replacement pane has no source composer");
    expect(lifetime.alive).toBe(false);
    expect(source.alive).toBe(false);
    expect(replacement.serial).not.toBe(lifetime.serial);
    render(<Composer ref="root" source={replacementSource} focused={false} />);
    await waitFor(() => expect(replacementSource.getSnapshot().freshRecoveryRef).toBe("root"));
    await selectSkill(userEvent.setup(), "skill-2");
    selectEditorText(screen.getByRole("textbox", { name: "Message" }), "Run /skill-2 ".length);
    const encoding = installControlledImageEncoding();
    selectImageFile(new File([new Uint8Array([1, 2, 3])], "replacement.png", { type: "image/png" }));
    await act(async () => encoding.resolve());
    await screen.findByRole("button", { name: "View replacement.png" });
    const replacementRecoveryId = pending === "recovery edit" ? original.clientMutationId : null;
    expect(replacementSource.getSnapshot().activeRecoveryId).toBe(replacementRecoveryId);
    await act(async () => {
      hold.release();
      if (completion) await completion;
      else await delivered.promise;
    });
    await flushPendingTurnsProjectionForTests();
    expect(screen.getByRole("textbox", { name: "Message" }).textContent).toBe("Run /skill-2 [image 1]");
    expect(
      within(screen.getByRole("textbox", { name: "Message" })).getByTestId("composer-skill-chip").textContent,
    ).toBe("/skill-2");
    expect(screen.getByRole("button", { name: "View replacement.png" })).toBeTruthy();
    expect(replacementSource.getSnapshot()).toMatchObject({
      text: "Run /skill-2 [image 1]",
      skillNames: ["skill-2"],
      activeRecoveryId: replacementRecoveryId,
    });
    expect(replacementSource.attachments.getState().items).toEqual([
      { marker: 1, name: "replacement.png", mediaType: "image/png", pending: false, data: "AQID", width: 8, height: 4 },
    ]);
    if (pending === "recovery edit") {
      expect(await storage.getRecovery(original.clientMutationId)).toMatchObject({
        clientMutationId: original.clientMutationId,
        targetRef: "root",
        payload: {
          input: [
            { type: "text", text: "Run /skill-2 [image 1]" },
            { type: "image", mediaType: "image/png", data: "AQID", name: "replacement.png" },
            { type: "skill", name: "skill-2" },
          ],
        },
      });
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } else {
      expect(readComposerDraft("root")).toEqual({ text: "Run /skill-2 [image 1]", skillNames: ["skill-2"] });
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([
        { method: "turn/start", params: expect.objectContaining({ ref: "root", input }) },
      ]);
    }
    expect(getToasts()).toEqual([]);
  } finally {
    hold.release();
  }
});

test("a pending source image survives a Composer view remount without sending", async () => {
  const encoding = installControlledImageEncoding();
  const source = createTestComposerSource("root");
  const fake = await mountComposer("root", idleFocusThread("root"), source);
  await settleActivityDiscovery("root");
  const message = screen.getByRole("textbox", { name: "Message" });
  const file = new File([new Uint8Array([1, 2, 3])], "source.png", { type: "image/png" });
  selectImageFile(file);
  expect(message.textContent).toBe("[image 1]");
  expect(screen.getByRole("button", { name: "Remove source.png" })).toBeTruthy();

  cleanup();
  render(<Composer ref="root" source={source} focused={false} />);
  await settleActivityDiscovery("root");
  expect(screen.getByRole("textbox", { name: "Message" }).textContent).toBe("[image 1]");
  expect(screen.queryByRole("button", { name: "Remove source.png" })).not.toBeNull();
  await act(async () => encoding.resolve());
  await screen.findByRole("button", { name: "View source.png" });
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
});

test("a source encode failure after remount keeps text typed in its current view", async () => {
  const encoding = installControlledImageEncoding();
  const source = createTestComposerSource("root");
  await mountComposer("root", idleFocusThread("root"), source);
  await settleActivityDiscovery("root");
  const file = new File([new Uint8Array([1, 2, 3])], "bad.png", { type: "image/png" });
  selectImageFile(file);
  cleanup();
  render(<Composer ref="root" source={source} focused={false} />);
  await settleActivityDiscovery("root");
  replaceEditorText(screen.getByRole("textbox", { name: "Message" }), "[image 1] keep this edit");
  await act(async () => encoding.reject());
  await waitFor(() => expect(readComposerDraft("root").text).toBe(" keep this edit"));
  expect(screen.getByRole("textbox", { name: "Message" }).textContent).toBe(" keep this edit");
  expect(screen.queryByRole("button", { name: "Remove bad.png" })).toBeNull();
});

test("an old source encode failure cannot overwrite a newer pane's same-ref draft", async () => {
  const encoding = installControlledImageEncoding();
  await mountComposer("root", idleFocusThread("root"));
  await settleActivityDiscovery("root");
  const file = new File([new Uint8Array([1, 2, 3])], "bad.png", { type: "image/png" });
  selectImageFile(file);
  cleanup();
  render(<Composer ref="root" source={createTestComposerSource("root")} focused={false} />);
  await settleActivityDiscovery("root");
  replaceEditorText(screen.getByRole("textbox", { name: "Message" }), "foreign [image 1] draft");
  await act(async () => encoding.reject());
  await waitFor(() => expect(getToasts().map((toast) => toast.text)).toEqual(["bad.png (image decode failed)"]));
  expect(readComposerDraft("root")).toEqual({ text: "foreign [image 1] draft", skillNames: [] });
  expect(screen.getByRole("textbox", { name: "Message" }).textContent).toBe("foreign [image 1] draft");
});

test.each([
  { action: "paste", text: "source draft [image 1]", persistedText: "source draft [image 1]" },
  { action: "remove", text: "source draft ", persistedText: "source draft " },
  { action: "submitted cleanup", text: "source draft ", persistedText: "foreign [image 1] draft" },
])(
  "mounted same-ref attachment $action uses its own editor after another pane edits",
  async ({ action, text, persistedText }) => {
    const encoding = installControlledImageEncoding();
    const source = createTestComposerSource("root");
    const fake = await mountComposer("root", idleFocusThread("root"), source);
    await settleActivityDiscovery("root");
    const editor = screen.getByRole("textbox", { name: "Message" });
    replaceEditorText(editor, "source draft ");
    if (action !== "paste") {
      selectEditorText(editor, "source draft ".length);
      pastePngInto(editor, "source.png");
      await act(async () => encoding.resolve());
      await screen.findByRole("button", { name: "View source.png" });
    }
    const otherSource = createTestComposerSource("root");
    const other = render(<Composer ref="root" source={otherSource} focused={false} />);
    await flushPendingTurnsProjectionForTests();
    const otherEditor = within(other.container).getByRole("textbox", { name: "Message" });
    replaceEditorText(otherEditor, "foreign [image 1] draft");
    expect(editor.textContent).toBe(action === "paste" ? "source draft " : "source draft [image 1]");
    expect(readComposerDraft("root")).toEqual({ text: "foreign [image 1] draft", skillNames: [] });

    if (action === "paste") {
      selectEditorText(editor, "source draft ".length);
      pastePngInto(editor, "source.png");
      await act(async () => encoding.resolve());
      await screen.findByRole("button", { name: "View source.png" });
    } else if (action === "remove") {
      await userEvent.setup().click(screen.getByRole("button", { name: "Remove source.png" }));
    } else {
      act(() => source.clearSubmittedAttachments([...source.attachments.getState().items]));
    }

    expect.soft(editor.textContent).toBe(text);
    expect.soft(source.getSnapshot().text).toBe(text);
    expect.soft(otherEditor.textContent).toBe("foreign [image 1] draft");
    expect.soft(readComposerDraft("root")).toEqual({ text: persistedText, skillNames: [] });
    expect(source.attachments.getState().items).toEqual(
      action === "paste"
        ? [{ marker: 1, name: "source.png", mediaType: "image/png", pending: false, data: "AQID", width: 8, height: 4 }]
        : [],
    );
    expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
    expect(getToasts()).toEqual([]);
  },
);

test("mounted encode failure cleans its own marker without claiming a newer same-ref draft", async () => {
  const encoding = installControlledImageEncoding();
  const source = createTestComposerSource("root");
  const fake = await mountComposer("root", idleFocusThread("root"), source);
  await settleActivityDiscovery("root");
  const editor = screen.getByRole("textbox", { name: "Message" });
  replaceEditorText(editor, "source draft ");
  selectEditorText(editor, "source draft ".length);
  pastePngInto(editor, "bad.png");
  expect(editor.textContent).toBe("source draft [image 1]");
  const otherSource = createTestComposerSource("root");
  const other = render(<Composer ref="root" source={otherSource} focused={false} />);
  await flushPendingTurnsProjectionForTests();
  const otherEditor = within(other.container).getByRole("textbox", { name: "Message" });
  replaceEditorText(otherEditor, "foreign [image 1] draft");

  await act(async () => encoding.reject());

  expect.soft(editor.textContent).toBe("source draft ");
  expect.soft(source.getSnapshot().text).toBe("source draft ");
  expect.soft(otherEditor.textContent).toBe("foreign [image 1] draft");
  expect.soft(readComposerDraft("root")).toEqual({ text: "foreign [image 1] draft", skillNames: [] });
  expect(source.attachments.getState().items).toEqual([]);
  expect(screen.queryByRole("button", { name: "Remove bad.png" })).toBeNull();
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
  expect(getToasts().map((toast) => ({ kind: toast.kind, text: toast.text }))).toEqual([
    { kind: "error", text: "bad.png (image decode failed)" },
  ]);
});

test.each(["success", "failure"] as const)(
  "detached recovery encode %s keeps its original durable identity",
  async (outcome) => {
    const encoding = installControlledImageEncoding();
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const input = [
      { type: "text" as const, text: "Run /skill-1 " },
      { type: "skill" as const, name: "skill-1" },
    ];
    const original = await storage.enqueueIntent({
      targetRef: "root",
      method: "turn/start",
      payload: { ref: "root", input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    });
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    const source = createTestComposerSource("root");
    const fake = await mountComposer("root", skillFocusThread("root"), source);
    await settleActivityDiscovery("root");
    selectEditorText(screen.getByRole("textbox", { name: "Message" }), "Run /skill-1 ".length);
    const file = new File([new Uint8Array([1, 2, 3])], "source.png", { type: "image/png" });
    selectImageFile(file);
    expect(source.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    cleanup();
    const child = createTestComposerSource("child");
    child.editor.write("child draft", 11);
    await act(async () => {
      if (outcome === "success") await encoding.resolve();
      else await encoding.reject();
    });
    await flushPendingTurnsProjectionForTests();
    const recovered = await storage.getRecovery(original.clientMutationId);
    expect(recovered?.clientMutationId).toBe(original.clientMutationId);
    expect(recovered?.targetRef).toBe("root");
    expect(source.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(recovered?.payload.input).toEqual(
      outcome === "success"
        ? [
            { type: "text", text: "Run /skill-1 [image 1]" },
            { type: "image", mediaType: "image/png", data: "AQID", name: "source.png" },
            { type: "skill", name: "skill-1" },
          ]
        : input,
    );
    expect(getToasts().map((toast) => toast.text)).toEqual(
      outcome === "failure" ? ["source.png (image decode failed)"] : [],
    );
    expect(readComposerDraft("child")).toEqual({ text: "child draft", skillNames: [] });
    render(<Composer ref="root" source={source} focused={false} />);
    await flushPendingTurnsProjectionForTests();
    expect(
      within(screen.getByRole("textbox", { name: "Message" })).getByTestId("composer-skill-chip").textContent,
    ).toBe("/skill-1");
    if (outcome === "success") await screen.findByRole("button", { name: "View source.png" });
    expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
  },
);

test.each(["pointer", "keyboard"] as const)(
  "ordinary %s Send returns focus to Message after successful submission",
  async (activation) => {
    const fake = await mountComposer("ref_a", {
      status: { type: "idle" },
      evener: {
        ref: "ref_a",
        mutationStateAuthoritative: true,
        capabilities: FULL_CAPABILITIES,
        queue: { revision: 0 },
      },
      turns: [],
    });
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_focus", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
    }));
    const user = userEvent.setup();
    const message = screen.getByRole("textbox", { name: "Message" }) as HTMLDivElement;
    const send = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
    await user.type(message, "ok");
    expect(document.activeElement).toBe(message);
    expect(send.disabled).toBe(false);

    if (activation === "pointer") {
      await user.click(send);
    } else {
      // Navigate the real controls, stopping if Tab wraps without reaching Send.
      do {
        await user.tab();
      } while (
        document.activeElement !== send &&
        document.activeElement !== message &&
        document.activeElement !== document.body
      );
      expect(document.activeElement).toBe(send);
      await user.keyboard("{Enter}");
    }

    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([
      {
        method: "turn/start",
        params: expect.objectContaining({ ref: "ref_a", input: [{ type: "text", text: "ok" }] }),
      },
    ]);
    expect(message.textContent).toBe("");
    expect(message.getAttribute("contenteditable")).toBe("true");
    expect(send.disabled).toBe(true);
    // The contract is usable composer focus, not a jsdom-specific BODY blur.
    await waitFor(() => expect(document.activeElement).toBe(message));
  },
);

function idleFocusThread(ref: string): Partial<Thread> {
  return {
    status: { type: "idle" },
    evener: { ref, mutationStateAuthoritative: true, capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    turns: [],
  };
}

function acceptFocusSubmission(fake: FakeClient, method: "turn/start" | "turn/queue"): void {
  if (method === "turn/start") {
    fake.on(method, (params) => ({
      turn: { id: "turn_focus", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
    }));
    return;
  }
  fake.on(method, (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thr_ref_a",
      projectionState: "reflected",
    },
  }));
}

test.each(["pointer", "keyboard"] as const)(
  "ordinary %s Send keeps Message focus when routing to Queue",
  async (activation) => {
    const fake = await mountComposer("ref_a");
    acceptFocusSubmission(fake, "turn/queue");
    const user = userEvent.setup();
    const message = screen.getByRole("textbox", { name: "Message" }) as HTMLDivElement;
    const send = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
    await user.type(message, "qf");
    expect(send.disabled).toBe(false);
    if (activation === "pointer") {
      await user.click(send);
    } else {
      do {
        await user.tab();
      } while (
        document.activeElement !== send &&
        document.activeElement !== message &&
        document.activeElement !== document.body
      );
      expect(document.activeElement).toBe(send);
      await user.keyboard("{Enter}");
    }
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([
      {
        method: "turn/queue",
        params: expect.objectContaining({ ref: "ref_a", input: [{ type: "text", text: "qf" }] }),
      },
    ]);
    expect(message.textContent).toBe("");
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    expect(message.getAttribute("contenteditable")).toBe("true");
    expect(send.disabled).toBe(true);
    await waitFor(() => expect(document.activeElement).toBe(message));
  },
);

test.each(["other control", "sibling Composer", "replacement Composer"] as const)(
  "ordinary Send does not steal focus from %s after a delayed commit",
  async (destination) => {
    const storage = new PausedCommitStorage();
    setMutationStorageForTests(storage);
    const fake = await mountComposer("ref_a", idleFocusThread("ref_a"));
    acceptFocusSubmission(fake, "turn/start");
    const user = userEvent.setup();
    const message = screen.getByRole("textbox", { name: "Message" }) as HTMLDivElement;
    const send = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
    await user.type(message, "df");
    try {
      await user.click(send);
      await storage.commitStarted;
      expect(send.disabled).toBe(true);
      expect(message.textContent).toBe("df");
      expect(document.activeElement).toBe(message);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
      fireEvent.submit(message.closest("form")!);
      expect(document.activeElement).toBe(message);

      let destinationElement: HTMLElement;
      if (destination === "other control") {
        render(<button type="button">Elsewhere</button>);
        destinationElement = screen.getByRole("button", { name: "Elsewhere" });
        await user.click(destinationElement);
      } else {
        if (destination === "replacement Composer") cleanup();
        fake.on("thread/read", () => readResponse("ref_b", idleFocusThread("ref_b")));
        await act(async () => {
          await threadsStore.getState().ensureThread("ref_b");
        });
        const second = render(<Composer ref="ref_b" source={createTestComposerSource("ref_b")} focused={false} />);
        destinationElement = second
          .getAllByRole("textbox", { name: "Message" })
          .find((element) => element !== message)!;
        await user.type(destinationElement, "od");
      }
      expect(document.activeElement).toBe(destinationElement);
      await act(async () => storage.release());
      await flushPendingTurnsProjectionForTests();
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
      if (destination !== "replacement Composer") await waitFor(() => expect(message.textContent).toBe(""));
      else expect(message.isConnected).toBe(false);
      expect(document.activeElement).toBe(destinationElement);
      if (destination !== "other control") expect((destinationElement as HTMLDivElement).textContent).toBe("od");
    } finally {
      storage.release();
    }
  },
);

test("ordinary Send does not take another control's focus on programmatic form submission", async () => {
  const fake = await mountComposer("ref_a", idleFocusThread("ref_a"));
  acceptFocusSubmission(fake, "turn/start");
  render(<button type="button">Elsewhere</button>);
  const user = userEvent.setup();
  const message = screen.getByRole("textbox", { name: "Message" }) as HTMLDivElement;
  const elsewhere = screen.getByRole("button", { name: "Elsewhere" });
  const send = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
  await user.type(message, "us");
  await user.click(elsewhere);
  act(() => message.closest("form")!.requestSubmit(send));
  expect(document.activeElement).toBe(elsewhere);
  await flushPendingTurnsProjectionForTests();
  expect(message.textContent).toBe("");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  expect(document.activeElement).toBe(elsewhere);
});

test("ordinary Send preserves the textarea submission shortcut and next typing", async () => {
  const fake = await mountComposer("ref_a", idleFocusThread("ref_a"));
  acceptFocusSubmission(fake, "turn/start");
  const user = userEvent.setup();
  const message = screen.getByRole("textbox", { name: "Message" }) as HTMLDivElement;
  await user.type(message, "sf");
  await user.keyboard("{Control>}{Enter}{/Control}");
  await flushPendingTurnsProjectionForTests();
  expect(message.textContent).toBe("");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  expect(document.activeElement).toBe(message);
  await user.keyboard("next draft");
  expect(message.textContent).toBe("next draft");
});

test("ordinary Send retains draft and reports local failure without late focus theft", async () => {
  const storage = new PausedCommitStorage();
  setMutationStorageForTests(storage);
  let failCommit: (() => void) | undefined;
  const failure = new Promise<void>((resolve) => {
    failCommit = resolve;
  });
  const enqueue = vi.spyOn(storage, "enqueueIntent").mockImplementationOnce(async () => {
    await failure;
    throw new Error("focus proof storage failure");
  });
  const fake = await mountComposer("ref_a", idleFocusThread("ref_a"));
  render(<button type="button">Elsewhere</button>);
  const user = userEvent.setup();
  const message = screen.getByRole("textbox", { name: "Message" }) as HTMLDivElement;
  const send = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
  const elsewhere = screen.getByRole("button", { name: "Elsewhere" });
  try {
    await user.type(message, "kf");
    await user.click(send);
    await waitFor(() => expect(enqueue).toHaveBeenCalledTimes(1));
    expect(send.disabled).toBe(true);
    expect(document.activeElement).toBe(message);
    await user.click(elsewhere);
    await act(async () => failCommit?.());
    await waitFor(() =>
      expect(screen.getByRole("region", { name: "Notifications" }).textContent).toContain(
        "focus proof storage failure",
      ),
    );
    await flushPendingTurnsProjectionForTests();
    expect(message.textContent).toBe("kf");
    expect(readDraft("ref_a")).toBe("kf");
    expect(send.disabled).toBe(false);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    expect(document.activeElement).toBe(elsewhere);
  } finally {
    failCommit?.();
    storage.release();
  }
});

test("ordinary Send empty form no-op leaves another control focused", async () => {
  const fake = await mountComposer("ref_a", idleFocusThread("ref_a"));
  render(<button type="button">Elsewhere</button>);
  const user = userEvent.setup();
  const message = screen.getByRole("textbox", { name: "Message" }) as HTMLDivElement;
  const elsewhere = screen.getByRole("button", { name: "Elsewhere" });
  await user.click(elsewhere);
  fireEvent.submit(message.closest("form")!);
  await flushPendingTurnsProjectionForTests();
  expect(message.textContent).toBe("");
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/queue")).toHaveLength(0);
  expect(document.activeElement).toBe(elsewhere);
});

test("an unconfirmed storage commit stays visible and repeated Steer clicks cannot duplicate it", async () => {
  const consoleError = vi.spyOn(console, "error");
  const fake = await mountComposer("ref_a");
  let deliveryObserved: (() => void) | undefined;
  const delivered = new Promise<void>((resolve) => {
    deliveryObserved = resolve;
  });
  fake.on("turn/steer", (params) => {
    deliveryObserved?.();
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
    };
  });
  await flushPendingTurnsProjectionForTests();
  const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  const committed = hold.reached;
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    await act(async () => {
      replaceEditorText(textarea() as HTMLDivElement, "one message only");
      expect(composerSteerButton().disabled).toBe(false);
      fireEvent.click(composerSteerButton());
      await committed;
      await vi.runOnlyPendingTimersAsync();
    });
    expect(screen.getByRole("status", { name: "Message storage" }).textContent).toBeTruthy();
    expect(textarea()?.textContent).toBe("one message only");
    expect(textarea()?.getAttribute("contenteditable")).toBe("true");
    expect(composerSteerButton().disabled).toBe(true);
    fireEvent.click(composerSteerButton());
  } finally {
    await act(async () => hold.release());
    vi.useRealTimers();
    await flushPendingTurnsProjectionForTests();
  }
  expect(textarea()?.textContent).toBe("");
  expect(screen.queryByRole("status", { name: "Message storage" })).toBeNull();
  await act(async () => delivered);
  expect(fake.calls.filter((call) => call.method === "turn/steer")).toHaveLength(1);
  expect(consoleError).not.toHaveBeenCalled();
});

test("a stalled capture read dispatches the steer directly and never fails closed", async () => {
  const fake = await mountComposer("ref_a");
  let deliveryObserved: (() => void) | undefined;
  const delivered = new Promise<void>((resolve) => {
    deliveryObserved = resolve;
  });
  fake.on("turn/steer", (params) => {
    deliveryObserved?.();
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
    };
  });
  await flushPendingTurnsProjectionForTests();
  const get = IDBObjectStore.prototype.get;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let requestObserved: (() => void) | undefined;
  let keepAlive = true;
  const reached = new Promise<void>((resolve) => {
    requestObserved = resolve;
  });
  vi.spyOn(IDBObjectStore.prototype, "get").mockImplementationOnce(function (this: IDBObjectStore, ...args) {
    const request = get.apply(this, args);
    hold = holdIndexedDBEvent(request, "success");
    void hold.reached.then(() => requestObserved?.());
    const pulse = () => {
      this.count().addEventListener("success", () => {
        if (keepAlive) pulse();
      });
    };
    pulse();
    return request;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    await act(async () => {
      replaceEditorText(textarea() as HTMLDivElement, "keep this draft");
      fireEvent.click(composerSteerButton());
      await reached;
      await vi.runOnlyPendingTimersAsync();
    });
    // The click-time capture read never answered, so this steer carries no
    // click-time stop epoch - but it still went out as a plain RPC instead of
    // failing closed and making the user retry.
    expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");
    expect(fake.calls.filter((call) => call.method === "turn/steer")).toHaveLength(1);
  } finally {
    keepAlive = false;
    hold?.release();
    vi.useRealTimers();
    await flushPendingTurnsProjectionForTests();
  }
  await act(async () => delivered);
  // The fallback send succeeded, so the composer clears the input exactly as
  // it does for any successful steer.
  expect(textarea()?.textContent).toBe("");
  // Releasing the stalled read after the fallback send must not send again.
  expect(fake.calls.filter((call) => call.method === "turn/steer")).toHaveLength(1);
});

test("a second message queues behind a committed start even when recovery projection reads stall", async () => {
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: {
      ref: "ref_a",
      // A live idle snapshot's capability set: Steer, Interrupt and Queue
      // advertise harness support (#1375), so an idle thread on a wired daemon
      // carries them true. The false idle set the kata-8c65 window used to
      // carry no longer exists.
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
    },
    turns: [],
  });
  await flushPendingTurnsProjectionForTests();
  const getAll = IDBObjectStore.prototype.getAll;
  const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "recovery") held.push(holdIndexedDBEvent(request, "success"));
    return request;
  });
  let acceptSecond: ((method: string) => void) | undefined;
  const secondRequest = new Promise<string>((resolve) => {
    acceptSecond = resolve;
  });
  let requests = 0;
  for (const method of ["turn/start", "turn/queue"] as const) {
    fake.on(method, (params) => {
      requests += 1;
      if (requests === 2) acceptSecond?.(method);
      return {
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thr_ref_a",
          projectionState: "pending",
        },
      };
    });
  }
  const user = userEvent.setup();
  try {
    await user.type(textarea() as HTMLDivElement, "m1");
    await user.click(screen.getByTestId("composer-submit"));
    await waitFor(() => expect(textarea()?.textContent).toBe(""));
    await user.type(textarea() as HTMLDivElement, "m2");
    await user.click(screen.getByTestId("composer-submit"));
    // The second submit's local commit and its draft bookkeeping can land after
    // the click returns, before the dispatch this waits for can go out, so the
    // wait runs inside act. The projection flush the other tests settle with
    // cannot settle here until the finally releases the held recovery reads.
    expect(await act(async () => secondRequest)).toBe("turn/queue");
  } finally {
    spy.mockRestore();
    for (const hold of held) hold.release();
    await flushPendingTurnsProjectionForTests();
  }
});

test.each([
  { edited: false, fromStrip: false, remount: true },
  { edited: false, fromStrip: false, remount: false },
  { edited: true, fromStrip: false, remount: true },
  { edited: true, fromStrip: false, remount: false },
  { edited: false, fromStrip: true, remount: true },
  { edited: false, fromStrip: true, remount: false },
  { edited: true, fromStrip: true, remount: true },
  { edited: true, fromStrip: true, remount: false },
  { edited: "same", fromStrip: false, remount: true },
  { edited: "same", fromStrip: false, remount: false },
  { edited: "same", fromStrip: true, remount: true },
  { edited: "same", fromStrip: true, remount: false },
])(
  "pending submissions preserve draft ownership ($edited, strip $fromStrip, remount $remount)",
  async ({ edited, fromStrip, remount }) => {
    const fake = await mountComposer(
      "ref_a",
      fromStrip
        ? {
            evener: {
              ref: "ref_a",
              capabilities: FULL_CAPABILITIES,
              activeTurnId: "turn_1",
              queue: { revision: 1, depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
            },
          }
        : {},
    );
    const method = fromStrip ? "turn/drainAsSteer" : "turn/steer";
    const actionButton = fromStrip ? drainButton : composerSteerButton;
    fake.on(method, (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "reflected",
      },
    }));
    await flushPendingTurnsProjectionForTests();
    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    const committed = hold.reached;
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await act(async () => {
        replaceEditorText(textarea() as HTMLDivElement, "original message");
        fireEvent.click(actionButton());
        await committed;
      });
      if (remount) {
        cleanup();
        render(<Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />);
      }
      expect(actionButton().disabled).toBe(true);
      fireEvent.click(actionButton());
      if (edited) replaceEditorText(textarea() as HTMLDivElement, "new draft");
      if (edited === "same") replaceEditorText(textarea() as HTMLDivElement, "original message");
    } finally {
      await act(async () => hold.release());
      vi.useRealTimers();
      await flushPendingTurnsProjectionForTests();
    }
    const expectedDraft = edited === "same" ? "original message" : edited ? "new draft" : "";
    expect(textarea()?.textContent).toBe(expectedDraft);
    expect(readDraft("ref_a")).toBe(expectedDraft);
    // The flush in the finally above has seen the dispatch out.
    expect(fake.calls.filter((call) => call.method === method)).toHaveLength(1);
  },
);

test.each(["storage", "composer"] as const)(
  "a throwing %s subscriber cannot fail a committed submission",
  async (source) => {
    const subscribe = source === "storage" ? subscribeMutationPersistence : subscribeComposerSubmissionCommitted;
    const failure = new Error("subscriber failed");
    const expectedMessage =
      source === "storage" ? "Mutation persistence listener failed" : "Composer submission listener failed";
    const realConsoleError = console.error.bind(console);
    const report = vi.spyOn(console, "error").mockImplementation((...args: unknown[]) => {
      if (args.length === 2 && args[0] === expectedMessage && args[1] === failure) return;
      realConsoleError(...args);
    });
    const unsubscribeFailure = subscribe(() => {
      throw failure;
    });
    const observed = vi.fn();
    const unsubscribeObserved = subscribe(observed);
    try {
      const fake = await mountComposer("ref_a");
      fake.on("turn/steer", () => new Promise<never>(() => undefined));
      const user = userEvent.setup();
      await user.type(textarea() as HTMLDivElement, "os");
      await user.click(composerSteerButton());
      await flushPendingTurnsProjectionForTests();
      expect(textarea()?.textContent).toBe("");
      expect(readDraft("ref_a")).toBe("");
      expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");
      expect(observed).toHaveBeenCalled();
      expect(fake.calls.filter((call) => call.method === "turn/steer")).toHaveLength(1);
      expect(report).toHaveBeenCalledWith(expect.any(String), failure);
    } finally {
      unsubscribeFailure();
      unsubscribeObserved();
      report.mockRestore();
    }
  },
);

test("the composer resends recovered text while recovery projection callbacks are stalled", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "recover me" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "recover me" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const fake = await mountComposer("ref_a");
  // The steer is awaited where it arrives: the recovery reads held below keep
  // the projection work open, so a flush could not settle until they are
  // released.
  const steerSent = deferred<void>();
  fake.on("turn/steer", () => {
    steerSent.resolve();
    return new Promise<never>(() => undefined);
  });
  await flushPendingTurnsProjectionForTests();
  expect(textarea()?.textContent).toBe("recover me");
  const getAll = IDBObjectStore.prototype.getAll;
  const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
  const spy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "recovery") held.push(holdIndexedDBEvent(request, "success"));
    return request;
  });
  try {
    const user = userEvent.setup();
    selectEditorText(textarea() as HTMLDivElement, "recover me".length);
    await user.keyboard(" edited");
    await user.click(composerSteerButton());
    await waitFor(() => expect(textarea()?.textContent).toBe(""));
    expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
    await act(async () => steerSent.promise);
    expect(fake.calls.filter((call) => call.method === "turn/steer")).toHaveLength(1);
    const call = fake.calls.find((call) => call.method === "turn/steer");
    expect(call?.params).toEqual(expect.objectContaining({ input: [{ type: "text", text: "recover me edited" }] }));
  } finally {
    spy.mockRestore();
    for (const hold of held) hold.release();
    await flushPendingTurnsProjectionForTests();
  }
});

test("rejected inline skill input restores its atoms and resends edited prose with both selections", async () => {
  const text = "Run /skill-1 and then /skill-2";
  const skills = [
    { type: "skill" as const, name: "skill-1" },
    { type: "skill" as const, name: "skill-2" },
  ];
  const input = [{ type: "text" as const, text }, ...skills];
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/steer", () => new Promise<never>(() => undefined));
  await flushPendingTurnsProjectionForTests();
  const editor = screen.getByRole("textbox", { name: "Message" }) as HTMLDivElement;
  expect(editor.textContent).toBe(text);
  expect(
    within(editor)
      .getAllByTestId("composer-skill-chip")
      .map((chip) => chip.textContent),
  ).toEqual(["/skill-1", "/skill-2"]);

  const user = userEvent.setup();
  selectEditorText(editor, text.length);
  await user.keyboard(" again");
  await flushPendingTurnsProjectionForTests();
  const editedInput = [{ type: "text", text: `${text} again` }, ...skills];
  expect((await storage.getRecovery(original.clientMutationId))?.payload.input).toEqual(editedInput);
  await user.click(composerSteerButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/steer")).toHaveLength(1);
  expect(fake.calls.find((call) => call.method === "turn/steer")?.params).toEqual(
    expect.objectContaining({ input: editedInput }),
  );
  expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
  expect(editor.textContent).toBe("");
});

// --- ask_user wire fixtures (mirrors AskDock.test.tsx's own harness) -------

function startTurn(fake: FakeClient, ref: string, turnId: string): void {
  act(() => {
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: `thr_${ref}`,
        ref: "ref-1",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: turnId, status: "inProgress", itemsView: "" }],
      },
    });
  });
}

// --- T3: queue strip wiring --------------------------------------------------

test("the strip's drain-as-steer reads the composer's live text at click time, not a stale snapshot", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea() as HTMLDivElement, "st");
  await user.click(drainButton());

  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "st" }] });
});

test("a successful strip-triggered drain clears the composer's own text and draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea() as HTMLDivElement, "dm");
  await user.click(drainButton());

  await waitFor(() => expect((textarea() as HTMLDivElement).textContent).toBe(""));
  expect(localStorage.getItem("evener.composer.draft.v1.ref_a")).toBeNull();
});

// Mirrors this file's own "text typed while a send is still in flight
// survives" idiom (Composer.test.tsx) for the strip-triggered drain path:
// onDrainSuccess previously cleared the composer's CURRENT text/attachments
// unconditionally, with no snapshot to compare against (unlike this
// component's own classic drain, which uses clearIfUnchanged) - so an edit
// landing while a strip-triggered drain was still in flight would be
// silently discarded once the drain resolved (w5-integration-wiring-
// report.md Concern #2).
test("text changed while a strip-triggered drain is in flight survives the drain's own success (not cleared) - the same unchanged-since-submit asymmetry as the classic drain path", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  let resolveDrain: (() => void) | undefined;
  fake.on(
    "turn/drainAsSteer",
    (params) =>
      new Promise((resolve) => {
        resolveDrain = () =>
          resolve({
            receipt: {
              clientMutationId: params.clientMutationId,
              disposition: "applied",
              threadId: "thread_a",
              projectionState: "reflected",
            },
          });
      }),
  );

  await user.type(textarea() as HTMLDivElement, "ab");
  // The drain's durable write is held, so the edit below lands while the
  // drain is still in flight, before handleDrain can reach its success.
  const commit = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  await user.click(drainButton());
  await commit.reached;

  // The user keeps typing while the drain is in flight - a real, synchronous
  // DOM change event landing between the drain click and its settlement.
  replaceEditorText(textarea() as HTMLDivElement, "ab plus more");
  expect(readComposerDraft("ref_a")).toEqual({ text: "ab plus more", skillNames: [] });

  // The commit lands: onDrainSuccess (handleDrain's own continuation) runs,
  // which is where a wrong clear would happen, and the dispatcher sends the
  // request this fake holds.
  commit.release();
  await flushPendingTurnsProjectionForTests();
  resolveDrain?.();
  // Settles the receipt the answered drain writes, and the refresh after it.
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true);

  expect((textarea() as HTMLDivElement).textContent).toBe("ab plus more"); // NOT cleared - text changed since the drain was triggered
  expect(readComposerDraft("ref_a")).toEqual({ text: "ab plus more", skillNames: [] });
});

test("queue edit restores inline selections that a nonempty composer drain sends unchanged", async () => {
  const text = "Run /skill-1 and then /skill-2 and /skill-1";
  const skillNames = ["skill-1", "skill-2"];
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: {
        revision: 0,
        depth: 2,
        ids: ["q1", "q2"],
        texts: [text, "another queued message"],
        preview: [text, "another queued message"],
        skillNames: [skillNames, []],
      },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    removedText: text,
  }));
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  const user = userEvent.setup();
  const edit = screen.getAllByRole("button", { name: /edit message/i })[0];
  if (!edit) throw new Error("missing queued message edit control");
  await user.click(edit);
  const editor = screen.getByRole("textbox", { name: "Message" });
  expect(editor.textContent).toBe(text);
  expect(readComposerDraft("ref_a")).toEqual({ text, skillNames });
  expect(
    within(editor)
      .getAllByTestId("composer-skill-chip")
      .map((chip) => chip.textContent),
  ).toEqual(["/skill-1", "/skill-2", "/skill-1"]);
  await user.click(drainButton());
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/drainAsSteer")).toHaveLength(1);
  expect(fake.calls.find((call) => call.method === "turn/drainAsSteer")?.params).toEqual(
    expect.objectContaining({
      input: [
        { type: "text", text },
        { type: "skill", name: "skill-1" },
        { type: "skill", name: "skill-2" },
      ],
    }),
  );
  await waitFor(() => expect(editor.textContent).toBe(""));
  expect(readComposerDraft("ref_a")).toEqual({ text: "", skillNames: [] });
});

test("editing a selection-only queued entry keeps its selection as a visible chip", async () => {
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: {
        revision: 0,
        depth: 1,
        ids: ["q1"],
        texts: [""],
        preview: ["[skill: probe]"],
        skillNames: [["probe"]],
      },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    removedText: "",
  }));
  const user = userEvent.setup();
  const edit = screen.getAllByRole("button", { name: /edit message/i })[0];
  if (!edit) throw new Error("missing queued message edit control");
  await user.click(edit);

  // The entry carries a selection and no prose. Its chip has to be visible in
  // the sentence, so the reference is written out rather than the selection
  // dropped on the way in.
  const editor = screen.getByRole("textbox", { name: "Message" });
  expect(
    within(editor)
      .getAllByTestId("composer-skill-chip")
      .map((chip) => chip.textContent),
  ).toEqual(["/probe"]);
  expect(readComposerDraft("ref_a")).toEqual({ text: "/probe", skillNames: ["probe"] });
});

test("queue edit restores inline selections when the composer already holds a draft", async () => {
  const text = "Run /skill-1 and then /skill-2";
  const merged = "already typing\n\nRun /skill-1 and then /skill-2";
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, skillInput: true },
      queue: {
        revision: 0,
        depth: 1,
        ids: ["q1"],
        texts: [text],
        preview: [text],
        skillNames: [["skill-1", "skill-2"]],
      },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    removedText: text,
  }));
  const user = userEvent.setup();
  const editor = screen.getByRole("textbox", { name: "Message" });
  // A draft the user already typed makes this a partial append rather than a
  // whole-value replacement, which is the branch that must still produce chips.
  replaceEditorText(editor, "already typing");

  const edit = screen.getAllByRole("button", { name: /edit message/i })[0];
  if (!edit) throw new Error("missing queued message edit control");
  await user.click(edit);

  expect(editor.textContent).toBe(merged);
  expect(
    within(editor)
      .getAllByTestId("composer-skill-chip")
      .map((chip) => chip.textContent),
  ).toEqual(["/skill-1", "/skill-2"]);
  expect(readComposerDraft("ref_a")).toEqual({ text: merged, skillNames: ["skill-1", "skill-2"] });
});

test("clicking Edit on a queued entry whose daemon skillNames slot is null restores the text and still cancels it", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      // A plain queued entry has no skill selection, and the daemon emits a JSON
      // null slot for it (`slices.Clone(nil)` with no custom marshaler) rather
      // than an absent field. The restore path must tolerate that shape: the
      // other strip-edit tests seed no `skillNames` at all, or a non-empty
      // selection, so neither exercises it.
      queue: {
        revision: 0,
        depth: 1,
        ids: ["q1"],
        texts: ["plain queued text"],
        preview: ["plain queued text"],
        skillNames: [null] as unknown as string[][],
      },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    removedText: "plain queued text",
  }));

  await user.click(screen.getByRole("button", { name: /edit message/i }));

  expect((textarea() as HTMLDivElement).textContent).toBe("plain queued text");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/cancelQueued")).toBe(true);
});

test("clicking Edit appends the restored text after a blank line when the composer already has typed text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued copy"], preview: ["queued copy"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    removedText: "queued copy",
  }));

  await user.type(textarea() as HTMLDivElement, "mine");
  await user.click(screen.getByRole("button", { name: /edit message/i }));

  expect((textarea() as HTMLDivElement).textContent).toBe("mine\n\nqueued copy");
});

test("clicking a queued row's cancel button fires turn/cancelQueued with that row's expectedEntryId", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/cancelQueued", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    removedText: "queued hello",
    removedImages: 0,
  }));

  await user.click(screen.getByRole("button", { name: /remove from queue/i }));

  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
  expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
});

test("mobile composer omits the working path and repository without a git lookup", async () => {
  installMobileViewport();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("local:ref_loc", { cwd: "/home/jesse/repo" }));
  fake.on("evener/git/head", () => ({ head: "composer-line", originUrl: "git@github.com:owner/repo.git" }));
  await threadsStore.getState().ensureThread("local:ref_loc");
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref="local:ref_loc" source={createTestComposerSource("local:ref_loc")} focused={false} />
    </ClientProvider>,
  );
  await flushPendingTurnsProjectionForTests();

  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  expect(screen.queryByTestId("composer-repo-location")).toBeNull();
  expect(screen.queryByTestId("composer-repo-path")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/git/head")).toBe(false);
});

test("mobile finished session with no composer card omits repository metadata", async () => {
  installMobileViewport();
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("local:ref_ended", {
      cwd: "/home/jesse/repo",
      status: { type: "closed" },
      evener: {
        ref: "local:ref_ended",
        mutationStateAuthoritative: true,
        // No follow-up card when sending is unavailable.
        capabilities: { ...FULL_CAPABILITIES, send: false },
        queue: { revision: 0 },
      },
      turns: [],
    }),
  );
  fake.on("evener/git/head", () => ({ head: "ended-branch", originUrl: "git@github.com:owner/repo.git" }));
  await threadsStore.getState().ensureThread("local:ref_ended");
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref="local:ref_ended" source={createTestComposerSource("local:ref_ended")} focused={false} />
    </ClientProvider>,
  );
  await flushPendingTurnsProjectionForTests();

  expect(screen.queryByTestId("composer-input-card")).toBeNull();
  expect(screen.queryByTestId("composer-repo-location")).toBeNull();
  expect(screen.queryByTestId("composer-repo-path")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/git/head")).toBe(false);
});

// A source-backed session's cwd is another host's path. This hub must not be
// asked to resolve a branch there: a local repository that merely shares the
// path would render as that session's branch.
test("mobile source-backed session omits repository metadata without a local git lookup", async () => {
  installMobileViewport();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("remote:ref_remote", { cwd: "/srv/remote/repo", source: "remote" }));
  await threadsStore.getState().ensureThread("remote:ref_remote");
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref="remote:ref_remote" source={createTestComposerSource("remote:ref_remote")} focused={false} />
    </ClientProvider>,
  );
  await flushPendingTurnsProjectionForTests();

  expect(screen.getByTestId("composer-input-card")).toBeTruthy();
  expect(screen.queryByTestId("composer-repo-location")).toBeNull();
  expect(screen.queryByTestId("composer-repo-path")).toBeNull();
  expect(screen.queryByTestId("composer-repo-branch")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/git/head")).toBe(false);
});

// --- shared busy gate across Composer and QueueStrip (item 6) ---------------
// Composer's own busyAction and QueueStrip's drain previously tracked busy
// state independently, so a user could fire the classic drain (Shift+Enter
// or this component's own "Steer" button) and QueueStrip's "Steer queue now"
// button concurrently - both ultimately call the SAME drainAsSteer RPC,
// neither button disabling the other.

test("while a strip-triggered drain is in flight, the composer's own classic steer control is also disabled (shared busy gate)", async () => {
  const storage = new PausedCommitStorage();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.click(drainButton());
  await storage.commitStarted;
  expect(composerSteerButton().disabled).toBe(true);

  storage.release();
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true);
});

test("while the composer's own classic drain is in flight, the strip's Steer-now button is also disabled (shared busy gate)", async () => {
  const storage = new PausedCommitStorage();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea() as HTMLDivElement, "dm");
  await user.click(composerSteerButton());

  await storage.commitStarted;
  expect(drainButton().disabled).toBe(true);

  storage.release();
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true);
});

// --- pending-tracking uniformity (send/steer/queue/drain all register) ------

test("a plain send exposes its durable pending entry while the network remains unsettled", async () => {
  const storage = new CommitObservedStorage();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  fake.on("turn/start", () => new Promise(() => {}));
  const { result } = renderHook(() => usePendingTurnEntries("ref_a", "send"));

  await user.type(textarea() as HTMLDivElement, "ha");
  await user.click(screen.getByRole("button", { name: /^send\b/i }));
  await storage.committed;
  await flushPendingTurnsProjectionForTests();

  expect(result.current).toHaveLength(1);
  expect(result.current[0]).toMatchObject({ ref: "ref_a", method: "send", text: "ha" });
});

test("a queue submit also exposes its durable pending entry in the composed UI while the network remains unsettled", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  fake.on("turn/queue", () => new Promise(() => {}));
  const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

  await user.type(textarea() as HTMLDivElement, "qm");
  // "Send" in every state - a mid-turn submit still queues (that is the ROUTE,
  // which this test proves) but the verb never changes under the user; see
  // Composer.tsx's own submitLabel comment.
  await user.click(screen.getByTestId("composer-submit"));

  await waitFor(() => expect(result.current).toHaveLength(1));
  // QueueStrip.test.tsx's own "a pending queue-method entry from another
  // submission renders as an extra, action-less row" test already proves
  // QueueStrip renders a pending row given the right store state, but only
  // in isolation (props handed to it directly) - this is the missing
  // end-to-end proof that the pending row is ALSO visible once driven
  // through the REAL, fully composed Composer+QueueStrip tree via an actual
  // user submit, not just the store's own hook state (queue-strip stream
  // review, Minor).
  expect(await screen.findByText("qm")).toBeTruthy();
});

test("relay recovery refreshes stale queue capability without reconnecting or remounting the composer", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let readCount = 0;
  fake.on("thread/read", (params) => {
    readCount += 1;
    return {
      ...readResponse("ref_a", {
        status: { type: "active" },
        evener: {
          ref: "ref_a",
          capabilities: { ...FULL_CAPABILITIES, queue: readCount > 1 },
          queue: { revision: 0 },
          activeTurnId: "turn_1",
        },
      }),
      ...(params.requestGeneration !== undefined ? { requestGeneration: params.requestGeneration } : {}),
    };
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  await threadsStore.getState().ensureThread("ref_a");
  render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref="ref_a" source={createTestComposerSource("ref_a")} focused={false} />
    </ClientProvider>,
  );

  await user.type(textarea() as HTMLDivElement, "fu");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  expect(await screen.findByText("Send is not available for this session")).toBeTruthy();
  expect(fake.calls.filter((call) => call.method === "turn/queue" || call.method === "turn/start")).toHaveLength(0);
  expect((textarea() as HTMLDivElement).textContent).toBe("fu");

  await act(async () => {
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
  });
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.capabilities.queue).toBe(true));
  expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
  expect(connectionStore.getState().client).toBe(fake);

  await user.click(screen.getByTestId("composer-submit"));

  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
});

// --- T4: ask dock wiring ------------------------------------------------------

// Ask-dock scenarios mount idle with NO pre-existing turn (unlike this
// file's own default testThread, seeded with an already-open turn_1 for the
// queue/steer scenarios above) - startTurn below is the ONE place turn_1
// gets created, exactly like AskDock.test.tsx's own hydrateWithOneAsk.
// Reusing this file's default fixture and re-firing turn/started for the
// SAME id it already pre-seeded would append a second, colliding turn_1.
function idleNoTurnOverrides(): Partial<Thread> {
  return {
    status: { type: "idle" },
    evener: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    turns: [],
  };
}

test("a pending ask hides the composer's input row, which un-hides once the ask resolves through the normal send path", async () => {
  const fake = await mountComposer("ref_a", idleNoTurnOverrides());
  expect(textarea()).toBeTruthy(); // sanity: visible before any ask arrives
  let observeTurnStart!: (params: MethodTypes["turn/start"]["params"]) => void;
  const turnStarted = new Promise<MethodTypes["turn/start"]["params"]>((resolve) => {
    observeTurnStart = resolve;
  });
  fake.on("turn/start", (params) => {
    observeTurnStart(params);
    return {
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
      turn: { id: "turn_2", status: "inProgress", itemsView: "" },
    };
  });
  startTurn(fake, "ref_a", "turn_1");
  act(() => ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1"));
  // The dock itself is the transcript's trailing row now, so this test
  // resolves the batch through the same store seam its Send button calls
  // (askDockStore.sendBatch, the real durable send path) rather than a UI
  // click - what THIS component owns is the hide and the un-hide that follows.
  //
  // Excluded from the accessibility tree by the `hidden` attribute (RTL's
  // byRole queries respect it, matching real assistive-tech behavior) -
  // a stronger, more meaningful signal than probing the `inert` IDL
  // property directly, and it also proves the textarea can't be tabbed to.
  await waitFor(() => expect(textarea()).toBeNull());
  // Nothing ask-shaped renders here: the answering surface is the
  // transcript's trailing row (Session.tsx passes AskDock as TranscriptBody's
  // trailingRow; AskDock.test.tsx and Session.test.tsx prove that half).
  expect(document.querySelector("[data-ask-response-dock]")).toBeNull();
  expect(screen.queryByText("Deploy?")).toBeNull();
  const batchId = askDockStore.getState().byRef.get("ref_a")?.batches[0]?.id;
  if (batchId === undefined) throw new Error("pending ask batch did not reconcile");

  await act(async () => {
    await askDockStore.sendBatch("ref_a", batchId);
    await turnStarted;
  });
  await flushPendingTurnsProjectionForTests();

  await expect(turnStarted).resolves.toMatchObject({
    input: [{ type: "text", text: "[answers]\n1. [Deploy?] → skipped (no answer)" }],
  });
  expect(await screen.findByRole("textbox", { name: /message/i })).toBeTruthy(); // composer un-hides again
});

// AskDock's own anchor (askDock/AskDock.tsx) announces entering ask-pending
// mode ("Answer the agent's questions.") but unmounts entirely once its
// batches empty - it cannot also announce the OTHER half of parity-m5-
// composer.md line 118's legacy transition. This is Composer's own half:
// exiting ask-pending mode announces "Message composer ready." through this
// component's OWN aria-live region (w5-integration-wiring-report.md
// Concern #4).
test("resolving the pending ask announces the composer's restoration via this component's own aria-live region", async () => {
  const fake = await mountComposer("ref_a", idleNoTurnOverrides());
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_2", status: "inProgress", itemsView: "" },
  }));

  // Never announced before any ask has ever happened - there is nothing
  // that just became ready (honest liveness, not a static claim).
  expect(screen.queryByText("Message composer ready.")).toBeNull();

  startTurn(fake, "ref_a", "turn_1");
  act(() => ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1"));
  await waitFor(() => expect(askDockStore.getState().byRef.get("ref_a")?.batches.length ?? 0).toBe(1));
  expect(screen.queryByText("Message composer ready.")).toBeNull(); // not yet - still pending

  // Resolve through the same store seam the dock's Send button calls (the
  // dock itself renders in the transcript now, not in this component).
  const batchId = askDockStore.getState().byRef.get("ref_a")?.batches[0]?.id;
  if (batchId === undefined) throw new Error("pending ask batch did not reconcile");
  await act(async () => {
    await askDockStore.sendBatch("ref_a", batchId);
  });

  expect(await screen.findByText("Message composer ready.")).toBeTruthy();
});

// --- full-tree sweep: cross-seam scenarios (task 5) -------------------------

test("the queue strip stays rendered while an ask is pending, with the dock no longer in the composer", async () => {
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    evener: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, ids: ["q1"], texts: ["queued hello"], preview: ["queued hello"] },
    },
    turns: [],
  });

  startTurn(fake, "ref_a", "turn_1");
  act(() => ackAskUserCall(fake, "ref_a", "turn_1", "item_1", "call_1"));
  // The queue strip is not part of the ask-pending hide: queued messages
  // stay visible (and manageable) while the input row is replaced.
  const queueHeading = await screen.findByText(/queued messages/i);
  expect(queueHeading).toBeTruthy();
  // The dock moved to the transcript's trailing row (Session.tsx) - it
  // renders nowhere under this component.
  expect(document.querySelector("[data-ask-response-dock]")).toBeNull();
});

test("queuing a message end to end: queue -> strip renders -> edit restores text -> cancel fires with expectedEntryId", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    evener: {
      ref: "ref_a",
      mutationStateAuthoritative: true,
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea() as HTMLDivElement, "fq");
  await user.click(screen.getByTestId("composer-submit"));
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true);
  expect((textarea() as HTMLDivElement).textContent).toBe(""); // clears optimistically like any other successful submit
  const queueCall = fake.calls.find((c) => c.method === "turn/queue");
  const clientMutationId = (queueCall?.params as { clientMutationId?: string } | undefined)?.clientMutationId;
  expect(clientMutationId).toBeTruthy();

  // The daemon's own wire echo is what actually reconciles the pending
  // entry AND is the strip's only source of queue rows (no local mutation)
  // - a successful RPC response alone never does either (pendingTurnsStore's
  // own documented contract).
  await act(async () => {
    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: {
          revision: 0,
          depth: 1,
          ids: ["q1"],
          clientMutationIds: [clientMutationId!],
          texts: ["fq"],
          preview: ["fq"],
        },
      },
    });
  });

  expect(await screen.findByText(/queued messages/i)).toBeTruthy();
  expect(screen.getByText("fq")).toBeTruthy();

  fake.on("turn/cancelQueued", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    removedText: "fq",
  }));
  await user.click(screen.getByRole("button", { name: /edit message/i }));

  expect((textarea() as HTMLDivElement).textContent).toBe("fq"); // restored into the (now empty) composer
  await flushPendingTurnsProjectionForTests();
  const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
  expect(call?.params).toMatchObject({ ref: "ref_a", index: 0, expectedEntryId: "q1" });
});
