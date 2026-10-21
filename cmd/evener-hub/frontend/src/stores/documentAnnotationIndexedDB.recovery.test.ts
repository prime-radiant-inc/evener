// @vitest-environment node

import type { DocFileContent, HandoffResult, PreparedAnnotation } from "@evener/appwire-client";
import { IDBDatabase, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation } from "../panes/session/composer/composerContract";
import { APPEND_STORES } from "./browserDraftRepository";
import { createDocumentAnnotationRepository, RESET_STORES, SAVE_STORES } from "./documentAnnotationIndexedDB";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_NAMESPACES_STORE,
  COMPOSER_DRAFTS_STORE,
} from "./localCompositionSchema";
import {
  type AnnotationFixtureWithRuntime,
  deferred,
  openAnnotationFixture,
  pollUntil,
} from "./testing/annotationFixture";
import { holdIndexedDBEvent, holdNextWriteTransaction } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// A changed read installs its provisional suppression at entry and releases it
// only when its durable validation answers. A handoff overlapping that window
// refuses against the provisional restriction and returns pending, recording
// the runtime's retry need. A validation superseded by another adapter proves
// nothing about the change, so it releases the restriction - and that release
// is the only event that can spend the recorded need. Without a storage-success
// signal at that boundary no pass starts, and the eligible annotation waits for
// unrelated activity.
test("a stale validation that releases provisional suppression signals the pending handoff", async () => {
  const preparing = deferred<void>();
  const preparationReleased = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-recovery",
    origin: "https://recovery.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: async () => {
      preparations += 1;
      // The first handoff finishes preparation while the overlapping
      // acceptance's provisional suppression is active, so it refuses.
      if (preparations === 1) {
        preparing.resolve();
        await preparationReleased.promise;
        return null;
      }
      return fixture?.preparation ?? null;
    },
  });

  // This adapter issues a newer read, so the acceptance below is the invocation
  // that owns the provisional suppression its local comparison installs.
  const ticket = await fixture.repository.beginRead(fixture.document);
  // Another adapter's read supersedes that ticket durably: the validation can
  // only answer stale, which releases the provisional entry.
  const sibling = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-recovery",
    drafts: fixture.drafts,
    targetFor: () => null,
  });
  await sibling.beginRead(fixture.document);

  fixture.storageSuccess.mockClear();
  const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  await preparing.promise;
  // The handoff is mid-preparation. The overlapping acceptance installs its
  // provisional suppression synchronously at entry, before its first await.
  const changed: DocFileContent = {
    text: "Changed content\n",
    binary: false,
    mediaType: "text/markdown",
    truncated: false,
    sizeBytes: 17,
  };
  const acceptance = fixture.repository.acceptRead(ticket, changed);
  preparationReleased.resolve();

  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  expect(await acceptance).toEqual({ state: "stale-ticket" });
  // No further external activity: the release of the provisional suppression
  // must be what starts the pass that completes the refused handoff.
  await fixture.runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  expect(fixture.storageSuccess).toHaveBeenCalled();

  sibling.close();
  fixture.repository.close();
  fixture.drafts.close();
});

// A save can commit while its completion stays unobserved: the watchdog
// rejects before saveAnnotation reaches the wake it publishes only after the
// transaction wrapper settles. The runtime retains the failed attempt, and its
// retry finds the operation already durable, so the ordinary replay publishes
// no wake either. For an ownerless destination no
// later append supplies the missing notification, and subscribed readers stay
// stale despite the successful recovery. The durable row the replay confirms is
// the only cross-adapter evidence of that commit, so the replay must publish
// the invalidation the lost attempt could not.
test("a save replay whose first completion went unobserved wakes subscribed readers", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-recovery",
    origin: "https://recovery.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    destination: { kind: "browser-readonly-session", sourcePaneId: "pane-1" },
  });
  const operation: PreparedAnnotation = {
    ...fixture.operation,
    operationId: "operation-2",
    destination: { kind: "browser-readonly-session", sourcePaneId: "pane-1" },
  };
  const listener = vi.fn();
  fixture.repository.subscribe(fixture.document, listener);

  // The next save transaction commits, but its completion event is held: the
  // write is durable while everything awaiting the event still waits.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const firstSave = fixture.runtime.save(operation);
  await pollUntil(() => reached, 25);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);

  // The save reported failure and retained its operation; the commit it could
  // not observe never reached the wake publication.
  expect(await firstSave).toBeNull();
  expect(listener).not.toHaveBeenCalled();
  hold.release();
  vi.useRealTimers();

  // Recovery: the retained attempt replays against the durable row, and the
  // ownerless destination settles its handoff without an append of its own.
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)?.handoff.state).toBe("pending");
  expect(listener).toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// Recovery from an append whose completion went unobserved is a two-operation
