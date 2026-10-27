import type { InputItem } from "@evener/appwire-client";
import { act, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { lazy } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { registerPaneForTests } from "../../../shell/paneRegistry";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../../stores/threads";
import { getToasts, pushToast, resetToastStoreForTests } from "../../../widgets/toast/store";
import { createTestComposerSource } from "../testing/composerSource";
import { installControlledImageEncoding } from "../testing/imageEncoding";
import { createAttachmentOperations, useAttachments } from "./attachments/useAttachments";
import {
  composerSelectionScope,
  draftStorageKey,
  markDraftEdited,
  readComposerDraft,
  readComposerDraftRevisionId,
  readDraftRevision,
  readRecoverySelection,
  readRefComposerSeat,
  writeComposerDraft,
} from "./draft";
import {
  preserveRecoveryPendingTurn,
  resetPendingTurnsStoreForTests,
  useRecoveryEntries,
} from "./queue/pendingTurnsStore";

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

// The user kept working while a refused save awaited storage, then closed the
// pane. Disposal clears the live attachment store, so the disposal snapshot -
// not the pre-await one - is the composition that must survive.
test("a pane disposed during a delayed refusal preserves its newest composition", async () => {
  class HeldRefusalStorage extends MutationOutboxIndexedDB {
    holdNextRefusal = true;
    readonly reached = deferred<void>();
    readonly release = deferred<void>();

    override async updateRecoveryInput(
      ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
    ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
      const result = await super.updateRecoveryInput(...args);
      if (result === undefined && this.holdNextRefusal) {
        this.holdNextRefusal = false;
        this.reached.resolve();
        await this.release.promise;
      }
      return result;
    }
  }
  const storage = new HeldRefusalStorage();
  setMutationStorageForTests(storage);
  const ref = "local:disposal-latest";
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
  const source = createTestComposerSource(ref, undefined, "pane-a");
  try {
    await source.activateRecovery(recovery);
    const operations = createAttachmentOperations(source.editor, source.attachments);
    source.editText("start [image 1]");
    const peer = new MutationOutboxIndexedDB();
    try {
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
    } finally {
      peer.close();
    }

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "start [image 1]", [], []);
    await storage.reached.promise;
    // Newer typing and a settled attachment arrive while the refusal is held.
    source.editText("latest [image 1]");
    operations.replaceWithSettled([
      { marker: 1, name: "latest.png", mediaType: "image/png", data: "AQID", pending: false },
    ]);
    source.dispose();
    storage.release.resolve();
    await saving;
    const preserved = (await storage.listRecovery(ref)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({
      composerText: "latest [image 1]",
      payload: {
        input: [
          { type: "text", text: "latest [image 1]" },
          { type: "image", mediaType: "image/png", data: "AQID", name: "latest.png" },
        ],
      },
      attachments: [expect.objectContaining({ marker: 1, name: "latest.png" })],
    });
    expect(readRecoverySelection(ref, source.recoverySelectionOwnerId)).toBe(preserved?.clientMutationId);
  } finally {
    source.dispose();
    storage.close();
  }
});

// A preservation can commit under a resumed ref after the resumed view is
// already mounted. That view must learn about the row without a remount.
test("preservation under a resumed ref refreshes an already-mounted view of it", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const oldRef = "local:resume-refresh-old";
  const resumed = "local:resume-refresh-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const view = renderHook(() => useRecoveryEntries(resumed));
  try {
    await waitFor(() => expect(view.result.current).toEqual([]));
    // The base names no surviving row, so preservation commits a fresh one
    // under the resumed identity while the resumed view is mounted.
    let preserved: Awaited<ReturnType<typeof preserveRecoveryPendingTurn>>;
    await act(async () => {
      preserved = await preserveRecoveryPendingTurn(
        { ...recovery, clientMutationId: "absent-row", targetRef: resumed },
        oldRef,
        "local composition",
        [],
        [],
        [],
        [],
        "composition-owner",
      );
    });
    expect(preserved?.targetRef).toBe(resumed);
    await waitFor(() =>
      expect(view.result.current.map((record) => record.clientMutationId)).toContain(preserved?.clientMutationId),
    );
  } finally {
    view.unmount();
    storage.close();
  }
});

