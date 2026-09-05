// @vitest-environment node

import { IDBDatabase, IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";

const intent: MutationIntent = {
  targetRef: "local:thread-1",
  method: "turn/steer",
  payload: { ref: "local:thread-1", input: [{ type: "text", text: "one message" }] },
  attachments: [],
  optimisticDisplay: null,
};

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

test("a timed-out open cannot install its late connection over a recovered connection", async () => {
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
  let failure: unknown;
  const read = storage.listOutbox().catch((error) => {
    failure = error;
  });
  if (!hold) throw new Error("open did not reach IndexedDB");
  await hold.reached;
  await vi.runOnlyPendingTimersAsync();
  expect(failure).toMatchObject({ name: "MutationStorageTimeoutError" });
  await read;
  const record = await storage.enqueueIntent(intent);
  hold.release();
  expect(() => lateDatabase?.transaction("outbox")).toThrow();
  expect(await storage.listOutbox()).toEqual([record]);
  storage.close();
});

test("a timed-out upgrade releases its lock so the adapter can reopen", async () => {
  const storage = new MutationOutboxIndexedDB({ indexedDB: new IDBFactory() });
  const createObjectStore = IDBDatabase.prototype.createObjectStore;
  let keepAlive = true;
  let reached: () => void = () => {};
  const upgrading = new Promise<void>((resolve) => {
    reached = resolve;
  });
  vi.spyOn(IDBDatabase.prototype, "createObjectStore").mockImplementationOnce(function (this: IDBDatabase, ...args) {
    const store = createObjectStore.apply(this, args);
    const pulse = () => {
      store.count().addEventListener("success", () => {
        reached();
        if (keepAlive) pulse();
      });
    };
    pulse();
    return store;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const failure = storage.listOutbox().catch((error: unknown) => error);
  try {
    await upgrading;
    await vi.runOnlyPendingTimersAsync();
    expect(await failure).toMatchObject({ name: "MutationStorageTimeoutError" });
    // Reopening must finish while the abandoned upgrade would otherwise remain alive.
    const record = await storage.enqueueIntent(intent);
    expect(await storage.listOutbox()).toEqual([record]);
    expect(record.intentSequence).toBe(1);
  } finally {
    keepAlive = false;
    storage.close();
  }
});

test("a write past cancellation stays pending until its original commit is observed", async () => {
  const stalled: boolean[] = [];
  const storage = new MutationOutboxIndexedDB({
    indexedDB: new IDBFactory(),
    onWriteStalled: (waiting) => stalled.push(waiting),
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
