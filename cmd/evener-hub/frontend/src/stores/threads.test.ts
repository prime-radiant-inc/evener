import "fake-indexeddb/auto";
import type {
  AnyNotification,
  ConnectionState,
  MethodName,
  MethodTypes,
  ModelListResponse,
  NotesHumanSetResponse,
  QueueState,
  Thread,
  ThreadCapabilities,
  ThreadClearResponse,
  ThreadModel,
  ThreadReadResponse,
  ThreadStatus,
  ThreadTurnsListResponse,
  TurnQueueResponse,
  TurnStartResponse,
} from "@evener/appwire-client";
import {
  applyNotification,
  ClientNotReadyError,
  errorKind,
  hydrateThread,
  notificationTargetsThread,
  RequestTimeoutError,
  WireError,
} from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { mulberry32 } from "@evener/appwire-client/testing/tokenFlood";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { recoveryComposerDraft } from "../panes/session/composer/recovery/recoveryDraft";
import {
  resetSubagentModuleStoreForTests,
  turnScopeKey,
  upsertSubagentRow,
  useSubagentRow,
} from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { connectionStore, useConnectionStore } from "./connection";
import { editHumanNote, syncHumanNote, useHumanNoteDraft } from "./humanNoteDrafts";
import { MutationDispatcher } from "./mutationDispatcher";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";
import {
  appendFrameTime,
  ConflictError,
  FRAME_TIMES_MAX_ENTRIES,
  FRAME_TIMES_WINDOW_MS,
  installHydrationRetrySchedulerForTests,
  putThreadModel,
  readMutationPersistence,
  resendRecoveryMutation,
  resetThreadsStoreForTests,
  resumeStopBaseline,
  resumeStopFence,
  retryBlockedMutation,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadRoutingIndexesForTests,
  threadsStore,
  useThreadsStore,
} from "./threads";

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
// callback, by contrast, is specified to run only after the microtask
// checkpoint has drained completely, including microtasks queued by other
// microtasks. So this holds however many turns that path grows.
//
// Its one boundary: it does not cover a future change that parks the caller
// behind a task or I/O of its own (an IndexedDB read on the rejection path,
// say). That would need its own awaited condition, and the mutation proof in
// this task's report - which fires each owner-wait arm and requires the
// matching test to fail - is what would catch it.
function settleCallerContinuations(): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
}

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
    method: "item/completed" as const,
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      turnId: "turn_1",
      item: {
        type: "commandExecution" as const,
        id: "item_1",
        turnId: "turn_1",
        output: "done",
        status: "completed" as const,
      },
    },
  };
  const turnCompleted = {
    method: "turn/completed" as const,
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      turn: { id: "turn_1", status: "completed", itemsView: "" },
    },
  };
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
});

afterEach(async () => {
  cleanup();
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  vi.restoreAllMocks();
  // The beforeEach above only resets threadsStore BEFORE each test. Many
  // tests here call ensureThread()/watchThread() directly (not through a
  // mounted pane's own unmount lifecycle), so nothing ever calls
  // releaseThread/releaseWatchedThread for them - without this, the LAST
  // test's tracked/pinned refs stay refcounted after this file finishes, and
  // under isolate:false every later file's own connectionStore.connect()
  // re-triggers rewireClient, which re-issues a stray thread/read against
  // whatever client that later file just connected.
  resetThreadsStoreForTests();
  resetSubagentModuleStoreForTests();
  // The beforeEach above only clears the GLOBAL "evener-mutation-outbox"
  // IndexedDB database (installed once, for the worker's life, by this
  // file's own `import "fake-indexeddb/auto"") before EACH of THIS file's
  // own tests - it never runs again after the LAST test. A test here that
  // exercises the real default getMutationRuntime() path (no
  // setMutationStorageForTests override) writes into that same global
  // database, and under isolate:false it stays there for whichever file
  // runs next in this worker, resurfacing as a stray pinned/discovered
  // mutation ref the moment that later file's own code calls
  // getMutationRuntime() and rediscovers the leftover record.
  await deleteMutationDatabase();
});

describe("FakeClient", () => {
  test("request() rejects while not ready, mirroring AppwireClient's own ready-gate, without recording the call", async () => {
    const fake = new FakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a"));

    await expect(fake.request("thread/read", { ref: "ref_a", includeTurns: true })).rejects.toThrow(
      /cannot call "thread\/read" while state is "connecting"/,
    );
    expect(fake.calls).toHaveLength(0); // never "sent" — the real client never reaches socket.send() in this case either
  });
});

