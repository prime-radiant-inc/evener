import {
  decodeTranscriptItem,
  encodeTranscriptItem,
  markItemIdentityOnly,
  markItemTextOmitted,
} from "@evener/appwire-client";
import { IDBDatabase, IDBFactory, IDBIndex, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import {
  CURRENT_META_STORE,
  CURRENT_STORE,
  ENTRY_COVERAGE_STORE,
  ENTRY_ITEM_STORE,
  ENTRY_META_STORE,
  ENTRY_TURN_STORE,
  entryHistoryKey,
  LEGACY_META_STORE,
  LEGACY_RECORDS_STORE,
  type TranscriptHistoryMetaRow,
  type TranscriptSessionMetadata,
} from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";

const caches: Array<{ close(): void }> = [];
const databases: Array<{ factory: IDBFactory; name: string }> = [];
const openedDatabases: IDBDatabase[] = [];

function openFixtureDatabase(factory: IDBFactory, name: string): Promise<IDBDatabase> {
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

function deleteFixtureDatabase(factory: IDBFactory, name: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = factory.deleteDatabase(name);
    request.addEventListener("success", () => resolve(), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
    request.addEventListener("blocked", () => reject(new Error(`${name} database deletion blocked`)), {
      once: true,
    });
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

function independentJsonBytes(value: unknown): number {
  const json = JSON.stringify(value);
  if (json === undefined) throw new TypeError("expected a JSON-encoded row");
  return new TextEncoder().encode(json).byteLength;
}

function persistedSessionMetadataPayload(value: unknown): Omit<TranscriptHistoryMetaRow, "encodedBytes" | "itemCount"> {
  if (!value || typeof value !== "object") throw new TypeError("expected persisted session metadata");
  const { encodedBytes: _encodedBytes, itemCount: _itemCount, ...payload } = value as TranscriptHistoryMetaRow;
  return payload;
}

function holdNextReadonlyTransactionContaining(storeName: string) {
  const transact = IDBDatabase.prototype.transaction;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let heldTransaction: IDBTransaction | undefined;
  const reached = new Promise<void>((resolve) => {
    vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
      this: IDBDatabase,
      ...args: Parameters<IDBDatabase["transaction"]>
    ) {
      const transaction = transact.apply(this, args);
      if (!hold && transaction.mode === "readonly" && transaction.objectStoreNames.contains(storeName)) {
        heldTransaction = transaction;
        hold = holdIndexedDBEvent(transaction, "complete");
        void hold.reached.then(resolve);
      }
      return transaction;
    });
  });
  return {
    reached,
    transaction: () => heldTransaction,
    release() {
      hold?.release();
      vi.restoreAllMocks();
    },
  };
}

async function readEntryMeta(factory: IDBFactory, name: string, key: string): Promise<unknown> {
  const database = await openFixtureDatabase(factory, name);
  const transaction = database.transaction(ENTRY_META_STORE, "readonly");
  const done = transactionDone(transaction);
  const row = await requestValue(transaction.objectStore(ENTRY_META_STORE).get(key));
  await done;
  return row;
}

afterEach(async () => {
  vi.restoreAllMocks();
  for (const database of openedDatabases.splice(0)) database.close();
  for (const cache of caches.splice(0)) cache.close();
  for (const { factory, name } of databases.splice(0)) await deleteFixtureDatabase(factory, name);
});

it("waits for the real entry fixture database deletion to settle", async () => {
  const fixture = createEntryCacheFixture();
  caches.push(fixture.cache);
  databases.push({ factory: fixture.indexedDB, name: fixture.name });
  const deleteDatabase = fixture.indexedDB.deleteDatabase.bind(fixture.indexedDB);
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(fixture.indexedDB, "deleteDatabase").mockImplementation((name) => {
    const request = deleteDatabase(name);
    if (name === fixture.name) {
      hold = holdIndexedDBEvent(request, "success");
      request.addEventListener("success", () => {}, { once: true });
    }
    return request;
  });

  try {
    let settled = false;
    const disposed = fixture.dispose();
    expect(disposed).toBeInstanceOf(Promise);
    if (!(disposed instanceof Promise)) return;
    const observed = disposed.then(() => {
      settled = true;
    });
    await hold?.reached;
    expect(settled).toBe(false);
    hold?.release();
    await observed;
    expect(settled).toBe(true);
  } finally {
    hold?.release();
  }
});

it("writes and reads one entry by history identity and transcript key", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const row = {
    historyKey: "host/session/incarnation",
    transcriptKey: "entry-1/item-0",
    position: { entry: 1, item: 0 },
    version: 1,
    item: { id: "item-0", turnId: "turn-1", type: "message", text: "hello" },
  };

  await cache.putItems([row]);
  await expect(cache.getLatest(row.historyKey, 40)).resolves.toEqual([row]);
  const request = factory.open(name);
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
  expect(Array.from(database.objectStoreNames)).toEqual(
    [
      CURRENT_META_STORE,
      CURRENT_STORE,
      ENTRY_COVERAGE_STORE,
      ENTRY_ITEM_STORE,
      ENTRY_META_STORE,
      ENTRY_TURN_STORE,
      LEGACY_META_STORE,
      LEGACY_RECORDS_STORE,
    ].sort(),
  );
  database.close();
});

it("refuses to enable entry stores on the production cache database name", () => {
  expect(() => new SessionEntryCache({ databaseName: "evener-session-cache", indexedDB: new IDBFactory() })).toThrow(
    "entry storage requires an isolated fixture database",
  );
});

it("opens only the latest bounded rows for the requested history identity", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const rows = Array.from({ length: 100 }, (_, entry) => ({
    historyKey: "host/session/incarnation",
    transcriptKey: `entry-${entry}`,
    position: { entry, item: 0 },
    version: 1,
    item: { id: `item-${entry}`, turnId: "turn-1", type: "assistant", text: `item-${entry}` },
  }));
  await cache.putItems(rows);
  await cache.putItems([{ ...rows[99]!, historyKey: "other-host/session/incarnation" }]);

  const openCursor = IDBIndex.prototype.openCursor;
  let visited = 0;
  vi.spyOn(IDBIndex.prototype, "openCursor").mockImplementation(function (this: IDBIndex, ...args) {
    const request = openCursor.apply(this, args);
    request.addEventListener("success", () => {
      if (request.result) visited += 1;
    });
    return request;
  });
  await expect(cache.getLatest("host/session/incarnation", 3)).resolves.toEqual(rows.slice(97));
  expect(visited).toBe(3);
  vi.restoreAllMocks();
});

it("orders bounded windows by entry, item, and sub position", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/incarnation";
  const rows = [
    {
      historyKey,
      transcriptKey: "z-sub-0",
      position: { entry: 1, item: 0, sub: 0 },
      version: 1,
      item: { id: "sub-0", turnId: "turn-1", type: "message", text: "zero" },
    },
    {
      historyKey,
      transcriptKey: "a-sub-2",
      position: { entry: 1, item: 0, sub: 2 },
      version: 1,
      item: { id: "sub-2", turnId: "turn-1", type: "message", text: "two" },
    },
    {
      historyKey,
      transcriptKey: "item-1",
      position: { entry: 1, item: 1 },
      version: 1,
      item: { id: "item-1", turnId: "turn-1", type: "message", text: "one" },
    },
  ];

  await cache.putItems(rows);

  await expect(cache.getLatest(historyKey, 2)).resolves.toEqual([rows[1], rows[2]]);
});

it("does not let an older item version replace a newer committed entry", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/incarnation";
  const row = {
    historyKey,
    transcriptKey: "entry-1/item-0",
    position: { entry: 1, item: 0 },
    version: 2,
    item: { id: "item-0", turnId: "turn-1", type: "message", text: "newer" },
  };
  await cache.putItems([row]);
  await cache.putItems([{ ...row, version: 1, item: { ...row.item, text: "older" } }]);
  await expect(cache.getLatest(historyKey, 40)).resolves.toEqual([row]);
});

it("keeps richer equal-version item fields and accepts a genuinely newer explicit clear", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/equal-version";
  const held = {
    historyKey,
    transcriptKey: "entry-4/item-0",
    position: { entry: 4, item: 0 },
    version: 4,
    item: {
      id: "item-0",
      turnId: "turn-1",
      type: "assistant",
      text: "known text",
      images: [{ src: "data:image/png;base64,AA==", name: "held.png" }],
      output: "known output",
    },
  };
  await cache.putItems([held]);

  const equalVersion = {
    ...held,
    item: { ...held.item, text: "", images: [], output: "" },
  };
  await cache.putItems([equalVersion]);
  expect.soft(await cache.getLatest(historyKey, 10)).toEqual([held]);

  const newerClear = {
    ...held,
    version: 5,
    item: { ...held.item, text: "", output: "" },
  };
  await cache.putItems([newerClear]);
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([newerClear]);
});

