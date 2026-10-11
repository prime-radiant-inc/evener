import { acquireSessionActivity } from "./sessionActivity";
import { activitySummary } from "./sessionActivityTestUtils";
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  CachedSessionRecord,
  ConnectionState,
  MethodName,
  MethodTypes,
  QueueState,
  Thread,
  ThreadCapabilities,
  ThreadModel,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  TurnQueueResponse,
  TurnStartResponse,
} from "@evener/appwire-client";
import {
  acquireThreadSubscription,
  applyNotification,
  notificationTargetsThread,
  RequestTimeoutError,
  type ThreadSubscriptionLease,
  WireError,
} from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { mulberry32 } from "@evener/appwire-client/testing/tokenFlood";
import { cleanup, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "./connection";
import "./humanNoteDrafts";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionCacheIndexedDB, type SessionCacheWriteOutcome } from "./sessionCacheIndexedDB";
import {
  appendFrameTime,
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  setSessionCacheAdapterForTests,
  threadRoutingIndexesForTests,
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
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
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

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

type TestThreadOverrides = Omit<Partial<Thread>, "evener"> & {
  evener?: Omit<Thread["evener"], "queue"> & { queue: Partial<QueueState> };
};

function testThread(ref: string, overrides: TestThreadOverrides = {}): Thread {
  const { evener, ...threadOverrides } = overrides;
  const threadID = threadOverrides.id ?? `thr_${ref}`;
  return {
    id: threadID,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref,
      instanceId: threadID,
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      ...evener,
      queue: { revision: 0, ...evener?.queue },
    },
    ...threadOverrides,
  };
}

