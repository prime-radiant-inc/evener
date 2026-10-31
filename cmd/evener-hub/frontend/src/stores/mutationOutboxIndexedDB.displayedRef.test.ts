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
      { stopEpoch: 0, targetRef: DISPLAYED },
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

  test("a reconcile reopen holds an uncertain parked row canceled after a Stop on the displayed ref", async () => {
    const storage = store();
    const row = await seedRetargetedResend(storage);
    await storage.markAttempted(row.clientMutationId);
    await storage.markUnknown(row.clientMutationId, "blockedUnknown");
    await storage.enqueueInterruptAndCancel(interruptIntent(DISPLAYED));
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "blockedUnknown" });
    // The authoritative read proves the row never reached the hub, so the
    // reopen could make it dispatchable - unless a Stop crossed it.
    const restored = await storage.restoreProvenAbsent(DESTINATION, new Set());
    expect(restored).toContain(row.clientMutationId);
    expect(await storage.getOutbox(row.clientMutationId)).toMatchObject({ state: "canceled" });
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
});
