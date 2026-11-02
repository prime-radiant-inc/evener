// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import type { MutationIntent, MutationOutboxRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { rewriteOutboxRowWithoutFields } from "./testing/legacyOutboxRows";

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

  // §4's stop barrier, the half a second connection exercises: the submitter
  // reads the ref's stop epoch at its click, another tab's Stop commits while
  // the submitter's write is still in flight, and the write that lands after
  // that cancel must commit born-"canceled" - the one interleave transaction
  // ordering cannot fence, because the engine's FIFO is exactly what put the
  // enqueue's write after the cancel scan.
  test("an in-flight enqueue whose barrier predates another tab's stop commits canceled, not submitting", async () => {
    // Two connections share one id space: distinct tabs mint distinct ids, and
    // per-store counters would collide on the outbox's primary key.
    const ids = idSequence();
    const sender = store({ createMutationId: ids });
    const capture = await sender.readStopEpoch(TARGET);
    expect(capture).toBe(0);

    const stopper = store({ createMutationId: ids });
    const interrupt = await stopper.enqueueInterruptAndCancel(interruptIntent());
    stopper.close();

    const raced = await sender.enqueueIntent(intent("raced the stop"), { stopEpoch: capture });
    expect(raced).toMatchObject({ state: "canceled", attempted: false });
    // The FIFO head is the stop's own interrupt, never the canceled row behind it.
    expect(await sender.nextDispatchable(TARGET)).toMatchObject({ clientMutationId: interrupt.clientMutationId });
    // A canceled row's only re-queue is still the explicit user Retry: the
    // mint removes the old row and mints a fresh dispatchable one.
    const racedCapture = await sender.getOutboxWithStopEpoch(raced.clientMutationId);
    const remint = await sender.mintRetryFromRow(raced.clientMutationId, racedCapture);
    expect(remint).toMatchObject({ state: "submitting" });
    expect(await sender.getOutbox(raced.clientMutationId)).toBeUndefined();
    sender.close();
  });

  test("an enqueue after the stop captures the bumped epoch and still sends", async () => {
    const storage = store();
    await storage.cancelUnattempted(TARGET);
    const capture = await storage.readStopEpoch(TARGET);
    expect(capture).toBe(1);

    // The user clicked send after the stop: the barrier the click captured is
    // the post-stop epoch, nothing intervenes, and the row goes live. A caller
    // passing no barrier at all (a host that never captured) is unchanged.
    const after = await storage.enqueueIntent(intent("sent after the stop"), { stopEpoch: capture });
    const barrierless = await storage.enqueueIntent(intent("no barrier"));
    expect(after.state).toBe("submitting");
    expect(barrierless.state).toBe("submitting");
    expect(await storage.nextDispatchable(TARGET)).toMatchObject({ method: "turn/queue" });
    storage.close();
  });

  // §4's stop barrier on the mint, the Retry half: the mint of a canceled row
  // is the one transition that can carry a row a newer Stop claimed back into
  // play — the Stop's own cancel scan skips a row already canceled — so it
  // carries the same click-time capture the enqueue compares. The mint reads
  // both stop epochs inside its own write transaction, and a Stop that landed
  // between the capture and the mint is never absorbed: the fresh row commits
  // born-canceled for it, the same comparison the enqueue barrier carries.
  test("a mint whose capture predates another tab's Stop commits the fresh row born-canceled", async () => {
    const ids = idSequence();
    const retrier = store({ createMutationId: ids });
    const canceled = await retrier.enqueueIntent(intent("canceled by the first stop"));
    await retrier.cancelUnattempted(TARGET);
    // The Retry click's capture: the row and the epoch in one read.
    const capture = await retrier.getOutboxWithStopEpoch(canceled.clientMutationId);
    expect(capture.record?.state).toBe("canceled");
    expect(capture.stopEpoch).toBe(1);

    // The second tab's Stop: its scan skips the canceled row, and its bump is
    // the one trace the mint's birth compare must catch.
    const stopper = store({ createMutationId: ids });
    const interrupt = await stopper.enqueueInterruptAndCancel(interruptIntent());
    stopper.close();

    const crossed = await retrier.mintRetryFromRow(canceled.clientMutationId, capture);
    expect(crossed).toMatchObject({ state: "canceled" });
    expect(await retrier.getOutbox(canceled.clientMutationId)).toBeUndefined();
    // The born-canceled row is not the queue's head: the Stop's own interrupt
    // is, and dispatching it must not carry the canceled row.
    expect(await retrier.nextDispatchable(TARGET)).toMatchObject({ clientMutationId: interrupt.clientMutationId });

    // A capture taken after the newer Stop — the deliberate post-Stop Retry,
    // §9 item 7's protected send — compares equal and mints born-submitting.
    const fresh = await retrier.getOutboxWithStopEpoch(crossed!.clientMutationId);
    const remint = await retrier.mintRetryFromRow(crossed!.clientMutationId, fresh);
    expect(remint).toMatchObject({ state: "submitting" });
    expect(await retrier.getOutbox(crossed!.clientMutationId)).toBeUndefined();
    retrier.close();
  });

  // The epoch rides the sequence row, so every allocation that writes that
  // row back must preserve it - and a reload (a fresh connection) reads the
  // same count both stop paths bump.
  test("the stop epoch survives later enqueues, a reload, and both stop paths bump it", async () => {
    const ids = idSequence();
    const first = store({ createMutationId: ids });
    await first.cancelUnattempted(TARGET);
    await first.enqueueIntent(intent("after the first stop"));
    await first.enqueueIntent(intent("and another"));
    expect(await first.readStopEpoch(TARGET)).toBe(1);
    first.close();

    const reloaded = store({ createMutationId: ids });
    expect(await reloaded.readStopEpoch(TARGET)).toBe(1);
    await reloaded.enqueueInterruptAndCancel(interruptIntent());
    expect(await reloaded.readStopEpoch(TARGET)).toBe(2);
    await reloaded.cancelUnattempted(TARGET);
    expect(await reloaded.readStopEpoch(TARGET)).toBe(3);
    reloaded.close();
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
    // The pre-#936 shape: a row the old code persisted with no `attempted`
    // metadata at all.
    await rewriteOutboxRowWithoutFields(indexedDB, databaseName, legacy.clientMutationId, ["attempted"]);
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
    // Scoped: another ref's canceled note row and a canceled non-note row
    // keep their state. The settled-newer boundary the earlier round
    // disclosed is now CLOSED by the settle-time retirement ruling: an
    // OLDER blockedUnknown note row on the same target retires to terminal
    // "superseded" with this settlement (state change only - its id and
    // attempt evidence stay, so its own late receipt still settles it),
    // which kills the reflected-newer stale-dispatch case by construction:
    // by the time a reconcile could re-queue the older save, it is already
    // terminal.
    expect((await storage.getOutbox(otherRefNote.clientMutationId))?.state).toBe("canceled");
    expect((await storage.getOutbox(canceledTurn.clientMutationId))?.state).toBe("canceled");
    expect((await storage.getOutbox(uncertainNote.clientMutationId))?.state).toBe("superseded");
    expect((await storage.getOutbox(uncertainNote.clientMutationId))?.attempted).toBe(true);
    storage.close();
  });

  // RoboRev PR #1873 medium, the fresh review's fire-and-forget finding: the
  // supersede discard commits with no notification of its own — the settle
  // that spawned it has already notified — so the storage must tell the
  // owning runtime when the cleanup's write completes. Zero deletions
  // included: another tab may have removed the rows first, and this tab's
  // cached projection is exactly what zero leaves stale. A non-note settle
  // runs no discard, so it must not fire the listener either.
  test("the supersede discard notifies the owning runtime when its write completes, zero included", async () => {
    const storage = store();
    const notified: string[] = [];
    storage.setSupersededDiscardListener((targetRef) => notified.push(targetRef));

    const superseded = await storage.enqueueIntent(noteIntent("the stop-canceled note"));
    await storage.cancelUnattempted(TARGET);

    // Another connection removes the canceled row first — this storage's own
    // discard will commit with zero deletions.
    const other = store();
    await other.discardCanceled(TARGET);
    other.close();

    const newerNote = await storage.enqueueIntent(noteIntent("the newer save"));
    expect(await storage.settleReceipt(newerNote.clientMutationId, "reflected")).toBe(true);
    // A read queued behind the discard's write resolves after it, so the
    // listener has fired by the time this await returns.
    await storage.getOutbox(newerNote.clientMutationId);
    expect(notified).toEqual([TARGET]);
    expect(await storage.getOutbox(superseded.clientMutationId)).toBeUndefined();

    // A non-note settle runs no supersede discard and notifies nothing.
    const turn = await storage.enqueueIntent(intent("a turn row"));
    expect(await storage.settleReceipt(turn.clientMutationId, "reflected")).toBe(true);
    await storage.getOutbox(turn.clientMutationId);
    expect(notified).toEqual([TARGET]);
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

  test("an old tab's version-2 open against the version-3 database fails closed", async () => {
    const storage = store();
    await storage.enqueueIntent(intent("a row the old code cannot classify"));
    storage.close();

    // The deployed pre-cancellation code opens the database at version 2. The
    // version-3 database this build writes must refuse that open with a
    // VersionError instead of sharing rows the old nextDispatchable cannot
    // read honestly: it returns undefined unless the FIRST record is
    // "submitting", so a canceled row at the head of the FIFO would stall the
    // ref's whole queue in the old tab, silently
    // (docs/design/stop-cancellation-outbox.md §8). Failing the open is the
    // safe direction: every outbox read and write in the old tab then reports
    // a storage error, and projections degrade to "storage unavailable".
    const openRequest = indexedDB.open(databaseName, 2);
    const failure = await new Promise<unknown>((resolve, reject) => {
      openRequest.addEventListener("success", () => reject(new Error("the version-2 open unexpectedly succeeded")), {
        once: true,
      });
      openRequest.addEventListener("error", () => resolve(openRequest.error), { once: true });
    });
    expect((failure as DOMException).name).toBe("VersionError");
  });

  test("an old tab's version-3 open against the version-4 database fails closed", async () => {
    const storage = store();
    await storage.enqueueIntent(intent("a row the old code cannot count"));
    storage.close();

    // The deployed pre-counter code opens the database at version 3. The
    // version-4 database this build writes must refuse that open with a
    // VersionError instead of sharing rows whose attempt evidence the old
    // code cannot count: a new tab's refusal rollback would decrement a
    // counter that never counted the old tab's concurrent dispatch, erasing
    // the row's only attempt evidence so Stop could misclassify a possibly
    // delivered row as never sent (docs/design/stop-cancellation-outbox.md
    // §8). Failing the open is the safe direction: every outbox read and
    // write in the old tab then reports a storage error, and projections
    // degrade to "storage unavailable" - the safe mixed-version state is a
    // reload, never a silent share.
    const openRequest = indexedDB.open(databaseName, 3);
    const failure = await new Promise<unknown>((resolve, reject) => {
      openRequest.addEventListener("success", () => reject(new Error("the version-3 open unexpectedly succeeded")), {
        once: true,
      });
      openRequest.addEventListener("error", () => resolve(openRequest.error), { once: true });
    });
    expect((failure as DOMException).name).toBe("VersionError");
  });

  test("an old tab's version-4 open against the version-5 database fails closed", async () => {
    const storage = store();
    await storage.enqueueIntent(intent("a row the old code cannot fence"));
    storage.close();

    // The pre-fence code opens the database at version 4. The version-5
    // database this build writes must refuse that open with a VersionError
    // instead of sharing rows the old code cannot fence honestly: an old
    // tab's markAttempted commits attempt evidence with no crossed-fence
    // check, so a mixed-version share would let the old tab redispatch a
    // row a newer Stop crossed, and its cancel scan misses displayed-ref
    // rows entirely - the original #5182 High again, mixed-version
    // (docs/design/stop-cancellation-outbox.md §8). Failing the open is the
    // safe direction: every outbox read and write in the old tab then
    // reports a storage error, and projections degrade to "storage
    // unavailable" - the safe mixed-version state is a reload, never a
    // silent share.
    const openRequest = indexedDB.open(databaseName, 4);
    const failure = await new Promise<unknown>((resolve, reject) => {
      openRequest.addEventListener("success", () => reject(new Error("the version-4 open unexpectedly succeeded")), {
        once: true,
      });
      openRequest.addEventListener("error", () => resolve(openRequest.error), { once: true });
    });
    expect((failure as DOMException).name).toBe("VersionError");
  });

  // #4342 (round-5 review), under the mint: an explicit Retry is the user
  // deliberately sending after a Stop (§4's exemption), so it re-queues the
  // row's work past the Stop its fence held: the fresh row's birth baselines
  // are the epochs as they stand, and its next refusal redelivers instead of
  // re-canceling.
  test("an explicit Retry mints a fresh row past the Stop that held its predecessor", async () => {
    const stopped = store();
    const row = await stopped.enqueueIntent(intent("refused while stopped"));
    await stopped.markAttempted(row.clientMutationId);
    // The Stop lands while the attempt is in flight: the scan skips the
    // attempted row, the epoch bumps, and the refusal's rollback returns the
    // row to its never-attempted shape - the pre-transport gate then refuses
    // the redispatch for the crossed Stop.
    await stopped.enqueueInterruptAndCancel(intent("stop"));
    // The refusal proves the attempt never landed, so the Stop that landed
    // while the attempt was in flight is the row's truth: the rollback holds
    // it canceled (#4342's hold, shared with the host-neutral conformance
    // suite), never dispatchable ahead of the Stop's interrupt - released
    // only by the explicit Retry below. The pre-transport gate stays
    // defense-in-depth for a pass the rollback never sees.
    expect(await stopped.unmarkAttempted(row.clientMutationId)).toBe(false);
    expect((await stopped.getOutbox(row.clientMutationId))?.state).toBe("canceled");
    expect(await stopped.markAttempted(row.clientMutationId)).toBe(false);
    expect((await stopped.getOutbox(row.clientMutationId))?.state).toBe("canceled");

    // The user presses Retry after the Stop: the press's capture is the
    // post-Stop epoch, the mint's identity check passes, and the fresh row's
    // own birth baseline IS that epoch.
    const capture = await stopped.getOutboxWithStopEpoch(row.clientMutationId);
    const minted = await stopped.mintRetryFromRow(row.clientMutationId, capture);
    expect(minted).toMatchObject({ state: "submitting" });
    expect(await stopped.getOutbox(row.clientMutationId)).toBeUndefined();

    // The minted row's next refusal must not see the historical Stop again.
    await stopped.markAttempted(minted!.clientMutationId);
    expect(await stopped.unmarkAttempted(minted!.clientMutationId)).toBe(true);
    expect((await stopped.getOutbox(minted!.clientMutationId))?.state).toBe("submitting");
    // And without another user action, the ordinary drive settles it.
    await stopped.markAttempted(minted!.clientMutationId);
    expect(await stopped.settleReceipt(minted!.clientMutationId, "reflected")).toBe(true);
    expect(await stopped.getOutbox(minted!.clientMutationId)).toBeUndefined();
    stopped.close();
  });

  // RoboRev round-1 H2 (#4342, the concurrent shape): with two attempts
  // outstanding, a Stop followed by ONE refusal leaves the other attempt in
  // flight - the refusal proves only its own attempt never landed. The
  // rollback must preserve the survivor's evidence and park the row with
  // the Stop-fence stamp, never erase the evidence and cancel the row as if
  // the whole send were provably unsent.
  test("a refusal of one of two outstanding attempts parks the row fenced with the survivor's evidence", async () => {
    const stopped = store();
    const row = await stopped.enqueueIntent(intent("two attempts in flight"));
    await stopped.markAttempted(row.clientMutationId);
    await stopped.markAttempted(row.clientMutationId);
    await stopped.enqueueInterruptAndCancel(intent("stop"));
    expect(await stopped.unmarkAttempted(row.clientMutationId)).toBe(false);
    expect(await stopped.getOutbox(row.clientMutationId)).toMatchObject({
      state: "blockedUnknown",
      attempted: true,
      unresolvedAttempts: 1,
      stopFenced: true,
    });
    // The surviving attempt's own refusal is then the proof that cancels.
    expect(await stopped.unmarkAttempted(row.clientMutationId)).toBe(false);
    expect(await stopped.getOutbox(row.clientMutationId)).toMatchObject({
      state: "canceled",
      attempted: false,
      unresolvedAttempts: 0,
    });
    stopped.close();
  });

  test("discardCanceledOfInstance removes only the superseded instance's canceled rows", async () => {
    const storage = store();
    const deadRow = await storage.enqueueIntent({ ...intent("superseded instance's row"), threadId: "thr-old" });
    const liveRow = await storage.enqueueIntent({ ...intent("current instance's row"), threadId: "thr-new" });
    const otherRefRow = await storage.enqueueIntent({ ...intent("other ref's row", OTHER), threadId: "thr-old" });
    const uncertainRow = await storage.enqueueIntent({ ...intent("uncertain row"), threadId: "thr-old" });
    await storage.markAttempted(uncertainRow.clientMutationId);
    await storage.markUnknown(uncertainRow.clientMutationId, "blockedUnknown");
    await storage.cancelUnattempted(TARGET);
    await storage.cancelUnattempted(OTHER);

    const discarded = await storage.discardCanceledOfInstance(TARGET, "thr-old");

    expect(discarded).toEqual([deadRow.clientMutationId]);
    expect(await storage.getOutbox(deadRow.clientMutationId)).toBeUndefined();
    // The current instance's canceled row keeps its explicit-Retry contract,
    // another ref's row is untouched, and a delivery-uncertain row of the dead
    // instance is never removed this way.
    expect((await storage.getOutbox(liveRow.clientMutationId))?.state).toBe("canceled");
    expect((await storage.getOutbox(otherRefRow.clientMutationId))?.state).toBe("canceled");
    expect((await storage.getOutbox(uncertainRow.clientMutationId))?.state).toBe("blockedUnknown");
    storage.close();
  });

  // RoboRev PR #1873 medium, the fresh review's instance identity mismatch:
  // the fencing identity is `instanceId ?? threadId` (the expectedInstanceId
  // every payload carries), but the cross-tab cleanup matched the row's
  // threadId alone. A replacement that rotates the instance while retaining
  // the thread id left the superseded instance's canceled rows attached, so
  // the cleanup must match the same fused identity the fence uses — the
  // row's enqueue-time instance, with its threadId as the pre-instance
  // fallback older rows carry.
  test("discardCanceledOfInstance matches the fused instance identity, not the thread id", async () => {
    const storage = store();
    const deadRow = await storage.enqueueIntent({
      ...intent("superseded instance's row"),
      threadId: "thr-same",
      instanceId: "instance-old",
    });
    const liveRow = await storage.enqueueIntent({
      ...intent("current instance's row"),
      threadId: "thr-same",
      instanceId: "instance-new",
    });
    await storage.cancelUnattempted(TARGET);

    const discarded = await storage.discardCanceledOfInstance(TARGET, "instance-old");

    expect(discarded).toEqual([deadRow.clientMutationId]);
    expect(await storage.getOutbox(deadRow.clientMutationId)).toBeUndefined();
    // The current instance's canceled row keeps its explicit-Retry contract:
    // it is the user's to release, not this cleanup's to remove.
    expect((await storage.getOutbox(liveRow.clientMutationId))?.state).toBe("canceled");
    storage.close();
  });

  test("the version-3 upgrade is additive: an existing version-2 database opens with its rows intact", async () => {
    // Seed the database exactly as the version-2 code left it: the same four
    // stores and indexes, one durable row, no version-3 knowledge anywhere.
    const seeded = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open(databaseName, 2);
      request.addEventListener("upgradeneeded", () => {
        const database = request.result;
        const outbox = database.createObjectStore("outbox", { keyPath: "clientMutationId" });
        outbox.createIndex("byTargetSequence", ["targetRef", "intentSequence"], { unique: true });
        const optimistic = database.createObjectStore("optimistic", { keyPath: "clientMutationId" });
        optimistic.createIndex("byTargetSequence", ["targetRef", "intentSequence"], { unique: true });
        const recovery = database.createObjectStore("recovery", { keyPath: "clientMutationId" });
        recovery.createIndex("byTargetSequence", ["targetRef", "intentSequence"]);
        database.createObjectStore("sequences", { keyPath: "targetRef" });
      });
      request.addEventListener("success", () => resolve(request.result), { once: true });
      request.addEventListener("error", () => reject(request.error), { once: true });
    });
    try {
      const legacyRow: MutationOutboxRecord = {
        ...intent("written by the version-2 code"),
        version: 1,
        clientMutationId: "legacy-row",
        originClientId: "old-tab",
        intentSequence: 1,
        createdAt: 1,
        state: "submitting",
      };
      const write = seeded.transaction("outbox", "readwrite").objectStore("outbox").put(legacyRow);
      await new Promise<void>((resolve, reject) => {
        write.addEventListener("success", () => resolve(), { once: true });
        write.addEventListener("error", () => reject(write.error), { once: true });
      });
      // The version-2 adapter kept the sequence counter in step with its rows;
      // the seed must too, or the upgraded unique index sees two rows claim
      // sequence 1.
      const sequence = seeded
        .transaction("sequences", "readwrite")
        .objectStore("sequences")
        .put({ targetRef: TARGET, lastSequence: 1 });
      await new Promise<void>((resolve, reject) => {
        sequence.addEventListener("success", () => resolve(), { once: true });
        sequence.addEventListener("error", () => reject(sequence.error), { once: true });
      });
    } finally {
      seeded.close();
    }

    const storage = store();
    // The upgraded database keeps the version-2 rows and stays writable: no
    // schema migration, no data loss, sequence allocation continues.
    expect(await storage.getOutbox("legacy-row")).toMatchObject({
      clientMutationId: "legacy-row",
      state: "submitting",
    });
    const after = await storage.enqueueIntent(intent("written after the upgrade"));
    expect(after.intentSequence).toBe(2);
    expect((await storage.listOutbox(TARGET)).map((record) => record.clientMutationId)).toEqual([
      "legacy-row",
      after.clientMutationId,
    ]);
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

  test("markUnknown's state parameter names exactly blockedUnknown", async () => {
    const storage = store();
    const row = await storage.enqueueIntent(intent("typed row"));
    expect(await storage.markUnknown(row.clientMutationId, "blockedUnknown")).toBe(true);

    // The compile-time half of the non-reclassifiable invariant (roborev PR
    // #1873 low), beside the runtime guard pinned in the test above: the
    // parameter's literal type is the contract, so asking the uncertain-outcome
    // write for any other state is a type error - "canceled" is the user's
    // durable decision (only an explicit user Retry releases it), and
    // "submitting" is the settle/reopen paths' verdict. The two misuse
    // bindings below are type-level only and never execute.
    const legal: Parameters<MutationOutboxIndexedDB["markUnknown"]>[1] = "blockedUnknown";
    // @ts-expect-error markUnknown cannot name "canceled"
    const misusedCanceled: Parameters<MutationOutboxIndexedDB["markUnknown"]>[1] = "canceled";
    // @ts-expect-error markUnknown cannot name "submitting"
    const misusedSubmitting: Parameters<MutationOutboxIndexedDB["markUnknown"]>[1] = "submitting";
    expect([legal, misusedCanceled, misusedSubmitting].filter((value) => value === "blockedUnknown")).toEqual([
      "blockedUnknown",
    ]);
    // Nothing but the one legal call ever ran: the row is exactly where it
    // left it.
    expect((await storage.getOutbox(row.clientMutationId))?.state).toBe("blockedUnknown");
    storage.close();
  });

  // The runtime half of that contract: the literal type narrows at compile
  // time, but an untyped caller (a JS bridge, dev tooling) can pass anything,
  // and this delivery-uncertainty write must never fabricate a "canceled"
  // row - the user's durable Stop decision, releasable only by an explicit
  // Retry. The native adapter's runtime guard is the same check.
  test("markUnknown refuses a non-blockedUnknown state at runtime, not only at the type level", async () => {
    const storage = store();
    const row = await storage.enqueueIntent(intent("live"));
    await expect(storage.markUnknown(row.clientMutationId, "canceled" as "blockedUnknown")).rejects.toThrow(
      'markUnknown only names "blockedUnknown"',
    );
    expect((await storage.getOutbox(row.clientMutationId))?.state).toBe("submitting");
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
