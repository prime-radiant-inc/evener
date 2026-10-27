import type { InputItem, Thread } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { appliedReceipt } from "../../../mutationReceiptTestUtils";
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
import { createAttachmentOperations } from "./attachments/useAttachments";
import { Composer } from "./Composer";
import { readComposerDraftRevisionId, readDraft, writeComposerDraft } from "./draft";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import {
  flushPendingTurnsProjectionForTests,
  outlastEmptyFlushRoundForTests,
} from "./queue/testing/flushPendingTurnsProjection";

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

async function setup(offline = true, storage = new MutationOutboxIndexedDB()) {
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

function recoveryCompositionRevision(record: object): number {
  return (record as { compositionRevision?: number }).compositionRevision ?? 0;
}

class CommitThenPauseRecoveryStorage extends MutationOutboxIndexedDB {
  private pauseNextCommit = true;
  private committedSignal = deferred<void>();
  private continuedSignal = deferred<void>();
  private continuation = deferred<void>();

  get committed(): Promise<void> {
    return this.committedSignal.promise;
  }

  get continued(): Promise<void> {
    return this.continuedSignal.promise;
  }

  skipNextPause(): void {
    this.pauseNextCommit = false;
  }

  armPause(): void {
    this.pauseNextCommit = true;
    this.committedSignal = deferred<void>();
    this.continuedSignal = deferred<void>();
    this.continuation = deferred<void>();
  }

  override async updateRecoveryInput(
    ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
  ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
    const result = await super.updateRecoveryInput(...args);
    if (!this.pauseNextCommit) return result;
    this.pauseNextCommit = false;
    this.committedSignal.resolve();
    await this.continuation.promise;
    this.continuedSignal.resolve();
    return result;
  }

  releaseContinuation(): void {
    this.continuation.resolve();
  }
}

test("committed recovery consolidation does not append the incorporated draft twice after source disposal", async () => {
  const storage = new CommitThenPauseRecoveryStorage();
  await setup(false, storage);
  const queuedRecord = await queued(storage, "B");
  const source = createTestComposerSource("root");
  source.editor.write("A", 1);
  source.persistDraft("A");
  const revisionId = readComposerDraftRevisionId("root");
  if (!revisionId) throw new Error("missing persisted draft revision fixture");
  await editQueuedMutation(queuedRecord.clientMutationId, "root");

  let replacement: ReturnType<typeof createTestComposerSource> | undefined;
  let reopened: MutationOutboxIndexedDB | undefined;
  try {
    const claimedRecovery = await storage.getRecovery(queuedRecord.clientMutationId);
    if (!claimedRecovery) throw new Error("missing claimed recovery fixture");
    const activation = source.activateRecovery(claimedRecovery);
    await storage.committed;
    expect((await storage.getRecovery(queuedRecord.clientMutationId))?.composerText).toBe("A\n\nB");

    source.dispose();
    replacement = createTestComposerSource("root");
    expect(replacement.getSnapshot().text).toBe("A");
    const committedRecovery = await storage.getRecovery(queuedRecord.clientMutationId);
    if (!committedRecovery) throw new Error("missing committed recovery fixture");
    expect(committedRecovery.localDraftRevisionId).toBe(revisionId);
    storage.releaseContinuation();
    await storage.continued;
    await expect(activation).resolves.toBe(false);
    await expect(replacement.activateRecovery(committedRecovery)).resolves.toBe(true);

    expect(replacement.getSnapshot().text).toBe("A\n\nB");
    expect(readComposerDraftRevisionId("root")).toBeUndefined();
    storage.close();
    reopened = new MutationOutboxIndexedDB();
    expect((await reopened.getRecovery(queuedRecord.clientMutationId))?.localDraftRevisionId).toBe(revisionId);
  } finally {
    storage.releaseContinuation();
    source.dispose();
    replacement?.dispose();
    reopened?.close();
    if (!reopened) storage.close();
  }
});

test("recovery activation keeps newer replacement text selections and attachments", async () => {
  const storage = new CommitThenPauseRecoveryStorage();
  await setup(false, storage);
  const queuedRecord = await queued(storage, "B");
  const source = createTestComposerSource("root");
  source.editor.write("A", 1);
  source.persistDraft("A");
  await editQueuedMutation(queuedRecord.clientMutationId, "root");
  const claimed = await storage.getRecovery(queuedRecord.clientMutationId);
  if (!claimed) throw new Error("missing claimed recovery fixture");
  const activation = source.activateRecovery(claimed);
  let replacement: ReturnType<typeof createTestComposerSource> | undefined;

  try {
    await storage.committed;
    source.dispose();
    replacement = createTestComposerSource("root");
    replacement.editText("New /draft");
    replacement.editSkillNames(["draft"]);
    replacement.updateSelectionMeta({
      text: "New /draft",
      skillNames: ["draft"],
      mentions: [{ kind: "skill", name: "draft", offset: 4 }],
    });
    replacement.attachments.setState({
      items: [{ marker: 1, name: "new.png", mediaType: "image/png", data: "AQID", pending: false }],
    });
    storage.releaseContinuation();
    await storage.continued;
    await activation;
    const committed = await storage.getRecovery(queuedRecord.clientMutationId);
    if (!committed) throw new Error("missing committed recovery fixture");
    await replacement.activateRecovery(committed);
    expect(replacement.getSnapshot().text).toBe("New /draft\n\nA\n\nB");
    expect(replacement.getSnapshot().skillNames).toEqual(["draft"]);
    expect(replacement.getSnapshot().mentions).toEqual([{ kind: "skill", name: "draft", offset: 4 }]);
    expect(replacement.attachments.getState().items).toEqual([
      { marker: 1, name: "new.png", mediaType: "image/png", data: "AQID", pending: false },
    ]);
  } finally {
    storage.releaseContinuation();
    source.dispose();
    replacement?.dispose();
    storage.close();
  }
});

test("typing during delayed activation persists after the composer unmounts", async () => {
  const storage = new CommitThenPauseRecoveryStorage();
  const { fake } = await setup(false, storage);
  const queuedRecord = await queued(storage, "B");
  const source = createTestComposerSource("root");
  source.editor.write("A", 1);
  source.persistDraft("A");
  await editQueuedMutation(queuedRecord.clientMutationId, "root");
  const claimed = await storage.getRecovery(queuedRecord.clientMutationId);
  if (!claimed) throw new Error("missing claimed recovery fixture");
  const updateSpy = vi.spyOn(storage, "updateRecoveryInput");
  const activation = source.activateRecovery(claimed);
  const view = mount(fake, source);
  const user = userEvent.setup();
  let reopened: MutationOutboxIndexedDB | undefined;
  let replacement: ReturnType<typeof createTestComposerSource> | undefined;

  try {
    await act(async () => storage.committed);
    expect((await storage.getRecovery(queuedRecord.clientMutationId))?.composerText).toBe("A\n\nB");
    const editor = screen.getByRole("textbox", { name: "Message" });
    selectEditorText(editor, editor.textContent?.length ?? 0);
    await user.keyboard(" typed");
    act(() => view.unmount());
    await act(async () => {
      storage.releaseContinuation();
      await storage.continued;
      await activation;
    });
    await waitFor(async () => {
      expect((await storage.getRecovery(queuedRecord.clientMutationId))?.composerText).toBe("A typed\n\nB");
    });
    const committedTexts = updateSpy.mock.calls.map((call) => call[3]);
    expect(committedTexts.filter((text) => text === "A\n\nB")).toHaveLength(1);
    expect(committedTexts).toContain("A typed\n\nB");

    source.dispose();
    storage.close();
    resetThreadsStoreForTests();
    reopened = new MutationOutboxIndexedDB();
    await setup(false, reopened);
    const saved = await reopened.getRecovery(queuedRecord.clientMutationId);
    if (!saved) throw new Error("missing reopened recovery fixture");
    expect(saved.composerText).toBe("A typed\n\nB");
    replacement = createTestComposerSource("root");
    await replacement.activateRecovery(saved);
    expect(replacement.getSnapshot().text).toBe("A typed\n\nB");
  } finally {
    storage.releaseContinuation();
    source.dispose();
    replacement?.dispose();
    reopened?.close();
    if (!reopened) storage.close();
  }
});

test("image encoding survives a literal-marker recovery merge while storage is pending", async () => {
  const storage = new CommitThenPauseRecoveryStorage();
  const { fake } = await setup(false, storage);
  const queuedRecord = await queued(storage, "literal [image 1]");
  const source = createTestComposerSource("root");
  const encoding = installControlledImageEncoding();
  const user = userEvent.setup();

  try {
    await editQueuedMutation(queuedRecord.clientMutationId, "root");
    const claimed = await storage.getRecovery(queuedRecord.clientMutationId);
    if (!claimed) throw new Error("missing claimed recovery fixture");
    const activation = source.activateRecovery(claimed);
    const attachments = createAttachmentOperations(source.editor, source.attachments);
    const rejections: string[] = [];
    await storage.committed;
    attachments.ingestFiles([new File([new Uint8Array([1, 2, 3])], "new.png", { type: "image/png" })], (message) => {
      rejections.push(message);
    });
    storage.releaseContinuation();
    await storage.continued;
    await activation;
    expect(source.attachments.getState().items).toEqual([
      { marker: 2, name: "new.png", mediaType: "image/png", pending: true },
    ]);
    expect(rejections).toEqual([]);
    expect(source.getSnapshot().activeRecoveryId).toBe(queuedRecord.clientMutationId);
    expect(source.getSnapshot().text).toContain("literal [image 1]");
    expect(source.attachments.getState().items[0]?.marker).toBe(2);

    await encoding.resolve();
    expect(source.attachments.getState().items[0]?.pending).toBe(false);
    const snapshot = source.getSnapshot();
    await source.queueRecoveryPersistence(
      queuedRecord.clientMutationId,
      snapshot.text,
      attachments.toInputAttachments(),
      snapshot.skillNames,
      snapshot.commandNames,
      snapshot.mentions,
    );
    mount(fake, source);
    expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(false);

    const deliveredInputs: InputItem[][] = [];
    fake.on("turn/start", (params) => {
      deliveredInputs.push(params.input ?? []);
      return {
        turn: { id: "image-merge", status: "inProgress", itemsView: "full", items: [] },
        receipt: appliedReceipt(params.clientMutationId, "thread-a"),
      };
    });
    await user.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(deliveredInputs).toHaveLength(1));
    expect(deliveredInputs[0]).toEqual([
      { type: "text", text: "(attached image 2: new.png)\n\nliteral [image 1]" },
      { type: "image", mediaType: "image/png", data: "AQID", name: "new.png" },
    ]);
  } finally {
    storage.releaseContinuation();
    act(() => source.dispose());
    storage.close();
  }
});

