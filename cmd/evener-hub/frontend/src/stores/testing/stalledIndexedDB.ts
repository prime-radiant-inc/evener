import { IDBDatabase, IDBFactory } from "fake-indexeddb";
import { vi } from "vitest";

// A request that never fires success, error, or blocked: the wedged
// connection-coordinator shape, where open()/deleteDatabase() return and then
// no event ever arrives.
export function neverSettlingRequest(): IDBOpenDBRequest {
  return new EventTarget() as unknown as IDBOpenDBRequest;
}

// An open request another tab's older-version connection blocks: it fires
// "blocked" on the next task and nothing else, the shape a tab still holding
// the previous schema produces for an upgrade.
export function blockedUpgradeRequest(): IDBOpenDBRequest {
  const request = Object.assign(new EventTarget(), { transaction: null });
  setTimeout(() => request.dispatchEvent(new Event("blocked")), 0);
  return request as unknown as IDBOpenDBRequest;
}

// A fake-indexeddb factory whose open() returns the never-settling request:
// neverSettlingRequest() alone IS the dead open request, and a wedged-open
// adapter needs a factory handing that request out.
export function neverSettlingFactory(): IDBFactory {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  return indexedDB;
}

// Delete the named database between tests, so no row one test seeded survives
// into the next one. "blocked" is a bed bug — a connection the test left
// open — not an outcome to wait out, so it rejects like an error.
export function deleteIndexedDatabase(name: string): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(name);
    request.addEventListener("success", () => resolve(), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
    request.addEventListener("blocked", () => reject(new Error(`${name} database deletion blocked`)), {
      once: true,
    });
  });
}

// Bump the session cache's reserved epoch row behind the adapter's back: the
// durable fact a sibling tab's clear committed, whose channel message this
// tab never receives. The production bumper is clear() itself, so a test
// moving the row this way proves a scheduled write reads it before
// committing anything. One readwrite transaction over the meta store; the
// connection closes once it commits.
export function bumpCacheEpochRow(indexedDB: IDBFactory, epoch: number): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const request = indexedDB.open("evener-session-cache", 1);
    request.addEventListener(
      "success",
      () => {
        const database = request.result;
        const tx = database.transaction("meta", "readwrite");
        tx.objectStore("meta").put({ ref: "__clearEpoch", epoch });
        tx.addEventListener(
          "complete",
          () => {
            database.close();
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

// Hold browser callbacks while fake-indexeddb performs the real transaction.
// This distinguishes an unobserved commit from a write that can still abort.
export function holdIndexedDBEvent(target: EventTarget, type: string) {
  const held: (() => void)[] = [];
  let released = false;
  let observed: (() => void) | undefined;
  const reached = new Promise<void>((resolve) => {
    observed = resolve;
  });
  const add = target.addEventListener.bind(target);
  const spy = vi.spyOn(target, "addEventListener").mockImplementation((eventType, listener, options) => {
    if (eventType !== type || !listener) return add(eventType, listener, options);
    add(
      eventType,
      (event) => {
        const deliver = () => {
          if (typeof listener === "function") listener.call(target, event);
          else listener.handleEvent(event);
        };
        // Release means "stop holding", not "deliver what has already
        // arrived": restoring the spy stops NEW listeners from being wrapped,
        // but every listener registered while the hold was up keeps this
        // wrapper for good. An event still in flight at release time would
        // otherwise land in a queue nobody drains again, which for an
        // IndexedDB request is a promise that never settles (issue #1187).
        // reached answers "has the event arrived", which a release does not
        // change, so it is signalled on both paths.
        observed?.();
        if (released) {
          deliver();
          return;
        }
        held.push(deliver);
      },
      options,
    );
  });
  return {
    reached,
    release() {
      released = true;
      spy.mockRestore();
      for (const deliver of held.splice(0)) deliver();
    },
  };
}

// Holds the next readwrite transaction opened over exactly `stores`:
// fake-indexeddb commits it, but its "complete" event, and with it the
// storage call that is waiting on that event, stays held until release().
// `reached` resolves once the held event has arrived, which is when the
// write is durably done yet still in flight for everything awaiting it.
export function holdNextWriteTransaction(stores: readonly string[]) {
  const transact = IDBDatabase.prototype.transaction;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let markReached: (() => void) | undefined;
  const reached = new Promise<void>((resolve) => {
    markReached = resolve;
  });
  const spy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const transaction = transact.apply(this, args);
    if (
      !hold &&
      transaction.mode === "readwrite" &&
      transaction.objectStoreNames.length === stores.length &&
      stores.every((store) => transaction.objectStoreNames.contains(store))
    ) {
      hold = holdIndexedDBEvent(transaction, "complete");
      void hold.reached.then(() => markReached?.());
    }
    return transaction;
  });
  return {
    reached,
    release() {
      spy.mockRestore();
      hold?.release();
    },
  };
}

// One real macrotask hop, off the faked timers: MessageChannel is a task the
// setTimeout fake does not touch. fake-indexeddb delivers open, delete and
// versionchange events on such a task, which a fake-timer advance does not
// reach.
export function nextRealTask(): Promise<void> {
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

// Yield to the real task queue until `ready()`, bounded, so parked work can
// progress without its fake watchdog firing first. The bound is a tripwire, not
// the mechanism.
export async function settleRealTasks(ready: () => boolean): Promise<void> {
  for (let i = 0; i < 20 && !ready(); i += 1) await nextRealTask();
}