// retargetOutbox moved the row to the resumed ref while preservation waited.
// Another pane then consumed that row, so the destination cannot be read from
// the row itself: it must resolve independently of the row's survival.
test("preservation after a consumed retargeted row lands under the resumed ref", async () => {
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
  const oldRef = "local:retarget-consumed-old";
  const resumed = "local:retarget-consumed-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const localText = "local work across a consumed retarget";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    await storage.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    // Another pane consumes the moved row before preservation continues.
    expect(await peer.discardRecovery(original.clientMutationId)).toBe(true);
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(resumed)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ targetRef: resumed, composerText: localText });
    expect(await storage.listRecovery(oldRef)).toEqual([]);
    expect(readRecoverySelection(resumed, source.recoverySelectionOwnerId)).toBe(preserved?.clientMutationId);
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// The retargeting adapter is not the one holding the preservation. Its
// in-memory history cannot help: the destination must live in shared storage
// beside the rows the retarget moved.
test("preservation resolves a retarget performed by another storage instance", async () => {
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
  const oldRef = "local:peer-retarget-old";
  const resumed = "local:peer-retarget-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const localText = "local work across a peer retarget";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    // The other tab retargets and consumes the moved row while this tab's
    // preservation is held. This adapter never saw the retarget.
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    expect(await peer.discardRecovery(original.clientMutationId)).toBe(true);
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(resumed)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ targetRef: resumed, composerText: localText });
    expect(await storage.listRecovery(oldRef)).toEqual([]);
    expect(readRecoverySelection(resumed, source.recoverySelectionOwnerId)).toBe(preserved?.clientMutationId);
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// The replacement composer under the resumed ref has already selected another
// row while this pane's preservation was in flight. Adoption must not write
// its preserved row over that newer destination choice.
test("a delayed preservation cannot replace a newer destination selection", async () => {
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
  const oldRef = "local:adoption-old";
  const resumed = "local:adoption-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const replacement = createTestComposerSource(resumed, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  let reloaded: ReturnType<typeof createTestComposerSource> | undefined;
  try {
    await source.activateRecovery(recovery);
    const localText = "local work that must not win";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    // Preservation now lands under the resumed ref; the replacement composer
    // selects its own newer row there before preservation returns.
    await storage.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const newer = {
      ...recovery,
      clientMutationId: "replacement-row",
      targetRef: resumed,
      composerText: "replacement work",
    } as MutationRecoveryRecord;
    replacement.setActiveRecoveryId(newer);
    expect(readRecoverySelection(resumed, replacement.recoverySelectionOwnerId)).toBe("replacement-row");
    storage.release.resolve();
    await saving;

    expect(readRecoverySelection(resumed, replacement.recoverySelectionOwnerId)).toBe("replacement-row");
    // Reload under the resumed ref restores the replacement's newer choice.
    reloaded = createTestComposerSource(resumed, undefined, "pane-a");
    expect(readRecoverySelection(resumed, reloaded.recoverySelectionOwnerId)).toBe("replacement-row");
  } finally {
    source.dispose();
    replacement.dispose();
    reloaded?.dispose();
    peer.close();
    storage.close();
  }
});

// Adoption must not overwrite a replacement composer that has newer edits.
test("a delayed preservation leaves newer replacement edits alone", async () => {
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
  const oldRef = "local:edited-old";
  const resumed = "local:edited-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const replacement = createTestComposerSource(resumed, undefined, "pane-a");
  let reloaded: ReturnType<typeof createTestComposerSource> | undefined;
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const localText = "local preserved composition";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const retargeted = await storage.getRecovery(original.clientMutationId);
    if (!retargeted) throw new Error("missing retargeted recovery fixture");
    replacement.setActiveRecoveryId(retargeted);
    replacement.editText("newer replacement work");
    expect(replacement.ownsStaleRecoveryWithNewerEdits(resumed, original.clientMutationId)).toBe(true);
    // Its own autosave persists the newer work on the stale row.
    await replacement.queueRecoveryPersistence(original.clientMutationId, "newer replacement work", [], []);
    storage.release.resolve();
    await saving;

    expect(replacement.getSnapshot().text).toBe("newer replacement work");
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    // The persisted selection still names the row holding that newer work, so
    // a reload restores it instead of the older preserved composition.
    expect(readRecoverySelection(resumed, replacement.recoverySelectionOwnerId)).toBe(original.clientMutationId);
    reloaded = createTestComposerSource(resumed, undefined, "pane-a");
    expect(readRecoverySelection(resumed, reloaded.recoverySelectionOwnerId)).toBe(original.clientMutationId);
    expect((await storage.getRecovery(original.clientMutationId))?.composerText).toBe("newer replacement work");
  } finally {
    source.dispose();
    replacement.dispose();
    reloaded?.dispose();
    peer.close();
    storage.close();
  }
});

