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
  ThreadClearResponse,
  ThreadModel,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  TurnQueueResponse,
  TurnStartResponse,
} from "@evener/appwire-client";
import {
  acquireThreadSubscription,
  applyNotification,
  hydrateThread,
  notificationTargetsThread,
  RequestTimeoutError,
  type ThreadSubscriptionLease,
  WireError,
} from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { mulberry32 } from "@evener/appwire-client/testing/tokenFlood";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { type IDBDatabase, IDBFactory, IDBVersionChangeEvent } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import {
  resetSubagentModuleStoreForTests,
  turnScopeKey,
  upsertSubagentRow,
  useSubagentRow,
} from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "./connection";
import "./humanNoteDrafts";
import { MutationDispatcher } from "./mutationDispatcher";
import type { MutationOutboxRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { SessionCacheIndexedDB, type SessionCacheWriteOutcome } from "./sessionCacheIndexedDB";
import { holdNextWriteTransaction, neverSettlingRequest } from "./testing/stalledIndexedDB";
import {
  appendFrameTime,
  ConflictError,
  discardRecoveryMutation,
  hasBlockedUnknown,
  hasQueuedNonSend,
  installHydrationRetrySchedulerForTests,
  notifyReadyForMutationDispatch,
  putThreadModel,
  resetThreadsStoreForTests,
  resumeOnlyLocalDispatchable,
  resumeOnlyLocalModel,
  resumeStopBaseline,
  resumeStopFence,
  retryBlockedMutation,
  setMutationStorageForTests,
  setSessionCacheAdapterForTests,
  subscribeMutationPersistence,
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

// flushMicrotasks drains a bounded number of microtask turns without waiting on
// any condition - for a caller that only wants pending continuations to run
// before its next assertion. (A predicate that is never true would cost at most
// the same 20 hops; this makes that intent explicit rather than reading as a
// wait on the impossible.)
async function flushMicrotasks(turns = 20): Promise<void> {
  for (let i = 0; i < turns; i += 1) await Promise.resolve();
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

function clearResponse(params: { clientMutationId: string }, thread: Thread): ThreadClearResponse {
  return {
    thread,
    ref: thread.evener.ref,
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: thread.id,
      instanceId: thread.evener.instanceId,
      projectionState: "reflected",
    },
  };
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

async function ensureActiveMutationTarget(fake: FakeClient, ref: string): Promise<void> {
  fake.on("thread/read", (params) =>
    readResponse(params.ref ?? ref, {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      evener: {
        ref: params.ref ?? ref,
        capabilities: CAPABILITIES,
        queue: { revision: 7 },
        activeTurnId: "turn_1",
      },
    }),
  );
  await threadsStore.getState().ensureThread(ref);
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

  test("an initial authoritative snapshot supersedes notifications buffered before its response", async () => {
    const fake = connectFakeClient();
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
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    expect(resolveRead).not.toBeNull();

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

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    expect(resolveRead).not.toBeNull();
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(authoritativeSnapshot);
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.activeTurnId).toBeUndefined();
    expect(model?.turns[0]?.status).toBe("completed");
    expect(model?.turns[0]?.items[0]?.output).toBe("done");
  });

  // The pending routing's thread id is seeded from the published model and
  // re-seeded from the authoritative snapshot at the response cut; it is never
  // learned from the stream (kata j4b0). This pins both halves of why that is
  // enough BEFORE the cut, the only window where the routing can lack a thread
  // id at all. An initial hydration has no published model at its ref, so a
  // ref-less frame has nothing to reach whether the buffer takes it or not;
  // and the cut discards everything buffered before it, because the snapshot
  // the daemon ordered there already represents it. A frame the snapshot does
  // NOT carry is what makes that discard visible.
  test("frames before the response cut leave no trace: nothing published to reach, and the cut drops the buffer", async () => {
    const fake = connectFakeClient();
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;

    // A ref-targeted frame naming a turn the snapshot will not carry, then a
    // ref-less frame on the same stream: the pair a buffer would have to route
    // on a thread id taken from the first of them. The second one needs the
    // cast because every notification's params requires `ref` on the wire
    // (ItemLifecycleParams) - the strongest form of the scenario, and still no
    // trace.
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_9", status: "inProgress", itemsView: "", startedAt: 1000 }],
      },
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref-1",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "commandExecution", id: "item_pre_cut_1", turnId: "turn_9", status: "inProgress" },
            turnId: "turn_9",
          },
        ],
      },
    } as AnyNotification);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    finishRead(
      readResponse("ref_a", {
        turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_1"]);
    expect(model?.turns.flatMap((turn) => turn.items.map((item) => item.id))).toEqual([]);
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("an announcement frame landing after the response cut is replayed onto the published snapshot", async () => {
    // The daemon bundles a no-active-turn announcement into one synthetic turn
    // and sends it as a complete turn/completed (systemAnnouncementItem in
    // internal/appprojector/appwire_projection.go). That turn is never the
    // hydration's active turn, so an activeTurnId gate on BUFFERING loses the
    // frame outright: post-cut frames have no other route into the model, and
    // handleNotification also withholds them from the stale published model
    // while a hydration is pending.
    const fake = connectFakeClient();
    const cut = { reached: false };
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(
      markResponseCut(
        versionedReadResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
        }),
        cut,
      ),
    );
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [
            {
              id: "turn_system",
              status: "completed",
              itemsView: "full",
            },
          ],
          items: [
            {
              type: "systemMessage",
              id: "item_plugin_loaded_1",
              turnId: "turn_system",
              position: { entry: 0, item: 0 },
              description: "Plugin loaded: superpowers",
              text: "",
              eventKind: "plugin_loaded",
              status: "completed",
            },
          ].map((it) => ({ ...it, turnId: it.turnId ?? "turn_system" })),
        },
      } as AnyNotification),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_system", "turn_1"]);
    expect(model?.turns[0]?.items[0]?.id).toBe("item_plugin_loaded_1");
    // The real turn the snapshot was cut on is still in flight.
    expect(model?.activeTurnId).toBe("turn_1");
  });

  test("a ref-less announcement frame is buffered on its thread id, not on the active turn", async () => {
    const fake = connectFakeClient();
    const cut = { reached: false };
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(
      markResponseCut(
        versionedReadResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
        }),
        cut,
      ),
    );
    // No ref on this frame: the thread id the response cut taught the routing
    // is the whole identity check it gets.
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [
            {
              id: "turn_system",
              status: "completed",
              itemsView: "full",
            },
          ],
          items: [
            {
              type: "systemMessage",
              id: "item_hook_completed_1",
              turnId: "turn_system",
              position: { entry: 0, item: 0 },
              description: "Hook completed",
              text: "exit 0",
              eventKind: "hook_completed",
              status: "completed",
            },
          ].map((it) => ({ ...it, turnId: it.turnId ?? "turn_system" })),
        },
      } as AnyNotification),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_system", "turn_1"]);
    expect(model?.turns[0]?.items[0]?.id).toBe("item_hook_completed_1");
  });

  test("a turn opened and completed after the response cut arrives whole", async () => {
    // The projector announces a turn it opens implicitly (kata e5r2), so a
    // round that starts and fails inside the post-cut window reaches the
    // buffer as turn/started + turn/completed. Both must be buffered, in
    // order, or the published snapshot shows a turn that never opened or one
    // that never ended.
    const fake = connectFakeClient();
    const cut = { reached: false };
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(
      markResponseCut(
        versionedReadResponse("ref_a", {
          status: { type: "active" },
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
        }),
        cut,
      ),
    );
    await emitAtResponseCut(cut, "ref_a", () => {
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ id: "turn_7", status: "inProgress", itemsView: "", startedAt: 1000 }],
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
          items: [
            {
              ...{ type: "commandExecution", id: "item_tool_1", turnId: "turn_7", status: "inProgress" },
              turnId: "turn_7",
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
          turns: [{ id: "turn_7", status: "failed", itemsView: "", error: { message: "boom" } }],
        },
      } as AnyNotification);
    });
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_7"]);
    expect(model?.turns[0]?.status).toBe("failed");
    expect(model?.turns[0]?.items.map((item) => item.id)).toEqual(["item_tool_1"]);
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("a thread resync publishes its authoritative replacement snapshot", async () => {
    const fake = connectFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) {
        return readResponse("ref_a", {
          status: { type: "active" },
          evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: false }, queue: { revision: 0 } },
        });
      }
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = resolve;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.capabilities.queue).toBe(false);

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);

    const reads = fake.calls.filter((call) => call.method === "thread/read");
    expect(reads).toHaveLength(2);
    expect(reads[1]?.params).toMatchObject({ ref: "ref_a", includeTurns: true });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();

    fake.emitNotification({
      method: "thread/status/changed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        status: { type: "active", activeFlags: ["streaming"] },
      },
    });
    replacementRead.resolve?.(
      readResponse("ref_a", {
        status: { type: "active", activeFlags: ["streaming"] },
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.capabilities.queue === true);

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.capabilities.queue).toBe(true);
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();
  });

  test("a refresh preserves a live active turn omitted by the snapshot cut", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    const cut = { reached: false };
    let resolveRefresh: ((response: ThreadReadResponse) => void) | null = null;
    let refreshRequestGeneration: number | undefined;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) {
        return versionedReadResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
        });
      }
      refreshRequestGeneration = (params as { requestGeneration?: number } | undefined)?.requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => {
        resolveRefresh = resolve;
      });
    });

    await threadsStore.getState().ensureThread("ref_a");
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 }],
      },
    });
    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["turn_m5", "turn_m6"]);

    const refreshing = threadsStore.getState().refreshThread("ref_a");
    await flushUntil(() => resolveRefresh !== null);
    const baselineHydrations = threadsStore.getState().hydrations.get("ref_a") ?? 0;
    const finishRefresh = resolveRefresh as unknown as (response: ThreadReadResponse) => void;
    finishRefresh(
      markResponseCut(
        {
          ...versionedReadResponse(
            "ref_a",
            {
              status: { type: "active" },
              turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
              evener: {
                ref: "ref_a",
                capabilities: CAPABILITIES,
                queue: { revision: 0 },
              },
            },
            1,
          ),
          ...(refreshRequestGeneration !== undefined ? { requestGeneration: refreshRequestGeneration } : {}),
        },
        cut,
      ),
    );
    await emitAtResponseCut(
      cut,
      "ref_a",
      () => {
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
                ...{
                  type: "agentMessage",
                  id: "item_assistant_m6",
                  turnId: "turn_m6",
                  text: "skillguard turn complete",
                  status: "completed",
                },
                turnId: "turn_m6",
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
            turns: [{ id: "turn_m6", status: "completed", itemsView: "" }],
          },
        });
      },
      () => (threadsStore.getState().hydrations.get("ref_a") ?? 0) > baselineHydrations,
    );
    await refreshing;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_m5", "turn_m6"]);
    expect(model?.turns[1]?.items[0]?.text).toBe("skillguard turn complete");
    expect(model?.turns[1]?.status).toBe("completed");
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("a refresh does not preserve a same-numbered turn from a replaced instance", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) {
        return readResponse("ref_a", {
          turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
        });
      }
      return readResponse("ref_a", {
        turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
        evener: {
          ref: "ref_a",
          instanceId: "replacement-instance",
          capabilities: CAPABILITIES,
          queue: { revision: 0 },
          activeTurnId: "turn_m6",
        },
      });
    });

    await threadsStore.getState().ensureThread("ref_a");
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 }],
      },
    });
    await threadsStore.getState().refreshThread("ref_a");

    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["turn_m5"]);
  });

  // The generation is how a mounted consumer notices a WHOLESALE model
  // replacement whose visible fields didn't change - e.g. jobsUpdatedAt is
  // null both before and after a resync, yet activity retained through the
  // gap may be stale (ActivityPanel's freshness effect keys on this).
  test("a thread resync bumps the ref's hydration generation", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const initial = threadsStore.getState().hydrations.get("ref_a");
    expect(initial).toBeGreaterThanOrEqual(1);

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => (threadsStore.getState().hydrations.get("ref_a") ?? 0) > (initial ?? 0));

    expect(threadsStore.getState().hydrations.get("ref_a")).toBe((initial ?? 0) + 1);
  });

  test("a targeted resync preserves identical ordered streaming deltas and their frame times", async () => {
    const fake = connectFakeClient();
    const snapshot = versionedReadResponse("ref_a", {
      status: { type: "active", activeFlags: ["streaming"] },
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "", status: "inProgress" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    });
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    let replacementRequestGeneration: number | undefined;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return snapshot;
      replacementRequestGeneration = (params as { requestGeneration?: number } | undefined)?.requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = resolve;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);

    const delta = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "ha", status: "inProgress" }],
      },
    };
    const delta2 = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 2 },
        items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "haha", status: "inProgress" }],
      },
    };
    replacementRead.resolve?.({
      thread: { ...snapshot.thread, name: "replacement" },
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 0 },
      ...(replacementRequestGeneration !== undefined ? { requestGeneration: replacementRequestGeneration } : {}),
    });
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");
    fake.emitNotification(delta);
    fake.emitNotification(delta2);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.text === "haha");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.items[0]?.text).toBe("haha");
    expect(threadsStore.getState().frameTimes.get("ref_a")).toHaveLength(2);
  });

  test("a targeted resync does not replay a pre-response delta already represented by its snapshot", async () => {
    const fake = connectFakeClient();
    const initial = readResponse("ref_a", {
      status: { type: "active", activeFlags: ["streaming"] },
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "", status: "inProgress" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    });
    const replacement = readResponse("ref_a", {
      status: { type: "active", activeFlags: ["streaming"] },
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "included", status: "inProgress" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    });
    let resolveReplacement: ((response: ThreadReadResponse) => void) | undefined;
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) return initial;
      return new Promise<ThreadReadResponse>((resolve) => {
        resolveReplacement = resolve;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => resolveReplacement !== undefined);
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "included", status: "inProgress" }],
      },
    });
    resolveReplacement?.(replacement);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.text === "included");

    const item = threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0];
    expect(item?.text).toBe("included");
    expect(item?.pendingText).toBeUndefined();
  });

  test("a targeted resync does not replay a pre-response item start already represented by its snapshot", async () => {
    const fake = connectFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) {
        return readResponse("ref_a", {
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
        });
      }
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = resolve;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          { ...{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" }, turnId: "turn_1" },
        ],
      },
    });
    replacementRead.resolve?.(
      readResponse("ref_a", {
        name: "replacement",
        turns: [
          {
            id: "turn_1",
            status: "inProgress",
            itemsView: "full",
            items: [
              {
                type: "agentMessage",
                id: "item_1",
                turnId: "turn_1",
                text: "snapshot",
                status: "inProgress",
              },
            ],
          },
        ],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");

    const items = threadsStore.getState().threads.get("ref_a")?.turns[0]?.items;
    expect(items).toHaveLength(1);
    expect(items?.[0]?.text).toBe("snapshot");
  });

  test("a targeted resync does not replay a pre-response turn start already represented by its snapshot", async () => {
    const fake = connectFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) return readResponse("ref_a");
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = resolve;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "full" }],
        items: [],
      },
    });
    replacementRead.resolve?.(
      readResponse("ref_a", {
        name: "replacement",
        turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns).toHaveLength(1);
    expect(model?.turns[0]?.status).toBe("completed");
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("a targeted resync does not replay a pre-response queue change already represented by its snapshot", async () => {
    const fake = connectFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) return readResponse("ref_a");
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = resolve;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);
    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { revision: 1, preview: ["pre-cut"] },
      },
    });
    replacementRead.resolve?.(
      readResponse("ref_a", {
        name: "replacement",
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 2, preview: ["snapshot"] },
        },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");

    expect(threadsStore.getState().threads.get("ref_a")?.queue).toEqual({
      revision: 2,
      preview: ["snapshot"],
    });
  });

  test("a thread resync supersedes an initial same-epoch open hydration", async () => {
    const fake = connectFakeClient();
    await establishMembership(fake);
    const reads: Array<(response: ThreadReadResponse) => void> = [];
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => reads.push(resolve)));

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 1);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 2);

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    reads[1]?.(
      readResponse("ref_a", {
        turns: [{ id: "turn_authoritative", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_authoritative");
    reads[0]?.(
      readResponse("ref_a", {
        turns: [{ id: "turn_stale", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: false }, queue: { revision: 0 } },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_authoritative");
    expect(model?.capabilities.queue).toBe(true);
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

  test("a second thread resync supersedes the first targeted refresh in the same epoch", async () => {
    const fake = connectFakeClient();
    const replacementReads: Array<(response: ThreadReadResponse) => void> = [];
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) return readResponse("ref_a");
      return new Promise<ThreadReadResponse>((resolve) => replacementReads.push(resolve));
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementReads.length === 1);
    fake.emitNotification({
      method: "thread/status/changed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        status: { type: "active", activeFlags: ["streaming"] },
      },
    });
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementReads.length === 2);

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);
    replacementReads[1]?.(
      readResponse("ref_a", {
        status: { type: "active", activeFlags: ["streaming"] },
        turns: [{ id: "turn_newest", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_newest");
    replacementReads[0]?.(
      readResponse("ref_a", {
        status: { type: "idle" },
        turns: [{ id: "turn_superseded", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_superseded");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_newest");
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
  });

  test("a thread resync for an untracked ref does not read it", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_untracked"));

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_untracked", ref: "ref_untracked" },
    });
    await flushUntil(() => fake.calls.some((call) => call.method === "thread/read"));

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
  });

  test("an initial snapshot supersedes threadId-only notifications buffered after ref routing", async () => {
    const fake = connectFakeClient();
    const box: { resolveRead: ((response: ThreadReadResponse) => void) | null } = { resolveRead: null };
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          box.resolveRead = resolve;
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => box.resolveRead !== null);
    expect(box.resolveRead).not.toBeNull();

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
        items: [
          {
            ...{ type: "agentMessage", id: "item_live", turnId: "turn_live", text: "answer", status: "completed" },
            turnId: "turn_live",
          },
        ],
      },
    });

    box.resolveRead?.(
      readResponse("ref_a", {
        turns: [
          {
            id: "turn_live",
            status: "inProgress",
            itemsView: "full",
            items: [
              {
                type: "agentMessage",
                id: "item_live",
                turnId: "turn_live",
                text: "answer",
                status: "completed",
              },
            ],
          },
        ],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_live" },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns).toHaveLength(1);
    expect(model?.turns[0]?.id).toBe("turn_live");
    expect(model?.turns[0]?.items[0]?.text).toBe("answer");
  });

  test("an initial snapshot supersedes v2 notifications buffered after thread identity is established", async () => {
    const fake = connectFakeClient();
    const box: { resolveRead: ((response: ThreadReadResponse) => void) | null } = { resolveRead: null };
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          box.resolveRead = resolve;
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => box.resolveRead !== null);
    expect(box.resolveRead).not.toBeNull();

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active", activeFlags: ["streaming"] } },
    });
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

    box.resolveRead?.(
      readResponse("ref_a", {
        status: { type: "active", activeFlags: ["streaming"] },
        turns: [
          {
            id: "turn_1",
            status: "inProgress",
            itemsView: "full",
            items: [
              {
                type: "commandExecution",
                id: "item_1",
                turnId: "turn_1",
                output: "done",
                status: "completed",
              },
            ],
          },
        ],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
    expect(model?.turns[0]?.items[0]?.output).toBe("done");
  });

  test("hydrates via thread/read and routes a subsequent matching notification through the reducer", async () => {
    const fake = connectFakeClient();
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

  test("a second ensureThread(ref) does not re-read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_a");

    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  });

  test("concurrent ensureThread(ref) calls share one thread/read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    const p1 = threadsStore.getState().ensureThread("ref_a");
    const p2 = threadsStore.getState().ensureThread("ref_a");
    await Promise.all([p1, p2]);

    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
  });

  test("restarts a pending initial hydrate on a client swap and waits for the new client's model", async () => {
    const a = connectFakeClient();
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

    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  // The one window where a superseding hydrate's routing and the pending it
  // supersedes disagree about which thread this ref names: thread/clear rebinds
  // ref_a while an earlier resync read is still in flight, so the next resync
  // is seeded from the post-clear model and the pending it replaces still holds
  // the pre-clear id. Neither seed decides anything past the response cut - the
  // snapshot this read actually returned re-seeds the routing there, and that
  // is the only reason a frame naming the post-clear thread reaches the model
  // instead of being refused as a contradictory identity and dropped.
  test("a resync that supersedes a pre-clear hydrate routes post-cut frames by its own snapshot's thread id", async () => {
    const fake = connectFakeClient();
    const cut = { reached: false };
    const resyncReads: Array<(response: ThreadReadResponse) => void> = [];
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) return readResponse("ref_a");
      return new Promise<ThreadReadResponse>((resolve) => resyncReads.push(resolve));
    });
    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { id: "thr_cleared", turns: [] })));

    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.threadId).toBe("thr_ref_a");

    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushUntil(() => resyncReads.length === 1);

    await threadsStore.getState().clearThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.threadId).toBe("thr_cleared");

    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_cleared", ref: "ref_a" } });
    await flushUntil(() => resyncReads.length === 2);

    const snapshotTurn = (): boolean =>
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.some((turn) => turn.id === "turn_9") === true;
    resyncReads[1]?.(
      markResponseCut(
        readResponse("ref_a", {
          id: "thr_cleared",
          turns: [{ id: "turn_9", status: "completed", itemsView: "full", items: [] }],
        }),
        cut,
      ),
    );
    await emitAtResponseCut(
      cut,
      "ref_a",
      () =>
        fake.emitNotification({
          method: "thread/status/changed",
          params: { threadId: "thr_cleared", ref: "ref_a", status: { type: "active", activeFlags: ["streaming"] } },
        }),
      snapshotTurn,
    );
    await flushUntil(snapshotTurn);

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_9"]);
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toHaveLength(1);

    threadsStore.getState().releaseThread("ref_a");
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

  test("late A rejection cannot remove or reject B's newer pending initial hydrate", async () => {
    const a = connectFakeClient();
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
    const fake = connectFakeClient();
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
    const fake = connectFakeClient();
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

    // The single natural release must fully untrack it. releaseThread()
    // only removes the ref from `threads` on the branch where its refcount
    // was exactly 1 going in — so this passing is itself proof the failed
    // attempt never left a second claim behind.
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    // Further evidence refCounts (module-private, unreachable directly from
    // this test) reads back at true zero rather than negative or stale: a
    // brand new cycle behaves exactly as if the ref had never been touched
    // before — one fresh read, one release fully untracks it again.
    await threadsStore.getState().ensureThread("ref_a");
    expect(readCount).toBe(3);
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("an owner joining a shared read mid-flight shares its one retry and keeps its own claim", async () => {
    const fake = connectFakeClient();
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
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("a ref released before its in-flight hydrate resolves is not resurrected", async () => {
    const fake = connectFakeClient();
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
      const original = connectFakeClient();
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
    const original = connectFakeClient();
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
    const fake = connectFakeClient();
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

    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("a retired ensure rejection does not consume a replacement lifecycle claim", async () => {
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

// Lean child watches are refcounted independently of real panes.
describe("useThreadsStore.watchThread", () => {
  test("an initial watched snapshot supersedes notifications buffered before its response", async () => {
    const fake = connectFakeClient();
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );

    const watching = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    expect(resolveRead).not.toBeNull();

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(readResponse("ref_a", { status: { type: "active" } }));
    await watching;

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("a watched refresh preserves a live active turn omitted by the snapshot cut", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () =>
      versionedReadResponse("ref_a", {
        turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
      }),
    );

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    a.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 }],
      },
    });
    const liveWatched = threadsStore.getState().watchedThreads.get("ref_a");
    expect(liveWatched?.turns.map((turn) => turn.id)).toEqual(["turn_m5", "turn_m6"]);

    const b = new FakeClient("ready");
    const cut = { reached: false };
    let resolveRefresh: ((response: ThreadReadResponse) => void) | null = null;
    let refreshRequestGeneration: number | undefined;
    b.on("thread/read", (params) => {
      refreshRequestGeneration = (params as { requestGeneration?: number } | undefined)?.requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => (resolveRefresh = resolve));
    });
    connectionStore.getState().connect(b);
    await flushUntil(() => resolveRefresh !== null);
    const finishRefresh = resolveRefresh as unknown as (response: ThreadReadResponse) => void;
    finishRefresh(
      markResponseCut(
        {
          ...versionedReadResponse(
            "ref_a",
            {
              turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
              evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
            },
            1,
          ),
          ...(refreshRequestGeneration !== undefined ? { requestGeneration: refreshRequestGeneration } : {}),
        },
        cut,
      ),
    );
    await emitAtResponseCut(
      cut,
      "ref_a",
      () => {
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
                ...{
                  type: "agentMessage",
                  id: "item_assistant_m6",
                  turnId: "turn_m6",
                  text: "skillguard watched turn complete",
                  status: "completed",
                },
                turnId: "turn_m6",
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
            turns: [{ id: "turn_m6", status: "completed", itemsView: "" }],
          },
        });
      },
      // A versioned refresh's own beginWatchedHydration bumps and republishes
      // the model's issuedGeneration bookkeeping the instant the refresh
      // starts (issuedGenerationFor), before the network round trip even
      // begins - a legitimate content-preserving republish (a new model
      // object, the SAME turns array), not the race this checks for. Compare
      // turns, not the whole model, so this still detects the real content
      // update the response cut race is about.
      () => threadsStore.getState().watchedThreads.get("ref_a")?.turns !== liveWatched?.turns,
    );

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_m5", "turn_m6"]);
    expect(model?.turns[1]?.items[0]?.text).toBe("skillguard watched turn complete");
    expect(model?.turns[1]?.status).toBe("completed");
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("a watched refresh does not preserve a same-numbered turn from a replaced instance", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () =>
      readResponse("ref_a", {
        turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
      }),
    );

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    a.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 }],
      },
    });
    const liveWatched = threadsStore.getState().watchedThreads.get("ref_a");

    const b = new FakeClient("ready");
    b.on("thread/read", () =>
      readResponse("ref_a", {
        turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
        evener: {
          ref: "ref_a",
          instanceId: "replacement-instance",
          capabilities: CAPABILITIES,
          queue: { revision: 0 },
          activeTurnId: "turn_m6",
        },
      }),
    );
    connectionStore.getState().connect(b);
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a") !== liveWatched);

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_m5"]);
  });

  test("restarts a pending initial watched hydrate on a client swap and waits for the new client's model", async () => {
    const a = connectFakeClient();
    const aRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    a.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (aRead.resolve = resolve)));

    const watching = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => aRead.resolve !== null);

    const b = new FakeClient("ready");
    const bRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    b.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (bRead.resolve = resolve)));
    connectionStore.getState().connect(b);
    await flushUntil(() => bRead.resolve !== null);

    let settled = false;
    void watching.then(() => {
      settled = true;
    });
    aRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_a", status: "completed", itemsView: "full", items: [] }] }),
    );
    await flushUntil(() => settled);

    expect(settled).toBe(false);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    bRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] }),
    );
    await watching;

    expect(a.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(b.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(b.calls[0]?.params).toEqual({
      ref: "ref_a",
      includeTurns: false,
      itemsView: "full",
      subscribe: true,
      replaceSubscription: false,
      itemLimit: 40,
    });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_b");

    b.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.status).toEqual({ type: "active" });
    a.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "idle" } },
    });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.status).toEqual({ type: "active" });

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("re-hydrates a watched same-client epoch from its authoritative response cut", async () => {
    const fake = connectFakeClient();
    const reads: Array<(response: ThreadReadResponse) => void> = [];
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => reads.push(resolve)));
    const { authoritativeSnapshot, completion, turnCompleted } = sameEpochReconnectFixture();

    const watching = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => reads.length === 1);

    fake.emitStateChange("reconnecting");
    fake.emitNotification(completion);
    fake.emitNotification(turnCompleted);
    fake.emitReady();
    await flushUntil(() => reads.length === 2);
    fake.emitReady(); // same-ready duplicate must not start a third hydration
    expect(reads).toHaveLength(2);

    // B publishes before old A settles. The watch caller still awaiting A
    // must observe B after A later resolves, without letting A overwrite it.
    reads[1]!(authoritativeSnapshot);
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.status === "completed");
    reads[0]!(authoritativeSnapshot);
    await watching;

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.activeTurnId).toBeUndefined();
    expect(model?.turns[0]?.status).toBe("completed");
    expect(model?.turns[0]?.items[0]?.output).toBe("done");
    expect(model?.turns[0]?.items).toHaveLength(1);
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("hydrates via thread/read with includeTurns:false and routes a subsequent matching notification", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      expect(params).toEqual({
        ref: "ref_a",
        includeTurns: false,
        itemsView: "full",
        subscribe: true,
        replaceSubscription: false,
        itemLimit: 40,
      });
      return readResponse("ref_a");
    });

    await threadsStore.getState().watchThread("ref_a");

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.threadId).toBe("thr_ref_a");

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("a second watchThread(ref) does not re-read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    await threadsStore.getState().watchThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().watchThread("ref_a")).rejects.toThrow(/no client connected/i);
  });

  // A swap onto a client that is not ready yet bumps the ready epoch and wires
  // the new client without running handleReady - nothing re-reads, so the old
  // client's in-flight watched read still owns the current pending entry and
  // its watcher still holds its claim. Both checks either side of the epoch
  // fence therefore pass, and the epoch is the only thing left that says this
  // snapshot was cut on a connection nobody is subscribed to any more.
  test("a watched read resolving after a client swap publishes nothing until the live client answers", async () => {
    const a = connectFakeClient();
    const staleRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    a.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (staleRead.resolve = resolve)));

    const watching = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    await flushUntil(() => staleRead.resolve !== null);

    const b = new FakeClient("connecting");
    b.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_live", status: "completed", itemsView: "full", items: [] }] }),
    );
    connectionStore.getState().connect(b);

    staleRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_stale", status: "completed", itemsView: "full", items: [] }] }),
    );
    // The publish decision after a resolved read is microtasks only, and a task
    // yield by spec runs after the microtask checkpoint drains completely - so
    // this is the decision having been made, not a budget being waited out.
    await settleCallerContinuations();
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    b.emitReady();
    await watching;

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_live");
    expect(a.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a failed watched read keeps the watcher claim and leaves the ref untracked until release", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      throw new Error("boom");
    });

    const watching = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    expect(readAttempts).toBe(1);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    threadsStore.getState().releaseWatchedThread("ref_a");
    await watching;
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);
    expect(readAttempts).toBe(1);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("same-ready watched read failure retries while watcher ownership remains", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) throw new RequestTimeoutError("watched thread/read timed out");
      return readResponse("ref_a");
    });

    const watching = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    // The watcher must be parked on its lifecycle before the retry fires - see
    // the ensureThread cases for why, and what happens without this.
    await settleCallerContinuations();

    expect(readAttempts).toBe(1);
    expect(scheduledHydrationRetries).toHaveLength(1);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    runScheduledHydrationRetry();
    await watching;

    expect(readAttempts).toBe(2);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    expect(scheduledHydrationRetries).toHaveLength(1);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.threadId).toBe("thr_ref_a");
    // The watched lifecycle is its own owner kind: the real-pane map stays out
    // of it entirely.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("watchThread and ensureThread are refcounted independently - releasing one never affects the other's tracking", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);
    const scope = turnScopeKey("ref_a", "turn_1");
    upsertSubagentRow(scope, { rowKey: "dlg:1", resultPreview: "" });
    const { result: row } = renderHook(() => useSubagentRow(scope, "dlg:1"));

    act(() => threadsStore.getState().releaseThread("ref_a"));
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true); // the watch survives
    expect(row.current).toBeUndefined();

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a notification is delivered to both a real pane and a watch on the same ref", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    vi.spyOn(Date, "now").mockReturnValue(9_000_000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([9_000_000]);
  });

  test("releaseWatchedThread refcounts watchers; stops tracking only when the last watcher releases", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().watchThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("releasing an untracked watch is a harmless no-op", () => {
    expect(() => threadsStore.getState().releaseWatchedThread("never_watched")).not.toThrow();
  });

  test("a watched ref is not re-subscribed on reconnect once released, same as a real pane", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().watchThread("ref_a");
    threadsStore.getState().releaseWatchedThread("ref_a");

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => fake.calls.filter((c) => c.method === "thread/read").length > 1);

    const refsReRead = fake.calls
      .filter((c) => c.method === "thread/read")
      .slice(1)
      .map((c) => (c.params as { ref: string }).ref);
    expect(refsReRead).toEqual([]);
  });

  test("onReady re-subscribes every tracked watch additively, replacing its model wholesale, independent of ensureThread's own refs", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      const ref = (params as { ref: string }).ref;
      const turns = readCount <= 1 ? [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] : [];
      return readResponse(ref, { turns });
    });

    await threadsStore.getState().watchThread("ref_a");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a")?.turns.length === 0);

    const readCallsAfterReconnect = fake.calls.filter((c) => c.method === "thread/read").slice(1);
    expect(readCallsAfterReconnect).toHaveLength(1);
    expect(readCallsAfterReconnect[0]?.params).toEqual({
      ref: "ref_a",
      includeTurns: false,
      itemsView: "full",
      subscribe: true,
      replaceSubscription: false,
      itemLimit: 40,
    });
  });

  test("a thread resync preserves a watched ref's rich authoritative replacement", async () => {
    const fake = connectFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) {
        return readResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_before", status: "completed", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: false }, queue: { revision: 0 } },
        });
      }
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = resolve;
      });
    });
    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);

    const reads = fake.calls.filter((call) => call.method === "thread/read");
    expect(reads).toHaveLength(2);
    expect(reads[1]?.params).toMatchObject({ ref: "ref_a", includeTurns: true });

    fake.emitNotification({
      method: "thread/status/changed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        status: { type: "active", activeFlags: ["streaming"] },
      },
    });
    replacementRead.resolve?.(
      readResponse("ref_a", {
        status: { type: "active", activeFlags: ["streaming"] },
        turns: [{ id: "turn_after", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a")?.capabilities.queue === true);

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.capabilities.queue).toBe(true);
    expect(model?.turns[0]?.id).toBe("turn_after");
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
  });

  test("repeated thread resyncs keep rich watched hydration newest-wins in one epoch", async () => {
    const fake = connectFakeClient();
    await establishMembership(fake);
    const reads: Array<{
      includeTurns: boolean;
      resolve: (response: ThreadReadResponse) => void;
    }> = [];
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          reads.push({ includeTurns: (params as { includeTurns: boolean }).includeTurns, resolve });
        }),
    );

    const watching = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
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

    expect(reads.map((read) => read.includeTurns)).toEqual([true, true, true]);
    reads[2]?.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_newest", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id === "turn_newest");
    reads[1]?.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_superseded", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: false }, queue: { revision: 0 } },
      }),
    );
    reads[0]?.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_initial", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: false }, queue: { revision: 0 } },
      }),
    );
    await watching;

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_newest");
    expect(model?.capabilities.queue).toBe(true);
  });

  test("a rich watched hydration rejection follows its same-epoch resync replacement", async () => {
    const fake = connectFakeClient();
    await establishMembership(fake);
    const reads: Array<{
      includeTurns: boolean;
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: Error) => void;
    }> = [];
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve, reject) => {
          reads.push({ includeTurns: (params as { includeTurns: boolean }).includeTurns, resolve, reject });
        }),
    );

    const watching = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    await flushUntil(() => reads.length === 1);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 2);

    let rejected = false;
    void watching.catch(() => {
      rejected = true;
    });
    reads[0]!.reject(new Error("superseded initial rich read"));
    await flushUntil(() => rejected);

    expect(rejected).toBe(false);
    expect(reads.map((read) => read.includeTurns)).toEqual([true, true]);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    reads[1]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_authoritative", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await watching;

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_authoritative");
    expect(model?.capabilities.queue).toBe(true);
  });

  test("a rich watched lifecycle follows a newest resync after its failed predecessor cleared ownership", async () => {
    const fake = connectFakeClient();
    await establishMembership(fake);
    const reads: Array<{
      includeTurns: boolean;
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: Error) => void;
    }> = [];
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve, reject) => {
          reads.push({ includeTurns: (params as { includeTurns: boolean }).includeTurns, resolve, reject });
        }),
    );

    const watching = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    await flushUntil(() => reads.length === 1);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 2);

    reads[1]!.reject(new Error("failed rich replacement B"));
    await flushUntil(() => false);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 3);

    let rejected = false;
    void watching.catch(() => {
      rejected = true;
    });
    reads[0]!.reject(new Error("superseded initial rich A"));
    await flushUntil(() => rejected);

    expect(rejected).toBe(false);
    expect(reads.map((read) => read.includeTurns)).toEqual([true, true, true]);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    reads[2]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_authoritative", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await watching;

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_authoritative");
    expect(model?.capabilities.queue).toBe(true);
  });

  test("a published newest rich watched resync survives later superseded rejections", async () => {
    const fake = connectFakeClient();
    await establishMembership(fake);
    const reads: Array<{
      includeTurns: boolean;
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: Error) => void;
    }> = [];
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve, reject) => {
          reads.push({ includeTurns: (params as { includeTurns: boolean }).includeTurns, resolve, reject });
        }),
    );

    const watching = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
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

    expect(reads.map((read) => read.includeTurns)).toEqual([true, true, true]);
    reads[2]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_authoritative", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id === "turn_authoritative");
    await Promise.resolve();
    await Promise.resolve();

    reads[1]!.reject(new Error("superseded rich replacement B"));
    await Promise.resolve();
    await Promise.resolve();
    reads[0]!.reject(new Error("superseded initial rich A"));
    await watching;

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_authoritative");
    expect(model?.capabilities.queue).toBe(true);
  });

  test("a watch released before its in-flight hydrate resolves is not resurrected", async () => {
    const fake = connectFakeClient();
    const box: { resolveRead: ((resp: ThreadReadResponse) => void) | null } = { resolveRead: null };
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (box.resolveRead = resolve)));

    const watching = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => box.resolveRead !== null);
    threadsStore.getState().releaseWatchedThread("ref_a");
    box.resolveRead?.(readResponse("ref_a"));
    await watching;

    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  // yd16 §4.2: the expanded subagent card watches with { includeTurns: true }
  // so the Activity feed has the child's turn history. A read scripted to
  // return a turn only when includeTurns is true lets these tests prove turns
  // actually crossed the wire, not just that a flag was threaded through.
  function turnsAwareRead(fake: FakeClient): void {
    fake.on("thread/read", (params) => {
      const includeTurns = (params as { includeTurns: boolean }).includeTurns;
      return readResponse("ref_a", {
        turns: includeTurns ? [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] : [],
      });
    });
  }

  test("watchThread(ref, { includeTurns: true }) hydrates with turns populated", async () => {
    const fake = connectFakeClient();
    turnsAwareRead(fake);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });

    const call = fake.calls.find((c) => c.method === "thread/read");
    expect(call?.params).toMatchObject({ includeTurns: true });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
  });

  test("a lean watch followed by a { includeTurns: true } call upgrades: turns become populated despite the .has(ref) short-circuit", async () => {
    const fake = connectFakeClient();
    turnsAwareRead(fake);

    await threadsStore.getState().watchThread("ref_a"); // lean first: no turns
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(0);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true }); // upgrade re-read
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
    // The upgrade bypasses the .has(ref)/inflight-dedup short-circuits (those
    // are for concurrent first-mounts), so a genuine second read fired.
    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(2);
  });

  test("a concurrent rich watch does not share an in-flight lean hydrate or lose its turns", async () => {
    const fake = connectFakeClient();
    await establishMembership(fake);
    const pending: Array<{
      includeTurns: boolean;
      resolve: (response: ThreadReadResponse) => void;
    }> = [];
    fake.on("thread/read", (params) => {
      return new Promise<ThreadReadResponse>((resolve) => {
        pending.push({ includeTurns: (params as { includeTurns: boolean }).includeTurns, resolve });
      });
    });

    const lean = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 1);
    const rich = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    await flushUntil(() => pending.length === 2);

    expect(pending.map((request) => request.includeTurns)).toEqual([false, true]);

    pending[1]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_rich", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    pending[0]!.resolve(readResponse("ref_a", { turns: [] }));
    await Promise.all([lean, rich]);

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a rejected shared hydrate keeps the mounted watcher claim and retries for it", async () => {
    const fake = connectFakeClient();
    const pending: Array<{
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: Error) => void;
    }> = [];
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve, reject) => {
          pending.push({ resolve, reject });
        }),
    );

    const first = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 1);
    const second = threadsStore.getState().watchThread("ref_a");
    expect(pending).toHaveLength(1);

    // The first watcher has already unmounted; the second watcher still owns
    // its claim when the shared request fails, so the read is retried on its
    // behalf rather than abandoned.
    threadsStore.getState().releaseWatchedThread("ref_a");
    pending[0]!.reject(new Error("hydrate failed"));
    await flushUntil(() => scheduledHydrationRetries.length === 1);
    expect(scheduledHydrationRetries).toHaveLength(1);

    runScheduledHydrationRetry();
    await flushUntil(() => pending.length === 2);
    pending[1]!.resolve(readResponse("ref_a"));
    await Promise.all([first, second]);

    // Exactly one claim survived the failure: had the rejection consumed the
    // still-mounted watcher's claim, nothing would have been left to retry for
    // and the model could not be tracked at all.
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a rich upgrade wins over a slower lean reconnect hydrate", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    const pending: Array<{
      includeTurns: boolean;
      resolve: (response: ThreadReadResponse) => void;
    }> = [];
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return readResponse("ref_a", { turns: [] });
      return new Promise<ThreadReadResponse>((resolve) => {
        pending.push({ includeTurns: (params as { includeTurns: boolean }).includeTurns, resolve });
      });
    });

    await threadsStore.getState().watchThread("ref_a");
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => pending.length === 1);

    const rich = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    expect(pending).toHaveLength(1); // Rich demand waits for the reconnect membership barrier.
    pending[0]!.resolve(readResponse("ref_a", { turns: [] }));
    await flushUntil(() => pending.length === 2);
    expect(pending.map((request) => request.includeTurns)).toEqual([false, true]);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);

    pending[1]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_reconnect_rich", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    await rich;

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_reconnect_rich");

    threadsStore.getState().releaseWatchedThread("ref_a");
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a new watcher starts a fresh hydrate after the previous lifecycle is released", async () => {
    const fake = connectFakeClient();
    await establishMembership(fake);
    const pending: Array<(response: ThreadReadResponse) => void> = [];
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => pending.push(resolve)));

    const first = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 1);
    threadsStore.getState().releaseWatchedThread("ref_a");

    const second = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 2);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);

    pending[1]!(
      readResponse("ref_a", { turns: [{ id: "turn_new", status: "completed", itemsView: "full", items: [] }] }),
    );
    await second;
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_new");

    pending[0]!(
      readResponse("ref_a", { turns: [{ id: "turn_old", status: "completed", itemsView: "full", items: [] }] }),
    );
    await first;
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_new");

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("monotonic: once turns are loaded, a later lean watch does not downgrade them away", async () => {
    const fake = connectFakeClient();
    turnsAwareRead(fake);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);

    await threadsStore.getState().watchThread("ref_a"); // a lean watcher joins
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1); // still there
    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1); // no re-read: already rich
  });

  test("releasing the last watcher clears the per-ref includeTurns flag so a fresh watch starts lean again", async () => {
    const fake = connectFakeClient();
    turnsAwareRead(fake);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    await threadsStore.getState().watchThread("ref_a"); // fresh lean watch
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(0);
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

for (const state of ["blockedUnknown", "submitting"] as const) {
  test(`incompatible refresh preserves ${state} until a compatible snapshot arrives`, async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "upgrade-pending" });
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    await storage.markAttempted(record.clientMutationId);
    if (state === "blockedUnknown") await storage.markUnknown(record.clientMutationId, state);
    storage.close();
    const fake = connectFakeClient("connecting");
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().refreshThread("ref_a");
    const inspector = new MutationOutboxIndexedDB();
    expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
    expect((await inspector.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    status = "notLoaded";
    await threadsStore.getState().refreshThread("ref_a");
    expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
    expect((await inspector.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    status = "idle";
    await threadsStore.getState().refreshThread("ref_a");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    inspector.close();
  });
}

test("compatible refresh wins over a delayed incompatible receipt write", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "delayed-upgrade" });
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  await storage.markAttempted(record.clientMutationId);
  const writeStarted = deferred<void>();
  const releaseWrite = deferred<void>();
  const markUnknown = storage.markUnknown.bind(storage);
  vi.spyOn(storage, "markUnknown").mockImplementation(async (id, state) => {
    writeStarted.resolve();
    await releaseWrite.promise;
    return markUnknown(id, state);
  });
  const settled = deferred<void>();
  const settleReceipt = storage.settleReceipt.bind(storage);
  vi.spyOn(storage, "settleReceipt").mockImplementation(async (id, projectionState) => {
    const result = await settleReceipt(id, projectionState);
    if (id === record.clientMutationId) settled.resolve();
    return result;
  });
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  let status = "restartRequired";
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
  const receipt = deferred<{ receipt: ReturnType<typeof mutationReceipt> }>();
  fake.on("turn/queue", () => receipt.promise);
  fake.emitReady();
  const firstRead = threadsStore.getState().ensureThread("ref_a");
  await writeStarted.promise;
  status = "idle";
  const refresh = threadsStore.getState().refreshThread("ref_a");
  await flushIndexedDBUntil(() => threadsStore.getState().threads.get("ref_a")?.status.type === "idle");
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
  releaseWrite.resolve();
  await Promise.all([firstRead, refresh]);
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
  expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("submitting");
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  receipt.resolve({ receipt: mutationReceipt(record.clientMutationId) });
  await settled.promise;
});

