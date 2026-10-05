import { type CurrentTranscriptState, currentTranscriptState, hydrateThread } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { IDBFactory } from "fake-indexeddb";
import { describe, expect, it, vi } from "vitest";
import { type CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { holdNextWriteTransaction, neverSettlingFactory } from "./testing/stalledIndexedDB";

const DAY_MS = 24 * 60 * 60 * 1000;

function currentRecord(ownerToken: string, ref: string, savedAt = 1): CurrentSessionRecord {
  const state = currentTranscriptState(hydrateThread(currentRead(ref), ref, 1));
  if (!state) throw new Error("versioned fixture lacks current state");
  return {
    ownerToken,
    ref,
    connectionGeneration: 1,
    ownershipGeneration: 1,
    clearEpoch: 0,
    appliedSequence: 7,
    savedAt,
    state,
  };
}

function encodedBytes(record: CurrentSessionRecord): number {
  return new TextEncoder().encode(JSON.stringify(record)).byteLength;
}

describe("current session checkpoints", () => {
  it("creates the owner-scoped current store in the existing database", async () => {
    const indexedDB = new IDBFactory();
    const cache = new SessionCacheIndexedDB({ indexedDB });
    await cache.get("local:one", 2);
    const request = indexedDB.open("evener-session-cache");
    const schema = await new Promise<{
      names: string[];
      keyPath: IDBObjectStore["keyPath"];
      currentIndex: string[];
      metadataKeyPath: IDBObjectStore["keyPath"];
      index: string[];
    }>((resolve, reject) => {
      request.addEventListener(
        "success",
        () => {
          const database = request.result;
          const names = Array.from(database.objectStoreNames);
          const tx = database.transaction(["current", "currentMeta"]);
          const current = tx.objectStore("current");
          const currentMeta = tx.objectStore("currentMeta");
          const keyPath = current.keyPath;
          const currentIndex = Array.from(current.indexNames);
          const metadataKeyPath = currentMeta.keyPath;
          const index = Array.from(currentMeta.indexNames);
          database.close();
          resolve({ names, keyPath, currentIndex, metadataKeyPath, index });
        },
        { once: true },
      );
      request.addEventListener("error", () => reject(request.error), { once: true });
    });
    expect(schema.names).toContain("current");
    expect(schema.names).toContain("currentMeta");
    expect(schema.keyPath).toEqual(["ownerToken", "ref"]);
    expect(schema.currentIndex).toContain("ref");
    expect(schema.metadataKeyPath).toEqual(["ownerToken", "ref"]);
    expect(schema.index).toContain("ref");
    cache.close();
  });

  it("isolates checkpoints by owner and fences a write after clear", async () => {
    const cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory() });
    const state = currentTranscriptState(hydrateThread(currentRead("local:one"), "local:one", 1));
    if (!state) throw new Error("versioned fixture lacks current state");
    const record: CurrentSessionRecord = {
      ownerToken: "tab-a",
      ref: "local:one",
      connectionGeneration: 1,
      ownershipGeneration: 1,
      clearEpoch: 0,
      appliedSequence: 7,
      savedAt: 1,
      state: state as CurrentTranscriptState,
    };

    expect((await cache.putCurrent(record, 1)).outcome).toBe("written");
    expect((await cache.getCurrent(["tab-a", "local:one"], 2))?.record?.appliedSequence).toBe(7);
    expect((await cache.getCurrent(["tab-b", "local:one"], 2))?.record).toBeUndefined();
    const cleared = await cache.clear();
    expect(cleared.committed).toBe(true);
    expect(await cache.putCurrent(record, 3)).toEqual({ outcome: "aborted", observedEpoch: cleared.epoch });
    expect((await cache.getCurrent(["tab-a", "local:one"], 3))?.record).toBeUndefined();
    cache.close();
  });

  it("expires current state at the exact fourteen-day boundary", async () => {
    const cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory() });
    const record = currentRecord("tab-a", "local:expiry", 1);
    expect((await cache.putCurrent(record, 1)).outcome).toBe("written");
    expect(await cache.getCurrent([record.ownerToken, record.ref], 1 + 14 * DAY_MS)).toEqual({
      record: undefined,
      epoch: 0,
    });
    expect(await cache.getCurrent([record.ownerToken, record.ref], 2 + 14 * DAY_MS)).toEqual({
      record: undefined,
      epoch: 0,
    });
    cache.close();
  });

  it("deletes a checkpoint whose codec version is corrupt", async () => {
    const indexedDB = new IDBFactory();
    const cache = new SessionCacheIndexedDB({ indexedDB });
    const record = currentRecord("tab-a", "local:corrupt");
    expect((await cache.putCurrent(record, 1)).outcome).toBe("written");
    const request = indexedDB.open("evener-session-cache");
    await new Promise<void>((resolve, reject) => {
      request.addEventListener(
        "success",
        () => {
          const database = request.result;
          const tx = database.transaction("current", "readwrite");
          const row = tx.objectStore("current").get([record.ownerToken, record.ref]);
          row.addEventListener("success", () =>
            tx.objectStore("current").put({ ...row.result, state: { version: 99 } }),
          );
          tx.addEventListener(
            "complete",
            () => {
              database.close();
              resolve();
            },
            { once: true },
          );
          tx.addEventListener("abort", () => reject(tx.error), { once: true });
        },
        { once: true },
      );
      request.addEventListener("error", () => reject(request.error), { once: true });
    });
    expect(await cache.getCurrent([record.ownerToken, record.ref], 2)).toEqual({ record: undefined, epoch: 0 });
    cache.close();
  });

  it("deletes every owner's checkpoint for a ref and counts distinct refs once", async () => {
    const cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory() });
    const first = currentRecord("tab-a", "local:shared");
    const second = currentRecord("tab-b", "local:shared");
    expect((await cache.putCurrent(first, 1)).outcome).toBe("written");
    expect((await cache.putCurrent(second, 1)).outcome).toBe("written");
    const history = {
      ref: "local:shared",
      threadId: "thr_shared",
      name: "n",
      modelProvider: "p",
      model: "m",
      savedAt: 1,
      history: {
        bootGeneration: "bg",
        epoch: 1,
        incarnation: "inc",
        length: 0,
        appliedGeneration: 1,
        issuedGeneration: 1,
        turns: [],
      },
    };
    expect((await cache.put(history, 0, 1)).outcome).toBe("written");
    expect(await cache.count()).toBe(1);
    expect(await cache.deleteRecords([first.ref])).toBe(true);
    expect(await cache.get(first.ref, 2)).toBeUndefined();
    expect((await cache.getCurrent([first.ownerToken, first.ref], 2))?.record).toBeUndefined();
    expect((await cache.getCurrent([second.ownerToken, second.ref], 2))?.record).toBeUndefined();
    expect(await cache.count()).toBe(0);
    cache.close();
  });

  it("does not evict a current checkpoint to fit unrelated recorded history", async () => {
    const indexedDB = new IDBFactory();
    const checkpoint = currentRecord("tab-a", "local:current");
    const cache = new SessionCacheIndexedDB({ indexedDB, maxBytes: encodedBytes(checkpoint) });
    expect((await cache.putCurrent(checkpoint, 1)).outcome).toBe("written");
    const history = {
      ref: "local:history",
      threadId: "thr_history",
      name: "n",
      modelProvider: "p",
      model: "m",
      savedAt: 2,
      history: {
        bootGeneration: "bg",
        epoch: 1,
        incarnation: "inc",
        length: 0,
        appliedGeneration: 1,
        issuedGeneration: 1,
        turns: [],
      },
    };
    expect(await cache.put(history, 0, 2)).toEqual({ outcome: "oversize" });
    expect((await cache.getCurrent([checkpoint.ownerToken, checkpoint.ref], 3))?.record).toEqual(checkpoint);
    cache.close();
  });

  it("evicts recorded history before current state when a current write needs shared room", async () => {
    const checkpoint = currentRecord("tab-a", "local:current");
    const cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), maxBytes: encodedBytes(checkpoint) });
    const history = {
      ref: "local:history",
      threadId: "thr_history",
      name: "n",
      modelProvider: "p",
      model: "m",
      savedAt: 1,
      history: {
        bootGeneration: "bg",
        epoch: 1,
        incarnation: "inc",
        length: 0,
        appliedGeneration: 1,
        issuedGeneration: 1,
        turns: [],
      },
    };
    expect((await cache.put(history, 0, 1)).outcome).toBe("written");
    expect((await cache.putCurrent(checkpoint, 2)).outcome).toBe("written");
    expect(await cache.get(history.ref, 3)).toBeUndefined();
    expect((await cache.getCurrent([checkpoint.ownerToken, checkpoint.ref], 3))?.record).toEqual(checkpoint);
    cache.close();
  });

  it("returns current eviction receipts when current records alone exceed the shared cap", async () => {
    const first = currentRecord("tab-a", "local:one", 1);
    const second = currentRecord("tab-b", "local:two", 2);
    const newest = currentRecord("tab-c", "local:tri", 3);
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      maxBytes: encodedBytes(first) + encodedBytes(second),
    });
    expect((await cache.putCurrent(first, 1)).outcome).toBe("written");
    expect((await cache.putCurrent(second, 2)).outcome).toBe("written");
    expect(await cache.putCurrent(newest, 3)).toEqual({
      outcome: "written",
      evictedCurrent: [[first.ownerToken, first.ref]],
    });
    expect((await cache.getCurrent([first.ownerToken, first.ref], 4))?.record).toBeUndefined();
    expect((await cache.getCurrent([second.ownerToken, second.ref], 4))?.record).toEqual(second);
    cache.close();
  });

  it("rolls back a checkpoint when the real transaction aborts before commit", async () => {
    const record = currentRecord("tab-a", "local:abort");
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      beforeCommit: (operation) => {
        if (operation === "putCurrent") throw new Error("abort current write");
      },
    });
    expect(await cache.putCurrent(record, 1)).toEqual({ outcome: "failed" });
    expect((await cache.getCurrent([record.ownerToken, record.ref], 2))?.record).toBeUndefined();
    cache.close();
  });

  it("returns the durable epoch after a lookup held across a sibling clear", async () => {
    const indexedDB = new IDBFactory();
    const cache = new SessionCacheIndexedDB({ indexedDB });
    await cache.getCurrent(["tab-a", "local:open"], 1); // complete the adapter's open-time sweep first
    const held = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
    const lookup = cache.getCurrent(["tab-a", "local:race"], 1);
    await held.reached;
    const clear = await cache.clear();
    expect(clear).toEqual({ committed: true, epoch: 1 });
    held.release();
    expect(await lookup).toEqual({ record: undefined, epoch: 0 });
    expect(await cache.readEpoch()).toBe(1);
    expect(await cache.putCurrent(currentRecord("tab-a", "local:race"), 2)).toEqual({
      outcome: "aborted",
      observedEpoch: 1,
    });
    cache.close();
  });

  it("bounds current lookup and epoch reads by the 250 ms cache deadline", async () => {
    const cache = new SessionCacheIndexedDB({ indexedDB: neverSettlingFactory() });
    vi.useFakeTimers();
    try {
      const lookup = cache.getCurrent(["tab-a", "local:stalled"], 1);
      const epoch = cache.readEpoch();
      await vi.advanceTimersByTimeAsync(250);
      expect(await lookup).toBeUndefined();
      expect(await epoch).toBeUndefined();
    } finally {
      vi.useRealTimers();
      cache.close();
    }
  });

  it("bounds a current lookup while a database version upgrade is blocked", async () => {
    const indexedDB = new IDBFactory();
    const setup = new SessionCacheIndexedDB({ indexedDB });
    await setup.getCurrent(["tab-a", "local:blocker"], 1);
    setup.close();
    const request = indexedDB.open("evener-session-cache");
    const blocker = await new Promise<IDBDatabase>((resolve, reject) => {
      request.addEventListener("success", () => resolve(request.result), { once: true });
      request.addEventListener("error", () => reject(request.error), { once: true });
    });
    const blocked = new SessionCacheIndexedDB({ indexedDB, databaseVersion: 3 });
    vi.useFakeTimers();
    try {
      const lookup = blocked.getCurrent(["tab-b", "local:blocked"], 1);
      await vi.advanceTimersByTimeAsync(250);
      expect(await lookup).toBeUndefined();
    } finally {
      vi.useRealTimers();
      blocked.close();
      blocker.close();
    }
  });

  it("upgrades v1 history without changing its committed row", async () => {
    const indexedDB = new IDBFactory();
    const savedAt = Date.now();
    const history = {
      ref: "local:legacy",
      threadId: "thr_legacy",
      name: "legacy",
      modelProvider: "p",
      model: "m",
      savedAt,
      history: {
        bootGeneration: "bg",
        epoch: 1,
        incarnation: "inc",
        length: 0,
        appliedGeneration: 1,
        issuedGeneration: 1,
        turns: [],
      },
    };
    const request = indexedDB.open("evener-session-cache", 1);
    await new Promise<void>((resolve, reject) => {
      request.addEventListener(
        "upgradeneeded",
        () => {
          const database = request.result;
          database.createObjectStore("records", { keyPath: "ref" });
          const meta = database.createObjectStore("meta", { keyPath: "ref" });
          meta.put({ ref: "__clearEpoch", epoch: 0 });
        },
        { once: true },
      );
      request.addEventListener(
        "success",
        () => {
          const database = request.result;
          const tx = database.transaction(["records", "meta"], "readwrite");
          tx.objectStore("records").put(history);
          tx.objectStore("meta").put({
            ref: history.ref,
            bytes: new TextEncoder().encode(JSON.stringify(history)).byteLength,
            savedAt: history.savedAt,
          });
          tx.addEventListener(
            "complete",
            () => {
              database.close();
              resolve();
            },
            { once: true },
          );
          tx.addEventListener("abort", () => reject(tx.error), { once: true });
        },
        { once: true },
      );
      request.addEventListener("error", () => reject(request.error), { once: true });
    });
    const v2 = new SessionCacheIndexedDB({ indexedDB });
    expect((await v2.get(history.ref, savedAt + 1))?.record).toEqual(history);
    expect(await indexedDB.databases()).toEqual([{ name: "evener-session-cache", version: 2 }]);
    v2.close();
  });
});
