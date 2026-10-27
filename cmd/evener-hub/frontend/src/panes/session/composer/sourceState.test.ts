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
import { settleProjectionWorkForTests } from "../../../stores/projectionWork";
import * as threads from "../../../stores/threads";
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
  writeRecoverySelection,
} from "./draft";
import {
  preserveRecoveryPendingTurn,
  resetPendingTurnsStoreForTests,
  useRecoveryEntries,
} from "./queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";
import { beginRecoverySubmission } from "./sourceState";

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

// Preservation can await storage while the pane keeps working. Work typed or
// settled during that wait is captured by dispose() when the pane closes
// before the preservation settles; a disposed source can never save it itself,
// so the preserved row must be reconciled with the disposal snapshot before
// it settles - otherwise a reopen restores the composition from before the
// wait and loses those edits.
test("a pane disposed while preservation is held keeps the work it gained", async () => {
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
  const ref = "local:held-preserve";
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
    // Newer typing and a settled attachment arrive while the preservation
    // itself is held, then the pane closes before it settles.
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

// A replacement composer can be mid-send on its restored row when an older
// preservation settles. Adoption must respect the destination's submission
// guard and recovery-operation lock: swapping the lease under the awaited
// save makes the resend read the preserved row's revision under the original
// id - a spurious refusal - and replaces the composition being sent. Defer,
// then re-evaluate ownership once the operation settles.
test("adoption defers while the destination is sending its restored row", async () => {
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
  const oldRef = "local:send-defer-old";
  const resumed = "local:send-defer-new";
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "row composition", [], []);
    await storage.reached.promise;
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    const retargeted = await storage.getRecovery(original.clientMutationId);
    if (!retargeted) throw new Error("missing retargeted recovery fixture");
    await replacement.activateRecovery(retargeted);
    // The replacement presses Send on its restored row: the operation lock
    // and the submission guard are held across the awaited save.
    const releaseOperation = replacement.tryAcquireRecoveryOperation();
    if (!releaseOperation) throw new Error("operation lock unavailable");
    const releaseInFlight = beginRecoverySubmission(original.clientMutationId);
    storage.release.resolve();
    await saving;

    // The send still owns the lease and the composition it captured.
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(replacement.getSnapshot().text).toBe("peer composition");
    const preserved = (await storage.listRecovery(resumed)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toBeDefined();

    // Once the operation settles, ownership is re-evaluated and adoption lands.
    releaseInFlight();
    releaseOperation();
    await flushPendingTurnsProjectionForTests();
    expect(replacement.getSnapshot().activeRecoveryId).toBe(preserved?.clientMutationId);
    expect(replacement.getSnapshot().text).toBe("local preserved composition");
  } finally {
    source.dispose();
    replacement.dispose();
    peer.close();
    storage.close();
  }
});

// A pending attachment is newer work on the active row too: an image whose
// encoding is still running when the deferred adoption is flushed used to
// drop the adoption at the pending guard before the selection reclamation
// could run, so a reload restored the older preserved composition instead of
// the work the encoded image settled into.
test("a pending image encoding reclaims the selection for the active row", async () => {
  installLocalStorage(new MemoryStorage());
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:reclaim-encoding";
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
  const preservedIntent = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "preserved composition" }] },
    attachments: [],
    composerText: "preserved composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "preserved composition" }] },
  });
  await storage.transferToRecovery(preservedIntent.clientMutationId, "rejected");
  const preserved = await storage.getRecovery(preservedIntent.clientMutationId);
  if (!preserved) throw new Error("missing preserved fixture");
  const encoding = installControlledImageEncoding();
  const replacement = createTestComposerSource(ref, undefined, "pane-a");
  const view = renderHook(() => useAttachments(replacement.editor, replacement.attachments));
  const scope = composerSelectionScope("pane-a");
  let unmounted = false;
  try {
    act(() => {
      replacement.setActiveRecoveryId(recovery);
    });
    // What the delayed preservation published: the stored selection names
    // the preserved row while the adoption waits on the held send.
    writeRecoverySelection(ref, scope, preserved.clientMutationId);
    const releaseSend = beginRecoverySubmission(original.clientMutationId);
    void replacement.adoptPreservedSelection(ref, original.clientMutationId, preserved);
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);

    // The user starts encoding an image while the send is held.
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    expect(replacement.attachments.getState().items.some((item) => item.pending)).toBe(true);
    releaseSend();
    await flushPendingTurnsProjectionForTests();
    // The pending image dropped the adoption and reclaimed the selection
    // for the row the encoded image will settle onto.
    expect(readRecoverySelection(ref, scope)).toBe(original.clientMutationId);

    // The encode settles and autosaves onto the active row.
    await act(async () => {
      await encoding.resolve();
    });
    await flushPendingTurnsProjectionForTests();
    await flushPendingTurnsProjectionForTests();
    const saved = await storage.getRecovery(original.clientMutationId);
    expect(saved?.composerText).toContain("[image 1]");

    // A remount restores the selection and the image-carrying work, not the
    // older preserved composition.
    view.unmount();
    unmounted = true;
    replacement.dispose();
    const remounted = createTestComposerSource(ref, undefined, "pane-a");
    try {
      expect(readRecoverySelection(ref, remounted.recoverySelectionOwnerId)).toBe(original.clientMutationId);
      const restored = await storage.getRecovery(original.clientMutationId);
      if (!restored) throw new Error("missing restored row");
      expect(restored.composerText).toContain("[image 1]");
      await remounted.activateRecovery(restored);
      expect(remounted.getSnapshot().text).toContain("[image 1]");
    } finally {
      remounted.dispose();
    }
  } finally {
    if (!unmounted) view.unmount();
    replacement.dispose();
    storage.close();
  }
});

