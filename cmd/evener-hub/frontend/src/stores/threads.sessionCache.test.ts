// The cached-shell load seam (web session-history cache plan, Task 5; spec
// docs/superpowers/specs/2026-09-29-web-session-cache-design.md, "The load
// seam: the cached shell"): the bounded lookup on ensureThread, the shell it
// paints before the read resolves, the lease epoch it captures, and the
// races the spec's scenario 13 concedes — the shared-lookup join (Review
// Focus 5), the release during the lookup, and the lookup that lost its own
// deadline race.
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  CachedSessionRecord,
  HistoryChanges,
  MethodName,
  MethodTypes,
  QueueState,
  SnapshotIdentity,
  Thread,
  ThreadCapabilities,
  ThreadClearResponse,
  ThreadModel,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  Turn,
  TurnModel,
  TurnQueueResponse,
} from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deleteSession } from "../shell/rail/actions";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { clearProjectionWorkForTests, settleProjectionWorkForTests } from "./projectionWork";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import {
  bumpCacheEpochRow,
  deleteIndexedDatabase,
  holdNextWriteTransaction,
  neverSettlingFactory,
} from "./testing/stalledIndexedDB";
import {
  clearCachedSessions,
  installHydrationRetrySchedulerForTests,
  markCacheSessionsDeleted,
  resetThreadsStoreForTests,
  setCacheChannelFactoryForTests,
  setCacheWriteTimersForTests,
  setSessionCacheAdapterForTests,
  threadsStore,
} from "./threads";

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

// The response-level overrides a read fixture can carry besides the thread's
// own fields (threads.test.ts's readResponse shape plus the v6 identity a
// cached shell's reconciling read answers with).
type ReadResponseOverrides = {
  bootGeneration?: string;
  epoch?: number;
  snapshot?: SnapshotIdentity;
  requestGeneration?: number;
  olderCursor?: string;
  changes?: HistoryChanges;
};

// The default v6 identity every fixture in this file agrees on: boot
// generation "1", epoch 1, incarnation "inc-1", length 40 — the same
// identity seedCache records, so a readResponse() out of the box merges with
// a shell built from the default seed.
function readResponse(ref: string, overrides: ReadResponseOverrides & TestThreadOverrides = {}): ThreadReadResponse {
  const { bootGeneration, epoch, snapshot, requestGeneration, olderCursor, changes, ...threadOverrides } = overrides;
  return {
    thread: testThread(ref, threadOverrides),
    bootGeneration: bootGeneration ?? "1",
    epoch: epoch ?? 1,
    snapshot: snapshot ?? { incarnation: "inc-1", length: 40 },
    requestGeneration: requestGeneration ?? 1,
    ...(olderCursor === undefined ? {} : { olderCursor }),
    ...(changes === undefined ? {} : { changes }),
  };
}

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

// A recorded turn with one positioned item: the anchor the shell's record
// carries (and what newestItemPosition reads from it) needs a position, and
// the replay assertion reads the turn id. The shape satisfies both the model
// (TurnModel — the record's history turns) and the wire (Turn — a read
// response's window turns and a history/updated fold) so one fixture serves
// the seed, the read, and the notification.
function turnFixture(id: string, text = "cached turn"): TurnModel & Turn {
  return {
    id,
    status: "completed",
    itemsView: "full",
    items: [{ id: `item_${id}`, turnId: id, type: "agentMessage", text, position: { entry: 1, item: 0 } }],
    version: 2,
  };
}

interface SeedOverrides {
  incarnation?: string;
  length?: number;
  turns?: TurnModel[];
  olderCursor?: string;
  bootGeneration?: string;
  epoch?: number;
  appliedGeneration?: number;
  issuedGeneration?: number;
}

// seededRecord is the record literal every seed in this file agrees on: the
// shape the store's own write seam persists, threadId pinned to "thr_1" (not
// the wire fixture's thr_<ref> derivation) because the replay assertion fences
// a Clear on the CACHED thread id — the record is the authority the shell
// paints, so its id is the one the test names.
function seededRecord(ref: string, overrides: SeedOverrides): CachedSessionRecord {
  return {
    ref,
    threadId: "thr_1",
    name: "cached session",
    modelProvider: "anthropic/claude-sonnet-4-5",
    model: "anthropic/claude-sonnet-4-5",
    olderCursor: overrides.olderCursor ?? "cur-1",
    savedAt: Date.now(),
    history: {
      bootGeneration: overrides.bootGeneration ?? "1",
      epoch: overrides.epoch ?? 1,
      incarnation: overrides.incarnation ?? "inc-1",
      length: overrides.length ?? 40,
      appliedGeneration: overrides.appliedGeneration ?? 0,
      issuedGeneration: overrides.issuedGeneration ?? 0,
      turns: overrides.turns ?? [turnFixture("turn_1")],
    },
  };
}

// seedCache writes a CachedSessionRecord through a SessionCacheIndexedDB on
// the global fake factory — until Task 6 lands the debounced write seam this
// is how a "prior tab's" record exists at all.
async function seedCache(adapter: SessionCacheIndexedDB, ref: string, overrides: SeedOverrides = {}): Promise<void> {
  const record = seededRecord(ref, overrides);
  const outcome = await adapter.put(record, 0, record.savedAt);
  expect(outcome.outcome).toBe("written");
}

// nextHandledRequest resolves with the params of the first request to
// `method` — same contract as threads.test.ts's helper; a second request
// re-resolves it to no effect, so the count assertions (not this promise)
// are what pins "exactly one".
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

// connectFakeClient wires a fresh FakeClient through the locked connect()
// entry point — the same path threads.ts's requireClient() rides. The file's
// notification helpers emit through the last client connected here.
let connectedFakeClient: FakeClient | undefined;

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  connectedFakeClient = fake;
  return fake;
}

function threadInstanceIDForTest(model: ThreadModel): string {
  return model.instanceId ?? model.threadId;
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

function installWedgedCacheAdapter(): void {
  setSessionCacheAdapterForTests(new SessionCacheIndexedDB({ indexedDB: neverSettlingFactory() }));
}

function restoreCacheAdapter(): void {
  setSessionCacheAdapterForTests(undefined);
  installedCacheAdapter = undefined;
}

// The adapter a test installed on the singleton seam for the write seam's own
// storage universe (the oversize fixture's private IDBFactory is not the
// default one); undefined when the test rides the module adapter and the
// default database.
let installedCacheAdapter: SessionCacheIndexedDB | undefined;

function installCacheAdapter(adapter: SessionCacheIndexedDB): void {
  installedCacheAdapter = adapter;
  beds.push(adapter);
  setSessionCacheAdapterForTests(adapter);
}

// cacheRecord reads the adapter's stored record for assertions: the installed
// adapter when a test installed one, otherwise a throwaway reader over the
// default factory — the same database the module adapter writes. The read's
// readwrite transaction queues behind any in-flight put on the same stores,
// so a record asserted here is one whose write already committed.
async function cacheRecord(ref: string): Promise<CachedSessionRecord | undefined> {
  const reader = installedCacheAdapter ?? new SessionCacheIndexedDB();
  try {
    const found = await reader.get(ref, Date.now());
    return found?.record;
  } finally {
    if (reader !== installedCacheAdapter) reader.close();
  }
}

// echoingReadHandler answers a thread/read with the file's fixture, echoing
// the request's generation: the store's latest-window contract discards a
// response to a superseded generation, and both a resync and the
// stale-cursor retry re-issue with a bumped one.
function echoingReadHandler(
  overrides: ReadResponseOverrides & TestThreadOverrides = {},
): RequestHandler<"thread/read"> {
  return (params) => {
    if (params.ref === undefined) throw new Error("thread/read params must carry a ref");
    return readResponse(params.ref, { ...overrides, requestGeneration: params.requestGeneration });
  };
}

// The connected client a notification helper emits through; a missing client
// or model is a fixture wiring bug, not an assertion outcome.
function requireConnectedClient(caller: string): FakeClient {
  const client = connectedFakeClient;
  if (client === undefined) throw new Error(`${caller}: no client is connected`);
  return client;
}

function requireOpenHistory(
  caller: string,
  ref: string,
): { model: ThreadModel; history: NonNullable<ThreadModel["history"]> } {
  const model = threadsStore.getState().threads.get(ref);
  if (model === undefined) throw new Error(`${caller}: no open model for ${ref}`);
  const history = model.history;
  if (history === undefined) throw new Error(`${caller}: ${ref} holds no versioned history`);
  return { model, history };
}

// emitHistoryUpdated pushes a history/updated fold through the connected fake
// client's notification path — the reducer's live merge, the publication route
// the subscription exists to see (the notification handler's own setState,
// never putThreadModel). An explicit `entry` positions the fold's item the
// way the daemon's own pushes do: the turn scalar in `turns` and the item —
// the position's only carrier — in top-level `items` (the reducer's live
// merge reads exactly that split; an item nested inside the turn is dropped
// and the fold would sort by its version surrogate instead of its entry).
// Without an entry the fold keeps the file's original shape, exactly as
// Tasks 5-6 emitted it.
function emitHistoryUpdated(ref: string, options: { fold: string; entry?: number }): void {
  const client = requireConnectedClient("emitHistoryUpdated");
  const { model, history } = requireOpenHistory("emitHistoryUpdated", ref);
  client.emitNotification({
    method: "history/updated",
    params: {
      threadId: model.threadId,
      ref,
      bootGeneration: history.bootGeneration,
      epoch: history.epoch,
      snapshot: { incarnation: history.incarnation ?? "inc-1", length: history.length },
      ...(options.entry === undefined
        ? { turns: [turnFixture(options.fold, "folded live")] }
        : {
            turns: [{ id: options.fold, status: "completed", itemsView: "full", version: 2 }],
            items: [
              {
                type: "agentMessage",
                id: `item_${options.fold}`,
                turnId: options.fold,
                text: "folded live",
                status: "completed",
                position: { entry: options.entry, item: 0 },
              },
            ],
          }),
    },
  } as AnyNotification);
}

// emitAppliedQueuedNotification carries a clientMutationId on a
// history/updated item — the only trigger that settles a durable row (and so
// unpins its ref) without a successful authoritative read
// (threads.test.ts's appliedItemNotification contract).
function emitAppliedQueuedNotification(ref: string, clientMutationId: string): void {
  const client = requireConnectedClient("emitAppliedQueuedNotification");
  const { model, history } = requireOpenHistory("emitAppliedQueuedNotification", ref);
  client.emitNotification({
    method: "history/updated",
    params: {
      threadId: model.threadId,
      ref,
      bootGeneration: history.bootGeneration,
      epoch: history.epoch,
      snapshot: { incarnation: history.incarnation ?? "inc-1", length: history.length },
      items: [
        {
          type: "commandExecution",
          id: `item_${clientMutationId}`,
          turnId: "turn_drain",
          clientMutationId,
          output: "queued",
          status: "completed",
        },
      ],
    },
  } as AnyNotification);
}

// Subscription awaits, not polls: each resolves exactly when the store's own
// publication or removal runs (the retirement suite's awaitHydrationBump
// idiom), which is what lets a test hold a deterministic position between a
// notification it emits and the model state it asserts — under fake timers,
// where vi.waitFor's own polling clock never ticks.
function nextModelPublished(ref: string): Promise<void> {
  if (threadsStore.getState().threads.has(ref)) return Promise.resolve();
  return new Promise((resolve) => {
    const unsubscribe = threadsStore.subscribe((state) => {
      if (!state.threads.has(ref)) return;
      unsubscribe();
      resolve();
    });
  });
}

function nextModelRemoved(ref: string): Promise<void> {
  if (!threadsStore.getState().threads.has(ref)) return Promise.resolve();
  return new Promise((resolve) => {
    const unsubscribe = threadsStore.subscribe((state) => {
      if (state.threads.has(ref)) return;
      unsubscribe();
      resolve();
    });
  });
}

// resolveEverything settles what an awaited ensureThread leaves in flight —
// the load seam's bounded lookup and any IndexedDB delivery still queued — so
// the publication the read produced is fully on the store before the test
// advances the debounce.
async function resolveEverything(_fake: FakeClient): Promise<void> {
  // The tracker's settle waits for in-flight storage work by yielding through
  // a MessageChannel hop, which the scoped fake timers above do not fake —
  // so it both flushes the load seam's own lookup transaction and lets every
  // queued IndexedDB delivery land, which nextMacrotask (a faked setTimeout)
  // could not do here.
  await settleProjectionWorkForTests();
}

// scriptStaleSnapshotRetry mirrors threads.history.test.ts's below-floor
// fixture: the next read that carries a held snapshot is answered with the
// hub's TranscriptItemCursorStale rejection, and the retry the store issues
// without it is answered with `overrides` (echoing the retry's generation).
function scriptStaleSnapshotRetry(fake: FakeClient, overrides: ReadResponseOverrides & TestThreadOverrides): void {
  fake.on("thread/read", (params) => {
    if (params.heldSnapshot !== undefined) {
      throw new WireError("stale", -32000, { evenerErrorInfo: "transcriptItemCursorStale" });
    }
    if (params.ref === undefined) throw new Error("thread/read params must carry a ref");
    return readResponse(params.ref, { ...overrides, requestGeneration: params.requestGeneration });
  });
}

// driveResyncRead re-issues the ref's latest-window read the way the store's
// invalidation path does — the same driver threads.history.test.ts's
// stale-cursor tests use (refreshThread), never a second read mechanism.
async function driveResyncRead(ref: string): Promise<void> {
  await threadsStore.getState().refreshThread(ref);
}

// Hold delivery after the real lookup commits. Holding the native complete
// event also traps fake-indexeddb's internal queue wakeup, which can strand
// an unrelated clear when the corrected lifetime gate refuses all writes.
class GatedCacheAdapter extends SessionCacheIndexedDB {
  readonly #gate: Promise<void>;

  constructor(gate: Promise<void>) {
    super();
    this.#gate = gate;
  }

  override async get(ref: string, now: number): Promise<{ record: CachedSessionRecord; epoch: number } | undefined> {
    const found = await super.get(ref, now);
    await this.#gate;
    return found;
  }
}

interface CacheTestBed {
  adapter: SessionCacheIndexedDB;
  /** Opens the parked lookup: the lookup settles and the read it arms can proceed. */
  gate: { release(): void };
  /** The late-race name for the same release: the parked lookup answers after its deadline already lost. */
  lateGate: { settle(): void };
}

// Every adapter a test installed on the singleton seam, closed in afterEach
// so the next beforeEach's cache-database deletion never fires "blocked".
const beds: SessionCacheIndexedDB[] = [];

// cacheTestBed installs a cache adapter on the store's singleton seam and
// returns it with the gate. The gate exists only for { gated: true }: the
// replay path must NOT park its own lookup, so the default bed arms nothing.
function cacheTestBed(options: { gated?: boolean } = {}): CacheTestBed {
  let release: (() => void) | undefined;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const adapter = options.gated ? new GatedCacheAdapter(gate) : new SessionCacheIndexedDB();
  setSessionCacheAdapterForTests(adapter);
  beds.push(adapter);
  const settle = () => release?.();
  return { adapter, gate: { release: settle }, lateGate: { settle } };
}

interface ReloadOptions {
  failFirstRead?: boolean;
  historyFailed?: boolean;
}

let pendingReadRef: string | undefined;
let pendingReadGeneration: number | undefined;
let resolvePendingReadFn: ((response: ThreadReadResponse) => void) | undefined;
const readArmedWaiters: Array<() => void> = [];

// awaitReadArmed resolves on the scripted read handler's next invocation —
// the deterministic witness that a read actually reached the wire, which a
// retry arms only after its own await chain (never synchronously inside
// runScheduledHydrationRetryForThisFile).
function awaitReadArmed(): Promise<void> {
  return new Promise((resolve) => {
    readArmedWaiters.push(resolve);
  });
}

function notifyReadArmed(): void {
  for (const resolve of readArmedWaiters.splice(0)) resolve();
}

// seedAndReload is Task 5's replay bed as the write seam's fixture: a prior
// tab's record is seeded, the store reloads against a scripted read, and the
// cached shell publishes before that read resolves. The seeded row is then
// deleted from storage: the shell's model is already in memory, and without
// the deletion no read-back could tell "the shell publication wrote nothing"
// apart from "it rewrote the seed" — the assertion the shell gate's tests
// stand on.
async function seedAndReload(ref: string, options: ReloadOptions = {}): Promise<void> {
  const { adapter } = cacheTestBed();
  await seedCache(adapter, ref);
  const fake = connectFakeClient();
  let reads = 0;
  const firstReadArmed = awaitReadArmed();
  fake.on(
    "thread/read",
    (params) =>
      new Promise<ThreadReadResponse>((resolve, reject) => {
        reads += 1;
        notifyReadArmed();
        if (reads === 1 && options.historyFailed === true) {
          reject(new WireError("history failed", -32000, { evenerErrorInfo: "transcriptHistoryFailed" }));
          return;
        }
        if (reads === 1 && options.failFirstRead === true) {
          reject(new Error("transport: read failed"));
          return;
        }
        pendingReadRef = ref;
        pendingReadGeneration = params.requestGeneration;
        resolvePendingReadFn = resolve;
      }),
  );
  const pending = threadsStore.getState().ensureThread(ref);
  // The fixture owns the pane claim's failure modes (its reads are scripted);
  // a rejection it did not script surfaces through the assertions below.
  void pending.catch(() => {});
  await nextModelPublished(ref);
  await firstReadArmed; // the read is armed the moment the shell publishes
  if (threadsStore.getState().cacheShellRefs.has(ref) !== true) {
    throw new Error(`seedAndReload: ${ref} must publish as a cached shell`);
  }
  const readSettledInFailure = options.failFirstRead === true || options.historyFailed === true;
  if (!readSettledInFailure && resolvePendingReadFn === undefined) {
    throw new Error("seedAndReload: the read must stay pending");
  }
  await adapter.deleteRecords([ref]);
}

// resolvePendingRead answers the deferred read with the file's default
// identity (the seed's own), echoing the request's generation so the answer
// merges rather than being discarded as superseded.
function resolvePendingRead(overrides: ReadResponseOverrides & TestThreadOverrides = {}): void {
  const resolve = resolvePendingReadFn;
  if (resolve === undefined) throw new Error("resolvePendingRead: no read is pending");
  const ref = pendingReadRef;
  if (ref === undefined) throw new Error("resolvePendingRead: no ref captured for the pending read");
  resolvePendingReadFn = undefined;
  resolve(readResponse(ref, { ...overrides, requestGeneration: pendingReadGeneration }));
}

// The hydration retry scheduler is injected so every retry in this suite is
// driven by an explicit call, never by elapsed time (threads.test.ts's own
// rationale): a scripted failed read arms a retry this file invokes by hand,
// and installing it for every test keeps the production backoff's real
// setTimeout out of the suite entirely.
interface ScheduledHydrationRetry {
  attempt: number;
  retry: () => void;
  cancelled: boolean;
}

let scheduledHydrationRetries: ScheduledHydrationRetry[] = [];

// runScheduledHydrationRetryForThisFile invokes exactly one scheduled retry,
// proving first that it exists and was not cancelled — the same guard
// threads.test.ts's own driver keeps, so a cancelled entry that still fires
// cannot make an assertion vacuous.
function runScheduledHydrationRetryForThisFile(index = 0): void {
  const scheduled = scheduledHydrationRetries[index];
  expect(scheduled, `no hydration retry scheduled at index ${index}`).toBeDefined();
  expect(scheduled?.cancelled).toBe(false);
  scheduled?.retry();
}

let restoreHydrationRetryScheduler: (() => void) | null = null;

beforeEach(async () => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  connectedFakeClient = undefined;
  installedCacheAdapter = undefined;
  pendingReadRef = undefined;
  pendingReadGeneration = undefined;
  resolvePendingReadFn = undefined;
  pendingReloadResolve = undefined;
  pendingReloadGeneration = undefined;
  for (const resolve of readArmedWaiters.splice(0)) resolve();
  scheduledHydrationRetries = [];
  restoreHydrationRetryScheduler = installHydrationRetrySchedulerForTests((attempt, retry) => {
    const scheduled: ScheduledHydrationRetry = { attempt, retry, cancelled: false };
    scheduledHydrationRetries.push(scheduled);
    return () => {
      scheduled.cancelled = true;
    };
  });
  await deleteMutationDatabase();
  await deleteIndexedDatabase("evener-session-cache");
});

