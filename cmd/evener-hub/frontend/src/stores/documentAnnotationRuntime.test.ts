// Step 5 runtime retry tests: the browser-only runtime coordinator and its
// bounded outcome govern retry health across the real annotation adapter, using
// the same retained runtime for the first Send and every retry. The regression
// that exercises the adapter alone lives in documentAnnotationIndexedDB.test.ts.
import {
  displayedFingerprint,
  type PreparedAnnotation,
  type ReadTicket,
  type SaveReceipt,
} from "@evener/appwire-client";
import { IDBObjectStore } from "fake-indexeddb";
import { expect, it, vi } from "vitest";
import { REFERENCE, SOURCE } from "../panes/doc/testing/annotationFixtures";
import {
  acceptedOperation,
  attachmentIdentity,
  blobBytes,
  cleanupAfterTest,
  docContent,
  expectStampedNoEarlierThan,
  fixture,
  preparedAnnotationFromLease,
  rowsForTests,
} from "../panes/doc/testing/annotationRepositoryFixtures";
import { createDocumentAnnotationRepository } from "../stores/documentAnnotationIndexedDB";
import { createDocumentAnnotationRuntime, type DocumentAnnotationRuntime } from "../stores/documentAnnotationRuntime";
import {
  ANNOTATION_HEADS_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  type AnnotationHeadRow,
  type AnnotationOperationRow,
  type AnnotationPayloadRow,
  COMPOSER_DRAFTS_STORE,
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
    expect(retries.mock.calls.map(([reason]) => reason)).toEqual(["foreground"]);
    expect(saves.mock.calls.filter(([value]) => value.operationId === original.operationId)).toHaveLength(1); // NO failed saves left
    expect(appendAttempts).toBe(2); // initial handoff plus the unsuccessful real repository retry
    await expectStillPending();
    await retainedRuntime.whenIdle();
    expect(retries).toHaveBeenCalledTimes(1); // a pending outcome does not self-reschedule
    retainedRuntime.invalidate(original.lease.key, original.lease); // empty failed map must not clear persisted-pending health
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
      expect(afterOrdinary).toEqual({
        ...ordinary,
        text: expectedText,
        token: afterOrdinary?.token,
        editedAt: expect.any(Number),
      });
      expectStampedNoEarlierThan(afterOrdinary, ordinary);
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
    expect(retries.mock.calls.map(([reason]) => reason)).toEqual(["foreground", "storage-recovered"]);
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

it("retries a handoff refused while the composer was typing once that edit's draft commit lands", async () => {
  let runtime: DocumentAnnotationRuntime | undefined;
  let interruptAppend = false;
  const typedText = "typed while the annotation handed off";
  const f = await fixture("refused-mid-typing-draft-commit", {
    beforeFinalGuard: () => {
      if (!interruptAppend) return;
      interruptAppend = false;
      // The user types: the explicit edit lands while the handoff's append is
      // between its final boundary and the ownership guard, so the append
      // loses the preparation and the handoff stays pending.
      f.source.acceptEdit({ text: typedText, skillNames: [], mentions: [] });
    },
    onStorageSuccess: () => runtime?.storageSucceeded(),
  });
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(async () => {
    retainedRuntime.dispose();
    await retainedRuntime.whenIdle();
  });
  const operation = await acceptedOperation(f.repo, f.document, "op-2", "Second note.", f.destination);
  await f.repo.saveAnnotation(operation);
  interruptAppend = true;
  await expect(retainedRuntime.handoff(f.document, "op-2")).resolves.toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  // The interrupted typing settles into the durable draft on its own schedule.
  // Its commit is the only notification the runtime gets: no reader-open,
  // composer-open, foreground return, or annotation action is issued here.
  await f.source.whenSettled();
  await retainedRuntime.whenIdle();
  const secondReference = REFERENCE.replace('"Keep this scope."', '"Second note."');
  expect((await f.drafts.load(f.key))?.text).toBe(`${typedText}\n\n${secondReference}`);
  const applied = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationId === "op-2");
  expect(applied).toMatchObject({ state: "applied" });
  expect(f.source.getSnapshot().text).toBe(`${typedText}\n\n${secondReference}`);
});

