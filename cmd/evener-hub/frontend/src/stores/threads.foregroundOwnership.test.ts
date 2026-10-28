import { type TestThreadOverrides, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import { RequestTimeoutError, type ThreadModel, type ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { connectionStore } from "./connection";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setProductionEntryCacheForTests,
  threadsStore,
} from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `foreground-ownership-${crypto.randomUUID()}`,
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
  resetThreadsStoreForTests();
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  cache.close();
  vi.restoreAllMocks();
});
const connectFakeClient = () => connectReadyFakeClient((fake) => fake.on("thread/unsubscribe", () => ({})));

async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
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

describe("useThreadsStore.ensureThread", () => {
  test("same-ready initial read failure retries while the pane still owns the ref", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) throw new RequestTimeoutError("thread/read timed out");
      return readResponse("ref_a", {
        turns: [{ id: "turn_retry", status: "completed", itemsView: "full", items: [] }],
      });
    });

    const publishedModels: ThreadModel[] = [];
    const unsubscribe = threadsStore.subscribe((state, previous) => {
      const model = state.threads.get("ref_a");
      if (model && model !== previous.threads.get("ref_a")) publishedModels.push(model);
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    // The ordinary empty IndexedDB lookup settles on real tasks, not only microtasks.
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(1));
    // Drain the rejection all the way through the caller before the retry
    // fires, so this covers an owner already WAITING on its lifecycle rather
    // than one that happens to find a replacement read already in flight.
    await settleCallerContinuations();

    // Nothing below emits ready, focuses the window, remounts a pane, or swaps
    // the client: the retry is the store's own, scheduled by the failed read.
    expect(readAttempts).toBe(1);
    expect(scheduledHydrationRetries).toHaveLength(1);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    runScheduledHydrationRetry();
    await ensuring;
    unsubscribe();

    expect(readAttempts).toBe(2);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    expect(scheduledHydrationRetries).toHaveLength(1);
    expect(publishedModels).toHaveLength(1);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_retry");

    // One claim, one release: the failed attempt never rolled the claim back,
    // so a single release still fully untracks the ref.
    threadsStore.getState().releaseThread("ref_a");
    // The ordinary final owner keeps the model until its fresh finite end read.
    // R36 supersedes only the legacy synchronous-removal portion at this point.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  // Owned hydration (the retry lifecycle). A read that fails while the socket
  // is still ready is a transport failure, not a lost claim: the pane keeps the
  // ref and the store schedules the next read itself. Every case below drives
  // the injected scheduler by hand and asserts request counts and map identity,
  // never elapsed time.
  test("same-ready refresh failure preserves stale model until retry succeeds", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", (params) => {
      readAttempts += 1;
      const authority = {
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 0 },
        requestGeneration: params.requestGeneration ?? 1,
      };
      if (readAttempts === 1) {
        return {
          ...readResponse("ref_a", { turns: [{ id: "turn_a", status: "completed", itemsView: "full", items: [] }] }),
          ...authority,
        };
      }
      if (readAttempts === 2) throw new RequestTimeoutError("refresh read timed out");
      return {
        ...readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }),
        ...authority,
      };
    });

    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_a");

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(1));

    // Stale beats blank: the failed refresh leaves version A published.
    expect(readAttempts).toBe(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_a");

    runScheduledHydrationRetry();
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_b");
    await vi.waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b"));

    expect(readAttempts).toBe(3);
    expect(
      fake.calls
        .filter((call) => call.method === "thread/read")
        .map((call) => (call.params as { requestGeneration?: number }).requestGeneration),
    ).toEqual([undefined, 2, 3]);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b");
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  test("release cancels scheduled hydration retry and late response cannot resurrect", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    const late: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts <= 2) throw new RequestTimeoutError("thread/read timed out");
      return new Promise<ThreadReadResponse>((resolve) => {
        late.resolve = resolve;
      });
    });

    // Phase 1: release while a retry is scheduled. The scheduler's own cancel
    // must run, and firing the cancelled callback anyway must reach no wire.
    const firstEnsure = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(1));
    threadsStore.getState().releaseThread("ref_a");
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);

    scheduledHydrationRetries[0]?.retry();
    await firstEnsure;
    expect(readAttempts).toBe(1);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    // Phase 2: release while the retry's own read is still in flight. Its late
    // response belongs to a retired generation and must not resurrect the ref.
    const secondEnsure = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 2);
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(2));
    runScheduledHydrationRetry(1);
    await flushUntil(() => late.resolve !== null);
    await vi.waitFor(() => expect(late.resolve).not.toBeNull());
    expect(readAttempts).toBe(3);

    threadsStore.getState().releaseThread("ref_a");
    late.resolve?.(readResponse("ref_a", { turns: [{ id: "turn_late", status: "completed", itemsView: "full" }] }));
    await secondEnsure;

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);
  });

  test("client swap fences an old client's scheduled retry and response", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () => {
      if (a.calls.filter((call) => call.method === "thread/read").length === 1) {
        throw new RequestTimeoutError("client A read timed out");
      }
      // Only reachable if the stale retry escapes its client fence; this
      // response would overwrite client B's authoritative model.
      return readResponse("ref_a", { turns: [{ id: "turn_a", status: "completed", itemsView: "full", items: [] }] });
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(1));
    expect(a.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);

    const b = new FakeClient("ready");
    const bRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    b.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (bRead.resolve = resolve)));
    connectionStore.getState().connect(b);
    await flushUntil(() => bRead.resolve !== null);
    await vi.waitFor(() => expect(bRead.resolve).not.toBeNull());

    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);
    scheduledHydrationRetries[0]?.retry();
    await flushUntil(() => a.calls.filter((call) => call.method === "thread/read").length === 2);
    expect(a.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);

    bRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }),
    );
    await ensuring;

    expect(b.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b");
    // All original wire-count and replacement-model observations are above.
    // The new finite final read gets a healthy response, not the earlier held one.
    b.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }),
    );
    b.on("thread/unsubscribe", () => ({}));
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  test("concurrent owners share one retrying read lifecycle", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) throw new RequestTimeoutError("thread/read timed out");
      return readResponse("ref_a", {
        turns: [{ id: "turn_shared", status: "completed", itemsView: "full", items: [] }],
      });
    });

    const publishedModels: ThreadModel[] = [];
    const unsubscribe = threadsStore.subscribe((state, previous) => {
      const model = state.threads.get("ref_a");
      if (model && model !== previous.threads.get("ref_a")) publishedModels.push(model);
    });

    const firstOwner = threadsStore.getState().ensureThread("ref_a");
    const secondOwner = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(1));
    // Both owners must be waiting on the one lifecycle before it produces a
    // read - see the initial-hydration case above for why this drain matters.
    await settleCallerContinuations();

    // Two owners, one failed read, one scheduled retry.
    expect(readAttempts).toBe(1);
    expect(scheduledHydrationRetries).toHaveLength(1);

    runScheduledHydrationRetry();
    await Promise.all([firstOwner, secondOwner]);
    unsubscribe();

    expect(readAttempts).toBe(2);
    expect(scheduledHydrationRetries).toHaveLength(1);
    expect(publishedModels).toHaveLength(1);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_shared");

    // Both claims survived the shared failure, so the ref stays tracked until
    // both owners release.
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  // The headline scenario: a pane whose first read failed converges when the
  // connection comes back, with nothing driving the retry by hand.
  //
  // A reconnect does NOT re-read this ref. The failed attempt deleted its own
  // pending entry, no model was ever published, and nothing pinned it, so the
  // ref is in none of the three sets handleReady fans out over. Retiring the
  // owned lifecycles on the ready-epoch bump is the whole of the convergence
  // here: it settles the parked owner, which then re-arms against the new
  // generation itself.
  test("a reconnect converges a pane whose first read failed, with no retry fired by hand", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) throw new RequestTimeoutError("thread/read timed out");
      return readResponse("ref_a", {
        turns: [{ id: "turn_reconnected", status: "completed", itemsView: "full", items: [] }],
      });
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(1));
    // The owner must be parked on its lifecycle before the reconnect, or this
    // covers the adopt-a-replacement arm instead of the wait this test names.
    await settleCallerContinuations();
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await ensuring;

    // Nothing here fired the scheduled retry; the reconnect cancelled it.
    expect(scheduledHydrationRetries).toHaveLength(1);
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);
    expect(readAttempts).toBe(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_reconnected");
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  // The fire-time fence, on its own. Every other reason a retry must stand down
  // is decided when it is ARMED; the only thing left to check when it fires is
  // that the lifecycle it was armed for is still the live one. This case puts
  // every downstream guard in the permissive state - the pane still owns the
  // ref, the client and its socket are the same object, nothing is in flight -
  // so a retired record is the only thing standing between a superseded ready
  // generation's callback and a read aimed at a dead epoch.
  test("a retired lifecycle's fired retry reaches no wire while its ref is still owned", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) throw new RequestTimeoutError("thread/read timed out");
      return readResponse("ref_a", {
        turns: [{ id: "turn_reconnected", status: "completed", itemsView: "full", items: [] }],
      });
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(1));
    await settleCallerContinuations();

    // The reconnect retires the lifecycle and converges the pane. In production
    // the retire's clearTimeout ends the story; the injected scheduler only
    // marks the entry cancelled, so firing it below stands in for a timer that
    // real clearTimeout would never have let run.
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await ensuring;
    expect(readAttempts).toBe(2);
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_reconnected");

    scheduledHydrationRetries[0]?.retry();
    await settleCallerContinuations();

    expect(readAttempts).toBe(2);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_reconnected");

    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  // ensureThread's client-swap re-arm waits for the replacement client to reach
  // ready. Anything captured before that wait can be a whole ready generation
  // out of date when it resumes, and a client captured there is the one thing a
  // hydration can carry that its own ready-epoch stamp will not show: the epoch
  // is read fresh at capture, so a read issued to a superseded client arrives
  // labelled with the live generation. The read has to go to the client that is
  // wired now, not to the one that was wired when the wait began.
  test("a re-arm that waited for readiness reads from the client wired now", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () => {
      throw new RequestTimeoutError("client A read timed out");
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    void ensuring.catch(() => undefined);
    await flushUntil(() => a.calls.some((call) => call.method === "thread/read"));
    await vi.waitFor(() => expect(a.calls.some((call) => call.method === "thread/read")).toBe(true));

    // B is still connecting, so the re-arm parks waiting for it.
    const b = new FakeClient("connecting");
    b.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }),
    );
    connectionStore.getState().connect(b);
    await settleCallerContinuations();

    // B reaches ready and C replaces it before the parked caller runs again:
    // waking it resolves a microtask later, and this swap is synchronous.
    b.emitReady();
    const c = new FakeClient("ready");
    c.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_c", status: "completed", itemsView: "full", items: [] }] }),
    );
    connectionStore.getState().connect(c);
    await ensuring;

    expect(b.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
    expect(c.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_c");
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  // A read that fails on a client the store has already replaced must arm
  // nothing. The doomed retry would take the one retry slot the CURRENT
  // generation's lifecycle has, and the next genuine failure on the live
  // client would then find it occupied and schedule nothing at all.
  test("a superseded client's failure arms no retry on the live lifecycle", async () => {
    const a = connectFakeClient();
    const aRead: { reject: ((error: unknown) => void) | null } = { reject: null };
    a.on("thread/read", () => new Promise<ThreadReadResponse>((_, reject) => (aRead.reject = reject)));

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => aRead.reject !== null);
    await vi.waitFor(() => expect(aRead.reject).not.toBeNull());

    // B is not ready yet, so the swap re-reads nothing and client A's attempt
    // is still the current pending one at the moment it fails - which is what
    // puts the failure past the attempt-identity check and onto this fence.
    const b = new FakeClient("connecting");
    b.on("thread/read", (params) =>
      readResponse((params as { ref: string }).ref, {
        turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    connectionStore.getState().connect(b);

    aRead.reject?.(new RequestTimeoutError("client A read timed out"));
    await flushUntil(() => scheduledHydrationRetries.length > 0);
    expect(scheduledHydrationRetries).toHaveLength(0);

    b.emitReady();
    await ensuring;
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b");
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  // A socket that has dropped is not this lifecycle's to pace: that client
  // generation's own next ready transition re-reads what it tracks. Arming a
  // retry here would aim a read at a dead wire and occupy the lifecycle's one
  // retry slot while doing it.
  test("a failure on a client that is no longer ready arms no retry", async () => {
    const fake = connectFakeClient();
    const firstRead: { reject: ((error: unknown) => void) | null } = { reject: null };
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) return new Promise<ThreadReadResponse>((_, reject) => (firstRead.reject = reject));
      return readResponse("ref_a", {
        turns: [{ id: "turn_after_ready", status: "completed", itemsView: "full", items: [] }],
      });
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => firstRead.reject !== null);
    await vi.waitFor(() => expect(firstRead.reject).not.toBeNull());

    // The socket drops with no ready epoch change - a transition INTO ready is
    // the only thing that bumps it - so client identity and epoch both still
    // match and this fence is the only one left standing.
    fake.emitStateChange("reconnecting");
    firstRead.reject?.(new RequestTimeoutError("read timed out on a dropped socket"));
    await flushUntil(() => scheduledHydrationRetries.length > 0);
    expect(scheduledHydrationRetries).toHaveLength(0);

    fake.emitReady();
    await ensuring;
    expect(readAttempts).toBe(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_after_ready");
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  // A targeted resync re-reads one ref on the SAME client and ready epoch, so
  // none of the fire-time identity fences applies to it. The scheduled retry
  // must still stand down: the attempt already on the wire owns the next
  // outcome, including arming the retry after it.
  test("a scheduled retry stands down for an attempt a resync already put on the wire", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    const resyncRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) throw new RequestTimeoutError("thread/read timed out");
      return new Promise<ThreadReadResponse>((resolve) => (resyncRead.resolve = resolve));
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    await vi.waitFor(() => expect(scheduledHydrationRetries).toHaveLength(1));
    await settleCallerContinuations();

    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushUntil(() => resyncRead.resolve !== null);
    await vi.waitFor(() => expect(resyncRead.resolve).not.toBeNull());
    expect(readAttempts).toBe(2);

    runScheduledHydrationRetry();
    await flushUntil(() => readAttempts === 3);
    expect(readAttempts).toBe(2);

    resyncRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_resync", status: "completed", itemsView: "full", items: [] }] }),
    );
    await ensuring;
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_resync");
    // The resync assertions are complete before this final-read-only responder.
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_resync", status: "completed", itemsView: "full", items: [] }] }),
    );
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner keeps the model at this same release boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });
});
