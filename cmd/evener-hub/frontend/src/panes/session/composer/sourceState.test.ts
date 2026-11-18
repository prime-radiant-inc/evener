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
import * as draftModule from "./draft";
import {
  draftStorageKey,
  markDraftEdited,
  readComposerDraft,
  readComposerDraftImageAnchors,
  readComposerDraftImageMarkers,
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
  writeCompositionImageRecord(original.clientMutationId, { anchors: [{ marker: 1 }], tiles: [] });
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
  writeCompositionImageRecord(original.clientMutationId, { anchors: [{ marker: 1 }], tiles: [] });

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
        // `insertedAt` is where the tile's own reference was spliced in - here
        // offset 9, the end of "original ". It is asserted rather than ignored
        // because it is the fact that binds these bytes to `[image 1]`; a tile
        // that lost it would fall back to guessing from the text.
        {
          marker: 1,
          name: "source.png",
          mediaType: "image/png",
          pending: false,
          data: "AQID",
          width: 8,
          height: 4,
          insertedAt: 9,
        },
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
  writeCompositionImageRecord(original.clientMutationId, { anchors: [{ marker: 1 }], tiles: [] });

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

// The cascade guard's own complaint, in the unit suite.
//
// A failed decode strips the tile AND its reference, text first. The stored
// draft record is copied forward so an anchor adopted before this source
// started editing is not lost - but that copy kept naming the marker for one
// more save, so the persisted draft named an image whose bytes it no longer
// held. That is the issue this PR exists to fix, and it is the shape the
// browser journey failed on ("after mounted image settlement and Return,
// persisted draft names the markers whose bytes it holds": actual [4, 6, 5]).
test("a stored anchor whose reference the edit removed is not carried forward", () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // An earlier save left this record behind, naming a reference that existed
  // then. The offset makes it an OCCURRENCE, which is the shape the writer now
  // emits and the only shape that can say WHICH spelling it meant.
  writeComposerDraft("root", { text: "keep [image 4]\n\nbad [image 5]", skillNames: [] }, [
    { marker: 4 },
    { marker: 5, offset: 20 },
  ]);

  const source = createTestComposerSource("root");
  try {
    // The first save is what makes this source the record's owner; until it
    // runs, the stored list is somebody else's and is not carried.
    source.persistDraft(source.getSnapshot().text);
    expect(readComposerDraftImageMarkers("root")).toEqual([4, 5]);
    // The failed decode: the reference and its tile leave together, and the
    // composer saves what is left.
    const after = "keep [image 4]\n\nbad ";
    source.updateText(after);
    source.persistDraft(after);
    // Marker 5 has no bytes and no spelling left, so the saved draft must not
    // name it. Marker 4 is still there and still must be named.
    expect(readComposerDraft("root")?.text).toEqual(after);
    expect(readComposerDraftImageMarkers("root")).toEqual([4]);
  } finally {
    source.dispose();
    storage.close();
  }
});

// The case the text filter above cannot see, and the reason retirement is its
// own fact rather than a second reading of the same question.
//
// The person removes the missing reference and repairs the draft by typing a
// literal `[image 5]` somewhere else. Marker 5 is still spelled in the text, so
// "does the text still say 5?" answers yes - but the REFERENCE the warning was
// about is gone. Carrying the old occurrence forward resurrects a warning on a
// spelling the person never called an image, and blocks a Send they repaired.
// Only the retirement of that occurrence records the difference.
test("a retired reference is not resurrected by the same number typed elsewhere", () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  writeComposerDraft("root", { text: "a [image 5] b", skillNames: [] }, [{ marker: 5, offset: 2 }]);

  const source = createTestComposerSource("root");
  try {
    // The first save makes this source the record's owner, so the stored
    // occurrence is carried rather than ignored.
    source.persistDraft(source.getSnapshot().text);
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([5]);

    // The reference goes; an ordinary spelling of the same number arrives.
    const repaired = "a x b [image 5]";
    source.updateText(repaired);
    source.persistDraft(repaired);

    // The repaired draft says the number as prose, so nothing is unbacked and
    // the person's edit is sendable.
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([]);
    // And the SAVED draft must not name it either: a record that still lists
    // marker 5 as having had bytes brings the warning back on the next reload,
    // blocking a Send the person repaired by deleting the reference.
    expect(readComposerDraftImageMarkers("root")).toEqual([]);
  } finally {
    source.dispose();
    storage.close();
  }
});