it("round-trips omitted and explicit-empty text, identity-only rows, and rich recorded payloads", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/incarnation";
  const omitted = markItemTextOmitted({ id: "omitted", turnId: "turn-1", type: "assistant", text: "" });
  const empty = { id: "empty", turnId: "turn-1", type: "assistant", text: "" };
  const identityOnly = markItemIdentityOnly({ id: "identity", turnId: "turn-1", type: "toolCall", text: "" });
  const rich = {
    id: "rich",
    turnId: "turn-1",
    type: "toolResult",
    text: "recorded result",
    output: "structured result",
    error: "denied",
    images: [{ src: "data:image/png;base64,AA==", name: "input.png", path: "/tmp/input.png" }],
    reasoningSummaries: [["reasoning chunk"]],
  };
  const items = [omitted, empty, identityOnly, rich];
  const rows = items.map((item, index) => ({
    historyKey,
    transcriptKey: item.id,
    position: { entry: index + 1, item: 0 },
    version: index + 1,
    item,
  }));
  await cache.putItems(rows);

  const stored = await cache.getLatest(historyKey, 10);
  const decoded = stored.map((row) => decodeTranscriptItem(row.item));
  expect(decoded[0]).toMatchObject({ text: "" });
  expect(encodeTranscriptItem(decoded[0]!).textOmitted).toBe(true);
  expect(decoded[1]).toMatchObject({ text: "" });
  expect(encodeTranscriptItem(decoded[1]!).textOmitted).toBeUndefined();
  expect(encodeTranscriptItem(decoded[2]!).identityOnly).toBe(true);
  expect(decoded[3]).toMatchObject(rich);
});

it("commits keyed items, item-free turns, coverage, and encoded totals as one batch", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const identity = { clientId: "client", hostId: "host", sessionId: "same-id", incarnation: "boot-a" };
  const historyKey = entryHistoryKey(identity);
  const row = {
    historyKey,
    transcriptKey: "tool-call-1",
    position: { entry: 12, item: 0 },
    version: 12,
    item: { id: "tool-call-1", turnId: "turn-2", type: "toolCall", text: "call", output: "result" },
  };

  await expect(
    cache.commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [row],
      turns: [{ id: "turn-2", status: "completed", items: [{ ...row.item, version: 12 }], version: 12 }],
      coverage: {
        kind: "read",
        readId: "read-a",
        start: { entry: 10, item: 0 },
        end: { entry: 12, item: 0 },
        representedSnapshot: 12,
        serverCursor: "opaque-cursor-from-server",
        boundary: "read-12",
      },
    }),
  ).resolves.toMatchObject({ outcome: "committed", coverage: "extended" });

  await expect(cache.getLatest(historyKey, 40)).resolves.toEqual([row]);
  await expect(cache.getTurn(historyKey, "turn-2")).resolves.toEqual({
    historyKey,
    turnId: "turn-2",
    version: 12,
    metadata: { status: "completed", version: 12 },
  });
  await expect(cache.getCoverage(historyKey, "read:read-a")).resolves.toEqual({
    historyKey,
    rangeId: "read:read-a",
    kind: "read",
    start: { entry: 10, item: 0 },
    end: { entry: 12, item: 0 },
    representedSnapshot: 12,
    serverCursor: "opaque-cursor-from-server",
    readId: "read-a",
    boundary: "read-12",
  });
  await expect(cache.getLatestWindow(historyKey, 40, "read-12")).resolves.toMatchObject({
    rows: [row],
    complete: true,
    stale: false,
    boundary: "read-12",
    serverCursor: "opaque-cursor-from-server",
  });
  const totals = await cache.getByteTotals(historyKey);
  expect(totals.historyBytes).toBeGreaterThan(0);
  expect(totals.aggregateBytes).toBe(totals.historyBytes);
  expect(entryHistoryKey({ ...identity, hostId: "other-host" })).not.toBe(historyKey);
  expect(entryHistoryKey({ ...identity, incarnation: "boot-b" })).not.toBe(historyKey);
});

it("extends only an uninterrupted accepted suffix and keeps older read coverage", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/incarnation";
  const baseline = {
    historyKey,
    clearEpoch: 0,
    items: [],
    coverage: {
      kind: "read" as const,
      readId: "read-1",
      start: { entry: 0, item: 0 },
      end: { entry: 4, item: 0 },
      representedSnapshot: 4,
      serverCursor: "cursor-4",
      boundary: "boundary-4",
    },
  };
  await expect(cache.commitBatch(baseline)).resolves.toMatchObject({ outcome: "committed", coverage: "extended" });
  const suffix = {
    kind: "suffix" as const,
    readId: "read-1",
    continuityId: "connection-1",
    previousBoundary: "boundary-4",
    boundary: "boundary-5",
    start: { entry: 0, item: 0 },
    end: { entry: 5, item: 0 },
    representedSnapshot: 5,
  };
  await expect(cache.commitBatch({ ...baseline, coverage: suffix })).resolves.toMatchObject({
    outcome: "committed",
    coverage: "extended",
  });
  await expect(
    cache.commitBatch({
      ...baseline,
      items: [
        {
          historyKey,
          transcriptKey: "old-key-update",
          position: { entry: 1, item: 0 },
          version: 20,
          item: { id: "old-key-update", turnId: "turn-1", type: "toolResult", text: "completion" },
        },
        {
          historyKey,
          transcriptKey: "distant-new-item",
          position: { entry: 90, item: 0 },
          version: 91,
          item: { id: "distant-new-item", turnId: "turn-9", type: "assistant", text: "far away" },
        },
      ],
      coverage: { ...suffix, previousBoundary: "missing-work", boundary: "boundary-6", representedSnapshot: 6 },
    }),
  ).resolves.toMatchObject({ outcome: "committed", coverage: "not-extended" });
  await expect(cache.getCoverage(historyKey, "read:read-1")).resolves.toMatchObject({
    end: { entry: 4, item: 0 },
    serverCursor: "cursor-4",
  });
  await expect(cache.getCoverage(historyKey, "suffix:read-1")).resolves.toMatchObject({
    end: { entry: 5, item: 0 },
    boundary: "boundary-5",
  });
  await expect(
    cache.commitBatch({
      ...baseline,
      coverage: { ...suffix, previousBoundary: "boundary-5", boundary: "boundary-7", representedSnapshot: 7 },
    }),
  ).resolves.toMatchObject({ outcome: "committed", coverage: "not-extended" });
  await expect(cache.getCoverage(historyKey, "read:read-1")).resolves.toMatchObject({ suffixBlocked: true });
  const seedKeys = (await cache.getLatest(historyKey, 200)).map((row) => row.transcriptKey);
  expect(seedKeys).toEqual(expect.arrayContaining(["old-key-update", "distant-new-item"]));

  await expect(
    cache.commitBatch({
      ...baseline,
      coverage: { ...baseline.coverage, readId: "shorter-read", end: { entry: 2, item: 0 }, representedSnapshot: 2 },
    }),
  ).resolves.toMatchObject({ outcome: "committed", coverage: "extended" });
  await expect(cache.getCoverage(historyKey, "read:read-1")).resolves.toMatchObject({
    end: { entry: 4, item: 0 },
  });
});

it("refuses a batch captured before the durable Clear epoch", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);

  await expect(
    cache.commitBatch({
      historyKey: "host/session/incarnation",
      clearEpoch: 1,
      items: [],
      coverage: {
        kind: "read",
        readId: "pre-clear",
        start: null,
        end: null,
        representedSnapshot: 0,
        boundary: "pre-clear",
      },
    }),
  ).resolves.toEqual({ outcome: "refused", reason: "clear-epoch" });
  await expect(cache.getCoverage("host/session/incarnation", "read:pre-clear")).resolves.toBeUndefined();
  await expect(cache.getByteTotals("host/session/incarnation")).resolves.toEqual({
    historyBytes: 0,
    aggregateBytes: 0,
  });
});

it("treats an authoritative empty history read as complete with its real boundary", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/empty-incarnation";
  await expect(
    cache.commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [],
      coverage: {
        kind: "read",
        readId: "empty-read",
        start: null,
        end: null,
        representedSnapshot: 0,
        boundary: "empty-boundary",
      },
    }),
  ).resolves.toMatchObject({ outcome: "committed", coverage: "extended" });
  await expect(cache.getLatestWindow(historyKey, 40, "empty-boundary")).resolves.toMatchObject({
    rows: [],
    complete: true,
    stale: false,
    boundary: "empty-boundary",
  });
});

