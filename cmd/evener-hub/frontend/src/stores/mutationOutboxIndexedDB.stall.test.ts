// @vitest-environment node

import { IDBDatabase, IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB, MutationStorageWedgedError } from "./mutationOutboxIndexedDB";
import { holdIndexedDBEvent, neverSettlingRequest } from "./testing/stalledIndexedDB";

const intent: MutationIntent = {
  targetRef: "local:thread-1",
  method: "turn/steer",
  payload: { ref: "local:thread-1", input: [{ type: "text", text: "one message" }] },
  attachments: [],
  optimisticDisplay: null,
};

// The open ladder (#openWithRecovery, #runTransaction) adds await hops over a
// bare #open, so a single microtask no longer reaches IndexedDB. Flush a
// bounded number of turns; the bound is a tripwire, not the mechanism.
async function flushMicrotasks(turns = 5): Promise<void> {
  for (let i = 0; i < turns; i += 1) await Promise.resolve();
}

// One real macrotask hop, off the faked timers: MessageChannel is a task the
// setTimeout fake does not touch. fake-indexeddb delivers open/delete events on
// such a task, which a fake-timer advance does not reach.
function nextRealTask(): Promise<void> {
  return new Promise<void>((resolve) => {
    const channel = new MessageChannel();
    channel.port1.onmessage = () => {
      channel.port1.close();
      channel.port2.close();
      resolve();
    };
    channel.port2.postMessage(null);
  });
}

// Yield to the real task queue until `ready()`, bounded, so a parked recovery
// can progress without its fake watchdog firing first.
async function settleRealTasks(ready: () => boolean): Promise<void> {
  for (let i = 0; i < 20 && !ready(); i += 1) {
    await nextRealTask();
  }
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
  await flushMicrotasks();
  if (!hold) throw new Error("read did not reach IndexedDB");
  await hold.reached;
  await vi.runOnlyPendingTimersAsync();
  expect(failure).toMatchObject({ name: "MutationStorageTimeoutError" });
  await read;
  hold.release();
  expect(await storage.listOutbox()).toEqual([record]);
  storage.close();
});

test("a timed-out open cannot install its late connection over a retried connection", async () => {
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
  // The timeout hands off to the non-destructive retry, which opens its own
  // connection. Release the withheld late success so its abandoned
  // connection closes rather than lingering.
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
      // The first two opens wedge (no success, error, or blocked ever arrives):
      // the plain retry after the first timeout also fails, so the adapter
      // reaches the destructive reset.
      if (mainOpens <= 2) return neverSettlingRequest();
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
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
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
      // Two timeouts in a row are the wedge signature; the reset then heals.
      if (mainOpens <= 2) return neverSettlingRequest();
    }
    return open(name, version);
  });
  const wedged: boolean[] = [];
  const resets: number[] = [];
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName,
    onStorageWedged: (value) => wedged.push(value),
    onStorageReset: () => resets.push(1),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const enqueue = storage.enqueueIntent(intent);
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
  const record = await enqueue;
  expect(await storage.listOutbox()).toEqual([record]);
  expect(mainOpens).toBeGreaterThanOrEqual(3);
  expect(wedged).toEqual([]);
  expect(resets).toHaveLength(1);
  storage.close();
});

test("a single transient open timeout retries without deleting the database or losing records", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-transient-retry";
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const seeded = await storage.enqueueIntent(intent);
  // Drop the live connection so the next call must open the database again.
  storage.close();
  const deleteDatabase = vi.spyOn(IDBFactory.prototype, "deleteDatabase");
  const open = indexedDB.open.bind(indexedDB);
  let opens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    opens += 1;
    // Exactly one open wedges, the way a tab frozen mid-open trips the
    // watchdog and answers on the very next request.
    if (opens === 1) return neverSettlingRequest();
    return open(name, version);
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const enqueue = storage.enqueueIntent(intent);
  await vi.advanceTimersByTimeAsync(10_000);
  const committed = await enqueue;
  // The one timeout was transient: the plain retry reopened the existing
  // database, so the new record committed and the earlier one survived, with
  // no destructive reset.
  expect(await storage.listOutbox()).toEqual([seeded, committed]);
  expect(deleteDatabase).not.toHaveBeenCalled();
  expect(opens).toBe(2);
  storage.close();
});

test("concurrent operations share one reset and both settle successfully", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-concurrent-reset";
  const open = indexedDB.open.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
      if (mainOpens <= 2) return neverSettlingRequest();
    }
    return open(name, version);
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const read = storage.listOutbox();
  const enqueue = storage.enqueueIntent(intent);
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
  // Both callers awaited the one shared recovery and settled: neither rejected
  // with "Mutation outbox connection was closed" from a clobbered reopen.
  const [before, record] = await Promise.all([read, enqueue]);
  expect(Array.isArray(before)).toBe(true);
  expect(record.intentSequence).toBe(1);
  expect(await storage.listOutbox()).toEqual([record]);
  storage.close();
});