afterEach(() => {
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  for (const adapter of beds.splice(0)) adapter.close();
  vi.restoreAllMocks();
});

describe("cached shell load seam", () => {
  it("replay: the reload paints the recorded turns before the read resolves, then the read carries the record's heldSnapshot and a higher generation", async () => {
    // First life: a prior tab's record exists in storage, so this test still
    // writes it through the adapter directly (the write seam's own tests
    // below exercise the live path); then reload.
    const { adapter } = cacheTestBed();
    await seedCache(adapter, "local:thr_1", {
      incarnation: "inc-1",
      length: 40,
      turns: [turnFixture("turn_1")],
      olderCursor: "cur-1",
    });
    // Reload: fresh store state, same database, a scripted read that stays pending.
    const fake = connectFakeClient();
    let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );
    const pending = threadsStore.getState().ensureThread("local:thr_1");
    await vi.waitFor(() => expect(threadsStore.getState().threads.get("local:thr_1")).toBeDefined());
    const shell = threadsStore.getState().threads.get("local:thr_1");
    if (shell === undefined) throw new Error("the cached shell must publish before the read resolves");
    expect(shell.history?.turns.map((t) => t.id)).toEqual(["turn_1"]); // painted from the cache
    expect(shell.capabilities.send).toBe(false); // the empty set admits nothing
    expect(threadInstanceIDForTest(shell)).toBe("thr_1"); // non-gated actions fence on the cached threadId
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(true);
    expect(shell.history?.issuedGeneration).toBeGreaterThan(0); // the bumped base, not the raw record
    const call = fake.calls.find((c) => c.method === "thread/read");
    if (call === undefined) throw new Error("the read must be armed once the shell publishes");
    const params = call.params as { heldSnapshot?: SnapshotIdentity; requestGeneration?: number };
    expect(params.heldSnapshot).toEqual({ incarnation: "inc-1", length: 40 }); // the read carries the held identity
    const appliedGeneration = shell.history?.appliedGeneration ?? 0;
    expect(params.requestGeneration).toBeGreaterThan(appliedGeneration);
    if (resolveRead === undefined) throw new Error("the read must be in flight while the shell is published");
    resolveRead(
      readResponse("local:thr_1", {
        snapshot: { incarnation: "inc-1", length: 40 },
        requestGeneration: params.requestGeneration,
        changes: { turns: [], items: [] },
      }),
    );
    await pending;
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false); // the first authoritative read cleared the flag
  });

  it("miss: an absent ref arms the ordinary cold hydration exactly as today (no heldSnapshot)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) =>
      readResponse((params as { ref: string }).ref, { snapshot: { incarnation: "inc-1", length: 1 } }),
    );
    await threadsStore.getState().ensureThread("local:cold");
    const call = fake.calls.find((c) => c.method === "thread/read");
    expect((call?.params as { heldSnapshot?: unknown } | undefined)?.heldSnapshot).toBeUndefined();
  });

  it("deadline: a wedged open resolves the lookup as a miss within 250 ms and the hydration proceeds cold", async () => {
    // Only the clock functions are faked: fake-indexeddb delivers every
    // IndexedDB event on setImmediate, which a bare useFakeTimers() fakes
    // too — freezing the mutation outbox's post-publish work mid-test. The
    // toFake scoping is mutationOutboxIndexedDB.stall.test.ts's own pattern.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      installWedgedCacheAdapter(); // neverSettlingRequest factory on the singleton seam
      const fake = connectFakeClient();
      let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
      fake.on(
        "thread/read",
        () =>
          new Promise<ThreadReadResponse>((resolve) => {
            resolveRead = resolve;
          }),
      );
      const pending = threadsStore.getState().ensureThread("local:thr_1");
      await vi.advanceTimersByTimeAsync(250); // the bound: no pane waits longer
      if (resolveRead === undefined) throw new Error("the cold read must be armed within the bound");
      resolveRead(readResponse("local:thr_1"));
      await pending;
      const call = fake.calls.find((c) => c.method === "thread/read");
      expect((call?.params as { heldSnapshot?: unknown } | undefined)?.heldSnapshot).toBeUndefined();
    } finally {
      vi.useRealTimers();
      // The wedged lookup's open never settles, so the storage work it
      // registered can never settle either: forget it (the tracker's own
      // reset contract) or every later settle in this file spins on a
      // registration whose result no longer matters — its ref is gone.
      clearProjectionWorkForTests();
      restoreCacheAdapter();
    }
  });

  it("join: a concurrent second ensureThread joins the shared lookup and never double-arms (Review Focus 5)", async () => {
    const { gate, adapter } = cacheTestBed({ gated: true });
    await seedCache(adapter, "local:thr_1");
    const fake = connectFakeClient();
    let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
    const readArmed = nextHandledRequest(
      fake,
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );
    const first = threadsStore.getState().ensureThread("local:thr_1");
    const second = threadsStore.getState().ensureThread("local:thr_1");
    gate.release(); // let the shared lookup settle
    // The read arms only after the lookup settles, a real-task chain — await
    // its arming before resolving it.
    const params = (await readArmed) as { heldSnapshot?: SnapshotIdentity };
    expect(params.heldSnapshot).toEqual({ incarnation: "inc-1", length: 40 }); // the creator's shell armed this read
    if (resolveRead === undefined) throw new Error("the shared lookup must arm exactly one read");
    resolveRead(readResponse("local:thr_1"));
    await Promise.all([first, second]);
    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1); // one hydration, one read
  });

  it("join after the deadline: a second ensureThread arriving once the lookup lost its own race joins the pending hydration (Review Focus 5)", async () => {
    // The plan's Review Focus 5 case, literally (plan line 37): the second
    // caller arrives AFTER the 250 ms deadline resolved the wedged lookup as
    // a miss, so the shared lookup is already gone — what must join is the
    // cold hydration the first caller armed (ensureThread's inflightHydrates
    // guard skips the lookup block and awaits the shared hydration), never a
    // second wedged open. The spied get is the witness: joinCacheLookup is
    // exactly one adapter.get per lookup, so a second lookup would be a
    // second get call.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const wedged = new SessionCacheIndexedDB({ indexedDB: neverSettlingFactory() });
      const getSpy = vi.spyOn(wedged, "get");
      setSessionCacheAdapterForTests(wedged);
      const fake = connectFakeClient();
      let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
      let readGeneration: number | undefined;
      fake.on("thread/read", (params) => {
        readGeneration = params.requestGeneration;
        return new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        });
      });
      const first = threadsStore.getState().ensureThread("local:thr_1");
      await vi.advanceTimersByTimeAsync(250); // the lookup loses its own deadline race; the cold read arms parked
      if (resolveRead === undefined) throw new Error("the cold read must be armed within the bound");
      expect(getSpy).toHaveBeenCalledTimes(1); // the first caller's lookup — the only adapter.get so far
      const second = threadsStore.getState().ensureThread("local:thr_1"); // arrives after the race was lost
      expect(getSpy).toHaveBeenCalledTimes(1); // joined the pending hydration, never a second wedged lookup
      resolveRead(readResponse("local:thr_1", { requestGeneration: readGeneration }));
      // Both callers settle from the one read. A second caller that had
      // started its own wedged lookup would still be parked behind a fresh
      // 250 ms deadline this test never advances — the await would hang.
      await Promise.all([first, second]);
      expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1); // never double-armed
      expect(threadsStore.getState().threads.get("local:thr_1")?.history?.incarnation).toBe("inc-1"); // the one read published
      expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false); // the cold path: no shell ever
    } finally {
      vi.useRealTimers();
      // The wedged lookup's open never settles, so the storage work it
      // registered can never settle either: forget it (the tracker's own
      // reset contract) — the deadline test's own cleanup discipline.
      clearProjectionWorkForTests();
      restoreCacheAdapter();
    }
  });

  it("release during the lookup publishes nothing", async () => {
    const { gate, adapter } = cacheTestBed({ gated: true });
    await seedCache(adapter, "local:thr_1");
    const fake = connectFakeClient();
    // The read handler answers immediately: if the released pane wrongly arms
    // a cold read, the answer settles the caller at once — the assertion
    // below fails fast on the recorded call instead of hanging on a parked
    // response nothing will ever resolve.
    fake.on("thread/read", () => readResponse("local:thr_1"));
    const first = threadsStore.getState().ensureThread("local:thr_1");
    threadsStore.getState().releaseThread("local:thr_1"); // refcount zero while the lookup is in flight
    gate.release();
    await first;
    expect(threadsStore.getState().threads.has("local:thr_1")).toBe(false); // never published, never armed
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false);
    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(0); // a closed pane sends no read
  });

  it("release after the shell published drops the shell-only cache metadata with the model", async () => {
    const { adapter } = cacheTestBed();
    await seedCache(adapter, "local:thr_1");
    const fake = connectFakeClient();
    let resolveRead: ((value: ThreadReadResponse) => void) | undefined;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );
    const pending = threadsStore.getState().ensureThread("local:thr_1");
    await vi.waitFor(() => expect(threadsStore.getState().threads.get("local:thr_1")).toBeDefined());
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(true); // the shell window is open
    expect(threadsStore.getState().cacheAnchors.has("local:thr_1")).toBe(true);
    threadsStore.getState().releaseThread("local:thr_1"); // final release before the authoritative read resolves
    expect(threadsStore.getState().threads.has("local:thr_1")).toBe(false); // the model is gone
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false); // and no unverified shell remains named
    expect(threadsStore.getState().cacheAnchors.has("local:thr_1")).toBe(false); // the anchor went with it
    expect(threadsStore.getState().cacheLeases.has("local:thr_1")).toBe(false); // Task 10: the lease ends with the same final release, so the clear's suppression decays with it
    if (resolveRead === undefined) throw new Error("the read must still be parked at release");
    resolveRead(readResponse("local:thr_1"));
    await pending; // the refused publish settles the owner without resurrecting anything
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false);
    expect(threadsStore.getState().cacheAnchors.has("local:thr_1")).toBe(false);
  });

  it("a lookup that lost its own deadline race resolves late and publishes nothing", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { lateGate, adapter } = cacheTestBed({ gated: true });
      await seedCache(adapter, "local:thr_1"); // the record exists, so the late answer is a real hit to discard
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
      const pending = threadsStore.getState().ensureThread("local:thr_1");
      await vi.advanceTimersByTimeAsync(250);
      lateGate.settle(); // the record arrives after the race was lost
      await pending;
      const model = threadsStore.getState().threads.get("local:thr_1");
      expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false); // the shell never published
      expect(model?.history?.incarnation).toBe(readResponse("local:thr_1").snapshot?.incarnation); // the cold path won: ...
      expect(model?.history?.issuedGeneration).toBe(readResponse("local:thr_1").requestGeneration ?? 1); // ...from the fresh read, not the record
    } finally {
      vi.useRealTimers();
    }
  });

  it("a ready transition during a missed lookup claims at the observed epoch and re-arms epoch-current (the Session handshake)", async () => {
    const { gate } = cacheTestBed({ gated: true }); // no record: the lookup misses
    const fake = connectFakeClient();
    let reads = 0;
    let resolveFirst: ((value: ThreadReadResponse) => void) | undefined;
    let resolveSecond: ((value: ThreadReadResponse) => void) | undefined;
    fake.on("thread/read", () => {
      reads += 1;
      return reads === 1
        ? new Promise<ThreadReadResponse>((resolve) => {
            resolveFirst = resolve;
          })
        : new Promise<ThreadReadResponse>((resolve) => {
            resolveSecond = resolve;
          });
    });
    const pending = threadsStore.getState().ensureThread("local:thr_1");
    // The ready transition lands while the lookup is parked: the ready pass
    // sees no pending hydration (the claim is behind the lookup), so the
    // epoch-current replacement can only come from the claim's own ladder.
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    gate.release();
    await vi.waitFor(() => {
      expect(reads).toBe(1);
    });
    const firstParams = fake.calls[0]?.params as { heldSnapshot?: unknown };
    expect(firstParams.heldSnapshot).toBeUndefined(); // the missed lookup armed a cold claim
    if (resolveFirst === undefined) throw new Error("the claim read must be armed once the lookup settled");
    resolveFirst(readResponse("local:thr_1")); // its publish is refused: the epoch moved past the claim
    await vi.waitFor(() => {
      expect(reads).toBe(2);
    });
    if (resolveSecond === undefined) throw new Error("the refused claim must be replaced by an epoch-current read");
    resolveSecond(readResponse("local:thr_1"));
    await pending;
    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(2);
    const model = threadsStore.getState().threads.get("local:thr_1");
    expect(model?.history?.incarnation).toBe("inc-1"); // the epoch-current read published
    expect(model?.capabilities.send).toBe(true); // not a shell: the cold path's read carries real capabilities
  });

  it("a ready transition during a hit publishes the shell and arms it epoch-current, one read carrying the held identity", async () => {
    const { gate, adapter } = cacheTestBed({ gated: true });
    await seedCache(adapter, "local:thr_1");
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("local:thr_1"));
    const pending = threadsStore.getState().ensureThread("local:thr_1");
    fake.emitStateChange("reconnecting");
    fake.emitReady(); // the ready pass goes by while the lookup is parked and would never replace a stale arming
    gate.release();
    await pending;
    const calls = fake.calls.filter((c) => c.method === "thread/read");
    expect(calls).toHaveLength(1); // one epoch-current read, not a refused claim plus a replacement
    const read = calls[0];
    if (read === undefined) throw new Error("the epoch-current read must have been recorded");
    expect((read.params as { heldSnapshot?: SnapshotIdentity }).heldSnapshot).toEqual({
      incarnation: "inc-1",
      length: 40,
    }); // the shell published and armed this read with its held identity
    const model = threadsStore.getState().threads.get("local:thr_1");
    expect(model?.history?.turns.map((t) => t.id)).toEqual(["turn_1"]); // the recorded page survived the merge
    expect(model?.capabilities.send).toBe(true); // the authoritative read published over the shell
    expect(threadsStore.getState().cacheShellRefs.has("local:thr_1")).toBe(false); // and closed the shell window
  });
});