it("selects only certified rows when isolated old and distant seeds are present", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/seed-incarnation";
  const oldSeed = {
    historyKey,
    transcriptKey: "isolated-old",
    position: { entry: 2, item: 0 },
    version: 99,
    item: { id: "isolated-old", turnId: "turn-0", type: "toolResult", text: "old seed" },
  };
  const coveredRows = [10, 11].map((entry) => ({
    historyKey,
    transcriptKey: `covered-${entry}`,
    position: { entry, item: 0 },
    version: entry,
    item: { id: `covered-${entry}`, turnId: "turn-1", type: "assistant", text: `covered-${entry}` },
  }));
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: coveredRows,
    coverage: {
      kind: "read",
      readId: "read-10",
      start: { entry: 10, item: 0 },
      end: { entry: 11, item: 0 },
      representedSnapshot: 11,
      serverCursor: "cursor-10",
      boundary: "boundary-10",
    },
  });
  const distantSeed = {
    historyKey,
    transcriptKey: "isolated-distant",
    position: { entry: 90, item: 0 },
    version: 99,
    item: { id: "isolated-distant", turnId: "turn-9", type: "toolResult", text: "distant seed" },
  };
  await cache.putItems([oldSeed, distantSeed]);

  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([oldSeed, ...coveredRows, distantSeed]);
  const bounded = await cache.getLatestWindow(historyKey, 2, "boundary-10");
  expect.soft(bounded).toMatchObject({
    rows: coveredRows,
    coverage: [expect.objectContaining({ rangeId: "read:read-10", serverCursor: "cursor-10" })],
    complete: true,
    stale: false,
    boundary: "boundary-10",
    serverCursor: "cursor-10",
  });
  const wider = await cache.getLatestWindow(historyKey, 10, "boundary-10");
  expect.soft(wider).toMatchObject({ rows: coveredRows, complete: true, stale: false, boundary: "boundary-10" });
});

it("selects one highest-snapshot certificate when many reads share a boundary", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/shared-boundary";
  const rows = Array.from({ length: 24 }, (_, index) => ({
    historyKey,
    transcriptKey: `read-${index + 1}`,
    position: { entry: index + 1, item: 0 },
    version: index + 1,
    item: { id: `item-${index + 1}`, turnId: "turn-1", type: "assistant", text: `body-${index + 1}` },
  }));
  for (let index = 0; index < rows.length; index += 1) {
    const row = rows[index]!;
    await cache.commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [row],
      coverage: {
        kind: "read",
        readId: `read-${index + 1}`,
        start: row.position,
        end: row.position,
        representedSnapshot: index + 1,
        boundary: "shared-boundary",
      },
    });
  }

  const openCursor = IDBIndex.prototype.openCursor;
  let returnedCertificates = 0;
  vi.spyOn(IDBIndex.prototype, "openCursor").mockImplementation(function (this: IDBIndex, ...args) {
    const request = openCursor.apply(this, args);
    if (this.objectStore.name === ENTRY_COVERAGE_STORE) {
      request.addEventListener("success", () => {
        if (request.result) returnedCertificates += 1;
      });
    }
    return request;
  });
  const window = await cache.getLatestWindow(historyKey, 1, "shared-boundary");

  expect(window.rows).toEqual([rows[23]]);
  expect(window.coverage).toMatchObject([{ readId: "read-24", representedSnapshot: 24 }]);
  expect(returnedCertificates).toBeLessThanOrEqual(1);
});

it("reads rows and their certificate from one snapshot across a peer commit", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  const peer = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache, peer);
  const historyKey = "host/session/read-race";
  const row10 = {
    historyKey,
    transcriptKey: "entry-10",
    position: { entry: 10, item: 0 },
    version: 10,
    item: { id: "entry-10", turnId: "turn-1", type: "assistant", text: "ten" },
  };
  const row11 = {
    historyKey,
    transcriptKey: "entry-11",
    position: { entry: 11, item: 0 },
    version: 11,
    item: { id: "entry-11", turnId: "turn-1", type: "assistant", text: "eleven" },
  };
  const readCoverage = (end: typeof row10.position | typeof row11.position, representedSnapshot: number) => ({
    kind: "read" as const,
    readId: "race-read",
    start: row10.position,
    end,
    representedSnapshot,
    boundary: "race-boundary",
  });
  await cache.commitBatch({ historyKey, clearEpoch: 0, items: [row10], coverage: readCoverage(row10.position, 10) });

  const heldRead = holdNextReadonlyTransactionContaining(ENTRY_ITEM_STORE);
  const pendingWindow = cache.getLatestWindow(historyKey, 2, "race-boundary");
  try {
    await heldRead.reached;
    expect(heldRead.transaction()).toBeDefined();
    await peer.commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [row11],
      coverage: readCoverage(row11.position, 11),
    });
  } finally {
    heldRead.release();
  }

  const racedWindow = await pendingWindow;
  expect.soft(racedWindow).toMatchObject({
    rows: [row10],
    coverage: [{ end: row10.position, representedSnapshot: 10 }],
    complete: true,
    stale: false,
    boundary: "race-boundary",
  });
  await expect(peer.getLatestWindow(historyKey, 2, "race-boundary")).resolves.toMatchObject({
    rows: [row10, row11],
    coverage: [{ end: row11.position, representedSnapshot: 11 }],
    complete: true,
    stale: false,
  });
});

it("advances consecutive metadata-only suffixes before a later append", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/metadata-suffix";
  const baseline = {
    historyKey,
    clearEpoch: 0,
    items: [],
    coverage: {
      kind: "read" as const,
      readId: "metadata-read",
      start: { entry: 1, item: 0 },
      end: { entry: 5, item: 0 },
      representedSnapshot: 5,
      boundary: "B5",
    },
  };
  await cache.commitBatch(baseline);
  const suffix = (
    previousBoundary: string,
    boundary: string,
    representedSnapshot: number,
    end: { entry: number; item: number },
  ) => ({
    kind: "suffix" as const,
    readId: "metadata-read",
    continuityId: "connection-1",
    previousBoundary,
    boundary,
    start: { entry: 1, item: 0 },
    end,
    representedSnapshot,
  });

  const first = await cache.commitBatch({ ...baseline, coverage: suffix("B5", "B6", 5, { entry: 5, item: 0 }) });
  expect.soft(first).toMatchObject({ outcome: "committed", coverage: "extended" });
  expect.soft(await cache.getCoverage(historyKey, "suffix:metadata-read")).toMatchObject({
    boundary: "B6",
    representedSnapshot: 5,
  });
  const second = await cache.commitBatch({ ...baseline, coverage: suffix("B6", "B7", 5, { entry: 5, item: 0 }) });
  expect.soft(second).toMatchObject({ outcome: "committed", coverage: "extended" });
  expect.soft(await cache.getCoverage(historyKey, "suffix:metadata-read")).toMatchObject({
    boundary: "B7",
    representedSnapshot: 5,
  });

  const appended = {
    historyKey,
    transcriptKey: "entry-6",
    position: { entry: 6, item: 0 },
    version: 6,
    item: { id: "entry-6", turnId: "turn-1", type: "assistant", text: "six" },
  };
  const third = await cache.commitBatch({
    ...baseline,
    items: [appended],
    coverage: suffix("B7", "B8", 6, { entry: 6, item: 0 }),
  });
  expect.soft(third).toMatchObject({ outcome: "committed", coverage: "extended" });
  expect.soft(await cache.getCoverage(historyKey, "suffix:metadata-read")).toMatchObject({
    end: appended.position,
    boundary: "B8",
    representedSnapshot: 6,
  });
});

it("keeps baseline paging cursor associated with its range across multiple appends", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/cursor-provenance";
  const rows = [1, 2, 3, 4].map((entry) => ({
    historyKey,
    transcriptKey: `entry-${entry}`,
    position: { entry, item: 0 },
    version: entry,
    item: { id: `entry-${entry}`, turnId: "turn-1", type: "assistant", text: `body-${entry}` },
  }));
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: rows.slice(0, 2),
    coverage: {
      kind: "read",
      readId: "cursor-read",
      start: rows[0]!.position,
      end: rows[1]!.position,
      representedSnapshot: 2,
      serverCursor: "cursor-before-entry-1",
      boundary: "B2",
    },
  });
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [rows[2]!],
    coverage: {
      kind: "suffix",
      readId: "cursor-read",
      continuityId: "connection-1",
      previousBoundary: "B2",
      boundary: "B3",
      start: rows[0]!.position,
      end: rows[2]!.position,
      representedSnapshot: 3,
    },
  });
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [rows[3]!],
    coverage: {
      kind: "suffix",
      readId: "cursor-read",
      continuityId: "connection-1",
      previousBoundary: "B3",
      boundary: "B4",
      start: rows[0]!.position,
      end: rows[3]!.position,
      representedSnapshot: 4,
    },
  });

  const latest = await cache.getLatestWindow(historyKey, 2, "B4");
  expect.soft(latest.rows).toEqual(rows.slice(2));
  expect.soft(latest.coverage).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        rangeId: "read:cursor-read",
        start: rows[0]!.position,
        end: rows[1]!.position,
        serverCursor: "cursor-before-entry-1",
      }),
      expect.objectContaining({ rangeId: "suffix:cursor-read", start: rows[0]!.position, end: rows[3]!.position }),
    ]),
  );
  expect.soft(latest.serverCursor).toBeUndefined();
  const cachedBeforeCursor = await cache.getLatestWindow(historyKey, 4, "B4");
  expect(cachedBeforeCursor.rows).toEqual(rows);
  expect(cachedBeforeCursor.coverage).toEqual(latest.coverage);
});

