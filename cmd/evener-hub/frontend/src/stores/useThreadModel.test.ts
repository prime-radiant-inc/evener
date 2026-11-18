// The destination claim lease the recovery Send holds on its destination for
// the resend's duration. The acquisition runs the same
// ensureThread/releaseThread pair useThreadClaim holds a pane's subscription
// with - never a mock of it - and the hold is counted, so a destination
// another pane already tracks survives that pane's unmount until the resend
// releases it.

import "fake-indexeddb/auto";
import { type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { beforeEach, expect, test, vi } from "vitest";
import {
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "../panes/session/composer/queue/pendingTurnsStore";
import { withRecoveryDestinationClaim } from "../panes/session/composer/submitProtocol";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import {
  entryTranscriptRoute,
  productionEntryTranscriptForTests,
  setProductionEntryCacheForTests,
} from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { deleteMutationDatabase } from "./mutationOutboxTestUtils";
import { SessionEntryCache } from "./sessionEntryCache";
import { pumpIndexedDBUntil } from "./testing/indexedDBPump";
import { foregroundHolds } from "./threadOwners";
import {
  installHydrationRetrySchedulerForTests,
  resendRecoveryMutation,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  threadsStore,
} from "./threads";
import {
  connectMutationClient,
  mutationReceipt,
  nextHandledRequest,
  readResponse,
  resumeOnlySnapshot,
} from "./threadsSessionActionsTestUtils";
import { acquireUnreadThreadClaim } from "./useThreadModel";

beforeEach(async () => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  await deleteMutationDatabase();
});

async function flushUntil(done: () => boolean, maxTurns = 40): Promise<void> {
  for (let turn = 0; turn < maxTurns && !done(); turn += 1) await nextMacrotask();
  if (!done()) throw new Error("condition not reached");
}

test("a failed destination claim acquisition releases no claim another reader holds", async () => {
  const first = connectMutationClient();
  let rejectRead: ((error: Error) => void) | undefined;
  first.on(
    "thread/read",
    () =>
      new Promise<ThreadReadResponse>((_, reject) => {
        rejectRead = reject;
      }),
  );
  // Another foreground reader holds the destination while its hydration read
  // is still in flight - the claim a pane's useThreadClaim mount would hold.
  const otherReader = threadsStore.getState().ensureThread("ref_dest");
  await flushUntil(() => rejectRead !== undefined);
  // The acquisition runs with no wired client, so ensureThread refuses
  // before taking any claim of its own.
  resetConnectionStoreForTests();
  // The rejection itself is the behavior; its message wording is not.
  await expect(acquireUnreadThreadClaim("ref_dest")).rejects.toThrow();
  // A replacement connection answers the destination read, and the first
  // connection's stalled read dies with it.
  const second = connectMutationClient();
  second.on("thread/read", () => readResponse("ref_dest"));
  rejectRead?.(new Error("transport died"));
  await otherReader;
  // The other reader's claim survived the failed acquisition: it re-arms on
  // the replacement connection and publishes the destination model. A lease
  // that released on failure would have consumed that claim, retired its
  // hydration lifecycle, and left the destination unpublished forever.
  expect(threadsStore.getState().threads.has("ref_dest")).toBe(true);
  threadsStore.getState().releaseThread("ref_dest");
  await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_dest")).toBe(false));
}, 20_000);

// The counted hold, in isolation: a destination this tab already tracks is
// claimed for the resend's duration. Every release decrements one count, so
// the pane's own unmount leaves the model standing and the lease's release is
// what finally retires it - no leak, and no bare check-then-act.
test("a tracked destination is held by count until the lease releases", async () => {
  const fake = connectMutationClient();
  fake.on("thread/read", () => resumeOnlySnapshot("ref_dest"));
  await threadsStore.getState().ensureThread("ref_dest");
  expect(threadsStore.getState().threads.has("ref_dest")).toBe(true);
  const lease = await acquireUnreadThreadClaim("ref_dest");
  // The pane that already tracks the destination closes first; the resend's
  // hold keeps the model, so its dispatch still has the model it reads.
  // Retirement has finished by the time this resolves, so the model still
  // standing here is the acquisition's count, not a retirement still running.
  await threadsStore.getState().releaseThreadAndWait("ref_dest");
  expect(
    threadsStore.getState().threads.has("ref_dest"),
    "the acquisition must hold the destination past the tracking pane's own release",
  ).toBe(true);
  lease.release();
  // Releasing twice is a no-op: the lease owns exactly one count.
  lease.release();
  await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_dest")).toBe(false));
});

// A leaked count is worse than the defect this fixes: it would keep a
// destination's model and subscription alive for the life of the tab, long
// after the resend and the pane that opened it are gone. These assert the
// counts themselves, because the model's survival is decided asynchronously by
// retirement and an assertion on it can pass while a count is still stranded.
test("the lease owns exactly one count, and a ref-keyed release cannot spend it", async () => {
  const fake = connectMutationClient();
  fake.on("thread/read", () => resumeOnlySnapshot("ref_dest"));
  await threadsStore.getState().ensureThread("ref_dest");
  const paneHolds = foregroundHolds("ref_dest");
  const lease = await acquireUnreadThreadClaim("ref_dest");
  expect(foregroundHolds("ref_dest"), "the lease takes exactly one count").toBe(paneHolds + 1);
  // A ref-keyed release names the newest PANE hold. With the pane's hold still
  // standing, this drops the pane's count and leaves the lease's - the
  // direction that keeps a resend's destination alive.
  threadsStore.getState().releaseThread("ref_dest");
  expect(foregroundHolds("ref_dest"), "a ref-keyed release drops one pane count").toBe(paneHolds);
  // A second ref-keyed release has no pane hold of its own left to drop, so it
  // must not fall through to the lease's count.
  threadsStore.getState().releaseThread("ref_dest");
  expect(foregroundHolds("ref_dest"), "a ref-keyed release must never spend a routing lease's count").toBe(paneHolds);
  lease.release();
  expect(foregroundHolds("ref_dest"), "the lease's own release drops its count").toBe(paneHolds - 1);
  // Releasing twice is a no-op: exactly one count, never two.
  lease.release();
  expect(foregroundHolds("ref_dest"), "a lease releases once").toBe(paneHolds - 1);
  await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_dest")).toBe(false));
});

// A lease is a claim that the destination is routable. A hub that answers the
// read with a deletion fence has confirmed it is not, so the acquisition must
// reject - and reject without leaving its count behind. A pane keeps its
// deleted-session state; that is a display, not a claim.
test("a claim on a destination the hub reports deleted rejects and drops its hold", async () => {
  const fake = connectMutationClient();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    throw new WireError("target has been deleted: local:claim-deleted", -32001, {
      evenerErrorInfo: "actionUnavailable",
      mutationOutcome: "targetDeleted",
      retryDisposition: "none",
    });
  });
  const holdsBefore = foregroundHolds("local:claim-deleted");
  await expect(acquireUnreadThreadClaim("local:claim-deleted")).rejects.toThrow();
  // The hub's fence is what made the acquisition fail, and the read that
  // carried it ran exactly once: the claim did not retry an unrecoverable
  // destination. The rollback then released exactly the hold this acquisition
  // took, so no count is stranded. The fence itself is the pane's display
  // state and retires with the model, so it is not asserted here.
  expect(reads, "the claim read the destination once and did not retry").toBe(1);
  expect(foregroundHolds("local:claim-deleted"), "a rejected acquisition leaves no count behind").toBe(holdsBefore);
}, 20_000);