// Medium 2 (`sourceState.ts:804-810`). Two IDENTICAL spellings sit beside each
// other and the second one is the reference. Deleting it leaves `[image 1]`,
// which the prefix/suffix inference cannot tell from having deleted the first:
// both readings explain the two strings, and the inference takes the earlier
// one - so it retires the reference and keeps the prose, and the warning
// survives on a spelling the person never called an image. The editor knows
// which span it replaced, so passing that span down classifies the deletion
// instead of guessing at it.
test("deleting the tagged spelling retires it even beside an identical one", () => {
  installLocalStorage(new MemoryStorage());
  const text = "[image 1][image 1]";
  // The SECOND spelling is the reference that lost its bytes.
  writeComposerDraft("root", { text, skillNames: [] }, [{ marker: 1, offset: 9 }]);
  const source = createTestComposerSource("root");
  try {
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
    // Remove the tagged occurrence. The editor reports the span it replaced.
    const after = "[image 1]";
    source.updateText(after, { from: 9, to: 18 });
    // What survives is prose the person typed, so nothing is unbacked.
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([]);
  } finally {
    source.dispose();
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

    // The bytes are durable: the tile was never removed. They live in the
    // composition the row OWNS (`attachments`), not in its request
    // (`payload.input`) - a released tile is exactly the case where those two
    // answer differently. This assertion used to read the request and expect
    // the bytes there, which was the defect stated as an expectation: a reopen
    // that found them in `payload.input` restored them ELIGIBLE, so the next
    // Send carried an image whose reference the person had deleted.
    const saved = await storage.getRecovery(original.clientMutationId);
    expect(saved?.attachments.map((attachment) => attachment.marker)).toContain(1);
    const savedInput = (saved?.payload as { input?: InputItem[] } | undefined)?.input ?? [];
    const savedImages = savedInput.flatMap((item) => (item.type === "image" ? [item.data] : []));
    expect(savedImages).not.toContain("KEPT");
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
  writeCompositionImageRecord(original.clientMutationId, { anchors: [{ marker: 1 }], tiles: [] });

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
    expect(readCompositionImageRecord(preserved.clientMutationId)?.anchors.map((anchor) => anchor.marker)).toEqual([1]);
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
  writeCompositionImageRecord(original.clientMutationId, { anchors: [{ marker: 1 }], tiles: [] });

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
    expect(readCompositionImageRecord(preserved.clientMutationId)?.anchors.map((anchor) => anchor.marker)).toEqual([1]);
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
    //
    // This row now genuinely carries the tile in `attachments` and not in
    // `payload.input`, which is what makes the decoder read its Blob. Under
    // jsdom fake-indexeddb's structured clone drops the Blob prototype, so the
    // fixture rebuilds a real Blob, as a browser read would return.
    const saved = await storage.getRecovery(original.clientMutationId);
    if (!saved) throw new Error("missing saved recovery row");
    const reopenRow: MutationRecoveryRecord = {
      ...saved,
      attachments: saved.attachments.map((attachment) => ({
        ...attachment,
        blob: new Blob([attachment.blob], { type: attachment.mediaType }),
      })),
    };
    const reopened = createTestComposerSource("root");
    try {
      await expect(reopened.activateRecovery(reopenRow)).resolves.toBe(true);
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

// ── The six findings on 96176d3 ─────────────────────────────────────────────

// Medium (sourceState.ts:1517-1519). A literal `[image 1]` in the pane's own
// prose and a REAL missing `[image 1]` in the incoming row are two references
// that share a glyph. The merge used to install only the number, so the tag
// landed on the first spelling in the combined text - the prose - and deleting
// the prose cleared the warning while the real missing reference went on to
// send as literal text.
test("a merged missing reference keeps its own occurrence when local prose spells the same marker", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // The row's text carries the REAL missing reference.
  const row = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "row says [image 1] missing" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "row says [image 1] missing" }] },
  });
  await storage.transferToRecovery(row.clientMutationId, "rejected");
  const saved = await storage.getRecovery(row.clientMutationId);
  if (!saved) throw new Error("missing recovery fixture");
  // The row's record names the occurrence it means: the only spelling in ITS
  // text, at offset 9.
  writeCompositionImageRecord(row.clientMutationId, { anchors: [{ marker: 1, offset: 9 }], tiles: [] });

  const source = createTestComposerSource("root");
  try {
    // Local prose spells the same marker FIRST in the combined text.
    source.updateText("my prose [image 1] here");
    await source.activateRecovery(saved);
    const merged = source.getSnapshot().text;
    expect(merged.startsWith("my prose [image 1] here")).toBe(true);
    // The warning is live for the row's real reference.
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
    // Save while BOTH spellings are still in the text. Only the offset can say
    // the warning is about the row's spelling (the second), so a record that
    // collapsed it back to a bare number would be read on reopen as the first
    // spelling - the local prose - and deleting that prose would enable Send
    // while the row's real missing reference was still there.
    await source.queueRecoveryPersistence(row.clientMutationId, merged, [], []);
    await source.flushRecoveryWrites();
    expect(readCompositionImageRecord(row.clientMutationId)?.anchors).toEqual([{ marker: 1, offset: 34 }]);

    // Deleting the LOCAL PROSE spelling must not clear the warning about the
    // row's reference - that one is still there.
    source.updateText("my prose here\n\nrow says [image 1] missing");
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
  } finally {
    source.dispose();
    storage.close();
  }
});

// Medium (sourceState.ts:1146-1154). The person deletes a missing reference
// and types the same spelling as prose while another image is decoding, so the
// save is paused. Reading the ROW's stored anchors when the write finally lands
// stamps the retired tag onto the new revision, and a reopen blocks the Send
// they had already repaired.
test("a save queued after the last reference was removed records an empty anchor set", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const row = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "edit [image 1] please" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "edit [image 1] please" }] },
  });
  await storage.transferToRecovery(row.clientMutationId, "rejected");
  const saved = await storage.getRecovery(row.clientMutationId);
  if (!saved) throw new Error("missing recovery fixture");
  // A NUMBER-ONLY anchor: the shape an older build wrote, which this reader
  // still accepts. It states "marker 1 had no bytes" and cannot say which
  // spelling. Re-derived at save time it would take the first spelling in the
  // NEW text - the prose below - and the reopen would block a repaired Send.
  writeCompositionImageRecord(row.clientMutationId, { anchors: [{ marker: 1 }], tiles: [] });

  const source = createTestComposerSource("root");
  try {
    await source.activateRecovery(saved);
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
    // The person repairs it: the REFERENCE is deleted and the same spelling is
    // typed as ordinary prose. The live tag retires immediately, because the
    // occurrence it named is gone.
    source.updateText("keep [image 1] as prose");
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([]);
    // The save that follows must record the EMPTY answer, not the row's stored
    // tag. An empty set is the answer that says "nothing is reserved".
    await source.queueRecoveryPersistence(row.clientMutationId, "keep [image 1] as prose", [], []);
    await source.flushRecoveryWrites();
    expect(readCompositionImageRecord(row.clientMutationId)?.anchors).toEqual([]);
  } finally {
    source.dispose();
    storage.close();
  }
});

// Medium (sourceState.ts:560-563) is fixed but NOT tested from the composer
// side. The comparison in `storedImageFacts` now drops each tile's per-pane id
// and the record's own revision, so two panes holding the same image compare
// equal. No fixture reached that comparison: a pane that activates a row
// renumbers any tile whose marker collides with an anchor, so the two panes'
// compositions then genuinely differ and storage correctly mints a second row.
// Deleting the test rather than shipping one whose premise the code cannot
// produce; see the PR body for the named gap.