// RoboRev review 43208: a provisional suppression is a local comparison awaiting
// durable validation, not a proven source change. Answering the overlapping
// handoff with the terminal cancelled outcome dropped the runtime's retry intent
// even though the durable receipt stayed pending, so a later storage success had
// nothing to recover once the stale read released the suppression.
it("retains retry intent through a provisional suppression and completes the handoff once it releases", async () => {
  let runtime: DocumentAnnotationRuntime | undefined;
  let staleTicket: ReadTicket | undefined;
  let staleRead: Promise<unknown> | undefined;
  let startStaleRead = false;
  const f = await fixture("provisional-suppression-retry", {
    onAppendReadsIssued: () => {
      if (!startStaleRead) return;
      startStaleRead = false;
      if (!staleTicket) throw new Error("missing stale ticket");
      // acceptRead installs its provisional suppression synchronously, before
      // its first await, and that durable validation proves the ticket stale.
      staleRead = f.repo.acceptRead(staleTicket, docContent(SOURCE, "rev-B"));
    },
    onStorageSuccess: () => runtime?.storageSucceeded(),
  });
  const second = createDocumentAnnotationRepository({
    indexedDB: f.factory,
    databaseName: f.databaseName,
    drafts: f.drafts,
    targetFor: () => null,
  });
  cleanupAfterTest(() => second.close());
  // This adapter remembers a ticket; another adapter's issuance advances the
  // durable read generation, so the remembered ticket can only validate stale.
  staleTicket = await f.repo.beginRead(f.document);
  await second.beginRead(f.document);
  runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(async () => {
    try {
      await retainedRuntime.whenIdle();
    } finally {
      retainedRuntime.dispose();
    }
  });
  const appends = vi.spyOn(f.drafts, "appendAnnotation");
  startStaleRead = true;
  await expect(retainedRuntime.handoff(f.document, "op-1")).resolves.toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  if (!staleRead) throw new Error("the overlapping stale read never started");
  await expect(staleRead).resolves.toEqual({ state: "stale-ticket" });
  // Only a real storage success follows - no reader-open, composer-open,
  // foreground return or annotation gesture drives the recovery.
  await f.repo.beginRead({ ...f.document, path: "UNRELATED-STORAGE-SUCCESS.md" });
  await retainedRuntime.whenIdle();
  const applied = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationId === "op-1");
  expect(applied).toMatchObject({ state: "applied" });
  expect((await f.drafts.load(f.key))?.text).toBe(`ordinary /same\n\n${REFERENCE}`);
  // One append was refused by the provisional suppression, and the retry
  // appended the reference exactly once.
  expect(appends.mock.calls.filter(([input]) => input.operationKey === f.operationKey)).toHaveLength(2);
});

