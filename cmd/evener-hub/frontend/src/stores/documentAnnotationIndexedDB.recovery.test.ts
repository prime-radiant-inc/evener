// @vitest-environment node

import type {
  AcceptedSourceLease,
  DocumentKey,
  ExpiryReceipt,
  HandoffResult,
  PreparedAnnotation,
} from "@evener/appwire-client";
import { IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation } from "../panes/session/composer/composerContract";
import { APPEND_STORES } from "./browserDraftRepository";
import { ACCEPT_STORES, RESET_STORES, SAVE_STORES } from "./documentAnnotationIndexedDB";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_NAMESPACES_STORE,
  COMPOSER_DRAFTS_STORE,
} from "./localCompositionSchema";
import {
  type AnnotationFixtureWithRuntime,
  buildAnnotationOperation,
  deferred,
  holdFirstPreparation,
  openAnnotationFixture,
  openAnnotationStore,
  openSiblingRepository,
  sampleChangedContent,
  sampleThirdContent,
} from "./testing/annotationFixture";
import {
  holdIndexedDBEvent,
  holdNextTransactions,
  holdNextWriteTransaction,
  settleRealTasks,
} from "./testing/stalledIndexedDB";

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
  const sibling = openSiblingRepository(fixture);
  await sibling.beginRead(fixture.document);

  fixture.storageSuccess.mockClear();
  const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  await preparing.promise;
  // The handoff is mid-preparation. The overlapping acceptance installs its
  // provisional suppression synchronously at entry, before its first await.
  const changed = sampleChangedContent();
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
  await settleRealTasks(() => reached, 25);
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
  await settleRealTasks(() => reached, 50);
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
  const changed = sampleChangedContent();
  const acceptance = fixture.repository.acceptRead(ticket, changed);
  await settleRealTasks(() => validationHold !== undefined, 50);
  if (!validationHold) throw new Error("validation did not reach IndexedDB");
  await validationHold.reached;
  // The stalled validation transaction commits empty (the changed read's write
  // belongs to its separate fence transaction), so the handoff's reads are not
  // queued behind it.
  await settleRealTasks(() => validationSettled, 50);
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
  const holds = holdNextTransactions([{ stores: RESET_STORES, event: "abort" }]);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const reset = fixture.repository.resetOwnedNamespace(fixture.key.namespace, fixture.incarnation);
  let resetFailure: unknown;
  const resetSettled = reset.catch((error: unknown) => {
    resetFailure = error;
  });
  // The reset has ABORTED - its rollback is durable - but the held event keeps
  // the reset call from observing it, so provisional suppression is still
  // installed when the handoff runs.
  await holds.reached[0];

  const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  let handoffResult: HandoffResult | undefined;
  void handoff.then((result) => {
    handoffResult = result;
  });
  await settleRealTasks(() => handoffResult !== undefined, 100);
  expect(handoffResult).toEqual({ state: "pending", reason: "owner-unavailable" });

  await vi.advanceTimersByTimeAsync(10_001);
  await resetSettled;
  expect(resetFailure).toBeDefined();
  holds.release();
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
  await settleRealTasks(() => reached, 50);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await edit).toEqual({ state: "unavailable" });
  hold.release();
  vi.useRealTimers();

  // The late completion is the commit's observation: it publishes the changed
  // key, and the annotation adapter forwards that storage progress.
  await settleRealTasks(() => fixture.storageSuccess.mock.calls.length > 0, 50);
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
  await settleRealTasks(() => reached, 50);
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