// The originating source installs its own preserved record after the
// preservation's post-commit projection waits. Another pane can consume the
// row during that wait; installing the captured snapshot would let a later
// autosave resurrect the removed content, so the originating path validates
// the durable row the same way every adoption does.
test("the originating source drops a preserved row a peer consumed during the post-commit wait", async () => {
  class HeldRefreshStorage extends MutationOutboxIndexedDB {
    hold = false;
    readonly refreshReached = deferred<void>();
    readonly releaseRefresh = deferred<void>();

    override async listRecovery(targetRef?: string): Promise<MutationRecoveryRecord[]> {
      const rows = await super.listRecovery(targetRef);
      // Hold the post-commit refresh read that first sees the preserved row.
      if (this.hold && rows.some((row) => row.clientMutationId !== original.clientMutationId)) {
        this.refreshReached.resolve();
        await this.releaseRefresh.promise;
      }
      return rows;
    }
  }
  const storage = new HeldRefreshStorage();
  setMutationStorageForTests(storage);
  const ref = "local:originating-revalidate";
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
  const source = createTestComposerSource(ref, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    // A peer advances the row so the source's save is refused into
    // preservation.
    const peerRow = await peer.getRecovery(original.clientMutationId);
    if (!peerRow) throw new Error("missing peer recovery fixture");
    await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: "peer composition" }],
      [],
      "peer composition",
      [],
      [],
      undefined,
      undefined,
      { [original.clientMutationId]: recoveryCompositionRevision(peerRow) },
    );

    storage.hold = true;
    const saving = source.queueRecoveryPersistence(original.clientMutationId, "row composition", [], []);
    await storage.refreshReached.promise;
    // The preservation committed and its post-commit refresh is held; the
    // peer consumes the fresh row before the source can install it.
    const preservedRows = (await peer.listRecovery(ref)).filter(
      (row) => row.clientMutationId !== original.clientMutationId,
    );
    expect(preservedRows).toHaveLength(1);
    const preservedRow = preservedRows[0];
    if (!preservedRow) throw new Error("missing preserved row");
    expect(await peer.discardRecovery(preservedRow.clientMutationId)).toBe(true);
    storage.releaseRefresh.resolve();
    await saving;
    await flushPendingTurnsProjectionForTests();

    // The consumed row is neither installed nor resurrected: the lease
    // retired and no fresh recovery carries the removed content.
    expect(source.getSnapshot().activeRecoveryId).toBeNull();
    expect(
      (await storage.listRecovery(ref)).filter((row) => row.clientMutationId !== original.clientMutationId),
    ).toEqual([]);
  } finally {
    storage.releaseRefresh.resolve();
    source.dispose();
    peer.close();
    storage.close();
  }
});

