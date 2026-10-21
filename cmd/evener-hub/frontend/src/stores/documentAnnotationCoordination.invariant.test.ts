// @vitest-environment node

import type { DocFileContent, DocumentKey, PreparedAnnotation } from "@evener/appwire-client";
import { IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation, ComposerSourceState } from "../panes/session/composer/composerContract";
import { APPEND_STORES, browserDraftKey } from "./browserDraftRepository";
import { createDocumentAnnotationRepository, SAVE_STORES } from "./documentAnnotationIndexedDB";
import { ANNOTATION_EVIDENCE_STORE, ANNOTATION_EXPIRY_STORE } from "./localCompositionSchema";
import {
  type AnnotationFixture,
  type AnnotationFixtureWithRuntime,
  buildAnnotationOperation,
  deferred,
  openAnnotationFixture,
  pollUntil,
} from "./testing/annotationFixture";
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// A save is work whose outcome the retry runtime cannot see until it settles.
// A storage success arriving while its transaction is in flight names the
// commit that save's later failure may leave claimable: the row is durable but
// the completion event - and the wake it publishes - is exactly what the
// watchdog outlives. Retain that trigger across the save and spend it when the
// save settles with a recorded need, so the receipt and the reader wake no
// longer wait for unrelated activity.
test("a storage success during an outstanding save starts the recovery pass its failure needs", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  const listener = vi.fn();
  fixture.repository.subscribe(fixture.document, listener);

  // The save transaction commits, but its completion event is held: the row is
  // durable while the save (and the wake it would publish) still waits.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const save = fixture.runtime.save(operation);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);

  // A drafts commit publishes while the save is outstanding. The annotation
  // adapter routes that publication to the runtime as storage progress.
  const draft = await fixture.drafts.load(fixture.key);
  if (!draft) throw new Error("fixture seeded no draft");
  await fixture.drafts.commitEdit(fixture.key, draft.token, { text: "typing", skillNames: [] }, () => true);

  // The watchdog rejects the save and its catch records the retry need.
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await save).toBeNull();
  hold.release();
  vi.useRealTimers();

  // No further external trigger: the retained storage success must start the
  // pass whose replay of the durable row publishes the lost wake.
  await fixture.runtime.whenIdle();
  expect(listener).toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// Cancellation reaches a save transaction only while it can still abort. A
// dismissal landing after the transaction passed that boundary cannot stop the
// commit, and the runtime's success path never checked the token, so the
// durable pending row survived and a later pass handed a user-dismissed note
// off to the composer. The repository observes its own token after the
// transaction settles and discards the row it committed.
test("a save cancelled after its commit boundary leaves no pending operation", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };

  // The save transaction commits, but its completion event is held: the
  // cancellation lands inside the commit window, past the abort boundary.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  const save = fixture.runtime.save(operation);
  await hold.reached;
  fixture.runtime.retireAttempted(operation);
  hold.release();

  // A cancelled attempt is the handled dismissal the catch path already
  // defines, even though this attempt committed.
  expect(await save).toBeNull();

  // A later pass must not hand the dismissed note off.
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)).toBeUndefined();

  fixture.repository.close();
  fixture.drafts.close();
});

// Every durable identity is namespace-qualified: the operation row's key is
// [kind, origin, operationId]. The reconciliation sweep's handled set compared
// bare operation IDs, so two namespaces that both minted "operation-1" made
// one namespace's reconciliation skip the other's pending handoff for the rest
// of the pass. A second namespace with the same operation ID proves the sweep
// must key by the qualified identity: reconciling A must leave B eligible in
// the SAME pass.
test("a reconciled append does not skip a same-id pending operation in another namespace", async () => {
  const secondOrigin = "https://second.example";
  const secondKey = browserDraftKey("root", secondOrigin);
  const secondDocument: DocumentKey = { ...secondKey, path: "NOTES.md" };
  let secondPreparation: ComposerPreparation | null = null;
  const secondSource = {
    ref: "root",
    sourceId: "source-2",
    alive: true,
    subscribeRecoveryPersisted: () => () => undefined,
    prepareAnnotation: async () => secondPreparation,
    ownsPreparation: () => true,
    acknowledgeAnnotation: vi.fn(),
  } as unknown as ComposerSourceState;
  let fixture: AnnotationFixture | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    targetFor: (_destination, key) =>
      key.namespace.kind === "browser" && key.namespace.origin === secondOrigin
        ? secondSource
        : (fixture?.source ?? null),
  });

  // The other namespace's pending operation reuses the same operation ID.
  const seeded = await fixture.drafts.seedDraft(secondKey, { text: "", skillNames: [] });
  if (seeded.state !== "committed") throw new Error(`second draft seed was not committed: ${seeded.state}`);
  secondPreparation = { key: secondKey, token: seeded.draft.token, backing: { kind: "draft" }, localGeneration: 0 };
  const ticket = await fixture.repository.beginRead(secondDocument);
  const accepted = await fixture.repository.acceptRead(ticket, fixture.content);
  if (accepted.state !== "accepted") throw new Error(`second read was not accepted: ${accepted.state}`);
  const operationB = buildAnnotationOperation(accepted.lease, fixture.content, {
    kind: "browser-origin",
    paneId: "pane-2",
    compositionOwnerId: "source-2",
  });
  expect(operationB.operationId).toBe(fixture.operation.operationId);
  await fixture.repository.saveAnnotation(operationB);

  // A's append commits unobserved: its receipt is applied and its uncertain
  // record retained while the completion event stays held.
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const appendA = fixture.repository.handoff(fixture.document, fixture.operation.operationId);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await appendA).toEqual({ state: "pending", reason: "owner-unavailable" });
  hold.release();
  vi.useRealTimers();

  // ONE pass reconciles A first, then sweeps the persisted pending handoffs.
  await fixture.repository.retryPending("storage-recovered");
  const views = await fixture.repository.listAnnotations(secondDocument);
  expect(views.find((view) => view.operation.operationId === operationB.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});

