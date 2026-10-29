import type { InputItem } from "@evener/appwire-client";
import { act, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { holdNextWriteTransaction } from "../../../stores/testing/stalledIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../../stores/threads";
import { getToasts, pushToast, resetToastStoreForTests } from "../../../widgets/toast/store";
import { createTestComposerSource } from "../testing/composerSource";
import { installControlledImageEncoding } from "../testing/imageEncoding";
import { createAttachmentOperations, useAttachments } from "./attachments/useAttachments";
import {
  draftStorageKey,
  markDraftEdited,
  readComposerDraft,
  readComposerDraftRevisionId,
  readDraftRevision,
  readRecoveryPick,
  resolveRecoveryPick,
  writeComposerDraft,
} from "./draft";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  globalThis.indexedDB = new IDBFactory();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetToastStoreForTests();
});
afterEach(() => {
  resetWorkspaceStoreForTests();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function image(name = "source.png") {
  return new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function recoveryCompositionRevision(record: object): number {
  return (record as { compositionRevision?: number }).compositionRevision ?? 0;
}

test("source restoration retains mixed atom identity and inert prose", () => {
  const text = "🙂 /same /same /same";
  const commandOffset = text.indexOf("/same");
  const skillOffset = text.indexOf("/same", commandOffset + 1);
  const value = {
    text,
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command" as const, name: "same", offset: commandOffset },
      { kind: "skill" as const, name: "same", offset: skillOffset },
    ],
  };
  writeComposerDraft("root", value);
  const source = createTestComposerSource("root");
  expect(source.getSnapshot()).toMatchObject(value);
  source.persistDraft(text);
  expect(readComposerDraft("root")).toEqual(value);
});

// Dropping mentions at restore/persist would select this deliberately inert token.
test("source persistence keeps a meaningful empty mention set", () => {
  const value = { text: "/same", skillNames: [], mentions: [] };
  writeComposerDraft("root", value);
  const source = createTestComposerSource("root");
  source.persistDraft(value.text);
  expect(readComposerDraft("root")).toEqual(value);
});

// A single broad marker deletion spans both retained atoms and loses their identity.
test("detached submitted markers preserve atoms between each exact splice", () => {
  writeComposerDraft("root", {
    text: "🙂 [image 1] /same [image 2] /same [image 3] /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 13 },
      { kind: "skill", name: "same", offset: 29 },
    ],
  });
  const source = createTestComposerSource("root");
  const operations = createAttachmentOperations(source.editor, source.attachments);
  operations.replaceWithSettled(
    [1, 2, 3].map((marker) => ({
      marker,
      name: `${marker}.png`,
      mediaType: "image/png",
      data: "AQID",
      pending: false,
    })),
  );
  source.clearSubmittedAttachments(source.attachments.getState().items);
  expect(source.attachments.getState().items).toEqual([]);
  expect(readComposerDraft("root")).toEqual({
    text: "🙂  /same  /same  /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command", name: "same", offset: 4 },
      { kind: "skill", name: "same", offset: 11 },
    ],
  });
});

// Ignoring command selections in the empty-work predicate discards a real recovery.
test("detached command-only recovery persists instead of being discarded", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const input = [{ type: "command" as const, name: "same" }];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  writeComposerDraft("root", { text: "", skillNames: [], commandNames: ["same"], mentions: [] });
  const source = createTestComposerSource("root");
  source.setActiveRecoveryId(recovery);
  await source.queueRecoveryPersistence(original.clientMutationId, "", [], [], ["same"], []);
  expect(source.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
  expect(source.getSnapshot().commandNames).toEqual(["same"]);
  expect(await storage.getRecovery(original.clientMutationId)).toMatchObject({
    clientMutationId: original.clientMutationId,
    targetRef: "root",
    payload: { input },
    composerText: "",
    composerMentions: [],
  });
});