// A reset rotates the namespace incarnation and deletes every head, so the
// durable read generation restarts at 1. An abandoned old-incarnation
// registration must not gate the new incarnation's first read: incarnation
// identities have no order, so the reset retires exactly the registrations
// that name the incarnation it rotated away. A sibling's reset must hold too:
// this adapter hears the rotation only as a best-effort namespace wake, so
// registration itself must consult the durable incarnation rather than depend
// on that wake arriving.
test.each([
  ["own", "annotation-reset-registration"],
  ["sibling's", "annotation-reset-registration-sibling"],
] as const)("a %s reset lets the new incarnation's first read register", async (resetter, databaseName) => {
  const store = await openAnnotationStore({
    databaseName,
    origin: "https://reset.example",
    targetFor: () => null,
  });
  const sibling = resetter === "own" ? undefined : openSiblingRepository(store);
  try {
    const abandoned = await store.repository.beginRead(store.document);
    expect(
      await (sibling ?? store.repository).resetOwnedNamespace(store.key.namespace, abandoned.namespaceIncarnation),
    ).toMatchObject({ committed: true });

    const fresh = await store.repository.beginRead(store.document);
    expect(fresh.namespaceIncarnation).not.toBe(abandoned.namespaceIncarnation);
    expect(await store.repository.acceptRead(fresh, sampleChangedContent())).toMatchObject({ state: "accepted" });
  } finally {
    sibling?.close();
    store.repository.close();
    store.drafts.close();
  }
});

// A blind clear at reset is unsafe: a new-incarnation read can register while
// the reset's own post-commit continuation is still pending, and retiring it
// would leave a current ticket stale. Retirement is scoped to the incarnation
// the reset actually rotated away - never a blanket clear.
test("a read registered after the reset commit survives the reset's own retirement", async () => {
  const store = await openAnnotationStore({
    databaseName: "annotation-reset-retirement-scope",
    origin: "https://reset.example",
    targetFor: () => null,
  });
  try {
    const abandoned = await store.repository.beginRead(store.document);
    // The reset commits, but its completion event - and every post-commit
    // retirement - stays held while the new incarnation's reads register.
    const hold = holdNextWriteTransaction(RESET_STORES);
    const reset = store.repository.resetOwnedNamespace(store.key.namespace, abandoned.namespaceIncarnation);
    await hold.reached;
    const first = await store.repository.beginRead(store.document);
    const current = await store.repository.beginRead(store.document);
    hold.release();
    expect(await reset).toMatchObject({ committed: true });

    expect(await store.repository.acceptRead(current, sampleChangedContent())).toMatchObject({ state: "accepted" });
    expect(await store.repository.acceptRead(first, sampleChangedContent())).toEqual({ state: "stale-ticket" });
  } finally {
    store.repository.close();
    store.drafts.close();
  }
});

// The read-state retirement has to name the incarnation the reset rotated
// away, like every other retirement. A replacement-incarnation acceptance
// installed while the reset's own completion is held must survive it: the next
// read's entry-time provisional suppression is what protects an overlapping
// handoff's append window, and losing this state reopens that window.
test("a replacement-incarnation acceptance survives the held reset's own retirement", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  const preparation = holdFirstPreparation(() => fixture);
  fixture = await openAnnotationFixture({
    databaseName: "annotation-reset-read-state-scope",
    origin: "https://reset.example",
    runtime: true,
    prepareAnnotation: preparation.prepareAnnotation,
  });
  const resetHold = holdNextWriteTransaction(RESET_STORES);
  const reset = fixture.repository.resetOwnedNamespace(fixture.key.namespace, fixture.incarnation);
  await resetHold.reached;
  try {
    // The replacement incarnation is durably current, but the reset's own
    // continuation has not run yet. Accept a read and save its operation.
    const replacement = await fixture.repository.beginRead(fixture.document);
    expect(replacement.namespaceIncarnation).not.toBe(fixture.incarnation);
    const accepted = await fixture.repository.acceptRead(replacement, fixture.content);
    if (accepted.state !== "accepted") throw new Error(`replacement read was not accepted: ${accepted.state}`);
    const operation: PreparedAnnotation = {
      ...buildAnnotationOperation(accepted.lease, fixture.content),
      operationId: "operation-2",
    };
    await fixture.repository.saveAnnotation(operation);

    resetHold.release();
    expect(await reset).toMatchObject({ committed: true });

    // The next read of changed content installs its entry-time provisional
    // from the remembered replacement-incarnation acceptance. Hold its
    // validation completion so the provisional stays unconfirmed while the
    // overlapping handoff reaches its post-preparation guard.
    const validationHold = holdNextWriteTransaction(ACCEPT_STORES);
    const changedTicket = await fixture.repository.beginRead(fixture.document);
    const handoff = fixture.runtime.handoff(fixture.document, operation.operationId);
    await preparation.preparing.promise;
    const changedRead = fixture.repository.acceptRead(changedTicket, sampleChangedContent());
    await validationHold.reached;
    preparation.releasePreparation();

    expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
    validationHold.release();
    expect(await changedRead).toMatchObject({ state: "expiry-pending" });
  } finally {
    resetHold.release();
    fixture.repository.close();
    fixture.drafts.close();
  }
});

