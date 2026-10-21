// @vitest-environment node

import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation } from "../panes/session/composer/composerContract";
import { createDocumentAnnotationRepository } from "./documentAnnotationIndexedDB";
import { ANNOTATION_OPERATIONS_STORE, ANNOTATION_PAYLOADS_STORE } from "./localCompositionSchema";
import {
  type AnnotationFixture,
  deferred,
  openAnnotationFixture,
  sampleChangedContent,
} from "./testing/annotationFixture";
import { holdNextTransactionEvent } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
});

// The adapter arms one recovery-persisted subscription per composer source
// when a handoff resolves that source. close() must release it: a surviving
// composer source otherwise keeps the closed repository alive and still
// invokes its storage-success callback after closure.
test("close releases the handoff's recovery-persisted subscription", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-close",
    origin: "https://close.example",
    trackRecoveryListeners: true,
    seedDraft: false,
  });

  // The handoff resolves the source and arms its recovery notification before
  // preparing the append; a null preparation ends the handoff without
  // touching the composer.
  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(fixture.listeners.size).toBe(1);

  fixture.storageSuccess.mockClear();
  for (const listener of [...fixture.listeners]) listener();
  expect(fixture.storageSuccess).toHaveBeenCalledTimes(1);

  fixture.repository.close();
  // A late notification from the surviving source cannot reach the closed
  // repository's storage-success callback.
  for (const listener of [...fixture.listeners]) listener();
  expect(fixture.storageSuccess).toHaveBeenCalledTimes(1);
  expect(fixture.listeners.size).toBe(0);
  fixture.drafts.close();
});

// close() must fence work, not only release listeners: a handoff awaiting its
// preparation read would otherwise resume after closure, append through the
// retained composer, and notify storage success on a torn-down repository.
test("close while a handoff's preparation read is outstanding stops the resumed handoff", async () => {
  const preparationStarted = deferred<void>();
  const preparation = deferred<ComposerPreparation | null>();
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-close",
    origin: "https://close.example",
    trackRecoveryListeners: true,
    seedDraft: true,
    prepareAnnotation: () => {
      preparationStarted.resolve();
      return preparation.promise;
    },
  });
  if (!fixture.preparation) throw new Error("fixture seeded no preparation");

  const handoff = fixture.repository.handoff(fixture.document, fixture.operation.operationId);
  await preparationStarted.promise;
  expect(fixture.subscribeCount()).toBe(1);
  fixture.storageSuccess.mockClear();
  fixture.repository.close();
  preparation.resolve(fixture.preparation);

  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  expect(fixture.subscribeCount()).toBe(1);
  expect(fixture.listeners.size).toBe(0);
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  // The append never ran: the retained draft keeps its pre-annotation text.
  expect((await fixture.drafts.load(fixture.preparation.key))?.text).toBe("");
  fixture.drafts.close();
});

// The arm site is after the destination resolves and before the preparation
// read; closure at that boundary must register no recovery listener.
test("close before a handoff arms its listener registers nothing", async () => {
  let fixture: AnnotationFixture | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-close",
    origin: "https://close.example",
    trackRecoveryListeners: true,
    seedDraft: false,
    onTarget: () => fixture?.repository.close(),
  });
  expect(fixture.subscribeCount()).toBe(0);

  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(fixture.subscribeCount()).toBe(0);
  expect(fixture.listeners.size).toBe(0);
  fixture.drafts.close();
});

// The append's own reads are its last asynchronous boundary before the final
// guard; closure while they are outstanding must stop the commit.
test("close during the append's reads stops the final commit", async () => {
  let fixture: AnnotationFixture | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-close",
    origin: "https://close.example",
    trackRecoveryListeners: true,
    seedDraft: true,
    onAppendReadsIssued: () => fixture?.repository.close(),
  });
  if (!fixture.preparation) throw new Error("fixture seeded no preparation");

  fixture.storageSuccess.mockClear();
  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  expect((await fixture.drafts.load(fixture.preparation.key))?.text).toBe("");
  fixture.drafts.close();
});

// A changed read validates in one transaction and fences in a second, with an
// observation boundary between them. Closure at that boundary must stop the
// fence before it opens a connection again: a resumed fence would rewrite the
// durable head after application teardown. The refusal is a stale ticket, not
// a retained expiry: this adapter holds no retry work past close().
test("close between a changed read's validation and its expiry fence stops the fence", async () => {
  const fenceReached = deferred<void>();
  const fenceGate = deferred<void>();
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-close",
    origin: "https://close.example",
    trackRecoveryListeners: true,
    seedDraft: false,
    beforeExpiryFence: () => {
      fenceReached.resolve();
      return fenceGate.promise;
    },
  });
  const changed = sampleChangedContent();
  const ticket = await fixture.repository.beginRead(fixture.document);
  const acceptance = fixture.repository.acceptRead(ticket, changed);
  await fenceReached.promise;
  fixture.storageSuccess.mockClear();
  fixture.repository.close();
  fenceGate.resolve();

  expect(await acceptance).toEqual({ state: "stale-ticket" });
  // No post-close notification and no retained work this closed adapter could
  // never retry.
  expect(fixture.pendingExpiryRetained).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();
  // No fence landed: a fresh adapter still reads the head the first acceptance
  // established, not the replacement epoch a resumed fence would have written.
  const probe = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-close",
    drafts: fixture.drafts,
    targetFor: () => null,
  });
  const read = await probe.beginRead(fixture.document);
  expect(read.headRevision).toBe(fixture.operation.lease.headRevision);
  probe.close();
  fixture.drafts.close();
});

// The runtime's retry pass sweeps persisted work before the separate outcome
// scan opens its own connection. Closure landing while the pass is outstanding
// must reach that scan too: the scan's promise keeps the caller's retry-needed
// state alive, so it rejects as a storage refusal instead of reopening the
// closed adapter and reading through it.
test("close during an outstanding retry pass stops the outcome scan reopening", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-close",
    origin: "https://close.example",
    trackRecoveryListeners: true,
    seedDraft: true,
  });
  const open = vi.spyOn(fixture.factory, "open");
  // The pass's pending-operation read is its last transaction before the scan.
  const hold = holdNextTransactionEvent(
    [ANNOTATION_OPERATIONS_STORE, ANNOTATION_PAYLOADS_STORE],
    "complete",
    "readonly",
  );
  const retry = fixture.repository.retryPendingWithOutcome("storage-recovered");
  await hold.reached;
  fixture.repository.close();
  const opensAtClose = open.mock.calls.length;
  expect(opensAtClose).toBe(0);
  hold.release();

  await expect(retry).rejects.toThrow("Document annotation repository is closed");
  expect(open).toHaveBeenCalledTimes(opensAtClose);
  fixture.drafts.close();
});
