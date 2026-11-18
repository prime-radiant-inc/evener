// The destination claim lease the recovery Send holds on its destination for
// the resend's duration. The acquisition runs the same
// ensureThread/releaseThread pair useThreadClaim holds a pane's subscription
// with - never a mock of it - and the hold is counted, so a destination
// another pane already tracks survives that pane's unmount until the resend
// releases it.

import "fake-indexeddb/auto";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { beforeEach, expect, test, vi } from "vitest";
import {
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "../panes/session/composer/queue/pendingTurnsStore";
import { withRecoveryDestinationClaim } from "../panes/session/composer/submitProtocol";
import { resetConnectionStoreForTests } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { deleteMutationDatabase } from "./mutationOutboxTestUtils";
import { pumpIndexedDBUntil } from "./testing/indexedDBPump";
import { foregroundHolds } from "./threadOwners";
import { resendRecoveryMutation, resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "./threads";
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
