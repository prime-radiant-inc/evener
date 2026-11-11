// @vitest-environment node
import type { InputItem } from "@evener/appwire-client";
import {
  describeQueuedMessageStorage,
  describeWebQueuedMessageStorage,
  queuedIntent,
} from "@evener/appwire-client/testing/queuedMessageStorageConformance";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { requestResult, transactionCompletion } from "./idbConnection";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

const opened: MutationOutboxIndexedDB[] = [];
afterEach(() => {
  vi.restoreAllMocks();
  for (const storage of opened.splice(0)) storage.close();
});
function fixture() {
  const indexedDB = new IDBFactory();
  const databaseName = "queued-messages";
  let id = 0;
  let failCommit = false;
  const open = () => {
    const storage = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: () => `mutation-${++id}`,
      beforeCommit: () => {
        if (failCommit) throw new Error("injected storage fault");
      },
    });
    opened.push(storage);
    return storage;
  };
  const storage = open();
  const peer = open();
  return {
    storage,
    peer,
    openOtherTab: () => {
      const other = new MutationOutboxIndexedDB({
        indexedDB,
        databaseName,
        createMutationId: () => `mutation-${++id}`,
      });
      opened.push(other);
      return other;
    },
    indexedDB,
    databaseName,
    fail: (value: boolean) => {
      failCommit = value;
    },
    reopen: async () => {
      storage.close();
      peer.close();
      return open();
    },
    release: async (record: { clientMutationId: string }, barrier: { stopEpoch: number }) => {
      const capture = await peer.getOutboxWithStopEpoch(record.clientMutationId);
      const minted = await peer.mintRetryFromRow(record.clientMutationId, {
        record: capture.record,
        stopEpoch: barrier.stopEpoch,
        displayedStopEpoch: 0,
      });
      return minted?.state === "submitting";
    },
  };
}
describeQueuedMessageStorage("IndexedDB", fixture);

// Failing any write, including the final commit, must leave the original owner.
test.each(["put", "delete", "commit"] as const)("edit rolls back at %s and retries", async (boundary) => {
  const f = fixture();
  const original = await f.storage.enqueueIntent(queuedIntent());
  if (boundary === "commit") f.fail(true);
  else
    vi.spyOn(IDBObjectStore.prototype, boundary).mockImplementation(() => {
      throw new Error("injected");
    });
  await expect(f.storage.editQueuedMessage(original, { stopEpoch: 0 })).rejects.toBeDefined();
  vi.restoreAllMocks();
  f.fail(false);
  const storage = await f.reopen();
  expect(await storage.getOutbox(original.clientMutationId)).toMatchObject(original);
  expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
  expect(await storage.editQueuedMessage(original, { stopEpoch: 0 })).toMatchObject({ recoveryKind: "editing" });
});

test.each(["delete", "commit"] as const)(
  "remove failure at %s retains only original and retry succeeds",
  async (boundary) => {
    const f = fixture();
    const original = await f.storage.enqueueIntent(queuedIntent());
    if (boundary === "commit") f.fail(true);
    else
      vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementation(() => {
        throw new Error("injected");
      });
    await expect(f.storage.removeQueuedMessage(original, { stopEpoch: 0 })).rejects.toBeDefined();
    vi.restoreAllMocks();
    f.fail(false);
    expect(await f.peer.getOutbox(original.clientMutationId)).toMatchObject(original);
    expect(await f.peer.removeQueuedMessage(original, { stopEpoch: 0 })).toBe(true);
  },
);

test.each(["put", "add", "delete", "commit"] as const)("resend rolls back at %s and retries", async (boundary) => {
  const f = fixture();
  const ids: string[] = [];
  for (let i = 0; i < 2; i++) {
    const row = await f.storage.enqueueIntent(queuedIntent());
    ids.push(row.clientMutationId);
    await f.storage.transferToRecovery(row.clientMutationId, "rejected");
  }
  const expectedRecoveryRevisions = Object.fromEntries(
    await Promise.all(
      ids.map(async (id) => {
        const record = await f.storage.getRecovery(id);
        if (!record) throw new Error(`missing recovery fixture ${id}`);
        return [id, record.compositionRevision ?? 0] as const;
      }),
    ),
  );
  if (boundary === "commit") f.fail(true);
  else
    vi.spyOn(IDBObjectStore.prototype, boundary).mockImplementation(() => {
      throw new Error("injected");
    });
  await expect(
    f.storage.resendRecovery(ids, queuedIntent(), undefined, expectedRecoveryRevisions),
  ).rejects.toBeDefined();
  vi.restoreAllMocks();
  f.fail(false);
  for (const id of ids) expect(await f.peer.getRecovery(id)).toBeDefined();
  expect(await f.peer.listOutbox()).toHaveLength(0);
  expect(await f.peer.resendRecovery(ids, queuedIntent(), undefined, expectedRecoveryRevisions)).toMatchObject({
    intentSequence: 3,
  });
});

describeWebQueuedMessageStorage("IndexedDB", fixture);

test.each(["put", "delete", "commit"] as const)(
  "consolidation rolls back saved composition and secondary deletion at %s",
  async (boundary) => {
    const f = fixture();
    const rows = [];
    for (let i = 0; i < 3; i++) {
      const row = await f.storage.enqueueIntent(queuedIntent());
      rows.push(row);
      await f.storage.transferToRecovery(row.clientMutationId, "rejected");
    }
    const [first, second, third] = rows;
    if (!first || !second || !third) throw new Error("missing fixtures");
    const sources = [second.clientMutationId, third.clientMutationId];
    const input = [{ type: "text" as const, text: "combined" }];
    expect(
      await f.storage.updateRecoveryInput(
        first.clientMutationId,
        input,
        [],
        "combined",
        [],
        [...sources, "absent"],
        undefined,
        0,
        { [first.clientMutationId]: 0, [second.clientMutationId]: 0, [third.clientMutationId]: 0 },
      ),
    ).toBeUndefined();
    const foreign = await f.storage.enqueueIntent(queuedIntent("other:target"));
    await f.storage.transferToRecovery(foreign.clientMutationId, "rejected");
    expect(
      await f.storage.updateRecoveryInput(
        first.clientMutationId,
        input,
        [],
        "combined",
        [],
        [foreign.clientMutationId],
        undefined,
        0,
        { [first.clientMutationId]: 0, [foreign.clientMutationId]: 0 },
      ),
    ).toBeUndefined();
    if (boundary === "commit") f.fail(true);
    else if (boundary === "put")
      vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(() => {
        throw new Error("injected");
      });
    else {
      const originalDelete = IDBObjectStore.prototype.delete;
      let count = 0;
      vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementation(function (this: IDBObjectStore, key) {
        if (++count === 2) throw new Error("injected");
        return originalDelete.call(this, key);
      });
    }
    await expect(
      f.storage.updateRecoveryInput(first.clientMutationId, input, [], "combined", [], sources, undefined, 0, {
        [first.clientMutationId]: 0,
        [second.clientMutationId]: 0,
        [third.clientMutationId]: 0,
      }),
    ).rejects.toBeDefined();
    vi.restoreAllMocks();
    f.fail(false);
    for (const row of rows)
      expect(await f.peer.getRecovery(row.clientMutationId)).toMatchObject({ composerText: "same input @inspect" });
    const saved = await f.peer.updateRecoveryInput(
      first.clientMutationId,
      input,
      [],
      "combined",
      [],
      sources,
      undefined,
      0,
      { [first.clientMutationId]: 0, [second.clientMutationId]: 0, [third.clientMutationId]: 0 },
    );
    expect(saved).toMatchObject({ composerText: "combined", payload: { input }, composerMentions: [] });
    for (const id of sources) expect(await f.storage.getRecovery(id)).toBeUndefined();
    expect(await f.storage.getRecovery(foreign.clientMutationId)).toBeDefined();
  },
);

test("recovery draft provenance commits with merged composition and secondary consumption", async () => {
  const f = fixture();
  const first = await f.storage.enqueueIntent(queuedIntent());
  const second = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.transferToRecovery(first.clientMutationId, "rejected");
  await f.storage.transferToRecovery(second.clientMutationId, "rejected");
  const saved = await f.storage.updateRecoveryInput(
    first.clientMutationId,
    [{ type: "text", text: "combined" }],
    [],
    "combined",
    [],
    [second.clientMutationId],
    "draft-revision-7",
    0,
    { [first.clientMutationId]: 0, [second.clientMutationId]: 0 },
  );
  expect(saved).toMatchObject({ composerText: "combined", localDraftRevisionId: "draft-revision-7" });
  expect(await f.storage.getRecovery(second.clientMutationId)).toBeUndefined();
  const reopened = await f.reopen();
  expect(await reopened.getRecovery(first.clientMutationId)).toMatchObject({
    composerText: "combined",
    localDraftRevisionId: "draft-revision-7",
  });
});