test("an autosave finishing after goal replacement cannot restore the retired lease", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const input = [{ type: "text" as const, text: "recovered" }];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource("root");
  await source.activateRecovery(recovery);

  let committed!: () => void;
  const committedWrite = new Promise<void>((resolve) => {
    committed = resolve;
  });
  let releaseWrite!: () => void;
  const writeGate = new Promise<void>((resolve) => {
    releaseWrite = resolve;
  });
  const originalUpdate = storage.updateRecoveryInput.bind(storage);
  let pauseNext = true;
  (
    storage as unknown as {
      updateRecoveryInput: typeof storage.updateRecoveryInput;
    }
  ).updateRecoveryInput = async (...args) => {
    const result = await originalUpdate(...args);
    if (pauseNext) {
      pauseNext = false;
      committed();
      await writeGate;
    }
    return result;
  };

  try {
    const saving = source.queueRecoveryPersistence(original.clientMutationId, "mid-flight", [], []);
    await committedWrite;
    // Goal replacement retires the recovery owner while that write is in
    // flight, then the user keeps typing an ordinary goal draft.
    source.setActiveRecoveryId(null);
    source.persistDraft("goal draft");
    releaseWrite();
    await saving;
    expect(source.continuity.recoveryOwnerLeaseRef.current).toBeNull();
    source.writeDraft("goal draft typed", true);
    expect(readComposerDraft("root").text).toBe("goal draft typed");
  } finally {
    source.dispose();
  }
});

// A peer that advanced the recovery no longer owns the local images; the
// refusal must leave a durable fresh owner so a reload can restore them.
test("a stale recovery refusal preserves image-bearing work under a fresh owner", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const originalText = "keep this [image 1]";
  const input: InputItem[] = [
    { type: "text", text: originalText },
    { type: "image", mediaType: "image/png", data: "AQID", name: "recovered.png" },
  ];
  const original = await storage.enqueueIntent({
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
    composerText: originalText,
    payload: { ref: "root", input },
    optimisticDisplay: { method: "turn/queue", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "editing");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");

  const source = createTestComposerSource("root");
  const peer = new MutationOutboxIndexedDB();
  let reloaded: ReturnType<typeof createTestComposerSource> | undefined;
  try {
    await source.activateRecovery(recovery);
    const localText = "keep this [image 1] /cleanup local";
    const skillOffset = localText.indexOf("/cleanup");
    source.editText(localText);
    source.editSkillNames(["cleanup"]);
    source.updateSelectionMeta({
      text: localText,
      skillNames: ["cleanup"],
      mentions: [{ kind: "skill", name: "cleanup", offset: skillOffset }],
    });
    const localAttachments = [{ marker: 1, name: "recovered.png", mediaType: "image/png", data: "AQID" }];

    // Another tab replaces the composition this pane activated.
    const peerRecovery = await peer.getRecovery(original.clientMutationId);
    if (!peerRecovery) throw new Error("missing peer recovery fixture");
    const peerText = "newer composition in another tab";
    const newer = await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: peerText }],
      undefined,
      peerText,
      undefined,
      undefined,
      undefined,
      undefined,
      {
        [original.clientMutationId]:
          (peerRecovery as MutationRecoveryRecord & { compositionRevision?: number }).compositionRevision ?? 0,
      },
    );
    expect(newer).toBeDefined();

    const saved = await source.queueRecoveryPersistence(original.clientMutationId, localText, localAttachments, [
      "cleanup",
    ]);
    expect(saved).toBe(false);

    const preserved = (await storage.listRecovery("root")).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toBeDefined();
    expect(preserved).toMatchObject({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/queue",
      composerText: localText,
      payload: {
        input: [
          { type: "text", text: localText },
          { type: "image", mediaType: "image/png", data: "AQID", name: "recovered.png" },
          { type: "skill", name: "cleanup" },
        ],
      },
    });
    expect(preserved?.attachments).toEqual([
      expect.objectContaining({ marker: 1, name: "recovered.png", mediaType: "image/png" }),
    ]);
    // The peer's newer composition is untouched.
    expect(await storage.getRecovery(original.clientMutationId)).toMatchObject({
      composerText: peerText,
      payload: { input: [{ type: "text", text: peerText }] },
    });

    // Reload: a fresh source restores the preserved images from the fresh owner.
    await Promise.resolve();
    await source.flushRecoveryWrites();
    source.dispose();
    reloaded = createTestComposerSource("root");
    const reloadedRow = preserved ? await storage.getRecovery(preserved.clientMutationId) : undefined;
    if (!reloadedRow) throw new Error("missing preserved recovery fixture");
    await expect(reloaded.activateRecovery(reloadedRow)).resolves.toBe(true);
    expect(reloaded.getSnapshot()).toMatchObject({ text: localText, skillNames: ["cleanup"] });
    expect(reloaded.attachments.getState().items).toEqual([
      expect.objectContaining({ marker: 1, name: "recovered.png", mediaType: "image/png", data: "AQID" }),
    ]);
  } finally {
    source.dispose();
    reloaded?.dispose();
    peer.close();
    storage.close();
  }
});