// A resume can retarget the committed row while preservation awaits its
// post-commit projection refreshes. The revalidation read used to filter by
// the captured target ref, treating the surviving row as consumed, so its
// selection was never published or adopted under the new ref and a reopen
// restored an older rejected peer row instead.
test("a retarget during the post-commit wait keeps the preserved row selectable under the resumed ref", async () => {
  class HeldRefreshStorage extends MutationOutboxIndexedDB {
    hold = false;
    readonly refreshReached = deferred<void>();
    readonly releaseRefresh = deferred<void>();

    override async listRecovery(targetRef?: string): Promise<MutationRecoveryRecord[]> {
      const rows = await super.listRecovery(targetRef);
      // Hold the post-commit refresh read that first sees the preserved row.
      if (
        this.hold &&
        rows.some((row) => row.clientMutationId !== original.clientMutationId && row.clientMutationId !== peerRowId)
      ) {
        this.refreshReached.resolve();
        await this.releaseRefresh.promise;
      }
      return rows;
    }
  }
  const storage = new HeldRefreshStorage();
  setMutationStorageForTests(storage);
  const oldRef = "local:retarget-revalidate-old";
  const resumed = "local:retarget-revalidate-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "row composition" }] },
    attachments: [],
    composerText: "row composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "row composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  // An older rejected peer row already lives under the resumed ref.
  const peerRowIntent = await storage.enqueueIntent({
    targetRef: resumed,
    method: "turn/start",
    payload: { ref: resumed, input: [{ type: "text", text: "older peer composition" }] },
    attachments: [],
    composerText: "older peer composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "older peer composition" }] },
  });
  await storage.transferToRecovery(peerRowIntent.clientMutationId, "rejected");
  const peerRowId = peerRowIntent.clientMutationId;
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  const scope = composerSelectionScope("pane-a");
  try {
    await source.activateRecovery(recovery);
    // A peer advances the row so the source's save is refused into
    // preservation.
    const peerCurrent = await peer.getRecovery(original.clientMutationId);
    if (!peerCurrent) throw new Error("missing peer recovery fixture");
    await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: "peer composition" }],
      [],
      "peer composition",
      [],
      [],
      undefined,
      undefined,
      { [original.clientMutationId]: recoveryCompositionRevision(peerCurrent) },
    );

    storage.hold = true;
    const saving = source.queueRecoveryPersistence(original.clientMutationId, "row composition", [], []);
    await storage.refreshReached.promise;
    // The preservation committed; a resume retargets it (and the stale row)
    // while the post-commit refresh is held.
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    storage.releaseRefresh.resolve();
    await saving;
    await flushPendingTurnsProjectionForTests();

    // The preserved row survived under the resumed ref and its selection and
    // seat were published there, so a reopen selects it - not the older
    // peer row.
    const preservedRows = (await storage.listRecovery(resumed)).filter((row) => row.clientMutationId !== peerRowId);
    const preserved = preservedRows.find((row) => row.clientMutationId !== original.clientMutationId);
    expect(preserved).toMatchObject({ targetRef: resumed, composerText: "row composition" });
    expect(readRecoverySelection(resumed, scope)).toBe(preserved?.clientMutationId);
    expect(readRefComposerSeat(resumed)).toBe("pane-a");
    expect(source.getSnapshot().activeRecoveryId).toBe(preserved?.clientMutationId);
  } finally {
    storage.releaseRefresh.resolve();
    source.dispose();
    peer.close();
    storage.close();
  }
});

// The revalidation read is the last await before the preserved record is
// published and installed. Typing, an attachment settling, and the pane
// closing during that read left the newest work only in the disposal
// snapshot - queued saves abort on the disposed source - so the disposal is
// rechecked after the read and reconciled before publication.
test("a disposal during the revalidation read reconciles the pane's newest work", async () => {
  installLocalStorage(new MemoryStorage());
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:revalidate-disposal";
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
  const encoding = installControlledImageEncoding();
  const source = createTestComposerSource(ref, undefined, "pane-a");
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  const originalRead = threads.readMutationPersistence;
  const revalidationReached = deferred<void>();
  const releaseRevalidation = deferred<void>();
  const readSpy = vi.spyOn(threads, "readMutationPersistence").mockImplementation(async (targetRef?: string) => {
    const snapshot = await originalRead(targetRef);
    if (
      targetRef === undefined &&
      snapshot.recovery.some((row) => row.clientMutationId !== original.clientMutationId)
    ) {
      revalidationReached.resolve();
      await releaseRevalidation.promise;
    }
    return snapshot;
  });
  const peer = new MutationOutboxIndexedDB();
  let unmounted = false;
  try {
    await act(async () => {
      await source.activateRecovery(recovery);
    });
    // A peer advances the row so the source's save is refused into
    // preservation.
    const peerRow = await peer.getRecovery(original.clientMutationId);
    if (!peerRow) throw new Error("missing peer recovery fixture");
    await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: "peer composition" }],
      [],
      "peer composition",
      [],
      [],
      undefined,
      undefined,
      { [original.clientMutationId]: recoveryCompositionRevision(peerRow) },
    );

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "row composition", [], []);
    await revalidationReached.promise;
    // While the revalidation read is held, the user types, settles an image,
    // and closes the pane.
    act(() => {
      source.editText("newer disposal work");
    });
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    await act(async () => {
      await encoding.resolve();
    });
    view.unmount();
    unmounted = true;
    source.dispose();
    releaseRevalidation.resolve();
    await saving;
    await flushPendingTurnsProjectionForTests();

    // The preserved row carries the disposal snapshot's newest work, and the
    // published selection points at it.
    const preserved = (await storage.listRecovery(ref)).find(
      (row) => row.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ targetRef: ref, composerText: "newer disposal work[image 1]" });
    expect(readRecoverySelection(ref, source.recoverySelectionOwnerId)).toBe(preserved?.clientMutationId);
  } finally {
    releaseRevalidation.resolve();
    readSpy.mockRestore();
    if (!unmounted) view.unmount();
    source.dispose();
    peer.close();
    storage.close();
  }
});

