import type { ThreadReadResponse } from "@evener/appwire-client";
import { IDBDatabase, IDBFactory, IDBKeyRange, IDBObjectStore, IDBTransaction } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { EntryWriteAdmission } from "./entryWriteAdmission";
import {
  ENTRY_COVERAGE_STORE,
  ENTRY_ITEM_STORE,
  ENTRY_META_STORE,
  ENTRY_TURN_STORE,
  entryHistoryKey,
  type TranscriptResolvedIdentity,
  type TranscriptSessionMetadata,
} from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdIndexedDBEvent, holdNextWriteTransaction, settleRealTasks } from "./testing/stalledIndexedDB";
import { TranscriptRepository } from "./transcriptRepository";

const caches: Array<{ close(): void }> = [];
const databases: Array<{ factory: IDBFactory; name: string }> = [];
const openedDatabases: IDBDatabase[] = [];

function openDatabase(factory: IDBFactory, name: string): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = factory.open(name);
    request.addEventListener(
      "success",
      () => {
        openedDatabases.push(request.result);
        resolve(request.result);
      },
      { once: true },
    );
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

function deleteDatabase(factory: IDBFactory, name: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = factory.deleteDatabase(name);
    request.addEventListener("success", () => resolve(), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
    request.addEventListener("blocked", () => reject(new Error(`${name} database deletion blocked`)), { once: true });
  });
}

function requestValue<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

function transactionDone(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.addEventListener("complete", () => resolve(), { once: true });
    transaction.addEventListener("error", () => reject(transaction.error), { once: true });
    transaction.addEventListener("abort", () => reject(transaction.error), { once: true });
  });
}

async function countRows(factory: IDBFactory, name: string, store: string): Promise<number> {
  const database = await openDatabase(factory, name);
  const transaction = database.transaction(store, "readonly");
  const done = transactionDone(transaction);
  const count = await requestValue(transaction.objectStore(store).count());
  await done;
  return count;
}

afterEach(async () => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  for (const database of openedDatabases.splice(0)) database.close();
  for (const cache of caches.splice(0)) cache.close();
  for (const { factory, name } of databases.splice(0)) await deleteDatabase(factory, name);
});

