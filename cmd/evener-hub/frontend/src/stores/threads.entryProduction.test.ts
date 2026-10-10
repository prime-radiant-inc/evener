import {
  cachedSessionRecord,
  currentTranscriptState,
  hydrateThread,
  type ThreadReadResponse,
  type Turn,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { entryWriteAdmissionFor } from "./entryWriteAdmission";
import { requestResult, transactionCompletion } from "./idbConnection";
import { settleProjectionWorkForTests } from "./projectionWork";
import { type CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { entryHistoryKey, SESSION_CACHE_DATABASE_NAME } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdNextTransaction } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  clearCachedSessions,
  countCachedSessions,
  installEntryTranscriptFixtureForTests,
  markCacheSessionsDeleted,
  resetThreadsStoreForTests,
  setCacheChannelFactoryForTests,
  settleEntryTranscriptForTests,
  subscribeCacheWrites,
  threadsStore,
} from "./threads";

let factory: IDBFactory;
const caches: SessionEntryCache[] = [];
beforeEach(() => {
  factory = new IDBFactory();
  vi.stubGlobal("indexedDB", factory);
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  resetThreadsStoreForTests();
});
afterEach(() => {
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  for (const cache of caches.splice(0)) cache.close();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function cache(): SessionEntryCache {
  const result = new SessionEntryCache({
    databaseName: SESSION_CACHE_DATABASE_NAME,
    indexedDB: factory,
    keyRange: IDBKeyRange,
  });
  caches.push(result);
  return result;
}
const identity = { clientId: "http://localhost:3000", hostId: "local", sessionId: "session-a", incarnation: "inc-a" };
const historyKey = entryHistoryKey(identity);
function turn(id: string, entry = 1, text = id): Turn {
  return {
    id,
    status: "completed",
    itemsView: "full",
    version: 1,
    items: [
      { id, transcriptKey: id, turnId: id, version: 1, type: "agentMessage", text, position: { entry, item: 0 } },
    ],
  };
}
function response(ref = "local:a", turns = [turn("recorded")]): ThreadReadResponse {
  return {
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-a", length: 3 },
    requestGeneration: 1,
    thread: {
      id: "thread-a",
      sessionId: "session-a",
      preview: "",
      ephemeral: false,
      modelProvider: "p",
      createdAt: 0,
      updatedAt: 0,
      status: { type: "idle" },
      cwd: "/fixture",
      cliVersion: "test",
      source: "evener",
      turns,
      evener: {
        ref,
        instanceId: "thread-a",
        queue: { revision: 0 },
        capabilities: {
          send: true,
          steer: true,
          interrupt: true,
          compact: true,
          clear: true,
          forkFromTurn: true,
          shutdown: true,
          changeModel: true,
          changeVisionModel: true,
          queue: true,
          goal: true,
          sharedNotes: true,
          rename: true,
        },
      },
    },
  };
}
function connect(read = response()): FakeClient {
  const client = new FakeClient("ready");
  client.on("thread/read", () => read);
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  return client;
}

it("production cache writes and reopens keyed entries on the real database", async () => {
  const first = cache();
  const item = turn("disk-row").items?.[0];
  expect(item).toBeDefined();
  if (!item) throw new Error("missing fixture item");
  expect(
    await first.commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [{ historyKey, transcriptKey: "disk-row", position: { entry: 1, item: 0 }, version: 1, item }],
    }),
  ).toMatchObject({ outcome: "committed" });
  first.close();
  expect((await cache().getLatest(historyKey, 1))[0]?.item).toMatchObject({ text: "disk-row" });
  expect(await factory.databases()).toEqual([{ name: SESSION_CACHE_DATABASE_NAME, version: 3 }]);
});