// The first reconciliation can incorporate the disposal snapshot into the
// preserved row, and a peer can then consume that row before the
// revalidation read. Re-preserving the incorporated content then would
// resurrect work the peer already sent or discarded; only work gained after
// the first reconciliation may preserve again.
test("a peer consuming the reconciled row before revalidation does not resurrect disposal work", async () => {
  class HeldStagesStorage extends MutationOutboxIndexedDB {
    holdPreservation = true;
    readonly preservationReached = deferred<void>();
    readonly releasePreservation = deferred<void>();
    holdReconcileWrite = false;
    readonly reconcileReached = deferred<void>();
    readonly releaseReconcile = deferred<void>();

    override async preserveRecoveryComposition(
      ...args: Parameters<MutationOutboxIndexedDB["preserveRecoveryComposition"]>
    ): ReturnType<MutationOutboxIndexedDB["preserveRecoveryComposition"]> {
      if (this.holdPreservation) {
        this.holdPreservation = false;
        this.preservationReached.resolve();
        await this.releasePreservation.promise;
      }
      return super.preserveRecoveryComposition(...args);
    }

    override async updateRecoveryInput(
      ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
    ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
      const result = await super.updateRecoveryInput(...args);
      // Hold the reconciliation's own successful write: the peer consumes
      // the row between this reconciliation and the revalidation read.
      if (this.holdReconcileWrite && result !== undefined) {
        this.holdReconcileWrite = false;
        this.reconcileReached.resolve();
        await this.releaseReconcile.promise;
      }
      return result;
    }
  }
  const storage = new HeldStagesStorage();
  setMutationStorageForTests(storage);
  const ref = "local:consumed-reconcile";
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
  const source = createTestComposerSource(ref, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  const scope = composerSelectionScope("pane-a");
  try {
    await source.activateRecovery(recovery);
    // A peer advances the row so the source's save is refused into
    // preservation.
    const peerRow = await peer.getRecovery(original.clientMutationId);
    if (!peerRow) throw new Error("missing peer recovery fixture");
    await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: "peer composition" }],
      [],
      "peer composition",
      [],
      [],
      undefined,
      undefined,
      { [original.clientMutationId]: recoveryCompositionRevision(peerRow) },
    );

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "row composition", [], []);
    await storage.preservationReached.promise;
    // The pane gains newer work and closes while the preservation is held.
    source.editText("newer disposal work");
    source.dispose();
    storage.holdReconcileWrite = true;
    storage.releasePreservation.resolve();
    await storage.reconcileReached.promise;

    // The first reconciliation incorporated the disposal work into the
    // preserved row; a peer discards that row before the revalidation read.
    const preserved = (await peer.listRecovery(ref)).find((row) => row.clientMutationId !== original.clientMutationId);
    expect(preserved?.composerText).toBe("newer disposal work");
    expect(await peer.discardRecovery(preserved?.clientMutationId ?? "")).toBe(true);
    storage.releaseReconcile.resolve();
    await saving;
    await flushPendingTurnsProjectionForTests();

    // The consumed disposal work is not resurrected under a fresh identity,
    // and the stored selection still names the activation's row.
    expect(
      (await storage.listRecovery(ref)).filter((row) => row.clientMutationId !== original.clientMutationId),
    ).toEqual([]);
    expect(readRecoverySelection(ref, scope)).toBe(original.clientMutationId);
  } finally {
    storage.releasePreservation.resolve();
    storage.releaseReconcile.resolve();
    source.dispose();
    peer.close();
    storage.close();
  }
});

// A deferred adoption can wait on another source's submission alone - the
// destination holds no operation lock of its own, so only the shared
// submission guard's release can recheck it.
test("a deferred adoption rechecks when the sending peer's submission settles", async () => {
  installLocalStorage(new MemoryStorage());
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:defer-peer-send";
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
  const preservedIntent = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "preserved composition" }] },
    attachments: [],
    composerText: "preserved composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "preserved composition" }] },
  });
  await storage.transferToRecovery(preservedIntent.clientMutationId, "rejected");
  const preserved = await storage.getRecovery(preservedIntent.clientMutationId);
  if (!preserved) throw new Error("missing preserved fixture");
  const sender = createTestComposerSource(ref, undefined, "pane-a");
  const replacement = createTestComposerSource(ref, undefined, "pane-a");
  try {
    sender.setActiveRecoveryId(recovery);
    replacement.setActiveRecoveryId(recovery);
    // The peer composer is mid-send on the same row; the replacement holds no
    // operation lock of its own.
    const releaseSend = beginRecoverySubmission(original.clientMutationId);
    replacement.adoptPreservedSelection(ref, original.clientMutationId, preserved);
    // Deferred: the lease is untouched while the send is in flight.
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(replacement.getSnapshot().text).toBe("");
    // The send fails; its guard release must recheck the deferred adoption.
    releaseSend();
    await flushPendingTurnsProjectionForTests();
    expect(replacement.getSnapshot().activeRecoveryId).toBe(preserved.clientMutationId);
    expect(replacement.getSnapshot().text).toBe("preserved composition");
  } finally {
    sender.dispose();
    replacement.dispose();
    storage.close();
  }
});