// The adapter's seam treats every drafts-repository commit as storage
// progress: the drafts subscription is the one channel by which a commit that
// advances a composer's durable token wakes the retry runtime. Every other
// drafts write publishes; appendAnnotation was the only one that did not, so
// the seam's stated invariant was false for appends.
test("an applied append publishes its drafts commit to the repository subscription", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
  });
  const published = vi.fn();
  fixture.drafts.subscribe(published);

  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toMatchObject({
    state: "applied",
  });
  expect(published).toHaveBeenCalledWith(
    expect.objectContaining({
      namespace: expect.objectContaining({ origin: fixture.key.namespace.origin }),
      sessionRef: fixture.key.sessionRef,
    }),
  );

  fixture.repository.close();
  fixture.drafts.close();
});

// Reconciliation acknowledges the composer from the committed backing, and
// that acknowledgment is exactly the moment a handoff refused against the
// pre-append token becomes claimable again. Outside a pass nothing told the
// runtime: the applied receipt and the reader wake are not storage progress.
// An out-of-pass ordering shows the gap: A commits unobserved, B refuses
// against A's stale composer token, and a direct retry of A reconciles. The
// reconciliation's own signal must start the pass that applies B, with no
// other external trigger.
test("a reconciliation's acknowledgment signals the runtime so a refused handoff recovers", async () => {
  const composer: { preparation: ComposerPreparation | null } = { preparation: null };
  const fixture: AnnotationFixtureWithRuntime = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: async () => composer.preparation,
    // A real composer adopts the committed token a reconciliation hands it.
    acknowledgeAnnotation: (preparation, token) => {
      composer.preparation = { ...preparation, token };
    },
  });
  composer.preparation = fixture.preparation;
  const operationB: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await fixture.runtime.save(operationB);
  fixture.storageSuccess.mockClear();

  // A's append transaction commits, but its completion event is held: the
  // receipt is applied and the composer's durable token advanced while the
  // handoff waits on an event the watchdog outlives.
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
  expect(await appendA).toEqual({ state: "pending", reason: "owner-unavailable" });
  hold.release();
  vi.useRealTimers();

  // B prepares against the composer's pre-append token, which A's committed
  // append advanced durably, so B refuses.
  expect(await fixture.runtime.handoff(fixture.document, operationB.operationId)).toEqual({
    state: "pending",
    reason: "revision-conflict",
  });

  // A direct retry of A reconciles the unobserved append: the acknowledgment
  // adopts the committed token, which is the moment B becomes claimable.
  expect(await fixture.runtime.handoff(fixture.document, fixture.operation.operationId)).toMatchObject({
    state: "already-applied",
  });
  expect(fixture.storageSuccess).toHaveBeenCalled();
  // No other trigger follows: the reconciliation's signal must start the pass.
  await fixture.runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operationB.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});

// ---------------------------------------------------------------------------
// The invariant walk: one latch across every kind of outstanding work.
// ---------------------------------------------------------------------------