it("entry store repairs aggregate overflow through an automatic authoritative read", async () => {
  const restore = installBackgroundTranscriptOptionsForTests({ pendingBytes: 2048 });
  const storage = cache();
  const fixture = installEntryTranscriptFixtureForTests({
    ref: "local:a",
    cache: storage,
    clientId: identity.clientId,
    hostId: identity.hostId,
  });
  const client = connect();
  let reads = 0;
  let latest = response();
  client.on("thread/read", (params) => {
    reads += 1;
    return { ...latest, requestGeneration: params.requestGeneration };
  });
  const owner = {};
  const bodies = [{ key: {}, bytes: 1900 }];
  const admission = entryWriteAdmissionFor(storage);
  try {
    await threadsStore.getState().ensureThread("local:a");
    await fixture.settled();
    const before = reads;
    expect(admission.retain(bodies, owner, "writing")).toBe(true);
    latest = response("local:a", [turn("recorded"), turn("recovered", 2, "r".repeat(300))]);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    client.emitNotification({
      method: "history/updated",
      params: {
        ref: "local:a",
        threadId: "thread-a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-a", length: 3 },
        turns: [turn("recovered", 2, "r".repeat(300))],
        items: turn("recovered", 2, "r".repeat(300)).items,
      },
    });
    expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).toContain("recovered");
    expect(admission.snapshotForTesting().retainedBytes).toBe(1900);
    expect(vi.getTimerCount()).toBe(1);
    admission.release(bodies, owner);
    await vi.advanceTimersByTimeAsync(250);
    expect(reads).toBe(before + 1);
    vi.useRealTimers();
    await settleEntryTranscriptForTests("local:a");
    expect((await storage.getLatest(historyKey, 10)).map((row) => row.transcriptKey)).toContain("recovered");
  } finally {
    vi.useRealTimers();
    admission.release(bodies, owner);
    fixture.dispose();
    restore();
  }
});

it("entry store and cache share one clock for metadata and TTL", async () => {
  const now = Date.now() + 15 * 24 * 60 * 60 * 1000;
  const storage = new SessionEntryCache({
    databaseName: SESSION_CACHE_DATABASE_NAME,
    indexedDB: factory,
    keyRange: IDBKeyRange,
    now: () => now,
  });
  caches.push(storage);
  const fixture = installEntryTranscriptFixtureForTests({
    ref: "local:a",
    cache: storage,
    clientId: identity.clientId,
    hostId: identity.hostId,
  });
  try {
    connect();
    await threadsStore.getState().ensureThread("local:a");
    await fixture.settled();
    expect((await storage.getLatest(historyKey, 1)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
    expect(await storage.getSessionMetadata(historyKey)).toMatchObject({ savedAt: now, accessedAt: now });
  } finally {
    fixture.dispose();
  }
});

it("ordinary browser routes reads notifications paging reopen reconnect and final release through entries", async () => {
  const storage = cache();
  const ref = "local:a";
  const client = connect();
  let reads = 0;
  let latest = response(ref, [turn("latest", 2)]);
  latest.olderCursor = "older";
  client.on("thread/read", (params) => {
    reads += 1;
    return { ...latest, requestGeneration: params.requestGeneration };
  });
  let pages = 0;
  client.on("thread/turns/list", () => {
    pages += 1;
    return { data: [turn("older", 1)], bootGeneration: "1", epoch: 1, snapshot: latest.snapshot };
  });
  const lease = acquireBackgroundThread(ref);
  await lease.ready;
  await settleEntryTranscriptForTests(ref);
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["latest"]);
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().loadOlderTurns(ref);
  expect(pages).toBe(1);
  await settleEntryTranscriptForTests(ref);
  client.emitNotification({
    method: "history/updated",
    params: {
      ref,
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("pushed", 3).items,
    },
  });
  await settleEntryTranscriptForTests(ref);
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["older", "latest", "pushed"]);
  threadsStore.getState().releaseThread(ref);
  await settleEntryTranscriptForTests(ref);
  expect(JSON.stringify(threadsStore.getState().threads.get(ref))).not.toContain('"text":"pushed"');
  const beforeOpen = reads;
  await threadsStore.getState().ensureThread(ref);
  expect(reads).toBe(beforeOpen);
  expect(
    threadsStore
      .getState()
      .threads.get(ref)
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toContain("older");
  latest = response(ref, [turn("latest", 2), turn("pushed", 3), turn("reconnected", 4)]);
  latest.snapshot = { incarnation: "inc-a", length: 5 };
  client.emitStateChange("reconnecting");
  client.emitStateChange("ready");
  await settleEntryTranscriptForTests(ref);
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toContain("reconnected");
  threadsStore.getState().releaseThread(ref);
  expect(await lease.release()).toEqual({ kind: "saved" });
  expect(threadsStore.getState().threads.has(ref)).toBe(false);
  expect(await factory.databases()).toEqual([
    { name: "evener-mutation-outbox", version: 3 },
    { name: SESSION_CACHE_DATABASE_NAME, version: 3 },
  ]);
});

async function legacyCounts(): Promise<number[]> {
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = factory.open(SESSION_CACHE_DATABASE_NAME);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  try {
    const transaction = database.transaction(["records", "current"], "readonly");
    const done = transactionCompletion(transaction);
    const result = await Promise.all([
      requestResult(transaction.objectStore("records").count()),
      requestResult(transaction.objectStore("current").count()),
    ]);
    await done;
    return result;
  } finally {
    database.close();
  }
}

it("ordinary settings count and write notifications describe entries without legacy writers", async () => {
  connect();
  const counts: Array<Promise<number | undefined>> = [];
  const unsubscribe = subscribeCacheWrites(() => counts.push(countCachedSessions()));
  try {
    const lease = acquireBackgroundThread("local:a");
    await lease.ready;
    await settleEntryTranscriptForTests("local:a");
    expect(await countCachedSessions()).toBe(1);
    expect(counts.length).toBeGreaterThan(0);
    expect(await Promise.all(counts)).toEqual(counts.map(() => 1));
    expect(await legacyCounts()).toEqual([0, 0]);
    expect(await lease.release()).toEqual({ kind: "saved" });
    expect(await legacyCounts()).toEqual([0, 0]);
  } finally {
    unsubscribe();
  }
});

it("ordinary Clear removes entries once and suppresses the held cache lifetime without losing live display", async () => {
  const storage = cache();
  const client = connect();
  const ref = "local:a";
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  await threadsStore.getState().ensureThread(ref);
  await settleEntryTranscriptForTests(ref);
  expect(await countCachedSessions()).toBe(1);
  expect(await clearCachedSessions()).toEqual({ committed: true });
  expect(await storage.getClearEpoch()).toBe(1);
  expect(await storage.getLatest(key, 10)).toEqual([]);
  expect(await countCachedSessions()).toBe(0);
  expect(
    threadsStore
      .getState()
      .threads.get(ref)
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toContain("recorded");
  client.emitNotification({
    method: "history/updated",
    params: {
      ref,
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("after-clear", 2).items,
    },
  });
  await settleEntryTranscriptForTests(ref);
  expect(
    threadsStore
      .getState()
      .threads.get(ref)
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toContain("after-clear");
  expect(await storage.getLatest(key, 10)).toEqual([]);
  await threadsStore.getState().releaseThreadAndWait(ref);
  await threadsStore.getState().ensureThread(ref);
  await settleEntryTranscriptForTests(ref);
  expect(await countCachedSessions()).toBe(1);
});

it("an aborted ordinary Clear preserves disk rows and the held entry route resumes writes", async () => {
  const storage = cache();
  const client = connect();
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const originalClear = IDBObjectStore.prototype.clear;
  const fault = vi.spyOn(IDBObjectStore.prototype, "clear").mockImplementationOnce(function (this: IDBObjectStore) {
    this.transaction.abort();
    return originalClear.call(this);
  });
  expect(await clearCachedSessions()).toEqual({ committed: false });
  fault.mockRestore();
  expect(await storage.getClearEpoch()).toBe(0);
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("after-abort", 2).items,
    },
  });
  await settleEntryTranscriptForTests("local:a");
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded", "after-abort"]);
});

