import type { AnyNotification, CachedSessionItem, ThreadItem } from "@evener/appwire-client";
import {
  encodeTranscriptItem,
  hydrateThread,
  type ThreadReadResponse,
  type Turn,
  WireError,
} from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { deleteSession } from "../shell/rail/actions";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { entryWriteAdmissionFor } from "./entryWriteAdmission";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { settleProjectionWorkForTests } from "./projectionWork";
import {
  entryHistoryKey,
  SESSION_CACHE_DATABASE_NAME,
  SESSION_CACHE_DATABASE_VERSION,
  type TranscriptItemRow,
} from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  clearCachedSessions,
  countCachedSessions,
  markCacheSessionsDeleted,
  productionEntryTranscriptForTests,
  resetThreadsStoreForTests,
  setCacheChannelFactoryForTests,
  setProductionEntryCacheForTests,
  settleBackgroundTranscriptsForTests,
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
  vi.useRealTimers();
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

function unrelatedOverlay(client: FakeClient): void {
  client.emitNotification({
    method: "overlay/upserted",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      item: {
        key: "unrelated",
        kind: "notice",
        anchor: { entry: 2, item: 0 },
        turnId: "recorded",
        item: { id: "unrelated", type: "agentMessage", text: "unrelated" },
      },
    },
  });
  expect(threadsStore.getState().threads.get("local:a")?.overlay?.unrelated).toBeDefined();
}

it.each([false, true])("response ownership preserves goal after overlay, newer notification=%s", async (newer) => {
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  expect(productionEntryTranscriptForTests("local:a")?.repository).toBeDefined();
  const reply = deferred<{ started: boolean }>();
  client.on("goal/set", () => reply.promise);
  const pending = threadsStore.getState().setGoal("local:a", "confirmed goal");
  const pushed = { objective: "newer goal", status: "active", iterations: 2 };
  if (newer)
    client.emitNotification({
      method: "evener/goal/updated",
      params: { ref: "local:a", threadId: "thread-a", goal: pushed },
    });
  reply.resolve({ started: true });
  await pending;
  unrelatedOverlay(client);
  expect(threadsStore.getState().threads.get("local:a")?.goal).toEqual(
    newer ? pushed : { objective: "confirmed goal", status: "active", iterations: 0 },
  );
});

it.each(["response", "notification", "intent"] as const)(
  "response ownership preserves human note after overlay, authority=%s",
  async (authority) => {
    const client = connect();
    await threadsStore.getState().ensureThread("local:a");
    expect(productionEntryTranscriptForTests("local:a")?.repository).toBeDefined();
    const reached = deferred<void>();
    const reply = deferred<void>();
    client.on("notes/human/set", async (params) => {
      if (params.note === "confirmed note") {
        reached.resolve();
        await reply.promise;
      }
      return {
        note: params.note ?? "",
        receipt: {
          clientMutationId: params.clientMutationId,
          threadId: "thread-a",
          disposition: "applied",
          projectionState: "notProjected",
        },
      };
    });
    await threadsStore.getState().setHumanNote("local:a", "confirmed note");
    await reached.promise;
    if (authority === "notification")
      client.emitNotification({
        method: "evener/notes/updated",
        params: { ref: "local:a", threadId: "thread-a", humanNote: "newer note" },
      });
    if (authority === "intent") await threadsStore.getState().setHumanNote("local:a", "newer note");
    reply.resolve();
    const outbox = new MutationOutboxIndexedDB({ indexedDB: factory });
    await vi.waitFor(async () => expect(await outbox.listOutbox("local:a")).toEqual([]));
    await settleProjectionWorkForTests();
    const expected = authority === "response" ? "confirmed note" : "newer note";
    await vi.waitFor(() => expect(threadsStore.getState().threads.get("local:a")?.humanNote).toBe(expected));
    unrelatedOverlay(client);
    expect(threadsStore.getState().threads.get("local:a")?.humanNote).toBe(expected);
  },
);

it.each(
  (["thread", "watched"] as const).flatMap((kind) =>
    [false, true].flatMap((committed) =>
      (["held", "released", "replaced"] as const).map((owner) => ({ kind, committed, owner })),
    ),
  ),
)("Clear pending demand $kind committed=$committed owner=$owner", async ({ kind, committed, owner }) => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect();
  const acquire = () =>
    kind === "thread"
      ? threadsStore.getState().ensureThread("local:a")
      : threadsStore.getState().watchThread("local:a", { includeTurns: true });
  await acquire();
  await settleEntryTranscriptForTests("local:a");
  const model = () =>
    (kind === "thread" ? threadsStore.getState().threads : threadsStore.getState().watchedThreads).get("local:a");
  const obsolete = deferred<ThreadReadResponse>();
  const entered = deferred<void>();
  client.on("thread/read", async (params) => {
    entered.resolve();
    return { ...(await obsolete.promise), requestGeneration: params.requestGeneration };
  });
  client.emitNotification({ method: "evener/thread/resync", params: { ref: "local:a", threadId: "thread-a" } });
  await entered.promise;
  const clearReply = deferred<void>();
  const original = storage.clear.bind(storage);
  vi.spyOn(storage, "clear").mockImplementation(async () => {
    await clearReply.promise;
    return committed ? original() : { committed: false, epoch: 0 };
  });
  const clearing = clearCachedSessions();
  const published: string[] = [];
  const unsubscribe = threadsStore.subscribe(() => published.push(JSON.stringify(model()) ?? ""));
  const replacement = deferred<ThreadReadResponse>();
  client.on("thread/read", async (params) => ({
    ...(await replacement.promise),
    requestGeneration: params.requestGeneration,
  }));
  if (owner !== "held") {
    if (kind === "thread") threadsStore.getState().releaseThread("local:a");
    else threadsStore.getState().releaseWatchedThread("local:a");
  }
  if (owner === "replaced") {
    client.on("thread/read", (params) => ({
      ...response("local:a", [turn("new-owner", 2)]),
      requestGeneration: params.requestGeneration,
    }));
    await acquire();
  }
  const reads = client.calls.filter((call) => call.method === "thread/read").length;
  clearReply.resolve();
  expect(await clearing).toEqual({ committed });
  obsolete.resolve({ ...response("local:a", [turn("obsolete", 2)]), authoritative: true });
  if (owner === "held")
    await vi.waitFor(() =>
      expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads + 1),
    );
  expect(JSON.stringify(model()) ?? "").not.toContain("obsolete");
  replacement.resolve({ ...response("local:a", [turn("replacement", 2)]), authoritative: true });
  if (owner === "held") await vi.waitFor(() => expect(JSON.stringify(model())).toContain("replacement"));
  await settleBackgroundTranscriptsForTests();
  expect(published.join("\n")).not.toContain("obsolete");
  unsubscribe();
  if (owner !== "held") expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
  if (committed) expect(await storage.count()).toBe(0);
});

