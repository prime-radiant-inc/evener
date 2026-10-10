import {
  cachedSessionRecord,
  currentTranscriptState,
  hydrateThread,
  type ThreadReadResponse,
  type Turn,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { deleteSession } from "../shell/rail/actions";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { entryWriteAdmissionFor } from "./entryWriteAdmission";
import { requestResult, transactionCompletion } from "./idbConnection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
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
  client.on("evener/session/delete", () => ({
    deleted: ["a"],
    skipped: [],
    navigation: { generation_id: "generation-test", targets: [] },
  }));
  await deleteSession(client, "local:a");
  expect(threadsStore.getState().deletedRefs.has("local:a")).toBe(true);
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

it("ordinary recorded notifications persist during busy input without a whole-model publication or debounce", async () => {
  const storage = cache();
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  for (let entry = 2; entry <= 7; entry += 1) {
    client.emitNotification({
      method: "history/updated",
      params: {
        ref: "local:a",
        threadId: "thread-a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-a", length: entry + 1 },
        items: turn(`busy-${entry}`, entry).items,
      },
    });
  }
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual([
    "recorded",
    "busy-2",
    "busy-3",
    "busy-4",
    "busy-5",
    "busy-6",
    "busy-7",
  ]);
  expect(await legacyCounts()).toEqual([0, 0]);
});

it("ordinary final pane release saves its fresh finite read before dropping the model", async () => {
  const storage = cache();
  const client = connect();
  let latest = response();
  let reads = 0;
  client.on("thread/read", (params) => {
    reads += 1;
    return { ...latest, requestGeneration: params.requestGeneration };
  });
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const before = reads;
  latest = response("local:a", [turn("final-pane", 2)]);
  expect(await threadsStore.getState().releaseThreadAndWait("local:a")).toEqual({ kind: "saved" });
  expect(reads).toBe(before + 1);
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toContain("final-pane");
  expect(threadsStore.getState().threads.has("local:a")).toBe(false);
});

it("ordinary overlapping refresh keeps two loaded pages and the deepest usable cursor", async () => {
  const storage = cache();
  const client = connect();
  let latest = response("local:a", [turn("window", 30)]);
  latest.snapshot = { incarnation: "inc-a", length: 60 };
  latest.olderCursor = "page-1";
  client.on("thread/read", (params) => ({ ...latest, requestGeneration: params.requestGeneration }));
  client.on("thread/turns/list", (params) => ({
    data: [turn(params.cursor === "page-1" ? "older" : "oldest", params.cursor === "page-1" ? 10 : 0)],
    nextCursor: params.cursor === "page-1" ? "page-2" : "deep",
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-a", length: 60 },
  }));
  await threadsStore.getState().ensureThread("local:a");
  await threadsStore.getState().loadOlderTurns("local:a");
  await threadsStore.getState().loadOlderTurns("local:a");
  latest = response("local:a", [turn("overlap", 25)]);
  latest.snapshot = { incarnation: "inc-a", length: 60 };
  latest.olderCursor = "shallow";
  await threadsStore.getState().refreshThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  expect(
    threadsStore
      .getState()
      .threads.get("local:a")
      ?.turns.map((turn) => turn.id),
  ).toEqual(["oldest", "older", "overlap", "window"]);
  expect(threadsStore.getState().threads.get("local:a")?.olderCursor).toBe("deep");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual([
    "oldest",
    "older",
    "overlap",
    "window",
  ]);
});

it("ordinary reconnect keeps a same-turn tail outside the recovered window without fabricating a missing row", async () => {
  const storage = cache();
  const client = connect(response("local:a", [turn("window", 30)]));
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  let releaseRead: (() => void) | undefined;
  let markArmed: (() => void) | undefined;
  const armed = new Promise<void>((resolve) => {
    markArmed = resolve;
  });
  client.on(
    "thread/read",
    (params) =>
      new Promise((resolve) => {
        releaseRead = () => {
          const recovered = turn("window", 40);
          recovered.items = recovered.items?.map((item) => ({
            ...item,
            id: "fresh-window",
            transcriptKey: "fresh-window",
          }));
          const read = response("local:a", [recovered]);
          read.requestGeneration = params.requestGeneration;
          read.snapshot = { incarnation: "inc-a", length: 4 };
          resolve(read);
        };
        markArmed?.();
      }),
  );
  client.emitStateChange("reconnecting");
  client.emitStateChange("ready");
  await armed;
  const tail = turn("tail", 50).items?.[0];
  if (!tail) throw new Error("missing tail item");
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 5 },
      items: [{ ...tail, turnId: "window" }],
    },
  });
  if (!releaseRead) throw new Error("source read was not issued");
  releaseRead();
  await settleEntryTranscriptForTests("local:a");
  expect(
    threadsStore
      .getState()
      .threads.get("local:a")
      ?.turns.find((turn) => turn.id === "window")
      ?.items.map((item) => item.transcriptKey),
  ).toContain("tail");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  const persisted = (await storage.getLatest(key, 10)).map((row) => row.transcriptKey);
  expect(persisted).toContain("tail");
  expect(persisted).toContain("fresh-window");
  expect(persisted).not.toContain("missing-48");
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).not.toContain("missing-48");
});

