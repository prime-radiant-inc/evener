import "fake-indexeddb/auto";
import {
  type AnyNotification,
  AppwireClient,
  acquireThreadSubscription,
  currentTranscriptState,
  hydrateThread,
  type ThreadModel,
  type ThreadTurnsListResponse,
  WireError,
} from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { IDBDatabase, IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { holdIndexedDBEvent, holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  installHydrationRetrySchedulerForTests,
  observePendingHydrationRetirementsForTests,
  putThreadModel,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  setSessionCacheAdapterForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";

const databaseName = `background-thread-${crypto.randomUUID()}`;
let cache: SessionCacheIndexedDB;
const activityLeases: ReturnType<typeof acquireThreadSubscription>[] = [];
const mutationStorages: MutationOutboxIndexedDB[] = [];

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), databaseName });
  setSessionCacheAdapterForTests(cache);
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
  fake.on("thread/read", ({ ref }) => currentRead(ref ?? "local:one"));
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

function threadReadRequests(socket: FakeSocket): Array<{ id: number; ref: string }> {
  return socket.sent
    .map((frame) => JSON.parse(frame) as { id?: number; method?: string; params?: { ref?: string } })
    .filter(
      (frame): frame is { id: number; method: string; params: { ref: string } } =>
        frame.method === "thread/read" && typeof frame.id === "number" && typeof frame.params?.ref === "string",
    )
    .map(({ id, params }) => ({ id, ref: params.ref }));
}

async function connectRealClientWithSocket(): Promise<{ client: AppwireClient; socket: FakeSocket }> {
  const socket = new FakeSocket({ autoInitialize: true });
  const client = new AppwireClient({ url: "ws://test/rpc", socketFactory: () => socket });
  const ready = client.connect();
  socket.open();
  await ready;
  connectionStore.getState().connect(client);
  return { client, socket };
}

function observeThreadReadSettlements(client: AppwireClient) {
  const active = new Map<number, string>();
  const starts: Array<{ ref: string; at: number }> = [];
  const settlements: Array<{ ref: string; outcome: "response" | "rejection" }> = [];
  let maximumActive = 0;
  let nextRequest = 0;
  const originalRequest = client.request.bind(client);
  const observedRequest = ((method: string, params: unknown, options?: { timeoutMs?: number }) => {
    const request = originalRequest(method as never, params as never, options) as Promise<unknown>;
    if (method !== "thread/read") return request;
    const ref = (params as { ref: string }).ref;
    const requestId = nextRequest++;
    active.set(requestId, ref);
    starts.push({ ref, at: Date.now() });
    maximumActive = Math.max(maximumActive, active.size);
    void request.then(
      () => {
        active.delete(requestId);
        settlements.push({ ref, outcome: "response" });
      },
      () => {
        active.delete(requestId);
        settlements.push({ ref, outcome: "rejection" });
      },
    );
    return request;
  }) as AppwireClient["request"];
  const spy = vi.spyOn(client, "request").mockImplementation(observedRequest);
  return {
    active,
    starts,
    settlements,
    maximumActive: () => maximumActive,
    restore: () => spy.mockRestore(),
  };
}

function independentJsonBytes(value: unknown): number {
  const encoded = JSON.stringify(value);
  if (encoded === undefined) return Number.POSITIVE_INFINITY;
  return new TextEncoder().encode(encoded).byteLength;
}

