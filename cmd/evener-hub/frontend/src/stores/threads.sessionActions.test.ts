import "fake-indexeddb/auto";
import {
  type AnyNotification,
  type ConnectionState,
  hydrateThread,
  type MethodName,
  type MethodTypes,
  type NotesHumanSetResponse,
  type QueueState,
  RequestTimeoutError,
  type Thread,
  type ThreadCapabilities,
  type ThreadClearResponse,
  type ThreadReadResponse,
  type TurnStartResponse,
  WireError,
} from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { IDBDatabase, IDBFactory, IDBKeyRange, IDBVersionChangeEvent } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  pendingTurnEntries,
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { recoveryComposerDraft } from "../panes/session/composer/recovery/recoveryDraft";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { connectionStore } from "./connection";
import { editHumanNote, syncHumanNote, useHumanNoteDraft } from "./humanNoteDrafts";
import { IDBUpgradeError } from "./idbConnection";
import { MutationDispatcher } from "./mutationDispatcher";
import type { MutationOutboxRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { settleProjectionWorkForTests } from "./projectionWork";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  blockedUpgradeRequest,
  holdIndexedDBEvent,
  holdNextWriteTransaction,
  wedgeDatabaseOpens,
} from "./testing/stalledIndexedDB";
import {
  ConflictError,
  discardRecoveryMutation,
  hasBlockedUnknown,
  hasQueuedNonSend,
  installBackpressureRetrySchedulerForTests,
  installHydrationRetrySchedulerForTests,
  notifyReadyForMutationDispatch,
  putThreadModel,
  readMutationPersistence,
  resendRecoveryMutation,
  resetThreadsStoreForTests,
  resumeOnlyDispatchable,
  resumeOnlyModel,
  resumeStopBaseline,
  resumeStopFence,
  retryBlockedMutation,
  sendResumesModel,
  setMutationStorageForTests,
  setProductionEntryCacheForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "./threads";

interface ScheduledHydrationRetry {
  attempt: number;
  retry: () => void;
  cancelled: boolean;
}
let scheduledHydrationRetries: ScheduledHydrationRetry[] = [];
let restoreHydrationRetryScheduler: (() => void) | null = null;
let cache: SessionEntryCache;
beforeEach(async () => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
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
  // The original mutation-database reset runs only on this imported fake IDB.
  expect(indexedDB).toBeInstanceOf(IDBFactory);
  await deleteMutationDatabase();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `session-actions-entry-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  cleanup();
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});

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
  const client = connectionStore.getState().client;
  const request = (
    client instanceof FakeClient
      ? client.calls
          .filter(
            (call) =>
              call.method === "thread/read" && (call.params as MethodTypes["thread/read"]["params"]).ref === ref,
          )
          .at(-1)?.params
      : undefined
  ) as MethodTypes["thread/read"]["params"] | undefined;
  return {
    thread: testThread(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
    requestGeneration: request?.requestGeneration,
  };
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

// flushMicrotasks drains a bounded number of microtask turns without waiting on
// any condition - for a caller that only wants pending continuations to run
// before its next assertion. (A predicate that is never true would cost at most
// the same 20 hops; this makes that intent explicit rather than reading as a
// wait on the impossible.)
async function flushMicrotasks(turns = 20): Promise<void> {
  for (let i = 0; i < turns; i += 1) await Promise.resolve();
}

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

function runScheduledHydrationRetry(index = 0): void {
  const scheduled = scheduledHydrationRetries[index];
  expect(scheduled, `no hydration retry scheduled at index ${index}`).toBeDefined();
  expect(scheduled?.cancelled).toBe(false);
  scheduled?.retry();
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe("useThreadsStore session actions (setModel/setReasoningEffort/setGoal/rename/compact/shutdown/clearThread/forkFromTurn)", () => {
  test("setModel sends thread/model/set with {ref, modelProvider, model}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/model/set", () => ({}));

    await threadsStore.getState().setModel("ref_a", "anthropic", "claude-opus-4-1");

    const call = fake.calls.find((c) => c.method === "thread/model/set");
    expect(call?.params).toEqual({ ref: "ref_a", modelProvider: "anthropic", model: "claude-opus-4-1" });
  });

  test("setVisionModel sends thread/vision-model/set with {ref, visionModel}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/vision-model/set", () => ({}));

    await threadsStore.getState().setVisionModel("ref_a", "off");

    const call = fake.calls.find((c) => c.method === "thread/vision-model/set");
    expect(call?.params).toEqual({ ref: "ref_a", visionModel: "off" });
  });

  test("setVisionModel maps a Conflict rejection to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("thread/vision-model/set", () => {
      throw new WireError("vision model unavailable", -32013, { evenerErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().setVisionModel("ref_a", "off")).rejects.toBeInstanceOf(ConflictError);
  });

  test("setReasoningEffort sends thread/reasoning-effort/set with {ref, reasoningEffort: level}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/reasoning-effort/set", () => ({}));

    await threadsStore.getState().setReasoningEffort("ref_a", "high");

    const call = fake.calls.find((c) => c.method === "thread/reasoning-effort/set");
    expect(call?.params).toEqual({ ref: "ref_a", reasoningEffort: "high" });
  });

  test("setGoal sends goal/set and commits the successful result to the tracked model", async () => {
    const fake = connectFakeClient();
    fake.on("goal/set", () => ({ started: true }));

    threadsStore.setState({
      threads: new Map([["ref_a", hydrateThread(readResponse("ref_a"), "ref_a", 1000)]]),
    });

    const result = await threadsStore.getState().setGoal("ref_a", "ship wave 5");

    const call = fake.calls.find((c) => c.method === "goal/set");
    expect(call?.params).toEqual({ ref: "ref_a", objective: "ship wave 5" });
    expect(result).toEqual({ started: true });
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual({
      objective: "ship wave 5",
      status: "active",
      iterations: 0,
    });

    await threadsStore.getState().setGoal("ref_a", "");
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toBeNull();
  });

  test("setGoal does not overwrite a newer accepted goal notification in either tracked map", async () => {
    const fake = connectFakeClient();
    let resolveSetGoal: (response: { started: boolean }) => void = () => {
      throw new Error("goal/set handler was not reached");
    };
    const requestReachedHandler = nextHandledRequest(
      fake,
      "goal/set",
      () =>
        new Promise((resolve) => {
          resolveSetGoal = resolve;
        }),
    );

    const model = hydrateThread(readResponse("ref_a"), "ref_a", 1000);
    threadsStore.setState({
      threads: new Map([["ref_a", model]]),
      watchedThreads: new Map([["ref_a", model]]),
    });

    const pending = threadsStore.getState().setGoal("ref_a", "local objective");
    await requestReachedHandler;

    const pushedGoal = { objective: "newer pushed objective", status: "active", iterations: 4 };
    fake.emitNotification({
      method: "evener/goal/updated",
      params: { threadId: model.threadId, ref: "ref_a", goal: pushedGoal },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(pushedGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(pushedGoal);

    resolveSetGoal({ started: true });
    await pending;

    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(pushedGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(pushedGoal);
  });

  test("setGoal does not overwrite a newer authoritative hydration", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    let resolveSetGoal: (response: { started: boolean }) => void = () => {
      throw new Error("goal/set handler was not reached");
    };
    const setGoalReachedHandler = nextHandledRequest(
      fake,
      "goal/set",
      () =>
        new Promise((resolve) => {
          resolveSetGoal = resolve;
        }),
    );
    const pending = threadsStore.getState().setGoal("ref_a", "local objective");
    await setGoalReachedHandler;

    const refreshReads: Array<(response: ThreadReadResponse) => void> = [];
    let resolveRefreshReadsReached: () => void = () => {
      throw new Error("both hydration handlers were not reached");
    };
    const refreshReadsReached = new Promise<void>((resolve) => {
      resolveRefreshReadsReached = resolve;
    });
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          refreshReads.push(resolve);
          if (refreshReads.length === 2) resolveRefreshReadsReached();
        }),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await refreshReadsReached;

    const hydratedGoal = { objective: "authoritative objective", status: "active" as const, iterations: 6 };
    const authoritativeResponse = readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        goal: hydratedGoal,
      },
    });
    for (const resolveRead of refreshReads) resolveRead(authoritativeResponse);
    await flushUntil(
      () =>
        threadsStore.getState().threads.get("ref_a")?.goal?.objective === hydratedGoal.objective &&
        threadsStore.getState().watchedThreads.get("ref_a")?.goal?.objective === hydratedGoal.objective,
    );
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(hydratedGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(hydratedGoal);

    resolveSetGoal({ started: true });
    await pending;

    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(hydratedGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(hydratedGoal);
  });

  test("setGoal fallback survives an unaccepted contradictory notification", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    const refreshReads: Array<(response: ThreadReadResponse) => void> = [];
    let resolveRefreshReadsReached: () => void = () => {
      throw new Error("both hydration handlers were not reached");
    };
    const refreshReadsReached = new Promise<void>((resolve) => {
      resolveRefreshReadsReached = resolve;
    });
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          refreshReads.push(resolve);
          if (refreshReads.length === 2) resolveRefreshReadsReached();
        }),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await refreshReadsReached;

    let resolveSetGoal: (response: { started: boolean }) => void = () => {
      throw new Error("goal/set handler was not reached");
    };
    const requestReachedHandler = nextHandledRequest(
      fake,
      "goal/set",
      () =>
        new Promise((resolve) => {
          resolveSetGoal = resolve;
        }),
    );

    const pending = threadsStore.getState().setGoal("ref_a", "local objective");
    await requestReachedHandler;
    fake.emitNotification({
      method: "evener/goal/updated",
      params: {
        threadId: "thr_conflicting",
        ref: "ref_a",
        goal: { objective: "contradictory objective", status: "active", iterations: 9 },
      },
    });

    resolveSetGoal({ started: true });
    await pending;

    const fallbackGoal = { objective: "local objective", status: "active", iterations: 0 };
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(fallbackGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(fallbackGoal);

    threadsStore.getState().releaseThread("ref_a");
    threadsStore.getState().releaseWatchedThread("ref_a");
    for (const resolveRead of refreshReads) resolveRead(readResponse("ref_a"));
  });

  test("buffered accepted goal notification invalidates a pending response fallback", async () => {
    const fake = connectFakeClient();
    let resolveRead: (response: ThreadReadResponse) => void = () => {
      throw new Error("thread/read handler was not reached");
    };
    const readReachedHandler = nextHandledRequest(
      fake,
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await readReachedHandler;

    let resolveSetGoal: (response: { started: boolean }) => void = () => {
      throw new Error("goal/set handler was not reached");
    };
    const setGoalReachedHandler = nextHandledRequest(
      fake,
      "goal/set",
      () =>
        new Promise((resolve) => {
          resolveSetGoal = resolve;
        }),
    );
    const pending = threadsStore.getState().setGoal("ref_a", "local objective");
    await setGoalReachedHandler;

    const cut = { reached: false };
    resolveRead(markResponseCut(readResponse("ref_a"), cut));
    const pushedGoal = { objective: "buffered pushed objective", status: "active" as const, iterations: 3 };
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "evener/goal/updated",
        params: { threadId: "thr_ref_a", ref: "ref_a", goal: pushedGoal },
      }),
    );
    await ensuring;
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(pushedGoal);

    resolveSetGoal({ started: true });
    await pending;

    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(pushedGoal);
  });

  test("removeURL refuses a session the write gate rejects and sends nothing", async () => {
    const fake = connectMutationClient();
    const ref = "ref_readonly";
    fake.on("thread/read", (params) => readResponse(params.ref ?? ref, { status: { type: "restartRequired" } }));
    await threadsStore.getState().ensureThread(ref);
    await expect(threadsStore.getState().removeURL(ref, "url_1")).rejects.toThrow(/cannot accept notes/i);
    expect(fake.calls.filter((call) => call.method === "urls/remove")).toEqual([]);
  });

  test("setHumanNote does not overwrite a newer authoritative hydration", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    let resolveSetNote: (response: NotesHumanSetResponse) => void = () => {
      throw new Error("notes/human/set handler was not reached");
    };
    const setNoteReachedHandler = nextHandledRequest(
      fake,
      "notes/human/set",
      () =>
        new Promise((resolve) => {
          resolveSetNote = resolve;
        }),
    );
    const pending = threadsStore.getState().setHumanNote("ref_a", "local note");
    const params = await setNoteReachedHandler;

    const refreshReads: Array<(response: ThreadReadResponse) => void> = [];
    let resolveRefreshReadsReached: () => void = () => {
      throw new Error("both hydration handlers were not reached");
    };
    const refreshReadsReached = new Promise<void>((resolve) => {
      resolveRefreshReadsReached = resolve;
    });
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          refreshReads.push(resolve);
          if (refreshReads.length === 2) resolveRefreshReadsReached();
        }),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await refreshReadsReached;

    const authoritativeResponse = readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        humanNote: "authoritative note",
      },
    });
    for (const resolveRead of refreshReads) resolveRead(authoritativeResponse);
    await flushUntil(
      () =>
        threadsStore.getState().threads.get("ref_a")?.humanNote === "authoritative note" &&
        threadsStore.getState().watchedThreads.get("ref_a")?.humanNote === "authoritative note",
    );
    expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("authoritative note");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.humanNote).toBe("authoritative note");

    resolveSetNote({
      note: "local note",
      receipt: {
        clientMutationId: params.clientMutationId,
        threadId: "thr_ref_a",
        disposition: "applied",
        projectionState: "notProjected",
      },
    });
    await pending;
    await flushPendingTurnsProjectionForTests();
    expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);

    expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("authoritative note");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.humanNote).toBe("authoritative note");
  });

  test.each(["response", "push", "hydration", "newer draft"] as const)(
    "retry after authoritative absence respects %s authority and the submitted draft generation",
    async (boundary) => {
      const indexedDB = new IDBFactory();
      setMutationStorageForTests(new MutationOutboxIndexedDB({ indexedDB }));
      const fake = connectFakeClient();
      // A mounted draft consumer implies its session pane in production;
      // holding the ref keeps the acknowledged draft from the pane-store
      // eviction sweep this acknowledgment now schedules.
      act(() => {
        workspaceStore.setState({
          panes: [{ id: "p_retry", type: "session", params: { ref: "ref_a" }, slot: "main" }],
          focusedPaneId: "p_retry",
        });
      });
      const snapshot = (humanNote: string) =>
        readResponse("ref_a", {
          evener: { ref: "ref_a", capabilities: CAPABILITIES, humanNote, queue: { revision: 0 } },
        });
      fake.on("thread/read", () => snapshot("A"));
      fake.on("notes/human/set", (params) => ({
        note: params.note ?? "",
        receipt: {
          clientMutationId: params.clientMutationId,
          threadId: "thr_ref_a",
          disposition: "applied",
          projectionState: "notProjected",
        },
      }));
      await threadsStore.getState().ensureThread("ref_a");
      // Response-only control proves this same runtime can acknowledge an ordinary write.
      await threadsStore.getState().setHumanNote("ref_a", "smoke");
      await flushPendingTurnsProjectionForTests();
      expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);
      expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("smoke");
      syncHumanNote("ref_a", "smoke");
      const { result } = renderHook(() => useHumanNoteDraft("ref_a"));
      const firstRequest = nextHandledRequest(fake, "notes/human/set", () => {
        throw new RequestTimeoutError("request lost before acceptance");
      });
      // The draft hook is already mounted: the dispatch, the lost-response
      // settlement, and the outbox persistence all publish to it, so the whole
      // region through the persisted submitted draft is owned.
      const record = await act(async () => {
        const submitted = await threadsStore.getState().setHumanNote("ref_a", "B");
        await firstRequest;
        // vitest's wait: Testing Library's turns the act environment off while
        // it polls, and these publishes would then warn inside this act() (#3928).
        await vi.waitFor(() => expect(result.current?.submitted?.id).toBe(submitted.clientMutationId));
        return submitted;
      });
      expect(result.current?.text).toBe("B");
      const inspector = new MutationOutboxIndexedDB({ indexedDB });
      expect((await inspector.getOutbox(record.clientMutationId))?.payload).toEqual(record.payload);
      fake.on("thread/read", () => snapshot("smoke"));
      const reply = deferred<NotesHumanSetResponse>();
      const retried = nextHandledRequest(fake, "notes/human/set", () => reply.promise);
      // The ready event triggers outbox rediscovery, whose persistence refresh
      // republishes the retained draft to the mounted hook; own the region
      // through the retried request it dispatches.
      const params = await act(async () => {
        fake.emitStateChange("reconnecting");
        fake.emitReady();
        return retried;
      });
      expect(params).toEqual(record.payload);
      expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("smoke");
      if (boundary === "push") {
        act(() =>
          fake.emitNotification({
            method: "evener/notes/updated",
            params: { ref: "ref_a", threadId: "thr_ref_a", humanNote: "new authority" },
          }),
        );
      } else if (boundary === "hydration") {
        fake.on("thread/read", () => snapshot("new authority"));
        act(() =>
          fake.emitNotification({ method: "evener/thread/resync", params: { ref: "ref_a", threadId: "thr_ref_a" } }),
        );
        await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("new authority"));
      } else if (boundary === "newer draft") {
        act(() => editHumanNote("ref_a", "C"));
      }
      await act(async () =>
        reply.resolve({
          note: "B",
          receipt: {
            clientMutationId: params.clientMutationId,
            threadId: "thr_ref_a",
            disposition: "applied",
            projectionState: "notProjected",
          },
        }),
      );
      await flushPendingTurnsProjectionForTests();
      expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);
      const authoritative = boundary === "push" || boundary === "hydration" ? "new authority" : "B";
      expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe(authoritative);
      expect(result.current).toMatchObject(
        boundary === "newer draft"
          ? { text: "C", dirty: true, saved: false }
          : { text: authoritative, dirty: false, saved: true },
      );
      expect(await inspector.listOptimistic()).toEqual([]);
      inspector.close();
    },
  );

  test("a rejoin note identity acknowledges canonical text without inventing a chat item", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("notes/human/set", () => {
      throw new RequestTimeoutError("lost note response");
    });
    const record = await threadsStore.getState().setHumanNote("ref_a", "raw draft");
    syncHumanNote("ref_a", "A");
    // The mounted draft consumer implies a session pane in production; hold
    // the ref so the acknowledgment's eviction sweep keeps the record.
    act(() => {
      workspaceStore.setState({
        panes: [{ id: "p_rejoin", type: "session", params: { ref: "ref_a" }, slot: "main" }],
        focusedPaneId: "p_rejoin",
      });
    });
    const { result } = renderHook(() => useHumanNoteDraft("ref_a"));
    await waitFor(() => expect(result.current?.submitted?.id).toBe(record.clientMutationId));
    const canonical = " \n\tcanonical\u00a0e\u0301🙂  ";
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        evener: {
          ...readResponse("ref_a").thread.evener,
          humanNote: canonical,
          pendingMutations: [
            {
              clientMutationId: record.clientMutationId,
              method: "notes/human/set",
              executionState: "applied",
              projectionState: "pending",
            },
          ],
        },
        turns: [],
      }),
    );
    act(() => {
      fake.emitStateChange("reconnecting");
      fake.emitReady();
    });
    await waitFor(() => expect(result.current).toMatchObject({ text: canonical, dirty: false, saved: true }));
    await flushPendingTurnsProjectionForTests();
    expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);
    expect((await readMutationPersistence("ref_a")).optimistic).toEqual([]);
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
  });

  test("a lost note response reconnects with the same independently persisted identity and raw payload", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const reached = nextHandledRequest(fake, "notes/human/set", () => {
      throw new RequestTimeoutError("lost note response");
    });
    const raw = " \tline one\nline two\u00a0e\u0301🙂  ";
    const record = await threadsStore.getState().setHumanNote("ref_a", raw);
    const first = await reached;
    const independent = new MutationOutboxIndexedDB();
    expect((await independent.getOutbox(record.clientMutationId))?.payload).toEqual(first);
    expect(first.note).toBe(raw);
    const retried = nextHandledRequest(fake, "notes/human/set", (params) => ({
      note: raw,
      receipt: {
        clientMutationId: params.clientMutationId,
        threadId: "thr_ref_a",
        disposition: "replayed",
        projectionState: "notProjected",
      },
    }));
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    expect(await retried).toEqual(first);
    await flushPendingTurnsProjectionForTests();
    expect(await independent.getOutbox(record.clientMutationId)).toBeUndefined();
    expect(await independent.listOptimistic()).toEqual([]);
    independent.close();
  });

  test.each(["ended", "capability"])("blocked notes do not retry after %s loss", async (loss) => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const storage = new MutationOutboxIndexedDB();
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "notes/human/set",
      payload: { ref: "ref_a", note: "blocked sentinel", expectedInstanceId: "thr_ref_a" },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.markUnknown(record.clientMutationId, "blockedUnknown");
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("missing hydrated model");
    threadsStore.setState({
      threads: new Map([
        [
          "ref_a",
          loss === "ended"
            ? { ...model, status: { type: "ended" } }
            : { ...model, capabilities: { ...model.capabilities, sharedNotes: false } },
        ],
      ]),
      mutationAuthorityRefs: new Set(["ref_a"]),
    });
    expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
    expect(fake.calls.filter((call) => call.method === "notes/human/set")).toEqual([]);
    storage.close();
  });

  test("a urls/updated push arriving during a setHumanNote await does not drop the note commit", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    let resolveSetNote: (response: NotesHumanSetResponse) => void = () => {
      throw new Error("notes/human/set handler was not reached");
    };
    const setNoteReachedHandler = nextHandledRequest(
      fake,
      "notes/human/set",
      () =>
        new Promise((resolve) => {
          resolveSetNote = resolve;
        }),
    );
    const pending = threadsStore.getState().setHumanNote("ref_a", "local note");
    const params = await setNoteReachedHandler;

    // An unrelated urls push carries no note state: it must retire only the
    // urls fallback, leaving the in-flight note commit intact.
    fake.emitNotification({
      method: "evener/urls/updated",
      params: { threadId: "thr_ref_a", ref: "ref_a", urls: [{ id: "u1", url: "https://x.test/y" }] },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.sessionUrls).toEqual([{ id: "u1", url: "https://x.test/y" }]);

    resolveSetNote({
      note: "local note",
      receipt: {
        clientMutationId: params.clientMutationId,
        threadId: "thr_ref_a",
        disposition: "applied",
        projectionState: "notProjected",
      },
    });
    await pending;
    await flushPendingTurnsProjectionForTests();
    expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);

    expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("local note");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.humanNote).toBe("local note");
  });

  test("rename sends evener/thread/name/set with {ref, name}", async () => {
    const fake = connectFakeClient();
    fake.on("evener/thread/name/set", () => ({}));

    await threadsStore.getState().rename("ref_a", "New title");

    const call = fake.calls.find((c) => c.method === "evener/thread/name/set");
    expect(call?.params).toEqual({ ref: "ref_a", name: "New title" });
  });

  test("compact sends thread/compact/start with {ref}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/compact/start", () => ({}));

    await threadsStore.getState().compact("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/compact/start");
    expect(call?.params).toEqual({ ref: "ref_a" });
  });

  test("shutdown sends thread/shutdown with {ref}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/shutdown", () => ({}));

    await threadsStore.getState().shutdown("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/shutdown");
    expect(call?.params).toEqual({ ref: "ref_a" });
  });

  test("forkFromTurn sends thread/fork with {ref, ...opts} and returns the response verbatim", async () => {
    const fake = connectFakeClient();
    fake.on("thread/fork", () => ({ thread: testThread("ref_child"), originalInput: undefined }));

    const result = await threadsStore
      .getState()
      .forkFromTurn("ref_a", { sourceItemKey: "apptranscript-item-v2:turn_1:0:0", editedInput: "edited text" });

    const call = fake.calls.find((c) => c.method === "thread/fork");
    expect(call?.params).toEqual({
      ref: "ref_a",
      sourceItemKey: "apptranscript-item-v2:turn_1:0:0",
      editedInput: "edited text",
    });
    expect(result.thread.evener.ref).toBe("ref_child");
  });

  test("forkFromTurn supports the aside mode's mutually-exclusive param set", async () => {
    const fake = connectFakeClient();
    fake.on("thread/fork", () => ({ thread: testThread("ref_aside") }));

    await threadsStore.getState().forkFromTurn("ref_a", { aside: true });

    const call = fake.calls.find((c) => c.method === "thread/fork");
    // sourceItemKey carries `omitempty` on the wire (appwire/types.go), so
    // an aside-mode caller that never set it (aside is mutually exclusive
    // with it) sends no field at all.
    expect(call?.params).toEqual({ ref: "ref_a", aside: true });
  });

  // The durable clear response is the authoritative replacement snapshot; the
  // dispatcher applies it before settling the outbox record.
  test("clearThread queues thread/clear with the instance fence and applies the response snapshot", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));
    await threadsStore.getState().clearThread("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/clear");
    expect(call?.params).toEqual({
      ref: "ref_a",
      expectedInstanceId: "thr_ref_a",
      clientMutationId: expect.any(String),
    });
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
  });

  test("clearThread updates a watched model tracking the same ref too", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));
    await threadsStore.getState().clearThread("ref_a");

    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toEqual([]);
  });

  // Dual-map atomicity (round-2 fix): thread/clear's response snapshot lands
  // in threads and watchedThreads through ONE setState (applyClearResponse's
  // single patch), so a synchronous subscriber never sees threads cleared
  // while watchedThreads still holds the old turns.
  test("clearThread replaces both maps in one setState - no synchronous subscriber sees split state", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));

    const snapshots: { tracked: number; watched: number }[] = [];
    const unsubscribe = threadsStore.subscribe((state) => {
      snapshots.push({
        tracked: state.threads.get("ref_a")?.turns.length ?? -1,
        watched: state.watchedThreads.get("ref_a")?.turns.length ?? -1,
      });
    });
    try {
      await threadsStore.getState().clearThread("ref_a");
    } finally {
      unsubscribe();
    }

    for (const [i, snap] of snapshots.entries()) {
      expect(snap.tracked, `snapshot ${i} must not be split`).toBe(snap.watched);
    }
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toEqual([]);
  });

  test("clearThread retains a transport failure for retry and leaves the tracked model untouched", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/clear", () => {
      throw new Error("turn in progress");
    });

    const before = threadsStore.getState().threads.get("ref_a");
    await threadsStore.getState().clearThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")).toBe(before);
    const persistence = await readMutationPersistence("ref_a");
    expect(persistence.outbox).toHaveLength(1);
    expect(persistence.outbox[0]?.method).toBe("thread/clear");
  });

  // A canceled row's only exits are explicit Retry and the thread going
  // away: when the clear lands, the row leaves with the history it belonged
  // to (docs/design/stop-cancellation-outbox.md §6).
  test("clearThread removes the ref's canceled rows once the clear lands", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await threadsStore.getState().ensureThread("ref_a");
    const canceled = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "stopped before sending" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_a");

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));
    await threadsStore.getState().clearThread("ref_a");

    await flushPendingTurnsProjectionForTests();
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
  });

  // RoboRev's detached-promise finding: discardCanceledMutations fires the pin
  // refresh without awaiting it, and refreshMutationPins reads storage
  // (listOutbox/listOptimistic) - reads that can reject (a timeout, a
  // VersionError, a retired connection) after the discard already committed.
  // The fired promise must stay inside the discard's best-effort envelope
  // rather than escaping as an unhandled rejection. Reaching the end of this
  // test instead of the runner failing on an unhandled rejection is the
  // assertion, the same rule as the credentials retry test.
  test("a pin-refresh read failing after a successful discard stays best-effort, not an unhandled rejection", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await threadsStore.getState().ensureThread("ref_a");
    const canceled = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "stopped before sending" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_a");

    // The discard commits; the pin refresh that follows reads storage, and
    // that read fails. The wrapper only arms the failure after the real
    // discard resolves and fires it exactly once - clearThread's own awaited
    // pin refresh that follows must keep the real reads - so the discard
    // itself stays real and only its fired refresh pays the failure.
    const realDiscardCanceled = storage.discardCanceled.bind(storage);
    const realListOptimistic = storage.listOptimistic.bind(storage);
    storage.discardCanceled = (targetRef: string) =>
      realDiscardCanceled(targetRef).then((discarded) => {
        storage.listOptimistic = () => {
          storage.listOptimistic = realListOptimistic;
          return Promise.reject(new Error("pin refresh read failed"));
        };
        return discarded;
      });

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));
    await threadsStore.getState().clearThread("ref_a");

    await flushPendingTurnsProjectionForTests();
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
    // Let a would-be unhandled rejection surface: a few real event-loop turns,
    // each a real storage read the same runtime serves.
    const probe = new MutationOutboxIndexedDB();
    for (let round = 0; round < 5; round += 1) await probe.listOutbox("ref_a");
    probe.close();
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
  });

  // The other half of §6's removal rule: the hub's durable deletion fence is
  // the moment the ref provably no longer exists, so its canceled rows go too.
  test("a hub-deleted thread's canceled rows are removed when the deletion fence lands", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    const canceled = await storage.enqueueIntent({
      targetRef: "ref_gone",
      threadId: "thr_ref_gone",
      method: "turn/queue",
      payload: { ref: "ref_gone", input: [{ type: "text", text: "stopped before sending" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_gone");
    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });

    // A deleted ref's ensureThread never settles (its retry loop treats every
    // rejection as transient); the deletion fence, not the promise, is the
    // observable outcome.
    void threadsStore.getState().ensureThread("ref_gone");

    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("ref_gone")).toBe(true);
    });
    await flushPendingTurnsProjectionForTests();
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
  });

  // UI-01: a durable deletion fence is terminal for the ref's hydration
  // lifecycle. A pane retains its claim, but the hub has proven the ref gone,
  // so the retry loop must not keep reading an unrecoverable target and the
  // acquisition promise must settle instead of hanging forever.
  test("a durably deleted ref's hydration is terminal: no retry and a settled acquisition", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });

    const acquisition = threadsStore.getState().ensureThread("local:ref_gone");
    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("local:ref_gone")).toBe(true);
    });
    // The acquisition settles rather than awaiting a retry that can never
    // publish into this owner generation.
    await expect(acquisition).resolves.toBeUndefined();
    expect(reads).toBe(1);
    // Nothing is left to advance: the fence armed no retry.
    expect(scheduledHydrationRetries).toHaveLength(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  });

  // UI-01's cached-model half: a fence that lands after the ref hydrated must
  // still expose the deletion. The store deliberately keeps the (now stale)
  // model, so the deleted surface has to be read from deletedRefs rather than
  // from "no model".
  test("a deletion fence recorded after hydration exposes the deletion while the cached model remains", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_gone", { name: "Soon gone" }));
    await threadsStore.getState().ensureThread("ref_gone");
    expect(threadsStore.getState().threads.has("ref_gone")).toBe(true);

    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    await expect(threadsStore.getState().refreshThread("ref_gone")).rejects.toThrow(/deleted/);
    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("ref_gone")).toBe(true);
    });
    // The stale model is retained, so the deletion is observable independently
    // of it, and no retry is armed against the unrecoverable target.
    expect(threadsStore.getState().threads.has("ref_gone")).toBe(true);
    expect(scheduledHydrationRetries).toHaveLength(0);
  });

  // Control: the terminal deleted path must not swallow ordinary transient
  // failures - a transport rejection still schedules exactly one retry that
  // recovers the ref.
  test("a transient read failure still schedules one retry and recovers", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      if (reads === 1) throw new Error("transport hiccup");
      return readResponse("ref_a", { name: "Recovered" });
    });

    const acquisition = threadsStore.getState().ensureThread("ref_a");
    await waitFor(() => {
      expect(scheduledHydrationRetries).toHaveLength(1);
    });
    expect(threadsStore.getState().deletedRefs.has("ref_a")).toBe(false);

    runScheduledHydrationRetry();
    await expect(acquisition).resolves.toBeUndefined();
    await waitFor(() => {
      expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    });
    expect(reads).toBe(2);
  });

  // RoboRev on #3010 (Medium): a fence must retire an already-scheduled retry
  // immediately, not leave it to fire. Both hydrate paths call
  // scheduleOwnedHydrationRetry right after recording the fence, and its
  // deleted branch retires the lifecycle - so the timer is cancelled before the
  // rejection even propagates.
  test("a deletion fence cancels an already-scheduled retry immediately and settles the acquisition", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => {
      throw new Error("transport hiccup");
    });
    const acquisition = threadsStore.getState().ensureThread("ref_a");
    await waitFor(() => {
      expect(scheduledHydrationRetries).toHaveLength(1);
    });
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(false);

    // The thread is deleted before the retry timer fires: the next read is
    // fenced, and the live retry must be retired there and then.
    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:ref_a", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    await expect(threadsStore.getState().refreshThread("ref_a")).rejects.toThrow(/deleted/);

    // No timer advance: the fence retired the lifecycle synchronously.
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);
    await expect(acquisition).resolves.toBeUndefined();
    expect(threadsStore.getState().deletedRefs.has("ref_a")).toBe(true);
  });

  // RoboRev on #3010 (Low): the watched (subagent/delegate card) path needs the
  // same terminal coverage as the pane path - a watcher that failed to settle
  // would hang its caller indefinitely.
  test("a durably deleted watched ref's hydration is terminal: no retry and a settled acquisition", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });

    const acquisition = threadsStore.getState().watchThread("local:ref_gone");
    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("local:ref_gone")).toBe(true);
    });
    await expect(acquisition).resolves.toBeUndefined();
    expect(reads).toBe(1);
    expect(scheduledHydrationRetries).toHaveLength(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  });

  // One representative Conflict-mapping test standing in for every
  // thread-level action above - each wraps its client.request in the exact
  // same mapConflict try/catch as send/steer/queue/interrupt (proven
  // exhaustively above); repeating it per method would test the identical
  // wrapper code path over and over rather than add real coverage.
  test("session actions also map a Conflict rejection to ConflictError (setModel as the representative case)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/model/set", () => {
      throw new WireError("model unavailable", -32013, { evenerErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toBeInstanceOf(ConflictError);
  });

  // Issue #195's RCA / PR #211's rejected transport-queuing design: a
  // mutation whose first attempt may already be executing server-side must
  // never blind-retry across a reconnect - it could double-fire a
  // non-idempotent method. Unlike the read-only actions (listJobs and
  // siblings, gated through requireReadyClient), every session action here
  // calls client.request() directly with no ready-wait, so it keeps
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection. One representative case stands in for the whole family -
  // each wraps the identical direct client.request() call (proven above);
  // repeating this per method would test the same rejection path over and
  // over rather than add real coverage.
  test("setModel rejects synchronously while reconnecting, unlike the read-only actions (representative direct-request mutation)", async () => {
    const fake = connectFakeClient();
    fake.emitStateChange("reconnecting");

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toThrow(
      /cannot call "thread\/model\/set" while state is "reconnecting"/,
    );
    expect(fake.calls).toHaveLength(0); // never sent - the real client never reaches socket.send() either
  });
});
function mutationReceipt(clientMutationId: string, disposition = "applied") {
  return {
    clientMutationId,
    disposition,
    threadId: "thr_ref_a",
    projectionState: "reflected",
  };
}

describe("useThreadsStore.send", () => {
  test("calls turn/start with text and a base64 image attachment (wire InputItem.data/mediaType/name - appwire/types.go:561-570)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "hello", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" }]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "hello" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
  });

  test("send with no attachments sends text-only input", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore.getState().send("ref_a", "hello");
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [{ type: "text", text: "hello" }],
    });
  });

  // kata 6nmz: a raw "[image N]" marker on the wire misleads small models -
  // haiku read one as a file path and called read_file("[image 1]") instead of
  // looking at the vision block it was sitting next to. The composer keeps the
  // marker as its tile anchor; the wire gets prose.
  test("translates the composer's [image N] markers to prose on the wire (kata 6nmz)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "[image 1]Describe the attached image", [
        { marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "(attached image 1: pic.png)Describe the attached image" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
  });

  // kata 1gm2: the marker number rides InputAttachment so the composer, the
  // durable record and the recovery draft can pair text to attachment by
  // identity. It is client-side state and the daemon must never see it.
  test("the attachment's marker number never reaches the wire (kata 1gm2)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "[image 7]look", [{ marker: 7, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" }]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "(attached image 7: pic.png)look" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
    expect(JSON.stringify(params)).not.toContain("marker");
  });

  test("an unnamed attachment's marker translates without a dangling name separator (kata 6nmz)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "look: [image 1]", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=" }]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "look: (attached image 1)" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=" },
      ],
    });
  });

  test("a marker-less text reaches the wire byte-identical, untrimmed (kata 6nmz / floor §1.12)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "  keep\n  every\n  byte  ", [
        { marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "  keep\n  every\n  byte  " },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
  });

  // Issue #195's RCA / PR #211's rejected transport-queuing design: a
  // mutation whose first attempt may already be executing server-side must
  // never blind-retry across a reconnect - it could double-fire a
  // non-idempotent method like turn/start. Unlike the read-only actions
  // (listJobs and siblings, gated through requireReadyClient), the mutation
  // outbox's own enqueueMutationIntent (stores/threads.ts) checks
  // client.state itself and rejects synchronously with no ready-wait -
  // representative of the whole outbox family (steer/queue/interrupt/
  // drainAsSteer/promoteQueuedAsSteer/cancelQueued all funnel through the
  // same gate).
  test("rejects synchronously while reconnecting, unlike the read-only actions (representative outbox mutation)", async () => {
    const fake = connectFakeClient();
    fake.emitStateChange("reconnecting");

    await expect(threadsStore.getState().send("ref_a", "hello")).rejects.toThrow(
      /cannot enqueue mutation while reconnecting/,
    );
    expect(fake.calls).toHaveLength(0); // never sent
  });
});

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

async function flushIndexedDBUntil(done: () => boolean | Promise<boolean>, maxTurns = 30): Promise<void> {
  const probe = new MutationOutboxIndexedDB();
  for (let turn = 0; turn < maxTurns && !(await done()); turn += 1) await probe.listTargetRefs();
  probe.close();
}

describe("useThreadsStore.steer / queue / interrupt", () => {
  async function ensureActiveTurn(fake: FakeClient, ref: string): Promise<void> {
    fake.on("thread/read", (params) =>
      readResponse((params as { ref: string }).ref, {
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
        evener: {
          ref: (params as { ref: string }).ref,
          capabilities: CAPABILITIES,
          queue: { revision: 7 },
          activeTurnId: "turn_1",
        },
      }),
    );
    await threadsStore.getState().ensureThread(ref);
  }

  test("steer sends turn/steer with the composer input and no turn id", async () => {
    const fake = connectMutationClient();
    await ensureActiveTurn(fake, "ref_a");
    fake.on("turn/steer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().steer("ref_a", "steer text");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/steer"));

    const call = fake.calls.find((c) => c.method === "turn/steer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      input: [{ type: "text", text: "steer text" }],
    });
  });

  test("steer includes a base64 image attachment when provided", async () => {
    const fake = connectMutationClient();
    await ensureActiveTurn(fake, "ref_a");
    fake.on("turn/steer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore
      .getState()
      .steer("ref_a", "steer text", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=" }]);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/steer"));

    const call = fake.calls.find((c) => c.method === "turn/steer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      input: [
        { type: "text", text: "steer text" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=" },
      ],
    });
  });

  // Stop never names a turn, even here, where this client is tracking one.
  // Naming it could only ever make Stop fail -- the id goes stale when a turn
  // rolls over between the click and the request -- and a refused Stop is the
  // failure the button exists to prevent.
  test("interrupt asks for whatever is running even when this client knows the turn id", async () => {
    const fake = connectMutationClient();
    await ensureActiveTurn(fake, "ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().interrupt("ref_a");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));

    const call = fake.calls.find((c) => c.method === "turn/interrupt");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
    });
  });

  // Same request from the opposite client state: the session is working, so
  // Stop is on screen, but no turn id has reached this client -- a turn the
  // session started for itself, a boundary between two turns of one drain, or a
  // cold client. This is the window a turn-scoped Stop could not address at all,
  // and the request it produces is identical to the one above.
  test("interrupt with no turn id asks the daemon to stop whatever is running", async () => {
    const fake = connectMutationClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref, { status: { type: "active" } }));
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId ?? "").toBe("");
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().interrupt("ref_a");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));

    const call = fake.calls.find((c) => c.method === "turn/interrupt");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
    });
  });

  test("queue sends turn/queue with the composer input and no turn id", async () => {
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().queue("ref_a", "queued text");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    const call = fake.calls.find((c) => c.method === "turn/queue");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      input: [{ type: "text", text: "queued text" }],
    });
  });

  test("queue includes a base64 image attachment when provided", async () => {
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore
      .getState()
      .queue("ref_a", "", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "x.png" }]);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    const call = fake.calls.find((c) => c.method === "turn/queue");
    // queueText allows empty text when attachments are present (parity
    // finding §B: "image-only queue entries are valid") - buildInput's
    // text.trim() guard means an empty string contributes no text item.
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [{ type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "x.png" }],
    });
  });
});

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

// drainAsSteer (kata 0bq1 Path B): the plan's terse locked-interfaces block
// shows `drainAsSteer(ref)`, but the wire method it calls
// (TurnDrainAsSteerParams, appwire/types.go:769-776) carries an optional
// Input the daemon appends before draining ("Input lets clients atomically
// append the current composer payload before the drain"), and the parity
// floor's Path B row requires exactly that ("anything + non-empty queue ...
// turn/drainAsSteer carrying the textarea text/items so the daemon
// appends-then-drains atomically" - parity-m5-composer.md §A). Shipping a
// bare `drainAsSteer(ref)` would silently drop the composer's pending
// text/attachments on every Path-B drain, contradicting both the parity
// floor and the "optimistic pending applies uniformly to
// send/steer/queue/drain" binding constraint (which needs to know WHAT was
// submitted to render a pending chip). This store therefore ships
// `drainAsSteer(ref, text, attachments?)`, mirroring send/steer/queue's own
// shape exactly - flagged in the T1 report as an interpretation, not a
// silent deviation.
describe("useThreadsStore.drainAsSteer", () => {
  test("sends turn/drainAsSteer with the composer's text and attachments as input", async () => {
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/drainAsSteer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore
      .getState()
      .drainAsSteer("ref_a", "drain text", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=" }]);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/drainAsSteer"));

    const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      expectedQueueRevision: 7,
      input: [
        { type: "text", text: "drain text" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=" },
      ],
    });
  });

  test("sends an empty input array when the composer was empty (draining the queue alone)", async () => {
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/drainAsSteer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().drainAsSteer("ref_a", "");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/drainAsSteer"));

    const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      expectedQueueRevision: 7,
      input: [],
    });
  });

  // A drain's own thread/queueChanged push names the client mutation ids it
  // consumed (issue #1704), so the client settles those optimistic turn/queue
  // records by positive evidence -- never by inferring consumption from
  // sequence order.
  test("a queueChanged naming consumed ids settles the matching optimistic queue record", async () => {
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "pending",
      },
    }));

    await threadsStore.getState().queue("ref_a", "queued");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    const inspector = new MutationOutboxIndexedDB();
    let record: Awaited<ReturnType<typeof inspector.listOptimistic>>[number] | undefined;
    for (let attempt = 0; attempt < 20 && !record; attempt += 1) {
      [record] = await inspector.listOptimistic("ref_a");
    }
    if (!record) throw new Error("queued record never reached the optimistic store");

    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { revision: 8 },
        consumedClientMutationIds: [record.clientMutationId],
      },
    });

    let settled = false;
    for (let attempt = 0; attempt < 20 && !settled; attempt += 1) {
      settled = (await inspector.getOptimistic(record.clientMutationId)) === undefined;
    }
    expect(settled).toBe(true);
    inspector.close();
  });

  // A malformed consumedClientMutationIds (anything that is not an array of
  // non-empty strings) must be ignored outright: a string is itself iterable
  // character-by-character, so an unguarded spread would settle a record
  // named by coincidence rather than by the daemon. The fixed id below ("a")
  // is deliberately a substring of the malformed value, so an unguarded
  // spread of "abc" would wrongly retire it.
  test("a malformed consumedClientMutationIds on a queueChanged push is ignored, not iterated", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "a" });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thr_ref_a",
        projectionState: "pending",
      },
    }));

    await threadsStore.getState().queue("ref_a", "queued");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    let record: Awaited<ReturnType<typeof storage.listOptimistic>>[number] | undefined;
    for (let attempt = 0; attempt < 20 && !record; attempt += 1) {
      [record] = await storage.listOptimistic("ref_a");
    }
    if (!record) throw new Error("queued record never reached the optimistic store");
    expect(record.clientMutationId).toBe("a");

    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { revision: 8 },
        consumedClientMutationIds: "abc",
      },
    } as unknown as AnyNotification);
    await settleCallerContinuations();

    expect(await storage.getOptimistic("a")).toBeDefined();
    storage.close();
  });
});

describe("useThreadsStore.promoteQueuedAsSteer / cancelQueued", () => {
  test("promoteQueuedAsSteer sends turn/promoteQueuedAsSteer with {ref, index, expectedEntryId}", async () => {
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/promoteQueuedAsSteer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().promoteQueuedAsSteer("ref_a", 1, "entry_2", { text: "queued message" });
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/promoteQueuedAsSteer"));

    const call = fake.calls.find((c) => c.method === "turn/promoteQueuedAsSteer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      index: 1,
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      expectedEntryId: "entry_2",
    });
  });

  test("cancelQueued durably enqueues turn/cancelQueued with {ref, index, expectedEntryId}", async () => {
    const fake = connectMutationClient();
    fake.on("turn/cancelQueued", (params) => ({
      removedText: "queued message",
      removedImages: 2,
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore.getState().cancelQueued("ref_a", 0, "entry_1");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/cancelQueued"));

    const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
    expect(call?.params).toEqual({
      ref: "ref_a",
      index: 0,
      clientMutationId: expect.any(String),
      expectedEntryId: "entry_1",
    });
  });
});

// kata 1gm2: a record born from a real submit used to reach recovery carrying
// only the translated prose, so the restored composer showed sentences where
// its tile anchors belonged and removing a tile stripped nothing. Nothing here
// is hand-seeded: the record is the one send() actually wrote.
test("a submitted record recovers into a composer draft with its marker anchors intact", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const dispatched = nextHandledRequest(fake, "turn/start", () => new Promise<never>(() => undefined));

  await threadsStore.getState().send("ref_a", "look [image 3] then [image 1]", [
    { marker: 1, mediaType: "image/png", data: "AQID", name: "first.png" },
    { marker: 3, mediaType: "image/png", data: "BAUG", name: "third.png" },
  ]);
  await dispatched;
  const submitted = (await storage.listOutbox("ref_a"))[0];
  expect(submitted).toBeDefined();
  if (!submitted) return;
  expect((submitted.payload as { input: { text?: string }[] }).input[0]?.text).toBe(
    "look (attached image 3: third.png) then (attached image 1: first.png)",
  );

  const recovered = await storage.transferToRecovery(submitted.clientMutationId, "rejected");
  expect(recovered).toBeDefined();
  if (!recovered) return;
  const draft = recoveryComposerDraft(recovered);

  expect(draft.text).toBe("look [image 3] then [image 1]");
  expect(draft.attachments.map((attachment) => [attachment.marker, attachment.name])).toEqual([
    [1, "first.png"],
    [3, "third.png"],
  ]);
});

test("recovery resend rebuilds queue CAS values from the current thread", async () => {
  let mutationId = 0;
  const storage = new MutationOutboxIndexedDB({
    createMutationId: () => `mutation-${++mutationId}`,
  });
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "stale" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "stale" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const fake = connectFakeClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      status: { type: "active" },
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        activeTurnId: "turn-current",
        queue: { revision: 7 },
      },
    }),
  );
  fake.on("turn/queue", () => new Promise<never>(() => undefined));
  await threadsStore.getState().ensureThread("ref_a");

  expect(await resendRecoveryMutation(original.clientMutationId, "ref_a", "queue", "edited", [])).toBeDefined();
  expect((await storage.listOutbox("ref_a"))[0]).toMatchObject({
    method: "turn/queue",
    payload: {
      input: [{ type: "text", text: "edited" }],
    },
  });
});

test("the first submission commits while startup discovery is stalled", async ({ onTestFailed }) => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const getAll = IDBObjectStore.prototype.getAll;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let announceRead: (() => void) | undefined;
  const readHeld = new Promise<void>((resolve) => {
    announceRead = resolve;
  });
  vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    if (this.name === "outbox" && !hold) {
      hold = holdIndexedDBEvent(request, "success");
      void hold.reached.then(() => announceRead?.());
    }
    return request;
  });
  // Neither the storage watchdog nor lifecycle retries may rescue this send.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  const release = () => {
    hold?.release();
    vi.useRealTimers();
  };
  onTestFailed(release);
  const fake = connectMutationClient();
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  try {
    await readHeld;
    await threadsStore.getState().send("ref_a", "saved before startup discovery finishes");
    expect(await storage.listOutbox("ref_a")).toMatchObject([
      {
        state: "submitting",
        method: "turn/start",
        payload: { input: [{ type: "text", text: "saved before startup discovery finishes" }] },
      },
    ]);
  } finally {
    release();
  }
});

test("reset retires an outbox discovery before the next runtime starts", async () => {
  const oldStorage = new MutationOutboxIndexedDB();
  let finishOldDiscovery!: (targetRefs: string[]) => void;
  const oldDiscovery = new Promise<string[]>((resolve) => {
    finishOldDiscovery = resolve;
  });
  let announceDiscovery: (() => void) | undefined;
  const discoveryStarted = new Promise<void>((resolve) => {
    announceDiscovery = resolve;
  });
  vi.spyOn(oldStorage, "listTargetRefs").mockImplementationOnce(() => {
    announceDiscovery?.();
    return oldDiscovery;
  });
  setMutationStorageForTests(oldStorage);

  const oldRead = readMutationPersistence();
  await discoveryStarted;
  await oldRead;
  expect(oldStorage.listTargetRefs).toHaveBeenCalledTimes(1);
  resetThreadsStoreForTests();

  const newStorage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(newStorage);
  let newMutationDispatched!: () => void;
  const dispatched = new Promise<void>((resolve) => {
    newMutationDispatched = resolve;
  });
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("stale_ref"));
  fake.on("turn/start", (params) => {
    newMutationDispatched();
    return {
      turn: { id: "turn_new", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    };
  });
  fake.emitReady();
  await threadsStore.getState().send("new_ref", "new runtime");
  await dispatched;

  finishOldDiscovery(["stale_ref"]);
  await newStorage.listOutbox();

  expect(fake.calls.filter((call) => call.method === "thread/read").map((call) => call.params)).not.toContainEqual(
    expect.objectContaining({ ref: "stale_ref" }),
  );
});

test("reset retires an in-flight dispatcher's persistence callback", async () => {
  const oldStorage = new MutationOutboxIndexedDB();
  let finishOldSettlement!: (settled: boolean) => void;
  const oldSettlement = new Promise<boolean>((resolve) => {
    finishOldSettlement = resolve;
  });
  let oldSettlementStarted!: () => void;
  const settlementStarted = new Promise<void>((resolve) => {
    oldSettlementStarted = resolve;
  });
  vi.spyOn(oldStorage, "settleReceipt").mockImplementation(() => {
    oldSettlementStarted();
    return oldSettlement;
  });
  setMutationStorageForTests(oldStorage);

  const oldClient = connectMutationClient();
  oldClient.on("turn/start", (params) => ({
    turn: { id: "turn_old", status: "inProgress", itemsView: "" },
    receipt: mutationReceipt(params.clientMutationId),
  }));
  await threadsStore.getState().send("old_ref", "old runtime");
  await settlementStarted;

  resetThreadsStoreForTests();
  const persisted: string[][] = [];
  const unsubscribe = subscribeMutationPersistence((targetRefs) => persisted.push(targetRefs));
  finishOldSettlement(true);
  await settleCallerContinuations();
  unsubscribe();

  expect(persisted).toEqual([]);
});

test("reset retires an in-flight pin refresh before it can repin the next runtime", async () => {
  const oldStorage = new MutationOutboxIndexedDB();
  const realListOutbox = oldStorage.listOutbox.bind(oldStorage);
  let holdRefresh = false;
  let finishOldRefresh!: (records: Awaited<ReturnType<MutationOutboxIndexedDB["listOutbox"]>>) => void;
  const oldRefresh = new Promise<Awaited<ReturnType<MutationOutboxIndexedDB["listOutbox"]>>>((resolve) => {
    finishOldRefresh = resolve;
  });
  let oldRefreshStarted!: () => void;
  const refreshStarted = new Promise<void>((resolve) => {
    oldRefreshStarted = resolve;
  });
  const listOutbox = vi.spyOn(oldStorage, "listOutbox").mockImplementation((targetRef) => {
    if (!holdRefresh) return realListOutbox(targetRef);
    oldRefreshStarted();
    return oldRefresh;
  });
  setMutationStorageForTests(oldStorage);
  await readMutationPersistence();
  const listOptimistic = vi.spyOn(oldStorage, "listOptimistic").mockResolvedValue([]);
  const oldClient = connectFakeClient("connecting");
  holdRefresh = true;
  oldClient.emitNotification({
    method: "history/updated",
    params: {
      threadId: "thr_stale_ref",
      ref: "stale_ref",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [
        {
          ...{
            type: "commandExecution",
            id: "item_1",
            turnId: "turn_1",
            clientMutationId: "old-mutation",
            output: "done",
            status: "completed",
          },
          turnId: "turn_1",
        },
      ],
    },
  });
  await refreshStarted;
  expect(listOutbox).toHaveBeenCalledWith("stale_ref");
  expect(listOptimistic).toHaveBeenCalledWith("stale_ref");

  resetThreadsStoreForTests();
  threadsStore.setState({
    threads: new Map([["stale_ref", hydrateThread(readResponse("stale_ref"), "stale_ref", 1000)]]),
  });
  finishOldRefresh([]);
  await settleCallerContinuations();

  expect(threadsStore.getState().threads.has("stale_ref")).toBe(true);
});

test("reset retires a ready scan before it can pin refs in the next runtime", async () => {
  const oldStorage = new MutationOutboxIndexedDB();
  let finishOldReadyScan!: (targetRefs: string[]) => void;
  const oldReadyScan = new Promise<string[]>((resolve) => {
    finishOldReadyScan = resolve;
  });
  let oldReadyScanStarted!: () => void;
  const readyScanStarted = new Promise<void>((resolve) => {
    oldReadyScanStarted = resolve;
  });
  let scans = 0;
  vi.spyOn(oldStorage, "listTargetRefs").mockImplementation(() => {
    scans += 1;
    if (scans === 1) return Promise.resolve([]);
    oldReadyScanStarted();
    return oldReadyScan;
  });
  setMutationStorageForTests(oldStorage);

  const oldClient = connectFakeClient("connecting");
  oldClient.emitReady();
  await readyScanStarted;

  resetThreadsStoreForTests();
  const newStorage = new MutationOutboxIndexedDB();
  vi.spyOn(newStorage, "listTargetRefs").mockResolvedValue([]);
  setMutationStorageForTests(newStorage);
  finishOldReadyScan(["stale_ref"]);
  await settleCallerContinuations();

  const newClient = new FakeClient("ready");
  newClient.on("thread/read", () => readResponse("stale_ref"));
  connectionStore.getState().connect(newClient);

  expect(newClient.calls.filter((call) => call.method === "thread/read")).toEqual([]);
});

test("force stop uses the independent recovery API and fences uncertain outcomes", async () => {
  const fake = new FakeClient();
  connectionStore.setState({ client: fake, state: "ready" });
  const recover = vi.spyOn(fake, "forceStop").mockRejectedValueOnce(new Error("exit unconfirmed"));
  await expect(threadsStore.getState().forceStop("local:owner")).rejects.toThrow("exit unconfirmed");
  expect(threadsStore.getState().restartBlockingObligations.has("local:owner")).toBe(true);
  recover.mockResolvedValueOnce(undefined);
  await threadsStore.getState().forceStop("local:owner");
  expect(recover).toHaveBeenNthCalledWith(2, "local:owner");
  expect(threadsStore.getState().restartBlockingObligations.has("local:owner")).toBe(true);
  expect(fake.calls.some((call) => call.method === "evener/thread/forceStop")).toBe(false);
});

test.each([false, true])(
  "failed force stop reconciles without hiding the original error (read fails: %s)",
  async (readFails) => {
    const ref = "local:owner";
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse(ref));
    await threadsStore.getState().ensureThread(ref);
    const record = await storage.enqueueIntent({
      targetRef: ref,
      method: "turn/queue",
      payload: { ref, expectedInstanceId: `thr_${ref}`, input: [{ type: "text", text: "uncertain input" }] },
      attachments: [],
      optimisticDisplay: { text: "uncertain input" },
    });
    await storage.markAttempted(record.clientMutationId);
    await storage.markUnknown(record.clientMutationId, "blockedUnknown");
    const read = deferred<ThreadReadResponse>();
    fake.on("thread/read", () => read.promise);
    const original = new Error("exit confirmation failed");
    vi.spyOn(fake, "forceStop").mockRejectedValueOnce(original);
    const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");
    await expect(threadsStore.getState().forceStop(ref)).rejects.toBe(original);
    expect(refresh).toHaveBeenCalledWith(ref);
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    const refreshing = refresh.mock.results[0]?.value as Promise<void>;
    if (readFails) {
      read.reject(new Error("read unavailable"));
      await expect(refreshing).rejects.toThrow("read unavailable");
      expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    } else {
      const saved = readResponse(ref, { status: { type: "notLoaded" } });
      saved.thread.evener.resumeRequired = true;
      saved.thread.evener.mutationStateAuthoritative = false;
      saved.thread.evener.capabilities = {} as ThreadCapabilities;
      read.resolve(saved);
      await refreshing;
      expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded");
      expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    }
    expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(fake.calls.filter((call) => call.method === "thread/resume" || call.method === "turn/queue")).toHaveLength(
      0,
    );
  },
);

test("a healthy authoritative refresh releases the fence after refused force stop", async () => {
  const ref = "local:healthy-owner";
  setMutationStorageForTests(new MutationOutboxIndexedDB());
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  const read = deferred<ThreadReadResponse>();
  fake.on("thread/read", () => read.promise);
  vi.spyOn(fake, "forceStop").mockRejectedValueOnce(new Error("termination refused"));
  const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");
  await expect(threadsStore.getState().forceStop(ref)).rejects.toThrow("termination refused");
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  read.resolve(readResponse(ref));
  await refresh.mock.results[0]?.value;
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  expect(fake.calls.some((call) => call.method === "thread/resume")).toBe(false);
});

// RoboRev Medium (PR 2862, round 9): stoppingRefs is a boolean Set with no
// ownership or generation. Two overlapping forceStop calls therefore cleared
// the fence when the FIRST completed, while the second stop was still
// draining - and a stale thread/read refresh could then clear the restart
// obligation too, reopening mutation dispatch for the rest of the stop
// window. A per-ref in-flight count keeps the fence armed until the last stop
// returns, whichever order they finish in.
test("an earlier forceStop completion cannot clear the fence while a second stop still drains", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const ref = "ref_a";
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  const stops = [deferred<void>(), deferred<void>()];
  let stopCalls = 0;
  fake.on("evener/thread/forceStop", () => stops[stopCalls++]?.promise ?? Promise.resolve());
  const first = threadsStore.getState().forceStop(ref);
  const second = threadsStore.getState().forceStop(ref);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
  // The first stop resolves while the second is still in flight.
  stops[0]?.resolve();
  await first;
  // The fence must stay armed: the second stop has not returned.
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
  // A stale refresh clearing the obligation must not reopen dispatch while
  // the second stop still drains: stoppingRefs is the independent fence.
  threadsStore.setState((state) => {
    const restartBlockingObligations = new Map(state.restartBlockingObligations);
    restartBlockingObligations.delete(ref);
    return { restartBlockingObligations };
  });
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
  // Only when the last stop returns does the fence clear.
  stops[1]?.resolve();
  await second;
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false);
});

// RoboRev Low (PR 2862, round 9, a regression from the round's own Stop
// fence): the restart obligation forceStop arms is restored only when the
// cancellation write fails. A requireClient() lookup failure (offline, no
// ready client) throws before any signal reaches a daemon, so the armed
// obligation must be restored exactly as the storage failure does - and
// refreshThread must NOT be kicked, since while offline it cannot clear the
// fence and only leaves a live session's recovery fenced.
test("forceStop with no connected client leaves the obligation untouched and does not refresh", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  connectionStore.setState({ client: null, state: "idle" });
  const ref = "local:offline";
  const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");
  await expect(threadsStore.getState().forceStop(ref)).rejects.toThrow("threads store: no client connected");
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false);
  expect(refresh).not.toHaveBeenCalled();
});

test("forceStop with no connected client restores a pre-existing obligation exactly", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  connectionStore.setState({ client: null, state: "idle" });
  const ref = "local:offline-owned";
  const original = Symbol();
  threadsStore.setState((state) => ({
    restartBlockingObligations: new Map(state.restartBlockingObligations).set(ref, original),
  }));
  const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");
  await expect(threadsStore.getState().forceStop(ref)).rejects.toThrow("threads store: no client connected");
  expect(threadsStore.getState().restartBlockingObligations.get(ref)).toBe(original);
  expect(refresh).not.toHaveBeenCalled();
});

// RoboRev Medium (PR 2862, round 11): each forceStop captured the obligation
// map's value and restored it unconditionally when it aborted, so overlapping
// stops clobbered the one shared recovery obligation. The obligation now lives
// in the refcounted stop group: captured once on the 0 -> 1 transition and
// restored on the 1 -> 0 transition only when no member ever signalled a
// daemon. An aborting member never writes back its own captured value over an
// entry another still-active member owns.
test("an aborted forceStop cannot clear the obligation a second stop owns", async () => {
  // Only the first stop's cancellation write fails; the second proceeds.
  let cancels = 0;
  const storage = new MutationOutboxIndexedDB({
    beforeCommit(operation) {
      if (operation === "cancelUnattempted" && ++cancels === 1) throw new Error("storage unavailable");
    },
  });
  setMutationStorageForTests(storage);
  const ref = "ref_a";
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  const forceStopRpc = vi.spyOn(fake, "forceStop").mockResolvedValue(undefined);

  const first = threadsStore.getState().forceStop(ref);
  const second = threadsStore.getState().forceStop(ref);
  await expect(first).rejects.toThrow("storage unavailable");
  await second;

  // The second stop reached the daemon, so the fence must stay armed even
  // though the aborted first stop's restore ran while it was still draining.
  expect(forceStopRpc).toHaveBeenCalledTimes(1);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false);
});

// RoboRev Medium (round 12): endStop restored the group's captured obligation
// on the no-signal 1 -> 0 transition unconditionally, so an aborting stop
// deleted (or overwrote) a recovery fence a CONCURRENT hydration had armed
// during its cancellation await. publishAndReconcileThreadHydration arms a fresh
// Symbol() on any read reporting resumeRequired, and such a read can resolve
// while the stop is still in that await; the abort then clobbered the newer
// fence, leaving the client unfenced while the hub still held ResumeRequired.
// The group now owns the exact symbol it armed and settles the ref's obligation
// only while the ref still holds it, so the hydration's fence survives.
test("an aborted forceStop leaves a concurrently armed recovery fence in place", async () => {
  const ref = "local:abort-hydration";
  const freshFence = Symbol("hydrated-fence");
  const storage = new MutationOutboxIndexedDB({
    beforeCommit(operation) {
      if (operation !== "cancelUnattempted") return;
      // A hydration arms a fresh obligation while the stop's cancellation write
      // is still in flight (publishAndReconcileThreadHydration's own setState),
      // then the write fails and aborts the stop before it signals a daemon.
      threadsStore.setState((state) => ({
        restartBlockingObligations: new Map(state.restartBlockingObligations).set(ref, freshFence),
      }));
      throw new Error("storage unavailable");
    },
  });
  setMutationStorageForTests(storage);
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);

  await expect(threadsStore.getState().forceStop(ref)).rejects.toThrow("storage unavailable");

  // The aborted stop must not delete the hydration's newer fence.
  expect(threadsStore.getState().restartBlockingObligations.get(ref)).toBe(freshFence);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false);
});

test("two overlapping aborted forceStops leave the obligation at its pre-group value", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  connectionStore.setState({ client: null, state: "idle" });
  const ref = "local:offline-owned";
  const original = Symbol();
  threadsStore.setState((state) => ({
    restartBlockingObligations: new Map(state.restartBlockingObligations).set(ref, original),
  }));
  const refresh = vi.spyOn(threadsStore.getState(), "refreshThread");

  const first = threadsStore.getState().forceStop(ref);
  const second = threadsStore.getState().forceStop(ref);
  await expect(first).rejects.toThrow("threads store: no client connected");
  await expect(second).rejects.toThrow("threads store: no client connected");

  // Neither stop reached a daemon: the obligation is exactly the pre-group
  // value, never a sibling stop's arming symbol.
  expect(threadsStore.getState().restartBlockingObligations.get(ref)).toBe(original);
  expect(threadsStore.getState().restartBlockingObligations.size).toBe(1);
  expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(false);
  expect(refresh).not.toHaveBeenCalled();
});

test("two overlapping aborted forceStops leave no stale obligation when none existed", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  connectionStore.setState({ client: null, state: "idle" });
  const ref = "local:offline";
  const first = threadsStore.getState().forceStop(ref);
  const second = threadsStore.getState().forceStop(ref);
  await expect(first).rejects.toThrow("threads store: no client connected");
  await expect(second).rejects.toThrow("threads store: no client connected");
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
});

test("clear permits explicit fresh recovery without replaying old-instance input", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const saved = readResponse("ref_a", { status: { type: "notLoaded" } });
  saved.thread.evener.mutationStateAuthoritative = false;
  const replacement = testThread("ref_a", { id: "replacement" });
  let cleared = false;
  fake.on("thread/read", () => (cleared ? { ...readResponse("ref_a"), thread: replacement } : saved));
  const started = deferred<void>();
  const release = deferred<void>();
  fake.on("thread/clear", async (params) => {
    started.resolve();
    await release.promise;
    cleared = true;
    return clearResponse(params, replacement);
  });
  fake.on("turn/queue", (params) => {
    expect(params.expectedInstanceId).toBe("thr_ref_a");
    throw new WireError("thread instance is stale", -32014, {
      evenerErrorInfo: "mutationOutcome",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "none",
    });
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  const clearing = threadsStore.getState().clearThread("ref_a");
  await started.promise;
  // Another tab can contribute an uncertain old-instance intent while clear is in flight.
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", expectedInstanceId: "thr_ref_a", input: [{ type: "text", text: "old input" }] },
    attachments: [],
    optimisticDisplay: { text: "old input" },
  });
  await storage.markAttempted(record.clientMutationId);
  await storage.markUnknown(record.clientMutationId, "blockedUnknown");
  release.resolve();
  await clearing;
  expect(threadsStore.getState().threads.get("ref_a")?.instanceId).toBe("replacement");
  expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  const reads = fake.calls.filter((call) => call.method === "thread/read").length;
  expect(await retryBlockedMutation(record.clientMutationId)).toBe(true);
  expect(fake.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThan(reads);
  await flushPendingTurnsProjectionForTests();
  expect(await storage.getRecovery(record.clientMutationId)).toMatchObject({
    recoveryKind: "rejected",
    payload: { expectedInstanceId: "thr_ref_a" },
  });
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
});

test("a stale client's ready callback cannot begin a generation for a replaced client", async () => {
  const stale = new FakeClient("connecting");
  const current = new FakeClient("ready");
  current.on("thread/read", () => readResponse("ref_test"));
  const staleReady = vi.spyOn(stale, "onReady");
  connectionStore.getState().connect(stale);
  connectionStore.setState({
    features: { ...(await stale.connect()).features },
  });
  // The callback the module registered on `stale`, captured before it ever
  // fires - `stale` is still mid-handshake, so nothing has begun yet.
  const staleReadyCallback = staleReady.mock.calls[0]?.[0];
  expect(staleReadyCallback).toBeDefined();

  connectionStore.getState().connect(current);
  connectionStore.setState({
    features: { ...(await current.connect()).features },
  });
  // Seed the current store with a tracked ref so handleReady would refresh it
  // if the stale callback somehow did trigger.
  await threadsStore.getState().ensureThread("ref_test");
  // Verify there's at least one tracked ref: if the stale callback ran its body,
  // it would clear this set to empty via setState({ mutationAuthorityRefs: new Set() }).
  expect(threadsStore.getState().threads.has("ref_test")).toBe(true);
  expect(threadsStore.getState().mutationAuthorityRefs.size).toBeGreaterThan(0);
  const authorityRefsSizeBeforeStale = threadsStore.getState().mutationAuthorityRefs.size;

  // The race this fixes: `stale`'s own dispatch can snapshot its ready
  // handlers before rewireClient's unsubscribe removes this one, so it
  // still runs - after `current` is already the wired client. Without the
  // guard, the stale callback would run its body: readyEpoch += 1,
  // setState({ mutationAuthorityRefs: new Set() }), then call
  // handleReady(stale, epoch).
  staleReadyCallback?.(await stale.connect());

  // The stale callback must not have run its body. If it had, mutationAuthorityRefs
  // would have been cleared to empty via setState({ mutationAuthorityRefs: new Set() })
  // in its first statement after incrementing readyEpoch.
  expect(threadsStore.getState().mutationAuthorityRefs.size).toBe(authorityRefsSizeBeforeStale);
});

// docs/design/stop-cancellation-outbox.md: the user's click is the cancel
// moment, recorded durably on the outbox rows themselves.
describe("Stop cancellation as durable outbox state", () => {
  // §9.1: the UI's real Stop path is the cancellation path.
  test("interrupt durably cancels the ref's pending rows and a later discovery scan does not resend them", async () => {
    // Fake the outbox's interval so the only scans are the ones this test
    // drives explicitly; flushIndexedDBUntil still works (it awaits real
    // IndexedDB operations, never timers).
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const storage = new MutationOutboxIndexedDB();
    try {
      setMutationStorageForTests(storage);
      const fake = connectMutationClient();
      await ensureActiveMutationTarget(fake, "ref_a");
      // A queued send still waiting in the outbox: never attempted.
      const queued = await storage.enqueueIntent({
        targetRef: "ref_a",
        threadId: "thr_ref_a",
        method: "turn/queue",
        payload: { ref: "ref_a", input: [{ type: "text", text: "queued behind the stop" }] },
        attachments: [],
        optimisticDisplay: null,
      });
      let queuedSends = 0;
      fake.on("turn/queue", (params) => {
        queuedSends += 1;
        return { receipt: mutationReceipt(params.clientMutationId) };
      });
      fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

      await threadsStore.getState().interrupt("ref_a");

      // The interrupt dispatches (its own record settles, leaving only the
      // canceled row) even though the canceled row sorts ahead of it.
      await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));
      expect(fake.calls.some((call) => call.method === "turn/interrupt")).toBe(true);
      expect((await storage.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
      await flushIndexedDBUntil(() => false);
      expect((await storage.listOutbox("ref_a")).map((record) => record.method)).toEqual(["turn/queue"]);
      expect(queuedSends).toBe(0);

      // A later discovery scan finds the ref (the canceled row still pins it)
      // but cannot resend it.
      let notified = false;
      const unsubscribe = subscribeMutationPersistence(() => {
        notified = true;
      });
      try {
        await vi.advanceTimersByTimeAsync(2000);
        await flushIndexedDBUntil(() => notified);
        expect(notified).toBe(true);
        await flushIndexedDBUntil(() => queuedSends > 0);
        expect(queuedSends).toBe(0);
        expect((await storage.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
      } finally {
        unsubscribe();
      }
    } finally {
      vi.useRealTimers();
    }
  });

  // §9.5: the cancellation write is the cancel moment, so its failure aborts
  // the stop before the daemon is touched, and the stop stays retryable.
  test("forceStop aborts before touching the daemon when the cancellation write fails", async () => {
    let failWrites = true;
    const storage = new MutationOutboxIndexedDB({
      beforeCommit(operation) {
        if (failWrites && operation === "cancelUnattempted") throw new Error("storage unavailable");
      },
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    const forceStopRpc = vi.spyOn(fake, "forceStop").mockResolvedValue(undefined);

    await expect(threadsStore.getState().forceStop("ref_a")).rejects.toThrow("storage unavailable");
    expect(forceStopRpc).not.toHaveBeenCalled();
    // The abort touched no daemon and leaves the user free to retry: the fence
    // armed with the drain is restored to its captured state (none was armed).
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);

    failWrites = false;
    await threadsStore.getState().forceStop("ref_a");
    expect(forceStopRpc).toHaveBeenCalledWith("ref_a");
  });

  test("shutdown aborts before touching the daemon when the cancellation write fails", async () => {
    let failWrites = true;
    const storage = new MutationOutboxIndexedDB({
      beforeCommit(operation) {
        if (failWrites && operation === "cancelUnattempted") throw new Error("storage unavailable");
      },
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    fake.on("thread/shutdown", () => ({}));

    await expect(threadsStore.getState().shutdown("ref_a")).rejects.toThrow("storage unavailable");
    expect(fake.calls.some((call) => call.method === "thread/shutdown")).toBe(false);

    failWrites = false;
    await threadsStore.getState().shutdown("ref_a");
    expect(fake.calls.some((call) => call.method === "thread/shutdown")).toBe(true);
  });

  // §4's boundary: with no mutation store at all (IndexedDB unavailable to
  // this tab) there are no durable rows a resurrection hazard could come
  // from, so write-first has nothing to protect and the stop proceeds.
  test("forceStop and shutdown proceed when no mutation store exists", async () => {
    const indexedDB = globalThis.indexedDB;
    vi.stubGlobal("indexedDB", undefined);
    try {
      const fake = connectFakeClient();
      const forceStopRpc = vi.spyOn(fake, "forceStop").mockResolvedValue(undefined);
      fake.on("thread/shutdown", () => ({}));

      await threadsStore.getState().forceStop("ref_a");
      expect(forceStopRpc).toHaveBeenCalledWith("ref_a");
      await threadsStore.getState().shutdown("ref_a");
      expect(fake.calls.some((call) => call.method === "thread/shutdown")).toBe(true);
    } finally {
      vi.stubGlobal("indexedDB", indexedDB);
    }
  });

  // §9.7: only an explicit user Retry releases a canceled row.
  test("an explicit Retry releases a canceled row and dispatches it", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const queued = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "stopped, then retried" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_a");
    let queuedSends = 0;
    fake.on("turn/queue", (params) => {
      queuedSends += 1;
      return { receipt: mutationReceipt(params.clientMutationId) };
    });

    expect(await retryBlockedMutation(queued.clientMutationId)).toBe(true);

    await flushIndexedDBUntil(() => queuedSends > 0);
    expect(queuedSends).toBe(1);
    // The receipt settled the retried row: nothing is left canceled.
    await flushIndexedDBUntil(() => false);
    expect(await storage.getOutbox(queued.clientMutationId)).toBeUndefined();
  });

  test("a background retry never releases a canceled row", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const queued = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "still stopped" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_a");

    expect(await retryBlockedMutation(queued.clientMutationId, "backgroundNote")).toBe(false);

    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
    expect((await storage.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
  });
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

  // The storage-fallback tests' shared opening: build the outbox on the
  // caller's own factory and database, install it for the store, connect the
  // client, and hydrate ref_a. The caller keeps the factory and the name - it
  // needs both to wedge the database afterwards and to open a reader connection
  // over the same rows, and two callers need them before this runs to seed a
  // row through their own adapter first.
  async function hydratedFallbackTarget({
    indexedDB,
    databaseName,
  }: {
    indexedDB: IDBFactory;
    databaseName: string;
  }): Promise<{ storage: MutationOutboxIndexedDB; fake: FakeClient }> {
    const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    return { storage, fake };
  }

  // §9 item 2 - the §2 reload hazard, end to end. The old mechanism kept the
  // cancellation fact in per-tab memory, and a reload's hydration is the one
  // moment restoreProvenAbsent reopens rows, armed with an empty in-memory
  // cancel set: the exact resurrection window. The durable state must survive
  // the reload itself - a fresh runtime, a fresh connection, the same
  // database - and a hydration that re-arms dispatch without sending a single
  // canceled row.
  test("a reload rehydrates and re-arms dispatch without resurrecting or resending canceled rows", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const before = new MutationOutboxIndexedDB();
      setMutationStorageForTests(before);
      const firstClient = connectMutationClient();
      await ensureActiveMutationTarget(firstClient, "ref_a");
      const queued = await before.enqueueIntent(queueIntent("queued when the stop landed"));
      const blocked = await before.enqueueIntent(queueIntent("blocked when the stop landed"));
      await before.markUnknown(blocked.clientMutationId, "blockedUnknown");
      firstClient.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
      firstClient.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

      await threadsStore.getState().interrupt("ref_a");
      await flushUntilArrived("tab A's interrupt to dispatch", () =>
        firstClient.calls.some((call) => call.method === "turn/interrupt"),
      );
      expect(firstClient.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
      expect((await before.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
      expect((await before.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");

      // The reload: the page's entire in-memory fact set is gone - runtime,
      // store state, connection - while the durable rows survive it untouched.
      before.close();
      resetThreadsStoreForTests();
      connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
      const after = new MutationOutboxIndexedDB();
      setMutationStorageForTests(after);
      const reloadedClient = connectMutationClient();
      reloadedClient.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
      await ensureActiveMutationTarget(reloadedClient, "ref_a");

      // The fresh connection reads the same durable rows, and the hydration's
      // reconciliation - the one reopen path - left them canceled.
      expect((await after.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
      expect((await after.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
      expect(reloadedClient.calls.filter((call) => call.method === "turn/queue")).toEqual([]);

      // A discovery scan - the reload's re-armed dispatchability is exactly
      // what it sees now - still sends nothing.
      let scanNotified = false;
      const unsubscribe = subscribeMutationPersistence(() => {
        scanNotified = true;
      });
      try {
        await vi.advanceTimersByTimeAsync(2000);
        await flushUntilArrived("the post-reload discovery scan to run", () => scanNotified);
        expect(reloadedClient.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
      } finally {
        unsubscribe();
      }

      // Liveness control, so "nothing dispatched" cannot be vacuous: the
      // reloaded store still sends NEW work over the same armed machinery,
      // proving the ref is dispatchable and the canceled rows specifically
      // are what it holds back.
      await threadsStore.getState().queue("ref_a", "sent after the reload");
      await flushUntilArrived("the post-reload send to dispatch", () =>
        reloadedClient.calls.some((call) => call.method === "turn/queue"),
      );
      expect(
        reloadedClient.calls
          .filter((call) => call.method === "turn/queue")
          .map((call) => (call.params as { input?: { text?: string }[] }).input?.[0]?.text),
      ).toEqual(["sent after the reload"]);
      expect((await after.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
      expect((await after.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
    } finally {
      vi.useRealTimers();
    }
  });

  // §9 item 3: two connections over one database. Tab A is the live store
  // holding the Stop; tab B is a second MutationOutboxIndexedDB connection
  // making exactly the reads its own hydration and discovery would make
  // against the shared rows - including while tab A's interrupt is still on
  // the wire, the interleave the old in-memory set survived only by
  // transaction-ordering luck.
  test("a second connection over the same database cannot resurrect tab A's canceled rows", async () => {
    const tabA = new MutationOutboxIndexedDB();
    setMutationStorageForTests(tabA);
    const clientA = connectMutationClient();
    await ensureActiveMutationTarget(clientA, "ref_a");
    const queued = await tabA.enqueueIntent(queueIntent("queued in tab A"));
    const blocked = await tabA.enqueueIntent(queueIntent("blocked in tab A"));
    await tabA.markUnknown(blocked.clientMutationId, "blockedUnknown");
    let queuedSends = 0;
    clientA.on("turn/queue", (params) => {
      queuedSends += 1;
      return { receipt: mutationReceipt(params.clientMutationId) };
    });
    // Hold the interrupt's RPC on the wire: the Stop is already durably
    // canceled (the click committed), tab A's dispatch of the interrupt is
    // parked, and tab B wakes and runs its scan in that window.
    let releaseInterrupt!: () => void;
    let interruptOnWire = false;
    clientA.on("turn/interrupt", (params) => {
      interruptOnWire = true;
      return new Promise((resolve) => {
        releaseInterrupt = () => resolve({ receipt: mutationReceipt(params.clientMutationId) });
      });
    });

    await threadsStore.getState().interrupt("ref_a");
    await flushUntilArrived("tab A's interrupt to reach the wire", () => interruptOnWire);

    const tabB = new MutationOutboxIndexedDB();
    try {
      // Tab B's scan reads the shared rows: the two the Stop canceled stay
      // canceled, and the interrupt itself is the one record still live.
      expect((await tabB.listOutbox("ref_a")).map((record) => record.state)).toEqual([
        "canceled",
        "canceled",
        "submitting",
      ]);
      // Tab B's reconciliation - the hydration's reopen path, run with none of
      // tab A's memory - reopens nothing.
      expect(await tabB.restoreProvenAbsent("ref_a", new Set())).toEqual([]);
      expect((await tabB.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
      expect((await tabB.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
      // Tab B's dispatch pick is the still-in-flight interrupt record - the
      // FIFO head it honestly owes the daemon - and never a canceled row.
      expect((await tabB.nextDispatchable("ref_a"))?.method).toBe("turn/interrupt");
    } finally {
      tabB.close();
    }

    // Tab A's interrupt settles, and its own store-level reconciliation over
    // the same shared rows (the wakeup's targeted resync) cannot resurrect
    // them either.
    releaseInterrupt();
    await flushUntilArrived("the interrupt record to settle out of the outbox", async () =>
      (await tabA.listOutbox("ref_a")).every((record) => record.method !== "turn/interrupt"),
    );
    await threadsStore.getState().refreshThread("ref_a");
    expect((await tabA.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
    expect((await tabA.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
    expect(queuedSends).toBe(0);

    // Liveness: tab A's store still sends new work through the shared outbox.
    await threadsStore.getState().queue("ref_a", "tab A sends after the stop");
    await flushUntilArrived("tab A's new send to dispatch", () => queuedSends === 1);
    expect((await tabA.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
    expect((await tabA.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
  });

  // An enqueue clicked before the Stop but whose durable write had not yet
  // been issued when the stop's cancel transaction committed - a cold tab's
  // connection still opening, its runtime still starting, while a warm tab's
  // Stop lands - is the one interleave IndexedDB's transaction ordering cannot
  // fence: the engine's FIFO puts that write AFTER the cancel scan, so the row
  // would commit "submitting" and dispatch into the session the user just
  // stopped. The durable stop-epoch barrier (§4) closes it: the enqueue
  // captures the ref's stop epoch at click time, and a Stop landing in
  // between makes the row commit born-"canceled" - still announced for the
  // canceled queue strip, never dispatched. The seam wrapper below only
  // delays the adapter's real write; the write that lands is the real one.
  test("an in-flight enqueue whose write lands after the stop's cancel commits canceled, never dispatched", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    const realEnqueueIntent = storage.enqueueIntent.bind(storage);
    const writeReached = deferred<void>();
    const writeRelease = deferred<void>();
    storage.enqueueIntent = (...args: Parameters<typeof realEnqueueIntent>) => {
      writeReached.resolve();
      return writeRelease.promise.then(() => realEnqueueIntent(...args));
    };

    // The send is clicked and in flight; its write waits at the seam.
    const send = threadsStore.getState().queue("ref_a", "clicked before the stop");
    await writeReached.promise;
    // The Stop's cancel commits while the send's write is still in flight.
    await threadsStore.getState().interrupt("ref_a");
    // The send's write finally issues - after the cancel, the finding's order.
    writeRelease.resolve();
    await send;

    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));
    await flushIndexedDBUntil(() => false);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
    const queued = (await storage.listOutbox("ref_a")).find((record) => record.method === "turn/queue");
    expect(queued?.state).toBe("canceled");
  });

  // RoboRev PR #1873 medium, the fresh review's stop-epoch capture race: the
  // capture was requested after `await runtime.start`, so on a connection that
  // is still opening at the click — the finding's cold tab — everything else
  // could take the queue position first: a warm tab's Stop committed during the
  // opening wait, the capture read the POST-Stop epoch, the comparison passed
  // equal, and the row committed "submitting" into the session the user just
  // stopped. The capture's read transaction must be REQUESTED at true click
  // time — inside the click's own synchronous prefix, before any startup wait
  // in the enqueue chain — so the engine's creation-order queue puts the
  // capture ahead of every write the click precedes, and the Stop that commits
  // during this tab's connection setup leaves its bump where the enqueue's own
  // comparison reads it.
  test("an enqueue whose click requests the capture first fences a Stop committed during its connection setup", async () => {
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
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    // The other tab: a second real connection to the same durable database —
    // no store of ours, exactly a warm sibling whose Stop is a real write.
    const tabA = new MutationOutboxIndexedDB();

    // The cold tab: THIS store's connection is closed at the click, so the
    // capture read must reopen it before it can run — the finding's opening
    // wait, however long the engine takes to satisfy it.
    connections[connections.length - 1]?.dispatchEvent(new IDBVersionChangeEvent("versionchange"));
    await nextMacrotask();

    // The click. queue()'s synchronous prefix must request the capture read —
    // and with it the reopen — before yielding to the event loop.
    const send = threadsStore.getState().queue("ref_a", "clicked before the stop");
    // The warm tab's Stop commits while the cold tab's connection is still
    // opening, before the barrier read resolves. No yield precedes it: this
    // is exactly the queue position the click-time request has to own.
    await tabA.enqueueInterruptAndCancel({
      targetRef: "ref_a",
      method: "turn/interrupt",
      payload: { ref: "ref_a" },
      attachments: [],
      optimisticDisplay: { method: "turn/interrupt" },
    });
    await send;

    // The Stop's own cancel scan committed before the row existed, so the
    // epoch barrier is the only fence that can still catch it — and must:
    // the capture was requested at the click, the Stop's bump landed after
    // it, and the enqueue's own comparison reads the bumped epoch.
    const queued = (await storage.listOutbox("ref_a")).find((record) => record.method === "turn/queue");
    expect(queued?.state).toBe("canceled");
    // Born-canceled rows never dispatch: the send clicked before the Stop
    // must not reach the daemon after it.
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
  });

  // §4's honest boundary, the verdict's minimum test: a Stop whose durable
  // write is CREATED before the capture read's transaction can exist — a cold
  // tab whose connection is still opening at the Stop's click, or an engine
  // whose cross-connection scheduling puts a concurrent Stop's write ahead of
  // a capture requested in the same instant — commits before the barrier read
  // resolves, and the capture then reads the POST-Stop epoch. The comparison
  // passes equal and the row commits "submitting": the barrier cannot fence
  // it, because the database holds no record of the click and a cold tab holds
  // no connection to read one with. No client-side mechanism closes this
  // window; §4 bounds it (the click requests the capture, so every Stop
  // created after that request is fenced), and this test pins the residual's
  // shape — the row goes live and dispatches, indistinguishable in the
  // database from §9 item 10's deliberate post-Stop send, which must send.
  // A future change that claims to close this window must update §4 first,
  // and this pin is what forces that reckoning.
  test("the residual window: a Stop committed before the capture read's transaction exists refreshes the baseline invisibly", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    // The other tab's Stop, as a real write on a second real connection.
    const tabA = new MutationOutboxIndexedDB();
    const stopCommitted = tabA.enqueueInterruptAndCancel({
      targetRef: "ref_a",
      method: "turn/interrupt",
      payload: { ref: "ref_a" },
      attachments: [],
      optimisticDisplay: { method: "turn/interrupt" },
    });

    // The capture read held at the seam until the Stop has committed — the
    // cold-open order: the read that lands is the real one, its transaction
    // simply cannot exist before the Stop's write does.
    const realReadStopEpoch = storage.readStopEpoch.bind(storage);
    storage.readStopEpoch = async (targetRef: string) => {
      await stopCommitted;
      return realReadStopEpoch(targetRef);
    };

    await threadsStore.getState().queue("ref_a", "clicked before the stop");

    // The residual: the capture read the post-Stop epoch, the comparison
    // passed equal, and the row committed "submitting".
    const queued = (await storage.listOutbox("ref_a")).find((record) => record.method === "turn/queue");
    expect(queued?.state).toBe("submitting");
    // ... and it dispatches — the one resurrection shape the barrier cannot
    // close, bounded to the window above and pinned here.
    await flushUntilArrived("the residual's send to dispatch", () =>
      fake.calls.some((call) => call.method === "turn/queue"),
    );
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  });

  // RoboRev PR #1873 Low, the fresh review: the click-time capture was
  // awaited OUTSIDE the try whose catch disarms the ref when the durable
  // enqueue fails, so a capture that rejects - a VersionError from a
  // versionchange, a retired connection - aborted the submission AND left the
  // ref armed in dispatchableMutationRefs with no durable row behind it. A
  // later discovery pass that names the ref then ran its dispatch work anyway,
  // for a ref with nothing of its own to dispatch. The failed capture must be
  // treated exactly like the failed enqueue it precedes: disarmed on the way
  // out.
  //
  // A composer send whose capture fails does not abort: it takes the
  // direct-dispatch fallback (pinned by "a send whose storage never answers
  // still reaches the daemon" and the quota variant). A Promote has no
  // fallback, so it keeps the abort-and-disarm contract this test pins.
  test("a rejecting stop-epoch capture aborts the submission without leaving the ref's dispatch bookkeeping armed", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const storage = new MutationOutboxIndexedDB();
      setMutationStorageForTests(storage);
      const fake = connectMutationClient();
      await ensureActiveMutationTarget(fake, "ref_a");
      fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

      // The ref's one durable row is terminal: a canceled row keeps the ref
      // in listTargetRefs - so a later discovery pass names it - while
      // nextDispatchable provably has nothing to give. Exactly the ref whose
      // stale arm would cost a dispatch pass's work for nothing.
      const seeded = await storage.enqueueIntent(queueIntent("canceled before the failed click"));
      await storage.cancelUnattempted("ref_a");
      expect((await storage.getOutbox(seeded.clientMutationId))?.state).toBe("canceled");

      // The capture fails the way a versionchange or a retired connection does.
      storage.readStopEpoch = async () => {
        throw new Error("Mutation outbox upgrade is blocked");
      };

      // The submission aborts and leaves nothing durable behind.
      await expect(
        threadsStore.getState().promoteQueuedAsSteer("ref_a", 0, "entry_1", { text: "past a failed capture" }),
      ).rejects.toThrow("Mutation outbox upgrade is blocked");
      expect((await storage.listOutbox("ref_a")).filter((record) => record.state === "submitting")).toEqual([]);

      // A later discovery pass names the ref (its canceled row). What the
      // pass must NOT do is dispatch work for it: the failed click armed
      // nothing. scheduleMutationDispatch filters refs through the armed
      // bookkeeping and calls the dispatcher synchronously within the pass,
      // right after the persistence notification - so once the pass has
      // named the ref, the dispatcher either was or was not asked, with no
      // further settling in between.
      let discoveredRefA = false;
      const unsubscribed = subscribeMutationPersistence((refs) => {
        if (refs.includes("ref_a")) discoveredRefA = true;
      });
      const dispatchTargets = vi.spyOn(MutationDispatcher.prototype, "dispatchTargets");
      try {
        await vi.advanceTimersByTimeAsync(2000);
        await flushUntilArrived("the discovery pass to name the ref", () => discoveredRefA);
        expect(dispatchTargets.mock.calls.some(([refs]) => Array.from(refs).includes("ref_a"))).toBe(false);
        // And nothing reached the wire for the failed click or the ref's dead row.
        expect(fake.calls.filter((call) => call.method === "turn/promoteQueuedAsSteer")).toEqual([]);
        expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
      } finally {
        dispatchTargets.mockRestore();
        unsubscribed();
      }
    } finally {
      vi.useRealTimers();
    }
  });

  // The durable outbox exists so a send survives a reload, but it must not
  // become a hard dependency: when storage never answers, the send goes out as
  // a plain RPC (like setModel/rename/compact) with no durable row, instead of
  // failing closed after the 10s watchdog and dispatching nothing. This is the
  // adapter's open never answering - the wedged shape PR #2983 stopped
  // aborting upgrades for.
  test("a send whose storage never answers still reaches the daemon and leaves no durable row", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback";
    // Hydrate on working storage first: the mutation runtime is created and its
    // connection opened here, so it is the SEND that meets the wedge below
    // rather than the hydration's own reconciliation reads.
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    // Retire the connection, then make the factory's next open never answer:
    // storage is wedged for every call the send makes.
    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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

  // An outbox schema bug fails the send closed: it never goes out without its
  // durable row (see IDBUpgradeError).
  test("a send whose outbox schema upgrade throws stays fail-closed", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    // The send's open lands on a database that needs its schema again, and the
    // upgrade's first store creation throws.
    storage.close();
    await deleteMutationDatabase();
    const schemaBug = new TypeError("schema bug");
    vi.spyOn(IDBDatabase.prototype, "createObjectStore").mockImplementation(() => {
      throw schemaBug;
    });
    const send = threadsStore.getState().send("ref_a", "not sent past a schema bug");
    await expect(send).rejects.toBeInstanceOf(IDBUpgradeError);
    await expect(send).rejects.toMatchObject({ cause: schemaBug });
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  });

  // A browser that denies this origin IndexedDB (a privacy mode, blocked site
  // data) refuses the open with SecurityError: a storage policy, not a verdict
  // on the send, so the send goes out directly.
  test("a send whose browser denies IndexedDB access still reaches the daemon", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-denied";
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi.spyOn(indexedDB, "open").mockImplementation((name: string, version?: number) => {
      if (name === databaseName) throw new DOMException("The operation is insecure.", "SecurityError");
      return open(name, version);
    });
    try {
      await threadsStore.getState().send("ref_a", "sent while storage is denied");
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      openSpy.mockRestore();
    }
  });

  // With no IndexedDB at all - the global absent, not merely failing - the
  // mutation runtime is never created, and a composer send must still reach the
  // daemon on the direct path, the same product rule every other storage
  // failure follows. Before #4194 the durable enqueue threw "IndexedDB is
  // unavailable" here, so every send failed.
  test("a send goes out directly when IndexedDB is absent entirely", async () => {
    const savedIndexedDB = globalThis.indexedDB;
    vi.stubGlobal("indexedDB", undefined);
    try {
      const fake = connectMutationClient();
      fake.on("turn/start", (params) => ({
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        receipt: mutationReceipt(params.clientMutationId),
      }));
      await ensureActiveMutationTarget(fake, "ref_a");

      await threadsStore.getState().send("ref_a", "sent with no IndexedDB");

      const sent = fake.calls.filter((call) => call.method === "turn/start");
      expect(sent, "the send never reached the daemon").toHaveLength(1);
      const sentParams = sent[0]?.params as { clientMutationId?: string; input?: unknown } | undefined;
      expect(sentParams?.clientMutationId).toBeTruthy();
      expect(sentParams?.input).toBeDefined();
    } finally {
      vi.stubGlobal("indexedDB", savedIndexedDB);
    }
  });

  // The direct path is for the four composer send verbs only: a durable write of
  // any other method keeps its fail-closed contract even with no IndexedDB.
  // turn/promoteQueuedAsSteer stands for that boundary - an ordinary "enqueue"
  // durable write that is not a composer send verb.
  test("a non-send durable verb still fails closed when IndexedDB is absent", async () => {
    const savedIndexedDB = globalThis.indexedDB;
    vi.stubGlobal("indexedDB", undefined);
    try {
      const fake = connectMutationClient();
      await ensureActiveMutationTarget(fake, "ref_a");

      // The refusal's text is copy, not a contract: assert the behavior (no
      // durable row could be written, so the verb fails closed and no RPC
      // leaves) rather than the message (docs/developing-evener/testing.md,
      // "Copy Is Not a Test Oracle").
      await expect(
        threadsStore.getState().promoteQueuedAsSteer("ref_a", 1, "entry_2", { text: "queued message" }),
      ).rejects.toThrow();
      expect(fake.calls.some((call) => call.method === "turn/promoteQueuedAsSteer")).toBe(false);
    } finally {
      vi.stubGlobal("indexedDB", savedIndexedDB);
    }
  });

  // Another tab still holding the previous schema blocks the outbox's upgrade,
  // and the adapter refuses the open at once rather than wait. That refusal is
  // the storage's, not the send's, so the send goes out directly.
  test("a send whose outbox upgrade another tab blocks still reaches the daemon", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-blocked";
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    storage.close();
    const open = indexedDB.open.bind(indexedDB);
    const openSpy = vi
      .spyOn(indexedDB, "open")
      .mockImplementation((name: string, version?: number) =>
        name === databaseName ? blockedUpgradeRequest() : open(name, version),
      );
    try {
      await threadsStore.getState().send("ref_a", "sent while the upgrade is blocked");
      expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    } finally {
      openSpy.mockRestore();
    }
  });

  // The fallback send's RPC is its only transport, so a failure on the wire
  // must be retried rather than surfaced. The id is minted once and reused
  // across the attempts, so a reply lost after the daemon applied the mutation
  // is deduped on the retry instead of applied twice. The composer stays in its
  // "Sending" state through the ladder (the submission wraps the whole enqueue).
  test("a fallback send retries a wire failure under the same clientMutationId", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-retry";
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", () => {
      throw new WireError("turn is not active", -32013, { evenerErrorInfo: "conflict" });
    });

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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

  // The wedge this fallback exists for also fails the ref's reconciliation
  // (its outbox read never answers), and that failure must not undo the
  // fallback: a storage-blocked ref is admissible to the send, whose own
  // in-memory ordering guards carry the ordering responsibility, while the
  // durable dispatcher stays fenced until discovery's retry reconciles. Before
  // the split, the reconcile failure fenced the fallback and every send died
  // with the watchdog's error until a page reload.
  test("a send survives a reconciliation that the same storage wedge failed", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-reconcile-wedge";
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    // Retire the connection and wedge every open on the outbox database, then
    // fail a reconciliation through it: its outbox read meets the same
    // never-answering open the send's capture will.
    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      void threadsStore
        .getState()
        .refreshThread("ref_a")
        .catch(() => undefined);
      // The reconcile read's watchdog is the only thing that ends it.
      await vi.advanceTimersByTimeAsync(11_000);
      await flushMicrotasks();

      const send = threadsStore.getState().send("ref_a", "sent while the wedge also failed the reconcile");
      // The assertion attaches before the advance: the watchdog's rejection
      // fires inside it, across an Immediate boundary, and a promise still
      // without a handler there would surface as an unhandled rejection.
      const delivered = expect(send).resolves.toBeUndefined();
      // The click-time capture's open never answers either; the watchdog ends
      // it and the send must survive both the wedge and the reconcile failure
      // it caused.
      await vi.advanceTimersByTimeAsync(10_000);
      await delivered;

      // The ref really was recorded storage-blocked by the failed reconcile -
      // the premise this journey exists for, not an incidental pass.
      expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
      const sent = fake.calls.find((call) => call.method === "turn/start");
      expect(sent, "the send never reached the daemon").toBeDefined();
      const sentParams = sent?.params as { clientMutationId?: string; input?: unknown } | undefined;
      expect(sentParams?.clientMutationId).toBeTruthy();
      expect(sentParams?.input).toBeDefined();
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    // The wedged enqueue left no durable row, and neither did the fallback.
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // A wedged reconcile read never settles, so the ref can sit in
  // pendingMutationReconciliations for exactly as long as the wedge holds -
  // the one fence the storage-blocked admission must also skip, or the fallback
  // would strand the send for the whole wedge anyway. The enqueue is wedged to
  // the same storage timeout the fallback answers; the record path is pinned
  // by the storage-blocked classification test, so the slice is set by hand
  // here to isolate this fence.
  test("a send survives a reconciliation still pending on the same storage wedge", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    // Hold a reconcile open forever: its outbox read never settles.
    const parked = parkRefReconcileRead(storage, "ref_a");
    void threadsStore.getState().refreshThread("ref_a");
    await flushUntil(parked.opened);
    threadsStore.setState({ mutationReconciliationStorageBlocked: new Set(["ref_a"]) });

    // The durable write cannot be made: both enqueue attempts time out. The
    // capture read is a different storage method and still succeeds, so the
    // fallback decision is the only seam this exercises.
    timeOutDurableEnqueues(storage);

    const send = threadsStore.getState().send("ref_a", "sent while the reconcile is wedged open");
    await send;
    expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true);
  });

  // A body that throws inside the adapter's real transaction rejects with its
  // own error, never the AbortError its abort then reports: here the
  // cross-store id guard meets a recovery record another ref holds. That is a
  // record judgment, so the send fails closed with nothing sent.
  test("a send whose write the outbox's own id guard refuses stays fail-closed", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-shared" });
    const refused = await storage.enqueueIntent({
      targetRef: "ref_b",
      method: "turn/queue",
      payload: { ref: "ref_b", input: [{ type: "text", text: "refused elsewhere" }] },
      attachments: [],
      optimisticDisplay: { text: "refused elsewhere" },
    });
    await storage.transferToRecovery(refused.clientMutationId, "rejected", "turn is not active");
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");

    await expect(threadsStore.getState().send("ref_a", "collides with the recovery record")).rejects.toThrow(
      "clientMutationId is already active",
    );
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  });

  // A retired connection (or a VersionError) refuses the write at once and
  // fails the reconcile's read the same way, so a reconcile still pending for
  // the ref sits on the same broken storage: the fallback classifies it and
  // sends rather than meet the pending fence.
  test("a send survives a reconciliation still pending when the browser refuses its write", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));
    const parked = parkRefReconcileRead(storage, "ref_a");
    void threadsStore.getState().refreshThread("ref_a");
    await flushUntilArrived("the reconcile read to park", parked.opened);
    storage.enqueueIntent = async () => {
      throw new DOMException("The database connection is closing.", "InvalidStateError");
    };

    await threadsStore.getState().send("ref_a", "sent while the connection is retired");
    expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
  });

  // The fallback answers storage failures only. An error that is not one - the
  // outbox's own validation, or a payload IndexedDB cannot clone - keeps the
  // ordinary submission failure, with nothing sent.
  test.each([
    { failure: "a validation error", error: () => new Error("targetRef is required") },
    { failure: "an uncloneable payload", error: () => new DOMException("could not be cloned", "DataCloneError") },
    { failure: "a missing object store", error: () => new DOMException("no such store", "NotFoundError") },
    {
      failure: "a request on a finished transaction",
      error: () => new DOMException("transaction finished", "TransactionInactiveError"),
    },
  ])("a send whose durable write fails with $failure stays fail-closed", async ({ error }) => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    const thrown = error();
    storage.enqueueIntent = async () => {
      throw thrown;
    };
    await expect(threadsStore.getState().send("ref_a", "not a storage failure")).rejects.toBe(thrown);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  });

  // The classification must not wait for the reconcile's own watchdog. The
  // send's fallback decision can land while the wedged reconcile is still
  // pending - before the reconcile rejected and recorded the ref - and a send
  // pressed in that window must still deliver: the fallback's own storage
  // timeout is the same wedge, so it classifies the pending read itself
  // instead of meeting the pending fence and dying with the wedge's error.
  // (This replaces an earlier test that hand-injected the slice; a real
  // MutationStorageTimeoutError IS storage unavailability, so "healthy
  // pending reconcile plus a timeout-class enqueue failure" is not a state
  // production can reach. The pending fence still holds every non-fallback
  // caller, and a reconcile that later succeeds clears the record.)
  test("a send survives a wedged reconcile that has not yet rejected", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-pending-wedge";
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    try {
      // The send goes out first: its capture read parks on the wedge, and the
      // shared open's watchdog ends it at +10s.
      const send = threadsStore.getState().send("ref_a", "sent while the wedged reconcile is still pending");
      const delivered = expect(send).resolves.toBeUndefined();
      // Two seconds later a refresh starts a reconcile whose read parks on
      // the same wedged open; its rejection - and the record it would write -
      // lands only when the shared watchdog fires.
      await vi.advanceTimersByTimeAsync(2_000);
      void threadsStore
        .getState()
        .refreshThread("ref_a")
        .catch(() => undefined);
      await flushMicrotasks();
      await vi.advanceTimersByTimeAsync(9_000);
      await delivered;

      const sent = fake.calls.find((call) => call.method === "turn/start");
      expect(sent, "the send never reached the daemon").toBeDefined();
      // The reconcile's own rejection still records the ref afterwards - the
      // attempt was never stolen from it.
      expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
    } finally {
      vi.useRealTimers();
      openSpy.mockRestore();
    }
    const reader = new MutationOutboxIndexedDB({ indexedDB, databaseName });
    expect(await reader.listOutbox("ref_a")).toEqual([]);
    reader.close();
  });

  // The fence this split must not widen: a GENUINE reconciliation failure (a
  // mutation-state conflict, not a storage timeout) still refuses the fallback,
  // exactly as before the storage-blocked slice existed.
  test("a genuine reconciliation failure still refuses the send fallback", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");

    threadsStore.setState({ mutationReconciliationFailures: new Set(["ref_a"]) });
    timeOutDurableEnqueues(storage);

    await expect(threadsStore.getState().send("ref_a", "sent while a real reconcile failed")).rejects.toBeInstanceOf(
      MutationStorageTimeoutError,
    );
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  });

  // The guard is `err instanceof WireError`, i.e. EVERY settled WireError -
  // not only a conflict. A non-conflict refusal is surfaced unchanged, not
  // mapped to ConflictError and not retried.
  test("a fallback send does not retry a non-conflict WireError", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-send-fallback-invalid-params";
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", () => {
      throw new WireError("bad params", -32602);
    });

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", () => {
      fake.emitStateChange("reconnecting");
      throw new Error("AppwireClient: socket closed");
    });

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/start", () => {
      throw new Error("FakeClient: socket closed");
    });

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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

    // Hydrating reconciles the store's view: the ref is retained (a canceled
    // row) but has no undelivered work.
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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

    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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

  // Round-7 Medium: a fenced ref must fail closed rather than reach the daemon
  // directly. A restart-blocked daemon is the one shape Send cannot recover -
  // its older process has to be stopped first - so admission refuses the send
  // outright, before any durable write, and no storage fallback can carry it
  // to the wire either.
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

    await expect(threadsStore.getState().send("ref_a", "blocked")).rejects.toThrow(
      "Send isn't available until this session is resumed",
    );
    // The fenced ref must not reach the daemon, and no row is minted for it.
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    expect(await storage.listOutbox("ref_a")).toEqual([]);
  });

  // Round-12 Low: the fallback set has four send verbs; behavior was pinned for
  // turn/start and turn/steer, so pin the rest. Queue and Drain go out as plain
  // RPCs with no durable row; Promote stays fail-closed.
  test("a Queue whose storage never answers still reaches the daemon and leaves no durable row", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-queue-fallback";
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/drainAsSteer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    // Hydrate on working storage first, exactly like the send case: the
    // runtime's connection opens here, so it is the Stop that meets the wedge.
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    // Retire the connection, then make the factory's next open never answer.
    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a")));

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    fake.on("notes/human/set", (params) => ({ note: "a note", receipt: mutationReceipt(params.clientMutationId) }));

    storage.close();
    const openSpy = wedgeDatabaseOpens(indexedDB, databaseName);
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
}); // The merely-resumable snapshot the three tests below drive: ResumeRequired with
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
  expect(resumeOnlyModel(ref)).toBe(true);

  // The connection that produced the snapshot is replaced by a fresh one.
  const next = new FakeClient("ready");
  next.on("thread/read", (params) => snapshot(params.ref ?? ref));
  connectionStore.getState().connect(next);

  // Synchronously after the rewire the outgoing connection's bit is gone: no
  // folded send is admitted from a bit the previous connection produced.
  expect(threadsStore.getState().threads.get(ref)?.resumeOnlyFoldable).toBe(false);
  expect(resumeOnlyModel(ref)).toBe(false);

  // A fresh read on the current connection, still carrying the bit, restores
  // folding.
  await waitFor(() => expect(resumeOnlyModel(ref)).toBe(true));
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
  expect(resumeOnlyModel(ref)).toBe(false);
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
  expect(resumeOnlyModel(ref)).toBe(false);
  // Only once the durable outbox has loaded is the ref foldable.
  await refreshPendingTurnsProjection(ref);
  // The runtime's startup discovery scan starts an all-targets read that
  // out-ranks this specific one; readiness now waits for the latest read, so
  // settle the outstanding work before asserting the ref is loaded.
  await flushPendingTurnsProjectionForTests();
  expect(hasBlockedUnknown(ref)).toBe(false);
  expect(hasQueuedNonSend(ref)).toBe(false);
  expect(resumeOnlyModel(ref)).toBe(true);
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
  test("release and reacquire supersedes the departed acquisition's pending membership and fences the retired model", async () => {
    const fake = connectFakeClient(),
      initial = deferred<ThreadReadResponse>(),
      replacement = deferred<ThreadReadResponse>();
    let reads = 0;
    fake.on("thread/read", () => (++reads === 1 ? initial.promise : replacement.promise));
    const first = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads === 1);
    threadsStore.getState().releaseThread("ref_a");
    const second = threadsStore.getState().ensureThread("ref_a");
    // The reopened pane's own read issues now — it does not queue behind the
    // departed holder's still-pending request, which may never settle.
    await flushUntil(() => reads === 2);
    expect(reads).toBe(2);
    initial.resolve(
      readResponse("ref_a", { turns: [{ id: "old", status: "completed", itemsView: "full", items: [] }] }),
    );
    await flushMicrotasks();
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    replacement.resolve(
      readResponse("ref_a", { turns: [{ id: "new", status: "completed", itemsView: "full", items: [] }] }),
    );
    await Promise.all([first, second]);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("new");
    threadsStore.getState().releaseThread("ref_a");
  });
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
// A Stop that lands WHILE the drive waits out a drain is a person canceling
// the message the drive would send, so the baseline must be captured before
// the wait; captured after it, that Stop would read as already accounted for
// and the resume would run anyway.
test("a Stop during the drain wait cancels the pending resume", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const ref = "local:drain-then-stop";
  await threadsStore.getState().ensureThread(ref);
  const stop = deferred<void>();
  const requested = nextHandledRequest(fake, "evener/thread/forceStop", () => stop.promise);
  const hold = holdNextWriteTransaction(["outbox", "sequences"]);
  try {
    const pending = threadsStore.getState().forceStop(ref);
    await hold.reached;
    await threadsStore.getState().send(ref, "text");
    hold.release();
    await requested;
    expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
    // The second Stop lands inside the drive's wait for this drain.
    await threadsStore.getState().interrupt(ref);
    stop.resolve();
    await pending;
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "thread/resume"));
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toEqual([]);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    await waitFor(() => expect(threadsStore.getState().resumeFailures.get(ref)?.stopCanceled).toBe(true));
  } finally {
    hold.release();
    stop.resolve();
  }
});

test("a Send during a Stop drain parks and resumes once the drain settles", async () => {
  let minted = 0;
  const storage = new MutationOutboxIndexedDB({
    createMutationId: () => {
      minted += 1;
      return `stop-drain-${minted}`;
    },
  });
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const ref = "local:draining";
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false);
  const stop = deferred<void>();
  const requested = nextHandledRequest(fake, "evener/thread/forceStop", () => stop.promise);
  // Hold the cancellation write open. The drain window begins at the click,
  // and this is the window BEFORE that write completes. Send stays offered
  // throughout: its row parks, and the drive waits the drain out.
  const hold = holdNextWriteTransaction(["outbox", "sequences"]);
  try {
    const pending = threadsStore.getState().forceStop(ref);
    await hold.reached;
    expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    await threadsStore.getState().send(ref, "text");
    // The press parked its row; no RPC leaves while the drain stands.
    expect((await storage.listOutbox(ref)).map((record) => record.method)).toEqual(["turn/start"]);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    hold.release();
    await requested;
    // Mid-drain: the hub holds Stopping > 0 and refuses even turn/start, so
    // neither the parked send nor the resume it drives may reach the wire.
    expect(threadsStore.getState().stoppingRefs.has(ref)).toBe(true);
    expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toEqual([]);
    expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    stop.resolve();
    await pending;
    // The drain has settled, so the waiting drive issues the resume it
    // parked behind, and the send follows it.
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "thread/resume"));
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
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
test("a local session with a blockedUnknown row takes the Send-resumes face and parks the send", async () => {
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
  // R09: the delivery-uncertain row no longer keeps Send refused - this shape
  // is the Send-resumes face. The send is admitted and PARKS (dispatch stays
  // closed while the obligation stands); the store drives the resume, and while
  // it is in flight nothing reaches the wire.
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  await threadsStore.getState().send(ref, "text");
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  const parked = await storage.listOutbox(ref);
  expect(parked.map((record) => record.method)).toContain("turn/start");
  // Only turn/start is carved out: every other fenced verb still refuses with
  // its own message on the new face.
  await expect(threadsStore.getState().steer(ref, "steer text")).rejects.toThrow(
    "Steer isn't available until this session is resumed",
  );
  await expect(threadsStore.getState().queue(ref, "queue text")).rejects.toThrow(
    "Queue isn't available until this session is resumed",
  );
});

// RoboRev Medium: the dispatch gate exempted EVERY queued mutation once the
// session matched resumeOnlyModel, but the hub's carve-out admits only
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
  expect(resumeOnlyModel(ref)).toBe(false);
  expect(resumeOnlyDispatchable(ref)).toBe(true);
  // R09: a NEW send minted behind the queued non-send row is still not the fold,
  // but it is the Send-resumes face: admitted and parked while the store drives
  // the resume. Before R09 this admission refused it outright.
  expect(sendResumesModel(ref)).toBe(true);
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  await threadsStore.getState().send(ref, "another");
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
}); // Part 2's predicate: a queued, not-yet-attempted NON-turn/start row parks at
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
  expect(resumeOnlyModel(ref)).toBe(true);

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
  expect(resumeOnlyModel(ref)).toBe(true);

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
  expect(resumeOnlyModel(ref)).toBe(false);
});

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

for (const status of ["active", "idle"]) {
  test(`saved ${status} delegate snapshots do not release uncertain mutations`, async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "delegate-uncertain" });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let snapshot = readResponse("ref_a", { status: { type: "restartRequired" } });
    fake.on("thread/read", (params) => ({ ...snapshot, requestGeneration: params.requestGeneration }));
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
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
    faultEnabled = false;
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(false);
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
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
    faultEnabled = false;
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => !threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a"));
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(false);
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
    fake.on("thread/read", (params) => ({ ...snapshot, requestGeneration: params.requestGeneration }));
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
    fake.on("thread/read", (params) => ({ ...snapshot, requestGeneration: params.requestGeneration }));
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

// The durable write cannot be made: every enqueue attempt rejects with the
// adapter's watchdog error, the wedge shape the retry ladder retries before
// the send fallback answers it.
function timeOutDurableEnqueues(storage: MutationOutboxIndexedDB): void {
  storage.enqueueIntent = async () => {
    throw new MutationStorageTimeoutError();
  };
}

// Parks the ref's reconcile read forever: its listOutbox never settles, so
// the ref sits in pendingMutationReconciliations until the test ends. The
// store registers the pending entry before awaiting the read, so the probe
// turning true means the fence is already visible to currentDispatchClient.
function parkRefReconcileRead(storage: MutationOutboxIndexedDB, ref: string) {
  let readOpened = false;
  const listOutbox = storage.listOutbox.bind(storage);
  vi.spyOn(storage, "listOutbox").mockImplementation((parkedRef?: string) => {
    if (parkedRef === ref) {
      readOpened = true;
      return new Promise<never>(() => {});
    }
    return listOutbox(parkedRef);
  });
  return {
    opened: () => readOpened,
  };
}

// A storage failure on the outbox's read path: while armed, every per-ref
// listOutbox read rejects - by default with the adapter's watchdog error, the
// same shape a never-answering IndexedDB open produces. The bound real read
// is returned so a test can still inspect rows while the fault is armed.
function wedgeOutboxReads(
  storage: MutationOutboxIndexedDB,
  storageError: () => unknown = () => new MutationStorageTimeoutError(),
) {
  let armed = false;
  const readOutbox = storage.listOutbox.bind(storage);
  vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
    if (armed && ref) throw storageError();
    return readOutbox(ref);
  });
  return {
    arm: () => {
      armed = true;
    },
    lift: () => {
      armed = false;
    },
    readOutbox,
  };
}

// A full origin is not a wedge: IndexedDB refuses the write at once with
// QuotaExceededError. The outbox is local persistence only, so the send still
// goes out as a plain RPC, exactly as it does through a wedge; the refusal is
// final, so the ladder does not retry it. Nothing durable pins the ref.
test("a send whose durable write the browser refuses for quota goes out directly and pins nothing", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => ({
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    receipt: mutationReceipt(params.clientMutationId),
  }));
  const unsubscribed = nextHandledRequest(fake, "thread/unsubscribe", () => ({}));
  const add = vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(() => {
    throw new DOMException("storage full", "QuotaExceededError");
  });
  await threadsStore.getState().send("ref_a", "sent from a full origin");
  expect(add).toHaveBeenCalledTimes(1);
  const sent = fake.calls.filter((call) => call.method === "turn/start");
  expect(sent, "the send from a full origin never reached the daemon").toHaveLength(1);
  expect((sent[0]?.params as { clientMutationId?: string } | undefined)?.clientMutationId).toBeTruthy();
  expect(await storage.listOutbox()).toEqual([]);
  threadsStore.getState().releaseThread("ref_a");
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  await unsubscribed;
});

// Behind an undelivered durable send the fallback refuses rather than jump
// it, and only that earlier durable row keeps the ref pinned.
test("a refused write behind an undelivered durable send fails and preserves only the durable pin", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  await threadsStore.getState().send("ref_a", "already saved");
  vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(() => {
    throw new DOMException("storage full", "QuotaExceededError");
  });
  await expect(threadsStore.getState().send("ref_a", "not saved")).rejects.toThrow("storage full");
  expect(await storage.listOutbox()).toHaveLength(1);
  threadsStore.getState().releaseThread("ref_a");
  expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
});

// A reconciliation that storage unavailability fails is not a mutation-state
// fact: the same wedge that fences the durable write also fails the reconcile
// read. Recording it as a genuine failure would fence the direct-dispatch
// fallback and strand every send behind a page reload, so a storage-unavailable
// reconcile records the ref as storage-blocked - the slice the send fallback
// may admit - while every other failure keeps its old, fencing meaning. A
// retired connection fails the read at once instead of timing out, and is
// just as much a storage failure.
test.each([
  { failure: "a storage timeout", storageError: () => new MutationStorageTimeoutError() },
  { failure: "a retired connection", storageError: () => new DOMException("closing", "InvalidStateError") },
  { failure: "an aborted transaction", storageError: () => new DOMException("aborted", "AbortError") },
])("a reconciliation that $failure fails records the ref as storage-blocked, not failed", async ({ storageError }) => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "storage-blocked-reconcile" });
  await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  const wedge = wedgeOutboxReads(storage, storageError);
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await settleCallerContinuations();
  wedge.arm();
  await threadsStore
    .getState()
    .refreshThread("ref_a")
    .catch(() => undefined);
  expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
  expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(false);
});

// A genuine reconcile failure must survive a later storage-timeout reconcile:
// the timeout adds the storage-blocked record but does not supersede the real
// failure, so the failures fence - and the fallback refusal it carries - hold
// through the wedge until a successful reconcile clears both.
test("a storage-timeout reconcile does not erase a genuine reconciliation failure", async () => {
  const storage = new MutationOutboxIndexedDB();
  const wedge = wedgeOutboxReads(storage);
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await ensureActiveMutationTarget(fake, "ref_a");
  fake.on("turn/start", (params) => ({
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    receipt: mutationReceipt(params.clientMutationId),
  }));

  // The genuine failure: a blocked shared record or a real reconcile conflict.
  threadsStore.setState({ mutationReconciliationFailures: new Set(["ref_a"]) });
  wedge.arm();
  await threadsStore
    .getState()
    .refreshThread("ref_a")
    .catch(() => undefined);
  expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
  expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(true);

  timeOutDurableEnqueues(storage);
  await expect(threadsStore.getState().send("ref_a", "sent while both fences hold")).rejects.toBeInstanceOf(
    MutationStorageTimeoutError,
  );
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// The storage-blocked fence must self-heal, not stick: discovery retries the
// ref on every pass, and once storage answers the reconcile succeeds, the
// slice clears, and the durable dispatcher resumes the ref's queued row - so a
// transient wedge costs a wedge-long dispatch pause, not a reload. The
// sentinel row parks behind the restart fence while the wedge is armed, so
// the healthy hydrate can never race its dispatch out first.
test("periodic discovery recovers a storage-blocked reconciliation after storage returns", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "storage-blocked-recovery" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    const wedge = wedgeOutboxReads(storage);
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    // Hydrate behind the restart fence: the healthy reconcile runs, but the
    // sentinel row stays parked, so no healthy window can dispatch it before
    // the wedge lands.
    await threadsStore.getState().ensureThread("ref_a");
    await settleCallerContinuations();
    wedge.arm();
    status = "idle";
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
    // The sentinel row is still parked: nothing left while the reconcile was
    // wedged.
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);

    wedge.lift();
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(false);
    expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(false);
  } finally {
    vi.useRealTimers();
  }
});

// The union clause: a storage-blocked ref with NO durable row is invisible to
// every row-driven discovery trigger, so only the storage-blocked slice's own
// membership in the discovery pass can retry its reconcile. Without that
// clause the slice would stick until a manual refresh or a reload.
test("periodic discovery retries a storage-blocked ref that has no durable row", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB();
    const wedge = wedgeOutboxReads(storage);
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await settleCallerContinuations();
    wedge.arm();
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);

    wedge.lift();
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => !threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a"));
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(false);
  } finally {
    vi.useRealTimers();
  }
});

// The record-level gate: a storage-blocked ref cannot answer the ordering
// questions a Retry press asks, so the press refuses and the canceled row
// stays put until discovery's retry reconciles. The press is otherwise
// admissible here - idle, authoritative, nothing pending - so the refusal
// isolates the storage-blocked clause.
test("a storage-blocked ref refuses a queued record's Retry press", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-storage-blocked-retry";
  const seeder = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const record = await seeder.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  await seeder.cancelUnattempted("ref_a");
  seeder.close();

  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const wedge = wedgeOutboxReads(storage);
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await settleCallerContinuations();

  wedge.arm();
  await threadsStore
    .getState()
    .refreshThread("ref_a")
    .catch(() => undefined);
  expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);

  expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
  // The row is untouched: still canceled, and nothing reached the daemon.
  expect((await wedge.readOutbox("ref_a")).map((row) => row.state)).toEqual(["canceled"]);
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
});

// Reviewer B finding 3: the recovery-resend branch (a failed turn's Retry,
// QueueStrip's retry) bypasses the send funnel and would otherwise park on the
// Send-resumes face with no driver. The resend now runs resumeFencedForSend
// after its durable write, exactly as the send funnel does.
test("a recovery resend on a Send-resumes ref parks and drives the resume", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const ref = "local:resend-fenced";
  fake.on("thread/read", () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    }),
  );
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "retry me" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "retry me" }] },
  });
  const recovery = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
  expect(recovery).toBeDefined();
  if (!recovery) throw new Error("expected the recovery row to be durable");
  await refreshPendingTurnsProjection(ref);
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  await resendRecoveryMutation(recovery.clientMutationId, ref, "send", "retry me", []);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// The drive is idempotent per ref: a second send landing while the first
// resume is in flight parks its row and rides the first drive's reconciliation
// rather than starting a second thread/resume (which AppwireClient would reject
// anyway). Both rows stay parked while the obligation stands.
test("a second send while the Send-resumes drive is in flight parks and rides the first resume", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const ref = "local:drive-idempotent";
  fake.on("thread/read", () =>
    readResponse(ref, {
      status: { type: "notLoaded" },
      evener: {
        ref,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    }),
  );
  await threadsStore.getState().ensureThread(ref);
  await refreshPendingTurnsProjection(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  fake.on("thread/resume", () => new Promise<never>(() => {}));
  await Promise.all([threadsStore.getState().send(ref, "one"), threadsStore.getState().send(ref, "two")]);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  const parked = await storage.listOutbox(ref);
  expect(parked.filter((record) => record.method === "turn/start")).toHaveLength(2);
});

// The parked rows keep their FIFO order when the resume drains them: the ref's
// sequence allocations are re-used in the order they were made, so the first
// pressed message is the first turn/start on the wire.
test("the parked Send-resumes rows drain in FIFO order after the resume", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const ref = "local:fifo-rows";
  let resumed = false;
  const fenced = () =>
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
  const idle = () =>
    readResponse(ref, {
      status: { type: "idle" },
      evener: { ref, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  fake.on("thread/read", () => (resumed ? idle() : fenced()));
  let releaseResume!: () => void;
  const heldResume = new Promise<ReturnType<typeof idle>>((resolve) => {
    releaseResume = () => {
      resumed = true;
      resolve(idle());
    };
  });
  fake.on("thread/resume", () => heldResume);
  const texts: string[] = [];
  fake.on("turn/start", (params) => {
    const input = params.input as Array<{ text?: string }> | undefined;
    texts.push(input?.[0]?.text ?? "");
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(ref);
  await threadsStore.getState().send(ref, "one");
  await threadsStore.getState().send(ref, "two");
  await flushIndexedDBUntil(() => false);
  expect(texts).toEqual([]);
  releaseResume();
  await flushIndexedDBUntil(() => texts.length >= 2);
  expect(texts).toEqual(["one", "two"]);
});

// The admission carve-out is turn/start for the Send-resumes face only. A
// restartRequired daemon still refuses: Send cannot start it at all, so the
// pressed row must not park.
test("a restartRequired local ref still refuses the Send-resumes admission", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  const ref = "local:restart-required";
  fake.on("thread/read", () =>
    readResponse(ref, {
      status: { type: "restartRequired" },
      evener: {
        ref,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        queue: { revision: 0 },
      },
    }),
  );
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  await expect(threadsStore.getState().send(ref, "text")).rejects.toThrow(
    "Send isn't available until this session is resumed",
  );
  expect(await storage.listOutbox(ref)).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// The parked Send-resumes row is drained by whichever dispatch trigger releases
// last: the resume path's own refresh tail, the outbox's resync scan, or a ready
// discovery. Every trigger re-reads the outbox and re-arms the ref, so the row
// must reach the wire exactly once: one turn/start total, no matter how many
// triggers fire around it, and nothing once it is gone.
test("the parked Send-resumes row drains exactly once across the resume tail, a resync, and a ready discovery", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const ref = "local:exactly-once-tails";
  let resumed = false;
  const fenced = () =>
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
  const idle = () =>
    readResponse(ref, {
      status: { type: "idle" },
      evener: { ref, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  fake.on("thread/read", () => (resumed ? idle() : fenced()));
  // The resume is held so the parked window is the one under test.
  let releaseResume!: () => void;
  const heldResume = new Promise<ReturnType<typeof idle>>((resolve) => {
    releaseResume = () => {
      resumed = true;
      resolve(idle());
    };
  });
  fake.on("thread/resume", () => heldResume);
  let sends = 0;
  fake.on("turn/start", (params) => {
    sends += 1;
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  await threadsStore.getState().send(ref, "hello");
  // The row parks while the resume is held: nothing has dispatched.
  await flushIndexedDBUntil(() => false);
  expect(sends).toBe(0);
  // Two more triggers fire while the row is parked: neither may send it.
  fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
  notifyReadyForMutationDispatch([ref]);
  await flushIndexedDBUntil(() => false);
  expect(sends).toBe(0);
  // Release the resume: its refresh tail reconciles the ref and dispatches once.
  releaseResume();
  await flushIndexedDBUntil(() => sends >= 1);
  expect(sends).toBe(1);
  // Fire both other triggers again: the row is gone, so neither dispatches a
  // second send.
  fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
  notifyReadyForMutationDispatch([ref]);
  await flushIndexedDBUntil(() => false);
  expect(sends).toBe(1);
});

// A send enqueued WHILE an identity-changing resume is in flight rides the same
// in-flight drive (one resumeThread), so it parks under the superseded ref after
// the drive's first snapshot. The final re-target pass must move it too, or it
// strands on the dead ref forever.
test("a send enqueued during a held identity-changing resume follows the resumed ref", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:late-from";
  const toRef = "local:late-to";
  const fromShape = () =>
    readResponse(fromRef, {
      status: { type: "notLoaded" },
      evener: {
        ref: fromRef,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  const toShape = () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  // Hold the resumed ref's hydration read, so a second send can land while the
  // drive is between its first snapshot and its final pass.
  let holdToRead = false;
  let toReadHeld = false;
  let releaseToRead!: () => void;
  fake.on("thread/read", (params) => {
    if (params.ref !== toRef) return fromShape();
    if (holdToRead) {
      holdToRead = false;
      toReadHeld = true;
      return new Promise<ReturnType<typeof toShape>>((resolve) => {
        releaseToRead = () => resolve(toShape());
      });
    }
    return toShape();
  });
  let resolveResume!: (response: ReturnType<typeof toShape>) => void;
  const heldResume = new Promise<ReturnType<typeof toShape>>((resolve) => {
    resolveResume = resolve;
  });
  fake.on("thread/resume", () => heldResume);
  const starts: string[] = [];
  fake.on("turn/start", (params) => {
    starts.push(String(params.ref));
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  await threadsStore.getState().ensureThread(toRef);
  expect(threadsStore.getState().restartBlockingObligations.has(fromRef)).toBe(true);
  // First send parks and starts the drive, which is held at the resume RPC.
  await threadsStore.getState().send(fromRef, "one");
  await flushIndexedDBUntil(() => false);
  expect(starts).toEqual([]);
  // Release the resume but hold the resumed ref's hydration read, so the drive
  // is in flight when the second send lands.
  holdToRead = true;
  resolveResume(toShape());
  await flushIndexedDBUntil(() => toReadHeld);
  // The first row moved in the drive's first snapshot and drains under the new
  // ref while the resumed hydration is held.
  await flushIndexedDBUntil(() => starts.length >= 1);
  expect(starts).toEqual([toRef]);
  await threadsStore.getState().send(fromRef, "two");
  await flushIndexedDBUntil(() => false);
  expect(starts).toEqual([toRef]);
  // Release the hydration: the final pass moves the late row to the new ref and
  // both drain there.
  releaseToRead();
  await flushIndexedDBUntil(() => starts.length >= 2);
  expect(starts).toEqual([toRef, toRef]);
  expect(await storage.listOutbox(fromRef)).toEqual([]);
});

// Two concurrent resume triggers (the notice's Resume press racing a send) share
// the ONE in-flight drive, so there is exactly one thread/resume and both
// callers await the same hydration and publication.
test("two concurrent resumeSession drives share one thread/resume", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const ref = "local:concurrent-resume";
  let resolved = false;
  const shape = () =>
    readResponse(ref, {
      status: { type: resolved ? "idle" : "notLoaded" },
      evener: {
        ref,
        capabilities: resolved ? CAPABILITIES : { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: resolved,
        resumeRequired: !resolved,
        queue: { revision: resolved ? 1 : 0 },
      },
    });
  fake.on("thread/read", () => shape());
  let releaseResume!: () => void;
  const heldResume = new Promise<ReturnType<typeof shape>>((resolve) => {
    releaseResume = () => {
      resolved = true;
      resolve(shape());
    };
  });
  fake.on("thread/resume", () => heldResume);
  fake.emitReady();
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  const first = threadsStore.getState().resumeSession(ref);
  const second = threadsStore.getState().resumeSession(ref);
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "thread/resume"));
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  releaseResume();
  await Promise.all([first, second]);
  // Both callers resolved on the same one drive.
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
});

// An identity change with ONLY an optimistic row to move (its outbox row already
// settled to accepted) must still run the per-ref bookkeeping: the resumed ref
// is pinned and notified, and its projection shows the row. The any-moved
// condition is what decides this, so a check that only counted outbox rows would
// leave the optimistic row showing under the dead ref.
test("an optimistic-only identity change still pins, notifies, and projects under the resumed ref", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:optimistic-from";
  const toRef = "local:optimistic-to";
  fake.on("thread/read", (params) =>
    params.ref === toRef
      ? readResponse(toRef, {
          status: { type: "idle" },
          evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
        })
      : readResponse(fromRef, {
          status: { type: "notLoaded" },
          evener: {
            ref: fromRef,
            capabilities: { ...CAPABILITIES, send: false },
            mutationStateAuthoritative: false,
            resumeRequired: true,
            queue: { revision: 0 },
          },
        }),
  );
  fake.on("thread/resume", () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    }),
  );
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  expect(threadsStore.getState().restartBlockingObligations.has(fromRef)).toBe(true);
  // Seed an accepted-but-unreflected row: settleReceipt moves it out of the
  // outbox into the optimistic store, so the retarget finds ONLY optimistic
  // rows of the old ref.
  const seeded = await storage.enqueueIntent({
    targetRef: fromRef,
    threadId: `thr_${fromRef}`,
    instanceId: "instance-old",
    method: "turn/queue",
    payload: { ref: fromRef, expectedInstanceId: "instance-old", input: [{ type: "text", text: "accepted" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "accepted" }] },
  });
  await storage.settleReceipt(seeded.clientMutationId, "pending");
  expect(await storage.listOutbox(fromRef)).toEqual([]);
  expect(await storage.listOptimistic(fromRef)).toHaveLength(1);
  const persisted: string[] = [];
  const unsubscribe = subscribeMutationPersistence((refs) => persisted.push(...refs));
  try {
    await threadsStore.getState().resumeSession(fromRef);
  } finally {
    unsubscribe();
  }
  expect(await storage.listOptimistic(fromRef)).toEqual([]);
  expect((await storage.listOptimistic(toRef)).map((record) => record.clientMutationId)).toEqual([
    seeded.clientMutationId,
  ]);
  expect(persisted).toContain(toRef);
  await refreshPendingTurnsProjection(toRef);
  expect(pendingTurnEntries(toRef)).toHaveLength(1);
  expect(pendingTurnEntries(fromRef)).toHaveLength(0);
});

// The superseded ref's projection still holds the rows retargetOutbox moved out
// of its storage, so the drive must refresh BOTH projections: the destination's
// shows the rows, and the old ref's re-reads and clears to the honest state.
test("an identity-changing drive clears the superseded ref's projection", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:projection-from";
  const toRef = "local:projection-to";
  fake.on("thread/read", (params) =>
    params.ref === toRef
      ? readResponse(toRef, {
          status: { type: "idle" },
          evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
        })
      : readResponse(fromRef, {
          status: { type: "notLoaded" },
          evener: {
            ref: fromRef,
            capabilities: { ...CAPABILITIES, send: false },
            mutationStateAuthoritative: false,
            resumeRequired: true,
            queue: { revision: 0 },
          },
        }),
  );
  fake.on("thread/resume", () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    }),
  );
  // The moved row stays in flight under the destination, so its projection keeps
  // showing it while we assert the old ref's projection cleared.
  fake.on("turn/start", () => new Promise<never>(() => {}));
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  expect(threadsStore.getState().restartBlockingObligations.has(fromRef)).toBe(true);
  await storage.enqueueIntent({
    targetRef: fromRef,
    threadId: `thr_${fromRef}`,
    instanceId: "instance-old",
    method: "turn/start",
    payload: { ref: fromRef, expectedInstanceId: "instance-old", input: [{ type: "text", text: "parked" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "parked" }] },
  });
  await refreshPendingTurnsProjection(fromRef);
  expect(pendingTurnEntries(fromRef)).toHaveLength(1);
  // The drive's persistence notification must name BOTH refs: the destination so
  // its projection shows the moved row, and the superseded ref so its projection
  // re-reads and drops the rows retargetOutbox took out of its storage.
  const persisted: string[] = [];
  const unsubscribe = subscribeMutationPersistence((refs) => persisted.push(...refs));
  try {
    await threadsStore.getState().resumeSession(fromRef);
  } finally {
    unsubscribe();
  }
  expect(persisted).toContain(fromRef);
  expect(persisted).toContain(toRef);
  await flushPendingTurnsProjectionForTests();
  expect(pendingTurnEntries(fromRef)).toHaveLength(0);
  expect(pendingTurnEntries(toRef)).toHaveLength(1);
});

// The drain's in-line cap is not a silent exit: a row that arrives after each
// real pass (up to the cap) is still moved by the scheduled follow-up pass, and
// every moved row dispatches under the resumed ref.
test("a row arriving after the drain cap is still moved by the follow-up pass", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:drain-from";
  const toRef = "local:drain-to";
  fake.on("thread/read", (params) =>
    params.ref === toRef
      ? readResponse(toRef, {
          status: { type: "idle" },
          evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
        })
      : readResponse(fromRef, {
          status: { type: "notLoaded" },
          evener: {
            ref: fromRef,
            capabilities: { ...CAPABILITIES, send: false },
            mutationStateAuthoritative: false,
            resumeRequired: true,
            queue: { revision: 0 },
          },
        }),
  );
  fake.on("thread/resume", () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    }),
  );
  const starts: string[] = [];
  fake.on("turn/start", (params) => {
    starts.push(String(params.ref));
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  expect(threadsStore.getState().restartBlockingObligations.has(fromRef)).toBe(true);
  const seedRow = (text: string) =>
    storage.enqueueIntent({
      targetRef: fromRef,
      threadId: `thr_${fromRef}`,
      instanceId: "instance-old",
      method: "turn/start",
      payload: { ref: fromRef, expectedInstanceId: "instance-old", input: [{ type: "text", text }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
    });
  await seedRow("seed");
  // Stage one more arrival AFTER each real pass, so the third pass still moves a
  // row and the in-line cap is reached with the work unfinished.
  let staged = 0;
  const realRetarget = MutationOutboxIndexedDB.prototype.retargetOutbox;
  const spy = vi.spyOn(MutationOutboxIndexedDB.prototype, "retargetOutbox").mockImplementation(async function (
    this: MutationOutboxIndexedDB,
    from: string,
    to: string,
    identity: { threadId?: string; instanceId?: string },
  ) {
    const moved = await realRetarget.call(this, from, to, identity);
    if (staged < 4) {
      staged += 1;
      await seedRow(`late ${staged}`);
    }
    return moved;
  });
  try {
    await threadsStore.getState().resumeSession(fromRef);
    await flushIndexedDBUntil(() => starts.length >= 5);
    expect(staged).toBe(4);
    expect(starts).toEqual([toRef, toRef, toRef, toRef, toRef]);
    expect(await storage.listOutbox(fromRef)).toEqual([]);
  } finally {
    spy.mockRestore();
  }
});

// A row committed inside the CONVERGENCE pass's own read window - the pass that
// moves nothing and ends the inline loop - must still be moved by the trailing
// pass, which is now scheduled unconditionally. The probe also pins the ordering
// the trailing pass relies on: the inline passes run before the drive publishes
// the resumed identity, and the trailing pass runs after that publication.
test("a row committed after the inline loop's empty pass is moved by the trailing pass", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:converge-from";
  const toRef = "local:converge-to";
  fake.on("thread/read", (params) =>
    params.ref === toRef
      ? readResponse(toRef, {
          status: { type: "idle" },
          evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
        })
      : readResponse(fromRef, {
          status: { type: "notLoaded" },
          evener: {
            ref: fromRef,
            capabilities: { ...CAPABILITIES, send: false },
            mutationStateAuthoritative: false,
            resumeRequired: true,
            queue: { revision: 0 },
          },
        }),
  );
  fake.on("thread/resume", () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    }),
  );
  const starts: string[] = [];
  fake.on("turn/start", (params) => {
    starts.push(String(params.ref));
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  expect(threadsStore.getState().restartBlockingObligations.has(fromRef)).toBe(true);
  const seedRow = (text: string) =>
    storage.enqueueIntent({
      targetRef: fromRef,
      threadId: `thr_${fromRef}`,
      instanceId: "instance-old",
      method: "turn/start",
      payload: { ref: fromRef, expectedInstanceId: "instance-old", input: [{ type: "text", text }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
    });
  await seedRow("seed");
  // Stage after the first two real passes, then stop, so the next pass finds
  // nothing (the convergence pass); stage the straggler right after that empty
  // pass, inside its own read window.
  let realCalls = 0;
  let stagedAfterEmpty = false;
  const publishedAtPass: boolean[] = [];
  const realRetarget = MutationOutboxIndexedDB.prototype.retargetOutbox;
  const spy = vi.spyOn(MutationOutboxIndexedDB.prototype, "retargetOutbox").mockImplementation(async function (
    this: MutationOutboxIndexedDB,
    from: string,
    to: string,
    identity: { threadId?: string; instanceId?: string },
  ) {
    publishedAtPass.push(threadsStore.getState().resumedIdentities.has(fromRef));
    const moved = await realRetarget.call(this, from, to, identity);
    realCalls += 1;
    if (realCalls <= 2) {
      await seedRow(`late ${realCalls}`);
    } else if (moved.outbox.length === 0 && !stagedAfterEmpty) {
      stagedAfterEmpty = true;
      await seedRow("straggler");
    }
    return moved;
  });
  try {
    await threadsStore.getState().resumeSession(fromRef);
    await flushIndexedDBUntil(() => starts.length >= 4);
    expect(stagedAfterEmpty).toBe(true);
    expect(starts).toEqual([toRef, toRef, toRef, toRef]);
    expect(await storage.listOutbox(fromRef)).toEqual([]);
    // The inline passes (the first four calls) ran before the publication; the
    // trailing pass saw it.
    expect(publishedAtPass.slice(0, 4)).toEqual([false, false, false, false]);
    expect(publishedAtPass[4]).toBe(true);
  } finally {
    spy.mockRestore();
  }
});

// A Stop that lands after the drive's retarget but before the identity fence:
// the Stop's own durable cancel names the OLD ref, where the row no longer is,
// so without the swap registration (the Stop site's second cancel) AND the
// drive's own compensation the UI says canceled while the moved row still
// delivers under the resumed ref.
test("a Stop after the retarget cancels the moved row and never sends it", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:stop-swap-from";
  const toRef = "local:stop-swap-to";
  const fromShape = () =>
    readResponse(fromRef, {
      status: { type: "notLoaded" },
      evener: {
        ref: fromRef,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  const toFenced = () =>
    readResponse(toRef, {
      status: { type: "notLoaded" },
      evener: {
        ref: toRef,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  const toIdle = () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  // The first toRef read (ensureThread's) arms its restart obligation, so the
  // moved row's dispatch parks until the held hydration below clears it.
  let toReads = 0;
  let holdToRead = false;
  let toReadHeld = false;
  let releaseToRead!: () => void;
  fake.on("thread/read", (params) => {
    if (params.ref !== toRef) return fromShape();
    toReads += 1;
    if (toReads === 1) return toFenced();
    if (holdToRead) {
      holdToRead = false;
      toReadHeld = true;
      return new Promise<ReturnType<typeof toIdle>>((resolve) => {
        releaseToRead = () => resolve(toIdle());
      });
    }
    return toIdle();
  });
  fake.on("thread/resume", () => toIdle());
  fake.on("thread/shutdown", () => ({}));
  const starts: string[] = [];
  fake.on("turn/start", (params) => {
    starts.push(String(params.ref));
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  await threadsStore.getState().ensureThread(toRef);
  expect(threadsStore.getState().restartBlockingObligations.has(fromRef)).toBe(true);
  const seeded = await storage.enqueueIntent({
    targetRef: fromRef,
    threadId: `thr_${fromRef}`,
    instanceId: "instance-old",
    method: "turn/start",
    payload: { ref: fromRef, expectedInstanceId: "instance-old", input: [{ type: "text", text: "parked" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "parked" }] },
  });
  holdToRead = true;
  const drive = threadsStore.getState().resumeSession(fromRef);
  await flushIndexedDBUntil(() => toReadHeld);
  // The retarget already moved the row to toRef; the Stop lands before the fence
  // runs, so only the swap registration can reach it.
  await threadsStore.getState().shutdown(fromRef);
  await flushIndexedDBUntil(() => false);
  releaseToRead();
  await drive;
  await flushIndexedDBUntil(() => false);
  expect(starts).toEqual([]);
  const outbox = await storage.listOutbox(toRef);
  expect(outbox.map((record) => ({ id: record.clientMutationId, state: record.state }))).toEqual([
    { id: seeded.clientMutationId, state: "canceled" },
  ]);
  expect(await storage.listOutbox(fromRef)).toEqual([]);
});

// The non-stop asymmetry: a transport/hydration failure after the retarget must
// NOT cancel the moved rows. They stay preserved and unattempted under the
// resumed identity, to deliver when it recovers; canceling them there would lose
// the user's message. Pinned so the failure lands while the moved row is still
// unattempted - the only state a durable cancel could reach.
test("a non-stop failure after the retarget preserves the moved rows", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:preserve-from";
  const toRef = "local:preserve-to";
  const fromShape = () =>
    readResponse(fromRef, {
      status: { type: "notLoaded" },
      evener: {
        ref: fromRef,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  const toFenced = () =>
    readResponse(toRef, {
      status: { type: "notLoaded" },
      evener: {
        ref: toRef,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  const toIdle = () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  // The first toRef read (ensureThread's) arms its restart obligation, so the
  // moved row's dispatch parks under the resumed ref. The held hydration below is
  // the read that would clear it, and it fails - so the row never dispatches.
  let toReads = 0;
  let holdToRead = false;
  let toReadHeld = false;
  let rejectToRead!: (error: Error) => void;
  fake.on("thread/read", (params) => {
    if (params.ref !== toRef) return fromShape();
    toReads += 1;
    if (toReads === 1) return toFenced();
    if (holdToRead) {
      holdToRead = false;
      toReadHeld = true;
      return new Promise<ReturnType<typeof toIdle>>((_resolve, reject) => {
        rejectToRead = reject;
      });
    }
    return toIdle();
  });
  fake.on("thread/resume", () => toIdle());
  const starts: string[] = [];
  fake.on("turn/start", (params) => {
    starts.push(String(params.ref));
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  await threadsStore.getState().ensureThread(toRef);
  expect(threadsStore.getState().restartBlockingObligations.has(fromRef)).toBe(true);
  const seeded = await storage.enqueueIntent({
    targetRef: fromRef,
    threadId: `thr_${fromRef}`,
    instanceId: "instance-old",
    method: "turn/start",
    payload: { ref: fromRef, expectedInstanceId: "instance-old", input: [{ type: "text", text: "keep me" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "keep me" }] },
  });
  holdToRead = true;
  const drive = threadsStore.getState().resumeSession(fromRef);
  await flushIndexedDBUntil(() => toReadHeld);
  rejectToRead(new Error("hydrate failed"));
  await drive;
  await flushIndexedDBUntil(() => false);
  // Not canceled and not sent: the preserved row waits, unattempted, under the
  // resumed ref.
  expect(starts).toEqual([]);
  const outbox = await storage.listOutbox(toRef);
  expect(
    outbox.map((record) => ({ id: record.clientMutationId, state: record.state, attempted: record.attempted })),
  ).toEqual([{ id: seeded.clientMutationId, state: "submitting", attempted: false }]);
});

// The swap registration's OWN reach, past the drive: the straggler is staged
// after the inline loop's empty pass, so only the trailing drain - which runs
// once the drive has settled and no fence or catch remains - moves it. A Stop
// during that pass must still cancel the moved row: the trailing drain's
// registration is the only thing that can reach it.
test("a Stop during the trailing drain cancels the row the drain moved", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const fromRef = "local:drain-stop-from";
  const toRef = "local:drain-stop-to";
  const fromShape = () =>
    readResponse(fromRef, {
      status: { type: "notLoaded" },
      evener: {
        ref: fromRef,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
    });
  const toIdle = () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  fake.on("thread/read", (params) => (params.ref === toRef ? toIdle() : fromShape()));
  fake.on("thread/resume", () => toIdle());
  fake.on("thread/shutdown", () => ({}));
  const starts: string[] = [];
  fake.on("turn/start", (params) => {
    starts.push(String(params.ref));
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  const seedRow = (text: string) =>
    storage.enqueueIntent({
      targetRef: fromRef,
      threadId: `thr_${fromRef}`,
      instanceId: "instance-old",
      method: "turn/start",
      payload: { ref: fromRef, expectedInstanceId: "instance-old", input: [{ type: "text", text }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
    });
  // No row for the drive's first retarget or its inline loop: the loop's first
  // pass moves nothing and ends it, so the straggler staged right after that
  // empty pass is moved by the trailing drain alone - after the drive has
  // published the resumed identity and returned.
  let realCalls = 0;
  let stagedAfterEmpty = false;
  let stopped = false;
  let stragglerId = "";
  const realRetarget = MutationOutboxIndexedDB.prototype.retargetOutbox;
  const spy = vi.spyOn(MutationOutboxIndexedDB.prototype, "retargetOutbox").mockImplementation(async function (
    this: MutationOutboxIndexedDB,
    from: string,
    to: string,
    identity: { threadId?: string; instanceId?: string },
  ) {
    const published = threadsStore.getState().resumedIdentities.has(fromRef);
    const moved = await realRetarget.call(this, from, to, identity);
    realCalls += 1;
    if (published && moved.outbox.length > 0 && !stopped) {
      // The drive has settled; nothing but the swap registration can cancel the
      // row this pass just moved.
      stopped = true;
      await threadsStore.getState().shutdown(fromRef);
    } else if (realCalls === 2 && moved.outbox.length === 0 && !stagedAfterEmpty) {
      stagedAfterEmpty = true;
      stragglerId = (await seedRow("straggler")).clientMutationId;
    }
    return moved;
  });
  try {
    await threadsStore.getState().resumeSession(fromRef);
    await flushIndexedDBUntil(() => stopped);
    await flushIndexedDBUntil(() => false);
    expect(stopped).toBe(true);
    expect(starts).toEqual([]);
    const outbox = await storage.listOutbox(toRef);
    expect(outbox.map((record) => ({ id: record.clientMutationId, state: record.state }))).toEqual([
      { id: stragglerId, state: "canceled" },
    ]);
    expect(await storage.listOutbox(fromRef)).toEqual([]);
  } finally {
    spy.mockRestore();
  }
});

// The shared stage for the four resume-swap Stop tests below: a resume that
// returns a different ref moves the row parked under the old ref, the resumed
// ref stays fenced so the moved row sits unattempted under it, the drive settles
// and its trailing drain converges, and then a Stop lands on the old ref while
// the pane still shows it. The caller supplies how that Stop runs, and whether
// the pane's identity-follow effect (clearResumedIdentity) fires before it or
// inside the Stop's own cancellation await.
async function stageResumeSwapStop(options: {
  fromRef: string;
  toRef: string;
  stop: (ref: string) => Promise<void>;
  followBeforeStop?: boolean;
  followInsideStop?: boolean;
}): Promise<{ starts: string[]; followed: () => boolean; seededId: string; storage: MutationOutboxIndexedDB }> {
  const { fromRef, toRef } = options;
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const toIdle = () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  let toFenced = true;
  fake.on("thread/read", (params) =>
    params.ref === toRef ? (toFenced ? resumeOnlySnapshot(toRef) : toIdle()) : resumeOnlySnapshot(fromRef),
  );
  fake.on("thread/resume", () => resumeOnlySnapshot(toRef));
  fake.on("thread/shutdown", () => ({}));
  fake.on("evener/thread/forceStop", () => ({}));
  const starts: string[] = [];
  fake.on("turn/start", (params) => {
    starts.push(String(params.ref));
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(fromRef);
  await threadsStore.getState().ensureThread(toRef);
  const seeded = await storage.enqueueIntent({
    targetRef: fromRef,
    threadId: `thr_${fromRef}`,
    instanceId: "instance-old",
    method: "turn/start",
    payload: { ref: fromRef, expectedInstanceId: "instance-old", input: [{ type: "text", text: "parked" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "parked" }] },
  });
  // Run the drive to completion: it moves the row to the resumed ref, publishes
  // the resumed identity, and its trailing drain converges. No rows remain under
  // the old ref, so the swap registration is the only thing that still holds the
  // moved row for the old ref's Stop.
  await threadsStore.getState().resumeSession(fromRef);
  await flushIndexedDBUntil(() => false);
  expect(threadsStore.getState().resumedIdentities.has(fromRef)).toBe(true);
  expect(await storage.listOutbox(fromRef)).toEqual([]);
  if (options.followBeforeStop) threadsStore.getState().clearResumedIdentity(fromRef);
  let followed = false;
  const realCancel = MutationOutboxIndexedDB.prototype.cancelUnattempted;
  const spy = options.followInsideStop
    ? vi.spyOn(MutationOutboxIndexedDB.prototype, "cancelUnattempted").mockImplementation(async function (
        this: MutationOutboxIndexedDB,
        target: string,
      ) {
        if (target === fromRef && !followed) {
          followed = true;
          threadsStore.getState().clearResumedIdentity(fromRef);
        }
        return realCancel.call(this, target);
      })
    : undefined;
  try {
    await options.stop(fromRef);
  } finally {
    spy?.mockRestore();
  }
  // The resumed ref clears its own recovery obligation: a Stop that reached the
  // moved row keeps it from dispatching.
  toFenced = false;
  await threadsStore.getState().refreshThread(toRef);
  notifyReadyForMutationDispatch([toRef]);
  await flushIndexedDBUntil(() => starts.length > 0, 10);
  return { starts, followed: () => followed, seededId: seeded.clientMutationId, storage };
}

// The swap registration must survive the drain's CONVERGENCE. An empty pass
// proves only that no rows remain parked under the OLD ref - not that the rows
// the drive already moved to the resumed ref have been delivered, and not that
// the pane has followed the identity (that is a later React commit). With the
// resumed ref itself fenced, the moved row sits unattempted under it. A Stop
// pressed on the still-displayed old ref after the drain converged but before
// the pane's identity-follow effect must still reach the moved row, or the
// message dispatches when the resumed ref clears despite the Stop - the window
// issue #3947 records.
test("a Stop after the drain converges still cancels the moved row before the pane follows the identity", async () => {
  const { starts, seededId, storage } = await stageResumeSwapStop({
    fromRef: "local:converge-stop-from",
    toRef: "local:converge-stop-to",
    stop: (ref) => threadsStore.getState().shutdown(ref),
  });
  expect(starts).toEqual([]);
  const outbox = await storage.listOutbox("local:converge-stop-to");
  expect(outbox.map((record) => ({ id: record.clientMutationId, state: record.state }))).toEqual([
    { id: seededId, state: "canceled" },
  ]);
});

// The other side of the same contract: once the pane HAS followed the identity
// (clearResumedIdentity), the user is on the resumed ref and a Stop there names
// it directly. The old ref's registration must release then - otherwise it
// outlives the swap and keeps canceling rows that are no longer the old ref's.
test("a Stop on the old ref after the pane follows the identity does not cancel the moved row", async () => {
  const { starts, storage } = await stageResumeSwapStop({
    fromRef: "local:followed-stop-from",
    toRef: "local:followed-stop-to",
    stop: (ref) => threadsStore.getState().shutdown(ref),
    followBeforeStop: true,
  });
  expect(starts).toEqual(["local:followed-stop-to"]);
  const outbox = await storage.listOutbox("local:followed-stop-to");
  expect(outbox.map((record) => record.state)).not.toContain("canceled");
});

// The Stop's own cancellation write is an await, and the pane's follow effect
// can run inside it. A Stop pressed while the pane still showed the old ref must
// still reach the rows the drive moved, even when the follow lands in that gap -
// so the target is captured at the press, before the write.
test("a Stop whose cancellation awaits while the pane follows the identity still cancels the moved row", async () => {
  const { starts, followed, seededId, storage } = await stageResumeSwapStop({
    fromRef: "local:straddle-from",
    toRef: "local:straddle-to",
    stop: (ref) => threadsStore.getState().shutdown(ref),
    followInsideStop: true,
  });
  expect(followed()).toBe(true);
  expect(starts).toEqual([]);
  const outbox = await storage.listOutbox("local:straddle-to");
  expect(outbox.map((record) => ({ id: record.clientMutationId, state: record.state }))).toEqual([
    { id: seededId, state: "canceled" },
  ]);
});

// forceStop carries the same straddle: its own cancellation write is an await,
// and the follow can land inside it. The synchronous capture must cover this
// call site too, not only shutdown.
test("a forceStop whose cancellation awaits while the pane follows the identity still cancels the moved row", async () => {
  const { starts, followed, seededId, storage } = await stageResumeSwapStop({
    fromRef: "local:force-straddle-from",
    toRef: "local:force-straddle-to",
    stop: (ref) => threadsStore.getState().forceStop(ref),
    followInsideStop: true,
  });
  expect(followed()).toBe(true);
  expect(starts).toEqual([]);
  const outbox = await storage.listOutbox("local:force-straddle-to");
  expect(outbox.map((record) => ({ id: record.clientMutationId, state: record.state }))).toEqual([
    { id: seededId, state: "canceled" },
  ]);
});

// The registration carries a per-drive token: two drives can target the SAME
// (old ref -> resumed ref) pair. inFlightResumes drops the first drive when its
// promise resolves while its trailing drain still awaits storage, so a second
// press starts a second drive that registers the same pair and moves a row. The
// first drive's empty pass must NOT settle the second drive's registration, or a
// subsequent Stop's sweep finds nothing and the moved row survives.
test("a stale drain pass does not clear a later drive's swap registration", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const ref = "local:token-from";
  const toRef = "local:token-to";
  const fenced = (target: string) =>
    readResponse(target, {
      status: { type: "notLoaded" },
      evener: {
        ref: target,
        capabilities: { ...CAPABILITIES, send: false },
        mutationStateAuthoritative: false,
        resumeRequired: true,
        queue: { revision: 0 },
      },
      turns: [],
    });
  const toIdle = () =>
    readResponse(toRef, {
      status: { type: "idle" },
      evener: { ref: toRef, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  fake.on("thread/read", (params) => (params.ref === toRef ? fenced(toRef) : fenced(ref)));
  fake.on("thread/resume", () => toIdle());
  fake.on("thread/shutdown", () => ({}));
  const starts: string[] = [];
  fake.on("turn/start", (params) => {
    starts.push(String(params.ref));
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(ref);
  // Hold each drive's TRAILING drain pass. The first hold keeps drive 1's
  // registration from settling while drive 2 registers the same pair; both
  // drives only reach the trailing pass after they publish.
  let realCalls = 0;
  let firstHeld = false;
  let secondHeld = false;
  let releaseFirst!: () => void;
  let releaseSecond!: () => void;
  const firstGate = new Promise<void>((resolve) => {
    releaseFirst = resolve;
  });
  const secondGate = new Promise<void>((resolve) => {
    releaseSecond = resolve;
  });
  const realRetarget = MutationOutboxIndexedDB.prototype.retargetOutbox;
  const spy = vi.spyOn(MutationOutboxIndexedDB.prototype, "retargetOutbox").mockImplementation(async function (
    this: MutationOutboxIndexedDB,
    from: string,
    to: string,
    identity: { threadId?: string; instanceId?: string },
  ) {
    realCalls += 1;
    if (realCalls === 3) {
      firstHeld = true;
      await firstGate;
    } else if (realCalls === 6) {
      secondHeld = true;
      await secondGate;
    }
    return realRetarget.call(this, from, to, identity);
  });
  try {
    await threadsStore.getState().resumeSession(ref);
    await flushIndexedDBUntil(() => firstHeld);
    const late = await storage.enqueueIntent({
      targetRef: ref,
      threadId: `thr_${ref}`,
      instanceId: "instance-old",
      method: "turn/start",
      payload: { ref, expectedInstanceId: "instance-old", input: [{ type: "text", text: "late" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "late" }] },
    });
    await threadsStore.getState().resumeSession(ref);
    await flushIndexedDBUntil(() => secondHeld);
    // Both drains are held and drive 2 has moved its row onto the resumed ref.
    const parked = await storage.listOutbox(toRef);
    expect(parked.some((record) => record.clientMutationId === late.clientMutationId)).toBe(true);
    // Release drive 1's empty pass: its settle must leave drive 2's registration
    // (same pair) in place.
    releaseFirst();
    await flushIndexedDBUntil(() => false);
    await threadsStore.getState().shutdown(ref);
    await flushIndexedDBUntil(() => false);
    expect(starts).toEqual([]);
    const outbox = await storage.listOutbox(toRef);
    expect(outbox.find((record) => record.clientMutationId === late.clientMutationId)?.state).toBe("canceled");
  } finally {
    releaseFirst();
    releaseSecond();
    spy.mockRestore();
  }
});

// The harder order of the same property: the resume's OWN post-resume hydration
// is held open, and the resync tail and a ready discovery each run first. Either
// may send the parked row; releasing the held refresh afterwards must not send a
// second. One turn/start total, whatever order the three triggers release in.
test("the parked Send-resumes row dispatches exactly once when the resync and ready tails release before the resume's own refresh", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const ref = "local:exactly-once-held-refresh";
  let resumed = false;
  let holdRefreshRead = false;
  let refreshReadHeld = false;
  let releaseRefreshRead!: () => void;
  const fenced = () =>
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
  const idle = () =>
    readResponse(ref, {
      status: { type: "idle" },
      evener: { ref, capabilities: CAPABILITIES, mutationStateAuthoritative: true, queue: { revision: 1 } },
    });
  fake.on("thread/read", () => {
    if (holdRefreshRead) {
      holdRefreshRead = false;
      refreshReadHeld = true;
      return new Promise<ReturnType<typeof idle>>((resolve) => {
        releaseRefreshRead = () => resolve(resumed ? idle() : fenced());
      });
    }
    return resumed ? idle() : fenced();
  });
  fake.on("thread/resume", () => {
    resumed = true;
    // The resume's own refresh hydration is the read the driver issues next;
    // hold exactly that one.
    holdRefreshRead = true;
    return idle();
  });
  let sends = 0;
  fake.on("turn/start", (params) => {
    sends += 1;
    return {
      receipt: mutationReceipt(params.clientMutationId),
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    };
  });
  fake.emitReady();
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  await threadsStore.getState().send(ref, "hello");
  await flushIndexedDBUntil(() => refreshReadHeld);
  // The parked row cannot dispatch while the resume's refresh is held.
  expect(sends).toBe(0);
  // The other two tails release first; each may clear the obligation and send.
  fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
  notifyReadyForMutationDispatch([ref]);
  await flushIndexedDBUntil(() => sends >= 1);
  expect(sends).toBe(1);
  // Release the held refresh: its reconciliation must not send a second.
  releaseRefreshRead();
  await flushIndexedDBUntil(() => false);
  expect(sends).toBe(1);
});

// The fixture's refusal message: one constant shared by every fake throw
// and the passthrough assertion, so no assertion matches a duplicated
// string (copy is not a test oracle).
const HUB_REFUSAL_MESSAGE = "the hub's inbound queue is busy and did not accept this message";

// #4342: the hub's high-water backpressure refusal is transient. The row
// stays queued in the outbox FIFO (never recovery), the pending surface
// shows the hub did not accept it yet — the busy marker holds until the row
// settles and a fresh refusal re-marks it at the next pace step — and the store paces
// retries through the injectable backoff scheduler, one arm per refusal,
// doubling while the hub keeps refusing. A retry the hub accepts settles
// the row and leaves nothing marked.
test("a backpressured send stays queued, surfaces busy, and paces retries that redeliver", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  let refuse = true;
  fake.on("turn/start", (params) => {
    if (refuse) {
      throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
        evenerErrorInfo: "connectionBackpressure",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
        retryDisposition: "automatic",
        cause: "queueSaturated",
      });
    }
    return {
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    };
  });
  const scheduled: Array<{ attempt: number; retry: () => void }> = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((attempt, retry) => {
    scheduled.push({ attempt, retry });
    return () => {};
  });

  await threadsStore.getState().send("ref_a", "held by backpressure");
  // The rollback lands one storage read after the wire call (the refusal
  // path's pre-rollback state read), so wait for the marker the
  // assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [refused] = await storage.listOutbox("ref_a");
  if (!refused) throw new Error("expected the refused row to stay durable");
  expect(refused.state).toBe("submitting");
  expect(refused.attempted).toBe(false);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(refused.clientMutationId);
  expect(scheduled.map((entry) => entry.attempt)).toEqual([1]);

  // Firing the retry while the hub still refuses: the marker holds through
  // the attempt in flight, and the refusal that answers it re-marks at a
  // doubled pace.
  const firstRetry = scheduled[0];
  if (!firstRetry) throw new Error("expected the first paced retry to be armed");
  firstRetry.retry();
  await flushIndexedDBUntil(
    () => fake.calls.filter((call) => call.method === "turn/start").length === 2 && scheduled.length === 2,
  );
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(refused.clientMutationId);
  expect(scheduled.map((entry) => entry.attempt)).toEqual([1, 2]);

  // A retry the hub accepts settles the row and clears the marker.
  refuse = false;
  const secondRetry = scheduled[1];
  if (!secondRetry) throw new Error("expected the doubled paced retry to be armed");
  secondRetry.retry();
  await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/start").length === 3);
  await flushIndexedDBUntil(async () => (await storage.listOutbox("ref_a")).length === 0);
  expect(await storage.listOutbox("ref_a")).toEqual([]);
  expect(threadsStore.getState().transientRefusalPaces.size).toBe(0);
  restoreScheduler();
});

// #4342 (round-9 review): the refusal's own storage notification refreshes
// the ref's projection, and that refresh's newly-loaded hand-off must not
// wake the dispatcher - the notification fires once per refusal, so an
// unfiltered hand-off redelivers the refused row immediately and each
// refusal repeats the wake in a tight loop, hammering the saturated
// connection and perpetually replacing the pace timer. The paced retry and
// the ordinary discovery drive own the re-drive: with the scheduler withheld
// and nothing else driving, a fully drained runtime leaves exactly one
// request on the wire.
test("a refusal's storage notification does not itself redispatch the refused row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().send("ref_a", "refused under saturation");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));

  // Drain every storage and projection async the refusal kicked off: the
  // rollback's notification, the commit feed's projection refresh, the
  // marker prune. Each round is bounded so a busy loop cannot hang the
  // drain itself; any quiet gap lets one settle through.
  for (let round = 0; round < 3; round++) {
    await Promise.race([settleProjectionWorkForTests(), new Promise((resolve) => setTimeout(resolve, 100))]);
  }

  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt).toBe(1);
  restoreScheduler();
});

// #4342 (round-12 review): a projection refresh from any source - the
// discovery cadence, a commit, the reopened row's own notification-driven
// refresh - completing while a refused row's paced-retry timer is live
// must not hand readiness to the dispatcher: that timer owns the re-drive,
// and its fire schedules the same dispatch the hand-off would. The gate
// marker, so it cannot outlive what backs it.
test("a projection refresh during a live paced retry does not redispatch the refused row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().send("ref_a", "refused, paced, and refreshed");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  await flushIndexedDBUntil(() => threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt).toBe(1);

  // A projection read from outside the refusal's own path - the same read
  // the discovery cadence and every commit run - while the pace is live
  // (the withheld scheduler keeps the timer armed, as an in-window read
  // would find it). The read completes, the projection is true, and its
  // readiness hand-off must still hand nothing to the dispatcher.
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  expect((await storage.listOutbox("ref_a"))[0]?.state).toBe("submitting");
  restoreScheduler();
});

// #4342 (round-11 review): a refusal that rolls back to residual evidence -
// the two-tab case the count exists for: another tab's concurrent attempt
// marked the row while this attempt was in flight - keeps the row submitting
// under its uncertain treatment: no busy marker, no paced timer, the
// ordinary discovery cadence as its only re-drive. That refusal's
// notification must not redispatch the row either: the rollback changed
// only attempt evidence, which no queue projection carries, so a projection
// refresh would read an identical queue and hand readiness straight back
// to the dispatcher - the same tight loop the clean refusal ran before
// round 10.
test("a refusal rolling back to residual evidence does not redispatch the row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  let firstRefused = false;
  const inFlight = deferred<TurnStartResponse>();
  fake.on("turn/start", (params) => {
    if (!firstRefused) return inFlight.promise;
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().send("ref_a", "refused with a concurrent attempt aboard");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the in-flight attempt's row to stay durable");

  // The other tab's concurrent attempt, committed while this attempt holds
  // the wire: the refusal rolls back only its own, leaving the row with
  // residual evidence, no busy marker, and no timer - the uncertain
  // treatment.
  await expect(storage.markAttempted(row.clientMutationId)).resolves.toBe(true);
  firstRefused = true;
  inFlight.reject(
    new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: row.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    }),
  );
  await flushIndexedDBUntil(async () => (await storage.listOutbox("ref_a"))[0]?.unresolvedAttempts === 1);
  const [residual] = await storage.listOutbox("ref_a");
  if (!residual) throw new Error("expected the refused row to stay durable");
  expect(residual.state).toBe("submitting");
  expect(residual.unresolvedAttempts).toBe(1);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")).toBeUndefined();

  // The same bounded drain as the clean-refusal test: nothing may redispatch
  // the row before the ordinary discovery cadence gets its turn.
  for (let round = 0; round < 3; round++) {
    await Promise.race([settleProjectionWorkForTests(), new Promise((resolve) => setTimeout(resolve, 100))]);
  }

  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  restoreScheduler();
});

// #4342: the dispatcher's attempted-marker rollback is what keeps a
// refused row out of the browser's own reconnect reconcile park — the
// non-authoritative first read marks submitting+attempted rows
// blockedUnknown, and a parked row stops dispatching. A refused row
// (its marker cleared by the refusal itself) survives the scan still
// submitting, and the ordinary ready drive redelivers it.
test("a backpressured send survives the non-authoritative reconcile park and redelivers", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  let snapshot = readResponse("ref_a");
  fake.on("thread/read", () => snapshot);
  let refuse = true;
  fake.on("turn/start", (params) => {
    if (!refuse) {
      return {
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        receipt: mutationReceipt(params.clientMutationId),
      };
    }
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const paced: Array<() => void> = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((_attempt, retry) => {
    paced.push(retry);
    return () => {};
  });

  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().send("ref_a", "held across a reconnect");
  // The rollback lands one storage read after the wire call, so wait for
  // the marker the assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [refused] = await storage.listOutbox("ref_a");
  if (!refused) throw new Error("expected the refused row to stay durable");
  expect(refused.state).toBe("submitting");

  // A reconnect whose first read is not mutation-authoritative runs the
  // reconcile's onlyAttempted park over the ref's submitting rows.
  snapshot = readResponse("ref_a");
  Object.assign(snapshot.thread.evener, { mutationStateAuthoritative: false });
  await threadsStore.getState().refreshThread("ref_a");
  const [survived] = await storage.listOutbox("ref_a");
  if (!survived) throw new Error("expected the refused row to survive the reconcile");
  expect(survived.clientMutationId).toBe(refused.clientMutationId);
  expect(survived.state).toBe("submitting");

  // The ordinary ready drive redelivers the row; the hub refusing again
  // proves the redelivery reached the wire while the row never parked.
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/start").length === 2);
  await flushIndexedDBUntil(() => threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 2);
  const [redelivered] = await storage.listOutbox("ref_a");
  if (!redelivered) throw new Error("expected the refused row to stay durable through redelivery");
  expect(redelivered.state).toBe("submitting");
  expect(redelivered.clientMutationId).toBe(refused.clientMutationId);

  // Once the hub accepts, the refusal's paced retry redelivers it - the
  // redrive the spec gives a refused row. (A direct drive made here would
  // join the second refusal's still-running dispatch and die with its
  // stop; the pace is what recovers it.)
  refuse = false;
  paced.at(-1)?.();
  await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/start").length === 3);
  await flushIndexedDBUntil(async () => (await storage.listOutbox("ref_a")).length === 0);
  expect(await storage.listOutbox("ref_a")).toEqual([]);
  restoreScheduler();
});

// #4342: the direct-send fallback (C13) has no durable row, so a
// backpressure refusal there can neither pace nor surface busy: it
// rejects the send with the hub's own message through the existing
// send-failure path, nothing is queued, and no retry is armed. The user
// re-presses Send; the spec claims no paced retry for this path.
test("a backpressure refusal on the direct-send path rejects with the hub's message and pins nothing", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const scheduled: number[] = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((attempt) => {
    scheduled.push(attempt);
    return () => {};
  });
  // Force the direct path the same way the quota test does: the durable
  // write refuses at once, so the send goes out as a plain RPC.
  const add = vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(() => {
    throw new DOMException("storage full", "QuotaExceededError");
  });

  await expect(threadsStore.getState().send("ref_a", "sent from a full origin")).rejects.toThrow(HUB_REFUSAL_MESSAGE);
  expect(add).toHaveBeenCalledTimes(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  expect(await storage.listOutbox()).toEqual([]);
  expect(threadsStore.getState().transientRefusalPaces.size).toBe(0);
  expect(scheduled).toEqual([]);
  restoreScheduler();
});

// #4342 (implementation review): a fresh refusal for a ref replaces the
// pending paced retry's timer rather than stacking a second one — the
// outbox's 2s discovery cadence re-drives a refused row on its own, and a
// live timer per refusal would multiply dispatches past the pace the
// backoff promises.
test("a fresh refusal replaces the pending paced retry's timer", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const attempts: number[] = [];
  const cancels: number[] = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((attempt) => {
    const index = attempts.length;
    attempts.push(attempt);
    return () => cancels.push(index);
  });

  await threadsStore.getState().send("ref_a", "held one");
  await flushIndexedDBUntil(() => attempts.length === 1);
  // Let the refused dispatch's settle chain drain before re-driving: the
  // dispatcher coalesces a notify onto the still-settling run and its stop
  // outcome discards the requested run, so a notify that races the settle
  // would be lost. IndexedDB turns yield to the microtasks that finish the
  // settle, so two turns bound the drain.
  await flushIndexedDBUntil(() => false, 2);
  // A discovery-driven re-attempt before the first retry fires refuses
  // again; the new arm must replace (cancel) the first.
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => attempts.length === 2);
  expect(attempts).toEqual([1, 2]);
  expect(cancels).toEqual([0]);
  restoreScheduler();
});

// #4342 (implementation review): the busy marker's contract is settlement —
// it clears when the refused record settles (a receipt, a reclassification),
// not when the paced attempt fires. An in-flight attempt is still the hub's
// refusal being worked: the row must keep reading as busy until its retry is
// actually accepted.
test("the busy marker clears when the record settles, not when the paced attempt fires", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  let refuse = true;
  const acceptedRetry = deferred<TurnStartResponse>();
  fake.on("turn/start", (params) => {
    if (refuse) {
      throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
        evenerErrorInfo: "connectionBackpressure",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
        retryDisposition: "automatic",
        cause: "queueSaturated",
      });
    }
    return acceptedRetry.promise;
  });
  const retries: Array<() => void> = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((_attempt, retry) => {
    retries.push(retry);
    return () => {};
  });

  await threadsStore.getState().send("ref_a", "held until accepted");
  // The rollback lands one storage read after the wire call, so wait for
  // the marker the assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the refused row to stay durable");
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // The paced attempt goes out and parks on the hub's answer: the marker
  // must survive the attempt in flight.
  refuse = false;
  expect(retries.length).toBe(1);
  const firstRetry = retries[0];
  if (!firstRetry) throw new Error("expected the paced retry to be armed");
  firstRetry();
  await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/start").length === 2);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // The accepted retry settles the row; settlement is what clears the marker.
  acceptedRetry.resolve({
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    receipt: mutationReceipt(row.clientMutationId),
  });
  await flushIndexedDBUntil(
    () => threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId !== row.clientMutationId,
  );
  await flushIndexedDBUntil(async () => (await storage.listOutbox("ref_a")).length === 0);
  expect(await storage.listOutbox("ref_a")).toEqual([]);
  restoreScheduler();
});

// #4342 (implementation review): the store's test reset cancels every armed
// paced retry along with the pace map — clearing the map alone would leave
// each arm's timer live against the next test's fresh runtime.
test("resetting the store cancels pending paced retries", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  let arms = 0;
  const cancels: number[] = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => {
    const index = arms;
    arms += 1;
    return () => cancels.push(index);
  });

  await threadsStore.getState().send("ref_a", "held across a reset");
  await flushIndexedDBUntil(() => arms === 1);
  resetThreadsStoreForTests();
  expect(arms).toBe(1);
  expect(cancels).toEqual([0]);
  restoreScheduler();
});

// #4342 (review): the marker's settle prune also runs when the row's
// settlement happened in ANOTHER tab - this tab's dispatcher never sees
// that settle, but every dispatch tail re-reads the shared storage, and the
// prune rides it.
test("a refusal's busy marker leaves when another tab settles the row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().send("ref_a", "held across tabs");
  // The rollback lands one storage read after the wire call, so wait for
  // the marker the assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the refused row to stay durable");
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // The other tab settles the row straight into the optimistic store, through
  // its own adapter instance over the shared database: this tab's dispatcher
  // and storage hooks never see the write, and the suite's broadcast channel
  // is a stub, so the dispatch tail's prune is the one path that can clear
  // the marker.
  const otherTab = new MutationOutboxIndexedDB();
  await otherTab.settleReceipt(row.clientMutationId, "reflected");
  otherTab.close();
  // Any dispatch pass's pin refresh re-reads the ref; the prune rides it.
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => threadsStore.getState().transientRefusalPaces.get("ref_a") === undefined);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")).toBeUndefined();
  restoreScheduler();
});

// #4342 (round-4 review): a Stop whose cancel scan skips the in-flight
// attempted row must not be outrun by the refusal that answers that attempt.
// The hub proved the attempt never landed, but the row holds canceled -
// released only by an explicit Retry - instead of returning dispatchable
// ahead of the interrupt the Stop already queued.
test("a refusal after a mid-attempt Stop holds the row canceled instead of redelivering", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  const inFlight = deferred<TurnStartResponse>();
  fake.on("turn/start", () => inFlight.promise);
  fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  const paced: number[] = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((attempt) => {
    paced.push(attempt);
    return () => {};
  });

  await threadsStore.getState().send("ref_a", "held past a stop");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the in-flight row to stay durable");
  expect(row.attempted).toBe(true);

  // The user stops while the attempt is in flight: the cancel scan skips
  // the attempted row, and the interrupt goes out.
  await threadsStore.getState().interrupt("ref_a");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));

  // The hub's refusal lands after the Stop: the rollback holds the row
  // canceled, arms no paced retry, and leaves no busy marker.
  inFlight.reject(
    new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: row.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    }),
  );
  await flushIndexedDBUntil(async () => (await storage.getOutbox(row.clientMutationId))?.state === "canceled");
  expect((await storage.getOutbox(row.clientMutationId))?.state).toBe("canceled");
  expect(paced).toEqual([]);
  expect(threadsStore.getState().transientRefusalPaces.size).toBe(0);

  // Discovery cannot outrun the Stop either: the canceled row never re-sends.
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => false, 4);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  restoreScheduler();
});

// #4342 (review): the settle prune is fire-and-forget at every call site,
// so a storage read failing inside it must not escape as an unhandled
// rejection - and a failed read proves nothing, so it clears nothing: the
// marker and its armed retry survive for a later recovery pass. The
// unhandled-rejection claim itself is pinned by this file run's exit, not
// an in-test listener (vitest intercepts process-level unhandledRejection
// dispatch, so a per-test listener cannot discriminate; see the
// transcript-scroll precedent) - before the fix, this suite exits 1 naming
// the stubbed storage failure.
test("a refusal's busy marker survives a prune whose storage read fails", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().send("ref_a", "held through a failed prune");
  // The rollback lands one storage read after the wire call, so wait for
  // the marker the assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the refused row to stay durable");
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // The dispatch-tail prune's shared-storage read fails for the marked row:
  // the dispatcher drain's own read of the same row comes first and its
  // failure is handled by the dispatch chain, so the failure is armed for
  // the second read - the tail prune's.
  let failedReads = 0;
  let markerReads = 0;
  const originalRead = storage.getOutbox.bind(storage);
  const read = vi.spyOn(storage, "getOutbox").mockImplementation((clientMutationId: string) => {
    if (clientMutationId === row.clientMutationId) {
      markerReads += 1;
      if (markerReads > 1) {
        failedReads += 1;
        return Promise.reject(new MutationStorageTimeoutError());
      }
    }
    return originalRead(clientMutationId);
  });
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => failedReads > 0, 30);
  read.mockRestore();
  expect(failedReads).toBeGreaterThan(0);
  // The failed prune proved nothing: the marker and its retry survive.
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // Once storage answers again, the same dispatch-tail path still prunes a
  // row that settled meanwhile - the recovery pass the marker was kept for.
  const otherTab = new MutationOutboxIndexedDB();
  await otherTab.settleReceipt(row.clientMutationId, "reflected");
  otherTab.close();
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => threadsStore.getState().transientRefusalPaces.get("ref_a") === undefined);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")).toBeUndefined();
  restoreScheduler();
});