// Deletion can arrive on the RETRY, not on the claim's first read: the read
// fails in transport, its owner schedules the retry, and the retry is the
// attempt the hub answers with the fence. Deletion is terminal, so the owned
// lifecycle has to settle before the fence's own cache work revokes it - the
// retirement that revocation performs removes the pending entry, and a retry
// handler that finds no entry of its own never settles the hydration a waiting
// claim is parked on. The claim must then reject rather than hang on a
// lifecycle nothing will wake, and its rollback must drop exactly its count.
test("a retry that answers deletion rejects the claim instead of hanging", async () => {
  const ref = "local:retry-deleted";
  const fake = connectMutationClient();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    if (reads === 1) throw new Error("transport died");
    throw new WireError(`target has been deleted: ${ref}`, -32001, {
      evenerErrorInfo: "actionUnavailable",
      mutationOutcome: "targetDeleted",
      retryDisposition: "none",
    });
  });
  // The retry the failed first read arms, captured rather than awaited: its
  // backoff is wall-clock, and what this test needs is the attempt itself.
  let fireRetry: (() => void) | undefined;
  const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, run) => {
    fireRetry = run;
    return () => {};
  });
  const holdsBefore = foregroundHolds(ref);
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => fireRetry !== undefined);
  fireRetry?.();
  // Bounded, not `await claim`: a claim parked on an unsettled lifecycle is
  // exactly the defect, and a test timeout would report it as a slow test
  // rather than as a hang.
  let outcome: "pending" | "resolved" | "rejected" = "pending";
  void claim.then(
    () => {
      outcome = "resolved";
    },
    () => {
      outcome = "rejected";
    },
  );
  for (let turn = 0; turn < 60 && outcome === "pending"; turn += 1) await nextMacrotask();
  expect(outcome, "a deleted destination rejects the claim rather than parking it").toBe("rejected");
  expect(reads, "the retry ran once and was not re-armed at a deleted destination").toBe(2);
  expect(foregroundHolds(ref), "a rejected acquisition leaves no count behind").toBe(holdsBefore);
  // The fence is terminal: the rejected claim's rollback retired the ref and
  // nothing re-armed a third read at a destination the hub has confirmed gone.
  // (The `deletedRefs` marker itself is the pane's display state and retires
  // with the model, so the read count is the standing evidence here.)
  for (let turn = 0; turn < 20; turn += 1) await nextMacrotask();
  expect(reads, "no read follows the fence").toBe(2);
  restoreScheduler();
}, 20_000);