// A validation can commit and then lose its completion while a newer beginRead
// registers. Letting the delayed acceptance write its own ticket into the
// incarnation slot displaces the newer registration's identity, so the newer
// ticket's changed read skips its entry-time provisional and an overlapping
// handoff appends into the dying epoch. The delayed completion carries the
// newest durable evidence and must be merged into the newer identity instead.
test("a delayed acceptance carries its evidence into the newer registration", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  const preparation = holdFirstPreparation(() => fixture);
  fixture = await openAnnotationFixture({
    databaseName: "annotation-accept-registration-order",
    origin: "https://coordination.example",
    runtime: true,
    prepareAnnotation: preparation.prepareAnnotation,
  });
  try {
    // The earlier acceptance's validation commits, but its continuation stays
    // held while a newer read registers.
    const validationHold = holdNextWriteTransaction(ACCEPT_STORES);
    const earlierTicket = await fixture.repository.beginRead(fixture.document);
    const earlierAcceptance = fixture.repository.acceptRead(earlierTicket, fixture.content);
    await validationHold.reached;
    const laterTicket = await fixture.repository.beginRead(fixture.document);
    validationHold.release();
    expect(await earlierAcceptance).toMatchObject({ state: "accepted" });

    // The newer ticket's changed read installs its entry-time provisional from
    // the remembered acceptance. An overlapping handoff must refuse against it,
    // so it cannot append while the newer read's validation is in flight.
    const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
    await preparation.preparing.promise;
    const changedValidation = holdNextWriteTransaction(ACCEPT_STORES);
    const changedRead = fixture.repository.acceptRead(laterTicket, sampleChangedContent());
    await changedValidation.reached;
    preparation.releasePreparation();

    expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
    changedValidation.release();
    expect(await changedRead).toMatchObject({ state: "expiry-pending" });
  } finally {
    fixture.repository.close();
    fixture.drafts.close();
  }
});

// The same ordering rule holds on the fenced path. A fence commits and loses
// its completion while a newer beginRead registers; the delayed fence's read
// state must not displace the newer ticket's identity either, or the newer
// ticket's changed read loses the entry-time provisional that protects the
// replacement epoch's append window.
test("a delayed fence carries its evidence into the newer registration", async () => {
  let fenceHold: ReturnType<typeof holdNextWriteTransaction> | undefined;
  const fenceInstalled = deferred<void>();
  let fixture: AnnotationFixtureWithRuntime | undefined;
  const preparation = holdFirstPreparation(() => fixture);
  fixture = await openAnnotationFixture({
    databaseName: "annotation-fence-registration-order",
    origin: "https://coordination.example",
    runtime: true,
    beforeExpiryFence: async () => {
      if (!fenceHold) {
        fenceHold = holdNextWriteTransaction(ACCEPT_STORES);
        fenceInstalled.resolve();
      }
    },
    prepareAnnotation: preparation.prepareAnnotation,
  });
  try {
    const earlierTicket = await fixture.repository.beginRead(fixture.document);
    const earlierAcceptance = fixture.repository.acceptRead(earlierTicket, sampleChangedContent());
    await fenceInstalled.promise;
    if (!fenceHold) throw new Error("changed read did not reach its fence");
    await fenceHold.reached;
    const laterTicket = await fixture.repository.beginRead(fixture.document);
    fenceHold.release();
    const fenced = await earlierAcceptance;
    if (fenced.state !== "expiry-pending" || fenced.receipt === undefined) {
      throw new Error(`changed read did not fence: ${fenced.state}`);
    }

    // An operation saved on the replacement lease is what the provisional
    // suppression protects while the newer ticket's changed read validates.
    const replacementLease = {
      key: fixture.document,
      namespaceIncarnation: fenced.receipt.namespaceIncarnation,
      epoch: fenced.receipt.replacementEpoch,
      headRevision: 1,
      displayedSnapshotId: "fenced-snapshot",
    };
    const operation: PreparedAnnotation = {
      ...buildAnnotationOperation(replacementLease, sampleChangedContent()),
      operationId: "operation-2",
    };
    await fixture.repository.saveAnnotation(operation);

    const handoff = fixture.runtime.handoff(fixture.document, operation.operationId);
    await preparation.preparing.promise;
    const changedValidation = holdNextWriteTransaction(ACCEPT_STORES);
    const thirdContent = sampleThirdContent();
    const changedRead = fixture.repository.acceptRead(laterTicket, thirdContent);
    await changedValidation.reached;
    preparation.releasePreparation();

    expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
    changedValidation.release();
    expect(await changedRead).toMatchObject({ state: "expiry-pending" });
  } finally {
    fixture.repository.close();
    fixture.drafts.close();
  }
});

