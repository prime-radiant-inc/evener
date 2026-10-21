// @vitest-environment node

import type { DocFileContent, DocumentKey, PreparedAnnotation } from "@evener/appwire-client";
import { IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation, ComposerSourceState } from "../panes/session/composer/composerContract";
import { APPEND_STORES, browserDraftKey } from "./browserDraftRepository";
import { createDocumentAnnotationRepository, SAVE_STORES } from "./documentAnnotationIndexedDB";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_OPERATIONS_STORE,
} from "./localCompositionSchema";
import {
  type AnnotationFixture,
  type AnnotationFixtureWithRuntime,
  buildAnnotationOperation,
  deferred,
  openAnnotationFixture,
  pollUntil,
} from "./testing/annotationFixture";
import { holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";

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

// A cancellation that lands past the abort boundary leaves its committed row
// pending until the save's own completion settles and its discard runs. A
// retry during that window used to hand the row off: handoffs consulted only
// storage closure and the retry-scoped cancellation, never the operation-scoped
// dismissal, so a note the user dismissed appended anyway. The dismissal must
// fence the identity from handoff for the whole window, not just after it.
test("a retry during a cancelled save's held completion cannot append the dismissed note", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    // Every handoff prepares against the draft's current token: the sweep
    // visits the fixture's own pending operation first, and its append must not
    // make the dismissed operation's preparation stale.
    prepareAnnotation: async () => {
      if (fixture === undefined) return null;
      const loaded = await fixture.drafts.load(fixture.key);
      return loaded ? { key: loaded.key, token: loaded.token, backing: { kind: "draft" }, localGeneration: 0 } : null;
    },
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  const handoff = vi.spyOn(fixture.repository, "handoff");
  const answerFor = async (operationId: string) => {
    const index = handoff.mock.calls.findIndex((call) => call[1] === operationId);
    const result = index < 0 ? undefined : handoff.mock.results[index];
    return result === undefined ? undefined : await result.value;
  };
  // Apply the fixture's own pending operation first: a later pending operation
  // for the same draft would otherwise refuse the direct handoff below as a
  // dirty draft, which would hide the append this case is about.
  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toMatchObject({
    state: "applied",
  });

  // The save transaction commits, but its completion event is held: the row is
  // durable while the save's promise is still unsettled.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  const save = fixture.runtime.save(operation);
  await hold.reached;
  // The dismissal lands inside the commit window, past the abort boundary.
  fixture.runtime.retireAttempted(operation);

  // A handoff DURING the window, while the pending row is still visible, must
  // answer the terminal cancelled outcome instead of appending the note.
  expect(await fixture.repository.handoff(fixture.document, operation.operationId)).toEqual({ state: "cancelled" });

  // A retry pass runs in the same window and must not append it either: the
  // retained discard completes first, leaving the sweep nothing to hand off.
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();
  expect(await answerFor(operation.operationId)).toEqual({ state: "cancelled" });
  const midWindow = await fixture.repository.listAnnotations(fixture.document);
  expect(midWindow.find((view) => view.operation.operationId === operation.operationId)).toBeUndefined();

  // The held completion settles; the dismissed row must not survive it.
  hold.release();
  expect(await save).toBeNull();
  await fixture.runtime.whenIdle();

  // A later pass finds nothing to hand off and nothing left pending.
  handoff.mockClear();
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();
  expect(handoff.mock.calls.some((call) => call[1] === operation.operationId)).toBe(false);
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)).toBeUndefined();

  handoff.mockRestore();
  fixture.repository.close();
  fixture.drafts.close();
});

