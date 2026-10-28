import { ALL_CAPABILITIES as CAPABILITIES } from "../threadTestUtils";
import "fake-indexeddb/auto";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { cleanup, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import {
  pendingTurnEntries,
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  installHydrationRetrySchedulerForTests,
  notifyReadyForMutationDispatch,
  resendRecoveryMutation,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "./threads";
import {
  connectFakeClient,
  connectMutationClient,
  deferred,
  flushIndexedDBUntil,
  mutationReceipt,
  nextHandledRequest,
  readResponse,
  resumeOnlySnapshot,
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

// Recovery resend bypasses the send funnel, but must still drive the resume
// after its durable write and preserve the resent row through completion.
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
  const resume = deferred<ThreadReadResponse>();
  const resumeRequested = nextHandledRequest(fake, "thread/resume", () => resume.promise);
  const result = await resendRecoveryMutation(recovery.clientMutationId, ref, "send", "retry me", []);
  expect(result.status).toBe("sent");
  if (result.status !== "sent") throw new Error("expected a durable resent row");
  // The resend resolves at durability, not at the async resume's RPC boundary.
  await resumeRequested;
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(await storage.listOutbox(ref)).toMatchObject([
    { clientMutationId: result.record.clientMutationId, attempted: false },
  ]);
  const completion = threadsStore.getState().resumeSession(ref);
  fake.on("thread/read", () => readResponse(ref));
  const sent = nextHandledRequest(fake, "turn/start", (params) => ({
    receipt: mutationReceipt(params.clientMutationId),
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  resume.resolve(readResponse(ref));
  await completion;
  expect(await sent).toMatchObject({
    ref,
    clientMutationId: result.record.clientMutationId,
    input: [{ type: "text", text: "retry me" }],
  });
  await waitFor(async () => expect(await storage.listOutbox(ref)).toEqual([]));
  expect(await storage.listRecovery(ref)).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
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
  const hydrationEntered = deferred<void>();
  const hydration = deferred<void>();
  fake.on("thread/read", async (params) => {
    if (params.ref !== toRef) return fromShape();
    const response = toShape();
    if (holdToRead) {
      holdToRead = false;
      hydrationEntered.resolve();
      await hydration.promise;
    }
    return response;
  });
  const resume = deferred<ThreadReadResponse>();
  const resumeRequested = nextHandledRequest(fake, "thread/resume", () => resume.promise);
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
  await resumeRequested;
  const completion = threadsStore.getState().resumeSession(fromRef);
  const [first] = await storage.listOutbox(fromRef);
  if (!first) throw new Error("expected the first send to be durable");
  expect(starts).toEqual([]);
  // Release the resume but hold the resumed ref's hydration read, so the drive
  // is in flight when the second send lands.
  holdToRead = true;
  resume.resolve(toShape());
  await hydrationEntered.promise;
  // The first row moved, but the resume owner still fences its destination.
  expect(await storage.listOutbox(toRef)).toMatchObject([
    { clientMutationId: first.clientMutationId, attempted: false },
  ]);
  expect(starts).toEqual([]);
  await threadsStore.getState().send(fromRef, "two");
  const [second] = await storage.listOutbox(fromRef);
  if (!second) throw new Error("expected the late send to be durable");
  expect(second.clientMutationId).not.toBe(first.clientMutationId);
  expect(second.attempted).toBe(false);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(starts).toEqual([]);
  // Release the hydration: the final pass moves the late row to the new ref and
  // both drain there.
  hydration.resolve();
  await completion;
  await waitFor(async () => {
    expect(starts).toEqual([toRef, toRef]);
    expect(await storage.listOutbox(fromRef)).toEqual([]);
    expect(await storage.listOutbox(toRef)).toEqual([]);
  });
  expect(starts).toEqual([toRef, toRef]);
  expect(fake.calls.filter((call) => call.method === "turn/start").map((call) => call.params)).toMatchObject([
    { ref: toRef, clientMutationId: first.clientMutationId, input: [{ type: "text", text: "one" }] },
    { ref: toRef, clientMutationId: second.clientMutationId, input: [{ type: "text", text: "two" }] },
  ]);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
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

// Resync and ready can reconcile first, but only the explicit resume owner's
// completion releases its parked row. The competing tails must not duplicate it.
test("the parked Send-resumes row dispatches exactly once when the resync and ready tails release before the resume's own refresh", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const ref = "local:exactly-once-held-refresh";
  let resumed = false;
  let holdRefreshRead = false;
  const hydrationEntered = deferred<void>();
  const hydration = deferred<void>();
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
  fake.on("thread/read", async () => {
    const response = resumed ? idle() : fenced();
    if (holdRefreshRead) {
      holdRefreshRead = false;
      hydrationEntered.resolve();
      await hydration.promise;
    }
    return response;
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
  await hydrationEntered.promise;
  const completion = threadsStore.getState().resumeSession(ref);
  const [parked] = await storage.listOutbox(ref);
  if (!parked) throw new Error("expected the send to remain durable during hydration");
  // The parked row cannot dispatch while the resume's refresh is held.
  expect(sends).toBe(0);
  // Resync clears the recovery obligation, but not the explicit resume owner.
  const resyncRead = nextHandledRequest(fake, "thread/read", () => idle());
  fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
  await resyncRead;
  await waitFor(() => expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(false));
  // Observe the real dispatcher's storage tail, not an arbitrary delay, before
  // asserting that the ready trigger also leaves the parked row unattempted.
  const readyTail = deferred<void>();
  const listOptimistic = storage.listOptimistic.bind(storage);
  const observeTail = vi.spyOn(storage, "listOptimistic").mockImplementation(async (target) => {
    const rows = await listOptimistic(target);
    if (target === ref) readyTail.resolve();
    return rows;
  });
  try {
    notifyReadyForMutationDispatch([ref]);
    await readyTail.promise;
  } finally {
    observeTail.mockRestore();
  }
  expect(sends).toBe(0);
  expect(await storage.listOutbox(ref)).toMatchObject([
    { clientMutationId: parked.clientMutationId, attempted: false },
  ]);
  // Releasing the owner must deliver without another ready or resync trigger.
  hydration.resolve();
  await completion;
  await waitFor(async () => {
    expect(sends).toBe(1);
    expect(await storage.listOutbox(ref)).toEqual([]);
  });
  expect(sends).toBe(1);
  expect(fake.calls.filter((call) => call.method === "turn/start").map((call) => call.params)).toMatchObject([
    { ref, clientMutationId: parked.clientMutationId, input: [{ type: "text", text: "hello" }] },
  ]);
  expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
  expect(threadsStore.getState().resumeFailures.has(ref)).toBe(false);
});