// A reset provisions unconfirmed suppression for the leases it is about to
// delete, and an in-flight changed acceptance may install its own provisional
// for the same epoch. When the reset does NOT commit, its release must name the
// entry it provisioned: releasing by identity alone deletes the live
// acceptance's provisional and reopens the append window it protects.
test("a non-committing reset releases only the provisional it provisioned", async () => {
  const resetAttempted = deferred<void>();
  let fixture: AnnotationFixtureWithRuntime | undefined;
  const preparation = holdFirstPreparation(() => fixture);
  fixture = await openAnnotationFixture({
    databaseName: "annotation-reset-provisional-owner",
    origin: "https://reset.example",
    runtime: true,
    beforeCommit: (operation) => {
      if (operation !== "reset") return;
      resetAttempted.resolve();
      throw new Error("reset fault");
    },
    prepareAnnotation: preparation.prepareAnnotation,
  });
  try {
    // The overlapping handoff passes its entry guard and waits mid-preparation
    // BEFORE the reset provisions.
    const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
    await preparation.preparing.promise;

    // The reset provisions suppression and its transaction aborts, but the
    // abort event is held so its release path has not run yet. The changed
    // acceptance's validation is held in the same window.
    const holds = holdNextTransactions([
      { stores: RESET_STORES, event: "abort" },
      { stores: ACCEPT_STORES, event: "complete" },
    ]);
    const reset = fixture.repository.resetOwnedNamespace(fixture.key.namespace, fixture.incarnation);
    await resetAttempted.promise;
    await holds.reached[0];

    // The changed acceptance installs its own provisional for the same epoch
    // and holds its validation completion, so that provisional stays
    // unconfirmed while the reset's abort resolves.
    const changedTicket = await fixture.repository.beginRead(fixture.document);
    const changedAcceptance = fixture.repository.acceptRead(changedTicket, sampleChangedContent());
    await holds.reached[1];

    // The reset releases while the acceptance's provisional is unconfirmed.
    holds.releaseAt(0);
    await expect(reset).rejects.toThrow("reset fault");
    preparation.releasePreparation();

    expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
    holds.release();
    expect(await changedAcceptance).toMatchObject({ state: "expiry-pending" });
  } finally {
    fixture.repository.close();
    fixture.drafts.close();
  }
});

