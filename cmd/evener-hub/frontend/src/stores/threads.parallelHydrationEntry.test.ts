import type { MethodName, MethodTypes, ThreadReadResponse } from "@evener/appwire-client";
import { acquireThreadSubscription, RequestTimeoutError, type ThreadSubscriptionLease } from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { type TestThreadOverrides, testThread } from "../threadTestUtils";
import { connectionStore } from "./connection";
import { productionEntryTranscriptForTests, setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import { installHydrationRetrySchedulerForTests, resetThreadsStoreForTests, threadsStore } from "./threads";

const establishedMemberships: ThreadSubscriptionLease[] = [];
async function establishMembership(fake: FakeClient, ref = "ref_a"): Promise<void> {
  fake.on("thread/read", () => readResponse(ref));
  const lease = acquireThreadSubscription(fake, ref);
  establishedMemberships.push(lease);
  await lease.ensure();
  fake.calls.length = 0;
}

// Ordinary storage lookups use IndexedDB tasks. Yield to those tasks while
// retaining each original wait's bounded predicate and observation order.
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await nextMacrotask();
}

function nextHandledRequest<M extends MethodName>(
  fake: FakeClient,
  method: M,
  handler: RequestHandler<M>,
): Promise<MethodTypes[M]["params"]> {
  return new Promise((resolve) => {
    fake.on(method, (params) => {
      resolve(params);
      return handler(params);
    });
  });
}

const settleCallerContinuations = nextMacrotask;

