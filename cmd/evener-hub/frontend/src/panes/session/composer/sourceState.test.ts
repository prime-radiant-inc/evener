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
  draftStorageKey,
  markDraftEdited,
  readComposerDraft,
  readComposerDraftRevisionId,
  readCompositionImageRecord,
  readDraftRevision,
  writeComposerDraft,
  writeCompositionImageRecord,
} from "./draft";
import { resetPendingTurnsStoreForTests } from "./queue/pendingTurnsStore";
import { mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recovery/recoveryDraft";
import { sendableAttachments } from "./submitProtocol";

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

// A rejected recovery row exactly as the outbox stores it. No composer
// snapshot means the record cannot say which `[image N]` its bytes belonged
// to - the shape the high describes.
function recoveryRecord(input: InputItem[]): MutationRecoveryRecord {
  return {
    version: 1,
    clientMutationId: "mutation-1",
    intentSequence: 1,
    createdAt: 1,
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
    state: "submitting",
    recoveryKind: "rejected",
  };
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
      id: `staged-${marker}`,
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

// RoboRev round-1 M5: an edit-recovery born of a retargeted row is
// associated with the displayed session by displayedRef while its durable
// destination is the resumed session. The displayed composer must activate
// its associated record, or the row the strip surfaced stays inert. The
// fixture is built the way the product builds it: the recovery row is the
// canceled retargeted RESEND the strip surfaces (displayedRef is the
// resend's displayed barrier, never a retarget stamp), moved to recovery by
// the strip's own Edit.
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

// Identical text and identical tiles are not identical compositions when the
// FACTS differ. One pane deletes a missing reference and saves the same
// spelling as literal prose; a stale pane still holds the old tag against
// otherwise identical content. Storage reuses the peer's row - the comparison
// covers text, mentions and tiles, not tags - and this continuation then
// overwrote that row's facts with the stale pane's, so reopening the peer's
// draft blocked a Send the peer had already fixed.
test("reusing a peer's row does not overwrite its image facts with a stale pane's", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/queue",
    payload: { ref: "root", input: [{ type: "text", text: "same [image 1]" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "same [image 1]" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "editing");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  // This pane's composition carries a missing reference on marker 1.
  writeCompositionImageRecord(original.clientMutationId, { anchors: [1], tiles: [] });
  const source = createTestComposerSource("root");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
    // The peer makes the SAME text durable - the compositions match - and
    // retires the tag, because for it the spelling is ordinary prose.
    const activated = await peer.getRecovery(original.clientMutationId);
    if (!activated) throw new Error("peer cannot see the row");
    const advanced = await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text: "same [image 1]" }],
      undefined,
      "same [image 1]",
      undefined,
      undefined,
      undefined,
      undefined,
      {
        [original.clientMutationId]:
          (activated as MutationRecoveryRecord & { compositionRevision?: number }).compositionRevision ?? 0,
      },
    );
    expect(advanced).toBeDefined();
    writeCompositionImageRecord(original.clientMutationId, { anchors: [], tiles: [] });

    // This pane's save now loses the lease and preserves its own work, and
    // storage reuses the peer's row because the composition is identical.
    await source.queueRecoveryPersistence(original.clientMutationId, "same [image 1]", [], []);
    await source.flushRecoveryWrites();

    // The reused row keeps its own facts: the peer's delete is a decision, not
    // a stale opinion this pane may overwrite.
    expect(readCompositionImageRecord(original.clientMutationId)?.anchors).toEqual([]);
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// A number is a label the text uses, not a durable key: the same `[image 1]`
// can appear twice in one draft - one the anchor whose bytes are gone, one a
// literal spelling that was always prose. Retiring the tag by "is this number
// still spelled anywhere" answers yes on the strength of the OTHER occurrence,
// so deleting the real reference leaves Send disabled forever.
//
// The anchor here is the SECOND `[image 1]`; deleting it must retire the tag
// even though the first spelling is still in the text. The row's record can
// only name a number, so it is read as the first occurrence it could have
// meant - which is why this fixture makes the anchor its own text's first
// occurrence and the PROSE the second: the recorded number then resolves to
// the occurrence that is genuinely the anchor.
test("deleting an anchor retires its tag even while the same spelling survives as prose", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const text = "anchor [image 1] here\n\nqueued [image 1] literal";
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const row = await storage.getRecovery(original.clientMutationId);
  if (!row) throw new Error("missing recovery fixture");
  // One reference with no bytes. The text spells the number twice, so a
  // number-only rule cannot tell which occurrence this describes.
  writeCompositionImageRecord(original.clientMutationId, { anchors: [1], tiles: [] });

  const source = createTestComposerSource("root");
  try {
    // Adopting the row installs its anchors, which is the path a reopen takes.
    const activated = await source.activateRecovery(row);
    expect(activated).toBe(true);
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);

    // The person removes the ANCHOR - the first occurrence - and leaves the
    // second spelling alone as prose.
    source.updateText("anchor removed here\n\nqueued [image 1] literal");

    // Send is restored: nothing in this text is an anchor any more.
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([]);
  } finally {
    source.dispose();
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
test("a recovered image renumbered by the merge stays sendable under its new number", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const input: InputItem[] = [
    { type: "text", text: "row prose" },
    { type: "image", mediaType: "image/png", data: "AQID", name: "recovered.png" },
  ];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [
      {
        presentationId: "presentation-1",
        marker: 1,
        name: "recovered.png",
        mediaType: "image/png",
        blob: new Blob([Uint8Array.of(1, 2, 3)], { type: "image/png" }),
      },
    ],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");

  // This composer already holds its own image 1, so the row's image collides
  // with it and the merge hands it the next free number.
  const source = createTestComposerSource("root");
  source.attachments.setState({
    items: [{ id: "local-image", marker: 1, name: "local.png", mediaType: "image/png", data: "BAUG", pending: false }],
  });
  source.editor.write("[image 1] local", 9);

  try {
    await expect(source.activateRecovery(recovery)).resolves.toBe(true);
    const staged = source.attachments.getState().items;
    const recovered = staged.find((item) => item.name === "recovered.png");
    if (!recovered) throw new Error("the merge dropped the recovered image");
    expect(recovered.marker).not.toBe(1);
    expect(source.getSnapshot().restoredImageIds).toEqual([recovered.id]);
    // The row's text never spells the new number, so only the identity-keyed
    // exemption can keep these bytes on the send that consumes the row.
    expect(
      sendableAttachments(staged, source.getSnapshot().text, {
        foreign: source.getSnapshot().foreignImageIds,
        restored: source.getSnapshot().restoredImageIds,
      }).map((item) => item.name),
    ).toContain("recovered.png");
  } finally {
    source.dispose();
    storage.close();
  }
});

// Opening a row through explicit Edit, with an image already staged, must
// deliver BOTH images: the row's renumbered one and this draft's own. A
// renumbering fix that only exempts the recovered tile would silently drop the
// local one; one that only exempts the local tile would lose the row's bytes
// when the resend consumes it. Literal marker prose the row carried as text
// stays text.
test("explicit Edit beside a staged image sends both images and keeps literal prose", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const input: InputItem[] = [
    { type: "text", text: "row prose" },
    { type: "image", mediaType: "image/png", data: "AQID", name: "recovered.png" },
  ];
  const original = await storage.enqueueIntent({
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [
      {
        presentationId: "presentation-1",
        marker: 1,
        name: "recovered.png",
        mediaType: "image/png",
        blob: new Blob([Uint8Array.of(1, 2, 3)], { type: "image/png" }),
      },
    ],
    optimisticDisplay: { method: "turn/start", input },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");

  const source = createTestComposerSource("root");
  source.attachments.setState({
    items: [{ id: "local-image", marker: 1, name: "local.png", mediaType: "image/png", data: "BAUG", pending: false }],
  });
  source.editor.write("[image 1] local", 9);

  try {
    await expect(source.activateRecovery(recovery)).resolves.toBe(true);
    const snapshot = source.getSnapshot();
    const staged = source.attachments.getState().items;
    const sendable = sendableAttachments(staged, snapshot.text, {
      foreign: snapshot.foreignImageIds,
      restored: snapshot.restoredImageIds,
    });
    // Both images travel. The row's tile was renumbered; this draft's tile
    // keeps its own number and is spelled in the text.
    expect(sendable.map((item) => item.name).sort()).toEqual(["local.png", "recovered.png"]);
    // The row's prose is still prose: nothing turned it into a second
    // reference to the local image.
    expect(snapshot.text).toContain("row prose");
  } finally {
    source.dispose();
    storage.close();
  }
});

// The high: editing a second recovery row into a composer that already holds
// an exempt image must not drop the first image's exemption. The tile is still
// staged and still exempt, so the resend that consumes the row must carry it.
// The high: a row with no composer snapshot cannot say which `[image N]` its
// bytes belonged to, so its text may spell the image's number as a literal the
// merge then renumbers away. Reading that prose as the answer would leave the
// renumbered tile unexempt, and the next autosave would write the row without
// the image's bytes - data loss before anyone even presses Send.
test("a restored image from a row without recorded references stays eligible after a renumbering merge", async () => {
  const source = createTestComposerSource("root");
  try {
    const staged = {
      id: "staged",
      marker: 1,
      name: "current.png",
      mediaType: "image/png",
      data: "CURRENT",
      pending: false,
    };
    source.attachments.setState({ items: [staged] });
    source.editor.write("current [image 1]", 15);

    const row = recoveryComposerDraft(
      recoveryRecord([
        { type: "text", text: "legacy [image 1] stays literal" },
        { type: "image", mediaType: "image/png", data: "LEGACY", name: "legacy.png" },
      ]),
    );
    const merged = mergeRecoveryComposerDraft(source.editor.read().text, source.attachments.getState().items, row);

    // The merge gave the recovered image marker 2, while the record's own prose
    // still spells 1 - the literal the person sees.
    expect(merged.attachments.map((item) => item.marker)).toEqual([2, 3]);
    source.attachments.setState({ items: merged.attachments });
    source.adoptRecoveredImageFacts(row, null);

    // Whatever number the merge chose, the bytes the row carried are sendable.
    const snapshot = source.getSnapshot();
    const sendable = sendableAttachments(source.attachments.getState().items, merged.text, {
      foreign: snapshot.foreignImageIds,
      restored: snapshot.restoredImageIds,
    });
    expect(sendable.map((item) => item.data)).toEqual(expect.arrayContaining(["LEGACY"]));
  } finally {
    source.dispose();
  }
});

test("an exemption survives a second recovery adoption while its tile stays staged", async () => {
  const source = createTestComposerSource("root");
  try {
    const first = { id: "first", marker: 1, name: "first.png", mediaType: "image/png", data: "AQID", pending: false };
    source.attachments.setState({ items: [first] });
    source.editor.write("first row", 9);
    await act(async () => {
      source.adoptRecoveredImageFacts({ text: "first row", attachments: [first], skillNames: [] }, null);
    });
    expect(source.getSnapshot().restoredImageIds).toEqual(["first"]);

    // A second row is edited in. It declares no images at all, and the merge
    // keeps the first tile staged.
    await act(async () => {
      source.adoptRecoveredImageFacts({ text: "first row\n\nsecond row", attachments: [], skillNames: [] }, null);
    });
    const snapshot = source.getSnapshot();
    expect(snapshot.restoredImageIds).toEqual(["first"]);
    expect(
      sendableAttachments(source.attachments.getState().items, snapshot.text, {
        foreign: snapshot.foreignImageIds,
        restored: snapshot.restoredImageIds,
      }).map((item) => item.id),
    ).toEqual(["first"]);
  } finally {
    source.dispose();
  }
});

test("a new image reusing a retired number gets ordinary submission rules", async () => {
  const source = createTestComposerSource("root");
  try {
    // A recovery row restores image 1 whose text never spells it: that tile
    // is exempt from the text-spelling rule.
    const restoredTile = {
      id: "first",
      marker: 1,
      name: "first.png",
      mediaType: "image/png",
      data: "AQID",
      pending: false,
    };
    source.attachments.setState({ items: [restoredTile] });
    source.editor.write("row prose", 9);
    await act(async () => {
      source.adoptRecoveredImageFacts({ text: "row prose", attachments: [restoredTile], skillNames: [] }, null);
    });
    expect(source.getSnapshot().restoredImageIds).toEqual(["first"]);
    expect(
      sendableAttachments([restoredTile], "row prose", { foreign: [], restored: ["first"] }).map((item) => item.id),
    ).toEqual(["first"]);

    // The tile goes away and the numbering restarts. Nothing recorded for it
    // may apply to the image that now bears number 1.
    await act(async () => {
      source.attachments.setState({ items: [] });
    });
    expect(source.getSnapshot().restoredImageIds).toEqual([]);
    const replacement = {
      id: "second",
      marker: 1,
      name: "second.png",
      mediaType: "image/png",
      data: "BAUG",
      pending: false,
    };
    source.attachments.setState({ items: [replacement] });
    const facts = source.getSnapshot();
    // With no reference spelled and no inherited exemption, this tile is not
    // sendable: it is ordinary content, not a restored anchor.
    expect(
      sendableAttachments([replacement], "row prose", {
        foreign: facts.foreignImageIds,
        restored: facts.restoredImageIds,
      }),
    ).toEqual([]);
  } finally {
    source.dispose();
  }
});

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
      const settled = source.attachments.getState().items;
      expect(settled).toHaveLength(1);
      // Identity is minted per attachment, so it is asserted present rather
      // than literal; every other field stays exact.
      expect(settled[0]?.id).toEqual(expect.any(String));
      expect(settled.map(({ id: _id, ...item }) => item)).toEqual([
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

// The high: a pane holding its own image 1 adopts a peer's newer draft that
// spells "[image 1]" as literal prose. Marker numbers are not identity, so the
// retained tile is a DIFFERENT image and the peer's reference never meant it.
// Editing a recovery row on top used to merge the whole staged bag, renumber
// the retained tile to dodge the collision and rewrite the peer's prose into a
// reference carrying the wrong bytes - then commit that tile as the row's own,
// so a reopen before the next autosave sent it.
//
// The selection that decides what a send would carry must therefore apply
// BEFORE the merge and before the durable write. The tile stays staged in the
// pane; it simply is not this composition's image.
test("a retained foreign image cannot enter a recovery merge or its durable row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    // The row's own text spells "[image 1]" as literal prose. That collision is
    // what makes a bare merge renumber the retained tile and rewrite the text.
    const queuedText = "queued [image 1] prose";
    const original = await storage.enqueueIntent({
      targetRef: "root",
      method: "turn/start",
      payload: { ref: "root", input: [{ type: "text", text: queuedText }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: queuedText }] },
    });
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    const row = await storage.getRecovery(original.clientMutationId);
    if (!row) throw new Error("missing recovery fixture");

    // This pane mounts on its own draft and stages its own image 1...
    writeComposerDraft("root", { text: "local [image 1]", skillNames: [] });
    const source = createTestComposerSource("root");
    try {
      source.attachments.setState({
        items: [{ id: "retained", marker: 1, name: "old.png", mediaType: "image/png", data: "OLD", pending: false }],
      });
      // ...then a peer writes a NEWER draft that spells "[image 1]" as prose.
      // The mount's own draft is now stale, so binding the editor adopts the
      // peer's text and records everything already staged as foreign.
      const peerText = "peer [image 1] prose";
      writeComposerDraft("root", { text: peerText, skillNames: [] });
      source.bindEditor({
        read: () => source.getSnapshot().text,
        write: (text: string) => source.updateText(text),
        focus: () => {},
        setSelection: () => {},
        getSelection: () => ({ start: 0, end: 0 }),
      } as never);
      expect(source.getSnapshot().text).toBe(peerText);

      await expect(source.activateRecovery(row)).resolves.toBe(true);

      // The peer's prose is still prose: the merge did not renumber the
      // retained tile around it and turn its number into a reference.
      expect(source.getSnapshot().text).toContain("peer [image 1] prose");
      expect(source.getSnapshot().text).toContain("queued [image 1] prose");
      const saved = await storage.getRecovery(original.clientMutationId);
      // Nothing durable claims the retained tile: it is not this row's image,
      // and a reopen before the follow-up autosave must not find it eligible.
      const durableInput = (saved?.payload as { input?: InputItem[] } | undefined)?.input ?? [];
      const durableImages = durableInput.flatMap((item) => (item.type === "image" ? [item.data] : []));
      expect(durableImages).not.toContain("OLD");
      // The tile is still staged in the pane - preserved, not destroyed.
      expect(source.attachments.getState().items.map((item) => item.id)).toContain("retained");
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
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

// The high: a missing image adopted from a persisted draft is an anchor with no
// bytes. When an Edit merges that draft into a recovery row, the row owns the
// text - and its own attachment list holds only tiles that still have bytes, so
// a marker whose bytes were already gone has no entry to describe it. If the
// tag does not travel with the composition, the reopen reads the row's text
// with nothing declaring the anchor and Send delivers it as literal prose, with
// no image and no refusal. The person sees a broken image and the model gets a
// sentence.
//
// The tag must therefore be filed against the composition the row now owns,
// before the local draft that held it is cleared, and restored when the row is
// opened again - explicitly here, and automatically on mount by the same call.
// The Medium: a row that owns a missing-image draft must record EVERY committed
// edit, including the one that deletes the last reference. Skipping the empty
// answer leaves the previous one standing, so a reopen blocks the same spelling
// typed later as ordinary prose.
test("deleting the last missing reference retires the row's tag", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "anchor [image 1]" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "anchor [image 1]" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const row = await storage.getRecovery(original.clientMutationId);
  if (!row) throw new Error("missing recovery fixture");
  // The composition declared one anchor and no tiles: a reference with no
  // bytes. Seeded through the record so the test exercises the same shape the
  // writer produces.
  writeCompositionImageRecord(original.clientMutationId, { anchors: [1], tiles: [] });

  const source = createTestComposerSource("root");
  try {
    source.setActiveRecoveryId(row);
    // The person deletes the reference and leaves prose behind.
    source.updateText("anchor removed");
    await source.queueRecoveryPersistence(original.clientMutationId, "anchor removed", [], []);
    await source.flushRecoveryWrites();

    // The tag is retired, not carried: the committed text spells no reference.
    expect(readCompositionImageRecord(original.clientMutationId)?.anchors).toEqual([]);
  } finally {
    source.dispose();
    storage.close();
  }
});

// A tile whose reference the person deleted is still theirs: it stays staged,
// and its BYTES must survive a reload - they never removed the image. What must
// not survive is their eligibility. Persisting the tile and deciding "sendable,
// because it exists" are different questions; answering both with one filter
// is how a released image is lost on one path and sent on the other.
test("a released image's bytes survive into recovery without becoming sendable", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "caption" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "caption" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const row = await storage.getRecovery(original.clientMutationId);
  if (!row) throw new Error("missing recovery fixture");

  const source = createTestComposerSource("root");
  try {
    source.setActiveRecoveryId(row);
    // A tile the row restored, whose reference the person replaced with prose.
    source.attachments.setState({
      items: [{ id: "kept", marker: 1, name: "kept.png", mediaType: "image/png", data: "KEPT", pending: false }],
    });
    source.updateText("caption with no reference");

    await source.queueRecoveryPersistence(original.clientMutationId, "caption with no reference", [], []);
    await source.flushRecoveryWrites();

    // The bytes are durable: the tile was never removed.
    const saved = await storage.getRecovery(original.clientMutationId);
    const savedInput = (saved?.payload as { input?: InputItem[] } | undefined)?.input ?? [];
    const savedImages = savedInput.flatMap((item) => (item.type === "image" ? [item.data] : []));
    expect(savedImages).toContain("KEPT");
  } finally {
    source.dispose();
    storage.close();
  }
});

// A stale lease is preserved as a COMPOSITION: its text, its tiles and the
// anchors it adopted, taken together. Reading the anchors back off the row it
// is being preserved FROM was the defect - the peer that advanced that row
// replaced them, so the fork walked away carrying its own text and the PEER's
// answer to "which reference in this text had no bytes". A reopen then either
// blocked prose the fork never tagged, or dropped a tag the fork did carry.
// Two compositions can agree on their text and their tiles and still not be the
// same composition: one says a `[image 1]` is an anchor with no bytes, the other
// says the same spelling is prose. Reusing the row for the second restores the
// warning the person already repaired, and the draft refuses a send it should
// allow. The facts are part of the composition, so the reuse comparison asks
// about them too.
test("a preserved fork whose image facts differ gets its own row, not the peer's", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const text = "same [image 1]";
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/queue",
    payload: { ref: "root", input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "editing");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  // The row as it stands: this spelling is an anchor with no bytes.
  writeCompositionImageRecord(original.clientMutationId, { anchors: [1], tiles: [] });

  const source = createTestComposerSource("root");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    // The peer retires the tag: for it the spelling is ordinary prose. Text and
    // tiles are unchanged, so only the facts move.
    const activated = await peer.getRecovery(original.clientMutationId);
    if (!activated) throw new Error("peer cannot see the row");
    const advanced = await peer.updateRecoveryInput(
      original.clientMutationId,
      [{ type: "text", text }],
      undefined,
      text,
      undefined,
      undefined,
      undefined,
      undefined,
      {
        [original.clientMutationId]:
          (activated as MutationRecoveryRecord & { compositionRevision?: number }).compositionRevision ?? 0,
      },
    );
    expect(advanced).toBeDefined();
    writeCompositionImageRecord(original.clientMutationId, { anchors: [], tiles: [] });

    // This pane still holds its own tag and now loses the lease.
    await source.queueRecoveryPersistence(original.clientMutationId, text, [], []);
    await source.flushRecoveryWrites();

    const preserved = (await storage.listRecovery("root")).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    if (!preserved) throw new Error("the stale composition was not preserved in its own row");
    // The fork keeps the anchor it actually held, in a row of its own.
    expect(readCompositionImageRecord(preserved.clientMutationId)?.anchors).toEqual([1]);
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

test("a stale editor's preserved fork keeps its own anchors, not the peer's", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/queue",
    payload: { ref: "root", input: [{ type: "text", text: "anchor [image 1]" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "anchor [image 1]" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "editing");
  const recovery = await storage.getRecovery(original.clientMutationId);
  if (!recovery) throw new Error("missing recovery fixture");
  // This pane's composition carries a missing reference on 1.
  writeCompositionImageRecord(original.clientMutationId, { anchors: [1], tiles: [] });

  const source = createTestComposerSource("root");
  const peer = new MutationOutboxIndexedDB();
  try {
    await source.activateRecovery(recovery);
    source.updateText("anchor [image 1] still mine");
    // The peer advances the same row and retires its tag: from here on the
    // row's record answers for the PEER's composition.
    const peerText = "peer text, no reference";
    // Activation committed its own revision, so the peer must name the row's
    // CURRENT one or the write is refused as stale.
    const activated = await peer.getRecovery(original.clientMutationId);
    if (!activated) throw new Error("peer cannot see the row");
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
          (activated as MutationRecoveryRecord & { compositionRevision?: number }).compositionRevision ?? 0,
      },
    );
    expect(newer).toBeDefined();
    writeCompositionImageRecord(original.clientMutationId, { anchors: [], tiles: [] });

    // This pane's save now loses the lease and preserves its own work.
    await source.queueRecoveryPersistence(original.clientMutationId, "anchor [image 1] still mine", [], []);
    await source.flushRecoveryWrites();

    const preserved = (await storage.listRecovery("root")).find(
      (record) => record.clientMutationId !== original.clientMutationId,
    );
    if (!preserved) throw new Error("the stale composition was not preserved");
    // The fork carries its OWN anchor, so a reopen blocks the send it should.
    expect(readCompositionImageRecord(preserved.clientMutationId)?.anchors).toEqual([1]);
    // And the peer's row is untouched.
    expect(await storage.getRecovery(original.clientMutationId)).toMatchObject({ composerText: peerText });
  } finally {
    source.dispose();
    peer.close();
    storage.close();
  }
});

// A tile whose reference the person deleted is still theirs: it stays staged.
// It must NOT be handed a number the merged text spells, because those words
// are then a reference to it - Send would carry bytes the prose never asked
// for, and removing the tile would edit words the person typed. The number is
// a label; reserving only the numbers other TILES hold misses the numbers the
// TEXT already uses.
test("a released tile is not renumbered onto a recovery row's literal marker", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "queued [image 1] prose" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "queued [image 1] prose" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const row = await storage.getRecovery(original.clientMutationId);
  if (!row) throw new Error("missing recovery fixture");

  const source = createTestComposerSource("root");
  try {
    // This pane holds its own image 1, whose editor reference the person has
    // already deleted. The tile is released, not removed.
    source.attachments.setState({
      items: [{ id: "released", marker: 1, name: "old.png", mediaType: "image/png", data: "OLD", pending: false }],
    });
    source.updateText("");
    expect(source.getSnapshot().text).toBe("");

    await expect(source.activateRecovery(row)).resolves.toBe(true);

    // The row's text spells "[image 1]" as a literal in the record, and the
    // released tile is staged on 1. Handing the tile 1 back makes the row's
    // own words a reference to it, so it must take a free number instead.
    const items = source.attachments.getState().items;
    const released = items.find((item) => item.id === "released");
    if (!released) throw new Error("released tile was destroyed instead of preserved");
    expect(items.filter((item) => item.marker === 1)).toEqual([]);
    expect(released.marker).not.toBe(1);
  } finally {
    source.dispose();
    storage.close();
  }
});

test("a missing-image tag merged into a recovery row survives the reopen", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    const original = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/start",
      payload: { ref: "root", input: [{ type: "text", text: "queued prose" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "queued prose" }] },
    });
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    const row = await storage.getRecovery(original.clientMutationId);
    if (!row) throw new Error("missing recovery fixture");

    // A draft on disk whose record names marker 1 as having had bytes - the
    // shape a reload of an image-bearing draft leaves behind. No source holds
    // those bytes any more.
    const draftText = "look [image 1] here";
    writeComposerDraft("root", { text: draftText, skillNames: [] }, [1]);

    const source = createTestComposerSource("root");
    try {
      // The mount read adopts the anchor: the record says marker 1 was an
      // image, so this source holds a reference with no bytes behind it.
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
      // Edit merges the draft into the row...
      await expect(source.activateRecovery(row)).resolves.toBe(true);
      // ...and the follow-up autosave commits that composition, then clears the
      // local draft it incorporated. This is the window the high describes: the
      // row owns the text and the tag has to have been filed with it already.
      await source.queueRecoveryPersistence(original.clientMutationId, source.getSnapshot().text, [], []);
      await source.flushRecoveryWrites();
    } finally {
      source.dispose();
    }

    // The local draft is gone; the row owns the text.
    expect(readComposerDraft("root").text).toBe("");
    expect(readComposerDraftRevisionId("root")).toBeUndefined();

    const reopened = createTestComposerSource("root");
    try {
      const saved = await storage.getRecovery(original.clientMutationId);
      if (!saved) throw new Error("missing saved recovery row");
      await expect(reopened.activateRecovery(saved)).resolves.toBe(true);
      expect(reopened.getSnapshot().text).toContain("[image 1]");
      // The anchor is still an anchor: the row restored the text, and nothing
      // restored bytes, so the reference must still refuse to send as prose.
      expect(reopened.getSnapshot().unbackedImageMarkers).toEqual([1]);
    } finally {
      reopened.dispose();
    }
  } finally {
    storage.close();
  }
});