test("failed source draft write cannot acquire another source's revision token", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "recovered" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "recovered" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const sourceA = createTestComposerSource("root");
  const sourceB = createTestComposerSource("root");
  sourceB.persistDraft("C");
  const revisionC = readComposerDraftRevisionId("root");
  if (!revisionC) throw new Error("missing source B draft revision");
  const setItem = vi.spyOn(localStorage, "setItem").mockImplementation(() => {
    throw new DOMException("quota exceeded", "QuotaExceededError");
  });
  try {
    sourceA.persistDraft("A");
    expect(setItem).toHaveBeenCalledOnce();
    expect(readComposerDraftRevisionId("root")).toBe(revisionC);
    expect(readComposerDraft("root").text).toBe("C");
    setItem.mockRestore();
    const recovery = await storage.getRecovery(original.clientMutationId);
    if (!recovery) throw new Error("missing recovery fixture");
    sourceA.setActiveRecoveryId(recovery);
    await sourceA.queueRecoveryPersistence(original.clientMutationId, "A recovery", [], []);
    expect((await storage.getRecovery(original.clientMutationId))?.localDraftRevisionId).not.toBe(revisionC);
  } finally {
    setItem.mockRestore();
    sourceA.dispose();
    sourceB.dispose();
    storage.close();
  }
});

test("rebound composer adopts and consumes the exact persisted draft revision once", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const queued = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "B" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "B" }] },
  });
  await storage.transferToRecovery(queued.clientMutationId, "rejected");

  // The pane source exists before another owner writes the persisted draft.
  const source = createTestComposerSource("root");
  const detachInitialEditor = source.bindEditor({ read: () => source.editor.read(), write: () => {} });
  detachInitialEditor();
  writeComposerDraft("root", { text: "A", skillNames: [] });
  const draftRevisionId = readComposerDraftRevisionId("root");
  if (!draftRevisionId) throw new Error("missing persisted draft revision fixture");
  const unbind = source.bindEditor({ read: () => source.editor.read(), write: () => {} });

  try {
    expect(source.getSnapshot().text).toBe("A");
    const recovery = await storage.getRecovery(queued.clientMutationId);
    if (!recovery) throw new Error("missing recovery fixture");
    await source.activateRecovery(recovery);
    const consolidated = await storage.getRecovery(queued.clientMutationId);
    expect(consolidated?.composerText).toBe("A\n\nB");
    expect(consolidated?.localDraftRevisionId).toBe(draftRevisionId);

    unbind();
    source.dispose();
    const reopened = createTestComposerSource("root");
    try {
      const reopenedRecovery = await storage.getRecovery(queued.clientMutationId);
      if (!reopenedRecovery) throw new Error("missing consolidated recovery fixture");
      await reopened.activateRecovery(reopenedRecovery);
      expect(reopened.getSnapshot().text).toBe("A\n\nB");
      expect(readComposerDraftRevisionId("root")).toBeUndefined();
    } finally {
      reopened.dispose();
    }
  } finally {
    unbind();
    source.dispose();
    storage.close();
  }
});

test("rebound legacy draft is merged without claiming a missing revision", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const queued = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "B" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "B" }] },
  });
  await storage.transferToRecovery(queued.clientMutationId, "rejected");
  const source = createTestComposerSource("root");
  const detachInitialEditor = source.bindEditor({ read: () => source.editor.read(), write: () => {} });
  detachInitialEditor();
  localStorage.setItem(draftStorageKey("root"), "legacy A");
  markDraftEdited("root");
  const unbind = source.bindEditor({ read: () => source.editor.read(), write: () => {} });

  try {
    const recovery = await storage.getRecovery(queued.clientMutationId);
    if (!recovery) throw new Error("missing recovery fixture");
    await source.activateRecovery(recovery);
    const consolidated = await storage.getRecovery(queued.clientMutationId);
    expect(consolidated?.composerText).toBe("legacy A\n\nB");
    expect(consolidated?.localDraftRevisionId).toBeUndefined();
  } finally {
    unbind();
    source.dispose();
    storage.close();
  }
});

