import { ALL_CAPABILITIES as CAPABILITIES, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import { hydrateThread, type ThreadCapabilities, type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { recoveryComposerDraft } from "../panes/session/composer/recovery/recoveryDraft";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdIndexedDBEvent } from "./testing/stalledIndexedDB";
import {
  installHydrationRetrySchedulerForTests,
  readMutationPersistence,
  resendRecoveryMutation,
  resetThreadsStoreForTests,
  retryBlockedMutation,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "./threads";
import {
  clearResponse,
  connectFakeClient,
  connectMutationClient,
  deferred,
  ensureActiveMutationTarget,
  flushIndexedDBUntil,
  mutationReceipt,
  nextHandledRequest,
  readResponse,
  settleCallerContinuations,
} from "./threadsSessionActionsTestUtils";

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
