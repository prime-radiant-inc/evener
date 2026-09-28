// @vitest-environment node

import { IDBDatabase, IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB, MutationStorageWedgedError } from "./mutationOutboxIndexedDB";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";

const intent: MutationIntent = {
  targetRef: "local:thread-1",
  method: "turn/steer",
  payload: { ref: "local:thread-1", input: [{ type: "text", text: "one message" }] },
  attachments: [],
  optimisticDisplay: null,
};

// A request that never fires success, error, or blocked: the wedged
// connection-coordinator shape, where open()/deleteDatabase() return and then
// no event ever arrives.
function wedgedOpenRequest(): IDBOpenDBRequest {
  return new EventTarget() as unknown as IDBOpenDBRequest;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

test("a stalled read stops waiting and the same adapter can reopen and read its durable messages", async () => {
  const storage = new MutationOutboxIndexedDB({ indexedDB: new IDBFactory() });
  const record = await storage.enqueueIntent(intent);
  const getAll = IDBObjectStore.prototype.getAll;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementationOnce(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    hold = holdIndexedDBEvent(request, "success");
    return request;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let failure: unknown;
  const read = storage.listOutbox().catch((error) => {
    failure = error;
  });
  // #open yields even for an existing connection.
  await Promise.resolve();
  if (!hold) throw new Error("read did not reach IndexedDB");
  await hold.reached;
  await vi.runOnlyPendingTimersAsync();
  expect(failure).toMatchObject({ name: "MutationStorageTimeoutError" });
  await read;
  hold.release();
  expect(await storage.listOutbox()).toEqual([record]);
  storage.close();
});

test("a timed-out open cannot install its late connection over a reset connection", async () => {
  const indexedDB = new IDBFactory();
  const open = indexedDB.open.bind(indexedDB);
  let lateDatabase: IDBDatabase | undefined;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(indexedDB, "open").mockImplementationOnce((...args) => {
    const request = open(...args);
    request.addEventListener("success", () => {
      lateDatabase = request.result;
    });
    hold = holdIndexedDBEvent(request, "success");
    return request;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const storage = new MutationOutboxIndexedDB({ indexedDB });
  const read = storage.listOutbox();
  if (!hold) throw new Error("open did not reach IndexedDB");
  await hold.reached;
  await vi.runOnlyPendingTimersAsync();
  // The timeout hands off to the reset path: release the withheld late success
  // so its abandoned connection closes and the reset's deletion can complete.
  hold.release();
  expect(await read).toEqual([]);
  const record = await storage.enqueueIntent(intent);
  expect(() => lateDatabase?.transaction("outbox")).toThrow();
  expect(await storage.listOutbox()).toEqual([record]);
  storage.close();
});

test("a timed-out open resets the wedged database and reopens so the adapter can enqueue", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-wedged-reset";
  const open = indexedDB.open.bind(indexedDB);
  const deleteDatabase = indexedDB.deleteDatabase.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
      // The first open wedges: no success, error, or blocked ever arrives.
      if (mainOpens === 1) return wedgedOpenRequest();
    }
    return open(name, version);
  });
  const deleted: string[] = [];
  vi.spyOn(indexedDB, "deleteDatabase").mockImplementation((name: string) => {
    deleted.push(name);
    return deleteDatabase(name);
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const read = storage.listOutbox();
  await vi.advanceTimersByTimeAsync(10_000);
  // The adapter no longer aborts the wedged open; it resets the database
  // (delete + reopen) so the same adapter can read and enqueue afterward.
  expect(await read).toEqual([]);
  expect(deleted).toContain(databaseName);
  const record = await storage.enqueueIntent(intent);
  expect(await storage.listOutbox()).toEqual([record]);
  expect(record.intentSequence).toBe(1);
  storage.close();
});

test("a wedged open heals by probing, deleting, and reopening so the enqueue commits", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-wedged-heal";
  const open = indexedDB.open.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
      if (mainOpens === 1) return wedgedOpenRequest();
    }
    return open(name, version);
  });
  const wedged: boolean[] = [];
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName,
    onStorageWedged: (value) => wedged.push(value),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const enqueue = storage.enqueueIntent(intent);
  await vi.advanceTimersByTimeAsync(10_000);
  const record = await enqueue;
  expect(await storage.listOutbox()).toEqual([record]);
  expect(mainOpens).toBeGreaterThanOrEqual(2);
  expect(wedged).toEqual([]);
  storage.close();
});

