import type { CachedSessionRecord } from "@evener/appwire-client";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { settleProjectionWorkForTests } from "./projectionWork";
import { SessionCacheIndexedDB, type SessionCacheOpenDiagnostic } from "./sessionCacheIndexedDB";
import { bumpCacheEpochRow, holdIndexedDBEvent, neverSettlingFactory } from "./testing/stalledIndexedDB";

// These fixtures use millisecond 1_000 as "now", including opens whose expiry
// sweep has no injected timestamp. Keep the real event-delivery timers.
beforeEach(() => vi.spyOn(Date, "now").mockReturnValue(1_000));
afterEach(() => vi.restoreAllMocks());

function record(overrides: Partial<CachedSessionRecord> = {}): CachedSessionRecord {
  return {
    ref: "local:thr_1",
    threadId: "thr_1",
    name: "n",
    modelProvider: "p",
    model: "m",
    savedAt: 1_000,
    history: {
      bootGeneration: "bg",
      epoch: 1,
      incarnation: "inc",
      length: 10,
      appliedGeneration: 1,
      issuedGeneration: 1,
      turns: [],
    },
    ...overrides,
  };
}

// Raw seeding stays for the corrupt-row test only: that test seeds a valid
// row and then poisons the body behind the adapter's back, so it must not
// depend on `put`'s correctness to set up a `get` test. Everything else
// writes through the adapter (`write` calls `put` since Task 3 landed). One
// readwrite transaction writes the two rows a real put writes - the record
// body and its `{ ref, bytes, savedAt }` meta row - after a trigger `get`
// lets the adapter open its schema first, so store creation and epoch-row
// seeding run exactly as in production.
async function seedRecord(
  adapter: SessionCacheIndexedDB,
  factory: IDBFactory,
  seeded: CachedSessionRecord,
): Promise<void> {
  await adapter.get(seeded.ref, seeded.savedAt); // opens the database; a miss on a fresh one
  await new Promise<void>((resolve, reject) => {
    const request = factory.open("evener-session-cache");
    request.addEventListener(
      "success",
      () => {
        const db = request.result;
        const tx = db.transaction(["records", "meta"], "readwrite");
        tx.objectStore("records").put(seeded);
        tx.objectStore("meta").put({ ref: seeded.ref, bytes: JSON.stringify(seeded).length, savedAt: seeded.savedAt });
        tx.addEventListener(
          "complete",
          () => {
            db.close();
            resolve();
          },
          { once: true },
        );
        tx.addEventListener("error", () => reject(tx.error), { once: true });
      },
      { once: true },
    );
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

// A store's row count, read directly: `count` is a Task 4 stub, and the
// adapter tests need the numbers now - records for the delete/corruption
// assertions, meta for eviction (the epoch row is one of these rows).
function countStoreRows(factory: IDBFactory, store: "records" | "meta"): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const request = factory.open("evener-session-cache");
    request.addEventListener(
      "success",
      () => {
        const db = request.result;
        const count = db.transaction(store, "readonly").objectStore(store).count();
        count.addEventListener(
          "success",
          () => {
            const rows = count.result;
            db.close();
            resolve(rows);
          },
          { once: true },
        );
        count.addEventListener("error", () => reject(count.error), { once: true });
      },
      { once: true },
    );
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

// Poison a record body row (valid meta row kept): opens the cache database
// directly on the adapter's own factory and writes a structurally invalid
// body row beside a real meta row, in one readwrite transaction. put's cap
// enumeration must read only meta rows, so a body no decode could survive
// has to pass under it unnoticed.
async function poisonRecordBody(factory: IDBFactory, ref: string): Promise<void> {
  const body = { ref }; // no history: structurally invalid, like a truncated write
  await new Promise<void>((resolve, reject) => {
    const request = factory.open("evener-session-cache", 1);
    request.addEventListener(
      "success",
      () => {
        const db = request.result;
        const tx = db.transaction(["records", "meta"], "readwrite");
        tx.objectStore("records").put(body);
        tx.objectStore("meta").put({ ref, bytes: JSON.stringify(body).length, savedAt: 1_000 });
        tx.addEventListener(
          "complete",
          () => {
            db.close();
            resolve();
          },
          { once: true },
        );
        tx.addEventListener("error", () => reject(tx.error), { once: true });
      },
      { once: true },
    );
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

// Seed a meta row no record row backs - an orphan the adapter never creates,
// so only a raw write can produce it: clear must delete every meta row except
// the reserved epoch row, including one the records keys never name.
async function seedOrphanMetaRow(factory: IDBFactory, ref: string): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = factory.open("evener-session-cache", 1);
    request.addEventListener(
      "success",
      () => {
        const db = request.result;
        const tx = db.transaction("meta", "readwrite");
        tx.objectStore("meta").put({ ref, bytes: 10, savedAt: 1_000 });
        tx.addEventListener(
          "complete",
          () => {
            db.close();
            resolve();
          },
          { once: true },
        );
        tx.addEventListener("error", () => reject(tx.error), { once: true });
      },
      { once: true },
    );
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

function freshAdapter(): {
  adapter: SessionCacheIndexedDB;
  factory: IDBFactory;
  write: (r: CachedSessionRecord, epoch?: number) => Promise<void>;
} {
  const factory = new IDBFactory();
  const adapter = new SessionCacheIndexedDB({ indexedDB: factory });
  const write = async (r: CachedSessionRecord, epoch = 0) => {
    await adapter.put(r, epoch, r.savedAt);
    await settleProjectionWorkForTests();
  };
  return { adapter, factory, write };
}

describe("SessionCacheIndexedDB get", () => {
  it.each([
    {},
    { ...record().history, turns: null },
    { ...record().history, turns: [null] },
    { ...record().history, turns: [{ id: "t", status: "completed", items: {} }] },
    { ...record().history, turns: [{ id: "t", status: "completed", items: [null] }] },
    { ...record().history, epoch: "1" },
  ])("deletes malformed nested history and its metadata: %j", async (history) => {
    const { adapter, factory } = freshAdapter();
    await seedRecord(adapter, factory, { ...record(), history } as CachedSessionRecord);
    expect(await adapter.get("local:thr_1", 2_000)).toBeUndefined();
    expect(await countStoreRows(factory, "records")).toBe(0);
    expect(await countStoreRows(factory, "meta")).toBe(1);
    adapter.close();
  });

  it("sweeps untouched expired rows on reopen before count, preserving the epoch", async () => {
    const { adapter, factory, write } = freshAdapter();
    await write(record({ savedAt: Date.now() - 15 * 24 * 60 * 60 * 1000 }));
    await bumpCacheEpochRow(factory, 7);
    adapter.close();
    const reopened = new SessionCacheIndexedDB({ indexedDB: factory });
    expect(await reopened.count()).toBe(0);
    expect(await countStoreRows(factory, "meta")).toBe(1);
    expect(await reopened.put(record({ savedAt: Date.now() }), 0, Date.now())).toEqual({
      outcome: "aborted",
      observedEpoch: 7,
    });
    reopened.close();
  });

  it("sweeps untouched expired pairs in a deleteRecords write", async () => {
    const { adapter, factory, write } = freshAdapter();
    await write(record({ savedAt: Date.now() - 15 * 24 * 60 * 60 * 1000 }));
    expect(await adapter.deleteRecords(["local:other"])).toBe(true);
    expect(await countStoreRows(factory, "records")).toBe(0);
    expect(await countStoreRows(factory, "meta")).toBe(1);
    adapter.close();
  });

  it("round-trips a record with identical history, and the epoch captured in the same transaction is 0 on a fresh database", async () => {
    const { adapter, write } = freshAdapter();
    await write(record());
    const hit = await adapter.get("local:thr_1", 2_000);
    expect(hit?.record.history).toEqual(record().history);
    expect(hit?.record.olderCursor).toBeUndefined();
    expect(hit?.epoch).toBe(0); // Review Focus 3: fresh database, no epoch row
    adapter.close();
  });

  it("returns nothing on a miss, and nothing but not a throw on a stalled or failed open", async () => {
    const { adapter } = freshAdapter();
    expect(await adapter.get("local:absent", 1_000)).toBeUndefined();
    adapter.close();

    const stalled = new SessionCacheIndexedDB({ indexedDB: neverSettlingFactory() });
    vi.useFakeTimers();
    try {
      const pending = stalled.get("local:thr_1", 1_000);
      await vi.advanceTimersByTimeAsync(60_000); // past the 10 s storage timeout, the open's watchdog fires
      expect(await pending).toBeUndefined(); // the adapter's failure discipline: a miss, never a throw
    } finally {
      vi.useRealTimers();
      stalled.close();
    }
  });

  it("records the open failure through the diagnostic seam and never throws", async () => {
    const diagnostics: SessionCacheOpenDiagnostic[] = [];
    const adapter = new SessionCacheIndexedDB({
      indexedDB: neverSettlingFactory(),
      onOpenDiagnostic: (d) => diagnostics.push(d),
    });
    vi.useFakeTimers();
    try {
      const pending = adapter.get("local:thr_1", 1_000);
      await vi.advanceTimersByTimeAsync(60_000);
      await pending;
      expect(diagnostics).toEqual([
        { database: "evener-session-cache", version: 1, path: "open-timeout", versionchangeTransaction: false },
      ]);
    } finally {
      vi.useRealTimers();
      adapter.close();
    }
  });

  it("reads an expired record as a miss and deletes the rows it found", async () => {
    const { adapter, factory, write } = freshAdapter();
    const stale = record({ savedAt: 1_000 });
    await write(stale);
    const TTL_MS = 14 * 24 * 60 * 60 * 1000;
    const miss = await adapter.get("local:thr_1", stale.savedAt + TTL_MS + 1);
    expect(miss).toBeUndefined();
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:thr_1", stale.savedAt + TTL_MS + 2)).toBeUndefined();
    expect(await countStoreRows(factory, "records")).toBe(0); // the expired row was deleted, not just missed
    adapter.close();
  });

  it("reads a corrupt record row as a miss and deletes it (Review Focus 1)", async () => {
    // Write a valid row, then corrupt the body behind the adapter's back by
    // inserting a raw non-JSON value into the records store.
    const indexedDB = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({ indexedDB });
    await seedRecord(adapter, indexedDB, record());
    await settleProjectionWorkForTests();
    const poison = new Promise<void>((resolve, reject) => {
      const request = indexedDB.open("evener-session-cache", 1);
      request.addEventListener(
        "success",
        () => {
          const db = request.result;
          const tx = db.transaction("records", "readwrite");
          tx.objectStore("records").put({ ref: "local:thr_1" }); // no history: structurally invalid
          tx.addEventListener(
            "complete",
            () => {
              db.close();
              resolve();
            },
            { once: true },
          );
          tx.addEventListener("error", () => reject(tx.error), { once: true });
        },
        { once: true },
      );
      request.addEventListener("error", () => reject(request.error), { once: true });
    });
    await poison;
    expect(await adapter.get("local:thr_1", 2_000)).toBeUndefined();
    await settleProjectionWorkForTests();
    expect(await countStoreRows(indexedDB, "records")).toBe(0); // the corrupt row was deleted, not left behind
    adapter.close();
  });
});

describe("SessionCacheIndexedDB put", () => {
  it("caps UTF-8 payload bytes rather than UTF-16 code units", async () => {
    const factory = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({ indexedDB: factory, maxBytes: 500 });
    const small = record();
    expect(await adapter.put(small, 0, 1_000)).toEqual({ outcome: "written" });
    const large = record({ name: "界".repeat(100) });
    expect(JSON.stringify(large).length).toBeLessThan(500);
    expect(new TextEncoder().encode(JSON.stringify(large)).byteLength).toBeGreaterThan(500);
    expect(await adapter.put(large, 0, 1_000)).toEqual({ outcome: "oversize" });
    expect(await countStoreRows(factory, "records")).toBe(0);
    expect(await countStoreRows(factory, "meta")).toBe(1);
    adapter.close();
  });

  it("replaces on a second write for the same ref, and meta bytes/savedAt follow", async () => {
    const { adapter, write } = freshAdapter();
    await write(record());
    const bigger = record({
      savedAt: 2_000,
      history: {
        ...record().history,
        length: 20,
        turns: [{ id: "turn_2", status: "completed", items: [] }],
      },
    });
    await adapter.put(bigger, 0, 2_000);
    await settleProjectionWorkForTests();
    const hit = await adapter.get("local:thr_1", 3_000);
    expect(hit?.record.history.length).toBe(20);
    adapter.close();
  });

  it("evicts the least-recently-saved record past maxBytes, deleting record and meta rows together", async () => {
    // The two fixtures encode to 351 bytes each (ItemModel.text is a required
    // field), so a 500-byte cap keeps the first write and evicts it when the
    // second lands: 702 total crosses the small injected cap, which is what
    // the maxBytes option exists for.
    const factory = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({ indexedDB: factory, maxBytes: 500 });
    const older = record({
      ref: "local:old",
      history: {
        ...record().history,
        turns: [
          {
            id: "t1",
            status: "completed",
            items: [{ type: "assistantMessage", id: "i1", turnId: "t1", status: "completed", text: "" }],
          },
        ],
      },
    });
    await adapter.put(older, 0, 1_000);
    await settleProjectionWorkForTests();
    const newer = record({
      ref: "local:new",
      savedAt: 2_000,
      history: {
        ...record().history,
        turns: [
          {
            id: "t2",
            status: "completed",
            items: [{ type: "assistantMessage", id: "i2", turnId: "t2", status: "completed", text: "" }],
          },
        ],
      },
    });
    await adapter.put(newer, 0, 2_000);
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:old", 3_000)).toBeUndefined(); // evicted: oldest savedAt
    expect((await adapter.get("local:new", 3_000))?.record.ref).toBe("local:new");
    expect(await countStoreRows(factory, "records")).toBe(1); // the victim's body row went with the eviction...
    expect(await countStoreRows(factory, "meta")).toBe(2); // ...and its meta row too: the epoch row plus the survivor
    adapter.close();
  });

  it("enumeration never reads record bodies: a poisoned body row does not break a later put", async () => {
    const { adapter, factory, write } = freshAdapter();
    await write(record());
    await poisonRecordBody(factory, "local:poison"); // structurally invalid body, valid meta row
    const other = record({ ref: "local:other", savedAt: 1_500 });
    expect(await adapter.put(other, 0, 1_500)).toMatchObject({ outcome: "written" }); // the enumeration never decoded the poison
    await settleProjectionWorkForTests();
    expect(await countStoreRows(factory, "records")).toBe(3); // count() still counts it: the poisoned row exists
    expect(await adapter.get("local:poison", 2_000)).toBeUndefined(); // a poisoned body reads as a miss...
    await settleProjectionWorkForTests();
    expect(await countStoreRows(factory, "records")).toBe(2); // ...the miss deleted it...
    expect(await adapter.get("local:other", 2_000)).toBeDefined(); // ...while the healthy row survived the sweep
    adapter.close();
  });

  it("skips an oversize record whole and deletes its stored row", async () => {
    const adapter = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), maxBytes: 100 });
    // TurnModel carries no text; ItemModel.text is the settled text and is
    // required, so the 500-char bulk lives on the item: the record encodes to
    // 850 bytes, far past the 100-byte cap.
    const huge = record({
      ref: "local:huge",
      history: {
        ...record().history,
        turns: [
          {
            id: "t",
            status: "completed",
            items: [{ type: "assistantMessage", id: "i1", turnId: "t", status: "completed", text: "x".repeat(500) }],
          },
        ],
      },
    });
    expect(await adapter.put(huge, 0, 1_000)).toMatchObject({ outcome: "oversize" });
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:huge", 2_000)).toBeUndefined();
    adapter.close();
  });

  it("an oversize put the seam aborts leaves the previously stored row intact and reports failed", async () => {
    const indexedDB = new IDBFactory();
    const storer = new SessionCacheIndexedDB({ indexedDB, maxBytes: 500 });
    expect(await storer.put(record({ ref: "local:huge" }), 0, 1_000)).toMatchObject({ outcome: "written" });
    await settleProjectionWorkForTests();
    const crashing = new SessionCacheIndexedDB({
      indexedDB,
      maxBytes: 500,
      beforeCommit: (op) => {
        if (op === "put") throw new Error("fault");
      },
    });
    // The stored record encodes to 351 bytes; this outgrown one to 850, so
    // the put takes the oversize path and queues the sweep of its own row.
    const outgrown = record({
      ref: "local:huge",
      history: {
        ...record().history,
        turns: [
          {
            id: "t",
            status: "completed",
            items: [{ type: "assistantMessage", id: "i1", turnId: "t", status: "completed", text: "x".repeat(500) }],
          },
        ],
      },
    });
    expect(await crashing.put(outgrown, 0, 2_000)).toEqual({ outcome: "failed" }); // never a throw
    await settleProjectionWorkForTests();
    // The seam fired after the oversize path queued its delete, so the abort
    // rolled the sweep back: the stored row is intact, exactly as written.
    expect((await storer.get("local:huge", 3_000))?.record.history).toEqual(record({ ref: "local:huge" }).history);
    storer.close();
    crashing.close();
  });

  it("aborts a write scheduled under an older epoch and reports the observed one", async () => {
    const { adapter, factory, write } = freshAdapter();
    await write(record()); // durable epoch is 0
    await bumpCacheEpochRow(factory, 5); // clear() is the production bumper; here the row moves behind its back
    const newer = record({ savedAt: 2_000, history: { ...record().history, length: 30 } });
    expect(await adapter.put(newer, 0, 2_000)).toEqual({ outcome: "aborted", observedEpoch: 5 });
    await settleProjectionWorkForTests();
    const hit = await adapter.get("local:thr_1", 3_000);
    expect(hit?.record.history.length).toBe(10); // the aborted write changed nothing
    expect(hit?.epoch).toBe(5);
    expect(await adapter.put(newer, 5, 2_000)).toMatchObject({ outcome: "written" }); // a write carrying the new epoch commits
    adapter.close();
  });

  it("expires past-TTL rows inside every write transaction", async () => {
    const { adapter, factory, write } = freshAdapter();
    const stale = record({ ref: "local:stale", savedAt: 1_000 });
    await write(stale);
    const TTL_MS = 14 * 24 * 60 * 60 * 1000;
    const fresh = record({ ref: "local:fresh", savedAt: 2_000 });
    await adapter.put(fresh, 0, stale.savedAt + TTL_MS + 1);
    await settleProjectionWorkForTests();
    // Counted before any get: the read path also expires-and-deletes, so only
    // the row count here proves the WRITE transaction did the sweeping.
    expect(await countStoreRows(factory, "records")).toBe(1); // the stale body died inside the write
    expect(await adapter.get("local:stale", stale.savedAt + TTL_MS + 2)).toBeUndefined(); // swept by the write
    expect((await adapter.get("local:fresh", stale.savedAt + TTL_MS + 2))?.record.ref).toBe("local:fresh");
    adapter.close();
  });
});

describe("SessionCacheIndexedDB clear, deleteRecords, count", () => {
  it("clear deletes every record, increments the epoch, and reports the commit", async () => {
    const { adapter, factory, write } = freshAdapter();
    await write(record({ ref: "local:a" }));
    await write(record({ ref: "local:b" }));
    await seedOrphanMetaRow(factory, "local:orphan"); // a meta row no record row backs
    const result = await adapter.clear();
    await settleProjectionWorkForTests();
    expect(result.committed).toBe(true);
    expect(result.epoch).toBe(1);
    expect(await adapter.count()).toBe(0);
    expect(await countStoreRows(factory, "meta")).toBe(1); // every meta row died except the reserved epoch row
    // The epoch row survived clear and carries the new epoch: a put scheduled
    // under the pre-clear epoch reads it and aborts.
    expect(await adapter.put(record({ ref: "local:c" }), 0, 1_000)).toEqual({
      outcome: "aborted",
      observedEpoch: 1,
    });
    expect((await adapter.get("local:a", Date.now()))?.record).toBeUndefined();
    adapter.close();
  });

  it("an aborted clear changes nothing: records remain, the epoch remains, and committed is false (the honest no-op)", async () => {
    const indexedDB = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({
      indexedDB,
      beforeCommit: (op) => {
        if (op === "clear") throw new Error("fault");
      },
    });
    await adapter.put(record(), 0, 1_000);
    await settleProjectionWorkForTests();
    const result = await adapter.clear();
    expect(result.committed).toBe(false);
    await settleProjectionWorkForTests();
    expect(await adapter.count()).toBe(1); // nothing changed anywhere
    expect((await adapter.get("local:thr_1", 2_000))?.record.ref).toBe("local:thr_1");
    adapter.close();
  });

  it("deleteRecords removes the named refs' rows in one transaction and reports false on failure", async () => {
    const indexedDB = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({
      indexedDB,
      beforeCommit: (op) => {
        if (op === "deleteRecords") throw new Error("fault");
      },
    });
    await adapter.put(record({ ref: "local:a" }), 0, 1_000);
    await adapter.put(record({ ref: "local:b" }), 0, 1_000);
    await settleProjectionWorkForTests();
    expect(await adapter.deleteRecords(["local:a", "local:b"])).toBe(false); // aborted: idempotent retry upstream
    await settleProjectionWorkForTests();
    const ok = new SessionCacheIndexedDB({ indexedDB });
    expect(await ok.deleteRecords(["local:a", "local:b"])).toBe(true);
    await settleProjectionWorkForTests();
    expect(await ok.count()).toBe(0);
    ok.close();
    adapter.close();
  });

  it("count answers undefined when the open fails", async () => {
    const adapter = new SessionCacheIndexedDB({ indexedDB: neverSettlingFactory() });
    vi.useFakeTimers();
    try {
      const pending = adapter.count();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(await pending).toBeUndefined();
    } finally {
      vi.useRealTimers();
      adapter.close();
    }
  });
});

// Take the cache database to version 2 over the factory, closing the
// connection: the fence and retire tests need a schema version this build
// did not create.
function openAtVersionTwo(factory: IDBFactory): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const request = factory.open("evener-session-cache", 2);
    request.addEventListener(
      "success",
      () => {
        request.result.close();
        resolve();
      },
      { once: true },
    );
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

// A request that never settles on its own (neverSettlingRequest's wedged
// shape) yet exposes the two fields the upgradeneeded handler reads: a
// settable versionchange `transaction`, and a `result` whose stores all
// already exist so the handler's schema-creation branch is skipped. A test
// can dispatch a real upgradeneeded at a request the watchdog abandoned.
interface UpgradeArrivingRequest extends EventTarget {
  transaction: IDBTransaction | null;
  result: { objectStoreNames: { contains: (name: string) => boolean } };
}

function upgradeArrivingRequest(): UpgradeArrivingRequest {
  const request = new EventTarget() as UpgradeArrivingRequest;
  Object.defineProperties(request, {
    transaction: { value: null, writable: true, configurable: true },
    result: {
      value: { objectStoreNames: { contains: () => true } },
      writable: true,
      configurable: true,
    },
  });
  return request;
}

describe("SessionCacheIndexedDB open discipline", () => {
  it("closes a late open success that close() superseded instead of installing it", async () => {
    const factory = new IDBFactory();
    const adapter = new SessionCacheIndexedDB({ indexedDB: factory });
    const open = factory.open.bind(factory);
    let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
    vi.spyOn(factory, "open").mockImplementation((...args: Parameters<IDBFactory["open"]>) => {
      const request = open(...args);
      hold = holdIndexedDBEvent(request, "success");
      return request;
    });
    const pending = adapter.get("local:thr_1", 1_000); // the open succeeded; its success event is held
    if (hold === undefined) throw new Error("the held open never started");
    await hold.reached;
    adapter.close(); // supersede the in-flight open before its success is delivered
    hold.release();
    expect(await pending).toBeUndefined(); // the superseded open is a miss, never a throw
    expect(adapter.isOpen()).toBe(false); // the late connection was closed, not installed
  });

  it("reports a superseded attempt's late upgrade as abandoned, and the upgrade still commits", async () => {
    const factory = new IDBFactory();
    const diagnostics: SessionCacheOpenDiagnostic[] = [];
    const adapter = new SessionCacheIndexedDB({ indexedDB: factory, onOpenDiagnostic: (d) => diagnostics.push(d) });
    const pending = adapter.get("local:thr_1", 1_000); // a real open is in flight
    adapter.close(); // supersede it before its upgrade arrives
    expect(await pending).toBeUndefined();
    expect(diagnostics).toEqual([
      { database: "evener-session-cache", version: 1, path: "upgrade-abandoned", versionchangeTransaction: true },
    ]);
    // The schema the abandoned attempt committed is durable: the records store
    // a fresh connection reads was already there, with no upgrade to run.
    expect(await countStoreRows(factory, "records")).toBe(0);
  });

  it("records the abandoned path when an upgrade arrives for an open the watchdog already failed", async () => {
    const factory = new IDBFactory();
    const request = upgradeArrivingRequest();
    vi.spyOn(factory, "open").mockImplementation(() => request as unknown as IDBOpenDBRequest);
    const diagnostics: SessionCacheOpenDiagnostic[] = [];
    const adapter = new SessionCacheIndexedDB({ indexedDB: factory, onOpenDiagnostic: (d) => diagnostics.push(d) });
    vi.useFakeTimers();
    try {
      const pending = adapter.get("local:thr_1", 1_000);
      await vi.advanceTimersByTimeAsync(10_000); // the watchdog abandons the attempt
      expect(diagnostics).toEqual([
        { database: "evener-session-cache", version: 1, path: "open-timeout", versionchangeTransaction: false },
      ]);
      // The upgrade finally arrives on the abandoned attempt, transaction live.
      request.transaction = { objectStore: () => ({ put: () => undefined }) } as unknown as IDBTransaction;
      request.dispatchEvent(new Event("upgradeneeded"));
      expect(diagnostics).toEqual([
        { database: "evener-session-cache", version: 1, path: "open-timeout", versionchangeTransaction: false },
        { database: "evener-session-cache", version: 1, path: "upgrade-abandoned", versionchangeTransaction: true },
      ]);
      expect(await pending).toBeUndefined(); // still just a miss
    } finally {
      vi.useRealTimers();
      adapter.close();
    }
  });

  it("retires its connection when another tab takes the database to a new version, and the next read re-opens into the fence", async () => {
    const factory = new IDBFactory();
    const diagnostics: SessionCacheOpenDiagnostic[] = [];
    const adapter = new SessionCacheIndexedDB({ indexedDB: factory, onOpenDiagnostic: (d) => diagnostics.push(d) });
    expect(await adapter.get("local:absent", 1_000)).toBeUndefined(); // opens and holds the connection
    expect(adapter.isOpen()).toBe(true);
    await openAtVersionTwo(factory); // a sibling tab asks for a higher version
    expect(adapter.isOpen()).toBe(false); // this connection was told to retire
    expect(await adapter.get("local:thr_1", 1_000)).toBeUndefined(); // the re-open hits the fence: a miss
    expect(diagnostics).toEqual([
      { database: "evener-session-cache", version: 1, path: "versionchange-retire", versionchangeTransaction: false },
      { database: "evener-session-cache", version: 1, path: "version-fence", versionchangeTransaction: false },
    ]);
    adapter.close();
  });

  it("fails closed at the version fence: a newer tab's database version reads as a miss with the fence diagnostic", async () => {
    const factory = new IDBFactory();
    await openAtVersionTwo(factory);
    const diagnostics: SessionCacheOpenDiagnostic[] = [];
    const adapter = new SessionCacheIndexedDB({ indexedDB: factory, onOpenDiagnostic: (d) => diagnostics.push(d) });
    expect(await adapter.get("local:thr_1", 1_000)).toBeUndefined();
    expect(diagnostics).toEqual([
      { database: "evener-session-cache", version: 1, path: "version-fence", versionchangeTransaction: false },
    ]);
    adapter.close();
  });

  it("records nothing for an open error that is not the version fence, and still reads as a miss", async () => {
    const failure = new Error("a storage-level open failure");
    failure.name = "UnknownError";
    const request = Object.assign(new EventTarget(), { error: failure }) as unknown as IDBOpenDBRequest;
    const factory = new IDBFactory();
    vi.spyOn(factory, "open").mockImplementation(() => request);
    const diagnostics: SessionCacheOpenDiagnostic[] = [];
    const adapter = new SessionCacheIndexedDB({ indexedDB: factory, onOpenDiagnostic: (d) => diagnostics.push(d) });
    const pending = adapter.get("local:thr_1", 1_000);
    queueMicrotask(() => request.dispatchEvent(new Event("error")));
    expect(await pending).toBeUndefined();
    expect(diagnostics).toEqual([]); // the version-fence label belongs to VersionError alone
    adapter.close();
  });
});
