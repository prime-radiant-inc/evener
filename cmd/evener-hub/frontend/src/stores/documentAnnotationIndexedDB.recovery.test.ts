// @vitest-environment node

import {
  type DestinationProvenance,
  type DocFileContent,
  type DocumentKey,
  displayedFingerprint,
  type HandoffResult,
  type PreparedAnnotation,
} from "@evener/appwire-client";
import { IDBDatabase, IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation, ComposerSourceState } from "../panes/session/composer/composerContract";
import { MemoryStorage } from "../storageTestUtils";
import {
  APPEND_STORES,
  type BrowserDraftKey,
  type BrowserDraftRepository,
  browserDraftKey,
  createBrowserDraftRepository,
} from "./browserDraftRepository";
import {
  type BrowserDocumentAnnotationRepository,
  createDocumentAnnotationRepository,
} from "./documentAnnotationIndexedDB";
import { createDocumentAnnotationRuntime, type DocumentAnnotationRuntime } from "./documentAnnotationRuntime";
import {
  ANNOTATION_EVIDENCE_STORE,
  ANNOTATION_EXPIRY_STORE,
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  ANNOTATION_PAYLOADS_STORE,
  ANNOTATION_SEQUENCES_STORE,
} from "./localCompositionSchema";
import { holdIndexedDBEvent, holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

interface FixtureOptions {
  prepareAnnotation?: () => Promise<ComposerPreparation | null>;
  destination?: DestinationProvenance;
  // A fault injected at a commit boundary: the reset case aborts its transaction
  // there, which is the rollback whose release this suite covers.
  beforeCommit?: (operation: "acceptRead" | "save" | "expiry" | "deleteExpiry" | "reset") => void;
  // A real composer adopts the committed token a reconciliation hands it: that
  // adoption is what makes a later handoff's preparation current again.
  acknowledgeAnnotation?: (preparation: ComposerPreparation, token: ComposerPreparation["token"]) => void;
}

interface Fixture {
  factory: IDBFactory;
  repository: BrowserDocumentAnnotationRepository;
  drafts: BrowserDraftRepository;
  runtime: DocumentAnnotationRuntime;
  acknowledge: ReturnType<typeof vi.fn>;
  storageSuccess: ReturnType<typeof vi.fn>;
  key: BrowserDraftKey;
  document: DocumentKey;
  // The namespace incarnation the fixture's first read was issued under: the
  // reset rollback regression resets exactly this one.
  incarnation: string;
  content: DocFileContent;
  operation: PreparedAnnotation;
  preparation: ComposerPreparation;
}

async function openFixture(options: FixtureOptions = {}): Promise<Fixture> {
  const factory = new IDBFactory();
  const drafts = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: "annotation-recovery",
    storage: new MemoryStorage(),
  });
  const key = browserDraftKey("root", "https://recovery.example");
  // Seed before the repository subscribes to drafts, so setup publishes no
  // storage-success notification of its own.
  const seeded = await drafts.seedDraft(key, { text: "", skillNames: [], commandNames: [] });
  if (seeded.state !== "committed") throw new Error(`draft seed was not committed: ${seeded.state}`);
  const preparation: ComposerPreparation = {
    key,
    token: seeded.draft.token,
    backing: { kind: "draft" },
    localGeneration: 0,
  };
  const acknowledge = vi.fn((preparation: ComposerPreparation, token: ComposerPreparation["token"]) => {
    options.acknowledgeAnnotation?.(preparation, token);
  });
  const source = {
    ref: "root",
    sourceId: "source-1",
    alive: true,
    subscribeRecoveryPersisted: () => () => undefined,
    prepareAnnotation: options.prepareAnnotation ?? (async () => preparation),
    ownsPreparation: () => true,
    acknowledgeAnnotation: acknowledge,
  } as unknown as ComposerSourceState;
  let runtime: DocumentAnnotationRuntime | undefined;
  // Production wiring: the repository's post-commit storage-success boundary is
  // the runtime's recovery trigger, so a notification the repository never
  // sends is one the runtime never sees.
  const storageSuccess = vi.fn(() => {
    runtime?.storageSucceeded();
  });
  const repository = createDocumentAnnotationRepository({
    indexedDB: factory,
    databaseName: "annotation-recovery",
    drafts,
    targetFor: () => source,
    beforeCommit: options.beforeCommit,
    onStorageSuccess: storageSuccess,
  });
  runtime = createDocumentAnnotationRuntime(repository);
  const document: DocumentKey = { ...key, path: "NOTES.md" };
  const content: DocFileContent = {
    text: "A note with an [image 1] marker\n",
    binary: false,
    mediaType: "text/markdown",
    truncated: false,
    sizeBytes: 29,
  };
  const ticket = await repository.beginRead(document);
  const accepted = await repository.acceptRead(ticket, content);
  if (accepted.state !== "accepted") throw new Error(`read was not accepted: ${accepted.state}`);
  const operation: PreparedAnnotation = {
    operationId: "operation-1",
    lease: accepted.lease,
    capture: {
      evidence: {
        wholeRevision: content.revision,
        fingerprint: displayedFingerprint(content.text, content.truncated),
      },
      sourceRange: { start: 0, end: 1 },
      start: { line: 1, column: 1 },
      endInclusive: { line: 1, column: 1 },
      selectedRenderedText: "A",
      contextBefore: "",
      contextAfter: "",
      mappingId: "mapping-1",
      displayedSnapshotId: accepted.lease.displayedSnapshotId,
    },
    note: "note",
    destination: options.destination ?? { kind: "browser-origin", paneId: "pane-1", compositionOwnerId: "source-1" },
  };
  await repository.saveAnnotation(operation);
  return {
    factory,
    repository,
    drafts,
    runtime,
    acknowledge,
    storageSuccess,
    key,
    document,
    incarnation: ticket.namespaceIncarnation,
    content,
    operation,
    preparation,
  };
}