// A deferred adoption waits on a submission it does not own. While it waits,
// another pane can edit or remove the preserved row; installing the captured
// record would re-write removed or sent content as a fresh recovery. The
// flush must re-read the row and drop the adoption when it no longer matches
// what was captured.
test.each<[string, (peer: MutationOutboxIndexedDB, preserved: MutationRecoveryRecord) => Promise<void>]>([
  [
    "edited",
    async (peer, preserved) => {
      await peer.updateRecoveryInput(
        preserved.clientMutationId,
        [{ type: "text", text: "edited preserved composition" }],
        [],
        "edited preserved composition",
        [],
        [],
        undefined,
        undefined,
        { [preserved.clientMutationId]: recoveryCompositionRevision(preserved) },
      );
    },
  ],
  [
    "removed",
    async (peer, preserved) => {
      expect(await peer.discardRecovery(preserved.clientMutationId)).toBe(true);
    },
  ],
])("a deferred adoption drops a preserved row another pane %s while it waited", async (_name, interfere) => {
  installLocalStorage(new MemoryStorage());
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = `local:defer-${_name}`;
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
  const preservedIntent = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "preserved composition" }] },
    attachments: [],
    composerText: "preserved composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "preserved composition" }] },
  });
  await storage.transferToRecovery(preservedIntent.clientMutationId, "rejected");
  const preserved = await storage.getRecovery(preservedIntent.clientMutationId);
  if (!preserved) throw new Error("missing preserved fixture");
  const replacement = createTestComposerSource(ref, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    replacement.setActiveRecoveryId(recovery);
    const releaseSend = beginRecoverySubmission(original.clientMutationId);
    replacement.adoptPreservedSelection(ref, original.clientMutationId, preserved);
    // Adoption is deferred while the send is in flight.
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);

    await interfere(peer, preserved);
    releaseSend();
    await flushPendingTurnsProjectionForTests();
    // The captured record no longer describes the preserved row: the
    // deferred adoption is dropped, and the composer keeps its own lease.
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(replacement.getSnapshot().text).toBe("");
  } finally {
    replacement.dispose();
    peer.close();
    storage.close();
  }
});

// The preservation that captures a record awaits projection refreshes after
// committing, so another pane can edit, remove, or send the row before an
// immediate adoption runs too. Installing the captured record then would let
// the destination's next autosave resurrect that removed or superseded
// content under a fresh recovery id, so the immediate path re-reads and drops
// what no longer matches, exactly like the deferred one.
test.each<[string, (peer: MutationOutboxIndexedDB, preserved: MutationRecoveryRecord) => Promise<void>]>([
  [
    "edited",
    async (peer, preserved) => {
      await peer.updateRecoveryInput(
        preserved.clientMutationId,
        [{ type: "text", text: "edited preserved composition" }],
        [],
        "edited preserved composition",
        [],
        [],
        undefined,
        undefined,
        { [preserved.clientMutationId]: recoveryCompositionRevision(preserved) },
      );
    },
  ],
  [
    "removed",
    async (peer, preserved) => {
      expect(await peer.discardRecovery(preserved.clientMutationId)).toBe(true);
    },
  ],
])(
  "an immediate adoption drops a preserved row another pane %s while the post-commit wait lasted",
  async (_name, interfere) => {
    installLocalStorage(new MemoryStorage());
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const ref = `local:immediate-${_name}`;
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
    const preservedIntent = await storage.enqueueIntent({
      targetRef: ref,
      method: "turn/start",
      payload: { ref, input: [{ type: "text", text: "preserved composition" }] },
      attachments: [],
      composerText: "preserved composition",
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "preserved composition" }] },
    });
    await storage.transferToRecovery(preservedIntent.clientMutationId, "rejected");
    const preserved = await storage.getRecovery(preservedIntent.clientMutationId);
    if (!preserved) throw new Error("missing preserved fixture");
    const replacement = createTestComposerSource(ref, undefined, "pane-a");
    const detachEditor = replacement.bindEditor({ read: () => replacement.editor.read(), write: () => {} });
    const peer = new MutationOutboxIndexedDB();
    try {
      replacement.setActiveRecoveryId(recovery);
      await interfere(peer, preserved);
      await replacement.adoptPreservedSelection(ref, original.clientMutationId, preserved);
      // The captured record no longer describes the durable row: the immediate
      // adoption is dropped, and the composer keeps its own lease.
      expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
      expect(replacement.getSnapshot().text).toBe("");
    } finally {
      detachEditor();
      replacement.dispose();
      peer.close();
      storage.close();
    }
  },
);

