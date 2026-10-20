import type { InputItem, Thread } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { holdNextTransaction, holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import {
  editQueuedMutation,
  removeQueuedMutation,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  threadsStore,
} from "../../../stores/threads";
import { PendingChips } from "../pending/PendingChips";
import { createTestComposerSource } from "../testing/composerSource";
import { pastePngInto, selectEditorText } from "../testing/editor";
import { installControlledImageEncoding } from "../testing/imageEncoding";
import { HeldSteerAnnouncements } from "../transcript/messages/HeldSteerAnnouncements";
import { HeldSteerStack } from "../transcript/messages/HeldSteerStack";
import { Composer } from "./Composer";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});
afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const thread: Thread = {
  id: "thread-a",
  sessionId: "session-a",
  preview: "",
  ephemeral: false,
  modelProvider: "anthropic/claude-sonnet-4-5",
  createdAt: 1,
  updatedAt: 1,
  status: { type: "idle" },
  cwd: "/tmp/project",
  cliVersion: "1",
  source: "evener",
  turns: [],
  evener: {
    ref: "root",
    mutationStateAuthoritative: true,
    queue: { revision: 0 },
    capabilities: {
      compact: true,
      clear: true,
      forkFromTurn: true,
      shutdown: true,
      changeModel: true,
      changeVisionModel: true,
      goal: true,
      sharedNotes: true,
      rename: true,
      send: true,
      queue: true,
      steer: true,
      interrupt: true,
      skillInput: true,
      commandInput: true,
    },
  },
};

async function setup(offline = true) {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = new FakeClient("ready");
  fake.on("thread/read", () => ({
    thread,
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc", length: 0 },
  }));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread("root");
  if (offline) fake.emitStateChange("reconnecting");
  return { storage, fake };
}

function mount(fake: FakeClient, source = createTestComposerSource("root")) {
  const view = render(
    <ClientProvider client={fake}>
      <Composer ref="root" source={source} focused={false} />
    </ClientProvider>,
  );
  return { source, ...view };
}