// The pane face of the same read, unchanged: a pane acquisition returns and
// shows the deleted-session state rather than rejecting.
test("a pane acquisition over a deleted destination still returns", async () => {
  const fake = connectMutationClient();
  fake.on("thread/read", () => {
    throw new WireError("target has been deleted: local:pane-deleted", -32001, {
      evenerErrorInfo: "actionUnavailable",
      mutationOutcome: "targetDeleted",
      retryDisposition: "none",
    });
  });
  await expect(threadsStore.getState().ensureThread("local:pane-deleted")).resolves.toBeUndefined();
  expect(threadsStore.getState().deletedRefs.has("local:pane-deleted")).toBe(true);
}, 20_000);

// The claim's answer is verified routing state, and the certification open is
// the pane's work. A claim that ran it would attach a reader and read the
// transcript cache before its caller could write, so an unopened destination's
// claim must leave no route reader of its own behind. The pane that opens the
// same destination afterwards still performs its own open.
test("a claim on an unopened destination verifies without opening a transcript", async () => {
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse("local:claim-unopened"));
  const lease = await acquireUnreadThreadClaim("local:claim-unopened");
  // No pane body owner exists, so the claim's read published routing state and
  // left the transcript open to the pane that eventually opens the ref.
  const claimed = productionEntryTranscriptForTests("local:claim-unopened");
  expect(claimed.reader, "a claim is not a pane open").toBeUndefined();
  expect(claimed.lastLocalOpenStatus, "a claim owes no cached open").toBeUndefined();
  lease.release();
}, 20_000);

