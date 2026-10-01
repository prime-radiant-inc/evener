// @vitest-environment node

import { IDBDatabase, IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { transactionCompletion } from "./idbConnection";
import { SessionCacheIndexedDB, type SessionCacheOpenDiagnostic } from "./sessionCacheIndexedDB";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

test("the cache keeps a blocked open pending and uses it when the older connection closes", async () => {
  const indexedDB = new IDBFactory();
  const older = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open("evener-session-cache", 1);
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
  let reportBlocked: () => void = () => {};
  const blocked = new Promise<void>((resolve) => {
    reportBlocked = resolve;
  });
  const diagnostics: SessionCacheOpenDiagnostic[] = [];
  const cache = new SessionCacheIndexedDB({
    indexedDB,
    databaseVersion: 2,
    onOpenDiagnostic: (diagnostic) => {
      diagnostics.push(diagnostic);
      if (diagnostic.path === "open-blocked") reportBlocked();
    },
  });
  try {
    const count = cache.count();
    await blocked;
    expect(cache.isOpen()).toBe(false);
    older.close();
    expect(await count).toBe(0);
    expect(cache.isOpen()).toBe(true);
    expect(diagnostics).toEqual([
      { database: "evener-session-cache", version: 2, path: "open-blocked", versionchangeTransaction: false },
    ]);
  } finally {
    older.close();
    cache.close();
  }
});

test.each(["close", "timeout"])("a %s during the cache's initial sweep cannot install its connection", async (end) => {
  const cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory() });
  type Hold = ReturnType<typeof holdIndexedDBEvent>;
  let reportStarted: (hold: Hold) => void = () => {};
  const started = new Promise<Hold>((resolve) => {
    reportStarted = resolve;
  });
  let database: IDBDatabase | undefined;
  const transact = IDBDatabase.prototype.transaction;
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementationOnce(function (this: IDBDatabase, ...args) {
    database = this;
    const transaction = transact.apply(this, args);
    reportStarted(holdIndexedDBEvent(transaction, "complete"));
    return transaction;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const count = cache.count();
  const hold = await started;
  try {
    await hold.reached;
    expect(cache.isOpen()).toBe(false); // the sweep's completion is part of opening
    if (end === "close") cache.close();
    else await vi.advanceTimersByTimeAsync(10_000);
    hold.release();
    expect(await count).toBeUndefined();
    expect(cache.isOpen()).toBe(false);
    expect(database).toBeDefined();
    expect(() => database?.transaction("records")).toThrow();
    expect(await cache.count()).toBe(0); // the failed attempt is not remembered
    expect(cache.isOpen()).toBe(true);
  } finally {
    hold.release();
    cache.close();
  }
});

test("outbox transaction completion rejects on error without waiting for abort", async () => {
  const error = new DOMException("request failure", "UnknownError");
  const transaction = Object.assign(new EventTarget(), { error }) as unknown as IDBTransaction;
  const completion = transactionCompletion(transaction);
  transaction.dispatchEvent(new Event("error"));
  await expect(completion).rejects.toBe(error);
});

test.each(["complete", "abort"])("cache transaction completion waits past error for %s", async (terminal) => {
  const transaction = Object.assign(new EventTarget(), { error: null }) as unknown as IDBTransaction;
  const completion = transactionCompletion(transaction, "abort");
  let settled = false;
  const observed = completion.then(
    () => {
      settled = true;
      return "committed";
    },
    (error: unknown) => {
      settled = true;
      return error;
    },
  );
  transaction.dispatchEvent(new Event("error"));
  await Promise.resolve(); // any rejection at the error event has now reached the observer
  expect(settled).toBe(false);
  transaction.dispatchEvent(new Event(terminal));
  if (terminal === "complete") expect(await observed).toBe("committed");
  else expect(await observed).toEqual(new Error("IndexedDB transaction aborted"));
});