// Losing the durable Edit claim, merging over the draft, or resending either
// original identity breaks this journey. Transport is the only scripted owner.
test.each([false, true])("queuedEditsPreserveDraftAndSendOnce, Stop=%s", async (stop) => {
  const { storage, fake } = await setup(false);
  const readStarted = deferred<void>();
  const releaseRead = deferred<void>();
  fake.on("thread/read", async () => {
    readStarted.resolve();
    await releaseRead.promise;
    return { thread, bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc", length: 0 } };
  });
  // A transport read is outstanding: the real dispatcher must wait for its
  // authoritative snapshot while the real enqueue funnel retains intent.
  const refresh = threadsStore.getState().refreshThread("root");
  await readStarted.promise;
  const original = { marker: 1, mediaType: "image/png", data: "AQID", name: "original.png" };
  await threadsStore.getState().queue("root", "original [image 1]", [original], ["review"]);
  await threadsStore.getState().queue("root", "original [image 1]", [original], ["review"]);
  const [first, second] = await storage.listOutbox("root");
  if (!first || !second) throw new Error("missing queued fixture");
  if (stop) await threadsStore.getState().interrupt("root");
  fake.emitStateChange("reconnecting");
  releaseRead.resolve();
  await refresh;
  expect((await storage.getOutbox(first.clientMutationId))?.state).toBe(stop ? "canceled" : "submitting");
  expect((await storage.getOutbox(first.clientMutationId))?.attempted).toBe(false);
  const reopened = new MutationOutboxIndexedDB();
  const source = createTestComposerSource("root");
  const draftText = "current [image 1] /draft";
  source.editor.write(draftText, draftText.length);
  source.editSkillNames(["draft"]);
  source.updateSelectionMeta({
    text: draftText,
    skillNames: ["draft"],
    mentions: [{ kind: "skill", name: "draft", offset: draftText.indexOf("/draft") }],
  });
  source.persistDraft(draftText);
  source.attachments.setState({
    items: [{ marker: 1, name: "current.png", mediaType: "image/png", data: "BAUG", pending: false }],
  });
  mount(fake, source);
  await flushPendingTurnsProjectionForTests();
  const user = userEvent.setup();
  await user.click(screen.getAllByRole("button", { name: "Edit message" })[0] as HTMLElement);
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(first.clientMutationId));
  await flushPendingTurnsProjectionForTests();
  expect(await reopened.getOutbox(first.clientMutationId)).toBeUndefined();
  expect(source.getSnapshot().text).toBe("current [image 1] /draft\n\noriginal [image 2] /review");
  expect(source.getSnapshot().skillNames).toEqual(["draft", "review"]);
  const savedImageBytes = (
    (await reopened.getRecovery(first.clientMutationId))?.payload.input as InputItem[] | undefined
  )
    ?.filter((item) => item.type === "image")
    .map((image) => image.data);
  expect(savedImageBytes).toEqual(["BAUG", "AQID"]);
  const row = screen.getByRole("button", { name: "Remove from queue" }).closest("li");
  if (!row) throw new Error("missing local queue row");
  await user.click(within(row).getByRole("button", { name: "Remove from queue" }));
  await flushPendingTurnsProjectionForTests();
  expect(await reopened.getOutbox(second.clientMutationId)).toBeUndefined();
  // Recreate the composer before the explicit Send, with only its durable owner.
  cleanup();
  source.dispose();
  const restored = mount(fake).source;
  await flushPendingTurnsProjectionForTests();
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await waitFor(() => expect(restored.getSnapshot().activeRecoveryId).toBe(first.clientMutationId));
  selectEditorText(screen.getByRole("textbox", { name: "Message" }), restored.getSnapshot().text.length);
  await user.keyboard("revised ");
  const deliveredInputs: InputItem[][] = [];
  fake.on("turn/start", (params) => {
    deliveredInputs.push(params.input ?? []);
    return {
      turn: { id: "revision", status: "inProgress", itemsView: "full", items: [] },
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread-a",
        projectionState: "reflected",
      },
    };
  });
  fake.on("turn/interrupt", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread-a",
      projectionState: "reflected",
    },
  }));
  fake.on("thread/resume", () => ({ thread }));
  await act(async () => {
    fake.emitStateChange("ready");
    fake.emitReady();
  });
  await user.click(screen.getByRole("button", { name: "Send" }));
  await flushPendingTurnsProjectionForTests();
  expect(await reopened.getRecovery(first.clientMutationId)).toBeUndefined();
  await act(async () => {
    fake.emitStateChange("ready");
    fake.emitReady();
  });
  await waitFor(() => expect(deliveredInputs).toHaveLength(1));
  expect(deliveredInputs).toEqual([
    [
      {
        type: "text",
        text: "current (attached image 1: current.png) /draft\n\noriginal (attached image 2: original.png) /review revised ",
      },
      { type: "image", mediaType: "image/png", data: "BAUG", name: "current.png" },
      { type: "image", mediaType: "image/png", data: "AQID", name: "original.png" },
      { type: "skill", name: "draft" },
      { type: "skill", name: "review" },
    ],
  ]);
  expect(await reopened.getOutbox(second.clientMutationId)).toBeUndefined();
  reopened.close();
});

async function queued(
  storage: MutationOutboxIndexedDB,
  text = "queued prose",
  input: InputItem[] = [{ type: "text", text }],
  method: "turn/queue" | "turn/drainAsSteer" | "turn/promoteQueuedAsSteer" = "turn/queue",
) {
  return storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method,
    attachments: [],
    composerText: text,
    payload: { input },
    optimisticDisplay: { input: [{ type: "text", text: "daemon preview must not enter composer" }] },
  });
}

test.each(["turn/start", "turn/steer", "turn/drainAsSteer", "turn/promoteQueuedAsSteer"] as const)(
  "joined surfaces show one local %s identity, including remove-only controls",
  async (method) => {
    const { storage, fake } = await setup();
    const control = method === "turn/drainAsSteer" || method === "turn/promoteQueuedAsSteer";
    await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method,
      attachments: [],
      composerText: control ? "" : "one local identity",
      payload: { input: control ? [] : [{ type: "text", text: "one local identity" }] },
      optimisticDisplay: { input: [{ type: "text", text: "one local identity" }] },
    });
    const view = render(
      <ClientProvider client={fake}>
        <Composer ref="root" source={createTestComposerSource("root")} focused={false} />
        <PendingChips sessionRef="root" />
        <HeldSteerStack ref="root" />
        <HeldSteerAnnouncements ref="root" />
      </ClientProvider>,
    );
    await flushPendingTurnsProjectionForTests();
    expect(screen.getAllByRole("button", { name: "Remove from queue" })).toHaveLength(1);
    expect(view.container.querySelectorAll("li")).toHaveLength(1);
    expect(screen.queryByTestId("pending-chips")).toBeNull();
    expect(screen.queryByTestId("held-steer-stack")).toBeNull();
    expect(screen.getByTestId("held-steer-announcements").getAttribute("data-outcomes")).toBeNull();
  },
);