it("preserves a longer peer read and its cursor after a shorter compatible read", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const primary = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  const peer = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(primary, peer);
  const historyKey = "host/session/shorter-peer-read";
  const rows = [10, 11, 12, 13].map((entry) => ({
    historyKey,
    transcriptKey: `entry-${entry}`,
    position: { entry, item: 0 },
    version: entry,
    item: { id: `entry-${entry}`, turnId: "turn-1", type: "assistant", text: `body-${entry}` },
  }));
  await primary.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: rows,
    coverage: {
      kind: "read",
      readId: "peer-read",
      start: rows[0]!.position,
      end: rows[3]!.position,
      representedSnapshot: 13,
      serverCursor: "cursor-13",
      boundary: "B13",
    },
  });
  await peer.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: rows.slice(0, 2),
    coverage: {
      kind: "read",
      readId: "peer-read",
      start: rows[0]!.position,
      end: rows[1]!.position,
      representedSnapshot: 11,
      serverCursor: "cursor-11",
      boundary: "B11",
    },
  });

  await expect(primary.getLatestWindow(historyKey, 2, "B13")).resolves.toMatchObject({
    rows: rows.slice(2),
    coverage: [expect.objectContaining({ end: rows[3]!.position, representedSnapshot: 13, serverCursor: "cursor-13" })],
    complete: true,
    stale: false,
    boundary: "B13",
    serverCursor: "cursor-13",
  });
  await expect(peer.getCoverage(historyKey, "read:peer-read")).resolves.toMatchObject({
    end: rows[3]!.position,
    representedSnapshot: 13,
    serverCursor: "cursor-13",
    boundary: "B13",
  });
});

it("stores requested aliases separately from resolved identity with typed session metadata", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const resolvedIdentity = {
    clientId: "client-main",
    hostId: "host-resolved",
    sessionId: "session-resolved",
    incarnation: "boot-7",
  };
  const historyKey = entryHistoryKey(resolvedIdentity);
  const metadata: TranscriptSessionMetadata = {
    historyKey,
    requestedRefs: [
      { clientId: "client-main", ref: "alias://production" },
      { clientId: "client-main", ref: "session://stable-name" },
    ],
    resolvedIdentity,
    savedAt: 1_791_550_000_000,
    accessedAt: 1_791_550_012_345,
    formatVersion: 1,
  };
  const batch = { historyKey, clearEpoch: 0, items: [], sessionMetadata: metadata };

  await expect(cache.commitBatch(batch)).resolves.toMatchObject({ outcome: "committed" });
  expect.soft(await readEntryMeta(factory, name, historyKey)).toMatchObject(metadata);
  expect(metadata.requestedRefs).not.toContainEqual({ clientId: "client-main", ref: resolvedIdentity.sessionId });
  await expect(cache.getSessionMetadata(historyKey)).resolves.toEqual(metadata);
});

it("accounts exact session metadata bytes across histories, replacement, item-only writes, and abort", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    now: () => 0,
  });
  caches.push(cache);
  const identity = (sessionId: string) => ({
    clientId: "client",
    hostId: "host",
    sessionId,
    incarnation: "boot-1",
  });
  const metadata = (sessionId: string, ref: string, savedAt: number): TranscriptSessionMetadata => {
    const resolvedIdentity = identity(sessionId);
    return {
      historyKey: entryHistoryKey(resolvedIdentity),
      requestedRefs: [{ clientId: "client", ref }],
      resolvedIdentity,
      savedAt,
      accessedAt: savedAt + 1,
      formatVersion: 1,
    };
  };
  const payload = (value: TranscriptSessionMetadata) => ({
    historyKey: value.historyKey,
    requestedRefs: value.requestedRefs,
    resolvedIdentity: value.resolvedIdentity,
    savedAt: value.savedAt,
    accessedAt: value.accessedAt,
    formatVersion: value.formatVersion,
  });
  const alpha = metadata("session-alpha", "alias://短", 100);
  const beta = metadata("session-beta", "session://beta", 200);
  await cache.commitBatch({ historyKey: alpha.historyKey, clearEpoch: 0, items: [], sessionMetadata: alpha });
  const alphaPayload = persistedSessionMetadataPayload(await readEntryMeta(factory, name, alpha.historyKey));
  expect(alphaPayload).toEqual(payload(alpha));
  const alphaBytes = independentJsonBytes(alphaPayload);
  await expect(cache.getByteTotals(alpha.historyKey)).resolves.toEqual({
    historyBytes: alphaBytes,
    aggregateBytes: alphaBytes,
  });

  await cache.commitBatch({ historyKey: beta.historyKey, clearEpoch: 0, items: [], sessionMetadata: beta });
  const betaPayload = persistedSessionMetadataPayload(await readEntryMeta(factory, name, beta.historyKey));
  expect(betaPayload).toEqual(payload(beta));
  const betaBytes = independentJsonBytes(betaPayload);
  await expect(cache.getByteTotals(beta.historyKey)).resolves.toEqual({
    historyBytes: betaBytes,
    aggregateBytes: alphaBytes + betaBytes,
  });

  const grown = metadata("session-alpha", "alias://a much longer 名称", 300);
  await cache.commitBatch({ historyKey: alpha.historyKey, clearEpoch: 0, items: [], sessionMetadata: grown });
  const grownPayload = persistedSessionMetadataPayload(await readEntryMeta(factory, name, alpha.historyKey));
  expect(grownPayload).toEqual(payload(grown));
  const grownBytes = independentJsonBytes(grownPayload);
  await expect(cache.getSessionMetadata(alpha.historyKey)).resolves.toEqual(grown);
  await expect(cache.getByteTotals(alpha.historyKey)).resolves.toEqual({
    historyBytes: grownBytes,
    aggregateBytes: grownBytes + betaBytes,
  });

  const item = {
    historyKey: alpha.historyKey,
    transcriptKey: "item-alpha",
    position: { entry: 1, item: 0 },
    version: 1,
    item: { id: "item-alpha", turnId: "turn-alpha", type: "assistant", text: "item-only" },
  };
  const storedItem = { ...item, positionSub: 0 };
  const itemBytes = independentJsonBytes(storedItem);
  await cache.commitBatch({ historyKey: alpha.historyKey, clearEpoch: 0, items: [item] });
  await expect(cache.getSessionMetadata(alpha.historyKey)).resolves.toEqual(grown);
  await expect(cache.getByteTotals(alpha.historyKey)).resolves.toEqual({
    historyBytes: grownBytes + itemBytes,
    aggregateBytes: grownBytes + betaBytes + itemBytes,
  });

  await cache.commitBatch({ historyKey: alpha.historyKey, clearEpoch: 0, items: [], sessionMetadata: grown });
  await expect(cache.getByteTotals(alpha.historyKey)).resolves.toEqual({
    historyBytes: grownBytes + itemBytes,
    aggregateBytes: grownBytes + betaBytes + itemBytes,
  });

  const shrunk = metadata("session-alpha", "alias://ø", 400);
  await cache.commitBatch({ historyKey: alpha.historyKey, clearEpoch: 0, items: [], sessionMetadata: shrunk });
  const shrunkPayload = persistedSessionMetadataPayload(await readEntryMeta(factory, name, alpha.historyKey));
  expect(shrunkPayload).toEqual(payload(shrunk));
  const shrunkBytes = independentJsonBytes(shrunkPayload);
  await expect(cache.getByteTotals(alpha.historyKey)).resolves.toEqual({
    historyBytes: shrunkBytes + itemBytes,
    aggregateBytes: shrunkBytes + betaBytes + itemBytes,
  });

  const beforeAbortMetadata = await cache.getSessionMetadata(alpha.historyKey);
  const beforeAbortTotals = await cache.getByteTotals(alpha.historyKey);
  const candidateMetadata = metadata("session-alpha", "alias://abort replacement", 500);
  const candidate = {
    historyKey: alpha.historyKey,
    transcriptKey: "aborted-item",
    position: { entry: 2, item: 0 },
    version: 2,
    item: { id: "aborted-item", turnId: "turn-alpha", type: "assistant", text: "must roll back" },
  };
  const transact = IDBDatabase.prototype.transaction;
  let transactionToAbort: IDBTransaction | undefined;
  const staged: Array<{ store: string; value: unknown }> = [];
  const put = IDBObjectStore.prototype.put;
  let held: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let pulsing = true;
  let markHeld!: () => void;
  const requestHeld = new Promise<void>((resolve) => {
    markHeld = resolve;
  });
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const opened = transact.apply(this, args);
    if (opened.mode === "readwrite" && opened.objectStoreNames.contains(ENTRY_META_STORE)) {
      transactionToAbort = opened;
    }
    return opened;
  });
  const pulse = (store: IDBObjectStore) => {
    if (pulsing) store.count().addEventListener("success", () => pulse(store), { once: true });
  };
  vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["put"]>
  ) {
    const request = put.apply(this, args);
    staged.push({ store: this.name, value: args[0] });
    if (
      !held &&
      this.name === ENTRY_META_STORE &&
      (args[0] as { historyKey?: string }).historyKey === alpha.historyKey
    ) {
      held = holdIndexedDBEvent(request, "success");
      request.addEventListener("success", () => {}, { once: true });
      void held.reached.then(markHeld);
      pulse(this);
    }
    return request;
  });

  let settled = false;
  const pending = cache
    .commitBatch({
      historyKey: alpha.historyKey,
      clearEpoch: 0,
      items: [candidate],
      sessionMetadata: candidateMetadata,
    })
    .then((result) => {
      settled = true;
      return result;
    });
  try {
    const state = await Promise.race([requestHeld.then(() => "held" as const), pending.then(() => "settled" as const)]);
    expect(state).toBe("held");
    expect(transactionToAbort).toBeDefined();
    expect(settled).toBe(false);
    expect(staged).toContainEqual({
      store: ENTRY_ITEM_STORE,
      value: expect.objectContaining({ transcriptKey: "aborted-item" }),
    });
    expect(staged).toContainEqual({
      store: ENTRY_META_STORE,
      value: expect.objectContaining({ requestedRefs: candidateMetadata.requestedRefs }),
    });
    transactionToAbort?.abort();
  } finally {
    pulsing = false;
    try {
      transactionToAbort?.abort();
    } catch {
      // The transaction may already have completed or aborted.
    }
    held?.release();
    vi.restoreAllMocks();
    await pending.catch(() => {});
  }

  await expect(pending).resolves.toMatchObject({ outcome: "failed" });
  await expect(cache.getSessionMetadata(alpha.historyKey)).resolves.toEqual(beforeAbortMetadata);
  await expect(cache.getLatest(alpha.historyKey, 10)).resolves.toEqual([item]);
  await expect(cache.getByteTotals(alpha.historyKey)).resolves.toEqual(beforeAbortTotals);

  await expect(
    cache.commitBatch({
      historyKey: alpha.historyKey,
      clearEpoch: 0,
      items: [candidate],
      sessionMetadata: candidateMetadata,
    }),
  ).resolves.toMatchObject({ outcome: "committed" });
  const candidatePayload = persistedSessionMetadataPayload(await readEntryMeta(factory, name, alpha.historyKey));
  expect(candidatePayload).toEqual(payload(candidateMetadata));
  const afterAbortBytes = independentJsonBytes(candidatePayload);
  const candidateItemBytes = independentJsonBytes({ ...candidate, positionSub: 0 });
  await expect(cache.getSessionMetadata(alpha.historyKey)).resolves.toEqual(candidateMetadata);
  await expect(cache.getByteTotals(alpha.historyKey)).resolves.toEqual({
    historyBytes: afterAbortBytes + itemBytes + candidateItemBytes,
    aggregateBytes: afterAbortBytes + betaBytes + itemBytes + candidateItemBytes,
  });
});

