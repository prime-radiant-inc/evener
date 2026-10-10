import {
  type CachedSessionRecord,
  currentTranscriptState,
  hydrateThread,
  type ThreadItem,
  type ThreadReadResponse,
} from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { IDBDatabase, IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { type CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { entryHistoryKey, type TranscriptResolvedIdentity } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { legacySessionToEntryRows, migrateLegacySession } from "./sessionEntryMigration";
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

async function readRaw(factory: IDBFactory, name: string, store: string, key: IDBValidKey): Promise<unknown> {
  const database = await openDatabase(factory, name);
  const transaction = database.transaction(store, "readonly");
  const done = transactionDone(transaction);
  const row = await requestValue(transaction.objectStore(store).get(key));
  await done;
  return row;
}

function jsonBytes(value: unknown): number {
  const json = JSON.stringify(value);
  if (json === undefined) throw new TypeError("expected JSON-encodable value");
  return new TextEncoder().encode(json).byteLength;
}

afterEach(async () => {
  vi.restoreAllMocks();
  for (const database of openedDatabases.splice(0)) database.close();
  for (const cache of caches.splice(0)) cache.close();
  for (const { factory, name } of databases.splice(0)) await deleteDatabase(factory, name);
});

async function setup(extra: { maxBytes?: number; ttlMs?: number } = {}) {
  const name = `entry-migrate-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    now: () => 1_000,
    ...extra,
  });
  caches.push(cache);
  // Open the fixture database first so the v3 upgrade creates the entry stores;
  // the legacy adapter then shares the same stores at the same version.
  await cache.getByteTotals("warmup");
  const legacy = new SessionCacheIndexedDB({ indexedDB: factory, databaseName: name, databaseVersion: 3 });
  caches.push(legacy);
  return { cache, legacy, factory, name };
}

function legacyRecord(ref = "local:thr_1"): CachedSessionRecord {
  return {
    ref,
    threadId: "thr_1",
    name: "n",
    modelProvider: "p",
    model: "m",
    savedAt: 1_000,
    history: {
      bootGeneration: "bg",
      epoch: 1,
      incarnation: "inc-1",
      length: 2,
      appliedGeneration: 1,
      issuedGeneration: 1,
      turns: [
        {
          id: "turn_1",
          status: "completed",
          version: 2,
          items: [
            {
              id: "a1",
              turnId: "turn_1",
              type: "agentMessage",
              text: "alpha",
              transcriptKey: "e1",
              position: { entry: 1, item: 0 },
              version: 1,
            },
            {
              id: "a2",
              turnId: "turn_1",
              type: "agentMessage",
              text: "",
              textOmitted: true,
              transcriptKey: "e2",
              position: { entry: 2, item: 0 },
              version: 2,
            },
          ],
        },
      ],
    },
  };
}

const identity: TranscriptResolvedIdentity = {
  clientId: "c",
  hostId: "h",
  sessionId: "s",
  incarnation: "inc-1",
};
const historyKey = entryHistoryKey(identity);

it("converts one legacy records blob into stale seeds and removes its source atomically", async () => {
  const { cache, legacy, factory, name } = await setup();
  const seeded = legacyRecord();
  await legacy.put(seeded, 0, 1_000);
  const metaBefore = await readRaw(factory, name, "meta", seeded.ref);

  const outcome = await migrateLegacySession(cache, {
    identity,
    requestedRefs: [{ clientId: "c", ref: seeded.ref }],
    savedAt: 1_000,
    source: { kind: "records", ref: seeded.ref },
    clearEpoch: 0,
  });
  expect(outcome).toMatchObject({ outcome: "converted", itemCount: 2, turnCount: 1 });
  if (outcome.outcome !== "converted") return;
  expect(outcome.removedBytes).toBe(jsonBytes(seeded) + jsonBytes(metaBefore));

  const rows = await cache.getLatest(historyKey, 10);
  expect(rows.map((row) => row.transcriptKey)).toEqual(["e1", "e2"]);
  await expect(cache.getTurn(historyKey, "turn_1")).resolves.toMatchObject({
    turnId: "turn_1",
    version: 2,
    metadata: { status: "completed" },
  });
  // Stale seeds carry no certified range: no coverage row, no complete window.
  await expect(cache.getLatestWindow(historyKey, 10)).resolves.toMatchObject({ complete: false, stale: true });
  await expect(cache.getCoverage(historyKey, "read:anything")).resolves.toBeUndefined();

  await expect(readRaw(factory, name, "records", seeded.ref)).resolves.toBeUndefined();
  await expect(readRaw(factory, name, "meta", seeded.ref)).resolves.toBeUndefined();
});

it("leaves the legacy source intact when the conversion transaction aborts", async () => {
  const { cache, legacy, factory, name } = await setup();
  const seeded = legacyRecord();
  await legacy.put(seeded, 0, 1_000);

  const transact = IDBDatabase.prototype.transaction;
  let migrationTransaction: IDBTransaction | undefined;
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const transaction = transact.apply(this, args);
    if (transaction.mode === "readwrite" && transaction.objectStoreNames.contains("records")) {
      migrationTransaction = transaction;
    }
    return transaction;
  });

  const pending = migrateLegacySession(cache, {
    identity,
    requestedRefs: [{ clientId: "c", ref: seeded.ref }],
    savedAt: 1_000,
    source: { kind: "records", ref: seeded.ref },
    clearEpoch: 0,
  });
  await Promise.resolve();
  await settleRealTasks(() => migrationTransaction !== undefined);
  expect(migrationTransaction).toBeDefined();
  migrationTransaction?.abort();
  await expect(pending).resolves.toMatchObject({ outcome: "failed" });
  vi.restoreAllMocks();

  await expect(readRaw(factory, name, "records", seeded.ref)).resolves.toMatchObject({ ref: seeded.ref });
  await expect(readRaw(factory, name, "meta", seeded.ref)).resolves.toBeDefined();
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([]);
});

it("skips an undecodable legacy record and leaves its source for retry", async () => {
  const { cache, legacy, factory, name } = await setup();
  // Seed a structurally invalid record directly: no history, so conversion
  // cannot produce rows, and the source must survive for a later attempt.
  await legacy.put(legacyRecord(), 0, 1_000);
  const database = await openDatabase(factory, name);
  await new Promise<void>((resolve, reject) => {
    const tx = database.transaction(["records", "meta"], "readwrite");
    tx.objectStore("records").put({ ref: "local:bad" });
    tx.objectStore("meta").put({ ref: "local:bad", bytes: 10, savedAt: 1_000 });
    tx.addEventListener("complete", () => resolve(), { once: true });
    tx.addEventListener("error", () => reject(tx.error), { once: true });
  });

  await expect(
    migrateLegacySession(cache, {
      identity: { ...identity, sessionId: "bad" },
      requestedRefs: [{ clientId: "c", ref: "local:bad" }],
      savedAt: 1_000,
      source: { kind: "records", ref: "local:bad" },
      clearEpoch: 0,
    }),
  ).resolves.toEqual({ outcome: "skipped", reason: "undecodable" });
  await expect(readRaw(factory, name, "records", "local:bad")).resolves.toEqual({ ref: "local:bad" });
});

it("refuses a migration captured before the durable Clear epoch", async () => {
  const { cache, legacy, factory, name } = await setup();
  const seeded = legacyRecord();
  await legacy.put(seeded, 0, 1_000);
  await cache.clear();

  await expect(
    migrateLegacySession(cache, {
      identity,
      requestedRefs: [{ clientId: "c", ref: seeded.ref }],
      savedAt: 1_000,
      source: { kind: "records", ref: seeded.ref },
      clearEpoch: 0,
    }),
  ).resolves.toEqual({ outcome: "refused", reason: "clear-epoch" });
  // Approved all-history Clear contract: Clear removed the legacy source too,
  // so there is nothing for a later epoch to re-import.
  await expect(readRaw(factory, name, "records", seeded.ref)).resolves.toBeUndefined();
  await expect(readRaw(factory, name, "meta", seeded.ref)).resolves.toBeUndefined();
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([]);
});

it("does not let an older legacy seed regress a newer committed entry row", async () => {
  const { cache, legacy } = await setup();
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [
      {
        historyKey,
        transcriptKey: "e1",
        position: { entry: 1, item: 0 },
        version: 5,
        item: { id: "a1", turnId: "turn_1", type: "agentMessage", text: "newer" },
      },
    ],
  });
  await legacy.put(legacyRecord(), 0, 1_000);

  await expect(
    migrateLegacySession(cache, {
      identity,
      requestedRefs: [{ clientId: "c", ref: "local:thr_1" }],
      savedAt: 1_000,
      source: { kind: "records", ref: "local:thr_1" },
      clearEpoch: 0,
    }),
  ).resolves.toMatchObject({ outcome: "converted" });

  const rows = await cache.getLatest(historyKey, 10);
  expect(rows.find((row) => row.transcriptKey === "e1")).toMatchObject({
    version: 5,
    item: { text: "newer" },
  });
  expect(rows.map((row) => row.transcriptKey)).toEqual(["e1", "e2"]);
});

it("converts a real current checkpoint recorded history and removes its source", async () => {
  const { cache, legacy, factory, name } = await setup();
  const item: ThreadItem = {
    id: "a1",
    turnId: "turn_1",
    type: "agentMessage",
    text: "alpha",
    transcriptKey: "e1",
    position: { entry: 1, item: 0 },
    version: 1,
  };
  const base = currentRead("local:cur");
  const read: ThreadReadResponse = {
    ...base,
    thread: { ...base.thread, turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [item] }] },
  };
  const model = hydrateThread(read, "local:cur", 10);
  const state = currentTranscriptState(model);
  expect(state).toBeDefined();
  if (!state) return;
  const record: CurrentSessionRecord = {
    ownerToken: "tab",
    ref: "local:cur",
    connectionGeneration: 1,
    ownershipGeneration: 1,
    clearEpoch: 0,
    appliedSequence: 1,
    savedAt: 10,
    state,
  };
  await legacy.putCurrent(record, 10);

  const currentIdentity: TranscriptResolvedIdentity = {
    ...identity,
    incarnation: state.history.incarnation ?? "inc-1",
  };
  await expect(
    migrateLegacySession(cache, {
      identity: currentIdentity,
      requestedRefs: [{ clientId: "c", ref: "local:cur" }],
      savedAt: 10,
      source: { kind: "current", key: ["tab", "local:cur"] },
      clearEpoch: 0,
    }),
  ).resolves.toMatchObject({ outcome: "converted", itemCount: 1, turnCount: 1 });

  const entryHistoryKeyValue = entryHistoryKey(currentIdentity);
  await expect(cache.getLatest(entryHistoryKeyValue, 10)).resolves.toMatchObject([
    { transcriptKey: "e1", item: { text: "alpha" } },
  ]);
  await expect(readRaw(factory, name, "current", ["tab", "local:cur"])).resolves.toBeUndefined();
  await expect(readRaw(factory, name, "currentMeta", ["tab", "local:cur"])).resolves.toBeUndefined();
});

it("drops items without a key, position or version and never guesses one", () => {
  const converted = legacySessionToEntryRows(
    {
      history: {
        turns: [
          {
            id: "turn_1",
            status: "completed",
            version: 1,
            items: [
              { id: "a1", turnId: "turn_1", type: "agentMessage", text: "alpha" },
              {
                id: "a2",
                turnId: "turn_1",
                type: "agentMessage",
                text: "beta",
                transcriptKey: "e2",
                position: { entry: 2, item: 0 },
                version: 2,
              },
            ],
          },
        ],
      },
    },
    "identity",
  );
  expect(converted?.items.map((row) => row.transcriptKey)).toEqual(["e2"]);
  expect(legacySessionToEntryRows({ ref: "x" }, "identity")).toBeUndefined();
});

it("does not regress newer metadata or delete the newer target when migrating an older source", async () => {
  // now=1000, ttl=100: the stale source timestamp 1 would expire, the newer
  // 1000 would not. The merge must keep the newer retention state.
  const { cache, legacy } = await setup({ ttlMs: 100 });
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [
      {
        historyKey,
        transcriptKey: "e1",
        position: { entry: 1, item: 0 },
        version: 5,
        item: { id: "a1", turnId: "turn_1", type: "agentMessage", text: "newer" },
      },
    ],
    coverage: {
      kind: "read",
      readId: "read-new",
      start: { entry: 1, item: 0 },
      end: { entry: 1, item: 0 },
      representedSnapshot: 1,
      boundary: "read-new",
    },
    sessionMetadata: {
      historyKey,
      requestedRefs: [{ clientId: "c", ref: "alias://newer" }],
      resolvedIdentity: identity,
      savedAt: 1_000,
      accessedAt: 1_000,
      formatVersion: 3,
    },
  });
  await legacy.put(legacyRecord(), 0, 1);

  await expect(
    migrateLegacySession(cache, {
      identity,
      requestedRefs: [{ clientId: "c", ref: "local:thr_1" }],
      savedAt: 1,
      source: { kind: "records", ref: "local:thr_1" },
      clearEpoch: 0,
    }),
  ).resolves.toMatchObject({ outcome: "converted" });

  const rows = await cache.getLatest(historyKey, 10);
  expect(rows.find((row) => row.transcriptKey === "e1")).toMatchObject({ version: 5, item: { text: "newer" } });
  await expect(cache.getSessionMetadata(historyKey)).resolves.toMatchObject({ savedAt: 1_000, accessedAt: 1_000 });
  await expect(cache.getCoverage(historyKey, "read:read-new")).resolves.toMatchObject({ boundary: "read-new" });
  const metadataAfter = await cache.getSessionMetadata(historyKey);
  expect(metadataAfter?.requestedRefs).toEqual(
    expect.arrayContaining([
      { clientId: "c", ref: "alias://newer" },
      { clientId: "c", ref: "local:thr_1" },
    ]),
  );
});

it("refuses a migration captured before a delete, keeps the source, and admits a fresh generation", async () => {
  const { cache, legacy, factory, name } = await setup();
  const seeded = legacyRecord();
  await legacy.put(seeded, 0, 1_000);
  await cache.deleteHistory(historyKey);
  expect(await cache.getHistoryGeneration(historyKey)).toBe(1);

  await expect(
    migrateLegacySession(cache, {
      identity,
      requestedRefs: [{ clientId: "c", ref: seeded.ref }],
      savedAt: 1_000,
      source: { kind: "records", ref: seeded.ref },
      clearEpoch: 0,
    }),
  ).resolves.toEqual({ outcome: "refused", reason: "history-fence" });
  // The refused migration must not remove the source it would have converted.
  await expect(readRaw(factory, name, "records", seeded.ref)).resolves.toMatchObject({ ref: seeded.ref });
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([]);

  await expect(
    migrateLegacySession(cache, {
      identity,
      requestedRefs: [{ clientId: "c", ref: seeded.ref }],
      savedAt: 1_000,
      source: { kind: "records", ref: seeded.ref },
      clearEpoch: 0,
      historyGeneration: 1,
    }),
  ).resolves.toMatchObject({ outcome: "converted" });
  const rowsAfter = await cache.getLatest(historyKey, 10);
  expect(rowsAfter.map((row) => row.transcriptKey)).toEqual(["e1", "e2"]);
});

it("aborting Clear preserves every history store, the fence and the epoch", async () => {
  const { cache, legacy, factory, name } = await setup();
  await legacy.put(legacyRecord(), 0, 1_000);
  const item: ThreadItem = {
    id: "a1",
    turnId: "turn_1",
    type: "agentMessage",
    text: "cur",
    transcriptKey: "e1",
    position: { entry: 1, item: 0 },
    version: 1,
  };
  const base = currentRead("local:cur");
  const read: ThreadReadResponse = {
    ...base,
    thread: { ...base.thread, turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [item] }] },
  };
  const state = currentTranscriptState(hydrateThread(read, "local:cur", 10));
  if (!state) throw new Error("fixture state did not build");
  const current: CurrentSessionRecord = {
    ownerToken: "tab",
    ref: "local:cur",
    connectionGeneration: 1,
    ownershipGeneration: 1,
    clearEpoch: 0,
    appliedSequence: 1,
    savedAt: 10,
    state,
  };
  await legacy.putCurrent(current, 10);
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [
      {
        historyKey,
        transcriptKey: "entry",
        position: { entry: 1, item: 0 },
        version: 1,
        item: { id: "entry", turnId: "turn_1", type: "agentMessage", text: "entry" },
      },
    ],
  });

  const transact = IDBDatabase.prototype.transaction;
  let clearTransaction: IDBTransaction | undefined;
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const transaction = transact.apply(this, args);
    if (
      transaction.mode === "readwrite" &&
      transaction.objectStoreNames.contains("records") &&
      transaction.objectStoreNames.contains("current")
    ) {
      clearTransaction = transaction;
    }
    return transaction;
  });
  const pending = cache.clear();
  await settleRealTasks(() => clearTransaction !== undefined);
  expect(clearTransaction).toBeDefined();
  clearTransaction?.abort();
  await expect(pending).resolves.toEqual({ committed: false, epoch: 0 });
  vi.restoreAllMocks();

  await expect(readRaw(factory, name, "records", "local:thr_1")).resolves.toBeDefined();
  await expect(readRaw(factory, name, "current", ["tab", "local:cur"])).resolves.toBeDefined();
  await expect(cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "entry" }]);
  expect(await cache.getHistoryGeneration(historyKey)).toBe(0);
});
