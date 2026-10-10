import { IDBDatabase, IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { ENTRY_COVERAGE_STORE, ENTRY_ITEM_STORE, ENTRY_META_STORE, ENTRY_TURN_STORE } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { settleRealTasks } from "./testing/stalledIndexedDB";

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
  expect(await countRows(factory, name, ENTRY_META_STORE)).toBe(0);
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