// story. The committed append A advances the composer's durable token while the
// live composer still holds its pre-append one, so the persisted pending handoff
// B - swept FIRST - refuses revision-conflict against that stale token.
// Reconciliation then acknowledges A, and that acknowledgment is exactly the
// moment the composer becomes ready for B again. Reconciliation answers an
// applied receipt and publishes its wake to readers, not a storage-success
// signal to the retry runtime, so a sweep that reconciles after it already
// visited B stops with B pending and nothing left to trigger a revisit. The
// sweep must reconcile the uncertain appends it retained BEFORE it visits the
// persisted pending handoffs, so the composer those handoffs prepare against is
// the reconciled one.
test("an unobserved append reconciles before the pending sweep so a later handoff applies", async () => {
  const composer: { preparation: ComposerPreparation | null } = { preparation: null };
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-recovery",
    origin: "https://recovery.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: async () => composer.preparation,
    // A real composer adopts the committed token a reconciliation hands it:
    // that adoption is what makes a later handoff's preparation current again.
    acknowledgeAnnotation: (preparation, token) => {
      composer.preparation = { ...preparation, token };
    },
  });
  composer.preparation = fixture.preparation;
  const operationB: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await fixture.runtime.save(operationB);
  fixture.storageSuccess.mockClear();

  // A's next append transaction commits, but its completion event is held: the
  // applied receipt and the composer's new durable token exist while the
  // handoff that produced them waits on an event the watchdog outlives.
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const appendA = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);

  // The append is durable but unobserved: the handoff retains its composer seam
  // and answers pending, exactly the state reconciliation exists to resolve.
  expect(await appendA).toEqual({ state: "pending", reason: "owner-unavailable" });
  hold.release();
  vi.useRealTimers();

  // ONE recovery pass, with no external trigger after it. B must come out of the
  // same pass applied: reconciliation makes its preparation current before the
  // sweep reaches it.
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();

  // Reconciliation ran - the composer now carries the token A committed - so the
  // same pass can apply B.
  expect(fixture.acknowledge).toHaveBeenCalled();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operationB.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});

