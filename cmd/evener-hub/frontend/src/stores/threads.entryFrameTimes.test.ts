import type { MethodTypes, ThreadReadResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import { productionEntryTranscriptForTests } from "./entryTranscriptRoutes";
import { observePendingHydrationRetirementsForTests, resetThreadsStoreForTests, threadsStore } from "./threads";

beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
});
afterEach(() => {
  resetThreadsStoreForTests();
  resetConnectionStoreForTests();
  vi.unstubAllGlobals();
});

const root = "local:root";
const child = "local:child";
function packet(ref: string): ThreadReadResponse {
  const response = currentRead(ref);
  response.thread.status = { type: "idle" };
  response.thread.turns = [];
  delete response.thread.evener.activeTurnId;
  response.overlay = [];
  response.snapshot = { incarnation: "inc-1", length: 0 };
  delete response.olderCursor;
  return response;
}

test.each([true, false])(
  "ordinary accepted jobs metadata banks one frame without conversation progress, emit %s",
  async (emit) => {
    const rootInput = packet(root);
    const childInput = packet(child);
    const fake = new FakeClient("ready");
    fake.on("thread/read", (params) => ({
      ...(params.ref === root ? rootInput : childInput),
      requestGeneration: params.requestGeneration,
    }));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    await threadsStore.getState().ensureThread(root);
    await threadsStore.getState().ensureThread(child);
    const rootBefore = threadsStore.getState().threads.get(root);
    const childBefore = threadsStore.getState().threads.get(child);
    expect(rootBefore).toBeDefined();
    expect(childBefore).toBeDefined();
    expect(productionEntryTranscriptForTests(root).baseline).toBeDefined();
    expect(threadsStore.getState().frameTimes.get(root)).toBeUndefined();
    if (!emit) {
      expect(threadsStore.getState().threads.get(root)).toEqual(rootBefore);
      expect(threadsStore.getState().threads.get(child)).toEqual(childBefore);
      return;
    }
    fake.emitNotification({
      method: "evener/jobs/treeUpdated",
      params: { ref: root, threadId: rootInput.thread.id, revision: 7 },
    });
    const accepted = threadsStore.getState().threads.get(root);
    expect(accepted?.jobsTreeRevision).toBe(7);
    expect(productionEntryTranscriptForTests(root).model?.jobsTreeRevision).toBe(7);
    expect(accepted?.jobsUpdatedAt).not.toBeNull();
    expect(accepted?.lastFrameAt).toBe(rootBefore?.lastFrameAt);
    expect(threadsStore.getState().threads.get(child)).toEqual(childBefore);
    expect(threadsStore.getState().frameTimes.get(root)).toHaveLength(1);
    for (const revision of [7, 6])
      fake.emitNotification({
        method: "evener/jobs/treeUpdated",
        params: { ref: root, threadId: rootInput.thread.id, revision },
      });
    expect(threadsStore.getState().threads.get(root)?.jobsTreeRevision).toBe(7);
    expect(threadsStore.getState().frameTimes.get(root)).toHaveLength(1);
    expect(threadsStore.getState().frameTimes.get(child)).toBeUndefined();
    fake.emitNotification({
      method: "evener/jobs/treeUpdated",
      params: { ref: root, threadId: rootInput.thread.id, revision: 8 },
    });
    expect(threadsStore.getState().threads.get(root)?.jobsTreeRevision).toBe(8);
    expect(threadsStore.getState().frameTimes.get(root)).toHaveLength(2);
    expect(threadsStore.getState().threads.get(child)).toEqual(childBefore);
    fake.emitNotification({
      method: "evener/jobs/treeUpdated",
      params: { ref: child, threadId: childInput.thread.id, revision: 7 },
    });
    expect(threadsStore.getState().threads.get(child)?.jobsTreeRevision).toBe(7);
    expect(threadsStore.getState().frameTimes.get(child)).toHaveLength(1);
    expect(threadsStore.getState().frameTimes.get(root)).toHaveLength(2);
  },
);

test("ordinary after-cut metadata during a refresh banks once at snapshot publication", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => ({ ...packet(root), requestGeneration: params.requestGeneration }));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread(root);
  expect(productionEntryTranscriptForTests(root).baseline).toBeDefined();
  const read = deferred<ThreadReadResponse>();
  const reached = deferred<MethodTypes["thread/read"]["params"]>();
  fake.on("thread/read", (params) => {
    reached.resolve(params);
    return read.promise;
  });
  const retired: Array<{ retainedNotificationCount: number }> = [];
  const unsubscribe = observePendingHydrationRetirementsForTests((event) => retired.push(event));
  try {
    const refreshing = threadsStore.getState().refreshThread(root);
    const params = await reached.promise;
    const response = { ...packet(root), requestGeneration: params.requestGeneration };
    const cut = { reached: false };
    Object.defineProperty(response, "olderCursor", {
      configurable: true,
      enumerable: true,
      get: () => {
        cut.reached = true;
        return undefined;
      },
    });
    read.resolve(response);
    for (let i = 0; i < 20 && !cut.reached; i += 1) await Promise.resolve();
    expect(cut.reached, "the actual refresh response cut must run").toBe(true);
    fake.emitNotification({
      method: "evener/jobs/treeUpdated",
      params: { ref: root, threadId: response.thread.id, revision: 7 },
    });
    expect(productionEntryTranscriptForTests(root).model?.jobsTreeRevision).toBe(7);
    await refreshing;
    expect(retired.at(-1)?.retainedNotificationCount).toBe(1);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    expect(threadsStore.getState().threads.get(root)?.jobsTreeRevision).toBe(7);
    expect(threadsStore.getState().frameTimes.get(root)).toHaveLength(1);
  } finally {
    unsubscribe();
  }
});
