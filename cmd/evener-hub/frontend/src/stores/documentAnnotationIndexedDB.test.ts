// First-slice append tests for the browser document-annotation adapter; every
// expected reference string is the literal REFERENCE from annotationFixtures.ts.

import {
  type AcceptedReadResult,
  type AcceptedSourceLease,
  type AnnotationView,
  buildInput,
  type ComposerMention,
  type DestinationProvenance,
  type DocFileContent,
  type DocumentKey,
  displayedFingerprint,
  type PreparedAnnotation,
  type ReadTicket,
} from "@evener/appwire-client";
import { IDBDatabase } from "fake-indexeddb";
import { expect, it, vi } from "vitest";
import { REFERENCE, SOURCE } from "../panes/doc/testing/annotationFixtures";
import {
  acceptedOperation,
  attachmentIdentity,
  blobBytes,
  cleanupAfterTest,
  docContent,
  fixture,
  preparedAnnotationFromLease,
  rowsForTests,
} from "../panes/doc/testing/annotationRepositoryFixtures";
import { createComposerSourceState } from "../panes/session/composer/sourceState";
import { browserDraftKey } from "../stores/browserDraftRepository";
import { type NamespaceRow, serializeSessionKey } from "../stores/compositionRevision";
import { createDocumentAnnotationRepository } from "../stores/documentAnnotationIndexedDB";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_SEQUENCES_STORE,
  type AnnotationEvidenceRow,
  type AnnotationExpiryRow,
  type AnnotationHeadRow,
  type AnnotationOperationRow,
  type AnnotationPayloadRow,
  type AnnotationSequenceRow,
  annotationDocumentKey,
  annotationExpiryKey,
  annotationNamespaceKey,
  annotationOperationKey,
  MUTATION_OUTBOX_STORE,
} from "../stores/localCompositionSchema";
import type { RecoveryWriteGuard } from "../stores/mutationOutboxIndexedDB";
import { SessionCacheIndexedDB } from "../stores/sessionCacheIndexedDB";
import { clearCachedSessions, resetThreadsStoreForTests, setSessionCacheAdapterForTests } from "../stores/threads";

it.each(["draft", "recovery"] as const)("appends only to %s backing in the SAME session", async (backing) => {
  const f = await fixture(`append-${backing}`);
  // Select the recovery backing only; never merge or consume the ordinary draft.
  if (backing === "recovery") f.source.setActiveRecoveryId("rec-1");
  const beforeOrdinary = await f.drafts.load(f.key);
  const beforeRecovery = await f.outbox.getRecovery("rec-1");
  if (!beforeOrdinary || !beforeRecovery) throw new Error("fixture baseline rows missing");
  const beforeAttachment = beforeRecovery.attachments[0];
  if (!beforeAttachment) throw new Error("fixture recovery attachment missing");
  expect(beforeAttachment).toMatchObject(attachmentIdentity);
  expect(await blobBytes(beforeAttachment.blob)).toEqual(new Uint8Array([1]));

  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const ordinary = await f.drafts.load(f.key);
  const recovery = await f.outbox.getRecovery("rec-1");
  if (!ordinary || !recovery) throw new Error("post-append rows missing");
  if (backing === "draft") {
    expect(ordinary.text).toBe(`ordinary /same\n\n${REFERENCE}`);
    expect(recovery).toEqual(beforeRecovery);
  } else {
    expect(ordinary).toMatchObject({
      text: "ordinary /same",
      skillNames: ["same"],
      mentions: [{ kind: "skill", name: "same", offset: 9 }],
    });
    expect(recovery.composerText).toBe(`recovery /plan [image 1]\n\n${REFERENCE}`);
    expect(recovery.composerMentions).toEqual([{ kind: "command", name: "plan", offset: 9 }]);
    expect(recovery.payload.input).toEqual([
      {
        type: "text",
        text: `recovery /plan [image 1]\n\n${REFERENCE}`,
        mentions: [{ kind: "command", name: "plan", offset: 9 }],
      },
      { type: "image", mediaType: "image/png", data: "AQ==", name: "one.png" },
      { type: "command", name: "plan" },
    ]);
    expect(recovery.optimisticDisplay).toEqual({ method: "turn/start", input: recovery.payload.input });
  }
  // Both backings preserve the full attachment record and its real bytes.
  expect(recovery.attachments).toEqual(beforeRecovery.attachments);
  const afterAttachment = recovery.attachments[0];
  if (!afterAttachment) throw new Error("appended recovery attachment missing");
  expect(afterAttachment).toMatchObject(attachmentIdentity);
  expect(await blobBytes(afterAttachment.blob)).toEqual(new Uint8Array([1]));
  expect(ordinary.token.sessionRevision).toBe(beforeOrdinary.token.sessionRevision + 1);

  // Repeating the same identified operation is idempotent, with no second
  // reference: both real backing records stay byte-identical to their post-first state.
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
  const replayedOrdinary = await f.drafts.load(f.key);
  const replayedRecovery = await f.outbox.getRecovery("rec-1");
  if (!replayedOrdinary || !replayedRecovery) throw new Error("post-replay rows missing");
  expect(replayedOrdinary).toEqual(ordinary);
  expect(replayedRecovery).toEqual(recovery);
  const replayedAttachment = replayedRecovery.attachments[0];
  if (!replayedAttachment) throw new Error("replayed recovery attachment missing");
  expect(replayedAttachment).toMatchObject(attachmentIdentity);
  expect(await blobBytes(replayedAttachment.blob)).toEqual(new Uint8Array([1]));
});

it("does not recreate missing recovery", async () => {
  const f = await fixture("absent");
  f.source.setActiveRecoveryId("rec-1");
  await f.outbox.discardRecovery("rec-1");
  const ordinaryBefore = await f.drafts.load(f.key);
  if (!ordinaryBefore) throw new Error("fixture baseline row missing");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("pending");
  expect(await f.outbox.getRecovery("rec-1")).toBeUndefined();
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
});

const mentionCases: Array<{ name: string; mentions: ComposerMention[] | undefined }> = [
  { name: "absent", mentions: undefined },
  { name: "empty", mentions: [] },
];

it.each(mentionCases)("preserves meaningful mention $name on recovery append", async ({ name, mentions }) => {
  const f = await fixture(`mentions-${name}`);
  // updateRecoveryInput is itself a durable session write; capture the baseline
  // row after it so the append's single revision advance is what +1 measures.
  await f.outbox.updateRecoveryInput("rec-1", buildInput("recovery", [], [], [], mentions), [], "recovery", mentions);
  const beforeOrdinary = await f.drafts.load(f.key);
  if (!beforeOrdinary) throw new Error("fixture baseline row missing");
  f.source.setActiveRecoveryId("rec-1");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const recovery = await f.outbox.getRecovery("rec-1");
  if (!recovery) throw new Error("appended recovery missing");
  expect(recovery.composerText).toBe(`recovery\n\n${REFERENCE}`);
  expect((await f.drafts.load(f.key))?.token.sessionRevision).toBe(beforeOrdinary.token.sessionRevision + 1);
  expect(recovery.composerMentions).toEqual(mentions);
});

type AnnotationFixture = Awaited<ReturnType<typeof fixture>>;

type ComposerSourceSnapshot = ReturnType<AnnotationFixture["source"]["getSnapshot"]>;

async function annotationRows(f: AnnotationFixture) {
  return {
    operations: await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE),
    payloads: await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE),
    sequences: await rowsForTests<AnnotationSequenceRow>(f.factory, f.databaseName, ANNOTATION_SEQUENCES_STORE),
  };
}

// The complete annotation row state: head, evidence, payload, operation,
// sequence and expiry. Test-local, so comparisons never depend on a framework.
async function annotationSnapshot(f: AnnotationFixture) {
  return {
    heads: await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE),
    evidence: await rowsForTests<AnnotationEvidenceRow>(f.factory, f.databaseName, ANNOTATION_EVIDENCE_STORE),
    payloads: await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE),
    operations: await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE),
    sequences: await rowsForTests<AnnotationSequenceRow>(f.factory, f.databaseName, ANNOTATION_SEQUENCES_STORE),
    expiries: await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE),
  };
}

// Save must persist the attempt as it was when saveAnnotation was called, not
// the caller's object after later synchronous edits.
it("saves the captured attempt despite caller mutation after the call", async () => {
  const f = await fixture("save-immutable");
  const operation = await acceptedOperation(f.repo, f.document, "save-immutable", "original note", f.destination);
  const original = structuredClone(operation);
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  const sequencesBefore = (await annotationRows(f)).sequences;
  const baseSequence = sequencesBefore[0]?.lastSequence;
  if (baseSequence === undefined) throw new Error("baseline sequence missing");

  const saving = f.repo.saveAnnotation(operation);
  operation.note = "mutated note";
  operation.destination = {
    kind: "browser-origin",
    paneId: "pane-B",
    compositionOwnerId: f.source.sourceId,
  };
  const receipt = await saving;

  const operationKey = annotationOperationKey(f.document.namespace, "save-immutable");
  expect(receipt.operationId).toBe("save-immutable");
  const payloads = (await annotationRows(f)).payloads.filter((row) => row.payloadKey === operationKey);
  expect(payloads).toHaveLength(1);
  expect(payloads[0]?.operation).toEqual(original);
  expect(payloads[0]?.operation.note).toBe("original note");
  expect(payloads[0]?.operation.destination).toEqual(original.destination);
  const operations = (await annotationRows(f)).operations.filter((row) => row.operationKey === operationKey);
  expect(operations).toHaveLength(1);
  expect((await annotationRows(f)).sequences[0]?.lastSequence).toBe(baseSequence + 1);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
});

it("rejects a different payload under a pending operation id and preserves every row", async () => {
  const f = await fixture("duplicate-pending");
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  const rowsBefore = await annotationRows(f);
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  const originalOperation = rowsBefore.operations.find((row) => row.operationKey === f.operationKey);
  if (!originalOperation) throw new Error("fixture operation row missing");

  const clone = { ...f.operation, note: "different literal note" };
  const mismatched = await f.repo.saveAnnotation(clone).then(
    (receipt) => ({ ok: true as const, receipt }),
    (error: unknown) => ({ ok: false as const, error }),
  );

  // Preservation evidence runs whether or not the mismatched save resolved.
  expect(await annotationRows(f)).toEqual(rowsBefore);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);

  // The unchanged original returns the actual original receipt and changes nothing.
  const receipt = await f.repo.saveAnnotation(f.operation);
  expect(receipt.operationId).toBe(originalOperation.operationId);
  expect(receipt.epoch).toBe(originalOperation.epoch);
  expect(receipt.sessionSequence).toBe(originalOperation.sessionSequence);
  expect(await annotationRows(f)).toEqual(rowsBefore);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  expect(mismatched.ok).toBe(false);
  if (mismatched.ok) throw new Error("mismatched duplicate unexpectedly resolved");
  expect(String((mismatched.error as Error).message)).toContain("duplicate operation id with a different payload");
});

it("rejects a different payload under an applied operation id and preserves every row", async () => {
  const f = await fixture("duplicate-applied");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  const rowsBefore = await annotationRows(f);
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  const originalOperation = rowsBefore.operations.find((row) => row.operationKey === f.operationKey);
  if (!originalOperation) throw new Error("fixture operation row missing");

  const clone = { ...f.operation, note: "different literal note" };
  const mismatched = await f.repo.saveAnnotation(clone).then(
    (receipt) => ({ ok: true as const, receipt }),
    (error: unknown) => ({ ok: false as const, error }),
  );

  expect(await annotationRows(f)).toEqual(rowsBefore);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);

  const receipt = await f.repo.saveAnnotation(f.operation);
  expect(receipt.operationId).toBe(originalOperation.operationId);
  expect(receipt.epoch).toBe(originalOperation.epoch);
  expect(receipt.sessionSequence).toBe(originalOperation.sessionSequence);
  expect(await annotationRows(f)).toEqual(rowsBefore);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  expect(mismatched.ok).toBe(false);
  if (mismatched.ok) throw new Error("mismatched duplicate unexpectedly resolved");
  expect(String((mismatched.error as Error).message)).toContain("duplicate operation id with a different payload");
});

it.each(["draft", "recovery"] as const)(
  "rolls back a whole %s abort and retries the same operation to applied",
  async (backing) => {
    const expectedText =
      backing === "draft" ? `ordinary /same\n\n${REFERENCE}` : `recovery /plan [image 1]\n\n${REFERENCE}`;
    let fault = true;
    const f = await fixture(`abort-${backing}`, {
      beforeAbortableCompletion: () => {
        if (fault) throw new Error("abort boundary");
      },
    });
    if (backing === "recovery") f.source.setActiveRecoveryId("rec-1");
    await f.source.ready();
    await f.source.prepareAnnotation();
    const snapshots: ComposerSourceSnapshot[] = [];
    const unsubscribe = f.source.subscribe(() => {
      snapshots.push(f.source.getSnapshot());
    });
    cleanupAfterTest(() => unsubscribe());
    const acknowledge = vi.spyOn(f.source, "acknowledgeAnnotation");
    const ordinaryBefore = await f.drafts.load(f.key);
    if (!ordinaryBefore) throw new Error("fixture baseline ordinary row missing");
    const recoveryBefore = await f.outbox.getRecovery("rec-1");
    if (!recoveryBefore) throw new Error("fixture baseline recovery row missing");
    const rowsBefore = await annotationRows(f);
    const sourceBefore = f.source.getSnapshot();
    const tokenBefore = f.source.getDraftToken();
    const generationBefore = f.source.getLocalGeneration();
    const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
    const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
    const resend = vi.spyOn(f.outbox, "resendRecovery");
    const update = vi.spyOn(f.outbox, "updateRecoveryInput");

    const settled = await f.repo.handoff(f.document, "op-1").then(
      (result) => ({ ok: true as const, result }),
      (error: unknown) => ({ ok: false as const, error }),
    );

    // Whole-transaction rollback and zero-mutation evidence run before the
    // desired-result assertion that currently fails.
    expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
    expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
    expect(await annotationRows(f)).toEqual(rowsBefore);
    expect(f.source.getSnapshot()).toEqual(sourceBefore);
    expect(f.source.getDraftToken()).toEqual(tokenBefore);
    expect(f.source.getLocalGeneration()).toBe(generationBefore);
    // prepareAnnotation publishes hydration, so the rollback claim is that no
    // acknowledgment ran and no captured state carried the appended reference.
    expect(acknowledge).not.toHaveBeenCalled();
    for (const snapshot of snapshots) {
      expect(snapshot.text).not.toContain(REFERENCE);
    }
    expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
    expect(enqueue).not.toHaveBeenCalled();
    expect(resend).not.toHaveBeenCalled();
    expect(update).not.toHaveBeenCalled();

    // A whole abort must surface as the documented pending result, not a throw.
    expect(settled.ok).toBe(true);
    if (!settled.ok) throw settled.error;
    expect(settled.result).toEqual({ state: "pending", reason: "owner-unavailable" });

    // Healthy retry tail: only the fault flag changes.
    fault = false;
    const expectedInput = [
      {
        type: "text",
        text: expectedText,
        mentions: [{ kind: "command", name: "plan", offset: 9 }],
      },
      { type: "image", mediaType: "image/png", data: "AQ==", name: "one.png" },
      { type: "command", name: "plan" },
    ];
    const advancedToken = {
      ...ordinaryBefore.token,
      sessionRevision: ordinaryBefore.token.sessionRevision + 1,
    };
    const applied = await f.repo.handoff(f.document, "op-1");
    expect(applied.state).toBe("applied");
    const ordinaryAfter = await f.drafts.load(f.key);
    if (!ordinaryAfter) throw new Error("post-retry ordinary row missing");
    const recoveryAfter = await f.outbox.getRecovery("rec-1");
    if (!recoveryAfter) throw new Error("post-retry recovery row missing");
    expect(ordinaryAfter).toEqual({
      ...ordinaryBefore,
      ...(backing === "draft" ? { text: expectedText } : {}),
      token: advancedToken,
    });
    if (backing === "draft") {
      expect(recoveryAfter).toEqual(recoveryBefore);
    } else {
      // The complete recovery row must equal its baseline with only the appended
      // text, input and optimistic display changed.
      expect(recoveryAfter).toEqual({
        ...recoveryBefore,
        composerText: expectedText,
        payload: { ...recoveryBefore.payload, input: expectedInput },
        optimisticDisplay: { method: "turn/start", input: expectedInput },
      });
      const attachment = recoveryAfter.attachments[0];
      if (!attachment) throw new Error("post-retry recovery attachment missing");
      expect(attachment).toMatchObject({
        presentationId: "image-1",
        marker: 1,
        name: "one.png",
        mediaType: "image/png",
      });
      expect(new Uint8Array(await attachment.blob.arrayBuffer())).toEqual(new Uint8Array([1]));
    }
    expect(ordinaryAfter.token.sessionRevision).toBe(ordinaryBefore.token.sessionRevision + 1);
    expect(f.source.getSnapshot()).toEqual({ ...sourceBefore, text: expectedText });
    expect(f.source.getDraftToken()).toEqual(ordinaryAfter.token);
    const appliedRows = await annotationRows(f);
    const appliedReceipts = appliedRows.operations.filter((row) => row.operationKey === f.operationKey);
    expect(appliedReceipts).toHaveLength(1);
    const appliedReceipt = appliedReceipts[0];
    if (appliedReceipt?.state !== "applied") throw new Error("applied receipt missing");
    expect(appliedReceipt.draftRevision).toBe(ordinaryAfter.token.sessionRevision);
    expect(appliedRows.payloads).toEqual(rowsBefore.payloads);
    expect(acknowledge).toHaveBeenCalledTimes(1);
    expect(snapshots.some((snapshot) => snapshot.text === expectedText)).toBe(true);
    const snapshotsAfterApply = snapshots.length;
    expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);

    expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
    expect(await annotationRows(f)).toEqual(appliedRows);
    expect(await f.drafts.load(f.key)).toEqual(ordinaryAfter);
    const replayedRecovery = await f.outbox.getRecovery("rec-1");
    expect(replayedRecovery).toEqual(recoveryAfter);
    expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
    if (backing === "recovery") {
      const replayedAttachment = replayedRecovery?.attachments[0];
      if (!replayedAttachment) throw new Error("replayed recovery attachment missing");
      expect(replayedAttachment).toMatchObject({
        presentationId: "image-1",
        marker: 1,
        name: "one.png",
        mediaType: "image/png",
      });
      expect(new Uint8Array(await replayedAttachment.blob.arrayBuffer())).toEqual(new Uint8Array([1]));
    }
    expect(acknowledge).toHaveBeenCalledTimes(1);
    expect(snapshots.length).toBe(snapshotsAfterApply);
    expect(enqueue).not.toHaveBeenCalled();
    expect(resend).not.toHaveBeenCalled();
    expect(update).not.toHaveBeenCalled();
  },
);