it("ordinary sibling Clear permits a genuinely fresh explicit acquisition to cache the new epoch", async () => {
  const storage = cache();
  connect();
  await threadsStore.getState().ensureThread("local:a");
  await threadsStore.getState().releaseThreadAndWait("local:a");
  expect(await storage.clear()).toEqual({ committed: true, epoch: 1 });
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
  expect(await storage.getClearEpoch()).toBe(1);
});

it("ordinary read-proven deletion fences the model, heals entry storage and broadcasts once", async () => {
  const storage = cache();
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect(
    await storage.commitBatch({
      historyKey: key,
      clearEpoch: 0,
      items: [],
      sessionMetadata: {
        historyKey: key,
        requestedRefs: [{ clientId: location.origin, ref: "local:a" }],
        resolvedIdentity: { ...identity, clientId: location.origin },
        savedAt: storage.now(),
        accessedAt: storage.now(),
        formatVersion: 3,
      },
    }),
  ).toMatchObject({ outcome: "committed" });
  const posted: unknown[] = [];
  const channel = Object.assign(new EventTarget(), {
    name: "evener.session-cache.v1",
    onmessage: null,
    onmessageerror: null,
    postMessage(message: unknown) {
      posted.push(message);
    },
    close() {},
  });
  setCacheChannelFactoryForTests(() => channel);
  const client = connect();
  client.on("thread/read", () => {
    throw new WireError("target deleted", -32001, {
      evenerErrorInfo: "actionUnavailable",
      mutationOutcome: "targetDeleted",
      retryDisposition: "none",
    });
  });
  await threadsStore.getState().ensureThread("local:a");
  await settleProjectionWorkForTests();
  expect(threadsStore.getState().deletedRefs.has("local:a")).toBe(true);
  expect(threadsStore.getState().threads.has("local:a")).toBe(false);
  expect(await countCachedSessions()).toBe(0);
  expect(posted).toEqual([{ version: 1, sourceId: expect.any(String), kind: "deletion", refs: ["local:a"] }]);
});

it("ordinary content clear retires the old entry history so a late old push cannot restore it", async () => {
  const storage = cache();
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect(await storage.getLatest(key, 10)).toHaveLength(1);
  client.on("thread/clear", (params) => {
    const thread = response("local:a", []).thread;
    thread.id = "cleared-thread";
    thread.evener.instanceId = thread.id;
    return {
      thread,
      ref: "local:a",
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: thread.id,
        instanceId: thread.id,
        projectionState: "reflected",
      },
    };
  });
  await threadsStore.getState().clearThread("local:a");
  await settleProjectionWorkForTests();
  expect(threadsStore.getState().threads.get("local:a")?.history).toBeUndefined();
  expect(await storage.getLatest(key, 10)).toEqual([]);
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 5 },
      items: turn("old-after-content-clear", 2).items,
    },
  });
  await settleEntryTranscriptForTests("local:a");
  expect(await storage.getLatest(key, 10)).toEqual([]);
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).not.toContain("old-after-content-clear");
  const fresh = response("local:a", [turn("new-after-content-clear", 2)]);
  fresh.thread.id = "cleared-thread";
  fresh.thread.evener.instanceId = "cleared-thread";
  fresh.snapshot = { incarnation: "cleared-incarnation", length: 3 };
  client.on("thread/read", (params) => ({ ...fresh, requestGeneration: params.requestGeneration }));
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "cleared-thread",
      bootGeneration: "1",
      epoch: 1,
      snapshot: fresh.snapshot,
      items: turn("new-after-content-clear", 2).items,
    },
  });
  await settleEntryTranscriptForTests("local:a");
  const freshKey = entryHistoryKey({ ...identity, clientId: location.origin, incarnation: "cleared-incarnation" });
  expect((await storage.getLatest(freshKey, 10)).map((row) => row.transcriptKey)).toEqual(["new-after-content-clear"]);
  expect(threadsStore.getState().threads.get("local:a")?.threadId).toBe("cleared-thread");
});