it.each(
  (["mutation", "history"] as const).flatMap((holder) =>
    [false, true].map((secondHolder) => ({ holder, secondHolder })),
  ),
)("shared reader release holder=$holder secondHolder=$secondHolder", async ({ holder, secondHolder }) => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const initial = { ...response(), olderCursor: "older" };
  const client = connect(initial);
  const background = acquireBackgroundThread("local:a");
  await background.ready;
  await settleEntryTranscriptForTests("local:a");
  const entered = deferred<void>();
  const finish = deferred<void>();
  let historyRequest: Promise<void> | undefined;
  if (holder === "mutation") {
    client.on("notes/human/set", async (params) => {
      entered.resolve();
      await finish.promise;
      return {
        note: params.note ?? "",
        receipt: {
          clientMutationId: params.clientMutationId,
          threadId: "thread-a",
          disposition: "applied",
          projectionState: "notProjected",
        },
      };
    });
    await threadsStore.getState().setHumanNote("local:a", "note pin");
  } else {
    client.on("thread/turns/list", async () => {
      entered.resolve();
      await finish.promise;
      return { data: [turn("older", 0)], bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc-a", length: 3 } };
    });
    historyRequest = threadsStore.getState().loadOlderTurns("local:a");
  }
  await entered.promise;
  client.on("thread/read", (params) => ({ ...initial, requestGeneration: params.requestGeneration }));
  client.emitNotification({ method: "evener/thread/resync", params: { ref: "local:a", threadId: "thread-a" } });
  await vi.waitFor(() => expect(productionEntryTranscriptForTests("local:a").reader).toBeDefined());
  await settleEntryTranscriptForTests("local:a");
  const repository = productionEntryTranscriptForTests("local:a").repository;
  expect(
    repository
      ?.getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toContain("recorded");
  if (secondHolder) {
    await threadsStore.getState().ensureThread("local:a");
    threadsStore.getState().releaseThread("local:a");
    expect(productionEntryTranscriptForTests("local:a").reader).toBeDefined();
    expect(
      repository
        ?.getSnapshot()
        ?.history?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toContain("recorded");
    await threadsStore.getState().ensureThread("local:a");
  }
  finish.resolve();
  await historyRequest;
  if (holder === "mutation") {
    const outbox = new MutationOutboxIndexedDB({ indexedDB: factory });
    await vi.waitFor(async () => expect(await outbox.listOutbox("local:a")).toEqual([]));
    await settleProjectionWorkForTests();
  }
  await settleBackgroundTranscriptsForTests();
  if (secondHolder) {
    expect(productionEntryTranscriptForTests("local:a").reader).toBeDefined();
    expect(
      repository
        ?.getSnapshot()
        ?.history?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toContain("recorded");
    threadsStore.getState().releaseThread("local:a");
  }
  await settleBackgroundTranscriptsForTests();
  expect(productionEntryTranscriptForTests("local:a").repository).toBe(repository);
  expect(productionEntryTranscriptForTests("local:a").reader).toBeUndefined();
  expect(
    repository
      ?.getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual([""]);
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect((await storage.getLatest<ThreadItem>(key, 200)).map((row) => row.item.text)).toContain("recorded");
  const retain = vi.spyOn(entryWriteAdmissionFor(storage), "retain");
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("later", 3).items,
    },
  });
  await settleBackgroundTranscriptsForTests();
  expect(retain.mock.calls.map((call) => call[2])).toContain("background");
  expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
  background.release();
});

async function seedDiskOnlyOpening(withPages = false): Promise<SessionEntryCache> {
  const storage = cache();
  const read = response("local:a", [turn("window-1", 30), turn("window-2", 31), turn("window-3", 32)]);
  read.snapshot = { incarnation: "inc-a", length: 60 };
  read.olderCursor = "cur-page-1";
  const client = connect(read);
  client.on("thread/turns/list", (params) => {
    const first = params.cursor === "cur-page-1";
    return {
      data: [0, 1, 2].map((index) => turn(`${first ? "older" : "oldest"}-${index + 1}`, (first ? 10 : 0) + index)),
      nextCursor: first ? "cur-page-2" : "cur-page-3",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 60 },
    };
  });
  await threadsStore.getState().ensureThread("local:a");
  if (withPages) {
    await threadsStore.getState().loadOlderTurns("local:a");
    await threadsStore.getState().loadOlderTurns("local:a");
  }
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  const metadata = await storage.getSessionMetadata(key);
  expect(metadata?.cachedHeader).toMatchObject({ threadId: "thread-a", imageSessionId: "session-a" });
  expect(metadata?.cachedHeader?.history).not.toHaveProperty("turns");
  expect(metadata?.cachedHeader).not.toHaveProperty("overlay");
  expect((await storage.getLatest(key, 200)).map((row) => row.transcriptKey)).toEqual(
    withPages
      ? ["oldest-1", "oldest-2", "oldest-3", "older-1", "older-2", "older-3", "window-1", "window-2", "window-3"]
      : ["window-1", "window-2", "window-3"],
  );
  // A reload drops every store model and repository, not just its reader.
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  expect(threadsStore.getState().threads.has("local:a")).toBe(false);
  return storage;
}

it("ordinary disk-only opening displays the window and both cached pages before source success, withholds paging, then uses the settled cursor", async () => {
  await seedDiskOnlyOpening(true);
  const client = connect();
  let releaseRead: (() => void) | undefined;
  let sourcePending = false;
  client.on(
    "thread/read",
    (params) =>
      new Promise((resolve) => {
        sourcePending = true;
        releaseRead = () => {
          sourcePending = false;
          const read = response("local:a", [turn("settled", 50)]);
          read.snapshot = { incarnation: "inc-a", length: 70 };
          read.olderCursor = "cur-settled";
          read.requestGeneration = params.requestGeneration;
          resolve(read);
        };
      }),
  );
  const listCalls: Array<{ cursor?: string }> = [];
  client.on("thread/turns/list", (params) => {
    listCalls.push({ cursor: params.cursor });
    return {
      data: [turn("next-page", 40)],
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 70 },
    };
  });
  const opening = threadsStore.getState().ensureThread("local:a");
  try {
    await vi.waitFor(() => {
      expect(sourcePending).toBe(true);
      expect(
        threadsStore
          .getState()
          .threads.get("local:a")
          ?.history?.turns.map((turn) => turn.id),
      ).toEqual([
        "oldest-1",
        "oldest-2",
        "oldest-3",
        "older-1",
        "older-2",
        "older-3",
        "window-1",
        "window-2",
        "window-3",
      ]);
    });
    expect(threadsStore.getState().threads.get("local:a")?.olderCursor).toBe("cur-page-3");
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.history?.turns.map((turn) => turn.items[0]?.text),
    ).toEqual([
      "oldest-1",
      "oldest-2",
      "oldest-3",
      "older-1",
      "older-2",
      "older-3",
      "window-1",
      "window-2",
      "window-3",
    ]);
    expect(client.calls.filter((call) => call.method === "thread/read")[0]?.params).not.toHaveProperty("heldSnapshot");
    expect(threadsStore.getState().threads.get("local:a")?.history?.invalidatedAtGeneration).toBeDefined();
    await threadsStore.getState().loadOlderTurns("local:a");
    expect(listCalls).toEqual([]);
    expect(sourcePending).toBe(true);
    if (!releaseRead) throw new Error("reconciliation read was not issued");
    releaseRead();
    await opening;
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.history?.turns.map((turn) => turn.id),
    ).toEqual(["settled"]);
    expect(threadsStore.getState().threads.get("local:a")?.history?.invalidatedAtGeneration).toBeUndefined();
    await threadsStore.getState().loadOlderTurns("local:a");
    expect(listCalls).toEqual([{ cursor: "cur-settled" }]);
  } finally {
    releaseRead?.();
    await opening;
  }
});

it.each(["transport", "history"] as const)(
  "ordinary disk-only opening keeps content and refuses paging and writes after a %s read failure",
  async (failure) => {
    const storage = await seedDiskOnlyOpening();
    const writes = vi.spyOn(SessionEntryCache.prototype, "commitBatch");
    const client = connect();
    client.on("thread/read", () => {
      throw failure === "history"
        ? new WireError("source unavailable", -32000, { evenerErrorInfo: "transcriptHistoryFailed" })
        : new Error("transport unavailable");
    });
    const pages = vi.fn(() => ({ data: [] }));
    client.on("thread/turns/list", pages);
    await threadsStore.getState().ensureThread("local:a");
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.history?.turns.map((turn) => turn.id),
    ).toEqual(["window-1", "window-2", "window-3"]);
    expect(client.calls.filter((call) => call.method === "thread/read")[0]?.params).not.toHaveProperty("heldSnapshot");
    if (failure === "history") expect(threadsStore.getState().threads.get("local:a")?.history?.failed).toBeDefined();
    client.emitNotification({
      method: "history/updated",
      params: {
        ref: "local:a",
        threadId: "thread-a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-a", length: 70 },
        items: turn("unverified-input", 40).items,
      },
    });
    await threadsStore.getState().loadOlderTurns("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect(pages).not.toHaveBeenCalled();
    expect(writes).not.toHaveBeenCalled();
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    expect((await storage.getLatest(key, 200)).map((row) => row.transcriptKey)).toEqual([
      "window-1",
      "window-2",
      "window-3",
    ]);
    expect(JSON.stringify(threadsStore.getState().threads.get("local:a"))).not.toContain("unverified-input");
  },
);

it.each(["release", "delete", "Clear"] as const)(
  "ordinary disk-only lookup cannot publish old content after %s retires its acquisition",
  async (retirement) => {
    await seedDiskOnlyOpening();
    const original = SessionEntryCache.prototype.getSessionForRequestedRef;
    let releaseLookup: (() => void) | undefined;
    let markLookup: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      markLookup = resolve;
    });
    vi.spyOn(SessionEntryCache.prototype, "getSessionForRequestedRef").mockImplementation(async function (
      this: SessionEntryCache,
      clientId,
      ref,
    ) {
      const row = await original.call(this, clientId, ref);
      await new Promise<void>((resolve) => {
        releaseLookup = resolve;
        markLookup?.();
      });
      return row;
    });
    const client = connect(response("local:a", [turn("fresh-after-retirement", 50)]));
    const opening = threadsStore.getState().ensureThread("local:a");
    await held;
    try {
      if (retirement === "release") await threadsStore.getState().releaseThreadAndWait("local:a");
      else if (retirement === "delete") {
        markCacheSessionsDeleted(["local:a"]);
        await settleProjectionWorkForTests();
      } else await clearCachedSessions();
      releaseLookup?.();
      await opening;
      const readCount = client.calls.filter((call) => call.method === "thread/read").length;
      expect(readCount).toBe(retirement === "Clear" ? 1 : 0);
      expect(JSON.stringify(threadsStore.getState().threads.get("local:a")) ?? "").not.toContain("window-1");
      if (retirement !== "Clear") expect(threadsStore.getState().threads.has("local:a")).toBe(false);
    } finally {
      releaseLookup?.();
      await opening;
    }
  },
);

