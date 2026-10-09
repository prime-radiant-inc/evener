// Step 5 runtime retry tests: the browser-only runtime coordinator and its
// bounded outcome govern retry health across the real annotation adapter, using
// the same retained runtime for the first Send and every retry. The regression
// that exercises the adapter alone lives in documentAnnotationIndexedDB.test.ts.
import type { SaveReceipt } from "@evener/appwire-client";
import { expect, it, vi } from "vitest";
import { REFERENCE } from "../panes/doc/testing/annotationFixtures";
import {
  acceptedOperation,
  attachmentIdentity,
  blobBytes,
  cleanupAfterTest,
  fixture,
  rowsForTests,
} from "../panes/doc/testing/annotationRepositoryFixtures";
import { createDocumentAnnotationRepository } from "../stores/documentAnnotationIndexedDB";
import { createDocumentAnnotationRuntime, type DocumentAnnotationRuntime } from "../stores/documentAnnotationRuntime";
import {
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  type AnnotationOperationRow,
  type AnnotationPayloadRow,
} from "../stores/localCompositionSchema";

it("coalesces each accepted retry reason, failed save retains immutable work", async () => {
  let fail = false,
    attempts = 0;
  const f = await fixture("runtime", {
    beforeCommit: (op) => {
      if (op === "save") {
        attempts++;
        if (fail) throw new Error("fault");
      }
    },
  });
  const runtime = createDocumentAnnotationRuntime(f.repo);
  cleanupAfterTest(() => runtime.dispose());
  const operation = await acceptedOperation(f.repo, f.document, "runtime-id", "note", f.destination);
  const original = structuredClone(operation);
  fail = true;
  expect(await runtime.save(operation)).toBeNull();
  // A failed save retains the ORIGINAL work, so a caller mutation afterwards
  // cannot change what a later retry persists.
  operation.note = "caller changed after the failed save";
  operation.capture.selectedRenderedText = "caller changed the capture";
  const beforeRetry = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
  expect(beforeRetry.filter((row) => row.operation.operationId === "runtime-id")).toEqual([]);
  for (const reason of ["reader-open", "composer-open", "foreground", "storage-recovered"] as const) {
    const before = attempts;
    await Promise.all([runtime.retryPending(reason), runtime.retryPending(reason)]);
    expect(attempts).toBe(before + 1);
  }
  fail = false;
  await runtime.retryPending("composer-open");
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE))
      .filter((row) => row.operation.operationId === "runtime-id")
      .map((row) => row.operation),
  ).toEqual([original]);
  expect(
    (await f.repo.listAnnotations(f.document)).filter((v) => v.operation.operationId === "runtime-id"),
  ).toHaveLength(1);
});

it("commit then lost ack and recreation reads terminal receipt before retry", async () => {
  let disposeOnCommit: (() => void) | undefined;
  const f = await fixture("lost-ack", {
    afterCommit: () => {
      const dispose = disposeOnCommit;
      disposeOnCommit = undefined;
      dispose?.();
      throw new Error("lost ack");
    },
  });
  const snapshotBefore = f.source.getSnapshot();
  const tokenBefore = f.source.getDraftToken();
  // Dispose the original source at the real afterCommit boundary: the durable
  // append has committed and acknowledgement has not run yet, so the source never
  // receives the appended state or token.
  disposeOnCommit = () => f.source.dispose();
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  expect(f.source.getSnapshot()).toEqual(snapshotBefore);
  expect(f.source.getDraftToken()).toEqual(tokenBefore);
  f.repo.close();
  const reopened = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => null,
  });
  cleanupAfterTest(() => reopened.close());
  const draftAfterCommit = await f.drafts.load(f.key);
  expect(draftAfterCommit?.text).toBe(`ordinary /same\n\n${REFERENCE}`);
  const receiptAfterCommit = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationId === "op-1");
  expect(receiptAfterCommit).toMatchObject({ state: "applied" });
  expect((await reopened.handoff(f.document, "op-1")).state).toBe("already-applied");
  // The terminal receipt returns before any new append: the durable draft and
  // receipt are unchanged by the replayed handoff.
  expect(await f.drafts.load(f.key)).toEqual(draftAfterCommit);
  expect(
    (await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).find(
      (row) => row.operationId === "op-1",
    ),
  ).toEqual(receiptAfterCommit);
});