test.each([false, true])(
  "stopped refresh retains an overlapping blocking obligation (reconnect=%s)",
  async (reconnect) => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "overlapping-stop" });
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    await storage.markAttempted(record.clientMutationId);
    const scanStarted = deferred<void>();
    const releaseScan = deferred<void>();
    const listOutbox = storage.listOutbox.bind(storage);
    let held = false;
    vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
      const records = await listOutbox(ref);
      if (ref && !held && threadsStore.getState().threads.get(ref)?.status.type === "restartRequired") {
        held = true;
        scanStarted.resolve();
        await releaseScan.promise;
      }
      return records;
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    const firstRead = threadsStore.getState().ensureThread("ref_a");
    await scanStarted.promise;
    status = "notLoaded";
    if (reconnect) {
      fake.emitStateChange("reconnecting");
      fake.emitReady();
    }
    const refresh = threadsStore.getState().refreshThread("ref_a");
    await flushIndexedDBUntil(() => threadsStore.getState().threads.get("ref_a")?.status.type === "notLoaded");
    expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("notLoaded");
    releaseScan.resolve();
    await Promise.all([firstRead, refresh]);
    expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    status = "idle";
    await threadsStore.getState().refreshThread("ref_a");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  },
);

for (const failure of ["listOutbox", "markUnknown"] as const) {
  test.each([false, true])(
    `restart blocking survives ${failure} failure and stopped refresh (overlap=%s)`,
    async (overlap) => {
      const storage = new MutationOutboxIndexedDB({ createMutationId: () => "failed-restart-block" });
      const record = await storage.enqueueIntent({
        targetRef: "ref_a",
        method: "turn/queue",
        payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
        attachments: [],
        optimisticDisplay: { text: "sentinel" },
      });
      await storage.markAttempted(record.clientMutationId);
      const blockingStarted = deferred<void>();
      const releaseFailure = deferred<void>();
      let faultEnabled = true;
      let held = false;
      const failStorage = async () => {
        if (!held) {
          held = true;
          blockingStarted.resolve();
          await releaseFailure.promise;
        }
        throw new DOMException("IndexedDB transaction aborted", "AbortError");
      };
      if (failure === "listOutbox") {
        const listOutbox = storage.listOutbox.bind(storage);
        vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
          if (
            faultEnabled &&
            ref &&
            (held || threadsStore.getState().threads.get(ref)?.status.type === "restartRequired")
          )
            await failStorage();
          return listOutbox(ref);
        });
      } else {
        const markUnknown = storage.markUnknown.bind(storage);
        vi.spyOn(storage, "markUnknown").mockImplementation(async (id, state) => {
          if (faultEnabled && state === "blockedUnknown") await failStorage();
          return markUnknown(id, state);
        });
      }
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      let status = "restartRequired";
      fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
      fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
      fake.emitReady();
      const firstRead = threadsStore
        .getState()
        .ensureThread("ref_a")
        .catch(() => undefined);
      await blockingStarted.promise;
      let stopped: Promise<unknown> | undefined;
      if (overlap) {
        status = "notLoaded";
        stopped = threadsStore
          .getState()
          .refreshThread("ref_a")
          .catch(() => undefined);
        await flushIndexedDBUntil(() => threadsStore.getState().threads.get("ref_a")?.status.type === "notLoaded");
      }
      releaseFailure.resolve();
      await firstRead;
      status = "notLoaded";
      await (stopped ??
        threadsStore
          .getState()
          .refreshThread("ref_a")
          .catch(() => undefined));
      expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("submitting");
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
      faultEnabled = false;
      await threadsStore.getState().refreshThread("ref_a");
      expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
      status = "idle";
      await threadsStore.getState().refreshThread("ref_a");
      await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    },
  );
}

test("periodic discovery recovers failed compatible reconciliation after storage returns", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "recovery-after-storage" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    let faultEnabled = false;
    let failures = 0;
    const listOutbox = storage.listOutbox.bind(storage);
    vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
      if (
        faultEnabled &&
        ref &&
        (failures > 0 || threadsStore.getState().threads.get(ref)?.status.type === "restartRequired")
      ) {
        failures += 1;
        throw new DOMException("IndexedDB transaction aborted", "AbortError");
      }
      return listOutbox(ref);
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await settleCallerContinuations();
    faultEnabled = true;
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(failures).toBeGreaterThan(0);
    status = "idle";
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    expect(failures).toBeGreaterThan(1);
    expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(true);
    faultEnabled = false;
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(false);
  } finally {
    vi.useRealTimers();
  }
});

test("periodic discovery recovers reconciliation after the final durable record settles", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "last-record" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    let faultEnabled = false;
    const listOutbox = storage.listOutbox.bind(storage);
    vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
      const records = await listOutbox(ref);
      if (faultEnabled && ref && records.length === 0)
        throw new DOMException("IndexedDB transaction aborted", "AbortError");
      return records;
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let compatible = false;
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        status: { type: compatible ? "idle" : "restartRequired" },
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 1, clientMutationIds: compatible ? ["last-record"] : [] },
        },
      }),
    );
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await settleCallerContinuations();
    compatible = true;
    faultEnabled = true;
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(await storage.listTargetRefs()).toEqual([]);
    expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(true);
    faultEnabled = false;
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => !threadsStore.getState().mutationReconciliationFailures.has("ref_a"));
    expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(false);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  } finally {
    vi.useRealTimers();
  }
});