// The dismissal's cleanup is durable work of its own: when its transaction
// faults, the dismissed identity must stay fenced rather than become eligible
// again, and the discard must be retained for a later trigger. Best-effort
// cleanup that swallowed the fault left the pending row and no retry, so the
// dismissed operation stayed appending-eligible forever.
test("a faulted cancelled-save discard stays fenced and a later trigger completes it", async () => {
  // Count every durably attempted discard and fault the first one: storage
  // refuses the dismissal's cleanup exactly once, then accepts it.
  let discardAttempts = 0;
  let faultNext = true;
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    routeCancelledDiscardIntoRuntime: true,
    // Every handoff prepares against the draft's current token: the fixture's
    // own operation is applied first, and its append must not leave the
    // dismissed operation's preparation stale.
    prepareAnnotation: async () => {
      if (fixture === undefined) return null;
      const loaded = await fixture.drafts.load(fixture.key);
      return loaded ? { key: loaded.key, token: loaded.token, backing: { kind: "draft" }, localGeneration: 0 } : null;
    },
    beforeCommit: (operation) => {
      if (operation !== "discard") return;
      discardAttempts += 1;
      if (faultNext) {
        faultNext = false;
        throw new Error("discard fault");
      }
    },
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  // Apply the fixture's own pending operation first: a later pending operation
  // for the same draft would otherwise refuse the direct handoff below as a
  // dirty draft, which would hide the append this case is about.
  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toMatchObject({
    state: "applied",
  });
  fixture.acknowledge.mockClear();

  const hold = holdNextWriteTransaction(SAVE_STORES);
  const save = fixture.runtime.save(operation);
  await hold.reached;
  fixture.runtime.retireAttempted(operation);
  hold.release();
  expect(await save).toBeNull();
  expect(discardAttempts).toBe(1);

  // The identity stays fenced while its discard work is pending: a retry that
  // runs in that window must still refuse the dismissed note.
  expect(await fixture.repository.handoff(fixture.document, operation.operationId)).toEqual({ state: "cancelled" });
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  // The fault retained the discard as retryable work and said so.
  expect(fixture.cancelledDiscardRetained).toHaveBeenCalledTimes(1);

  // The retained work registered its need; this trigger alone must spend the
  // pass that retries and commits the discard.
  fixture.runtime.storageSucceeded();
  await fixture.runtime.whenIdle();
  expect(discardAttempts).toBe(2);
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)).toBeUndefined();

  // The committed discard cleared the fence: no later trigger re-attempts it.
  fixture.runtime.storageSucceeded();
  await fixture.runtime.whenIdle();
  expect(discardAttempts).toBe(2);

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

// ---------------------------------------------------------------------------
// The dismissal lifecycle of a saved note.
// ---------------------------------------------------------------------------

// Every handoff prepares against the draft's CURRENT token: an earlier append
// in the same test advances the session revision, and a stale fixture
// preparation would refuse revision-conflict instead of exercising the
// dismissal.
function currentPreparation(fixture: () => AnnotationFixture | undefined) {
  return async (): Promise<ComposerPreparation | null> => {
    const current = fixture();
    if (current === undefined) return null;
    const loaded = await current.drafts.load(current.key);
    return loaded ? { key: loaded.key, token: loaded.token, backing: { kind: "draft" }, localGeneration: 0 } : null;
  };
}

// Apply the fixture's own pending operation first: a later pending operation
// for the same draft would otherwise refuse a direct handoff as a dirty draft,
// which would hide the dismissal this suite is about.
async function applyFixtureOperation(fixture: AnnotationFixture): Promise<void> {
  expect(await fixture.repository.handoff(fixture.document, fixture.operation.operationId)).toMatchObject({
    state: "applied",
  });
}