// Two changed reads of the same accepted epoch overlap. The first installs the
// provisional that protects its in-flight validation. A second, later read's
// entry install used to replace that entry; when its validation answers stale,
// its release then deleted the first invocation's protection. The displaced
// owner's protection must survive the second invocation's release.
test("a stale overlapping acceptance keeps the earlier provisional alive", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  const preparation = holdFirstPreparation(() => fixture);
  fixture = await openAnnotationFixture({
    databaseName: "annotation-entry-provisional-owner",
    origin: "https://coordination.example",
    runtime: true,
    prepareAnnotation: preparation.prepareAnnotation,
  });
  const sibling = openSiblingRepository(fixture);
  try {
    // The overlapping handoff passes its entry guard and waits mid-preparation
    // before either changed read installs suppression.
    const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
    await preparation.preparing.promise;

    // T0's changed validation commits but its completion is held, so its
    // provisional is installed and still unconfirmed.
    const firstHold = holdNextWriteTransaction(ACCEPT_STORES);
    const firstTicket = await fixture.repository.beginRead(fixture.document);
    const firstAcceptance = fixture.repository.acceptRead(firstTicket, sampleChangedContent());
    await firstHold.reached;

    // T1 installs its own entry over T0's, then a sibling's read supersedes T1
    // durably without touching this adapter's remembered registration, so T1's
    // validation can only answer stale.
    const secondTicket = await fixture.repository.beginRead(fixture.document);
    await sibling.beginRead(fixture.document);
    expect(await fixture.repository.acceptRead(secondTicket, sampleChangedContent())).toEqual({
      state: "stale-ticket",
    });

    // T0's provisional must still protect the window T1's release touched.
    preparation.releasePreparation();

    expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
    firstHold.release();
    expect(await firstAcceptance).toMatchObject({ state: "expiry-pending" });
  } finally {
    sibling.close();
    fixture.repository.close();
    fixture.drafts.close();
  }
});

function replacementLease(key: DocumentKey, receipt: ExpiryReceipt, headRevision: number): AcceptedSourceLease {
  return {
    key,
    namespaceIncarnation: receipt.namespaceIncarnation,
    epoch: receipt.replacementEpoch,
    headRevision,
    displayedSnapshotId: `snapshot-${receipt.replacementEpoch}`,
  };
}

// Introduce the ordering inversion the merge must rank away: a newer changed
// read's fence commits first, a third ticket registers, then the fence's held
// completion lands and an older unchanged acceptance's held completion follows
// it into the same slot. Returns an operation saved on the fence's live
// replacement lease, so the caller can assert which epoch the next changed
// read's entry-time provisional protects.
async function seedRegressedReadState(databaseName: string) {
  const fixture = await openAnnotationFixture({
    databaseName,
    origin: "https://coordination.example",
  });
  const holds = holdNextTransactions([
    { stores: ACCEPT_STORES, event: "complete" },
    { stores: ACCEPT_STORES, event: "complete" },
    { stores: ACCEPT_STORES, event: "complete" },
  ]);
  try {
    const olderTicket = await fixture.repository.beginRead(fixture.document);
    const olderAcceptance = fixture.repository.acceptRead(olderTicket, fixture.content);
    await holds.reached[0];

    const newerTicket = await fixture.repository.beginRead(fixture.document);
    const newerAcceptance = fixture.repository.acceptRead(newerTicket, sampleChangedContent());
    await holds.reached[1];
    holds.releaseAt(1);
    await holds.reached[2];

    const thirdTicket = await fixture.repository.beginRead(fixture.document);
    holds.releaseAt(2);
    const fenced = await newerAcceptance;
    if (fenced.state !== "expiry-pending" || fenced.receipt === undefined) {
      throw new Error(`changed read did not fence: ${fenced.state}`);
    }
    const operation: PreparedAnnotation = {
      ...buildAnnotationOperation(replacementLease(fixture.document, fenced.receipt, 2), sampleChangedContent()),
      operationId: "operation-live",
    };
    await fixture.repository.saveAnnotation(operation);

    holds.releaseAt(0);
    expect(await olderAcceptance).toMatchObject({ state: "accepted" });
    return { fixture, ticket: thirdTicket, operation };
  } finally {
    holds.release();
  }
}

