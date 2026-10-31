import { deferred } from "@evener/appwire-client/testing/deferred";
import { ALL_CAPABILITIES as CAPABILITIES, type TestThreadOverrides, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import { hydrateThread, type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { MutationDispatcher } from "@evener/appwire-client/state/mutation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { act, cleanup, waitFor } from "@testing-library/react";
import { type IDBDatabase, IDBFactory, IDBKeyRange, IDBVersionChangeEvent } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { resetConnectionStoreForTests } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdNextWriteTransaction, wedgeDatabaseOpens } from "./testing/stalledIndexedDB";
import {
  discardRecoveryMutation,
  installHydrationRetrySchedulerForTests,
  notifyReadyForMutationDispatch,
  resetThreadsStoreForTests,
  resumeStopBaseline,
  retryBlockedMutation,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "./threads";
import {
  clearResponse,
  connectMutationClient,
  ensureActiveMutationTarget,
  flushIndexedDBUntil,
  flushMicrotasks,
  mutationReceipt,
  onTurnStart,
  readResponse,
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
  // The cancel path names the ids it cancels, so it must resolve them: the
  // design leaves ids only for cancel paths that cannot name their rows.
  test("a Stop that cancels a durably committed row does not refuse the next send", async () => {
    const indexedDB = new IDBFactory();
    const databaseName = "evener-mutation-outbox-stop-names-canceled-ids";
    const { storage, fake } = await hydratedFallbackTarget({ indexedDB, databaseName });
    await nextMacrotask();
    onTurnStart(fake);
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
    onTurnStart(fake);
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
    onTurnStart(fake);
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
    onTurnStart(fake);

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
    onTurnStart(fake);
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
    onTurnStart(fake);

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
    onTurnStart(fake);
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
    onTurnStart(fake);

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
    onTurnStart(fake);

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

  // The retargeted-resend seed the #5182 fence tests share: a recovery row a
  // peer retargeted to ref_a, resent while its pane still displayed ref_b - the
  // fresh outbox row crosses both refs, born under the destination identity
  // the Retry press must match.
  async function seedRetargetedResendToRefA(
    storage: MutationOutboxIndexedDB,
  ): Promise<{ clientMutationId: string; threadId: string }> {
    const original = await storage.enqueueIntent({
      targetRef: "ref_b",
      threadId: "thr_b",
      method: "turn/start",
      payload: { ref: "ref_b", input: [{ type: "text", text: "hello" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    await storage.retargetOutbox("ref_b", "ref_a", { threadId: "thr_ref_a" });
    const sent = await storage.resendRecoveryResult(
      original.clientMutationId,
      {
        targetRef: "ref_a",
        threadId: "thr_ref_a",
        method: "turn/start",
        payload: { ref: "ref_a", input: [{ type: "text", text: "hello" }] },
        attachments: [],
        optimisticDisplay: null,
      },
      undefined,
      undefined,
      undefined,
      { stopEpoch: 0, displayedRef: "ref_b" },
    );
    if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
    return { clientMutationId: sent.record.clientMutationId, threadId: "thr_ref_a" };
  }

  // #5182 M1, the store-level shape: an attempted retargeted resend parked
  // blockedUnknown behind a crossed Stop. The reconcile fence holds it
  // delivery-uncertain on purpose, so when the hub never accepted the
  // mutation no receipt can ever settle it - the user's explicit Retry click
  // is the one authorization that may reauthorize it, and the send must
  // proceed and dispatch.
  test("a Retry reauthorizes a Stop-crossed blockedUnknown retargeted resend and dispatches it", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    onTurnStart(fake);
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    const seeded = await seedRetargetedResendToRefA(storage);
    const clientMutationId = seeded.clientMutationId;

    // The resend attempts, its outcome goes unknown, and a Stop lands on the
    // still-displayed ref_b: the cancel scan skips the attempted row and the
    // reconcile fence holds it - no automatic path reopens it.
    await storage.markAttempted(clientMutationId);
    await storage.markUnknown(clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel({
      targetRef: "ref_b",
      method: "turn/interrupt",
      payload: { ref: "ref_b" },
      attachments: [],
      optimisticDisplay: { method: "turn/interrupt" },
    });
    expect((await storage.getOutbox(clientMutationId))?.state).toBe("blockedUnknown");
    expect(await storage.restoreProvenAbsent("ref_a", new Set())).not.toContain(clientMutationId);

    // The user's explicit Retry: the click reauthorizes the parked row under
    // its original mutation ID and the send proceeds and dispatches.
    expect(await retryBlockedMutation(clientMutationId)).toBe(true);
    await flushUntilArrived("the reauthorized resend to dispatch", () =>
      fake.calls.some(
        (call) =>
          call.method === "turn/start" &&
          (call.params as { clientMutationId?: string }).clientMutationId === clientMutationId,
      ),
    );
    // The re-sent row is the SAME send: the row left its parked state.
    expect((await storage.getOutbox(clientMutationId))?.state).not.toBe("blockedUnknown");
  });

  // #5182 M2, the store-level shape: for a canceled retargeted resend, a Stop
  // on the displayed ref landing inside the Retry's capture-to-release
  // window. The cancel scan skips the already-canceled row and the
  // destination epoch never moved - the release must validate the displayed
  // half of its capture pair or the baseline refresh absorbs the newer Stop
  // and the row dispatches despite it.
  test("a Retry whose release lands after a Stop on the displayed ref stays canceled and never dispatches", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    onTurnStart(fake);
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    const seeded = await seedRetargetedResendToRefA(storage);
    const clientMutationId = seeded.clientMutationId;

    // A Stop on the still-displayed ref_b cancels the unattempted resend.
    await storage.enqueueInterruptAndCancel({
      targetRef: "ref_b",
      method: "turn/interrupt",
      payload: { ref: "ref_b" },
      attachments: [],
      optimisticDisplay: { method: "turn/interrupt" },
    });
    expect((await storage.getOutbox(clientMutationId))?.state).toBe("canceled");

    // The release seam: the retry has read the row, captured its pair, and
    // passed its press-time checks; its release write waits at the seam.
    const realReleaseCanceled = storage.releaseCanceled.bind(storage);
    const releaseReached = deferred<void>();
    const releaseGate = deferred<void>();
    storage.releaseCanceled = (...args: Parameters<typeof realReleaseCanceled>) => {
      releaseReached.resolve();
      return releaseGate.promise.then(() => realReleaseCanceled(...args));
    };

    // The user's Retry click.
    const retry = retryBlockedMutation(clientMutationId);
    await releaseReached.promise;

    // The second tab's Stop lands on the DISPLAYED ref inside the window:
    // its cancel scan skips the already-canceled row, so the displayed
    // epoch's bump is the one trace the release must catch.
    const tabB = new MutationOutboxIndexedDB();
    try {
      await tabB.enqueueInterruptAndCancel({
        targetRef: "ref_b",
        method: "turn/interrupt",
        payload: { ref: "ref_b" },
        attachments: [],
        optimisticDisplay: { method: "turn/interrupt" },
      });

      // The release finally issues - after the Stop, the finding's order. The
      // displayed half of the capture refuses it: the row stays canceled for
      // the Stop that claimed it, and nothing dispatches.
      releaseGate.resolve();
      expect(await retry).toBe(false);
      expect((await storage.getOutbox(clientMutationId))?.state).toBe("canceled");
      await flushIndexedDBUntil(() => false);
      expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
    } finally {
      tabB.close();
    }

    // Liveness control: a Retry pressed AFTER the newer Stop captures the
    // post-Stop displayed epoch and is the deliberate post-Stop send - it
    // releases and dispatches.
    expect(await retryBlockedMutation(clientMutationId)).toBe(true);
    await flushUntilArrived("the deliberate post-Stop retry to dispatch", () =>
      fake.calls.some((call) => call.method === "turn/start"),
    );
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