// A cached shell advertises neither Send nor Queue. A resend that joined a read
// which failed after a connection replacement must not accept that shell as
// verification: it waits for the re-arm to verify on the replacement.
test("a failed read after a replacement does not verify a routing claim from the shell", async () => {
  const first = connectMutationClient();
  const ref = "local:shell-dest";
  first.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  // The resident model is a cached shell: its source read has not confirmed it.
  // A shell publishes neither Send nor Queue, so it is not routing state.
  const route = entryTranscriptRoute(ref);
  if (!route) throw new Error("expected an entry route");
  route.cachedUnverified = true;
  // The read is still in flight when the connection is replaced, so the
  // failure is judged against a client that is no longer the wired one: that
  // is the branch a shell could be accepted on.
  const stalled = deferred<ThreadReadResponse>();
  let firstReads = 0;
  first.on("thread/read", () => {
    firstReads += 1;
    return stalled.promise;
  });
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => firstReads > 0);
  // The connection is replaced while the claim's read is still in flight, then
  // that read rejects: the shell standing in the store is all the old path
  // would have.
  let replacementReads = 0;
  // The replacement's answer is held, so the lease must stay pending while it
  // is outstanding: resolving before it arrives is exactly what accepting the
  // shell looked like. The handler is installed BEFORE the client is connected,
  // because the ready transition starts the refresh read: a client whose default
  // handler answered first would verify the ref on a read this test never held.
  const replacement = deferred<ThreadReadResponse>();
  const second = new FakeClient("ready");
  second.on("thread/read", () => {
    replacementReads += 1;
    return replacement.promise;
  });
  connectionStore.getState().connect(second);
  stalled.reject(new Error("transport died"));
  let settledEarly = false;
  void claim.then(
    () => {
      settledEarly = true;
    },
    () => {
      settledEarly = true;
    },
  );
  // Drain generously while the replacement is unanswered: the lease must not
  // resolve on the shell, whatever else settles.
  for (let turn = 0; turn < 60; turn += 1) await nextMacrotask();
  expect(settledEarly, "the shell was not accepted as verification").toBe(false);
  replacement.resolve(readResponse(ref));
  const lease = await claim;
  // Exactly one replacement read: the claim joined the re-armed verification
  // instead of starting a duplicate that would supersede it.
  expect(replacementReads, "the claim joined the replacement read").toBe(1);
  lease.release();
}, 20_000);