// A changed read installs its provisional suppression at entry and releases it
// on the way out of a validation fault. That release RESTORES the old epoch's
// eligibility: an overlapping handoff that refused pending during the
// suppression can append again, and the release is the only event that proves
// it. The cleanup used to remove the entry silently, so the runtime kept its
// recorded need but nothing started the pass that could spend it, and the
// handoff waited for unrelated activity. The release now signals the same
// storage-success boundary a stale validation does - after the validation
// transaction settles, never inside it.
test("an overlapping handoff completes automatically after a validation fault releases provisional suppression", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-recovery",
    origin: "https://recovery.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  const ticket = await fixture.repository.beginRead(fixture.document);
  fixture.storageSuccess.mockClear();

  // Hold the validation's own first read so the overlapping handoff observes
  // the provisional suppression while the validation is still in flight. The
  // request's success event has already arrived when `reached` resolves; only
  // its delivery to the awaiting body is held.
  const get = IDBObjectStore.prototype.get;
  let validationHold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let validationSettled = false;
  const getSpy = vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["get"]>
  ) {
    const request = get.apply(this, args);
    const scope = [...this.transaction.objectStoreNames];
    if (
      !validationHold &&
      this.name === ANNOTATION_NAMESPACES_STORE &&
      scope.includes(ANNOTATION_EVIDENCE_STORE) &&
      scope.includes(ANNOTATION_EXPIRY_STORE)
    ) {
      this.transaction.addEventListener(
        "complete",
        () => {
          validationSettled = true;
        },
        { once: true },
      );
      this.transaction.addEventListener(
        "abort",
        () => {
          validationSettled = true;
        },
        { once: true },
      );
      validationHold = holdIndexedDBEvent(request, "success");
    }
    return request;
  });
  // The validation faults after its held read: the evidence scan's request
  // creation throws, which rejects the body and aborts the transaction. The
  // changed content has already installed the provisional suppression at entry.
  const getAll = IDBObjectStore.prototype.getAll;
  const getAllSpy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["getAll"]>
  ) {
    const scope = [...this.transaction.objectStoreNames];
    if (this.name === ANNOTATION_EVIDENCE_STORE && scope.includes(ANNOTATION_EXPIRY_STORE)) {
      throw new Error("validation fault");
    }
    return getAll.apply(this, args);
  });
  const changed: DocFileContent = {
    text: "Changed content\n",
    binary: false,
    mediaType: "text/markdown",
    truncated: false,
    sizeBytes: 17,
  };
  const acceptance = fixture.repository.acceptRead(ticket, changed);
  await pollUntil(() => validationHold !== undefined, 50);
  if (!validationHold) throw new Error("validation did not reach IndexedDB");
  await validationHold.reached;
  // The stalled validation transaction commits empty (the changed read's write
  // belongs to its separate fence transaction), so the handoff's reads are not
  // queued behind it.
  await pollUntil(() => validationSettled, 50);
  expect(validationSettled).toBe(true);

  const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });

  // The fault releases the suppression; no external trigger follows.
  validationHold.release();
  let acceptanceFailed = false;
  await acceptance.catch(() => {
    acceptanceFailed = true;
  });
  expect(acceptanceFailed).toBe(true);
  await fixture.runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  expect(fixture.storageSuccess).toHaveBeenCalled();

  getSpy.mockRestore();
  getAllSpy.mockRestore();
  fixture.repository.close();
  fixture.drafts.close();
});

