// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// Stop's cancellation is a durable record state, not in-memory bookkeeping:
// these tests run the real adapter against fake-indexeddb and read the rows
// back through fresh connections, exactly as a reload or a second tab would.

const TARGET = "local:thread-1";
const OTHER = "local:thread-2";

function intent(text: string, targetRef = TARGET): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "turn/queue",
    payload: { ref: targetRef, input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { text },
  };
}

function interruptIntent(targetRef = TARGET): MutationIntent {
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

describe("MutationOutboxIndexedDB cancellation", () => {
  let indexedDB: IDBFactory;
  let databaseName: string;

  beforeEach(() => {
    indexedDB = new IDBFactory();
    databaseName = `mutation-outbox-cancel-${crypto.randomUUID()}`;
    setMutationClientIdentityForTests(undefined);
  });

  function store(options: Partial<ConstructorParameters<typeof MutationOutboxIndexedDB>[0]> = {}) {
    return new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence(), ...options });
  }

  test("enqueueInterruptAndCancel cancels every non-attempted row for the ref and commits the interrupt with them", async () => {
    const storage = store();
    const queued = await storage.enqueueIntent(intent("queued"));
    const blocked = await storage.enqueueIntent(intent("blocked"));
    await storage.markUnknown(blocked.clientMutationId, "blockedUnknown");
    const inFlight = await storage.enqueueIntent(intent("in flight"));
    await storage.markAttempted(inFlight.clientMutationId);
    const attemptedBlocked = await storage.enqueueIntent(intent("attempted blocked"));
    await storage.markAttempted(attemptedBlocked.clientMutationId);
    await storage.markUnknown(attemptedBlocked.clientMutationId, "blockedUnknown");
    const otherRef = await storage.enqueueIntent(intent("other ref", OTHER));

    const interrupt = await storage.enqueueInterruptAndCancel(interruptIntent());

    expect(interrupt).toMatchObject({
      method: "turn/interrupt",
      state: "submitting",
      attempted: false,
      // The ref's sequence continues: the interrupt is the ref's fifth intent.
      intentSequence: 5,
    });
    expect((await storage.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
    expect((await storage.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
    // Attempted rows may be on the wire; cancellation cannot unsend them.
    expect(await storage.getOutbox(inFlight.clientMutationId)).toMatchObject({ state: "submitting", attempted: true });
    expect(await storage.getOutbox(attemptedBlocked.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
    });
    expect((await storage.getOutbox(otherRef.clientMutationId))?.state).toBe("submitting");
    storage.close();
  });

  test("an aborted enqueueInterruptAndCancel commit leaves no interrupt record and no cancellations", async () => {
    const crashing = store({
      beforeCommit(operation) {
        if (operation === "enqueueInterruptAndCancel") throw new Error("tab crashed before commit");
      },
    });
    const queued = await crashing.enqueueIntent(intent("queued"));

    await expect(crashing.enqueueInterruptAndCancel(interruptIntent())).rejects.toThrow("tab crashed before commit");

    const recovered = store();
    expect(await recovered.getOutbox(queued.clientMutationId)).toMatchObject({ state: "submitting" });
    expect((await recovered.listOutbox(TARGET)).map((record) => record.method)).toEqual(["turn/queue"]);
    crashing.close();
    recovered.close();
  });

  test("a canceled row never reopens, even across a reload", async () => {
    const firstPage = store();
    const queued = await firstPage.enqueueIntent(intent("queued"));
    const blocked = await firstPage.enqueueIntent(intent("blocked"));
    await firstPage.markUnknown(blocked.clientMutationId, "blockedUnknown");
    await firstPage.cancelUnattempted(TARGET);
    // The reload hazard: reconciliation proves nothing about these rows, and
    // restoreProvenAbsent is the one reopen path. It must leave them canceled.
    expect(await firstPage.restoreProvenAbsent(TARGET, new Set())).toEqual([]);
    expect((await firstPage.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
    expect((await firstPage.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
    firstPage.close();

    const reloaded = store();
    expect(await reloaded.restoreProvenAbsent(TARGET, new Set())).toEqual([]);
    expect((await reloaded.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
    expect((await reloaded.getOutbox(blocked.clientMutationId))?.state).toBe("canceled");
    expect(await reloaded.nextDispatchable(TARGET)).toBeUndefined();
    reloaded.close();
  });

  test("a second tab cannot resurrect a row the first tab canceled", async () => {
    const tabA = store();
    const queued = await tabA.enqueueIntent(intent("queued"));
    await tabA.cancelUnattempted(TARGET);

    const tabB = store();
    expect(await tabB.restoreProvenAbsent(TARGET, new Set())).toEqual([]);
    expect((await tabB.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
    expect(await tabB.nextDispatchable(TARGET)).toBeUndefined();
    tabA.close();
    tabB.close();
  });

  test("cancelUnattempted returns the canceled ids and never touches attempted rows or other refs", async () => {
    const storage = store();
    const queued = await storage.enqueueIntent(intent("queued"));
    const inFlight = await storage.enqueueIntent(intent("in flight"));
    await storage.markAttempted(inFlight.clientMutationId);
    const otherRef = await storage.enqueueIntent(intent("other ref", OTHER));

    const canceled = await storage.cancelUnattempted(TARGET);

    expect(canceled).toEqual([queued.clientMutationId]);
    expect((await storage.getOutbox(queued.clientMutationId))?.state).toBe("canceled");
    expect(await storage.getOutbox(inFlight.clientMutationId)).toMatchObject({ state: "submitting", attempted: true });
    expect((await storage.getOutbox(otherRef.clientMutationId))?.state).toBe("submitting");
    storage.close();
  });

  test("a canceled row cannot be marked attempted or reclassified back to blockedUnknown", async () => {
    const storage = store();
    const queued = await storage.enqueueIntent(intent("queued"));
    await storage.cancelUnattempted(TARGET);

    // Cancellation is terminal: only an explicit Retry releases the row, and a
    // late uncertain-outcome write must not make it reopenable again.
    expect(await storage.markAttempted(queued.clientMutationId)).toBe(false);
    expect(await storage.markUnknown(queued.clientMutationId, "blockedUnknown")).toBe(false);
    expect(await storage.getOutbox(queued.clientMutationId)).toMatchObject({ state: "canceled", attempted: false });
    storage.close();
  });

  test("a canceled head row does not park the queue, but a blockedUnknown head still does", async () => {
    const storage = store();
    await storage.enqueueIntent(intent("canceled head"));
    await storage.cancelUnattempted(TARGET);
    // The canceled row was provably never sent, so the later intent may go.
    const later = await storage.enqueueIntent(intent("later send"));
    expect((await storage.nextDispatchable(TARGET))?.clientMutationId).toBe(later.clientMutationId);

    // Delivery-uncertain rows keep the FIFO closed: skipping them could
    // reorder a send the daemon may already have applied.
    const blockedStorage = store({ databaseName: `mutation-outbox-blocked-${crypto.randomUUID()}` });
    const blockedHead = await blockedStorage.enqueueIntent(intent("blocked head"));
    await blockedStorage.markUnknown(blockedHead.clientMutationId, "blockedUnknown");
    await blockedStorage.enqueueIntent(intent("queued behind"));
    expect(await blockedStorage.nextDispatchable(TARGET)).toBeUndefined();

    // The interrupt itself dispatches even though the rows it canceled sort
    // ahead of it: a Stop whose own interrupt never went out would stop nothing.
    const interruptStorage = store({ databaseName: `mutation-outbox-interrupt-${crypto.randomUUID()}` });
    await interruptStorage.enqueueIntent(intent("canceled by stop"));
    const interrupt = await interruptStorage.enqueueInterruptAndCancel(interruptIntent());
    expect((await interruptStorage.nextDispatchable(TARGET))?.clientMutationId).toBe(interrupt.clientMutationId);

    storage.close();
    blockedStorage.close();
    interruptStorage.close();
  });

  test("discardCanceled removes only the ref's canceled rows", async () => {
    const storage = store();
    const first = await storage.enqueueIntent(intent("first"));
    const second = await storage.enqueueIntent(intent("second"));
    const otherRef = await storage.enqueueIntent(intent("other ref", OTHER));
    await storage.cancelUnattempted(TARGET);
    await storage.cancelUnattempted(OTHER);
    const pending = await storage.enqueueIntent(intent("still pending"));

    const discarded = await storage.discardCanceled(TARGET);

    expect(discarded?.sort()).toEqual([first.clientMutationId, second.clientMutationId].sort());
    expect((await storage.listOutbox(TARGET)).map((record) => record.clientMutationId)).toEqual([
      pending.clientMutationId,
    ]);
    expect((await storage.getOutbox(otherRef.clientMutationId))?.state).toBe("canceled");
    storage.close();
  });
});