// Actual source lifetime at every approved boundary, both backings. The
// original source is disposed through real handoff hooks; a distinct replacement
// source on the same session key witnesses that the disposed original never
// mutated shared state. A completed transaction is never treated as abortable.
it.each([
  ["draft", "before"],
  ["draft", "abort"],
  ["draft", "after"],
  ["draft", "close-without-abort"],
  ["recovery", "before"],
  ["recovery", "abort"],
  ["recovery", "after"],
  ["recovery", "close-without-abort"],
] as const)("resolves %s/%s without partial persistence or publication", async (backing, phase) => {
  let f: Awaited<ReturnType<typeof fixture>>;
  // The synchronous hooks only ever dispose the source during a real handoff.
  f = await fixture(`close-${backing}-${phase}`, {
    beforeFinalGuard: () => {
      if (phase === "before") f.source.dispose();
    },
    beforeAbortableCompletion: () => {
      if (phase === "abort") {
        f.source.dispose();
        throw new Error("abort boundary");
      }
      if (phase === "close-without-abort") f.source.dispose();
    },
    afterCommit: () => {
      if (phase === "after") f.source.dispose();
    },
  });
  if (backing === "recovery") f.source.setActiveRecoveryId("rec-1");
  // Prepare and settle the ORIGINAL source before any baseline capture.
  await f.source.prepareAnnotation();
  await f.source.whenSettled();

  const replacement = createComposerSourceState("root", {
    repository: f.drafts,
    key: f.key,
    outbox: f.outbox,
  });
  cleanupAfterTest(() => replacement.dispose());
  await replacement.ready();
  const replacementBefore = replacement.getSnapshot();

  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const baselineAttachment = recoveryBefore.attachments[0];
  if (!baselineAttachment) throw new Error("fixture recovery attachment missing");
  const recoveryBytesBefore = await blobBytes(baselineAttachment.blob);
  const operationsBefore = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  const payloadsBefore = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
  const sequencesBefore = await rowsForTests<AnnotationSequenceRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_SEQUENCES_STORE,
  );
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);
  const baselineReceipt = operationsBefore.find((row) => row.operationKey === f.operationKey);
  if (!baselineReceipt) throw new Error("fixture operation row missing");
  expect(baselineReceipt.state).toBe("pending");

  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");

  // subscribe takes () => void: count real calls, never undocumented arguments.
  const publication = vi.fn();
  const unsubscribe = f.source.subscribe(publication);
  cleanupAfterTest(() => unsubscribe());

  const result = await f.repo.handoff(f.document, "op-1");
  const committed = phase === "after" || phase === "close-without-abort";
  expect(result.state).toBe(committed ? "applied" : "pending");
  // Plan 736: handoff must suppress redundant same-value preparation
  // publications. Soft so this known obligation cannot hide persistence below.
  expect.soft(publication).not.toHaveBeenCalled();
  expect(replacement.getSnapshot()).toEqual(replacementBefore);

  const receipt = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationKey === f.operationKey);
  expect(receipt?.state).toBe(committed ? "applied" : "pending");

  if (!committed) {
    // Early close and a real whole abort roll back to the exact baseline.
    expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
    expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
    expect(receipt).toEqual(baselineReceipt);
    expect(await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).toEqual(
      payloadsBefore,
    );
    expect(await rowsForTests<AnnotationSequenceRow>(f.factory, f.databaseName, ANNOTATION_SEQUENCES_STORE)).toEqual(
      sequencesBefore,
    );
  } else {
    const ordinary = await f.drafts.load(f.key);
    const recovery = await f.outbox.getRecovery("rec-1");
    if (!ordinary || !recovery) throw new Error("post-append rows missing");
    // The expected token is advanced independently from the before token; the
    // post-append row must carry exactly that identity, not itself.
    const advancedToken = {
      ...ordinaryBefore.token,
      sessionRevision: ordinaryBefore.token.sessionRevision + 1,
    };
    expect(ordinary.token).toEqual(advancedToken);
    const appendedRecoveryInput = [
      {
        type: "text",
        text: `recovery /plan [image 1]\n\n${REFERENCE}`,
        mentions: [{ kind: "command", name: "plan", offset: 9 }],
      },
      { type: "image", mediaType: "image/png", data: "AQ==", name: "one.png" },
      { type: "command", name: "plan" },
    ];
    if (backing === "recovery") {
      expect(ordinary).toEqual({ ...ordinaryBefore, token: advancedToken });
      // Only the approved recovery fields change; attachments and every other
      // metadata field stay intact under full-row equality.
      expect(recovery).toEqual({
        ...recoveryBefore,
        composerText: `recovery /plan [image 1]\n\n${REFERENCE}`,
        payload: { ...recoveryBefore.payload, input: appendedRecoveryInput },
        optimisticDisplay: { method: "turn/start", input: appendedRecoveryInput },
      });
      const appendedAttachment = recovery.attachments[0];
      if (!appendedAttachment) throw new Error("post-append recovery attachment missing");
      expect(appendedAttachment).toMatchObject(attachmentIdentity);
      expect(await blobBytes(appendedAttachment.blob)).toEqual(recoveryBytesBefore);
    } else {
      expect(ordinary).toEqual({
        ...ordinaryBefore,
        text: `ordinary /same\n\n${REFERENCE}`,
        token: advancedToken,
      });
      // Exactly one append to the selected backing.
      expect(ordinary.text.split(REFERENCE)).toHaveLength(2);
      expect(recovery).toEqual(recoveryBefore);
    }
    const appliedRows = await rowsForTests<AnnotationOperationRow>(
      f.factory,
      f.databaseName,
      ANNOTATION_OPERATIONS_STORE,
    );
    expect(appliedRows.filter((row) => row.operationKey === f.operationKey)).toHaveLength(1);
    const appliedReceipt = appliedRows.find((row) => row.operationKey === f.operationKey);
    if (appliedReceipt?.state !== "applied") throw new Error("applied receipt missing");
    expect(appliedReceipt.draftRevision).toBe(ordinaryBefore.token.sessionRevision + 1);
    expect(await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).toEqual(
      payloadsBefore,
    );

    // Replay is terminal: identical persisted state, no second publication, no
    // changed attachment bytes.
    const publicationsAfterApply = publication.mock.calls.length;
    expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
    expect(await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).toEqual(
      appliedRows,
    );
    expect(await f.drafts.load(f.key)).toEqual(ordinary);
    expect(await f.outbox.getRecovery("rec-1")).toEqual(recovery);
    expect(publication.mock.calls.length).toBe(publicationsAfterApply);
    // Read the ACTUAL post-replay recovery row, not the pre-replay capture.
    const replayedRecovery = await f.outbox.getRecovery("rec-1");
    if (!replayedRecovery) throw new Error("replayed recovery row missing");
    const replayedAttachment = replayedRecovery.attachments[0];
    if (!replayedAttachment) throw new Error("replayed recovery attachment missing");
    expect(await blobBytes(replayedAttachment.blob)).toEqual(recoveryBytesBefore);
  }
  replacement.dispose();

  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

it("rejects a genuinely stale ticket from a second adapter without changing durable state", async () => {
  const f = await fixture("read-stale-second-adapter");
  const second = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    // beginRead/acceptRead never resolve a destination source.
    targetFor: () => null,
  });
  cleanupAfterTest(() => second.close());

  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);
  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");

  const t1 = await f.repo.beginRead(f.document);
  // T2's ISSUANCE alone (its beginRead completing) must retire T1; no acceptance
  // has run when the old ticket is attempted. Durable state is captured here.
  const t2 = await second.beginRead(f.document);
  const snapshotBefore = await annotationSnapshot(f);
  expect(snapshotBefore.expiries).toEqual([]);

  // Changed content through the old T1 is stale at T2's issuance: nothing durable
  // and neither composer backing may change.
  const stale = await f.repo.acceptRead(t1, docContent(SOURCE, "rev-B"));
  expect(stale).toEqual({ state: "stale-ticket" });
  expect(await annotationSnapshot(f)).toEqual(snapshotBefore);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  // The SAME original T2 still accepts SOURCE/rev-A, and the original pending
  // operation applies once through its eligible original source.
  const accepted = await second.acceptRead(t2, docContent(SOURCE, "rev-A"));
  expect(accepted.state).toBe("accepted");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const ordinaryAfter = await f.drafts.load(f.key);
  expect(ordinaryAfter).toEqual({
    ...ordinaryBefore,
    text: `ordinary /same\n\n${REFERENCE}`,
    token: { ...ordinaryBefore.token, sessionRevision: ordinaryBefore.token.sessionRevision + 1 },
  });
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

it("an unchanged re-read keeps the accepted lease, payload and pending operation usable", async () => {
  const f = await fixture("read-unchanged");
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const payloadsBefore = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
  const operationsBefore = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  const originalOperation = operationsBefore.find((row) => row.operationKey === f.operationKey);
  if (!originalOperation) throw new Error("fixture operation row missing");

  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);
  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");

  const reRead = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-A"));
  expect(reRead.state).toBe("accepted");
  if (reRead.state !== "accepted") throw new Error("unchanged re-read not accepted");
  // Retains the original epoch and namespace incarnation, and opens no expiry row.
  expect(reRead.lease.epoch).toBe(originalOperation.epoch);
  expect(reRead.lease.namespaceIncarnation).toBe(originalOperation.namespaceIncarnation);
  expect(await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE)).toEqual([]);
  // The original payload and pending operation are unchanged; head metadata may advance.
  expect(await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).toEqual(
    payloadsBefore,
  );
  expect(
    (await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).find(
      (row) => row.operationKey === f.operationKey,
    ),
  ).toEqual(originalOperation);

  // A new valid operation saved from that accepted lease.
  const next = preparedAnnotationFromLease(reRead.lease, "op-2", "second note", f.destination, "rev-A");
  const receipt = await f.repo.saveAnnotation(next);
  expect(receipt.operationId).toBe("op-2");
  expect(receipt.sessionSequence).toBe(originalOperation.sessionSequence + 1);

  // Both hand off in session order, each reference appearing exactly once.
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  expect((await f.repo.handoff(f.document, "op-2")).state).toBe("applied");
  const secondReference = REFERENCE.replace('"Keep this scope."', '"second note"');
  const expectedText = `ordinary /same\n\n${REFERENCE}\n\n${secondReference}`;
  const ordinaryAfter = await f.drafts.load(f.key);
  expect(ordinaryAfter).toEqual({
    ...ordinaryBefore,
    text: expectedText,
    token: { ...ordinaryBefore.token, sessionRevision: ordinaryBefore.token.sessionRevision + 2 },
  });
  expect(ordinaryAfter?.text.split(REFERENCE)).toHaveLength(2);
  expect(ordinaryAfter?.text.split(secondReference)).toHaveLength(2);
  // No recovery mutation from either ordinary append.
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

it("fences a pending note before deletion and resumes useful work afterward", async () => {
  const f = await fixture("read-expiry-fence");
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const recoveryAttachmentBefore = recoveryBefore.attachments[0];
  if (!recoveryAttachmentBefore) throw new Error("fixture recovery attachment missing");
  const recoveryBytesBefore = await blobBytes(recoveryAttachmentBefore.blob);
  const operationsBefore = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  const operationBefore = operationsBefore.find((row) => row.operationKey === f.operationKey);
  if (!operationBefore) throw new Error("fixture operation row missing");
  const oldEpoch = operationBefore.epoch;
  const headsBefore = await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE);
  const oldEvidenceKey = headsBefore[0]?.evidenceKey;
  if (oldEvidenceKey === undefined) throw new Error("fixture evidence key missing");
  expect(await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE)).toEqual([]);

  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);
  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");

  const changed = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-B", true));
  expect(changed.state).toBe("expiry-pending");
  if (changed.state !== "expiry-pending" || !changed.receipt) throw new Error("fenced expiry receipt missing");
  const receipt = changed.receipt;
  expect(receipt.key).toEqual(f.document);
  expect(receipt.expiredEpoch).toBe(oldEpoch);
  expect(receipt.phase).toBe("fenced");

  // Before finishExpiry the fence is durable: new-epoch head, op-1 cancelled and
  // the expiry receipt written.
  const headAfterFence = (await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE))[0];
  if (!headAfterFence) throw new Error("durable head row missing");
  expect(headAfterFence.documentKey).toBe(annotationDocumentKey(f.document));
  expect(headAfterFence.epoch).not.toBe(oldEpoch);
  expect(
    (await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).find(
      (row) => row.operationKey === f.operationKey,
    )?.state,
  ).toBe("cancelled");
  const expiryRows = await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE);
  expect(expiryRows.map((row) => row.expiryKey)).toEqual([
    annotationExpiryKey(annotationDocumentKey(f.document), oldEpoch),
  ]);
  expect(expiryRows[0]?.receipt).toEqual(receipt);

  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("cancelled");
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  expect((await f.drafts.load(f.key))?.token.sessionRevision).toBe(ordinaryBefore.token.sessionRevision);

  // A NEW operation id using the OLD accepted lease must fail without changing
  // ANY row, including head/evidence/expiry, or consuming a sequence number.
  const snapshotBeforeStaleLease = await annotationSnapshot(f);
  const staleAttempt = preparedAnnotationFromLease(
    f.operation.lease,
    "op-stale-lease",
    "stale lease note",
    f.destination,
  );
  const staleSave = await f.repo.saveAnnotation(staleAttempt).then(
    (value) => ({ ok: true as const, value }),
    (error: unknown) => ({ ok: false as const, error }),
  );
  expect(staleSave.ok).toBe(false);
  expect(await annotationSnapshot(f)).toEqual(snapshotBeforeStaleLease);

  await f.repo.finishExpiry(receipt);
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).some(
      (row) => row.payloadKey === f.operationKey,
    ),
  ).toBe(false);
  expect(
    (await rowsForTests<AnnotationEvidenceRow>(f.factory, f.databaseName, ANNOTATION_EVIDENCE_STORE)).some(
      (row) => row.evidenceKey === oldEvidenceKey,
    ),
  ).toBe(false);
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  // After deletion the terminal receipt is authoritative: replaying the ORIGINAL
  // op-1 save callback returns its original receipt without recreating payload,
  // and its handoff stays cancelled. Nothing durable or composed changed.
  const snapshotAfterDelete = await annotationSnapshot(f);
  expect(await f.repo.saveAnnotation(f.operation)).toEqual({
    operationId: "op-1",
    epoch: oldEpoch,
    sessionSequence: operationBefore.sessionSequence,
  });
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("cancelled");
  expect(await annotationSnapshot(f)).toEqual(snapshotAfterDelete);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  // A fresh accepted B operation saves and applies afterward, its expected
  // reference carrying rev-B provenance.
  const fresh = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-B", true));
  expect(fresh.state).toBe("accepted");
  if (fresh.state !== "accepted") throw new Error("fresh B read not accepted");
  const bOperation = preparedAnnotationFromLease(fresh.lease, "B", "B note", f.destination, "rev-B", true);
  await f.repo.saveAnnotation(bOperation);
  expect((await f.repo.handoff(f.document, "B")).state).toBe("applied");
  const bReference = REFERENCE.replace('"Keep this scope."', '"B note"').replace('"rev-A"', '"rev-B"');
  const ordinaryAfterB = await f.drafts.load(f.key);
  expect(ordinaryAfterB).toEqual({
    ...ordinaryBefore,
    text: `ordinary /same\n\n${bReference}`,
    token: { ...ordinaryBefore.token, sessionRevision: ordinaryBefore.token.sessionRevision + 1 },
  });
  const recoveryAfterB = await f.outbox.getRecovery("rec-1");
  expect(recoveryAfterB).toEqual(recoveryBefore);
  const rereadAttachment = recoveryAfterB?.attachments[0];
  if (!rereadAttachment) throw new Error("reread recovery attachment missing");
  expect(rereadAttachment).toMatchObject(attachmentIdentity);
  expect(await blobBytes(rereadAttachment.blob)).toEqual(recoveryBytesBefore);
  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