it("disposal preserves the composer while handoffs are queued behind an in-flight one", async () => {
  let appendStarted: (() => void) | undefined;
  const appendStartedPromise = new Promise<void>((resolve) => {
    appendStarted = resolve;
  });
  let observeAppend = false;
  const f = await fixture("handoff-disposal-queued", {
    onAppendReadsIssued: () => {
      if (observeAppend) appendStarted?.();
    },
  });
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  const runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(async () => {
    retainedRuntime.dispose();
    await retainedRuntime.whenIdle();
  });
  for (const [operationId, note] of [
    ["op-first", "first note"],
    ["op-second", "second note"],
    ["op-third", "third note"],
  ] as const) {
    const operation = await acceptedOperation(f.repo, f.document, operationId, note, f.destination);
    await f.repo.saveAnnotation(operation);
  }
  observeAppend = true;
  const inflight = retainedRuntime.handoff(f.document, "op-first");
  await appendStartedPromise; // op-first is mid-append; the append runner issued its reads
  observeAppend = false;
  const queued = retainedRuntime.handoff(f.document, "op-second"); // waits behind the in-flight one
  retainedRuntime.dispose(); // application teardown: one handoff in flight, one queued
  const [inflightResult, queuedResult] = await Promise.all([inflight, queued]);
  expect(inflightResult.state).toBe("applied"); // the already-running handoff completes
  expect(queuedResult).toEqual({ state: "cancelled" }); // the queued one must never reach the composer
  await expect(retainedRuntime.handoff(f.document, "op-third")).resolves.toEqual({
    state: "cancelled",
  });
  // Disposal preserved the durable draft: only the in-flight handoff's reference landed.
  const firstRef = REFERENCE.replace('"Keep this scope."', '"first note"');
  expect((await f.drafts.load(f.key))?.text).toBe(`ordinary /same\n\n${REFERENCE}\n\n${firstRef}`);
  const receipts = await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE);
  expect(receipts.find((row) => row.operationId === "op-second")?.state).toBe("pending");
  expect(receipts.find((row) => row.operationId === "op-third")?.state).toBe("pending");
});

// RoboRev review 43186: the persisted retry sweep drives repository handoffs
// directly, so the runtime's disposal guard never saw them. Disposal while the
// sweep's first handoff was awaiting completion still started every later
// handoff, and each one could append to a retained composer after teardown.
it("disposal during a persisted sweep stops later handoffs and leaves them pending", async () => {
  let appendIssued: (() => void) | undefined;
  const firstAppend = new Promise<void>((resolve) => {
    appendIssued = resolve;
  });
  let observeAppend = false;
  const f = await fixture("sweep-disposal", {
    onAppendReadsIssued: () => {
      if (!observeAppend) return;
      observeAppend = false;
      appendIssued?.();
    },
  });
  // op-1 is already persisted pending by the fixture; two more queued
  // annotations make the sweep a multi-operation one in session save order.
  for (const [operationId, note] of [
    ["op-second", "second note"],
    ["op-third", "third note"],
  ] as const) {
    const operation = await acceptedOperation(f.repo, f.document, operationId, note, f.destination);
    await f.repo.saveAnnotation(operation);
  }
  const runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(async () => {
    await retainedRuntime.whenIdle();
    retainedRuntime.dispose();
  });
  observeAppend = true;
  const pass = retainedRuntime.retryPending("foreground");
  await firstAppend; // op-1's append reads are issued, its handoff is still awaiting
  retainedRuntime.dispose(); // application teardown lands mid-sweep
  await pass;

  const receipts = await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE);
  expect(receipts.find((row) => row.operationId === "op-1")?.state).toBe("applied");
  // The operations the sweep never started remain the durable pending work a
  // later runtime retries; they are neither applied nor dropped.
  expect(receipts.find((row) => row.operationId === "op-second")?.state).toBe("pending");
  expect(receipts.find((row) => row.operationId === "op-third")?.state).toBe("pending");
  const text = (await f.drafts.load(f.key))?.text;
  expect(text).toContain('"Keep this scope."');
  expect(text).not.toContain('"second note"');
  expect(text).not.toContain('"third note"');
});