// A retry that no pane opened behind must register as a routing read. If it
// registers as pane-backed, the first pane to open skips its cached lookup and
// paints loading over a usable cached transcript.
test("a pane opening over a retry still restores its cached transcript", async () => {
  const ref = "local:cached-pane";
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `cached-pane-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
  // The one question M4 asks: did the pane spend its own bounded local
  // lookup? A retry registered as pane-backed makes the pane skip it.
  const lookup = vi.spyOn(cache, "getSessionForRequestedRef");
  const fake = connectMutationClient();
  let failReads = true;
  fake.on("thread/read", () => {
    if (failReads) throw new Error("transport died");
    return readResponse(ref);
  });
  // The retry the claim arms is machinery's, and M4 is about the registration
  // that retry leaves behind. Capture the retry callback instead of waiting out
  // its backoff, then fire it while the pane is opening.
  let fireRetry: (() => void) | undefined;
  const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, run) => {
    fireRetry = run;
    return () => {};
  });
  void acquireUnreadThreadClaim(ref).catch(() => {});
  await flushUntil(() => fake.calls.some((call) => call.method === "thread/read"));
  await flushUntil(() => fireRetry !== undefined);
  // The retry's own read stays in flight and publishes nothing, so the pane
  // meets that pending registration - the window M4 names.
  const retryRead = deferred<ThreadReadResponse>();
  failReads = false;
  fake.on("thread/read", () => retryRead.promise);
  fireRetry?.();
  await flushUntil(() => fake.calls.filter((call) => call.method === "thread/read").length > 1);
  // The first pane opens over the pending retry, with no model published.
  const pane = threadsStore.getState().ensureThread(ref);
  for (let turn = 0; turn < 20; turn += 1) await nextMacrotask();
  expect(lookup, "the pane still owes its own cached lookup").toHaveBeenCalled();
  retryRead.resolve(readResponse(ref));
  await pane;
  restoreScheduler();
  cache.close();
}, 20_000);

// The issue's exact scenario end to end: the destination is open in another
// pane, and that pane unmounts while the resend's durable write is still in
// flight. Nothing else pins the destination, so without the resend's own hold
// its model retires and the row the resend enqueued parks until someone opens
// the destination again. With the hold, the resend drives the resume the row
// needs and it dispatches.
test("a resend to a tracked destination still dispatches when that pane unmounts mid-resend", async () => {
  const storage = new HeldResendStorage();
  setMutationStorageForTests(storage);
  const ref = "local:resend-held";
  const fake = connectMutationClient();
  fake.on("thread/read", (params) => {
    if (params.ref === ref) return resumeOnlySnapshot(ref);
    if (params.ref === undefined) throw new Error("thread/read test request requires ref");
    return readResponse(params.ref);
  });
  // The dispatch the fix is proven by: the resent row's own id on the wire,
  // recorded from the handler rather than read back off the call log.
  const startedIds: string[] = [];
  fake.on("turn/start", (params) => {
    startedIds.push(params.clientMutationId);
    return {
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    };
  });
  // Another pane has the fenced destination open.
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().restartBlockingObligations.has(ref)).toBe(true);
  const recovery = await stageRecoveryRow(storage, ref);
  await refreshPendingTurnsProjection(ref);

  const resume = deferred<ThreadReadResponse>();
  let resumeAnswered = false;
  fake.on("thread/resume", () => resume.promise);
  const resumeRequested = nextHandledRequest(fake, "thread/resume", () => resume.promise);

  const resend = withRecoveryDestinationClaim(ref, () =>
    resendRecoveryMutation(recovery.clientMutationId, ref, "send", "retry me", []),
  );
  await storage.resendReached;
  // The other pane unmounts while the resend is still awaiting its write, and
  // its retirement finishes before the write can land: a resend that committed
  // first would pin the model and could dispatch without any claim of its own.
  await threadsStore.getState().releaseThreadAndWait(ref);
  storage.openResendGate();
  const result = await resend;
  if (result.status !== "sent") throw new Error("expected the resend to commit a durable row");
  const resentId = result.record.clientMutationId;

  // Nobody reopens the destination. The resent row is the Send-resumes face's
  // recovery action, so the resend drives the resume and the row dispatches.
  const started = await pumpIndexedDBUntil({
    turns: 80,
    until: () => fake.calls.some((call) => call.method === "turn/start"),
    onTurn: () => {
      if (resumeAnswered || !fake.calls.some((call) => call.method === "thread/resume")) return;
      resumeAnswered = true;
      // The resume landed: the destination is no longer fenced, so its
      // post-resume refresh publishes an unfenced model and the parked row's
      // gate opens.
      fake.on("thread/read", () => readResponse(ref));
      resume.resolve(readResponse(ref));
    },
  });
  expect(started, "the resent row must dispatch without anyone reopening the destination").toBe(true);
  await resumeRequested;
  expect(startedIds).toEqual([resentId]);
}, 20_000);

class HeldResendStorage extends MutationOutboxIndexedDB {
  readonly resendReached: Promise<void>;
  private markResendReached: (() => void) | undefined;
  private releaseResend: (() => void) | undefined;
  private readonly resendGate: Promise<void>;

  constructor() {
    super({ databaseName: `held-resend-${crypto.randomUUID()}` });
    this.resendReached = new Promise((resolve) => {
      this.markResendReached = resolve;
    });
    this.resendGate = new Promise((resolve) => {
      this.releaseResend = resolve;
    });
  }

  openResendGate(): void {
    this.releaseResend?.();
  }

  override async resendRecoveryResult(
    ...args: Parameters<MutationOutboxIndexedDB["resendRecoveryResult"]>
  ): ReturnType<MutationOutboxIndexedDB["resendRecoveryResult"]> {
    this.markResendReached?.();
    await this.resendGate;
    return super.resendRecoveryResult(...args);
  }
}

async function stageRecoveryRow(storage: MutationOutboxIndexedDB, ref: string) {
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: `thr_${ref}`,
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text: "retry me" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "retry me" }] },
  });
  const recovery = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovery) throw new Error("expected the recovery row to be durable");
  return recovery;
}