test("settlement leaves recovery advanced to revision one for reconciliation", async () => {
  const f = fixture();
  const outbox = await f.storage.enqueueIntent(queuedIntent("ref_a"));
  const original = await f.storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!original) throw new Error("failed to seed recovery");
  const edited = await f.peer.updateRecoveryInput(
    original.clientMutationId,
    [{ type: "text", text: "newer composition" }],
    [],
    "newer composition",
    [],
    [],
    undefined,
    0,
  );
  expect(edited?.compositionRevision).toBe(1);
  expect(await f.storage.settleApplied(original.clientMutationId)).toMatchObject({
    clientMutationId: original.clientMutationId,
  });
  const reopened = await f.reopen();
  expect(await reopened.getRecovery(original.clientMutationId)).toMatchObject({
    compositionRevision: 1,
    composerText: "newer composition",
    payload: { input: [{ type: "text", text: "newer composition" }] },
  });
});

test("settleApplied can keep the accepted display copy for the queue handoff", async () => {
  const f = fixture();
  const outbox = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(outbox.clientMutationId);
  expect(await f.storage.settleReceipt(outbox.clientMutationId, "pending")).toBe(true);
  expect(await f.storage.getOptimistic(outbox.clientMutationId)).toBeDefined();

  await f.storage.settleApplied(outbox.clientMutationId, { retainDisplayCopy: true });

  expect(await f.storage.getOptimistic(outbox.clientMutationId)).toMatchObject({
    clientMutationId: outbox.clientMutationId,
    state: "accepted",
  });
});

test("stale cross-adapter recovery consolidation refuses without deleting the secondary", async () => {
  const f = fixture();
  const seedImageRecovery = async (text: string, imageName: string) => {
    const input = [
      { type: "text" as const, text },
      { type: "image" as const, mediaType: "image/png", data: "AQID", name: imageName },
    ];
    const intent = queuedIntent("ref_a");
    const outbox = await f.storage.enqueueIntent({
      ...intent,
      payload: { ...intent.payload, input },
      attachments: [
        {
          presentationId: `presentation-${imageName}`,
          marker: 1,
          name: imageName,
          mediaType: "image/png",
          blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
        },
      ],
    });
    const record = await f.storage.transferToRecovery(outbox.clientMutationId, "rejected");
    if (!record) throw new Error(`failed to seed ${text}`);
    return record;
  };
  const owner = await seedImageRecovery("owner A", "a.png");
  const secondaryB = await seedImageRecovery("secondary B", "b.png");
  const secondaryC = await seedImageRecovery("secondary C", "c.png");
  const snapshotFromAdapterA = await f.storage.getRecovery(owner.clientMutationId);
  const snapshotFromAdapterB = await f.peer.getRecovery(owner.clientMutationId);
  if (!snapshotFromAdapterA || !snapshotFromAdapterB) throw new Error("missing owner snapshot");

  const makeComposition = (text: string, imageName: string) => ({
    input: [
      { type: "text" as const, text },
      { type: "image" as const, mediaType: "image/png", data: "AQID", name: imageName },
    ],
    attachments: [
      {
        presentationId: `merged-${imageName}`,
        marker: 1,
        name: imageName,
        mediaType: "image/png",
        blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
      },
    ],
  });
  const compositionAB = makeComposition("owner A + secondary B", "b.png");
  const compositionAC = makeComposition("owner A + secondary C", "c.png");
  const first = await f.storage.updateRecoveryInput(
    owner.clientMutationId,
    compositionAB.input,
    compositionAB.attachments,
    "owner A + secondary B",
    [],
    [secondaryB.clientMutationId],
    undefined,
    0,
    { [owner.clientMutationId]: 0, [secondaryB.clientMutationId]: 0 },
  );
  expect(first).toBeDefined();
  const stale = await f.peer.updateRecoveryInput(
    owner.clientMutationId,
    compositionAC.input,
    compositionAC.attachments,
    "owner A + secondary C",
    [],
    [secondaryC.clientMutationId],
    undefined,
    0,
    { [owner.clientMutationId]: 0, [secondaryC.clientMutationId]: 0 },
  );
  const reopened = await f.reopen();
  expect(await reopened.getRecovery(owner.clientMutationId)).toMatchObject({
    composerText: "owner A + secondary B",
    payload: { input: compositionAB.input },
    attachments: [expect.objectContaining({ name: "b.png" })],
  });
  expect(await reopened.getRecovery(secondaryB.clientMutationId)).toBeUndefined();
  expect(await reopened.getRecovery(secondaryC.clientMutationId)).toMatchObject({
    composerText: secondaryC.composerText,
    payload: { input: secondaryC.payload.input },
    attachments: [expect.objectContaining({ name: "c.png" })],
  });
  expect(stale).toBeUndefined();
});

test("a stale pane on one adapter is refused while the owning pane may advance", async () => {
  const f = fixture();
  const outbox = await f.storage.enqueueIntent(queuedIntent("ref_a"));
  const recovery = await f.storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovery) throw new Error("missing recovery fixture");
  const owner = "pane-a";
  const revision = recovery.compositionRevision ?? 0;
  const advanced = await f.storage.updateRecoveryInput(
    recovery.clientMutationId,
    [{ type: "text", text: "newer work" }],
    [],
    "newer work",
    [],
    [],
    undefined,
    revision,
    undefined,
    owner,
  );
  expect(advanced?.compositionRevision).toBe(revision + 1);
  const intent = queuedIntent("ref_a");
  expect(
    await f.storage.resendRecoveryResult(
      recovery.clientMutationId,
      intent,
      undefined,
      {
        [recovery.clientMutationId]: revision,
      },
      "pane-b",
    ),
  ).toEqual({ status: "refused" });
  expect(
    await f.storage.resendRecoveryResult(
      recovery.clientMutationId,
      intent,
      undefined,
      {
        [recovery.clientMutationId]: revision,
      },
      owner,
    ),
  ).toMatchObject({ status: "sent" });
});

test("a different-tab resend refuses an advanced recovery revision", async () => {
  const f = fixture();
  const outbox = await f.storage.enqueueIntent(queuedIntent("ref_a"));
  const recovery = await f.storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovery) throw new Error("missing recovery fixture");
  const otherTab = f.openOtherTab();
  const edited = await otherTab.updateRecoveryInput(
    recovery.clientMutationId,
    [{ type: "text", text: "other tab edit" }],
    [],
    "other tab edit",
    [],
    [],
    undefined,
    0,
  );
  expect(edited).toBeDefined();
  const result = await f.storage.resendRecoveryResult(recovery.clientMutationId, queuedIntent("ref_a"), undefined, {
    [recovery.clientMutationId]: recovery.compositionRevision ?? 0,
  });
  expect(result).toEqual({ status: "refused" });
  const reopened = await f.reopen();
  expect(await reopened.getRecovery(recovery.clientMutationId)).toMatchObject({
    composerText: "other tab edit",
  });
  expect(await reopened.listOutbox("ref_a")).toHaveLength(0);
});

test("stale secondary revision refuses consolidation, preserving newer secondary and owner", async () => {
  const f = fixture();
  const seed = async (text: string) => {
    const outbox = await f.storage.enqueueIntent({
      ...queuedIntent("ref_a"),
      payload: { ...queuedIntent("ref_a").payload, input: [{ type: "text" as const, text }] },
    });
    const record = await f.storage.transferToRecovery(outbox.clientMutationId, "rejected");
    if (!record) throw new Error(`missing recovery ${text}`);
    return record;
  };
  const owner = await seed("owner A");
  const secondary = await seed("secondary B");
  const latestB = await f.peer.updateRecoveryInput(
    secondary.clientMutationId,
    [{ type: "text", text: "newer B" }],
    [],
    "newer B",
    [],
    [],
    undefined,
    0,
  );
  expect((latestB as (typeof latestB & { compositionRevision?: number }) | undefined)?.compositionRevision).toBe(1);
  const refused = await f.storage.updateRecoveryInput(
    owner.clientMutationId,
    [{ type: "text", text: "owner A + stale B" }],
    [],
    "owner A + stale B",
    [],
    [secondary.clientMutationId],
    undefined,
    0,
    { [owner.clientMutationId]: 0, [secondary.clientMutationId]: 0 },
  );
  expect(refused).toBeUndefined();
  const reopened = await f.reopen();
  expect(await reopened.getRecovery(owner.clientMutationId)).toMatchObject({
    composerText: "same input @inspect",
    compositionRevision: 0,
  });
  expect(await reopened.getRecovery(secondary.clientMutationId)).toMatchObject({
    composerText: "newer B",
    compositionRevision: 1,
  });
});

