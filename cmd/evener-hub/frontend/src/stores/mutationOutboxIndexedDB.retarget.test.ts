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

function intent(text: string, targetRef = FROM): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "turn/queue",
    payload: { ref: targetRef, input: [{ type: "text", text }] },
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

  test("moves the non-canceled rows to the resumed ref, preserving ids and order and rewriting payload.ref", async () => {
    const storage = store();
    const first = await storage.enqueueIntent(intent("one"));
    const second = await storage.enqueueIntent(intent("two"));

    const moved = await storage.retargetOutbox(FROM, TO);

    expect(moved.map((record) => record.clientMutationId)).toEqual([first.clientMutationId, second.clientMutationId]);
    expect(moved.map((record) => record.targetRef)).toEqual([TO, TO]);
    expect(moved.map((record) => record.payload.ref)).toEqual([TO, TO]);
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

    const moved = await storage.retargetOutbox(FROM, TO);

    expect(moved.map((record) => record.clientMutationId)).toEqual([kept.clientMutationId]);
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

    const moved = await storage.retargetOutbox(FROM, TO);

    expect(moved).toEqual([]);
    expect(await storage.listOptimistic(FROM)).toEqual([]);
    expect((await storage.listOptimistic(TO)).map((record) => record.clientMutationId)).toEqual([
      pending.clientMutationId,
    ]);
    expect((await storage.listOptimistic(TO)).map((record) => record.payload.ref)).toEqual([TO]);
    storage.close();
  });

  test("is a no-op when the refs are equal", async () => {
    const storage = store();
    await storage.enqueueIntent(intent("stays"));

    expect(await storage.retargetOutbox(FROM, FROM)).toEqual([]);
    expect((await storage.listOutbox(FROM)).map((record) => textOf(record))).toEqual(["stays"]);
    storage.close();
  });
});
