import type { ThreadReadResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { pendingThreadHydrationUpdatesForTests, resetThreadsStoreForTests, threadsStore } from "./threads";

const ref = "local:pre-cut-queue";
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
});
afterEach(() => {
  resetThreadsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  vi.unstubAllGlobals();
});

for (const bufferedRevision of [1, 2, 3, undefined]) {
  test(`ordinary resync uses the response queue instead of replaying a pre-cut queue, buffered ${bufferedRevision ?? "none"}`, async () => {
    const fake = new FakeClient("ready");
    const replacement = deferred<ThreadReadResponse>();
    let reads = 0;
    let generation = 0;
    const initial = currentRead(ref);
    initial.thread.evener.queue = { revision: 0 };
    fake.on("thread/read", (params) => {
      reads++;
      if (reads === 1) return { ...initial, requestGeneration: params.requestGeneration };
      generation = params.requestGeneration ?? 0;
      return replacement.promise;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    await threadsStore.getState().ensureThread(ref);
    expect(threadsStore.getState().threads.get(ref)?.queue).toEqual({ revision: 0 });
    fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: initial.thread.id } });
    await vi.waitFor(() => expect(reads).toBe(2));
    expect(generation).toBeGreaterThan(0);
    if (bufferedRevision !== undefined) {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: { ref, threadId: initial.thread.id, queue: { revision: bufferedRevision, preview: ["pre-cut"] } },
      });
      expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1);
    }
    const response = currentRead(ref);
    response.requestGeneration = generation;
    response.thread.name = "replacement";
    response.thread.evener.queue = { revision: 2, preview: ["snapshot"] };
    replacement.resolve(response);
    await vi.waitFor(() => expect(threadsStore.getState().threads.get(ref)?.name).toBe("replacement"));
    expect(reads).toBe(2);
    expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(0);
    expect(threadsStore.getState().threads.get(ref)?.queue).toEqual(
      bufferedRevision === 3 ? { revision: 3, preview: ["pre-cut"] } : { revision: 2, preview: ["snapshot"] },
    );
  });
}

for (const bufferedVersion of [0, 2]) {
  test(`ordinary opening merges pre-cut recorded input by item version ${bufferedVersion}`, async () => {
    const fake = new FakeClient("ready");
    const read = deferred<ThreadReadResponse>();
    let generation: number | undefined;
    const response = currentRead(ref);
    const item = {
      id: "item_before_cut",
      turnId: "turn_before_cut",
      type: "agentMessage" as const,
      status: "completed" as const,
      position: { entry: 0, item: 0 },
      transcriptKey: "entry:0/item:0",
      version: 1,
      text: "read version one",
    };
    response.snapshot = { incarnation: "inc-1", length: 1 };
    response.overlay = [];
    response.thread.turns = [{ id: "turn_before_cut", status: "completed", itemsView: "full", items: [item] }];
    fake.on("thread/read", (params) => {
      generation = params.requestGeneration;
      return read.promise;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const opening = threadsStore.getState().ensureThread(ref);
    await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1));
    fake.emitNotification({
      method: "history/updated",
      params: {
        ref,
        threadId: response.thread.id,
        bootGeneration: response.bootGeneration!,
        epoch: response.epoch!,
        snapshot: response.snapshot,
        turns: [{ id: "turn_before_cut", status: "completed", itemsView: "full" }],
        items: [{ ...item, version: bufferedVersion, text: "buffered version" }],
      },
    });
    expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1);
    response.requestGeneration = generation;
    read.resolve(response);
    await opening;
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(0);
    const recorded = threadsStore.getState().threads.get(ref)?.history?.turns[0]?.items[0];
    expect(recorded?.version).toBe(bufferedVersion === 2 ? 2 : 1);
    expect(recorded?.text).toBe(bufferedVersion === 2 ? "buffered version" : "read version one");
  });
}
