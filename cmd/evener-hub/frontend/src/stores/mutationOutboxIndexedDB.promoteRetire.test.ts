// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import type { MutationRecoveryRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// A refused promote whose queue entry another transition already delivered
// must not outlive the proof: render-side suppression alone resurrects the
// row once the delivery ages past a bounded transcript window, so the
// hydration path settles the UNEDITED recovery durably. These tests run the
// real adapter against fake-indexeddb and read rows back through fresh
// connections, exactly as a reload would.

const TARGET = "local:thread-1";

describe("MutationOutboxIndexedDB retireDeliveredPromoteRecoveries", () => {
  let indexedDB: IDBFactory;
  let databaseName: string;

  beforeEach(() => {
    indexedDB = new IDBFactory();
    databaseName = `mutation-outbox-promote-retire-${crypto.randomUUID()}`;
  });

  function store() {
    return new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence() });
  }

  async function refusedPromote(
    storage: MutationOutboxIndexedDB,
    overrides: { compositionRevision?: number; sourceMutationId?: string; wireId?: string } = {},
  ): Promise<MutationRecoveryRecord> {
    const record = await storage.enqueueIntent({
      targetRef: TARGET,
      threadId: "thread-1",
      method: "turn/promoteQueuedAsSteer",
      payload: {
        ref: TARGET,
        index: 0,
        expectedEntryId: "q1",
        ...(overrides.wireId !== undefined ? { clientMutationId: overrides.wireId } : {}),
      },
      attachments: [],
      optimisticDisplay: {
        method: "turn/promoteQueuedAsSteer",
        input: [{ type: "text", text: "queued text" }],
        ...(overrides.sourceMutationId !== undefined ? { sourceMutationId: overrides.sourceMutationId } : {}),
      },
    });
    const recovery = await storage.transferToRecovery(
      record.clientMutationId,
      "rejected",
      "promote: queue index 0 out of range (depth 0)",
    );
    if (!recovery) throw new Error("transferToRecovery returned nothing");
    if (overrides.compositionRevision !== undefined && overrides.compositionRevision > 0) {
      const edited = await storage.updateRecoveryInput(
        recovery.clientMutationId,
        [{ type: "text", text: "edited text" }],
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        0,
      );
      if (!edited) throw new Error("updateRecoveryInput returned nothing");
    }
    return (await storage.listRecovery(TARGET)).find((row) => row.clientMutationId === recovery.clientMutationId)!;
  }

  test("retires an unedited refused promote whose source is delivered, and only that", async () => {
    const storage = store();
    const delivered = await refusedPromote(storage, { sourceMutationId: "m_source" });
    const undelivered = await refusedPromote(storage, { sourceMutationId: "m_other" });
    const edited = await refusedPromote(storage, {
      sourceMutationId: "m_source",
      compositionRevision: 1,
    });

    const retired = await storage.retireDeliveredPromoteRecoveries(TARGET, new Set(["m_source"]));

    expect(retired).toEqual([delivered.clientMutationId]);
    const survivors = (await storage.listRecovery(TARGET)).map((row) => row.clientMutationId).sort();
    expect(survivors).toEqual([edited.clientMutationId, undelivered.clientMutationId].sort());
    storage.close();
  });

  test("a second call after a reload retires nothing more", async () => {
    const storage = store();
    const delivered = await refusedPromote(storage, { sourceMutationId: "m_source" });
    await storage.retireDeliveredPromoteRecoveries(TARGET, new Set(["m_source"]));
    storage.close();

    const reopened = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: idSequence(),
    });
    const again = await reopened.retireDeliveredPromoteRecoveries(TARGET, new Set(["m_source"]));
    expect(again).toEqual([]);
    expect((await reopened.listRecovery(TARGET)).map((row) => row.clientMutationId)).not.toContain(
      delivered.clientMutationId,
    );
    reopened.close();
  });

  // A promotion retry keeps a same-wire superseded predecessor in the outbox
  // (an uncertain wire's resend inherits the wire identity). When the sweep
  // retires the recovery for delivery, the predecessor must retire in the
  // same transaction: a delayed terminal refusal of that predecessor would
  // otherwise transferToRecovery a fresh refused row for the already
  // delivered message (roborev round 11, finding 2 - discardRecovery's own
  // rule).
  // An uncertain promote retry leaves its superseded predecessor in the outbox
  // while the retry's own refusal becomes a recovery of the SAME wire (the
  // mint inherits the predecessor's id). When the sweep retires that recovery
  // for delivery, the predecessor must retire in the same transaction
  // (discardRecovery's rule): a delayed terminal refusal of the predecessor
  // would otherwise transferToRecovery a fresh refused row for the
  // already-delivered message (roborev round 11, finding 2).
  test("retiring a delivered promote recovery retires its same-wire outbox sibling in the same transaction", async () => {
    const storage = store();
    // A: the promote whose response was lost -> delivery-uncertain.
    const a = await storage.enqueueIntent({
      targetRef: TARGET,
      threadId: "thread-1",
      method: "turn/promoteQueuedAsSteer",
      payload: { ref: TARGET, index: 0, expectedEntryId: "q1" },
      attachments: [],
      optimisticDisplay: {
        method: "turn/promoteQueuedAsSteer",
        input: [{ type: "text", text: "queued text" }],
        sourceMutationId: "m_source",
      },
    });
    await storage.markAttempted(a.clientMutationId);
    await storage.markUnknown(a.clientMutationId, "blockedUnknown");
    // Retry mints B under A's wire id; A settles terminal superseded, keeping
    // its id and attempt evidence for its real attempt's late outcome.
    const capture = await storage.getOutboxWithStopEpoch(a.clientMutationId);
    const b = await storage.mintRetryFromRow(a.clientMutationId, {
      record: capture.record,
      stopEpoch: capture.stopEpoch,
      displayedStopEpoch: 0,
    });
    if (!b) throw new Error("mint failed");
    // B's attempt is refused -> recovery R (key B, wire A).
    await storage.markAttempted(b.clientMutationId);
    await storage.transferToRecovery(b.clientMutationId, "rejected");
    const [recovery] = await storage.listRecovery(TARGET);
    if (!recovery) throw new Error("recovery never seeded");
    const retiredIds: string[] = [];
    storage.setRetiredIdsListener((ids) => retiredIds.push(...ids));

    const retired = await storage.retireDeliveredPromoteRecoveries(TARGET, new Set(["m_source"]));

    expect(retired).toEqual([recovery.clientMutationId]);
    // The superseded predecessor left the outbox with the recovery, and its
    // key reached the retiredIds channel the runtime's registrations clear
    // through.
    const survivingOutbox = await storage.listOutbox(TARGET);
    expect(survivingOutbox.map((row) => row.clientMutationId)).not.toContain(a.clientMutationId);
    expect(retiredIds).toContain(a.clientMutationId);
    expect((await storage.listRecovery(TARGET)).map((row) => row.clientMutationId)).not.toContain(
      recovery.clientMutationId,
    );
    storage.close();
  });
});

function idSequence(): () => string {
  let counter = 0;
  return () => {
    counter += 1;
    return `cmid-${counter}`;
  };
}