describe("background thread claims", () => {
  test("last owner release cancels a queued real-client read before it can issue", async () => {
    vi.useFakeTimers();
    const socket = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://test/rpc", socketFactory: () => socket });
    const ready = client.connect();
    socket.open();
    await ready;
    connectionStore.getState().connect(client);

    const one = acquireBackgroundThread("local:one");
    const two = acquireBackgroundThread("local:two");
    let third: ReturnType<typeof acquireBackgroundThread> | undefined;
    try {
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      const firstRequest = threadReadRequests(socket)[0];
      expect(firstRequest).toBeDefined();
      third = acquireBackgroundThread("local:three");
      await vi.waitFor(() =>
        expect(backgroundTranscriptSnapshotForTests().queuedHydrationRefs).toContain("local:three"),
      );
      await third.release();
      expect(backgroundTranscriptSnapshotForTests().queuedHydrationRefs).not.toContain("local:three");
      socket.receive({ id: firstRequest!.id, result: currentRead(firstRequest!.ref) });
      await one.ready;
      expect(threadReadRequests(socket).map(({ ref }) => ref)).toEqual(["local:one", "local:two"]);
      expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1);
    } finally {
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all([one.release(), two.release(), third?.release()]);
      client.close();
      vi.useRealTimers();
    }
  });

  test("issued reads retain both client slots through the deadline, then healthy work proceeds", async () => {
    vi.useFakeTimers();
    const { client, socket } = await connectRealClientWithSocket();
    const requests = observeThreadReadSettlements(client);
    const first = acquireBackgroundThread("local:deadline-one");
    const second = acquireBackgroundThread("local:deadline-two");
    const healthy = acquireBackgroundThread("local:deadline-healthy");
    try {
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      await vi.waitFor(() =>
        expect(backgroundTranscriptSnapshotForTests().queuedHydrationRefs).toContain("local:deadline-healthy"),
      );
      expect(requests.starts.map(({ ref }) => ref)).toEqual(["local:deadline-one", "local:deadline-two"]);
      expect(requests.active.size).toBe(2);
      expect(requests.maximumActive()).toBe(2);

      await Promise.all([first.release(), second.release()]);
      expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(2);
      expect(requests.active.size).toBe(2);
      expect(threadReadRequests(socket)).toHaveLength(2);

      await vi.advanceTimersByTimeAsync(20_000);
      const ping = socket.sent
        .map((frame) => JSON.parse(frame) as { id?: number; method?: string })
        .find((frame) => frame.method === "ping" && typeof frame.id === "number");
      expect(ping?.id).toBeDefined();
      socket.receive({ id: ping!.id!, result: {} });

      const earliestDeadline = Math.min(...requests.starts.map(({ at }) => at)) + 30_000;
      const beforeDeadline = earliestDeadline - Date.now() - 1;
      if (beforeDeadline > 0) await vi.advanceTimersByTimeAsync(beforeDeadline);
      expect(requests.active.size).toBe(2);
      expect(requests.settlements).toEqual([]);
      expect(threadReadRequests(socket)).toHaveLength(2);

      await vi.advanceTimersByTimeAsync(1);
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(3));
      expect(requests.settlements.some(({ outcome }) => outcome === "rejection")).toBe(true);
      expect(requests.active.size).toBeLessThanOrEqual(2);
      expect(requests.maximumActive()).toBeLessThanOrEqual(2);
      expect(threadReadRequests(socket).map(({ ref }) => ref)).toEqual([
        "local:deadline-one",
        "local:deadline-two",
        "local:deadline-healthy",
      ]);

      const lastOldDeadline = Math.max(...requests.starts.slice(0, 2).map(({ at }) => at)) + 30_000;
      const untilLastOldDeadline = lastOldDeadline - Date.now();
      if (untilLastOldDeadline > 0) await vi.advanceTimersByTimeAsync(untilLastOldDeadline);
      await vi.waitFor(() =>
        expect(requests.settlements.filter(({ ref }) => ref !== "local:deadline-healthy")).toHaveLength(2),
      );
      for (const abandoned of threadReadRequests(socket).slice(0, 2)) {
        socket.receive({ id: abandoned.id, result: currentRead(abandoned.ref) });
      }
      expect(requests.active.size).toBe(1);
      expect(threadsStore.getState().threads.has("local:deadline-one")).toBe(false);
      expect(threadsStore.getState().threads.has("local:deadline-two")).toBe(false);

      const healthyRead = threadReadRequests(socket)[2]!;
      socket.receive({ id: healthyRead.id, result: currentRead(healthyRead.ref) });
      await healthy.ready;
      expect(requests.settlements).toContainEqual({ ref: "local:deadline-healthy", outcome: "response" });
      expect(requests.active.size).toBe(0);
      expect(requests.maximumActive()).toBeLessThanOrEqual(2);
    } finally {
      requests.restore();
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all([first.release(), second.release(), healthy.release()]);
      client.close();
      vi.useRealTimers();
    }
  });

  test("actual client read occupancy stays within two through repeated owner churn", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const requests = observeThreadReadSettlements(client);
    const leases: Array<ReturnType<typeof acquireBackgroundThread>> = [];
    let current = ["local:churn-0-one", "local:churn-0-two"].map((ref) => acquireBackgroundThread(ref));
    leases.push(...current);
    try {
      for (let round = 0; round < 3; round += 1) {
        const expectedStarts = (round + 1) * 2;
        await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(expectedStarts));
        expect(requests.active.size).toBe(2);
        expect(requests.maximumActive()).toBeLessThanOrEqual(2);
        expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBeLessThanOrEqual(2);

        if (round === 2) {
          for (const request of threadReadRequests(socket).slice(round * 2, expectedStarts)) {
            socket.receive({ id: request.id, result: currentRead(request.ref) });
          }
          await Promise.all(current.map((lease) => lease.ready));
          expect(requests.active.size).toBe(0);
          break;
        }

        await Promise.all(current.map((lease) => lease.release()));
        const nextRefs = [`local:churn-${round + 1}-one`, `local:churn-${round + 1}-two`];
        current = nextRefs.map((ref) => acquireBackgroundThread(ref));
        leases.push(...current);
        await vi.waitFor(() => {
          const queued = backgroundTranscriptSnapshotForTests().queuedHydrationRefs;
          expect(nextRefs.every((ref) => queued.includes(ref))).toBe(true);
        });
        expect(threadReadRequests(socket)).toHaveLength(expectedStarts);
        expect(requests.active.size).toBe(2);

        const retiredRequests = threadReadRequests(socket).slice(round * 2, expectedStarts);
        socket.receive({ id: retiredRequests[0]!.id, result: currentRead(retiredRequests[0]!.ref) });
        await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(expectedStarts + 1));
        expect(requests.active.size).toBe(2);
        socket.receive({ id: retiredRequests[1]!.id, result: currentRead(retiredRequests[1]!.ref) });
        await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(expectedStarts + 2));
        expect(requests.active.size).toBe(2);
        expect(requests.maximumActive()).toBeLessThanOrEqual(2);
        expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBeLessThanOrEqual(2);
        for (const { ref } of requests.starts.slice(round * 2, expectedStarts)) {
          expect(threadsStore.getState().threads.has(ref)).toBe(false);
        }
      }
      expect(requests.starts.map(({ ref }) => ref)).toEqual([
        "local:churn-0-one",
        "local:churn-0-two",
        "local:churn-1-one",
        "local:churn-1-two",
        "local:churn-2-one",
        "local:churn-2-two",
      ]);
    } finally {
      requests.restore();
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all(leases.map((lease) => lease.release()));
      client.close();
    }
  });

  test("reacquisition gets a fresh real-client read while the retired request still owns its slot", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const retired = acquireBackgroundThread("local:reacquire");
    let replacement: ReturnType<typeof acquireBackgroundThread> | undefined;
    try {
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      const first = threadReadRequests(socket)[0]!;
      await retired.release();
      replacement = acquireBackgroundThread("local:reacquire");

      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(2));
      expect(threadReadRequests(socket)).toHaveLength(1);
      const retiredRead = currentRead(first.ref);
      retiredRead.overlay![0]!.item.text = "retired snapshot";
      socket.receive({ id: first.id, result: retiredRead });
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1));
      const second = threadReadRequests(socket)[1]!;
      expect(second.ref).toBe(first.ref);
      expect(threadsStore.getState().threads.has(first.ref)).toBe(false);
      const replacementRead = currentRead(second.ref);
      replacementRead.overlay![0]!.item.text = "replacement snapshot";
      socket.receive({ id: second.id, result: replacementRead });
      await replacement.ready;
      expect(
        threadsStore
          .getState()
          .threads.get(second.ref)
          ?.turns.flatMap((turn) => turn.items)
          .some((item) => item.text === "replacement snapshot"),
      ).toBe(true);
    } finally {
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all([retired.release(), replacement?.release()]);
      client.close();
    }
  });

  test("same-ref background claimants share one real read until the last owner releases", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const requests = observeThreadReadSettlements(client);
    const first = acquireBackgroundThread("local:shared-read");
    const second = acquireBackgroundThread("local:shared-read");
    try {
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      expect(requests.starts.map(({ ref }) => ref)).toEqual(["local:shared-read"]);
      expect(requests.active.size).toBe(1);
      await first.release();
      expect(requests.active.size).toBe(1);
      expect(threadReadRequests(socket)).toHaveLength(1);

      const request = threadReadRequests(socket)[0]!;
      socket.receive({ id: request.id, result: currentRead(request.ref) });
      await second.ready;
      expect(threadsStore.getState().threads.has("local:shared-read")).toBe(true);
      expect(requests.settlements).toEqual([{ ref: "local:shared-read", outcome: "response" }]);
      expect(threadReadRequests(socket)).toHaveLength(1);
    } finally {
      requests.restore();
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all([first.release(), second.release()]);
      client.close();
    }
  });

  test("a paced retry cannot starve ready real reads or exceed the request cap", async () => {
    const retryCallbacks: Array<() => void> = [];
    const restoreRetryScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      retryCallbacks.push(retry);
      return () => {};
    });
    const { client, socket } = await connectRealClientWithSocket();
    const requests = observeThreadReadSettlements(client);
    const leases: Array<ReturnType<typeof acquireBackgroundThread>> = [];
    try {
      const retrying = acquireBackgroundThread("local:retry-fairness");
      leases.push(retrying);
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      const initial = threadReadRequests(socket)[0]!;
      socket.receive({ id: initial.id, error: { code: -32000, message: "temporary read failure" } });
      await retrying.ready;
      await vi.waitFor(() => expect(retryCallbacks).toHaveLength(1));

      const occupying = acquireBackgroundThread("local:retry-occupying");
      leases.push(occupying);
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      retryCallbacks[0]!();
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(3));
      expect(requests.starts.map(({ ref }) => ref)).toEqual([
        "local:retry-fairness",
        "local:retry-occupying",
        "local:retry-fairness",
      ]);

      const healthy = acquireBackgroundThread("local:retry-healthy");
      leases.push(healthy);
      await vi.waitFor(() =>
        expect(backgroundTranscriptSnapshotForTests().queuedHydrationRefs).toContain("local:retry-healthy"),
      );
      const firstRetry = threadReadRequests(socket)[2]!;
      socket.receive({ id: firstRetry.id, error: { code: -32000, message: "retry still failing" } });
      await vi.waitFor(() => expect(retryCallbacks).toHaveLength(2));
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(4));
      expect(threadReadRequests(socket)[3]?.ref).toBe("local:retry-healthy");
      expect(requests.active.size).toBe(2);
      expect(requests.maximumActive()).toBeLessThanOrEqual(2);

      retryCallbacks[1]!();
      await vi.waitFor(() =>
        expect(backgroundTranscriptSnapshotForTests().queuedHydrationRefs).toContain("local:retry-fairness"),
      );
      const nextHealthy = acquireBackgroundThread("local:retry-next-healthy");
      leases.push(nextHealthy);
      await vi.waitFor(() =>
        expect(backgroundTranscriptSnapshotForTests().queuedHydrationRefs).toContain("local:retry-next-healthy"),
      );

      const occupyingRead = threadReadRequests(socket)[1]!;
      socket.receive({ id: occupyingRead.id, result: currentRead(occupyingRead.ref) });
      await occupying.ready;
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(5));
      expect(threadReadRequests(socket)[4]?.ref).toBe("local:retry-fairness");
      const secondRetry = threadReadRequests(socket)[4]!;
      socket.receive({ id: secondRetry.id, error: { code: -32000, message: "retry failed again" } });
      await vi.waitFor(() => expect(retryCallbacks).toHaveLength(3));
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(6));
      expect(threadReadRequests(socket)[5]?.ref).toBe("local:retry-next-healthy");
      expect(requests.starts.map(({ ref }) => ref)).toEqual([
        "local:retry-fairness",
        "local:retry-occupying",
        "local:retry-fairness",
        "local:retry-healthy",
        "local:retry-fairness",
        "local:retry-next-healthy",
      ]);
      expect(requests.active.size).toBe(2);
      expect(requests.maximumActive()).toBeLessThanOrEqual(2);
      expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(2);

      for (const request of [threadReadRequests(socket)[3]!, threadReadRequests(socket)[5]!]) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all([healthy.ready, nextHealthy.ready]);
      expect(requests.settlements).toContainEqual({ ref: "local:retry-healthy", outcome: "response" });
      expect(requests.settlements).toContainEqual({ ref: "local:retry-next-healthy", outcome: "response" });
    } finally {
      requests.restore();
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all(leases.map((lease) => lease.release()));
      client.close();
      restoreRetryScheduler();
    }
  });

  test("last-owner retirement releases the pending charge and detaches frames and base model", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:retired-buffer";
    const background = acquireBackgroundThread(ref);
    const retirements: Array<{
      ref: string;
      retainedNotificationCount: number;
      hadBaseModel: boolean;
      notificationCount: number;
      hasBaseModel: boolean;
    }> = [];
    let restoreObserver: (() => void) | undefined;
    try {
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      const initial = threadReadRequests(socket)[0]!;
      socket.receive({ id: initial.id, result: currentRead(ref) });
      await background.ready;

      restoreObserver = observePendingHydrationRetirementsForTests((retirement) => retirements.push(retirement));
      socket.receive({
        method: "evener/thread/resync",
        params: { ref, threadId: `thr_${ref}` },
      });
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      const retired = threadReadRequests(socket)[1]!;
      const update = textDelta(ref, "buffered while snapshot waits");
      socket.receive({ method: update.method, params: update.params });
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThan(0));

      await background.release();
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(0);
      expect(retirements).toEqual([
        { ref, retainedNotificationCount: 1, hadBaseModel: true, notificationCount: 0, hasBaseModel: false },
      ]);

      socket.receive({ id: retired.id, result: currentRead(ref) });
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
    } finally {
      restoreObserver?.();
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await background.release();
      client.close();
    }
  });

  test("last pane release detaches its pending frames and base model", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:retired-pane-buffer";
    const retirements: Array<{
      ref: string;
      retainedNotificationCount: number;
      hadBaseModel: boolean;
      notificationCount: number;
      hasBaseModel: boolean;
    }> = [];
    let restoreObserver: (() => void) | undefined;
    try {
      const initial = threadsStore.getState().ensureThread(ref);
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      const first = threadReadRequests(socket)[0]!;
      socket.receive({ id: first.id, result: currentRead(ref) });
      await initial;

      restoreObserver = observePendingHydrationRetirementsForTests((retirement) => retirements.push(retirement));
      socket.receive({
        method: "evener/thread/resync",
        params: { ref, threadId: `thr_${ref}` },
      });
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      const retired = threadReadRequests(socket)[1]!;
      const update = textDelta(ref, "buffered while pane snapshot waits");
      socket.receive({ method: update.method, params: update.params });

      threadsStore.getState().releaseThread(ref);
      expect(retirements).toEqual([
        { ref, retainedNotificationCount: 1, hadBaseModel: true, notificationCount: 0, hasBaseModel: false },
      ]);
      socket.receive({ id: retired.id, result: currentRead(ref) });
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
    } finally {
      restoreObserver?.();
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      threadsStore.getState().releaseThread(ref);
      client.close();
    }
  });

  test("retirement prevents a stale-cursor fallback read after its first response rejects", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:retired-fallback";
    const lease = acquireBackgroundThread(ref);
    try {
      putThreadModel(ref, hydrateThread(currentRead(ref), ref, Date.now()));
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      const first = threadReadRequests(socket)[0]!;
      const release = lease.release();
      socket.receive({
        id: first.id,
        error: {
          code: -32000,
          message: "stale cursor",
          data: { evenerErrorInfo: "transcriptItemCursorStale" },
        },
      });
      await release;
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0), {
        timeout: 1_000,
      });
      expect(threadReadRequests(socket)).toHaveLength(1);
    } finally {
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await lease.release();
      client.close();
    }
  });

  test.each(["resync control", "overflow recovery"])(
    "%s does not issue fallback during a held final write",
    async (path) => {
      const restoreOptions = installBackgroundTranscriptOptionsForTests({ pendingBytes: 1 });
      const { client, socket } = await connectRealClientWithSocket();
      const ref = "local:recovery-retirement";
      const lease = acquireBackgroundThread(ref);
      let hold: ReturnType<typeof holdNextWriteTransaction> | undefined;
      let release: Promise<void> | undefined;
      try {
        await cache.readEpoch();
        await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
        socket.receive({ id: threadReadRequests(socket)[0]!.id, result: currentRead(ref) });
        await lease.ready;
        await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));

        if (path === "resync control") {
          socket.receive({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
        } else {
          const update = textDelta(ref, "overflow forces canonical residency recovery");
          socket.receive({ method: update.method, params: update.params });
        }
        await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
        expect(threadReadRequests(socket)[1]?.ref).toBe(ref);
        hold = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        release = lease.release();
        await hold.reached;
        const recovery = threadReadRequests(socket)[1]!;
        socket.receive({
          id: recovery.id,
          error: {
            code: -32000,
            message: "stale cursor",
            data: { evenerErrorInfo: "transcriptItemCursorStale" },
          },
        });
        await vi.waitFor(() =>
          expect(
            backgroundTranscriptSnapshotForTests().networkHydrations === 0 || threadReadRequests(socket).length > 2,
          ).toBe(true),
        );
        expect(threadReadRequests(socket)).toHaveLength(2);
      } finally {
        hold?.release();
        client.close();
        await (release ?? lease.release());
        restoreOptions();
      }
    },
  );

  test("retired overflow recovery does not dispatch after the ready continuation on reacquisition", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ pendingBytes: 1 });
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:ready-continuation-retirement";
    const firstOwner = acquireBackgroundThread(ref);
    let replacement: ReturnType<typeof acquireBackgroundThread> | undefined;
    let heldWrite: ReturnType<typeof holdNextWriteTransaction> | undefined;
    let firstRelease: Promise<void> | undefined;
    let replacementRelease: Promise<void> | undefined;
    try {
      await cache.readEpoch();
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      socket.receive({ id: threadReadRequests(socket)[0]!.id, result: currentRead(ref) });
      await firstOwner.ready;
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));

      const update = textDelta(ref, "overflow crosses ready continuation");
      socket.receive({ method: update.method, params: update.params });
      heldWrite = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
      // The callback has called requireReadyClient and suspended at its await.
      // Releasing and reacquiring in this stack retires only the old signal;
      // its continuation must not claim the replacement owner's authority.
      firstRelease = firstOwner.release();
      replacement = acquireBackgroundThread(ref);
      await heldWrite.reached;
      await vi.waitFor(() =>
        expect(
          backgroundTranscriptSnapshotForTests().networkHydrations === 0 || threadReadRequests(socket).length > 1,
        ).toBe(true),
      );
      expect(threadReadRequests(socket)).toHaveLength(1);
      replacementRelease = replacement.release();
    } finally {
      heldWrite?.release();
      client.close();
      await Promise.all([firstRelease ?? firstOwner.release(), replacementRelease ?? replacement?.release()]);
      restoreOptions();
    }
  });

  test("a released read waiting on shared membership cannot issue after that transition", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:membership-retire";
    const activity = acquireThreadSubscription(client, ref);
    activityLeases.push(activity);
    const membershipRead = activity.read({ includeTurns: false, itemsView: "full", itemLimit: 80 });
    const background = acquireBackgroundThread(ref);
    try {
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1));
      await background.release();
      const first = threadReadRequests(socket)[0]!;
      socket.receive({ id: first.id, result: currentRead(ref) });
      await membershipRead;
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
      expect(threadReadRequests(socket)).toHaveLength(1);
    } finally {
      socket.closeFromServer(1000);
      await background.release();
      client.close();
    }
  });

  test("promotes a zero-resident current checkpoint without another thread/read", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 4096 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    let releasePane: (() => void) | undefined;
    try {
      await background.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0);
      fake.emitNotification(textDelta("local:one", "-disk"));
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThan(0);
      const reads = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread("local:one");
      releasePane = () => threadsStore.getState().releaseThread("local:one");
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
      expect(
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join(""),
      ).toContain("alpha-disk");
    } finally {
      releasePane?.();
      try {
        await background.release();
      } finally {
        restoreOptions();
      }
    }
  });

  test("promotes once across a held current lookup and applies notifications on both sides", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 4096 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const storage = mutationStorage();
    const targetScans = deferred<void>();
    const originalListTargetRefs = storage.listTargetRefs.bind(storage);
    let targetScanCount = 0;
    vi.spyOn(storage, "listTargetRefs").mockImplementation(async () => {
      const refs = await originalListTargetRefs();
      targetScanCount += 1;
      if (targetScanCount >= 3) targetScans.resolve();
      return refs;
    });
    const fake = connectCurrentClient();
    fake.on("thread/read", ({ ref }) => {
      const response = currentRead(ref ?? "local:one");
      response.thread.evener = { ...response.thread.evener, instanceId: "instance-canonical" };
      return response;
    });
    const background = acquireBackgroundThread("local:one");
    const completionSource = acquireBackgroundThread("local:completion-source");
    let paneHeld = false;
    let heldLookup: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const promotionLookupReached = deferred<void>();
    const releasePromotionLookup = deferred<void>();
    const lookupTransactionReached = deferred<void>();
    const releaseLookupTransaction = deferred<void>();
    const lookupWorkCommitted = deferred<void>();
    let armPromotionLookup = false;
    let getCurrentSpy: ReturnType<typeof vi.spyOn> | undefined;
    let promotedAtPublication: ThreadModel | undefined;
    let promotion: Promise<void> | undefined;
    let firstWriteReached: ReturnType<typeof deferred<void>> | undefined;
    let releaseFirstWrite: ReturnType<typeof deferred<void>> | undefined;
    let laterWriteReached: ReturnType<typeof deferred<void>> | undefined;
    let releaseLaterWrite: ReturnType<typeof deferred<void>> | undefined;
    let putCurrentSpy: ReturnType<typeof vi.spyOn> | undefined;
    const observePromotion = threadsStore.subscribe((state) => {
      const model = state.threads.get("local:one");
      if (model?.turns.flatMap((turn) => turn.items).some((item) => item.text?.includes("-during-lookup"))) {
        promotedAtPublication = model;
      }
    });
    try {
      await targetScans.promise;
      await background.ready;
      await completionSource.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);

      const pending = await blockedOutboxRow(storage, "local:one");
      expect(pending?.state).toBe("blockedUnknown");
      expect(threadsStore.getState().mutationAuthorityRefs.has("local:one")).toBe(false);
      const outboxBeforePromotion = await storage.listOutbox("local:one");
      expect(outboxBeforePromotion).toEqual([pending]);

      const readCount = fake.calls.filter((call) => call.method === "thread/read").length;
      const originalPutCurrent = cache.putCurrent.bind(cache);
      firstWriteReached = deferred<void>();
      releaseFirstWrite = deferred<void>();
      laterWriteReached = deferred<void>();
      releaseLaterWrite = deferred<void>();
      let completionUpdates = 0;
      putCurrentSpy = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
        if (record.ref === "local:one" && record.appliedSequence === 1) {
          firstWriteReached?.resolve();
          await releaseFirstWrite?.promise;
          const result = await originalPutCurrent(record, now);
          for (const sequence of [2, 3, 4, 5]) {
            if (sequence === 2) {
              fake.emitNotification({
                method: "overlay/upserted",
                params: {
                  threadId: "thr_local:one",
                  ref: "local:one",
                  item: {
                    key: "stream:round_tool/0:toolOutput",
                    kind: "stream",
                    turnId: "turn_1",
                    roundId: "round_tool",
                    streamId: "round_tool/0",
                    callId: "call_tool",
                    item: {
                      type: "commandExecution",
                      id: "item_tool",
                      turnId: "turn_1",
                      roundId: "round_tool",
                      callId: "call_tool",
                      status: "inProgress",
                    },
                  },
                },
              } as AnyNotification);
            } else if (sequence === 3) {
              fake.emitNotification({
                method: "overlay/delta",
                params: {
                  threadId: "thr_local:one",
                  ref: "local:one",
                  key: "stream:round_tool/0:toolOutput",
                  field: "output",
                  delta: "工具🙂",
                },
              } as AnyNotification);
            } else {
              fake.emitNotification(textDelta("local:one", `-completion-${sequence}`));
            }
            completionUpdates += 1;
          }
          return result;
        }
        if (record.ref === "local:one" && record.appliedSequence >= 2) {
          laterWriteReached?.resolve();
          await releaseLaterWrite?.promise;
        }
        const result = await originalPutCurrent(record, now);
        if (record.ref === "local:completion-source" && record.appliedSequence === 1) {
          fake.emitNotification(textDelta("local:one", "-completion-in-lookup"));
          lookupWorkCommitted.resolve();
        }
        return result;
      });
      const originalGetCurrent = cache.getCurrent.bind(cache);
      getCurrentSpy = vi.spyOn(cache, "getCurrent").mockImplementation(async (...args) => {
        if (armPromotionLookup) {
          armPromotionLookup = false;
          promotionLookupReached.resolve();
          await releasePromotionLookup.promise;
          heldLookup = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
          const lookup = originalGetCurrent(...args);
          await heldLookup.reached;
          lookupTransactionReached.resolve();
          await releaseLookupTransaction.promise;
          return lookup;
        }
        return originalGetCurrent(...args);
      });
      fake.emitNotification(textDelta("local:one", "-captured"));
      await firstWriteReached.promise;
      releaseFirstWrite.resolve();
      await laterWriteReached.promise;
      releaseLaterWrite.resolve();
      await settleBackgroundTranscriptsForTests();

      armPromotionLookup = true;
      promotion = threadsStore.getState().ensureThread("local:one");
      await promotionLookupReached.promise;
      fake.emitNotification(textDelta("local:completion-source", "-source-write"));
      await lookupWorkCommitted.promise;
      await settleBackgroundTranscriptsForTests();
      releasePromotionLookup.resolve();
      await lookupTransactionReached.promise;
      const toolKey = "stream:round_tool/0:toolOutput";
      fake.emitNotification({
        method: "overlay/delta",
        params: { threadId: "thr_local:one", ref: "local:one", key: toolKey, field: "output", delta: ":during-lookup" },
      } as AnyNotification);
      fake.emitNotification(textDelta("local:one", "-during-lookup"));
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThan(0);
      if (!heldLookup) throw new Error("promotion lookup transaction was not held");
      heldLookup.release();
      releaseLookupTransaction.resolve();
      await promotion;
      paneHeld = true;

      const adopted = threadsStore.getState().threads.get("local:one");
      expect(adopted).toBeDefined();
      expect(adopted).toBe(promotedAtPublication);
      expect(adopted?.capabilities.send).toBe(true);
      expect(adopted?.capabilities.queue).toBe(true);
      expect(adopted?.capabilitySource).toBe("read");
      expect(adopted?.instanceId).toBe("instance-canonical");
      expect(threadsStore.getState().mutationAuthorityRefs.has("local:one")).toBe(false);
      fake.emitNotification({
        method: "overlay/delta",
        params: {
          threadId: "thr_local:one",
          ref: "local:one",
          key: toolKey,
          field: "output",
          delta: ":after-publication",
        },
      } as AnyNotification);
      fake.emitNotification(textDelta("local:one", "-after-publication"));
      const text =
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join("") ?? "";
      expect(text).toContain(
        "alpha-captured-completion-4-completion-5-completion-in-lookup-during-lookup-after-publication",
      );
      let previousIndex = text.indexOf("-captured");
      for (const marker of [
        "-completion-4",
        "-completion-5",
        "-completion-in-lookup",
        "-during-lookup",
        "-after-publication",
      ]) {
        expect(text.split(marker), marker).toHaveLength(2);
        const index = text.indexOf(marker);
        expect(index).toBeGreaterThan(previousIndex);
        previousIndex = index;
      }
      expect(completionUpdates).toBe(4);
      const toolItems = adopted?.turns.flatMap((turn) => turn.items).filter((item) => item.id === "item_tool") ?? [];
      expect(toolItems).toHaveLength(1);
      expect(toolItems[0]?.output).toBe("工具🙂:during-lookup");
      const afterNotifications = threadsStore.getState().threads.get("local:one");
      expect(
        afterNotifications?.turns.flatMap((turn) => turn.items).find((item) => item.id === "item_tool")?.output,
      ).toBe("工具🙂:during-lookup:after-publication");
      expect(afterNotifications?.overlay?.[toolKey]?.item.output).toBe("工具🙂:during-lookup:after-publication");
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readCount);
      expect(await storage.listOutbox("local:one")).toEqual(outboxBeforePromotion);
      expect(
        fake.calls.filter((call) =>
          ["thread/resume", "turn/start", "turn/steer", "turn/queue", "turn/interrupt"].includes(call.method),
        ),
      ).toEqual([]);
    } finally {
      heldLookup?.release();
      releaseLookupTransaction.resolve();
      releasePromotionLookup.resolve();
      releaseFirstWrite?.resolve();
      releaseLaterWrite?.resolve();
      getCurrentSpy?.mockRestore();
      putCurrentSpy?.mockRestore();
      await promotion?.catch(() => undefined);
      observePromotion();
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      await completionSource.release();
      await background.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("accounts two completed hydrations and deltas while the real checkpoint transaction is held", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 1024 * 1024,
      pendingBytes: 4 * 1024 * 1024,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const one = deferred<ReturnType<typeof currentRead>>();
    const two = deferred<ReturnType<typeof currentRead>>();
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => (ref === "local:one" ? one.promise : two.promise));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:two");
    const acceptedFrames: AnyNotification[] = [];
    const writePayloads: Array<{ state: unknown; bytes: number; recordBytes: number }> = [];
    const putCurrentStarted = deferred<void>();
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const publicationAccounting: Array<{ retainedBytes: number; chargedBytes: number }> = [];
    const observeAccounting = threadsStore.subscribe((state) => {
      const retainedBytes = [...state.threads.values()].reduce(
        (sum, model) => sum + independentJsonBytes(currentTranscriptState(model)),
        0,
      );
      const snapshot = backgroundTranscriptSnapshotForTests();
      publicationAccounting.push({
        retainedBytes,
        chargedBytes: snapshot.residentBytes + snapshot.pendingBytes + snapshot.workerBytes,
      });
    });
    const originalPutCurrent = cache.putCurrent.bind(cache);
    const putCurrent = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (!transaction) {
        writePayloads.push({
          state: record.state,
          bytes: independentJsonBytes(record),
          recordBytes: independentJsonBytes(record),
        });
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        putCurrentStarted.resolve();
      }
      return originalPutCurrent(record, now);
    });
    const payload = (ref: string) => {
      const response = currentRead(ref);
      response.overlay![0]!.item.text = "x".repeat(256 * 1024);
      return response;
    };
    try {
      await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2));
      const firstResponse = payload("local:one");
      one.resolve(firstResponse);
      await vi.waitFor(() => expect(threadsStore.getState().hydrations.get("local:one")).toBeGreaterThan(0), {
        timeout: 1500,
      });
      const secondResponse = payload("local:two");
      two.resolve(secondResponse);
      await vi.waitFor(() => expect(threadsStore.getState().hydrations.get("local:two")).toBeGreaterThan(0), {
        timeout: 1500,
      });
      expect(threadsStore.getState().threads.has("local:two")).toBe(true);

      const firstFrame = textDelta("local:one", "🙂".repeat(1024));
      acceptedFrames.push(firstFrame);
      fake.emitNotification(firstFrame);
      await vi.waitFor(() => expect(writePayloads.length).toBeGreaterThan(0), { timeout: 1500 });
      await putCurrentStarted.promise;
      await transaction?.reached;

      // The first write payload is retained inside the real cache put call while
      // its four-store IndexedDB transaction is held. The second decoded model
      // remains in the real store. Measure those concrete objects independently.
      const firstWrite = writePayloads[0];
      expect(firstWrite).toBeDefined();
      const heldSnapshot = backgroundTranscriptSnapshotForTests();
      const firstBodyBytes = independentJsonBytes(firstWrite?.state);
      const secondModel = threadsStore.getState().threads.get("local:two");
      expect(secondModel).toBeDefined();
      const secondStateBytes = independentJsonBytes(currentTranscriptState(secondModel!));
      expect(firstBodyBytes).toBeGreaterThan(1024);
      expect(secondStateBytes).toBeGreaterThan(1024);
      expect(heldSnapshot.workerBytes).toBeGreaterThanOrEqual(firstBodyBytes);
      expect(firstWrite?.recordBytes).toBeGreaterThan(firstBodyBytes);
      expect(heldSnapshot.residentBytes + heldSnapshot.pendingBytes + heldSnapshot.workerBytes).toBeGreaterThanOrEqual(
        firstBodyBytes + secondStateBytes,
      );

      for (let i = 0; i < 15; i += 1) {
        const frame = textDelta("local:one", "🙂".repeat(1024));
        acceptedFrames.push(frame);
        fake.emitNotification(frame);
        const snapshot = backgroundTranscriptSnapshotForTests();
        expect(snapshot.residentBytes).toBeLessThanOrEqual(1024 * 1024);
        expect(snapshot.pendingBytes).toBeLessThanOrEqual(4 * 1024 * 1024);
        expect(snapshot.diskWorkers).toBeLessThanOrEqual(1);
        expect(snapshot.networkHydrations).toBeLessThanOrEqual(2);
        expect(snapshot.workerBytes).toBeLessThanOrEqual(32 * 1024 * 1024);
      }
      const toolKey = "stream:accounting/0:toolOutput";
      const toolUpsert = {
        method: "overlay/upserted",
        params: {
          threadId: "thr_local:one",
          ref: "local:one",
          item: {
            key: toolKey,
            kind: "stream",
            turnId: "turn_1",
            roundId: "accounting",
            streamId: "accounting/0",
            callId: "call_accounting",
            item: {
              type: "commandExecution",
              id: "item_accounting_tool",
              turnId: "turn_1",
              roundId: "accounting",
              callId: "call_accounting",
              status: "inProgress",
            },
          },
        },
      } as AnyNotification;
      const toolOutput = {
        method: "overlay/delta",
        params: {
          threadId: "thr_local:one",
          ref: "local:one",
          key: toolKey,
          field: "output",
          delta: JSON.stringify({ structured: { output: ["工具", 7, { retained: true }] } }),
        },
      } as AnyNotification;
      acceptedFrames.push(toolUpsert, toolOutput);
      fake.emitNotification(toolUpsert);
      fake.emitNotification(toolOutput);
      expect(threadsStore.getState().threads.get("local:one")?.overlay?.[toolKey]?.item.output).toContain(
        '"retained":true',
      );
      expect(publicationAccounting.length).toBeGreaterThan(0);
      expect(publicationAccounting.every(({ retainedBytes, chargedBytes }) => chargedBytes >= retainedBytes)).toBe(
        true,
      );
      expect(acceptedFrames.every((frame) => independentJsonBytes(frame) > 0)).toBe(true);
      const retainedBytes = [...threadsStore.getState().threads.values()].reduce(
        (sum, model) => sum + independentJsonBytes(currentTranscriptState(model)),
        0,
      );
      const acceptedFrameBytes = acceptedFrames.reduce((sum, frame) => sum + independentJsonBytes(frame), 0);
      const chargedBytes = backgroundTranscriptSnapshotForTests();
      expect(chargedBytes.residentBytes + chargedBytes.pendingBytes + chargedBytes.workerBytes).toBeGreaterThanOrEqual(
        retainedBytes + acceptedFrameBytes,
      );
      expect(encodedPayloadBytes({ text: "🙂", raw: { output: ["工具", 7] } })).toBe(
        new TextEncoder().encode(JSON.stringify({ text: "🙂", raw: { output: ["工具", 7] } })).byteLength,
      );
      expect(backgroundTranscriptSnapshotForTests().workerBytes).toBeGreaterThan(0);
      transaction?.release();
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0), { timeout: 1000 });
      await settleBackgroundTranscriptsForTests();
      const completed = backgroundTranscriptSnapshotForTests();
      expect(completed.diskWorkers).toBe(0);
      expect(completed.refs.get("local:one")?.current, JSON.stringify(completed.refs.get("local:one"))).toBe(true);
      expect(completed.refs.get("local:two")?.current).toBe(true);
      const readsBeforePromotion = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread("local:one");
      const promoted = threadsStore.getState().threads.get("local:one");
      const text =
        promoted?.turns
          .flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join("") ?? "";
      expect(text.match(/🙂/gu)).toHaveLength(1024 * 16);
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforePromotion);
    } finally {
      transaction?.release();
      putCurrent.mockRestore();
      observeAccounting();
      threadsStore.getState().releaseThread("local:one");
      await Promise.all([first.release(), second.release()]);
      await settleBackgroundTranscriptsForTests();
      restoreOptions();
    }
  });

  test("sizes each resident text delta without serializing the whole retained transcript", async () => {
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => {
      const response = currentRead(ref ?? "local:one");
      response.overlay![0]!.item.text = "x".repeat(256 * 1024);
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const background = acquireBackgroundThread("local:one");
    const originalStringify = JSON.stringify.bind(JSON);
    const stringify = vi.spyOn(JSON, "stringify");
    try {
      await background.ready;
      await settleBackgroundTranscriptsForTests();
      const model = threadsStore.getState().threads.get("local:one");
      expect(model).toBeDefined();
      const retainedStateBytes = new TextEncoder().encode(originalStringify(currentTranscriptState(model!))).byteLength;
      expect(retainedStateBytes).toBeGreaterThan(128 * 1024);
      stringify.mockClear();

      for (let index = 0; index < 8; index += 1) {
        fake.emitNotification(textDelta("local:one", `🙂${index}`));
      }

      const serializedArguments = stringify.mock.calls.map(([value]) => value);
      const serializedSizes = serializedArguments.map((value) => {
        const encoded = originalStringify(value);
        return encoded === undefined ? Number.POSITIVE_INFINITY : new TextEncoder().encode(encoded).byteLength;
      });
      expect(serializedSizes).toHaveLength(8);
      expect(Math.max(...serializedSizes)).toBeLessThan(retainedStateBytes / 2);
      expect(threadsStore.getState().threads.get("local:one")).not.toBe(model);
      expect(
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join(""),
      ).toContain("🙂0🙂1🙂2🙂3🙂4🙂5🙂6🙂7");
    } finally {
      stringify.mockRestore();
      await background.release();
    }
  });

  test("preserves a pane-held history window and cursor while another background ref is under pressure", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 4096 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectCurrentClient();
    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:two");
    let paneHeld = false;
    try {
      await first.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      await threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      const held = threadsStore.getState().threads.get("local:one");
      expect(held).toBeDefined();
      const cursor = held?.olderCursor;
      const heldHistory = held?.history;

      await second.ready;
      await settleBackgroundTranscriptsForTests();
      expect(backgroundTranscriptSnapshotForTests().residentBytes).toBe(0);
      expect(threadsStore.getState().threads.get("local:one")).toBe(held);
      expect(threadsStore.getState().threads.get("local:one")?.olderCursor).toBe(cursor);
      expect(threadsStore.getState().threads.get("local:one")?.history).toBe(heldHistory);
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      await Promise.all([first.release(), second.release()]);
      restoreOptions();
    }
  });

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
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thr_local:one",
          projectionState: "reflected",
        },
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
    expect(
      resident?.turns
        .flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha");
    const subscribedReads = membershipReads(fake);
    expect(subscribedReads).toBe(1);

    await Promise.all([
      threadsStore.getState().ensureThread("local:one"),
      threadsStore.getState().ensureThread("local:one"),
    ]);
    expect(threadsStore.getState().threads.get("local:one")).toBe(resident);
    expect(membershipReads(fake)).toBe(subscribedReads);

    await first.release();
    await second.release();
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
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) {
        entered.resolve();
        return held.promise;
      }
      return currentRead("local:one");
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
      fake.on("thread/read", () => {
        attempts += 1;
        if (attempts === 1) throw new Error("temporary read failure");
        return currentRead("local:one");
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
      fake.on("thread/read", () => {
        reads += 1;
        if (reads === 1) throw new Error("temporary read failure");
        const response = currentRead("local:one");
        return {
          ...response,
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
    oldClient.on("thread/read", () => {
      oldReadCount += 1;
      if (oldReadCount === 1) {
        oldEntered.resolve();
        return oldResponse.promise;
      }
      return currentRead("local:one");
    });
    const background = acquireBackgroundThread("local:one");
    await oldEntered.promise;
    const replacement = new FakeClient("ready");
    replacement.on("thread/read", ({ ref }) => currentRead(ref ?? "local:one"));
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

  test("a threadId-only notification routes to the requested background alias", async () => {
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const notification = {
      method: "overlay/delta",
      params: {
        threadId: "thr_local:one",
        key: "stream:s1:agentMessage",
        field: "text",
        delta: "-via-id",
      },
    } as AnyNotification;
    fake.emitNotification(notification);
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-via-id");
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
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    fake.emitNotification(textDelta("local:one", "-persist-me"));
    const transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
    try {
      const release = background.release();
      await transaction.reached;
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
      await replacement.release();
    } finally {
      transaction.release();
    }
  });

  test("a pane claim during held final persistence keeps actual wire membership", async () => {
    const fake = connectCurrentClient();
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
    try {
      const release = background.release();
      await transaction.reached;
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

  test.each(["background", "pane"] as const)(
    "a new %s claim during the final cut write keeps later notification progress",
    async (claimKind) => {
      const fake = connectCurrentClient();
      await cache.get("local:one", Date.now());
      const background = acquireBackgroundThread("local:one");
      await background.ready;
      fake.emitNotification(textDelta("local:one", "-before-cut"));
      const reached = [deferred<void>(), deferred<void>()] as const;
      const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
      const stores = ["records", "meta", "current", "currentMeta"];
      const originalTransaction = IDBDatabase.prototype.transaction;
      let writeCount = 0;
      const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
        this: IDBDatabase,
        ...args: Parameters<IDBDatabase["transaction"]>
      ) {
        const transaction = originalTransaction.apply(this, args);
        if (
          transaction.mode === "readwrite" &&
          transaction.objectStoreNames.length === stores.length &&
          stores.every((store) => transaction.objectStoreNames.contains(store)) &&
          writeCount < 2
        ) {
          const index = writeCount++;
          const gate = holdIndexedDBEvent(transaction, "complete");
          held[index] = gate;
          const completion = reached[index];
          if (!completion) throw new Error(`unexpected cache write ${index + 1}`);
          void gate.reached.then(() => completion.resolve());
        }
        return transaction;
      });
      let replacement: ReturnType<typeof acquireBackgroundThread> | undefined;
      let paneClaimed = false;
      try {
        const startingReads = membershipReads(fake);
        const release = background.release();
        await reached[0].promise;
        fake.emitNotification({
          method: "history/updated",
          params: {
            ref: "local:one",
            threadId: "thr_local:one",
            bootGeneration: "1",
            epoch: 1,
            snapshot: { incarnation: "inc-1", length: 42 },
            turns: [{ id: "turn_cut", status: "completed", itemsView: "full", version: 2 }],
            items: [
              {
                type: "agentMessage",
                id: "item_cut",
                turnId: "turn_cut",
                text: "pre-cut-model",
                position: { entry: 42, item: 0 },
              },
            ],
          },
        } as AnyNotification);
        const firstWrite = held[0];
        if (!firstWrite) throw new Error("first cache write was not held");
        firstWrite.release();
        await reached[1].promise;
        if (claimKind === "background") {
          replacement = acquireBackgroundThread("local:one");
          await replacement.ready;
        } else {
          await threadsStore.getState().ensureThread("local:one");
          paneClaimed = true;
        }
        expect(membershipReads(fake)).toBe(startingReads);
        expect(unsubscriptions(fake)).toBe(0);
        fake.emitNotification(textDelta("local:one", "-during-cut"));
        const secondWrite = held[1];
        if (!secondWrite) throw new Error("second cache write was not held");
        secondWrite.release();
        await release;
        fake.emitNotification(textDelta("local:one", "-after-new-claim"));
        expect(
          threadsStore
            .getState()
            .threads.get("local:one")
            ?.turns.flatMap((turn) => turn.items)
            .map((item) => item.text)
            .join(""),
        ).toContain("pre-cut-modelalpha-before-cut-during-cut-after-new-claim");
        expect(threadsStore.getState().threads.has("local:one")).toBe(true);
        expect(unsubscriptions(fake)).toBe(0);
      } finally {
        transactionSpy.mockRestore();
        for (const gate of held) gate.release();
        await replacement?.release();
        if (paneClaimed) threadsStore.getState().releaseThread("local:one");
        await background.release();
      }
    },
  );

  test.each([true, false])("final release drains a recorded-history update, identity=%s", async (hasIdentity) => {
    const storage = mutationStorage();
    const fake = connectCurrentClient();
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const reached = [deferred<void>(), deferred<void>()] as const;
    const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
    const stores = ["records", "meta", "current", "currentMeta"];
    const receiptCleanupRead = deferred<void>();
    const originalListOptimistic = storage.listOptimistic.bind(storage);
    const listOptimistic = vi.spyOn(storage, "listOptimistic").mockImplementation(async (ref) => {
      const rows = await originalListOptimistic(ref);
      if (ref === "local:one") receiptCleanupRead.resolve();
      return rows;
    });
    const originalTransaction = IDBDatabase.prototype.transaction;
    let writeCount = 0;
    const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
      this: IDBDatabase,
      ...args: Parameters<IDBDatabase["transaction"]>
    ) {
      const transaction = originalTransaction.apply(this, args);
      if (
        transaction.mode === "readwrite" &&
        transaction.objectStoreNames.length === stores.length &&
        stores.every((store) => transaction.objectStoreNames.contains(store)) &&
        writeCount < 2
      ) {
        const index = writeCount++;
        const gate = holdIndexedDBEvent(transaction, "complete");
        held[index] = gate;
        if (index === 0) void gate.reached.then(() => reached[0].resolve());
        else void gate.reached.then(() => reached[1].resolve());
      }
      return transaction;
    });
    try {
      const release = background.release();
      let releaseSettled = false;
      void release.then(() => {
        releaseSettled = true;
      });
      await reached[0].promise;
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: "local:one",
          threadId: "thr_local:one",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 41 },
          turns: [{ id: "turn_final", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "item_final",
              turnId: "turn_final",
              ...(hasIdentity ? { clientMutationId: "receipt-from-another-client" } : {}),
              text: "accepted-during-final-write",
              position: { entry: 41, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      if (hasIdentity) {
        await receiptCleanupRead.promise;
        await nextRealTask();
      }
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      const firstWrite = held[0];
      if (!firstWrite) throw new Error("first cache write was not held");
      firstWrite.release();
      await reached[1].promise;
      expect(releaseSettled).toBe(false);
      const secondWrite = held[1];
      if (!secondWrite) throw new Error("second cache write was not held");
      secondWrite.release();
      await release;
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(unsubscriptions(fake)).toBe(1);
      const durable = JSON.stringify(await cache.get("local:one", Date.now()));
      expect(durable).toContain("accepted-during-final-write");
      expect(durable.split("accepted-during-final-write")).toHaveLength(2);
    } finally {
      transactionSpy.mockRestore();
      listOptimistic.mockRestore();
      for (const gate of held) gate.release();
      await background.release();
    }
  });

  test.each(["background", "pane"] as const)(
    "a %s replacement keeps wire membership through identity receipt cleanup",
    async (replacementKind) => {
      const storage = mutationStorage();
      const fake = connectCurrentClient();
      await cache.get("local:one", Date.now());
      const background = acquireBackgroundThread("local:one");
      await background.ready;
      const reached = [deferred<void>(), deferred<void>()] as const;
      const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
      const stores = ["records", "meta", "current", "currentMeta"];
      const receiptCleanupRead = deferred<void>();
      const originalListOptimistic = storage.listOptimistic.bind(storage);
      const listOptimistic = vi.spyOn(storage, "listOptimistic").mockImplementation(async (ref) => {
        const rows = await originalListOptimistic(ref);
        if (ref === "local:one") receiptCleanupRead.resolve();
        return rows;
      });
      const originalTransaction = IDBDatabase.prototype.transaction;
      let writeCount = 0;
      const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
        this: IDBDatabase,
        ...args: Parameters<IDBDatabase["transaction"]>
      ) {
        const transaction = originalTransaction.apply(this, args);
        if (
          transaction.mode === "readwrite" &&
          transaction.objectStoreNames.length === stores.length &&
          stores.every((store) => transaction.objectStoreNames.contains(store)) &&
          writeCount < reached.length
        ) {
          const index = writeCount++;
          const gate = holdIndexedDBEvent(transaction, "complete");
          held[index] = gate;
          const completion = reached[index];
          if (!completion) throw new Error(`unexpected cache write ${index + 1}`);
          void gate.reached.then(() => completion.resolve());
        }
        return transaction;
      });
      let replacement: ReturnType<typeof acquireBackgroundThread> | undefined;
      let paneClaimed = false;
      try {
        const startingReads = membershipReads(fake);
        const release = background.release();
        await reached[0].promise;
        fake.emitNotification({
          method: "history/updated",
          params: {
            ref: "local:one",
            threadId: "thr_local:one",
            bootGeneration: "1",
            epoch: 1,
            snapshot: { incarnation: "inc-1", length: 41 },
            turns: [{ id: "turn_receipt_claim", status: "completed", itemsView: "full", version: 2 }],
            items: [
              {
                type: "agentMessage",
                id: "item_receipt_claim",
                turnId: "turn_receipt_claim",
                clientMutationId: "receipt-from-another-client",
                text: "receipt-and-replacement",
                position: { entry: 41, item: 0 },
              },
            ],
          },
        } as AnyNotification);
        await receiptCleanupRead.promise;
        await nextRealTask();

        if (replacementKind === "background") {
          replacement = acquireBackgroundThread("local:one");
          await replacement.ready;
        } else {
          await threadsStore.getState().ensureThread("local:one");
          paneClaimed = true;
        }
        expect(membershipReads(fake)).toBe(startingReads);
        expect(unsubscriptions(fake)).toBe(0);
        expect(threadsStore.getState().threads.has("local:one")).toBe(true);

        const firstWrite = held[0];
        if (!firstWrite) throw new Error("first cache write was not held");
        firstWrite.release();
        await reached[1].promise;
        expect(unsubscriptions(fake)).toBe(0);
        fake.emitNotification(textDelta("local:one", "-while-final-write-held"));
        expect(
          threadsStore
            .getState()
            .threads.get("local:one")
            ?.turns.flatMap((turn) => turn.items)
            .map((item) => item.text)
            .join(""),
        ).toContain("-while-final-write-held");
        const secondWrite = held[1];
        if (!secondWrite) throw new Error("final cache write was not held");
        secondWrite.release();
        await release;
        expect(unsubscriptions(fake)).toBe(0);
        fake.emitNotification(textDelta("local:one", "-after-release"));
        expect(
          threadsStore
            .getState()
            .threads.get("local:one")
            ?.turns.flatMap((turn) => turn.items)
            .map((item) => item.text)
            .join(""),
        ).toContain("-while-final-write-held-after-release");
      } finally {
        transactionSpy.mockRestore();
        listOptimistic.mockRestore();
        for (const gate of held) gate.release();
        await replacement?.release();
        if (paneClaimed) threadsStore.getState().releaseThread("local:one");
        await background.release();
      }
    },
  );

  test("final release cuts live updates at the bounded final write", async () => {
    const fake = connectCurrentClient();
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const emitRecorded = (length: number, text: string) => {
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: "local:one",
          threadId: "thr_local:one",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length },
          turns: [{ id: "turn_bounded", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: `item_bounded_${length}`,
              turnId: "turn_bounded",
              text,
              position: { entry: length, item: 0 },
            },
          ],
        },
      } as AnyNotification);
    };
    emitRecorded(40, "before-release");
    const gates: ReturnType<typeof holdIndexedDBEvent>[] = [];
    const stores = ["records", "meta", "current", "currentMeta"];
    const originalTransaction = IDBDatabase.prototype.transaction;
    let writeCount = 0;
    const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
      this: IDBDatabase,
      ...args: Parameters<IDBDatabase["transaction"]>
    ) {
      const transaction = originalTransaction.apply(this, args);
      if (
        transaction.mode === "readwrite" &&
        transaction.objectStoreNames.length === stores.length &&
        stores.every((store) => transaction.objectStoreNames.contains(store))
      ) {
        const index = writeCount++;
        const gate = holdIndexedDBEvent(transaction, "complete");
        gates[index] = gate;
        void gate.reached.then(() => {
          gate.release();
          if (index < 3) fake.emitNotification(textDelta("local:one", `-write-completion-${index + 1}`));
        });
      }
      return transaction;
    });
    try {
      const release = background.release();
      await release;
      expect(writeCount).toBe(2);
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(unsubscriptions(fake)).toBe(1);
      const durable = JSON.stringify(await cache.get("local:one", Date.now()));
      expect(durable).toContain("before-release");
      expect(durable.split("before-release")).toHaveLength(2);
    } finally {
      transactionSpy.mockRestore();
      for (const gate of gates) gate.release();
      await background.release();
    }
  });

  test("a bounded final cache failure does not block another ref release or claim a save", async () => {
    cache.close();
    let failNextPut = true;
    cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `background-thread-failure-${crypto.randomUUID()}`,
      beforeCommit(operation) {
        if (operation === "put" && failNextPut) {
          failNextPut = false;
          throw new Error("injected final cache failure");
        }
      },
    });
    setSessionCacheAdapterForTests(cache);
    await Promise.all([cache.get("local:one", Date.now()), cache.get("local:two", Date.now())]);
    const fake = connectCurrentClient();
    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:two");
    await Promise.all([first.ready, second.ready]);
    for (const [ref, threadId, itemId] of [
      ["local:one", "thr_local:one", "item_failure_one"],
      ["local:two", "thr_local:two", "item_failure_two"],
    ]) {
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref,
          threadId,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 41 },
          turns: [{ id: `turn_${itemId}`, status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: itemId,
              turnId: `turn_${itemId}`,
              text: itemId,
              position: { entry: 41, item: 0 },
            },
          ],
        },
      } as AnyNotification);
    }

    await first.release();
    expect(failNextPut).toBe(false);
    expect(await cache.get("local:one", Date.now())).toBeUndefined();
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);

    await second.release();
    expect(threadsStore.getState().threads.has("local:two")).toBe(false);
    expect(await cache.get("local:two", Date.now())).toBeDefined();
    expect(unsubscriptions(fake)).toBe(2);
  });

  test("keeps the model owned until an in-flight history page finishes", async () => {
    const fake = connectCurrentClient();
    await threadsStore.getState().ensureThread("local:one");
    const entered = deferred<void>();
    const page = deferred<ThreadTurnsListResponse>();
    fake.on("thread/turns/list", () => {
      entered.resolve();
      return page.promise;
    });

    const loading = threadsStore.getState().loadOlderTurns("local:one");
    await entered.promise;
    threadsStore.getState().releaseThread("local:one");
    expect(threadsStore.getState().threads.has("local:one")).toBe(true);

    page.resolve({ data: [], bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc-1", length: 40 } });
    await loading;
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });
});