it("ordinary first open migrates a legacy blob once as stale seeds and atomically removes its source", async () => {
  const legacy = new SessionCacheIndexedDB({ indexedDB: factory });
  try {
    const seeded = cachedSessionRecord(
      hydrateThread(response("local:a", [turn("legacy-older", 0)]), "local:a", Date.now()),
      Date.now(),
    );
    if (!seeded) throw new Error("fixture must encode a legacy session");
    expect((await legacy.put(seeded, 0, Date.now())).outcome).toBe("written");
    const storage = cache();
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    expect(await legacyCounts()).toEqual([1, 0]);
    connect(response("local:a", [turn("live-newer", 2)]));
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["legacy-older", "live-newer"]);
    expect(await legacyCounts()).toEqual([0, 0]);
    const window = await storage.getOlderWindow(key, { entry: 2, item: 0 }, 10);
    expect(window.rows).toEqual([]);
    expect(window.coverage.every((range) => range.start === null || range.start.entry >= 2)).toBe(true);
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["legacy-older", "live-newer"]);
  } finally {
    legacy.close();
  }
});

it("ordinary migration keeps an aborted source intact and retries the next explicit open", async () => {
  const legacy = new SessionCacheIndexedDB({ indexedDB: factory });
  try {
    const seeded = cachedSessionRecord(
      hydrateThread(response("local:a", [turn("retry-seed", 0)]), "local:a", Date.now()),
      Date.now(),
    );
    if (!seeded) throw new Error("fixture must encode a legacy session");
    expect((await legacy.put(seeded, 0, Date.now())).outcome).toBe("written");
    const originalPut = IDBObjectStore.prototype.put;
    let aborted = false;
    const fault = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (
      this: IDBObjectStore,
      value,
      key,
    ) {
      if (!aborted && this.name === "transcriptItems" && value.transcriptKey === "retry-seed") {
        aborted = true;
        this.transaction.abort();
      }
      return originalPut.call(this, value, key);
    });
    connect(response("local:a", [turn("live-newer", 2)]));
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect(aborted).toBe(true);
    fault.mockRestore();
    expect(await legacyCounts()).toEqual([1, 0]);
    const storage = cache();
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["live-newer"]);
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect(await legacyCounts()).toEqual([0, 0]);
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["retry-seed", "live-newer"]);
  } finally {
    legacy.close();
  }
});