test("the wedged latch clears after the cooldown so a recovered origin heals", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-cooldown";
  const open = indexedDB.open.bind(indexedDB);
  const deleteDatabase = indexedDB.deleteDatabase.bind(indexedDB);
  let wedged = true;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) =>
    wedged ? neverSettlingRequest() : open(name, version),
  );
  vi.spyOn(indexedDB, "deleteDatabase").mockImplementation((name: string) =>
    wedged ? neverSettlingRequest() : deleteDatabase(name),
  );
  let now = 0;
  const states: boolean[] = [];
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName,
    now: () => now,
    onStorageWedged: (value) => states.push(value),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const first = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
  await vi.advanceTimersByTimeAsync(3_000); // the probe watchdog
  expect(await first).toBeInstanceOf(MutationStorageWedgedError);
  expect(states).toEqual([true]);
  // The multi-tab hold releases and the cooldown elapses: the latch clears, the
  // normal ladder runs again, and onStorageWedged(false) fires.
  wedged = false;
  now = 15_000;
  expect(await storage.listOutbox()).toEqual([]);
  expect(states).toEqual([true, false]);
  storage.close();
});

test("a caller arriving during the probe window joins the in-flight recovery", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-join-recovery";
  const open = indexedDB.open.bind(indexedDB);
  const deleteDatabase = indexedDB.deleteDatabase.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
      if (mainOpens <= 2) return neverSettlingRequest();
    }
    return open(name, version);
  });
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(indexedDB, "deleteDatabase").mockImplementation((name: string) => {
    const request = deleteDatabase(name);
    // Park the recovery inside its delete so a caller can arrive mid-window.
    if (name === databaseName) hold = holdIndexedDBEvent(request, "success");
    return request;
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const first = storage.listOutbox();
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
  await settleRealTasks(() => hold !== undefined);
  if (!hold) throw new Error("recovery did not reach its delete");
  const opensBefore = mainOpens;
  // #database/#databasePromise are cleared here; a caller that opened beside the
  // recovery would get its connection closed by the reset. Joining opens none.
  const second = storage.listOutbox();
  await flushMicrotasks();
  expect(mainOpens).toBe(opensBefore);
  hold.release();
  const [a, b] = await Promise.all([first, second]);
  expect(a).toEqual([]);
  expect(b).toEqual([]);
  // One recovery: initial open, retry, and the single reopen.
  expect(mainOpens).toBe(3);
  storage.close();
});

test("close() during a recovery closes the late connection and does not install it", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-close-mid-recovery";
  const open = indexedDB.open.bind(indexedDB);
  const deleteDatabase = indexedDB.deleteDatabase.bind(indexedDB);
  let reopened: IDBDatabase | undefined;
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name !== databaseName) return open(name, version);
    mainOpens += 1;
    if (mainOpens <= 2) return neverSettlingRequest();
    const request = open(name, version);
    if (mainOpens === 3) {
      request.addEventListener("success", () => {
        reopened = request.result;
      });
    }
    return request;
  });
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(indexedDB, "deleteDatabase").mockImplementation((name: string) => {
    const request = deleteDatabase(name);
    if (name === databaseName) hold = holdIndexedDBEvent(request, "success");
    return request;
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const read = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
  await settleRealTasks(() => hold !== undefined);
  if (!hold) throw new Error("recovery did not reach its delete");
  storage.close();
  hold.release();
  const failure = await read;
  expect(failure).toBeInstanceOf(Error);
  expect((failure as Error).message).toMatch(/closed/);
  // The reopen landed after close(): its connection is closed, not installed.
  expect(reopened).toBeDefined();
  expect(() => reopened?.transaction("outbox")).toThrow();
  storage.close();
});

test("the reset notice fires even when the post-delete reopen then fails", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-reset-notice-failed-reopen";
  const open = indexedDB.open.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
      // The initial open, the retry, AND the post-reset reopen all time out.
      if (mainOpens <= 3) return neverSettlingRequest();
    }
    return open(name, version);
  });
  const resets: number[] = [];
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName,
    onStorageReset: () => resets.push(1),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const failure = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
  // Probe (real open) and delete land on real tasks; let the reopen be issued
  // before advancing to its watchdog.
  await settleRealTasks(() => mainOpens >= 3);
  await vi.advanceTimersByTimeAsync(10_000); // the post-reset reopen watchdog
  expect(await failure).toBeInstanceOf(MutationStorageWedgedError);
  // The delete already ran, so the loss is real even though the reopen failed.
  expect(resets).toHaveLength(1);
  storage.close();
});

test("a wedged open whose probe also stalls latches wedged and later calls fail fast", async () => {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  vi.spyOn(indexedDB, "deleteDatabase").mockImplementation(() => neverSettlingRequest());
  const wedged: boolean[] = [];
  const storage = new MutationOutboxIndexedDB({ indexedDB, onStorageWedged: (value) => wedged.push(value) });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const first = storage.enqueueIntent(intent).then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
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
    await flushMicrotasks();
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
    await flushMicrotasks();
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
    await flushMicrotasks();
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