test("typing and selections during an already-incorporated activation survive its commit", async () => {
  const storage = new CommitThenPauseRecoveryStorage();
  await setup(false, storage);
  const queuedRecord = await queued(storage, "B");
  const original = createTestComposerSource("root");
  original.editor.write("A", 1);
  original.persistDraft("A");
  const revisionA = readComposerDraftRevisionId("root");
  if (!revisionA) throw new Error("missing draft A revision");
  await editQueuedMutation(queuedRecord.clientMutationId, "root");
  const claimed = await storage.getRecovery(queuedRecord.clientMutationId);
  if (!claimed) throw new Error("missing claimed recovery fixture");
  storage.skipNextPause();
  const seededFirst = await storage.updateRecoveryInput(
    claimed.clientMutationId,
    [{ type: "text", text: "A\n\nB" }],
    [],
    "A\n\nB",
    [],
    [],
    revisionA,
    0,
  );
  expect(seededFirst).toBeDefined();
  original.dispose();
  const replacement = createTestComposerSource("root");
  storage.armPause();
  const activationRecord = await storage.getRecovery(claimed.clientMutationId);
  if (!activationRecord) throw new Error("missing activation recovery fixture");
  const activation = replacement.activateRecovery(activationRecord);

  try {
    await storage.committed;
    replacement.editor.write("C /draft", 8);
    replacement.editSkillNames(["draft"]);
    replacement.updateSelectionMeta({
      text: "C /draft",
      skillNames: ["draft"],
      mentions: [{ kind: "skill", name: "draft", offset: 2 }],
    });
    storage.releaseContinuation();
    await storage.continued;
    await activation;
    expect(replacement.getSnapshot().text).toBe("C /draft\n\nA\n\nB");
    expect(replacement.getSnapshot().skillNames).toEqual(["draft"]);
    expect(replacement.getSnapshot().mentions).toEqual([{ kind: "skill", name: "draft", offset: 2 }]);
  } finally {
    storage.releaseContinuation();
    original.dispose();
    replacement.dispose();
    storage.close();
  }
});