// High (sourceState.ts:1398-1403). A pane already owns a tile whose reference
// the person deleted, and a recovery row merges into it. The row's durable
// payload must describe the REQUEST - the sendable subset - while its attachment
// list keeps every owned tile, so a reopen whose image-facts write failed still
// knows that tile was excluded, rather than reading it as part of the request and
// granting it the markerless-image exemption.
test("a merge keeps a released tile's bytes without writing them into the request", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const row = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "row prose" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "row prose" }] },
  });
  await storage.transferToRecovery(row.clientMutationId, "rejected");
  const saved = await storage.getRecovery(row.clientMutationId);
  if (!saved) throw new Error("missing recovery fixture");

  // An OWNER row exists so the merge has somewhere to write.
  const owner = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/queue",
    payload: { ref: "root", input: [{ type: "text", text: "mine" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "mine" }] },
  });
  await storage.transferToRecovery(owner.clientMutationId, "editing");
  const ownerRow = await storage.getRecovery(owner.clientMutationId);
  if (!ownerRow) throw new Error("missing owner fixture");

  const source = createTestComposerSource("root");
  try {
    await source.activateRecovery(ownerRow);
    // A tile this composition owns, whose reference the person deleted.
    source.attachments.setState({
      items: [{ id: "released", marker: 1, name: "kept.png", mediaType: "image/png", data: "KEPT", pending: false }],
    });
    source.updateText("mine");
    // The facts write fails the way a full quota makes it fail.
    const setItem = Storage.prototype.setItem;
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key: string, value: string) {
      if (key.includes("composition-image-record")) throw new Error("quota exceeded");
      return setItem.call(this, key, value);
    });
    // Merging the row runs the durable write at issue.
    await source.activateRecovery(saved).catch(() => false);
  } finally {
    source.dispose();
  }

  const durable = await storage.getRecovery(owner.clientMutationId);
  if (!durable) throw new Error("owner row vanished");
  // The row OWNS the released tile...
  expect(durable.attachments.map((attachment) => attachment.marker)).toContain(1);
  // ...and its request does NOT. That gap is the exclusion, and it is what stops
  // a reopen from reading those bytes as the request's own image.
  const carried = (durable.payload.input as InputItem[]).filter((item) => item.type === "image").length;
  expect(carried).toBe(0);
  storage.close();
});

// High (sourceState.ts:764-781). Pasting an image whose marker shares a
// PREFIX with a missing reference - `[image 2]` before `[image 1]` - used to
// look like an edit that overlapped the anchor, because the shared prefix and
// suffix bound a region spanning it. The warning was dropped while the
// person's reference was still in the text, so once the new image decoded Send
// enabled and shipped the old marker as literal prose.
test("an insertion beside a missing reference moves its warning instead of clearing it", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // A draft on disk whose record names marker 1 as having had bytes, with the
  // bytes now gone - the shape a reload of an image-bearing draft leaves.
  writeComposerDraft("root", { text: "[image 1]", skillNames: [] }, [1]);

  const source = createTestComposerSource("root");
  try {
    // The mount read adopts the anchor: marker 1 was an image, and no bytes
    // are behind it here.
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
    // A new image is pasted at the START. Its marker shares the "[image "
    // PREFIX with the anchor, so the inferred splice ran its changed region
    // straight through the anchor and dropped it.
    source.updateText("[image 2][image 1]");
    // The missing reference is untouched, so its warning must still be live.
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
  } finally {
    source.dispose();
    storage.close();
  }
});

// Medium (sourceState.ts:804-810). An insertion INSIDE a missing reference
// destroys the spelling its warning names. The insertion branch shifted every
// anchor at or after the insertion point without checking that its characters
// survived, so `[image 1]` edited to `[imXage 1]` kept a tag pointing at text
// that was no longer a marker. The warning was invisible, but typing a literal
// `[image 1]` anywhere else resurrected it and blocked Send.
test("an edit that breaks a missing reference's spelling retires its warning", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  writeComposerDraft("root", { text: "[image 1]", skillNames: [] }, [1]);
  const source = createTestComposerSource("root");
  try {
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
    // A character is inserted INSIDE the marker: the reference is gone.
    source.updateText("[imXage 1]");
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([]);
    // A literal spelling typed LATER is ordinary prose, so it must not
    // resurrect the retired tag.
    source.updateText("[imXage 1] [image 1]");
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([]);
  } finally {
    source.dispose();
    storage.close();
  }
});