// The high: a released tile survives a reopen and is STILL not sendable.
//
// The record stores its facts against the attachment id that existed when the
// composition was written. Decoding a row mints a fresh id per tile, so every
// reader that looks the stored id up afterwards finds nothing - and the tile
// silently regains the markerless-image exemption, which lets the next Send
// carry an image whose reference the person deleted. Identity has to be
// assigned by the same step that reads the facts about it.
test("a released tile stays ineligible after the row is reopened", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    const original = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/start",
      payload: { ref: "root", input: [{ type: "text", text: "caption" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "caption" }] },
    });
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    const row = await storage.getRecovery(original.clientMutationId);
    if (!row) throw new Error("missing recovery fixture");

    const source = createTestComposerSource("root");
    try {
      source.setActiveRecoveryId(row);
      // A tile this composition owns whose reference the person deleted.
      source.attachments.setState({
        items: [{ id: "released", marker: 1, name: "kept.png", mediaType: "image/png", data: "KEPT", pending: false }],
      });
      source.updateText("caption with no reference");
      await source.queueRecoveryPersistence(original.clientMutationId, "caption with no reference", [], []);
      await source.flushRecoveryWrites();
    } finally {
      source.dispose();
    }

    // Reopen: the bytes come back, so the tile is on screen - and the text does
    // not ask for them, so a Send must not carry them.
    const reopened = createTestComposerSource("root");
    try {
      const saved = await storage.getRecovery(original.clientMutationId);
      if (!saved) throw new Error("missing saved recovery row");
      await expect(reopened.activateRecovery(saved)).resolves.toBe(true);
      const items = reopened.attachments.getState().items;
      const restored = items.find((item) => item.name === "kept.png");
      if (!restored) throw new Error("the released tile's bytes did not survive the reopen");
      const sendable = sendableAttachments(items, reopened.getSnapshot().text, {
        foreign: reopened.getSnapshot().foreignImageIds,
        restored: reopened.getSnapshot().restoredImageIds,
      });
      expect(sendable.map((item) => item.id)).toEqual([]);
    } finally {
      reopened.dispose();
    }
  } finally {
    storage.close();
  }
});
