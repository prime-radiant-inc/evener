import "fake-indexeddb/auto";
import type {
  AnyNotification,
  ConnectionState,
  QueueState,
  Thread,
  ThreadCapabilities,
  ThreadReadResponse,
  ThreadSubscriptionLease,
} from "@evener/appwire-client";
import { acquireThreadSubscription, RequestTimeoutError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { act, cleanup, renderHook } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import {
  resetSubagentModuleStoreForTests,
  turnScopeKey,
  upsertSubagentRow,
  useSubagentRow,
} from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  setProductionEntryCacheForTests,
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
let mutations: MutationOutboxIndexedDB;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetWorkspaceStoreForTests();
  resetSubagentModuleStoreForTests();
  const indexedDB = new IDBFactory();
  cache = new SessionEntryCache({ indexedDB, keyRange: IDBKeyRange, databaseName: "parallel-watched-entry" });
  mutations = new MutationOutboxIndexedDB({ indexedDB, databaseName: "parallel-watched-mutations" });
  setProductionEntryCacheForTests(cache);
  setMutationStorageForTests(mutations);
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
  cleanup();
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  resetThreadsStoreForTests();
  cache.close();
  mutations.close();
  vi.restoreAllMocks();
});

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
    // This watch explicitly requested turns, including their recorded bodies.
    expect(model?.turns[1]?.items[0]?.text).toBe("skillguard watched turn complete");
    expect(model?.turns[1]?.items).toHaveLength(1);
    expect(model?.turns[1]?.status).toBe("completed");
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("a watched refresh does not preserve a same-numbered turn from a replaced instance", async () => {
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

    const b = new FakeClient("ready");
    b.on("thread/read", (params) => ({
      ...versionedReadResponse("ref_a", {
        turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
        evener: {
          ref: "ref_a",
          instanceId: "replacement-instance",
          capabilities: CAPABILITIES,
          queue: { revision: 0 },
          activeTurnId: "turn_m6",
        },
      }),
      snapshot: { incarnation: "inc-2", length: 1 },
      requestGeneration: params.requestGeneration,
    }));
    connectionStore.getState().connect(b);
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a")?.instanceId === "replacement-instance");

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

    act(() => {
      threadsStore.getState().releaseThread("ref_a");
    });
    // R36: the finite final-read owner retains the pane model at this boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true); // the watch survives
    expect(row.current).toBeUndefined();

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
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
