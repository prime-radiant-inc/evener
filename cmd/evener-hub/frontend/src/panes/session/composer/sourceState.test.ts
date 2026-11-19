import type { InputItem } from "@evener/appwire-client";
import { act, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
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
  composerDraftStorageKey,
  draftStorageKey,
  markDraftEdited,
  readComposerDraft,
  readComposerDraftRevisionId,
  readComposerDraftSnapshot,
  readDraftRevision,
  writeComposerDraft,
} from "./draft";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { unbackedImageMarkersIn } from "./submitProtocol";

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

// An edit-recovery born of a retargeted row is associated with the displayed
// session by displayedRef while its durable destination is the resumed
// session. The displayed composer must activate its associated record, or the
// row the strip surfaced stays inert. The fixture is built the way the
// product builds it: the recovery row is the canceled retargeted RESEND the
// strip surfaces (displayedRef is the resend's displayed barrier, never a
// retarget stamp), moved to recovery by the strip's own Edit.
test("the displayed composer activates a recovery record associated by displayed ref", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const input = [{ type: "text" as const, text: "retargeted" }];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  await storage.retargetOutbox("root", "ref_b", { threadId: "thr_ref_b" });
  const sent = await storage.resendRecoveryResult(
    original.clientMutationId,
    {
      targetRef: "ref_b",
      threadId: "thr_ref_b",
      method: "turn/start",
      payload: { ref: "ref_b", input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    },
    undefined,
    undefined,
    undefined,
    { stopEpoch: 0, displayedRef: "root" },
  );
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  // A Stop on the still-displayed session cancels the unattempted resend -
  // the canceled row the strip surfaces under the displayed ref.
  await storage.enqueueInterruptAndCancel({
    targetRef: "root",
    threadId: "thr_root",
    method: "turn/interrupt",
    payload: { ref: "root" },
    attachments: [],
    optimisticDisplay: { method: "turn/interrupt" },
  });
  const canceled = await storage.getOutbox(sent.record.clientMutationId);
  if (canceled?.state !== "canceled") throw new Error("missing canceled retargeted row");
  await storage.transferToRecovery(sent.record.clientMutationId, "editing");
  const recovery = await storage.getRecovery(sent.record.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  expect(recovery.targetRef).toBe("ref_b");
  expect(recovery.displayedRef).toBe("root");
  const source = createTestComposerSource("root");
  await expect(source.activateRecovery(recovery)).resolves.toBe(true);
  expect(source.getSnapshot().activeRecoveryId).toBe(sent.record.clientMutationId);
  expect(source.getSnapshot().text).toBe("retargeted");
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

// A marker the OTHER writer typed is prose, not a staged anchor, so adopting
// the draft must not put it in the record. A rebind that recorded every
// marker the text spelled would offer to delete valid prose and block Send on
// it.
test("a rebind does not adopt a marker the other writer typed", () => {
  const source = createTestComposerSource("root");
  const detach = source.bindEditor({ read: () => source.editor.read(), write: () => {} });
  detach();
  writeComposerDraft("root", { text: "Cascade reader draft[image 1]", skillNames: [] });
  const unbind = source.bindEditor({ read: () => source.editor.read(), write: () => {} });
  try {
    expect(source.getSnapshot().text).toBe("Cascade reader draft[image 1]");
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([]);
  } finally {
    unbind();
    source.dispose();
  }
});

// The writer STAGED that anchor with this very revision, so its bytes are
// gone with it even though this reader never saw the writer. A fresh source
// that records nothing leaks the orphaned marker onto the wire as literal
// text.
test("a fresh source adopts an anchor the record says its writer staged", () => {
  writeComposerDraft("root", { text: "look at [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const source = createTestComposerSource("root");
  try {
    expect(source.getSnapshot().text).toBe("look at [image 1]");
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([1]);
  } finally {
    source.dispose();
  }
});

// A legacy record carries no provenance: nothing there can say a marker was
// staged, so its markers stay prose and keep sending.
test("a draft written before provenance existed adopts nothing", () => {
  localStorage.setItem(composerDraftStorageKey("root"), JSON.stringify({ text: "[image 3] hi", skillNames: [] }));
  const source = createTestComposerSource("root");
  try {
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([]);
  } finally {
    source.dispose();
  }
});

// The record names a marker the text no longer spells: the writer removed the
// anchor before this reader got here, so there is nothing to refuse.
test("staged provenance for a marker the text does not spell adopts nothing", () => {
  writeComposerDraft("root", { text: "just prose", skillNames: [], stagedImageMarkers: [2] });
  const source = createTestComposerSource("root");
  try {
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([]);
  } finally {
    source.dispose();
  }
});

// The record survives an edit that stops spelling the anchor, so an undo
// (which restores the text, not the record) brings the refusal back. Deleting
// the anchor must clear the refusal on its own while it is absent.
test("adoption survives an edit so undo can restore the refusal", () => {
  writeComposerDraft("root", { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const source = createTestComposerSource("root");
  try {
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([1]);
    source.editText("kept ");
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([]);
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([1]);
    source.editText("kept [image 1]");
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([1]);
  } finally {
    source.dispose();
  }
});

// A live reader that staged the image itself owns the draft revision, so no
// adoption runs and its anchor sends with the bytes it holds. (The payload
// direction of this case is Composer.attachments.test.tsx's "a composer
// holding the image bytes sends the marker with its image item".)
test("an owning reader never adopts its own staged anchor", () => {
  const source = createTestComposerSource("root");
  try {
    source.persistDraft("mine [image 1]");
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([]);
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([]);
  } finally {
    source.dispose();
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

// Staging is ONE write. The text and the provenance that says this writer
// staged its marker land together, so a reload between the gesture and its
// async encode reads a record that already knows the anchor was staged.
test("staging persists the marker and its provenance in one write", () => {
  const source = createTestComposerSource("root");
  installControlledImageEncoding();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  try {
    source.editor.write("staged ", 7);
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    // Read straight from storage: the encode is still held, so a provenance
    // that depended on the tile SETTLING would not be here yet.
    expect(readComposerDraftSnapshot("root").text).toBe("staged [image 1]");
    expect(readComposerDraftSnapshot("root").stagedImageMarkers).toEqual([1]);
    expect(source.attachments.getState().items.map((item) => item.pending)).toEqual([true]);
  } finally {
    view.unmount();
    source.dispose();
  }
});

// A successful encode must not rewrite the draft. The tile update is
// in-memory; writing here would push this tab's older text over any edit
// another tab made while the encode ran.
test("a successful encode leaves the persisted draft untouched", async () => {
  const encoding = installControlledImageEncoding();
  const source = createTestComposerSource("root");
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  try {
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    const revisionId = readComposerDraftRevisionId("root");
    await act(async () => {
      await encoding.resolve();
    });
    expect(readComposerDraftRevisionId("root")).toBe(revisionId);
    expect(source.attachments.getState().items).toEqual([
      { marker: 1, name: "source.png", mediaType: "image/png", pending: false, data: "AQID", width: 8, height: 4 },
    ]);
    expect(readComposerDraftSnapshot("root").stagedImageMarkers).toEqual([1]);
  } finally {
    view.unmount();
    source.dispose();
  }
});

// Two tabs, one draft. Tab A's encode settles after Tab B replaced the shared
// draft; A's background continuation must not put its own older text back.
// Ownership is the persisted revision identity, which B's write changed.
test("a settle in one tab never overwrites another tab's newer draft", async () => {
  const encoding = installControlledImageEncoding();
  const source = createTestComposerSource("root");
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  try {
    source.editor.write("tab A ", 6);
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    // Tab B owns the same ref and replaces the draft.
    writeComposerDraft("root", { text: "tab B newer", skillNames: [] });
    expect(source.ownsDraftRevision()).toBe(false);
    await act(async () => {
      await encoding.resolve();
    });
    expect(readComposerDraft("root")).toEqual({ text: "tab B newer", skillNames: [] });
  } finally {
    view.unmount();
    source.dispose();
  }
});

// A detached write cannot claim another tab's draft either. The old tab-local
// counter read as "mine" here, which is the write that clobbered B.
test("a detached write refuses a draft another tab replaced", () => {
  const source = createTestComposerSource("root");
  try {
    source.editor.write("tab A ", 6);
    expect(readComposerDraft("root").text).toBe("tab A ");
    writeComposerDraft("root", { text: "tab B", skillNames: [] });
    source.editor.write("tab A late", 10);
    expect(readComposerDraft("root")).toEqual({ text: "tab B", skillNames: [] });
  } finally {
    source.dispose();
  }
});

// A number a dormant adoption still names is not handed to a new attachment.
// Reusing it would pair fresh bytes with an anchor the person never staged,
// and the stale refusal would block the replacement image.
test("a replacement image takes a number a dormant adoption reserves", () => {
  writeComposerDraft("root", { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const source = createTestComposerSource("root");
  installControlledImageEncoding();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  try {
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([1]);
    // The person removes the reference before attaching its replacement.
    source.editText("kept ");
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    expect(source.attachments.getState().items.map((item) => item.marker)).toEqual([2]);
    expect(source.getSnapshot().text).toBe("kept [image 2]");
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([]);
  } finally {
    view.unmount();
    source.dispose();
  }
});

// Retiring the reservation must not erase the adoption itself. Removing then
// restoring the anchor within the same draft still refuses.
test("an undo within the same draft restores the original refusal", () => {
  writeComposerDraft("root", { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const source = createTestComposerSource("root");
  try {
    source.editText("kept ");
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([]);
    source.editText("kept [image 1]");
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([1]);
    expect(source.attachments.getState().reservedMarkersRef.current).toEqual([1]);
  } finally {
    source.dispose();
  }
});

// Replacing the draft retires the adoption. The replaced anchors are not in
// the new text, so keeping them would reserve numbers forever.
test("clearing the draft retires its adoption", () => {
  writeComposerDraft("root", { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const source = createTestComposerSource("root");
  try {
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([1]);
    source.clearIfUnchanged("kept [image 1]", 0, readDraftRevision("root"), []);
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([]);
    expect(source.attachments.getState().reservedMarkersRef.current).toEqual([]);
  } finally {
    source.dispose();
  }
});

// Editing a durable queued message while the composer holds an adopted anchor
// must keep that provenance on the row. Without it a reload lets Send emit
// the orphan as prose.
test("an autosave keeps the adoption its composition carries", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const text = "kept [image 1]";
  const input = [{ type: "text" as const, text }];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/queue",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input },
    composerText: text,
    adoptedImageMarkers: [1],
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  expect(recovery.adoptedImageMarkers).toEqual([1]);
  const source = createTestComposerSource("root");
  try {
    source.setActiveRecoveryId(recovery);
    await source.queueRecoveryPersistence(original.clientMutationId, text, [], [], [], undefined, text, [1]);
    expect(await storage.getRecovery(original.clientMutationId)).toMatchObject({
      composerText: text,
      adoptedImageMarkers: [1],
    });
  } finally {
    source.dispose();
  }
});

// Restore, end to end: a reloaded pane opens the row and Send is still
// refused on the anchor whose bytes never came back. A recovered tile whose
// bytes DID travel is not adopted.
test("an activated recovery restores the adoption its row carries", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const text = "kept [image 1]";
  const input = [{ type: "text" as const, text }];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/queue",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input },
    composerText: text,
    adoptedImageMarkers: [1],
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  const source = createTestComposerSource("root");
  try {
    await expect(source.activateRecovery(recovery)).resolves.toBe(true);
    expect(source.getSnapshot().text).toBe(text);
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([1]);
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([1]);
  } finally {
    source.dispose();
  }
});

// A staged anchor names its own occurrence too, even when it is the only one.
// The tile backs it HERE, but the record outlives those bytes: a reload holds
// no tiles, and a number alone marks every spelling of it - so the record has
// to say WHICH spelling its writer staged.
test("a draft whose only anchor this composer staged names that occurrence", async () => {
  const source = createTestComposerSource("root");
  installControlledImageEncoding();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  try {
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    const text = source.editor.read().text;
    expect(text).toContain("[image 1]");
    expect(source.persistDraft(text));
    const snapshot = readComposerDraftSnapshot("root");
    expect(snapshot.stagedImageMarkers).toEqual([1]);
    const start = text.indexOf("[image 1]");
    expect(snapshot.stagedImageRanges).toEqual([{ marker: 1, start, end: start + "[image 1]".length }]);
  } finally {
    view.unmount();
    source.dispose();
  }
});

// End to end through the real staging pipeline: a gesture stages an image, then
// the person types a separate literal "[image 1]" elsewhere in the same draft.
// The record has to say WHICH spelling the gesture wrote, or a reload holds no
// tiles and marks both - and the one repair press the notice offers deletes the
// person's typed prose along with the anchor.
test("a reloaded draft keeps a typed literal beside the anchor its writer staged", () => {
  const source = createTestComposerSource("root");
  installControlledImageEncoding();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  let text = "";
  try {
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    // The real staging write put "[image 1]" at offset 0; the person then types
    // a literal of the same number after it.
    source.editor.write("[image 1] typed beside [image 1]", 33);
    text = source.getSnapshot().text;
    expect(text).toBe("[image 1] typed beside [image 1]");
    expect(readComposerDraftSnapshot("root").stagedImageRanges).toEqual([{ marker: 1, start: 0, end: 9 }]);
  } finally {
    view.unmount();
    source.dispose();
  }

  // The reload: a fresh source holds no tiles, so only the record can say which
  // of the two spellings is the anchor whose image is gone.
  const reloaded = createTestComposerSource("root");
  try {
    expect(reloaded.getSnapshot().text).toBe(text);
    // Exactly one occurrence is refused, and it is the staged one.
    expect(reloaded.getSnapshot().adoptedImageRanges).toEqual([{ marker: 1, start: 0, end: 9 }]);
    expect(reloaded.getSnapshot().adoptedImageMarkers).toEqual([1]);
    expect(unbackedImageMarkersIn(text, reloaded.getSnapshot().adoptedImageMarkers)).toEqual([1]);
  } finally {
    reloaded.dispose();
  }
});

// The persisted draft names WHICH spelling of a duplicated number its writer
// staged. A literal the person typed beside an adopted "[image 1]" is prose:
// it must survive the reload unmarked, and the record's own ranges must come
// back naming only the adopted occurrence - otherwise a reloading reader marks
// both, and the one repair the notice offers removes the person's text.
test("a reloaded draft keeps the literal beside an adopted duplicate sendable", () => {
  const text = "[image 1] tail [image 1]";
  const adopted = { marker: 1, start: 15, end: 24 };
  writeComposerDraft("root", { text, skillNames: [], stagedImageMarkers: [1], stagedImageRanges: [adopted] });
  const source = createTestComposerSource("root");
  try {
    expect(source.getSnapshot().text).toBe(text);
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([1]);
    expect(source.getSnapshot().adoptedImageRanges).toEqual([adopted]);
  } finally {
    source.dispose();
  }
});

// The durable plane says the same thing: an autosave that carries a duplicate
// keeps its offsets on the recovery row, and activating that row gives back a
// source whose repair names the adopted occurrence only.
test("an activated recovery restores the exact occurrence a duplicate record names", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const text = "[image 1] tail [image 1]";
  const adopted = { marker: 1, start: 15, end: 24 };
  const input = [{ type: "text" as const, text }];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/queue",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input },
    composerText: text,
    adoptedImageMarkers: [1],
    adoptedImageRanges: [adopted],
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  expect(recovery.adoptedImageRanges).toEqual([adopted]);

  const source = createTestComposerSource("root");
  try {
    await expect(source.activateRecovery(recovery)).resolves.toBe(true);
    expect(source.getSnapshot().text).toBe(text);
    expect(source.getSnapshot().adoptedImageRanges).toEqual([adopted]);
    // The literal at 0 is not adopted: only the occurrence the record named is.
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([1]);
  } finally {
    source.dispose();
  }
});

// A retained composer's tiles outlive the draft another pane replaced. If that
// draft spells the same number as literal prose, Send must not pair the literal
// with this composer's older bytes: the tile moves off it and the incoming text
// is delivered byte-identical. The adopted anchors the incoming record names are
// still refused, so the repair story for a genuinely missing image is unchanged.
test("a rebind moves a retained tile off a literal the incoming draft spells", () => {
  const source = createTestComposerSource("root");
  installControlledImageEncoding();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  try {
    // This composer holds tile 1's bytes...
    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    expect(source.attachments.getState().items.map((item) => item.marker)).toEqual([1]);
    view.unmount();

    // ...and another pane then saves a draft whose text spells "[image 1]" as
    // something that writer staged and whose bytes did not travel, next to a
    // second, missing anchor the record also names.
    const incoming = "look at [image 1] and [image 2]";
    writeComposerDraft("root", { text: incoming, skillNames: [], stagedImageMarkers: [1, 2] });
    const unbind = source.bindEditor({ read: () => source.editor.read(), write: () => {} });
    try {
      // The tile moved off the incoming text's own occurrence.
      expect(source.attachments.getState().items.map((item) => item.marker)).toEqual([3]);
      // Byte-identical: the rebind never rewrites the text it adopted.
      expect(source.getSnapshot().text).toBe(incoming);
      expect(readComposerDraft("root").text).toBe(incoming);
      // The record's anchors are still refused, and the adopted tile's new
      // number is not one of them.
      expect(source.getSnapshot().adoptedImageMarkers).toEqual([1, 2]);
      expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([
        1, 2,
      ]);
      // The moved tile's bytes are the ones this composer staged, not a
      // re-encode: identity survives the renumber.
      expect(source.attachments.getState().items).toEqual([
        { marker: 3, name: "source.png", mediaType: "image/png", pending: true },
      ]);
    } finally {
      unbind();
    }
  } finally {
    source.dispose();
  }
});

// The same rebind with NO adopted provenance: the incoming text's marker is
// prose a person typed, so nothing is refused - but the retained tile still
// cannot keep a number that text spells, because Send would translate the
// literal into a reference to the older tile's bytes.
test("a rebind moves a retained tile off typed prose that spells its number", () => {
  const source = createTestComposerSource("root");
  source.editor.write("mine ", 5);
  const operations = createAttachmentOperations(source.editor, source.attachments);
  operations.replaceWithSettled([
    { marker: 1, name: "retained.png", mediaType: "image/png", data: "AQID", pending: false },
  ]);
  const detach = source.bindEditor({ read: () => source.editor.read(), write: () => {} });
  detach();

  writeComposerDraft("root", { text: "typed [image 1]", skillNames: [] });
  const unbind = source.bindEditor({ read: () => source.editor.read(), write: () => {} });
  try {
    expect(source.attachments.getState().items.map((item) => item.marker)).toEqual([2]);
    expect(source.getSnapshot().text).toBe("typed [image 1]");
    // Nothing was staged by the record's writer, so the literal stays sendable.
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([]);
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([]);
  } finally {
    unbind();
    source.dispose();
  }
});

// A number a dormant adoption reserves survives an edit that stops spelling it,
// so attaching the replacement image takes a fresh number. That is the library
// half of "the undo still refuses": the restored occurrence must never find a
// tile sitting on its number. The mark-and-undo half is the editor's, proven
// through the real composer in Composer.attachments.test.tsx.
test("a replacement image after deleting the only occurrence takes a fresh number", () => {
  writeComposerDraft("root", { text: "[image 1]", skillNames: [], stagedImageMarkers: [1] });
  const source = createTestComposerSource("root");
  installControlledImageEncoding();
  const view = renderHook(() => useAttachments(source.editor, source.attachments));
  try {
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([1]);
    // Deleting the only occurrence BLANKS the draft. Provenance lives in the
    // editor's marks now, so this edit retires nothing: the number stays
    // reserved, because the editor can put the occurrence back with an undo.
    source.editText("");
    expect(source.attachments.getState().reservedMarkersRef.current).toEqual([1]);
    expect(unbackedImageMarkersIn(source.getSnapshot().text, source.getSnapshot().adoptedImageMarkers)).toEqual([]);

    act(() => view.result.current.ingestFiles([image()], (message) => pushToast("error", message)));
    expect(source.attachments.getState().items.map((item) => item.marker)).toEqual([2]);
    expect(source.getSnapshot().text).toBe("[image 2]");
  } finally {
    view.unmount();
    source.dispose();
  }
});

// Reservation numbers only grow while the source lives: an edit that removes
// the occurrence and a later undo that restores it may not have its number
// handed to a tile in between, or the restored occurrence would be paired with
// that tile's bytes.
test("the reserved set keeps a number after the active adoption stops spelling it", () => {
  writeComposerDraft("root", { text: "kept [image 1]", skillNames: [], stagedImageMarkers: [1] });
  const source = createTestComposerSource("root");
  try {
    expect(source.attachments.getState().reservedMarkersRef.current).toEqual([1]);
    source.setAdoptedImageMarkers([]);
    // The active set is empty - nothing in the current text is refused.
    expect(source.getSnapshot().adoptedImageMarkers).toEqual([]);
    // ...but the number stays reserved, because the editor can put it back.
    expect(source.attachments.getState().reservedMarkersRef.current).toEqual([1]);
  } finally {
    source.dispose();
  }
});