it("applied text survives a failed expiry deletion, stale cleanup and reversion", async () => {
  let failDeletion = true;
  const f = await fixture("read-expiry-delete-fault", {
    beforeCommit: (operation) => {
      if (operation === "deleteExpiry" && failDeletion) throw new Error("delete fault");
    },
  });

  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);
  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");

  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const ordinaryApplied = await f.drafts.load(f.key);
  const recoveryApplied = await f.outbox.getRecovery("rec-1");
  if (!ordinaryApplied || !recoveryApplied) throw new Error("applied rows missing");
  const appliedText = `ordinary /same\n\n${REFERENCE}`;
  expect(ordinaryApplied.text).toBe(appliedText);
  const appliedAttachment = recoveryApplied.attachments[0];
  if (!appliedAttachment) throw new Error("applied recovery attachment missing");
  const appliedAttachmentBytes = await blobBytes(appliedAttachment.blob);

  const changed = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-B", true));
  expect(changed.state).toBe("expiry-pending");
  if (changed.state !== "expiry-pending" || !changed.receipt) throw new Error("missing fenced expiry");
  const receipt = changed.receipt;

  // ONLY deleteExpiry fails. The failed deletion must roll back: EVERY annotation
  // row set is unchanged, listing stays empty and composer text/bytes survive.
  const snapshotBeforeFault = await annotationSnapshot(f);
  await expect(f.repo.finishExpiry(receipt)).rejects.toThrow("delete fault");
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryApplied);
  const recoveryAfterFault = await f.outbox.getRecovery("rec-1");
  expect(recoveryAfterFault).toEqual(recoveryApplied);
  const attachmentAfterFault = recoveryAfterFault?.attachments[0];
  if (!attachmentAfterFault) throw new Error("recovery attachment missing after fault");
  expect(attachmentAfterFault).toMatchObject(attachmentIdentity);
  expect(await blobBytes(attachmentAfterFault.blob)).toEqual(appliedAttachmentBytes);
  expect(await annotationSnapshot(f)).toEqual(snapshotBeforeFault);

  // Restore storage, finish the same receipt, save a fresh B note, and replay the
  // old A cleanup: the complete annotation state must not change.
  failDeletion = false;
  await f.repo.finishExpiry(receipt);
  const bRead = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-B", true));
  expect(bRead.state).toBe("accepted");
  if (bRead.state !== "accepted") throw new Error("fresh B read not accepted");
  const bOperation = preparedAnnotationFromLease(bRead.lease, "B", "B note", f.destination, "rev-B", true);
  await f.repo.saveAnnotation(bOperation);
  const snapshotBeforeReplay = await annotationSnapshot(f);
  await f.repo.finishExpiry(receipt);
  expect(await annotationSnapshot(f)).toEqual(snapshotBeforeReplay);
  expect((await f.repo.listAnnotations(f.document)).map((view: AnnotationView) => view.operation.operationId)).toEqual([
    "B",
  ]);
  expect((await f.repo.handoff(f.document, "B")).state).toBe("applied");
  const bReference = REFERENCE.replace('"Keep this scope."', '"B note"').replace('"rev-A"', '"rev-B"');
  const ordinaryAfterB = await f.drafts.load(f.key);
  expect(ordinaryAfterB).toEqual({
    ...ordinaryApplied,
    text: `ordinary /same\n\n${REFERENCE}\n\n${bReference}`,
    token: { ...ordinaryApplied.token, sessionRevision: ordinaryApplied.token.sessionRevision + 1 },
  });
  const recoveryAfterB = await f.outbox.getRecovery("rec-1");
  expect(recoveryAfterB).toEqual(recoveryApplied);
  const attachmentAfterB = recoveryAfterB?.attachments[0];
  if (!attachmentAfterB) throw new Error("B recovery attachment missing");
  expect(await blobBytes(attachmentAfterB.blob)).toEqual(appliedAttachmentBytes);

  // Revert to rev-A through a real current read and finish that expiry: no old
  // note reappears, and applied text plus fresh bytes are unchanged.
  const reverted = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-A"));
  expect(reverted.state).toBe("expiry-pending");
  if (reverted.state !== "expiry-pending" || !reverted.receipt) throw new Error("missing revert fence");
  await f.repo.finishExpiry(reverted.receipt);
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryAfterB);
  const recoveryAfterRevert = await f.outbox.getRecovery("rec-1");
  expect(recoveryAfterRevert).toEqual(recoveryApplied);
  const attachmentAfterRevert = recoveryAfterRevert?.attachments[0];
  if (!attachmentAfterRevert) throw new Error("revert recovery attachment missing");
  expect(await blobBytes(attachmentAfterRevert.blob)).toEqual(appliedAttachmentBytes);

  // The final original-op replay stays terminal; bytes are re-read afterwards.
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
  const recoveryAfterReplay = await f.outbox.getRecovery("rec-1");
  expect(recoveryAfterReplay).toEqual(recoveryApplied);
  const attachmentAfterReplay = recoveryAfterReplay?.attachments[0];
  if (!attachmentAfterReplay) throw new Error("replay recovery attachment missing");
  expect(await blobBytes(attachmentAfterReplay.blob)).toEqual(appliedAttachmentBytes);
  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

it("typing at the final guard retries the SAME operation, and typing before ack wins", async () => {
  let f: Awaited<ReturnType<typeof fixture>>;
  let editAtGuard = true;
  let editAtAck = false;
  f = await fixture("typing", {
    beforeFinalGuard: () => {
      if (editAtGuard) {
        editAtGuard = false;
        f.source.acceptEdit({ text: "during", skillNames: [], mentions: [] });
      }
    },
    afterCommit: () => {
      if (editAtAck) f.source.acceptEdit({ text: "after ack boundary", skillNames: [], mentions: [] });
    },
  });
  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);

  // The guard edit advances the source generation while the append is before
  // writes, so the identified append stays pending and never commits its capture.
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("pending");
  await f.source.whenSettled();
  expect(f.source.getSnapshot().text).toBe("during");
  expect((await f.drafts.load(f.key))?.text).toBe("during");

  // Retry the SAME key/ID with the guard edit disabled: the original identified
  // append now commits exactly once.
  editAtAck = true;
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  await f.source.whenSettled();
  // The after-commit edit wins before acknowledgment, and persists.
  expect(f.source.getSnapshot().text).toBe("after ack boundary");
  expect((await f.drafts.load(f.key))?.text).toBe("after ack boundary");

  // Capture the complete settled state before replay.
  const ordinaryApplied = await f.drafts.load(f.key);
  const recoveryApplied = await f.outbox.getRecovery("rec-1");
  const sourceApplied = f.source.getSnapshot();
  const tokenApplied = f.source.getDraftToken();
  const operationsApplied = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  const operations = operationsApplied.filter((row) => row.operationKey === f.operationKey);
  expect(operations).toHaveLength(1);
  expect(operations[0]?.state).toBe("applied");

  // Replay does not edit again: no second revision or metadata change anywhere.
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
  await f.source.whenSettled();
  expect(await f.drafts.load(f.key)).toEqual(ordinaryApplied);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryApplied);
  expect(f.source.getSnapshot()).toEqual(sourceApplied);
  expect(f.source.getDraftToken()).toEqual(tokenApplied);
  expect(await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).toEqual(
    operationsApplied,
  );

  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

it("orders pending operations across files, duplicate save never allocates again", async () => {
  const f = await fixture("fifo");
  const other: DocumentKey = { ...f.document, path: "OTHER.md" };
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!recoveryBefore) throw new Error("fixture recovery row missing");
  const recoveryAttachment = recoveryBefore.attachments[0];
  if (!recoveryAttachment) throw new Error("fixture recovery attachment missing");
  const recoveryBytesBefore = await blobBytes(recoveryAttachment.blob);
  const ordinaryBefore = await f.drafts.load(f.key);
  if (!ordinaryBefore) throw new Error("fixture ordinary row missing");

  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);

  const later = await acceptedOperation(f.repo, other, "op-2", "second", f.destination);
  const second = await f.repo.saveAnnotation(later);
  const payloadsAfterSave = await rowsForTests<AnnotationPayloadRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_PAYLOADS_STORE,
  );
  const sequencesAfterSave = await rowsForTests<AnnotationSequenceRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_SEQUENCES_STORE,
  );
  // A duplicate save of op-2 reuses its receipt/sequence and allocates no payload
  // or sequence change.
  expect((await f.repo.saveAnnotation(later)).sessionSequence).toBe(second.sessionSequence);
  expect(await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).toEqual(
    payloadsAfterSave,
  );
  expect(await rowsForTests<AnnotationSequenceRow>(f.factory, f.databaseName, ANNOTATION_SEQUENCES_STORE)).toEqual(
    sequencesAfterSave,
  );

  const otherKey = annotationOperationKey(other.namespace, "op-2");

  // op-2 is later than the pending op-1, so its handoff stays pending without
  // touching either backing.
  expect((await f.repo.handoff(other, "op-2")).state).toBe("pending");
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  // Then op-1 and op-2 apply in session order.
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  expect((await f.repo.handoff(other, "op-2")).state).toBe("applied");

  const expectedText =
    `ordinary /same\n\n${REFERENCE}\n\n` +
    REFERENCE.replace("NOTES.md", "OTHER.md").replace('"Keep this scope."', '"second"');
  expect((await f.drafts.load(f.key))?.text).toBe(expectedText);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  const operationsApplied = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  expect(operationsApplied.find((row) => row.operationKey === f.operationKey)?.state).toBe("applied");
  expect(operationsApplied.find((row) => row.operationKey === otherKey)?.state).toBe("applied");

  // Replay both without a further revision or edit; the complete ordinary and
  // recovery rows and the operation rows are unchanged.
  const ordinaryApplied = await f.drafts.load(f.key);
  const recoveryApplied = await f.outbox.getRecovery("rec-1");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
  expect((await f.repo.handoff(other, "op-2")).state).toBe("already-applied");
  expect(await f.drafts.load(f.key)).toEqual(ordinaryApplied);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryApplied);
  expect(await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).toEqual(
    operationsApplied,
  );
  const replayedRecovery = await f.outbox.getRecovery("rec-1");
  if (!replayedRecovery) throw new Error("replayed recovery row missing");
  const replayedAttachment = replayedRecovery.attachments[0];
  if (!replayedAttachment) throw new Error("replayed recovery attachment missing");
  expect(await blobBytes(replayedAttachment.blob)).toEqual(recoveryBytesBefore);

  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

// ---------------------------------------------------------------------------
// Task 3 Step 3a: immediate suppression while an annotation append is
// outstanding, two-ticket suppression ownership, and stale-only recovery.
// ---------------------------------------------------------------------------

type ReadOutcome = { ok: true; result: AcceptedReadResult } | { ok: false; error: unknown };

// Every started real read is joined through this observation so a controlled
// fence rejection is an explicit outcome, never an unhandled rejection.
function observeRead(started: Promise<AcceptedReadResult>): Promise<ReadOutcome> {
  return started.then(
    (result) => ({ ok: true as const, result }),
    (error: unknown) => ({ ok: false as const, error }),
  );
}

// Assert the read resolved, surfacing the observed rejection in the failure text
// instead of hiding it as success.
function expectResolvedRead(outcome: ReadOutcome): AcceptedReadResult {
  expect(outcome.ok ? "resolved" : `rejected: ${String((outcome.error as Error).message)}`).toBe("resolved");
  if (!outcome.ok) throw outcome.error;
  return outcome.result;
}

type PendingOperationRow = Extract<AnnotationOperationRow, { state: "pending" }>;

// The terminal applied receipt is the pending row with its payload binding
// stripped and the draft revision advanced once, matching the adapter's applied
// write, so a full-receipt comparison never uses the produced row as its oracle.
function terminalReceiptFromPending(row: PendingOperationRow, draftRevision: number): AnnotationOperationRow {
  return {
    operationKey: row.operationKey,
    namespaceKey: row.namespaceKey,
    namespaceIncarnation: row.namespaceIncarnation,
    documentKey: row.documentKey,
    sessionKey: row.sessionKey,
    sessionSequence: row.sessionSequence,
    operationId: row.operationId,
    epoch: row.epoch,
    state: "applied",
    draftRevision,
  };
}

// The literal reference text and input the adapter appends to the selected
// backing, built from the same literal REFERENCE the rest of the file uses.
function expectedAppendedRecoveryInput(): unknown[] {
  return [
    {
      type: "text",
      text: `recovery /plan [image 1]\n\n${REFERENCE}`,
      mentions: [{ kind: "command", name: "plan", offset: 9 }],
    },
    { type: "image", mediaType: "image/png", data: "AQ==", name: "one.png" },
    { type: "command", name: "plan" },
  ];
}

// A. Outstanding append: draft/recovery x fence success/failure.
it.each([
  ["draft", false],
  ["draft", true],
  ["recovery", false],
  ["recovery", true],
] as const)("suppresses %s before a delayed/failed fence, failure=%s", async (backing, failFence) => {
  let f: Awaited<ReturnType<typeof fixture>>;
  let ticket: ReadTicket;
  let changed: Promise<ReadOutcome> | undefined;
  let startRead = false;
  let fail = failFence;
  f = await fixture(`overlap-${backing}-${failFence}`, {
    onAppendReadsIssued: () => {
      if (startRead) {
        startRead = false;
        changed = observeRead(f.repo.acceptRead(ticket, docContent(SOURCE, "rev-B")));
      }
    },
    beforeCommit: (operation) => {
      if (operation === "expiry" && fail) throw new Error("fence fault");
    },
  });
  // Join the changed read at fixture close if an assertion or operation exits
  // before the explicit await below.
  cleanupAfterTest(async () => {
    await changed?.catch(() => undefined);
  });
  // Observational spies installed only after fixture creation; they observe real
  // mutations and never replace results.
  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);
  if (backing === "recovery") f.source.setActiveRecoveryId("rec-1");
  const preparation = await f.source.prepareAnnotation();
  if (!preparation) throw new Error("missing preparation");
  ticket = await f.repo.beginRead(f.document);
  const ordinary = await f.drafts.load(f.key);
  const recovery = await f.outbox.getRecovery("rec-1");
  const baselineAttachment = recovery?.attachments[0];
  if (!baselineAttachment) throw new Error("fixture recovery attachment missing");
  expect(baselineAttachment).toMatchObject(attachmentIdentity);
  const baselineBytes = await blobBytes(baselineAttachment.blob);
  const snapshot = f.source.getSnapshot();
  const generationBefore = f.source.getLocalGeneration();
  const backingBefore = f.source.getRuntimeBacking();
  const tokenBefore = f.source.getDraftToken();
  const publication = vi.fn();
  const unsubscribe = f.source.subscribe(publication);
  cleanupAfterTest(() => unsubscribe());
  startRead = true;
  const handedOff = await f.repo.handoff(f.document, "op-1");
  if (!changed) throw new Error("changed read never started");
  const changedOutcome = await changed;
  // The joined read is asserted explicitly: a controlled fence fault must still
  // resolve to expiry-pending, never be swallowed as success.
  const joinedState = expectResolvedRead(changedOutcome).state;
  expect(joinedState).toBe("expiry-pending");
  expect(handedOff.state).toBe("cancelled");
  expect(await f.drafts.load(f.key)).toEqual(ordinary); // includes unchanged session revision
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recovery);
  expect(f.source.getSnapshot()).toEqual(snapshot);
  expect(f.source.ownsPreparation(preparation)).toBe(true);
  // Explicit getter preservation across the fence: generation, backing and token
  // must not have advanced while the changed read was suppressed.
  expect(f.source.getLocalGeneration()).toBe(generationBefore);
  expect(f.source.getRuntimeBacking()).toEqual(backingBefore);
  expect(f.source.getDraftToken()).toEqual(tokenBefore);
  // Real recovery attachment identity and bytes survive the fence.
  const fencedAttachment = (await f.outbox.getRecovery("rec-1"))?.attachments[0];
  if (!fencedAttachment) throw new Error("post-fence recovery attachment missing");
  expect(fencedAttachment).toMatchObject(attachmentIdentity);
  expect(await blobBytes(fencedAttachment.blob)).toEqual(baselineBytes);
  expect(publication).not.toHaveBeenCalled();
  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
  const receipt = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationKey === f.operationKey);
  expect(receipt?.state).toBe(failFence ? "pending" : "cancelled");
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  fail = false;
  await f.repo.retryPending("storage-recovered");
  const recovered = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationKey === f.operationKey);
  expect(recovered?.state).toBe("cancelled");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("cancelled");
  expect(await f.drafts.load(f.key)).toEqual(ordinary);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recovery);
  // The healed retry preserves the same source getters and attachment bytes.
  expect(f.source.getLocalGeneration()).toBe(generationBefore);
  expect(f.source.getRuntimeBacking()).toEqual(backingBefore);
  expect(f.source.getDraftToken()).toEqual(tokenBefore);
  const healedAttachment = (await f.outbox.getRecovery("rec-1"))?.attachments[0];
  if (!healedAttachment) throw new Error("post-retry recovery attachment missing");
  expect(healedAttachment).toMatchObject(attachmentIdentity);
  expect(await blobBytes(healedAttachment.blob)).toEqual(baselineBytes);
  expect(enqueue).not.toHaveBeenCalled();
  expect(resend).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

