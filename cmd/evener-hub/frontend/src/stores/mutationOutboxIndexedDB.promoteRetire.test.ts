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
    overrides: { compositionRevision?: number; sourceMutationId?: string } = {},
  ): Promise<MutationRecoveryRecord> {
    const record = await storage.enqueueIntent({
      targetRef: TARGET,
      threadId: "thread-1",
      method: "turn/promoteQueuedAsSteer",
      payload: { ref: TARGET, index: 0, expectedEntryId: "q1" },
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
});

function idSequence(): () => string {
  let counter = 0;
  return () => {
    counter += 1;
    return `cmid-${counter}`;
  };
}
