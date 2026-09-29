// @vitest-environment node

import { IDBDatabase, IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { MutationIntent } from "./mutationOutbox";
import {
  MutationOutboxIndexedDB,
  MutationStorageClosedError,
  MutationStorageTimeoutError,
  MutationStorageWedgedError,
} from "./mutationOutboxIndexedDB";
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

test("a timed-out open does not abort its upgrade and cannot install the late connection", async () => {
  const indexedDB = new IDBFactory();
  const open = indexedDB.open.bind(indexedDB);
  let lateDatabase: IDBDatabase | undefined;
  let lateStores: string[] = [];
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(indexedDB, "open").mockImplementationOnce((...args) => {
    const request = open(...args);
    request.addEventListener("success", () => {
      lateDatabase = request.result;
      // The upgrade committed: the schema the adapter created is present. An
      // aborted upgrade never fires success at all, so reaching here at all is
      // the proof the timeout left the versionchange transaction alone.
      const names: string[] = [];
      const storeNames = request.result.objectStoreNames;
      for (let i = 0; i < storeNames.length; i += 1) {
        const name = storeNames.item(i);
        if (name) names.push(name);
      }
      lateStores = names;
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
  // connection. Release the withheld late success so its abandoned connection
  // closes rather than lingering.
  hold.release();
  expect(await read).toEqual([]);
  expect(lateStores).toContain("outbox");
  const record = await storage.enqueueIntent(intent);
  expect(() => lateDatabase?.transaction("outbox")).toThrow();
  expect(await storage.listOutbox()).toEqual([record]);
  storage.close();
});

test("a single transient open timeout retries without deleting the database or losing records", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-transient-retry";
  const seeder = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const seeded = await seeder.enqueueIntent(intent);
  // close() is terminal, so a second adapter is what must open the database
  // again; the seeder is retired.
  seeder.close();
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
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const enqueue = storage.enqueueIntent(intent);
  await vi.advanceTimersByTimeAsync(10_000);
  const committed = await enqueue;
  // The one timeout was transient: the plain retry reopened the existing
  // database, so the new record committed and the earlier one survived.
  expect(await storage.listOutbox()).toEqual([seeded, committed]);
  expect(deleteDatabase).not.toHaveBeenCalled();
  expect(opens).toBe(2);
  storage.close();
});

// §4's stop barrier: the click-time capture must be the click's FIRST storage
// observation. A retry here would answer ~10s later and could read an epoch a
// sibling tab's Stop bumped during the stall, letting the row commit submitting
// after the Stop. The capture therefore takes a single attempt.
test("a capture read stalled by a timeout rejects rather than reading a Stop committed after the click", async () => {
  const indexedDB = new IDBFactory();
  const open = indexedDB.open.bind(indexedDB);
  const databaseName = "evener-mutation-outbox-capture-no-retry";
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
      // The capture read's one attempt wedges; a second attempt would answer.
      if (mainOpens === 1) return neverSettlingRequest();
    }
    return open(name, version);
  });
  const sender = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const sibling = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const capture = sender.readStopEpoch("local:thread-1").then(
    () => undefined,
    (error: unknown) => error,
  );
  // The sibling tab's Stop commits while the capture is stalled. A retry would
  // answer with the bumped epoch, compare it as the click-time epoch, and let
  // the row commit submitting after the Stop.
  await sibling.cancelUnattempted("local:thread-1");
  await vi.advanceTimersByTimeAsync(10_000); // the capture's single open watchdog
  // Safe outcome: the capture fails, so the send fails; the epoch the sibling
  // raised during the stall is never read.
  expect(await capture).toBeInstanceOf(MutationStorageTimeoutError);
  // The Stop really landed: a retry would have read its bumped epoch (1) and
  // let the row commit after the Stop. The capture read a timeout instead.
  expect(await sibling.readStopEpoch("local:thread-1")).toBe(1);
  sender.close();
  sibling.close();
});

test("the retry click's capture does not retry a timed-out open", async () => {
  const indexedDB = new IDBFactory();
  const open = indexedDB.open.bind(indexedDB);
  let opens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    opens += 1;
    if (opens === 1) return neverSettlingRequest();
    return open(name, version);
  });
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName: "evener-mutation-outbox-release-capture-no-retry",
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const capture = storage.getOutboxWithStopEpoch("mutation-1").then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000);
  expect(await capture).toBeInstanceOf(MutationStorageTimeoutError);
  expect(opens).toBe(1);
  storage.close();
});