it.each(["draft", "recovery"] as const)(
  "retries persisted pending %s after real storage recovery without another gesture",
  async (backing) => {
    let f: Awaited<ReturnType<typeof fixture>>, runtime: DocumentAnnotationRuntime | undefined;
    let failAppend = false,
      startSuccessDuringPass = false,
      observeSuccess = false,
      observedSuccesses = 0;
    let duringPassSave: Promise<SaveReceipt> | undefined;
    let appendAttempts = 0;
    f = await fixture(`persisted-pending-${backing}`, {
      beforeAbortableCompletion: () => {
        if (runtime) appendAttempts++; // actual append boundary, including internal lexical handoffs
        if (startSuccessDuringPass) {
          startSuccessDuringPass = false;
          observeSuccess = true;
          duringPassSave = f.repo.saveAnnotation(f.operation); // START real op-1 duplicate, never await here
        }
        if (failAppend) throw new Error("append storage fault");
      },
      onStorageSuccess: () => {
        if (observeSuccess) {
          observedSuccesses++;
          runtime?.storageSucceeded();
        }
      },
    });
    expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied"); // settle setup before observation
    if (backing === "recovery") f.source.setActiveRecoveryId("rec-1");
    runtime = createDocumentAnnotationRuntime(f.repo);
    const retainedRuntime = runtime;
    // Join every started pass and the during-pass save before the fixture closes
    // its connections, even when an earlier assertion failed.
    cleanupAfterTest(async () => {
      try {
        await retainedRuntime.whenIdle();
        await duringPassSave;
      } finally {
        retainedRuntime.dispose();
      }
    });
    const operation = await acceptedOperation(
      f.repo,
      f.document,
      "persisted-pending-id",
      "Recover this pending append.",
      f.destination,
    );
    const original = structuredClone(operation);
    const prepared = await f.source.prepareAnnotation();
    if (!prepared) throw new Error("missing preparation");
    const preparation = prepared;
    const ordinary = await f.drafts.load(f.key),
      recovery = await f.outbox.getRecovery("rec-1");
    if (!ordinary || !recovery) throw new Error("missing fixture backing");
    const recoveryAttachmentBefore = recovery.attachments[0];
    if (!recoveryAttachmentBefore) throw new Error("missing fixture recovery attachment");
    expect(recoveryAttachmentBefore).toMatchObject(attachmentIdentity);
    const recoveryBytesBefore = await blobBytes(recoveryAttachmentBefore.blob);
    async function recoveryBytes(): Promise<Uint8Array> {
      const record = await f.outbox.getRecovery("rec-1");
      const attachment = record?.attachments[0];
      if (!attachment) throw new Error("missing recovery attachment");
      expect(attachment).toMatchObject(attachmentIdentity);
      return blobBytes(attachment.blob);
    }
    const snapshot = f.source.getSnapshot(),
      publication = vi.fn();
    f.source.subscribe(publication);
    const saves = vi.spyOn(f.repo, "saveAnnotation"),
      retries = vi.spyOn(f.repo, "retryPending");
    const outcomes = vi.spyOn(f.repo, "retryPendingWithOutcome");
    const enqueues = vi.spyOn(f.outbox, "enqueueIntent"),
      resends = vi.spyOn(f.outbox, "resendRecovery");
    const recoveryEdits = vi.spyOn(f.outbox, "updateRecoveryInput");
    const saved = await retainedRuntime.save(operation);
    if (!saved) throw new Error("initial save failed");
    expect(saved.operationId).toBe(original.operationId);
    expect(saves).toHaveBeenCalledTimes(1);
    operation.note = "caller changed after successful save"; // persisted/runtime work must still use original
    const receipts = await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE);
    const payloads = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
    expect(receipts.filter((row) => row.operationId === original.operationId)).toHaveLength(1);
    expect(receipts.find((row) => row.operationId === original.operationId)?.state).toBe("pending");
    expect(
      payloads.filter((row) => row.operation.operationId === original.operationId).map((row) => row.operation),
    ).toEqual([original]);
    async function expectStillPending() {
      expect(await f.drafts.load(f.key)).toEqual(ordinary);
      expect(await f.outbox.getRecovery("rec-1")).toEqual(recovery);
      expect(await recoveryBytes()).toEqual(recoveryBytesBefore);
      expect(
        await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE),
      ).toEqual(receipts);
      expect(await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).toEqual(
        payloads,
      );
      expect(f.source.getSnapshot()).toEqual(snapshot);
      expect(f.source.ownsPreparation(preparation)).toBe(true);
      expect(f.source.getLocalGeneration()).toBe(preparation.localGeneration);
      expect(f.source.getRuntimeBacking()).toEqual(preparation.backing);
      expect(f.source.getDraftToken()).toEqual(preparation.token);
      expect(publication).not.toHaveBeenCalled();
    }
    failAppend = true;
    expect((await retainedRuntime.handoff(original.lease.key, original.operationId)).state).toBe("pending");
    await expectStillPending();
    startSuccessDuringPass = true;
    const firstPass = retainedRuntime.retryPending("foreground"),
      coalesced = retainedRuntime.retryPending("foreground");
    expect(coalesced).toBe(firstPass);
    await firstPass;
    if (!duringPassSave) throw new Error("real during-pass storage operation not started");
    expect((await duringPassSave).operationId).toBe("op-1");
    await retainedRuntime.whenIdle();
    observeSuccess = false;
    expect(observedSuccesses).toBeGreaterThan(0); // actual authority success during the pass did not recurse
    expect(retries.mock.calls).toEqual([["foreground"]]);
    expect(saves.mock.calls.filter(([value]) => value.operationId === original.operationId)).toHaveLength(1); // NO failed saves left
    expect(appendAttempts).toBe(2); // initial handoff plus the unsuccessful real repository retry
    await expectStillPending();
    await retainedRuntime.whenIdle();
    expect(retries).toHaveBeenCalledTimes(1); // a pending outcome does not self-reschedule
    retainedRuntime.invalidate(original.lease.key); // empty failed map must not clear persisted-pending health
    failAppend = false;
    observeSuccess = true;
    await f.repo.beginRead({ ...f.document, path: "STORAGE-RECOVERED.md" }); // unrelated real success, no Send/open/foreground
    await retainedRuntime.whenIdle();
    observeSuccess = false;
    const afterOrdinary = await f.drafts.load(f.key),
      afterRecovery = await f.outbox.getRecovery("rec-1");
    const reference = REFERENCE.replace('"Keep this scope."', '"Recover this pending append."');
    const expectedText = `${backing === "draft" ? ordinary.text : recovery.composerText}\n\n${reference}`;
    // A real storage-recovery pass applies the retained pending handoff exactly once.
    expect(backing === "draft" ? afterOrdinary?.text : afterRecovery?.composerText).toBe(expectedText);
    expect(expectedText.split(reference).length - 1).toBe(1);
    expect(afterOrdinary?.token).toEqual({ ...ordinary.token, sessionRevision: ordinary.token.sessionRevision + 1 });
    if (backing === "draft") {
      expect(afterOrdinary).toEqual({ ...ordinary, text: expectedText, token: afterOrdinary?.token });
      expect(afterRecovery).toEqual(recovery);
    } else {
      expect(afterOrdinary).toEqual({ ...ordinary, token: afterOrdinary?.token });
      const input = [
        { type: "text", text: expectedText, mentions: [{ kind: "command", name: "plan", offset: 9 }] },
        { type: "image", mediaType: "image/png", data: "AQ==", name: "one.png" },
        { type: "command", name: "plan" },
      ];
      expect(afterRecovery).toEqual({
        ...recovery,
        composerText: expectedText,
        payload: { ...recovery.payload, input },
        optimisticDisplay: { method: "turn/start", input },
      });
      expect(afterRecovery?.attachments).toEqual(recovery.attachments);
      expect(afterRecovery?.composerMentions).toEqual(recovery.composerMentions);
    }
    expect(f.source.getSnapshot().text).toBe(expectedText);
    expect(f.source.getSnapshot().mentions).toEqual(
      backing === "draft" ? ordinary.mentions : recovery.composerMentions,
    );
    expect(publication).toHaveBeenCalledTimes(1);
    const applied = (
      await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
    ).filter((row) => row.operationId === original.operationId);
    expect(applied).toHaveLength(1);
    expect(applied[0]).toMatchObject({
      state: "applied",
      draftRevision: ordinary.token.sessionRevision + 1,
    });
    expect(await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).toEqual(
      payloads,
    );
    expect(
      saves.mock.calls.filter(([value]) => value.operationId === original.operationId).map(([value]) => value),
    ).toEqual([original]);
    expect(saves.mock.calls.filter(([value]) => value.operationId === "op-1").map(([value]) => value)).toEqual([
      f.operation,
    ]);
    expect(retries.mock.calls).toEqual([["foreground"], ["storage-recovered"]]);
    expect(outcomes).toHaveBeenCalledTimes(2);
    const [firstOutcome, secondOutcome] = outcomes.mock.results;
    if (!firstOutcome || !secondOutcome) throw new Error("missing retry outcome observations");
    expect(await firstOutcome.value).toEqual({ pending: true });
    expect(await secondOutcome.value).toEqual({ pending: false });
    expect(appendAttempts).toBe(3); // the third real attempt applied the unchanged original operation
    expect(await recoveryBytes()).toEqual(recoveryBytesBefore);
    // Resolution cleared retry-needed. Successful own-pass callbacks and this later actual success cause no third pass.
    observeSuccess = true;
    await f.repo.beginRead({ ...f.document, path: "HEALTHY-AFTER-RESOLUTION.md" });
    await retainedRuntime.whenIdle();
    observeSuccess = false;
    expect(retries).toHaveBeenCalledTimes(2);
    expect((await retainedRuntime.handoff(original.lease.key, original.operationId)).state).toBe("already-applied");
    expect(appendAttempts).toBe(3); // terminal receipt returns before any new append transaction
    expect(await f.drafts.load(f.key)).toEqual(afterOrdinary);
    expect(await f.outbox.getRecovery("rec-1")).toEqual(afterRecovery);
    expect(await recoveryBytes()).toEqual(recoveryBytesBefore);
    expect(publication).toHaveBeenCalledTimes(1);
    expect(enqueues).not.toHaveBeenCalled();
    expect(resends).not.toHaveBeenCalled();
    expect(recoveryEdits).not.toHaveBeenCalled();
    expect(await f.outbox.listOutbox()).toEqual([]);
  },
);