describe("useConnectionStore", () => {
  // Counts BOTH halves of the listener lifecycle. A live-listener count alone
  // cannot tell "connect returned early" apart from "connect detached and
  // re-attached" — both leave exactly one — so the idempotency check needs the
  // registration and unsubscribe tallies to mean anything.
  class CountingClient extends FakeClient {
    registrations = 0;
    unsubscribes = 0;
    override onStateChange(cb: (s: ConnectionState) => void): () => void {
      this.registrations++;
      const detach = super.onStateChange(cb);
      return () => {
        this.unsubscribes++;
        detach();
      };
    }
  }

  test("connect captures the client's current state immediately", () => {
    const fake = new FakeClient("connecting");
    connectionStore.getState().connect(fake);
    expect(connectionStore.getState().state).toBe("connecting");
  });

  test("connect mirrors every subsequent client state change", () => {
    const fake = connectFakeClient("idle");
    fake.emitStateChange("connecting");
    expect(connectionStore.getState().state).toBe("connecting");
    fake.emitStateChange("ready");
    expect(connectionStore.getState().state).toBe("ready");
    fake.emitStateChange("reconnecting");
    expect(connectionStore.getState().state).toBe("reconnecting");
  });

  test("connect is idempotent: a second call with the same client does not double-subscribe", () => {
    const fake = new CountingClient("idle");
    connectionStore.getState().connect(fake);
    connectionStore.getState().connect(fake); // second call, same client instance

    // Assert the early return itself. Counting live listeners (or setState
    // calls) cannot: once connect() detaches the previous listener before
    // wiring a replacement, dropping the early return still leaves exactly
    // one listener, and the assertion passes against the broken code.
    expect(fake.registrations).toBe(1);
    expect(fake.unsubscribes).toBe(0);

    const setStateSpy = vi.spyOn(connectionStore, "setState");
    fake.emitStateChange("connecting");
    expect(setStateSpy).toHaveBeenCalledTimes(1); // one onStateChange listener, not two
  });

  // AppwireClientLike DOES expose connect() (it resolves with the
  // InitializeResponse - see protocol/testing/fakeClient.ts) - this store's
  // own connect(client) just never calls it: it only mirrors
  // ConnectionState, so it stays safe to call before any handshake has even
  // started. AppShell.tsx is the caller that actually drives client.connect()
  // and sets serverInfo directly from its resolved value.
  test("serverInfo stays undefined: connect(client) only mirrors ConnectionState, it never calls the client's own connect()", () => {
    connectFakeClient();
    expect(connectionStore.getState().serverInfo).toBeUndefined();
  });

  test("hook reflects store state and updates on change", () => {
    const fake = connectFakeClient("idle");
    const { result } = renderHook(() => useConnectionStore((s) => s.state));
    expect(result.current).toBe("idle");

    act(() => {
      fake.emitStateChange("ready");
    });
    expect(result.current).toBe("ready");
  });

  test("a replaced client's later state cannot overwrite the current client", () => {
    const a = connectFakeClient("ready");
    const b = new FakeClient("ready");
    connectionStore.getState().connect(b);

    a.emitStateChange("closed");

    expect(connectionStore.getState().client).toBe(b);
    expect(connectionStore.getState().state).toBe("ready");

    // The fence must not over-fire: the client that replaced a still owns the
    // store and its own transitions have to land.
    b.emitStateChange("reconnecting");
    expect(connectionStore.getState().state).toBe("reconnecting");
  });

  // The detach half, proven on its own. The identity check below would keep
  // the store correct even without this, so nothing else here fails when the
  // unsubscribe is dropped — and a replaced client would then keep a live
  // subscription for the rest of the page's life.
  test("wiring a replacement invokes the outgoing client's unsubscribe", () => {
    const outgoing = new CountingClient("ready");
    connectionStore.getState().connect(outgoing);
    expect(outgoing.unsubscribes).toBe(0);

    connectionStore.getState().connect(new FakeClient("ready"));
    expect(outgoing.unsubscribes).toBe(1);
  });

  // A subscriber can call connect() from inside the synchronous setState
  // dispatch of an outer connect(). The inner frame completes first and owns
  // the module slot; if the outer frame then overwrote it, the inner client's
  // subscription would never be detachable again.
  test("a connect re-entered during publication keeps the inner client's unsubscribe", () => {
    const outer = new CountingClient("ready");
    const inner = new CountingClient("ready");

    const stopWatching = connectionStore.subscribe(() => {
      if (connectionStore.getState().client === outer) {
        connectionStore.getState().connect(inner);
      }
    });
    connectionStore.getState().connect(outer);
    stopWatching();

    expect(connectionStore.getState().client).toBe(inner);
    // The outer frame must retire its own listener rather than clobber the
    // slot, so replacing inner later still detaches inner.
    connectionStore.getState().connect(new FakeClient("ready"));
    expect(inner.unsubscribes).toBe(1);
  });

  // The real client transitions synchronously inside connect()/close(), so a
  // subscriber that drives it during publication must not have its transition
  // dropped on the floor.
  test("a state change during publication is not lost", () => {
    const client = new FakeClient("idle");
    const stopWatching = connectionStore.subscribe(() => {
      if (connectionStore.getState().client === client && client.state === "idle") {
        client.emitStateChange("connecting");
      }
    });
    connectionStore.getState().connect(client);
    stopWatching();

    expect(connectionStore.getState().state).toBe("connecting");
  });

  // The unsubscribe returned by onStateChange is the cooperative half of the
  // fence. It is not sufficient on its own: a real client may have already
  // captured the callback into an in-flight dispatch, so detaching cannot
  // un-invoke it. The identity check has to stand without it.
  test("a late callback from a replaced client is ignored even when its unsubscribe does not detach", () => {
    class UndetachableClient extends FakeClient {
      captured: ((s: ConnectionState) => void) | null = null;
      override onStateChange(cb: (s: ConnectionState) => void): () => void {
        this.captured = cb;
        return () => {};
      }
    }

    const stale = new UndetachableClient("ready");
    connectionStore.getState().connect(stale);
    const leaked = stale.captured;
    expect(leaked).not.toBeNull();

    const current = new FakeClient("ready");
    connectionStore.getState().connect(current);

    leaked?.("closed");

    expect(connectionStore.getState().client).toBe(current);
    expect(connectionStore.getState().state).toBe("ready");
  });
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
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" },
      },
    });
    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
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
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_9", status: "inProgress", itemsView: "", startedAt: 1000 },
      },
    });
    fake.emitNotification({
      method: "item/started",
      params: {
        threadId: "thr_ref_a",
        turnId: "turn_9",
        item: { type: "commandExecution", id: "item_pre_cut_1", turnId: "turn_9", status: "inProgress" },
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
        readResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
        }),
        cut,
      ),
    );
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "turn/completed",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turn: {
            id: "turn_system",
            status: "completed",
            itemsView: "full",
            items: [
              {
                type: "systemMessage",
                id: "item_plugin_loaded_1",
                turnId: "turn_system",
                description: "Plugin loaded: superpowers",
                text: "",
                eventKind: "plugin_loaded",
                status: "completed",
              },
            ],
          },
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
        readResponse("ref_a", {
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
        method: "turn/completed",
        params: {
          threadId: "thr_ref_a",
          turn: {
            id: "turn_system",
            status: "completed",
            itemsView: "full",
            items: [
              {
                type: "systemMessage",
                id: "item_hook_completed_1",
                turnId: "turn_system",
                description: "Hook completed",
                text: "exit 0",
                eventKind: "hook_completed",
                status: "completed",
              },
            ],
          },
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
        readResponse("ref_a", {
          status: { type: "active" },
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
        }),
        cut,
      ),
    );
    await emitAtResponseCut(cut, "ref_a", () => {
      fake.emitNotification({
        method: "turn/started",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turn: { id: "turn_7", status: "inProgress", itemsView: "", startedAt: 1000 },
        },
      });
      fake.emitNotification({
        method: "item/started",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turnId: "turn_7",
          item: { type: "commandExecution", id: "item_tool_1", turnId: "turn_7", status: "inProgress" },
        },
      });
      fake.emitNotification({
        method: "turn/completed",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turn: { id: "turn_7", status: "failed", itemsView: "", error: { message: "boom" } },
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
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) {
        return readResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
        });
      }
      return new Promise<ThreadReadResponse>((resolve) => {
        resolveRefresh = resolve;
      });
    });

    await threadsStore.getState().ensureThread("ref_a");
    fake.emitNotification({
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 },
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
        readResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
          evener: {
            ref: "ref_a",
            capabilities: CAPABILITIES,
            queue: { revision: 0 },
            activeTurnId: "turn_m6",
          },
        }),
        cut,
      ),
    );
    await emitAtResponseCut(
      cut,
      "ref_a",
      () => {
        fake.emitNotification({
          method: "item/completed",
          params: {
            threadId: "thr_ref_a",
            ref: "ref_a",
            turnId: "turn_m6",
            item: {
              type: "agentMessage",
              id: "item_assistant_m6",
              turnId: "turn_m6",
              text: "skillguard turn complete",
              status: "completed",
            },
          },
        });
        fake.emitNotification({
          method: "turn/completed",
          params: {
            threadId: "thr_ref_a",
            ref: "ref_a",
            turn: { id: "turn_m6", status: "completed", itemsView: "" },
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
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 },
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
    const snapshot = readResponse("ref_a", {
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
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) return snapshot;
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
      method: "item/agentMessage/delta" as const,
      params: { threadId: "thr_ref_a", ref: "ref_a", turnId: "turn_1", itemId: "item_1", delta: "ha" },
    };
    replacementRead.resolve?.({ thread: { ...snapshot.thread, name: "replacement" } });
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");
    fake.emitNotification(delta);
    fake.emitNotification(delta);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.pendingText !== undefined);

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.items[0]?.pendingText?.join("")).toBe("haha");
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
      method: "item/agentMessage/delta",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        itemId: "item_1",
        delta: "included",
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
      method: "item/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" },
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
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_1", status: "inProgress", itemsView: "full", items: [] },
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
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_live", status: "inProgress", itemsView: "" },
      },
    });
    fake.emitNotification({
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_live",
        item: { type: "agentMessage", id: "item_live", turnId: "turn_live", text: "answer", status: "completed" },
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
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" },
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

  // Owned hydration (the retry lifecycle). A read that fails while the socket
  // is still ready is a transport failure, not a lost claim: the pane keeps the
  // ref and the store schedules the next read itself. Every case below drives
  // the injected scheduler by hand and asserts request counts and map identity,
  // never elapsed time.
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
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("same-ready refresh failure preserves stale model until retry succeeds", async () => {
    const fake = connectFakeClient();
    let readAttempts = 0;
    fake.on("thread/read", () => {
      readAttempts += 1;
      if (readAttempts === 1) {
        return readResponse("ref_a", { turns: [{ id: "turn_a", status: "completed", itemsView: "full", items: [] }] });
      }
      if (readAttempts === 2) throw new RequestTimeoutError("refresh read timed out");
      return readResponse("ref_a", { turns: [{ id: "turn_b", status: "completed", itemsView: "full", items: [] }] });
    });

    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_a");

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => scheduledHydrationRetries.length === 1);

    // Stale beats blank: the failed refresh leaves version A published.
    expect(readAttempts).toBe(2);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_a");

    runScheduledHydrationRetry();
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_b");

    expect(readAttempts).toBe(3);
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_b");
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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
    runScheduledHydrationRetry(1);
    await flushUntil(() => late.resolve !== null);
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
    expect(a.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);

    const b = new FakeClient("ready");
    const bRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    b.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (bRead.resolve = resolve)));
    connectionStore.getState().connect(b);
    await flushUntil(() => bRead.resolve !== null);

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
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
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
    await settleCallerContinuations();

    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushUntil(() => resyncRead.resolve !== null);
    expect(readAttempts).toBe(2);

    runScheduledHydrationRetry();
    await flushUntil(() => readAttempts === 3);
    expect(readAttempts).toBe(2);

    resyncRead.resolve?.(
      readResponse("ref_a", { turns: [{ id: "turn_resync", status: "completed", itemsView: "full", items: [] }] }),
    );
    await ensuring;
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.id).toBe("turn_resync");
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
  // once per OTHER such store whose module happens to already be loaded in
  // this worker (e.g. via an earlier file's real App render pulling in
  // tree.ts) - a real, correct fact about this composition, not a leak.
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

