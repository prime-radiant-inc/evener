import type { CachedSessionRecord } from "@evener/appwire-client";
import { IDBFactory } from "fake-indexeddb";
import { describe, expect, it, vi } from "vitest";
import { settleProjectionWorkForTests } from "./projectionWork";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { neverSettlingRequest } from "./testing/stalledIndexedDB";

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

// `put` and `count` are throwing stubs in this task (Tasks 3 and 4 replace
// them, and their own RED steps expect the throws), so these tests seed and
// observe rows the way the real methods will: straight through the adapter's
// own factory, in one readwrite transaction writing the two rows a real put
// writes - the record body and its `{ ref, bytes, savedAt }` meta row. The
// adapter opens its schema first, so store creation and epoch-row seeding run
// exactly as in production. Task 3 may switch `write` back to `adapter.put`
// once `put` lands.
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

// The records store's row count, read directly: `count` is a Task 4 stub,
// and the corrupt-row test needs the number now.
function countRecordsRows(factory: IDBFactory): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const request = factory.open("evener-session-cache");
    request.addEventListener(
      "success",
      () => {
        const db = request.result;
        const count = db.transaction("records", "readonly").objectStore("records").count();
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

// The tree's neverSettlingRequest() takes no factory - it IS the dead open
// request. A wedged-open adapter needs a factory whose open() returns it,
// which is the spy wrap the outbox's open-diagnostic tests use.
function neverSettlingFactory(): IDBFactory {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  return indexedDB;
}

function freshAdapter(): {
  adapter: SessionCacheIndexedDB;
  factory: IDBFactory;
  write: (r: CachedSessionRecord, epoch?: number) => Promise<void>;
} {
  const factory = new IDBFactory();
  const adapter = new SessionCacheIndexedDB({ indexedDB: factory });
  const write = async (r: CachedSessionRecord, _epoch = 0) => {
    await seedRecord(adapter, factory, r);
    await settleProjectionWorkForTests();
  };
  return { adapter, factory, write };
}

describe("SessionCacheIndexedDB get", () => {
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
    const diagnostics: unknown[] = [];
    const adapter = new SessionCacheIndexedDB({
      indexedDB: neverSettlingFactory(),
      onOpenDiagnostic: (d) => diagnostics.push(d),
    });
    vi.useFakeTimers();
    try {
      const pending = adapter.get("local:thr_1", 1_000);
      await vi.advanceTimersByTimeAsync(60_000);
      await pending;
      expect(diagnostics).toHaveLength(1);
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
    expect(await countRecordsRows(factory)).toBe(0); // the expired row was deleted, not just missed
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
    expect(await countRecordsRows(indexedDB)).toBe(0); // the corrupt row was deleted, not left behind
    adapter.close();
  });
});