function readResponse(ref: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

// Like readResponse, but stamped with the same live-history identity
// ("1"/epoch 1/"inc-1") this file's history/updated fixtures carry, so a
// thread/read a test pairs with a live history/updated notification hydrates
// straight into the versioned-history path instead of getting invalidated by
// a boot-generation mismatch (EMPTY_HISTORY's "" vs the frame's "1") the
// instant the first live frame lands.
function versionedReadResponse(
  ref: string,
  overrides: TestThreadOverrides = {},
  snapshotLength = 0,
): ThreadReadResponse {
  return {
    ...readResponse(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: snapshotLength },
  };
}

// readResponse derives the wire thread id as thr_<ref>. The routing-index
// tests need models with known and sometimes SHARED thread ids (a lean watch
// of a ref that is also pane-owned resolves to the same thread id), so this
// variant pins the id explicitly.
function readResponseWithId(ref: string, threadId: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  const base = readResponse(ref, overrides);
  return { thread: { ...base.thread, id: threadId } };
}

// A pending hydration's notification buffer only ever matters in one window:
// between the response cut (which discards everything buffered before it,
// because the authoritative snapshot already represents it) and the publish
// that replays whatever arrived after. markResponseCut/emitAtResponseCut park a
// test exactly there.
//
// The mark rides olderCursor because hydrateThread reads it while building the
// snapshot model, in the same synchronous step that applies the cut. That is a
// probe into someone else's read order, so both guards in emitAtResponseCut are
// load-bearing: if hydrateThread stops reading olderCursor the mark never
// fires, and if the publish outruns it the snapshot is already in the store —
// either way the test fails there rather than emitting into the live-model path
// and passing for the wrong reason. The emit is a callback rather than the
// caller's next statement for the same reason: returning from here would cost
// microtasks of its own, and publish is the very next one.
//
// `published` is how the second guard recognises the snapshot: a ref gaining a
// model at all covers an initial hydrate, but a resync into a ref that already
// has one needs a witness from the snapshot itself, so those callers pass their
// own.
function markResponseCut(response: ThreadReadResponse, cut: { reached: boolean }): ThreadReadResponse {
  return {
    ...response,
    get olderCursor(): string | undefined {
      cut.reached = true;
      return response.olderCursor;
    },
  };
}

async function emitAtResponseCut(
  cut: { reached: boolean },
  ref: string,
  emit: () => void,
  published: () => boolean = () => threadsStore.getState().threads.has(ref),
): Promise<void> {
  for (let i = 0; i < 20 && !cut.reached; i += 1) await Promise.resolve();
  expect(cut.reached, "the response cut never ran: hydrateThread no longer reads olderCursor").toBe(true);
  expect(published(), "the snapshot published before the cut window was reached").toBe(false);
  emit();
}

function sameEpochReconnectFixture() {
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

// connectFakeClient wires a fresh FakeClient through useConnectionStore's
// locked connect(client) entry point — the same path threads.ts's
// requireClient() rides to reach the client (see connection.ts).
function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
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

// The load seam's lookup runs before a cold read is armed, and this suite's
// deferred-read fixtures flush microtasks only — a real IndexedDB lookup
// settles on macrotasks, which would shift every fixture's read past its
// flush window. This suite never tests the cache (threads.sessionCache
// .test.ts does), so its beforeEach swaps in an adapter whose lookups miss
// synchronously: the cold path keeps today's microtask arming timing exactly.
class MissCacheAdapter extends SessionCacheIndexedDB {
  override get(_ref: string, _now: number): Promise<undefined> {
    return Promise.resolve(undefined);
  }
  // The write seam (Task 6) fires debounced writes through this same adapter
  // whenever a publication passes its fire-time gates. The connection gate
  // already refuses them here — the sync-miss get above never opens the
  // adapter — but the shim says "no storage" outright rather than relying on
  // that, so no fixture in this suite can ever write a record or leave one
  // behind in the shared fake database.
  override put(_record: CachedSessionRecord, _scheduledEpoch: number, _now: number): Promise<SessionCacheWriteOutcome> {
    return Promise.resolve({ outcome: "failed" });
  }
}

async function flushIndexedDBUntil(done: () => boolean, maxTurns = 30): Promise<void> {
  const probe = new MutationOutboxIndexedDB();
  for (let turn = 0; turn < maxTurns && !done(); turn += 1) await probe.listTargetRefs();
  probe.close();
}

function mutationReceipt(clientMutationId: string, disposition = "applied") {
  return {
    clientMutationId,
    disposition,
    threadId: "thr_ref_a",
    projectionState: "reflected",
  };
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
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
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
  setSessionCacheAdapterForTests(new MissCacheAdapter());
});

afterEach(() => {
  for (const lease of establishedMemberships.splice(0)) lease.release();
  cleanup();
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  vi.restoreAllMocks();
});

describe("useThreadsStore.ensureThread", () => {
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

  test("re-hydrates an initial same-client epoch from its authoritative response cut", async () => {
    const fake = connectFakeClient();
    const reads: Array<(response: ThreadReadResponse) => void> = [];
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => reads.push(resolve)));
    const { authoritativeSnapshot, completion, turnCompleted } = sameEpochReconnectFixture();

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 1);

    fake.emitStateChange("reconnecting");
    // This frame arrives while the old same-client hydration is still tagged
    // with the previous ready epoch. The new pending record must inherit it.
    fake.emitNotification(completion);
    fake.emitNotification(turnCompleted);
    fake.emitReady();
    await flushUntil(() => reads.length === 2);
    fake.emitReady(); // same-ready duplicate must not start a third hydration
    expect(reads).toHaveLength(2);

    // Old A settles first. Its response must not publish or clear B's pending
    // buffer; B then publishes the authoritative cut.
    reads[0]!(authoritativeSnapshot);
    reads[1]!(authoritativeSnapshot);
    await ensuring;

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.activeTurnId).toBeUndefined();
    expect(model?.turns[0]?.status).toBe("completed");
    expect(model?.turns[0]?.items[0]?.output).toBe("done");
    expect(model?.turns[0]?.items).toHaveLength(1);
    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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

describe("notification routing differential (randomized: index vs scan reference)", () => {
  // A scan-based reference implementation of the pre-index applyToMap: for
  // every tracked model, select via notificationTargetsThread exactly as the
  // old store did, fold via applyNotification, and record frame times the
  // way handleNotification does. The randomized tests below fold the SAME
  // random notification sequence through this reference and through the real
  // store, then assert the resulting maps are structurally identical. The
  // reference is reimplemented from the pre-change shape of applyToMap +
  // handleNotification rather than sharing code with the index path, so it
  // is an independent oracle for the equivalence claim.
  interface FoldResult {
    threads: Map<string, ThreadModel>;
    watchedThreads: Map<string, ThreadModel>;
    frameTimes: Map<string, number[]>;
  }

  function scanFold(state: FoldResult, n: AnyNotification, now: number, skipped: ReadonlySet<string>): void {
    for (const mapName of ["threads", "watchedThreads"] as const) {
      const map = state[mapName];
      const next = new Map(map);
      let changed = false;
      for (const [ref, model] of map) {
        if (skipped.has(ref)) continue;
        if (!notificationTargetsThread(n, model)) continue;
        const updated = applyNotification(model, n, now);
        if (updated === model) continue;
        next.set(ref, updated);
        changed = true;
        if (mapName === "threads") {
          state.frameTimes.set(ref, appendFrameTime(state.frameTimes.get(ref) ?? [], now));
        }
      }
      if (changed) state[mapName] = next;
    }
  }

  // snapshotFor compares two fold results by value, as compact JSON digests:
  // a mismatch fails with a small string diff (a deep object comparison over
  // 200-notification-grown models produces an unprintably large diff), while
  // any content divergence still changes the digest.
  function snapshotFor(state: FoldResult): Record<string, unknown> {
    const capture = (map: Map<string, ThreadModel>) =>
      JSON.stringify(
        Array.from(map, ([ref, model]) => [
          ref,
          model.threadId,
          model.status,
          model.lastFrameAt,
          model.turns,
          model.activeTurnId,
          model.queue,
        ]),
      );
    return {
      threads: capture(state.threads),
      watchedThreads: capture(state.watchedThreads),
      frameTimes: JSON.stringify(Array.from(state.frameTimes)),
    };
  }

  // assertIndexesConsistent checks the thread-id routing index agrees with
  // the maps it indexes, by KEY SET: every tracked model's threadId is
  // indexed under exactly the model's ref, and nothing the maps dropped
  // lingers in an index. Identity needs no assertion any more — a
  // threadId-routed frame resolves its refs back through the map at route
  // time, so a stale model object cannot exist in the index at all; only a
  // stale ref could, and key-set equality is precisely the property that
  // rules it out. Without this, a skipped index update only fails when a
  // random notification sequence happens to observe the staleness.
  function assertIndexesConsistent(): void {
    const indexes = threadRoutingIndexesForTests();
    const check = (
      name: string,
      map: Map<string, ThreadModel>,
      byThreadId: ReadonlyMap<string, ReadonlySet<string>>,
    ): void => {
      const expected = new Map<string, Set<string>>();
      for (const [ref, model] of map) {
        let refs = expected.get(model.threadId);
        if (!refs) {
          refs = new Set();
          expected.set(model.threadId, refs);
        }
        refs.add(ref);
      }
      expect(byThreadId.size, `${name}: byThreadId key set matches the models' thread ids`).toBe(expected.size);
      for (const [threadId, refs] of expected) {
        const indexed = byThreadId.get(threadId);
        expect(indexed, `${name}: byThreadId holds ${threadId}`).toBeDefined();
        expect([...(indexed ?? [])].sort(), `${name}: byThreadId refs for ${threadId}`).toEqual([...refs].sort());
      }
      for (const threadId of byThreadId.keys()) {
        expect(expected.has(threadId), `${name}: byThreadId key ${threadId} exists in the map`).toBe(true);
      }
    };
    check("threads", threadsStore.getState().threads, indexes.threadsByThreadId);
    check("watchedThreads", threadsStore.getState().watchedThreads, indexes.watchedByThreadId);
  }

  test("folding random catalog notifications through the store matches the scan reference exactly", async () => {
    // Deterministic PRNG so a failure is reproducible from the seed printed
    // in the assertion message (mulberry32, shared with the token-flood
    // harness).
    const prng = mulberry32(20260828);
    const pick = <T>(items: readonly T[]): T => {
      const item = items[Math.floor(prng() * items.length)];
      if (item === undefined) throw new Error("pick: empty list");
      return item;
    };

    const refs = ["ref_a", "ref_b", "ref_c"];
    const threadIds: Record<string, string> = {
      ref_a: "thr_shared",
      ref_b: "thr_shared", // ref_a and ref_b deliberately share a thread id
      ref_c: "thr_c",
    };

    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      const ref = (params as { ref: string }).ref;
      const threadId = threadIds[ref];
      if (!threadId) throw new Error(`unexpected thread/read ref ${ref}`);
      // Versioned: the generators below fire history/updated at bootGeneration
      // "1" throughout, so an unversioned hydrate (EMPTY_HISTORY's
      // bootGeneration "") would invalidate on the very first one and never
      // apply another.
      return {
        ...readResponseWithId(ref, threadId),
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 0 },
      };
    });
    for (const ref of refs) await threadsStore.getState().ensureThread(ref);
    await threadsStore.getState().watchThread("ref_a");

    // The reference starts from the same published models the store has.
    const reference: FoldResult = {
      threads: new Map(threadsStore.getState().threads),
      watchedThreads: new Map(threadsStore.getState().watchedThreads),
      frameTimes: new Map(threadsStore.getState().frameTimes),
    };

    // Notification generators covering every routing shape: ref+threadId
    // frames (consistent and contradictory), threadId-only frames, ref-only
    // frames for unknown refs, identity-free broadcasts, and turn/item
    // streaming shapes with turn/item id spaces shared across threads.
    const turnIds = ["turn_1", "turn_2"];
    const itemIds = ["item_1", "item_2"];
    const generators: Array<() => AnyNotification> = [
      () => ({
        method: "thread/status/changed",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          status: pick([{ type: "idle" }, { type: "active" }]),
        },
      }),
      () =>
        ({
          method: "thread/status/changed",
          params: { threadId: pick(Object.values(threadIds)), status: { type: "active" } },
        }) as AnyNotification,
      () => ({
        method: "thread/status/changed",
        params: { threadId: "thr_unknown", ref: "ref_unknown", status: { type: "active" } },
      }),
      () =>
        ({
          method: "thread/status/changed",
          params: { threadId: "thr_unknown", status: { type: "active" } },
        }) as AnyNotification,
      () => ({ method: "evener/auth/updated", params: { provider: "p" } }),
      () => ({ method: "evener/marketplace/updated", params: {} }),
      () => ({
        method: "history/updated",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ id: pick(turnIds), status: "inProgress", itemsView: "" }],
        },
      }),
      () => ({
        method: "history/updated",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [
            {
              ...{ type: "agentMessage", id: pick(itemIds), turnId: pick(turnIds), status: "inProgress" },
              turnId: pick(turnIds),
            },
          ],
        },
      }),
      () => ({
        method: "history/updated",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [{ type: "agentMessage", id: pick(itemIds), turnId: pick(turnIds), text: "x", status: "inProgress" }],
        },
      }),
      () => ({
        method: "history/updated",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [
            {
              ...{ type: "agentMessage", id: pick(itemIds), turnId: pick(turnIds), text: "done", status: "completed" },
              turnId: pick(turnIds),
            },
          ],
        },
      }),
      () => ({
        method: "history/updated",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ id: pick(turnIds), status: "completed", itemsView: "" }],
        },
      }),
      () => ({
        method: "thread/queueChanged",
        params: { threadId: pick(Object.values(threadIds)), ref: pick(refs), queue: { revision: 1 } },
      }),
    ];

    let clock = 10_000;
    const history: AnyNotification[] = [];
    for (let i = 0; i < 200; i += 1) {
      const n = pick(generators)();
      history.push(n);
      clock += 7;
      // turn/started's duplicate-turn-id diagnostic (a console.error the old
      // reducer logged when a turn/started frame named an id already in
      // model.turns) has no read-model replacement — reducer.ts logs nothing
      // any more (history/updated's mergeHistory merges by identity, so a
      // repeated id is an ordinary update, never a duplicate-row hazard).
      // This still asserts the fold never logs unexpectedly.
      const checkNoDiagnostics = (fold: () => void): void => {
        const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
        try {
          fold();
          expect(errorSpy.mock.calls, `diagnostics for notification ${i}: ${n.method}`).toEqual([]);
        } finally {
          errorSpy.mockRestore();
        }
      };
      const dateNowSpy = vi.spyOn(Date, "now").mockReturnValue(clock);
      try {
        checkNoDiagnostics(() => fake.emitNotification(n));
      } finally {
        dateNowSpy.mockRestore();
      }
      checkNoDiagnostics(() => scanFold(reference, n, clock, new Set()));
      for (const map of [
        threadsStore.getState().threads,
        threadsStore.getState().watchedThreads,
        reference.threads,
        reference.watchedThreads,
      ]) {
        for (const model of map.values()) {
          expect(new Set(model.turns.map((turn) => turn.id)).size, `unique turn ids after notification ${i}`).toBe(
            model.turns.length,
          );
        }
      }
      // The index must stay in lockstep with the maps after every fold, not
      // just at the end: a skipped re-index must fail at the frame that
      // skipped it, not only if a later random frame observes the staleness.
      assertIndexesConsistent();
    }

    const actual = snapshotFor({
      threads: threadsStore.getState().threads,
      watchedThreads: threadsStore.getState().watchedThreads,
      frameTimes: threadsStore.getState().frameTimes,
    });
    const expected = snapshotFor(reference);
    expect(actual, `differential mismatch after ${history.length} random notifications`).toEqual(expected);
  });
});