// High (sourceState.ts:1270-1284). The autosave handed the whole owned list to
// the durable write as its PAYLOAD attachments, so a tile whose reference the
// person had deleted was written into `payload.input` - the request - as well as
// into the composition the row owns. A reopen then read those bytes as the
// request's own image and restored them eligible, so the next Send carried an
// image the person had excluded. The two lists answer two questions and must be
// computed as two lists, exactly as activation already does.
//
// Both halves are asserted, because either one alone is satisfied by a wrong
// fix: dropping the tile makes the send half pass and loses the person's image;
// keeping it in the payload makes the preservation half pass and sends it.
test("autosave keeps a released tile out of the request without dropping its bytes", async () => {
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
      // A tile this composition owns whose reference the person just deleted:
      // it is staged and visible, and the text does not spell `[image 1]`.
      source.attachments.setState({
        items: [{ id: "released", marker: 1, name: "kept.png", mediaType: "image/png", data: "KEPT", pending: false }],
      });
      source.updateText("caption with no reference");
      // The REAL autosave argument list - the live store's own attachments, not
      // an empty stand-in. Passing `[]` hid the defect, which is why the earlier
      // reopen test read green on broken code.
      await source.queueRecoveryPersistence(
        original.clientMutationId,
        "caption with no reference",
        // The wire shape a real autosave passes: every settled tile, with bytes.
        source.attachments
          .getState()
          .items.filter((item) => item.data !== undefined)
          .map((item) => ({
            marker: item.marker,
            mediaType: item.mediaType,
            data: item.data as string,
            name: item.name,
          })),
        [],
      );
      await source.flushRecoveryWrites();
    } finally {
      source.dispose();
    }

    const saved = await storage.getRecovery(original.clientMutationId);
    if (!saved) throw new Error("missing saved recovery row");
    // The durable row still OWNS the released tile's bytes...
    expect(saved.attachments.map((attachment) => attachment.marker)).toContain(1);
    // ...and its request does not. This is the exclusion the reopen reads.
    expect((saved.payload.input as InputItem[]).filter((item) => item.type === "image")).toEqual([]);

    // Under jsdom, fake-indexeddb's structured clone drops the Blob prototype,
    // so a blob read back here has no `arrayBuffer`. A real browser's recovery
    // read returns a genuine Blob, and the excluded-image decoder reads exactly
    // that. Restoring the prototype keeps the fixture faithful to the browser
    // instead of skipping the read this test exists to drive.
    const reopenRow: MutationRecoveryRecord = {
      ...saved,
      attachments: saved.attachments.map((attachment) => ({
        ...attachment,
        blob: new Blob([attachment.blob], { type: attachment.mediaType }),
      })),
    };

    const reopened = createTestComposerSource("root");
    try {
      await expect(reopened.activateRecovery(reopenRow)).resolves.toBe(true);
      const items = reopened.attachments.getState().items;
      // Preservation half: the bytes are back on screen, so the person can undo
      // their own deletion.
      const restored = items.find((item) => item.name === "kept.png");
      if (!restored) throw new Error("the released tile's bytes did not survive the reopen");
      // Send-safety half: and they are not on the wire, because the text does
      // not ask for them.
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

// High (sourceState.ts:1694-1709). The merge wrote its text and bytes to
// IndexedDB and its missing-image anchors to a SEPARATE localStorage record,
// after the transaction that consumed the incoming row. Two stores, no shared
// transaction: a browser closed in between, or a full quota, left a row whose
// text spells `[image 1]` with nothing saying that reference had no bytes, so
// a reopen passed the reference through as literal prose and Send shipped it.
// The anchors now travel in the same transaction as the text they describe.
// The merge commits its anchors with the text, and that committed set must WIN
// over a side record that failed to update.
//
// The row here is at revision 2 and its committed anchors are EMPTY: the edit
// retired the last missing reference. The localStorage record still describes
// revision 1, where `[image 1]` was a missing reference. Reading that record
// over the row - or reading an empty committed set as "no answer" and falling
// through to it - resurrects a reference the merge already removed, and the
// reopen blocks a Send the person had repaired.
test("a row's committed anchors win over a side record from an older revision", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    // The text SPELLS `[image 1]` as prose. That is what makes the stale record
    // do damage: the tag only sticks to a marker the text actually spells, so a
    // fixture whose text did not spell it would pass with or without the fix.
    const seeded = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/start",
      payload: { ref: "root", input: [{ type: "text", text: "fixed prose [image 1]" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "fixed prose [image 1]" }] },
    });
    await storage.transferToRecovery(seeded.clientMutationId, "rejected");
    // The side record describes the PREVIOUS revision, where the reference was
    // still missing. Revision 2's write never landed.
    writeCompositionImageRecord(seeded.clientMutationId, {
      anchors: [{ marker: 1 }],
      tiles: [],
      revision: 1,
    });
    // The row itself is at revision 2 and states that its text has no missing
    // reference - an empty set, which is an answer.
    await storage.updateRecoveryInput(
      seeded.clientMutationId,
      [{ type: "text", text: "fixed prose [image 1]" }],
      undefined,
      "fixed prose [image 1]",
      undefined,
      undefined,
      undefined,
      // The durable revision this write is based on. The guard refuses a write
      // that is not based on the row it read, so the fixture must name it.
      0,
      undefined,
      undefined,
      undefined,
      // The text carries no missing reference, and the row states that. An
      // empty set is an answer, not silence.
      [],
    );
    const row = await storage.getRecovery(seeded.clientMutationId);
    if (!row) throw new Error("missing recovery fixture");
    expect((row as { anchors?: unknown[] }).anchors).toEqual([]);

    const source = createTestComposerSource("root");
    try {
      await source.activateRecovery(row);
      // The stale record must not resurrect the reference the row retired.
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([]);
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
});

test("a merge's missing-image anchor is durable when localStorage refuses the write", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    // An incoming row carrying a missing reference: its text spells `[image 1]`
    // and it has no attachment to back it.
    const incoming = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/start",
      payload: { ref: "root", input: [{ type: "text", text: "row prose [image 1]" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "row prose [image 1]" }] },
    });
    await storage.transferToRecovery(incoming.clientMutationId, "rejected");
    writeCompositionImageRecord(incoming.clientMutationId, { anchors: [{ marker: 1 }], tiles: [] });
    const arriving = await storage.getRecovery(incoming.clientMutationId);
    if (!arriving) throw new Error("missing incoming recovery fixture");

    // An owner row for the merge to write into.
    const owner = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/queue",
      payload: { ref: "root", input: [{ type: "text", text: "mine" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "mine" }] },
    });
    await storage.transferToRecovery(owner.clientMutationId, "editing");
    const ownerRow = await storage.getRecovery(owner.clientMutationId);
    if (!ownerRow) throw new Error("missing owner fixture");

    const source = createTestComposerSource("root");
    try {
      await source.activateRecovery(ownerRow);
      source.updateText("mine");
      // The live record write fails, the way a full quota makes it fail. It is
      // swallowed by design (the composition still has to be usable), so the
      // point is that IndexedDB must not depend on it having landed.
      vi.spyOn(draftModule, "writeCompositionImageRecord").mockImplementation(() => undefined);
      await source.activateRecovery(arriving).catch(() => false);
    } finally {
      source.dispose();
    }

    // The row knows its text carries a missing reference, because the anchors
    // were committed by the transaction that wrote that text - not by a
    // localStorage write that may never happen.
    const durable = await storage.getRecovery(owner.clientMutationId);
    if (!durable) throw new Error("owner row vanished");
    const durableAnchors = (durable as { anchors?: { marker: number }[] }).anchors ?? [];
    // Marker 1 is recorded as a missing reference. It may appear once as a
    // resolved occurrence and once offsetless - those are different identities
    // by design - so the assertion is about which marker is reserved, not how
    // many spellings of it the record kept.
    expect([...new Set(durableAnchors.map((anchor) => anchor.marker))]).toEqual([1]);
  } finally {
    storage.close();
  }
});