test("concurrent operations share one retry and both settle successfully", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-concurrent-retry";
  const open = indexedDB.open.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
      // ONE wedged open: both callers share it, and one shared retry heals both.
      if (mainOpens === 1) return neverSettlingRequest();
    }
    return open(name, version);
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const read = storage.listOutbox();
  const enqueue = storage.enqueueIntent(intent);
  await vi.advanceTimersByTimeAsync(10_000); // the shared first open's watchdog
  const [before, record] = await Promise.all([read, enqueue]);
  expect(Array.isArray(before)).toBe(true);
  expect(record.intentSequence).toBe(1);
  expect(await storage.listOutbox()).toEqual([record]);
  // One initial open and one shared retry: the callers did not each open.
  expect(mainOpens).toBe(2);
  storage.close();
});

test("the wedged latch clears after the cooldown so a recovered origin heals", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-cooldown";
  const open = indexedDB.open.bind(indexedDB);
  let wedged = true;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) =>
    wedged ? neverSettlingRequest() : open(name, version),
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
  expect(await first).toBeInstanceOf(MutationStorageWedgedError);
  expect(states).toEqual([true]);
  // The origin recovers and the cooldown elapses: the latch fails fast only for
  // the cooldown, so the normal ladder runs again and unwedges on success.
  wedged = false;
  now = 15_000;
  expect(await storage.listOutbox()).toEqual([]);
  expect(states).toEqual([true, false]);
  storage.close();
});

test("a caller arriving during the retry window joins the in-flight recovery", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-join-recovery";
  const open = indexedDB.open.bind(indexedDB);
  let retryHold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name !== databaseName) return open(name, version);
    mainOpens += 1;
    if (mainOpens === 1) return neverSettlingRequest();
    const request = open(name, version);
    if (mainOpens === 2) retryHold = holdIndexedDBEvent(request, "success");
    return request;
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const first = storage.listOutbox();
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog; the retry is issued
  await flushMicrotasks();
  if (!retryHold) throw new Error("recovery did not reach its retry");
  const opensBefore = mainOpens;
  // The retry is in flight: a caller that opened beside the recovery would race
  // it, so it joins instead and opens none.
  const second = storage.listOutbox();
  await flushMicrotasks();
  expect(mainOpens).toBe(opensBefore);
  retryHold.release();
  const [a, b] = await Promise.all([first, second]);
  expect(a).toEqual([]);
  expect(b).toEqual([]);
  expect(mainOpens).toBe(2);
  storage.close();
});

test("close() during the recovery retry closes the late connection and does not install it", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-close-mid-recovery";
  const open = indexedDB.open.bind(indexedDB);
  let retried: IDBDatabase | undefined;
  let retryHold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name !== databaseName) return open(name, version);
    mainOpens += 1;
    if (mainOpens === 1) return neverSettlingRequest();
    const request = open(name, version);
    if (mainOpens === 2) {
      request.addEventListener("success", () => {
        retried = request.result;
      });
      retryHold = holdIndexedDBEvent(request, "success");
    }
    return request;
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const read = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog; the retry is in flight
  await flushMicrotasks();
  if (!retryHold) throw new Error("recovery did not reach its retry");
  storage.close();
  retryHold.release();
  const failure = await read;
  expect(failure).toBeInstanceOf(MutationStorageClosedError);
  // The retry landed after close(): its connection is closed, not installed.
  expect(retried).toBeDefined();
  expect(() => retried?.transaction("outbox")).toThrow();
  storage.close();
});

test("a call after close() throws the closed error and installs no connection", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-terminal-close";
  const open = indexedDB.open.bind(indexedDB);
  let opens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    opens += 1;
    return open(name, version);
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  await storage.listOutbox();
  expect(opens).toBe(1);
  storage.close();
  storage.close(); // idempotent
  await expect(storage.listOutbox()).rejects.toBeInstanceOf(MutationStorageClosedError);
  await expect(storage.enqueueIntent(intent)).rejects.toBeInstanceOf(MutationStorageClosedError);
  // No reopen: the retired adapter never installs another connection.
  expect(opens).toBe(1);
});

