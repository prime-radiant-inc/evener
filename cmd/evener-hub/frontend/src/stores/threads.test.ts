import { deferred } from "@evener/appwire-client/testing/deferred";
import { appliedReceipt } from "../mutationReceiptTestUtils";
import { connectFakeClient } from "../testing/connectionTestUtils";
import { ALL_CAPABILITIES as CAPABILITIES, type TestThreadOverrides, testThread } from "../threadTestUtils";
import { acquireSessionActivity } from "./sessionActivity";
import { activitySummary } from "./sessionActivityTestUtils";
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  MethodName,
  MethodTypes,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  TurnQueueResponse,
  TurnStartResponse,
} from "@evener/appwire-client";
import {
  AppwireClient,
  acquireThreadSubscription,
  RequestTimeoutError,
  type ThreadSubscriptionLease,
  WireError,
} from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { JobOutputPeer } from "@evener/appwire-client/testing/jobOutputPeer";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { cleanup, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import "./humanNoteDrafts";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";
import {
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  threadsStore,
} from "./threads";

const establishedMemberships: ThreadSubscriptionLease[] = [];
async function establishMembership(fake: FakeClient, ref = "ref_a"): Promise<void> {
  fake.on("thread/read", () => readResponse(ref));
  const lease = acquireThreadSubscription(fake, ref);
  establishedMemberships.push(lease);
  await lease.ensure();
  fake.calls.length = 0;
}

// flushUntil drains microtask turns until `done()` reports true (or a bounded
// number of turns elapse, so a genuine hang fails fast instead of silently).
// Same contract/name as protocol/client.test.ts's helper; duplicated here
// because the two test files share no test-utils module.
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

// settleCallerContinuations yields to the task queue exactly once, which the
// hydration-retry tests use to park an ensureThread/watchThread caller on its
// lifecycle before firing the retry that lifecycle scheduled.
//
// Why a task yield and not `flushUntil(() => false, N)`: a count is an
// assumption about how many microtask turns separate the failed read from the
// caller's own catch, and nothing fails when that assumption stops holding -
// the caller instead converges through the "adopt a replacement read already in
// flight" arm and the test silently stops covering the owner's wait. A task
// yield holds however many turns that path grows (see nextMacrotask).
//
// Its one boundary: it does not cover a future change that parks the caller
// behind a task or I/O of its own (an IndexedDB read on the rejection path,
// say). That would need its own awaited condition, and the mutation proof in
// this task's report - which fires each owner-wait arm and requires the
// matching test to fail - is what would catch it.
const settleCallerContinuations = nextMacrotask;

function readResponse(ref: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

// readResponse derives the wire thread id as thr_<ref>. The routing-index
// tests need models with known and sometimes SHARED thread ids (a lean watch
// of a ref that is also pane-owned resolves to the same thread id), so this
// variant pins the id explicitly.
function _readResponseWithId(ref: string, threadId: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  const base = readResponse(ref, overrides);
  return { thread: { ...base.thread, id: threadId } };
}

function _sameEpochReconnectFixture() {
  const authoritativeSnapshot = readResponse("ref_a", {
    status: { type: "active", activeFlags: ["streaming"] },
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" }],
      },
    ],
    evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
  });
  const completion = {
    method: "history/updated",
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [
        {
          ...{
            type: "commandExecution" as const,
            id: "item_1",
            turnId: "turn_1",
            output: "done",
            status: "completed" as const,
          },
          turnId: "turn_1",
        },
      ],
    },
  } as AnyNotification;
  const turnCompleted = {
    method: "history/updated",
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
    },
  } as AnyNotification;
  return { authoritativeSnapshot, completion, turnCompleted };
}

function connectMutationClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => {
    if (!params.ref) throw new Error("thread/read test request requires ref");
    return readResponse(params.ref);
  });
  connectionStore.getState().connect(fake);
  return fake;
}

async function deleteMutationDatabase(): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase("evener-mutation-outbox");
    request.addEventListener("success", () => resolve(), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
    request.addEventListener("blocked", () => reject(new Error("mutation database deletion blocked")), {
      once: true,
    });
  });
}

// This suite exercises the ordinary entry route (the route production takes
// when no fixture or adapter override is installed). Its storage boundary is a
// fresh real SessionEntryCache over a private fake-IDB database, so no fixture
// can write into a shared database.
let entryCache: SessionEntryCache;

async function flushIndexedDBUntil(done: () => boolean, maxTurns = 30): Promise<void> {
  const probe = new MutationOutboxIndexedDB();
  for (let turn = 0; turn < maxTurns && !done(); turn += 1) await probe.listTargetRefs();
  probe.close();
}

function mutationReceipt(clientMutationId: string, disposition = "applied") {
  return appliedReceipt(clientMutationId, "thr_ref_a", { disposition });
}

// The hydration retry scheduler is injected so every retry in this suite is
// driven by an explicit call, never by elapsed time: the assertions below count
// requests and compare map identity, and no test advances a timer or waits out
// a delay. Installing it for EVERY test also keeps the production backoff's
// real setTimeout out of the suite entirely, so a failing read in an unrelated
// test cannot leave a live timer behind.
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