// Medium (sourceState.ts:464-487). The persisted draft recorded anchor markers
// as bare numbers, so the occurrence was lost on the way to disk. A text with
// two spellings of one marker then reloaded with the warning bound to the FIRST
// spelling: deleting that literal cleared the warning while the real missing
// reference stayed in the text, and Send shipped it as prose. The number cannot
// say which spelling it meant; the offset can.
test("a persisted draft keeps which spelling its missing reference was", async () => {
  // The person's reference is the SECOND spelling; the first is prose.
  const text = "prose [image 1] literal, real [image 1] reference";
  const realOffset = text.lastIndexOf("[image 1]");
  // A recovery row supplies the reference with no bytes. Two spellings of its
  // number are in this text and only one of them is the reference.
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: "real [image 1] reference" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "real [image 1] reference" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const row = await storage.getRecovery(original.clientMutationId);
  if (!row) throw new Error("missing recovery fixture");
  // The row's own record names the occurrence that is its reference.
  writeCompositionImageRecord(original.clientMutationId, { anchors: [{ marker: 1, offset: 5 }], tiles: [] });

  const source = createTestComposerSource("root");
  try {
    expect(await source.activateRecovery(row)).toBe(true);
    // Build the two-spelling text: the row's reference is now the SECOND.
    const twoSpellings = `${text.split("real")[0]}real [image 1] reference`;
    source.updateText(twoSpellings);
    expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);

    // Persisting must keep WHICH spelling it was, or the reload binds the
    // warning to the first one.
    source.persistDraft(twoSpellings);
    const stored = readComposerDraftImageAnchors("root").filter((anchor) => anchor.marker === 1);
    expect(stored).toEqual([{ marker: 1, offset: realOffset }]);
  } finally {
    source.dispose();
    storage.close();
  }
});

// Medium (sourceState.ts:1048-1053). The merge copied the incoming row's
// anchor offsets RAW into the merged composition. Those offsets describe the
// row's OWN text, and the merged text is that text appended to this composer's,
// so every offset shifted by the prefix length. The warning then landed on
// whatever spelling happened to sit at that position in the combined text -
// and the durable record filed the same wrong position for the next reopen.
test("a merge translates an incoming anchor's offset into the merged text", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // The incoming row's text puts its missing reference at offset 5.
  const rowText = "real [image 1] reference";
  const rowOffset = rowText.indexOf("[image 1]");
  const original = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/start",
    payload: { ref: "root", input: [{ type: "text", text: rowText }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: rowText }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const incoming = await storage.getRecovery(original.clientMutationId);
  if (!incoming) throw new Error("missing incoming fixture");
  writeCompositionImageRecord(original.clientMutationId, { anchors: [{ marker: 1, offset: rowOffset }], tiles: [] });

  // An owner row so the merge has somewhere to write.
  const owner = await storage.enqueueIntent({
    targetRef: "root",
    threadId: "thread-a",
    method: "turn/queue",
    payload: { ref: "root", input: [{ type: "text", text: "mine" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "mine" }] },
  });
  await storage.transferToRecovery(owner.clientMutationId, "editing");
  const ownerRow = await storage.getRecovery(owner.clientMutationId);
  if (!ownerRow) throw new Error("missing owner fixture");

  const source = createTestComposerSource("root");
  try {
    await source.activateRecovery(ownerRow);
    // A prefix the person already typed in this composer.
    source.updateText("mine typed first ");
    await source.activateRecovery(incoming).catch(() => false);
  } finally {
    source.dispose();
  }

  const durable = await storage.getRecovery(owner.clientMutationId);
  if (!durable) throw new Error("owner row vanished");
  const anchors = (durable as { anchors?: { marker: number; offset?: number }[] }).anchors ?? [];
  const mergedText = (durable.payload.input as InputItem[]).flatMap((item) =>
    item.type === "text" ? [item.text] : [],
  )[0];
  if (mergedText === undefined) throw new Error("merged row carries no text");
  // The filed offset must point at a spelling of its own marker IN THE MERGED
  // TEXT. A raw copy points at whatever the prefix shifted it onto.
  const filed = anchors.find((anchor) => anchor.marker === 1 && anchor.offset !== undefined);
  expect(filed).toBeDefined();
  expect(mergedText.slice(filed?.offset ?? 0, (filed?.offset ?? 0) + "[image 1]".length)).toBe("[image 1]");
  storage.close();
});

// Medium (sourceState.ts:532-550). A retirement entry describes a REFERENCE
// that left the text. It was applied to every entry of the record, including
// the staged half - and a NUMBER CAN BE REUSED. Delete an adopted missing
// `[image 1]`, attach a replacement, and an emptied store hands the new tile
// number 1 again; the old reference's retirement then dropped the new tile's
// own entry. A reload lost the replacement's bytes while the text still spelled
// its reference.
test("a replacement tile reusing a retired number is recorded after reload", () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    // A draft was persisted naming image 1, whose bytes are gone: the adopted
    // anchor the mount reads back.
    writeComposerDraft("root", { text: "missing [image 1] here", skillNames: [] }, [{ marker: 1 }]);
    const source = createTestComposerSource("root");
    try {
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
      // The person deletes the reference. That retirement is real.
      source.updateText("missing  here");
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([]);
      // ...and attaches a replacement, which an empty store numbers 1 again.
      const replacement = {
        id: "replacement",
        marker: 1,
        name: "replacement.png",
        mediaType: "image/png",
        data: "BAUG",
        pending: false,
      };
      source.attachments.setState({ items: [replacement] });
      source.editor.write("missing [image 1] here", 19);

      source.persistDraft("missing [image 1] here");
      // The persisted draft must name the tile it actually holds. A retirement
      // about the reference that USED to be here says nothing about it.
      expect(readComposerDraftImageAnchors("root")).toEqual([{ marker: 1, offset: 8 }]);
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
});