// Draft storage is shared by panes and tabs. Adoption must clear only the
// exact revision the preserved composition incorporated, never a newer
// ordinary draft another composer wrote while preservation was waiting.
test("adoption keeps another composer's newer draft for the ref", async () => {
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
  const oldRef = "local:draft-old";
  const resumed = "local:draft-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const replacement = createTestComposerSource(resumed, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    source.editText("local preserved composition");
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "local preserved composition", [], []);
    await storage.reached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const retargeted = await storage.getRecovery(original.clientMutationId);
    if (!retargeted) throw new Error("missing retargeted recovery fixture");
    await replacement.activateRecovery(retargeted);
    // Another composer writes an ordinary draft for the same ref after the
    // preserved composition's own revision was captured.
    writeComposerDraft(resumed, { text: "another composer's draft", skillNames: [] });
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(resumed)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toBeDefined();
    expect(readRecoverySelection(resumed, replacement.recoverySelectionOwnerId)).toBe(preserved?.clientMutationId);
    expect(replacement.getSnapshot().text).toBe("local preserved composition");
    expect(readComposerDraft(resumed).text).toBe("another composer's draft");
    // Reload reads the surviving draft, not an empty composer.
    replacement.dispose();
    const reloaded = createTestComposerSource(resumed, undefined, "pane-a");
    try {
      expect(reloaded.getSnapshot().text).toBe("another composer's draft");
    } finally {
      reloaded.dispose();
    }
  } finally {
    source.dispose();
    replacement.dispose();
    peer.close();
    storage.close();
  }
});

// An explicit activation merges this composer's newer draft into the stale
// row. Delayed adoption must not replace that merged composition with the
// older preserved one.
test("explicit activation keeps newer draft work over delayed preservation", async () => {
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
  const oldRef = "local:activate-old";
  const resumed = "local:activate-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const replacement = createTestComposerSource(resumed, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const preservedText = "older preserved composition";
    source.editText(preservedText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, preservedText, [], []);
    await storage.reached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const retargeted = await storage.getRecovery(original.clientMutationId);
    if (!retargeted) throw new Error("missing retargeted recovery fixture");
    // The replacement composer types first, then explicitly activates the
    // stale row: activation merges that newer draft.
    replacement.editText("replacement newer draft");
    await expect(replacement.activateRecovery(retargeted)).resolves.toBe(true);
    storage.release.resolve();
    await saving;

    expect(replacement.getSnapshot().text).toContain("replacement newer draft");
    expect(replacement.getSnapshot().text).not.toBe("older preserved composition");
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(readRecoverySelection(resumed, replacement.recoverySelectionOwnerId)).toBe(original.clientMutationId);
    expect((await storage.getRecovery(original.clientMutationId))?.composerText).toContain("replacement newer draft");
  } finally {
    source.dispose();
    replacement.dispose();
    peer.close();
    storage.close();
  }
});

// Typing while an explicit activation awaits storage must also count as newer
// work. The activation merged it into its post-await composition, so delayed
// preservation cannot replace it with the older preserved row.
test("typing during activation survives delayed preservation", async () => {
  class HeldActivationStorage extends MutationOutboxIndexedDB {
    readonly preservationReached = deferred<void>();
    readonly releasePreservation = deferred<void>();
    readonly activationReached = deferred<void>();
    readonly releaseActivation = deferred<void>();
    holdActivation = false;

    override async preserveRecoveryComposition(
      ...args: Parameters<MutationOutboxIndexedDB["preserveRecoveryComposition"]>
    ): ReturnType<MutationOutboxIndexedDB["preserveRecoveryComposition"]> {
      this.preservationReached.resolve();
      await this.releasePreservation.promise;
      return super.preserveRecoveryComposition(...args);
    }

    override async updateRecoveryInput(
      ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
    ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
      const result = await super.updateRecoveryInput(...args);
      if (this.holdActivation) {
        this.holdActivation = false;
        this.activationReached.resolve();
        await this.releaseActivation.promise;
      }
      return result;
    }
  }
  const storage = new HeldActivationStorage();
  setMutationStorageForTests(storage);
  const oldRef = "local:activation-typing-old";
  const resumed = "local:activation-typing-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const replacement = createTestComposerSource(resumed, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    source.editText("older preserved composition");
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "older preserved composition", [], []);
    await storage.preservationReached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const retargeted = await storage.getRecovery(original.clientMutationId);
    if (!retargeted) throw new Error("missing retargeted recovery fixture");

    storage.holdActivation = true;
    const activating = replacement.activateRecovery(retargeted);
    await storage.activationReached.promise;
    replacement.editText("typed during activation");
    storage.releaseActivation.resolve();
    await expect(activating).resolves.toBe(true);
    storage.releasePreservation.resolve();
    await saving;

    expect(replacement.getSnapshot().text).toContain("typed during activation");
    expect(replacement.getSnapshot().text).not.toBe("older preserved composition");
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(readRecoverySelection(resumed, replacement.recoverySelectionOwnerId)).toBe(original.clientMutationId);
  } finally {
    storage.releaseActivation.resolve();
    storage.releasePreservation.resolve();
    source.dispose();
    replacement.dispose();
    peer.close();
    storage.close();
  }
});