describe("cached write seam", () => {
  // Every test scopes its fake timers to the clock functions (Task 5's own
  // rationale): fake-indexeddb delivers every IndexedDB event on
  // setImmediate, which a bare useFakeTimers() fakes too — freezing the
  // debounced write's own transaction mid-test — while the debounce rides
  // setTimeout either way.

  it.each([
    { cadence: "injected", reset: false, debounceMs: 40, maxWaitMs: 100, stepMs: 25 },
    { cadence: "default after reset", reset: true, debounceMs: 1_000, maxWaitMs: 5_000, stepMs: 500 },
  ])("uses the $cadence write cadence for debounce and max-wait", async ({ reset, debounceMs, maxWaitMs, stepMs }) => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      setCacheWriteTimersForTests({ debounceMs: 40, maxWaitMs: 100 });
      if (reset) resetThreadsStoreForTests();
      const adapter = new SessionCacheIndexedDB();
      const putSpy = vi.spyOn(adapter, "put");
      installCacheAdapter(adapter);
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:cadence");
      await resolveEverything(fake);

      await vi.advanceTimersByTimeAsync(debounceMs - 1);
      expect(putSpy).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(1);
      expect(putSpy).toHaveBeenCalledTimes(1);
      expect(await cacheRecord("local:cadence")).toBeDefined();

      // Publications faster than the debounce keep postponing it, leaving
      // only the max-wait to commit the latest fold at its exact boundary.
      let lastFold = "";
      for (let elapsed = 0; elapsed < maxWaitMs; elapsed += stepMs) {
        lastFold = `turn_${elapsed}`;
        emitHistoryUpdated("local:cadence", { fold: lastFold });
        await vi.advanceTimersByTimeAsync(stepMs - 1);
        expect(putSpy).toHaveBeenCalledTimes(1);
        await vi.advanceTimersByTimeAsync(1);
      }
      expect(putSpy).toHaveBeenCalledTimes(2);
      expect((await cacheRecord("local:cadence"))?.history.turns.map((turn) => turn.id)).toContain(lastFold);
      await vi.advanceTimersByTimeAsync(maxWaitMs);
      expect(putSpy).toHaveBeenCalledTimes(2); // neither timer survives its burst
    } finally {
      resetThreadsStoreForTests();
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });

  it("shell safety: the shell and its bumped base publish without a write, and the first read merge resumes writes", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await seedAndReload("local:thr_1"); // Task 5's replay bed: shell published, read pending
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:thr_1")).toBeUndefined(); // the shell gate held
      resolvePendingRead(); // the authoritative read merges
      await vi.advanceTimersByTimeAsync(1_000);
      const record = await cacheRecord("local:thr_1");
      expect(record?.history.incarnation).toBe("inc-1"); // writes resumed from a verified model
    } finally {
      vi.useRealTimers();
    }
  });

  it("a failed reconciling read keeps content visible and writes nothing, including after a live fold onto the shell", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await seedAndReload("local:thr_1", { failFirstRead: true });
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:thr_1")).toBeUndefined(); // the shell gate held through the failure
      emitHistoryUpdated("local:thr_1", { fold: "turn_2" }); // folded onto the unverified shell
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:thr_1")).toBeUndefined(); // ...and through the fold
      runScheduledHydrationRetryForThisFile(); // this file's own retry driver
      await awaitReadArmed(); // the retry arms its read after its own await chain
      resolvePendingRead(); // the retry succeeds
      await vi.advanceTimersByTimeAsync(1_000);
      // Only the fold could have put turn_2 in the record: the retry's
      // response carries no turns of its own, so the merged write proves both
      // that the fold landed on the shell and that it survived the retry.
      expect((await cacheRecord("local:thr_1"))?.history.turns.map((t) => t.id)).toContain("turn_2");
    } finally {
      vi.useRealTimers();
    }
  });

  it("a history-failed answer applies the one-diagnostic rule and refuses writes", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await seedAndReload("local:thr_1", { historyFailed: true });
      await vi.advanceTimersByTimeAsync(1_000);
      expect(threadsStore.getState().threads.get("local:thr_1")?.history?.failed).toBeDefined();
      expect(await cacheRecord("local:thr_1")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("watch exclusion: a rich watched upgrade publishes without a cache write", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // The watched model is rich (a turns-bearing history) and the
      // connection is open, so the only thing standing between the
      // publication and a stored record is the threads-map-only placement.
      const { adapter } = cacheTestBed();
      await adapter.get("local:watched_warm", Date.now()); // a pane-mount-shaped miss opens the connection
      const fake = connectFakeClient();
      fake.on(
        "thread/read",
        echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 }, turns: [turnFixture("turn_w")] }),
      );
      await threadsStore.getState().watchThread("local:watched", { includeTurns: true });
      await vi.advanceTimersByTimeAsync(5_000);
      const watched = threadsStore.getState().watchedThreads.get("local:watched");
      if (watched === undefined) throw new Error("the watched model must publish");
      expect(watched.history?.turns.length).toBeGreaterThan(0);
      expect(await cacheRecord("local:watched")).toBeUndefined(); // threads-map-only placement
    } finally {
      vi.useRealTimers();
    }
  });

  it("live-notification capture: a history/updated fold refreshes the record with no putThreadModel, within the max-wait", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:live");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:live"), "the debounced write landed").toBeDefined();
      for (let i = 0; i < 6; i += 1) {
        // sub-second notifications keep resetting the trailing timer
        emitHistoryUpdated("local:live", { fold: `turn_x${i}` });
        await vi.advanceTimersByTimeAsync(700);
      }
      await vi.advanceTimersByTimeAsync(5_000); // ...but the max-wait fired within it
      const record = await cacheRecord("local:live");
      expect(record?.history.turns.length).toBeGreaterThan(1); // the busy session's record did not starve
    } finally {
      vi.useRealTimers();
    }
  });

  it("write gating: failed, invalidated, and mid-resync histories write nothing until the resync settles", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:gated");
      await resolveEverything(fake);
      const model = threadsStore.getState().threads.get("local:gated");
      if (model === undefined) throw new Error("local:gated must be published before the write-gate edits");
      const history = model.history;
      if (history === undefined)
        throw new Error("local:gated must hold a versioned history before the write-gate edits");
      threadsStore.setState((s) => ({
        threads: new Map(s.threads).set("local:gated", { ...model, history: { ...history, failed: "history failed" } }),
      }));
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:gated")).toBeUndefined(); // failed gate

      const settled = threadsStore.getState().threads.get("local:gated");
      if (settled === undefined) throw new Error("local:gated must stay published between the write-gate edits");
      const settledHistory = settled.history;
      if (settledHistory === undefined) {
        throw new Error("local:gated must keep a versioned history between the write-gate edits");
      }
      threadsStore.setState((s) => ({
        threads: new Map(s.threads).set("local:gated", {
          ...settled,
          history: {
            ...settledHistory,
            failed: undefined,
            invalidatedAtGeneration: settledHistory.issuedGeneration,
            awaited: { bootGeneration: settledHistory.bootGeneration, epoch: settledHistory.epoch },
          },
        }),
      }));
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:gated")).toBeUndefined(); // invalidated gate

      // The resync read the invalidation asked for settles it: a fresh
      // authoritative read replaces the mid-transition pair (the delayed-
      // reconciliation case), and the settled pair then persists.
      await driveResyncRead("local:gated");
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:gated")).toBeDefined(); // the settled pair persists
    } finally {
      vi.useRealTimers();
    }
  });

  it("a fire retires its whole schedule: a stale max-wait never collapses a later window (fix round 1)", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const adapter = new SessionCacheIndexedDB();
      const putSpy = vi.spyOn(adapter, "put");
      installCacheAdapter(adapter);
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:timer");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000); // the first trailing window closes: intended write #1
      expect(putSpy).toHaveBeenCalledTimes(1);
      emitHistoryUpdated("local:timer", { fold: "turn_t1" }); // the fresh schedule: trailing at +1 s, max-wait at +5 s
      await vi.advanceTimersByTimeAsync(4_000); // t = 5 s: past the FIRST schedule's original max-wait
      expect(putSpy).toHaveBeenCalledTimes(2); // only the fresh trailing's own write: the stale max-wait never fired
      await vi.advanceTimersByTimeAsync(2_000); // t = 7 s: past the fresh max-wait too — every timer is retired
      expect(putSpy).toHaveBeenCalledTimes(2); // exactly the two intended writes, no orphans left to fire
    } finally {
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });

  it("the max-wait keeps its guarantee across bursts: a stale-closure max-wait ends its burst and the next arms fresh (fix round 2)", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const adapter = new SessionCacheIndexedDB();
      const putSpy = vi.spyOn(adapter, "put");
      installCacheAdapter(adapter);
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:starve");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000); // write #1: the hydration publish's own trailing window
      expect(putSpy).toHaveBeenCalledTimes(1);
      // Phase 1: publications 700 ms apart keep resetting the trailing timer,
      // so only the burst's max-wait (armed at the first fold, t = 1 s) can
      // fire — at t = 6 s, while the map holds a schedule object SEVEN
      // generations newer than the one the max-wait's closure captured.
      for (let i = 0; i < 7; i += 1) {
        emitHistoryUpdated("local:starve", { fold: `turn_a${i}` });
        await vi.advanceTimersByTimeAsync(700);
      }
      await vi.advanceTimersByTimeAsync(200); // t = 6.1 s: the max-wait boundary fired
      expect(putSpy).toHaveBeenCalledTimes(2); // write #2: the starved stream got its once-per-max-wait write
      // Phase 2: the stream keeps publishing sub-trailing. The next burst must
      // arm a FRESH max-wait — the stale-closure fire must not leave the
      // current entry holding its dead handle for later reschedules to
      // inherit — so another write lands within the next 5 s window.
      for (let i = 0; i < 7; i += 1) {
        emitHistoryUpdated("local:starve", { fold: `turn_b${i}` });
        await vi.advanceTimersByTimeAsync(700);
      }
      await vi.advanceTimersByTimeAsync(200); // t = 11.2 s: the second burst's max-wait boundary (armed at t = 6.1 s)
      expect(putSpy).toHaveBeenCalledTimes(3); // write #3: the guarantee survives its own first firing — not zero
    } finally {
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });

  it("the connection gate: a write firing while the adapter is still opening skips, and the next publication retries", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      installWedgedCacheAdapter();
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      // The wedged lookup can only lose its own 250 ms deadline race, and
      // that deadline is a faked timer: drive it, then await the cold read it
      // armed (Task 5's deadline-test shape — awaiting the ensureThread
      // outright would deadlock on the faked deadline).
      const pending = threadsStore.getState().ensureThread("local:conn");
      await vi.advanceTimersByTimeAsync(250);
      await pending;
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:conn")).toBeUndefined(); // skipped: the open never settled
      restoreCacheAdapter();
      // The restored adapter was never opened in this test — the wedged
      // override owned the only lookup — so warm the connection with a
      // pane-mount-shaped lookup before the retrying write meets the gate.
      await threadsStore.getState().ensureThread("local:conn_warm");
      emitHistoryUpdated("local:conn", { fold: "turn_z" }); // the next publication retries
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:conn")).toBeDefined();
    } finally {
      vi.useRealTimers();
      // Same as the deadline test above: the wedged lookup this test armed
      // can never settle its registered storage work, so forget it before
      // any later test settles the tracker.
      clearProjectionWorkForTests();
      restoreCacheAdapter();
    }
  });

  it("flush: releaseThread commits a pending debounced write ordered before removal", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:flushed");
      await resolveEverything(fake);
      // no debounce advance yet: the write is still pending
      threadsStore.getState().releaseThread("local:flushed");
      expect(await cacheRecord("local:flushed")).toBeDefined(); // the flush committed the tail before removal
      expect(threadsStore.getState().threads.has("local:flushed")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("flush: a ref closed by deletion flushes nothing (the deletedRefs gate)", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:gone");
      await resolveEverything(fake);
      // Arm the deletion state directly (Task 9's markCacheSessionsDeleted is
      // the production writer of it); this test owns the flush-side gate.
      threadsStore.setState((s) => ({ deletedRefs: new Set(s.deletedRefs).add("local:gone") }));
      threadsStore.getState().releaseThread("local:gone"); // the flush runs; the deletedRefs gate refuses it
      expect(await cacheRecord("local:gone")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("the missed-message backstop: a write whose transaction observes a newer epoch aborts and arms suppression for older leases", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // The ref must hold a lease — the durable epoch its arming lookup
      // captured — because the backstop arms suppression per lease (spec,
      // "Eviction, cap, and cross-tab"): a cold-loaded ref holds none and its
      // next write would simply adopt the observed epoch. A seeded record is
      // what gives the pane a lease to arm.
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:backstop");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 40 } }));
      await threadsStore.getState().ensureThread("local:backstop");
      await adapter.deleteRecords(["local:backstop"]); // observe only this tab's writes from here
      await bumpCacheEpochRow(indexedDB, 5); // a sibling's clear committed; the message has not arrived
      emitHistoryUpdated("local:backstop", { fold: "turn_y" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:backstop")).toBeUndefined(); // the aborted write armed suppression...
      // The arming itself, not just the absence: without the backstop the
      // in-transaction epoch check would still abort this tab's writes, but
      // nothing would ever arm the suppression the missed message owed us.
      expect(threadsStore.getState().cacheSuppressed.has("local:backstop")).toBe(true);
      emitHistoryUpdated("local:backstop", { fold: "turn_y2" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:backstop")).toBeUndefined(); // ...so repeated notifications never re-persist the ref
    } finally {
      vi.useRealTimers();
    }
  });

  it("oversize memo: repeated debounced updates during an oversize lifetime keep skipping, and a replacement clears the memo", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // The brief's 200-byte cap sits below the floor of any record this
      // fixture family can build (the history envelope alone approaches it),
      // so the oversize boundary moves to 1_000 bytes and the big turn's
      // padding to 1_200 — the same skip-memo-replace structure with a
      // boundary the small record can actually fit under.
      const adapter = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), maxBytes: 1_000 });
      const putSpy = vi.spyOn(adapter, "put");
      installCacheAdapter(adapter);
      const fake = connectFakeClient();
      fake.on(
        "thread/read",
        echoingReadHandler({
          snapshot: { incarnation: "inc-1", length: 900 },
          turns: [turnFixture("turn_big", "x".repeat(1_200))],
        }),
      );
      await threadsStore.getState().ensureThread("local:big");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:big")).toBeUndefined(); // oversize: skipped whole, stale row deleted
      emitHistoryUpdated("local:big", { fold: "turn_big2" });
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:big")).toBeUndefined(); // memoized: the second fire skipped the serialize
      expect(putSpy).toHaveBeenCalledTimes(1); // ...and the memo is what skipped it: no second put at all
      scriptStaleSnapshotRetry(fake, {
        snapshot: { incarnation: "inc-2", length: 2 },
        turns: [turnFixture("turn_small")],
      });
      await driveResyncRead("local:big"); // the retry-without-held replacement shrinks the history
      emitHistoryUpdated("local:big", { fold: "turn_small2" });
      await vi.advanceTimersByTimeAsync(1_000);
      const record = await cacheRecord("local:big");
      expect(record).toBeDefined(); // the replacement cleared the memo: an eligible session persists again
      expect(record?.history.incarnation).toBe("inc-2");
    } finally {
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });

  it("flush: the pinned drain (dropUnpinnedModel) commits a pending debounced write before the model leaves the map", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // A durable queued row pins the ref: a pinned model outlives its pane's
      // final releaseThread and leaves only when the row settles and the pin
      // refresh drops it (dropUnpinnedModel) — the path the spec calls "the
      // path a pinned ref's model finally leaves through".
      const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-drain" });
      await storage.enqueueIntent({
        targetRef: "local:pinned",
        method: "turn/queue",
        payload: { ref: "local:pinned", expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
        attachments: [],
        optimisticDisplay: { text: "queued" },
      });
      storage.close();
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      fake.on("turn/queue", () => new Promise<TurnQueueResponse>(() => {})); // the row stays durable
      // The ready discovery's rejoin read publishes the pinned ref's model.
      await nextModelPublished("local:pinned");
      // The connection gate needs an open adapter, and a pane's arming lookup
      // is what opens it: warm it with a second pane the way a workspace
      // mount does.
      await threadsStore.getState().ensureThread("local:pinned_warm");
      void threadsStore.getState().ensureThread("local:pinned"); // the pane claim; the model exists, so no read
      threadsStore.getState().releaseThread("local:pinned"); // pinned: the model stays
      expect(threadsStore.getState().threads.has("local:pinned")).toBe(true);
      const drained = nextModelRemoved("local:pinned");
      emitAppliedQueuedNotification("local:pinned", "mutation-drain"); // the row settles: the pin drops
      await drained;
      expect(await cacheRecord("local:pinned")).toBeDefined(); // the flush committed the tail before removal
      expect(threadsStore.getState().threads.has("local:pinned")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("clear suppresses a restored pinned ref without an ensureThread claim until its final release", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const ref = "local:pinned_clear";
      const { adapter } = cacheTestBed();
      await adapter.get(ref, Date.now()); // open storage without a pane claim
      const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-pinned-clear" });
      await storage.enqueueIntent({
        targetRef: ref,
        method: "turn/queue",
        payload: { ref, expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
        attachments: [],
        optimisticDisplay: { text: "queued" },
      });
      storage.close();
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ turns: [turnFixture("turn_pinned")] }));
      fake.on("turn/queue", () => new Promise<TurnQueueResponse>(() => {}));
      await nextModelPublished(ref); // handleReady discovers the durable row, not ensureThread
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect((await cacheRecord(ref))?.history.turns.map((turn) => turn.id)).toEqual(["turn_pinned"]);

      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(await cacheRecord(ref)).toBeUndefined();
      emitHistoryUpdated(ref, { fold: "turn_after_clear" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord(ref)).toBeUndefined();
      expect(threadsStore.getState().cacheSuppressed.has(ref)).toBe(true);
      expect(threadsStore.getState().cacheLeases.get(ref)).toBe(0);

      const drained = nextModelRemoved(ref);
      emitAppliedQueuedNotification(ref, "mutation-pinned-clear");
      await drained;
      expect(await cacheRecord(ref)).toBeUndefined(); // final-release flush must also respect suppression
      expect(threadsStore.getState().cacheSuppressed.has(ref)).toBe(false);
      expect(threadsStore.getState().cacheLeases.has(ref)).toBe(false);
      await threadsStore.getState().ensureThread(ref); // the first pane claim starts a new lifetime
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord(ref)).toBeDefined();
      expect(threadsStore.getState().cacheLeases.get(ref)).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("flush: the pinned drain (dropUnpinnedModel) ends a suppressed shell's metadata and lease with the model", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // Claim a real cached shell and pin it while its read is still pending.
      // No authoritative publish may clear its shell metadata before the drain.
      await seedAndReload("local:pinned_sup");
      await threadsStore.getState().queue("local:pinned_sup", "queued");
      const storage = new MutationOutboxIndexedDB();
      const rows = await storage.listOutbox("local:pinned_sup");
      storage.close();
      expect(rows).toHaveLength(1);
      const row = rows[0];
      if (row === undefined) throw new Error("the queued mutation must pin the shell");
      threadsStore.getState().releaseThread("local:pinned_sup"); // pinned: the model stays
      expect(threadsStore.getState().threads.has("local:pinned_sup")).toBe(true);
      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(threadsStore.getState().cacheSuppressed.has("local:pinned_sup")).toBe(true);
      expect(threadsStore.getState().cacheLeases.has("local:pinned_sup")).toBe(true);
      expect(threadsStore.getState().cacheShellRefs.has("local:pinned_sup")).toBe(true);
      expect(threadsStore.getState().cacheAnchors.has("local:pinned_sup")).toBe(true);
      const drained = nextModelRemoved("local:pinned_sup");
      emitAppliedQueuedNotification("local:pinned_sup", row.clientMutationId);
      await drained;
      expect(threadsStore.getState().threads.has("local:pinned_sup")).toBe(false);
      expect(threadsStore.getState().cacheSuppressed.has("local:pinned_sup")).toBe(false);
      expect(threadsStore.getState().cacheLeases.has("local:pinned_sup")).toBe(false);
      expect(threadsStore.getState().cacheShellRefs.has("local:pinned_sup")).toBe(false);
      expect(threadsStore.getState().cacheAnchors.has("local:pinned_sup")).toBe(false);
      expect(await cacheRecord("local:pinned_sup")).toBeUndefined();
      resolvePendingRead(); // the retired read cannot revive the shell
    } finally {
      vi.useRealTimers();
    }
  });
});