test.each(["active", "idle"])(
  "recovered %s descendant without uncertain messages permits new sends",
  async (status) => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "fresh-descendant-send" });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let snapshot = readResponse("ref_a", { status: { type: "restartRequired" } });
    fake.on("thread/read", () => snapshot);
    const delivered = deferred<void>();
    fake.on("turn/queue", (params) => {
      delivered.resolve();
      return { receipt: mutationReceipt(params.clientMutationId) };
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    snapshot = readResponse("ref_a", { status: { type: status } });
    Object.assign(snapshot.thread.evener, { mutationStateAuthoritative: false, kind: "subagent" });
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(false);
    await threadsStore.getState().queue("ref_a", "new message after restart");
    await delivered.promise;
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  },
);
test.each(["active", "idle"])(
  "recovered %s descendant with a never-attempted intent permits delivery",
  async (status) => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "unsent-descendant-send" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "unsent" }] },
      attachments: [],
      optimisticDisplay: { text: "unsent" },
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let snapshot = readResponse("ref_a", { status: { type: "restartRequired" } });
    fake.on("thread/read", () => snapshot);
    const delivered = deferred<void>();
    fake.on("turn/queue", (params) => {
      delivered.resolve();
      return { receipt: mutationReceipt(params.clientMutationId) };
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    snapshot = readResponse("ref_a", { status: { type: status } });
    Object.assign(snapshot.thread.evener, { mutationStateAuthoritative: false, kind: "subagent" });
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(false);

    await delivered.promise;
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  },
);

for (const status of ["active", "idle"]) {
  test(`saved ${status} delegate snapshots do not release uncertain mutations`, async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "delegate-uncertain" });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let snapshot = readResponse("ref_a", { status: { type: "restartRequired" } });
    fake.on("thread/read", () => snapshot);
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    await storage.markAttempted(record.clientMutationId);
    snapshot = readResponse("ref_a", { status: { type: status } });
    Object.assign(snapshot.thread.evener, { mutationStateAuthoritative: false, kind: "subagent" });
    await threadsStore.getState().refreshThread("ref_a");
    expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    snapshot = readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, clientMutationIds: [record.clientMutationId] },
      },
    });
    await threadsStore.getState().refreshThread("ref_a");
    expect(await storage.getOutbox(record.clientMutationId)).toBeUndefined();
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  });
}

test.each(["active", "idle"])("manual retry stays blocked after reload of a saved %s delegate", async (status) => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "saved-manual-retry" });
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  await storage.markUnknown(record.clientMutationId, "blockedUnknown");
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const saved = readResponse("ref_a", { status: { type: status } });
  saved.thread.evener.mutationStateAuthoritative = false;
  fake.on("thread/read", () => saved);
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await settleCallerContinuations();
  expect(threadsStore.getState().restartBlockingObligations.size).toBe(0);
  expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
  expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
});

test.each(["active", "idle"].flatMap((status) => [true, false].map((accepted) => ({ status, accepted }))))(
  "periodic discovery resumes authority checks for saved $status delegates, accepted=$accepted",
  async ({ status, accepted }) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    // Per-case private IndexedDB factory, the same pattern this file uses for
    // its other directly driven storage cases. A fresh factory is its own
    // in-memory database namespace: this test awaits only the FIRST settle
    // call, so its reconciliation tail can still be running when the case ends,
    // and a shared (even uniquely named) database let that tail linger in the
    // worker's factory. A private factory means the tail cannot write into a
    // sibling case's records, a rerun cannot inherit a stale `delegate-periodic`
    // record (whose duplicate `add` would mask the real failure as a
    // ConstraintError), and no database is left behind in the shared global
    // factory that the suite's deleteMutationDatabase() never touches.
    const indexedDB = new IDBFactory();
    const databaseName = `evener-mutation-outbox-periodic-${status}-${accepted}`;
    try {
      const storage = new MutationOutboxIndexedDB({
        indexedDB,
        databaseName,
        createMutationId: () => "delegate-periodic",
      });
      const record = await storage.enqueueIntent({
        targetRef: "ref_a",
        method: "turn/queue",
        payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
        attachments: [],
        optimisticDisplay: { text: "sentinel" },
      });
      await storage.markAttempted(record.clientMutationId);
      const settled = deferred<void>();
      const settleApplied = storage.settleApplied.bind(storage);
      vi.spyOn(storage, "settleApplied").mockImplementation(async (...args) => {
        const result = await settleApplied(...args);
        if (args[0] === record.clientMutationId && result) settled.resolve();
        return result;
      });
      const settleReceipt = storage.settleReceipt.bind(storage);
      vi.spyOn(storage, "settleReceipt").mockImplementation(async (...args) => {
        const result = await settleReceipt(...args);
        if (args[0] === record.clientMutationId) settled.resolve();
        return result;
      });
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      let recovered = false;
      fake.on("thread/read", () => {
        const response = readResponse("ref_a", { status: { type: status } });
        response.thread.evener.mutationStateAuthoritative = recovered;
        if (recovered && accepted) response.thread.evener.queue.clientMutationIds = [record.clientMutationId];
        return response;
      });
      fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
      fake.emitReady();
      await threadsStore.getState().ensureThread("ref_a");
      // ensureThread resolves on model publication; refreshThread also waits
      // for durable reconciliation before the single simulated recovery tick.
      await threadsStore.getState().refreshThread("ref_a");
      expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
      recovered = true;
      await vi.advanceTimersByTimeAsync(2000);
      await settled.promise;
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      expect(await storage.getOutbox(record.clientMutationId)).toBeUndefined();
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(accepted ? 0 : 1);
    } finally {
      vi.useRealTimers();
    }
  },
);

test.each(["daemonRestartRequired", "persistenceUnavailable"])(
  "blocked %s mutation invalidates earlier authority on the same connection",
  async (cause) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const storage = new MutationOutboxIndexedDB({ createMutationId: () => "authority-lost" });
      const blocked = deferred<void>();
      const markUnknown = storage.markUnknown.bind(storage);
      vi.spyOn(storage, "markUnknown").mockImplementation(async (...args) => {
        const result = await markUnknown(...args);
        blocked.resolve();
        return result;
      });
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      const fresh = deferred<ThreadReadResponse>();
      let reads = 0;
      fake.on("thread/read", () => {
        reads++;
        return reads === 1 ? readResponse("ref_a", { status: { type: "idle" } }) : fresh.promise;
      });
      fake.on("turn/queue", (params) => {
        throw new WireError("owner unavailable", -32014, {
          evenerErrorInfo: "mutationOutcome",
          clientMutationId: params.clientMutationId,
          mutationOutcome: "unknown",
          retryDisposition: "blocked",
          cause,
        });
      });
      fake.emitReady();
      await threadsStore.getState().ensureThread("ref_a");
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      await threadsStore.getState().queue("ref_a", "preserve this message");
      await blocked.promise;
      await settleCallerContinuations();
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(false);
      expect(await retryBlockedMutation("authority-lost")).toBe(false);
      await vi.advanceTimersByTimeAsync(2000);
      await flushIndexedDBUntil(() => reads === 2);
      expect(reads).toBe(2);
      const response = readResponse("ref_a", { status: { type: "restartRequired" } });
      response.thread.evener.mutationStateAuthoritative = false;
      fresh.resolve(response);
      await flushIndexedDBUntil(() => threadsStore.getState().threads.get("ref_a")?.status.type === "restartRequired");
      expect((await storage.getOutbox("authority-lost"))?.state).toBe("blockedUnknown");
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  },
);

test.each([true, false])(
  "discovery reconciles another tab's blocked send with cached authority, accepted=%s",
  async (accepted) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "other-tab-blocked" });
    const otherTab = new MutationOutboxIndexedDB({ createMutationId: () => "other-tab-blocked" });
    const settled = deferred<void>();
    const settleApplied = storage.settleApplied.bind(storage);
    vi.spyOn(storage, "settleApplied").mockImplementation(async (...args) => {
      const result = await settleApplied(...args);
      settled.resolve();
      return result;
    });
    const settleReceipt = storage.settleReceipt.bind(storage);
    vi.spyOn(storage, "settleReceipt").mockImplementation(async (...args) => {
      const result = await settleReceipt(...args);
      settled.resolve();
      return result;
    });
    try {
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      let reads = 0;
      fake.on("thread/read", () => {
        reads++;
        const response = readResponse("ref_a", { status: { type: "idle" } });
        if (reads > 1 && accepted)
          response.thread.evener.queue = { revision: 1, clientMutationIds: ["other-tab-blocked"] };
        return response;
      });
      let sends = 0;
      fake.on("turn/queue", (params) => {
        if (++sends === 1) throw new RequestTimeoutError("response lost");
        return { receipt: mutationReceipt(params.clientMutationId) };
      });
      fake.emitReady();
      await threadsStore.getState().ensureThread("ref_a");
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      await threadsStore.getState().queue("ref_a", "sentinel");
      await flushIndexedDBUntil(() => sends === 1);
      await settleCallerContinuations();
      await otherTab.markUnknown("other-tab-blocked", "blockedUnknown");
      otherTab.close();
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      await vi.advanceTimersByTimeAsync(2000);
      await flushIndexedDBUntil(() => reads > 1);
      expect(reads).toBe(2);
      await flushIndexedDBUntil(() => accepted || sends === 2);
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(accepted ? 1 : 2);
      await settled.promise;
      expect(await storage.getOutbox("other-tab-blocked")).toBeUndefined();
    } finally {
      otherTab.close();
      vi.useRealTimers();
    }
  },
);

test.each(["accepted", "absent", "unavailable"])(
  "manual retry refreshes authority after another tab blocks a send: %s",
  async (outcome) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "manual-other-tab" });
    const otherTab = new MutationOutboxIndexedDB();
    const fresh = deferred<ThreadReadResponse>();
    try {
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      let reads = 0;
      fake.on("thread/read", () =>
        ++reads === 1 ? readResponse("ref_a", { status: { type: "idle" } }) : fresh.promise,
      );
      let sends = 0;
      fake.on("turn/queue", (params) => {
        if (++sends === 1) throw new RequestTimeoutError("response lost");
        return { receipt: mutationReceipt(params.clientMutationId) };
      });
      fake.emitReady();
      await threadsStore.getState().ensureThread("ref_a");
      await threadsStore.getState().queue("ref_a", "sentinel");
      await flushIndexedDBUntil(() => sends === 1);
      await settleCallerContinuations();
      await otherTab.markUnknown("manual-other-tab", "blockedUnknown");
      otherTab.close();
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      const retry = retryBlockedMutation("manual-other-tab");
      await flushIndexedDBUntil(() => reads === 2);
      expect(reads).toBe(2);
      expect((await storage.getOutbox("manual-other-tab"))?.state).toBe("blockedUnknown");
      expect(sends).toBe(1);
      expect(await retryBlockedMutation("manual-other-tab")).toBe(false);
      const response = readResponse("ref_a", {
        status: { type: outcome === "unavailable" ? "restartRequired" : "idle" },
      });
      if (outcome === "accepted")
        response.thread.evener.queue = { revision: 1, clientMutationIds: ["manual-other-tab"] };
      if (outcome === "unavailable") response.thread.evener.mutationStateAuthoritative = false;
      fresh.resolve(response);
      expect(await retry).toBe(outcome !== "unavailable");
      if (outcome === "absent") await flushIndexedDBUntil(() => sends === 2);
      expect(sends).toBe(outcome === "absent" ? 2 : 1);
      if (outcome === "unavailable")
        expect((await storage.getOutbox("manual-other-tab"))?.state).toBe("blockedUnknown");
    } finally {
      fresh.resolve(readResponse("ref_a", { status: { type: "restartRequired" } }));
      otherTab.close();
      vi.useRealTimers();
    }
  },
);

// RoboRev finding on the reduced branch: a background note save only verifies
// that the blocked row it retries is a note, but the targeted resync it drives
// (handleReady) schedules dispatch for the WHOLE target - so saving a note in
// the background could submit unrelated pending mutations for the same session
// as a side effect. The background path must not dispatch at all; the durable
// outbox's own lifecycle scan owns resending the note.
test("a background note save does not dispatch an unrelated pending mutation", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const storage = new MutationOutboxIndexedDB();
  try {
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
    let sends = 0;
    fake.on("turn/queue", () => {
      sends += 1;
      throw new RequestTimeoutError("response lost");
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    // An unrelated pending send for the same target, left submitting by the
    // first dispatch's lost response.
    await threadsStore.getState().queue("ref_a", "unrelated queued work");
    await flushIndexedDBUntil(() => sends === 1);
    expect(sends).toBe(1);
    // The blocked note the background save is retrying.
    const note = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "notes/human/set",
      payload: { ref: "ref_a", note: "draft", expectedInstanceId: "thr_ref_a" },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.markUnknown(note.clientMutationId, "blockedUnknown");
    expect(await retryBlockedMutation(note.clientMutationId, "backgroundNote")).toBe(true);
    // The save's resync is fully awaited above; this drains anything it may
    // have scheduled. The unrelated record must still be unsent...
    await flushIndexedDBUntil(() => sends >= 2);
    expect(sends).toBe(1);
    // ...and that assertion is not vacuous: the record is still submitting (a
    // lost response leaves it dispatchable - mutationDispatcher's bare "stop"),
    // so the outbox's own periodic discovery scan resends it the moment its
    // interval is advanced. The same drain proving the resend here is what
    // would have proven a dispatch the save armed, had it armed one.
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => sends >= 2);
    expect(sends).toBe(2);
  } finally {
    vi.useRealTimers();
  }
});

// #1716: the background path fences its own dispatch, but the authoritative
// read it drives (handleReady -> refreshTrackedThread) still reconciled the
// WHOLE target: restoreProvenAbsent reopened every blockedUnknown row for the
// ref that the fresh read did not name, so saving a note in the background
// returned an unrelated blocked chat mutation to submitting, where the
// outbox's next lifecycle scan would dispatch it. A background retry may
// reopen only the record it carries.
test("a background note save does not reopen an unrelated blocked record for the same target", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const storage = new MutationOutboxIndexedDB();
  try {
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    // Drain the initial hydration's own reconciliation so the only reopen left
    // to observe belongs to the note retry below.
    await threadsStore.getState().refreshThread("ref_a");
    // An unrelated mutation for the same target, left delivery-uncertain by a
    // lost response.
    const unrelated = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", expectedInstanceId: "thr_ref_a", input: [{ type: "text", text: "unrelated" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "unrelated" }] },
    });
    await storage.markAttempted(unrelated.clientMutationId);
    await storage.markUnknown(unrelated.clientMutationId, "blockedUnknown");
    // The blocked note the background save retries.
    const note = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "notes/human/set",
      payload: { ref: "ref_a", note: "draft", expectedInstanceId: "thr_ref_a" },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.markAttempted(note.clientMutationId);
    await storage.markUnknown(note.clientMutationId, "blockedUnknown");

    expect(await retryBlockedMutation(note.clientMutationId, "backgroundNote")).toBe(true);

    // The reconciliation ran and reopened the note it was asked to retry...
    expect((await storage.getOutbox(note.clientMutationId))?.state).toBe("submitting");
    // ...but the unrelated blocked row for the same target is untouched.
    expect((await storage.getOutbox(unrelated.clientMutationId))?.state).toBe("blockedUnknown");
  } finally {
    vi.useRealTimers();
  }
});

// RoboRev finding on the reduced branch: refreshThread's beforePublish fence is
// checked before publication, but the dispatch it schedules at its tail was
// unconditional. reconcileIdentities is held open here so the Stop lands after
// that fence has already passed; the dispatch must recheck it (as handleReady's
// targeted tail does at the scheduling point) rather than run after a Stop.
test("a Stop during refreshThread's reconciliation is not overtaken by its dispatch", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "refresh-stop" });
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  fake.on("thread/shutdown", () => ({}));
  let sends = 0;
  fake.on("turn/start", (params) => {
    sends += 1;
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  // Drain the initial hydration's own reconciliation before arming the gate, so
  // the held call is unambiguously the refreshed read's.
  await threadsStore.getState().refreshThread("ref_a");
  await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "pending" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "pending" }] },
  });
  const release = deferred<void>();
  let reconciliationStarted!: () => void;
  const started = new Promise<void>((resolve) => {
    reconciliationStarted = resolve;
  });
  const reconcile = MutationDispatcher.prototype.reconcileIdentities;
  const spy = vi.spyOn(MutationDispatcher.prototype, "reconcileIdentities").mockImplementation(async function (
    this: MutationDispatcher,
    ...args
  ) {
    reconciliationStarted();
    await release.promise;
    return reconcile.apply(this, args);
  });
  try {
    const fence = resumeStopFence("ref_a");
    const refresh = threadsStore.getState().refreshThread("ref_a", fence);
    await started;
    // The Stop lands while the refreshed read's reconciliation is in flight,
    // after its beforePublish fence has already been evaluated once.
    await threadsStore.getState().shutdown("ref_a");
    release.resolve();
    await expect(refresh).rejects.toThrow("Stop canceled this pending action");
    await flushIndexedDBUntil(() => sends >= 1);
    expect(sends).toBe(0);
  } finally {
    spy.mockRestore();
  }
});

// RoboRev Medium on d7a264d4 (PR 1393): the beforePublish recheck that rejects
// a Stop-overtaken refreshThread runs only after the refresh has already
// banked what it earned on the way - publication opened the ref's dispatch
// gate, and reconciliation cleared the recovery-blocked obligation the stopped
// snapshot had armed. The rejection left both banked, so the outbox's own later
// discovery scan dispatched the queued mutation anyway, on the strength of a
// refresh the Stop had just canceled. The fence must unwind what it canceled:
// the gate closes and the obligation re-arms before the rejection.
test("a later discovery cannot dispatch what a Stop-canceled refresh earned", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "refresh-stop-discovery" });
  try {
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    // The stopped session the recovery UX opens on: recovery-blocked until a
    // resume proves otherwise - and stopped again once the Stop below lands.
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("thread/shutdown", () => ({}));
    let sends = 0;
    fake.on("turn/start", (params) => {
      sends += 1;
      return {
        receipt: mutationReceipt(params.clientMutationId),
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      };
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    // Drain the stopped snapshot's own reconciliation before arming anything,
    // so the held call below is unambiguously the resumed read's.
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    // A message queued while the session was recovery-blocked, never attempted.
    await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "pending" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "pending" }] },
    });
    // The resume: a fresh authoritative snapshot whose reconciliation is the
    // window the Stop lands in.
    status = "idle";
    const release = deferred<void>();
    let reconciliationStarted!: () => void;
    const started = new Promise<void>((resolve) => {
      reconciliationStarted = resolve;
    });
    const reconcile = MutationDispatcher.prototype.reconcileIdentities;
    const spy = vi.spyOn(MutationDispatcher.prototype, "reconcileIdentities").mockImplementation(async function (
      this: MutationDispatcher,
      ...args
    ) {
      reconciliationStarted();
      await release.promise;
      return reconcile.apply(this, args);
    });
    try {
      const fence = resumeStopFence("ref_a");
      const refresh = threadsStore.getState().refreshThread("ref_a", fence);
      await started;
      // The Stop lands while the resume's reconciliation is in flight, and the
      // stopped daemon's fresh reads are recovery-blocked again.
      await threadsStore.getState().shutdown("ref_a");
      status = "restartRequired";
      release.resolve();
      await expect(refresh).rejects.toThrow("Stop canceled this pending action");
      // Publication itself was not undone - the finding's premise - so the
      // canceled refresh's earned dispatchability is all there is to unwind.
      expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
      // The outbox's own later discovery scan must not dispatch the queued
      // mutation on the strength of the canceled refresh.
      await vi.advanceTimersByTimeAsync(2000);
      await flushIndexedDBUntil(() => sends >= 1);
      expect(sends).toBe(0);
      // The recovery-blocked obligation the canceled refresh's reconciliation
      // cleared is restored: Retry stays fenced until a fresh snapshot proves
      // recovery again.
      expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    } finally {
      spy.mockRestore();
    }
  } finally {
    vi.useRealTimers();
  }
});

// RoboRev Medium on PR 1393 (b04a358): 33605e1a2 unwound a Stop-canceled
// refresh at refreshThread's tail recheck, but a Stop that lands while the
// refreshed read is still in flight cancels at refreshTrackedThread's
// PRE-publication fence instead. That throw rejects the hydration before
// publication - refreshTrackedThread re-reports it, refreshThread's await
// rejects, and the tail recheck the cleanup is attached to is never reached.
// The dispatch gate the resume banked (the enqueue's replay gate, or an
// earlier refresh's publication) and the recovery obligation its proven
// snapshot cleared both stay standing, and the outbox's own later discovery
// scan dispatches the queued mutation despite the acknowledged Stop. The
// pre-publication evaluation must unwind the canceled refresh too.
test("a later discovery cannot dispatch what a Stop during a refresh's pre-publication left banked", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "refresh-stop-prepublication" });
  try {
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    // The stopped session the recovery UX opens on: recovery-blocked until a
    // resume proves otherwise - and stopped again once the Stop below lands.
    let status = "restartRequired";
    // The one-shot read hold: the refresh under test parks its thread/read on
    // `holdRead`, so the Stop below lands in that refresh's pre-publication
    // window; every later read (the scan's resync among them) answers live.
    let holdRead: Promise<void> | undefined;
    let heldReadArrived!: () => void;
    const readArrived = new Promise<void>((resolve) => {
      heldReadArrived = resolve;
    });
    fake.on("thread/read", () => {
      const response = readResponse("ref_a", { status: { type: status } });
      const hold = holdRead;
      holdRead = undefined;
      if (!hold) return response;
      heldReadArrived();
      return hold.then(() => response);
    });
    fake.on("thread/shutdown", () => ({}));
    let sends = 0;
    fake.on("turn/start", (params) => {
      sends += 1;
      return {
        receipt: mutationReceipt(params.clientMutationId),
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      };
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    // Drain the stopped snapshot's own reconciliation before arming anything.
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    // A message queued while the session was recovery-blocked, never attempted.
    await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "pending" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "pending" }] },
    });
    // The resume's proven snapshot: its publication opens the replay gate and
    // its reconciliation clears the obligation - recovery banked, exactly
    // what a later Stop must unwind again.
    status = "idle";
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
    // One more refresh, and its read hangs: the Stop lands before that
    // refresh ever publishes.
    const release = deferred<void>();
    holdRead = release.promise;
    const fence = resumeStopFence("ref_a");
    const refresh = threadsStore.getState().refreshThread("ref_a", fence);
    await readArrived;
    // The Stop lands while the refreshed read is in flight, and the stopped
    // daemon's fresh reads are recovery-blocked again.
    await threadsStore.getState().shutdown("ref_a");
    status = "restartRequired";
    release.resolve();
    await expect(refresh).rejects.toThrow("Stop canceled this pending action");
    // Publication itself never happened - the fence canceled it first - so
    // the canceled refresh must still unwind what recovery banked before it.
    expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    // The outbox's own later discovery scan must not dispatch the queued
    // mutation on the strength of the state the Stop left banked.
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => sends >= 1);
    expect(sends).toBe(0);
  } finally {
    vi.useRealTimers();
  }
});

// RoboRev Medium (PR 1393 fresh review, 0708b9b): both of a Stop-canceled
// refresh's unwind evaluations ran unconditionally, so a SUPERSEDED refresh's
// stale fence could clobber a newer resume's success. The old refresh's held
// read resolves late; its pre-Stop baseline still throws at the
// pre-publication evaluation; and the unconditional unwind then re-armed the
// restart-blocking obligation and closed the dispatch gate that the NEWER
// refresh - the one that began after the Stop, published a fresh snapshot, and
// reconciled - had just earned. The unwind must be owned by currency: only
// the ref's newest tracked-hydration attempt may unwind, because only it can
// still speak for the state a Stop would cancel. A superseded refresh banked
// nothing itself (publishThreadHydration refuses it), and the newer attempt
// owns the ref's convergence from fresh state.
test("a superseded refresh's stale Stop fence cannot unwind a newer resume's state", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "superseded-unwind" });
  try {
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    // The stopped session the recovery UX opens on: recovery-blocked until a
    // resume proves otherwise.
    let status = "restartRequired";
    // The one-shot read hold: the refresh under test parks its thread/read on
    // `holdRead`, so the Stop and the newer resume below land inside that
    // refresh's pre-publication window; every later read answers live.
    let holdRead: Promise<void> | undefined;
    let heldReadArrived!: () => void;
    const readArrived = new Promise<void>((resolve) => {
      heldReadArrived = resolve;
    });
    fake.on("thread/read", () => {
      const response = readResponse("ref_a", { status: { type: status } });
      const hold = holdRead;
      holdRead = undefined;
      if (!hold) return response;
      heldReadArrived();
      return hold.then(() => response);
    });
    fake.on("thread/shutdown", () => ({}));
    let sends = 0;
    fake.on("turn/start", (params) => {
      sends += 1;
      return {
        receipt: mutationReceipt(params.clientMutationId),
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      };
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    // Drain the stopped snapshot's own reconciliation before arming anything.
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    // A message queued while the session was recovery-blocked, never attempted.
    await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "pending" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "pending" }] },
    });
    // The refresh under test: fenced with a PRE-Stop baseline, its read held.
    const release = deferred<void>();
    holdRead = release.promise;
    const fence = resumeStopFence("ref_a");
    const refresh = threadsStore.getState().refreshThread("ref_a", fence);
    await readArrived;
    // The Stop lands while the held read is still in flight.
    await threadsStore.getState().shutdown("ref_a");
    // The newer resume: a fresh fenced refresh captured AFTER the Stop. It
    // supersedes the held read and proves recovery from a fresh snapshot -
    // its publication opens the gate and its reconciliation clears the
    // obligation.
    status = "idle";
    const resumeFence = resumeStopFence("ref_a");
    const resumed = threadsStore.getState().refreshThread("ref_a", resumeFence);
    await resumed;
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    // The held read releases late: its PRE-Stop fence still throws, and the
    // rejection is still its caller's honest cancellation - but the superseded
    // refresh must not unwind the newer resume's banked state.
    release.resolve();
    await expect(refresh).rejects.toThrow("Stop canceled this pending action");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    // The newer resume's dispatchability stands. The shutdown durably canceled
    // the pre-Stop probe row (the stacked stop-cancellation semantics), so the
    // proof the gate is open is the design's own release path: an explicit
    // Retry after the resume dispatches it.
    expect((await storage.getOutbox("superseded-unwind"))?.state).toBe("canceled");
    expect(await retryBlockedMutation("superseded-unwind", "user")).toBe(true);
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => sends >= 1);
    expect(sends).toBe(1);
  } finally {
    vi.useRealTimers();
  }
});

