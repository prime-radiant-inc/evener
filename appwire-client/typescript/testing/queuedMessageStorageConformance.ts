import { describe, expect, test } from "vitest";
import type {
  MutationIntent,
  MutationOutboxRecord,
  MutationOutboxStorage,
  MutationStopBarrier,
  QueuedMessageStorage,
  WebQueuedMessageStorage,
} from "../state/mutation";

export const queuedIntent = (targetRef = "local:queued"): MutationIntent<never> => ({
  targetRef,
  threadId: "thread",
  instanceId: "instance",
  method: "turn/queue",
  payload: {
    ref: targetRef,
    input: [
      { type: "text", text: "same input" },
      { type: "skill", name: "inspect" },
    ],
  },
  optimisticDisplay: { input: [{ type: "text", text: "same input" }] },
  attachments: [],
  composerText: "same input @inspect",
  composerMentions: [{ kind: "skill", name: "inspect", offset: 11 }],
});

export type Storage = MutationOutboxStorage &
  QueuedMessageStorage & {
    listOutbox(targetRef?: string): Promise<MutationOutboxRecord[]>;
    readStopEpoch(targetRef: string): Promise<number>;
  };
export interface QueuedStorageFixture {
  storage: Storage;
  peer: Storage;
  reopen(): Promise<Storage>;
  release(record: MutationOutboxRecord, barrier: MutationStopBarrier): Promise<boolean>;
}