// Adoption must not replace the composition an explicit activation is
// currently building. The activation's await leaves its stale lease and edit
// epoch looking unchanged, but its merged work is already in progress.
test("preservation cannot replace an explicit activation in progress", async () => {
  class HeldActivationStorage extends MutationOutboxIndexedDB {
    readonly preservationReached = deferred<void>();
    readonly releasePreservation = deferred<void>();
    readonly activationReached = deferred<void>();
    readonly releaseActivation = deferred<void>();
    holdActivation = false;

    override async preserveRecoveryComposition(
      ...args: Parameters<MutationOutboxIndexedDB["preserveRecoveryComposition"]>
    ): ReturnType<MutationOutboxIndexedDB["preserveRecoveryComposition"]> {
      this.preservationReached.resolve();
      await this.releasePreservation.promise;
      return super.preserveRecoveryComposition(...args);
    }

    override async updateRecoveryInput(
      ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
    ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
      const result = await super.updateRecoveryInput(...args);
      if (this.holdActivation) {
        this.holdActivation = false;
        this.activationReached.resolve();
        await this.releaseActivation.promise;
      }
      return result;
    }
  }
  const storage = new HeldActivationStorage();
  setMutationStorageForTests(storage);
  const oldRef = "local:activation-held-old";
  const resumed = "local:activation-held-new";
  const queue = async (targetRef: string, text: string) => {
    const record = await storage.enqueueIntent({
      targetRef,
      method: "turn/start",
      payload: { ref: targetRef, input: [{ type: "text", text }] },
      attachments: [],
      composerText: text,
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
    });
    await storage.transferToRecovery(record.clientMutationId, "rejected");
    return record;
  };
  const original = await queue(oldRef, "old composition");
  const activationTarget = await queue(resumed, "activation row work");
  const recovery = await storage.getRecovery(original.clientMutationId);
  const activationRecovery = await storage.getRecovery(activationTarget.clientMutationId);
  if (!recovery || !activationRecovery) throw new Error("missing recovery fixtures");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  writeComposerDraft(resumed, { text: "replacement visible work", skillNames: [] });
  const replacement = createTestComposerSource(resumed, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    source.editText("older preserved composition");
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "older preserved composition", [], []);
    await storage.preservationReached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const retargeted = await storage.getRecovery(original.clientMutationId);
    if (!retargeted) throw new Error("missing retargeted recovery fixture");

    replacement.setActiveRecoveryId(retargeted);
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    storage.holdActivation = true;
    const activating = replacement.activateRecovery(activationRecovery);
    await storage.activationReached.promise;
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    storage.releasePreservation.resolve();
    await saving;
    expect(replacement.getSnapshot().text).not.toBe("older preserved composition");

    storage.releaseActivation.resolve();
    await expect(activating).resolves.toBe(true);
    expect(replacement.getSnapshot().text).toContain("activation row work");
    expect(replacement.getSnapshot().text).not.toContain("older preserved composition");
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(readRecoverySelection(resumed, replacement.recoverySelectionOwnerId)).toBe(original.clientMutationId);
  } finally {
    storage.releaseActivation.resolve();
    storage.releasePreservation.resolve();
    source.dispose();
    replacement.dispose();
    peer.close();
    storage.close();
  }
});