test("two offline edits consolidate one durable owner and preserve typing during commit", async () => {
  const { storage, fake } = await setup();
  const first = await queued(storage, "first");
  const second = await queued(storage, "second");
  const { source } = mount(fake);
  await flushPendingTurnsProjectionForTests();
  const user = userEvent.setup();
  await user.click(screen.getAllByRole("button", { name: "Edit message" })[0] as HTMLElement);
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(first.clientMutationId));
  await flushPendingTurnsProjectionForTests();
  const hold = holdNextWriteTransaction(["recovery"]);
  try {
    await user.click(screen.getByRole("button", { name: "Edit message" }));
    await hold.reached;
    selectEditorText(screen.getByRole("textbox", { name: "Message" }), source.getSnapshot().text.length);
    await user.keyboard(" newer");
    await act(async () => hold.release());
    await waitFor(() => expect(source.getSnapshot().text).toBe("first newer\n\nsecond"));
    await flushPendingTurnsProjectionForTests();
    expect(source.getSnapshot().activeRecoveryId).toBe(first.clientMutationId);
    expect(await storage.getRecovery(second.clientMutationId)).toBeUndefined();
    expect((await storage.getRecovery(first.clientMutationId))?.composerText).toBe("first newer\n\nsecond");
    expect(await storage.listOutbox("root")).toEqual([]);
    const deliveredInputs: InputItem[][] = [];
    fake.on("turn/start", (params) => {
      deliveredInputs.push(params.input ?? []);
      return {
        turn: { id: "combined", status: "inProgress", itemsView: "full", items: [] },
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thread-a",
          projectionState: "reflected",
        },
      };
    });
    await act(async () => {
      fake.emitStateChange("ready");
      fake.emitReady();
    });
    await user.click(screen.getByRole("button", { name: "Send" }));
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(deliveredInputs).toHaveLength(1));
    expect(deliveredInputs).toEqual([[{ type: "text", text: "first newer\n\nsecond" }]]);
    expect(await storage.listRecovery("root")).toEqual([]);
  } finally {
    hold.release();
  }
});

test.each(["edit", "remove"] as const)(
  "failed %s write retains identity then storage recovery permits the action",
  async (action) => {
    const { storage } = await setup();
    const record = await queued(storage);
    const proto = IDBObjectStore.prototype;
    const write = action === "edit" ? "put" : "delete";
    const original = proto[write];
    const spy = vi.spyOn(proto, write).mockImplementation(function (this: IDBObjectStore, ...args: unknown[]) {
      if (this.name === (action === "edit" ? "recovery" : "outbox"))
        throw new DOMException("write failure", "QuotaExceededError");
      return Reflect.apply(original, this, args);
    });
    const perform = () =>
      action === "edit"
        ? editQueuedMutation(record.clientMutationId, "root")
        : removeQueuedMutation(record.clientMutationId, "root");
    await expect(perform()).rejects.toBeDefined();
    spy.mockRestore();
    expect(await storage.getOutbox(record.clientMutationId)).toEqual(record);
    expect(await storage.getRecovery(record.clientMutationId)).toBeUndefined();
    expect(await perform()).toBeTruthy();
    expect(await storage.getOutbox(record.clientMutationId)).toBeUndefined();
  },
);

test.each(["remove", "retry", "dispatch", "stop"] as const)(
  "a sibling tab's %s wins over an earlier Edit snapshot",
  async (winner) => {
    const { storage } = await setup();
    const original = await queued(storage);
    if (winner === "retry") await storage.cancelUnattempted("root");
    const sibling = new MutationOutboxIndexedDB();
    const hold = holdNextTransaction(["outbox", "sequences"], "readonly");
    try {
      const editing = editQueuedMutation(original.clientMutationId, "root");
      await hold.reached;
      const { record, stopEpoch } = await sibling.getOutboxWithStopEpoch(original.clientMutationId);
      if (!record) throw new Error("missing race fixture");
      if (winner === "remove") await sibling.removeQueuedMessage(record, { stopEpoch });
      else if (winner === "retry") await sibling.releaseCanceled(record.clientMutationId, { stopEpoch });
      else if (winner === "stop") await sibling.cancelUnattempted("root");
      else await sibling.markAttempted(record.clientMutationId);
      hold.release();
      expect(await editing).toBeUndefined();
      expect(await sibling.getRecovery(original.clientMutationId)).toBeUndefined();
    } finally {
      hold.release();
      sibling.close();
    }
  },
);

