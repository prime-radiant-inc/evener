// A package-level port-conformance suite for MutationOutboxStorage, run by
// each host adapter (the web IndexedDB adapter, the native expo-sqlite
// adapter) with its own factory. It lives under the package's test-helper
// namespace - `@evener/appwire-client/testing/...`, the path both apps already
// alias for in-repo test support - so a production source file cannot import
// it through the shipped `state/mutation` subpath and pull the vitest runtime
// import below into a bundle unnoticed.
//
// Before this, each adapter hand-transcribed the other's storage contracts
// with "Oracle:" comments citing line numbers, so a contract change could
// drift between hosts. Now one set of contracts runs against both.

import { beforeEach, describe, expect, test } from "vitest";
import type { MutationOutboxStorage } from "../state/mutation/outbox";
import type { MutationAttachmentRef, MutationIntent } from "../state/mutation/records";

// The storage options a conformance run varies: the id generator, the clock,
// and the identity each host reads fresh at enqueue time (the web's
// sessionStorage singleton, the native adapter's getOwnClientId option).
export interface MutationOutboxStorageFactoryOptions {
  createMutationId?: () => string;
  now?: () => number;
  getOwnClientId?: () => string | undefined;
}

// One host's storage under conformance. Each adapter supplies a factory, and
// the SAME contracts below run against both.
//
// createStorage must return a storage over empty persistence: the suite calls
// it once per test and never shares an instance across tests.
export interface MutationOutboxStorageFactory {
  // Human-readable host name, used as the describe block's label.
  readonly name: string;
  createStorage(options?: MutationOutboxStorageFactoryOptions): MutationOutboxStorage | Promise<MutationOutboxStorage>;
  // Rewrites one already-marked record into the pre-unresolvedAttempts
  // legacy shape: the bare attempted boolean with no count, exactly as the
  // pre-field adapter wrote it. The legacy contracts below pin that both
  // adapters read that shape as ONE unresolved attempt, not zero. Only a
  // host whose persistence can hold the pre-field shape supplies this.
  stripAttemptCount?(storage: MutationOutboxStorage, clientMutationId: string): Promise<void> | void;
  // Rewrites one record into the pre-capture legacy shape: the
  // stopEpochAtEnqueue field absent (browser) or zeroed (native, whose
  // migration column reads DEFAULT 0), exactly as a build from before the
  // field existed persisted it - untouched by every later schema version.
  // Only a host whose persistence can hold the pre-field shape supplies
  // this.
  stripCaptureAtEnqueue?(storage: MutationOutboxStorage, clientMutationId: string): Promise<void> | void;
}

const CONFORMANCE_TARGET = "local:conformance-1";
const CONFORMANCE_OTHER_TARGET = "local:conformance-2";

function conformanceTextIntent(text: string, targetRef = CONFORMANCE_TARGET): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "turn/queue",
    payload: { ref: targetRef, input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { text },
  };
}

function conformanceInterruptIntent(targetRef = CONFORMANCE_TARGET): MutationIntent {
  return {
    targetRef,
    method: "turn/interrupt",
    payload: { ref: targetRef },
    attachments: [],
    optimisticDisplay: { method: "turn/interrupt" },
  };
}

// A production-shaped human note: notes/human/set carries no optimistic display
// (the composer stages the text itself), so nothing renders while it waits on
// its canonical reflection.
function conformanceNoteIntent(note: string, targetRef = CONFORMANCE_TARGET): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "notes/human/set",
    payload: { ref: targetRef, expectedInstanceId: "instance-1", note },
    attachments: [],
    optimisticDisplay: null,
  };
}

// The fixture the note-supersede contracts share: the ref's earlier refused and
// orphaned note recovery rows (a later save supersedes both), a settling note,
// a newer note, a recovery row of another method on the same ref, and a note
// recovery row on another ref. The other-method row is seeded BEFORE the
// settling note, so its lower sequence means its survival proves the method
// filter rather than recency; the other-target row proves the target filter the
// same way.
async function seedSupersededNoteScenario(storage: MutationOutboxStorage) {
  const refusedOlder = await storage.enqueueIntent(conformanceNoteIntent("refused older"));
  await storage.transferToRecovery(refusedOlder.clientMutationId, "rejected", "note refused");
  const orphanedOlder = await storage.enqueueIntent(conformanceNoteIntent("orphaned older"));
  await storage.transferToRecovery(orphanedOlder.clientMutationId, "orphaned");
  const otherMethod = await storage.enqueueIntent(conformanceTextIntent("a turn", CONFORMANCE_TARGET));
  await storage.transferToRecovery(otherMethod.clientMutationId, "rejected", "turn refused");
  const settling = await storage.enqueueIntent(conformanceNoteIntent("settling now"));
  const newer = await storage.enqueueIntent(conformanceNoteIntent("newer still"));
  await storage.transferToRecovery(newer.clientMutationId, "rejected", "note refused");
  const otherTarget = await storage.enqueueIntent(conformanceNoteIntent("elsewhere", CONFORMANCE_OTHER_TARGET));
  await storage.transferToRecovery(otherTarget.clientMutationId, "orphaned");
  return { refusedOlder, orphanedOlder, settling, newer, otherMethod, otherTarget };
}