// Equal prose/skills do not authorize clearing a different command snapshot.
test("source clearing compares the submitted command selection", () => {
  writeComposerDraft("root", {
    text: "/same",
    skillNames: [],
    commandNames: ["same"],
    mentions: [{ kind: "command", name: "same", offset: 0 }],
  });
  const source = createTestComposerSource("root");
  source.clearIfUnchanged("/same", 0, readDraftRevision("root"), [], ["different"]);
  expect(source.getSnapshot().text).toBe("/same");
  expect(readComposerDraft("root").commandNames).toEqual(["same"]);
});

// Detached marker writes must patch the full atom value, not infer chips from text.
test.each(["success", "failure"] as const)("detached mixed atoms survive image decode %s", async (outcome) => {
  const value = {
    text: "🙂 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command" as const, name: "same", offset: 3 },
      { kind: "skill" as const, name: "same", offset: 9 },
    ],
  };
  writeComposerDraft("root", value);
  const source = createTestComposerSource("root");
  const encoding = installControlledImageEncoding();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
  view.unmount();
  source.editor.write("🙂 /same /same /same[image 1] newer", 33);
  await act(async () => {
    if (outcome === "success") await encoding.resolve();
    else await encoding.reject();
  });
  await waitFor(() => expect(source.attachments.getState().items.some((item) => item.pending)).toBe(false));
  expect(readComposerDraft("root")).toEqual({
    ...value,
    text: outcome === "success" ? "🙂 /same /same /same[image 1] newer" : "🙂 /same /same /same newer",
  });
  expect(source.getSnapshot()).toMatchObject({ commandNames: ["same"], mentions: value.mentions });
});

test.each(["success", "failure"] as const)(
  "an encode %s settles its original source while detached",
  async (outcome) => {
    const encoding = installControlledImageEncoding();
    const source = createTestComposerSource("root");
    source.editor.write("original ", 9);
    const view = renderHook(() => useAttachments(source.editor, source.attachments));
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    const child = createTestComposerSource("child");
    child.editor.write("child draft", 11);
    view.unmount();
    await act(async () => {
      if (outcome === "success") await encoding.resolve();
      else await encoding.reject();
    });
    await waitFor(() => expect(source.attachments.getState().items.some((item) => item.pending)).toBe(false));
    expect(readComposerDraft("child")).toEqual({ text: "child draft", skillNames: [] });
    if (outcome === "success") {
      expect(source.attachments.getState().items).toEqual([
        { marker: 1, name: "source.png", mediaType: "image/png", pending: false, data: "AQID", width: 8, height: 4 },
      ]);
      expect(readComposerDraft("root")).toEqual({ text: "original [image 1]", skillNames: [] });
      expect(getToasts()).toEqual([]);
    } else {
      expect(source.attachments.getState().items).toEqual([]);
      expect(readComposerDraft("root")).toEqual({ text: "original ", skillNames: [] });
      expect(getToasts().map((toast) => ({ kind: toast.kind, text: toast.text }))).toEqual([
        { kind: "error", text: "source.png (image decode failed)" },
      ]);
    }
  },
);

test.each(["success", "failure"] as const)("disposing a source invalidates its pending encode %s", async (outcome) => {
  const encoding = installControlledImageEncoding();
  const source = createTestComposerSource("root");
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
  const generation = source.attachments.getState().generationRef.current;
  view.unmount();
  source.dispose();
  const replacement = createTestComposerSource("root");
  replacement.editor.write("replacement [image 1]", 21);
  expect(source.alive).toBe(false);
  expect(source.attachments.getState().generationRef.current).toBeGreaterThan(generation);
  await act(async () => {
    if (outcome === "success") await encoding.resolve();
    else await encoding.reject();
  });
  await waitFor(() => expect(source.attachments.getState().items).toEqual([]));
  expect(replacement.attachments.getState().items).toEqual([]);
  expect(readComposerDraft("root")).toEqual({ text: "replacement [image 1]", skillNames: [] });
  expect(getToasts()).toEqual([]);
});

