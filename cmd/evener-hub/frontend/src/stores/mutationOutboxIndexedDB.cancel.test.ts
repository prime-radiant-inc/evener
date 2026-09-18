// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import type { MutationIntent, MutationOutboxRecord } from "./mutationOutbox";
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

function noteIntent(note: string, targetRef = TARGET): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "notes/human/set",
    payload: { ref: targetRef, expectedInstanceId: "thread-1", note },
    attachments: [],
    optimisticDisplay: null,
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

  // Rewrite a row exactly as the pre-#936 code wrote it: the same record with
  // no `attempted` metadata at all. The adapter has written `attempted` on
  // every row since #936, so the legacy shape can only be seeded by a raw
  // IndexedDB write against the same database - a real durable write at the
  // real boundary, the way the old code actually produced these rows.
  async function rewriteRowWithoutAttemptedMetadata(clientMutationId: string): Promise<void> {
    const openRequest = indexedDB.open(databaseName);
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      openRequest.addEventListener("success", () => resolve(openRequest.result), { once: true });
      openRequest.addEventListener("error", () => reject(openRequest.error), { once: true });
    });
    try {
      const read = database.transaction("outbox", "readonly").objectStore("outbox").get(clientMutationId);
      const row = await new Promise<MutationOutboxRecord>((resolve, reject) => {
        read.addEventListener("success", () => resolve(read.result), { once: true });
        read.addEventListener("error", () => reject(read.error), { once: true });
      });
      const legacyRow: MutationOutboxRecord = { ...row };
      delete legacyRow.attempted;
      const write = database.transaction("outbox", "readwrite").objectStore("outbox").put(legacyRow);
      await new Promise<void>((resolve, reject) => {
        write.addEventListener("success", () => resolve(), { once: true });
        write.addEventListener("error", () => reject(write.error), { once: true });
      });
    } finally {
      database.close();
    }
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

  test("a legacy row without attempted metadata is never canceled by Stop", async () => {
    const storage = store();
    const legacy = await storage.enqueueIntent(intent("legacy row"));
    await rewriteRowWithoutAttemptedMetadata(legacy.clientMutationId);
    const fresh = await storage.enqueueIntent(intent("fresh row"));

    const canceled = await storage.cancelUnattempted(TARGET);

    // The pre-#936 row's attempt state is unknown, not safely unattempted:
    // it may already be on the wire, so Stop reports it in-flight/uncertain
    // instead of writing a cancellation it cannot honor. Only rows the
    // current code marked unattempted may turn canceled.
    expect(canceled).toEqual([fresh.clientMutationId]);
    const surviving = await storage.getOutbox(legacy.clientMutationId);
    expect(surviving).toMatchObject({ state: "submitting" });
    expect("attempted" in (surviving ?? {})).toBe(false);
    storage.close();
  });

  test("a settled newer note save discards the canceled note rows it supersedes", async () => {
    const storage = store();
    const superseded = await storage.enqueueIntent(noteIntent("the stop-canceled note"));
    const canceledTurn = await storage.enqueueIntent(intent("canceled turn row"));
    const uncertainNote = await storage.enqueueIntent(noteIntent("uncertain attempted note"));
    await storage.markAttempted(uncertainNote.clientMutationId);
    await storage.markUnknown(uncertainNote.clientMutationId, "blockedUnknown");
    const otherRefNote = await storage.enqueueIntent(noteIntent("other ref's note", OTHER));
    await storage.cancelUnattempted(TARGET);
    await storage.cancelUnattempted(OTHER);

    // The blur-save the note editor makes after the Stop: a newer note intent,
    // which is the only retry the note UI ever offers (its retry branch is
    // gated on blockedUnknown). When this save commits, the row the Stop
    // canceled is superseded and must leave with it - not stay pinned (with its
    // note text) until the thread is cleared or deleted.
    const newerNote = await storage.enqueueIntent(noteIntent("the newer save"));
    expect(await storage.settleReceipt(newerNote.clientMutationId, "reflected")).toBe(true);

    expect(await storage.getOutbox(superseded.clientMutationId)).toBeUndefined();
    expect(await storage.getOutbox(newerNote.clientMutationId)).toBeUndefined();
    // Scoped: another ref's canceled note row, a canceled non-note row, and a
    // delivery-uncertain attempted note row all keep their state.
    expect((await storage.getOutbox(otherRefNote.clientMutationId))?.state).toBe("canceled");
    expect((await storage.getOutbox(canceledTurn.clientMutationId))?.state).toBe("canceled");
    expect((await storage.getOutbox(uncertainNote.clientMutationId))?.state).toBe("blockedUnknown");
    storage.close();
  });

  test("a note save reconciled as applied also discards the canceled note rows it supersedes", async () => {
    const storage = store();
    const superseded = await storage.enqueueIntent(noteIntent("the stop-canceled note"));
    await storage.cancelUnattempted(TARGET);

    // evener/notes/updated settles the newer save through reconcileIdentities'
    // settleApplied, not its own response receipt: the same supersede must run
    // there, or the pinned row survives every commit path but one.
    const newerNote = await storage.enqueueIntent(noteIntent("the newer save"));
    expect(await storage.settleApplied(newerNote.clientMutationId)).toBe(true);

    expect(await storage.getOutbox(superseded.clientMutationId)).toBeUndefined();
    expect(await storage.getOutbox(newerNote.clientMutationId)).toBeUndefined();
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