test("a wedged open whose probe also stalls latches wedged and later calls fail fast", async () => {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => wedgedOpenRequest());
  vi.spyOn(indexedDB, "deleteDatabase").mockImplementation(() => wedgedOpenRequest());
  const wedged: boolean[] = [];
  const storage = new MutationOutboxIndexedDB({ indexedDB, onStorageWedged: (value) => wedged.push(value) });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const first = storage.enqueueIntent(intent).then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the open watchdog
  await vi.advanceTimersByTimeAsync(3_000); // the probe watchdog
  const error = await first;
  expect(error).toBeInstanceOf(MutationStorageWedgedError);
  expect((error as Error).name).toBe("MutationStorageWedgedError");
  expect(wedged).toEqual([true]);
  // Fast fail: the latched adapter rejects without scheduling any watchdog, so
  // a caller's next attempt does not wait the full open timeout again. Awaiting
  // it directly (no timer advance) proves the rejection needs no watchdog.
  const timersBefore = vi.getTimerCount();
  const second = await storage.enqueueIntent(intent).catch((again: unknown) => again);
  expect(second).toBeInstanceOf(MutationStorageWedgedError);
  expect(vi.getTimerCount()).toBe(timersBefore);
  storage.close();
});

test.each([undefined, true, false])(
  "a write past cancellation preserves its commit when the status listener throws on %s",
  async (throwOn) => {
    const stalled: boolean[] = [];
    const storage = new MutationOutboxIndexedDB({
      indexedDB: new IDBFactory(),
      onWriteStalled: (waiting) => {
        stalled.push(waiting);
        if (waiting === throwOn) throw new Error("status listener failed");
      },
    });
    await storage.listOutbox();
    const transact = IDBDatabase.prototype.transaction;
    let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
    vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementationOnce(function (this: IDBDatabase, ...args) {
      const transaction = transact.apply(this, args);
      hold = holdIndexedDBEvent(transaction, "complete");
      return transaction;
    });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let settled = false;
    const enqueue = storage.enqueueIntent(intent).finally(() => {
      settled = true;
    });
    await Promise.resolve();
    if (!hold) throw new Error("write did not reach IndexedDB");
    await hold.reached;
    await vi.runOnlyPendingTimersAsync();
    expect(stalled).toEqual([true]);
    expect(settled).toBe(false);
    hold.release();
    const record = await enqueue;
    expect(stalled).toEqual([true, false]);
    expect(await storage.listOutbox()).toEqual([record]);
    storage.close();
  },
);

test("a terminal abort rejects while a request callback is still withheld", async () => {
  const storage = new MutationOutboxIndexedDB({ indexedDB: new IDBFactory() });
  await storage.listOutbox();
  const get = IDBObjectStore.prototype.get;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(IDBObjectStore.prototype, "get").mockImplementationOnce(function (this: IDBObjectStore, ...args) {
    const request = get.apply(this, args);
    request.addEventListener("success", () => this.transaction.abort());
    hold = holdIndexedDBEvent(request, "success");
    return request;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const failure = storage.enqueueIntent(intent).then(
    () => undefined,
    (error: unknown) => error,
  );
  try {
    await Promise.resolve();
    if (!hold) throw new Error("write did not reach IndexedDB");
    await hold.reached;
    // Do not release the request callback or advance the watchdog clock.
    expect(await failure).toBeInstanceOf(Error);
    expect(await storage.listOutbox()).toEqual([]);
  } finally {
    hold?.release();
    storage.close();
  }
});

test("cancelling a stalled write rolls it back before the draft can be retried", async () => {
  const storage = new MutationOutboxIndexedDB({ indexedDB: new IDBFactory() });
  await storage.listOutbox();
  const get = IDBObjectStore.prototype.get;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let keepAlive = true;
  vi.spyOn(IDBObjectStore.prototype, "get").mockImplementationOnce(function (this: IDBObjectStore, ...args) {
    const request = get.apply(this, args);
    hold = holdIndexedDBEvent(request, "success");
    const pulse = () => {
      this.count().addEventListener("success", () => {
        if (keepAlive) pulse();
      });
    };
    pulse();
    return request;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let failure: unknown;
  const enqueue = storage.enqueueIntent(intent).catch((error) => {
    failure = error;
  });
  try {
    await Promise.resolve();
    if (!hold) throw new Error("write did not reach IndexedDB");
    await hold.reached;
    await vi.runOnlyPendingTimersAsync();
    expect(failure).toMatchObject({ name: "MutationStorageTimeoutError" });
    await enqueue;
  } finally {
    keepAlive = false;
    hold?.release();
  }
  expect(await storage.listOutbox()).toEqual([]);
  const retried = await storage.enqueueIntent(intent);
  expect(await storage.listOutbox()).toEqual([retried]);
  expect(retried.intentSequence).toBe(1);
  storage.close();
});