// Each pair drives the same trigger into one kind of outstanding work and
// settles that work both ways. The latch retains the trigger either way; only
// a settle that leaves work pending spends it. retryPendingWithOutcome is the
// pass entry point, so its call count is the number of passes scheduled.
test("a trigger during a pass that settles clean is dropped", async () => {
  const preparing = deferred<void>();
  const released = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: async () => {
      preparations += 1;
      if (preparations === 1) {
        preparing.resolve();
        await released.promise;
      }
      return fixture?.preparation ?? null;
    },
  });
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");

  const pass = fixture.runtime.retryPending("composer-open");
  await preparing.promise;
  fixture.runtime.storageSucceeded();
  released.resolve();
  await pass;
  await fixture.runtime.whenIdle();

  expect(passes).toHaveBeenCalledTimes(1);
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});

test("a trigger during a pass that leaves work pending runs exactly one follow-up", async () => {
  const preparing = deferred<void>();
  const released = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: async () => {
      preparations += 1;
      // The first sweep skips the operation: the pass leaves work pending.
      if (preparations === 1) {
        preparing.resolve();
        await released.promise;
        return null;
      }
      return fixture?.preparation ?? null;
    },
  });
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");

  const pass = fixture.runtime.retryPending("composer-open");
  await preparing.promise;
  fixture.runtime.storageSucceeded();
  released.resolve();
  await pass;
  await fixture.runtime.whenIdle();

  // The follow-up is the spend of the trigger retained during the first pass.
  expect(passes).toHaveBeenCalledTimes(2);
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});

test("a trigger during a clean direct handoff starts no pass", async () => {
  const preparing = deferred<void>();
  const released = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: async () => {
      preparations += 1;
      if (preparations === 1) {
        preparing.resolve();
        await released.promise;
      }
      return fixture?.preparation ?? null;
    },
  });
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");

  const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  await preparing.promise;
  fixture.runtime.storageSucceeded();
  released.resolve();

  expect(await handoff).toMatchObject({ state: "applied" });
  await fixture.runtime.whenIdle();
  expect(passes).not.toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

test("a trigger during a direct handoff that refuses starts exactly one pass", async () => {
  const preparing = deferred<void>();
  const released = deferred<void>();
  let preparations = 0;
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: async () => {
      preparations += 1;
      // The handoff refuses: it leaves work pending for the retained trigger.
      if (preparations === 1) {
        preparing.resolve();
        await released.promise;
        return null;
      }
      return fixture?.preparation ?? null;
    },
  });
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");

  const handoff = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  await preparing.promise;
  fixture.runtime.storageSucceeded();
  released.resolve();

  expect(await handoff).toEqual({ state: "pending", reason: "owner-unavailable" });
  await fixture.runtime.whenIdle();
  expect(passes).toHaveBeenCalledTimes(1);
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  fixture.repository.close();
  fixture.drafts.close();
});

test("a trigger during a clean save starts no pass", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");

  const hold = holdNextWriteTransaction(SAVE_STORES);
  const save = fixture.runtime.save(operation);
  await hold.reached;
  fixture.runtime.storageSucceeded();
  hold.release();

  expect(await save).not.toBeNull();
  await fixture.runtime.whenIdle();
  expect(passes).not.toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

test("a trigger retained across an outstanding save never schedules after dispose", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");

  const hold = holdNextWriteTransaction(SAVE_STORES);
  const save = fixture.runtime.save(operation);
  await hold.reached;
  fixture.runtime.storageSucceeded();
  fixture.runtime.dispose();
  hold.release();

  expect(await save).toBeNull();
  await fixture.runtime.whenIdle();
  expect(passes).not.toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// ---------------------------------------------------------------------------
// The invariant walk: every eligibility-restoring release reaches the trigger.
// ---------------------------------------------------------------------------

// A recorded need plus an eligibility-restoring event must never end
// unscheduled. Each case below installs one kind of provisional suppression,
// registers the need the runtime records when a handoff refuses, releases the
// suppression through that path, and asserts the release itself reached the
// runtime trigger and started exactly one pass.

function changedContent(): DocFileContent {
  return {
    text: "Changed content\n",
    binary: false,
    mediaType: "text/markdown",
    truncated: false,
    sizeBytes: 17,
  };
}

test("a stale validation's release reaches the runtime trigger", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");
  const ticket = await fixture.repository.beginRead(fixture.document);
  // A newer read in another adapter supersedes this ticket durably: its
  // validation can only answer stale, and that release restores eligibility.
  const sibling = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-coordination",
    drafts: fixture.drafts,
    targetFor: () => null,
  });
  await sibling.beginRead(fixture.document);
  fixture.storageSuccess.mockClear();
  fixture.runtime.registerRetryNeed();

  expect(await fixture.repository.acceptRead(ticket, changedContent())).toEqual({ state: "stale-ticket" });
  await pollUntil(() => passes.mock.calls.length > 0, 50);
  expect(fixture.storageSuccess).toHaveBeenCalled();
  await fixture.runtime.whenIdle();
  expect(passes).toHaveBeenCalledTimes(1);

  sibling.close();
  fixture.repository.close();
  fixture.drafts.close();
});

