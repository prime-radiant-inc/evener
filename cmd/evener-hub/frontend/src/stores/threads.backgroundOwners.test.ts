import { appliedReceipt } from "../mutationReceiptTestUtils";
import "fake-indexeddb/auto";
import { type AnyNotification, acquireThreadSubscription, WireError } from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import { productionEntryTranscriptForTests, setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "./threads";

let cache: SessionEntryCache;
const mutationStorages: MutationOutboxIndexedDB[] = [];
const activityLeases: Array<ReturnType<typeof acquireThreadSubscription>> = [];

beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `background-entry-owners-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});

afterEach(() => {
  for (const lease of activityLeases.splice(0)) lease.release();
  resetThreadsStoreForTests();
  for (const storage of mutationStorages.splice(0)) storage.close();
  cache.close();
});

function mutationStorage(): MutationOutboxIndexedDB {
  const storage = new MutationOutboxIndexedDB({
    indexedDB: new IDBFactory(),
    databaseName: `background-outbox-${crypto.randomUUID()}`,
    createMutationId: () => `background-mutation-${crypto.randomUUID()}`,
  });
  mutationStorages.push(storage);
  setMutationStorageForTests(storage);
  return storage;
}

async function blockedOutboxRow(storage: MutationOutboxIndexedDB, ref: string) {
  const queued = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/queue",
    payload: { ref, expectedTurnId: "", input: [{ type: "text", text: "blocked queue" }] },
    attachments: [],
    optimisticDisplay: { text: "blocked queue" },
  });
  await storage.markAttempted(queued.clientMutationId);
  await storage.markUnknown(queued.clientMutationId, "blockedUnknown", { onlyAttempted: true });
  return storage.getOutbox(queued.clientMutationId);
}

function waitForHydration(ref: string, count: number): Promise<void> {
  if ((threadsStore.getState().hydrations.get(ref) ?? 0) >= count) return Promise.resolve();
  return new Promise((resolve) => {
    const unsubscribe = threadsStore.subscribe((state) => {
      if ((state.hydrations.get(ref) ?? 0) < count) return;
      unsubscribe();
      resolve();
    });
  });
}

function connectCurrentClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => ({
    ...currentRead(params.ref ?? "local:one"),
    requestGeneration: params.requestGeneration,
  }));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

function membershipReads(fake: FakeClient): number {
  return fake.calls.filter(
    (call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === true,
  ).length;
}

function unsubscriptions(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "thread/unsubscribe").length;
}

// Original owner/outbox assertions and timelines moved from threads.background.
// Only the real storage boundary and canonical optional read generation differ.
describe("background thread claims", () => {
  test("background-only hydration leaves durable mutations untouched and sends no mutation RPC", async () => {
    const storage = mutationStorage();
    await storage.enqueueIntent({
      targetRef: "local:one",
      method: "turn/queue",
      payload: { ref: "local:one", expectedTurnId: "", input: [{ type: "text", text: "saved queue" }] },
      attachments: [],
      optimisticDisplay: { text: "saved queue" },
    });
    const before = await storage.listOutbox("local:one");
    const fake = connectCurrentClient();

    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const after = await storage.listOutbox("local:one");
    expect(after).toEqual(before);
    expect(
      fake.calls.filter((call) => ["turn/start", "turn/steer", "turn/queue", "thread/resume"].includes(call.method)),
    ).toEqual([]);
    await background.release();
  });

  test("an explicit foreground send dispatches while a background claim remains active", async () => {
    mutationStorage();
    const fake = connectCurrentClient();
    const dispatched = deferred<{ input?: Array<{ type: string; text?: string }> }>();
    fake.on("turn/start", (params) => {
      dispatched.resolve(params);
      return {
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        receipt: appliedReceipt(params.clientMutationId, "thr_local:one"),
      };
    });

    const background = acquireBackgroundThread("local:one");
    await background.ready;
    await threadsStore.getState().send("local:one", "foreground send", [], [], [], []);
    const params = await dispatched.promise;
    expect(params.input).toContainEqual({ type: "text", text: "foreground send" });
    await background.release();
  });

  test("pane adopts a background-hydrated model and releases only its own claim", async () => {
    const fake = connectCurrentClient();
    const activity = acquireThreadSubscription(fake, "local:one");
    activityLeases.push(activity);
    await activity.ensure();

    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:one");
    await Promise.all([first.ready, second.ready]);
    const resident = threadsStore.getState().threads.get("local:one");
    // Healthy empty-source proof: the accepted read has no recorded bodies,
    // only a live overlay, and admission has lost no coverage. Unlike an
    // overflow-dropped empty model, this needs no recovery read on adoption.
    expect(resident?.history?.turns.flatMap((turn) => turn.items)).toEqual([]);
    expect(productionEntryTranscriptForTests("local:one").repository?.getRecoveryStateForTesting()).toMatchObject({
      cacheContinuityLost: false,
    });
    expect(
      resident?.turns
        .flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha");
    const readsBeforeAdoption = fake.calls.filter((call) => call.method === "thread/read").length;
    const subscribedReads = membershipReads(fake);
    expect(subscribedReads).toBe(1);

    await Promise.all([
      threadsStore.getState().ensureThread("local:one"),
      threadsStore.getState().ensureThread("local:one"),
    ]);
    // R45: adoption replaces the background projection with the canonical model.
    // This fixture prunes no substantive body, so only here do the values match.
    const adopted = threadsStore.getState().threads.get("local:one");
    expect(adopted).toStrictEqual(resident);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforeAdoption);
    expect(adopted).toBe(productionEntryTranscriptForTests("local:one").repository?.getSnapshot());
    expect(membershipReads(fake)).toBe(subscribedReads);

    const ownerCalls = fake.calls.length;
    await first.release();
    await second.release();
    expect(
      fake.calls
        .slice(ownerCalls)
        .filter((call) =>
          ["turn/start", "turn/steer", "turn/queue", "thread/resume", "thread/unsubscribe"].includes(call.method),
        ),
    ).toEqual([]);
    fake.emitNotification(textDelta("local:one", "-after-background-close"));
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-after-background-close");
    expect(unsubscriptions(fake)).toBe(0);

    threadsStore.getState().releaseThread("local:one");
    expect(unsubscriptions(fake)).toBe(0);
    const unsubscribed = deferred<void>();
    fake.on("thread/unsubscribe", () => {
      unsubscribed.resolve();
      return {};
    });
    threadsStore.getState().releaseThread("local:one");
    activity.release();
    await unsubscribed.promise;
    expect(unsubscriptions(fake)).toBe(1);
  });

  test("foreground release leaves background routing alive until its final claim", async () => {
    const fake = connectCurrentClient();
    const pane = threadsStore.getState().ensureThread("local:one");
    await pane;
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const reads = fake.calls.filter((call) => call.method === "thread/read").length;

    threadsStore.getState().releaseThread("local:one");
    fake.emitNotification(textDelta("local:one", "-live"));
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-live");
    expect(unsubscriptions(fake)).toBe(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);

    await background.release();
    expect(unsubscriptions(fake)).toBe(1);
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });

  test("releasing the last background claim retires a held read without publishing it", async () => {
    const fake = connectCurrentClient();
    const entered = deferred<void>();
    const held = deferred<ReturnType<typeof currentRead>>();
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) {
        entered.resolve();
        return held.promise.then((response) => ({ ...response, requestGeneration: params.requestGeneration }));
      }
      return { ...currentRead("local:one"), requestGeneration: params.requestGeneration };
    });
    const background = acquireBackgroundThread("local:one");
    try {
      await entered.promise;
      const readsBeforeRelease = membershipReads(fake);
      await background.release();
      await background.ready;
      held.resolve(currentRead("local:one"));
      await nextRealTask();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(membershipReads(fake)).toBe(readsBeforeRelease);
      expect(unsubscriptions(fake)).toBe(1);
    } finally {
      held.resolve(currentRead("local:one"));
      await background.release();
    }
  });

  test("a failed first attempt settles ready and leaves paced recovery armed", async () => {
    const scheduled = deferred<() => void>();
    const restore = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      scheduled.resolve(retry);
      return () => {};
    });
    try {
      const fake = connectCurrentClient();
      let attempts = 0;
      fake.on("thread/read", (params) => {
        attempts += 1;
        if (attempts === 1) throw new Error("temporary read failure");
        return { ...currentRead("local:one"), requestGeneration: params.requestGeneration };
      });
      const background = acquireBackgroundThread("local:one");
      await background.ready;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
      const retry = await scheduled.promise;
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      const hydrated = deferred<void>();
      const unsubscribe = threadsStore.subscribe((state) => {
        if (state.threads.has("local:one")) hydrated.resolve();
      });
      try {
        retry();
        await hydrated.promise;
      } finally {
        unsubscribe();
      }
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
      await background.release();
    } finally {
      restore();
    }
  });
  test("background retry does not reconcile or dispatch an uncertain durable row", async () => {
    const storage = mutationStorage();
    const retryScheduled = deferred<() => void>();
    const restore = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      retryScheduled.resolve(retry);
      return () => {};
    });
    const restoreSpy = vi.spyOn(storage, "restoreProvenAbsent");
    const initialScans = deferred<void>();
    const listTargetRefs = storage.listTargetRefs.bind(storage);
    let scanCount = 0;
    const scanSpy = vi.spyOn(storage, "listTargetRefs").mockImplementation(async () => {
      const refs = await listTargetRefs();
      scanCount += 1;
      if (scanCount === 3) initialScans.resolve();
      return refs;
    });
    try {
      const fake = connectCurrentClient();
      await initialScans.promise;
      await nextRealTask();
      const blocked = await blockedOutboxRow(storage, "local:one");
      let reads = 0;
      fake.on("thread/read", (params) => {
        reads += 1;
        if (reads === 1) throw new Error("temporary read failure");
        const response = currentRead("local:one");
        return {
          ...response,
          requestGeneration: params.requestGeneration,
          thread: {
            ...response.thread,
            evener: { ...response.thread.evener, mutationStateAuthoritative: true },
          },
        };
      });
      const background = acquireBackgroundThread("local:one");
      try {
        await background.ready;
        const hydration = waitForHydration("local:one", 1);
        const retry = await retryScheduled.promise;
        retry();
        await hydration;
        await nextRealTask();
        expect(restoreSpy).not.toHaveBeenCalled();
        await expect(storage.getOutbox(blocked?.clientMutationId ?? "missing")).resolves.toEqual(blocked);
        expect(fake.calls.filter((call) => call.method.startsWith("turn/") || call.method === "thread/resume")).toEqual(
          [],
        );
      } finally {
        await background.release();
      }
    } finally {
      scanSpy.mockRestore();
      restoreSpy.mockRestore();
      restore();
    }
  });

  test("background history invalidation does not reconcile an uncertain durable row", async () => {
    const storage = mutationStorage();
    const restoreSpy = vi.spyOn(storage, "restoreProvenAbsent");
    const initialScans = deferred<void>();
    const listTargetRefs = storage.listTargetRefs.bind(storage);
    let scanCount = 0;
    const scanSpy = vi.spyOn(storage, "listTargetRefs").mockImplementation(async () => {
      const refs = await listTargetRefs();
      scanCount += 1;
      if (scanCount === 3) initialScans.resolve();
      return refs;
    });
    try {
      const fake = connectCurrentClient();
      await initialScans.promise;
      await nextRealTask();
      const blocked = await blockedOutboxRow(storage, "local:one");
      let reads = 0;
      fake.on("thread/read", (params) => {
        reads += 1;
        const response = currentRead("local:one");
        return {
          ...response,
          epoch: reads === 1 ? 2 : 3,
          requestGeneration: params.requestGeneration ?? response.requestGeneration,
          thread: {
            ...response.thread,
            evener: { ...response.thread.evener, mutationStateAuthoritative: true },
          },
        };
      });
      const background = acquireBackgroundThread("local:one");
      try {
        await background.ready;
        const invalidatedHydration = waitForHydration("local:one", 2);
        fake.emitNotification({
          method: "history/updated",
          params: {
            ref: "local:one",
            threadId: "thr_local:one",
            bootGeneration: "1",
            epoch: 3,
            snapshot: { incarnation: "inc-1", length: 41 },
            items: [],
          },
        } as AnyNotification);
        expect(threadsStore.getState().threads.get("local:one")?.history?.invalidatedAtGeneration).toBeDefined();
        await invalidatedHydration;
        await nextRealTask();
        expect(restoreSpy).not.toHaveBeenCalled();
        await expect(storage.getOutbox(blocked?.clientMutationId ?? "missing")).resolves.toEqual(blocked);
        expect(fake.calls.filter((call) => call.method.startsWith("turn/") || call.method === "thread/resume")).toEqual(
          [],
        );
      } finally {
        await background.release();
      }
    } finally {
      scanSpy.mockRestore();
      restoreSpy.mockRestore();
    }
  });

  test("repeated background release shares one final drain", async () => {
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const first = background.release();
    const second = background.release();
    expect(second).toBe(first);
    await Promise.all([first, second]);
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
    expect(unsubscriptions(fake)).toBe(1);
  });

  test("a replacement client hydrates while the old client's held response is fenced", async () => {
    const oldClient = connectCurrentClient();
    const oldEntered = deferred<void>();
    const oldResponse = deferred<ReturnType<typeof currentRead>>();
    let oldReadCount = 0;
    oldClient.on("thread/read", (params) => {
      oldReadCount += 1;
      if (oldReadCount === 1) {
        oldEntered.resolve();
        return oldResponse.promise.then((response) => ({ ...response, requestGeneration: params.requestGeneration }));
      }
      return { ...currentRead("local:one"), requestGeneration: params.requestGeneration };
    });
    const background = acquireBackgroundThread("local:one");
    await oldEntered.promise;
    const replacement = new FakeClient("ready");
    replacement.on("thread/read", (params) => ({
      ...currentRead(params.ref ?? "local:one"),
      requestGeneration: params.requestGeneration,
    }));
    replacement.on("thread/unsubscribe", () => ({}));
    const hydrated = deferred<void>();
    const unsubscribe = threadsStore.subscribe((state) => {
      if (state.threads.get("local:one")?.turns.length) hydrated.resolve();
    });
    let readySettled = false;
    void background.ready.then(() => {
      readySettled = true;
    });
    try {
      connectionStore.getState().connect(replacement);
      await hydrated.promise;
      // Finish the successor's own checkpoint before capturing its identity,
      // while the predecessor response remains held at the original boundary.
      await settleEntryTranscriptForTests("local:one");
      const replacementModel = threadsStore.getState().threads.get("local:one");
      expect(replacement.calls.some((call) => call.method === "thread/read")).toBe(true);
      await nextRealTask();
      expect(readySettled).toBe(true);
      oldResponse.resolve(currentRead("local:one"));
      await nextRealTask();
      expect(threadsStore.getState().threads.get("local:one")).toBe(replacementModel);
    } finally {
      oldResponse.resolve(currentRead("local:one"));
      unsubscribe();
      await background.release();
    }
  });

  test("a deletion response settles the background attempt and records the deleted ref", async () => {
    const fake = connectCurrentClient();
    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:one", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    expect(threadsStore.getState().deletedRefs.has("local:one")).toBe(true);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    await background.release();
  });

  test("a mutation pin keeps a background-owned model until the foreground send settles", async () => {
    const storage = mutationStorage();
    const fake = connectCurrentClient();
    const entered = deferred<{ clientMutationId: string }>();
    const response = deferred<{
      turn: { id: string; status: "inProgress"; itemsView: string };
      receipt: {
        clientMutationId: string;
        disposition: "applied";
        threadId: string;
        projectionState: "reflected";
      };
    }>();
    fake.on("turn/start", (params) => {
      entered.resolve(params);
      return response.promise;
    });
    await threadsStore.getState().ensureThread("local:one");
    const sending = threadsStore.getState().send("local:one", "pinned send", [], [], [], []);
    const { clientMutationId } = await entered.promise;
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    threadsStore.getState().releaseThread("local:one");
    await background.release();
    expect(threadsStore.getState().threads.has("local:one")).toBe(true);
    response.resolve({
      turn: { id: "turn_pinned", status: "inProgress", itemsView: "" },
      receipt: {
        clientMutationId,
        disposition: "applied",
        threadId: "thr_local:one",
        projectionState: "reflected",
      },
    });
    await sending;
    expect(await storage.getOutbox(clientMutationId)).toBeDefined();
    const retired = deferred<void>();
    const unsubscribe = threadsStore.subscribe((state) => {
      if (!state.threads.has("local:one")) retired.resolve();
    });
    try {
      await storage.settleReceipt(clientMutationId, "reflected");
      await retired.promise;
    } finally {
      unsubscribe();
    }
    await expect(storage.getOutbox(clientMutationId)).resolves.toBeUndefined();
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });

  test("a new canonical claim during held final persistence keeps the model resident", async () => {
    const fake = connectCurrentClient();
    await cache.getClearEpoch();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    fake.emitNotification(textDelta("local:one", "-persist-me"));
    expect(threadsStore.getState().threads.get("local:one")?.overlay?.["stream:s1:agentMessage"]?.item.text).toBe(
      "alpha-persist-me",
    );
    // A genuine fresh final read must report the text this source already emitted.
    fake.on("thread/read", (params) => {
      const source = currentRead(params.ref ?? "local:one");
      return {
        ...source,
        requestGeneration: params.requestGeneration,
        overlay: source.overlay?.map((slot) => ({ ...slot, item: { ...slot.item, text: "alpha-persist-me" } })),
      };
    });
    await settleEntryTranscriptForTests("local:one");
    const transaction = holdNextWriteTransaction([
      "transcriptItems",
      "transcriptTurns",
      "transcriptCoverage",
      "transcriptMeta",
    ]);
    try {
      const release = background.release();
      await transaction.reached;
      expect(
        fake.calls.filter(
          (call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === false,
        ),
      ).toHaveLength(1);
      const replacement = acquireBackgroundThread("local:one");
      await replacement.ready;
      expect(unsubscriptions(fake)).toBe(0);
      transaction.release();
      await release;
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      fake.emitNotification(textDelta("local:one", "-after-held-save"));
      expect(
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join(""),
      ).toContain("persist-me-after-held-save");
      fake.on("thread/read", (params) => {
        const source = currentRead(params.ref ?? "local:one");
        return {
          ...source,
          requestGeneration: params.requestGeneration,
          overlay: source.overlay?.map((slot) => ({
            ...slot,
            item: { ...slot.item, text: "alpha-persist-me-after-held-save" },
          })),
        };
      });
      await replacement.release();
    } finally {
      transaction.release();
    }
  });

  test("a pane claim during held final persistence keeps actual wire membership", async () => {
    const fake = connectCurrentClient();
    await cache.getClearEpoch();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    await settleEntryTranscriptForTests("local:one");
    const transaction = holdNextWriteTransaction([
      "transcriptItems",
      "transcriptTurns",
      "transcriptCoverage",
      "transcriptMeta",
    ]);
    try {
      const release = background.release();
      await transaction.reached;
      expect(
        fake.calls.filter(
          (call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === false,
        ),
      ).toHaveLength(1);
      await threadsStore.getState().ensureThread("local:one");
      expect(unsubscriptions(fake)).toBe(0);
      transaction.release();
      await release;
      expect(unsubscriptions(fake)).toBe(0);
    } finally {
      transaction.release();
      await background.release();
      threadsStore.getState().releaseThread("local:one");
    }
  });
});
