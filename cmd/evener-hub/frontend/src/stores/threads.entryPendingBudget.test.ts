import type { AnyNotification, CachedSessionItem, ThreadReadResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { entryWriteAdmissionFor } from "./entryWriteAdmission";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  observePendingHydrationRetirementsForTests,
  pendingThreadHydrationUpdatesForTests,
  productionEntryTranscriptForTests,
  resetThreadsStoreForTests,
  setProductionEntryCacheForTests,
  threadsStore,
} from "./threads";

const LIMIT = 4 * 1024 * 1024;
let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `entry-pending-budget-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  resetThreadsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cache.close();
});

function connectClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => ({
    ...currentRead(params.ref ?? "local:buffer-budget"),
    requestGeneration: params.requestGeneration,
  }));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

function completedFrame(ref: string, position: number) {
  return {
    method: "history/updated",
    params: {
      ref,
      threadId: `thr_${ref}`,
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: position },
      turns: [{ id: `turn_${position}`, status: "completed", itemsView: "full", version: 1, items: [] }],
      items: [
        {
          type: "agentMessage",
          id: `item_${position}`,
          turnId: `turn_${position}`,
          transcriptKey: `apptranscript-item-v2:turn_${position}:0:0`,
          position: { entry: position, item: 0 },
          version: 1,
          text: `${position}:`.padEnd(1024 * 1024, "x"),
          status: "completed",
        },
      ],
    },
  } satisfies AnyNotification;
}

function currentResponse(ref: string, frames: ReturnType<typeof completedFrame>[]): ThreadReadResponse {
  const initial = currentRead(ref);
  return {
    ...initial,
    snapshot: { incarnation: "inc-1", length: frames.at(-1)?.params.snapshot.length ?? 40 },
    thread: {
      ...initial.thread,
      turns: frames.map((frame) => ({ ...frame.params.turns[0]!, items: frame.params.items })),
    },
  };
}

function actualBufferedBytes(): number {
  return pendingThreadHydrationUpdatesForTests().reduce(
    (sum, update) => sum + new TextEncoder().encode(JSON.stringify(update.notification)).byteLength,
    0,
  );
}

async function expectDurableFrames(ref: string, frames: ReturnType<typeof completedFrame>[]): Promise<void> {
  const key = productionEntryTranscriptForTests(ref).historyKey;
  if (!key) throw new Error("expected the ordinary history key");
  const rows = await cache.getLatest<CachedSessionItem>(key, 10);
  expect(
    rows.map((row) => ({ key: row.transcriptKey, position: row.position, version: row.version, text: row.item.text })),
  ).toEqual(
    frames.map((frame) => {
      const item = frame.params.items[0]!;
      return { key: item.transcriptKey, position: item.position, version: item.version, text: item.text };
    }),
  );
}

test.each([false, true])("completed recorded input has a bounded real resync buffer, pending=%s", async (pending) => {
  const ref = "local:buffer-budget";
  const fake = connectClient();
  const background = acquireBackgroundThread(ref);
  const source = deferred<ThreadReadResponse>();
  const frames = Array.from({ length: 5 }, (_, index) => completedFrame(ref, 41 + index));
  const observations: Array<{ bytes: number; charged: number; count: number }> = [];
  try {
    await background.ready;
    const repository = productionEntryTranscriptForTests(ref).repository;
    if (!repository) throw new Error("expected the ordinary repository");
    await repository.settled();
    if (pending) {
      fake.on("thread/read", (params) =>
        source.promise.then((result) => ({ ...result, requestGeneration: params.requestGeneration })),
      );
      fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
      await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2));
    }
    for (const frame of frames) {
      fake.emitNotification(frame);
      await repository.settled();
      observations.push({
        bytes: actualBufferedBytes(),
        charged: backgroundTranscriptSnapshotForTests().pendingBytes,
        count: pendingThreadHydrationUpdatesForTests().length,
      });
    }
    // These writes have finished. The pending-read copy must still be charged,
    // rather than disappearing from accounting when write admission reaches 0.
    await expectDurableFrames(ref, frames);
    const admission = entryWriteAdmissionFor(cache).snapshotForTesting();
    expect(admission.limit).toBe(LIMIT);
    expect(admission.retainedBytes).toBe(0);
    expect(admission.exemptBytes).toBe(0);
    const bytes = actualBufferedBytes();
    expect(bytes).toBeLessThanOrEqual(LIMIT);
    expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(bytes);
    for (const observation of observations) {
      expect(observation.bytes).toBeLessThanOrEqual(LIMIT);
      expect(observation.charged).toBe(observation.bytes);
    }
    expect(observations.map((observation) => observation.count)).toEqual(pending ? [1, 2, 3, 0, 0] : [0, 0, 0, 0, 0]);
    expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(0);
    if (pending) {
      source.resolve(currentResponse(ref, frames));
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
      await repository.settled();
      expect(actualBufferedBytes()).toBe(0);
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(0);
      expect(productionEntryTranscriptForTests(ref).repository).toBe(repository);
      await expectDurableFrames(ref, frames);
    }
  } finally {
    source.resolve(currentResponse(ref, frames));
    await background.release();
  }
});

test("post-cut exhaustion cannot let the read overwrite the repository's latest accepted goal", async () => {
  const ref = "local:post-cut-exhaustion";
  const fake = connectClient();
  const background = acquireBackgroundThread(ref);
  const source = deferred<ThreadReadResponse>();
  try {
    await background.ready;
    const repository = productionEntryTranscriptForTests(ref).repository;
    if (!repository) throw new Error("expected the ordinary repository");
    fake.on("thread/read", (params) =>
      source.promise.then((result) => ({ ...result, requestGeneration: params.requestGeneration })),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
    await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2));
    const beforeCutGoal = { objective: "represented at the source cut", status: "active" as const, iterations: 0 };
    fake.emitNotification({
      method: "evener/goal/updated",
      params: { ref, threadId: `thr_${ref}`, goal: beforeCutGoal },
    });
    expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1);
    const response = currentRead(ref);
    response.thread.evener.goal = beforeCutGoal;
    const cut = { reached: false };
    Object.defineProperty(response, "olderCursor", {
      configurable: true,
      enumerable: true,
      get: () => {
        cut.reached = true;
        return "older-40";
      },
    });
    source.resolve(response);
    for (let index = 0; index < 30 && (!cut.reached || pendingThreadHydrationUpdatesForTests().length > 0); index++) {
      await Promise.resolve();
    }
    expect(cut.reached).toBe(true);
    expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(0);
    expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1);
    let objective = "";
    for (let index = 0; index < 5; index++) {
      objective = `${index}:`.padEnd(1024 * 1024, "g");
      fake.emitNotification({
        method: "evener/goal/updated",
        params: { ref, threadId: `thr_${ref}`, goal: { objective, status: "active", iterations: index } },
      });
    }
    expect(repository.getSnapshot()?.goal?.objective === objective, "the repository has the exact fifth goal").toBe(
      true,
    );
    await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
    expect(
      threadsStore.getState().threads.get(ref)?.goal?.objective === objective,
      "publication keeps the exact fifth goal",
    ).toBe(true);
    expect(repository.getSnapshot()?.goal?.objective === objective, "read adoption keeps the exact fifth goal").toBe(
      true,
    );
    expect(productionEntryTranscriptForTests(ref).repository).toBe(repository);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
  } finally {
    source.resolve(currentRead(ref));
    await background.release();
  }
});

test("entry buffer exhaustion retires only that read, preserving durable bodies and the next post-cut replay", async () => {
  const ref = "local:budget-post-cut";
  const fake = connectClient();
  const background = acquireBackgroundThread(ref);
  const source = deferred<ThreadReadResponse>();
  const nextSource = deferred<ThreadReadResponse>();
  const frames = Array.from({ length: 5 }, (_, index) => completedFrame(ref, 41 + index));
  const retirements: Array<{ ref: string; retainedNotificationCount: number; notificationCount: number }> = [];
  const restoreObserver = observePendingHydrationRetirementsForTests((event) => retirements.push(event));
  try {
    await background.ready;
    const repository = productionEntryTranscriptForTests(ref).repository;
    if (!repository) throw new Error("expected the ordinary repository");
    await repository.settled();
    retirements.length = 0;
    fake.on("thread/read", (params) =>
      source.promise.then((result) => ({ ...result, requestGeneration: params.requestGeneration })),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
    await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2));
    for (const frame of frames) {
      fake.emitNotification(frame);
      await repository.settled();
    }
    await expectDurableFrames(ref, frames);
    expect(retirements).toEqual([
      { ref, retainedNotificationCount: 3, notificationCount: 0, hadBaseModel: true, hasBaseModel: false },
    ]);
    expect(actualBufferedBytes()).toBe(0);
    expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(0);
    expect(
      backgroundTranscriptSnapshotForTests().networkHydrations,
      "logical retirement must not free the request slot",
    ).toBe(1);
    source.resolve(currentResponse(ref, frames));
    await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
    expect(productionEntryTranscriptForTests(ref).repository).toBe(repository);
    await expectDurableFrames(ref, frames);
    retirements.length = 0;
    fake.on("thread/read", (params) =>
      nextSource.promise.then((result) => ({ ...result, requestGeneration: params.requestGeneration })),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
    await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3));
    const beforeCutGoal = { objective: "represented in the next read", status: "active" as const, iterations: 2 };
    fake.emitNotification({
      method: "evener/goal/updated",
      params: { ref, threadId: `thr_${ref}`, goal: beforeCutGoal },
    });
    expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1);
    expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(actualBufferedBytes());
    const response = currentResponse(ref, frames);
    response.thread.evener.goal = beforeCutGoal;
    const cut = { reached: false };
    Object.defineProperty(response, "olderCursor", {
      configurable: true,
      enumerable: true,
      get: () => {
        cut.reached = true;
        return "older-40";
      },
    });
    nextSource.resolve(response);
    for (let index = 0; index < 30 && (!cut.reached || pendingThreadHydrationUpdatesForTests().length > 0); index++) {
      await Promise.resolve();
    }
    expect(cut.reached, "the actual response cut must run").toBe(true);
    expect(retirements, "the response must not have published yet").toEqual([]);
    expect(actualBufferedBytes(), "the cut releases the pre-cut buffer").toBe(0);
    expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(0);
    const goal = { objective: "after the exhausted buffer's response cut", status: "active" as const, iterations: 3 };
    fake.emitNotification({ method: "evener/goal/updated", params: { ref, threadId: `thr_${ref}`, goal } });
    expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1);
    expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(actualBufferedBytes());
    expect(actualBufferedBytes()).toBeGreaterThan(0);
    await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
    await repository.settled();
    expect(threadsStore.getState().threads.get(ref)?.goal).toEqual(goal);
    expect(repository.getSnapshot()?.goal).toEqual(goal);
    expect(productionEntryTranscriptForTests(ref).repository).toBe(repository);
    expect(retirements).toEqual([
      { ref, retainedNotificationCount: 1, notificationCount: 0, hadBaseModel: true, hasBaseModel: false },
    ]);
    expect(actualBufferedBytes()).toBe(0);
    expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(0);
    await expectDurableFrames(ref, frames);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);
  } finally {
    restoreObserver();
    source.resolve(currentResponse(ref, frames));
    nextSource.resolve(currentResponse(ref, frames));
    await background.release();
  }
});