test("stale session identity cannot edit or remove a queued message", async () => {
  const { storage } = await setup();
  const stale = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "old-thread",
    method: "turn/queue",
    optimisticDisplay: {},
    attachments: [],
    payload: { input: [{ type: "text", text: "old" }] },
  });
  expect(await editQueuedMutation(stale.clientMutationId, "root")).toBeUndefined();
  expect(await removeQueuedMutation(stale.clientMutationId, "root")).toBe(false);
  expect(await storage.getOutbox(stale.clientMutationId)).toEqual(stale);
});

test("encoding blocks Edit before claiming and later images avoid literal marker references", async () => {
  const { storage, fake } = await setup();
  const record = await queued(storage, "literal [image 2]");
  const { source } = mount(fake);
  await flushPendingTurnsProjectionForTests();
  const encoding = installControlledImageEncoding();
  pastePngInto(screen.getByRole("textbox", { name: "Message" }));
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  expect(await storage.getOutbox(record.clientMutationId)).toEqual(record);
  expect(await storage.getRecovery(record.clientMutationId)).toBeUndefined();
  await act(async () => encoding.resolve());
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(record.clientMutationId));
  pastePngInto(screen.getByRole("textbox", { name: "Message" }), "later.png");
  expect(source.attachments.getState().items.map((item) => item.marker)).toEqual([1, 3]);
  await act(async () => encoding.resolve(1));
});

test.each([
  {
    kind: "image",
    text: "",
    input: [{ type: "image", mediaType: "image/png", data: "AQID" }] as InputItem[],
    method: "turn/queue" as const,
    editable: true,
  },
  {
    kind: "selection",
    text: "",
    input: [
      { type: "skill", name: "review" },
      { type: "command", name: "check" },
    ] as InputItem[],
    method: "turn/queue" as const,
    editable: true,
  },
  {
    kind: "promotion",
    text: "",
    input: [] as InputItem[],
    method: "turn/promoteQueuedAsSteer" as const,
    editable: false,
  },
  { kind: "empty drain", text: "", input: [] as InputItem[], method: "turn/drainAsSteer" as const, editable: false },
  {
    kind: "content drain",
    text: "new input only",
    input: [{ type: "text", text: "new input only" }] as InputItem[],
    method: "turn/drainAsSteer" as const,
    editable: true,
  },
])(
  "held $kind restores only composer input or offers removal only",
  async ({ text, input, method, editable, kind }) => {
    const { storage, fake } = await setup();
    const record = await queued(storage, text, input, method);
    await storage.cancelUnattempted("root");
    const { source } = mount(fake);
    await flushPendingTurnsProjectionForTests();
    expect(screen.queryAllByRole("button", { name: "Edit message" })).toHaveLength(editable ? 1 : 0);
    const user = userEvent.setup();
    if (editable) {
      await user.click(screen.getByRole("button", { name: "Edit message" }));
      await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(record.clientMutationId));
      if (kind === "image") expect(source.attachments.getState().items.map((item) => item.data)).toEqual(["AQID"]);
      else if (kind === "selection")
        expect(source.getSnapshot()).toMatchObject({ skillNames: ["review"], commandNames: ["check"] });
      else expect(source.getSnapshot().text).toBe("new input only");
    } else {
      await user.click(screen.getByRole("button", { name: "Remove from queue" }));
      await flushPendingTurnsProjectionForTests();
      expect(await storage.getOutbox(record.clientMutationId)).toBeUndefined();
      expect(source.getSnapshot().text).toBe("");
    }
  },
);