// Medium (sourceState.ts:529-532). Staging a tile splices its `[image N]` at the
// cursor, and the record saved only the NUMBER. A reload therefore resolved the
// number to the FIRST spelling - so staging image 1 and then typing a literal
// `[image 1]` before it bound the warning to the prose. Deleting that prose
// enabled Send while the real reference was still in the text.
test("a staged reference is recorded at its own spelling, not the first one", () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    const source = createTestComposerSource("root");
    try {
      const tile = {
        id: "staged",
        marker: 1,
        name: "staged.png",
        mediaType: "image/png",
        data: "AQID",
        pending: false,
      };
      source.attachments.setState({ items: [tile] });
      // The tile's reference is spliced at the cursor, at the END.
      source.editor.write("literal [image 1] then [image 1]", 30);
      const own = "literal [image 1] then [image 1]".lastIndexOf("[image 1]");

      source.persistDraft("literal [image 1] then [image 1]");

      // The record names the occurrence the tile's bytes back, not the prose.
      expect(readComposerDraftImageAnchors("root")).toEqual([{ marker: 1, offset: own }]);
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
});

// Medium (sourceState.ts:532-550). The tile's own spelling was inferred as the
// LAST `[image N]` no other anchor accounted for. That is right only while
// nothing is typed after the reference: type a literal `[image 1]` AFTER it and
// the last unaccounted spelling is the PROSE, so the persisted draft named the
// words as the image's anchor. A reload then tied the warning to the prose, and
// deleting the genuine reference left Send blocked over text that never had
// bytes.
//
// The splice is the only moment the tile and its reference are one fact, so it
// is recorded there and carried; the reader must not re-derive it.
test("a literal typed after a staged reference does not become its anchor", () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    const source = createTestComposerSource("root");
    try {
      // The splice: the tile's reference begins where the person's cursor was.
      const tile = {
        id: "staged",
        marker: 1,
        name: "staged.png",
        mediaType: "image/png",
        data: "AQID",
        pending: false,
        insertedAt: 0,
      };
      source.attachments.setState({ items: [tile] });
      source.editor.write("[image 1]", 9);

      // ...and then they type the same spelling AFTER it, as words.
      const text = "[image 1] literal [image 1]";
      // The editor reports where it typed, which is what the composer gets
      // from SkillEditor: the insertion begins just past the reference.
      source.editText(text, { from: "[image 1]".length, to: "[image 1]".length });
      const literalAt = text.lastIndexOf("[image 1]");
      expect(literalAt).toBeGreaterThan(0);

      source.persistDraft(text);

      // ONE anchor, and it is the reference the bytes back - not the prose, and
      // not the prose in addition.
      expect(readComposerDraftImageAnchors("root")).toEqual([{ marker: 1, offset: 0 }]);
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
});

// Medium (SkillEditor.tsx:50-66). A drag is ONE transaction with a delete and
// an insert, and the reported range folds both steps into their bounding span.
// Dragging prose from before a reference to after it therefore reported a span
// covering the reference, which neither step touched - so the warning retired
// and the reference went out as literal text.
//
// The steps are what actually happened, so the reader maps each anchor through
// them. This drives the reported shape directly: `[image 1]` sits at offset 0
// and survives untouched while text before it is deleted and re-inserted after
// it.
test("a drag around a reference does not retire its warning", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    const rowText = "prose [image 1] tail";
    const row = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/queue",
      payload: { ref: "root", input: [{ type: "text", text: rowText }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: rowText }] },
    });
    await storage.transferToRecovery(row.clientMutationId, "editing");
    const recovery = await storage.getRecovery(row.clientMutationId);
    if (!recovery) throw new Error("missing recovery fixture");
    // The row's own record: its text spells a reference it has no bytes for.
    writeCompositionImageRecord(row.clientMutationId, {
      anchors: [{ marker: 1, offset: rowText.indexOf("[image 1]") }],
      tiles: [],
      revision: (recovery as { compositionRevision?: number }).compositionRevision ?? 0,
    });

    const source = createTestComposerSource("root");
    try {
      await source.activateRecovery(recovery);
      const before = source.getSnapshot().text;
      expect(before).toContain("[image 1]");
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);

      // The drag: delete the leading prose "prose " (0-6) and re-insert it
      // just past the reference. One transaction, two steps, and the reference
      // lies between them - touched by neither.
      const referenceAt = before.indexOf("[image 1]");
      expect(referenceAt).toBe(6);
      const referenceEnd = referenceAt + "[image 1]".length;
      const after = `${before.slice(6, referenceEnd)}prose ${before.slice(referenceEnd)}`;
      // The hull of both steps, which is what the editor used to report alone.
      source.editText(after, {
        from: 0,
        to: referenceEnd,
        spans: [
          // Each span is in the coordinates of the document ITS step applied
          // to, so the insertion's offset is measured after the deletion.
          { from: 0, to: 6, delta: -6 },
          { from: "[image 1]".length, to: "[image 1]".length, delta: 6 },
        ],
      });

      // The reference moved with the text and still has no bytes: the warning
      // has to survive, or Send ships it as prose.
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
});

// Medium (sourceState.ts:2481-2484). The merge's durable write is awaited, and
// the anchors it installs into the live editor were resolved BEFORE that await.
// Text typed during the write - a caption before the reference - moves those
// offsets and `latest.text` is re-merged to include it, so installing the
// pre-write offsets tests each anchor against text it is no longer in. A
// reference that merely slid is then dropped, and the next Send carries it as
// prose.
test("a caption typed while the merge is in flight does not lose the reference's warning", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    const localText = "mine [image 1] gone";
    writeComposerDraft("root", { text: localText, skillNames: [] }, [
      { marker: 1, offset: localText.indexOf("[image 1]") },
    ]);
    const rowText = "peer text with no references";
    const row = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/queue",
      payload: { ref: "root", input: [{ type: "text", text: rowText }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: rowText }] },
    });
    await storage.transferToRecovery(row.clientMutationId, "editing");
    const recovery = await storage.getRecovery(row.clientMutationId);
    if (!recovery) throw new Error("missing recovery fixture");

    // Hold the durable write open so the test can type inside it.
    const original = storage.updateRecoveryInput.bind(storage);
    let reached: () => void = () => undefined;
    const writeStarted = new Promise<void>((resolve) => {
      reached = resolve;
    });
    let release: () => void = () => undefined;
    const released = new Promise<void>((resolve) => {
      release = resolve;
    });
    storage.updateRecoveryInput = (async (...args: Parameters<typeof original>) => {
      reached();
      await released;
      return original(...args);
    }) as typeof storage.updateRecoveryInput;

    const source = createTestComposerSource("root");
    try {
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
      const activation = source.activateRecovery(recovery);
      await writeStarted;
      // The person types a caption BEFORE the reference while the row's write
      // is still committing.
      source.editText(`caption ${source.getSnapshot().text}`);
      release();
      await activation.catch(() => false);

      expect(source.getSnapshot().text).toContain("caption");
      // The reference moved, so its warning had to move with it. Losing it here
      // means the next Send ships `[image 1]` as literal text with no image.
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
});

