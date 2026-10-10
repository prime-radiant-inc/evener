import type { ThreadReadResponse, Turn } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { entryWriteAdmissionFor } from "./entryWriteAdmission";
import { requestResult, transactionCompletion } from "./idbConnection";
import { entryHistoryKey, SESSION_CACHE_DATABASE_NAME } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  acquireBackgroundThread,
  clearCachedSessions,
  countCachedSessions,
  installEntryTranscriptFixtureForTests,
  resetThreadsStoreForTests,
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
  const fault = vi.spyOn(IDBObjectStore.prototype, "clear").mockImplementationOnce(function () {
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