function newCache() {
  const name = `entry-lifecycle-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  return { cache, factory, name };
}

function row(historyKey: string, entry: number) {
  return {
    historyKey,
    transcriptKey: `row-${entry}`,
    position: { entry, item: 0 },
    version: entry,
    item: { id: `row-${entry}`, turnId: "turn-1", type: "assistant", text: `body-${entry}` },
  };
}

it.each([
  ["count", "request"],
  ["count", "completion"],
  ["deleteHistory", "request"],
  ["deleteHistory", "completion"],
] as const)("bounds %s stalled %s and retires its connection", async (operation, event) => {
  const { cache, factory } = newCache();
  await cache.commitBatch({ historyKey: "seed", clearEpoch: 0, items: [row("seed", 1)] });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const closed = vi.spyOn(IDBDatabase.prototype, "close");
  const aborted = vi.spyOn(IDBTransaction.prototype, "abort");
  const opened = vi.spyOn(factory, "open");
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  if (event === "completion") {
    const transact = IDBDatabase.prototype.transaction;
    vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (this: IDBDatabase, ...args) {
      const transaction = transact.apply(this, args);
      if (!hold) hold = holdIndexedDBEvent(transaction, "complete");
      return transaction;
    });
  } else if (operation === "count") {
    const getAll = IDBObjectStore.prototype.getAll;
    vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
      const request = getAll.apply(this, args);
      if (!hold) hold = holdIndexedDBEvent(request, "success");
      return request;
    });
  } else {
    const get = IDBObjectStore.prototype.get;
    vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, ...args) {
      const request = get.apply(this, args);
      if (!hold) hold = holdIndexedDBEvent(request, "success");
      return request;
    });
  }
  let settled = false;
  const pending = (operation === "count" ? cache.count() : cache.deleteHistory("seed")).then((value) => {
    settled = true;
    return value;
  });
  try {
    await settleRealTasks(() => hold !== undefined);
    expect(hold).toBeDefined();
    await hold!.reached;
    await vi.advanceTimersByTimeAsync(9_999);
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(settled).toBe(true);
    expect(await pending).toBe(operation === "count" ? undefined : false);
    expect(aborted).toHaveBeenCalled();
    expect(closed).toHaveBeenCalled();
    hold!.release();
    await cache.count();
    expect(opened).toHaveBeenCalledTimes(1);
  } finally {
    hold?.release();
    await pending;
  }
});

it.each(["request", "completion"] as const)(
  "bounds a held write %s event, releases admission and fences late success",
  async (heldEvent) => {
    const { cache, factory } = newCache();
    // Open and exercise the connection before installing the event hold: this
    // must test the transaction deadline, not the existing open watchdog.
    expect(await cache.commitBatch({ historyKey: "seed", clearEpoch: 0, items: [row("seed", 1)] })).toMatchObject({
      outcome: "committed",
    });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const opened = vi.spyOn(factory, "open");
    const closed = vi.spyOn(IDBDatabase.prototype, "close");
    const aborted = vi.spyOn(IDBTransaction.prototype, "abort");
    const committed = vi.fn();
    cache.subscribeWrites(committed);
    const writes = vi.spyOn(cache, "commitBatch");
    let requestHold: ReturnType<typeof holdIndexedDBEvent> | undefined;
    const completionHold =
      heldEvent === "completion"
        ? holdNextWriteTransaction([ENTRY_ITEM_STORE, ENTRY_TURN_STORE, ENTRY_COVERAGE_STORE, ENTRY_META_STORE])
        : undefined;
    if (heldEvent === "request") {
      const get = IDBObjectStore.prototype.get;
      vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, key) {
        const request = get.call(this, key);
        if (!requestHold && this.transaction.mode === "readwrite") {
          requestHold = holdIndexedDBEvent(request, "success");
        }
        return request;
      });
    }
    const admission = new EntryWriteAdmission(4 * 1024 * 1024);
    const identity = { clientId: "client", hostId: "host", sessionId: "stalled", incarnation: "inc" };
    const historyKey = entryHistoryKey(identity);
    const owner = new TranscriptRepository(cache, {
      ref: "ref-stalled",
      threadId: "thread-stalled",
      historyKey,
      identity,
      clearEpoch: 0,
      lifetime: 1,
      admission,
    });
    const response: ThreadReadResponse = {
      thread: {
        id: "thread-stalled",
        sessionId: "stalled",
        preview: "",
        ephemeral: false,
        modelProvider: "test/provider",
        createdAt: 1,
        updatedAt: 1,
        status: { type: "idle" },
        cwd: "/tmp",
        cliVersion: "test",
        source: "evener",
        evener: {
          ref: "ref-stalled",
          capabilities: {
            send: true,
            steer: true,
            interrupt: true,
            compact: true,
            clear: true,
            forkFromTurn: true,
            shutdown: true,
            changeModel: true,
            changeVisionModel: true,
            queue: true,
            goal: true,
            sharedNotes: true,
            rename: true,
          },
          queue: { revision: 0 },
        },
        turns: [
          {
            id: "turn-1",
            status: "completed",
            version: 1,
            itemsView: "full",
            items: [
              {
                type: "agentMessage",
                id: "item-stalled",
                transcriptKey: "item-stalled",
                turnId: "turn-1",
                position: { entry: 1, item: 0 },
                version: 1,
                text: "stalled body",
              },
            ],
          },
        ],
      },
      bootGeneration: "1",
      epoch: 0,
      snapshot: { incarnation: "inc", length: 1 },
      overlay: [],
    };
    try {
      expect(owner.acceptRead(response, { lifetime: 1, readId: "read-stalled", boundary: "stalled" })).toBe(true);
      if (completionHold) await completionHold.reached;
      else {
        await settleRealTasks(() => requestHold !== undefined);
        expect(requestHold).toBeDefined();
        await requestHold?.reached;
      }
      expect(opened).not.toHaveBeenCalled();
      expect(admission.snapshotForTesting().backgroundInFlight).toBe(true);
      expect(admission.snapshotForTesting().retainedBytes).toBeGreaterThan(0);
      const pending = writes.mock.results[0]?.value as ReturnType<SessionEntryCache["commitBatch"]>;
      let settled = false;
      void pending.then(() => {
        settled = true;
      });
      await vi.advanceTimersByTimeAsync(9_999);
      expect(settled).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      expect(settled).toBe(true);
      await expect(pending).resolves.toEqual({
        outcome: "failed",
        error: expect.any(Error),
      });
      expect(closed).toHaveBeenCalledTimes(1);
      expect(aborted).toHaveBeenCalled();
      expect(committed).not.toHaveBeenCalled();
      expect(admission.snapshotForTesting()).toMatchObject({ backgroundInFlight: false, retainedBytes: 0 });
      expect(owner.retainedInventoryForTesting().inFlightBodies).toEqual([]);
      expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);

      // Do not release the lost event to get recovery: another session must
      // succeed using a replacement connection while the old callback is held.
      await expect(
        cache.commitBatch({ historyKey: "next-session", clearEpoch: 0, items: [row("next-session", 2)] }),
      ).resolves.toMatchObject({ outcome: "committed" });
      expect(opened).toHaveBeenCalledTimes(1);
      expect(committed).toHaveBeenCalledTimes(1);
      await expect(cache.getLatest("next-session", 10)).resolves.toMatchObject([{ transcriptKey: "row-2" }]);
      // Holding completion means the real transaction already committed. A
      // timeout must not claim it rolled back or destroy its durable rows.
      await expect(cache.getLatest(historyKey, 10)).resolves.toMatchObject(
        heldEvent === "completion" ? [{ transcriptKey: "item-stalled", item: { text: "stalled body" } }] : [],
      );
      requestHold?.release();
      completionHold?.release();
      await vi.advanceTimersByTimeAsync(0);
      expect(committed).toHaveBeenCalledTimes(1);
      await expect(pending).resolves.toMatchObject({ outcome: "failed" });
      expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    } finally {
      owner.dispose();
      requestHold?.release();
      completionHold?.release();
    }
  },
);

it("bounds failed write cleanup when the abort event never arrives", async () => {
  const { cache, factory } = newCache();
  await cache.commitBatch({ historyKey: "seed", clearEpoch: 0, items: [row("seed", 1)] });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const opened = vi.spyOn(factory, "open");
  const committed = vi.fn();
  cache.subscribeWrites(committed);
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  const transact = IDBDatabase.prototype.transaction;
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const transaction = transact.apply(this, args);
    if (!hold && transaction.mode === "readwrite") hold = holdIndexedDBEvent(transaction, "abort");
    return transaction;
  });
  vi.spyOn(IDBObjectStore.prototype, "get").mockImplementationOnce(() => {
    throw new Error("request work failed");
  });
  const pending = cache.commitBatch({ historyKey: "failed", clearEpoch: 0, items: [row("failed", 1)] });
  let settled = false;
  void pending.then(() => {
    settled = true;
  });
  try {
    await settleRealTasks(() => hold !== undefined);
    expect(hold).toBeDefined();
    await hold?.reached;
    await vi.advanceTimersByTimeAsync(10_000);
    expect(settled).toBe(true);
    await expect(pending).resolves.toEqual({
      outcome: "failed",
      error: expect.any(Error),
    });
    expect(committed).not.toHaveBeenCalled();
    await expect(
      cache.commitBatch({ historyKey: "next-session", clearEpoch: 0, items: [row("next-session", 2)] }),
    ).resolves.toMatchObject({ outcome: "committed" });
    expect(opened).toHaveBeenCalledTimes(1);
    await expect(cache.getLatest("failed", 10)).resolves.toEqual([]);
  } finally {
    hold?.release();
  }
});

it("clear removes every entry store row and advances the durable epoch together", async () => {
  const { cache, factory, name } = newCache();
  const historyKey = "host/session/clear";
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [row(historyKey, 1)],
    turns: [{ id: "turn-1", status: "completed", version: 1, items: [] }],
    coverage: {
      kind: "read",
      readId: "read-1",
      start: { entry: 1, item: 0 },
      end: { entry: 1, item: 0 },
      representedSnapshot: 1,
      boundary: "b1",
    },
  });
  expect(await countRows(factory, name, ENTRY_ITEM_STORE)).toBe(1);
  expect(await countRows(factory, name, ENTRY_META_STORE)).toBeGreaterThan(0);

  await expect(cache.clear()).resolves.toEqual({ committed: true, epoch: 1 });

  expect(await countRows(factory, name, ENTRY_ITEM_STORE)).toBe(0);
  expect(await countRows(factory, name, ENTRY_TURN_STORE)).toBe(0);
  expect(await countRows(factory, name, ENTRY_COVERAGE_STORE)).toBe(0);
  // The reserved Clear epoch row is entry-owned now, so it is the one row that
  // survives Clear; every history meta row is gone.
  expect(await countRows(factory, name, ENTRY_META_STORE)).toBe(1);
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([]);
  await expect(cache.getSessionMetadata(historyKey)).resolves.toBeUndefined();
  await expect(cache.getByteTotals(historyKey)).resolves.toEqual({ historyBytes: 0, aggregateBytes: 0 });

  await expect(cache.commitBatch({ historyKey, clearEpoch: 0, items: [row(historyKey, 2)] })).resolves.toEqual({
    outcome: "refused",
    reason: "clear-epoch",
  });
  await expect(cache.commitBatch({ historyKey, clearEpoch: 1, items: [row(historyKey, 2)] })).resolves.toMatchObject({
    outcome: "committed",
  });
  await expect(cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "row-2" }]);
});

it("deleteHistory removes one identity's rows and accounting without touching another", async () => {
  const { cache } = newCache();
  const alpha = "host/session/alpha";
  const beta = "host/session/beta";
  await cache.commitBatch({
    historyKey: alpha,
    clearEpoch: 0,
    items: [row(alpha, 1)],
    turns: [{ id: "turn-1", status: "completed", version: 1, items: [] }],
    coverage: {
      kind: "read",
      readId: "read-a",
      start: { entry: 1, item: 0 },
      end: { entry: 1, item: 0 },
      representedSnapshot: 1,
      boundary: "a1",
    },
  });
  await cache.commitBatch({ historyKey: beta, clearEpoch: 0, items: [row(beta, 5)] });
  const alphaTotals = await cache.getByteTotals(alpha);
  const betaTotals = await cache.getByteTotals(beta);

  await expect(cache.deleteHistory(alpha)).resolves.toBe(true);

  await expect(cache.getLatest(alpha, 10)).resolves.toEqual([]);
  await expect(cache.getCoverage(alpha, "read:read-a")).resolves.toBeUndefined();
  await expect(cache.getTurn(alpha, "turn-1")).resolves.toBeUndefined();
  const aggregateAfter = betaTotals.aggregateBytes - alphaTotals.historyBytes;
  await expect(cache.getByteTotals(alpha)).resolves.toEqual({ historyBytes: 0, aggregateBytes: aggregateAfter });
  await expect(cache.getLatest(beta, 10)).resolves.toMatchObject([{ transcriptKey: "row-5" }]);
  await expect(cache.getByteTotals(beta)).resolves.toEqual({
    historyBytes: betaTotals.historyBytes,
    aggregateBytes: aggregateAfter,
  });
});

it("returns false and keeps rows when a delete transaction aborts", async () => {
  const { cache } = newCache();
  const historyKey = "host/session/delete-abort";
  await cache.commitBatch({ historyKey, clearEpoch: 0, items: [row(historyKey, 1)] });

  const transact = IDBDatabase.prototype.transaction;
  let deleteTransaction: IDBTransaction | undefined;
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const transaction = transact.apply(this, args);
    if (transaction.mode === "readwrite" && transaction.objectStoreNames.contains(ENTRY_ITEM_STORE)) {
      deleteTransaction = transaction;
    }
    return transaction;
  });

  const pending = cache.deleteHistory(historyKey);
  await settleRealTasks(() => deleteTransaction !== undefined);
  expect(deleteTransaction).toBeDefined();
  deleteTransaction?.abort();
  await expect(pending).resolves.toBe(false);
  vi.restoreAllMocks();

  await expect(cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "row-1" }]);
});

it("refuses pre-delete work and admits an explicit fresh-generation write", async () => {
  const { cache } = newCache();
  const historyKey = "host/session/delete-resurrection";
  await cache.commitBatch({ historyKey, clearEpoch: 0, items: [row(historyKey, 1), row(historyKey, 2)] });
  await cache.deleteHistory(historyKey);
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([]);
  const generation = await cache.getHistoryGeneration(historyKey);
  expect(generation).toBe(1);

  // Approved contract: a captured pre-delete batch (generation 0) is refused,
  // not silently re-admitted against an empty history.
  await expect(cache.commitBatch({ historyKey, clearEpoch: 0, items: [row(historyKey, 3)] })).resolves.toEqual({
    outcome: "refused",
    reason: "history-fence",
  });
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([]);

  await expect(
    cache.commitBatch({ historyKey, clearEpoch: 0, historyGeneration: generation, items: [row(historyKey, 3)] }),
  ).resolves.toMatchObject({ outcome: "committed" });
  await expect(cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "row-3" }]);
});

it("keeps a delete fence through repeat deletes, TTL expiry and unrelated eviction", async () => {
  const name = `entry-lifecycle-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    maxBytes: 1,
    ttlMs: 100,
    now: () => 1_000,
  });
  caches.push(cache);
  const deleted = "host/session/fenced";
  const other = "host/session/other";
  await cache.commitBatch({ historyKey: deleted, clearEpoch: 0, items: [row(deleted, 1)] });
  await cache.deleteHistory(deleted);
  await cache.deleteHistory(deleted);
  expect(await cache.getHistoryGeneration(deleted)).toBe(2);

  // A small-cap, short-TTL write elsewhere runs TTL and eviction; the fence row
  // has neither bytes nor timestamps, so it survives and still refuses old work.
  await cache.commitBatch({ historyKey: other, clearEpoch: 0, items: [row(other, 9)] });
  expect(await cache.getHistoryGeneration(deleted)).toBe(2);
  await expect(
    cache.commitBatch({ historyKey: deleted, clearEpoch: 0, historyGeneration: 0, items: [row(deleted, 2)] }),
  ).resolves.toEqual({ outcome: "refused", reason: "history-fence" });
  await expect(
    cache.commitBatch({ historyKey: deleted, clearEpoch: 0, historyGeneration: 2, items: [row(deleted, 2)] }),
  ).resolves.toEqual({ outcome: "refused", reason: "capacity" });
  await expect(cache.getLatest(deleted, 10)).resolves.toEqual([]);
});