// A dismissal while a handoff waits on its preparation must cancel that
// handoff. The durable flip can fault or still be in flight at this boundary,
// so the dismissal must join the eligibility predicate the handoff consults
// after preparation; the append must never issue its reads.
test("a dismissal during an outstanding preparation cancels the resumed handoff", async () => {
  const preparationStarted = deferred<void>();
  const preparationReleased = deferred<void>();
  let holdPreparation = false;
  let discardFaults = 1;
  const appendReads = vi.fn();
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeCancelledDiscardIntoRuntime: true,
    onAppendReadsIssued: appendReads,
    beforeCommit: (operation) => {
      if (operation === "discard" && discardFaults > 0) {
        discardFaults -= 1;
        throw new Error("discard fault");
      }
    },
    prepareAnnotation: async () => {
      const current = fixture;
      if (current === undefined) return null;
      if (holdPreparation) {
        preparationStarted.resolve();
        await preparationReleased.promise;
      }
      const loaded = await current.drafts.load(current.key);
      return loaded ? { key: loaded.key, token: loaded.token, backing: { kind: "draft" }, localGeneration: 0 } : null;
    },
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await applyFixtureOperation(fixture);
  appendReads.mockClear();
  fixture.acknowledge.mockClear();

  // The save commits with its completion held; the handoff passes the entry
  // guard, reads that pending receipt and waits on the composer.
  const saveHold = holdNextWriteTransaction(SAVE_STORES);
  const save = fixture.runtime.save(operation);
  await saveHold.reached;
  holdPreparation = true;
  const handoff = fixture.runtime.handoff(fixture.document, operation.operationId);
  await preparationStarted.promise;

  // The dismissal lands while the handoff waits. The durable flip faults, so
  // only the in-memory fence can protect the identity at this boundary.
  fixture.runtime.retireAttempted(operation);
  preparationReleased.resolve();
  saveHold.release();

  expect(await save).toBeNull();
  expect(await handoff).toEqual({ state: "cancelled" });
  expect(appendReads).not.toHaveBeenCalled();
  expect(fixture.acknowledge).not.toHaveBeenCalled();

  // The retained dismissal completes on the next trigger and retires the row.
  fixture.runtime.storageSucceeded();
  await fixture.runtime.whenIdle();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)).toBeUndefined();

  fixture.repository.close();
  fixture.drafts.close();
});

// A dismissal landing after the append issued its reads but before its final
// guard cannot be seen by the append's first receipt read, and its durable flip
// is serialized behind the append's own write transaction. The final guard
// must consult the same eligibility predicate and refuse.
test("a dismissal during the append's reads refuses the final commit", async () => {
  let dismissOnReads: (() => void) | undefined;
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    prepareAnnotation: currentPreparation(() => fixture),
    onAppendReadsIssued: () => dismissOnReads?.(),
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await applyFixtureOperation(fixture);
  await fixture.repository.saveAnnotation(operation);
  fixture.acknowledge.mockClear();

  dismissOnReads = () => fixture?.runtime.retireAttempted(operation);
  expect(await fixture.runtime.handoff(fixture.document, operation.operationId)).toEqual({ state: "cancelled" });
  expect(fixture.acknowledge).not.toHaveBeenCalled();

  // The queued flip retires the row once the refused append transaction ends.
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)).toBeUndefined();

  fixture.repository.close();
  fixture.drafts.close();
});

// A save can settle failed while its row is committed and retained as retry
// work. Dismissing that retained operation used to reach only the in-flight
// cancellation handle, which the settled save had already released, so the
// next pass appended the dismissed note. The dismissal must reach the durable
// row itself.
test("a dismissal of a settled-failed save keeps the next pass from appending it", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    prepareAnnotation: currentPreparation(() => fixture),
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await applyFixtureOperation(fixture);
  fixture.acknowledge.mockClear();

  // The save's transaction commits but its completion is held, so the watchdog
  // rejects a save whose row is durable: exactly the settled-failed state.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const save = fixture.runtime.save(operation);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await save).toBeNull();
  hold.release();
  vi.useRealTimers();

  // No in-flight save exists to cancel; the dismissal must still be durable.
  fixture.runtime.retireAttempted(operation);
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();

  expect(fixture.acknowledge).not.toHaveBeenCalled();
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)).toBeUndefined();

  fixture.repository.close();
  fixture.drafts.close();
});

// The in-memory fence cannot protect a closed adapter or a sibling on the same
// database. A user dismissal must flip the shared durable row terminal, so a
// fresh pair finds a terminal receipt instead of a pending note to append.
test("a dismissed save stays terminal for a sibling adapter after close", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    prepareAnnotation: currentPreparation(() => fixture),
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await applyFixtureOperation(fixture);
  await fixture.repository.saveAnnotation(operation);
  fixture.acknowledge.mockClear();

  fixture.runtime.retireAttempted(operation);

  // Observe the durable row through an adapter that shares no fence with the
  // dismissing one: listing excludes the row only when its flip committed.
  const sibling = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-coordination",
    drafts: fixture.drafts,
    targetFor: () => fixture?.source ?? null,
  });
  let durable = false;
  for (let hop = 0; hop < 50 && !durable; hop += 1) {
    const views = await sibling.listAnnotations(fixture.document);
    durable = !views.some((view) => view.operation.operationId === operation.operationId);
    if (!durable) await nextRealTask();
  }
  expect(durable).toBe(true);

  fixture.repository.close();
  expect(await sibling.handoff(fixture.document, operation.operationId)).toEqual({ state: "cancelled" });
  expect(fixture.acknowledge).not.toHaveBeenCalled();

  sibling.close();
  fixture.drafts.close();
});