// Each host supplies REAL storage. These contracts catch split ownership,
// stale-snapshot admission, and partial recovery consumption, not UI wording.
export function describeQueuedMessageStorage(name: string, create: () => QueuedStorageFixture) {
  describe(`${name} queued-message ownership`, () => {
    test("editClaimsOneDurableOwner", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      const claims = await Promise.all([
        f.storage.editQueuedMessage(original, capture),
        f.peer.editQueuedMessage(original, capture),
      ]);
      expect(claims.filter(Boolean)).toHaveLength(1);
      const storage = await f.reopen();
      expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
      expect(await storage.getRecovery(original.clientMutationId)).toMatchObject({
        recoveryKind: "editing",
        composerText: "same input @inspect",
        composerMentions: [{ kind: "skill", name: "inspect", offset: 11 }],
        payload: {
          input: [
            { type: "text", text: "same input" },
            { type: "skill", name: "inspect" },
          ],
        },
      });
      expect(await storage.editQueuedMessage(original, capture)).toBeUndefined();
      expect(await storage.markAttempted(original.clientMutationId)).toBe(false);
    });

    test("removeOnlySelectedIdentity", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      const neighbor = await f.storage.enqueueIntent(queuedIntent());
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      expect(await f.peer.removeQueuedMessage(original, capture)).toBe(true);
      const storage = await f.reopen();
      expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
      expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
      expect(await storage.getOutbox(neighbor.clientMutationId)).toMatchObject(neighbor);
      expect(await storage.removeQueuedMessage(original, capture)).toBe(false);
    });

    test.each([true, false])("dispatchAndEditHaveOneWinner, dispatch first %s", async (dispatchFirst) => {
      const { storage, peer } = create();
      expect(storage.editQueuedMessage).toBeTypeOf("function");
      const original = await storage.enqueueIntent(queuedIntent());
      const capture = await storage.getOutboxWithStopEpoch(original.clientMutationId);
      const actions = dispatchFirst
        ? [peer.markAttempted(original.clientMutationId), storage.editQueuedMessage(original, capture)]
        : [storage.editQueuedMessage(original, capture), peer.markAttempted(original.clientMutationId)];
      const results = await Promise.all(actions);
      expect(results.filter(Boolean)).toHaveLength(1);
      const outbox = await storage.getOutbox(original.clientMutationId);
      const recovery = await storage.getRecovery(original.clientMutationId);
      expect([outbox, recovery].filter(Boolean)).toHaveLength(1);
      if (outbox) expect(outbox.attempted).toBe(true);
      if (recovery) expect(recovery.recoveryKind).toBe("editing");
    });

    test("retryStopAndEditRejectStaleSnapshots", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      const beforeStop = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      await f.peer.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      expect(await f.storage.editQueuedMessage(original, beforeStop)).toBeUndefined();
      expect(await f.storage.removeQueuedMessage(original, beforeStop)).toBe(false);
      const held = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      if (!held.record) throw new Error("missing held row");
      expect(await f.release(held.record, held)).toBe(true);
      expect(await f.release(held.record, held)).toBe(false);
      expect(await f.storage.editQueuedMessage(held.record, held)).toBeUndefined();
      // The re-queued work is dispatchable under whatever identity the host's
      // release re-queues it as: the same row reopened in place, or the fresh
      // row a host whose explicit Retry mints one carries.
      const requeued = (await f.storage.listOutbox("local:queued")).find(
        (row) => row.method === "turn/queue" && row.state === "submitting",
      );
      if (!requeued) throw new Error("missing requeued row");
      await f.peer.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      const heldAgain = await f.storage.getOutboxWithStopEpoch(requeued.clientMutationId);
      if (!heldAgain.record) throw new Error("missing held row");
      await f.peer.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      expect(await f.storage.editQueuedMessage(heldAgain.record, heldAgain)).toBeUndefined();
      expect(await f.storage.removeQueuedMessage(heldAgain.record, heldAgain)).toBe(false);
      const fresh = await f.storage.getOutboxWithStopEpoch(requeued.clientMutationId);
      if (!fresh.record) throw new Error("missing held row");
      expect(await f.storage.editQueuedMessage(fresh.record, fresh)).toMatchObject({ recoveryKind: "editing" });
      expect(await f.release(fresh.record, fresh)).toBe(false);
    });

    test.each(["targetRef", "instanceId", "threadId", "method", "state", "intentSequence"] as const)(
      "rejects stale %s",
      async (field) => {
        const { storage } = create();
        const original = await storage.enqueueIntent(queuedIntent());
        const stale = { ...original, [field]: field === "intentSequence" ? 500 : "stale" } as MutationOutboxRecord;
        expect(await storage.editQueuedMessage(stale, { stopEpoch: 0 })).toBeUndefined();
        expect(await storage.removeQueuedMessage(stale, { stopEpoch: 0 })).toBe(false);
        expect(await storage.getOutbox(original.clientMutationId)).toMatchObject(original);
      },
    );

    test("resendConsumesEverySourceOrNone", async () => {
      const f = create();
      const first = await f.storage.enqueueIntent(queuedIntent());
      const second = await f.storage.enqueueIntent(queuedIntent());
      for (const record of [first, second]) await f.storage.transferToRecovery(record.clientMutationId, "rejected");
      const ids = [first.clientMutationId, second.clientMutationId];
      const expectedRecoveryRevisions = Object.fromEntries(
        await Promise.all(
          ids.map(async (id) => {
            const record = await f.storage.getRecovery(id);
            if (!record) throw new Error(`missing recovery fixture ${id}`);
            return [id, record.compositionRevision ?? 0] as const;
          }),
        ),
      );
      expect(
        await f.storage.resendRecovery([...ids, "absent"], queuedIntent(), undefined, expectedRecoveryRevisions),
      ).toBeUndefined();
      expect(
        await f.storage.resendRecovery(ids, queuedIntent("local:other"), undefined, expectedRecoveryRevisions),
      ).toBeUndefined();
      expect(await f.storage.resendRecovery([], queuedIntent(), undefined, {})).toBeUndefined();
      for (const id of ids) expect(await f.storage.getRecovery(id)).toBeDefined();
      expect(await f.storage.listOutbox()).toHaveLength(0);
      const successfulResends = (
        await Promise.all([
          f.storage.resendRecovery(ids, queuedIntent(), undefined, expectedRecoveryRevisions),
          f.peer.resendRecovery(ids, queuedIntent(), undefined, expectedRecoveryRevisions),
        ])
      ).filter((record) => record !== undefined);
      expect(successfulResends).toHaveLength(1);
      expect(successfulResends[0]?.clientMutationId).not.toBe(first.clientMutationId);
      expect(successfulResends[0]?.clientMutationId).not.toBe(second.clientMutationId);
      const reopened = await f.reopen();
      for (const id of ids) expect(await reopened.getRecovery(id)).toBeUndefined();
      expect(await reopened.listOutbox()).toHaveLength(1);
      expect(await reopened.resendRecovery(ids, queuedIntent(), undefined, expectedRecoveryRevisions)).toBeUndefined();
    });

    test("newer Stop holds an explicit recovery resend", async () => {
      const { storage, peer } = create();
      const original = await storage.enqueueIntent(queuedIntent());
      await storage.transferToRecovery(original.clientMutationId, "rejected");
      const recovered = await storage.getRecovery(original.clientMutationId);
      if (!recovered) throw new Error("missing recovery fixture");
      const stopEpoch = await storage.readStopEpoch(original.targetRef);
      await peer.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      const resent = await storage.resendRecovery(
        original.clientMutationId,
        queuedIntent(),
        { stopEpoch },
        {
          [original.clientMutationId]: recovered.compositionRevision ?? 0,
        },
      );
      expect(resent).toMatchObject({ state: "canceled", attempted: false });
      expect(await storage.getRecovery(original.clientMutationId)).toBeUndefined();
    });

    test.each([true, false])("edit and Remove have one winner, edit first %s", async (editFirst) => {
      const { storage, peer } = create();
      const original = await storage.enqueueIntent(queuedIntent());
      const capture = await storage.getOutboxWithStopEpoch(original.clientMutationId);
      const results = await Promise.all(
        editFirst
          ? [storage.editQueuedMessage(original, capture), peer.removeQueuedMessage(original, capture)]
          : [peer.removeQueuedMessage(original, capture), storage.editQueuedMessage(original, capture)],
      );
      expect(results.filter(Boolean)).toHaveLength(1);
      expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
      const edited = results.some((result) => typeof result === "object");
      expect(Boolean(await storage.getRecovery(original.clientMutationId))).toBe(edited);
    });

    test.each([true, false])("Retry and Edit have one winner, edit first %s", async (editFirst) => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      if (!capture.record) throw new Error("missing held row");
      const record = capture.record;
      const results = await Promise.all(
        editFirst
          ? [f.storage.editQueuedMessage(record, capture), f.release(record, capture)]
          : [f.release(record, capture), f.storage.editQueuedMessage(record, capture)],
      );
      expect(results.filter(Boolean)).toHaveLength(1);
      const storage = await f.reopen();
      expect(
        [
          await storage.getOutbox(original.clientMutationId),
          await storage.getRecovery(original.clientMutationId),
        ].filter(Boolean),
      ).toHaveLength(1);
    });

    test.each([
      { method: "notes/human/set", input: [], editable: false, removable: false },
      { method: "turn/interrupt", input: [], editable: false, removable: false },
      { method: "turn/promoteQueuedAsSteer", input: [], editable: false, removable: true },
      { method: "turn/drainAsSteer", input: [], editable: false, removable: true },
      { method: "turn/drainAsSteer", input: [{ type: "command", name: "inspect" }], editable: true, removable: true },
      {
        method: "turn/queue",
        input: [{ type: "image", data: "AP8H", mediaType: "image/png" }],
        editable: true,
        removable: true,
      },
    ])("storage policy for $method with $input", async ({ method, input, editable, removable }) => {
      const { storage } = create();
      const original = await storage.enqueueIntent({ ...queuedIntent(), method, payload: { input } });
      const capture = await storage.getOutboxWithStopEpoch(original.clientMutationId);
      const recovery = await storage.editQueuedMessage(original, capture);
      expect(Boolean(recovery)).toBe(editable);
      if (editable) expect(recovery?.payload.input).toEqual(input);
      const neighbor = await storage.enqueueIntent({ ...queuedIntent(), method, payload: { input } });
      expect(await storage.removeQueuedMessage(neighbor, capture)).toBe(removable);
    });
  });
}