it.each(["evicted", "failed-write"] as const)(
  "opening a background model recovers bodies after %s",
  async (failure) => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let now = 1_000;
    const storage = new SessionEntryCache({
      databaseName: SESSION_CACHE_DATABASE_NAME,
      indexedDB: factory,
      keyRange: IDBKeyRange,
      ttlMs: 100,
      now: () => now,
    });
    caches.push(storage);
    setProductionEntryCacheForTests(storage);
    const writes =
      failure === "failed-write"
        ? vi
            .spyOn(storage, "commitBatch")
            .mockResolvedValue({ outcome: "failed", error: new Error("disk unavailable") })
        : undefined;
    const client = connect();
    client.on("thread/read", (params) => ({ ...response(), requestGeneration: params.requestGeneration }));
    const background = acquireBackgroundThread("local:a");
    await background.ready;
    if (failure === "failed-write") {
      await expect(settleEntryTranscriptForTests("local:a")).rejects.toBeInstanceOf(Error);
      expect(await storage.getLatest(entryHistoryKey({ ...identity, clientId: location.origin }), 10)).toEqual([]);
    } else {
      await settleEntryTranscriptForTests("local:a");
    }
    writes?.mockRestore();
    if (failure === "evicted") {
      const key = entryHistoryKey({ ...identity, clientId: location.origin });
      const rows = await storage.getLatest(key, 10);
      expect(rows).toHaveLength(1);
      now += 1_000;
      expect(
        (
          await storage.commitBatch({
            historyKey: "other-history",
            clearEpoch: 0,
            items: rows.map((row) => ({ ...row, historyKey: "other-history" })),
          })
        ).outcome,
      ).toBe("committed");
      expect(await storage.getLatest(key, 10)).toEqual([]);
    }
    const reads = client.calls.filter((call) => call.method === "thread/read").length;
    await threadsStore.getState().ensureThread("local:a");
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["recorded"]);
    expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads + 1);
    if (failure === "failed-write") {
      const repository = productionEntryTranscriptForTests("local:a").repository!;
      // Foreground recovery does not fast-forward an already paced retry.
      expect(repository.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0);
      await expect(settleEntryTranscriptForTests("local:a")).rejects.toBeInstanceOf(Error);
      await vi.advanceTimersByTimeAsync(249);
      expect(repository.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0);
      await vi.advanceTimersByTimeAsync(1);
      await settleEntryTranscriptForTests("local:a");
      expect(repository.retainedInventoryForTesting().retryBodies).toEqual([]);
    }
    await settleEntryTranscriptForTests("local:a");
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    expect((await storage.getLatest<CachedSessionItem>(key, 10)).map((row) => row.item.text)).toEqual(["recorded"]);
    background.release();
  },
);

it.each([false, true])("opening verifies accepted bodies when the latest write was lost: %s", async (lost) => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  let latest = response("local:a", [turn("K", 1, "version one")]);
  const client = connect(latest);
  client.on("thread/read", (params) => ({ ...latest, requestGeneration: params.requestGeneration }));
  const background = acquireBackgroundThread("local:a");
  await background.ready;
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect((await storage.getLatest(key, 10))[0]?.version).toBe(1);
  const updated = turn("K", 1, "version two exact body");
  for (const item of updated.items ?? []) item.version = 2;
  latest = response("local:a", [updated]);
  const writes = lost
    ? vi.spyOn(storage, "commitBatch").mockResolvedValue({ outcome: "failed", error: new Error("lost write") })
    : undefined;
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 3 },
      items: updated.items,
    },
  });
  if (lost) {
    await expect(settleEntryTranscriptForTests("local:a")).rejects.toBeInstanceOf(Error);
  } else await settleEntryTranscriptForTests("local:a");
  expect((await storage.getLatest(key, 10))[0]?.version).toBe(lost ? 1 : 2);
  expect(threadsStore.getState().threads.get("local:a")?.history?.turns[0]?.items[0]?.text).toBe("");
  writes?.mockRestore();
  const reads = client.calls.filter((call) => call.method === "thread/read").length;
  await threadsStore.getState().ensureThread("local:a");
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads + (lost ? 1 : 0));
  expect(
    threadsStore
      .getState()
      .threads.get("local:a")
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual(["version two exact body"]);
  if (lost) {
    const repository = productionEntryTranscriptForTests("local:a").repository!;
    expect(repository.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0);
    await expect(settleEntryTranscriptForTests("local:a")).rejects.toBeInstanceOf(Error);
    await vi.advanceTimersByTimeAsync(249);
    expect(repository.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0);
    await vi.advanceTimersByTimeAsync(1);
    await settleEntryTranscriptForTests("local:a");
    expect(repository.retainedInventoryForTesting().retryBodies).toEqual([]);
  }
  await settleEntryTranscriptForTests("local:a");
  expect((await storage.getLatest<CachedSessionItem>(key, 10))[0]?.item.text).toBe("version two exact body");
  background.release();
});

it.each(["body", "empty", "omitted"] as const)(
  "a retained %s payload opens without a cache certificate or source read",
  async (kind) => {
    const storage = cache();
    setProductionEntryCacheForTests(storage);
    const client = connect();
    const background = acquireBackgroundThread("local:a");
    await background.ready;
    await settleEntryTranscriptForTests("local:a");
    const repository = productionEntryTranscriptForTests("local:a").repository;
    if (!repository) throw new Error("expected background repository");
    const retained = repository.attachReader();
    await retained.openLocal(200);
    const item: ThreadItem =
      kind === "omitted"
        ? {
            id: "recorded",
            transcriptKey: "recorded",
            turnId: "recorded",
            position: { entry: 1, item: 0 },
            version: 2,
            type: "toolCall",
            toolName: "exec_command",
            output: "exact tool output",
            status: "completed",
          }
        : { ...turn("recorded", 1, kind === "empty" ? "" : "retained exact body").items![0]!, version: 2 };
    const writes = vi
      .spyOn(storage, "commitBatch")
      .mockResolvedValue({ outcome: "failed", error: new Error("lost write") });
    try {
      client.emitNotification({
        method: "history/updated",
        params: {
          ref: "local:a",
          threadId: "thread-a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-a", length: 3 },
          items: [item],
        },
      });
      await expect(settleEntryTranscriptForTests("local:a")).rejects.toBeInstanceOf(Error);
      expect(repository.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0);
      const reads = client.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread("local:a");
      expect(productionEntryTranscriptForTests("local:a").lastLocalOpenStatus).toBe("recovery-needed");
      expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
      const displayed = threadsStore
        .getState()
        .threads.get("local:a")
        ?.turns.flatMap((turn) => turn.items)[0];
      expect(displayed?.version).toBe(2);
      if (kind === "omitted") expect(displayed?.output).toBe("exact tool output");
      else expect(displayed?.text).toBe(kind === "empty" ? "" : "retained exact body");
      writes.mockRestore();
      const key = entryHistoryKey({ ...identity, clientId: location.origin });
      await vi.waitFor(async () => expect((await storage.getLatest(key, 10))[0]?.version).toBe(2));
      await settleEntryTranscriptForTests("local:a");
      expect(repository.retainedInventoryForTesting().retryBodies).toEqual([]);
      const persisted = (await storage.getLatest<CachedSessionItem>(key, 10))[0]?.item;
      if (kind === "omitted") expect(persisted?.output).toBe("exact tool output");
      else expect(persisted?.text).toBe(kind === "empty" ? "" : "retained exact body");
    } finally {
      writes.mockRestore();
      retained.dispose();
      background.release();
    }
  },
);

it.each(["opening", "paging"])(
  "background promotion restores every cached body beyond 200 items: %s",
  async (stage) => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const storage = cache();
    setProductionEntryCacheForTests(storage);
    const turns = Array.from({ length: 250 }, (_, index) => turn(`body-${index}`, index));
    const read = response("local:a", turns);
    read.snapshot = { incarnation: "inc-a", length: 250 };
    const client = connect(read);
    const background = acquireBackgroundThread("local:a");
    await background.ready;
    await settleEntryTranscriptForTests("local:a");
    const reads = client.calls.filter((call) => call.method === "thread/read").length;
    await threadsStore.getState().ensureThread("local:a");
    expect(productionEntryTranscriptForTests("local:a")?.lastLocalOpenStatus).toBe("current");
    const items = () =>
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.turns.flatMap((turn) => turn.items);
    // Round 22 supersedes the old display expectation of 50 empty strings
    // followed by 200 bodies: the no-trim ruling preserves HISTORY evidence,
    // not blank display rows. Keep exact identities and positions in history.
    const bodies = () =>
      items()
        ?.map((item) => item.text)
        .filter(Boolean);
    if (stage === "opening") {
      const identities = threadsStore
        .getState()
        .threads.get("local:a")
        ?.history?.turns.flatMap((turn) => turn.items)
        .map(encodeTranscriptItem)
        .filter((item) => item.identityOnly);
      expect(identities).toEqual(
        Array.from({ length: 50 }, (_, entry) => ({
          id: `body-${entry}`,
          turnId: `body-${entry}`,
          transcriptKey: `body-${entry}`,
          type: "agentMessage",
          version: 1,
          position: { entry, item: 0 },
          text: "",
          textOmitted: true,
          identityOnly: true,
        })),
      );
      expect(items()?.map((item) => item.text)).toEqual(
        turns.slice(-200).flatMap((turn) => (turn.items ?? []).map((item) => item.text)),
      );
      // The virtual list and its near-top scroll origin start at hydrated
      // content, not at the retained identity prefix.
      expect(
        threadsStore
          .getState()
          .threads.get("local:a")
          ?.turns.map((turn) => turn.id),
      ).toEqual(turns.slice(-200).map((turn) => turn.id));
    }
    expect(bodies()).toEqual(turns.slice(-200).flatMap((turn) => (turn.items ?? []).map((item) => item.text)));
    expect(threadsStore.getState().threads.get("local:a")?.olderCursor).toBeDefined();
    const raw = vi.spyOn(storage, "getBefore");
    const older = vi.spyOn(storage, "getOlderWindow");
    await threadsStore.getState().loadOlderTurns("local:a");
    // Supersedes the feature-only getOlderWindow-at-50 oracle: the known
    // identity interval restores through raw paging before certified demand.
    expect(raw).toHaveBeenCalledTimes(1);
    expect(raw.mock.calls[0]?.[1]).toEqual({ entry: 50, item: 0 });
    expect(await raw.mock.results[0]?.value).toHaveLength(40);
    expect(older).not.toHaveBeenCalled();
    expect(bodies()).toEqual(turns.slice(10).flatMap((turn) => (turn.items ?? []).map((item) => item.text)));
    expect(threadsStore.getState().threads.get("local:a")?.olderCursor).toBeDefined();
    await threadsStore.getState().loadOlderTurns("local:a");
    expect(raw).toHaveBeenCalledTimes(2);
    expect(raw.mock.calls[1]?.[1]).toEqual({ entry: 10, item: 0 });
    expect(await raw.mock.results[1]?.value).toHaveLength(10);
    expect(older).not.toHaveBeenCalled();
    expect(productionEntryTranscriptForTests("local:a").repository?.oldestPosition()).toEqual({ entry: 0, item: 0 });
    expect(items()?.map((item) => item.text)).toEqual(
      turns.flatMap((turn) => (turn.items ?? []).map((item) => item.text)),
    );
    expect(threadsStore.getState().threads.get("local:a")?.olderCursor).toBeUndefined();
    await threadsStore.getState().loadOlderTurns("local:a");
    expect(raw).toHaveBeenCalledTimes(2);
    expect(older).toHaveBeenCalled();
    expect(older.mock.calls[0]?.[1]).toEqual({ entry: 0, item: 0 });
    expect(threadsStore.getState().threads.get("local:a")?.olderCursor).toBeUndefined();
    expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
    expect(client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
    threadsStore.getState().releaseThread("local:a");
    background.release();
    vi.useRealTimers();
  },
);