// A changed read installs its provisional suppression at entry and releases it
// only when its durable validation answers. A handoff overlapping that window
// refuses against the provisional restriction and returns pending, recording
// the runtime's retry need. A validation superseded by another adapter proves
// nothing about the change, so it releases the restriction - and that release
// is the only event that can spend the recorded need. Without a storage-success
// signal at that boundary no pass starts, and the eligible annotation waits for
// unrelated activity.
test("a stale validation that releases provisional suppression signals the pending handoff", async () => {
  let markPreparing: () => void = () => undefined;
  const preparing = new Promise<void>((resolve) => {
    markPreparing = resolve;
  });
  let releasePreparation: () => void = () => undefined;
  const preparationReleased = new Promise<void>((resolve) => {
    releasePreparation = resolve;
  });
  let preparations = 0;
  let fixture: Fixture | undefined;
  fixture = await openFixture({
    prepareAnnotation: async () => {
      preparations += 1;
      // The first handoff finishes preparation while the overlapping
      // acceptance's provisional suppression is active, so it refuses.
      if (preparations === 1) {
        markPreparing();
        await preparationReleased;
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
  await preparing;
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
  releasePreparation();

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
// retry finds the operation already durable - newlySaved stays false - so the
// ordinary replay publishes no wake either. For an ownerless destination no
// later append supplies the missing notification, and subscribed readers stay
// stale despite the successful recovery. The durable row the replay confirms is
// the only cross-adapter evidence of that commit, so the replay must publish
// the invalidation the lost attempt could not.
test("a save replay whose first completion went unobserved wakes subscribed readers", async () => {
  const fixture = await openFixture({
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
  const hold = holdNextWriteTransaction([
    ANNOTATION_NAMESPACES_STORE,
    ANNOTATION_HEADS_STORE,
    ANNOTATION_EVIDENCE_STORE,
    ANNOTATION_PAYLOADS_STORE,
    ANNOTATION_OPERATIONS_STORE,
    ANNOTATION_SEQUENCES_STORE,
  ]);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let reached = false;
  void hold.reached.then(() => {
    reached = true;
  });
  const firstSave = fixture.runtime.save(operation);
  for (let i = 0; i < 25 && !reached; i += 1) await nextRealTask();
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
  const fixture = await openFixture({
    prepareAnnotation: async () => composer.preparation,
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
  for (let i = 0; i < 50 && !reached; i += 1) await nextRealTask();
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
  const fixture = await openFixture();
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
  for (let i = 0; i < 50 && !validationHold; i += 1) await nextRealTask();
  if (!validationHold) throw new Error("validation did not reach IndexedDB");
  await validationHold.reached;
  // The stalled validation transaction commits empty (the changed read's write
  // belongs to its separate fence transaction), so the handoff's reads are not
  // queued behind it.
  for (let i = 0; i < 50 && !validationSettled; i += 1) await nextRealTask();
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
  const fixture = await openFixture({
    beforeCommit: (operation) => {
      if (operation === "reset" && faultArmed) throw new Error("reset fault");
    },
  });
  faultArmed = true;
  fixture.storageSuccess.mockClear();

  // Hold the aborted reset's abort event: the rollback is real, but the reset
  // call cannot observe it until the watchdog's deadline, which keeps the
  // provisional suppression installed while the handoff refuses against it.
  const resetStores = [
    ANNOTATION_NAMESPACES_STORE,
    ANNOTATION_HEADS_STORE,
    ANNOTATION_EVIDENCE_STORE,
    ANNOTATION_PAYLOADS_STORE,
    ANNOTATION_OPERATIONS_STORE,
    ANNOTATION_SEQUENCES_STORE,
    ANNOTATION_EXPIRY_STORE,
  ];
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
      scope.length === resetStores.length &&
      resetStores.every((store) => scope.includes(store))
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
  for (let i = 0; i < 100 && !resetHold; i += 1) await nextRealTask();
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
  for (let i = 0; i < 100 && handoffResult === undefined; i += 1) await nextRealTask();
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