test.each([
  { action: "close", outcome: "success" },
  { action: "close", outcome: "failure" },
  { action: "reset", outcome: "success" },
  { action: "reset", outcome: "failure" },
])("pane $action ignores late encode $outcome after ID reuse", async ({ action, outcome }) => {
  const encoding = installControlledImageEncoding();
  const pane: OpenPaneRecord = { id: "session-1", type: "session", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const source = lifetime.composer;
  if (!source) throw new Error("session pane has no source composer");
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
  const generation = source.attachments.getState().generationRef.current;
  view.unmount();
  if (action === "close") workspaceStore.getState().closePane(pane.id);
  else resetWorkspaceStoreForTests();
  const replacementPane: OpenPaneRecord = { id: "session-1", type: "session", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [replacementPane], focusedPaneId: replacementPane.id });
  const replacement = conversationPaneLifetime(replacementPane);
  replacement.composer?.editor.write("replacement [image 1]", 21);
  expect(lifetime.alive).toBe(false);
  expect(replacement.serial).not.toBe(lifetime.serial);
  expect(source.attachments.getState().generationRef.current).toBeGreaterThan(generation);
  await act(async () => {
    if (outcome === "success") await encoding.resolve();
    else await encoding.reject();
  });
  expect(source.attachments.getState().items).toEqual([]);
  expect(replacement.composer?.attachments.getState().items).toEqual([]);
  expect(readComposerDraft("root")).toEqual({ text: "replacement [image 1]", skillNames: [] });
  expect(getToasts()).toEqual([]);
});

test.each(["success", "failure"] as const)(
  "an old detached encode %s cannot claim a newer same-ref draft",
  async (outcome) => {
    const encoding = installControlledImageEncoding();
    const source = createTestComposerSource("root");
    const view = renderHook(() => useAttachments(source.editor, source.attachments));
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    view.unmount();
    const newer = createTestComposerSource("root");
    newer.editor.write("foreign [image 1] /cleanup", 27);
    newer.editSkillNames(["cleanup"]);
    newer.persistDraft("foreign [image 1] /cleanup");
    await act(async () => {
      if (outcome === "success") await encoding.resolve();
      else await encoding.reject();
    });
    await waitFor(() => expect(source.attachments.getState().items.some((item) => item.pending)).toBe(false));
    expect(newer.getSnapshot().text).toBe("foreign [image 1] /cleanup");
    expect(readComposerDraft("root")).toEqual({ text: "foreign [image 1] /cleanup", skillNames: ["cleanup"] });
    expect(getToasts().map((toast) => toast.text)).toEqual(
      outcome === "failure" ? ["source.png (image decode failed)"] : [],
    );
  },
);