test("a second committed consolidation records C before adapter and source reopen", async () => {
  const storage = new CommitThenPauseRecoveryStorage();
  await setup(false, storage);
  const queuedRecord = await queued(storage, "B");
  const original = createTestComposerSource("root");
  original.editor.write("A", 1);
  original.persistDraft("A");
  const revisionA = readComposerDraftRevisionId("root");
  if (!revisionA) throw new Error("missing draft A revision");
  await editQueuedMutation(queuedRecord.clientMutationId, "root");
  const claimed = await storage.getRecovery(queuedRecord.clientMutationId);
  if (!claimed) throw new Error("missing claimed recovery fixture");
  storage.skipNextPause();
  await original.activateRecovery(claimed);
  const firstCommit = await storage.getRecovery(queuedRecord.clientMutationId);
  if (!firstCommit) throw new Error("missing first recovery commit");
  expect(firstCommit.composerText).toBe("A\n\nB");
  expect(firstCommit.localDraftRevisionId).toBe(revisionA);
  expect(recoveryCompositionRevision(firstCommit)).toBe(1);
  original.dispose();

  const replacement = createTestComposerSource("root");
  replacement.editor.write("C", 1);
  replacement.persistDraft("C");
  const revisionC = readComposerDraftRevisionId("root");
  if (!revisionC || revisionC === revisionA) throw new Error("missing distinct draft C revision");
  storage.armPause();
  const secondActivation = replacement.activateRecovery(firstCommit);
  let reopened: MutationOutboxIndexedDB | undefined;
  try {
    await storage.committed;
    const secondCommit = await storage.getRecovery(queuedRecord.clientMutationId);
    if (!secondCommit) throw new Error("missing second recovery commit");
    expect(secondCommit.composerText).toBe("C\n\nA\n\nB");
    expect(recoveryCompositionRevision(secondCommit)).toBe(recoveryCompositionRevision(firstCommit) + 1);
    expect(secondCommit.localDraftRevisionId).toBe(revisionC);
    storage.releaseContinuation();
    await storage.continued;
    await expect(secondActivation).resolves.toBe(true);
    replacement.dispose();

    storage.close();
    resetThreadsStoreForTests();
    reopened = new MutationOutboxIndexedDB();
    await setup(false, reopened);
    const afterReload = await reopened.getRecovery(queuedRecord.clientMutationId);
    if (!afterReload) throw new Error("missing reopened recovery fixture");
    expect(afterReload.localDraftRevisionId).toBe(revisionC);
    expect(readComposerDraftRevisionId("root")).toBe(revisionC);
    const again = createTestComposerSource("root");
    try {
      await again.activateRecovery(afterReload);
      expect(again.getSnapshot().text).toBe("C\n\nA\n\nB");
      expect(readComposerDraftRevisionId("root")).toBeUndefined();
    } finally {
      again.dispose();
    }
  } finally {
    storage.releaseContinuation();
    original.dispose();
    replacement.dispose();
    reopened?.close();
    if (!reopened) storage.close();
  }
});

test("recovery skips its unchanged loaded A while another source retains C", async () => {
  const storage = new MutationOutboxIndexedDB();
  await setup(false, storage);
  const queuedRecord = await queued(storage, "B");
  const revisionA = writeComposerDraft("root", { text: "A", skillNames: [] });
  if (!revisionA) throw new Error("missing draft A revision");
  const sourceA = createTestComposerSource("root");
  await editQueuedMutation(queuedRecord.clientMutationId, "root");
  const seededSecond = await storage.updateRecoveryInput(
    queuedRecord.clientMutationId,
    [{ type: "text", text: "A\n\nB" }],
    [],
    "A\n\nB",
    [],
    [],
    revisionA,
    0,
  );
  expect(seededSecond).toBeDefined();
  const committed = await storage.getRecovery(queuedRecord.clientMutationId);
  if (!committed) throw new Error("missing committed recovery fixture");
  expect(committed.composerText).toBe("A\n\nB");
  expect(committed.localDraftRevisionId).toBe(revisionA);

  const sourceB = createTestComposerSource("root");
  sourceB.editor.write("C", 1);
  const revisionC = readComposerDraftRevisionId("root");
  if (!revisionC || revisionC === revisionA) throw new Error("missing distinct draft C revision");
  expect(sourceA.getSnapshot().text).toBe("A");
  expect(sourceA.continuity.draftEditRevisionRef.current).toBe(0);

  try {
    await expect(sourceA.activateRecovery(committed)).resolves.toBe(true);
    expect(sourceA.getSnapshot().text).toBe("A\n\nB");
    await sourceA.queueRecoveryPersistence(queuedRecord.clientMutationId, "A\n\nB", [], []);
    const consolidated = await storage.getRecovery(queuedRecord.clientMutationId);
    expect(consolidated?.composerText).toBe("A\n\nB");
    expect(consolidated?.localDraftRevisionId).toBe(revisionA);
    expect(sourceB.getSnapshot().text).toBe("C");
    expect(readDraft("root")).toBe("C");
    expect(readComposerDraftRevisionId("root")).toBe(revisionC);
  } finally {
    sourceA.dispose();
    sourceB.dispose();
    storage.close();
  }
});