// B. Two-ticket suppression ownership: draft/recovery x fence success/failure.
it.each([
  ["draft", false],
  ["draft", true],
  ["recovery", false],
  ["recovery", true],
] as const)("transfers unconfirmed suppression to current %s T2, fence failure=%s", async (backing, failFence) => {
  let f: Awaited<ReturnType<typeof fixture>>;
  let t1: ReadTicket;
  let t2: ReadTicket | undefined;
  let oldRead: Promise<ReadOutcome> | undefined;
  let newRead: Promise<ReadOutcome> | undefined;
  let armed = false;
  let fail = failFence;
  const trace: string[] = [];
  async function observePendingWindow() {
    const handoff = await f.repo.handoff(f.document, "op-1");
    const recoveryRow = await f.outbox.getRecovery("rec-1");
    const attachment = recoveryRow?.attachments[0];
    if (!attachment) throw new Error("pending-window recovery attachment missing");
    return {
      handoff,
      ordinary: await f.drafts.load(f.key),
      recovery: recoveryRow,
      attachmentBytes: await blobBytes(attachment.blob),
      receipts: await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE),
      expiries: await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE),
      snapshot: f.source.getSnapshot(),
      ownsPreparation: f.source.ownsPreparation(preparation),
      generation: f.source.getLocalGeneration(),
      backing: f.source.getRuntimeBacking(),
      token: f.source.getDraftToken(),
      publications: publication.mock.calls.length,
    };
  }
  let pendingWindow: Awaited<ReturnType<typeof observePendingWindow>> | undefined;
  f = await fixture(`tickets-${backing}-${failFence}`, {
    onReadBoundary: (event) => {
      if (event.phase === "begin-read-issued" && armed) {
        armed = false;
        trace.push("T2 issued");
        oldRead = observeRead(f.repo.acceptRead(t1, docContent(SOURCE, "rev-B"))).then((outcome) => {
          if (outcome.ok) trace.push(`T1 ${outcome.result.state}`);
          return outcome;
        });
      } else if (event.phase === "ticket-remembered" && oldRead && !newRead) {
        t2 = event.ticket;
        trace.push("T2 remembered");
        newRead = observeRead(f.repo.acceptRead(t2, docContent(SOURCE, "rev-B")));
        trace.push("T2 entered");
      }
    },
    beforeExpiryFence: async (ticket) => {
      if (ticket.requestId !== t2?.requestId) return;
      trace.push("T2 validated");
      pendingWindow = await observePendingWindow();
    },
    beforeCommit: (operation) => {
      if (operation === "expiry" && fail) throw new Error("fence fault");
    },
  });
  // Join both started reads at fixture close if an assertion or operation exits
  // before the explicit awaits below.
  cleanupAfterTest(async () => {
    await oldRead?.catch(() => undefined);
    await newRead?.catch(() => undefined);
  });
  // Observational spies installed only after fixture creation; they observe real
  // mutations and never replace results.
  const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
  const resend = vi.spyOn(f.outbox, "resendRecovery");
  const update = vi.spyOn(f.outbox, "updateRecoveryInput");
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);
  if (backing === "recovery") f.source.setActiveRecoveryId("rec-1");
  const prepared = await f.source.prepareAnnotation();
  if (!prepared) throw new Error("missing preparation");
  const preparation = prepared; // immutable capture shared by both deferred observers
  t1 = await f.repo.beginRead(f.document);
  const ordinary = await f.drafts.load(f.key);
  const recovery = await f.outbox.getRecovery("rec-1");
  const baselineAttachment = recovery?.attachments[0];
  if (!baselineAttachment) throw new Error("fixture recovery attachment missing");
  expect(baselineAttachment).toMatchObject(attachmentIdentity);
  const baselineBytes = await blobBytes(baselineAttachment.blob);
  const receipts = await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE);
  expect(receipts.find((row) => row.operationKey === f.operationKey)?.state).toBe("pending");
  const snapshot = f.source.getSnapshot();
  const publication = vi.fn();
  const unsubscribe = f.source.subscribe(publication);
  cleanupAfterTest(() => unsubscribe());
  async function expectOriginalComposition() {
    expect(await f.drafts.load(f.key)).toEqual(ordinary); // full row includes unchanged session revision
    const currentRecovery = await f.outbox.getRecovery("rec-1");
    expect(currentRecovery).toEqual(recovery);
    const attachment = currentRecovery?.attachments[0];
    if (!attachment) throw new Error("recovery attachment missing");
    expect(attachment).toMatchObject(attachmentIdentity);
    expect(await blobBytes(attachment.blob)).toEqual(baselineBytes);
    expect(f.source.getSnapshot()).toEqual(snapshot);
    expect(f.source.ownsPreparation(preparation)).toBe(true);
    expect(f.source.getLocalGeneration()).toBe(preparation.localGeneration);
    expect(f.source.getRuntimeBacking()).toEqual(preparation.backing);
    expect(f.source.getDraftToken()).toEqual(preparation.token);
    expect(publication).not.toHaveBeenCalled();
    expect(enqueue).not.toHaveBeenCalled();
    expect(resend).not.toHaveBeenCalled();
    expect(update).not.toHaveBeenCalled();
    expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
  }
  armed = true;
  const issued = await f.repo.beginRead(f.document);
  expect(t2?.requestId).toBe(issued.requestId);
  if (!oldRead || !newRead) throw new Error("two-ticket observation did not start both real reads");
  const oldOutcome = await oldRead;
  const newOutcome = await newRead;
  expect(expectResolvedRead(oldOutcome).state).toBe("stale-ticket");
  expect(expectResolvedRead(newOutcome).state).toBe("expiry-pending");
  expect(trace).toEqual(["T2 issued", "T2 remembered", "T2 entered", "T1 stale-ticket", "T2 validated"]);
  if (!pendingWindow) throw new Error("current validation never reached the outside-transaction fence boundary");
  expect(pendingWindow.handoff.state).toBe("cancelled");
  expect(pendingWindow.ordinary).toEqual(ordinary);
  expect(pendingWindow.recovery).toEqual(recovery);
  expect(pendingWindow.attachmentBytes).toEqual(baselineBytes);
  expect(pendingWindow.receipts).toEqual(receipts);
  expect(pendingWindow.expiries).toEqual([]);
  expect(pendingWindow.snapshot).toEqual(snapshot);
  expect(pendingWindow.ownsPreparation).toBe(true);
  expect(pendingWindow.generation).toBe(preparation.localGeneration);
  expect(pendingWindow.backing).toEqual(preparation.backing);
  expect(pendingWindow.token).toEqual(preparation.token);
  expect(pendingWindow.publications).toBe(0);
  await expectOriginalComposition();
  const fenced = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationKey === f.operationKey);
  expect(fenced?.state).toBe(failFence ? "pending" : "cancelled");
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  if (failFence) {
    // A confirmed entry is not transferred by a later changed ticket, nor erased
    // by reversion to rev-A.
    for (const revision of ["rev-C", "rev-A"]) {
      const read = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, revision));
      expect(read.state).toBe("expiry-pending");
      expect((await f.repo.handoff(f.document, "op-1")).state).toBe("cancelled");
      expect(
        await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE),
      ).toEqual(receipts);
      await expectOriginalComposition();
    }
  }
  fail = false;
  await f.repo.retryPending("storage-recovered");
  const recovered = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationKey === f.operationKey);
  expect(recovered?.state).toBe("cancelled");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("cancelled");
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  await expectOriginalComposition();
});

// C. Stale-only recovery: draft/recovery.
it.each(["draft", "recovery"] as const)(
  "removes only stale T1's own unconfirmed suppression on %s",
  async (backing) => {
    let f: Awaited<ReturnType<typeof fixture>>;
    let t1: ReadTicket;
    let armed = false;
    let oldRead: Promise<ReadOutcome> | undefined;
    f = await fixture(`stale-only-${backing}`, {
      onReadBoundary: (event) => {
        if (event.phase === "begin-read-issued" && armed) {
          armed = false;
          oldRead = observeRead(f.repo.acceptRead(t1, docContent(SOURCE, "rev-B")));
        }
      },
    });
    // Join the started stale read at fixture close if an assertion exits early.
    cleanupAfterTest(async () => {
      await oldRead?.catch(() => undefined);
    });
    // Observational spies installed only after fixture creation; they observe real
    // mutations and never replace results.
    const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
    const resend = vi.spyOn(f.outbox, "resendRecovery");
    const update = vi.spyOn(f.outbox, "updateRecoveryInput");
    const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
    expect(outboxBefore).toEqual([]);
    if (backing === "recovery") f.source.setActiveRecoveryId("rec-1");
    const preparation = await f.source.prepareAnnotation();
    if (!preparation) throw new Error("missing preparation");
    t1 = await f.repo.beginRead(f.document);
    const ordinary = await f.drafts.load(f.key);
    const recovery = await f.outbox.getRecovery("rec-1");
    if (!ordinary || !recovery) throw new Error("fixture baseline rows missing");
    const baselineAttachment = recovery.attachments[0];
    if (!baselineAttachment) throw new Error("fixture recovery attachment missing");
    expect(baselineAttachment).toMatchObject(attachmentIdentity);
    const baselineBytes = await blobBytes(baselineAttachment.blob);
    const receipts = await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE);
    const pendingRow = receipts.find((row) => row.operationKey === f.operationKey);
    if (pendingRow?.state !== "pending") throw new Error("fixture pending receipt missing");
    const sourceBefore = f.source.getSnapshot();
    // The full expected token advances exactly once from the PRE-action token, and
    // the full applied receipt is derived from the pending row independently.
    const advancedToken = { ...ordinary.token, sessionRevision: ordinary.token.sessionRevision + 1 };
    const expectedReceipt = terminalReceiptFromPending(pendingRow, advancedToken.sessionRevision);
    const expectedSourceText =
      backing === "draft" ? `ordinary /same\n\n${REFERENCE}` : `recovery /plan [image 1]\n\n${REFERENCE}`;
    armed = true;
    const t2 = await f.repo.beginRead(f.document); // no changed acceptRead(T2)
    expect(t2.requestId).not.toBe(t1.requestId);
    if (!oldRead) throw new Error("stale-only observation did not start the real read");
    const oldOutcome = await oldRead;
    expect(expectResolvedRead(oldOutcome).state).toBe("stale-ticket");
    expect(await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).toEqual(
      receipts,
    );
    expect(await rowsForTests<AnnotationExpiryRow>(f.factory, f.databaseName, ANNOTATION_EXPIRY_STORE)).toEqual([]);
    expect(f.source.ownsPreparation(preparation)).toBe(true);
    expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
    // Complete post-apply state: full rows, full receipt, source getters and real
    // attachment bytes on BOTH backings.
    const afterOrdinary = await f.drafts.load(f.key);
    const afterRecovery = await f.outbox.getRecovery("rec-1");
    if (!afterOrdinary || !afterRecovery) throw new Error("post-apply rows missing");
    if (backing === "draft") {
      expect(afterOrdinary).toEqual({
        ...ordinary,
        text: `ordinary /same\n\n${REFERENCE}`,
        token: advancedToken,
      });
      expect(afterRecovery).toEqual(recovery);
    } else {
      // The ordinary row is untouched apart from the session token advance, derived
      // from the pre-action token, never from the post-action row.
      expect(afterOrdinary).toEqual({ ...ordinary, token: advancedToken });
      expect(afterRecovery).toEqual({
        ...recovery,
        composerText: `recovery /plan [image 1]\n\n${REFERENCE}`,
        payload: { ...recovery.payload, input: expectedAppendedRecoveryInput() },
        optimisticDisplay: { method: "turn/start", input: expectedAppendedRecoveryInput() },
      });
    }
    const afterAttachment = afterRecovery.attachments[0];
    if (!afterAttachment) throw new Error("post-apply recovery attachment missing");
    expect(afterAttachment).toMatchObject(attachmentIdentity);
    expect(await blobBytes(afterAttachment.blob)).toEqual(baselineBytes);
    const afterReceipt = (
      await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
    ).find((row) => row.operationKey === f.operationKey);
    expect(afterReceipt).toEqual(expectedReceipt);
    expect(afterOrdinary.token).toEqual(advancedToken);
    expect(f.source.getSnapshot()).toEqual({ ...sourceBefore, text: expectedSourceText });
    expect(f.source.getDraftToken()).toEqual(advancedToken);
    expect(enqueue).not.toHaveBeenCalled();
    expect(resend).not.toHaveBeenCalled();
    expect(update).not.toHaveBeenCalled();
    expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
    // Replay is terminal: re-read every actual row, source and receipt afterwards
    // and prove no further revision advance or mutation.
    expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
    const replayedOrdinary = await f.drafts.load(f.key);
    const replayedRecovery = await f.outbox.getRecovery("rec-1");
    const replayedReceipt = (
      await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
    ).find((row) => row.operationKey === f.operationKey);
    expect(replayedOrdinary).toEqual(afterOrdinary);
    expect(replayedRecovery).toEqual(afterRecovery);
    expect(replayedReceipt).toEqual(afterReceipt);
    expect(f.source.getSnapshot()).toEqual({ ...sourceBefore, text: expectedSourceText });
    expect(f.source.getDraftToken()).toEqual(advancedToken);
    const replayedAttachment = replayedRecovery?.attachments[0];
    if (!replayedAttachment) throw new Error("replayed recovery attachment missing");
    expect(replayedAttachment).toMatchObject(attachmentIdentity);
    expect(await blobBytes(replayedAttachment.blob)).toEqual(baselineBytes);
  },
);

// ---------------------------------------------------------------------------
// Task 3 Step 4: save/fence fault retry, history-clear cache isolation and
// namespace-reset preservation.
// ---------------------------------------------------------------------------

// 1. Save/fence failure and same-ID retry.
it("save/fence failure is honest and retry keeps the same operation id", async () => {
  let failSave = false;
  let failFence = false;
  const f = await fixture("fault", {
    beforeCommit: (operation) => {
      if ((operation === "save" && failSave) || (operation === "expiry" && failFence)) {
        throw new Error("storage fault");
      }
    },
  });

  // The accepted lease for the save attempt is established BEFORE any fault is
  // enabled; only the rejected attempt is under test.
  const pending = await acceptedOperation(f.repo, f.document, "same-id", "note", f.destination);
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const recoveryAttachmentBefore = recoveryBefore.attachments[0];
  if (!recoveryAttachmentBefore) throw new Error("fixture recovery attachment missing");
  const recoveryBytesBefore = await blobBytes(recoveryAttachmentBefore.blob);
  const rowsBefore = await annotationRows(f);
  const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);
  expect(outboxBefore).toEqual([]);
  const sameIdKey = annotationOperationKey(f.document.namespace, "same-id");

  // The rejected save is a whole-transaction abort: operation/payload/sequence
  // rows and both complete backing rows stay byte-identical to the baseline.
  failSave = true;
  await expect(f.repo.saveAnnotation(pending)).rejects.toThrow("storage fault");
  expect(await annotationRows(f)).toEqual(rowsBefore);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  // Recovery: the SAME immutable operation saves once, allocating exactly one
  // payload, one receipt and one sequence.
  failSave = false;
  const receipt = await f.repo.saveAnnotation(pending);
  expect(receipt.operationId).toBe("same-id");
  const savedRows = await annotationRows(f);
  expect(savedRows.payloads.filter((row) => row.payloadKey === sameIdKey)).toHaveLength(1);
  expect(savedRows.operations.filter((row) => row.operationKey === sameIdKey)).toHaveLength(1);
  expect(savedRows.sequences[0]?.lastSequence).toBe((rowsBefore.sequences[0]?.lastSequence ?? 0) + 1);

  // A duplicate save of the same immutable operation returns its receipt and
  // leaves those actual rows unchanged.
  expect((await f.repo.saveAnnotation(pending)).sessionSequence).toBe(receipt.sessionSequence);
  expect(await annotationRows(f)).toEqual(savedRows);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  // The expiry fence for the changed rev-C fails: an honest expiry-pending with
  // no committed receipt and every pending receipt still pending.
  const fenceBaseline = {
    ordinary: await f.drafts.load(f.key),
    recovery: await f.outbox.getRecovery("rec-1"),
    source: f.source.getSnapshot(),
  };
  failFence = true;
  const result = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-C"));
  expect(result.state).toBe("expiry-pending");
  if (result.state !== "expiry-pending") throw new Error("changed rev-C read was not fenced");
  expect(result.receipt).toBeUndefined();
  const failedFence = await annotationSnapshot(f);
  for (const operationId of ["op-1", "same-id"]) {
    expect(
      failedFence.operations.find(
        (row) => row.operationKey === annotationOperationKey(f.document.namespace, operationId),
      )?.state,
    ).toBe("pending");
  }
  expect(failedFence.expiries).toEqual([]);

  // Clearing the fault, the real retry fences/cancels the pending work durably:
  // cancelled receipts and a real fenced expiry row. Listing is empty and both
  // complete composition rows, source values and fresh attachment bytes stay
  // unchanged.
  failFence = false;
  await f.repo.retryPending("reader-open");
  const healed = await annotationSnapshot(f);
  for (const operationId of ["op-1", "same-id"]) {
    expect(
      healed.operations.find((row) => row.operationKey === annotationOperationKey(f.document.namespace, operationId))
        ?.state,
    ).toBe("cancelled");
  }
  expect(healed.expiries).toHaveLength(1);
  expect(healed.expiries[0]?.receipt.phase).toBe("fenced");
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  expect(await f.drafts.load(f.key)).toEqual(fenceBaseline.ordinary);
  const recoveryAfterFence = await f.outbox.getRecovery("rec-1");
  expect(recoveryAfterFence).toEqual(fenceBaseline.recovery);
  expect(f.source.getSnapshot()).toEqual(fenceBaseline.source);
  const recoveryAttachmentAfter = recoveryAfterFence?.attachments[0];
  if (!recoveryAttachmentAfter) throw new Error("post-retry recovery attachment missing");
  expect(recoveryAttachmentAfter).toMatchObject(attachmentIdentity);
  expect(await blobBytes(recoveryAttachmentAfter.blob)).toEqual(recoveryBytesBefore);
  expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);
});

