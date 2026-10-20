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
  if (boundary === "commit") f.fail(true);
  else
    vi.spyOn(IDBObjectStore.prototype, boundary).mockImplementation(() => {
      throw new Error("injected");
    });
  await expect(f.storage.resendRecovery(ids, queuedIntent())).rejects.toBeDefined();
  vi.restoreAllMocks();
  f.fail(false);
  for (const id of ids) expect(await f.peer.getRecovery(id)).toBeDefined();
  expect(await f.peer.listOutbox()).toHaveLength(0);
  expect(await f.peer.resendRecovery(ids, queuedIntent())).toMatchObject({ intentSequence: 3 });
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
      await f.storage.updateRecoveryInput(first.clientMutationId, input, [], "combined", [], [...sources, "absent"]),
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
      f.storage.updateRecoveryInput(first.clientMutationId, input, [], "combined", [], sources),
    ).rejects.toBeDefined();
    vi.restoreAllMocks();
    f.fail(false);
    for (const row of rows)
      expect(await f.peer.getRecovery(row.clientMutationId)).toMatchObject({ composerText: "same input @inspect" });
    const saved = await f.peer.updateRecoveryInput(first.clientMutationId, input, [], "combined", [], sources);
    expect(saved).toMatchObject({ composerText: "combined", payload: { input }, composerMentions: [] });
    for (const id of sources) expect(await f.storage.getRecovery(id)).toBeUndefined();
    expect(await f.storage.getRecovery(foreign.clientMutationId)).toBeDefined();
  },
);

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