it("retries a recovery-backed handoff refused while typing once the recovery edit commits", async () => {
  let runtime: DocumentAnnotationRuntime | undefined;
  let interruptAppend = false;
  const typedText = "typed in recovery while the annotation handed off";
  const f = await fixture("refused-mid-typing-recovery-commit", {
    beforeFinalGuard: () => {
      if (!interruptAppend) return;
      interruptAppend = false;
      // The user types through the recovery edit path: markEdited's recovery
      // branch marks the write durable-unsaved, and the detached persistence
      // commits it into the recovery record through the mutation outbox.
      f.source.editText(typedText);
    },
    onStorageSuccess: () => runtime?.storageSucceeded(),
  });
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  f.source.setActiveRecoveryId("rec-1");
  runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(async () => {
    retainedRuntime.dispose();
    await retainedRuntime.whenIdle();
  });
  const operation = await acceptedOperation(f.repo, f.document, "op-2", "Second note.", f.destination);
  await f.repo.saveAnnotation(operation);
  interruptAppend = true;
  await expect(retainedRuntime.handoff(f.document, "op-2")).resolves.toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  // The interrupted typing settles into the durable recovery record on its own
  // schedule; await that commit, which is the only notification the runtime
  // gets: no reader-open, composer-open, foreground, or annotation action runs.
  await vi.waitFor(async () => {
    const committed = await f.outbox.getRecovery("rec-1");
    expect(committed?.composerText).toContain(typedText);
  });
  await retainedRuntime.whenIdle();
  const secondReference = REFERENCE.replace('"Keep this scope."', '"Second note."');
  const record = await f.outbox.getRecovery("rec-1");
  expect(record?.composerText).toBe(`${typedText}\n\n${secondReference}`);
  const applied = (
    await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)
  ).find((row) => row.operationId === "op-2");
  expect(applied).toMatchObject({ state: "applied" });
  expect(f.source.getSnapshot().text).toBe(`${typedText}\n\n${secondReference}`);
});

it("keeps recovery typing persistable when it lands after the append's final guard", async () => {
  let runtime: DocumentAnnotationRuntime | undefined;
  let typeAfterFinalGuard = false;
  const typedText = "typed in recovery after the append accepted the preparation";
  const f = await fixture("recovery-typing-after-final-guard", {
    beforeAbortableCompletion: () => {
      if (!typeAfterFinalGuard) return;
      typeAfterFinalGuard = false;
      // The user types through the recovery edit path AFTER the append's final
      // guard accepted the preparation, so the append commits and the typing
      // is queued against the pre-append token. The acknowledgment must still
      // record the append's committed revision and keep the typing persistable.
      f.source.editText(typedText);
    },
    onStorageSuccess: () => runtime?.storageSucceeded(),
  });
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  f.source.setActiveRecoveryId("rec-1");
  runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(async () => {
    retainedRuntime.dispose();
    await retainedRuntime.whenIdle();
  });
  const operation = await acceptedOperation(f.repo, f.document, "op-2", "Second note.", f.destination);
  await f.repo.saveAnnotation(operation);
  typeAfterFinalGuard = true;
  await expect(retainedRuntime.handoff(f.document, "op-2")).resolves.toMatchObject({
    state: "applied",
  });
  // The typed text persists through the append's committed revision on its
  // own schedule: no reader-open, composer-open, or foreground gesture runs,
  // and the typing's own queued write captured a token the append has left
  // behind, so only an acknowledgment that records the committed revision can
  // re-queue it.
  await vi.waitFor(async () => {
    const committed = await f.outbox.getRecovery("rec-1");
    expect(committed?.composerText).toBe(typedText);
  });
  // The interrupted source can prepare again: the pending marker cleared with
  // the re-queued write, so a later resend no longer refuses.
  const preparation = await f.source.prepareAnnotation();
  expect(preparation).not.toBeNull();
});

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
  retainedRuntime.invalidate(invalidated.lease.key, invalidated.lease);
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
  expect(retries.mock.calls.map(([reason]) => reason)).toEqual(["foreground"]); // actual empty outcome cleared prior failed-save health
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
  expect(retries.mock.calls.map(([reason]) => reason)).toEqual(["foreground"]);
  expect(saves.mock.calls.filter(([operation]) => operation.operationId === disposed.operationId)).toHaveLength(1);
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).some(
      (row) => row.operation.operationId === disposed.operationId,
    ),
  ).toBe(false);
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("already-applied");
});