test("an immediate adoption still adopts a preserved row no peer changed", async () => {
  installLocalStorage(new MemoryStorage());
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:immediate-unchanged";
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
  const preservedIntent = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "preserved composition" }] },
    attachments: [],
    composerText: "preserved composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "preserved composition" }] },
  });
  await storage.transferToRecovery(preservedIntent.clientMutationId, "rejected");
  const preserved = await storage.getRecovery(preservedIntent.clientMutationId);
  if (!preserved) throw new Error("missing preserved fixture");
  const replacement = createTestComposerSource(ref, undefined, "pane-a");
  const detachEditor = replacement.bindEditor({ read: () => replacement.editor.read(), write: () => {} });
  try {
    replacement.setActiveRecoveryId(recovery);
    await replacement.adoptPreservedSelection(ref, original.clientMutationId, preserved);
    expect(replacement.getSnapshot().activeRecoveryId).toBe(preserved.clientMutationId);
    expect(replacement.getSnapshot().text).toBe("preserved composition");
  } finally {
    detachEditor();
    replacement.dispose();
    storage.close();
  }
});

// Preservation publishes the preserved row as the destination's stored
// selection before the adoption waits on a submission or the durable read.
// When the destination's user types while that adoption is deferred, dropping
// it is right - but the stored selection still named the preserved row, so a
// reload restored the older preserved composition instead of the typed work.
// The drop must reclaim the selection for the active row the newer edits
// autosave onto.
test("typing during a deferred adoption reclaims the selection for the edited row", async () => {
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
  const ref = "local:reclaim-selection";
  const original = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "row composition" }] },
    attachments: [],
    composerText: "row composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "row composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const staleRow = await storage.getRecovery(original.clientMutationId);
  if (!staleRow) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(ref, undefined, "pane-a");
  const replacement = createTestComposerSource(ref, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  const scope = composerSelectionScope("pane-a");
  try {
    await source.activateRecovery(staleRow);
    // A peer advances the row so the source's next save is refused into
    // preservation.
    const peerRow = await peer.getRecovery(original.clientMutationId);
    if (!peerRow) throw new Error("missing peer recovery fixture");
    await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: "peer composition" }],
      [],
      "peer composition",
      [],
      [],
      undefined,
      undefined,
      { [original.clientMutationId]: recoveryCompositionRevision(peerRow) },
    );
    // The replacement restores the advanced row and starts sending it.
    const advanced = await storage.getRecovery(original.clientMutationId);
    if (!advanced) throw new Error("missing advanced recovery fixture");
    await replacement.activateRecovery(advanced);
    const releaseSend = beginRecoverySubmission(original.clientMutationId);

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "local preserved composition", [], []);
    await storage.reached.promise;
    storage.release.resolve();
    await saving;
    await flushPendingTurnsProjectionForTests();
    // The delayed preservation selected the preserved row; the adoption
    // deferred behind the held send.
    const preserved = (await storage.listRecovery(ref)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toMatchObject({ targetRef: ref, composerText: "row composition" });
    expect(readRecoverySelection(ref, scope)).toBe(preserved?.clientMutationId);
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);

    // The user types while the send is held.
    replacement.editText("newer local work");
    releaseSend();
    await flushPendingTurnsProjectionForTests();
    // The newer typing dropped the adoption; the stored selection was
    // reclaimed for the row those edits live on.
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);
    expect(readRecoverySelection(ref, scope)).toBe(original.clientMutationId);

    // The newer work autosaves onto the active row.
    await replacement.queueRecoveryPersistence(original.clientMutationId, "newer local work", [], []);
    const saved = await storage.getRecovery(original.clientMutationId);
    expect(saved).toMatchObject({ composerText: "newer local work" });

    // A remount restores the selection: the typed work, not the older
    // preserved composition.
    replacement.dispose();
    const remounted = createTestComposerSource(ref, undefined, "pane-a");
    try {
      expect(readRecoverySelection(ref, remounted.recoverySelectionOwnerId)).toBe(original.clientMutationId);
      const restored = await storage.getRecovery(original.clientMutationId);
      if (!restored) throw new Error("missing restored row");
      expect(restored.composerText).toBe("newer local work");
      await remounted.activateRecovery(restored);
      expect(remounted.getSnapshot().text).toBe("newer local work");
    } finally {
      remounted.dispose();
    }
  } finally {
    source.dispose();
    replacement.dispose();
    peer.close();
    storage.close();
  }
});