it("ordinary migration isolates a legacy incarnation from the current live repository", async () => {
  const legacy = new SessionCacheIndexedDB({ indexedDB: factory });
  try {
    const old = response("local:a", [turn("old-incarnation", 0)]);
    old.snapshot = { incarnation: "old-incarnation", length: 3 };
    const seeded = cachedSessionRecord(hydrateThread(old, "local:a", Date.now()), Date.now());
    if (!seeded) throw new Error("fixture must encode a legacy session");
    expect((await legacy.put(seeded, 0, Date.now())).outcome).toBe("written");
    connect();
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    const storage = cache();
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    const oldKey = entryHistoryKey({ ...identity, clientId: location.origin, incarnation: "old-incarnation" });
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
    expect((await storage.getLatest(oldKey, 10)).map((row) => row.transcriptKey)).toEqual(["old-incarnation"]);
    expect(await legacyCounts()).toEqual([0, 0]);
    expect(await countCachedSessions()).toBe(1);
  } finally {
    legacy.close();
  }
});

it("ordinary deletion removes bound histories and durably refuses the captured pre-delete write", async () => {
  const storage = cache();
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const bound = { ...identity, clientId: location.origin };
  const key = entryHistoryKey(bound);
  const oldIdentity = { ...bound, incarnation: "older-incarnation" };
  const oldKey = entryHistoryKey(oldIdentity);
  expect(
    await storage.commitBatch({
      historyKey: oldKey,
      clearEpoch: 0,
      items: [],
      sessionMetadata: {
        historyKey: oldKey,
        requestedRefs: [{ clientId: bound.clientId, ref: "local:a" }],
        resolvedIdentity: oldIdentity,
        savedAt: storage.now(),
        accessedAt: storage.now(),
        formatVersion: 3,
      },
    }),
  ).toMatchObject({ outcome: "committed" });
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
  markCacheSessionsDeleted(["local:a"]);
  await settleProjectionWorkForTests();
  expect(await storage.getLatest(key, 10)).toEqual([]);
  expect(await storage.getHistoryGeneration(key)).toBe(1);
  expect(await storage.getHistoryGeneration(oldKey)).toBe(1);
  expect(await countCachedSessions()).toBe(0);
  const item = turn("stale-after-delete", 2).items?.[0];
  if (!item) throw new Error("missing positioned item");
  expect(
    await storage.commitBatch({
      historyKey: key,
      historyGeneration: 0,
      clearEpoch: 0,
      items: [{ historyKey: key, transcriptKey: item.id, position: { entry: 2, item: 0 }, version: 1, item }],
    }),
  ).toEqual({ outcome: "refused", reason: "history-fence" });
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: [item],
    },
  });
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).not.toContain("stale-after-delete");
});

it("ordinary sibling deletion heals entry rows and retires the still-held publication and write claims", async () => {
  const storage = cache();
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect(await storage.getLatest(key, 10)).toHaveLength(1);
  const channel = Object.assign(new EventTarget(), {
    name: "evener.session-cache.v1",
    onmessage: null,
    onmessageerror: null,
    postMessage(_message: unknown) {},
    close() {},
  });
  setCacheChannelFactoryForTests(() => channel);
  channel.dispatchEvent(
    new MessageEvent("message", { data: { version: 1, sourceId: "sibling-tab", kind: "deletion", refs: ["local:a"] } }),
  );
  await settleProjectionWorkForTests();
  expect(await storage.getLatest(key, 10)).toEqual([]);
  expect(await storage.getHistoryGeneration(key)).toBe(1);
  expect(threadsStore.getState().cacheLifetimes.get("local:a")?.suppressed).toBe(true);
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("sibling-deleted", 2).items,
    },
  });
  await settleEntryTranscriptForTests("local:a");
  expect(await storage.getLatest(key, 10)).toEqual([]);
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).not.toContain("sibling-deleted");
});

