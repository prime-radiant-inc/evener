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
  WireError,
} from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { type IDBDatabase, IDBFactory, IDBKeyRange, IDBVersionChangeEvent } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { recoveryComposerDraft } from "../panes/session/composer/recovery/recoveryDraft";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { connectionStore } from "./connection";
import { editHumanNote, syncHumanNote, useHumanNoteDraft } from "./humanNoteDrafts";
import { MutationDispatcher } from "./mutationDispatcher";
import type { MutationOutboxRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdIndexedDBEvent, holdNextWriteTransaction, neverSettlingRequest } from "./testing/stalledIndexedDB";
import {
  ConflictError,
  discardRecoveryMutation,
  installHydrationRetrySchedulerForTests,
  notifyReadyForMutationDispatch,
  readMutationPersistence,
  resendRecoveryMutation,
  resetThreadsStoreForTests,
  resumeStopBaseline,
  retryBlockedMutation,
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
        await waitFor(() => expect(result.current?.submitted?.id).toBe(submitted.clientMutationId));
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

async function flushIndexedDBUntil(done: () => boolean, maxTurns = 30): Promise<void> {
  const probe = new MutationOutboxIndexedDB();
  for (let turn = 0; turn < maxTurns && !done(); turn += 1) await probe.listTargetRefs();
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
  // A capture that instead fails with MutationStorageTimeoutError - storage
  // not answering - no longer aborts: it takes the direct-dispatch fallback
  // (pinned by "a send whose storage never answers still reaches the daemon").
  // This test keeps the abort-and-disarm contract for every OTHER rejection.
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
      await expect(threadsStore.getState().queue("ref_a", "past a failed capture")).rejects.toThrow(
        "Mutation outbox upgrade is blocked",
      );
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
        // And nothing reached the wire for the ref's dead row either.
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