test("close() while wedged clears the latch and notifies false", async () => {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  const states: boolean[] = [];
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName: "evener-mutation-outbox-close-wedged",
    onStorageWedged: (value) => states.push(value),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const first = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
  expect(await first).toBeInstanceOf(MutationStorageWedgedError);
  expect(states).toEqual([true]);
  storage.close();
  // The retired adapter is not wedged, so the banner must not stay latched.
  expect(states).toEqual([true, false]);
  await expect(storage.listOutbox()).rejects.toBeInstanceOf(MutationStorageClosedError);
});

test("the wedged latch stays true through a cooldown retry that re-wedges", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-cooldown-retry-stays-wedged";
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
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
  expect(await first).toBeInstanceOf(MutationStorageWedgedError);
  expect(states).toEqual([true]);
  // Cooldown elapses but the origin is still stuck: the attempt is allowed, the
  // latch (and the banner) stays true throughout, and a re-wedge adds no flicker.
  now = 15_000;
  const second = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the cooldown attempt's watchdog
  await vi.advanceTimersByTimeAsync(10_000); // its recovery retry's watchdog
  expect(await second).toBeInstanceOf(MutationStorageWedgedError);
  expect(states).toEqual([true]);
  storage.close();
});

test("a stalled upgrade during recovery latches wedged rather than healing", async () => {
  const indexedDB = new IDBFactory();
  const createObjectStore = IDBDatabase.prototype.createObjectStore;
  let keepAlive = true;
  let upgraded: () => void = () => {};
  const upgrading = new Promise<void>((resolve) => {
    upgraded = resolve;
  });
  // Keep the versionchange transaction alive (never commits) with a count pulse
  // so the upgrade path, not the plain open path, is what stalls.
  vi.spyOn(IDBDatabase.prototype, "createObjectStore").mockImplementationOnce(function (this: IDBDatabase, ...args) {
    const store = createObjectStore.apply(this, args);
    const pulse = () => {
      store.count().addEventListener("success", () => {
        upgraded();
        if (keepAlive) pulse();
      });
    };
    pulse();
    return store;
  });
  const wedged: boolean[] = [];
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    onStorageWedged: (value) => wedged.push(value),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const failure = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  try {
    await upgrading;
    await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
    await nextRealTask();
    await vi.advanceTimersByTimeAsync(10_000); // the recovery retry's watchdog
    await settleRealTasks(() => wedged.length > 0);
    expect(await failure).toBeInstanceOf(MutationStorageWedgedError);
    expect(wedged).toEqual([true]);
  } finally {
    keepAlive = false;
    storage.close();
  }
});

test("a double timeout latches wedged, calls onStorageWedged(true), and never deletes", async () => {
  const indexedDB = new IDBFactory();
  const deleteDatabase = vi.spyOn(IDBFactory.prototype, "deleteDatabase");
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  const wedged: boolean[] = [];
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName: "evener-mutation-outbox-latch",
    onStorageWedged: (value) => wedged.push(value),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const first = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000); // the first open watchdog
  await vi.advanceTimersByTimeAsync(10_000); // the non-destructive retry's watchdog
  const error = await first;
  expect(error).toBeInstanceOf(MutationStorageWedgedError);
  expect((error as Error).name).toBe("MutationStorageWedgedError");
  expect(wedged).toEqual([true]);
  // Fast fail during the cooldown: the latched adapter rejects without
  // scheduling any watchdog, so a caller does not wait the open timeout again.
  const timersBefore = vi.getTimerCount();
  const second = await storage.listOutbox().catch((again: unknown) => again);
  expect(second).toBeInstanceOf(MutationStorageWedgedError);
  expect(vi.getTimerCount()).toBe(timersBefore);
  // Nothing is ever deleted by the latch or the ladder.
  expect(deleteDatabase).not.toHaveBeenCalled();
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
