import "fake-indexeddb/auto";
import { AppwireClient } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  threadsStore,
} from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `background-scheduling-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});

// Ordinary final retirement performs a fresh read. Respond only in finally,
// after all original occupancy, cancellation and fairness observations.
function answerFinalReads(socket: FakeSocket): void {
  const send = socket.send.bind(socket);
  vi.spyOn(socket, "send").mockImplementation((data) => {
    send(data);
    const frame = JSON.parse(data) as {
      id?: number;
      method?: string;
      params?: { ref?: string; requestGeneration?: number };
    };
    if (frame.method === "thread/read" && typeof frame.id === "number") {
      queueMicrotask(() =>
        socket.receive({
          id: frame.id,
          result: {
            ...currentRead(frame.params?.ref ?? "local:one"),
            requestGeneration: frame.params?.requestGeneration,
          },
        }),
      );
    }
  });
}

function threadReadRequests(socket: FakeSocket): Array<{ id: number; ref: string; requestGeneration?: number }> {
  return socket.sent
    .map(
      (frame) =>
        JSON.parse(frame) as { id?: number; method?: string; params?: { ref?: string; requestGeneration?: number } },
    )
    .filter(
      (frame): frame is { id: number; method: string; params: { ref: string; requestGeneration?: number } } =>
        frame.method === "thread/read" && typeof frame.id === "number" && typeof frame.params?.ref === "string",
    )
    .map(({ id, params }) => ({ id, ref: params.ref, requestGeneration: params.requestGeneration }));
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

describe("background thread claims", () => {
  test("last owner release cancels a queued real-client read before it can issue", async () => {
    // Keep IndexedDB task scheduling real while faking the protocol clock.
    vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
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
      socket.receive({
        id: firstRequest!.id,
        result: { ...currentRead(firstRequest!.ref), requestGeneration: firstRequest!.requestGeneration },
      });
      await one.ready;
      expect(threadReadRequests(socket).map(({ ref }) => ref)).toEqual(["local:one", "local:two"]);
      expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1);
    } finally {
      answerFinalReads(socket);
      for (const request of threadReadRequests(socket)) {
        socket.receive({
          id: request.id,
          result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
        });
      }
      await Promise.all([one.release(), two.release(), third?.release()]);
      client.close();
      vi.useRealTimers();
    }
  });

  test("issued reads retain both client slots through the deadline, then healthy work proceeds", async () => {
    // Keep IndexedDB task scheduling real while faking the protocol clock.
    vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
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
        socket.receive({
          id: abandoned.id,
          result: { ...currentRead(abandoned.ref), requestGeneration: abandoned.requestGeneration },
        });
      }
      expect(requests.active.size).toBe(1);
      expect(threadsStore.getState().threads.has("local:deadline-one")).toBe(false);
      expect(threadsStore.getState().threads.has("local:deadline-two")).toBe(false);

      const healthyRead = threadReadRequests(socket)[2]!;
      socket.receive({
        id: healthyRead.id,
        result: { ...currentRead(healthyRead.ref), requestGeneration: healthyRead.requestGeneration },
      });
      await healthy.ready;
      expect(requests.settlements).toContainEqual({ ref: "local:deadline-healthy", outcome: "response" });
      expect(requests.active.size).toBe(0);
      expect(requests.maximumActive()).toBeLessThanOrEqual(2);
    } finally {
      answerFinalReads(socket);
      requests.restore();
      for (const request of threadReadRequests(socket)) {
        socket.receive({
          id: request.id,
          result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
        });
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
            socket.receive({
              id: request.id,
              result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
            });
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
        socket.receive({
          id: retiredRequests[0]!.id,
          result: { ...currentRead(retiredRequests[0]!.ref), requestGeneration: retiredRequests[0]!.requestGeneration },
        });
        await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(expectedStarts + 1));
        expect(requests.active.size).toBe(2);
        socket.receive({
          id: retiredRequests[1]!.id,
          result: { ...currentRead(retiredRequests[1]!.ref), requestGeneration: retiredRequests[1]!.requestGeneration },
        });
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
      answerFinalReads(socket);
      requests.restore();
      for (const request of threadReadRequests(socket)) {
        socket.receive({
          id: request.id,
          result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
        });
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
      // Main's subscription change lets a fresh acquisition supersede the
      // departed holder's pending membership without waiting for its response.
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      const retiredRead = { ...currentRead(first.ref), requestGeneration: first.requestGeneration };
      retiredRead.overlay![0]!.item.text = "retired snapshot";
      socket.receive({ id: first.id, result: retiredRead });
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1));
      const second = threadReadRequests(socket)[1]!;
      expect(second.ref).toBe(first.ref);
      expect(threadsStore.getState().threads.has(first.ref)).toBe(false);
      const replacementRead = { ...currentRead(second.ref), requestGeneration: second.requestGeneration };
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
      answerFinalReads(socket);
      for (const request of threadReadRequests(socket)) {
        socket.receive({
          id: request.id,
          result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
        });
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
      socket.receive({
        id: request.id,
        result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
      });
      await second.ready;
      expect(threadsStore.getState().threads.has("local:shared-read")).toBe(true);
      expect(requests.settlements).toEqual([{ ref: "local:shared-read", outcome: "response" }]);
      expect(threadReadRequests(socket)).toHaveLength(1);
    } finally {
      answerFinalReads(socket);
      requests.restore();
      for (const request of threadReadRequests(socket)) {
        socket.receive({
          id: request.id,
          result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
        });
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
      socket.receive({
        id: occupyingRead.id,
        result: { ...currentRead(occupyingRead.ref), requestGeneration: occupyingRead.requestGeneration },
      });
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
        socket.receive({
          id: request.id,
          result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
        });
      }
      await Promise.all([healthy.ready, nextHealthy.ready]);
      expect(requests.settlements).toContainEqual({ ref: "local:retry-healthy", outcome: "response" });
      expect(requests.settlements).toContainEqual({ ref: "local:retry-next-healthy", outcome: "response" });
    } finally {
      answerFinalReads(socket);
      requests.restore();
      for (const request of threadReadRequests(socket)) {
        socket.receive({
          id: request.id,
          result: { ...currentRead(request.ref), requestGeneration: request.requestGeneration },
        });
      }
      await Promise.all(leases.map((lease) => lease.release()));
      client.close();
      restoreRetryScheduler();
    }
  });
});