it("ordinary mutation-pin release saves a fresh final read before removing the last pinned model", async () => {
  const storage = cache();
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, createMutationId: () => "entry-pin-drain" });
  try {
    await outbox.enqueueIntent({
      targetRef: "local:a",
      method: "turn/queue",
      payload: { ref: "local:a", expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
      attachments: [],
      optimisticDisplay: { text: "queued" },
    });
    const client = connect();
    let latest = response();
    let finalReadSawModel = false;
    let retiring = false;
    client.on("thread/read", (params) => {
      if (retiring) finalReadSawModel = threadsStore.getState().threads.has("local:a");
      const read = { ...latest, requestGeneration: params.requestGeneration };
      read.thread.evener.mutationStateAuthoritative = true;
      return read;
    });
    client.on("turn/queue", () => new Promise(() => {}));
    await threadsStore.getState().ensureThread("local:a");
    await settleProjectionWorkForTests();
    await settleEntryTranscriptForTests("local:a");
    threadsStore.getState().releaseThread("local:a");
    expect(threadsStore.getState().threads.has("local:a")).toBe(true);
    const removed = new Promise<void>((resolve) => {
      const unsubscribe = threadsStore.subscribe((state) => {
        if (!state.threads.has("local:a")) {
          unsubscribe();
          resolve();
        }
      });
    });
    latest = response("local:a", [turn("pin-final", 3)]);
    retiring = true;
    const applied = turn("pin-settled", 2).items?.[0];
    if (!applied) throw new Error("missing settlement item");
    client.emitNotification({
      method: "history/updated",
      params: {
        ref: "local:a",
        threadId: "thread-a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-a", length: 4 },
        items: [{ ...applied, clientMutationId: "entry-pin-drain" }],
      },
    });
    await removed;
    expect(finalReadSawModel).toBe(true);
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toContain("pin-final");
  } finally {
    outbox.close();
  }
});

it("ordinary invalidating history input automatically reads a fresh baseline before caching its new identity", async () => {
  const storage = cache();
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  let recoveryStarted = false;
  let finish: (() => void) | undefined;
  client.on(
    "thread/read",
    (params) =>
      new Promise((resolve) => {
        recoveryStarted = true;
        finish = () => {
          const fresh = response("local:a", [turn("fresh-baseline", 2), turn("pending-new-identity", 3)]);
          fresh.epoch = 2;
          fresh.snapshot = { incarnation: "inc-b", length: 4 };
          resolve({ ...fresh, requestGeneration: params.requestGeneration });
        };
      }),
  );
  try {
    client.emitNotification({
      method: "history/updated",
      params: {
        ref: "local:a",
        threadId: "thread-a",
        bootGeneration: "1",
        epoch: 2,
        snapshot: { incarnation: "inc-b", length: 4 },
        items: turn("pending-new-identity", 3).items,
      },
    });
    // Observe the real wake before any settlement helper can drive parked work.
    await vi.waitFor(() => expect(recoveryStarted).toBe(true), { timeout: 500 });
    expect(threadsStore.getState().threads.get("local:a")?.history?.invalidatedAtGeneration).toBeDefined();
    const oldKey = entryHistoryKey({ ...identity, clientId: location.origin });
    expect((await storage.getLatest(oldKey, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
    const freshKey = entryHistoryKey({ ...identity, clientId: location.origin, incarnation: "inc-b" });
    expect(await storage.getLatest(freshKey, 10)).toEqual([]);
    expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).not.toContain("pending-new-identity");
    if (!finish) throw new Error("recovery read was not started");
    finish();
    await settleEntryTranscriptForTests("local:a");
    expect((await storage.getLatest(freshKey, 10)).map((row) => row.transcriptKey)).toEqual([
      "fresh-baseline",
      "pending-new-identity",
    ]);
    expect(threadsStore.getState().threads.get("local:a")?.history?.invalidatedAtGeneration).toBeUndefined();
    expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).toContain("pending-new-identity");
  } finally {
    finish?.();
  }
});

it("ordinary suppression-only acquisition still cold-reads but never caches until every holder releases", async () => {
  const storage = cache();
  const channel = Object.assign(new EventTarget(), {
    name: "evener.session-cache.v1",
    onmessage: null,
    onmessageerror: null,
    postMessage(_message: unknown) {},
    close() {},
  });
  setCacheChannelFactoryForTests(() => channel);
  channel.dispatchEvent(
    new MessageEvent("message", {
      data: { version: 1, sourceId: "sibling-tab", kind: "deletion", refs: ["local:a"] },
    }),
  );
  await settleProjectionWorkForTests();
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const coldReads = client.calls.filter((call) => call.method === "thread/read");
  expect(coldReads).toHaveLength(1);
  expect(coldReads[0]?.params).not.toHaveProperty("heldSnapshot");
  expect(threadsStore.getState().threads.has("local:a")).toBe(true);
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).toContain("recorded");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect(await storage.getLatest(key, 10)).toEqual([]);
  await threadsStore.getState().ensureThread("local:a");
  await threadsStore.getState().releaseThreadAndWait("local:a");
  expect(threadsStore.getState().cacheLifetimes.get("local:a")?.suppressed).toBe(true);
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("suppressed-live", 2).items,
    },
  });
  await settleEntryTranscriptForTests("local:a");
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).toContain("suppressed-live");
  expect(await storage.getLatest(key, 10)).toEqual([]);
  await threadsStore.getState().releaseThreadAndWait("local:a");
  expect(threadsStore.getState().cacheLifetimes.has("local:a")).toBe(false);
  expect(await storage.getLatest(key, 10)).toEqual([]);
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
});