// The same currency rule at the SECOND unwind site - refreshThread's tail
// recheck: a refresh can publish and still be overtaken before its tail runs
// (its reconciliation is the window), and a newer refresh that published after
// the Stop must not have its state re-armed over by the older refresh's tail
// cancellation. The older refresh's reconciliation clear is legitimately
// canceled by the acknowledged Stop, but the OBLIGATION re-arm closes the
// newer refresh's reconciliation out (its captured obligation symbol no
// longer matches), leaving the resumed session fenced with no fresh proof
// pending - the clobber, reached from the tail instead of the pre-publication
// evaluation.
test("a refresh overtaken before its tail recheck cannot unwind the newer resume's state", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "overtaken-tail-unwind" });
  try {
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    // The stopped session the recovery UX opens on: recovery-blocked until a
    // resume proves otherwise - and stopped again once the Stop below lands.
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("thread/shutdown", () => ({}));
    let sends = 0;
    fake.on("turn/start", (params) => {
      sends += 1;
      return {
        receipt: mutationReceipt(params.clientMutationId),
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      };
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    // Drain the stopped snapshot's own reconciliation before arming anything.
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    // A message queued while the session was recovery-blocked, never attempted.
    await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/start",
      payload: { ref: "ref_a", input: [{ type: "text", text: "pending" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "pending" }] },
    });
    // The resume's proven snapshot: the refresh under test publishes it, and
    // its held reconciliation is the window where the Stop and the newer
    // refresh both land.
    status = "idle";
    const release = deferred<void>();
    let reconciliationStarted!: () => void;
    const started = new Promise<void>((resolve) => {
      reconciliationStarted = resolve;
    });
    const reconcile = MutationDispatcher.prototype.reconcileIdentities;
    const spy = vi.spyOn(MutationDispatcher.prototype, "reconcileIdentities").mockImplementation(async function (
      this: MutationDispatcher,
      ...args
    ) {
      reconciliationStarted();
      await release.promise;
      return reconcile.apply(this, args);
    });
    try {
      const fence = resumeStopFence("ref_a");
      const refresh = threadsStore.getState().refreshThread("ref_a", fence);
      await started;
      // The Stop lands while the resume-proving refresh's reconciliation is
      // in flight.
      await threadsStore.getState().shutdown("ref_a");
      // The newer resume: a fresh fenced refresh captured after the Stop. Its
      // own read answers live and it PUBLISHES during the held reconciliation
      // (its reconciliation chains behind the held one); its publication is
      // the store's per-ref hydration counter advancing.
      const published = (threadsStore.getState().hydrations.get("ref_a") ?? 0) + 1;
      const resumeFence = resumeStopFence("ref_a");
      const resumed = threadsStore.getState().refreshThread("ref_a", resumeFence);
      await vi.waitFor(() => expect(threadsStore.getState().hydrations.get("ref_a") ?? 0).toBe(published));
      // The held reconciliation completes; the overtaken refresh's tail
      // recheck then finds the acknowledged Stop and throws - its rejection
      // is its caller's honest cancellation, but it must not unwind the newer
      // resume's state.
      release.resolve();
      await expect(refresh).rejects.toThrow("Stop canceled this pending action");
      await resumed;
      expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
      // The newer resume's dispatchability stands. The shutdown durably
      // canceled the pre-Stop probe row (the stacked stop-cancellation
      // semantics), so the proof the gate is open is the design's own release
      // path: an explicit Retry after the resume dispatches it.
      expect((await storage.getOutbox("overtaken-tail-unwind"))?.state).toBe("canceled");
      expect(await retryBlockedMutation("overtaken-tail-unwind", "user")).toBe(true);
      await vi.advanceTimersByTimeAsync(2000);
      await flushIndexedDBUntil(() => sends >= 1);
      expect(sends).toBe(1);
    } finally {
      spy.mockRestore();
    }
  } finally {
    vi.useRealTimers();
  }
});

// RoboRev Medium on fee4eb8 (PR 1393): pruning a ref's Stop generation on
// release could make a fence miss a REAL Stop in one interleaving - the
// baseline sees the absent entry as 0, the Stop records a nonzero sequence
// value, release deletes the entry, and the fence reads absent-as-0 again,
// equal to its own baseline. Stop generations now persist for the page
// session, so the Stop a fence was captured before still reads as a landed
// Stop after any release.
test("a fence captured before a Stop still fires after the ref is released", async () => {
  setMutationStorageForTests(new MutationOutboxIndexedDB());
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  fake.on("thread/shutdown", () => ({}));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  // Captured before any Stop: the ref's absent entry reads as 0.
  const fence = resumeStopFence("ref_a");
  const baseline = resumeStopBaseline();
  await threadsStore.getState().shutdown("ref_a");
  threadsStore.getState().releaseThread("ref_a");
  expect(fence).toThrow("Stop canceled this pending action");
  expect(() => baseline("ref_a")).toThrow("Stop canceled this pending action");
  expect(() => baseline()).toThrow("Stop canceled this pending action");
});

// Stop generations persist for the page session. Retention makes release
// truthful where pruning could only be conservative: a fence captured AFTER
// a Stop reads the retained generation as exactly its own baseline - no NEW
// Stop - and stays quiet across a bare release, because release is not a
// Stop. The page-wide sequence still never reissues a value, so a Stop
// landing after a re-ensure fires every fence captured before it. (This
// restructures the pruning test added with the sequence: that test pinned
// prune-as-reset, which fired post-Stop fences on release precisely because
// deletion destroyed the information retention now keeps.)
test("releasing a ref retains its Stop generation across release and re-ensure", async () => {
  setMutationStorageForTests(new MutationOutboxIndexedDB());
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  fake.on("thread/shutdown", () => ({}));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().shutdown("ref_a");
  // Captured after the Stop: quiet before AND after a bare release - the
  // retained value is exactly the captured baseline.
  const settledFence = resumeStopFence("ref_a");
  const settledBaseline = resumeStopBaseline();
  threadsStore.getState().releaseThread("ref_a");
  settledFence();
  settledBaseline("ref_a");
  settledBaseline();
  // A fence captured before a SECOND Stop still fires: retention never
  // recycles the first Stop's value, and the sequence never reissues it.
  const staleFence = resumeStopFence("ref_a");
  const staleBaseline = resumeStopBaseline();
  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().shutdown("ref_a");
  expect(staleFence).toThrow("Stop canceled this pending action");
  expect(() => staleBaseline("ref_a")).toThrow("Stop canceled this pending action");
  threadsStore.getState().releaseThread("ref_a");
});

// The no-ref form of the baseline is the beforeRequest fence: the resume RPC's
// reconnect window ends before the resumed identity is knowable, so the check
// cannot name a ref and instead fires on ANY Stop acknowledged since the
// snapshot. Values only grow (the page-wide sequence), so an increased entry
// is exactly a landed Stop; a released ref's retained entry compares equal to
// its snapshot, which is correct - release is not a Stop.
test("the global resume Stop baseline fires on any ref's Stop and on nothing else", async () => {
  setMutationStorageForTests(new MutationOutboxIndexedDB());
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { status: { type: "idle" } }));
  fake.on("thread/shutdown", () => ({}));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().ensureThread("ref_b");
  const baseline = resumeStopBaseline();
  // Nothing moved: quiet.
  baseline();
  // A Stop against ANY ref - not just the one a fence could name - fires it.
  await threadsStore.getState().shutdown("ref_b");
  expect(() => baseline()).toThrow("Stop canceled this pending action");
  // A baseline taken after that Stop is quiet again, and a bare release of an
  // already-stopped ref does not move it.
  const settled = resumeStopBaseline();
  threadsStore.getState().releaseThread("ref_b");
  settled();
  threadsStore.getState().releaseThread("ref_a");
});

test("persistent journal failures wait for periodic recovery between attempts", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "persistent-journal" });
    let blocked = deferred<void>();
    const markUnknown = storage.markUnknown.bind(storage);
    vi.spyOn(storage, "markUnknown").mockImplementation(async (...args) => {
      const result = await markUnknown(...args);
      blocked.resolve();
      return result;
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
    fake.on("turn/queue", (params) => {
      throw new WireError("journal unavailable", -32014, {
        evenerErrorInfo: "mutationOutcome",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
        cause: "persistenceUnavailable",
      });
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().queue("ref_a", "keep until persistence recovers");
    for (let attempt = 1; attempt <= 3; attempt++) {
      await blocked.promise;
      await settleCallerContinuations();
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(attempt);
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(attempt);
      expect(await retryBlockedMutation("persistent-journal")).toBe(false);
      expect((await storage.getOutbox("persistent-journal"))?.state).toBe("blockedUnknown");
      if (attempt < 3) {
        blocked = deferred<void>();
        await vi.advanceTimersByTimeAsync(2000);
      }
    }
    const settled = deferred<void>();
    const settleReceipt = storage.settleReceipt.bind(storage);
    vi.spyOn(storage, "settleReceipt").mockImplementation(async (...args) => {
      const result = await settleReceipt(...args);
      settled.resolve();
      return result;
    });
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    await vi.advanceTimersByTimeAsync(2000);
    await settled.promise;
    expect(await storage.getOutbox("persistent-journal")).toBeUndefined();
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(4);
  } finally {
    vi.useRealTimers();
  }
});

test("failed blocking persistence prevents a new enqueue from retrying uncertain work", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    let nextID = 0;
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => `blocking-write-${++nextID}` });
    const failed = deferred<void>();
    vi.spyOn(storage, "markUnknown").mockImplementationOnce(async () => {
      failed.resolve();
      throw new DOMException("IndexedDB transaction aborted", "AbortError");
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    const fresh = deferred<ThreadReadResponse>();
    let reads = 0;
    fake.on("thread/read", () => (++reads === 1 ? readResponse("ref_a", { status: { type: "idle" } }) : fresh.promise));
    fake.on("turn/queue", (params) => {
      throw new WireError("owner unavailable", -32014, {
        evenerErrorInfo: "mutationOutcome",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
      });
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().queue("ref_a", "first");
    await failed.promise;
    await settleCallerContinuations();
    expect((await storage.getOutbox("blocking-write-1"))?.state).toBe("submitting");
    await threadsStore.getState().queue("ref_a", "second");
    await settleCallerContinuations();
    await storage.listOutbox("ref_a");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fresh.resolve(readResponse("ref_a", { status: { type: "idle" } }));
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/queue").length === 3);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(3);
  } finally {
    vi.useRealTimers();
  }
});

test("clear response fences goal responses from the previous instance", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().watchThread("ref_a");
  const goalResponse = deferred<{ started: boolean }>();
  const requested = nextHandledRequest(fake, "goal/set", () => goalResponse.promise);
  const pending = threadsStore.getState().setGoal("ref_a", "old objective");
  await requested;
  fake.on("thread/clear", (params) =>
    clearResponse(params, testThread("ref_a", { id: "cleared-instance", turns: [] })),
  );
  await threadsStore.getState().clearThread("ref_a");
  expect(threadsStore.getState().threads.get("ref_a")?.threadId).toBe("cleared-instance");
  goalResponse.resolve({ started: true });
  await pending;
  expect(threadsStore.getState().threads.get("ref_a")?.goal).toBeNull();
  expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toBeNull();
  fake.on("goal/set", () => ({ started: true }));
  await threadsStore.getState().setGoal("ref_a", "new objective");
  expect(threadsStore.getState().threads.get("ref_a")?.goal?.objective).toBe("new objective");
  expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal?.objective).toBe("new objective");
});

// RoboRev Medium (PR 1393 fresh review, b04a358): the local recovery fence was
// enforced only by the surfaces that render their own refusal - Composer's
// availabilityFor and QueueStrip's press handlers, both of which check above
// the store's send action. The alternate send paths call threadsStore.send
// directly - the palette's slash fallthrough and the ask dock's batch send
// pass text alone, a failed turn's Retry passes text with attachments - and
// each could enqueue a durable turn/start for a fenced local session: intent
// the hub's recovery admission refuses for the obligation's whole window, so
// it could only park until an explicit Resume clears the fence. The store's
// shared admission must refuse exactly as those surfaces do.
test("a recovery-fenced local session's send refuses at admission for every caller's shape", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "send-fence" });
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  // A Stop in flight arms the fence while the snapshot still reads idle -
  // the window the liveControls predicate exists for. QueueStrip.test seeds
  // this same obligation directly.
  threadsStore.setState((state) => ({
    restartBlockingObligations: new Map(state.restartBlockingObligations).set("local:session", Symbol()),
  }));
  await expect(threadsStore.getState().send("local:session", "/plain text")).rejects.toThrow(
    "Send isn't available until this session is resumed",
  );
  await expect(
    threadsStore
      .getState()
      .send("local:session", "retry text", [{ marker: 1, mediaType: "image/png", data: "AQID", name: "first.png" }]),
  ).rejects.toThrow("Send isn't available until this session is resumed");
  expect(await storage.listOutbox("local:session")).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// RoboRev Medium (PR 1393 fresh review, 0708b9b): 91764c32d6 fenced only the
// send action at its own admission, so every OTHER durable action still
// funneled an obligation-blocked local session's intent straight into the
// outbox: steer, queue, drainAsSteer, promoteQueuedAsSteer, cancelQueued,
// clearThread and setHumanNote each parked a mutation the hub's recovery
// admission refuses for the obligation's whole window (sessionActionRecoveryError
// is reached by every one of those methods' handlers - withDeletionTargetOwnership
// for the turn verbs and thread/clear, relayWithResume's durable leg for
// notes/human/set), to deliver or fail only after an explicit Resume. The fence
// must read the obligation at enqueueMutationIntent - the one funnel every
// durable action passes through - and refuse exactly the verbs the hub refuses,
// so no caller shape can park what the fence exists to keep unparked.
// turn/interrupt is the deliberate exemption: Stop is how the fenced window
// ends, the Stop button is never disabled by the fence, and the typed
// /interrupt agrees with the button (commands.ts's own carve-out), so
// interrupt still enqueues and settles after Resume.
test("a recovery-fenced local session's non-send durable admissions refuse at the shared funnel", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "shared-fence" });
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const ref = "local:session";
  await threadsStore.getState().ensureThread(ref);
  // A Stop in flight arms the fence while the snapshot still reads idle -
  // the window the liveControls predicate exists for (same seeding as the
  // send-fence test above).
  threadsStore.setState((state) => ({
    restartBlockingObligations: new Map(state.restartBlockingObligations).set(ref, Symbol()),
  }));
  await expect(threadsStore.getState().steer(ref, "steer text")).rejects.toThrow(
    "Steer isn't available until this session is resumed",
  );
  await expect(threadsStore.getState().queue(ref, "queued text")).rejects.toThrow(
    "Queue isn't available until this session is resumed",
  );
  await expect(threadsStore.getState().drainAsSteer(ref, "drain text")).rejects.toThrow(
    "Drain isn't available until this session is resumed",
  );
  await expect(
    threadsStore.getState().promoteQueuedAsSteer(ref, 0, "entry_1", { text: "queued text" }),
  ).rejects.toThrow("Queue actions aren't available until this session is resumed");
  await expect(threadsStore.getState().cancelQueued(ref, 0, "entry_1")).rejects.toThrow(
    "Queue actions aren't available until this session is resumed",
  );
  await expect(threadsStore.getState().clearThread(ref)).rejects.toThrow(
    "Clear isn't available until this session is resumed",
  );
  await expect(threadsStore.getState().setHumanNote(ref, "note text")).rejects.toThrow(
    "Notes aren't available until this session is resumed",
  );
  // turn/interrupt is the exemption: Stop stays pressable through the fence,
  // so its admission still enqueues. The dispatcher keeps refusing the
  // obligation, so nothing reaches the wire.
  await threadsStore.getState().interrupt(ref);
  const outbox = await storage.listOutbox(ref);
  expect(outbox.map((record) => record.method)).toEqual(["turn/interrupt"]);
  expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toEqual([]);
});

// RoboRev Medium (PR 2862): the fence a Stop arms was set only AFTER the
// forceStop RPC settled, so the drain window itself - the hub's Stopping > 0,
// for which sessionActionRecoveryError refuses even turn/start - was unfenced
// at the store. A send pressed while the stop RPC was still in flight enqueued
// and dispatched into the hub's refusal, parking the intent in recovery, which
// is exactly what the fence above exists to keep from happening. The obligation
// must be armed when the drain begins, not when it ends; the two tests above
// seed the obligation directly and so could not see this gap.
test("a Stop in flight arms the recovery fence for the drain window", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "stop-drain" });
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const ref = "local:draining";
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  const stop = deferred<void>();
  const requested = nextHandledRequest(fake, "evener/thread/forceStop", () => stop.promise);
  // Hold the cancellation write open. The drain window begins at the click,
  // and this is the window BEFORE that write completes - the write is an await,
  // and the in-flight Stop is not itself a fence, so a send offered here must
  // still be refused rather than enqueued and dispatched.
  const hold = holdNextWriteTransaction(["outbox", "sequences"]);
  try {
    const pending = threadsStore.getState().forceStop(ref);
    await hold.reached;
    expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    await expect(threadsStore.getState().send(ref, "text")).rejects.toThrow(
      "Send isn't available until this session is resumed",
    );
    expect(await storage.listOutbox(ref)).toEqual([]);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    hold.release();
    await requested;
    // Mid-drain: the hub holds Stopping > 0 and refuses even turn/start, so the
    // store's own admission must refuse the send here rather than dispatch it.
    expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    await expect(threadsStore.getState().send(ref, "text")).rejects.toThrow(
      "Send isn't available until this session is resumed",
    );
    expect(await storage.listOutbox(ref)).toEqual([]);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    // The obligation a stale thread refresh could clear mid-drain must not
    // re-open the fence: the in-flight Stop is its own fence. Clear it exactly
    // as a superseded hydration would, then press Send again.
    threadsStore.setState((state) => {
      const restartBlockingObligations = new Map(state.restartBlockingObligations);
      restartBlockingObligations.delete(ref);
      return { restartBlockingObligations };
    });
    expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
    // Admission still refuses the send with only stopInFlight standing.
    await expect(threadsStore.getState().send(ref, "text")).rejects.toThrow(
      "Send isn't available until this session is resumed",
    );
    expect(await storage.listOutbox(ref)).toEqual([]);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    // Dispatch is fenced for every method while Stopping > 0: turn/interrupt is
    // exempt at admission but must still never reach the wire here.
    await threadsStore.getState().interrupt(ref);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));
    expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toEqual([]);
    expect((await storage.listOutbox(ref)).map((record) => record.method)).toEqual(["turn/interrupt"]);
    stop.resolve();
    await pending;
  } finally {
    hold.release();
  }
});

// RoboRev Medium (all three reviewers): the store-wide resume-only predicate
// must read the delivery-uncertain rows the composer's blockedMutations
// selector fences on, not merely stopInFlight. A direct caller that bypasses
// the composer - the palette's slash fallthrough, the ask dock's batch send, a
// failed turn's Retry - reaches enqueueMutationIntent with no such read, so a
// blockedUnknown row must keep turn/start refused there exactly as the surfaces
// render it (the fence-refusal test above).
test("a merely-resumable local session's send refuses while a blockedUnknown row stands", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const ref = "local:stopped-uncertain";
  fake.on("thread/read", (params) =>
    readResponse(params.ref ?? ref, {
      status: { type: "notLoaded" },
      evener: {
        ref: params.ref ?? ref,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        resumeOnlyFoldable: true,
        queue: { revision: 0 },
      },
    }),
  );
  await threadsStore.getState().ensureThread(ref);
  // The resume-only shape the hub admits turn/start for, obligation armed.
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  // A delivery-uncertain row for the ref: exactly the shape the composer's
  // useBlockedMutationEntries selector fences on. Without the store-level read
  // the predicate would fold the resume into a send ahead of reconciling it.
  const uncertain = await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/queue",
    payload: { ref, input: [{ type: "text", text: "uncertain" }] },
    attachments: [],
    optimisticDisplay: null,
  });
  await storage.markUnknown(uncertain.clientMutationId, "blockedUnknown");
  await refreshPendingTurnsProjection(ref);
  await expect(threadsStore.getState().send(ref, "text")).rejects.toThrow(
    "Send isn't available until this session is resumed",
  );
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// RoboRev Medium: the dispatch gate exempted EVERY queued mutation once the
// session matched resumeOnlyLocalModel, but the hub's carve-out admits only
// turn/start. A queued turn/queue/turn/steer/turn/interrupt from before the
// fence would therefore dispatch into a refusal and be parked in recovery
// instead of staying parked in the outbox. The dispatcher now names the
// record's method (dispatcher.ts's method-aware lookup), and the gate exempts
// only turn/start, so a queued non-send method never reaches the wire.
test("a merely-resumable local session's queued non-send method stays parked", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "queued-method" });
  await storage.enqueueIntent({
    targetRef: "local:stopped-queued",
    threadId: "thr_local:stopped-queued",
    method: "turn/queue",
    payload: { ref: "local:stopped-queued", input: [{ type: "text", text: "queued before recovery" }] },
    attachments: [],
    optimisticDisplay: { text: "queued before recovery" },
  });
  storage.close();
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () =>
    readResponse("local:stopped-queued", {
      status: { type: "notLoaded" },
      evener: {
        ref: "local:stopped-queued",
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        resumeOnlyFoldable: true,
        queue: { revision: 0 },
      },
    }),
  );
  let queued = 0;
  fake.on("turn/queue", (params) => {
    queued += 1;
    return { receipt: mutationReceipt(params.clientMutationId) };
  });
  fake.emitReady();
  // Hydrate the pinned ref (arming the obligation), then let the recovery
  // window's dispatch pass run: the turn/queue must never leave the outbox.
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "thread/read"));
  await flushIndexedDBUntil(() => false);
  expect(threadsStore.getState().restartBlockingObligations.has("local:stopped-queued")).toBe(true);
  expect(queued).toBe(0);
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
});

// RoboRev Medium: the queued-non-send fence was order-blind at dispatch. The
// admission question ("may a NEW send be minted behind this queue?") and the
// dispatch question ("is this exact head record admissible?") genuinely
// differ: hasQueuedNonSend answers the first, but at dispatch the head record
// may itself be the turn/start with nothing ahead of it. With this outbox
// [turn/start, turn/queue] on a foldable ref, the head turn/start must
// dispatch; before the fix the whole ref was refused before the gate could
// inspect the head's method, so the session could neither resume nor drain.
// The queued turn/queue behind it stays parked, and a NEW send is still
// refused at admission while that queued row stands.
test("a merely-resumable local session's head turn/start dispatches ahead of a queued non-send", async () => {
  const ref = "local:head-send-queued-tail";
  const storage = new MutationOutboxIndexedDB();
  await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "resume and send" }] },
    attachments: [],
    optimisticDisplay: { text: "resume and send" },
  });
  await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/queue",
    payload: { ref, input: [{ type: "text", text: "queued behind the send" }] },
    attachments: [],
    optimisticDisplay: { text: "queued behind the send" },
  });
  storage.close();
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        resumeOnlyFoldable: true,
        queue: { revision: 0 },
      },
    }),
  );
  let starts = 0;
  fake.on("turn/start", (params) => {
    starts += 1;
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  let queuedCalls = 0;
  fake.on("turn/queue", (params) => {
    queuedCalls += 1;
    return { receipt: mutationReceipt(params.clientMutationId) };
  });
  fake.emitReady();
  // The head turn/start dispatches even though a non-send row sits behind it.
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  await flushIndexedDBUntil(() => false);
  expect(starts).toBe(1);
  // The queued non-send row behind it stays parked: the hub admits only
  // turn/start under the resume-only carve-out.
  expect(queuedCalls).toBe(0);
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
  await refreshPendingTurnsProjection(ref);
  // The two readings the fix splits: the head record is dispatchable even
  // though a new send behind it is not (the queued row still stands).
  expect(hasQueuedNonSend(ref)).toBe(true);
  expect(resumeOnlyLocalModel(ref)).toBe(false);
  expect(resumeOnlyLocalDispatchable(ref)).toBe(true);
  await expect(threadsStore.getState().send(ref, "another")).rejects.toThrow(
    "Send isn't available until this session is resumed",
  );
});

// Part 2's predicate: a queued, not-yet-attempted NON-turn/start row parks at
// the target's FIFO ahead of a folded send (the dispatcher is strictly FIFO per
// target and the hub's carve-out admits only turn/start), so the store-wide
// predicate must not fold while one stands. The read answers from the durable
// outbox, and a queued turn/start - the send itself - does not block it.
test("the queued-non-send projection read blocks the foldable predicate", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  // Not ready: no dispatch pass runs, so a "submitting" row stays queued and
  // the read observes exactly the durable state under test.
  connectFakeClient("connecting");

  const ref = "local:queued-nonsend";
  putThreadModel(
    ref,
    hydrateThread(
      readResponse(ref, {
        status: { type: "notLoaded" },
        evener: {
          ref,
          capabilities: { ...CAPABILITIES, send: false },
          mutationStateAuthoritative: false,
          resumeRequired: true,
          resumeOnlyFoldable: true,
          queue: { revision: 0 },
        },
      }),
      ref,
      Date.now(),
    ),
  );
  await refreshPendingTurnsProjection(ref);
  // The runtime's startup discovery scan starts an all-targets read that
  // out-ranks this specific one; readiness now waits for the latest read, so
  // settle the outstanding work before asserting the ref is loaded.
  await flushPendingTurnsProjectionForTests();
  expect(hasQueuedNonSend(ref)).toBe(false);
  expect(resumeOnlyLocalModel(ref)).toBe(true);

  // A queued turn/start is the send itself: it does not make the ref unfolable.
  await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "send" }] },
    attachments: [],
    optimisticDisplay: null,
  });
  await refreshPendingTurnsProjection(ref);
  expect(hasQueuedNonSend(ref)).toBe(false);
  expect(resumeOnlyLocalModel(ref)).toBe(true);

  // A queued non-send row does: folding a send behind it would starve the send.
  await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/queue",
    payload: { ref, input: [{ type: "text", text: "queued" }] },
    attachments: [],
    optimisticDisplay: null,
  });
  await refreshPendingTurnsProjection(ref);
  expect(hasQueuedNonSend(ref)).toBe(true);
  expect(resumeOnlyLocalModel(ref)).toBe(false);
});