// An append that commits with an unobserved completion retains its composer
// seam for reconciliation. If the user dismissed the operation, that record
// must be DROPPED: reconciling acknowledges the composer from the committed
// backing and is the one path that can still deliver the dismissed note.
test("a dismissed append with an unobserved completion is dropped, not reconciled", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: currentPreparation(() => fixture),
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await applyFixtureOperation(fixture);
  await fixture.runtime.save(operation);
  fixture.acknowledge.mockClear();

  // The append commits while its completion event is held, so the handoff
  // outlives the commit and retains its uncertain reconciliation record.
  const hold = holdNextWriteTransaction(APPEND_STORES);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const append = fixture.runtime.handoff(fixture.document, operation.operationId);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);
  expect(await append).toEqual({ state: "pending", reason: "owner-unavailable" });
  hold.release();
  vi.useRealTimers();

  // Dismissing an applied row cannot flip it, but the dismissal still owns the
  // identity: the retained record must retire without acknowledging anyone.
  fixture.runtime.retireAttempted(operation);
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();

  expect(fixture.acknowledge).not.toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// Disposal is teardown, not dismissal: it aborts the in-flight transaction it
// can still abort, but a row that already committed survives as pending work
// for whichever runtime retries after teardown. Treating teardown as a
// dismissal made the row's fate depend on the dispose-versus-close ordering.
test("dispose leaves a committed save's row pending for a later runtime", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    prepareAnnotation: currentPreparation(() => fixture),
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await applyFixtureOperation(fixture);
  fixture.acknowledge.mockClear();

  // The save commits, then teardown lands before its completion is observed.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  const save = fixture.runtime.save(operation);
  await hold.reached;
  fixture.runtime.dispose();
  hold.release();
  expect(await save).toBeNull();

  // The committed row is still pending: teardown fenced and flipped nothing.
  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operation.operationId)?.handoff.state).toBe("pending");
  const sibling = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-coordination",
    drafts: fixture.drafts,
    targetFor: () => fixture?.source ?? null,
  });
  const siblingViews = await sibling.listAnnotations(fixture.document);
  expect(siblingViews.find((view) => view.operation.operationId === operation.operationId)?.handoff.state).toBe(
    "pending",
  );
  // A later runtime can still complete the surviving work.
  expect(await sibling.handoff(fixture.document, operation.operationId)).toMatchObject({ state: "applied" });

  sibling.close();
  fixture.repository.close();
  fixture.drafts.close();
});

// The discard's commit is what may release a dismissal fence. A discard that
// no-ops because the row already became APPLIED must not silently release a
// fence that still names live protection work: the identity would reconcile
// the dismissed note into the composer on the next handoff.
test("a no-op discard for an applied row keeps the dismissal fence", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    prepareAnnotation: currentPreparation(() => fixture),
  });
  const operation: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await applyFixtureOperation(fixture);
  await fixture.repository.saveAnnotation(operation);
  expect(await fixture.repository.handoff(fixture.document, operation.operationId)).toMatchObject({ state: "applied" });
  fixture.acknowledge.mockClear();

  // A replay save is in flight when the user dismisses; its own row is already
  // applied, so the dismissal's durable discard is a no-op.
  const hold = holdNextWriteTransaction(SAVE_STORES);
  const save = fixture.runtime.save(operation);
  await hold.reached;
  fixture.runtime.retireAttempted(operation);
  hold.release();
  expect(await save).toBeNull();

  expect(await fixture.repository.handoff(fixture.document, operation.operationId)).toEqual({ state: "cancelled" });
  expect(fixture.acknowledge).not.toHaveBeenCalled();

  fixture.repository.close();
  fixture.drafts.close();
});