it("a second foreground claim preserves its established reader without another local opening", async () => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const released = deferred<void>();
  const original = storage.getLatestWindow.bind(storage);
  const lookup = vi.spyOn(storage, "getLatestWindow").mockImplementation(async (...args) => {
    await released.promise;
    return original(...args);
  });
  try {
    await threadsStore.getState().ensureThread("local:a");
    expect(lookup).not.toHaveBeenCalled();
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["recorded"]);
  } finally {
    released.resolve();
    await Promise.all(lookup.mock.results.map((result) => result.value));
    threadsStore.getState().releaseThread("local:a");
    threadsStore.getState().releaseThread("local:a");
  }
});

it("existing background opening deadline releases paging hold and fences a late local result", async () => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect();
  client.on("thread/read", (params) => ({ ...response(), requestGeneration: params.requestGeneration }));
  const background = acquireBackgroundThread("local:a");
  await background.ready;
  await settleEntryTranscriptForTests("local:a");
  const entered = deferred<void>();
  const released = deferred<void>();
  const original = storage.getLatestWindow.bind(storage);
  const lookup = vi.spyOn(storage, "getLatestWindow").mockImplementationOnce(async (...args) => {
    const window = await original(...args);
    entered.resolve();
    await released.promise;
    return window;
  });
  client.on("thread/read", (params) => ({
    ...response("local:a", [turn("recovered", 2)]),
    authoritative: true,
    requestGeneration: params.requestGeneration,
  }));
  let settled = false;
  const opening = threadsStore
    .getState()
    .ensureThread("local:a")
    .then(() => {
      settled = true;
    });
  try {
    await entered.promise;
    expect(threadsStore.getState().localOpenings.has("local:a")).toBe(true);
    await vi.waitFor(() => expect(settled).toBe(true));
    expect(threadsStore.getState().localOpenings.has("local:a")).toBe(false);
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["recovered"]);
    released.resolve();
    await lookup.mock.results[0]?.value;
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["recovered"]);
  } finally {
    released.resolve();
    await opening;
    background.release();
  }
});

it("ordinary disk-only lookup deadline starts the source opening and a late lookup cannot replace it", async () => {
  await seedDiskOnlyOpening();
  const original = SessionEntryCache.prototype.getSessionForRequestedRef;
  let releaseLookup: (() => void) | undefined;
  const lookups = vi.spyOn(SessionEntryCache.prototype, "getSessionForRequestedRef").mockImplementation(async function (
    this: SessionEntryCache,
    clientId,
    ref,
  ) {
    const row = await original.call(this, clientId, ref);
    await new Promise<void>((resolve) => {
      releaseLookup = resolve;
    });
    return row;
  });
  const client = connect(response("local:a", [turn("deadline-source", 50)]));
  const opening = threadsStore.getState().ensureThread("local:a");
  try {
    await opening;
    expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.history?.turns.map((turn) => turn.id),
    ).toEqual(["deadline-source"]);
    expect(releaseLookup).toBeDefined();
    releaseLookup?.();
    await Promise.all(lookups.mock.results.map((result) => result.value));
    await settleEntryTranscriptForTests("local:a");
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.history?.turns.map((turn) => turn.id),
    ).toEqual(["deadline-source"]);
  } finally {
    releaseLookup?.();
    await opening;
  }
});

it("ordinary headerless v3 entries display from cached identity without inventing a source instance", async () => {
  const storage = cache();
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  const sourceTurn = hydrateThread(response("local:a", [turn("headerless-body", 1)]), "local:a", storage.now())
    .turns[0];
  const item = sourceTurn?.items[0];
  if (!item || !sourceTurn) throw new Error("missing fixture item");
  expect(
    await storage.commitBatch({
      historyKey: key,
      clearEpoch: 0,
      items: [
        {
          historyKey: key,
          transcriptKey: "headerless-body",
          position: { entry: 1, item: 0 },
          version: 1,
          item: encodeTranscriptItem(item),
        },
      ],
      turns: [{ ...sourceTurn, items: [] }],
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
  const client = connect();
  client.on("thread/read", () => {
    throw new WireError("offline", -32000, { evenerErrorInfo: "transcriptHistoryFailed" });
  });
  await threadsStore.getState().ensureThread("local:a");
  expect(threadsStore.getState().threads.get("local:a")).toMatchObject({
    threadId: "",
    imageSessionId: "session-a",
    capabilities: { send: false },
    turns: [{ items: [{ text: "headerless-body" }] }],
  });
  expect(client.calls.filter((call) => call.method === "thread/read")[0]?.params).not.toHaveProperty("heldSnapshot");
});

it("ordinary disk-only row-restore deadline disposes its binding before a late body can publish", async () => {
  await seedDiskOnlyOpening();
  const original = SessionEntryCache.prototype.getLatest;
  let releaseRows: (() => void) | undefined;
  const lookups = vi.spyOn(SessionEntryCache.prototype, "getLatest").mockImplementation(async function <T>(
    this: SessionEntryCache,
    key: string,
    limit: number,
  ): Promise<TranscriptItemRow<T>[]> {
    const rows = (await original.call(this, key, limit)) as TranscriptItemRow<T>[];
    await new Promise<void>((resolve) => {
      releaseRows = resolve;
    });
    return rows;
  });
  const client = connect(response("local:a", [turn("row-deadline-source", 50)]));
  const opening = threadsStore.getState().ensureThread("local:a");
  try {
    await opening;
    expect(releaseRows).toBeDefined();
    expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.history?.turns.map((turn) => turn.id),
    ).toEqual(["row-deadline-source"]);
    releaseRows?.();
    await Promise.all(lookups.mock.results.map((result) => result.value));
    await settleEntryTranscriptForTests("local:a");
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.history?.turns.map((turn) => turn.id),
    ).toEqual(["row-deadline-source"]);
  } finally {
    releaseRows?.();
    await opening;
  }
});

it("ordinary disk-only restore refuses a captured body after a silent durable history deletion", async () => {
  const storage = await seedDiskOnlyOpening();
  const original = SessionEntryCache.prototype.getTurn;
  let releaseTurns: (() => void) | undefined;
  let markHeld: (() => void) | undefined;
  const gate = new Promise<void>((resolve) => {
    releaseTurns = resolve;
  });
  const held = new Promise<void>((resolve) => {
    markHeld = resolve;
  });
  vi.spyOn(SessionEntryCache.prototype, "getTurn").mockImplementation(async function (
    this: SessionEntryCache,
    key,
    turnId,
  ) {
    const row = await original.call(this, key, turnId);
    markHeld?.();
    await gate;
    return row;
  });
  const client = connect();
  let finishRead: (() => void) | undefined;
  client.on(
    "thread/read",
    (params) =>
      new Promise((resolve) => {
        finishRead = () =>
          resolve({
            ...response("local:a", [turn("post-delete-source", 50)]),
            requestGeneration: params.requestGeneration,
          });
      }),
  );
  const opening = threadsStore.getState().ensureThread("local:a");
  await held;
  try {
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    await storage.deleteHistory(key); // No channel message or store-side retirement.
    expect(await storage.getHistoryGeneration(key)).toBe(1);
    releaseTurns?.();
    await vi.waitFor(() => expect(finishRead).toBeDefined());
    expect(threadsStore.getState().threads.has("local:a")).toBe(false);
  } finally {
    releaseTurns?.();
    finishRead?.();
    await opening;
  }
});

it("includeTurns watches preserve recorded snapshot and notification bodies", async () => {
  const client = connect();
  await threadsStore.getState().watchThread("local:a", { includeTurns: true });
  const texts = () =>
    threadsStore
      .getState()
      .watchedThreads.get("local:a")
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text);
  expect(texts()).toEqual(["recorded"]);
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("live-recorded", 2).items,
    },
  });
  expect(texts()).toEqual(["recorded", "live-recorded"]);
  threadsStore.getState().releaseWatchedThread("local:a");
});

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
  expect(await factory.databases()).toEqual([
    { name: SESSION_CACHE_DATABASE_NAME, version: SESSION_CACHE_DATABASE_VERSION },
  ]);
});