describe("useThreadsStore.releaseThread", () => {
  test("refcounts panes; stops tracking only when the last pane releases", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a"); // pane 1
    await threadsStore.getState().ensureThread("ref_a"); // pane 2 (refcount 2, no re-read)

    threadsStore.getState().releaseThread("ref_a"); // pane 1 leaves
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true); // pane 2 still open

    threadsStore.getState().releaseThread("ref_a"); // pane 2 leaves
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("session and panel lifecycle claims survive close and remount reordering until the final reference releases", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    await threadsStore.getState().ensureThread("ref_a"); // session pane mounts
    await threadsStore.getState().ensureThread("ref_a"); // panel pane mounts
    threadsStore.getState().releaseThread("ref_a"); // session pane closes first
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);

    // A dockview reorder can remount the session before it unmounts the panel.
    // The hand-off must remain refcounted instead of transiently evicting the
    // hydrated model or issuing another read.
    await threadsStore.getState().ensureThread("ref_a");
    threadsStore.getState().releaseThread("ref_a"); // panel pane unmounts
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);

    threadsStore.getState().releaseThread("ref_a"); // remounted session is the last reference
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("releasing an untracked ref is a harmless no-op", () => {
    expect(() => threadsStore.getState().releaseThread("never_tracked")).not.toThrow();
  });

  test("the final pane release unsubscribes the ref on the wire; earlier releases do not", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a"); // pane 1
    await threadsStore.getState().ensureThread("ref_a"); // pane 2

    threadsStore.getState().releaseThread("ref_a"); // pane 1 leaves
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);

    threadsStore.getState().releaseThread("ref_a"); // last pane leaves
    const unsubscribes = fake.calls.filter((call) => call.method === "thread/unsubscribe");
    expect(unsubscribes).toHaveLength(1);
    expect(unsubscribes[0]?.params).toMatchObject({ ref: "ref_a" });
  });

  test("a released-then-re-ensured ref re-subscribes with subscribe:true again", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    threadsStore.getState().releaseThread("ref_a");

    await threadsStore.getState().ensureThread("ref_a");
    const reads = fake.calls.filter((call) => call.method === "thread/read");
    expect(reads).toHaveLength(2);
    expect(reads[0]?.params).toMatchObject({ subscribe: true });
    expect(reads[1]?.params).toMatchObject({ subscribe: true });
    // And the final release unsubscribes exactly once more.
    threadsStore.getState().releaseThread("ref_a");
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(2);
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

describe("notification routing", () => {
  test("notifications for a never-tracked ref are dropped (same map reference preserved)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_tracked"));
    await threadsStore.getState().ensureThread("ref_tracked");

    const before = threadsStore.getState().threads;
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_untracked", ref: "ref_untracked", status: { type: "active" } },
    });

    expect(threadsStore.getState().threads).toBe(before);
  });

  test("turn/completed is delivered only to the model whose activeTurnId matches (sibling immunity)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      const ref = (params as { ref: string }).ref;
      if (ref === "ref_a") {
        return readResponse("ref_a", {
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
        });
      }
      return readResponse("ref_b", {
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "", items: [] }],
        evener: { ref: "ref_b", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
      });
    });

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");
    expect(threadsStore.getState().threads.get("ref_b")?.activeTurnId).toBe("turn_1");

    const beforeB = threadsStore.getState().threads.get("ref_b");

    // Stream A's own item BEFORE settling — wire-true: the real
    // turn/completed is a bare status/timing stamp with no items (every live
    // settle site in internal/appprojector/appwire_projection.go emits
    // Turn{ID,Status[,Error]} with Items nil, ItemsView "" — see
    // protocol/reducer.ts's "turn/completed" case), so A's item must already
    // be in the model via item/started + item/completed, not smuggled in
    // through the settle payload.
    fake.emitNotification({
      method: "item/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_a1", turnId: "turn_1", status: "inProgress" },
      },
    });
    fake.emitNotification({
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" },
      },
    });

    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    });

    // The rightful owner (A, whose activeTurnId matched) settles...
    const modelA = threadsStore.getState().threads.get("ref_a");
    expect(modelA?.activeTurnId).toBeUndefined();
    expect(modelA?.turns[0]?.items[0]?.text).toBe("A's answer");

    // ...while B, simultaneously active on the same numbered turn_1, is a
    // same-reference no-op because v2's ref/thread identity is authoritative.
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
  });

  test("a session's startup announcements reach the tracked model even though their synthetic turn is never active", async () => {
    // The daemon bundles every SESSION_START-time announcement into one
    // synthetic prelude turn and sends it as turn/completed
    // (internal/appprojector/appwire_projection.go's systemAnnouncementItem).
    // That turn is never the model's activeTurnId, so an activeTurnId gate on
    // delivery drops the whole startup burst and the "N system events" group
    // materializes only on the next hydrate. Identity (ref/threadId) is what
    // decides delivery; where the frame lands inside the model is the
    // reducer's call.
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "", items: [] }],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
      }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");

    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: {
          id: "turn_system",
          status: "completed",
          itemsView: "full",
          items: [
            {
              type: "systemMessage",
              id: "item_plugin_loaded_1",
              turnId: "turn_system",
              description: "Plugin loaded: superpowers",
              text: "",
              eventKind: "plugin_loaded",
              status: "completed",
            },
          ],
        },
      },
    });

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_system", "turn_1"]);
    expect(model?.turns[0]?.items[0]?.id).toBe("item_plugin_loaded_1");
    // The real turn above it is still in flight.
    expect(model?.activeTurnId).toBe("turn_1");
  });
});

describe("notification routing index (ref / threadId fast path)", () => {
  test("a ref-routed notification reaches exactly the model with that ref (sibling and watched models untouched)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    await threadsStore.getState().watchThread("ref_w");

    const beforeB = threadsStore.getState().threads.get("ref_b");
    const beforeWatched = threadsStore.getState().watchedThreads.get("ref_w");

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
    // Same-reference no-op for the sibling pane and the lean watch: both hold
    // distinct models the scan would not have selected.
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
    expect(threadsStore.getState().watchedThreads.get("ref_w")).toBe(beforeWatched);
  });

  test("a threadId-routed notification (no ref on the frame) reaches every model with that thread id, in both maps", async () => {
    const fake = connectFakeClient();
    // ref_primary and ref_alias hydrate from snapshots carrying the SAME
    // thread id thr_shared (distinct refs, one thread); ref_watched is a lean
    // watch of a third ref whose thread id is also thr_shared; ref_other is
    // an unrelated pane. A frame with only threadId = thr_shared must select
    // every model the old scan would have: primary, alias, and the watch —
    // and nothing else.
    fake.on("thread/read", (params) => {
      const ref = (params as { ref: string }).ref;
      if (ref === "ref_primary" || ref === "ref_alias") return readResponseWithId(ref, "thr_shared");
      if (ref === "ref_watched") return readResponseWithId(ref, "thr_shared");
      return readResponse(ref);
    });
    await threadsStore.getState().ensureThread("ref_primary");
    await threadsStore.getState().ensureThread("ref_alias");
    await threadsStore.getState().ensureThread("ref_other");
    await threadsStore.getState().watchThread("ref_watched");

    const beforeOther = threadsStore.getState().threads.get("ref_other");

    // thread/status/changed with threadId but NO ref: not wire-true for this
    // method (the hub always sends both), so cast like the suite's other
    // wire-shape probes. The routing layer must key on threadId alone.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_shared", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().threads.get("ref_primary")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().threads.get("ref_alias")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().watchedThreads.get("ref_watched")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().threads.get("ref_other")).toBe(beforeOther);
  });

  test("a ref-routed notification wins over a contradictory threadId (ref has precedence, like the scan)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a"); // thr_ref_a
    await threadsStore.getState().ensureThread("ref_b"); // thr_ref_b

    const beforeB = threadsStore.getState().threads.get("ref_b");

    // ref names ref_a but threadId names ref_b's thread: notificationTargetsThread
    // checks ref FIRST, so the scan selected only ref_a — the index must too.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_b", ref: "ref_a", status: { type: "active" } },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
  });

  test("a notification for an unknown ref changes nothing (no model, same map reference)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_w");

    const before = threadsStore.getState().threads;
    const beforeWatched = threadsStore.getState().watchedThreads;

    fake.emitNotification({
      method: "item/agentMessage/delta",
      params: { threadId: "thr_nowhere", ref: "ref_nowhere", turnId: "turn_1", itemId: "item_1", delta: "x" },
    });
    // threadId-only frame for an unknown id, too.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_nowhere", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().threads).toBe(before);
    expect(threadsStore.getState().watchedThreads).toBe(beforeWatched);
  });

  test("identity-free broadcast-style notifications match no model (same map reference, like the scan)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_w");

    const before = threadsStore.getState().threads;
    const beforeWatched = threadsStore.getState().watchedThreads;

    const broadcasts: AnyNotification[] = [
      { method: "evener/auth/updated", params: { provider: "p" } },
      { method: "evener/launch/updated", params: { cwd: "/tmp", layer: "test" } },
      { method: "evener/navigation/invalidated", params: { generationId: "g", sequence: 1, targets: [] } },
      { method: "evener/marketplace/updated", params: {} },
      { method: "evener/plugin/updated", params: {} },
      {
        method: "evener/settings/transcriptDisplay/changed",
        params: {
          layout: "compact",
          revision: 1,
          config: { version: 1, content: {}, advanced: {} },
        },
      } as AnyNotification,
    ];
    for (const n of broadcasts) fake.emitNotification(n);

    expect(threadsStore.getState().threads).toBe(before);
    expect(threadsStore.getState().watchedThreads).toBe(beforeWatched);
  });

  test("a notification arriving mid-hydration for a pending ref is withheld from the stale model but buffered for replay", async () => {
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
    finishRead(markResponseCut(readResponse("ref_a"), cut));
    // The pending hydration still owns ref_a: a live frame for it must be
    // buffered, and the (absent) stale model must not be resurrected.
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "thread/status/changed",
        params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
      }),
    );
    await ensuring;

    // The buffered frame replayed onto the published snapshot.
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("index survives release and re-ensure of the same ref (no stale model left behind)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    // A frame for the released ref must find no model through the index —
    // the released model must have been de-indexed, not just unmapped.
    const before = threadsStore.getState().threads;
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads).toBe(before);

    // Re-ensure republishes and the index must pick the ref up again.
    await threadsStore.getState().ensureThread("ref_a");
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("index survives release and re-watch of the same ref (watched map)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().watchThread("ref_w");
    threadsStore.getState().releaseWatchedThread("ref_w");
    expect(threadsStore.getState().watchedThreads.has("ref_w")).toBe(false);

    const before = threadsStore.getState().watchedThreads;
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_w", ref: "ref_w", status: { type: "active" } },
    });
    expect(threadsStore.getState().watchedThreads).toBe(before);

    await threadsStore.getState().watchThread("ref_w");
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_w", ref: "ref_w", status: { type: "active" } },
    });
    expect(threadsStore.getState().watchedThreads.get("ref_w")?.status).toEqual({ type: "active" });
  });

  test("index follows model replacement on rehydration (resync publishes a fresh model under the same ref)", async () => {
    const fake = connectFakeClient();
    let status: ThreadStatus = { type: "idle" };
    fake.on("thread/read", () => readResponse("ref_a", { status }));
    await threadsStore.getState().ensureThread("ref_a");

    // A targeted resync republishes a whole new model; routing must follow it.
    status = { type: "active" };
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    // hydrations counts the initial ensureThread publish too, so the resync
    // lands on 2 (and the second resync below on 3).
    await flushUntil(() => threadsStore.getState().hydrations.get("ref_a") === 2);
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });

    status = { type: "idle" };
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushUntil(() => threadsStore.getState().hydrations.get("ref_a") === 3);
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "idle" });

    // And live routing still lands on the newest model.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
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
      return readResponseWithId(ref, threadId);
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
        method: "turn/started",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          turn: { id: pick(turnIds), status: "inProgress", itemsView: "" },
        },
      }),
      () => ({
        method: "item/started",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          turnId: pick(turnIds),
          item: { type: "agentMessage", id: pick(itemIds), turnId: pick(turnIds), status: "inProgress" },
        },
      }),
      () => ({
        method: "item/agentMessage/delta",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          turnId: pick(turnIds),
          itemId: pick(itemIds),
          delta: "x",
        },
      }),
      () => ({
        method: "item/completed",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          turnId: pick(turnIds),
          item: { type: "agentMessage", id: pick(itemIds), turnId: pick(turnIds), text: "done", status: "completed" },
        },
      }),
      () => ({
        method: "turn/completed",
        params: {
          threadId: pick(Object.values(threadIds)),
          ref: pick(refs),
          turn: { id: pick(turnIds), status: "completed", itemsView: "" },
        },
      }),
      () => ({
        method: "thread/queueChanged",
        params: { threadId: pick(Object.values(threadIds)), ref: pick(refs), queue: { revision: 1 } },
      }),
    ];

    let clock = 10_000;
    const history: AnyNotification[] = [];
    let duplicateStarts = 0;
    for (let i = 0; i < 200; i += 1) {
      const n = pick(generators)();
      history.push(n);
      clock += 7;
      // The small, deliberately shared turn-id space generates repeated
      // starts. Derive the expected diagnostics from the independent scan
      // state BEFORE either fold, never from what the indexed store logs.
      const expectedDiagnostics =
        n.method === "turn/started"
          ? [...reference.threads.values(), ...reference.watchedThreads.values()]
              .filter(
                (model) =>
                  notificationTargetsThread(n, model) && model.turns.some((turn) => turn.id === n.params.turn.id),
              )
              .map(() => [
                `applyNotification: turn/started turnId ${n.params.turn.id} already exists in model.turns — replacing it in place instead of appending a duplicate row (turn-id-uniqueness invariant violated)`,
              ])
          : [];
      duplicateStarts += expectedDiagnostics.length;
      const checkDiagnostics = (fold: () => void): void => {
        const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
        try {
          fold();
          expect(errorSpy.mock.calls, `diagnostics for notification ${i}: ${n.method}`).toEqual(expectedDiagnostics);
        } finally {
          errorSpy.mockRestore();
        }
      };
      const dateNowSpy = vi.spyOn(Date, "now").mockReturnValue(clock);
      try {
        checkDiagnostics(() => fake.emitNotification(n));
      } finally {
        dateNowSpy.mockRestore();
      }
      checkDiagnostics(() => scanFold(reference, n, clock, new Set()));
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
    expect(duplicateStarts).toBeGreaterThan(0);

    const actual = snapshotFor({
      threads: threadsStore.getState().threads,
      watchedThreads: threadsStore.getState().watchedThreads,
      frameTimes: threadsStore.getState().frameTimes,
    });
    const expected = snapshotFor(reference);
    expect(actual, `differential mismatch after ${history.length} random notifications`).toEqual(expected);
  });
});

