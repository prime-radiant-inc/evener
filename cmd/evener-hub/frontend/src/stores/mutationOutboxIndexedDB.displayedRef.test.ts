// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import type { MutationIntent, MutationOutboxRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// The dual-ref Stop fence's durable half (#5182): the displayed-ref ownership
// and both enqueue-time epochs a retargeted resend stamps on its row, checked
// at every seam that can make that row dispatchable again - Stop's cancel
// scan, the refusal rollback, and the reconcile reopen. Real adapter against
// fake IndexedDB, rows read back the way a second tab would see them.

const DISPLAYED = "local:displayed";
const DESTINATION = "local:destination";

function intent(text: string, targetRef: string): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "turn/start",
    payload: { ref: targetRef, input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { text },
  };
}

function interruptIntent(targetRef: string): MutationIntent {
  return {
    targetRef,
    method: "turn/interrupt",
    payload: { ref: targetRef },
    attachments: [],
    optimisticDisplay: { method: "turn/interrupt" },
  };
}

function idSequence(prefix = "mutation") {
  let next = 0;
  return () => `${prefix}-${++next}`;
}

describe("retargeted resend rows' displayed-ref fence", () => {
  let indexedDB: IDBFactory;
  let databaseName: string;

  beforeEach(() => {
    indexedDB = new IDBFactory();
    databaseName = `mutation-outbox-displayed-${crypto.randomUUID()}`;
    setMutationClientIdentityForTests(undefined);
  });

  function store() {
    return new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence() });
  }

  // Seeds the reviewer's shape: a rejected recovery row a peer retargeted to
  // DESTINATION, resent while the sending pane still displayed DISPLAYED.
  // Returns the fresh outbox row the resend enqueued, born dispatchable.
  async function seedRetargetedResend(storage: MutationOutboxIndexedDB): Promise<MutationOutboxRecord> {
    const original = await storage.enqueueIntent(intent("hello", DISPLAYED));
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    await storage.retargetOutbox(DISPLAYED, DESTINATION, { threadId: "thread-b" });
    const sent = await storage.resendRecoveryResult(
      original.clientMutationId,
      intent("hello", DESTINATION),
      undefined,
      undefined,
      undefined,
      { stopEpoch: 0, displayedRef: DISPLAYED },
    );
    if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
    return sent.record;
  }

  test("the resend stamps its displayed ref and both enqueue-time epochs", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    expect(row).toMatchObject({
      targetRef: DESTINATION,
      displayedRef: DISPLAYED,
      stopEpochAtEnqueue: 0,
      displayedStopEpochAtEnqueue: 0,
      state: "submitting",
    });
    storage.close();
  });

  test("a Stop on the displayed ref cancels the unattempted resend row", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "canceled" });
    storage.close();
  });

  test("a refusal's rollback across a displayed-ref Stop holds the row canceled", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    // The Stop's cancel scan must skip the attempted row.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: true,
    });
    // The refusal proves the attempt never landed; the Stop that landed
    // while the attempt was in flight is the row's truth, so the rollback
    // settles it canceled right there - #4342's hold, dual-ref: a Stop on
    // the still-displayed session holds the row the same way one on the
    // destination does, never dispatchable ahead of the Stop's interrupt,
    // released only by an explicit Retry. The pre-transport gate remains
    // defense-in-depth for a pass the rollback never sees.
    expect(await storage.unmarkAttempted(row.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "canceled",
      attempted: false,
    });
    expect(await storage.markAttempted(row.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "canceled",
      attempted: false,
    });
    storage.close();
  });

  test("a refusal after no Stop still reopens the row for its paced retry", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    expect(await storage.unmarkAttempted(row.clientMutationId)).toBe(true);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: false,
    });
    // No Stop crossed the row, so the gate lets the paced retry through.
    expect(await storage.markAttempted(row.clientMutationId)).toBe(true);
    storage.close();
  });

  test("a reconcile keeps a Stop-crossed attempted row delivery-uncertain, never canceled", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    // The attempt may already be delivered with its response lost, and
    // absence from a bounded read proves nothing: the crossed Stop fences
    // redispatch only, so the fence decides nothing for this row.
    const restored = await storage.restoreProvenAbsent(DESTINATION, new Set());
    expect(restored).not.toContain(row.clientMutationId);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
    });
    // The held state parks the ref's FIFO - nothing re-dispatches ahead of
    // the Stop - and a second reconcile pass holds the same line.
    expect(await storage.nextDispatchable(DESTINATION)).toBeUndefined();
    await storage.restoreProvenAbsent(DESTINATION, new Set());
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    // The row's truth stays open: a receipt for the attempt that was
    // accepted at the hub with its response lost still establishes delivery.
    expect(await storage.settleReceipt(row.clientMutationId, "reflected")).toBe(true);
    expect(await storage.getOutbox(row.clientMutationId)).toBeUndefined();
    storage.close();
  });

  test("a reconcile re-queues an innocent parked row as a fresh row and settles the old one terminal", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    // Reconciliation NEVER re-enables the old row: an innocent parked row
    // mints a FRESH row in the background - barrier-free like §4's recovery
    // resends, fenced at birth by construction - and the old row settles
    // terminal "superseded", keeping its id and attempt evidence for the
    // receipt a real in-flight attempt can still bring.
    const restored = await storage.restoreProvenAbsent(DESTINATION, new Set());
    expect(restored).toHaveLength(1);
    const freshId = restored[0]!;
    expect(freshId).not.toBe(row.clientMutationId);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "superseded",
      attempted: true,
    });
    // The fresh row is born dispatchable at the ref's tail, both epochs
    // stamped at its own birth, and inherits the predecessor's wire id: the
    // daemon journals this composition under that id, and a re-submission
    // under it replays rather than double-applies.
    const fresh = await storage.getOutbox(freshId);
    expect(fresh).toMatchObject({
      state: "submitting",
      targetRef: DESTINATION,
      displayedRef: DISPLAYED,
      attempted: false,
    });
    expect(fresh?.payload.clientMutationId).toBe(row.clientMutationId);
    expect(await storage.nextDispatchable(DESTINATION)).toMatchObject({ clientMutationId: freshId });
    storage.close();
  });

  test("a Retry of a canceled row mints a fresh row whose own birth baselines let a later refusal pace", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    // A Stop cancels the unattempted resend; the user explicitly retries it.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "canceled" });
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    const fresh = await storage.mintRetryFromRow(row.clientMutationId, capture);
    expect(fresh).toMatchObject({ state: "submitting", targetRef: DESTINATION, displayedRef: DISPLAYED });
    expect(fresh?.clientMutationId).not.toBe(row.clientMutationId);
    // A canceled predecessor provably never left any tab, so the fresh row
    // carries its OWN wire id, and the old row is removed - §6's removal on
    // explicit Retry - so its chip leaves the canceled strip and a second
    // press cannot mint the same send twice.
    expect(fresh?.payload.clientMutationId).toBe(fresh?.clientMutationId);
    expect(await storage.getOutbox(row.clientMutationId)).toBeUndefined();
    // The retried row attempts, and the hub answers with an automatic
    // backpressure refusal: the refusal reopens the FRESH row for its paced
    // retry - its baselines are its own birth's, so no crossed Stop
    // re-cancels it.
    await storage.markAttempted(fresh!.clientMutationId);
    expect(await storage.unmarkAttempted(fresh!.clientMutationId)).toBe(true);
    expect(await storage.getOutbox(fresh!.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: false,
    });
    storage.close();
  });

  test("a Retry of an ordinary canceled row mints a fresh row with a single birth baseline", async () => {
    const storage = store();
    // An ordinary (non-retargeted) row crosses one ref: the minted row
    // carries no displayed half, and its one baseline is its own birth's.
    const original = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({ state: "canceled" });
    const capture = await storage.getOutboxWithStopEpoch(original.clientMutationId);
    const minted = await storage.mintRetryFromRow(original.clientMutationId, capture);
    expect(minted).toMatchObject({ state: "submitting", targetRef: DESTINATION });
    expect(minted?.displayedRef).toBeUndefined();
    expect(minted?.displayedStopEpochAtEnqueue).toBeUndefined();
    expect(minted?.stopEpochAtEnqueue).toBe(capture.stopEpoch);
    expect(await storage.markAttempted(minted!.clientMutationId)).toBe(true);
    expect(await storage.unmarkAttempted(minted!.clientMutationId)).toBe(true);
    storage.close();
  });

  test("a Retry whose window a newer displayed Stop crosses mints the fresh row born-canceled", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    // A Stop on the displayed ref cancels the unattempted resend.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "canceled" });
    // The Retry click's capture PAIR: the destination epoch, and the
    // displayed ref's post-Stop epoch.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    expect(capture).toMatchObject({ stopEpoch: 0, displayedStopEpoch: 1 });
    // A second Stop lands on the displayed ref inside the capture-to-mint
    // window: its cancel scan skips the already-canceled row, and the
    // destination epoch never moved - the displayed epoch's bump is the one
    // trace the mint's birth compare must catch. Nothing absorbs the newer
    // Stop: the fresh row commits born-canceled for it, and the old row is
    // removed (§6's removal on explicit Retry).
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    const crossed = await storage.mintRetryFromRow(row.clientMutationId, capture);
    expect(crossed).toMatchObject({ state: "canceled", targetRef: DESTINATION, displayedRef: DISPLAYED });
    expect(await storage.getOutbox(row.clientMutationId)).toBeUndefined();
    // The born-canceled row never dispatches.
    expect(await storage.nextDispatchable(DESTINATION)).toBeUndefined();
    // Liveness control: that canceled chip's own Retry is the deliberate
    // post-Stop send - a fresh capture mints born-submitting under the
    // epochs as they stand.
    const after = await storage.getOutboxWithStopEpoch(crossed!.clientMutationId);
    const minted = await storage.mintRetryFromRow(crossed!.clientMutationId, after);
    expect(minted).toMatchObject({
      state: "submitting",
      stopEpochAtEnqueue: 0,
      displayedStopEpochAtEnqueue: 2,
    });
    storage.close();
  });

  test("an explicit Retry mints a fresh row from a Stop-crossed blockedUnknown row, inheriting its wire id", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    // The reviewer's premise: authoritative reads never mention the mutation
    // ID, so the reconcile fence holds the crossed row and no receipt can
    // ever settle it. Automatic reconciliation decides nothing for it.
    expect(await storage.restoreProvenAbsent(DESTINATION, new Set())).not.toContain(row.clientMutationId);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
    });
    // The user's click: its capture reads the post-Stop epochs and the mint
    // deliberately outranks the crossed Stop - as a FRESH row. The old row
    // settles terminal "superseded", keeping its id and attempt evidence for
    // the receipt the real in-flight attempt can still bring; the fresh row
    // inherits the predecessor's wire id so the daemon's journal replays the
    // first attempt's receipt instead of double-applying the send.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    const fresh = await storage.mintRetryFromRow(row.clientMutationId, capture);
    expect(fresh).toMatchObject({ state: "submitting", targetRef: DESTINATION, displayedRef: DISPLAYED });
    expect(fresh?.clientMutationId).not.toBe(row.clientMutationId);
    expect(fresh?.payload.clientMutationId).toBe(row.clientMutationId);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "superseded",
      attempted: true,
    });
    // The fresh row proceeds: it is the ref's dispatchable head again.
    expect(await storage.nextDispatchable(DESTINATION)).toMatchObject({
      clientMutationId: fresh?.clientMutationId,
    });
    storage.close();
  });

  test("a Retry of a blockedUnknown row whose window a newer displayed Stop crosses mints born-canceled", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    // The click's capture, then a second Stop inside the capture-to-mint
    // window: the mint never absorbs the newer Stop - the fresh row commits
    // born-canceled for it, and the old row still settles terminal with its
    // evidence intact for the receipt a real attempt can still bring.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    const minted = await storage.mintRetryFromRow(row.clientMutationId, capture);
    expect(minted).toMatchObject({ state: "canceled", targetRef: DESTINATION, displayedRef: DISPLAYED });
    expect(minted?.payload.clientMutationId).toBe(row.clientMutationId);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "superseded",
      attempted: true,
    });
    expect(await storage.nextDispatchable(DESTINATION)).toBeUndefined();
    // Liveness control: the born-canceled chip's own Retry is the deliberate
    // post-Stop send - a fresh capture mints born-submitting.
    const after = await storage.getOutboxWithStopEpoch(minted!.clientMutationId);
    const remint = await storage.mintRetryFromRow(minted!.clientMutationId, after);
    expect(remint).toMatchObject({ state: "submitting" });
    storage.close();
  });

  test("a minted row a newer displayed Stop crosses is canceled by that Stop's own scan", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    // The explicit Retry mints the fresh row past the crossed Stop.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    const minted = await storage.mintRetryFromRow(row.clientMutationId, capture);
    expect(minted).toMatchObject({ state: "submitting" });
    // A NEWER Stop lands on the displayed ref while the minted row waits for
    // its dispatch: the fresh row is submitting and unattempted, so the
    // Stop's cancel scan matches it on the displayed ref and cancels it
    // directly - fencing at birth makes every later Stop's scan the fence.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(minted!.clientMutationId)).toMatchObject({ state: "canceled" });
    expect(await storage.markAttempted(minted!.clientMutationId)).toBe(false);
    expect(await storage.nextDispatchable(DESTINATION)).toBeUndefined();
    storage.close();
  });

  test("markAttempted fences a residual-evidence ordinary row the refusal rollback left dispatchable", async () => {
    const storage = store();
    // An ordinary row (no displayed ref): its only fence is the destination
    // epoch, and the reviewer's residual-evidence hole is the refusal
    // rollback's unresolvedAttempts branch, which skips the fence today.
    const original = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.markAttempted(original.clientMutationId);
    // A second tab's concurrent attempt: the state check admits it, and the
    // counter now holds two unresolved attempts.
    await storage.markAttempted(original.clientMutationId);
    // The second tab's attempt is refused in band: one unresolved attempt
    // remains, so the rollback keeps the evidence and the row stays
    // submitting+attempted with no fence consulted.
    expect(await storage.unmarkAttempted(original.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: true,
    });
    // A Stop lands on the ref while the first attempt is still unresolved.
    const stop = await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    // The pre-transport gate must refuse the redispatch and park the row
    // delivery-uncertain: the surviving attempt may still be in flight.
    expect(await storage.markAttempted(original.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    // The park is fence-stamped: the Stop is the reason this row is not
    // delivering, so the queue still passes the Stop's own interrupt.
    expect(await storage.nextDispatchable(DESTINATION)).toMatchObject({
      clientMutationId: stop.clientMutationId,
    });
    storage.close();
  });

  test("markAttempted still clears an innocent ordinary row for dispatch", async () => {
    const storage = store();
    const original = await storage.enqueueIntent(intent("hello", DESTINATION));
    expect(await storage.markAttempted(original.clientMutationId)).toBe(true);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: true,
    });
    storage.close();
  });

  test("markAttempted clears a row an explicit Retry minted past its captured Stop", async () => {
    const storage = store();
    const original = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({ state: "canceled" });
    // The Retry pressed after the Stop is §9 item 7's deliberate post-Stop
    // send: its capture reads the Stop's own epoch and the mint commits the
    // fresh row born-submitting - its birth baseline IS that epoch.
    const capture = await storage.getOutboxWithStopEpoch(original.clientMutationId);
    const minted = await storage.mintRetryFromRow(original.clientMutationId, capture);
    expect(minted).toMatchObject({ state: "submitting" });
    // The pre-transport gate must let the deliberate send through: the
    // fresh row's birth baseline is current.
    expect(await storage.markAttempted(minted!.clientMutationId)).toBe(true);
    expect(await storage.getOutbox(minted!.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: true,
    });
    storage.close();
  });

  test("a retargeted resend crossed at enqueue commits born-canceled, visible under the displayed ref", async () => {
    const storage = store();
    // The reviewer's shape up to the resend: a rejected recovery row a peer
    // retargeted to DESTINATION, its pane still displaying DISPLAYED.
    const original = await storage.enqueueIntent(intent("hello", DISPLAYED));
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    await storage.retargetOutbox(DISPLAYED, DESTINATION, { threadId: "thread-b" });
    // A Stop lands on the still-displayed ref after the press's barrier
    // captured the pre-Stop epoch: the resend's fence trips at its enqueue.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    const result = await storage.resendRecoveryResult(
      original.clientMutationId,
      intent("hello", DESTINATION),
      undefined,
      undefined,
      undefined,
      { stopEpoch: 0, displayedRef: DISPLAYED },
    );
    // The retargeted-fenced carve-out is deleted: a crossed resend still
    // mints its row - born-canceled, keyed to the destination, carrying the
    // displayed ref - so the canceled strip's displayedRef match keeps the
    // composition visible in the session the user is looking at, and the
    // press answers sent with the recovery row consumed.
    expect(result.status).toBe("sent");
    if (result.status !== "sent") throw new Error("unreachable");
    expect(result.record).toMatchObject({
      state: "canceled",
      targetRef: DESTINATION,
      displayedRef: DISPLAYED,
    });
    expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
    storage.close();
  });

  test("an ordinary recovery resend crossed at enqueue keeps its born-canceled row in the sending session", async () => {
    const storage = store();
    // The ordinary shape: the pane's displayed ref IS the row's destination,
    // and the press names it - the captureRecoverySendIntent caller always
    // passes the displayed barrier, retargeted or not.
    const original = await storage.enqueueIntent(intent("hello", DISPLAYED));
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    // A Stop lands before the resend's barrier captured the pre-Stop epoch.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    const result = await storage.resendRecoveryResult(
      original.clientMutationId,
      intent("hello", DISPLAYED),
      undefined,
      undefined,
      undefined,
      { stopEpoch: 0, displayedRef: DISPLAYED },
    );
    // The crossed ordinary send commits born-canceled in the SAME session:
    // the row surfaces in that session's queue strip with its Retry
    // affordance (§6's display), the recovery row is consumed, and the press
    // answers sent - the non-retargeted case's consistency the fenced outcome
    // must not swallow.
    expect(result.status).toBe("sent");
    if (result.status !== "sent") throw new Error("unreachable");
    expect(result.record).toMatchObject({
      state: "canceled",
      targetRef: DISPLAYED,
      displayedRef: DISPLAYED,
    });
    expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
    storage.close();
  });

  test("a row the Stop fence parks at the FIFO head does not block the Stop's own interrupt", async () => {
    const storage = store();
    // A send whose attempt's request was lost: the row keeps its evidence at
    // the head of the destination's FIFO while the user presses Stop.
    const send = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.markAttempted(send.clientMutationId);
    const stop = await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    // The cancel scan skips the attempted row, so the dispatcher's next pass
    // takes it to the pre-transport gate - and the crossed Stop parks it.
    expect(await storage.markAttempted(send.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(send.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      stopFenced: true,
    });
    // The fence stops the row's DELIVERY, not the queue: the Stop's own
    // interrupt must still reach the daemon past the row it parked.
    expect(await storage.nextDispatchable(DESTINATION)).toMatchObject({
      clientMutationId: stop.clientMutationId,
    });
    storage.close();
  });

  test("a row parked for genuine delivery uncertainty still blocks the queue behind it", async () => {
    const storage = store();
    // The control for the fence stamp: an attempt whose OUTCOME nobody can
    // vouch for (persistenceUnavailable) parks the row for a reason no Stop
    // caused - the fence never ran, so the queue stays closed behind it
    // until its receipt or refusal settles.
    const send = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.markAttempted(send.clientMutationId);
    await storage.markUnknown(send.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    const parked = await storage.getOutbox(send.clientMutationId);
    expect(parked).toMatchObject({ state: "blockedUnknown" });
    expect(parked?.stopFenced).toBeUndefined();
    expect(await storage.nextDispatchable(DESTINATION)).toBeUndefined();
    storage.close();
  });

  test("a reconcile holds an ordinary Stop-crossed attempted row for the explicit Retry that resends it", async () => {
    const storage = store();
    // The reviewer's shape, ordinary row: no displayed ref, so the row's only
    // fence is its destination's epoch - and the reconcile's fence requiring
    // a displayed ref let every crossed ordinary row reopen.
    const original = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.markAttempted(original.clientMutationId);
    await storage.markUnknown(original.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    // The crossed row must NOT reopen: the Stop the user pressed fences its
    // redispatch, and no automatic path may outrank it.
    expect(await storage.restoreProvenAbsent(DESTINATION, new Set())).not.toContain(original.clientMutationId);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
      stopFenced: true,
    });
    // The user's click is the one authorization that outranks the crossed
    // Stop: the mint settles the held row terminal and queues the send as a
    // FRESH row - born under the epochs as they stand, inheriting the wire id
    // so the daemon's journal replays rather than double-applies - and the
    // gate lets it through.
    const capture = await storage.getOutboxWithStopEpoch(original.clientMutationId);
    const minted = await storage.mintRetryFromRow(original.clientMutationId, capture);
    expect(minted).toMatchObject({ state: "submitting" });
    expect(minted?.clientMutationId).not.toBe(original.clientMutationId);
    expect(minted?.payload.clientMutationId).toBe(original.clientMutationId);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({ state: "superseded" });
    expect(await storage.markAttempted(minted!.clientMutationId)).toBe(true);
    storage.close();
  });

  test("a fence-refused gate pass mints no attempt evidence", async () => {
    const storage = store();
    // One real attempt in flight; the user presses Stop; the scan skips the
    // attempted row and bumps the epoch.
    const original = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.markAttempted(original.clientMutationId);
    await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    // A second tab hits the pre-transport gate for the same row: the fence
    // refuses its pass, and a refused pass is not an attempt - the count
    // must read the one real unresolved attempt, not two.
    expect(await storage.markAttempted(original.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
      unresolvedAttempts: 1,
    });
    // The real attempt's in-band refusal proves the truth: the rollback
    // settles the row canceled for the crossed Stop (#4342's hold) - the
    // count reads one-for-one and no phantom attempt keeps a provably unsent
    // row delivery-uncertain forever.
    expect(await storage.unmarkAttempted(original.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({
      state: "canceled",
      attempted: false,
    });
    storage.close();
  });

  test("a mint refuses a row a peer retargeted inside the capture-to-mint window", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    // The click's capture: the row on DESTINATION under the seed's identity.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    // A peer retargets the row to another destination inside the window: the
    // world the capture vouched for is gone, and the mint refuses as ONE
    // identity check - the row's durable destination and instance must still
    // match the capture (the park record's survivor, dead by construction:
    // no stored past travels with the mint, so no release transaction can
    // compare a captured epoch against a retargeted row).
    await storage.retargetOutbox(DESTINATION, "local:elsewhere", { threadId: "thread-c" });
    expect(await storage.mintRetryFromRow(row.clientMutationId, capture)).toBeUndefined();
    // The row itself is untouched: still parked, still holding its evidence.
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
    });
    // A fresh capture mints under the row's new world.
    const fresh = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    const minted = await storage.mintRetryFromRow(row.clientMutationId, fresh);
    expect(minted).toMatchObject({ state: "submitting", targetRef: "local:elsewhere" });
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "superseded" });
    storage.close();
  });

  test("a reconcile-held ordinary row stays settleable by the receipt its lost response never carried", async () => {
    const storage = store();
    // The fence's hold fences REDISPATCH only - it must not foreclose the
    // row's OUTCOME (§5): the attempt may have been accepted at the hub with
    // its response lost, and the reconcile's bounded read omitted it.
    const original = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.markAttempted(original.clientMutationId);
    await storage.markUnknown(original.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    expect(await storage.restoreProvenAbsent(DESTINATION, new Set())).not.toContain(original.clientMutationId);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    // The receipt the hub's journal replays still establishes delivery.
    expect(await storage.settleReceipt(original.clientMutationId, "reflected")).toBe(true);
    expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
    storage.close();
  });
});