// 2. History clear retains all 101 new files.
it("history clear retains annotations in ALL 101 files", async () => {
  // Reset the thread singleton first so cache epoch/channel/timer state from an
  // unrelated test cannot leak into this one.
  resetThreadsStoreForTests();
  const f = await fixture("cache");
  const cache = new SessionCacheIndexedDB({ indexedDB: f.factory, databaseName: "history-cache" });
  setSessionCacheAdapterForTests(cache);
  cleanupAfterTest(() => {
    setSessionCacheAdapterForTests(undefined);
    resetThreadsStoreForTests();
    cache.close();
  });

  const notes = Array.from({ length: 101 }, (_, n) => ({
    document: { ...f.document, path: `notes-${n}.md` } as DocumentKey,
    operationId: `note-${n}`,
    note: `note ${n}`,
    payloadKey: annotationOperationKey(f.document.namespace, `note-${n}`),
  }));
  for (const entry of notes) {
    await f.repo.saveAnnotation(
      await acceptedOperation(f.repo, entry.document, entry.operationId, entry.note, f.destination),
    );
  }

  // Every exact note, its payload and its receipt exist before clearing.
  const payloadsBefore = (await annotationRows(f)).payloads;
  for (const entry of notes) {
    const views = await f.repo.listAnnotations(entry.document);
    expect(views.map((view) => view.operation.note)).toEqual([entry.note]);
    expect(views[0]?.receipt.operationId).toBe(entry.operationId);
    expect(payloadsBefore.some((row) => row.payloadKey === entry.payloadKey)).toBe(true);
  }
  // The original fixture note is present too.
  expect((await f.repo.listAnnotations(f.document)).map((view) => view.operation.operationId)).toEqual(["op-1"]);

  const snapshotBefore = await annotationSnapshot(f);
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");

  // The production clear runs through the real adapter seam, never a mock.
  expect((await clearCachedSessions()).committed).toBe(true);

  // Every exact note is still listed afterwards, not just an aggregate count.
  for (const entry of notes) {
    const views = await f.repo.listAnnotations(entry.document);
    expect(views.map((view) => view.operation.note)).toEqual([entry.note]);
    expect(views[0]?.receipt.operationId).toBe(entry.operationId);
  }
  expect((await f.repo.listAnnotations(f.document)).map((view) => view.operation.operationId)).toEqual(["op-1"]);
  // The complete annotation stores and both backing rows are unchanged by a clear.
  expect(await annotationSnapshot(f)).toEqual(snapshotBefore);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
});

// 3-5. Reset preservation and useful writing returns: live, closed-draft,
// closed-recovery.
it.each(["live", "closed-draft", "closed-recovery"] as const)(
  "reset preserves %s and useful writing returns",
  async (lifetime) => {
    const f = await fixture(`reset-${lifetime}`);
    if (lifetime === "closed-recovery") f.source.setActiveRecoveryId("rec-1");
    // Stage one real live attachment before the submission snapshot, and carry
    // its identity across reset.
    const liveAttachment = { marker: 1, name: "live.png", mediaType: "image/png", data: "AQ==", pending: false };
    if (lifetime === "live") f.source.attachments.setState({ items: [liveAttachment] });
    const submitted = await f.source.submissionDraft();
    const ordinary = await f.drafts.load(f.key);
    const recovery = await f.outbox.getRecovery("rec-1");
    if (!ordinary) throw new Error("missing ordinary row");
    if (!recovery) throw new Error("missing recovery row");
    const recoveryAttachmentBefore = recovery.attachments[0];
    if (!recoveryAttachmentBefore) throw new Error("fixture recovery attachment missing");
    const recoveryBytesBefore = await blobBytes(recoveryAttachmentBefore.blob);
    const oldIncarnation = ordinary.token.namespaceIncarnation;
    // The approved captured key/token guard, deliberately authorized: refusal
    // after reset must come from the namespace fence, not a disposed callback.
    const guard: RecoveryWriteGuard = { key: f.key, token: ordinary.token, stillAuthorized: () => true };
    const rejectOldRecovery = async () => {
      expect(
        await f.outbox.updateRecoveryInput("rec-1", buildInput("stale"), recovery.attachments, "stale", [], guard),
      ).toBeUndefined();
      expect(await f.outbox.discardRecovery("rec-1", () => true, guard)).toBe(false);
      // The stale-resend case intentionally CALLS resendRecovery.
      expect(await f.outbox.resendRecovery("rec-1", recovery, guard)).toBeUndefined();
    };
    expect(guard.stillAuthorized()).toBe(true);
    const liveSnapshot = f.source.getSnapshot();
    const liveGeneration = f.source.getLocalGeneration();
    const liveBacking = f.source.getRuntimeBacking();
    const liveToken = f.source.getDraftToken();
    if (lifetime !== "live") f.source.dispose();

    const reset = await f.repo.resetOwnedNamespace(f.key.namespace, oldIncarnation);
    expect(reset.committed).toBe(true);
    const newIncarnation = reset.incarnation;
    expect(typeof newIncarnation).toBe("string");
    expect(newIncarnation.length).toBeGreaterThan(0);
    expect(newIncarnation).not.toBe(oldIncarnation);

    // BEFORE any lazy draft load: old cleanup cannot clear or write.
    expect((await f.drafts.clearSubmitted(f.key, submitted)).cleared).toBe(false);
    expect(
      (await f.drafts.commitEdit(f.key, ordinary.token, { text: "stale", skillNames: [] }, () => true)).state,
    ).toBe("conflict");
    await rejectOldRecovery();

    if (lifetime === "live") {
      expect(f.source.getSnapshot()).toEqual(liveSnapshot);
      expect(f.source.getLocalGeneration()).toBe(liveGeneration);
      expect(f.source.getRuntimeBacking()).toEqual(liveBacking);
      expect(f.source.getDraftToken()).toEqual(liveToken);
      expect(f.source.attachments.getState().items[0]).toBe(liveAttachment);
    }

    // Lazy load re-tokens ONLY the incarnation; the entire ordinary row survives
    // with its numeric revision unchanged.
    const loaded = await f.drafts.load(f.key);
    expect(loaded).toEqual({
      ...ordinary,
      token: { ...ordinary.token, namespaceIncarnation: newIncarnation },
    });
    expect(await f.outbox.getRecovery("rec-1")).toEqual(recovery);

    // Repeat the old refusals after the lazy load; neither backing nor outbox moved.
    expect((await f.drafts.clearSubmitted(f.key, submitted)).cleared).toBe(false);
    expect(
      (await f.drafts.commitEdit(f.key, ordinary.token, { text: "stale", skillNames: [] }, () => true)).state,
    ).toBe("conflict");
    await rejectOldRecovery();
    expect(await f.drafts.load(f.key)).toEqual(loaded);
    const recoveryAfterRefusals = await f.outbox.getRecovery("rec-1");
    expect(recoveryAfterRefusals).toEqual(recovery);
    const recoveryAttachmentAfter = recoveryAfterRefusals?.attachments[0];
    if (!recoveryAttachmentAfter) throw new Error("post-refusal recovery attachment missing");
    expect(recoveryAttachmentAfter).toMatchObject(attachmentIdentity);
    expect(await blobBytes(recoveryAttachmentAfter.blob)).toEqual(recoveryBytesBefore);
    expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual([]);

    // Old operation handoff is cancelled and the old note is gone.
    expect((await f.repo.handoff(f.document, "op-1")).state).toBe("cancelled");
    expect(await f.repo.listAnnotations(f.document)).toEqual([]);

    // A fresh source on the real APIs gets useful writing back.
    const source =
      lifetime === "live"
        ? f.source
        : createComposerSourceState("root", { repository: f.drafts, key: f.key, outbox: f.outbox });
    cleanupAfterTest(() => source.dispose());
    await source.ready();
    if (lifetime === "closed-recovery") source.setActiveRecoveryId("rec-1");
    await source.prepareAnnotation();
    if (lifetime === "closed-recovery") {
      await source.queueRecoveryPersistence("rec-1", "fresh recovery", [], [], [], []);
    } else {
      source.acceptEdit({ text: "fresh", skillNames: [], mentions: [] });
      await source.whenSettled();
    }
    const freshOrdinaryBefore = await f.drafts.load(f.key);
    const freshRecoveryBefore = await f.outbox.getRecovery("rec-1");
    if (!freshOrdinaryBefore || !freshRecoveryBefore) throw new Error("fresh baseline rows missing");
    if (lifetime === "closed-recovery") expect(freshRecoveryBefore.attachments).toEqual([]);
    const destination: DestinationProvenance = {
      kind: "browser-origin",
      paneId: "pane-fresh",
      compositionOwnerId: source.sourceId,
    };
    const repo = createDocumentAnnotationRepository({
      indexedDB: f.factory,
      databaseName: f.databaseName,
      drafts: f.drafts,
      targetFor: (origin, doc) =>
        origin.kind === "browser-origin" &&
        origin.paneId === "pane-fresh" &&
        origin.compositionOwnerId === source.sourceId &&
        doc.sessionRef === f.key.sessionRef &&
        doc.namespace.kind === "browser" &&
        doc.namespace.origin === f.key.namespace.origin &&
        source.alive
          ? source
          : null,
    });
    cleanupAfterTest(() => repo.close());
    await repo.saveAnnotation(await acceptedOperation(repo, f.document, "fresh-id", "Keep this scope.", destination));
    expect((await repo.handoff(f.document, "fresh-id")).state).toBe("applied");
    const freshInput = [{ type: "text", text: `fresh recovery\n\n${REFERENCE}` }];
    if (lifetime === "closed-recovery") {
      const freshRecoveryAfter = await f.outbox.getRecovery("rec-1");
      expect(freshRecoveryAfter).toEqual({
        ...freshRecoveryBefore,
        composerText: `fresh recovery\n\n${REFERENCE}`,
        payload: { ...freshRecoveryBefore.payload, input: freshInput },
        optimisticDisplay: { method: "turn/start", input: freshInput },
      });
      expect(freshRecoveryAfter?.attachments).toEqual([]);
      const freshOrdinaryAfter = await f.drafts.load(f.key);
      expect(freshOrdinaryAfter).toEqual({
        ...freshOrdinaryBefore,
        token: { ...freshOrdinaryBefore.token, sessionRevision: freshOrdinaryBefore.token.sessionRevision + 1 },
      });
      expect(freshOrdinaryAfter?.text).toBe("ordinary /same");
    } else {
      const freshOrdinaryAfter = await f.drafts.load(f.key);
      expect(freshOrdinaryAfter).toEqual({
        ...freshOrdinaryBefore,
        text: `fresh\n\n${REFERENCE}`,
        token: { ...freshOrdinaryBefore.token, sessionRevision: freshOrdinaryBefore.token.sessionRevision + 1 },
      });
      expect(await f.outbox.getRecovery("rec-1")).toEqual(freshRecoveryBefore);
    }
    source.dispose();
    repo.close();
  },
);

// 6. Another namespace remains untouched by an owned reset.
it("owned reset leaves another namespace's payload/head/value/token untouched", async () => {
  const f = await fixture("isolated");
  const otherKey = browserDraftKey("other", "https://other.test");
  const otherDoc: DocumentKey = { ...otherKey, path: "OTHER.md" };
  await f.drafts.seedDraft(otherKey, { text: "other work", skillNames: [], mentions: [] });
  await f.repo.saveAnnotation(
    await acceptedOperation(f.repo, otherDoc, "other-id", "other note", { kind: "browser-restored-session" }),
  );

  const otherNamespaceKey = annotationNamespaceKey(otherKey.namespace);
  const otherDocumentKey = annotationDocumentKey(otherDoc);
  const otherSessionKey = serializeSessionKey(otherKey);
  const isOther = <T extends { documentKey?: string }>(row: T): boolean => row.documentKey === otherDocumentKey;
  const isOtherExpiry = (row: AnnotationExpiryRow): boolean =>
    JSON.stringify(row.receipt.key) === JSON.stringify(otherDoc);

  // Capture the other namespace's complete draft/token, listing and every durable
  // annotation row before the owned reset.
  const value = await f.drafts.load(otherKey);
  const notes = await f.repo.listAnnotations(otherDoc);
  const snapshotBefore = await annotationSnapshot(f);
  const otherNamespacesBefore = (
    await rowsForTests<NamespaceRow>(f.factory, f.databaseName, ANNOTATION_NAMESPACES_STORE)
  ).filter((row) => row.namespaceKey === otherNamespaceKey);
  expect(otherNamespacesBefore).toHaveLength(1);
  const otherHeadsBefore = snapshotBefore.heads.filter(isOther);
  const otherEvidenceBefore = snapshotBefore.evidence.filter(isOther);
  const otherPayloadsBefore = snapshotBefore.payloads.filter(isOther);
  const otherOperationsBefore = snapshotBefore.operations.filter(isOther);
  const otherSequencesBefore = snapshotBefore.sequences.filter((row) => row.documentKey === otherSessionKey);
  const otherExpiriesBefore = snapshotBefore.expiries.filter(isOtherExpiry);
  expect(otherHeadsBefore.length).toBeGreaterThan(0);
  expect(otherEvidenceBefore.length).toBeGreaterThan(0);
  expect(otherPayloadsBefore.length).toBeGreaterThan(0);
  expect(otherOperationsBefore.length).toBeGreaterThan(0);
  expect(otherSequencesBefore).toHaveLength(1);

  const row = await f.drafts.load(f.key);
  if (!row) throw new Error("missing reset row");
  await f.repo.resetOwnedNamespace(f.key.namespace, row.token.namespaceIncarnation);

  // The other namespace's actual rows and values are unchanged, filtered by its
  // complete key/namespace, never substituted by another session in it.
  expect(await f.drafts.load(otherKey)).toEqual(value);
  expect(await f.repo.listAnnotations(otherDoc)).toEqual(notes);
  const after = await annotationSnapshot(f);
  expect(
    (await rowsForTests<NamespaceRow>(f.factory, f.databaseName, ANNOTATION_NAMESPACES_STORE)).filter(
      (r) => r.namespaceKey === otherNamespaceKey,
    ),
  ).toEqual(otherNamespacesBefore);
  expect(after.heads.filter(isOther)).toEqual(otherHeadsBefore);
  expect(after.evidence.filter(isOther)).toEqual(otherEvidenceBefore);
  expect(after.payloads.filter(isOther)).toEqual(otherPayloadsBefore);
  expect(after.operations.filter(isOther)).toEqual(otherOperationsBefore);
  expect(after.sequences.filter((row) => row.documentKey === otherSessionKey)).toEqual(otherSequencesBefore);
  expect(after.expiries.filter(isOtherExpiry)).toEqual(otherExpiriesBefore);

  // The reset namespace's old note is gone.
  const resetDocumentKey = annotationDocumentKey(f.document);
  expect(snapshotBefore.operations.some((op) => op.documentKey === resetDocumentKey)).toBe(true);
  expect(after.operations.some((op) => op.documentKey === resetDocumentKey)).toBe(false);
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
});

// 7. A failed reset must keep retained expiry work recoverable: the aborted reset
// transaction rolls back its durable writes, and the retained pending expiry from
// an earlier failed fence must still be finishable by a direct retry.
it("a failed reset keeps retained expiry work recoverable", async () => {
  let failFence = false;
  let failReset = false;
  const f = await fixture("reset-abort", {
    beforeCommit: (operation) => {
      if (operation === "expiry" && failFence) throw new Error("expiry fault");
      if (operation === "reset" && failReset) throw new Error("reset fault");
    },
  });
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const recoveryAttachmentBefore = recoveryBefore.attachments[0];
  if (!recoveryAttachmentBefore) throw new Error("fixture recovery attachment missing");
  const recoveryBytesBefore = await blobBytes(recoveryAttachmentBefore.blob);
  const sourceBefore = f.source.getSnapshot();
  const oldIncarnation = ordinaryBefore.token.namespaceIncarnation;
  const resetDocumentKey = annotationDocumentKey(f.document);

  // Real retained work: a changed rev-C read fails its durable expiry fence and
  // is honest expiry-pending with no receipt and no committed expiry row.
  failFence = true;
  const fenced = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-C"));
  expect(fenced.state).toBe("expiry-pending");
  if (fenced.state !== "expiry-pending") throw new Error("changed rev-C read was not fenced");
  expect(fenced.receipt).toBeUndefined();
  const afterFailedFence = await annotationSnapshot(f);
  expect(afterFailedFence.expiries).toEqual([]);
  expect(afterFailedFence.operations.find((row) => row.operationKey === f.operationKey)?.state).toBe("pending");

  // The reset itself is a whole-transaction abort: every durable annotation and
  // namespace row, both complete backings, the live source and the real Blob
  // bytes survive unchanged.
  const namespaceRowsBefore = await rowsForTests<NamespaceRow>(f.factory, f.databaseName, ANNOTATION_NAMESPACES_STORE);
  const rowsBeforeReset = await annotationSnapshot(f);
  failReset = true;
  await expect(f.repo.resetOwnedNamespace(f.key.namespace, oldIncarnation)).rejects.toThrow("reset fault");
  expect(await annotationSnapshot(f)).toEqual(rowsBeforeReset);
  expect(await rowsForTests<NamespaceRow>(f.factory, f.databaseName, ANNOTATION_NAMESPACES_STORE)).toEqual(
    namespaceRowsBefore,
  );
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  const recoveryAfterResetFault = await f.outbox.getRecovery("rec-1");
  expect(recoveryAfterResetFault).toEqual(recoveryBefore);
  const attachmentAfterResetFault = recoveryAfterResetFault?.attachments[0];
  if (!attachmentAfterResetFault) throw new Error("post-reset-fault recovery attachment missing");
  expect(attachmentAfterResetFault).toMatchObject(attachmentIdentity);
  expect(await blobBytes(attachmentAfterResetFault.blob)).toEqual(recoveryBytesBefore);
  expect(f.source.getSnapshot()).toEqual(sourceBefore);

  // Heal both faults and finish the retained expiry DIRECTLY: the aborted reset
  // must not have dropped it, so the pending operation cancels and one durable
  // fenced expiry receipt commits. Listing emptiness alone is insufficient
  // because in-memory suppression already hides the note.
  failFence = false;
  failReset = false;
  await f.repo.retryPending("reader-open");
  const healed = await annotationSnapshot(f);
  expect(healed.expiries).toHaveLength(1);
  expect(healed.expiries[0]?.receipt.phase).toBe("fenced");
  expect(healed.operations.find((row) => row.operationKey === f.operationKey)?.state).toBe("cancelled");
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  const recoveryAfterHeal = await f.outbox.getRecovery("rec-1");
  expect(recoveryAfterHeal).toEqual(recoveryBefore);
  const attachmentAfterHeal = recoveryAfterHeal?.attachments[0];
  if (!attachmentAfterHeal) throw new Error("post-heal recovery attachment missing");
  expect(await blobBytes(attachmentAfterHeal.blob)).toEqual(recoveryBytesBefore);
  expect(f.source.getSnapshot()).toEqual(sourceBefore);

  // Only now, through the same actual API, does a reset commit: new incarnation,
  // old annotation gone, preserved draft/recovery.
  const reset = await f.repo.resetOwnedNamespace(f.key.namespace, oldIncarnation);
  expect(reset.committed).toBe(true);
  expect(reset.incarnation).not.toBe(oldIncarnation);
  expect((await annotationSnapshot(f)).operations.some((op) => op.documentKey === resetDocumentKey)).toBe(false);
  expect(await f.repo.listAnnotations(f.document)).toEqual([]);
  expect(await f.drafts.load(f.key)).toMatchObject({
    text: ordinaryBefore.text,
    skillNames: ordinaryBefore.skillNames,
  });
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  // Useful operation returns: refresh the existing source, take a fresh edit and
  // append a new annotation exactly once through the chosen backing.
  await f.source.prepareAnnotation();
  f.source.acceptEdit({ text: "fresh", skillNames: [], mentions: [] });
  await f.source.whenSettled();
  await f.repo.saveAnnotation(await acceptedOperation(f.repo, f.document, "fresh-id", "Fresh note.", f.destination));
  expect((await f.repo.handoff(f.document, "fresh-id")).state).toBe("applied");
  const freshReference = REFERENCE.replace('"Keep this scope."', '"Fresh note."');
  expect((await f.drafts.load(f.key))?.text).toBe(`fresh\n\n${freshReference}`);
});