// The row preserved from before a disposal reconciliation can be advanced or
// consumed by another pane while this source holds the disposal snapshot. A
// refused reconciliation must preserve that snapshot under a fresh recovery
// owner - a disposed source can never save it again.
test.each<[string, (peer: MutationOutboxIndexedDB, preservedId: string, revision: number) => Promise<void>]>([
  [
    "concurrent edit",
    async (peer, preservedId, revision) => {
      await peer.updateRecoveryInput(
        preservedId,
        [{ type: "text", text: "peer edit of the preserved row" }],
        [],
        "peer edit of the preserved row",
        [],
        [],
        undefined,
        undefined,
        { [preservedId]: revision },
      );
    },
  ],
  [
    "consumption",
    async (peer, preservedId) => {
      expect(await peer.discardRecovery(preservedId)).toBe(true);
    },
  ],
])("a refused disposal reconciliation preserves the newest work under a fresh owner (%s)", async (_name, interfere) => {
  class HeldAfterPreservationStorage extends MutationOutboxIndexedDB {
    holdNextPreservation = true;
    readonly reached = deferred<void>();
    readonly release = deferred<void>();

    override async preserveRecoveryComposition(
      ...args: Parameters<MutationOutboxIndexedDB["preserveRecoveryComposition"]>
    ): ReturnType<MutationOutboxIndexedDB["preserveRecoveryComposition"]> {
      const result = await super.preserveRecoveryComposition(...args);
      if (this.holdNextPreservation) {
        this.holdNextPreservation = false;
        this.reached.resolve();
        await this.release.promise;
      }
      return result;
    }
  }
  const storage = new HeldAfterPreservationStorage();
  setMutationStorageForTests(storage);
  const ref = `local:refused-reconcile-${_name}`;
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
    const operations = createAttachmentOperations(source.editor, source.attachments);
    source.editText("start [image 1]");
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "start [image 1]", [], []);
    await storage.reached.promise;
    const preservedBefore = (await peer.listRecovery(ref)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    if (!preservedBefore) throw new Error("missing preserved fixture");
    // While the preservation is held, the pane gains work, another pane takes
    // the preserved row over, and the pane closes before settling.
    source.editText("latest [image 1]");
    operations.replaceWithSettled([
      { marker: 1, name: "latest.png", mediaType: "image/png", data: "AQID", pending: false },
    ]);
    await interfere(peer, preservedBefore.clientMutationId, recoveryCompositionRevision(preservedBefore));
    source.dispose();
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(ref)).find(
      (record) =>
        record.clientMutationId !== original.clientMutationId &&
        record.clientMutationId !== preservedBefore.clientMutationId,
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
    peer.close();
    storage.close();
  }
});

// Clearing the composer while a preservation is pending, then closing the
// pane, leaves an empty disposal snapshot: the row preserved from before the
// clear no longer describes the user's final state and must be cleaned up,
// not left as the composition a reopen restores.
test("clearing the composer during a held preservation discards the stale row", async () => {
  class HeldAfterPreservationStorage extends MutationOutboxIndexedDB {
    holdNextPreservation = true;
    readonly reached = deferred<void>();
    readonly release = deferred<void>();

    override async preserveRecoveryComposition(
      ...args: Parameters<MutationOutboxIndexedDB["preserveRecoveryComposition"]>
    ): ReturnType<MutationOutboxIndexedDB["preserveRecoveryComposition"]> {
      const result = await super.preserveRecoveryComposition(...args);
      if (this.holdNextPreservation) {
        this.holdNextPreservation = false;
        this.reached.resolve();
        await this.release.promise;
      }
      return result;
    }
  }
  const storage = new HeldAfterPreservationStorage();
  setMutationStorageForTests(storage);
  const ref = "local:clear-during-preserve";
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
    source.editText("start composition");
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

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "start composition", [], []);
    await storage.reached.promise;
    // The user clears the composer while the preservation is pending, then
    // closes the pane.
    source.editText("");
    source.dispose();
    storage.release.resolve();
    await saving;

    const preserved = (await storage.listRecovery(ref)).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    expect(preserved).toBeUndefined();
    expect(readRecoverySelection(ref, source.recoverySelectionOwnerId)).toBe(original.clientMutationId);
    const reopened = createTestComposerSource(ref, undefined, "pane-a");
    try {
      expect(reopened.getSnapshot().text).toBe("");
    } finally {
      reopened.dispose();
    }
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// An identical-composition preservation returns the stale row itself, which
// another tab may have retargeted to a resumed identity without changing a
// byte. The destination's selection and seat mapping must still be recorded:
// the disposed source can never run the reconciliation a live one would, and
// a reopen of the resumed conversation would otherwise select an older peer
// recovery.
test("an identical-composition preservation under a resumed ref records the destination", async () => {
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
  const oldRef = "local:identical-old";
  const resumed = "local:identical-new";
  const original = await storage.enqueueIntent({
    targetRef: oldRef,
    method: "turn/start",
    payload: { ref: oldRef, input: [{ type: "text", text: "same composition" }] },
    attachments: [],
    composerText: "same composition",
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "same composition" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource(oldRef, undefined, "pane-a");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    const peerRecovery = await peer.getRecovery(original.clientMutationId);
    if (!peerRecovery) throw new Error("missing peer recovery fixture");
    // Another tab advances the row's revision without changing its
    // composition, then retargets it to a resumed identity.
    await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: "same composition" }],
      [],
      "same composition",
      [],
      [],
      undefined,
      undefined,
      { [original.clientMutationId]: recoveryCompositionRevision(peerRecovery) },
    );
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });

    const saving = source.queueRecoveryPersistence(original.clientMutationId, "same composition", [], []);
    await storage.reached.promise;
    // The pane closes before the preservation settles, so the destination
    // records this pass writes are the only ones that land.
    source.dispose();
    storage.release.resolve();
    await saving;

    expect(readRecoverySelection(resumed, source.recoverySelectionOwnerId)).toBe(original.clientMutationId);
    expect(readRefComposerSeat(resumed)).toBe("pane-a");
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// Delayed preservation can select a fresh preserved row while a replacement
// composer is mid-send on its stale row, with the adoption deferred until the
// submission settles. A successful send retires the lease - and that
// retirement must clear only the retiring lease's own stored choice, never
// the preserved row the delayed preservation selected for this seat.
test("a retired lease keeps the preserved row a delayed preservation selected", async () => {
  installLocalStorage(new MemoryStorage());
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "local:retire-preserved-selection";
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
  const replacement = createTestComposerSource(ref, undefined, "pane-a");
  try {
    replacement.setActiveRecoveryId(recovery);
    // The delayed preservation publishes the fresh row's selection while the
    // composer is mid-send on its restored row.
    const preserved: typeof recovery = {
      ...recovery,
      clientMutationId: "preserved-row",
      composerText: "preserved composition",
      payload: { ...recovery.payload, input: [{ type: "text", text: "preserved composition" }] },
    };
    const releaseOperation = replacement.tryAcquireRecoveryOperation();
    if (!releaseOperation) throw new Error("operation lock unavailable");
    const releaseSend = beginRecoverySubmission(original.clientMutationId);
    writeRecoverySelection(ref, replacement.recoverySelectionOwnerId, "preserved-row");
    replacement.adoptPreservedSelection(ref, original.clientMutationId, preserved);
    // Adoption is deferred while the send owns the lease.
    expect(replacement.getSnapshot().activeRecoveryId).toBe(original.clientMutationId);

    // The send commits: the lease retires under the held guards.
    replacement.setActiveRecoveryId(null);
    releaseSend();
    releaseOperation();
    // The deferred adoption is dropped - its lease is gone - but the
    // preserved row's selection must survive the retirement.
    expect(replacement.getSnapshot().activeRecoveryId).toBeNull();
    expect(readRecoverySelection(ref, replacement.recoverySelectionOwnerId)).toBe("preserved-row");
  } finally {
    replacement.dispose();
    storage.close();
  }
});

