import {
  decodeTranscriptItem,
  encodeTranscriptItem,
  markItemIdentityOnly,
  markItemTextOmitted,
} from "@evener/appwire-client";
import { IDBDatabase, IDBFactory, IDBIndex, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
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
} from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";

const caches: SessionEntryCache[] = [];
const databases: Array<{ factory: IDBFactory; name: string }> = [];
afterEach(() => {
  vi.restoreAllMocks();
  for (const cache of caches.splice(0)) cache.close();
  for (const { factory, name } of databases.splice(0)) factory.deleteDatabase(name);
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

it("returns isolated old rows as seeds but marks a window outside certified coverage stale", async () => {
  const name = `entry-cache-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  databases.push({ factory, name });
  const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  caches.push(cache);
  const historyKey = "host/session/seed-incarnation";
  const baselineRow = {
    historyKey,
    transcriptKey: "covered",
    position: { entry: 10, item: 0 },
    version: 10,
    item: { id: "covered", turnId: "turn-1", type: "assistant", text: "covered" },
  };
  await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [baselineRow],
    coverage: {
      kind: "read",
      readId: "read-10",
      start: { entry: 10, item: 0 },
      end: { entry: 10, item: 0 },
      representedSnapshot: 10,
      serverCursor: "cursor-10",
      boundary: "boundary-10",
    },
  });
  const isolatedSeed = {
    historyKey,
    transcriptKey: "isolated-old",
    position: { entry: 2, item: 0 },
    version: 99,
    item: { id: "isolated-old", turnId: "turn-0", type: "toolResult", text: "seed" },
  };
  await cache.putItems([isolatedSeed]);

  await expect(cache.getLatestWindow(historyKey, 10, "boundary-10")).resolves.toMatchObject({
    rows: [isolatedSeed, baselineRow],
    coverage: [expect.objectContaining({ rangeId: "read:read-10", serverCursor: "cursor-10" })],
    complete: false,
    stale: true,
    boundary: "boundary-10",
    serverCursor: "cursor-10",
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