// Repository-retry regression over the real adapter APIs: a retry pass visits
// persisted pending handoffs in session SAVE order while another session's
// unresolved owner stays pending and never blocks this session's FIFO. Every
// backing is compared to the captured post-application state after a repeat retry.
it("retry visits persisted pending work in save order and leaves another session pending", async () => {
  const f = await fixture("retry-pending-order");
  const secondDocument: DocumentKey = { ...f.key, path: "SECOND.md" };
  const otherDocument: DocumentKey = { ...browserDraftKey("other-session", location.origin), path: "OTHER.md" };

  // op-1 (sessionSequence 1) is the earlier root-session operation the fixture
  // saved. Save a LATER same-session operation whose ID sorts BEFORE op-1: a
  // retry that walked operation-key order instead of session sequence would reach
  // this FIFO-blocked later operation first and leave it pending rather than
  // reaching op-1 first.
  const second = await acceptedOperation(f.repo, secondDocument, "a-second", "second", f.destination);
  await f.repo.saveAnnotation(second);
  // A different session's operation: the fixture's targetFor resolves only the
  // root session, so its handoff stays pending without stopping this session.
  const blocked = await acceptedOperation(f.repo, otherDocument, "0-blocked", "blocked", f.destination);
  await f.repo.saveAnnotation(blocked);

  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const recoveryAttachment = recoveryBefore.attachments[0];
  if (!recoveryAttachment) throw new Error("fixture recovery attachment missing");
  const recoveryBytesBefore = await blobBytes(recoveryAttachment.blob);
  const payloadsBefore = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);

  // The later same-session handoff genuinely returns pending while op-1 is pending.
  expect((await f.repo.handoff(secondDocument, "a-second")).state).toBe("pending");
  // A blocked handoff changes neither backing.
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  const recoveryEdits = vi.spyOn(f.outbox, "updateRecoveryInput");

  await f.repo.retryPending("foreground");

  const ordinaryAfter = await f.drafts.load(f.key);
  const recoveryAfter = await f.outbox.getRecovery("rec-1");
  if (!ordinaryAfter || !recoveryAfter) throw new Error("post-retry rows missing");
  // The second file's reference is the shared literal with only file and note
  // replaced, never the production formatter.
  const secondReference = REFERENCE.replace("NOTES.md:1:19-1:22", "SECOND.md:1:19-1:22").replace(
    '"Keep this scope."',
    '"second"',
  );
  const expectedText = `${ordinaryBefore.text}\n\n${REFERENCE}\n\n${secondReference}`;
  expect(ordinaryAfter.text).toBe(expectedText);
  // The complete ordinary row equals the captured original with the appended text
  // and an independently constructed token advanced by two (one append per op).
  expect(ordinaryAfter).toEqual({
    ...ordinaryBefore,
    text: expectedText,
    token: { ...ordinaryBefore.token, sessionRevision: ordinaryBefore.token.sessionRevision + 2 },
  });
  expect(recoveryAfter).toEqual(recoveryBefore);
  const recoveryAttachmentAfter = recoveryAfter.attachments[0];
  if (!recoveryAttachmentAfter) throw new Error("post-retry recovery attachment missing");
  expect(recoveryAttachmentAfter).toMatchObject(attachmentIdentity);
  expect(await blobBytes(recoveryAttachmentAfter.blob)).toEqual(recoveryBytesBefore);

  // Persisted original payloads are unchanged; root receipts applied in save
  // order; the other session's receipt stays pending.
  const payloadsAfter = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
  expect(payloadsAfter).toEqual(payloadsBefore);
  const receiptsAfter = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  const rootApplied = new Map(
    receiptsAfter
      .filter((row) => row.operationId === "op-1" || row.operationId === "a-second")
      .map((row) => [row.operationId, row]),
  );
  expect(rootApplied.get("op-1")).toMatchObject({
    state: "applied",
    draftRevision: ordinaryBefore.token.sessionRevision + 1,
  });
  expect(rootApplied.get("a-second")).toMatchObject({
    state: "applied",
    draftRevision: ordinaryBefore.token.sessionRevision + 2,
  });
  expect(receiptsAfter.find((row) => row.operationId === "0-blocked")).toMatchObject({ state: "pending" });

  // Repeat retry and freshly reread every backing; compare to the captured
  // post-application state to prove no duplicate text, revision or receipt change.
  await f.repo.retryPending("foreground");
  expect(await f.drafts.load(f.key)).toEqual(ordinaryAfter);
  const recoveryReplayed = await f.outbox.getRecovery("rec-1");
  if (!recoveryReplayed) throw new Error("post-replay recovery missing");
  expect(recoveryReplayed).toEqual(recoveryAfter);
  const recoveryReplayAttachment = recoveryReplayed.attachments[0];
  if (!recoveryReplayAttachment) throw new Error("post-replay recovery attachment missing");
  expect(recoveryReplayAttachment).toMatchObject(attachmentIdentity);
  expect(await blobBytes(recoveryReplayAttachment.blob)).toEqual(recoveryBytesBefore);
  expect(await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).toEqual(
    payloadsAfter,
  );
  expect(await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).toEqual(
    receiptsAfter,
  );

  // Annotation retry performs no local send/resend/update and leaves the durable
  // outbox empty (no RPC claim follows from this unit).
  expect(enqueues).not.toHaveBeenCalled();
  expect(resends).not.toHaveBeenCalled();
  expect(recoveryEdits).not.toHaveBeenCalled();
  expect(await f.outbox.listOutbox()).toEqual([]);
});

// A. Keyed subscribers against real storage: exact-DocumentKey wakeups after commit,
// scoped consumer reads across expiry and reset, retirement on unsubscribe/close, and
// best-effort cross-adapter delivery that rereads committed data as authority.
type FixtureRepo = Awaited<ReturnType<typeof fixture>>["repo"];

// The frozen adapter lists every unexpired pending AND applied row for a key, so a
// view is summarized by operation id and handoff state, never by a bare count.
function viewSummary(views: AnnotationView[]): string {
  return views.map((view) => `${view.operation.operationId}:${view.handoff.state}`).join(",") || "empty";
}

// A compact real-observation helper: each wakeup initiates a real listAnnotations
// read and records the summarized view. `dispose` unsubscribes and joins every
// started read so unexpected failures propagate and no read outlives its subscription.
function observeKeyStates(repo: FixtureRepo, key: DocumentKey) {
  const states: string[] = [];
  let pending: Promise<void> = Promise.resolve();
  const unsubscribe = repo.subscribe(key, () => {
    pending = pending.then(async () => {
      states.push(viewSummary(await repo.listAnnotations(key)));
    });
  });
  let disposed = false;
  const dispose = async () => {
    if (disposed) return;
    disposed = true;
    unsubscribe();
    await pending;
  };
  cleanupAfterTest(dispose);
  return { states, dispose, settled: () => pending };
}

it("notifies a keyed listener for real saved and applied work despite a throwing listener", async () => {
  const f = await fixture("subscriber-save-apply");
  // Settle the fixture's earlier pending op so this session's FIFO is free.
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  // Prepare the accepted operation BEFORE the save-only observation window.
  const operation = await acceptedOperation(f.repo, f.document, "subscriber-id", "note", f.destination);

  const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
  cleanupAfterTest(() => consoleError.mockRestore());
  const observed: string[] = [];
  let reading: Promise<void> = Promise.resolve();
  const unsubscribeThrowing = f.repo.subscribe(f.document, () => {
    throw new Error("throwing listener");
  });
  cleanupAfterTest(() => unsubscribeThrowing());
  const unsubscribe = f.repo.subscribe(f.document, () => {
    // The healthy listener initiates a real read; every assertion stays outside it.
    reading = reading.then(async () => {
      observed.push(viewSummary(await f.repo.listAnnotations(f.document)));
    });
  });
  cleanupAfterTest(async () => {
    unsubscribe();
    await reading;
  });

  // Independent key literals in the approved row shape, built from the real origin
  // rather than the production serializers.
  const origin = location.origin;
  const operationKey = JSON.stringify(["browser", origin, "subscriber-id"]);
  const documentKey = JSON.stringify(["browser", origin, "root", "NOTES.md"]);
  const namespaceKey = JSON.stringify(["browser", origin]);
  const sessionKey = JSON.stringify(["browser", origin, "root"]);
  const baselinePayloads = await rowsForTests<AnnotationPayloadRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_PAYLOADS_STORE,
  );
  const epoch = baselinePayloads.find((row) => row.operation.operationId === "op-1")?.epoch;
  const baselineReceipts = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  const opOneReceipt = baselineReceipts.find((row) => row.operationId === "op-1");
  if (epoch === undefined || !opOneReceipt) throw new Error("settled fixture baseline missing");
  if (opOneReceipt.state !== "applied") throw new Error("fixture op-1 is not applied");

  await f.repo.saveAnnotation(operation);
  await reading;
  // The view keeps the settled op-1 alongside the new pending operation.
  expect(observed[observed.length - 1]).toBe("op-1:already-applied,subscriber-id:pending");
  const afterSaveCount = observed.length;
  expect((await f.repo.handoff(f.document, operation.operationId)).state).toBe("applied");
  await reading;
  // The list view reports an applied receipt as already-applied.
  expect(observed.slice(afterSaveCount)).toContain("op-1:already-applied,subscriber-id:already-applied");

  const noteReference = REFERENCE.replace('"Keep this scope."', '"note"');
  const draft = await f.drafts.load(f.key);
  if (!draft) throw new Error("root draft missing");
  expect(draft.text).toBe(`ordinary /same\n\n${REFERENCE}\n\n${noteReference}`);

  const payloads = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
  expect(payloads.filter((row) => row.operation.operationId === "subscriber-id")).toEqual([
    { payloadKey: operationKey, documentKey, epoch, operation },
  ]);

  const receipts = await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE);
  expect(receipts.find((row) => row.operationId === "subscriber-id")).toEqual({
    operationKey,
    namespaceKey,
    namespaceIncarnation: opOneReceipt.namespaceIncarnation,
    documentKey,
    sessionKey,
    sessionSequence: opOneReceipt.sessionSequence + 1,
    operationId: "subscriber-id",
    epoch,
    state: "applied",
    draftRevision: opOneReceipt.draftRevision + 1,
  });

  // Any diagnostics are only the deliberately thrown listener fault; silent isolation
  // when the producer logs nothing is acceptable.
  for (const call of consoleError.mock.calls) {
    expect(call.map((value) => String(value)).join(" ")).toContain("throwing listener");
  }
});

it("does not announce an aborted save and notifies only after the save transaction completes", async () => {
  let fail = false;
  let observingSave = false;
  const events: string[] = [];
  const f = await fixture("subscriber-aborted", {
    beforeCommit: (operation) => {
      if (observingSave && operation === "save") {
        events.push("beforeCommit");
        if (fail) throw new Error("save fault");
      }
    },
  });
  // Prepare the accepted operation BEFORE the observed window AND the subscription,
  // so preparation/read notifications cannot enter the save-only timeline.
  const operation = await acceptedOperation(f.repo, f.document, "healed-id", "note", f.destination);
  const observed: string[] = [];
  let reading: Promise<void> = Promise.resolve();
  const unsubscribe = f.repo.subscribe(f.document, () => {
    events.push("notify");
    reading = reading.then(async () => {
      const view = (await f.repo.listAnnotations(f.document)).find(
        (candidate) => candidate.operation.operationId === "healed-id",
      );
      observed.push(view ? view.handoff.state : "absent");
    });
  });
  cleanupAfterTest(async () => {
    unsubscribe();
    await reading;
  });

  // Call-through observation of the REAL save transaction's own terminal events:
  // a scoped spy on the real transaction method observes the returned transaction
  // without replacing its behavior.
  const originalTransaction = IDBDatabase.prototype.transaction;
  const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const transaction = originalTransaction.apply(this, args);
    const storeNames = args[0];
    const stores = Array.isArray(storeNames) ? storeNames : [storeNames];
    if (
      observingSave &&
      this.name === f.databaseName &&
      args[1] === "readwrite" &&
      stores.includes(ANNOTATION_PAYLOADS_STORE)
    ) {
      transaction.addEventListener("complete", () => events.push("complete"));
      transaction.addEventListener("abort", () => events.push("abort"));
    }
    return transaction;
  });
  cleanupAfterTest(() => transactionSpy.mockRestore());

  observingSave = true;
  fail = true;
  await expect(f.repo.saveAnnotation(operation)).rejects.toThrow("save fault");
  // The aborted attempt reached beforeCommit, then its own real transaction aborted.
  expect(events).toEqual(["beforeCommit", "abort"]);
  // Immediately after the real abort: neither a payload nor an operation receipt.
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).some(
      (row) => row.operation.operationId === "healed-id",
    ),
  ).toBe(false);
  expect(
    (await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).some(
      (row) => row.operationId === "healed-id",
    ),
  ).toBe(false);
  fail = false;
  const receipt = await f.repo.saveAnnotation(operation);
  expect(receipt.operationId).toBe("healed-id");
  observingSave = false;
  await reading;
  // The healed save's own completion precedes its useful consumer notification, and
  // the consumer's real read observes the saved pending operation.
  expect(events.filter((event) => event === "beforeCommit")).toHaveLength(2);
  expect(events.indexOf("complete")).toBeGreaterThan(events.indexOf("abort"));
  expect(events.indexOf("notify")).toBeGreaterThan(events.indexOf("complete"));
  expect(observed).toContain("pending");
});