// A dismissal can lose the race against an append that already committed: the
// flip finds the applied row and keeps the fence, by design, because the
// repository offers no abort for committed work. The DELIVERY is still ours to
// refuse. The handoff's applied branch acknowledged the composer
// unconditionally, so the dismissed note landed in the very editor it was
// dismissed from. The row stays applied and readers are woken (both are
// true), and only the acknowledgment is withheld.
test("a dismissal that loses the append's commit race withholds the acknowledgment", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    prepareAnnotation: currentPreparation(() => fixture),
  });
  const listener = vi.fn();
  fixture.repository.subscribe(fixture.document, listener);
  fixture.acknowledge.mockClear();
  fixture.storageSuccess.mockClear();

  // The append transaction commits, but its completion event is held: the row is
  // applied while the handoff has not resumed past appendAnnotation.
  const hold = holdNextWriteTransaction(APPEND_STORES);
  const append = fixture.runtime.handoff(fixture.document, fixture.operation.operationId);
  await hold.reached;
  fixture.runtime.retireAttempted(fixture.operation);
  hold.release();

  // The append committed, so the handoff's honest answer is applied, with the
  // revision the durable draft now carries.
  const result = await append;
  expect(result).toMatchObject({ state: "applied" });
  if (result.state !== "applied") throw new Error("append did not apply");
  const draft = await fixture.drafts.load(fixture.key);
  expect(result.draftRevision).toBe(draft?.token.sessionRevision);
  // The composer never received the dismissed note.
  expect(fixture.acknowledge).not.toHaveBeenCalled();
  // The commit itself is still reported: the runtime hears the storage success
  // and readers wake for the now-applied row.
  expect(fixture.storageSuccess).toHaveBeenCalled();
  expect(listener).toHaveBeenCalled();

  // The durable row is honestly applied: an adapter sharing no fence lists it.
  const sibling = createDocumentAnnotationRepository({
    indexedDB: fixture.factory,
    databaseName: "annotation-coordination",
    drafts: fixture.drafts,
    targetFor: () => null,
  });
  const views = await sibling.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)?.handoff.state).toBe(
    "already-applied",
  );

  sibling.close();
  fixture.repository.close();
  fixture.drafts.close();
});

// The dismissal's durable flip is the event that unblocks a successor: B's
// handoff refuses behind A only because A's row is still PENDING in the session
// FIFO, and the flip removes that predecessor. The flip emitted nothing, so the
// runtime kept its recorded need with no trigger to spend it and B stayed
// pending until unrelated activity. A committed pending-to-cancelled transition
// must signal storage success after its transaction settles.
test("a committed dismissal flip starts the pass that completes a FIFO-blocked successor", async () => {
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    prepareAnnotation: currentPreparation(() => fixture),
  });
  // A is the fixture's own pending operation at sessionSequence 1; B is saved
  // after it for the same destination, so B's append refuses behind A.
  const operationB: PreparedAnnotation = { ...fixture.operation, operationId: "operation-2" };
  await fixture.runtime.save(operationB);
  expect(await fixture.runtime.handoff(fixture.document, operationB.operationId)).toEqual({
    state: "pending",
    reason: "dirty-draft",
  });

  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");
  fixture.runtime.retireAttempted(fixture.operation);

  // No further external activity: the committed flip's own signal must start the
  // pass that applies B.
  await pollUntil(() => passes.mock.calls.length > 0, 50);
  expect(passes).toHaveBeenCalledTimes(1);
  await fixture.runtime.whenIdle();

  const views = await fixture.repository.listAnnotations(fixture.document);
  expect(views.find((view) => view.operation.operationId === operationB.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  expect(views.find((view) => view.operation.operationId === fixture.operation.operationId)).toBeUndefined();

  fixture.repository.close();
  fixture.drafts.close();
});

