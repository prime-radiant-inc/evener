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

  test("a refusal rollback holds the attempted row canceled after a Stop on the displayed ref", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    // The Stop's cancel scan must skip the attempted row.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: true,
    });
    // The refusal proves the attempt never landed; the rollback must not make
    // the row dispatchable across the Stop that landed while it was in flight.
    expect(await storage.unmarkAttempted(row.clientMutationId)).toBe(false);
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

  test("a reconcile reopen of an innocent parked row still reopens it", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    const restored = await storage.restoreProvenAbsent(DESTINATION, new Set());
    expect(restored).toContain(row.clientMutationId);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "submitting" });
    storage.close();
  });

  test("an explicit Retry refreshes the displayed epoch so a refused retry paces instead of re-canceling", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    // A Stop cancels the unattempted resend; the user explicitly retries it.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "canceled" });
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    expect(await storage.releaseCanceled(row.clientMutationId, { stopEpoch: capture.stopEpoch })).toBe(true);
    const retried = await storage.getOutbox(row.clientMutationId);
    // The reauthorization refreshes BOTH fence baselines: the destination's,
    // and the displayed ref's - whose Stop this explicit Retry outranked. A
    // stale displayed capture would make the row's next refusal rollback
    // re-cancel it against that very Stop.
    expect(retried).toMatchObject({
      state: "submitting",
      stopEpochAtEnqueue: capture.stopEpoch,
      displayedStopEpochAtEnqueue: 1,
    });
    // The retried row attempts, and the hub answers with an automatic
    // backpressure refusal: the refusal must reopen the row for its paced
    // retry, not re-cancel it.
    await storage.markAttempted(row.clientMutationId);
    expect(await storage.unmarkAttempted(row.clientMutationId)).toBe(true);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: false,
    });
    storage.close();
  });

  test("an explicit Retry of an ordinary recovery send keeps both baselines consistent", async () => {
    const storage = store();
    // An ordinary (non-retargeted) recovery send: the displayed ref IS the
    // destination, so the two baselines must move together.
    const original = await storage.enqueueIntent(intent("hello", DISPLAYED));
    await storage.transferToRecovery(original.clientMutationId, "rejected");
    const sent = await storage.resendRecoveryResult(
      original.clientMutationId,
      intent("hello", DISPLAYED),
      undefined,
      undefined,
      undefined,
      { stopEpoch: 0, displayedRef: DISPLAYED },
    );
    if (sent.status !== "sent") throw new Error(`resend did not send: ${sent.status}`);
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    const capture = await storage.getOutboxWithStopEpoch(sent.record.clientMutationId);
    expect(await storage.releaseCanceled(sent.record.clientMutationId, { stopEpoch: capture.stopEpoch })).toBe(true);
    const retried = await storage.getOutbox(sent.record.clientMutationId);
    expect(retried?.state).toBe("submitting");
    expect(retried?.displayedStopEpochAtEnqueue).toBe(retried?.stopEpochAtEnqueue);
    await storage.markAttempted(sent.record.clientMutationId);
    expect(await storage.unmarkAttempted(sent.record.clientMutationId)).toBe(true);
    storage.close();
  });

  test("a release whose displayed capture went stale inside the click's window refuses instead of absorbing the newer Stop", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    // A Stop on the displayed ref cancels the unattempted resend.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "canceled" });
    // The Retry click's capture PAIR: the destination epoch, and the
    // displayed ref's post-Stop epoch.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    expect(capture).toMatchObject({ stopEpoch: 0, displayedStopEpoch: 1 });
    // A second Stop lands on the displayed ref inside the capture-to-release
    // window: its cancel scan skips the already-canceled row, and the
    // destination epoch never moved - the displayed epoch's bump is the one
    // trace the release must catch.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(
      await storage.releaseCanceled(row.clientMutationId, {
        stopEpoch: capture.stopEpoch,
        displayedStopEpoch: capture.displayedStopEpoch,
      }),
    ).toBe(false);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "canceled" });
    // Liveness control: a Retry clicked after the newer Stop captures its
    // epoch and is the deliberate post-Stop send - it releases, and the
    // refresh absorbs nothing: both baselines read the epochs as they stand.
    const fresh = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    expect(
      await storage.releaseCanceled(row.clientMutationId, {
        stopEpoch: fresh.stopEpoch,
        displayedStopEpoch: fresh.displayedStopEpoch,
      }),
    ).toBe(true);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "submitting",
      stopEpochAtEnqueue: 0,
      displayedStopEpochAtEnqueue: 2,
    });
    storage.close();
  });

  test("an explicit reauthorization reopens a Stop-crossed blockedUnknown row under its original mutation ID with its attempt evidence", async () => {
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
    // The user's click: its capture reads the post-Stop epochs, and the
    // reauthorization deliberately outranks the crossed Stop - under the
    // ORIGINAL mutation ID, with the attempt evidence preserved.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    expect(
      await storage.reauthorizeBlockedMutation(row.clientMutationId, {
        stopEpoch: capture.stopEpoch,
        displayedStopEpoch: capture.displayedStopEpoch,
      }),
    ).toBe(true);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "submitting",
      clientMutationId: row.clientMutationId,
      attempted: true,
      stopEpochAtEnqueue: capture.stopEpoch,
      displayedStopEpochAtEnqueue: capture.displayedStopEpoch,
    });
    // The row proceeds: it is the ref's dispatchable head again.
    expect(await storage.nextDispatchable(DESTINATION)).toMatchObject({
      clientMutationId: row.clientMutationId,
    });
    storage.close();
  });

  test("a reauthorization whose capture a newer displayed Stop outranks refuses and keeps the row parked", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    // The click's capture, then a second Stop inside the capture-to-release
    // window: the reauthorization must refuse the click for the newer Stop.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(
      await storage.reauthorizeBlockedMutation(row.clientMutationId, {
        stopEpoch: capture.stopEpoch,
        displayedStopEpoch: capture.displayedStopEpoch,
      }),
    ).toBe(false);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    expect(await storage.nextDispatchable(DESTINATION)).toBeUndefined();
    // Liveness control: a click whose capture reads the newer Stop still
    // reauthorizes - it is the deliberate post-Stop send.
    const fresh = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    expect(
      await storage.reauthorizeBlockedMutation(row.clientMutationId, {
        stopEpoch: fresh.stopEpoch,
        displayedStopEpoch: fresh.displayedStopEpoch,
      }),
    ).toBe(true);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "submitting" });
    storage.close();
  });

  test("markAttempted refuses a reauthorized row a newer Stop crosses and keeps it delivery-uncertain", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    // The explicit Retry reauthorizes the row past the crossed Stop.
    const capture = await storage.getOutboxWithStopEpoch(row.clientMutationId);
    expect(
      await storage.reauthorizeBlockedMutation(row.clientMutationId, {
        stopEpoch: capture.stopEpoch,
        displayedStopEpoch: capture.displayedStopEpoch,
      }),
    ).toBe(true);
    // A NEWER Stop lands on the displayed ref while the reauthorized row waits
    // for its dispatch: the cancel scan skips the row (attempt evidence is
    // preserved), so the pre-transport gate is the only fence left.
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    // The gate must refuse the redispatch: the row parks delivery-uncertain
    // with its attempt evidence, never dispatching past the newer Stop.
    expect(await storage.markAttempted(row.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
    });
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

  test("markAttempted clears a row an explicit Retry released past its captured Stop", async () => {
    const storage = store();
    const original = await storage.enqueueIntent(intent("hello", DESTINATION));
    await storage.enqueueInterruptAndCancel(interruptIntent(DESTINATION));
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({ state: "canceled" });
    // The Retry pressed after the Stop is §9 item 7's deliberate post-Stop
    // send: its release refreshes the baseline to the Stop's own epoch.
    const capture = await storage.getOutboxWithStopEpoch(original.clientMutationId);
    expect(
      await storage.releaseCanceled(original.clientMutationId, {
        stopEpoch: capture.stopEpoch,
        displayedStopEpoch: capture.displayedStopEpoch,
      }),
    ).toBe(true);
    // The pre-transport fence must let the deliberate send through: the
    // baselines the release refreshed are current.
    expect(await storage.markAttempted(original.clientMutationId)).toBe(true);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({
      state: "submitting",
      attempted: true,
    });
    storage.close();
  });

  test("a fence-stopped retargeted resend keeps its source recovery rows and answers not-sent", async () => {
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
    // The press answers a distinct not-sent outcome: no destination row
    // exists, and the composition's only durable copy stays in the recovery
    // tray on the session the user is looking at, with its existing
    // affordances - never a canceled row invisible to its author.
    expect(result).toEqual({ status: "fenced" });
    expect(await storage.listOutbox(DESTINATION)).toEqual([]);
    expect(await storage.getRecovery(original.clientMutationId)).toBeDefined();
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
    // Stop: explicit Retry reauthorizes under the original mutation ID, and
    // the refreshed baselines pass the pre-transport gate - the resend goes.
    const capture = await storage.getOutboxWithStopEpoch(original.clientMutationId);
    expect(await storage.reauthorizeBlockedMutation(original.clientMutationId, { stopEpoch: capture.stopEpoch })).toBe(
      true,
    );
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({ state: "submitting" });
    expect(await storage.markAttempted(original.clientMutationId)).toBe(true);
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
    // The real attempt's in-band refusal proves the truth: the count reads
    // one-for-one and the row cancels for the Stop - no phantom attempt
    // keeps a provably unsent row delivery-uncertain forever.
    expect(await storage.unmarkAttempted(original.clientMutationId)).toBe(false);
    expect(await storage.getOutbox(original.clientMutationId)).toMatchObject({
      state: "canceled",
      attempted: false,
    });
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