test("detached recovery persistence cannot stamp or clear another source's draft", async () => {
  const storage = new CommitThenPauseRecoveryStorage();
  await setup(false, storage);
  const queuedRecord = await queued(storage, "B");
  const sourceA = createTestComposerSource("root");
  sourceA.editor.write("A", 1);
  sourceA.persistDraft("A");
  const revisionA = readComposerDraftRevisionId("root");
  if (!revisionA) throw new Error("missing draft A revision");
  await editQueuedMutation(queuedRecord.clientMutationId, "root");
  const claimed = await storage.getRecovery(queuedRecord.clientMutationId);
  if (!claimed) throw new Error("missing claimed recovery fixture");
  storage.skipNextPause();
  const unbind = sourceA.bindEditor({ read: () => sourceA.editor.read(), write: () => {} });
  await sourceA.activateRecovery(claimed);
  unbind();

  const sourceB = createTestComposerSource("root");
  sourceB.editor.write("C", 1);
  sourceB.persistDraft("C");
  const revisionC = readComposerDraftRevisionId("root");
  if (!revisionC || revisionC === revisionA) throw new Error("missing draft C revision");
  storage.armPause();
  sourceA.editText("A\n\nB revised by A");

  try {
    await storage.committed;
    const committed = await storage.getRecovery(queuedRecord.clientMutationId);
    if (!committed) throw new Error("missing detached recovery commit");
    expect(committed.composerText).toBe("A\n\nB revised by A");
    expect(committed.localDraftRevisionId).not.toBe(revisionC);
    storage.releaseContinuation();
    await storage.continued;
    await new Promise<void>((resolve) => window.setTimeout(resolve, 0));
    expect(readDraft("root")).toBe("C");
    expect(readComposerDraftRevisionId("root")).toBe(revisionC);
  } finally {
    storage.releaseContinuation();
    sourceA.dispose();
    sourceB.dispose();
    storage.close();
  }
});