// The gap-rule bed (Task 7; spec, "The two serving paths, the live gap, and
// its rule"). Positions are explicit so overlap, abut, and disjoint windows
// are arithmetic, not guesswork: turnAt builds one positioned TurnModel
// (turnFixture's shape with the entry as the position, so one fixture serves
// the seed and the read), and windowResponse builds the reconciling read's
// wire answer through the file's own readResponse builder so the wire shape
// stays identical.

function turnAt(id: string, entry: number, text = "positioned turn"): TurnModel & Turn {
  return {
    id,
    status: "completed",
    itemsView: "full",
    items: [{ id: `item_${id}`, turnId: id, type: "agentMessage", text, position: { entry, item: 0 } }],
    version: 2,
  };
}

// windowResponse builds the reconciling read's wire answer: a fresh window
// of `count` turns named w<entry> starting at `from`, one positioned item per
// turn, with no `changes`. The identity matches the seed (incarnation
// "inc-1", length 40) so the read's disposition is a merge and the gap
// rule's position arithmetic is the only thing that decides; the ref is the
// block's own "local:gap", the one every scenario drives.
function windowResponse(from: number, count: number, cursor: string): ThreadReadResponse {
  const turns: Turn[] = [];
  for (let at = 0; at < count; at += 1) turns.push(turnAt(`w${from + at}`, from + at));
  return readResponse("local:gap", { olderCursor: cursor, turns });
}

// seedPositionedRecord seeds a record holding three turns — turn_p2 at entry
// 10 (page 2), turn_p1 at entry 20 (page 1), turn_w at entry 30 (the window's
// newest) — with `olderCursor: "cur-deep"`, so the captured anchor is
// `{ entry: 30 }`. Seeding goes through a throwaway adapter over the same
// global database the module adapter reads (cacheRecord's reader pattern), so
// the bed installs nothing on the singleton seam.
async function seedPositionedRecord(ref: string, overrides: { turns?: TurnModel[] } = {}): Promise<void> {
  const adapter = new SessionCacheIndexedDB();
  try {
    await seedCache(adapter, ref, {
      turns: overrides.turns ?? [turnAt("turn_p2", 10), turnAt("turn_p1", 20), turnAt("turn_w", 30)],
      olderCursor: "cur-deep",
    });
  } finally {
    adapter.close();
  }
}

// The reload bed's parked read: whichever deferred read a fixture parks — the
// shell's own, the stale-snapshot retry's, or the hand-driven failure
// retry's — the scenario resolves it through resolveReloadRead, which echoes
// the parked request's own generation back (the store's latest-window
// contract discards a response to a superseded generation; resolvePendingRead's
// own rule).
let pendingReloadResolve: ((response: ThreadReadResponse) => void) | undefined;
let pendingReloadGeneration: number | undefined;

function parkReloadRead(params: { requestGeneration?: number }): Promise<ThreadReadResponse> {
  pendingReloadGeneration = params.requestGeneration;
  return new Promise<ThreadReadResponse>((resolve) => {
    pendingReloadResolve = resolve;
  });
}

function resolveReloadRead(response: ThreadReadResponse): void {
  const resolve = pendingReloadResolve;
  if (resolve === undefined) throw new Error("resolveReloadRead: no reload read is parked");
  pendingReloadResolve = undefined;
  resolve(
    pendingReloadGeneration === undefined ? response : { ...response, requestGeneration: pendingReloadGeneration },
  );
}