beforeEach(async () => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  // The store-wide resume-only predicate reads the pending-turns projection
  // (threads.ts's installResumeOnlyProjection), so clear any blockedUnknown
  // rows a prior test left before this one hydrates.
  resetPendingTurnsStoreForTests();
  resetWorkspaceStoreForTests();
  resetSubagentModuleStoreForTests();
  scheduledHydrationRetries = [];
  restoreHydrationRetryScheduler = installHydrationRetrySchedulerForTests((attempt, retry) => {
    const scheduled: ScheduledHydrationRetry = { attempt, retry, cancelled: false };
    scheduledHydrationRetries.push(scheduled);
    return () => {
      scheduled.cancelled = true;
    };
  });
  await deleteMutationDatabase();
  entryCache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    databaseName: `ordinary-threads-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(entryCache);
});

afterEach(() => {
  for (const lease of establishedMemberships.splice(0)) lease.release();
  cleanup();
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  entryCache.close();
  vi.restoreAllMocks();
});

describe("useThreadsStore.ensureThread", () => {
  // The hub's transcriptIndexRebuilding answer (#4343): a daemonless read
  // whose sidecar needs a rebuild answers a typed retryable error while one
  // owned repair runs off the read path. In the store it is deliberately NOT
  // the history-failed discriminant — hydrations land in the paced
  // retry-forever owner, the held history stays, and the per-ref flag the
  // loading surface reads shows the concise repair status until a read that
  // does not fail with the code lands.
  describe("transcript index rebuilding answers", () => {
    const rebuildingRejection = () =>
      new WireError("thread history is being rebuilt in the background", -32000, {
        evenerErrorInfo: "transcriptIndexRebuilding",
      });

    test("a first open arms the owned retry, marks the ref repairing, and clears the flag when a later read succeeds", async () => {
      const fake = connectFakeClient();
      let reads = 0;
      fake.on("thread/read", () => {
        reads += 1;
        if (reads === 1) throw rebuildingRejection();
        return readResponse("ref_a", { name: "Repaired" });
      });

      const ensuring = threadsStore.getState().ensureThread("ref_a");
      await flushUntil(() => scheduledHydrationRetries.length === 1);
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);
      // Nothing was published for a first open — no model, so no surface to
      // carry a history-failed diagnostic either.
      expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

      runScheduledHydrationRetry(0);
      await ensuring;
      expect(threadsStore.getState().threads.get("ref_a")?.name).toBe("Repaired");
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
    });

    test("an already-open session keeps its held model without a history-failed diagnostic while its refresh reports the repair", async () => {
      const fake = connectFakeClient();
      fake.on("thread/read", () => readResponse("ref_a", { name: "Held" }));
      await threadsStore.getState().ensureThread("ref_a");

      fake.on("thread/read", () => {
        throw rebuildingRejection();
      });
      await threadsStore
        .getState()
        .refreshThread("ref_a")
        .catch(() => undefined);
      await flushUntil(() => scheduledHydrationRetries.length === 1);

      // The held model stays exactly as it was, with no failure marker, and
      // the surface flag says what is actually happening.
      expect(threadsStore.getState().threads.get("ref_a")?.name).toBe("Held");
      expect(threadsStore.getState().threads.get("ref_a")?.history?.failed).toBeUndefined();
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      // The repair commits and the paced retry's read succeeds: the flag
      // clears and the re-hydration publishes.
      let reads = 0;
      fake.on("thread/read", () => {
        reads += 1;
        return readResponse("ref_a", { name: "Held" });
      });
      runScheduledHydrationRetry(0);
      await flushUntil(() => reads === 1 && !threadsStore.getState().repairingRefs.has("ref_a"));
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
      expect(threadsStore.getState().threads.get("ref_a")?.history?.failed).toBeUndefined();
    });

    test("a history-failed answer after a rebuilding one clears the flag and applies the diagnostic, arming no further retry", async () => {
      const fake = connectFakeClient();
      fake.on("thread/read", () => readResponse("ref_a", { name: "Held" }));
      await threadsStore.getState().ensureThread("ref_a");

      fake.on("thread/read", () => {
        throw rebuildingRejection();
      });
      await threadsStore
        .getState()
        .refreshThread("ref_a")
        .catch(() => undefined);
      await flushUntil(() => scheduledHydrationRetries.length === 1);
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      // The budget-exhausted answer arrives on the retry: the existing
      // history-failed diagnostic applies, never a false repair status, and
      // the no-auto-retry branch means no new retry is armed.
      fake.on("thread/read", () => {
        throw new WireError("thread history failed at entry 3", -32000, {
          evenerErrorInfo: "transcriptHistoryFailed",
        });
      });
      runScheduledHydrationRetry(0);
      await flushUntil(() => !threadsStore.getState().repairingRefs.has("ref_a"));
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
      // The diagnostic's presence is the contract, not its wording (Copy Is
      // Not a Test Oracle): the failed flag is set, the held content from the
      // first read survives under it, and no further retry is armed.
      expect(threadsStore.getState().threads.get("ref_a")?.history?.failed).toBeDefined();
      expect(threadsStore.getState().threads.get("ref_a")?.name).toBe("Held");
      expect(scheduledHydrationRetries.length).toBe(1);
    });

    test("a superseded read's late success does not clear the repair status the newer read owns", async () => {
      const fake = connectFakeClient();
      fake.on("thread/read", () => readResponse("ref_a", { name: "Open" }));
      await threadsStore.getState().ensureThread("ref_a");

      // Attempt A hangs on the wire.
      const hungRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
      fake.on(
        "thread/read",
        () =>
          new Promise<ThreadReadResponse>((resolve) => {
            hungRead.resolve = resolve;
          }),
      );
      const refreshA = threadsStore
        .getState()
        .refreshThread("ref_a")
        .catch(() => undefined);
      await flushUntil(() => fake.calls.filter((call) => call.method === "thread/read").length === 2);

      // Attempt B supersedes A on the wire and answers rebuilding: the flag is
      // B's outcome to own.
      fake.on("thread/read", () => {
        throw rebuildingRejection();
      });
      const refreshB = threadsStore
        .getState()
        .refreshThread("ref_a")
        .catch(() => undefined);
      await flushUntil(() => threadsStore.getState().repairingRefs.has("ref_a"));

      // A resolves late with success. Publication was already refused for it
      // (superseded); its repair-status update must be refused the same way.
      hungRead.resolve?.(readResponse("ref_a", { name: "Late" }));
      await refreshA;
      await refreshB;
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      threadsStore.getState().releaseThread("ref_a");
    });

    test("releasing a first open that never published still clears the repair status", async () => {
      const fake = connectFakeClient();
      fake.on("thread/read", () => {
        throw rebuildingRejection();
      });
      const ensuring = threadsStore.getState().ensureThread("ref_a");
      await flushUntil(() => threadsStore.getState().repairingRefs.has("ref_a"));
      expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

      threadsStore.getState().releaseThread("ref_a");
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
      void ensuring.catch(() => undefined);
    });

    test("releasing the last watcher still clears the repair status", async () => {
      const fake = connectFakeClient();
      fake.on("thread/read", () => {
        throw rebuildingRejection();
      });
      const watching = threadsStore.getState().watchThread("ref_a");
      await flushUntil(() => threadsStore.getState().repairingRefs.has("ref_a"));

      threadsStore.getState().releaseWatchedThread("ref_a");
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
      void watching.catch(() => undefined);
    });

    test("a watched release leaves the tracked pane's repair status alone", async () => {
      const fake = connectFakeClient();
      fake.on("thread/read", () => {
        throw rebuildingRejection();
      });
      const ensuring = threadsStore.getState().ensureThread("ref_a");
      await flushUntil(() => threadsStore.getState().repairingRefs.has("ref_a"));

      const watching = threadsStore.getState().watchThread("ref_a");
      threadsStore.getState().releaseWatchedThread("ref_a");
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      threadsStore.getState().releaseThread("ref_a");
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
      void ensuring.catch(() => undefined);
      void watching.catch(() => undefined);
    });

    test("a tracked release leaves a remaining watcher's repair status alone", async () => {
      const fake = connectFakeClient();
      fake.on("thread/read", () => readResponse("ref_a", { name: "Open" }));
      await threadsStore.getState().ensureThread("ref_a");

      fake.on("thread/read", () => {
        throw rebuildingRejection();
      });
      const watching = threadsStore.getState().watchThread("ref_a");
      await flushUntil(() => threadsStore.getState().repairingRefs.has("ref_a"));

      // The pane leaves; the delegate card's watched lifecycle keeps the
      // in-flight repair's status for its own surface — nothing published for
      // the watcher (its read rejected too), so the surviving flag is the
      // held-owner proof.
      threadsStore.getState().releaseThread("ref_a");
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      threadsStore.getState().releaseWatchedThread("ref_a");
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
      void watching.catch(() => undefined);
    });

    test("an older watched read's late success does not clear the repair status the newer tracked read owns", async () => {
      const fake = connectFakeClient();
      fake.on("thread/read", () => readResponse("ref_a", { name: "Open" }));
      await threadsStore.getState().ensureThread("ref_a");

      // The watcher's read hangs on the wire.
      const hungWatch: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
      fake.on(
        "thread/read",
        () =>
          new Promise<ThreadReadResponse>((resolve) => {
            hungWatch.resolve = resolve;
          }),
      );
      const watching = threadsStore.getState().watchThread("ref_a");
      await flushUntil(() => fake.calls.filter((call) => call.method === "thread/read").length === 2);

      // The pane's newer refresh answers rebuilding: the flag is the newer
      // read's outcome. The watched pending is still current in its own map,
      // so pending-entry currency alone cannot order the two.
      fake.on("thread/read", () => {
        throw rebuildingRejection();
      });
      const refresh = threadsStore
        .getState()
        .refreshThread("ref_a")
        .catch(() => undefined);
      await flushUntil(() => threadsStore.getState().repairingRefs.has("ref_a"));
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      // The older watched read resolves late with success: it must not
      // publish the newer read's outcome away.
      hungWatch.resolve?.(readResponse("ref_a", { name: "Late" }));
      await settleCallerContinuations();
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);
      await refresh;

      threadsStore.getState().releaseThread("ref_a");
      threadsStore.getState().releaseWatchedThread("ref_a");
      void watching.catch(() => undefined);
    });

    test("a successful lean watch preserves the tracked read's repair status", async () => {
      const fake = connectFakeClient();
      let reads = 0;
      fake.on("thread/read", () => {
        reads += 1;
        if (reads === 1) throw rebuildingRejection();
        return readResponse("ref_a", { name: "Lean" });
      });

      // The tracked first open answers rebuilding: the owned retry stands and
      // the status flag is set by the read that observed the answer.
      const ensuring = threadsStore.getState().ensureThread("ref_a");
      await flushUntil(() => scheduledHydrationRetries.length === 1);
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      // A lean watcher's read requests no history — the hub can answer it
      // without touching the transcript index — so it observed nothing about
      // the repair: the flag stands until the tracked owner's own read
      // answers, never cleared by the lean watch's success.
      const watching = threadsStore.getState().watchThread("ref_a");
      await watching;
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      // The tracked retry's own read succeeds: the flag clears through the
      // owner that observed the answer.
      runScheduledHydrationRetry(0);
      await ensuring;
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);

      threadsStore.getState().releaseWatchedThread("ref_a");
    });

    test("a lean watch's ordinary rejection preserves the tracked read's repair status", async () => {
      const fake = connectFakeClient();
      let reads = 0;
      fake.on("thread/read", () => {
        reads += 1;
        if (reads === 1) throw rebuildingRejection();
        throw new RequestTimeoutError("lean watch read timed out");
      });

      // The tracked first open answers rebuilding: the owned retry stands and
      // the status flag is set by the read that observed the answer.
      const ensuring = threadsStore.getState().ensureThread("ref_a");
      await flushUntil(() => scheduledHydrationRetries.length === 1);
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      // A lean watcher's read times out — an ordinary transport failure that
      // says nothing about the transcript index. It must not replace the
      // tracked repair's status; the lean read's own retry still converges.
      const watching = threadsStore.getState().watchThread("ref_a");
      await flushUntil(() => fake.calls.filter((call) => call.method === "thread/read").length === 2);
      await settleCallerContinuations();
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      // The tracked retry's own read succeeds: the flag clears through the
      // owner that observed the answer.
      fake.on("thread/read", () => readResponse("ref_a", { name: "Repaired" }));
      runScheduledHydrationRetry(0);
      await ensuring;
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);

      threadsStore.getState().releaseWatchedThread("ref_a");
      void watching.catch(() => undefined);
    });

    test("a lean watch does not silence a tracked read's own repair verdict", async () => {
      const fake = connectFakeClient();
      let reads = 0;
      const hungRefresh: { reject: ((reason?: unknown) => void) | null } = { reject: null };
      fake.on("thread/read", () => {
        reads += 1;
        if (reads === 2) {
          return new Promise<ThreadReadResponse>((_, reject) => {
            hungRefresh.reject = reject;
          });
        }
        return readResponse("ref_a", { name: "Open" });
      });

      // The pane is open and its refresh hangs on the wire.
      await threadsStore.getState().ensureThread("ref_a");
      const refresh = threadsStore
        .getState()
        .refreshThread("ref_a")
        .catch(() => undefined);
      await flushUntil(() => fake.calls.filter((call) => call.method === "thread/read").length === 2);

      // A lean watcher joins while the refresh is in flight: the watcher's
      // metadata-only read succeeds and must not advance the repair sequence
      // out from under the tracked refresh still on the wire.
      const watching = threadsStore.getState().watchThread("ref_a");
      await watching;

      // The refresh answers rebuilding: its own verdict must publish — the
      // lean watch neither bumped the shared sequence nor fenced the
      // refresh's publication out.
      hungRefresh.reject?.(rebuildingRejection());
      await flushUntil(() => threadsStore.getState().repairingRefs.has("ref_a"));
      expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

      threadsStore.getState().releaseThread("ref_a");
      threadsStore.getState().releaseWatchedThread("ref_a");
      void watching.catch(() => undefined);
      await refresh;
    });
  });

  test("initial item hydration sends only the v4 bounded item-read fields", async () => {
    const fake = connectFakeClient();
    let readParams: MethodTypes["thread/read"]["params"] | undefined;
    fake.on("thread/read", (params) => {
      readParams = params;
      return readResponse("ref_a");
    });

    await threadsStore.getState().ensureThread("ref_a");
    expect(fake.calls).toHaveLength(1);
    expect(readParams).toEqual({
      ref: "ref_a",
      includeTurns: true,
      itemsView: "full",
      subscribe: true,
      replaceSubscription: false,
      itemLimit: 40,
    });
  });

  test("an initial open hydration rejection follows its same-epoch resync replacement", async () => {
    const fake = connectFakeClient();
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

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 1);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 2);

    let rejected = false;
    void ensuring.catch(() => {
      rejected = true;
    });
    reads[0]!.reject(new Error("superseded initial read"));
    await flushUntil(() => rejected);

    expect(rejected).toBe(false);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    reads[1]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_authoritative", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_authoritative");
    expect(model?.capabilities.queue).toBe(true);
  });

  test("an open lifecycle follows a newest resync after its failed predecessor already cleared ownership", async () => {
    const fake = connectFakeClient();
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

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 1);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 2);

    reads[1]!.reject(new Error("failed replacement B"));
    await flushUntil(() => false);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 3);

    let rejected = false;
    void ensuring.catch(() => {
      rejected = true;
    });
    reads[0]!.reject(new Error("superseded initial A"));
    await flushUntil(() => rejected);

    expect(rejected).toBe(false);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    reads[2]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_authoritative", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_authoritative");
    expect(model?.capabilities.queue).toBe(true);
  });

  test("a published newest open resync survives later superseded rejections", async () => {
    const fake = connectFakeClient();
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

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 1);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 2);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 3);

    reads[2]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_authoritative", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_authoritative");
    await Promise.resolve();
    await Promise.resolve();

    reads[1]!.reject(new Error("superseded replacement B"));
    await Promise.resolve();
    await Promise.resolve();
    reads[0]!.reject(new Error("superseded initial A"));
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_authoritative");
    expect(model?.capabilities.queue).toBe(true);
  });

  test("routes evener/jobs/treeUpdated by root ref while a child thread is open, ignoring stale revisions", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      const ref = params.ref ?? "";
      if (ref === "local:root") {
        return readResponse(ref, {
          id: "thr_root",
          evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
        });
      }
      if (ref === "local:child") {
        return readResponse(ref, {
          id: "thr_child",
          evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
        });
      }
      throw new Error(`unexpected ref ${ref}`);
    });

    await threadsStore.getState().ensureThread("local:root");
    await threadsStore.getState().ensureThread("local:child");

    const rootBefore = threadsStore.getState().threads.get("local:root");
    const childBefore = threadsStore.getState().threads.get("local:child");
    expect(rootBefore).toBeDefined();
    expect(childBefore).toBeDefined();

    fake.emitNotification({
      method: "evener/jobs/treeUpdated",
      params: { threadId: "thr_root", ref: "local:root", revision: 7 },
    });

    let root = threadsStore.getState().threads.get("local:root");
    let child = threadsStore.getState().threads.get("local:child");
    expect(root?.jobsTreeRevision).toBe(7);
    expect(root?.jobsUpdatedAt).not.toBeNull();
    expect(root?.lastFrameAt).toBe(rootBefore?.lastFrameAt);
    expect(child).toEqual(childBefore);
    expect(threadsStore.getState().frameTimes.get("local:root")).toHaveLength(1);

    fake.emitNotification({
      method: "evener/jobs/treeUpdated",
      params: { threadId: "thr_root", ref: "local:root", revision: 7 },
    });
    fake.emitNotification({
      method: "evener/jobs/treeUpdated",
      params: { threadId: "thr_root", ref: "local:root", revision: 6 },
    });

    root = threadsStore.getState().threads.get("local:root");
    child = threadsStore.getState().threads.get("local:child");
    expect(root?.jobsTreeRevision).toBe(7);
    expect(root?.lastFrameAt).toBe(rootBefore?.lastFrameAt);
    expect(child).toEqual(childBefore);
    expect(threadsStore.getState().frameTimes.get("local:root")).toHaveLength(1);
    expect(threadsStore.getState().frameTimes.get("local:child")).toBeUndefined();
  });

  // Pre-Task-5, wiring was lazy (attached inside requireClient(), the first
  // time some store action ran) - so this test used to connect the client
  // FIRST, attach spies second, and prove idempotency only across
  // subsequent action calls. Wiring is now reactive to connectionStore's
  // own client reference (see rewireClient/connectionStore.subscribe in
  // threads.ts) precisely so an already-open pane keeps receiving deltas
  // through a manual-retry client swap with no action call required at
  // all - so the spies must be attached BEFORE connect() to observe that.
  //
  // The count right after connect() is NOT this store's own contribution
  // alone: stores/navigation/store.ts, stores/extensions.ts, and stores/credentials.ts
  // each independently run this exact same reactive-wiring pattern against
  // connectionStore, so `fake.onNotification`/`fake.onReady` also get called
  // once per OTHER such store whose module this file's imports happen to
  // load - a real, correct fact about this composition, not a leak.
  //
  // What THIS test owns proving is narrower: that connecting a client wires
  // threads.ts's OWN handler exactly once, and that wiring never happens
  // AGAIN per store action. resetThreadsStoreForTests() unwires only
  // threads.ts's registration and nulls its own wiredClient - every other
  // reactively-wired store still holds wiredClient === fake, so re-publishing
  // that SAME client reference through connectionStore (bypassing connect()'s
  // own "already this client" guard) re-triggers ONLY threads.ts's
  // rewireClient. The resulting delta is threads.ts's contribution in
  // isolation, provably exactly one call per handler - not merely "some
  // baseline that stays flat afterward".
  test("wires onNotification/onReady on the client exactly once, at connect time - not per store action", async () => {
    const fake = new FakeClient();
    const onNotificationSpy = vi.spyOn(fake, "onNotification");
    const onReadySpy = vi.spyOn(fake, "onReady");
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));

    connectionStore.getState().connect(fake);

    resetThreadsStoreForTests();
    const notificationCallsBeforeRewire = onNotificationSpy.mock.calls.length;
    const readyCallsBeforeRewire = onReadySpy.mock.calls.length;
    connectionStore.setState({ client: fake, state: fake.state });
    expect(onNotificationSpy.mock.calls.length - notificationCallsBeforeRewire).toBe(1);
    expect(onReadySpy.mock.calls.length - readyCallsBeforeRewire).toBe(1);

    const wiredNotificationCalls = onNotificationSpy.mock.calls.length;
    const wiredReadyCalls = onReadySpy.mock.calls.length;

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    await threadsStore
      .getState()
      .send("ref_a", "hi")
      .catch(() => {}); // no turn/start handler scripted; rejection irrelevant here

    expect(onNotificationSpy).toHaveBeenCalledTimes(wiredNotificationCalls);
    expect(onReadySpy).toHaveBeenCalledTimes(wiredReadyCalls);
  });
});

describe("wire subscription tracking", () => {
  test("a re-read of an already-subscribed ref sends subscribe:false, not another subscribe", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    // The resync path re-reads the ref while it stays tracked.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => fake.calls.filter((call) => call.method === "thread/read").length >= 2);
    await flushUntil(() => threadsStore.getState().hydrations.get("ref_a") !== undefined);

    const reads = fake.calls.filter((call) => call.method === "thread/read");
    expect(reads.length).toBeGreaterThanOrEqual(2);
    expect(reads[0]?.params).toMatchObject({ subscribe: true });
    expect(reads[1]?.params).toMatchObject({ subscribe: false });
  });

  test("a reconnect re-subscribes the still-tracked ref on the new connection", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    // A fresh client is a fresh connection: its subscriptions start empty.
    const next = connectFakeClient();
    next.on("thread/read", () => readResponse("ref_a"));
    await flushUntil(() => next.calls.some((call) => call.method === "thread/read"));

    const nextReads = next.calls.filter((call) => call.method === "thread/read");
    expect(nextReads[0]?.params).toMatchObject({ subscribe: true });
  });

  test("releasing while another pane is pending does not unsubscribe the watched ref", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a", { includeTurns: false });

    threadsStore.getState().releaseThread("ref_a"); // pane leaves; watcher remains
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);

    threadsStore.getState().releaseWatchedThread("ref_a"); // watcher leaves too
    await flushUntil(() => fake.calls.filter((call) => call.method === "thread/unsubscribe").length === 1);
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
  });

  // The lost-window fix: a release that runs while the hydrating read is
  // still in flight sees the set WITHOUT the ref (no unsubscribe sent), so
  // the read's own resolution must not record a zero-holder entry — it sends
  // its own unsubscribe instead, and the server-side subscription this read
  // created does not linger until connection close.
  test("a read resolving after its final release unsubscribes instead of leaking the entry", async () => {
    const fake = connectFakeClient();
    const releaseRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          releaseRead.resolve = resolve;
        }),
    );
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => releaseRead.resolve !== null);

    threadsStore.getState().releaseThread("ref_a"); // mid-flight: no unsubscribe yet
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);

    releaseRead.resolve?.(readResponse("ref_a"));
    await ensuring;
    await flushUntil(() => fake.calls.filter((call) => call.method === "thread/unsubscribe").length === 1);
    const unsubscribes = fake.calls.filter((call) => call.method === "thread/unsubscribe");
    expect(unsubscribes[0]?.params).toMatchObject({ ref: "ref_a" });
    // And a later ensure of the same ref subscribes afresh.
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const reads = fake.calls.filter((call) => call.method === "thread/read");
    expect(reads[reads.length - 1]?.params).toMatchObject({ subscribe: true });
  });
});

describe("reconnect resubscribe", () => {
  test("onReady refire re-subscribes every tracked ref additively and replaces each model wholesale", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      const ref = (params as { ref: string }).ref;
      // The first pass (ensureThread) hydrates one turn each; the
      // post-reconnect pass returns an empty turns list. If the store merged
      // instead of replacing, the old turn would still be there.
      const turns = readCount <= 2 ? [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] : [];
      return readResponse(ref, { turns });
    });

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);
    expect(threadsStore.getState().threads.get("ref_b")?.turns).toHaveLength(1);

    // FakeClient defaults to "ready"; emitReady() alone would no-op (same
    // early-return AppwireClient.setState has for a same-state transition),
    // so a genuine reconnect needs the intermediate drop first.
    fake.emitStateChange("reconnecting");
    fake.emitReady(); // simulated reconnect

    await flushUntil(
      () =>
        threadsStore.getState().threads.get("ref_a")?.turns.length === 0 &&
        threadsStore.getState().threads.get("ref_b")?.turns.length === 0,
    );

    const readCallsAfterReconnect = fake.calls.filter((c) => c.method === "thread/read").slice(2);
    expect(readCallsAfterReconnect).toHaveLength(2); // every tracked ref re-subscribed, nothing else

    const forA = readCallsAfterReconnect.find((c) => (c.params as { ref: string }).ref === "ref_a");
    const forB = readCallsAfterReconnect.find((c) => (c.params as { ref: string }).ref === "ref_b");
    const expectedParams = (ref: string) => ({
      ref,
      includeTurns: true,
      itemsView: "full",
      subscribe: true,
      replaceSubscription: false,
      itemLimit: 40,
    });
    expect(forA?.params).toEqual(expectedParams("ref_a"));
    expect(forB?.params).toEqual(expectedParams("ref_b"));
  });
});

test.each([false, true])(
  "a non-storage enqueue failure preserves only durable pins (existing mutation: %s)",
  async (existingMutation) => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("turn/start", () => new Promise<never>(() => undefined));
    if (existingMutation) await threadsStore.getState().send("ref_a", "already saved");
    const unsubscribed = existingMutation ? undefined : nextHandledRequest(fake, "thread/unsubscribe", () => ({}));
    vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(() => {
      throw new Error("enqueue fault");
    });
    await expect(threadsStore.getState().send("ref_a", "not saved")).rejects.toThrow("enqueue fault");
    expect(await storage.listOutbox()).toHaveLength(existingMutation ? 1 : 0);
    threadsStore.getState().releaseThread("ref_a");
    await flushUntil(() => threadsStore.getState().threads.has("ref_a") === existingMutation);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(existingMutation);
    await unsubscribed;
  },
);

describe("frameTimes tracking (threads store)", () => {
  test("releaseThread drops the ref's frameTimes entry along with its model", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    vi.spyOn(Date, "now").mockReturnValue(1000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000]);

    threadsStore.getState().releaseThread("ref_a");

    await flushUntil(() => !threadsStore.getState().frameTimes.has("ref_a"));
    expect(threadsStore.getState().frameTimes.has("ref_a")).toBe(false);
  });
});

describe("useThreadsStore.loadOlderTurns", () => {
  test("fetches the older page via thread/turns/list using the model's olderCursor, prepends it, and advances the cursor", async () => {
    const fake = connectFakeClient();
    let readParams: unknown;
    let listParams: unknown;
    fake.on("thread/read", (params) => {
      readParams = params;
      return {
        thread: testThread("ref_a", { turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] }),
        olderCursor: "cursor_1",
      };
    });
    fake.on("thread/turns/list", (params) => {
      listParams = params;
      return {
        data: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
        nextCursor: "cursor_0",
      };
    });
    await threadsStore.getState().ensureThread("ref_a");

    await threadsStore.getState().loadOlderTurns("ref_a");

    expect(readParams).not.toHaveProperty("pageUnit");
    expect(readParams).toHaveProperty("itemLimit", 40);
    expect(readParams).not.toHaveProperty("turnLimit");
    expect(listParams).toEqual({
      ref: "ref_a",
      cursor: "cursor_1",
      itemsView: "full",
      itemLimit: 40,
    });
    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((t) => t.id)).toEqual(["turn_1", "turn_2"]);
    expect(model?.olderCursor).toBe("cursor_0");
  });

  test("reacquires the ready client after waiting for a tracked hydration", async () => {
    const oldClient = connectFakeClient();
    oldClient.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
    oldClient.on("thread/turns/list", () => ({ data: [], nextCursor: undefined }));
    await threadsStore.getState().ensureThread("ref_a");

    let resolveBlockingHydration!: (response: ThreadReadResponse) => void;
    const blockingHydrationRequested = nextHandledRequest(
      oldClient,
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveBlockingHydration = resolve;
        }),
    );

    oldClient.emitStateChange("reconnecting");
    oldClient.emitReady();
    await blockingHydrationRequested;
    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await settleCallerContinuations();

    const newClient = new FakeClient("ready");
    newClient.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_2" }));
    newClient.on("thread/turns/list", () => ({ data: [], nextCursor: undefined }));
    connectionStore.getState().connect(newClient);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.olderCursor === "cursor_2");
    resolveBlockingHydration({ thread: testThread("ref_a"), olderCursor: "stale-cursor" });
    await loading;

    expect({
      old: oldClient.calls.filter((call) => call.method === "thread/turns/list").length,
      current: newClient.calls.filter((call) => call.method === "thread/turns/list").length,
    }).toEqual({ old: 0, current: 1 });
  });

  test("a stale item cursor triggers one fresh subscribed read and does not surface an older-page error", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", (params) => {
      expect(params).toMatchObject({ itemLimit: 40 });
      expect(params).not.toHaveProperty("pageUnit");
      expect(params).not.toHaveProperty("turnLimit");
      reads += 1;
      return {
        thread: testThread("ref_a", {
          turns: [{ id: reads === 1 ? "old-turn" : "fresh-turn", status: "completed", itemsView: "full", items: [] }],
        }),
        olderCursor: reads === 1 ? "stale-cursor" : "fresh-cursor",
      };
    });
    fake.on("thread/turns/list", () => {
      throw new WireError("cursor stale", -32013, { evenerErrorInfo: "transcriptItemCursorStale" });
    });
    await threadsStore.getState().ensureThread("ref_a");

    await expect(threadsStore.getState().loadOlderTurns("ref_a")).resolves.toBeUndefined();

    expect(reads).toBe(2);
    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["fresh-turn"]);
    expect(threadsStore.getState().threads.get("ref_a")?.olderCursor).toBe("fresh-cursor");
  });

  test("a stale rejection from a superseded same-cursor page preserves the accepted page", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      return {
        thread: testThread("ref_a", {
          turns: [
            {
              id: reads === 1 ? "current-turn" : "unexpected-recovery",
              status: "completed",
              itemsView: "full",
              items: [],
            },
          ],
        }),
        olderCursor: reads === 1 ? "shared-cursor" : "recovery-cursor",
      };
    });
    type DeferredPage = {
      resolve: (response: ThreadTurnsListResponse) => void;
      reject: (error: Error) => void;
    };
    const requests: DeferredPage[] = [];
    let announceFirstRequest!: () => void;
    let announceSecondRequest!: () => void;
    const firstRequested = new Promise<void>((resolve) => (announceFirstRequest = resolve));
    const secondRequested = new Promise<void>((resolve) => (announceSecondRequest = resolve));
    fake.on(
      "thread/turns/list",
      () =>
        new Promise<ThreadTurnsListResponse>((resolve, reject) => {
          requests.push({ resolve, reject });
          if (requests.length === 1) announceFirstRequest();
          if (requests.length === 2) announceSecondRequest();
        }),
    );
    await threadsStore.getState().ensureThread("ref_a");

    const accepted = threadsStore.getState().loadOlderTurns("ref_a");
    await firstRequested;
    const stale = threadsStore.getState().loadOlderTurns("ref_a");
    await secondRequested;
    requests[0]?.resolve({
      data: [{ id: "accepted-older", status: "completed", itemsView: "full", items: [] }],
      nextCursor: "advanced-cursor",
    });
    await accepted;
    requests[1]?.reject(new WireError("cursor stale", -32013, { evenerErrorInfo: "transcriptItemCursorStale" }));
    await stale;

    expect(reads).toBe(1);
    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["accepted-older", "current-turn"]);
    expect(threadsStore.getState().threads.get("ref_a")?.olderCursor).toBe("advanced-cursor");
  });

  test("a stale page rejection does not replace a pending same-epoch targeted resync", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    let resolveResync!: (response: ThreadReadResponse) => void;
    let announceResyncRequest!: () => void;
    const resyncRequested = new Promise<void>((resolve) => (announceResyncRequest = resolve));
    fake.on("thread/read", () => {
      reads += 1;
      if (reads === 1) {
        return {
          thread: testThread("ref_a", {
            turns: [{ id: "before-resync", status: "completed", itemsView: "full", items: [] }],
          }),
          olderCursor: "stale-cursor",
        };
      }
      if (reads === 2) {
        return new Promise<ThreadReadResponse>((resolve) => {
          resolveResync = resolve;
          announceResyncRequest();
        });
      }
      return {
        thread: testThread("ref_a", {
          turns: [{ id: "unexpected-recovery", status: "completed", itemsView: "full", items: [] }],
        }),
        olderCursor: "recovery-cursor",
      };
    });
    let rejectStalePage!: (error: Error) => void;
    let announcePageRequest!: () => void;
    const pageRequested = new Promise<void>((resolve) => (announcePageRequest = resolve));
    fake.on(
      "thread/turns/list",
      () =>
        new Promise<ThreadTurnsListResponse>((_resolve, reject) => {
          rejectStalePage = reject;
          announcePageRequest();
        }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await pageRequested;

    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await resyncRequested;
    const resyncPublished = new Promise<void>((resolve) => {
      const unsubscribe = threadsStore.subscribe((state) => {
        if (state.threads.get("ref_a")?.turns[0]?.id !== "after-resync") return;
        unsubscribe();
        resolve();
      });
    });
    rejectStalePage(new WireError("cursor stale", -32013, { evenerErrorInfo: "transcriptItemCursorStale" }));
    await loading;

    expect(reads).toBe(2);
    resolveResync({
      thread: testThread("ref_a", {
        turns: [{ id: "after-resync", status: "completed", itemsView: "full", items: [] }],
      }),
      olderCursor: "resync-cursor",
    });
    await resyncPublished;
    expect(threadsStore.getState().threads.get("ref_a")?.olderCursor).toBe("resync-cursor");
  });

  test("a successful pre-resync older page does not merge while a same-epoch targeted resync is pending", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    let resolveResync!: (response: ThreadReadResponse) => void;
    let announceResyncRequest!: () => void;
    const resyncRequested = new Promise<void>((resolve) => (announceResyncRequest = resolve));
    fake.on("thread/read", () => {
      reads += 1;
      if (reads === 1) {
        return {
          thread: testThread("ref_a", {
            turns: [{ id: "before-resync", status: "completed", itemsView: "full", items: [] }],
          }),
          olderCursor: "shared-cursor",
        };
      }
      return new Promise<ThreadReadResponse>((resolve) => {
        resolveResync = resolve;
        announceResyncRequest();
      });
    });
    let resolvePage!: (response: ThreadTurnsListResponse) => void;
    let announcePageRequest!: () => void;
    const pageRequested = new Promise<void>((resolve) => (announcePageRequest = resolve));
    fake.on(
      "thread/turns/list",
      () =>
        new Promise<ThreadTurnsListResponse>((resolve) => {
          resolvePage = resolve;
          announcePageRequest();
        }),
    );
    await threadsStore.getState().ensureThread("ref_a");

    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await pageRequested;
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await resyncRequested;

    resolvePage({
      data: [{ id: "stale-older-page", status: "completed", itemsView: "full", items: [] }],
      nextCursor: "page-cursor",
    });
    await loading;
    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["before-resync"]);

    const resyncPublished = new Promise<void>((resolve) => {
      const unsubscribe = threadsStore.subscribe((state) => {
        if (state.threads.get("ref_a")?.turns[0]?.id !== "after-resync") return;
        unsubscribe();
        resolve();
      });
    });
    resolveResync({
      thread: testThread("ref_a", {
        turns: [{ id: "after-resync", status: "completed", itemsView: "full", items: [] }],
      }),
      olderCursor: "resync-cursor",
    });
    await resyncPublished;
    expect(reads).toBe(2);
    expect(threadsStore.getState().threads.get("ref_a")?.olderCursor).toBe("resync-cursor");
  });

  test("an ordinary older-page failure rejects so the inline retry UI can surface it", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
    const failure = new Error("list failed");
    fake.on("thread/turns/list", () => {
      throw failure;
    });
    await threadsStore.getState().ensureThread("ref_a");

    await expect(threadsStore.getState().loadOlderTurns("ref_a")).rejects.toBe(failure);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  });

  test("a live notification arriving while an older page is in flight survives the merge", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => ({
      thread: testThread("ref_a"),
      olderCursor: "cursor_1",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 0 },
    }));
    let resolvePage!: (response: ThreadTurnsListResponse) => void;
    fake.on("thread/turns/list", () => new Promise<ThreadTurnsListResponse>((resolve) => (resolvePage = resolve)));
    await threadsStore.getState().ensureThread("ref_a");

    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await flushUntil(() => resolvePage !== undefined);
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 2 },
        turns: [{ id: "live-turn", status: "inProgress", itemsView: "", version: 2 }],
      },
    });
    resolvePage({
      data: [{ id: "older-turn", status: "completed", itemsView: "full", items: [], version: 1 }],
      nextCursor: "cursor_0",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
    });
    await loading;

    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["older-turn", "live-turn"]);
  });

  test("is a no-op when the tracked model has no olderCursor (nothing more to load)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a")); // no olderCursor override -> undefined
    await threadsStore.getState().ensureThread("ref_a");

    await threadsStore.getState().loadOlderTurns("ref_a");

    expect(fake.calls.filter((c) => c.method === "thread/turns/list")).toHaveLength(0);
  });

  test("is a no-op when the ref is not tracked at all", async () => {
    const fake = connectFakeClient();

    await threadsStore.getState().loadOlderTurns("ref_never_tracked");

    expect(fake.calls.filter((c) => c.method === "thread/turns/list")).toHaveLength(0);
  });

  test("throws when no client has been connected yet, same as every other action", async () => {
    await expect(threadsStore.getState().loadOlderTurns("ref_a")).rejects.toThrow(/no client connected/i);
  });

  test("a ref released while the older-turns request is in flight is not resurrected", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
    await threadsStore.getState().ensureThread("ref_a");

    const box: { resolve: ((r: ThreadTurnsListResponse) => void) | null } = { resolve: null };
    fake.on("thread/turns/list", () => new Promise<ThreadTurnsListResponse>((resolve) => (box.resolve = resolve)));

    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await flushUntil(() => box.resolve !== null);
    threadsStore.getState().releaseThread("ref_a");
    box.resolve?.({ data: [], nextCursor: undefined });
    await loading;

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  // Issue #195's RCA: read-only, so it waits out a reconnect (via
  // requireReadyClient, stores/threads.ts) instead of failing with
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection - contrast with the mutation re-pin tests (setModel, send),
  // which must keep rejecting synchronously.
  test("waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/turns/list", () => ({
      data: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
      nextCursor: "cursor_0",
    }));

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().loadOlderTurns("ref_a");
    let settled = false;
    pending.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      },
    );
    await flushUntil(() => false, 5);
    expect(settled).toBe(false); // still waiting, not rejected synchronously
    expect(fake.calls.filter((c) => c.method === "thread/turns/list")).toHaveLength(0);

    fake.emitReady();
    await pending;
    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((t) => t.id)).toEqual(["turn_1"]);
    expect(model?.olderCursor).toBe("cursor_0");
  });
});

describe("retry-safe mutation outbox integration", () => {
  test("send resolves from the local commit while a lost response leaves one durable intent", async () => {
    const response = deferred<TurnStartResponse>();
    const called = deferred<void>();
    const fake = connectFakeClient();
    fake.on("turn/start", () => {
      called.resolve();
      return response.promise;
    });

    const submitted = threadsStore.getState().send("ref_a", "hello");
    void submitted.catch(() => undefined);
    await called.promise;
    await submitted;

    const storage = new MutationOutboxIndexedDB();
    const records = await storage.listOutbox("ref_a");
    expect(records).toHaveLength(1);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(fake.calls.find((call) => call.method === "turn/start")?.params).toMatchObject({
      ref: "ref_a",
      clientMutationId: records[0]?.clientMutationId,
      input: [{ type: "text", text: "hello" }],
    });

    response.reject(new RequestTimeoutError("response lost"));
  });

  test.each([true, false])(
    "activity and a pending outbox replay share alias membership; release activity first %s",
    async (activityFirst) => {
      const ref = "remote:workspace";
      const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-activity-alias" });
      await storage.enqueueIntent({
        targetRef: ref,
        method: "turn/queue",
        payload: { ref, input: [{ type: "text", text: "retained intent" }] },
        attachments: [],
        optimisticDisplay: { text: "retained intent" },
      });
      storage.close();
      const fake = connectFakeClient("connecting");
      const response = deferred<TurnQueueResponse>();
      fake.on("thread/read", () => readResponse(ref));
      fake.on("evener/thread/activity/read", ({ scope }) => activitySummary(ref, scope));
      fake.on("thread/unsubscribe", () => ({}));
      fake.on("turn/queue", () => response.promise);
      const activity = acquireSessionActivity(fake, ref);
      try {
        fake.emitReady();
        await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
        expect(
          fake.calls.filter(
            (call) => call.method === "thread/read" && (call.params as { subscribe: boolean }).subscribe,
          ),
        ).toHaveLength(1);
        expect(
          fake.calls
            .filter((call) => call.method === "thread/read")
            .every((call) => (call.params as { ref: string }).ref === ref),
        ).toBe(true);
        if (activityFirst) {
          activity.release();
          expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
        }
        response.resolve({ receipt: mutationReceipt("mutation-activity-alias") });
        const probe = new MutationOutboxIndexedDB();
        await waitFor(async () => expect(await probe.listTargetRefs()).toEqual([]));
        probe.close();
        if (!activityFirst) {
          expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
          activity.release();
        }
        await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1));
        expect(fake.calls.find((call) => call.method === "thread/unsubscribe")?.params).toEqual({ ref });
      } finally {
        activity.release();
        response.resolve({ receipt: mutationReceipt("mutation-activity-alias") });
      }
    },
  );

  test("hydrates a pinned outbox ref before replaying it", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: {
        ref: "ref_a",
        input: [{ type: "text", text: "queued" }],
      },
      attachments: [],
      optimisticDisplay: { text: "queued" },
    });
    storage.close();
    const read = deferred<ThreadReadResponse>();
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => read.promise);
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    fake.emitReady();
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "thread/read"));

    expect(fake.calls.map((call) => call.method)).toEqual(["thread/read"]);
    read.resolve(readResponse("ref_a"));
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.map((call) => call.method)).toEqual(["thread/read", "turn/queue"]);
  });

  test.each(["bounded transcript", "cleared instance"])(
    "retries an unresolved %s mutation with its original identity and payload",
    async (snapshot) => {
      const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
      const record = await storage.enqueueIntent({
        targetRef: "ref_a",
        method: "turn/queue",
        payload: {
          ref: "ref_a",
          expectedInstanceId: "original-instance",
          expectedQueueRevision: 3,
          input: [{ type: "text", text: "queued before the outage" }],
        },
        attachments: [],
        optimisticDisplay: { text: "queued before the outage" },
      });
      await storage.markAttempted(record.clientMutationId);
      await storage.markUnknown(record.clientMutationId, "blockedUnknown");
      storage.close();
      const fake = connectFakeClient("connecting");
      fake.on("thread/read", () => ({
        olderCursor: snapshot === "bounded transcript" ? "older-transcript" : undefined,
        ...readResponse("ref_a", {
          evener: {
            ref: "ref_a",
            capabilities: CAPABILITIES,
            instanceId: snapshot === "bounded transcript" ? "original-instance" : "cleared-instance",
            mutationStateAuthoritative: true,
            queue: { revision: 9, clientMutationIds: [] },
          },
        }),
      }));
      const retried = nextHandledRequest(fake, "turn/queue", (params) => {
        if (snapshot === "cleared instance") {
          throw new WireError("instance changed", -32014, {
            clientMutationId: params.clientMutationId,
            mutationOutcome: "notAccepted",
          });
        }
        return { receipt: { ...mutationReceipt(params.clientMutationId), disposition: "replayed" } };
      });

      fake.emitReady();
      expect(await retried).toEqual({
        ref: "ref_a",
        expectedInstanceId: "original-instance",
        expectedQueueRevision: 3,
        input: [{ type: "text", text: "queued before the outage" }],
        clientMutationId: "mutation-a",
      });
      await threadsStore.getState().refreshThread("ref_a");
      const inspector = new MutationOutboxIndexedDB();
      expect(await inspector.getOutbox(record.clientMutationId)).toBeUndefined();
      if (snapshot === "cleared instance") {
        expect(await inspector.getRecovery(record.clientMutationId)).toMatchObject({
          recoveryKind: "rejected",
          payload: record.payload,
        });
      } else {
        expect(await inspector.getRecovery(record.clientMutationId)).toBeUndefined();
      }
      inspector.close();
    },
  );

  test("hydrates a durable optimistic ref without redispatching its settled transport intent", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/start",
      payload: {
        ref: "ref_a",
        input: [{ type: "text", text: "accepted, not reflected" }],
      },
      attachments: [],
      optimisticDisplay: {
        method: "turn/start",
        input: [{ type: "text", text: "accepted, not reflected" }],
      },
    });
    await storage.settleReceipt(record.clientMutationId, "pending");
    storage.close();
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a"));

    fake.emitReady();
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "thread/read"));
    await flushIndexedDBUntil(() => threadsStore.getState().threads.has("ref_a"));

    expect(fake.calls.map((call) => call.method)).toEqual(["thread/read"]);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    const inspector = new MutationOutboxIndexedDB();
    expect(await inspector.getOptimistic(record.clientMutationId)).toBeDefined();
    inspector.close();
  });

  test("retries a failed pinned rejoin on a ready lifecycle discovery before replay", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
      attachments: [],
      optimisticDisplay: { text: "queued" },
    });
    storage.close();
    let readAttempts = 0;
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) throw new Error("transient read failure");
      return readResponse("ref_a");
    });
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    fake.emitReady();
    await flushIndexedDBUntil(() => readAttempts === 1);
    await flushIndexedDBUntil(() => false, 2);
    window.dispatchEvent(new Event("focus"));
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    expect(readAttempts).toBe(2);
    expect(fake.calls.map((call) => call.method)).toEqual(["thread/read", "thread/read", "turn/queue"]);
  });

  test("pinned mutation rejoin retries without focus or another ready transition", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
      attachments: [],
      optimisticDisplay: { text: "queued" },
    });
    storage.close();
    let readAttempts = 0;
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => {
      readAttempts += 1;
      // Both reads the ready transition itself can produce fail: the rejoin
      // read for the discovered pinned ref, and the one the outbox's own
      // ready-scan then asks for. Every non-retry trigger this connection has
      // is exhausted by the time the assertions below run.
      if (readAttempts <= 2) throw new RequestTimeoutError("rejoin read timed out");
      return readResponse("ref_a");
    });
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    fake.emitReady();
    await flushIndexedDBUntil(() => readAttempts === 2);

    // The durable record is what owns this ref; replay stays closed while the
    // authoritative read is still missing. Both failures share ONE scheduled
    // retry - a lifecycle never stacks them.
    expect(scheduledHydrationRetries).toHaveLength(1);
    expect(fake.calls.map((call) => call.method)).toEqual(["thread/read", "thread/read"]);

    // No window focus event and no second emitReady: the store's own scheduled
    // retry is the only thing left that can converge this ref.
    runScheduledHydrationRetry();
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    expect(readAttempts).toBe(3);
    expect(fake.calls.map((call) => call.method)).toEqual(["thread/read", "thread/read", "thread/read", "turn/queue"]);
    expect(fake.calls.find((call) => call.method === "turn/queue")?.params).toMatchObject({
      clientMutationId: "mutation-a",
    });
  });

  // openOwnedHydration must retire the lifecycle it replaces. A pinned ref is
  // the one owner that survives releaseThread untouched, so an owner
  // generation can be bumped while an older generation's lifecycle still holds
  // a scheduled retry and an unsettled firstHydration. Without the retire that
  // retry is never cancelled, and whatever is waiting on that promise waits
  // for a generation nothing will ever publish into.
  test("a superseded owner generation retires the lifecycle it replaces", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
      attachments: [],
      optimisticDisplay: { text: "queued" },
    });
    storage.close();
    let readAttempts = 0;
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => {
      readAttempts += 1;
      throw new RequestTimeoutError("rejoin read timed out");
    });

    // The durable record is the only owner, so this lifecycle belongs to owner
    // generation zero. Both reads the ready transition produces fail and share
    // the one retry that lifecycle has.
    fake.emitReady();
    await flushIndexedDBUntil(() => readAttempts === 2);
    expect(scheduledHydrationRetries).toHaveLength(1);

    // A pane claims the same ref, which bumps the owner generation. Its own
    // failed read opens the replacement lifecycle.
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    void ensuring.catch(() => undefined);
    await flushUntil(() => scheduledHydrationRetries.length === 2);

    expect(scheduledHydrationRetries).toHaveLength(2);
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);
    expect(scheduledHydrationRetries[1]?.cancelled).toBe(false);
  });

  // seedPinnedIntent leaves one durable turn/queue record for `ref`, which is
  // the only kind of owner a ref can have with no pane and no watcher mounted.
  async function seedPinnedIntent(ref: string, clientMutationId: string): Promise<void> {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => clientMutationId });
    await storage.enqueueIntent({
      targetRef: ref,
      method: "turn/queue",
      payload: { ref, expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
      attachments: [],
      optimisticDisplay: { text: "queued" },
    });
    storage.close();
  }

  // appliedItemNotification carries a clientMutationId, which is what makes the
  // store reconcile that identity and then re-derive the ref's pin from what is
  // left in storage. It is the only trigger that can unpin a ref without a
  // successful authoritative read.
  function appliedItemNotification(ref: string, clientMutationId: string) {
    return {
      method: "history/updated",
      params: {
        threadId: `thr_${ref}`,
        ref,
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "commandExecution" as const,
              id: "item_1",
              turnId: "turn_1",
              clientMutationId,
              output: "queued",
              status: "completed" as const,
            },
            turnId: "turn_1",
          },
        ],
      },
    } as AnyNotification;
  }

  // Retirement is total only if every way a ref can lose its last owner runs
  // through it. Losing a pin is the one that does not go through
  // releaseThread/releaseWatchedThread, so dropUnpinnedModel has to retire the
  // lifecycle itself: without that, a retry stays armed for a ref this store
  // has stopped tracking, and its callback would still find a live record.
  test("settling the last durable record retires the lifecycle its pin held open", async () => {
    await seedPinnedIntent("ref_a", "mutation-a");
    let readAttempts = 0;
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => {
      readAttempts += 1;
      throw new RequestTimeoutError("rejoin read timed out");
    });

    fake.emitReady();
    await flushIndexedDBUntil(() => readAttempts === 2);
    expect(scheduledHydrationRetries).toHaveLength(1);
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(false);
    fake.emitNotification(appliedItemNotification("ref_a", "mutation-a"));
    await flushIndexedDBUntil(() => scheduledHydrationRetries[0]?.cancelled === true);

    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);
    expect(readAttempts).toBe(2);
  });

  // The drain's bookkeeping obligations: the inflight hydration it leaves
  // behind must not be adoptable. A pane that reopens while the drained
  // read is still on the wire must hydrate from its own fresh read, not
  // adopt a retired promise whose rejection can no longer schedule a retry.
  test("a pane reopening after the pin drains hydrates from its own read, not the retired one", async () => {
    await seedPinnedIntent("ref_a", "mutation-a");
    const reads: Array<{
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: unknown) => void;
    }> = [];
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => {
      const read = deferred<ThreadReadResponse>();
      reads.push(read);
      return read.promise;
    });
    fake.on("turn/queue", () => new Promise<TurnQueueResponse>(() => {}));

    // The ready transition rejoins the discovered pinned ref; its read hangs.
    fake.emitReady();
    await flushIndexedDBUntil(() => reads.length === 1);

    // The record settles: the drain retires the pinned lifetime while the
    // rejoin read is still on the wire.
    fake.emitNotification(appliedItemNotification("ref_a", "mutation-a"));
    await flushIndexedDBUntil(() => scheduledHydrationRetries[0]?.cancelled === true);

    // A pane opens. Its own read must hydrate it — the retired rejoin read
    // is not a promise it can adopt, and not one its own hydration queues
    // behind either: the pane hydrates while the drained request is still
    // unresolved on the wire.
    fake.on("thread/read", () => readResponse("ref_a", { name: "Fresh" }));
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await ensuring;
    expect(threadsStore.getState().threads.get("ref_a")?.name).toBe("Fresh");
    reads[0]!.reject(new RequestTimeoutError("the drained rejoin read timed out"));

    threadsStore.getState().releaseThread("ref_a");
  });

  // The drain's repair-status obligations (#4343): the flag a pinned ref's
  // read set cannot outlive the pin, and a read still on the wire when the
  // pin leaves cannot recreate it for a lifetime that just ended.
  test("settling the last durable record clears the repair status its ref's read set", async () => {
    await seedPinnedIntent("ref_a", "mutation-a");
    const reads: Array<{
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: unknown) => void;
    }> = [];
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => {
      const read = deferred<ThreadReadResponse>();
      reads.push(read);
      return read.promise;
    });
    fake.on("turn/queue", () => new Promise<TurnQueueResponse>(() => {}));

    // The ready transition rejoins the discovered pinned ref and publishes
    // its snapshot, so a resync can put a second read on the wire. The
    // macrotask settles the first read's consumer chain — the lease resolves
    // it through a timer hop — so no late step of it can land after the
    // events the assertions below pin.
    fake.emitReady();
    await flushIndexedDBUntil(() => reads.length === 1);
    reads[0]!.resolve(readResponse("ref_a"));
    await flushIndexedDBUntil(() => threadsStore.getState().threads.has("ref_a"));
    await settleCallerContinuations();
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushIndexedDBUntil(() => reads.length === 2);

    // That read answers the hub's rebuilding code: the flag is set.
    reads[1]!.reject(
      new WireError("thread history is being rebuilt in the background", -32000, {
        evenerErrorInfo: "transcriptIndexRebuilding",
      }),
    );
    await flushIndexedDBUntil(() => threadsStore.getState().repairingRefs.has("ref_a"));
    expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(true);

    // Settling the record drops the pin — the ref's only owner — so the
    // repair status cannot outlive it.
    fake.emitNotification(appliedItemNotification("ref_a", "mutation-a"));
    await flushIndexedDBUntil(() => !threadsStore.getState().threads.has("ref_a"));
    expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
  });

  test("a rejoin read that answers rebuilding after its record settles recreates no repair status", async () => {
    await seedPinnedIntent("ref_a", "mutation-a");
    const reads: Array<{
      promise: Promise<ThreadReadResponse>;
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: unknown) => void;
    }> = [];
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => {
      const read = deferred<ThreadReadResponse>();
      reads.push(read);
      return read.promise;
    });
    fake.on("turn/queue", () => new Promise<TurnQueueResponse>(() => {}));

    fake.emitReady();
    await flushIndexedDBUntil(() => reads.length === 1);
    reads[0]!.resolve(readResponse("ref_a"));
    await flushIndexedDBUntil(() => threadsStore.getState().threads.has("ref_a"));
    // The macrotask settles the first read's consumer chain — the lease
    // resolves it through a timer hop — so its late steps cannot land after
    // the drain and muddy what the assertions below pin.
    await settleCallerContinuations();
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushIndexedDBUntil(() => reads.length === 2);

    // The pin leaves while the read is still on the wire: the drain retires
    // the lifetime, so the read's late rebuilding answer must publish
    // nothing. The rejection settles through the lease's timer hop too, so
    // one macrotask covers the consumer's catch chain.
    fake.emitNotification(appliedItemNotification("ref_a", "mutation-a"));
    await flushIndexedDBUntil(() => !threadsStore.getState().threads.has("ref_a"));
    reads[1]!.reject(
      new WireError("thread history is being rebuilt in the background", -32000, {
        evenerErrorInfo: "transcriptIndexRebuilding",
      }),
    );
    // The lease's read is a derived promise, so settling the raw deferred
    // does not settle the consumer's catch chain; one macrotask does.
    await reads[1]!.promise.catch(() => undefined);
    await settleCallerContinuations();
    expect(threadsStore.getState().repairingRefs.has("ref_a")).toBe(false);
  });

  // The mirror of the case above, one step earlier: here the pin goes away
  // while the read is still on the wire, so there is no lifecycle to retire
  // yet. Opening one anyway would arm a retry for a ref nothing owns - a timer
  // and a map entry that outlive everything that could ever consume them.

  // handleReady re-checks the ready epoch before its pinned-ref discovery scan
  // and has nothing to say after it. The scan is a real IndexedDB read, so a
  // reconnect landing inside it is not exotic: on a cold start nothing is
  // tracked, the tracked-ref fan-out finishes in a microtask, and the scan is
  // still outstanding tasks later. Its continuation must not dispatch rejoins
  // for a ready generation that is already dead - a read issued there returns a
  // snapshot cut on a connection nobody is subscribed to any more.
  test("a reconnect during the pinned-ref discovery scan dispatches no rejoin on the dead epoch", async () => {
    await seedPinnedIntent("ref_a", "mutation-a");
    // The scan is held open by a deferred rather than by timing luck. It is a
    // real IndexedDB read either way - the outbox's own startup scan is the
    // first one, and handleReady's is the second, issued after the runtime
    // initializes - so this removes the race's variance, not its existence.
    const storage = new MutationOutboxIndexedDB();
    const discovery = deferred<string[]>();
    const realListTargetRefs = storage.listTargetRefs.bind(storage);
    let scans = 0;
    vi.spyOn(storage, "listTargetRefs").mockImplementation(() => {
      scans += 1;
      return scans === 2 ? discovery.promise : realListTargetRefs();
    });
    setMutationStorageForTests(storage);

    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a"));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    // handleReady's own scan being ISSUED is the observable that puts this
    // generation inside the window: its tracked-ref fan-out is empty here and
    // settles in microtasks, while the scan remains outstanding.
    fake.emitReady();
    await flushIndexedDBUntil(() => scans >= 2);
    expect(scans).toBe(2);

    // The reconnect supersedes that generation while its scan is still open.
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "thread/read"));

    // Releasing the scan resumes the dead generation. It may record the pin -
    // that is a storage fact - but it must put nothing on the wire.
    discovery.resolve(["ref_a"]);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
  });

  // The publish gate's ready-epoch fence, reached without any stale dispatch:
  // this rejoin was started on the generation that was live at the time. What
  // makes its snapshot stale is only that the connection came back underneath
  // it, and what keeps its pending entry current is that the ref was unowned
  // when the reconnect fanned out - so the reconnect skipped it rather than
  // superseding it. A pane then adopts that in-flight read and re-owns the ref,
  // which puts the attempt-identity and ownership checks either side of the
  // epoch fence back into the permissive state.

  // The publish gate's ownership fence does more than keep an unowned model out
  // of the store - the setState below it declines that on its own. What only
  // this fence stops is the snapshot being treated as published at all:
  // publishAndReconcileThreadHydration settles every durable intent the
  // snapshot claims as applied, and a settled intent does not come back. A
  // snapshot for a ref nobody owns any more must not get that authority.
  test("a snapshot for a ref that lost its last owner settles no durable intent", async () => {
    await seedPinnedIntent("ref_a", "mutation-a");
    await seedPinnedIntent("ref_b", "mutation-b");
    const reads = new Map<string, { resolve: (response: ThreadReadResponse) => void }>();
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", (params) => {
      const ref = (params as { ref: string }).ref;
      return new Promise<ThreadReadResponse>((resolve) => reads.set(ref, { resolve }));
    });
    // Neither intent may settle by way of its own replay.
    fake.on("turn/queue", () => new Promise<TurnQueueResponse>(() => {}));

    fake.emitReady();
    await flushIndexedDBUntil(() => reads.has("ref_a") && reads.has("ref_b"));
    expect(reads.has("ref_a")).toBe(true);

    // ref_a's record settles, and it was ref_a's only owner.
    fake.emitNotification(appliedItemNotification("ref_a", "mutation-a"));
    const inspector = new MutationOutboxIndexedDB();
    let recordSettled = false;
    for (let attempt = 0; attempt < 20 && !recordSettled; attempt += 1) {
      recordSettled = (await inspector.getOutbox("mutation-a")) === undefined;
    }
    expect(recordSettled).toBe(true);
    await settleCallerContinuations();

    // ref_a's rejoin now answers, and its snapshot claims ref_b's intent as
    // applied. Nothing owns ref_a, so the snapshot carries no authority at all.
    reads.get("ref_a")?.resolve(
      readResponse("ref_a", {
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 1, clientMutationIds: ["mutation-b"] },
        },
      }),
    );
    await settleCallerContinuations();

    expect(await inspector.getOutbox("mutation-b")).toBeDefined();
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    inspector.close();
  });

  test("a targeted resync closes the target replay gate until its snapshot reconciles", async () => {
    const responseLost = deferred<TurnQueueResponse>();
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => {
      if (fake.calls.filter((call) => call.method === "turn/queue").length === 1) return responseLost.promise;
      return { receipt: mutationReceipt(params.clientMutationId) };
    });
    await threadsStore.getState().queue("ref_a", "queued");
    await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/queue").length === 1);
    responseLost.reject(new RequestTimeoutError("response lost"));
    await flushIndexedDBUntil(() => false, 2);
    const inspector = new MutationOutboxIndexedDB();
    const [record] = await inspector.listOutbox("ref_a");
    expect(record).toBeDefined();

    const resyncRead = deferred<ThreadReadResponse>();
    fake.on("thread/read", () => resyncRead.promise);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "thread/read").length >= 1);
    await threadsStore.getState().queue("ref_a", "enqueued during resync");
    window.dispatchEvent(new Event("focus"));
    await flushIndexedDBUntil(() => false, 3);

    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);

    resyncRead.resolve(
      readResponse("ref_a", {
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 1, clientMutationIds: [record!.clientMutationId] },
          pendingMutations: [],
        },
      }),
    );
    for (let attempt = 0; attempt < 20 && (await inspector.getOutbox(record!.clientMutationId)); attempt += 1) {
      await Promise.resolve();
    }
    expect(await inspector.getOutbox(record!.clientMutationId)).toBeUndefined();
    await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/queue").length === 2);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(2);
    inspector.close();
  });

  test("snapshot identity settles a pinned intent before replay, including receipt-only controls", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/interrupt",
      payload: { ref: "ref_a", expectedTurnId: "turn_1" },
      attachments: [],
      optimisticDisplay: null,
    });
    storage.close();
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 4 },
          pendingMutations: [
            {
              clientMutationId: "mutation-a",
              method: "turn/interrupt",
              executionState: "accepted",
              projectionState: "removed",
            },
          ],
        },
      }),
    );
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    fake.emitReady();
    await flushUntil(() => fake.calls.some((call) => call.method === "thread/read"));
    const inspector = new MutationOutboxIndexedDB();
    for (let attempt = 0; attempt < 20 && (await inspector.getOutbox("mutation-a")) !== undefined; attempt += 1) {
      await Promise.resolve();
    }

    expect(await inspector.getOutbox("mutation-a")).toBeUndefined();
    expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toHaveLength(0);
  });

  test("a stale client hydration cannot settle an intent in a newer ready generation", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
      attachments: [],
      optimisticDisplay: { text: "queued" },
    });
    storage.close();
    const staleRead = deferred<ThreadReadResponse>();
    const currentRead = deferred<ThreadReadResponse>();
    const stale = connectFakeClient("connecting");
    stale.on("thread/read", () => staleRead.promise);
    stale.emitReady();
    await flushIndexedDBUntil(() => stale.calls.some((call) => call.method === "thread/read"));

    const current = new FakeClient("ready");
    current.on("thread/read", () => currentRead.promise);
    current.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    connectionStore.getState().connect(current);
    await flushIndexedDBUntil(() => current.calls.some((call) => call.method === "thread/read"));

    staleRead.resolve(
      readResponse("ref_a", {
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 1, clientMutationIds: ["mutation-a"] },
        },
      }),
    );
    await Promise.resolve();
    const inspector = new MutationOutboxIndexedDB();
    expect(await inspector.getOutbox("mutation-a")).toBeDefined();

    currentRead.resolve(readResponse("ref_a"));
    await flushIndexedDBUntil(() => current.calls.some((call) => call.method === "turn/queue"));
    expect(current.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  });
});

test("a new message composed after a saved snapshot can still dispatch", async () => {
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "notLoaded" } }));
  fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().queue("ref_a", "new message");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
});

test("a message composed during a saved read dispatches when the read completes", async () => {
  // The outbox's periodic scan would also find this message, seconds later.
  // With the interval faked and never advanced, only the hydration's own
  // completion can deliver it.
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient("connecting");
    const saved = deferred<ThreadReadResponse>();
    fake.on("thread/read", () => saved.promise);
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    const hydration = threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().queue("ref_a", "new message during hydration");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    saved.resolve(readResponse("ref_a", { status: { type: "notLoaded" } }));
    await hydration;
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  } finally {
    vi.useRealTimers();
  }
});

test("reload after a failed restart write reconciles persisted uncertainty with the resumed daemon", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "reload-uncertain" });
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  await storage.markAttempted(record.clientMutationId);
  setMutationStorageForTests(storage);
  vi.spyOn(storage, "markUnknown").mockRejectedValue(new DOMException("storage unavailable", "AbortError"));
  const old = connectFakeClient("connecting");
  old.on("thread/read", () => readResponse("ref_a", { status: { type: "restartRequired" } }));
  old.emitReady();
  await threadsStore
    .getState()
    .ensureThread("ref_a")
    .catch(() => undefined);
  expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("submitting");
  resetThreadsStoreForTests();
  const reloadedStorage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(reloadedStorage);
  const reloaded = connectFakeClient("connecting");
  let resumed = false;
  reloaded.on("thread/read", (params) => ({
    ...readResponse("ref_a", {
      status: { type: resumed ? "idle" : "notLoaded" },
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, clientMutationIds: resumed ? [record.clientMutationId] : [] },
      },
    }),
    // The mid-case reset clears the legacy override, so this reload is ordinary.
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
    requestGeneration: params.requestGeneration ?? 1,
  }));
  reloaded.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  reloaded.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  expect((await reloadedStorage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
  expect(reloaded.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  resumed = true;
  await threadsStore.getState().refreshThread("ref_a");
  expect(await reloadedStorage.getOutbox(record.clientMutationId)).toBeUndefined();
  expect(reloaded.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  expect(
    reloaded.calls
      .filter((call) => call.method === "thread/read")
      .map((call) => (call.params as { requestGeneration?: number }).requestGeneration),
  ).toContain(2);
});

// RoboRev finding on the reduced branch: the refreshThread beforePublish fence
// is what keeps a stale post-resume hydration from publishing over a newer Stop.
// Only the fence's ordering was proven before (the FakeClient tests assert a
// function was passed, never that a throw cancels the read's result). This
// proves the invariant at the store boundary: a throwing fence rejects the call
// and leaves the published model, the mutation-authority set and the hydration
// counter exactly as they were.
test("a throwing beforePublish cancels a refreshThread publication", async () => {
  setMutationStorageForTests(new MutationOutboxIndexedDB());
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  const before = threadsStore.getState();
  const publishedModel = before.threads.get("ref_a");
  expect(publishedModel?.status.type).toBe("idle");
  expect(before.mutationAuthorityRefs.has("ref_a")).toBe(true);
  const hydrations = before.hydrations.get("ref_a");
  // The pending read would replace the model and drop authority if it published.
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      preview: "replacement",
      evener: { ref: "ref_a", mutationStateAuthoritative: false, capabilities: CAPABILITIES, queue: { revision: 0 } },
    }),
  );
  const fence = new Error("Stop canceled this pending action");
  await expect(
    threadsStore.getState().refreshThread("ref_a", () => {
      throw fence;
    }),
  ).rejects.toBe(fence);
  const after = threadsStore.getState();
  expect(after.threads.get("ref_a")).toBe(publishedModel);
  expect(after.mutationAuthorityRefs.has("ref_a")).toBe(true);
  expect(after.hydrations.get("ref_a")).toBe(hydrations);
});

test.each(["output", "metadata"] as const)("fences obsolete job %s after readiness returns", async (kind) => {
  const peer = new JobOutputPeer();
  const client = new AppwireClient({ url: "ws://job-output.test/rpc", socketFactory: () => peer });
  connectionStore.getState().connect(client);
  const connecting = client.connect();
  let current = true;
  const pending =
    kind === "output"
      ? threadsStore.getState().jobOutput("local:session_output", "job_output", 0, 1, () => current)
      : threadsStore.getState().jobGet("local:session_output", "job_output", () => current);
  const method = kind === "output" ? "evener/jobs/output" : "evener/jobs/get";
  // A wrongly dispatched read gets a real wire response, so RED is a fulfilled
  // obsolete call rather than a fixture timeout.
  void peer.request(method).then((request) => peer.reply(request, {}));
  current = false;
  try {
    const result = expect(pending).rejects.toThrow("job read is no longer current");
    peer.open();
    await connecting;
    await result;
    expect(peer.requests(method)).toEqual([]);
  } finally {
    client.close();
    resetConnectionStoreForTests();
  }
});

// #4552 (Jesse's ruling, 2026-10-13): a graceful shutdown gates mutation
// DISPATCH only. Its cancellation write is an await and is not itself a fence,
// so a dispatch already scheduled for the ref could be admitted in that window
// and put the row the Stop is cancelling on the wire - the race a 30 ms pause at
// the top of cancelUnattemptedMutations reproduces in Session.test.tsx's "a Stop
// on the resumed identity cancels the stale post-resume publish". forceStop
// closes the window with stoppingRefs; a graceful shutdown must NOT, because
// that fence also refuses a Send the user makes during the window
// (isRecoveryFenced and isSendResumes read it). This dispatch-only
// gate keeps the cancelled row off the wire while leaving every Send face
// untouched.
//
// The shared stage for the shutdown-dispatch-gate tests below: an idle local ref
// whose client answers reads and turn/start, on the given (or a fresh) outbox.
function stageShutdownGate(
  ref: string,
  storage = new MutationOutboxIndexedDB(),
): { fake: FakeClient; storage: MutationOutboxIndexedDB } {
  setMutationStorageForTests(storage);
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref, { status: { type: "idle" } }));
  fake.on("turn/start", (params) => ({
    receipt: mutationReceipt(params.clientMutationId),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  return { fake, storage };
}

test("a graceful shutdown gates dispatch without refusing a Send made during the window", async () => {
  const ref = "local:shutdown-gate-send";
  const { fake, storage } = stageShutdownGate(ref);
  fake.on("thread/shutdown", () => ({}));
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  // The cancellation write is durably done but still in flight for the Stop -
  // exactly the window that is unfenced today.
  const hold = holdNextWriteTransaction(["outbox", "sequences"]);
  try {
    const pending = threadsStore.getState().shutdown(ref);
    await hold.reached;
    // The dispatch-only gate did NOT arm the broad Stop fence, so the session
    // stays off the Send-resumes face, exactly as Jesse ruled.
    expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false);
    // A Send pressed during the window is admitted and its row is durable.
    await threadsStore.getState().send(ref, "during shutdown");
    expect((await storage.listOutbox(ref)).map((record) => record.state)).toEqual(["submitting"]);
    // ...but its dispatch is held off the wire for the window.
    await flushIndexedDBUntil(() => false);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    hold.release();
    await pending;
  } finally {
    hold.release();
  }
  // The gate is released with the shutdown, so the row dispatches afterwards
  // with no manual readiness nudge (RoboRev Low on the first revision: release
  // must reschedule, or the row waits for the next discovery scan).
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
});

// The gate must not leak: a shutdown that rejects has still released it, or the
// session could never dispatch again.
test("the shutdown dispatch gate is released when the shutdown rejects", async () => {
  const ref = "local:shutdown-gate-reject";
  const { fake, storage } = stageShutdownGate(ref);
  const stop = deferred<void>();
  fake.on("thread/shutdown", () => stop.promise);
  await threadsStore.getState().ensureThread(ref);
  const hold = holdNextWriteTransaction(["outbox", "sequences"]);
  try {
    const pending = threadsStore.getState().shutdown(ref);
    // Attach the rejection handler up front: the shutdown rejects only when the
    // held write releases below, and an unobserved rejection would be flagged.
    const settled = pending.then(
      () => undefined,
      (err: Error) => err,
    );
    await hold.reached;
    await threadsStore.getState().send(ref, "during shutdown");
    expect((await storage.listOutbox(ref)).map((record) => record.state)).toEqual(["submitting"]);
    await flushIndexedDBUntil(() => false);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    hold.release();
    stop.reject(new Error("shutdown refused"));
    expect((await settled)?.message).toBe("shutdown refused");
  } finally {
    hold.release();
  }
  // The gate did not leak: the row dispatches once the shutdown has returned,
  // with no manual readiness nudge.
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
});

// The Stop's reach is wider than the ref it names: an identity-changing resume
// registers old -> resumed, and the Stop's second cancellation write reaches the
// rows the drive moved under the resumed ref. The gate must cover that ref for
// its own cancellation window, or a dispatch racing that write can put a moved
// row on the wire.
test("a graceful shutdown also gates the ref a resume moved rows to", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:swap-gate-from";
  const toRef = "local:swap-gate-to";
  const idle = (ref: string) =>
    readResponse(ref, {
      status: { type: "idle" },
      evener: { ref, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  const fenced = (ref: string) =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  fake.on("thread/read", (params) => (params.ref === toRef ? idle(toRef) : fenced(fromRef)));
  fake.on("thread/resume", () => idle(toRef));
  fake.on("thread/shutdown", () => ({}));
  fake.on("turn/start", (params) => ({
    receipt: mutationReceipt(params.clientMutationId),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  const startsToTarget = () =>
    fake.calls.filter((call) => call.method === "turn/start" && (call.params as { ref?: string }).ref === toRef);
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  await threadsStore.getState().ensureThread(toRef);
  // A send on the resume-only old ref drives the identity-changing resume, which
  // registers old -> resumed until the pane follows the identity.
  await threadsStore.getState().send(fromRef, "resumed");
  await flushIndexedDBUntil(() => threadsStore.getState().resumedIdentities.has(fromRef));
  const dispatchedBeforeStop = startsToTarget().length;
  // Hold the resumed ref's own cancellation write: that await is the window its
  // arm covers.
  const realCancel = MutationOutboxIndexedDB.prototype.cancelUnattempted;
  let swapCancelReached!: () => void;
  const reached = new Promise<void>((resolve) => {
    swapCancelReached = resolve;
  });
  let releaseSwapCancel!: () => void;
  const swapCancelHeld = new Promise<void>((resolve) => {
    releaseSwapCancel = resolve;
  });
  const spy = vi.spyOn(MutationOutboxIndexedDB.prototype, "cancelUnattempted").mockImplementation(async function (
    this: MutationOutboxIndexedDB,
    target: string,
  ) {
    if (target === toRef) {
      swapCancelReached();
      await swapCancelHeld;
    }
    return realCancel.call(this, target);
  });
  try {
    const pending = threadsStore.getState().shutdown(fromRef);
    await reached;
    // A Send on the resumed ref is admitted, but its dispatch is gated for the
    // window of that ref's own cancellation write.
    await threadsStore.getState().send(toRef, "during shutdown");
    await flushIndexedDBUntil(() => false);
    expect(startsToTarget()).toHaveLength(dispatchedBeforeStop);
    releaseSwapCancel();
    await pending;
  } finally {
    spy.mockRestore();
  }
});

// The gate must not leak: a shutdown whose cancellation write fails has still
// released it, or the session could never dispatch again.
test("the shutdown dispatch gate is released when the cancellation write fails", async () => {
  let failCancel = true;
  const storage = new MutationOutboxIndexedDB({
    beforeCommit(operation) {
      if (failCancel && operation === "cancelUnattempted") throw new Error("storage unavailable");
    },
  });
  const ref = "local:shutdown-gate-cancel-fail";
  const { fake } = stageShutdownGate(ref, storage);
  fake.on("thread/shutdown", () => ({}));
  await threadsStore.getState().ensureThread(ref);
  await expect(threadsStore.getState().shutdown(ref)).rejects.toThrow("storage unavailable");
  failCancel = false;
  // The gate is released, so a fresh Send dispatches.
  await threadsStore.getState().send(ref, "after shutdown");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
});