// The originating pane can close before its preservation finishes. The
// destination selection and the resumed ref's seat mapping must still be
// recorded, or a mobile reconstruction allocates a fresh seat.
test("preservation after disposal records the resumed seat mapping", async () => {
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
  const oldRef = "local:disposed-seat-old";
  const resumed = "local:disposed-seat-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const localText = "local work across disposal";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    source.dispose();
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(resumed)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ targetRef: resumed, composerText: localText });
    expect(readRefComposerSeat(resumed)).toBe("pane-a");

    // Mobile reconstruction of the resumed route finds the retained seat.
    registerPaneForTests({ id: "session", title: () => "Session", component: lazy(() => new Promise(() => {})) });
    resetWorkspaceStoreForTests();
    const paneId = workspaceStore.getState().openPane("session", { ref: resumed });
    const pane = workspaceStore.getState().panes.find((record) => record.id === paneId);
    expect(pane?.composerSeatId).toBe("pane-a");
    expect(readRecoverySelection(resumed, composerSelectionScope("pane-a"))).toBe(preserved?.clientMutationId);
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// A peer retarget does not advance the recovery's composition revision, so a
// later autosave can succeed without activating anything. The target ref must
// follow the durable owner the write returned.
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
  const source = createTestComposerSource(ref, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    await peer.retargetOutbox(ref, resumed, { threadId: "thread-mid", instanceId: "instance-mid" });
    expect(await source.queueRecoveryPersistence(original.clientMutationId, "first newer", [], [])).toBe(true);
    expect(readRefComposerSeat(resumed)).toBe("pane-a");

    // Another tab retargets the row again; the next successful write follows.
    await peer.retargetOutbox(resumed, resumedAgain, { threadId: "thread-new", instanceId: "instance-new" });
    expect(await source.queueRecoveryPersistence(original.clientMutationId, "second newer", [], [])).toBe(true);
    expect(readRefComposerSeat(resumedAgain)).toBe("pane-a");
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// Two rejected rows can move with one retarget. After a successful autosave
// under the resumed ref, reload must select this pane's row, not the older
// peer row that also moved.
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
  const source = createTestComposerSource(ref, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  let reloaded: ReturnType<typeof createTestComposerSource> | undefined;
  try {
    await source.activateRecovery(recovery);
    await peer.retargetOutbox(ref, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    expect(await source.queueRecoveryPersistence(selected.clientMutationId, "selected local work", [], [])).toBe(true);

    expect(readRecoverySelection(resumed, source.recoverySelectionOwnerId)).toBe(selected.clientMutationId);
    expect(readRecoverySelection(resumed, source.recoverySelectionOwnerId)).not.toBe(olderPeer.clientMutationId);
    reloaded = createTestComposerSource(resumed, undefined, "pane-a");
    expect(readRecoverySelection(resumed, reloaded.recoverySelectionOwnerId)).toBe(selected.clientMutationId);
  } finally {
    source.dispose();
    reloaded?.dispose();
    peer.close();
    storage.close();
  }
});

// A replacement's saved edit is newer than the composition preservation is
// about to restore, even after its composer closes.
test("a disposed replacement's saved edit keeps its selection", async () => {
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
  const oldRef = "local:disposed-edit-old";
  const resumed = "local:disposed-edit-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const replacement = createTestComposerSource(resumed, undefined, "pane-a");
  let reloaded: ReturnType<typeof createTestComposerSource> | undefined;
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const localText = "local preserved composition";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const retargeted = await storage.getRecovery(original.clientMutationId);
    if (!retargeted) throw new Error("missing retargeted recovery fixture");
    replacement.setActiveRecoveryId(retargeted);
    replacement.editText("newer replacement work");
    await replacement.queueRecoveryPersistence(original.clientMutationId, "newer replacement work", [], []);
    replacement.dispose();
    storage.release.resolve();
    await saving;

    expect(readRecoverySelection(resumed, source.recoverySelectionOwnerId)).toBe(original.clientMutationId);
    reloaded = createTestComposerSource(resumed, undefined, "pane-a");
    expect(readRecoverySelection(resumed, reloaded.recoverySelectionOwnerId)).toBe(original.clientMutationId);
    expect((await storage.getRecovery(original.clientMutationId))?.composerText).toBe("newer replacement work");
  } finally {
    storage.release.resolve();
    source.dispose();
    replacement.dispose();
    reloaded?.dispose();
    peer.close();
    storage.close();
  }
});