// RoboRev round 37 medium: invalidate swept every failed save for the document
// by document key alone, so a reader retiring its own dead authority also
// removed a FAILED SAVE against the live replacement epoch held by another
// reader of the same document on the same runtime - and that reader's comment
// stayed unsaved until a fresh Send. Invalidation is scoped to the retired
// lease's incarnation and epoch, preserving failures against newer authority.
it("an older reader's invalidation retires only its own epoch's failed saves", async () => {
  let failSave = false;
  const f = await fixture("retry-invalidate-epoch-scoped", {
    beforeCommit: (operation) => {
      if (operation === "save" && failSave) throw new Error("save fault");
    },
  });
  await f.repo.handoff(f.document, "op-1");
  const runtime = createDocumentAnnotationRuntime(f.repo);
  cleanupAfterTest(() => runtime.dispose());
  const saves = vi.spyOn(f.repo, "saveAnnotation");

  // The older reader leases the document's first epoch and fails its save
  // while that authority is still live.
  const older = await acceptedOperation(f.repo, f.document, "older-id", "older reader note", f.destination);
  failSave = true;
  expect(await runtime.save(older)).toBeNull();

  // The document changes: the changed read fences the first epoch, and the
  // next read leases the replacement epoch - a second reader of the same
  // document on newer authority.
  const fenced = await f.repo.acceptRead(await f.repo.beginRead(f.document), docContent(`${SOURCE}CHANGED`, "rev-B"));
  if (fenced.state !== "expiry-pending") throw new Error("the changed read should fence the first epoch");
  const replacementRead = await f.repo.acceptRead(
    await f.repo.beginRead(f.document),
    docContent(`${SOURCE}CHANGED`, "rev-B"),
  );
  if (replacementRead.state !== "accepted") throw new Error("the replacement read should accept");
  const newerBase = preparedAnnotationFromLease(
    replacementRead.lease,
    "newer-id",
    "newer reader note",
    f.destination,
    "rev-B",
  );
  const newer: PreparedAnnotation = {
    ...newerBase,
    capture: {
      ...newerBase.capture,
      evidence: { wholeRevision: "rev-B", fingerprint: displayedFingerprint(`${SOURCE}CHANGED`, false) },
    },
  };
  if (newer.lease.epoch === older.lease.epoch) throw new Error("the replacement lease kept the first epoch");
  expect(await runtime.save(newer)).toBeNull();

  // The older reader retires its own dead authority. Its failed save must
  // stop retrying with it, but the newer reader's failed save against the
  // live replacement epoch must survive for the retry passes.
  runtime.invalidate(older.lease.key, older.lease);
  failSave = false;
  await runtime.retryPending("foreground");
  await runtime.whenIdle();
  expect(saves.mock.calls.filter(([operation]) => operation.operationId === older.operationId)).toHaveLength(1);
  expect(saves.mock.calls.filter(([operation]) => operation.operationId === newer.operationId)).toHaveLength(2);
  expect(
    (await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).some(
      (row) => row.operation.operationId === newer.operationId,
    ),
  ).toBe(true);
});

// Fault the real target transaction at a specific database/store/mode boundary:
// the real get is issued on the real transaction first, then that same
// transaction is aborted, and the observed abort is awaited. This is the public
// fake-indexeddb boundary, not a repository/source mock.
function faultStoreRead(
  databaseName: string,
  store: string,
  mode: IDBTransactionMode,
): { abortObserved: Promise<void>; restore: () => void } {
  const original = IDBObjectStore.prototype.get;
  let resolveAbort: (() => void) | undefined;
  const abortObserved = new Promise<void>((resolve) => {
    resolveAbort = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["get"]>
  ) {
    const request = original.apply(this, args);
    const transaction = this.transaction;
    if (this.name === store && transaction.mode === mode && transaction.db.name === databaseName) {
      transaction.addEventListener("abort", () => resolveAbort?.());
      // Abort AFTER the real get has been issued on the real transaction.
      queueMicrotask(() => {
        try {
          transaction.abort();
        } catch {
          // The transaction already settled.
        }
      });
    }
    return request;
  });
  return { abortObserved, restore: () => spy.mockRestore() };
}