test("an unchanged acceptance's release reaches the runtime trigger", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");
  // Another adapter proves a change, rotating the durable epoch. This
  // adapter's in-memory observations still hold the old content, so its next
  // acceptance installs a provisional by local comparison; the durable
  // validation of the fresh epoch finds no change and accepts, releasing it.
  const sibling = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-coordination",
    drafts: fixture.drafts,
    targetFor: () => null,
  });
  const siblingTicket = await sibling.beginRead(fixture.document);
  expect(await sibling.acceptRead(siblingTicket, changedContent())).toMatchObject({ state: "expiry-pending" });
  const ticket = await fixture.repository.beginRead(fixture.document);
  fixture.storageSuccess.mockClear();
  fixture.runtime.registerRetryNeed();

  expect((await fixture.repository.acceptRead(ticket, changedContent())).state).toBe("accepted");
  await pollUntil(() => passes.mock.calls.length > 0, 50);
  expect(fixture.storageSuccess).toHaveBeenCalled();
  await fixture.runtime.whenIdle();
  expect(passes).toHaveBeenCalledTimes(1);

  sibling.close();
  fixture.repository.close();
  fixture.drafts.close();
});

test("a validation fault's release reaches the runtime trigger", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");
  const ticket = await fixture.repository.beginRead(fixture.document);
  // The changed read installs its provisional at entry; the evidence scan then
  // throws, and the fault path releases that still-owned entry.
  const getAll = IDBObjectStore.prototype.getAll;
  let faultArmed = true;
  const getAllSpy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["getAll"]>
  ) {
    const scope = [...this.transaction.objectStoreNames];
    if (faultArmed && this.name === ANNOTATION_EVIDENCE_STORE && scope.includes(ANNOTATION_EXPIRY_STORE)) {
      faultArmed = false;
      throw new Error("validation fault");
    }
    return getAll.apply(this, args);
  });
  fixture.storageSuccess.mockClear();
  fixture.runtime.registerRetryNeed();

  await expect(fixture.repository.acceptRead(ticket, changedContent())).rejects.toThrow("validation fault");
  getAllSpy.mockRestore();
  await pollUntil(() => passes.mock.calls.length > 0, 50);
  expect(fixture.storageSuccess).toHaveBeenCalled();
  await fixture.runtime.whenIdle();
  expect(passes).toHaveBeenCalledTimes(1);

  fixture.repository.close();
  fixture.drafts.close();
});

test("a reset rollback's release reaches the runtime trigger", async () => {
  let faultArmed = false;
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    beforeCommit: (operation) => {
      if (operation === "reset" && faultArmed) throw new Error("reset fault");
    },
  });
  faultArmed = true;
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");
  fixture.storageSuccess.mockClear();
  fixture.runtime.registerRetryNeed();

  // The reset provisions its provisional entries before the fault; the
  // rollback releases them after the transaction settles.
  await expect(fixture.repository.resetOwnedNamespace(fixture.key.namespace, fixture.incarnation)).rejects.toThrow(
    "reset fault",
  );
  await pollUntil(() => passes.mock.calls.length > 0, 50);
  expect(fixture.storageSuccess).toHaveBeenCalled();
  await fixture.runtime.whenIdle();
  expect(passes).toHaveBeenCalledTimes(1);

  fixture.repository.close();
  fixture.drafts.close();
});

test("a stale-incarnation reset's release reaches the runtime trigger", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
  });
  // A sibling rotates the namespace first, so this adapter's expected
  // incarnation is stale and its reset rotates nothing; the entries it
  // provisioned are released as an aborted reset's are.
  const sibling = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-coordination",
    drafts: fixture.drafts,
    targetFor: () => null,
  });
  const rotated = await sibling.resetOwnedNamespace(fixture.key.namespace, fixture.incarnation);
  expect(rotated.committed).toBe(true);
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");
  fixture.storageSuccess.mockClear();
  fixture.runtime.registerRetryNeed();

  expect(await fixture.repository.resetOwnedNamespace(fixture.key.namespace, fixture.incarnation)).toEqual({
    committed: false,
    incarnation: rotated.incarnation,
  });
  await pollUntil(() => passes.mock.calls.length > 0, 50);
  expect(fixture.storageSuccess).toHaveBeenCalled();
  await fixture.runtime.whenIdle();
  expect(passes).toHaveBeenCalledTimes(1);

  sibling.close();
  fixture.repository.close();
  fixture.drafts.close();
});
