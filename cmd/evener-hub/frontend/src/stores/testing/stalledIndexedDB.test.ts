// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { holdIndexedDBEvent, holdNextWriteTransaction } from "./stalledIndexedDB";

function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

function openStore(): Promise<IDBDatabase> {
  const open = new IDBFactory().open("stalled-indexeddb-test", 1);
  open.addEventListener("upgradeneeded", () => {
    open.result.createObjectStore("rows", { keyPath: "id" });
    open.result.createObjectStore("other", { keyPath: "id" });
  });
  return requestResult(open);
}

test("release delivers an event the hold already caught", () => {
  const target = new EventTarget();
  const hold = holdIndexedDBEvent(target, "success");
  let delivered = 0;
  target.addEventListener("success", () => {
    delivered += 1;
  });
  target.dispatchEvent(new Event("success"));
  expect(delivered).toBe(0);
  hold.release();
  expect(delivered).toBe(1);
});

// Release means "stop holding", not "deliver whatever has arrived so far". The
// interception outlives mockRestore for every listener already registered
// through it, so an event still in flight when a test releases is a callback
// nobody ever calls - and, for an IndexedDB request, a promise that never
// settles. That is what turned one stalled read in Composer.integration's
// recovery-projection test into a 5s timeout inside act() (issue #1187).
test("an event arriving after release still reaches its listener", () => {
  const target = new EventTarget();
  const hold = holdIndexedDBEvent(target, "success");
  let delivered = 0;
  target.addEventListener("success", () => {
    delivered += 1;
  });
  hold.release();
  target.dispatchEvent(new Event("success"));
  expect(delivered).toBe(1);
});

// reached is the "the held event arrived" signal, and arriving is not the same
// question as being held: a caller awaiting it after a release would otherwise
// wait for a signal that can never come, even though its listener already ran.
test("an event arriving after release still resolves reached", async () => {
  const target = new EventTarget();
  const hold = holdIndexedDBEvent(target, "success");
  target.addEventListener("success", () => undefined);
  hold.release();
  target.dispatchEvent(new Event("success"));
  await expect(hold.reached).resolves.toBeUndefined();
});

test("a read released before its success event still settles", async () => {
  const database = await openStore();
  const request = database.transaction("rows", "readonly").objectStore("rows").getAll();
  const hold = holdIndexedDBEvent(request, "success");
  const rows = requestResult<unknown[]>(request);
  hold.release();
  expect(await rows).toEqual([]);
});

function completion(transaction: IDBTransaction): { done: Promise<void>; isDone: () => boolean } {
  let finished = false;
  const done = new Promise<void>((resolve) => {
    transaction.addEventListener("complete", () => {
      finished = true;
      resolve();
    });
  });
  return { done, isDone: () => finished };
}

function writeRow(database: IDBDatabase, stores: string | string[], id: string) {
  const transaction = database.transaction(stores, "readwrite");
  transaction.objectStore("rows").put({ id });
  return completion(transaction);
}

afterEach(() => {
  vi.restoreAllMocks();
});

// The storage's transactions differ by mode and store scope, and the helper
// holds exactly one: the first readwrite transaction over exactly the stores it
// names. A readonly transaction and a write over a wider scope are not held,
// and neither is a later write over the same stores.
test("holdNextWriteTransaction holds only the first readwrite transaction over exactly the named stores", async () => {
  const database = await openStore();
  const held = holdNextWriteTransaction(["rows"]);

  const read = completion(database.transaction("rows", "readonly"));
  const wider = writeRow(database, ["rows", "other"], "wider");
  const target = writeRow(database, "rows", "target");
  const later = writeRow(database, "rows", "later");

  await held.reached;
  expect(read.isDone()).toBe(true);
  expect(wider.isDone()).toBe(true);
  expect(target.isDone()).toBe(false);

  held.release();
  expect(target.isDone()).toBe(true);
  await later.done;
});

// The composer's commit tests hold the enqueue write, whose scope is the four
// mutation stores. The sites these replaced matched "a readwrite transaction
// whose scope contains sequences"; naming the enqueue scope exactly must hold
// that write and not a narrower one that merely shares the sequence store (a
// cancel or release). This pins the exact scope the converted sites depend on.
test("holdNextWriteTransaction holds the exact scope, not an earlier narrower write sharing a store", async () => {
  const database = await openStore();
  const held = holdNextWriteTransaction(["rows", "other"]);

  const narrower = writeRow(database, "rows", "narrower");
  const target = writeRow(database, ["rows", "other"], "target");

  await held.reached;
  expect(narrower.isDone()).toBe(true);
  expect(target.isDone()).toBe(false);

  held.release();
  expect(target.isDone()).toBe(true);
});