it("refuses a pre-Clear batch after real Clear and allows a fresh epoch write", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  const sessionCache = new SessionCacheIndexedDB({ indexedDB: factory, databaseName: name, databaseVersion: 3 });
  caches.push(cache, sessionCache);
  const historyKey = "host/session/real-clear";
  const baseline = {
    historyKey,
    transcriptKey: "baseline",
    position: { entry: 1, item: 0 },
    version: 1,
    item: { id: "baseline", turnId: "turn-1", type: "assistant", text: "survives entry epoch check" },
  };
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [baseline],
    coverage: {
      kind: "read",
      readId: "before-clear",
      start: baseline.position,
      end: baseline.position,
      representedSnapshot: 1,
      serverCursor: "cursor-before-clear",
      boundary: "before-clear",
    },
  });
  const beforeClearTotals = await cache.getByteTotals(historyKey);
  const clear = await sessionCache.clear();
  expect(clear).toEqual({ committed: true, epoch: 1 });

  const stale = {
    historyKey,
    transcriptKey: "stale-after-clear",
    position: { entry: 2, item: 0 },
    version: 2,
    item: { id: "stale-after-clear", turnId: "turn-1", type: "assistant", text: "must be refused" },
  };
  await expect(
    cache.commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [stale],
      coverage: {
        kind: "read",
        readId: "stale-after-clear",
        start: stale.position,
        end: stale.position,
        representedSnapshot: 2,
        boundary: "stale-after-clear",
      },
    }),
  ).resolves.toEqual({ outcome: "refused", reason: "clear-epoch" });
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([baseline]);
  await expect(cache.getCoverage(historyKey, "read:before-clear")).resolves.toMatchObject({
    boundary: "before-clear",
    serverCursor: "cursor-before-clear",
  });
  await expect(cache.getCoverage(historyKey, "read:stale-after-clear")).resolves.toBeUndefined();
  await expect(cache.getByteTotals(historyKey)).resolves.toEqual(beforeClearTotals);

  const fresh = { ...stale, transcriptKey: "fresh-after-clear", item: { ...stale.item, id: "fresh-after-clear" } };
  await expect(cache.commitBatch({ historyKey, clearEpoch: 1, items: [fresh] })).resolves.toMatchObject({
    outcome: "committed",
  });
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([baseline, fresh]);
  await expect(cache.getByteTotals(historyKey)).resolves.toMatchObject({
    historyBytes: expect.any(Number),
    aggregateBytes: expect.any(Number),
  });
});

