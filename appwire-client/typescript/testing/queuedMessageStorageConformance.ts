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
      expect(await f.storage.getOutbox(original.clientMutationId)).toMatchObject({
        state: "submitting",
        clientMutationId: original.clientMutationId,
      });
      await f.peer.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      const heldAgain = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      if (!heldAgain.record) throw new Error("missing held row");
      await f.peer.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      expect(await f.storage.editQueuedMessage(heldAgain.record, heldAgain)).toBeUndefined();
      expect(await f.storage.removeQueuedMessage(heldAgain.record, heldAgain)).toBe(false);
      const fresh = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
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
type WebStorage = Omit<Storage, "getOutboxWithStopEpoch"> & WebQueuedMessageStorage;

export interface WebQueuedStorageFixture {
  storage: WebStorage;
}

// The web recovery fence's storage surface, as a contract: the capture pair a
// reauthorizing click reads, and the blockedUnknown release only an explicit
// user Retry may perform. Hosts without the surface run the base describe
// alone; the web runtime runs this one against the same real storage.
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

    test("reauthorizeBlockedAnswersTheCapture", async () => {
      const f = create();
      const original = await f.storage.enqueueIntent(queuedIntent());
      await f.storage.markAttempted(original.clientMutationId);
      await f.storage.markUnknown(original.clientMutationId, "blockedUnknown");
      // A Stop lands while the attempt's outcome is unknown: the cancel scan
      // skips the attempted row and bumps the ref's epoch past its baseline,
      // so the crossed fence parks it beyond every automatic reopen.
      await f.storage.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      expect((await f.storage.getOutbox(original.clientMutationId))?.state).toBe("blockedUnknown");
      const capture = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      // A second Stop outranks the earlier capture: the reauthorization
      // refuses and the row stays parked.
      await f.storage.enqueueInterruptAndCancel({ ...queuedIntent(), method: "turn/interrupt" });
      expect(
        await f.storage.reauthorizeBlockedMutation(original.clientMutationId, {
          stopEpoch: capture.stopEpoch,
          displayedStopEpoch: capture.displayedStopEpoch,
        }),
      ).toBe(false);
      expect((await f.storage.getOutbox(original.clientMutationId))?.state).toBe("blockedUnknown");
      // The post-Stop capture is the explicit Retry outranking the crossed
      // Stop: the row reopens under its ORIGINAL mutation ID with its attempt
      // evidence preserved.
      const fresh = await f.storage.getOutboxWithStopEpoch(original.clientMutationId);
      expect(
        await f.storage.reauthorizeBlockedMutation(original.clientMutationId, {
          stopEpoch: fresh.stopEpoch,
          displayedStopEpoch: fresh.displayedStopEpoch,
        }),
      ).toBe(true);
      expect(await f.storage.getOutbox(original.clientMutationId)).toMatchObject({
        state: "submitting",
        clientMutationId: original.clientMutationId,
        attempted: true,
      });
    });
  });
}