// The merely-resumable snapshot the three tests below drive: ResumeRequired with
// no send capability, and optionally the hub's foldable bit. A ref that is not
// foldable is the one whose fence a status fold must NOT clear - the hub stamped
// ResumeRequired for a cause whose turn/start it still refuses.
function resumeOnlySnapshot(ref: string, foldable = false): ThreadReadResponse {
  return readResponse(ref, {
    status: { type: "notLoaded" },
    evener: {
      ref,
      capabilities: { ...CAPABILITIES, send: false },
      mutationStateAuthoritative: false,
      resumeRequired: true,
      ...(foldable ? { resumeOnlyFoldable: true } : {}),
      queue: { revision: 0 },
    },
  });
}

// RoboRev Medium (round 13): the off-shut-down clear ends only the
// merely-resumable window a genuinely resume-only snapshot opened. A ref that
// was never resume-only - a restart-required daemon, an unconfirmed force-stop
// exit, connection recovery - carries no resumeOnlyFoldable bit, so a status
// fold off the shut-down set is not evidence the hub resumed it. Clearing that
// ref's recovery obligation would admit mutations the hub still rejects.
test("an off-shut-down status fold keeps the fence for a ref that was never resume-only", async () => {
  const ref = "local:not-resume-only-off-shutdown";
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    // No foldable bit: the hub stamped ResumeRequired for a cause whose
    // turn/start it still refuses, so this fold is no evidence it resumed.
    resumeOnlySnapshot(ref),
  );
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().threads.get(ref)?.resumeOnlyFoldable).toBe(false);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);

  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${ref}`, ref, status: { type: "active" } },
    });
  });

  expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("active");
  // The bit was never set, so the fold is not evidence the hub resumed it: the
  // recovery obligation the snapshot armed must survive.
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
});

// RoboRev Medium (round 13): a status fold must not drop a fence an in-flight
// Stop this page started owns. The Stop's group holds the obligation on purpose
// until its RPC settles, and the hub holds Stopping > 0 (refusing even
// turn/start) for that whole window, so the off-shut-down clear skips it.
test("an off-shut-down status fold leaves a stop-owned fence armed", async () => {
  const ref = "local:stop-owned-off-shutdown";
  const fake = connectFakeClient();
  fake.on("thread/read", () => resumeOnlySnapshot(ref, true));
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);

  const stop = deferred<void>();
  fake.on("evener/thread/forceStop", () => stop.promise);
  const pending = threadsStore.getState().forceStop(ref);
  await flushUntil(() => threadsStore.getState().stoppingRefs.has(ref));
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);

  act(() => {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: `thr_${ref}`, ref, status: { type: "active" } },
    });
  });

  // The stop group still owns the obligation: the fold did not drop its fence.
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  expect(threadsStore.getState().threads.get(ref)?.resumeOnlyFoldable).toBe(false);

  stop.resolve();
  await pending;
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false);
});

// RoboRev Medium (round 13): the resumeOnlyFoldable bit is the PREVIOUS
// connection's answer (applyThreadResumeRequirement stamped it on a snapshot
// that connection produced). After the connection generation changes the hub's
// connection-recovery fence can refuse a folded turn/start on the new
// connection, so the retained bit must not fold a send. A connection change
// invalidates it (invalidateHeldHistoriesForReconnect, the store's single
// reconnect hook), and only a fresh read on the current connection re-stamps
// folding.
test("a connection generation change invalidates a retained resumeOnlyFoldable bit", async () => {
  const ref = "local:resume-only-reconnect";
  const snapshot = (threadRef: string) => resumeOnlySnapshot(threadRef, true);
  const first = connectFakeClient("ready");
  first.on("thread/read", (params) => snapshot(params.ref ?? ref));
  await threadsStore.getState().ensureThread(ref);
  await flushPendingTurnsProjectionForTests();
  expect(resumeOnlyLocalModel(ref)).toBe(true);

  // The connection that produced the snapshot is replaced by a fresh one.
  const next = new FakeClient("ready");
  next.on("thread/read", (params) => snapshot(params.ref ?? ref));
  connectionStore.getState().connect(next);

  // Synchronously after the rewire the outgoing connection's bit is gone: no
  // folded send is admitted from a bit the previous connection produced.
  expect(threadsStore.getState().threads.get(ref)?.resumeOnlyFoldable).toBe(false);
  expect(resumeOnlyLocalModel(ref)).toBe(false);

  // A fresh read on the current connection, still carrying the bit, restores
  // folding.
  await waitFor(() => expect(resumeOnlyLocalModel(ref)).toBe(true));
});

// RoboRev Medium (round 8): an ATTEMPTED non-send row survives a force stop
// (cancelUnattemptedMutations keeps attempted rows) and still parks at the
// target's FIFO. The method-aware dispatcher parks any non-turn/start head of a
// resume-only ref BEFORE markAttempted, so such a row is both attempted and
// still "submitting". The queued-non-send read must count it as blocking: a new
// send is otherwise admitted, enqueued behind the parked head, and never
// dispatched - while the Resume notice/button is already removed for a
// resume-only session, leaving no UI path out.
test("an attempted non-send row blocks the foldable predicate", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  connectFakeClient("connecting");
  const ref = "local:attempted-nonsend";
  putThreadModel(
    ref,
    hydrateThread(
      readResponse(ref, {
        status: { type: "notLoaded" },
        evener: {
          ref,
          capabilities: { ...CAPABILITIES, send: false },
          mutationStateAuthoritative: false,
          resumeRequired: true,
          resumeOnlyFoldable: true,
          queue: { revision: 0 },
        },
      }),
      ref,
      Date.now(),
    ),
  );
  const row = await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/queue",
    payload: { ref, input: [{ type: "text", text: "parked non-send" }] },
    attachments: [],
    optimisticDisplay: null,
  });
  // Dispatched once, then parked by the resume-only fence: attempted, still
  // "submitting", never settled.
  expect(await storage.markAttempted(row.clientMutationId)).toBe(true);
  await refreshPendingTurnsProjection(ref);
  expect(hasQueuedNonSend(ref)).toBe(true);
  expect(resumeOnlyLocalModel(ref)).toBe(false);
});

// RoboRev Medium (round 8): the projection answers from an empty outbox until
// its first durable read resolves. Failing closed only on a never-installed
// projection left an installed-but-not-yet-refreshed one reporting "no
// uncertainty", so a resume could fold ahead of an unreconciled blockedUnknown
// row in the window between hydration and the refresh completing. Readiness is
// per ref: an unloaded ref is not foldable, and folds only once its own durable
// outbox has loaded.
test("a foldable ref whose outbox has not loaded is not foldable", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  connectFakeClient("connecting");
  const ref = "local:not-yet-loaded";
  putThreadModel(
    ref,
    hydrateThread(
      readResponse(ref, {
        status: { type: "notLoaded" },
        evener: {
          ref,
          capabilities: { ...CAPABILITIES, send: false },
          mutationStateAuthoritative: false,
          resumeRequired: true,
          resumeOnlyFoldable: true,
          queue: { revision: 0 },
        },
      }),
      ref,
      Date.now(),
    ),
  );
  // The module's own subscription started a refresh for this ref, but it has
  // not resolved: the projection holds no durable rows for it yet, so the
  // predicate must fail closed rather than fold a resume ahead of a row it has
  // not read.
  // The two direct reads the store-wide predicate folds in fail closed too
  // (the readiness seam), before any durable read has loaded.
  expect(hasBlockedUnknown(ref)).toBe(true);
  expect(hasQueuedNonSend(ref)).toBe(true);
  expect(resumeOnlyLocalModel(ref)).toBe(false);
  // Only once the durable outbox has loaded is the ref foldable.
  await refreshPendingTurnsProjection(ref);
  // The runtime's startup discovery scan starts an all-targets read that
  // out-ranks this specific one; readiness now waits for the latest read, so
  // settle the outstanding work before asserting the ref is loaded.
  await flushPendingTurnsProjectionForTests();
  expect(hasBlockedUnknown(ref)).toBe(false);
  expect(hasQueuedNonSend(ref)).toBe(false);
  expect(resumeOnlyLocalModel(ref)).toBe(true);
});

// docs/design/stop-cancellation-outbox.md §9 items 2-4: the durability
// integrations only a full store can express. Units 1-3 pinned the storage
// transitions, the Stop paths, and the dispatcher's race guards in isolation;
// these run the real threads store end to end over rows a real Stop canceled -
// across a reload (fresh runtime, fresh connection, same durable database),
// across a second connection to that database, and across an explicit Resume
// followed by the explicit Retry that is a canceled row's only release.
//
// §9 item 8 (clear/delete removal) is already pinned store-level above
// ("clearThread removes the ref's canceled rows once the clear lands" and the
// deletion-fence test beside it), so it is not re-run here.
describe("Stop cancellation durability across reload, tabs, and resume", () => {
  // §9 item 9: every wait in this battery fails loudly when its condition never
  // arrives. flushIndexedDBUntil above ends silently at its turn bound and
  // relies on the caller's next expect to notice; that is not enough for waits
  // whose negative is the point (a resend that must never happen), so this
  // variant names what failed to arrive.
  async function flushUntilArrived(what: string, arrived: () => boolean | Promise<boolean>, maxTurns = 60) {
    const probe = new MutationOutboxIndexedDB();
    try {
      for (let turn = 0; turn < maxTurns && !(await arrived()); turn += 1) await probe.listTargetRefs();
    } finally {
      probe.close();
    }
    if (!(await arrived())) throw new Error(`never arrived after ${maxTurns} IndexedDB turns: ${what}`);
  }

  function queueIntent(text: string) {
    return {
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text }] },
      attachments: [],
      optimisticDisplay: null,
    };
  }

  // The durable outbox exists so a send survives a reload, but it must not
  // become a hard dependency: when storage never answers, the send goes out as
  // a plain RPC (like setModel/rename/compact) with no durable row, instead of
  // failing closed after the 10s watchdog and dispatching nothing. This is the
  // adapter's open never answering - the wedged shape PR #2983 stopped
  // aborting upgrades for.
  test("a send whose storage never answers still reaches the daemon and leaves no durable row", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    // Hydrate on working storage first: the mutation runtime is created and its
    // connection opened here, so it is the SEND that meets the wedge below
    // rather than the hydration's own reconciliation reads.
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    // Retire the connection, then make the factory's next open never answer:
    // storage is wedged for every call the send makes.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "sent while storage is wedged");
      // The click-time capture's open never answers; the watchdog is the only
      // thing that ends it, and the send must survive it.
      await vi.advanceTimersByTimeAsync(10_000);
      await send;

      const sent = fake.calls.find((call) => call.method === "turn/start");
      expect(sent, "the wedged send never reached the daemon").toBeDefined();
      const sentParams = sent?.params as { clientMutationId?: string; input?: unknown } | undefined;
      expect(sentParams?.clientMutationId).toBeTruthy();
      expect(sentParams?.input).toBeDefined();
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    // No durable row was written for it: the durable store holds nothing.
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // The fallback send's RPC is its only transport, so a failure on the wire
  // must be retried rather than surfaced. The id is minted once and reused
  // across the attempts, so a reply lost after the daemon applied the mutation
  // is deduped on the retry instead of applied twice. The composer stays in its
  // "Sending" state through the ladder (the submission wraps the whole enqueue).
  test("a fallback send retries a wire failure under the same clientMutationId", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-retry";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    // The first attempt fails on the wire; the second answers.
    let attempts = 0;
    fake.on("turn/start", (params) => {
      attempts += 1;
      if (attempts === 1) throw new Error("FakeClient: socket closed");
      return {
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        receipt: mutationReceipt(params.clientMutationId),
      };
    });

    // Retire the connection, then make the factory's next open never answer:
    // storage is wedged for every call the send makes.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "retried while storage is wedged");
      // The capture's open never answers; the watchdog ends it and the send
      // takes the direct-dispatch fallback, whose first RPC then fails. The
      // client stays ready, so the retry is issued at once.
      await vi.advanceTimersByTimeAsync(10_000);
      await send;

      const sent = fake.calls.filter((call) => call.method === "turn/start");
      expect(sent).toHaveLength(2);
      const ids = sent.map((call) => (call.params as { clientMutationId?: string }).clientMutationId);
      expect(ids[0]).toBeTruthy();
      expect(ids[1]).toBe(ids[0]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // A settled WireError is the daemon's own answer, so it is surfaced at once:
  // an identical retry cannot succeed. Pinned by call count - a retry would
  // show a second turn/start.
  test("a fallback send does not retry a settled WireError refusal", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-conflict";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", () => {
      throw new WireError("turn is not active", -32013, { evenerErrorInfo: "conflict" });
    });

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "refused while storage is wedged");
      const rejection = expect(send).rejects.toBeInstanceOf(ConflictError);
      await vi.advanceTimersByTimeAsync(10_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The guard is `err instanceof WireError`, i.e. EVERY settled WireError -
  // not only a conflict. A non-conflict refusal is surfaced unchanged, not
  // mapped to ConflictError and not retried.
  test("a fallback send does not retry a non-conflict WireError", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-invalid-params";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", () => {
      throw new WireError("bad params", -32602);
    });

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "refused while storage is wedged");
      const rejection = expect(send).rejects.toBeInstanceOf(WireError);
      await vi.advanceTimersByTimeAsync(10_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The ready wait is bounded: a client stuck "reconnecting" past the bound
  // surfaces the original transport error after the single attempt, never
  // hanging the send.
  test("a fallback send surfaces when the ready wait times out", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-ready-timeout";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", () => {
      fake.emitStateChange("reconnecting");
      throw new Error("AppwireClient: socket closed");
    });

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "stuck reconnecting while storage is wedged");
      const rejection = expect(send).rejects.toThrow("AppwireClient: socket closed");
      // Reach the fallback; its first RPC fails with the client reconnecting.
      await vi.advanceTimersByTimeAsync(10_000);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
      // The ready wait elapses with no reconnect: the original error surfaces.
      await vi.advanceTimersByTimeAsync(10_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The retry is bounded: a transport failure on both attempts is surfaced
  // after exactly two, with nothing else left to try.
  test("a fallback send surfaces a transport failure after both attempts", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-exhausted";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", () => {
      throw new Error("FakeClient: socket closed");
    });

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "failed twice while storage is wedged");
      const rejection = expect(send).rejects.toThrow("FakeClient: socket closed");
      await vi.advanceTimersByTimeAsync(10_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(2);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // A dropped connection leaves the client "reconnecting", where a retry fired
  // now only meets the client's own synchronous not-ready rejection. The retry
  // must wait out the reconnect and then go out under the same id.
  test("a fallback send waits out a reconnect before retrying", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-reconnect";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    // The first attempt drops the socket and fails; the second answers.
    let attempts = 0;
    fake.on("turn/start", (params) => {
      attempts += 1;
      if (attempts === 1) {
        fake.emitStateChange("reconnecting");
        throw new Error("AppwireClient: socket closed");
      }
      return {
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        receipt: mutationReceipt(params.clientMutationId),
      };
    });

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "reconnecting while storage is wedged");
      // Reach the fallback; its first RPC fails with the client reconnecting,
      // so the send now waits for readiness instead of issuing a doomed retry.
      await vi.advanceTimersByTimeAsync(10_000);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
      // The reconnect lands: the retry goes out under the same id and settles.
      fake.emitReady();
      await send;
      const sent = fake.calls.filter((call) => call.method === "turn/start");
      expect(sent).toHaveLength(2);
      const ids = sent.map((call) => (call.params as { clientMutationId?: string }).clientMutationId);
      expect(ids[1]).toBe(ids[0]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // A manual retry (ConnectionBanner) can swap in a fresh, already-ready client
  // while the failed one is closed. The fallback must retry against that
  // replacement rather than surface, under the same id.
  test("a fallback send retries against a replacement client after the failed one closes", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-rewire";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const replacement = new FakeClient("ready");
    // The first attempt dies with the client closed and a healthy replacement
    // wired in the same window, exactly what requireClient()'s rewire path is
    // for.
    fake.on("turn/start", () => {
      fake.close();
      connectionStore.getState().connect(replacement);
      throw new Error("AppwireClient: socket closed");
    });
    replacement.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "rewired while storage is wedged");
      await vi.advanceTimersByTimeAsync(10_000);
      await send;
      const failedCalls = fake.calls.filter((call) => call.method === "turn/start");
      const retriedCalls = replacement.calls.filter((call) => call.method === "turn/start");
      expect(failedCalls).toHaveLength(1);
      expect(retriedCalls).toHaveLength(1);
      const firstId = (failedCalls[0]?.params as { clientMutationId?: string } | undefined)?.clientMutationId;
      const retriedId = (retriedCalls[0]?.params as { clientMutationId?: string } | undefined)?.clientMutationId;
      expect(firstId).toBeTruthy();
      expect(retriedId).toBe(firstId);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // A manual retry can wire a replacement that is itself still connecting. A
  // healthy destination exists, so the fallback must wait for it to become
  // ready rather than surface.
  test("a fallback send waits out a wired-but-connecting replacement client", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-connecting";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const replacement = new FakeClient("connecting");
    fake.on("turn/start", () => {
      fake.close();
      connectionStore.getState().connect(replacement);
      throw new Error("AppwireClient: socket closed");
    });
    replacement.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "replaced while storage is wedged");
      // Reach the fallback; its first RPC dies with a connecting replacement
      // wired, so the send now waits for that replacement to be ready.
      await vi.advanceTimersByTimeAsync(10_000);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
      // The replacement connects: the retry goes out under the same id.
      replacement.emitStateChange("ready");
      await send;
      const failedCalls = fake.calls.filter((call) => call.method === "turn/start");
      const retriedCalls = replacement.calls.filter((call) => call.method === "turn/start");
      expect(retriedCalls).toHaveLength(1);
      const firstId = (failedCalls[0]?.params as { clientMutationId?: string } | undefined)?.clientMutationId;
      const retriedId = (retriedCalls[0]?.params as { clientMutationId?: string } | undefined)?.clientMutationId;
      expect(firstId).toBeTruthy();
      expect(retriedId).toBe(firstId);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // The fallback is not the ref's durable FIFO head. The dispatcher sends only
  // nextDispatchable (the ref's head) and re-checks its admission before every
  // attempt; the fallback must re-earn that admission and must not jump an
  // earlier undelivered durable send. Here an earlier queued message A is stuck
  // ("submitting") because no turn/queue handler ever delivers it, so it stays
  // durable and pins the ref; B's enqueue then times out. B must fail closed
  // rather than reach the daemon ahead of A.
  test("a fallback send refuses to jump an earlier undelivered durable send for the ref", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-fallback-order";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    // A enqueues durably and, with no handler, is attempted but never
    // delivered: it stays a "submitting" row and keeps the ref pinned.
    await threadsStore.getState().queue("ref_a", "A");
    expect((await storage.listOutbox("ref_a")).filter((record) => record.method === "turn/queue")).toHaveLength(1);

    // Storage wedges for B's enqueue.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const sendB = threadsStore.getState().send("ref_a", "B");
      const rejection = expect(sendB).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(11_000);
      await rejection;
      // B must not reach the daemon ahead of A's undelivered queued send.
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    // A is untouched by B's refusal.
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect((await reader.listOutbox("ref_a")).filter((record) => record.method === "turn/queue")).toHaveLength(1);
    reader.close();
  });

  // pinnedMutationRefs is populated only after a durable enqueue COMMITS, so a
  // concurrent enqueue for the same ref whose write is still in flight is
  // invisible to it - and the fallback would dispatch past it. A's capture
  // succeeds and its enqueue hangs in flight (arms the ref, never commits, so
  // never pins); B's enqueue times out. B must refuse, not jump A.
  test("a fallback send refuses to jump a concurrent in-flight durable send for the ref", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    let enqueueAttempts = 0;
    storage.enqueueIntent = async () => {
      enqueueAttempts += 1;
      // A: in flight forever (never commits, never pins). B and its retry:
      // a storage timeout.
      if (enqueueAttempts === 1) return new Promise<never>(() => {});
      throw new MutationStorageTimeoutError();
    };

    // A's click arms the ref; let it reach its (hanging) enqueue.
    const sendA = threadsStore.getState().send("ref_a", "A");
    await flushUntil(() => enqueueAttempts === 1);

    const sendB = threadsStore.getState().send("ref_a", "B");
    await expect(sendB).rejects.toBeInstanceOf(MutationStorageTimeoutError);
    // B must not reach the daemon ahead of A's in-flight durable send.
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    // A never settled; drop the handle so nothing is awaited.
    void sendA;
  });

  // Round-7 Medium: the ordering guard must ask "does the ref have non-canceled
  // durable work", not "is the ref retained". A canceled row leaves only on
  // Retry or the thread going away, so it keeps the retention pin forever, and
  // a guard reading that pin would refuse the fallback for a ref with nothing
  // left to deliver - re-breaking the whole feature.
  test("a canceled queued row does not block the send fallback", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-canceled-fallback";
    // Seed the canceled row on its own adapter, so the store's runtime never
    // arms or dispatches it. cancelUnattempted is the Stop's own cancellation
    // write (forceStop/shutdown's, and the one interruptAndCancel makes in the
    // interrupt's transaction).
    const seeder = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    await seeder.enqueueIntent(queueIntent("queued"));
    await seeder.cancelUnattempted("ref_a");
    seeder.close();

    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    // Hydrating reconciles the store's view: the ref is retained (a canceled
    // row) but has no undelivered work.
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    await threadsStore.getState().refreshThread("ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    expect((await storage.listOutbox("ref_a")).map((record) => record.state)).toEqual(["canceled"]);

    // Storage wedges; the send must still reach the daemon.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "after the stop");
      await vi.advanceTimersByTimeAsync(10_000);
      await send;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // Round-12 Medium: the commit-time re-arm must read the hydrated-replay gate
  // FRESH, not from a value captured before the write. A hydration pending at
  // the commit keeps the gate closed, so the commit must NOT re-arm - re-arming
  // there would dispatch during the replay. (A successful commit still re-arms
  // when the gate is open - the code path is shared; this pins the closed case,
  // whose pre-write capture is the stale one.)
  test("a hydration pending at the commit keeps the dispatch gate closed", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-commit-rearm";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    const realEnqueue = storage.enqueueIntent.bind(storage);
    let releaseWrite: () => void = () => {};
    const writeGate = new Promise<void>((resolve) => {
      releaseWrite = resolve;
    });
    let writeReached: () => void = () => {};
    const reached = new Promise<void>((resolve) => {
      writeReached = resolve;
    });
    let held = false;
    storage.enqueueIntent = async (intent, barrier) => {
      if (!held) {
        held = true;
        writeReached();
        await writeGate;
      }
      return realEnqueue(intent, barrier);
    };

    const send = threadsStore.getState().send("ref_a", "hello");
    // Probe the arm at the commit's OWN persistence notify, which runs before
    // its discovery could re-arm via a hydration. A listener consulting the
    // store's scheduler here sees the arm only if the commit re-armed it.
    const dispatchTargets = vi.spyOn(MutationDispatcher.prototype, "dispatchTargets");
    let armedAtCommit: boolean | undefined;
    const unsubscribe = subscribeMutationPersistence((refs, committed) => {
      if (committed === undefined || !refs.includes("ref_a")) return;
      const before = dispatchTargets.mock.calls.length;
      notifyReadyForMutationDispatch(["ref_a"]);
      armedAtCommit = dispatchTargets.mock.calls.length > before;
    });
    try {
      await reached;
      // A refresh during the in-flight write leaves a hydration in flight across
      // the commit (its own resync is still reconciling), so the replay gate is
      // closed at the commit.
      await threadsStore.getState().refreshThread("ref_a");
      await nextMacrotask();
      releaseWrite();
      await send;
    } finally {
      unsubscribe();
      dispatchTargets.mockRestore();
    }
    // The gate was closed, so the commit did NOT re-arm.
    expect(armedAtCommit).toBe(false);
  });

  // Round-8 Medium/Low: the arm is shared state, so ownership cannot clean it
  // up - two clicks can each decline to remove it (A because B was still in
  // flight, B because B never added it) and leave it stale with no durable row.
  // The disarm is quiescence-based instead. A ref whose only row is canceled
  // keeps the outbox non-empty (so no refresh clears the arm) but has no
  // undelivered work; after A and B both fail, the arm must be gone.
  test("the dispatch arm does not survive two concurrent failures on a ref with only a canceled row", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-arm-quiescence";
    const seeder = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    await seeder.enqueueIntent(queueIntent("queued"));
    await seeder.cancelUnattempted("ref_a");
    seeder.close();

    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    await threadsStore.getState().refreshThread("ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    let aAttempts = 0;
    let releaseA: () => void = () => {};
    const aGate = new Promise<void>((resolve) => {
      releaseA = resolve;
    });
    let bStarted: () => void = () => {};
    const bReached = new Promise<void>((resolve) => {
      bStarted = resolve;
    });
    let releaseB: () => void = () => {};
    const bGate = new Promise<void>((resolve) => {
      releaseB = resolve;
    });
    storage.enqueueIntent = async (intent) => {
      if (intent.composerText === "A") {
        aAttempts += 1;
        if (aAttempts === 1) await aGate;
      } else {
        bStarted();
        await bGate;
      }
      throw new MutationStorageTimeoutError();
    };

    // A clicks first and arms the ref; B clicks while A is in flight.
    const sendA = threadsStore.getState().send("ref_a", "A");
    await flushUntil(() => aAttempts === 1);
    const sendB = threadsStore.getState().send("ref_a", "B");
    await bReached;
    releaseA();
    // A's fallback refuses (B is in flight), so it must leave the arm.
    await expect(sendA).rejects.toBeInstanceOf(MutationStorageTimeoutError);
    // B's fallback runs with nothing in flight and no undelivered work: it
    // dispatches directly and must drop the now-idle arm.
    releaseB();
    await sendB;
    // The ref is idle, so the arm must be gone: the scheduler acts only on
    // armed refs, and it must not be asked to dispatch ref_a.
    const dispatchTargets = vi.spyOn(MutationDispatcher.prototype, "dispatchTargets");
    try {
      notifyReadyForMutationDispatch(["ref_a"]);
      expect(dispatchTargets.mock.calls.some(([refs]) => Array.from(refs).includes("ref_a"))).toBe(false);
    } finally {
      dispatchTargets.mockRestore();
    }
    // B reached the daemon (A refused).
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  });

  // Round-7 Medium: the fallback re-runs the dispatcher's own admission, so a
  // fenced ref must fail closed rather than reach the daemon directly. A
  // restart-blocked thread is the fence this drives most directly:
  // currentDispatchClient refuses it (threads.ts's restartRequired clause and
  // the restart obligation).
  test("a send on a restart-blocked ref fails closed instead of falling back", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-restart-fenced";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    fake.on("thread/read", (params) =>
      readResponse(params.ref ?? "ref_a", {
        status: { type: "restartRequired" },
        evener: { ref: params.ref ?? "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
      }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "blocked");
      const rejection = expect(send).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(10_000);
      await rejection;
      // The fenced ref must not reach the daemon.
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // Round-12 Low: the fallback set has four send verbs; behavior was pinned for
  // turn/start and turn/steer, so pin the rest. Queue and Drain go out as plain
  // RPCs with no durable row; Promote stays fail-closed.
  test("a Queue whose storage never answers still reaches the daemon and leaves no durable row", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-queue-fallback";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const queue = threadsStore.getState().queue("ref_a", "queued while wedged");
      await vi.advanceTimersByTimeAsync(10_000);
      await queue;
      const sent = fake.calls.find((call) => call.method === "turn/queue");
      expect(sent, "the wedged queue never reached the daemon").toBeDefined();
      expect((sent?.params as { clientMutationId?: string } | undefined)?.clientMutationId).toBeTruthy();
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  test("a Drain whose storage never answers still reaches the daemon and leaves no durable row", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-drain-fallback";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/drainAsSteer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const drain = threadsStore.getState().drainAsSteer("ref_a", "drain while wedged");
      await vi.advanceTimersByTimeAsync(10_000);
      await drain;
      const sent = fake.calls.find((call) => call.method === "turn/drainAsSteer");
      expect(sent, "the wedged drain never reached the daemon").toBeDefined();
      expect((sent?.params as { clientMutationId?: string } | undefined)?.clientMutationId).toBeTruthy();
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  test("a Promote whose storage never answers stays fail-closed with no wire call", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-promote-fenced";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const promote = threadsStore
        .getState()
        .promoteQueuedAsSteer("ref_a", 0, "entry_1", { text: "promote while wedged" });
      const rejection = expect(promote).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(10_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/promoteQueuedAsSteer")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // Round-13 Medium: the guard must not disable the fallback for a row that is
  // already delivered. A settled send's clearing refresh is fire-and-forget and
  // swallows failures, so a row left counted with nothing undelivered behind it
  // would refuse every later send for the ref once storage wedges - the outage
  // this feature exists to cover. The cause is the settle: it resolves the
  // row's id itself, so nothing here depends on this failed read clearing
  // anything (the direct test of that cause is "a settle re-enables the
  // fallback with no read involved" below).
  test("a settled send whose clearing refresh cannot read does not block the fallback", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-stale-marker";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    // After the durable send's row settles, make the clearing refresh's read
    // fail. listOptimistic is the refresh's read that the dispatcher's own
    // nextDispatchable does not use, so the drain still ends cleanly and the
    // refresh runs - then fails.
    let settled = false;
    const realSettle = storage.settleReceipt.bind(storage);
    storage.settleReceipt = async (id, state) => {
      const result = await realSettle(id, state);
      settled = true;
      storage.listOptimistic = async () => {
        throw new MutationStorageTimeoutError();
      };
      return result;
    };

    await threadsStore.getState().send("ref_a", "A");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    for (let i = 0; i < 80 && !settled; i += 1) await probe.listOutbox();
    // Let the after-drain clearing refresh run (and fail).
    for (let i = 0; i < 10; i += 1) await probe.listOutbox();
    probe.close();
    expect(settled).toBe(true);

    // Storage wedges; the next send must still take the fallback.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "B");
      await vi.advanceTimersByTimeAsync(10_000);
      await send;
      // A's settled dispatch plus B's fallback dispatch.
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(2);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // Follow-up Medium: the round-13 clearing refresh deleted the marker
  // unconditionally. A durable commit that lands while the refresh's read is
  // in flight writes the marker and bumps the generation, so the stale
  // rejection must not erase the commit's own marker - that would let a later
  // send jump the commit's undelivered row. The catch is guarded exactly as
  // the success path guards its write-back. Here A's settled send leaves its
  // clearing refresh parked on its read (generation captured before the read);
  // B commits mid-read; the parked read then rejects (the wedge), and C must
  // still refuse rather than jump B.
  test("a stale refresh failure does not clobber a marker a commit wrote mid-read", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-stale-read-clobber";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    // Park the refresh's OWN read after A's row settles, then let it reject on
    // demand - an explicit wrapper, not a stack sniff. listOptimistic is also
    // read by the projection (readMutationPersistence), which the settle's own
    // notify kicks off first, and by the dispatcher's listTargetRefs. The
    // pins refresh reads only the outbox and optimistic stores, while the
    // projection's read also reads recovery rows, so arming the hold on the
    // recovery read lets the projection's optimistic read through and parks
    // the refresh's.
    let rejectRead: (error: unknown) => void = () => {};
    const readGate = new Promise<never>((_resolve, reject) => {
      rejectRead = reject;
    });
    let readReached: () => void = () => {};
    const reached = new Promise<void>((resolve) => {
      readReached = resolve;
    });
    const realListOptimistic = storage.listOptimistic.bind(storage);
    let holdNextRead = false;
    storage.listOptimistic = (ref) => {
      if (holdNextRead) {
        holdNextRead = false;
        readReached();
        return readGate;
      }
      return realListOptimistic(ref);
    };

    let settled = false;
    const realListRecovery = storage.listRecovery.bind(storage);
    let recoveryArmed = false;
    storage.listRecovery = (ref) => {
      const read = realListRecovery(ref);
      if (settled && !recoveryArmed) {
        recoveryArmed = true;
        holdNextRead = true;
      }
      return read;
    };
    const realSettle = storage.settleReceipt.bind(storage);
    storage.settleReceipt = async (id, state) => {
      const result = await realSettle(id, state);
      settled = true;
      return result;
    };

    await threadsStore.getState().send("ref_a", "A");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    for (let i = 0; i < 80 && !settled; i += 1) await probe.listOutbox();
    await reached;
    expect(settled).toBe(true);

    // B's durable commit lands while that read is still parked: it marks the
    // ref undelivered and bumps the generation past the parked read's capture.
    // With no turn/queue handler bound, B is attempted but never delivered, so
    // it stays a "submitting" row and keeps the ref undelivered.
    await threadsStore.getState().queue("ref_a", "B");
    expect((await storage.listOutbox("ref_a")).some((record) => record.method === "turn/queue")).toBe(true);
    for (let i = 0; i < 10; i += 1) await probe.listOutbox();

    // The parked, now-stale read rejects (the same failure a wedged storage
    // produces). It must NOT clear the marker B's commit wrote, and the wedge
    // follows before any later successful read could mask a clear.
    rejectRead(new MutationStorageTimeoutError());
    await nextMacrotask();
    probe.close();

    // Storage wedges; B is still undelivered, so C must fail closed rather
    // than reach the daemon ahead of it.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const sendC = threadsStore.getState().send("ref_a", "C");
      const rejection = expect(sendC).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(11_000);
      await rejection;
      // A's settled turn/start only: C refused, so it dispatched nothing.
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // Reviewers' Medium, PR #3397, direction one. The failed-read clear may fail
  // a marker open only where no evidence backs it. scheduleMutationDispatch
  // refreshes after dispatchTargets resolves even when the row was only
  // ATTEMPTED (a turn/queue or turn/start with no receipt), so the commit
  // generation is unchanged while a genuinely undelivered row stands - the old
  // unconditional clear dropped the marker there, and a later storage-timeout
  // send then dispatched directly, ahead of the committed row, inverting the
  // user's order once storage recovered.
  test("a failed refresh read retains the marker for a committed but undelivered row", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-general-refresh";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    // Arm the failing reads at the drain's own attempt: the drain's remaining
    // work needs no list read, and the post-attempt clearing refresh is the
    // first read after this point. Both halves fail, so nothing this read can
    // observe backs the marker - only the commit that awaits its delivery does.
    const realMarkAttempted = storage.markAttempted.bind(storage);
    storage.markAttempted = async (id) => {
      const marked = await realMarkAttempted(id);
      storage.listOutbox = async () => {
        throw new MutationStorageTimeoutError();
      };
      storage.listOptimistic = async () => {
        throw new MutationStorageTimeoutError();
      };
      return marked;
    };

    // A's durable queued row: committed, attempted, and never delivered (no
    // turn/queue handler), so it stays undelivered work.
    await threadsStore.getState().queue("ref_a", "A");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    for (let i = 0; i < 10; i += 1) await probe.listOutbox("ref_a");
    expect((await probe.listOutbox("ref_a")).some((record) => record.method === "turn/queue")).toBe(true);

    // Storage wedges; the next send must fail closed rather than reach the
    // daemon ahead of A.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "B");
      const rejection = expect(send).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(11_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // A partially failed read proves nothing either, so it changes nothing: the
  // ref's committed rows stand whatever the half-read returned. Only the
  // optimistic half fails here, so the outbox half's row is what refuses the
  // fallback.
  test("a refresh that fails one read changes nothing about the ref's rows", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-partial-read";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    const realMarkAttempted = storage.markAttempted.bind(storage);
    storage.markAttempted = async (id) => {
      const marked = await realMarkAttempted(id);
      // Only the optimistic half of the post-attempt refresh fails; the outbox
      // half still reads A's undelivered row.
      storage.listOptimistic = async () => {
        throw new MutationStorageTimeoutError();
      };
      return marked;
    };

    await threadsStore.getState().queue("ref_a", "A");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    for (let i = 0; i < 10; i += 1) await probe.listOutbox("ref_a");
    expect((await probe.listOutbox("ref_a")).some((record) => record.method === "turn/queue")).toBe(true);

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "B");
      const rejection = expect(send).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(11_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // A settle is a truth-changing event like a commit, so it must invalidate an
  // in-flight refresh read the same way. Park the refresh's outbox read (armed
  // at the drain's own attempt, which reads nothing after it), settle the id
  // that read will return, then let the read land with its pre-settlement
  // snapshot: the ref's generation moved under it, so the snapshot is discarded
  // and the delivered id is not put back. Without the settle's bump the stale
  // read reinserts the id and every later fallback send fails closed.
  test("a settle that lands during a refresh read is not undone by its snapshot", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-settle-during-read";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    // The refresh's outbox read parks on a gate; listOptimistic stays real, so
    // the read's Promise.all waits on the outbox half alone.
    let releaseRead: (rows: MutationOutboxRecord[]) => void = () => {};
    const readGate = new Promise<MutationOutboxRecord[]>((resolve) => {
      releaseRead = resolve;
    });
    let parked = false;
    const realListOutbox = storage.listOutbox.bind(storage);
    const realMarkAttempted = storage.markAttempted.bind(storage);
    storage.markAttempted = async (id) => {
      const marked = await realMarkAttempted(id);
      if (!parked) {
        parked = true;
        storage.listOutbox = () => readGate;
      }
      return marked;
    };

    await threadsStore.getState().queue("ref_a", "A");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    for (let i = 0; i < 80 && !parked; i += 1) await probe.listOutbox("ref_a");
    expect(parked).toBe(true);
    // The pre-settlement snapshot the parked read will return.
    const snapshot = await probe.listOutbox("ref_a");
    expect(snapshot.map((record) => record.state)).toEqual(["submitting"]);

    // The settle lands while that read is still in flight.
    expect(await storage.settleReceipt(snapshot[0]!.clientMutationId, "pending")).toBe(true);
    // Let the parked read land with its pre-settlement snapshot, then stop
    // stubbing reads so nothing later re-serves it.
    releaseRead(snapshot);
    storage.listOutbox = realListOutbox;
    for (let i = 0; i < 10; i += 1) await probe.listOutbox("ref_a");
    probe.close();

    // Storage wedges; the delivered row must not refuse the fallback.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "B");
      await vi.advanceTimersByTimeAsync(11_000);
      await send;
      // B's fallback dispatched: the settled id was not restored.
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The rule is unconditional: ANY change to a ref's durable outbox state
  // advances its read invalidation, not only changes to ids this tab happens to
  // hold. Harness for the two tests below: a row written straight to storage
  // (never committed here, so it is never an id the guard holds) and held
  // non-dispatchable, so the drain attempts nothing; the post-drain refresh's
  // outbox read then parks until the test releases it, so that read is in
  // flight across whatever change the test lands.
  function parkPostDispatchRefreshRead(storage: MutationOutboxIndexedDB): {
    release: (rows: MutationOutboxRecord[]) => void;
    held: () => number;
    restore: () => void;
  } {
    let releaseRead: (rows: MutationOutboxRecord[]) => void = () => {};
    const readGate = new Promise<MutationOutboxRecord[]>((resolve) => {
      releaseRead = resolve;
    });
    const realOutbox = storage.listOutbox.bind(storage);
    const realNextDispatchable = storage.nextDispatchable.bind(storage);
    let armed = false;
    let held = 0;
    // The drain's own read is what precedes the refresh; arming here keeps the
    // drain's other storage calls real.
    storage.nextDispatchable = (ref) =>
      realNextDispatchable(ref).then((record) => {
        armed = true;
        return record;
      });
    storage.listOutbox = (ref) => {
      if (!armed) return realOutbox(ref);
      held += 1;
      return readGate;
    };
    return {
      release: releaseRead,
      held: () => held,
      restore: () => {
        storage.listOutbox = realOutbox;
        storage.nextDispatchable = realNextDispatchable;
      },
    };
  }

  test("a settle of a row this tab never registered is not undone by a parked read", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-unregistered-settle";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    // A store commit arms the ref for dispatch, then settles away, leaving the
    // ref with nothing the guard holds.
    await threadsStore.getState().queue("ref_a", "ours");
    for (let i = 0; i < 10; i += 1) await nextMacrotask();
    const ours = await storage.listOutbox("ref_a");
    expect(ours).toHaveLength(1);
    expect(await storage.settleReceipt(ours[0]!.clientMutationId, "pending")).toBe(true);

    // A durable queued row this tab never committed and never registered, held
    // non-dispatchable so the drain attempts nothing.
    const persisted = await storage.enqueueIntent(queueIntent("persisted"));
    await storage.markUnknown(persisted.clientMutationId, "blockedUnknown");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    const preSettlement = await probe.listOutbox("ref_a");
    expect(preSettlement.map((record) => record.state)).toEqual(["blockedUnknown"]);
    const parked = parkPostDispatchRefreshRead(storage);
    notifyReadyForMutationDispatch(["ref_a"]);
    for (let i = 0; i < 20 && parked.held() === 0; i += 1) await nextMacrotask();
    expect(parked.held()).toBeGreaterThan(0);

    // The settled record is the authority on which ref's state changed.
    expect(await storage.settleReceipt(persisted.clientMutationId, "pending")).toBe(true);
    // The parked read lands with its pre-settlement snapshot.
    expect((await probe.listOutbox("ref_a")).map((record) => record.state)).toEqual([]);
    parked.release(preSettlement);
    parked.restore();

    // Every later read fails from here, so only the parked read's snapshot can
    // decide the guard - and it must not reinstate the row the change resolved.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "after the settle");
      await vi.advanceTimersByTimeAsync(11_000);
      await send;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  test("a cancel that lands during a parked read is not undone by its snapshot", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-unregistered-cancel";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    fake.on("thread/shutdown", () => ({}));
    await threadsStore.getState().queue("ref_a", "ours");
    for (let i = 0; i < 10; i += 1) await nextMacrotask();
    const ours = await storage.listOutbox("ref_a");
    expect(ours).toHaveLength(1);
    expect(await storage.settleReceipt(ours[0]!.clientMutationId, "pending")).toBe(true);

    // The same unregistered, non-dispatchable row - and unattempted, so the
    // Stop's cancel reaches it.
    const persisted = await storage.enqueueIntent(queueIntent("persisted"));
    await storage.markUnknown(persisted.clientMutationId, "blockedUnknown");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    const preCancel = await probe.listOutbox("ref_a");
    expect(preCancel.map((record) => record.state)).toEqual(["blockedUnknown"]);
    const parked = parkPostDispatchRefreshRead(storage);
    notifyReadyForMutationDispatch(["ref_a"]);
    for (let i = 0; i < 20 && parked.held() === 0; i += 1) await nextMacrotask();
    expect(parked.held()).toBeGreaterThan(0);

    // The Stop's cancel write is a state change the guard must invalidate reads
    // for, registered id or not.
    await threadsStore.getState().shutdown("ref_a");
    // The parked read lands with its pre-cancel snapshot.
    expect((await probe.listOutbox("ref_a")).map((record) => record.state)).toEqual(["canceled"]);
    parked.release(preCancel);
    parked.restore();

    // Every later read fails from here, so only the parked read's snapshot can
    // decide the guard - and it must not reinstate the row the change resolved.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "after the stop");
      await vi.advanceTimersByTimeAsync(11_000);
      await send;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The cancel path names the ids it cancels, so it must resolve them: the
  // design leaves ids only for cancel paths that cannot name their rows.
  test("a Stop that cancels a durably committed row does not refuse the next send", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-stop-names-canceled-ids";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    fake.on("thread/shutdown", () => ({}));
    // A store commit arms the ref and registers its row, which then settles
    // away, leaving the guard empty and the arm in place.
    await threadsStore.getState().queue("ref_a", "ours");
    for (let i = 0; i < 10; i += 1) await nextMacrotask();
    const ours = await storage.listOutbox("ref_a");
    expect(ours).toHaveLength(1);
    expect(await storage.settleReceipt(ours[0]!.clientMutationId, "pending")).toBe(true);

    // A durable row written after the hydration (so nothing reopens it) and
    // held non-dispatchable, read into the guard by the dispatch's own trailing
    // refresh - which attempts nothing behind a blockedUnknown head, so the
    // Stop's cancel still reaches it.
    const row = await storage.enqueueIntent(queueIntent("persisted"));
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    notifyReadyForMutationDispatch(["ref_a"]);
    for (let i = 0; i < 10; i += 1) await nextMacrotask();
    const guarded = await probe.listOutbox("ref_a");
    expect(guarded.map((record) => record.state)).toEqual(["blockedUnknown"]);
    expect(guarded[0]!.attempted).toBe(false);

    // The Stop cancels the row and the write names the id it canceled.
    await threadsStore.getState().shutdown("ref_a");
    expect((await probe.listOutbox("ref_a")).map((record) => record.state)).toEqual(["canceled"]);

    // Storage wedges. Nothing undelivered is left, so the send must dispatch
    // rather than fail closed behind a row that never reached the daemon.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "after the stop");
      await vi.advanceTimersByTimeAsync(11_000);
      await send;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // A recovery row leaves the outbox with its id still registered; discarding
  // it resolves that id, so later unreadable storage cannot keep refusing.
  test("a discarded recovery row does not leave its id blocking the fallback", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-discard-recovery-id";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    await threadsStore.getState().queue("ref_a", "ours");
    for (let i = 0; i < 10; i += 1) await nextMacrotask();
    const ours = await storage.listOutbox("ref_a");
    expect(ours).toHaveLength(1);
    expect(await storage.settleReceipt(ours[0]!.clientMutationId, "pending")).toBe(true);

    const row = await storage.enqueueIntent(queueIntent("persisted"));
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    notifyReadyForMutationDispatch(["ref_a"]);
    for (let i = 0; i < 10; i += 1) await nextMacrotask();
    expect((await probe.listOutbox("ref_a")).map((record) => record.state)).toEqual(["blockedUnknown"]);

    // A terminal refusal moves the row to recovery, id and all.
    expect(await storage.transferToRecovery(row.clientMutationId, "rejected")).toBeDefined();
    expect(await probe.listOutbox("ref_a")).toEqual([]);
    // The user discards it: the id is resolved with the row.
    expect(await discardRecoveryMutation(row.clientMutationId, "ref_a")).toBe(true);

    // Storage wedges; the row's id must not refuse the fallback.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "after the discard");
      await vi.advanceTimersByTimeAsync(11_000);
      await send;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The rebuilt guard's seeding rule: a successful read is the authoritative
  // snapshot of the ref, so the non-canceled rows it observes ARE the ref's
  // committed rows - including a row this tab never committed, as a reload or
  // another tab leaves behind. Without the seed such a row is invisible to the
  // guard and a wedged send jumps it.
  test("a successful read seeds the ref's committed rows from the outbox", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-seed-on-read";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    // A durable queued row written straight to storage: no commit of ours, no
    // dispatch of ours.
    const persisted = await storage.enqueueIntent(queueIntent("persisted"));
    await threadsStore.getState().refreshThread("ref_a");
    await nextMacrotask();
    expect((await storage.listOutbox("ref_a")).map((record) => record.clientMutationId)).toEqual([
      persisted.clientMutationId,
    ]);

    // Storage wedges; the row the read seeded still refuses the fallback.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "after the reload");
      const rejection = expect(send).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(11_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The rebuilt guard's delivery rule, and the settled case's cause: the settle
  // write itself resolves the row, so the guard stands down with NO read
  // involved. Both halves of every later read fail here, and the drain's next
  // read fails too - so dispatchTargets rejects and the post-drain clearing
  // refresh never even runs. The fallback still works, because the settle
  // removed the id rather than a lucky read having cleared anything.
  test("a settle re-enables the fallback with no read involved", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-settle-clears";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    let settled = false;
    const realSettle = storage.settleReceipt.bind(storage);
    storage.settleReceipt = async (id, state) => {
      const result = await realSettle(id, state);
      settled = true;
      // Every read from here on fails, both halves included.
      storage.listOutbox = async () => {
        throw new MutationStorageTimeoutError();
      };
      storage.listOptimistic = async () => {
        throw new MutationStorageTimeoutError();
      };
      return result;
    };

    await threadsStore.getState().send("ref_a", "A");
    const probe = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    for (let i = 0; i < 80 && !settled; i += 1) await probe.listOutbox();
    // Let the rejected drain and its skipped clearing refresh settle out.
    for (let i = 0; i < 10; i += 1) await probe.listOutbox();
    probe.close();
    expect(settled).toBe(true);

    // Storage wedges; with the id resolved by the settle, B may take the
    // fallback even though no read ever succeeded after the settle.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "B");
      await vi.advanceTimersByTimeAsync(11_000);
      await send;
      // A's settled dispatch plus B's fallback.
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(2);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The rebuilt guard's failed-read rule: a read that fails proves nothing, so
  // it changes nothing - a row a reload left queued still refuses the fallback
  // even when the store cannot read it back.
  test("a failed read preserves a row a reload left queued", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-reload-failed-read";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await nextMacrotask();
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    await storage.enqueueIntent(queueIntent("persisted"));
    await threadsStore.getState().refreshThread("ref_a");
    // Let the seeding read's own reconciliation settle before anything breaks
    // reads: a reconcile left in flight across the failure would fence the ref
    // and refuse the send for its own reason, hiding what the marker does.
    for (let i = 0; i < 5; i += 1) await nextMacrotask();
    // Break both halves of the post-attempt clearing refresh, so the read that
    // fails is the one whose failure the old fail-open cleared the marker for.
    let attempted = false;
    const realMarkAttempted = storage.markAttempted.bind(storage);
    storage.markAttempted = async (id) => {
      const marked = await realMarkAttempted(id);
      attempted = true;
      storage.listOutbox = async () => {
        throw new MutationStorageTimeoutError();
      };
      storage.listOptimistic = async () => {
        throw new MutationStorageTimeoutError();
      };
      return marked;
    };
    // Arm the ref through a store commit of its own: the persisted row is
    // earlier in the FIFO, so it is the head the drain attempts.
    await threadsStore.getState().queue("ref_a", "ours");
    for (let i = 0; i < 10; i += 1) await nextMacrotask();
    expect(attempted).toBe(true);

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "after the reload");
      const rejection = expect(send).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(11_000);
      await rejection;
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
  });

  // The fallback discards the RPC response. What projects the turn, then? The
  // reducer's applyNotificationToThread (appwire-client/typescript/reducer.ts)
  // has NO turn/started case; the only case that builds turns and items is
  // history/updated (case at reducer.ts:3528 -> applyHistoryUpdated at 3068).
  // The turn is therefore projected from the daemon's own read-model push,
  // exactly as for a dispatched row - the receipt the dispatcher would have
  // settled only settles the optimistic/pending copy, which the fallback never
  // wrote. Pin it: after a fallback send, a history/updated push shows the
  // turn, and nothing is left stuck optimistic.
  test("a fallback-dispatched send is projected from the daemon's history/updated push, not stuck optimistic", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-fallback-projection";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    // Hydrate on a versioned read so the live history/updated frame below
    // merges into the same boot generation instead of being invalidated.
    fake.on("thread/read", (params) => versionedReadResponse(params.ref ?? "ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const send = threadsStore.getState().send("ref_a", "hello");
      await vi.advanceTimersByTimeAsync(10_000);
      await send;
      expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    // Nothing is left stuck optimistic: the fallback wrote no durable or
    // optimistic row for this send.
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOptimistic("ref_a")).toEqual([]);
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
    // The daemon's read-model push is what projects the turn.
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "", startedAt: 1000 }],
      },
    });
    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["turn_1"]);
  });

  // Round-3 High, contested. A readwrite timeout rejects ONLY when the
  // watchdog's abort() succeeds - i.e. when rollback is proven. A write that is
  // already committing when the watchdog fires makes abort() throw, and
  // #runTransaction's stalledWrite path then leaves the promise pending rather
  // than rejecting: enqueueDurableMutation does not retry and the direct
  // fallback never runs, and the commit still lands. This pins that path
  // mechanically (see the teeth proof in the commit message).
  test("a readwrite timeout that cannot abort never rejects, retries or falls back, and the commit stands", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    let enqueueAttempts = 0;
    const realEnqueue = storage.enqueueIntent.bind(storage);
    storage.enqueueIntent = (...args: Parameters<typeof realEnqueue>) => {
      enqueueAttempts += 1;
      return realEnqueue(...args);
    };

    // Hold the enqueue transaction's complete event: the write is durably done
    // and committing when the watchdog fires, so transaction.abort() throws.
    const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
    // Only the timer types the deadline and the retry delay use: the outbox
    // interval stays real so no discovery fires mid-test.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let outcome: "resolved" | "rejected" | null = null;
    try {
      const send = threadsStore.getState().send("ref_a", "commit in flight");
      void send.then(
        () => {
          outcome = "resolved";
        },
        () => {
          outcome = "rejected";
        },
      );
      await hold.reached;
      // The enqueue watchdog fires: abort() throws, so the deadline cannot
      // reject and the retry delay is never scheduled. Advance past both the
      // watchdog and the retry delay so a mutation that DOES reject would run
      // its retry to completion and trip the assertions below.
      await vi.advanceTimersByTimeAsync(11_000);
      // Flush pending continuations so a wrongly-rejecting or retrying
      // implementation would have settled before the assertions below. This
      // does NOT wait for `outcome`: the correct path leaves it null forever,
      // which is exactly what the next assertion pins.
      await flushMicrotasks();
      // Not rejected, not resolved, not retried, and nothing reached the wire.
      expect(outcome).toBeNull();
      expect(enqueueAttempts).toBe(1);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } finally {
      hold.release();
      vi.useRealTimers();
    }
    // The withheld commit lands: the send resolves and the outbox dispatches it.
    await flushUntilArrived("the committing send to dispatch", () =>
      fake.calls.some((call) => call.method === "turn/start"),
    );
    expect(enqueueAttempts).toBe(1);
  });

  // A Stop is the one durable write that must NOT take the direct fallback.
  // Its enqueue is enqueueInterruptAndCancel: the ref's cancelable rows turn
  // "canceled" in the same transaction that writes the turn/interrupt record,
  // both land or neither does. A fallback that dispatched turn/interrupt while
  // writing no cancels would interrupt the turn and leave the queued messages
  // the click was cancelling still queued - to be dispatched later, exactly
  // what the click meant to stop. So a Stop whose storage never answers must
  // fail closed: nothing reaches the daemon and nothing is durably written.
  test("a Stop whose storage never answers stays fail-closed: no interrupt reaches the daemon and no durable row is written", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-stop-wedged";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    // Hydrate on working storage first, exactly like the send case: the
    // runtime's connection opens here, so it is the Stop that meets the wedge.
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    // Retire the connection, then make the factory's next open never answer.
    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const stop = threadsStore.getState().interrupt("ref_a");
      const rejection = expect(stop).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      // Both enqueue attempts end on their own watchdog before the failure.
      await vi.advanceTimersByTimeAsync(21_000);
      await rejection;
      // Stop must not fall back: the interrupt never reaches the daemon.
      expect(fake.calls.filter((call) => call.method === "turn/interrupt")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    // ...and no durable row was written for it.
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // thread/clear and notes/human/set carry their own durable contracts and must
  // NOT take the direct fallback: a fallback clear would skip applyClearResponse
  // (leaving stale local state until the next read) and a fallback note would
  // never fire setHumanNote's onCommitted (so the note panel never marks the
  // draft saved). Only the composer's four send verbs fall back; both of these
  // stay fail-closed.
  test("a Clear whose storage never answers stays fail-closed: no thread/clear reaches the daemon and no durable row is written", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-clear-wedged";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a")));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const clear = threadsStore.getState().clearThread("ref_a");
      const rejection = expect(clear).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      // The click-time capture read is the clear's first storage call; its
      // watchdog ends the attempt.
      await vi.advanceTimersByTimeAsync(11_000);
      await rejection;
      // The clear must not fall back: it never reaches the daemon.
      expect(fake.calls.filter((call) => call.method === "thread/clear")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    // ...and no durable row was written for it.
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  test("a note save whose storage never answers stays fail-closed: no notes/human/set reaches the daemon and no durable row is written", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-note-wedged";
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("notes/human/set", (params) => ({ note: "a note", receipt: mutationReceipt(params.clientMutationId) }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? neverSettlingRequest() : open(name, version),
      );
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      const save = threadsStore.getState().setHumanNote("ref_a", "a note");
      const rejection = expect(save).rejects.toBeInstanceOf(MutationStorageTimeoutError);
      await vi.advanceTimersByTimeAsync(11_000);
      await rejection;
      // The note save must not fall back: it never reaches the daemon.
      expect(fake.calls.filter((call) => call.method === "notes/human/set")).toEqual([]);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    // ...and no durable row was written for it.
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // §4's stop barrier is the click's OWN storage observation. When the durable
  // write times out, the enqueue retries it - but the retry must reuse the
  // capture taken at the click, never re-read the epoch, or a Stop landing
  // between attempts would become the retry's baseline and be silently lost.
  test("a durable write that times out is retried once and the retry never re-reads the click-time capture", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    const realReadStopEpoch = storage.readStopEpoch.bind(storage);
    let captureReads = 0;
    storage.readStopEpoch = async (targetRef: string) => {
      captureReads += 1;
      return realReadStopEpoch(targetRef);
    };
    let writeAttempts = 0;
    storage.enqueueIntent = async () => {
      writeAttempts += 1;
      throw new MutationStorageTimeoutError();
    };

    await threadsStore.getState().send("ref_a", "retried then fallen back");

    // Two total attempts - the original and one retry - then the fallback.
    expect(writeAttempts).toBe(2);
    // The click-time capture was read exactly once across the ladder.
    expect(captureReads).toBe(1);
    // The write never committed, so the send went out as a plain RPC.
    expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true);
    expect(await storage.listOutbox("ref_a")).toEqual([]);
  });

  // The fallback must not change the healthy path: a send with working storage
  // is committed durably and dispatched by the outbox, so the id on the wire is
  // the committed row's own id - not a fresh one minted by a direct dispatch.
  test("storage healthy: a send takes the durable path and is dispatched by the outbox, not directly", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    const committedIds: string[] = [];
    const unsubscribe = subscribeMutationPersistence((refs, commit) => {
      if (refs.includes("ref_a") && commit) committedIds.push(commit.record.clientMutationId);
    });
    try {
      await threadsStore.getState().send("ref_a", "durable send");
      await flushUntilArrived("the durable send to dispatch", () =>
        fake.calls.some((call) => call.method === "turn/start"),
      );
    } finally {
      unsubscribe();
    }

    const sent = fake.calls.find((call) => call.method === "turn/start");
    const sentParams = sent?.params as { clientMutationId?: string } | undefined;
    expect(committedIds).toContain(sentParams?.clientMutationId);
  });

  // RoboRev PR #1873 medium, the fresh review's Retry-path gap: the stop-epoch
  // barrier was captured and compared only on the enqueue path.
  // retryBlockedMutation reads the row as canceled and then releases it with
  // no capture of its own, so a Stop in another tab committing between that
  // read and the release is silently defeated: its cancel scan skips the row
  // (isCancelableByStop excludes "canceled"), the release resurrects it to
  // "submitting", and the dispatch tail sends the message the second Stop
  // meant to cancel. The release now carries §4's click-time capture - the
  // row and the ref's stop epoch read in ONE transaction requested in the
  // click's synchronous prefix, the enqueue barrier's own rule - and a Stop
  // landing between the capture and the release refuses it: a newer Stop
  // outranks an earlier Retry, commit-order, the same comparison the enqueue
  // barrier carries.
  test("a Retry whose release lands after another tab's Stop stays canceled and never dispatches", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const canceled = await storage.enqueueIntent(queueIntent("canceled by the first stop"));
    await storage.cancelUnattempted("ref_a");
    expect((await storage.getOutbox(canceled.clientMutationId))?.state).toBe("canceled");
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    // The release seam: the retry has read the row and passed its press-time
    // checks; its release write waits at the seam. The write that lands is
    // the real one.
    const realReleaseCanceled = storage.releaseCanceled.bind(storage);
    const releaseReached = deferred<void>();
    const releaseGate = deferred<void>();
    storage.releaseCanceled = (...args: Parameters<typeof realReleaseCanceled>) => {
      releaseReached.resolve();
      return releaseGate.promise.then(() => realReleaseCanceled(...args));
    };

    // The user's Retry click.
    const retry = retryBlockedMutation(canceled.clientMutationId);
    await releaseReached.promise;

    // The second tab's Stop commits between the retry's read and its release:
    // its cancel scan skips the already-canceled row - exactly the finding's
    // premise - and its epoch bump is the one trace the release must catch.
    const tabB = new MutationOutboxIndexedDB();
    try {
      const interrupt = await tabB.enqueueInterruptAndCancel({
        targetRef: "ref_a",
        method: "turn/interrupt",
        payload: { ref: "ref_a" },
        attachments: [],
        optimisticDisplay: { method: "turn/interrupt" },
      });
      expect((await tabB.getOutbox(canceled.clientMutationId))?.state).toBe("canceled");

      // The release finally issues - after the Stop, the finding's order. A
      // newer Stop outranks the earlier Retry: the release refuses, the
      // retry reports the press-time refusal, and the row stays canceled for
      // the Stop that claimed it.
      releaseGate.resolve();
      expect(await retry).toBe(false);
      expect((await storage.getOutbox(canceled.clientMutationId))?.state).toBe("canceled");
      // Nothing re-cancels it, so the dispatch machinery is the only thing
      // that could resurrect it: the FIFO head is the second Stop's own
      // interrupt, never the row the Retry tried to release.
      await flushIndexedDBUntil(() => false);
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
      expect((await storage.nextDispatchable("ref_a"))?.clientMutationId).toBe(interrupt.clientMutationId);
    } finally {
      tabB.close();
    }

    // Liveness control, so the refusal is the barrier and not a broken
    // retry: a Retry pressed AFTER the newer Stop - its capture reads the
    // post-Stop epoch - is §9 item 7's deliberate post-Stop send, and it
    // must still release and dispatch.
    expect(await retryBlockedMutation(canceled.clientMutationId)).toBe(true);
    await flushUntilArrived("the deliberate post-Stop retry to dispatch", () =>
      fake.calls.some((call) => call.method === "turn/queue"),
    );
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  });

  // The same finding's cold-connection shape, the capture-race fix's own rule
  // (the enqueue barrier's d8eec70232 lesson): the capture is a read, and a
  // read must wait for the tab's own connection. A Retry clicked while this
  // tab's connection is closed must REQUEST the capture read inside the
  // click's synchronous prefix - the request itself issues the reopen - so
  // the capture is the first transaction the reopened connection creates and
  // a warm tab's Stop committed during that opening wait lands its bump
  // where the release's comparison reads it. A capture requested after any
  // startup wait instead reads the POST-Stop epoch, the comparison passes
  // equal, and the release resurrects the row into the session the other
  // tab just stopped.
  test("a Retry whose click requests the capture first fences a Stop committed during its connection setup", async () => {
    // "Drop the live connection" uses the adapter's own versionchange path:
    // dispatch versionchange on the live connection and the adapter's retire
    // listener drops it (the adapter stays usable).
    const indexedDB = globalThis.indexedDB;
    const open = indexedDB.open.bind(indexedDB);
    const connections: IDBDatabase[] = [];
    vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
      const request = open(name, version);
      request.addEventListener("success", () => connections.push(request.result));
      return request;
    });
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const canceled = await storage.enqueueIntent(queueIntent("canceled by the first stop"));
    await storage.cancelUnattempted("ref_a");
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    // The other tab: a second real connection to the same durable database —
    // no store of ours, exactly a warm sibling whose Stop is a real write.
    const tabB = new MutationOutboxIndexedDB();

    // The cold tab: THIS store's connection is closed at the click, so the
    // capture read must reopen it before it can run — the finding's opening
    // wait, however long the engine takes to satisfy it.
    connections[connections.length - 1]?.dispatchEvent(new IDBVersionChangeEvent("versionchange"));
    await nextMacrotask();

    // The click. retryBlockedMutation's synchronous prefix must request the
    // capture read — and with it the reopen — before yielding to the event
    // loop.
    const retry = retryBlockedMutation(canceled.clientMutationId);
    // The warm tab's Stop commits while the cold tab's connection is still
    // opening, before the capture read resolves. No yield precedes it: this
    // is exactly the queue position the click-time request has to own.
    await tabB.enqueueInterruptAndCancel({
      targetRef: "ref_a",
      method: "turn/interrupt",
      payload: { ref: "ref_a" },
      attachments: [],
      optimisticDisplay: { method: "turn/interrupt" },
    });
    expect(await retry).toBe(false);
    // The Stop's own cancel scan skipped the canceled row, so the epoch
    // barrier is the only fence that can still catch it — and must.
    expect((await storage.getOutbox(canceled.clientMutationId))?.state).toBe("canceled");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
    tabB.close();
  });

  // RoboRev PR #1873 low, the fresh review: cancelUnattemptedMutations
  // suppressed its persistence notification when its own write canceled
  // nothing, but zero is exactly what this tab observes when a sibling tab's
  // Stop already canceled the rows — and the raw storage write announces
  // nothing over the BroadcastChannel either — so this tab's cached
  // projection and pins stayed on the pre-cancel view until the next
  // discovery scan refreshed them. Every successful cancel write must
  // notify, zero included, the same rule the discard paths carry.
  test("a stop whose rows a sibling tab already canceled still refreshes this tab's projection", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const storage = new MutationOutboxIndexedDB();
      setMutationStorageForTests(storage);
      const fake = connectMutationClient();
      await ensureActiveMutationTarget(fake, "ref_a");
      const queued = await storage.enqueueIntent(queueIntent("canceled by the sibling tab"));
      fake.on("thread/shutdown", () => ({}));

      // The sibling tab's Stop already canceled the row, so this tab's own
      // cancel write will observe zero — the finding's precondition.
      const sibling = new MutationOutboxIndexedDB();
      const siblingCanceled = await sibling.cancelUnattempted("ref_a");
      sibling.close();
      expect(siblingCanceled).toEqual([queued.clientMutationId]);

      // Fake timers pin the discovery interval: no scan may be what
      // refreshes the projection, so the notify under test is the only
      // thing that can fire here. The flush first drains every discovery
      // the setup armed, so the baseline below is honest.
      await flushIndexedDBUntil(() => false);
      let notified = false;
      const unsubscribe = subscribeMutationPersistence((refs) => {
        if (refs.includes("ref_a")) notified = true;
      });
      try {
        expect(notified).toBe(false);
        await threadsStore.getState().shutdown("ref_a");
        await flushUntilArrived("the zero-cancel stop to refresh this tab's projection", () => notified);
      } finally {
        unsubscribe();
      }
    } finally {
      vi.useRealTimers();
    }
  });

  // §9 item 4: a canceled row must not ride a Resume back out. The stopped
  // session's recovery flow - Force stop, the recovery obligation it leaves,
  // the explicit Resume that clears it - all run between the cancellation and
  // the row's only release, an explicit user Retry.
  test("an explicit Resume reconciles without resending, and Retry only then dispatches", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const stopped = await storage.enqueueIntent(queueIntent("stopped by the force stop"));
    let queuedSends = 0;
    fake.on("turn/queue", (params) => {
      queuedSends += 1;
      return { receipt: mutationReceipt(params.clientMutationId) };
    });
    fake.on("evener/thread/forceStop", () => ({}));

    // The user's Force stop: write-first cancellation, then the RPC, then the
    // recovery obligation a stopped session carries.
    await threadsStore.getState().forceStop("ref_a");
    expect(queuedSends).toBe(0);
    expect((await storage.getOutbox(stopped.clientMutationId))?.state).toBe("canceled");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);

    // The pane's re-read after the stop lands the stopped snapshot, which
    // keeps the obligation in place - and Retry stays refused while it holds.
    fake.on("thread/read", (params) =>
      readResponse(params.ref ?? "ref_a", {
        status: { type: "restartRequired" },
        evener: {
          ref: params.ref ?? "ref_a",
          capabilities: CAPABILITIES,
          resumeRequired: true,
          mutationStateAuthoritative: false,
          queue: { revision: 0 },
        },
      }),
    );
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    expect(await retryBlockedMutation(stopped.clientMutationId)).toBe(false);

    // Explicit Resume, exactly the Session pane's own flow: baseline the Stop
    // generations, resume, then refresh the resumed identity so the store's
    // reconciliation publishes the live snapshot.
    fake.on("thread/resume", () => readResponse("ref_a", { status: { type: "idle" } }));
    fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { status: { type: "idle" } }));
    const stopBaseline = resumeStopBaseline();
    const { thread } = await fake.resumeThread("ref_a", { beforeRequest: stopBaseline });
    const refreshedRef = thread.evener.ref;
    await threadsStore.getState().refreshThread(refreshedRef, () => stopBaseline(refreshedRef));

    // The resume's reconciliation published the live, authoritative snapshot -
    // obligation cleared, ref re-armed by its still-canceled row - and resent
    // nothing.
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    expect(queuedSends).toBe(0);
    expect((await storage.getOutbox(stopped.clientMutationId))?.state).toBe("canceled");

    // Retry after Resume is the one release: it dispatches, and the receipt
    // settles the row out of the outbox.
    expect(await retryBlockedMutation(stopped.clientMutationId)).toBe(true);
    await flushUntilArrived("the retried send to dispatch", () => queuedSends === 1);
    await flushUntilArrived(
      "the retried row to settle out of the outbox",
      async () => (await storage.getOutbox(stopped.clientMutationId)) === undefined,
    );
    expect(queuedSends).toBe(1);
  });

  // RoboRev PR #1873 medium: a canceled note row's only exits were an explicit
  // user Retry (the note editor never offers one - its retry branch is gated
  // on blockedUnknown) and the thread going away. The user's next blur-save IS
  // the retry: when that newer notes/human/set commits, the canceled row it
  // supersedes must leave the outbox with it, or it pins the ref (with its
  // note text in durable storage) until the thread is cleared or deleted.
  test("a newer committed note save discards the canceled note row it supersedes", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const canceled = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "notes/human/set",
      payload: { ref: "ref_a", expectedInstanceId: "thr_ref_a", note: "stopped before saving" },
      attachments: [],
      optimisticDisplay: null,
    });
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    await threadsStore.getState().interrupt("ref_a");
    await flushUntilArrived(
      "the Stop's cancellation to land",
      async () => (await storage.getOutbox(canceled.clientMutationId))?.state === "canceled",
    );

    // The note editor's next save after the Stop: a fresh intent for the same
    // note, exactly what blurHumanNote enqueues (its blockedUnknown-gated
    // retry branch cannot release a canceled row).
    fake.on("notes/human/set", (params) => ({
      note: params.note ?? "",
      receipt: mutationReceipt(params.clientMutationId),
    }));
    await threadsStore.getState().setHumanNote("ref_a", "saved after the stop");

    // The newer save commits and takes the superseded canceled row with it:
    // the ref no longer carries any note row at all.
    await flushUntilArrived("the canceled note row to leave with the newer save's commit", async () =>
      (await storage.listOutbox("ref_a")).every((record) => record.method !== "notes/human/set"),
    );
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
  });

  // RoboRev PR #1873 medium, the press half: a clear's discard of canceled
  // rows is best-effort (a storage failure keeps them visible for exactly
  // this moment), so the press itself must refuse what the discard could not
  // remove. A row whose enqueue-time instance the clear already replaced has
  // no honest release: the daemon would fence the stale send, and releasing it
  // lets it dispatch against a thread the user just cleared.
  test("Retry refuses a row whose instance a clear already replaced, even when the clear-time discard failed", async () => {
    const storage = new MutationOutboxIndexedDB({
      // The design's own best-effort boundary, staged at the commit seam: the
      // clear's discard write fails, so the canceled row stays durable past
      // the clear (stop-cancellation-outbox §6).
      beforeCommit: (operation) => {
        if (operation === "discardCanceled") throw new Error("discard commit failed");
      },
    });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const canceled = await storage.enqueueIntent(queueIntent("canceled before the clear"));
    await storage.cancelUnattempted("ref_a");

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [], id: "thr_cleared" })));
    await threadsStore.getState().clearThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.threadId).toBe("thr_cleared");

    // The press the finding stages: the cleared thread is published, the
    // canceled row is still durable, and the user hits Retry. It must refuse.
    expect((await storage.getOutbox(canceled.clientMutationId))?.state).toBe("canceled");
    expect(await retryBlockedMutation(canceled.clientMutationId)).toBe(false);
    expect((await storage.getOutbox(canceled.clientMutationId))?.state).toBe("canceled");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
  });

  // RoboRev PR #1873 medium, the fresh review's instance identity mismatch,
  // the Retry half: the press-time instance check compared the row's
  // threadId with the press model's threadId, so a replacement that rotated
  // the instance while retaining the thread id passed it — and Retry
  // released and sent a mutation whose expectedInstanceId names an instance
  // the daemon already fenced away. The press must compare the same fused
  // identity the fence uses: the row's enqueue-time instance against the
  // press model's.
  test("Retry refuses a row whose instance a replacement rotated under the same thread id", async () => {
    const storage = new MutationOutboxIndexedDB({
      // The design's own best-effort boundary, staged at the commit seam:
      // the publication-driven discard of the rotated-away instance's rows
      // fails, so the canceled row stays durable past the replacement — and
      // the press itself must refuse what the discard could not remove.
      beforeCommit: (operation) => {
        if (operation === "discardCanceled") throw new Error("discard commit failed");
      },
    });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    const canceled = await storage.enqueueIntent({
      ...queueIntent("canceled before the replacement"),
      instanceId: "thr_ref_a",
    });
    await threadsStore.getState().interrupt("ref_a");
    await flushUntilArrived(
      "the Stop to cancel the queued row",
      async () => (await storage.getOutbox(canceled.clientMutationId))?.state === "canceled",
    );

    // The replacement publishes: same thread id, rotated instance.
    fake.on("thread/read", (params) =>
      readResponse(params.ref ?? "ref_a", {
        evener: { ref: "ref_a", capabilities: CAPABILITIES, instanceId: "cleared-instance", queue: { revision: 0 } },
      }),
    );
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.threadId).toBe("thr_ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.instanceId).toBe("cleared-instance");

    // The press: the discard could not remove the row, and it belongs to the
    // rotated-away instance, so Retry must refuse — the daemon would fence
    // the stale send anyway.
    expect(await retryBlockedMutation(canceled.clientMutationId)).toBe(false);
    expect((await storage.getOutbox(canceled.clientMutationId))?.state).toBe("canceled");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
  });

  // The same finding's persistence half: cleanup and Retry can only compare
  // the identity the durable row carries, so every store-enqueued intent
  // must persist its enqueue-time instance next to the thread id it already
  // carried — the raw model instance, fused with the threadId at comparison
  // time exactly the way expectedInstanceId is.
  test("a store-enqueued intent persists its enqueue-time instance identity", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    // A send the daemon never answers keeps the row durable to be read.
    fake.on("turn/queue", () => new Promise(() => {}));

    await threadsStore.getState().queue("ref_a", "sent under thr_ref_a");

    const queued = (await storage.listOutbox("ref_a")).find((record) => record.method === "turn/queue");
    expect(queued).toMatchObject({ threadId: "thr_ref_a", instanceId: "thr_ref_a" });
    storage.close();
  });

  // RoboRev PR #1873 medium, the ordering half: the cleared model must not
  // publish while the ref's canceled rows are still durable. Publish first and
  // every reader of the published state - a Retry press, a discovery scan -
  // faces a thread that claims to be cleared but still holds live canceled
  // rows in storage.
  test("the cleared model publishes only after the clear's canceled-row discard commits", async () => {
    const events: string[] = [];
    const storage = new MutationOutboxIndexedDB({
      beforeCommit: (operation) => {
        if (operation === "discardCanceled") events.push("discard-committed");
      },
    });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    await storage.enqueueIntent(queueIntent("canceled before the clear"));
    await storage.cancelUnattempted("ref_a");

    const unsubscribe = threadsStore.subscribe((state) => {
      if (state.threads.get("ref_a")?.threadId === "thr_cleared") events.push("cleared-published");
    });
    try {
      fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [], id: "thr_cleared" })));
      await threadsStore.getState().clearThread("ref_a");
    } finally {
      unsubscribe();
    }

    // The discard reached its commit boundary before the cleared model first
    // published, never after. (The store's clear path publishes through two
    // setStates, so "cleared-published" can legitimately appear more than
    // once; the ordering is the assertion.)
    expect(events.indexOf("discard-committed")).toBeGreaterThan(-1);
    expect(events.indexOf("discard-committed")).toBeLessThan(events.indexOf("cleared-published"));
  });

  // RoboRev PR #1873 medium: §6's removal was tied to the LOCAL clear response
  // (applyClearResponse), so a clear settled by ANOTHER tab - whose response
  // and best-effort removal never reach this store - left this tab's canceled
  // rows attached to the cleared thread, still offering Retry. The model
  // transition every tab observes on its own (the daemon's resync push, this
  // tab's re-read) is the authoritative signal - the BroadcastChannel wakeup
  // is a timing hint, never an authority - so the removal rides it, scoped to
  // the instance the transition provably replaced.
  test("a clear settled by another tab is still removed when this tab observes the replacement instance", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = `cross-tab-clear-${crypto.randomUUID()}`;
    const tabB = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(tabB);
    const clientB = connectMutationClient();
    await ensureActiveMutationTarget(clientB, "ref_a");

    // The Stop residue: a canceled row enqueued against the live instance,
    // canceled through the store's real Stop path (which also mints the
    // mutation runtime this tab's observation needs).
    const canceled = await tabB.enqueueIntent(queueIntent("canceled before the clear"));
    clientB.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    await threadsStore.getState().interrupt("ref_a");
    await flushUntilArrived(
      "the Stop to cancel the queued row",
      async () => (await tabB.getOutbox(canceled.clientMutationId))?.state === "canceled",
    );

    // Another tab: a second storage connection and dispatcher - the package's
    // own shape, no threads store of ours - dispatches and settles a
    // thread/clear over the same durable database. Its response never reaches
    // this tab's store, and this staging wires none of its own §6 cleanup, so
    // only the OBSERVING tab's transition can remove the row.
    const tabA = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    const clientA = new FakeClient("ready");
    clientA.on("thread/clear", (params) =>
      clearResponse(params, testThread("ref_a", { turns: [], id: "thr_cleared" })),
    );
    const dispatcherA = new MutationDispatcher(tabA, { getClient: () => clientA });
    await tabA.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "thread/clear",
      payload: { ref: "ref_a", expectedInstanceId: "thr_ref_a" },
      attachments: [],
      optimisticDisplay: { method: "thread/clear" },
    });
    await dispatcherA.dispatchTargets(["ref_a"]);
    expect(clientA.calls.some((call) => call.method === "thread/clear")).toBe(true);

    // This tab observes the clear the way every subscribed tab does: the
    // daemon's resync push lands, this tab re-reads, and the replacement
    // instance publishes over the one the canceled row belonged to.
    clientB.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { id: "thr_cleared" }));
    act(() => {
      clientB.emitNotification({ method: "evener/thread/resync", params: { ref: "ref_a", threadId: "thr_ref_a" } });
    });

    // The observation drove the removal: the superseded instance's canceled
    // row left with it, in this tab.
    await flushUntilArrived(
      "the observing tab to discard the superseded instance's canceled row",
      async () => (await tabB.getOutbox(canceled.clientMutationId)) === undefined,
    );
    tabA.close();
  });

  // RoboRev PR #1873 medium, the fresh review's instance identity mismatch,
  // the cross-tab-removal half: §6's removal detected the replaced instance
  // by comparing threadId, but the fencing identity is `instanceId ??
  // threadId` (the expectedInstanceId every payload carries), so a
  // replacement that rotated the instance while retaining the thread id was
  // invisible to it — the superseded instance's canceled rows stayed
  // attached, still offering Retry, until the thread went away. The
  // detection and the row matching must both compare the fused identity.
  test("an instance rotated under the same thread id still removes the superseded instance's canceled rows", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = `cross-tab-instance-${crypto.randomUUID()}`;
    const tabB = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(tabB);
    const clientB = connectMutationClient();
    await ensureActiveMutationTarget(clientB, "ref_a");

    // The Stop residue: a canceled row carrying the enqueue-time instance
    // identity this tab's model presented (the fixture's instanceId equals
    // its thread id).
    const canceled = await tabB.enqueueIntent({ ...queueIntent("canceled before the clear"), instanceId: "thr_ref_a" });
    clientB.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    await threadsStore.getState().interrupt("ref_a");
    await flushUntilArrived(
      "the Stop to cancel the queued row",
      async () => (await tabB.getOutbox(canceled.clientMutationId))?.state === "canceled",
    );

    // Another tab settles a thread/clear that rotates the instance while
    // RETAINING the thread id — the exact shape threadId-only detection
    // cannot see.
    const tabA = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    const clientA = new FakeClient("ready");
    clientA.on("thread/clear", (params) =>
      clearResponse(
        params,
        testThread("ref_a", {
          turns: [],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, instanceId: "cleared-instance", queue: { revision: 0 } },
        }),
      ),
    );
    const dispatcherA = new MutationDispatcher(tabA, { getClient: () => clientA });
    await tabA.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "thread/clear",
      payload: { ref: "ref_a", expectedInstanceId: "thr_ref_a" },
      attachments: [],
      optimisticDisplay: { method: "thread/clear" },
    });
    await dispatcherA.dispatchTargets(["ref_a"]);

    // This tab observes the replacement the way every subscribed tab does:
    // the resync push lands, this tab re-reads, and the rotated instance
    // publishes over the one the canceled row belonged to — same thread id.
    clientB.on("thread/read", (params) =>
      readResponse(params.ref ?? "ref_a", {
        evener: { ref: "ref_a", capabilities: CAPABILITIES, instanceId: "cleared-instance", queue: { revision: 0 } },
      }),
    );
    act(() => {
      clientB.emitNotification({ method: "evener/thread/resync", params: { ref: "ref_a", threadId: "thr_ref_a" } });
    });
    await flushUntilArrived(
      "the rotated instance to publish",
      () => threadsStore.getState().threads.get("ref_a")?.instanceId === "cleared-instance",
    );

    // The observation drove the removal on the fused identity: the
    // rotated-away instance's canceled row left with it, in this tab.
    await flushUntilArrived(
      "the observing tab to discard the rotated-away instance's canceled row",
      async () => (await tabB.getOutbox(canceled.clientMutationId)) === undefined,
    );
    tabA.close();
  });

  // RoboRev PR #1873 medium, examined and REFUTED: "during initial hydration
  // putThreadModels runs before the runtime exists, so a replacement-instance
  // transition leaves stale canceled rows permanently." No publication can run
  // pre-runtime: the mutation runtime is minted unconditionally by the FIRST
  // client-ready (rewireClient's direct handleReady call for an already-ready
  // client; handleReady's opening getMutationRuntime), and every
  // path that can observe a replacement instance - tracked and watched
  // hydrations, the clear response, escalation resolution, older-page merges -
  // completes a client RPC after that ready. The guard's early return is
  // reachable only when no runtime can EVER exist (IndexedDB unavailable to
  // the tab), where there are no durable rows to clean, so returning is the
  // correct behavior there. This test pins the refutation at the earliest
  // publication any page can make - a seeded old-instance row, a fresh page's
  // very first hydration carrying the replacement instance, and no mutation
  // ever sent, so the runtime owes its existence to the ready flow alone - and
  // goes red if a future change breaks the ready-time mint or the observation.
  test("the earliest possible replacement-instance publication already finds a runtime and discards the superseded rows", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    // Another tab's Stop residue against the OLD instance, plus a canceled row
    // of an instance no transition ever supersedes: the removal is scoped to
    // the instance the publication provably replaced, so that row must stay.
    const superseded = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_old",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "canceled against the old instance" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    const unrelated = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_keep",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "canceled against an unrelated instance" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_a");

    // The page opens tracking the OLD instance, seeded the way a list row
    // hydrates from hub state. Nothing has minted a runtime yet: no mutation
    // was sent and no hydration has completed.
    // The old instance seeds with its full identity (the fixture's id override
    // rotates threadId and instanceId together — the shape a real replacement
    // carries, and the only shape the fused detection can observe).
    threadsStore.setState({
      threads: new Map([["ref_a", hydrateThread(readResponse("ref_a", { id: "thr_old" }), "ref_a", 1000)]]),
    });

    // The client connects - ready - and the ready flow mints the runtime
    // BEFORE any publication can complete. The first hydration then publishes
    // the REPLACEMENT instance (another tab cleared the thread).
    const fake = connectMutationClient();
    fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { id: "thr_new" }));
    await threadsStore.getState().ensureThread("ref_a");

    // The transition's cleanup ran at that earliest publication: the
    // superseded instance's row left, the unrelated instance's row stays.
    await flushUntilArrived(
      "the earliest publication's transition cleanup to discard the superseded row",
      async () => (await storage.getOutbox(superseded.clientMutationId)) === undefined,
    );
    expect((await storage.getOutbox(unrelated.clientMutationId))?.state).toBe("canceled");
  });

  // RoboRev PR #1873 medium, the pin half: the cross-tab removal notified
  // persistence only when it deleted a row and never refreshed the pin set.
  // publishAndReconcileThreadHydration's own pin refresh races the
  // fire-and-forget discardCanceledOfInstance write, so the refresh can read
  // the row before the discard commits, pin the ref, and leave the pin stale
  // once the row is gone - and releaseThread returns early on a pinned ref,
  // so the cleared thread's model stays in `threads` forever after its last
  // pane closes. This test pins that whole chain, with the race's order made
  // deterministic at the storage seam: the cleanup write is held until the
  // hydration's own pin refresh has read the row, then commits.
  test("a cross-tab cleanup that removes the last row also refreshes the pin releaseThread waits on", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const storage = new MutationOutboxIndexedDB();
      setMutationStorageForTests(storage);
      // The ref's ONLY durable row is another tab's Stop residue against the
      // old instance; the tracked model is that old instance, pane-held.
      const canceled = await storage.enqueueIntent({
        targetRef: "ref_a",
        threadId: "thr_old",
        method: "turn/queue",
        payload: { ref: "ref_a", input: [{ type: "text", text: "canceled against the old instance" }] },
        attachments: [],
        optimisticDisplay: null,
      });
      await storage.cancelUnattempted("ref_a");
      threadsStore.setState({
        threads: new Map([["ref_a", hydrateThread(readResponse("ref_a", { id: "thr_old" }), "ref_a", 1000)]]),
      });
      const fake = connectMutationClient();
      fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { id: "thr_old" }));
      await threadsStore.getState().ensureThread("ref_a");

      // Hold the cleanup's durable write at the seam: the discard is issued by
      // the transition's publication, and the write that lands is the real
      // one - only its completion waits for the test.
      const realDiscard = storage.discardCanceledOfInstance.bind(storage);
      const discardReached = deferred<void>();
      const discardRelease = deferred<void>();
      storage.discardCanceledOfInstance = (targetRef: string, instanceThreadId: string) => {
        discardReached.resolve();
        return discardRelease.promise.then(() => realDiscard(targetRef, instanceThreadId));
      };

      // The re-read every subscribed tab runs when the daemon's resync push
      // lands: the replacement instance publishes, the cleanup fires (held at
      // the seam), and the hydration's own pin refresh reads the still-durable
      // row - the raced order that leaves the pin stale without this fix.
      fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { id: "thr_new" }));
      await threadsStore.getState().refreshThread("ref_a");
      await discardReached.promise;
      // The refresh drive also schedules a dispatch whose tail re-reads pins;
      // hold the discard until that last scheduled refresh has completed too,
      // so every pin read the drive can make has seen the pre-discard view.
      // Releasing after that leaves the pin stale - the finding's chain -
      // unless the cleanup's own tail refreshes it.
      await flushIndexedDBUntil(() => false);
      discardRelease.resolve();

      // The pane closes. A stale pin makes releaseThread return early and the
      // cleared thread's model leak; the cleanup's own pin refresh must let
      // the release drop it.
      threadsStore.getState().releaseThread("ref_a");
      await flushUntilArrived(
        "the superseded row to leave",
        async () => (await storage.getOutbox(canceled.clientMutationId)) === undefined,
      );
      await flushUntilArrived(
        "the released pane's model to leave threads",
        () => !threadsStore.getState().threads.has("ref_a"),
      );
    } finally {
      vi.useRealTimers();
    }
  });

  // The same finding's notify half, the cross-tab path: a cleanup that removes
  // nothing still has to notify persistence (the rule the deletion-fence path
  // already carries) - zero says what THIS tab's write removed, never what
  // another tab may have removed from under this tab's cached projection.
  test("a cross-tab cleanup that removes nothing still notifies persistence", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { id: "thr_old" }));
    await threadsStore.getState().ensureThread("ref_a");

    let notified = false;
    const unsubscribe = subscribeMutationPersistence((refs) => {
      if (refs.includes("ref_a")) notified = true;
    });
    try {
      // The superseded instance holds no canceled rows: the cleanup succeeds
      // with zero, and the notify is the whole observable.
      fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { id: "thr_new" }));
      await threadsStore.getState().refreshThread("ref_a");
      await flushUntilArrived("the zero-row cross-tab cleanup to notify persistence", () => notified);
    } finally {
      unsubscribe();
    }
  });

  // RoboRev PR #1873 medium, the fresh review's zero-deletion half: the
  // cross-tab cleanup refreshed the mutation pin only when it deleted at
  // least one row, but zero is exactly what another tab's identical cleanup
  // leaves THIS tab's write — the finding's interleave: the row was this
  // tab's pin, the other tab removed it, and this tab's own cleanup then
  // succeeds with nothing to remove. The stale pin keeps releaseThread from
  // dropping the cleared model, so the refresh must follow every successful
  // cleanup, zero included — the local discard path's own rule.
  test("a cross-tab cleanup that removes nothing still refreshes the pin releaseThread waits on", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const storage = new MutationOutboxIndexedDB();
      setMutationStorageForTests(storage);
      // The ref's ONLY durable row is another tab's Stop residue against the
      // old instance; the tracked model is that old instance, pane-held.
      const canceled = await storage.enqueueIntent({
        targetRef: "ref_a",
        threadId: "thr_old",
        method: "turn/queue",
        payload: { ref: "ref_a", input: [{ type: "text", text: "canceled against the old instance" }] },
        attachments: [],
        optimisticDisplay: null,
      });
      await storage.cancelUnattempted("ref_a");
      threadsStore.setState({
        threads: new Map([["ref_a", hydrateThread(readResponse("ref_a", { id: "thr_old" }), "ref_a", 1000)]]),
      });
      const fake = connectMutationClient();
      fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { id: "thr_old" }));
      await threadsStore.getState().ensureThread("ref_a");

      // Hold this tab's cleanup at the seam, the finding's order made
      // deterministic: the re-read publishes the replacement, the cleanup
      // issues (held), and the hydration's own pin refresh reads the
      // still-durable row and pins the ref.
      const realDiscard = storage.discardCanceledOfInstance.bind(storage);
      const discardReached = deferred<void>();
      const discardRelease = deferred<void>();
      storage.discardCanceledOfInstance = (targetRef: string, supersededInstanceId: string) => {
        discardReached.resolve();
        return discardRelease.promise.then(() => realDiscard(targetRef, supersededInstanceId));
      };
      fake.on("thread/read", (params) => readResponse(params.ref ?? "ref_a", { id: "thr_new" }));
      await threadsStore.getState().refreshThread("ref_a");
      await discardReached.promise;
      await flushIndexedDBUntil(() => false);

      // The other tab removes the row while this tab's cleanup is still
      // held: the durable row leaves through the OTHER connection, so the
      // write this tab finally issues finds nothing to remove — the zero the
      // finding names.
      const tabA = new MutationOutboxIndexedDB();
      await tabA.discardCanceledOfInstance("ref_a", "thr_old");
      await flushUntilArrived(
        "the other tab's removal to land",
        async () => (await storage.getOutbox(canceled.clientMutationId)) === undefined,
      );
      discardRelease.resolve();

      // The pane closes. A stale pin makes releaseThread return early and
      // the cleared thread's model leak; the zero-row cleanup's own pin
      // refresh must let the release drop it.
      threadsStore.getState().releaseThread("ref_a");
      await flushUntilArrived(
        "the released pane's model to leave threads",
        () => !threadsStore.getState().threads.has("ref_a"),
      );
      tabA.close();
    } finally {
      vi.useRealTimers();
    }
  });

  // RoboRev PR #1873 medium, the fresh review's fire-and-forget supersede
  // discard: the post-settle cleanup of superseded canceled note rows
  // committed with no storage-change notification and no pin refresh, so the
  // settle's own notification — the dispatcher's post-settlement refresh,
  // already passed — was the last word on the ref. When the discard commits
  // after that (its write is asynchronous by design: the settle's commit
  // boundary must not wait on the scan), the removal is invisible to every
  // projection and pin this runtime holds until some unrelated storage event
  // happens to fire. The cleanup's own commit must notify the owning
  // runtime — the same zero-included rule the discard paths carry — and the
  // pin refresh follows the notification, so a releaseThread waiting on the
  // pin drops the model whose row just left.
  test("the supersede discard's own commit notifies the owning runtime", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    let notifies = 0;
    const unsubscribe = subscribeMutationPersistence((refs) => {
      if (refs.includes("ref_a")) notifies += 1;
    });
    try {
      // The class's own commit-boundary seam announces the discard's moment:
      // its write body is done, the durable commit event is pending, and every
      // notification the settle chain emits has already passed.
      const discardBoundaryReached = deferred<void>();
      const storage = new MutationOutboxIndexedDB({
        beforeCommit: (operation) => {
          if (operation === "discardCanceled") discardBoundaryReached.resolve();
        },
      });
      setMutationStorageForTests(storage);
      const fake = connectMutationClient();
      await ensureActiveMutationTarget(fake, "ref_a");
      fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
      fake.on("notes/human/set", (params) => ({
        note: params.note ?? "",
        receipt: mutationReceipt(params.clientMutationId),
      }));
      // The Stop's canceled note row is the ref's pin (the interrupt's
      // dispatch tail refreshes the pin with the row still durable).
      const canceledNote = await storage.enqueueIntent({
        targetRef: "ref_a",
        threadId: "thr_ref_a",
        method: "notes/human/set",
        payload: { ref: "ref_a", expectedInstanceId: "thr_ref_a", note: "stopped before saving" },
        attachments: [],
        optimisticDisplay: null,
      });
      await threadsStore.getState().interrupt("ref_a");
      await flushUntilArrived(
        "the Stop to cancel the note row",
        async () => (await storage.getOutbox(canceledNote.clientMutationId))?.state === "canceled",
      );
      await flushIndexedDBUntil(() => false, 4);

      // The note editor's next save after the Stop — the store's real path:
      // enqueue, dispatch, settle, and the post-settle supersede discard
      // (its commit boundary announces through the seam above).
      const save = threadsStore.getState().setHumanNote("ref_a", "saved after the stop");
      await save;
      await discardBoundaryReached.promise;
      // The settle's own notification has already passed; this is the count
      // it leaves behind at the discard's commit boundary.
      const notifiesBeforeCommit = notifies;
      // The commit must itself notify the owning runtime: nothing else runs
      // after it — the settle's notification is in the past, and a
      // fire-and-forget write with no notification of its own leaves the
      // removal invisible to every projection and pin this runtime holds.
      await flushUntilArrived(
        "the discard's commit to notify the owning runtime",
        () => notifies > notifiesBeforeCommit,
      );
      // The superseded row did leave, and the pin the discard took with it
      // follows the notification's refresh: the pane closes and the model
      // drops instead of staying pinned for a row that no longer exists.
      await flushUntilArrived(
        "the supersede discard to commit",
        async () => (await storage.getOutbox(canceledNote.clientMutationId)) === undefined,
      );
      threadsStore.getState().releaseThread("ref_a");
      await flushUntilArrived(
        "the released pane's model to leave threads",
        () => !threadsStore.getState().threads.has("ref_a"),
      );
    } finally {
      unsubscribe();
      vi.useRealTimers();
    }
  });

  // RoboRev PR #1873 low, the notify half: a zero-discard success still has
  // to notify persistence. Zero says what THIS tab's write removed - never what
  // another tab removed from under this tab's cached projection, and the
  // notify is what refreshes that projection (the queue strip's rows, the
  // note editor's statuses).
  test("a deletion fence's zero-discard cleanup still notifies persistence", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    let notified = false;
    const unsubscribe = subscribeMutationPersistence((refs) => {
      if (refs.includes("ref_gone")) notified = true;
    });
    try {
      fake.on("thread/read", () => {
        throw new WireError("target has been deleted: local:ref_gone", -32001, {
          evenerErrorInfo: "actionUnavailable",
          mutationOutcome: "targetDeleted",
          retryDisposition: "none",
        });
      });
      // The ref holds no rows at all: the fence's discard succeeds with zero -
      // the exact success whose notification was suppressed.
      void threadsStore.getState().ensureThread("ref_gone");
      await waitFor(() => {
        expect(threadsStore.getState().deletedRefs.has("ref_gone")).toBe(true);
      });
      await flushUntilArrived("the zero-discard cleanup to notify persistence", () => notified);
    } finally {
      unsubscribe();
    }
  });

  // RoboRev PR #1873 low, the pins half: the discard that removes a ref's
  // last row must refresh the mutation pin. Without the refresh the stale pin
  // keeps releaseThread from dropping the model of a thread the hub has
  // proven deleted.
  test("the deletion fence's discard refreshes the pin so the last release drops the model", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_gone");
    // A pinned durable row: enqueued durably, then canceled by a real Stop
    // whose interrupt settles.
    const canceled = await storage.enqueueIntent({
      targetRef: "ref_gone",
      threadId: "thr_ref_gone",
      method: "turn/queue",
      payload: { ref: "ref_gone", input: [{ type: "text", text: "canceled before the deletion" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    await threadsStore.getState().interrupt("ref_gone");
    await flushUntilArrived(
      "the Stop to cancel the queued row",
      async () => (await storage.getOutbox(canceled.clientMutationId))?.state === "canceled",
    );
    // Let the interrupt's own dispatch tail run out of the way; the canceled
    // row is the ref's pin, exactly the pin the fence's discard must refresh.
    await flushIndexedDBUntil(() => false, 4);

    // The hub's deletion fence lands: the canceled row leaves with the ref.
    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    await threadsStore
      .getState()
      .refreshThread("ref_gone")
      .catch(() => undefined);
    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("ref_gone")).toBe(true);
    });
    await flushUntilArrived(
      "the fence's discard to remove the canceled row",
      async () => (await storage.getOutbox(canceled.clientMutationId)) === undefined,
    );
    await flushIndexedDBUntil(() => false, 4);

    // The pane goes away: with the pin refreshed, the last release drops the
    // deleted thread's model instead of holding it for a row that no longer
    // exists.
    threadsStore.getState().releaseThread("ref_gone");
    expect(threadsStore.getState().threads.has("ref_gone")).toBe(false);
  });
});