// Review fix 2: a storage/preparation fault before append must not escape as a
// rejection; the handoff must return pending/owner-unavailable and keep the
// operation recoverable through the SAME retained runtime's storage-recovery path.
it.each([
  ["receipt read", ANNOTATION_OPERATIONS_STORE, "readonly"],
  ["payload read", ANNOTATION_PAYLOADS_STORE, "readonly"],
  ["source preparation", COMPOSER_DRAFTS_STORE, "readwrite"],
] as const)("recovers a saved operation whose handoff %s faults", async (_boundary, store, mode) => {
  let runtime: DocumentAnnotationRuntime | undefined;
  const f = await fixture(`handoff-fault-${store}`, { onStorageSuccess: () => runtime?.storageSucceeded() });
  // Settle the fixture's earlier op-1 first.
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");
  runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(() => retainedRuntime.dispose());
  const operation = await acceptedOperation(f.repo, f.document, "fault-id", "note", f.destination);
  if (!(await retainedRuntime.save(operation))) throw new Error("initial save failed");
  const ordinaryBefore = await f.drafts.load(f.key);
  const recoveryBefore = await f.outbox.getRecovery("rec-1");
  if (!ordinaryBefore || !recoveryBefore) throw new Error("fixture baseline rows missing");
  const payloadsBefore = await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE);
  const receiptsBefore = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  const enqueues = vi.spyOn(f.outbox, "enqueueIntent");
  const resends = vi.spyOn(f.outbox, "resendRecovery");
  // The real source must not publish while the handoff faults.
  const snapshotBefore = f.source.getSnapshot();
  const publication = vi.fn();
  const unsubscribe = f.source.subscribe(publication);
  cleanupAfterTest(() => unsubscribe());

  const fault = faultStoreRead(f.databaseName, store, mode);
  let outcome: unknown;
  try {
    outcome = await retainedRuntime.handoff(operation.lease.key, operation.operationId).then(
      (result) => result,
      (error: unknown) => error,
    );
    await fault.abortObserved;
  } finally {
    fault.restore();
  }
  // Soft so the missing automatic-recovery evidence below is still reached when
  // this defect is present.
  expect.soft(outcome).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(f.source.getSnapshot()).toEqual(snapshotBefore);
  expect(publication).not.toHaveBeenCalled();
  expect(await f.drafts.load(f.key)).toEqual(ordinaryBefore);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryBefore);
  expect(await rowsForTests<AnnotationPayloadRow>(f.factory, f.databaseName, ANNOTATION_PAYLOADS_STORE)).toEqual(
    payloadsBefore,
  );
  expect(await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).toEqual(
    receiptsBefore,
  );

  // An unrelated real successful storage operation drives the SAME runtime's
  // storage-recovery path; no explicit foreground retry is used.
  await f.repo.beginRead({ ...f.document, path: "RECOVERY.md" });
  await retainedRuntime.whenIdle();

  // Fresh complete values after recovery.
  const noteReference = REFERENCE.replace('"Keep this scope."', '"note"');
  const ordinaryRecovered = await f.drafts.load(f.key);
  const recoveryRecovered = await f.outbox.getRecovery("rec-1");
  const receiptsRecovered = await rowsForTests<AnnotationOperationRow>(
    f.factory,
    f.databaseName,
    ANNOTATION_OPERATIONS_STORE,
  );
  const applied = receiptsRecovered.filter((row) => row.operationId === operation.operationId);
  expect(applied).toHaveLength(1);
  expect(applied[0]).toMatchObject({
    state: "applied",
    draftRevision: ordinaryBefore.token.sessionRevision + 1,
  });
  expect(ordinaryRecovered).toEqual({
    ...ordinaryBefore,
    text: `${ordinaryBefore.text}\n\n${noteReference}`,
    token: { ...ordinaryBefore.token, sessionRevision: ordinaryBefore.token.sessionRevision + 1 },
    editedAt: expect.any(Number),
  });
  expectStampedNoEarlierThan(ordinaryRecovered, ordinaryBefore);
  expect(recoveryRecovered).toEqual(recoveryBefore);

  // Replay is idempotent and leaves the recovered token and rows unchanged.
  expect((await retainedRuntime.handoff(operation.lease.key, operation.operationId)).state).toBe("already-applied");
  expect(await f.drafts.load(f.key)).toEqual(ordinaryRecovered);
  expect(await f.outbox.getRecovery("rec-1")).toEqual(recoveryRecovered);
  expect(await rowsForTests<AnnotationOperationRow>(f.factory, f.databaseName, ANNOTATION_OPERATIONS_STORE)).toEqual(
    receiptsRecovered,
  );
  expect(enqueues).not.toHaveBeenCalled();
  expect(resends).not.toHaveBeenCalled();
});