test("legacy recovery without draft provenance retains the manual merge behavior", async () => {
  const { storage } = await setup(false);
  const queuedRecord = await queued(storage, "B");
  await editQueuedMutation(queuedRecord.clientMutationId, "root");
  const legacy = await storage.getRecovery(queuedRecord.clientMutationId);
  if (!legacy) throw new Error("missing legacy recovery fixture");
  expect(legacy.localDraftRevisionId).toBeUndefined();
  const source = createTestComposerSource("root");
  source.editor.write("A", 1);
  source.persistDraft("A");

  try {
    await expect(source.activateRecovery(legacy)).resolves.toBe(true);
    expect(source.getSnapshot().text).toBe("A\n\nB");
  } finally {
    source.dispose();
    storage.close();
  }
});

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
  const editor = screen.getByRole("textbox", { name: "Message" });
  const recoveredLabel = source.getSnapshot().text.indexOf("/review");
  selectEditorText(editor, recoveredLabel, recoveredLabel + "/review".length);
  await user.keyboard("{Backspace}");
  await flushPendingTurnsProjectionForTests();
  expect(source.getSnapshot().skillNames).toEqual(["draft"]);
  expect(source.getSnapshot().text).toBe("current [image 1] /draft\n\noriginal [image 2] ");
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
      receipt: appliedReceipt(params.clientMutationId, "thread-a"),
    };
  });
  fake.on("turn/interrupt", (params) => ({
    receipt: appliedReceipt(params.clientMutationId, "thread-a"),
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
        text: "current (attached image 1: current.png) /draft\n\noriginal (attached image 2: original.png) revised ",
      },
      { type: "image", mediaType: "image/png", data: "BAUG", name: "current.png" },
      { type: "image", mediaType: "image/png", data: "AQID", name: "original.png" },
      { type: "skill", name: "draft" },
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
    // The press's own continuation is fire-and-forget (QueueStrip's handler),
    // and its edit mutation publishes a projection refresh plus the activation
    // state before the held recovery write reaches its gate. A projection flush
    // here would wait on this test's own held completion and could not settle,
    // so the gate wait itself owns the act scope: restored composer text is not
    // evidence that those earlier publications finished.
    await act(async () => {
      await hold.reached;
    });
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
        receipt: appliedReceipt(params.clientMutationId, "thread-a"),
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
  // The Send's settlement completes durable writes whose queued completion
  // notifications start the pending-turns projection's final refresh. Under
  // load such a notification can still be queued when the last await above
  // returns, and the refresh it starts then publishes its snapshot outside
  // act at teardown - the five QueueStrip plus three Composer not-wrapped-in-
  // act warnings this test flaked on in whole-suite runs. Hop the macrotask
  // queue empty so every queued notification has started its refresh, then
  // flush the projection inside act.
  await outlastEmptyFlushRoundForTests();
  await flushPendingTurnsProjectionForTests();
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

test("recorded literal survives Edit beside a current image and Send", async () => {
  const { storage, fake } = await setup();
  const record = await queued(storage, "literal [image 1]");
  const source = createTestComposerSource("root");
  source.editor.write("current [image 1]", 17);
  source.persistDraft("current [image 1]");
  source.attachments.setState({
    items: [{ marker: 1, name: "current.png", mediaType: "image/png", data: "AQID", pending: false }],
  });
  mount(fake, source);
  await flushPendingTurnsProjectionForTests();
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await waitFor(() =>
    expect(source.getSnapshot()).toMatchObject({
      activeRecoveryId: record.clientMutationId,
      recoveryActivating: false,
    }),
  );
  const deliveredInputs: InputItem[][] = [];
  fake.on("turn/start", (params) => {
    deliveredInputs.push(params.input ?? []);
    expect(params.clientMutationId).not.toBe(record.clientMutationId);
    return {
      turn: { id: "literal-revision", status: "inProgress", itemsView: "full", items: [] },
      receipt: appliedReceipt(params.clientMutationId, "thread-a"),
    };
  });
  await act(async () => {
    fake.emitStateChange("ready");
    fake.emitReady();
  });
  await user.click(screen.getByRole("button", { name: "Send" }));
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(deliveredInputs).toHaveLength(1));
  expect(deliveredInputs).toEqual([
    [
      { type: "text", text: "current (attached image 2: current.png)\n\nliteral [image 1]" },
      { type: "image", mediaType: "image/png", data: "AQID", name: "current.png" },
    ],
  ]);
  expect(await storage.getRecovery(record.clientMutationId)).toBeUndefined();
  expect(await storage.getOutbox(record.clientMutationId)).toBeUndefined();
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

test("recovery activation during a paused resend preserves the secondary image recovery", async () => {
  const barrier = deferred<{ stopEpoch: number }>();
  const barrierReached = deferred<void>();
  class PausedBarrierStorage extends MutationOutboxIndexedDB {
    override async readStopEpoch(ref: string): Promise<number> {
      barrierReached.resolve();
      await barrier.promise;
      return super.readStopEpoch(ref);
    }
  }
  const storage = new PausedBarrierStorage();
  const { fake } = await setup(false, storage);
  const first = await queued(storage, "first recovery");
  const second = await queued(storage, "image recovery");
  await editQueuedMutation(first.clientMutationId, "root");
  await editQueuedMutation(second.clientMutationId, "root");
  const imageRecovery = await storage.getRecovery(second.clientMutationId);
  if (!imageRecovery) throw new Error("missing image recovery fixture");
  const seededImage = await storage.updateRecoveryInput(
    second.clientMutationId,
    [
      { type: "text", text: "image recovery [image 1]" },
      { type: "image", mediaType: "image/png", data: "AQID", name: "secondary.png" },
    ],
    [
      {
        presentationId: "secondary-image",
        marker: 1,
        mediaType: "image/png",
        blob: new Blob([Uint8Array.of(1, 2, 3)]),
        name: "secondary.png",
      },
    ],
    "image recovery [image 1]",
    undefined,
    undefined,
    undefined,
    0,
  );
  expect(seededImage).toBeDefined();
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

  try {
    const sendClick = user.click(screen.getByRole("button", { name: "Send" }));
    await barrierReached.promise;
    const editRecovery = screen.getByRole("button", { name: "Edit message" });
    const recoveryB = await storage.getRecovery(second.clientMutationId);
    if (!recoveryB) throw new Error("missing image recovery while resend is paused");
    expect(recoveryB.attachments[0]).toMatchObject({ marker: 1, name: "secondary.png" });
    expect(recoveryB.payload.input).toContainEqual({
      type: "image",
      mediaType: "image/png",
      data: "AQID",
      name: "secondary.png",
    });
    expect(editRecovery.getAttribute("aria-disabled")).toBe("true");
    let activationSettled = false;
    await act(async () => {
      const activation = source.activateRecovery(recoveryB).finally(() => {
        activationSettled = true;
      });
      await Promise.resolve();
      expect(await storage.getRecovery(second.clientMutationId)).toBeDefined();
      expect(activationSettled).toBe(false);
      barrier.resolve({ stopEpoch: 0 });
      await sendClick;
      await activation;
    });
    storage.close();
    const reopened = new MutationOutboxIndexedDB();
    try {
      const recovery = await reopened.getRecovery(second.clientMutationId);
      expect(recovery).toBeDefined();
      expect(recovery?.attachments[0]).toMatchObject({ marker: 1, name: "secondary.png" });
      expect(recovery?.payload.input).toContainEqual({
        type: "image",
        mediaType: "image/png",
        data: "AQID",
        name: "secondary.png",
      });
    } finally {
      reopened.close();
    }
  } finally {
    barrier.resolve({ stopEpoch: 0 });
    source.dispose();
    storage.close();
  }
});

test("stale recovery persistence refusal preserves the unsent composition under a fresh owner", async () => {
  class RefusedRecoveryStorage extends MutationOutboxIndexedDB {
    readonly refused = deferred<void>();
    readonly releaseRefusal = deferred<void>();

    override async updateRecoveryInput(
      ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
    ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
      const saved = await super.updateRecoveryInput(...args);
      if (!saved) {
        this.refused.resolve();
        await this.releaseRefusal.promise;
      }
      return saved;
    }
  }
  const storage = new RefusedRecoveryStorage();
  const { fake } = await setup(false, storage);
  const text = "keep this [image 1]";
  const input: InputItem[] = [
    { type: "text", text },
    { type: "image", mediaType: "image/png", data: "AQID", name: "recovered.png" },
  ];
  const queuedRecord = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/queue",
    attachments: [
      {
        presentationId: "recovered-presentation",
        marker: 1,
        name: "recovered.png",
        mediaType: "image/png",
        blob: new Blob([Uint8Array.of(1, 2, 3)], { type: "image/png" }),
      },
    ],
    composerText: text,
    payload: { input },
    optimisticDisplay: { input },
  });
  await editQueuedMutation(queuedRecord.clientMutationId, "root");
  const source = createTestComposerSource("root");
  const peer = new MutationOutboxIndexedDB();
  const user = userEvent.setup();
  const recoveryPersistenceSettled = deferred<void>();
  const queueRecoveryPersistence = source.queueRecoveryPersistence.bind(source);
  vi.spyOn(source, "queueRecoveryPersistence").mockImplementation(async (...args) => {
    const saved = await queueRecoveryPersistence(...args);
    if (!saved) recoveryPersistenceSettled.resolve();
    return saved;
  });

  try {
    mount(fake, source);
    await flushPendingTurnsProjectionForTests();
    await user.click(screen.getByRole("button", { name: "Edit message" }));
    await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(queuedRecord.clientMutationId));
    await screen.findByRole("button", { name: "Remove recovered.png" });
    const submittedText = source.getSnapshot().text;
    // Restored attachments do not mean the recovery projection has finished publishing.
    await flushPendingTurnsProjectionForTests();
    const peerRecovery = await peer.getRecovery(queuedRecord.clientMutationId);
    if (!peerRecovery) throw new Error("missing peer recovery fixture");

    // A second tab commits a newer durable composition after this Composer
    // loaded its owner revision. Its own recovery composition stays available.
    let competingWrite: Awaited<ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]>>;
    await act(async () => {
      competingWrite = await peer.updateRecoveryInput(
        queuedRecord.clientMutationId,
        [{ type: "text", text: "newer composition in another tab" }],
        undefined,
        "newer composition in another tab",
        undefined,
        undefined,
        undefined,
        undefined,
        { [queuedRecord.clientMutationId]: recoveryCompositionRevision(peerRecovery) },
      );
    });
    expect(competingWrite).toBeDefined();
    await flushPendingTurnsProjectionForTests();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Send" }));
      await storage.refused.promise;
      storage.releaseRefusal.resolve();
      await recoveryPersistenceSettled.promise;
    });
    await flushPendingTurnsProjectionForTests();
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    expect(source.getSnapshot().text).toBe(submittedText);
    await waitFor(() => expect(screen.getByRole("button", { name: "Send" }).hasAttribute("disabled")).toBe(false));
    expect((screen.getByRole("textbox", { name: "Message" }) as HTMLElement).textContent).toBe(submittedText);
    expect(screen.getByRole("button", { name: "Remove recovered.png" })).toBeDefined();
    const preserved = (await peer.listRecovery("root")).find(
      (record) => record.clientMutationId !== queuedRecord.clientMutationId,
    );
    expect(preserved).toBeDefined();
    expect(source.getSnapshot().activeRecoveryId).toBe(preserved?.clientMutationId);
    // The fresh recovery row owns the composition; no draft carries its markers.
    expect(readDraft("root") ?? "").toBe("");
    // Enabled Send does not mean the recovery projection has finished publishing.
    await flushPendingTurnsProjectionForTests();
    expect(await peer.getRecovery(preserved?.clientMutationId ?? "")).toMatchObject({
      composerText: submittedText,
      payload: {
        input: [
          { type: "text", text: submittedText },
          { type: "image", mediaType: "image/png", data: "AQID", name: "recovered.png" },
        ],
      },
      attachments: [expect.objectContaining({ marker: 1, name: "recovered.png" })],
    });
    expect(await peer.getRecovery(queuedRecord.clientMutationId)).toMatchObject({
      composerText: "newer composition in another tab",
      payload: { input: [{ type: "text", text: "newer composition in another tab" }] },
      attachments: [expect.objectContaining({ name: "recovered.png" })],
    });
    act(() => cleanup());
    // Reload: a fresh composer restores the preserved images from its own row.
    source.dispose();
    const replacement = createTestComposerSource("root");
    try {
      mount(fake, replacement);
      await waitFor(() => expect(replacement.getSnapshot().freshRecoveryRef).toBe("root"));
      await waitFor(() => expect(replacement.getSnapshot().activeRecoveryId).toBe(preserved?.clientMutationId));
      expect(replacement.getSnapshot().text).toBe(submittedText);
      expect(replacement.attachments.getState().items).toEqual([
        expect.objectContaining({ marker: 1, name: "recovered.png", mediaType: "image/png", data: "AQID" }),
      ]);
    } finally {
      act(() => replacement.dispose());
    }
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await flushPendingTurnsProjectionForTests();
  } finally {
    act(() => source.dispose());
    peer.close();
    storage.close();
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

async function editQueuedMessageIntoCompact() {
  const { storage, fake } = await setup(false);
  const record = await queued(storage, "queued work");
  const source = createTestComposerSource("root");
  mount(fake, source);
  await flushPendingTurnsProjectionForTests();
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(record.clientMutationId));
  await flushPendingTurnsProjectionForTests();
  const editor = screen.getByRole("textbox", { name: "Message" });
  selectEditorText(editor, 0, source.getSnapshot().text.length);
  await user.keyboard("/compact");
  await flushPendingTurnsProjectionForTests();
  return { storage, fake, record, source, user };
}