describe("pending shared membership acquisition", () => {
  test("a resync surviving a timed-out first acquisition retries in its current owner", async () => {
    const fake = connectFakeClient();
    const initial = deferred<ThreadReadResponse>();
    let reads = 0;
    fake.on("thread/read", () =>
      ++reads === 1
        ? initial.promise
        : readResponse("ref_a", { turns: [{ id: "turn_current", status: "completed", itemsView: "full", items: [] }] }),
    );
    const first = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads === 1);
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushMicrotasks();
    expect(reads).toBe(1);
    initial.reject(new RequestTimeoutError("first acquisition timed out"));
    await flushUntil(() => scheduledHydrationRetries.length > 0);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    runScheduledHydrationRetry();
    await first;
    expect(reads).toBe(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_current");
    expect(
      fake.calls
        .filter((call) => call.method === "thread/read")
        .every((call) => (call.params as { subscribe: boolean }).subscribe),
    ).toBe(true);
    threadsStore.getState().releaseThread("ref_a");
  });
  test("a rich watcher surviving a failed lean acquisition retries with its required turns", async () => {
    const fake = connectFakeClient(),
      initial = deferred<ThreadReadResponse>();
    const includes: boolean[] = [];
    fake.on("thread/read", (params) => {
      includes.push(params.includeTurns === true);
      return includes.length === 1
        ? initial.promise
        : readResponse("ref_a", { turns: [{ id: "rich", status: "completed", itemsView: "full", items: [] }] });
    });
    const lean = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => includes.length === 1);
    const rich = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    await flushMicrotasks();
    expect(includes).toEqual([false]);
    initial.reject(new RequestTimeoutError("lean acquisition timed out"));
    await flushUntil(() => scheduledHydrationRetries.length > 0);
    runScheduledHydrationRetry();
    await Promise.all([lean, rich]);
    expect(includes).toEqual([false, true]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("rich");
    threadsStore.getState().releaseWatchedThread("ref_a");
    threadsStore.getState().releaseWatchedThread("ref_a");
  });
  test("release and reacquire waits for pending membership and fences the retired model", async () => {
    const fake = connectFakeClient(),
      initial = deferred<ThreadReadResponse>(),
      replacement = deferred<ThreadReadResponse>();
    let reads = 0;
    fake.on("thread/read", () => (++reads === 1 ? initial.promise : replacement.promise));
    const first = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads === 1);
    threadsStore.getState().releaseThread("ref_a");
    const second = threadsStore.getState().ensureThread("ref_a");
    await flushMicrotasks();
    expect(reads).toBe(1);
    initial.resolve(
      readResponse("ref_a", { turns: [{ id: "old", status: "completed", itemsView: "full", items: [] }] }),
    );
    await flushUntil(() => reads === 2);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    replacement.resolve(
      readResponse("ref_a", { turns: [{ id: "new", status: "completed", itemsView: "full", items: [] }] }),
    );
    await Promise.all([first, second]);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("new");
    threadsStore.getState().releaseThread("ref_a");
  });
});