// Reopening the replacement before the older preservation settles must not
// retire the provenance that says this row holds newer work.
test("a reopened replacement keeps its newer edit over delayed preservation", async () => {
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
  const oldRef = "local:reopened-edit-old";
  const resumed = "local:reopened-edit-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const replacement = createTestComposerSource(resumed, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  let reopened: ReturnType<typeof createTestComposerSource> | undefined;
  let reloaded: ReturnType<typeof createTestComposerSource> | undefined;
  try {
    await source.activateRecovery(recovery);
    const localText = "older preserved composition";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const retargeted = await storage.getRecovery(original.clientMutationId);
    if (!retargeted) throw new Error("missing retargeted recovery fixture");
    replacement.setActiveRecoveryId(retargeted);
    replacement.editText("newer replacement work");
    await replacement.queueRecoveryPersistence(original.clientMutationId, "newer replacement work", [], []);
    replacement.dispose();
    writeComposerDraft(resumed, { text: "", skillNames: [] });

    // A new source restores the same row while the older preservation waits.
    reopened = createTestComposerSource(resumed, undefined, "pane-a");
    expect(reopened.getSnapshot().text).toBe("");
    const reopenedRecovery = await storage.getRecovery(original.clientMutationId);
    if (!reopenedRecovery) throw new Error("missing reopened recovery fixture");
    reopened.updateText("newer replacement work");
    reopened.setActiveRecoveryId(reopenedRecovery);
    expect(reopened.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(readRecoverySelection(resumed, reopened.recoverySelectionOwnerId)).toBe(original.clientMutationId);

    storage.release.resolve();
    await saving;
    const preserved = (await storage.listRecovery(resumed)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ composerText: "older preserved composition" });
    expect(reopened.getSnapshot().text).toBe("newer replacement work");
    expect(readRecoverySelection(resumed, reopened.recoverySelectionOwnerId)).toBe(original.clientMutationId);
    reloaded = createTestComposerSource(resumed, undefined, "pane-a");
    expect(readRecoverySelection(resumed, reloaded.recoverySelectionOwnerId)).toBe(original.clientMutationId);
  } finally {
    storage.release.resolve();
    source.dispose();
    replacement.dispose();
    reopened?.dispose();
    reloaded?.dispose();
    peer.close();
    storage.close();
  }
});

// Another tab may consume the source row before it retargets the ref, and the
// ref may resume again afterwards. No surviving recovery row can carry the
// destination, so ref forwarding itself must persist every hop.
test("preservation follows retarget forwarding after the row was consumed first", async () => {
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
  const oldRef = "local:forward-old";
  const middleRef = "local:forward-middle";
  const resumed = "local:forward-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const localText = "local work across forwarded resumes";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    // Consume the row first, then resume twice. Neither retarget can record a
    // surviving recovery row.
    expect(await peer.discardRecovery(original.clientMutationId)).toBe(true);
    await peer.retargetOutbox(oldRef, middleRef, { threadId: "thread-mid", instanceId: "instance-mid" });
    await peer.retargetOutbox(middleRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(resumed)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ targetRef: resumed, composerText: localText });
    expect(await storage.listRecovery(oldRef)).toEqual([]);
    expect(await storage.listRecovery(middleRef)).toEqual([]);
    expect(readRecoverySelection(resumed, source.recoverySelectionOwnerId)).toBe(preserved?.clientMutationId);
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// A row-recorded destination is only the first hop. After the row is consumed,
// a later resume forwards that destination again; preservation must follow it
// instead of stranding the composition under the intermediate ref.
test("preservation follows forwarding past a row-recorded destination", async () => {
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
  const oldRef = "local:row-hop-old";
  const middleRef = "local:row-hop-middle";
  const resumed = "local:row-hop-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "old composition" }] },
    attachments: [],
    composerText: "old composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "old composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const localText = "local work across two retargets";
    source.editText(localText);
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, localText, [], []);
    await storage.reached.promise;
    // A -> B records the row destination B while the row survives.
    await peer.retargetOutbox(oldRef, middleRef, { threadId: "thread-mid", instanceId: "instance-mid" });
    expect(await peer.discardRecovery(original.clientMutationId)).toBe(true);
    // B -> C forwards the ref after the row is gone.
    await peer.retargetOutbox(middleRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(resumed)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ targetRef: resumed, composerText: localText });
    expect(await storage.listRecovery(middleRef)).toEqual([]);
    expect(readRecoverySelection(resumed, source.recoverySelectionOwnerId)).toBe(preserved?.clientMutationId);
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});