const messageMutationMethods = new Set([
  "turn/start",
  "turn/steer",
  "turn/queue",
  "turn/drainAsSteer",
  "turn/promoteQueuedAsSteer",
]);

test("successful built-in completes edited recovery without sending a message or resurrecting it", async () => {
  const { storage, fake, record, source, user } = await editQueuedMessageIntoCompact();
  const finishCommand = deferred<void>();
  fake.on("thread/compact/start", async () => {
    await finishCommand.promise;
    return {};
  });

  await user.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/compact/start")).toHaveLength(1));
  await act(async () => finishCommand.resolve());
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(source.getSnapshot()).toMatchObject({ text: "", activeRecoveryId: null }));
  await waitFor(async () => expect(await storage.getRecovery(record.clientMutationId)).toBeUndefined());
  expect(fake.calls.filter((call) => messageMutationMethods.has(call.method))).toEqual([]);

  cleanup();
  source.dispose();
  const reopenedStorage = new MutationOutboxIndexedDB();
  const reopenedSource = mount(fake).source;
  await flushPendingTurnsProjectionForTests();
  expect(reopenedSource.getSnapshot()).toMatchObject({ text: "", activeRecoveryId: null });
  expect(await reopenedStorage.getRecovery(record.clientMutationId)).toBeUndefined();
  expect(await reopenedStorage.listRecovery("root")).toEqual([]);
  expect(fake.calls.filter((call) => messageMutationMethods.has(call.method))).toEqual([]);
  cleanup();
  reopenedSource.dispose();
  reopenedStorage.close();
});

test("failed built-in keeps edited recovery and reopen does not retry the command", async () => {
  const { storage, fake, record, source, user } = await editQueuedMessageIntoCompact();
  const failCommand = deferred<void>();
  fake.on("thread/compact/start", async () => {
    await failCommand.promise;
    throw new Error("scripted compact failure");
  });

  await user.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/compact/start")).toHaveLength(1));
  await act(async () => failCommand.resolve());
  await waitFor(async () =>
    expect((await storage.getRecovery(record.clientMutationId))?.composerText).toBe("/compact"),
  );
  expect(source.getSnapshot()).toMatchObject({ text: "/compact", activeRecoveryId: record.clientMutationId });
  expect(fake.calls.filter((call) => messageMutationMethods.has(call.method))).toEqual([]);

  cleanup();
  source.dispose();
  const reopenedStorage = new MutationOutboxIndexedDB();
  const reopenedSource = mount(fake).source;
  await flushPendingTurnsProjectionForTests();
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await waitFor(() => expect(reopenedSource.getSnapshot().text).toBe("/compact"));
  expect(reopenedSource.getSnapshot()).toMatchObject({ text: "/compact", activeRecoveryId: record.clientMutationId });
  expect(fake.calls.filter((call) => call.method === "thread/compact/start")).toHaveLength(1);
  expect(fake.calls.filter((call) => messageMutationMethods.has(call.method))).toEqual([]);
  cleanup();
  reopenedSource.dispose();
  reopenedStorage.close();
});