describe("notification routing for out-of-catalog methods", () => {
  test("an unknown method carrying a matching ref still routes by that ref", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");

    const beforeB = threadsStore.getState().threads.get("ref_b");
    const before = threadsStore.getState().threads;

    // A method this build predates. Routing reads only the frame's own
    // ref/threadId (notificationRoutingKey is method-agnostic), so an
    // out-of-catalog frame routes exactly like a catalog frame: the
    // ref-routed model is selected, and no full-scan fallback exists to
    // behave differently. The reducer's default case returns the same
    // reference, so the pin here is selection-plus-no-side-effect: the
    // map is untouched for the sibling and the map reference is identical,
    // and a follow-up catalog frame still lands on the selected model.
    fake.emitUnknownNotification({ method: "totally/unknown", params: { ref: "ref_a" } });

    expect(threadsStore.getState().threads).toBe(before);
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
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
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" },
      },
    });
    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");
    expect(threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.output).toBe("");

    reconnectRead.resolve?.(authoritativeSnapshot);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
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
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_live", status: "inProgress", itemsView: "" },
      },
    });
    fake.emitNotification({
      method: "turn/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_live", status: "completed", itemsView: "" },
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
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" },
      },
    });
    b.emitNotification({
      method: "turn/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
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

describe("client swap (manual retry) rewiring", () => {
  // The trap this covers (see docs/superpowers/plans/
  // 2026-07-20-webui-rewrite-wave3-shell.md, Task 5): before this
  // describe block existed, wiredClient was only ever updated lazily,
  // inside requireClient(), the first time some ACTION (ensureThread/send/
  // steer/queue/interrupt) ran. Swapping connectionStore's client
  // reference alone (shell/ConnectionBanner.tsx's retry, which mints a
  // FRESH AppwireClient rather than reusing the dead one) left
  // onNotification/onReady still attached to the dead client - the banner
  // would report "ready" while every open pane silently stopped receiving
  // deltas, since nothing forces a store action to run just because the
  // connection recovered. This test drives the swap through NOTHING but
  // connectionStore.getState().connect(b) - no ensureThread/send/etc call
  // follows it - so it only passes if the rewiring is reactive to the
  // client reference itself, not piggybacked on some later action.
  test("swapping to a fresh client re-hydrates tracked refs, routes its notifications, and detaches the dead client's handlers (no double delivery)", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);

    // Kill A for good (a terminal drop, not a same-client reconnect - see
    // the "reconnect resubscribe" tests above for that separate case).
    a.emitStateChange("closed");

    // The manual retry: a FRESH client, already "ready" by the time it's
    // handed to connectionStore.connect() - mirrors the real sequence
    // ConnectionBanner's retry follows (construct, await connect(), THEN
    // wire the store), not an in-flight handshake. Its own thread/read
    // response deliberately differs from A's (empty turns, not one) so a
    // passing assertion below can only mean the re-hydrate actually ran
    // against B, not a stale snapshot left over from A.
    const b = new FakeClient("ready");
    b.on("thread/read", () => readResponse("ref_a", { turns: [] }));
    connectionStore.getState().connect(b);

    // Re-hydration is async (a thread/read round trip against B) with
    // nothing else in this test driving it forward.
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns.length === 0);
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(0);

    // B's own live notification reaches the tracked model...
    b.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });

    // ...while A's handlers were detached at the swap: the same
    // notification shape, injected via the now-dead client, must NOT be
    // delivered - proof of no lingering double-subscription, not just
    // that B independently works.
    a.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "idle" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" }); // unchanged - A's emit was a no-op
  });

  test("swapping to a fresh client that is not yet ready waits for its own onReady, same as the initial connection", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    a.emitStateChange("closed");

    const b = new FakeClient("connecting"); // still mid-handshake when wired in
    b.on("thread/read", () => readResponse("ref_a"));
    connectionStore.getState().connect(b);

    // b isn't ready yet, so no eager hydrate should have fired against it -
    // asserted directly on b's own call log (not on `threads` content,
    // which can't distinguish "not re-hydrated yet" from "re-hydrated to
    // an identical snapshot").
    await Promise.resolve(); // let any (wrongly) eager work settle before asserting it didn't happen
    expect(b.calls.filter((c) => c.method === "thread/read")).toHaveLength(0);

    b.emitReady(); // completes B's own handshake
    await flushUntil(() => b.calls.filter((c) => c.method === "thread/read").length > 0);

    expect(b.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  });
});

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
    await waitFor(async () => expect((await readMutationPersistence("ref_a")).outbox).toEqual([]));

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
      await waitFor(async () => expect((await readMutationPersistence("ref_a")).outbox).toEqual([]));
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
      await waitFor(async () => expect((await readMutationPersistence("ref_a")).outbox).toEqual([]));
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
    await waitFor(async () => expect((await readMutationPersistence("ref_a")).outbox).toEqual([]));
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
    await waitFor(async () => expect(await independent.getOutbox(record.clientMutationId)).toBeUndefined());
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
    await waitFor(async () => expect((await readMutationPersistence("ref_a")).outbox).toEqual([]));

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
      .forkFromTurn("ref_a", { sourceTurnId: "turn_1", editedInput: "edited text" });

    const call = fake.calls.find((c) => c.method === "thread/fork");
    expect(call?.params).toEqual({ ref: "ref_a", sourceTurnId: "turn_1", editedInput: "edited text" });
    expect(result.thread.evener.ref).toBe("ref_child");
  });

  test("forkFromTurn supports the aside mode's mutually-exclusive param set", async () => {
    const fake = connectFakeClient();
    fake.on("thread/fork", () => ({ thread: testThread("ref_aside") }));

    await threadsStore.getState().forkFromTurn("ref_a", { aside: true });

    const call = fake.calls.find((c) => c.method === "thread/fork");
    // sourceTurnId has no `omitempty` on the wire (appwire/types.go:694) -
    // it is required JSON even when meaningless (aside is mutually
    // exclusive with it), so the store defaults it to "" rather than
    // omitting the field.
    expect(call?.params).toEqual({ ref: "ref_a", aside: true, sourceTurnId: "" });
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

    await waitFor(async () => {
      expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
    });
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

    await waitFor(async () => {
      expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
    });
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
    await waitFor(async () => {
      expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
    });
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

// listModels/listTasks (T1 addendum, sanctioned NEEDS_CONTEXT gap for the
// chrome stream): both are plain read-only wire calls with no turn-CAS
// concept - verified against every server-side handler
// (cmd/evener-hub/app_rpc.go's registerMiscHandlers, cmd/evener-hub/
// app source ListTasks implementations, and server/appwire_runtime.go's
// handleAppTasksList/handleAppModelList): none
// of them ever construct appwire.Conflict(). Neither action maps errors -
// a WireError (even one shaped like a Conflict, which cannot actually occur
// here) passes through unchanged, same as resolveEscalation above.
describe("useThreadsStore.listModels", () => {
  function modelListResponse(): ModelListResponse {
    return {
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5" },
        { provider: "openai", model: "gpt-5.5" },
      ],
      diagnostics: [{ provider: "ollama", message: "ollama: connection refused" }],
      recent: [{ provider: "anthropic", model: "claude-sonnet-4-5" }],
    };
  }

  test("sends model/list with no params and returns the response verbatim", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    const result = await threadsStore.getState().listModels();

    const call = fake.calls.find((c) => c.method === "model/list");
    expect(call?.params).toEqual({});
    expect(result).toEqual(modelListResponse());
  });

  test("caches across calls within the session - a second call does not re-request", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    await threadsStore.getState().listModels();
    await threadsStore.getState().listModels();

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);
  });

  test("concurrent calls before the first resolves share one request", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    const [a, b] = await Promise.all([threadsStore.getState().listModels(), threadsStore.getState().listModels()]);

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);
    expect(a).toEqual(modelListResponse());
    expect(b).toEqual(modelListResponse());
  });

  test("evener/auth/updated clears the cache so the next call re-requests", async () => {
    const fake = connectFakeClient();
    let call = 0;
    fake.on("model/list", () => {
      call += 1;
      return { data: [{ provider: "google-vertex", model: `model-${call}` }] };
    });

    const before = await threadsStore.getState().listModels();
    // A stored credential can make new models discoverable (a Vertex
    // credential JSON enables the publisher-model listing), so the listing
    // cached before it is stale.
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } });
    const after = await threadsStore.getState().listModels();

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
    expect(before.data[0]?.model).toBe("model-1");
    expect(after.data[0]?.model).toBe("model-2");
  });

  // deferredModelList scripts model/list to hang until the test resolves it,
  // one resolver per request in arrival order, so a test can interleave the
  // auth-updated notification with listings still in flight.
  function deferredModelList(fake: FakeClient): Array<(resp: ModelListResponse) => void> {
    const pending: Array<(resp: ModelListResponse) => void> = [];
    fake.on(
      "model/list",
      () =>
        new Promise<ModelListResponse>((resolve) => {
          pending.push(resolve);
        }),
    );
    return pending;
  }
  const stale: ModelListResponse = { data: [{ provider: "google-vertex", model: "stale" }] };
  const fresh: ModelListResponse = { data: [{ provider: "google-vertex", model: "fresh" }] };

  test("a listing in flight when evener/auth/updated arrives still answers its caller but does not become the cache", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const first = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } });
    pending[0]?.(stale);
    expect((await first).data[0]?.model).toBe("stale");

    const after = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 2);
    pending[1]?.(fresh);
    expect((await after).data[0]?.model).toBe("fresh");
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
  });

  test("a caller arriving after evener/auth/updated does not de-dupe onto a pre-credential listing still in flight", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const first = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } });
    const second = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 2);
    expect(pending).toHaveLength(2);

    pending[0]?.(stale);
    pending[1]?.(fresh);
    expect((await first).data[0]?.model).toBe("stale");
    expect((await second).data[0]?.model).toBe("fresh");
  });

  test("a pre-credential listing settling does not evict the newer in-flight listing from the dedupe slot", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const first = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } });
    const second = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 2);
    pending[0]?.(stale);
    await first;

    // The third caller must share the second request, not start a third: a
    // task yield (not a turn count) lets any request it would have issued
    // reach the fake before the negative assertion.
    const third = threadsStore.getState().listModels();
    await settleCallerContinuations();
    expect(pending).toHaveLength(2);

    pending[1]?.(fresh);
    expect((await second).data[0]?.model).toBe("fresh");
    expect((await third).data[0]?.model).toBe("fresh");
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
  });

  test("refresh:true bypasses the cache and issues a fresh request", async () => {
    const fake = connectFakeClient();
    let call = 0;
    fake.on("model/list", () => {
      call += 1;
      return { data: [{ provider: "anthropic", model: `model-${call}` }] };
    });

    const first = await threadsStore.getState().listModels();
    const second = await threadsStore.getState().listModels(true);

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
    expect(first.data[0]?.model).toBe("model-1");
    expect(second.data[0]?.model).toBe("model-2");
  });

  test("a refresh landing before an older in-flight request keeps the cache fresh - the stale late answer never overwrites it", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const preConnection = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    // A keyless/no-credential-change connection emits no evener/auth/updated
    // between the pre-connection listing and the post-connection refresh, so
    // the epoch guard alone cannot protect the cache here.
    const refreshed = threadsStore.getState().listModels(true);
    await flushUntil(() => pending.length === 2);

    pending[1]?.(fresh);
    expect((await refreshed).data[0]?.model).toBe("fresh");
    pending[0]?.(stale);
    // The older request still answers its own caller; it must not become the cache.
    expect((await preConnection).data[0]?.model).toBe("stale");

    expect((await threadsStore.getState().listModels()).data[0]?.model).toBe("fresh");
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
  });

  test("a refresh supersedes the shared in-flight list so a concurrent caller awaits the newest request", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const preRefresh = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    // A refresh with no intervening evener/auth/updated (a keyless connection,
    // a config-only save) leaves the dedupe slot in place, so the slot must be
    // superseded here or the next caller joins the pre-refresh request.
    const refreshed = threadsStore.getState().listModels(true);
    await flushUntil(() => pending.length === 2);

    const concurrent = threadsStore.getState().listModels();
    await settleCallerContinuations();
    expect(pending).toHaveLength(2);

    pending[1]?.(fresh);
    pending[0]?.(stale);
    // The older request still answers its own caller.
    expect((await preRefresh).data[0]?.model).toBe("stale");
    expect((await refreshed).data[0]?.model).toBe("fresh");
    // The concurrent caller joined the refresh, not the pre-refresh listing.
    expect((await concurrent).data[0]?.model).toBe("fresh");
  });

  test("a request in flight across a test reset cannot repopulate the fresh cache", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const inFlight = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    // The reset clears the cache and the in-flight slot; a request already in
    // flight must lose the cache write to the reset the way it loses it to a
    // newer request, or its late answer becomes the post-reset cache.
    resetThreadsStoreForTests();
    pending[0]?.(fresh);
    await inFlight;

    const listSpy = vi.fn(() => ({ data: [{ provider: "google-vertex", model: "after" }] }));
    fake.on("model/list", listSpy);
    const after = await threadsStore.getState().listModels();
    expect(listSpy).toHaveBeenCalledTimes(1);
    expect(after.data[0]?.model).toBe("after");
  });

  test("a warm cache does not answer a non-refresh caller while a refresh is in flight", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);
    // Warm the cache with the pre-refresh listing.
    const first = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    pending[0]?.(stale);
    expect((await first).data[0]?.model).toBe("stale");

    // A refresh is now in flight. A non-refresh caller arriving alongside it
    // must not be handed the warm cache: the refresh was issued precisely to
    // replace that listing, and two concurrent model pickers would otherwise
    // receive different (one stale, one fresh) results.
    const refreshed = threadsStore.getState().listModels(true);
    await flushUntil(() => pending.length === 2);
    const concurrent = threadsStore.getState().listModels();
    await settleCallerContinuations();
    expect(pending).toHaveLength(2); // joined the refresh, issued no third request

    pending[1]?.(fresh);
    expect((await refreshed).data[0]?.model).toBe("fresh");
    expect((await concurrent).data[0]?.model).toBe("fresh");
  });

  test("an older in-flight request landing first still loses the cache to a later refresh", async () => {
    const fake = connectFakeClient();
    const pending = deferredModelList(fake);

    const preConnection = threadsStore.getState().listModels();
    await flushUntil(() => pending.length === 1);
    const refreshed = threadsStore.getState().listModels(true);
    await flushUntil(() => pending.length === 2);

    pending[0]?.(stale);
    expect((await preConnection).data[0]?.model).toBe("stale");
    pending[1]?.(fresh);
    expect((await refreshed).data[0]?.model).toBe("fresh");

    expect((await threadsStore.getState().listModels()).data[0]?.model).toBe("fresh");
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
  });

  test("a failed call does not cache a rejected promise - the next call retries rather than repeating the same rejection", async () => {
    const fake = connectFakeClient();
    let shouldFail = true;
    fake.on("model/list", () => {
      if (shouldFail) throw new Error("boom");
      return modelListResponse();
    });

    await expect(threadsStore.getState().listModels()).rejects.toThrow("boom");

    shouldFail = false;
    const result = await threadsStore.getState().listModels();

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
    expect(result).toEqual(modelListResponse());
  });

  test("propagates a rejection unchanged - not mapped to ConflictError even when it is Conflict-shaped (model/list can never actually return one)", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => {
      throw new WireError("shouldn't happen", -32013, { evenerErrorInfo: "conflict" });
    });

    const rejection = threadsStore.getState().listModels();
    await expect(rejection).rejects.toBeInstanceOf(WireError);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().listModels()).rejects.toThrow(/no client connected/i);
  });

  test("resetThreadsStoreForTests clears the models cache, same as every other module-private cache", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());
    await threadsStore.getState().listModels();
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);

    resetThreadsStoreForTests();

    const fake2 = connectFakeClient();
    fake2.on("model/list", () => modelListResponse());
    await threadsStore.getState().listModels();
    expect(fake2.calls.filter((c) => c.method === "model/list")).toHaveLength(1); // fresh fetch, not a stale cache hit
  });

  // Issue #195's RCA: read-only, so it waits out a reconnect (via
  // requireReadyClient, stores/threads.ts) instead of failing with
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection - contrast with the mutation re-pin tests (setModel, send),
  // which must keep rejecting synchronously.
  test("waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().listModels();
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
    expect(fake.calls).toHaveLength(0); // never sent while reconnecting

    fake.emitReady();
    await expect(pending).resolves.toEqual(modelListResponse());
  });

  // The ready-wait is skipped entirely for a warm cache/inflight hit (see
  // listModels's own comment): a call that needs no wire round-trip must
  // not block on a reconnect it has no reason to care about.
  test("a cached response resolves immediately even while reconnecting, without waiting", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());
    await threadsStore.getState().listModels(); // warms the cache
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);

    fake.emitStateChange("reconnecting");
    await expect(threadsStore.getState().listModels()).resolves.toEqual(modelListResponse());
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1); // no second wire call
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
    method: "item/completed",
    params: {
      threadId: "thr_stale_ref",
      ref: "stale_ref",
      turnId: "turn_1",
      item: {
        type: "commandExecution",
        id: "item_1",
        turnId: "turn_1",
        clientMutationId: "old-mutation",
        output: "done",
        status: "completed",
      },
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

describe("useThreadsStore.listTasks", () => {
  // Wire-true shape: TaskListResponse.Data is `any` on the catalog
  // (appwire/types.go:896-898) - server/server.go's SetTasksFunc doc
  // comment says the registered function "should return a JSON-serializable
  // slice (typically []task.Task)"; agent/task/task_store.go:54-74 is that
  // struct. This fixture mirrors its real JSON field names verbatim.
  const TASKS_DATA = [
    { id: 1, type: "implement", description: "Wire up listModels/listTasks", prompt: "…", status: "done" },
    {
      id: 2,
      type: "verify",
      description: "Confirm tests pass",
      prompt: "…",
      status: "in_progress",
      depends_on: [1],
    },
  ];

  test("sends evener/tasks/list with {ref} and returns the raw data field, not the response wrapper", async () => {
    const fake = connectFakeClient();
    fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

    const result = await threadsStore.getState().listTasks("ref_a");

    const call = fake.calls.find((c) => c.method === "evener/tasks/list");
    expect(call?.params).toEqual({ ref: "ref_a" });
    expect(result).toEqual(TASKS_DATA);
  });

  test("propagates a source-backed rejection (actionUnavailable) unchanged, not mapped to ConflictError", async () => {
    const fake = connectFakeClient();
    // Mirrors a source that omits the capability: appwire.Unavailable(...),
    // code -32014, evenerErrorInfo "actionUnavailable" - never a Conflict.
    fake.on("evener/tasks/list", () => {
      throw new WireError("remote source does not expose evener tasks", -32014, {
        evenerErrorInfo: "actionUnavailable",
      });
    });

    const rejection = threadsStore.getState().listTasks("ref_remote");
    await expect(rejection).rejects.toBeInstanceOf(WireError);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toMatchObject({ evenerErrorInfo: "actionUnavailable" });
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().listTasks("ref_a")).rejects.toThrow(/no client connected/i);
  });

  // Issue #195's RCA: listTasks is read-only, so it waits out a reconnect
  // (via requireReadyClient, stores/threads.ts) instead of failing with
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection - contrast with the mutation re-pin tests (setModel, send),
  // which must keep rejecting synchronously.
  test("waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("evener/tasks/list", () => ({ data: TASKS_DATA }));

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().listTasks("ref_a");
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
    expect(fake.calls).toHaveLength(0); // never sent while reconnecting

    fake.emitReady();
    await expect(pending).resolves.toEqual(TASKS_DATA);
  });
});