// reloadWithDeferredRead: the shell publishes from the seeded record, the
// reconciling read arms and stays parked, and the scenario resolves it.
async function reloadWithDeferredRead(ref: string): Promise<{ resolveRead: (response: ThreadReadResponse) => void }> {
  const fake = connectFakeClient();
  let markArmed: () => void = () => {};
  const armed = new Promise<void>((resolve) => {
    markArmed = resolve;
  });
  fake.on("thread/read", (params) => {
    markArmed();
    return parkReloadRead(params);
  });
  const pending = threadsStore.getState().ensureThread(ref);
  void pending.catch(() => {}); // the fixture owns the claim's failure modes
  await nextModelPublished(ref);
  await armed;
  if (threadsStore.getState().cacheShellRefs.has(ref) !== true) {
    throw new Error(`reloadWithDeferredRead: ${ref} must publish as a cached shell`);
  }
  if (pendingReloadResolve === undefined) throw new Error("reloadWithDeferredRead: the read must stay parked");
  return { resolveRead: resolveReloadRead };
}

// reloadWithStaleSnapshot does the same but the first read — the one the
// shell armed with its heldSnapshot — rejects with the hub's
// TranscriptItemCursorStale, and the retry the store issues without it stays
// parked. calls() answers the recorded thread/read params array so
// heldSnapshot assertions read the actual wire request.
async function reloadWithStaleSnapshot(ref: string): Promise<{
  resolveRetry: (response: ThreadReadResponse) => void;
  calls: () => Array<MethodTypes["thread/read"]["params"]>;
}> {
  const fake = connectFakeClient();
  let markRetryArmed: () => void = () => {};
  const retryArmed = new Promise<void>((resolve) => {
    markRetryArmed = resolve;
  });
  fake.on("thread/read", (params) => {
    if (params.heldSnapshot !== undefined) {
      throw new WireError("stale", -32000, { evenerErrorInfo: "transcriptItemCursorStale" });
    }
    markRetryArmed();
    return parkReloadRead(params);
  });
  const pending = threadsStore.getState().ensureThread(ref);
  void pending.catch(() => {}); // the fixture owns the claim's failure modes
  await nextModelPublished(ref);
  await retryArmed;
  if (threadsStore.getState().cacheShellRefs.has(ref) !== true) {
    throw new Error(`reloadWithStaleSnapshot: ${ref} must publish as a cached shell`);
  }
  if (pendingReloadResolve === undefined) throw new Error("reloadWithStaleSnapshot: the retry must stay parked");
  return {
    resolveRetry: resolveReloadRead,
    calls: () =>
      fake.calls
        .filter((call) => call.method === "thread/read")
        .map((call) => call.params as MethodTypes["thread/read"]["params"]),
  };
}

// reloadWithFailedFirstRead arms the spec's failed-first-read reload (scenario
// 7's fold cases): the shell publishes, its reconciling read rejects as a
// transport failure, and the retry the store schedules stays undriven — the
// scenario folds its live history/updated onto the shell in the gap, the one
// moment a fold is genuinely live. (A fold emitted while a read is in flight
// is buffered for the publish and then cleared by the response cut, which
// already contains every notification the wire delivered before it — a
// scripted response that omits it is a shape the real daemon cannot produce.)
// The scenario drives the parked retry by hand through the file's injected
// scheduler and resolves it through resolveReloadRead.
async function reloadWithFailedFirstRead(ref: string): Promise<void> {
  const fake = connectFakeClient();
  let reads = 0;
  fake.on("thread/read", (params) => {
    reads += 1;
    if (reads === 1) {
      notifyReadArmed();
      return Promise.reject(new Error("transport: read failed"));
    }
    notifyReadArmed();
    return parkReloadRead(params);
  });
  const pending = threadsStore.getState().ensureThread(ref);
  void pending.catch(() => {}); // the fixture owns the claim's failure modes
  await nextModelPublished(ref);
  await awaitReadArmed(); // the failing read is armed
  // The rejection's own await chain must settle before the caller folds: the
  // pending hydration leaves the map inside that catch (a frame buffered
  // meanwhile would be dropped with it), and the hand-driven retry is what
  // the scheduler records. Microtask drain, the file's cut-window idiom.
  for (let at = 0; at < 20 && scheduledHydrationRetries.length === 0; at += 1) await Promise.resolve();
  if (scheduledHydrationRetries.length === 0) {
    throw new Error("reloadWithFailedFirstRead: the failed read must schedule its retry");
  }
  if (threadsStore.getState().cacheShellRefs.has(ref) !== true) {
    throw new Error(`reloadWithFailedFirstRead: ${ref} must still be a cached shell`);
  }
}

// settleReload awaits the reload's publication: the store bumps the ref's
// hydrations counter exactly once per successful publish
// (publishThreadHydration), so the bump is the deterministic witness that the
// reconciling read's model is on the store — whichever fixture drove the read
// (the parked one, the stale-snapshot retry, or the hand-driven failure
// retry, whose own ensureThread promise already rejected). Called as the
// next statement after the read resolves, before the publish's microtask
// chain can run.
async function settleReload(ref: string): Promise<void> {
  const before = threadsStore.getState().hydrations.get(ref) ?? 0;
  await new Promise<void>((resolve) => {
    const unsubscribe = threadsStore.subscribe((state) => {
      if ((state.hydrations.get(ref) ?? 0) <= before) return;
      unsubscribe();
      resolve();
    });
  });
}

describe("the live gap rule", () => {
  it("scenario 6: an overlapping window merges and the deepest cursor is kept (the pinned reducer rule)", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    resolveRead(windowResponse(25, 3, "cur-shallow")); // start 25 is below the anchor 30: the merge is gapless
    await settleReload("local:gap");
    const { model, history } = requireOpenHistory("the live gap rule: scenario 6 overlap", "local:gap");
    // The pages survived and the window arrived: the merge interleaves the
    // window's own turns (w25-w27) between the held pages and the record's
    // window newest — a fresh turn with a new id is never dropped, so the
    // window's turns appear alongside the held ones the scenario pins.
    expect(history.turns.map((t) => t.id)).toEqual(["turn_p2", "turn_p1", "w25", "w26", "w27", "turn_w"]);
    expect(model.olderCursor).toBe("cur-deep"); // 792c379eca's rule, now pinned in the reload scenario
  });

  it("scenario 6: a window beginning exactly at the captured position's successor replaces (the accepted abut cost)", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    resolveRead(windowResponse(31, 3, "cur-abut")); // start 31 > anchor 30: one-item adjacency is indistinguishable from a one-item hole
    await settleReload("local:gap");
    const { model, history } = requireOpenHistory("the live gap rule: scenario 6 abut", "local:gap");
    expect(history.turns.map((t) => t.id)).toEqual(["w31", "w32", "w33"]); // the cached pages were dropped
    expect(model.olderCursor).toBe("cur-abut"); // the response's cursor is taken
  });

  it("scenario 7: a window starting entirely above the anchor replaces, and the response's cursor is taken", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    resolveRead(windowResponse(40, 3, "cur-new")); // disjoint: start 40 > anchor 30, no changes
    await settleReload("local:gap");
    const { model, history } = requireOpenHistory("the live gap rule: scenario 7 replace", "local:gap");
    expect(history.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42"]); // replaced, pages dropped
    expect(model.olderCursor).toBe("cur-new"); // no silent hole: nothing merged around the gap
  });

  it("scenario 7: a fold newer than the response replaces and replays the fold tail", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await seedPositionedRecord("local:gap");
      await reloadWithFailedFirstRead("local:gap"); // the shell's read fails; the retry is the scenario's to drive
      emitHistoryUpdated("local:gap", { fold: "turn_fold", entry: 50 }); // the fold lands above the incoming window's end
      runScheduledHydrationRetryForThisFile();
      await awaitReadArmed();
      resolveReloadRead(windowResponse(40, 3, "cur-new")); // the window's end (42) is below the fold (50)
      await settleReload("local:gap");
      const { history } = requireOpenHistory("the live gap rule: scenario 7 fold", "local:gap");
      expect(history.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42", "turn_fold"]); // replace, then replay
      expect(threadsStore.getState().cacheShellRefs.has("local:gap")).toBe(false); // the first authoritative read cleared the shell
      await vi.advanceTimersByTimeAsync(1_000);
      const record = await cacheRecord("local:gap");
      if (record === undefined) throw new Error("scenario 7 fold: the debounced write must have landed");
      expect(record.history.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42", "turn_fold"]); // no hole was written
    } finally {
      vi.useRealTimers();
    }
  });

  it("scenario 7: a fold arriving with a dropped notification behind it replays what arrived, carrying today's hole", async () => {
    await seedPositionedRecord("local:gap");
    await reloadWithFailedFirstRead("local:gap"); // the shell's read fails; the retry is the scenario's to drive
    // The first push (entry 48) is dropped — only the second (entry 50) arrives.
    emitHistoryUpdated("local:gap", { fold: "turn_keep", entry: 50 });
    runScheduledHydrationRetryForThisFile();
    await awaitReadArmed();
    resolveReloadRead(windowResponse(40, 3, "cur-new"));
    await settleReload("local:gap");
    const { history } = requireOpenHistory("the live gap rule: scenario 7 dropped", "local:gap");
    expect(history.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42", "turn_keep"]); // exactly what arrived: today's hole, mirrored faithfully
  });

  it("scenario 8: stale identity: the server rejects the held snapshot, the retry replaces, and the cached turns do not survive", async () => {
    await seedPositionedRecord("local:gap");
    const { resolveRetry, calls } = await reloadWithStaleSnapshot("local:gap"); // the first read rejects TranscriptItemCursorStale
    resolveRetry(windowResponse(40, 3, "cur-new"));
    await settleReload("local:gap");
    expect(calls()[1]?.heldSnapshot).toBeUndefined(); // the retry carried no held identity
    const { history } = requireOpenHistory("the live gap rule: scenario 8 stale", "local:gap");
    expect(history.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42"]); // the cached turns did not survive
  });

  it("an empty record takes the ordinary cold merge and the predicate holds (the gap rule's empty case, scenario 15's tail)", async () => {
    await seedPositionedRecord("local:gap", { turns: [] }); // a zero-turn record: no anchor to compare against
    const { resolveRead } = await reloadWithDeferredRead("local:gap");
    resolveRead(windowResponse(40, 3, "cur-new"));
    await settleReload("local:gap");
    const { history } = requireOpenHistory("the live gap rule: empty record", "local:gap");
    expect(history.turns.map((t) => t.id)).toEqual(["w40", "w41", "w42"]); // the ordinary cold path, no crash, no replace branch
  });
});

// The scroll-back bed (Task 8; spec, "Scroll-back: the deepest held cursor",
// Testing scenario 12). olderPageResponse answers a thread/turns/list with
// the wire shape the hub stamps on every page it serves: identity rides the
// response (source.go's StampPage — bootGeneration, epoch, snapshot), the
// page's turns live in `data`, and an exhausted page is `data: []` with no
// nextCursor, "the transcript's first item ... as the daemon itself answers".
// The identity is the file's default one so a page into the settled window's
// versioned history classifies as a same-identity merge (pageDisposition),
// not as a boot-generation stranger.
function olderPageResponse(overrides: { turns?: Turn[]; nextCursor?: string } = {}): ThreadTurnsListResponse {
  return {
    data: overrides.turns ?? [],
    ...(overrides.nextCursor === undefined ? {} : { nextCursor: overrides.nextCursor }),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 40 },
  };
}

// seedScrollbackRecord seeds the volume case's record: the window the prior
// session ended on (entries 30-32) plus the two older pages it paged in —
// page 1 at 20-22, page 2 at 10-12 — with olderCursor "cur-page-3", the
// boundary below the deepest held page. Seeding goes through a throwaway
// adapter over the same global database the module adapter reads
// (seedPositionedRecord's pattern), so the bed installs nothing on the
// singleton seam.
async function seedScrollbackRecord(ref: string): Promise<void> {
  const adapter = new SessionCacheIndexedDB();
  try {
    await seedCache(adapter, ref, {
      turns: [
        turnAt("turn_p2a", 10),
        turnAt("turn_p2b", 11),
        turnAt("turn_p2c", 12),
        turnAt("turn_p1a", 20),
        turnAt("turn_p1b", 21),
        turnAt("turn_p1c", 22),
        turnAt("turn_wa", 30),
        turnAt("turn_wb", 31),
        turnAt("turn_wc", 32),
      ],
      olderCursor: "cur-page-3",
    });
  } finally {
    adapter.close();
  }
}

// The settled window the reconciling read answers with: three turns entirely
// above the record's newest held item (entry 32), so the gap rule replaces
// and the response's cursor "cur-settled" becomes the model's — the cursor
// scroll-back continues from once the shell is gone.
function settledWindowResponse(ref: string): ThreadReadResponse {
  return readResponse(ref, {
    turns: [turnAt("turn_s0", 40), turnAt("turn_s1", 41), turnAt("turn_s2", 42)],
    olderCursor: "cur-settled",
  });
}

describe("scroll-back, the volume case", () => {
  it("renders window plus both cached pages, refuses loadOlderTurns while the shell is unverified, then continues from the settled cursor", async () => {
    // Scenario 12: a prior session hydrated, paged back twice, and its
    // record carries window plus both pages. The reload paints all of it
    // from the shell while the reconciling read stays parked.
    await seedScrollbackRecord("local:scroll");
    const { resolveRead } = await reloadWithDeferredRead("local:scroll");
    const fake = requireConnectedClient("scroll-back: the volume case");
    const listCalls: Array<{ ref?: string; cursor?: string }> = [];
    fake.on("thread/turns/list", (params) => {
      listCalls.push({ ref: params.ref, cursor: params.cursor });
      return olderPageResponse();
    });
    const { model: shell, history } = requireOpenHistory("scroll-back: the shell", "local:scroll");
    expect(history.turns).toHaveLength(3 + 3 + 3); // the window plus the two pages the prior session paged in
    expect(shell.olderCursor).toBe("cur-page-3"); // the deepest held cursor: scroll-back continues below page 2
    // The shell gate: the shell's cursor belongs to whichever window the
    // reconcile settles on, so scroll-back refuses while the read is pending.
    await threadsStore.getState().loadOlderTurns("local:scroll");
    expect(listCalls).toEqual([]); // refused: nothing was fetched against the unverified cursor
    // The reconcile settles on a window entirely above the record, so the
    // gap rule replaces the shell and the response's cursor is taken.
    resolveRead(settledWindowResponse("local:scroll"));
    await settleReload("local:scroll");
    expect(threadsStore.getState().cacheShellRefs.has("local:scroll")).toBe(false); // the authoritative read ended the shell
    await threadsStore.getState().loadOlderTurns("local:scroll");
    expect(listCalls).toHaveLength(1); // one page: the two already-held pages were never re-requested
    expect(listCalls[0]?.cursor).toBe("cur-settled"); // continues from where the settled window left off
  });
});