test("late built-in success does not consume newer edited recovery work", async () => {
  const { storage, fake, record, source, user } = await editQueuedMessageIntoCompact();
  const commandStarted = deferred<void>();
  const finishCommand = deferred<void>();
  fake.on("thread/compact/start", async () => {
    commandStarted.resolve();
    await finishCommand.promise;
    return {};
  });

  await user.click(screen.getByRole("button", { name: "Send" }));
  await commandStarted.promise;
  const editor = screen.getByRole("textbox", { name: "Message" });
  selectEditorText(editor, 0, source.getSnapshot().text.length);
  await user.keyboard("newer work");
  await waitFor(async () =>
    expect((await storage.getRecovery(record.clientMutationId))?.composerText).toBe("newer work"),
  );
  await act(async () => finishCommand.resolve());
  await waitFor(() => expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(false));
  await flushPendingTurnsProjectionForTests();
  await waitFor(() =>
    expect(source.getSnapshot()).toMatchObject({ text: "newer work", activeRecoveryId: record.clientMutationId }),
  );
  expect((await storage.getRecovery(record.clientMutationId))?.composerText).toBe("newer work");

  await act(async () => {
    cleanup();
    source.dispose();
  });
  const reopenedStorage = new MutationOutboxIndexedDB();
  const reopenedSource = mount(fake).source;
  await flushPendingTurnsProjectionForTests();
  await user.click(screen.getByRole("button", { name: "Edit message" }));
  await waitFor(() => expect(reopenedSource.getSnapshot().text).toBe("newer work"));
  // Restored text does not mean the recovery projection has finished publishing.
  await flushPendingTurnsProjectionForTests();
  expect(reopenedSource.getSnapshot()).toMatchObject({ text: "newer work", activeRecoveryId: record.clientMutationId });
  expect((await reopenedStorage.getRecovery(record.clientMutationId))?.composerText).toBe("newer work");
  expect(fake.calls.filter((call) => messageMutationMethods.has(call.method))).toEqual([]);
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  await act(async () => {
    cleanup();
    reopenedSource.dispose();
  });
  reopenedStorage.close();
});

test("transient cleanup failure retires completed command recovery without replay", async () => {
  const { storage, fake, record, source, user } = await editQueuedMessageIntoCompact();
  fake.on("thread/compact/start", () => ({}));
  const originalDelete = IDBObjectStore.prototype.delete;
  let cleanupFailed = false;
  const failRecoveryDelete = vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementation(function (
    this: IDBObjectStore,
    ...args: unknown[]
  ) {
    if (this.name === "recovery" && !cleanupFailed) {
      cleanupFailed = true;
      throw new DOMException("scripted cleanup failure", "QuotaExceededError");
    }
    return Reflect.apply(originalDelete, this, args);
  });
  try {
    await user.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/compact/start")).toHaveLength(1));
    await waitFor(() => expect(cleanupFailed).toBe(true));
    failRecoveryDelete.mockRestore();
    await flushPendingTurnsProjectionForTests();
    expect(source.getSnapshot().text).toBe("");
    expect(fake.calls.filter((call) => call.method === "thread/compact/start")).toHaveLength(1);
    expect(fake.calls.filter((call) => messageMutationMethods.has(call.method))).toEqual([]);
    await waitFor(async () => expect(await storage.getRecovery(record.clientMutationId)).toBeUndefined());
  } finally {
    failRecoveryDelete.mockRestore();
    cleanup();
    source.dispose();
    storage.close();
  }
});

async function emptyRecoverySource() {
  const { storage } = await setup();
  const record = await queued(storage, "completed command");
  const recovery = await editQueuedMutation(record.clientMutationId, "root");
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource("root");
  source.bindEditor({
    read: () => ({ text: "", cursor: 0, selection: { start: 0, end: 0 } }),
    write: () => {},
  });
  source.setActiveRecoveryId(recovery);
  return { storage, record, source };
}

test("repeated cleanup failures retry at bounded intervals until storage recovers", async () => {
  const { storage, record, source } = await emptyRecoverySource();
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const originalDelete = IDBObjectStore.prototype.delete;
  let deleteAttempts = 0;
  const failThreeDeletes = vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementation(function (
    this: IDBObjectStore,
    ...args: unknown[]
  ) {
    if (this.name === "recovery") {
      deleteAttempts += 1;
      if (deleteAttempts <= 3) throw new DOMException("scripted cleanup failure", "QuotaExceededError");
    }
    return Reflect.apply(originalDelete, this, args);
  });
  try {
    await act(async () => source.queueRecoveryPersistence(record.clientMutationId, "", [], []).catch(() => undefined));
    expect(deleteAttempts).toBe(1);
    expect(vi.getTimerCount()).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(249);
    });
    await flushPendingTurnsProjectionForTests();
    expect(deleteAttempts).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushPendingTurnsProjectionForTests();
    await storage.getRecovery(record.clientMutationId);
    expect(deleteAttempts).toBe(2);
    expect(vi.getTimerCount()).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500);
    });
    await flushPendingTurnsProjectionForTests();
    await storage.getRecovery(record.clientMutationId);
    expect(deleteAttempts).toBe(3);
    expect(vi.getTimerCount()).toBe(1);
    await act(async () => {
      await vi.runOnlyPendingTimersAsync();
    });
    await flushPendingTurnsProjectionForTests();
    await storage.getRecovery(record.clientMutationId);
    expect(deleteAttempts).toBe(4);
    expect(vi.getTimerCount()).toBe(0);
    expect(await storage.getRecovery(record.clientMutationId)).toBeUndefined();
    expect(source.getSnapshot().activeRecoveryId).toBeNull();
  } finally {
    failThreeDeletes.mockRestore();
    vi.useRealTimers();
    source.dispose();
    storage.close();
  }
});