// Clearing the composer while a preservation under a resumed ref is pending,
// then closing the pane, discards the preserved row. The discard's refresh
// must reach the mounted view of the row's own ref - the source's stale ref
// refresh leaves the resumed entry on screen.
test("clearing during preservation removes the preserved row from a mounted resumed view", async () => {
  class HeldAfterPreservationStorage extends MutationOutboxIndexedDB {
    holdNextPreservation = true;
    readonly reached = deferred<void>();
    readonly release = deferred<void>();

    override async preserveRecoveryComposition(
      ...args: Parameters<MutationOutboxIndexedDB["preserveRecoveryComposition"]>
    ): ReturnType<MutationOutboxIndexedDB["preserveRecoveryComposition"]> {
      const result = await super.preserveRecoveryComposition(...args);
      if (this.holdNextPreservation) {
        this.holdNextPreservation = false;
        this.reached.resolve();
        await this.release.promise;
      }
      return result;
    }
  }
  const storage = new HeldAfterPreservationStorage();
  setMutationStorageForTests(storage);
  const oldRef = "local:clear-retarget-old";
  const resumed = "local:clear-retarget-new";
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
  const view = renderHook(() => useRecoveryEntries(resumed));
  // A mounted composer holds an editor binding; without one, every editText
  // would also queue a detached persistence chain whose settled reads land
  // outside the window this test awaits.
  const releaseEditor = source.bindEditor({
    read: () => ({ text: "", cursor: 0, selection: { start: 0, end: 0 } }),
    write: () => {},
  });
  try {
    await waitFor(() => expect(view.result.current).toEqual([]));
    await act(async () => {
      await source.activateRecovery(recovery);
    });
    await flushPendingTurnsProjectionForTests();
    source.editText("start composition");
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
    await peer.retargetOutbox(oldRef, resumed, { threadId: "thread-new", instanceId: "instance-new" });
    await act(async () => {
      const saving = source.queueRecoveryPersistence(original.clientMutationId, "start composition", [], []);
      await storage.reached.promise;
      // The user clears the composer while the preservation is pending, then
      // closes the pane.
      source.editText("");
      source.dispose();
      storage.release.resolve();
      await saving;
      // Settle the tracked reads inside the window: one settling between
      // this act's exit and the next flush round would publish outside both.
      await settleProjectionWorkForTests();
    });
    await flushPendingTurnsProjectionForTests();
    // The row was written and discarded inside the same settled chain: the
    // mounted resumed view must not retain it - the view keeps exactly the
    // surviving peer row and nothing else.
    await waitFor(() =>
      expect(view.result.current.map((record) => record.clientMutationId)).toEqual([original.clientMutationId]),
    );
    expect(
      (await storage.listRecovery(resumed)).find((record) => record.clientMutationId !== original.clientMutationId),
    ).toBeUndefined();
  } finally {
    view.unmount();
    releaseEditor();
    source.dispose();
    peer.close();
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