it("entry store repairs aggregate overflow through an automatic authoritative read", async () => {
  const restore = installBackgroundTranscriptOptionsForTests({ pendingBytes: 2048 });
  const storage = cache();
  setProductionEntryCacheForTests(storage);
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
    await settleEntryTranscriptForTests("local:a");
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
    resetThreadsStoreForTests();
    restore();
  }
});

it("opening an overflow-dropped window requests the now-available source", async () => {
  const restore = installBackgroundTranscriptOptionsForTests({ pendingBytes: 2048 });
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect();
  client.on("thread/read", (params) => ({ ...response(), requestGeneration: params.requestGeneration }));
  const admission = entryWriteAdmissionFor(storage);
  const owner = {};
  const bodies = [{ key: {}, bytes: 2048 }];
  expect(admission.retain(bodies, owner, "writing")).toBe(true);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const background = acquireBackgroundThread("local:a");
  try {
    await background.ready;
    await expect(settleEntryTranscriptForTests("local:a")).rejects.toBeInstanceOf(Error);
    expect(
      productionEntryTranscriptForTests("local:a").repository?.getRecoveryStateForTesting().cacheContinuityLost,
    ).toBe(true);
    expect(productionEntryTranscriptForTests("local:a").repository?.retainedInventoryForTesting()).toMatchObject({
      retryBodies: [],
      backgroundAcceptBodies: [],
      inFlightBodies: [],
    });
    expect(await storage.getLatest(entryHistoryKey({ ...identity, clientId: location.origin }), 10)).toEqual([]);
    await vi.advanceTimersByTimeAsync(250);
    await expect(settleEntryTranscriptForTests("local:a")).rejects.toBeInstanceOf(Error);
    expect(
      productionEntryTranscriptForTests("local:a").repository?.getRecoveryStateForTesting().cacheContinuityLost,
    ).toBe(true);
    expect(productionEntryTranscriptForTests("local:a").repository?.retainedInventoryForTesting()).toMatchObject({
      retryBodies: [],
      backgroundAcceptBodies: [],
      inFlightBodies: [],
    });
    expect(await storage.getLatest(entryHistoryKey({ ...identity, clientId: location.origin }), 10)).toEqual([]);
    const reads = client.calls.filter((call) => call.method === "thread/read").length;
    expect(reads).toBe(2);
    expect(
      productionEntryTranscriptForTests("local:a")
        .repository?.getSnapshot()
        ?.history?.turns.flatMap((turn) => turn.items),
    ).toEqual([]);
    admission.release(bodies, owner);
    await threadsStore.getState().ensureThread("local:a");
    expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads + 1);
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["recorded"]);
  } finally {
    admission.release(bodies, owner);
    threadsStore.getState().releaseThread("local:a");
    background.release();
    vi.useRealTimers();
    resetThreadsStoreForTests();
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
  setProductionEntryCacheForTests(storage);
  try {
    connect();
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect((await storage.getLatest(historyKey, 1)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
    expect(await storage.getSessionMetadata(historyKey)).toMatchObject({ savedAt: now, accessedAt: now });
  } finally {
    resetThreadsStoreForTests();
  }
});

it("ordinary older paging restores the bodies omitted by a 200-row local opening without a server cursor", async () => {
  const storage = cache();
  const ref = "local:a";
  const read = response(
    ref,
    Array.from({ length: 225 }, (_, index) => turn(`row-${index + 1}`, index + 1)),
  );
  read.snapshot = { incarnation: "inc-a", length: 225 };
  const client = connect(read);
  const lease = acquireBackgroundThread(ref);
  await lease.ready;
  await settleEntryTranscriptForTests(ref);
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect(await storage.getLatest(key, 300)).toHaveLength(225);
  const bodies = () =>
    threadsStore
      .getState()
      .threads.get(ref)
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text)
      .filter(Boolean);
  expect(bodies()).toEqual([]);
  await threadsStore.getState().ensureThread(ref);
  expect(bodies()).toEqual(Array.from({ length: 200 }, (_, index) => `row-${index + 26}`));
  // Supersedes undefined: rows 1..25 remain below the loaded 26..225
  // window, so the display must expose local demand until they are restored.
  expect(threadsStore.getState().threads.get(ref)?.olderCursor).toBeDefined();
  const readsBefore = client.calls.filter((call) => call.method === "thread/read").length;
  await threadsStore.getState().loadOlderTurns(ref);
  expect(bodies()).toEqual(Array.from({ length: 225 }, (_, index) => `row-${index + 1}`));
  expect(threadsStore.getState().threads.get(ref)?.olderCursor).toBeUndefined();
  await threadsStore.getState().loadOlderTurns(ref);
  expect(client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBefore);
  threadsStore.getState().releaseThread(ref);
  await lease.release();
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
    { name: "evener-mutation-outbox", version: 4 },
    { name: SESSION_CACHE_DATABASE_NAME, version: SESSION_CACHE_DATABASE_VERSION },
  ]);
});

// The 6d removal dropped the legacy stores, so the entry stores are the only
// object stores the cache database holds.
async function cacheStoreNames(): Promise<string[]> {
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = factory.open(SESSION_CACHE_DATABASE_NAME);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  try {
    return Array.from(database.objectStoreNames);
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
    expect((await cacheStoreNames()).sort()).toEqual(
      ["transcriptCoverage", "transcriptItems", "transcriptMeta", "transcriptTurns"].sort(),
    );
    expect(await lease.release()).toEqual({ kind: "saved" });
    expect((await cacheStoreNames()).sort()).toEqual(
      ["transcriptCoverage", "transcriptItems", "transcriptMeta", "transcriptTurns"].sort(),
    );
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

it.each(["goal", "note"] as const)(
  "Clear replay preserves newer response-only %s and reconciles metadata",
  async (field) => {
    const storage = cache();
    setProductionEntryCacheForTests(storage);
    const client = connect();
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    const held = deferred<void>();
    vi.spyOn(storage, "clear").mockImplementation(async () => {
      await held.promise;
      return { committed: false, epoch: 0 };
    });
    const clearing = clearCachedSessions();
    expect(threadsStore.getState().clearInFlight).toBeDefined();
    if (field === "goal") {
      client.emitNotification({
        method: "evener/goal/updated",
        params: {
          ref: "local:a",
          threadId: "thread-a",
          goal: { objective: "old", status: "active", iterations: 0 },
        },
      });
      client.on("goal/set", () => ({ started: true }));
      await threadsStore.getState().setGoal("local:a", "new");
    } else {
      client.emitNotification({
        method: "evener/notes/updated",
        params: { ref: "local:a", threadId: "thread-a", humanNote: "old" },
      });
      client.on("notes/human/set", (params) => ({
        note: params.note ?? "",
        receipt: {
          clientMutationId: params.clientMutationId,
          threadId: "thread-a",
          disposition: "applied",
          projectionState: "notProjected",
        },
      }));
      await threadsStore.getState().setHumanNote("local:a", "new");
      await vi.waitFor(() => expect(threadsStore.getState().threads.get("local:a")?.humanNote).toBe("new"));
    }
    const value = () => {
      const model = threadsStore.getState().threads.get("local:a");
      return field === "goal" ? model?.goal?.objective : model?.humanNote;
    };
    expect(value()).toBe("new");
    const reads = client.calls.filter((call) => call.method === "thread/read").length;
    const refresh = deferred<ThreadReadResponse>();
    client.on("thread/read", () => refresh.promise);
    try {
      held.resolve();
      expect(await clearing).toEqual({ committed: false });
      expect.soft(value()).toBe("new");
      expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads + 1);
    } finally {
      refresh.resolve(response());
    }
  },
);

it.each([true, false])("input suppressed during Clear is reconciled after committed=%s", async (committed) => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  const held = deferred<void>();
  const originalClear = storage.clear.bind(storage);
  vi.spyOn(storage, "clear").mockImplementation(async () => {
    await held.promise;
    return committed ? originalClear() : { committed: false, epoch: 0 };
  });
  const clearing = clearCachedSessions();
  expect(threadsStore.getState().clearInFlight).toBeDefined();
  const latest = response("local:a", [turn("recorded"), turn("during-clear", 2, "new recorded body")]);
  const slot = {
    key: "clear-stream",
    kind: "stream" as const,
    streamId: "clear-stream",
    roundId: "clear-round",
    turnId: "clear-turn",
    anchor: { entry: 3, item: 0 },
    item: { id: "clear-output", type: "agentMessage" as const, text: "active output" },
  };
  latest.overlay = [{ ...slot, item: { ...slot.item, text: "active output plus delta" } }];
  client.on("thread/read", (params) => ({ ...latest, requestGeneration: params.requestGeneration }));
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 3 },
      items: latest.thread.turns?.[1]?.items,
    },
  });
  client.emitNotification({ method: "overlay/upserted", params: { ref: "local:a", threadId: "thread-a", item: slot } });
  client.emitNotification({
    method: "overlay/delta",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      key: slot.key,
      field: "text",
      delta: " plus delta",
    },
  });
  const reads = client.calls.filter((call) => call.method === "thread/read").length;
  held.resolve();
  expect(await clearing).toEqual({ committed });
  await vi.waitFor(() => {
    expect(
      threadsStore
        .getState()
        .threads.get("local:a")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["recorded", "new recorded body", "active output plus delta"]);
  });
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads + 1);
  await settleEntryTranscriptForTests("local:a");
  if (committed) {
    expect(await storage.getLatest(key, 10)).toEqual([]);
    expect(threadsStore.getState().cacheLifetimes.get("local:a")?.suppressed).toBe(true);
  } else {
    expect((await storage.getLatest<CachedSessionItem>(key, 10)).map((row) => row.item.text)).toEqual([
      "recorded",
      "new recorded body",
    ]);
  }
});