it("scopes consumer reads across save, changed-read expiry, reset and unsubscribe", async () => {
  const f = await fixture("subscriber-scope");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const siblingPath: DocumentKey = { ...f.document, path: "SIBLING.md" };
  const otherSession: DocumentKey = { ...browserDraftKey("other-session", location.origin), path: "NOTES.md" };
  const otherNamespaceDocument: DocumentKey = { ...browserDraftKey("root", "https://other.test"), path: "NOTES.md" };
  // Prepare the accepted operation BEFORE the save observation window.
  const operation = await acceptedOperation(f.repo, f.document, "scoped-id", "note", f.destination);
  const matching = observeKeyStates(f.repo, f.document);
  const sibling = observeKeyStates(f.repo, siblingPath);
  const session = observeKeyStates(f.repo, otherSession);
  const namespace = observeKeyStates(f.repo, otherNamespaceDocument);

  // Phase 1: the matching view contains the settled op-1 AND the new operation; no
  // sibling path, session or namespace consumer hears it.
  const matchingBaseline = matching.states.length;
  await f.repo.saveAnnotation(operation);
  await matching.settled();
  expect(matching.states.length).toBeGreaterThan(matchingBaseline);
  expect(matching.states[matching.states.length - 1]).toBe("op-1:already-applied,scoped-id:pending");
  const afterSave = matching.states.length;
  await f.repo.handoff(f.document, operation.operationId);
  await matching.settled();
  expect(matching.states.slice(afterSave)).toContain("op-1:already-applied,scoped-id:already-applied");
  expect(sibling.states).toEqual([]);
  expect(session.states).toEqual([]);
  expect(namespace.states).toEqual([]);

  // Phase 2: a changed accepted read then finishExpiry clears the matching view and
  // leaks to no sibling path/session/namespace consumer.
  const matchingBeforeExpiry = matching.states.length;
  const siblingBeforeExpiry = sibling.states.length;
  const sessionBeforeExpiry = session.states.length;
  const namespaceBeforeExpiry = namespace.states.length;
  const fenced = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-B"));
  expect(fenced.state).toBe("expiry-pending");
  if (fenced.state !== "expiry-pending" || !fenced.receipt) throw new Error("changed read did not fence");
  await f.repo.finishExpiry(fenced.receipt);
  await matching.settled();
  expect(matching.states.length).toBeGreaterThan(matchingBeforeExpiry);
  expect(matching.states[matching.states.length - 1]).toBe("empty");
  expect(sibling.states.length).toBe(siblingBeforeExpiry);
  expect(session.states.length).toBe(sessionBeforeExpiry);
  expect(namespace.states.length).toBe(namespaceBeforeExpiry);

  // Phase 3: a real other-namespace operation survives a root-namespace reset.
  const otherOperation = await acceptedOperation(f.repo, otherNamespaceDocument, "other-ns-id", "note", f.destination);
  await f.repo.saveAnnotation(otherOperation);
  await namespace.settled();
  // Capture and validate the other namespace's full saved operation/payload and
  // receipt BEFORE reset, then fresh-read and compare the complete rows afterward.
  const expectedOtherPayloads = (
    await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)
  ).filter((row) => row.operation.operationId === "other-ns-id");
  const expectedOtherReceipts = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).filter((row) => row.operationId === "other-ns-id");
  expect(expectedOtherPayloads).toHaveLength(1);
  expect(expectedOtherPayloads[0]?.operation).toEqual(otherOperation);
  expect(expectedOtherReceipts).toHaveLength(1);
  expect(expectedOtherReceipts[0]?.state).toBe("pending");
  const namespaceRows = await rowsForTests<NamespaceRow>(f.factory, f.databaseName, ANNOTATION_NAMESPACES_STORE);
  const rootIncarnation = namespaceRows.find(
    (row) => row.namespaceKey === annotationNamespaceKey(f.key.namespace),
  )?.incarnation;
  if (!rootIncarnation) throw new Error("root namespace row missing");
  const matchingBeforeReset = matching.states.length;
  const siblingBeforeReset = sibling.states.length;
  const sessionBeforeReset = session.states.length;
  const namespaceBeforeReset = namespace.states.length;
  expect((await f.repo.resetOwnedNamespace(f.key.namespace, rootIncarnation)).committed).toBe(true);
  await matching.settled();
  await sibling.settled();
  await session.settled();
  // Reset wakes every subscribed consumer in its exact namespace to the empty view.
  expect(matching.states.length).toBeGreaterThan(matchingBeforeReset);
  expect(sibling.states.length).toBeGreaterThan(siblingBeforeReset);
  expect(session.states.length).toBeGreaterThan(sessionBeforeReset);
  expect(matching.states[matching.states.length - 1]).toBe("empty");
  expect(sibling.states[sibling.states.length - 1]).toBe("empty");
  expect(session.states[session.states.length - 1]).toBe("empty");
  // The other namespace's listener gets no reset wakeup; its saved work is intact.
  expect(namespace.states.length).toBe(namespaceBeforeReset);
  expect(
    (await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).filter(
      (row) => row.operationId === "other-ns-id",
    ),
  ).toEqual(expectedOtherReceipts);
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).filter(
      (row) => row.operation.operationId === "other-ns-id",
    ),
  ).toEqual(expectedOtherPayloads);

  // Phase 4: unsubscribe retires that consumer; a remaining consumer still works.
  await matching.dispose();
  const matchingAfterUnsubscribe = matching.states.length;
  const remaining = observeKeyStates(f.repo, f.document);
  const later = await acceptedOperation(f.repo, f.document, "after-id", "note", f.destination);
  await f.repo.saveAnnotation(later);
  await f.repo.handoff(f.document, later.operationId);
  await remaining.settled();
  expect(matching.states.length).toBe(matchingAfterUnsubscribe);
  expect(remaining.states[remaining.states.length - 1]).toBe("after-id:already-applied");
});

it("retires a closed adapter's subscription while a real channel still reaches a healthy consumer", async () => {
  const { BroadcastChannel: NodeBroadcastChannel } = await vi.importActual<{
    BroadcastChannel: typeof BroadcastChannel;
  }>("node:worker_threads");
  // Install the real channel BEFORE either adapter is created.
  vi.stubGlobal("BroadcastChannel", NodeBroadcastChannel);
  cleanupAfterTest(() => {
    vi.unstubAllGlobals();
  });
  const f = await fixture("subscriber-close");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  // Phase 1: prepare the first operation BEFORE creating/subscribing the retiring adapter.
  const first = await acceptedOperation(f.repo, f.document, "close-id", "note", f.destination);
  const retired = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => null,
  });
  // Register cleanup immediately, before the missing-member call can throw.
  cleanupAfterTest(() => retired.close());
  let resolveRetired: (() => void) | undefined;
  const retiredWake = new Promise<void>((resolve) => {
    resolveRetired = resolve;
  });
  const retiredStates: string[] = [];
  let retiredReads: Promise<void> = Promise.resolve();
  const unsubscribeRetired = retired.subscribe(f.document, () => {
    resolveRetired?.();
    retiredReads = retiredReads.then(async () => {
      const view = (await retired.listAnnotations(f.document)).find(
        (candidate) => candidate.operation.operationId === "close-id",
      );
      if (view) retiredStates.push(`${view.operation.operationId}:${view.handoff.state}`);
    });
  });
  cleanupAfterTest(async () => {
    unsubscribeRetired();
    await retiredReads;
  });

  await f.repo.saveAnnotation(first);
  let firstTimer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      retiredWake,
      new Promise<never>((_resolve, reject) => {
        firstTimer = setTimeout(() => reject(new Error("no wakeup before close")), 2000);
      }),
    ]);
  } finally {
    if (firstTimer) clearTimeout(firstTimer);
  }
  await retiredReads;
  expect(retiredStates).toContain("close-id:pending");
  // The complete captured operation is visible before retirement.
  const retiredView = (await retired.listAnnotations(f.document)).find(
    (candidate) => candidate.operation.operationId === "close-id",
  );
  expect(retiredView?.operation).toEqual(first);

  // The retirement action is the REAL close, not unsubscribe.
  retired.close();
  const retiredAfterClose = retiredStates.length;

  // Phase 2: finish after-close-id preparation FIRST, then create/register a healthy
  // receiver whose wait cannot have been fulfilled by phase 1.
  const second = await acceptedOperation(f.repo, f.document, "after-close-id", "note", f.destination);
  const healthy = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => null,
  });
  cleanupAfterTest(() => healthy.close());
  let resolveHealthy: (() => void) | undefined;
  const healthyWake = new Promise<void>((resolve) => {
    resolveHealthy = resolve;
  });
  const healthyStates: string[] = [];
  let healthyReads: Promise<void> = Promise.resolve();
  const unsubscribeHealthy = healthy.subscribe(f.document, () => {
    resolveHealthy?.();
    healthyReads = healthyReads.then(async () => {
      const view = (await healthy.listAnnotations(f.document)).find(
        (candidate) => candidate.operation.operationId === "after-close-id",
      );
      if (view) healthyStates.push(`${view.operation.operationId}:${view.handoff.state}`);
    });
  });
  cleanupAfterTest(async () => {
    unsubscribeHealthy();
    await healthyReads;
  });

  await f.repo.saveAnnotation(second);
  let secondTimer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      healthyWake,
      new Promise<never>((_resolve, reject) => {
        secondTimer = setTimeout(() => reject(new Error("healthy consumer was not woken")), 2000);
      }),
    ]);
  } finally {
    if (secondTimer) clearTimeout(secondTimer);
  }
  await healthyReads;
  expect(healthyStates).toContain("after-close-id:pending");
  // The healthy consumer reads the complete captured second operation.
  const healthyView = (await healthy.listAnnotations(f.document)).find(
    (candidate) => candidate.operation.operationId === "after-close-id",
  );
  expect(healthyView?.operation).toEqual(second);
  // The closed registration has no later observations.
  expect(retiredStates.length).toBe(retiredAfterClose);
});

it("wakes a second adapter through a real BroadcastChannel, rereading the committed operation", async () => {
  const { BroadcastChannel: NodeBroadcastChannel } = await vi.importActual<{
    BroadcastChannel: typeof BroadcastChannel;
  }>("node:worker_threads");
  // Install the real channel BEFORE either adapter is created.
  vi.stubGlobal("BroadcastChannel", NodeBroadcastChannel);
  cleanupAfterTest(() => {
    vi.unstubAllGlobals();
  });
  const f = await fixture("subscriber-channel");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  // Finish ALL preparation before creating/subscribing the receiver.
  const operation = await acceptedOperation(f.repo, f.document, "channel-id", "note", f.destination);
  const second = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => null,
  });
  cleanupAfterTest(() => second.close());
  let resolveWake: (() => void) | undefined;
  const wake = new Promise<void>((resolve) => {
    resolveWake = resolve;
  });
  const unsubscribe = second.subscribe(f.document, () => resolveWake?.());
  cleanupAfterTest(() => unsubscribe());
  await f.repo.saveAnnotation(operation);
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      wake,
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error("no cross-adapter wakeup")), 2000);
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
  // The receiving adapter rereads committed data; the channel message is not authority.
  const views = await second.listAnnotations(f.document);
  expect(views.filter((view) => view.operation.operationId === "channel-id").map((view) => view.operation)).toEqual([
    operation,
  ]);
});

it("keeps local consumer reads working with no BroadcastChannel", async () => {
  vi.stubGlobal("BroadcastChannel", undefined);
  cleanupAfterTest(() => {
    vi.unstubAllGlobals();
  });
  const f = await fixture("subscriber-no-channel");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  // Prepare before subscribing so the observed window contains only the tested save.
  const operation = await acceptedOperation(f.repo, f.document, "no-channel-id", "note", f.destination);
  const observed: string[] = [];
  let pending: Promise<void> = Promise.resolve();
  const unsubscribe = f.repo.subscribe(f.document, () => {
    pending = pending.then(async () => {
      observed.push(viewSummary(await f.repo.listAnnotations(f.document)));
    });
  });
  cleanupAfterTest(async () => {
    unsubscribe();
    await pending;
  });
  await f.repo.saveAnnotation(operation);
  await pending;
  expect(observed).toContain("op-1:already-applied,no-channel-id:pending");
  const views = await f.repo.listAnnotations(f.document);
  expect(views.filter((view) => view.operation.operationId === "no-channel-id").map((view) => view.operation)).toEqual([
    operation,
  ]);
});

// Review fix 1: an inconclusive read must not erase the epoch's original
// comparison evidence, so a later definite change still fences the old epoch.
it("retains the original comparison baseline across an inconclusive truncated read", async () => {
  const f = await fixture("baseline-retention");
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  expect((await f.repo.listAnnotations(f.document)).map((view) => view.operation.operationId)).toContain("op-1");

  // A compatible truncated prefix with no revision is not a change: it must be
  // accepted and must NOT expire op-1.
  const prefix = await f.repo.acceptRead(
    await f.repo.beginRead(f.document),
    docContent(SOURCE.slice(0, 10), undefined, true),
  );
  expect(prefix.state).toBe("accepted");
  if (prefix.state !== "accepted") throw new Error("prefix not accepted");
  expect((await f.repo.listAnnotations(f.document)).map((view) => view.operation.operationId)).toContain("op-1");
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);

  // Current-display save control: the prefix acceptance carries a genuine valid
  // lease with a literal capture of the accepted prefix display ("First" at
  // 1:1-1:5), so retaining only the old display evidence cannot pass.
  expect(SOURCE).toBe("First occurrence: same\n\nOther context for a second occurrence is nearby: same\n");
  const prefixOperation: PreparedAnnotation = {
    operationId: "prefix-control",
    lease: prefix.lease,
    destination: f.destination,
    note: "prefix note",
    capture: {
      evidence: { fingerprint: displayedFingerprint(SOURCE.slice(0, 10), true) },
      sourceRange: { start: 0, end: 5 },
      start: { line: 1, column: 1 },
      endInclusive: { line: 1, column: 5 },
      selectedRenderedText: "First",
      contextBefore: "",
      contextAfter: " occu",
      mappingId: "prefix-map",
      displayedSnapshotId: prefix.lease.displayedSnapshotId,
    },
  };
  expect((await f.repo.saveAnnotation(prefixOperation)).operationId).toBe("prefix-control");

  // Close the original adapter before the replacement, then use the replacement
  // for every post-expiry call. targetFor resolves the real still-alive original
  // source, so target absence cannot mask missing expiry.
  f.repo.close();
  const replacement = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => f.source,
  });
  cleanupAfterTest(() => replacement.close());

  // The later definite full change must fence the surviving epoch.
  const changed = await replacement.acceptRead(
    await replacement.beginRead(f.document),
    docContent(`${SOURCE}changed\n`, "rev-B"),
  );
  expect(changed.state).toBe("expiry-pending");
  if (changed.state !== "expiry-pending" || !changed.receipt) throw new Error("changed read did not fence");
  await replacement.finishExpiry(changed.receipt);

  const listed = (await replacement.listAnnotations(f.document)).map((view) => view.operation.operationId);
  expect(listed).not.toContain("op-1");
  expect(listed).not.toContain("prefix-control");
  expect((await replacement.handoff(f.document, "op-1")).state).toBe("cancelled");
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
});

// Review fix 1, revision variant: an intervening revision-less read must not
// erase the original revision evidence.
it("expires when a revision-less read intervenes before a definite revision change", async () => {
  const f = await fixture("baseline-revision");
  // The fixture head already holds the full SOURCE at rev-A.
  expect((await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-A"))).state).toBe(
    "accepted",
  );
  // The same full text WITHOUT a revision is compatible, not a change.
  expect((await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, undefined))).state).toBe(
    "accepted",
  );
  // The same text at a DEFINITE new revision must expire the old epoch.
  const changed = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(SOURCE, "rev-B"));
  expect(changed.state).toBe("expiry-pending");
});

// An accepted, richer observation must itself become durable comparison evidence.
// Save a real operation under the richer lease, recreate the adapter on the SAME
// database with the SAME still-live target, then require the later definite change
// to fence and expire that operation before any deletion is requested.
async function evidenceGrowthExpiry(
  f: Awaited<ReturnType<typeof fixture>>,
  fresh: DocumentKey,
  richerLease: AcceptedSourceLease,
  operationId: string,
  finalContent: DocFileContent,
): Promise<void> {
  // Backing baseline is captured after the fixture's pending predecessor was
  // settled through its real handoff, so FIFO cannot mask the operation below.
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const textBefore = f.source.getSnapshot().text;
  const operation = preparedAnnotationFromLease(richerLease, operationId, "growth note", f.destination, "rev-A");
  expect((await f.repo.saveAnnotation(operation)).operationId).toBe(operationId);
  const operationKey = annotationOperationKey(fresh.namespace, operationId);
  const saved = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationKey === operationKey);
  if (!saved) throw new Error("saved operation row missing");

  // Close the first adapter, then use the replacement for every later call. The
  // target resolves the real still-alive source, so a missing target cannot
  // masquerade as expiry protection.
  f.repo.close();
  const replacement = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => f.source,
  });
  cleanupAfterTest(() => replacement.close());

  const changed = await replacement.acceptRead(await replacement.beginRead(fresh), finalContent);
  expect(changed.state).toBe("expiry-pending");
  if (changed.state !== "expiry-pending" || !changed.receipt) throw new Error("changed read did not fence");
  const receipt = changed.receipt;
  expect(receipt.key).toEqual(fresh);
  expect(receipt.expiredEpoch).toBe(saved.epoch);

  // The fence alone must already unlist and cancel the old operation while every
  // backing, its revision token and the composer text stay unchanged. This runs
  // BEFORE deletion is requested, pinning the fenced-before-deletion contract.
  expect((await replacement.listAnnotations(fresh)).map((view) => view.operation.operationId)).not.toContain(
    operationId,
  );
  expect((await replacement.handoff(fresh, operationId)).state).toBe("cancelled");
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect((await f.drafts.load(f.key))?.token.sessionRevision).toBe(ordinaryBefore.token.sessionRevision);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  const attachmentBeforeFinish = (await f.outbox.getRecovery("rec-1"))?.attachments[0];
  if (!attachmentBeforeFinish) throw new Error("recovery attachment lost before deletion");
  expect(await blobBytes(attachmentBeforeFinish.blob)).toEqual(new Uint8Array([1]));
  expect(f.source.getSnapshot().text).toBe(textBefore);

  await replacement.finishExpiry(receipt);

  // Fresh row reads: the old operation is unlisted and terminally cancelled, its
  // payload row is deleted, and neither backing nor composer text moved.
  expect((await replacement.listAnnotations(fresh)).map((view) => view.operation.operationId)).not.toContain(
    operationId,
  );
  expect(
    (await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).find(
      (row) => row.operationKey === operationKey,
    )?.state,
  ).toBe("cancelled");
  expect((await replacement.handoff(fresh, operationId)).state).toBe("cancelled");
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).some(
      (row) => row.payloadKey === operationKey,
    ),
  ).toBe(false);
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  expect((await f.drafts.load(f.key))?.token.sessionRevision).toBe(ordinaryBefore.token.sessionRevision);
  expect(f.source.getSnapshot().text).toBe(textBefore);
  const attachmentAfter = (await f.outbox.getRecovery("rec-1"))?.attachments[0];
  if (!attachmentAfter) throw new Error("recovery attachment lost");
  expect(attachmentAfter).toMatchObject(attachmentIdentity);
  expect(await blobBytes(attachmentAfter.blob)).toEqual(new Uint8Array([1]));
}