describe("useThreadsStore.listJobs / jobOutput", () => {
  // Wire-true shape: JobsListResponse.Data / JobsOutputResponse.Data are both
  // `any` in appwire/types.go. The replacement jobs-list payload is the
  // recursive activity tree, while job output stays JobOutputTail. These
  // fixtures mirror the current wire JSON field names verbatim.
  const JOBS_DATA = {
    revision: 5,
    root: {
      sessionId: "sess_root",
      ref: "ref_a",
      label: "Root",
      aggregate: "working",
      counts: { active: 2, failed: 0, completed: 1, complete: true },
      entries: [
        {
          kind: "shell",
          job: {
            jobId: "job_1",
            ownerSessionId: "sess_root",
            ownerRef: "ref_a",
            type: "shell",
            status: "completed",
            description: "run tests",
            command: "go test ./...",
            terminal: true,
            background: true,
            hasOutput: true,
            startedAt: "2026-07-31T12:00:00Z",
            endedAt: "2026-07-31T12:01:00Z",
            exitCode: 0,
            outputBytes: 123,
          },
        },
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_1",
            childSessionId: "sess_child",
            childRef: "ref_child",
            turns: [
              {
                jobId: "job_2",
                ownerSessionId: "sess_root",
                ownerRef: "ref_a",
                type: "delegate",
                status: "running",
                terminal: false,
                background: true,
                hasOutput: false,
                description: "scout",
                startedAt: "2026-07-31T12:05:00Z",
                outputBytes: 0,
              },
            ],
            branch: {},
          },
        },
      ],
      branch: {},
    },
  };
  const OUTPUT_DATA = { tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true };

  test("listJobs sends evener/jobs/list with {ref} and returns the raw data field", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/list", () => ({ data: JOBS_DATA }));

    const result = await threadsStore.getState().listJobs("ref_a");

    const call = fake.calls.find((c) => c.method === "evener/jobs/list");
    expect(call?.params).toEqual({ ref: "ref_a" });
    expect(result).toEqual(JOBS_DATA);
  });

  test("listJobs includes continuation only when non-empty and keeps the AppWire method name", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/list", () => ({ data: JOBS_DATA }));

    await threadsStore.getState().listJobs("ref_a", "page-2");
    await threadsStore.getState().listJobs("ref_a", "");

    const calls = fake.calls.filter((c) => c.method === "evener/jobs/list");
    expect(calls).toHaveLength(2);
    expect(calls[0]?.params).toEqual({ ref: "ref_a", continuation: "page-2" });
    expect(calls[1]?.params).toEqual({ ref: "ref_a" });
  });

  test("jobOutput sends evener/jobs/output with {ref, jobId} and returns the raw data field", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/output", () => ({ data: OUTPUT_DATA }));

    const result = await threadsStore.getState().jobOutput("ref_a", "job_1");

    const call = fake.calls.find((c) => c.method === "evener/jobs/output");
    expect(call?.params).toEqual({ ref: "ref_a", jobId: "job_1" });
    expect(result).toEqual(OUTPUT_DATA);
  });

  test("jobOutput passes beforeBytes and maxBytes through only when positive", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/output", () => ({ data: OUTPUT_DATA }));

    await threadsStore.getState().jobOutput("ref_a", "job_1", 64, 256);
    await threadsStore.getState().jobOutput("ref_a", "job_1", 0, 0);

    const calls = fake.calls.filter((c) => c.method === "evener/jobs/output");
    expect(calls).toHaveLength(2);
    expect(calls[0]?.params).toEqual({ ref: "ref_a", jobId: "job_1", beforeBytes: 64, maxBytes: 256 });
    expect(calls[1]?.params).toEqual({ ref: "ref_a", jobId: "job_1" });
  });

  test("both throw when no client has been connected yet", async () => {
    await expect(threadsStore.getState().listJobs("ref_a")).rejects.toThrow(/no client connected/i);
    await expect(threadsStore.getState().jobOutput("ref_a", "job_1")).rejects.toThrow(/no client connected/i);
  });

  // Issue #195's RCA: both are read-only, so they wait out a reconnect (via
  // requireReadyClient, stores/threads.ts) instead of failing with
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection - contrast with the mutation re-pin tests (setModel, send),
  // which must keep rejecting synchronously.
  test("listJobs waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/list", () => ({ data: JOBS_DATA }));

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().listJobs("ref_a");
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
    expect(fake.calls).toHaveLength(0); // never sent while reconnecting

    fake.emitReady();
    await expect(pending).resolves.toEqual(JOBS_DATA);
  });

  test("jobOutput waits out a reconnect instead of rejecting synchronously, then resolves once ready", async () => {
    const fake = connectFakeClient();
    fake.on("evener/jobs/output", () => ({ data: OUTPUT_DATA }));

    fake.emitStateChange("reconnecting");
    const pending = threadsStore.getState().jobOutput("ref_a", "job_1");
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
    expect(settled).toBe(false);
    expect(fake.calls).toHaveLength(0);

    fake.emitReady();
    await expect(pending).resolves.toEqual(OUTPUT_DATA);
  });

  // requireReadyClient always returns a FRESH requireClient() read once
  // ready, never the client it started waiting on - a wait can span a
  // manual retry (shell/ConnectionBanner.tsx) that swaps in a genuinely
  // different client object while the read is still pending.
  test("a rewire mid-wait retries against the new client, not the stale one it started waiting on", async () => {
    const stale = connectFakeClient("reconnecting");
    const pending = threadsStore.getState().listJobs("ref_a");
    await flushUntil(() => false, 5);

    const fresh = new FakeClient("ready");
    fresh.on("evener/jobs/list", () => ({ data: JOBS_DATA }));
    connectionStore.getState().connect(fresh);

    await expect(pending).resolves.toEqual(JOBS_DATA);
    expect(fresh.calls.filter((c) => c.method === "evener/jobs/list")).toHaveLength(1);
    expect(stale.calls).toHaveLength(0); // the stale client never saw this call
  });
});