test("a Stop during recovery persistence holds the new Send identity without dispatch", async () => {
  const { storage, fake } = await setup();
  const original = await queued(storage, "held revision");
  const { source } = mount(fake);
  await flushPendingTurnsProjectionForTests();
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(original.clientMutationId));
  await flushPendingTurnsProjectionForTests();
  await act(async () => {
    fake.emitStateChange("ready");
    fake.emitReady();
  });
  await flushPendingTurnsProjectionForTests();
  const hold = holdNextWriteTransaction(["recovery"]);
  try {
    await user.click(screen.getByRole("button", { name: "Send" }));
    await hold.reached;
    await storage.cancelUnattempted("root");
    await act(async () => hold.release());
    await flushPendingTurnsProjectionForTests();
    const rows = await storage.listOutbox("root");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.clientMutationId).not.toBe(original.clientMutationId);
    expect(rows[0]?.state).toBe("canceled");
    expect(rows[0]?.composerText).toBe("held revision");
    expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  } finally {
    hold.release();
  }
});

test("an Edit claim completing after pane disposal stays recoverable without touching its replacement", async () => {
  const { storage, fake } = await setup();
  const original = await queued(storage, "original pane input");
  const { source } = mount(fake);
  await flushPendingTurnsProjectionForTests();
  const hold = holdNextWriteTransaction(["outbox", "recovery", "sequences"]);
  try {
    await userEvent.setup().click(screen.getByRole("button", { name: "Edit message" }));
    await hold.reached;
    cleanup();
    source.dispose();
    const replacement = createTestComposerSource("root");
    replacement.editor.write("replacement work", 16);
    mount(fake, replacement);
    await act(async () => hold.release());
    await flushPendingTurnsProjectionForTests();
    expect(replacement.getSnapshot()).toMatchObject({ text: "replacement work", activeRecoveryId: null });
    expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
    const reopened = new MutationOutboxIndexedDB();
    expect((await reopened.getRecovery(original.clientMutationId))?.composerText).toBe("original pane input");
    reopened.close();
  } finally {
    hold.release();
  }
});

test("Send cannot race an unobserved secondary recovery consolidation", async () => {
  const { storage, fake } = await setup();
  const first = await queued(storage, "first");
  const second = await queued(storage, "second");
  await editQueuedMutation(first.clientMutationId, "root");
  await editQueuedMutation(second.clientMutationId, "root");
  const { source } = mount(fake);
  const user = userEvent.setup();
  await flushPendingTurnsProjectionForTests();
  await user.click(screen.getAllByRole("button", { name: "Edit message" })[0] as HTMLElement);
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(first.clientMutationId));
  await flushPendingTurnsProjectionForTests();
  await act(async () => {
    fake.emitStateChange("ready");
    fake.emitReady();
  });
  await flushPendingTurnsProjectionForTests();
  const hold = holdNextWriteTransaction(["recovery"]);
  try {
    await user.click(screen.getByRole("button", { name: "Edit message" }));
    await hold.reached;
    const send = screen.getByRole("button", { name: "Send" });
    expect(send.getAttribute("aria-disabled") === "true" || (send as HTMLButtonElement).disabled).toBe(true);
    await user.click(send);
    expect(await storage.listOutbox("root")).toEqual([]);
    await act(async () => hold.release());
    await flushPendingTurnsProjectionForTests();
    expect(source.getSnapshot().text).toBe("first\n\nsecond");
  } finally {
    hold.release();
  }
});

test("failed secondary consolidation retains both original recoveries and the current draft", async () => {
  const { storage, fake } = await setup();
  const first = await queued(storage, "first");
  const second = await queued(storage, "second");
  await editQueuedMutation(first.clientMutationId, "root");
  await editQueuedMutation(second.clientMutationId, "root");
  const { source } = mount(fake);
  const user = userEvent.setup();
  await flushPendingTurnsProjectionForTests();
  await user.click(screen.getAllByRole("button", { name: "Edit message" })[0] as HTMLElement);
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(first.clientMutationId));
  await flushPendingTurnsProjectionForTests();
  const put = IDBObjectStore.prototype.put;
  const spy = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (this: IDBObjectStore, ...args) {
    if (this.name === "recovery") throw new DOMException("write failure", "QuotaExceededError");
    return put.apply(this, args);
  });
  try {
    await user.click(screen.getByRole("button", { name: "Edit message" }));
    await flushPendingTurnsProjectionForTests();
    expect(source.getSnapshot().text).toBe("first");
    const reopened = new MutationOutboxIndexedDB();
    expect((await reopened.listRecovery("root")).map((record) => record.composerText)).toEqual(["first", "second"]);
    reopened.close();
  } finally {
    spy.mockRestore();
  }
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await waitFor(() => expect(source.getSnapshot().text).toBe("first\n\nsecond"));
});