test("successful recovery writes follow a peer retarget", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:write-retarget-old";
  const resumed = "local:write-retarget-middle";
  const resumedAgain = "local:write-retarget-new";
  const original = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(ref);
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    await peer.retargetOutbox(ref, resumed, { threadId: "thread-mid", instanceId: "instance-mid" });
    expect(await source.queueRecoveryPersistence(original.clientMutationId, "first newer", [], [])).toBe(true);
    const pickUnder = (conversationRef: string) =>
      resolveRecoveryPick(conversationRef, (rowId) => storage.getRecovery(rowId)).then(
        (resolved) => resolved?.row.clientMutationId,
      );
    // The pick is keyed by the row, and the retarget preserved its id while
    // moving its durable target: resolving under the resumed ref finds it.
    expect(await pickUnder(resumed)).toBe(original.clientMutationId);

    // Another tab retargets the row again; the next successful write follows.
    await peer.retargetOutbox(resumed, resumedAgain, { threadId: "thread-new", instanceId: "instance-new" });
    expect(await source.queueRecoveryPersistence(original.clientMutationId, "second newer", [], [])).toBe(true);
    expect(await pickUnder(resumedAgain)).toBe(original.clientMutationId);
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});
test("a retargeted autosave carries the selected row past an older peer row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:retarget-selection-old";
  const resumed = "local:retarget-selection-new";
  const queue = async (text: string) => {
    const record = await storage.enqueueIntent({
      targetRef: ref,
      method: "turn/start",
      payload: { ref, input: [{ type: "text", text }] },
      attachments: [],
      composerText: text,
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
    });
    await storage.transferToRecovery(record.clientMutationId, "rejected");
    return record;
  };
  const olderPeer = await queue("older peer work");
  const selected = await queue("selected local work");
  const recovery = await storage.getRecovery(selected.clientMutationId);
  if (!recovery) throw new Error("missing selected recovery fixture");
  const source = createTestComposerSource(ref);
  const peer = new MutationOutboxIndexedDB();
  let reloaded: ReturnType<typeof createTestComposerSource> | undefined;
  try {
    await source.activateRecovery(recovery);
    await peer.retargetOutbox(ref, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    expect(await source.queueRecoveryPersistence(selected.clientMutationId, "selected local work", [], [])).toBe(true);

    const pickUnderResumed = () =>
      resolveRecoveryPick(resumed, (rowId) => storage.getRecovery(rowId)).then(
        (resolved) => resolved?.row.clientMutationId,
      );
    expect(await pickUnderResumed()).toBe(selected.clientMutationId);
    expect(await pickUnderResumed()).not.toBe(olderPeer.clientMutationId);
    reloaded = createTestComposerSource(resumed);
    expect(await pickUnderResumed()).toBe(selected.clientMutationId);
  } finally {
    source.dispose();
    reloaded?.dispose();
    peer.close();
    storage.close();
  }
});
// Retirement on the explicit path (a send consumed the row) releases the
// retiring row's own pick entry only; picks this tab holds for other rows -
// recorded by other panes' explicit actions - survive it.
test("retiring a lease clears only its own stored selection", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:retire-own-selection";
  const original = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "row composition" }] },
    attachments: [],
    composerText: "row composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "row composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const otherRow = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "other row" }] },
    attachments: [],
    composerText: "other row",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "other row" }] },
  });
  await storage.transferToRecovery(otherRow.clientMutationId, "rejected");
  const otherRecovery = await storage.getRecovery(otherRow.clientMutationId);
  if (!otherRecovery) throw new Error("missing other recovery fixture");
  const source = createTestComposerSource(ref);
  const other = createTestComposerSource(ref);
  try {
    source.setActiveRecoveryId(recovery);
    expect(readRecoveryPick(original.clientMutationId)).toBeDefined();
    // Another pane in this tab explicitly picks a different row.
    other.setActiveRecoveryId(otherRecovery);
    expect(readRecoveryPick(otherRow.clientMutationId)).toBeDefined();
    // The send consumed the source's row: retirement releases only that row's
    // own entry.
    source.setActiveRecoveryId(null);
    expect(source.getSnapshot().activeRecoveryId).toBeNull();
    expect(readRecoveryPick(original.clientMutationId)).toBeUndefined();
    expect(readRecoveryPick(otherRow.clientMutationId)).toBeDefined();
  } finally {
    source.dispose();
    other.dispose();
    storage.close();
  }
});