function checkpoint(owner: string, itemId: string, entry: number): CurrentSessionRecord {
  const now = Date.now();
  const state = currentTranscriptState(hydrateThread(response("local:a", [turn(itemId, entry)]), "local:a", now));
  if (!state) throw new Error("fixture must encode the actual current checkpoint format");
  return {
    ownerToken: owner,
    ref: "local:a",
    connectionGeneration: 1,
    ownershipGeneration: 1,
    clearEpoch: 0,
    appliedSequence: 7,
    savedAt: now,
    state,
  };
}

it("ordinary current-only migration converts each owner checkpoint once without certifying its older seeds", async () => {
  const legacy = new SessionCacheIndexedDB({ indexedDB: factory });
  try {
    expect((await legacy.putCurrent(checkpoint("old-owner-a", "current-a", 0), Date.now())).outcome).toBe("written");
    expect((await legacy.putCurrent(checkpoint("old-owner-b", "current-b", 1), Date.now())).outcome).toBe("written");
    expect(await legacyCounts()).toEqual([0, 2]);
    connect(response("local:a", [turn("live-current", 3)]));
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    const storage = cache();
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual([
      "current-a",
      "current-b",
      "live-current",
    ]);
    expect(await legacyCounts()).toEqual([0, 0]);
    const window = await storage.getOlderWindow(key, { entry: 3, item: 0 }, 10);
    expect(window.rows).toEqual([]);
    expect(window.coverage.every((range) => range.start === null || range.start.entry >= 3)).toBe(true);
  } finally {
    legacy.close();
  }
});

it("ordinary current-only migration leaves an aborted checkpoint intact and retries explicit opening", async () => {
  const legacy = new SessionCacheIndexedDB({ indexedDB: factory });
  try {
    expect((await legacy.putCurrent(checkpoint("retry-owner", "retry-current", 0), Date.now())).outcome).toBe(
      "written",
    );
    const originalPut = IDBObjectStore.prototype.put;
    let aborted = false;
    const fault = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (
      this: IDBObjectStore,
      value,
      key,
    ) {
      if (!aborted && this.name === "transcriptItems" && value.transcriptKey === "retry-current") {
        aborted = true;
        this.transaction.abort();
      }
      return originalPut.call(this, value, key);
    });
    connect(response("local:a", [turn("live-current", 3)]));
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect(aborted).toBe(true);
    fault.mockRestore();
    expect(await legacyCounts()).toEqual([0, 1]);
    const storage = cache();
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["live-current"]);
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual([
      "retry-current",
      "live-current",
    ]);
    expect(await legacyCounts()).toEqual([0, 0]);
  } finally {
    legacy.close();
  }
});

it("ordinary migration does not read a second legacy blob while the first source is still in flight", async () => {
  const legacy = new SessionCacheIndexedDB({ indexedDB: factory });
  for (const ref of ["local:a", "local:b"]) {
    const record = cachedSessionRecord(
      hydrateThread(response(ref, [turn(`legacy-${ref}`, 0)]), ref, Date.now()),
      Date.now(),
    );
    if (!record) throw new Error("fixture must encode a source blob");
    expect((await legacy.put(record, 0, Date.now())).outcome).toBe("written");
  }
  const get = IDBObjectStore.prototype.get;
  let secondSourceReads = 0;
  const observed = vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, key) {
    if (this.name === "records" && key === "local:b") secondSourceReads += 1;
    return get.call(this, key);
  });
  const first = holdNextTransaction(["records", "current", "meta"], "readonly");
  try {
    const client = connect();
    client.on("thread/read", (params) => {
      const ref = params.ref ?? "local:a";
      const read = response(ref, [turn(`live-${ref}`, 3)]);
      if (ref === "local:b") {
        read.thread.id = "thread-b";
        read.thread.sessionId = "session-b";
      }
      return { ...read, requestGeneration: params.requestGeneration };
    });
    await threadsStore.getState().ensureThread("local:a");
    await first.reached;
    await threadsStore.getState().ensureThread("local:b");
    // Storage has completed A's readonly transaction. Only the source owner's
    // settlement is held, so B could read if the production scheduler let it.
    expect(secondSourceReads).toBe(0);
    expect(
      threadsStore
        .getState()
        .threads.get("local:b")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.transcriptKey),
    ).toContain("live-local:b");
    first.release();
    await Promise.all([settleEntryTranscriptForTests("local:a"), settleEntryTranscriptForTests("local:b")]);
    expect(secondSourceReads).toBe(2);
    expect(await legacyCounts()).toEqual([0, 0]);
  } finally {
    first.release();
    observed.mockRestore();
    legacy.close();
  }
});