describe("reconnect resubscribe", () => {
  test("a reconnect publishes the completion represented by its authoritative response cut", async () => {
    const fake = connectFakeClient();
    const initialSnapshot = readResponse("ref_a", {
      status: { type: "active" },
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ type: "commandExecution", id: "item_1", turnId: "turn_1", output: "", status: "inProgress" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    });
    const authoritativeSnapshot = readResponse("ref_a", {
      status: { type: "active" },
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
    const reconnectRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      expect((params as { subscribe: boolean }).subscribe).toBe(true);
      if (readCount === 1) return initialSnapshot;
      return new Promise<ThreadReadResponse>((resolve) => {
        reconnectRead.resolve = resolve;
      });
    });

    await threadsStore.getState().ensureThread("ref_a");

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => reconnectRead.resolve !== null);
    expect(reconnectRead.resolve).not.toBeNull();

    // These notifications precede the response cut. The old model remains
    // visible until the authoritative replacement is published.
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" },
            turnId: "turn_1",
          },
        ],
      },
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
      },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.output).toBe("");

    reconnectRead.resolve?.(authoritativeSnapshot);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.activeTurnId === undefined);
    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.activeTurnId).toBeUndefined();
    expect(model?.turns[0]?.status).toBe("completed");
    expect(model?.turns[0]?.items[0]?.output).toBe("done");
  });

  test("a reconnect snapshot supersedes a pre-response ref-targeted turn lifecycle", async () => {
    const fake = connectFakeClient();
    const initialSnapshot = readResponse("ref_a");
    const authoritativeSnapshot = readResponse("ref_a", {
      turns: [{ id: "turn_live", status: "completed", itemsView: "full", items: [] }],
    });
    const reconnectRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) return initialSnapshot;
      return new Promise<ThreadReadResponse>((resolve) => {
        reconnectRead.resolve = resolve;
      });
    });

    await threadsStore.getState().ensureThread("ref_a");

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => reconnectRead.resolve !== null);
    expect(reconnectRead.resolve).not.toBeNull();

    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_live", status: "inProgress", itemsView: "" }],
      },
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_live", status: "completed", itemsView: "" }],
      },
    });

    reconnectRead.resolve?.(authoritativeSnapshot);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.status === "completed");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.activeTurnId).toBeUndefined();
    expect(model?.turns).toHaveLength(1);
    expect(model?.turns[0]?.id).toBe("turn_live");
    expect(model?.turns[0]?.status).toBe("completed");
  });

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

  test("a late old-client hydration cannot overwrite the newest client's authoritative completion", async () => {
    const a = connectFakeClient();
    const staleSnapshot = readResponse("ref_a", {
      status: { type: "active" },
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ type: "commandExecution", id: "item_1", turnId: "turn_1", output: "", status: "inProgress" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    });
    const authoritativeSnapshot = readResponse("ref_a", {
      status: { type: "active" },
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
    let aReadCount = 0;
    let resolveA: ((response: ThreadReadResponse) => void) | null = null;
    a.on("thread/read", () => {
      aReadCount += 1;
      if (aReadCount === 1) return staleSnapshot;
      return new Promise<ThreadReadResponse>((resolve) => {
        resolveA = resolve;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    a.emitStateChange("reconnecting");
    a.emitReady();
    await flushUntil(() => resolveA !== null);

    const b = new FakeClient("ready");
    let resolveB: ((response: ThreadReadResponse) => void) | null = null;
    b.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveB = resolve;
        }),
    );
    connectionStore.getState().connect(b);
    await flushUntil(() => resolveB !== null);

    b.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" },
            turnId: "turn_1",
          },
        ],
      },
    });
    b.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
      },
    });

    // B wins publication first with the authoritative completion.
    expect(resolveB).not.toBeNull();
    const finishB = resolveB as unknown as (response: ThreadReadResponse) => void;
    finishB(authoritativeSnapshot);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.status === "completed");
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.output).toBe("done");

    // A's older response arrives after B and must not restore its stale model.
    const finishA = resolveA as unknown as (response: ThreadReadResponse) => void;
    resolveA = null;
    finishA(staleSnapshot);
    await Promise.resolve();
    await Promise.resolve();
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBeUndefined();
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.output).toBe("done");
  });

  test("a released ref is not re-subscribed on the next onReady", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    threadsStore.getState().releaseThread("ref_a"); // only ref_b remains tracked

    fake.emitStateChange("reconnecting");
    fake.emitReady(); // simulated reconnect — must still re-subscribe ref_b for this test to mean anything
    await flushUntil(() => fake.calls.filter((c) => c.method === "thread/read").length > 2);

    const refsReRead = fake.calls
      .filter((c) => c.method === "thread/read")
      .slice(2)
      .map((c) => (c.params as { ref: string }).ref);
    expect(refsReRead).toEqual(["ref_b"]); // ref_a was released before the reconnect; only ref_b re-subscribes
  });
});