// The deletion bed (Task 9; spec, "The write seam" deletion bullets): the
// response-keyed hook, the deletion fence's cache re-arm, and cross-tab
// propagation with its idempotent heal. fencedReadError builds the hub's
// durable deletion-fence rejection exactly the way threads.test.ts's own
// markThreadDeletedIfFenced coverage does (data.mutationOutcome ===
// "targetDeleted" — cmd/evener-hub's deletionFenceError).
function fencedReadError(ref: string): WireError {
  return new WireError(`target has been deleted: ${ref}`, -32001, {
    evenerErrorInfo: "actionUnavailable",
    mutationOutcome: "targetDeleted",
    retryDisposition: "none",
  });
}

// seedCacheDirect writes a "prior tab's" record without the store: seedCache's
// record through a throwaway adapter over the global fake factory — the same
// database the singleton adapter and cacheRecord's reader share
// (seedPositionedRecord's pattern).
async function seedCacheDirect(ref: string, overrides: SeedOverrides = {}): Promise<void> {
  const adapter = new SessionCacheIndexedDB();
  try {
    await seedCache(adapter, ref, overrides);
  } finally {
    adapter.close();
  }
}

// healRecord is the record a racing write re-creates: the file's seed shape
// for the named ref (seededRecord's literal is the model).
function healRecord(ref: string): CachedSessionRecord {
  return seededRecord(ref, {});
}

// holdCachePutTransaction parks the singleton adapter's next write
// transaction over the cache's two stores — the debounced put's own —
// mid-flight: the tree's holdNextWriteTransaction, itself built on
// holdIndexedDBEvent.
function holdCachePutTransaction(): ReturnType<typeof holdNextWriteTransaction> {
  return holdNextWriteTransaction(["records", "meta"]);
}

// The faulted-delete adapter: beforeCommit("deleteRecords") throws until
// clearDeleteFault, so the fence's delete transaction aborts and the record
// survives one round — the stated residual — while reads stay clean (get
// never passes the label).
let deleteRecordsFaulted = false;

function installFaultedDeleteAdapter(): void {
  deleteRecordsFaulted = true;
  installCacheAdapter(
    new SessionCacheIndexedDB({
      beforeCommit: (operation) => {
        if (operation === "deleteRecords" && deleteRecordsFaulted) {
          throw new Error("deleteRecords storage fault");
        }
      },
    }),
  );
}

function clearDeleteFault(): void {
  deleteRecordsFaulted = false;
}

// driveSecondFencedRead re-delivers the fenced rejection through the same
// hydrate path the first one took: refreshThread re-issues the pane's read —
// it is not gated on deletedRefs (threads.test.ts's own fence coverage drives
// the same second firing, and the pane claim outlives the retired lifecycle) —
// so the rejection re-fires markThreadDeletedIfFenced, which is what retries
// the aborted delete. The rejection itself is the expected outcome.
async function driveSecondFencedRead(ref: string): Promise<void> {
  await expect(threadsStore.getState().refreshThread(ref)).rejects.toThrow(/deleted/);
}

// TestBroadcastChannel is mutationOutbox.test.ts's shape: an EventTarget
// subclass with a peers set, so posts from a "peer" tab reach every channel
// of the same name — this tab's singleton handler included.
class TestBroadcastChannel extends EventTarget {
  constructor(
    readonly name: string,
    private readonly peers: Set<TestBroadcastChannel>,
    private readonly onPosted?: (message: unknown) => void,
  ) {
    super();
    this.peers.add(this);
  }

  postMessage(message: unknown): void {
    this.onPosted?.(message);
    for (const peer of this.peers) {
      if (peer !== this && peer.name === this.name) {
        peer.dispatchEvent(new MessageEvent("message", { data: message }));
      }
    }
  }

  close(): void {
    this.peers.delete(this);
  }
}

const CACHE_CHANNEL_NAME = "evener.session-cache.v1";

// installTestCacheChannel mirrors the outbox's injected-factory pattern: the
// singleton's channel is created inside the peers set (the factory seam), a
// peer tab posts into it, and `posted` records everything this tab sent.
function installTestCacheChannel(): { peer: { post(message: unknown): void }; posted: unknown[] } {
  const peers = new Set<TestBroadcastChannel>();
  const posted: unknown[] = [];
  const peer = new TestBroadcastChannel(CACHE_CHANNEL_NAME, peers);
  setCacheChannelFactoryForTests(
    (name: string) =>
      new TestBroadcastChannel(name, peers, (message: unknown) => {
        posted.push(message);
      }) as unknown as BroadcastChannel,
  );
  return { peer: { post: (message: unknown) => peer.postMessage(message) }, posted };
}

// installNoCacheChannel is the webview: the factory answers null, exactly as
// the typeof BroadcastChannel guard does where the API is missing.
function installNoCacheChannel(): void {
  setCacheChannelFactoryForTests(() => null);
}

describe("deletion", () => {
  it("a deleteSession success removes the record, cancels the pending timer, and arms deletedRefs", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:thr_1");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:thr_1")).toBeDefined();
      fake.on("evener/session/delete", () => ({
        deleted: ["thr_1"],
        skipped: [],
        navigation: { generation_id: "generation_test", targets: [] },
      }));
      const gone = (await deleteSession(fake, "local:thr_1")).deleted;
      const refs = gone.map((id) => (id.includes(":") ? id : `local:${id}`));
      markCacheSessionsDeleted(refs); // the response-time markDeletedSessionCaches hook
      expect(threadsStore.getState().deletedRefs.has("local:thr_1")).toBe(true);
      expect(await cacheRecord("local:thr_1")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("a debounce task already queued when the deletion ran is refused by the fire-time deletedRefs gate", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:thr_2");
      await resolveEverything(fake);
      emitHistoryUpdated("local:thr_2", { fold: "turn_q" }); // write scheduled, timer not yet fired
      markCacheSessionsDeleted(["local:thr_2"]); // the success handler's arm, before the queued task runs
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:thr_2")).toBeUndefined(); // the queued task's gates refused the ref
    } finally {
      vi.useRealTimers();
    }
  });

  it("a write whose transaction was already open commits first, and the deletion serialized after it removes the record", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:order");
      await resolveEverything(fake);
      const hold = holdCachePutTransaction(); // parks the debounced write's put transaction mid-flight
      emitHistoryUpdated("local:order", { fold: "turn_p" });
      await vi.advanceTimersByTimeAsync(1_000); // the write's transaction is open and parked
      markCacheSessionsDeleted(["local:order"]); // the deletion's deleteRecords transaction queues behind it
      hold.release(); // the write commits first, the deletion runs after it
      await settleProjectionWorkForTests();
      expect(await cacheRecord("local:order")).toBeUndefined(); // the two-orders invariant's second order: removed
    } finally {
      vi.useRealTimers();
    }
  });

  it("the deletion fence removes the record and propagates on a read-proven deletion", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { posted } = installTestCacheChannel();
      await seedCacheDirect("local:fenced", { incarnation: "inc-1", length: 1, turns: [turnFixture("turn_1")] });
      const fake = connectFakeClient();
      fake.on("thread/read", () => {
        throw fencedReadError("local:fenced"); // mutationOutcome "targetDeleted"
      });
      await threadsStore.getState().ensureThread("local:fenced");
      expect(threadsStore.getState().threads.has("local:fenced")).toBe(true); // the shell's content stays visible
      expect(threadsStore.getState().deletedRefs.has("local:fenced")).toBe(true); // the fence armed
      expect(await cacheRecord("local:fenced")).toBeUndefined(); // the fence deleted the record
      expect(posted).toEqual([{ version: 1, sourceId: expect.any(String), kind: "deletion", refs: ["local:fenced"] }]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a record delete whose transaction aborted is retried by the fence's next firing", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      installFaultedDeleteAdapter(); // beforeCommit("deleteRecords") throws on the first pass only
      await seedCacheDirect("local:retry", { incarnation: "inc-1", length: 1, turns: [turnFixture("turn_1")] });
      const fake = connectFakeClient();
      fake.on("thread/read", () => {
        throw fencedReadError("local:retry");
      });
      await threadsStore.getState().ensureThread("local:retry");
      expect(threadsStore.getState().deletedRefs.has("local:retry")).toBe(true); // the fence armed
      expect(await cacheRecord("local:retry")).toBeDefined(); // the delete aborted: the record survived one round (the stated residual)
      clearDeleteFault(); // the storage fault clears
      await driveSecondFencedRead("local:retry"); // the fence's next firing retries idempotently
      expect(await cacheRecord("local:retry")).toBeUndefined();
    } finally {
      vi.useRealTimers();
      restoreCacheAdapter();
    }
  });

  it("the channel message: a sibling holding the ref open stops writing without a reload and never re-creates the record", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { peer } = installTestCacheChannel(); // TestBroadcastChannel: posts from a "peer" tab reach the handler
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:sib");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:sib")).toBeDefined();
      peer.post({ version: 1, sourceId: "other-tab", kind: "deletion", refs: ["local:sib"] });
      emitHistoryUpdated("local:sib", { fold: "turn_w" }); // the sibling keeps receiving pushes
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:sib")).toBeUndefined(); // suppressed + healed: never re-created
    } finally {
      vi.useRealTimers();
    }
  });

  it("the heal: a message arriving after a racing write re-created the record deletes it in the same step", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { peer } = installTestCacheChannel();
      await seedCacheDirect("local:heal", { incarnation: "inc-1", length: 1, turns: [turnFixture("turn_1")] });
      markCacheSessionsDeleted(["local:heal"]); // the deleting tab removed it and broadcast (delivery delayed)
      expect(await cacheRecord("local:heal")).toBeUndefined();
      const sibling = new SessionCacheIndexedDB(); // the same global fake database the singleton uses
      await sibling.put(healRecord("local:heal"), 0, Date.now());
      await settleProjectionWorkForTests();
      expect(await cacheRecord("local:heal")).toBeDefined(); // the racing write re-created the record
      peer.post({ version: 1, sourceId: "other-tab", kind: "deletion", refs: ["local:heal"] });
      await settleProjectionWorkForTests();
      expect(await cacheRecord("local:heal")).toBeUndefined(); // the heal deleted the resurrection in the same step
      sibling.close();
    } finally {
      vi.useRealTimers();
    }
  });

  it("BroadcastChannel absent: deletion still works locally and nothing throws (Review Focus 2)", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      installNoCacheChannel(); // the factory answers null, as the typeof guard does in a webview
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:quiet");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      markCacheSessionsDeleted(["local:quiet"]);
      expect(await cacheRecord("local:quiet")).toBeUndefined(); // local deletion intact, no channel needed
      expect(threadsStore.getState().deletedRefs.has("local:quiet")).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});

// The clear bed (Task 10; spec, "The clear-cached-sessions setting"): the
// settings action's parked and faulted adapters, and the thread/clear driver.

// clearResponse is threads.test.ts's own clear fixture: the durable clear's
// response snapshot carrying the applied receipt the dispatcher settles its
// outbox row with.
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

// driveThreadClear drives the store's own clearThread — the same dispatch
// path threads.test.ts's applyClearResponse coverage uses (the durable outbox
// row, the instance fence, the applied receipt), never a second mechanism.
async function driveThreadClear(ref: string, fake: FakeClient): Promise<void> {
  fake.on("thread/clear", (params) => clearResponse(params, testThread(ref, { turns: [] })));
  await threadsStore.getState().clearThread(ref);
}

// The held-clear adapter: the singleton seam answers clear() only when the
// test releases it. The park is at the adapter seam, not a held transaction:
// holdCachePutTransaction parks a committed transaction's complete event, so
// the data would still land while a clear sat parked on it — the ordering
// test needs the clear's transaction to not have run at all, so it can
// witness the in-memory step's effects against storage the clear has not
// touched. Every other operation passes through to the base class over the
// same default database the seeded record lives in.
class HeldClearAdapter extends SessionCacheIndexedDB {
  readonly #open: Promise<void>;

  constructor(open: Promise<void>) {
    super();
    this.#open = open;
  }

  override clear(): Promise<{ committed: boolean; epoch: number }> {
    return this.#open.then(() => super.clear());
  }
}

let releaseHeldClearFn: (() => void) | undefined;

function installHeldClearAdapter(): void {
  releaseHeldClearFn = undefined;
  const opened = new Promise<void>((resolve) => {
    releaseHeldClearFn = resolve;
  });
  installCacheAdapter(new HeldClearAdapter(opened));
}

function releaseHeldClear(): void {
  const release = releaseHeldClearFn;
  releaseHeldClearFn = undefined;
  release?.();
}

// The held-then-aborting clear adapter: the singleton seam parks clear()
// until the test releases it, then resolves the abort ({ committed: false })
// without ever running a transaction — the shape a clear that never reaches
// a definite commit takes in the wild, scriptable mid-flight.
class HeldAbortingClearAdapter extends SessionCacheIndexedDB {
  readonly #open: Promise<void>;

  constructor(open: Promise<void>) {
    super();
    this.#open = open;
  }

  override clear(): Promise<{ committed: boolean; epoch: number }> {
    // The abort's epoch is unused by the caller; the transaction never ran,
    // so no epoch was observed.
    return this.#open.then(() => ({ committed: false, epoch: 0 }));
  }
}

let releaseHeldAbortingClearFn: (() => void) | undefined;

function installHeldAbortingClearAdapter(): HeldAbortingClearAdapter {
  releaseHeldAbortingClearFn = undefined;
  const opened = new Promise<void>((resolve) => {
    releaseHeldAbortingClearFn = resolve;
  });
  const adapter = new HeldAbortingClearAdapter(opened);
  installCacheAdapter(adapter);
  return adapter;
}

function releaseHeldAbortingClear(): void {
  const release = releaseHeldAbortingClearFn;
  releaseHeldAbortingClearFn = undefined;
  release?.();
}

// The faulted-clear adapter: beforeCommit("clear") throws, so the clear's
// transaction aborts and reads as { committed: false } — the honest no-op
// every tab must revert from (installFaultedDeleteAdapter's pattern).
function installFaultedClearAdapter(): void {
  installCacheAdapter(
    new SessionCacheIndexedDB({
      beforeCommit: (operation) => {
        if (operation === "clear") {
          throw new Error("clear storage fault");
        }
      },
    }),
  );
}