// The flip removes A's row, so a subscribed reader's listing no longer shows it.
// Without the wake, a reader keeps rendering the dismissed note until some
// unrelated commit happens to refresh the same document. The committed flip must
// publish the document wake after its transaction settles.
test("a committed dismissal flip wakes subscribed readers", async () => {
  const fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
  });
  const listener = vi.fn();
  fixture.repository.subscribe(fixture.document, listener);
  const before = await fixture.repository.listAnnotations(fixture.document);
  expect(before.map((view) => view.operation.operationId)).toContain(fixture.operation.operationId);

  await fixture.repository.dismissOperation(fixture.document, fixture.operation.operationId);

  expect(listener).toHaveBeenCalledTimes(1);
  const after = await fixture.repository.listAnnotations(fixture.document);
  expect(after.find((view) => view.operation.operationId === fixture.operation.operationId)).toBeUndefined();

  fixture.repository.close();
  fixture.drafts.close();
});

// A dismissal flip whose completion the watchdog outlives still commits. The
// arm that later observes the already-terminal row owns the publication that
// lost completion skipped: the retained-discard retry used to read "terminal",
// publish nothing, and delete the record - the last trace of the dismissal.
// The cancelled row is the evidence of the commit, so the retry publishes the
// storage-success signal and the document wake before it drops the record.
test("a retained discard that observes an already-terminal row publishes the lost dismissal", async () => {
  let preparations = 0;
  let fixture: AnnotationFixtureWithRuntime | undefined;
  fixture = await openAnnotationFixture({
    databaseName: "annotation-coordination",
    origin: "https://coordination.example",
    runtime: true,
    routeStorageSuccessIntoRuntime: true,
    routeCancelledDiscardIntoRuntime: true,
    prepareAnnotation: async () => {
      preparations += 1;
      // The recovery pass that completes the dismissal schedules B's handoff
      // against a composer that is not ready yet; only the follow-up the lost
      // publication must start sees the current preparation.
      if (preparations === 1) return null;
      const current = fixture;
      if (current === undefined) return null;
      const loaded = await current.drafts.load(current.key);
      return loaded ? { key: loaded.key, token: loaded.token, backing: { kind: "draft" }, localGeneration: 0 } : null;
    },
  });
  // B belongs to the same session as A but another document, so only the
  // dismissal's own publication can wake A's reader; B's applied append wakes
  // its own document instead.
  const otherDocument: DocumentKey = { ...fixture.key, path: "OTHER.md" };
  const ticket = await fixture.repository.beginRead(otherDocument);
  const accepted = await fixture.repository.acceptRead(ticket, fixture.content);
  if (accepted.state !== "accepted") throw new Error(`read was not accepted: ${accepted.state}`);
  const operationB: PreparedAnnotation = {
    ...buildAnnotationOperation(accepted.lease, fixture.content),
    operationId: "operation-2",
  };
  await fixture.repository.saveAnnotation(operationB);
  const listener = vi.fn();
  fixture.repository.subscribe(fixture.document, listener);
  const passes = vi.spyOn(fixture.repository, "retryPendingWithOutcome");

  // The flip transaction commits, but its completion event is held: the
  // watchdog rejects a dismissal whose row is already terminal, so the flip's
  // own emission never runs.
  const hold = holdNextWriteTransaction([ANNOTATION_OPERATIONS_STORE]);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  fixture.runtime.retireAttempted(fixture.operation);
  await pollUntil(() => reached, 50);
  expect(reached).toBe(true);
  await vi.advanceTimersByTimeAsync(10_001);
  hold.release();
  vi.useRealTimers();
  await pollUntil(() => fixture?.cancelledDiscardRetained.mock.calls.length === 1, 50);
  expect(fixture.cancelledDiscardRetained).toHaveBeenCalledTimes(1);

  // No pass starts on its own after the lost completion. This one pass
  // completes the retained discard by observing the terminal row, and that
  // observation must publish the dismissal the lost completion skipped.
  listener.mockClear();
  await fixture.runtime.retryPending("storage-recovered");
  await fixture.runtime.whenIdle();

  expect(listener).toHaveBeenCalled();
  const views = await fixture.repository.listAnnotations(otherDocument);
  expect(views.find((view) => view.operation.operationId === operationB.operationId)?.handoff.state).toBe(
    "already-applied",
  );
  // The original pass plus the follow-up its lost publication must start.
  expect(passes).toHaveBeenCalledTimes(2);

  fixture.repository.close();
  fixture.drafts.close();
});