// Medium (sourceState.ts:1374-1389). When a local draft and an incoming row
// each contain a genuine missing `[image 1]`, the incoming occurrence claimed
// the NUMBER and the local side - a real reference, at a different spelling -
// was filtered out. The merged text then spelled a reference nothing warned
// about: deleting the incoming one enabled Send while the local missing
// reference was still there.
test("a merge keeps both sides' missing references when both spell the same marker", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    const rowText = "incoming [image 1] gone";
    const incoming = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/queue",
      payload: { ref: "root", input: [{ type: "text", text: rowText }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: rowText }] },
    });
    await storage.transferToRecovery(incoming.clientMutationId, "editing");
    const incomingRow = await storage.getRecovery(incoming.clientMutationId);
    if (!incomingRow) throw new Error("missing incoming fixture");
    writeCompositionImageRecord(incoming.clientMutationId, {
      anchors: [{ marker: 1, offset: rowText.indexOf("[image 1]") }],
      tiles: [],
    });
    // THIS pane's own draft carries a missing reference of its own: the same
    // number, a DIFFERENT spelling, and no row to hold its tag.
    const localText = "mine [image 1] also missing";
    writeComposerDraft("root", { text: localText, skillNames: [] }, [
      { marker: 1, offset: localText.indexOf("[image 1]") },
    ]);

    const source = createTestComposerSource("root");
    try {
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
      await source.activateRecovery(incomingRow).catch(() => false);
      expect(source.getSnapshot().text).toContain(localText);
      expect(source.getSnapshot().text).toContain(rowText);
      // TWO references, two spellings. One warning for one number is the
      // defect: the local missing reference would ship as literal prose.
      const durable = (await storage.getRecovery(incoming.clientMutationId)) as
        | { anchors?: { marker: number; offset?: number }[] }
        | undefined;
      expect(durable?.anchors).toHaveLength(2);
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
});

// Medium (sourceState.ts:1540-1548). Two texts that both BEGIN with the same
// marker put both anchors at offset 0 IN THEIR OWN TEXT. Deduplicating before
// placing the texts therefore compared two different rulers and treated the
// pair as one reference: the local warning was dropped, and deleting the
// incoming reference enabled a Send whose local missing reference was still in
// the text.
//
// The offset is the whole test: at the head of each text, "same marker, same
// offset" is true of two references that are nowhere near each other in the
// merged draft.
test("a merge keeps both sides' missing references when both begin at offset zero", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  try {
    const rowText = "[image 1] incoming missing";
    const incoming = await storage.enqueueIntent({
      targetRef: "root",
      threadId: "thread-a",
      method: "turn/queue",
      payload: { ref: "root", input: [{ type: "text", text: rowText }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: rowText }] },
    });
    await storage.transferToRecovery(incoming.clientMutationId, "editing");
    const incomingRow = await storage.getRecovery(incoming.clientMutationId);
    if (!incomingRow) throw new Error("missing incoming fixture");
    expect(rowText.indexOf("[image 1]")).toBe(0);
    writeCompositionImageRecord(incoming.clientMutationId, {
      anchors: [{ marker: 1, offset: 0 }],
      tiles: [],
    });
    // This pane's own missing reference is ALSO at offset 0, in ITS text.
    const localText = "[image 1] local missing";
    writeComposerDraft("root", { text: localText, skillNames: [] }, [{ marker: 1, offset: 0 }]);

    const source = createTestComposerSource("root");
    try {
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
      await source.activateRecovery(incomingRow).catch(() => false);
      const merged = source.getSnapshot().text;
      expect(merged).toContain(localText);
      expect(merged).toContain(rowText);
      // Both references survive in the EDITOR, at the positions the merged text
      // actually puts them. One warning for two references is the defect.
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
      // Deleting the incoming reference leaves the local one refusing a Send.
      source.updateText(localText);
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
      // ...and the durable row states both, in the merged text's coordinates.
      const durable = (await storage.getRecovery(incoming.clientMutationId)) as
        | { anchors?: { marker: number; offset?: number }[] }
        | undefined;
      expect(durable?.anchors).toHaveLength(2);
    } finally {
      source.dispose();
    }
  } finally {
    storage.close();
  }
});

// Medium 4 is NOT covered by a test, and this comment records why rather than
// leaving a green that proves nothing.
//
// The reviewer's sequence needs the owner row to have committed an EMPTY anchor
// list whose localStorage write then FAILED, so the stale side record is still
// readable when Explicit Edit merges into that row. I built the fixture three
// ways - repair in a peer, repair in the row, staleness by revision stamp - and
// probed the merge site each time: `readCompositionImageRecord(ownerId)` there
// returns `[]` (or the record the current revision just wrote), never the stale
// warning. Activating the repaired row runs its own reconciliation, which
// rewrites the side record at the revision being opened, so the failed write is
// gone by the time a merge can consult it. On the paths a test can drive, the
// stale record this finding describes cannot be present at the merge.
//
// The fix is kept because it states the same derivation rule the reader already
// uses and costs nothing when the record agrees: `compositionAnchorsForRecord`
// prefers the row's committed copy and consults the side record only when it
// names the revision being opened. If the reviewer's path exists outside this
// fixture, that rule is what covers it.