// Two held completions can resume newer-then-older. The older acceptance is
// older durable evidence and must not overwrite the newer fence's lease, or the
// next changed read suppresses the dead epoch while an operation on the live
// epoch appends into the changed read's window.
test("a delayed older acceptance does not regress the newer fence's read state", async () => {
  const seeded = await seedRegressedReadState("annotation-read-state-rank");
  const { fixture, ticket, operation } = seeded;
  try {
    const changedHold = holdNextWriteTransaction(ACCEPT_STORES);
    const changedRead = fixture.repository.acceptRead(ticket, sampleThirdContent());
    await changedHold.reached;
    expect(await fixture.repository.handoff(fixture.document, operation.operationId)).toEqual({
      state: "pending",
      reason: "owner-unavailable",
    });
    changedHold.release();
    expect(await changedRead).toMatchObject({ state: "expiry-pending" });
  } finally {
    fixture.repository.close();
    fixture.drafts.close();
  }
});

// The fenced path has the same rule: two fences lose their completions and
// resume newer-then-older. The older fence's replacement epoch is older durable
// evidence and must not overwrite the newer fence's lease.
test("delayed fences released out of order keep the newer replacement lease", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-fence-read-state-rank",
    origin: "https://coordination.example",
  });
  const holds = holdNextTransactions([
    { stores: ACCEPT_STORES, event: "complete" },
    { stores: ACCEPT_STORES, event: "complete" },
    { stores: ACCEPT_STORES, event: "complete" },
    { stores: ACCEPT_STORES, event: "complete" },
  ]);
  try {
    const firstTicket = await fixture.repository.beginRead(fixture.document);
    const firstAcceptance = fixture.repository.acceptRead(firstTicket, sampleChangedContent());
    await holds.reached[0];
    holds.releaseAt(0);
    await holds.reached[1];

    const secondTicket = await fixture.repository.beginRead(fixture.document);
    const secondAcceptance = fixture.repository.acceptRead(secondTicket, sampleThirdContent());
    await holds.reached[2];
    holds.releaseAt(2);
    await holds.reached[3];

    const thirdTicket = await fixture.repository.beginRead(fixture.document);
    holds.releaseAt(3);
    const newerFence = await secondAcceptance;
    if (newerFence.state !== "expiry-pending" || newerFence.receipt === undefined) {
      throw new Error(`second changed read did not fence: ${newerFence.state}`);
    }
    const operation: PreparedAnnotation = {
      ...buildAnnotationOperation(replacementLease(fixture.document, newerFence.receipt, 3), sampleThirdContent()),
      operationId: "operation-live",
    };
    await fixture.repository.saveAnnotation(operation);

    holds.releaseAt(1);
    expect(await firstAcceptance).toMatchObject({ state: "expiry-pending" });
    holds.release();

    const changedHold = holdNextWriteTransaction(ACCEPT_STORES);
    const changedRead = fixture.repository.acceptRead(thirdTicket, fixture.content);
    await changedHold.reached;
    expect(await fixture.repository.handoff(fixture.document, operation.operationId)).toEqual({
      state: "pending",
      reason: "owner-unavailable",
    });
    changedHold.release();
    expect(await changedRead).toMatchObject({ state: "expiry-pending" });
  } finally {
    holds.release();
    fixture.repository.close();
    fixture.drafts.close();
  }
});

// The revert route: with the slot regressed, a content revert compares
// "unchanged" against the older acceptance's observations and installs no
// provisional, while the durable evidence says changed. The local verdict must
// match the durable one, so the live epoch stays protected.
test("a reverted content read keeps the live epoch's provisional after a delayed older acceptance", async () => {
  const seeded = await seedRegressedReadState("annotation-read-state-revert");
  const { fixture, ticket, operation } = seeded;
  try {
    const changedHold = holdNextWriteTransaction(ACCEPT_STORES);
    const changedRead = fixture.repository.acceptRead(ticket, fixture.content);
    await changedHold.reached;
    expect(await fixture.repository.handoff(fixture.document, operation.operationId)).toEqual({
      state: "pending",
      reason: "owner-unavailable",
    });
    changedHold.release();
    expect(await changedRead).toMatchObject({ state: "expiry-pending" });
  } finally {
    fixture.repository.close();
    fixture.drafts.close();
  }
});