it("a failed expiry fence recovers through the next storage-success notification", async () => {
  let failFence = true;
  let runtime: DocumentAnnotationRuntime | undefined;
  const f = await fixture("expiry-fence-retry", {
    beforeCommit: (operation) => {
      if (operation === "expiry" && failFence) throw new Error("fence fault");
    },
    onPendingExpiryRetained: () => runtime?.registerRetryNeed(),
  });
  runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(async () => {
    try {
      await retainedRuntime.whenIdle();
    } finally {
      retainedRuntime.dispose();
    }
  });
  // A changed read confirms expiry; the fence transaction fails, so the
  // repository retains the pending expiry and the read is expiry-pending.
  const ticket = await f.repo.beginRead(f.document);
  const fenced = await f.repo.acceptRead(ticket, docContent(SOURCE, "rev-B"));
  expect(fenced.state).toBe("expiry-pending");
  const headBefore = (await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE))[0];
  if (!headBefore) throw new Error("durable head row missing before recovery");

  // Recovery arrives ONLY through the storage-success notification: no
  // foreground event, no composer open, no other reader action.
  failFence = false;
  retainedRuntime.storageSucceeded();
  await retainedRuntime.whenIdle();
  const headAfter = (await rowsForTests<AnnotationHeadRow>(f.factory, f.databaseName, ANNOTATION_HEADS_STORE))[0];
  if (!headAfter) throw new Error("durable head row missing after recovery");
  // The pass completed the retained fence: the durable head moved off the
  // expired epoch without any reader-driven trigger.
  expect(headAfter.epoch).not.toBe(headBefore.epoch);
});

it("a storage-success notification that races a later pending handoff still schedules the pass it needs", async () => {
  let runtime: DocumentAnnotationRuntime | undefined;
  const f = await fixture("runtime-concurrent-handoffs", {
    onStorageSuccess: () => runtime?.storageSucceeded(),
  });
  runtime = createDocumentAnnotationRuntime(f.repo);
  const retainedRuntime = runtime;
  cleanupAfterTest(async () => {
    try {
      await retainedRuntime.whenIdle();
    } finally {
      retainedRuntime.dispose();
    }
  });
  expect((await f.repo.handoff(f.document, "op-1")).state).toBe("applied");

  // Two saved annotations for the same composer, both pending.
  const first = await acceptedOperation(f.repo, f.document, "op-first", "first note", f.destination);
  const second = await acceptedOperation(f.repo, f.document, "op-second", "second note", f.destination);
  await retainedRuntime.save(first);
  await retainedRuntime.save(second);

  // Both handoffs run concurrently: the composer source's preparation
  // machinery is single-flight, and concurrent handoffs to one composer used
  // to deadlock - the second handoff never returned at all. Serialized
  // handoffs give each one a fresh, settled preparation, so both references
  // apply without any external trigger.
  const [r1, r2] = await Promise.all([
    retainedRuntime.handoff(f.document, "op-first"),
    retainedRuntime.handoff(f.document, "op-second"),
  ]);
  expect(r1.state).toBe("applied");
  expect(r2.state).toBe("applied");
});
