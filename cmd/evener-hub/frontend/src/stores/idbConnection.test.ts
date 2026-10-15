// @vitest-environment node

import { type IDBDatabase, IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { IDBConnection, IDBUpgradeError, requestResult, transactionCompletion } from "./idbConnection";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

type Diagnostic = { path: string; versionchangeTransaction: boolean };

function openAt(factory: IDBFactory, name: string, version: number): Promise<IDBDatabase> {
  const request = factory.open(name, version);
  return new Promise((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
}

function makeConnection(
  indexedDB: IDBFactory,
  overrides: {
    databaseVersion?: number;
    waitMs?: number;
    upgrade?: (database: IDBDatabase) => void;
    prepare?: (database: IDBDatabase) => Promise<void>;
    errors?: Partial<{
      open: () => Error;
      superseded: () => Error;
      timeout: () => Error;
      blocked: () => Error;
    }>;
    reportDiagnostic?: (path: string, versionchangeTransaction: boolean) => void;
    reportOpenError?: (error: Error) => void;
  } = {},
): IDBConnection {
  return new IDBConnection({
    indexedDB,
    databaseName: "evener-session-cache",
    databaseVersion: overrides.databaseVersion ?? 2,
    waitMs: overrides.waitMs ?? 10_000,
    upgrade: overrides.upgrade ?? (() => {}),
    prepare: overrides.prepare,
    errors: {
      open: overrides.errors?.open ?? (() => new Error("session cache open failed")),
      superseded: overrides.errors?.superseded ?? (() => new Error("session cache open was superseded")),
      timeout: overrides.errors?.timeout ?? (() => new Error("session cache open timed out")),
      blocked: overrides.errors?.blocked,
    },
    reportDiagnostic: overrides.reportDiagnostic ?? (() => {}),
    reportOpenError: overrides.reportOpenError,
  });
}

// Legacy case: sessionCacheIndexedDB.test.ts:22 "production schema advances once and fences the old tab".
// Re-harnessed on IDBConnection: an open advances the stored schema and a live old
// connection is fenced (told the new version) exactly once.
test("an open advances the stored schema and fences the older live connection once", async () => {
  const indexedDB = new IDBFactory();
  const older = await openAt(indexedDB, "evener-session-cache", 1);
  const changes: Array<number | null> = [];
  older.onversionchange = (event) => {
    changes.push(event.newVersion);
    older.close();
  };
  const connection = makeConnection(indexedDB, {
    databaseVersion: 2,
    upgrade: (database) => database.createObjectStore("records", { keyPath: "ref" }),
  });
  try {
    await connection.open();
    const database = await openAt(indexedDB, "evener-session-cache", 2);
    expect(database.version).toBe(2);
    expect(database.objectStoreNames.contains("records")).toBe(true);
    expect(changes).toEqual([2]);
    database.close();
    // A second open installs the already-advanced schema without a new upgrade.
    await connection.open();
    expect(changes).toEqual([2]);
  } finally {
    older.close();
    connection.close();
  }
});

// Legacy case: idbConnection.test.ts:14 "the cache keeps a blocked open pending and uses it when the older connection closes".
test("a blocked open stays pending and installs when the older connection closes", async () => {
  const indexedDB = new IDBFactory();
  const older = await openAt(indexedDB, "evener-session-cache", 1);
  const diagnostics: Diagnostic[] = [];
  const connection = makeConnection(indexedDB, {
    reportDiagnostic: (path, versionchangeTransaction) => diagnostics.push({ path, versionchangeTransaction }),
  });
  try {
    const opened = connection.open();
    void opened.catch(() => {});
    await vi.waitFor(() => expect(diagnostics.some((d) => d.path === "open-blocked")).toBe(true));
    expect(connection.isOpen()).toBe(false);
    older.close();
    const database = await opened;
    expect(connection.isOpen()).toBe(true);
    expect(diagnostics).toEqual([{ path: "open-blocked", versionchangeTransaction: false }]);
    database.close();
  } finally {
    older.close();
    connection.close();
  }
});

// Legacy case: idbConnection.test.ts:50 "a close during the cache's initial sweep cannot install its connection".
test("a close during the initial prepare cannot install its connection, and a later open recovers", async () => {
  const indexedDB = new IDBFactory();
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let prepares = 0;
  const connection = makeConnection(indexedDB, {
    prepare: async () => {
      prepares += 1;
      await gate;
    },
  });
  try {
    const opened = connection.open();
    void opened.catch(() => {});
    await vi.waitFor(() => expect(prepares).toBe(1));
    expect(connection.isOpen()).toBe(false); // the prepare is part of opening
    connection.close();
    release();
    await expect(opened).rejects.toThrow("session cache open was superseded");
    expect(connection.isOpen()).toBe(false);
    const database = await connection.open(); // the failed attempt is not remembered
    expect(connection.isOpen()).toBe(true);
    database.close();
  } finally {
    connection.close();
  }
});

// Legacy case: idbConnection.test.ts:50 "a timeout during the cache's initial sweep cannot install its connection".
test("a timeout during the initial prepare rejects with the timeout diagnostic and installs nothing", async () => {
  const indexedDB = new IDBFactory();
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const diagnostics: Diagnostic[] = [];
  const connection = makeConnection(indexedDB, {
    waitMs: 1_000,
    prepare: () => gate,
    reportDiagnostic: (path, versionchangeTransaction) => diagnostics.push({ path, versionchangeTransaction }),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    const opened = connection.open();
    void opened.catch(() => {});
    await vi.advanceTimersByTimeAsync(1_000);
    await expect(opened).rejects.toThrow("session cache open timed out");
    expect(diagnostics[0]).toEqual({ path: "open-timeout", versionchangeTransaction: false });
    expect(connection.isOpen()).toBe(false);
    release();
    const recovered = await connection.open(); // the failed attempt is not remembered
    expect(connection.isOpen()).toBe(true);
    recovered.close();
  } finally {
    release();
    vi.useRealTimers();
    connection.close();
  }
});

// Legacy case: sessionCacheIndexedDB.test.ts:313 "preserves the committed clear epoch across a database version upgrade".
// Re-harnessed on IDBConnection: a keyed row committed before a version bump is
// still readable after the higher-version open runs its upgrade.
test("a committed keyed row survives a database version upgrade", async () => {
  const indexedDB = new IDBFactory();
  const first = makeConnection(indexedDB, {
    databaseVersion: 1,
    upgrade: (database) => database.createObjectStore("meta", { keyPath: "ref" }),
  });
  const databaseV1 = await first.open();
  const write = databaseV1.transaction("meta", "readwrite");
  write.objectStore("meta").put({ ref: "__clearEpoch", epoch: 1 });
  await transactionCompletion(write);
  first.close();

  const second = makeConnection(indexedDB, { databaseVersion: 2, upgrade: () => {} });
  try {
    const databaseV2 = await second.open();
    const stored = await requestResult(
      databaseV2.transaction("meta", "readonly").objectStore("meta").get("__clearEpoch"),
    );
    expect(stored).toEqual({ ref: "__clearEpoch", epoch: 1 });
    databaseV2.close();
  } finally {
    second.close();
  }
});

// Legacy case: sessionCacheIndexedDB.test.ts:803 "closes a late open success that close() superseded instead of installing it".
test("closes a late open success that close() superseded instead of installing it", async () => {
  const indexedDB = new IDBFactory();
  const connection = makeConnection(indexedDB);
  const open = indexedDB.open.bind(indexedDB);
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(indexedDB, "open").mockImplementation((...args: Parameters<IDBFactory["open"]>) => {
    const request = open(...args);
    hold = holdIndexedDBEvent(request, "success");
    return request;
  });
  try {
    const opened = connection.open();
    void opened.catch(() => {}); // the open succeeds; its success event is held
    if (hold === undefined) throw new Error("the held open never started");
    await hold.reached;
    connection.close(); // supersede the in-flight open before its success is delivered
    hold.release();
    await expect(opened).rejects.toThrow("session cache open was superseded");
    expect(connection.isOpen()).toBe(false); // the late connection was closed, not installed
  } finally {
    connection.close();
  }
});

// Legacy case: sessionCacheIndexedDB.test.ts:822 "reports a superseded attempt's late upgrade as abandoned, and the upgrade still commits".
test("reports a superseded attempt's late upgrade as abandoned, and the upgrade still commits", async () => {
  const indexedDB = new IDBFactory();
  const diagnostics: Diagnostic[] = [];
  let upgrades = 0;
  const connection = makeConnection(indexedDB, {
    upgrade: () => {
      upgrades += 1;
    },
    reportDiagnostic: (path, versionchangeTransaction) => diagnostics.push({ path, versionchangeTransaction }),
  });
  try {
    const opened = connection.open();
    void opened.catch(() => {}); // a real open is in flight
    connection.close(); // supersede it before its upgrade arrives
    await expect(opened).rejects.toThrow("session cache open was superseded");
    expect(upgrades).toBe(1); // the upgrade still ran (Chromium wedges on an aborted versionchange)
    expect(diagnostics).toEqual([{ path: "upgrade-abandoned", versionchangeTransaction: true }]);
  } finally {
    connection.close();
  }
});

// Legacy case: sessionCacheIndexedDB.test.ts:837 "records the abandoned path when an upgrade arrives for an open the watchdog already failed".
test("records the abandoned path when an upgrade arrives for an open the watchdog already failed", async () => {
  const indexedDB = new IDBFactory();
  const request = Object.assign(new EventTarget(), {
    transaction: null,
    result: { objectStoreNames: { contains: () => true } },
  }) as unknown as IDBOpenDBRequest;
  vi.spyOn(indexedDB, "open").mockImplementation(() => request);
  const diagnostics: Diagnostic[] = [];
  const connection = makeConnection(indexedDB, {
    waitMs: 1_000,
    reportDiagnostic: (path, versionchangeTransaction) => diagnostics.push({ path, versionchangeTransaction }),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    const opened = connection.open();
    void opened.catch(() => {});
    await vi.advanceTimersByTimeAsync(1_000);
    expect(diagnostics).toEqual([{ path: "open-timeout", versionchangeTransaction: false }]);
    // The upgrade finally arrives on the abandoned attempt, transaction live.
    Object.defineProperty(request, "transaction", {
      value: { objectStore: () => ({ put: () => undefined }) },
      configurable: true,
    });
    request.dispatchEvent(new Event("upgradeneeded"));
    expect(diagnostics).toEqual([
      { path: "open-timeout", versionchangeTransaction: false },
      { path: "upgrade-abandoned", versionchangeTransaction: true },
    ]);
    await expect(opened).rejects.toThrow("session cache open timed out");
  } finally {
    vi.useRealTimers();
    connection.close();
  }
});

// Legacy case: sessionCacheIndexedDB.test.ts:864 "retires its connection when another tab takes the database to a new version, and the next read re-opens into the fence".
test("retires its connection when another tab takes the database to a new version", async () => {
  const indexedDB = new IDBFactory();
  const diagnostics: Diagnostic[] = [];
  const connection = makeConnection(indexedDB, {
    databaseVersion: 1,
    reportDiagnostic: (path, versionchangeTransaction) => diagnostics.push({ path, versionchangeTransaction }),
  });
  try {
    await connection.open();
    expect(connection.isOpen()).toBe(true);
    const sibling = await openAt(indexedDB, "evener-session-cache", 2); // a sibling tab asks for a higher version
    expect(connection.isOpen()).toBe(false); // this connection was told to retire
    expect(diagnostics).toEqual([{ path: "versionchange-retire", versionchangeTransaction: false }]);
    sibling.close();
  } finally {
    connection.close();
  }
});

// Legacy case: sessionCacheIndexedDB.test.ts:884 "fails closed at the version fence: a newer tab's database version reads as a miss with the fence diagnostic".
test("an open at a version below the stored database reports the version open error", async () => {
  const indexedDB = new IDBFactory();
  const sibling = await openAt(indexedDB, "evener-session-cache", 2);
  const errors: Error[] = [];
  const connection = makeConnection(indexedDB, {
    databaseVersion: 1,
    reportOpenError: (error) => errors.push(error),
  });
  try {
    await expect(connection.open()).rejects.toMatchObject({ name: "VersionError" });
    expect(errors).toHaveLength(1);
    expect(errors[0]?.name).toBe("VersionError");
    expect(connection.isOpen()).toBe(false);
  } finally {
    sibling.close();
    connection.close();
  }
});

// Legacy case: sessionCacheIndexedDB.test.ts:900 "records nothing for an open error that is not the version fence, and still reads as a miss".
test("reports a non-version open error through reportOpenError and never installs", async () => {
  const failure = new Error("a storage-level open failure");
  failure.name = "UnknownError";
  const request = Object.assign(new EventTarget(), { error: failure }) as unknown as IDBOpenDBRequest;
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => request);
  const errors: Error[] = [];
  const connection = makeConnection(indexedDB, { reportOpenError: (error) => errors.push(error) });
  const opened = connection.open();
  void opened.catch(() => {});
  queueMicrotask(() => request.dispatchEvent(new Event("error")));
  await expect(opened).rejects.toBe(failure);
  expect(errors).toEqual([failure]);
  expect(connection.isOpen()).toBe(false);
  connection.close();
});

// Legacy case: sessionCacheIndexedDB.test.ts:405 "returns nothing on a miss, and nothing but not a throw on a stalled or failed open".
test("a stalled open rejects on the watchdog rather than hanging", async () => {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(
    () => Object.assign(new EventTarget(), { transaction: null }) as unknown as IDBOpenDBRequest,
  );
  const connection = makeConnection(indexedDB, { waitMs: 1_000 });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    const opened = connection.open();
    void opened.catch(() => {});
    await vi.advanceTimersByTimeAsync(1_000);
    await expect(opened).rejects.toThrow("session cache open timed out");
  } finally {
    vi.useRealTimers();
    connection.close();
  }
});

// Legacy case: sessionCacheIndexedDB.test.ts:422 "records the open failure through the diagnostic seam and never throws".
test("records the open failure through the diagnostic seam", async () => {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(
    () => Object.assign(new EventTarget(), { transaction: null }) as unknown as IDBOpenDBRequest,
  );
  const diagnostics: Diagnostic[] = [];
  const connection = makeConnection(indexedDB, {
    waitMs: 1_000,
    reportDiagnostic: (path, versionchangeTransaction) => diagnostics.push({ path, versionchangeTransaction }),
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    const opened = connection.open();
    void opened.catch(() => {});
    await vi.advanceTimersByTimeAsync(1_000);
    await expect(opened).rejects.toThrow("session cache open timed out");
    expect(diagnostics).toEqual([{ path: "open-timeout", versionchangeTransaction: false }]);
  } finally {
    vi.useRealTimers();
    connection.close();
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

// The open names a throwing upgrade's own failure (see IDBUpgradeError).
test("a throwing schema upgrade rejects the open with its own failure", async () => {
  const schemaBug = new TypeError("schema bug");
  const connection = new IDBConnection({
    indexedDB: new IDBFactory(),
    databaseName: "evener-upgrade-bug",
    databaseVersion: 1,
    waitMs: 10_000,
    upgrade: () => {
      throw schemaBug;
    },
    errors: {
      open: () => new Error("open failed"),
      superseded: () => new Error("open superseded"),
      timeout: () => new Error("open timed out"),
    },
    reportDiagnostic: () => undefined,
  });
  const open = connection.open();
  await expect(open).rejects.toBeInstanceOf(IDBUpgradeError);
  await expect(open).rejects.toMatchObject({ cause: schemaBug });
});