// Automatic background restoration is cut from this PR: a preservation that
// commits after the pane closed records no pick and touches no registry
// entry - the preserved row is durable and an explicit action reaches it,
// while the draft carries the composition back on reopen.
test("a preservation that commits after disposal records no pick", async () => {
  class HeldPreservationStorage extends MutationOutboxIndexedDB {
    readonly reached = deferred<void>();
    readonly release = deferred<void>();

    override async preserveRecoveryComposition(
      ...args: Parameters<MutationOutboxIndexedDB["preserveRecoveryComposition"]>
    ): ReturnType<MutationOutboxIndexedDB["preserveRecoveryComposition"]> {
      this.reached.resolve();
      await this.release.promise;
      return super.preserveRecoveryComposition(...args);
    }
  }
  const storage = new HeldPreservationStorage();
  setMutationStorageForTests(storage);
  const ref = "local:disposed-preservation-absence";
  const original = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "stale composition" }] },
    attachments: [],
    composerText: "stale composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "stale composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(ref);
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const peerRecovery = await peer.getRecovery(original.clientMutationId);
    if (!peerRecovery) throw new Error("missing peer recovery fixture");
    await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: "peer composition" }],
      [],
      "peer composition",
      [],
      [],
      undefined,
      undefined,
      { [original.clientMutationId]: recoveryCompositionRevision(peerRecovery) },
    );

    source.editText("local preserved work");
    const saving = source.queueRecoveryPersistence(original.clientMutationId, "local preserved work", [], []);
    await storage.reached.promise;
    // The pane closes while the preservation transaction is still open.
    source.dispose();
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(ref)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ targetRef: ref, composerText: "local preserved work" });
    // The settle wrote no pick and cleared none: the registry names the row
    // exactly as the user's explicit activation left it.
    expect(readRecoveryPick(original.clientMutationId)).toBeDefined();
    expect(readRecoveryPick(preserved?.clientMutationId ?? "")).toBeUndefined();
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// Automatic background restoration is cut from this PR: an asynchronous
// preservation settle must not touch the pick registry at all. A newer
// explicit pick another pane recorded for the displaced row stands exactly
// as written, and the preserved row carries no pick of the settle's making.
test("a background preservation settle never overrides a newer explicit pick", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:settle-absence";
  const original = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "row composition" }] },
    attachments: [],
    composerText: "row composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "row composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(ref);
  const other = createTestComposerSource(ref);
  try {
    source.setActiveRecoveryId(recovery);
    expect(readRecoveryPick(original.clientMutationId)).toBeDefined();
    // Another tab consumes the row outright, so the pending write goes stale.
    expect(await storage.discardRecovery(original.clientMutationId)).toBe(true);
    const hold = holdNextWriteTransaction(["recovery"]);

    source.editText("local preserved work");
    const saving = source.queueRecoveryPersistence(original.clientMutationId, "local preserved work", [], []);
    await hold.reached;
    // Meanwhile another pane in this tab explicitly re-picks the displaced
    // row - the user's newest choice, made after the write began.
    other.setActiveRecoveryId(recovery);
    expect(readRecoveryPick(original.clientMutationId)).toMatchObject({ background: false });

    hold.release();
    await saving;

    const preserved = (await storage.listRecovery(ref))[0];
    if (!preserved) throw new Error("missing preserved row");
    expect(preserved.clientMutationId).not.toBe(original.clientMutationId);
    // The settle touched no picks: the user's pick stands exactly as the
    // other pane wrote it, and the preserved row carries none.
    expect(readRecoveryPick(original.clientMutationId)).toMatchObject({ background: false });
    expect(readRecoveryPick(preserved?.clientMutationId ?? "")).toBeUndefined();
  } finally {
    source.dispose();
    other.dispose();
    storage.close();
  }
});

// A peer retarget moves the selected row while its autosave is in flight,
// then the pane closes before the committed write returns. The row's durable
// target now names the resumed ref: resolving this tab's pick under that ref
// must find the row even though the source is gone, or a reopen of that ref
// restores an older peer row instead of this row.
test("a retargeted write that settles after disposal still carries the selection", async () => {
  class HeldWriteStorage extends MutationOutboxIndexedDB {
    holdNextWrite = false;
    readonly reached = deferred<void>();
    readonly release = deferred<void>();

    override async updateRecoveryInput(
      ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
    ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
      const result = await super.updateRecoveryInput(...args);
      if (this.holdNextWrite) {
        this.holdNextWrite = false;
        this.reached.resolve();
        await this.release.promise;
      }
      return result;
    }
  }
  const storage = new HeldWriteStorage();
  setMutationStorageForTests(storage);
  const ref = "local:disposed-retarget-write-old";
  const resumed = "local:disposed-retarget-write-new";
  const original = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "selected composition" }] },
    attachments: [],
    composerText: "selected composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "selected composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(ref);
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    expect(readRecoveryPick(original.clientMutationId)).toBeDefined();
    await peer.retargetOutbox(ref, resumed, { threadId: "thread-new", instanceId: "instance-new" });

    storage.holdNextWrite = true;
    source.editText("newer selected work");
    const saving = source.queueRecoveryPersistence(original.clientMutationId, "newer selected work", [], []);
    await storage.reached.promise;
    // The pane closes while the committed write has not settled back yet.
    source.dispose();
    storage.release.resolve();
    expect(await saving).toBe(true);

    expect((await resolveRecoveryPick(resumed, (rowId) => storage.getRecovery(rowId)))?.row.clientMutationId).toBe(
      original.clientMutationId,
    );
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});
