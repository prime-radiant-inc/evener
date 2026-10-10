// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// R09's identity-changing resume re-targets the rows parked under the
// superseded ref onto the resumed one. These run the real adapter against
// fake-indexeddb and read the rows back, exactly as the driver's own storage
// call does.

const FROM = "local:old-identity";
const TO = "local:new-identity";
const OLD_IDENTITY = { threadId: "thr-old", instanceId: "instance-old" };
const NEW_IDENTITY = { threadId: "thr-new", instanceId: "instance-new" };

function intent(text: string, targetRef = FROM): MutationIntent {
  return {
    targetRef,
    threadId: OLD_IDENTITY.threadId,
    instanceId: OLD_IDENTITY.instanceId,
    method: "turn/queue",
    payload: { ref: targetRef, expectedInstanceId: OLD_IDENTITY.instanceId, input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text }] },
  };
}

function textOf(record: { payload: Record<string, unknown> }): string {
  const input = record.payload.input as Array<{ text?: string }> | undefined;
  return input?.[0]?.text ?? "";
}

describe("MutationOutboxIndexedDB retargetOutbox", () => {
  let indexedDB: IDBFactory;
  let databaseName: string;

  beforeEach(() => {
    indexedDB = new IDBFactory();
    databaseName = `mutation-outbox-retarget-${crypto.randomUUID()}`;
    setMutationClientIdentityForTests(undefined);
  });

  function store() {
    let next = 0;
    return new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: () => `mutation-${++next}` });
  }

  test("moves the non-canceled rows to the resumed ref, rewriting the identity and preserving ids and order", async () => {
    const storage = store();
    const first = await storage.enqueueIntent(intent("one"));
    const second = await storage.enqueueIntent(intent("two"));

    const moved = await storage.retargetOutbox(FROM, TO, NEW_IDENTITY);

    expect(moved.outbox.map((record) => record.clientMutationId)).toEqual([
      first.clientMutationId,
      second.clientMutationId,
    ]);
    expect(moved.outbox.map((record) => record.targetRef)).toEqual([TO, TO]);
    expect(moved.outbox.map((record) => record.payload.ref)).toEqual([TO, TO]);
    // The daemon's instance fence token is the resumed identity's.
    expect(moved.outbox.map((record) => record.payload.expectedInstanceId)).toEqual([
      NEW_IDENTITY.instanceId,
      NEW_IDENTITY.instanceId,
    ]);
    expect(moved.outbox.map((record) => record.threadId)).toEqual([NEW_IDENTITY.threadId, NEW_IDENTITY.threadId]);
    expect(moved.outbox.map((record) => record.instanceId)).toEqual([NEW_IDENTITY.instanceId, NEW_IDENTITY.instanceId]);
    expect(await storage.listOutbox(FROM)).toEqual([]);
    const landed = await storage.listOutbox(TO);
    expect(landed.map((record) => record.clientMutationId)).toEqual([first.clientMutationId, second.clientMutationId]);
    expect(landed.map((record) => textOf(record))).toEqual(["one", "two"]);
    storage.close();
  });

  test("leaves canceled rows and other refs behind", async () => {
    const storage = store();
    await storage.enqueueIntent(intent("canceled one"));
    await storage.enqueueIntent(intent("canceled two"));
    await storage.cancelUnattempted(FROM);
    const kept = await storage.enqueueIntent(intent("kept"));
    const other = await storage.enqueueIntent(intent("other ref", "local:unrelated"));

    const moved = await storage.retargetOutbox(FROM, TO, NEW_IDENTITY);

    expect(moved.outbox.map((record) => record.clientMutationId)).toEqual([kept.clientMutationId]);
    expect((await storage.listOutbox(FROM)).map((record) => record.state)).toEqual(["canceled", "canceled"]);
    expect((await storage.listOutbox(TO)).map((record) => record.clientMutationId)).toEqual([kept.clientMutationId]);
    expect((await storage.listOutbox("local:unrelated")).map((record) => record.clientMutationId)).toEqual([
      other.clientMutationId,
    ]);
    storage.close();
  });

  test("moves the accepted-but-unreflected copies too", async () => {
    const storage = store();
    const pending = await storage.enqueueIntent(intent("pending"));
    await storage.settleReceipt(pending.clientMutationId, "pending");
    expect(await storage.listOutbox(FROM)).toEqual([]);
    expect(await storage.listOptimistic(FROM)).toHaveLength(1);

    const moved = await storage.retargetOutbox(FROM, TO, NEW_IDENTITY);

    expect(moved.outbox).toEqual([]);
    expect(moved.optimistic.map((record) => record.clientMutationId)).toEqual([pending.clientMutationId]);
    expect(await storage.listOptimistic(FROM)).toEqual([]);
    const landed = await storage.listOptimistic(TO);
    expect(landed.map((record) => record.clientMutationId)).toEqual([pending.clientMutationId]);
    expect(landed.map((record) => record.payload.ref)).toEqual([TO]);
    expect(landed.map((record) => record.payload.expectedInstanceId)).toEqual([NEW_IDENTITY.instanceId]);
    storage.close();
  });

  // A recovery row (a rejected turn awaiting Retry) is identity-bound by
  // retryBlockedMutation's own fused check (the row's instanceId ?? threadId vs
  // the press model's, threads.ts), so under the superseded ref it could never
  // be retried. The retarget carries it and rewrites the identity so Retry
  // matches the resumed model.
  test("moves a recovery row and rewrites its identity so Retry can reach it", async () => {
    const storage = store();
    const rejected = await storage.enqueueIntent(intent("retry me"));
    const recovery = await storage.transferToRecovery(rejected.clientMutationId, "rejected");
    expect(recovery).toBeDefined();
    expect(await storage.listRecovery(FROM)).toHaveLength(1);

    const moved = await storage.retargetOutbox(FROM, TO, NEW_IDENTITY);

    expect(moved.recovery.map((record) => record.clientMutationId)).toEqual([rejected.clientMutationId]);
    expect(moved.recovery.map((record) => record.instanceId)).toEqual([NEW_IDENTITY.instanceId]);
    expect(moved.recovery.map((record) => record.threadId)).toEqual([NEW_IDENTITY.threadId]);
    expect(moved.recovery.map((record) => record.payload.expectedInstanceId)).toEqual([NEW_IDENTITY.instanceId]);
    expect(await storage.listRecovery(FROM)).toEqual([]);
    expect(await storage.listRecovery(TO)).toHaveLength(1);
    storage.close();
  });

  test("is a no-op when the refs are equal", async () => {
    const storage = store();
    await storage.enqueueIntent(intent("stays"));

    expect(await storage.retargetOutbox(FROM, FROM, NEW_IDENTITY)).toEqual({
      outbox: [],
      optimistic: [],
      recovery: [],
    });
    expect((await storage.listOutbox(FROM)).map((record) => textOf(record))).toEqual(["stays"]);
    storage.close();
  });
});