it("accounts exact independent byte deltas and rolls back all held writes including turns", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/exact-byte-deltas";
  const row1 = {
    historyKey,
    transcriptKey: "entry-1",
    position: { entry: 1, item: 0 },
    version: 1,
    item: { id: "entry-1", turnId: "turn-1", type: "assistant", text: "first" },
  };
  const storedItem1 = { ...row1, positionSub: 0 };
  const turn1 = {
    id: "turn-1",
    status: "completed",
    version: 1,
    items: [{ ...row1.item, version: 1 }],
  };
  const storedTurn1 = { historyKey, turnId: "turn-1", version: 1, metadata: { status: "completed", version: 1 } };
  const coverage1 = {
    historyKey,
    rangeId: "read:bytes-read",
    kind: "read" as const,
    start: row1.position,
    end: row1.position,
    representedSnapshot: 1,
    serverCursor: "cursor-1",
    readId: "bytes-read",
    boundary: "B1",
  };
  const appendDelta =
    independentJsonBytes(storedItem1) + independentJsonBytes(storedTurn1) + independentJsonBytes(coverage1);
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [row1],
    turns: [turn1],
    coverage: {
      kind: "read",
      readId: "bytes-read",
      start: row1.position,
      end: row1.position,
      representedSnapshot: 1,
      serverCursor: "cursor-1",
      boundary: "B1",
    },
  });
  await expect(cache.getByteTotals(historyKey)).resolves.toEqual({
    historyBytes: appendDelta,
    aggregateBytes: appendDelta,
  });

  const row2 = { ...row1, version: 2, item: { ...row1.item, text: "replacement is longer" } };
  const storedItem2 = { ...row2, positionSub: 0 };
  const turn2 = { ...turn1, version: 2, items: [{ ...row2.item, version: 2 }] };
  const storedTurn2 = { historyKey, turnId: "turn-1", version: 2, metadata: { status: "completed", version: 2 } };
  const replacementDelta =
    independentJsonBytes(storedItem2) -
    independentJsonBytes(storedItem1) +
    independentJsonBytes(storedTurn2) -
    independentJsonBytes(storedTurn1);
  await cache.commitBatch({ historyKey, clearEpoch: 0, items: [row2], turns: [turn2] });
  const afterReplacement = appendDelta + replacementDelta;
  await expect(cache.getByteTotals(historyKey)).resolves.toEqual({
    historyBytes: afterReplacement,
    aggregateBytes: afterReplacement,
  });

  const coverage2 = {
    ...coverage1,
    representedSnapshot: 2,
    serverCursor: "cursor-2",
    boundary: "B2",
  };
  const metadataDelta = independentJsonBytes(coverage2) - independentJsonBytes(coverage1);
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [],
    coverage: {
      kind: "read",
      readId: "bytes-read",
      start: row1.position,
      end: row1.position,
      representedSnapshot: 2,
      serverCursor: "cursor-2",
      boundary: "B2",
    },
  });
  const beforeAbort = afterReplacement + metadataDelta;
  await expect(cache.getByteTotals(historyKey)).resolves.toEqual({
    historyBytes: beforeAbort,
    aggregateBytes: beforeAbort,
  });

  const candidate = {
    historyKey,
    transcriptKey: "candidate",
    position: { entry: 2, item: 0 },
    version: 3,
    item: { id: "candidate", turnId: "turn-2", type: "assistant", text: "must roll back" },
  };
  const storedCandidate = { ...candidate, positionSub: 0 };
  const candidateTurn = {
    id: "turn-2",
    status: "completed",
    version: 3,
    items: [{ ...candidate.item, version: 3 }],
  };
  const storedCandidateTurn = {
    historyKey,
    turnId: "turn-2",
    version: 3,
    metadata: { status: "completed", version: 3 },
  };
  const candidateCoverage = {
    historyKey,
    rangeId: "read:aborted-read",
    kind: "read" as const,
    start: candidate.position,
    end: candidate.position,
    representedSnapshot: 3,
    serverCursor: "cursor-aborted",
    readId: "aborted-read",
    boundary: "aborted-boundary",
  };
  const abortedDelta =
    independentJsonBytes(storedCandidate) +
    independentJsonBytes(storedCandidateTurn) +
    independentJsonBytes(candidateCoverage);
  expect(abortedDelta).toBeGreaterThan(0);

  const transaction = IDBDatabase.prototype.transaction;
  let heldTransaction: IDBTransaction | undefined;
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const opened = transaction.apply(this, args);
    if (opened.mode === "readwrite" && opened.objectStoreNames.contains(ENTRY_COVERAGE_STORE)) {
      heldTransaction = opened;
    }
    return opened;
  });
  const put = IDBObjectStore.prototype.put;
  const staged: Array<{ store: string; value: unknown }> = [];
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let pulsing = true;
  let markHeld!: () => void;
  const requestHeld = new Promise<void>((resolve) => {
    markHeld = resolve;
  });
  const pulse = (store: IDBObjectStore) => {
    if (pulsing) store.count().addEventListener("success", () => pulse(store), { once: true });
  };
  vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["put"]>
  ) {
    const request = put.apply(this, args);
    const value = args[0];
    staged.push({ store: this.name, value });
    if (
      !hold &&
      this.name === ENTRY_COVERAGE_STORE &&
      value !== null &&
      typeof value === "object" &&
      "rangeId" in value &&
      value.rangeId === "read:aborted-read"
    ) {
      hold = holdIndexedDBEvent(request, "success");
      request.addEventListener("success", () => {}, { once: true });
      void hold.reached.then(markHeld);
      pulse(this);
    }
    return request;
  });

  let settled = false;
  const pending = cache
    .commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [candidate],
      turns: [candidateTurn],
      coverage: {
        kind: "read",
        readId: "aborted-read",
        start: candidate.position,
        end: candidate.position,
        representedSnapshot: 3,
        serverCursor: "cursor-aborted",
        boundary: "aborted-boundary",
      },
    })
    .then((result) => {
      settled = true;
      return result;
    });
  try {
    const state = await Promise.race([requestHeld.then(() => "held" as const), pending.then(() => "settled" as const)]);
    expect(state).toBe("held");
    expect(heldTransaction).toBeDefined();
    expect(hold).toBeDefined();
    expect(settled).toBe(false);
    expect(staged).toContainEqual({
      store: ENTRY_ITEM_STORE,
      value: expect.objectContaining({ transcriptKey: "candidate", positionSub: 0, item: candidate.item }),
    });
    expect(staged).toContainEqual({ store: ENTRY_TURN_STORE, value: expect.objectContaining(storedCandidateTurn) });
    expect(staged).toContainEqual({ store: ENTRY_COVERAGE_STORE, value: expect.objectContaining(candidateCoverage) });
    expect(staged).toContainEqual({
      store: ENTRY_META_STORE,
      value: expect.objectContaining({ historyKey, encodedBytes: beforeAbort + abortedDelta, itemCount: 2 }),
    });
    expect(staged).toContainEqual({
      store: ENTRY_META_STORE,
      value: expect.objectContaining({ historyKey: "__aggregate", encodedBytes: beforeAbort + abortedDelta }),
    });
    heldTransaction?.abort();
  } finally {
    pulsing = false;
    try {
      heldTransaction?.abort();
    } catch {
      // The transaction may already have completed or aborted.
    }
    hold?.release();
    vi.restoreAllMocks();
    await pending.catch(() => {});
  }

  await expect(pending).resolves.toMatchObject({ outcome: "failed" });
  await expect(cache.getLatest(historyKey, 10)).resolves.toEqual([row2]);
  await expect(cache.getTurn(historyKey, "turn-1")).resolves.toMatchObject({
    version: 2,
    metadata: { status: "completed", version: 2 },
  });
  await expect(cache.getTurn(historyKey, "turn-2")).resolves.toBeUndefined();
  await expect(cache.getCoverage(historyKey, "read:bytes-read")).resolves.toMatchObject(coverage2);
  await expect(cache.getCoverage(historyKey, "read:aborted-read")).resolves.toBeUndefined();
  await expect(cache.getByteTotals(historyKey)).resolves.toEqual({
    historyBytes: beforeAbort,
    aggregateBytes: beforeAbort,
  });
});

it("appends and completes an old item through affected-key requests only", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/incarnation";
  const rows = Array.from({ length: 240 }, (_, entry) => ({
    historyKey,
    transcriptKey: `old-${entry}`,
    position: { entry, item: 0 },
    version: entry + 1,
    item: { id: `item-${entry}`, turnId: "turn-1", type: "assistant", text: `body-${entry}` },
  }));
  await cache.putItems(rows);

  const get = IDBObjectStore.prototype.get;
  const readKeys: Array<{ store: string; key: IDBValidKey }> = [];
  vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, ...args) {
    if (args[0] !== undefined && !(args[0] instanceof IDBKeyRange)) {
      readKeys.push({ store: this.name, key: args[0] });
    }
    return get.apply(this, args);
  });
  const appended = {
    historyKey,
    transcriptKey: "new-item",
    position: { entry: 241, item: 0 },
    version: 242,
    item: { id: "new-item", turnId: "turn-2", type: "assistant", text: "append" },
  };
  await cache.putItems([appended]);
  const appendItemKeys = readKeys.filter(({ store }) => store === ENTRY_ITEM_STORE).map(({ key }) => key);
  expect(appendItemKeys).toEqual([[historyKey, "new-item"]]);

  readKeys.length = 0;
  await cache.putItems([
    {
      ...rows[30]!,
      version: 400,
      item: { ...rows[30]!.item, output: "completed tool result", error: "structured failure" },
    },
  ]);
  const completionItemKeys = readKeys.filter(({ store }) => store === ENTRY_ITEM_STORE).map(({ key }) => key);
  expect(completionItemKeys).toEqual([[historyKey, "old-30"]]);
  vi.restoreAllMocks();

  const persisted = await cache.getLatest(historyKey, 240);
  expect(persisted.find((row) => row.transcriptKey === "old-30")).toMatchObject({
    item: { output: "completed tool result", error: "structured failure" },
  });
  expect(persisted.at(-1)).toMatchObject({ transcriptKey: "new-item" });
});