describe("useThreadsStore.resolveEscalation", () => {
  function threadWithEscalation(ref: string, escalationId: string): ThreadReadResponse {
    return readResponse(ref, {
      evener: {
        ref,
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        pendingEscalations: [
          {
            ref,
            threadId: `thr_${ref}`,
            escalationId,
            mode: "workspace-write",
            tool: "shell",
            kind: "shell",
            deniedPath: "/etc/hosts",
          },
        ],
      },
    });
  }

  test("calls evener/sandbox/escalation/resolve with exact params", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("evener/sandbox/escalation/resolve", () => ({}));

    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);

    const call = fake.calls.find((c) => c.method === "evener/sandbox/escalation/resolve");
    expect(call?.params).toEqual({ ref: "ref_a", escalationId: "esc_1", approve: true });
  });

  test("a successful resolve removes the escalation from the tracked model", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("evener/sandbox/escalation/resolve", () => ({}));

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toHaveLength(1);
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", false);
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a rejected resolve propagates and leaves the model untouched", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("evener/sandbox/escalation/resolve", () => {
      throw new Error("sandbox offline");
    });

    const before = threadsStore.getState().threads.get("ref_a");
    await expect(threadsStore.getState().resolveEscalation("ref_a", "esc_1", true)).rejects.toThrow("sandbox offline");
    expect(threadsStore.getState().threads.get("ref_a")).toBe(before); // same reference: untouched
  });

  test("maps a Conflict wire rejection (evenerErrorInfo === conflict) to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    // A stale/double/raced resolve is surfaced as appwire.Conflict() by the
    // daemon (server/appwire_runtime.go's handleAppSandboxEscalationResolve:
    // "Surface it as a conflict so the client can drop the card rather than
    // retry"). resolve must map it to ConflictError like every other mutating
    // action, so the escalation rail treats it as terminal, not retryable.
    fake.on("evener/sandbox/escalation/resolve", () => {
      throw new WireError("escalation is not pending (unknown or already resolved)", -32013, {
        evenerErrorInfo: "conflict",
      });
    });

    const rejection = threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    await expect(rejection).rejects.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toThrow("already resolved");
  });

  test("does not map a same-code, different-evenerErrorInfo WireError to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    // Same wire code (-32013) but a non-conflict evenerErrorInfo — the
    // discriminator is the evenerErrorInfo string, not the code alone.
    fake.on("evener/sandbox/escalation/resolve", () => {
      throw new WireError("something else", -32013, { evenerErrorInfo: "queuedDrainPartial" });
    });

    const rejection = threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toBeInstanceOf(WireError);
  });

  test("a resolve for an escalation absent from the model is a same-reference no-op", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("evener/sandbox/escalation/resolve", () => ({}));

    const before = threadsStore.getState().threads;
    await threadsStore.getState().resolveEscalation("ref_a", "esc_never_pending", true);
    expect(threadsStore.getState().threads).toBe(before);
  });

  test("updates BOTH threads and watchedThreads when the same ref is tracked in each", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  // Dual-map atomicity (round-2 fix): the escalation clear in threads and the
  // clear in watchedThreads are one setState, so a synchronous subscriber
  // running between the two halves of the update — which the sequential
  // putThreadModel + putWatchedThreadModel pair this replaced made possible —
  // cannot observe threads updated while watchedThreads still holds the
  // escalation. The subscriber records every intermediate snapshot it sees;
  // after the resolve it must have seen exactly the before-state and the
  // after-state, never a mixed one.
  test("resolveEscalation clears both maps in one setState - no synchronous subscriber sees split state", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    fake.on("evener/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    const snapshots: { tracked: number; watched: number }[] = [];
    const unsubscribe = threadsStore.subscribe((state) => {
      snapshots.push({
        tracked: state.threads.get("ref_a")?.pendingEscalations.length ?? -1,
        watched: state.watchedThreads.get("ref_a")?.pendingEscalations.length ?? -1,
      });
    });
    try {
      await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    } finally {
      unsubscribe();
    }

    // Every observed snapshot is consistent: both maps agree on the
    // escalation count (or the model was absent from both, -1). A split
    // update would leave a { tracked: 0, watched: 1 } snapshot behind.
    for (const [i, snap] of snapshots.entries()) {
      expect(snap.tracked, `snapshot ${i} must not be split`).toBe(snap.watched);
    }
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a evener/sandbox/escalation/resolved notification clears the matching card from both tracked and watched models", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toHaveLength(1);

    // The real wire shape (appwire.SandboxEscalationResolved): {threadId, ref,
    // escalationId} — the daemon's broadcast to every OTHER subscribed client
    // when a pending escalation leaves the set (server/appwire_runtime.go's M7
    // fix). Unlike the local resolveEscalation action, this arrives for a
    // resolve some OTHER client made, so a client that only watches the session
    // still drops its now-stale card.
    fake.emitNotification({
      method: "evener/sandbox/escalation/resolved",
      params: { threadId: "thr_ref_a", ref: "ref_a", escalationId: "esc_1" },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });
});

describe("useThreadsStore hook", () => {
  test("reflects store state and updates when the tracked threads map changes", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    const { result } = renderHook(() => useThreadsStore((s) => s.threads.has("ref_a")));
    expect(result.current).toBe(false);

    await act(async () => {
      await threadsStore.getState().ensureThread("ref_a");
    });

    expect(result.current).toBe(true);
  });
});