test("typing before cleanup retry preserves newer work and the recovery row", async () => {
  const { storage, record, source } = await emptyRecoverySource();
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const originalDelete = IDBObjectStore.prototype.delete;
  let deleteAttempts = 0;
  const failFirstDelete = vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementation(function (
    this: IDBObjectStore,
    ...args: unknown[]
  ) {
    if (this.name === "recovery" && args[0] === record.clientMutationId) {
      deleteAttempts += 1;
      if (deleteAttempts === 1) throw new DOMException("scripted cleanup failure", "QuotaExceededError");
    }
    return Reflect.apply(originalDelete, this, args);
  });
  try {
    await act(async () => source.queueRecoveryPersistence(record.clientMutationId, "", [], []).catch(() => undefined));
    expect(vi.getTimerCount()).toBe(1);
    await act(async () => source.editText("newer work"));
    expect(vi.getTimerCount()).toBe(0);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    await flushPendingTurnsProjectionForTests();
    expect(deleteAttempts).toBe(1);
    expect(source.getSnapshot().text).toBe("newer work");
    expect((await storage.getRecovery(record.clientMutationId))?.composerText).toBe("completed command");
  } finally {
    failFirstDelete.mockRestore();
    vi.useRealTimers();
    source.dispose();
    storage.close();
  }
});

test("disposing the source cancels its delayed recovery cleanup retry", async () => {
  const { storage, record, source } = await emptyRecoverySource();
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const originalDelete = IDBObjectStore.prototype.delete;
  let deleteAttempts = 0;
  const failFirstDelete = vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementation(function (
    this: IDBObjectStore,
    ...args: unknown[]
  ) {
    if (this.name === "recovery") {
      deleteAttempts += 1;
      if (deleteAttempts === 1) throw new DOMException("scripted cleanup failure", "QuotaExceededError");
    }
    return Reflect.apply(originalDelete, this, args);
  });
  try {
    await act(async () => source.queueRecoveryPersistence(record.clientMutationId, "", [], []).catch(() => undefined));
    expect(vi.getTimerCount()).toBe(1);
    await act(async () => source.dispose());
    expect(vi.getTimerCount()).toBe(0);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    await flushPendingTurnsProjectionForTests();
    expect(deleteAttempts).toBe(1);
    expect((await storage.getRecovery(record.clientMutationId))?.composerText).toBe("completed command");
  } finally {
    failFirstDelete.mockRestore();
    vi.useRealTimers();
    source.dispose();
    storage.close();
  }
});

test("replacing the recovery owner cancels the prior owner's cleanup retry", async () => {
  const { storage, record, source } = await emptyRecoverySource();
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const originalDelete = IDBObjectStore.prototype.delete;
  let deleteAttempts = 0;
  const failFirstDelete = vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementation(function (
    this: IDBObjectStore,
    ...args: unknown[]
  ) {
    if (this.name === "recovery") {
      deleteAttempts += 1;
      if (deleteAttempts === 1) throw new DOMException("scripted cleanup failure", "QuotaExceededError");
    }
    return Reflect.apply(originalDelete, this, args);
  });
  try {
    await act(async () => source.queueRecoveryPersistence(record.clientMutationId, "", [], []).catch(() => undefined));
    expect(vi.getTimerCount()).toBe(1);
    const replacement = await queued(storage, "replacement owner");
    const replacementRecovery = await editQueuedMutation(replacement.clientMutationId, "root");
    if (!replacementRecovery) throw new Error("missing replacement recovery fixture");
    await act(async () => source.setActiveRecoveryId(replacementRecovery));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    await flushPendingTurnsProjectionForTests();
    expect(deleteAttempts).toBe(1);
    expect((await storage.getRecovery(record.clientMutationId))?.composerText).toBe("completed command");
  } finally {
    failFirstDelete.mockRestore();
    vi.useRealTimers();
    source.dispose();
    storage.close();
  }
});

test("the recovery-operation lease disables Send until the Edit claim releases it", async () => {
  const storage = new MutationOutboxIndexedDB();
  const { fake } = await setup(false, storage);
  const { source } = mount(fake);
  await flushPendingTurnsProjectionForTests();
  act(() => source.editText("A"));
  let release: (() => void) | null = null;
  act(() => {
    release = source.tryAcquireRecoveryOperation();
  });
  if (!release) throw new Error("missing recovery-operation lease");
  await waitFor(() => expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(true));
  await act(async () => release?.());
  await waitFor(() => expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(false));
});

test("an ordinary send cannot start while a recovery Edit claim is settling", async () => {
  const storage = new MutationOutboxIndexedDB();
  const { fake } = await setup(false, storage);
  const record = await queued(storage, "recovered while editing");
  const { source } = mount(fake);
  await flushPendingTurnsProjectionForTests();
  // The user has a draft in the composer while claiming the queued edit.
  act(() => source.editText("A"));
  const hold = holdNextWriteTransaction(["recovery"]);
  try {
    // A local queue row's Edit first claims the row (outbox -> recovery); the
    // activation has not started yet, so only the shared lease stops Send.
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Edit message" }));
      await hold.reached;
    });
    // The local queue row's Edit holds the shared lease for the whole claim
    // window, before activation begins.
    expect(source.getSnapshot().recoveryOperationBusy).toBe(true);
    expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Send" }));
      await Promise.resolve();
    });
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
    await act(async () => hold.release());
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(source.getSnapshot().activeRecoveryId).toBe(record.clientMutationId));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Send" }));
    });
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1));
    act(() => cleanup());
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await flushPendingTurnsProjectionForTests();
  } finally {
    hold.release();
  }
});