it.each(["before", "after", "failed-after"])(
  "committed Clear cancels queued background persistence, predecessor=%s",
  async (ordering) => {
    const settlesFirst = ordering === "before";
    const storage = cache();
    setProductionEntryCacheForTests(storage);
    const client = connect();
    const background = acquireBackgroundThread("local:a");
    await background.ready;
    await settleEntryTranscriptForTests("local:a");
    const repository = productionEntryTranscriptForTests("local:a").repository;
    if (!repository) throw new Error("expected background repository");
    const admission = entryWriteAdmissionFor(storage);
    const heldWrite = deferred<void>();
    const heldClear = deferred<void>();
    const realCommit = storage.commitBatch.bind(storage);
    const writes = vi.spyOn(storage, "commitBatch").mockImplementation(async (batch) => {
      if (batch.items.some((row) => row.transcriptKey === "predecessor")) {
        await heldWrite.promise;
        if (ordering === "failed-after") return { outcome: "failed", error: new Error("transaction failed") };
      }
      return realCommit(batch);
    });
    const send = (id: string, entry: number) =>
      client.emitNotification({
        method: "history/updated",
        params: {
          ref: "local:a",
          threadId: "thread-a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-a", length: entry + 2 },
          items: turn(id, entry).items,
        },
      });
    send("predecessor", 3);
    await vi.waitFor(() => expect(writes).toHaveBeenCalledTimes(1));
    const issuedBytes = admission.retainedBytes;
    expect(issuedBytes).toBeGreaterThan(0);
    send("successor", 4);
    expect(repository.retainedInventoryForTesting().backgroundAcceptBodies.length).toBeGreaterThan(0);
    expect(admission.retainedBytes).toBeGreaterThan(issuedBytes);
    // Keep a display owner without creating further hydration demand.
    const reader = repository.attachReader();
    const displayed = repository.getSnapshot();
    const realClear = storage.clear.bind(storage);
    vi.spyOn(storage, "clear").mockImplementation(async () => {
      await heldClear.promise;
      return realClear();
    });
    const clearing = clearCachedSessions();
    try {
      if (settlesFirst) {
        heldWrite.resolve();
        await vi.waitFor(() => expect(repository.retainedInventoryForTesting().inFlightBodies).toEqual([]));
      }
      heldClear.resolve();
      expect(await clearing).toEqual({ committed: true });
      expect(repository.retainedInventoryForTesting().backgroundAcceptBodies).toEqual([]);
      expect(repository.retainedInventoryForTesting().retryBodies).toEqual([]);
      expect(repository.retainedInventoryForTesting().pendingRows).toEqual([]);
      expect(admission.retainedBytes).toBe(settlesFirst ? 0 : issuedBytes);
      expect(writes).toHaveBeenCalledTimes(1);
      expect(repository.getSnapshot()).toEqual(displayed);
      const other = {};
      expect(
        admission.tryAcquireBackground(
          other,
          () => false,
          () => {},
        ),
      ).toBe(settlesFirst);
      if (settlesFirst) admission.releaseBackground(other);
      else admission.cancelBackground(other);
      heldWrite.resolve();
      await expect(settleEntryTranscriptForTests("local:a")).rejects.toBeInstanceOf(Error);
      expect(await storage.getLatest(entryHistoryKey({ ...identity, clientId: location.origin }), 10)).toEqual([]);
      expect(admission.retainedBytes).toBe(0);
      expect(
        admission.tryAcquireBackground(
          other,
          () => false,
          () => {},
        ),
      ).toBe(true);
      admission.releaseBackground(other);
      expect(writes).toHaveBeenCalledTimes(1);
      expect(repository.getSnapshot()).toEqual(displayed);
    } finally {
      heldWrite.resolve();
      heldClear.resolve();
      reader.dispose();
      background.release();
    }
  },
);

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

it("an aborted ordinary Clear replays recorded input delivered while Clear was pending", async () => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect();
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");

  // Hold the clear transaction open so a recorded update lands inside the
  // window, then abort the real transaction: nothing changed, so the update
  // must survive without a release or reopen.
  const realClear = storage.clear.bind(storage);
  const clearGate = deferred<void>();
  const clearSpy = vi.spyOn(storage, "clear").mockImplementation(async () => {
    await clearGate.promise;
    return realClear();
  });
  const clearing = clearCachedSessions();
  await vi.waitFor(() => expect(threadsStore.getState().clearInFlight).toBeDefined());
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("during-clear", 2, "DURING_CLEAR_BODY").items,
    },
  });
  await settleProjectionWorkForTests();
  // Nothing is published while the clear is pending.
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a") ?? null)).not.toContain("DURING_CLEAR_BODY");

  const originalClear = IDBObjectStore.prototype.clear;
  const fault = vi.spyOn(IDBObjectStore.prototype, "clear").mockImplementationOnce(function (this: IDBObjectStore) {
    this.transaction.abort();
    return originalClear.call(this);
  });
  clearGate.resolve();
  await expect(clearing).resolves.toEqual({ committed: false });
  fault.mockRestore();
  clearSpy.mockRestore();

  await settleEntryTranscriptForTests("local:a");
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a") ?? null)).toContain("DURING_CLEAR_BODY");
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toContain("during-clear");
});