// Omit the base capture signature so the web pair shape is the only
// getOutboxWithStopEpoch an intersection would see: intersecting the two
// signatures instead makes them overloads, and the base's narrower return
// would win every call.
type WebStorage = Omit<Storage, "getOutboxWithStopEpoch"> &
  WebQueuedMessageStorage & {
    cancelUnattempted(targetRef: string): Promise<string[]>;
  };

export interface WebQueuedStorageFixture {
  storage: WebStorage;
}

// The web recovery fence's storage surface, as a contract: the capture pair
// a Retry's click reads, and the mint only an explicit user Retry may
// perform. Hosts without the surface run the base describe alone; the web
// runtime runs this one against the same real storage.
export function describeWebQueuedMessageStorage(name: string, create: () => WebQueuedStorageFixture): void {
  describe(`${name} web stop-fence surface`, () => {
    test("capturePairAnswersBothHalves", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      // An ordinary row crosses no displayed ref: the pair's displayed half
      // reads zero, never undefined, so a capture consumer can compare both
      // halves unconditionally.
      expect(capture.stopEpoch).toBe(0);
      expect(capture.displayedStopEpoch).toBe(0);
      await f.storage.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      const stopped = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      expect(stopped.stopEpoch).toBe(1);
      expect(stopped.displayedStopEpoch).toBe(0);
    });

    test("mintRequeuesFreshAndSettlesTheOldRowTerminal", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      // A Stop lands while the attempt's outcome is unknown: the cancel scan
      // skips the attempted row and bumps the ref's epoch past its baseline,
      // so the crossed fence parks it beyond every automatic reopen.
      const interrupt = await f.storage.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      expect((await f.storage.getOutbox(original.clientMutationId))?.state).toBe("blockedUnknown");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      // The explicit Retry outranks the crossed Stop: the mint settles the
      // old row terminal "superseded" - its id and attempt evidence kept, so a
      // receipt from the real in-flight attempt still settles it - and queues
      // the work as a FRESH row that inherits the predecessor's wire id.
      const minted = await f.storage.mintRetryFromRow(original.clientMutationId, capture);
      expect(minted?.state).toBe("submitting");
      expect(minted?.clientMutationId).not.toBe(original.clientMutationId);
      expect(minted?.payload.clientMutationId).toBe(original.clientMutationId);
      expect(await f.storage.getOutbox(original.clientMutationId)).toMatchObject({
        state: "superseded",
        attempted: true,
      });
      // The Stop's own interrupt row still heads the queue ahead of the tail
      // mint; retire it the way its dispatch would, then the old row is never
      // dispatched again: only the fresh row is dispatchable.
      expect(await f.storage.settleApplied(interrupt.clientMutationId)).toMatchObject({
        clientMutationId: interrupt.clientMutationId,
      });
      expect(await f.storage.nextDispatchable("local:queued")).toMatchObject({
        clientMutationId: minted?.clientMutationId,
      });
      // The capture's identity is the mint's one check: a row whose durable
      // world no longer matches it refuses.
      expect(await f.storage.mintRetryFromRow(original.clientMutationId, capture)).toBeUndefined();
    });

    // The reconcile's re-queue is a mint too - reconciliation NEVER re-enables
    // a parked row: an innocent blocked row settles terminal "superseded"
    // (its id and attempt evidence kept for the receipt its real in-flight
    // attempt can still bring) and a FRESH row carries the work, inheriting
    // the predecessor's wire id, queued at the ref's tail, fenced at birth by
    // construction - barrier-free like §4's recovery resends. The returned id
    // is the fresh row's: the row that dispatches.
    test("aProvenAbsentRequeueMintsAFreshRowAndSettlesThePredecessor", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      const restored = await f.storage.restoreProvenAbsent("local:queued", new Set());
      expect(restored).toHaveLength(1);
      expect(await f.storage.getOutbox(original.clientMutationId)).toMatchObject({
        state: "superseded",
        attempted: true,
      });
      expect(restored[0]?.clientMutationId).not.toBe(original.clientMutationId);
      const fresh = await f.storage.getOutbox(restored[0]?.clientMutationId ?? "");
      expect(fresh).toMatchObject({ state: "submitting", attempted: false });
      expect(fresh?.payload.clientMutationId).toBe(original.clientMutationId);
      expect(fresh?.stopEpochAtEnqueue).toBe(0);
    });

    // RoboRev round-1 H1: repeated retries must keep the ORIGINAL wire id.
    // A replacement minted from a blockedUnknown row carries its wire id, a
    // Stop can commit that replacement born-canceled, and a later Retry of
    // THAT row mints a second generation. The inheritance decision keys on
    // delivery-uncertain ancestry - the payload id IS the uncertainty
    // marker - not on the immediate predecessor's own state, or the second
    // generation discards the inherited id and the daemon executes the
    // composition again under a new one.
    test("aMintFromAMintedRowKeepsTheOriginalWireId", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      // A Stop crosses inside the first capture-to-mint window: the first
      // replacement commits born-canceled, still carrying the original id.
      await f.storage.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      const first = await f.storage.mintRetryFromRow(original.clientMutationId, capture);
      if (!first) throw new Error("first mint refused");
      expect(first.state).toBe("canceled");
      expect(first.payload.clientMutationId).toBe(original.clientMutationId);
      // A Retry of the born-canceled replacement mints a second generation:
      // the original's work is still delivery-uncertain, so the second
      // generation carries the SAME wire id.
      const recapture = await f.storage.getOutboxWithStopEpoch(first.clientMutationId);
      const second = await f.storage.mintRetryFromRow(first.clientMutationId, recapture);
      expect(second?.state).toBe("submitting");
      expect(second?.payload.clientMutationId).toBe(original.clientMutationId);
    });

    // RoboRev round-1 H1, the reconcile side: an innocent blocked replacement
    // mints its own successor, and the successor inherits the ORIGINAL wire
    // id - the effective wire identity travels, never the immediate
    // predecessor's storage key.
    test("aReconcileMintFromAMintedRowKeepsTheOriginalWireId", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      const first = await f.storage.mintRetryFromRow(original.clientMutationId, capture);
      if (!first) throw new Error("first mint refused");
      // The replacement parks delivery-uncertain; reconciliation proves it
      // absent and re-queues it as a second minted generation.
      await f.storage.markAttempted(first.clientMutationId);
      await f.storage.markUnknown(first.clientMutationId, "blockedUnknown");
      const restored = await f.storage.restoreProvenAbsent("local:queued", new Set());
      expect(restored).toHaveLength(1);
      const second = await f.storage.getOutbox(restored[0]?.clientMutationId ?? "");
      expect(second?.payload.clientMutationId).toBe(original.clientMutationId);
    });

    // RoboRev round-2 H1: a note save is per-session state, not FIFO work,
    // and a reconcile mint queues at the ref's TAIL - minting an OLDER
    // blocked note save behind a newer queued save would dispatch the stale
    // text AFTER the newer note and overwrite it. The older row settles
    // superseded WITHOUT minting when a newer save for the same note is
    // already queued or waiting.
    test("a reconcile of an older blocked note save never re-queues it behind a newer save", async () => {
      const f = create();
      const noteIntent = (note: string) => ({
        ...queuedIntent(),
        method: "notes/human/set",
        payload: { ref: "local:queued", note },
        optimisticDisplay: null,
      });
      const older = await f.storage.enqueueIntent(noteIntent("older text"));
      const newer = await f.storage.enqueueIntent(noteIntent("newer text"));
      await f.storage.markAttempted(older.clientMutationId);
      await f.storage.markUnknown(older.clientMutationId, "blockedUnknown");
      const restored = await f.storage.restoreProvenAbsent("local:queued", new Set());
      expect(restored).toHaveLength(0);
      const rows = await f.storage.listOutbox("local:queued");
      expect(rows).toHaveLength(2);
      expect(rows.find((row) => row.clientMutationId === older.clientMutationId)).toMatchObject({
        state: "superseded",
      });
      expect(rows.find((row) => row.clientMutationId === newer.clientMutationId)).toMatchObject({
        state: "submitting",
      });
      // The newer note is the only text that can ever dispatch for the note.
      const dispatchableNotes = rows.filter((row) => row.state === "submitting");
      expect(dispatchableNotes).toHaveLength(1);
      expect(dispatchableNotes[0]?.payload.note).toBe("newer text");
    });

    // RoboRev round-1 M3, interleaving one: the replacement earned an
    // accepted-but-pending receipt and lives in the optimistic store under
    // its own key while the authoritative feed names the WIRE id. The feed
    // must reach the replacement and retire the settled pair.
    test("aFeedNamingTheWireIdSettlesTheReplacementAndRetiresThePredecessor", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      const fresh = await f.storage.mintRetryFromRow(original.clientMutationId, capture);
      if (!fresh) throw new Error("mint refused");
      expect(await f.storage.settleReceipt(fresh.clientMutationId, "pending")).toBe(true);
      expect(await f.storage.getOptimistic(fresh.clientMutationId)).toBeDefined();
      expect(await f.storage.settleApplied(original.clientMutationId)).toMatchObject({
        clientMutationId: fresh.clientMutationId,
      });
      expect(await f.storage.getOptimistic(fresh.clientMutationId)).toBeUndefined();
      expect(await f.storage.getOutbox(original.clientMutationId)).toBeUndefined();
    });

    // RoboRev round-1 M3, interleaving two: a reflected replay settles the
    // replacement, and the predecessor's outcome is proven by the same wire
    // id - it must not linger superseded, retained and pinning the ref, once
    // the replacement is reflected.
    test("aReflectedReplacementSettlesItsSupersededPredecessorToo", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      const fresh = await f.storage.mintRetryFromRow(original.clientMutationId, capture);
      if (!fresh) throw new Error("mint refused");
      expect(await f.storage.settleReceipt(fresh.clientMutationId, "reflected")).toBe(true);
      expect(await f.storage.getOutbox(original.clientMutationId)).toBeUndefined();
    });

    // RoboRev round-2 M3 corner: a composition can earn REPEATED pending
    // receipts - two tabs both settling the same accepted-but-unreflected
    // mutation. The retained accepted copy is the input's only durable
    // pending representation once the outbox row is gone, so the second
    // settlement must leave it in place: the optimistic delete is conditional
    // on the settlement NOT retaining, never an unconditional delete of
    // whatever the receipt names.
    test("repeatedPendingReceiptsKeepTheRetainedAcceptedCopy", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      expect(await f.storage.settleReceipt(original.clientMutationId, "pending")).toBe(true);
      expect(await f.storage.getOptimistic(original.clientMutationId)).toBeDefined();
      // The second tab's pending receipt for the same mutation.
      expect(await f.storage.settleReceipt(original.clientMutationId, "pending")).toBe(true);
      expect(await f.storage.getOptimistic(original.clientMutationId)).toBeDefined();
      expect(await f.storage.getOutbox(original.clientMutationId)).toBeUndefined();
    });

    // The settle-time retirement ruling (the disclosed settled-newer
    // boundary, now closed): when a notes/human/set row settles, OLDER
    // blockedUnknown note rows on the same target retire to terminal
    // superseded - state change only, keeping id and attempt evidence
    // exactly like the mint's own superseded semantics. By the time a
    // reconciliation could mint the older save, it is already terminal, so
    // the newer note's text can never be overwritten by the stale dispatch;
    // and the older row's own late receipt still settles it.
    test("aSettledNewerNoteSaveRetiresOlderBlockedNoteRows", async () => {
      const f = create();
      const noteIntent = (note: string) => ({
        ...queuedIntent(),
        method: "notes/human/set",
        payload: { ref: "local:queued", note },
        optimisticDisplay: null,
      });
      const older = await f.storage.enqueueIntent(noteIntent("older text"));
      const newer = await f.storage.enqueueIntent(noteIntent("newer text"));
      await f.storage.markAttempted(older.clientMutationId);
      await f.storage.markUnknown(older.clientMutationId, "blockedUnknown");
      // The newer save dispatches and settles.
      expect(await f.storage.settleReceipt(newer.clientMutationId, "reflected")).toBe(true);
      expect(await f.storage.getOutbox(older.clientMutationId)).toMatchObject({
        state: "superseded",
        attempted: true,
      });
      // Reconciliation finds no innocent work to re-queue: the older row is
      // terminal, so the newer note's text survives untouched.
      expect(await f.storage.restoreProvenAbsent("local:queued", new Set())).toHaveLength(0);
      const rows = await f.storage.listOutbox("local:queued");
      expect(rows.filter((row) => row.method === "notes/human/set" && row.state === "submitting")).toHaveLength(0);
      // The older row's own late receipt still settles it.
      expect(await f.storage.settleReceipt(older.clientMutationId, "reflected")).toBe(true);
      expect(await f.storage.getOutbox(older.clientMutationId)).toBeUndefined();
    });

    // Whole-endpoint review M3: terminal refusals can arrive for BOTH
    // halves of a superseded wire pair - the original (key A) and its minted
    // replacement (key B carrying the inherited wire id A). Transfers keyed
    // on the storage key would leave two independent sendable copies of the
    // SAME wire mutation in recovery, and resending both assigns independent
    // wire ids - the composition could submit twice. A terminal transfer
    // whose effective wire id already has a live recovery copy coalesces:
    // no second copy is created, and nothing here ever replaces a recovery
    // record, so an independently edited composition keeps the
    // compositionRevision guard's existing protection.
    test("terminalRefusalsOfAWirePairCoalesceIntoOneRecoveryCopy", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      const fresh = await f.storage.mintRetryFromRow(original.clientMutationId, capture);
      if (!fresh) throw new Error("mint refused");
      expect(await f.storage.transferToRecovery(original.clientMutationId, "rejected", "refused")).toBeDefined();
      // The replacement's own later terminal refusal coalesces into the
      // original's copy: no second sendable recovery row exists, and the
      // refused row leaves the outbox either way.
      const coalesced = await f.storage.transferToRecovery(fresh.clientMutationId, "rejected", "refused again");
      expect(coalesced?.clientMutationId).toBe(original.clientMutationId);
      expect(await f.storage.getRecovery(fresh.clientMutationId)).toBeUndefined();
      expect(await f.storage.getOutbox(fresh.clientMutationId)).toBeUndefined();
      expect(await f.storage.getRecovery(original.clientMutationId)).toMatchObject({
        recoveryKind: "rejected",
      });
    });

    // The same coalesce in the other order: the replacement's refusal lands
    // first, and the original's own late refusal must not spawn a second
    // sendable copy either.
    test("terminalRefusalsOfAWirePairCoalesceInEitherOrder", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      const fresh = await f.storage.mintRetryFromRow(original.clientMutationId, capture);
      if (!fresh) throw new Error("mint refused");
      expect(await f.storage.transferToRecovery(fresh.clientMutationId, "rejected", "refused")).toBeDefined();
      const coalesced = await f.storage.transferToRecovery(original.clientMutationId, "rejected", "refused late");
      expect(coalesced?.clientMutationId).toBe(fresh.clientMutationId);
      expect(await f.storage.getRecovery(original.clientMutationId)).toBeUndefined();
      expect(await f.storage.getOutbox(original.clientMutationId)).toBeUndefined();
      expect(await f.storage.getRecovery(fresh.clientMutationId)).toMatchObject({
        recoveryKind: "rejected",
      });
    });

    test("aMintCrossedByANewerStopCommitsBornCanceled", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.cancelUnattempted("local:queued");
      expect((await f.storage.getOutbox(original.clientMutationId))?.state).toBe("canceled");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      // A Stop lands inside the capture-to-mint window: its scan skips the
      // already-canceled row, so the mint's birth compare is the only thing
      // that can catch the bump - and it does, by never absorbing it: the
      // fresh row commits born-canceled and the old canceled row is removed.
      const interrupt = await f.storage.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      const crossed = await f.storage.mintRetryFromRow(original.clientMutationId, capture);
      expect(crossed?.state).toBe("canceled");
      expect(await f.storage.getOutbox(original.clientMutationId)).toBeUndefined();
      // Retire the Stop's own interrupt row the way its dispatch would; with
      // it gone nothing on the ref is dispatchable - the fresh row was born
      // canceled and the old canceled row is removed.
      expect(await f.storage.settleApplied(interrupt.clientMutationId)).toMatchObject({
        clientMutationId: interrupt.clientMutationId,
      });
      expect(await f.storage.nextDispatchable("local:queued")).toBeUndefined();
    });
  });
}