// The supersede is not a "refused"-only rule: the web oracle discards every
// earlier notes/human/set recovery row for the ref whatever its kind, so an
// older orphaned note goes too. The other method, the other ref, and the newer
// note must all survive.
async function expectSupersedeDroppedEarlierNotes(
  storage: MutationOutboxStorage,
  ids: Awaited<ReturnType<typeof seedSupersededNoteScenario>>,
) {
  await expect(storage.getRecovery(ids.refusedOlder.clientMutationId)).resolves.toBeUndefined();
  await expect(storage.getRecovery(ids.orphanedOlder.clientMutationId)).resolves.toBeUndefined();
  await expect(storage.getRecovery(ids.newer.clientMutationId)).resolves.toMatchObject({
    clientMutationId: ids.newer.clientMutationId,
  });
  await expect(storage.getRecovery(ids.otherMethod.clientMutationId)).resolves.toMatchObject({
    clientMutationId: ids.otherMethod.clientMutationId,
  });
  await expect(storage.getRecovery(ids.otherTarget.clientMutationId)).resolves.toMatchObject({
    clientMutationId: ids.otherTarget.clientMutationId,
  });
}

// The MutationOutboxStorage port's behavioral contracts, run against any
// host's factory. Every assertion goes through the port's public methods:
// nothing here reads a host's raw rows, so the suite is the one set of
// contracts both adapters must satisfy rather than a second transcription of
// one of them.
//
// Tracked divergence, deliberately not asserted here (#1927, measured at
// #1965 ae2d1944e): an explicit `optimisticDisplay: null` round-trips as null
// on the web adapter but normalizes to undefined on the native adapter (an
// absent display and an explicit null are both serialized as JSON null and
// read back as undefined). Existing native consumers treat the two
// equivalently, so no contract here pins either spelling until a host
// actually depends on the difference.
export function describeMutationOutboxStorage(factory: MutationOutboxStorageFactory): void {
  describe(`MutationOutboxStorage conformance (${factory.name})`, () => {
    let storage: MutationOutboxStorage;
    let nextId = 0;
    const idSequence = () => `conformance-${++nextId}`;

    beforeEach(async () => {
      nextId = 0;
      storage = await factory.createStorage({ createMutationId: idSequence, now: () => 1234 });
    });

    // The central durable-intent contract: the full intent survives, and the
    // generated clientMutationId rides the payload the daemon correlates on.
    // Asserted both on the enqueue's own return value and on the record read
    // back through getOutbox, so an adapter that returned a complete record but
    // persisted a partial one fails.
    test("enqueueIntent persists the full intent with the generated id on its payload", async () => {
      const expected = {
        version: 1,
        clientMutationId: "conformance-1",
        targetRef: CONFORMANCE_TARGET,
        threadId: "thread-1",
        intentSequence: 1,
        createdAt: 1234,
        method: "turn/queue",
        payload: {
          ref: CONFORMANCE_TARGET,
          input: [{ type: "text", text: "survive reload" }],
          clientMutationId: "conformance-1",
        },
        attachments: [],
        optimisticDisplay: { text: "survive reload" },
        state: "submitting",
        attempted: false,
      };
      const record = await storage.enqueueIntent(conformanceTextIntent("survive reload"));
      expect(record).toMatchObject(expected);
      await expect(storage.getOutbox("conformance-1")).resolves.toMatchObject(expected);
    });

    // #1927 Low (raw GLM review of #1916): a nonempty attachment, composerText
    // and an injected getOwnClientId must all round-trip through persistence.
    test("enqueueIntent persists attachments, composerText, and the injected submitting identity", async () => {
      const identified = await factory.createStorage({
        createMutationId: idSequence,
        now: () => 1234,
        getOwnClientId: () => "client-one",
      });
      const attachment: MutationAttachmentRef = {
        presentationId: "presentation-1",
        marker: 1,
        name: "photo.png",
        mediaType: "image/png",
      };
      const record = await identified.enqueueIntent({
        ...conformanceTextIntent("with attachment"),
        attachments: [attachment],
        composerText: "with attachment [image 1]",
      });
      expect(record).toMatchObject({
        attachments: [attachment],
        composerText: "with attachment [image 1]",
        originClientId: "client-one",
      });
      await expect(identified.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        attachments: [attachment],
        composerText: "with attachment [image 1]",
        originClientId: "client-one",
      });
    });

    test("enqueueIntent rejects an empty or whitespace targetRef", async () => {
      for (const targetRef of ["", "   "]) {
        await expect(storage.enqueueIntent(conformanceTextIntent("no target", targetRef))).rejects.toThrow(
          "targetRef is required",
        );
      }
    });

    test("intentSequence is gap-free and per target ref", async () => {
      const first = await storage.enqueueIntent(conformanceTextIntent("first", CONFORMANCE_TARGET));
      const second = await storage.enqueueIntent(conformanceTextIntent("second", CONFORMANCE_TARGET));
      const other = await storage.enqueueIntent(conformanceTextIntent("other", CONFORMANCE_OTHER_TARGET));
      expect([first.intentSequence, second.intentSequence]).toEqual([1, 2]);
      expect(other.intentSequence).toBe(1);
    });

    test("markAttempted flips a submitting record's flag and refuses a non-submitting one", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("attempt me"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({ attempted: true });
      await expect(storage.markAttempted("missing")).resolves.toBe(false);
      await storage.markUnknown(record.clientMutationId, "blockedUnknown");
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(false);
    });

    // #4342: after the hub authoritatively refused an attempt in band (a
    // connectionBackpressure refusal), the dispatcher rolls back exactly
    // that attempt's evidence. The row returns to its never-attempted shape
    // — so each host's reconnect reconcile onlyAttempted-guarded
    // blockedUnknown park skips it and the ordinary flush rediscovers it —
    // only when the refused attempt was the row's one unresolved attempt;
    // evidence of an earlier or concurrent unresolved attempt survives, and
    // a row parked while its only attempt was in flight reopens, because
    // that refusal is authoritative proof the park's uncertainty resolves.
    // Authoritative outcomes own every other state, so only submitting and
    // blockedUnknown rows roll back.
    test("unmarkAttempted returns a once-attempted row to its never-attempted shape", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("refused in band"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: false,
        unresolvedAttempts: 0,
      });
    });

    test("unmarkAttempted reopens a row parked while its only attempt was refused in band", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("parked mid-attempt"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await storage.markUnknown(record.clientMutationId, "blockedUnknown");
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: false,
        unresolvedAttempts: 0,
      });
    });

    // #4342 (review): a Stop whose cancel scan skipped the attempted row
    // bumped the ref's stop epoch while the attempt was in flight. The
    // refusal then proves the attempt never landed, but the row must not
    // return dispatchable ahead of the interrupt the Stop already queued:
    // the rollback holds it canceled, released only by an explicit Retry.
    test("unmarkAttempted holds a row canceled when a Stop interrupted the refused attempt", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("stopped mid-attempt"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(record.targetRef));
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(false);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "canceled",
      });
    });

    test("unmarkAttempted holds a parked row canceled when a Stop interrupted the refused attempt", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("parked, then stopped"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await storage.markUnknown(record.clientMutationId, "blockedUnknown");
      await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(record.targetRef));
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(false);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "canceled",
      });
    });

    // #4342 (round-5 review): the Stop's own interrupt record passes no
    // barrier - it IS the click the epoch records - so its capture is the
    // epoch its Stop just bumped. A backpressure refusal of the interrupt
    // itself must redeliver like any transient refusal, not fence-cancel the
    // Stop's own row and leave the agent running.
    test("the interrupt's own refused attempt redelivers rather than fence-cancels", async () => {
      const interrupt = await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(CONFORMANCE_TARGET));
      await expect(storage.markAttempted(interrupt.clientMutationId)).resolves.toBe(true);
      await expect(storage.unmarkAttempted(interrupt.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(interrupt.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: false,
      });
    });

    // #4342 (round-8 review): a record from before the capture field
    // existed reaches this build with none - on native the migration
    // column reads DEFAULT 0. Its first mark proves by survival that no
    // Stop's cancel scan ran since its birth (any bump would have
    // canceled it while unattempted), so the epoch as it stands IS the
    // epoch it was born at: the mark initializes the capture, and a
    // historical Stop predating the row does not fence-cancel its first
    // refusal.
    test("a pre-capture row's first refusal after a historical Stop stays transient", async () => {
      if (!factory.stripCaptureAtEnqueue) return;
      await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(CONFORMANCE_TARGET));
      const record = await storage.enqueueIntent(conformanceTextIntent("born after the stop"));
      await factory.stripCaptureAtEnqueue(storage, record.clientMutationId);
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: false,
      });
    });

    // The same seam through the interrupt itself: a pre-capture pending
    // interrupt refuses transiently instead of fence-canceling its own
    // Stop - the round-5 interrupt rule, held for the legacy shape too.
    test("a pre-capture pending interrupt's refused attempt redelivers", async () => {
      if (!factory.stripCaptureAtEnqueue) return;
      const interrupt = await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(CONFORMANCE_TARGET));
      await factory.stripCaptureAtEnqueue(storage, interrupt.clientMutationId);
      await expect(storage.markAttempted(interrupt.clientMutationId)).resolves.toBe(true);
      await expect(storage.unmarkAttempted(interrupt.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(interrupt.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
      });
    });

    test("markAttempted counts concurrent unresolved attempts", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("two tabs, one row"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        attempted: true,
        unresolvedAttempts: 2,
      });
    });

    test("unmarkAttempted rolls back only the refused attempt, keeping earlier evidence", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("earlier attempt unresolved"));
      // An earlier attempt whose response was lost: submitting, evidence
      // retained (a transport failure leaves exactly this shape).
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      // The re-dispatch marks again, then its attempt is refused in band.
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(false);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: true,
        unresolvedAttempts: 1,
      });
      // The earlier attempt's own refusal resolves the last evidence.
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: false,
        unresolvedAttempts: 0,
      });
    });

    test("unmarkAttempted keeps a parked row blocked while earlier evidence remains", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("parked with earlier evidence"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await storage.markUnknown(record.clientMutationId, "blockedUnknown");
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(false);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "blockedUnknown",
        attempted: true,
        unresolvedAttempts: 1,
      });
      await expect(storage.unmarkAttempted("missing")).resolves.toBe(false);
    });

    // #4342 (round-2 review): records written before the unresolvedAttempts
    // field existed carry their evidence as the bare attempted boolean. A
    // refused retry must not erase that legacy evidence: the count reads
    // the boolean as one unresolved attempt, not zero.
    test("a refused retry preserves a legacy record's pre-count attempt evidence", async () => {
      if (!factory.stripAttemptCount) return;
      const record = await storage.enqueueIntent(conformanceTextIntent("legacy pre-count evidence"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await factory.stripAttemptCount(storage, record.clientMutationId);
      // The retry's own mark lands on the legacy evidence, not over it.
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.unmarkAttempted(record.clientMutationId)).resolves.toBe(false);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: true,
        unresolvedAttempts: 1,
      });
    });

    test("markUnknown's onlyAttempted guard refuses an un-attempted record", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("unknown outcome"));
      await expect(
        storage.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true }),
      ).resolves.toBe(false);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({ state: "submitting" });
      await storage.markAttempted(record.clientMutationId);
      await expect(
        storage.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true }),
      ).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({ state: "blockedUnknown" });
    });

    // #1927 Low (raw GLM review of #1916): an intent attempted before a
    // recovery-to-optimistic settlement must not carry its attempt evidence
    // into the accepted optimistic record.
    test("markAttempted before settlement resets attempted in the accepted optimistic record", async () => {
      const record = await storage.enqueueIntent({
        ...conformanceTextIntent("attempted before receipt"),
        optimisticDisplay: { input: [{ type: "text", text: "attempted before receipt" }] },
      });
      await storage.markAttempted(record.clientMutationId);
      await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toBeUndefined();
      const accepted = await storage.getOptimistic(record.clientMutationId);
      expect(accepted).toMatchObject({ state: "accepted" });
      expect(accepted).not.toHaveProperty("attempted");
    });

    test("nextDispatchable is blocked by an earlier blockedUnknown on the same target only", async () => {
      const first = await storage.enqueueIntent(conformanceTextIntent("first", CONFORMANCE_TARGET));
      const second = await storage.enqueueIntent(conformanceTextIntent("second", CONFORMANCE_TARGET));
      const other = await storage.enqueueIntent(conformanceTextIntent("other", CONFORMANCE_OTHER_TARGET));
      await storage.markAttempted(first.clientMutationId);
      await storage.markUnknown(first.clientMutationId, "blockedUnknown");

      await expect(storage.nextDispatchable(CONFORMANCE_TARGET)).resolves.toBeUndefined();
      await expect(storage.nextDispatchable(CONFORMANCE_OTHER_TARGET)).resolves.toMatchObject({
        clientMutationId: other.clientMutationId,
      });
      await storage.settleApplied(first.clientMutationId);
      await expect(storage.nextDispatchable(CONFORMANCE_TARGET)).resolves.toMatchObject({
        clientMutationId: second.clientMutationId,
      });
    });

    // #1927 Low (raw #1917 panel): restoreProvenAbsent's target scoping is
    // distinct from authoritative-id membership. A blockedUnknown row on
    // another target is also absent from authoritativeIds, and restoring the
    // selected target must leave it blocked.
    test("restoreProvenAbsent reopens only the selected target's omitted records", async () => {
      const omitted = await storage.enqueueIntent(conformanceTextIntent("omitted", CONFORMANCE_TARGET));
      const named = await storage.enqueueIntent(conformanceTextIntent("named", CONFORMANCE_TARGET));
      const blockedElsewhere = await storage.enqueueIntent(
        conformanceTextIntent("blocked elsewhere", CONFORMANCE_OTHER_TARGET),
      );
      await storage.markUnknown(omitted.clientMutationId, "blockedUnknown");
      await storage.markUnknown(named.clientMutationId, "blockedUnknown");
      await storage.markUnknown(blockedElsewhere.clientMutationId, "blockedUnknown");

      await expect(storage.restoreProvenAbsent(CONFORMANCE_TARGET, new Set([named.clientMutationId]))).resolves.toEqual(
        [omitted.clientMutationId],
      );
      await expect(storage.getOutbox(omitted.clientMutationId)).resolves.toMatchObject({ state: "submitting" });
      await expect(storage.getOutbox(named.clientMutationId)).resolves.toMatchObject({ state: "blockedUnknown" });
      await expect(storage.getOutbox(blockedElsewhere.clientMutationId)).resolves.toMatchObject({
        state: "blockedUnknown",
      });
    });

    test("enqueueInterruptAndCancel cancels non-attempted rows and commits the interrupt with them", async () => {
      const queued = await storage.enqueueIntent(conformanceTextIntent("queued", CONFORMANCE_TARGET));
      const inFlight = await storage.enqueueIntent(conformanceTextIntent("in flight", CONFORMANCE_TARGET));
      await storage.markAttempted(inFlight.clientMutationId);
      const other = await storage.enqueueIntent(conformanceTextIntent("other ref", CONFORMANCE_OTHER_TARGET));

      const interrupt = await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(CONFORMANCE_TARGET));
      expect(interrupt).toMatchObject({ method: "turn/interrupt", state: "submitting", attempted: false });
      await expect(storage.getOutbox(queued.clientMutationId)).resolves.toMatchObject({ state: "canceled" });
      await expect(storage.getOutbox(inFlight.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: true,
      });
      await expect(storage.getOutbox(other.clientMutationId)).resolves.toMatchObject({ state: "submitting" });
    });

    test("a canceled record cannot be marked attempted or reclassified", async () => {
      const queued = await storage.enqueueIntent(conformanceTextIntent("queued", CONFORMANCE_TARGET));
      await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(CONFORMANCE_TARGET));
      await expect(storage.markAttempted(queued.clientMutationId)).resolves.toBe(false);
      await expect(storage.markUnknown(queued.clientMutationId, "blockedUnknown")).resolves.toBe(false);
      await expect(storage.getOutbox(queued.clientMutationId)).resolves.toMatchObject({
        state: "canceled",
        attempted: false,
      });
    });

    test("transferToRecovery moves a record out of the outbox with the daemon's reason", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("steer that lost its turn"));
      const recovery = await storage.transferToRecovery(record.clientMutationId, "rejected", "turn is not active");
      expect(recovery).toMatchObject({
        clientMutationId: record.clientMutationId,
        recoveryKind: "rejected",
        recoveryReason: "turn is not active",
      });
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toBeUndefined();
      await expect(storage.getRecovery(record.clientMutationId)).resolves.toMatchObject({
        recoveryReason: "turn is not active",
      });
    });

    test("listTargetRefs reports every ref with a waiting outbox or optimistic record", async () => {
      await storage.enqueueIntent(conformanceTextIntent("a", CONFORMANCE_TARGET));
      const accepted = await storage.enqueueIntent({
        ...conformanceTextIntent("b", CONFORMANCE_OTHER_TARGET),
        optimisticDisplay: { input: [{ type: "text", text: "b" }] },
      });
      await storage.settleReceipt(accepted.clientMutationId, "pending");
      // The ref's waiting record now lives in the optimistic table; both stores
      // feed listTargetRefs.
      await expect(storage.getOptimistic(accepted.clientMutationId)).resolves.toMatchObject({ state: "accepted" });
      await expect(storage.listTargetRefs()).resolves.toEqual([CONFORMANCE_TARGET, CONFORMANCE_OTHER_TARGET].sort());
    });

    // The shared settlement contract's note supersede: a settled notes/human/set
    // discards the SAME ref's earlier note recovery rows (a later save is the
    // note editor's only retry), while leaving other methods, other targets,
    // and newer rows exactly where they were. A structural interface cannot
    // express this semantic side effect, so both hosts must run it.
    test("settleReceipt discards a ref's superseded note recovery rows and preserves the rest", async () => {
      const ids = await seedSupersededNoteScenario(storage);
      await expect(storage.settleReceipt(ids.settling.clientMutationId, "pending")).resolves.toBe(true);
      await expectSupersedeDroppedEarlierNotes(storage, ids);
    });

    // The same supersede rides settleApplied's transaction: an applied note is
    // authoritative too, so it retires the ref's earlier note recovery rows just
    // as a receipt does.
    test("settleApplied discards a ref's superseded note recovery rows and preserves the rest", async () => {
      const ids = await seedSupersededNoteScenario(storage);
      await expect(storage.settleApplied(ids.settling.clientMutationId)).resolves.toBe(true);
      await expectSupersedeDroppedEarlierNotes(storage, ids);
    });

    // The guard: only a settling NOTE supersedes note recovery rows. Settling
    // another method on the ref (here a turn) must leave the refused note row
    // where it is - without this case the method guard could be removed and no
    // contract would notice.
    test("settling a non-note intent preserves the ref's refused note recovery rows", async () => {
      const refused = await storage.enqueueIntent(conformanceNoteIntent("refused older"));
      await storage.transferToRecovery(refused.clientMutationId, "rejected", "note refused");
      const settling = await storage.enqueueIntent(conformanceTextIntent("a turn", CONFORMANCE_TARGET));
      await expect(storage.settleReceipt(settling.clientMutationId, "pending")).resolves.toBe(true);
      await expect(storage.getRecovery(refused.clientMutationId)).resolves.toMatchObject({
        clientMutationId: refused.clientMutationId,
      });
    });

    // settleApplied's transaction carries the same guard: an applied turn must
    // not touch the ref's refused note recovery rows.
    test("settling a non-note intent via settleApplied preserves the ref's refused note recovery rows", async () => {
      const refused = await storage.enqueueIntent(conformanceNoteIntent("refused older"));
      await storage.transferToRecovery(refused.clientMutationId, "rejected", "note refused");
      const settling = await storage.enqueueIntent(conformanceTextIntent("a turn", CONFORMANCE_TARGET));
      await expect(storage.settleApplied(settling.clientMutationId)).resolves.toBe(true);
      await expect(storage.getRecovery(refused.clientMutationId)).resolves.toMatchObject({
        clientMutationId: refused.clientMutationId,
      });
    });

    // The settlement source may be a recovery row, not only the outbox: a note
    // already in recovery that later settles still supersedes the ref's earlier
    // note recovery rows.
    test("a note settling from recovery still supersedes earlier note recovery rows", async () => {
      const older = await storage.enqueueIntent(conformanceNoteIntent("refused older"));
      await storage.transferToRecovery(older.clientMutationId, "rejected", "note refused");
      const settling = await storage.enqueueIntent(conformanceNoteIntent("recovered then receipted"));
      await storage.transferToRecovery(settling.clientMutationId, "rejected", "note refused");
      await expect(storage.settleReceipt(settling.clientMutationId, "pending")).resolves.toBe(true);
      await expect(storage.getRecovery(older.clientMutationId)).resolves.toBeUndefined();
    });
  });
}
