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
import { type CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { holdIndexedDBEvent, holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  installHydrationRetrySchedulerForTests,
  observePendingHydrationRetirementsForTests,
  pendingThreadHydrationBaseModelsForTests,
  pendingThreadHydrationUpdatesForTests,
  putThreadModel,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  setSessionCacheAdapterForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";
import { observeWorkerCheckpointsForTests } from "./transcriptResidency";

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

function occurrences(value: string, needle: string): number {
  return value.split(needle).length - 1;
}

// Two models share one logical transcript body when they are the same model or
// carry identical payload fields, turns and overlay, ignoring only the read
// bookkeeping generation a resync bumps.
function transcriptBodySignature(model: ThreadModel): string | undefined {
  const state = currentTranscriptState(model);
  if (!state) return undefined;
  return JSON.stringify({
    fields: state.fields,
    overlay: state.overlay,
    history: { ...state.history, issuedGeneration: 0, appliedGeneration: 0 },
  });
}

function sharesTranscriptBody(base: ThreadModel, canonical: ThreadModel): boolean {
  if (base === canonical) return true;
  const baseSignature = transcriptBodySignature(base);
  return baseSignature !== undefined && baseSignature === transcriptBodySignature(canonical);
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

  test.each(["settled checkpoint", "unfinished captured work"])(
    "promotes once across a held current lookup and applies notifications on both sides, %s",
    async (phase) => {
      const overlapCapturedWork = phase === "unfinished captured work";
      const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 4096 });
      resetThreadsStoreForTests();
      setSessionCacheAdapterForTests(cache);
      const storage = mutationStorage();
      const targetScans = deferred<void>();
      const originalListTargetRefs = storage.listTargetRefs.bind(storage);
      let targetScanCount = 0;
      const targetScanSpy = vi.spyOn(storage, "listTargetRefs").mockImplementation(async () => {
        const refs = await originalListTargetRefs();
        targetScanCount += 1;
        if (targetScanCount >= 3) targetScans.resolve();
        return refs;
      });
      const fake = connectCurrentClient();
      fake.on("thread/read", ({ ref }) => {
        const response = currentRead(ref ?? "local:one");
        response.thread.evener = { ...response.thread.evener, instanceId: "instance-canonical" };
        response.overlay?.push({
          key: "stream:pre-cut/toolOutput",
          kind: "stream",
          streamId: "pre-cut",
          roundId: "pre-cut-round",
          turnId: "turn_1",
          item: {
            id: "pre-cut-tool",
            turnId: "turn_1",
            type: "commandExecution",
            output: "pre-cut-tool🙂",
            status: "inProgress",
          },
        });
        return response;
      });
      const background = acquireBackgroundThread("local:one");
      const completionSource = acquireBackgroundThread("local:completion-source");
      let paneHeld = false;
      let heldLookup: ReturnType<typeof holdNextWriteTransaction> | undefined;
      let heldCapturedWrite: ReturnType<typeof holdNextWriteTransaction> | undefined;
      let capturedWriteCompleted = false;
      let lookupStarted = false;
      const phases: string[] = [];
      const promotionLookupReached = deferred<void>();
      const releasePromotionLookup = deferred<void>();
      const lookupTransactionReached = deferred<void>();
      const releaseLookupTransaction = deferred<void>();
      const lookupWorkCommitted = deferred<void>();
      const sourceWriteReached = deferred<void>();
      const releaseSourceWrite = deferred<void>();
      const postCutWriteReached = deferred<void>();
      const releasePostCutWrite = deferred<void>();
      let postCutWriteCompleted = false;
      let postCutWriteText: string | undefined;
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
            // The transaction commits, but its completion callback remains held.
            // The real adapter and coordinator still await that operation.
            heldCapturedWrite = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
            const write = originalPutCurrent(record, now);
            await heldCapturedWrite.reached;
            phases.push("captured callback held");
            firstWriteReached?.resolve();
            await releaseFirstWrite?.promise;
            heldCapturedWrite.release();
            const result = await write;
            capturedWriteCompleted = true;
            phases.push("captured write returned");
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
            phases.push("completion updates emitted");
            return result;
          }
          if (record.ref === "local:one" && record.appliedSequence === 5) {
            laterWriteReached?.resolve();
            await releaseLaterWrite?.promise;
          }
          if (overlapCapturedWork && record.ref === "local:one" && record.appliedSequence > 5) {
            phases.push("later target write held");
            postCutWriteReached.resolve();
            await releasePostCutWrite.promise;
          }
          if (overlapCapturedWork && record.ref === "local:completion-source" && record.appliedSequence === 1) {
            sourceWriteReached.resolve();
            await releaseSourceWrite.promise;
          }
          const result = await originalPutCurrent(record, now);
          if (overlapCapturedWork && record.ref === "local:one" && record.appliedSequence === 5) {
            // The ownership retry completes the captured cut. Replenish target
            // work at this actual completion too, not just during local lookup.
            fake.emitNotification(textDelta("local:one", "-post-cut"));
            fake.emitNotification({
              method: "overlay/delta",
              params: {
                threadId: "thr_local:one",
                ref: "local:one",
                key: "stream:round_tool/0:toolOutput",
                field: "output",
                delta: ":post-cut",
              },
            } as AnyNotification);
            phases.push("cut retry returned and replenished target");
          }
          if (overlapCapturedWork && record.ref === "local:one" && record.appliedSequence > 5) {
            expect(result.outcome).toBe("written");
            postCutWriteCompleted = true;
            postCutWriteText = JSON.stringify(record.state);
          }
          if (record.ref === "local:completion-source" && record.appliedSequence === 1) {
            fake.emitNotification(textDelta("local:one", "-completion-in-lookup"));
            lookupWorkCommitted.resolve();
          }
          return result;
        });
        const originalGetCurrent = cache.getCurrent.bind(cache);
        let targetLookupCount = 0;
        getCurrentSpy = vi.spyOn(cache, "getCurrent").mockImplementation(async (...args) => {
          if (args[0][1] === "local:one") targetLookupCount += 1;
          if (armPromotionLookup && args[0][1] === "local:one") {
            armPromotionLookup = false;
            lookupStarted = true;
            phases.push("promotion lookup reached");
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
        if (overlapCapturedWork) {
          expect(capturedWriteCompleted, "ensure must start before the captured completion").toBe(false);
          expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(1);
          expect(threadsStore.getState().threads.has("local:one")).toBe(false);
          const lookupsBeforeEnsure = targetLookupCount;
          phases.push("ensure started with captured work unfinished");
          promotion = threadsStore.getState().ensureThread("local:one");
          paneHeld = true;
          // ensureThread synchronously acquires the pane and enters promote's
          // cut wait. Complete an independent durable outbox read while the
          // target completion remains held, not a sleep or microtask flush.
          expect(await storage.listOutbox("local:one")).toEqual(outboxBeforePromotion);
          expect(targetLookupCount).toBe(lookupsBeforeEnsure);
          expect(capturedWriteCompleted).toBe(false);
          expect(lookupStarted).toBe(false);
          expect(promotedAtPublication).toBeUndefined();
          expect(threadsStore.getState().threads.has("local:one")).toBe(false);
          expect(threadsStore.getState().mutationAuthorityRefs.has("local:one")).toBe(false);
          expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readCount);
          expect(
            fake.calls.filter((call) =>
              ["thread/resume", "turn/start", "turn/steer", "turn/queue", "turn/interrupt"].includes(call.method),
            ),
          ).toEqual([]);
        }
        releaseFirstWrite.resolve();
        await laterWriteReached.promise;
        // Foreground acquisition can require the already-issued write's real
        // ownership retry. Do not mistake that worker load for promotion lookup.
        if (overlapCapturedWork) {
          // Queue this ref before the retry completes. It occupies the single
          // worker while promotion starts its target lookup, without preemption
          // or an internal lookup-call ordinal deciding which read to gate.
          fake.emitNotification(textDelta("local:completion-source", "-source-write"));
        }
        armPromotionLookup = overlapCapturedWork;
        releaseLaterWrite.resolve();
        if (!overlapCapturedWork) {
          await settleBackgroundTranscriptsForTests();
          armPromotionLookup = true;
          promotion = threadsStore.getState().ensureThread("local:one");
          paneHeld = true;
        }
        await promotionLookupReached.promise;
        if (overlapCapturedWork) {
          expect(phases).toEqual([
            "captured callback held",
            "ensure started with captured work unfinished",
            "captured write returned",
            "completion updates emitted",
            "cut retry returned and replenished target",
            "promotion lookup reached",
          ]);
          await sourceWriteReached.promise;
          releaseSourceWrite.resolve();
        } else {
          fake.emitNotification(textDelta("local:completion-source", "-source-write"));
        }
        await lookupWorkCommitted.promise;
        if (overlapCapturedWork) await postCutWriteReached.promise;
        else await settleBackgroundTranscriptsForTests();
        releasePromotionLookup.resolve();
        await lookupTransactionReached.promise;
        const toolKey = "stream:round_tool/0:toolOutput";
        fake.emitNotification({
          method: "overlay/delta",
          params: {
            threadId: "thr_local:one",
            ref: "local:one",
            key: toolKey,
            field: "output",
            delta: ":during-lookup",
          },
        } as AnyNotification);
        fake.emitNotification(textDelta("local:one", "-during-lookup"));
        expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThan(0);
        if (!heldLookup) throw new Error("promotion lookup transaction was not held");
        heldLookup.release();
        releaseLookupTransaction.resolve();
        await promotion;
        paneHeld = true;
        if (overlapCapturedWork) {
          phases.push("published with later target write unfinished");
          expect(postCutWriteCompleted).toBe(false);
          expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(1);
          expect(phases.slice(-2)).toEqual(["later target write held", "published with later target write unfinished"]);
        }

        const adopted = threadsStore.getState().threads.get("local:one");
        expect(adopted).toBeDefined();
        expect(adopted).toBe(promotedAtPublication);
        expect(adopted?.capabilities.send).toBe(true);
        expect(adopted?.capabilities.queue).toBe(true);
        expect(adopted?.capabilitySource).toBe("read");
        expect(adopted?.instanceId).toBe("instance-canonical");
        expect(adopted?.turns.flatMap((turn) => turn.items).find((item) => item.id === "pre-cut-tool")?.output).toBe(
          "pre-cut-tool🙂",
        );
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
          `alpha-captured-completion-4-completion-5${overlapCapturedWork ? "-post-cut" : ""}-completion-in-lookup-during-lookup-after-publication`,
        );
        expect(text.split("-captured"), "pre-cut update must be applied exactly once").toHaveLength(2);
        let previousIndex = text.indexOf("-captured");
        for (const marker of [
          "-completion-4",
          "-completion-5",
          ...(overlapCapturedWork ? ["-post-cut"] : []),
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
        const completionToolOutput = `工具🙂${overlapCapturedWork ? ":post-cut" : ""}:during-lookup`;
        expect(toolItems[0]?.output).toBe(completionToolOutput);
        const afterNotifications = threadsStore.getState().threads.get("local:one");
        expect(
          afterNotifications?.turns.flatMap((turn) => turn.items).find((item) => item.id === "item_tool")?.output,
        ).toBe(`${completionToolOutput}:after-publication`);
        expect(afterNotifications?.overlay?.[toolKey]?.item.output).toBe(`${completionToolOutput}:after-publication`);
        expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readCount);
        expect(await storage.listOutbox("local:one")).toEqual(outboxBeforePromotion);
        expect(
          fake.calls.filter((call) =>
            ["thread/resume", "turn/start", "turn/steer", "turn/queue", "turn/interrupt"].includes(call.method),
          ),
        ).toEqual([]);
        releasePostCutWrite.resolve();
        await settleBackgroundTranscriptsForTests();
        if (overlapCapturedWork) {
          expect(postCutWriteCompleted).toBe(true);
          expect(postCutWriteText).toContain("alpha-captured-completion-4-completion-5-post-cut-completion-in-lookup");
          expect(postCutWriteText?.split("-captured")).toHaveLength(2);
          expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0);
        }
        fake.emitNotification(textDelta("local:one", "-after-drain"));
        fake.emitNotification({
          method: "overlay/delta",
          params: { threadId: "thr_local:one", ref: "local:one", key: toolKey, field: "output", delta: ":after-drain" },
        } as AnyNotification);
        const afterDrain = threadsStore.getState().threads.get("local:one");
        expect(afterDrain?.turns.flatMap((turn) => turn.items).find((item) => item.id === "a1")?.text).toBe(
          `${text}-after-drain`,
        );
        expect(afterDrain?.turns.flatMap((turn) => turn.items).find((item) => item.id === "pre-cut-tool")?.output).toBe(
          "pre-cut-tool🙂",
        );
        expect(afterDrain?.turns.flatMap((turn) => turn.items).find((item) => item.id === "item_tool")?.output).toBe(
          `${completionToolOutput}:after-publication:after-drain`,
        );
        expect(afterDrain?.overlay?.[toolKey]?.item.output).toBe(
          `${completionToolOutput}:after-publication:after-drain`,
        );
        expect(await storage.listOutbox("local:one")).toEqual(outboxBeforePromotion);
        expect(threadsStore.getState().mutationAuthorityRefs.has("local:one")).toBe(false);
        expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readCount);
        expect(
          fake.calls.filter((call) =>
            ["thread/resume", "turn/start", "turn/steer", "turn/queue", "turn/interrupt"].includes(call.method),
          ),
        ).toEqual([]);
      } finally {
        heldCapturedWrite?.release();
        heldLookup?.release();
        releaseLookupTransaction.resolve();
        releasePromotionLookup.resolve();
        releaseFirstWrite?.resolve();
        releaseLaterWrite?.resolve();
        releaseSourceWrite.resolve();
        releasePostCutWrite.resolve();
        getCurrentSpy?.mockRestore();
        putCurrentSpy?.mockRestore();
        targetScanSpy.mockRestore();
        await promotion?.catch(() => undefined);
        observePromotion();
        if (paneHeld) threadsStore.getState().releaseThread("local:one");
        await completionSource.release();
        await background.release();
        resetThreadsStoreForTests();
        restoreOptions();
      }
    },
  );

  test("accounts two completed hydrations and deltas while the real checkpoint transaction is held", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 1024,
      pendingBytes: 4 * 1024 * 1024,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const one = deferred<ReturnType<typeof currentRead>>();
    const two = deferred<ReturnType<typeof currentRead>>();
    const writerResync = deferred<ReturnType<typeof currentRead>>();
    let writerReads = 0;
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => {
      if (ref === "local:one") return one.promise;
      if (ref === "local:two") return two.promise;
      if (ref === "local:writer") {
        writerReads += 1;
        return writerReads === 1 ? currentRead("local:writer") : writerResync.promise;
      }
      return currentRead("local:writer");
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const writer = acquireBackgroundThread("local:writer");
    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:two");
    let heldWrite: CurrentSessionRecord | undefined;
    const putCurrentStarted = deferred<void>();
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    let secondHoldArmed = false;
    let secondTransaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const secondWriteStarted = deferred<void>();
    const writtenRecords: CurrentSessionRecord[] = [];
    const workerCheckpoints: Array<{ ref: string; batchLength: number }> = [];
    let foregroundPromotion = false;
    const publicationAccounting: Array<{ retainedBytes: number; chargedBytes: number }> = [];
    const coverageFailures: unknown[] = [];
    // Independent simultaneous inventory of every retained class, with shared
    // identity de-duplication and a paired live reservation per class:
    // - canonical store models -> that ref's resident+handoff charge
    // - coordinator pending/inFlight frames -> pending budget
    // - hydration notification buffers -> pending budget
    // - hydration base models -> the canonical body they share, never a global
    //   spare charge
    // The worker checkpoint body is covered separately at the call-through
    // putCurrent boundary, where the folded model and record are both visible.
    // Incoming decoding, JSON serialization temporaries and IndexedDB copies are
    // excluded; a base model shared with the store model is counted once by
    // identity, never as a second body.
    const accountRetainedInventory = (state: { threads: Map<string, ThreadModel> }) => {
      const snapshot = backgroundTranscriptSnapshotForTests();
      const seen = new Set<object>();
      let retainedBytes = 0;
      const claim = (identity: object, bytes: number, reservation: number, label: string) => {
        if (seen.has(identity)) return;
        seen.add(identity);
        retainedBytes += bytes;
        expect(reservation, `${label} covered by its live reservation`).toBeGreaterThanOrEqual(bytes);
      };
      for (const [ref, model] of state.threads) {
        const ownership = snapshot.refs.get(ref);
        claim(
          model,
          independentJsonBytes(currentTranscriptState(model)),
          (ownership?.residentBytes ?? 0) + (ownership?.handoffBytes ?? 0),
          `canonical model ${ref}`,
        );
      }
      let queuedBytes = 0;
      for (const entry of snapshot.refs.values()) {
        for (const update of [...entry.pending, ...entry.inFlight]) {
          const bytes = independentJsonBytes(update.notification);
          expect(bytes, `coordinator frame ${update.sequence}`).toBe(update.bytes);
          queuedBytes += bytes;
          claim(update.notification, bytes, snapshot.pendingBytes, `coordinator frame ${update.sequence}`);
        }
      }
      let hydrationBytes = 0;
      for (const update of pendingThreadHydrationUpdatesForTests()) {
        const bytes = independentJsonBytes(update.notification);
        expect(bytes).toBe(update.bytes);
        hydrationBytes += bytes;
        claim(update.notification, bytes, snapshot.pendingBytes, `hydration frame ${update.sequence}`);
      }
      const handoffTotal = [...snapshot.refs.values()].reduce((sum, entry) => sum + entry.handoffBytes, 0);
      // A hydration base is the canonical model or shares its body; it is never
      // a distinct uncharged transcript covered by unrelated spare charges.
      for (const base of pendingThreadHydrationBaseModelsForTests()) {
        const canonical = state.threads.get(base.ref);
        expect(
          canonical !== undefined && sharesTranscriptBody(base, canonical),
          `hydration base ${base.ref} shares its canonical body`,
        ).toBe(true);
      }
      // Bucket sum: the pending budget is exactly the handoff charges plus the
      // actual queued frames plus the hydration notification bytes.
      expect(snapshot.pendingBytes, JSON.stringify({ handoffTotal, queuedBytes, hydrationBytes })).toBe(
        handoffTotal + queuedBytes + hydrationBytes,
      );
      return { retainedBytes, chargedBytes: snapshot.residentBytes + snapshot.pendingBytes + snapshot.workerBytes };
    };
    // The subscriber's own failures are swallowed by the notification emitter,
    // so record them here and assert them empty at each visible check.
    const assertCoverage = (label: string) => {
      const measured = accountRetainedInventory(threadsStore.getState());
      expect(measured.chargedBytes, label).toBeGreaterThanOrEqual(measured.retainedBytes);
      expect(coverageFailures, `${label}: subscriber coverage`).toEqual([]);
    };
    const observeAccounting = threadsStore.subscribe((state) => {
      if (foregroundPromotion) return;
      try {
        publicationAccounting.push(accountRetainedInventory(state));
      } catch (error) {
        coverageFailures.push(error);
      }
    });
    // The worker-boundary observer sees the folded worker model and the exact
    // record it is about to write, which the store and cache ports cannot.
    const restoreWorkerObserver = observeWorkerCheckpointsForTests((observation) => {
      const snapshot = backgroundTranscriptSnapshotForTests();
      const modelState = currentTranscriptState(observation.model);
      expect(modelState, `worker model ${observation.ref} has a recorded state`).toBeDefined();
      const stateBytes = independentJsonBytes(modelState);
      const recordBytes = independentJsonBytes(observation.record);
      expect(stateBytes).toBe(encodedPayloadBytes(modelState));
      expect(independentJsonBytes(observation.record.state)).toBe(stateBytes);
      expect(recordBytes).toBe(encodedPayloadBytes(observation.record));
      expect(recordBytes).toBeGreaterThanOrEqual(stateBytes);
      // The larger model-state/record encoding is charged exactly once.
      expect(snapshot.workerBytes).toBe(recordBytes);
      const ownership = snapshot.refs.get(observation.ref);
      if (observation.resident && observation.resident !== observation.model) {
        const residentBytes = independentJsonBytes(currentTranscriptState(observation.resident));
        expect(ownership?.residentBytes ?? 0, `worker starting body ${observation.ref}`).toBeGreaterThanOrEqual(
          residentBytes,
        );
      }
      workerCheckpoints.push({ ref: observation.ref, batchLength: observation.batch.length });
    });
    const originalPutCurrent = cache.putCurrent.bind(cache);
    const putCurrent = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (!transaction) {
        heldWrite = record;
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        putCurrentStarted.resolve();
      } else if (secondHoldArmed && !secondTransaction && record.ref === "local:one") {
        secondHoldArmed = false;
        secondTransaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        secondWriteStarted.resolve();
      }
      const outcome = await originalPutCurrent(record, now);
      if (outcome.outcome === "written") writtenRecords.push(record);
      return outcome;
    });
    const payload = (ref: string) => {
      const response = currentRead(ref);
      response.overlay![0]!.item.text = "x".repeat(1024 * 1024);
      return response;
    };
    try {
      await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3));
      await putCurrentStarted.promise;
      await transaction?.reached;
      expect(threadsStore.getState().threads.has("local:writer")).toBe(true);
      assertCoverage("writer hydration published with its write held");
      fake.emitNotification(textDelta("local:one", "buffered-one"));
      fake.emitNotification(textDelta("local:two", "buffered-two"));
      const bufferedUpdates = pendingThreadHydrationUpdatesForTests();
      const independentlyMeasuredBufferedBytes = bufferedUpdates.reduce((sum, update) => {
        const bytes = independentJsonBytes(update.notification);
        expect(bytes).toBe(update.bytes);
        return sum + bytes;
      }, 0);
      expect(bufferedUpdates).toHaveLength(2);
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThanOrEqual(
        independentlyMeasuredBufferedBytes,
      );
      one.resolve(payload("local:one"));
      two.resolve(payload("local:two"));
      await Promise.all([waitForHydration("local:one", 1), waitForHydration("local:two", 1)]);
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      expect(threadsStore.getState().threads.has("local:two")).toBe(true);
      assertCoverage("both hydrations published while the write is held");

      // Phase 1 (still held): one deferred writer resync through the real
      // notification seam exposes a nonempty hydration base and buffer.
      fake.emitNotification({
        method: "evener/thread/resync",
        params: { ref: "local:writer", threadId: "thr_local:writer" },
      } as AnyNotification);
      await vi.waitFor(() => expect(writerReads).toBeGreaterThanOrEqual(2));
      await vi.waitFor(() => expect(pendingThreadHydrationBaseModelsForTests()).toHaveLength(1));
      const resyncBase = pendingThreadHydrationBaseModelsForTests()[0]!;
      expect(resyncBase.ref).toBe("local:writer");
      const resyncCanonical = threadsStore.getState().threads.get("local:writer");
      expect(resyncCanonical).toBe(resyncBase);
      expect(sharesTranscriptBody(resyncBase, resyncCanonical!)).toBe(true);
      expect(resyncBase.history?.turns).toBe(resyncCanonical!.history?.turns);
      expect(resyncBase.overlay).toBe(resyncCanonical!.overlay);
      const baseOwnership = backgroundTranscriptSnapshotForTests().refs.get("local:writer");
      expect(
        (baseOwnership?.residentBytes ?? 0) + (baseOwnership?.handoffBytes ?? 0),
        "resync base covered by its canonical reservation",
      ).toBeGreaterThanOrEqual(independentJsonBytes(currentTranscriptState(resyncBase)));
      fake.emitNotification(textDelta("local:writer", "-resync-buffered"));
      await vi.waitFor(() => expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1));
      assertCoverage("nonempty resync base and buffer while the write is held");
      const resyncResponse = currentRead("local:writer");
      resyncResponse.requestGeneration = resyncBase.history?.issuedGeneration ?? 1;
      writerResync.resolve(resyncResponse);
      await vi.waitFor(() => expect(pendingThreadHydrationBaseModelsForTests()).toHaveLength(0));
      await vi.waitFor(() => expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(0));

      const firstFrame = textDelta("local:one", "🙂".repeat(1024));
      fake.emitNotification(firstFrame);
      const firstWrite = heldWrite;
      expect(firstWrite).toBeDefined();
      const heldSnapshot = backgroundTranscriptSnapshotForTests();
      const firstBodyBytes = independentJsonBytes(firstWrite?.state);
      const firstModel = threadsStore.getState().threads.get("local:one");
      const secondModel = threadsStore.getState().threads.get("local:two");
      expect(firstModel).toBeDefined();
      expect(secondModel).toBeDefined();
      const firstStateBytes = independentJsonBytes(currentTranscriptState(firstModel!));
      const secondStateBytes = independentJsonBytes(currentTranscriptState(secondModel!));
      expect(firstBodyBytes).toBeGreaterThan(0);
      const remainingResidentRoom = 1024 - heldSnapshot.residentBytes;
      expect(firstStateBytes).toBeGreaterThan(remainingResidentRoom);
      expect(secondStateBytes).toBeGreaterThan(remainingResidentRoom);
      expect(secondStateBytes).toBeGreaterThan(1024);
      expect(firstStateBytes).toBeLessThan(32 * 1024 * 1024);
      expect(secondStateBytes).toBeLessThan(32 * 1024 * 1024);
      // The held checkpoint write is the writer's; its captured batch was empty.
      expect(
        workerCheckpoints.some((checkpoint) => checkpoint.ref === "local:writer" && checkpoint.batchLength === 0),
      ).toBe(true);
      expect(heldSnapshot.workerBytes).toBeGreaterThanOrEqual(firstBodyBytes);
      expect(independentJsonBytes(firstWrite)).toBeGreaterThan(firstBodyBytes);
      assertCoverage("first emoji frame accepted");

      for (let i = 0; i < 15; i += 1) {
        const frame = textDelta("local:one", "🙂".repeat(1024));
        fake.emitNotification(frame);
        assertCoverage(`emoji frame ${i}`);
        const snapshot = backgroundTranscriptSnapshotForTests();
        expect(snapshot.residentBytes).toBeLessThanOrEqual(1024);
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
      fake.emitNotification(toolUpsert);
      fake.emitNotification(toolOutput);
      expect(threadsStore.getState().threads.get("local:one")?.overlay?.[toolKey]?.item.output).toContain(
        '"retained":true',
      );
      assertCoverage("structured tool notification accepted");
      expect(publicationAccounting.length).toBeGreaterThan(0);
      expect(publicationAccounting.every(({ retainedBytes, chargedBytes }) => chargedBytes >= retainedBytes)).toBe(
        true,
      );
      expect(encodedPayloadBytes({ text: "🙂", raw: { output: ["工具", 7] } })).toBe(
        new TextEncoder().encode(JSON.stringify({ text: "🙂", raw: { output: ["工具", 7] } })).byteLength,
      );
      expect(backgroundTranscriptSnapshotForTests().workerBytes).toBeGreaterThan(0);
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeLessThanOrEqual(4 * 1024 * 1024);

      transaction?.release();
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0), { timeout: 1000 });
      await settleBackgroundTranscriptsForTests();
      const completed = backgroundTranscriptSnapshotForTests();
      expect(completed.diskWorkers).toBe(0);
      expect(completed.refs.get("local:one")?.current, JSON.stringify(completed.refs.get("local:one"))).toBe(true);
      expect(completed.refs.get("local:two")?.current).toBe(true);
      expect(pendingThreadHydrationUpdatesForTests()).toEqual([]);
      expect(pendingThreadHydrationBaseModelsForTests()).toEqual([]);
      expect([...completed.refs.values()].every((entry) => entry.inFlight.length === 0)).toBe(true);
      expect(completed.workerBytes).toBe(0);
      assertCoverage("initial draining quiescent");
      // Initial draining has made local:one disk-only.
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(completed.refs.get("local:one")?.resident).toBe(false);
      expect(completed.refs.get("local:one")?.residentBytes).toBe(0);
      expect(completed.refs.get("local:one")?.handoffBytes).toBe(0);

      // Phase 2: hold local:one's next real write, capture one ordinary batch,
      // then let a second delta queue behind it as a pending tail.
      secondHoldArmed = true;
      const capturedDelta = textDelta("local:one", "-captured-batch");
      fake.emitNotification(capturedDelta);
      assertCoverage("nonresident delta reserved");
      await secondWriteStarted.promise;
      await secondTransaction?.reached;
      const heldBatch = backgroundTranscriptSnapshotForTests().refs.get("local:one");
      expect(heldBatch?.inFlight).toHaveLength(1);
      expect(heldBatch?.inFlight[0]?.notification).toBe(capturedDelta);
      expect(heldBatch?.pending).toHaveLength(0);
      const tailDelta = textDelta("local:one", "-pending-tail");
      fake.emitNotification(tailDelta);
      assertCoverage("captured batch plus pending tail");
      const withTail = backgroundTranscriptSnapshotForTests().refs.get("local:one");
      expect(withTail?.inFlight).toHaveLength(1);
      expect(withTail?.inFlight[0]?.notification).toBe(capturedDelta);
      expect(withTail?.pending).toHaveLength(1);
      expect(withTail?.pending[0]?.notification).toBe(tailDelta);
      secondTransaction?.release();
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0), { timeout: 1000 });
      await settleBackgroundTranscriptsForTests();
      assertCoverage("captured batch and pending tail committed");

      // Both chunks reach the real disk content exactly once.
      const localOneWrite = writtenRecords.filter((record) => record.ref === "local:one").at(-1);
      if (!localOneWrite) throw new Error("expected a real local:one checkpoint");
      const onDisk = await cache.getCurrent([localOneWrite.ownerToken, "local:one"], Date.now());
      const onDiskText = JSON.stringify(onDisk?.record?.state);
      expect(occurrences(onDiskText, "-captured-batch")).toBe(1);
      expect(occurrences(onDiskText, "-pending-tail")).toBe(1);

      // Local reopening serves that same disk content with no further read.
      const readsBeforePromotion = fake.calls.filter((call) => call.method === "thread/read").length;
      foregroundPromotion = true;
      await threadsStore.getState().ensureThread("local:one");
      const promoted = threadsStore.getState().threads.get("local:one");
      const text =
        promoted?.turns
          .flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join("") ?? "";
      expect(text.match(/🙂/gu)).toHaveLength(1024 * 16);
      const reopenedText = JSON.stringify(promoted ? currentTranscriptState(promoted) : undefined);
      expect(occurrences(reopenedText, "-captured-batch")).toBe(1);
      expect(occurrences(reopenedText, "-pending-tail")).toBe(1);
      await threadsStore.getState().ensureThread("local:two");
      expect(
        threadsStore.getState().threads.get("local:two")?.overlay?.["stream:s1:agentMessage"]?.item.text,
      ).toContain("x".repeat(1024));
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforePromotion);
    } finally {
      one.resolve(currentRead("local:one"));
      two.resolve(currentRead("local:two"));
      writerResync.resolve(currentRead("local:writer"));
      transaction?.release();
      secondTransaction?.release();
      putCurrent.mockRestore();
      restoreWorkerObserver();
      observeAccounting();
      threadsStore.getState().releaseThread("local:one");
      threadsStore.getState().releaseThread("local:two");
      await Promise.all([writer.release(), first.release(), second.release()]);
      await settleBackgroundTranscriptsForTests();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("refuses an oversized resident notification at the real publication gate while the write is held, then recovers", async () => {
    const ref = "local:refuse";
    const base = hydrateThread(currentRead(ref), ref, 1);
    const baseBytes = encodedPayloadBytes(currentTranscriptState(base));
    const marker = "-oversized-candidate";
    const oversizedFrame = textDelta(ref, marker.repeat(6000));
    const frameBytes = encodedPayloadBytes(oversizedFrame);
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      // The real hydrated store body is a few bytes larger than the fixture
      // model measured above, so allow that slack while still refusing the
      // 120 KiB candidate.
      residentBytes: baseBytes + 64,
      pendingBytes: frameBytes,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested }) => currentRead(requested ?? ref));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const writeStarted = deferred<void>();
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (!transaction && record.ref === ref) {
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        writeStarted.resolve();
      }
      return originalPut(record, now);
    });
    const lease = acquireBackgroundThread(ref);
    let reacquired: ReturnType<typeof acquireBackgroundThread> | undefined;
    try {
      await lease.ready;
      await writeStarted.promise;
      await transaction?.reached;
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      const preFrame = backgroundTranscriptSnapshotForTests();
      const baseCharged = preFrame.refs.get(ref)?.residentBytes;
      if (!baseCharged) throw new Error("expected the real base model to be admitted");
      expect(preFrame.refs.get(ref)?.handoffBytes).toBe(0);

      // The store folds the notification into the resident, asks the real
      // publication gate to admit it, and must revert when admission refuses.
      fake.emitNotification(oversizedFrame);
      const refusedModel = threadsStore.getState().threads.get(ref);
      if (!refusedModel) throw new Error("expected the base model to remain published");
      expect(JSON.stringify(currentTranscriptState(refusedModel))).not.toContain(marker);
      const refused = backgroundTranscriptSnapshotForTests();
      expect(refused.refs.get(ref)?.residentBytes).toBe(baseCharged);
      expect(refused.refs.get(ref)?.handoffBytes).toBe(0);
      expect(refused.pendingBytes).toBe(0);
      expect(refused.refs.get(ref)?.persistence).toBe("repairing");

      // Releasing the held write and the lease, then re-acquiring, forces a
      // fresh real background hydration through the authoritative publication
      // gate; the refused candidate never appears and the ref returns to
      // useful, current state.
      transaction?.release();
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      reacquired = acquireBackgroundThread(ref);
      await reacquired.ready;
      await settleBackgroundTranscriptsForTests();
      const recovered = threadsStore.getState().threads.get(ref);
      if (!recovered) throw new Error("expected the ref to recover through the real publication gate");
      expect(JSON.stringify(currentTranscriptState(recovered))).not.toContain(marker);
      const recoveredEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect(recoveredEntry?.persistence).toBe("enabled");
      expect(recoveredEntry?.current).toBe(true);
    } finally {
      transaction?.release();
      put.mockRestore();
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      await reacquired?.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("recovers a background ref through a real cache miss without replaying the dropped frame", async () => {
    const ref = "local:miss";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: encodedPayloadBytes(currentTranscriptState(hydrateThread(currentRead(ref), ref, 1))),
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested }) => currentRead(requested ?? ref));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const writes: Array<{ ref: string; state: unknown; ownerToken: string }> = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const lease = acquireBackgroundThread(ref);
    const other = acquireBackgroundThread("local:other");
    try {
      await Promise.all([lease.ready, other.ready]);
      await settleBackgroundTranscriptsForTests();
      // The second resident evicted the target while keeping its background
      // ownership; deleting the real checkpoint makes the next write look up a
      // genuinely missing record.
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      expect(await cache.deleteRecords([ref])).toBe(true);

      const stale = textDelta(ref, "-stale-frame");
      fake.emitNotification(stale);
      await waitForHydration(ref, 2);
      await settleBackgroundTranscriptsForTests();
      const recoveredEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect(recoveredEntry?.persistence).toBe("enabled");
      expect(recoveredEntry?.current).toBe(true);
      const recoveredWrite = writes.filter((write) => write.ref === ref).at(-1);
      if (!recoveredWrite) throw new Error("expected the recovered ref to reach the real cache");
      const onDisk = await cache.getCurrent([recoveredWrite.ownerToken, ref], Date.now());
      expect(JSON.stringify(onDisk?.record?.state)).not.toContain("-stale-frame");
      expect(JSON.stringify(onDisk?.record?.state)).toContain("alpha");
    } finally {
      await settleBackgroundTranscriptsForTests();
      put.mockRestore();
      await Promise.all([lease.release(), other.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("recovers a real oversized cache hit through the base guard and a same-lease authoritative repair", async () => {
    const ref = "local:oversized-base";
    const maxBytes = 64 * 1024 * 1024;
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const localCache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `background-oversized-${crypto.randomUUID()}`,
      maxBytes,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(localCache);
    const fake = new FakeClient("ready");
    const readIssued = deferred<void>();
    const releaseRead = deferred<void>();
    let holdReads = false;
    fake.on("thread/read", async ({ ref: requested }) => {
      if (holdReads) {
        readIssued.resolve();
        await releaseRead.promise;
      }
      return currentRead(requested ?? ref);
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);

    // Call-through observers over the real test-local adapter: capture the
    // initial successful checkpoint's key/certificate, and record every real
    // getCurrent so an oversized hit is told apart from a cache miss.
    const writes: CurrentSessionRecord[] = [];
    const lookups: CurrentSessionRecord[] = [];
    const originalPut = localCache.putCurrent.bind(localCache);
    const put = vi.spyOn(localCache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const originalGet = localCache.getCurrent.bind(localCache);
    const get = vi.spyOn(localCache, "getCurrent").mockImplementation(async (key, now) => {
      const found = await originalGet(key, now);
      if (found?.record) lookups.push(found.record);
      return found;
    });

    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      // residentBytes 0 settles the hydrated base to disk-only, so the next
      // real delta has to load whatever checkpoint the worker finds.
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      const initial = writes.filter((write) => write.ref === ref).at(-1);
      if (!initial) throw new Error("expected the real background hydration to write an initial checkpoint");
      expect(independentJsonBytes(initial.state)).toBeLessThan(32 * 1024 * 1024);

      // Replace the checkpoint's body through the real adapter, keeping its
      // captured key/certificate. The valid body independently measures over
      // the product's 32 MiB base bound while the whole record still fits the
      // injected 64 MiB cache cap, so the adapter admits the write itself.
      const oversizedState = JSON.parse(JSON.stringify(initial.state)) as CurrentSessionRecord["state"];
      const overlayKey = "stream:s1:agentMessage";
      const overlayEntry = oversizedState.overlay[overlayKey];
      if (!overlayEntry) throw new Error(`expected the captured overlay item ${overlayKey}`);
      oversizedState.overlay[overlayKey] = {
        ...overlayEntry,
        item: { ...overlayEntry.item, text: "z".repeat(34 * 1024 * 1024 + 4) } as typeof overlayEntry.item,
      };
      const oversizedRecord: CurrentSessionRecord = { ...initial, savedAt: Date.now(), state: oversizedState };
      expect(independentJsonBytes(oversizedState)).toBeGreaterThan(32 * 1024 * 1024);
      expect(independentJsonBytes(oversizedRecord)).toBeLessThan(maxBytes);
      const oversizedWrite = await localCache.putCurrent(oversizedRecord, Date.now());
      expect(oversizedWrite.outcome).toBe("written");

      // The uniquely marked stale delta sends the worker to the real cache hit
      // and parks the ensuing authoritative read in its handler. At the hold
      // the oversized base was rejected before folding: the retired frame's
      // queue entry and charge are gone, the ref stays repairing, and nothing
      // oversized or stale published.
      const staleMarker = "-stale-oversized";
      const readsBeforeStale = fake.calls.filter((call) => call.method === "thread/read").length;
      const hydrationsBeforeStale = threadsStore.getState().hydrations.get(ref) ?? 0;
      holdReads = true;
      fake.emitNotification(textDelta(ref, staleMarker));
      await readIssued.promise;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforeStale + 1);
      expect(lookups.some((record) => independentJsonBytes(record.state) > 32 * 1024 * 1024)).toBe(true);
      const held = backgroundTranscriptSnapshotForTests();
      const heldEntry = held.refs.get(ref);
      expect(heldEntry?.persistence).toBe("repairing");
      expect(held.pendingBytes).toBe(0);
      expect(heldEntry?.residentBytes).toBe(0);
      expect(heldEntry?.pending).toHaveLength(0);
      expect(heldEntry?.inFlight).toHaveLength(0);
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      expect(JSON.stringify(threadsStore.getState().threads.get(ref) ?? null)).not.toContain(staleMarker);

      // A small authoritative response, without releasing or reacquiring the
      // lease, republishes current state and restores a usable checkpoint.
      releaseRead.resolve();
      await waitForHydration(ref, hydrationsBeforeStale + 1);
      await settleBackgroundTranscriptsForTests();
      const recoveredEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect(recoveredEntry?.persistence).toBe("enabled");
      expect(recoveredEntry?.current).toBe(true);
      const recovered = await localCache.getCurrent([initial.ownerToken, ref], Date.now());
      if (!recovered?.record) throw new Error("expected the repaired ref to have a current checkpoint");
      expect(independentJsonBytes(recovered.record.state)).toBeLessThan(32 * 1024 * 1024);
      expect(JSON.stringify(recovered.record.state)).toContain("alpha");
      expect(JSON.stringify(recovered.record.state)).not.toContain(staleMarker);

      // A fresh delta settles exactly once, and reopening locally repaints it
      // from the checkpoint with no activation read.
      const freshMarker = "-fresh-oversized";
      fake.emitNotification(textDelta(ref, freshMarker));
      await settleBackgroundTranscriptsForTests();
      const readsBeforeOpen = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      const opened = threadsStore.getState().threads.get(ref);
      if (!opened) throw new Error("expected the recovered ref to reopen from its local checkpoint");
      const displayText = opened.turns
        .flatMap((turn) => turn.items)
        .map((item) => item.text ?? "")
        .join("");
      expect(displayText).toContain(freshMarker);
      expect(displayText.split(freshMarker)).toHaveLength(2);
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforeOpen);
    } finally {
      releaseRead.resolve();
      holdReads = false;
      put.mockRestore();
      get.mockRestore();
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
      localCache.close();
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