test("stale consolidation of C into peer-edited B refuses, preserving edited B and C", async () => {
  const f = fixture();
  const seed = async (text: string) => {
    const intent = queuedIntent("ref_a");
    const outbox = await f.storage.enqueueIntent({
      ...intent,
      payload: { ...intent.payload, input: [{ type: "text" as const, text }] },
    });
    const record = await f.storage.transferToRecovery(outbox.clientMutationId, "rejected");
    if (!record) throw new Error(`missing recovery ${text}`);
    return record;
  };
  const a = await seed("A");
  const b = await seed("B");
  const c = await seed("C");
  const mergedB = await f.storage.updateRecoveryInput(
    b.clientMutationId,
    [{ type: "text", text: "B + A" }],
    [],
    "B + A",
    [],
    [a.clientMutationId],
    undefined,
    0,
    { [b.clientMutationId]: 0, [a.clientMutationId]: 0 },
  );
  expect((mergedB as (typeof mergedB & { compositionRevision?: number }) | undefined)?.compositionRevision).toBe(1);
  const peerEdit = await f.peer.updateRecoveryInput(
    b.clientMutationId,
    [{ type: "text", text: "edited B" }],
    [],
    "edited B",
    [],
    [],
    undefined,
    1,
    { [b.clientMutationId]: 1 },
  );
  expect((peerEdit as (typeof peerEdit & { compositionRevision?: number }) | undefined)?.compositionRevision).toBe(2);
  const refused = await f.storage.updateRecoveryInput(
    b.clientMutationId,
    [{ type: "text", text: "stale B + C" }],
    [],
    "stale B + C",
    [],
    [c.clientMutationId],
    undefined,
    1,
    { [b.clientMutationId]: 1, [c.clientMutationId]: 0 },
  );
  expect(refused).toBeUndefined();
  const reopened = await f.reopen();
  expect(await reopened.getRecovery(b.clientMutationId)).toMatchObject({
    composerText: "edited B",
    compositionRevision: 2,
  });
  expect(await reopened.getRecovery(c.clientMutationId)).toMatchObject({
    composerText: "same input @inspect",
    compositionRevision: 0,
  });
});

test("legacy reopen preserves unknown attempts and editing image bytes", async () => {
  const f = fixture();
  const row = await f.storage.enqueueIntent({
    ...queuedIntent(),
    attachments: [
      {
        presentationId: "image-a",
        marker: 4,
        name: "a",
        mediaType: "image/png",
        blob: new Blob([new Uint8Array([0, 255, 7])]),
      },
    ],
  });
  f.storage.close();
  f.peer.close();
  const db = await requestResult(f.indexedDB.open(f.databaseName));
  const tx = db.transaction("outbox", "readwrite");
  const complete = transactionCompletion(tx);
  const legacy = { ...row };
  delete legacy.attempted;
  delete legacy.unresolvedAttempts;
  await requestResult(tx.objectStore("outbox").put(legacy));
  await complete;
  db.close();
  const storage = await f.reopen();
  expect(await storage.editQueuedMessage(legacy, { stopEpoch: 0 })).toBeUndefined();
  expect(await storage.removeQueuedMessage(legacy, { stopEpoch: 0 })).toBe(false);
  const known = await storage.enqueueIntent({ ...queuedIntent(), attachments: row.attachments });
  await storage.editQueuedMessage(known, { stopEpoch: 0 });
  const recovery = await storage.getRecovery(known.clientMutationId);
  expect(recovery?.attachments[0]?.marker).toBe(4);
  const blob = recovery?.attachments[0]?.blob;
  if (!blob) throw new Error("missing image bytes");
  expect(new Uint8Array(await blob.arrayBuffer())).toEqual(new Uint8Array([0, 255, 7]));
});

test("preserveRecoveryComposition writes a fresh owner with image bytes", async () => {
  const f = fixture();
  const outbox = await f.storage.enqueueIntent(queuedIntent());
  const original = await f.storage.transferToRecovery(outbox.clientMutationId, "editing");
  if (!original) throw new Error("failed to seed recovery");
  const input: InputItem[] = [
    { type: "text", text: "keep this [image 1]" },
    { type: "image", mediaType: "image/png", data: "AQID", name: "recovered.png" },
  ];
  const fresh = await f.storage.preserveRecoveryComposition(
    original,
    input,
    [
      {
        presentationId: "image-a",
        marker: 1,
        name: "recovered.png",
        mediaType: "image/png",
        blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
      },
    ],
    "keep this [image 1]",
    [],
    "pane-owner",
    "draft-9",
  );
  expect(fresh?.clientMutationId).not.toBe(original.clientMutationId);
  expect(fresh).toMatchObject({
    targetRef: original.targetRef,
    threadId: original.threadId,
    method: "turn/queue",
    recoveryKind: "rejected",
    composerText: "keep this [image 1]",
    compositionOwnerId: "pane-owner",
    localDraftRevisionId: "draft-9",
    payload: { input },
    attachments: [expect.objectContaining({ marker: 1, name: "recovered.png" })],
  });
  expect(await f.storage.getRecovery(original.clientMutationId)).toMatchObject({
    composerText: original.composerText,
  });
  const reopened = await f.reopen();
  expect(await reopened.getRecovery(fresh?.clientMutationId ?? "")).toMatchObject({ payload: { input } });
});

// Whole-endpoint review M2: a retargeted editing recovery's displayed-session
// association is part of the composition the preserve must carry. The
// preserved replacement row keeps base.displayedRef, or the preserved
// composition - attachment bytes included - vanishes from the displayed
// session's tray after reload: the displayedRef-matching persistence reads
// can no longer find it.
test("preserveRecoveryComposition keeps the displayed-session association of a retargeted recovery", async () => {
  const f = fixture();
  const input: InputItem[] = [{ type: "text", text: "retargeted composition" }];
  const seed = await f.storage.enqueueIntent({
    targetRef: "local:queued",
    threadId: "thread",
    method: "turn/start",
    payload: { ref: "local:queued", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
  });
  await f.storage.transferToRecovery(seed.clientMutationId, "rejected");
  await f.storage.retargetOutbox("local:queued", "local:resumed", { threadId: "thread-resumed" });
  const sent = await f.storage.resendRecoveryResult(
    seed.clientMutationId,
    {
      targetRef: "local:resumed",
      threadId: "thread-resumed",
      method: "turn/start",
      payload: { ref: "local:resumed", input },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input },
    },
    undefined,
    undefined,
    undefined,
    { stopEpoch: 0, displayedRef: "local:queued" },
  );
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  // The retargeted editing recovery the displayed session's tray holds.
  const original = await f.storage.transferToRecovery(sent.record.clientMutationId, "editing");
  if (original?.displayedRef !== "local:queued") throw new Error("missing retargeted recovery");
  const fresh = await f.storage.preserveRecoveryComposition(
    original,
    input,
    [],
    "retargeted composition",
    [],
    "pane-owner",
  );
  // The preserved replacement keeps the displayed-session association...
  expect(fresh).toMatchObject({ displayedRef: "local:queued", targetRef: "local:resumed" });
  // ...so the displayed session's tray still finds it after a reload.
  const reopened = await f.reopen();
  const displayed = await reopened.listRecoveryDisplayedAt("local:queued");
  expect(displayed.map((record) => record.clientMutationId)).toContain(fresh?.clientMutationId);
});

test("preserveRecoveryComposition reuses a row that already holds the composition", async () => {
  const f = fixture();
  const outbox = await f.storage.enqueueIntent(queuedIntent());
  const original = await f.storage.transferToRecovery(outbox.clientMutationId, "editing");
  if (!original) throw new Error("failed to seed recovery");
  const preserved = await f.storage.preserveRecoveryComposition(
    original,
    original.payload.input as InputItem[],
    original.attachments,
    original.composerText ?? "",
    original.composerMentions,
    "pane-owner",
  );
  expect(preserved?.clientMutationId).toBe(original.clientMutationId);
  expect(await f.storage.listRecovery(original.targetRef)).toHaveLength(1);
});