it("invalidates failed work and disposal stops later storage and explicit retry triggers", async () => {
  let failSave = false,
    runtime: DocumentAnnotationRuntime | undefined;
  const f = await fixture("retry-invalidate-dispose", {
    beforeCommit: (operation) => {
      if (operation === "save" && failSave) throw new Error("save fault");
    },
    onStorageSuccess: () => runtime?.storageSucceeded(),
  });
  await f.repo.handoff(f.document, "op-1");
  runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(() => retainedRuntime.dispose());
  const saves = vi.spyOn(f.repo, "saveAnnotation"),
    retries = vi.spyOn(f.repo, "retryPending");
  const invalidated = await acceptedOperation(f.repo, f.document, "invalidated-id", "old capture", f.destination);
  failSave = true;
  expect(await retainedRuntime.save(invalidated)).toBeNull();
  retainedRuntime.invalidate(invalidated.lease.key);
  failSave = false;
  await retainedRuntime.retryPending("foreground");
  expect(saves.mock.calls.filter(([operation]) => operation.operationId === invalidated.operationId)).toHaveLength(1);
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).some(
      (row) => row.operation.operationId === invalidated.operationId,
    ),
  ).toBe(false);
  await f.repo.beginRead({ ...f.document, path: "AFTER-INVALIDATION.md" });
  await retainedRuntime.whenIdle();
  expect(retries.mock.calls).toEqual([["foreground"]]); // actual empty outcome cleared prior failed-save health
  const disposed = await acceptedOperation(
    f.repo,
    f.document,
    "disposed-id",
    "never retry after disposal",
    f.destination,
  );
  failSave = true;
  expect(await retainedRuntime.save(disposed)).toBeNull();
  retainedRuntime.dispose();
  failSave = false;
  await f.repo.beginRead({ ...f.document, path: "AFTER-DISPOSAL.md" });
  await retainedRuntime.retryPending("foreground");
  await retainedRuntime.whenIdle();
  expect(await retainedRuntime.save(disposed)).toBeNull();
  expect(retries.mock.calls).toEqual([["foreground"]]);
  expect(saves.mock.calls.filter(([operation]) => operation.operationId === disposed.operationId)).toHaveLength(1);
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).some(
      (row) => row.operation.operationId === disposed.operationId,
    ),
  ).toBe(false);
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
});