it("aborting a held pre-commit batch leaves items, coverage, and byte totals unchanged", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/incarnation";
  const prior = {
    historyKey,
    transcriptKey: "prior",
    position: { entry: 1, item: 0 },
    version: 1,
    item: { id: "prior", turnId: "turn-1", type: "assistant", text: "durable" },
  };
  await cache.putItems([prior]);
  const beforeTotals = await cache.getByteTotals(historyKey);
  const transaction = IDBDatabase.prototype.transaction;
  let heldTransaction: IDBTransaction | undefined;
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const tx = transaction.apply(this, args);
    if (tx.mode === "readwrite" && tx.objectStoreNames.contains(ENTRY_COVERAGE_STORE)) heldTransaction = tx;
    return tx;
  });

  const get = IDBObjectStore.prototype.get;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let markRequestHeld!: () => void;
  const requestHeld = new Promise<void>((resolve) => {
    markRequestHeld = resolve;
  });
  let pulsing = true;
  vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = get.apply(this, args);
    if (this.name === ENTRY_META_STORE && args[0] === "__aggregate" && !hold) {
      hold = holdIndexedDBEvent(request, "success");
      markRequestHeld();
      const pulse = () => {
        if (!pulsing) return;
        this.count().addEventListener("success", pulse, { once: true });
      };
      pulse();
    }
    return request;
  });

  let settled = false;
  const pending = cache
    .commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [
        {
          historyKey,
          transcriptKey: "aborted",
          position: { entry: 2, item: 0 },
          version: 2,
          item: { id: "aborted", turnId: "turn-1", type: "assistant", text: "must roll back" },
        },
      ],
      coverage: {
        kind: "read",
        readId: "aborted-read",
        start: { entry: 1, item: 0 },
        end: { entry: 2, item: 0 },
        representedSnapshot: 2,
        boundary: "aborted-boundary",
      },
    })
    .then((result) => {
      settled = true;
      return result;
    });

  await requestHeld;
  if (!hold) throw new Error("batch did not reach a real entry metadata request");
  await hold.reached;
  expect(heldTransaction).toBeDefined();
  expect(settled).toBe(false);
  pulsing = false;
  heldTransaction?.abort();
  hold.release();
  const result = await pending;
  vi.restoreAllMocks();

  expect(result.outcome).toBe("failed");
  await expect(cache.getLatest(historyKey, 40)).resolves.toEqual([prior]);
  await expect(cache.getCoverage(historyKey, "read:aborted-read")).resolves.toBeUndefined();
  await expect(cache.getByteTotals(historyKey)).resolves.toEqual(beforeTotals);
});

it("publishes item rows, coverage, and totals together after a held transaction commits", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/incarnation";
  const get = IDBObjectStore.prototype.get;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let markRequestHeld!: () => void;
  const requestHeld = new Promise<void>((resolve) => {
    markRequestHeld = resolve;
  });
  let pulsing = true;
  vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = get.apply(this, args);
    if (this.name === ENTRY_META_STORE && args[0] === "__aggregate" && !hold) {
      hold = holdIndexedDBEvent(request, "success");
      markRequestHeld();
      const pulse = () => {
        if (!pulsing) return;
        this.count().addEventListener("success", pulse, { once: true });
      };
      pulse();
    }
    return request;
  });

  let settled = false;
  const pending = cache
    .commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [
        {
          historyKey,
          transcriptKey: "committed",
          position: { entry: 7, item: 1 },
          version: 8,
          item: { id: "committed", turnId: "turn-1", type: "assistant", text: "all together" },
        },
      ],
      coverage: {
        kind: "read",
        readId: "committed-read",
        start: { entry: 7, item: 1 },
        end: { entry: 7, item: 1 },
        representedSnapshot: 8,
        serverCursor: "cursor-8",
        boundary: "boundary-8",
      },
    })
    .then((result) => {
      settled = true;
      return result;
    });
  await requestHeld;
  if (!hold) throw new Error("batch did not reach its real accounting read");
  await hold.reached;
  expect(settled).toBe(false);
  pulsing = false;
  hold.release();
  await expect(pending).resolves.toMatchObject({ outcome: "committed", coverage: "extended" });
  vi.restoreAllMocks();

  await expect(cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "committed" }]);
  await expect(cache.getCoverage(historyKey, "read:committed-read")).resolves.toMatchObject({
    boundary: "boundary-8",
    serverCursor: "cursor-8",
  });
  const totals = await cache.getByteTotals(historyKey);
  expect(totals.historyBytes).toBeGreaterThan(0);
  expect(totals.aggregateBytes).toBe(totals.historyBytes);
});

function newEntryCache() {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  return { cache, factory, name };
}

function itemRow(historyKey: string, entry: number) {
  return {
    historyKey,
    transcriptKey: `row-${entry}`,
    position: { entry, item: 0 },
    version: entry,
    item: { id: `row-${entry}`, turnId: "turn-1", type: "assistant", text: `body-${entry}` },
  };
}

function seedPage(
  cache: SessionEntryCache,
  historyKey: string,
  page: {
    readId: string;
    before: { entry: number; item: number } | null;
    start: { entry: number; item: number } | null;
    end: { entry: number; item: number } | null;
    representedSnapshot: number;
    serverCursor?: string;
    boundary: string;
  },
) {
  return cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [],
    coverage: {
      kind: "page",
      readId: page.readId,
      before: page.before,
      start: page.start,
      end: page.end,
      representedSnapshot: page.representedSnapshot,
      ...(page.serverCursor === undefined ? {} : { serverCursor: page.serverCursor }),
      boundary: page.boundary,
    },
  });
}

it("walks provenance-linked page certificates below a boundary and refuses an uncertified gap", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-walk";
  await cache.putItems(Array.from({ length: 20 }, (_, index) => itemRow(historyKey, index + 1)));
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 25, item: 0 },
    start: { entry: 11, item: 0 },
    end: { entry: 20, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-page1",
    boundary: "page-1",
  });
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 11, item: 0 },
    start: { entry: 1, item: 0 },
    end: { entry: 10, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-page2",
    boundary: "page-2",
  });

  const full = await cache.getOlderWindow(historyKey, { entry: 25, item: 0 }, 100);
  expect(full.stale).toBe(false);
  expect(full.complete).toBe(true);
  expect(full.rows.map((row) => row.position.entry)).toEqual(Array.from({ length: 20 }, (_, index) => index + 1));
  // The continuation cursor belongs to the oldest connected certificate, not
  // to the newest one the query happened to anchor on.
  expect(full.serverCursor).toBe("cursor-page2");

  const gap = await cache.getOlderWindow(historyKey, { entry: 21, item: 0 }, 100);
  expect(gap.stale).toBe(true);
  expect(gap.rows).toEqual([]);
  expect(gap.serverCursor).toBeUndefined();

  const topOnly = await cache.getOlderWindow(historyKey, { entry: 10, item: 0 }, 100);
  expect(topOnly.rows.map((row) => row.position.entry)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9]);
  expect(topOnly.complete).toBe(true);
});

it("truncates an older window to the limit and withholds the cursor until certified coverage is exhausted", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-limit";
  await cache.putItems(Array.from({ length: 20 }, (_, index) => itemRow(historyKey, index + 1)));
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 25, item: 0 },
    start: { entry: 11, item: 0 },
    end: { entry: 20, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-page1",
    boundary: "page-1",
  });

  const first = await cache.getOlderWindow(historyKey, { entry: 25, item: 0 }, 5);
  expect(first.rows.map((row) => row.position.entry)).toEqual([16, 17, 18, 19, 20]);
  expect(first.complete).toBe(false);
  expect(first.serverCursor).toBeUndefined();

  const second = await cache.getOlderWindow(historyKey, { entry: 16, item: 0 }, 5);
  expect(second.rows.map((row) => row.position.entry)).toEqual([11, 12, 13, 14, 15]);
  expect(second.complete).toBe(true);
  expect(second.serverCursor).toBe("cursor-page1");

  const zero = await cache.getOlderWindow(historyKey, { entry: 25, item: 0 }, 0);
  expect(zero).toMatchObject({ rows: [], complete: false, stale: false });
});

it("distinguishes an empty terminal page, no certificate, and a zero limit", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-terminal";
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 5, item: 0 },
    start: null,
    end: null,
    representedSnapshot: 5,
    boundary: "terminal",
  });

  const terminal = await cache.getOlderWindow(historyKey, { entry: 5, item: 0 }, 10);
  expect(terminal).toMatchObject({ rows: [], complete: true, stale: false, boundary: "terminal" });
  expect(terminal.serverCursor).toBeUndefined();

  const zero = await cache.getOlderWindow(historyKey, { entry: 5, item: 0 }, 0);
  expect(zero).toMatchObject({ rows: [], complete: true, stale: false });

  const missing = await cache.getOlderWindow(historyKey, { entry: 9, item: 0 }, 10);
  expect(missing).toMatchObject({ rows: [], complete: false, stale: true });
  expect(missing.boundary).toBeUndefined();
});

it("picks the most specific page certificate deterministically when several share a boundary", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-deterministic";
  await cache.putItems(Array.from({ length: 12 }, (_, index) => itemRow(historyKey, index + 1)));
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 25, item: 0 },
    start: { entry: 1, item: 0 },
    end: { entry: 5, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-sibling",
    boundary: "page-sibling",
  });
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 25, item: 0 },
    start: { entry: 11, item: 0 },
    end: { entry: 12, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-anchor",
    boundary: "page-anchor",
  });

  const window = await cache.getOlderWindow(historyKey, { entry: 25, item: 0 }, 100);
  expect(window.rows.map((row) => row.position.entry)).toEqual([11, 12]);
  expect(window.serverCursor).toBe("cursor-anchor");
});