// appendFrameTime is the pure ring-buffer step behind the frameTimes
// tracking below: expiry (evict anything older than the 60s trace window
// widgets/cadence's own Cadence renders) and the 64-entry cap are both unit
// tested directly here, with no client/store machinery involved.
describe("appendFrameTime", () => {
  test("appends to an empty ring", () => {
    expect(appendFrameTime([], 1000)).toEqual([1000]);
  });

  test("appends after existing entries, preserving order", () => {
    expect(appendFrameTime([100, 200], 300)).toEqual([100, 200, 300]);
  });

  test("keeps an entry exactly at the 60s window boundary (matches Cadence's own age>WINDOW_MS exclusion)", () => {
    const now = 100_000;
    expect(appendFrameTime([now - FRAME_TIMES_WINDOW_MS], now)).toEqual([now - FRAME_TIMES_WINDOW_MS, now]);
  });

  test("evicts entries older than the 60s window relative to now", () => {
    const now = 100_000;
    const times = [now - FRAME_TIMES_WINDOW_MS - 1, now - 60_000, now - 1000];
    expect(appendFrameTime(times, now)).toEqual([now - 60_000, now - 1000, now]);
  });

  test("caps the ring at 64 entries, dropping the oldest to make room", () => {
    const now = 100_000;
    // 64 entries, all well within the window, oldest first.
    const times = Array.from({ length: FRAME_TIMES_MAX_ENTRIES }, (_, i) => now - (FRAME_TIMES_MAX_ENTRIES - i));
    const result = appendFrameTime(times, now);
    expect(result).toHaveLength(FRAME_TIMES_MAX_ENTRIES);
    expect(result[0]).toBe(times[1]); // the single oldest entry was evicted
    expect(result[result.length - 1]).toBe(now); // the new entry always survives
  });

  test("does not cap below 64 entries", () => {
    expect(appendFrameTime([1, 2, 3], 4)).toEqual([1, 2, 3, 4]);
  });

  test("unsorted input is accepted as-is (no re-sort) - Cadence's own contract already tolerates that", () => {
    expect(appendFrameTime([300, 100, 200], 400)).toEqual([300, 100, 200, 400]);
  });
});