describe("the clear", () => {
  it("an unobserved clear reverts its own arm and never broadcasts a late completion", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const { adapter } = cacheTestBed();
    const { posted } = installTestCacheChannel();
    const fake = connectFakeClient();
    fake.on("thread/read", echoingReadHandler());
    await threadsStore.getState().ensureThread("local:uncertain-clear");
    const hold = holdNextWriteTransaction(["records", "meta"]);
    const clearing = clearCachedSessions();
    try {
      await hold.reached;
      await vi.advanceTimersByTimeAsync(10_001);
      expect(threadsStore.getState().clearInFlight).toBeUndefined();
      expect(await clearing).toEqual({ committed: false });
      expect(threadsStore.getState().cacheSuppressed.has("local:uncertain-clear")).toBe(false);
      expect(posted).toEqual([]);
      hold.release();
      await settleProjectionWorkForTests();
      expect(posted).toEqual([]);
      // The fake committed but withheld delivery. The durable epoch, not the
      // abandoned action, still prevents re-persistence after reconnecting.
      await adapter.count();
      emitHistoryUpdated("local:uncertain-clear", { fold: "after-timeout", entry: 2 });
      await vi.advanceTimersByTimeAsync(1_000);
      await settleProjectionWorkForTests();
      expect(await adapter.count()).toBe(0);
      expect(threadsStore.getState().cacheSuppressed.has("local:uncertain-clear")).toBe(true);
    } finally {
      hold.release();
      await clearing;
      vi.useRealTimers();
    }
  }, 3_000);

  it("14b ordering: in-memory epoch and timer cancellation happen before the awaited transaction", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // The suppression the clear arms is per open lease, and a lease is what
      // a pane's arming lookup captured on a cache hit — a seeded record is
      // what gives this pane one (the missed-message suite's own premise).
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:held");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 40 } }));
      await threadsStore.getState().ensureThread("local:held");
      await resolveEverything(fake);
      emitHistoryUpdated("local:held", { fold: "turn_k" }); // a pending timer exists
      installHeldClearAdapter(); // the singleton seam answers clear() only when the test releases it
      const clearing = clearCachedSessions();
      // The in-memory step already ran while the clear's transaction never
      // started: the in-flight marker holds the armed epoch, and it is what
      // refuses the ref's writes until the outcome lands.
      expect(threadsStore.getState().clearInFlight).toBe(1);
      await vi.advanceTimersByTimeAsync(6_000); // the timer would have fired long ago
      expect(await cacheRecord("local:held")).toBeDefined(); // the pre-clear write died by timer cancellation
      emitHistoryUpdated("local:held", { fold: "turn_k2" }); // a publication during the flight
      await vi.advanceTimersByTimeAsync(6_000);
      expect((await cacheRecord("local:held"))?.threadId).toBe("thr_1"); // the in-flight marker refused it: the seed's record stands
      releaseHeldClear(); // the transaction commits
      expect(await clearing).toEqual({ committed: true });
      expect(threadsStore.getState().clearInFlight).toBeUndefined(); // the marker went with the commit
      expect(await cacheRecord("local:held")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("14b revert: a clear that never reaches a definite commit sends no message and reverts, so open refs resume caching", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { posted } = installTestCacheChannel();
      // The ref must hold a lease for the clear's arm and the revert's disarm
      // to have anything to do — a seeded record opens the pane with one.
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:resume");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 40 } }));
      await threadsStore.getState().ensureThread("local:resume");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:resume")).toBeDefined();
      installFaultedClearAdapter(); // beforeCommit("clear") throws: the honest no-op
      expect(await clearCachedSessions()).toEqual({ committed: false });
      expect(posted).toEqual([]); // commit-gated: no message for a clear that did not happen
      expect(threadsStore.getState().cacheSuppressed.has("local:resume")).toBe(false); // the abort left nothing behind: the arm never touched cacheSuppressed
      emitHistoryUpdated("local:resume", { fold: "turn_m" }); // suppression disarmed: caching resumes
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:resume")).toBeDefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("the stacked clear: an aborted clear's revert never disarms a committed clear's suppression", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // The ref opens against a seeded record so it holds the lease the
      // first clear's suppression arms.
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:stack");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 40 } }));
      await threadsStore.getState().ensureThread("local:stack");
      await resolveEverything(fake);
      // Clear #1 commits: the open lease's ref is suppressed through its
      // final release, and the durable epoch is now 1.
      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(threadsStore.getState().cacheSuppressed.has("local:stack")).toBe(true);
      installFaultedClearAdapter(); // clear #2 aborts: beforeCommit("clear") throws
      expect(await clearCachedSessions()).toEqual({ committed: false });
      // The abort reverts exactly what clear #2 armed — nothing, the ref was
      // already suppressed by clear #1 — so an earlier committed clear's
      // suppression stands and the pane's next write stays refused.
      expect(threadsStore.getState().cacheSuppressed.has("local:stack")).toBe(true);
      emitHistoryUpdated("local:stack", { fold: "turn_x" }); // the pane keeps receiving pushes
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:stack")).toBeUndefined(); // no content re-cached after a committed clear
      // The final release is still what lifts the suppression the committed clear armed.
      threadsStore.getState().releaseThread("local:stack");
      expect(threadsStore.getState().cacheSuppressed.has("local:stack")).toBe(false);
      expect(threadsStore.getState().cacheLeases.has("local:stack")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("the in-flight abort: a sibling deletion message that arrived during the flight keeps its suppression when the clear aborts", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { peer } = installTestCacheChannel();
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:race");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 40 } }));
      await threadsStore.getState().ensureThread("local:race");
      await resolveEverything(fake);
      installHeldAbortingClearAdapter(); // park clear(); the release resolves the abort
      const clearing = clearCachedSessions();
      // While the clear's transaction is pending, a sibling tab's deletion
      // message arrives: it arms the suppression itself and heals storage.
      peer.post({ version: 1, sourceId: "other-tab", kind: "deletion", refs: ["local:race"] });
      await settleProjectionWorkForTests();
      expect(threadsStore.getState().cacheSuppressed.has("local:race")).toBe(true); // the deletion message's own arm
      expect(await cacheRecord("local:race")).toBeUndefined(); // and its heal removed the record
      releaseHeldAbortingClear(); // the clear aborts: committed: false
      expect(await clearing).toEqual({ committed: false });
      // (a) The abort left the deletion message's suppression standing.
      expect(threadsStore.getState().cacheSuppressed.has("local:race")).toBe(true);
      // (b) The T9 sibling property: a post-abort fold never resurrects the record the sibling deleted.
      emitHistoryUpdated("local:race", { fold: "turn_r" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:race")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("the in-flight abort: a newer epoch observed during the flight survives it, with the suppression it armed", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // Both hit and cold-loaded refs own leases. A lookup of another ref
      // discovers the newer durable epoch while this tab's clear is pending.
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:epoch_a");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 40 } }));
      await threadsStore.getState().ensureThread("local:epoch_a");
      await threadsStore.getState().ensureThread("local:epoch_b");
      await resolveEverything(fake);
      await adapter.deleteRecords(["local:epoch_a"]); // observe only this tab's writes for the leased ref
      await bumpCacheEpochRow(indexedDB, 5); // a sibling's clear committed; the message never arrives
      const held = installHeldAbortingClearAdapter();
      // The double parks clear() before any base call, so it would never
      // open its connection; a real aborting clear ran against an open one.
      // Warm it, or the write gate's isOpen check refuses the mid-flight
      // write this test needs to abort on the newer epoch.
      await held.get("local:epoch_warm", Date.now());
      const clearing = clearCachedSessions();
      await threadsStore.getState().ensureThread("local:epoch_observer");
      emitHistoryUpdated("local:epoch_b", { fold: "turn_e" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:epoch_b")).toBeUndefined(); // the write aborted on the newer epoch...
      expect(threadsStore.getState().cacheSuppressed.has("local:epoch_a")).toBe(true); // ...and the backstop armed the leased ref
      releaseHeldAbortingClear();
      expect(await clearing).toEqual({ committed: false });
      // The abort neither disarmed the backstop's suppression...
      expect(threadsStore.getState().cacheSuppressed.has("local:epoch_a")).toBe(true);
      emitHistoryUpdated("local:epoch_a", { fold: "turn_e2" }); // the leased ref's post-abort fold stays refused
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:epoch_a")).toBeUndefined();
      // The cold ref is suppressed too, through its final release. A fresh
      // lifetime carries the observed epoch and can cache again.
      emitHistoryUpdated("local:epoch_b", { fold: "turn_e3" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:epoch_b")).toBeUndefined();
      threadsStore.getState().releaseThread("local:epoch_b");
      await threadsStore.getState().ensureThread("local:epoch_b");
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:epoch_b")).toBeDefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("14c lifecycle: an open pane's post-clear notification write is refused until its final release, and a re-open caches again", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      // The suppression the clear arms is per open lease, so the pane opens
      // against a seeded record and holds the lease its arming captured.
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:lc");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 40 } }));
      await threadsStore.getState().ensureThread("local:lc");
      await resolveEverything(fake);
      expect(await clearCachedSessions()).toEqual({ committed: true });
      emitHistoryUpdated("local:lc", { fold: "turn_n" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:lc")).toBeUndefined(); // suppressed through the open lease
      threadsStore.getState().releaseThread("local:lc"); // final release ends the suppression
      // The flush side (Review Focus 4): the release flush runs before the
      // removal, so it still sees the suppression armed — a suppressed ref's
      // release flush writes nothing.
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:lc")).toBeUndefined(); // the release flush wrote nothing
      await threadsStore.getState().ensureThread("local:lc"); // a deliberate re-open: fresh lease
      await resolveEverything(fake);
      emitHistoryUpdated("local:lc", { fold: "turn_o" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:lc")).toBeDefined(); // the feature working, not the remedy failing
    } finally {
      vi.useRealTimers();
    }
  });

  it("14c sibling: a ref closed before the clear and re-opened after its commit is a fresh lease and caches again", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:fresh");
      await resolveEverything(fake);
      threadsStore.getState().releaseThread("local:fresh"); // closed BEFORE the clear: the lease is gone
      expect(await clearCachedSessions()).toEqual({ committed: true }); // the durable epoch is now 1
      await threadsStore.getState().ensureThread("local:fresh"); // re-opened AFTER the commit: a fresh lease by construction
      await resolveEverything(fake);
      emitHistoryUpdated("local:fresh", { fold: "turn_f" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:fresh")).toBeDefined(); // the feature working, not the remedy failing
    } finally {
      vi.useRealTimers();
    }
  });

  it("14d the missed message: the delayed clear delivery changes nothing after the backstop already armed", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { peer } = installTestCacheChannel();
      // The lease the backstop's suppression arm needs: the pane's arming
      // lookup captured the durable epoch, so a seeded record opens it (the
      // missed-message backstop suite's own premise).
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:mm");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 40 } }));
      await threadsStore.getState().ensureThread("local:mm");
      await resolveEverything(fake);
      await adapter.deleteRecords(["local:mm"]); // observe only this tab's writes from here
      await bumpCacheEpochRow(indexedDB, 3); // a sibling's clear committed; this tab never got the message
      emitHistoryUpdated("local:mm", { fold: "turn_b" });
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await cacheRecord("local:mm")).toBeUndefined(); // the aborted write armed the suppression itself
      expect(threadsStore.getState().cacheSuppressed.has("local:mm")).toBe(true);
      peer.post({ version: 1, sourceId: "other-tab", kind: "clear", epoch: 3 }); // the message finally arrives
      await settleProjectionWorkForTests();
      expect(threadsStore.getState().cacheSuppressed.has("local:mm")).toBe(true); // idempotent: nothing double-armed, nothing disarmed
      expect(await cacheRecord("local:mm")).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("the session-content clear (applyClearResponse) deletes the ref's record in the same step", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 1 } }));
      await threadsStore.getState().ensureThread("local:cc");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(await cacheRecord("local:cc")).toBeDefined();
      await driveThreadClear("local:cc", fake); // the thread/clear driver threads.test.ts's own coverage uses
      expect(await cacheRecord("local:cc")).toBeUndefined(); // gone in the same step: no pre-clear shell on the next reload
      expect(threadsStore.getState().threads.get("local:cc")?.history).toBeUndefined(); // the model is the bare hydrate
    } finally {
      vi.useRealTimers();
    }
  });

  it("the session-content clear retires shell metadata, admits paging, and re-caches after reopen", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const ref = "local:clear_shell";
      const { adapter } = cacheTestBed();
      await seedCache(adapter, ref);
      const fake = connectFakeClient();
      let resolveRead: ((response: ThreadReadResponse) => void) | undefined;
      const readArmed = nextHandledRequest(
        fake,
        "thread/read",
        () =>
          new Promise<ThreadReadResponse>((resolve) => {
            resolveRead = resolve;
          }),
      );
      const pending = threadsStore.getState().ensureThread(ref);
      const params = await readArmed;
      expect(threadsStore.getState().cacheShellRefs.has(ref)).toBe(true);
      expect(threadsStore.getState().cacheAnchors.has(ref)).toBe(true);

      await driveThreadClear(ref, fake);
      expect(threadsStore.getState().threads.get(ref)?.history).toBeUndefined();
      expect(threadsStore.getState().cacheShellRefs.has(ref)).toBe(false);
      expect(threadsStore.getState().cacheAnchors.has(ref)).toBe(false);
      expect(await cacheRecord(ref)).toBeUndefined();
      if (resolveRead === undefined) throw new Error("the pre-clear read must still be pending");
      resolveRead(readResponse(ref, { requestGeneration: params.requestGeneration, turns: [turnFixture("old")] }));
      await pending;
      expect(threadsStore.getState().threads.get(ref)?.history).toBeUndefined(); // the stale read did not replace the clear

      // A clear has no older cursor. Demand must still reach the ready wait,
      // rather than return at the stale shell gate; reconnect supplies history.
      fake.emitStateChange("connecting");
      const listeners = fake.listenerCount;
      const paging = threadsStore.getState().loadOlderTurns(ref);
      expect(fake.listenerCount).toBeGreaterThan(listeners);
      fake.on(
        "thread/read",
        echoingReadHandler({
          snapshot: { incarnation: "inc-cleared", length: 2 },
          turns: [turnFixture("new")],
          olderCursor: "older-cleared",
        }),
      );
      fake.on("thread/turns/list", () => ({
        ...olderPageResponse({ turns: [turnFixture("older")] }),
        snapshot: { incarnation: "inc-cleared", length: 2 },
      }));
      fake.emitReady();
      await paging;
      expect(fake.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
      expect(
        threadsStore
          .getState()
          .threads.get(ref)
          ?.turns.map((turn) => turn.id),
      ).toContain("older");

      threadsStore.getState().releaseThread(ref);
      await threadsStore.getState().ensureThread(ref);
      await vi.advanceTimersByTimeAsync(1_000);
      expect((await cacheRecord(ref))?.history.incarnation).toBe("inc-cleared");
    } finally {
      vi.useRealTimers();
    }
  });

  it("13, the same-tab window: a clear committing between the lookup's transaction and its publish discards the result through the in-memory epoch check", async () => {
    // The gated bed parks the pane's own lookup (the load seam's held
    // transaction): its transaction already read the pre-clear record and
    // the pre-clear epoch row, and its publish happens only after a
    // committed clear moved the in-memory epoch view — the window scenario
    // 13 names. No fake timers: nothing here rides a debounce window.
    const { gate, adapter } = cacheTestBed({ gated: true });
    await seedCache(adapter, "local:sc");
    const fake = connectFakeClient();
    fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-2", length: 2 } }));
    const pending = threadsStore.getState().ensureThread("local:sc");
    await clearCachedSessions(); // commits while the lookup is held mid-flight: the epoch view is now 1
    gate.release(); // the lookup resolves with its pre-clear capture (epoch 0)
    await pending;
    // The discard, through the epochMatch === false branch (threads.ts:4398-4400):
    // the capture does not match the clear's epoch view, so the found branch
    // publishes no shell and arms nothing.
    expect(threadsStore.getState().cacheShellRefs.has("local:sc")).toBe(false);
    expect(threadsStore.getState().cacheLeases.get("local:sc")).toBe(0);
    expect(threadsStore.getState().cacheAnchors.has("local:sc")).toBe(false);
    expect(threadsStore.getState().cacheSuppressed.has("local:sc")).toBe(true);
    // The reconcile proceeded as a cold read: the model is the wire's
    // (inc-2), not the pre-clear record's (the seed's identity is inc-1).
    expect(threadsStore.getState().threads.get("local:sc")?.history?.incarnation).toBe("inc-2");
  });

  it("13, a lookup captured before a real sibling clear never re-persists after reconciliation", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const { adapter } = cacheTestBed();
    let releaseLookup: (() => void) | undefined;
    let capturedLookup: (() => void) | undefined;
    const captured = new Promise<void>((resolve) => {
      capturedLookup = resolve;
    });
    const release = new Promise<void>((resolve) => {
      releaseLookup = resolve;
    });
    try {
      await seedCache(adapter, "local:sib");
      const get = adapter.get.bind(adapter);
      vi.spyOn(adapter, "get").mockImplementationOnce(async (ref, now) => {
        const found = await get(ref, now);
        expect(found?.epoch).toBe(0);
        expect(found?.record.history.incarnation).toBe("inc-1");
        capturedLookup?.();
        await release;
        return found;
      });
      const sibling = new SessionCacheIndexedDB();
      beds.push(sibling);
      const fake = connectFakeClient();
      fake.on("thread/read", parkReloadRead);
      const pending = threadsStore.getState().ensureThread("local:sib");
      await captured;
      expect(await sibling.clear()).toEqual({ committed: true, epoch: 1 });
      expect(await sibling.count()).toBe(0);
      // No channel message is delivered. Release the genuinely pre-clear capture.
      releaseLookup?.();
      await nextModelPublished("local:sib");
      expect(threadsStore.getState().cacheShellRefs.has("local:sib")).toBe(true);
      await vi.advanceTimersByTimeAsync(6_000);
      expect(await sibling.count()).toBe(0); // the shell itself is never written
      resolveReloadRead(readResponse("local:sib", { snapshot: { incarnation: "inc-2", length: 2 } }));
      await pending;
      expect(threadsStore.getState().cacheShellRefs.has("local:sib")).toBe(false);
      expect(threadsStore.getState().threads.get("local:sib")?.history?.incarnation).toBe("inc-2");
      await vi.advanceTimersByTimeAsync(1_000);
      await settleProjectionWorkForTests();
      expect(threadsStore.getState().cacheSuppressed.has("local:sib")).toBe(true);
      emitHistoryUpdated("local:sib", { fold: "later", entry: 3 });
      await vi.advanceTimersByTimeAsync(6_000);
      threadsStore.getState().releaseThread("local:sib");
      await settleProjectionWorkForTests();
      expect(await sibling.get("local:sib", Date.now())).toBeUndefined();
      expect(await sibling.count()).toBe(0);
    } finally {
      releaseLookup?.();
      vi.useRealTimers();
    }
  });
});

// The degradation bed (Task 12; spec, "Failure and degradation", Testing
// scenario 17): the parity contract the whole feature hangs on — an adapter
// whose every open stalls leaves the pane's behavior identical to today's
// reload. The wedged lookup can only lose its own 250 ms deadline race and
// every failure is a cache miss, so the cold path serves the pane exactly as
// it did before the cache existed: a full latest-window read with no
// heldSnapshot, scroll-back pages fetched over the wire as before, and no
// shell ever published.
describe("degradation", () => {
  it("an adapter that always fails leaves behavior identical to today's reload", async () => {
    // Only the clock functions are faked: fake-indexeddb delivers every
    // IndexedDB event on setImmediate, which a bare useFakeTimers() fakes
    // too — freezing the mutation outbox's post-publish work mid-test. The
    // toFake scoping is the deadline test's own pattern.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      installWedgedCacheAdapter(); // every open stalls; every operation is a miss
      const fake = connectFakeClient();
      const listCalls: string[] = [];
      fake.on("thread/turns/list", (params) => {
        if (params.ref === undefined) throw new Error("thread/turns/list params must carry a ref");
        listCalls.push(params.ref);
        return olderPageResponse();
      });
      fake.on("thread/read", echoingReadHandler({ snapshot: { incarnation: "inc-1", length: 5 }, olderCursor: "cur" }));
      // The wedged lookup can only lose its own 250 ms deadline race, and
      // that deadline is a faked timer: drive it, then await the cold read it
      // armed (the deadline test's shape — awaiting the ensureThread outright
      // would deadlock on the faked deadline).
      const pending = threadsStore.getState().ensureThread("local:thr_1");
      await vi.advanceTimersByTimeAsync(250);
      await pending;
      expect(threadsStore.getState().threads.get("local:thr_1")?.history?.incarnation).toBe("inc-1"); // full window read
      const call = fake.calls.find((c) => c.method === "thread/read");
      expect((call?.params as { heldSnapshot?: unknown } | undefined)?.heldSnapshot).toBeUndefined(); // no heldSnapshot
      await threadsStore.getState().loadOlderTurns("local:thr_1");
      expect(listCalls).toEqual(["local:thr_1"]); // scroll-back pages fetched as before
      expect(threadsStore.getState().cacheShellRefs.size).toBe(0); // no shell ever published
    } finally {
      vi.useRealTimers();
      // The wedged lookup's open never settles, so the storage work it
      // registered can never settle either: forget it (the tracker's own
      // reset contract) or every later settle in this file spins on a
      // registration whose result no longer matters.
      clearProjectionWorkForTests();
      restoreCacheAdapter();
    }
  });
});

// Whole-branch regressions, exercising the authoritative read and durable cache.
describe("cache privacy and history regressions", () => {
  it("a deadline-fallback lifetime stays suppressed through its final release", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const { adapter, gate } = cacheTestBed({ gated: true });
    try {
      await seedCache(adapter, "local:deadline-clear");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler());
      const pending = threadsStore.getState().ensureThread("local:deadline-clear");
      await vi.advanceTimersByTimeAsync(250);
      await pending;
      gate.release();
      expect(threadsStore.getState().cacheShellRefs.has("local:deadline-clear")).toBe(false);
      expect(await clearCachedSessions()).toEqual({ committed: true });
      emitHistoryUpdated("local:deadline-clear", { fold: "after-clear", entry: 2 });
      await vi.advanceTimersByTimeAsync(1_000);
      threadsStore.getState().releaseThread("local:deadline-clear");
      await settleProjectionWorkForTests();
      expect(await adapter.count()).toBe(0);
    } finally {
      gate.release();
      vi.useRealTimers();
    }
  }, 3_000);

  it("a stale lookup cannot downgrade the epoch used by a subsequent fresh lifetime", async () => {
    const { adapter, gate } = cacheTestBed({ gated: true });
    await seedCache(adapter, "local:stale-lookup");
    const fake = connectFakeClient();
    fake.on("thread/read", echoingReadHandler());
    const pending = threadsStore.getState().ensureThread("local:stale-lookup");
    expect(await clearCachedSessions()).toEqual({ committed: true });
    gate.release();
    await pending;
    await threadsStore.getState().ensureThread("local:after-stale");
    expect(threadsStore.getState().cacheLeases.get("local:after-stale")).toBe(1);
    threadsStore.getState().releaseThread("local:after-stale");
    await settleProjectionWorkForTests();
    expect(await adapter.get("local:after-stale", Date.now())).toBeDefined();
  }, 3_000);

  it("a cold-loaded open ref stays absent after a committed cache clear", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { adapter } = cacheTestBed();
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ turns: [turnFixture("cold")] }));
      await threadsStore.getState().ensureThread("local:cold-probe");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1000);
      expect(await adapter.count()).toBe(1);
      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(await adapter.count()).toBe(0);
      emitHistoryUpdated("local:cold-probe", { fold: "after-clear", entry: 2 });
      await vi.advanceTimersByTimeAsync(1000);
      await settleProjectionWorkForTests();
      expect(await adapter.count()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it.each(["hit", "miss"])("a lookup %s observing a newer clear epoch suppresses older open leases", async (kind) => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { adapter } = cacheTestBed();
      await seedCache(adapter, "local:old-lease");
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler());
      await threadsStore.getState().ensureThread("local:old-lease");
      expect(threadsStore.getState().cacheLeases.get("local:old-lease")).toBe(0);
      const sibling = new SessionCacheIndexedDB();
      beds.push(sibling);
      expect(await sibling.clear()).toEqual({ committed: true, epoch: 1 });
      const refill = seededRecord("local:fresh-lookup", {});
      if (kind === "hit") expect(await sibling.put(refill, 1, Date.now())).toEqual({ outcome: "written" });
      await threadsStore.getState().ensureThread("local:fresh-lookup");
      expect(threadsStore.getState().cacheSuppressed.has("local:old-lease")).toBe(true);
      emitHistoryUpdated("local:old-lease", { fold: "after-sibling-clear", entry: 3 });
      await vi.advanceTimersByTimeAsync(1000);
      await settleProjectionWorkForTests();
      expect(await adapter.get("local:old-lease", Date.now())).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });
  it("gap replacement keeps a fold tail within a turn that spans the window", async () => {
    await seedPositionedRecord("local:gap");
    await reloadWithFailedFirstRead("local:gap");
    const { model, history } = requireOpenHistory("probe", "local:gap");
    requireConnectedClient("probe").emitNotification({
      method: "history/updated",
      params: {
        ref: "local:gap",
        threadId: model.threadId,
        bootGeneration: history.bootGeneration,
        epoch: history.epoch,
        snapshot: { incarnation: history.incarnation, length: history.length },
        turns: [{ id: "turn_w", status: "completed", itemsView: "full", version: 3 }],
        items: [
          {
            id: "fold-tail-50",
            type: "agentMessage",
            turnId: "turn_w",
            text: "latest live content",
            status: "completed",
            position: { entry: 50, item: 0 },
          },
        ],
      },
    } as AnyNotification);
    expect(
      threadsStore
        .getState()
        .threads.get("local:gap")
        ?.history?.turns.flatMap((t) => t.items.map((i) => i.id)),
    ).toContain("fold-tail-50");
    runScheduledHydrationRetryForThisFile();
    await awaitReadArmed();
    resolveReloadRead(windowResponse(40, 3, "cur-new"));
    await settleReload("local:gap");
    expect(
      threadsStore
        .getState()
        .threads.get("local:gap")
        ?.history?.turns.flatMap((t) => t.items.map((i) => i.id)),
    ).toContain("fold-tail-50");
  });

  it("an oversized release flush cannot memoize a later model lifetime", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const adapter = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), maxBytes: 1000 });
      installCacheAdapter(adapter);
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ turns: [turnFixture("large", "x".repeat(1200))] }));
      await threadsStore.getState().ensureThread("local:oversize-release");
      await resolveEverything(fake);
      threadsStore.getState().releaseThread("local:oversize-release");
      await settleProjectionWorkForTests();
      expect(await adapter.count()).toBe(0);
      fake.on("thread/read", echoingReadHandler({ turns: [turnFixture("small")] }));
      await threadsStore.getState().ensureThread("local:oversize-release");
      await resolveEverything(fake);
      await vi.advanceTimersByTimeAsync(1000);
      await settleProjectionWorkForTests();
      expect(await adapter.count()).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("an ordinary identity replacement clears the oversized model memo", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const adapter = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), maxBytes: 1000 });
      installCacheAdapter(adapter);
      const fake = connectFakeClient();
      fake.on("thread/read", echoingReadHandler({ turns: [turnFixture("large", "x".repeat(1200))] }));
      await threadsStore.getState().ensureThread("local:oversize-replace");
      await vi.advanceTimersByTimeAsync(1000);
      await settleProjectionWorkForTests();
      expect(await adapter.count()).toBe(0);
      fake.on(
        "thread/read",
        echoingReadHandler({ snapshot: { incarnation: "inc-new", length: 2 }, turns: [turnFixture("small")] }),
      );
      await threadsStore.getState().refreshThread("local:oversize-replace");
      expect(threadsStore.getState().threads.get("local:oversize-replace")?.history?.incarnation).toBe("inc-new");
      await vi.advanceTimersByTimeAsync(1000);
      await settleProjectionWorkForTests();
      expect(await adapter.count()).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });
  it("a corrupt history object is a deleted cache miss rather than a shell crash", async () => {
    const { adapter } = cacheTestBed();
    await seedCache(adapter, "local:malformed");
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open("evener-session-cache", 1);
      request.onsuccess = () => {
        const db = request.result;
        const tx = db.transaction("records", "readwrite");
        tx.objectStore("records").put({ ...seededRecord("local:malformed", {}), history: {} });
        tx.oncomplete = () => {
          db.close();
          resolve();
        };
        tx.onabort = () => reject(tx.error);
      };
      request.onerror = () => reject(request.error);
    });
    const found = await adapter.get("local:malformed", Date.now());
    expect.soft(found).toBeUndefined();
    expect.soft(await adapter.count()).toBe(0);
    const fake = connectFakeClient();
    fake.on("thread/read", echoingReadHandler());
    await expect.soft(threadsStore.getState().ensureThread("local:malformed")).resolves.toBeUndefined();
    expect.soft(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  });
});
