import { ALL_CAPABILITIES as CAPABILITIES, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import { hydrateThread, RequestTimeoutError, type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { MutationDispatcher } from "@evener/appwire-client/state/mutation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
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
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";
import {
  hasBlockedUnknown,
  hasQueuedNonSend,
  installHydrationRetrySchedulerForTests,
  putThreadModel,
  resetThreadsStoreForTests,
  resumeOnlyDispatchable,
  resumeOnlyModel,
  resumeStopBaseline,
  resumeStopFence,
  retryBlockedMutation,
  sendResumesModel,
  setMutationStorageForTests,
  threadsStore,
} from "./threads";
import {
  clearResponse,
  connectFakeClient,
  connectMutationClient,
  deferred,
  flushIndexedDBUntil,
  flushMicrotasks,
  flushUntil,
  mutationReceipt,
  nextHandledRequest,
  readResponse,
  resumeOnlySnapshot,
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

function runScheduledHydrationRetry(index = 0): void {
  const scheduled = scheduledHydrationRetries[index];
  expect(scheduled, `no hydration retry scheduled at index ${index}`).toBeDefined();
  expect(scheduled?.cancelled).toBe(false);
  scheduled?.retry();
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
});

// Part 2's predicate: a queued, not-yet-attempted NON-turn/start row parks at
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