it("never lets a page certificate hijack the latest window selection", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-latest";
  await cache.putItems([100, 101, 102, 103, 104, 105, 106].map((entry) => itemRow(historyKey, entry)));
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [],
    coverage: {
      kind: "read",
      readId: "read-latest",
      start: { entry: 100, item: 0 },
      end: { entry: 106, item: 0 },
      representedSnapshot: 106,
      serverCursor: "read-cursor",
      boundary: "read-latest",
    },
  });
  await seedPage(cache, historyKey, {
    readId: "read-latest",
    before: { entry: 60, item: 0 },
    start: { entry: 50, item: 0 },
    end: { entry: 59, item: 0 },
    representedSnapshot: 99_999,
    serverCursor: "page-cursor",
    boundary: "page-99",
  });

  const latest = await cache.getLatestWindow(historyKey, 3);
  expect(latest).toMatchObject({ complete: true, stale: false, boundary: "read-latest", serverCursor: "read-cursor" });
  expect(latest.rows.map((row) => row.position.entry)).toEqual([104, 105, 106]);

  const explicit = await cache.getLatestWindow(historyKey, 3, "read-latest");
  expect(explicit).toMatchObject({ boundary: "read-latest", serverCursor: "read-cursor" });
  await expect(cache.getLatest(historyKey, 3)).resolves.toMatchObject([
    { position: { entry: 104, item: 0 } },
    { position: { entry: 105, item: 0 } },
    { position: { entry: 106, item: 0 } },
  ]);
});

it("keeps page certificates when a later read commits", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-persist";
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 25, item: 0 },
    start: { entry: 11, item: 0 },
    end: { entry: 20, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-page1",
    boundary: "page-keep",
  });
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [],
    coverage: {
      kind: "read",
      readId: "read-later",
      start: { entry: 21, item: 0 },
      end: { entry: 24, item: 0 },
      representedSnapshot: 24,
      boundary: "shorter-read",
    },
  });
  await expect(cache.getCoverage(historyKey, "page:read-1:page-keep")).resolves.toMatchObject({
    kind: "page",
    boundary: "page-keep",
    serverCursor: "cursor-page1",
  });
});

it("preserves a continuation cursor from an empty nonterminal page", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-empty-nonterminal";
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 5, item: 0 },
    start: null,
    end: null,
    representedSnapshot: 5,
    serverCursor: "next-empty",
    boundary: "empty",
  });

  const window = await cache.getOlderWindow(historyKey, { entry: 5, item: 0 }, 10);
  expect(window).toMatchObject({
    rows: [],
    complete: true,
    stale: false,
    boundary: "empty",
    serverCursor: "next-empty",
  });
});

it("exhausts a connected terminal page instead of returning the superseded cursor", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-connected-terminal";
  await cache.putItems(Array.from({ length: 10 }, (_, index) => itemRow(historyKey, index + 11)));
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 25, item: 0 },
    start: { entry: 11, item: 0 },
    end: { entry: 20, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-superseded",
    boundary: "page-top",
  });
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 11, item: 0 },
    start: null,
    end: null,
    representedSnapshot: 10,
    boundary: "page-terminal",
  });

  const window = await cache.getOlderWindow(historyKey, { entry: 25, item: 0 }, 100);
  expect(window.rows.map((row) => row.position.entry)).toEqual([11, 12, 13, 14, 15, 16, 17, 18, 19, 20]);
  expect(window.complete).toBe(true);
  expect(window.serverCursor).toBeUndefined();
});

it("returns an empty complete window with its cursor when the boundary equals the range start", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-before-equals-start";
  await cache.putItems(Array.from({ length: 10 }, (_, index) => itemRow(historyKey, index + 11)));
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 25, item: 0 },
    start: { entry: 11, item: 0 },
    end: { entry: 20, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-C1",
    boundary: "page-top",
  });

  await expect(cache.getOlderWindow(historyKey, { entry: 11, item: 0 }, 10)).resolves.toMatchObject({
    rows: [],
    complete: true,
    stale: false,
    serverCursor: "cursor-C1",
  });
});

it("includes an overlapping certified page range in an older window", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-overlap";
  await cache.putItems(Array.from({ length: 20 }, (_, index) => itemRow(historyKey, index + 1)));
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 25, item: 0 },
    start: { entry: 11, item: 0 },
    end: { entry: 20, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-top",
    boundary: "page-top",
  });
  await seedPage(cache, historyKey, {
    readId: "read-1",
    before: { entry: 20, item: 0 },
    start: { entry: 1, item: 0 },
    end: { entry: 15, item: 0 },
    representedSnapshot: 25,
    serverCursor: "cursor-bottom",
    boundary: "page-bottom",
  });

  const window = await cache.getOlderWindow(historyKey, { entry: 25, item: 0 }, 100);
  expect(window.rows.map((row) => row.position.entry)).toEqual(Array.from({ length: 20 }, (_, index) => index + 1));
  expect(window.serverCursor).toBe("cursor-bottom");
});

it("ignores a page certificate when the latest window is requested by that page boundary", async () => {
  const { cache } = newEntryCache();
  const historyKey = "host/session/page-latest-boundary";
  await cache.putItems([100, 101, 102, 103].map((entry) => itemRow(historyKey, entry)));
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [],
    coverage: {
      kind: "read",
      readId: "read-latest",
      start: { entry: 100, item: 0 },
      end: { entry: 103, item: 0 },
      representedSnapshot: 103,
      serverCursor: "read-cursor",
      boundary: "read-latest",
    },
  });
  await seedPage(cache, historyKey, {
    readId: "read-latest",
    before: { entry: 60, item: 0 },
    start: { entry: 50, item: 0 },
    end: { entry: 59, item: 0 },
    representedSnapshot: 99_999,
    serverCursor: "page-cursor",
    boundary: "page-boundary",
  });

  const explicit = await cache.getLatestWindow(historyKey, 3, "page-boundary");
  expect(explicit).toMatchObject({ complete: false, stale: true });
  expect(explicit.serverCursor).toBeUndefined();
});

function identityFor(sessionId: string) {
  return { clientId: "c", hostId: "h", sessionId, incarnation: "i" };
}

function metadataFor(sessionId: string, savedAt: number, accessedAt: number) {
  const resolvedIdentity = identityFor(sessionId);
  return {
    historyKey: entryHistoryKey(resolvedIdentity),
    requestedRefs: [{ clientId: resolvedIdentity.clientId, ref: `local:${sessionId}` }],
    resolvedIdentity,
    savedAt,
    accessedAt,
    formatVersion: 3,
  };
}

it("evicts the least recently used history together with its coverage under a small cap", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    maxBytes: 1,
    ttlMs: Number.MAX_SAFE_INTEGER,
    now: () => 1_000,
  });
  caches.push(cache);
  const older = entryHistoryKey(identityFor("older"));
  const newer = entryHistoryKey(identityFor("newer"));
  await cache.commitBatch({
    historyKey: older,
    clearEpoch: 0,
    items: [itemRow(older, 1)],
    coverage: {
      kind: "read",
      readId: "read-older",
      start: { entry: 1, item: 0 },
      end: { entry: 1, item: 0 },
      representedSnapshot: 1,
      boundary: "older",
    },
    sessionMetadata: metadataFor("older", 1, 1),
  });
  await cache.commitBatch({
    historyKey: newer,
    clearEpoch: 0,
    items: [itemRow(newer, 2)],
    sessionMetadata: metadataFor("newer", 2, 2),
  });

  // The protected (current) identity survives; the only other identity is
  // evicted atomically with its coverage rather than left with a silent hole.
  await expect(cache.getLatest(older, 10)).resolves.toEqual([]);
  await expect(cache.getCoverage(older, "read:read-older")).resolves.toBeUndefined();
  await expect(cache.getByteTotals(older)).resolves.toEqual({ historyBytes: 0, aggregateBytes: expect.any(Number) });
  await expect(cache.getLatest(newer, 10)).resolves.toMatchObject([{ transcriptKey: "row-2" }]);
});

it("reclaims expired history on the next transaction under an injected clock", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  let now = 1_000;
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    maxBytes: 32 * 1024 * 1024,
    ttlMs: 100,
    now: () => now,
  });
  caches.push(cache);
  const expired = entryHistoryKey(identityFor("expired"));
  const fresh = entryHistoryKey(identityFor("fresh"));
  await cache.commitBatch({
    historyKey: expired,
    clearEpoch: 0,
    items: [itemRow(expired, 1)],
    sessionMetadata: metadataFor("expired", 1_000, 1_000),
  });

  now = 1_200;
  await cache.commitBatch({
    historyKey: fresh,
    clearEpoch: 0,
    items: [itemRow(fresh, 2)],
    sessionMetadata: metadataFor("fresh", 1_200, 1_200),
  });

  await expect(cache.getLatest(expired, 10)).resolves.toEqual([]);
  await expect(cache.getLatest(fresh, 10)).resolves.toMatchObject([{ transcriptKey: "row-2" }]);
});
