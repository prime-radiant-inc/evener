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
  ThreadModel,
  ThreadReadResponse,
  Turn,
  TurnModel,
  TurnQueueResponse,
} from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { clearProjectionWorkForTests, settleProjectionWorkForTests } from "./projectionWork";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { holdNextWriteTransaction, neverSettlingRequest } from "./testing/stalledIndexedDB";
import {
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
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

// seedCache writes a CachedSessionRecord through a SessionCacheIndexedDB on
// the global fake factory — until Task 6 lands the debounced write seam this
// is how a "prior tab's" record exists at all. threadId is pinned to "thr_1"
// (not the wire fixture's thr_<ref> derivation) because the replay assertion
// fences a Clear on the CACHED thread id: the record is the authority the
// shell paints, so its id is the one the test names.
async function seedCache(adapter: SessionCacheIndexedDB, ref: string, overrides: SeedOverrides = {}): Promise<void> {
  const record: CachedSessionRecord = {
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

// Mirrors deleteMutationDatabase for the cache database, so no record one
// test seeded survives into the next one's lookup.
async function deleteCacheDatabase(): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase("evener-session-cache");
    request.addEventListener("success", () => resolve(), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
    request.addEventListener("blocked", () => reject(new Error("session cache database deletion blocked")), {
      once: true,
    });
  });
}

// The tree's neverSettlingRequest() takes no factory — it IS the dead open
// request. A wedged-open adapter needs a factory whose open() returns it
// (the same spy wrap sessionCacheIndexedDB.test.ts uses).
function neverSettlingFactory(): IDBFactory {
  const indexedDB = new IDBFactory();
  vi.spyOn(indexedDB, "open").mockImplementation(() => neverSettlingRequest());
  return indexedDB;
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

// bumpDurableCacheEpoch moves the reserved epoch row behind the adapter's
// back, exactly sessionCacheIndexedDB.test.ts's bumpEpochRow: the durable
// fact a sibling tab's clear committed, whose channel message this tab never
// receives (the missed-message backstop's premise).
async function bumpDurableCacheEpoch(epoch: number): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.open("evener-session-cache", 1);
    request.addEventListener(
      "success",
      () => {
        const database = request.result;
        const tx = database.transaction("meta", "readwrite");
        tx.objectStore("meta").put({ ref: "__clearEpoch", epoch });
        tx.addEventListener(
          "complete",
          () => {
            database.close();
            resolve();
          },
          { once: true },
        );
        tx.addEventListener("error", () => reject(tx.error), { once: true });
      },
      { once: true },
    );
    request.addEventListener("error", () => reject(request.error), { once: true });
  });
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
// never putThreadModel).
function emitHistoryUpdated(ref: string, options: { fold: string }): void {
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
      turns: [turnFixture(options.fold, "folded live")],
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

// A bed adapter that parks the store's next lookup: the first get() arms a
// one-shot hold on the next [records, meta] readwrite transaction — the
// lookup's own, because seeding goes through put() and never arms the hold —
// so a test scripts exactly when the shared lookup settles.
class GatedCacheAdapter extends SessionCacheIndexedDB {
  readonly #arm: () => void;

  constructor(arm: () => void) {
    super();
    this.#arm = arm;
  }

  override get(ref: string, now: number): Promise<{ record: CachedSessionRecord; epoch: number } | undefined> {
    this.#arm();
    return super.get(ref, now);
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
// so the next beforeEach's deleteCacheDatabase never fires "blocked".
const beds: SessionCacheIndexedDB[] = [];

// cacheTestBed installs a cache adapter on the store's singleton seam and
// returns it with the gate. The gate exists only for { gated: true }: the
// replay path must NOT park its own lookup, so the default bed arms nothing.
function cacheTestBed(options: { gated?: boolean } = {}): CacheTestBed {
  let release: (() => void) | undefined;
  const adapter: SessionCacheIndexedDB = options.gated
    ? new GatedCacheAdapter(() => {
        if (release) return;
        release = holdNextWriteTransaction(["records", "meta"]).release;
      })
    : new SessionCacheIndexedDB();
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
  await deleteCacheDatabase();
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
    expect(threadsStore.getState().cacheLeases.has("local:thr_1")).toBe(true); // the lease is Task 10's, not the shell window's
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
      await bumpDurableCacheEpoch(5); // a sibling's clear committed; the message has not arrived
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
});