it.each(["reject", "stall"])("captured deletion is independent of metadata discovery: %s", async (mode) => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect(await storage.getLatest(key, 10)).toHaveLength(1);
  const discovery = deferred<string[]>();
  vi.spyOn(storage, "historyKeysForRequestedRefs").mockImplementation(() =>
    mode === "reject" ? Promise.reject(new Error("metadata unavailable")) : discovery.promise,
  );
  client.on("evener/session/delete", () => ({
    deleted: ["a"],
    skipped: [],
    navigation: { generation_id: "generation-test", targets: [] },
  }));
  try {
    await deleteSession(client, "local:a");
    expect(threadsStore.getState().deletedRefs.has("local:a")).toBe(true);
    await vi.waitFor(async () => expect(await storage.getHistoryGeneration(key)).toBe(1));
    expect(await storage.getLatest(key, 10)).toEqual([]);
  } finally {
    discovery.resolve([]);
    await settleProjectionWorkForTests();
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
  expect((await cacheStoreNames()).sort()).toEqual(
    ["transcriptCoverage", "transcriptItems", "transcriptMeta", "transcriptTurns"].sort(),
  );
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

it("a page issued before deletion does not publish after the deletion fence retires its owner", async () => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect();
  const latest = response("local:a", [turn("window", 30)]);
  latest.snapshot = { incarnation: "inc-a", length: 60 };
  latest.olderCursor = "page-1";
  client.on("thread/read", (params) => ({ ...latest, requestGeneration: params.requestGeneration }));
  const pageGate = deferred<void>();
  client.on("thread/turns/list", async () => {
    await pageGate.promise;
    return {
      data: [turn("deleted-late", 10, "DELETED_LATE_PAGE_BODY")],
      nextCursor: "page-2",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 60 },
    };
  });
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");

  const paging = threadsStore.getState().loadOlderTurns("local:a");
  markCacheSessionsDeleted(["local:a"]);
  await vi.waitFor(() => expect(threadsStore.getState().deletedRefs.has("local:a")).toBe(true));
  // Return the page that was already in flight when the deletion fence landed.
  pageGate.resolve();
  await paging;
  expect(JSON.stringify(threadsStore.getState().threads.get("local:a") ?? null)).not.toContain(
    "DELETED_LATE_PAGE_BODY",
  );
  expect(
    (await storage.getLatest(entryHistoryKey({ ...identity, clientId: location.origin }), 10)).map(
      (row) => row.transcriptKey,
    ),
  ).not.toContain("deleted-late");
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

it("automatic outbox discovery after Clear stays suppressed until explicit foreground acquisition", async () => {
  const storage = cache();
  const client = connect();
  client.on("thread/read", (params) => ({ ...response(), requestGeneration: params.requestGeneration }));
  client.on("turn/queue", () => new Promise(() => {}));
  const background = acquireBackgroundThread("local:a");
  await background.ready;
  await settleEntryTranscriptForTests("local:a");
  expect(await clearCachedSessions()).toEqual({ committed: true });
  const outbox = new MutationOutboxIndexedDB({ indexedDB: factory, createMutationId: () => "post-clear-auto-pin" });
  try {
    await outbox.enqueueIntent({
      targetRef: "local:a",
      method: "turn/queue",
      payload: { ref: "local:a", expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
      attachments: [],
      optimisticDisplay: { text: "queued" },
    });
    const reads = client.calls.filter((call) => call.method === "thread/read").length;
    client.emitStateChange("reconnecting");
    client.emitStateChange("ready");
    await settleProjectionWorkForTests();
    await vi.waitFor(() =>
      expect(client.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThan(reads),
    );
    await settleEntryTranscriptForTests("local:a");
    expect(threadsStore.getState().cacheLifetimes.get("local:a")?.suppressed).toBe(true);
    expect(threadsStore.getState().cacheLifetimes.get("local:a")?.freshForegroundLifetime).toBeUndefined();
    const key = entryHistoryKey({ ...identity, clientId: location.origin });
    expect(await storage.getLatest(key, 10)).toEqual([]);
    await threadsStore.getState().ensureThread("local:a");
    await settleEntryTranscriptForTests("local:a");
    expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
    threadsStore.getState().releaseThread("local:a");
  } finally {
    background.release();
    outbox.close();
  }
});

it("a fresh foreground lifetime with a surviving background owner caches the cleared epoch", async () => {
  const storage = cache();
  const client = connect();
  client.on("thread/read", (params) => ({ ...response(), requestGeneration: params.requestGeneration }));
  const background = acquireBackgroundThread("local:a");
  await background.ready;
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  expect(await clearCachedSessions()).toEqual({ committed: true });
  expect(await storage.getClearEpoch()).toBe(1);
  await threadsStore.getState().releaseThreadAndWait("local:a");
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
  background.release();
});

it("automatic hydration observes a missed sibling Clear without recaching a continuously held pane", async () => {
  const storage = cache();
  const client = connect();
  client.on("thread/read", (params) => ({ ...response(), requestGeneration: params.requestGeneration }));
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  expect(await storage.clear()).toEqual({ committed: true, epoch: 1 });
  await threadsStore.getState().refreshThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  expect(threadsStore.getState().cacheLifetimes.get("local:a")?.suppressed).toBe(true);
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
  expect(await storage.getLatest(key, 10)).toEqual([]);
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

it("ordinary disk-only read-proven deletion keeps cached content visible while healing entry storage and broadcasting once", async () => {
  const storage = await seedDiskOnlyOpening();
  const key = entryHistoryKey({ ...identity, clientId: location.origin });
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
  let rejectRead: (() => void) | undefined;
  let sourcePending = false;
  client.on(
    "thread/read",
    () =>
      new Promise((_, reject) => {
        sourcePending = true;
        rejectRead = () => {
          sourcePending = false;
          reject(
            new WireError("target deleted", -32001, {
              evenerErrorInfo: "actionUnavailable",
              mutationOutcome: "targetDeleted",
              retryDisposition: "none",
            }),
          );
        };
      }),
  );
  const opening = threadsStore.getState().ensureThread("local:a");
  const visible = () =>
    threadsStore
      .getState()
      .threads.get("local:a")
      ?.history?.turns.map((turn) => turn.id);
  try {
    await vi.waitFor(() => {
      expect(sourcePending).toBe(true);
      expect(visible()).toEqual(["window-1", "window-2", "window-3"]);
    });
    expect(threadsStore.getState().threads.get("local:a")?.turns[0]?.items[0]?.text).toBe("window-1");
    expect(await storage.getLatest(key, 200)).toHaveLength(3);
    if (!rejectRead) throw new Error("deletion read was not issued");
    rejectRead();
    await opening;
    await settleProjectionWorkForTests();
    expect(visible()).toEqual(["window-1", "window-2", "window-3"]);
    expect(threadsStore.getState().threads.get("local:a")?.turns[0]?.items[0]?.text).toBe("window-1");
    expect(threadsStore.getState().deletedRefs.has("local:a")).toBe(true);
    expect(await storage.getLatest(key, 200)).toEqual([]);
    expect(await countCachedSessions()).toBe(0);
    expect(posted).toEqual([{ version: 1, sourceId: expect.any(String), kind: "deletion", refs: ["local:a"] }]);
  } finally {
    rejectRead?.();
    await opening;
  }
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
  const sourceFailure = new WireError("source unavailable", -32000, { evenerErrorInfo: "transcriptHistoryFailed" });
  client.on("thread/read", () => {
    throw sourceFailure;
  });
  await expect(threadsStore.getState().refreshThread("local:a")).rejects.toBe(sourceFailure);
  await settleEntryTranscriptForTests("local:a");
  expect((await storage.getLatest(key, 10)).map((row) => row.transcriptKey)).toEqual(["recorded"]);
  expect(threadsStore.getState().threads.get("local:a")?.history?.failed).toBeDefined();
  client.emitNotification({
    method: "history/updated",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-a", length: 4 },
      items: turn("while-history-failed", 2).items,
    },
  });
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
  const commits = vi.spyOn(SessionEntryCache.prototype, "commitBatch");
  const oldClient = connect();
  await threadsStore.getState().ensureThread("local:a");
  await settleEntryTranscriptForTests("local:a");
  oldClient.emitNotification({
    method: "overlay/upserted",
    params: {
      ref: "local:a",
      threadId: "thread-a",
      item: {
        key: "outgoing-only",
        kind: "notice",
        anchor: { entry: 2, item: 0 },
        turnId: "recorded",
        item: { id: "outgoing-only", type: "agentMessage", text: "outgoing memory only" },
      },
    },
  });
  const outgoingModel = threadsStore.getState().threads.get("local:a");
  expect(JSON.stringify(outgoingModel)).toContain("outgoing memory only");
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
    // Ruling 34 preserves disk display, not the outgoing repository's memory.
    const cached = threadsStore.getState().threads.get("local:a");
    expect(cached).not.toBe(outgoingModel);
    expect(cached?.history?.turns.map((turn) => turn.id)).toEqual(["recorded"]);
    expect(cached?.history?.invalidatedAtGeneration).toBeDefined();
    expect(JSON.stringify(cached)).not.toContain("outgoing memory only");
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
    expect(
      commits.mock.calls.map(([batch]) => ({
        historyKey: batch.historyKey,
        rows: batch.items.map((row) => row.transcriptKey),
      })),
    ).toContainEqual({ historyKey: freshKey, rows: ["replacement-client-baseline"] });
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

const ref = "local:retirement";
const threadId = `thr_${ref}`;
function recorded(text = "recorded-stream"): ThreadItem {
  return {
    type: "agentMessage",
    id: "recorded",
    turnId: "turn_1",
    transcriptKey: "recorded",
    position: { entry: 41, item: 0 },
    version: 41,
    roundId: "r1",
    text,
  };
}
function readOf(items: ThreadItem[] = [], length = 40): ThreadReadResponse {
  const base = currentRead(ref);
  return {
    ...base,
    snapshot: { incarnation: "inc-1", length },
    thread: {
      ...base.thread,
      turns: [{ id: "turn_1", status: "completed", itemsView: "full", version: length, items }],
    },
  };
}
const end: AnyNotification = { method: "overlay/end", params: { ref, threadId, roundId: "r1" } };
const tool: AnyNotification = {
  method: "overlay/upserted",
  params: {
    ref,
    threadId,
    item: {
      key: "tool:old",
      kind: "tool",
      roundId: "r1",
      historyKey: "old-tool",
      turnId: "turn_1",
      anchor: { entry: 2, item: 0 },
      item: {
        id: "old-tool",
        type: "commandExecution",
        turnId: "turn_1",
        status: "inProgress",
        output: "provisional-tool",
      },
    },
  },
};

it("aborted Clear replays a delta only to the suppressed foreground owner, not its watcher", async () => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect(readOf());
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().watchThread(ref, { includeTurns: true });
  client.emitNotification(tool);
  const clearing = deferred<{ committed: false; epoch: number }>();
  vi.spyOn(storage, "clear").mockReturnValueOnce(clearing.promise);
  const clear = clearCachedSessions();
  client.emitNotification({
    method: "overlay/delta",
    params: { ref, threadId, key: "tool:old", field: "output", delta: "-once" },
  });
  await settleProjectionWorkForTests();
  const output = (watched: boolean) => {
    const state = threadsStore.getState();
    const model = (watched ? state.watchedThreads : state.threads).get(ref);
    return model?.overlay?.["tool:old"]?.item.output;
  };
  expect(output(true)).toEqual("provisional-tool-once");
  expect(output(false)).toEqual("provisional-tool");
  clearing.resolve({ committed: false, epoch: 0 });
  expect(await clear).toEqual({ committed: false });
  await settleProjectionWorkForTests();
  expect(output(false)).toEqual("provisional-tool-once");
  expect(output(true)).toEqual("provisional-tool-once");
});

it("aborted Clear preserves status accepted during suppression after replaying content", async () => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = connect(readOf());
  await threadsStore.getState().ensureThread(ref);
  client.emitNotification({ method: "thread/status/changed", params: { ref, threadId, status: { type: "active" } } });
  const clearing = deferred<{ committed: false; epoch: number }>();
  vi.spyOn(storage, "clear").mockReturnValueOnce(clearing.promise);
  const clear = clearCachedSessions();
  client.emitNotification(tool);
  client.emitNotification({ method: "thread/status/changed", params: { ref, threadId, status: { type: "idle" } } });
  await settleProjectionWorkForTests();
  expect(threadsStore.getState().threads.get(ref)?.status).toEqual({ type: "idle" });
  clearing.resolve({ committed: false, epoch: 0 });
  expect(await clear).toEqual({ committed: false });
  await settleProjectionWorkForTests();
  expect(threadsStore.getState().threads.get(ref)?.status).toEqual({ type: "idle" });
  expect(productionEntryTranscriptForTests(ref)?.repository?.getSnapshot()?.status).toEqual({ type: "idle" });
  expect(threadsStore.getState().threads.get(ref)?.overlay?.["tool:old"]?.item.output).toBe("provisional-tool");
});

it.each([false, true])(
  "Clear replay is budgeted and overflow recovery follows the Clear outcome, committed=%s",
  async (committed) => {
    const restore = installBackgroundTranscriptOptionsForTests({ pendingBytes: 64_000 });
    const storage = cache();
    setProductionEntryCacheForTests(storage);
    const client = connect(readOf());
    let reads = 0;
    client.on("thread/read", (params) => {
      reads += 1;
      return {
        ...(reads === 1 ? readOf() : readOf([recorded("recovered-after-clear")], 60)),
        requestGeneration: params.requestGeneration,
      };
    });
    const owner = acquireBackgroundThread(ref);
    await owner.ready;
    await settleEntryTranscriptForTests(ref);
    const admission = entryWriteAdmissionFor(storage);
    const gate = deferred<void>();
    const realClear = storage.clear.bind(storage);
    vi.spyOn(storage, "clear").mockImplementationOnce(async () => {
      await gate.promise;
      return committed ? realClear() : { committed: false, epoch: 0 };
    });
    const clear = clearCachedSessions();
    try {
      for (let index = 0; index < 20; index += 1) {
        client.emitNotification({
          method: "history/updated",
          params: {
            ref,
            threadId,
            bootGeneration: "1",
            epoch: 1,
            snapshot: { incarnation: "inc-1", length: 41 + index },
            items: [{ ...recorded("x".repeat(32_000)), version: 41 + index }],
          },
        });
        if (index === 0) expect(admission.snapshotForTesting().retainedBytes).toBeGreaterThan(32_000);
        expect(admission.snapshotForTesting().retainedBytes).toBeLessThanOrEqual(64_000);
        expect(admission.snapshotForTesting().exemptBytes).toBe(0);
      }
      expect(reads).toBe(1);
      gate.resolve();
      expect(await clear).toEqual({ committed });
      if (!committed) {
        await vi.waitFor(() => expect(reads).toBe(2));
        await settleEntryTranscriptForTests(ref);
        const key = entryHistoryKey({
          clientId: location.origin,
          hostId: "local",
          sessionId: readOf().thread.sessionId,
          incarnation: "inc-1",
        });
        expect(JSON.stringify(await storage.getLatest(key, 100))).toContain("recovered-after-clear");
      } else {
        // Supersede reads === 1: the incoming finding requires reconciliation
        // after committed Clear too. Its owner-fenced reconcile starts read 2;
        // this path discards replay owners and does not run overflow replay.
        // Background-only publication prunes history bodies, so visibility is
        // asserted by the foreground "input suppressed during Clear" test,
        // not by adding a foreground owner to this budget/disk-suppression case.
        await vi.waitFor(() => expect(reads).toBe(2));
        await settleEntryTranscriptForTests(ref);
        expect(await storage.count()).toBe(0);
      }
      // An aborted Clear replays the input its window suppressed, and that
      // replay can re-arm the overflow retry. The replay's replacement read and
      // the retry's replacement read then run in sequence for the same ref, so
      // its pending write drains after this point rather than before it. The
      // predicate is unchanged: a retained byte is still a failure, it just
      // gets to finish first.
      await vi.waitFor(() => expect(admission.snapshotForTesting().retainedBytes).toBe(0));
    } finally {
      gate.resolve();
      await clear;
      await owner.release();
      restore();
    }
  },
);

it("aborted Clear restarts a pending end read from the current owner", async () => {
  const storage = cache();
  setProductionEntryCacheForTests(storage);
  const client = new FakeClient("ready");
  const pendingEnd = deferred<ThreadReadResponse>();
  let reads = 0;
  client.on("thread/read", (params) => {
    reads += 1;
    return reads === 2
      ? pendingEnd.promise
      : {
          ...(reads === 1 ? readOf() : readOf([recorded("fresh-end")], 41)),
          requestGeneration: params.requestGeneration,
        };
  });
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const owner = acquireBackgroundThread(ref);
  await owner.ready;
  await settleEntryTranscriptForTests(ref);
  client.emitNotification(tool);
  client.emitNotification(end);
  await vi.waitFor(() => expect(reads).toBe(2));
  const clearing = deferred<{ committed: false; epoch: number }>();
  vi.spyOn(storage, "clear").mockReturnValueOnce(clearing.promise);
  const clear = clearCachedSessions();
  pendingEnd.resolve({ ...readOf([recorded("obsolete-end")], 41), overlay: [], requestGeneration: 2 });
  await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
  clearing.resolve({ committed: false, epoch: 0 });
  expect(await clear).toEqual({ committed: false });
  await vi.waitFor(() => expect(reads).toBe(3));
  await settleEntryTranscriptForTests(ref);
  const key = entryHistoryKey({
    clientId: location.origin,
    hostId: "local",
    sessionId: readOf().thread.sessionId,
    incarnation: "inc-1",
  });
  expect(JSON.stringify(await storage.getLatest(key, 100))).toContain("fresh-end");
  await owner.release();
});

it("background disk settlement does not await a pending end read", async () => {
  cache();
  const client = new FakeClient("ready");
  const pendingEnd = deferred<ThreadReadResponse>();
  let reads = 0;
  client.on("thread/read", () => (++reads === 2 ? pendingEnd.promise : readOf()));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const owner = acquireBackgroundThread(ref);
  await owner.ready;
  await settleEntryTranscriptForTests(ref);
  client.emitNotification(tool);
  client.emitNotification(end);
  await vi.waitFor(() => expect(reads).toBe(2));
  let settled = false;
  const settling = settleBackgroundTranscriptsForTests().then(() => {
    settled = true;
  });
  try {
    await vi.waitFor(() => expect(settled).toBe(true));
    expect(reads).toBe(2);
  } finally {
    pendingEnd.resolve({ ...readOf([recorded("end")], 41), overlay: [], requestGeneration: 2 });
    await settling;
    await owner.release();
  }
});

it("ordinary default reconnect handles the retired end-read rejection without an unhandled promise", async () => {
  const storage = cache();
  const key = entryHistoryKey({
    clientId: location.origin,
    hostId: "local",
    sessionId: readOf().thread.sessionId,
    incarnation: "inc-1",
  });
  const client = new FakeClient("ready");
  const pendingEnd = deferred<ThreadReadResponse>();
  let reads = 0;
  client.on("thread/read", () => (++reads === 2 ? pendingEnd.promise : readOf()));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const owner = acquireBackgroundThread(ref);
  await owner.ready;
  await settleEntryTranscriptForTests(ref);
  client.emitNotification(tool);
  client.emitNotification(end);
  await vi.waitFor(() => expect(reads).toBe(2));
  const releasing = owner.release();
  client.emitStateChange("reconnecting");
  client.emitReady();
  pendingEnd.resolve({ ...readOf([recorded("obsolete-end")], 41), overlay: [], requestGeneration: 2 });
  expect(await releasing).toEqual({ kind: "stale" });
  expect(reads).toBe(2);
  expect(await storage.getLatest(key, 100)).toEqual([]);
  const renewed = acquireBackgroundThread(ref);
  await renewed.ready;
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);
  expect(await renewed.release()).toEqual({ kind: "saved" });
});

it("an aborted Clear resumes a parked background end read without an unhandled rejection", async () => {
  const client = new FakeClient("ready");
  const pendingEnd = deferred<ThreadReadResponse>();
  let reads = 0;
  client.on("thread/read", () => (++reads === 2 ? pendingEnd.promise : readOf()));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const owner = acquireBackgroundThread(ref);
  await owner.ready;
  await settleEntryTranscriptForTests(ref);
  // The overlay/end starts the repository's background end-reconciliation read.
  client.emitNotification(tool);
  client.emitNotification(end);
  await vi.waitFor(() => expect(reads).toBe(2));

  // Abort a Clear while that read is parked. The resumed task must run after
  // its route is re-enabled, not reject through threads.ts:1006.
  const realClear = SessionEntryCache.prototype.clear;
  const clearGate = deferred<void>();
  const clearSpy = vi.spyOn(SessionEntryCache.prototype, "clear").mockImplementation(async function (
    this: SessionEntryCache,
  ) {
    await clearGate.promise;
    return realClear.call(this);
  });
  const clearing = clearCachedSessions();
  await vi.waitFor(() => expect(threadsStore.getState().clearInFlight).toBeDefined());
  const originalClear = IDBObjectStore.prototype.clear;
  const fault = vi.spyOn(IDBObjectStore.prototype, "clear").mockImplementationOnce(function (this: IDBObjectStore) {
    this.transaction.abort();
    return originalClear.call(this);
  });
  clearGate.resolve();
  await expect(clearing).resolves.toEqual({ committed: false });
  fault.mockRestore();
  clearSpy.mockRestore();

  pendingEnd.resolve({ ...readOf([recorded("obsolete-end")], 41), overlay: [], requestGeneration: 2 });
  await settleEntryTranscriptForTests(ref);
  await owner.release();
});