// Reset also installs provisional suppression before its first await so an
// in-flight handoff cancels instead of appending behind the wiped namespace.
// An ABORTED reset rolls nothing back into eligibility until its transaction
// settles, and that rollback releases the suppression; the release used to be
// silent, so a handoff that refused pending during the suppression stayed
// pending with its retry need recorded and nothing to spend it. The rollback
// now notifies the runtime after the reset transaction settles, in the same
// eligibility-restoration voice as the stale-validation release.
test("an overlapping handoff completes automatically after an aborted namespace reset releases provisional suppression", async () => {
  let faultArmed = false;
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-recovery",
    origin: "https://recovery.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    // The reset case aborts its transaction at this commit boundary, which is
    // the rollback whose release this suite covers.
    beforeCommit: (operation) => {
      if (operation === "reset" && faultArmed) throw new Error("reset fault");
    },
  });
  faultArmed = true;
  fixture.storageSuccess.mockClear();

  // Hold the aborted reset's abort event: the rollback is real, but the reset
  // call cannot observe it until the watchdog's deadline, which keeps the
  // provisional suppression installed while the handoff refuses against it.
  const transact = IDBDatabase.prototype.transaction;
  let resetHold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  const transactSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args: Parameters<IDBDatabase["transaction"]>
  ) {
    const transaction = transact.apply(this, args);
    const scope = [...transaction.objectStoreNames];
    if (
      !resetHold &&
      transaction.mode === "readwrite" &&
      scope.length === RESET_STORES.length &&
      RESET_STORES.every((store) => scope.includes(store))
    ) {
      resetHold = holdIndexedDBEvent(transaction, "abort");
    }
    return transaction;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const reset = fixture.repository.resetOwnedNamespace(fixture.key.namespace, fixture.incarnation);
  let resetFailure: unknown;
  const resetSettled = reset.catch((error: unknown) => {
    resetFailure = error;
  });
  await pollUntil(() => resetHold !== undefined, 100);
  if (!resetHold) throw new Error("reset transaction did not reach IndexedDB");
  // The reset has ABORTED - its rollback is durable - but the held event keeps
  // the reset call from observing it, so provisional suppression is still
  // installed when the handoff runs.
  await resetHold.reached;

  const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  let handoffResult: HandoffResult | undefined;
  void handoff.then((result) => {
    handoffResult = result;
  });
  await pollUntil(() => handoffResult !== undefined, 100);
  expect(handoffResult).toEqual({ state: "pending", reason: "owner-unavailable" });

  await vi.advanceTimersByTimeAsync(10_001);
  await resetSettled;
  expect(resetFailure).toBeDefined();
  resetHold.release();
  transactSpy.mockRestore();
  vi.useRealTimers();

  // No external trigger follows the rollback's release.
  await fixture.runtime.whenIdle();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  expect(fixture.storageSuccess).toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// The drafts repository's write pattern published from its own post-await
// continuation, so a watchdog that outlived a held completion rejected the
// write without publishing the commit it actually made. The annotation
// adapter subscribes to those commits to hear storage progress; a committed
// draft edit whose completion was lost left that subscription silent. The
// transaction runner's late-completion seam publishes the same keys once the
// completion lands.
test("a drafts write whose completion was lost publishes its commit to subscribers", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-recovery",
    origin: "https://recovery.example",
  });
  const published = vi.fn();
  fixture.drafts.subscribe(published);
  fixture.storageSuccess.mockClear();
  const draft = await fixture.drafts.load(fixture.key);
  if (!draft) throw new Error("fixture seeded no draft");

  // The edit transaction commits, but its completion event is held: the
  // watchdog rejects the write while the new text is already durable.
  const hold = holdNextWriteTransaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE]);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const edit = fixture.drafts.commitEdit(fixture.key, draft.token, { text: "lost", skillNames: [] }, () => true);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await edit).toEqual({ state: "unavailable" });
  hold.release();
  vi.useRealTimers();

  // The late completion is the commit's observation: it publishes the changed
  // key, and the annotation adapter forwards that storage progress.
  await pollUntil(() => fixture.storageSuccess.mock.calls.length > 0, 50);
  expect(published).toHaveBeenCalledWith(expect.objectContaining({ sessionRef: fixture.key.sessionRef }));
  expect(fixture.storageSuccess).toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// Teardown past the save transaction's abort boundary lets the write commit.
// The catch threw SaveCancelledError without publishing, while the success
// path's teardown branch publishes the commit it found. For an OWNERLESS
// destination no later handoff supplies the missing publication either: the
// only re-observation is #settleUnclaimed, which is deliberately silent
// because a pending-to-unclaimed settle changes no reader's listing. The
// committed row is the save's evidence, so the catch announces it.
test("a save that commits past abort during teardown announces its ownerless row", async () => {
  const origin = "https://recovery.example";
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-recovery",
    origin,
    runtime: true,
    destination: { kind: "browser-readonly-session", sourcePaneId: "pane-1" },
  });
  const operation: PreparedAnnotation = {
    ...fixture.operation,
    operationId: "operation-2",
    destination: { kind: "browser-readonly-session", sourcePaneId: "pane-1" },
  };
  const listener = vi.fn();
  fixture.repository.subscribe(fixture.document, listener);
  fixture.storageSuccess.mockClear();

  // The save transaction commits, but its completion event is held: teardown
  // lands past the point where the abort could roll it back.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const save = fixture.runtime.save(operation);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);
  fixture.runtime.dispose();
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await save).toBeNull();
  hold.release();
  vi.useRealTimers();

  expect(listener).toHaveBeenCalled();
  expect(fixture.storageSuccess).toHaveBeenCalled();

  // The trace: the ownerless destination's later handoff re-observes the row
  // only through the deliberately silent unclaimed settle.
  listener.mockClear();
  fixture.storageSuccess.mockClear();
  expect(await fixture.repository.handoff(fixture.document, operation.operationId)).toEqual({
    state: "pending",
    reason: "owner-unavailable",
  });
  expect(listener).not.toHaveBeenCalled();
  expect(fixture.storageSuccess).not.toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});