test.each([false, true])(
  "a failed enqueue preserves only durable pins (existing mutation: %s)",
  async (existingMutation) => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("turn/start", () => new Promise<never>(() => undefined));
    if (existingMutation) await threadsStore.getState().send("ref_a", "already saved");
    const unsubscribed = existingMutation ? undefined : nextHandledRequest(fake, "thread/unsubscribe", () => ({}));
    vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(() => {
      throw new DOMException("storage full", "QuotaExceededError");
    });
    await expect(threadsStore.getState().send("ref_a", "not saved")).rejects.toThrow("storage full");
    expect(await storage.listOutbox()).toHaveLength(existingMutation ? 1 : 0);
    threadsStore.getState().releaseThread("ref_a");
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

  test("a stale cursor rejection from a replaced client leaves reconnect hydration authoritative", async () => {
    const oldClient = connectFakeClient();
    let oldReads = 0;
    oldClient.on("thread/read", () => {
      oldReads += 1;
      return {
        thread: testThread("ref_a", {
          turns: [
            {
              id: oldReads === 1 ? "before-reconnect" : "old-client-recovery",
              status: "completed",
              itemsView: "full",
              items: [],
            },
          ],
        }),
        olderCursor: "stale-cursor",
      };
    });
    let rejectStalePage: ((error: Error) => void) | undefined;
    const stalePageRequested = nextHandledRequest(
      oldClient,
      "thread/turns/list",
      () =>
        new Promise<ThreadTurnsListResponse>((_resolve, reject) => {
          rejectStalePage = reject;
        }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await stalePageRequested;

    const newClient = new FakeClient("ready");
    let resolveReconnectHydration: ((response: ThreadReadResponse) => void) | undefined;
    const reconnectHydrationRequested = nextHandledRequest(
      newClient,
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveReconnectHydration = resolve;
        }),
    );
    connectionStore.getState().connect(newClient);
    await reconnectHydrationRequested;
    const authoritativePublished = new Promise<void>((resolve) => {
      const unsubscribe = threadsStore.subscribe((state) => {
        if (state.threads.get("ref_a")?.turns[0]?.id !== "after-reconnect") return;
        unsubscribe();
        resolve();
      });
    });

    rejectStalePage?.(new WireError("cursor stale", -32013, { evenerErrorInfo: "transcriptItemCursorStale" }));
    await loading;

    expect(oldReads).toBe(1);
    resolveReconnectHydration?.({
      thread: testThread("ref_a", {
        turns: [{ id: "after-reconnect", status: "completed", itemsView: "full", items: [] }],
      }),
      olderCursor: "authoritative-cursor",
    });
    await authoritativePublished;
    expect(threadsStore.getState().threads.get("ref_a")?.olderCursor).toBe("authoritative-cursor");
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

  test("a reconnect hydration rejects a pre-cut older-page completion", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    fake.on("thread/read", (params) => {
      expect(params).toMatchObject({ itemLimit: 40 });
      expect(params).not.toHaveProperty("pageUnit");
      expect(params).not.toHaveProperty("turnLimit");
      readCount += 1;
      return {
        thread: testThread("ref_a", {
          turns: [
            {
              id: readCount === 1 ? "before-reconnect" : "after-reconnect",
              status: "completed",
              itemsView: "full",
              items: [],
            },
          ],
        }),
        olderCursor: readCount === 1 ? "cursor_1" : "cursor_2",
      };
    });
    let resolvePage!: (response: ThreadTurnsListResponse) => void;
    fake.on("thread/turns/list", () => new Promise<ThreadTurnsListResponse>((resolve) => (resolvePage = resolve)));
    await threadsStore.getState().ensureThread("ref_a");
    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await flushUntil(() => resolvePage !== undefined);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(
      () => readCount === 2 && threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "after-reconnect",
    );
    resolvePage({
      data: [{ id: "stale-older", status: "completed", itemsView: "full", items: [] }],
      nextCursor: "cursor_0",
    });
    await loading;

    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["after-reconnect"]);
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

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
      fake.on("evener/thread/activity/read", () => activitySummary(ref));
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

  // The mirror of the case above, one step earlier: here the pin goes away
  // while the read is still on the wire, so there is no lifecycle to retire
  // yet. Opening one anyway would arm a retry for a ref nothing owns - a timer
  // and a map entry that outlive everything that could ever consume them.
  test("a rejoin read that fails after its record settles arms no retry", async () => {
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
    // The intent must stay durable until this test settles it by hand, so its
    // replay never reaches a response.
    fake.on("turn/queue", () => new Promise<TurnQueueResponse>(() => {}));

    // The ready transition rejoins the discovered pinned ref. Publishing its
    // snapshot is what makes the unpin below observable: dropUnpinnedModel
    // removes exactly the model this read published.
    fake.emitReady();
    await flushIndexedDBUntil(() => reads.length === 1);
    reads[0]!.resolve(readResponse("ref_a"));
    await flushIndexedDBUntil(() => threadsStore.getState().threads.has("ref_a"));
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);

    // A resync puts one more read on the wire, and its pending entry is the
    // current one - so nothing but ownership stands between its failure and a
    // scheduled retry.
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushIndexedDBUntil(() => reads.length === 2);
    expect(reads).toHaveLength(2);

    // Settling the record drops the pin, and the pin was the only claim: the
    // model is dropped, which is this store deciding it has nothing to converge.
    fake.emitNotification(appliedItemNotification("ref_a", "mutation-a"));
    await flushIndexedDBUntil(() => !threadsStore.getState().threads.has("ref_a"));
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    expect(scheduledHydrationRetries).toHaveLength(0);

    reads[1]!.reject(new RequestTimeoutError("resync read timed out"));
    await flushIndexedDBUntil(() => scheduledHydrationRetries.length > 0);
    expect(scheduledHydrationRetries).toHaveLength(0);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

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
  test("a rejoin adopted across a reconnect publishes the live snapshot, not its own", async () => {
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

    fake.emitReady();
    await flushIndexedDBUntil(() => reads.length === 1);
    expect(reads).toHaveLength(1);

    // Settle the durable record: the pin was this ref's only owner, so the
    // reconnect below finds nothing to refresh and leaves the in-flight read's
    // pending entry standing.
    fake.emitNotification(appliedItemNotification("ref_a", "mutation-a"));
    const inspector = new MutationOutboxIndexedDB();
    let recordSettled = false;
    for (let attempt = 0; attempt < 20 && !recordSettled; attempt += 1) {
      recordSettled =
        (await inspector.getOutbox("mutation-a")) === undefined &&
        (await inspector.getOptimistic("mutation-a")) === undefined;
    }
    inspector.close();
    expect(recordSettled).toBe(true);
    await settleCallerContinuations();

    // A fan-out puts its requests on the wire synchronously, so if the ref were
    // still owned this read count would already be 2 on the next line. Nothing
    // is being waited out here: the reconnect either superseded that pending
    // entry or it did not, and the answer is settled by the time emitReady
    // returns.
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);

    // A pane claims the ref and adopts the read already in flight for it -
    // also synchronous, for the same reason: starting its own read instead
    // would show up in fake.calls before ensureThread first suspends.
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);

    reads[0]!.resolve(
      readResponse("ref_a", { turns: [{ id: "turn_stale", status: "completed", itemsView: "full", items: [] }] }),
    );
    await flushIndexedDBUntil(() => reads.length === 2);
    expect(reads).toHaveLength(2);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    reads[1]!.resolve(
      readResponse("ref_a", { turns: [{ id: "turn_live", status: "completed", itemsView: "full", items: [] }] }),
    );
    await ensuring;

    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_live");
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

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

test("explicit refresh captures the replacement client with its ready epoch", async () => {
  const oldClient = connectFakeClient();
  oldClient.on("thread/read", () => ({ thread: testThread("ref_a", { status: { type: "restartRequired" } }) }));
  await threadsStore.getState().ensureThread("ref_a");
  const refreshing = threadsStore.getState().refreshThread("ref_a");
  const replacement = new FakeClient("ready");
  replacement.on("thread/read", () => ({ thread: testThread("ref_a", { status: { type: "idle" } }) }));
  connectionStore.getState().connect(replacement);
  await refreshing;
  await settleCallerContinuations();
  expect(oldClient.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
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

test("saved snapshots retain restart protection for subsequently discovered outbox records", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "late-upgrade-record" });
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  let status = "restartRequired";
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
  fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  status = "notLoaded";
  await threadsStore.getState().refreshThread("ref_a");
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  await storage.markAttempted(record.clientMutationId);
  await threadsStore.getState().refreshThread("ref_a");
  expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  status = "idle";
  await threadsStore.getState().refreshThread("ref_a");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
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
