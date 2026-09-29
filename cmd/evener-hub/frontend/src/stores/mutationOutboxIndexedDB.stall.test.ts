// @vitest-environment node

import { IDBDatabase, IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { holdIndexedDBEvent, neverSettlingRequest } from "./testing/stalledIndexedDB";

const intent: MutationIntent = {
  targetRef: "local:thread-1",
  method: "turn/steer",
  payload: { ref: "local:thread-1", input: [{ type: "text", text: "one message" }] },
  attachments: [],
  optimisticDisplay: null,
};

// A transaction's path through #runTransaction adds await hops over a bare
// #open, so a single microtask does not always reach IndexedDB. Flush a bounded
// number of turns; the bound is a tripwire, not the mechanism.
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
  const read = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  if (!hold) throw new Error("open did not reach IndexedDB");
  await hold.reached;
  await vi.runOnlyPendingTimersAsync(); // the watchdog: this attempt fails
  expect(await read).toBeInstanceOf(MutationStorageTimeoutError);
  // The upgrade committed - the schema the adapter created is present, and an
  // aborted upgrade would never have fired success at all.
  expect(lateStores).toContain("outbox");
  // Release the withheld late success: its abandoned connection closes rather
  // than being installed.
  hold.release();
  expect(() => lateDatabase?.transaction("outbox")).toThrow();
  // The open answers again: a later call attempts it and succeeds.
  const record = await storage.enqueueIntent(intent);
  expect(await storage.listOutbox()).toEqual([record]);
  storage.close();
});

test("a timeout fails the call without deleting anything, and a later call succeeds once the open answers", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-timeout-recovers";
  const seeder = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const seeded = await seeder.enqueueIntent(intent);
  // The seeder is retired after seeding; a second adapter opens the database
  // again for the timed-out first open below.
  seeder.close();
  const deleteDatabase = vi.spyOn(IDBFactory.prototype, "deleteDatabase");
  const open = indexedDB.open.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
      // The first open wedges; the open answers again afterwards.
      if (mainOpens === 1) return neverSettlingRequest();
    }
    return open(name, version);
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const stalled = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000);
  const error = await stalled;
  expect(error).toBeInstanceOf(MutationStorageTimeoutError);
  // The failure is the shared neutral retry message. Nothing is deleted and
  // nothing refuses: the very next call attempts the open again and succeeds,
  // and the earlier record survived.
  expect((error as Error).message).toBe("It didn't go through. Try again.");
  expect(await storage.listOutbox()).toEqual([seeded]);
  expect(deleteDatabase).not.toHaveBeenCalled();
  storage.close();
});

// §4's stop barrier: the click-time capture must be the click's FIRST storage
// observation. A retry here would answer ~10s later and could read an epoch a
// sibling tab's Stop bumped during the stall, letting the row commit submitting
// after the Stop. The capture therefore takes a single attempt.
test("a capture read stalled by a timeout rejects rather than reading a Stop committed after the click", async () => {
  const indexedDB = new IDBFactory();
  const open = indexedDB.open.bind(indexedDB);
  const databaseName = "evener-mutation-outbox-capture-timeout";
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) {
      mainOpens += 1;
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
  // The sibling tab's Stop commits while the capture is stalled. There is no
  // retry to answer with the bumped epoch, so the capture must fail.
  await sibling.cancelUnattempted("local:thread-1");
  await vi.advanceTimersByTimeAsync(10_000);
  expect(await capture).toBeInstanceOf(MutationStorageTimeoutError);
  // The Stop really landed: a later read (after the open answers) sees its
  // bumped epoch, but the click-time capture never did.
  expect(await sibling.readStopEpoch("local:thread-1")).toBe(1);
  sender.close();
  sibling.close();
});

test("the retry click's capture rejects on a timed-out open", async () => {
  const indexedDB = new IDBFactory();
  const open = indexedDB.open.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    mainOpens += 1;
    if (mainOpens === 1) return neverSettlingRequest();
    return open(name, version);
  });
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName: "evener-mutation-outbox-release-capture-timeout",
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const capture = storage.getOutboxWithStopEpoch("mutation-1").then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000);
  expect(await capture).toBeInstanceOf(MutationStorageTimeoutError);
  storage.close();
});

test("concurrent operations share one open attempt and both settle successfully", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-concurrent";
  const open = indexedDB.open.bind(indexedDB);
  let mainOpens = 0;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    if (name === databaseName) mainOpens += 1;
    return open(name, version);
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const read = storage.listOutbox();
  const enqueue = storage.enqueueIntent(intent);
  const [before, record] = await Promise.all([read, enqueue]);
  expect(Array.isArray(before)).toBe(true);
  expect(record.intentSequence).toBe(1);
  expect(await storage.listOutbox()).toEqual([record]);
  // Both callers shared one connection: the second did not open its own.
  expect(mainOpens).toBe(1);
  storage.close();
});

test("close() during an open closes the late connection and does not install it", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-close-mid-open";
  const open = indexedDB.open.bind(indexedDB);
  let late: IDBDatabase | undefined;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    const request = open(name, version);
    if (name === databaseName) {
      request.addEventListener("success", () => {
        late = request.result;
      });
      hold = holdIndexedDBEvent(request, "success");
    }
    return request;
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const read = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  if (!hold) throw new Error("open did not reach IndexedDB");
  await hold.reached;
  storage.close();
  hold.release();
  const failure = await read;
  // close() forgot the in-flight open, so its success lands superseded: the
  // connection closes rather than installing, and the call fails.
  expect(failure).toBeInstanceOf(Error);
  expect(late).toBeDefined();
  expect(() => late?.transaction("outbox")).toThrow();
});

test("a stalled upgrade times out, and once the upgrade commits a later operation succeeds", async () => {
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
  // Watch the stalled attempt's open request: its success fires only once the
  // versionchange transaction has committed, which is the signal the upgrade
  // was never aborted and can still land.
  const open = indexedDB.open.bind(indexedDB);
  let abandonedSucceeded = false;
  vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
    const request = open(name, version);
    request.addEventListener("success", () => {
      abandonedSucceeded = true;
    });
    return request;
  });
  const storage = new MutationOutboxIndexedDB({ indexedDB });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let settled = false;
  const failure = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => {
      settled = true;
      return error;
    },
  );
  try {
    await upgrading;
    await vi.advanceTimersByTimeAsync(10_000); // the open watchdog
    await nextRealTask();
    await settleRealTasks(() => settled);
    // The stalled attempt failed, but its upgrade was never aborted.
    expect(await failure).toBeInstanceOf(MutationStorageTimeoutError);
    // Let the versionchange transaction commit: the abandoned connection then
    // closes, the promise is cleared, and a later call opens afresh and
    // succeeds. Recovery is possible - the non-aborting upgrade design holds.
    keepAlive = false;
    await settleRealTasks(() => abandonedSucceeded);
    expect(abandonedSucceeded).toBe(true);
    const record = await storage.enqueueIntent(intent);
    expect(await storage.listOutbox()).toEqual([record]);
  } finally {
    keepAlive = false;
    storage.close();
  }
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