// Both directions of the one comparator, one fixture each: an identical
// composition REUSES the peer's row, and different image bytes get their own.
// A test that only proves "the two sides call the same function" passes without
// the function being right, so these assert ROW IDENTITY and the BYTES each row
// ends up owning.
async function staleSaveFixture(tileData: string) {
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
  // Real base64: the durable write decodes these bytes, so a placeholder like
  // "KEPT" throws before any row is minted and the test would be measuring the
  // fixture rather than the comparator.
  const tile = { id: "kept", marker: 1, name: "kept.png", mediaType: "image/png", data: "S0VQVA==", pending: false };
  const source = createTestComposerSource("root");
  const peer = new MutationOutboxIndexedDB();
  await source.activateRecovery(recovery);
  source.attachments.setState({ items: [tile] });
  // The record is written by the AUTOSAVE, not hand-seeded: its digest is the
  // real fingerprint of these bytes, and a hand-seeded one would only prove the
  // comparator agrees with a number the test invented.
  await source.queueRecoveryPersistence(original.clientMutationId, text, [], []);
  await source.flushRecoveryWrites();
  // The peer advances the row's revision WITHOUT changing its composition, so
  // this pane's lease goes stale and its next save preserves instead of
  // writing in place.
  const row = await peer.getRecovery(original.clientMutationId);
  if (!row) throw new Error("peer cannot see the row");
  const advanced = await peer.updateRecoveryInput(
    original.clientMutationId,
    (row.payload as { input?: InputItem[] }).input ?? [],
    row.attachments,
    row.composerText,
    undefined,
    undefined,
    undefined,
    (row as { compositionRevision?: number }).compositionRevision ?? 0,
  );
  expect(advanced).toBeDefined();
  source.attachments.setState({ items: [{ ...tile, data: tileData }] });
  await source.queueRecoveryPersistence(original.clientMutationId, text, [], []);
  await source.flushRecoveryWrites();
  const rows = await storage.listRecovery("root");
  source.dispose();
  peer.close();
  storage.close();
  return rows;
}

test("a stale save reuses the peer's row when the composition is identical", async () => {
  const rows = await staleSaveFixture("S0VQVA==");
  // Same text, same bytes, same eligibility: the peer's row already says it.
  expect(rows.map((row) => row.clientMutationId)).toEqual([rows[0]?.clientMutationId]);
  expect(rows).toHaveLength(1);
  expect(rows[0]?.attachments).toHaveLength(1);
});

test("a stale save with different image bytes gets its own row", async () => {
  const rows = await staleSaveFixture("T1RIRVI=");
  // Two images under one marker with one filename: only the bytes tell the
  // compositions apart, and treating them as equal loses the stale pane's.
  expect(rows).toHaveLength(2);
  const peerRow = rows.find((row) => row.attachments[0]?.marker === 1 && row.attachments.length === 1);
  expect(peerRow).toBeDefined();
  // The fork keeps ITS OWN bytes and the peer keeps its own: the two rows
  // describe different images, which is the whole reason they are two rows.
  expect(peerRow).toBeDefined();
  const forked = rows.find((row) => row.clientMutationId !== peerRow?.clientMutationId);
  if (!forked) throw new Error("the different composition was not preserved");
  const payloadBytes = (row: (typeof rows)[number]) =>
    ((row.payload as { input?: InputItem[] }).input ?? []).flatMap((item) =>
      item.type === "image" ? [item.data] : [],
    );
  expect(payloadBytes(peerRow as (typeof rows)[number])).toEqual(["S0VQVA=="]);
  expect(payloadBytes(forked)).toEqual(["T1RIRVI="]);
});

// Medium (sourceState.ts:746-754). A peer replaces a missing reference with the
// SAME literal text, so the composed text and the attachments are identical and
// only the anchors tell the two compositions apart. IndexedDB commits the empty
// anchor list first; the side record is still describing the reference the peer
// removed. A stale pane then reads that record by id, sees its own warning in
// it, and calls the peer's row proof that its composition was already saved -
// so the row reopens without the warning the pane is looking at.
//
// The row is the only witness to what it contains, so the comparison must read
// the anchors the transaction committed WITH that text.
test("a stale save compares the row's committed anchors, not a stale side record", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const peer = new MutationOutboxIndexedDB();
  try {
    // The composition both panes hold: one real reference, no tile.
    const text = "[image 1] repaired as prose";
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

    const startedAt = (recovery as { compositionRevision?: number }).compositionRevision ?? 0;
    // THIS pane opens the row first, while the reference is still missing, so
    // the warning is part of the composition it is holding.
    writeCompositionImageRecord(original.clientMutationId, {
      anchors: [{ marker: 1, offset: 0 }],
      tiles: [],
      revision: startedAt,
    });
    const source = createTestComposerSource("root");
    try {
      await source.activateRecovery(recovery);
      expect(source.getSnapshot().unbackedImageMarkers).toEqual([1]);
      // Activation advances the revision itself, so the peer's write is gated
      // on the CURRENT one - a stale expectation would be refused as a
      // concurrent edit and the test would measure that instead.
      const current = (await peer.getRecovery(original.clientMutationId)) as
        | { compositionRevision?: number }
        | undefined;
      const currentRevision = current?.compositionRevision ?? 0;

      // The PEER then repairs that reference - replacing it with the same
      // literal text - so the row commits an EMPTY anchor list: "this text
      // references no missing image". Text and attachments are unchanged, which
      // is why only the anchor list distinguishes the two compositions.
      const advanced = await peer.updateRecoveryInput(
        original.clientMutationId,
        [{ type: "text", text }],
        [],
        text,
        undefined,
        undefined,
        undefined,
        currentRevision,
        undefined,
        undefined,
        undefined,
        [],
      );
      if (!advanced) throw new Error("peer could not advance the row");
      // ...and the side record has NOT caught up: it still describes the
      // reference the peer removed. Unstamped, so a reader that consults it
      // trusts it - which is exactly the reading that loses this pane's warning.
      writeCompositionImageRecord(original.clientMutationId, {
        anchors: [{ marker: 1, offset: 0 }],
        tiles: [],
      });

      // This pane saves the composition it is holding. Its lease is stale, so
      // the save preserves rather than writing in place.
      await source.queueRecoveryPersistence(original.clientMutationId, text, [], []);
      await source.flushRecoveryWrites();
      const rows = await storage.listRecovery("root");
      // TWO compositions: one pane's text carries a missing reference, the
      // row's does not. Reusing the row would discard this pane's warning, and
      // the reopen would show prose that no longer refuses a Send.
      expect(rows).toHaveLength(2);
    } finally {
      source.dispose();
    }
  } finally {
    peer.close();
    storage.close();
  }
});
