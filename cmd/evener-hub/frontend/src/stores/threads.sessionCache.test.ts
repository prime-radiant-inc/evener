// The cached-shell load seam (web session-history cache plan, Task 5; spec
// docs/superpowers/specs/2026-09-29-web-session-cache-design.md, "The load
// seam: the cached shell"): the bounded lookup on ensureThread, the shell it
// paints before the read resolves, the lease epoch it captures, and the
// races the spec's scenario 13 concedes — the shared-lookup join (Review
// Focus 5), the release during the lookup, and the lookup that lost its own
// deadline race.
import "fake-indexeddb/auto";
import type {
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
  TurnModel,
} from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connectionStore } from "./connection";
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
// the replay assertion reads the turn id.
function turnFixture(id: string): TurnModel {
  return {
    id,
    status: "completed",
    items: [
      { id: `item_${id}`, turnId: id, type: "agentMessage", text: "cached turn", position: { entry: 1, item: 0 } },
    ],
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
// entry point — the same path threads.ts's requireClient() rides.
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
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

let restoreHydrationRetryScheduler: (() => void) | null = null;

beforeEach(async () => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  // The no-op scheduler keeps the production backoff's real setTimeout out of
  // this suite (threads.test.ts's own beforeEach rationale): nothing here
  // scripts a failed read, so nothing ever arms a retry.
  restoreHydrationRetryScheduler = installHydrationRetrySchedulerForTests(() => () => {});
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
    // First life: hydrate against a scripted daemonless read, let the
    // debounced write land (Task 6 makes writes work; until then this test
    // writes the record through the adapter directly), then reload.
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
});