it("ordinary failed source refresh preserves only the prior committed rows until a successful baseline", async () => {
  const storage = cache();
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  client.on("thread/read", () => {
    throw new Error("source unavailable");
  });
  await expect(threadsStore.getState().refreshThread("local:a")).rejects.toThrow("source unavailable");
  await settleEntryTranscriptForTests("local:a");
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
  client.on("thread/read", (params) => ({
    ...response("local:a", [turn("recovered-source", 2)]),
    requestGeneration: params.requestGeneration,
  }));
  await threadsStore.getState().refreshThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toContain("recovered-source");
});

it("ordinary empty source cold-opens and accepts later recorded input without a blob anchor", async () => {
  const storage = cache();
  const empty = response("local:a", []);
  empty.snapshot = { incarnation: "inc-a", length: 0 };
  const client = connect(empty);
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  expect(threadsStore.getState().threads.get("local:a")?.turns).toEqual([]);
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect(await storage.getLatest(key, 10)).toEqual([]);
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 1 },
      items: turn("first-recorded", 0).items,
    },
  });
  await settleEntryTranscriptForTests("local:a");
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["first-recorded"]);
});

it("ordinary reacquisition on a replacement client cannot reuse the outgoing retirement model", async () => {
  const storage = cache();
  const oldClient = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  let finishOld: (() => void) | undefined;
  oldClient.on(
    "thread/read",
    (params) =>
      new Promise((resolve) => {
        finishOld = () =>
          resolve({
            ...response("local:a", [turn("obsolete-client-final", 2)]),
            requestGeneration: params.requestGeneration,
          });
      }),
  );
  const releasing = threadsStore.getState().releaseThreadAndWait("local:a");
  await vi.waitFor(() => expect(finishOld).toBeDefined());
  const replacement = new FakeClient("ready");
  let finishNew: (() => void) | undefined;
  replacement.on("thread/unsubscribe", () => ({}));
  replacement.on(
    "thread/read",
    (params) =>
      new Promise((resolve) => {
        finishNew = () => {
          const fresh = response("local:a", [turn("replacement-client-baseline", 3)]);
          fresh.snapshot = { incarnation: "replacement-incarnation", length: 4 };
          resolve({ ...fresh, requestGeneration: params.requestGeneration });
        };
      }),
  );
  connectionStore.getState().connect(replacement);
  const opening = threadsStore.getState().ensureThread("local:a");
  try {
    await vi.waitFor(() => expect(finishNew).toBeDefined(), { timeout: 500 });
    expect(threadsStore.getState().threads.has("local:a")).toBe(false);
    finishNew?.();
    await opening;
    expect(threadsStore.getState().threads.get("local:a")).toMatchObject({
      history: { incarnation: "replacement-incarnation" },
    });
    expect(threadsStore.getState().cacheLifetimes.get("local:a")?.suppressed).not.toBe(true);
    await settleEntryTranscriptForTests("local:a");
    const freshKey = entryHistoryKey({
      ...identity,
      clientId: location.origin,
      incarnation: "replacement-incarnation",
    });
    expect((await storage.getLatest(freshKey, 10)).map((row) => row.transcriptKey)).toEqual([
      "replacement-client-baseline",
    ]);
    finishOld?.();
    await releasing;
    await settleEntryTranscriptForTests("local:a");
    const key = entryHistoryKey({ ...identity, clientId: location.origin, incarnation: "replacement-incarnation" });
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["replacement-client-baseline"]);
    expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).not.toContain("obsolete-client-final");
  } finally {
    finishOld?.();
    finishNew?.();
    await opening;
    await releasing;
  }
});