it("preserves the generation fence through a fresh write, capacity eviction and TTL", async () => {
  const name = `entry-lifecycle-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const identityFor = (sessionId: string): TranscriptResolvedIdentity => ({
    clientId: "c",
    hostId: "h",
    sessionId,
    incarnation: "i",
  });
  const metadataFor = (sessionId: string, savedAt: number): TranscriptSessionMetadata => {
    const resolvedIdentity = identityFor(sessionId);
    return {
      historyKey: entryHistoryKey(resolvedIdentity),
      requestedRefs: [{ clientId: "c", ref: `local:${sessionId}` }],
      resolvedIdentity,
      savedAt,
      accessedAt: savedAt,
      formatVersion: 3,
    };
  };
  const fenced = entryHistoryKey(identityFor("fenced"));
  const other = entryHistoryKey(identityFor("other"));
  const third = entryHistoryKey(identityFor("third"));
  let now = 1_000;

  const measure = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    ttlMs: Number.MAX_SAFE_INTEGER,
    now: () => 1_000,
  });
  caches.push(measure);
  await measure.commitBatch({
    historyKey: fenced,
    clearEpoch: 0,
    items: [row(fenced, 1)],
    sessionMetadata: metadataFor("fenced", 1_000),
  });
  const fencedBytes = (await measure.getByteTotals(fenced)).historyBytes;
  await measure.commitBatch({
    historyKey: other,
    clearEpoch: 0,
    items: [row(other, 1)],
    sessionMetadata: metadataFor("other", 1_000),
  });
  const otherBytes = (await measure.getByteTotals(other)).historyBytes;

  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    maxBytes: Math.max(fencedBytes, otherBytes) + 1,
    ttlMs: 100,
    now: () => now,
  });
  caches.push(cache);
  await cache.commitBatch({
    historyKey: fenced,
    clearEpoch: 0,
    items: [row(fenced, 1)],
    sessionMetadata: metadataFor("fenced", 1_000),
  });
  await cache.deleteHistory(fenced);
  expect(await cache.getHistoryGeneration(fenced)).toBe(1);

  // A successful explicit fresh-generation write after the delete.
  await expect(
    cache.commitBatch({
      historyKey: fenced,
      clearEpoch: 0,
      historyGeneration: 1,
      items: [row(fenced, 2)],
      sessionMetadata: metadataFor("fenced", 1_000),
    }),
  ).resolves.toMatchObject({ outcome: "committed" });

  // Capacity pressure evicts the fenced identity's live rows; its generation
  // fence must survive and still refuse captured old work.
  await expect(
    cache.commitBatch({
      historyKey: other,
      clearEpoch: 0,
      items: [row(other, 1)],
      sessionMetadata: metadataFor("other", 1_000),
    }),
  ).resolves.toMatchObject({ outcome: "committed" });
  expect(await cache.getHistoryGeneration(fenced)).toBe(1);
  await expect(
    cache.commitBatch({ historyKey: fenced, clearEpoch: 0, historyGeneration: 0, items: [row(fenced, 3)] }),
  ).resolves.toEqual({ outcome: "refused", reason: "history-fence" });

  // A later TTL pass reclaims expired live rows; the fence still survives and a
  // fresh generation can still cache, so it is neither erased nor stranded.
  now = 2_000;
  await cache.commitBatch({
    historyKey: third,
    clearEpoch: 0,
    items: [row(third, 1)],
    sessionMetadata: metadataFor("third", 2_000),
  });
  expect(await cache.getHistoryGeneration(fenced)).toBe(1);
  await expect(
    cache.commitBatch({
      historyKey: fenced,
      clearEpoch: 0,
      historyGeneration: 1,
      items: [row(fenced, 3)],
      sessionMetadata: metadataFor("fenced", 2_000),
    }),
  ).resolves.toMatchObject({ outcome: "committed" });
  await expect(cache.getLatest(fenced, 10)).resolves.toMatchObject([{ transcriptKey: "row-3" }]);
});
