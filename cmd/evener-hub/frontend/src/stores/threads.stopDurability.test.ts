import { deferred } from "@evener/appwire-client/testing/deferred";
import { ALL_CAPABILITIES as CAPABILITIES } from "../threadTestUtils";
import "fake-indexeddb/auto";
import { WireError } from "@evener/appwire-client";
import { MutationDispatcher } from "@evener/appwire-client/state/mutation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { cleanup } from "@testing-library/react";
import { IDBDatabase, IDBFactory, IDBKeyRange, IDBVersionChangeEvent } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { IDBUpgradeError } from "./idbConnection";
import type { MutationOutboxRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import { blockedUpgradeRequest, wedgeDatabaseOpens } from "./testing/stalledIndexedDB";
import {
  installHydrationRetrySchedulerForTests,
  notifyReadyForMutationDispatch,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "./threads";
import { ConflictError } from "./threadsErrors";
import {
  connectMutationClient,
  ensureActiveMutationTarget,
  flushIndexedDBUntil,
  flushMicrotasks,
  flushUntil,
  mutationReceipt,
  onTurnStart,
  readResponse,
  timeOutDurableEnqueues,
} from "./threadsSessionActionsTestUtils";
import { flushUntilArrived, hydratedFallbackTarget, queueIntent } from "./threadsStopDurabilityTestUtils";

interface ScheduledHydrationRetry {
  attempt: number;
  retry: () => void;
  cancelled: boolean;
}

let scheduledHydrationRetries: ScheduledHydrationRetry[] = [];

let restoreHydrationRetryScheduler: (() => void) | null = null;

let cache: SessionEntryCache;

beforeEach(async () => {
  resetConnectionStoreForTests();
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
      resetConnectionStoreForTests();
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
    onTurnStart(fake);

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
    onTurnStart(fake);

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
    onTurnStart(fake);

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
      onTurnStart(fake);
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
    onTurnStart(fake);

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
    onTurnStart(fake);

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
    onTurnStart(fake);

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
    onTurnStart(fake);
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
    onTurnStart(fake);

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
    onTurnStart(fake);
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
    onTurnStart(fake);

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
    onTurnStart(fake);
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
    onTurnStart(fake);

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
    onTurnStart(fake);

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
    onTurnStart(fake);

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
    onTurnStart(fake);

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
    onTurnStart(fake);

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
    onTurnStart(fake);
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
    onTurnStart(fake);
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
    onTurnStart(fake);
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
    onTurnStart(fake);
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
    onTurnStart(fake);
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
});

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