describe("frameTimes tracking (threads store)", () => {
  test("starts with no entry for a freshly-hydrated ref - only live notifications populate it", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();
  });

  test("appends the notification's own timestamp on every applied notification - not a fresh Date.now() read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    vi.spyOn(Date, "now").mockReturnValue(5_000_000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });

    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([5_000_000]);
  });

  test("accumulates across multiple applied notifications, in arrival order", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    const dateNowSpy = vi.spyOn(Date, "now");
    for (const t of [1000, 2000, 3000]) {
      dateNowSpy.mockReturnValue(t);
      fake.emitNotification({
        method: "thread/status/changed",
        params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
      });
    }

    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000, 2000, 3000]);
  });

  test("a notification for an untracked ref creates no frameTimes entry", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_tracked"));
    await threadsStore.getState().ensureThread("ref_tracked");

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_untracked", ref: "ref_untracked", status: { type: "active" } },
    });

    expect(threadsStore.getState().frameTimes.has("ref_untracked")).toBe(false);
  });

  test("a matched notification the reducer treats as a same-reference no-op does not append a frame time", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    // An unrecognized method still carries a matching ref (passes
    // notificationTargetsThread) but falls through the reducer's `default:`
    // case, which returns the exact same model reference - handleNotification
    // must not treat that as "applied" for frameTimes purposes either.
    fake.emitUnknownNotification({ method: "totally/unknown", params: { ref: "ref_a" } });

    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();
  });

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

  test("a reconnect re-hydrate does not reset or otherwise touch existing frameTimes", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    vi.spyOn(Date, "now").mockReturnValue(1000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000]);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => fake.calls.filter((c) => c.method === "thread/read").length > 1);

    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000]);
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
      readResponse("ref_a", {
        turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
      }),
    );

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    a.emitNotification({
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 },
      },
    });
    const liveWatched = threadsStore.getState().watchedThreads.get("ref_a");
    expect(liveWatched?.turns.map((turn) => turn.id)).toEqual(["turn_m5", "turn_m6"]);

    const b = new FakeClient("ready");
    const cut = { reached: false };
    let resolveRefresh: ((response: ThreadReadResponse) => void) | null = null;
    b.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (resolveRefresh = resolve)));
    connectionStore.getState().connect(b);
    await flushUntil(() => resolveRefresh !== null);
    const finishRefresh = resolveRefresh as unknown as (response: ThreadReadResponse) => void;
    finishRefresh(
      markResponseCut(
        readResponse("ref_a", {
          turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_m6" },
        }),
        cut,
      ),
    );
    await emitAtResponseCut(
      cut,
      "ref_a",
      () => {
        b.emitNotification({
          method: "item/completed",
          params: {
            threadId: "thr_ref_a",
            ref: "ref_a",
            turnId: "turn_m6",
            item: {
              type: "agentMessage",
              id: "item_assistant_m6",
              turnId: "turn_m6",
              text: "skillguard watched turn complete",
              status: "completed",
            },
          },
        });
        b.emitNotification({
          method: "turn/completed",
          params: {
            threadId: "thr_ref_a",
            ref: "ref_a",
            turn: { id: "turn_m6", status: "completed", itemsView: "" },
          },
        });
      },
      () => threadsStore.getState().watchedThreads.get("ref_a") !== liveWatched,
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
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 },
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
    await flushUntil(() => pending.length === 2);
    expect(pending.map((request) => request.includeTurns)).toEqual([false, true]);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);

    pending[1]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_reconnect_rich", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    pending[0]!.resolve(readResponse("ref_a", { turns: [] }));
    await rich;

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_reconnect_rich");

    threadsStore.getState().releaseWatchedThread("ref_a");
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a new watcher starts a fresh hydrate after the previous lifecycle is released", async () => {
    const fake = connectFakeClient();
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
    fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
    let resolvePage!: (response: ThreadTurnsListResponse) => void;
    fake.on("thread/turns/list", () => new Promise<ThreadTurnsListResponse>((resolve) => (resolvePage = resolve)));
    await threadsStore.getState().ensureThread("ref_a");

    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await flushUntil(() => resolvePage !== undefined);
    fake.emitNotification({
      method: "turn/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turn: { id: "live-turn", status: "inProgress", itemsView: "" },
      },
    });
    resolvePage({
      data: [{ id: "older-turn", status: "completed", itemsView: "full", items: [] }],
      nextCursor: "cursor_0",
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

// requireReadyClient's own bounded wait, exercised directly through a
// read-only action (listJobs stands in for all five - same helper) rather
// than duplicated five times. Real timers elsewhere in this file resolve via
// scripted FakeClient events (emitReady, etc.), never elapsed time, so this
// is the one describe block that needs fake timers to advance past
// requireReadyClient's default 15s budget without the suite actually
// waiting 15 real seconds.
describe("useThreadsStore read-only ready-gating (requireReadyClient)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("explicit refresh keeps one readiness deadline across ready-to-rewire races", async () => {
    let client = connectFakeClient("reconnecting");
    const clients = [client];
    let outcome: unknown;
    const refreshing = threadsStore
      .getState()
      .refreshThread("ref_a")
      .then(
        () => {
          outcome = "resolved";
        },
        (error: unknown) => {
          outcome = error;
        },
      );

    for (let attempt = 0; attempt < 2; attempt += 1) {
      await vi.advanceTimersByTimeAsync(5_000);
      client.emitReady();
      // Ready resolves the helper; the connection changes before the refresh
      // continuation captures the current client and its ready epoch.
      await Promise.resolve();
      client = new FakeClient("reconnecting");
      clients.push(client);
      connectionStore.getState().connect(client);
    }
    await vi.advanceTimersByTimeAsync(5_000);

    expect(outcome).toBeInstanceOf(ClientNotReadyError);
    expect(errorKind(outcome)).toBe("hub-unreachable");
    await refreshing;
    expect(clients.flatMap((candidate) => candidate.calls)).toHaveLength(0);
  });

  test("a read call times out with a classified ClientNotReadyError if the client never becomes ready", async () => {
    connectFakeClient("reconnecting"); // never reaches ready in this test
    const pending = threadsStore.getState().listJobs("ref_a");
    let outcome: { ok: true } | { ok: false; err: unknown } | undefined;
    pending.then(
      () => {
        outcome = { ok: true };
      },
      (err: unknown) => {
        outcome = { ok: false, err };
      },
    );

    await vi.advanceTimersByTimeAsync(15_000);

    expect(outcome?.ok).toBe(false);
    if (outcome?.ok !== false) throw new Error("expected the read call to reject");
    expect(outcome.err).toBeInstanceOf(ClientNotReadyError);
    expect((outcome.err as Error).message).toMatch(/timed out waiting for a ready client after 15000ms/);
    // Distinguishable from AppwireClient's own synchronous rejection text,
    // but still classified as hub-unreachable so it gets the same friendly
    // toast (see stores/activitySummary.ts's refreshRoot).
    expect((outcome.err as Error).message).not.toMatch(/cannot call/);
    expect(errorKind(outcome.err)).toBe("hub-unreachable");
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
      method: "item/completed" as const,
      params: {
        threadId: `thr_${ref}`,
        ref,
        turnId: "turn_1",
        item: {
          type: "commandExecution" as const,
          id: "item_1",
          turnId: "turn_1",
          clientMutationId,
          output: "queued",
          status: "completed" as const,
        },
      },
    };
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

describe("putThreadModel ref invariant (map key === model.ref)", () => {
  // Round-2 fix, second finding: the replace path already threw when a
  // model's ref disagreed with the slot it was filed under; the pure-add
  // path now throws for the same disagreement. Both guards exist for the
  // routing index (see the routing-index describe block above): a model
  // filed under a key its own ref contradicts would mis-route every
  // ref-routed frame for that key, so it fails loudly at the membership
  // boundary instead.
  test("adding a model whose ref disagrees with the map key throws", () => {
    const model = hydrateThread(readResponse("ref_other"), "ref_other", Date.now());
    expect(() => putThreadModel("ref_a", model)).toThrow(/map key and model\.ref must agree/);
    // Nothing was filed: the store keeps whatever it had at that key.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("replacing a model whose ref disagrees with the map key still throws (pre-existing guard)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    const moved = hydrateThread(readResponse("ref_moved"), "ref_moved", Date.now());
    expect(() => putThreadModel("ref_a", moved)).toThrow(/map key and model\.ref must agree/);
    // The tracked model is untouched by the rejected put.
    expect(threadsStore.getState().threads.get("ref_a")?.ref).toBe("ref_a");
  });

  test("an agreeing add still lands (guard does not reject the legitimate path)", () => {
    const model = hydrateThread(readResponse("ref_a"), "ref_a", Date.now());
    putThreadModel("ref_a", model);
    expect(threadsStore.getState().threads.get("ref_a")?.ref).toBe("ref_a");
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

test("a message composed during a saved read dispatches its first delivery", async () => {
  const fake = connectFakeClient("connecting");
  const saved = deferred<ThreadReadResponse>();
  fake.on("thread/read", () => saved.promise);
  const delivered = deferred<void>();
  fake.on("turn/queue", (params) => {
    delivered.resolve();
    return { receipt: mutationReceipt(params.clientMutationId) };
  });
  fake.emitReady();
  const hydration = threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().queue("ref_a", "new message during hydration");
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  saved.resolve(readResponse("ref_a", { status: { type: "notLoaded" } }));
  await hydration;
  await delivered.promise;
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
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
  reloaded.on("thread/read", () =>
    readResponse("ref_a", {
      status: { type: resumed ? "idle" : "notLoaded" },
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, clientMutationIds: resumed ? [record.clientMutationId] : [] },
      },
    }),
  );
  reloaded.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  reloaded.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  expect((await reloadedStorage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
  expect(reloaded.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  resumed = true;
  await threadsStore.getState().refreshThread("ref_a");
  expect(await reloadedStorage.getOutbox(record.clientMutationId)).toBeUndefined();
  expect(reloaded.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
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

test("clear permits explicit fresh recovery without replaying old-instance input", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const saved = readResponse("ref_a", { status: { type: "notLoaded" } });
  saved.thread.evener.mutationStateAuthoritative = false;
  const replacement = testThread("ref_a", { id: "replacement" });
  let cleared = false;
  fake.on("thread/read", () => (cleared ? { thread: replacement } : saved));
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
  await vi.waitFor(async () => {
    expect(await storage.getRecovery(record.clientMutationId)).toMatchObject({
      recoveryKind: "rejected",
      payload: { expectedInstanceId: "thr_ref_a" },
    });
  });
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
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
    storage.close();

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
  // enqueue fails, so a capture that rejects - a stalled read's
  // MutationStorageTimeoutError, a VersionError from a versionchange, a
  // retired connection - aborted the submission AND left the ref armed in
  // dispatchableMutationRefs with no durable row behind it. A later
  // discovery pass that names the ref then ran its dispatch work anyway,
  // for a ref with nothing of its own to dispatch. The failed capture must
  // be treated exactly like the failed enqueue it precedes: disarmed on the
  // way out.
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

      // The capture fails the way a stalled or retired connection does.
      storage.readStopEpoch = async () => {
        throw new MutationStorageTimeoutError();
      };

      // The submission aborts and leaves nothing durable behind.
      await expect(threadsStore.getState().queue("ref_a", "past a stalled capture")).rejects.toThrow(
        MutationStorageTimeoutError,
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
    storage.close();

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
