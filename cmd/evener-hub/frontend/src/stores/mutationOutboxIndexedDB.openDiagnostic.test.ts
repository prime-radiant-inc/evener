// @vitest-environment node

// The open diagnostic (kata: IndexedDB mutation-outbox open failures). The
// wedge these records are for - a Chromium connection coordinator stuck so
// open() never fires success, error, or blocked (crbug 40278488) - could not be
// reproduced in tests, so these tests pin the instrumentation, not the wedge:
// each of the four failure-prone paths records one diagnostic, and a healthy
// open records none. The reporter is injected (the class's own seam) so the
// assertions read records, never console text.

import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { MutationIntent } from "./mutationOutbox";
import type { MutationOutboxOpenDiagnostic } from "./mutationOutboxIndexedDB";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError, warnOpenDiagnostic } from "./mutationOutboxIndexedDB";
import { neverSettlingRequest } from "./testing/stalledIndexedDB";

const DATABASE_NAME = "evener-mutation-outbox";
// The adapter's own schema fence: DATABASE_VERSION in mutationOutboxIndexedDB.ts.
const VERSION = 3;

const intent: MutationIntent = {
  targetRef: "local:thread-1",
  method: "turn/steer",
  payload: { ref: "local:thread-1", input: [{ type: "text", text: "one message" }] },
  attachments: [],
  optimisticDisplay: null,
};

function collect(): { diagnostics: MutationOutboxOpenDiagnostic[]; report: (d: MutationOutboxOpenDiagnostic) => void } {
  const diagnostics: MutationOutboxOpenDiagnostic[] = [];
  return { diagnostics, report: (diagnostic) => diagnostics.push(diagnostic) };
}

// A real open over fake-indexeddb, for the connections this adapter must share
// the database with (an old-version holder, a higher-version upgrade). Rejects
// on error or blocked so a surprise in setup is a named failure, not a timeout.
function openRequest(indexedDB: IDBFactory, name: string, version: number): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(name, version);
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("open failed")), { once: true });
    request.addEventListener("blocked", () => reject(new Error("open blocked")), { once: true });
  });
}

// One real macrotask hop, off the faked timers: fake-indexeddb delivers open and
// versionchange events on such a task, which a timer advance does not reach.
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

async function settleRealTasks(ready: () => boolean): Promise<void> {
  for (let i = 0; i < 20 && !ready(); i += 1) await nextRealTask();
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

test("a healthy open records no diagnostic", async () => {
  const { diagnostics, report } = collect();
  const storage = new MutationOutboxIndexedDB({ indexedDB: new IDBFactory(), onOpenDiagnostic: report });
  const record = await storage.enqueueIntent(intent);
  expect(await storage.listOutbox()).toEqual([record]);
  expect(diagnostics).toEqual([]);
  storage.close();
});

test("the open watchdog records the timeout path once", async () => {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  const { diagnostics, report } = collect();
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const storage = new MutationOutboxIndexedDB({ indexedDB, onOpenDiagnostic: report });
  const failure = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000);
  expect(await failure).toBeInstanceOf(MutationStorageTimeoutError);
  expect(diagnostics).toEqual([
    { database: DATABASE_NAME, version: VERSION, path: "open-timeout", versionchangeTransaction: false },
  ]);
  storage.close();
});

test("an upgradeneeded for a superseded attempt records the abandoned path, and the upgrade still commits", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-superseded-upgrade";
  const { diagnostics, report } = collect();
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName, onOpenDiagnostic: report });
  // Supersede the in-flight open before its upgrade arrives: close() forgets
  // the attempt, so the later upgradeneeded runs for a promise nobody owns.
  // The upgrade must still commit - the record is the only effect.
  const read = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  storage.close();
  expect(await read).toBeInstanceOf(Error);
  expect(diagnostics).toContainEqual({
    database: databaseName,
    version: VERSION,
    path: "upgrade-abandoned",
    versionchangeTransaction: true,
  });
  // The schema the superseded attempt created is durable: a fresh adapter opens
  // it without an upgrade and can write.
  const reopened = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const record = await reopened.enqueueIntent(intent);
  expect(await reopened.listOutbox()).toEqual([record]);
  reopened.close();
});

test("a blocked upgrade request records the blocked path", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-blocked";
  // An older-version connection this adapter never controls holds the lock, so
  // its version-3 open is blocked.
  const older = await openRequest(indexedDB, databaseName, 1);
  const { diagnostics, report } = collect();
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName, onOpenDiagnostic: report });
  const failure = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await settleRealTasks(() => diagnostics.length > 0);
  expect(diagnostics).toContainEqual({
    database: databaseName,
    version: VERSION,
    path: "open-blocked",
    versionchangeTransaction: false,
  });
  expect(await failure).toBeInstanceOf(Error);
  older.close();
  storage.close();
});

test("a versionchange retirement records the retire path", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-versionchange";
  const { diagnostics, report } = collect();
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName, onOpenDiagnostic: report });
  // Install the connection and its versionchange listener.
  await storage.listOutbox();
  // A sibling tab asks for a higher version; this connection is told to retire.
  const upgraded = openRequest(indexedDB, databaseName, VERSION + 1);
  await settleRealTasks(() => diagnostics.length > 0);
  expect(diagnostics).toContainEqual({
    database: databaseName,
    version: VERSION,
    path: "versionchange-retire",
    versionchangeTransaction: false,
  });
  await upgraded;
  storage.close();
});

test("the default reporter warns on the console with a stable prefix", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  warnOpenDiagnostic({
    database: DATABASE_NAME,
    version: VERSION,
    path: "open-timeout",
    versionchangeTransaction: false,
  });
  expect(warn).toHaveBeenCalledWith("evener mutation outbox:", {
    database: DATABASE_NAME,
    version: VERSION,
    path: "open-timeout",
    versionchangeTransaction: false,
  });
});

test("the constructor's default reporter is silent under the test environment", async () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const storage = new MutationOutboxIndexedDB({ indexedDB });
  const failure = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000);
  expect(await failure).toBeInstanceOf(MutationStorageTimeoutError);
  // The default reporter is gated off under MODE=test, so a test that does not
  // inject a reporter records nothing; the timeout still fails the call.
  expect(warn).not.toHaveBeenCalled();
  storage.close();
});

test("a throwing diagnostic reporter does not change the open outcome", async () => {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const storage = new MutationOutboxIndexedDB({
    indexedDB,
    onOpenDiagnostic: () => {
      throw new Error("diagnostic sink failed");
    },
  });
  const failure = storage.listOutbox().then(
    () => undefined,
    (error: unknown) => error,
  );
  await vi.advanceTimersByTimeAsync(10_000);
  expect(await failure).toBeInstanceOf(MutationStorageTimeoutError);
  storage.close();
});