// Round-3 review M-A: the wire coalesce consults only LIVE recovery rows, so
// once a recovery is consumed by a resend (deleted from the store) a delayed
// terminal refusal for its superseded sibling finds no wire copy and mints a
// NEW sendable recovery for work the resend already delivered. The resend's
// consumption must retire the superseded wire siblings in the same write
// transaction, so the late refusal lands on a missing row and is inert -
// the same late-outcome semantics a receipt already has.
test("a consumed recovery retires its superseded wire siblings, so a late refusal cannot resurrect delivered work", async () => {
  const f = fixture();
  // A: attempted, response lost -> delivery-uncertain.
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  // Retry mints B under A's wire id; A settles terminal superseded, keeping
  // its id and attempt evidence for its real attempt's late outcome.
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  // B's attempt is refused -> recovery R (key B, wire A).
  await f.storage.markAttempted(b.clientMutationId);
  await f.storage.markUnknown(b.clientMutationId, "blockedUnknown");
  const recovery = await f.storage.transferToRecovery(b.clientMutationId, "rejected");
  if (!recovery) throw new Error("failed to seed recovery");
  // The user resends R: R is consumed and C is enqueued.
  const sent = await f.storage.resendRecoveryResult(b.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  // The consumed recovery's superseded wire siblings retire with it, in the
  // same transaction that consumed the recovery.
  expect(await f.storage.getOutbox(a.clientMutationId)).toBeUndefined();
  // C delivers and settles: the composition has left exactly once.
  await f.storage.markAttempted(sent.record.clientMutationId);
  expect(await f.storage.settleReceipt(sent.record.clientMutationId, "reflected")).toBe(true);
  // A's delayed terminal refusal now arrives for work already delivered as C:
  // it must find no row to transfer and create no second sendable recovery.
  expect(await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late")).toBeUndefined();
  expect(await f.storage.getRecovery(a.clientMutationId)).toBeUndefined();
  expect(await f.storage.listRecovery("local:queued")).toHaveLength(0);
});

// Round-3 review M-B (half 1): the AUTOMATIC mint - reconciliation's
// proven-absent innocent-row requeue - can run in ANY tab, so stamping the
// fresh row with the minting tab's identity loses the sender's provenance:
// the sender's own recordSubmittedHere filters by originClientId and never
// claims the row, and its Send/Queue routing mis-routes behind the send.
// The automatic mint carries the source row's originClientId; only an
// explicit Retry press - a user-chosen actor - stamps the pressing tab.
test("an automatic requeue mint preserves the sender's tab identity", async () => {
  setMutationClientIdentityForTests("sender-tab");
  try {
    const f = fixture();
    const a = await f.storage.enqueueIntent(queuedIntent());
    await f.storage.markAttempted(a.clientMutationId);
    await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
    // Reconciliation runs in ANOTHER tab...
    setMutationClientIdentityForTests("reconciling-tab");
    const restored = await f.storage.restoreProvenAbsent(a.targetRef, new Set());
    expect(restored).toHaveLength(1);
    const restoredId = restored[0]?.clientMutationId;
    if (!restoredId) throw new Error("missing restored row");
    const fresh = await f.storage.getOutbox(restoredId);
    if (!fresh) throw new Error("missing restored row");
    // ...but the fresh row keeps the SENDER's provenance and the inherited
    // wire id, so the sender's tab still recognizes its own pending send.
    expect(fresh.originClientId).toBe("sender-tab");
    expect(fresh.payload.clientMutationId).toBe(a.clientMutationId);
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});

// The scope pin, disclosed: an explicit Retry press is a user-chosen actor,
// so its mint stamps the PRESSING tab's identity - the reviewer's scope for
// the fix is automatic requeues only.
test("an explicit Retry mint stamps the pressing tab's identity", async () => {
  setMutationClientIdentityForTests("pressing-tab");
  try {
    const f = fixture();
    const a = await f.storage.enqueueIntent(queuedIntent());
    await f.storage.markAttempted(a.clientMutationId);
    await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
    const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
    const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
      record: capture.record,
      stopEpoch: capture.stopEpoch,
      displayedStopEpoch: 0,
    });
    expect(b?.originClientId).toBe("pressing-tab");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});

// Round-4 review M1: the round-3 consume-time retirement filtered to
// superseded siblings only, so a replacement still SUBMITTING (mid-flight)
// or CANCELED on the same wire survived the consumption of its predecessor's
// recovery - and the surviving sibling's later terminal refusal then found
// no live wire copy and minted a second sendable recovery for work the
// resend already delivered. The consumption retires the wire's remaining
// rows whatever their state: the fresh resend row is the composition's one
// live row, and every other row of that wire is either a settled predecessor
// or a duplicate whose late outcomes must land inert on a missing row.
test("a consumed recovery retires its submitting wire sibling, whose late refusal cannot resurrect delivered work", async () => {
  const f = fixture();
  // A: attempted, response lost -> delivery-uncertain.
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  // Retry mints B under A's wire id; B stays submitting - mid-flight.
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  expect(b.state).toBe("submitting");
  // A's delayed refusal moves the superseded predecessor into recovery.
  const recovery = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late");
  if (!recovery) throw new Error("failed to seed recovery");
  // The user resends A's recovery: R consumed, C enqueued; C delivers.
  const sent = await f.storage.resendRecoveryResult(a.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  await f.storage.markAttempted(sent.record.clientMutationId);
  expect(await f.storage.settleReceipt(sent.record.clientMutationId, "reflected")).toBe(true);
  // The mid-flight sibling retired with the consumption...
  expect(await f.storage.getOutbox(b.clientMutationId)).toBeUndefined();
  // ...so its late refusal finds no row and creates no second recovery for
  // work the resend already delivered.
  expect(await f.storage.transferToRecovery(b.clientMutationId, "rejected", "late")).toBeUndefined();
  expect(await f.storage.getRecovery(b.clientMutationId)).toBeUndefined();
  expect(await f.storage.listRecovery("local:queued")).toHaveLength(0);
});

// The canceled half of M1: a Stop crossed while the replacement sat
// unattempted leaves it the user's visible canceled strip row - and it is a
// duplicate of the composition the resend just re-delivered. Retiring it at
// consume time makes its chip leave the canceled strip, the same
// tail-position surface an explicit requeue already has, and a later press
// can no longer re-queue delivered work from it.
// Round-6 review H:1613 - a preserved composition is NOT a wire sibling. The
// preserve path copied the predecessor's payload id into the preserved draft,
// so after another pane advanced recovery A, a locally preserved draft B sat in
// A's wire family - and a late settlement of A swept B away through the
// recovery-store sweep, losing the preserved composition and its bytes. The
// preserved draft is independent work: its payload id must be its OWN fresh
// key, so predecessor settlements cannot reach it.
test("a preserved composition is not a wire sibling: predecessor settlements cannot retire it", async () => {
  const f = fixture();
  // A: refused, its recovery row the preserve's base.
  const a = await f.storage.enqueueIntent(queuedIntent());
  const recovery = await f.storage.transferToRecovery(a.clientMutationId, "rejected");
  if (!recovery) throw new Error("failed to seed recovery");
  // Another pane advanced A; this pane preserves its local edits as a fresh
  // recovery owner B.
  const preserved = await f.storage.preserveRecoveryComposition(
    recovery,
    [{ type: "text", text: "locally preserved edits" }],
    [],
    "locally preserved edits",
  );
  if (!preserved) throw new Error("failed to preserve composition");
  // The preserved draft carries its OWN wire identity, never the
  // predecessor's.
  expect(preserved.payload.clientMutationId).toBe(preserved.clientMutationId);
  expect(preserved.payload.clientMutationId).not.toBe(a.clientMutationId);
  // A late authoritative settlement of the predecessor's wire must not
  // retire the preserved composition.
  expect(await f.storage.settleReceipt(a.clientMutationId, "reflected")).toBe(true);
  expect(await f.storage.getRecovery(preserved.clientMutationId)).toMatchObject({
    composerText: "locally preserved edits",
  });
});

// Round-5 review H1: the recovery resend assigned a FRESH wire id even when
// the consumed recovery's lineage is delivery-uncertain - a refusal that never
// reached the daemon (the hub refused the shape before any execution, so the
// daemon journaled nothing for the wire). An outstanding sibling RPC of the
// same wire can still apply, and a fresh-id resend is a composition the
// daemon's replay journal cannot dedupe: double application. The resent row
// must INHERIT the wire id (fresh storage key, inherited payload id) exactly
// like a mint from blockedUnknown, so the journal replays instead.
test("a resend of a delivery-uncertain recovery inherits its wire identity so an outstanding sibling cannot double-apply", async () => {
  const f = fixture();
  // A: attempted, response lost -> delivery-uncertain.
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  // Retry mints B under A's wire id; B stays submitting - its RPC is the
  // outstanding attempt that may still apply.
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  // B's terminal PRE-DAEMON refusal: the hub refused the request's shape
  // without running it, so the daemon never journaled a rejection for the
  // wire - the uncertainty travels into the recovery.
  await f.storage.markAttempted(b.clientMutationId);
  await f.storage.markUnknown(b.clientMutationId, "blockedUnknown");
  const recovery = await f.storage.transferToRecovery(b.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  if (!recovery) throw new Error("failed to seed recovery");
  // The user resends: the fresh row takes a fresh STORAGE key but carries the
  // inherited WIRE id in its payload, so the daemon's journal replays it
  // against B's outstanding attempt instead of applying the composition twice.
  const sent = await f.storage.resendRecoveryResult(b.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  expect(sent.record.clientMutationId).not.toBe(b.clientMutationId);
  expect(sent.record.payload.clientMutationId).toBe(a.clientMutationId);
  // The resent row settles as the wire's one delivery: a receipt naming the
  // wire id (a replay of B's journaled attempt) settles it exactly like a
  // minted replacement.
  await f.storage.markAttempted(sent.record.clientMutationId);
  expect(await f.storage.settleReceipt(sent.record.clientMutationId, "reflected")).toBe(true);
  expect(await f.storage.getOutbox(sent.record.clientMutationId)).toBeUndefined();
});

// Round-6 review M:1761 - inheritance is only sound for byte-identical
// replays. The daemon journals a client mutation by id WITH its method and
// payload hash (agent/session_client_mutation.go: errClientMutationMismatch);
// resending EDITED content under the inherited id is a mismatch the daemon
// refuses before journaling, which lands the row back in a marked-uncertain
// recovery whose resend inherits the same id again - the mismatch loop. An
// edited or rerouted resend is NEW work: it takes a fresh wire identity.
test("an edited resend of a delivery-uncertain recovery takes a fresh wire identity", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  const recovery = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  if (!recovery) throw new Error("failed to seed recovery");
  // The user edits the recovery's input; the resend carries new content.
  const edited = await f.storage.updateRecoveryInput(
    a.clientMutationId,
    [{ type: "text", text: "edited content" }],
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    0,
  );
  if (!edited) throw new Error("failed to edit recovery");
  // Round-7 rewrite, disclosed: the resend now carries the EDITED content the
  // user actually resends, not the original fixture. The byte-identity
  // contract is judged against what goes on the wire; resending the original
  // input never exercised the autosaved-edit path the round-7 High names.
  const sent = await f.storage.resendRecoveryResult(a.clientMutationId, {
    ...queuedIntent(),
    payload: { ...queuedIntent().payload, input: [{ type: "text", text: "edited content" }] },
  });
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  // The edited composition is new work: a fresh wire identity, so the
  // daemon journals it as its own mutation instead of mismatching the
  // original wire's method-and-payload hash forever.
  expect(sent.record.payload.clientMutationId).toBe(sent.record.clientMutationId);
  expect(sent.record.payload.clientMutationId).not.toBe(a.clientMutationId);
});

// Round-7 review High: the byte-identity check compared the resend against
// the recovery record AS IT STOOD AT CONSUME TIME, but the composer's
// autosave replaces payload.input in place while preserving the uncertainty
// marker - so an edited composition compared equal to its own edited record
// and inheritance was authorized for content the daemon never journaled
// under that wire: the mismatch loop again, and a delayed settlement of the
// original wire could sweep-delete the edited resend after the composer
// cleared. The daemon's journal is the immutable reference: the comparison
// runs against the composition frozen at transferToRecovery time, so an
// autosaved edit is NEW intent under a fresh id, unreachable by the
// original wire's late settlement.
test("an autosaved edit of a delivery-uncertain recovery is new intent, not a replay", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  const recovery = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  if (!recovery) throw new Error("failed to seed recovery");
  // The composer's autosave replaces the input in place, revision and all.
  const autosaved = await f.storage.updateRecoveryInput(
    a.clientMutationId,
    [{ type: "text", text: "autosaved edit" }],
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    0,
  );
  if (!autosaved) throw new Error("failed to autosave");
  // The user resends the edited content the autosave actually holds.
  const sent = await f.storage.resendRecoveryResult(
    a.clientMutationId,
    { ...queuedIntent(), payload: { ...queuedIntent().payload, input: [{ type: "text", text: "autosaved edit" }] } },
    undefined,
    { [a.clientMutationId]: 1 },
  );
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  // The edited composition is new work: its own wire identity, not the
  // inherited one the daemon would mismatch against its journal forever.
  expect(sent.record.payload.clientMutationId).toBe(sent.record.clientMutationId);
  expect(sent.record.payload.clientMutationId).not.toBe(a.clientMutationId);
  // And the original wire's delayed settlement cannot reach the edited
  // resend: the fresh row is not in that wire's family, so no sweep can
  // delete it after the composer cleared.
  await f.storage.markAttempted(sent.record.clientMutationId);
  await f.storage.settleReceipt(a.clientMutationId, "reflected");
  expect(await f.storage.getOutbox(sent.record.clientMutationId)).toBeDefined();
});

// The rerouted half, REWRITTEN for round-8 (disclosed): a byte-identical
// payload under a different METHOD is not different work - the client-side
// route/method choice cannot be allowed to mint a second wire for a
// composition whose sibling RPC can still apply (the round-8 High, reached
// through destinationSendRoute's automatic send-to-queue reroute). The
// replay submits under the ORIGINAL method with the inherited wire id, and
// the daemon's journal dedupes it; the round-6 "fresh id on reroute"
// contract is superseded. Edited PAYLOADS remain new work under fresh ids
// (the test above), and journaled rejections keep fresh ids (the pin below).
test("a rerouted resend of an unchanged delivery-uncertain recovery replays under its original method and wire identity", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  const recovery = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  if (!recovery) throw new Error("failed to seed recovery");
  const rerouted = { ...queuedIntent("local:queued"), method: "turn/start" };
  const sent = await f.storage.resendRecoveryResult(a.clientMutationId, rerouted);
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  // Identity is a unit: the resend keeps the wire's ORIGINAL method and the
  // inherited id - the route's method choice is discarded, never honored
  // with a fresh wire.
  expect(sent.record.method).toBe(a.method);
  expect(sent.record.payload.clientMutationId).toBe(a.clientMutationId);
});

// The complementary pin: a refusal the daemon JOURNALED (in band, naming this
// mutation) is authoritative for the wire - the daemon will never apply it -
// so its recovery resends under a FRESH wire identity. Green before and after.
test("a resend of a journaled-rejected recovery takes a fresh wire identity", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.transferToRecovery(a.clientMutationId, "rejected", "not accepted");
  const sent = await f.storage.resendRecoveryResult(a.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  expect(sent.record.payload.clientMutationId).toBe(sent.record.clientMutationId);
});

// Round-8 review Medium: #retireOlderBlockedNoteRows superseded older
// blockedUnknown note rows inside the settlement's own write but returned
// nothing, so their ids never reached the settlement's post-commit
// retiredIds notification. Their in-memory delivery registrations survived
// until the next successful background read cleared them, and a storage
// wedge inside that window left admitDirectSend refusing fallback sends
// for work that can never dispatch again (superseded rows never dispatch).
// The notification must name them, like every other retired row.
test("a settled newer note save reports the older blocked rows it superseded", async () => {
  const f = fixture();
  const noteIntent = (text: string) => ({
    targetRef: "local:queued",
    threadId: "thread",
    method: "notes/human/set",
    payload: { ref: "local:queued", input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { method: "notes/human/set", input: [{ type: "text", text }] },
  });
  const older = await f.storage.enqueueIntent(noteIntent("older blocked note"));
  await f.storage.markAttempted(older.clientMutationId);
  await f.storage.markUnknown(older.clientMutationId, "blockedUnknown");
  const newer = await f.storage.enqueueIntent(noteIntent("newer note text"));
  const result = await f.storage.settleReceiptResult(newer.clientMutationId, "reflected");
  expect(result.settled).toBe(true);
  expect((await f.storage.getOutbox(older.clientMutationId))?.state).toBe("superseded");
  expect(result.retiredIds).toContain(older.clientMutationId);
});

// Jesse's 2026-11-03 ruling on the #5583 collision: both Edit and Remove
// reach inherited-wire rows, and the duplicate problem is solved
// MECHANICALLY, not by hiding the verb. The edit seam stamps the row's
// delivery-uncertainty identity onto the editing recovery - the same
// stamping transferToRecovery does - so an UNCHANGED resend replays under
// the inherited wire id (journal dedup, no double-apply) while changed
// content resends as the user's new message under a fresh id. The predicate
// is uniform across the two uncertainty shapes: a blockedUnknown row's own
// attempt may have landed, and an inherited-wire row's predecessor may have
// landed under the shared wire id.
test("an edited inherited-wire row resends unchanged under its inherited wire id", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  expect(b.payload.clientMutationId).toBe(a.clientMutationId);
  const bNow = await f.storage.getOutbox(b.clientMutationId);
  if (!bNow) throw new Error("minted row missing");
  const recovery = await f.storage.editQueuedMessage(bNow, { stopEpoch: 0 });
  if (!recovery) throw new Error("edit failed");
  const sent = await f.storage.resendRecoveryResult(recovery.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  expect(sent.record.payload.clientMutationId).toBe(a.clientMutationId);
});

test("an edited blockedUnknown row resends unchanged under its own wire id", async () => {
  const f = fixture();
  const x = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(x.clientMutationId);
  await f.storage.markUnknown(x.clientMutationId, "blockedUnknown");
  const xNow = await f.storage.getOutbox(x.clientMutationId);
  if (!xNow) throw new Error("row missing");
  const recovery = await f.storage.editQueuedMessage(xNow, { stopEpoch: 0 });
  if (!recovery) throw new Error("edit failed");
  const sent = await f.storage.resendRecoveryResult(recovery.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  expect(sent.record.payload.clientMutationId).toBe(x.clientMutationId);
});

// The ruling's boundary, changed-content side: an edit the user actually
// changed is their new message - a fresh wire identity, never an inherited
// one.
test("an edited-and-changed composition resends as new work under a fresh id", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  const bNow = await f.storage.getOutbox(b.clientMutationId);
  if (!bNow) throw new Error("minted row missing");
  const recovery = await f.storage.editQueuedMessage(bNow, { stopEpoch: 0 });
  if (!recovery) throw new Error("edit failed");
  const sent = await f.storage.resendRecoveryResult(recovery.clientMutationId, {
    ...queuedIntent(),
    payload: { ...queuedIntent().payload, input: [{ type: "text", text: "changed my mind" }] },
  });
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  expect(sent.record.payload.clientMutationId).toBe(sent.record.clientMutationId);
  expect(sent.record.payload.clientMutationId).not.toBe(a.clientMutationId);
});

// The ruling's boundary, canceled side: a provably-never-delivered canceled
// row carries no uncertainty, so its edit resends under a fresh id.
test("an edited canceled row resends under a fresh id", async () => {
  const f = fixture();
  const c = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.cancelUnattempted("local:queued");
  const cNow = await f.storage.getOutbox(c.clientMutationId);
  if (cNow?.state !== "canceled") throw new Error("canceled fixture failed");
  // The cancel scan bumps the ref's stop epoch: the edit's press-time barrier
  // reads the POST-cancel epoch, exactly like a real press after a Stop.
  const recovery = await f.storage.editQueuedMessage(cNow, {
    stopEpoch: await f.storage.readStopEpoch("local:queued"),
  });
  if (!recovery) throw new Error("edit failed");
  const sent = await f.storage.resendRecoveryResult(recovery.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  expect(sent.record.payload.clientMutationId).toBe(sent.record.clientMutationId);
});

// Seam 2, the #5583 ruling's Remove half: removal ends the composition, so it
// ends the wire - the round-9 routing at the new seam. The removed row's
// wire siblings (an inherited-wire row's superseded predecessor) retire
// atomically in the same write, and the retired keys - including the removed
// row's own - reach the retiredIds notification, so the runtime's live
// registration clears without waiting for a background read. A delayed
// terminal refusal for the wire then lands inert: no resurrection of work
// the user removed. (If the wire's in-flight attempt genuinely lands, the
// message may still appear in the conversation - it delivered; nothing may
// re-queue it locally.)
test("removing an inherited-wire row retires its wire so a late refusal cannot resurrect it", async () => {
  const f = fixture();
  const retired: string[] = [];
  f.storage.setRetiredIdsListener((ids) => retired.push(...ids));
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  const bNow = await f.storage.getOutbox(b.clientMutationId);
  if (!bNow) throw new Error("minted row missing");
  expect(await f.storage.removeQueuedMessage(bNow, { stopEpoch: 0 })).toBe(true);
  expect(await f.storage.getOutbox(a.clientMutationId)).toBeUndefined();
  expect(retired).toEqual(expect.arrayContaining([a.clientMutationId, b.clientMutationId]));
  expect(await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late refusal")).toBeUndefined();
});

// Round-13 review Medium, the Edit half: the edit seam's recovery check was
// keyed on the row's OWN storage key alone, so a late journaled refusal of
// the predecessor - which transfers an UNEDITED recovery copy under the
// PREDECESSOR's key while the minted replacement waits in the outbox - sat
// outside the check. Editing the replacement inserted a SECOND copy of the
// same wire, and both copies ride the persistence read's recovery list -
// the feed an activation's consolidation consumes from - so the same text
// and attachments would merge twice into an active recovery. The mechanical
// coalesce, per the #5583 ruling: the editing recovery minted under the
// row's key IS the wire's one recovery, stamped with the wire's
// uncertainty (an unchanged resend replays under the inherited wire id);
// the unedited carrier retires in the same write. One copy means the
// activation's consume set cannot double-merge what does not exist.
test("editing a replacement whose predecessor left an unedited recovery coalesces to the one wire copy", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  const bNow = await f.storage.getOutbox(b.clientMutationId);
  if (!bNow) throw new Error("minted row missing");
  // The predecessor's delayed terminal refusal: an UNEDITED recovery under
  // A's own key, no uncertainty marker (a journaled rejection resends fresh).
  const carrier = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late refusal");
  if (!carrier) throw new Error("failed to seed the wire's carrier");
  const recovery = await f.storage.editQueuedMessage(bNow, { stopEpoch: 0 });
  if (!recovery) throw new Error("edit failed");
  // The wire's ONE recovery copy: the stamped editing recovery under the
  // row's own key, carrying the inherited wire identity for the replay.
  const copies = await f.storage.listRecovery("local:queued");
  expect(copies).toHaveLength(1);
  expect(copies[0]?.clientMutationId).toBe(b.clientMutationId);
  expect(copies[0]?.payload.clientMutationId).toBe(a.clientMutationId);
  expect((copies[0] as { wireDeliveryUncertain?: boolean })?.wireDeliveryUncertain).toBe(true);
});

// The preservation boundary of the same seam: an INDEPENDENTLY EDITED
// carrier is the user's live draft, and it owns the wire's editing session -
// the own-key guard's rule, one more transition. The replacement row's Edit
// refuses, and the edited draft survives untouched.
test("an edited recovery carrier owns the wire's editing session and survives a sibling row's Edit", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  const bNow = await f.storage.getOutbox(b.clientMutationId);
  if (!bNow) throw new Error("minted row missing");
  const carrier = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late refusal");
  if (!carrier) throw new Error("failed to seed the wire's carrier");
  // The user edits the carrier's draft: it is now independently edited work.
  const edited = await f.storage.updateRecoveryInput(
    carrier.clientMutationId,
    [{ type: "text", text: "the user's edited draft" }],
    [],
    "the user's edited draft",
    undefined,
    undefined,
    undefined,
    0,
  );
  if (!edited) throw new Error("carrier edit failed");
  expect(edited.compositionRevision ?? 0).toBeGreaterThan(0);
  expect(await f.storage.editQueuedMessage(bNow, { stopEpoch: 0 })).toBeUndefined();
  const surviving = await f.storage.getRecovery(carrier.clientMutationId);
  expect(surviving?.compositionRevision ?? 0).toBeGreaterThan(0);
  expect(surviving?.payload.input).toEqual([{ type: "text", text: "the user's edited draft" }]);
});

// Round-13 review Medium, the Remove half: removal ends the composition, so
// it ends the wire - the round-10 seam's rule, applied to the wire's
// RECOVERY carriers too. An unedited carrier under the predecessor's key
// offered the removed input for resend; with the family sweep's unedited
// rule (settlementRetiresRecovery) it retires in the same write, and its
// key rides the retiredIds notification so the runtime clears without a
// background read.
test("removing a replacement retires the wire's unedited recovery sibling in the same write", async () => {
  const f = fixture();
  const retired: string[] = [];
  f.storage.setRetiredIdsListener((ids) => retired.push(...ids));
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  const bNow = await f.storage.getOutbox(b.clientMutationId);
  if (!bNow) throw new Error("minted row missing");
  const carrier = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late refusal");
  if (!carrier) throw new Error("failed to seed the wire's carrier");
  expect(await f.storage.removeQueuedMessage(bNow, { stopEpoch: 0 })).toBe(true);
  // The removed input leaves the tray: the unedited carrier retired with the
  // row, in the same write, its key on the notification.
  expect(await f.storage.getRecovery(carrier.clientMutationId)).toBeUndefined();
  expect(await f.storage.getOutbox(a.clientMutationId)).toBeUndefined();
  expect(retired).toEqual(expect.arrayContaining([a.clientMutationId, b.clientMutationId]));
  expect(await f.storage.listRecovery("local:queued")).toEqual([]);
});

// The same preservation boundary for Remove: an EDITED carrier is the user's
// live draft and survives the sibling row's removal - the family sweep's
// unedited-only rule (settlementRetiresRecovery) holds at this seam too.
test("an edited recovery sibling survives removal of its wire's outbox row", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  const bNow = await f.storage.getOutbox(b.clientMutationId);
  if (!bNow) throw new Error("minted row missing");
  const carrier = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late refusal");
  if (!carrier) throw new Error("failed to seed the wire's carrier");
  const edited = await f.storage.updateRecoveryInput(
    carrier.clientMutationId,
    [{ type: "text", text: "the user's edited draft" }],
    [],
    "the user's edited draft",
    undefined,
    undefined,
    undefined,
    0,
  );
  if (!edited) throw new Error("carrier edit failed");
  expect(edited.compositionRevision ?? 0).toBeGreaterThan(0);
  expect(await f.storage.removeQueuedMessage(bNow, { stopEpoch: 0 })).toBe(true);
  const surviving = await f.storage.getRecovery(carrier.clientMutationId);
  expect(surviving?.compositionRevision ?? 0).toBeGreaterThan(0);
  expect(surviving?.payload.input).toEqual([{ type: "text", text: "the user's edited draft" }]);
  // The outbox rows are gone; the wire's remaining life is the edited draft.
  expect(await f.storage.getOutbox(a.clientMutationId)).toBeUndefined();
  expect(await f.storage.getOutbox(b.clientMutationId)).toBeUndefined();
});

// Round-10 review Medium: when the daemon's journal write keeps failing while
// authoritative reads succeed, every 2-second reconcile cycle takes the
// innocent-row path - the blockedUnknown generation settles superseded (its
// full payload and image bytes retained) and the mint queues another full
// copy under the inherited wire id. NOTHING bounded the retained
// generations: unbounded accumulation of byte-identical superseded copies
// until the origin quota exhausts and further mints fail, leaving delivery
// stuck even after the daemon recovers. The bound lives at the mint - the
// one place the accumulation is created: redundant OLDER superseded
// siblings of the wire retire in the same write, keeping exactly ONE
// evidence-bearing superseded row (the newest predecessor) so the wire's
// late outcome still reconciles through it.
test("a repeatedly uncertain wire bounds its retained superseded generations at the mint", async () => {
  const f = fixture();
  const bigText = `payload `.repeat(2048);
  const largeIntent = {
    ...queuedIntent(),
    payload: { ...queuedIntent().payload, input: [{ type: "text", text: bigText }] },
  };
  const a = await f.storage.enqueueIntent(largeIntent);
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const generations: string[] = [a.clientMutationId];
  // Three reconcile cycles while the journal write keeps failing: each
  // generation is proven absent and mints the next full copy.
  for (let cycle = 0; cycle < 3; cycle += 1) {
    const restored = await f.storage.restoreProvenAbsent("local:queued", new Set());
    expect(restored).toHaveLength(1);
    const fresh = (await f.storage.listOutbox("local:queued")).find(
      (row) => row.clientMutationId === restored[0]?.clientMutationId,
    );
    if (!fresh) throw new Error("no minted generation");
    // The journal write failed again: the fresh generation went uncertain.
    await f.storage.markAttempted(fresh.clientMutationId);
    await f.storage.markUnknown(fresh.clientMutationId, "blockedUnknown");
    generations.push(fresh.clientMutationId);
  }
  const superseded = (await f.storage.listOutbox("local:queued")).filter((row) => row.state === "superseded");
  // Bounded retention: ONE superseded evidence row - the newest predecessor -
  // not one full copy per cycle.
  expect(superseded).toHaveLength(1);
  expect(superseded[0]?.clientMutationId).toBe(generations.at(-2));
  // The retained row keeps the wire identity and attempt evidence, so the
  // wire's late outcome still reconciles through it.
  expect(superseded[0]?.payload.clientMutationId).toBe(a.clientMutationId);
  expect(await f.storage.settleReceipt(generations.at(-2)!, "reflected")).toBe(true);
});

// Round-9 review Medium: the consolidation's write deleted secondary recovery
// rows directly - no wire-family resolution, no family retirement, no
// retiredIds notification. Retry leaves the predecessor superseded in the
// outbox carrying its attempt evidence; when the replacement's recovery is
// consumed by a consolidation and the merged composition sends, the
// predecessor's delayed terminal refusal finds no live carrier and mints a
// FRESH sendable recovery of already-sent content. Consuming a recovery ends
// its wire's life in the same write, exactly like the resend's consume.
test("a consolidation that consumes a recovery retires its wire so a late refusal cannot resurrect sent content", async () => {
  const f = fixture();
  const retired: string[] = [];
  f.storage.setRetiredIdsListener((ids) => retired.push(...ids));
  // A: attempted, response lost -> Retry mints B under A's wire id.
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  // B is refused pre-daemon: its recovery carries the wire's uncertainty, and
  // A settles superseded in the outbox, keeping its attempt evidence.
  await f.storage.markAttempted(b.clientMutationId);
  const recoveryB = await f.storage.transferToRecovery(b.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  if (!recoveryB) throw new Error("failed to seed B's recovery");
  // An independent recovery on the same target: the consolidation survivor.
  const c = await f.storage.enqueueIntent(queuedIntent());
  const recoveryC = await f.storage.transferToRecovery(c.clientMutationId, "rejected", "not needed");
  if (!recoveryC) throw new Error("failed to seed C's recovery");
  // The consolidation absorbs B's recovery into C's (the delete-sources path).
  const merged = await f.storage.updateRecoveryInput(
    recoveryC.clientMutationId,
    [{ type: "text", text: "combined" }],
    [],
    "combined",
    [],
    [recoveryB.clientMutationId],
    undefined,
    0,
    { [recoveryC.clientMutationId]: 0, [recoveryB.clientMutationId]: 0 },
  );
  if (!merged) throw new Error("consolidation failed");
  // The predecessor retired WITH the consumption - in the same write - and the
  // notification that clears the runtime's delivery registrations names it.
  expect(await f.storage.getOutbox(a.clientMutationId)).toBeUndefined();
  expect(retired).toContain(a.clientMutationId);
  // The merged composition sends successfully.
  const sent = await f.storage.resendRecoveryResult(recoveryC.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  await f.storage.markAttempted(sent.record.clientMutationId);
  expect(await f.storage.settleReceipt(sent.record.clientMutationId, "reflected")).toBe(true);
  // A's delayed terminal refusal: no live carrier and no row - it lands inert
  // instead of minting a sendable recovery of already-sent content.
  expect(await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late refusal")).toBeUndefined();
  expect(await f.storage.listRecovery()).toEqual([]);
});

// The explicit-discard variant of the same gap: discarding a recovery row
// deleted only the row, so the wire's superseded predecessor stayed in the
// outbox and its late refusal resurrected the discarded composition as a
// fresh sendable recovery.
test("an explicit discard retires the recovery's wire so a late refusal cannot resurrect discarded content", async () => {
  const f = fixture();
  const retired: string[] = [];
  f.storage.setRetiredIdsListener((ids) => retired.push(...ids));
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  await f.storage.markAttempted(b.clientMutationId);
  const recoveryB = await f.storage.transferToRecovery(b.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  if (!recoveryB) throw new Error("failed to seed B's recovery");
  expect(await f.storage.discardRecovery(recoveryB.clientMutationId)).toBe(true);
  // The wire's rows retire with the discard, and the notification names them.
  expect(await f.storage.getOutbox(a.clientMutationId)).toBeUndefined();
  expect(retired).toContain(a.clientMutationId);
  // A's delayed terminal refusal lands inert: no resurrection.
  expect(await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late refusal")).toBeUndefined();
  expect(await f.storage.listRecovery()).toEqual([]);
});

// Round-5 review H2: the settlement's sibling sweep enumerated the outbox and
// optimistic stores only, so a wire sibling living as an UNEDITED recovery
// record escaped both the named lookup (the receipt names the predecessor's
// key) and the sweep - a late receipt for the wire settled its own record and
// left the sibling's recovery sendable, offering already-delivered work under
// a fresh id. The sweep must cover the recovery store, preserving edited
// compositions (settlementRetiresRecovery).
test("a settled wire retires its unedited recovery sibling, so the tray never offers delivered work", async () => {
  const f = fixture();
  // A: the predecessor whose lost-response attempt actually landed.
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  // Retry mints B under A's wire id; B is refused and its unedited recovery
  // copy (key B, wire A) is the sibling the settlement must reach.
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  await f.storage.markAttempted(b.clientMutationId);
  await f.storage.markUnknown(b.clientMutationId, "blockedUnknown");
  const recovery = await f.storage.transferToRecovery(b.clientMutationId, "rejected");
  if (!recovery) throw new Error("failed to seed recovery");
  // A receipt naming A proves the WIRE delivered: A settles and B's unedited
  // recovery must retire with it.
  expect(await f.storage.settleReceipt(a.clientMutationId, "reflected")).toBe(true);
  expect(await f.storage.getRecovery(b.clientMutationId)).toBeUndefined();
  expect(await f.storage.listRecovery("local:queued")).toHaveLength(0);
});

// settleApplied carries the same sweep.
test("an applied settlement retires its unedited recovery sibling", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  await f.storage.markAttempted(b.clientMutationId);
  await f.storage.markUnknown(b.clientMutationId, "blockedUnknown");
  const recovery = await f.storage.transferToRecovery(b.clientMutationId, "rejected");
  if (!recovery) throw new Error("failed to seed recovery");
  expect(await f.storage.settleApplied(a.clientMutationId)).toMatchObject({ clientMutationId: a.clientMutationId });
  expect(await f.storage.getRecovery(b.clientMutationId)).toBeUndefined();
});

// Round-6 review M:776 - a wire whose every outbox and optimistic carrier is
// gone but whose UNEDITED recovery copy still lives must still settle from a
// late authoritative event: recovery A consumed into an inherited-id resend,
// another pre-daemon refusal moving the resent row into recovery - only the
// recovery carrier holds the wire, and the settlement refused it.
test("a late settlement for a wire living only as a recovery carrier settles the wire and retires the tray row", async () => {
  const f = fixture();
  // A: delivery-uncertain, refused before the daemon.
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  const recovery = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  if (!recovery) throw new Error("failed to seed recovery");
  // The recovery is consumed by its byte-identical resend, which inherits
  // the wire id under a fresh storage key.
  const sent = await f.storage.resendRecoveryResult(a.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  expect(sent.record.payload.clientMutationId).toBe(a.clientMutationId);
  // The resent row is refused before the daemon again and moves into
  // recovery: the wire's ONLY carrier left anywhere is this recovery row.
  await f.storage.markAttempted(sent.record.clientMutationId);
  const second = await f.storage.transferToRecovery(sent.record.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  if (!second) throw new Error("failed to seed second recovery");
  expect(await f.storage.getOutbox(a.clientMutationId)).toBeUndefined();
  expect(await f.storage.getOutbox(sent.record.clientMutationId)).toBeUndefined();
  // A late authoritative event naming A must settle the wire - and retire
  // the sendable tray row that claims already-delivered work.
  expect(await f.storage.settleReceipt(a.clientMutationId, "reflected")).toBe(true);
  expect(await f.storage.getRecovery(sent.record.clientMutationId)).toBeUndefined();
  expect(await f.storage.listRecovery("local:queued")).toHaveLength(0);
});

// The preserve pin: an independently edited composition (compositionRevision
// past zero) survives the wire's settlement - its edits are never lost.
test("a settlement never retires an edited recovery sibling of the settled wire", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  await f.storage.markAttempted(b.clientMutationId);
  await f.storage.markUnknown(b.clientMutationId, "blockedUnknown");
  const recovery = await f.storage.transferToRecovery(b.clientMutationId, "rejected");
  if (!recovery) throw new Error("failed to seed recovery");
  const edited = await f.storage.updateRecoveryInput(
    b.clientMutationId,
    [{ type: "text", text: "independently edited" }],
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    0,
  );
  if (!edited) throw new Error("failed to edit recovery");
  expect(await f.storage.settleReceipt(a.clientMutationId, "reflected")).toBe(true);
  expect(await f.storage.getRecovery(b.clientMutationId)).toMatchObject({
    compositionRevision: 1,
  });
});

test("a retained copy from an edited recovery keeps the frozen original input", async () => {
  const f = fixture();
  const queued = await f.storage.enqueueIntent(queuedIntent());
  const originalInput = (queued.payload as { input: InputItem[] }).input;
  const originalText = originalInput[0]?.text ?? "";
  await f.storage.markAttempted(queued.clientMutationId);
  await f.storage.transferToRecovery(queued.clientMutationId, "rejected", "invalid params", {
    wireDeliveryUncertain: true,
  });
  const edited = await f.storage.updateRecoveryInput(
    queued.clientMutationId,
    [{ type: "text", text: "edited after refusal" }],
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    0,
  );
  if (!edited) throw new Error("failed to edit recovery");

  await f.storage.settleApplied(queued.clientMutationId, { retainDisplayCopy: true });

  const optimistic = await f.storage.getOptimistic(queued.clientMutationId);
  if (!optimistic) throw new Error("retained display copy missing");
  expect((optimistic.payload as { input: InputItem[] }).input[0]?.text).toBe(originalText);
  expect((optimistic.optimisticDisplay as { input: InputItem[] }).input[0]?.text).toBe(originalText);
});

test("a consumed recovery retires its canceled wire sibling, whose chip leaves the strip", async () => {
  const f = fixture();
  const a = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(a.clientMutationId);
  await f.storage.markUnknown(a.clientMutationId, "blockedUnknown");
  const capture = await f.storage.getOutboxWithStopEpoch(a.clientMutationId);
  const b = await f.storage.mintRetryFromRow(a.clientMutationId, {
    record: capture.record,
    stopEpoch: capture.stopEpoch,
    displayedStopEpoch: 0,
  });
  if (!b) throw new Error("mint failed");
  // A crossed Stop cancels the unattempted replacement: the strip row.
  await f.storage.enqueueInterruptAndCancel({
    targetRef: "local:queued",
    threadId: "thread",
    method: "turn/interrupt",
    payload: { ref: "local:queued" },
    attachments: [],
    optimisticDisplay: { method: "turn/interrupt" },
  });
  expect((await f.storage.getOutbox(b.clientMutationId))?.state).toBe("canceled");
  // A's delayed refusal moves the superseded predecessor into recovery; the
  // user resends it and C delivers.
  const recovery = await f.storage.transferToRecovery(a.clientMutationId, "rejected", "late");
  if (!recovery) throw new Error("failed to seed recovery");
  const sent = await f.storage.resendRecoveryResult(a.clientMutationId, queuedIntent());
  if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
  await f.storage.markAttempted(sent.record.clientMutationId);
  expect(await f.storage.settleReceipt(sent.record.clientMutationId, "reflected")).toBe(true);
  // The canceled duplicate chip left the strip with the consumption, and
  // the predecessor's own recovery went with the resend that consumed it:
  // nothing of the composition remains in the outbox queue except the
  // crossed Stop's own interrupt row - the resend's fresh row is the
  // composition's one live row, delivered and settled.
  expect(await f.storage.getOutbox(b.clientMutationId)).toBeUndefined();
  // The crossed Stop's own interrupt row is this ref's queue business too;
  // among the composition's rows only the predecessor's terminal evidence
  // remains.
  const remaining = (await f.storage.listOutbox("local:queued")).filter((record) => record.method !== "turn/interrupt");
  expect(remaining).toEqual([]);
});

test("removeQueuedMessage clears a delivery-uncertain row the user lets go", async () => {
  const f = fixture();
  const uncertain = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(uncertain.clientMutationId);
  await f.storage.markUnknown(uncertain.clientMutationId, "blockedUnknown");
  const waiting = await f.storage.enqueueIntent(queuedIntent());
  const uncertainNow = await f.storage.getOutbox(uncertain.clientMutationId);
  if (uncertainNow === undefined) throw new Error("fixture row missing");

  // An uncertain row is local work like any other: the user's Remove reaches it.
  expect(await f.storage.removeQueuedMessage(uncertainNow, { stopEpoch: 0 })).toBe(true);
  expect(await f.storage.getOutbox(uncertain.clientMutationId)).toBeUndefined();
  expect(await f.storage.getOutbox(waiting.clientMutationId)).toMatchObject({ state: "submitting" });
});

test("a row another tab already settled is not removed again", async () => {
  const f = fixture();
  const uncertain = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(uncertain.clientMutationId);
  await f.storage.markUnknown(uncertain.clientMutationId, "blockedUnknown");
  const uncertainNow = await f.storage.getOutbox(uncertain.clientMutationId);
  if (uncertainNow === undefined) throw new Error("fixture row missing");
  // Another tab settled it before this click's write: the row is gone, and the
  // press reports no removal of its own.
  await f.peer.settleReceipt(uncertain.clientMutationId, "notProjected");

  expect(await f.storage.removeQueuedMessage(uncertainNow, { stopEpoch: 0 })).toBe(false);
  expect(await f.storage.getOutbox(uncertain.clientMutationId)).toBeUndefined();
});

test("editQueuedMessage opens a delivery-uncertain row for a fresh send", async () => {
  const f = fixture();
  const uncertain = await f.storage.enqueueIntent(queuedIntent());
  await f.storage.markAttempted(uncertain.clientMutationId);
  await f.storage.markUnknown(uncertain.clientMutationId, "blockedUnknown");
  const uncertainNow = await f.storage.getOutbox(uncertain.clientMutationId);
  if (uncertainNow === undefined) throw new Error("fixture row missing");

  const recovery = await f.storage.editQueuedMessage(uncertainNow, { stopEpoch: 0 });

  expect(recovery).toMatchObject({ recoveryKind: "editing" });
  expect(await f.storage.getOutbox(uncertain.clientMutationId)).toBeUndefined();
});
