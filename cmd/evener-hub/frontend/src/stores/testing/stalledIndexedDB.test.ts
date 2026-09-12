import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
import { holdIndexedDBEvent } from "./stalledIndexedDB";

function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

function openStore(): Promise<IDBDatabase> {
  const open = new IDBFactory().open("stalled-indexeddb-test", 1);
  open.addEventListener("upgradeneeded", () => open.result.createObjectStore("rows", { keyPath: "id" }));
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

test("a read released before its success event still settles", async () => {
  const database = await openStore();
  const request = database.transaction("rows", "readonly").objectStore("rows").getAll();
  const hold = holdIndexedDBEvent(request, "success");
  const rows = requestResult<unknown[]>(request);
  hold.release();
  expect(await rows).toEqual([]);
});
