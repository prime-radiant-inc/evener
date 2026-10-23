// @vitest-environment node
import {
  describeQueuedMessageStorage,
  queuedIntent,
} from "@evener/appwire-client/testing/queuedMessageStorageConformance";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { requestResult, transactionCompletion } from "./idbConnection";
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
    release: (record: { clientMutationId: string }, barrier: { stopEpoch: number }) =>
      peer.releaseCanceled(record.clientMutationId, barrier),
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
  expect(await f.storage.settleApplied(original.clientMutationId)).toBe(true);
  const reopened = await f.reopen();
  expect(await reopened.getRecovery(original.clientMutationId)).toMatchObject({
    compositionRevision: 1,
    composerText: "newer composition",
    payload: { input: [{ type: "text", text: "newer composition" }] },
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