function readResponse(ref: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

interface ScheduledHydrationRetry {
  attempt: number;
  retry: () => void;
  cancelled: boolean;
}

let scheduledHydrationRetries: ScheduledHydrationRetry[] = [];
let restoreHydrationRetryScheduler: (() => void) | null = null;

// runScheduledHydrationRetry invokes exactly one scheduled retry, proving first
// that it exists and was not cancelled - a cancelled entry that still fires
// would make every "release/swap cancels the retry" assertion below vacuous.
function runScheduledHydrationRetry(index = 0): void {
  const scheduled = scheduledHydrationRetries[index];
  expect(scheduled, `no hydration retry scheduled at index ${index}`).toBeDefined();
  expect(scheduled?.cancelled).toBe(false);
  scheduled?.retry();
}

let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetWorkspaceStoreForTests();
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `parallel-hydration-entry-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  scheduledHydrationRetries = [];
  restoreHydrationRetryScheduler = installHydrationRetrySchedulerForTests((attempt, retry) => {
    const scheduled: ScheduledHydrationRetry = { attempt, retry, cancelled: false };
    scheduledHydrationRetries.push(scheduled);
    return () => {
      scheduled.cancelled = true;
    };
  });
});
afterEach(() => {
  for (const lease of establishedMemberships.splice(0)) lease.release();
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("useThreadsStore.ensureThread", () => {
  test("hydrates via thread/read and routes a subsequent matching notification through the reducer", async () => {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", (params) => {
      expect(params).toEqual({
        ref: "ref_a",
        includeTurns: true,
        itemsView: "full",
        subscribe: true,
        replaceSubscription: false,
        itemLimit: 40,
      });
      return readResponse("ref_a");
    });

    await threadsStore.getState().ensureThread("ref_a");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.threadId).toBe("thr_ref_a");

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("a second ensureThread(ref) does not re-read", async () => {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_a");

    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  });

  test("concurrent ensureThread(ref) calls share one thread/read", async () => {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    const p1 = threadsStore.getState().ensureThread("ref_a");
    const p2 = threadsStore.getState().ensureThread("ref_a");
    await Promise.all([p1, p2]);

    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
  });

  test("restarts a pending initial hydrate on a client swap and waits for the new client's model", async () => {
    const a = connectReadyFakeClient();
    const aRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    a.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (aRead.resolve = resolve)));

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => aRead.resolve !== null);

    const b = new FakeClient("connecting");
    const bRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    b.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (bRead.resolve = resolve)));
    connectionStore.getState().connect(b);
    b.emitReady();
    await flushUntil(() => bRead.resolve !== null);

    let settled = false;
    void ensuring.then(() => {
      settled = true;
    });
    aRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_a", status: "completed", itemsView: "full", items: [] }] }),
    );
    await flushUntil(() => settled);

    expect(settled).toBe(false);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    bRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }),
    );
    await ensuring;

    expect(a.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(b.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b");

    b.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
    a.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "idle" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });

    b.on("thread/read", () => readResponse("ref_a"));
    threadsStore.getState().releaseThread("ref_a");
    // R36, ordinary final ownership retains the model at this same boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  test("late A rejection cannot remove or reject B's newer pending initial hydrate", async () => {
    const a = connectReadyFakeClient();
    const aRead: { reject: ((error: Error) => void) | null } = { reject: null };
    a.on("thread/read", () => new Promise<ThreadReadResponse>((_, reject) => (aRead.reject = reject)));

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => aRead.reject !== null);

    const b = new FakeClient("ready");
    const bRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    b.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (bRead.resolve = resolve)));
    connectionStore.getState().connect(b);
    await flushUntil(() => bRead.resolve !== null);

    aRead.reject?.(new Error("late A rejection"));
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    bRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }),
    );
    await ensuring;

    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    threadsStore.getState().releaseThread("ref_a");
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().ensureThread("ref_a")).rejects.toThrow(/no client connected/i);
  });

  test("a repeatedly failing read keeps the ref untracked and re-arms one retry per attempt", async () => {
    const fake = connectReadyFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      throw new Error("boom");
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    // Each failure re-arms exactly one retry rather than stacking them, and the
    // attempt number the scheduler is asked to pace advances with it.
    runScheduledHydrationRetry(0);
    await flushUntil(() => scheduledHydrationRetries.length === 2);
    expect(readAttempts).toBe(2);
    expect(scheduledHydrationRetries.map((scheduled) => scheduled.attempt)).toEqual([1, 2]);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    // Release is what ends it, not a failure count.
    threadsStore.getState().releaseThread("ref_a");
    await ensuring;
    expect(scheduledHydrationRetries[1]?.cancelled).toBe(true);
    expect(readAttempts).toBe(2);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("a failed read never doubles the pane's claim: retry-success + a single release fully untracks the ref", async () => {
    const fake = connectReadyFakeClient();
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) throw new Error("boom");
      return readResponse("ref_a");
    });

    // One logical pane, ONE ensureThread call across the failure and the retry:
    // the failed attempt keeps the single claim it made instead of dropping it
    // and requiring the caller to claim again.
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    runScheduledHydrationRetry();
    await ensuring;
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    expect(readCount).toBe(2); // one failed read, one successful read — no stale inflight sharing across attempts

    const repository = productionEntryTranscriptForTests("ref_a").repository;
    expect(repository).toBeDefined();
    if (!repository) throw new Error("ordinary acquisition published no repository");
    const retire = vi.spyOn(repository, "retire");
    // One natural release starts retirement and eventually fully untracks it.
    // A leaked second pane claim would prevent both observations.
    threadsStore.getState().releaseThread("ref_a");
    // R36, ordinary final ownership retains the model at this same boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));

    expect(retire).toHaveBeenCalledTimes(1);
    // The original minimal read fixture has no snapshot authority, so its
    // terminal read retires stale. This case proves claims, not persistence.
    expect(await retire.mock.results[0]?.value).toEqual({ kind: "stale" });
    expect(readCount).toBe(3);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);
    expect(fake.calls[2]?.params).toMatchObject({ ref: "ref_a", subscribe: false, requestGeneration: 1 });
    // Further evidence refCounts (module-private, unreachable directly from
    // this test) reads back at true zero rather than negative or stale: a
    // brand new cycle behaves exactly as if the ref had never been touched
    // before — one fresh read, one release fully untracks it again.
    await threadsStore.getState().ensureThread("ref_a");
    // One failed read, one successful retry, one distinct terminal read,
    // then this cycle's fresh initial read. The terminal owner is observed above.
    expect(readCount).toBe(4);
    expect(fake.calls.filter((call) => call.method === "thread/read")[3]?.params).toEqual({
      ref: "ref_a",
      includeTurns: true,
      itemsView: "full",
      subscribe: true,
      replaceSubscription: false,
      itemLimit: 40,
      requestGeneration: 1,
    });
    threadsStore.getState().releaseThread("ref_a");
    // R36, ordinary final ownership retains the model at this same boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  test("an owner joining a shared read mid-flight shares its one retry and keeps its own claim", async () => {
    const fake = connectReadyFakeClient();
    let readCount = 0;
    const box: { rejectRead: ((error: Error) => void) | null } = { rejectRead: null };
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) {
        return new Promise<ThreadReadResponse>((_, reject) => {
          box.rejectRead = reject;
        });
      }
      return readResponse("ref_a", {
        turns: [{ id: "turn_retry", status: "completed", itemsView: "full", items: [] }],
      });
    });

    const firstEnsure = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => box.rejectRead !== null);
    const secondEnsure = threadsStore.getState().ensureThread("ref_a");
    expect(readCount).toBe(1);

    box.rejectRead?.(new Error("shared read failure"));
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    expect(scheduledHydrationRetries).toHaveLength(1);

    runScheduledHydrationRetry();
    await Promise.all([firstEnsure, secondEnsure]);
    expect(readCount).toBe(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_retry");

    // Both claims survived the shared failure, so both have to be released.
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    threadsStore.getState().releaseThread("ref_a");
    // R36, ordinary final ownership retains the model at this same boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  test("a ref released before its in-flight hydrate resolves is not resurrected", async () => {
    const fake = connectReadyFakeClient();
    // A plain `let` reassigned only inside the executor closure below gets
    // narrowed to `never` at the use site by TS's control-flow analysis
    // (a variable mutated solely inside a nested function isn't tracked as
    // "possibly non-null" outside it); a boxed field sidesteps that.
    const box: { resolveRead: ((resp: ThreadReadResponse) => void) | null } = { resolveRead: null };
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (box.resolveRead = resolve)));

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    // request()'s handler invocation (which captures the resolver) is
    // deferred a microtask behind the synchronous call above; wait for it
    // before racing releaseThread() against the still-pending hydrate.
    await flushUntil(() => box.resolveRead !== null);
    threadsStore.getState().releaseThread("ref_a");
    box.resolveRead?.(readResponse("ref_a"));
    await ensuring;

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test.each([true, false])(
    "a released read cannot resume on a replacement connection (release first: %s)",
    async (releaseFirst) => {
      const original = connectReadyFakeClient();
      let rejectRead: ((error: Error) => void) | undefined;
      const started = nextHandledRequest(
        original,
        "thread/read",
        () =>
          new Promise<ThreadReadResponse>((_resolve, reject) => {
            rejectRead = reject;
          }),
      );
      const ensuring = threadsStore.getState().ensureThread("ref_a");
      await started;
      if (releaseFirst) threadsStore.getState().releaseThread("ref_a");

      const replacement = new FakeClient("connecting");
      replacement.on("thread/read", () => readResponse("ref_a"));
      connectionStore.getState().connect(replacement);
      rejectRead?.(new Error("old connection failed"));
      if (!releaseFirst) {
        await settleCallerContinuations();
        threadsStore.getState().releaseThread("ref_a");
      }
      replacement.emitReady();
      await ensuring;

      expect(replacement.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
      expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    },
  );

  test("a released retry owner cannot resume after replacement readiness", async () => {
    const original = connectReadyFakeClient();
    const started = nextHandledRequest(original, "thread/read", () => {
      throw new RequestTimeoutError("initial read failed");
    });
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await started;
    await settleCallerContinuations();

    const replacement = new FakeClient("connecting");
    replacement.on("thread/read", () => readResponse("ref_a"));
    connectionStore.getState().connect(replacement);
    await settleCallerContinuations();
    threadsStore.getState().releaseThread("ref_a");
    replacement.emitReady();
    await ensuring;

    expect(replacement.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("last release retires a pending hydrate before an immediate re-ensure starts a new lifecycle", async () => {
    const fake = connectReadyFakeClient();
    await establishMembership(fake);
    const reads: Array<(response: ThreadReadResponse) => void> = [];
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => reads.push(resolve)));

    const firstEnsure = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 1);
    let firstSettled = false;
    void firstEnsure.then(
      () => {
        firstSettled = true;
      },
      () => {
        firstSettled = true;
      },
    );

    threadsStore.getState().releaseThread("ref_a");

    const secondEnsure = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 2);
    expect(reads).toHaveLength(2);

    reads[0]!(readResponse("ref_a", { turns: [{ id: "turn_a", status: "completed", itemsView: "full", items: [] }] }));
    await Promise.resolve();
    await Promise.resolve();
    expect(firstSettled).toBe(false);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    reads[1]!(readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }));
    await Promise.all([firstEnsure, secondEnsure]);

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b");

    fake.on("thread/read", () => readResponse("ref_a"));
    threadsStore.getState().releaseThread("ref_a");
    // R36, ordinary final ownership retains the model at this same boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  test("a retired ensure rejection does not consume a replacement lifecycle claim", async () => {
    const fake = connectReadyFakeClient();
    await establishMembership(fake);
    const reads: Array<{
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: Error) => void;
    }> = [];
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve, reject) => {
          reads.push({ resolve, reject });
        }),
    );

    const firstEnsure = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 1);

    threadsStore.getState().releaseThread("ref_a");

    const secondEnsure = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 2);

    let firstRejected = false;
    void firstEnsure.then(
      () => undefined,
      () => {
        firstRejected = true;
      },
    );
    reads[0]!.reject(new Error("retired read A"));
    await flushUntil(() => firstRejected);

    reads[1]!.resolve(
      readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }),
    );
    await secondEnsure;

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b");

    fake.on("thread/read", () => readResponse("ref_a"));
    threadsStore.getState().releaseThread("ref_a");
    // R36, ordinary final ownership retains the model at this same boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });
});