// A revision acquired after a revision-less full read is richer evidence: it
// establishes a definite revision change that a later rev-B must detect even
// though the first observation carried no revision. Must expire, not accept.
it("expires when a definite revision arrives after a revision-less full first observation", async () => {
  const f = await fixture("growth-revision");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const fresh: DocumentKey = { ...f.document, path: "GROWTH-REV.md" };

  const first = await f.repo.acceptRead(await f.repo.beginRead(fresh), docContent(SOURCE, undefined));
  expect(first.state).toBe("accepted");
  if (first.state !== "accepted") throw new Error("first observation not accepted");

  const richer = await f.repo.acceptRead(await f.repo.beginRead(fresh), docContent(SOURCE, "rev-A"));
  expect(richer.state).toBe("accepted");
  if (richer.state !== "accepted") throw new Error("richer observation not accepted");
  // The richer observation advances the SAME epoch it was accepted into.
  expect(richer.lease.epoch).toBe(first.lease.epoch);

  await evidenceGrowthExpiry(f, fresh, richer.lease, "growth-rev-op", docContent(SOURCE, "rev-B"));
});

// Full coverage acquired after an initial prefix is richer evidence too: the
// prefix-only baseline must not let a later changed full source pass. Increased
// coverage alone is accepted in the same epoch and its valid save succeeds.
it("expires when full coverage arrives after an initial prefix observation", async () => {
  const f = await fixture("growth-prefix");
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const fresh: DocumentKey = { ...f.document, path: "GROWTH-PREFIX.md" };

  const prefix = await f.repo.acceptRead(
    await f.repo.beginRead(fresh),
    docContent(SOURCE.slice(0, 10), undefined, true),
  );
  expect(prefix.state).toBe("accepted");
  if (prefix.state !== "accepted") throw new Error("prefix observation not accepted");

  const full = await f.repo.acceptRead(await f.repo.beginRead(fresh), docContent(SOURCE, "rev-A"));
  expect(full.state).toBe("accepted");
  if (full.state !== "accepted") throw new Error("full observation not accepted");
  // The full observation advances the SAME epoch it was accepted into.
  expect(full.lease.epoch).toBe(prefix.lease.epoch);

  await evidenceGrowthExpiry(f, fresh, full.lease, "growth-prefix-op", docContent(`${SOURCE}changed\n`, "rev-B"));
});

// After a full rev-A acceptance, a compatible revision-less prefix is accepted and
// becomes the retained local accepted source. A changed full read started while an
// older operation's append already holds the stores must install suppression
// BEFORE any await, at the append's last abortable boundary. Covered both inside
// one real repository instance and after the annotation adapter is recreated on the
// same factory/database with the original source still alive.
it.each([
  ["draft", "same"],
  ["draft", "recreated"],
  ["recovery", "same"],
  ["recovery", "recreated"],
] as const)(
  "cancels a pending append when a changed full read starts after a compatible prefix on %s backing with a %s adapter",
  async (backing, mode) => {
    let f: Awaited<ReturnType<typeof fixture>>;
    let active: ReturnType<typeof createDocumentAnnotationRepository>;
    let ticket: ReadTicket;
    let changed: Promise<ReadOutcome> | undefined;
    let startRead = false;
    const onAppendReadsIssued = () => {
      if (startRead) {
        startRead = false;
        changed = observeRead(active.acceptRead(ticket, docContent(`${SOURCE}changed\n`, "rev-B")));
      }
    };
    f = await fixture(`local-guard-${backing}-${mode}`, { onAppendReadsIssued });
    active = f.repo;
    // Recreated mode closes ONLY the first adapter BEFORE the prefix acceptance,
    // then runs every annotation call through a replacement on the same
    // factory/database and drafts, with the real live target.
    if (mode === "recreated") {
      f.repo.close();
      const replacement = createDocumentAnnotationRepository({
        indexedDB: f.factory,
        databaseName: f.databaseName,
        drafts: f.drafts,
        onAppendReadsIssued,
        targetFor: () => f.source,
      });
      cleanupAfterTest(() => replacement.close());
      active = replacement;
    }
    // Join and validate the started observed read even when an assertion exits
    // the case early; a rejection is an outcome, not a value.
    cleanupAfterTest(async () => {
      if (changed) expectResolvedRead(await changed);
    });
    const enqueue = vi.spyOn(f.outbox, "enqueueIntent");
    const resend = vi.spyOn(f.outbox, "resendRecovery");
    const update = vi.spyOn(f.outbox, "updateRecoveryInput");

    // Select the real recovery backing without seeding or consuming another draft,
    // and complete every real preparation BEFORE the preservation baseline.
    if (backing === "recovery") f.source.setActiveRecoveryId("rec-1");
    const preparation = await f.source.prepareAnnotation();
    if (!preparation) throw new Error("missing preparation");

    // op-1 stays pending: it is the operation under test.
    const operationBefore = (
      await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
    ).find((row) => row.operationKey === f.operationKey);
    if (!operationBefore) throw new Error("fixture operation row missing");

    // A genuinely accepted compatible prefix leaves only partial local evidence,
    // in the SAME epoch as op-1.
    const prefix = await active.acceptRead(
      await active.beginRead(f.document),
      docContent(SOURCE.slice(0, 10), undefined, true),
    );
    expect(prefix.state).toBe("accepted");
    if (prefix.state !== "accepted") throw new Error("prefix not accepted");
    expect(prefix.lease.epoch).toBe(operationBefore.epoch);

    // Preservation baselines captured after every real setup step.
    const ordinary = await f.drafts.load(f.key);
    const recovery = await f.outbox.getRecovery("rec-1");
    const baselineAttachment = recovery?.attachments[0];
    if (!baselineAttachment) throw new Error("fixture recovery attachment missing");
    expect(baselineAttachment).toMatchObject(attachmentIdentity);
    expect(await blobBytes(baselineAttachment.blob)).toEqual(new Uint8Array([1]));
    const snapshot = f.source.getSnapshot();
    const generationBefore = f.source.getLocalGeneration();
    const backingBefore = f.source.getRuntimeBacking();
    const tokenBefore = f.source.getDraftToken();

    // Next genuine ticket BEFORE starting handoff.
    ticket = await active.beginRead(f.document);

    startRead = true;
    const handedOff = await active.handoff(f.document, "op-1");
    if (!changed) throw new Error("changed read never started");
    const changedResult = expectResolvedRead(await changed);

    // Soft assertions so the unchanged backing/source evidence is still reported
    // when the boundary guard is absent and the append wrongly commits.
    expect.soft(handedOff.state).toBe("cancelled");
    expect.soft(changedResult.state).toBe("expiry-pending");
    expect.soft(await f.drafts.load(f.key)).toEqual(ordinary);
    expect.soft((await f.drafts.load(f.key))?.token.sessionRevision).toBe(ordinary?.token.sessionRevision);
    expect.soft(await f.outbox.getRecovery("rec-1")).toEqual(recovery);
    expect.soft(f.source.getSnapshot()).toEqual(snapshot);
    expect.soft(f.source.getLocalGeneration()).toBe(generationBefore);
    expect.soft(f.source.getRuntimeBacking()).toEqual(backingBefore);
    expect.soft(f.source.getDraftToken()).toEqual(tokenBefore);
    const boundaryAttachment = (await f.outbox.getRecovery("rec-1"))?.attachments[0];
    if (boundaryAttachment) expect.soft(await blobBytes(boundaryAttachment.blob)).toEqual(new Uint8Array([1]));
    expect.soft(enqueue).not.toHaveBeenCalled();
    expect.soft(resend).not.toHaveBeenCalled();
    expect.soft(update).not.toHaveBeenCalled();

    // Complete the real expiry only through its genuine returned receipt, which is
    // required: a missing receipt must fail, never silently skip completion.
    if (changedResult.state !== "expiry-pending" || !changedResult.receipt) {
      throw new Error("changed read did not return a genuine expiry receipt");
    }
    await active.finishExpiry(changedResult.receipt);

    // Fresh rows after actual expiry: no old-operation append, terminal
    // cancellation, payload deletion, and every backing/source value unchanged.
    expect
      .soft(
        (await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).find(
          (row) => row.operationKey === f.operationKey,
        )?.state,
      )
      .toBe("cancelled");
    expect
      .soft(
        (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).some(
          (row) => row.payloadKey === f.operationKey,
        ),
      )
      .toBe(false);
    expect
      .soft((await active.listAnnotations(f.document)).map((view) => view.operation.operationId))
      .not.toContain("op-1");
    const ordinaryAfterExpiry = await f.drafts.load(f.key);
    const recoveryAfterExpiry = await f.outbox.getRecovery("rec-1");
    expect.soft(ordinaryAfterExpiry).toEqual(ordinary);
    expect.soft(recoveryAfterExpiry).toEqual(recovery);
    const expiryAttachment = recoveryAfterExpiry?.attachments[0];
    expect.soft(expiryAttachment).toMatchObject(attachmentIdentity);
    if (expiryAttachment) expect.soft(await blobBytes(expiryAttachment.blob)).toEqual(new Uint8Array([1]));
    expect.soft(f.source.getSnapshot()).toEqual(snapshot);
    expect.soft(f.source.getLocalGeneration()).toBe(generationBefore);
    expect.soft(f.source.getRuntimeBacking()).toEqual(backingBefore);
    expect.soft(f.source.getDraftToken()).toEqual(tokenBefore);

    // Final cancelled replay: fresh rows and the same source values must hold again.
    expect.soft((await active.handoff(f.document, "op-1")).state).toBe("cancelled");
    expect.soft(await f.drafts.load(f.key)).toEqual(ordinary);
    expect.soft(await f.outbox.getRecovery("rec-1")).toEqual(recovery);
    const replayAttachment = (await f.outbox.getRecovery("rec-1"))?.attachments[0];
    expect.soft(replayAttachment).toMatchObject(attachmentIdentity);
    if (replayAttachment) expect.soft(await blobBytes(replayAttachment.blob)).toEqual(new Uint8Array([1]));
    expect.soft(f.source.getSnapshot()).toEqual(snapshot);
    expect.soft(f.source.getLocalGeneration()).toBe(generationBefore);
    expect.soft(f.source.getRuntimeBacking()).toEqual(backingBefore);
    expect.soft(f.source.getDraftToken()).toEqual(tokenBefore);
  },
);

// A failed FIRST save must stay retryable through a genuine identical reread:
// the immutable operation captured from the first accepted lease is reused
// verbatim, and an exactly identical acceptance preserves the accepted head
// revision and displayed snapshot identity, so the unchanged operation still
// matches the durable head at the retry save. Runs on the SAME adapter and on a
// second real adapter that shares the SAME database, against the real
// repository, draft/source/handoff and fake-indexeddb.
it.each(["same-adapter", "second-adapter"] as const)(
  "reuses the original immutable operation after a failed first save and an identical reread (%s)",
  async (adapterCase) => {
    let fail = false;
    const f = await fixture(`identical-read-${adapterCase}`, {
      beforeCommit: (operation) => {
        if (operation === "save" && fail) throw new Error("first-save fault");
      },
    });
    // A second real adapter shares the fixture's factory/database and drafts;
    // beginRead/acceptRead never resolve a destination source.
    const second =
      adapterCase === "second-adapter"
        ? createDocumentAnnotationRepository({
            indexedDB: f.factory,
            databaseName: f.databaseName,
            drafts: f.drafts,
            targetFor: () => null,
          })
        : undefined;
    if (second) cleanupAfterTest(() => second.close());
    const reader = second ?? f.repo;

    // 1. Settle op-1 through its real handoff BEFORE observing the new operation,
    // so FIFO ordering cannot mask the attempted operation, then record the full
    // ordinary/recovery/source/annotation baseline.
    expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
    const ordinaryBefore = await f.drafts.load(f.key);
    const recoveryBefore = await f.outbox.getRecovery("rec-1");
    if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
    const recoveryAttachmentBefore = recoveryBefore.attachments[0];
    if (!recoveryAttachmentBefore) throw new Error("fixture recovery attachment missing");
    const recoveryBytesBefore = await blobBytes(recoveryAttachmentBefore.blob);
    const sourceBefore = f.source.getSnapshot();
    const rowsBefore = await annotationRows(f);
    const op1RowBefore = rowsBefore.operations.find((row) => row.operationKey === f.operationKey);
    if (!op1RowBefore) throw new Error("fixture op-1 row missing");
    expect(op1RowBefore.state).toBe("applied");
    const sequenceBefore = rowsBefore.sequences[0]?.lastSequence;
    if (sequenceBefore === undefined) throw new Error("baseline sequence missing");
    const outboxBefore = await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE);

    // 2. Capture a NEW operation from a genuine accepted lease of the existing
    // SOURCE/rev-A with the fixture capture/note/destination, and clone its value.
    const capture = await reader.acceptRead(await reader.beginRead(f.document), docContent(SOURCE, "rev-A"));
    expect(capture.state).toBe("accepted");
    if (capture.state !== "accepted") throw new Error("capture read not accepted");
    const operation = preparedAnnotationFromLease(
      capture.lease,
      "op-retry",
      "Keep this scope.",
      f.destination,
      "rev-A",
    );
    const original = structuredClone(operation);
    const operationKey = annotationOperationKey(f.document.namespace, "op-retry");

    // 3. Abort the FIRST save of the new operation through the real storage fault
    // boundary: no payload/operation row and no sequence allocation, op-1 intact.
    fail = true;
    await expect(f.repo.saveAnnotation(operation)).rejects.toThrow("first-save fault");
    const afterFault = await annotationRows(f);
    expect(afterFault.payloads.filter((row) => row.payloadKey === operationKey)).toEqual([]);
    expect(afterFault.operations.filter((row) => row.operationKey === operationKey)).toEqual([]);
    expect(afterFault).toEqual(rowsBefore);
    expect(afterFault.operations.find((row) => row.operationKey === f.operationKey)).toEqual(op1RowBefore);
    expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
    expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
    expect(operation).toEqual(original);
    expect(f.source.getSnapshot()).toEqual(sourceBefore);
    expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);

    // 4. A genuine current ticket accepts exactly identical SOURCE/rev-A: the
    // namespace and epoch are unchanged, with durable ticket supersession left to
    // the existing neighboring guards.
    const reread = await reader.acceptRead(await reader.beginRead(f.document), docContent(SOURCE, "rev-A"));
    expect(reread.state).toBe("accepted");
    if (reread.state !== "accepted") throw new Error("identical reread not accepted");
    expect(reread.lease.namespaceIncarnation).toBe(original.lease.namespaceIncarnation);
    expect(reread.lease.epoch).toBe(original.lease.epoch);
    expect(reread.lease.key).toEqual(f.document);

    // 5. Recover the fault and save the ORIGINAL immutable operation: exactly one
    // exact payload, one pending receipt and one sequence allocation, with a
    // repeat save duplicating nothing. This save is the primary behavioral
    // assertion.
    fail = false;
    const receipt = await f.repo.saveAnnotation(operation);
    expect(receipt.operationId).toBe(operation.operationId);
    expect(operation).toEqual(original);
    // The exact identical-acceptance contract, asserted after the save so the
    // retry save stays the primary RED: the reread preserved the accepted head
    // revision and displayed snapshot identity the immutable operation owns,
    // which is why the unchanged retry save can match the durable head.
    expect(reread.lease.headRevision).toBe(original.lease.headRevision);
    expect(reread.lease.displayedSnapshotId).toBe(original.lease.displayedSnapshotId);
    const savedRows = await annotationRows(f);
    const savedPayloads = savedRows.payloads.filter((row) => row.payloadKey === operationKey);
    expect(savedPayloads).toHaveLength(1);
    expect(savedPayloads[0]?.operation).toEqual(original);
    const savedOperations = savedRows.operations.filter((row) => row.operationKey === operationKey);
    expect(savedOperations).toHaveLength(1);
    expect(savedOperations[0]?.state).toBe("pending");
    expect(savedOperations[0]?.sessionSequence).toBe(sequenceBefore + 1);
    expect(receipt.sessionSequence).toBe(sequenceBefore + 1);
    expect(savedRows.sequences[0]?.lastSequence).toBe(sequenceBefore + 1);
    expect((await f.repo.saveAnnotation(operation)).sessionSequence).toBe(receipt.sessionSequence);
    expect(await annotationRows(f)).toEqual(savedRows);

    // 6. Handoff once through the original eligible source: the full ordinary row
    // equals the baseline plus the literal fixture reference, one revision advance,
    // and recovery/attachment bytes/outbox are preserved.
    const expectedText = `${ordinaryBefore.text}\n\n${REFERENCE}`;
    expect((await f.repo.handoff(f.document, operation.operationId)).state).toBe("applied");
    const ordinaryAfter = await f.drafts.load(f.key);
    expect(ordinaryAfter).toEqual({
      ...ordinaryBefore,
      text: expectedText,
      token: { ...ordinaryBefore.token, sessionRevision: ordinaryBefore.token.sessionRevision + 1 },
    });
    expect(f.source.getSnapshot()).toEqual({ ...sourceBefore, text: expectedText });
    const recoveryAfter = await f.outbox.getRecovery("rec-1");
    expect(recoveryAfter).toEqual(recoveryBefore);
    const attachmentAfter = recoveryAfter?.attachments[0];
    if (!attachmentAfter) throw new Error("post-append recovery attachment missing");
    expect(attachmentAfter).toMatchObject(attachmentIdentity);
    expect(await blobBytes(attachmentAfter.blob)).toEqual(recoveryBytesBefore);
    expect(await rowsForTests<unknown>(f.factory, f.databaseName, MUTATION_OUTBOX_STORE)).toEqual(outboxBefore);

    const appliedRows = await annotationRows(f);
    expect(appliedRows.operations.find((row) => row.operationKey === operationKey)?.state).toBe("applied");
    // Replay: no second append, no second revision advance, no duplicated rows.
    expect((await f.repo.handoff(f.document, operation.operationId)).state).toBe("already-applied");
    expect((await f.repo.saveAnnotation(operation)).sessionSequence).toBe(receipt.sessionSequence);
    expect(await annotationRows(f)).toEqual(appliedRows);
    expect(await f.drafts.load(f.key)).toEqual(ordinaryAfter);
    expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryAfter);
  },
);
